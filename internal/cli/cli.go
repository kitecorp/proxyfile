// Package cli is the proxyfile program: its commands, flags, messages and exit codes.
package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/render"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

const usage = `usage: proxyfile render [<file>] -o <output> [--target envoy] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile check [<file>] -o <output> [--target envoy] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile schema
       proxyfile version
`

// defaultNames are the files render and check look for in the working directory when no file is given, in the order
// the messages list them.
var defaultNames = []string{"Proxyfile", "proxyfile.conf", "proxyfile.yaml", "proxyfile.yml"}

// targets are the proxies --target names, in the order a message lists them. The ones with a generator are those
// format.Targets declares, envoy alone in this version, and the others are refused by name rather than given an Envoy
// file for a gateway meant for them.
var targets = []string{"envoy", "nginx", "haproxy"}

// The exit codes: done, a problem in the input or a difference check found, and wrong usage.
const (
	exitDone    = 0
	exitProblem = 1
	exitUsage   = 2
)

// Run is the whole program but the process: it returns the exit code.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch command := args[0]; command {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitDone
	case "version":
		fmt.Fprintf(stdout, "proxyfile %s\n", version)
		return exitDone
	case "schema":
		data, err := format.Schema(format.Proxyfile)
		if err == nil {
			// A write that fails, on a full disk or a closed pipe, ends the command with 1, as a failed write of render
			// does, so a script never takes a cut-off schema for a whole one.
			_, err = stdout.Write(data)
		}
		if err != nil {
			fmt.Fprintf(stderr, "proxyfile schema: %v\n", err)
			return exitProblem
		}
		return exitDone
	case "render", "check":
		req, err := request(command, args[1:])
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitDone
		}
		if err != nil {
			// The flag package writes an argument it does not know as it is given, which may hold a newline.
			fmt.Fprintf(stderr, "proxyfile %s: %s\n", command, model.OneLine(err.Error()))
			return exitUsage
		}
		files, problems, err := outputs(req)
		if err != nil {
			fmt.Fprintf(stderr, "proxyfile %s: %v\n", command, err)
			return exitUsage
		}
		if problems != nil {
			for _, problem := range problems {
				fmt.Fprintln(stderr, problem)
			}
			return exitProblem
		}
		if command == "render" {
			return write(files, stderr)
		}
		return compare(files, stderr)
	default:
		fmt.Fprintf(stderr, "proxyfile: unknown command %q\n%s", command, usage)
		return exitUsage
	}
}

// request reads the arguments of render and check. Go's flag package stops at the first argument that is not a flag,
// so the file may come before the flags only if each positional argument is taken off and the rest parsed again. With
// no file argument, the default file is looked for after the other argument checks, so a mistake in the arguments is
// reported whatever the working directory holds.
func request(command string, args []string) (render.Request, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// Run writes every message itself, so the package's own usage text never appears.
	flags.SetOutput(io.Discard)
	out := flags.String("o", "", "")
	methods := flags.String("rpc-methods", "", "")
	table := flags.String("rpc-table", "", "")
	target := flags.String("target", "envoy", "")
	var files []string
	for {
		if err := flags.Parse(args); err != nil {
			return render.Request{}, err
		}
		if flags.NArg() == 0 {
			break
		}
		files = append(files, flags.Arg(0))
		args = flags.Args()[1:]
	}
	switch {
	case *out == "":
		return render.Request{}, errors.New("-o is required")
	case len(files) > 1:
		return render.Request{}, errors.New("give exactly one file")
	case !slices.Contains(targets, *target):
		return render.Request{}, fmt.Errorf("--target must be %s, not %q", orList(targets), *target)
	case !hasGenerator(*target):
		return render.Request{}, fmt.Errorf("--target %s: %s", *target, format.NoGenerator(*target))
	}
	if len(files) == 0 {
		file, err := defaultFile()
		if err != nil {
			return render.Request{}, err
		}
		files = []string{file}
	}
	return render.Request{Proxyfile: files[0], Out: *out, RPCMethods: *methods, RPCTable: *table, Target: *target}, nil
}

// hasGenerator reports whether format.Targets declares the target, the one list of the targets with a generator,
// which render.Read reads too.
func hasGenerator(target string) bool {
	_, declared := format.TargetNamed(target)
	return declared
}

// defaultFile returns the one name of defaultNames that is a regular file in the working directory. Two or more are
// refused rather than ranked: two files that may disagree are a mistake, and choosing one would hide the other.
func defaultFile() (string, error) {
	var found []string
	for _, name := range defaultNames {
		if info, err := os.Stat(name); err == nil && info.Mode().IsRegular() {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no file given and no %s here", orList(defaultNames))
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("more than one default file here: %s; give the file", strings.Join(found, ", "))
}

// outputs reads every input of req and returns every output in memory, or the problems of the inputs, or a usage
// error: an RPC table with no methods to fill it, or an output that would overwrite an input. Both rules run after
// the read, because the proxyfile may write the methods and name the origins file, neither of which the command line
// shows.
func outputs(req render.Request) ([]render.File, []model.Problem, error) {
	run, problems := render.Read(req)
	if problems != nil {
		return nil, problems, nil
	}
	if req.RPCTable != "" && !run.HasMethods() {
		return nil, nil, errors.New("--rpc-table needs rpc.methods, rpc.proto or --rpc-methods")
	}
	if overwritesAnInput(req, run.Inputs()) {
		return nil, nil, errors.New("an output path names an input or the other output")
	}
	files, problems := run.Files()
	return files, problems, nil
}

// overwritesAnInput reports whether an output path names one of the inputs or the other output, which render would
// overwrite. Paths are compared by tree.Absolute, so ./a.yaml and a.yaml are one path. Two inputs on one path
// overwrite nothing.
func overwritesAnInput(req render.Request, inputs []string) bool {
	outputPaths := []string{req.Out}
	if req.RPCTable != "" {
		outputPaths = append(outputPaths, req.RPCTable)
	}
	for i, output := range outputPaths {
		for _, other := range slices.Concat(outputPaths[i+1:], inputs) {
			if tree.Absolute(output) == tree.Absolute(other) {
				return true
			}
		}
	}
	return false
}

// write writes the files that outputs returned, which it returns only when every input was read without a problem and
// no output names an input, so a problem writes nothing.
func write(files []render.File, stderr io.Writer) int {
	for _, file := range files {
		if err := os.WriteFile(file.Path, file.Bytes, 0o644); err != nil {
			fmt.Fprintf(stderr, "proxyfile render: %s\n", writeFailure(err))
			return exitProblem
		}
	}
	return exitDone
}

// writeFailure is the text of an error os.WriteFile returned, as Go writes it ("open <path>: <reason>"), with the path
// written as a problem's file is, so the message stays one line.
func writeFailure(err error) string {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return fmt.Sprintf("%s %s: %s", pathErr.Op, model.OneLinePath(pathErr.Path), model.OneLine(pathErr.Err.Error()))
	}
	return model.OneLine(err.Error())
}

// compare reads each output on disk and writes nothing. A file it cannot read, such as one that does not exist,
// differs.
func compare(files []render.File, stderr io.Writer) int {
	exit := exitDone
	for _, file := range files {
		if onDisk, err := os.ReadFile(file.Path); err != nil || !bytes.Equal(onDisk, file.Bytes) {
			fmt.Fprintf(stderr, "%s differs from what proxyfile render writes\n", model.OneLinePath(file.Path))
			exit = exitProblem
		}
	}
	return exit
}

// orList joins names as a sentence lists them, "a, b or c". Each list it is given here holds two names or more.
func orList(names []string) string {
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " or " + names[last]
}
