// Package cli is the proxyfile program: its commands, flags, messages and exit codes.
package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/kitecorp/proxyfile/internal/format"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/render"
)

const usage = `usage: proxyfile render <file> -o <envoy.yaml> [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile check <file> -o <envoy.yaml> [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile schema
       proxyfile version
`

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
			fmt.Fprintf(stderr, "proxyfile %s: %v\n", command, err)
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
// so the file may come before the flags only if each positional argument is taken off and the rest parsed again.
func request(command string, args []string) (render.Request, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// Run writes every message itself, so the package's own usage text never appears.
	flags.SetOutput(io.Discard)
	out := flags.String("o", "", "")
	methods := flags.String("rpc-methods", "", "")
	table := flags.String("rpc-table", "", "")
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
	case len(files) != 1:
		return render.Request{}, errors.New("give exactly one file")
	case *table != "" && *methods == "":
		return render.Request{}, errors.New("--rpc-table needs --rpc-methods")
	}
	return render.Request{Proxyfile: files[0], Out: *out, RPCMethods: *methods, RPCTable: *table}, nil
}

// outputs reads every input of req and returns every output in memory, or the problems of the inputs, or the usage
// error of an output that would overwrite an input. That rule runs after the read, because the origins file is named
// in the proxyfile, not on the command line.
func outputs(req render.Request) ([]render.File, []model.Problem, error) {
	run, problems := render.Read(req)
	if problems != nil {
		return nil, problems, nil
	}
	if overwritesAnInput(req, run.Inputs()) {
		return nil, nil, errors.New("an output path names an input or the other output")
	}
	files, problems := run.Files()
	return files, problems, nil
}

// overwritesAnInput reports whether an output path names one of the inputs or the other output, which render would
// overwrite. Paths are compared after filepath.Abs, so ./a.yaml and a.yaml are one path. Two inputs on one path
// overwrite nothing.
func overwritesAnInput(req render.Request, inputs []string) bool {
	outputPaths := []string{req.Out}
	if req.RPCTable != "" {
		outputPaths = append(outputPaths, req.RPCTable)
	}
	for i, output := range outputPaths {
		for _, other := range slices.Concat(outputPaths[i+1:], inputs) {
			if absolute(output) == absolute(other) {
				return true
			}
		}
	}
	return false
}

// absolute falls back to the cleaned path when the working directory cannot be read, so two relative paths still
// compare by their text.
func absolute(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// write writes the files that outputs returned, which it returns only when every input was read without a problem and
// no output names an input, so a problem writes nothing.
func write(files []render.File, stderr io.Writer) int {
	for _, file := range files {
		if err := os.WriteFile(file.Path, file.Bytes, 0o644); err != nil {
			fmt.Fprintf(stderr, "proxyfile render: %v\n", err)
			return exitProblem
		}
	}
	return exitDone
}

// compare reads each output on disk and writes nothing. A file it cannot read, such as one that does not exist,
// differs.
func compare(files []render.File, stderr io.Writer) int {
	exit := exitDone
	for _, file := range files {
		if onDisk, err := os.ReadFile(file.Path); err != nil || !bytes.Equal(onDisk, file.Bytes) {
			fmt.Fprintf(stderr, "%s differs from what proxyfile render writes\n", file.Path)
			exit = exitProblem
		}
	}
	return exit
}
