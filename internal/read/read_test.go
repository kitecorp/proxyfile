package read

import (
	"cmp"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
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
		{name: "address neither an IP address nor a DNS name", document: b.Replaced(3, 3, `  web: {http: 3000, address: "web_internal"}`),
			want: []string{`g.yaml:3:30: service "web": the address "web_internal" is not an IP address or a DNS name`}},
		{name: "address with a port", document: b.Replaced(3, 3, `  web: {http: 3000, address: "localhost:3000"}`),
			want: []string{`g.yaml:3:30: service "web": the address "localhost:3000" has a port; the port goes in http or grpc`}},
		{name: "addresses with a port", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["10.0.0.2:3000", "[fd00::1]:3000"]}`),
			want: []string{`g.yaml:3:33: service "web": "10.0.0.2:3000" in addresses has a port; the port goes in http or grpc`,
				`g.yaml:3:50: service "web": "[fd00::1]:3000" in addresses has a port; the port goes in http or grpc`}},
		{name: "address in the dotted form, not an IP address", document: b.Replaced(3, 3, `  web: {http: 3000, address: "10.0.0.256"}`),
			want: []string{`g.yaml:3:30: service "web": the address "10.0.0.256" is not an IP address or a DNS name`}},
		{name: "address and addresses", document: b.Replaced(3, 3, `  web: {http: 3000, address: "10.0.0.1", addresses: ["10.0.0.2", "10.0.0.3"]}`),
			want: []string{`g.yaml:3:42: service "web": write address or addresses, not both`}},
		{name: "addresses with one address", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["10.0.0.2"]}`),
			want: []string{`g.yaml:3:32: service "web": addresses needs two or more addresses; write one in address`}},
		{name: "a name in addresses", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["10.0.0.2", web.internal]}`),
			want: []string{`g.yaml:3:45: service "web": "web.internal" in addresses is not an IP address; a DNS name goes in address`}},
		{name: "an address twice", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["10.0.0.2", "10.0.0.3", "10.0.0.2"]}`),
			want: []string{`g.yaml:3:57: service "web": the address "10.0.0.2" is written twice in addresses (the first on line 3)`}},
		{name: "an IPv6 address twice", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["fd00::1", "fd00:0::1"]}`),
			want: []string{`g.yaml:3:44: service "web": the address "fd00:0::1" is written twice in addresses (the first on line 3)`}},
		{name: "an address twice, once as an IPv4-mapped IPv6 address", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["10.0.0.2", "::ffff:10.0.0.2"]}`),
			want: []string{`g.yaml:3:45: service "web": the address "::ffff:10.0.0.2" is written twice in addresses (the first on line 3)`}},
		{name: "resolver not an IP address", document: b.Inserted(8, "dns: {resolver: ns.example}"),
			want: []string{`g.yaml:8:17: "resolver" must be an IP address with an optional port, such as 10.0.0.2:53`}},
		{name: "balance hash without hash", document: b.Replaced(3, 3, `  web: {http: 3000, balance: hash}`),
			want: []string{`g.yaml:3:30: service "web": balance hash needs hash`}},
		{name: "hash without balance hash", document: b.Replaced(3, 3, `  web: {http: 3000, hash: {header: x-user}}`),
			want: []string{`g.yaml:3:21: service "web": hash is written, but balance is not hash`}},
		{name: "health with neither path nor grpc", document: b.Replaced(3, 3, "  web: {http: 3000, health: {interval: 10s}}"),
			want: []string{`g.yaml:3:21: service "web": health needs path or grpc`}},
		{name: "health path without an http port", document: b.Replaced(4, 4, "  rpc-backend: {grpc: 5000, health: {path: /healthz}}"),
			want: []string{`g.yaml:4:44: service "rpc-backend": health.path needs an http port`}},
		{name: "health grpc without a grpc port", document: b.Replaced(3, 3, "  web: {http: 3000, health: {grpc: true}}"),
			want: []string{`g.yaml:3:36: service "web": health.grpc needs a grpc port`}},
		{name: "health timeout as long as the interval", document: b.Replaced(3, 3, "  web: {http: 3000, health: {path: /healthz, interval: 2s, timeout: 2s}}"),
			want: []string{`g.yaml:3:69: service "web": health.timeout 2s must be shorter than health.interval 2s`}},
		// The default timeout, 2s, against a written interval: the message points at the interval.
		{name: "health interval below the default timeout", document: b.Replaced(3, 3, "  web: {http: 3000, health: {path: /healthz, interval: 1s}}"),
			want: []string{`g.yaml:3:56: service "web": health.timeout 2s must be shorter than health.interval 1s`}},
		{name: "health interval too large", document: b.Replaced(3, 3, "  web: {http: 3000, health: {path: /healthz, interval: 9999999999999h}}"),
			want: []string{`g.yaml:3:56: "interval" must be a time such as 5s`}},
		{name: "health interval below 1s", document: b.Replaced(3, 3, "  web: {http: 3000, health: {path: /healthz, interval: 500ms, timeout: 100ms}}"),
			want: []string{`g.yaml:3:56: health.interval must be at least 1s`}},
		// Envoy's smallest fill interval is 50ms; nginx writes a rate per second or per minute.
		{name: "rate limit period below 1s", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web, rate_limit: {requests: 10, per: 500ms}}"),
			want: []string{`g.yaml:7:69: rate_limit.per must be at least 1s`}},
		{name: "rate limit period below 1s on the rpc entry",
			document: b.Replaced(7, 7, `  - rpc: {to: {"*": rpc-backend}, rate_limit: {requests: 10, per: 999ms}}`, "  - {name: web, prefix: /, to: web}"),
			want:     []string{`g.yaml:7:67: rate_limit.per must be at least 1s`}},
		// The bucket holds requests + burst tokens, a uint32 in Envoy.
		{name: "requests and burst above Envoy's largest",
			document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web, rate_limit: {requests: 4294967295, burst: 1, per: 1s}}"),
			want:     []string{`g.yaml:7:79: rate_limit: requests and burst together must be at most 4294967295`}},
		{name: "rate limit period too large", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web, rate_limit: {requests: 10, per: 9999999999999h}}"),
			want: []string{`g.yaml:7:69: "per" must be a time such as 1s or 1m`}},
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
		{name: "size too large", document: b.Inserted(8, "envoy: {heap: 9999999999999GiB}"),
			want: []string{`g.yaml:8:15: "heap" must be a size such as 192MiB`}},
		{name: "time too large", document: b.Inserted(8, "timeouts: {request: 9999999999999h}"),
			want: []string{`g.yaml:8:21: "request" must be a time such as 125s, or none`}},
		// Decoding finds the header size before heap, which is written first, so the two come out in written order only when
		// they are sorted.
		{name: "decoding problems, in order",
			document: b.Inserted(8, "envoy: {heap: 9999999999999GiB}\nlimits: {request_headers: {size: 9999999999999GiB, count: 1}}"),
			want: []string{
				`g.yaml:8:15: "heap" must be a size such as 192MiB`,
				`g.yaml:9:34: "size" must be a size such as 96KiB`,
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
				`g.conf: liste: unknown key "liste"; known here: admin, auth, client_address, cors, dns, envoy, haproxy, limits, listen, log, nginx, origins, remove_from_clients, routes, rpc, services, timeouts, tls, tracing`,
			}},
		// A 1.x file is refused at every key that moved into the envoy block, each named by its key path, in one run. A
		// 1.x tracing is read as 2.1's tracing key, so it is refused no more.
		{name: "HOCON: every 1.x key at its 1.x place", file: "g.conf",
			document: "namespace = example.gateway\nlimits { heap = 64MiB }\n" + bHOCON + "tracing { provider { xray { segment = s } } }\n",
			want: []string{
				`g.conf: limits.heap: "heap" moved to envoy.heap in proxyfile 2.0`,
				`g.conf: namespace: "namespace" moved to envoy.namespace in proxyfile 2.0`,
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
		{name: "roots empty", document: b.Inserted(8, "rpc: {proto: {roots: [], option: o.v1.route}}"),
			want: []string{`g.yaml:8:15: the roots list is empty`}},
		{name: "root written twice", document: b.Inserted(8, "rpc: {proto: {roots: [proto, ../x, proto, proto], option: o.v1.route}}"),
			want: []string{
				`g.yaml:8:36: the root "proto" is written twice (the first on line 8)`,
				`g.yaml:8:43: the root "proto" is written twice (the first on line 8)`,
			}},
		// The format check reads the tree, so the HOCON form gets the YAML form's choice rule.
		{name: "HOCON: rpc with methods and proto", file: "g.conf", document: bHOCON + "rpc { methods = [], proto { roots = [proto], option = o.v1.route } }\n",
			want: []string{`g.conf: rpc: exactly one of methods, proto is needed here`}},
		{name: "HOCON: root written twice", file: "g.conf", document: bHOCON + "rpc { proto { roots = [proto, proto, proto], option = o.v1.route } }\n",
			want: []string{
				`g.conf: rpc.proto.roots[1]: the root "proto" is written twice (the first at rpc.proto.roots[0])`,
				`g.conf: rpc.proto.roots[2]: the root "proto" is written twice (the first at rpc.proto.roots[0])`,
			}},
		{name: "tracing in both places", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web}",
			"tracing: {provider: {xray: {segment: s}}}", "envoy: {tracing: {provider: {xray: {segment: s}}}}"),
			want: []string{`g.yaml:9:9: write tracing or envoy.tracing, not both`}},
		// The check's pattern for agent takes digits and dots and a port of up to five digits; the reader refuses an
		// octet above 255 and a port above 65535.
		{name: "agent above 255", document: b.Inserted(8, "envoy: {tracing: {provider: {xray: {segment: s, agent: 300.1.2.3:2000}}}}"),
			want: []string{`g.yaml:8:56: "agent" must be an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000`}},
		{name: "agent port above 65535", document: b.Inserted(8, "envoy: {tracing: {provider: {xray: {segment: s, agent: 127.0.0.1:65536}}}}"),
			want: []string{`g.yaml:8:56: "agent" must be an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000`}},
		// The check's pattern for endpoint takes any text without a space; the reader refuses one that is not an IP
		// address or a DNS name, and a port.
		{name: "otel endpoint without a port", document: b.Inserted(8, "tracing: {provider: {otel: {endpoint: collector, service: s}}}"),
			want: []string{`g.yaml:8:39: "endpoint" must be an IP address or a DNS name, and a port, such as 127.0.0.1:4317 or [fd00::40]:4317`}},
		// auth holds ask, jwt or both; the check's keys are required with ask and refused without it.
		{name: "auth with neither ask nor jwt", document: b.Inserted(8, "auth: {}"),
			want: []string{`g.yaml:8:7: auth needs ask or jwt`}},
		{name: "ask without its three keys", document: b.Inserted(8, "auth: {ask: web /check}"), want: []string{
			`g.yaml:8:7: "enabled" is required here`, `g.yaml:8:7: "timeout" is required here`, `g.yaml:8:7: "when_unavailable" is required here`}},
		{name: "the check's keys without ask", document: b.Inserted(8, "auth: {jwt: {issuer: i, audiences: any, keys: /k.json}, timeout: 1s, send_request: true}"),
			want: []string{`g.yaml:8:57: auth: timeout is written, but ask is not`, `g.yaml:8:70: auth: send_request is written, but ask is not`}},
		{name: "jwt keys a URL without a host, and no audience", document: b.Inserted(8, `auth: {jwt: {issuer: i, audiences: [], keys: "https://"}}`),
			want: []string{`g.yaml:8:36: auth.jwt: audiences needs one audience or more; any accepts every audience`,
				`g.yaml:8:46: "keys" must be an https URL of an IP address or a DNS name, with no user part, and with a port from 1 to 65535 when it writes one`}},
		{name: "jwt ca with a keys file", document: b.Inserted(8, "auth: {jwt: {issuer: i, audiences: any, keys: /k.json, ca: /ca.pem}}"),
			want: []string{`g.yaml:8:56: auth.jwt: ca is written, but keys is not a URL`}},
		// The check's answer would overwrite the verified claim.
		{name: "a claims header accept names", document: b.Inserted(8, "auth: {ask: web /check, timeout: 1s, enabled: true, when_unavailable: deny, "+
			"accept: [x-user-*], jwt: {issuer: i, audiences: any, keys: /k.json, claims: {x-user-id: sub}}}"),
			want: []string{`g.yaml:8:154: auth.jwt.claims: "x-user-id" is a header accept names`}},
		{name: "a claims header send names", document: b.Inserted(8, "auth: {ask: web /check, timeout: 1s, enabled: true, when_unavailable: deny, "+
			"send: [x-user-id], jwt: {issuer: i, audiences: any, keys: /k.json, claims: {x-user-id: sub}}}"),
			want: []string{`g.yaml:8:153: auth.jwt.claims: "x-user-id" is a header send names`}},
		// The early header removal takes every claims header and every remove_from_clients entry from the client's
		// request before the token filter reads it, so a file whose token header is either would never see a token.
		{name: "a claims header that carries the token", document: b.Inserted(8, "auth: {jwt: {issuer: i, audiences: any, keys: /k.json, claims: {authorization: sub}}}"),
			want: []string{`g.yaml:8:65: auth.jwt.claims: "authorization" is the header that carries the token`}},
		{name: "a remove_from_clients entry that covers the token's header", document: b.Replaced(7, 7, "  - {name: web, prefix: /, to: web}",
			"auth: {jwt: {issuer: i, audiences: any, keys: /k.json, header: x-token}}", `remove_from_clients: [x-debug, "x-*"]`),
			want: []string{`g.yaml:9:32: remove_from_clients: this entry removes "x-token", the header that carries the token of auth.jwt`}},
		// A header other than authorization is read whole as the bare token, and a Cookie header holds name=value pairs.
		{name: "the token in the cookie header", document: b.Inserted(8, "auth: {jwt: {issuer: i, audiences: any, keys: /k.json, header: cookie}}"),
			want: []string{`g.yaml:8:64: auth.jwt: header cannot be cookie; a token inside a cookie is not read`}},
		// HOCON reads an unquoted colon as a separator, so the agent is written in quotes there.
		{name: "HOCON: agent unquoted", file: "g.conf",
			document: bHOCON + "envoy { tracing { provider { xray { segment = s, agent = 127.0.0.1:2000 } } } }\n",
			want:     []string{`g.conf:10:67: unexpected token 11`}},
		// rate = 5% is a HOCON string, which the percentage rule reads; rate = 5 is a number, which it refuses.
		{name: "HOCON: rate as a number", file: "g.conf",
			document: bHOCON + "envoy { tracing { provider { xray { segment = s, sample { rate = 5 } } } } }\n",
			want:     []string{`g.conf: envoy.tracing.provider.xray.sample.rate: "rate" must be a whole percentage such as 5%`}},
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
	file, problems := Proxyfile("../../examples/" + name + "/yaml/proxyfile.yaml")
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
		lines = append(lines, strings.TrimSpace(s.Name+" "+strings.Join(values(s.Addresses), ",")+" "+s.HostHeader))
	}
	return lines
}

