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
	root := c.node(document.Content[0])
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

func (c *converter) node(n *yaml.Node) *Node {
	c.refuseTag(n)
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
		for _, item := range n.Content {
			list.Items = append(list.Items, c.node(item))
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
		key, keyOK := c.key(keyNode, firstLine)
		value := c.node(valueNode)
		if keyOK {
			mapping.Entries = append(mapping.Entries, Entry{Key: key, KeyPos: position(c.file, keyNode), Value: value})
		}
	}
	return mapping
}

func (c *converter) key(n *yaml.Node, firstLine map[string]int) (string, bool) {
	if n.Kind == yaml.AliasNode {
		c.problem(n, aliasMessage)
		return "", false
	}
	c.refuseTag(n)
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
// which bareTag finds. The node is still converted, so problems inside a tagged mapping or list are reported in the
// same run.
func (c *converter) refuseTag(n *yaml.Node) {
	if n.Style&yaml.TaggedStyle != 0 || c.bareTag(n) {
		c.problem(n, tagMessage)
	}
}

// bareTag reports whether n is written with the bare tag !, which go-yaml drops without a mark: parser.node sets
// TaggedStyle only for a tag that is not "!" (decode.go line 172) and resolves a plain scalar from its text, so
// "listen: ! 8000" reads as the number 8000, where the tag makes it text (YAML 1.2.2, section 10.3.2). A node's
// position is the start of its properties (yaml_parser_parse_node in parserc.go), so the text there is read: no other
// node can start with !, because a plain scalar may not (YAML 1.2.2, section 7.3.3) and a quoted one starts with its
// quote.
func (c *converter) bareTag(n *yaml.Node) bool {
	// An untagged block mapping starts where its first key does, so a tag there is the key's; a block list starts at
	// its first -. Without this rule "!!str a: 1" would be reported twice, on the mapping and on its key.
	if len(n.Content) > 0 && n.Content[0].Line == n.Line && n.Content[0].Column == n.Column {
		return false
	}
	if n.Line < 1 || n.Line > len(c.lines) {
		return false
	}
	// go-yaml counts a column in characters, not in bytes.
	line := []rune(string(c.lines[n.Line-1]))
	if n.Column < 1 || n.Column > len(line) {
		return false
	}
	rest := string(line[n.Column-1:])
	// An anchor may come before the tag: & and its name, which runs to the next space or tab.
	if strings.HasPrefix(rest, "&") {
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			return false
		}
		rest = strings.TrimLeft(rest[end:], " \t")
	}
	return strings.HasPrefix(rest, "!")
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
