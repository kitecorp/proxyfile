package conformance

import (
	"crypto/tls"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

// scenarios run in this order, each exchange after the one before, never in parallel: a later scenario's quiet and
// burst count on nothing else reaching the proxy meanwhile. The rate limit runs last, so the requests the scenarios
// before it send to its route come first, and its quiet lets that route's bucket fill again before its burst.
var scenarios = []scenario{routing, mutualTLS, cors, auth, timeout, retry, gRPC, paths, asWritten, offRoute, rateLimit, cost}

// get is a GET over HTTP/1.1 to localhost, a host of the example's local environment.
func get(path string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: path}
}

// rpc is a unary gRPC call to localhost with the empty message.
func rpc(path string) request {
	return request{proto: "grpc", method: "POST", host: "localhost", path: path}
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
// port it does not read, where Envoy serves both (lineRow).
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
		// measured on Envoy 1.40.2: 404 with an empty body
		{name: "a host no environment names", keys: []string{"origins.<environment name>.hosts"}, send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/1"},
			want: answer{status: 404}},
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
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/admin/a%0Ab"}}},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an upgrade other than WebSocket is refused", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"upgrade": "spdy/3.1", "connection": "upgrade"}}, want: answer{status: 403}},
		{name: "an h2c upgrade is served as a plain request", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"upgrade": "h2c", "connection": "Upgrade, HTTP2-Settings", "http2-settings": "AAMAAABkAAQAAP__"}},
			want: answer{status: 200, service: "shop", absent: []string{"upgrade", "http2-settings"}},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "the scheme the request came in on", send: get("/products/1"), want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "http"}},
			on: map[string]literal{"full": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"portable":     {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}},
				"haproxy-file": {want: answer{status: 200, service: "shop", received: map[string]string{"x-forwarded-proto": "https"}}}}},
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
			want: answer{status: 200, service: "shop", oneLine: []string{"x-forwarded-for"}}},
		{name: "a service that refuses connections is answered 502", send: get("/catalog/items"), refusing: "catalog", want: answer{status: 502}},
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
			path: "/products/1"}, want: answer{status: 404}},
		{name: "a method Envoy's parser does not know is answered 400", send: request{proto: "http/1.1", method: "FOO", host: "localhost",
			path: "/products/1"}, want: answer{status: 400}},
		{name: "a pattern's {,3} is text, as RE2 reads it", keys: []string{"routes[].pattern"}, send: get("/braces/xxx"),
			want: answer{status: 200, service: "shop", path: "/braces/xxx"}},
		{name: "a path PCRE2 gives up on as the pattern is written is matched as Go's parse writes it", keys: []string{"routes[].pattern"},
			send: get("/backtrack/" + strings.Repeat("a", 40) + "b"), want: answer{status: 200, service: "shop"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts"}}}},
		{name: "a path PCRE2 gives up on is refused", keys: []string{"routes[].pattern"}, send: get("/give-up/" + strings.Repeat("a", 40) + "b"),
			want: answer{status: 200, service: "shop"},
			by:   map[string]own{"nginx": {difference: "routes[].pattern", literal: literal{want: answer{status: 400}}}}},
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
		{name: "a header name holding @ over HTTP 2 is refused", send: rawHeader("h2", "x@at"), want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header name holding a parenthesis over HTTP 2 is refused", send: rawHeader("h2", "x(p)"), want: answer{refused: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a header name holding the byte 0xFF over HTTP 2 is refused", send: rawHeader("h2", "x\xffy"), want: answer{refused: true},
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
		{name: "an absolute target with an empty port", send: rawLine("GET http://localhost:/products/1 HTTP/1.1", "localhost"),
			want: answer{status: 400}},
		// nginx serves a request Envoy refuses here, and the service receives the body and no trailer either way: nginx
		// reads no trailer, so it can refuse none (lineRow).
		{name: "a trailer whose name holds @", send: chunked("http/1.1", "x@at"), want: answer{status: 400},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}}}}},
		{name: "a trailer reaches the service through no proxy", send: chunked("http/1.1", "x-t"),
			want: answer{status: 200, service: "shop", body: "abc", noTrailers: true}},
		{name: "a trailer over HTTP 2", send: chunked("h2", "x-t"), want: answer{status: 200, service: "shop", body: "abc", noTrailers: true},
			by: map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{refused: true}}}}},
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
		// Over HTTP/2 Envoy counts the 4 pseudo-headers of a request among its 100.
		{name: "96 header fields over HTTP 2 reach the service", send: rawFields(numbered(96)), want: answer{status: 200, service: "shop"}},
		{name: "HTTP/1.0 is answered 426", send: rawLine("GET /products/1 HTTP/1.0", "localhost"), want: answer{status: 426}},
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
		// but not an empty one; nginx serves ten elements of white space (lineRow). HAProxy counts an empty element
		// but the last, so it refuses a token and ten empty elements, and does not count close or keep-alive, which
		// it removes before any rule reads the header, so it serves ten of them (lineRow; measured on Envoy 1.40.2,
		// nginx 1.30 and 1.31, and HAProxy 3.2.26 and 3.4.7).
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
			want: answer{status: 400},
			by:   map[string]own{"nginx": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}}}},
		{name: "ten Connection tokens close are answered 400", send: request{proto: "http/1.1", method: "GET", host: "localhost",
			path: "/products/1", headers: map[string]string{"connection": "close, close, close, close, close, close, close, close, close, close"}},
			want: answer{status: 400},
			by:   map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 200, service: "shop"}}}}},
		// A WebSocket over HTTP/2 (RFC 8441's extended CONNECT), sent by hand: Envoy 1.40.2 closes the connection, and
		// HAProxy 3.2.26 sent it to the service as an HTTP/1.1 upgrade before the file answered it 400.
		{name: "a WebSocket over HTTP 2 is refused", send: request{proto: "h2", method: "CONNECT", host: "localhost", path: "/products/1",
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
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 431}}},
				"nginx": {difference: lineRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a path of 20,000 bytes reaches the service", send: get("/products/" + strings.Repeat("a", 20000)),
			want: answer{status: 200, service: "shop"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{status: 414}}},
				"nginx": {difference: lineRow, literal: literal{want: answer{status: 414}}}}},
		// HAProxy writes each byte above 0x7F as its escape, three bytes, which outgrows its buffer here: HAProxy 3.4.7
		// answers 400, and HAProxy 3.2.26 500.
		{name: "an HTTP 2 path of 7,000 bytes above 0x7F reaches the service", send: request{proto: "h2", method: "GET",
			host: "localhost", path: "/products/" + strings.Repeat("\xe9", 7000), raw: true}, want: answer{status: 200, service: "shop"},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{refused: true}}},
				"nginx": {difference: "routes[].pattern", files: []string{"nginx-file"}, literal: literal{want: answer{status: 400}}}}},
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
	}}

