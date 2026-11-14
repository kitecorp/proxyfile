// Package pcre writes a route's pattern, which the reader checks with Go's regexp and Envoy runs with RE2, as PCRE2
// reads it in the byte mode nginx compiles its regular expressions in: from Go's own parse of the pattern, so PCRE2 is
// never handed RE2 text it reads another way ({,3}, { 2 } and \v are text or other characters in RE2 and repetitions
// or vertical space in PCRE2, and \Q with no \E would quote the text written after the pattern).
package pcre

import (
	"fmt"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Write returns PCRE2 text that takes exactly the paths of ASCII characters the pattern takes, the only paths Envoy
// matches a pattern against: Envoy refuses a byte above 0x7F in an HTTP/1.1 path and escapes it in an HTTP/2 one, and
// matches the path with every such escape kept (measured on Envoy 1.40.2). A character outside ASCII written in the
// pattern therefore matches nothing, and a class takes a byte above 0x7F only when it takes every character outside
// ASCII, as [^/] and . do; nginx meets such a byte only decoded from an escape, the difference its declaration names.
// Its error is an operation of Go's parse this writer does not know: a bug in proxyfile, because the reader has
// compiled the same pattern.
func Write(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	var w strings.Builder
	if err := write(&w, re); err != nil {
		return "", fmt.Errorf("the pattern %q: %w", pattern, err)
	}
	return w.String(), nil
}

// word is the class \b reads, ASCII letters, digits and _, as Go reads it.
const word = `[0-9A-Z\_a-z]`

// write writes re. Every quantifier applies to an atom: a group, a class or one character.
func write(w *strings.Builder, re *syntax.Regexp) error {
	switch re.Op {
	case syntax.OpNoMatch:
		w.WriteString(`(?!)`)
	case syntax.OpEmptyMatch:
		w.WriteString(`(?:)`)
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 {
				writeClass(w, folded(r))
			} else {
				writeRune(w, r)
			}
		}
	case syntax.OpCharClass:
		writeClass(w, re.Rune)
	case syntax.OpAnyCharNotNL:
		writeClass(w, []rune{0, '\n' - 1, '\n' + 1, unicode.MaxRune})
	case syntax.OpAnyChar:
		writeClass(w, []rune{0, unicode.MaxRune})
	case syntax.OpBeginLine:
		w.WriteString(`(?:\A|(?<=\n))`)
	case syntax.OpEndLine:
		w.WriteString(`(?:\z|(?=\n))`)
	case syntax.OpBeginText:
		w.WriteString(`\A`)
	case syntax.OpEndText:
		// Go's $ outside multi-line mode is the end of the text, which PCRE2's $ is not: it also matches before a
		// final line break.
		w.WriteString(`\z`)
	case syntax.OpWordBoundary:
		w.WriteString(`(?:(?<=` + word + `)(?!` + word + `)|(?<!` + word + `)(?=` + word + `))`)
	case syntax.OpNoWordBoundary:
		w.WriteString(`(?:(?<=` + word + `)(?=` + word + `)|(?<!` + word + `)(?!` + word + `))`)
	case syntax.OpCapture:
		if re.Sub[0].Op == syntax.OpAlternate {
			return write(w, re.Sub[0])
		}
		return group(w, re.Sub...)
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		if err := atom(w, re.Sub[0]); err != nil {
			return err
		}
		w.WriteString(map[syntax.Op]string{syntax.OpStar: "*", syntax.OpPlus: "+", syntax.OpQuest: "?"}[re.Op])
		lazy(w, re)
	case syntax.OpRepeat:
		if err := atom(w, re.Sub[0]); err != nil {
			return err
		}
		switch {
		case re.Max == re.Min:
			fmt.Fprintf(w, "{%d}", re.Min)
		case re.Max < 0:
			fmt.Fprintf(w, "{%d,}", re.Min)
		default:
			fmt.Fprintf(w, "{%d,%d}", re.Min, re.Max)
		}
		lazy(w, re)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if err := write(w, sub); err != nil {
				return err
			}
		}
	case syntax.OpAlternate:
		w.WriteString("(?:")
		for i, sub := range re.Sub {
			if i > 0 {
				w.WriteString("|")
			}
			if err := write(w, sub); err != nil {
				return err
			}
		}
		w.WriteString(")")
	default:
		return fmt.Errorf("holds %v, which proxyfile does not write for PCRE2", re.Op)
	}
	return nil
}

