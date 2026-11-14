package conformance

import (
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/origins"
	"github.com/kitecorp/proxyfile/v2/internal/protofiles"
	"github.com/kitecorp/proxyfile/v2/internal/read"
	// Named so, because the scenarios' rpc builds a gRPC request.
	rpcmethods "github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// writtenKeys returns every core key a render of the proxyfile at path reads, as format.WrittenKeys spells them, or
// the text of the first problem a reader finds: the proxyfile's own keys, with its includes; those of the origins file
// it names when it does not write the origins; and those of the methods the .proto options under its rpc.proto give.
// Each is read as render.Read reads it, by the same readers and from the same place, so a row keyed on an origins key
// or a method's action runs on a file that takes them from another file. No --rpc-methods file is read: the battery
// renders with no such flag, so a proxyfile that needs one does not render.
func writtenKeys(path string) ([]string, string) {
	f, problems := read.Proxyfile(path)
	if problems != nil {
		return nil, problems[0].String()
	}
	keys := format.WrittenKeys(format.Proxyfile, f.Tree)
	if f.Origins == nil {
		_, root, problems := origins.Read(tree.Beside(path, f.Gateway.Origins.Value))
		if problems != nil {
			return nil, problems[0].String()
		}
		keys = append(keys, format.WrittenKeys(format.Origins, root)...)
	}
	if f.Proto != nil {
		source := protofiles.Source{Option: f.Proto.Option, OptionPos: f.Proto.OptionKey}
		for _, root := range f.Proto.Roots {
			source.Roots = append(source.Roots, protofiles.Root{Dir: tree.Beside(path, root.Value), Pos: root.Pos})
		}
		methods, _, problems := protofiles.Methods(source)
		if problems != nil {
			return nil, problems[0].String()
		}
		keys = append(keys, format.WrittenKeys(format.Methods, rpcmethods.Tree(methods))...)
	}
	slices.Sort(keys)
	return slices.Compact(keys), ""
}

// runsOn reports whether e runs on a file that writes written: one that writes every key e lists, so its answer is
// the one those keys give.
func runsOn(e exchange, written []string) bool {
	for _, key := range e.keys {
		if !slices.Contains(written, key) {
			return false
		}
	}
	return true
}

// literalOn returns what e wants on the file labelled label: its literal there when e.on holds one, and otherwise
// e.want and e.admitted.
func literalOn(e exchange, label string) literal {
	if l, ok := e.on[label]; ok {
		return l
	}
	return literal{want: e.want, admitted: e.admitted}
}
