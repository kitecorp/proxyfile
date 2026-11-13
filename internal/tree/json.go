package tree

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// FromJSON reads a JSON file with the YAML front, which reads JSON as YAML's flow form, with the two escapes of JSON
// that go-yaml refuses read first. JSON writes a slash in a string as / or \/ (RFC 8259, section 7), and go-yaml
// refuses \/ (found unknown escape character), though YAML 1.2 defines it for JSON's sake (YAML 1.2.2, section 5.7),
// because go-yaml follows YAML 1.1 there. JSON writes a character outside the Basic Multilingual Plane as two \u
// escapes, a high surrogate and a low one (RFC 8259, section 7), as Python's json.dumps does by default, and go-yaml
// refuses every escape from D800 to DFFF (found invalid Unicode character escape code).
func FromJSON(file string, data []byte) (*Node, []model.Problem) {
	return FromYAML(file, escapesForYAML(data))
}

// pairLength is the length of a surrogate pair written as two \u escapes.
const pairLength = len(`\uHHHH\uLLLL`)

// escapesForYAML returns a copy of data with each \/ inside a string written as /, and each surrogate pair written as
// the one character it encodes. Any other escape is copied as it is, so \\/ stays an escaped backslash and a slash,
// and half a pair stays for go-yaml to refuse. After the string's closing quote it writes one space for each character
// it removed from that string, so every value after it on the line keeps its column, which go-yaml counts in
// characters; a space there changes nothing in JSON or in YAML's flow form.
func escapesForYAML(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	removed := 0
	for i := 0; i < len(data); i++ {
		b := data[i]
		pair, isPair := surrogatePair(data[i:])
		switch {
		case !inString:
			out = append(out, b)
			inString = b == '"'
		case isPair:
			out = utf8.AppendRune(out, pair)
			removed += pairLength - 1
			i += pairLength - 1
		case b == '\\' && i+1 < len(data):
			if data[i+1] == '/' {
				out = append(out, '/')
				removed++
			} else {
				out = append(out, b, data[i+1])
			}
			i++
		case b == '"':
			out = append(out, b)
			for ; removed > 0; removed-- {
				out = append(out, ' ')
			}
			inString = false
		default:
			out = append(out, b)
		}
	}
	return out
}

// surrogatePair returns the character text starts with when it starts with a surrogate pair written as two \u
// escapes, a high surrogate (D800 to DBFF) and then a low one (DC00 to DFFF), and whether it does.
func surrogatePair(text []byte) (rune, bool) {
	if len(text) < pairLength || string(text[:2]) != `\u` || string(text[6:8]) != `\u` {
		return 0, false
	}
	high, highErr := strconv.ParseUint(string(text[2:6]), 16, 16)
	low, lowErr := strconv.ParseUint(string(text[8:12]), 16, 16)
	if highErr != nil || lowErr != nil {
		return 0, false
	}
	// DecodeRune returns U+FFFD for any two values that are not a high surrogate followed by a low one, and a pair
	// never encodes U+FFFD, which lies inside the Basic Multilingual Plane.
	r := utf16.DecodeRune(rune(high), rune(low))
	return r, r != utf8.RuneError
}
