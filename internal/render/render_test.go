package render

import (
	"bytes"
	"cmp"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/testsupport"
)

var update = flag.Bool("update", false, "rewrite the generated files of examples/")

// TestExampleTrees is the generator's proof: the tree it writes for each example equals a tree written by hand,
// list order included, and a second render gives the same bytes.
func TestExampleTrees(t *testing.T) {
	for _, name := range []string{"minimal", "partial", "full"} {
		t.Run(name, func(t *testing.T) {
			req := requestFor(t, name, "envoy.yaml")
			files, problems := render(req)
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got, want := paths(files), outputs(req); !slices.Equal(got, want) {
				t.Fatalf("render paths = %q, want %q", got, want)
			}
			want := decode(t, testsupport.ReadFile(t, filepath.Join("testdata", name+".expected.yaml")))
			if d := firstDifference(want, decode(t, files[0].Bytes), ""); d != "" {
				t.Error(d)
			}
			again, _ := render(req)
			if len(again) == 0 || !bytes.Equal(again[0].Bytes, files[0].Bytes) {
				t.Error("a second render of the same input gave other bytes")
			}
		})
	}
}

// TestExamplesAreCurrent keeps the committed outputs of examples/ equal to what render writes; -update rewrites them.
func TestExamplesAreCurrent(t *testing.T) {
	for _, name := range []string{"minimal", "partial", "full"} {
		t.Run(name, func(t *testing.T) {
			files, problems := render(requestFor(t, name, filepath.Join("..", "..", "examples", name, "envoy.yaml")))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			for _, file := range files {
				if *update {
					if err := os.WriteFile(file.Path, file.Bytes, 0o644); err != nil {
						t.Fatalf("write %s: %v", file.Path, err)
					}
					continue
				}
				if d := committedDifference(t, name, file); d != "" {
					t.Errorf("%s\n  run the test with -update to write what render writes", d)
				}
			}
		})
	}
}

// One gateway, whichever way it is written: every file a render of the HOCON example writes equals the committed file
// byte for byte, and TestExamplesAreCurrent keeps the committed files equal to the render of the YAML example.
func TestBothFormsGiveTheSameFiles(t *testing.T) {
	cases := []struct {
		name  string
		files int
	}{{"minimal", 1}, {"partial", 2}, {"full", 2}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := requestFor(t, c.name, "envoy.yaml")
			req.Proxyfile = filepath.Join("..", "..", "examples", c.name, "gateway.conf")
			// The partial HOCON example includes rpc-methods.conf and the full one writes rpc.methods, so neither takes
			// the methods file the partial YAML example is rendered with.
			req.RPCMethods = ""
			files, problems := render(req)
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if len(files) != c.files {
				t.Fatalf("render wrote %d files %q, want %d", len(files), paths(files), c.files)
			}
			for _, file := range files {
				if d := committedDifference(t, c.name, file); d != "" {
					t.Errorf("rendered from %s: %s", req.Proxyfile, d)
				}
			}
		})
	}
}

// committedDifference returns "" when file holds the bytes of the file of the same name committed in
// examples/<example>, and otherwise names the line, counted from 1, where the two first differ, with that line of
// each, so a failure points at the line to read instead of printing two whole files.
func committedDifference(t *testing.T, example string, file File) string {
	t.Helper()
	path := filepath.Join("..", "..", "examples", example, filepath.Base(file.Path))
	committed := testsupport.ReadFile(t, path)
	if bytes.Equal(file.Bytes, committed) {
		return ""
	}
	line, inCommitted, rendered := firstDifferentLine(committed, file.Bytes)
	return fmt.Sprintf("%s differs first on line %d:\n  committed: %s\n  rendered:  %s", path, line, inCommitted, rendered)
}

// firstDifferentLine returns the number, counted from 1, of the line where a and b first differ, and that line of each.
// The bytes before the first difference are the same in both, so the line starts at the same offset in each.
func firstDifferentLine(a, b []byte) (int, string, string) {
	same := 0
	for same < len(a) && same < len(b) && a[same] == b[same] {
		same++
	}
	start := bytes.LastIndexByte(a[:same], '\n') + 1
	return bytes.Count(a[:same], []byte("\n")) + 1, lineAt(a, start), lineAt(b, start)
}