func values(texts []model.Text) []string {
	list := make([]string, len(texts))
	for i, text := range texts {
		list[i] = text.Value
	}
	return list
}

// backends writes one line per service: its backends, whether they are found by name, how requests are spread over
// them and what is hashed.
func backends(g *model.Gateway) []string {
	var lines []string
	for _, s := range g.Services {
		kind := "static"
		if s.DNS {
			kind = "dns"
		}
		line := fmt.Sprintf("%s %s %s %s", s.Name, strings.Join(values(s.Addresses), ","), kind, s.Balance.Value)
		if s.Hash != nil && s.Hash.Client {
			line += " client"
		} else if s.Hash != nil {
			line += " header " + s.Hash.Header
		}
		lines = append(lines, line)
	}
	return lines
}

// health writes one line per service with health: what is checked, how often, how long a check may take and how many
// checks in a row a backend must pass to join the pool and fail to leave it.
func health(g *model.Gateway) []string {
	var lines []string
	for _, s := range g.Services {
		h := s.Health
		if h == nil {
			continue
		}
		var what []string
		if h.Path != nil {
			what = append(what, "path "+h.Path.Value)
		}
		if h.GRPC != nil {
			what = append(what, "grpc")
		}
		lines = append(lines, fmt.Sprintf("%s %s every %s within %s, %d to join, %d to leave", s.Name, strings.Join(what, " and "),
			model.FormatSeconds(h.Interval.Millis), model.FormatSeconds(h.Timeout.Millis), h.Healthy, h.Unhealthy))
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
		{"namespace", g.Envoy.Namespace, "example.gateway"},
		{"listen", g.Listen.Number, int64(10000)},
		{"admin", g.Admin.Number, int64(9901)},
		{"heap", g.Envoy.Heap.Bytes, int64(268435456)},
		{"connections", g.Limits.Connections.Number, int64(4096)},
		{"request header size", g.Limits.RequestHeaders.Size.Bytes, int64(98304)},
		{"request header count", g.Limits.RequestHeaders.Count.Number, int64(150)},
		{"timeouts", millis(g.Timeouts.Request, g.Timeouts.Idle, g.Timeouts.UpstreamIdle), []int64{0, 90000, 45000}},
		{"ports", ports(g), []string{"shop http 8080", "shop grpc 8081", "accounts grpc 7070", "accounts http 7070", "catalog http 8090"}},
		{"addresses", addresses(g), []string{"shop 10.0.1.11,10.0.1.12", "accounts 127.0.0.1 127.0.0.1:7070", "catalog catalog.internal.example"}},
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
		{"routes", routes(g), []string{"accounts-events pattern", "rpc", "accounts-webhooks path", "catalog prefix", "shop prefix"}},
		{"route keys", routeKeys(g), []string{
			`accounts-events "^/events/[a-z0-9-]+$" "" to accounts rewrite "" auth off true retries 0`,
			"rpc retries 1",
			`accounts-webhooks "/api/webhooks" "POST" to accounts rewrite "/hooks/payments" auth off true retries 0`,
			`catalog "/catalog/" "" to catalog rewrite "" auth off false retries 0`,
			`shop "/" "" to shop rewrite "" auth off false retries 1`,
		}},
		{"targets", targets(g), []string{"example.shop.* shop shop", "* accounts accounts-rpc"}},
		{"log", fmt.Sprint(g.Log.StatusFrom, g.Log.SamplePercent), "400 1"},
		{"services key", g.ServicesPos, model.Pos{File: "../../examples/full/yaml/proxyfile.yaml", Line: 17, Column: 1}},
		{"routes key", g.RoutesPos, model.Pos{File: "../../examples/full/yaml/proxyfile.yaml", Line: 63, Column: 1}},
		{"methods", len(file.Methods), 3},
		{"methods key", *file.MethodsKey, model.Pos{File: "../../examples/full/yaml/proxyfile.yaml", Line: 75, Column: 3}},
		{"client address", g.ClientAddress, model.ClientConnection},
		{"send request", g.Auth.SendRequest, true},
		{"jwt", *g.JWT, model.JWT{Issuer: "https://login.example.com", Audiences: []string{"api.example.com", "admin.example.com"},
			URL: "https://login.example.com/.well-known/jwks.json", Host: "login.example.com", Port: 443, DNS: true,
			CA: "/etc/gateway/jwt/ca.pem", Header: "x-access-token", Optional: true,
			Claims: []model.Claim{{Header: "x-subject", Name: "sub"}, {Header: "x-roles", Name: "realm.roles"}}}},
		{"otel", *g.Tracing.OTel, model.OTel{Host: "10.0.3.40", Port: 4317, Service: "example-gateway", RatePercent: 10}},
	})
}

