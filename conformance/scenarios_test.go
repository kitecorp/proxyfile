package conformance

import (
	"crypto/tls"
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// scenarios run in this order, each exchange after the one before but for those that overlap (exchange.overlap), which
// wait beside the rest of their own scenario alone: a later scenario's quiet and burst count on nothing else reaching
// the proxy meanwhile. The rate limit runs last, so the requests the scenarios before it send to its route come first,
// and its quiet lets that route's bucket fill again before its burst.
var scenarios = []scenario{routing, mutualTLS, cors, auth, toldScheme, timeout, retry, gRPC, paths, asWritten, offRoute, clientKeys, hash, tracing, tracingRetried, rateLimit, cost, backendLimits, traffic}

// get is a GET over HTTP/1.1 to localhost, a host of the example's local environment.
func get(path string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: path}
}

// rpc is a unary gRPC call to localhost with the empty message.
func rpc(path string) request {
	return request{proto: "grpc", method: "POST", host: "localhost", path: path}
}

// The labels of the nginx entries' files a target's own literal holds on (own.files), a list for each reason the
// files share, so a file added to the entries is added once for each reason it shares.
var (
	// nginxFiles are the battery's five files for nginx, beside the examples the nginx entries run too.
	nginxFiles = []string{"nginx-file", "nginx-plain", "nginx-checks", "nginx-auth", "nginx-tracing"}
	// nginxYAMLFiles are nginx.yaml, nginx-auth.yaml, which writes nginx.yaml's keys beside the check, and
	// nginx-tracing.yaml, nginx-auth.yaml's copy with tracing: the route PCRE2 gives up on, the idle time of 2 s and the
	// environment of 64 Origin patterns.
	nginxYAMLFiles = []string{"nginx-file", "nginx-auth", "nginx-tracing"}
	// patternFiles are the files with a pattern route, where nginx refuses a path it would otherwise match decoded
	// (routes[].pattern).
	patternFiles = []string{"nginx-file", "nginx-auth", "nginx-tracing", "portable"}
	// checkedPatternFiles are the files of patternFiles that ask a check, the only ones a row of the check's keys runs
	// on.
	checkedPatternFiles = []string{"nginx-auth", "nginx-tracing", "portable"}
	// allowFiles are the files nginx runs whose check is asked under when_unavailable: allow, where nginx answers 403
	// to an answer its script cannot read (auth.when_unavailable).
	allowFiles = []string{"nginx-auth", "nginx-tracing", "portable"}
	// certificateFiles are the files that ask a client for its certificate (tls.clients.ca).
	certificateFiles = []string{"nginx-file", "nginx-auth", "portable"}
)

// onEach returns l as the literal of each of files.
func onEach(files []string, l literal) map[string]literal {
	on := map[string]literal{}
	for _, f := range files {
		on[f] = l
	}
	return on
}

// routing: every value measured on Envoy 1.40.2, and nginx's own literals (by) on nginx 1.30.6 and 1.31.8. A shop
// answer may come from either backend, so no row names the instance. Routes match by case. The check's path,
// /_pf_auth, is the path nginx files are to ask the check through once they write auth.ask, so a client's request for
// it, written or with its underscore escaped, must reach the routes as any other path does. nginx answers a path
// holding a newline, written %0A, with 400 before any route (pathRow), where Envoy routes it as sent: the battery's
// nginx file has a pattern route that takes any character, which examples/full and examples/portable do not. An
// Upgrade other than WebSocket is refused; an h2c upgrade is dropped with its HTTP2-Settings, and the request served.
// The service is told the scheme the client used, https on a file that writes tls. With client_address = connection a
// client chooses none of x-forwarded-proto, x-envoy-external-address and x-request-id: the service receives the
// connection's scheme, and an address and an id the proxy writes in place of the client's, which a row reads as values
// other than the client's (replaced), because the id is new on each request. A service's answer carrying
// X-Accel-Redirect, which the stub writes when the request carries x-stub-accel-redirect, reaches the client, and the
// request is not sent to the URI it names; its Keep-Alive, which the stub writes for x-stub-keep-alive, does not, as it
// speaks of the service's connection. A header name outside RFC 9110's token characters is answered 400 over
// HTTP/1.1; over HTTP/2 Envoy closes the connection and nginx answers 400 (lineRow). An absolute target's host is the
// Host the service receives however many spaces come around it, and a tab after the method is answered 400 by nginx
// (lineRow). TE in any letter case reaches the service as trailers when Connection names te, and an empty element in
// Connection is served. An absolute target with an empty port is answered 400 by both. A request trailer reaches the
// service through neither; nginx reads none, so it lets one whose name is outside the token characters through over
// HTTP/1.1 and ends the connection on any over HTTP/2, and it answers 400 to a folded header line and to a Host whose
// port it does not read, where Envoy serves both (lineRow). A header line with no colon, which Envoy answers 400,
// reaches the service through nginx as a header with an empty value, which nginx stores as it stores one written with a
// colon (lineRow). nginx answers 400 to a header it reads written with _ for -, but not to x_forwarded_for, which it
// does not read, nor to a name every JavaScript object holds, such as constructor or __proto__, which proxyfile.js must
// not find in its tables; and to a second line of X-Forwarded-For, of a header of one value such as Content-Type,
// User-Agent or Referer, or of a header such as If-Match, which Envoy joins into one line with a comma and serves; a
// second Accept line, which Envoy joins too, reaches the service through nginx as a line of its own (lineRow). Each
// of 32 requests that carry no x-request-id, sent one after the other, gets one the proxy makes, a version 4 UUID, as
// Envoy makes it, which the answer carries too. A request line whose version is not one digit, a dot and one digit,
// such as HTTP/1.10 or HTTP/1.01, is answered 400, where nginx alone would serve it as HTTP/1.1.
var routing = scenario{name: "routing",
	exchanges: []exchange{
		{name: "a pattern route", keys: []string{"routes[].pattern", "routes[].to", "services.<service name>.http",
			"services.<service name>.host_header", "origins.<environment name>.hosts"}, send: get("/events/abc-123"),
			want: answer{status: 200, service: "accounts", port: "7070", method: "GET", path: "/events/abc-123", host: "127.0.0.1:7070"}},
		{name: "a pattern miss falls to the prefix route", keys: []string{"routes[].pattern", "routes[].prefix", "services.<service name>.addresses"},
			send: get("/events/ABC"),
			want: answer{status: 200, service: "shop", port: "8080", path: "/events/ABC", host: "localhost"}},
		{name: "a path and method route with a rewrite", keys: []string{"routes[].path", "routes[].method", "routes[].rewrite",
			"services.<service name>.host_header"}, send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/api/webhooks"},
			want: answer{status: 200, service: "accounts", port: "7070", method: "POST", path: "/hooks/payments", host: "127.0.0.1:7070"}},
		{name: "another method falls to the prefix route", keys: []string{"routes[].path", "routes[].method", "routes[].prefix"}, send: get("/api/webhooks"),
			want: answer{status: 200, service: "shop", port: "8080", path: "/api/webhooks"}},
		{name: "a service found by DNS", keys: []string{"services.<service name>.address", "dns.resolver"}, send: get("/catalog/items?id=7"),
			want: answer{status: 200, service: "catalog", port: "8090", path: "/catalog/items?id=7", host: "localhost"}},
		{name: "a service at its one address", keys: []string{"services.<service name>.address"}, send: get("/catalog/items?id=8"),
			want: answer{status: 200, service: "catalog", port: "8090", path: "/catalog/items?id=8"}},
		{name: "the prefix route", keys: []string{"routes[].prefix", "routes[].to", "services.<service name>.http"}, send: get("/products/1"),
			want: answer{status: 200, service: "shop", port: "8080", proto: "HTTP/1.1", method: "GET", path: "/products/1", host: "localhost"}},
		{name: "over HTTP 2 the service still hears HTTP 1.1", keys: []string{"routes[].prefix"}, send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1"},
			want: answer{status: 200, service: "shop", proto: "HTTP/1.1", path: "/products/1"}},
		{name: "another environment's host", keys: []string{"origins.<environment name>.hosts"}, send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1"},
			want: answer{status: 200, service: "shop", host: "api.example.com"}},
		// measured on Envoy 1.40.2: 404 with an empty body, no Content-Type, vary: Origin and a request id it makes
		{name: "a host no environment names", keys: []string{"origins.<environment name>.hosts"}, send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/1"},
			want: answer{status: 404, headers: map[string]string{"content-type": "", "vary": "Origin"}, made: map[string]*regexp.Regexp{"x-request-id": uuid4}}},
		{name: "a path matched by case", keys: []string{"routes[].path", "routes[].method", "routes[].prefix"},
			send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/API/webhooks"},
			want: answer{status: 200, service: "shop", path: "/API/webhooks"}},
		{name: "the check's path is routed as any other", keys: []string{"routes[].prefix"}, send: get("/_pf_auth"),
			want: answer{status: 200, service: "shop", path: "/_pf_auth"}},
		{name: "the check's path with an escaped underscore", keys: []string{"routes[].prefix"}, send: get("/%5Fpf_auth"),
			want: answer{status: 200, service: "shop", path: "/_pf_auth"}},
		{name: "a newline at the end of a path a pattern takes", keys: []string{"routes[].pattern"}, send: get("/events/abc%0A"),
			want: answer{status: 200, service: "shop", path: "/events/abc%0A"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a newline inside a path a pattern of any character takes", keys: []string{"routes[].pattern"}, send: get("/admin/a%0Ab"),
			want: answer{status: 200, service: "shop", path: "/admin/a%0Ab"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/admin/a%0Ab"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/admin/a%0Ab"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/admin/a%0Ab"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/admin/a%0Ab"}}},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an upgrade other than WebSocket is refused", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"upgrade": "spdy/3.1", "connection": "upgrade"}}, want: answer{status: 403, headers: map[string]string{"content-type": ""}}},
		{name: "an h2c upgrade is served as a plain request", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"upgrade": "h2c", "connection": "Upgrade, HTTP2-Settings", "http2-settings": "AAMAAABkAAQAAP__"}},
			want: answer{status: 200, service: "shop", absent: []string{"upgrade", "http2-settings"}},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "the scheme the request came in on", send: get("/products/1"), want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "http"}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"portable":     {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"haproxy-file": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"nginx-file":   {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"nginx-auth":   {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}}}},
		{name: "a client's own x-forwarded-proto is replaced by the connection's", keys: []string{"client_address", "tls.certificate"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-forwarded-proto": "http"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
		{name: "a client's own x-envoy-external-address and x-request-id are replaced by the connection's", keys: []string{"client_address"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-envoy-external-address": "203.0.113.9", "x-request-id": "client-id-1"}},
			want: answer{status: 200, service: "shop", replaced: map[string]string{"x-envoy-external-address": "203.0.113.9", "x-request-id": "client-id-1"}}},
		{name: "the client's X-Forwarded-For and the peer's address reach the service as one line", keys: []string{"client_address"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-forwarded-for": "198.51.100.7"}},
			want: answer{status: 200, service: "shop", oneLine: []string{"x-forwarded-for"}}},
		{name: "the x-envoy headers Envoy removes from a client it faces do not reach the service", keys: []string{"client_address"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: facedHeaders()},
			want: answer{status: 200, service: "shop", absent: slices.Sorted(maps.Keys(facedHeaders()))}},
		{name: "two X-Forwarded-For lines of the client's and the peer's address reach the service as one line", keys: []string{"client_address"},
			send: rawHeaders(map[string]string{"x-forwarded-for": "198.51.100.7\r\nx-forwarded-for: 198.51.100.8"}),
			want: answer{status: 200, service: "shop", oneLine: []string{"x-forwarded-for"}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header nginx reads, written with underscores", send: rawHeaders(map[string]string{"x_forwarded_proto": "https"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x_forwarded_proto": "https"}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header named constructor reaches the service", send: rawHeaders(map[string]string{"constructor": "x"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"constructor": "x"}}},
		{name: "a header named __proto__ reaches the service", send: rawHeaders(map[string]string{"__proto__": "x"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"__proto__": "x"}}},
		{name: "a header named constructor reaches the service over HTTP/2", send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"constructor": "x"}, raw: true},
			want: answer{status: 200, service: "shop", received: map[string]string{"constructor": "x"}}},
		{name: "x_forwarded_for, which nginx does not read as its X-Forwarded-For, reaches the service",
			send: rawHeaders(map[string]string{"x_forwarded_for": "192.0.2.1"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x_forwarded_for": "192.0.2.1"}}},
		{name: "a request with no x-request-id gets a version 4 UUID, which the answer carries", send: get("/products/1"),
			repeat: 32, every: true, want: answer{status: 200, service: "shop", forms: map[string]*regexp.Regexp{"x-request-id": uuid4}}},
		{name: "two Accept lines", send: rawHeaders(map[string]string{"accept": "a=1\r\naccept: b=2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"accept": "a=1,b=2"}},
			by: map[string]own{"nginx": {difference: lineRow,
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"accept": "a=1\nb=2"}}}}}},
		{name: "two Content-Type lines", send: rawHeaders(map[string]string{"content-type": "a=1\r\ncontent-type: b=2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"content-type": "a=1,b=2"}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "two User-Agent lines", send: rawHeaders(map[string]string{"user-agent": "a=1\r\nuser-agent: b=2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"user-agent": "a=1,b=2"}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "two Referer lines", send: rawHeaders(map[string]string{"referer": "a=1\r\nreferer: b=2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"referer": "a=1,b=2"}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "two If-Match lines", send: rawHeaders(map[string]string{"if-match": "\"a\"\r\nif-match: \"b\""}),
			want: answer{status: 200, service: "shop", received: map[string]string{"if-match": `"a","b"`}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		// The 502 carries no Content-Type, as the Envoy output's carries none (measured on Envoy 1.40.2).
		{name: "a service that refuses connections is answered 502", send: get("/catalog/items"), refusing: "catalog",
			want: answer{status: 502, headers: map[string]string{"content-type": ""}}},
		// The hold backend takes no connection, and only nginx-plain names it (its hold service): the Envoy output answers
		// 502 with no body when the cluster's 5 s connect timeout ends the try (measured on Envoy 1.40.2, 5.0 s), 50 ms
		// under for the two clocks and 1 s over for a loaded Mac, as the auth timeout row. want is what every file that
		// names no hold service answers, the path reaching the shop route, so a file added to the battery needs no entry
		// here unless it names hold.
		{name: "a service that takes no connection within 5 s is answered 502", overlap: true, send: get("/hold/x"),
			want: answer{status: 200, service: "shop", path: "/hold/x"},
			on: map[string]literal{"nginx-plain": {want: answer{status: 502, atLeast: 4950 * time.Millisecond,
				below: 5950 * time.Millisecond}}}},
		{name: "a service's X-Accel-Redirect is passed on, not followed", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/catalog/items", headers: map[string]string{"x-stub-accel-redirect": "/products/1"}},
			want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "a service's Keep-Alive does not reach the client", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"x-stub-keep-alive": "timeout=5"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"keep-alive": ""}}},
		{name: "the headers Envoy removes do not reach the service", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: removedHeaders()}, want: answer{status: 200, service: "shop", absent: slices.Sorted(maps.Keys(removedHeaders()))}},
		{name: "the host of an absolute request line is the Host the service receives", send: request{proto: "http/1.1", method: "GET",
			host: "other.internal", path: "http://localhost/products/1"}, want: answer{status: 200, service: "shop", host: "localhost"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an Upgrade that Connection does not name is removed", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"upgrade": "websocket"}}, want: answer{status: 200, service: "shop", absent: []string{"upgrade"}}},
		{name: "header names holding _ and . reach the service", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"x_user_id": "7", "x.dot": "1"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x_user_id": "7", "x.dot": "1"}}},
		{name: "TE reaches the service as trailers", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"te": "gzip, trailers"}}, want: answer{status: 200, service: "shop", received: map[string]string{"te": "trailers"}}},
		{name: "a host with a dot at its end is on no environment", send: request{proto: "http/1.1", method: "GET", host: "localhost.",
			path: "/products/1"}, want: answer{status: 404, headers: map[string]string{"content-type": ""}}},
		// Envoy 1.40.2 sends no x-request-id on its 400s to what it refuses before it reads the request through, nor on
		// its 426, and one on its 404 to a host no environment names (measured).
		{name: "a method Envoy's parser does not know is answered 400", send: request{proto: "http/1.1", method: "FOO", host: "localhost",
			path: "/products/1"}, want: answer{status: 400, headers: map[string]string{"x-request-id": ""}}},
		// Envoy 1.40.2 closes the connection after each 400 of what its parser refuses, a second after it, and answers no
		// request sent after it on that connection (measured), and so does HAProxy after these three.
		{name: "after the 400 of a byte above 0x7F in the target the connection is closed",
			send: thenAnother(request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/\xc3\xa9"}), want: answer{refused: true}},
		{name: "after the 400 of a header name holding @ the connection is closed", send: thenAnother(rawHeader("http/1.1", "x@at")),
			want: answer{refused: true}},
		{name: "after the 400 of HTTP/1.10 the connection is closed", send: thenAnother(rawLine("GET /products/1 HTTP/1.10", "localhost")),
			want: answer{refused: true}},
		// Envoy 1.40.2 closes the connection after its 426 to a version it does not serve and after its 403 to an upgrade
		// other than WebSocket, from any origin, and answers no request sent after it (measured), and so does nginx.
		// HAProxy 3.2.26 and 3.4.7 keep it and serve the request after the body (lineRow).
		{name: "after the 426 of HTTP/1.9 the connection is closed", send: thenAnother(rawLine("GET /products/1 HTTP/1.9", "localhost")),
			want: answer{refused: true}, by: map[string]own{"haproxy": {difference: lineRow, literal: keptAfter(426)}}},
		{name: "after the 403 of an upgrade other than WebSocket the connection is closed", send: thenAnother(rawHeaders(map[string]string{
			"upgrade": "spdy/3.1", "connection": "upgrade"})), want: answer{refused: true},
			by: map[string]own{"haproxy": {difference: lineRow, literal: keptAfter(403)}}},
		{name: "after the 403 of an upgrade other than WebSocket from a foreign origin the connection is closed",
			send: thenAnother(rawHeaders(map[string]string{"upgrade": "spdy/3.1", "connection": "upgrade", "origin": "https://evil.example.net"})),
			want: answer{refused: true}, by: map[string]own{"haproxy": {difference: lineRow, literal: keptAfter(403)}}},
		// Envoy 1.40.2 answers 400 to a QUERY whose Content-Type is missing or empty, over HTTP/1.1 and HTTP/2, before it
		// picks a route, and serves a QUERY with one (measured). A Content-Type of spaces alone, or of a tab after the
		// space the battery writes after the colon, is empty over HTTP/1.1, whose parser drops the spaces and tabs around
		// a value, where nginx keeps a tab, and a value over HTTP/2, which keeps them; HAProxy reads spaces alone as
		// empty over HTTP/2 too (lineRow), and a tab over HTTP/1.1 as Envoy does (measured on HAProxy 3.2.26 and 3.4.7).
		{name: "a QUERY with a Content-Type is served", send: request{proto: "http/1.1", method: "QUERY", host: "localhost",
			path: "/products/1", headers: map[string]string{"content-type": "application/json"}}, want: answer{status: 200, service: "shop"}},
		{name: "a QUERY with a Content-Type is served over HTTP 2", send: request{proto: "h2", method: "QUERY", host: "localhost",
			path: "/products/1", headers: map[string]string{"content-type": "application/json"}}, want: answer{status: 200, service: "shop"}},
		{name: "a QUERY with a Content-Type of spaces is answered 400", send: request{proto: "http/1.1", method: "QUERY", host: "localhost",
			path: "/products/1", headers: map[string]string{"content-type": "   "}, raw: true}, want: answer{status: 400}},
		{name: "a QUERY with a Content-Type of a tab is answered 400", send: request{proto: "http/1.1", method: "QUERY", host: "localhost",
			path: "/products/1", headers: map[string]string{"content-type": "\t"}, raw: true}, want: answer{status: 400}},
		{name: "a QUERY with a Content-Type of spaces is served over HTTP 2", send: request{proto: "h2", method: "QUERY", host: "localhost",
			path: "/products/1", headers: map[string]string{"content-type": "   "}, raw: true}, want: answer{status: 200, service: "shop"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		// Envoy 1.40.2 answers it with no body and no Content-Type, whatever the host, where it answers what its parser
		// refuses 400 Bad Request as text/plain (measured).
		{name: "a QUERY with no Content-Type is answered 400", send: request{proto: "http/1.1", method: "QUERY", host: "localhost",
			path: "/products/1"}, want: answer{status: 400, headers: map[string]string{"content-type": "", "x-request-id": ""}}},
		{name: "a QUERY with no Content-Type to a host no environment names is answered 400", send: request{proto: "http/1.1",
			method: "QUERY", host: "other.example.net", path: "/products/1"},
			want: answer{status: 400, headers: map[string]string{"content-type": "", "vary": "Origin", "x-request-id": ""}}},
		{name: "a QUERY with no Content-Type is answered 400 over HTTP 2", send: request{proto: "h2", method: "QUERY", host: "localhost",
			path: "/products/1"}, want: answer{status: 400, headers: map[string]string{"content-type": ""}}},
		{name: "a pattern's {,3} is text, as RE2 reads it", keys: []string{"routes[].pattern"}, send: get("/braces/xxx"),
			want: answer{status: 200, service: "shop", path: "/braces/xxx"}},
		{name: "a path PCRE2 gives up on as the pattern is written is matched as Go's parse writes it", keys: []string{"routes[].pattern"},
			send: get("/backtrack/" + strings.Repeat("a", 40) + "b"), want: answer{status: 200, service: "shop"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts"}}, "nginx-auth": {want: answer{status: 200, service: "accounts"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts"}}}},
		// nginx's answer comes within 50 ms, about ten times Envoy's slowest answer to a long path or a long Origin
		// (5.8 ms): under the match limit the generator writes, 1.6 to 3.6 ms; under PCRE2's own, 112 to 227 ms (nginx
		// 1.30.6 and 1.31.8 on an arm64 Mac), a worker serving no other request meanwhile.
		{name: "a path PCRE2 gives up on is refused", keys: []string{"routes[].pattern"}, send: get("/give-up/" + strings.Repeat("a", 40) + "b"),
			want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: nginxYAMLFiles,
				literal: literal{want: answer{status: 400, below: 50 * time.Millisecond}}}}},
		{name: "a header name of token characters, sent by hand", send: rawHeader("http/1.1", "x-ok"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-ok": "1"}}},
		{name: "a header name holding @ is answered 400", send: rawHeader("http/1.1", "x@at"), want: answer{status: 400}},
		{name: "a header name holding a parenthesis is answered 400", send: rawHeader("http/1.1", "x(p)"), want: answer{status: 400}},
		{name: "a header name holding the byte 0xFF is answered 400", send: rawHeader("http/1.1", "x\xffy"), want: answer{status: 400}},
		// The battery writes these HTTP/2 requests by hand, so a request it writes by hand with a name of token
		// characters must reach the service, or a fault of its own writing would pass the rows that want a refusal.
		{name: "a header name of token characters over HTTP 2, sent by hand", send: rawHeader("h2", "x-ok"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-ok": "1"}}},
		// Envoy closes the connection; nginx answers 400 (lineRow).
		{name: "a header name holding @ over HTTP 2 is refused", overlap: true, send: rawHeader("h2", "x@at"), want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header name holding a parenthesis over HTTP 2 is refused", overlap: true, send: rawHeader("h2", "x(p)"), want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header name holding the byte 0xFF over HTTP 2 is refused", overlap: true, send: rawHeader("h2", "x\xffy"), want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "header names of token characters reach the service", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: tokenNames}, want: answer{status: 200, service: "shop", received: tokenNames}},
		{name: "header names of token characters reach the service over HTTP 2", send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/1", headers: tokenNames}, want: answer{status: 200, service: "shop", received: tokenNames}},
		{name: "two spaces before an absolute target leave its host the Host the service receives",
			send: rawLine("GET  http://localhost/products/1 HTTP/1.1", "other.internal"), want: answer{status: 200, service: "shop", host: "localhost"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "three spaces around an absolute target leave its host the Host the service receives",
			send: rawLine("GET   http://localhost/products/1   HTTP/1.1", "other.internal"), want: answer{status: 200, service: "shop", host: "localhost"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "two spaces before an absolute target whose host ends with a dot", send: rawLine("GET  http://localhost./products/1 HTTP/1.1", "localhost"),
			want: answer{status: 404}, by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a tab after the method", send: rawLine("GET\t/products/1 HTTP/1.1", "localhost"), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an absolute target whose host a query follows is refused", send: get("http://localhost?x=1"), want: answer{refused: true}},
		{name: "TE in capitals reaches the service as trailers when Connection names te", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"te": "Trailers", "connection": "te"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"te": "trailers"}}},
		{name: "TE in capitals does not reach the service when Connection does not name te", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"te": "Trailers"}},
			want: answer{status: 200, service: "shop", absent: []string{"te"}}},
		{name: "an empty element in Connection", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"connection": "close,"}}, want: answer{status: 200, service: "shop"}},
		// Envoy answers an absolute target of any scheme but http and https, in any letter case, 400 with the body Bad
		// Request, as it answers what its parser refuses; the tracing rows hold the same line with HTTP/1.0.
		{name: "an absolute target of another scheme than http and https", send: rawLine("GET ftp://localhost/products/1 HTTP/1.1", "localhost"),
			want: answer{status: 400, headers: map[string]string{"x-request-id": "", "content-type": "text/plain"}},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400, headers: map[string]string{"x-request-id": ""}}}}}},
		{name: "an absolute target with an empty port", send: rawLine("GET http://localhost:/products/1 HTTP/1.1", "localhost"),
			want: answer{status: 400}},
		// nginx serves a request Envoy refuses here, and the service receives the body and no trailer either way: nginx
		// reads no trailer, so it can refuse none (lineRow). HAProxy serves a trailer value holding a control byte too,
		// over HTTP/1.1 and HTTP/2 (its less strict way, lineRow); over HTTP/2 nginx ends the connection on any trailer.
		{name: "a trailer whose name holds @", send: chunked("http/1.1", "x@at"), want: answer{status: 400},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}}}}},
		{name: "a trailer value holding a control character", send: request{proto: "http/1.1", method: "POST", host: "localhost",
			path: "/products/1", raw: true, body: "abc", trailers: map[string]string{"x-t": "b\x01c"}}, want: answer{status: 400},
			by: map[string]own{
				"nginx":   {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}}}}},
		{name: "a trailer value holding a control character over HTTP/2", overlap: true, send: request{proto: "h2", method: "POST", host: "localhost",
			path: "/products/1", raw: true, body: "abc", trailers: map[string]string{"x-t": "b\x01c"}}, want: answer{refused: true},
			by: map[string]own{
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}}}}},
		{name: "a trailer reaches the service through no proxy", send: chunked("http/1.1", "x-t"),
			want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}},
		{name: "a trailer over HTTP 2", send: chunked("h2", "x-t"), want: answer{status: 200, service: "shop", body: "abc", noTrailers: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{refused: true}}}}},
		// A header value holding a control byte other than a tab is answered 400 over HTTP/1.1 and refused over HTTP/2,
		// where Envoy closes the connection, HAProxy resets the stream and nginx answers 400. A tab and UTF-8 reach the
		// service through every proxy.
		{name: "a header value holding a control character is answered 400", send: rawHeaders(map[string]string{"x-a": "b\x01c"}),
			want: answer{status: 400}},
		{name: "a header value holding a control character is refused over HTTP/2", overlap: true, send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"x-a": "b\x7fc"}, raw: true},
			want: answer{refused: true}},
		{name: "a header value holding a tab and UTF-8 reaches the service", send: rawHeaders(map[string]string{"x-a": "b\tc\u00e9d"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-a": "b\tc\u00e9d"}}},
		{name: "a header value holding a tab and UTF-8 reaches the service over HTTP/2", send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"x-a": "b\tc\u00e9d"}, raw: true},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-a": "b\tc\u00e9d"}}},
		{name: "a header line with no colon", send: rawHeaders(map[string]string{"x-a": "1\r\nnocolon"}), want: answer{status: 400},
			by: map[string]own{"nginx": {difference: lineRow,
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-a": "1"}, oneLine: []string{"nocolon"}}}}}},
		{name: "a header line folded with a space", send: rawHeaders(map[string]string{"x-a": "1\r\n x@at: 2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-a": "1 x@at: 2"}},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header line folded with a tab", send: rawHeaders(map[string]string{"x-a": "1\r\n\tx@at: 2"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-a": "1\tx@at: 2"}},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a Host whose port is past 65535", send: rawLine("GET /products/1 HTTP/1.1", "localhost:99999"),
			want: answer{status: 200, service: "shop", host: "localhost:99999"},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a Host whose port has a sign", send: rawLine("GET /products/1 HTTP/1.1", "localhost:+1"),
			want: answer{status: 200, service: "shop", host: "localhost:+1"},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		// An environment's host written with a dot at its start or its end is compared as written; only haproxy-file has
		// such hosts.
		{name: "a host written with a dot at its start is an environment's host", send: rawLine("GET /products/1 HTTP/1.1", ".dot.example.com"),
			want: answer{status: 404}, on: map[string]literal{"haproxy-file": {want: answer{status: 200, service: "shop", host: ".dot.example.com"}}}},
		{name: "a host written with a dot at its end is an environment's host", send: rawLine("GET /products/1 HTTP/1.1", "dot.example.com."),
			want: answer{status: 404}, on: map[string]literal{"haproxy-file": {want: answer{status: 200, service: "shop", host: "dot.example.com."}}}},
		// The reader admits a host that starts with -; only haproxy-file has such hosts, which HAProxy read as an acl
		// line's flags before each value came after --: -f stopped the file loading, and -n and -i matched no host.
		{name: "a host written -f is an environment's host", send: rawLine("GET /products/1 HTTP/1.1", "-f"),
			want: answer{status: 404}, on: map[string]literal{"haproxy-file": {want: answer{status: 200, service: "shop", host: "-f"}}}},
		{name: "a host written -i is an environment's host", send: rawLine("GET /products/1 HTTP/1.1", "-i"),
			want: answer{status: 404}, on: map[string]literal{"haproxy-file": {want: answer{status: 200, service: "shop", host: "-i"}}}},
		{name: "a host written -n is an environment's host", send: rawLine("GET /products/1 HTTP/1.1", "-n"),
			want: answer{status: 404}, on: map[string]literal{"haproxy-file": {want: answer{status: 200, service: "shop", host: "-n"}}}},
		// Envoy admits 100 request header lines, Host among them, and refuses one more with 431; the files that write
		// limits.request_headers admit 150.
		{name: "100 header lines reach the service", send: rawHeaders(numbered(99)), want: answer{status: 200, service: "shop"}},
		{name: "101 header lines are refused", send: rawHeaders(numbered(100)), want: answer{refused: true},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		// Over HTTP/2 Envoy counts the 4 pseudo-headers of a request among its 100. nginx counts :authority whether or not
		// the client sent it, as it cannot tell :authority from a host field (limits.request_headers.count), so on the
		// files that hold 100 fields it refuses this request, one field short of Envoy's count; examples/portable writes
		// 150, and both serve it there.
		{name: "96 header fields over HTTP 2 reach the service", send: rawFields(numbered(96)), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "limits.request_headers.count", files: nginxFiles,
				literal: literal{want: answer{refused: true}}}}},
		// With limits.request_headers Envoy counts the whole head of an HTTP/1.1 request, its request line, each header
		// line with its CRLF and the empty line: it serves a head of exactly the size, 61,440 bytes for the battery
		// file's 60 KiB, and answers 431 one byte past, whether a header line or the request line holds it. nginx reads
		// a head of 61,376 bytes at most, 64 bytes under, so the rows 64 bytes under hold for both, and past the size it
		// answers 400, or 414 when the request line is the long one (limits.request_headers.size). Over HTTP/2 Envoy
		// resets the stream of a request with more fields than the count, its 4 pseudo-headers among them. examples/full
		// and examples/portable write 96 KiB and 150 fields, so Envoy serves each of these there. nginx holds
		// examples/portable's 96 KiB in two buffers of 49,120 bytes and refuses a line longer than one, 400 for a header
		// line and 414 for the request line (limits.request_headers.size), so it refuses the rows' long lines there.
		{name: "97 header fields over HTTP 2 are refused", keys: []string{"limits.request_headers.count"}, send: rawFields(numbered(97)),
			want: answer{refused: true},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		// :authority and a host field of the same value are two fields as Envoy counts them, so 96 fields beside them
		// are 101.
		{name: "96 header fields over HTTP 2 beside :authority and a host field are refused", keys: []string{"limits.request_headers.count"},
			send: rawFields(withHeader(numbered(96), "host", "localhost")), want: answer{refused: true},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		{name: "a header block 64 bytes under the size", keys: []string{"limits.request_headers.size"},
			send: rawHeaders(map[string]string{"x-big": strings.Repeat("a", 61322)}), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 400}}}}},
		{name: "a header block one byte over the size", keys: []string{"limits.request_headers.size"},
			send: rawHeaders(map[string]string{"x-big": strings.Repeat("a", 61387)}), want: answer{refused: true},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 400}}}}},
		{name: "a request line 64 bytes under the size", keys: []string{"limits.request_headers.size"},
			send: rawLine("GET /products/"+strings.Repeat("a", 61332)+" HTTP/1.1", "localhost"), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 414}}}}},
		{name: "a request line one byte over the size", keys: []string{"limits.request_headers.size"},
			send: rawLine("GET /products/"+strings.Repeat("a", 61397)+" HTTP/1.1", "localhost"), want: answer{status: 431},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", literal: literal{want: answer{status: 414}}}}},
		{name: "HTTP/1.0 is answered 426", send: rawLine("GET /products/1 HTTP/1.0", "localhost"),
			want: answer{status: 426, headers: map[string]string{"x-request-id": ""}}},
		// Envoy answers 426 to HTTP/1.2 to HTTP/1.9 as to HTTP/1.0, and so do nginx and HAProxy.
		{name: "HTTP/1.2 is answered 426", send: rawLine("GET /products/1 HTTP/1.2", "localhost"), want: answer{status: 426}},
		{name: "HTTP/1.9 is answered 426", send: rawLine("GET /products/1 HTTP/1.9", "localhost"), want: answer{status: 426}},
		// Envoy answers 426 to every HTTP/1 version but 1.1, HTTP/0.9 with headers and HTTP/2.0 sent on an HTTP/1
		// connection among them, where nginx answers the first 400 and the second 505 (lineRow). A request line with no
		// version, HTTP/0.9's own form, is answered 426 by Envoy and 400 by HAProxy, and nginx answers in that form, the
		// text Bad Request alone with no status line, which no HTTP/1.1 client reads as an answer (lineRow).
		{name: "HTTP/0.9 with headers is answered 426", send: rawLine("GET /products/1 HTTP/0.9", "localhost"), want: answer{status: 426},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "HTTP/2.0 on an HTTP/1 connection is answered 426", send: rawLine("GET /products/1 HTTP/2.0", "localhost"),
			want: answer{status: 426}, by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 505}}}}},
		{name: "a request line with no version is answered 426", send: rawLine("GET /products/1", "localhost"), want: answer{status: 426},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{refused: true}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "HTTP/1.10 is answered 400", send: rawLine("GET /products/1 HTTP/1.10", "localhost"), want: answer{status: 400}},
		{name: "HTTP/1.01 is answered 400", send: rawLine("GET /products/1 HTTP/1.01", "localhost"), want: answer{status: 400}},
		{name: "a method in lower case is answered 400", send: rawLine("get /products/1 HTTP/1.1", "localhost"), want: answer{status: 400}},
		// Envoy removes the header a Connection token names; nginx and HAProxy refuse the request (lineRow).
		{name: "a header a Connection token names does not reach the service", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"connection": "x-hop", "x-hop": "1"}},
			want: answer{status: 200, service: "shop", absent: []string{"connection", "x-hop"}},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "Keep-Alive does not reach the service", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"connection": "keep-alive", "keep-alive": "timeout=5"}},
			want: answer{status: 200, service: "shop", absent: []string{"connection", "keep-alive"}}},
		// Envoy keeps HTTP2-Settings on a request that is no h2c upgrade, and so does HAProxy, which refuses an h2c
		// upgrade by the Connection token it keeps (lineRow).
		{name: "HTTP2-Settings without an upgrade reaches the service", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"http2-settings": "AAMAAABkAAQAAP__"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"http2-settings": "AAMAAABkAAQAAP__"}}},
		// Envoy 1.40.2 answers ten Connection tokens or more 400 and serves nine, counting an element of white space
		// but not an empty one, and so does nginx. HAProxy counts an empty element but the last, so it refuses a token
		// and ten empty elements, and does not count close or keep-alive, which it removes before any rule reads the
		// header, so it serves ten of them (lineRow; measured on Envoy 1.40.2, nginx 1.30 and 1.31, and HAProxy 3.2.26
		// and 3.4.7).
		{name: "ten Connection tokens are answered 400", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"connection": "te, te, te, te, te, te, te, te, te, te", "te": "trailers"}},
			want: answer{status: 400}},
		{name: "nine Connection tokens are served", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"connection": "te, te, te, te, te, te, te, te, te", "te": "trailers"}},
			want: answer{status: 200, service: "shop"}},
		{name: "a Connection token and ten empty elements are served", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"connection": "te,,,,,,,,,,", "te": "trailers"}},
			want: answer{status: 200, service: "shop"},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a Connection token and nine elements of white space are answered 400", send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"connection": "te, , , , , , , , , ,", "te": "trailers"}},
			want: answer{status: 400}},
		{name: "ten Connection tokens close are answered 400", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"connection": "close, close, close, close, close, close, close, close, close, close"}},
			want: answer{status: 400},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}}}},
		// A WebSocket over HTTP/2 (RFC 8441's extended CONNECT), sent by hand: Envoy 1.40.2 closes the connection, and
		// HAProxy 3.2.26 sent it to the service as an HTTP/1.1 upgrade before the file answered it 400.
		{name: "a WebSocket over HTTP 2 is refused", overlap: true, send: request{proto: "h2", method: "CONNECT", host: "localhost", path: "/products/1",
			headers: map[string]string{":protocol": "websocket", "sec-websocket-version": "13"}, raw: true},
			want: answer{refused: true}},
		// HAProxy's parsers read each of the requests below before any rule of its file, which can then refuse none of
		// them, and nginx answers some of them in its own way (lineRow; measured on Envoy 1.40.2, nginx 1.30 and 1.31,
		// and HAProxy 3.2.26 and 3.4.7). An :authority with an empty port is sent by hand, because net/http drops the
		// colon.
		{name: "an HTTP 2 authority with an empty port reaches the service as sent", send: request{proto: "h2", method: "GET",
			host: "localhost:", path: "/products/1", raw: true}, want: answer{status: 200, service: "shop", host: "localhost:"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", host: "localhost"}}}}},
		{name: "two Host lines of one host are answered 404", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"Host": "localhost"}, raw: true}, want: answer{status: 404},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", host: "localhost"}}},
				"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "two Content-Length lines of one value are answered 400", send: request{proto: "http/1.1", method: "POST",
			host: "localhost", path: "/products/1", headers: map[string]string{"Content-Length": "3", "content-length": "3"},
			body: "abc", unframed: true, raw: true}, want: answer{status: 400},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc"}}}}},
		{name: "Content-Length beside Transfer-Encoding chunked is answered 400", send: request{proto: "http/1.1", method: "POST",
			host: "localhost", path: "/products/1", headers: map[string]string{"Content-Length": "3", "Transfer-Encoding": "chunked"},
			body: "3\r\nabc\r\n0\r\n\r\n", unframed: true, raw: true}, want: answer{status: 400},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc"}}}}},
		{name: "Transfer-Encoding gzip and chunked is answered 501", send: request{proto: "http/1.1", method: "POST",
			host: "localhost", path: "/products/1", headers: map[string]string{"Transfer-Encoding": "gzip, chunked"},
			body: "3\r\nabc\r\n0\r\n\r\n", unframed: true, raw: true}, want: answer{status: 501},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 422}}}}},
		{name: "a POST with no body reaches the service with content-length 0", send: request{proto: "http/1.1", method: "POST",
			host: "localhost", path: "/products/1", raw: true}, want: answer{status: 200, service: "shop", received: map[string]string{"content-length": "0"}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", absent: []string{"content-length"}}}},
				"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", absent: []string{"content-length"}}}}}},
		// The stub answers a WebSocket upgrade 200 with its echo, as a service that does not switch: Envoy passes the
		// status with no body, so no stub reads as having answered.
		{name: "a WebSocket upgrade a service answers 200 reaches the client with no body", send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"connection": "Upgrade",
				"upgrade": "websocket", "sec-websocket-key": "dGhlIHNhbXBsZSBub25jZQ==", "sec-websocket-version": "13"}, raw: true},
			want: answer{status: 200},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}},
				"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}}}},
		{name: "a header of 20,000 bytes reaches the service", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"x-big": strings.Repeat("a", 20000)}}, want: answer{status: 200, service: "shop"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 431}}}}},
		{name: "a path of 20,000 bytes reaches the service", send: get("/products/" + strings.Repeat("a", 20000)),
			want: answer{status: 200, service: "shop"},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 414}}}}},
		// HAProxy writes each byte above 0x7F as its escape, three bytes, which outgrows its buffer here: HAProxy 3.4.7
		// answers 400, and HAProxy 3.2.26 500.
		{name: "an HTTP 2 path of 7,000 bytes above 0x7F reaches the service", send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/" + strings.Repeat("\xe9", 7000), raw: true}, want: answer{status: 200, service: "shop"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{refused: true}}},
				"nginx": {difference: "routes[].pattern", files: patternFiles, literal: literal{want: answer{status: 400}}}}},
		// A proxy gives a client that closes its side of the connection after its request no answer when it reads that
		// FIN before the service answers, and the service's answer when the answer comes first: with a service that
		// answers at once, a FIN sent with the request got no answer in 300 of 300 tries through Envoy 1.40.2 and nginx
		// 1.30.6 and 1.31.8, and one sent 1 ms later got the service's 200 in 297 and 300 of 300 (measured from the
		// host, and 200 µs later from inside the network, 300 of 300 each), so a service that answers at once makes
		// the row a race, which failed once in a run on nginx 1.31.8 and once on HAProxy 3.4.7. The service here holds
		// its answer, so the FIN always comes first. Envoy closes the connection with no answer, and HAProxy 3.2.26
		// and 3.4.7 answer 400 or 403, or close with no answer, which run to run is not one answer, so the row wants a
		// refusal, which reads each of them alike (HAProxy 3.2.26 without option abortonclose gave the client the
		// service's 200).
		{name: "a client that half-closes before the service answers gets no answer from it", send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", raw: true, halfClose: true,
			headers: map[string]string{"x-stub-delay": "500ms"}}, want: answer{refused: true}},
		// An X-Forwarded-Proto line that is present and empty, sent by a client in front, is answered 404 with no
		// content-type by Envoy 1.40.2 under client_address = forwarded, and by nginx and HAProxy alike; with connection
		// every proxy writes the connection's scheme in its place and serves the request.
		{name: "an empty X-Forwarded-Proto from a client in front is answered 404",
			send: rawHeaders(map[string]string{"x-forwarded-proto": ""}), want: answer{status: 404, headers: map[string]string{"content-type": ""}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"portable":     {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"nginx-file":   {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"nginx-auth":   {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"haproxy-file": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}}}},
		// An X-Forwarded-Proto of spaces alone over HTTP/2 is served by Envoy 1.40.2 under client_address = forwarded,
		// where HAProxy, whose decoder drops the spaces, answers 404 as to an empty one (client_address); with connection
		// every proxy writes the connection's scheme in its place.
		{name: "an X-Forwarded-Proto of spaces over HTTP/2", send: request{proto: "h2", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"x-forwarded-proto": "   "}, raw: true},
			want: answer{status: 200, service: "shop"},
			by:   map[string]own{"haproxy": {difference: "client_address", files: []string{"two-checks"}, literal: literal{want: answer{status: 404}}}}},
		// The first request of a connection of its own, sent by hand.
		{name: "a service that closes the connection before its answer is answered 502", send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-fail-first": "routing-close"}, raw: true},
			want: answer{status: 502}},
		// A service that closes a connection the proxy reused, before its answer, as one whose idle connection timed out
		// does: the request before it, on the client's same connection, leaves the proxy a connection to catalog, which
		// has one address, and the stub closes the next request's connection the first time its key arrives. Envoy
		// 1.40.2 and nginx answer 502 and send the request on no other connection; HAProxy, keeping its connections to
		// a service, sent it again on a new one, which the stub answered (attempt 2), over HTTP/1.1 by closing the
		// client's connection, which the client tried again, and over HTTP/2 by refusing the stream. A 502 shows the
		// request reached the service once: a second arrival is answered.
		{name: "a request before one whose reused connection the service closes", send: get("/catalog/items?id=9"),
			want: answer{status: 200, service: "catalog"}},
		{name: "a service that closes a reused connection before its answer is answered 502, the request sent once",
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/catalog/items",
				headers: map[string]string{"x-stub-fail-first": "routing-close-reused"}},
			want: answer{status: 502}},
		{name: "a request over HTTP 2 before one whose reused connection the service closes",
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/catalog/items?id=10"}, want: answer{status: 200, service: "catalog"}},
		{name: "over HTTP 2 a service that closes a reused connection before its answer is answered 502, the request sent once",
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/catalog/items",
				headers: map[string]string{"x-stub-fail-first": "routing-close-reused-h2"}},
			want: answer{status: 502}},
		// The check location's own path, which nginx's files write to ask the check, written or with its dot escaped,
		// reaches the routes as any other path does.
		{name: "the check location's own path is routed as any other", send: get("/.proxyfile/check"),
			want: answer{status: 200, service: "shop", path: "/.proxyfile/check"}},
		{name: "the check location's own path with its dot escaped is routed as any other", send: get("/%2Eproxyfile/check"),
			want: answer{status: 200, service: "shop", path: "/.proxyfile/check"}},
		// No route makes a proxy write a redirect of its own: a prefix route's path without its slash is another path.
		{name: "a prefix route's path without its slash is routed, not redirected", send: get("/catalog"),
			want: answer{status: 200, service: "shop", path: "/catalog", headers: map[string]string{"location": ""}}},
		// A client that sends a request nginx refuses itself and closes its side of the connection gets nginx's answer,
		// where Envoy closes the connection with no answer (the request line row).
		{name: "a client that half-closes after a dot segment with a ; parameter gets no answer", send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/%2e%2e;/1", raw: true, halfClose: true}, want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
	}}

