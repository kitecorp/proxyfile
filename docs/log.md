# `log`

`log` decides which requests the request log writes. [Defaults](defaults.md) gives the fields of the request log and
its two other kinds of line.

<!-- keys: log -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `log` | Which requests the request log writes. | A mapping. Without `log`, every request is logged. | core |
| `log.status_from` | The lowest status that is always logged. | Required. A whole number from 100 to 599. | core |
| `log.sample` | The share of the other requests that is logged. | Required. A whole percentage such as 1%. | core |
<!-- /keys -->
