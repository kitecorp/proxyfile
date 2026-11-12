# `timeouts`

`timeouts` sets how long a request, an idle request and an idle connection to a service may last. A time is written
as [the format](format.md#sizes-times-and-percentages) says, and [the auth check](auth.md) says how `upstream_idle`
must compare with an auth service's own idle timeout.

<!-- keys: timeouts -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `timeouts` | How long a request, an idle request and an idle connection to a service may last. | A mapping. | core |
| `timeouts.request` | The longest a whole request may take. | A time such as 125s, or none. Without it, the Envoy configuration keeps Envoy's default of 15 seconds, counted from the end of the request. `none` removes the limit. | core |
| `timeouts.idle` | The longest a request may go without sending or receiving data. | A time such as 125s, or none. | core |
| `timeouts.upstream_idle` | The longest a connection to a service is kept open with no request on it. | A time such as 125s, or none. Without it, the Envoy configuration keeps Envoy's default of 1 hour. | core |
<!-- /keys -->