// uuid4 is a version 4 UUID in lower case, the form of the request id Envoy makes (measured on Envoy 1.40.2).
var uuid4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// numbered is n headers named x-h001 onwards, each with the value 1.
func numbered(n int) map[string]string {
	headers := map[string]string{}
	for i := 1; i <= n; i++ {
		headers[fmt.Sprintf("x-h%03d", i)] = "1"
	}
	return headers
}

// withHeader is headers with one more header, name with value.
func withHeader(headers map[string]string, name, value string) map[string]string {
	headers[name] = value
	return headers
}

// rawFields is a GET to localhost over HTTP/2, sent by hand with the given header fields after the 4 pseudo-headers.
func rawFields(headers map[string]string) request {
	return request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", headers: headers, raw: true}
}

// tokenNames are header names of RFC 9110's token characters, which every proxy passes on.
var tokenNames = map[string]string{"x-ok": "1", "x_und": "2", "x.dot": "3"}

// rawHeader is a GET to localhost over proto, sent by hand with one header of the given name, which net/http would
// refuse to send.
func rawHeader(proto, name string) request {
	return request{proto: proto, method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{name: "1"}, raw: true}
}

// rawLine is a request over HTTP/1.1 sent by hand with the given request line and Host header.
func rawLine(line, host string) request {
	return request{proto: "http/1.1", host: host, line: line, raw: true}
}

// rawHeaders is a GET to localhost over HTTP/1.1 sent by hand with the given headers, whose values may hold a line
// break, which net/http refuses to send.
func rawHeaders(headers map[string]string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: headers, raw: true}
}

// fromListed is a GET of /products/1 to api.example.com over HTTP/1.1 sent by hand from https://www.example.com, an
// origin of the live environment's list, with the given headers beside its Origin.
func fromListed(headers map[string]string) request {
	all := map[string]string{"origin": "https://www.example.com"}
	maps.Copy(all, headers)
	return request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1", headers: all, raw: true}
}

// chunked is a POST to localhost over proto sent by hand with the body abc and one trailer of the given name.
func chunked(proto, trailer string) request {
	return request{proto: proto, method: "POST", host: "localhost", path: "/products/1", raw: true, body: "abc",
		trailers: map[string]string{trailer: "1"}}
}

// facedHeaders are the request headers the Envoy output removes too with client_address = connection, where it takes
// the client for one it faces, and passes on with forwarded (measured on Envoy 1.40.2 with every x-envoy- header its
// headers.h names): those only a proxy in front may send.
func facedHeaders() map[string]string {
	return map[string]string{"x-envoy-decorator-operation": "1", "x-envoy-downstream-service-cluster": "1",
		"x-envoy-downstream-service-node": "1", "x-envoy-original-host": "1", "x-envoy-original-path": "1"}
}

// removedHeaders are the request headers the Envoy output removes before the service whatever the client claims
// (measured on Envoy 1.40.2 with every x-envoy- header its headers.h names): the ones that steer an Envoy between the
// gateway and the service, Proxy-Connection and X-Forwarded-Client-Cert.
func removedHeaders() map[string]string {
	removed := map[string]string{}
	for _, name := range []string{"proxy-connection", "x-envoy-expected-rq-timeout-ms", "x-envoy-force-trace",
		"x-envoy-hedge-on-per-try-timeout", "x-envoy-internal", "x-envoy-ip-tags", "x-envoy-max-retries", "x-envoy-original-url",
		"x-envoy-retriable-header-names", "x-envoy-retriable-status-codes", "x-envoy-retry-grpc-on", "x-envoy-retry-on",
		"x-envoy-upstream-alt-stat-name", "x-envoy-upstream-rq-per-try-timeout-ms", "x-envoy-upstream-rq-timeout-alt-response",
		"x-envoy-upstream-rq-timeout-ms", "x-envoy-upstream-stream-duration-ms", "x-forwarded-client-cert"} {
		removed[name] = "1"
	}
	return removed
}

// pathRow, dotRow and lineRow are the rows of the support table that no key writes (format.WrittenWithNoKey), the
// differences nginx's own literals name.
const (
	pathRow = "the request's path normalised before a route is picked"
	dotRow  = "a dot segment with a ; parameter answered 400"
	lineRow = "the request line and header names read before a route is picked"
)

// tlsKeys are what both rows of mutual TLS depend on: a listener that asks for a client certificate, and the CA it
// must be signed by.
var tlsKeys = []string{"tls.certificate", "tls.key", "tls.clients.ca"}

var mutualTLS = scenario{name: "mutual TLS",
	exchanges: []exchange{
		// measured on Envoy 1.40.2: the handshake fails with "remote error: tls: certificate required"
		{name: "no client certificate", keys: tlsKeys, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", noCertificate: true},
			want: answer{refused: true}},
		// measured on Envoy 1.40.2: the handshake fails with "remote error: tls: unknown certificate authority". The
		// other CA carries the clients CA's name, so a proxy that matched the issuer by name would let it through.
		{name: "a client certificate another CA signed", keys: tlsKeys, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", otherCA: true},
			want: answer{refused: true}},
		// measured on Envoy 1.40.2: the handshake fails. nginx completes the handshake and answers 400, which reaches no
		// service either (tls.clients.ca).
		{name: "an expired client certificate", keys: tlsKeys, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", expired: true},
			want: answer{refused: true}},
		// A client asks for the host it sends as the TLS server name, and one that reuses its connection for another
		// host the server's certificate names sends that host with the first one's name. nginx, asking for client
		// certificates, answers a Host other than the TLS server name 421 when that name is a host of an environment
		// (tls.clients.ca), where Envoy serves it.
		{name: "a TLS server name and a Host of one environment", keys: slices.Concat(serverKeys, hostKeys),
			send: request{proto: "http/1.1", method: "GET", host: "api.example.org", path: "/products/1", serverName: "api.example.com"},
			want: answer{status: 200, service: "shop", host: "api.example.org"},
			by: map[string]own{"nginx": {difference: "tls.clients.ca", files: certificateFiles,
				literal: literal{want: answer{status: 421}}}}},
		// measured on Envoy 1.40.2, which takes TLS 1.2 and 1.3 only, of TLS 1.2 the ECDHE suites with AES-GCM or
		// ChaCha20 only, and the groups X25519, P-256 and X25519MLKEM768 only.
		{name: "a client that offers only TLS 1.1 is refused", keys: serverKeys, send: offered(tlsOffer{version: tls.VersionTLS11}),
			want: answer{refused: true}},
		{name: "a TLS 1.2 client that offers only a CBC cipher suite is refused", keys: serverKeys,
			send: offered(tlsOffer{version: tls.VersionTLS12, ciphers: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA}}),
			want: answer{refused: true}},
		{name: "a client that offers only the group P-384 is refused", keys: serverKeys,
			send: offered(tlsOffer{groups: []tls.CurveID{tls.CurveP384}}), want: answer{refused: true}},
		{name: "a client that offers only the group X25519MLKEM768 is served", keys: serverKeys,
			send: offered(tlsOffer{groups: []tls.CurveID{tls.X25519MLKEM768}}), want: answer{status: 200, service: "shop"}},
		// measured on Envoy 1.40.2 and HAProxy 3.2.26 and 3.4.7: the connection is closed with no answer, and nginx
		// 1.30.6 and 1.31.8 answer an empty 400 (tls). The request reaches no service, though it carries a token the
		// check allows, so under client_address connection a file with tls can tell the check https, the listener's
		// scheme, written as text (filter-ext-authz.yaml).
		{name: "plain HTTP sent to the TLS port", keys: serverKeys,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", raw: true, plain: true,
				headers: map[string]string{"authorization": "Bearer allow", "x-api-key": "key"}},
			want: answer{refused: true},
			by:   map[string]own{"nginx": {difference: "tls", literal: literal{want: answer{status: 400}}}}},
	}}

// serverKeys are what the rows of a client's TLS offer depend on: a listener that speaks TLS.
var serverKeys = []string{"tls.certificate", "tls.key"}

// offered is a GET of the shop route on localhost over HTTP/1.1 whose client offers offer.
func offered(offer tlsOffer) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", offer: &offer}
}

