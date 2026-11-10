package read

import (
	"cmp"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/testsupport"
)

// b is the valid proxyfile the problem rows change, one change each.
var b = testsupport.Document{
	"listen: 8000",
	"services:",
	"  web: {http: 3000}",
	"  rpc-backend: {grpc: 5000}",
	"origins: origins.json",
	"routes:",
	"  - {name: web, prefix: /, to: web}",
}

// bHOCON is b written in HOCON, which the HOCON rows change, one change each.
var bHOCON = `listen = 8000
services {
  web { http = 3000 }
  rpc-backend { grpc = 5000 }
}
origins = origins.json
routes = [
  { name = web, prefix = /, to = web }
]
`

func TestReadProblems(t *testing.T) {
	cases := []struct {
		name string
		// file is the name the document is written to, in a directory of its own; g.yaml when empty.
		file, document string
		// absent writes nothing, so the file does not exist.
		absent bool
		want   []string
	}{
		{name: "undeclared service", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: wbe}"),
			want: []string{`g.yaml:7:32: route "web": the service "wbe" is not declared`}},
		{name: "no http port", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: rpc-backend}"),
			want: []string{`g.yaml:7:32: route "web": the service "rpc-backend" has no http port`}},
		{name: "auth service without http",
			document: b.Inserted(8, "auth: {ask: rpc-backend /check, timeout: 2s, enabled: true, when_unavailable: allow}"),
			want:     []string{`g.yaml:8:13: auth: the service "rpc-backend" has no http port`}},
		{name: "rpc target without grpc",
			document: b.Replaced(7, 7, "  - rpc:", `      to: {"*": web}`, "  - {name: web, prefix: /, to: web}"),
			want:     []string{`g.yaml:8:17: rpc: the service "web" has no grpc port`}},
		{name: "same match", document: b.Inserted(8, "  - {name: web2, prefix: /, to: web}"),
			want: []string{`g.yaml:8:5: route "web2" matches what route "web" on line 7 matches`}},
		{name: "named rpc", document: b.Replaced(7, 7, "  - {name: rpc, prefix: /, to: web}"),
			want: []string{`g.yaml:7:12: a route may not be named rpc: that name marks the place of the RPC routes`}},
		{name: "two rpc entries", document: b.Replaced(7, 7,
			`  - rpc: {to: {"*": rpc-backend}}`, `  - rpc: {to: {"*": rpc-backend}}`, "  - {name: web, prefix: /, to: web}"),
			want: []string{`g.yaml:8:5: the routes have more than one rpc entry (the first is on line 7)`}},
		{name: "auth off without auth", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web, auth: off}"),
			want: []string{`g.yaml:7:43: route "web" turns auth off, but no auth is declared`}},
		{name: "rewrite with pattern", document: b.Replaced(7, 7, `  - {name: web, pattern: "^/a", to: web, rewrite: /b}`),
			want: []string{`g.yaml:7:51: route "web": rewrite works with prefix or path, not with pattern`}},
		{name: "pattern does not compile", document: b.Replaced(7, 7, `  - {name: web, pattern: "^/(a", to: web}`),
			want: []string{"g.yaml:7:26: route \"web\": the pattern does not compile: error parsing regexp: missing closing ): `^/(a`"}},
		{name: "listen equals admin", document: b.Inserted(8, "admin: 8000"),
			want: []string{`g.yaml:8:8: listen and admin are the same port 8000`}},
		{name: "address", document: b.Replaced(3, 3, "  web: {http: 3000, address: localhost}"),
			want: []string{`g.yaml:3:30: service "web": the address "localhost" is not an IP address`}},
		{name: "cors with an origins file", document: b.Inserted(6, `cors: {max_age: "600"}`),
			want: []string{`g.yaml:6:1: "cors" is written in the origins file when "origins" names a file`}},
		// The origins are read in the meaning rules' stage, so one run names the problems of both, sorted.
		{name: "origins and meaning in one stage", document: b.Replaced(5, 7, "origins:", "  live: {hosts: [b.example, a.example]}",
			"routes:", "  - {name: web, prefix: /, to: web, auth: off}"),
			want: []string{
				`g.yaml:6:17: environment "live": the hosts are not sorted, or list one twice`,
				`g.yaml:8:43: route "web" turns auth off, but no auth is declared`,
			}},
		{name: "file name", file: "g.txt", document: b.String(),
			want: []string{`g.txt: proxyfile reads a file named Proxyfile or ending in .conf, .yaml or .yml`}},
		// The name is matched exactly and by case: a file with none of the three extensions is read only when it is
		// named Proxyfile.
		{name: "file name by case", file: "proxyfile", document: bHOCON,
			want: []string{`proxyfile: proxyfile reads a file named Proxyfile or ending in .conf, .yaml or .yml`}},
		{name: "file name ending in Proxyfile", file: "myProxyfile", document: bHOCON,
			want: []string{`myProxyfile: proxyfile reads a file named Proxyfile or ending in .conf, .yaml or .yml`}},
		// A gateway with no service or no route would start and answer 404 to every request.
		{name: "no service", document: b.Replaced(2, 4, "services: {}"), want: []string{
			`g.yaml:2:1: no service is declared`,
			`g.yaml:5:32: route "web": the service "web" is not declared`,
		}},
		{name: "no route", document: b.Replaced(6, 7, "routes: []"),
			want: []string{`g.yaml:6:1: the routes list is empty`}},
		{name: "empty rpc target map", document: b.Replaced(7, 7, "  - rpc: {to: {}}", "  - {name: web, prefix: /, to: web}"),
			want: []string{`g.yaml:7:11: rpc: the to mapping is empty`}},
		{name: "service without a port", document: b.Replaced(4, 4, "  rpc-backend: {}"),
			want: []string{`g.yaml:4:3: service "rpc-backend" declares no port`}},
		{name: "name declared twice", document: b.Inserted(8, "  - {name: web, path: /x, to: web}"),
			want: []string{`g.yaml:8:12: route "web" is declared twice (the first is on line 7)`}},
		{name: "same match with another method", document: b.Inserted(8, "  - {name: web2, prefix: /, method: POST, to: web}")},
		{name: "auth service undeclared",
			document: b.Inserted(8, "auth: {ask: wbe /check, timeout: 2s, enabled: true, when_unavailable: allow}"),
			want:     []string{`g.yaml:8:13: auth: the service "wbe" is not declared`}},
		{name: "rpc target undeclared",
			document: b.Replaced(7, 7, "  - rpc:", `      to: {"*": wbe}`, "  - {name: web, prefix: /, to: web}"),
			want:     []string{`g.yaml:8:17: rpc: the service "wbe" is not declared`}},
		{name: "rpc target mapping without grpc",
			document: b.Replaced(7, 7, "  - rpc:", `      to: {"*": {service: web}}`, "  - {name: web, prefix: /, to: web}"),
			want:     []string{`g.yaml:8:27: rpc: the service "web" has no grpc port`}},
		// A size or a time can match its pattern and still not fit in an int64.
		{name: "size too large", document: b.Inserted(8, "limits: {heap: 9999999999999GiB}"),
			want: []string{`g.yaml:8:16: "heap" must be a size such as 192MiB`}},
		{name: "time too large", document: b.Inserted(8, "timeouts: {request: 9999999999999h}"),
			want: []string{`g.yaml:8:21: "request" must be a time such as 125s, or none`}},
		// Decoding finds heap before the header size, which is written first, so the two come out in written order only
		// when they are sorted.
		{name: "decoding problems, in order",
			document: b.Inserted(8, "limits: {request_headers: {size: 9999999999999GiB, count: 1}, heap: 9999999999999GiB}"),
			want: []string{
				`g.yaml:8:34: "size" must be a size such as 192MiB`,
				`g.yaml:8:69: "heap" must be a size such as 192MiB`,
			}},
		// Envoy refuses a header block limit above 8192 KiB. The whole numbers' limits belong to the check (TestCheck).
		{name: "header size above Envoy's limit",
			document: b.Inserted(8, "limits: {request_headers: {size: 8193KiB, count: 1}}"),
			want:     []string{`g.yaml:8:34: limits.request_headers.size must be at most 8MiB`}},
		// A time is at most the largest whole number of seconds below the limit Envoy sets on its field: 9223372035s in
		// any field, 4294967295s in auth's timeout, which Envoy holds lower. The first two rows, in two of the time
		// fields, are a thousandth of a second above that bound.
		{name: "time above its bound", document: b.Inserted(8, "timeouts: {idle: 9223372035001ms}"),
			want: []string{`g.yaml:8:18: timeouts.idle must be at most 9223372035s`}},
		{name: "upstream idle time above its bound", document: b.Inserted(8, "timeouts: {upstream_idle: 9223372035001ms}"),
			want: []string{`g.yaml:8:27: timeouts.upstream_idle must be at most 9223372035s`}},
		{name: "auth timeout above its bound",
			document: b.Inserted(8, "auth: {ask: web /check, timeout: 4294967296s, enabled: true, when_unavailable: allow}"),
			want:     []string{`g.yaml:8:34: auth.timeout must be at most 4294967295s`}},
		{name: "the largest values accepted", document: b.Replaced(5, 7, "origins: origins.json",
			"limits: {connections: 4294967295, request_headers: {size: 8MiB, count: 4294967295}}",
			"timeouts: {request: 9223372035s, idle: 9223372035s, upstream_idle: 9223372035s}",
			"auth: {ask: web /check, timeout: 4294967295s, enabled: true, when_unavailable: allow}", "routes:",
			`  - rpc: {retries: 4294967295, to: {"*": rpc-backend}}`, "  - {name: web, prefix: /, to: web, retries: 4294967295}")},
		{name: "every problem of a route, in order",
			document: b.Replaced(7, 7, `  - {name: rpc, pattern: "^/(a", to: wbe, rewrite: /b}`), want: []string{
				`g.yaml:7:12: a route may not be named rpc: that name marks the place of the RPC routes`,
				"g.yaml:7:26: route \"rpc\": the pattern does not compile: error parsing regexp: missing closing ): `^/(a`",
				`g.yaml:7:38: route "rpc": the service "wbe" is not declared`,
				`g.yaml:7:52: route "rpc": rewrite works with prefix or path, not with pattern`,
			}},
		// Each stage stops the run, so these rows hold a problem of a later stage that is not reported.
		{name: "the check stops the run", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: wbe, retries: -1}"),
			want: []string{`g.yaml:7:46: "retries" must be a whole number from 0 to 4294967295`}},
		{name: "decoding stops the run",
			document: b.Inserted(8, "auth: {ask: wbe /check, timeout: 9999999999999h, enabled: true, when_unavailable: allow}"),
			want:     []string{`g.yaml:8:34: "timeout" must be a time such as 2s`}},
		{name: "empty file", document: "", want: []string{`g.yaml: the file is empty`}},
		{name: "file missing", file: "missing.yaml", absent: true,
			want: []string{`missing.yaml: the file cannot be read: no such file or directory`}},
		{name: "yml file name", file: "g.yml", document: b.String()},
		// A HOCON value has no line, so a problem found after the syntax names the value's key path. The first three rows
		// show that the key paths reach the rules, the decoded routes and the check; the YAML text row shows that the
		// name picks the form, not the content.
		{name: "HOCON: rules", file: "Proxyfile", document: strings.Replace(bHOCON, "to = web }", "to = wbe }", 1),
			want: []string{`Proxyfile: routes[0].to: route "web": the service "wbe" is not declared`}},
		{name: "HOCON: route position", file: "g.conf",
			document: strings.Replace(bHOCON, "\n]", "\n  { name = web2, prefix = /, to = web }\n]", 1),
			want:     []string{`g.conf: routes[1]: route "web2" matches what route "web" at routes[0] matches`}},
		{name: "HOCON: two rpc entries", file: "g.conf",
			document: strings.Replace(bHOCON, "routes = [\n",
				"routes = [\n  { rpc { to { \"*\" = rpc-backend } } }\n  { rpc { to { \"*\" = rpc-backend } } }\n", 1),
			want: []string{`g.conf: routes[1]: the routes have more than one rpc entry (the first is at routes[0])`}},
		{name: "HOCON: format check", file: "g.conf", document: strings.Replace(bHOCON, "listen = 8000", "liste = 8000", 1),
			want: []string{
				`g.conf: "listen" is required here`,
				`g.conf: liste: unknown key "liste"; known here: admin, auth, cors, limits, listen, log, namespace, origins, remove_from_clients, routes, rpc, services, timeouts, tracing`,
			}},
		{name: "HOCON: YAML text", file: "g.conf", document: b.String(),
			want: []string{`g.conf:3:6: unexpected token 11`}},
		// An unquoted dot in a HOCON key starts a nested key, so this RPC pattern becomes three keys deep, not one.
		{name: "HOCON: dotted rpc key", file: "g.conf",
			document: strings.Replace(bHOCON, "routes = [\n", "routes = [\n  { rpc { to { example.v1.x = rpc-backend } } }\n", 1),
			want: []string{
				`g.conf: routes[0].rpc.to.example: "service" is required here`,
				`g.conf: routes[0].rpc.to.example.v1: unknown key "v1"; known here: name, service`,
			}},
		{name: "HOCON: route names twice", file: "g.conf",
			document: strings.Replace(bHOCON, "\n]", "\n  { name = web, path = /a, to = web }\n]", 1),
			want:     []string{`g.conf: routes[1].name: route "web" is declared twice (the first is at routes[0].name)`}},
		// The origins written in the proxyfile follow the origins file's rules, and cors the check's rules.
		{name: "HOCON: origins inline", file: "g.conf",
			document: strings.Replace(bHOCON, "origins = origins.json", "origins { live { hosts = [b.example, a.example] } }", 1),
			want:     []string{`g.conf: origins.live.hosts: environment "live": the hosts are not sorted, or list one twice`}},
		{name: "HOCON: max_age as a number", file: "g.conf", document: strings.Replace(bHOCON, "origins = origins.json",
			"cors { max_age = 600 }\norigins { live { hosts = [a.example] } }", 1),
			want: []string{`g.conf: cors.max_age: "max_age" must be text; quote it`}},
		{name: "HOCON: file missing", file: "missing.conf", absent: true,
			want: []string{`missing.conf: the file cannot be read: no such file or directory`}},
		// The check's pattern for agent takes digits and dots and a port of up to five digits; the reader refuses an
		// octet above 255 and a port above 65535.
		{name: "agent above 255", document: b.Inserted(8, "tracing: {provider: {xray: {segment: s, agent: 300.1.2.3:2000}}}"),
			want: []string{`g.yaml:8:48: "agent" must be an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000`}},
		{name: "agent port above 65535", document: b.Inserted(8, "tracing: {provider: {xray: {segment: s, agent: 127.0.0.1:65536}}}"),
			want: []string{`g.yaml:8:48: "agent" must be an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000`}},
		// HOCON reads an unquoted colon as a separator, so the agent is written in quotes there.
		{name: "HOCON: agent unquoted", file: "g.conf",
			document: bHOCON + "tracing { provider { xray { segment = s, agent = 127.0.0.1:2000 } } }\n",
			want:     []string{`g.conf:10:59: unexpected token 11`}},
		// rate = 5% is a HOCON string, which the percentage rule reads; rate = 5 is a number, which it refuses.
		{name: "HOCON: rate as a number", file: "g.conf",
			document: bHOCON + "tracing { provider { xray { segment = s, sample { rate = 5 } } } }\n",
			want:     []string{`g.conf: tracing.provider.xray.sample.rate: "rate" must be a whole percentage such as 5%`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, cmp.Or(c.file, "g.yaml"))
			if !c.absent {
				testsupport.WriteFile(t, path, c.document)
			}
			file, problems := Proxyfile(path)
			got := testsupport.ProblemStringsIn(dir, problems)
			if !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
			if gotModel, wantModel := file != nil, len(c.want) == 0; gotModel != wantModel {
				t.Errorf("model returned = %t, want %t", gotModel, wantModel)
			}
		})
	}
}

