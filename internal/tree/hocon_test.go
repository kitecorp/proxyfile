package tree

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/testsupport"
)

func TestFromHOCONAccepts(t *testing.T) {
	cases := []struct {
		name, input string
		others      map[string]string // more files written beside g.conf
		want        string
	}{
		{name: "owner's example", input: "services { api { http = 8080, grpc = 9091 } }\n",
			want: `{services={api={http=n"8080"@services.api.http, grpc=n"9091"@services.api.grpc}@services.api}@services}`},
		{name: "types", input: "a = 8080\nb = \"8080\"\nc = 256MiB\nd = 30s\ne = /\nf = 127.0.0.1\ng = \"^/a\\\\.b$\"\n" +
			"h = true\ni = \"true\"\nj = null\nk = off\nl = 1.5\nm = -1\nn = 0100\n",
			want: `{a=n"8080"@a, b=q"8080"@b, c=q"256MiB"@c, d=q"30s"@d, e=q"/"@e, f=q"127.0.0.1"@f, g=q"^/a\\.b$"@g, ` +
				`h=n"true"@h, i=q"true"@i, j=""@j, k=q"off"@k, l=n"1.5"@l, m=n"-1"@m, n=n"0100"@n}`},
		{name: "order", input: "services { zeta { http = 1, grpc = 2 }, alpha { grpc = 3 } }\n",
			want: `{services={zeta={http=n"1"@services.zeta.http, grpc=n"2"@services.zeta.grpc}@services.zeta, ` +
				`alpha={grpc=n"3"@services.alpha.grpc}@services.alpha}@services}`},
		{name: "routes with an rpc entry",
			input: "routes = [\n  { name = web, prefix = /, to = web }\n  { rpc { to { \"shop.v1.*\" = shop, \"*\" = web } } }\n]\n",
			want: `{routes=[{name=q"web"@routes[0].name, prefix=q"/"@routes[0].prefix, to=q"web"@routes[0].to}@routes[0], ` +
				`{rpc={to={shop.v1.*=q"shop"@routes[1].rpc.to."shop.v1.*", *=q"web"@routes[1].rpc.to."*"}@routes[1].rpc.to}` +
				`@routes[1].rpc}@routes[1]]@routes}`},
		{name: "merge", input: "listen = 1\nlisten = 2\na { x = 1 }\na { y = 2 }\n",
			want: `{listen=n"2"@listen, a={y=n"2"@a.y, x=n"1"@a.x}@a}`},
		{name: "substitution", input: "port = 8000\nlisten = ${port}\n", want: `{port=n"8000"@port, listen=n"8000"@listen}`},
		{name: "byte order mark, CRLF, colon and comments", input: "\uFEFF# c\r\nlisten : 1 // tail\r\nadmin = 2\r\n",
			want: `{listen=n"1"@listen, admin=n"2"@admin}`},
		{name: "unquoted dotted key", input: "to { shop.v1.x = shop }\n", want: `{to={shop={v1={x=q"shop"@to.shop.v1.x}@to.shop.v1}@to.shop}@to}`},
		{name: "quote and backslash in a key", input: "\"a\\\"b\" = 1\n\"c\\\\d\" = 2\n",
			want: `{a"b=n"1"@"a\"b", c\d=n"2"@"c\\d"}`},
		{name: "glued hash", input: "rewrite = /a#b\n", want: `{rewrite=q"/a"@rewrite}`},
		{name: "empty", input: "", want: `{}`},
		{name: "scalar list", input: "a = [x, \"y\", 0100, 1.50, true, -0, 1e3]\n",
			want: `{a=[q"x"@a[0], q"y"@a[1], n"0100"@a[2], n"1.50"@a[3], n"true"@a[4], n"-0"@a[5], n"1e3"@a[6]]@a}`},
		{name: "mixed array", input: "a = [ { z = 1, b = 2 }, x, 0100 ]\n",
			want: `{a=[{b=n"2"@a[0].b, z=n"1"@a[0].z}@a[0], q"x"@a[1], n"100"@a[2]]@a}`},
		// An array inside a mixed array is built from its Go value too, each item placed by its own index.
		{name: "array in a mixed array", input: "a = [ [x, 1], y ]\n",
			want: `{a=[[q"x"@a[0][0], n"1"@a[0][1]]@a[0], q"y"@a[1]]@a}`},
		{name: "include", input: "include \"other.conf\"\nlisten = 8000\n",
			others: map[string]string{"other.conf": "services { web { http = 3000 } }\n"},
			want:   `{services={web={http=n"3000"@services.web.http}@services.web}@services, listen=n"8000"@listen}`},
		{name: "missing include", input: "include \"nope.conf\"\nlisten = 8000\n", want: `{listen=n"8000"@listen}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeHOCONFiles(t, c.input, c.others, false)
			root, problems := FromHOCON("g.conf")
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := show(root); got != c.want {
				t.Errorf("tree =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// The messages, but for the file missing row, are the library's own, pinned byte for byte: a library upgrade that
// changes one fails here, which is where a changed message should be read.
func TestFromHOCONRefuses(t *testing.T) {
	cases := []struct {
		name, input string
		others      map[string]string
		absent      bool // g.conf is not written
		want        []string
	}{
		{name: "file missing", absent: true, want: []string{`g.conf: the file cannot be read: no such file or directory`}},
		{name: "brace not closed", input: "limits {\n  heap = 1MiB\n", want: []string{`g.conf:3:1: unexpected EOF, expected '}'`}},
		{name: "close with no object open", input: "listen = 1\n}\n", want: []string{`g.conf:2:1: expected key, got 7`}},
		{name: "string not closed", input: "p = \"abc\n", want: []string{`g.conf:1:5: unterminated string`}},
		{name: "array not closed", input: "send = [a, b\n", want: []string{`g.conf:2:1: unexpected EOF in array`}},
		{name: "unquoted star", input: "accept = [x-user-*]\n", want: []string{`g.conf:1:18: unexpected character: *`}},
		{name: "unquoted star key", input: "to { * = web }\n", want: []string{`g.conf:1:6: unexpected character: *`}},
		{name: "unquoted colon", input: "address = ::1\n", want: []string{`g.conf:1:11: unexpected token 11`}},
		{name: "semicolon", input: "http = 1; grpc = 2\n", want: []string{`g.conf:1:16: unexpected token 12`}},
		{name: "not UTF-8", input: "p = \"a\xffb\"\n",
			want: []string{`g.conf:1:7: invalid UTF-8 byte sequence: HOCON input must be valid UTF-8 (S1.1)`}},
		{name: "array at the root", input: "[1, 2]\n", want: []string{`g.conf: config error: g.conf: 1:1: document has type ` +
			`array rather than object at file root (HOCON.md L989-991); the Config API requires an object at file root`}},
		{name: "number above int64", input: "listen = 9223372036854775808\n",
			want: []string{`g.conf:1:10: invalid int "9223372036854775808"`}},
		{name: "environment variable", input: "listen = ${HOME}\n",
			want: []string{`g.conf: resolve error at path "HOME": unresolved substitution`}},
		{name: "required include", input: "include required(\"nope.conf\")\n",
			want: []string{`nope.conf: cannot read include file: open nope.conf: no such file or directory`}},
		{name: "syntax error in an include", input: "include \"broken.conf\"\nlisten = 8000\n",
			others: map[string]string{"broken.conf": "a = {\n"},
			want:   []string{`g.conf: parse error at line 2, col 1: unexpected EOF, expected '}'`}},
		{name: "url include", input: "include url(\"http://example.com/x.conf\")\n",
			want: []string{`g.conf:1:1: include url(...) is not supported in v1.0`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Set for every row, so a front that read the environment would accept the environment variable row.
			t.Setenv("HOME", "/somewhere")
			writeHOCONFiles(t, c.input, c.others, c.absent)
			root, problems := FromHOCON("g.conf")
			if got := testsupport.ProblemStrings(problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
			if root != nil {
				t.Errorf("root = %s, want nil next to problems", show(root))
			}
		})
	}
}

// writeHOCONFiles moves the test into a directory of its own and writes the row's files there, so FromHOCON reads
// g.conf by that name and every problem names it so.
func writeHOCONFiles(t *testing.T, input string, others map[string]string, absent bool) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if !absent {
		testsupport.WriteFile(t, filepath.Join(dir, "g.conf"), input)
	}
	for name, text := range others {
		testsupport.WriteFile(t, filepath.Join(dir, name), text)
	}
}

// show writes a tree on one line, so a row compares the whole tree with one literal: a scalar as its quoted text with
// q before it when Quoted is true and n when NumberOrBool is true, then @ and its key path; a list in brackets and a
// map in braces as key=value, each followed by @ and its key path but for the root, whose path is empty. An entry
// whose key position differs from its value's shows the key's own after the key.
func show(n *Node) string {
	switch n.Kind {
	case Scalar:
		flags := ""
		if n.Quoted {
			flags += "q"
		}
		if n.NumberOrBool {
			flags += "n"
		}
		return fmt.Sprintf("%s%q@%s", flags, n.Text, place(n.Pos))
	case List:
		items := make([]string, len(n.Items))
		for i, item := range n.Items {
			items[i] = show(item)
		}
		return "[" + strings.Join(items, ", ") + "]" + containerPlace(n.Pos)
	case Map:
		entries := make([]string, len(n.Entries))
		for i, entry := range n.Entries {
			key := entry.Key
			if entry.KeyPos != entry.Value.Pos {
				key += "@" + place(entry.KeyPos)
			}
			entries[i] = key + "=" + show(entry.Value)
		}
		return "{" + strings.Join(entries, ", ") + "}" + containerPlace(n.Pos)
	default:
		return fmt.Sprintf("kind %d", n.Kind)
	}
}

// place is the key path alone when the rest of the position is what every HOCON node carries (the file given, no line,
// no column), and the whole position otherwise, so a row fails on a wrong file or a line it did not expect.
func place(pos model.Pos) string {
	if pos == (model.Pos{File: "g.conf", Path: pos.Path}) {
		return pos.Path
	}
	return fmt.Sprintf("%+v", pos)
}

// containerPlace is @ and a list's or a map's place, or nothing for the root, so a row that holds no container but the
// root reads as before.
func containerPlace(pos model.Pos) string {
	if at := place(pos); at != "" {
		return "@" + at
	}
	return ""
}
