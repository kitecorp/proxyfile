package format

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// The core keys are listed as the key reference names them: a list's items as [], a Dict's value as <the name of its
// key>, and no key that names a proxy, nor any key under one.
func TestCoreKeys(t *testing.T) {
	small := Field{Kind: Map, Fields: []Field{{Name: "a", Kind: Int}, {Name: "b", Kind: Map, Fields: []Field{{Name: "c", Kind: Text}}},
		{Name: "own", Kind: Map, Class: "envoy", Fields: []Field{{Name: "d", Kind: Int}}},
		{Name: "list", Kind: List, Item: &Field{Kind: Map, Fields: []Field{{Name: "e", Kind: Int}}}},
		{Name: "dict", Kind: Dict, Key: &Field{Name: "x name"}, Item: &Field{Kind: Map, Fields: []Field{{Name: "f", Kind: Int}}}}}}
	if got, want := CoreKeys(small), []string{"a", "b", "b.c", "list", "list[].e", "dict", "dict.<x name>", "dict.<x name>.f"}; !slices.Equal(got, want) {
		t.Errorf("CoreKeys(small) = %q, want %q", got, want)
	}
	full := CoreKeys(Proxyfile)
	first, last := "", ""
	if len(full) > 0 {
		first, last = full[0], full[len(full)-1]
	}
	// A key is in a proxy's block by whole segments, so a core key such as envoy_compat would not count as one.
	proxyBlock := func(key string) bool {
		return slices.ContainsFunc([]string{"envoy", "nginx", "haproxy"}, func(block string) bool {
			return key == block || strings.HasPrefix(key, block+".")
		})
	}
	rows := []struct {
		name      string
		got, want any
	}{
		{name: "the count", got: len(full), want: 129},
		{name: "the first", got: first, want: "listen"},
		{name: "the last", got: last, want: "tracing.path_in_url"},
		{name: "a key of a Dict's value", got: slices.Contains(full, "services.<service name>.http"), want: true},
		{name: "a key under a list, an Either and a Dict", got: slices.Contains(full, "routes[].rpc.to.<package pattern>.service"), want: true},
		{name: "a key of a proxy's block", got: slices.ContainsFunc(full, proxyBlock), want: false},
	}
	for _, row := range rows {
		if row.got != row.want {
			t.Errorf("CoreKeys(Proxyfile), %s = %v, want %v", row.name, row.got, row.want)
		}
	}
}

// The support table has a column for each target and a row for each core key, and a note is escaped as the key
// reference escapes text, so a | or a ` in it does not break the table.
func TestSupportMatrix(t *testing.T) {
	spec := Field{Kind: Map, Fields: []Field{{Name: "a", Kind: Int}, {Name: "b", Kind: Int}}}
	targets := []Target{
		{Name: "envoy", Title: "Envoy", Keys: map[string]Support{"a": {Level: Supported}, "b": {Level: Supported}}},
		{Name: "lean", Title: "Lean", Keys: map[string]Support{"a": {Level: Partial, Note: "counts | per second"},
			"b": {Level: Refused, Note: "a `b` of its own"}}},
	}
	want := "| Key | Envoy | Lean |\n|---|---|---|\n" +
		"| `a` | supported | partial: counts \\| per second |\n" +
		"| `b` | supported | refused: a \\`b\\` of its own |\n"
	if got := string(SupportMatrix(spec, targets)); got != want {
		t.Errorf("SupportMatrix = %q, want %q", got, want)
	}
}

// Every target declares every core key and no other, so a key added to the format cannot go undeclared.
func TestTargetsDeclareEveryCoreKey(t *testing.T) {
	keys := CoreKeys(Proxyfile)
	for _, target := range Targets {
		var undeclared, notCore []string
		for _, key := range keys {
			if _, declared := target.Keys[key]; !declared {
				undeclared = append(undeclared, key)
			}
		}
		for key := range target.Keys {
			if !slices.Contains(keys, key) {
				notCore = append(notCore, key)
			}
		}
		slices.Sort(notCore)
		if !slices.Equal(undeclared, nil) {
			t.Errorf("%s: the core keys it does not declare = %q, want none", target.Name, undeclared)
		}
		if !slices.Equal(notCore, nil) {
			t.Errorf("%s: the keys it declares that are not core = %q, want none", target.Name, notCore)
		}
	}
}

