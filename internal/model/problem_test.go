package model

import (
	"slices"
	"testing"
)

func TestProblemString(t *testing.T) {
	cases := []struct {
		problem Problem
		want    string
	}{
		{Problem{Pos{"g.yaml", 3, 5}, "x"}, "g.yaml:3:5: x"},
		{Problem{Pos{"g.yaml", 3, 0}, "x"}, "g.yaml:3: x"},
		{Problem{Pos{"g.yaml", 0, 0}, "x"}, "g.yaml: x"},
	}
	for _, c := range cases {
		if got := c.problem.String(); got != c.want {
			t.Errorf("%#v.String() = %q, want %q", c.problem, got, c.want)
		}
	}
}

func TestSortProblems(t *testing.T) {
	ps := []Problem{{Pos{"b.yaml", 1, 1}, "x"}, {Pos{"a.yaml", 9, 1}, "y"}, {Pos{"a.yaml", 2, 5}, "z"},
		{Pos{"a.yaml", 2, 5}, "a"}, {Pos{"a.yaml", 2, 3}, "w"}}
	SortProblems(ps)
	got := make([]string, len(ps))
	for i, p := range ps {
		got[i] = p.String()
	}
	want := []string{"a.yaml:2:3: w", "a.yaml:2:5: a", "a.yaml:2:5: z", "a.yaml:9:1: y", "b.yaml:1:1: x"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted problems = %q, want %q", got, want)
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
			p.Add(Pos{"g.yaml", 9, 1}, "route %q", "web")
			p.Add(Pos{"g.yaml", 2, 5}, "port %d", 80)
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
