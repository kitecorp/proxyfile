# The format

A proxyfile describes one gateway in keys, in YAML or in HOCON. This page holds a file with every key, the forms of
sizes, times and percentages, the rules that compare keys with each other, and the two JSON files of 1.0.

`examples/full/gateway.yaml` holds every key a proxyfile can have except `services.<name>.address` and `tracing`, with
invented values: `cors`, the `origins` by environment and `rpc.methods` are among them. `examples/partial` writes
`address` and gives its methods three ways: `gateway.yaml` with `--rpc-methods examples/partial/methods.json`,
`gateway.conf` through `include required("rpc-methods.conf")`, and `Proxyfile` through `rpc.proto` and the `.proto`
files under `proto/`. `examples/tracing/gateway.yaml` writes `tracing`. Only `listen`, `services`, `origins` and
`routes` are required. The rest is left out when you do not need it.

```yaml
# yaml-language-server: $schema=../../schema/proxyfile.schema.json
# Every key a proxyfile can hold, with invented values, except tracing, which has its own example.
namespace: example.gateway
listen: 10000
admin: 9901
limits:
  heap: 256MiB
  connections: 4096
  request_headers: {size: 96KiB, count: 150}
timeouts:
  request: none
  idle: 90s
  upstream_idle: 45s
services:
  shop: {http: 8080, grpc: 8081}
  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070"}
auth:
  ask: accounts /auth/check
  timeout: 1500ms
  enabled: true
  when_unavailable: allow
  only_with_header: authorization
  send: [authorization, x-country]
  accept: [x-user-*, x-tier-*]
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
      to:
        example.shop.*: {service: shop, name: shop}
        "*": accounts
  - {name: accounts-webhooks, path: /api/webhooks, method: POST, to: accounts, rewrite: /hooks/payments, auth: off}
  - {name: shop, prefix: /, to: shop, retries: 1}
rpc:
  methods:
    - {service: example.shop.v1.CartService, method: GetCart, action: browse}
    - {service: example.accounts.v1.ProfileService, method: GetProfile}
    - {service: example.shop.v1.CartService, method: AddItem, action: buy, reports: [browse]}
log: {status_from: 400, sample: 1%}
```

The first line points an editor at the schema in this repository. In your own files, use the URL under
[Editors](editors.md).

The examples on these pages are YAML, whose file name ends in `.yaml` or `.yml`; [The HOCON form](hocon.md) gives the
other names and the same keys in HOCON. An unknown key anywhere is a problem, and the message lists the keys that are
known at that place.

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
- `auth: off` on a route needs an `auth` key.
- `cors` is not written when `origins` names a file, because the generator takes the CORS settings from that file and
  would ignore this key.
- The RPC methods come from one place: `rpc.methods`, `rpc.proto` or `--rpc-methods`. They need an rpc entry in
  `routes`, and the rpc entry needs them.

## The origins file and the methods file

Since 1.1 both can be written in the proxyfile: the environments under `origins`, the CORS settings under `cors`, and
the methods under `rpc.methods`. From 1.3 the methods can also be read from the `.proto` files, with `rpc.proto`. A
HOCON proxyfile that wants a separate file uses `include`, as `examples/partial/gateway.conf` does for its methods.
The 1.0 way stays accepted in every 1.x release, with the same rules: `origins: <file>` names a JSON origins file,
and `--rpc-methods <file>` a JSON methods file. Any two of `rpc.methods`, `rpc.proto` and `--rpc-methods` together
are refused, because exactly one gives the methods. `cors` in the proxyfile is refused too when `origins` names a
file.

The two files are JSON. They are read with the YAML reader, so a problem in either names its line and column, and a
syntax error its line. A slash escaped as `\/`, which JSON allows and some writers produce, is read as `/`.

[origins](origins.md#the-origins-file) describes the origins file, and [rpc](rpc.md#the-methods-file) the methods file.
