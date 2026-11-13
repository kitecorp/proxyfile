# The format

A proxyfile describes one gateway in keys, in YAML or in HOCON. This page holds a file with every key but the few it
names, the forms of sizes, times and percentages, the rules that compare keys with each other, the two JSON files of
1.0, [which keys a 1.x file moves](#from-1x-to-20) and [what 2.1 changes for a 2.0 file](#from-20-to-21).

`examples/full/yaml/proxyfile.yaml` holds every key a proxyfile can have, with invented values, except `rpc.proto`,
`envoy.tracing`, the `xray` provider, `hash.client` and the `nginx` and `haproxy` blocks, which take no key in this
version. Other examples write the first four and what the full example writes one way only:
`examples/partial/proto-methods` writes `rpc.proto`; `examples/tracing` the `xray` provider at `tracing` and, in
`envoy-tracing/`, at `envoy.tracing`, where it gives the same `envoy.yaml`; `examples/balanced` and `examples/dns` the
other ways to give a service its backends, `hash.client` among them; `examples/otel` a tracing endpoint by name; and
`examples/jwt` a keys file and a route without the token check. `examples/partial` gives its methods three ways, each
in a folder of its own: `yaml/proxyfile.yaml` with `--rpc-methods ../methods.json`, `hocon/Proxyfile` through
`include required("../rpc-methods.conf")`, and `proto-methods/Proxyfile` through `rpc.proto` and the `.proto` files
under `proto/`. `examples/grpc/yaml/proxyfile.yaml` turns `envoy.connect_bridge` off. Only `listen`, `services`,
`origins` and `routes` are required. The rest is left out when you do not need it.

```yaml
# yaml-language-server: $schema=../../../schema/proxyfile.schema.json
# Every key but rpc.proto, envoy.tracing, the xray provider, hash.client, nginx and haproxy, with invented values.
listen: 10000
admin: 9901
tls:
  certificate: /etc/gateway/tls/certificate.pem
  key: /etc/gateway/tls/key.pem
  clients: {ca: /etc/gateway/tls/clients-ca.pem}
client_address: connection
limits:
  connections: 4096
  request_headers: {size: 96KiB, count: 150}
timeouts:
  request: none
  idle: 90s
  upstream_idle: 45s
services:
  shop:
    http: 8080
    grpc: 8081
    addresses: ["10.0.1.11", "10.0.1.12"]
    balance: least_requests
    health: {path: /healthz, grpc: true, interval: 10s, timeout: 1s, healthy: 2, unhealthy: 3}
  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070"}
  catalog: {http: 8090, address: catalog.internal.example, balance: hash, hash: {header: x-session-id}}
dns: {resolver: "10.0.0.2:53"}
auth:
  ask: accounts /auth/check
  timeout: 1500ms
  enabled: true
  when_unavailable: allow
  only_with_header: authorization
  send: [authorization, x-country]
  accept: [x-user-*, x-tier-*]
  send_request: true
  jwt:
    issuer: https://login.example.com
    audiences: [api.example.com, admin.example.com]
    keys: https://login.example.com/.well-known/jwks.json
    ca: /etc/gateway/jwt/ca.pem
    header: x-access-token
    optional: true
    claims: {x-subject: sub, x-roles: realm.roles}
remove_from_clients: [x-user-*, x-client-*, x-tier-*, x-gateway-*]
cors:
  allow_methods: "*"
  allow_headers: "*"
  expose_headers: "Content-Disposition,X-Request-Id"
  max_age: "600"
origins:
  live:
    hosts: [api.example.com, api.example.org]
    origins: ["https://example.com", "https://www.example.com"]
    patterns: ["https://*.example.com"]
  test:
    hosts: [api.test.example.com]
    origins: ["http://localhost:3000", "https://test.example.com"]
    patterns: ["https://*.test.example.com"]
  local:
    hosts: [127.0.0.1, localhost]
    origins: []
    patterns: ["http://*.example.com:*", "http://localhost:*"]
routes:
  - {name: accounts-events, pattern: "^/events/[a-z0-9-]+$", to: accounts, auth: off}
  - rpc:
      retries: 1
      rate_limit: {requests: 100, per: 1s, by: all}
      to:
        example.shop.*: {service: shop, name: shop}
        "*": accounts
  - {name: accounts-webhooks, path: /api/webhooks, method: POST, to: accounts, rewrite: /hooks/payments, auth: off}
  - {name: catalog, prefix: /catalog/, to: catalog, rate_limit: {requests: 20, per: 1s, burst: 10, by: client}}
  - {name: shop, prefix: /, to: shop, retries: 1}
rpc:
  methods:
    - {service: example.shop.v1.CartService, method: GetCart, action: browse}
    - {service: example.accounts.v1.ProfileService, method: GetProfile}
    - {service: example.shop.v1.CartService, method: AddItem, action: buy, reports: [browse]}
log: {status_from: 400, sample: 1%}
tracing:
  provider:
    otel:
      endpoint: 10.0.3.40:4317
      service: example-gateway
      sample: {rate: 10%}
  tags:
    environment: {env: APP_ENVIRONMENT, default: local}
    request_id: {header: x-request-id}
  path_in_url: true
envoy:
  namespace: example.gateway
  heap: 256MiB
  connect_bridge: true
```

The first line points an editor at the schema in this repository. In your own files, use the URL under
[Editors](editors.md).

The examples on these pages are YAML, whose file name ends in `.yaml` or `.yml`; [The HOCON form](hocon.md) gives the
other names and the same keys in HOCON. An unknown key anywhere is a problem, and the message lists the keys that are
known at that place.

No key or value may hold a control character other than tab: NUL, a newline, a carriage return, any other character
from U+0001 to U+001F but tab, or U+007F. Written into the generated file as it is, such a character would break the
header value, the path or the name that holds it. It is written with an escape in quotes, such as `"a\nb"`, or as a
line break inside a YAML block or a HOCON `"""` string; a YAML block keeps the line break at its end unless it is
written `|-` or `>-`. Each such key or value is one problem, at its place, which names the first such character:
`the value "a\nb" contains a newline; no key or value may hold a control character other than tab`. The origins file,
the methods file and the `action` and reports read from `.proto` files follow the same rule.

## Sizes, times and percentages

Sizes are a whole number with `KiB`, `MiB` or `GiB`, such as `192MiB`. Times are a whole number with `ms`, `s`, `m` or
`h`, such as `125s`, and at most `9223372035s` (about 292 years), the largest whole number of seconds below the limit
Envoy sets on a duration. `auth.timeout` has a lower limit, which its row in [auth](auth.md) gives. A percentage is a
whole number from `0` to `100` with a `%`.

## Rules across keys

A proxyfile also has these rules, each reported with the file and where the problem is, a line in YAML and a key path in
HOCON:

- `listen` and `admin` are not the same port.
- At least one service is declared, and each service has at least one port.
- A route, `auth.ask` or an RPC mapping names a service that is declared and has a port of the protocol it needs: `http`
  for a route and for `auth.ask`, `grpc` for an RPC mapping.
- A service writes `address` or `addresses`, not both, and `addresses` lists two or more addresses, none twice.
- `hash` is written exactly when `balance` is `hash`.
- `health` writes `path` or `grpc`; `path` needs an `http` port and `grpc` a `grpc` port; `health.timeout` is shorter
  than `health.interval`, the defaults included.
- `auth` writes `ask`, `jwt` or both. `timeout`, `enabled` and `when_unavailable` are written with `ask` and only with
  it, and `only_with_header`, `send`, `accept` and `send_request` need it.
- `auth.jwt.ca` needs a URL in `auth.jwt.keys`. A header of `auth.jwt.claims` is not one that `auth.send` or
  `auth.accept` names. `auth.jwt.header` is not `cookie`, and the header that carries the token is neither a header of
  `auth.jwt.claims` nor one a `remove_from_clients` entry covers.
- `auth: off` on a route needs an `auth` key.
- `cors` is not written when `origins` names a file, because the generator takes the CORS settings from that file and
  would ignore this key.
- The RPC methods come from one place: `rpc.methods`, `rpc.proto` or `--rpc-methods`. They need an rpc entry in
  `routes`, and the rpc entry needs them.
- `tracing` and `envoy.tracing`, its old place, are not both written.

## The origins file and the methods file

Since 1.1 both can be written in the proxyfile: the environments under `origins`, the CORS settings under `cors`, and
the methods under `rpc.methods`. From 1.3 the methods can also be read from the `.proto` files, with `rpc.proto`. A
HOCON proxyfile that wants a separate file uses `include`, as `examples/partial/hocon/Proxyfile` does for its methods.
The 1.0 way is still accepted in 2.x, with the same rules: `origins: <file>` names a JSON origins file, and
`--rpc-methods <file>` a JSON methods file. Any two of `rpc.methods`, `rpc.proto` and `--rpc-methods` together are
refused, because exactly one gives the methods. `cors` in the proxyfile is refused too when `origins` names a file.

The two files are JSON. They are read with the YAML reader, so a problem in either names its line and column, and a
syntax error its line. A slash escaped as `\/`, which JSON allows and some writers produce, is read as `/`.

[origins](origins.md#the-origins-file) describes the origins file, and [rpc](rpc.md#the-methods-file) the methods file.

## From 1.x to 2.0

proxyfile 2.0 moves the three keys only Envoy reads into the [`envoy`](envoy.md) block. A 1.x file is refused with one
problem for each key still at its 1.x place, and each problem names where the key goes:

| In 1.x | In 2.0 |
|---|---|
| `namespace` | `envoy.namespace` |
| `limits.heap` | `envoy.heap` |
| `tracing` | `envoy.tracing` |

From 2.1 a 1.x file's `tracing` is read again where 1.x wrote it, as the [`tracing`](tracing.md) key, so only
`namespace` and `limits.heap` still move.

Every other key stays where it was, and in 2.0 a file with the three keys moved renders the same bytes as in 1.x. The
Connect bridge on RPC routes, which 1.x always wrote, is now `envoy.connect_bridge`, `true` when not written, so a 1.x
file needs no line for it.

## From 2.0 to 2.1

Render every file again after the upgrade, because every generated configuration changes. The proxy now normalises
each request's path before it picks a route, with three lines of the listener: `normalize_path: true`,
`merge_slashes: true` and `path_with_escaped_slashes_action: UNESCAPE_AND_FORWARD`, where 2.0 wrote the first two as
`false`. So a service receives `/b` for `/a/../b`, `/a` for `//a` and `a/b` for `a%2Fb`
([A route with `auth: off`](auth.md#a-route-with-auth-off) says why). Each virtual host also gains a first route, three
lines, that answers `400` to a path that still holds `/.;` or `/..;` after normalisation. Until a file is rendered
again, `proxyfile check` reports its 2.0 output as different.

With `auth`, the listener also gains one local reply mapper, seven lines: a check that answers a 2xx other than `200`,
such as `201`, `202` or `204`, now gives the client `403`. 2.0 passed that 2xx on to the client, a success for a request
that never reached the service ([A check that answers another 2xx](auth.md#a-check-that-answers-another-2xx)).

[`tracing`](tracing.md) is a key at the top again. `envoy.tracing`, where 2.0 read it, stays its old place in every 2.x
release, with the `xray` provider only. A file that writes both is refused, and moving the block from `envoy.tracing`
to `tracing` changes no byte. A 1.x file's `tracing` is read again where 1.x wrote it.

No 2.0 file is refused. [`auth`](auth.md) takes `jwt`, with or without `ask`, and `timeout`, `enabled` and
`when_unavailable` stay required with `ask`, as in 2.0.

The keys 2.1 adds:

- [`services.<name>.addresses`](services.md#backends-and-balancing): several backends for one service.
- [A DNS name in `address`](services.md#backends-and-balancing): backends the proxy looks up while it runs.
- [`balance`](services.md#backends-and-balancing): how requests are spread over the backends.
- [`hash`](services.md#backends-and-balancing): what `balance: hash` hashes, a header or the client's address.
- [`health`](services.md#health-checks): checks that take a failing backend out of the pool.
- [`dns`](services.md#name-lookups): the name server the proxy asks.
- [`tls`](tls.md): HTTPS on the public listener, and with `clients` mutual TLS.
- [`client_address`](listener.md#the-clients-address): which address counts as the client's.
- [`rate_limit`](routes.md#rate-limits): how many requests a route or the rpc entry admits.
- [`tracing` with `otel`](tracing.md#opentelemetry): spans sent to an OpenTelemetry collector.
- [`auth.send_request`](auth.md#what-the-check-is-told): the check learns which request it is asked about.
- [`auth.jwt`](auth.md#tokens): the proxy verifies a signed token on the request itself.
