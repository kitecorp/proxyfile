// Package protofiles reads a gateway's RPC methods from .proto files: it compiles them in memory and returns each
// method that carries a method option named by its full name, with that option's action and reports.
package protofiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
)

// Root is one directory of rpc.proto.roots: Dir is the directory read, already resolved against the proxyfile's
// directory; Pos is where the proxyfile writes it.
type Root struct {
	Dir string
	Pos model.Pos
}

// Source is what rpc.proto names: the roots in their written order, and the option's full name with the position
// of the option key, where a problem with the option is named.
type Source struct {
	Roots     []Root
	Option    string
	OptionPos model.Pos
}

// Methods returns each method of the compiled services that carries the option, sorted by path, and every .proto
// file it read; or the problems of the first step that finds any. The steps, in order: find the .proto files under the
// roots, parse them, choose the files that declare a service, name every import that is not found, compile, and read
// the methods that carry the option.
func Methods(s Source) ([]rpc.Method, []string, []model.Problem) {
	sources, read, problems := find(s.Roots)
	if problems != nil {
		return nil, read, problems
	}
	if problems := parse(sources); problems != nil {
		return nil, read, problems
	}
	chosen := choose(sources)
	if problems := missingImports(sources, chosen); problems != nil {
		return nil, read, problems
	}
	files, problems := compile(sources, chosen, s.OptionPos)
	if problems != nil {
		return nil, read, problems
	}
	methods, problems := routed(sources, files, s)
	return methods, read, problems
}

// source is one .proto file, kept by its import path: the root it is under and, once parsed, its syntax tree.
type source struct {
	root string
	ast  *ast.FileNode
}

// file names the source at an import path by its root and that path, as every problem in it does.
func (s *source) file(name string) string {
	return filepath.Join(s.root, filepath.FromSlash(name))
}

// find reads every root: each file whose name ends in .proto, at any depth, is a source under its import path, its
// path under the root written with /, which is how fs.WalkDir names it. The walk goes through os.DirFS because a root
// may itself be a symbolic link to a directory, which filepath.WalkDir would not walk into; below the root, fs.WalkDir
// walks into no link to a directory.
func find(roots []Root) (map[string]*source, []string, []model.Problem) {
	found := map[string]*source{}
	var read []string
	var problems model.Problems
	for _, root := range roots {
		info, err := os.Stat(root.Dir)
		if err != nil {
			problems.Add(root.Pos, "the root %q cannot be read: %s", root.Dir, model.Reason(err))
			continue
		}
		if !info.IsDir() {
			problems.Add(root.Pos, "the root %q is not a directory", root.Dir)
			continue
		}
		count := 0
		err = fs.WalkDir(os.DirFS(root.Dir), ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".proto") {
				return err
			}
			count++
			s := &source{root: root.Dir}
			// An import of the path always reads the first root's file, so nothing would read this one.
			if first, seen := found[name]; seen {
				problems.Add(model.Pos{File: s.file(name)},
					"the import path %q is also under the root %q, which comes first in rpc.proto.roots", name, first.root)
				return nil
			}
			found[name] = s
			read = append(read, s.file(name))
			return nil
		})
		switch {
		case err != nil:
			problems.Add(root.Pos, "the root %q cannot be read: %s", root.Dir, model.Reason(err))
		case count == 0:
			problems.Add(root.Pos, "the root %q holds no .proto file", root.Dir)
		}
	}
	return found, read, problems.Sorted()
}

// collector turns every error protocompile reports into a problem. Its reporter returns nil, which lets protocompile
// go on to the next error, so one run names them all.
type collector struct {
	sources  map[string]*source
	problems model.Problems
}

func (c *collector) reporter() reporter.Reporter {
	return reporter.NewReporter(func(err reporter.ErrorWithPos) error {
		c.add(err)
		return nil
	}, nil)
}

// add places an error at its file, line and column. protocompile names a file by its import path, so a source is
// named by its root and path, as every other problem names it; a standard file keeps its import path.
func (c *collector) add(err reporter.ErrorWithPos) {
	at := err.GetPosition()
	file := at.Filename
	if s, ok := c.sources[file]; ok {
		file = s.file(file)
	}
	c.problems.Add(model.Pos{File: file, Line: at.Line, Column: at.Col}, "%v", err.Unwrap())
}

// parse parses every source, in import path order, and keeps its syntax tree.
func parse(sources map[string]*source) []model.Problem {
	c := collector{sources: sources}
	handler := reporter.NewHandler(c.reporter())
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		s := sources[name]
		data, err := os.ReadFile(s.file(name))
		if err != nil {
			problem := model.CannotRead(s.file(name), err)
			c.problems.Add(problem.Pos, "%s", problem.Message)
			continue
		}
		file, err := parser.Parse(name, bytes.NewReader(data), handler)
		if err == nil {
			s.ast = file
		}
	}
	return c.problems.Sorted()
}