// numbered is n headers named x-h001 onwards, each with the value 1.
func numbered(n int) map[string]string {
	headers := map[string]string{}
	for i := 1; i <= n; i++ {
		headers[fmt.Sprintf("x-h%03d", i)] = "1"
	}
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
	}}

// serverKeys are what the rows of a client's TLS offer depend on: a listener that speaks TLS.
var serverKeys = []string{"tls.certificate", "tls.key"}

// offered is a GET of the shop route on localhost over HTTP/1.1 whose client offers offer.
func offered(offer tlsOffer) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", offer: &offer}
}

// cors: every value measured on Envoy 1.40.2. An origin on one of the environment's own hosts is admitted whether or
// not a list holds it, and only a list gives it access-control-allow-origin: https://api.example.org, the request's
// own host and port, is in neither list of the live environment, whose patterns cover api.example.com but not
// api.example.org. Envoy also answers, and no row asserts: access-control-allow-methods with the method asked for and
// access-control-allow-headers with the headers asked for, or empty, because both keys are "*";
// access-control-expose-headers on the preflights from a listed origin too; vary: Origin on every answer; and the
// body Invalid CORS request with content-type text/plain on both 403s. The preflight from a listed origin and the request from
// another origin carry a token the check denies and one the token filter refuses, each answered 401 alone on the same
// host and path: on a file that writes the check, their 200 and 403 show that the origin is decided before the token
// and the check. Their answers do not depend on the check, so they run on a file that writes none too, such as
// HAProxy's, which then runs each key of cors.
//
// The rows on hostKeys need no list: every file refuses a browser origin on none of the environment's hosts, so they
// run on every file, nginx's included, and send to localhost, a host of every file's local environment. An origin on
// one of its hosts, 127.0.0.1 or localhost, on either scheme and any port, is admitted; one on another environment's
// host, in capitals, with a trailing dot, with other characters for a host's dots, or null is refused, and so is a
// preflight from such an origin and a WebSocket upgrade. A preflight from an admitted origin is answered by the proxy,
// reaching no service. A present but empty Origin is refused by Envoy; nginx cannot tell it from no Origin and lets it
// through, which no browser shows, because a browser's Origin is always an origin or null (the Fetch standard, the
// Origin header). Envoy also answers both 403s with the body Invalid CORS request, which no row asserts.
var cors = scenario{name: "CORS",
	exchanges: []exchange{
		{name: "a preflight from a listed origin", keys: []string{"cors.allow_methods", "cors.allow_headers", "cors.max_age",
			"origins.<environment name>.origins"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST", "access-control-request-headers": "content-type,x-custom",
				"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-max-age": "600"}}},
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
				received: map[string]string{"origin": "https://evil.example.net,https://www.example.com"}}},
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
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-methods": "POST,PUT"}}},
		{name: "a preflight asking for headers on two lines", keys: []string{"cors.allow_headers", "origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST",
					"access-control-request-headers": "x-a\r\naccess-control-request-headers: x-b"}, raw: true},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-allow-headers": "x-a,x-b"}}},
		{name: "a refused path from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/a/..%3B/b", headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "a preflight to a refused path from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/a/..%3B/b",
				headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "GET"}},
			want: answer{status: 400, headers: map[string]string{"access-control-allow-origin": ""}}},
		{name: "an upgrade other than WebSocket from a listed origin carries no CORS answer", keys: []string{"origins.<environment name>.origins"},
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
				headers: map[string]string{"origin": "https://www.example.com", "upgrade": "spdy/3.1", "connection": "upgrade"}},
			want: answer{status: 403, headers: map[string]string{"access-control-allow-origin": ""}}},
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
		{name: "a present but empty Origin", keys: hostKeys, send: fromOrigin(""), want: answer{status: 403},
			by: map[string]own{"nginx": {difference: "origins.<environment name>.hosts", literal: literal{want: answer{status: 200, service: "shop"}}}}},
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
// forged one. The // check itself receives authorization, x-country, x-forwarded-method, x-forwarded-uri with the
// query, x-forwarded-proto: https, x-forwarded-host, x-gateway-timeout-ms: 1500, Envoy's own x-envoy-internal: true and
// x-envoy-expected-rq-timeout-ms: 1500, the original method and the host the client sent. The stub's check denies a
// request with no token it knows, so the first row fails if the check is asked. A client's own x-forwarded-uri and
// x-forwarded-method reach the service as sent, which the format allows, but never the check. The headers a client may
// not send are three rows, because a file may write remove_from_clients without the check, and the check without
// auth.jwt: x-client-id, which remove_from_clients alone removes; the check's x-user-id and question; and x-subject and
// x-roles, the headers auth.jwt.claims sets from a verified token, which only a file that writes the claims runs.
var auth = scenario{name: "auth",
	exchanges: []exchange{
		{name: "no authorization header, so the check is not asked", keys: []string{"auth.ask", "auth.only_with_header"}, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "the check allows", keys: []string{"auth.ask", "auth.send", "auth.accept", "auth.send_request"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer allow", "x-country": "RO", "x-forwarded-uri": "/admin", "x-forwarded-method": "DELETE",
				"x-gateway-action": "forged"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-id": "u-1", "x-tier-level": "gold",
				"x-user-check": "GET /products/1?q=1 action= reports= country=RO", "authorization": "Bearer allow"}, absent: []string{"x-other"}}},
		{name: "the check denies with 401", keys: []string{"auth.ask"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer deny-401", "x-country": "RO"}},
			want: answer{status: 401, headers: map[string]string{"x-envoy-upstream-service-time": ""}}},
		{name: "the check denies with 403", keys: []string{"auth.ask"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer deny-403"}},
			want: answer{status: 403, headers: map[string]string{"x-envoy-upstream-service-time": ""}}},
		{name: "the check answers 503 and when_unavailable is allow", keys: []string{"auth.ask", "auth.when_unavailable"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer status-503"}}, want: answer{status: 200, service: "shop", marked: "true"}},
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
				absent: []string{"x-user-id", "x-gateway-action"}}},
		{name: "a header remove_from_clients names a client may not send", keys: []string{"remove_from_clients"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-client-id": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"}, absent: []string{"x-client-id"}}},
		{name: "headers a verified token sets a client may not send", keys: []string{"auth.jwt.claims"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-subject": "forged", "x-roles": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"}, absent: []string{"x-subject", "x-roles"}}},
	}}

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
// milliseconds, and one that waited for the check's own answer takes 3 s.
var timeout = scenario{name: "timeout",
	exchanges: []exchange{
		{name: "the check answers after auth.timeout", keys: []string{"auth.timeout", "auth.when_unavailable"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer hang"}},
			want: answer{status: 200, service: "shop", marked: "true", atLeast: 1450 * time.Millisecond, below: 2950 * time.Millisecond}},
	}}

// retry: every value measured on Envoy 1.40.2. A request that reached the service is never tried again, and one whose
// connection a backend refused is tried again on a backend it has not tried: with shop-2 refusing connections, each of
// 40 requests and of 40 gRPC calls is answered by shop-1, where a retry that picked its backend as the first try did
// left 13 of the 40 requests answered 502 and 12 of the 40 calls with grpc-status 14. 40 requests send about 20 first
// tries to shop-2, so a retry that still picked shop-2 now and then would fail the row. Envoy also answers, and no row
// asserts: the 502 with an empty body, and the gRPC call with HTTP 200 and, in the headers, grpc-status: 14 and
// grpc-message: upstream connect error or disconnect/reset before headers. reset reason: remote reset.
var retry = scenario{name: "retry",
	exchanges: []exchange{
		{name: "a first try closed after it reached the service is not tried again", keys: []string{"routes[].retries"}, send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-fail-first": "shop-once"}}, want: answer{status: 502}},
		{name: "an answer of 503 is passed on", keys: []string{"routes[].retries"}, send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"x-stub-status": "503"}}, want: answer{status: 503, service: "shop", attempt: 1}},
		{name: "a gRPC call whose first try was reset is not tried again", keys: []string{"routes[].rpc.retries"}, send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"x-stub-fail-first": "cart-once"}}, want: answer{grpcStatus: "14"}},
		{name: "a retry after a connection failure reaches the other backend", keys: []string{"routes[].retries"}, send: get("/products/1"), repeat: 40, every: true,
			refusing: "shop-2", want: answer{status: 200, service: "shop", instance: "shop-1"}},
		{name: "a gRPC call retried after a connection failure reaches the other backend", keys: []string{"routes[].rpc.retries"},
			send: rpc("/example.shop.v1.CartService/GetCart"), repeat: 40, every: true, refusing: "shop-2",
			want: answer{status: 200, grpcStatus: "0", service: "shop", instance: "shop-1"}},
	}}

