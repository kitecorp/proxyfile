package conformance

import "time"

// scenarios run in this order, each exchange after the one before, never in parallel: a later scenario's quiet and
// burst count on nothing else reaching the proxy meanwhile. The rate limit runs last, so the requests the scenarios
// before it send to its route come first, and its quiet lets that route's bucket fill again before its burst.
var scenarios = []scenario{routing, mutualTLS, cors, auth, timeout, retry, gRPC, paths, rateLimit}

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
	keys: []string{"routes[].pattern", "routes[].path", "routes[].method", "routes[].rewrite", "routes[].prefix", "routes[].to",
		"services.<service name>.http", "services.<service name>.addresses", "services.<service name>.address",
		"services.<service name>.host_header", "dns.resolver", "origins.<environment name>.hosts"},
	exchanges: []exchange{
		{name: "a pattern route", send: get("/events/abc-123"),
			want: answer{status: 200, service: "accounts", port: "7070", method: "GET", path: "/events/abc-123", host: "127.0.0.1:7070"}},
		{name: "a pattern miss falls to the prefix route", send: get("/events/ABC"),
			want: answer{status: 200, service: "shop", port: "8080", path: "/events/ABC", host: "localhost"}},
		{name: "a path and method route with a rewrite", send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/api/webhooks"},
			want: answer{status: 200, service: "accounts", port: "7070", method: "POST", path: "/hooks/payments", host: "127.0.0.1:7070"}},
		{name: "another method falls to the prefix route", send: get("/api/webhooks"),
			want: answer{status: 200, service: "shop", port: "8080", path: "/api/webhooks"}},
		{name: "a service found by DNS", send: get("/catalog/items?id=7"),
			want: answer{status: 200, service: "catalog", port: "8090", path: "/catalog/items?id=7", host: "localhost"}},
		{name: "the prefix route", send: get("/products/1"),
			want: answer{status: 200, service: "shop", port: "8080", proto: "HTTP/1.1", method: "GET", path: "/products/1", host: "localhost"}},
		{name: "over HTTP 2 the service still hears HTTP 1.1", send: request{proto: "h2", method: "GET", host: "localhost", path: "/products/1"},
			want: answer{status: 200, service: "shop", proto: "HTTP/1.1", path: "/products/1"}},
		{name: "another environment's host", send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1"},
			want: answer{status: 200, service: "shop", host: "api.example.com"}},
		// measured on Envoy 1.40.2: 404 with an empty body
		{name: "a host no environment names", send: request{proto: "http/1.1", method: "GET", host: "other.example.net", path: "/products/1"},
			want: answer{status: 404}},
	}}

var mutualTLS = scenario{name: "mutual TLS", keys: []string{"tls.certificate", "tls.key", "tls.clients.ca"},
	exchanges: []exchange{
		// measured on Envoy 1.40.2: the handshake fails with "remote error: tls: certificate required"
		{name: "no client certificate", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", noCertificate: true},
			want: answer{refused: true}},
		// measured on Envoy 1.40.2: the handshake fails with "remote error: tls: unknown certificate authority". The
		// other CA carries the clients CA's name, so a proxy that matched the issuer by name would let it through.
		{name: "a client certificate another CA signed", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1", otherCA: true},
			want: answer{refused: true}},
	}}