// rpc.proto is read as written, in both forms, with each root at its place and the keys a problem names; the roots
// are resolved and the files read by render.
func TestReadProto(t *testing.T) {
	cases := []struct {
		name, file, document string
		want                 *Proto
	}{
		{name: "no rpc.proto", file: "g.yaml", document: b.String()},
		{name: "YAML", file: "g.yaml", document: b.Inserted(8, "rpc: {proto: {roots: [proto, /abs/x], option: o.v1.route}}"),
			want: &Proto{Key: model.Pos{File: "g.yaml", Line: 8, Column: 7}, OptionKey: model.Pos{File: "g.yaml", Line: 8, Column: 39},
				Option: "o.v1.route",
				Roots: []model.Text{{Value: "proto", Pos: model.Pos{File: "g.yaml", Line: 8, Column: 23}},
					{Value: "/abs/x", Pos: model.Pos{File: "g.yaml", Line: 8, Column: 30}}}}},
		{name: "HOCON", file: "g.conf", document: bHOCON + "rpc { proto { roots = [proto, /abs/x], option = o.v1.route } }\n",
			want: &Proto{Key: model.Pos{File: "g.conf", Path: "rpc.proto"}, OptionKey: model.Pos{File: "g.conf", Path: "rpc.proto.option"},
				Option: "o.v1.route",
				Roots: []model.Text{{Value: "proto", Pos: model.Pos{File: "g.conf", Path: "rpc.proto.roots[0]"}},
					{Value: "/abs/x", Pos: model.Pos{File: "g.conf", Path: "rpc.proto.roots[1]"}}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(dir, c.file), c.document)
			t.Chdir(dir)
			file, problems := Proxyfile(c.file)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if !reflect.DeepEqual(file.Proto, c.want) {
				t.Errorf("Proto = %+v, want %+v", file.Proto, c.want)
			}
		})
	}
}

