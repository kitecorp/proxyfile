package input

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// minimal is the minimal example, which the YAML rows change, one change each.
var minimal = testsupport.Document{
	"listen: 8000",
	"services:",
	"  app: {http: 3000}",
	"origins:",
	`  all: {hosts: [localhost], origins: ["http://localhost:3000"]}`,
	"routes:",
	"  - {name: app, prefix: /, to: app}",
}

// hoconFile is the HOCON front as the proxyfile's reader calls it, without the files its includes name.
func hoconFile(path string) (*tree.Node, []model.Problem) {
	root, _, problems := tree.FromHOCON(path)
	return root, problems
}

const rule = "; no key or value may hold a control character other than tab"

// block ends the message when the only control character is one line break at the end, as a YAML block written | or >
// keeps.
const block = "; a YAML block keeps its final line break unless written |- or >-"

// A key or a value that holds a control character other than tab is refused at its place, in every file and either
// syntax, before the format check: written into the generated file as it is, it would break a name, a path or a header
// value there. Each such key or value is one problem, which names its first control character and quotes the text, so
// the problem stays on one line.
func TestCheckedRefusesControlCharacters(t *testing.T) {
	cases := []struct {
		name, file, text string
		spec             format.Field
		front            func(path string) (*tree.Node, []model.Problem)
		want             []string
	}{
		{name: "NUL in a value", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\0b", to: app}`),
			want: []string{`g.yaml:7:25: the value "/a\x00b" contains NUL` + rule}},
		{name: "a newline in a value", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\nb", to: app}`),
			want: []string{`g.yaml:7:25: the value "/a\nb" contains a newline` + rule}},
		{name: "a carriage return in a value", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\rb", to: app}`),
			want: []string{`g.yaml:7:25: the value "/a\rb" contains a carriage return` + rule}},
		{name: "U+0001 in a value", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\x01b", to: app}`),
			want: []string{`g.yaml:7:25: the value "/a\x01b" contains the control character U+0001` + rule}},
		{name: "U+007F in a value", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\x7fb", to: app}`),
			want: []string{`g.yaml:7:25: the value "/a\x7fb" contains the control character U+007F` + rule}},
		{name: "a tab in a value is read", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: app, prefix: "/a\tb", to: app}`)},
		// The key's own rule, the pattern of a service name, is not reported as well: the key is reported once.
		{name: "a key", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(3, 3, `  "a\nb": {http: 3000}`),
			want: []string{`g.yaml:3:3: the key "a\nb" contains a newline` + rule}},
		{name: "an item of a list", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(5, 5, `  all: {hosts: [localhost, "a\rb"], origins: ["http://localhost:3000"]}`),
			want: []string{`g.yaml:5:28: the value "a\rb" contains a carriage return` + rule}},
		// A literal or folded block keeps the line break at its end, which a header value would then hold.
		{name: "the line break a literal block ends with", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.String() + "cors:\n  allow_methods: |\n    GET\n",
			want: []string{`g.yaml:9:18: the value "GET\n" ends with a line break` + rule + block}},
		{name: "the line break a folded block ends with", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.String() + "cors:\n  allow_methods: >\n    GET\n",
			want: []string{`g.yaml:9:18: the value "GET\n" ends with a line break` + rule + block}},
		{name: "a literal block written |- keeps no line break", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.String() + "cors:\n  allow_methods: |-\n    GET\n"},
		{name: "every value, each once", file: "g.yaml", spec: format.Proxyfile, front: YAMLFile,
			text: minimal.Replaced(7, 7, `  - {name: "a\nb", prefix: "/\rb", to: app}`),
			want: []string{
				`g.yaml:7:12: the value "a\nb" contains a newline` + rule,
				`g.yaml:7:28: the value "/\rb" contains a carriage return` + rule,
			}},
		{name: "an environment's name in an origins file", file: "o.json", spec: format.Origins, front: JSONFile,
			text: `{"cors": {}, "environments": [{"name": "zq\nqz", "hosts": ["localhost"], "origins": ["http://localhost:3000"]}]}`,
			want: []string{`o.json:1:40: the value "zq\nqz" contains a newline` + rule}},
		{name: "a key of an origins file", file: "o.json", spec: format.Origins, front: JSONFile,
			text: `{"cors": {"allow_methods\u0000": "GET"}, "environments": [{"name": "a", "hosts": ["localhost"]}]}`,
			want: []string{`o.json:1:11: the key "allow_methods\x00" contains NUL` + rule}},
		{name: "an action of a methods file", file: "m.json", spec: format.Methods, front: JSONFile,
			text: `[{"service": "a.v1.S", "method": "M", "action": "a\u0001b"}]`,
			want: []string{`m.json:1:49: the value "a\x01b" contains the control character U+0001` + rule}},
		{name: "HOCON: a value", file: "g.conf", spec: format.Proxyfile, front: hoconFile,
			text: "listen = 8000\nservices { app { http = 3000 } }\norigins { all { hosts = [localhost] } }\n" +
				`routes = [ { name = app, prefix = "/a\nb", to = app } ]` + "\n",
			want: []string{`g.conf: routes[0].prefix: the value "/a\nb" contains a newline` + rule}},
		{name: "HOCON: NUL in a value", file: "g.conf", spec: format.Proxyfile, front: hoconFile,
			text: "listen = 8000\nservices { app { http = 3000 } }\norigins { all { hosts = [localhost] } }\n" +
				`routes = [ { name = app, prefix = "/a\u0000b", to = app } ]` + "\n",
			want: []string{`g.conf: routes[0].prefix: the value "/a\x00b" contains NUL` + rule}},
		{name: "HOCON: a key", file: "g.conf", spec: format.Proxyfile, front: hoconFile,
			text: "listen = 8000\n" + `services { "a\rb" { http = 3000 } }` + "\norigins { all { hosts = [localhost] } }\n" +
				"routes = [ { name = app, prefix = /, to = app } ]\n",
			want: []string{`g.conf: services."a\rb": the key "a\rb" contains a carriage return` + rule}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, c.file)
			testsupport.WriteFile(t, path, c.text)
			root, problems := CheckedWith(path, c.spec, c.front)
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
			if gotRoot, wantRoot := root != nil, c.want == nil; gotRoot != wantRoot {
				t.Errorf("root returned = %t, want %t", gotRoot, wantRoot)
			}
		})
	}
}