// cors: every value measured on Envoy 1.40.2. An origin on one of the environment's own hosts is admitted whether or
// not a list holds it, and only a list gives it access-control-allow-origin: https://api.example.org, the request's own
// host and port, is in neither list of the live environment, whose patterns cover api.example.com but not
// api.example.org. Envoy also answers, and no row asserts: access-control-allow-methods with the method asked for and
// access-control-allow-headers with the headers asked for, or empty, because both keys are "*";
// access-control-expose-headers on the preflights from a listed origin too; vary: Origin on every answer; and the body
// Invalid CORS request with content-type text/plain on both 403s. The preflight's 200 carries no content-type, which
// the first row asserts. The preflight from a listed origin and the request
// from another origin carry a token the check denies and one the token filter refuses, each answered 401 alone on the
// same host and path: on a file that writes the check, their 200 and 403 show that the origin is decided before the
// token and the check. Their answers do not depend on the check, so they run on a file that writes none too, such as
// HAProxy's, which then runs each key of cors. An Origin with a space after it is read without the space; a request
// from a listed origin to a service that is down is answered the proxy's own 502 with access-control-allow-origin and
// access-control-expose-headers, and one to a host no environment names 404 with neither. A request the proxy refuses
// before it routes, such as one whose method Envoy's parser does not know, carries no CORS answer either, and neither
// does one nginx refuses while it reads it, a NUL byte in a header or Content-Length beside Transfer-Encoding; HAProxy
// serves the second with the CORS answer (lineRow). A header line over 8 KiB is served with it on every file, nginx's
// included, whose header buffers read a line as long as the header limit. nginx joins two
// Access-Control-Request-Method lines, and two Access-Control-Request-Headers lines, with a comma and a space where
// Envoy joins them with a comma (cors.allow_methods, cors.allow_headers).
//
// The rows on hostKeys need no list: every file refuses a browser origin on none of the environment's hosts, so they
// run on every file, nginx's included, and send to localhost, a host of every file's local environment. An origin on
// one of its hosts, 127.0.0.1 or localhost, on either scheme and any port, is admitted; one on another environment's
// host, in capitals, with a trailing dot, with other characters for a host's dots, or null is refused, and so is a
// preflight from such an origin and a WebSocket upgrade. A preflight from an admitted origin is answered by the proxy,
// reaching no service. A present but empty Origin is refused, over HTTP/1.1 and HTTP/2. Envoy joins the Origin lines
// that are not empty, each without the spaces and tabs around it over HTTP/1.1, so an empty line beside the origin of
// the host, before or after it, and a tab before that origin change nothing, and the service receives the origin as
// one line (measured on Envoy 1.40.2, nginx 1.30.6 and 1.31.8). Envoy also answers both 403s with the body Invalid
// CORS request, which no row asserts.
var cors = scenario{name: "CORS",
	exchanges: []exchange{
		{name: "a preflight from a listed origin", keys: []string{"cors.allow_methods", "cors.allow_headers", "cors.max_age",
			"origins.<environment name>.origins"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST", "access-control-request-headers": "content-type,x-custom",
				"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-max-age": "600",
				"content-type": ""}}},
		{name: "a preflight from an origin a pattern covers", keys: []string{"cors.max_age", "origins.<environment name>.patterns"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://shop.example.com", "access-control-request-method": "PUT"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://shop.example.com", "access-control-max-age": "600"}}},
		{name: "a preflight from another origin", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://evil.example.net", "access-control-request-method": "POST"}},
			want: answer{status: 403}},
		{name: "a request from a listed origin", keys: []string{"cors.expose_headers", "origins.<environment name>.origins"}, send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-expose-headers": "Content-Disposition,X-Request-Id"}, received: map[string]string{"origin": "https://www.example.com"}}},
		{name: "a request from another origin", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns"}, send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://evil.example.net", "authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			want: answer{status: 403}},
		// The proxy's own answer takes no type from the path's ending, where nginx's types would give .html text/html.
		{name: "a request from another origin to a path ending .html", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/x.html",
				headers: map[string]string{"origin": "https://evil.example.net"}},
			want: answer{status: 403, headers: map[string]string{"content-type": "text/plain"}}},
		{name: "a request from the request's own origin that no list holds", keys: ownOriginKeys, send: request{proto: "http/1.1", method: "GET",
			host: "api.example.org", path: "/products/1", headers: map[string]string{"origin": "https://api.example.org"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": ""},
				received: map[string]string{"origin": "https://api.example.org"}}},
		{name: "a preflight from the request's own origin that no list holds", keys: ownOriginKeys, send: request{proto: "http/1.1", method: "OPTIONS",
			host: "api.example.org", path: "/products/1", headers: map[string]string{"origin": "https://api.example.org",
				"access-control-request-method": "POST"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "a preflight from a listed origin, with no token", keys: []string{"cors.allow_methods", "cors.allow_headers", "cors.max_age",
			"origins.<environment name>.origins"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST",
				"access-control-request-headers": "content-type,x-custom"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-allow-methods": "POST", "access-control-allow-headers": "content-type,x-custom", "access-control-max-age": "600"}}},
		{name: "a preflight from a listed origin in capitals", keys: []string{"origins.<environment name>.origins"}, send: request{proto: "http/1.1",
			method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "HTTPS://WWW.EXAMPLE.COM", "access-control-request-method": "POST"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "HTTPS://WWW.EXAMPLE.COM"}}},
		{name: "a preflight from a pattern's origin in capitals", keys: []string{"origins.<environment name>.patterns"}, send: request{proto: "http/1.1",
			method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "HTTPS://SHOP.EXAMPLE.COM", "access-control-request-method": "PUT"}},
			want: answer{status: 403}},
		{name: "two Origin lines a pattern covers when joined", keys: []string{"origins.<environment name>.patterns"}, send: request{proto: "http/1.1",
			method: "GET", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://evil.example.net\r\norigin: https://www.example.com"}, raw: true},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://evil.example.net,https://www.example.com"},
				received: map[string]string{"origin": "https://evil.example.net,https://www.example.com"}},
			by: map[string]own{"nginx": {difference: "origins.<environment name>.patterns", literal: literal{want: answer{status: 403}}}}},
		{name: "two Origin lines, a listed one first", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com\r\norigin: https://evil.example.net"}, raw: true},
			want: answer{status: 403}},
		{name: "an Origin holding a comma, a listed origin after it", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://evil.example.net,https://example.com"}},
			want: answer{status: 403}},
		{name: "two Origin lines, the request's own origin twice", keys: ownOriginKeys, send: request{proto: "http/1.1", method: "GET",
			host: "api.example.org", path: "/products/1", headers: map[string]string{"origin": "https://api.example.org\r\norigin: https://api.example.org"},
			raw: true},
			want: answer{status: 403}},
		{name: "an empty Origin line after a listed one", keys: []string{"origins.<environment name>.origins"}, send: request{proto: "http/1.1",
			method: "GET", host: "api.example.com", path: "/products/1", headers: map[string]string{"origin": "https://www.example.com\r\norigin: "},
			raw: true},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://www.example.com"},
				received: map[string]string{"origin": "https://www.example.com"}}},
		{name: "a preflight asking for two methods on two lines", keys: []string{"cors.allow_methods", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1", raw: true,
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST\r\naccess-control-request-method: PUT"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-methods": "POST,PUT"}},
			by: map[string]own{"nginx": {difference: "cors.allow_methods", literal: literal{want: answer{status: 200,
				headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-methods": "POST, PUT"}}}}}},
		{name: "a preflight asking for headers on two lines", keys: []string{"cors.allow_headers", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST",
					"access-control-request-headers": "x-a\r\naccess-control-request-headers: x-b"}, raw: true},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-headers": "x-a,x-b"}},
			by: map[string]own{"nginx": {difference: "cors.allow_headers", literal: literal{want: answer{status: 200,
				headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-headers": "x-a, x-b"}}}}}},
		{name: "an Origin with a trailing space", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com "}, raw: true},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://www.example.com"},
				received: map[string]string{"origin": "https://www.example.com"}}},
		{name: "a service that is down, from a listed origin", keys: []string{"cors.expose_headers", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/catalog/items",
				headers: map[string]string{"origin": "https://www.example.com"}}, refusing: "catalog",
			want: answer{status: 502, headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-expose-headers": "Content-Disposition,X-Request-Id"}}},
		{name: "a request from a listed origin to a host no environment names", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 404, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "a refused path from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/a/..%3B/b", headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "a method Envoy's parser does not know, from a listed origin, carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "FOO", host: "api.example.com", path: "/products/1", headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "a NUL byte in a header from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: fromListed(map[string]string{"x-nul": "a\x00b"}),
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		// HAProxy serves this request chunked, with the CORS answer, its less strict way on Content-Length beside
		// Transfer-Encoding (lineRow).
		{name: "Content-Length beside Transfer-Encoding from a listed origin carries no CORS answer",
			keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "POST", host: "api.example.com", path: "/products/1", raw: true, unframed: true,
				headers: map[string]string{"origin": "https://www.example.com", "content-length": "3", "transfer-encoding": "chunked"},
				body:    "0\r\n\r\n"},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop",
				headers: map[string]string{"access-control-allow-origin": "https://www.example.com"}}}}}},
		{name: "a header line over 8 KiB from a listed origin", keys: []string{"origins.<environment name>.origins"},
			send: fromListed(map[string]string{"x-long": strings.Repeat("a", 9000)}),
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://www.example.com"}}},
		{name: "a preflight to a refused path from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/a/..%3B/b",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "GET"}},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "an upgrade other than WebSocket from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "upgrade": "spdy/3.1", "connection": "upgrade"}},
			want: answer{status: 403, headers: map[string]string{"access-control-allow-origin": "", "content-type": "text/plain"}}},
		{name: "a request from an origin on none of the environment's hosts", keys: hostKeys, send: fromOrigin("http://foreign.example"),
			want: answer{status: 403}},
		{name: "a request from the origin of the environment's host", keys: hostKeys, send: fromOrigin("http://localhost"),
			want: answer{status: 200, service: "shop", received: map[string]string{"origin": "http://localhost"}}},
		{name: "a preflight from the origin of the environment's host", keys: hostKeys, send: preflightFrom("http://localhost"),
			want: answer{status: 200}},
		{name: "a preflight from an origin on none of the environment's hosts", keys: hostKeys, send: preflightFrom("http://foreign.example"),
			want: answer{status: 403}},
		{name: "a request with no Origin", keys: hostKeys, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "an origin on the environment's host over https", keys: hostKeys, send: fromOrigin("https://localhost"),
			want: answer{status: 200, service: "shop"}},
		{name: "an origin on the environment's host with a port", keys: hostKeys, send: fromOrigin("http://localhost:8080"),
			want: answer{status: 200, service: "shop"}},
		{name: "an origin on the environment's other host", keys: hostKeys, send: fromOrigin("http://127.0.0.1"),
			want: answer{status: 200, service: "shop"}},
		{name: "an origin on another environment's host", keys: hostKeys, send: fromOrigin("http://api.example.com"),
			want: answer{status: 403}},
		{name: "an origin on the environment's host in capitals", keys: hostKeys, send: fromOrigin("http://LOCALHOST"),
			want: answer{status: 403}},
		{name: "an origin on the environment's host with a trailing dot", keys: hostKeys, send: fromOrigin("http://localhost."),
			want: answer{status: 403}},
		{name: "an origin whose host has other characters for its dots", keys: hostKeys, send: fromOrigin("http://127x0x0x1"),
			want: answer{status: 403}},
		{name: "the origin null", keys: hostKeys, send: fromOrigin("null"), want: answer{status: 403}},
		{name: "a present but empty Origin", keys: hostKeys, send: fromOrigin(""), want: answer{status: 403}},
		{name: "a present but empty Origin over HTTP/2", keys: hostKeys, send: request{proto: "h2", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"origin": ""}, raw: true}, want: answer{status: 403}},
		{name: "an empty Origin line, then the origin of the environment's host", keys: hostKeys, send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"origin": "\r\norigin: http://localhost"}, raw: true},
			want: answer{status: 200, service: "shop", received: map[string]string{"origin": "http://localhost"}}},
		{name: "the origin of the environment's host, then an empty Origin line", keys: hostKeys, send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"origin": "http://localhost\r\norigin: "}, raw: true},
			want: answer{status: 200, service: "shop", received: map[string]string{"origin": "http://localhost"}}},
		{name: "the origin of the environment's host after a tab", keys: hostKeys, send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"origin": "\thttp://localhost"}, raw: true},
			want: answer{status: 200, service: "shop", received: map[string]string{"origin": "http://localhost"}}},
		// HAProxy keeps 1,024 bytes for the lines it adds to an answer, which an echoed Origin and cors.expose_headers
		// share, so it answers an admitted Origin longer than 872 bytes less the length of cors.expose_headers, 840 on
		// haproxy-file, 403 before the check and the service (origins.<environment name>.patterns), where a longer echo
		// got HAProxy's own 500 after the service had answered (measured on HAProxy 3.2.26 and 3.4.7). The two rows sit
		// well inside and well outside that bound, never at its edge, each beside a service's answer of 100 KB, which
		// fills the buffer HAProxy reads an answer into first. Envoy 1.40.2 echoes both.
		{name: "an admitted Origin of 600 bytes is echoed beside an answer of 100 KB", keys: longEchoKeys,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": admittedOrigin(600), "x-stub-pad": "100000"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": admittedOrigin(600)}}},
		{name: "an admitted Origin of 1,100 bytes, longer than HAProxy echoes", keys: longEchoKeys,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": admittedOrigin(1100), "x-stub-pad": "100000"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": admittedOrigin(1100)}},
			by:   map[string]own{"haproxy": {difference: "origins.<environment name>.patterns", literal: literal{want: answer{status: 403}}}}},
		// A preflight Envoy 1.40.2 answers itself after it resolves the dot segment ..;x, as the preflight of the path it
		// reaches, here the shop route's, which no limit holds (measured); nginx and HAProxy answer 400, as to any
		// request through that segment (dotRow).
		{name: "a preflight from a listed origin through a dot segment with a ; parameter",
			keys: []string{"cors.allow_methods", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/..;x/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "GET"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com"}},
			by: map[string]own{"haproxy": {difference: dotRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: dotRow, literal: literal{want: answer{status: 400}}}}},
		// A preflight Envoy 1.40.2 answers itself after it resolves the dot segment ..;x, asking no check; HAProxy asks
		// the check about it and answers 400 once the check lets it through (dotRow).
		{name: "a preflight from a listed origin through a dot segment with a ; parameter, with a token the check allows",
			keys: []string{"auth.ask", "cors.allow_methods", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/..;x/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "GET",
					"authorization": "Bearer allow"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com"}},
			by: map[string]own{"haproxy": {difference: dotRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: dotRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a denial of the check from a listed origin carries the CORS answer",
			keys: []string{"auth.ask", "cors.expose_headers", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "authorization": "Bearer deny-401"}},
			want: answer{status: 401, headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-expose-headers": "Content-Disposition,X-Request-Id"}}},
		{name: "a WebSocket upgrade from an origin on none of the environment's hosts", keys: hostKeys, send: request{proto: "http/1.1",
			method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"origin": "http://foreign.example",
				"upgrade": "websocket", "connection": "upgrade"}},
			want: answer{status: 403},
			// HAProxy's parser answers a WebSocket upgrade with no Sec-WebSocket-Key 400 before any rule reads its
			// origin (lineRow).
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
	}}

// hostKeys are what the rows of an origin with no list depend on: the hosts of the environment the request is sent to.
var hostKeys = []string{"origins.<environment name>.hosts"}

// longEchoKeys are what the rows of a long admitted Origin depend on: the pattern that admits it, and
// cors.expose_headers, the echo's neighbour in the room HAProxy keeps for the lines it adds to an answer.
var longEchoKeys = []string{"cors.expose_headers", "origins.<environment name>.patterns"}

// admittedOrigin is an Origin of size bytes that the pattern https://*.example.com of the live environment admits.
func admittedOrigin(size int) string {
	return "https://" + strings.Repeat("a", size-len("https://.example.com")) + ".example.com"
}

// fromOrigin is a GET of the shop route on localhost carrying origin as its Origin header, written even when empty.
func fromOrigin(origin string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"origin": origin}}
}

// preflightFrom is a browser's preflight of a GET of the shop route on localhost from origin.
func preflightFrom(origin string) request {
	return request{proto: "http/1.1", method: "OPTIONS", host: "localhost", path: "/products/1",
		headers: map[string]string{"origin": origin, "access-control-request-method": "GET"}}
}

// ownOriginKeys are what the two rows of the request's own origin depend on: hosts admits the origin, and origins,
// which does not list it, leaves it without access-control-allow-origin.
var ownOriginKeys = []string{"origins.<environment name>.hosts", "origins.<environment name>.origins"}

// auth: every value measured on Envoy 1.40.2. The two denial rows assert that no x-envoy-upstream-service-time comes
// with the check's answer: Envoy's client for the check adds it, and the header mutation filter removes it (without the
// removal both rows got "0"). A header that is absent is absent for every proxy. Envoy also answers, and no row
// asserts: the check's own body and headers on a denial; 403 with an empty body when the check answers 204; and the
// unchecked mark to the service when the check closes the connection, as when it answers 503, which this scenario
// asserts, or times out, which the timeout row asserts. The mark is the header a proxy adds to a request the check
// could not answer (target.mark), x-envoy-auth-failure-mode-allowed: true on Envoy; a client's own copy is removed
// whether the check allows or is not asked, here and on an auth-off route (offRoute), so the service never reads a
// forged one. The check itself receives authorization, x-country, x-forwarded-method, x-forwarded-uri with the query,
// x-forwarded-proto: https, x-forwarded-host, x-gateway-timeout-ms: 1500, Envoy's own x-envoy-internal: true and
// x-envoy-expected-rq-timeout-ms: 1500, the original method and the host the client sent. The stub's check denies a
// request with no token it knows, so the first row fails if the check is asked. A client's own x-forwarded-uri and
// x-forwarded-method reach the service as sent, which the format allows, but never the check. The headers a client may
// not send are three rows, because a file may write remove_from_clients without the check, and the check without
// auth.jwt: x-client-id, which remove_from_clients alone removes, sent twice and the second time in capitals; the
// check's x-user-id and question; and x-subject and x-roles, the headers auth.jwt.claims sets from a verified token,
// which only a file that writes the claims runs. A client's own x-request-id that remove_from_clients names is read as
// never sent, so the service receives an id the proxy makes: with client_address = connection a proxy replaces it
// anyway, and nginx-plain, which keeps forwarded and removes it, shows the removal itself. An answer of the check that
// Envoy treats as unanswered is a row under allow, on haproxy-file, where the request is served with the mark, and
// under deny, on two-checks, where it is answered 403: a body that breaks off, a 101st header line, a head or a trailer
// section past 81,920 bytes and each kind of malformed answer, the last two of which HAProxy's reader answers 403 under
// allow too (auth.when_unavailable). two-checks also holds
// the check service at two addresses, one of which a row makes refuse connections. A request to the route the nginx
// battery files retry, retried, is checked once, before its first try, and a retry carries what the check answered:
// with shop-2 refusing connections, each of 20 requests the check allows reaches shop-1 with the x-user-check the check
// set; each of 6 it denies is answered 403 with no service; and each of 10 whose check answers 503 reaches shop-1 with
// the unchecked mark under allow and is answered 403 under deny, on nginx-checks. That nginx asks the check once for
// a retried request is held by the generator's tests, which find no handler in a copy that takes a next try, as the
// stub's check counts no questions.
var auth = scenario{name: "auth",
	exchanges: []exchange{
		{name: "no authorization header, so the check is not asked", keys: []string{"auth.ask", "auth.only_with_header"}, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "the check allows", keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer allow", "x-country": "RO", "x-forwarded-uri": "/admin", "x-forwarded-method": "DELETE",
				"x-gateway-action": "forged"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-id": "u-1", "x-tier-level": "gold",
				"x-user-check": "GET /products/1?q=1 action= reports= country=RO", "authorization": "Bearer allow"}, absent: []string{"x-other"}}},
		{name: "the check denies with 401", keys: []string{"auth.ask"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer deny-401", "x-api-key": "key", "x-country": "RO"}},
			want: answer{status: 401, headers: map[string]string{"x-envoy-upstream-service-time": ""}}},
		{name: "the check denies with 403", keys: []string{"auth.ask"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer deny-403", "x-api-key": "key"}},
			want: answer{status: 403, headers: map[string]string{"x-envoy-upstream-service-time": ""}}},
		{name: "the check answers 503 and when_unavailable is allow", keys: []string{"auth.ask", "auth.when_unavailable"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer status-503", "x-api-key": "key"}}, want: answer{status: 200, service: "shop", marked: "true"},
			// two-checks is deny.
			on: deniedUnderDeny},
		{name: "a client's own unchecked mark is removed when the check allows", keys: []string{"auth.ask", "auth.when_unavailable"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"authorization": "Bearer allow"},
				forgedMark: true}, want: answer{status: 200, service: "shop", unmarked: true}},
		{name: "a client's own unchecked mark is removed when the check is not asked", keys: []string{"auth.ask", "auth.when_unavailable", "auth.only_with_header"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", forgedMark: true},
			want: answer{status: 200, service: "shop", unmarked: true}},
		{name: "a route with auth off", keys: []string{"routes[].auth"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/events/abc-123",
			headers: map[string]string{"authorization": "Bearer deny-401"}}, want: answer{status: 200, service: "accounts"}},
		{name: "headers of the check a client may not send", keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-user-id": "forged", "x-gateway-action": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"},
				absent: []string{"x-user-id", "x-gateway-action"}},
			// two-checks accepts x-user-check alone, so a client's own x-user-id is the service's to judge there.
			on: map[string]literal{"two-checks": {want: answer{status: 200, service: "shop",
				received: map[string]string{"x-keep": "yes", "x-user-id": "forged"}, absent: []string{"x-gateway-action"}}},
				"nginx-checks": {want: answer{status: 200, service: "shop",
					received: map[string]string{"x-keep": "yes", "x-user-id": "forged"}, absent: []string{"x-gateway-action"}}}}},
		{name: "a header remove_from_clients names a client may not send", keys: []string{"remove_from_clients"},
			send: rawHeaders(map[string]string{"x-client-id": "forged\r\nX-CLIENT-ID: again", "x-keep": "yes"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"}, absent: []string{"x-client-id"}},
			// nginx-plain and two-checks remove x-request-id alone.
			on: map[string]literal{"nginx-plain": {want: answer{status: 200, service: "shop",
				received: map[string]string{"x-keep": "yes", "x-client-id": "forged\nagain"}}},
				"two-checks": {want: answer{status: 200, service: "shop",
					received: map[string]string{"x-keep": "yes", "x-client-id": "forged\nagain"}}},
				"nginx-checks": {want: answer{status: 200, service: "shop",
					received: map[string]string{"x-keep": "yes", "x-client-id": "forged\nagain"}}}}},
		// nginx-tracing's remove_from_clients names x-client-id alone and it writes no client_address, so under forwarded
		// the client's own x-request-id reaches the service (measured on Envoy 1.40.2), the id tracing's draw reads.
		{name: "a client's own x-request-id is replaced where remove_from_clients names it or client_address is connection, and kept otherwise", keys: []string{"remove_from_clients"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-request-id": "client-id-1"}},
			want: answer{status: 200, service: "shop", replaced: map[string]string{"x-request-id": "client-id-1"}},
			on: map[string]literal{"nginx-tracing": {want: answer{status: 200, service: "shop",
				received: map[string]string{"x-request-id": "client-id-1"}}}}},
		// two-checks names the prefix ho*, which covers Host: the client's header under it is removed and its Host kept,
		// where HAProxy's removal by the prefix took Host too and answered every request 404.
		{name: "a header under a remove_from_clients prefix is removed, and the Host the prefix covers is kept", keys: []string{"remove_from_clients"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"ho-x": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", host: "localhost", received: map[string]string{"ho-x": "forged", "x-keep": "yes"}},
			on: map[string]literal{"two-checks": {want: answer{status: 200, service: "shop", host: "localhost",
				received: map[string]string{"x-keep": "yes"}, absent: []string{"ho-x"}}}}},
		{name: "headers a verified token sets a client may not send", keys: []string{"auth.jwt.claims"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-subject": "forged", "x-roles": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"}, absent: []string{"x-subject", "x-roles"}}},
		{name: "a check's 2xx other than 200 is answered 403", keys: []string{"auth.ask"}, send: token("status-201"),
			want: answer{status: 403}},
		// A denial the check sends with no body reaches the client with no content-type, as the Envoy output passes it
		// on (measured on Envoy 1.40.2), where nginx would give it its default type.
		{name: "a denial with no body carries no content-type", keys: []string{"auth.ask"}, send: token("deny-401-empty"),
			want: answer{status: 401, headers: map[string]string{"content-type": ""}}},
		{name: "a denial larger than HAProxy reads whole", keys: []string{"auth.ask"}, send: token("big-denial"),
			want: answer{status: 401},
			by:   map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a denial with a header line longer than HAProxy reads whole", keys: []string{"auth.ask"}, send: token("big-header"),
			want: answer{status: 401},
			by:   map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a sent header on two lines reaches the check as one line", keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawHeaders(map[string]string{"authorization": "Bearer allow", "X-Country": "FR", "x-country": "RO"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=FR,RO"}}},
		// HAProxy reads no header line of 16,383 bytes or more in the check's answer, so it answers the check's 200 with
		// an x-user-id of 20,000 bytes 403 before any header is accepted (auth.ask), on two-checks too, which accepts
		// x-user-check alone and where Envoy 1.40.2 serves the request without x-user-id.
		{name: "an accepted header too large for the request", keys: []string{"auth.ask", "auth.accept"}, send: token("big-accept"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-id": strings.Repeat("u", 20000)}},
			on: map[string]literal{"two-checks": {want: answer{status: 200, service: "shop", absent: []string{"x-user-id"}}},
				"nginx-checks": {want: answer{status: 200, service: "shop", absent: []string{"x-user-id"}}}},
			by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "the check is told it is internal and its deadline, as Envoy tells it", keys: []string{"auth.ask", "auth.timeout", "auth.accept"},
			send: token("report"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "internal=true expected=1500"}}},
		{name: "a client's own x-forwarded-proto reaches the check under forwarded, and the connection's under connection",
			keys: []string{"auth.send_request", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key", "x-forwarded-proto": "https"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=https host=localhost"}}},
		{name: "the check receives the client's Host whatever the check service's host_header",
			keys: []string{"auth.ask", "auth.accept", "services.<service name>.host_header"}, send: token("report-forwarded"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=https host=localhost"}},
			on: onEach([]string{"two-checks", "nginx-checks", "nginx-tracing"}, literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"x-user-check": "proto=http host=localhost"}}})},
		{name: "the only_with_header header sent twice, an empty line then a token, asks the check",
			keys: []string{"auth.ask", "auth.only_with_header", "auth.accept", "auth.send_request"},
			send: rawHeaders(map[string]string{"authorization": "\r\nauthorization: Bearer allow", "x-api-key": "key"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country="}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "the only_with_header header sent twice, a token then an empty line, asks the check",
			keys: []string{"auth.ask", "auth.only_with_header", "auth.accept", "auth.send_request"},
			send: rawHeaders(map[string]string{"authorization": "Bearer allow\r\nauthorization: ", "x-api-key": "key"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country="}},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		// two-checks asks the check by x-api-key, a header the script passes on line by line, and the other files by
		// authorization, which it joins into one line. Envoy reads the lines of a header it does not join as one value,
		// joined by commas, so two empty lines are asked, and over HTTP/2 it asks a value of spaces, which HAProxy reads
		// as empty: HAProxy asks every HTTP/2 request that carries the header, an empty one too, which Envoy does not ask
		// (auth.only_with_header). A request the check is asked about here carries no token, so the check denies it.
		{name: "the only_with_header header sent as two empty lines asks the check when the script does not join it",
			keys: []string{"auth.ask", "auth.only_with_header"}, send: rawHeaders(map[string]string{"x-api-key": "\r\nx-api-key:"}),
			want: answer{status: 200, service: "shop"}, on: map[string]literal{"two-checks": {want: answer{status: 401}},
				"nginx-checks": {want: answer{status: 401}}}},
		{name: "the only_with_header header of spaces alone over HTTP/2 asks the check", keys: []string{"auth.ask", "auth.only_with_header"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"authorization": "   ", "x-api-key": "   "}},
			want: answer{status: 401}},
		{name: "the only_with_header header sent empty over HTTP/2 is not asked", keys: []string{"auth.ask", "auth.only_with_header"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"authorization": "", "x-api-key": ""}},
			want: answer{status: 200, service: "shop"},
			by:   map[string]own{"haproxy": {difference: "auth.only_with_header", literal: literal{want: answer{status: 401}}}}},
		// check-2 is a backend of two-checks' check service alone, and each check sent to it while it refuses connections
		// fails, which that file's when_unavailable: deny answers 403; on every other file the row is served. HAProxy
		// keeps one round robin count for a backend, so every second check reaches check-2. Envoy 1.40.2 keeps one for
		// each worker, started at a random number (seed_, source/extensions/load_balancing_policies/common/
		// load_balancer_impl.cc), and each pick takes the next address (unweightedHostPick, round_robin/round_robin_lb.h):
		// a worker that gets two of the row's checks sends one of them to check-2, and one that gets a single check sends
		// it there with a chance of one half, whatever the other workers do. A run of 24 checks with none refused then has
		// a chance of at most one in 2^24, about one in 16.8 million, on any number of workers, so on any number of CPUs.
		{name: "a check backend that refuses connections fails the checks it gets, which no other backend answers",
			keys: []string{"auth.ask"}, send: token("allow"), repeat: 24, refusing: "check-2",
			want: answer{status: 200, service: "shop"},
			on:   deniedUnderDeny},
		{name: "a denial whose body breaks off is an unanswered check", keys: unanswered, send: token("cut-body"),
			want: answer{status: 200, service: "shop", marked: "true"}, on: deniedUnderDeny},
		{name: "a denial of 101 header lines is an unanswered check", keys: unanswered, send: token("many-headers"),
			want: answer{status: 200, service: "shop", marked: "true"}, on: deniedUnderDeny},
		// Envoy 1.40.2 does not count a folded line among the 100 header lines it reads, in a 1xx head or the final one,
		// and counts a name it keeps inline, such as date, once however many lines carry it, so it reads each of the
		// next four answers; with set-cookie twice it counts 101 entries (measured).
		{name: "a denial of 100 header lines and a folded line is passed on as the check's denial", keys: []string{"auth.ask"},
			send: token("fold-after-100"), want: answer{status: 401}},
		{name: "a 200 of 100 header lines and a folded line is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("allow-fold-after-100"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "allow-fold-after-100"}}},
		{name: "a 200 after a 1xx of 100 header lines and a folded line is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("1xx-fold-after-100"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "1xx-fold-after-100"}}},
		{name: "a denial of 101 header lines with date on two of them is passed on as the check's denial", keys: []string{"auth.ask"},
			send: token("date-twice-in-101"), want: answer{status: 401},
			// HAProxy's script and nginx's relay refuse a head past 100 lines with a name on two of them.
			by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}},
				"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a denial of 101 header lines with set-cookie on two of them is an unanswered check", keys: unanswered,
			send: token("cookie-twice-in-101"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		// Envoy 1.40.2 reads 81,920 bytes of each head of the check's answer, from the status line to the empty line,
		// a 1xx head and folded lines included, and as many of a trailer section (measured to the byte).
		{name: "a 200 whose head is within 81,920 bytes is read", keys: []string{"auth.ask", "auth.accept"}, send: token("head-within"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "head-within"}}},
		{name: "a 200 whose head passes 81,920 bytes is an unanswered check", keys: unanswered, send: token("head-past"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose folded line takes its head past 81,920 bytes is an unanswered check", keys: unanswered,
			send: token("fold-past"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 after a 1xx whose head passes 81,920 bytes is an unanswered check", keys: unanswered,
			send: token("1xx-past"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose trailer section is within 81,920 bytes is read", keys: []string{"auth.ask", "auth.accept"},
			send: token("trailer-within"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "trailer-within"}}},
		{name: "a 200 whose trailer section passes 81,920 bytes is an unanswered check", keys: unanswered,
			send: token("trailer-past"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose Content-Length is not a number is an unanswered check", keys: unanswered, send: token("length-not-a-number"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with two Content-Length lines is an unanswered check", keys: unanswered, send: token("two-lengths"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose header value holds a control byte is an unanswered check", keys: unanswered, send: token("control-byte"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose status is not three digits is an unanswered check", keys: unanswered, send: token("status-not-three-digits"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with no space before its reason is an unanswered check", keys: unanswered, send: token("no-space-before-reason"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose header name holds a byte outside a token is an unanswered check", keys: unanswered,
			send: token("name-not-a-token"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with Content-Length beside Transfer-Encoding is an unanswered check", keys: unanswered,
			send: token("length-and-chunked"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose chunk size is not hexadecimal is an unanswered check", keys: unanswered, send: token("chunk-not-hex"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 after a 1xx with a malformed header line is an unanswered check", keys: unanswered, send: token("bad-line-in-1xx"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose trailer line is malformed is an unanswered check", keys: unanswered, send: token("bad-trailer"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a status of 600 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-600"),
			want: answer{status: 600},
			by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}},
				"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		// The forms each of nginx's own readers of an answer reads in a way of its own (stub/check.go), each sent once:
		// a status Envoy passes on to the client, which the battery's client cannot read when it is not three digits, an
		// answer Envoy reads as allowed or as the check's denial, and one Envoy reads as no answer, under allow and under
		// deny. nginx answers 403 to each the relay cannot be sure it read as Envoy does (auth.ask, auth.when_unavailable).
		{name: "a status of 2 0 0 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-spaced"),
			want: answer{refused: true}, by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a status of 200.1 is an unanswered check", keys: unanswered, send: token("status-fraction"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a status of 20 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-two-digits"),
			want: answer{refused: true}, by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a status of 000 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-zero"),
			want: answer{refused: true}, by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 with a header line with no colon is an unanswered check", keys: unanswered, send: token("allow-line-no-colon"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a denial with a header line with no colon is an unanswered check", keys: unanswered, send: token("deny-line-no-colon"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a denial with a folded header line is passed on as the check's denial", keys: []string{"auth.ask"}, send: token("deny-folded"),
			want: answer{status: 401}},
		// Envoy 1.40.2 answers 500 with an empty body to a request that the check's accepted headers take past
		// limits.request_headers: its size counts each header's name and value, with :method, :path, :authority and
		// :scheme, and the x-request-id and x-forwarded-proto it adds; its count counts those fields too, and with
		// client_address connection the x-forwarded-for and x-envoy-external-address it sets, so 91 lines from the
		// client, Host among them as :authority, and one accepted header reach 100 on two-checks and nginx-checks, and 87
		// lines and three accepted headers reach 100 on haproxy-file and nginx-auth. It counts no Transfer-Encoding or
		// Connection line, and over HTTP/2 no host field beside :authority (measured with 87 lines beside each). On
		// nginx-tracing, which accepts three headers under forwarded, it answers 500 to 91 lines, chunked too, and
		// grpc-status 2 to a gRPC call of 86 (measured).
		{name: "a 200 whose accepted header takes the request past the header size is answered 500", keys: []string{"auth.accept"},
			send: rawToken("accept-past-size", map[string]string{"x-pad": strings.Repeat("p", 30000)}), want: answer{status: 500},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 431}}}}},
		{name: "a 200 whose accepted headers take the request past the header count is answered 500", keys: []string{"auth.accept"},
			send: rawToken("allow", numbered(92)), want: answer{status: 500},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		// Envoy 1.40.2 counts the request only when the check's answer held a header auth.accept covers other than
		// keep-alive, which it never sets: a 200 that sets none is served up to the 100 lines its parser reads (measured).
		{name: "a 200 that sets no accepted header is served beside 96 numbered header lines", keys: []string{"auth.accept"},
			send: rawToken("allow-empty", numbered(96)), want: answer{status: 200, service: "shop"}},
		{name: "a 200 whose accepted headers take the request to the header count is served", keys: []string{"auth.accept"},
			send: rawToken("allow", numbered(91)), want: answer{status: 200, service: "shop"},
			on: map[string]literal{"haproxy-file": {want: answer{status: 500}}, "nginx-auth": {want: answer{status: 500}},
				"nginx-tracing": {want: answer{status: 500}}}},
		// The same request with a body: chunked, its Transfer-Encoding line is not counted and the request is served where
		// the count would reach 101 with it, and framed by its length, its Content-Length line is counted and the request
		// answered 500 (measured on Envoy 1.40.2: two-checks and nginx-checks serve 91 numbered lines chunked and 90 by
		// length, haproxy-file and nginx-auth 87 chunked and 86 by length).
		{name: "a chunked request's Transfer-Encoding is not counted with the accepted headers", keys: []string{"auth.accept"},
			send: withBody(rawToken("allow", numbered(91))), want: answer{status: 200, service: "shop", body: "abc"},
			on: map[string]literal{"haproxy-file": {want: answer{status: 500}}, "nginx-auth": {want: answer{status: 500}},
				"nginx-tracing": {want: answer{status: 500}}}},
		{name: "a request framed by its length is counted with its Content-Length and the accepted headers", keys: []string{"auth.accept"},
			send: withLength(rawToken("allow", numbered(91))), want: answer{status: 500},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", body: "abc"}},
				"portable": {want: answer{status: 200, service: "shop", body: "abc"}}}},
		// A gRPC call over HTTP/2 that the accepted headers take past the count is answered as Envoy answers a gRPC call
		// it fails itself, 200 with grpc-status 2 and no service: from 87 numbered fields on two-checks and nginx-checks,
		// and from 84 or fewer on haproxy-file and nginx-auth, which accept three headers and add two of the connection's;
		// from 90 the call itself passes the count, and Envoy resets its stream (measured on Envoy 1.40.2).
		{name: "a gRPC call whose accepted headers take it past the header count is answered with grpc-status 2",
			keys: []string{"auth.accept"}, send: rpcToken("allow", numbered(87)), want: answer{status: 200, grpcStatus: "2"},
			on: map[string]literal{"full": {want: answer{status: 200, grpcStatus: "0", service: "shop"}},
				"portable": {want: answer{status: 200, grpcStatus: "0", service: "shop"}}}},
		{name: "a gRPC call whose accepted headers take it to the header count is served", keys: []string{"auth.accept"},
			send: rpcToken("allow", numbered(86)), want: answer{status: 200, grpcStatus: "0", service: "shop"},
			on: map[string]literal{"haproxy-file": {want: answer{status: 200, grpcStatus: "2"}},
				"nginx-auth": {want: answer{status: 200, grpcStatus: "2"}}, "nginx-tracing": {want: answer{status: 200, grpcStatus: "2"}}}},
		{name: "with client_address connection, a 200 whose accepted headers take the request to the header count is served",
			keys: []string{"auth.accept", "client_address"}, send: rawToken("allow", numbered(87)), want: answer{status: 200, service: "shop"}},
		{name: "with client_address connection, a 200 whose accepted headers take the request past the header count is answered 500",
			keys: []string{"auth.accept", "client_address"}, send: rawToken("allow", numbered(88)), want: answer{status: 500},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		{name: "a 500 for the header count carries an admitted origin's CORS answer", keys: []string{"auth.accept", "client_address"},
			send: rawToken("allow", withHeader(numbered(88), "origin", "http://localhost:3000")),
			want: answer{status: 500, headers: map[string]string{"access-control-allow-origin": "http://localhost:3000"}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}, "portable": {want: answer{status: 200, service: "shop"}}}},
		// Envoy counts an absolute target's host and port as :authority, whatever Host says, and nginx routes by the same.
		// With a Host of one byte beside this target, the smallest pad Envoy answered 500 was 1,170 bytes, and a count of
		// the Host line alone answered 500 from 1,193 (measured on Envoy 1.40.2 and nginx 1.30.6), so 1,181 tells them
		// apart. examples/full and examples/portable write 96 KiB, so they serve it, and Envoy 1.40.2 serves it on
		// nginx-tracing, which writes no tls and leaves client_address at forwarded (measured), so the row tells the two
		// counts apart on the files that write client_address connection.
		{name: "an absolute target with a port beside a short Host is counted by its host and port", keys: []string{"auth.accept", "limits.request_headers.size"},
			send: request{proto: "http/1.1", method: "GET", host: "x", path: "http://api.test.example.com:10000/products/1", raw: true,
				serverName: "api.test.example.com", headers: map[string]string{"authorization": "Bearer accept-60000", "x-api-key": "key",
					"x-pad": strings.Repeat("p", 1181)}},
			want: answer{status: 500},
			on:   onEach([]string{"full", "portable", "nginx-tracing"}, literal{want: answer{status: 200, service: "shop"}})},
		{name: "a 200 whose accepted header is empty reaches the service empty", keys: []string{"auth.accept"}, send: token("accept-empty"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": ""}, oneLine: []string{"x-user-check"}}},
		// HAProxy's script joins a folded line by one space (auth.accept).
		{name: "a 200 whose accepted header is folded with a tab reaches the service with the tab", keys: []string{"auth.ask", "auth.accept"},
			send: token("folded-tab"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a\tb"}},
			by: map[string]own{"haproxy": {difference: "auth.accept", literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a b"}}}}}},
		{name: "a 200 whose accepted header is folded with spaces on both lines reaches the service with each space", keys: []string{"auth.ask", "auth.accept"},
			send: token("folded-spaces"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a   b"}},
			by: map[string]own{"haproxy": {difference: "auth.accept", literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a b"}}}}}},
		{name: "a 200 whose accepted header is folded twice reaches the service with each fold's spaces and tabs", keys: []string{"auth.ask", "auth.accept"},
			send: token("folded-twice"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a b\t c"}},
			by: map[string]own{"haproxy": {difference: "auth.accept", literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "a b c"}}}}}},
		{name: "a 200 of HTTP/2.0 is read as allowed", keys: []string{"auth.ask", "auth.accept"}, send: token("version-2"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "version-2"}},
			by:   map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}, "haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 with two spaces after its version is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("two-spaces-after-version"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "two-spaces-after-version"}},
			by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}},
				"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 whose header value holds a vertical tab is an unanswered check", keys: unanswered, send: token("vertical-tab"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with Transfer-Encoding gzip, chunked is an unanswered check", keys: unanswered, send: token("gzip-chunked"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with Transfer-Encoding identity and a Content-Length is an unanswered check", keys: unanswered,
			send: token("identity-length"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose chunk size has a space after it is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("chunk-size-space"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "chunk-size-space"}},
			by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}},
				"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 whose chunk does not end its line is an unanswered check", keys: unanswered, send: token("bad-chunk-end"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose chunk-size line ends in a bare LF is an unanswered check", keys: unanswered, send: token("chunk-size-lf"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose last chunk's line ends in a bare LF is an unanswered check", keys: unanswered, send: token("last-chunk-lf"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose chunk data, trailer line and final line end in a bare LF is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("chunk-other-lf"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "chunk-other-lf"}}},
		{name: "a 200 whose Content-Length has spaces around it is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("length-spaced"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "length-spaced"}}},
		{name: "a 200 with a space before a header's colon is an unanswered check", keys: unanswered, send: token("space-before-colon"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose header name holds a NUL is an unanswered check", keys: unanswered, send: token("nul-in-name"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 whose header line ends in a lone CR before its CRLF is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("lone-cr"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "lone-cr"}},
			by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}, "haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 with a header name holding _ is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("underscore-name"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "underscore-name"}}},
		{name: "a 200 whose header name holds the byte 0xE9 is an unanswered check", keys: unanswered, send: token("e9-in-name"),
			want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a 200 with bytes after its body is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("bytes-after-body"), want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "bytes-after-body"}}},
		{name: "a 200 whose Content-Length is past what any reader holds is an unanswered check", keys: unanswered,
			send: token("length-past-1mib"), want: markedOnly, on: deniedUnderDeny, by: refusedUnderAllow},
		{name: "a status of 304 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-304"),
			want: answer{status: 304}},
		{name: "a status of 500 from the check is an unanswered check", keys: unanswered, send: token("status-500"),
			want: answer{status: 200, service: "shop", marked: "true"}, on: deniedUnderDeny},
		{name: "a status of 503 with a body from the check is an unanswered check", keys: unanswered, send: token("status-503-body"),
			want: answer{status: 200, service: "shop", marked: "true"}, on: deniedUnderDeny},
		{name: "a status of 599 from the check is an unanswered check", keys: unanswered, send: token("status-599"),
			want: answer{status: 200, service: "shop", marked: "true"}, on: deniedUnderDeny},
		{name: "a status of 101 is passed on as the check's own", keys: []string{"auth.ask"}, send: token("status-101"),
			want: answer{status: 101}, by: map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}, "haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a 200 with a body of 1 MiB is read as allowed", keys: []string{"auth.ask", "auth.accept"}, send: token("allow-1mib-body"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "allow-1mib-body"}}},
		{name: "a 200 holding the relay's own header name is read as allowed", keys: []string{"auth.ask", "auth.accept"},
			send: token("allow-proxyfile-check"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "allow-proxyfile-check"}}},
		// A denial's body reaches the client whole up to the 1 MiB nginx reads of a check's answer; past it nginx answers
		// 403 with no body, where Envoy passes the denial on (auth.ask).
		{name: "a denial with a body of 1 MiB is passed on whole", keys: []string{"auth.ask"}, send: token("denial-1mib"),
			want: answer{status: 401, headers: map[string]string{"content-length": "1048576"}}, by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a denial with a body of 1 MiB and a byte is passed on", keys: []string{"auth.ask"}, send: token("denial-1mib-and-a-byte"),
			want: answer{status: 401, headers: map[string]string{"content-length": "1048577"}},
			by:   map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}, "haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a denial with a body of 1,100,000 bytes is passed on", keys: []string{"auth.ask"}, send: token("denial-1100000"),
			want: answer{status: 401, headers: map[string]string{"content-length": "1100000"}},
			by:   map[string]own{"nginx": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}, "haproxy": {difference: "auth.ask", literal: literal{want: answer{status: 403}}}}},
		{name: "a denial's header holding the byte 0xE9 reaches the client as the check wrote it", keys: []string{"auth.ask"},
			send: token("deny-e9-header"), want: answer{status: 401, headers: map[string]string{"x-a": "caf\xe9"}}},
		// A redirect read by hand, as the battery's other clients follow it.
		{name: "a check's redirect with a Location that is a path reaches the client as the check sent it", keys: []string{"auth.ask"},
			send: rawToken("redirect-relative", nil), want: answer{status: 302, headers: map[string]string{"location": "/login?next=%2Fx"}}},
		{name: "the check location's own path, with a token the check denies, asks the check as any other path", keys: []string{"auth.ask"},
			send: tokenTo("deny-401", "/.proxyfile/check"), want: answer{status: 401}},
		// An upload the check refuses: Envoy answers 100 Continue as it reads the head, which the battery reads as the
		// answer, and nginx sends it as it reads the body it refuses.
		{name: "an upload sent with Expect: 100-continue that the check refuses gets 100 Continue first", keys: []string{"auth.ask"},
			send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/products/1", raw: true, unframed: true, body: "abcd",
				headers: map[string]string{"authorization": "Bearer deny-401", "x-api-key": "key", "content-length": "4", "expect": "100-continue"}},
			want: answer{status: 100}},
		{name: "a request with a body the check allows reaches the service with its body", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/products/1", raw: true, body: "abcd",
				headers: map[string]string{"authorization": "Bearer allow", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", body: "abcd",
				received: map[string]string{"x-user-check": "POST /products/1 action= reports= country="}}},
		// The Envoy output tells the check a ! for each byte of a header it sends that is not part of well-formed UTF-8,
		// and well-formed UTF-8 as the client sent it, as nginx's relay does.
		{name: "a sent header holding the byte 0xE9 reaches the check with a ! for it", keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "caf\xe9"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=caf!"}},
			// HAProxy's check receives the byte itself and answers it in x-user-check, which the service receives (auth.send).
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=caf\xe9"}}}}}},
		{name: "a sent header holding well-formed UTF-8 reaches the check as sent", keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "caf\xc3\xa9"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=caf\u00e9"}}},
		{name: "a sent header holding well-formed UTF-8 and then a byte that is not reaches the check with a ! for that byte",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "\xc3\xa9\xe9z"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=\u00e9!z"}},
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=\u00e9\xe9z"}}}}}},
		// Four forms RFC 3629 does not admit, one row each: an overlong form, a surrogate, a code point past U+10FFFF and a
		// sequence cut at the value's end, each told as a ! for each of its bytes (measured on Envoy 1.40.2). HAProxy's
		// check receives the bytes themselves and answers them in x-user-check, which the service receives (auth.send).
		{name: "a sent header holding an overlong form reaches the check with a ! for each of its bytes",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "a\xc0\xafb"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a!!b"}},
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a\xc0\xafb"}}}}}},
		{name: "a sent header holding a surrogate reaches the check with a ! for each of its bytes",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "a\xed\xa0\x80b"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a!!!b"}},
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a\xed\xa0\x80b"}}}}}},
		{name: "a sent header holding a code point past U+10FFFF reaches the check with a ! for each of its bytes",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "a\xf4\x90\x80\x80b"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a!!!!b"}},
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=a\xf4\x90\x80\x80b"}}}}}},
		{name: "a sent header holding a sequence cut at the value's end reaches the check with a ! for each of its bytes",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "ab\xe2\x82"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=ab!!"}},
			by: map[string]own{"haproxy": {difference: "auth.send",
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=ab\xe2\x82"}}}}}},
		{name: "a sent header on two lines of two characters outside ASCII reaches the check as sent, joined by a comma",
			keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"},
			send: rawToken("allow", map[string]string{"x-country": "\xe8\xaf\xbb\r\nx-country: \xe5\x86\x99"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country=\u8bfb,\u5199"}}},
		// Over HTTP/2 a path byte of 0x80 or more reaches the service escaped, and the Envoy output tells the check that
		// path, whether the bytes are well-formed UTF-8 or not (measured on Envoy 1.40.2); nginx answers such a path 400
		// in a file with a pattern route (routes[].pattern).
		{name: "a path holding well-formed UTF-8 and a byte that is not, over HTTP/2, is told to the check escaped, as the service receives it",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/caf\xc3\xa9\xe9", raw: true,
				headers: map[string]string{"authorization": "Bearer allow", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", path: "/products/caf%C3%A9%E9",
				received: map[string]string{"x-user-check": "GET /products/caf%C3%A9%E9 action= reports= country="}},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: checkedPatternFiles, literal: literal{want: answer{status: 400}}}}},
		{name: "the only_with_header header sent as a lone tab is not asked", keys: []string{"auth.ask", "auth.only_with_header"},
			send: rawHeaders(map[string]string{"authorization": "\t", "x-api-key": "\t"}), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "auth.only_with_header", literal: literal{want: answer{status: 401}}}}},
		{name: "100 checked streams at once on one HTTP/2 connection are each answered by their own check",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, streams: 100,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"authorization": "Bearer allow", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1?i={i} action= reports= country="}}},
		// What a client could send past the check.
		// Under client_address connection the check and the service are told the connection's scheme, whatever scheme
		// the target names, so https over TLS, and under forwarded, on two-checks, nginx-checks and nginx-tracing, the
		// check is told the target's http.
		{name: "an absolute target's host is the host the check is told, whatever the Host header", keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", host: "127.0.0.1", line: "GET http://localhost/products/1 HTTP/1.1", raw: true,
				headers: map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", host: "localhost", received: map[string]string{"x-user-check": "proto=https host=localhost",
				"x-forwarded-proto": "https"}},
			on: onEach([]string{"two-checks", "nginx-checks", "nginx-tracing"}, literal{want: answer{status: 200, service: "shop",
				host: "localhost", received: map[string]string{"x-user-check": "proto=http host=localhost"}}}),
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a client's own user, question and unchecked mark do not reach the service when the check allows with none of them",
			keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", forgedMark: true,
				headers: map[string]string{"authorization": "Bearer allow-empty", "x-api-key": "key", "x-user-id": "forged",
					"x-gateway-action": "forged", "x-gateway-reports": "forged"}},
			want: answer{status: 200, service: "shop", unmarked: true, absent: []string{"x-user-id", "x-gateway-action", "x-gateway-reports"}},
			// two-checks and nginx-checks accept x-user-check alone, so a client's own x-user-id is the service's to judge there.
			on: map[string]literal{
				"two-checks": {want: answer{status: 200, service: "shop", unmarked: true, received: map[string]string{"x-user-id": "forged"},
					absent: []string{"x-gateway-action", "x-gateway-reports"}}},
				"nginx-checks": {want: answer{status: 200, service: "shop", unmarked: true, received: map[string]string{"x-user-id": "forged"},
					absent: []string{"x-gateway-action", "x-gateway-reports"}}}}},
		{name: "the check receives none of a client's own removed header, user and unchecked mark", keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", forgedMark: true,
				headers: map[string]string{"authorization": "Bearer report-removed", "x-api-key": "key", "x-client-id": "forged", "x-user-id": "forged"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "client-id= user-id= mark="}}},
		{name: "a dot segment with a ; parameter and a token the check denies asks the check first", keys: []string{"auth.ask"},
			send: tokenTo("deny-401", "/products/..%3B/1"), want: answer{status: 401}},
		{name: "a dot segment with a ; parameter and a token the check allows is answered 400", keys: []string{"auth.ask"},
			send: tokenTo("allow", "/products/..%3B/1"), want: answer{status: 400}},
		{name: "a denial of a request from a listed origin carries the CORS answer", keys: []string{"auth.ask", "cors.expose_headers", "origins.<environment name>.origins"},
			send: fromListed(map[string]string{"authorization": "Bearer deny-401", "x-api-key": "key"}),
			want: answer{status: 401, headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-expose-headers": "Content-Disposition,X-Request-Id"}}},
		// As a WebSocket upgrade with no check, which the stub answers 200 as a service that does not switch (routing):
		// Envoy passes the status with no body, and nginx with the echo (the request line row).
		{name: "a WebSocket upgrade the check allows reaches the service", keys: []string{"auth.ask"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", raw: true,
				headers: map[string]string{"connection": "Upgrade", "upgrade": "websocket", "sec-websocket-key": "dGhlIHNhbXBsZSBub25jZQ==",
					"sec-websocket-version": "13", "authorization": "Bearer allow", "x-api-key": "key"}},
			want: answer{status: 200},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}}}},
		{name: "a WebSocket upgrade the check denies is answered with the denial", keys: []string{"auth.ask"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", raw: true,
				headers: map[string]string{"connection": "Upgrade", "upgrade": "websocket", "sec-websocket-key": "dGhlIHNhbXBsZSBub25jZQ==",
					"sec-websocket-version": "13", "authorization": "Bearer deny-401", "x-api-key": "key"}},
			want: answer{status: 401}},
		// Two requests on one kept-alive connection of the battery's HTTP/1.1 client, which sends a row's request on the
		// connection the row before left: what the check accepted for the first never reaches the service with the second.
		{name: "a request the check allows with an accepted header, before another on its connection",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, send: token("allow"),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country="}}},
		{name: "the next request on that connection, which the check allows with no header, receives none of the first's",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, send: token("allow-empty"),
			want: answer{status: 200, service: "shop", unmarked: true, absent: []string{"x-user-id", "x-tier-level", "x-user-check"}}},
		// A refusal leaves no timer of the check holding its connection: the next request on it is answered at once, far
		// under the 1.5 s auth.timeout a timer left waiting would hold it for (1 s is the bound, for a loaded runner).
		{name: "a check's 2xx other than 200 is answered at once on a kept-alive connection", keys: []string{"auth.ask"},
			send: token("status-201"), want: answer{status: 403, below: time.Second}},
		{name: "the next request on that connection is answered at once", keys: []string{"auth.ask"},
			send: token("allow"), want: answer{status: 200, service: "shop", below: time.Second}},
		// A refused request whose body holds a request, followed on its connection by the client's next request, in one
		// write: the body is read as the body and the next request is answered as a request of its own. Envoy reads the
		// body it has already received and keeps the connection (measured on Envoy 1.40.2); after the 400 of a dot
		// segment with a ; parameter it closes it, which a row of path normalisation holds.
		{name: "a request the check denies has its body read as a body, and the next request on its connection is answered",
			keys: []string{"auth.ask"}, send: thenNext("/products/1", "deny-401"), want: answer{first: 401, status: 200, service: "shop", path: "/products/2"}},
		{name: "a request whose check is unanswered has its body read as a body, and the next request on its connection is answered",
			keys: unanswered, send: thenNext("/products/1", "status-503"), want: answer{first: 200, status: 200, service: "shop", path: "/products/2"},
			on: map[string]literal{"two-checks": {want: answer{first: 403, status: 200, service: "shop", path: "/products/2"}},
				"nginx-checks": {want: answer{first: 403, status: 200, service: "shop", path: "/products/2"}}}},
		{name: "a request whose check's status is passed on has its body read as a body, and the next request on its connection is answered",
			keys: []string{"auth.ask"}, send: thenNext("/products/1", "status-600"), want: answer{first: 600, status: 200, service: "shop", path: "/products/2"},
			by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{first: 403, status: 200, service: "shop", path: "/products/2"}}},
				"nginx": {difference: "auth.ask", literal: literal{want: answer{first: 403, status: 200, service: "shop", path: "/products/2"}}}}},
		// A redirect and a 304 are the two denials nginx's handler writes in parts, which leaves a body unread, so it
		// writes them so only for a request whose head announces no body; these two hold that a body is read as a body.
		{name: "a request the check redirects has its body read as a body, and the next request on its connection is answered",
			keys: []string{"auth.ask"}, send: thenNext("/products/1", "redirect-relative"), want: answer{first: 302, status: 200, service: "shop", path: "/products/2"}},
		// HAProxy closes the client's connection after a check's 304, so the next request gets no answer (auth.ask).
		{name: "a request the check answers 304 has its body read as a body, and the next request on its connection is answered",
			keys: []string{"auth.ask"}, send: thenNext("/products/1", "status-304"), want: answer{first: 304, status: 200, service: "shop", path: "/products/2"},
			by: map[string]own{"haproxy": {difference: "auth.ask", literal: literal{want: answer{refused: true}}}}},
		{name: "a client's own x-proxyfile-method and x-proxyfile-path do not change what the check is told",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"authorization": "Bearer allow", "x-api-key": "key", "x-proxyfile-method": "DELETE", "x-proxyfile-path": "/admin"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "GET /products/1 action= reports= country="}}},
		// The path the check is told with send_request: the one the route matched, normalised where Envoy normalises it,
		// with the query as the client wrote it. nginx tells a character Envoy escapes as the client wrote it in a file
		// with no pattern route, and answers 400 in one with a pattern route (auth.send_request, routes[].pattern).
		{name: "a path with an escaped letter is told with the letter", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: tokenTo("allow", "/products/%41"),
			want: answer{status: 200, service: "shop", path: "/products/A", received: map[string]string{"x-user-check": "GET /products/A action= reports= country="}}},
		{name: "a path with a dot-dot segment and a query is told normalised, with its query as written",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, send: tokenTo("allow", "/catalog/../products/1?q=%41"),
			want: answer{status: 200, service: "shop", path: "/products/1?q=%41",
				received: map[string]string{"x-user-check": "GET /products/1?q=%41 action= reports= country="}},
			by: map[string]own{"haproxy": {difference: pathRow, literal: literal{want: answer{status: 200, service: "shop", path: "/products/1?q=A",
				received: map[string]string{"x-user-check": "GET /products/1?q=A action= reports= country="}}}}}},
		// HAProxy decodes the escape of an unreserved character in the query with no dot segment in the path too, and
		// keeps the escapes of reserved characters as written, as Envoy does (measured on HAProxy 3.2.26 and 3.4.7).
		{name: "a query's escape of an unreserved character reaches the service and the check as written",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, send: tokenTo("allow", "/products/1?q=%7e"),
			want: answer{status: 200, service: "shop", path: "/products/1?q=%7e",
				received: map[string]string{"x-user-check": "GET /products/1?q=%7e action= reports= country="}},
			by: map[string]own{"haproxy": {difference: pathRow, literal: literal{want: answer{status: 200, service: "shop", path: "/products/1?q=~",
				received: map[string]string{"x-user-check": "GET /products/1?q=~ action= reports= country="}}}}}},
		{name: "a query's escapes of reserved characters reach the service and the check as written",
			keys: []string{"auth.ask", "auth.accept", "auth.send_request"}, send: tokenTo("allow", "/products/1?q=%2F%26%3d"),
			want: answer{status: 200, service: "shop", path: "/products/1?q=%2F%26%3d",
				received: map[string]string{"x-user-check": "GET /products/1?q=%2F%26%3d action= reports= country="}}},
		{name: "a path holding a quote is told with the quote escaped", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: tokenTo("allow", `/products/"x`),
			want: answer{status: 200, service: "shop", path: "/products/%22x",
				received: map[string]string{"x-user-check": "GET /products/%22x action= reports= country="}},
			by: map[string]own{"nginx": {difference: "auth.send_request", files: []string{"nginx-checks"},
				literal: literal{want: answer{status: 200, service: "shop", path: "/products/%22x",
					received: map[string]string{"x-user-check": `GET /products/"x action= reports= country=`}}},
				other: &own{difference: "routes[].pattern", files: checkedPatternFiles, literal: literal{want: answer{status: 400}}}}}},
		// An HTTP/2 :scheme that differs from the connection's, which a client in front may send. Under client_address
		// connection the check and the service are told the connection's scheme, so https over TLS, and under
		// forwarded, on two-checks, nginx-checks and nginx-tracing, the check is told the :scheme http.
		{name: "an HTTP/2 request with :scheme http", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", raw: true, scheme: "http",
				headers: map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=https host=localhost",
				"x-forwarded-proto": "https"}},
			on: onEach([]string{"two-checks", "nginx-checks", "nginx-tracing"}, literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"x-user-check": "proto=http host=localhost"}}})},
		{name: "an HTTP/2 request with :scheme https", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", raw: true, scheme: "https",
				headers: map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=https host=localhost"}},
			by: map[string]own{"nginx": {difference: "auth.send_request", files: []string{"nginx-checks", "nginx-tracing"},
				literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=http host=localhost"}}}},
				"haproxy": {difference: "auth.send_request", files: []string{"two-checks"},
					literal: literal{want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=http host=localhost"}}}}}},
		{name: "a client's HTTP2-Settings of an h2c upgrade does not reach the check", keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"upgrade": "h2c", "connection": "Upgrade, HTTP2-Settings", "http2-settings": "AAMAAABkAAQAAP__",
					"authorization": "Bearer report-settings", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "http2-settings="}},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a client's HTTP2-Settings with no upgrade reaches the check when send covers it", keys: []string{"auth.ask", "auth.accept"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"http2-settings": "AAMAAABkAAQAAP__", "authorization": "Bearer report-settings", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "http2-settings="}},
			on: onEach([]string{"nginx-auth", "nginx-tracing"}, literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"x-user-check": "http2-settings=AAMAAABkAAQAAP__"}}})},
		// The request log: authz_us, how long the check took, is a string of digits when a check ran and null when none
		// ran; nginx's counts whole milliseconds (log), so the rows compare what both hold. A request the check could not
		// answer that goes on with the mark writes a gateway_auth_unavailable line, and no other request does. The files
		// these rows run on log a status of 400 or more and 1% of the rest, so each request they read is one of those, or
		// one that writes gateway_auth_unavailable, which every one does.
		{name: "a request the check denies logs how long the check took, and no unanswered check", keys: []string{"log", "auth.ask"},
			send: token("deny-401"), want: answer{status: 401,
				logged: map[string]map[string]*regexp.Regexp{"request": {"authz_us": logDigits}, "gateway_auth_unavailable": nil}}},
		{name: "a request with no only_with_header header logs no check time", keys: []string{"log", "auth.ask", "auth.only_with_header"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-status": "404"}},
			want: answer{status: 404, service: "shop", logged: map[string]map[string]*regexp.Regexp{"request": {"authz_us": logNull}}}},
		{name: "a request the check cannot answer, under allow, logs an unanswered check", keys: []string{"log", "auth.ask", "auth.when_unavailable"},
			send: token("status-503"), want: answer{status: 200, service: "shop", marked: "true",
				logged: map[string]map[string]*regexp.Regexp{"gateway_auth_unavailable": {"action": logText(""),
					"failure_mode_allowed": logText("true"), "route": logText("shop"), "status": logNumber(200)}}}},
		// A retried request is checked once, before its first try, and its retry carries what the check accepted.
		{name: "a request whose first connection was refused is served with the check's accepted header",
			keys: []string{"auth.ask", "auth.accept", "routes[].retries"}, send: tokenTo("allow", retriedPath), repeat: 20, every: true,
			refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1",
				received: map[string]string{"x-user-check": "GET /retried/products/1 action= reports= country="}}},
		{name: "a request the check denies is answered 403 while a backend refuses", keys: []string{"auth.ask", "routes[].retries"},
			send: tokenTo("deny-403", retriedPath), repeat: 6, every: true, refusing: "shop-2", want: answer{status: 403}},
		{name: "a request whose check is unavailable and whose first connection was refused carries the unchecked mark",
			keys: []string{"auth.ask", "auth.when_unavailable", "routes[].retries"}, send: tokenTo("status-503", retriedPath), repeat: 10,
			every: true, refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1", marked: "true"},
			// nginx-checks is deny.
			on: map[string]literal{"nginx-checks": {want: answer{status: 403}}}},
	}}