// The minimal and partial examples hold the other side of each optional key: the defaults, a check that is off and
// denies, and addresses that are not the default.
func TestReadOtherExamples(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		g := readExample(t, "minimal").Gateway
		runChecks(t, []check{
			{"envoy", g.Envoy, model.Envoy{Namespace: "proxyfile", ConnectBridge: true}},
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
			{"namespace", g.Envoy.Namespace, "proxyfile"},
			{"admin", g.Admin, (*model.Port)(nil)},
			{"heap", g.Envoy.Heap.Bytes, int64(67108864)},
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

// The reader fills the defaults the description's Docs name and keeps the written order of the tags, which here is
// not their sorted order, in both forms; tracing at the top and at envoy.tracing, its old place, give one model; and
// otel splits its endpoint into a host and a port, says whether the host is a name, and traces 5% without sample.rate.
func TestReadTracing(t *testing.T) {
	every := &model.Tracing{
		XRay: &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "example-gateway", Sample: &model.Sample{
			Default: model.SampleRule{First: 1, RatePercent: 5},
			Rules:   []model.SampleRule{{Host: "*", Method: "*", Path: "/live*", First: 0, RatePercent: 0}},
		}},
		Tags: []model.Tag{
			{Name: "environment", Env: "APP_ENVIRONMENT", Default: "local"},
			{Name: "request_id", Header: "x-request-id"},
		},
	}
	otel := &model.Tracing{
		OTel: &model.OTel{Host: "otel-collector", Port: 4317, DNS: true, Service: "example-gateway", RatePercent: 10},
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
		{name: "the fewest keys, at the top", document: b.Inserted(8, "tracing: {provider: {xray: {segment: s}}}"),
			want: &model.Tracing{XRay: &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "s"}}},
		{name: "the fewest keys", document: b.Inserted(8, "envoy: {tracing: {provider: {xray: {segment: s}}}}"),
			want: &model.Tracing{XRay: &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "s"}}},
		{name: "the sample's defaults, a rule's patterns and the tags' order",
			document: b.Inserted(8, "envoy: {tracing: {provider: {xray: {segment: s, agent: 10.0.0.9:2001, sample: {rules: [{first: 3, rate: 50%}]}}}, "+
				"tags: {zone: {env: ZONE}, area: {header: x-area, default: none}}, path_in_url: true}}"),
			want: &model.Tracing{XRay: &model.XRay{Address: "10.0.0.9", Port: 2001, Segment: "s", Sample: &model.Sample{
				Default: model.SampleRule{First: 1, RatePercent: 5},
				Rules:   []model.SampleRule{{Host: "*", Method: "*", Path: "*", First: 3, RatePercent: 50}},
			}}, Tags: []model.Tag{{Name: "zone", Env: "ZONE"}, {Name: "area", Header: "x-area", Default: "none"}}, PathInURL: true}},
		{name: "the tags' order, HOCON", file: "g.conf",
			document: bHOCON + "envoy { tracing { provider { xray { segment = s } }, tags { zone { env = ZONE }, area { header = x-area } } } }\n",
			want: &model.Tracing{XRay: &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "s"},
				Tags: []model.Tag{{Name: "zone", Env: "ZONE"}, {Name: "area", Header: "x-area"}}}},
		{name: "every key, YAML", file: "../../examples/tracing/yaml/proxyfile.yaml", want: every},
		{name: "every key, HOCON", file: "../../examples/tracing/hocon/Proxyfile", want: every},
		{name: "every key, the old place", file: "../../examples/tracing/envoy-tracing/proxyfile.yaml", want: every},
		{name: "otel, the default rate and an IPv6 endpoint", document: b.Inserted(8, `tracing: {provider: {otel: {endpoint: "[fd00::40]:4317", service: s}}}`),
			want: &model.Tracing{OTel: &model.OTel{Host: "fd00::40", Port: 4317, Service: "s", RatePercent: 5}}},
		{name: "otel example, YAML", file: "../../examples/otel/yaml/proxyfile.yaml", want: otel},
		{name: "otel example, HOCON", file: "../../examples/otel/hocon/Proxyfile", want: otel},
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

// The reader fills auth.jwt with the defaults the description's Docs name, in both forms: the authorization header,
// the system CA bundle with a keys URL and none with a keys file, and 443 when the URL writes no port; claims keep the
// written order, a route's auth: off is read in a file without ask, and the check is there only with ask.
func TestReadJWT(t *testing.T) {
	example := &model.JWT{Issuer: "https://login.example.com", Audiences: []string{"api.example.com"},
		File: "/etc/gateway/jwt/jwks.json", Header: "authorization",
		Claims: []model.Claim{{Header: "x-user-id", Name: "sub"}, {Header: "x-user-roles", Name: "realm.roles"}}}
	cases := []struct {
		// file names an example that is read as it is; the document is read as g.yaml when it is empty.
		name, file, document string
		want                 *model.JWT
		check                bool // whether the gateway has the check, auth.ask
	}{
		{name: "no jwt", document: b.String()},
		{name: "jwt example, YAML", file: "../../examples/jwt/yaml/proxyfile.yaml", want: example},
		{name: "jwt example, HOCON", file: "../../examples/jwt/hocon/Proxyfile", want: example},
		{name: "keys at an IP address, any audience, a header and optional",
			document: b.Inserted(8, `auth: {jwt: {issuer: i, audiences: any, keys: "https://10.0.3.50:8443/jwks.json", header: x-token, optional: true}}`),
			want: &model.JWT{Issuer: "i", URL: "https://10.0.3.50:8443/jwks.json", Host: "10.0.3.50", Port: 8443,
				CA: "/etc/ssl/certs/ca-certificates.crt", Header: "x-token", Optional: true}},
		{name: "ask and jwt", document: b.Inserted(8, "auth: {ask: web /check, timeout: 1s, enabled: true, when_unavailable: deny, jwt: {issuer: i, audiences: [a], keys: /k.json}}"),
			want: &model.JWT{Issuer: "i", Audiences: []string{"a"}, File: "/k.json", Header: "authorization"}, check: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := c.file
			if path == "" {
				path = filepath.Join(t.TempDir(), "g.yaml")
				testsupport.WriteFile(t, path, c.document)
			}
			file, problems := Proxyfile(path)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if !reflect.DeepEqual(file.Gateway.JWT, c.want) {
				t.Errorf("JWT = %#v, want %#v", file.Gateway.JWT, c.want)
			}
			if got := file.Gateway.Auth != nil; got != c.check {
				t.Errorf("Auth != nil = %t, want %t", got, c.check)
			}
		})
	}
}