// choose returns the import paths of the sources that declare a service, sorted. A file that declares none has no
// method, and compiling it could only fail on an import no route needs.
func choose(sources map[string]*source) []string {
	var chosen []string
	for name, s := range sources {
		if slices.ContainsFunc(s.ast.Decls, func(decl ast.FileElement) bool {
			_, service := decl.(*ast.ServiceNode)
			return service
		}) {
			chosen = append(chosen, name)
		}
	}
	slices.Sort(chosen)
	return chosen
}

// standard answers the google/protobuf files protoc ships, which protocompile carries built in, and nothing else.
var standard = protocompile.WithStandardImports(protocompile.ResolverFunc(func(string) (protocompile.SearchResult, error) {
	return protocompile.SearchResult{}, fs.ErrNotExist
}))

// missingImports names every import of a chosen file, and of each source it imports at any depth, that is neither a
// source nor a standard file, at its import statement. proxyfile checks this itself because protocompile stops at the
// first such import and compiles in parallel, so which one it names can change between runs; this names them all, in
// a fixed order.
func missingImports(sources map[string]*source, chosen []string) []model.Problem {
	var problems model.Problems
	visited := map[string]bool{}
	queue := slices.Clone(chosen)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		s := sources[name]
		for _, decl := range s.ast.Decls {
			imp, ok := decl.(*ast.ImportNode)
			if !ok {
				continue
			}
			path := imp.Name.AsString()
			if _, isSource := sources[path]; isSource {
				queue = append(queue, path)
				continue
			}
			if _, err := standard.FindFileByPath(path); err == nil {
				continue
			}
			at := s.ast.NodeInfo(imp.Name).Start()
			problems.Add(model.Pos{File: s.file(name), Line: at.Line, Column: at.Col},
				"the import %q is under no root of rpc.proto.roots and is not a standard google/protobuf file", path)
		}
	}
	return problems.Sorted()
}

// compile compiles the chosen files with what they import, asking the sources first and the standard files after, so
// a root's own copy of a standard file is the one read. Every reported error wins over the one Compile returns. An
// error it returns without reporting it is placed at its own position, or, when it has none, at the option key, the
// one place in the proxyfile this package is given.
func compile(sources map[string]*source, chosen []string, at model.Pos) (linker.Files, []model.Problem) {
	c := collector{sources: sources}
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
			if s, ok := sources[path]; ok {
				return protocompile.SearchResult{AST: s.ast}, nil
			}
			return protocompile.SearchResult{}, fs.ErrNotExist
		})),
		Reporter:       c.reporter(),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := compiler.Compile(context.Background(), chosen...)
	if problems := c.problems.Sorted(); problems != nil {
		return nil, problems
	}
	if err != nil {
		if withPos, ok := errors.AsType[reporter.ErrorWithPos](err); ok {
			c.add(withPos)
			return nil, c.problems.Sorted()
		}
		return nil, []model.Problem{{Pos: at, Message: err.Error()}}
	}
	return files, nil
}

// withImports returns the files and every file they import, at any depth, each once, so the option is found wherever
// it is defined.
func withImports(files linker.Files) linker.Files {
	var all linker.Files
	seen := map[string]bool{}
	var add func(f linker.File)
	add = func(f linker.File) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		all = append(all, f)
		imports := f.Imports()
		for i := range imports.Len() {
			if imported := f.FindImportByPath(imports.Get(i).Path()); imported != nil {
				add(imported)
			}
		}
	}
	for _, f := range files {
		add(f)
	}
	return all
}

// routed returns each method of the compiled services that carries the option, sorted by path, or the problems of the
// methods whose action or report holds a control character other than tab. Those are refused here, at the method, as
// the reader refuses one in any key or value of a file (model.Problems.RefuseControl), because the action and the
// reports are read from the .proto files and never pass through that reader.
func routed(sources map[string]*source, files linker.Files, s Source) ([]rpc.Method, []model.Problem) {
	types := withImports(files).AsResolver()
	route, err := option(types, s.Option)
	if err != nil {
		return nil, []model.Problem{{Pos: s.OptionPos, Message: err.Error()}}
	}
	var problems model.Problems
	var methods []rpc.Method
	for _, f := range files {
		services := f.Services()
		for i := range services.Len() {
			service := services.Get(i)
			for j := range service.Methods().Len() {
				m := service.Methods().Get(j)
				at := f.SourceLocations().ByDescriptor(m)
				pos := model.Pos{File: sources[f.Path()].file(f.Path()), Line: at.StartLine + 1, Column: at.StartColumn + 1}
				value, err := route.valueIn(m.Options(), types)
				if err != nil {
					return nil, []model.Problem{{Pos: pos, Message: err.Error()}}
				}
				if value == nil {
					continue
				}
				found := rpc.Method{Service: string(service.FullName()), Method: string(m.Name()),
					Action: value.Get(route.action).String(), Pos: pos}
				problems.RefuseControl(pos, "the action", found.Action)
				if route.reports != nil {
					list := value.Get(route.reports).List()
					for k := range list.Len() {
						report := list.Get(k).String()
						found.Reports = append(found.Reports, report)
						problems.RefuseControl(pos, "the report", report)
					}
				}
				methods = append(methods, found)
			}
		}
	}
	if problems := problems.Sorted(); problems != nil {
		return nil, problems
	}
	// The loop gives the methods file by file, each in the order written; the path in byte order is how rpc.Resolve
	// sorts the routes.
	slices.SortFunc(methods, func(a, b rpc.Method) int {
		return strings.Compare("/"+a.Service+"/"+a.Method, "/"+b.Service+"/"+b.Method)
	})
	return methods, nil
}

