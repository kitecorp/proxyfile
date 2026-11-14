package tree

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// includes returns the files named by the include statements of the HOCON file at path and of every file they name,
// each once, in the order first named, and a problem for each include file(...).
//
// A name is read as the library reads it: a relative name beside the file that holds the statement, an absolute one
// as written (o3co internal/resolver/resolver.go:1628-1641, with the included file's directory as the base at 1835),
// and a name without an extension as the three names the library tries, in its order (resolver.go:1613). Each of the
// three is listed, also one that does not exist, because an output written there would be read by the next run. A
// file that cannot be read is skipped: the library skips a missing plain include and names a missing required one in
// its own words. A .properties file is listed but not scanned, because the library reads it as properties, not HOCON
// (resolver.go:1783-1785).
func includes(path string) ([]string, []model.Problem) {
	var files []string
	var problems []model.Problem
	// The proxyfile is seen from the start, so an include that names it back is not listed as an input of its own.
	seen := map[string]bool{Absolute(path): true}
	var scan func(file string)
	scan = func(file string) {
		data, err := os.ReadFile(file)
		if err != nil {
			return
		}
		for _, s := range statements(file, data) {
			switch s.resource {
			case "file":
				problems = append(problems, model.Problem{Pos: s.pos, Message: fileIncludeMessage(s)})
				continue
			case "url", "classpath", "package":
				// These name no file of this tree, and the library refuses each: url and classpath as not supported
				// (parser.go:396-399 and 506-509), a package because proxyfile gives it no package lookup
				// (resolver.go:1872-1888).
				continue
			}
			named := Beside(file, s.name)
			names := []string{named}
			if filepath.Ext(named) == "" {
				names = []string{named + ".properties", named + ".json", named + ".conf"}
			}
			for _, name := range names {
				if seen[Absolute(name)] {
					continue
				}
				seen[Absolute(name)] = true
				files = append(files, name)
				if filepath.Ext(name) != ".properties" {
					scan(name)
				}
			}
		}
	}
	scan(path)
	return files, problems
}

// fileIncludeMessage names the plain include that reads the same name beside the file, which is what a proxyfile
// author means, whatever directory the run starts in.
func fileIncludeMessage(s statement) string {
	plain := strconv.Quote(s.name)
	if s.required {
		plain = "required(" + plain + ")"
	}
	return "include file(...) is read from the working directory, not beside this file; write include " + plain
}

// Beside returns the path a name written in file reads: a relative name from file's own directory, so the same file
// reads the same files from any working directory, and an absolute one as written, because joining it under that
// directory would name a file that is not the one written. HOCON reads an include's name so, and render reads the
// origins file and each rpc.proto root a proxyfile names the same way, so every file a proxyfile names is found by one
// rule.
func Beside(file, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(filepath.Dir(file), name)
}

