// Package render reads every input of one run and returns every output, in memory, so a caller writes nothing when
// any input has a problem.
package render

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/envoy"
	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/origins"
	"github.com/kitecorp/proxyfile/v2/internal/protofiles"
	"github.com/kitecorp/proxyfile/v2/internal/read"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// Request is the files and the target of one run. RPCMethods and RPCTable are "" when not given, and a Target of "" is
// envoy.
type Request struct{ Proxyfile, Out, RPCMethods, RPCTable, Target string }

type File struct {
	Path  string
	Bytes []byte
}

// Target is a proxy with a generator: its --target name, the name the examples commit its configuration under, and
// its generator.
type Target struct {
	Name     string
	Config   string
	Generate func(generator.Input) ([]generator.File, error)
}

// Targets are the proxies with a generator: the one list render picks a generator from, which cli reads too to accept
// a --target. format.Targets declares each one's support of every key and cannot hold the generators itself: every
// generator depends on format, through origins and rpc, so format importing one would be an import cycle.
var Targets = []Target{{Name: "envoy", Config: "envoy.yaml", Generate: envoy.Generate}}

// TargetNamed returns the target whose Name is name. It reads Targets at each call, so a test may declare its own.
func TargetNamed(name string) (Target, bool) {
	i := slices.IndexFunc(Targets, func(t Target) bool { return t.Name == name })
	if i < 0 {
		return Target{}, false
	}
	return Targets[i], true
}

// Run is the inputs of one render, read without a problem and ready to generate.
type Run struct {
	req        Request
	generate   func(generator.Input) ([]generator.File, error)
	input      generator.Input // what the generator reads
	inputPaths []string        // the files it was read from
	hasMethods bool            // RPC methods were given, by rpc.methods, rpc.proto or --rpc-methods, also when none
}

// Read reads every input of req and returns them ready to generate, or every problem it finds.
//
// The target is looked up before any file is read, in Targets for its generator and in format.Targets for the keys it
// refuses, and one that either list lacks is refused, so no caller gets another proxy's file. The proxyfile is read
// first, because every other input is read against it, and a key the target refuses ends the run there, because no
// other input can make the target carry it. Then the origins file, when the proxyfile names one instead of writing the
// origins, the RPC methods and the rpc-entry rule run together, so one run names the problems of all three, the keys
// the target refuses in the origins file and in the methods among them, whether a methods file or .proto options give
// them. The methods are the proxyfile's rpc.methods when it writes them, those of the .proto files under the roots of
// rpc.proto when it writes that, and the --rpc-methods file otherwise; giving the flag as well is a problem, because
// one list would be ignored. A relative origins path or root is read from the proxyfile's own directory, and an
// absolute one as written (tree.Beside says why).
func Read(req Request) (*Run, []model.Problem) {
	name := cmp.Or(req.Target, "envoy")
	target, declared := format.TargetNamed(name)
	chosen, hasGenerator := TargetNamed(name)
	if !declared || !hasGenerator {
		return nil, model.FileProblem(req.Proxyfile, "--target "+name+": "+format.NoGenerator(name))
	}
	proxyfile, problems := read.Proxyfile(req.Proxyfile)
	if problems != nil {
		return nil, problems
	}
	if problems := format.Refusals(format.Proxyfile, proxyfile.Tree, proxyfile.Tree, target); problems != nil {
		return nil, problems
	}
	g, file := proxyfile.Gateway, proxyfile.Origins
	inputPaths := append([]string{req.Proxyfile}, proxyfile.Included...)
	var all []model.Problem
	if file == nil {
		originsPath := tree.Beside(req.Proxyfile, g.Origins.Value)
		var root *tree.Node
		file, root, problems = origins.Read(originsPath)
		if problems == nil {
			problems = format.Refusals(format.Origins, root, proxyfile.Tree, target)
		}
		all = append(all, problems...)
		inputPaths = append(inputPaths, originsPath)
	}
	var routes []rpc.Route
	switch entry := g.RPCEntry(); {
	case proxyfile.MethodsKey != nil:
		if req.RPCMethods != "" {
			all = append(all, model.Problem{Pos: *proxyfile.MethodsKey,
				Message: "rpc.methods is written here, so --rpc-methods must not be given"})
		}
		routes, problems = rpc.Resolve(g, proxyfile.Methods)
		all = append(all, problems...)
	case proxyfile.Proto != nil:
		if req.RPCMethods != "" {
			all = append(all, model.Problem{Pos: proxyfile.Proto.Key,
				Message: "rpc.proto is written here, so --rpc-methods must not be given"})
		}
		source := protofiles.Source{Option: proxyfile.Proto.Option, OptionPos: proxyfile.Proto.OptionKey}
		for _, root := range proxyfile.Proto.Roots {
			source.Roots = append(source.Roots, protofiles.Root{Dir: tree.Beside(req.Proxyfile, root.Value), Pos: root.Pos})
		}
		methods, protoPaths, problems := protofiles.Methods(source)
		inputPaths = append(inputPaths, protoPaths...)
		if problems == nil {
			routes, problems = rpc.Resolve(g, methods)
			problems = append(problems, format.Refusals(format.Methods, rpc.Tree(methods), proxyfile.Tree, target)...)
		}
		all = append(all, problems...)
	case req.RPCMethods != "":
		methods, root, problems := rpc.ReadMethods(req.RPCMethods)
		if problems == nil {
			routes, problems = rpc.Resolve(g, methods)
			problems = append(problems, format.Refusals(format.Methods, root, proxyfile.Tree, target)...)
		}
		all = append(all, problems...)
		inputPaths = append(inputPaths, req.RPCMethods)
	case entry != nil:
		// Without the methods the generated file would hold no RPC route, and every RPC call would fall to a plain
		// route.
		all = append(all, model.Problem{Pos: entry.Pos,
			Message: "the routes have an rpc entry, so rpc.methods, rpc.proto or --rpc-methods must list the RPC methods"})
	}
	if all != nil {
		model.SortProblems(all)
		return nil, all
	}
	return &Run{req: req, generate: chosen.Generate, input: generator.Input{Gateway: g, Origins: file, RPC: routes},
		inputPaths: inputPaths,
		hasMethods: proxyfile.MethodsKey != nil || proxyfile.Proto != nil || req.RPCMethods != ""}, nil
}

