// Package render reads every input of one run and returns every output, in memory, so a caller writes nothing when
// any input has a problem.
package render

import (
	"github.com/kitecorp/proxyfile/internal/envoy"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/origins"
	"github.com/kitecorp/proxyfile/internal/protofiles"
	"github.com/kitecorp/proxyfile/internal/read"
	"github.com/kitecorp/proxyfile/internal/rpc"
	"github.com/kitecorp/proxyfile/internal/tree"
)

type Request struct{ Proxyfile, Out, RPCMethods, RPCTable string } // RPCMethods, RPCTable "" when not given

type File struct {
	Path  string
	Bytes []byte
}

// Run is the inputs of one render, read without a problem and ready to generate.
type Run struct {
	req        Request
	input      envoy.Input // what the generator reads
	inputPaths []string    // the files it was read from
	hasMethods bool        // RPC methods were given, by rpc.methods, rpc.proto or --rpc-methods, also when none
}

// Read reads every input of req and returns them ready to generate, or every problem it finds.
//
// The proxyfile is read first, because every other input is read against it. Then the origins file, when the
// proxyfile names one instead of writing the origins, the RPC methods and the rpc-entry rule run together, so one run
// names the problems of all three. The methods are the proxyfile's rpc.methods when it writes them, those of the
// .proto files under the roots of rpc.proto when it writes that, and the --rpc-methods file otherwise; giving the
// flag as well is a problem, because one list would be ignored. A relative origins path or root is read from the
// proxyfile's own directory, and an absolute one as written (tree.Beside says why).
func Read(req Request) (*Run, []model.Problem) {
	proxyfile, problems := read.Proxyfile(req.Proxyfile)
	if problems != nil {
		return nil, problems
	}
	g, file := proxyfile.Gateway, proxyfile.Origins
	inputPaths := append([]string{req.Proxyfile}, proxyfile.Included...)
	var all []model.Problem
	if file == nil {
		originsPath := tree.Beside(req.Proxyfile, g.Origins.Value)
		file, problems = origins.Read(originsPath)
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
		}
		all = append(all, problems...)
	case req.RPCMethods != "":
		methods, problems := rpc.ReadMethods(req.RPCMethods)
		if problems == nil {
			routes, problems = rpc.Resolve(g, methods)
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
	return &Run{req: req, input: envoy.Input{Gateway: g, Origins: file, RPC: routes}, inputPaths: inputPaths,
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

// Files returns every output, in memory and in this order: Out, then RPCTable.
func (r *Run) Files() ([]File, []model.Problem) {
	out, err := envoy.Generate(r.input)
	if err != nil {
		return nil, model.FileProblem(r.req.Out, "proxyfile could not write this file, which is a bug in proxyfile: "+err.Error())
	}
	files := []File{{Path: r.req.Out, Bytes: out}}
	if r.req.RPCTable != "" {
		files = append(files, File{Path: r.req.RPCTable, Bytes: rpc.Table(r.input.RPC)})
	}
	return files, nil
}