// A key the target refuses is named at its own position, also inside a Dict's value or a list item, and in the origins
// file and the methods file by the path the proxyfile writes it at; a file that writes none of them has no problem.
func TestRefusals(t *testing.T) {
	lean := Target{Name: "lean", Title: "Lean", Keys: map[string]Support{}}
	for _, key := range CoreKeys(Proxyfile) {
		lean.Keys[key] = Support{Level: Supported}
	}
	lean.Keys["timeouts.idle"] = Support{Level: Refused, Note: "lean has no idle timeout"}
	lean.Keys["services.<service name>.health"] = Support{Level: Refused, Note: "lean has no health checks"}
	lean.Keys["routes[].retries"] = Support{Level: Refused, Note: "lean retries no request"}
	lean.Keys["cors.max_age"] = Support{Level: Refused, Note: "lean sends no max age"}
	lean.Keys["origins.<environment name>.patterns"] = Support{Level: Refused, Note: "lean admits no pattern"}
	lean.Keys["rpc.methods[].action"] = Support{Level: Refused, Note: "lean tells the check no action"}
	// oneHost refuses the environments themselves, which the proxyfile names by its keys and an origins file by name.
	oneHost := Target{Name: "one-host", Title: "One host",
		Keys: map[string]Support{"origins.<environment name>": {Level: Refused, Note: "one-host serves one host"}}}
	cases := []struct {
		name   string
		spec   Field
		target Target
		// file is read when set, and named in the problems by this path; otherwise document is read as g.yaml.
		file, document string
		want           []string
	}{
		{name: "nothing refused is written", spec: Proxyfile, target: lean, document: m.String(), want: nil},
		{name: "a refused key", spec: Proxyfile, target: lean, document: m.Inserted(7, "timeouts: {idle: 30s}"),
			want: []string{`g.yaml:7:12: "timeouts.idle" is refused by --target lean: lean has no idle timeout`}},
		{name: "a refused key in a service", spec: Proxyfile, target: lean, document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: /h}}"),
			want: []string{`g.yaml:3:21: "services.<service name>.health" is refused by --target lean: lean has no health checks`}},
		{name: "a refused key in a list item", spec: Proxyfile, target: lean, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, retries: 1}"),
			want: []string{`g.yaml:6:37: "routes[].retries" is refused by --target lean: lean retries no request`}},
		{name: "envoy refuses no key of the full example", spec: Proxyfile, target: Targets[0], file: "../../examples/full/yaml/proxyfile.yaml", want: nil},
		{name: "a refused cors key in an origins file", spec: Origins, target: lean,
			document: `{"cors": {"max_age": "600"}, "environments": [{"name": "all", "hosts": ["localhost"]}]}`,
			want:     []string{`g.yaml:1:11: "cors.max_age" is refused by --target lean: lean sends no max age`}},
		{name: "a refused key of an environment in an origins file", spec: Origins, target: lean,
			document: `{"environments": [{"name": "all", "hosts": ["localhost"], "patterns": ["http://*.localhost"]}]}`,
			want:     []string{`g.yaml:1:59: "origins.<environment name>.patterns" is refused by --target lean: lean admits no pattern`}},
		{name: "a refused environment in an origins file, at its name", spec: Origins, target: oneHost,
			document: `{"environments": [{"name": "all", "hosts": ["localhost"]}]}`,
			want:     []string{`g.yaml:1:20: "origins.<environment name>" is refused by --target one-host: one-host serves one host`}},
		{name: "a refused key of a method in a methods file", spec: Methods, target: lean,
			document: `[{"service": "shop.v1.CartService", "method": "AddItem", "action": "write"}]`,
			want:     []string{`g.yaml:1:58: "rpc.methods[].action" is refused by --target lean: lean tells the check no action`}},
		// Written in the proxyfile, each of the keys above is refused once, at its own line.
		{name: "the same keys in the proxyfile", spec: Proxyfile, target: lean,
			document: m.Replaced(4, 6, `cors: {max_age: "600"}`, `origins: {all: {hosts: [localhost], patterns: ["http://*.localhost"]}}`,
				"routes:", "  - {name: app, prefix: /, to: app}", "rpc: {methods: [{service: shop.v1.CartService, method: AddItem, action: write}]}"),
			want: []string{`g.yaml:4:8: "cors.max_age" is refused by --target lean: lean sends no max age`,
				`g.yaml:5:37: "origins.<environment name>.patterns" is refused by --target lean: lean admits no pattern`,
				`g.yaml:8:65: "rpc.methods[].action" is refused by --target lean: lean tells the check no action`}},
		{name: "the same environment in the proxyfile, at its key", spec: Proxyfile, target: oneHost,
			document: m.Replaced(4, 4, "origins: {all: {hosts: [localhost]}}"),
			want:     []string{`g.yaml:4:11: "origins.<environment name>" is refused by --target one-host: one-host serves one host`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, data := "g.yaml", []byte(c.document)
			if c.file != "" {
				var err error
				if data, err = os.ReadFile(c.file); err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				path = c.file
			}
			root, problems := tree.FromYAML(path, data)
			if problems != nil {
				t.Fatalf("FromYAML problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := testsupport.ProblemStrings(Refusals(c.spec, root, c.target)); !slices.Equal(got, c.want) {
				t.Errorf("Refusals = %q, want %q", got, c.want)
			}
		})
	}
}
