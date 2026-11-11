package tree

import "github.com/kitecorp/proxyfile/internal/model"

// FromJSON reads a JSON file with the YAML front, which reads JSON as YAML's flow form, with each \/ in a string read
// as /. JSON writes a slash in a string as / or \/ (RFC 8259, section 7), and go-yaml refuses \/ (found unknown
// escape character), though YAML 1.2 defines it for JSON's sake (YAML 1.2.2, section 5.7), because go-yaml follows
// YAML 1.1 there.
func FromJSON(file string, data []byte) (*Node, []model.Problem) {
	return FromYAML(file, slashesUnescaped(data))
}

// slashesUnescaped returns a copy of data with each \/ inside a string written as /. Any other escape is copied as it
// is, so \\/ stays an escaped backslash and a slash. After the string's closing quote it writes one space for each
// backslash it removed from that string, so every value after it on the line keeps its column; a space there changes
// nothing in JSON or in YAML's flow form.
func slashesUnescaped(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	removed := 0
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case !inString:
			out = append(out, b)
			inString = b == '"'
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