// The reader fills each service's backends, how requests are spread over them and the resolver, in both forms.
func TestReadBackends(t *testing.T) {
	balanced := []string{"shop 10.0.1.11,10.0.1.12,10.0.1.13 static least_requests", "orders 10.0.2.21,10.0.2.22 static random"}
	dns := []string{"catalog catalog.internal.example dns hash header x-session-id", "search search.internal.example dns hash client"}
	full := []string{"shop 10.0.1.11,10.0.1.12 static least_requests", "accounts 127.0.0.1 static round_robin", "catalog catalog.internal.example dns hash header x-session-id"}
	defaultBackend := "rpc-backend 127.0.0.1 static round_robin"
	cases := []struct {
		name, file, document string // file is an example read as it is; otherwise document is written to g.yaml
		want                 []string
		resolver             *model.Resolver
	}{
		{name: "balanced example, YAML", file: "../../examples/balanced/yaml/proxyfile.yaml", want: balanced},
		{name: "balanced example, HOCON", file: "../../examples/balanced/hocon/Proxyfile", want: balanced},
		{name: "dns example, YAML", file: "../../examples/dns/yaml/proxyfile.yaml", want: dns, resolver: &model.Resolver{Address: "10.0.0.2", Port: 53}},
		{name: "dns example, HOCON", file: "../../examples/dns/hocon/Proxyfile", want: dns, resolver: &model.Resolver{Address: "10.0.0.2", Port: 53}},
		{name: "full example, YAML", file: "../../examples/full/yaml/proxyfile.yaml", want: full, resolver: &model.Resolver{Address: "10.0.0.2", Port: 53}},
		{name: "full example, HOCON", file: "../../examples/full/hocon/Proxyfile", want: full, resolver: &model.Resolver{Address: "10.0.0.2", Port: 53}},
		// A Compose service name is a DNS name of one label.
		{name: "a name of one label", document: b.Replaced(3, 3, "  web: {http: 3000, address: catalog}"),
			want: []string{"web catalog dns round_robin", defaultBackend}},
		{name: "IPv6 backends and resolver", document: b.Replaced(3, 3, `  web: {http: 3000, addresses: ["fd00::1", "fd00::2"]}`) + "dns: {resolver: \"fd00::53\"}\n",
			want: []string{"web fd00::1,fd00::2 static round_robin", defaultBackend}, resolver: &model.Resolver{Address: "fd00::53", Port: 53}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := c.file
			if path == "" {
				path = filepath.Join(t.TempDir(), "g.yaml")
				testsupport.WriteFile(t, path, c.document)
			}
			file, problems := Proxyfile(path)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := backends(file.Gateway); !slices.Equal(got, c.want) {
				t.Errorf("backends = %q, want %q", got, c.want)
			}
			if !reflect.DeepEqual(file.Gateway.Resolver, c.resolver) {
				t.Errorf("Resolver = %#v, want %#v", file.Gateway.Resolver, c.resolver)
			}
		})
	}
}

