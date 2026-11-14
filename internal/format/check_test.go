package format

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// segmentRule and agentRule are the messages of the rows that break those rules, each at the same column of line 7.
const (
	segmentRule = `g.yaml:7:46: "segment" must be 1 to 200 characters: letters, digits, spaces and _ . : / % & # = + \ - @`
	agentRule   = `g.yaml:7:56: "agent" must be an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000`
)

// m is the valid proxyfile the problem rows change, one change each.
var m = testsupport.Document{
	"listen: 8000",
	"services:",
	"  app: {http: 3000}",
	"origins: origins.json",
	"routes:",
	"  - {name: app, prefix: /, to: app}",
}

type checkCase struct {
	name string
	spec Field
	// file is read when set, and named in the problems by this path; otherwise document is read as g.yaml.
	file, document string
	want           []string
}

func runCheckCases(t *testing.T, cases []checkCase) {
	t.Helper()
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
			if got := testsupport.ProblemStrings(Check(c.spec, root)); !slices.Equal(got, c.want) {
				t.Errorf("Check problems = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	runCheckCases(t, []checkCase{
		{name: "minimal example", spec: Proxyfile, file: "../../examples/minimal/yaml/proxyfile.yaml"},
		{name: "partial example", spec: Proxyfile, file: "../../examples/partial/yaml/proxyfile.yaml"},
		{name: "full example", spec: Proxyfile, file: "../../examples/full/yaml/proxyfile.yaml"},
		{name: "unknown key", spec: Proxyfile, document: m.Replaced(1, 1, "liste: 8000"), want: []string{
			`g.yaml:1:1: "listen" is required here`,
			`g.yaml:1:1: unknown key "liste"; known here: admin, auth, client_address, cors, dns, envoy, haproxy, limits, listen, log, nginx, origins, remove_from_clients, routes, rpc, services, timeouts, tls, tracing`,
		}},
		{name: "balance not a policy", spec: Proxyfile, document: m.Replaced(3, 3, "  app: {http: 3000, balance: fastest}"),
			want: []string{`g.yaml:3:30: "balance" must be one of: round_robin, least_requests, random, hash`}},
		{name: "hash with header and client", spec: Proxyfile,
			document: m.Replaced(3, 3, "  app: {http: 3000, balance: hash, hash: {header: x-user, client: true}}"),
			want:     []string{`g.yaml:3:42: exactly one of header, client is needed here`}},
		{name: "hash header in capitals", spec: Proxyfile, document: m.Replaced(3, 3, "  app: {http: 3000, balance: hash, hash: {header: X-User}}"),
			want: []string{`g.yaml:3:51: "header" must be a lower-case header name`}},
		{name: "health counts out of range", spec: Proxyfile,
			document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: /healthz, healthy: 0, unhealthy: 256}}"),
			want:     []string{`g.yaml:3:55: "healthy" must be a whole number from 1 to 255`, `g.yaml:3:69: "unhealthy" must be a whole number from 1 to 255`}},
		{name: "health interval none", spec: Proxyfile, document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: /healthz, interval: none}}"),
			want: []string{`g.yaml:3:56: "interval" must be a time such as 5s`}},
		{name: "health path not a path", spec: Proxyfile, document: m.Replaced(3, 3, "  app: {http: 3000, health: {path: healthz}}"),
			want: []string{`g.yaml:3:36: "path" must be a path starting with /, with no space`}},
		{name: "health path with a space", spec: Proxyfile, document: m.Replaced(3, 3, `  app: {http: 3000, health: {path: "/healthz deep"}}`),
			want: []string{`g.yaml:3:36: "path" must be a path starting with /, with no space`}},
		// A key that takes only true refuses false and a quoted "true" in the check, as the schema does.
		{name: "hash client false", spec: Proxyfile, document: m.Replaced(3, 3, "  app: {http: 3000, balance: hash, hash: {client: false}}"),
			want: []string{`g.yaml:3:51: "client" must be true`}},
		{name: "health grpc quoted true", spec: Proxyfile, document: m.Replaced(3, 3, `  app: {grpc: 3000, health: {grpc: "true"}}`),
			want: []string{`g.yaml:3:36: "grpc" must be true, not quoted or block text`}},
		{name: "tls without certificate and key", spec: Proxyfile, document: m.Inserted(2, "tls: {}"),
			want: []string{`g.yaml:2:6: "certificate" is required here`, `g.yaml:2:6: "key" is required here`}},
		{name: "tls certificate a relative path", spec: Proxyfile, document: m.Inserted(2, "tls: {certificate: gateway.pem, key: /etc/gateway/tls/key.pem}"),
			want: []string{`g.yaml:2:20: "certificate" must be an absolute path`}},
		{name: "tls clients without ca", spec: Proxyfile, document: m.Inserted(2, "tls: {certificate: /c.pem, key: /k.pem, clients: {}}"),
			want: []string{`g.yaml:2:50: "ca" is required here`}},
		{name: "client_address not a choice", spec: Proxyfile, document: m.Inserted(2, "client_address: peer"),
			want: []string{`g.yaml:2:17: "client_address" must be one of: forwarded, connection`}},
		{name: "rate limit without requests and per", spec: Proxyfile, document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, rate_limit: {}}"),
			want: []string{`g.yaml:6:49: "per" is required here`, `g.yaml:6:49: "requests" is required here`}},
		{name: "rate limit values", spec: Proxyfile,
			document: m.Replaced(6, 6, "  - {name: app, prefix: /, to: app, rate_limit: {requests: 0, per: second, burst: -1, by: ip}}"),
			want: []string{`g.yaml:6:60: "requests" must be a whole number from 1 to 4294967295`, `g.yaml:6:68: "per" must be a time such as 1s or 1m`,
				`g.yaml:6:83: "burst" must be a whole number from 0 to 4294967295`, `g.yaml:6:91: "by" must be one of: client, all`}},
		{name: "rate limit on the rpc entry", spec: Proxyfile, document: m.Replaced(6, 6, `  - rpc: {to: {"*": app}, rate_limit: {requests: 0, per: 1s}}`),
			want: []string{`g.yaml:6:50: "requests" must be a whole number from 1 to 4294967295`}},
		// A key that moved into the envoy block in 2.0 is refused at its 1.x place with its new place; the rest of limits
		// stays where it was.
		{name: "namespace at its 1.x place", spec: Proxyfile, document: m.Inserted(1, "namespace: example.gateway"),
			want: []string{`g.yaml:1:1: "namespace" moved to envoy.namespace in proxyfile 2.0`}},
		{name: "limits.heap at its 1.x place", spec: Proxyfile, document: m.Inserted(2, "limits: {heap: 192MiB, connections: 100}"),
			want: []string{`g.yaml:2:10: "heap" moved to envoy.heap in proxyfile 2.0`}},
		// tracing is a core key again: a 1.x file's tracing is read as it, and envoy.tracing is its old place.
		{name: "tracing at the top, the fewest keys", spec: Proxyfile, document: m.Inserted(7, "tracing: {provider: {xray: {segment: s}}}")},
		{name: "tracing provider of no known kind", spec: Proxyfile, document: m.Inserted(7, "tracing: {provider: {zipkin: {}}}"),
			want: []string{`g.yaml:7:21: "provider" must be a mapping with one key, xray or otel`}},
		// otel is read at the top alone, since envoy.tracing, the old place, takes xray only; its sample takes rate alone.
		{name: "otel at the old place", spec: Proxyfile, document: m.Inserted(7, `envoy: {tracing: {provider: {otel: {endpoint: "127.0.0.1:4317", service: s}}}}`),
			want: []string{`g.yaml:7:29: "provider" must be a mapping with the one key xray`}},
		{name: "otel without endpoint and service", spec: Proxyfile, document: m.Inserted(7, "tracing: {provider: {otel: {}}}"),
			want: []string{`g.yaml:7:28: "endpoint" is required here`, `g.yaml:7:28: "service" is required here`}},
		{name: "otel sample with first", spec: Proxyfile, document: m.Inserted(7, `tracing: {provider: {otel: {endpoint: "127.0.0.1:4317", service: s, sample: {first: 1, rate: 5%}}}}`),
			want: []string{`g.yaml:7:78: unknown key "first"; known here: rate`}},
		// A provider that names two kinds fits neither choice, so it gets the Either's message.
		{name: "otel and xray together", spec: Proxyfile, document: m.Inserted(7, `tracing: {provider: {xray: {segment: s}, otel: {endpoint: "127.0.0.1:4317", service: s}}}`),
			want: []string{`g.yaml:7:21: "provider" must be a mapping with one key, xray or otel`}},
		{name: "otel service with a !", spec: Proxyfile, document: m.Inserted(7, `tracing: {provider: {otel: {endpoint: "127.0.0.1:4317", service: gateway!}}}`),
			want: []string{`g.yaml:7:66: "service" must be 1 to 200 characters: letters, digits, spaces and _ . : / % & # = + \ - @`}},
		// auth without a key passes the check: the reader says it needs ask or jwt.
		{name: "auth with no key", spec: Proxyfile, document: m.Inserted(7, "auth: {}")},
		{name: "jwt without its required keys", spec: Proxyfile, document: m.Inserted(7, "auth: {jwt: {}}"), want: []string{
			`g.yaml:7:13: "audiences" is required here`, `g.yaml:7:13: "issuer" is required here`, `g.yaml:7:13: "keys" is required here`}},
		{name: "jwt audiences one text", spec: Proxyfile, document: m.Inserted(7, "auth: {jwt: {issuer: i, audiences: api.example, keys: /k.json}}"),
			want: []string{`g.yaml:7:36: "audiences" must be a list of audiences, or any`}},
		{name: "jwt audiences an empty text", spec: Proxyfile, document: m.Inserted(7, `auth: {jwt: {issuer: i, audiences: [""], keys: /k.json}}`),
			want: []string{`g.yaml:7:37: "" in "audiences" must be one character or more`}},
		{name: "jwt keys relative", spec: Proxyfile, document: m.Inserted(7, "auth: {jwt: {issuer: i, audiences: any, keys: jwks.json}}"),
			want: []string{`g.yaml:7:47: "keys" must be an absolute path, or a URL starting with https://`}},
		{name: "jwt keys over http", spec: Proxyfile, document: m.Inserted(7, `auth: {jwt: {issuer: i, audiences: any, keys: "http://login.example.com/jwks.json"}}`),
			want: []string{`g.yaml:7:47: "keys" must be an absolute path, or a URL starting with https://`}},
		{name: "jwt claims header in capitals and a claim name with two dots", spec: Proxyfile,
			document: m.Inserted(7, "auth: {jwt: {issuer: i, audiences: any, keys: /k.json, claims: {X-Sub: sub, x-roles: realm..roles}}}"),
			want: []string{`g.yaml:7:65: the header name "X-Sub" must be a lower-case header name`,
				`g.yaml:7:86: "x-roles" must be a claim name, with dots for a nested claim, such as realm.roles`}},
		{name: "jwt header in capitals and ca relative", spec: Proxyfile,
			document: m.Inserted(7, `auth: {jwt: {issuer: i, audiences: any, keys: "https://login.example.com/k.json", ca: ca.pem, header: X-Token}}`),
			want:     []string{`g.yaml:7:87: "ca" must be an absolute path`, `g.yaml:7:103: "header" must be a lower-case header name`}},
		{name: "jwt issuer empty", spec: Proxyfile, document: m.Inserted(7, `auth: {jwt: {issuer: "", audiences: any, keys: /k.json}}`),
			want: []string{`g.yaml:7:22: "issuer" must be one character or more`}},
		// nginx and haproxy are each proxy's own block, which takes no key in this version: written empty they are
		// accepted, and a key in either is refused, so a file written for a later version is never rendered without it.
		{name: "nginx and haproxy empty", spec: Proxyfile, document: m.Inserted(7, "nginx: {}\nhaproxy: {}")},
		{name: "a key in nginx", spec: Proxyfile, document: m.Inserted(7, "nginx: {worker_connections: 1024}"),
			want: []string{`g.yaml:7:9: "worker_connections" in nginx: the nginx block takes no key in this version; see docs/portability.md`}},
		{name: "a key in haproxy", spec: Proxyfile, document: m.Inserted(7, "haproxy: {maxconn: 1024}"),
			want: []string{`g.yaml:7:11: "maxconn" in haproxy: the haproxy block takes no key in this version; see docs/portability.md`}},
		{name: "wrong kind", spec: Proxyfile, document: m.Replaced(2, 3, "services: 5"),
			want: []string{`g.yaml:2:11: "services" must be a mapping`}},
		{name: "lowest port", spec: Proxyfile, document: m.Replaced(1, 1, "listen: 1")},
		{name: "below the lowest port", spec: Proxyfile, document: m.Replaced(1, 1, "listen: 0"),
			want: []string{`g.yaml:1:9: "listen" must be a whole number from 1 to 65535`}},
		{name: "highest port", spec: Proxyfile, document: m.Replaced(1, 1, "listen: 65535")},
		{name: "above the highest port", spec: Proxyfile, document: m.Replaced(1, 1, "listen: 65536"),
			want: []string{`g.yaml:1:9: "listen" must be a whole number from 1 to 65535`}},
		// Envoy holds the header count, the connections and the retries in a uint32 and refuses a larger value.
		{name: "header count above Envoy's largest", spec: Proxyfile,
			document: m.Inserted(2, "limits: {request_headers: {size: 8KiB, count: 4294967296}}"),
			want:     []string{`g.yaml:2:47: "count" must be a whole number from 1 to 4294967295`}},
		{name: "connections above Envoy's largest", spec: Proxyfile, document: m.Inserted(2, "limits: {connections: 4294967296}"),
			want: []string{`g.yaml:2:23: "connections" must be a whole number from 1 to 4294967295`}},
		{name: "retries above Envoy's largest", spec: Proxyfile, document: m.Replaced(6, 6,
			`  - rpc: {retries: 4294967296, to: {"*": app}}`, "  - {name: app, prefix: /, to: app, retries: 4294967296}"),
			want: []string{
				`g.yaml:6:20: "retries" must be a whole number from 0 to 4294967295`,
				`g.yaml:7:46: "retries" must be a whole number from 0 to 4294967295`,
			}},
		{name: "size", spec: Proxyfile, document: m.Inserted(2, "envoy: {heap: 192MB}"),
			want: []string{`g.yaml:2:15: "heap" must be a size such as 192MiB`}},
		{name: "time", spec: Proxyfile,
			document: m.Inserted(2, "auth: {ask: app /check, timeout: none, enabled: true, when_unavailable: allow}"),
			want:     []string{`g.yaml:2:34: "timeout" must be a time such as 2s`}},
		{name: "percent", spec: Proxyfile, document: m.Inserted(2, "log: {status_from: 400, sample: 1.5%}"),
			want: []string{`g.yaml:2:33: "sample" must be a whole percentage such as 1%`}},
		{name: "enum", spec: Proxyfile,
			document: m.Inserted(2, "auth: {ask: app /check, timeout: 2s, enabled: true, when_unavailable: maybe}"),
			want:     []string{`g.yaml:2:71: "when_unavailable" must be one of: allow, deny`}},
		{name: "bool", spec: Proxyfile,
			document: m.Inserted(2, "auth: {ask: app /check, timeout: 2s, enabled: yes, when_unavailable: allow}"),
			want:     []string{`g.yaml:2:47: "enabled" must be true or false`}},
		{name: "header name in capitals", spec: Proxyfile, document: m.Inserted(2, "remove_from_clients: [X-Secret-*]"),
			want: []string{`g.yaml:2:23: "X-Secret-*" in "remove_from_clients" must be a lower-case header name, or a prefix ending in *`}},
		{name: "service name", spec: Proxyfile, document: m.Replaced(3, 3, "  App: {http: 3000}"),
			want: []string{`g.yaml:3:3: the service name "App" must be lower-case letters, digits and -, starting with a letter`}},
		{name: "service name and its port", spec: Proxyfile, document: m.Replaced(3, 3, "  App: {http: 0}"), want: []string{
			`g.yaml:3:3: the service name "App" must be lower-case letters, digits and -, starting with a letter`,
			`g.yaml:3:15: "http" must be a whole number from 1 to 65535`,
		}},
		{name: "match kind", spec: Proxyfile, document: m.Replaced(6, 6, "  - {name: app, prefix: /, path: /x, to: app}"),
			want: []string{`g.yaml:6:5: exactly one of prefix, path, pattern is needed here`}},
		{name: "rpc entry key", spec: Proxyfile, document: m.Replaced(6, 6, "  - rpc:", `      to: {"*": app}`, "    retries: 1"),
			want: []string{`g.yaml:8:5: unknown key "retries"; known here: rpc`}},
		{name: "rpc target", spec: Proxyfile, document: m.Replaced(6, 6, "  - rpc:", `      to: {"*": {name: x}}`),
			want: []string{`g.yaml:7:17: "service" is required here`}},
		{name: "quoted number", spec: Proxyfile, document: m.Replaced(1, 1, `listen: "8000"`),
			want: []string{`g.yaml:1:9: "listen" must be a whole number from 1 to 65535, not quoted or block text`}},
		{name: "block number", spec: Proxyfile, document: m.Replaced(1, 1, "listen: |-", "  8000"),
			want: []string{`g.yaml:1:9: "listen" must be a whole number from 1 to 65535, not quoted or block text`}},
		{name: "quoted boolean", spec: Proxyfile,
			document: m.Inserted(2, `auth: {ask: app /check, timeout: 2s, enabled: 'true', when_unavailable: allow}`),
			want:     []string{`g.yaml:2:47: "enabled" must be true or false, not quoted or block text`}},
		// Envoy's regex field has min_len 1, and a null pattern is the empty text in the tree.
		{name: "empty pattern", spec: Proxyfile,
			document: m.Replaced(6, 6, `  - {name: app, pattern: "", to: app}`, "  - {name: app2, pattern: null, to: app}"),
			want: []string{
				`g.yaml:6:26: "pattern" must be a regular expression that is not empty`,
				`g.yaml:7:27: "pattern" must be a regular expression that is not empty`,
			}},
		{name: "quoted text and enum", spec: Proxyfile,
			document: m.Inserted(2, `auth: {ask: "app /check", timeout: "2s", enabled: true, when_unavailable: "allow"}`)},
		// The name matches its pattern, but a YAML reader takes 404 as a number, which the JSON Schema refuses.
		{name: "plain number as text", spec: Proxyfile, document: m.Replaced(6, 6, "  - {name: 404, prefix: /, to: app}"),
			want: []string{`g.yaml:6:12: "name" must be text; quote it`}},
		// origins holds the environments by name, or names an origins file as in 1.0; m writes the file form.
		{name: "origins inline", spec: Proxyfile, document: m.Replaced(4, 4, "origins:", "  all: {hosts: [localhost]}")},
		{name: "origins a list", spec: Proxyfile, document: m.Replaced(4, 4, "origins: [a]"),
			want: []string{`g.yaml:4:10: "origins" must be a mapping of environments by name, or a file name`}},
		{name: "environment name empty", spec: Proxyfile, document: m.Replaced(4, 4, "origins:", "  \"\": {hosts: [localhost]}"),
			want: []string{`g.yaml:5:3: the environment name "" must be one character or more`}},
		// The key is the environment's name, so the name key of an origins file's environment is not known here.
		{name: "environment with a name key", spec: Proxyfile,
			document: m.Replaced(4, 4, "origins:", "  all: {hosts: [localhost], name: all}"),
			want:     []string{`g.yaml:5:29: unknown key "name"; known here: hosts, origins, patterns`}},
		{name: "cors max_age a number", spec: Proxyfile,
			document: m.Replaced(4, 4, "cors: {max_age: 600}", "origins:", "  all: {hosts: [localhost]}"),
			want:     []string{`g.yaml:4:17: "max_age" must be text; quote it`}},
		// The RPC methods may be written in the proxyfile, as the methods file writes them, or read from .proto files;
		// rpc holds exactly one of the two, so an rpc written without either is a mistake, and one with both would
		// leave one list unread.
		{name: "rpc methods inline", spec: Proxyfile,
			document: m.Inserted(7, "rpc: {methods: [{service: shop.v1.CartService, method: AddItem}]}")},
		{name: "rpc without methods", spec: Proxyfile, document: m.Inserted(7, "rpc: {}"),
			want: []string{`g.yaml:7:6: exactly one of methods, proto is needed here`}},
		{name: "rpc proto", spec: Proxyfile, document: m.Inserted(7, "rpc: {proto: {roots: [proto, /abs/proto], option: example.gateway.v1.route}}")},
		{name: "rpc with methods and proto", spec: Proxyfile, document: m.Inserted(7, "rpc: {methods: [], proto: {roots: [proto], option: o.v1.route}}"),
			want: []string{`g.yaml:7:6: exactly one of methods, proto is needed here`}},
		{name: "rpc with a misspelt key", spec: Proxyfile, document: m.Inserted(7, "rpc: {protos: {}}"),
			want: []string{`g.yaml:7:6: exactly one of methods, proto is needed here`, `g.yaml:7:7: unknown key "protos"; known here: methods, proto`}},
		{name: "proto without its keys", spec: Proxyfile, document: m.Inserted(7, "rpc: {proto: {}}"),
			want: []string{`g.yaml:7:14: "option" is required here`, `g.yaml:7:14: "roots" is required here`}},
		{name: "proto values", spec: Proxyfile, document: m.Inserted(7, "rpc: {proto: {roots: proto, option: .o.route, root: x}}"),
			want: []string{
				`g.yaml:7:22: "roots" must be a list`,
				`g.yaml:7:37: "option" must be an option's full name such as example.gateway.v1.route`,
				`g.yaml:7:47: unknown key "root"; known here: option, roots`,
			}},
		{name: "tracing example", spec: Proxyfile, file: "../../examples/tracing/yaml/proxyfile.yaml"},
		{name: "grpc example", spec: Proxyfile, file: "../../examples/grpc/yaml/proxyfile.yaml"},
		{name: "tracing, the fewest keys", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: example-gateway}}}}")},
		{name: "tracing without provider", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {path_in_url: true}}"),
			want: []string{`g.yaml:7:18: "provider" is required here`}},
		// provider is chosen by its one key, as the rpc entry of routes is chosen by rpc.
		{name: "provider without xray", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {zipkin: {}}}}"),
			want: []string{`g.yaml:7:29: "provider" must be a mapping with the one key xray`}},
		{name: "provider with a key beside xray", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: a}, zipkin: {}}}}"),
			want: []string{`g.yaml:7:50: unknown key "zipkin"; known here: xray`}},
		{name: "segment missing", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {}}}}"),
			want: []string{`g.yaml:7:36: "segment" is required here`}},
		// X-Ray takes a segment name of 1 to 200 characters.
		{name: "segment of 200 characters", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: "+strings.Repeat("a", 200)+"}}}}")},
		{name: "segment of 201 characters", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: "+strings.Repeat("a", 201)+"}}}}"), want: []string{segmentRule}},
		{name: "segment, every character X-Ray takes", spec: Proxyfile,
			document: m.Inserted(7, `envoy: {tracing: {provider: {xray: {segment: 'a b_c.d:e/f%g&h#i=j+k\l-m@n'}}}}`)},
		{name: "segment with a character X-Ray refuses", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: gateway!}}}}"), want: []string{segmentRule}},
		{name: "segment with a letter outside ASCII", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: passerelle-é}}}}"), want: []string{segmentRule}},
		{name: "agent IPv6", spec: Proxyfile,
			document: m.Inserted(7, `envoy: {tracing: {provider: {xray: {segment: s, agent: "[::1]:2000"}}}}`), want: []string{agentRule}},
		{name: "agent host name", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, agent: localhost:2000}}}}"), want: []string{agentRule}},
		{name: "rate above 100%", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, sample: {rate: 101%}}}}}"),
			want: []string{`g.yaml:7:64: "rate" must be a whole percentage such as 5%`}},
		{name: "rate not whole", spec: Proxyfile, document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, sample: {rate: 2.5%}}}}}"),
			want: []string{`g.yaml:7:64: "rate" must be a whole percentage such as 5%`}},
		// The X-Ray sampler holds first, its fixed_target, in a uint32.
		{name: "sample at its bounds", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, sample: {first: 4294967295, rate: 100%, rules: [{first: 0, rate: 0%}]}}}}}")},
		{name: "first above Envoy's largest", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, sample: {first: 4294967296}}}}}"),
			want:     []string{`g.yaml:7:65: "first" must be a whole number from 0 to 4294967295`}},
		{name: "rule without first and rate", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s, sample: {rules: [{path: /a}]}}}}}"),
			want:     []string{`g.yaml:7:66: "first" is required here`, `g.yaml:7:66: "rate" is required here`}},
		{name: "rule patterns", spec: Proxyfile,
			document: m.Inserted(7, `envoy: {tracing: {provider: {xray: {segment: s, sample: {rules: [{host: "a b", method: get, path: a, first: 0, rate: 0%}]}}}}}`),
			want: []string{
				`g.yaml:7:73: "host" must be a pattern of letters, digits, ., -, :, [, ], * and ?`,
				`g.yaml:7:88: "method" must be a method in capitals, or a pattern of capitals, * and ?`,
				`g.yaml:7:99: "path" must be a pattern of printable characters without spaces, starting with / or *`,
			}},
		{name: "tag with env and header", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s}}, tags: {a: {env: A, header: b}}}}"),
			want:     []string{`g.yaml:7:61: exactly one of env, header is needed here`}},
		{name: "tag with neither", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s}}, tags: {a: {default: x}}}}"),
			want:     []string{`g.yaml:7:61: exactly one of env, header is needed here`}},
		{name: "tag name with a dash", spec: Proxyfile,
			document: m.Inserted(7, "envoy: {tracing: {provider: {xray: {segment: s}}, tags: {request-id: {header: x-request-id}}}}"),
			want:     []string{`g.yaml:7:58: the tag name "request-id" must be letters, digits and _`}},
		{name: "tag values", spec: Proxyfile,
			document: m.Inserted(7, `envoy: {tracing: {provider: {xray: {segment: s}}, tags: {a: {header: X-Id}, b: {env: 1ENV}, c: {env: A, default: ""}}}}`),
			want: []string{
				`g.yaml:7:70: "header" must be a lower-case header name`,
				`g.yaml:7:86: "env" must be a variable name: letters, digits and _, not starting with a digit`,
				`g.yaml:7:114: "default" must be one character or more`,
			}},
	})
}