// cors: every value measured on Envoy 1.40.2. Envoy also answers, and no row asserts: access-control-allow-methods
// with the method asked for and access-control-allow-headers with the headers asked for, or empty, because both keys
// are "*"; access-control-expose-headers on the preflights too; vary: Origin on every answer; and the body Invalid
// CORS request with content-type text/plain on both 403s. The preflight from a listed origin and the request from
// another origin carry a token the check denies and one the token filter refuses, each answered 401 alone on the same
// host and path: their 200 and 403 show that the origin is decided before the token and the check.
var cors = scenario{name: "CORS", keys: []string{"cors.allow_methods", "cors.allow_headers", "cors.expose_headers", "cors.max_age",
	"origins.<environment name>.origins", "origins.<environment name>.patterns"},
	exchanges: []exchange{
		{name: "a preflight from a listed origin", send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com", "access-control-request-method": "POST", "access-control-request-headers": "content-type,x-custom",
				"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://www.example.com", "access-control-max-age": "600"}}},
		{name: "a preflight from an origin a pattern covers", send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://shop.example.com", "access-control-request-method": "PUT"}},
			want: answer{status: 200, headers: map[string]string{"access-control-allow-origin": "https://shop.example.com", "access-control-max-age": "600"}}},
		{name: "a preflight from another origin", send: request{proto: "http/1.1", method: "OPTIONS", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://evil.example.net", "access-control-request-method": "POST"}},
			want: answer{status: 403}},
		{name: "a request from a listed origin", send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://www.example.com"}},
			want: answer{status: 200, service: "shop", headers: map[string]string{"access-control-allow-origin": "https://www.example.com",
				"access-control-expose-headers": "Content-Disposition,X-Request-Id"}, received: map[string]string{"origin": "https://www.example.com"}}},
		{name: "a request from another origin", send: request{proto: "http/1.1", method: "GET", host: "api.example.com", path: "/products/1",
			headers: map[string]string{"origin": "https://evil.example.net", "authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			want: answer{status: 403}},
	}}

// auth: every value measured on Envoy 1.40.2. Envoy also answers, and no row asserts: the check's own body and
// headers on a denial, with x-envoy-upstream-service-time: 0 beside them, because the router's
// suppress_envoy_headers does not cover the check's denial; 403 with an empty body when the check answers 204; and
// x-envoy-auth-failure-mode-allowed: true to the service when the check answers 503, closes or times out. The check
// itself receives authorization, x-country, x-forwarded-method, x-forwarded-uri with the query, x-forwarded-proto:
// https, x-forwarded-host, x-gateway-timeout-ms: 1500, the original method and the host the client sent. The
// stub's check denies a request with no token it knows, so the first row fails if the check is asked. A client's own
// x-forwarded-uri and x-forwarded-method reach the service as sent, which the format allows, but never the check.
// Of the headers a client may not send, x-client-id is removed by remove_from_clients alone, and x-subject and x-roles
// are the headers auth.jwt.claims sets from a verified token.
var auth = scenario{name: "auth", keys: []string{"auth.ask", "auth.only_with_header", "auth.send", "auth.accept", "auth.send_request",
	"auth.when_unavailable", "routes[].auth", "remove_from_clients", "auth.jwt.claims"},
	exchanges: []exchange{
		{name: "no authorization header, so the check is not asked", send: get("/products/1"), want: answer{status: 200, service: "shop"}},
		{name: "the check allows", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer allow", "x-country": "RO", "x-forwarded-uri": "/admin", "x-forwarded-method": "DELETE",
				"x-gateway-action": "forged"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-user-id": "u-1", "x-tier-level": "gold",
				"x-user-check": "GET /products/1?q=1 action= reports= country=RO", "authorization": "Bearer allow"}, absent: []string{"x-other"}}},
		{name: "the check denies with 401", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1?q=1",
			headers: map[string]string{"authorization": "Bearer deny-401", "x-country": "RO"}}, want: answer{status: 401}},
		{name: "the check denies with 403", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer deny-403"}}, want: answer{status: 403}},
		{name: "the check answers 503 and when_unavailable is allow", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer status-503"}}, want: answer{status: 200, service: "shop"}},
		{name: "a route with auth off", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/events/abc-123",
			headers: map[string]string{"authorization": "Bearer deny-401"}}, want: answer{status: 200, service: "accounts"}},
		{name: "headers a client may not send", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"x-user-id": "forged", "x-gateway-action": "forged", "x-client-id": "forged", "x-subject": "forged",
				"x-roles": "forged", "x-keep": "yes"}},
			want: answer{status: 200, service: "shop", received: map[string]string{"x-keep": "yes"},
				absent: []string{"x-user-id", "x-gateway-action", "x-client-id", "x-subject", "x-roles"}}},
	}}

// timeout: measured on Envoy 1.40.2. The check holds the request for 3 s, past auth.timeout's 1500 ms, and
// when_unavailable is allow, so the request goes on to the service when the timeout ends.
var timeout = scenario{name: "timeout", keys: []string{"auth.timeout", "auth.when_unavailable"},
	exchanges: []exchange{
		{name: "the check answers after auth.timeout", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"authorization": "Bearer hang"}},
			want: answer{status: 200, service: "shop", atLeast: 1500 * time.Millisecond, below: 3 * time.Second}},
	}}

// retry: every value measured on Envoy 1.40.2. A request that reached the service is never tried again. Envoy also
// answers, and no row asserts: the 502 with an empty body, and the gRPC call with HTTP 200 and, in the headers,
// grpc-status: 14 and grpc-message: upstream connect error or disconnect/reset before headers. reset reason: remote
// reset. No row retries after a connect failure: the retry picks a backend as the first try did, so it can pick the
// one that just failed (with one of the two shop backends refusing connections, 13 of 40 requests answered 502).
var retry = scenario{name: "retry", keys: []string{"routes[].retries", "routes[].rpc.retries"},
	exchanges: []exchange{
		{name: "a first try closed after it reached the service is not tried again", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/products/1", headers: map[string]string{"x-stub-fail-first": "shop-once"}}, want: answer{status: 502}},
		{name: "an answer of 503 is passed on", send: request{proto: "http/1.1", method: "GET", host: "localhost", path: "/products/1",
			headers: map[string]string{"x-stub-status": "503"}}, want: answer{status: 503, service: "shop", attempt: 1}},
		{name: "a gRPC call whose first try was reset is not tried again", send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"x-stub-fail-first": "cart-once"}}, want: answer{grpcStatus: "14"}},
	}}