// The reader fills each check and its defaults, in both forms.
func TestReadHealth(t *testing.T) {
	balanced := []string{"shop path /healthz every 5s within 2s, 2 to join, 3 to leave", "orders grpc every 10s within 1s, 1 to join, 2 to leave"}
	full := []string{"shop path /healthz and grpc every 10s within 1s, 2 to join, 3 to leave"}
	cases := []struct {
		name, file string
		want       []string
	}{
		{"balanced example, YAML", "../../examples/balanced/yaml/proxyfile.yaml", balanced},
		{"balanced example, HOCON", "../../examples/balanced/hocon/Proxyfile", balanced},
		{"dns example, no health", "../../examples/dns/yaml/proxyfile.yaml", nil},
		{"full example, YAML", "../../examples/full/yaml/proxyfile.yaml", full},
		{"full example, HOCON", "../../examples/full/hocon/Proxyfile", full},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, problems := Proxyfile(c.file)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := health(file.Gateway); !slices.Equal(got, c.want) {
				t.Errorf("health = %q, want %q", got, c.want)
			}
		})
	}
}

// The reader fills each route's rate limit and its defaults, burst 0 and by client, in both forms.
func TestReadRateLimits(t *testing.T) {
	want := []string{"rpc 100 per 1s, burst 0, all", "login 5 per 60s, burst 5, client", "shop no limit"}
	cases := []struct{ name, file string }{
		{"limited example, YAML", "../../examples/limited/yaml/proxyfile.yaml"},
		{"limited example, HOCON", "../../examples/limited/hocon/Proxyfile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, problems := Proxyfile(c.file)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := limits(file.Gateway); !slices.Equal(got, want) {
				t.Errorf("limits = %q, want %q", got, want)
			}
		})
	}
}

