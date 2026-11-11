# `envoy`

The `envoy` block holds the keys only the Envoy generator reads: the namespace of the gateway's metadata, its heap
budget, X-Ray tracing and the Connect bridge. nginx and HAProxy have nothing that matches them
([Portability](portability.md)), so a file written for one of those leaves the block out, and a file written for several
proxies keeps it beside their blocks. proxyfile 1.x read `namespace` and `tracing` at the top of the file and `heap`
under `limits`.

```yaml
envoy:
  namespace: example.gateway
  heap: 256MiB
```

## `envoy.namespace`

The namespace of the gateway's own metadata: the metadata of each route the auth check applies to, and the flag the
auth-unavailable log line reads. It is `proxyfile` when not written. [Defaults](defaults.md#fixed-names) lists the names
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

With `envoy.tracing`, the gateway takes part in an AWS X-Ray trace. It continues the trace that a request's
`x-amzn-trace-id` names, or starts one. It sends a segment for each sampled request to an agent over UDP. It passes the
trace on to the service in `x-amzn-trace-id`, with its own segment as the parent, and the auth check's request carries a
subsegment of it.

```yaml
envoy:
  tracing:
    provider:
      xray:
        agent: 127.0.0.1:2000
        segment: example-gateway
        sample:
          first: 1
          rate: 5%
          rules:
            # A WebSocket's segment would last as long as the socket, so the gateway samples no handshake by its own
            # rule.
            - {host: "*", method: "*", path: "/live*", first: 0, rate: 0%}
    tags:
      environment: {env: APP_ENVIRONMENT, default: local}
      request_id: {header: x-request-id}
    path_in_url: false
```

The same in HOCON:

```hocon
envoy {
  tracing {
    provider {
      xray {
        agent = "127.0.0.1:2000"
        segment = example-gateway
        sample {
          first = 1
          rate = 5%
          // A WebSocket's segment would last as long as the socket, so the gateway samples no handshake by its own
          // rule.
          rules = [
            { host = "*", method = "*", path = "/live*", first = 0, rate = 0% }
          ]
        }
      }
    }
    tags {
      environment { env = APP_ENVIRONMENT, default = local }
      request_id { header = x-request-id }
    }
    path_in_url = false
  }
}
```

[`examples/tracing/`](../examples/tracing/) holds this gateway with `auth` and `remove_from_clients` added, in
[YAML](../examples/tracing/gateway.yaml) and [HOCON](../examples/tracing/gateway.conf), and the
[`envoy.yaml`](../examples/tracing/envoy.yaml) it generates.

Envoy traces a request when a whole number it draws from 0 to 99 is below 100 times the rate, computed in floating
point. 100 times 0.07, 0.14, 0.28, 0.55 and 0.56 comes out a little above the whole number. So `rate: 7%`, `14%`, `28%`,
`55%` and `56%` trace 8%, 15%, 29%, 56% and 57% of the requests they apply to. Every other whole percentage is exact.

In `host`, `method` and `path`, `*` matches any run of characters and `?` one character. Case does not matter, and the
pattern must match the whole value. Each is `*` when not written. A request whose `x-amzn-trace-id` says `Sampled=1` is
traced and one that says `Sampled=0` is not, whatever the rules say. Only the other requests are asked of the rules. So
a client can have every request it sends traced, and X-Ray bills for each trace recorded.

`path_in_url` is false by default, for the reason the request log holds no path: paths can carry links and tokens. A
segment still holds the URL's scheme and host, the method and the user agent. As `client_ip` it holds the whole
`X-Forwarded-For` header as it came, or the connection's address when there is none. It holds the status and the
response size. For a request whose content type is `application/grpc` or `application/grpc+<x>`, it holds the whole path
in the `grpc.path` annotation, and no setting removes that. With `path_in_url: true` the URL keeps the first 256
characters of the path, query included. An environment tag is read by Envoy once, when it starts, never by proxyfile, so
the same proxyfile gives the same bytes on every machine. A tag name is letters, digits and `_`, because X-Ray filter
expressions take only those annotation keys. A tag with the name of a tag Envoy sets on every segment itself is dropped:
Envoy sets its own tags before the custom tags, and the X-Ray tracer keeps the first value of a name. A tag named
`error` whose value is `true` marks the segment as a fault. proxyfile keeps no list of Envoy's tag names.

What is written with the key:

- The provider's fixed parts: `envoy.tracers.xray` and UDP.
- The request id extension with `pack_trace_reason: false`, so `x-request-id` stays as the client sent it or Envoy
  generated it.
- `x-envoy-downstream-service-cluster` and `x-envoy-downstream-service-node`, removed from clients after every other
  early removal, each unless an entry before it covers it: a `remove_from_clients` entry or an `auth.accept` name, the
  rule the question headers follow. Envoy strips them only from an edge request, and with `use_remote_address: false`
  (see [Defaults](defaults.md)) no request is one.
- None of Envoy's own sampling shares, so the rules decide.

With no agent listening, a segment is dropped and the request is served as before. Without the key, none of this is
written.

In HOCON, the agent is quoted, `agent = "127.0.0.1:2000"`, because of its colon (see [Quote what HOCON would read
differently](hocon.md)). `rate = 5%` needs no quotes and is text, while `rate = 5` is a number and is refused.

<!-- keys: envoy -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `envoy` | The settings only the Envoy generator reads: the namespace of the gateway's metadata, its heap budget, X-Ray tracing and the Connect bridge. | A mapping. | envoy |
| `envoy.namespace` | The namespace of the gateway's own metadata, which the routes and the request log use; proxyfile when not written. | Lower-case letters, digits, dots, - and \_, starting with a letter. | envoy |
| `envoy.heap` | The heap budget: the gateway shrinks its heap at 90% of it and refuses new requests at 98%. | A size such as 192MiB. | envoy |
| `envoy.tracing` | An X-Ray segment for each sampled request, sent to an agent over UDP. | A mapping. | envoy |
| `envoy.tracing.provider` | The tracing system the segments go to: a mapping with one key that names it. | Required. A mapping with the one key xray. | envoy |
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
| `envoy.tracing.tags` | Annotations on each segment, by name. | A mapping keyed by tag name. Written order is kept, in both forms. | envoy |
| `envoy.tracing.tags.<tag name>` | One tag: the environment variable or the request header it reads, and an optional default. | A mapping. The tag name is letters, digits and \_. | envoy |
| `envoy.tracing.tags.<tag name>.env` | An environment variable of the gateway's process, read once when Envoy starts. | A variable name: letters, digits and \_, not starting with a digit. Exactly one of `env` and `header` is written. | envoy |
| `envoy.tracing.tags.<tag name>.header` | A request header. | A lower-case header name. Exactly one of `env` and `header` is written. | envoy |
| `envoy.tracing.tags.<tag name>.default` | The value when the variable is unset or the header is absent; without it the segment gets no tag then. | One character or more. | envoy |
| `envoy.tracing.path_in_url` | true keeps the request's path in the segment's URL, its first 256 characters; false, the default, keeps the scheme and the host only. | `true` or `false`. | envoy |
| `envoy.connect_bridge` | true turns each Connect call on an RPC route into gRPC for the service, and the answer back into Connect; false routes gRPC calls alone. true when not written. | `true` or `false`. `false` also leaves out the Lua filter that corrects the status of the bridge's errors. | envoy |
<!-- /keys -->
