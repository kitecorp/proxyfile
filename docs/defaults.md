# Defaults

Everything the format has no key for is the same in every configuration the Envoy generator writes, and this page says
what that is.

- **One public listener** on `0.0.0.0`, so a container runtime can publish the port. It serves plain HTTP unless
  [`tls`](tls.md) is written. Who may reach it is the network's rule, not the gateway's. Each service gets one cluster for each port it declares, named `<service>_http` and
  `<service>_grpc`, with no TLS to the service.
- **One backend at `127.0.0.1` for a service** that writes neither `address` nor `addresses`. Requests are spread round
  robin when `balance` is not written, and a DNS name in `address` is asked of the host's resolver configuration when
  `dns` is not written. Without `health` every backend stays in the pool.
- **A DNS name is looked up for its IPv4 and IPv6 addresses both** (`dns_lookup_family: ALL`), whether it is a
  service's address, the tracing endpoint or the keys URL's host, and each address is a backend. Envoy's own default
  asks for IPv6 addresses first and for IPv4 ones only when there are none
  ([Backends and balancing](services.md#backends-and-balancing)).
- **The client's address** is `forwarded` without `tls` and `connection` with it, when `client_address` is not
  written. With `forwarded` (`use_remote_address: false`) a proxy stands in front: `X-Forwarded-For` passes as it came,
  `X-Forwarded-Proto` is kept when present, and with no trusted hops Envoy takes the last `X-Forwarded-For` entry, the
  one the proxy in front appended, as the client's address. With `connection` (`use_remote_address: true`) the client's
  address is the peer of the connection, which Envoy appends to `X-Forwarded-For`, and `X-Forwarded-Proto` is set from
  the connection. [The listener](listener.md#the-clients-address) says when to write which.
- **A browser origin that is not admitted gets `403` with the body `Invalid CORS request`**, the answer a Spring service
  gives for a refused origin. Changing it needs a key that does not exist yet.
- **Every response carries `x-request-id`**, the client's own when it sent one, so a caller can quote the id of a failed
  call. It is the `request_id` of the request log. With `client_address: connection`, Envoy replaces a client's own id
  on a request it counts as external.
- **The service's own `Server` header passes through**, and Envoy adds none of its own. The router adds no `x-envoy-*`
  header to a request or a response. With `client_address: connection`, Envoy itself adds `x-envoy-external-address` to
  the request ([The listener](listener.md#the-clients-address)).
- **`Vary: Origin` on every response**, added to a `Vary` the service sent, never replacing it.
- **Every request's path is normalised before a route is picked.** `normalize_path: true`, `merge_slashes: true` and
  `path_with_escaped_slashes_action: UNESCAPE_AND_FORWARD` are always written: dot segments are resolved, repeated
  slashes merged, and `%2F`, a backslash and `%5C` read as `/`, so the route, a rate limit, the token, the check and
  the service all read the path the service receives. A `;` parameter on a name and the case of letters are kept as
  sent, and a `;` parameter on a dot segment written with dots is dropped with the segment resolved. Measured on Envoy
  1.40.2 ([`routes`](routes.md), [A route with `auth: off`](auth.md#a-route-with-auth-off)).
- **A path that still holds a dot segment with a `;` parameter is answered `400`.** The first route of every virtual
  host, `refused:dot-segment-with-parameter`, takes a path that holds `/.;` or `/..;` after normalisation, so no route
  of the proxyfile takes it and it reaches no service. The route does not turn `auth` off, so the token filter and the
  check still run before its `400` ([A route with `auth: off`](auth.md#a-route-with-auth-off)).
- **WebSocket is the one upgrade passed on.** Envoy answers any other `Upgrade` to a route with `403` and an empty
  body.
- **A service that cannot be reached answers `502` with an empty body**, where Envoy's own answer is a `503` or a `502`
  with a body that describes the failure.
- **A Connect call over a rate limit gets the Connect error `resource_exhausted`**, a `429` with a JSON body, where
  Envoy's own `429` is plain text, which a Connect client reads as `unavailable`. It is written with a `rate_limit`
  ([Rate limits](routes.md#rate-limits)).
- **A check's 2xx other than `200` is answered `403`**, with `auth.ask`. Only `200` lets a request through, so the
  client never receives a success for a request that did not reach the service
  ([`auth`](auth.md#a-check-that-answers-another-2xx)).
- **Headers are removed early.** Everything in `remove_from_clients`, and with `auth.ask` the header
  `x-envoy-auth-failure-mode-allowed`, is removed before routing and before any filter. With `auth.ask`, the gateway
  also removes from clients the two question headers and every header the check may set (each name of `auth.accept`),
  and with `auth.jwt` every header a verified token sets (each header of `auth.jwt.claims`), whether or not
  `remove_from_clients` lists them, because a client must not be able to forge what the gateway asks the check, what
  the check says or what the token says. With `tracing` (or `envoy.tracing`), `x-envoy-downstream-service-cluster` and
  `x-envoy-downstream-service-node` are removed after those, and with `otel`, `x-envoy-decorator-operation` last, which
  a client could otherwise use to rename the span; each unless an entry before it covers it.
- **No `tracing` and no `envoy.tracing`: no tracer.** [Tracing](tracing.md) says what the key writes.
- **No `auth.send_request`: the check sees the path of `ask` only.** [What the check is
  told](auth.md#what-the-check-is-told) says what the key adds.
- **No `auth.jwt`: no token is verified at the proxy.** [Tokens](auth.md#tokens) says what the key adds.
- **Retries** happen only on `connect-failure` and `reset-before-request`.
- **The overload manager** checks every 0.25 seconds. It exists when `envoy.heap` or `limits.connections` is written.
- **The Connect bridge.** Each route of the rpc entry turns a Connect call into gRPC for the service's gRPC port, and
  the answer back into Connect. Envoy's bridge still writes the HTTP status of an older Connect error-code table for
  four codes, so a small Lua filter sets the current status: `canceled` 499, `deadline_exceeded` 504,
  `failed_precondition` 400 and `unimplemented` 501. Both filters are off for every other route, and neither is written
  when `envoy.connect_bridge` is `false`.
- **Route metadata.** With `auth.ask`, a plain route carries an empty action under `envoy.namespace`, and an RPC route
  carries its method's action. A route with `auth: off` carries none. The auth-unavailable log line reads it. A limited
  route also carries its bucket under `<envoy.namespace>.rate_limit`, which the rate limit filter reads, so a route with
  `auth: off` and a limit carries its bucket alone.
- **Three kinds of JSON log line on standard output.** The request log has the fields `time`, `route`, `host`, `method`,
  `protocol`, `status`, `flags`, `details`, `duration_ms`, `upstream_ms`, `bytes_in`, `bytes_out`, `upstream` and
  `request_id`, and with `auth.ask` also `authz_us`, how long the check took (`null` when none ran). The upstream
  failure line (`event: gateway_upstream_failure`) is written for each request an upstream failed. The auth-unavailable
  line (`event: gateway_auth_unavailable`) is written with `auth.ask` only. No log line holds a path, query, address,
  user agent or referrer, because paths can carry links and tokens. With `log`, the request log writes every response
  at or above `status_from`, every response with a response flag, and `sample` of the rest, chosen by the first eight
  hex digits of `x-request-id`.
- **The order of the filters:** set metadata (with `auth.ask`), header mutation, the RBAC refusal of a foreign browser
  origin, CORS, the rate limit filter (only when a route or the rpc entry has a `rate_limit`), the token filter (with
  `auth.jwt`), the auth check (with `auth.ask`), the Lua status filter and the Connect bridge (with an rpc entry, unless
  `envoy.connect_bridge` is `false`), and the router. A foreign origin and a request over a rate limit are refused
  before a token is verified or the check could be called, and a token is verified before the check is asked.

## Fixed names

These names never follow `envoy.namespace` or any other key, because other programs read them. A tool that changes a
runtime value, for example, looks it up by its key.

| Name | What it is |
|---|---|
| `public` | The listener, its `stat_prefix` and the route table. |
| `refused:dot-segment-with-parameter` | The first route of every virtual host, which answers `400` to a path that still holds `/.;` or `/..;` after normalisation. The request log writes it as `route`. No route of the file can take the name, because a route name holds no colon. |
| `gateway.ext_authz.enabled` | The runtime key that switches the auth check, written with `auth.ask`. Its default comes from `auth.enabled`. |
| `gateway.request_log.min_status` | The runtime key for the lowest status that is always logged, written with `log`. Its default comes from `log.status_from`. |
| `gateway.request_log.sample` | The runtime key for the share of other requests that is logged, written with `log`. Its default comes from `log.sample`. |
| `gateway.local_reply.origin_refused_status` | The runtime key for the status the `Invalid CORS request` answer is matched on. Its default is `403`. |
| `gateway.local_reply.rate_limited_status` | The runtime key for the status the Connect answer to a call over a rate limit is matched on, written with a `rate_limit`. Its default is `429`. |
| `rate_limit` | The rate limit filter's `stat_prefix`, written with a `rate_limit`. |
| `otel_collector` | The cluster of the tracing endpoint, written with the `otel` provider. |
| `jwks` | The cluster of the keys URL's host, written when `auth.jwt.keys` is an https URL. |
| `browser-origins` | The one RBAC policy of each virtual host. |
| `x-gateway-action`, `x-gateway-reports` | The question headers of an RPC route, read by the auth check. |
| `x-gateway-timeout-ms` | The check's deadline in milliseconds. |

One line of the generated file is meant for a tool to switch by editing the file as text: the default of the auth check.
It stays on one line in flow style for that purpose. It reads `{numerator: 100, denominator: HUNDRED}` when the check is
on and `{numerator: 0, denominator: HUNDRED}` when it is off:

```yaml
default_value: {numerator: 100, denominator: HUNDRED}
```

`proxyfile check` fails on an edited file, as it does on any edit, so check the file before a tool edits it. To change
the setting for good, change `auth.enabled` and render again.