// lineAt returns the line that starts at start, quoted with its newline, so a trailing space, an empty line and a
// missing last newline each show; or, when the file ends before start, a marker that no quoted line can look like.
func lineAt(data []byte, start int) string {
	if start >= len(data) {
		return "(the file ends here)"
	}
	line := data[start:]
	if end := bytes.IndexByte(line, '\n'); end >= 0 {
		line = line[:end+1]
	}
	return fmt.Sprintf("%q", line)
}

// minimalNaming returns the minimal example with its inline origins replaced by "origins: <file>": the file form,
// which no example writes.
func minimalNaming(t *testing.T, file string) string {
	t.Helper()
	example := string(testsupport.ReadFile(t, "../../examples/minimal/gateway.yaml"))
	if !strings.Contains(example, testsupport.MinimalInlineOrigins) {
		t.Fatalf("the minimal example holds no %q to replace", testsupport.MinimalInlineOrigins)
	}
	return strings.Replace(example, testsupport.MinimalInlineOrigins, "origins: "+file+"\n", 1)
}

// Inputs is what the command line's output-path rule protects, so it lists the origins file and the methods file
// only when the run read them: origins and rpc.methods written in the proxyfile have no path of their own.
func TestInputs(t *testing.T) {
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), minimalNaming(t, "origins.json"))
	testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(testsupport.ReadFile(t, "testdata/origins.json")))
	cases := []struct {
		name string
		req  Request
		want []string // <dir> stands for the test's temporary directory
	}{
		{name: "inline origins", req: Request{Proxyfile: "../../examples/minimal/gateway.yaml"},
			want: []string{"../../examples/minimal/gateway.yaml"}},
		{name: "origins file", req: Request{Proxyfile: filepath.Join(dir, "g.yaml")},
			want: []string{"<dir>/g.yaml", "<dir>/origins.json"}},
		{name: "inline methods", req: Request{Proxyfile: "../../examples/full/gateway.yaml"},
			want: []string{"../../examples/full/gateway.yaml"}},
		{name: "methods file",
			req:  Request{Proxyfile: "../../examples/partial/gateway.yaml", RPCMethods: "../../examples/partial/methods.json"},
			want: []string{"../../examples/partial/gateway.yaml", "../../examples/partial/methods.json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, problems := Read(c.req)
			if problems != nil {
				t.Fatalf("read problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			var got []string
			for _, path := range run.Inputs() {
				got = append(got, strings.Replace(path, dir, "<dir>", 1))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Inputs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderProblems(t *testing.T) {
	// The minimal example in the file form, naming an origins file that is not there.
	minimal := minimalNaming(t, "missing.json")
	cases := []struct {
		name, proxyfile string // proxyfile is written to g.yaml in a temporary directory
		want            []string
	}{
		{name: "rpc entry without methods",
			proxyfile: "listen: 8000\nservices:\n  backend: {grpc: 5000}\norigins: origins.json\nroutes:\n  - rpc: {to: {\"*\": backend}}\n",
			want:      []string{"g.yaml:6:5: the routes have an rpc entry, so rpc.methods or --rpc-methods must list the RPC methods"}},
		{name: "origins file missing", proxyfile: minimal,
			want: []string{"missing.json: the file cannot be read: no such file or directory"}},
		// An absolute path is read as written, not under the proxyfile's directory, so the problem names it whole.
		{name: "origins file missing at an absolute path",
			proxyfile: strings.Replace(minimal, "origins: missing.json", "origins: /proxyfile-test-absent/origins.json", 1),
			want:      []string{"/proxyfile-test-absent/origins.json: the file cannot be read: no such file or directory"}},
		// render reads the origins file before it applies the rpc-entry rule, and g.yaml sorts before origins-absent.json,
		// so the two come out together and in this order only when the stages run together and their problems are sorted.
		{name: "problems of two inputs, together and sorted",
			proxyfile: "listen: 8000\nservices:\n  backend: {grpc: 5000}\norigins: origins-absent.json\nroutes:\n  - rpc: {to: {\"*\": backend}}\n",
			want: []string{
				"g.yaml:6:5: the routes have an rpc entry, so rpc.methods or --rpc-methods must list the RPC methods",
				"origins-absent.json: the file cannot be read: no such file or directory",
			}},
	}
	origins := testsupport.ReadFile(t, "testdata/origins.json")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), c.proxyfile)
			testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(origins))
			files, problems := render(Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml")})
			if files != nil {
				t.Errorf("render returned files %q next to problems", paths(files))
			}
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
		})
	}
}

// The RPC methods written in the proxyfile give what the methods file gives, the two together are refused, and the
// methods' own rules hold in either form.
func TestInlineMethods(t *testing.T) {
	partial := string(testsupport.ReadFile(t, "../../examples/partial/gateway.yaml")) + testsupport.PartialMethods
	// The minimal example has no rpc entry, so methods written into it are given for no RPC route.
	minimalMethods := string(testsupport.ReadFile(t, "../../examples/minimal/gateway.yaml")) +
		"rpc:\n  methods:\n    - {service: example.backend.v1.ReportService, method: ListReports}\n"
	// The HOCON row writes its own list in place of the partial example's include line. strings.Replace changes nothing
	// when that line is missing, and the row would then fail on another problem, which does not say why; the guard
	// does.
	partialConf := string(testsupport.ReadFile(t, "../../examples/partial/gateway.conf"))
	const include = `include required("rpc-methods.conf")` + "\n"
	if !strings.Contains(partialConf, include) {
		t.Fatalf("the partial HOCON example holds no %q to replace", include)
	}
	twice := strings.Replace(partialConf, include, `rpc { methods = [ { service = "example.backend.v1.ReportService", method = ListReports }, `+
		`{ service = "example.backend.v1.ReportService", method = ListReports } ] }`+"\n", 1)
	cases := []struct {
		name, file, proxyfile string // proxyfile is written to file in a temporary directory
		rpcMethods            string // given as --rpc-methods when set
		// want is the problems; a row that wants none compares each file with the partial example's committed one.
		want []string
	}{
		{name: "inline gives the flag's table", file: "g.yaml", proxyfile: partial},
		{name: "both given", file: "g.yaml", proxyfile: partial, rpcMethods: "../../examples/partial/methods.json",
			want: []string{"g.yaml:30:3: rpc.methods is written here, so --rpc-methods must not be given"}},
		{name: "HOCON: a method listed twice", file: "g.conf", proxyfile: twice, want: []string{
			"g.conf: rpc.methods[1]: the method example.backend.v1.ReportService/ListReports is listed twice (first at rpc.methods[0])"}},
		{name: "inline, no rpc entry", file: "g.yaml", proxyfile: minimalMethods,
			want: []string{"g.yaml:8:1: RPC methods are given, but the routes have no rpc entry"}},
		// With both given, one run names the inline methods' own problem as well as the second list, sorted.
		{name: "both given, no rpc entry", file: "g.yaml", proxyfile: minimalMethods, rpcMethods: "../../examples/partial/methods.json",
			want: []string{
				"g.yaml:8:1: RPC methods are given, but the routes have no rpc entry",
				"g.yaml:11:3: rpc.methods is written here, so --rpc-methods must not be given",
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			req := Request{Proxyfile: filepath.Join(dir, c.file), Out: filepath.Join(dir, "envoy.yaml"),
				RPCMethods: c.rpcMethods, RPCTable: filepath.Join(dir, "rpc-table.json")}
			testsupport.WriteFile(t, req.Proxyfile, c.proxyfile)
			files, problems := render(req)
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Fatalf("problems = %q, want %q", got, c.want)
			}
			if c.want != nil {
				return
			}
			if got, want := paths(files), outputs(req); !slices.Equal(got, want) {
				t.Fatalf("render paths = %q, want %q", got, want)
			}
			for _, file := range files {
				if d := committedDifference(t, "partial", file); d != "" {
					t.Error(d)
				}
			}
		})
	}
}

