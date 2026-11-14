package format

import (
	"net"
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
		{Name: "lean", Title: "Lean", Keys: map[string]Support{"a": {Level: Partial, Note: "counts | per second", Refuses: []Refusal{{Form: "above 10", Instead: "write 10 or less"}}},
			"b": {Level: Refused, Note: "a `b` of its own"}}},
	}
	want := "| Key | Envoy | Lean |\n|---|---|---|\n" +
		"| `a` | supported | partial: counts \\| per second; refuses above 10: write 10 or less |\n" +
		"| `b` | supported | refused: a \\`b\\` of its own |\n"
	if got := string(SupportMatrix(CoreKeys(spec), targets)); got != want {
		t.Errorf("SupportMatrix = %q, want %q", got, want)
	}
}

// Every target declares every row of the support table, each core key and each behaviour written with no key, and no
// other, so a key added to the format cannot go undeclared; none refuses a behaviour written with no key, in whole or
// in a form, because every configuration holds it; and a refused form sits only on a partial key, with a test of the
// value: on a supported key the table would say supported while render refuses, on a refused key render would name the
// key twice, and with no test render would stop on a nil function.
func TestTargetDeclarations(t *testing.T) {
	rows := MatrixRows()
	for _, target := range Targets {
		var undeclared, notRows []string
		for _, row := range rows {
			if _, declared := target.Keys[row]; !declared {
				undeclared = append(undeclared, row)
			}
		}
		for key, support := range target.Keys {
			if !slices.Contains(rows, key) {
				notRows = append(notRows, key)
			}
			if support.Refuses != nil && support.Level != Partial {
				t.Errorf("%s: %q lists refused forms, which only a partial key may", target.Name, key)
			}
			for _, refusal := range support.Refuses {
				if refusal.Applies == nil {
					t.Errorf("%s: %q refuses the form %q with no Applies to tell it", target.Name, key, refusal.Form)
				}
			}
		}
		slices.Sort(notRows)
		if !slices.Equal(undeclared, nil) {
			t.Errorf("%s: the rows of the support table it does not declare = %q, want none", target.Name, undeclared)
		}
		if !slices.Equal(notRows, nil) {
			t.Errorf("%s: the keys it declares that are no row of the support table = %q, want none", target.Name, notRows)
		}
		for _, row := range WrittenWithNoKey {
			if support := target.Keys[row]; support.Level == Refused || support.Refuses != nil {
				t.Errorf("%s refuses %q, which every configuration holds", target.Name, row)
			}
		}
	}
}

