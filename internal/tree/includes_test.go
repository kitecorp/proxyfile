package tree

import (
	"slices"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The statements are found where the library reads them, and nowhere else: in an object's field, at any depth, in the
// proxyfile and in every file it includes; never in a comment, a string, a value or an array.
func TestIncludes(t *testing.T) {
	const fileRule = "include file(...) is read from the working directory, not beside this file; write include "
	cases := []struct {
		name, input string
		others      map[string]string // more files written beside g.conf
		files       []string
		problems    []string
	}{
		{name: "no include", input: "listen = 1\n"},
		{name: "every form that names a file",
			input:  "include \"a.conf\"\ninclude required(\"b\")\nx { include \"c.conf\" }\ny = [ { include \"d.conf\" } ]\n",
			others: map[string]string{"a.conf": "include \"e.conf\"\n", "b.properties": "include \"z.conf\"\n"},
			files:  []string{"a.conf", "e.conf", "b.properties", "b.json", "b.conf", "c.conf", "d.conf"}},
		// A comma stands before each include in the comments and the string, so a scan that did not skip them, or that
		// ended the string at its escaped quote, would start a field there.
		{name: "comments, strings, values and arrays",
			input: "# a, include file(\"a\")\n// a, include file(\"b\")\n" + `a = "a\", include ", z = "c"` + "\nb = \"\"\"\ninclude file(\"d\")\n\"\"\"\n" +
				"c = include\nd = [\n  include \"e\"\n]\nincludes = 1\n"},
		{name: "a name that climbs out, an absolute name and a cycle",
			input:  "include \"../shared/o.conf\"\ninclude \"/proxyfile-test-absent/x.conf\"\ninclude \"a.conf\"\n",
			others: map[string]string{"a.conf": "include \"g.conf\"\n"},
			files:  []string{"../shared/o.conf", "/proxyfile-test-absent/x.conf", "a.conf"}},
		// A byte order mark takes no column: the library strips it before it counts one.
		{name: "include file", input: "\uFEFFinclude file(\"port.conf\")\n", others: map[string]string{"port.conf": "listen = 1\n"},
			problems: []string{"g.conf:1:1: " + fileRule + `"port.conf"`}},
		// The column counts characters, as the library's positions do.
		{name: "include required(file(...)) in an object of an included file", input: "include \"a.conf\"\n",
			others:   map[string]string{"a.conf": "x { é = 1, include required( file( \"p.conf\" ) ) }\n"},
			files:    []string{"a.conf"},
			problems: []string{"a.conf:1:12: " + fileRule + `required("p.conf")`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeHOCONFiles(t, c.input, c.others, false)
			files, problems := includes("g.conf")
			if !slices.Equal(files, c.files) {
				t.Errorf("files = %q, want %q", files, c.files)
			}
			if got := testsupport.ProblemStrings(problems); !slices.Equal(got, c.problems) {
				t.Errorf("problems = %q, want %q", got, c.problems)
			}
		})
	}
}
