package model

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
// printing a 0 that an editor would read as a real line or column. It keeps a
// problem to one line: a file whose name holds a control character, as a .proto
// file found under a root may, is written quoted, and a control character in the
// message is written as its escape. proxyfile's own messages quote the text they
// write, but a library's, protocompile's or the HOCON library's, writes a file's
// name as it is.
func (p Problem) String() string {
	file := OneLinePath(p.Pos.File)
	message := OneLine(p.Message)
	switch {
	case p.Pos.Line == 0 && p.Pos.Path != "":
		return fmt.Sprintf("%s: %s: %s", file, p.Pos.Path, message)
	case p.Pos.Line == 0:
		return fmt.Sprintf("%s: %s", file, message)
	case p.Pos.Column == 0:
		return fmt.Sprintf("%s:%d: %s", file, p.Pos.Line, message)
	default:
		return fmt.Sprintf("%s:%d:%d: %s", file, p.Pos.Line, p.Pos.Column, message)
	}
}

// OneLinePath returns path as a message writes it: quoted when it holds a control character other than tab, so the
// message stays one line and a path that holds the two characters \ and n reads apart from one that holds a newline.
func OneLinePath(path string) string {
	if _, found := firstControl(path); found {
		return strconv.Quote(path)
	}
	return path
}

// OneLine returns text with each control character other than tab written as Go writes it in a quoted string, such as
// \n or \x00, so a message that holds text it did not write, such as a library's error, stays one line.
func OneLine(text string) string {
	if _, found := firstControl(text); !found {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		if control(r) {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// firstControl returns the first control character other than tab that text holds, and whether it holds one.
func firstControl(text string) (rune, bool) {
	for _, r := range text {
		if control(r) {
			return r, true
		}
	}
	return 0, false
}

// control reports whether r is a control character other than tab: NUL to U+001F, or U+007F.
func control(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7F
}

// controlName names a control character by the name a reader knows it by, and the rest by their code point.
func controlName(r rune) string {
	switch r {
	case 0:
		return "NUL"
	case '\n':
		return "a newline"
	case '\r':
		return "a carriage return"
	default:
		return fmt.Sprintf("the control character U+%04X", r)
	}
}

// FileProblem is a problem with the file as a whole, which names no line. It returns a list because every caller
// returns it alone, as all the problems of its stage.
func FileProblem(path, message string) []Problem {
	return []Problem{{Pos: Pos{File: path}, Message: message}}
}

// CannotRead is the problem of a file that cannot be read. Every reader of every form reports it with this one text,
// so a missing file reads the same whichever reader met it.
func CannotRead(path string, err error) Problem {
	return Problem{Pos: Pos{File: path}, Message: "the file cannot be read: " + Reason(err)}
}

// Reason is why a file or directory cannot be read, as a problem words it. The message it goes in names the path
// already, so the path an os error holds is left out rather than written twice.
func Reason(err error) string {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// Problems collects the problems of one stage. A stage goes on after a problem, so one run names every problem of
// it.
type Problems struct {
	list []Problem
}

func (p *Problems) Add(pos Pos, format string, args ...any) {
	p.list = append(p.list, Problem{Pos: pos, Message: fmt.Sprintf(format, args...)})
}

// RefuseControl adds a problem at pos when text, a key or a value that subject names, such as "the value", "the key" or
// "the action", holds a control character other than tab: NUL to U+001F, or U+007F. Every reader of every input refuses
// these with this one rule, because written into the generated file as they are they break what holds them, as
// measured on Envoy 1.40.2: it refuses a file whose header value holds NUL, a newline or a carriage return, or whose
// health check path holds any control character but tab, and it loads a route whose prefix holds one, which no request
// can match, and a virtual host named with one. The message quotes text and names its first such character, except
// when the only one is a line break at the end, which a YAML block written | or > keeps: then it says how to drop it.
func (p *Problems) RefuseControl(pos Pos, subject, text string) {
	const rule = "no key or value may hold a control character other than tab"
	if body, cut := strings.CutSuffix(text, "\n"); cut {
		if _, found := firstControl(body); !found {
			p.Add(pos, "%s %q ends with a line break; %s; a YAML block keeps its final line break unless written |- or >-",
				subject, text, rule)
			return
		}
	}
	if r, found := firstControl(text); found {
		p.Add(pos, "%s %q contains %s; %s", subject, text, controlName(r), rule)
	}
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
