package model

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
)

func TestProblemString(t *testing.T) {
	cases := []struct {
		problem Problem
		want    string
	}{
		{Problem{Pos{File: "g.yaml", Line: 3, Column: 5}, "x"}, "g.yaml:3:5: x"},
		{Problem{Pos{File: "g.yaml", Line: 3, Column: 0}, "x"}, "g.yaml:3: x"},
		{Problem{Pos{File: "g.yaml", Line: 0, Column: 0}, "x"}, "g.yaml: x"},
		{Problem{Pos{File: "g.conf", Path: "services.api.http"}, "x"}, "g.conf: services.api.http: x"},
		{Problem{Pos{File: "g.conf", Line: 3, Column: 5, Path: "a"}, "x"}, "g.conf:3:5: x"},
		// A file's name may hold a control character, as a .proto file found under a root may, and written as it is a
		// newline there would print the problem on two lines. Quoted, the problem keeps to one line.
		{Problem{Pos{File: "a\nb.proto", Line: 3, Column: 5}, "x"}, `"a\nb.proto":3:5: x`},
		{Problem{Pos{File: "a\rb.conf", Path: "listen"}, "x"}, `"a\rb.conf": listen: x`},
		{Problem{Pos{File: "a\tb.yaml", Line: 3, Column: 5}, "x"}, "a\tb.yaml:3:5: x"},
		// A message of a library, protocompile's or the HOCON library's, writes a file's name as it is, so a control
		// character in it is written as its escape.
		{Problem{Pos{File: "s.proto", Line: 4, Column: 9}, "symbol \"a.v1.E\" already defined at a/v1/x\ny.proto:3:9"},
			`s.proto:4:9: symbol "a.v1.E" already defined at a/v1/x\ny.proto:3:9`},
		{Problem{Pos{File: "g.conf"}, "open a\x00b\x7f\tc"}, "g.conf: open a\\x00b\\x7f\tc"},
	}
	for _, c := range cases {
		if got := c.problem.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.problem, got, c.want)
		}
	}
}

func TestWhere(t *testing.T) {
	cases := []struct {
		pos  Pos
		want string
	}{
		{Pos{File: "g.yaml", Line: 8, Column: 3}, "on line 8"},
		{Pos{File: "g.conf", Path: "routes[0]"}, "at routes[0]"},
	}
	for _, c := range cases {
		if got := c.pos.Where(); got != c.want {
			t.Errorf("%#v.Where() = %q, want %q", c.pos, got, c.want)
		}
	}
}

// The rule every reader applies to a key or a value: NUL to U+001F and U+007F are refused, tab is read, and the first
// such character is named with the text quoted, so the problem stays on one line whatever the text holds.
func TestRefuseControl(t *testing.T) {
	const rule = "; no key or value may hold a control character other than tab"
	const block = "; a YAML block keeps its final line break unless written |- or >-"
	cases := []struct {
		name, text, want string // want is the message, or "" for no problem
	}{
		{name: "plain text", text: "/a b"},
		{name: "empty", text: ""},
		{name: "a tab", text: "a\tb"},
		{name: "text outside ASCII", text: "\u00e9\u2028"},
		{name: "U+0085, outside the range", text: "a\u0085b"},
		{name: "NUL", text: "a\x00b", want: `the value "a\x00b" contains NUL` + rule},
		{name: "a newline", text: "a\nb", want: `the value "a\nb" contains a newline` + rule},
		{name: "a carriage return", text: "a\rb", want: `the value "a\rb" contains a carriage return` + rule},
		{name: "U+0001", text: "a\x01b", want: `the value "a\x01b" contains the control character U+0001` + rule},
		{name: "U+001F", text: "a\x1fb", want: `the value "a\x1fb" contains the control character U+001F` + rule},
		{name: "U+007F", text: "a\x7fb", want: `the value "a\x7fb" contains the control character U+007F` + rule},
		{name: "the first of two", text: "a\r\nb", want: `the value "a\r\nb" contains a carriage return` + rule},
		// A YAML block written | or > ends with a line break, so the message says how to write one without it.
		{name: "one line break at the end", text: "GET\n", want: `the value "GET\n" ends with a line break` + rule + block},
		{name: "only a line break", text: "\n", want: `the value "\n" ends with a line break` + rule + block},
		{name: "two line breaks at the end", text: "a\n\n", want: `the value "a\n\n" contains a newline` + rule},
		{name: "a line break inside and at the end", text: "a\nb\n", want: `the value "a\nb\n" contains a newline` + rule},
		{name: "a carriage return before the line break at the end", text: "a\r\n",
			want: `the value "a\r\n" contains a carriage return` + rule},
		{name: "another character before the line break at the end", text: "a\x01b\n",
			want: `the value "a\x01b\n" contains the control character U+0001` + rule},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Problems
			p.RefuseControl(Pos{File: "g.yaml", Line: 1, Column: 4}, "the value", c.text)
			var got, want []string
			for _, problem := range p.Sorted() {
				got = append(got, problem.String())
			}
			if c.want != "" {
				want = []string{"g.yaml:1:4: " + c.want}
			}
			if !slices.Equal(got, want) {
				t.Errorf("problems of %q = %q, want %q", c.text, got, want)
			}
		})
	}
}

