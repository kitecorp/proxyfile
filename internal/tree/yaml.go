package tree

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// A YAML position is the line and column of a value's text, and an alias's text sits at its anchor, so a problem
// reported at the alias would point at the anchor, where a fix changes every place that uses it. HOCON substitutions
// are resolved by the library before the tree exists and each resolved value is placed by its own key path, so the
// question does not arise there. So the YAML front refuses aliases, merge keys and tags rather than reading them its
// own way. Merge keys and tags have a second reason: they change what a YAML reader takes the file to say, while the
// tree keeps only the text written, so proxyfile would read another value than the reader does. When go-yaml decodes
// into a map, a merge key copies another mapping's keys in (decoder.mapping and decoder.merge in decode.go), where the
// tree would hold one key named "<<"; and it decodes "!!binary aGVsbG8=" as "hello" (decoder.scalar in decode.go),
// where the tree would hold the base64 text.
const (
	aliasMessage = "aliases are not read; write the value out"
	mergeMessage = "merge keys are not read; write the keys out"
	tagMessage   = "tags are not read; write the value without the tag"
)

// go-yaml writes a syntax error as "yaml: line N: <problem>", or as "yaml: <problem>" when it has no line
// (parser.fail in decode.go, failf in yaml.go).
var lineError = regexp.MustCompile(`(?s)^yaml: line ([0-9]+): (.*)$`)

// FromYAML returns either the root or the problems, never both, so no caller can read a tree with a value left
// out of it.
func FromYAML(file string, data []byte) (*Node, []model.Problem) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	const empty = "the file is empty"
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, model.FileProblem(file, empty)
		}
		return nil, yamlErrorProblems(file, data, err)
	}
	// Decode reads one document per call, so a second document would otherwise be dropped without a word. A
	// syntax error after the first document also surfaces only here.
	var another yaml.Node
	switch err := decoder.Decode(&another); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, yamlErrorProblems(file, data, err)
	default:
		return nil, []model.Problem{{
			Pos:     position(file, another.Content[0]),
			Message: "only one YAML document is read; this file has another",
		}}
	}
	// A document marker with nothing after it ("---") still makes a document, whose root is YAML's empty node: a
	// plain scalar with no text (YAML 1.2.2 section 7.2). Written text never gives one, because a plain scalar must
	// not be empty (section 7.3.3), so this root means the file holds no value. This test comes after the
	// second-document test, because a file whose empty first document is followed by one with content
	// ("---\n---\nb: 2") has another document rather than no value.
	if root := document.Content[0]; root.Kind == yaml.ScalarNode && root.Style == 0 && root.Value == "" {
		return nil, model.FileProblem(file, empty)
	}
	// go-yaml skips a leading byte order mark without counting a column for it (yaml_parser_determine_encoding in
	// readerc.go), so the lines a column is counted in start after it.
	c := converter{file: file, lines: bytes.Split(bytes.TrimPrefix(data, []byte("\uFEFF")), []byte("\n"))}
	root := c.node(document.Content[0], 0)
	if problems := c.Sorted(); problems != nil {
		return nil, problems
	}
	return root, nil
}

// converter goes on after a problem, so one run names every problem in the file.
type converter struct {
	model.Problems
	file string
	// lines are the file's lines, where bareTag looks for a tag go-yaml leaves no mark of.
	lines [][]byte
}

// node converts n. indent is the column a later line of n must start after, which tells a tag on such a line that is
// n's from one that starts the next key (bareTag); 0 lets a later line start anywhere.
func (c *converter) node(n *yaml.Node, indent int) *Node {
	c.refuseTag(n, indent)
	switch n.Kind {
	case yaml.ScalarNode:
		// go-yaml tags every scalar that is not plain as a string (parser.scalar in decode.go), so a literal (|) or
		// folded (>) block is text to a YAML reader just as a quoted value is.
		quoted := n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0
		// go-yaml resolves a plain scalar's tag from its text (resolve in resolve.go). Its booleans are YAML 1.2's: true
		// and false, in three capitalisations each, so yes, on, no and off are text. Its numbers also take some YAML
		// 1.1 spellings, such as 1_000 and 0b1, which stay numbers here; Node.NumberOrBool says what that costs. A
		// timestamp, which it tags !!timestamp, is not a number: YAML 1.2's core schema reads YAML 1.1's timestamps
		// as text (section 10.3.2).
		numberOrBool := !quoted && slices.Contains([]string{"!!int", "!!float", "!!bool"}, n.ShortTag())
		return &Node{Kind: Scalar, Text: scalarText(n), Quoted: quoted, NumberOrBool: numberOrBool, Pos: position(c.file, n)}
	case yaml.SequenceNode:
		list := &Node{Kind: List, Pos: position(c.file, n)}
		// In a block list the next item starts with -, never with a tag, so a later line of an item needs only to
		// start after the column the list's own lines start after.
		for _, item := range n.Content {
			list.Items = append(list.Items, c.node(item, inFlow(n, indent)))
		}
		return list
	case yaml.MappingNode:
		return c.mapping(n)
	default:
		// The one kind left is an alias: a DocumentNode only wraps the root, which FromYAML has unwrapped.
		c.problem(n, aliasMessage)
		return nil
	}
}