// token is a GET of the shop route on localhost whose authorization header carries the given token of the stub's
// check, with x-api-key, the header two-checks asks the check by, so every file with a check asks it.
// smuggled is a request a client writes as the body of another, and nextRequest the request it sends next on the same
// connection; the check allows both.
const (
	smuggled    = "GET /products/smuggled HTTP/1.1\r\nHost: localhost\r\nauthorization: Bearer allow\r\nx-api-key: key\r\n\r\n"
	nextRequest = "GET /products/2 HTTP/1.1\r\nHost: localhost\r\nauthorization: Bearer allow\r\nx-api-key: key\r\n\r\n"
)

// thenNext is a POST to path with the token, whose body is smuggled, followed in the same write by nextRequest.
func thenNext(path, name string) request {
	return thenAnother(request{proto: "http/1.1", method: "POST", host: "localhost", path: path,
		headers: map[string]string{"authorization": "Bearer " + name, "x-api-key": "key"}})
}

// keptAfter is the literal of a row sent with thenAnother on a proxy that keeps the connection after its answer of
// status to the first request: the request after the body reaches shop, and the body is read as a body.
func keptAfter(status int) literal {
	return literal{want: answer{status: 200, service: "shop", path: "/products/2", first: status}}
}

// thenAnother is r sent by hand with smuggled as its body, framed by a Content-Length, and nextRequest after it in the
// same write, as a client that reuses its connection sends its next request: a proxy that closes the connection after
// its answer to r answers nextRequest with nothing.
func thenAnother(r request) request {
	r.headers = maps.Clone(r.headers)
	if r.headers == nil {
		r.headers = map[string]string{}
	}
	r.headers["content-length"] = strconv.Itoa(len(smuggled))
	r.raw, r.unframed, r.body, r.then = true, true, smuggled, nextRequest
	return r
}

func token(name string) request {
	r := get("/products/1")
	r.headers = map[string]string{"authorization": "Bearer " + name, "x-api-key": "key"}
	return r
}

// tokenTo is token's request to path.
func tokenTo(name, path string) request {
	r := token(name)
	r.path = path
	return r
}

// withBody is r, a request sent by hand, with the body abc, which sendRaw sends chunked.
func withBody(r request) request {
	r.body = "abc"
	return r
}

// withLength is r, a request sent by hand, with the body abc framed by a Content-Length line.
func withLength(r request) request {
	r = withBody(r)
	r.headers = maps.Clone(r.headers)
	r.headers["content-length"] = strconv.Itoa(len(r.body))
	r.unframed = true
	return r
}

// rpcToken is token's two headers, and more beside them, on a gRPC call to shop's GetCart.
func rpcToken(name string, more map[string]string) request {
	r := rpc("/example.shop.v1.CartService/GetCart")
	r.headers = token(name).headers
	maps.Copy(r.headers, more)
	return r
}

// rawToken is token's request sent by hand, with more headers beside its two, whose values may hold any byte.
func rawToken(name string, more map[string]string) request {
	r := token(name)
	maps.Copy(r.headers, more)
	r.raw = true
	return r
}

// logDigits and logNull are the two forms of authz_us in the request log's line (answer.logged): a JSON string of
// digits when a check ran, and null when none did.
var (
	logDigits = regexp.MustCompile(`^"[0-9]+"$`)
	logNull   = regexp.MustCompile(`^null$`)
)

// logText is a field of a log line that holds the JSON string text.
func logText(text string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(strconv.Quote(text)) + "$")
}