// The problem starts with the path, so the path an os error holds is left out of the message.
func TestCannotRead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"os error", &fs.PathError{Op: "open", Path: "dir/g.yaml", Err: fs.ErrNotExist},
			"dir/g.yaml: the file cannot be read: file does not exist"},
		{"other error", errors.New("is a directory"), "dir/g.yaml: the file cannot be read: is a directory"},
	}
	for _, c := range cases {
		if got := CannotRead("dir/g.yaml", c.err).String(); got != c.want {
			t.Errorf("%s: CannotRead = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSortProblems(t *testing.T) {
	cases := []struct {
		name     string
		problems []Problem
		want     []string
	}{
		{name: "file, line, column, key path, message", problems: []Problem{
			{Pos{File: "c.conf", Path: "routes[1]"}, "p"}, {Pos{File: "b.yaml", Line: 1, Column: 1}, "x"},
			{Pos{File: "a.yaml", Line: 9, Column: 1}, "y"}, {Pos{File: "a.yaml", Line: 2, Column: 5}, "z"},
			{Pos{File: "c.conf", Path: "listen"}, "q"}, {Pos{File: "a.yaml", Line: 2, Column: 5}, "a"},
			{Pos{File: "a.yaml", Line: 2, Column: 3}, "w"}, {Pos{File: "d.conf", Line: 2, Column: 1}, "s"},
			{Pos{File: "d.conf", Path: "a"}, "t"}},
			want: []string{"a.yaml:2:3: w", "a.yaml:2:5: a", "a.yaml:2:5: z", "a.yaml:9:1: y", "b.yaml:1:1: x",
				"c.conf: listen: q", "c.conf: routes[1]: p", "d.conf: a: t", "d.conf:2:1: s"}},
		// A list of ten or more items reports its problems in the order the items were written, as the YAML form does.
		{name: "an array index by its number", problems: []Problem{
			{Pos{File: "g.conf", Path: "rpc.methods[10]"}, "m"}, {Pos{File: "g.conf", Path: "rpc.methods[2]"}, "m"},
			{Pos{File: "g.conf", Path: "rpc.methods[1].reports[11]"}, "m"},
			{Pos{File: "g.conf", Path: "rpc.methods[1].reports[3]"}, "m"}, {Pos{File: "g.conf", Path: "rpc.methods[1]"}, "m"},
			{Pos{File: "g.conf", Path: "rpc.methods"}, "m"}},
			want: []string{"g.conf: rpc.methods: m", "g.conf: rpc.methods[1]: m", "g.conf: rpc.methods[1].reports[3]: m",
				"g.conf: rpc.methods[1].reports[11]: m", "g.conf: rpc.methods[2]: m", "g.conf: rpc.methods[10]: m"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			SortProblems(c.problems)
			got := make([]string, len(c.problems))
			for i, p := range c.problems {
				got[i] = p.String()
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("sorted problems = %q, want %q", got, c.want)
			}
		})
	}
}

// A stage tests Sorted for nil to learn whether it found a problem, so none must give nil, not an empty list.
func TestProblems(t *testing.T) {
	cases := []struct {
		name string
		add  func(p *Problems)
		want []string
	}{
		{name: "none", add: func(*Problems) {}},
		{name: "formatted and sorted", add: func(p *Problems) {
			p.Add(Pos{File: "g.yaml", Line: 9, Column: 1}, "route %q", "web")
			p.Add(Pos{File: "g.yaml", Line: 2, Column: 5}, "port %d", 80)
		}, want: []string{"g.yaml:2:5: port 80", `g.yaml:9:1: route "web"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Problems
			c.add(&p)
			sorted := p.Sorted()
			var got []string
			for _, problem := range sorted {
				got = append(got, problem.String())
			}
			if !slices.Equal(got, c.want) || (sorted == nil) != (c.want == nil) {
				t.Errorf("Sorted() = %#v, want the problems %q", sorted, c.want)
			}
		})
	}
}
