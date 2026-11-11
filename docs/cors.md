# `cors`

`cors` holds the CORS settings every environment shares. [origins](origins.md) says how the gateway answers a browser
with them, and when `origins` names a file, that file holds the settings instead.

<!-- keys: cors -->
| Key | Meaning | Rule | Class |
|---|---|---|---|
| `cors` | The CORS settings every environment shares. | A mapping. Not written when `origins` names a file, which holds its own `cors`. | core |
| `cors.allow_methods` | The value of Access-Control-Allow-Methods. | Text. | core |
| `cors.allow_headers` | The value of Access-Control-Allow-Headers. | Text. | core |
| `cors.expose_headers` | The value of Access-Control-Expose-Headers. | Text. | core |
| `cors.max_age` | The value of Access-Control-Max-Age: how long a browser may keep a preflight answer. | A number of seconds. Written as text. | core |
<!-- /keys -->
