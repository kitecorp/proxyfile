package model

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
)

// Pos is where a problem was found. Line and Column count from 1, so 0 is
// free to mean "not known". Path places a value whose syntax gives it no line,
// which is every value of a HOCON file: the HOCON library keeps no position for
// a value, only for a syntax error.
type Pos struct {
	File         string
	Line, Column int
	Path         string // the key path, where the syntax gives no line: services.api.http, routes[1].to
}

// Where names the position inside a message that points at another place: "on line 8" in YAML, "at routes[0]" in
// HOCON, whose values have no line.
func (p Pos) Where() string {
	if p.Line != 0 {
		return fmt.Sprintf("on line %d", p.Line)
	}
	return "at " + p.Path
}

type Problem struct {
	Pos     Pos
	Message string
}

// String drops the parts of the position that are not known rather than
// printing a 0 that an editor would read as a real line or column.
func (p Problem) String() string {
	switch {
	case p.Pos.Line == 0 && p.Pos.Path != "":
		return fmt.Sprintf("%s: %s: %s", p.Pos.File, p.Pos.Path, p.Message)
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

// CannotRead is the problem of a file that cannot be read. Every reader of every form reports it with this one text,
// so a missing file reads the same whichever reader met it. The problem already starts with the path, so the path an
// os error holds is left out rather than written twice.
func CannotRead(path string, err error) Problem {
	reason := err.Error()
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		reason = pathErr.Err.Error()
	}
	return Problem{Pos: Pos{File: path}, Message: "the file cannot be read: " + reason}
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

// SortProblems orders problems by file, line, column, key path and message. The
// message is the last key so that two problems at the same position still come
// out in the same order on every run. A YAML problem has no key path, so YAML
// problems keep the order they had before the key path existed.
func SortProblems(ps []Problem) {
	slices.SortFunc(ps, func(a, b Problem) int {
		return cmp.Or(
			cmp.Compare(a.Pos.File, b.Pos.File),
			cmp.Compare(a.Pos.Line, b.Pos.Line),
			cmp.Compare(a.Pos.Column, b.Pos.Column),
			comparePaths(a.Pos.Path, b.Pos.Path),
			cmp.Compare(a.Message, b.Message),
		)
	})
}

// arrayIndex is an array item's index in a key path, as in routes[10].to.
var arrayIndex = regexp.MustCompile(`\[[0-9]+\]`)

// comparePaths orders key paths as text, except that array indexes compare as numbers: as text, hosts[10] would come
// before hosts[2], out of the order the items were written in. Each path is cut at its indexes, so its pieces
// alternate between text, which may be empty, and an index, and the pieces are compared in turn.
func comparePaths(a, b string) int {
	aPieces, bPieces := pathPieces(a), pathPieces(b)
	for i := range min(len(aPieces), len(bPieces)) {
		c := cmp.Compare(aPieces[i], bPieces[i])
		if i%2 == 1 {
			// The tree package writes an index with strconv.Itoa, so it has no leading zero: the shorter index is the
			// smaller number, and two of the same length compare as text.
			c = cmp.Or(cmp.Compare(len(aPieces[i]), len(bPieces[i])), c)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(aPieces), len(bPieces))
}

// pathPieces cuts a key path before and after each array index: routes[10].to gives routes, [10] and .to.
func pathPieces(path string) []string {
	var pieces []string
	start := 0
	for _, index := range arrayIndex.FindAllStringIndex(path, -1) {
		pieces = append(pieces, path[start:index[0]], path[index[0]:index[1]])
		start = index[1]
	}
	return append(pieces, path[start:])
}
