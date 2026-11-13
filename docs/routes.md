# `routes`

`routes` lists the routes in the order they are tried, with the rpc entry among them marking where the RPC routes go.

How routes are matched:

- The routes are tried in the order written, and the first that matches wins. Every environment gets the same list. Put
  a `prefix: /` route last, because it matches every path. The generated file's first route, before every route written
  here, answers `400` to a path that still holds `/.;` or `/..;` after normalisation.
- Write exactly one of `prefix`, `path` and `pattern` on each route.
- A route matches the request's path after the proxy normalises it: dot segments resolved, repeated slashes merged,
  `%2F` and a backslash read as `/`, and an escaped letter written out (`%61` as `a`). The service receives that path:
  `/a/../b` as `/b`, `//a` as `/a`, `/files/a%2Fb` and `/files/a\b` as `/files/a/b`. A `;` parameter on a name and the
  case of letters are kept as sent: `/admin;x=1` and `/ADMIN` reach the service unchanged. Measured on Envoy 1.40.2.
  [A route with `auth: off`](auth.md#a-route-with-auth-off) says why the proxy does it.
- Two routes with the same kind of match, the same text and the same `method` are a problem, because the later one is
  never reached.
- A retry happens only when the request never reached the service (Envoy's `connect-failure` and
  `reset-before-request` conditions), so a request that the service saw is never sent twice. It goes to a backend the
  request has not tried yet when the service has one, so a backend that refuses connections does not get the retry
  too, and a service with one backend is retried on it. Measured on Envoy 1.40.2: with one of two backends refusing
  connections, every request with `retries: 1` was answered by the other; [Defaults](defaults.md) says how.

The rpc entry (`- rpc:` in YAML, `{ rpc { ... } }` in HOCON) is a place in the list. It holds one route for each method
of `rpc.methods`, of the `.proto` files `rpc.proto` reads, or of the methods file, matched on the exact path a call
carries, `/<package>.<Service>/<Method>`. Those routes are sorted by path and written where the entry stands, so a
plain route written above the entry is tried before the RPC routes, and one written below it after them. A file has
at most one rpc entry, and it needs methods from exactly one of `rpc.methods`, `rpc.proto` and `--rpc-methods`.

A method goes to the mapping with its exact service name if there is one. Otherwise it goes to the matching pattern with
the longest text, and `*` is last. The order in which the patterns are written does not matter. A method that no
pattern matches is a problem.

<!-- keys: routes -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `routes` | The routes, tried in the order written; the rpc entry marks where the RPC routes go. | Required. A list; each item is a mapping: a route or an rpc entry. Not empty. The rpc entry is `- rpc:` in YAML and `{ rpc { ... } }` in HOCON. | core |
| `routes[].rpc` | The RPC routes: one exact-path route for each RPC method. | A mapping. At most one rpc entry in a file. It needs methods from exactly one of `rpc.methods`, `rpc.proto` and `--rpc-methods`. | core |
| `routes[].rpc.retries` | How many times an RPC call that never reached its service is tried again. | A whole number from 0 to 4294967295. | core |
| `routes[].rpc.rate_limit` | How many requests the route admits in a period; a request over the limit is answered 429 and never reaches the service. | A mapping. One limit for every RPC route of the entry, in every environment. | core |
| `routes[].rpc.rate_limit.requests` | Requests admitted per period. | Required. A whole number from 1 to 4294967295. | core |
| `routes[].rpc.rate_limit.per` | The period. | Required. A time such as 1s or 1m. At least `1s`. | core |
| `routes[].rpc.rate_limit.burst` | Extra requests admitted above requests after a quiet spell; 0 when not written. | A whole number from 0 to 4294967295. `requests` and `burst` together at most 4294967295. | core |
| `routes[].rpc.rate_limit.by` | client counts each client address on its own, all every client together; client when not written. | `client` or `all`. The client's address is the one `client_address` names. | core |
| `routes[].rpc.to` | The service the RPC methods of each package go to. | Required. A mapping keyed by package pattern. Not empty. | core |
| `routes[].rpc.to.<package pattern>` | Which services' methods go to the value: \* matches every service, a package name ending in .\* every service whose full name starts with that package and a dot, and a full service name that service alone. | A service name, or a mapping with service and name; as text, lower-case letters, digits and -, starting with a letter. The package pattern is \*, a package name ending in .\*, or a full service name. The service needs a `grpc` port. | core |
| `routes[].rpc.to.<package pattern>.service` | The service the methods go to. | Required. Lower-case letters, digits and -, starting with a letter. | core |
| `routes[].rpc.to.<package pattern>.name` | The name of the routes; the service's name followed by -rpc when not written. | Letters, digits, dots, - and \_. | core |
| `routes[].name` | The route's name. | Required. Letters, digits, dots, - and \_. It appears in the request log. Not used twice, and not `rpc`. | core |
| `routes[].prefix` | Matches paths that start with this text. | A path starting with /. Exactly one of `prefix`, `path` and `pattern` is written. | core |
| `routes[].path` | Matches this path exactly. | A path starting with /. Exactly one of `prefix`, `path` and `pattern` is written. | core |
| `routes[].pattern` | Matches paths by this regular expression, in RE2 syntax. | A regular expression that is not empty. It must compile. Exactly one of `prefix`, `path` and `pattern` is written. | core |
| `routes[].method` | Matches only requests with this HTTP method. | An HTTP method in capitals, such as POST. | core |
| `routes[].to` | The service the route goes to. | Required. Lower-case letters, digits and -, starting with a letter. A declared service with an `http` port. | core |
| `routes[].rewrite` | Replaces the matched prefix or path before the request goes to the service. | A path starting with /. Not allowed with `pattern`. | core |
| `routes[].auth` | off sends the route's requests to the service without auth's checks: no service is asked and no token is verified. | `off`. Needs an `auth` key. | core |
| `routes[].retries` | How many times a request that never reached the service is tried again. | A whole number from 0 to 4294967295. | core |
| `routes[].rate_limit` | How many requests the route admits in a period; a request over the limit is answered 429 and never reaches the service. | A mapping. One limit for the route's copy in every environment. | core |
| `routes[].rate_limit.requests` | Requests admitted per period. | Required. A whole number from 1 to 4294967295. | core |
| `routes[].rate_limit.per` | The period. | Required. A time such as 1s or 1m. At least `1s`. | core |
| `routes[].rate_limit.burst` | Extra requests admitted above requests after a quiet spell; 0 when not written. | A whole number from 0 to 4294967295. `requests` and `burst` together at most 4294967295. | core |
| `routes[].rate_limit.by` | client counts each client address on its own, all every client together; client when not written. | `client` or `all`. The client's address is the one `client_address` names. | core |
<!-- /keys -->

## Rate limits

`rate_limit` on a route, or on the rpc entry, limits how many requests reach the service. It is a token bucket: it
holds `requests + burst` tokens and starts full, and each request takes one token. Tokens come back at a steady rate
of `requests` per `per`, not all at once when a period ends. A request that finds the bucket empty is answered `429`
and never reaches the service or the auth check.

Measured on Envoy 1.40.2 with the generated `examples/limited` file, where `login` has `requests: 5`, `per: 1m` and
`burst: 5`: a client's first 10 requests pass, the next is answered `429`, and after that one more passes every 12
seconds.

- Each limited route has one bucket, shared by its copy in every environment. On the rpc entry, every method the entry
  routes shares one bucket. A route without `rate_limit` is never limited.
- With `by: client`, the default, each client address has a bucket of its own: the address
  [`client_address`](listener.md#the-clients-address) names. With `forwarded`, a client that reaches the gateway
  directly writes its own `X-Forwarded-For`, and so chooses its own bucket; a gateway on plain HTTP that faces its
  clients writes `client_address: connection`. Envoy keeps the buckets of 65,536 client addresses for each limited
  route; when more addresses than that use the route, the one not seen for longest loses its bucket and starts again
  with a full one. An address is counted whole, so an IPv6 client that changes its address inside its own prefix gets a
  new bucket each time. `by: all` counts every client together.
- A limit counts the requests its route matches, and only those. The route is matched on the normalised path, which
  merges slashes and resolves dot segments but keeps the case, a slash at the end and a `;` parameter. So when another
  route sends the same service a path the service reads as the limited one, a client over its limit sends that path
  instead. Measured on Envoy 1.40.2 with `examples/limited`: after `POST /login` was answered `429`, `POST //login`,
  `/x/../login` and `/%2Flogin` were answered `429` too, and `/login/`, `/LOGIN` and `/login;x=1` each took the `shop`
  route and reached the service. Give the route that takes the rest a limit too, or make the service refuse the other
  spellings.
- A gRPC call over the limit gets `grpc-status` 8, `RESOURCE_EXHAUSTED`, which gRPC clients do not retry by default. A
  Connect call gets the Connect error `resource_exhausted`. A Connect GET call carries no `connect-protocol-version`
  header and gets the plain `429`, and a Connect stream reads either answer as `unavailable`. Any other request gets
  `429` with the body `local_rate_limited`.
- The request log writes such a request with the flag `RL` and the details `local_rate_limited`.

```yaml
routes:
  - rpc: {to: {"*": shop}, rate_limit: {requests: 100, per: 1s, by: all}}
  - {name: login, path: /login, method: POST, to: shop, rate_limit: {requests: 5, per: 1m, burst: 5}}
  - {name: shop, prefix: /, to: shop}
```

Here every RPC method together admits 100 calls a second. Each client may send 10 login requests at once, then one
every 12 seconds, the rate of 5 a minute. The `shop` route has no limit, so a path only it matches is never limited,
`/login/` included.

[`examples/limited`](../examples/limited/) writes a limit on a route and on the rpc entry in
[YAML](../examples/limited/yaml/proxyfile.yaml) and [HOCON](../examples/limited/hocon/Proxyfile), each beside the
`envoy.yaml` it gives.
