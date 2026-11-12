# Defaults

Everything the format has no key for is the same in every configuration the Envoy generator writes, and this page says
what that is.

- **One public listener** on `0.0.0.0`, so a container runtime can publish the port. It serves plain HTTP unless
  [`tls`](tls.md) is written. Who may reach it is the network's rule, not the gateway's. Each service gets one cluster for each port it declares, named `<service>_http` and
  `<service>_grpc`, with no TLS to the service.
- **One backend at `127.0.0.1` for a service** that writes neither `address` nor `addresses`. Requests are spread round
  robin when `balance` is not written, and a DNS name in `address` is asked of the host's resolver configuration when
  `dns` is not written. Without `health` every backend stays in the pool.
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
- **No path rewriting.** Envoy neither normalizes a path nor merges slashes, so the service's own path rules decide
  what a path means.
- **WebSocket is the one upgrade passed on.** Envoy answers any other `Upgrade` to a route with `403` and an empty
  body.
- **A service that cannot be reached answers `502` with an empty body**, where Envoy's own answer is a `503` or a `502`
  with a body that describes the failure.
- **A Connect call over a rate limit gets the Connect error `resource_exhausted`**, a `429` with a JSON body, where
  Envoy's own `429` is plain text, which a Connect client reads as `unavailable`. It is written with a `rate_limit`
  ([Rate limits](routes.md#rate-limits)).
- **Headers are removed early.** Everything in `remove_from_clients`, and with `auth` the header
  `x-envoy-auth-failure-mode-allowed`, is removed before routing and before any filter. With `auth`, the gateway also
  removes from clients the two question headers and every header the check may set (each name of `auth.accept`), whether
  or not `remove_from_clients` lists them, because a client must not be able to forge what the gateway asks the check or
  what the check says. With `envoy.tracing`, `x-envoy-downstream-service-cluster` and `x-envoy-downstream-service-node`
  are removed last, each unless an entry before it covers it.
- **Retries** happen only on `connect-failure` and `reset-before-request`.
- **The overload manager** checks every 0.25 seconds. It exists when `envoy.heap` or `limits.connections` is written.
- **The Connect bridge.** Each route of the rpc entry turns a Connect call into gRPC for the service's gRPC port, and
  the answer back into Connect. Envoy's bridge still writes the HTTP status of an older Connect error-code table for
  four codes, so a small Lua filter sets the current status: `canceled` 499, `deadline_exceeded` 504,
  `failed_precondition` 400 and `unimplemented` 501. Both filters are off for every other route, and neither is written
  when `envoy.connect_bridge` is `false`.
- **Route metadata.** With `auth`, a plain route carries an empty action under `envoy.namespace`, and an RPC route
  carries its method's action. A route with `auth: off` carries none. The auth-unavailable log line reads it. A limited
  route also carries its bucket under `<envoy.namespace>.rate_limit`, which the rate limit filter reads, so a route with
  `auth: off` and a limit carries its bucket alone.
- **Three kinds of JSON log line on standard output.** The request log has the fields `time`, `route`, `host`, `method`,
  `protocol`, `status`, `flags`, `details`, `duration_ms`, `upstream_ms`, `bytes_in`, `bytes_out`, `upstream` and
  `request_id`, and with `auth` also `authz_us`, how long the check took (`null` when none ran). The upstream failure
  line (`event: gateway_upstream_failure`) is written for each request an upstream failed. The auth-unavailable line
  (`event: gateway_auth_unavailable`) is written with `auth` only. No log line holds a path, query, address, user agent
  or referrer, because paths can carry links and tokens. With `log`, the request log writes every response at or above
  `status_from`, every response with a response flag, and `sample` of the rest, chosen by the first eight hex digits of
  `x-request-id`.
- **The order of the filters:** set metadata (with `auth`), header mutation, the RBAC refusal of a foreign browser
  origin, CORS, the rate limit filter (only when a route or the rpc entry has a `rate_limit`), the auth check (with
  `auth`), the Lua status filter and the Connect bridge (with an rpc entry, unless `envoy.connect_bridge` is `false`),
  and the router. A foreign origin and a request over a rate limit are refused before the check could be called.

## Fixed names

These names never follow `envoy.namespace` or any other key, because other programs read them. A tool that changes a
runtime value, for example, looks it up by its key.

| Name | What it is |
|---|---|
| `public` | The listener, its `stat_prefix` and the route table. |
| `gateway.ext_authz.enabled` | The runtime key that switches the auth check, written with `auth`. Its default comes from `auth.enabled`. |
| `gateway.request_log.min_status` | The runtime key for the lowest status that is always logged, written with `log`. Its default comes from `log.status_from`. |
| `gateway.request_log.sample` | The runtime key for the share of other requests that is logged, written with `log`. Its default comes from `log.sample`. |
| `gateway.local_reply.origin_refused_status` | The runtime key for the status the `Invalid CORS request` answer is matched on. Its default is `403`. |
| `gateway.local_reply.rate_limited_status` | The runtime key for the status the Connect answer to a call over a rate limit is matched on, written with a `rate_limit`. Its default is `429`. |
| `rate_limit` | The rate limit filter's `stat_prefix`, written with a `rate_limit`. |
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