// gRPC: every value measured on Envoy 1.40.2. The question the check is told, x-gateway-action and
// x-gateway-reports, is for the check alone, so the service receives neither.
var gRPC = scenario{name: "gRPC", keys: []string{"routes[].rpc", "routes[].rpc.to", "routes[].rpc.to.<package pattern>",
	"routes[].rpc.to.<package pattern>.service", "rpc.methods", "rpc.methods[].service", "rpc.methods[].method",
	"rpc.methods[].action", "rpc.methods[].reports", "services.<service name>.grpc", "services.<service name>.health"},
	exchanges: []exchange{
		{name: "a method of a package mapped to shop", send: rpc("/example.shop.v1.CartService/GetCart"),
			want: answer{status: 200, grpcStatus: "0", service: "shop", port: "8081", proto: "HTTP/2.0", path: "/example.shop.v1.CartService/GetCart"}},
		{name: "a method every other package sends to accounts", send: rpc("/example.accounts.v1.ProfileService/GetProfile"),
			want: answer{status: 200, grpcStatus: "0", service: "accounts", port: "7070", proto: "HTTP/2.0", host: "127.0.0.1:7070"}},
		{name: "the check is told the method's action and reports", send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/AddItem", headers: map[string]string{"authorization": "Bearer allow", "x-gateway-action": "forged"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop",
				received: map[string]string{"x-user-check": "POST /example.shop.v1.CartService/AddItem action=buy reports=browse country="},
				absent:   []string{"x-gateway-action", "x-gateway-reports"}}},
		{name: "a method with an action and no reports", send: request{proto: "grpc", method: "POST", host: "localhost",
			path: "/example.shop.v1.CartService/GetCart", headers: map[string]string{"authorization": "Bearer allow"}},
			want: answer{status: 200, grpcStatus: "0", service: "shop",
				received: map[string]string{"x-user-check": "POST /example.shop.v1.CartService/GetCart action=browse reports= country="}}},
	}}

// paths: every value measured on Envoy 1.40.2; no key, because path normalisation is written for every file
// (docs/portability.md). Envoy also answers, and no row asserts: the 400 with an empty body.
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
		{name: "an exact path reached through a dot-dot segment", send: request{proto: "http/1.1", method: "POST", host: "localhost", path: "/x/../api/webhooks"},
			want: answer{status: 200, service: "accounts", path: "/hooks/payments"}},
	}}

// rateLimit: every value measured on Envoy 1.40.2: after 2.5 s with no request to the route, 60 requests back to
// back took 24 ms; the first 30 were admitted and the last was refused. The route's bucket holds 30 (20 requests a
// second and a burst of 10) and refills 20 a second, so 60 requests empty it while each takes under 25 ms; on a
// slower runner the row fails and says how long the 60 took. The requests after the burst carry a token the check
// denies and one the token filter refuses, each answered 401 alone on this route, so a 429 shows that the limit is
// applied before the token and the check. They are sent 10 times, because the bucket refills one request every 50 ms
// and on a slow runner the first of them may find it refilled. Envoy also answers, and no row asserts: the 429 with
// the body local_rate_limited and content-type text/plain; a Connect call over the RPC route's limit with 429 and
// {"code":"resource_exhausted","message":"local_rate_limited"}; and a gRPC call over it with grpc-status: 8 (of 110
// GetCart calls in 103 ms, 109 were admitted, the bucket refilling as they went).
var rateLimit = scenario{name: "rate limit", keys: []string{"routes[].rate_limit", "routes[].rate_limit.requests",
	"routes[].rate_limit.per", "routes[].rate_limit.burst", "routes[].rate_limit.by"},
	quiet: 2500 * time.Millisecond,
	exchanges: []exchange{
		{name: "requests and burst are admitted, then the route refuses", send: get("/catalog/items"), repeat: 60, admitted: 30, want: answer{status: 429}},
		{name: "a request over the limit is refused before its token or the check is read", send: request{proto: "http/1.1", method: "GET",
			host: "localhost", path: "/catalog/items", headers: map[string]string{"authorization": "Bearer deny-401", "x-access-token": "not-a-token"}},
			repeat: 10, want: answer{status: 429}},
		{name: "another route is not limited", send: get("/products/1"), want: answer{status: 200, service: "shop"}},
	}}
