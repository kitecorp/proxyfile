# `routes`

`routes` lists the routes in the order they are tried, with the rpc entry among them marking where the RPC routes go.

How routes are matched:

- The routes are tried in the order written, and the first that matches wins. Every environment gets the same list. Put
  a `prefix: /` route last, because it matches every path.
- Write exactly one of `prefix`, `path` and `pattern` on each route.
- Two routes with the same kind of match, the same text and the same `method` are a problem, because the later one is
  never reached.
- A retry happens only when the request never reached the service (Envoy's `connect-failure` and
  `reset-before-request` conditions), so a request that the service saw is never sent twice.

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
| `routes[].auth` | off sends the route's requests to the service without the check. | `off`. Needs an `auth` key. | core |
| `routes[].retries` | How many times a request that never reached the service is tried again. | A whole number from 0 to 4294967295. | core |
<!-- /keys -->
