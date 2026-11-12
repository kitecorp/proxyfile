# `services`

`services` names the services behind the gateway and gives each its ports and its backends: one address, the
addresses of several backends, or a DNS name the proxy looks up while it runs, with how requests are spread over them.
[Defaults](defaults.md) says which clusters a service gets.

<!-- keys: services -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `services` | The services behind the gateway, by name. | Required. A mapping keyed by service name. At least one service. | core |
| `services.<service name>` | One service: a port per protocol, an optional Host header, its backends, how requests are spread over them and how they are checked. | A mapping. The service name is lower-case letters, digits and -, starting with a letter. It has at least one port. | core |
| `services.<service name>.http` | The service's HTTP port. | A whole number from 1 to 65535. Spoken as HTTP/1.1. | core |
| `services.<service name>.grpc` | The service's gRPC port. | A whole number from 1 to 65535. Spoken as HTTP/2 without TLS (h2c). | core |
| `services.<service name>.host_header` | The Host header a request carries when a route sends it to this service. | A host, with an optional port. Without it a request keeps the client's Host. | core |
| `services.<service name>.address` | The service's IP address, or a DNS name the proxy looks up again while it runs; 127.0.0.1 when not written. | An IP address or a DNS name. A name has no port and is looked up every 5 seconds; every address it gives is a backend. Not with `addresses`. | core |
| `services.<service name>.addresses` | The service's backends, by IP address, each on the service's ports. | A list; each item is an IP address. Two or more, none written twice. Not with `address`. | core |
| `services.<service name>.balance` | How requests are spread over the backends; round\_robin when not written. | `round_robin`, `least_requests`, `random` or `hash`. | core |
| `services.<service name>.hash` | What balance hash hashes, so that one client or one header value always reaches the same backend. | A mapping. Required with `balance: hash`, and refused with any other balance. | core |
| `services.<service name>.hash.header` | The request header whose value is hashed; a request without it goes to a backend chosen at random. | A lower-case header name. Exactly one of `header` and `client` is written. | core |
| `services.<service name>.hash.client` | true hashes the client's address, the one client\_address names. | `true` or `false`. Only `true`. Exactly one of `header` and `client` is written. | core |
| `services.<service name>.health` | Active checks of each backend: one that fails unhealthy checks in a row leaves the pool until it passes healthy checks in a row. | A mapping. At least one of `path` and `grpc`. | core |
| `services.<service name>.health.path` | The path an HTTP GET must answer with a 2xx, on the service's http port. | A path starting with /, with no space. Needs an `http` port. | core |
| `services.<service name>.health.grpc` | true checks the service's grpc port with the gRPC health protocol, grpc.health.v1.Health/Check. | `true` or `false`. Only `true`. Needs a `grpc` port. | core |
| `services.<service name>.health.interval` | How often each backend is checked; 5s when not written. | A time such as 5s. | core |
| `services.<service name>.health.timeout` | How long one check may take; 2s when not written. | A time such as 2s. Shorter than `interval`. | core |
| `services.<service name>.health.healthy` | Checks in a row a backend must pass to join the pool; 2 when not written. | A whole number from 1 to 255. | core |
| `services.<service name>.health.unhealthy` | Checks in a row a backend must fail to leave the pool; 3 when not written. | A whole number from 1 to 255. With `grpc`, the Envoy generator's check removes a backend that answers it is not serving at once. | core |
<!-- /keys -->

## Backends and balancing

`address` is an IP address or a DNS name. A name is looked up again every 5 seconds, and every address the answer gives
is a backend; a backend that leaves the answer leaves the pool at the next lookup. Envoy asks for the name's IPv6
addresses first and for its IPv4 addresses only when it has none, so a name with both kinds gives its IPv6 addresses
alone. A name has no port: each backend takes the service's ports. `addresses` lists two or more IP addresses, each a
backend on the service's ports; a name goes in `address` instead. A service writes one of the two, or neither, which is
the one backend `127.0.0.1`.

`balance` says how requests are spread over the backends:

- `round_robin`, the default, takes the backends in turn. Each of the proxy's worker threads keeps its own turn, so
  over many connections the spread is close to even, not exact.
- `least_requests` prefers the backends with fewer requests open. Envoy compares two backends chosen at random and
  takes the less busy one, so with three or more backends it is not always the least busy of all. It differs from
  `random` only while requests overlap: with one request at a time, every backend has none open.
- `random` sends each request to a backend chosen at random.
- `hash` sends the requests that share a value to one backend, so `hash` must name the value. `hash.header` hashes the
  value of a request header: each value goes to one backend, and a request without the header goes to a backend
  chosen at random. `hash.client` hashes the client's address, the one `client_address` names; a gateway that faces
  clients over plain HTTP writes `client_address: connection`, so a client cannot choose its own address by sending an
  X-Forwarded-For header.

## Health checks

`health` checks each backend of a service on its own, and a backend that fails takes no request:

```yaml
services:
  shop:
    http: 8080
    addresses: ["10.0.1.11", "10.0.1.12", "10.0.1.13"]
    health: {path: /healthz}
```

A backend that fails `unhealthy` checks in a row leaves the pool, and comes back after it passes `healthy` checks in a
row. A failed check is a refused connection, no answer within `timeout`, or, for `path`, an answer outside the 2xx. A
backend's first check after the proxy starts decides its state on its own, in or out, and the two counts apply to every
change after it. One exception on Envoy: a gRPC backend that answers that it is not serving (NOT_SERVING) leaves at
once, whatever `unhealthy` says.

`path` checks the `http` port with a GET that must answer with a 2xx, and `grpc: true` checks the `grpc` port with the
gRPC health protocol. A service with both ports and only `path` checks its HTTP side alone, and keeps every backend of
its gRPC side. With `host_header` the check sends that Host too. Without it the Envoy generator's check sends the
cluster's name, `<service>_http`, as its Host, and `<service>_grpc` as the gRPC check's authority, so a service that
refuses a Host it does not know needs `host_header`.

A backend of a service found by a DNS name that leaves the name's answer leaves the pool at the next lookup, whether or
not it still passes its checks. When every backend of a service has failed, a request to it is answered at once with
`502` and an empty body, the answer for a service the gateway cannot reach. So `health` on a service with one backend
also helps: a dead backend's slow failures become immediate answers.

[`examples/balanced`](../examples/balanced/) writes several `addresses`, `balance` and `health` in
[YAML](../examples/balanced/yaml/proxyfile.yaml) and [HOCON](../examples/balanced/hocon/Proxyfile), each beside the
`envoy.yaml` it gives.

## Name lookups

<!-- keys: dns -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `dns` | The name lookups of the services whose address is a DNS name. | A mapping. | core |
| `dns.resolver` | The name server the proxy asks; the host's own resolver configuration when not written. | An IP address with an optional port, such as 10.0.0.2:53. Port 53 when not written. An IPv6 address with a port is written in brackets, such as [fd00::53]:5353; no zone. | core |
<!-- /keys -->

The proxy asks the name server in `dns.resolver` when it is written, and otherwise follows the host's resolver
configuration. nginx reads no `/etc/resolv.conf`, so it will refuse a name without `dns.resolver`.

[`examples/dns`](../examples/dns/) writes a DNS name in `address`, `hash` and `dns.resolver` in
[YAML](../examples/dns/yaml/proxyfile.yaml) and [HOCON](../examples/dns/hocon/Proxyfile), each beside the
`envoy.yaml` it gives.
