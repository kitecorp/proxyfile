# Conformance

The conformance battery runs a proxy on the configuration proxyfile renders for it and checks that the proxy answers
requests as the proxyfile says. It is the Go package [`conformance`](../conformance), whose files are test files only,
so the released program never carries them. It runs Envoy, the one target of this version.

## What the battery proves

[`examples/full/hocon/Proxyfile`](../examples/full/hocon/Proxyfile), which writes every key but the few its opening
comment names, is rendered for each target with `proxyfile render --target <name>` through the program's own command
line, the path a user takes. The proxy then runs on the rendered file in its own image, among stub backends, and the
same requests get the answers the format promises.

Each scenario covers one behaviour and names the core keys it exercises, and the battery fails when a scenario names a
key that is not a core key of the format. A row checks only what every proxy must answer: the status, which service
and which of its backends answered and what it received (port, protocol, method, path, host and headers), the
response headers that matter, `grpc-status` and a time window. Every row's answer was measured on Envoy 1.40.2, and
[`conformance/scenarios_test.go`](../conformance/scenarios_test.go) notes beside each scenario what Envoy also answers
that no row checks. The scenarios run in this order, one request at a time, 48 rows in all:

| Scenario | Keys | What its rows check |
|---|---|---|
| routing | `routes[].pattern`, `routes[].path`, `routes[].method`, `routes[].rewrite`, `routes[].prefix`, `routes[].to`, `services.<service name>.http`, `services.<service name>.addresses`, `services.<service name>.address`, `services.<service name>.host_header`, `dns.resolver`, `origins.<environment name>.hosts` | A pattern route, and a path its pattern misses going to the prefix route; a path and method route with its rewrite, and another method going to the prefix route; a service whose address the resolver gives; the prefix route over HTTP/1.1 and over HTTP/2, where the service still hears HTTP/1.1; the host each service hears, `host_header`'s value for `accounts`; a host of another environment; `404` for a host no environment names. Nine rows. |
| mutual TLS | `tls.certificate`, `tls.key`, `tls.clients.ca` | A client with no certificate, and one whose certificate another CA signed, reach no service: the handshake fails, or the proxy refuses it. The other CA carries the clients CA's name, so a proxy that matched the issuer by name would let it through. Every other row sends a client certificate the clients CA signed. Two rows. |
| CORS | `cors.allow_methods`, `cors.allow_headers`, `cors.expose_headers`, `cors.max_age`, `origins.<environment name>.origins`, `origins.<environment name>.patterns` | A preflight from a listed origin and one from an origin a pattern covers answer `200` with `access-control-allow-origin` and `access-control-max-age`; a preflight and a request from another origin answer `403`; a request from a listed origin reaches the service with its `origin` and is answered with `access-control-allow-origin` and `access-control-expose-headers`. The preflight from a listed origin and the request from another origin carry a token the check denies and one the token filter refuses, each answered `401` alone, so their `200` and `403` show that the origin is decided before the token and the check. An origin on the environment's own host that neither list holds, `https://api.example.org` to `api.example.org`, is admitted without `access-control-allow-origin`: its request reaches the service and its preflight answers `200`. Seven rows. |
| auth | `auth.ask`, `auth.only_with_header`, `auth.send`, `auth.accept`, `auth.send_request`, `auth.when_unavailable`, `routes[].auth`, `remove_from_clients`, `auth.jwt.claims` | A request with no `authorization` header reaches the service, which it could not if the check were asked; when the check allows, the service receives the check's headers `auth.accept` admits and not another, and what the check was told (the method, the path with its query, `x-country`), never the `x-forwarded-uri` and `x-forwarded-method` the client sent; the check's `401` and `403` reach the client, without the `x-envoy-upstream-service-time` Envoy's client for the check adds; a check that answers `503` lets the request through, as `when_unavailable = allow` says; a route with `auth = off` is not checked; a client's own copies of the headers `remove_from_clients` names, of the check's headers and question, and of the headers `auth.jwt.claims` sets never reach the service. Seven rows. |
| timeout | `auth.timeout`, `auth.when_unavailable` | The check holds the request for 3 s, past `auth.timeout`'s 1500 ms, and the request reaches the service after at least 1.45 s and in under 2.95 s, 50 ms inside the two times because the row reads a different clock from the proxy's and the check's (below). One row. |
| retry | `routes[].retries`, `routes[].rpc.retries` | A request that reached the service is never tried again: a first try the service closes before any answer gives `502`, a gRPC call whose first try is reset gives `grpc-status` `14`, and a `503` from the service is passed on from its first try. A request whose connection a backend refused is tried again on the other: with one of the two shop backends refusing connections, 40 requests to the shop route and 40 gRPC calls to shop are each answered by the other backend. Five rows. |
| gRPC | `routes[].rpc`, `routes[].rpc.to`, `routes[].rpc.to.<package pattern>`, `routes[].rpc.to.<package pattern>.service`, `rpc.methods`, `rpc.methods[].service`, `rpc.methods[].method`, `rpc.methods[].action`, `rpc.methods[].reports`, `services.<service name>.grpc`, `services.<service name>.health` | A method of a package mapped to `shop` reaches shop's gRPC port over HTTP/2 and answers `grpc-status` `0`; a method of any other package reaches `accounts`; the check is told a method's action and reports, and not an action the client sent, and the service receives neither. Four rows. |
| path normalisation | none: it is written for every file ([Written with no key](portability.md#written-with-no-key)) | `..`, `.`, `//`, `%2F` and `%2e%2e` in a path are resolved before a route is picked, and the service hears the normalised path; so are `..` followed by an escaped slash (`..%2f`), by a backslash written as `%5C`, and by a written backslash; an exact path reached through `..`; a path that still holds a dot segment with a `;` parameter after normalisation, `/catalog/%2e%2e;/products/1`, answers `400`. Ten rows. |
| rate limit | `routes[].rate_limit`, `routes[].rate_limit.requests`, `routes[].rate_limit.per`, `routes[].rate_limit.burst`, `routes[].rate_limit.by` | After 2.5 s with no request to the route, 60 requests to `/catalog/items` back to back: the first 30 (20 a second and a burst of 10) answer `200` and at least one of the rest `429`; then, of 10 requests over the limit that carry a token the check denies and one the token filter refuses, at least one is answered `429`, not `401`, so the limit is applied before the token and the check; another route is not limited. Three rows. |

A target that refuses a key the full example writes cannot render it, so no proxy runs for that target. Each scenario
is then checked by the render's error output instead: it passes when that output ends a line with the refusal of each
of its keys the target refuses, and a scenario none of whose keys the target refuses fails, because the full example
could not run. A render that succeeds although the target declares a key of a scenario refused fails that scenario,
`render wrote examples/full for <target>, which refuses "<key>"`, because the full example writes every key a scenario
names. No target of this version refuses a key.

## The world

The battery starts every address the full example names on one Docker network, so the rendered file runs unchanged:

| Container | Address | What it is |
|---|---|---|
| `proxyfile-conformance-proxy` | `10.0.0.10` | The proxy, with the rendered file read-only at `/config`, the TLS files at `/etc/gateway/tls` and the token CA at `/etc/gateway/jwt`. Its listener, port 10000, is published on `127.0.0.1` at a port Docker picks, so a run never meets a port another program holds. |
| `proxyfile-conformance-shop-1`, `proxyfile-conformance-shop-2` | `10.0.1.11`, `10.0.1.12` | `shop`: HTTP on 8080 and gRPC on 8081. |
| `proxyfile-conformance-catalog` | `10.0.2.21` | `catalog` on 8090, an address the example leaves to its resolver. |
| `proxyfile-conformance-resolver` | `10.0.0.2`, port 53 | The resolver: `catalog.internal.example` is `10.0.2.21`, and any other name is NXDOMAIN. |
| `proxyfile-conformance-accounts` | `127.0.0.1:7070` in the proxy's own network namespace | `accounts`, HTTP/1.1 and gRPC on one port, and the auth check `auth.ask` names. |
| none | `10.0.3.40:4317` | The tracing collector, left unreachable: no request waits for it. |

The network is `proxyfile-conformance` on `10.0.0.0/22`, the smallest block that holds every one of these addresses.

One stub program, [`conformance/stub`](../conformance/stub), plays every backend, the auth check and the resolver. A
backend answers each request with what it received: its service name, port, protocol, method, path, host and headers, so
a row can say which service answered and what reached it. The check decides by the value of the `authorization` header
(`Bearer allow`, `Bearer deny-401`, `Bearer deny-403`, `Bearer status-503` or `Bearer hang`), so each row chooses the
check's answer; any other token, or none, is denied with `401`, so a row that wants the check not to be asked fails when
the proxy asks it. The backends share the volume `proxyfile-conformance-state`: a request the stub closes on its first
try is answered by whichever backend a second try reaches, so a retry would show. A backend refuses connections, as
one that is down does, while a row needs it to: the battery sends its container `SIGUSR1`, which closes every port
and connection of the stub, and waits until a connection made inside the container is refused; after the row,
`SIGUSR2` opens the ports again, and the battery waits until each answers. The stub is built with Go for the platform
the Docker server runs and copied alone into the image `proxyfile-conformance-stub`.

Before the first request, the stub in `accounts`, which shares the proxy's network namespace, fetches the target's
ready URL every 100 ms until it answers 200, for 30 s at most: Envoy's admin listener answers only on `127.0.0.1`, and
its image has no shell.

Each run makes throwaway TLS files: a self-signed server certificate for `localhost` and `127.0.0.1` with its key, a
clients CA, and a copy of the server certificate as the CA bundle `auth.jwt.ca` names. They are written to the folder
`proxyfile-conformance` in the system's temporary folder (`$TMPDIR`, or `/tmp`), with the rendered file and the
stub's build, and the folder is the user's alone; the proxy reads its `tls` and `jwt` folders through their own
mounts. The two client certificates, one the clients CA signs and one a second CA of the same name signs, stay in
memory with their keys, so the proxy never sees them and no run writes them to disk.

## What it does not prove

- Answers each proxy writes in its own words. No row checks a local reply's body, or that an answer carries a header a
  proxy names after itself, which [portability](portability.md) classes as that proxy's own: measured on Envoy 1.40.2,
  the `403` body `Invalid CORS request`, the empty `502`, the `429` body `local_rate_limited`,
  `x-envoy-auth-failure-mode-allowed`, and the `403` Envoy answers when the check answers `204`. A row may check such
  a header absent, which holds for every proxy, as the two denial rows do.
- A retry of a service with one backend. A row would need the backend to refuse exactly one connection and accept the
  next, which the stub cannot time. Measured on Envoy 1.40.2 instead, with a firewall rule in the backend's own network
  namespace that refused one connection: the retry went to the same backend and was answered `200`, five times of
  five.
- Keys no scenario names. The rendered file carries them, and the proxy loads it, but no row checks what they do:
  among others `auth.jwt` but its `claims`, `limits`, `timeouts`, `log` and `tracing`. No row sends a valid token, so
  of `auth.jwt.claims` only the removal of a client's own copy is checked. The table above names every key a
  scenario exercises.
- What the check is told of the host. The stub's check reports the method and the path the proxy told it, not
  `x-forwarded-host`.

## Running it

It needs Docker and Go. From the repository root:

```sh
go test -tags conformance -count=1 ./conformance/...
```

The battery's tests are behind the build tag `conformance`, so `go test ./...` without it starts no container. Each
row is a subtest, `TestBattery/<target>/<scenario>/<row>` with `_` for each space, which `-v` lists, so
`-run 'TestBattery/envoy/routing'` runs one scenario. A row that fails prints each way its answer differs, for
example `service: got "shop", want "catalog"`, and the proxy's last 50 log lines follow.

The first run pulls the proxy's image by the digest in [`scripts/envoy-image`](../scripts/envoy-image), the file
[`scripts/validate-envoy.sh`](../scripts/validate-envoy.sh) also reads, so an Envoy upgrade changes one line for both.
With the image already pulled, a run took about 12 s with an empty Go build cache and about 7 s after that, measured
on one arm64 Mac. Two rows count on time: the rate limit's 60 requests must each take under 25 ms, and the timeout
row's answer must come between 1.45 s and 2.95 s; on that Mac, over ten runs, the 60 took 0.02 to 0.03 s and the
timeout row 1.50 to 1.51 s. The timeout row times the request on the client's clock, while the proxy times
`auth.timeout` on its own, so its bounds keep 50 ms from the 1.5 s timeout and from the check's answer at 3 s. Envoy
counts the timeout from the time its event loop last read, from a clock that moves in 1 ms steps under Docker
Desktop's kernel, and 2 of 30 runs on that Mac saw the answer at 1.4996 s and 1.4998 s, under 1 ms short of 1.5 s.
A repeated row that fails says how long its requests took.

The battery removes its own leftovers when it starts and everything it made when it ends: the containers named
`proxyfile-conformance-<role>`, the network `proxyfile-conformance`, the volume `proxyfile-conformance-state`, the
image `proxyfile-conformance-stub` and the folder `proxyfile-conformance` in the temporary folder. It touches no other
Docker object. A run stopped before its end, by `go test`'s `-timeout` or an interrupt, leaves all of them: the
containers keep running, the proxy still published on `127.0.0.1`, and the folder keeps the run's server key, made
for that run alone and trusted by nothing else. The next run removes them when it starts; to remove them at once:

```sh
docker rm -f proxyfile-conformance-accounts proxyfile-conformance-proxy proxyfile-conformance-shop-1 \
  proxyfile-conformance-shop-2 proxyfile-conformance-catalog proxyfile-conformance-resolver
docker network rm proxyfile-conformance
docker volume rm proxyfile-conformance-state
docker image rm proxyfile-conformance-stub
rm -rf "${TMPDIR:-/tmp}/proxyfile-conformance"
```

The names are fixed, so two runs at the same time on one Docker host, from two checkouts or two jobs on one daemon,
remove each other's containers: run one at a time on a Docker host. When another network already holds addresses in
`10.0.0.0/22`, the run stops with Docker's `Pool overlaps` answer and says which range it needs; it removes no network
it did not make, so free the range or run on another Docker host.

The check `Required` of the `Pre-merge` workflow runs `go vet -tags conformance ./conformance/...` and the battery
after the Envoy validation, whose pull leaves the image cached. The release workflow runs the battery before it builds
the release, so a release whose rendered configuration answers a scenario otherwise than the format says is not
published.

## Adding a proxy

A proxy joins the battery with one entry in `targets` in
[`conformance/targets_test.go`](../conformance/targets_test.go): its image pinned by digest, the `--target` name, the
rendered file's name, its arguments and the URL that answers 200 when it is ready. Its declaration of every core key
goes in `format.Targets` in [`internal/format/support.go`](../internal/format/support.go). No scenario changes: the
rendered file is mounted at `/config/<file>`, and the ports 10000 and 9901 and the folders `/etc/gateway/tls` and
`/etc/gateway/jwt` are the full example's, the same for every target.

A scenario whose key the proxy refuses checks the refusal's text instead: for each such key, render prints
`"<key>" is refused by --target <name>: <what the proxy offers instead>`, and the scenario passes when that line is
there, and fails when render writes the file anyway. A refusal of any key the full example writes stops its whole
render, so every scenario none of whose keys the proxy refuses then fails for that proxy, as
[What the battery proves](#what-the-battery-proves) says.