// A key the target refuses is named at its own position, also inside a Dict's value or a list item, and in the origins
// file and the methods file by the path the proxyfile writes it at; a file that writes none of them has no problem. A
// form refused for another key reads that key in the proxyfile, also for a key of a methods file. nginx's own
// declarations refuse each form in the words nginx's users read, and leave the form beside it alone.
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
	// picky carries retries, an address and a method's reports in some forms only: a value, or a value without another
	// key of the proxyfile.
	picky := Target{Name: "picky", Title: "Picky", Keys: map[string]Support{
		"rpc.methods[].reports": {Level: Partial, Note: "picky tells the check reports through auth.ask only", Refuses: []Refusal{{
			Form: "without auth.ask", Instead: "write auth.ask",
			Applies: func(_, proxyfile *tree.Node) bool { return proxyfile.Get("auth").Get("ask") == nil }}}},
		"routes[].retries": {Level: Partial, Note: "picky tries a request once", Refuses: []Refusal{{Form: "of 1 or more", Instead: "write retries: 0",
			Applies: func(value, root *tree.Node) bool { return value.Text != "0" }}}},
		"services.<service name>.address": {Level: Partial, Note: "picky resolves a name with its own resolver only", Refuses: []Refusal{{
			Form: "naming a host without dns.resolver", Instead: "write dns.resolver",
			Applies: func(value, root *tree.Node) bool {
				return net.ParseIP(value.Text) == nil && root.Get("dns").Get("resolver") == nil
			}}}}}}
	// The words of nginx's refusals, which several of its rows repeat.
	const (
		notWrittenWords = "is refused by --target nginx: not written by this version's nginx generator; render it for envoy"
		jwtWords        = "is refused by --target nginx: nginx open source cannot verify a token (nginx Plus's auth_jwt); verify it in the service, or render it for envoy"
		healthWords     = "is refused by --target nginx: nginx open source has no active health checks (nginx Plus's health_check); remove health, and nginx keeps every backend in the pool"
		xrayWords       = "is refused by --target nginx: nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter"
		zoneWords       = "nginx reads no zone in a backend's address; give the service an address without one"
		pathWords       = "holding %, a character Envoy escapes before it picks a route (\" < > ^ ` { | }) or one outside ASCII is refused by --target nginx: nginx matches a path holding a character Envoy escapes as it decodes it, where Envoy matches the escape, so a prefix or a path with % or such a character would take other requests on nginx than on Envoy; write it without them, as a pattern, or render it for envoy"
		rewriteWords    = "holding a character nginx escapes again is refused by --target nginx: nginx escapes the new path before it sends it, so the service would receive that character escaped, where the Envoy output sends it as written; write the rewrite without it, or render it for envoy"
		hostDotWords    = "holding a host that starts or ends with . is refused by --target nginx: nginx reads a dot at a host's start as every name under it and drops one at its end; write the host without it, or render it for envoy"
	)
	cases := []struct {
		name   string
		spec   Field
		target Target
		// file is read when set, and named in the problems by this path; otherwise document is read as g.yaml.
		file, document string
		// beside is the proxyfile, read as p.yaml, that an origins or a methods document is read beside; "" when the
		// document is the proxyfile.
		beside string
		want   []string
	}{
		{name: "nothing refused is written", spec: Proxyfile, target: lean, document: m.String(), want: nil},
		{name: "a refused key", spec: Proxyfile, target: lean, document: m.Inserted(7, "timeouts: {idle: 30s}"),
			want: []string{`g.yaml:7:12: "timeouts.idle" is refused by --target lean: lean has no idle timeout`}},
		{name: "a refused key in a service", spec: Proxyfile, target: lean, document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: /h}}"),
			want: []string{`g.yaml:3:21: "services.<service name>.health" is refused by --target lean: lean has no health checks`}},
		{name: "a refused key in a list item", spec: Proxyfile, target: lean, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, retries: 1}"),
			want: []string{`g.yaml:6:37: "routes[].retries" is refused by --target lean: lean retries no request`}},
		{name: "envoy refuses no key of the full example", spec: Proxyfile, target: Targets[0], file: "../../examples/full/yaml/proxyfile.yaml", want: nil},
		{name: "a refused cors key in an origins file", spec: Origins, target: lean, beside: m.String(),
			document: `{"cors": {"max_age": "600"}, "environments": [{"name": "all", "hosts": ["localhost"]}]}`,
			want:     []string{`g.yaml:1:11: "cors.max_age" is refused by --target lean: lean sends no max age`}},
		{name: "a refused key of an environment in an origins file", spec: Origins, target: lean, beside: m.String(),
			document: `{"environments": [{"name": "all", "hosts": ["localhost"], "patterns": ["http://*.localhost"]}]}`,
			want:     []string{`g.yaml:1:59: "origins.<environment name>.patterns" is refused by --target lean: lean admits no pattern`}},
		{name: "a refused environment in an origins file, at its name", spec: Origins, target: oneHost, beside: m.String(),
			document: `{"environments": [{"name": "all", "hosts": ["localhost"]}]}`,
			want:     []string{`g.yaml:1:20: "origins.<environment name>" is refused by --target one-host: one-host serves one host`}},
		{name: "a refused key of a method in a methods file", spec: Methods, target: lean, beside: m.String(),
			document: `[{"service": "shop.v1.CartService", "method": "AddItem", "action": "write"}]`,
			want:     []string{`g.yaml:1:58: "rpc.methods[].action" is refused by --target lean: lean tells the check no action`}},
		// Written in the proxyfile, each of the keys above is refused once, at its own line.
		{name: "the same keys in the proxyfile", spec: Proxyfile, target: lean,
			document: m.Replaced(4, 6, `cors: {max_age: "600"}`, `origins: {all: {hosts: [localhost], patterns: ["http://*.localhost"]}}`,
				"routes:", "  - {name: app, prefix: /, to: app}", "rpc: {methods: [{service: shop.v1.CartService, method: AddItem, action: write}]}"),
			want: []string{`g.yaml:4:8: "cors.max_age" is refused by --target lean: lean sends no max age`,
				`g.yaml:5:37: "origins.<environment name>.patterns" is refused by --target lean: lean admits no pattern`,
				`g.yaml:8:65: "rpc.methods[].action" is refused by --target lean: lean tells the check no action`}},
		{name: "a value the target refuses", spec: Proxyfile, target: picky, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, retries: 1}"),
			want: []string{`g.yaml:6:37: "routes[].retries" of 1 or more is refused by --target picky: write retries: 0`}},
		{name: "a value of the same key the target takes", spec: Proxyfile, target: picky, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, retries: 0}"), want: nil},
		{name: "a value refused for want of another key", spec: Proxyfile, target: picky, document: m.Replaced(3, 3, "  app: {http: 3000, address: app.internal}"),
			want: []string{`g.yaml:3:21: "services.<service name>.address" naming a host without dns.resolver is refused by --target picky: write dns.resolver`}},
		{name: "the same value with the other key", spec: Proxyfile, target: picky,
			document: m.Replaced(3, 3, "  app: {http: 3000, address: app.internal}", `dns: {resolver: "10.0.0.2:53"}`), want: nil},
		{name: "an address no resolver reads", spec: Proxyfile, target: picky, document: m.Replaced(3, 3, "  app: {http: 3000, address: 10.0.1.5}"), want: nil},
		{name: "a key of a methods file refused for want of a key of the proxyfile", spec: Methods, target: picky, beside: m.String(),
			document: `[{"service": "shop.v1.CartService", "method": "AddItem", "reports": ["export"]}]`,
			want:     []string{`g.yaml:1:58: "rpc.methods[].reports" without auth.ask is refused by --target picky: write auth.ask`}},
		{name: "the same methods file beside a proxyfile that writes the key", spec: Methods, target: picky,
			beside: m.Inserted(7, "auth: {ask: app /check}"), document: `[{"service": "shop.v1.CartService", "method": "AddItem", "reports": ["export"]}]`, want: nil},
		{name: "the same environment in the proxyfile, at its key", spec: Proxyfile, target: oneHost,
			document: m.Replaced(4, 4, "origins: {all: {hosts: [localhost]}}"),
			want:     []string{`g.yaml:4:11: "origins.<environment name>" is refused by --target one-host: one-host serves one host`}},
		{name: "a whole-request limit", spec: Proxyfile, target: nginxTarget, document: m.Inserted(7, "timeouts: {request: 30s}"),
			want: []string{`g.yaml:7:12: "timeouts.request" a time is refused by --target nginx: nginx has no limit on a whole request (its time limits count the time between two reads or writes); write timeouts.request: none, or render it for envoy`}},
		{name: "no whole-request limit", spec: Proxyfile, target: nginxTarget, document: m.Inserted(7, "timeouts: {request: none}"), want: nil},
		{name: "an address by name", spec: Proxyfile, target: nginxTarget, document: m.Replaced(3, 3, "  app: {http: 3000, address: catalog.internal}"),
			want: []string{`g.yaml:3:21: "services.<service name>.address" a DNS name is refused by --target nginx: not written by this version's nginx generator; render it for envoy`}},
		{name: "an address by IP", spec: Proxyfile, target: nginxTarget, document: m.Replaced(3, 3, "  app: {http: 3000, address: 10.0.2.21}"), want: nil},
		// The reader takes a zoned IPv6 address as an IP address (netip.ParseAddr); nginx loads none in an upstream.
		{name: "a zoned IPv6 address", spec: Proxyfile, target: nginxTarget, document: m.Replaced(3, 3, `  app: {http: 3000, address: "fe80::1%en0"}`),
			want: []string{`g.yaml:3:21: "services.<service name>.address" a zoned IPv6 address is refused by --target nginx: ` + zoneWords}},
		{name: "a zoned IPv6 address in addresses", spec: Proxyfile, target: nginxTarget,
			document: m.Replaced(3, 3, `  app: {http: 3000, addresses: ["10.0.2.21", "fe80::1%en0"]}`),
			want:     []string{`g.yaml:3:21: "services.<service name>.addresses" holding a zoned IPv6 address is refused by --target nginx: ` + zoneWords}},
		{name: "an IPv6 address with no zone", spec: Proxyfile, target: nginxTarget, document: m.Replaced(3, 3, `  app: {http: 3000, addresses: ["10.0.2.21", "fe80::1"]}`),
			want: nil},
		{name: "a rewrite holding ?", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, `  - {name: app, prefix: /, to: app, rewrite: "/hooks?x=1"}`),
			want: []string{`g.yaml:6:37: "routes[].rewrite" holding ? is refused by --target nginx: nginx reads a ? in a rewrite as the start of new arguments, after which it adds the client's; write the path without ?`}},
		{name: "a rewrite with no ?", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, rewrite: /hooks}"), want: nil},
		// nginx matches a path holding a character Envoy escapes before it matches on the path it has decoded, so a
		// prefix or a path with % or with such a character takes other requests on nginx; every other character is
		// matched alike, on the path as the client wrote it or on the one both proxies normalise.
		{name: "a prefix holding an escape", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, prefix: /a%20b/, to: app}"),
			want: []string{`g.yaml:6:17: "routes[].prefix" ` + pathWords}},
		{name: "a prefix holding |", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, prefix: /a|b/, to: app}"),
			want: []string{`g.yaml:6:17: "routes[].prefix" ` + pathWords}},
		{name: "a prefix holding a reserved character", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, prefix: /a+b/, to: app}"),
			want: nil},
		{name: "a prefix holding nginx's own syntax", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, `  - {name: app, prefix: "/s;c{d}#e/", to: app}`),
			want: []string{`g.yaml:6:17: "routes[].prefix" ` + pathWords}},
		{name: "a prefix of unreserved characters", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, prefix: /a-b.c_d~e/9/, to: app}"),
			want: nil},
		{name: "a path holding :", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, path: /v1/x:list, to: app}"),
			want: nil},
		{name: "a path holding a letter outside ASCII", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, "  - {name: app, path: /é, to: app}"),
			want: []string{`g.yaml:6:17: "routes[].path" ` + pathWords}},
		{name: "a rewrite holding %", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, `  - {name: app, prefix: /, to: app, rewrite: "/t%20u/"}`),
			want: []string{`g.yaml:6:37: "routes[].rewrite" ` + rewriteWords}},
		{name: "a rewrite holding #", spec: Proxyfile, target: nginxTarget, document: m.Replaced(6, 6, `  - {name: app, prefix: /, to: app, rewrite: "/r#x/"}`),
			want: []string{`g.yaml:6:37: "routes[].rewrite" ` + rewriteWords}},
		{name: "a rewrite of characters both proxies send as written", spec: Proxyfile, target: nginxTarget,
			document: m.Replaced(6, 6, `  - {name: app, prefix: /, to: app, rewrite: "/q[x]'y$z;w!&()*+,=:@-._~/"}`), want: nil},
		{name: "a host with a dot at its start", spec: Proxyfile, target: nginxTarget, document: m.Replaced(4, 4, "origins: {all: {hosts: [.example.com]}}"),
			want: []string{`g.yaml:4:17: "origins.<environment name>.hosts" ` + hostDotWords}},
		{name: "a host with a dot at its end", spec: Proxyfile, target: nginxTarget, document: m.Replaced(4, 4, "origins: {all: {hosts: [example.com.]}}"),
			want: []string{`g.yaml:4:17: "origins.<environment name>.hosts" ` + hostDotWords}},
		{name: "the host .", spec: Proxyfile, target: nginxTarget, document: m.Replaced(4, 4, `origins: {all: {hosts: ["."]}}`),
			want: []string{`g.yaml:4:17: "origins.<environment name>.hosts" ` + hostDotWords}},
		{name: "a host with a dot at its start in an origins file", spec: Origins, target: nginxTarget, beside: m.String(),
			document: `{"environments": [{"name": "all", "hosts": [".example.com"]}]}`,
			want:     []string{`g.yaml:1:35: "origins.<environment name>.hosts" ` + hostDotWords}},
		{name: "a host with dots inside", spec: Proxyfile, target: nginxTarget, document: m.Replaced(4, 4, "origins: {all: {hosts: [api.example.com]}}"), want: nil},
		{name: "health", spec: Proxyfile, target: nginxTarget, document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: /health}}"),
			want: []string{`g.yaml:3:21: "services.<service name>.health" ` + healthWords, `g.yaml:3:30: "services.<service name>.health.path" ` + healthWords}},
		// auth.jwt as examples/jwt writes it: a line for each key under it, and the not-written line of auth itself.
		{name: "a token verified at the proxy", spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, "auth:\n  jwt:\n    issuer: https://login.example.com\n    audiences: [api.example.com]\n"+
				"    keys: /etc/gateway/jwt/jwks.json\n    claims:\n      x-user-id: sub\n      x-user-roles: realm.roles"),
			want: []string{`g.yaml:7:1: "auth" ` + notWrittenWords, `g.yaml:8:3: "auth.jwt" ` + jwtWords, `g.yaml:9:5: "auth.jwt.issuer" ` + jwtWords,
				`g.yaml:10:5: "auth.jwt.audiences" ` + jwtWords, `g.yaml:11:5: "auth.jwt.keys" ` + jwtWords, `g.yaml:12:5: "auth.jwt.claims" ` + jwtWords,
				`g.yaml:13:7: "auth.jwt.claims.<header name>" ` + jwtWords, `g.yaml:14:7: "auth.jwt.claims.<header name>" ` + jwtWords}},
		{name: "an X-Ray tracer", spec: Proxyfile, target: nginxTarget, document: m.Inserted(7, "tracing: {provider: {xray: {segment: s}}}"),
			want: []string{`g.yaml:7:1: "tracing" ` + notWrittenWords, `g.yaml:7:11: "tracing.provider" ` + notWrittenWords,
				`g.yaml:7:22: "tracing.provider.xray" ` + xrayWords, `g.yaml:7:29: "tracing.provider.xray.segment" ` + xrayWords}},
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
			proxyfile := root
			if c.beside != "" {
				if proxyfile, problems = tree.FromYAML("p.yaml", []byte(c.beside)); problems != nil {
					t.Fatalf("FromYAML problems = %q, want none", testsupport.ProblemStrings(problems))
				}
			}
			if got := testsupport.ProblemStrings(Refusals(c.spec, root, proxyfile, c.target)); !slices.Equal(got, c.want) {
				t.Errorf("Refusals = %q, want %q", got, c.want)
			}
		})
	}
}

