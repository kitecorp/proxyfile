package haproxy

import (
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// The keys whose values the file writes into a regular expression or a program, as the support table names them.
const (
	hostsKey    = "origins.<environment name>.hosts"
	patternsKey = "origins.<environment name>.patterns"
	// routePatternKey holds a route's pattern, whose program the file writes.
	routePatternKey = "routes[].pattern"
)

// longestRegex is the most bytes of a regular expression the file writes with a value of the proxyfile in it. HAProxy
// 3.2.26 and 3.4.7 loaded an expression of 32700 letters and refused one of 32800, past the size PCRE2 compiles (an
// expression of 16384 bytes of escaped dots loaded too); a letter, which takes two bytes compiled, grows the most of
// any character these expressions hold, so half the limit leaves room. Every other value HAProxy reads at any length:
// a word of 5,000,000 bytes loaded on both.
const longestRegex = 16384

// values writes each value the file puts into a regular expression, and each pattern's program, through one call,
// regex or program, which keeps a refusal of each value whose written form is longer than HAProxy loads or this
// version writes, by the key that holds it.
type values struct {
	problems model.Problems
	seen     map[refusedValue]bool
}

// refusedValue is a value already refused: a host is written into one expression per environment, and refused once.
type refusedValue struct {
	key string
	pos model.Pos
}

// refuse keeps the refusal of the value of key at pos, once.
func (v *values) refuse(key string, pos model.Pos, form, instead string) {
	if v.seen[refusedValue{key, pos}] {
		return
	}
	if v.seen == nil {
		v.seen = map[refusedValue]bool{}
	}
	v.seen[refusedValue{key, pos}] = true
	v.problems.Add(pos, "%s", format.FormRefusedMessage(key, form, "haproxy", instead))
}

// regex returns written, a regular expression that holds a value of key at pos, quoted, and keeps the value's refusal
// when written is longer than longestRegex.
func (v *values) regex(key string, pos model.Pos, written string) string {
	if len(written) > longestRegex {
		v.refuse(key, pos, "whose HAProxy regular expression is longer than HAProxy loads",
			"HAProxy loads no regular expression longer than 16384 bytes as the file writes it; write a shorter value, or render it for envoy")
	}
	return quote(written)
}

// program returns text, the program of the pattern at pos, quoted, and keeps the pattern's refusal when text is longer
// than longestProgram.
func (v *values) program(pos model.Pos, text string) string {
	if len(text) > longestProgram {
		v.refuse(routePatternKey, pos, "whose program is longer than this version writes",
			"this version's HAProxy file carries a pattern's program of at most 65536 bytes; write a shorter pattern, or render it for envoy")
	}
	return quote(text)
}

// refused returns the refusal of every value regex refused, or nil.
func (v *values) refused() error {
	if problems := v.problems.Sorted(); problems != nil {
		return generator.Refused{Problems: problems}
	}
	return nil
}

// mostWords is the most words HAProxy reads on one line of its file: it refused a line of 66 words with "too many
// words, truncating after word 64" and loaded one of 65 (measured on HAProxy 3.2.26 and 3.4.7).
const mostWords = 65

// overLong returns the first line of text that holds more than mostWords words, 0 for none. A word ends at a space or
// a tab outside quotes; a value in single quotes, where every character is as written, or in double quotes, where \
// escapes the next one, is part of its word, as is a character \ escapes outside quotes; a # outside quotes starts a
// comment, whose words HAProxy does not read.
func overLong(text string) int {
	for number, line := range strings.Split(text, "\n") {
		words, inWord := 0, false
		var quote byte
		for i := 0; i < len(line); i++ {
			c := line[i]
			switch {
			case quote == '\'':
				if c == '\'' {
					quote = 0
				}
				continue
			case quote == '"':
				if c == '\\' {
					i++
				} else if c == '"' {
					quote = 0
				}
				continue
			case c == ' ' || c == '\t':
				inWord = false
				continue
			case c == '#':
				i = len(line)
				continue
			}
			if !inWord {
				inWord = true
				words++
			}
			switch c {
			case '\'', '"':
				quote = c
			case '\\':
				i++
			}
		}
		if words > mostWords {
			return number + 1
		}
	}
	return 0
}