// atom writes re as one atom, in a group unless it is one already.
func atom(w *strings.Builder, re *syntax.Regexp) error {
	switch {
	case re.Op == syntax.OpCharClass, re.Op == syntax.OpAnyChar, re.Op == syntax.OpAnyCharNotNL,
		re.Op == syntax.OpCapture, re.Op == syntax.OpLiteral && len(re.Rune) == 1:
		return write(w, re)
	}
	return group(w, re)
}

// group writes the subexpressions in a group that captures nothing.
func group(w *strings.Builder, subs ...*syntax.Regexp) error {
	w.WriteString("(?:")
	for _, sub := range subs {
		if err := write(w, sub); err != nil {
			return err
		}
	}
	w.WriteString(")")
	return nil
}

// lazy marks a repetition Go reads as non-greedy.
func lazy(w *strings.Builder, re *syntax.Regexp) {
	if re.Flags&syntax.NonGreedy != 0 {
		w.WriteString("?")
	}
}

// folded is every character Go's case-insensitive mode matches for r, as class ranges.
func folded(r rune) []rune {
	ranges := []rune{r, r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		ranges = append(ranges, f, f)
	}
	return ranges
}

// writeRune writes one character to match. A character outside ASCII matches nothing (Write says why).
func writeRune(w *strings.Builder, r rune) {
	if r >= utf8.RuneSelf {
		w.WriteString(`(?!)`)
		return
	}
	writeByte(w, byte(r))
}

// writeByte writes b as PCRE2 reads it as a literal, in or out of a class: a letter or digit as it is, other printable
// ASCII after a backslash, which PCRE2 always reads as the character itself, and anything else in hexadecimal.
func writeByte(w *strings.Builder, b byte) {
	switch {
	case b >= '0' && b <= '9', b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z':
		w.WriteByte(b)
	case b > ' ' && b < 0x7F:
		w.WriteByte('\\')
		w.WriteByte(b)
	default:
		fmt.Fprintf(w, `\x%02X`, b)
	}
}

// writeClass writes the class of Go's ranges (pairs of first and last character) as byte ranges: their ASCII part,
// and the bytes above 0x7F when the class takes every character outside ASCII. A class left with no byte matches
// nothing, and one of a single byte is written as that byte.
func writeClass(w *strings.Builder, ranges []rune) {
	var bytes [][2]rune
	add := func(lo, hi rune) {
		if last := len(bytes) - 1; last >= 0 && bytes[last][1]+1 == lo {
			bytes[last][1] = hi
			return
		}
		bytes = append(bytes, [2]rune{lo, hi})
	}
	for i := 0; i+1 < len(ranges); i += 2 {
		if lo, hi := ranges[i], min(ranges[i+1], utf8.RuneSelf-1); lo <= hi {
			add(lo, hi)
		}
	}
	if coversNonASCII(ranges) {
		add(utf8.RuneSelf, 0xFF)
	}
	switch {
	case len(bytes) == 0:
		w.WriteString(`(?!)`)
		return
	case len(bytes) == 1 && bytes[0][0] == bytes[0][1]:
		writeByte(w, byte(bytes[0][0]))
		return
	}
	w.WriteString("[")
	for _, r := range bytes {
		writeByte(w, byte(r[0]))
		if r[1] > r[0] {
			w.WriteString("-")
			writeByte(w, byte(r[1]))
		}
	}
	w.WriteString("]")
}

// coversNonASCII reports whether the ranges take every character from 0x80 up, as one range: Go's parse merges
// adjacent ranges of a class.
func coversNonASCII(ranges []rune) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if ranges[i] <= utf8.RuneSelf && ranges[i+1] >= unicode.MaxRune {
			return true
		}
	}
	return false
}