// Inputs returns the path of every file the run read: the proxyfile, the files its includes name, the origins file
// when it names one, and the methods file when --rpc-methods gave one or every .proto file rpc.proto read, in the
// order its roots were walked. An included name that no file answers is listed too, because the next run would read a
// file written there.
func (r *Run) Inputs() []string {
	return r.inputPaths
}

// HasMethods reports whether the run was given RPC methods, by rpc.methods, rpc.proto or --rpc-methods, also when
// they hold no method, so a caller can refuse an RPC table that no methods were given for.
func (r *Run) HasMethods() bool {
	return r.hasMethods
}

// Files returns every output, in memory and in this order: each file the target's generator returns, then RPCTable.
// The configuration, the file with no name, goes at Out, and a file the configuration loads goes beside it under its
// own name, because the configuration names it by that name. A name that holds a folder, or is . or .., would put the
// file outside the folder of Out, so it is refused as the bug in proxyfile it is.
func (r *Run) Files() ([]File, []model.Problem) {
	generated, err := r.generate(r.input)
	if err != nil {
		return nil, bug(r.req.Out, err.Error())
	}
	var files []File
	var problems []model.Problem
	for _, file := range generated {
		path := r.req.Out
		if file.Name != "" {
			if file.Name == "." || !filepath.IsLocal(file.Name) || filepath.Base(file.Name) != file.Name {
				problems = append(problems, bug(r.req.Out, fmt.Sprintf("the generator named a file %q, which is not a name alone", file.Name))...)
				continue
			}
			path = filepath.Join(filepath.Dir(r.req.Out), file.Name)
		}
		files = append(files, File{Path: path, Bytes: file.Bytes})
	}
	if problems != nil {
		return nil, problems
	}
	if r.req.RPCTable != "" {
		files = append(files, File{Path: r.req.RPCTable, Bytes: rpc.Table(r.input.RPC)})
	}
	return files, nil
}

// bug is the problem of an output that a defect of proxyfile's own code keeps it from writing.
func bug(out, reason string) []model.Problem {
	return model.FileProblem(out, "proxyfile could not write this file, which is a bug in proxyfile: "+reason)
}
