# Conformance

The conformance battery runs a proxy on the configuration proxyfile renders for it and checks that the proxy answers
requests as the proxyfile says. It is the Go package [`conformance`](../conformance), whose files are test files only,
so the released program never carries them. It runs each target of this version: Envoy on Envoy 1.40.2, nginx on nginx
1.30.6 and 1.31.8, and HAProxy on HAProxy 3.2.26 and 3.4.7.

## What the battery proves

Each entry of the battery is a proxy image and the proxyfiles it runs. Envoy's entry, `envoy-1.40`, runs five:
[`examples/full/hocon/Proxyfile`](../examples/full/hocon/Proxyfile), which writes every key but the few its opening
comment names, [`examples/portable/hocon/Proxyfile`](../examples/portable/hocon/Proxyfile), which writes in the same
world only the keys every generator is to render alike, nginx's own two files,
[`conformance/testdata/nginx.yaml`](../conformance/testdata/nginx.yaml) and
[`conformance/testdata/nginx-plain.yaml`](../conformance/testdata/nginx-plain.yaml), the same with no pattern route,
and HAProxy's own file, [`conformance/testdata/haproxy.yaml`](../conformance/testdata/haproxy.yaml).
The nginx entries, `nginx-1.30` and `nginx-1.31`, run on the two images pinned in `scripts/nginx-images`, one entry for
each line, and run nginx's two files and `examples/full`, which nginx refuses.
The HAProxy entries, `haproxy-3.2` and `haproxy-3.4`, run on the two images pinned in `scripts/haproxy-images`, one
entry for each line, and run HAProxy's own file and `examples/full` and `examples/portable`, which HAProxy refuses. Each
file is rendered for the entry's target with `proxyfile render --target <name>` through the program's own command line,
the path a user takes. The proxy then runs on the rendered file in its own image, among stub backends, and the same
requests get the answers the format promises.

`conformance/testdata/<proxy>.yaml` is a proxy's own file, and `<proxy>-<name>.yaml` another of its own: the world of `examples/full`, or of `examples/portable` for HAProxy, with only the keys that proxy's
generator writes in this version, so the rows whose keys it writes run on that proxy before its generator writes every
key. Envoy's entry runs it too, so its answers are Envoy's, measured as every other answer is. nginx's `nginx.yaml`
writes `tls` with `clients`, `client_address: connection`, `remove_from_clients`, `cors` and the lists of every
environment as `examples/full` does, but `remove_from_clients` names only `x-client-id`, because the generator
refuses a name with a `*` prefix; `nginx-plain.yaml` writes no `tls`, keeps `client_address` at `forwarded`, the
default with no `tls`, and removes `x-request-id` from clients, which nginx reads to keep a client's own request id
under `forwarded`.

A file that writes no `tls` is sent over plain HTTP: HTTP/1.1, and HTTP/2 with prior knowledge. A file with `tls` is
sent over TLS, with a client certificate the run's clients CA signed ([The world](#the-world)). Every request goes to
the proxy's one listener, and the client asks for the host the request names as its TLS server name, without the port,
as a browser does, and checks the server certificate against `localhost`, the name it carries; an empty host, a host
that is an IP address, and one that holds a character other than a letter, a digit, a dot or a hyphen ask for
`localhost`. A row may name a server name of its own. nginx answers `421` to a request whose `Host` is another name than
its TLS server name when it asks for client certificates, so a client that asked for one name whatever the host a row
sends would read that refusal in every row that names another host.

Each row names the core keys its answer depends on and runs on each file that writes them all, counting the keys of the
origins file a proxyfile names and of the methods its `rpc.proto` reads; a row that names none runs on every file.
Before any container starts, the battery fails when a row names a key that is not a core key of the format, when a row
runs on no file of Envoy's entry, where its answers were measured, when a row holds an answer for a file label no entry
carries or for a file the row does not run on, when a row that runs on a file of Envoy's runs on none of an entry's
files while that entry's target refuses none of the row's keys, and when a key Envoy's rows exercise is exercised on
none of an entry's files while that entry's target does not refuse it. A row checks only what every proxy must answer:
the status, which service and which of its backends answered and what it received (port, protocol, method, path, host
and headers), the response headers that matter, `grpc-status` and a time window. On one file a row's answer is the same
for every target unless the target declares a difference that changes it (below); where two files give different
answers, as the rate limit's count and a route only one file writes do, the row holds each file's answer. Every answer
was measured on Envoy 1.40.2, and [`conformance/scenarios_test.go`](../conformance/scenarios_test.go) notes beside each
scenario what Envoy also answers that no row checks. The scenarios run in this order, one request at a time but for
the cost scenario's flood: in Envoy's entry 290 rows on `full`, 284 on `portable`, 250 on `nginx-file` and 195 on
`nginx-plain`, nginx's own files, and 250 on `haproxy-file`, HAProxy's own; in each nginx entry the same 250 on
`nginx-file` and 195 on `nginx-plain`; and in each HAProxy entry the same 250 on `haproxy-file`:

| Scenario | Keys | What its rows check |
|---|---|---|
| routing | `routes[].pattern`, `routes[].path`, `routes[].method`, `routes[].rewrite`, `routes[].prefix`, `routes[].to`, `services.<service name>.http`, `services.<service name>.addresses`, `services.<service name>.address`, `services.<service name>.host_header`, `dns.resolver`, `origins.<environment name>.hosts`, `client_address`, `tls.certificate` | A pattern route, and a path its pattern misses going to the prefix route; a path and method route with its rewrite, and another method going to the prefix route; a service whose address the resolver gives; the prefix route over HTTP/1.1 and over HTTP/2, where the service still hears HTTP/1.1; the host each service hears, `host_header`'s value for `accounts`; a host of another environment; `404` for a host no environment names; a service at its one address; a path compared by letter case, `/API/webhooks` going to the prefix route; the path `/_pf_auth`, written and with its underscore escaped, routed as any other; an escaped newline, `%0A`, at the end of a path and inside one, matched by the patterns as written and passed on as sent; an upgrade other than WebSocket answered `403`, and an `h2c` upgrade served as a plain request whose service receives no `upgrade` or `http2-settings`, which HAProxy answers `400`, a declared difference; the scheme the request came in on, in `x-forwarded-proto`; with `client_address = connection`, a client's own `x-forwarded-proto` replaced by the connection's scheme, and its own `x-envoy-external-address` and `x-request-id` by values the proxy writes; each of 32 requests that carry no `x-request-id` gets one the proxy makes, a version 4 UUID in lower case, 8-4-4-4-12 hexadecimal digits with the third group starting with `4` and the fourth with `8`, `9`, `a` or `b`, and the answer carries it too; `502` for a service that refuses connections; a service's `X-Accel-Redirect` passed on to the client, not followed; a service's `Keep-Alive` not passed on to the client; the headers the Envoy output removes (`proxy-connection`, `x-forwarded-client-cert` and the `x-envoy-` headers that steer an Envoy) never reach the service; the host of an absolute request line, not the `Host` header, is the host the service hears, where HAProxy answers every absolute target `400`, a declared difference; an `Upgrade` that `Connection` does not name is removed and the request served; header names holding `_` or `.` reach the service; `TE: gzip, trailers` reaches it as `trailers`; `404` for a host with a dot at its end; `400` for a method Envoy's parser does not know; a pattern's `{,3}` read as text, as RE2 reads it; a long path `((a|a)*c|a+b)` takes, which PCRE2 gives up on with the pattern as written, reaching its route; and a long path that makes PCRE2 give up on `(a|aa)+`, which goes on to the prefix route on Envoy and is answered `400` by nginx, a declared difference. A header name outside RFC 9110's token characters, `x@at`, `x(p)` or one holding the byte 0xFF, is answered `400` over HTTP/1.1 and refused over HTTP/2, where Envoy closes the connection and nginx answers `400`, a declared difference; the same requests, written by hand, with a name of token characters reach the service, and `x-ok`, `x_und` and `x.dot` reach it over both. A header value holding a control byte other than a tab is answered `400` over HTTP/1.1 and refused over HTTP/2, where nginx answers `400` and HAProxy passes it on to the service, whose own server refuses it, a declared difference; a value holding a tab and UTF-8 reaches the service over both. A header written `x_forwarded_proto`, a name nginx reads as its own `X-Forwarded-Proto`, reaches the service as sent through Envoy and is answered `400` by nginx, a declared difference; `x_forwarded_for`, which nginx does not read as its own `X-Forwarded-For`, and a header named `constructor` or `__proto__`, over HTTP/1.1 and HTTP/2, reach the service as sent through both. Two `Accept` lines reach the service as one line through Envoy and as two lines through nginx, the same message, and two `Content-Type`, `User-Agent`, `Referer` or `If-Match` lines are joined and served by Envoy and answered `400` by nginx, each a declared difference on nginx's request-line row. With two or three spaces around an absolute target, the target's host is the host the service hears whatever the `Host` header says, and one that ends with a dot gets `404`; a tab after the method is served by Envoy and answered `400` by nginx, a declared difference; an absolute target whose host a query follows is refused. `TE: Trailers` reaches the service as `trailers` when `Connection` names `te` and is removed when it does not, and `Connection: close,` is served. An absolute target with an empty port, `http://localhost:/products/1`, is answered `400`. A chunked body's trailer reaches the service through no proxy, which receives the body; a trailer whose name holds `@` is answered `400` by Envoy and let through by nginx, which serves the body without it, and so is a trailer whose value holds a control byte, which HAProxy lets through too, over HTTP/1.1 and HTTP/2, and over HTTP/2 a trailer is served by Envoy and ends the connection on nginx, each a declared difference. A header line with no colon is answered `400` by Envoy and reaches the service through nginx as a header with an empty value, one of nginx's less strict ways. A header line folded onto the next, with a space or a tab, is served by Envoy and answered `400` by nginx and HAProxy, and a `Host` whose port is `99999` or `+1` is served by Envoy and answered `400` by nginx, declared differences. 100 header lines reach the service and 101 are refused (on `full` and `portable`, whose `limits.request_headers` admits 150, they reach it), and 96 header fields over HTTP/2 reach it; HTTP/1.0 is answered `426`, and so are `HTTP/1.2`, `HTTP/1.9`, `HTTP/0.9` with headers, where nginx answers `400`, and `HTTP/2.0` on an HTTP/1 connection, where nginx answers `505`, and a request line with no version, where HAProxy answers `400` and nginx closes the connection, each a declared difference; `HTTP/1.10` and `HTTP/1.01` are answered `400`, and a method in lower case `400`; a header a `Connection` token names does not reach the service, where nginx and HAProxy answer `400`, a declared difference, and neither does `Keep-Alive`; `HTTP2-Settings` on a request that is no upgrade reaches the service; ten `Connection` tokens are answered `400` and nine served, a token and ten empty elements are served, where HAProxy answers `400`, a token and nine elements of white space are answered `400`, and ten tokens `close` are answered `400`, where HAProxy serves them, each a declared difference; a WebSocket over HTTP/2 is refused; over HTTP/2 an `:authority` with an empty port, `localhost:`, reaches the service as sent, where HAProxy sends `localhost`; two `Host` lines of one host are answered `404`, where HAProxy serves the request and nginx answers `400`; two `Content-Length` lines of one value, and `Content-Length` beside `Transfer-Encoding: chunked`, are answered `400`, and `Transfer-Encoding: gzip, chunked` `501`, where HAProxy serves the first two and answers the third `422`; a `POST` with no body reaches the service with `content-length: 0`, which neither nginx nor HAProxy sends; a WebSocket upgrade the service answers `200` without switching reaches the client with no body, where nginx and HAProxy pass the service's; a header of 20,000 bytes and a path of 20,000 bytes reach the service, where HAProxy answers `431` and `414` and nginx `400` and `414`; and over HTTP/2 a path of 7,000 bytes above 0x7F reaches the service, which HAProxy refuses and nginx answers `400` on `nginx-file`, each a declared difference; and a service that closes the connection of the first request a client sends on its connection before it answers is answered `502`, and so is one that closes, before its answer, a connection to `catalog` the proxy kept from a request on the client's same connection, over HTTP/1.1 and HTTP/2, the request reaching it once, where HAProxy sent it again before each request had a connection of its own. On `haproxy-file`, whose `test` environment has the hosts `.dot.example.com`, `dot.example.com.`, `-f`, `-i` and `-n`, a request to each reaches the service with that host, and on every other file it gets `404`. With `client_address = connection`, a client's `X-Forwarded-For` reaches the service on one line with the peer's address, and so do two lines of it, which Envoy joins, where nginx answers `400`, a declared difference, and the `x-envoy-` headers only a proxy in front may send (`x-envoy-decorator-operation`, `x-envoy-downstream-service-cluster`, `x-envoy-downstream-service-node`, `x-envoy-original-host`, `x-envoy-original-path`) do not reach it. One hundred and twenty-one rows: one hundred and twenty on `haproxy-file` and on `nginx-file`, which write no `dns.resolver`; and one hundred and eight on `nginx-plain`, which writes no pattern route and no `client_address` either. |
| mutual TLS | `tls.certificate`, `tls.key`, `tls.clients.ca`, `origins.<environment name>.hosts` | A client with no certificate, one whose certificate another CA signed, and one whose certificate the clients CA signed and whose validity ended reach no service: the handshake fails, or the proxy refuses it, where nginx completes the handshake and answers an empty `400`, the difference nginx's row of `tls.clients.ca` names. A request whose TLS server name is one host of an environment and whose `Host` is another host of the same environment is served by Envoy and answered with an empty `421` by nginx, which asks for client certificates, a declared difference. The other CA carries the clients CA's name, so a proxy that matched the issuer by name would let it through. Every other row sends a client certificate the clients CA signed. A client that offers only TLS 1.1, a TLS 1.2 client that offers only a CBC suite, and one that offers only the group P-384 are refused in the handshake, and one that offers only the group X25519MLKEM768 is served. Eight rows, on every file but `nginx-plain`, which writes no `tls`. |
| CORS | `cors.allow_methods`, `cors.allow_headers`, `cors.expose_headers`, `cors.max_age`, `origins.<environment name>.hosts`, `origins.<environment name>.origins`, `origins.<environment name>.patterns` | A preflight from a listed origin and one from an origin a pattern covers answer `200` with `access-control-allow-origin` and `access-control-max-age`; a preflight and a request from another origin answer `403`; a request from a listed origin reaches the service with its `origin` and is answered with `access-control-allow-origin` and `access-control-expose-headers`. The preflight from a listed origin and the request from another origin carry a token the check denies and one the token filter refuses, each answered `401` alone, so on a file that writes the check their `200` and `403` show that the origin is decided before the token and the check. An origin on the environment's own host that neither list holds, `https://api.example.org` to `api.example.org`, is admitted without `access-control-allow-origin`: its request reaches the service and its preflight answers `200`. With no list, an origin on one of the environment's hosts is admitted and every other refused: a request with no `Origin`, and one from `http://localhost` over `http` or `https`, with a port, or from the environment's other host `http://127.0.0.1` reaches the service, and a preflight from `http://localhost` is answered `200` by the proxy; an origin on another environment's host, the host in capitals, with a trailing dot or with other characters for its dots, `null`, a present but empty `Origin`, a preflight from another origin and a WebSocket upgrade from one answer `403`, where HAProxy's parser answers that upgrade, which carries no `Sec-WebSocket-Key`, `400`, a declared difference. An empty `Origin` over HTTP/2 is answered `403` too, and an empty `Origin` line before or after the origin of the host, or a tab before that origin, changes nothing: the service receives the origin as one line. A preflight from a listed origin with no token is answered with the method and the headers it asks for; one from a listed origin in capitals is admitted with the origin as sent, and one from a pattern's origin in capitals is refused. The `Origin` header is read as one line: two `Origin` lines a pattern covers when joined are admitted with the joined line in `access-control-allow-origin`, where nginx answers `403`, a declared difference, two whose first is listed, two of the request's own origin, and one line holding a listed origin after a comma are refused, and an empty `Origin` line after a listed one is left out; a preflight that asks on two lines for two methods or for headers is answered with both, joined with a comma, and by nginx with a comma and a space, a declared difference. A listed origin with a trailing space is admitted, and the service receives it without the space. A service that is down, asked from a listed origin, is answered `502` with `access-control-allow-origin` and `access-control-expose-headers`, and a request from a listed origin to a host no environment names is answered `404` with no `access-control-allow-origin`. A refused path, a preflight to it, an upgrade other than WebSocket, a request with a NUL byte in a header from a listed origin and one with `Content-Length` beside `Transfer-Encoding: chunked` from it are answered with no `access-control-allow-origin`, where HAProxy serves the last with it, a declared difference, and a header line over 8 KiB from one is served by Envoy with it and answered `400` by nginx without it, a declared difference. Forty-six rows, all on `haproxy-file` and `nginx-file`, and nineteen on `nginx-plain`, which writes no list of origins and no `cors`. |
| auth | `auth.ask`, `auth.only_with_header`, `auth.send`, `auth.accept`, `auth.send_request`, `auth.when_unavailable`, `routes[].auth`, `remove_from_clients`, `auth.jwt.claims` | A request with no `authorization` header reaches the service, which it could not if the check were asked; when the check allows, the service receives the check's headers `auth.accept` admits and not another, and what the check was told (the method, the path with its query, `x-country`), never the `x-forwarded-uri` and `x-forwarded-method` the client sent; the check's `401` and `403` reach the client, without the `x-envoy-upstream-service-time` Envoy's client for the check adds; a check that answers `503` lets the request through, as `when_unavailable = allow` says; the mark of a request the check could not answer reaches the service, and a client's own copy never does, whether the check allows or is not asked; a route with `auth = off` is not checked; a client's own copies of the headers `remove_from_clients` names, of the check's headers and question, and of the headers `auth.jwt.claims` sets never reach the service. Twelve rows, eleven on `portable`, which writes no `auth.jwt.claims`, and the two rows of `remove_from_clients` also on `haproxy-file`, `nginx-file` and `nginx-plain`, which write it and no check. A header `remove_from_clients` names does not reach the service when a client sends it on two lines, one with its name in capitals, and a client's own `x-request-id` is replaced by another when `remove_from_clients` names it. |
| timeout | `auth.timeout`, `auth.when_unavailable` | The check holds the request for 3 s, past `auth.timeout`'s 1500 ms, and the request reaches the service with the mark of a request the check could not answer, after at least 1.45 s and in under 2.95 s, 50 ms inside the two times because the row reads a different clock from the proxy's and the check's (below). One row. |
| retry | `routes[].retries`, `routes[].rpc.retries` | A request that reached the service is never tried again: a first try the service closes before any answer gives `502`, a gRPC call whose first try is reset gives `grpc-status` `14`, and a `503` from the service is passed on from its first try. A request whose connection a backend refused is tried again on the other: with one of the two shop backends refusing connections, 40 requests to the shop route and 40 gRPC calls to shop are each answered by the other backend. Five rows, which run on `full`: `portable` writes no retries. |
| gRPC | `routes[].rpc`, `routes[].rpc.to`, `routes[].rpc.to.<package pattern>`, `routes[].rpc.to.<package pattern>.service`, `rpc.methods`, `rpc.methods[].service`, `rpc.methods[].method`, `rpc.methods[].action`, `rpc.methods[].reports`, `services.<service name>.grpc`, `auth.ask` | A method of a package mapped to `shop` reaches shop's gRPC port over HTTP/2 and answers `grpc-status` `0`; a method of any other package reaches `accounts`; the check is told a method's action and reports, and not an action the client sent, and the service receives neither; a gRPC service's `X-Accel-Redirect` is passed on, not followed; a call through a `..` segment reaches the method's path. Six rows, four on `haproxy-file`, `nginx-file` and `nginx-plain`, which write no `auth.ask`. |
| path normalisation | none: it is written for every file ([Written with no key](portability.md#written-with-no-key)) | `..`, `.`, `//`, `%2F` and `%2e%2e` in a path are resolved before a route is picked, and the service hears the normalised path; so are `..` followed by an escaped slash (`..%2f`), by a backslash written as `%5C`, and by a written backslash; `..;` and `.;`, written with dots, resolved as `..` and `.`, which nginx and HAProxy answer `400`, a declared difference; an exact path reached through `..`. An invalid escape, `%zz`, is passed on to the service, whose own `400` answers it, and nginx answers it `400` itself, a declared difference. An escaped `+` and an empty query reach the service as sent; an empty query after a path that needs normalising reaches it after the normalised path, and nginx answers it `400`, a declared difference. A path that still holds a dot segment with a `;` parameter after normalisation answers `400` and reaches no service, the `;` written or escaped once: `/catalog/%2e%2e;/products/1`; `..%3B` and `..%3b`; `.%3B` and `%2e%3b`; dots written escaped, in either case of hex, before an escaped `;`; an escaped `;` with a value or twice over; on the last segment and on the first; between backslashes, written or as `%5C`; and before an escaped slash. These reach the service as sent: a `;` parameter on an ordinary segment, on a name and on a name that starts with a dot; a `;` and an escaped `;` after three dots; an escaped `;` after a name that ends in two dots and inside a name; a `;` escaped twice after `..`; a query that holds `/..;/` or `/..%3B/`; and an escaped `?` after `..`. A dot segment with an escaped `;` that a `..` after it removes reaches the service resolved, and nginx answers it `400`, a declared difference. Forty-two rows. |
| the path as the client wrote it | none: it is written for every file ([Written with no key](portability.md#written-with-no-key)) | A path with nothing to normalise, or with only an escape Envoy keeps, is matched and reaches the service as written: `/api/x%3Bv=1/admin`, `/api/a%20b/admin`, `/u/%40bob`, `/l/a%20b` and `/api/a%7cb`, in the case of hex written, where nginx's and HAProxy's own files have patterns that would take the decoded character; `/x%C3%A9` is not taken by the pattern `^/x..$`; an empty query on such a path reaches the service as sent. A path that needs normalising and keeps no escape is matched and sent normalised. A raw `\|` reaches the service as `%7C`. nginx answers `400`, each a declared difference, to a path that needs normalising and keeps an escape, `/api/a%2fb%3bc`; to a byte Envoy escapes beside an escape; to a byte Envoy escapes on `nginx-file`, which has pattern routes; to an escape and to an empty query on a route with a `rewrite`; and to an empty query after a byte Envoy escapes. A `#` in the target is answered `400` over HTTP/1.1 and HTTP/2, where HAProxy resets the HTTP/2 stream, a declared difference. An escaped NUL, `%00`, in the path is answered `400`, and a byte above 0x7F sent raw over HTTP/2 reaches the service escaped, `%C3%A9` for `é`, which nginx answers `400` on `nginx-file`, a declared difference. Twenty rows. |
| an auth-off prefix route | `routes[].prefix`, `routes[].auth`, `auth.ask`, `auth.only_with_header`, `auth.accept`, `auth.send_request` | On `portable`, which sends `/public/` to `shop` with `auth = off` before its other prefix routes: a path under it reaches the service unchecked with a token the check denies, and so does one with a repeated slash, which the service hears merged; a client's own copy of the mark of a request the check could not answer does not reach the service there either. A path that `..`, `%2e%2e`, `..%2f`, `..%5C` or `..\` takes out of `/public/` leaves the route: with a token the check denies it gets the check's `401`, and with a token the check allows it reaches the service, and the check is told the path the service hears, the normalised one; without the normalisation each would stay on the route and reach the service unchecked. `..;` with a token the check denies leaves the route too and gets the check's `401`. A dot segment with a `;` parameter is answered `400` and reaches no service with no token and with a token the check allows, the `;` written after `%2e%2e` or escaped as `%3B` after `..`. On `full`, which has no such route, the first and the third are answered `401` by the check its shop route asks. Eighteen rows. |
| rate limit | `routes[].rate_limit`, `routes[].rate_limit.requests`, `routes[].rate_limit.per`, `routes[].rate_limit.burst`, `routes[].rate_limit.by`, `auth.ask` | After 2.5 s with no request to the route, 60 requests to `/catalog/items` back to back: the first 30 on `full` (20 a second and a burst of 10) and the first 20 on `portable` (20 a second and no burst) answer `200`, and at least one of the rest `429`; then, of 10 requests over the limit that carry a token the check denies and one the token filter refuses, at least one is answered `429`, not `401`, so the limit is applied before the token and the check; another route is not limited. Three rows. |
| cost | `routes[].pattern`, `routes[].prefix`, `origins.<environment name>.patterns` | A path of 14,000 bytes that 24 pattern routes read, sent with an origin a pattern covers, reaches the prefix route's service with `access-control-allow-origin` within 500 ms, and an `Origin` of 8,000 bytes that 64 patterns of two `*` read is answered `403` within 500 ms; the request after each is served, which only a proxy still running answers. Each stopped HAProxy before its patterns ran in steps and its origin patterns were written so they cannot backtrack ([HAProxy](haproxy.md#what-a-pattern-route-costs)). The bound leaves a slower runner twenty times the 24 ms HAProxy's patterns may take at most for one request alone. On `nginx-file` the long path is answered `414`, nginx reading a request line of at most 8 KiB, the difference nginx's request-line row names, and a path of 8,177 bytes, the longest nginx reads in a `GET`, reaches the service within 500 ms; an `Origin` of 6,420 bytes that the last of the 64 patterns takes reaches the service with `access-control-allow-origin`, and one of 6,321 bytes that the 64 read is answered `403`, each within 500 ms on `haproxy-file` and `nginx-file`, the files that name an environment with its host, rows of their own because nginx's first form of the patterns refused the first and held a worker for 17 to 30 s with the second ([nginx](nginx.md#what-a-pattern-route-and-an-origin-pattern-cost)). On `full` and `portable`, which name no environment with the long `Origin`'s host, each long `Origin` is answered `404`. Then 3,000 requests over 24 connections at once, each to a path of `/k01/` to `/k40/` and 1,000 random `a` and `b`, which 40 pattern routes of `haproxy-file` read, are each served by the prefix route's service, and the proxy's memory, read through Docker's API, grows by less than 96 MB and 4 MB for each CPU Docker gives a container, 144 MB with 12: HAProxy runs a thread for each CPU, and each keeps the states of its own patterns, where Envoy's RE2 keeps a cache for each pattern, so Envoy grew by about 75 MB with 12 workers and with 4. On `haproxy-file` with 12 CPUs, HAProxy grew by about 540 MB on the code before the states of a thread shared one budget, by about 235 MB with a budget of about 8 MB a thread, and by about 60 MB with the script's, of about 1 MB; the first two fail the row. It runs on every file that writes pattern and prefix routes, which `haproxy-file` and `nginx-file`, the files with the 40 routes, share with `full` and `portable`. The row logs the memory it read on every run. Eight rows, every one on `nginx-file`. |

The stub reports each header the service received with its lines joined by a line break, which no value holds, so a row
tells one line holding a comma from two lines: Envoy joins the lines of some headers into one before the service reads
them, and a service that reads only a header's first line reads another value when a proxy passes both. A row checks
that a header came on one line with `oneLine`, where its value cannot be known, as when it holds the peer's address.

A header the proxy writes in place of the client's, with a value no row can know before the request, such as a new
`x-request-id`, is checked by `replaced`: the row names the header with the value the client sent, and the service must
receive it with another value. A header the service does not receive fails the row, as one that reaches it with the
client's value does.

Where a target declares a difference that changes a row's answer, the row holds that target's own literal under `by`,
named by the declaration's row: the row of the support table in [Portability](portability.md) whose partial cell says
the difference. That literal was measured on the target's own images. The battery fails an own literal whose row the
target does not declare partial, or that no run of that target reads.

A target that refuses a key a file writes cannot render that file, so the example commits, instead of the target's
configuration, the lines render prints: `<target>.refused` beside the proxyfile. For such a file the battery starts no
proxy: it passes when render exits 1 and prints exactly those lines, and a file render writes although its folder
commits a refusal fails. In this version nginx and HAProxy refuse every key their generators do not write yet, so
`examples/grpc`, `examples/minimal` and `examples/tls` commit `nginx.conf` with `proxyfile.js`, and every other example
commits `nginx.refused`, and every example but `examples/minimal`, `examples/grpc` and `examples/tls` commits
`haproxy.refused`.

## The world

The battery starts every address the files name on one Docker network, so each rendered file runs unchanged:

| Container | Address | What it is |
|---|---|---|
| `proxyfile-conformance-proxy` | `10.0.0.10` | The proxy, with the rendered files read-only at `/config`, the TLS files at `/etc/gateway/tls` and the token CA at `/etc/gateway/jwt`. Its listener, port 10000, is published on `127.0.0.1` at a port Docker picks, so a run never meets a port another program holds. |
| `proxyfile-conformance-shop-1`, `proxyfile-conformance-shop-2` | `10.0.1.11`, `10.0.1.12` | `shop`: HTTP on 8080 and gRPC on 8081. |
| `proxyfile-conformance-catalog` | `10.0.2.21` | `catalog` on 8090, an address the examples leave to the resolver and `conformance/testdata/nginx.yaml` writes itself. |
| `proxyfile-conformance-resolver` | `10.0.0.2`, port 53 | The resolver: `catalog.internal.example` is `10.0.2.21`, and any other name is NXDOMAIN. |
| `proxyfile-conformance-accounts` | `127.0.0.1:7070` in the proxy's own network namespace | `accounts`, HTTP/1.1 and gRPC on one port, and the auth check `auth.ask` names. |
| none | `10.0.3.40:4317` | The tracing collector `examples/full` names, left unreachable: no request waits for it. |

The network is `proxyfile-conformance` on `10.0.0.0/22`, the smallest block that holds every one of these addresses.
Each file a run renders gets a world of its own: the battery removes the first file's before it starts the second's.

One stub program, [`conformance/stub`](../conformance/stub), plays every backend, the auth check and the resolver. A
backend answers each request with what it received: its service name, port, protocol, method, path, host and headers, so
a row can say which service answered and what reached it. The check decides by the value of the `authorization` header
(`Bearer allow`, `Bearer deny-401`, `Bearer deny-403`, `Bearer status-503` or `Bearer hang`), so each row chooses the
check's answer; any other token, or none, is denied with `401`, so a row that wants the check not to be asked fails when
the proxy asks it.
A backend answers with `X-Accel-Redirect` set to the value of the request's `x-stub-accel-redirect`, on HTTP and gRPC,
so a row can check that the proxy passes the header on instead of following it, and with `Keep-Alive` set to the value
of `x-stub-keep-alive`, so a row can check that the proxy removes it. A request the stub cannot read gets
the stub's own answer, the plain-text status line of Go's `net/http`, and no description of the request: a row that
wants that answer says `unreadable`, and a row that wants no service fails on it.
The backends share the volume `proxyfile-conformance-state`: a request the stub closes on its first
try is answered by whichever backend a second try reaches, so a retry would show. A backend refuses connections, as
one that is down does, while a row needs it to: the battery sends its container `SIGUSR1`, which closes every port
and connection of the stub, and waits until a connection made inside the container is refused; after the row,
`SIGUSR2` opens the ports again, and the battery waits until each answers. The stub is built with Go for the platform
the Docker server runs and copied alone into the image `proxyfile-conformance-stub`.

Before the first request, the stub in `accounts`, which shares the proxy's network namespace, fetches the target's
ready URL every 100 ms until it answers 200, for 30 s at most: the proxy's admin listener answers only on `127.0.0.1`,
and Envoy's image has no shell.

Each run makes throwaway TLS files: a self-signed server certificate for `localhost` and `127.0.0.1` with its key, a
clients CA, and a copy of the server certificate as the CA bundle `examples/full`'s `auth.jwt.ca` names. They are
written to the folder `proxyfile-conformance` in the system's temporary folder (`$TMPDIR`, or `/tmp`), with the
rendered file and the stub's build, and the folder is the user's alone; the proxy reads its `tls` and `jwt` folders
through their own mounts. The three client certificates, one the clients CA signs, one a second CA of the same name
signs, and one the clients CA signed whose validity ended a day before the run, stay in memory with their keys, so the proxy never sees them and no run writes them to disk.

## What it does not prove

- Answers each proxy writes in its own words. No row checks a local reply's body, or that an answer carries a header a
  proxy names after itself, which [portability](portability.md) classes as that proxy's own: measured on Envoy 1.40.2,
  the `403` body `Invalid CORS request`, the empty `502`, the `429` body `local_rate_limited`, and the `403` Envoy
  answers when the check answers `204`. A row may check such a header absent, which holds for every proxy, as the two
  denial rows do. The mark of a request the check could not answer, `x-envoy-auth-failure-mode-allowed`, is checked
  although it carries Envoy's name, because every proxy writes that name.
- A retry of a service with one backend. A row would need the backend to refuse exactly one connection and accept the
  next, which the stub cannot time. Measured on Envoy 1.40.2 instead, with a firewall rule in the backend's own network
  namespace that refused one connection: the retry went to the same backend and was answered `200`, five times of
  five.
- Keys no scenario names. The rendered file carries them, and the proxy loads it, but no row checks what they do:
  among others `auth.jwt` but its `claims`, `limits`, `timeouts`, `log`, `tracing` and `services.<service name>.health`.
  No row sends a valid token, so of `auth.jwt.claims` only the removal of a client's own copy is checked. The table
  above names every key a scenario exercises.
- What the check is told of the host. The stub's check reports the method and the path the proxy told it, not
  `x-forwarded-host`.

## Running it

It needs Docker and Go. From the repository root:

```sh
go test -tags conformance -count=1 ./conformance/...
```

The battery's tests are behind the build tag `conformance`, so `go test ./...` without it starts no container. Each
row is a subtest, `TestBattery/<entry>/<file>/<scenario>/<row>` with `_` for each space, which `-v` lists, so
`-run 'TestBattery/envoy-1.40/portable/routing'` runs one scenario on one file. A row that fails prints each way its
answer differs, for example `service: got "shop", want "catalog"`, and the proxy's last 50 log lines follow.

The first run pulls the proxy's image by the digest in [`scripts/envoy-image`](../scripts/envoy-image), the file
[`scripts/validate-envoy.sh`](../scripts/validate-envoy.sh) also reads, so an Envoy upgrade changes one line for both;
it pulls nginx's two images by the digests in `scripts/nginx-images`, which
[`scripts/validate-nginx.sh`](../scripts/validate-nginx.sh) reads in the same way, and HAProxy's two by the digests in
`scripts/haproxy-images`, which HAProxy's validator, `scripts/validate-haproxy.sh`, reads.
With the images already pulled and a warm Go build cache, a run of every entry's files, eleven worlds, took 74.2 to 75.8
s in 10 of 10 runs in a row on one arm64 Mac, of which each HAProxy entry 5.2 to 5.7 s, and 10 s Envoy's two rows of a
control byte in a header or trailer value over HTTP/2 on its five files: Envoy closes the connection 1.00 to 1.02 s
after it refuses one. The cost scenario's flood runs eight times in a run, four in Envoy's entry and one in each other,
and took 0.15 to 0.22 s on Envoy's and nginx's files and 2.5 to 2.8 s on HAProxy's, each reading the proxy's memory
twice through Docker's API in one sample.
A series of 10 that read it with `docker stats`, which waits a second for two samples, took 86.8 to 94.4 s; the series
before the flood, of 30 runs, 55.1 to 61.8 s, one before it, with fewer rows, 48.2 to 50.5 s, and one of five worlds
19.1 to 19.9 s in 30 of 30. A world is
sometimes slow to start: in a series before those one run of 30 took 49 s, and in one run before the latest series
Envoy's world of `haproxy-file` took 36 s, where it takes 8.9 to 9.1 s.
Besides the rows that bound the time of one answer, the cost scenario's within 500 ms and nginx's `400` to a path PCRE2
gives up on within 50 ms, each of which took at most 0.02 s in those 10 runs, two rows count on time. The rate limit's
60 requests must take under 25 ms each on average on `full` and under 33 ms on `portable`, because the bucket refills
one request every 50 ms and 30 refills on `full`, 40 on `portable`, would admit every request after the first 30 or 20;
over the 10 runs of eleven worlds the 60 took 0.04 to 0.05 s on `full` and 0.04 s on `portable`. The timeout
row's answer must come between 1.45 s and 2.95 s, and took 1.50 to 1.51 s on each file in those 10 runs. One row counts
on memory: in those runs the flood grew HAProxy 3.2.26 by 35 to 36 MiB and 3.4.7 by 34 to 36 MiB, against the bound of
144 MiB on that Mac's 12 CPUs, Envoy by at most 75 MiB and nginx by 18 to 29 MiB. The timeout row times the request on
the client's clock, while the proxy times `auth.timeout` on its own, so its bounds keep 50 ms from the 1.5 s timeout and
from the check's answer at 3 s. Envoy counts the timeout from the time its event loop last read, from a clock that moves
in 1 ms steps under Docker Desktop's kernel, and in another series 2 of 30 runs on that Mac saw the answer at 1.4996 s
and 1.4998 s, under 1 ms short of 1.5 s. A repeated row that fails says how long its requests took.

The battery removes its own leftovers when it starts and everything it made when it ends but its lock (below): the
containers named `proxyfile-conformance-<role>`, the network `proxyfile-conformance`, the volume
`proxyfile-conformance-state`, the image `proxyfile-conformance-stub` and the folder `proxyfile-conformance` in the
temporary folder. It touches no other Docker object. A run stopped before its end, by `go test`'s `-timeout` or an
interrupt, leaves all of them: the containers keep running, the proxy still published on `127.0.0.1`, and the folder
keeps the run's server key, made for that run alone and trusted by nothing else. The next run removes them when it
starts; to remove them at once:

```sh
docker rm -f proxyfile-conformance-accounts proxyfile-conformance-proxy proxyfile-conformance-shop-1 \
  proxyfile-conformance-shop-2 proxyfile-conformance-catalog proxyfile-conformance-resolver
docker network rm proxyfile-conformance
docker volume rm proxyfile-conformance-state
docker image rm proxyfile-conformance-stub
rm -rf "${TMPDIR:-/tmp}/proxyfile-conformance"
```

The names are fixed, so a run takes a lock, an exclusive `flock` on `/tmp/proxyfile-conformance.lock`, before its first
file touches Docker, and gives it back once, after it removed what it made for its last file. The path is the same
whatever `TMPDIR` is, so two runs on one machine take turns, from two checkouts, two users or two jobs. A run that finds
the lock held logs that it waits, and waits once: 5 minutes at most, and less when `go test`'s `-timeout` would leave
under 2 minutes for its worlds after the wait, because that timeout ends the run without removing them; then it stops
and says another run holds the lock. A run of the checks alone, such as `-run 'TestBattery/none'`, takes no lock. The
file stays, so every run locks the same one. A killed run holds no lock, because the kernel gives it back, and the next
run removes the Docker objects the killed one left. The lock does not reach past the machine's own `/tmp`: runs that
share a Docker daemon from two machines, or from a container that uses the host's daemon, do not see each other's lock
and still remove each other's containers, as does a battery older than the lock. When another network already holds
addresses in `10.0.0.0/22`, the run stops with Docker's `Pool overlaps` answer and says which range it needs; it removes
no network it did not make, so free the range or run on another Docker host.

The check `Required` of the `Pre-merge` workflow runs `go vet -tags conformance ./conformance/...` and the battery after
the Envoy, nginx and HAProxy validations, whose pulls leave the images cached. The release workflow runs the battery
before it builds the release, so a release whose rendered configuration answers a scenario otherwise than the format
says is not published.

## Adding a proxy

A proxy joins in four places, and a row changes for it only where it declares a difference that changes the row's
answer (`by`, above):

1. Its generator in `render.Targets` in [`internal/render/render.go`](../internal/render/render.go): the `--target`
   name, the configuration's file name and the function. `render` and the command line read the targets there.
2. Its declaration of every core key and of the two behaviours written with no key, in a file of its own in
   [`internal/format`](../internal/format), listed in `format.Targets` in
   [`internal/format/support.go`](../internal/format/support.go). A key it does not write yet is declared refused, and
   a form of a key it refuses, by the key's value or for want of another key, is a `Refuses` entry of that key, which
   it declares partial.
3. `go test ./internal/render -run TestExamplesAreCurrent -update` writes each example's configuration for it, or
   `<proxy>.refused` where it refuses a key the example writes, and `go test ./internal/format -update` writes its
   column of the support table.
4. An entry per image, made in a file of its own, `conformance/targets_<proxy>_test.go`, by a function `targets` in
   [`conformance/targets_test.go`](../conformance/targets_test.go) calls: its label, the `--target` name, the files it
   runs, the header it adds to a request the check could not answer, its image pinned by digest, its arguments and
   the URL that answers 200 when it is ready. Its images are in `scripts/<proxy>-images`, one a line, tagged with its
   version and pinned by digest, read by its entries through `pinnedImages`, which labels each `<proxy>-<major>.<minor>`,
   and by its validator, `scripts/validate-<proxy>.sh`, which `Required` runs on every configuration the examples commit
   for it. The rendered files are mounted in `/config`, the
   configuration at `/config/<file name>`, and the ports 10000 and 9901 and the folders `/etc/gateway/tls` and `/etc/gateway/jwt` are
   the examples', the same for every target. Its files together must run each row Envoy's files run, unless its
   target refuses one of the row's keys, or the battery fails before any container starts.