// logNumber is a field of a log line that holds the JSON number n.
func logNumber(n int) *regexp.Regexp {
	return regexp.MustCompile("^" + strconv.Itoa(n) + "$")
}

// unanswered are what the rows of an answer the check could not give depend on: the check, and what the proxy does
// then.
var unanswered = []string{"auth.ask", "auth.when_unavailable"}

// markedOnly is the answer to a request whose check gave no answer the proxy could read, with when_unavailable =
// allow: the service receives it with the unchecked mark and without the user the check's answer named.
var markedOnly = answer{status: 200, service: "shop", marked: "true", absent: []string{"x-user-id"}}

// deniedUnderDeny is the same request's answer on two-checks and nginx-checks, whose when_unavailable is deny.
var deniedUnderDeny = map[string]literal{"two-checks": {want: answer{status: 403}}, "nginx-checks": {want: answer{status: 403}}}

// deniedLateUnderDeny is the answer under deny to a request whose check did not answer within auth.timeout, each bound 50 ms
// from the time it tests, for the two clocks (timeout).
var deniedLateUnderDeny = map[string]literal{
	"two-checks":   {want: answer{status: 403, atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond}},
	"nginx-checks": {want: answer{status: 403, atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond}},
}

// refusedUnderAllow is HAProxy's and nginx's 403 to an answer their scripts cannot read, which Envoy serves with the
// mark under allow (auth.when_unavailable); on two-checks and nginx-checks, under deny, each answers 403.
var refusedUnderAllow = map[string]own{
	"haproxy": {difference: "auth.when_unavailable", files: []string{"haproxy-file"}, literal: literal{want: answer{status: 403}}},
	"nginx":   {difference: "auth.when_unavailable", files: allowFiles, literal: literal{want: answer{status: 403}}},
}

// toldScheme: measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8 and HAProxy 3.2.26 and 3.4.7. Each row asks the
// check, which reports the scheme it was told, and the service reports the x-forwarded-proto it received. The rows
// whose keys hold client_address run on the files with client_address connection and send_request: full, portable,
// nginx-auth and haproxy-file, which write tls, and connection.yaml, which writes none, so it is sent over plain HTTP.
// The others run on every file with send_request, two-checks, nginx-checks and nginx-tracing among them, which write no
// tls and take forwarded.
//
// Under connection every proxy tells the check the connection's scheme, the one the service is sent, whatever the
// client writes: the scheme of an absolute target, in any letter case, an HTTP/2 :scheme, ftp among them, or its own
// x-forwarded-proto of http, https or ftp. Here each way names https, the scheme other than the connection's on
// connection.yaml, and x-forwarded-proto names http too, the other scheme over TLS; the auth scenario's rows "an
// absolute target's host is the host the check is told, whatever the Host header" and "an HTTP/2 request with :scheme
// http" send the request line and the :scheme naming http over TLS, and want the check and the service told https.
// HAProxy's public listener answers every absolute target 400 over HTTP/1.1, and over plain HTTP the Envoy output
// answers an absolute https target 403, HTTPS:// too (its codec's https_url_on_plaintext_connection, which compares the
// scheme in any letter case); neither asks the check, as the same request with a token the check denies shows, which a
// proxy that asks the check answers 401. nginx serves that https target, in either letter case, and tells the check
// http, a way nginx is less strict that its list names.
//
// Under forwarded an absolute https target sent with x-forwarded-proto http is told https by the Envoy output and nginx,
// the target's scheme before the header, while the service is sent the header's http, and an HTTP/2 :scheme https
// sent with it is told https by the Envoy output and http, the header's, by nginx and HAProxy (auth.send_request). An
// x-forwarded-proto of ftp reaches the service through every proxy, and the Envoy output tells the check the
// connection's scheme, http, where nginx and HAProxy tell it ftp as written (auth.send_request).
var toldScheme = scenario{name: "the scheme the check is told",
	exchanges: []exchange{
		{name: "an absolute https target", keys: connectionKeys,
			send: request{proto: "http/1.1", host: "localhost", line: "GET https://localhost/products/1 HTTP/1.1", raw: true,
				headers: map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", host: "localhost", received: map[string]string{"x-user-check": "proto=https host=localhost"}},
			on:   map[string]literal{"connection": {want: answer{status: 403}}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: "client_address", files: []string{"connection"}, literal: literal{want: answer{status: 200,
					service: "shop", host: "localhost", received: map[string]string{"x-user-check": "proto=http host=localhost"}}}}}},
		{name: "an absolute https target with a token the check denies", keys: connectionKeys,
			send: request{proto: "http/1.1", host: "localhost", line: "GET https://localhost/products/1 HTTP/1.1", raw: true,
				headers: map[string]string{"authorization": "Bearer deny-401", "x-api-key": "key"}},
			want: answer{status: 401},
			on:   map[string]literal{"connection": {want: answer{status: 403}}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: "client_address", files: []string{"connection"}, literal: literal{want: answer{status: 401}}}}},
		{name: "an absolute http target with a token the check denies", keys: connectionKeys,
			send: request{proto: "http/1.1", host: "localhost", line: "GET http://localhost/products/1 HTTP/1.1", raw: true,
				headers: map[string]string{"authorization": "Bearer deny-401", "x-api-key": "key"}},
			want: answer{status: 401},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an absolute HTTPS target in capitals", keys: connectionKeys,
			send: request{proto: "http/1.1", host: "localhost", line: "GET HTTPS://localhost/products/1 HTTP/1.1", raw: true,
				headers: schemeReport("")},
			want: withHost(toldTo("https", "https")),
			on:   map[string]literal{"connection": {want: answer{status: 403}}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: "client_address", files: []string{"connection"}, literal: literal{want: withHost(toldTo("http", "http"))}}}},
		{name: "an HTTP/2 request with :scheme https under connection", keys: connectionKeys,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", raw: true, scheme: "https",
				headers: schemeReport("")},
			want: toldTo("https", "https"),
			on:   map[string]literal{"connection": {want: toldTo("http", "http")}}},
		{name: "an HTTP/2 request with :scheme ftp under connection", keys: connectionKeys,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", raw: true, scheme: "ftp",
				headers: schemeReport("")},
			want: toldTo("https", "https"),
			on:   map[string]literal{"connection": {want: toldTo("http", "http")}}},
		{name: "a client's own x-forwarded-proto https under connection", keys: connectionKeys,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: schemeReport("https")},
			want: toldTo("https", "https"),
			on:   map[string]literal{"connection": {want: toldTo("http", "http")}}},
		{name: "a client's own x-forwarded-proto http under connection", keys: connectionKeys,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: schemeReport("http")},
			want: toldTo("https", "https"),
			on:   map[string]literal{"connection": {want: toldTo("http", "http")}}},
		{name: "an absolute https target sent with x-forwarded-proto http", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "http/1.1", host: "localhost", line: "GET https://localhost/products/1 HTTP/1.1", raw: true,
				headers: schemeReport("http")},
			want: withHost(toldTo("https", "https")),
			on: map[string]literal{"two-checks": {want: withHost(toldTo("https", "http"))}, "nginx-checks": {want: withHost(toldTo("https", "http"))},
				"nginx-tracing": {want: withHost(toldTo("https", "http"))}, "connection": {want: answer{status: 403}}},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}},
				"nginx": {difference: "client_address", files: []string{"connection"}, literal: literal{want: withHost(toldTo("http", "http"))}}}},
		{name: "an HTTP/2 request with :scheme https sent with x-forwarded-proto http", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", raw: true, scheme: "https",
				headers: schemeReport("http")},
			want: toldTo("https", "https"),
			on: map[string]literal{"two-checks": {want: toldTo("https", "http")}, "nginx-checks": {want: toldTo("https", "http")},
				"nginx-tracing": {want: toldTo("https", "http")}, "connection": {want: toldTo("http", "http")}},
			by: map[string]own{"nginx": {difference: "auth.send_request", files: []string{"nginx-checks", "nginx-tracing"},
				literal: literal{want: toldTo("http", "http")}},
				"haproxy": {difference: "auth.send_request", files: []string{"two-checks"}, literal: literal{want: toldTo("http", "http")}}}},
		{name: "an x-forwarded-proto that is neither http nor https", keys: []string{"auth.ask", "auth.accept", "auth.send_request"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: schemeReport("ftp")},
			want: toldTo("https", "https"),
			on: map[string]literal{"two-checks": {want: toldTo("http", "ftp")}, "nginx-checks": {want: toldTo("http", "ftp")},
				"nginx-tracing": {want: toldTo("http", "ftp")}, "connection": {want: toldTo("http", "http")}},
			by: map[string]own{"nginx": {difference: "auth.send_request", files: []string{"nginx-checks", "nginx-tracing"},
				literal: literal{want: toldTo("ftp", "ftp")}},
				"haproxy": {difference: "auth.send_request", files: []string{"two-checks"}, literal: literal{want: toldTo("ftp", "ftp")}}}},
	}}

// connectionKeys are what the rows of toldScheme under client_address connection depend on: the connection's scheme
// is the check's word only with that key (client_address), and the check reports it (auth.ask, auth.send_request) in a
// header its answer echoes to the service (auth.accept).
var connectionKeys = []string{"client_address", "auth.ask", "auth.accept", "auth.send_request"}

// schemeReport is the headers of a request the check answers with the scheme it was told (report-forwarded), with the
// client's own x-forwarded-proto when forwardedProto is not empty.
func schemeReport(forwardedProto string) map[string]string {
	headers := map[string]string{"authorization": "Bearer report-forwarded", "x-api-key": "key"}
	if forwardedProto != "" {
		headers["x-forwarded-proto"] = forwardedProto
	}
	return headers
}

// toldTo is a request of toldScheme served by shop, whose check was told the scheme check and whose service received
// the x-forwarded-proto service.
func toldTo(check, service string) answer {
	return answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "proto=" + check + " host=localhost",
		"x-forwarded-proto": service}}
}

// withHost is a, which also wants the service to receive the host localhost, the one an absolute target names.
func withHost(a answer) answer {
	a.host = "localhost"
	return a
}

// timeout: measured on Envoy 1.40.2. The check holds the request for 3 s, past auth.timeout's 1500 ms, and
// when_unavailable is allow, so the request goes on to the service when the timeout ends, with the unchecked mark, as a
// request the check could not answer must carry (the auth scenario names it). The row times the request on the client's
// clock, while the proxy times auth.timeout and the stub times its 3 s on clocks of their own, so each bound keeps 50
// ms from the time it tests. Envoy makes auth.timeout the deadline of its client for the check (ext_authz_http_impl.cc,
// setTimeout; router.cc, Filter::onRequestComplete, enableTimer, which takes milliseconds), and libevent counts that
// deadline from the time its event loop last read, which on Linux comes from CLOCK_MONOTONIC_COARSE because Envoy does
// not set EVENT_BASE_FLAG_PRECISE_TIMER (libevent's evutil_time.c gives that clock an accuracy of about 1 to 4 ms;
// under Docker Desktop's kernel, CONFIG_HZ=1000, it moves in 1 ms steps). On one arm64 Mac running the proxy in Docker
// Desktop, 2 of 30 runs saw the answer at 1.499558542 s and 1.499792084 s, 0.44 ms and 0.21 ms short of 1500 ms. 50 ms
// is fifty of those 1 ms steps and still far from both wrong answers: a request answered at once takes a few
// milliseconds, and one that waited for the check's own answer takes 3 s. The two rows of a body that stops coming hold
// the same bounds for the same reasons: the check's 401, and its 200, start at once and send their body of 100 bytes a
// byte every 100 ms, so a proxy that waited for the whole body would answer after 10 s. Under allow the request goes
// on with the mark and under deny, on two-checks, it is answered 403. On an arm64 Mac with 12 CPUs every row here was
// answered in 1.50 to 1.51 s on Envoy 1.40.2 and HAProxy 3.2.26 and 3.4.7 in each run of the battery, and the check's
// answers alone, with each proxy pinned to 4 of those CPUs, in 1.502 to 1.518 s; on GitHub's 4-CPU amd64 runner the
// pull request's Required run is the measurement.
//
// With timeouts.idle, the battery file's 2 s, a service silent for longer is answered 504 when the idle time ends, an
// answer whose body pauses for less is served whole, and a request whose body pauses for longer is answered 408 by
// Envoy, where nginx ends it with no answer (timeouts.idle); the 504 is Envoy's own text, as text/plain, where nginx's
// own page would be text/html. The stub holds its answer for the time x-stub-delay names, and with x-stub-trickle:
// 2x1250ms sends its body after two pauses of 1.25 s, a space after each: each pause is 0.75 s under the idle time and
// the answer ends 0.5 s past it, so a limit on the whole answer would end it. Each bound keeps 50 ms from the time it
// tests, for the two clocks, and the 504 comes within 1 s past the idle time, for a loaded runner: measured at 2.002 to
// 2.005 s on Envoy 1.40.2 and nginx. examples/full and examples/portable write idle: 90s, so they serve each.
var timeout = scenario{name: "timeout",
	exchanges: []exchange{
		{name: "the check answers after auth.timeout", overlap: true, keys: []string{"auth.timeout", "auth.when_unavailable"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer hang", "x-api-key": "key"}},
			want: answer{status: 200, service: "shop", marked: "true", atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond},
			on:   deniedLateUnderDeny},
		{name: "a denial whose body stops coming, past auth.timeout, is an unanswered check", overlap: true, keys: []string{"auth.timeout", "auth.when_unavailable"},
			send: token("trickle"), want: answer{status: 200, service: "shop", marked: "true", atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond},
			on: deniedLateUnderDeny},
		{name: "a 200 whose body stops coming, past auth.timeout, is an unanswered check", overlap: true, keys: []string{"auth.timeout", "auth.when_unavailable"},
			send: token("trickle-200"), want: answer{status: 200, service: "shop", marked: "true", atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond},
			on: deniedLateUnderDeny},
		{name: "a service silent past the idle time", overlap: true, keys: []string{"timeouts.idle"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-delay": "3s"}},
			want: answer{status: 504, headers: map[string]string{"content-type": "text/plain"}, atLeast: 1950 * time.Millisecond,
				below: 2950 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", atLeast: 2950 * time.Millisecond}},
				"portable": {want: answer{status: 200, service: "shop", atLeast: 2950 * time.Millisecond}}}},
		{name: "an answer that pauses less than the idle time", overlap: true, keys: []string{"timeouts.idle"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-trickle": "2x1250ms"}},
			want: answer{status: 200, service: "shop", atLeast: 2450 * time.Millisecond}},
		{name: "a request body that pauses past the idle time", overlap: true, keys: []string{"timeouts.idle"},
			send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/products/1", raw: true, unframed: true,
				headers: map[string]string{"content-length": "4"}, body: "abcd", pause: 3 * time.Second},
			want: answer{status: 408, atLeast: 1950 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", body: "abcd"}},
				"portable": {want: answer{status: 200, service: "shop", body: "abcd"}}},
			by: map[string]own{"nginx": {difference: "timeouts.idle", files: nginxYAMLFiles, literal: literal{want: answer{refused: true}}}}},
	}}

// retry: every value measured on Envoy 1.40.2. A request that reached the service is never tried again, and one whose
// connection a backend refused is tried again on a backend it has not tried: with shop-2 refusing connections, each of
// 40 requests and of 40 gRPC calls is answered by shop-1, where a retry that picked its backend as the first try did
// left 13 of the 40 requests answered 502 and 12 of the 40 calls with grpc-status 14. 40 requests send about 20 first
// tries to shop-2, so a retry that still picked shop-2 now and then would fail the row. Envoy also answers, and no row
// asserts: the 502 with an empty body, and the gRPC call with HTTP 200 and, in the headers, grpc-status: 14 and
// grpc-message: upstream connect error or disconnect/reset before headers. reset reason: remote reset. nginx-file writes
// retries: 0 on the shop route, so there each request shop-2 draws is answered 502 by the proxy, with no service, and
// tried on no other backend: 12 to 22 of the 40 in each of ten runs with the file's least_requests, and 20 in each of
// ten with round robin (measured on Envoy 1.40.2), so there the row wants each answer to be that 502 or shop-1's, and
// at least one of each, which a proxy that answered every request 502 would fail; nginx-plain, nginx-auth, its copy
// nginx-tracing and nginx-checks write no retries on their shop routes either, so the row wants the same there.
//
// The rows that send to /retried/products/1 send to the route the nginx battery files retry once, retried, which the
// Envoy entry's own files serve by their shop route, which retries. nginx reads the body of a request to a retried
// route whole before the first try when its Content-Length is at most 1 MiB, when it has no body over HTTP/1.1 (neither
// Content-Length nor Transfer-Encoding), or when a GET, HEAD, OPTIONS or DELETE over HTTP/2 has no content-length, and
// sends any other body as it arrives, never trying it again, so the rows send bodies at and over that bound, chunked
// and over HTTP/2 with no content-length. With shop-2 refusing connections: each of 40 requests is answered by
// shop-1, and each of 40 POSTs with no body and no Content-Length; each of 40 GETs whose body of 180,000 bytes comes in
// one-byte chunks reaches shop-1 whole, where nginx answers 502 each one shop-2 drew (routes[].retries), as chunk lines
// count against the buffer it reads a retried body into; each of 40 PUTs of 1 MiB reaches shop-1 whole; of 40 PUTs of
// one byte more, over Envoy's buffer of 1 MiB, at least one is answered 507, where the Envoy output cannot hold the
// body for a retry, and nginx answers that one 502 (routes[].retries); a chunked PUT, and a PUT over HTTP/2 with no
// content-length, are tried again by Envoy, at least one of 40 reaching shop-1 with its body, where nginx answers 502
// (routes[].retries). A row that needs shop-2 drawn at least once, or each backend, sends 40 requests: where the
// backend is drawn at random, as Envoy's workers each start their round robin at a random number, all 40 miss it with
// a chance of 2^-40, about one in 10^12, where 10 requests missed it with a chance of one in 1,024. nginx-plain retries
// no route of retriedPath, so its shop route serves the rows that send there and tries each request once
// (onceOnPlain). A PUT whose
// first try the service closed once it arrived is answered 502 and not tried again, over HTTP/1.1 and over HTTP/2,
// which net/http's client sends with a content-length, as the raw reader does not decode the 502 Envoy codes with
// Huffman. A request whose every backend refuses is answered 502 and writes one upstream-failure line with the flags
// URX,UF, its route and the status 502, on Envoy and on both nginx versions: that line carries no request id, so the
// row finds it by route and status among the lines written after the row began (countedLines), and no row beside it
// in this scenario sends to the route. A PUT over 1 MiB with no backend refusing is served once, whole. Envoy also
// answers, and no row asserts: the bodies of its 502 and 507.
var retry = scenario{name: "retry",
	exchanges: []exchange{
		{name: "a first try closed after it reached the service is not tried again", keys: []string{"routes[].retries"}, send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-fail-first": "shop-once"}}, want: answer{status: 502}},
		{name: "an answer of 503 is passed on", keys: []string{"routes[].retries"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"x-stub-status": "503"}}, want: answer{status: 503, service: "shop", attempt: 1}},
		{name: "a gRPC call whose first try was reset is not tried again", keys: []string{"routes[].rpc.retries"}, send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"x-stub-fail-first": "cart-once"}}, want: answer{grpcStatus: "14"}},
		// A gRPC call sent as a GET over HTTP/1.1 whose first try was reset is answered 200 with grpc-status 14 and no
		// service, as the POST is: a second try would reach the stub, which answers its key's second arrival.
		{name: "a gRPC call that is not POST is not tried again", keys: []string{"routes[].rpc.retries"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"content-type": "application/grpc", "x-stub-fail-first": "cart-get-once"}},
			want: answer{status: 200, grpcStatus: "14"}},
		{name: "a retry after a connection failure reaches the other backend", keys: []string{"routes[].retries"}, send: get("/products/1"), repeat: 40, every: true,
			refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1"},
			on: onEach(nginxFiles, literal{want: answer{status: 502}, also: &answer{status: 200, service: "shop", instance: "shop-1"}})},
		{name: "a gRPC call retried after a connection failure reaches the other backend", keys: []string{"routes[].rpc.retries"},
			send: rpc("/example.shop.v1.CartService/GetCart"), repeat: 40, every: true, refusing: "shop-2",
			want: answer{status: 200, grpcStatus: "0", service: "shop", instance: "shop-1"}},
		// nginx-plain sends example.hold.* to its held service, whose two backends take no connection, and retries once:
		// the Envoy output answers 200 with grpc-status 14 when the second try's 5 s connect timeout ends (measured on
		// Envoy 1.40.2, 10.0 s), 50 ms under for the two clocks and 1 s over for a loaded Mac, as the hold row. Every
		// other file that writes retries lists no such method, so the call reaches the shop route's service, which
		// answers it.
		{name: "a retried gRPC call whose every try times out connecting is answered 200 with grpc-status 14", overlap: true,
			keys: []string{"routes[].rpc.retries"}, send: rpc("/example.hold.v1.S/Call"),
			want: answer{status: 200, service: "shop"},
			on: map[string]literal{"nginx-plain": {want: answer{status: 200, grpcStatus: "14", atLeast: 9950 * time.Millisecond,
				below: 10950 * time.Millisecond}}}},
		// nginx-plain's route held retries once to the same two backends: the Envoy output answers 502 with no body when
		// the second try's 5 s connect timeout ends (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8, 10.0 s by
		// all three, each nginx try 5.0 s), the bound as the gRPC row's. nginx gives that answer only when the second
		// try is made, in the copy it takes: a script that found the time spent after the first try answered 504 at
		// 5 s, and a copy left out 500. The row does not hold the time left after a try: both tries end at the
		// connect time, which every copy shares, so a script that counts no time and sends the second try with the
		// route's whole time gives the same answer (measured on nginx 1.30.6 and 1.31.8: the row passes with it).
		// Every other file sends the path to its shop route.
		{name: "a retried request whose every try times out connecting is answered 502 after both tries", overlap: true,
			keys: []string{"routes[].retries"}, send: get("/held/products/1"),
			want: answer{status: 200, service: "shop"},
			on: map[string]literal{"nginx-plain": {want: answer{status: 502, atLeast: 9950 * time.Millisecond,
				below: 10950 * time.Millisecond}}}},
		{name: "a request to a retried route whose first connection was refused reaches the other backend", keys: []string{"routes[].retries"},
			send: get(retriedPath), repeat: 40, every: true, refusing: "shop-2",
			want: answer{status: 200, service: "shop", instance: "shop-1"}, on: onceOnPlain(answer{status: 200, service: "shop", instance: "shop-1"})},
		{name: "a POST with no body and no Content-Length whose first connection was refused reaches the other backend",
			keys: []string{"routes[].retries"}, send: request{proto: "http/1.1", method: "POST", host: "localhost", path: retriedPath, raw: true},
			repeat: 40, every: true, refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1", method: "POST"},
			on: onceOnPlain(answer{status: 200, service: "shop", instance: "shop-1", method: "POST"})},
		{name: "a GET in one-byte chunks whose first connection was refused is not tried again", keys: []string{"routes[].retries"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: retriedPath, raw: true, unframed: true,
				headers: map[string]string{"transfer-encoding": "chunked"}, body: oneByteChunks},
			repeat: 40, every: true, refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1", body: oneByteBody},
			on: onceOnPlain(answer{status: 200, service: "shop", instance: "shop-1", body: oneByteBody}),
			by: map[string]own{"nginx": {difference: "routes[].retries", literal: literal{want: answer{status: 502},
				also: &answer{status: 200, service: "shop", instance: "shop-1", body: oneByteBody}}}}},
		// The two rows of a PUT whose first try the service closed once it arrived send a body with a length of at most
		// 1 MiB, which nginx's jump map sends to the retried route's location, the one that reads the body whole and may
		// try it again: they hold that a try that reached the service is not repeated only while they reach it. Sent to
		// the copy that tries once, or to a route that does not retry, as nginx-plain's shop route serving retriedPath,
		// they are answered 502 whatever that location would do.
		{name: "a PUT whose first try was closed after it reached the service is not tried again", keys: []string{"routes[].retries"},
			send: putTo(retriedPath, "hello", map[string]string{"content-length": "5", "x-stub-fail-first": "shop-put-once"}),
			want: answer{status: 502}},
		{name: "a PUT over HTTP/2 whose first try was closed after it reached the service is not tried again", keys: []string{"routes[].retries"},
			send: request{proto: "h2", method: "PUT", host: "localhost", path: retriedPath, body: "hello",
				headers: map[string]string{"x-stub-fail-first": "shop-put-once-h2"}},
			want: answer{status: 502}},
		{name: "a PUT of 1 MiB whose first connection was refused reaches the other backend whole", keys: []string{"routes[].retries"},
			send: putTo(retriedPath, mebibyte, map[string]string{"content-length": "1048576"}), repeat: 40, every: true, refusing: "shop-2",
			want: answer{status: 200, service: "shop", instance: "shop-1", body: mebibyte},
			on:   onceOnPlain(answer{status: 200, service: "shop", instance: "shop-1", body: mebibyte})},
		{name: "a request whose every backend refuses is answered 502 and logged URX,UF", keys: []string{"routes[].retries", "log"},
			send: get(retriedPath), refusing: "shop-1,shop-2", want: answer{status: 502, counted: refusedEverywhere("retried")},
			on: map[string]literal{"full": {want: answer{status: 502, counted: refusedEverywhere("shop")}},
				"haproxy-traffic": {want: answer{status: 502, counted: refusedEverywhere("shop")}}}},
		{name: "a PUT over 1 MiB whose first connection was refused is not tried again", keys: []string{"routes[].retries"},
			send: putTo(retriedPath, overMebibyte, map[string]string{"content-length": "1048577"}), repeat: 40, refusing: "shop-2",
			want: answer{status: 507}, on: map[string]literal{"nginx-plain": {want: answer{status: 502}}},
			by: map[string]own{"nginx": {difference: "routes[].retries", literal: literal{want: answer{status: 502}}}}},
		{name: "a chunked PUT whose first connection was refused is not tried again", keys: []string{"routes[].retries"},
			send:   request{proto: "http/1.1", method: "PUT", host: "localhost", path: retriedPath, raw: true, body: "hello"},
			repeat: 40, refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1", body: "hello"},
			by: map[string]own{"nginx": {difference: "routes[].retries", literal: literal{want: answer{status: 502}}}}},
		{name: "a PUT over HTTP/2 with no content-length whose first connection was refused is not tried again", keys: []string{"routes[].retries"},
			send:   request{proto: "h2", method: "PUT", host: "localhost", path: retriedPath, raw: true, body: "hello"},
			repeat: 40, refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1", body: "hello"},
			by: map[string]own{"nginx": {difference: "routes[].retries", literal: literal{want: answer{status: 502}}}}},
		{name: "a PUT over 1 MiB is served once whole", keys: []string{"routes[].retries"},
			send: putTo(retriedPath, overMebibyte, map[string]string{"content-length": "1048577"}),
			want: answer{status: 200, service: "shop", attempt: 1, body: overMebibyte}},
	}}

// onceOnPlain is the literal on nginx-plain of a row that sends to retriedPath with shop-2 refusing connections and
// wants served from shop-1 every time: nginx-plain retries no route of that path, so its shop route serves it and tries
// each request once, and each request shop-2 draws is answered 502 by the proxy, with no service (measured on Envoy
// 1.40.2).
func onceOnPlain(served answer) map[string]literal {
	return map[string]literal{"nginx-plain": {want: answer{status: 502}, also: &served}}
}

// refusedEverywhere is the upstream-failure line of a request of route whose every try was refused, answered 502: it
// carries no request id, so the row finds it by its route and status (countedLines).
func refusedEverywhere(route string) *countedLines {
	return &countedLines{event: "gateway_upstream_failure", find: map[string]*regexp.Regexp{"route": logText(route), "status": logNumber(502)},
		fields: map[string]*regexp.Regexp{"flags": logText("URX,UF")}}
}

// retriedPath is a path of the route the nginx battery files retry once, retried, which only the rows of a retried
// request send to, in the retry and auth scenarios: on the Envoy entry's own files the shop route, which retries,
// serves it.
const retriedPath = "/retried/products/1"

// mebibyte and overMebibyte are bodies of 1 MiB, the most the nginx output reads whole and may try again, and of one
// byte more.
var (
	mebibyte     = strings.Repeat("x", 1048576)
	overMebibyte = strings.Repeat("x", 1048577)
)

// oneByteBody is a body of 180,000 bytes, and oneByteChunks the same body as chunks of one byte, 1,080,005 bytes on the
// wire: more than the 1,025 KiB nginx's retried route location reads a body into, chunk lines included.
var (
	oneByteBody   = strings.Repeat("x", 180000)
	oneByteChunks = strings.Repeat("1\r\nx\r\n", 180000) + "0\r\n\r\n"
)

// putTo is a PUT of body to path over HTTP/1.1, sent by hand with headers and the body as it is, so the headers frame it.
func putTo(path, body string, headers map[string]string) request {
	return request{proto: "http/1.1", method: "PUT", host: "localhost", path: path, raw: true, unframed: true, headers: headers, body: body}
}