// A tool may switch the check by editing this line of the generated file as text, so it appears exactly once.
func TestAuthSwitchLine(t *testing.T) {
	cases := []struct{ example, line string }{
		{"full", "default_value: {numerator: 100, denominator: HUNDRED}"},
		{"partial", "default_value: {numerator: 0, denominator: HUNDRED}"},
	}
	for _, c := range cases {
		t.Run(c.example, func(t *testing.T) {
			files, problems := render(requestFor(t, c.example, "envoy.yaml"))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := bytes.Count(files[0].Bytes, []byte(c.line)); got != 1 {
				t.Errorf("the line %q appears %d times, want 1", c.line, got)
			}
		})
	}
}

// An RPC route's question for the check: a header only when it has a value, no entry at all for a method that asks
// nothing, and the question's entry beside the route's own bridge and Lua entries and its metadata.
func TestRPCQuestion(t *testing.T) {
	const proxyfile = `listen: 8000
services:
  q: {http: 3000, grpc: 3001}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow}
origins: origins.json
routes:
  - rpc: {to: {"*": q}}
  - {name: q, prefix: /, to: q}
`
	const methods = `[
  {"service": "q.v1.S", "method": "Nothing"},
  {"service": "q.v1.S", "method": "ActionAlone", "action": "use"},
  {"service": "q.v1.S", "method": "ReportsAlone", "reports": ["operate", "use"]},
  {"service": "q.v1.S", "method": "Both", "action": "use", "reports": ["operate"]}
]
`
	cases := []struct{ name, path, want string }{
		{name: "nothing asked", path: "/q.v1.S/Nothing", want: `
metadata: {filter_metadata: {proxyfile: {action: ""}}}
typed_per_filter_config:
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "action alone", path: "/q.v1.S/ActionAlone", want: `
metadata: {filter_metadata: {proxyfile: {action: use}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-action, value: use}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "reports alone", path: "/q.v1.S/ReportsAlone", want: `
metadata: {filter_metadata: {proxyfile: {action: ""}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-reports, value: "operate,use"}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "both, beside the route's own entries", path: "/q.v1.S/Both", want: `
metadata: {filter_metadata: {proxyfile: {action: use}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-action, value: use}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
      - append: {header: {key: x-gateway-reports, value: operate}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
	}
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), proxyfile)
	testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(testsupport.ReadFile(t, "testdata/origins.json")))
	testsupport.WriteFile(t, filepath.Join(dir, "methods.json"), methods)
	files, problems := render(Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml"),
		RPCMethods: filepath.Join(dir, "methods.json")})
	if problems != nil {
		t.Fatalf("render problems = %q, want none", testsupport.ProblemStringsIn(dir, problems))
	}
	routes := listAt(t, decode(t, files[0].Bytes), "static_resources", "listeners", 0, "filter_chains", 0, "filters", 0,
		"typed_config", "route_config", "virtual_hosts", 0, "routes")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := slices.IndexFunc(routes, func(route any) bool {
				return valueAt(route, "match", "path") == c.path
			})
			if i < 0 {
				t.Fatalf("no route of the first virtual host matches the path %s", c.path)
			}
			route := routes[i].(map[string]any)
			got := map[string]any{"metadata": route["metadata"], "typed_per_filter_config": route["typed_per_filter_config"]}
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// Each row holds a combination no example has, so it is the only proof of the part it names: an rpc entry without
// auth adds the Lua filter and the bridge but not set_metadata or ext_authz, and its RPC routes carry no metadata and
// no question; a gateway with auth and no rpc entry has no Lua filter and no bridge; connections without heap watch
// connections alone; the check's switch header is the one only_with_header names; with auth, the early header removal
// adds the question headers and the accept names that remove_from_clients does not already cover; and an environment
// that admits no browser origin writes an empty CORS list.
func TestPartsNoExampleCombines(t *testing.T) {
	manager := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config"}
	const rpcWithoutAuth = `listen: 8000
services:
  q: {http: 3000, grpc: 3001}
origins: origins.json
routes:
  - rpc: {to: {"*": q}}
  - {name: q, prefix: /, to: q}
`
	const rpcMethod = `[{"service": "q.v1.S", "method": "Do", "action": "use", "reports": ["operate"]}]`
	cases := []struct {
		name, proxyfile, methods string // methods is written to methods.json and given to render when not empty
		origins                  string // written to origins.json; testdata/origins.json when empty
		at                       []any  // the sub-tree the row compares, as valueAt's steps
		// names compares only the name of each item of the list at at, which keeps the Lua script out of a filter row.
		names bool
		want  string
	}{
		{name: "rpc without auth, its filters", proxyfile: rpcWithoutAuth, methods: rpcMethod,
			at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors, envoy.filters.http.lua,
  envoy.filters.http.connect_grpc_bridge, envoy.filters.http.router]`},
		{name: "rpc without auth", proxyfile: rpcWithoutAuth, methods: rpcMethod,
			at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 0}),
			want: `
name: q-rpc
match: {path: /q.v1.S/Do}
typed_per_filter_config:
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
route: {cluster: q_grpc}
`},
		{name: "auth without rpc",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: slices.Concat(manager, []any{"http_filters"}),
			want: `
- name: envoy.filters.http.set_metadata
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.set_metadata.v3.Config
    metadata: [{metadata_namespace: proxyfile, value: {filtered: true}}]
- name: envoy.filters.http.header_mutation
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutation
    mutations: {response_mutations: [{append: {header: {key: vary, value: Origin}, append_action: APPEND_IF_EXISTS_OR_ADD}}]}
- {name: envoy.filters.http.rbac, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC}}
- {name: envoy.filters.http.cors, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.cors.v3.Cors}}
- name: envoy.filters.http.ext_authz
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
    http_service:
      server_uri: {uri: "http://127.0.0.1:3000/check", cluster: q_http, timeout: 1s}
      path_override: /check
      authorization_request: {headers_to_add: [{key: x-gateway-timeout-ms, value: "1000"}]}
    failure_mode_allow: true
    failure_mode_allow_header_add: true
    allowed_headers: {patterns: [{exact: x-gateway-action}, {exact: x-gateway-reports}]}
    emit_filter_state_stats: true
    filter_enabled: {default_value: {numerator: 100, denominator: HUNDRED}, runtime_key: gateway.ext_authz.enabled}
- name: envoy.filters.http.router
  typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router, suppress_envoy_headers: true}