// go-yaml checks for a key given twice only when it decodes into a Go map or struct; into a Node it copies the
// parsed mapping as it is, both entries included (decoder.unmarshal in decode.go returns before any mapping
// check). Reading either entry would silently drop the other, so the second is refused here.
func (c *converter) mapping(n *yaml.Node) *Node {
	mapping := &Node{Kind: Map, Pos: position(c.file, n)}
	// Only looked up, never ranged: the entries keep the order the file wrote them in.
	firstLine := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keyNode, valueNode := n.Content[i], n.Content[i+1]
		// Every entry of a block mapping starts at one column, an explicit one at its ?, so a line that starts there or
		// before is not the key's or the value's.
		entryIndent := inFlow(n, c.keyColumn(n, keyNode))
		key, keyOK := c.key(keyNode, firstLine, entryIndent)
		value := c.node(valueNode, entryIndent)
		if keyOK {
			mapping.Entries = append(mapping.Entries, Entry{Key: key, KeyPos: position(c.file, keyNode), Value: value})
		}
	}
	return mapping
}

// keyColumn returns the column the entries of the block mapping n start at, for the entry whose key is key: the column
// of an explicit key's ?, or the key's own. The key of an explicit entry starts after its ? on the same line, so the ?
// is the last character before it there; a mapping that starts at key itself, as the key of "? a: 1" does, holds no ?
// of its own on that line.
func (c *converter) keyColumn(n, key *yaml.Node) int {
	if key.Line == n.Line && key.Column == n.Column {
		return key.Column
	}
	text := c.chars(key.Line)
	i := min(key.Column-2, len(text)-1)
	for i >= 0 && blank(text[i]) {
		i--
	}
	if i >= 0 && text[i] == '?' {
		return i + 1
	}
	return key.Column
}

func (c *converter) key(n *yaml.Node, firstLine map[string]int, indent int) (string, bool) {
	if n.Kind == yaml.AliasNode {
		c.problem(n, aliasMessage)
		return "", false
	}
	c.refuseTag(n, indent)
	if n.Kind != yaml.ScalarNode {
		c.problem(n, "a key must be a single value")
		return "", false
	}
	// go-yaml tags a plain "<<" as a merge key (parser.scalar in decode.go); a quoted "<<" is an ordinary key.
	if n.ShortTag() == "!!merge" {
		c.problem(n, mergeMessage)
		return "", false
	}
	key := scalarText(n)
	if line, seen := firstLine[key]; seen {
		c.problem(n, "the key %q is given twice (first on line %d)", key, line)
		return "", false
	}
	firstLine[key] = n.Line
	return key, true
}

// go-yaml marks a node whose tag was written out with TaggedStyle (parser.node in decode.go), except the bare tag !,
// which only the text shows: parser.node sets TaggedStyle only for a tag that is not "!" (decode.go line 172) and
// resolves a plain scalar from its text, so "listen: ! 8000" reads as the number 8000, where the tag makes it text
// (YAML 1.2.2, section 10.3.2). Either tag is reported at n when it is on n's first line, and at the tag when it is on
// a later line, so the problem names the line the tag is on. The node is still converted, so problems inside a tagged
// mapping or list are reported in the same run.
func (c *converter) refuseTag(n *yaml.Node, indent int) {
	line, column, written := c.tagAt(n, indent)
	if n.Style&yaml.TaggedStyle == 0 && !written {
		return
	}
	if written && line != n.Line {
		c.Add(model.Pos{File: c.file, Line: line, Column: column}, tagMessage)
		return
	}
	c.problem(n, tagMessage)
}

// tagAt returns the line and column of the tag written on n, and whether n's text has one there. A node's position is
// the start of its properties (yaml_parser_parse_node in parserc.go), so the text there is read: no other node can
// start with !, because a plain scalar may not (YAML 1.2.2, section 7.3.3) and a quoted one starts with its quote.
//
// An anchor may come before the tag, on the same line or on an earlier one with comments and blank lines between
// (YAML 1.2.2, section 6.7), each line starting after indent. A line that does not is the next key's, as in "a: &x"
// followed by "! b: 1", or what follows the list n is in.
func (c *converter) tagAt(n *yaml.Node, indent int) (int, int, bool) {
	// An untagged block mapping starts where its first key does, so the properties there are the key's; a block list
	// starts at its first -. Without this rule "!!str a: 1" and "&a ! k: 1" would be reported twice, on the mapping and
	// on its key.
	if len(n.Content) > 0 && n.Content[0].Line == n.Line && n.Content[0].Column == n.Column {
		return 0, 0, false
	}
	line, column := n.Line, n.Column
	text := c.chars(line)
	if column < 1 || column > len(text) {
		return 0, 0, false
	}
	if text[column-1] == '&' {
		// go-yaml reads an anchor's name as letters, digits, _ and - only (yaml_parser_scan_anchor and is_alpha), so in
		// "[&a, ! 1]" the comma ends the item and the tag is the next item's.
		end := column
		for end < len(text) && anchorChar(text[end]) {
			end++
		}
		if line, column = c.textAfter(line, end+1); line == 0 || (line != n.Line && column <= indent) {
			return 0, 0, false
		}
	}
	if c.chars(line)[column-1] != '!' {
		return 0, 0, false
	}
	// A mapping's anchor may stand on a line of its own, and its first key's tag start the next one.
	if len(n.Content) > 0 && n.Content[0].Line == line && n.Content[0].Column == column {
		return 0, 0, false
	}
	return line, column, true
}