// The keys a file writes, each once and sorted, in CoreKeys' spelling: a Dict's value and a list item by their place,
// a methods file's keys at the path the proxyfile writes them at, and a proxy's block as written.
func TestWrittenKeys(t *testing.T) {
	cases := []struct {
		name     string
		spec     Field
		document string
		want     []string
	}{
		{name: "the smallest proxyfile", spec: Proxyfile, document: m.String(),
			want: []string{"listen", "origins", "routes", "routes[].name", "routes[].prefix", "routes[].to", "services",
				"services.<service name>", "services.<service name>.http"}},
		{name: "a methods file", spec: Methods, document: `[{"service": "shop.v1.CartService", "method": "AddItem", "action": "write"}]`,
			want: []string{"rpc.methods[].action", "rpc.methods[].method", "rpc.methods[].service"}},
		{name: "two routes and a proxy's block", spec: Proxyfile,
			document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, retries: 1}", "  - {name: api, prefix: /api/, to: app}", "envoy: {connect_bridge: false}"),
			want: []string{"envoy", "envoy.connect_bridge", "listen", "origins", "routes", "routes[].name", "routes[].prefix", "routes[].retries",
				"routes[].to", "services", "services.<service name>", "services.<service name>.http"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, problems := tree.FromYAML("g.yaml", []byte(c.document))
			if problems != nil {
				t.Fatalf("FromYAML problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := WrittenKeys(c.spec, root); !slices.Equal(got, c.want) {
				t.Errorf("WrittenKeys = %q, want %q", got, c.want)
			}
		})
	}
}