// Absolute is the path two spellings of one file share, so ./a.yaml and a.yaml are one file: the includes' set of
// files seen compares paths by it, and the command line's rule that no output names an input starts from it for an
// output that is not there yet. It falls back to the cleaned path when the working directory cannot be read, so two
// relative paths still compare by their text.
func Absolute(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// statement is one include statement as written: the position of its word include, the resource word inside the
// parentheses ("" for a plain name), whether required(...) wraps it, and the name.
type statement struct {
	pos      model.Pos
	resource string
	required bool
	name     string
}

// includeTail is what follows the word include in a statement: an optional required(, an optional resource word with
// its (, and the quoted name (o3co internal/parser/parser.go:291-303 and 348-400). \s takes the newlines the parser
// skips between the parts. The closing parentheses are left to the scan, which reads them as plain text.
var includeTail = regexp.MustCompile(`^\s*(required\s*\(\s*)?(?:(file|url|classpath|package)\s*\(\s*)?"((?:[^"\\\n]|\\.)*)"`)

// statements finds the include statements of one file in one forward scan. The parser reads include as a statement
// only where an object's field starts (parser.go:224-237), and as plain text in a value (parser.go:1013-1020), so the
// scan keeps the brackets open and skips what holds no field: comments, strings and array items. Line and column
// count from 1, the column in characters, as the library's positions do (o3co internal/lexer/lexer.go:144-153).
func statements(file string, data []byte) []statement {
	// The lexer strips a leading byte order mark before it counts a column (lexer.go:128-134).
	text := strings.TrimPrefix(string(data), "\uFEFF")
	var found []statement
	var open []rune // the brackets open, innermost last
	fieldStart := true
	line, column, i := 1, 1, 0
	advance := func(n int) {
		for _, r := range text[i : i+n] {
			if r == '\n' {
				line, column = line+1, 1
			} else {
				column++
			}
		}
		i += n
	}
	for i < len(text) {
		rest := text[i:]
		r, size := utf8.DecodeRuneInString(rest)
		switch {
		case r == '\n':
			fieldStart = true
			advance(size)
		case r == '#' || strings.HasPrefix(rest, "//"):
			advance(lineLength(rest))
		case strings.HasPrefix(rest, `"""`):
			fieldStart = false
			advance(tripleQuotedLength(rest))
		case r == '"':
			fieldStart = false
			advance(quotedLength(rest))
		case r == '{':
			open = append(open, r)
			fieldStart = true
			advance(size)
		case r == '[':
			open = append(open, r)
			fieldStart = false
			advance(size)
		case r == '}' || r == ']':
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
			fieldStart = false
			advance(size)
		case r == ',':
			fieldStart = true
			advance(size)
		case isHOCONSpace(r):
			advance(size)
		default:
			// An array item is a value, so a field starts only in an object or at the top of the file.
			if fieldStart && (len(open) == 0 || open[len(open)-1] == '{') {
				if s, length := includeAt(rest); length > 0 {
					s.pos = model.Pos{File: file, Line: line, Column: column}
					found = append(found, s)
					advance(length)
					fieldStart = false
					continue
				}
			}
			fieldStart = false
			advance(size)
		}
	}
	return found
}

// includeAt reads the statement at the start of text and returns it with its length in bytes, or a length of 0 when
// text holds no statement there. The lexer reads include as its keyword only where the word ends (lexer.go:858-906),
// and a statement goes on with whitespace or the quote of its name, so includes or include( is another word.
func includeAt(text string) (statement, int) {
	const word = "include"
	if !strings.HasPrefix(text, word) || len(text) == len(word) || !strings.ContainsRune(" \t\n\f\r\"", rune(text[len(word)])) {
		return statement{}, 0
	}
	m := includeTail.FindStringSubmatch(text[len(word):])
	if m == nil {
		return statement{}, 0
	}
	name := m[3]
	if unquoted, err := strconv.Unquote(`"` + m[3] + `"`); err == nil {
		name = unquoted
	}
	return statement{resource: m[2], required: m[1] != "", name: name}, len(word) + len(m[0])
}

// lineLength is the length of text up to its first newline, which stays for the scan to count.
func lineLength(text string) int {
	if end := strings.IndexByte(text, '\n'); end >= 0 {
		return end
	}
	return len(text)
}

// tripleQuotedLength is the length of the """ string at the start of text. It ends at the next run of three quotes or
// more, the whole run, as the lexer reads it (lexer.go:379-402).
func tripleQuotedLength(text string) int {
	end := strings.Index(text[3:], `"""`)
	if end < 0 {
		return len(text)
	}
	end += 6
	for end < len(text) && text[end] == '"' {
		end++
	}
	return end
}

// quotedLength is the length of the " string at the start of text: to the next quote no backslash escapes, or to the
// end of the line, which no such string goes past (lexer.go:291-298).
func quotedLength(text string) int {
	for j := 1; j < len(text); j++ {
		switch text[j] {
		case '\n':
			return j
		case '"':
			return j + 1
		case '\\':
			if j+1 < len(text) && text[j+1] != '\n' {
				j++
			}
		}
	}
	return len(text)
}

// isHOCONSpace is the whitespace HOCON allows between tokens but the newline, which ends a field (lexer.go:821-839).
func isHOCONSpace(r rune) bool {
	return unicode.Is(unicode.Zs, r) || strings.ContainsRune("\t\v\f\r\x1c\x1d\x1e\x1f\u2028\u2029\uFEFF", r)
}