// gRPC: every value measured on Envoy 1.40.2. The question the check is told, x-gateway-action and
// x-gateway-reports, is for the check alone, so the service receives neither. A gRPC service's X-Accel-Redirect
// reaches the client as an HTTP service's does (routing), and a call is routed by the path normalisation leaves.
var gRPC = scenario{name: "gRPC",
	exchanges: []exchange{
		{name: "a method of a package mapped to shop", keys: []string{"routes[].rpc", "routes[].rpc.to", "routes[].rpc.to.<package pattern>",
			"routes[].rpc.to.<package pattern>.service", "rpc.methods", "rpc.methods[].service", "rpc.methods[].method", "services.<service name>.grpc"},
			send: rpc("/example.shop.v1.CartService/GetCart"),
			want: answer{status: 200, grpcStatus: "0", service: "shop", port: "8081", proto: "HTTP/2.0", path: "/example.shop.v1.CartService/GetCart"}},
		// With both shop backends refusing connections the call is answered as Envoy answers a gRPC call it cannot
		// send, 200 with grpc-status 14 and no body in one header frame, and no grpc-message.
		{name: "a gRPC call to a service whose every backend refuses the connection is answered 200 with grpc-status 14",
			keys: []string{"routes[].rpc", "services.<service name>.grpc"}, send: rpc("/example.shop.v1.CartService/GetCart"),
			refusing: "shop-1,shop-2", want: answer{status: 200, grpcStatus: "14", headers: map[string]string{"content-type": "application/grpc",
				"grpc-message": ""}}},
		// A service that resets the call's stream before its answer, as the stub resets the first try of an
		// x-stub-fail-first key: Envoy answers 200 with grpc-status 14 and a grpc-message that names the reset, and nginx
		// the same with no grpc-message (services.<service name>.grpc).
		{name: "a gRPC call whose service resets its stream is answered 200 with grpc-status 14",
			keys: []string{"routes[].rpc", "services.<service name>.grpc"}, send: request{proto: "grpc", method: "POST",
				host: "localhost", path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"x-stub-fail-first": "grpc-stream-reset"}},
			want: answer{status: 200, grpcStatus: "14", headers: map[string]string{"content-type": "application/grpc",
				"grpc-message": "upstream connect error or disconnect/reset before headers. reset reason: remote reset"}},
			by: map[string]own{"nginx": {difference: "services.<service name>.grpc", literal: literal{want: answer{status: 200,
				grpcStatus: "14", headers: map[string]string{"content-type": "application/grpc", "grpc-message": ""}}}}}},
		// A request that is not a gRPC call, sent to a gRPC method's route, whose service resets its stream before its
		// answer, as the stub resets the first try of an x-stub-fail-first key: Envoy answers 503 with its text,
		// upstream connect error or disconnect/reset before headers. reset reason: remote reset, 85 bytes of text/plain,
		// and nginx and HAProxy 502 with no body (services.<service name>.grpc).
		{name: "a request that is not a gRPC call, on a gRPC route whose service resets its stream, is answered 503",
			keys: []string{"routes[].rpc", "services.<service name>.grpc"}, send: request{proto: "h2", method: "POST",
				host: "localhost", path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"content-type": "text/plain",
					"x-stub-fail-first": "plain-stream-reset"}},
			want: answer{status: 503, headers: map[string]string{"content-type": "text/plain", "content-length": "85"}},
			by: map[string]own{"nginx": {difference: "services.<service name>.grpc", literal: literal{want: answer{status: 502,
				headers: map[string]string{"content-length": "0"}}}},
				"haproxy": {difference: "services.<service name>.grpc", literal: literal{want: answer{status: 502,
					headers: map[string]string{"content-length": "0"}}}}}},
		{name: "a method every other package sends to accounts", keys: []string{"routes[].rpc.to.<package pattern>", "rpc.methods",
			"services.<service name>.grpc"}, send: rpc("/example.accounts.v1.ProfileService/GetProfile"),
			want: answer{status: 200, grpcStatus: "0", service: "accounts", port: "7070", proto: "HTTP/2.0", host: "127.0.0.1:7070"}},
		{name: "a gRPC service's X-Accel-Redirect is passed on, not followed", keys: []string{"routes[].rpc", "routes[].rpc.to",
			"routes[].rpc.to.<package pattern>", "routes[].rpc.to.<package pattern>.service", "rpc.methods", "services.<service name>.grpc"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"x-stub-accel-redirect": "/example.accounts.v1.ProfileService/GetProfile"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", path: "/example.shop.v1.CartService/GetCart"}},
		{name: "a gRPC call through a dot-dot segment reaches the method's path", keys: []string{"routes[].rpc", "rpc.methods"},
			send: rpc("/x/../example.shop.v1.CartService/GetCart"),
			want: answer{status: 200, grpcStatus: "0", service: "shop", path: "/example.shop.v1.CartService/GetCart"}},
		// nginx sends a gRPC service no header the check sent empty, where Envoy sends it empty (auth.accept).
		{name: "a gRPC call whose accepted header is empty reaches the service empty", keys: []string{"auth.accept"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/AddItem",
				headers: map[string]string{"authorization": "Bearer accept-empty", "x-api-key": "key"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", received: map[string]string{"x-user-check": ""}, oneLine: []string{"x-user-check"}},
			by:   map[string]own{"nginx": {difference: "auth.accept", literal: literal{want: answer{status: 200, grpcStatus: "0", service: "shop", absent: []string{"x-user-check"}}}}}},
		{name: "the check is told the method's action and reports", keys: []string{"rpc.methods[].action", "rpc.methods[].reports", "auth.ask"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/AddItem",
				headers: map[string]string{"authorization": "Bearer allow", "x-gateway-action": "forged"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop",
				received: map[string]string{"x-user-check": "POST /example.shop.v1.CartService/AddItem action=buy reports=browse country="},
				absent:   []string{"x-gateway-action", "x-gateway-reports"}}},
		{name: "a method with an action and no reports", keys: []string{"rpc.methods[].action", "auth.ask"}, send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"authorization": "Bearer allow"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop",
				received: map[string]string{"x-user-check": "POST /example.shop.v1.CartService/GetCart action=browse reports= country="}}},
		{name: "a gRPC call the check denies is answered in gRPC's form", keys: []string{"auth.ask", "rpc.methods[].action"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer deny-401"}},
			want: answer{status: 200, grpcStatus: "16", headers: map[string]string{"grpc-message": "denied by the check"}}},
		{name: "a denial's body is percent-encoded in grpc-message", keys: []string{"auth.ask", "rpc.methods[].action"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer deny-401-message"}},
			want: answer{status: 200, grpcStatus: "16", headers: map[string]string{"grpc-message": "no entry: 100%25 sure %C3%A9%0Anext line"}}},
		{name: "a gRPC call the check denies with 403 is answered in gRPC's form", keys: []string{"auth.ask", "routes[].rpc"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer deny-403", "x-api-key": "key"}},
			want: answer{status: 200, grpcStatus: "7"}},
		{name: "a gRPC call the check cannot answer goes on with the unchecked mark under allow", keys: []string{"auth.ask", "auth.when_unavailable", "routes[].rpc"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer status-503", "x-api-key": "key"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", marked: "true"},
			on:   map[string]literal{"two-checks": {want: answer{status: 200, grpcStatus: "7"}}, "nginx-checks": {want: answer{status: 200, grpcStatus: "7"}}}},
		{name: "a gRPC call whose check's answer cannot be read is an unanswered check", keys: []string{"auth.ask", "auth.when_unavailable", "routes[].rpc"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer length-not-a-number", "x-api-key": "key"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", marked: "true"},
			on:   map[string]literal{"two-checks": {want: answer{status: 200, grpcStatus: "7"}}, "nginx-checks": {want: answer{status: 200, grpcStatus: "7"}}},
			by: map[string]own{"nginx": {difference: "auth.when_unavailable", files: allowFiles, literal: literal{want: answer{status: 200, grpcStatus: "7"}}},
				"haproxy": {difference: "auth.when_unavailable", files: []string{"haproxy-file"}, literal: literal{want: answer{status: 200, grpcStatus: "7"}}}}},
		{name: "a gRPC call the check cannot answer logs an unanswered check with the method's action", keys: []string{"log", "auth.ask", "auth.when_unavailable", "rpc.methods[].action"},
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"authorization": "Bearer status-503"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", marked: "true",
				logged: map[string]map[string]*regexp.Regexp{"gateway_auth_unavailable": {"action": logText("browse"),
					"failure_mode_allowed": logText("true"), "route": logText("shop"), "status": logNumber(200)}}}},
	}}

// paths: every value measured on Envoy 1.40.2; no key, because path normalisation is written for every file
// (docs/portability.md). A segment that is . or .. followed by ;, or by ; escaped once in either case of hex, is
// refused wherever the proxy's normalisation leaves it; ; escaped twice, ; on any other segment and anything in the
// query pass as sent. Envoy passes an invalid escape on, and the stub's own server answers it 400 (unreadable). Envoy
// also answers, and no row asserts, the 400 with an empty body. /catalog/..;/products/1, with the dots and the ;
// written, reaches shop as /products/1, because Envoy drops the parameter and resolves the segment itself; with
// envoy.reloadable_features.strip_dotdot_segments_with_parameters false, and on Envoy 1.33.14, which do not, the same
// request answers 400. Its answer depends on the Envoy version and that setting, so its row checks the answer of the
// pinned Envoy 1.40.2 with the setting at its default, which the support table keeps beside the other proxies' 400,
// and nginx's and HAProxy's own 400 under dotRow; Envoy resolves a dot segment with a ; parameter written with one dot,
// /products/.;/1, as well, and nginx and HAProxy answer it 400 (dotRow). HAProxy's own literals were measured on
// HAProxy 3.2.26 and 3.4.7.
// nginx's other own literals, measured on nginx 1.30.6 and 1.31.8, fall under pathRow: nginx answers 400 itself to a
// backslash, written or as %5C, to an invalid escape, to a path that needs normalising and keeps an escape, as
// /catalog/.%3B/../products/1 does, and to an empty query after a path that needs normalising, where nginx would drop
// the ? Envoy sends on.
// examples/portable limits the catalog route to 20 requests a second with no burst, and examples/full, nginx-file and
// nginx-auth to 20 with a burst of 10. The bucket spans the scenarios, and a fast runner sends rows inside one refill of
// 50 ms, so the row of a ; parameter on a name waits one second first (refill). Envoy serves 21 of these rows on that
// route, 10 before that row and 11 from it on, and routing's and CORS's rows send it 9 more before them, so 19 reach the
// route before the wait on examples/portable, under the 20 its bucket holds, and 18 or 19 on the files whose bucket
// holds 30. A request refused before a route is picked, such as one with a dot segment and an escaped ;, takes no token
// (measured on Envoy 1.40.2, nginx 1.30.6 and 1.31.8). A row added that the catalog route serves keeps both counts
// under 20, as asWritten's rows send shop's paths for the same bucket.
var paths = scenario{name: "path normalisation",
	exchanges: []exchange{
		{name: "a dot-dot segment", send: get("/catalog/../products/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot segment", send: get("/catalog/./items"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "repeated slashes", send: get("/catalog//items"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "an encoded slash", send: get("/catalog%2Fitems"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "encoded dots", send: get("/catalog/%2e%2e/products/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot-dot segment and an escaped slash", send: get("/catalog/..%2fproducts/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot-dot segment and a backslash written as %5C", send: get("/catalog/..%5Cproducts/1"), want: answer{status: 200, service: "shop", path: "/products/1"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment and a backslash", send: get("/catalog/..\\products/1"), want: answer{status: 200, service: "shop", path: "/products/1"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment with a ; parameter written with dots", send: get("/catalog/..;/products/1"), want: answer{status: 200, service: "shop", path: "/products/1"},
			by: map[string]own{"nginx": {difference: dotRow, literal: literal{want: answer{status: 400}}},
				"haproxy": {difference: dotRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot segment with a ; parameter written with a dot", send: get("/products/.;/1"), want: answer{status: 200, service: "shop", path: "/products/1"},
			by: map[string]own{"nginx": {difference: dotRow, literal: literal{want: answer{status: 400}}},
				"haproxy": {difference: dotRow, literal: literal{want: answer{status: 400}}}}},
		// Envoy and HAProxy resolve a dot-dot segment before they merge repeated slashes, so the .. removes the empty
		// segment, where nginx merges first and answers an empty segment before a dot-dot segment 400 (pathRow). After the
		// last dot-dot segment an empty segment gives one path all three ways.
		{name: "an empty segment before a dot-dot one", send: get("/catalog//../products/1"), want: answer{status: 200, service: "catalog", path: "/catalog/products/1"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an escaped slash before a dot-dot segment", send: get("/catalog/%2F../products/1"), want: answer{status: 200, service: "catalog", path: "/catalog/products/1"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an empty segment before a dot-dot one over HTTP/2", send: request{proto: "h2", method: "GET", host: "localhost", path: "/catalog//../products/1"},
			want: answer{status: 200, service: "catalog", path: "/catalog/products/1"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an empty segment after a dot-dot one", send: get("/products/../catalog//items"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "an invalid escape", send: get("/catalog/a%zz"), want: answer{status: 400, unreadable: true},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an escaped plus", send: get("/catalog/x%2By"), want: answer{status: 200, service: "catalog", path: "/catalog/x%2By"}},
		{name: "an empty query", send: get("/catalog/x?"), want: answer{status: 200, service: "catalog", path: "/catalog/x?"}},
		{name: "an empty query after a path that needs normalising", send: get("/products//x?"), want: answer{status: 200, service: "shop", path: "/products/x?"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot segment that keeps a ; parameter", send: get("/catalog/%2e%2e;/products/1"), want: answer{status: 400, headers: map[string]string{"content-type": ""}}},
		{name: "a dot-dot segment with ; escaped in upper case hex", send: get("/catalog/..%3B/products/1"), want: answer{status: 400}},
		{name: "a dot-dot segment with ; escaped in lower case hex", send: get("/catalog/..%3b/products/1"), want: answer{status: 400}},
		{name: "escaped dots with an escaped ;", send: get("/catalog/%2e%2e%3b/products/1"), want: answer{status: 400}},
		{name: "escaped dots with an escaped ; all in upper case hex", send: get("/catalog/%2E%2E%3B/products/1"), want: answer{status: 400}},
		{name: "a dot and an escaped dot with an escaped ;", send: get("/catalog/.%2e%3B/products/1"), want: answer{status: 400}},
		{name: "a dot segment with an escaped ;", send: get("/catalog/items/.%3B/x"), want: answer{status: 400}},
		// Envoy closes the connection after that 400, at once with the check and after 1 s without it, and answers no
		// request sent after it, and the refused request's body is read as a body (measured on Envoy 1.40.2).
		{name: "after the 400 of a dot segment with an escaped ; the connection is closed", send: thenNext("/products/..%3B/1", "allow"),
			want: answer{refused: true},
			by:   map[string]own{"haproxy": {difference: dotRow, literal: literal{want: answer{first: 400, status: 200, service: "shop", path: "/products/2"}}}}},
		{name: "an escaped dot segment with an escaped ;", send: get("/catalog/items/%2e%3b/x"), want: answer{status: 400}},
		{name: "an escaped ; that carries a value", send: get("/catalog/..%3Bx=1/products/1"), want: answer{status: 400}},
		{name: "two escaped ;", send: get("/catalog/..%3B%3B/products/1"), want: answer{status: 400}},
		{name: "an escaped ; on the last segment", send: get("/catalog/..%3B"), want: answer{status: 400}},
		{name: "an escaped ; on the first segment", send: get("/..%3B/products/1"), want: answer{status: 400}},
		{name: "an escaped ; between backslashes", send: get("/catalog\\..%3B\\products\\1"), want: answer{status: 400}},
		{name: "an escaped ; between backslashes written as %5C", send: get("/catalog%5C..%3b%5Cproducts%5C1"), want: answer{status: 400}},
		{name: "an escaped ; before an escaped slash", send: get("/catalog/..%3B%2Fproducts/1"), want: answer{status: 400}},
		{name: "a ; parameter on an ordinary segment", send: get("/a;v=1/b"), want: answer{status: 200, service: "shop", path: "/a;v=1/b"}},
		{name: "a ; parameter on a name", send: get("/catalog/readme.txt;x=1"), refill: time.Second,
			want: answer{status: 200, service: "catalog", path: "/catalog/readme.txt;x=1"}},
		{name: "a ; parameter on a name that starts with a dot", send: get("/catalog/.well-known;x/y"),
			want: answer{status: 200, service: "catalog", path: "/catalog/.well-known;x/y"}},
		{name: "a ; after three dots", send: get("/catalog/...;/x"), want: answer{status: 200, service: "catalog", path: "/catalog/...;/x"}},
		{name: "an escaped ; after three dots", send: get("/catalog/...%3B/x"),
			want: answer{status: 200, service: "catalog", path: "/catalog/...%3B/x"}},
		{name: "an escaped ; after a name that ends in two dots", send: get("/catalog/x..%3B/y"),
			want: answer{status: 200, service: "catalog", path: "/catalog/x..%3B/y"}},
		{name: "an escaped ; inside a name", send: get("/catalog/a%3Bb/c"), want: answer{status: 200, service: "catalog", path: "/catalog/a%3Bb/c"}},
		{name: "a dot-dot segment with ; escaped twice", send: get("/catalog/..%253B/products/1"),
			want: answer{status: 200, service: "catalog", path: "/catalog/..%253B/products/1"}},
		{name: "a query that holds a dot-dot segment and an escaped ;", send: get("/catalog/items?q=/..%3B/"),
			want: answer{status: 200, service: "catalog", path: "/catalog/items?q=/..%3B/"}},
		{name: "a query that holds a dot-dot segment and a ;", send: get("/catalog/items?q=/..;/"),
			want: answer{status: 200, service: "catalog", path: "/catalog/items?q=/..;/"}},
		{name: "a dot-dot segment with an escaped ?", send: get("/catalog/..%3Fx/y"), want: answer{status: 200, service: "catalog", path: "/catalog/..%3Fx/y"}},
		{name: "a dot segment with an escaped ; that a dot-dot segment removes", send: get("/catalog/.%3B/../products/1"),
			want: answer{status: 200, service: "catalog", path: "/catalog/products/1"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an exact path reached through a dot-dot segment", send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/x/../api/webhooks"},
			want: answer{status: 200, service: "accounts", path: "/hooks/payments"}},
	}}

// untypedOnNginxFiles is the literal of a 400 with no Content-Type on each of the battery's files for nginx, which Envoy
// 1.40.2 answers so on every file (measured).
var untypedOnNginxFiles = onEach(nginxFiles, literal{want: answer{status: 400, headers: map[string]string{"content-type": ""}}})

// asWritten: every value measured on Envoy 1.40.2, and nginx's own literals on nginx 1.30.6 and 1.31.8. Envoy picks a
// route on the path as the client wrote it, or, when the path holds // , a dot segment, an escaped unreserved
// character or %2F, on the path it has normalised, which keeps every other escape as written; it escapes a raw " < >
// ^ ` { | } first, in upper case hex. The service receives the path the route was picked on. nginx's battery files
// write patterns a path would meet if the escape it keeps were decoded: [A-Za-z0-9%_-]+ takes %20 written and no space,
// @ takes no %40, a space no %20, and two characters of x.. no six-character escape of é. A path that needs
// normalising and keeps an escape, one with a byte Envoy escapes and an escape, one with a byte Envoy escapes in a file
// with a pattern route, and one that keeps an escape on a route with a rewrite are answered 400 by nginx, which cannot
// rebuild Envoy's path for them, and so is an empty query, a ? with nothing after it, wherever nginx would rewrite the
// path and drop the ?; each is declared. An empty query on a path sent as written reaches the service as Envoy sends
// it. A # in the target is answered 400 by both. The rows send shop's paths, not catalog's: examples/portable limits
// catalog to 20 requests a second with no burst, and the path normalisation rows just before spend 11 of those 20
// after their one-second wait (paths); when these rows sent catalog's paths, one run in eleven got 429.
var asWritten = scenario{name: "the path as the client wrote it",
	exchanges: []exchange{
		{name: "a path with nothing to normalise", send: get("/xab"), want: answer{status: 200, service: "shop", path: "/xab"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/xab"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/xab"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/xab"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/xab"}}}},
		{name: "an escaped space is matched as written", send: get("/api/a%20b/admin"), want: answer{status: 200, service: "shop", path: "/api/a%20b/admin"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}}}},
		{name: "an escaped ; reaches the service as written", send: get("/api/x%3Bv=1/admin"),
			want: answer{status: 200, service: "shop", path: "/api/x%3Bv=1/admin"}},
		{name: "an escaped @ is not the character", send: get("/u/%40bob"), want: answer{status: 200, service: "shop", path: "/u/%40bob"}},
		{name: "an escaped space is not the character", send: get("/l/a%20b"), want: answer{status: 200, service: "shop", path: "/l/a%20b"}},
		{name: "an escape in lower case hex is kept", send: get("/api/a%7cb"), want: answer{status: 200, service: "shop", path: "/api/a%7cb"}},
		{name: "two characters of a pattern do not take an escaped character outside ASCII", send: get("/x%C3%A9"),
			want: answer{status: 200, service: "shop", path: "/x%C3%A9"}},
		{name: "a path that needs normalising and keeps no escape", send: get("/api/b/../a%41b/admin"),
			want: answer{status: 200, service: "shop", path: "/api/aAb/admin"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/api/aAb/admin"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/api/aAb/admin"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/api/aAb/admin"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/api/aAb/admin"}}}},
		{name: "a path that needs normalising and keeps an escape", send: get("/api/a%2fb%3bc"),
			want: answer{status: 200, service: "shop", path: "/api/a/b%3bc"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a byte Envoy escapes and an escape", send: get("/products/a|b%3Bc"),
			want: answer{status: 200, service: "shop", path: "/products/a%7Cb%3Bc"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a byte Envoy escapes", send: get("/products/a|b"), want: answer{status: 200, service: "shop", path: "/products/a%7Cb"},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: patternFiles, literal: literal{want: answer{status: 400}}}}},
		{name: "an escape on a route with a rewrite", send: get("/hooks/a%3Bb"), want: answer{status: 200, service: "shop", path: "/hooks/a%3Bb"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"nginx-plain":   {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"two-checks":    {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"nginx-checks":  {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}}},
			by: map[string]own{"nginx": {difference: "routes[].rewrite", files: nginxFiles,
				literal: literal{want: answer{status: 400}}}}},
		{name: "a route with a rewrite and a path with nothing to normalise", send: get("/hooks/ab"), want: answer{status: 200, service: "shop", path: "/hooks/ab"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"nginx-plain":   {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"two-checks":    {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"nginx-checks":  {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/in/ab"}}}},
		{name: "an empty query on a route with a rewrite", send: get("/hooks/ab?"), want: answer{status: 200, service: "shop", path: "/hooks/ab?"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"nginx-plain":   {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"two-checks":    {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"nginx-checks":  {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/in/ab?"}}},
			by: map[string]own{"nginx": {difference: "routes[].rewrite", files: nginxFiles,
				literal: literal{want: answer{status: 400}}}}},
		{name: "an empty query after a byte Envoy escapes", send: get("/products/a|b?"), want: answer{status: 200, service: "shop", path: "/products/a%7Cb?"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an empty query after a path with nothing to normalise", send: get("/xab?"), want: answer{status: 200, service: "shop", path: "/xab?"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/xab?"}},
				"haproxy-file":  {want: answer{status: 200, service: "accounts", path: "/xab?"}},
				"nginx-auth":    {want: answer{status: 200, service: "accounts", path: "/xab?"}},
				"nginx-tracing": {want: answer{status: 200, service: "accounts", path: "/xab?"}}}},
		// Envoy 1.40.2 answers a # with no body and no Content-Type, whatever the host, on every file (measured); the
		// files nginx runs hold it to that, and HAProxy's 400 for it, text/html from its own parser, is left to HAProxy's
		// next pull request.
		{name: "a # in the path", send: get("/products/a#b"), want: answer{status: 400}, on: untypedOnNginxFiles},
		{name: "a # in the path to a host no environment names", send: request{proto: "http/1.1", method: "GET", host: "other.example.net",
			path: "/products/a#b"}, want: answer{status: 400}, on: untypedOnNginxFiles},
		// HAProxy's HTTP/2 parser resets the stream of a :path that holds a # (lineRow).
		{name: "a # in the path over HTTP 2", send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/a#b"},
			want: answer{status: 400, headers: map[string]string{"content-type": ""}},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{refused: true}}}}},
		{name: "an escaped NUL in the path is answered 400", send: get("/products/a%00b"), want: answer{status: 400}},
		{name: "a byte above 0x7F over HTTP 2 reaches the service escaped", send: request{proto: "h2", method: "GET", host: "localhost",
			path: "/products/\xc3\xa9", raw: true}, want: answer{status: 200, service: "shop", path: "/products/%C3%A9"},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: patternFiles, literal: literal{want: answer{status: 400}}}}},
	}}

// offRoute: every value measured on Envoy 1.40.2. Its rows send paths under /public/, which examples/portable routes to
// shop with auth = off before its catalog route; examples/full has no such route, so there the shop route takes each
// path and asks the check, and on["full"] holds that answer where it differs. A client's own unchecked mark is removed
// on the auth-off route too, which asks no check. A path that normalisation takes out of /public/ leaves the route, so
// the route it reaches asks the check: with a token the check denies, its 401 shows the check was asked; with a token
// the check allows, the check is told the path the service receives, the normalised one, so a check that decides by
// path judges what the service serves. Without the normalisation the same paths stay on the auth-off route and reach
// shop unchecked. A dot segment with a ; parameter, written or escaped once, is answered 400 by the first route of
// every file, which comes before the auth-off route and does not turn the check off, so the check answers first: with
// no token or a token the check allows the 400 is the route's, and with a token it denies the answer is the check's
// 401. Envoy resolves ..; written with dots itself, so /public/..;/products/1 with a token the check denies leaves the
// route and gets the check's 401 as well. Envoy also answers, and no row asserts: /public/..;/products/1 with a token
// the check allows reaches shop as /products/1, an answer that depends on the Envoy version and a runtime setting
// (docs/auth.md); and /public/..%3B/products/1 with a token the check denies gets the check's 401, where a proxy that
// refuses the path before it asks the check answers 400.
var offRoute = scenario{name: "an auth-off prefix route",
	exchanges: []exchange{
		{name: "the route is not checked", keys: offKeys, send: deny("/public/readme"),
			want: answer{status: 200, service: "shop", path: "/public/readme"}, on: map[string]literal{"full": {want: answer{status: 401}}}},
		{name: "a client's own unchecked mark is removed on the auth-off route", keys: offKeys,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/public/readme", forgedMark: true},
			want: answer{status: 200, service: "shop", path: "/public/readme", unmarked: true}},
		{name: "repeated slashes stay on the route", keys: offKeys, send: deny("/public//readme"),
			want: answer{status: 200, service: "shop", path: "/public/readme"}, on: map[string]literal{"full": {want: answer{status: 401}}}},
		{name: "a dot-dot segment leaves the route", keys: offKeys, send: deny("/public/../products/1"), want: answer{status: 401}},
		{name: "encoded dots leave the route", keys: offKeys, send: deny("/public/%2e%2e/products/1"), want: answer{status: 401}},
		{name: "a dot-dot segment and an escaped slash leave the route", keys: offKeys, send: deny("/public/..%2fproducts/1"), want: answer{status: 401}},
		{name: "a dot-dot segment and a backslash written as %5C leave the route", keys: offKeys, send: deny("/public/..%5Cproducts/1"),
			want: answer{status: 401}, by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment and a backslash leave the route", keys: offKeys, send: deny("/public/..\\products/1"), want: answer{status: 401},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment leaves the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/../products/1"), want: toldProducts},
		{name: "encoded dots leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/%2e%2e/products/1"), want: toldProducts},
		{name: "a dot-dot segment and an escaped slash leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/..%2fproducts/1"), want: toldProducts},
		{name: "a dot-dot segment and a backslash written as %5C leave the route, and the check is told the path the service receives",
			keys: toldKeys, send: allowed("/public/..%5Cproducts/1"), want: toldProducts, by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment and a backslash leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/..\\products/1"), want: toldProducts, by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot-dot segment with a written ; leaves the route", keys: offKeys, send: deny("/public/..;/products/1"), want: answer{status: 401}},
		// The .. removes the empty segment before it, so the path stays off the route and the check is asked; nginx
		// answers an empty segment before a dot-dot segment 400 (pathRow).
		{name: "an empty segment before a dot-dot segment does not reach the route", keys: offKeys, send: deny("/products//../public/readme"),
			want: answer{status: 401}, by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an escaped slash before a dot-dot segment does not reach the route over HTTP/2", keys: offKeys,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/%2F../public/readme",
				headers: map[string]string{"authorization": "Bearer deny-401"}},
			want: answer{status: 401}, by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot segment that keeps a ; parameter", keys: offKeys, send: get("/public/%2e%2e;/products/1"), want: answer{status: 400}},
		{name: "a dot segment that keeps a ; parameter and a token the check allows", keys: offKeys, send: allowed("/public/%2e%2e;/products/1"),
			want: answer{status: 400}},
		{name: "a dot segment with an escaped ; and no token", keys: offKeys, send: get("/public/..%3B/products/1"), want: answer{status: 400}},
		{name: "a dot segment with an escaped ; and a token the check allows", keys: offKeys, send: allowed("/public/..%3B/products/1"),
			want: answer{status: 400}},
		{name: "a client's own user, question and unchecked mark do not reach the service on the auth-off route", keys: slices.Concat(offKeys, []string{"auth.accept"}),
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/public/readme", forgedMark: true,
				headers: map[string]string{"x-user-id": "forged", "x-gateway-action": "forged", "x-gateway-reports": "forged"}},
			want: answer{status: 200, service: "shop", path: "/public/readme", unmarked: true, absent: []string{"x-user-id", "x-gateway-action", "x-gateway-reports"}}},
		{name: "the auth-off route logs no check time", keys: slices.Concat(offKeys, []string{"log"}),
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/public/readme",
				headers: map[string]string{"authorization": "Bearer deny-401", "x-stub-status": "404"}},
			want: answer{status: 404, service: "shop", logged: map[string]map[string]*regexp.Regexp{"request": {"authz_us": logNull}}},
			on:   map[string]literal{"full": {want: answer{status: 401, logged: map[string]map[string]*regexp.Regexp{"request": {"authz_us": logDigits}}}}}},
	}}

// offKeys are what every row of the auth-off prefix route depends on: the prefix route with auth = off, and the check
// that a request leaving it meets, which is asked only of a request with an authorization header.
var offKeys = []string{"routes[].prefix", "routes[].auth", "auth.ask", "auth.only_with_header"}

// toldKeys are what the rows that read the path the check is told depend on beside offKeys: the check is told the path
// (auth.send_request), and the header its answer echoes the path in reaches the service (auth.accept).
var toldKeys = slices.Concat(offKeys, []string{"auth.accept", "auth.send_request"})

// toldProducts is the answer to a request the check allows that normalisation takes to /products/1: shop hears
// /products/1, and the stub's check, which echoes the method and the path it was told in x-user-check, was told the
// same.
var toldProducts = answer{status: 200, service: "shop", path: "/products/1",
	received: map[string]string{"x-user-check": "GET /products/1 action= reports= country="}}

// deny is a GET to localhost with a token the stub's check denies with 401.
func deny(path string) request {
	r := get(path)
	r.headers = map[string]string{"authorization": "Bearer deny-401"}
	return r
}

// allowed is a GET to localhost with a token the stub's check allows.
func allowed(path string) request {
	r := get(path)
	r.headers = map[string]string{"authorization": "Bearer allow"}
	return r
}

// rateLimit: every value measured on Envoy 1.40.2: on examples/full, after 2.5 s with no request to the route, 60
// requests back to back took 24 ms; the first 30 were admitted and the last was refused. There the route's bucket holds
// 30 (20 requests a second and a burst of 10) and refills 20 a second, so 60 requests empty it while each takes under
// 25 ms; on a slower runner the row fails and says how long the 60 took. The requests after the burst carry a token the
// check denies and one the token filter refuses, each answered 401 alone on this route, so a 429 shows that the limit
// is applied before the token and the check. They are sent 60 times, as is each row after them that wants a 429 or a
// closed connection: a row before leaves the bucket with up to its 30 tokens, as the bucket refills one every 50 ms
// while that row's answers come, so a row of 10 that finds the bucket full gets no 429, and 60 sent back to back empty
// it while they take under 1.5 s, however full they find it. On Envoy 1.40.2, in ten runs on each file, the first two
// rows of 60 got 56 to 60 answers 429 and the third 3 closed connections, as Envoy closes each about 1 s after its 429
// and the bucket refills meanwhile. Envoy answers the 429 with the body local_rate_limited and content-type text/plain,
// which no row asserts; a Connect call over the limit with 429, content-type application/json and
// {"code":"resource_exhausted","message":"local_rate_limited"}; and a gRPC call over it with 200 and grpc-status: 8 (of
// 110 GetCart calls to the RPC route's limit of 100 a second, sent in 103 ms, 109 were admitted, the bucket refilling
// as they went, so the rows of those two forms send to the catalog route). nginx-file and nginx-auth limit their
// catalog route as examples/full does, and nginx-plain limits only its route limited (clientKeys). examples/portable's
// route holds 20 (20 requests a second and no burst), so there the first 20 are admitted; the bucket refills one
// request every 50 ms, so its 429 is missed only when the 40 requests after the first 20 take about 2 s, and on one
// arm64 Mac, over two sets of 30 runs, the 60 took 0.02 to 0.04 s. nginx-file's route retried-limited admits 10 requests
// a minute for all and retries once: with shop-2 refusing connections, the first 10 of 40 requests are answered 200 and
// a later one 429, so a request whose first try was refused spends one token, never two and never none. It is the only
// row that sends to that route, 40 requests of which 10 are admitted, and its bucket refills one request every 6 s, so
// a row added later on the route waits for it (refill). full, nginx-auth and nginx-plain, which have no such route,
// serve the path by their shop routes, so there the row wants an answer from shop-1, which a shop route that tries
// once, as nginx-auth's and nginx-plain's do, gives at least one of the 40 unless each draws shop-2: a chance of
// 2^-40 where the backend is drawn at random, about one in 10^12, where the 14 requests the row sent before gave one
// in 16,384.
var rateLimit = scenario{name: "rate limit",
	quiet: 2500 * time.Millisecond,
	exchanges: []exchange{
		{name: "requests and burst are admitted, then the route refuses", keys: burstKeys,
			send: get("/catalog/items"), repeat: 60, admitted: 30, want: answer{status: 429},
			on: map[string]literal{"portable": {admitted: 20, want: answer{status: 429}}}},
		{name: "a request over the limit is refused before its token or the check is read", keys: []string{"routes[].rate_limit", "auth.ask"},
			send: deniedOnCatalog, repeat: 60, want: answer{status: 429}},
		// A request over the limit with a body is answered 429 and its connection closed, so the request written right
		// after its body gets no answer (measured on Envoy 1.40.2). Each is sent 60 times, as the row before.
		{name: "a request over the limit with a body is answered 429", keys: burstKeys, send: postToCatalog(""), repeat: 60,
			want: answer{status: 429}},
		{name: "a request over the limit with a body ends its connection", keys: burstKeys, send: postToCatalog(nextRequest), repeat: 60,
			want: answer{refused: true}},
		// A gRPC call over the limit, told by its content type on a route that is not an RPC one, with a body over
		// HTTP/1.1, is answered in gRPC's form and its connection closed as well (measured on Envoy 1.40.2). Envoy closes
		// such a connection about 1 s after its answer, its delayed close, and the reader waits for it, while the bucket
		// refills 20 tokens a second: in a debug run on Envoy 1.40.2 the first of 12 such calls was refused and the next
		// 11 were admitted, and the row before leaves up to 30 tokens. So it is sent 60 times, more than a bucket of 30
		// and what refills while those go out.
		{name: "a gRPC call over the limit with a body ends its connection", keys: burstKeys, send: grpcToCatalog(nextRequest), repeat: 60,
			want: answer{refused: true}},
		// A gRPC call over the limit is answered in gRPC's form, and a Connect call, which carries
		// Connect-Protocol-Version, in Connect's: the content type tells each from the 429 of text. Each is sent 60
		// times: the bucket holds at most 30 and refills 20 a second, so 60 sent back to back empty it while they take
		// under 1.5 s. On an arm64 Mac, after 2 s with no request to such a route, 60 calls of each took 0.03 to 0.06 s
		// on Envoy 1.40.2, nginx 1.30.6 and 1.31.8, and on each the first 30 were admitted and the other 30 refused in
		// the row's form.
		{name: "a gRPC call over the limit is answered 200 with grpc-status 8", keys: burstKeys, send: rpc("/catalog/items"), repeat: 60,
			want: answer{status: 200, grpcStatus: "8", headers: map[string]string{"content-type": "application/grpc"}}},
		{name: "a Connect call over the limit is answered 429 in JSON", keys: burstKeys, send: connectToCatalog, repeat: 60,
			want: answer{status: 429, headers: map[string]string{"content-type": "application/json"}}},
		{name: "another route is not limited", keys: []string{"routes[].rate_limit"}, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		// After a wait that fills the bucket again, each request the check or the token filter denies takes a token, so
		// the bucket's first 30, or 20 on examples/portable, are answered 401 and the next 429: were a denied request
		// not charged, every answer would be 401.
		{name: "a request the check denies spends a token", keys: []string{"routes[].rate_limit", "auth.ask"}, send: deniedOnCatalog,
			refill: 2 * time.Second, repeat: 40, want: answer{status: 429}},
		{name: "a limited route whose first connections were refused charges each request once", keys: []string{"routes[].rate_limit", "routes[].retries"},
			send: get("/retried-limited/products/1"), repeat: 40, admitted: 10, refusing: "shop-2", want: answer{status: 429},
			on: onEach([]string{"full", "nginx-auth", "nginx-tracing", "nginx-plain", "haproxy-traffic"}, literal{want: answer{status: 200, service: "shop", instance: "shop-1"}})},
	}}

// burstKeys are what the rows that find the catalog route's bucket spent depend on: the limit with its burst, which the
// row that spends it reads, so they run where it runs.
var burstKeys = []string{"routes[].rate_limit", "routes[].rate_limit.requests", "routes[].rate_limit.per",
	"routes[].rate_limit.burst", "routes[].rate_limit.by"}

// deniedOnCatalog is a GET of the catalog route with a token the check denies and one the token filter refuses, each
// answered 401 alone on that route.
var deniedOnCatalog = request{proto: "http/1.1", method: "GET", host: "localhost", path: "/catalog/items",
	headers: map[string]string{"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}}

// postToCatalog is a POST of 5 bytes to the catalog route, sent by hand and framed by its length, with then written
// right after the body, as a client that reuses its connection sends its next request.
func postToCatalog(then string) request {
	return request{proto: "http/1.1", method: "POST", host: "localhost", path: "/catalog/items", raw: true, unframed: true,
		headers: map[string]string{"content-length": "5"}, body: "abcde", then: then}
}

// connectToCatalog is a Connect call to the catalog route with no body: a POST with Connect-Protocol-Version and JSON's
// content type, over HTTP/1.1, where a body would end the connection after the 429.
var connectToCatalog = request{proto: "http/1.1", method: "POST", host: "localhost", path: "/catalog/items",
	headers: map[string]string{"content-type": "application/json", "connect-protocol-version": "1"}}

// grpcToCatalog is postToCatalog with gRPC's content type: a gRPC call over HTTP/1.1, which a rate limit tells by its
// content type alone, on any route.
func grpcToCatalog(then string) request {
	r := postToCatalog(then)
	r.headers = withHeader(r.headers, "content-type", "application/grpc")
	return r
}

// clientKeys: every value measured on Envoy 1.40.2. nginx-plain's route limited admits one request a minute for each
// client, which under client_address forwarded, that file's, is the last X-Forwarded-For entry when that entry is an
// IPv4 or IPv6 address, compared by value, and otherwise the peer: an entry with a port, in brackets, with a leading
// zero or with a zone is no address. Each row sends after the rows before it, so a 200 is a client's first request and
// a 429 one whose bucket a row before spent. No other row sends to the route, and every other file that writes a
// limit's by has no such route, so there each request reaches shop.
var clientKeys = scenario{name: "client keys",
	exchanges: []exchange{
		{name: "an entry with a port is the peer", keys: limitByKeys, send: toLimited("10.80.0.1:80"),
			want: answer{status: 200, service: "shop"}},
		{name: "a request with no entry is the peer, whose request the row before spent", keys: limitByKeys, send: toLimited(""),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "an address is a client of its own", keys: limitByKeys, send: toLimited("10.80.0.2"),
			want: answer{status: 200, service: "shop"}},
		{name: "spaces around an address leave it the same client", keys: limitByKeys, send: toLimited(" 10.80.0.2 "),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "an IPv6 address in capitals is a client of its own", keys: limitByKeys, send: toLimited("FD02::7"),
			want: answer{status: 200, service: "shop"}},
		{name: "the same IPv6 address written another way is the same client", keys: limitByKeys, send: toLimited("fd02:0::7"),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "the last entry of a list is the client", keys: limitByKeys, send: toLimited("10.99.0.1, 10.80.0.3"),
			want: answer{status: 200, service: "shop"}},
		{name: "that entry alone is the same client", keys: limitByKeys, send: toLimited("10.80.0.3"),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "an address in brackets is the peer", keys: limitByKeys, send: toLimited("[10.80.0.4]"),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "an address with a leading zero is the peer", keys: limitByKeys, send: toLimited("010.80.0.5"),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
		{name: "an IPv6 address with a zone is the peer", keys: limitByKeys, send: toLimited("fd04::8%eth0"),
			want: answer{status: 200, service: "shop"}, on: spentOnPlain},
	}}

// limitByKeys are what the client keys' rows depend on: a limit by client. The rows also depend on client_address
// forwarded, which nginx-plain does not write, as forwarded is the default with no tls, so they name it in no key and
// hold it by nginx-plain's literal.
var limitByKeys = []string{"routes[].rate_limit.by"}

// spentOnPlain is the literal of a request whose client's one request a minute a row before spent on nginx-plain.
var spentOnPlain = map[string]literal{"nginx-plain": {want: answer{status: 429}}}

// toLimited is a GET of nginx-plain's limited route sent by hand, with the X-Forwarded-For line forwardedFor, as
// written, or none when it is empty.
func toLimited(forwardedFor string) request {
	r := request{proto: "http/1.1", method: "GET", host: "localhost", path: "/limited/1", raw: true}
	if forwardedFor != "" {
		r.headers = map[string]string{"x-forwarded-for": forwardedFor}
	}
	return r
}

// hash: every value measured on Envoy 1.40.2. nginx-file's service sessions, at shop's two addresses, picks a backend
// by the hash of x-session-id on the route /sessions/, which no rate limit holds: an empty header is a value, so each
// request that carries one reaches one backend, and so does each whose header comes on two lines, in either order. A
// file with no such route sends the path to shop, either backend.
var hash = scenario{name: "hash",
	exchanges: []exchange{
		// The hash's main row: one value, sent 20 times, reaches one backend of the two each time, on nginx-file and
		// haproxy-traffic, whose route /sessions/ hashes x-session-id.
		{name: "one hash header value reaches one backend every time", keys: hashKeys, send: toSessions("user-7"), repeat: 20, every: true,
			want: answer{status: 200, service: "shop"}, on: onEach([]string{"nginx-file", "haproxy-traffic"}, oneBackend)},
		{name: "an empty hash header reaches one backend every time", keys: hashKeys, send: toSessions(""), repeat: 20, every: true,
			want: answer{status: 200, service: "shop"}, on: oneBackendOnFile},
		{name: "a hash header on two lines reaches one backend every time", keys: hashKeys, send: toSessions("a\r\nx-session-id: b"),
			repeat: 20, every: true, want: answer{status: 200, service: "shop"}, on: oneBackendOnFile},
		// The two lines are read sorted, so either order is one value. Read in the order they came, c then d and d then c
		// reach different backends of nginx-file's two on nginx 1.30.6 and 1.31.8, where a and b reach the same one.
		{name: "a hash header's two lines in either order reach one backend every time", keys: hashKeys,
			send: toSessions("c\r\nx-session-id: d"), alternate: new(toSessions("d\r\nx-session-id: c")),
			repeat: 20, every: true, want: answer{status: 200, service: "shop"}, on: oneBackendOnFile},
		// A value of x, 60,000 spaces and y, read without the spaces and tabs at its ends, was answered by Envoy in under
		// 10 ms and by nginx after about 14 s, when proxyfile.js trimmed it with a regular expression that tries every
		// space of the run (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). The bound is 2 s, far below those
		// 14 s and far above the milliseconds the request takes, because the proxy in its container and the client are
		// two clocks and a slow runner writes 60 KB more slowly. On examples/portable, whose 96 KiB nginx reads in
		// buffers of 49,120 bytes, the line is longer than one buffer, which nginx answers 400 itself.
		{name: "a hash header of 60,000 spaces between two letters is answered at once", keys: hashKeys,
			send: toSessions("x" + strings.Repeat(" ", 60000) + "y"), want: answer{status: 200, service: "shop", below: 2 * time.Second},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"},
				literal: literal{want: answer{status: 400, below: 2 * time.Second}}}}},
	}}

// hashKeys are what the hash rows depend on: a service balanced by a header's hash.
var hashKeys = []string{"services.<service name>.hash.header"}

// oneBackend is a hash row's answer on a file whose route /sessions/ hashes x-session-id, and oneBackendOnFile the
// literal of the hash rows on nginx-file.
var (
	oneBackend       = literal{want: answer{status: 200, service: "shop", oneInstance: true}}
	oneBackendOnFile = map[string]literal{"nginx-file": oneBackend}
)

// toSessions is a GET of nginx-file's route /sessions/ sent by hand, with the x-session-id line value, which may hold a
// second line.
func toSessions(value string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: "/sessions/1", raw: true,
		headers: map[string]string{"x-session-id": value}}
}

// The keys of cost's rows: the long path's answer depends on the routes it passes and on the origin it carries, the
// long Origin's on the patterns that read it, and the flood's on the pattern routes its paths pass and the prefix route
// that answers them.
var (
	longPathKeys   = []string{"routes[].pattern", "routes[].prefix", "origins.<environment name>.patterns"}
	longOriginKeys = []string{"origins.<environment name>.patterns"}
	floodKeys      = []string{"routes[].pattern", "routes[].prefix"}
)

// floodPath is the path of the flood's request i: /k01/ to /k40/, the prefixes of the 40 pattern routes of the HAProxy
// battery file, then 1,000 bytes of a and b drawn from i, so each request is new to the proxy and each run sends the
// same ones. No path ends in the c each of those patterns ends in, so the route after them answers every request.
func floodPath(i int) string {
	random := rand.New(rand.NewPCG(uint64(i), 0))
	path := fmt.Appendf(nil, "/k%02d/", i%40+1)
	for range 1000 {
		path = append(path, "ab"[random.IntN(2)])
	}
	return string(path)
}

// cost holds what one request may cost a proxy: a long path that many pattern routes read, and a long Origin that many
// origin patterns read, each a request that stopped HAProxy 3.2.26 and 3.4.7 (exit 134, by its watchdog) when their
// patterns ran at once and backtracked, and the request after each, which only a proxy still running answers. Envoy
// 1.40.2 answered each in about 3 ms, and HAProxy in 20 ms or less on an arm64 Mac. The bound, 500 ms, leaves a slower
// runner twenty times the 24 ms proxyfile.lua's budget gives the patterns of one request alone at most, and stays under
// the second HAProxy's watchdog lets a thread run before it stops the process. Last, a flood of paths no proxy has read
// before, which the HAProxy battery file's 40 pattern routes read. On an arm64 Mac with 12 CPUs it grew HAProxy 3.2.26
// and 3.4.7 by about 510 MB before the patterns of a thread shared one budget of states, by about 215 MB with a budget
// of about 8 MB a thread, and by about 60 MB with proxyfile.lua's, of about 1 MB; Envoy 1.40.2 by about 75 MB with 12
// workers and with 4, because RE2 keeps a cache for each pattern, not each worker. The bound, 96 MB and 4 MB a thread,
// 144 MB with 12 threads, fails the first two and leaves the last two over 60 MB. nginx 1.30.6 and 1.31.8, at the
// battery file's header limit of 60 KiB, read a head of at most 61,376 bytes, so a path of 61,000 bytes is about the
// longest they read: the 24 pattern routes read it in 26.20 to 27.03 ms at the median on an arm64 Mac, where Envoy took
// 3.07 ms (docs/nginx.md). Their 64 patterns, written with .* for each *, held a worker for 17 to 30 s with an Origin
// of 6,321 bytes, about the longest each ran to its end without PCRE2 giving up, and refused an Origin the 64th pattern
// takes once PCRE2 gave up on the first; Envoy answered each in 6 ms or less. They match no Origin longer than 8,192
// bytes against the patterns, as a request may also hold the longest path they read (the generator's
// longestMatchedOrigin), so they refuse the Origins of 16,384 and 16,385 bytes that Envoy serves (origins.<environment
// name>.patterns), and the row with a path of 53,005 bytes and an Origin of 8,192 bytes holds the two costs in one
// request. On examples/portable, where no environment has the Origins' host, both answer those Origins 404, and nginx
// holds its 96 KiB in two buffers of 49,120 bytes, so it answers the paths of 61,000 and 53,005 bytes 414, a request
// line longer than one buffer (limits.request_headers.size). HAProxy 3.2.26 and 3.4.7 answer the 61,000-byte path 414
// and both of those Origins 431, as they answer any head that outgrows their buffer (lineRow). After the flood, 2,000
// connections that each send a head of 65 bytes, one past nginx's first header buffer of 64, and stay silent: over TLS
// in the battery, on an arm64 Mac with 12 CPUs, each grew Envoy 1.40.2 by 31.5 to 34.9 KiB and nginx 1.30.6 and 1.31.8
// by 32.9 to 38.6 KiB, and over plain HTTP Envoy by 23.6 to 24.4 KiB and nginx by 23.8 to 25.9 KiB at 60 KiB and at
// 8 MiB of headers. The memory is read 3 s after the last connection opened. One header buffer of 8 MiB in place of
// nginx's buffers grew it by 66.9 to 403.4 KiB each in the battery, and by about 2 MiB each over plain HTTP; with 500
// connections it grew it by 21.2 and 23.7 KiB each, under the bound, so the row keeps 2,000. The bound, 64 KiB a
// connection, 125 MiB for the 2,000, fails that and leaves the others under. The row catches that one buffer only on a
// kernel whose transparent huge pages are always, as Docker Desktop's are (7.0.14-linuxkit: [always] madvise never),
// where the buffer takes a whole huge page of 2 MiB; with madvise or never a buffer costs the pages a head touches
// (headerLines), so one large buffer can pass the row there. On every kernel the row bounds the memory a connection
// holds, and TestSecurityLines' header limits row holds the buffers' form.
var cost = scenario{name: "cost",
	exchanges: []exchange{
		{name: "a path of 14,000 bytes that 24 pattern routes read", keys: longPathKeys,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/api/" + strings.Repeat("a", 14000),
				headers: map[string]string{"origin": "https://app.example.com"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://app.example.com"},
				below: 500 * time.Millisecond}},
		// The whole head, with the Host, User-Agent, Origin, Accept-Encoding and Connection lines the client writes, is
		// 61,152 bytes, under the 61,376 nginx reads.
		{name: "a path of 61,000 bytes, about the longest nginx reads at 60 KiB, that 24 pattern routes read", keys: longPathKeys,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/api/" + strings.Repeat("a", 61000),
				headers: map[string]string{"origin": "https://app.example.com"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://app.example.com"},
				below: 500 * time.Millisecond},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 414}}},
				"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 414}}}}},
		{name: "the request after the long path is served", keys: longPathKeys, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "an Origin of 8,000 bytes that 64 patterns of two * read", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 4000) + "example1.orgx"}},
			want: answer{status: 403, below: 500 * time.Millisecond},
			// measured on Envoy 1.40.2: no environment of full or portable has the host
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}}},
		// HAProxy 3.2.26 and 3.4.7 echo an Origin of 7,620 bytes and answer one of 8,020 bytes 500.
		{name: "an Origin of 6,420 bytes that the last of 64 patterns of two * takes", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 3200) + "example9.org"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{
				"access-control-allow-origin": "https://" + strings.Repeat("a.", 3200) + "example9.org"}, below: 500 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}},
			// HAProxy answers an admitted Origin longer than it echoes 403 before the service (the CORS rows of a long
			// admitted Origin).
			by: map[string]own{"haproxy": {difference: "origins.<environment name>.patterns",
				literal: literal{want: answer{status: 403, below: 500 * time.Millisecond}}}}},
		{name: "an Origin of 6,321 bytes that 64 patterns of two * read", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 3150) + "example1.orgx"}},
			want: answer{status: 403, below: 500 * time.Millisecond},
			on:   map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}}},
		{name: "an Origin of 16,384 bytes that the last of 64 patterns of two * takes", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 8182) + "example9.org"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{
				"access-control-allow-origin": "https://" + strings.Repeat("a.", 8182) + "example9.org"}, below: 500 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}},
			by: map[string]own{"nginx": {difference: "origins.<environment name>.patterns", files: nginxYAMLFiles,
				literal: literal{want: answer{status: 403, below: 500 * time.Millisecond}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 431}}}}},
		{name: "an Origin of 16,385 bytes that the last of 64 patterns of two * takes", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://a" + strings.Repeat("a.", 8182) + "example9.org"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{
				"access-control-allow-origin": "https://a" + strings.Repeat("a.", 8182) + "example9.org"}, below: 500 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}},
			by: map[string]own{"nginx": {difference: "origins.<environment name>.patterns", files: nginxYAMLFiles,
				literal: literal{want: answer{status: 403, below: 500 * time.Millisecond}}},
				"haproxy": {difference: lineRow, literal: literal{want: answer{status: 431}}}}},
		// The whole head, with the Host, User-Agent, Origin, Accept-Encoding and Connection lines the client writes, is
		// 61,326 bytes, under the 61,376 nginx reads at 60 KiB, and its Origin is the longest nginx matches.
		{name: "a path of 53,005 bytes and an Origin of 8,192 bytes that the last of 64 patterns of two * takes, about the longest head nginx reads at 60 KiB",
			keys: append(slices.Clone(longPathKeys), "limits.request_headers.size"),
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/api/" + strings.Repeat("a", 53000),
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 4086) + "example9.org"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{
				"access-control-allow-origin": "https://" + strings.Repeat("a.", 4086) + "example9.org"}, below: 500 * time.Millisecond},
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 414}}}}},
		{name: "the request after the long Origin is served", keys: longOriginKeys, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "a flood of new paths that 40 pattern routes read keeps the proxy's memory bounded", keys: floodKeys,
			// The flood gives each request its path (floodPath).
			send:  request{proto: "http/1.1", method: "GET", host: "localhost"},
			flood: &flood{connections: 24, requests: 3000, path: floodPath, fixed: 96 << 20, perThread: 4 << 20},
			want:  answer{status: 200, service: "shop"}},
		{name: "connections each holding a head just over 64 bytes keep the proxy's memory bounded", keys: []string{"limits.request_headers.size"},
			// The connections are opened as this raw request's would be, and each sends the hold's head.
			send: request{proto: "http/1.1", host: "localhost", raw: true},
			holds: &holds{connections: 2000, head: "GET /products/1 HTTP/1.1\r\nHost: localhost\r\nx-pad: " + strings.Repeat("a", 15),
				settle: 3 * time.Second, perConnection: 64 << 10}},
	}}

