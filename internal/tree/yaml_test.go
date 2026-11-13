package tree

import (
	"bytes"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

func keys(n *Node) []string {
	names := make([]string, len(n.Entries))
	for i, entry := range n.Entries {
		names[i] = entry.Key
	}
	return names
}

func TestFromYAMLKeepsOrderAndPositions(t *testing.T) {
	root, problems := FromYAML("g.yaml", []byte("listen: 10000\nservices:\n  shop: {http: 8080, grpc: 8081}\n"))
	if problems != nil {
		t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	if got, want := keys(root), []string{"listen", "services"}; !slices.Equal(got, want) {
		t.Errorf("root keys = %q, want %q", got, want)
	}
	listen := root.Get("listen")
	if want := (model.Pos{File: "g.yaml", Line: 1, Column: 9}); listen.Text != "10000" || listen.Pos != want {
		t.Errorf("listen = %q at %v, want %q at %v", listen.Text, listen.Pos, "10000", want)
	}
	shop := root.Get("services").Get("shop")
	if got, want := keys(shop), []string{"http", "grpc"}; !slices.Equal(got, want) {
		t.Errorf("shop keys = %q, want %q", got, want)
	}
	if got, want := shop.Get("grpc").Pos, (model.Pos{File: "g.yaml", Line: 3, Column: 28}); got != want {
		t.Errorf("grpc value at %v, want %v", got, want)
	}
	if got, want := shop.Entries[1].KeyPos, (model.Pos{File: "g.yaml", Line: 3, Column: 22}); got != want {
		t.Errorf("grpc key at %v, want %v", got, want)
	}
}

// The quotes are dropped from the text and a YAML null is empty text, so a later stage sees one value however the
// file wrote it; Quoted keeps whether the value was written as text (in quotes or as a block), which the format check
// reads where a number or a boolean is expected, and NumberOrBool whether a plain value is a number or a boolean to
// a YAML reader, which it reads where text is expected.
func TestFromYAMLScalarText(t *testing.T) {
	cases := []struct {
		input, want          string
		quoted, numberOrBool bool
	}{
		{"v: plain\n", "plain", false, false},
		{"v: \"double\"\n", "double", true, false},
		{"v: 'single'\n", "single", true, false},
		{"v:\n", "", false, false},
		{"v: null\n", "", false, false},
		{"v: ~\n", "", false, false},
		{"v: \"null\"\n", "null", true, false},
		{"v: |-\n  8000\n", "8000", true, false},
		{"v: >-\n  8000\n", "8000", true, false},
		{"v: 404\n", "404", false, true},
		{"v: 1.5\n", "1.5", false, true},
		{"v: true\n", "true", false, true},
		{"v: \"404\"\n", "404", true, false},
		// An anchor without a tag is read, and a quoted value may start with the tag character.
		{"v: &a x\n", "x", false, false},
		{"v: \"! x\"\n", "! x", true, false},
		// go-yaml resolves YAML 1.1's timestamps, which YAML 1.2's core schema reads as text.
		{"v: 2026-11-10\n", "2026-11-10", false, false},
	}
	for _, c := range cases {
		root, problems := FromYAML("g.yaml", []byte(c.input))
		if problems != nil {
			t.Errorf("FromYAML(%q) problems = %q, want none", c.input, testsupport.ProblemStrings(problems))
			continue
		}
		v := root.Get("v")
		if v.Kind != Scalar || v.Text != c.want || v.Quoted != c.quoted || v.NumberOrBool != c.numberOrBool {
			const format = "FromYAML(%q) v = kind %d text %q quoted %t number or bool %t, " +
				"want kind %d text %q quoted %t number or bool %t"
			t.Errorf(format, c.input, v.Kind, v.Text, v.Quoted, v.NumberOrBool, Scalar, c.want, c.quoted, c.numberOrBool)
		}
	}
}

func TestFromYAMLList(t *testing.T) {
	root, problems := FromYAML("g.yaml", []byte("v:\n  - x\n  - 'y'\n"))
	if problems != nil {
		t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	v := root.Get("v")
	if v.Kind != List || len(v.Items) != 2 {
		t.Fatalf("v = kind %d with %d items, want kind %d with 2 items", v.Kind, len(v.Items), List)
	}
	want := []struct {
		text string
		pos  model.Pos
	}{
		{"x", model.Pos{File: "g.yaml", Line: 2, Column: 5}},
		{"y", model.Pos{File: "g.yaml", Line: 3, Column: 5}},
	}
	for i, item := range v.Items {
		if item.Text != want[i].text || item.Pos != want[i].pos {
			t.Errorf("item %d = %q at %v, want %q at %v", i, item.Text, item.Pos, want[i].text, want[i].pos)
		}
	}
}

func TestFromYAMLProblems(t *testing.T) {
	cases := []struct {
		name, input string
		want        []string
	}{
		{"duplicate key", "listen: 1\nlisten: 2\n", []string{`g.yaml:2:1: the key "listen" is given twice (first on line 1)`}},
		{"alias", "a: &x 1\nb: *x\n", []string{"g.yaml:2:4: aliases are not read; write the value out"}},
		{"empty", "", []string{"g.yaml: the file is empty"}},
		{"only a comment", "# c\n", []string{"g.yaml: the file is empty"}},
		{"only a document marker", "---\n", []string{"g.yaml: the file is empty"}},
		{"a document marker and a comment", "--- # c\n", []string{"g.yaml: the file is empty"}},
		{"two documents", "a: 1\n---\nb: 2\n", []string{"g.yaml:3:1: only one YAML document is read; this file has another"}},
		{"an empty document, then another", "---\n---\nb: 2\n", []string{"g.yaml:3:1: only one YAML document is read; this file has another"}},
		{"merge key with an alias", "a: &x {b: 1}\nc:\n  <<: *x\n", []string{
			"g.yaml:3:3: merge keys are not read; write the keys out",
			"g.yaml:3:7: aliases are not read; write the value out",
		}},
		{"merge key without an alias", "a:\n  <<: {b: 1}\n", []string{"g.yaml:2:3: merge keys are not read; write the keys out"}},
		{"custom tag", "v: !custom x\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		{"binary tag", "v: !!binary aGVsbG8=\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		{"null tag", "v: !!null foo\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		{"tag on a key", "!!str a: 1\n", []string{"g.yaml:1:1: tags are not read; write the value without the tag"}},
		// The bare tag ! leaves no mark on the node go-yaml returns (decode.go, parser.node), so it is found in the text.
		{"bare tag", "v: ! 8000\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		{"bare tag after an anchor", "v: &a ! x\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		// A node's properties may go on over lines, past comments and blank lines, while each line starts past the
		// column of the key or the - the node belongs to; a ! at that column or before is the next key's, reported once.
		{"bare tag on the line after an anchor", "v: &a\n  ! 8000\n", []string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		{"bare tag after an anchor, a comment and a blank line", "v: &a # c\n\n  ! 8000\n",
			[]string{"g.yaml:3:3: tags are not read; write the value without the tag"}},
		{"bare tag on the line after an anchor, in a list", "v:\n  - &a\n    ! 8000\n",
			[]string{"g.yaml:3:5: tags are not read; write the value without the tag"}},
		{"bare tag on the line after an anchor, in braces", "v: {a: &a\n  ! 8000}\n",
			[]string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		{"the next key's bare tag after an anchor", "a: &x\n! b: 1\n", []string{"g.yaml:2:1: tags are not read; write the value without the tag"}},
		{"the next key's bare tag after an anchor in a list", "a:\n  - &x\n! b: 1\n",
			[]string{"g.yaml:3:1: tags are not read; write the value without the tag"}},
		{"the first key's bare tag after a mapping's anchor", "a: &x\n  ! b: 1\n",
			[]string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		// go-yaml ends an anchor's name at the first character that is not a letter, a digit, _ or -, so a comma there
		// ends the item, and the ! after it is the next item's.
		{"the next item's bare tag after an anchor in brackets", "v: [&a, ! 1]\n",
			[]string{"g.yaml:1:9: tags are not read; write the value without the tag"}},
		{"the next item's bare tag on the line after an anchor in brackets", "v: [&a,\n  ! 1]\n",
			[]string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		// A block mapping starts where its first key does, so an anchor and a tag there are the key's, reported once.
		{"a key's anchor and bare tag", "&a ! k: 1\n", []string{"g.yaml:1:1: tags are not read; write the value without the tag"}},
		{"a key's anchor and tag", "&a !!str k: 1\n", []string{"g.yaml:1:1: tags are not read; write the value without the tag"}},
		{"a nested key's anchor and bare tag", "v:\n  &a ! k: 1\n", []string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		// Every tag on a line after the anchor is reported at the tag, bare or not.
		{"tag on the line after an anchor", "v: &a\n  !!str 8000\n", []string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		// An explicit key, and its value, go on over lines past the column of the ?, not of the key.
		{"bare tag on the line after an explicit key's anchor", "? &a\n  ! k\n: 1\n",
			[]string{"g.yaml:2:3: tags are not read; write the value without the tag"}},
		{"bare tag on the line after the anchor of an explicit key's value", "? k\n: &v\n  ! 1\n",
			[]string{"g.yaml:3:3: tags are not read; write the value without the tag"}},
		{"bare tag on a key", "! k: 1\n", []string{"g.yaml:1:1: tags are not read; write the value without the tag"}},
		{"bare tag on a mapping", "v: !\n  a: 1\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		// go-yaml counts no column for a byte order mark (yaml_parser_determine_encoding in readerc.go).
		{"bare tag after a byte order mark", "\ufeffv: ! 8000\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		// go-yaml counts a column in characters, so a character of two bytes before the tag counts once.
		{"bare tag after a character of two bytes", "é: ! 8000\n", []string{"g.yaml:1:4: tags are not read; write the value without the tag"}},
		{"tag on a mapping, with a problem inside", "a: &x 1\nv: !!map {b: *x}\n", []string{
			"g.yaml:2:4: tags are not read; write the value without the tag",
			"g.yaml:2:14: aliases are not read; write the value out",
		}},
		{"alias as a key", "a: &x k\n*x : 1\n", []string{"g.yaml:2:1: aliases are not read; write the value out"}},
		{"key is a list", "? [a, b]\n: 1\n", []string{"g.yaml:1:3: a key must be a single value"}},
		{"key is a mapping", "{x: 1}: 2\n", []string{"g.yaml:1:1: a key must be a single value"}},
		{"every problem, in order", "a: &x 1\nb: *x\na: 2\n", []string{
			"g.yaml:2:4: aliases are not read; write the value out",
			`g.yaml:3:1: the key "a" is given twice (first on line 1)`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, problems := FromYAML("g.yaml", []byte(c.input))
			if got := testsupport.ProblemStrings(problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
			if root != nil {
				t.Errorf("root = %+v, want nil next to problems", root)
			}
		})
	}
}

// A syntax error names the line it is on, counted from 1. go-yaml counts a parser error's line from 0 and writes no
// line when both of an error's marks are on the first (decode.go, parser.fail). The texts are the library's own.
// The rows after the first document are met only by the second Decode.
func TestFromYAMLSyntaxError(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"parser error on line 1", "a: [b}\n", "g.yaml:1: did not find expected ',' or ']'"},
		{"parser error on line 2", "- a\nb: c\n", "g.yaml:2: did not find expected '-' indicator"},
		{"parser error after the first document", "a: 1\n---\nb: [1, 2\n", "g.yaml:3: did not find expected ',' or ']'"},
		{"scanner error on line 1", "a: b: c\n", "g.yaml:1: mapping values are not allowed in this context"},
		{"scanner error on line 2", "a: 1\nb: c: d\n", "g.yaml:2: mapping values are not allowed in this context"},
		{"unknown anchor on line 1", "a: *x\n", "g.yaml:1: unknown anchor 'x' referenced"},
		// go-yaml places these nowhere, and the first line alone does not give them, so they name the file alone.
		{"unknown anchor on line 2", "a: 1\nb: *x\n", "g.yaml: unknown anchor 'x' referenced"},
		{"control character on line 2", "a: 1\nb: \"\x01\"\n", "g.yaml: control characters are not allowed"},
		{"unknown anchor after a first line that fails alone", "a: [\n*x]\n", "g.yaml: unknown anchor 'x' referenced"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, problems := FromYAML("g.yaml", []byte(c.input))
			if got := testsupport.ProblemStrings(problems); !slices.Equal(got, []string{c.want}) {
				t.Errorf("problems = %q, want %q", got, []string{c.want})
			}
		})
	}
}

// The generator relies on go-yaml keeping 1-based positions and writing a node read in flow style back in flow
// style, which is how a line another tool edits as text keeps its exact form.
func TestYAMLLibraryKeepsFlowStyle(t *testing.T) {
	const line = "default_value: {numerator: 100, denominator: HUNDRED}\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	hundred := doc.Content[0].Content[1].Content[3]
	if hundred.Value != "HUNDRED" || hundred.Line != 1 || hundred.Column != 46 {
		t.Errorf("node = %q at %d:%d, want %q at 1:46", hundred.Value, hundred.Line, hundred.Column, "HUNDRED")
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	encoder.CompactSeqIndent()
	if err := encoder.Encode(doc.Content[0]); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := out.String(); got != line {
		t.Errorf("encoded = %q, want %q", got, line)
	}
}

func TestGet(t *testing.T) {
	value := &Node{Kind: Scalar, Text: "1"}
	mapping := &Node{Kind: Map, Entries: []Entry{{Key: "a", Value: value}}}
	cases := []struct {
		name     string
		node     *Node
		key      string
		want     *Node
		wantText string
	}{
		{"key present", mapping, "a", value, "1"},
		{"key absent", mapping, "b", nil, ""},
		{"not a map", &Node{Kind: Scalar, Text: "a"}, "a", nil, ""},
		{"no node", nil, "a", nil, ""},
	}
	for _, c := range cases {
		if got := c.node.Get(c.key); got != c.want {
			t.Errorf("%s: Get(%q) = %p, want %p", c.name, c.key, got, c.want)
		}
		if got := c.node.GetText(c.key); got != c.wantText {
			t.Errorf("%s: GetText(%q) = %q, want %q", c.name, c.key, got, c.wantText)
		}
	}
}
