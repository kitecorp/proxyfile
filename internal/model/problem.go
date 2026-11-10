package model

import (
	"cmp"
	"fmt"
	"slices"
)

// Pos is where a problem was found. Line and Column count from 1, so 0 is
// free to mean "not known".
type Pos struct {
	File         string
	Line, Column int
}

type Problem struct {
	Pos     Pos
	Message string
}

// String drops the parts of the position that are not known rather than
// printing a 0 that an editor would read as a real line or column.
func (p Problem) String() string {
	switch {
	case p.Pos.Line == 0:
		return fmt.Sprintf("%s: %s", p.Pos.File, p.Message)
	case p.Pos.Column == 0:
		return fmt.Sprintf("%s:%d: %s", p.Pos.File, p.Pos.Line, p.Message)
	default:
		return fmt.Sprintf("%s:%d:%d: %s", p.Pos.File, p.Pos.Line, p.Pos.Column, p.Message)
	}
}

// FileProblem is a problem with the file as a whole, which names no line. It returns a list because every caller
// returns it alone, as all the problems of its stage.
func FileProblem(path, message string) []Problem {
	return []Problem{{Pos: Pos{File: path}, Message: message}}
}

// Problems collects the problems of one stage. A stage goes on after a problem, so one run names every problem of
// it.
type Problems struct {
	list []Problem
}

func (p *Problems) Add(pos Pos, format string, args ...any) {
	p.list = append(p.list, Problem{Pos: pos, Message: fmt.Sprintf(format, args...)})
}

// Sorted returns the problems in the order every stage reports them in, or nil when there are none.
func (p *Problems) Sorted() []Problem {
	SortProblems(p.list)
	return p.list
}

// SortProblems orders problems by file, line, column and message. The message
// is the last key so that two problems at the same position still come out in
// the same order on every run.
func SortProblems(ps []Problem) {
	slices.SortFunc(ps, func(a, b Problem) int {
		return cmp.Or(
			cmp.Compare(a.Pos.File, b.Pos.File),
			cmp.Compare(a.Pos.Line, b.Pos.Line),
			cmp.Compare(a.Pos.Column, b.Pos.Column),
			cmp.Compare(a.Message, b.Message),
		)
	})
}