`},
		{name: "connections without heap",
			proxyfile: `listen: 8000
limits: {connections: 100}
services:
  q: {http: 3000}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: []any{"overload_manager"},
			want: `
refresh_interval: 0.25s
resource_monitors:
- name: envoy.resource_monitors.global_downstream_max_connections
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig
    max_active_downstream_connections: 100
`},
		{name: "only_with_header other than authorization",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow, only_with_header: x-api-key}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			// The filters before ext_authz are set_metadata, the header mutation, RBAC and CORS.
			at: slices.Concat(manager, []any{"http_filters", 4, "typed_config", "xds_matcher"}),
			want: `
matcher_list:
  matchers:
  - predicate:
      not_matcher:
        single_predicate:
          input:
            name: x-api-key
            typed_config:
              "@type": type.googleapis.com/envoy.type.matcher.v3.HttpRequestHeaderMatchInput
              header_name: x-api-key
          value_match: {safe_regex: {google_re2: {}, regex: ".+"}}
    on_match:
      action:
        name: skip
        typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.common.matcher.action.v3.SkipFilter}
`},
		// x-identity is covered by the user's own entry, so it is not removed twice; x-tier-* is not, so its prefix is
		// added after the question headers.
		{name: "auth removes the check's headers from clients",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow, accept: [x-identity, x-tier-*]}