// The message cases TestCheck's rows do not reach: the other wrong kinds, the root, a number with no maximum, an
// Either no choice fits, and the subject a list item that is not a single value gets.
func TestCheckMessages(t *testing.T) {
	runCheckCases(t, []checkCase{
		{name: "file not a mapping", spec: Proxyfile, document: "- listen\n",
			want: []string{`g.yaml:1:1: the file must be a mapping`}},
		{name: "file not a list", spec: Methods, document: "service: shop.v1.CartService\n",
			want: []string{`g.yaml:1:1: the file must be a list`}},
		{name: "not a list", spec: Proxyfile, document: m.Inserted(2, "remove_from_clients: x-secret"),
			want: []string{`g.yaml:2:22: "remove_from_clients" must be a list`}},
		{name: "not a single value", spec: Proxyfile, document: m.Replaced(1, 1, "listen: [8000]"),
			want: []string{`g.yaml:1:9: "listen" must be a single value`}},
		// Every whole number of the three descriptions has a maximum, so this row builds a description without one.
		{name: "number with no maximum", spec: Field{Kind: Map, Fields: []Field{{Name: "count", Kind: Int, Min: 1}}},
			document: "count: 0\n", want: []string{`g.yaml:1:8: "count" must be a whole number of at least 1`}},
		{name: "number not written in plain digits", spec: Proxyfile, document: m.Replaced(1, 1, "listen: 08000"),
			want: []string{`g.yaml:1:9: "listen" must be a whole number from 1 to 65535`}},
		{name: "no match kind", spec: Proxyfile, document: m.Replaced(6, 6, "  - {name: app, to: app}"),
			want: []string{`g.yaml:6:5: exactly one of prefix, path, pattern is needed here`}},
		{name: "route not a mapping", spec: Proxyfile, document: m.Replaced(6, 6, "  - app"),
			want: []string{`g.yaml:6:5: "app" in "routes" must be a mapping: a route or an rpc entry`}},
		{name: "rpc target a list", spec: Proxyfile, document: m.Replaced(6, 6, "  - rpc:", `      to: {"*": [app]}`),
			want: []string{`g.yaml:7:17: "*" must be a service name, or a mapping with service and name`}},
		{name: "list item not a single value", spec: Proxyfile, document: m.Inserted(2, "remove_from_clients: [[x-secret]]"),
			want: []string{`g.yaml:2:23: an item in "remove_from_clients" must be a single value`}},
		{name: "service not a mapping", spec: Proxyfile, document: m.Replaced(3, 3, "  app: 3000"),
			want: []string{`g.yaml:3:8: "app" must be a mapping`}},
		{name: "list item of the file not a mapping", spec: Methods, document: "- shop.v1.CartService\n",
			want: []string{`g.yaml:1:3: "shop.v1.CartService" in the file must be a mapping`}},
	})
}

