# Conformance

The conformance battery runs a proxy on the configuration proxyfile renders for it and checks that the proxy answers
requests as the proxyfile says. It is the Go package [`conformance`](../conformance), whose files are test files only,
so the released program never carries them. It runs each target of this version: Envoy on Envoy 1.40.2, and nginx on
nginx 1.30.6 and 1.31.8.

## What the battery proves

Each entry of the battery is a proxy image and the proxyfiles it runs. Envoy's entry, `envoy-1.40`, runs four:
[`examples/full/hocon/Proxyfile`](../examples/full/hocon/Proxyfile), which writes every key but the few its opening
comment names, [`examples/portable/hocon/Proxyfile`](../examples/portable/hocon/Proxyfile), which writes in the same
world only the keys every generator is to render alike, and nginx's own two files,
[`conformance/testdata/nginx.yaml`](../conformance/testdata/nginx.yaml) and
[`conformance/testdata/nginx-plain.yaml`](../conformance/testdata/nginx-plain.yaml), the same with no pattern route.
The nginx entries, `nginx-1.30` and `nginx-1.31`, run on the two images pinned in `scripts/nginx-images`, one entry for
each line, and run nginx's two files and `examples/full`, which nginx refuses. Each
file is rendered for the entry's target with `proxyfile render --target <name>` through the program's own command line,
the path a user takes. The proxy then runs on the rendered file in its own image, among stub backends, and the same
requests get the answers the format promises.

`conformance/testdata/<proxy>.yaml` is a proxy's own file, and `<proxy>-<name>.yaml` another of its own: the world of `examples/full` with only the keys that proxy's
generator writes in this version, so the rows whose keys it writes run on that proxy before its generator writes every
key. Envoy's entry runs it too, so its answers are Envoy's, measured as every other answer is.

A file that writes no `tls` is sent over plain HTTP: HTTP/1.1, and HTTP/2 with prior knowledge. A file with `tls` is
sent over TLS, with a client certificate the run's clients CA signed ([The world](#the-world)).

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
scenario what Envoy also answers that no row checks. The scenarios run in this order, one request at a time: in Envoy's
entry 182 rows on `full`, 176 on `portable`, 133 on `nginx-file` and 126 on `nginx-plain`, nginx's own files, and in
each nginx entry the same 133 on `nginx-file` and 126 on `nginx-plain`:

| Scenario | Keys | What its rows check |
|---|---|---|
| routing | `routes[].pattern`, `routes[].path`, `routes[].method`, `routes[].rewrite`, `routes[].prefix`, `routes[].to`, `services.<service name>.http`, `services.<service name>.addresses`, `services.<service name>.address`, `services.<service name>.host_header`, `dns.resolver`, `origins.<environment name>.hosts` | A pattern route, and a path its pattern misses going to the prefix route; a path and method route with its rewrite, and another method going to the prefix route; a service whose address the resolver gives; the prefix route over HTTP/1.1 and over HTTP/2, where the service still hears HTTP/1.1; the host each service hears, `host_header`'s value for `accounts`; a host of another environment; `404` for a host no environment names; a service at its one address; a path compared by letter case, `/API/webhooks` going to the prefix route; the path `/_pf_auth`, written and with its underscore escaped, routed as any other; an escaped newline, `%0A`, at the end of a path and inside one, matched by the patterns as written and passed on as sent; an upgrade other than WebSocket answered `403`, and an `h2c` upgrade served as a plain request whose service receives no `upgrade` or `http2-settings`; the scheme the request came in on, in `x-forwarded-proto`; `502` for a service that refuses connections; a service's `X-Accel-Redirect` passed on to the client, not followed; the headers the Envoy output removes (`proxy-connection`, `x-forwarded-client-cert` and the `x-envoy-` headers that steer an Envoy) never reach the service; the host of an absolute request line, not the `Host` header, is the host the service hears; an `Upgrade` that `Connection` does not name is removed and the request served; header names holding `_` or `.` reach the service; `TE: gzip, trailers` reaches it as `trailers`; `404` for a host with a dot at its end; `400` for a method Envoy's parser does not know; a pattern's `{,3}` read as text, as RE2 reads it; a long path `((a|a)*c|a+b)` takes, which PCRE2 gives up on with the pattern as written, reaching its route; and a long path that makes PCRE2 give up on `(a|aa)+`, which goes on to the prefix route on Envoy and is answered `400` by nginx, a declared difference. A header name outside RFC 9110's token characters, `x@at`, `x(p)` or one holding the byte 0xFF, is answered `400` over HTTP/1.1 and refused over HTTP/2, where Envoy closes the connection and nginx answers `400`, a declared difference; the same requests, written by hand, with a name of token characters reach the service, and `x-ok`, `x_und` and `x.dot` reach it over both. With two or three spaces around an absolute target, the target's host is the host the service hears whatever the `Host` header says, and one that ends with a dot gets `404`; a tab after the method is served by Envoy and answered `400` by nginx, a declared difference; an absolute target whose host a query follows is refused. `TE: Trailers` reaches the service as `trailers` when `Connection` names `te` and is removed when it does not, and `Connection: close,` is served. An absolute target with an empty port, `http://localhost:/products/1`, is answered `400`. A chunked body's trailer reaches the service through no proxy, which receives the body; a trailer whose name holds `@` is answered `400` by Envoy and let through by nginx, which serves the body without it, and over HTTP/2 a trailer is served by Envoy and ends the connection on nginx, each a declared difference. A header line folded onto the next, with a space or a tab, and a `Host` whose port is `99999` or `+1` are served by Envoy and answered `400` by nginx, declared differences. Fifty-six rows, fifty-five on `nginx-file`, which writes no `dns.resolver`, and forty-eight on `nginx-plain`, which writes no pattern route either. |
| mutual TLS | `tls.certificate`, `tls.key`, `tls.clients.ca` | A client with no certificate, and one whose certificate another CA signed, reach no service: the handshake fails, or the proxy refuses it. The other CA carries the clients CA's name, so a proxy that matched the issuer by name would let it through. Every other row sends a client certificate the clients CA signed. Two rows. |
| CORS | `cors.allow_methods`, `cors.allow_headers`, `cors.expose_headers`, `cors.max_age`, `origins.<environment name>.hosts`, `origins.<environment name>.origins`, `origins.<environment name>.patterns`, `auth.ask` | A preflight from a listed origin and one from an origin a pattern covers answer `200` with `access-control-allow-origin` and `access-control-max-age`; a preflight and a request from another origin answer `403`; a request from a listed origin reaches the service with its `origin` and is answered with `access-control-allow-origin` and `access-control-expose-headers`. The preflight from a listed origin and the request from another origin carry a token the check denies and one the token filter refuses, each answered `401` alone, so their `200` and `403` show that the origin is decided before the token and the check. An origin on the environment's own host that neither list holds, `https://api.example.org` to `api.example.org`, is admitted without `access-control-allow-origin`: its request reaches the service and its preflight answers `200`. With no list, an origin on one of the environment's hosts is admitted and every other refused: a request with no `Origin`, and one from `http://localhost` over `http` or `https`, with a port, or from the environment's other host `http://127.0.0.1` reaches the service, and a preflight from `http://localhost` is answered `200` by the proxy; an origin on another environment's host, the host in capitals, with a trailing dot or with other characters for its dots, `null`, a present but empty `Origin`, a preflight from another origin and a WebSocket upgrade from one answer `403`. nginx reads an empty `Origin` as none, so it lets that one through, a difference it declares ([nginx](nginx.md#the-browser-origins-an-environment-admits)). Twenty-two rows, fifteen on `nginx-file`, which writes neither list nor `cors`. |
| auth | `auth.ask`, `auth.only_with_header`, `auth.send`, `auth.accept`, `auth.send_request`, `auth.when_unavailable`, `routes[].auth`, `remove_from_clients`, `auth.jwt.claims` | A request with no `authorization` header reaches the service, which it could not if the check were asked; when the check allows, the service receives the check's headers `auth.accept` admits and not another, and what the check was told (the method, the path with its query, `x-country`), never the `x-forwarded-uri` and `x-forwarded-method` the client sent; the check's `401` and `403` reach the client, without the `x-envoy-upstream-service-time` Envoy's client for the check adds; a check that answers `503` lets the request through, as `when_unavailable = allow` says; the mark of a request the check could not answer reaches the service, and a client's own copy never does, whether the check allows or is not asked; a route with `auth = off` is not checked; a client's own copies of the headers `remove_from_clients` names, of the check's headers and question, and of the headers `auth.jwt.claims` sets never reach the service. Ten rows, nine on `portable`, which writes no `auth.jwt.claims`. |
| timeout | `auth.timeout`, `auth.when_unavailable` | The check holds the request for 3 s, past `auth.timeout`'s 1500 ms, and the request reaches the service with the mark of a request the check could not answer, after at least 1.45 s and in under 2.95 s, 50 ms inside the two times because the row reads a different clock from the proxy's and the check's (below). One row. |
| retry | `routes[].retries`, `routes[].rpc.retries` | A request that reached the service is never tried again: a first try the service closes before any answer gives `502`, a gRPC call whose first try is reset gives `grpc-status` `14`, and a `503` from the service is passed on from its first try. A request whose connection a backend refused is tried again on the other: with one of the two shop backends refusing connections, 40 requests to the shop route and 40 gRPC calls to shop are each answered by the other backend. Five rows, which run on `full`: `portable` writes no retries. |
| gRPC | `routes[].rpc`, `routes[].rpc.to`, `routes[].rpc.to.<package pattern>`, `routes[].rpc.to.<package pattern>.service`, `rpc.methods`, `rpc.methods[].service`, `rpc.methods[].method`, `rpc.methods[].action`, `rpc.methods[].reports`, `services.<service name>.grpc`, `auth.ask` | A method of a package mapped to `shop` reaches shop's gRPC port over HTTP/2 and answers `grpc-status` `0`; a method of any other package reaches `accounts`; the check is told a method's action and reports, and not an action the client sent, and the service receives neither; a gRPC service's `X-Accel-Redirect` is passed on, not followed; a call through a `..` segment reaches the method's path. Six rows, four on `nginx-file`, which writes no `auth.ask`. |
| path normalisation | none: it is written for every file ([Written with no key](portability.md#written-with-no-key)) | `..`, `.`, `//`, `%2F` and `%2e%2e` in a path are resolved before a route is picked, and the service hears the normalised path; so are `..` followed by an escaped slash (`..%2f`), by a backslash written as `%5C`, and by a written backslash; `..;`, written with dots, resolved as `..`; an exact path reached through `..`. An invalid escape, `%zz`, is passed on to the service, whose own `400` answers it. An escaped `+` and an empty query reach the service as sent; an empty query after a path that needs normalising reaches it after the normalised path, and nginx answers it `400`, a declared difference. A path that still holds a dot segment with a `;` parameter after normalisation answers `400` and reaches no service, the `;` written or escaped once: `/catalog/%2e%2e;/products/1`; `..%3B` and `..%3b`; `.%3B` and `%2e%3b`; dots written escaped, in either case of hex, before an escaped `;`; an escaped `;` with a value or twice over; on the last segment and on the first; between backslashes, written or as `%5C`; and before an escaped slash. These reach the service as sent: a `;` parameter on an ordinary segment, on a name and on a name that starts with a dot; a `;` and an escaped `;` after three dots; an escaped `;` after a name that ends in two dots and inside a name; a `;` escaped twice after `..`; a query that holds `/..;/` or `/..%3B/`; and an escaped `?` after `..`. A dot segment with an escaped `;` that a `..` after it removes reaches the service resolved, and nginx answers it `400`, a declared difference. Forty-one rows. |
| the path as the client wrote it | none: it is written for every file ([Written with no key](portability.md#written-with-no-key)) | A path with nothing to normalise, or with only an escape Envoy keeps, is matched and reaches the service as written: `/api/x%3Bv=1/admin`, `/api/a%20b/admin`, `/u/%40bob`, `/l/a%20b` and `/api/a%7cb`, in the case of hex written, where nginx's own files have patterns that would take the decoded character; `/x%C3%A9` is not taken by the pattern `^/x..$`; an empty query on such a path reaches the service as sent. A path that needs normalising and keeps no escape is matched and sent normalised. A raw `\|` reaches the service as `%7C`. nginx answers `400`, each a declared difference, to a path that needs normalising and keeps an escape, `/api/a%2fb%3bc`; to a byte Envoy escapes beside an escape; to a byte Envoy escapes on `nginx-file`, which has pattern routes; to an escape and to an empty query on a route with a `rewrite`; and to an empty query after a byte Envoy escapes. A `#` in the target is answered `400` over HTTP/1.1 and HTTP/2. Eighteen rows. |
| an auth-off prefix route | `routes[].prefix`, `routes[].auth`, `auth.ask`, `auth.only_with_header`, `auth.accept`, `auth.send_request` | On `portable`, which sends `/public/` to `shop` with `auth = off` before its other prefix routes: a path under it reaches the service unchecked with a token the check denies, and so does one with a repeated slash, which the service hears merged; a client's own copy of the mark of a request the check could not answer does not reach the service there either. A path that `..`, `%2e%2e`, `..%2f`, `..%5C` or `..\` takes out of `/public/` leaves the route: with a token the check denies it gets the check's `401`, and with a token the check allows it reaches the service, and the check is told the path the service hears, the normalised one; without the normalisation each would stay on the route and reach the service unchecked. `..;` with a token the check denies leaves the route too and gets the check's `401`. A dot segment with a `;` parameter is answered `400` and reaches no service with no token and with a token the check allows, the `;` written after `%2e%2e` or escaped as `%3B` after `..`. On `full`, which has no such route, the first and the third are answered `401` by the check its shop route asks. Eighteen rows. |
| rate limit | `routes[].rate_limit`, `routes[].rate_limit.requests`, `routes[].rate_limit.per`, `routes[].rate_limit.burst`, `routes[].rate_limit.by`, `auth.ask` | After 2.5 s with no request to the route, 60 requests to `/catalog/items` back to back: the first 30 on `full` (20 a second and a burst of 10) and the first 20 on `portable` (20 a second and no burst) answer `200`, and at least one of the rest `429`; then, of 10 requests over the limit that carry a token the check denies and one the token filter refuses, at least one is answered `429`, not `401`, so the limit is applied before the token and the check; another route is not limited. Three rows. |

Where a target declares a difference that changes a row's answer, the row holds that target's own literal under `by`,
named by the declaration's row: the row of the support table in [Portability](portability.md) whose partial cell says
the difference. That literal was measured on the target's own images. The battery fails an own literal whose row the
target does not declare partial, or that no run of that target reads.

A target that refuses a key a file writes cannot render that file, so the example commits, instead of the target's
configuration, the lines render prints: `<target>.refused` beside the proxyfile. For such a file the battery starts no
proxy: it passes when render exits 1 and prints exactly those lines, and a file render writes although its folder
commits a refusal fails. In this version nginx refuses every key its generator does not write yet, so every example but
`examples/grpc` commits `nginx.refused`.

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
so a row can check that the proxy passes the header on instead of following it. A request the stub cannot read gets
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
through their own mounts. The two client certificates, one the clients CA signs and one a second CA of the same name
signs, stay in memory with their keys, so the proxy never sees them and no run writes them to disk.

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
[`scripts/validate-nginx.sh`](../scripts/validate-nginx.sh) reads in the same way.
With the images already pulled and a warm Go build cache, a run of every entry's files, five worlds, took 19.1 to 19.9 s
in 30 of 30 runs on one arm64 Mac; in an earlier series on that Mac one run of 30 took 49 s, its world slow to start.
Two rows count on time. The rate limit's 60 requests must take under 25 ms each on average on `full` and under 33 ms on
`portable`, because the bucket refills one request every 50 ms and 30 refills on `full`, 40 on `portable`, would admit
every request after the first 30 or 20; over those 30 runs the 60 took 0.03 to 0.04 s on `full` and 0.02 to 0.05 s on
`portable`. The timeout row's answer must come between 1.45 s and 2.95 s, and took 1.50 to 1.51 s on each file. The
timeout row times the request on the client's clock, while the proxy times `auth.timeout` on its own, so its bounds keep
50 ms from the 1.5 s timeout and from the check's answer at 3 s. Envoy counts the timeout from the time its event loop
last read, from a clock that moves in 1 ms steps under Docker Desktop's kernel, and in another series 2 of 30 runs on
that Mac saw the answer at 1.4996 s and 1.4998 s, under 1 ms short of 1.5 s. A repeated row that fails says how long its
requests took.

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

The check `Required` of the `Pre-merge` workflow runs `go vet -tags conformance ./conformance/...` and the battery
after the Envoy and nginx validations, whose pulls leave the images cached. The release workflow runs the battery
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
4. An entry per image in `targets` in [`conformance/targets_test.go`](../conformance/targets_test.go): its label, the
   `--target` name, the files it runs, the header it adds to a request the check could not answer, its image pinned
   by digest, its arguments and the URL that answers 200 when it is ready. Its images are in `scripts/<proxy>-images`,
   one pinned by digest a line, read by its entries and by its validator, `scripts/validate-<proxy>.sh`, which
   `Required` runs on every configuration the examples commit for it. The rendered files are mounted in `/config`, the
   configuration at `/config/<file name>`, and the ports 10000 and 9901 and the folders `/etc/gateway/tls` and `/etc/gateway/jwt` are
   the examples', the same for every target. Its files together must run each row Envoy's files run, unless its
   target refuses one of the row's keys, or the battery fails before any container starts.