remove_from_clients: [x-identity]
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}),
			want: `
- remove: x-identity
- remove: x-gateway-action
- remove: x-gateway-reports
- remove_on_match: {key_matcher: {prefix: x-tier-}}
- remove: x-envoy-auth-failure-mode-allowed
`},
		// RBAC admits only a request without an Origin and an origin on the environment's own host, and the CORS policy
		// keeps its list, written empty.
		{name: "an environment with no origins and no patterns",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			origins: `{"environments": [{"name": "closed", "hosts": ["localhost"]}]}`,
			at:      slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "typed_per_filter_config"}),
			want: `
envoy.filters.http.rbac:
  "@type": type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBACPerRoute
  rbac:
    rules:
      action: ALLOW
      policies:
        browser-origins:
          permissions: [{any: true}]
          principals:
          - {not_id: {header: {name: origin, present_match: true}}}
          - {header: {name: origin, string_match: {safe_regex: {regex: '^https?://localhost(:[0-9]+)?$'}}}}
envoy.filters.http.cors:
  "@type": type.googleapis.com/envoy.extensions.filters.http.cors.v3.CorsPolicy
  allow_origin_string_match: []
  forward_not_matching_preflights: false
`},
	}
	defaultOrigins := string(testsupport.ReadFile(t, "testdata/origins.json"))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), c.proxyfile)
			testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), cmp.Or(c.origins, defaultOrigins))
			req := Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml")}
			if c.methods != "" {
				req.RPCMethods = filepath.Join(dir, "methods.json")
				testsupport.WriteFile(t, req.RPCMethods, c.methods)
			}
			files, problems := render(req)
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStringsIn(dir, problems))
			}
			got := valueAt(decode(t, files[0].Bytes), c.at...)
			if c.names {
				got = names(got)
			}
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// names returns the name of each item of a list, or the value unchanged when it is not a list, so a difference
// names what was found.
func names(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	found := make([]any, len(list))
	for i, item := range list {
		found[i] = valueAt(item, "name")
	}
	return found
}

