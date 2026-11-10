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