// gRPC: every value measured on Envoy 1.40.2. The question the check is told, x-gateway-action and
// x-gateway-reports, is for the check alone, so the service receives neither. A gRPC service's X-Accel-Redirect
// reaches the client as an HTTP service's does (routing), and a call is routed by the path normalisation leaves.
var gRPC = scenario{name: "gRPC",
	exchanges: []exchange{
		{name: "a method of a package mapped to shop", keys: []string{"routes[].rpc", "routes[].rpc.to", "routes[].rpc.to.<package pattern>",
			"routes[].rpc.to.<package pattern>.service", "rpc.methods", "rpc.methods[].service", "rpc.methods[].method", "services.<service name>.grpc"},
			send: rpc("/example.shop.v1.CartService/GetCart"),
			want: answer{status: 200, grpcStatus: "0", service: "shop", port: "8081", proto: "HTTP/2.0", path: "/example.shop.v1.CartService/GetCart"}},
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
		{name: "an invalid escape", send: get("/catalog/a%zz"), want: answer{status: 400, unreadable: true},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an escaped plus", send: get("/catalog/x%2By"), want: answer{status: 200, service: "catalog", path: "/catalog/x%2By"}},
		{name: "an empty query", send: get("/catalog/x?"), want: answer{status: 200, service: "catalog", path: "/catalog/x?"}},
		{name: "an empty query after a path that needs normalising", send: get("/products//x?"), want: answer{status: 200, service: "shop", path: "/products/x?"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a dot segment that keeps a ; parameter", send: get("/catalog/%2e%2e;/products/1"), want: answer{status: 400}},
		{name: "a dot-dot segment with ; escaped in upper case hex", send: get("/catalog/..%3B/products/1"), want: answer{status: 400}},
		{name: "a dot-dot segment with ; escaped in lower case hex", send: get("/catalog/..%3b/products/1"), want: answer{status: 400}},
		{name: "escaped dots with an escaped ;", send: get("/catalog/%2e%2e%3b/products/1"), want: answer{status: 400}},
		{name: "escaped dots with an escaped ; all in upper case hex", send: get("/catalog/%2E%2E%3B/products/1"), want: answer{status: 400}},
		{name: "a dot and an escaped dot with an escaped ;", send: get("/catalog/.%2e%3B/products/1"), want: answer{status: 400}},
		{name: "a dot segment with an escaped ;", send: get("/catalog/items/.%3B/x"), want: answer{status: 400}},
		{name: "an escaped dot segment with an escaped ;", send: get("/catalog/items/%2e%3b/x"), want: answer{status: 400}},
		{name: "an escaped ; that carries a value", send: get("/catalog/..%3Bx=1/products/1"), want: answer{status: 400}},
		{name: "two escaped ;", send: get("/catalog/..%3B%3B/products/1"), want: answer{status: 400}},
		{name: "an escaped ; on the last segment", send: get("/catalog/..%3B"), want: answer{status: 400}},
		{name: "an escaped ; on the first segment", send: get("/..%3B/products/1"), want: answer{status: 400}},
		{name: "an escaped ; between backslashes", send: get("/catalog\\..%3B\\products\\1"), want: answer{status: 400}},
		{name: "an escaped ; between backslashes written as %5C", send: get("/catalog%5C..%3b%5Cproducts%5C1"), want: answer{status: 400}},
		{name: "an escaped ; before an escaped slash", send: get("/catalog/..%3B%2Fproducts/1"), want: answer{status: 400}},
		{name: "a ; parameter on an ordinary segment", send: get("/a;v=1/b"), want: answer{status: 200, service: "shop", path: "/a;v=1/b"}},
		{name: "a ; parameter on a name", send: get("/catalog/readme.txt;x=1"),
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
// catalog to 20 requests a second with no burst, and the path normalisation rows just before spend most of that (one
// run in eleven got 429).
var asWritten = scenario{name: "the path as the client wrote it",
	exchanges: []exchange{
		{name: "a path with nothing to normalise", send: get("/xab"), want: answer{status: 200, service: "shop", path: "/xab"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/xab"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/xab"}}}},
		{name: "an escaped space is matched as written", send: get("/api/a%20b/admin"), want: answer{status: 200, service: "shop", path: "/api/a%20b/admin"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/api/a%20b/admin"}}}},
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
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/api/aAb/admin"}}}},
		{name: "a path that needs normalising and keeps an escape", send: get("/api/a%2fb%3bc"),
			want: answer{status: 200, service: "shop", path: "/api/a/b%3bc"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a byte Envoy escapes and an escape", send: get("/products/a|b%3Bc"),
			want: answer{status: 200, service: "shop", path: "/products/a%7Cb%3Bc"},
			by:   map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "a byte Envoy escapes", send: get("/products/a|b"), want: answer{status: 200, service: "shop", path: "/products/a%7Cb"},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: []string{"nginx-file"}, literal: literal{want: answer{status: 400}}}}},
		{name: "an escape on a route with a rewrite", send: get("/hooks/a%3Bb"), want: answer{status: 200, service: "shop", path: "/hooks/a%3Bb"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}},
				"nginx-plain":  {want: answer{status: 200, service: "accounts", path: "/in/a%3Bb"}}},
			by: map[string]own{"nginx": {difference: "routes[].rewrite", literal: literal{want: answer{status: 400}}}}},
		{name: "a route with a rewrite and a path with nothing to normalise", send: get("/hooks/ab"), want: answer{status: 200, service: "shop", path: "/hooks/ab"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/in/ab"}},
				"nginx-plain":  {want: answer{status: 200, service: "accounts", path: "/in/ab"}}}},
		{name: "an empty query on a route with a rewrite", send: get("/hooks/ab?"), want: answer{status: 200, service: "shop", path: "/hooks/ab?"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/in/ab?"}},
				"nginx-plain":  {want: answer{status: 200, service: "accounts", path: "/in/ab?"}}},
			by: map[string]own{"nginx": {difference: "routes[].rewrite", literal: literal{want: answer{status: 400}}}}},
		{name: "an empty query after a byte Envoy escapes", send: get("/products/a|b?"), want: answer{status: 200, service: "shop", path: "/products/a%7Cb?"},
			by: map[string]own{"nginx": {difference: pathRow, literal: literal{want: answer{status: 400}}}}},
		{name: "an empty query after a path with nothing to normalise", send: get("/xab?"), want: answer{status: 200, service: "shop", path: "/xab?"},
			on: map[string]literal{"nginx-file": {want: answer{status: 200, service: "accounts", path: "/xab?"}},
				"haproxy-file": {want: answer{status: 200, service: "accounts", path: "/xab?"}}}},
		{name: "a # in the path", send: get("/products/a#b"), want: answer{status: 400}},
		// HAProxy's HTTP/2 parser resets the stream of a :path that holds a # (lineRow).
		{name: "a # in the path over HTTP 2", send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/a#b"}, want: answer{status: 400},
			by: map[string]own{"haproxy": {difference: lineRow, literal: literal{want: answer{refused: true}}}}},
		{name: "an escaped NUL in the path is answered 400", send: get("/products/a%00b"), want: answer{status: 400}},
		{name: "a byte above 0x7F over HTTP 2 reaches the service escaped", send: request{proto: "h2", method: "GET", host: "localhost",
			path: "/products/\xc3\xa9", raw: true}, want: answer{status: 200, service: "shop", path: "/products/%C3%A9"},
			by: map[string]own{"nginx": {difference: "routes[].pattern", files: []string{"nginx-file"}, literal: literal{want: answer{status: 400}}}}},
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
			want: answer{status: 401}},
		{name: "a dot-dot segment and a backslash leave the route", keys: offKeys, send: deny("/public/..\\products/1"), want: answer{status: 401}},
		{name: "a dot-dot segment leaves the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/../products/1"), want: toldProducts},
		{name: "encoded dots leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/%2e%2e/products/1"), want: toldProducts},
		{name: "a dot-dot segment and an escaped slash leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/..%2fproducts/1"), want: toldProducts},
		{name: "a dot-dot segment and a backslash written as %5C leave the route, and the check is told the path the service receives",
			keys: toldKeys, send: allowed("/public/..%5Cproducts/1"), want: toldProducts},
		{name: "a dot-dot segment and a backslash leave the route, and the check is told the path the service receives", keys: toldKeys,
			send: allowed("/public/..\\products/1"), want: toldProducts},
		{name: "a dot-dot segment with a written ; leaves the route", keys: offKeys, send: deny("/public/..;/products/1"), want: answer{status: 401}},
		{name: "a dot segment that keeps a ; parameter", keys: offKeys, send: get("/public/%2e%2e;/products/1"), want: answer{status: 400}},
		{name: "a dot segment that keeps a ; parameter and a token the check allows", keys: offKeys, send: allowed("/public/%2e%2e;/products/1"),
			want: answer{status: 400}},
		{name: "a dot segment with an escaped ; and no token", keys: offKeys, send: get("/public/..%3B/products/1"), want: answer{status: 400}},
		{name: "a dot segment with an escaped ; and a token the check allows", keys: offKeys, send: allowed("/public/..%3B/products/1"),
			want: answer{status: 400}},
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
// requests back to back took 24 ms; the first 30 were admitted and the last was refused. There the route's bucket
// holds 30 (20 requests a second and a burst of 10) and refills 20 a second, so 60 requests empty it while each takes
// under 25 ms; on a slower runner the row fails and says how long the 60 took. The requests after the burst carry a token the check
// denies and one the token filter refuses, each answered 401 alone on this route, so a 429 shows that the limit is
// applied before the token and the check. They are sent 10 times, because the bucket refills one request every 50 ms
// and on a slow runner the first of them may find it refilled. Envoy also answers, and no row asserts: the 429 with
// the body local_rate_limited and content-type text/plain; a Connect call over the RPC route's limit with 429 and
// {"code":"resource_exhausted","message":"local_rate_limited"}; and a gRPC call over it with grpc-status: 8 (of 110
// GetCart calls in 103 ms, 109 were admitted, the bucket refilling as they went). examples/portable's route holds 20
// (20 requests a second and no burst), so there the first 20 are admitted; the bucket refills one request every
// 50 ms, so its 429 is missed only when the 40 requests after the first 20 take about 2 s, and on one arm64 Mac, over
// two sets of 30 runs, the 60 took 0.02 to 0.04 s.
var rateLimit = scenario{name: "rate limit",
	quiet: 2500 * time.Millisecond,
	exchanges: []exchange{
		{name: "requests and burst are admitted, then the route refuses", keys: []string{"routes[].rate_limit", "routes[].rate_limit.requests",
			"routes[].rate_limit.per", "routes[].rate_limit.burst", "routes[].rate_limit.by"},
			send: get("/catalog/items"), repeat: 60, admitted: 30, want: answer{status: 429},
			on: map[string]literal{"portable": {admitted: 20, want: answer{status: 429}}}},
		{name: "a request over the limit is refused before its token or the check is read", keys: []string{"routes[].rate_limit", "auth.ask"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/catalog/items",
				headers: map[string]string{"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			repeat: 10, want: answer{status: 429}},
		{name: "another route is not limited", keys: []string{"routes[].rate_limit"}, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
	}}

// The keys of cost's rows: the long path's answer depends on the routes it passes and on the origin it carries, the
// long Origin's on the patterns that read it, and the flood's on the pattern routes its paths pass and the prefix route
// that answers them. nginx's files write no origin list, and nginx reads a request line of at most 8 KB, so neither
// of the first two runs there.
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
// workers and with 4, because RE2 keeps a cache for each pattern, not each worker. The bound, 96 MB and 4 MB a
// thread, 144 MB with 12 threads, fails the first two and leaves the last two over 60 MB.
var cost = scenario{name: "cost",
	exchanges: []exchange{
		{name: "a path of 14,000 bytes that 24 pattern routes read", keys: longPathKeys,
			send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/api/" + strings.Repeat("a", 14000),
				headers: map[string]string{"origin": "https://app.example.com"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://app.example.com"},
				below: 500 * time.Millisecond}},
		{name: "the request after the long path is served", keys: longPathKeys, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "an Origin of 8,000 bytes that 64 patterns of two * read", keys: longOriginKeys,
			send: request{proto: "http/1.1", method: "GET", host: "patterns.example.net", path: "/products/1",
				headers: map[string]string{"origin": "https://" + strings.Repeat("a.", 4000) + "example1.orgx"}},
			want: answer{status: 403, below: 500 * time.Millisecond},
			// measured on Envoy 1.40.2: no environment of full or portable has the host
			on: map[string]literal{"full": {want: answer{status: 404}}, "portable": {want: answer{status: 404}}}},
		{name: "the request after the long Origin is served", keys: longOriginKeys, send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "a flood of new paths that 40 pattern routes read keeps the proxy's memory bounded", keys: floodKeys,
			// The flood gives each request its path (floodPath).
			send:  request{proto: "http/1.1", method: "GET", host: "localhost"},
			flood: &flood{connections: 24, requests: 3000, path: floodPath, fixed: 96 << 20, perThread: 4 << 20},
			want:  answer{status: 200, service: "shop"}},
	}}
