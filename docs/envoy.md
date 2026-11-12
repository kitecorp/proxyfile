# `envoy`

The `envoy` block holds the keys only the Envoy generator reads: the namespace of the gateway's metadata, its heap
budget, the old place of tracing and the Connect bridge. nginx and HAProxy have nothing that matches them
([Portability](portability.md)), so a file written for one of those leaves the block out, and a file written for several
proxies keeps it beside their blocks. proxyfile 1.x read `namespace` and `tracing` at the top of the file and `heap`
under `limits`; from 2.1 `tracing` is read at the top again.

```yaml
envoy:
  namespace: example.gateway
  heap: 256MiB
```

## `envoy.namespace`

The namespace of the gateway's own metadata: the metadata of each route the auth check applies to, and the flag the
auth-unavailable log line reads. It is `proxyfile` when not written. A limited route's bucket is under
`<namespace>.rate_limit` ([Rate limits](routes.md#rate-limits)). [Defaults](defaults.md#fixed-names) lists the names
that never follow it.

## `envoy.heap`

The heap budget of Envoy's overload manager: at 90% of it the gateway shrinks its heap, and at 98% it refuses new
requests. A size is written as [the format](format.md#sizes-times-and-percentages) says, and [Defaults](defaults.md)
says how often the overload manager checks.

## `envoy.connect_bridge`

`true`, the default, makes each RPC route take Connect calls as well as gRPC ones: Envoy's Connect-gRPC bridge turns a
Connect call into gRPC for the service and the answer back into Connect, and a small Lua filter sets the current Connect
status on the bridge's errors ([Defaults](defaults.md)). `false` routes gRPC calls alone: each RPC route sends a call to
the service's gRPC port with neither filter, as nginx and HAProxy would, so a Connect call reaches the service as it
came. [`examples/grpc`](../examples/grpc/) writes `connect_bridge: false`.

## `envoy.tracing`

`envoy.tracing` is the old place of [`tracing`](tracing.md), which every 2.x release reads with the `xray` provider and
3.0 removes. It renders the same bytes as the same keys written under `tracing`, and a file that writes both is refused.
[`examples/tracing/envoy-tracing/proxyfile.yaml`](../examples/tracing/envoy-tracing/proxyfile.yaml) writes the tracing
example at this place. [Tracing](tracing.md) says what each key does.

<!-- keys: envoy -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `envoy` | The settings only the Envoy generator reads: the namespace of the gateway's metadata, its heap budget, the old place of tracing and the Connect bridge. | A mapping. | envoy |
| `envoy.namespace` | The namespace of the gateway's own metadata, which the routes and the request log use; proxyfile when not written. | Lower-case letters, digits, dots, - and \_, starting with a letter. | envoy |
| `envoy.heap` | The heap budget: the gateway shrinks its heap at 90% of it and refuses new requests at 98%. | A size such as 192MiB. | envoy |
| `envoy.tracing` | The old place of tracing, read in every 2.x release with the xray provider only; 3.0 removes it. | A mapping. Not with `tracing`. | envoy |
| `envoy.tracing.provider` | The tracing system the spans go to: a mapping with one key that names it. | Required. A mapping with the one key xray. | envoy |
| `envoy.tracing.provider.xray` | AWS X-Ray: where the segments go, their name and which requests are traced. | A mapping. | envoy |
| `envoy.tracing.provider.xray.agent` | Where the segments go over UDP: an X-Ray daemon, or an agent that takes X-Ray segments; 127.0.0.1:2000 when not written. | An IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000. | envoy |
| `envoy.tracing.provider.xray.segment` | The name of the gateway's segments, which names its node in X-Ray's service map. | Required. 1 to 200 characters: letters, digits, spaces and \_ . : / % & # = + \\ - @. | envoy |
| `envoy.tracing.provider.xray.sample` | Which requests are traced when the request does not say: the rules, then the default. Without it, Envoy traces the first request each second and 5% of the others. | A mapping. Without it no manifest is written and Envoy's own rule applies, which is `first: 1, rate: 5%`. | envoy |
| `envoy.tracing.provider.xray.sample.first` | How many requests each second are traced before rate applies; 1 when not written. | A whole number from 0 to 4294967295. | envoy |
| `envoy.tracing.provider.xray.sample.rate` | The share of the other requests that is traced; 5% when not written. | A whole percentage such as 5%. From `0%` to `100%`, the form of `log.sample`. | envoy |
| `envoy.tracing.provider.xray.sample.rules` | Rules tried in the order written, before the default; the first that matches a request decides. | A list; each item is a mapping. It may be empty. Each rule decides with its own `first` and `rate`. | envoy |
| `envoy.tracing.provider.xray.sample.rules[].host` | Matches the Host header, with its port when the request has one; \* when not written. | A pattern of letters, digits, ., -, :, \[, \], \* and ?. | envoy |
| `envoy.tracing.provider.xray.sample.rules[].method` | Matches the request's method; \* when not written. | A method in capitals, or a pattern of capitals, \* and ?. | envoy |
| `envoy.tracing.provider.xray.sample.rules[].path` | Matches the request's path, with its query; \* when not written. | A pattern of printable characters without spaces, starting with / or \*. | envoy |
| `envoy.tracing.provider.xray.sample.rules[].first` | How many matching requests each second are traced before rate applies. | Required. A whole number from 0 to 4294967295. | envoy |
| `envoy.tracing.provider.xray.sample.rules[].rate` | The share of the other matching requests that is traced. | Required. A whole percentage such as 5%. From `0%` to `100%`, the form of `log.sample`. | envoy |
| `envoy.tracing.tags` | Tags on each span, by name. | A mapping keyed by tag name. Written order is kept, in both forms. | envoy |
| `envoy.tracing.tags.<tag name>` | One tag: the environment variable or the request header it reads, and an optional default. | A mapping. The tag name is letters, digits and \_. | envoy |
| `envoy.tracing.tags.<tag name>.env` | An environment variable of the gateway's process, read once when Envoy starts. | A variable name: letters, digits and \_, not starting with a digit. Exactly one of `env` and `header` is written. | envoy |
| `envoy.tracing.tags.<tag name>.header` | A request header. | A lower-case header name. Exactly one of `env` and `header` is written. | envoy |
| `envoy.tracing.tags.<tag name>.default` | The value when the variable is unset or the header is absent; without it the span gets no tag then. | One character or more. | envoy |
| `envoy.tracing.path_in_url` | true keeps the request's path in the span's URL, its first 256 characters; false, the default, keeps the scheme and the host only. | `true` or `false`. | envoy |
| `envoy.connect_bridge` | true turns each Connect call on an RPC route into gRPC for the service, and the answer back into Connect; false routes gRPC calls alone. true when not written. | `true` or `false`. `false` also leaves out the Lua filter that corrects the status of the bridge's errors. | envoy |
<!-- /keys -->