// valueAt follows keys of maps and indexes of lists down a decoded tree, and returns nil when a step is missing.
func valueAt(tree any, steps ...any) any {
	for _, step := range steps {
		switch key := step.(type) {
		case string:
			m, _ := tree.(map[string]any)
			tree = m[key]
		case int:
			list, _ := tree.([]any)
			if key >= len(list) {
				return nil
			}
			tree = list[key]
		}
	}
	return tree
}

// listAt is valueAt for a step that must end at a list, and ends the test when it does not.
func listAt(t *testing.T, tree any, steps ...any) []any {
	t.Helper()
	list, ok := valueAt(tree, steps...).([]any)
	if !ok {
		t.Fatalf("no list at %v", steps)
	}
	return list
}

// requestFor points a request at examples/<name>: with the methods file when the example has methods.json, and with
// the RPC table when it commits rpc-table.json. The two are tested apart, because an example may write its methods
// in the proxyfile and still commit a table.
func requestFor(t *testing.T, name, out string) Request {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", name)
	req := Request{Proxyfile: filepath.Join(dir, "gateway.yaml"), Out: out}
	if methods := filepath.Join(dir, "methods.json"); exists(methods) {
		req.RPCMethods = methods
	}
	if exists(filepath.Join(dir, "rpc-table.json")) {
		req.RPCTable = filepath.Join(filepath.Dir(out), "rpc-table.json")
	}
	return req
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// render reads every input and returns every output: Read and Files in one step, as the command line runs them, but
// without the command line's rule that no output path names an input or the other output.
func render(req Request) ([]File, []model.Problem) {
	run, problems := Read(req)
	if problems != nil {
		return nil, problems
	}
	return run.Files()
}

// outputs is the paths render must return for req, in its order.
func outputs(req Request) []string {
	if req.RPCTable == "" {
		return []string{req.Out}
	}
	return []string{req.Out, req.RPCTable}
}

func paths(files []File) []string {
	var list []string
	for _, file := range files {
		list = append(list, file.Path)
	}
	return list
}

func decode(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return value
}

// firstDifference compares two decoded trees and names the first place they differ, or returns "". Maps compare
// over the sorted union of their keys, lists item by item and then by length, and scalars with reflect.DeepEqual,
// so 0.90 equals 0.9 and the text "2000" differs from the number 2000.
func firstDifference(want, got any, path string) string {
	wantMap, wantIsMap := want.(map[string]any)
	gotMap, gotIsMap := got.(map[string]any)
	if wantIsMap && gotIsMap {
		keys := slices.Collect(maps.Keys(wantMap))
		for key := range gotMap {
			if _, both := wantMap[key]; !both {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			wantValue, inWant := wantMap[key]
			gotValue, inGot := gotMap[key]
			switch {
			case !inWant:
				return difference(join(path, key), absent, gotValue)
			case !inGot:
				return difference(join(path, key), wantValue, absent)
			}
			if d := firstDifference(wantValue, gotValue, join(path, key)); d != "" {
				return d
			}
		}
		return ""
	}
	wantList, wantIsList := want.([]any)
	gotList, gotIsList := got.([]any)
	if wantIsList && gotIsList {
		for i := range min(len(wantList), len(gotList)) {
			if d := firstDifference(wantList[i], gotList[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		if len(wantList) != len(gotList) {
			return fmt.Sprintf("first difference at %s: want %d items, got %d", path, len(wantList), len(gotList))
		}
		return ""
	}
	if !reflect.DeepEqual(want, got) {
		return difference(path, want, got)
	}
	return ""
}

// absent stands for a key one side does not have, and prints as <absent>.
var absent = absentValue{}

type absentValue struct{}

func (absentValue) String() string { return "<absent>" }

func difference(path string, want, got any) string {
	return fmt.Sprintf("first difference at %s: want %v, got %v", path, want, got)
}

// join writes a key after a path with a dot, or as ["key"] when the key holds a dot itself, so the path can be read
// back without guessing where a key ends.
func join(path, key string) string {
	if strings.Contains(key, ".") {
		return fmt.Sprintf("%s[%q]", path, key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}
