// Package render reads every input of one run and returns every output, in memory, so a caller writes nothing when
// any input has a problem.
package render

import (
	"path/filepath"

	"github.com/kitecorp/proxyfile/internal/envoy"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/origins"
	"github.com/kitecorp/proxyfile/internal/read"
	"github.com/kitecorp/proxyfile/internal/rpc"
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
	hasMethods bool        // RPC methods were given, by rpc.methods or --rpc-methods, even as an empty list
}

// Read reads every input of req and returns them ready to generate, or every problem it finds.
//
// The proxyfile is read first, because every other input is read against it. Then the origins file, when the
// proxyfile names one instead of writing the origins, the RPC methods and the rpc-entry rule run together, so one run
// names the problems of all three. The methods are the proxyfile's rpc.methods when it writes them, and the
// --rpc-methods file otherwise; giving both is a problem, because one list would be ignored.
func Read(req Request) (*Run, []model.Problem) {
	proxyfile, problems := read.Proxyfile(req.Proxyfile)
	if problems != nil {
		return nil, problems
	}
	g, file := proxyfile.Gateway, proxyfile.Origins
	inputPaths := []string{req.Proxyfile}
	var all []model.Problem
	if file == nil {
		// A relative origins path is read from the proxyfile's own directory; an absolute one as written, because
		// joining it under that directory would name a file that is not the one written.
		originsPath := g.Origins.Value
		if !filepath.IsAbs(originsPath) {
			originsPath = filepath.Join(filepath.Dir(req.Proxyfile), originsPath)
		}
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
			Message: "the routes have an rpc entry, so rpc.methods or --rpc-methods must list the RPC methods"})
	}
	if all != nil {
		model.SortProblems(all)
		return nil, all
	}
	return &Run{req: req, input: envoy.Input{Gateway: g, Origins: file, RPC: routes}, inputPaths: inputPaths,
		hasMethods: proxyfile.MethodsKey != nil || req.RPCMethods != ""}, nil
}

// Inputs returns the path of every file the run read: the proxyfile, the origins file when it names one and the
// methods file when --rpc-methods gave one.
func (r *Run) Inputs() []string {
	return r.inputPaths
}

// HasMethods reports whether the run was given RPC methods, by rpc.methods or --rpc-methods, an empty list included,
// so a caller can refuse an RPC table that no methods were given for.
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
