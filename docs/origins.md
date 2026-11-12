# `origins`

`origins` gives, for each environment, the hosts it serves and the browser origins it admits; or, as in 1.0, it names
an origins file that holds them.

Each environment becomes one virtual host. It serves the environment's hosts, and the port in the Host header plays no
part, so a local setup may reach the gateway on any port. A browser request is admitted when:

- it carries no `Origin` header, or
- its `Origin` is one of the environment's own hosts, on either scheme and any port, or
- its `Origin` is in the environment's `origins` or matches one of its `patterns`.

Any other browser origin is refused with `403`, preflights, requests and WebSocket upgrades alike, before CORS, the auth
check or any service sees it. For an admitted origin the gateway answers every preflight itself, so none reaches a
service or the check. On every other answer it sets `Access-Control-Allow-Origin`, and `Access-Control-Expose-Headers`
when `expose_headers` is written, replacing any the service sent. The `max_age` lets a browser keep an admitted
preflight, so an origin removed from the file may keep working that long in a browser that cached its preflight.

Each list is sorted in byte order and holds no value twice, so adding or removing a value changes one line.

<!-- keys: origins -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `origins` | The browser origins each environment admits, by environment name; or, as in 1.0, the name of an origins file, relative to this file. | Required. A mapping of environments by name, or a file name. At least one environment. | core |
| `origins.<environment name>` | One environment: its hosts and the browser origins it admits. | A mapping. The environment name is one character or more. | core |
| `origins.<environment name>.hosts` | The host names the environment serves, without a port. | Required. A list; each item is text. Not empty. Lower-case host names, such as `api.example.com`. No host is in two environments. | core |
| `origins.<environment name>.origins` | The browser origins admitted exactly, each scheme://host\[:port\]. | A list; each item is text. Lower case, `http` or `https`, no path. | core |
| `origins.<environment name>.patterns` | The browser origins admitted by pattern, where \* matches any run of characters. | A list; each item is text. A `*` matches dots too. Each is `scheme://host[:port]` with at least one `*`. | core |
<!-- /keys -->

## The origins file

When `origins` names a file, a relative path is read from the proxyfile's directory, and an absolute path as written.
Written in the proxyfile, `origins` is instead a mapping from each environment's name to its `hosts`, `origins` and
`patterns`, and `cors` is a key of its own beside it, as
[`examples/full/yaml/proxyfile.yaml`](../examples/full/yaml/proxyfile.yaml) shows. Every key and rule below holds in
both places, with two differences. The file writes an environment's name as `name`, and the proxyfile as the key. A
HOCON proxyfile merges an environment written twice, where the file and the YAML form report it. An origins file looks
like this:

```json
{
  "cors": {
    "allow_methods": "*",
    "allow_headers": "*",
    "expose_headers": "Content-Disposition,X-Request-Id",
    "max_age": "600"
  },
  "environments": [
    {
      "name": "live",
      "hosts": ["api.example.com", "api.example.org"],
      "origins": ["https://example.com", "https://www.example.com"],
      "patterns": ["https://*.example.com"]
    },
    {
      "name": "test",
      "hosts": ["api.test.example.com"],
      "origins": ["http://localhost:3000", "https://test.example.com"],
      "patterns": ["https://*.test.example.com"]
    },
    {
      "name": "local",
      "hosts": ["127.0.0.1", "localhost"],
      "origins": [],
      "patterns": ["http://*.example.com:*", "http://localhost:*"]
    }
  ]
}
```

<!-- keys: origins file -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `cors` | The CORS settings every environment shares. | A mapping. | core |
| `cors.allow_methods` | The value of Access-Control-Allow-Methods. | Text. | core |
| `cors.allow_headers` | The value of Access-Control-Allow-Headers. | Text. | core |
| `cors.expose_headers` | The value of Access-Control-Expose-Headers. | Text. | core |
| `cors.max_age` | The value of Access-Control-Max-Age: how long a browser may keep a preflight answer. | A number of seconds. Written as text. | core |
| `environments` | The environments, each with its hosts and the browser origins it admits. | Required. A list; each item is a mapping. At least one environment. | core |
| `environments[].name` | The environment's name. | Required. Text. It names the environment's virtual host. Not empty, and not used by another environment. | core |
| `environments[].hosts` | The host names the environment serves, without a port. | Required. A list; each item is text. Not empty. Lower-case host names, such as `api.example.com`. No host is in two environments. | core |
| `environments[].origins` | The browser origins admitted exactly, each scheme://host\[:port\]. | A list; each item is text. Lower case, `http` or `https`, no path. | core |
| `environments[].patterns` | The browser origins admitted by pattern, where \* matches any run of characters. | A list; each item is text. A `*` matches dots too. Each is `scheme://host[:port]` with at least one `*`. | core |
<!-- /keys -->
