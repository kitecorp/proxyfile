# The listener

`listen`, `admin`, `client_address` and `remove_from_clients` are the keys at the top of a proxyfile that hold no keys
of their own: the ports of the gateway's two listeners, which address counts as the client's, and the headers it
removes from every client request. HTTPS on the public listener is [`tls`](tls.md). The namespace of its metadata is
[`envoy.namespace`](envoy.md#envoynamespace). [Defaults](defaults.md) says what every listener gets with no key.

<!-- keys: . -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `listen` | The port of the public listener, on 0.0.0.0. | Required. A whole number from 1 to 65535. | core |
| `admin` | The port of the admin listener, on 127.0.0.1; no admin listener when not written. | A whole number from 1 to 65535. Not the same as `listen`. It answers only `/ready`, `/stats` and `/server_info`. | core |
| `client_address` | Which address counts as the client's: forwarded, the last address a proxy in front appended to X-Forwarded-For, or connection, the peer of the TCP connection. | `forwarded` or `connection`. When not written: `connection` in a file that writes `tls`, `forwarded` otherwise. A rate limit by client and `hash.client` read it. | core |
| `remove_from_clients` | Headers removed from every client request before routing and before any filter; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. No route, filter or service sees a client's copy. Envoy holds header names in lower case, so a name with a capital would never match. | core |
<!-- /keys -->

## The client's address

`client_address` says which address the gateway takes as the client's. `hash.client` and a
[rate limit](routes.md#rate-limits) by client read that address.

- `forwarded` takes the last address in `X-Forwarded-For`, the one a proxy in front of the gateway appended. The
  gateway passes `X-Forwarded-For` on as it came, and keeps `X-Forwarded-Proto` when the request has one. This is right
  only when every client reaches the gateway through that proxy: a client that reaches the gateway directly writes the
  header itself, and so chooses its own address and scheme.
- `connection` takes the peer of the TCP connection. Envoy appends that address to `X-Forwarded-For`, sets
  `X-Forwarded-Proto` from the connection, and on a request it counts as external replaces the client's `x-request-id`
  and removes the client's own `x-envoy-` headers. It also adds `x-envoy-external-address`, the client's address, to the
  request the service receives, in place of a client's own copy. Measured on Envoy 1.40.2: a client's forged
  `X-Forwarded-For` does not change its address with `connection`, and does with `forwarded`.

When not written, `client_address` is `connection` in a file that writes [`tls`](tls.md), because a gateway that
terminates TLS usually faces its clients, and `forwarded` otherwise, as in every 2.0 file. A gateway that faces its
clients over plain HTTP writes `client_address: connection`, and one with `tls` behind a proxy that sets
`X-Forwarded-For` writes `client_address: forwarded`.
