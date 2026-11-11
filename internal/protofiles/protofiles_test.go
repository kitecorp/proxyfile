package protofiles

import (
	"fmt"
	"slices"
	"testing"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/testsupport"
)

// Each row compiles a tree under testdata. The roots are written at g.yaml:8:23 and g.yaml:8:30, and the option at
// g.yaml:8:39, as rpc.proto's flow form puts them.
func TestMethods(t *testing.T) {
	forms := []string{"testdata/forms"}
	cases := []struct {
		name   string
		roots  []string
		option string
		// want is each method as its path, action, reports and position, then each problem; a row expects methods
		// or problems.
		want []string
	}{
		{name: "routed methods, sorted by path", roots: forms, option: "o.v1.route", want: []string{
			`/o.v1.S/Open action "" reports [] at testdata/forms/o/v1/s.proto:12:3`,
			`/o.v1.S/Read action "read" reports ["export"] at testdata/forms/o/v1/s.proto:8:3`,
		}},
		{name: "a message without reports, with another field", roots: forms, option: "o.v1.bare", want: []string{
			`/o.v1.S/Read action "own" reports [] at testdata/forms/o/v1/s.proto:8:3`,
		}},
		{name: "an option no method carries", roots: forms, option: "o.v1.unused"},
		{name: "a root's own descriptor.proto", roots: []string{"testdata/owncopy"}, option: "o.v1.route", want: []string{
			`/o.v1.S/Read action "read" reports [] at testdata/owncopy/o/v1/s.proto:16:3`,
		}},
		// testdata/linked is a symbolic link to forms: a root may be one, as a checkout's link to another often is.
		{name: "a root that is a symbolic link", roots: []string{"testdata/linked"}, option: "o.v1.route", want: []string{
			`/o.v1.S/Open action "" reports [] at testdata/linked/o/v1/s.proto:12:3`,
			`/o.v1.S/Read action "read" reports ["export"] at testdata/linked/o/v1/s.proto:8:3`,
		}},
		{name: "a root that cannot be read", roots: []string{"testdata/missing"}, option: "o.v1.route", want: []string{
			"g.yaml:8:23: the root testdata/missing cannot be read: no such file or directory",
		}},
		{name: "a root that is not a directory", roots: []string{"testdata/forms/o/v1/s.proto"}, option: "o.v1.route", want: []string{
			"g.yaml:8:23: the root testdata/forms/o/v1/s.proto is not a directory",
		}},
		{name: "a root with no .proto file", roots: []string{"testdata/empty"}, option: "o.v1.route", want: []string{
			"g.yaml:8:23: the root testdata/empty holds no .proto file",
		}},
		{name: "one import path under two roots", roots: []string{"testdata/two/first", "testdata/two/second"}, option: "o.v1.route", want: []string{
			"testdata/two/second/a/v1/s.proto: the import path a/v1/s.proto is also under the root testdata/two/first, which comes first in rpc.proto.roots",
		}},
		{name: "a syntax error", roots: []string{"testdata/syntax"}, option: "o.v1.route", want: []string{
			"testdata/syntax/a/v1/s.proto:5:1: syntax error: expecting ';'",
		}},
		// d/v1/loose.proto imports a file no root holds too, but it declares no service and no service file imports
		// it, so it is not compiled and its import is not named.
		{name: "imports that name no file", roots: []string{"testdata/imports"}, option: "o.v1.route", want: []string{
			"testdata/imports/a/v1/s.proto:5:8: the import google/rpc/error_details.proto is under no root of rpc.proto.roots and is not a standard google/protobuf file",
			"testdata/imports/a/v1/s.proto:7:8: the import c/v1/missing.proto is under no root of rpc.proto.roots and is not a standard google/protobuf file",
			"testdata/imports/b/v1/types.proto:5:8: the import nowhere/y.proto is under no root of rpc.proto.roots and is not a standard google/protobuf file",
		}},
		{name: "an unknown type", roots: []string{"testdata/unknown"}, option: "o.v1.route", want: []string{
			"testdata/unknown/a/v1/s.proto:6:9: method a.v1.S.M: unknown request type Missing",
		}},
		{name: "a method declared twice", roots: []string{"testdata/twice"}, option: "o.v1.route", want: []string{
			`testdata/twice/a/v1/s.proto:7:7: symbol "a.v1.S.M" already defined at a/v1/s.proto:6:7`,
		}},
		{name: "no compiled file defines the option", roots: forms, option: "o.v1.rout", want: []string{
			"g.yaml:8:39: no compiled file defines the option o.v1.rout; the compiled files are those under the roots that declare a service, and the files they import",
		}},
		{name: "a message, not an option", roots: forms, option: "o.v1.Route", want: []string{
			"g.yaml:8:39: o.v1.Route is a message, not an option",
		}},
		{name: "a field, not an option", roots: forms, option: "o.v1.Route.action", want: []string{
			"g.yaml:8:39: o.v1.Route.action is a field, not an option",
		}},
		{name: "not a method option", roots: forms, option: "o.v1.message_route", want: []string{
			"g.yaml:8:39: the option o.v1.message_route extends google.protobuf.MessageOptions, not google.protobuf.MethodOptions",
		}},
		{name: "a string", roots: forms, option: "o.v1.text", want: []string{
			"g.yaml:8:39: the option o.v1.text holds a string, not one message",
		}},
		{name: "a list of messages", roots: forms, option: "o.v1.routes", want: []string{
			"g.yaml:8:39: the option o.v1.routes holds a list of messages, not one",
		}},
		// A kind that starts with u is read "you", so it takes a; bytes is plural already.
		{name: "a uint32", roots: forms, option: "o.v1.count", want: []string{
			"g.yaml:8:39: the option o.v1.count holds a uint32, not one message",
		}},
		{name: "an int64", roots: forms, option: "o.v1.size", want: []string{
			"g.yaml:8:39: the option o.v1.size holds an int64, not one message",
		}},
		{name: "a list of bytes", roots: forms, option: "o.v1.blobs", want: []string{
			"g.yaml:8:39: the option o.v1.blobs holds a list of bytes, not one message",
		}},
		{name: "no action", roots: forms, option: "o.v1.no_action", want: []string{
			"g.yaml:8:39: the option's message o.v1.NoAction has no field action that holds one string",
		}},
		{name: "reports not a list of strings", roots: forms, option: "o.v1.bad_reports", want: []string{
			"g.yaml:8:39: the field reports of the option's message o.v1.BadReports is not a list of strings",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var roots []Root
			for i, dir := range c.roots {
				roots = append(roots, Root{Dir: dir, Pos: model.Pos{File: "g.yaml", Line: 8, Column: 23 + 7*i}})
			}
			methods, _, problems := Methods(Source{Roots: roots, Option: c.option, OptionPos: model.Pos{File: "g.yaml", Line: 8, Column: 39}})
			if methods != nil && problems != nil {
				t.Errorf("Methods returned methods %v next to problems", methods)
			}
			var got []string
			for _, m := range methods {
				got = append(got, fmt.Sprintf("/%s/%s action %q reports %q at %s:%d:%d", m.Service, m.Method, m.Action, m.Reports, m.Pos.File, m.Pos.Line, m.Pos.Column))
			}
			got = append(got, testsupport.ProblemStrings(problems)...)
			if !slices.Equal(got, c.want) {
				t.Errorf("methods and problems = %q, want %q", got, c.want)
			}
		})
	}
}