// limits writes one line per route, rpc for the rpc entry and the name otherwise: its requests per period, its burst
// and whose requests are counted together, or no limit.
func limits(g *model.Gateway) []string {
	var lines []string
	for _, r := range g.Routes {
		name, limit := r.Name.Value, r.RateLimit
		if r.RPC != nil {
			name, limit = "rpc", r.RPC.RateLimit
		}
		if limit == nil {
			lines = append(lines, name+" no limit")
			continue
		}
		by := "all"
		if limit.ByClient {
			by = "client"
		}
		lines = append(lines, fmt.Sprintf("%s %d per %s, burst %d, %s", name, limit.Requests, model.FormatSeconds(limit.Per.Millis), limit.Burst, by))
	}
	return lines
}

// The reader fills tls, and the client's address, whose default follows tls: a gateway that terminates TLS usually
// faces clients, and one left at forwarded would let each client choose its own address by sending X-Forwarded-For.
func TestReadListener(t *testing.T) {
	const tlsLine = "tls: {certificate: /etc/gateway/tls/certificate.pem, key: /etc/gateway/tls/key.pem}"
	const withTLS = "tls /etc/gateway/tls/certificate.pem /etc/gateway/tls/key.pem"
	const example = withTLS + " clients /etc/gateway/tls/clients-ca.pem; client connection"
	cases := []struct{ name, file, document, want string }{
		{name: "neither", document: b.String(), want: "plain HTTP; client forwarded"},
		{name: "tls without client_address", document: b.Inserted(8, tlsLine), want: withTLS + "; client connection"},
		{name: "tls behind a proxy", document: b.Inserted(8, tlsLine+"\nclient_address: forwarded"), want: withTLS + "; client forwarded"},
		{name: "connection without tls", document: b.Inserted(8, "client_address: connection"), want: "plain HTTP; client connection"},
		{name: "tls example, YAML", file: "../../examples/tls/yaml/proxyfile.yaml", want: example},
		{name: "tls example, HOCON", file: "../../examples/tls/hocon/Proxyfile", want: example},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := c.file
			if path == "" {
				path = filepath.Join(t.TempDir(), "g.yaml")
				testsupport.WriteFile(t, path, c.document)
			}
			file, problems := Proxyfile(path)
			if problems != nil {
				t.Fatalf("problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got := listener(file.Gateway); got != c.want {
				t.Errorf("listener = %q, want %q", got, c.want)
			}
		})
	}
}

// listener writes what the public listener serves and whose address counts as the client's, on one line.
func listener(g *model.Gateway) string {
	served := "plain HTTP"
	if g.TLS != nil {
		served = "tls " + g.TLS.Certificate + " " + g.TLS.Key
		if g.TLS.ClientsCA != "" {
			served += " clients " + g.TLS.ClientsCA
		}
	}
	return served + "; client " + string(g.ClientAddress)
}