// textAfter returns the line and column of the first character from column of line on, on that line or a later one,
// that is not a space, a tab or part of a comment, or 0, 0 when the file ends first. It starts right after an anchor's
// name, which go-yaml accepts only when a space, a tab, the line's end or one of ? : , ] } % @ ` follows it
// (yaml_parser_scan_anchor), so a # it meets stands after a space or at a line's start, and starts a comment.
func (c *converter) textAfter(line, column int) (int, int) {
lines:
	for ; line <= len(c.lines); line, column = line+1, 1 {
		text := c.chars(line)
		for i := column - 1; i < len(text); i++ {
			switch {
			case text[i] == '#':
				continue lines
			case !blank(text[i]):
				return line, i + 1
			}
		}
	}
	return 0, 0
}

// chars returns line number line of the file as characters, in which go-yaml counts a column, or nil for a line the
// file does not have.
func (c *converter) chars(line int) []rune {
	if line < 1 || line > len(c.lines) {
		return nil
	}
	return []rune(string(c.lines[line-1]))
}

// blank reports whether r separates two things on a line: a space, a tab, or the carriage return a CRLF line ends with.
func blank(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r'
}

// anchorChar reports whether go-yaml reads r as part of an anchor's name (is_alpha in yamlprivateh.go).
func anchorChar(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '-'
}

// inFlow returns the indent the children of n are converted with: column, unless n is written in braces or brackets,
// where only a comma or the closing bracket ends a child, so a later line of a child's properties may start anywhere.
func inFlow(n *yaml.Node, column int) int {
	if n.Style&yaml.FlowStyle != 0 {
		return 0
	}
	return column
}

func (c *converter) problem(n *yaml.Node, format string, args ...any) {
	c.Add(position(c.file, n), format, args...)
}

// go-yaml has already removed the quotes from Value, but a null keeps its spelling there ("null", "~" or
// nothing at all), so the resolved tag decides.
func scalarText(n *yaml.Node) string {
	if n.ShortTag() == "!!null" {
		return ""
	}
	return n.Value
}

// go-yaml counts lines and columns from 1, as model.Pos does (parser.node in decode.go adds 1 to its marks).
func position(file string, n *yaml.Node) model.Pos {
	return model.Pos{File: file, Line: n.Line, Column: n.Column}
}

// parserProblems are the texts of the parser's errors: those it passes to yaml_parser_set_parser_error and
// yaml_parser_set_parser_error_context (parserc.go). go-yaml counts a parser error's line from 0, and a scanner
// error's from 1 (parser.fail in decode.go), so only an error with one of these texts needs one added to its line.
var parserProblems = []string{
	"did not find expected <stream-start>",
	"did not find expected <document start>",
	"did not find expected node content",
	"did not find expected key",
	"did not find expected '-' indicator",
	"did not find expected ',' or ']'",
	"did not find expected ',' or '}'",
	"found duplicate %YAML directive",
	"found incompatible YAML document",
	"found duplicate %TAG directive",
	"found undefined tag handle",
}

// A syntax error ends the reading on its own, so it is the stage's only problem. It names the line it is on, counted
// from 1. go-yaml writes no line when the line it computed is 0, which a parser error on line 1 and a scanner error on
// line 1 both give (parser.fail in decode.go), and none at all for an error without a mark (a reader error, such as
// a control character, or an unknown anchor). So an error without a line is placed on line 1 when the first line
// read alone fails with the same text; otherwise it stays a problem of the file as a whole.
func yamlErrorProblems(file string, data []byte, err error) []model.Problem {
	text := err.Error()
	if match := lineError.FindStringSubmatch(text); match != nil {
		if line, convErr := strconv.Atoi(match[1]); convErr == nil {
			if slices.Contains(parserProblems, match[2]) {
				line++
			}
			return []model.Problem{{Pos: model.Pos{File: file, Line: line}, Message: match[2]}}
		}
	}
	message := strings.TrimPrefix(text, "yaml: ")
	firstLine, _, _ := bytes.Cut(data, []byte("\n"))
	var ignored yaml.Node
	if firstErr := yaml.Unmarshal(firstLine, &ignored); firstErr != nil && firstErr.Error() == text {
		return []model.Problem{{Pos: model.Pos{File: file, Line: 1}, Message: message}}
	}
	return model.FileProblem(file, message)
}
