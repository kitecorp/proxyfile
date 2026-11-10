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

	"github.com/kitecorp/proxyfile/internal/model"
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
		return nil, yamlErrorProblems(file, err)
	}
	// Decode reads one document per call, so a second document would otherwise be dropped without a word. A
	// syntax error after the first document also surfaces only here.
	var another yaml.Node
	switch err := decoder.Decode(&another); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, yamlErrorProblems(file, err)
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
	c := converter{file: file}
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

// go-yaml marks a node whose tag was written out with TaggedStyle (parser.node in decode.go). The node is still
// converted, so problems inside a tagged mapping or list are reported in the same run.
func (c *converter) refuseTag(n *yaml.Node) {
	if n.Style&yaml.TaggedStyle != 0 {
		c.problem(n, tagMessage)
	}
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

// A syntax error ends the reading on its own, so it is the stage's only problem.
func yamlErrorProblems(file string, err error) []model.Problem {
	text := err.Error()
	if match := lineError.FindStringSubmatch(text); match != nil {
		if line, convErr := strconv.Atoi(match[1]); convErr == nil {
			return []model.Problem{{Pos: model.Pos{File: file, Line: line}, Message: match[2]}}
		}
	}
	return model.FileProblem(file, strings.TrimPrefix(text, "yaml: "))
}