type check struct {
	name      string
	got, want any
}

func runChecks(t *testing.T, checks []check) {
	t.Helper()
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
}

func readExample(t *testing.T, name string) *File {
	t.Helper()
	file, problems := Proxyfile("../../examples/" + name + "/gateway.yaml")
	if problems != nil {
		t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	return file
}

func ports(g *model.Gateway) []string {
	var lines []string
	for _, s := range g.Services {
		for _, p := range s.Ports {
			lines = append(lines, fmt.Sprintf("%s %s %d", s.Name, p.Protocol, p.Number))
		}
	}
	return lines
}

func addresses(g *model.Gateway) []string {
	var lines []string
	for _, s := range g.Services {
		lines = append(lines, strings.TrimSpace(s.Name+" "+s.Address.Value+" "+s.HostHeader))
	}
	return lines
}

func routes(g *model.Gateway) []string {
	var lines []string
	for _, r := range g.Routes {
		if r.RPC != nil {
			lines = append(lines, "rpc")
		} else {
			lines = append(lines, fmt.Sprintf("%s %s", r.Name.Value, r.Match))
		}
	}
	return lines
}

// routeKeys writes every other key of the plain routes: the match value, the method, the service, the rewrite,
// whether auth is off and the retries.
func routeKeys(g *model.Gateway) []string {
	var lines []string
	for _, r := range g.Routes {
		if r.RPC != nil {
			lines = append(lines, fmt.Sprintf("rpc retries %d", r.RPC.Retries))
			continue
		}
		rewrite := ""
		if r.Rewrite != nil {
			rewrite = r.Rewrite.Value
		}
		lines = append(lines, fmt.Sprintf("%s %q %q to %s rewrite %q auth off %t retries %d",
			r.Name.Value, r.Value.Value, r.Method, r.To.Value, rewrite, r.AuthOff != nil, r.Retries))
	}
	return lines
}

func targets(g *model.Gateway) []string {
	var lines []string
	for _, target := range g.RPCEntry().RPC.Targets {
		lines = append(lines, fmt.Sprintf("%s %s %s", target.Pattern, target.Service.Value, target.RouteName))
	}
	return lines
}

// headers writes a prefix as its name followed by " prefix", so the test sees that the * was taken off.
func headers(hs []model.Header) []string {
	var lines []string
	for _, h := range hs {
		if h.Prefix {
			lines = append(lines, h.Name+" prefix")
		} else {
			lines = append(lines, h.Name)
		}
	}
	return lines
}

// millis writes a time that was not written as -1, so it differs from none (0).
func millis(ts ...*model.Time) []int64 {
	var values []int64
	for _, t := range ts {
		if t == nil {
			values = append(values, -1)
		} else {
			values = append(values, t.Millis)
		}
	}
	return values
}

func TestReadFullExample(t *testing.T) {
	file := readExample(t, "full")
	g := file.Gateway
	if file.Origins == nil {
		t.Fatal("Origins = nil, want the origins the example writes")
	}
	if file.MethodsKey == nil {
		t.Fatal("MethodsKey = nil, want the methods key the example writes")
	}
	var environments []string
	for _, environment := range file.Origins.Environments {
		environments = append(environments, environment.Name)
	}
	runChecks(t, []check{
		{"namespace", g.Namespace, "example.gateway"},
		{"listen", g.Listen.Number, int64(10000)},
		{"admin", g.Admin.Number, int64(9901)},
		{"heap", g.Limits.Heap.Bytes, int64(268435456)},
		{"connections", g.Limits.Connections.Number, int64(4096)},
		{"request header size", g.Limits.RequestHeaders.Size.Bytes, int64(98304)},
		{"request header count", g.Limits.RequestHeaders.Count.Number, int64(150)},
		{"timeouts", millis(g.Timeouts.Request, g.Timeouts.Idle, g.Timeouts.UpstreamIdle), []int64{0, 90000, 45000}},
		{"ports", ports(g), []string{"shop http 8080", "shop grpc 8081", "accounts grpc 7070", "accounts http 7070"}},
		{"addresses", addresses(g), []string{"shop 127.0.0.1", "accounts 127.0.0.1 127.0.0.1:7070"}},
		{"auth service", g.Auth.Service.Value + " " + g.Auth.Path, "accounts /auth/check"},
		{"auth timeout", g.Auth.Timeout.Millis, int64(1500)},
		{"auth allow and enabled", g.Auth.Allow && g.Auth.Enabled, true},
		{"only with header", g.Auth.OnlyWithHeader, "authorization"},
		{"send", headers(g.Auth.Send), []string{"authorization", "x-country"}},
		{"accept", headers(g.Auth.Accept), []string{"x-user- prefix", "x-tier- prefix"}},
		{"remove from clients", headers(g.RemoveFromClients),
			[]string{"x-user- prefix", "x-client- prefix", "x-tier- prefix", "x-gateway- prefix"}},
		{"origins", g.Origins.Value, ""},
		{"environments", strings.Join(environments, ","), "live,test,local"},
		{"routes", routes(g), []string{"accounts-events pattern", "rpc", "accounts-webhooks path", "shop prefix"}},
		{"route keys", routeKeys(g), []string{
			`accounts-events "^/events/[a-z0-9-]+$" "" to accounts rewrite "" auth off true retries 0`,
			"rpc retries 1",
			`accounts-webhooks "/api/webhooks" "POST" to accounts rewrite "/hooks/payments" auth off true retries 0`,
			`shop "/" "" to shop rewrite "" auth off false retries 1`,
		}},
		{"targets", targets(g), []string{"example.shop.* shop shop", "* accounts accounts-rpc"}},
		{"log", fmt.Sprint(g.Log.StatusFrom, g.Log.SamplePercent), "400 1"},
		{"services key", g.ServicesPos, model.Pos{File: "../../examples/full/gateway.yaml", Line: 14, Column: 1}},
		{"routes key", g.RoutesPos, model.Pos{File: "../../examples/full/gateway.yaml", Line: 44, Column: 1}},
		{"methods", len(file.Methods), 3},
		{"methods key", *file.MethodsKey, model.Pos{File: "../../examples/full/gateway.yaml", Line: 54, Column: 3}},
	})
}

// The minimal and partial examples hold the other side of each optional key: the defaults, a check that is off and
// denies, and addresses that are not the default.
func TestReadOtherExamples(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		g := readExample(t, "minimal").Gateway
		runChecks(t, []check{
			{"namespace", g.Namespace, "proxyfile"},
			{"admin", g.Admin, (*model.Port)(nil)},
			{"limits", g.Limits, model.Limits{}},
			{"timeouts", g.Timeouts, model.Timeouts{}},
			{"auth", g.Auth, (*model.Auth)(nil)},
			{"remove from clients", headers(g.RemoveFromClients), []string(nil)},
			{"log", g.Log, (*model.Log)(nil)},
			{"addresses", addresses(g), []string{"app 127.0.0.1"}},
			{"routes", routes(g), []string{"app prefix"}},
			{"rpc entry", g.RPCEntry(), (*model.Route)(nil)},
		})
	})
	t.Run("partial", func(t *testing.T) {
		g := readExample(t, "partial").Gateway
		runChecks(t, []check{
			{"namespace", g.Namespace, "proxyfile"},
			{"admin", g.Admin, (*model.Port)(nil)},
			{"heap", g.Limits.Heap.Bytes, int64(67108864)},
			{"connections and request headers", g.Limits.Connections == nil && g.Limits.RequestHeaders == nil, true},
			{"timeouts", millis(g.Timeouts.Request, g.Timeouts.Idle, g.Timeouts.UpstreamIdle), []int64{30000, -1, -1}},
			{"addresses", addresses(g), []string{"web 10.0.0.5", "identity ::1", "backend 127.0.0.1"}},
			{"auth allow or enabled", g.Auth.Allow || g.Auth.Enabled, false},
			{"auth timeout", g.Auth.Timeout.Millis, int64(250)},
			{"only with header", g.Auth.OnlyWithHeader, ""},
			{"send and accept", slices.Concat(headers(g.Auth.Send), headers(g.Auth.Accept)), []string{"authorization", "x-identity"}},
			{"routes", routes(g), []string{"rpc", "login path", "web prefix"}},
			{"targets", targets(g), []string{"* backend backend-rpc"}},
			{"log", fmt.Sprint(g.Log.StatusFrom, g.Log.SamplePercent), "500 0"},
		})
	})
}

