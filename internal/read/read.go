// Package read turns a proxyfile into the model, or into the problems that stop the run.
package read

import (
	"path/filepath"
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/input"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/origins"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// File is a proxyfile after reading. The origins and the RPC methods it may hold have their types in packages that
// import model, so they sit beside the gateway rather than in it.
type File struct {
	Gateway *model.Gateway
	Origins *origins.File // read from the proxyfile; nil when "origins" names a file, which render.Read reads
	Methods []rpc.Method  // rpc.methods in written order; nil when not written or written empty
	// MethodsKey is the methods key inside rpc, nil when the proxyfile does not write it. render.Read tells an empty
	// list from none by it, and names it when --rpc-methods is given as well.
	MethodsKey *model.Pos
	Proto      *Proto // rpc.proto; nil when not written
	// Included is every file the include statements of a HOCON proxyfile name, also one that does not exist, in the
	// order first named; nil for a YAML proxyfile, which has no includes. They are inputs of the run, so no output
	// may name one.
	Included []string
	// Tree is the tree the format check accepted, in which render.Read finds the keys its target refuses.
	Tree *tree.Node
}

// Proxyfile reads a proxyfile and returns its model and the origins and RPC methods it writes, or the problems that
// stop the run. It chooses the form by the file name: a file named Proxyfile or ending in .conf is HOCON, one ending
// in .yaml or .yml is YAML.
//
// The stages run in order and the first that finds a problem ends the run with all of its problems, sorted. Each
// stage may then rely on the one before: decoding reads only a tree the check accepted, and the meaning rules read
// only a complete model. The origins and rpc.proto are read in the meaning rules' stage, so one run names the problems
// of all three.
func Proxyfile(path string) (*File, []model.Problem) {
	var included []string
	front := frontFor(path, &included)
	if front == nil {
		return nil, model.FileProblem(path, "proxyfile reads a file named Proxyfile or ending in .conf, .yaml or .yml")
	}
	root, problems := input.CheckedWith(path, format.Proxyfile, front)
	if problems != nil {
		return nil, problems
	}
	g, problems := decode(root)
	if problems != nil {
		return nil, problems
	}
	inline, originsProblems := readOrigins(root)
	proto, protoProblems := readProto(root, path)
	if problems := slices.Concat(meaning(g), originsProblems, protoProblems); len(problems) > 0 {
		model.SortProblems(problems)
		return nil, problems
	}
	file := &File{Gateway: g, Origins: inline, Proto: proto, Included: included, Tree: root}
	// The check requires exactly one of methods and proto inside rpc, so methods are written exactly when rpc is and
	// proto is not. Every rule of the methods is rpc.Resolve's, which render.Read runs on these as on a methods file's,
	// so this stage only reads them.
	if methods := root.Get("rpc").Entry("methods"); methods != nil {
		file.Methods, file.MethodsKey = rpc.MethodsOf(methods.Value), &methods.KeyPos
	}
	return file, nil
}

// readOrigins reads the origins the proxyfile writes, or returns nil when "origins" names a file.
func readOrigins(root *tree.Node) (*origins.File, []model.Problem) {
	if environments := root.Get("origins"); environments.Kind == tree.Map {
		return origins.Inline(root.Get("cors"), environments)
	}
	// An origins file holds its own cors settings, and those are the ones render.Read gives the generator, so a cors
	// written here would be ignored.
	if cors := root.Entry("cors"); cors != nil {
		return nil, []model.Problem{{Pos: cors.KeyPos, Message: `"cors" is written in the origins file when "origins" names a file`}}
	}
	return nil, nil
}

// frontFor picks the reader by the file name, never by the content: a guess could read a broken file of one form as
// the other. nil means proxyfile reads no file of that name. The HOCON reader stores the files the includes name in
// *included, because input.CheckedWith hands back only the tree.
func frontFor(path string, included *[]string) func(path string) (*tree.Node, []model.Problem) {
	switch ext := filepath.Ext(path); {
	case ext == ".yaml" || ext == ".yml":
		return input.YAMLFile
	case ext == ".conf" || filepath.Base(path) == "Proxyfile":
		return func(path string) (*tree.Node, []model.Problem) {
			root, files, problems := tree.FromHOCON(path)
			*included = files
			return root, problems
		}
	default:
		return nil
	}
}
