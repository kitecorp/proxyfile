package conformance

import (
	"slices"
	"time"
)

// scenarios run in this order, each exchange after the one before, never in parallel: a later scenario's quiet and
// burst count on nothing else reaching the proxy meanwhile. The rate limit runs last, so the requests the scenarios
// before it send to its route come first, and its quiet lets that route's bucket fill again before its burst.
var scenarios = []scenario{routing, mutualTLS, cors, auth, timeout, retry, gRPC, paths, offRoute, rateLimit}

// get is a GET over HTTP/1.1 to localhost, a host of the example's local environment.
func get(path string) request {
	return request{proto: "http/1.1", method: "GET", host: "localhost", path: path}
}

// rpc is a unary gRPC call to localhost with the empty message.
func rpc(path string) request {
	return request{proto: "grpc", method: "POST", host: "localhost", path: path}
}

// routing: every value measured on Envoy 1.40.2. A shop answer may come from either backend, so no row names the
// instance.
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
		{name: "the prefix route", keys: []string{"routes[].prefix", "routes[].to", "services.<service name>.http"}, send: get("/products/1"),
			want: answer{status: 200, service: "shop", port: "8080", proto: "HTTP/1.1", method: "GET", path: "/products/1", host: "localhost"}},
		{name: "over HTTP 2 the service still hears HTTP 1.1", keys: []string{"routes[].prefix"}, send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1"},
			want: answer{status: 200, service: "shop", proto: "HTTP/1.1", path: "/products/1"}},
		{name: "another environment's host", keys: []string{"origins.<environment name>.hosts"}, send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1"},
			want: answer{status: 200, service: "shop", host: "api.example.com"}},
		// measured on Envoy 1.40.2: 404 with an empty body
		{name: "a host no environment names", keys: []string{"origins.<environment name>.hosts"}, send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/1"},
			want: answer{status: 404}},
	}}

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
	}}

// cors: every value measured on Envoy 1.40.2. An origin on one of the environment's own hosts is admitted whether or
// not a list holds it, and only a list gives it access-control-allow-origin: https://api.example.org, the request's
// own host and port, is in neither list of the live environment, whose patterns cover api.example.com but not
// api.example.org. Envoy also answers, and no row asserts: access-control-allow-methods with the method asked for and
// access-control-allow-headers with the headers asked for, or empty, because both keys are "*";
// access-control-expose-headers on the preflights from a listed origin too; vary: Origin on every answer; and the
// body Invalid CORS request with content-type text/plain on both 403s. The preflight from a listed origin and the request from
// another origin carry a token the check denies and one the token filter refuses, each answered 401 alone on the same
// host and path: their 200 and 403 show that the origin is decided before the token and the check.
var cors = scenario{name: "CORS",
	exchanges: []exchange{
		{name: "a preflight from a listed origin", keys: []string{"cors.allow_methods", "cors.allow_headers", "cors.max_age",
			"origins.<environment name>.origins", "auth.ask"}, send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
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
		{name: "a request from another origin", keys: []string{"origins.<environment name>.origins", "origins.<environment name>.patterns",
			"auth.ask"}, send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
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
	}}

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
// not send are two rows, because a file may write the check without auth.jwt: x-client-id, which remove_from_clients
// alone removes, beside the check's x-user-id and question; and x-subject and x-roles, the headers auth.jwt.claims sets
// from a verified token, which only a file that writes the claims runs.
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
		{name: "headers of the check and of remove_from_clients a client may not send", keys: []string{"auth.ask", "auth.accept", "remove_from_clients"},
			send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
				headers: map[string]string{"x-user-id": "forged", "x-gateway-action": "forged", "x-client-id": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"},
				absent: []string{"x-user-id", "x-gateway-action", "x-client-id"}}},
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
// x-gateway-reports, is for the check alone, so the service receives neither.
var gRPC = scenario{name: "gRPC",
	exchanges: []exchange{
		{name: "a method of a package mapped to shop", keys: []string{"routes[].rpc", "routes[].rpc.to", "routes[].rpc.to.<package pattern>",
			"routes[].rpc.to.<package pattern>.service", "rpc.methods", "rpc.methods[].service", "rpc.methods[].method", "services.<service name>.grpc"},
			send: rpc("/example.shop.v1.CartService/GetCart"),
			want: answer{status: 200, grpcStatus: "0", service: "shop", port: "8081", proto: "HTTP/2.0", path: "/example.shop.v1.CartService/GetCart"}},
		{name: "a method every other package sends to accounts", keys: []string{"routes[].rpc.to.<package pattern>", "rpc.methods",
			"services.<service name>.grpc"}, send: rpc("/example.accounts.v1.ProfileService/GetProfile"),
			want: answer{status: 200, grpcStatus: "0", service: "accounts", port: "7070", proto: "HTTP/2.0", host: "127.0.0.1:7070"}},
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
// query pass as sent. Envoy also answers, and no row asserts: the 400 with an empty body; and /catalog/..;/products/1,
// with the dots and the ; written, reaches shop as /products/1, because Envoy drops the parameter and resolves the
// segment itself, while with envoy.reloadable_features.strip_dotdot_segments_with_parameters false, and on Envoy
// 1.33.14, which do not, the same request answers 400; its answer depends on the Envoy version and that setting, so no
// row checks it.
var paths = scenario{name: "path normalisation",
	exchanges: []exchange{
		{name: "a dot-dot segment", send: get("/catalog/../products/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot segment", send: get("/catalog/./items"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "repeated slashes", send: get("/catalog//items"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "an encoded slash", send: get("/catalog%2Fitems"), want: answer{status: 200, service: "catalog", path: "/catalog/items"}},
		{name: "encoded dots", send: get("/catalog/%2e%2e/products/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot-dot segment and an escaped slash", send: get("/catalog/..%2fproducts/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot-dot segment and a backslash written as %5C", send: get("/catalog/..%5Cproducts/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
		{name: "a dot-dot segment and a backslash", send: get("/catalog/..\\products/1"), want: answer{status: 200, service: "shop", path: "/products/1"}},
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
			want: answer{status: 200, service: "catalog", path: "/catalog/products/1"}},
		{name: "an exact path reached through a dot-dot segment", send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/x/../api/webhooks"},
			want: answer{status: 200, service: "accounts", path: "/hooks/payments"}},
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