// The origins and methods descriptions accept the forms their files take and refuse a value against a rule.
func TestCheckOriginsAndMethods(t *testing.T) {
	runCheckCases(t, []checkCase{
		{name: "origins", spec: Origins, document: `{"cors": {"allow_methods": "GET,POST", "allow_headers": "authorization", ` +
			`"expose_headers": "x-request-id", "max_age": "600"}, "environments": [{"name": "live", ` +
			`"hosts": ["api.example.com"], "origins": ["https://example.com"], "patterns": ["https://*.example.com"]}]}`},
		{name: "origins max age", spec: Origins, document: `{"cors": {"max_age": "10m"}, "environments": []}`,
			want: []string{`g.yaml:1:22: "max_age" must be a number of seconds`}},
		{name: "origins without environments", spec: Origins, document: `{"cors": {}}`,
			want: []string{`g.yaml:1:1: "environments" is required here`}},
		{name: "methods", spec: Methods, document: `[{"service": "shop.v1.CartService", "method": "AddItem", ` +
			`"action": "cart.add", "reports": ["cart"]}, {"service": "shop.v1.CartService", "method": "ListItems"}]`},
		{name: "methods service", spec: Methods, document: `[{"service": "CartService", "method": "AddItem"}]`,
			want: []string{`g.yaml:1:14: "service" must be a full service name such as shop.v1.CartService`}},
		{name: "methods method", spec: Methods, document: `[{"service": "shop.v1.CartService", "method": "Add.Item"}]`,
			want: []string{`g.yaml:1:47: "method" must be a method name such as AddItem`}},
	})
}

// Check takes any description, not only the three the package holds, so a description a caller builds is checked
// with its own patterns, a value's and a dictionary key's alike.
func TestCheckOtherDescription(t *testing.T) {
	other := Field{Kind: Map, Fields: []Field{
		{Name: "word", Kind: Text, Pattern: `^[a-z]+$`, Expect: "lower-case letters"},
		{Name: "by", Kind: Dict, Key: &Field{Name: "code", Kind: Text, Pattern: `^[A-Z]{2}$`, Expect: "two capitals"},
			Item: &Field{Kind: Text}},
	}}
	runCheckCases(t, []checkCase{
		{name: "value and key patterns", spec: other, document: "word: Hello\nby: {ro: x, FR: y}\n", want: []string{
			`g.yaml:1:7: "word" must be lower-case letters`,
			`g.yaml:2:6: the code "ro" must be two capitals`,
		}},
	})
}