// drawnID and undrawnID are the x-request-id of a tracing row's request, the row's own number at its end, so each
// span is told from every other: the Envoy output's draw reads the first eight characters of the id as a hex number,
// modulo 10,000, and traces the request when it is below the rate's 500 of 10,000, so 000001f3 (499) is drawn and
// 000001f4 (500) is not. nginx-tracing.yaml keeps client_address at forwarded, so the client's id reaches the draw.
func drawnID(row string) string   { return "000001f3-0000-4000-8000-" + row }
func undrawnID(row string) string { return "000001f4-0000-4000-8000-" + row }

// tracedTo is a GET over HTTP/1.1 of path on localhost carrying id and headers, which the shop route serves.
func tracedTo(path, id string, headers map[string]string) request {
	all := map[string]string{"x-request-id": id}
	maps.Copy(all, headers)
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: path, headers: all}
}

// The traceparent a client sends in the tracing rows, and the forms of the one a proxy sends on.
const (
	clientTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
	clientParent = "00f067aa0ba902b7"
	sampled      = "00-" + clientTrace + "-" + clientParent + "-01"
)

var (
	sampledTrace   = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
	unsampledTrace = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-00$`)
	// drawnTrace is either flag: on examples/full, whose client_address = connection replaces the client's id with the
	// proxy's own, the draw decides it.
	drawnTrace = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)
	// clientSampled and clientUnsampled continue the client's trace with a parent id of the proxy's own.
	clientSampled   = regexp.MustCompile(`^00-` + clientTrace + `-[0-9a-f]{16}-01$`)
	clientUnsampled = regexp.MustCompile(`^00-` + clientTrace + `-[0-9a-f]{16}-00$`)
	clientDrawn     = regexp.MustCompile(`^00-` + clientTrace + `-[0-9a-f]{16}-0[01]$`)
	shopSpan        = regexp.MustCompile(`^shop$`)
	clientParentID  = regexp.MustCompile(`^` + clientParent + `$`)
)

// servedOnFull is a tracing row's literal on examples/full: the shop service's answer with what the service received
// of the trace, which no draw decides there.
func servedOnFull(forms map[string]*regexp.Regexp, received map[string]string) map[string]literal {
	return map[string]literal{"full": {want: answer{status: 200, service: "shop", receivedForms: forms, received: received}}}
}

// otelRow is the row of nginx's support table that declares how its spans differ from the Envoy output's.
const otelRow = "tracing.provider.otel"

// tracing: every want measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8 on their images tagged -otel, and each of
// nginx's own literals (by) is a difference its support table declares. The proxy runs with the two variables
// nginx-tracing.yaml's set env tags read (proxyEnvironment). nginx-tracing.yaml traces 5% of the requests,
// drawn by the id, and sends its spans to the battery's collector (file.collector), so each row sends an id whose draw
// is known (drawnID, undrawnID) and reads the span that carries it, or wants none; examples/full traces 10% to a
// collector the world leaves unreachable, with client_address = connection, so on it a row reads what the service
// received, and nothing the draw decides. No row sends to the catalog route, whose bucket the rate limit rows count on.
var tracing = scenario{name: "tracing",
	exchanges: []exchange{
		{name: "a request whose id the draw picks is traced", keys: []string{"tracing", "tracing.provider.otel.sample.rate"}, overlap: true,
			send: tracedTo("/products/t1", drawnID("000000000001"), nil),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": sampledTrace},
				spanned: map[string]*regexp.Regexp{"name": shopSpan, "guid:x-request-id": regexp.MustCompile(`^` + drawnID("000000000001") + `$`)}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": drawnTrace}, nil)},
		{name: "a request whose id the draw does not pick is not traced", keys: []string{"tracing", "tracing.provider.otel.sample.rate"}, overlap: true,
			send: tracedTo("/products/t2", undrawnID("000000000002"), nil),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": unsampledTrace}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": drawnTrace}, nil)},
		{name: "an id that is not a UUID is drawn by its first eight characters", keys: []string{"tracing", "tracing.provider.otel.sample.rate"}, overlap: true,
			send: tracedTo("/products/t3", "000001f3x", nil),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": sampledTrace},
				spanned: map[string]*regexp.Regexp{"name": shopSpan}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": drawnTrace}, nil)},
		{name: "an id shorter than eight characters is never traced", keys: []string{"tracing", "tracing.provider.otel.sample.rate"}, overlap: true,
			send: tracedTo("/products/t4", "0001f3", nil),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": unsampledTrace}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": drawnTrace}, nil)},
		{name: "a sampled parent the draw does not pick is not traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t5", undrawnID("000000000005"), map[string]string{"traceparent": sampled}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientUnsampled}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil)},
		{name: "a parent the draw picks continues its trace", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t6", drawnID("000000000006"), map[string]string{"traceparent": sampled}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				spanned: map[string]*regexp.Regexp{"name": shopSpan, "parent": clientParentID}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil)},
		{name: "a parent the draw picks continues its trace over HTTP/2", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/t28",
				headers: map[string]string{"x-request-id": drawnID("000000000028"), "traceparent": sampled}},
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				spanned: map[string]*regexp.Regexp{"name": shopSpan, "parent": clientParentID}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil)},
		{name: "a parent marked not sampled is never traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t7", drawnID("000000000007"), map[string]string{"traceparent": "00-" + clientTrace + "-" + clientParent + "-00"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientUnsampled}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": clientUnsampled}, nil)},
		{name: "a malformed traceparent reaches the service as sent", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t8", drawnID("000000000008"), map[string]string{"traceparent": "00-abc-def-01"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"traceparent": "00-abc-def-01"}, noSpan: true},
			on:   servedOnFull(nil, map[string]string{"traceparent": "00-abc-def-01"})},
		{name: "a traceparent of another version than 00 continues its trace", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t9", drawnID("000000000009"), map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-01"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				spanned: map[string]*regexp.Regexp{"name": shopSpan, "parent": clientParentID}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-01"}, noSpan: true}}}}},
		{name: "a traceparent of another version than 00 keeps the client's tracestate", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t9b", drawnID("00000000009b"), map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-01",
				"tracestate": "a=1"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				received: map[string]string{"tracestate": "a=1"}, spanned: map[string]*regexp.Regexp{"name": shopSpan, "parent": clientParentID}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, map[string]string{"tracestate": "a=1"}),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-01", "tracestate": "a=1"}, noSpan: true}}}}},
		// The Envoy output writes the draw's flag on such a parent, and nginx keeps the client's version, trace id and
		// parent id beside the same flag.
		{name: "a traceparent of another version than 00 whose id the draw does not pick is sent on not sampled", keys: []string{"tracing", "tracing.provider.otel"},
			overlap: true, send: tracedTo("/products/t9c", undrawnID("00000000009c"), map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-01"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientUnsampled}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"traceparent": "01-" + clientTrace + "-" + clientParent + "-00"}, noSpan: true}}}}},
		{name: "two tracestate lines reach the service as one", keys: []string{"tracing", "tracing.provider.otel"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/t10", raw: true,
				headers: map[string]string{"x-request-id": drawnID("000000000010"), "traceparent": sampled, "tracestate": "a=1\r\ntracestate: b=2"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"tracestate": "a=1,b=2"}},
			on:   servedOnFull(nil, map[string]string{"tracestate": "a=1,b=2"})},
		// Over HTTP/1.1 the Envoy output reads a traceparent and a tracestate without the tabs beside them. nginx keeps
		// the tab, and its tracer then does not continue the parent, or puts the first tracestate line into the span
		// with its tab, so the request goes on as one of another version than 00: no span, the client's ids and the
		// draw's flag.
		{name: "a traceparent after a tab whose id the draw does not pick is sent on not sampled", keys: []string{"tracing", "tracing.provider.otel"},
			overlap: true, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c1", raw: true,
				headers: map[string]string{"x-request-id": undrawnID("0000000000c1"), "traceparent": "\t" + sampled}},
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientUnsampled}, noSpan: true},
			on:   servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, nil),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"traceparent": "00-" + clientTrace + "-" + clientParent + "-00"}, noSpan: true}}}}},
		{name: "a tracestate after a tab beside a parent the draw picks is joined without it", keys: []string{"tracing", "tracing.provider.otel"},
			overlap: true, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c7", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000c7"), "traceparent": sampled, "tracestate": "\ta=1"}},
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				received: map[string]string{"tracestate": "a=1"}, spanned: map[string]*regexp.Regexp{"name": shopSpan, "state": regexp.MustCompile(`^a=1$`)}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, map[string]string{"tracestate": "a=1"}),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"traceparent": "00-" + clientTrace + "-" + clientParent + "-01", "tracestate": "a=1"}, noSpan: true}}}}},
		// The client's bytes of its trace reach the service as it sent them, a byte outside UTF-8 and each line of a
		// malformed traceparent and of the tracestate beside it included.
		{name: "a tracestate holding the byte 0xFF reaches the service as sent", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c2", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000c2"), "traceparent": sampled, "tracestate": "k=\xffz"}},
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				received: map[string]string{"tracestate": "k=\xffz"}, spanned: map[string]*regexp.Regexp{"name": shopSpan}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, map[string]string{"tracestate": "k=\xffz"})},
		{name: "a malformed traceparent holding the byte 0xFF reaches the service as sent", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c3", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000c3"), "traceparent": "00-abc-\xff-01"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"traceparent": "00-abc-\xff-01"}, noSpan: true},
			on:   servedOnFull(nil, map[string]string{"traceparent": "00-abc-\xff-01"})},
		{name: "a malformed traceparent on two lines reaches the service as two lines, and so does its tracestate",
			keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c4", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000c4"), "traceparent": "00-abc-def-01\r\ntraceparent: " + sampled,
					"tracestate": "x=\xff\r\ntracestate: y=2"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"traceparent": "00-abc-def-01\n" + sampled,
				"tracestate": "x=\xff\ny=2"}, noSpan: true},
			on: servedOnFull(nil, map[string]string{"traceparent": "00-abc-def-01\n" + sampled, "tracestate": "x=\xff\ny=2"})},
		{name: "with no traceparent Envoy adds an empty tracestate", keys: []string{"tracing", "tracing.provider.otel"},
			send: tracedTo("/products/t11", drawnID("000000000011"), nil),
			want: answer{status: 200, service: "shop", received: map[string]string{"tracestate": ""}, oneLine: []string{"tracestate"}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", received: map[string]string{"tracestate": ""},
				oneLine: []string{"tracestate"}}}},
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop", absent: []string{"tracestate"}}}}}},
		{name: "a client's tracestate reaches the span as Envoy's does", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t29", drawnID("000000000029"), map[string]string{"traceparent": sampled, "tracestate": "a=1"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": clientSampled},
				received: map[string]string{"tracestate": "a=1"}, spanned: map[string]*regexp.Regexp{"name": shopSpan, "state": regexp.MustCompile(`^a=1$`)}},
			on: servedOnFull(map[string]*regexp.Regexp{"traceparent": clientDrawn}, map[string]string{"tracestate": "a=1"})},
		// The check reports the traceparent and tracestate it received (report-trace) in x-user-check, which the file
		// accepts. The Envoy output's check span, async accounts egress, carries no x-request-id, so no row reads it: its
		// difference is declared on the provider's row.
		{name: "the check receives the trace", keys: []string{"tracing", "tracing.provider.otel", "auth.ask"},
			send: tracedTo("/products/t12", drawnID("000000000012"), map[string]string{"authorization": "Bearer report-trace"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{
				"x-user-check": regexp.MustCompile(`^traceparent=00-[0-9a-f]{32}-[0-9a-f]{16}-01 tracestate=$`)}},
			on: servedOnFull(map[string]*regexp.Regexp{
				"x-user-check": regexp.MustCompile(`^traceparent=00-[0-9a-f]{32}-[0-9a-f]{16}-0[01] tracestate=$`)}, nil)},
		{name: "the check receives no trace beside a malformed traceparent", keys: []string{"tracing", "tracing.provider.otel", "auth.ask"},
			send: tracedTo("/products/t12b", drawnID("00000000012b"), map[string]string{"authorization": "Bearer report-trace", "traceparent": "00-abc-def-01"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "traceparent= tracestate="}}},
		{name: "the check receives a traceparent of another version than 00 continued", keys: []string{"tracing", "tracing.provider.otel", "auth.ask"},
			send: tracedTo("/products/t12c", drawnID("00000000012c"), map[string]string{"authorization": "Bearer report-trace",
				"traceparent": "01-" + clientTrace + "-" + clientParent + "-01"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{
				"x-user-check": regexp.MustCompile(`^traceparent=00-` + clientTrace + `-[0-9a-f]{16}-01 tracestate=$`)}},
			on: servedOnFull(map[string]*regexp.Regexp{
				"x-user-check": regexp.MustCompile(`^traceparent=00-` + clientTrace + `-[0-9a-f]{16}-0[01] tracestate=$`)}, nil),
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				received: map[string]string{"x-user-check": "traceparent=01-" + clientTrace + "-" + clientParent + "-01 tracestate="}}}}}},
		{name: "the check receives a parent marked not sampled continued", keys: []string{"tracing", "tracing.provider.otel", "auth.ask"},
			send: tracedTo("/products/t12d", drawnID("00000000012d"), map[string]string{"authorization": "Bearer report-trace",
				"traceparent": "00-" + clientTrace + "-" + clientParent + "-00"}),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{
				"x-user-check": regexp.MustCompile(`^traceparent=00-` + clientTrace + `-[0-9a-f]{16}-00 tracestate=$`)}}},
		{name: "a tracestate holding the byte 0xFF reaches the check as sent", keys: []string{"tracing", "tracing.provider.otel", "auth.ask"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/c5", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000c5"), "authorization": "Bearer report-tracestate",
					"traceparent": sampled, "tracestate": "k=\xffz"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-check": "tracestate=k=\xffz"}},
			on:   servedOnFull(nil, map[string]string{"x-user-check": "tracestate=k=\xffz"})},
		{name: "a span carries the Envoy output's fields", keys: []string{"tracing", "tracing.provider.otel", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/t13?q=1", drawnID("000000000013"), nil),
			want: answer{status: 200, service: "shop", spanned: spanFields(`^http://localhost/products/t13\?q=1$`, `^HTTP/1\.1$`, `^Go-http-client/1\.1$`, "string")},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 200, service: "shop",
				spanned: spanFields(`^http://localhost/products/t13\?q=1$`, `^HTTP/1\.1$`, `^Go-http-client/1\.1$`, "int")}}}}},
		{name: "a span's peer.address is the connection's whatever X-Forwarded-For says", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t13b", drawnID("00000000013b"), map[string]string{"x-forwarded-for": "203.0.113.9"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"name": shopSpan, "peer.address": worldClient}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		// The URL's scheme is what the Envoy output's :scheme holds: under forwarded a client's X-Forwarded-Proto that
		// is http or https in any letter case, and the connection's for any other value, which the service receives
		// all the same; and its host the host_header a route's service is sent.
		{name: "a span's URL holds the connection's scheme beside any other X-Forwarded-Proto", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/b2", drawnID("0000000000b2"), map[string]string{"x-forwarded-proto": "b2-x"}),
			want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "b2-x"},
				spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost/products/b2$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a span's URL holds https from an X-Forwarded-Proto in capitals", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/b3", drawnID("0000000000b3"), map[string]string{"x-forwarded-proto": "HTTPS"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^https://localhost/products/b3$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a span's URL holds the host_header of the route's service", keys: []string{"tracing", "tracing.provider.otel",
			"services.<service name>.host_header"}, overlap: true,
			send: tracedTo("/events/b1", drawnID("0000000000b1"), nil),
			want: answer{status: 200, service: "accounts", spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://127\.0\.0\.1:7070/events/b1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "accounts"}}}},
		{name: "over HTTP/2 the span says HTTP/2", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/t14", headers: map[string]string{"x-request-id": drawnID("000000000014")}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"name": shopSpan, "http.protocol": regexp.MustCompile(`^HTTP/2$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a request no route takes is named ingress", keys: []string{"tracing", "tracing.provider.otel", "origins.<environment name>.hosts"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/t15", headers: map[string]string{"x-request-id": drawnID("000000000015")}},
			want: answer{status: 404, spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^ingress$`), "upstream_cluster": nil}},
			on:   map[string]literal{"full": {want: answer{status: 404}}},
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 404,
				spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^ingress$`), "upstream_cluster": regexp.MustCompile(`^$`)}}}}}},
		// A target of 257 bytes, whose URL the Envoy output cuts to 256 characters after the host.
		{name: "a target over 256 bytes", keys: []string{"tracing", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/"+strings.Repeat("a", 247), drawnID("000000000016"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost.{256}$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by:   map[string]own{"nginx": {difference: "tracing.path_in_url", literal: literal{want: answer{status: 200, service: "shop", noSpan: true}}}}},
		{name: "a target of 256 bytes is whole in the span", keys: []string{"tracing", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/"+strings.Repeat("b", 246), drawnID("00000000016b"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost/products/b{246}$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		// nginx's tracer adds the target as the client wrote it, as http.target, so nginx writes no span for a target the
		// Envoy output's URL holds otherwise: one it escapes, one it normalises, one it rewrites, and the path a client's
		// x-envoy-original-path names in its place. Such a request is sent on not sampled, as one whose target is over
		// 256 bytes is.
		{name: "a query after characters Envoy escapes", keys: []string{"tracing", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/a2-"+strings.Repeat("^", 84)+"?token=a2", drawnID("0000000000a2"), nil),
			want: answer{status: 200, service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": sampledTrace},
				spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost/products/a2-(?:%5E){81}$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			// nginx refuses the path on nginx-tracing.yaml, whose pattern routes could take the byte where Envoy's never does.
			by: map[string]own{"nginx": {difference: "tracing.path_in_url", files: []string{"nginx-tracing-by-name"}, literal: literal{want: answer{status: 200,
				service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": unsampledTrace}, noSpan: true}},
				other: &own{difference: pathRow, files: []string{"nginx-tracing"}, literal: literal{want: answer{status: 400, noSpan: true}}}}}},
		{name: "a dot-dot segment", keys: []string{"tracing", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/a3x/../a3?x=1", drawnID("0000000000a3"), nil),
			want: answer{status: 200, service: "shop", path: "/products/a3?x=1",
				spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost/products/a3\?x=1$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", path: "/products/a3?x=1"}}},
			by: map[string]own{"nginx": {difference: "tracing.path_in_url", literal: literal{want: answer{status: 200, service: "shop",
				path: "/products/a3?x=1", noSpan: true}}}}},
		{name: "a client's x-envoy-original-path", keys: []string{"tracing", "tracing.path_in_url"}, overlap: true,
			send: tracedTo("/products/a4?real=1", drawnID("0000000000a4"), map[string]string{"x-envoy-original-path": "/fake/a4?fake=1"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://localhost/fake/a4\?fake=1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by:   map[string]own{"nginx": {difference: "tracing.path_in_url", literal: literal{want: answer{status: 200, service: "shop", noSpan: true}}}}},
		{name: "a route with a rewrite", keys: []string{"tracing", "tracing.path_in_url", "routes[].rewrite"}, overlap: true,
			send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/api/webhooks", headers: map[string]string{"x-request-id": drawnID("0000000000a5")}},
			want: answer{status: 200, service: "accounts", path: "/hooks/payments",
				spanned: map[string]*regexp.Regexp{"http.url": regexp.MustCompile(`^http://127\.0\.0\.1:7070/hooks/payments$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "accounts", path: "/hooks/payments"}}},
			by: map[string]own{"nginx": {difference: "tracing.path_in_url", literal: literal{want: answer{status: 200, service: "accounts",
				path: "/hooks/payments", noSpan: true}}}}},
		{name: "a header tag reads the first of two lines", keys: []string{"tracing", "tracing.tags.<tag name>.header"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/t17", raw: true,
				headers: map[string]string{"x-request-id": drawnID("000000000017"), "x-tenant": "a\r\nx-tenant: b"}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"tenant": regexp.MustCompile(`^a$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a header tag on a header Envoy joins reads its lines joined", keys: []string{"tracing", "tracing.tags.<tag name>.header"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/b4", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000b4"), "accept": "a\r\naccept: b"}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"accepts": regexp.MustCompile(`^a,b$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a header tag sent empty is not set and its default not used", keys: []string{"tracing", "tracing.tags.<tag name>.header"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/t18", raw: true,
				headers: map[string]string{"x-request-id": drawnID("000000000018"), "x-tenant": ""}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"tenant": nil}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "tracing.tags.<tag name>.header", literal: literal{want: answer{status: 200, service: "shop",
				spanned: map[string]*regexp.Regexp{"tenant": regexp.MustCompile(`^$`)}}}}}},
		{name: "a header tag absent takes its default, one without a default is not set", keys: []string{"tracing", "tracing.tags.<tag name>.header", "tracing.tags.<tag name>.default"},
			overlap: true, send: tracedTo("/products/t19", drawnID("000000000019"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"tenant": regexp.MustCompile(`^anon$`), "plain": nil}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "tracing.tags.<tag name>.header", literal: literal{want: answer{status: 200, service: "shop",
				spanned: map[string]*regexp.Regexp{"tenant": regexp.MustCompile(`^anon$`), "plain": regexp.MustCompile(`^$`)}}}}}},
		{name: "a tag on an accepted header reads the value the check accepted", keys: []string{"tracing", "tracing.tags.<tag name>.header", "auth.ask", "auth.accept"},
			overlap: true, send: tracedTo("/products/t20", drawnID("000000000020"), map[string]string{"authorization": "Bearer allow", "x-user-id": "forged"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"user": regexp.MustCompile(`^u-1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a tag on an accepted header takes its default on a route the check is off for", keys: []string{"tracing", "tracing.tags.<tag name>.header",
			"tracing.tags.<tag name>.default", "auth.accept", "routes[].auth"}, overlap: true,
			send: tracedTo("/public/t20b", drawnID("00000000020b"), map[string]string{"x-user-id": "forged"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^public$`), "user": regexp.MustCompile(`^nouser$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "an env tag whose variable is unset takes its default", keys: []string{"tracing", "tracing.tags.<tag name>.env", "tracing.tags.<tag name>.default"},
			overlap: true, send: tracedTo("/products/t21", drawnID("000000000021"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"environment": regexp.MustCompile(`^local$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		// constructor is a property every object has, not a variable of the environment.
		{name: "an env tag on a variable named like a property every object has takes its default", keys: []string{"tracing",
			"tracing.tags.<tag name>.env", "tracing.tags.<tag name>.default"}, overlap: true,
			send: tracedTo("/products/b5", drawnID("0000000000b5"), map[string]string{"x-tenant": "acme"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"inherited": regexp.MustCompile(`^own$`),
				"tenant": regexp.MustCompile(`^acme$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "an env tag whose variable is set reads it", keys: []string{"tracing", "tracing.tags.<tag name>.env"}, overlap: true,
			send: tracedTo("/products/b7", drawnID("0000000000b7"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"region": regexp.MustCompile(`^eu-1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		// njs leaves a variable named like a property every object has out of process.env, so nginx reads toString as
		// unset, where Envoy reads it.
		{name: "an env tag on a set variable named like a property every object has", keys: []string{"tracing",
			"tracing.tags.<tag name>.env", "tracing.tags.<tag name>.default"}, overlap: true,
			send: tracedTo("/products/b8", drawnID("0000000000b8"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"inherited_set": regexp.MustCompile(`^ts1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "tracing.tags.<tag name>.env", literal: literal{want: answer{status: 200, service: "shop",
				spanned: map[string]*regexp.Regexp{"inherited_set": regexp.MustCompile(`^own$`)}}}}}},
		{name: "a tag value keeps a quote, a backslash and bytes above 0x7F as sent", keys: []string{"tracing", "tracing.tags.<tag name>.header"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/t22", raw: true,
				headers: map[string]string{"x-request-id": drawnID("000000000022"), "x-tenant": `a"b\cé`}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"tenant": regexp.MustCompile(`^a"b\\cé$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a tag's default keeps a dollar, a quote and a backslash", keys: []string{"tracing", "tracing.tags.<tag name>.default"}, overlap: true,
			send: tracedTo("/products/t24", drawnID("000000000024"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"quoted": regexp.MustCompile(`^a\$b"c\\d$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		// nginx's tracer adds the client's User-Agent as nginx keeps it, a tab at either end included, as http.user_agent,
		// where the Envoy output's user_agent holds it without the tab; nginx sends such a request on not sampled.
		{name: "a User-Agent after a tab is held by a span as Envoy reads it", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/b6", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000b6"), "user-agent": "\tprobe/1"}},
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"user_agent": regexp.MustCompile(`^probe/1$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}},
			by: map[string]own{"nginx": {difference: "tracing.provider.otel", literal: literal{want: answer{status: 200, service: "shop",
				receivedForms: map[string]*regexp.Regexp{"traceparent": unsampledTrace}, noSpan: true}}}}},
		{name: "a tag named like a span field replaces it", keys: []string{"tracing", "tracing.tags.<tag name>.header"}, overlap: true,
			send: tracedTo("/products/t25", drawnID("000000000025"), map[string]string{"x-component": "mine"}),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"component": regexp.MustCompile(`^mine$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "a tag named like a span field keeps the field's value when its header is absent", keys: []string{"tracing", "tracing.tags.<tag name>.header"},
			overlap: true, send: tracedTo("/products/t25b", drawnID("00000000025b"), nil),
			want: answer{status: 200, service: "shop", spanned: map[string]*regexp.Regexp{"component": regexp.MustCompile(`^proxy$`)}},
			on:   map[string]literal{"full": {want: answer{status: 200, service: "shop"}}}},
		{name: "the tracing headers a client sends do not reach the service", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/t26", drawnID("000000000026"), map[string]string{"x-envoy-downstream-service-cluster": "a",
				"x-envoy-downstream-service-node": "b", "x-envoy-decorator-operation": "c"}),
			want: answer{status: 200, service: "shop", absent: []string{"x-envoy-decorator-operation", "x-envoy-downstream-service-cluster",
				"x-envoy-downstream-service-node"}, spanned: map[string]*regexp.Regexp{"name": shopSpan}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", absent: []string{"x-envoy-decorator-operation",
				"x-envoy-downstream-service-cluster", "x-envoy-downstream-service-node"}}}}},
		{name: "a gRPC call's service receives the span's traceparent", keys: []string{"tracing", "tracing.provider.otel", "routes[].rpc"}, overlap: true,
			send: request{proto: "grpc", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart",
				headers: map[string]string{"x-request-id": drawnID("000000000023")}},
			want: answer{status: 200, grpcStatus: "0", service: "shop", receivedForms: map[string]*regexp.Regexp{"traceparent": sampledTrace},
				spanned: map[string]*regexp.Regexp{"name": shopSpan}},
			on: map[string]literal{"full": {want: answer{status: 200, grpcStatus: "0", service: "shop",
				receivedForms: map[string]*regexp.Regexp{"traceparent": drawnTrace}}}}},
		// nginx's gRPC module sends one field for each grpc_set_header line, so the file refuses a call whose
		// traceparent is malformed and comes with a second line of it or of tracestate, which the Envoy output passes
		// on as fields of their own.
		{name: "a gRPC call's malformed traceparent on two lines reaches the service as two lines", keys: []string{"tracing", "tracing.provider.otel", "routes[].rpc"},
			overlap: true, send: request{proto: "h2", method: "POST", host: "localhost", path: "/example.shop.v1.CartService/GetCart", raw: true,
				body: "\x00\x00\x00\x00\x00", headers: map[string]string{"content-type": "application/grpc", "te": "trailers",
					"x-request-id": drawnID("0000000000c6"), "traceparent": "00-abc-def-01\r\ntraceparent: " + sampled}},
			want: answer{status: 200, service: "shop", received: map[string]string{"traceparent": "00-abc-def-01\n" + sampled}, noSpan: true},
			on:   servedOnFull(nil, map[string]string{"traceparent": "00-abc-def-01\n" + sampled}),
			by:   map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 400, noSpan: true}}}}},
		// The requests either proxy answers itself: a path both refuse, which the Envoy output traces as ingress; a %00
		// and a fragment, which it does not trace, where nginx's own answer to the fragment passes through a location
		// whose tracer reads $pf_refusal; and a refused origin, traced under the route.
		{name: "a path both refuse is traced as ingress", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/..%3B/t27", drawnID("000000000027"), nil),
			want: answer{status: 400, spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^ingress$`)}},
			on:   map[string]literal{"full": {want: answer{status: 400}}}},
		{name: "a %00 in the path is not traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/%00", drawnID("00000000027b"), nil),
			want: answer{status: 400, noSpan: true},
			on:   map[string]literal{"full": {want: answer{status: 400}}}},
		{name: "a fragment in the target is not traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", host: "localhost", line: "GET /products/t27c#x HTTP/1.1", raw: true,
				headers: map[string]string{"x-request-id": drawnID("00000000027c")}},
			want: answer{status: 400, noSpan: true},
			on:   map[string]literal{"full": {want: answer{status: 400}}}},
		{name: "a refused upgrade is not traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: tracedTo("/products/a1?t=a1", drawnID("0000000000a1"), map[string]string{"connection": "upgrade", "upgrade": "foo",
				"authorization": "Bearer allow"}),
			want: answer{status: 403, noSpan: true},
			on:   map[string]literal{"full": {want: answer{status: 403}}}},
		{name: "a request line of another scheme than http and https is not traced", keys: []string{"tracing", "tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", host: "localhost", line: "GET ftp://localhost/products/a6 HTTP/1.1", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000a6"), "authorization": "Bearer allow"}},
			want: answer{status: 400, noSpan: true},
			on:   map[string]literal{"full": {want: answer{status: 400}}}},
		// Envoy answers it 400 before the 426 of HTTP/1.0, and HAProxy, which runs no tracing row, 426.
		{name: "a request line of another scheme than http and https with HTTP/1.0 is answered 400 and not traced", keys: []string{"tracing",
			"tracing.provider.otel"}, overlap: true,
			send: request{proto: "http/1.1", host: "localhost", line: "GET ftp://localhost/products/a7 HTTP/1.0", raw: true,
				headers: map[string]string{"x-request-id": drawnID("0000000000a7")}},
			want: answer{status: 400, headers: map[string]string{"content-type": "text/plain"}, noSpan: true},
			on:   map[string]literal{"full": {want: answer{status: 400, headers: map[string]string{"content-type": "text/plain"}}}}},
		{name: "a request from a refused origin is traced under its route", keys: []string{"tracing", "tracing.provider.otel",
			"origins.<environment name>.origins", "origins.<environment name>.patterns"}, overlap: true,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/t27d",
				headers: map[string]string{"x-request-id": drawnID("00000000027d"), "origin": "https://evil.example.net"}},
			want: answer{status: 403, spanned: map[string]*regexp.Regexp{"name": shopSpan}},
			on:   map[string]literal{"full": {want: answer{status: 403}}},
			by: map[string]own{"nginx": {difference: otelRow, literal: literal{want: answer{status: 403,
				spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^ingress$`)}}}}}},
	}}

// worldClient is the address the proxy sees the battery's clients come from: the gateway of the battery's network,
// through which Docker forwards the listener's published port.
var worldClient = regexp.MustCompile(`^10\.0\.0\.1$`)

// spanFields are the fields of the Envoy output's span of a GET that the shop route serves, with url, protocol and the
// client's user agent, and statusType the type of http.status_code: the Envoy output writes it as text, nginx's tracer
// as a number.
func spanFields(url, protocol, userAgent, statusType string) map[string]*regexp.Regexp {
	fields := map[string]*regexp.Regexp{"name": shopSpan, "node_id": regexp.MustCompile(`^$`), "zone": regexp.MustCompile(`^$`),
		"downstream_cluster": regexp.MustCompile(`^-$`), "component": regexp.MustCompile(`^proxy$`), "http.method": regexp.MustCompile(`^GET$`),
		"http.protocol": regexp.MustCompile(protocol), "http.url": regexp.MustCompile(url), "peer.address": worldClient,
		"request_size": regexp.MustCompile(`^0$`), "response_size": regexp.MustCompile(`^[1-9][0-9]*$`),
		"upstream_cluster": regexp.MustCompile(`^shop_http$`), "upstream_cluster.name": regexp.MustCompile(`^shop_http$`),
		"user_agent": regexp.MustCompile(userAgent), "http.status_code": regexp.MustCompile(`^200$`),
		"type:http.status_code": regexp.MustCompile(`^` + statusType + `$`)}
	return fields
}

// tracingRetried: a traced request to the route retried to shop, each of 40 with an id of its own, while shop-2
// refuses connections. The service read the ids of the span, one for each request, on the try that served it. Its own
// scenario, because the rows of tracing that overlap send to shop too, and would meet shop-2 refusing. The row cannot
// tell a request whose first try met shop-2 from one shop-1 served at once; measured once, 20 of the 40 spans on each
// nginx version named the copy that tries again (http.route @pf_r_81-rt), and Envoy 1.40.2 retried 39 of the 40 after
// a failed connect. Were each first try a fair coin instead of a round robin, all 40 would miss shop-2 with a chance of
// 2^-40, under one in a trillion.
var tracingRetried = scenario{name: "tracing a retried request",
	exchanges: []exchange{
		{name: "a traced request retried after a refused connection has one span, whose ids the service read", keys: []string{"tracing", "routes[].retries"},
			send: tracedTo(retriedPath, drawnID("0000000031{i}"), nil), repeat: 40, every: true, refusing: "shop-2",
			want: answer{status: 200, service: "shop", instance: "shop-1", receivedForms: map[string]*regexp.Regexp{"traceparent": sampledTrace},
				spanned: map[string]*regexp.Regexp{"name": regexp.MustCompile(`^retried$`), "upstream_cluster": regexp.MustCompile(`^shop_http$`),
					"http.status_code": regexp.MustCompile(`^200$`)}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", instance: "shop-1"}}}},
	}}

// backendLimits: the connection limit and a service's failed backends, one behaviour of each. On nginx-limits, whose
// limit is two, three requests are sent at once on one HTTP/2 connection, each held 1 s by the stub: nginx's own
// limit_conn serves two and answers the third 503, and the Envoy output serves all three, in 2 s where each is held 1 s
// (measured on Envoy 1.40.2). On examples/full, whose limit is 4096, which Envoy alone runs, all three are served.
// Then a backend whose request failed takes no request for a while: four requests while shop-2 refuses connections
// make nginx meet it, as round robin over two backends sends at least one of four to each, and the six sent right
// after, back to back and well inside nginx's 10 s, all reach shop-1 on nginx, whose passive check took shop-2 out;
// without it they would alternate. The Envoy output, which checks its backends itself, serves each of the six from
// shop on both files (measured on Envoy 1.40.2), and the row asks no backend of it.
var backendLimits = scenario{name: "connection limit and failed backends",
	exchanges: []exchange{
		{name: "three requests at once on one connection past a limit of two", keys: []string{"limits.connections"},
			send:    request{proto: "h2", method: "GET", host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-delay": "1s"}},
			streams: 3, want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "limits.connections", files: []string{"nginx-limits"},
				literal: literal{want: answer{status: 200, service: "shop"}, also: &answer{status: 503}}},
				// HAProxy's maxconn on the one server of haproxy-limits serves two at once and holds the third in its queue
				// until one ends, so the third is answered a second after the others.
				"haproxy": {difference: "limits.connections", files: []string{"haproxy-limits"},
					literal: literal{want: answer{status: 200, service: "shop", below: 1500 * time.Millisecond},
						also: &answer{status: 200, service: "shop", atLeast: 1900 * time.Millisecond}}}}},
		{name: "requests while one backend refuses connections", keys: []string{"services.<service name>.health"},
			send: get("/products/1"), refusing: "shop-2", repeat: 4, want: answer{status: 200, service: "shop"}},
		{name: "requests right after a backend refused connections", keys: []string{"services.<service name>.health"},
			send: get("/products/1"), repeat: 6, every: true, want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "services.<service name>.health",
				literal: literal{want: answer{status: 200, service: "shop", instance: "shop-1"}}}}},
	}}

// traffic: the main rows of keys HAProxy writes with its own directives (haproxyMainRows), every value measured on
// Envoy 1.40.2 beside HAProxy 3.2.26 and 3.4.7. A header block of 50 KiB passes HAProxy's default buffer of 16,384
// bytes, so it reaches the service only through a buffer the size writes; one of 200 KiB passes 96 KiB. 120 header
// lines pass the 100 a file without the count gets, and 200 pass 150. The files of nginx's battery hold 100 lines, so
// they refuse 120 (measured on Envoy 1.40.2), and nginx holds examples/portable's 96 KiB in two buffers of 49,120
// bytes, so it refuses a header line longer than one there (limits.request_headers.size, as the size rows of routing
// say). checked, at shop's two addresses, is checked every second and leaves the pool after two failed checks, so 3 s
// after shop-2 began refusing connections every request reaches shop-1; on examples/full the shop route's retry gives
// the same answers. A request whose service answers 404 is logged, as the log of each file that writes one keeps every
// answer of 400 or more: HAProxy's line holds no path, so the row finds it by its frontend, backend, status, method and
// version.
var traffic = scenario{name: "traffic keys",
	exchanges: []exchange{
		{name: "a header block of 50 KiB reaches the service", keys: []string{"limits.request_headers.size"},
			send: rawHeaders(map[string]string{"x-big": strings.Repeat("a", 51200)}), want: answer{status: 200, service: "shop"},
			by: map[string]own{"nginx": {difference: "limits.request_headers.size", files: []string{"portable"}, literal: literal{want: answer{status: 400}}}}},
		{name: "a header block of 200 KiB is refused", keys: []string{"limits.request_headers.size"},
			send: rawHeaders(map[string]string{"x-big": strings.Repeat("a", 204800)}), want: answer{refused: true}},
		{name: "120 header lines reach the service", keys: []string{"limits.request_headers.count"}, send: rawHeaders(numbered(119)),
			want: answer{status: 200, service: "shop"}, on: onEach(nginxYAMLFiles, literal{want: answer{refused: true}})},
		{name: "200 header lines are refused", keys: []string{"limits.request_headers.count"}, send: rawHeaders(numbered(199)),
			want: answer{refused: true}},
		{name: "a backend that fails its health checks gets no request", keys: []string{"services.<service name>.health"}, send: get("/checked/products/1"),
			refusing: "shop-2", settle: 3 * time.Second, repeat: 20, every: true, want: answer{status: 200, service: "shop", instance: "shop-1"}},
		{name: "a request answered 404 is logged", keys: []string{"log"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/logged/1", headers: map[string]string{"x-stub-status": "404"}},
			want: answer{status: 404, service: "shop", logged: map[string]map[string]*regexp.Regexp{"request": {"status": logNumber(404)}}},
			by: map[string]own{"haproxy": {difference: "log", literal: literal{want: answer{status: 404, service: "shop",
				lineAfter: regexp.MustCompile(`^\[[^]]+\] public shop_http/shop[12] \S+ 404 \d+ \d+ \S+ GET HTTP/1\.1 "[^"]+"$`)}}}}},
	}}
