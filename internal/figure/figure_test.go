package figure

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

var update = flag.Bool("update", false, "rewrite the comparison figures under docs/images")

const (
	minimalSource    = "../../examples/minimal/yaml/proxyfile.yaml"
	minimalGenerated = "../../examples/minimal/yaml/envoy.yaml"
)

// The README shows the committed figures, so each must hold the bytes Comparison writes today from the minimal
// example's proxyfile and the envoy.yaml beside it, which TestExamplesAreCurrent keeps equal to what render writes.
func TestComparisonFiguresAreCurrent(t *testing.T) {
	source := testsupport.ReadFile(t, minimalSource)
	generated := testsupport.ReadFile(t, minimalGenerated)
	for _, c := range []struct {
		file  string
		theme Theme
	}{
		{file: "../../docs/images/comparison-envoy-light.svg", theme: Light},
		{file: "../../docs/images/comparison-envoy-dark.svg", theme: Dark},
	} {
		t.Run(filepath.Base(c.file), func(t *testing.T) {
			got := Comparison(source, generated, "proxyfile.yaml", "envoy.yaml", c.theme)
			if *update {
				testsupport.WriteFile(t, c.file, string(got))
			}
			want, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("Comparison differs from %s; rewrite it with go test ./internal/figure/ -run TestComparisonFiguresAreCurrent -update", c.file)
			}
		})
	}
}

// The titles and the description count the lines of each file, the last one whether or not a newline ends it.
func TestComparisonCountsTheLines(t *testing.T) {
	cases := []struct {
		name, source, generated                    string
		wantSource, wantGenerated, wantDescription string
	}{
		{name: "nine and 119 lines", source: strings.Repeat("listen: 8000\n", 9), generated: strings.Repeat("a: 1\n", 119),
			wantSource: "You write: proxyfile.yaml, 9 lines", wantGenerated: "proxyfile generates: envoy.yaml, 119 lines",
			wantDescription: "A 9-line proxyfile beside the 119-line Envoy configuration proxyfile generates from it"},
		{name: "three and two lines", source: "listen: 8000\nservices: {}\nroutes: []\n", generated: "a: 1\nb: 2\n",
			wantSource: "You write: proxyfile.yaml, 3 lines", wantGenerated: "proxyfile generates: envoy.yaml, 2 lines",
			wantDescription: "A 3-line proxyfile beside the 2-line Envoy configuration proxyfile generates from it"},
		{name: "no newline at the end", source: "listen: 8000", generated: "a: 1\nb: 2",
			wantSource: "You write: proxyfile.yaml, 1 line", wantGenerated: "proxyfile generates: envoy.yaml, 2 lines",
			wantDescription: "A 1-line proxyfile beside the 2-line Envoy configuration proxyfile generates from it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := read(t, Comparison([]byte(c.source), []byte(c.generated), "proxyfile.yaml", "envoy.yaml", Light))
			if got.description != c.wantDescription {
				t.Errorf("description = %q, want %q", got.description, c.wantDescription)
			}
			if title := titleOf(got.panes["source"]); title != c.wantSource {
				t.Errorf("source title = %q, want %q", title, c.wantSource)
			}
			if title := titleOf(got.panes["generated"]); title != c.wantGenerated {
				t.Errorf("generated title = %q, want %q", title, c.wantGenerated)
			}
		})
	}
}

// Every line of both files is in the figure as text a reader reads back whole, a source line longer than the left
// pane is wide too, which the pane draws on more than one row. The left pane numbers each line.
func TestComparisonShowsEveryLine(t *testing.T) {
	source := "# A comment longer than the left pane is wide, so the figure draws it on more than one row.\nlisten: 8000\n"
	generated := "static_resources:\n  listeners: []\n"
	got := read(t, Comparison([]byte(source), []byte(generated), "proxyfile.yaml", "envoy.yaml", Dark))
	want := map[string][]string{
		"source": {"You write: proxyfile.yaml, 2 lines",
			"1", "# A comment longer than the left pane is wide, so the figure draws it on more than one row.",
			"2", "listen: 8000"},
		"generated": {"proxyfile generates: envoy.yaml, 2 lines", "static_resources:", "  listeners: []"},
	}
	for pane, lines := range want {
		if !slices.Equal(got.panes[pane], lines) {
			t.Errorf("%s pane = %q, want %q", pane, got.panes[pane], lines)
		}
	}
}

