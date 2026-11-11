# `limits`

`limits` caps the gateway's connections and the header block of a request. A size is written as
[the format](format.md#sizes-times-and-percentages) says, and [Defaults](defaults.md) says when the overload manager
runs. The heap budget is [`envoy.heap`](envoy.md#envoyheap), because only Envoy's overload manager has one.

<!-- keys: limits -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `limits` | Limits on connections and request headers. | A mapping. | core |
| `limits.connections` | The most connections at once, from clients and to each service. | A whole number from 1 to 4294967295. | core |
| `limits.request_headers` | The largest header block and the most header fields a request may carry. | A mapping. | core |
| `limits.request_headers.size` | The largest header block of a request. | Required. A size such as 96KiB. At most `8MiB`. | core |
| `limits.request_headers.count` | The most header fields in a request. | Required. A whole number from 1 to 4294967295. | core |
<!-- /keys -->
