# `limits`

`limits` caps the gateway's heap, its connections and the header block of a request. A size is written as
[the format](format.md#sizes-times-and-percentages) says, and [Defaults](defaults.md) says when the overload manager
runs.

<!-- keys: limits -->
| Key | Meaning | Rule |
|---|---|---|
| `limits` | Limits on memory, connections and request headers. | A mapping. |
| `limits.heap` | The heap budget: the gateway shrinks its heap at 90% of it and refuses new requests at 98%. | A size such as 192MiB. |
| `limits.connections` | The most connections at once, from clients and to each service. | A whole number from 1 to 4294967295. |
| `limits.request_headers` | The largest header block and the most header fields a request may carry. | A mapping. |
| `limits.request_headers.size` | The largest header block of a request. | Required. A size such as 96KiB. At most `8MiB`. |
| `limits.request_headers.count` | The most header fields in a request. | Required. A whole number from 1 to 4294967295. |
<!-- /keys -->