// A character XML gives a meaning to is written as an entity in both panes, so the line still reads as itself.
func TestComparisonEscapesTheText(t *testing.T) {
	cases := []struct{ name, line, escaped string }{
		{name: "less than", line: "a: <b", escaped: "a: &lt;b"},
		{name: "greater than", line: "a: b>", escaped: "a: b&gt;"},
		{name: "ampersand", line: "a: b & c", escaped: "a: b &amp; c"},
		{name: "double quote", line: `a: "b"`, escaped: "a: &#34;b&#34;"},
		{name: "single quote", line: "a: 'b'", escaped: "a: &#39;b&#39;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Comparison([]byte(c.line+"\n"), []byte(c.line+"\n"), "proxyfile.yaml", "envoy.yaml", Light)
			if got := strings.Count(string(out), ">"+c.escaped+"</text>"); got != 2 {
				t.Errorf("text elements holding %q = %d, want 2", c.escaped, got)
			}
			got := read(t, out)
			if want := []string{"You write: proxyfile.yaml, 1 line", "1", c.line}; !slices.Equal(got.panes["source"], want) {
				t.Errorf("source pane = %q, want %q", got.panes["source"], want)
			}
			if want := []string{"proxyfile generates: envoy.yaml, 1 line", c.line}; !slices.Equal(got.panes["generated"], want) {
				t.Errorf("generated pane = %q, want %q", got.panes["generated"], want)
			}
		})
	}
}

func TestComparisonGivesTheSameBytes(t *testing.T) {
	source := testsupport.ReadFile(t, minimalSource)
	generated := testsupport.ReadFile(t, minimalGenerated)
	first := Comparison(source, generated, "proxyfile.yaml", "envoy.yaml", Dark)
	second := Comparison(source, generated, "proxyfile.yaml", "envoy.yaml", Dark)
	if !bytes.Equal(first, second) {
		t.Error("a second Comparison of the same input gave other bytes")
	}
}

// The right pane takes another column only when one more row would make it taller than generatedMaxHeight.
func TestColumns(t *testing.T) {
	for _, c := range []struct{ lines, want int }{{1, 1}, {62, 1}, {63, 2}, {119, 2}, {124, 2}, {125, 3}} {
		if got := columns(c.lines); got != c.want {
			t.Errorf("columns(%d) = %d, want %d", c.lines, got, c.want)
		}
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		first, rest int
		want        []string
	}{
		{name: "fits", line: "listen: 8000", first: 12, rest: 10, want: []string{"listen: 8000"}},
		{name: "spaces past the width draw as nothing", line: "listen: 8000  ", first: 12, rest: 10,
			want: []string{"listen: 8000  "}},
		{name: "a row ends after its last space", line: "# one two three", first: 10, rest: 8,
			want: []string{"# one two ", "three"}},
		{name: "a space right after the width ends the row", line: "# one two three", first: 9, rest: 8,
			want: []string{"# one two ", "three"}},
		{name: "a row keeps the spaces after its break", line: "one   two", first: 4, rest: 8,
			want: []string{"one   ", "two"}},
		{name: "after a slash when the row has no space", line: "$schema=../../../schema/proxyfile.schema.json",
			first: 30, rest: 30, want: []string{"$schema=../../../schema/", "proxyfile.schema.json"}},
		{name: "at the width when the row has neither", line: "abcdefghij", first: 4, rest: 3,
			want: []string{"abcd", "efg", "hij"}},
		{name: "never a row of indent alone", line: "    abcdefghij", first: 8, rest: 6,
			want: []string{"    abcd", "efghij"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrap(c.line, c.first, c.rest); !slices.Equal(got, c.want) {
				t.Errorf("wrap(%q, %d, %d) = %q, want %q", c.line, c.first, c.rest, got, c.want)
			}
		})
	}
}

// titleOf returns the first text of a pane, its title, or "" for a pane with no text.
func titleOf(texts []string) string {
	if len(texts) == 0 {
		return ""
	}
	return texts[0]
}

// shown is what a reader of a figure reads: its description, and the text of each <text> element of each pane, by
// the id of the pane's group, in document order, with the entities decoded and the rows of a wrapped line joined.
type shown struct {
	description string
	panes       map[string][]string
}

// read decodes svg, ending the test when it is not well-formed XML.
func read(t *testing.T, svg []byte) shown {
	t.Helper()
	got := shown{panes: map[string][]string{}}
	decoder := xml.NewDecoder(bytes.NewReader(svg))
	var (
		stack       []string // the pane each open element is in, "" outside both
		text, title strings.Builder
		inText      bool
		inTitle     bool
	)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("the figure is not well-formed XML: %v", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			pane := ""
			if len(stack) > 0 {
				pane = stack[len(stack)-1]
			}
			for _, attr := range token.Attr {
				if token.Name.Local == "g" && attr.Name.Local == "id" {
					pane = attr.Value
				}
			}
			stack = append(stack, pane)
			switch token.Name.Local {
			case "text":
				text.Reset()
				inText = true
			case "title":
				inTitle = true
			}
		case xml.CharData:
			if inText {
				text.Write(token)
			} else if inTitle {
				title.Write(token)
			}
		case xml.EndElement:
			pane := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			switch token.Name.Local {
			case "text":
				got.panes[pane] = append(got.panes[pane], text.String())
				inText = false
			case "title":
				inTitle = false
			}
		}
	}
	got.description = title.String()
	return got
}
