# The listener

`listen`, `admin` and `remove_from_clients` are the keys at the top of a proxyfile that hold no keys of their own: the
ports of the gateway's two listeners and the headers it removes from every client request. The namespace of its metadata
is [`envoy.namespace`](envoy.md#envoynamespace). [Defaults](defaults.md) says what every listener gets with no key.

<!-- keys: . -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `listen` | The port of the public listener, on 0.0.0.0. | Required. A whole number from 1 to 65535. | core |
| `admin` | The port of the admin listener, on 127.0.0.1; no admin listener when not written. | A whole number from 1 to 65535. Not the same as `listen`. It answers only `/ready`, `/stats` and `/server_info`. | core |
| `remove_from_clients` | Headers removed from every client request before routing and before any filter; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. No route, filter or service sees a client's copy. Envoy holds header names in lower case, so a name with a capital would never match. | core |
<!-- /keys -->