// route is the option and the fields of its message a routed method's values are read from.
type route struct {
	option  protoreflect.FieldDescriptor
	action  protoreflect.FieldDescriptor
	reports protoreflect.FieldDescriptor // nil when the message has no field reports
}

// valueIn returns the option's message in a method's options, or nil when the method does not carry it. protocompile
// keeps the option as a known field when descriptor.proto is its built-in copy, and as unknown bytes when a root
// brings its own copy; reading the bytes again with the compiled files as the resolver reads both.
func (r route) valueIn(options proto.Message, types linker.Resolver) (protoreflect.Message, error) {
	data, err := proto.Marshal(options)
	if err != nil {
		return nil, err
	}
	read := dynamicpb.NewMessage(options.ProtoReflect().Descriptor())
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(data, read); err != nil {
		return nil, err
	}
	if !read.Has(r.option) {
		return nil, nil
	}
	return read.Get(r.option).Message(), nil
}

// option finds the option by its full name among the compiled files and what they import, and checks its form: it
// extends google.protobuf.MethodOptions and holds one message, whose field action holds one string and whose field
// reports, when it has one, holds a list of strings. Every other field of the message is left alone.
func option(types linker.Resolver, name string) (route, error) {
	found, err := types.FindExtensionByName(protoreflect.FullName(name))
	if err != nil {
		other, err := types.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			return route{}, fmt.Errorf("no compiled file defines the option %s; the compiled files are those under the "+
				"roots that declare a service, and the files they import", name)
		}
		return route{}, fmt.Errorf("%s is %s, not an option", name, kindOf(other))
	}
	extension := found.TypeDescriptor()
	if extended := extension.ContainingMessage().FullName(); extended != "google.protobuf.MethodOptions" {
		return route{}, fmt.Errorf("the option %s extends %s, not google.protobuf.MethodOptions", name, extended)
	}
	// A group is a message written in the field that holds it, so it counts as one.
	isMessage := extension.Kind() == protoreflect.MessageKind || extension.Kind() == protoreflect.GroupKind
	switch {
	case extension.IsList() && isMessage:
		return route{}, fmt.Errorf("the option %s holds a list of messages, not one", name)
	case extension.IsList():
		return route{}, fmt.Errorf("the option %s holds a list of %s, not one message", name, plural(extension.Kind().String()))
	case !isMessage:
		return route{}, fmt.Errorf("the option %s holds %s, not one message", name, withArticle(extension.Kind().String()))
	}
	message := extension.Message()
	action := message.Fields().ByName("action")
	if action == nil || action.Kind() != protoreflect.StringKind || action.IsList() {
		return route{}, fmt.Errorf("the option's message %s has no field action that holds one string", message.FullName())
	}
	reports := message.Fields().ByName("reports")
	if reports != nil && (reports.Kind() != protoreflect.StringKind || !reports.IsList()) {
		return route{}, fmt.Errorf("the field reports of the option's message %s is not a list of strings", message.FullName())
	}
	return route{option: extension, action: action, reports: reports}, nil
}

// kindOf names what a full name that is not an extension names, with its article.
func kindOf(d protoreflect.Descriptor) string {
	switch d.(type) {
	case protoreflect.MessageDescriptor:
		return "a message"
	case protoreflect.EnumDescriptor:
		return "an enum"
	case protoreflect.EnumValueDescriptor:
		return "an enum value"
	case protoreflect.ServiceDescriptor:
		return "a service"
	case protoreflect.MethodDescriptor:
		return "a method"
	case protoreflect.OneofDescriptor:
		return "a oneof"
	default:
		// FindExtensionByName answers every extension, so what is left is a field that is not one.
		return "a field"
	}
}

// withArticle puts a or an before a field kind. A kind that starts with u takes a, because the two there are, uint32
// and uint64, are read "you int".
func withArticle(kind string) string {
	if strings.ContainsRune("aeio", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// plural names a list of a field kind: strings, bools, and bytes, which is plural already.
func plural(kind string) string {
	if strings.HasSuffix(kind, "s") {
		return kind
	}
	return kind + "s"
}
