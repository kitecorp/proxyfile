package format

import (
	"maps"
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
			"b": {Level: Refused, Note: "a `b` of its own"}},
			LessStrict: []LessStrict{{Row: "a", Name: "a | served", Measured: "m"}, {Row: "a", Name: "two", Measured: "m"}}},
	}
	want := "| Key | Envoy | Lean |\n|---|---|---|\n" +
		"| `a` | supported | partial: counts \\| per second; refuses above 10: write 10 or less; less strict than the Envoy output: a \\| served; two |\n" +
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
		// A less strict way is a difference, so its row is partial, and the page and the cell tell the ways apart by
		// name.
		var lessStrict []string
		for _, way := range target.LessStrict {
			if target.Keys[way.Row].Level != Partial {
				t.Errorf("%s: %q is on %q, which it does not declare partial", target.Name, way.Name, way.Row)
			}
			if way.Name == "" || way.Measured == "" || slices.Contains(lessStrict, way.Name) {
				t.Errorf("%s: a less strict way named %q, with %q measured, is unnamed, says nothing or has a name twice", target.Name, way.Name, way.Measured)
			}
			lessStrict = append(lessStrict, way.Name)
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
		dollarWords     = "nginx reads a $ in a certificate's or a key's path as the start of a variable, and a path written to keep the $ is read at each handshake, which nginx -t does not check; put the file at a path without $, or render it for envoy"
		hostDotWords    = "holding a host that starts or ends with . is refused by --target nginx: nginx reads a dot at a host's start as every name under it and drops one at its end; write the host without it, or render it for envoy"
	)
	type refusalCase struct {
		name   string
		spec   Field
		target Target
		// file is read when set, and named in the problems by this path; otherwise document is read as g.yaml.
		file, document string
		// beside is the proxyfile, read as p.yaml, that an origins or a methods document is read beside; "" when the
		// document is the proxyfile.
		beside string
		want   []string
	}
	cases := []refusalCase{
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
		// nginx reads a $ in a certificate's or a key's path as the start of a variable, and a CA's path as written
		// (measured on nginx 1.30.6 and 1.31.8).
		{name: "a $ in the certificate's path", spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, `tls: {certificate: "/etc/a$b/cert.pem", key: /etc/tls/key.pem}`),
			want:     []string{`g.yaml:7:7: "tls.certificate" holding $ is refused by --target nginx: ` + dollarWords}},
		{name: "a $ in the key's path", spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, `tls: {certificate: /etc/tls/cert.pem, key: "/etc/a$b/key.pem"}`),
			want:     []string{`g.yaml:7:39: "tls.key" holding $ is refused by --target nginx: ` + dollarWords}},
		{name: "a $ in the clients CA's path", spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, `tls: {certificate: /etc/tls/cert.pem, key: /etc/tls/key.pem, clients: {ca: "/etc/a$b/ca.pem"}}`), want: nil},
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
		{name: "a name with a * prefix", spec: Proxyfile, target: nginxTarget, document: m.Inserted(7, "remove_from_clients: [x-user-*]"),
			want: []string{`g.yaml:7:1: "remove_from_clients" holding a name with a * prefix is refused by --target nginx: nginx takes each header by name; write each name in full`}},
		// Neither refusal fires on a name that only holds one of the framing names inside it.
		{name: "names in full that nginx does not frame the request by", spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, "remove_from_clients: [x-client-id, x-host]"), want: nil},
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
		// HAProxy's refused forms, each beside the form of the same key it writes.
		{name: "HAProxy: an address naming a host", spec: Proxyfile, target: haproxyTarget, document: m.Replaced(3, 3, "  app: {http: 3000, address: app.internal}"),
			want: []string{`g.yaml:3:21: "services.<service name>.address" a DNS name is refused by --target haproxy: this version's HAProxy generator writes no DNS name; give the service's IP address, or render it for envoy`}},
		{name: "HAProxy: an IP address", spec: Proxyfile, target: haproxyTarget, document: m.Replaced(3, 3, "  app: {http: 3000, address: 10.0.2.21}"), want: nil},
		// The reader takes a zoned IPv6 address as an IP address (netip.ParseAddr); HAProxy loads none in a server line.
		{name: "HAProxy: a zoned IPv6 address", spec: Proxyfile, target: haproxyTarget, document: m.Replaced(3, 3, `  app: {http: 3000, address: "fe80::1%en0"}`),
			want: []string{`g.yaml:3:21: "services.<service name>.address" a zoned IPv6 address is refused by --target haproxy: HAProxy 3.2 and 3.4 load no zone in a server's address; give the service an address without one`}},
		{name: "HAProxy: a zoned IPv6 address in addresses", spec: Proxyfile, target: haproxyTarget,
			document: m.Replaced(3, 3, `  app: {http: 3000, addresses: ["10.0.2.21", "fe80::1%en0"]}`),
			want:     []string{`g.yaml:3:21: "services.<service name>.addresses" holding a zoned IPv6 address is refused by --target haproxy: HAProxy 3.2 and 3.4 load no zone in a server's address; give the service an address without one`}},
		{name: "HAProxy: an IPv6 address with no zone", spec: Proxyfile, target: haproxyTarget,
			document: m.Replaced(3, 3, `  app: {http: 3000, addresses: ["10.0.2.21", "fe80::1"]}`), want: nil},
		{name: "HAProxy: a whole-request time", spec: Proxyfile, target: haproxyTarget, document: m.Inserted(7, "timeouts: {request: 30s}"),
			want: []string{`g.yaml:7:12: "timeouts.request" a time is refused by --target haproxy: HAProxy has no limit on a whole request (timeout server is the time without data, timeouts.idle); write timeouts.request: none`}},
		{name: "HAProxy: no whole-request limit", spec: Proxyfile, target: haproxyTarget, document: m.Inserted(7, "timeouts: {request: none}"), want: nil},
	}
	// A name nginx frames or routes the request by, one row each. A name in capitals never reaches the declaration: the
	// format refuses it first, as remove_from_clients takes lower-case names.
	for _, name := range []string{"host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "keep-alive", "http2-settings"} {
		cases = append(cases, refusalCase{name: "a name nginx frames the request by: " + name, spec: Proxyfile, target: nginxTarget,
			document: m.Inserted(7, "remove_from_clients: ["+name+"]"),
			want:     []string{`g.yaml:7:1: "remove_from_clients" holding host, content-length, transfer-encoding, connection, upgrade, te, keep-alive or http2-settings is refused by --target nginx: nginx reads these headers itself to frame and route the request; remove the name from remove_from_clients`}})
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

// Render counts the ways a target is less strict than the Envoy output after it writes, and names the page and the
// heading that list them; the Envoy output is the measure of the others, so it has none and render prints nothing.
func TestLessStrictWarning(t *testing.T) {
	for _, c := range []struct{ target, want string }{
		{"envoy", ""},
		{"nginx", `this nginx configuration is less strict than the Envoy output in 5 ways; docs/nginx.md lists them under "Where nginx is less strict than the Envoy output"`},
		{"haproxy", `this HAProxy configuration is less strict than the Envoy output in 10 ways; docs/haproxy.md lists them under "Where HAProxy is less strict than the Envoy output"`},
	} {
		target, _ := TargetNamed(c.target)
		if got := LessStrictWarning(target); got != c.want {
			t.Errorf("LessStrictWarning(%s) = %q, want %q", c.target, got, c.want)
		}
	}
}

// The page's list gives each less strict way by its name, then what was measured, in the declaration's order.
func TestLessStrictList(t *testing.T) {
	target := Target{Name: "lean", Title: "Lean", LessStrict: []LessStrict{{Row: "a", Name: "one", Measured: "`x` served, where Envoy answers `400`."},
		{Row: "b", Name: "two", Measured: "y."}}}
	want := "- **one**: `x` served, where Envoy answers `400`.\n- **two**: y.\n"
	if got := string(LessStrictList(target)); got != want {
		t.Errorf("LessStrictList = %q, want %q", got, want)
	}
}

// HAProxy and nginx name the ways their pages list, each on the row the support table gives it.
func TestLessStrictWays(t *testing.T) {
	const line = "the request line and header names read before a route is picked"
	type way struct{ row, name string }
	for _, c := range []struct {
		target string
		want   []way
	}{
		{"haproxy", []way{
			{line, "an HTTP/2 :authority with a default or empty port reaches the service without it"},
			{line, "ten Connection tokens close or keep-alive are served"},
			{line, "a request with no body reaches the service without content-length: 0"},
			{line, "two Host lines of one host are served"},
			{line, "two Content-Length lines of one value are served"},
			{line, "Content-Length beside Transfer-Encoding: chunked is served"},
			{line, "over HTTP/2, two host fields and no :authority are served"},
			{line, "a WebSocket upgrade a service answers without switching passes the service's body"},
			{line, "a header or trailer value holding a control byte is served"},
			{"timeouts.request", "without timeouts.request, an answer that keeps arriving runs past 15 s"},
		}},
		{"nginx", []way{
			{line, "a request trailer whose name is outside the token characters or whose value holds a control byte is served over HTTP/1.1"},
			{line, "a header line with no colon reaches the service as a header with an empty value"},
			{line, "a request with no body reaches the service without content-length: 0"},
			{line, "a WebSocket upgrade a service answers without switching passes the service's body"},
			{"timeouts.request", "without timeouts.request, an answer that keeps arriving runs past 15 s"},
		}},
	} {
		target, _ := TargetNamed(c.target)
		var got []way
		for _, w := range target.LessStrict {
			got = append(got, way{w.Row, w.Name})
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s's less strict ways = %q\nwant %q", c.target, got, c.want)
		}
	}
}

// HAProxy's declaration writes the rows below, each as the format describes it or with the difference its note names,
// and refuses every other row by name: a key this version's generator does not write yet says so, and a key HAProxy
// cannot carry says what to write instead. The words are the ones the support table and a refused render print.
func TestHAProxyWrites(t *testing.T) {
	type declared struct {
		key   string
		level Level
		// note is the declaration's Note, and refuses each refused form of a partial key as "form: instead".
		note, refuses string
	}
	var written []declared
	refused := map[string][]string{}
	for _, key := range MatrixRows() {
		support := haproxyTarget.Keys[key]
		if support.Level == Refused {
			refused[support.Note] = append(refused[support.Note], key)
			continue
		}
		var forms []string
		for _, refusal := range support.Refuses {
			forms = append(forms, refusal.Form+": "+refusal.Instead)
		}
		written = append(written, declared{key: key, level: support.Level, note: support.Note, refuses: strings.Join(forms, "; ")})
	}
	wantWritten := []declared{
		{key: "listen", level: Supported},
		{key: "admin", level: Partial, note: "/ready answers 200 while HAProxy runs, not when the server is ready; no other admin path in this version"},
		{key: "tls", level: Partial, note: "of the TLS 1.2 suites both take, HAProxy picks by its own order, where Envoy takes the client's among suites it holds equal: a client that offers OpenSSL's default list is given ECDHE-RSA-AES128-GCM-SHA256, where Envoy gives ECDHE-RSA-CHACHA20-POLY1305"},
		{key: "tls.certificate", level: Supported},
		{key: "tls.key", level: Supported},
		{key: "tls.clients", level: Supported},
		{key: "tls.clients.ca", level: Partial, note: "a client certificate that is not valid yet is refused with the alert bad_certificate, where Envoy sends certificate_expired"},
		{key: "client_address", level: Supported},
		{key: "timeouts", level: Supported},
		{key: "timeouts.request", level: Partial,
			note:    "without the key a service that sends nothing for 15 s is answered 504, as by Envoy; HAProxy's limits count the time without data",
			refuses: "a time: HAProxy has no limit on a whole request (timeout server is the time without data, timeouts.idle); write timeouts.request: none"},
		{key: "services", level: Supported},
		{key: "services.<service name>", level: Supported},
		{key: "services.<service name>.http", level: Supported},
		{key: "services.<service name>.grpc", level: Supported},
		{key: "services.<service name>.host_header", level: Supported},
		{key: "services.<service name>.address", level: Partial, note: "an IP address with no zone",
			refuses: "a DNS name: this version's HAProxy generator writes no DNS name; give the service's IP address, or render it for envoy; " +
				"a zoned IPv6 address: HAProxy 3.2 and 3.4 load no zone in a server's address; give the service an address without one"},
		{key: "services.<service name>.addresses", level: Partial, note: "IP addresses with no zone",
			refuses: "holding a zoned IPv6 address: HAProxy 3.2 and 3.4 load no zone in a server's address; give the service an address without one"},
		{key: "remove_from_clients", level: Supported},
		{key: "cors", level: Supported},
		{key: "cors.allow_methods", level: Supported},
		{key: "cors.allow_headers", level: Partial, note: "an empty Access-Control-Allow-Headers is left out of a preflight's answer, where Envoy sends it empty"},
		{key: "cors.expose_headers", level: Supported},
		{key: "cors.max_age", level: Supported},
		{key: "origins", level: Supported},
		{key: "origins.<environment name>", level: Supported},
		{key: "origins.<environment name>.hosts", level: Supported},
		{key: "origins.<environment name>.origins", level: Supported},
		{key: "origins.<environment name>.patterns", level: Supported},
		{key: "routes", level: Supported},
		{key: "routes[].rpc", level: Supported},
		{key: "routes[].rpc.to", level: Supported},
		{key: "routes[].rpc.to.<package pattern>", level: Supported},
		{key: "routes[].rpc.to.<package pattern>.service", level: Supported},
		{key: "routes[].rpc.to.<package pattern>.name", level: Supported},
		{key: "routes[].name", level: Supported},
		{key: "routes[].prefix", level: Supported},
		{key: "routes[].path", level: Supported},
		{key: "routes[].pattern", level: Partial,
			note: "a request whose pattern routes take more than proxyfile.lua's budget of work, about 24 ms of CPU for one request alone and 33 ms for each of many at once, such as a path of 14,000 bytes that 80 pattern routes read to its end, is answered 400, where Envoy's RE2 always decides, in under 1 ms of CPU; the states the patterns build take up to about 1 MB on each HAProxy thread, and a flood of new paths grew HAProxy by about 48 MB with 12 threads, where it grew Envoy by about 76 MB; a pattern whose program is longer than 65,536 bytes is refused, where Envoy refuses an RE2 program of more than 100 instructions"},
		{key: "routes[].method", level: Supported},
		{key: "routes[].to", level: Supported},
		{key: "routes[].rewrite", level: Supported},
		{key: "rpc", level: Supported},
		{key: "rpc.methods", level: Supported},
		{key: "rpc.methods[].service", level: Supported},
		{key: "rpc.methods[].method", level: Supported},
		{key: "rpc.proto", level: Supported},
		{key: "rpc.proto.roots", level: Supported},
		{key: "rpc.proto.option", level: Supported},
		{key: "the request's path normalised before a route is picked", level: Supported},
		{key: "a dot segment with a ; parameter answered 400", level: Partial,
			note: "a dot segment whose ; parameter is written with dots, such as /a/..;/b, is answered 400, where Envoy 1.40 drops the parameter and resolves the segment"},
		{key: "the request line and header names read before a route is picked", level: Partial,
			note: "over HTTP/1.1 an absolute target is answered 400, where Envoy routes it by its host and sends the path alone with that host as Host, or answers 404 when no environment has the host, and OPTIONS * and CONNECT are answered 400, where Envoy answers 404; a request line with no version, HTTP/0.9's form, is answered 400, where Envoy answers 426; a Connection token other than close, keep-alive, upgrade, HTTP2-Settings and te is answered 400, where Envoy removes the header the token names and serves the request; ten Connection elements or more, an empty one counted but for an empty last one, are answered 400, where Envoy counts no empty element and serves a token with ten; an h2c upgrade is answered 400, where Envoy serves it as a plain request; a header line folded onto the next, and a header value that holds three spaces or tabs in a row, are answered 400, where Envoy serves them; a WebSocket upgrade with no Sec-WebSocket-Key is answered 400, where Envoy passes it on, or answers 403 to an origin it does not admit; more than 100 header lines are answered 431 or, from 102, 400, where Envoy answers 431, and 101 lines or more of a header Envoy joins into one line are answered 400, where Envoy serves them; two Host lines of different hosts, and a Host that holds a comma, are answered 400, where Envoy answers 404; Transfer-Encoding: chunked on two lines is answered 400, and Transfer-Encoding: gzip, chunked 422, where Envoy answers 501; a header of 20000 bytes is answered 431 and a path of 20000 bytes 414, where Envoy, under its default limit of 60 KiB, serves both; over HTTP/2 a request with 97 header fields or more is answered 400, where Envoy resets its stream, and so is one with fewer whose cookies, counted one a field, reach 97, and one with 97 fields or more of a header Envoy joins, which Envoy serves, is answered 400 or, with 120, its stream reset; a WebSocket, an extended CONNECT, is answered 400, where Envoy closes the connection; a path whose escaped form outgrows HAProxy's buffer, such as one of 7000 bytes above 0x7F, is answered 400 by HAProxy 3.4.7 and 500 by 3.2.26, where Envoy serves it; a header or trailer name outside RFC 9110's token characters resets the stream, where Envoy closes the connection; a # in :path resets the stream, where Envoy answers 400; and a request with neither :authority nor host is answered 404, where Envoy closes the connection; over HTTP/1.1 a request trailer whose name is outside those characters ends the connection with no answer when the body comes after the headers, where Envoy answers 400"},
	}
	wantRefused := map[string][]string{
		"not written by this version's HAProxy generator; render it for envoy": {
			"limits", "limits.connections", "limits.request_headers", "limits.request_headers.size", "limits.request_headers.count",
			"timeouts.idle", "timeouts.upstream_idle",
			"services.<service name>.balance", "services.<service name>.hash", "services.<service name>.hash.header", "services.<service name>.hash.client",
			"services.<service name>.health", "services.<service name>.health.path", "services.<service name>.health.grpc",
			"services.<service name>.health.interval", "services.<service name>.health.timeout", "services.<service name>.health.healthy",
			"services.<service name>.health.unhealthy",
			"dns", "dns.resolver",
			"auth", "auth.ask", "auth.timeout", "auth.enabled", "auth.when_unavailable", "auth.only_with_header", "auth.send", "auth.accept",
			"auth.send_request",
			"routes[].rpc.retries", "routes[].rpc.rate_limit", "routes[].rpc.rate_limit.requests", "routes[].rpc.rate_limit.per",
			"routes[].rpc.rate_limit.burst", "routes[].rpc.rate_limit.by",
			"routes[].auth", "routes[].retries", "routes[].rate_limit", "routes[].rate_limit.requests", "routes[].rate_limit.per",
			"routes[].rate_limit.burst", "routes[].rate_limit.by",
			"rpc.methods[].action", "rpc.methods[].reports",
			"log.status_from", "log.sample"},
		"not written by this version's HAProxy generator, and without it HAProxy logs no request, where the Envoy output logs every request; render it for envoy": {
			"log"},
		"HAProxy's jwt_verify takes one key file, with no key set, URL, issuer or audience check; verify the token in the service auth.ask names": {
			"auth.jwt", "auth.jwt.issuer", "auth.jwt.audiences", "auth.jwt.keys", "auth.jwt.ca", "auth.jwt.header", "auth.jwt.optional",
			"auth.jwt.claims", "auth.jwt.claims.<header name>"},
		"the official HAProxy images have no tracing filter (the OpenTelemetry filter is an addon built from source); remove tracing, or render it for envoy": {
			"tracing", "tracing.provider", "tracing.provider.xray", "tracing.provider.xray.agent", "tracing.provider.xray.segment",
			"tracing.provider.xray.sample", "tracing.provider.xray.sample.first", "tracing.provider.xray.sample.rate",
			"tracing.provider.xray.sample.rules", "tracing.provider.xray.sample.rules[].host", "tracing.provider.xray.sample.rules[].method",
			"tracing.provider.xray.sample.rules[].path", "tracing.provider.xray.sample.rules[].first", "tracing.provider.xray.sample.rules[].rate",
			"tracing.provider.otel", "tracing.provider.otel.endpoint", "tracing.provider.otel.service", "tracing.provider.otel.sample",
			"tracing.provider.otel.sample.rate", "tracing.tags", "tracing.tags.<tag name>", "tracing.tags.<tag name>.env",
			"tracing.tags.<tag name>.header", "tracing.tags.<tag name>.default", "tracing.path_in_url"},
	}
	if !slices.Equal(written, wantWritten) {
		t.Errorf("the rows haproxyTarget writes = %+v\nwant %+v", written, wantWritten)
	}
	if !maps.EqualFunc(refused, wantRefused, slices.Equal[[]string]) {
		t.Errorf("the rows haproxyTarget refuses, by note = %q\nwant %q", refused, wantRefused)
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
