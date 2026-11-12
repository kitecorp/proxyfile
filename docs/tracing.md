# `tracing`

With `tracing`, the gateway takes part in a trace: it sends a span for each sampled request to the tracing system that
`provider` names, and passes the trace on to the service. The provider is [`xray`](#x-ray), AWS X-Ray, or
[`otel`](#opentelemetry), an OpenTelemetry collector. `tracing` is a key every proxy can carry
([Portability](portability.md)). proxyfile 1.x read it here too. 2.0 read it at `envoy.tracing`, its old place, which
every 2.x release still reads with the `xray` provider and 3.0 removes ([`envoy`](envoy.md#envoytracing)). A file that
writes both `tracing` and `envoy.tracing` is refused.

<!-- keys: tracing -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `tracing` | A trace span for each sampled request, sent to AWS X-Ray or to an OpenTelemetry collector. | A mapping. Not with `envoy.tracing`. | core |
| `tracing.provider` | The tracing system the spans go to: a mapping with one key that names it. | Required. A mapping with one key, xray or otel. | core |
| `tracing.provider.xray` | AWS X-Ray: where the segments go, their name and which requests are traced. | A mapping. | core |
| `tracing.provider.xray.agent` | Where the segments go over UDP: an X-Ray daemon, or an agent that takes X-Ray segments; 127.0.0.1:2000 when not written. | An IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000. | core |
| `tracing.provider.xray.segment` | The name of the gateway's segments, which names its node in X-Ray's service map. | Required. 1 to 200 characters: letters, digits, spaces and \_ . : / % & # = + \\ - @. | core |
| `tracing.provider.xray.sample` | Which requests are traced when the request does not say: the rules, then the default. Without it, Envoy traces the first request each second and 5% of the others. | A mapping. Without it no manifest is written and Envoy's own rule applies, which is `first: 1, rate: 5%`. | core |
| `tracing.provider.xray.sample.first` | How many requests each second are traced before rate applies; 1 when not written. | A whole number from 0 to 4294967295. | core |
| `tracing.provider.xray.sample.rate` | The share of the other requests that is traced; 5% when not written. | A whole percentage such as 5%. From `0%` to `100%`, the form of `log.sample`. | core |
| `tracing.provider.xray.sample.rules` | Rules tried in the order written, before the default; the first that matches a request decides. | A list; each item is a mapping. It may be empty. Each rule decides with its own `first` and `rate`. | core |
| `tracing.provider.xray.sample.rules[].host` | Matches the Host header, with its port when the request has one; \* when not written. | A pattern of letters, digits, ., -, :, \[, \], \* and ?. | core |
| `tracing.provider.xray.sample.rules[].method` | Matches the request's method; \* when not written. | A method in capitals, or a pattern of capitals, \* and ?. | core |
| `tracing.provider.xray.sample.rules[].path` | Matches the request's path, with its query; \* when not written. | A pattern of printable characters without spaces, starting with / or \*. | core |
| `tracing.provider.xray.sample.rules[].first` | How many matching requests each second are traced before rate applies. | Required. A whole number from 0 to 4294967295. | core |
| `tracing.provider.xray.sample.rules[].rate` | The share of the other matching requests that is traced. | Required. A whole percentage such as 5%. From `0%` to `100%`, the form of `log.sample`. | core |
| `tracing.provider.otel` | OpenTelemetry: spans sent as OTLP over gRPC to a collector. | A mapping. | core |
| `tracing.provider.otel.endpoint` | Where the spans go, as plain OTLP over gRPC: a collector beside the gateway or in its network. | Required. An IP address or a DNS name, and a port, such as 127.0.0.1:4317 or \[fd00::40\]:4317. No TLS and no credentials. A name is looked up every 5 seconds, as a service's address is. An IPv6 address is written in brackets. | core |
| `tracing.provider.otel.service` | The service.name the spans carry, which names the gateway in the tracing backend. | Required. 1 to 200 characters: letters, digits, spaces and \_ . : / % & # = + \\ - @. | core |
| `tracing.provider.otel.sample` | Which requests are traced; one whose traceparent says it is not sampled never is. | A mapping. `first` and `rules` are X-Ray's sampling and are not taken here. | core |
| `tracing.provider.otel.sample.rate` | The share of the requests that is traced; 5% when not written. | A whole percentage such as 5%. From `0%` to `100%`, the form of `log.sample`. | core |
| `tracing.tags` | Tags on each span, by name. | A mapping keyed by tag name. Written order is kept, in both forms. | core |
| `tracing.tags.<tag name>` | One tag: the environment variable or the request header it reads, and an optional default. | A mapping. The tag name is letters, digits and \_. | core |
| `tracing.tags.<tag name>.env` | An environment variable of the gateway's process, read once when Envoy starts. | A variable name: letters, digits and \_, not starting with a digit. Exactly one of `env` and `header` is written. | core |
| `tracing.tags.<tag name>.header` | A request header. | A lower-case header name. Exactly one of `env` and `header` is written. | core |
| `tracing.tags.<tag name>.default` | The value when the variable is unset or the header is absent; without it the span gets no tag then. | One character or more. | core |
| `tracing.path_in_url` | true keeps the request's path in the span's URL, its first 256 characters; false, the default, keeps the scheme and the host only. | `true` or `false`. | core |
<!-- /keys -->

## X-Ray

With the `xray` provider, the gateway takes part in an AWS X-Ray trace. X-Ray propagates the trace in the
`x-amzn-trace-id` header. The gateway continues the trace that a request's `x-amzn-trace-id` names, or starts one. It
sends a segment for each sampled request to an agent over UDP. It passes the trace on to the service in
`x-amzn-trace-id`, with its own segment as the parent, and the auth check's request carries a subsegment of it.

```yaml
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
```

[`examples/tracing/`](../examples/tracing/) holds this gateway with `auth` and `remove_from_clients` added, in
[YAML](../examples/tracing/yaml/proxyfile.yaml) and [HOCON](../examples/tracing/hocon/Proxyfile), each beside the
[`envoy.yaml`](../examples/tracing/yaml/envoy.yaml) it generates.
[`envoy-tracing/proxyfile.yaml`](../examples/tracing/envoy-tracing/proxyfile.yaml) writes the same keys at
`envoy.tracing` and gives the same `envoy.yaml`.

Envoy traces a request when a whole number it draws from 0 to 99 is below 100 times the rate, computed in floating
point. 100 times 0.07, 0.14, 0.28, 0.55 and 0.56 comes out a little above the whole number. So `rate: 7%`, `14%`, `28%`,
`55%` and `56%` trace 8%, 15%, 29%, 56% and 57% of the requests they apply to. Every other whole percentage is exact.

In `host`, `method` and `path`, `*` matches any run of characters and `?` one character. Case does not matter, and the
pattern must match the whole value. Each is `*` when not written. A request whose `x-amzn-trace-id` says `Sampled=1` is
traced and one that says `Sampled=0` is not, whatever the rules say. Only the other requests are asked of the rules. So
a client can have every request it sends traced, and X-Ray bills for each trace recorded.

`path_in_url` is false by default, for the reason the request log holds no path: paths can carry links and tokens. A
segment still holds the URL's scheme and host, the method and the user agent. As `client_ip` it holds the whole
`X-Forwarded-For` header, as it came with `client_address: forwarded`, or the connection's address when there is none.
It holds the status and the response size. For a request whose content type is `application/grpc` or
`application/grpc+<x>`, it holds the whole path in the `grpc.path` annotation, and no setting removes that. With
`path_in_url: true` the URL keeps the first 256 characters of the path, query included. An environment tag is read by
Envoy once, when it starts, never by proxyfile, so the same proxyfile gives the same bytes on every machine. A tag name
is letters, digits and `_`, because X-Ray filter expressions take only those annotation keys. A tag with the name of a
tag Envoy sets on every segment itself is dropped: Envoy sets its own tags before the custom tags, and the X-Ray tracer
keeps the first value of a name. A tag named `error` whose value is `true` marks the segment as a fault. proxyfile keeps
no list of Envoy's tag names.

What the Envoy generator writes with the key:

- The provider's fixed parts: `envoy.tracers.xray` and UDP.
- The request id extension with `pack_trace_reason: false`, so `x-request-id` stays as the client sent it or Envoy
  generated it.
- `x-envoy-downstream-service-cluster` and `x-envoy-downstream-service-node`, removed from clients after every other
  early removal, each unless an entry before it covers it: a `remove_from_clients` entry, an `auth.accept` name or an
  `auth.jwt.claims` header, the rule the question headers follow. Envoy strips them only from an edge request, and with
  `client_address: forwarded` (`use_remote_address: false`, see [Defaults](defaults.md)) no request is one.
- None of Envoy's own sampling shares, so the rules decide.

With no agent listening, a segment is dropped and the request is served as before. Without the key, none of this is
written.

In HOCON, the agent is quoted, `agent = "127.0.0.1:2000"`, because of its colon (see [Quote what HOCON would read
differently](hocon.md)). `rate = 5%` needs no quotes and is text, while `rate = 5` is a number and is refused.

## OpenTelemetry

With the `otel` provider, the gateway sends its spans as OTLP over gRPC to an OpenTelemetry collector, with no TLS
and no credentials. A hosted tracing backend that needs TLS and an API key is reached through a collector, which
holds them. Each span is named after the route the request took, where Envoy would name every span `ingress`; a
request no route takes keeps that name. Its `service.name` is `service`. The auth check's request gets a span of its
own.

```yaml
tracing:
  provider:
    otel:
      endpoint: otel-collector:4317
      service: example-gateway
      sample: {rate: 10%}
  tags:
    environment: {env: APP_ENVIRONMENT, default: local}
    request_id: {header: x-request-id}
  path_in_url: false
```

The same in HOCON, with the endpoint quoted because of its colon:

```hocon
tracing {
  provider {
    otel {
      endpoint = "otel-collector:4317"
      service = example-gateway
      sample { rate = 10% }
    }
  }
  tags {
    environment { env = APP_ENVIRONMENT, default = local }
    request_id { header = x-request-id }
  }
  path_in_url = false
}
```

[`examples/otel/`](../examples/otel/) holds this gateway in [YAML](../examples/otel/yaml/proxyfile.yaml) and
[HOCON](../examples/otel/hocon/Proxyfile), each beside the [`envoy.yaml`](../examples/otel/yaml/envoy.yaml) it
generates. Its collector is found by name, such as a Compose service; the name is asked of
[`dns.resolver`](services.md#name-lookups) when it is written, as a service's address is.

The trace travels in the W3C `traceparent` header. The service receives the gateway's span in `traceparent`, and a
client's `traceparent` is continued: the gateway's span joins the client's trace as a child of the client's span.
`tracestate` passes unchanged. A client can always stop a trace: a `traceparent` that says "not sampled" gives no
span, and neither does a malformed one, which reaches the service as the client sent it. A `traceparent` that says
"sampled" does not force a trace, and neither does `x-client-trace-id`: the draw at `sample.rate` decides. Where
X-Ray's `rate: 7%` traces 8% of the requests, this one traces 7%.

Envoy takes the draw from the request's `x-request-id`, and a request whose id is not a UUID is never traced by the
draw. With `client_address: connection` Envoy writes the id itself, so no client chooses the draw. With
`client_address: forwarded`, the default without `tls`, the id is the client's own when it sent one, so a client that
chooses its id chooses the draw: measured on Envoy 1.40.2 at `rate: 10%`, 40 of 40 requests with one chosen id were
traced and 0 of 40 with another. A tracing backend bills for the spans it stores, so have the proxy in front replace a
client's `x-request-id`.

Tags are span attributes. A tag named like one of the attributes Envoy sets on every span replaces Envoy's value,
where X-Ray keeps Envoy's. `path_in_url` works as with X-Ray: with `false`, the default, the `http.url` attribute holds
the scheme and the host only, and a gRPC call's path, with its query, still stays in the `grpc.path` attribute.

With either provider, a tag with `header` copies that header's value, as the request carried it, into the span of
every sampled request, and `path_in_url: true` does the same for the path and its query. So name no header that holds
a secret, such as `authorization` or `cookie`: the tracing backend would store it.

With the collector down, every request is still answered, no slower than before, and the spans of that time are lost.
Each behaviour above was measured on Envoy 1.40.2.

What the Envoy generator writes with the key:

- The tracer `envoy.tracers.opentelemetry`, its `service_name`, and the cluster `otel_collector` it sends to over
  HTTP/2, after the services' clusters: `STATIC` for an IP address, `STRICT_DNS` for a name.
- `random_sampling` at `sample.rate`, and `client_sampling` at 0, so a client's `x-client-trace-id` does not force a
  trace, which with Envoy's default it does at every rate.
- A `decorator` on each route, which names its span after the route, with `propagate: false`, so the route's name is
  not sent to the client in an `x-envoy-decorator-operation` response header.
- `x-envoy-decorator-operation`, removed from clients after the two downstream headers, unless an entry before it
  covers it: with it a client could rename the span.
- The request id extension with `pack_trace_reason: false`, as with X-Ray; the sampling above was measured with it.
