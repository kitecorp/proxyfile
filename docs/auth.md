# `auth`

`auth` is not an auth service. It makes the proxy ask a service of yours "may this request pass?" before it forwards
the request, and copy the identity headers your service answers onto the request. Your service decides, and the proxy
acts on the answer. Envoy and nginx have this built in, as Envoy's `ext_authz` and nginx's `auth_request`, and HAProxy
has it through an add-on ([Portability](portability.md)).

```yaml
services:
  accounts: {http: 7070}   # your service that answers the question
auth:
  ask: accounts /check     # the service and the path the proxy asks about each request
  timeout: 2s              # how long the proxy waits for the answer
  enabled: true            # true runs the check; false turns it off for every request
  when_unavailable: deny   # deny refuses a request the check could not answer; allow lets it through
  accept: [x-user-id]      # the headers of the answer copied onto the request, such as who the user is
```

The check is a plain HTTP request to your service, so your service needs nothing made for one proxy. With the envoy
target it is Envoy's `ext_authz` in HTTP mode. How the check works:

- The check gets a request to the path of `ask`, never the client's path or query, because paths can carry links and
  tokens. The headers it gets are the ones `send` names and the route's question below. HTTP mode adds the client's
  `Host`, the request's method, `Authorization` and `Content-Length: 0`, whatever `send` says.
- Each RPC route adds its question to the check: `x-gateway-action` holds the method's `action` and `x-gateway-reports`
  its `reports` joined by commas. A plain route adds neither. The question is for the check alone, and no service sees
  it.
- `x-gateway-timeout-ms` holds the deadline in milliseconds, the same as `auth.timeout`, so the auth service can keep
  its own calls inside it.
- The service answers `200` to let the request through. The headers of the answer named by `accept` are set on the
  request to the service, replacing any value there. A `5xx` counts as a failed call, which `when_unavailable` decides.
  Any other status is sent to the client as a denial.
- With `when_unavailable: allow`, a request that could not be checked goes through with the header
  `x-envoy-auth-failure-mode-allowed` set, and the gateway writes a `gateway_auth_unavailable` log line for it. A
  client's own copy of that header is removed before anything reads it.
- A check sent on a connection the auth service is closing fails. Set `timeouts.upstream_idle` under the auth service's
  own idle timeout, so the gateway closes an idle connection first.

<!-- keys: auth -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `auth` | The external check every request goes through before it reaches a service. | A mapping. Without `auth` there is no check. | core |
| `auth.ask` | The service and the path the gateway asks. | Required. A service and a path, such as accounts /check. The service needs an `http` port. | core |
| `auth.timeout` | How long the gateway waits for the answer. | Required. A time such as 2s. At most `4294967295s`. `none` is not accepted. | core |
| `auth.enabled` | true runs the check on every request it applies to, false on none. | Required. `true` or `false`. | core |
| `auth.when_unavailable` | What happens to a request when the check cannot be asked: allow lets it through, deny refuses it. | Required. `allow` or `deny`. | core |
| `auth.only_with_header` | The check runs only on requests that carry this header. | A lower-case header name. A request without it, or with it empty, goes through with no check. | core |
| `auth.send` | The request headers the check is sent; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. | core |
| `auth.accept` | The headers of the check's answer that reach the service; a name ending in \* is a prefix. | A list; each item is a lower-case header name, or a prefix ending in \*. | core |
<!-- /keys -->