// The reader fills the defaults the design names and keeps the written order of the tags, which here is not their
// sorted order, in both forms; both forms of the tracing example give one model.
func TestReadTracing(t *testing.T) {
	every := &model.Tracing{
		XRay: model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "example-gateway", Sample: &model.Sample{
			Default: model.SampleRule{First: 1, RatePercent: 5},
			Rules:   []model.SampleRule{{Host: "*", Method: "*", Path: "/live*", First: 0, RatePercent: 0}},
		}},
		Tags: []model.Tag{
			{Name: "environment", Env: "APP_ENVIRONMENT", Default: "local"},
			{Name: "request_id", Header: "x-request-id"},
		},
	}
	cases := []struct {
		// file names the document's file in a directory of its own, g.yaml when empty; a path with a / is an example
		// that is read as it is.
		name, file, document string
		want                 *model.Tracing
	}{
		{name: "no tracing", document: b.String()},
		{name: "the fewest keys", document: b.Inserted(8, "tracing: {provider: {xray: {segment: s}}}"),
			want: &model.Tracing{XRay: model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "s"}}},
		{name: "the sample's defaults, a rule's patterns and the tags' order",
			document: b.Inserted(8, "tracing: {provider: {xray: {segment: s, agent: 10.0.0.9:2001, sample: {rules: [{first: 3, rate: 50%}]}}}, "+
				"tags: {zone: {env: ZONE}, area: {header: x-area, default: none}}, path_in_url: true}"),
			want: &model.Tracing{XRay: model.XRay{Address: "10.0.0.9", Port: 2001, Segment: "s", Sample: &model.Sample{
				Default: model.SampleRule{First: 1, RatePercent: 5},
				Rules:   []model.SampleRule{{Host: "*", Method: "*", Path: "*", First: 3, RatePercent: 50}},
			}}, Tags: []model.Tag{{Name: "zone", Env: "ZONE"}, {Name: "area", Header: "x-area", Default: "none"}}, PathInURL: true}},
		{name: "the tags' order, HOCON", file: "g.conf",
			document: bHOCON + "tracing { provider { xray { segment = s } }, tags { zone { env = ZONE }, area { header = x-area } } }\n",
			want: &model.Tracing{XRay: model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "s"},
				Tags: []model.Tag{{Name: "zone", Env: "ZONE"}, {Name: "area", Header: "x-area"}}}},
		{name: "every key, YAML", file: "../../examples/tracing/gateway.yaml", want: every},
		{name: "every key, HOCON", file: "../../examples/tracing/gateway.conf", want: every},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := c.file
			if !strings.Contains(c.file, "/") {
				path = filepath.Join(t.TempDir(), cmp.Or(c.file, "g.yaml"))
				testsupport.WriteFile(t, path, c.document)
			}
			file, problems := Proxyfile(path)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if !reflect.DeepEqual(file.Gateway.Tracing, c.want) {
				t.Errorf("Tracing = %#v, want %#v", file.Gateway.Tracing, c.want)
			}
		})
	}
}
