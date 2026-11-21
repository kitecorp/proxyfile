# nginx

`proxyfile render --target nginx` writes `nginx.conf`, a whole nginx configuration, from the same proxyfile as Envoy's,
and `proxyfile.js` beside it. The two go together: `nginx.conf` loads nginx's njs module and imports `proxyfile.js` from
its own folder, and nginx refuses to load the configuration when the script or the module's file is missing (measured
on nginx 1.30.6 and 1.31.8). The official nginx images carry njs; a distribution's nginx may need its njs package
installed. Line 2 of `nginx.conf` names the version it needs, njs 1.0.2 or later, the one measured. With `auth.ask` the file
loads njs's stream module too, which the official images carry ([The auth check](#the-auth-check)). `proxyfile check`
compares both files.

`proxyfile.js` reads what nginx's own variables cannot give the file. It reads the name and the value of every request
header, and `nginx.conf` answers `400` when a name is outside RFC 9110's token characters, when a value holds a control
byte other than a tab, when a name holds `_` and nginx reads it as one of its own headers, or when a second
`X-Forwarded-For`, `X-Forwarded-Proto`, `X-Request-Id`, `Content-Type`, `User-Agent` or `Referer` line comes: the
section [The client's address and removed headers](#the-clients-address-and-removed-headers) says why for the names with
`_` and the second lines, and [What nginx writes with no key](#what-nginx-writes-with-no-key) for the values. It also
reads the lines of `Origin`, which it joins as the Envoy output does, as [The browser origins an environment
admits](#the-browser-origins-an-environment-admits) describes. It counts the header fields of an HTTP/2 request against
`limits.request_headers.count` ([The header limits](#the-header-limits)), and tells an `X-Forwarded-Proto` line that is
present and empty from none ([The client's address and removed headers](#the-clients-address-and-removed-headers)).
With `auth.ask` it also asks the check service and reads its answer ([The auth check](#the-auth-check)). With a rate
limit or a hash keyed by the client under `client_address: forwarded` it reads the client from `X-Forwarded-For` as
Envoy reads it, with `hash.header` it reads the lines of the hashed header, and with a rate limit it tells which form of
answer a request over the limit gets ([Rate limits](#rate-limits), [Backends: balance, hash and DNS
names](#backends-balance-hash-and-dns-names)).

nginx's column in the generated table on [Portability](portability.md#support-by-proxy-from-the-generators) says how
this version writes each key and, for a key it refuses, what to write instead. A key it does not write for nginx, or a
value of a form it does not write, is refused at render by the key's name where the file writes it, so a file that needs
one is never rendered as if the key were not there; the one exception is `health`'s `path`, `grpc`, `interval`,
`timeout` and `healthy`, which are accepted and not read ([Passive health checks](#passive-health-checks)). A value is
refused in the same way, by its key, when its nginx form is longer than nginx reads: 4094 bytes between quotes, or 4095
without them, and one byte less where a space follows the value, as after a map's key or a header's name (measured on
nginx 1.30.6 and 1.31.8). `auth.jwt` is refused because open-source nginx verifies no token: `auth_jwt` is NGINX Plus's
([ngx_http_auth_jwt_module](https://nginx.org/en/docs/http/ngx_http_auth_jwt_module.html)).

## The path a route is matched on

Envoy picks a route on the path as the client wrote it, unless the path holds `//`, a `.` or `..` segment, an escaped
unreserved character (a letter, a digit, `-`, `.`, `_` or `~`) or `%2F`; then it picks it on the path it has
normalised, which keeps every other escape as written, in the letter case written. Before it matches, it escapes a raw
`"`, `<`, `>`, `^`, `` ` ``, `{`, `|` or `}`, and over HTTP/2 a byte above 0x7F. The service receives the path the route
was picked on, and the query as written. The nginx file reads the path as the client wrote it and does the same,
measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2:

- a path with nothing to normalise, or with only an escape Envoy keeps, such as `/api/x%3Bv=1/admin`, is matched as
  written and sent on byte for byte, so `%3B`, `%20`, `%40` and `%7c` reach the service as escapes and a route never
  takes them as the characters;
- a path that needs normalising and keeps no escape, such as `/api/b/../a%41b/admin`, is matched on nginx's normalised
  path, which is Envoy's then, and that is sent;
- a path with a character Envoy escapes and no kept escape is matched on nginx's normalised path in a file with no
  pattern route, where a prefix or a path cannot hold the character, and reaches the service escaped as Envoy sends it.

nginx cannot rebuild Envoy's path for the rest, so it answers them `400` where Envoy serves them, each declared on its
row: a path that needs normalising and keeps an escape (`/api/a%2fb%3bc`); a character Envoy escapes beside a kept
escape; a character Envoy escapes in a file with a pattern route, where a pattern could take the raw character Envoy
never matches; a kept escape on a route with a `rewrite`, because nginx rewrites the decoded path; an empty segment
before a dot-dot segment (`/api//../admin`), because Envoy resolves the `..` before it merges the slashes and nginx
merges first; and an empty query, a `?` with nothing after it, wherever nginx would rewrite the path, because nginx
drops the `?` Envoy sends on.

## The browser origins an environment admits

Every file checks the browser origin, with or without `cors` and lists of origins, as the Envoy output does: a request or
a preflight whose `Origin` an environment neither lists, covers with a pattern nor serves as one of its own hosts is
answered `403` before a route is picked, so it reaches no service, a WebSocket upgrade included. An origin on one of the
environment's own hosts is admitted on either scheme and any port, and compared as written: `http://LOCALHOST`,
`http://localhost.` and `null` are refused where the host is `localhost`. A request with no `Origin` is not checked. A
present but empty `Origin` is refused as the Envoy output refuses it, over HTTP/1.1 and HTTP/2. The `Origin` lines are
read as Envoy reads them: the lines that are not empty are joined with a comma, each without the spaces and tabs around
it over HTTP/1.1, and the service receives them as one line.

`origins.<environment name>.origins` and `patterns` admit more origins, and `cors` sets the lines a browser reads. An
origin in a list, or one a pattern covers, is admitted, and the answer to its request carries `Access-Control-Allow-Origin`
with the origin as sent, and `Access-Control-Expose-Headers` when `cors.expose_headers` is written, in place of any the
service sent. A listed origin is compared ignoring case, so `HTTPS://WWW.EXAMPLE.COM` is admitted by
`https://www.example.com` and sent back as written, and a pattern in the case written, so `HTTPS://SHOP.EXAMPLE.COM` is
refused by `https://*.example.com`. A preflight from an admitted origin is answered `200` by nginx itself, when a route
takes its path, with the `Access-Control-Allow-Methods`, `Access-Control-Allow-Headers` and `Access-Control-Max-Age` lines
that the `cors` keys write (a `*` for the first two sends what the request asks for), and with `Access-Control-Allow-Origin`
and `Access-Control-Expose-Headers` where a list holds its origin. An origin on one of the environment's own hosts that
no list holds is admitted without those two lines: its request reaches the service, and its preflight carries the other
three. A path the file refuses with `400`, an upgrade other than WebSocket, a request no route takes and a request
nginx refuses while it reads it, such as one with a NUL byte in a header, which the Envoy output refuses too, or a
header line longer than nginx's header buffers read ([The header limits](#the-header-limits)), carry none of them.
Five differences, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, which nginx's
rows of `origins.<environment name>.hosts`, `origins.<environment name>.patterns`, `cors.allow_methods` and
`cors.allow_headers` name:

- **A foreign or empty `Origin` to a path no route takes is answered `403`**, where the Envoy output answers `404`:
  nginx checks the origin before it picks a route.
- **An `Origin` that holds a comma is answered `403`**, where the Envoy output can admit it: it joins two `Origin` lines
  with a comma before a pattern reads them, which no browser sends, and a pattern's `*` takes the comma. With the pattern
  `https://*.example.com`, the lines `https://evil.example.net` and `https://www.example.com` were admitted, the answer
  carrying `access-control-allow-origin: https://evil.example.net,https://www.example.com`. nginx refuses an `Origin`
  that holds a comma before a pattern reads it, so a pattern never admits two lines joined.
- **A preflight that asks for its method on two `Access-Control-Request-Method` lines is answered with the two joined by
  a comma and a space**, `POST, PUT`, where the Envoy output sends `POST,PUT`: the same two methods, with
  `allow_methods: "*"`.
- **A preflight that asks for headers on two `Access-Control-Request-Headers` lines is answered with the two joined by a
  comma and a space**, `x-a, x-b`, where the Envoy output sends `x-a,x-b`: the same list, with `allow_headers: "*"`.
- **With `allow_headers: "*"`, a preflight that asks for no headers gets no `Access-Control-Allow-Headers` line**, where
  the Envoy output sends the line empty: nginx sends no line whose value is empty, and an empty list and no line grant
  the same.

## What a pattern route and an origin pattern cost

nginx runs each pattern route and each origin pattern with PCRE2 (10.46 in both images, with no JIT), and a worker
serves no other request while a run lasts. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, on an arm64 Mac,
with one worker unless it says otherwise; a time is the least and the most of three requests sent one at a time, except
in the first four bullets, which were measured on this version and give the median and the largest of the requests each
names, sent one at a time in the first three. The other bullets were measured on the version before it, which did not
yet write `limits`, `timeouts` or `log`:

- **The longest path nginx reads.** With no `limits`, nginx reads a request line of 61,336 bytes over HTTP/1.1, a path
  of 61,323 bytes ([The header limits](#the-header-limits)). The 24 pattern routes read each path below; a path ending
  `x.js1` has the last characters every asset pattern needs, so PCRE2 runs each one. The first number of each pair is
  the median of seven requests, the second the largest:

  | A `GET` of | nginx | Envoy |
  |---|---|---|
  | `/api/` and 61,000 `a` | 26.20 to 27.03 ms, 28.35 ms | 3.07 ms, 3.56 ms |
  | the same ending `x.js1` | 26.15 to 26.82 ms, 27.47 ms | 2.98 ms, 3.19 ms |
  | `/api/` and 65,000 `a`, with `size: 8MiB` | 28.79 to 29.12 ms, 30.84 ms | 3.15 ms, 3.80 ms |
  | the same ending `x.js1`, with `size: 8MiB` | 28.16 to 28.29 ms, 29.14 ms | 3.13 ms, 3.36 ms |

- **The longest `Origin` nginx matches.** An `Origin` longer than 8,192 bytes is not matched against the origin
  patterns: it is answered `403`, as one no pattern takes, so one a pattern would take is refused, the difference the
  `origins.<environment name>.patterns` note names. Measured in three series of seven requests, one at a time, with 12
  workers: an `Origin` of 8,192 bytes that the last of 64 patterns of two `*` takes was served in 12.20 to 13.02 ms at
  the median (13.77 ms at most), and one that none takes was refused in 11.70 to 12.58 ms (13.94 ms at most), where
  Envoy took 2.76 to 4.06 ms (6.25 ms at most) and 1.24 to 1.48 ms (1.62 ms at most). One of 8,193 and one of 61,000
  bytes were refused in 0.25 to 1.04 ms at the median (3.01 ms at most), where Envoy served them in 2.65 to 2.90 ms and
  14.47 to 15.41 ms (16.43 ms at most). One worker spends 11.7 to 13.0 ms on the costliest `Origin` nginx matches,
  where one of Envoy's spent 1.2 to 4.1 ms.
- **A long path and a long `Origin` in one request add their costs**, as the pattern routes read the path and the
  origin patterns read the `Origin`. The bound of 8,192 bytes keeps such a request within 50 ms at any `size`: with
  `size: 128KiB`, the longest path nginx reads beside an `Origin` the last pattern takes, 65,489 bytes, took 51.58 to
  58.09 ms at the median beside an `Origin` of 16,384 bytes. At the bound, measured in three series of 30 requests,
  one at a time, with 12 workers, a path of 53,055 bytes beside an `Origin` of 8,192 bytes, about the longest head
  nginx reads with no `limits`, was served in 32.52 to 33.18 ms at the median (35.36 ms at most), and one of 65,489
  bytes with `size: 128KiB` in 37.39 to 38.36 ms (44.72 ms at most), where Envoy took 4.26 to 4.65 ms and 5.02 to 5.13
  ms. 1,200 of the first over 24 connections at once took nginx 7.08 to 11.77 s and Envoy 1.24 s, and 1,200 of the
  second 8.38 to 9.08 s and 1.13 s.
- **A plain request waits behind a flood of that request on one worker.** With one worker (`worker_processes 1`, where
  the file writes `auto`) and `size: 128KiB`, 600 requests of a path of 65,489 bytes beside an `Origin` of 8,192 bytes,
  one of which takes 38.5 to 38.6 ms alone, took 22.5 to 22.7 s over 24 connections at once. A plain `GET` beside them,
  served in 0.21 ms alone, waited 10.2 to 10.8 s at the median and 18.7 s at most on a new connection, one sent every 50
  ms, and 871 to 881 ms at the median and 944 ms at most on a connection already open. One Envoy worker served the 600
  in 2.7 s, one taking 4.9 ms alone, and the plain `GET` beside them in 115 to 116 ms at the median and 168 ms at most
  on a new connection, and in 102 to 103 ms and 141 ms at most on one already open.
- **An origin pattern of several `*` costs time in the `Origin`'s length alone.** Each `*` but the last is written to
  take the shortest run before the next part and never give it back, as HAProxy's are, so a run cannot try each way
  of splitting the `Origin` between the `*`. With the 64 patterns above, the first form, which tried each way, held a
  worker for 17.1 to 21.7 s before it refused an `Origin` of 6,321 bytes (`https://`, 3,150 `a.` and
  `example1.orgx`), and refused one of 8,020 bytes that the last pattern takes after 344 to 369 ms. The written form
  refuses the first in 11.7 to 11.9 ms and serves the second in 14.9 to 15.2 ms, where Envoy took 1.5 to 1.6 ms and
  3.6 to 3.8 ms.
- **A route pattern runs at most 100,000 steps of PCRE2**, written at its start as `(*LIMIT_MATCH=100000)`, where
  PCRE2's own limit is ten million. A path past them is answered `400`, the difference nginx's row of
  `routes[].pattern` names. Under PCRE2's own limit, `/g/`, 40 `a` and `b` against `^/g/((a|aa)+c|a+b)$` held a
  worker for 213 to 227 ms before that `400`, and `/g/`, 30 `a` and `b` was served after 112 to 133 ms; under the
  written one each is answered `400` in 2.1 to 2.5 ms, where Envoy served both in 1.5 to 7.4 ms. Paths of 61,323
  bytes, the longest a `GET` holds with no `limits`, built against each pattern of the battery's files and the examples
  were decided as Envoy decides them, in 26.6 ms at most, but for `^/give-up/(a|aa)+$`, which nginx refuses on 61,323
  `a`, a difference the `routes[].pattern` note covers; with 10,000 steps nginx gave up on twelve more, some of them
  paths a pattern takes, so the limit stays at 100,000.
- **A request sent beside them** on the same worker is served only after the run ends: beside the 6,321-byte `Origin`
  of the first form it waited 17.0 to 17.7 s, and beside the `/g/` path under PCRE2's own limit 177 to 203 ms; beside
  each under the written forms it is served in 0.8 to 1.4 ms. With 12 workers, nginx's count for Docker's 12 CPUs, it
  is served by another worker.
- **Floods**, each of 3,000 requests over 24 connections at once unless it says otherwise, with a request every 50 ms
  beside them:

  | Requests | nginx, 12 workers | nginx, one worker | Envoy, 12 workers | Envoy, one worker |
  |---|---|---|---|---|
  | The 6,321-byte `Origin`, first form | 24 over 24 connections: 8 to 12 refused `403` within 60 s, the rest no answer in 60 s; beside them 2.0 to 2.6 ms at the median | 4 over 4 connections: 3 refused within 60 s, one no answer; the request beside them no answer in 30 s | `403` in 0.78 s; beside them 6.1 ms at the median | `403` in 3.3 s; beside them 56 ms at the median |
  | The 6,321-byte `Origin`, written form | `403` in 4.0 to 5.5 s; beside them 1.3 to 1.5 ms at the median | `403` in 33.0 to 33.4 s; beside them 763 to 764 ms at the median | `403` in 0.42 s; beside them 6.0 ms at the median | `403` in 2.8 s; beside them 49 ms at the median |
  | The 8,020-byte `Origin` the last pattern takes, written form | served in 8.3 to 8.4 s; beside them 1.1 ms at the median | served in 41.4 to 41.8 s; beside them 484 to 485 ms at the median | served in 1.3 s; beside them 6.3 ms at the median | served in 7.0 s; beside them 60 ms at the median |
  | New paths of `/kNN/` and 1,000 random `a` and `b`, the battery's flood | served in 1.1 to 1.3 s; beside them 2.4 ms at the median | served in 0.65 to 1.0 s; beside them 1.6 to 6.8 ms at the median | served in 0.57 s; beside them 5.5 ms at the median | served in 0.88 s; beside them 7.5 ms at the median |
- **Memory.** A worker held 16.3 to 16.4 MB at the start and 16.6 to 24.0 MB after the requests above. The battery's
  flood grew the 12 workers together by 6.7 to 7.0 MB and one worker by 0.3 to 2.7 MB, where Envoy grew by 73 to 75 MB;
  the `Origin` floods grew nginx by 0.2 MB at most and Envoy by less than 0.1 MB. Docker's API, which the battery's
  flood row reads, counts the whole container: in 10 runs of the battery, with `nginx-file`'s `tls`, the flood grew it
  by 18 to 29 MiB on nginx 1.30 and 1.31 and by 72 to 75 MiB on Envoy on the same file.

## The client's address and removed headers

With `client_address: connection`, which a file that writes `tls` takes when it writes no `client_address`, the file
takes each client for one it faces, as the Envoy output does. Each route's location sends the service:

- the client's `X-Forwarded-For` with the peer's address added on the same line, as `1.1.1.1,10.0.0.5`, or the peer's
  address alone when the client sent none or an empty one;
- `X-Forwarded-Proto` from the connection's scheme, and an `X-Request-Id` of the file's own, a version 4 UUID as the
  Envoy output makes, in place of the client's;
- `x-envoy-external-address`, the peer's address, in place of a client's own.

It also empties `x-envoy-decorator-operation`, `x-envoy-downstream-service-cluster`, `x-envoy-downstream-service-node`,
`x-envoy-original-host` and `x-envoy-original-path`, which only a proxy in front may send, besides the `x-envoy-`
headers every file empties. A client's own `X-Real-IP` and `Forwarded` pass, as through the Envoy output. With
`forwarded` the file writes none of these: the client's `X-Forwarded-For` passes as received, and the client's
`X-Forwarded-Proto` and `x-request-id` are kept when it sends them. These answers are equal to the Envoy output's,
measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, but for an absolute `https` target sent over plain HTTP under
`connection`, which nginx serves (below), a tab before or after a value, which reaches the service where Envoy removes
it ([the list](#where-nginx-is-less-strict-than-the-envoy-output)), and three refusals, named on nginx's request-line
row of [Portability](portability.md#support-by-proxy-from-the-generators), which `proxyfile.js` makes before a route
is picked:

- **A header name that holds `_` and that nginx reads as one of its own is answered `400`**:
  `access-control-request-method`, `access-control-request-headers`, `x-request-id`, `x-forwarded-proto`,
  `http2-settings`, `keep-alive`, `proxy-connection` and `transfer-encoding`, written with `_` for `-`. `nginx.conf`
  sets `underscores_in_headers on`, so that a name such as `x_und` reaches the service as it does through the Envoy
  output, and nginx's variable for a header then reads the spelling with `_` as well as the one with `-`. The service
  received `x-request-id: abc` from a header written `x_request_id: abc`, and a preflight granted `x-secret` from
  `access_control_request_headers`, where the Envoy output reads each name as another header and passes it on. nginx's
  own variable for `X-Forwarded-For` reads only the name written with `-`, so `x_forwarded_for` reaches the service as
  sent, as through the Envoy output, and so does a header named `constructor` or `__proto__`.
- **A second `X-Forwarded-For`, `X-Forwarded-Proto` or `X-Request-Id` line is answered `400`**, where the Envoy output
  joins the lines with a comma and serves the request. nginx's variable for a header reads both lines as one value, and
  the service received two lines, or the two joined with a comma and a space, where Envoy sends them joined with a comma.
- **A second `Content-Type`, `User-Agent` or `Referer` line is answered `400`**, where the Envoy output joins the lines
  with a comma and serves the request. Each holds one value, not a list, and nginx passed the two lines as two, so a
  service that reads the first line, as most do, would read another value through nginx than through the Envoy
  output.

The configuration cannot tell the spellings or the lines apart, so `proxyfile.js` refuses both.

With `forwarded`, which a file with no `tls` takes when it writes no `client_address`, an `X-Forwarded-Proto` line that
is present and empty is answered `404` before a route is picked, as the Envoy output answers it, whether the line is
empty or, over HTTP/1.1, holds only spaces and tabs; the configuration alone cannot tell it from no line, so
`proxyfile.js` does. The same name on a line with no colon is answered `404` too, where the Envoy output answers `400`.
With `connection` the line is not read and the request is served. Measured on nginx 1.30.6 and 1.31.8 beside Envoy
1.40.2.

With `connection` and no `tls`, an absolute `https` target sent over plain HTTP, such as
`GET https://localhost/products/1 HTTP/1.1`, is served: the service receives `X-Forwarded-Proto: http`, and with
`auth.send_request: true` the check is told `x-forwarded-proto: http`, the connection's scheme, where the Envoy output
answers `403` with the body `Forbidden`, closes the connection and asks no check; with `forwarded` both serve it. It is
a way nginx is less strict than the Envoy output, named in [the list](#where-nginx-is-less-strict-than-the-envoy-output)
and on the `client_address` row of [Portability](portability.md#support-by-proxy-from-the-generators) (measured on nginx
1.30.6 and 1.31.8 beside Envoy 1.40.2). proxyfile 2.2.0 told the check `https` for it, the scheme the target names.

`remove_from_clients` names headers a client may not send. For each name, every route's location sets the header empty
(`proxy_set_header <name> ""`, and `grpc_set_header` where the location sends to a gRPC service), and every place
`nginx.conf` itself reads a removed name reads it empty, so the request is answered as if the client had not sent it:
with `origin`, `x-request-id`, `x-forwarded-proto`, `x-forwarded-for`, `access-control-request-method` or
`x-envoy-internal` removed, a foreign origin was served, a preflight was sent to the service, a new request id was made,
the scheme was `http` and no `X-Forwarded-For` reached the service, as with the Envoy output. A name written in another
letter case, or sent twice, is removed, and a name written with `_` for `-` is not, as with the Envoy output. nginx
refuses two kinds of name when it renders:

- **A name with a `*` prefix**: nginx takes each header by name, so write each name in full.
- **`host`, `content-length`, `transfer-encoding`, `connection`, `upgrade`, `te`, `keep-alive` and `http2-settings`**:
  nginx reads these itself to frame and route the request. The Envoy output breaks when it removes the first five: with
  `host` removed every request was answered `404`, with `content-length` and `transfer-encoding` removed a body was sent
  chunked, and with `upgrade` and `connection` removed a WebSocket upgrade hung. `te`, `keep-alive` and `http2-settings`
  were not measured on Envoy.

## TLS

With `tls`, every server of the file, the default one included, listens with TLS (`listen <port> ssl`, with `http2 on`),
and `nginx.conf` names the certificate and its key, and with `tls.clients` the clients CA, at the paths the `tls` keys
give. nginx refuses to load the file when the certificate is missing at its path (`cannot load certificate`, measured on
nginx 1.30.6 and 1.31.8 with [`examples/tls/yaml/nginx.conf`](../examples/tls/yaml/nginx.conf) and no files);
[`scripts/validate-nginx.sh`](../scripts/validate-nginx.sh) makes throwaway files for the examples.

`proxyfile render --target nginx` refuses a `tls.certificate` or `tls.key` path that holds `$`, naming the key.
nginx reads a `$` in those two paths as the start of a variable, and a path that keeps its `$` through a variable is
read at each handshake, not at start, so `nginx -t` passes when the file is missing and every handshake then fails,
where the Envoy output refuses to start (measured on nginx 1.30.6 and 1.31.8). Put the file at a path without `$`. The
clients CA's path keeps its `$`: nginx reads it there as written and refuses to start when the file is missing.

The listener takes TLS 1.2 and 1.3 only. Of TLS 1.2 it takes the list the Envoy output offers, the ECDHE suites with
AES-GCM or ChaCha20: with an RSA certificate that is `ECDHE-RSA-AES128-GCM-SHA256`, `ECDHE-RSA-AES256-GCM-SHA384` and
`ECDHE-RSA-CHACHA20-POLY1305`, and every other TLS 1.2 cipher is refused. The three TLS 1.3 suites are the Envoy
output's, the key exchange groups are X25519, P-256 and X25519MLKEM768, and ALPN offers `h2` and `http/1.1`. A TLS
server name that matches no host is served, and so, without `tls.clients`, is a request whose `Host` names another
environment than its TLS server name, each as by the Envoy output. With `tls.clients`, the file asks for a certificate
the clients CA signed (`ssl_verify_client on`, `ssl_verify_depth 100`, which is BoringSSL's default depth; the bound is
not measured), and serves a good certificate, one under an intermediate sent with its chain, and a TLS 1.2 client, the
service told nothing of the certificate. Four differences, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2,
which nginx's rows of `tls` and `tls.clients.ca` name:

- **A client certificate that is missing or fails is answered with an empty `400` after the handshake**, where the Envoy
  output ends the handshake with an alert. The cases measured are no certificate, one a second CA of the same name
  signed, an expired one, one not valid yet, a self-signed one, one for servers only, and one under an intermediate sent
  without it. Nothing reaches a service.
- **With `tls.clients`, a request whose `Host` is another name than its TLS server name is answered with an empty
  `421`**, where the Envoy output serves it, when the server name is a host of an environment: nginx makes this check
  itself when it asks for client certificates, it compares the two names, not the servers, so two hosts of one
  environment get it too, and nothing reaches a service. A TLS server name that no environment names is served. A
  browser that reuses one HTTP/2 connection for `api.example.com` and `api.example.org` gets it; that was not measured
  with a browser.
- **Plain HTTP sent to the TLS port is answered with an empty `400`**, where the Envoy output closes the connection.
- **The cipher and the key exchange group chosen can differ for one offer, from the same sets.** `openssl s_client`'s
  default TLS 1.2 offer got `ECDHE-RSA-AES256-GCM-SHA384`, where Envoy gave `ECDHE-RSA-CHACHA20-POLY1305`, and an offer
  that names P-256 first got P-256, where Envoy gave X25519 over TLS 1.2 and X25519MLKEM768 over TLS 1.3.

## The header limits

`limits.request_headers` sets the largest head a request may carry, the request line, the header lines and the empty
line together, and the most header fields. With no `limits` the file holds the Envoy output's defaults, 60 KiB and 100
fields. `size` becomes nginx's header buffers, and `count` becomes `max_headers`:

| `size` | `large_client_header_buffers` |
|---|---|
| not written (60 KiB) | `1 61376` |
| `4KiB` | `1 4032` |
| `96KiB` | `2 49120` |
| `128KiB` | `2 65504` |
| `8MiB` | `128 65535` |

Each file also writes `client_header_buffer_size 64;`. `large_client_header_buffers` takes a number of buffers, n, and
their length, b: n is (`size` less 64) divided by 65,536, rounded up, and b is (`size` less 64) divided by n, rounded
down. nginx reads the request into the first buffer, of 64 bytes, then moves the line that does not fit, and the lines
after it, into a large buffer, and splits no line across two buffers. A head is therefore at most 64 plus n times b
bytes, never more than `size`, so nginx never takes a larger block than the Envoy output takes for the same `size`. A
buffer is at most 64 KiB for two costs, both measured on nginx 1.30.6 and 1.31.8. Linux's transparent huge pages back a
buffer of 2 MiB or more with a whole 2 MiB page: with transparent huge pages set to `always`, one buffer of the whole
`size` grew nginx by about 2,072 KiB a connection at 2, 4 and 8 MiB, where the buffers above grew it by 24 to 33 KiB.
And nginx's maps read the whole line a buffer holds, so a longer buffer lets a longer path hold a worker: with buffers of
1 MiB a path of 1,000,000 bytes took nginx 202 to 231 ms, where a buffer of 64 KiB holds no line longer than 65,533
bytes and a path of 65,005 bytes took 28.79 to 29.12 ms at the median ([What a pattern route and an origin pattern
cost](#what-a-pattern-route-and-an-origin-pattern-cost)).

What each proxy reads over HTTP/1.1, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 (the two nginx images gave
the same bounds). Envoy counts the whole head and serves a head of exactly `size`; nginx reads less, because no line is
split across two buffers:

| `size` | Envoy: largest head | nginx: largest head with one long header line | nginx: largest head with a long request line |
|---|---|---|---|
| not written (60 KiB) | 61,440 bytes | 61,438 bytes, a line of 61,372 | 61,376 bytes, a line of 61,336 |
| `4KiB` | 4,096 bytes | 4,094 bytes, a line of 4,028 | 4,032 bytes, a line of 3,992 |
| `96KiB` | 98,304 bytes | 49,184 bytes, a line of 49,118 | 49,158 bytes, a line of 49,118 |

One byte more, Envoy answers `431`. nginx answers `400`, or `414` for a request line at `96KiB` and for one that passes
Envoy's bound as well, a head of 61,441 bytes at the default and 4,097 bytes at `4KiB`. Above 64 KiB nginx reads a line
as long as one buffer, 32 KiB to 64 KiB, which is 49,118 bytes at `96KiB`, 65,502 at `128KiB` and 65,533 at `8MiB`, where
the Envoy output reads a line as long as its head allows, so the longest line does not always grow with `size`.

Over HTTP/2 Envoy counts the names and values of every field, the pseudo-headers included: it took 61,440 bytes at the
default and 4,096 at `4KiB`, and reset the stream one byte past. nginx took 61,376 and 4,032, and past them ends the
connection (`GOAWAY` with `ENHANCE_YOUR_CALM`).

`max_headers` counts each header line, and over HTTP/2 `proxyfile.js` counts the four pseudo-headers and each cookie
field from what nginx keeps. nginx gives `:authority`, a `host` field or both one `host` line, so the script counts
`:authority` whether or not the client sent it: at the default 100, nginx served 95 fields beside `:authority` alone, a
`host` field alone or both, where Envoy served 96, 96 and 95. A request over `count` is answered `400`, where the Envoy
output answers `431` over HTTP/1.1 and resets the stream over HTTP/2.

Memory held by connections that send the start of a head and wait, measured with 2,000 connections held 15 s, at the
default and at `size: 8MiB`:

| The head sent | Envoy, a connection | nginx, a connection |
|---|---|---|
| 65 bytes | 23.8 to 24.2 KiB | 23.8 to 24.2 KiB |
| 1,100 bytes | 24.3 to 24.8 KiB | 24.3 to 25.1 KiB |
| 9,000 bytes | 30.5 to 31.0 KiB | 32.1 to 32.8 KiB |

Held 150 s with heads of 1,100 bytes at the default, Envoy stayed at 24.1 KiB a connection and nginx at 24.8 to 25.3. In
two of seven earlier runs of 145 s and 290 s nginx rose to 35.5 and 28.1 KiB a connection, at most 1.5 times Envoy's,
for a reason not found; the kernel joining huge pages is the likely one, and it was not checked. The time a path or an
`Origin` takes to read against the patterns is in [What a pattern route and an origin pattern
cost](#what-a-pattern-route-and-an-origin-pattern-cost).

The notes of nginx's `limits.request_headers.size` and `limits.request_headers.count` rows name where nginx differs. A
header block within 64 bytes of the limit can be refused, where the Envoy output takes it. Above 64 KiB, a line longer
than one of nginx's buffers (32 KiB to 64 KiB) is refused, and a block of long lines can be refused under the limit. A
request over `size` is answered `400`, or `414` for a request line, where the Envoy output answers `431`, and over
HTTP/2 it ends the connection, where the Envoy output resets the stream. The lines of a header Envoy joins into one,
such as `accept`, count once each, and over HTTP/2 a cookie field holding several cookies counts once for each, where
the Envoy output counts it once. Two differences are less strict and are in [the list
below](#where-nginx-is-less-strict-than-the-envoy-output): a trailer larger than the limit is served over HTTP/1.1, and
empty frames in a row are served over HTTP/2.

## The connection limit

`limits.connections` is nginx's own `limit_conn`: `limit_conn_zone "all" zone=pf_conn:1m;` in the `http` block and
`limit_conn pf_conn <number>;` in each environment's server. nginx counts a request on its way to a service while it is
in process, once its header is read, each HTTP/2 stream as one and an idle connection as none, and answers one past the
number `503`, before its check, if any, is asked
([ngx_http_limit_conn_module](https://nginx.org/en/docs/http/ngx_http_limit_conn_module.html)). An answer the file gives
itself with `return`, such as the `404` of a host no environment names, a refused origin's `403` or a preflight's `200`,
is neither counted nor refused, as a `return` ends the request before `limit_conn` runs: so the default server, which
answers every request that way, holds no line, and the admin server none, so that `/stats`, which nginx answers after
that, is never answered `503`. No line limits a service's connections. Above 65535 the key is refused, as nginx refuses
it (`ngx_http_limit_conn_module.c` line 705).

## The timeouts

`timeouts.idle` is written as per-operation timers, the body, send, read and connect times below, where the Envoy output
has one idle timer; four per-operation timers stand for Envoy's one, as the note of nginx's `timeouts.idle` row says.
`timeouts.upstream_idle` is the time nginx keeps an idle connection to a service. What the file writes for each, with
the `grpc_` twin of each `proxy_` line carrying the same time:

| `timeouts` | `client_body_timeout`, `send_timeout`, `proxy_send_timeout` | `proxy_read_timeout` | `proxy_connect_timeout` | `keepalive_timeout` in each upstream |
|---|---|---|---|---|
| not written | `5m` | `15s` | `5s` | `1h` |
| `idle: 2s`, `upstream_idle: 3s` | `2s` | `2s` | `2s` | `3s` |
| `idle: 90s` | `90s` | `15s` | `5s` | `1h` |
| `request: none` | `5m` | `5m` | `5s` | `1h` |
| `idle: none` | the longest time | `15s` | `5s` | `1h` |
| `request: none`, `idle: none`, `upstream_idle: none` | the longest time | the longest time | `5s` | the longest time |

The read time is the shorter of `idle` and 15 s, or `idle` itself with `request: none` (5 minutes with no `idle`), and
the connect time is the shorter of `idle` and 5 s. `none` is written as `9223372036854775807ms`, the longest time both
nginx images load (they refuse `9223372036854775808ms`) and nginx's form of no limit. Measured on nginx 1.30.6 and
1.31.8 beside Envoy 1.40.2, with `idle`, `request` and `upstream_idle` all `none`, a service silent for 6 minutes was
served by all three after 359.9 s, and a service connection was reused 1, 4 and 70 s later by all three. With
`upstream_idle: 3s` and one nginx worker, a service connection was reused after 1 s and opened again after 4 s, by all
three.

A service that takes the connection and then sends nothing is answered `504` with Envoy's text, so a client reads the
same body from either proxy:

- With `idle: 2s` the answer is `504` with `stream timeout` after 2.00 s, by all three (nginx's body is `text/plain`, 14
  bytes).
- Without `timeouts` the answer is `504` with `upstream request timeout` after 15.0 s, as Envoy answers it. With
  `request: none` and no `idle` it is `504` with `stream timeout` after 300.1 s, where Envoy takes 300.0 s.
- A gRPC call to a service that sends nothing, with `idle: 2s`, is answered `504` with `stream timeout` as the body,
  where the Envoy output answers `200` with `grpc-status: 14` and the message `stream timeout`; both end the call after
  2.00 s.
- A request body that stops for longer than `idle` is ended with no answer, where the Envoy output answers `408`.

A service that takes the connection and does not read the request's body: a retried route's location, which reads a
body of at most 1 MiB whole before its first try, writes its own `proxy_send_timeout`, the read time, and each of its
copies the time left, so a `PUT` of 1 MiB to such a service is answered `504` after 15.0 s by the Envoy output and by
both nginx versions, to a service that reads one byte every 20 s as well, and through a retried route of three
backends whose first two took no connection (three runs of each, 15.0 to 15.1 s). A location that streams the body,
a route's with no retries or the copy of a retried route that tries once, sends it with the send time of the table;
what that answers, and the line tried in its place, is in [the list
below](#where-nginx-is-less-strict-than-the-envoy-output). A client that stops its body for 20 s while the service
reads is served by all three.

`timeouts.request` with a time is refused for nginx. Its time limits count the time between two reads or two writes, so
no directive holds a time for a whole request; write `timeouts.request: none`, or render the file for Envoy. Without the
key nginx answers `504` to a service silent for 15 s after the request, as the Envoy output does, and with `none` it
ends a request after 5 minutes with nothing read or written, as Envoy does. An answer that keeps arriving runs past the
15 s at which Envoy ends a request, and so does a request body its service keeps reading slowly, on a retried route
too: differences in [the list below](#where-nginx-is-less-strict-than-the-envoy-output).
What nginx answers when the connect time passes is in [What nginx writes with no
key](#what-nginx-writes-with-no-key).

## Rate limits

`routes[].rate_limit` and the rpc entry's `rate_limit` are written with nginx's `limit_req`, one zone of shared memory
for each limited entry of `routes`, as the Envoy output has one bucket for each: every method the rpc entry routes
shares the entry's zone, and a route's copy in every environment shares the route's. For
[`examples/limited`](../examples/limited/yaml/proxyfile.yaml), whose rpc entry admits 100 calls a second for every
client together and whose `login` route admits each client 5 requests a minute with a burst of 5, the `http` block
holds

```
js_set $pf_client_key pf.clientKey;
limit_req_status 429;
js_set $pf_429_kind pf.limitedKind;
limit_req_zone "all" zone=pf_rl_1_all:16m rate=100r/s;
limit_req_zone $pf_client_key zone=pf_rl_2_client:16m rate=5r/m;
```

and each location of the `login` route

```
limit_req zone=pf_rl_2_client burst=9 nodelay;
error_page 429 = @pf_429$pf_429_kind;
```

A zone's key is a constant for `by: all`, the peer's address, `$binary_remote_addr`, for `by: client` under
`client_address: connection`, and under `forwarded` the client `proxyfile.js` reads as Envoy reads it: the last
`X-Forwarded-For` entry when it is an IPv4 or IPv6 address, an IPv6 address compared by its value, and the peer
otherwise, so an entry with a port, in brackets, with a leading zero or with a zone counts as the peer. The rate is
`<r>r/s` when `requests` in `per` make a whole number a second, and `<m>r/m` when they make a whole number a minute.
nginx admits one request and then up to `burst` more at once with `nodelay`, so its `burst` is `requests` plus `burst`
less one, and a zone that has been quiet admits the `requests + burst` a full bucket of the Envoy output holds.
`routes[].rate_limit.per` and `routes[].rpc.rate_limit.per` giving no whole number of requests a second or a minute are
refused by `--target nginx`: nginx's rate is a whole number of requests a second or a minute; write requests and per so
that one of them is whole.

Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, these are the same:

- The same requests were admitted and refused at every instant of paced plans with `per` of `1s`, `2s` and `1m` and
  `burst` of 0 and 5: a bucket of `requests + burst`, refilled at `requests` in `per` as time passes, not all at once
  when a period ends.
- `by: all` counts the requests of every worker in one zone: of 200 requests sent at once to 12 workers, a limit of
  10 admitted 10.
- The limit is charged before the check: a request the check denies spends a token, and a request from a refused
  origin, a preflight and a refused path do not. nginx charges in its preaccess phase, before the check's `js_access`
  runs in the access phase, whatever the order of the lines.
- A request over the limit gets the Envoy output's answer for its kind, from a location the script's `limitedKind`
  names: `429` with the body `local_rate_limited` as `text/plain`, with `x-request-id` and `Vary: Origin`; for a
  Connect call `429` with `{"code":"resource_exhausted","message":"local_rate_limited"}` as `application/json`; and for
  a gRPC call `200` with `grpc-status: 8` and `grpc-message: local_rate_limited`, with `content-length: 0`, as every
  gRPC answer nginx writes itself. Each carries `Server: nginx`, as nginx's own answers do ([What nginx writes with no
  key](#what-nginx-writes-with-no-key)). Both close the connection after every HTTP/1.1 request over the limit with a
  body, a gRPC call and a Connect call among them, framed by its length or chunked or waiting on `Expect`, and keep it
  after one with none.
- The client under `forwarded` was the same in every form measured: the last entry of two, spaces or a tab around it, a
  port, two spellings of one IPv6 address, IPv4 in IPv6, a value that is no address, an empty value, a trailing comma,
  a leading zero, brackets, a zone and `1.2.3`. A second `X-Forwarded-For` line is answered `400`, as the request
  line's row declares.

nginx is stricter in two ways, each declared on the row of its key in
[Portability](portability.md#support-by-proxy-from-the-generators) in these words, for an entry of `routes` and for its
rpc entry alike:

- **`routes[].rate_limit.per`**: a rate a minute is kept in thousandths of a request a second, rounded down, so 1 a
  minute refills every 62.5 s where the Envoy output refills every 60 s (nginx refused at 62.1 s and admitted at
  63.3 s).
- **`routes[].rate_limit.by`**: each limited route remembers about 120,000 clients in up to 16 MiB of shared memory,
  where the Envoy output remembers 65,536, so a client is forgotten later. The zone takes its pages of shared memory as
  clients arrive.

A zone is named by its number and the kind of its key, `pf_rl_<n>_all`, `pf_rl_<n>_peer` or `pf_rl_<n>_client`,
because nginx refuses a reload that gives a zone another key and keeps the old configuration running with one line in
its error log; so a reload that changes `by`, `client_address`, a rate or the order of the limited routes is taken
(measured on nginx 1.30.6 and 1.31.8). At a reload nginx keeps a zone's counts where the Envoy output starts new
buckets, and two limited routes of one key kind that change places take each other's counts until they refill.

What a limited route costs beside a plain one is in [What a rate limit and a hash
cost](#what-a-rate-limit-and-a-hash-cost).

## Retries

The rpc entry's `retries` is written as `grpc_next_upstream error timeout;` and `grpc_next_upstream_tries` of `retries`
plus one in the locations of its methods' routes, so a call whose connection the service refused, or did not open in
the connect time, is tried on the next backend, as the Envoy output tries it, and a call nginx sent is never sent again.
Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2: with one of two backends refusing, 40 calls were all served by
the other, and a call whose stream or connection the service reset after it read it reached the service once through
both. A call whose method is not `POST`, such as a `GET` or a `PUT` with gRPC's content type over HTTP/1.1, goes to a
copy of its route's location without those two lines, because nginx sent such a call again after the service had read
it. nginx declares two differences on `routes[].rpc.retries` in these words: a call whose method is not POST is not
tried again; a service with one backend is not tried again, where the Envoy output tries the same backend. A third is
on [the list of ways nginx is less strict](#where-nginx-is-less-strict-than-the-envoy-output): a retried request or
call is sent again at once and with no limit on retries at the same time.

A route's `retries` of 1 or more tries a request again only when nothing of it reached a service, the requests the
Envoy output's `connect-failure` and `reset-before-request` conditions try again, and nginx never sends a service a
request twice. nginx's own retry stays off, as `proxy_next_upstream off` in every file's `http` block, because it sends
a request again after a service read it, for every method but `POST`, `LOCK` and `PATCH`: with one of two backends
resetting or closing the connection after it read a `GET` or a `PUT`, the Envoy output delivered each request once and
answered `502`, where nginx with `proxy_next_upstream error timeout` delivered it twice. A retried route is written
instead in this form:

- The route's location reads the request's body whole before the first try (`proxy_request_buffering on`), so each try
  sends the same bytes, and reads it into memory: a body of at most 1 MiB (`client_max_body_size 1m`), in a buffer one
  kilobyte larger (`client_body_buffer_size 1025k`). It takes a request with no `Transfer-Encoding` whose
  `Content-Length` is at most 1 MiB, whatever its method; over HTTP/1.x one with neither header, which has no body (RFC
  9112, section 6.3), whatever its method; and over HTTP/2 a `GET`, `HEAD`, `OPTIONS` or `DELETE` with no
  `content-length`, which it answers `413` when the body passes 1 MiB. Two forms were tried before this one. With a
  buffer of 1 MiB nginx wrote an HTTP/2 body of unknown length to a temporary file. With this buffer and a body sent in
  chunks taken too, it wrote a `GET` whose 300,000 bytes came in one-byte chunks to a temporary file, as nginx counts
  the chunk lines against the buffer. nginx has no setting that keeps a body it reads whole off its disk, so the
  location takes no request whose bytes could pass the buffer.
- The location's `error_page 502 504` goes to the target a map picks from the tries so far: when the last try sent the
  service 0 bytes and ended `502` or `504`, a jump is left and a backend the request has not tried is left, a copy of
  the location that sends the request to an upstream of that backend alone, the next one in the order the service
  lists them; once the route's time is spent, the answer the route's timer gives; otherwise the answer the route
  location gives a failed try. A copy is a named location that only that `error_page` reaches, so the check is asked
  and the rate limit charged once for a request, whatever its tries, and each copy sets the headers its route sets.
- The route's time counts across the tries, as the Envoy output counts it from the complete request: the route's
  location sends and reads for the route's read time, each copy for the time left ([The timeouts](#the-timeouts)), one
  copy for each second of tries, and `proxyfile.js`'s `retrySpent` picks it from the tries' own times as nginx lists
  them (`$upstream_response_time`), so a request whose tries fail, or whose service answers or stays silent, ends
  within half a second of the Envoy output's time. One whose service reads its body slowly, taking a write within the
  send time each time, runs past it, as [The timeouts](#the-timeouts) says.
- Every other request takes a copy of the route's location that tries once and sends the body as it arrives, as a route
  with no retries does: one with a `Content-Length` over 1 MiB, one with a `Transfer-Encoding` in any letter case, a
  body sent in chunks whatever its method, and over HTTP/2 one with no `content-length` and a method other than those
  four.

Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, with one to eight backends that refused the connection, did
not answer ARP or did not take it in time: each request was served or answered as through the Envoy output, within
half a second of its time, but in the cases below, and no service read a request twice. The check was asked once for
each request, every second try included, and the accepted headers and the unchecked mark reached the service on it;
under a limit of 10 requests a minute, 10 of 14 requests were served and 4 answered `429`, as through the Envoy
output, the request a retry served charged once. A `host_header`, a `rewrite`, a target sent as written, IPv6
backends in either way of writing them, a WebSocket upgrade after a refused first try and a gRPC call with a
`content-length` on an HTTP route were each served as through the Envoy output, and an RPC route on the same service
kept its own lines.

The differences, which nginx's `routes[].retries` row declares in these words: a request is tried again only when its
last try sent the service nothing, at once and with no limit on retries at the same time, which the less strict list
names, on the next backend in the service's order rather than one the balance or the hash picks, at most as many times
as the service has other backends and never more than 6; a service with one backend is not tried again, where the Envoy
output tries the same backend; a service found by a DNS name is tried again on the same name, where nginx may pick the
backend that failed; a request is tried again only when it has no Transfer-Encoding and a Content-Length of at most 1
MiB, when it has neither header over HTTP/1.x, which gives it no body, or when it is a GET, HEAD, OPTIONS or DELETE over
HTTP/2 with no content-length, and nginx reads its body into memory before the first try, so the service gets it only
once all of it arrived, and with a slow client a request whose tries fail ends up to the upload's time later than
through the Envoy output, which tries while the body arrives, and can be answered 504 where the Envoy output answers 502
or serves it; such a GET, HEAD, OPTIONS or DELETE over HTTP/2 with more than 1 MiB of body is answered 413, where the
Envoy output serves it; any other request, a body longer than 1 MiB, a body sent in chunks whatever its method, or one
of another method over HTTP/2 with no content-length, a gRPC call's among them, is sent as it arrives and never tried
again, and a first try of it that fails is answered as on a route with no retries, where the Envoy output tries again a
body of unknown length under its bound of 1 MiB, holds up to 16 MiB over HTTP/2, and answers 507 to a client that sent a
longer body quickly and serves one still sending; a request nginx wrote on a kept-alive connection the service had
closed is not tried again, where the Envoy output tries it again on another backend. Each as measured on nginx 1.30.6
and 1.31.8 beside Envoy 1.40.2:

- The cap: nginx follows 10 internal redirects of a request, and the chain of one whose every try fails takes the
  `418` to the route's location, each jump, the answer of the last try and the `502` or `504` location, so 7 jumps were
  answered `500`. With seven backends refusing and one serving, `retries: 7` was answered `502` after 7 tries, where the
  Envoy output, which may try a backend again, served it.
- A service found by DNS name: 40 of 40 requests sent one at a time were served, nginx trying the address that
  refused and then the other each time, and at 200 requests a second for 5 s nginx served 1,001 of 1,001, where the
  Envoy output served 898 and answered 103 `502`, held by its retry budget (the less strict list below).
- The body: an HTTP/2 `GET` whose headers end the stream, an HTTP/2 `PUT` with a `content-length` of 1,048,576, a
  `HEAD`, an `OPTIONS` and a `DELETE` over HTTP/2 with no length, a `GET` carrying 1,048,576 bytes with no length over
  HTTP/2, a `PATCH` and a `PUT` of 1,048,576 bytes, and a `POST`, `PUT`, `PATCH`, `LOCK` and `PROPFIND` over HTTP/1.1
  with neither `Content-Length` nor `Transfer-Encoding` were each tried again and served, as through the Envoy output.
  An HTTP/2 `GET` carrying 2 MiB with no length was answered `413` before any try, where the Envoy output served it. A
  `GET` sent in chunks over HTTP/1.1, of 300,000 one-byte chunks or of 2 MiB, a `DELETE`, `HEAD` and `OPTIONS` of
  200,000 one-byte chunks, a body under `Transfer-Encoding` written `Chunked` or `CHUNKED`, an HTTP/2 `PUT` of 102,400
  bytes with no `content-length`, a chunked `PUT` and every body over 1 MiB with a length (up to a slow `PUT` of 24 MiB)
  were sent once and never tried again, answered `502` when that try was refused, the chunked ones with no temporary
  file, where the Envoy output tried a body of unknown length again, answered a fast client's body over its bound `507`
  (the 2 MiB `GET` three times of four) and served the others. 100 `PUT`s of 1,048,576 bytes at once, and 20 HTTP/2
  `GET`s of 1,048,576 bytes with no length at once, were all served with no temporary file, and each service read every
  body whole.
- A slow upload: a `PUT` of 524,288 bytes sent in parts over about 11 s, to four backends that each fail late, to four
  whose connection attempts time out, and to one that times out then one that answers 12 s after the request. The
  Envoy output tried the backends while the body arrived: it answered the first `502`, the second `502` once its four
  tries had failed, and served each request of the third. nginx tried them only after the whole body: it answered the
  first `502` about the upload's time later, and the second and each request of the third `504` when the route's
  15 s, which it counts from the complete request as the Envoy output does, ran out.
- A kept-alive connection the service had closed: of 10 such requests, nginx answered 8 `502` and tried none again,
  where the Envoy output answered 1 `502` and served the others; no service read a request twice on either.

Four forms that would start the first try before the body is whole were measured on nginx 1.30.6 and 1.31.8, and none
holds, so the body is read first:

- The jump with `proxy_request_buffering off` in the retried route's locations: when the first try failed while the
  body was still arriving, the copy that took the next try sent the service only the part of the body nginx had read,
  under the request's whole `Content-Length`, and never the rest, so the service waited and the request was answered
  `504` when the route's 15 s ran out, where the Envoy output served it. Of 100 `PUT`s of 1,048,576 bytes at once, about
  half were answered so, each one whose first try was refused.
- The same in every copy as well, and that with `proxy_http_version 1.1` written in each of those locations: the same.
- nginx's own retry with the body sent as it arrives (`proxy_next_upstream error timeout`, `proxy_next_upstream_tries`
  and `proxy_next_upstream_timeout`, no jump): a slow `PUT` whose first backend refused was served whole, at the Envoy
  output's time, but a `PUT` of 100 bytes that the first service had read whole before it closed with no answer was
  sent to the second service as well, 8 of 8, and nginx has no setting that tells the two apart.

What the form costs in the file and in memory, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 on mains
power with no other container busy. A retried route writes copies of its route location: the one that tries once,
and one for each second the tries before a try can take, as a location's read time is fixed and nginx counts it for
each try. With the default timers and `retries: 1` to two backends, a route wrote 14 more locations; with
`retries: 4294967295`, eight backends and `timeouts: {request: none}`, whose tries can take 30 s, 64 more; and a file
with a retried route 3 more, which its routes share. The lines,
locations and bytes of each file are given with `retries: 0` and then with its retries; the memory, the median of 3
runs with their range, is that of the proxy at rest with the retries:

| File | Lines | Locations | Bytes | Idle memory, nginx 1.30.6 / 1.31.8 | Envoy output |
|---|---|---|---|---|---|
| 100 routes, 99 of them `retries: 1`, to two backends | 7,118 to 58,955 | 226 to 1,615 | 336,758 to 2,704,622 | 65.7 MiB (65.2 to 65.7) / 65.6 MiB (65.3 to 65.7) | 23.7 MiB (23.3 to 23.9) |
| 100 routes, each `retries: 4294967295`, to eight backends, `request: none` | 7,124 to 247,020 | 226 to 6,629 | 336,973 to 11,315,021 | 228.9 MiB (228.9 to 229.0) / 227.9 MiB (227.9 to 228.0) | 24.6 MiB (24.6 to 26.3) |
| 4,096 such routes | 278,914 to 10,101,218 | 8,218 to 270,365 | 13,258,655 to 463,512,002 | 8.368 GiB (8.368 to 8.368) / 8.335 GiB (8.335 to 8.335) | 67.7 MiB (67.7 to 70.1) |

So a retried route took about 0.45 MiB of nginx's memory with the default timers and about 2 MiB with the longest
tries, beside about 20 MiB for a 100-route file and 284 MiB for the 4,096-route file with no retries. nginx loaded
each file with no warning, and `nginx -t` read the 4,096-route file in 11.1 s on 1.30.6 (11.0 to 12.2) and 11.4 s on
1.31.8 (11.3 to 11.6), the medians of five runs. Copies at a coarser step than a second would cost less and end a
request up to that step earlier than the Envoy output.

Reading the body first holds it in memory while it arrives, measured on mains power as medians with each proxy restarted
before each case, of five runs each but where a figure names its own count. A `PUT` of 1,048,576 bytes alone took 1.96
ms on nginx 1.30.6 and 1.67 ms on 1.31.8 through a retried route, 1.35 and 1.18 ms through a route with no retries, and
1.40 ms through the Envoy output's retried route; the runs ranged from 1.07 to 2.29 ms, so no proxy was faster. 100 such
`PUT`s at once, each sent in 64 KiB parts 100 ms apart, were all served: at their peak nginx held 97.6 MiB more than at
rest on both versions (95.0 to 104.8) and about 11 MiB more 5 s later, where the Envoy output held 111.0 MiB more (109.4
to 111.6) and still held it 5 s later, and nginx opened no temporary file. 20 `PUT`s of 64 MiB at once, which the copy
that tries once sends as they arrive, were all served with nginx holding about 10 MiB more at the peak (6.7 to 16.5, of
four runs on 1.30.6 and five on 1.31.8), as on a route with no retries, where the Envoy output held 24.8 MiB more (24.6
to 25.1).

At 1,000 requests a second for 60 s on kept-alive connections, nginx served every request at that rate through a
retried route with every first try served, at a median of 10.9% of a CPU on 1.30.6 and 10.0% on 1.31.8, and with
every first try refused, at 13.8% and 12.8%, holding at most 27 MiB; the Envoy output used 8.1% with every first try
served and 12.6% with every first try refused, and at most 30 MiB (medians of three to five runs, the runs another
container was busy for left out). With every first try refused, nginx's 99th percentile was 0.49 ms on 1.30.6 and
0.45 ms on 1.31.8, where the Envoy output's was 24.4 ms (24.2 to 24.6), as it waits before a retry and answered 502
the requests over its limit of retries at the same time
([the less strict list](#where-nginx-is-less-strict-than-the-envoy-output)).

A reload runs the new workers beside the old ones until the old ones have ended their requests, so it needs room for
about twice the memory the file holds at rest: reloading the 4,096-route file on nginx 1.30.6 at 1,000 requests a
second passed a limit of 20 GiB and the kernel stopped the container (one run, on mains power).

## Backends: balance, hash and DNS names

`balance` is written in each upstream of the service:

| `balance` | What nginx writes |
|---|---|
| `round_robin`, the default | no line: nginx's own round robin |
| `least_requests` | `random two least_conn;`: of two backends chosen at random, the one with fewer requests open, as the Envoy output compares two |
| `random` | `random;` |
| `hash` with `hash.header` | `hash $pf_hash_key consistent;` |
| `hash` with `hash.client` | `hash $binary_remote_addr consistent;` under `client_address: connection`, and `hash $pf_client_key consistent;`, the client the rate limit reads, under `forwarded` |

For `hash.header`, a `map` gives the header's name for each upstream, `$pf_hash_name`, and the script's `hashKey` reads
the request's lines of it: an empty header is a value of its own, and the lines of one header are joined in sorted
order, as Envoy joins them before it hashes them, so one value, or one set of lines in any order, reaches one backend.
A request without the header goes round robin, as Envoy sends it to a backend chosen at random. The ring is nginx's own,
so a value reaches one backend through either proxy, but not always the same one. Measured on nginx 1.30.6 and 1.31.8
beside Envoy 1.40.2, with three backends: round robin, least requests and random spread the requests as the Envoy
output does, least requests and random by another draw of the same kind; each of 300 header values, over HTTP/1.1 and
HTTP/2, and each of 300 client addresses under `forwarded`, reached one backend; an empty header kept every request on
one backend; and with one of the three refusing, the requests that drew it were answered `502` by both, tried on no
other backend.

`hash` on the service `auth.ask` names is refused by `--target nginx`: nginx's check relay cannot read the check's
headers to hash them; write another balance on the service. That service's upstream is the relay's, in the `stream`
block, which reads none of the `http` block's variables, and under `client_address: connection` the relay's client is
nginx itself on the relay's socket, not the request's client. It is refused whether `auth.enabled` is true or not, so a
file stays valid when the check is turned on.

A service whose `address` is a DNS name gets, in each of its upstreams, `zone <upstream> 1m;` and
`server <name>:<port> resolve max_fails=0;` (with `health`, [`max_fails=<unhealthy>`](#passive-health-checks)) in place
of the address lines, and the `http` block names the server of
`dns.resolver`, `resolver 10.0.0.2:53 valid=5s;`, as does the `stream` block of the check's relay, which looks the
check's service up the same way when its address is a name. nginx asks for a name's IPv4 and IPv6 addresses both, and
asks again every 5 s, as the Envoy output does; [`examples/dns`](../examples/dns/yaml/proxyfile.yaml) writes two such
services. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2: an address added to the name, or removed from it,
was used or left within the 5 s; a name that answered no such name was answered `502`, and served again once it
answered; while the name server was down every request was served with its last answer; a proxy that started while no
name resolved started, answered `502`, and served once the names came; the check's service by name was asked at the
name's address; and a name of 100 and of 1,000 addresses was asked again over TCP after the answer over UDP was cut
short. nginx declares one difference on `services.<service name>.address` in these words: a name gives at most about
1,000 addresses for each port; the zone of 1 MiB held 1,000 addresses and not 2,000.

A DNS name with no `dns.resolver` is refused by `--target nginx`: nginx looks a name up only through the name server it
is given; write dns.resolver. nginx looks a name up while it runs only through `resolver`, never through the host's
configuration, and refuses to load an upstream that resolves a name with none.

### What a rate limit and a hash cost

These figures were measured on one Mac (Apple M5 Max, Docker 29.9.3 with 12 CPUs), on mains power, with no other
container above 3% of one CPU at the start and the end of each run (programs on the Mac itself also ran). They hold for
nginx 1.30.6 and 1.31.8 beside the Envoy output on Envoy 1.40.2, for the configuration rendered from one file whose
services each have the same three backends: a plain route to them by round robin; a route limited to 100,000 requests a
second by client, which the runs never came near, so every request was counted and admitted; a service with
`hash.header` on `x-session-id`, sent 300 values in turn; and one with `hash.client` under `client_address: forwarded`,
sent 300 `X-Forwarded-For` addresses in turn. The proxies ran one at a time with every CPU of the Docker host, nginx
with a worker for each. A client on the same Docker network sent each path requests alone, 20 a second on one connection
for 10 s, and then 1,000 a second over up to 64 connections for 30 s, all over HTTP/1.1, and every request of the 1,000
a second was answered `200` by one of the three backends. A run gives the median and the p99 of the requests' times, the
proxy's CPU 15 s into the 1,000 a second as a share of one CPU, and the proxy's memory before the first path and after
the last.

Each cell is the median of five runs and, in brackets, the least and the most of the five. A figure whose five runs
differ by more than three times is not given.

| Path and figure | Envoy 1.40.2 | nginx 1.30.6 | nginx 1.31.8 |
|---|---|---|---|
| The plain route, alone, median | 0.71 ms (0.69 to 0.77) | 0.60 ms (0.58 to 0.74) | 0.59 ms (0.57 to 0.70) |
| The plain route, 1,000 a second, median | 0.22 ms (0.21 to 0.28) | 0.23 ms (0.17 to 0.40) | 0.21 ms (0.16 to 0.39) |
| The plain route, 1,000 a second, p99 | 0.54 ms (0.50 to 0.67) | not given | 0.56 ms (0.39 to 1.17) |
| The plain route, 1,000 a second, CPU | 12.1% (8.8 to 18.3) | 12.4% (7.1 to 17.8) | 8.3% (6.6 to 19.1) |
| The limited route, alone, median | 0.71 ms (0.69 to 0.73) | 0.63 ms (0.61 to 0.75) | 0.64 ms (0.60 to 0.69) |
| The limited route, 1,000 a second, median | 0.22 ms (0.20 to 0.23) | 0.31 ms (0.18 to 0.39) | 0.23 ms (0.19 to 0.26) |
| The limited route, 1,000 a second, p99 | 0.66 ms (0.44 to 0.67) | 0.62 ms (0.39 to 0.92) | 0.53 ms (0.48 to 0.73) |
| The limited route, 1,000 a second, CPU | 9.5% (7.7 to 10.7) | 17.2% (8.0 to 19.0) | 11.3% (9.6 to 15.1) |
| `hash.header`, alone, median | 0.73 ms (0.70 to 0.75) | 0.62 ms (0.61 to 0.62) | 0.61 ms (0.59 to 0.75) |
| `hash.header`, 1,000 a second, median | 0.23 ms (0.20 to 0.48) | 0.32 ms (0.22 to 0.34) | 0.20 ms (0.19 to 0.29) |
| `hash.header`, 1,000 a second, p99 | not given | 0.87 ms (0.52 to 1.40) | 0.50 ms (0.36 to 0.63) |
| `hash.header`, 1,000 a second, CPU | 10.1% (8.5 to 21.6) | 17.1% (10.1 to 18.9) | 10.1% (7.2 to 16.9) |
| `hash.client`, alone, median | 0.71 ms (0.69 to 0.79) | 0.63 ms (0.62 to 0.70) | 0.62 ms (0.60 to 0.65) |
| `hash.client`, 1,000 a second, median | 0.29 ms (0.24 to 0.33) | 0.22 ms (0.20 to 0.40) | 0.20 ms (0.19 to 0.29) |
| `hash.client`, 1,000 a second, p99 | 0.69 ms (0.54 to 0.78) | 0.59 ms (0.48 to 0.90) | 0.53 ms (0.47 to 0.63) |
| `hash.client`, 1,000 a second, CPU | 12.6% (8.1 to 22.1) | 13.4% (10.5 to 18.6) | 12.2% (10.4 to 15.9) |
| Memory before the first path | 21.25 MiB (20.65 to 22.45) | 13.96 MiB (13.95 to 14.11) | 13.91 MiB (13.85 to 14.00) |
| Memory after the last path | 26.75 MiB (24.50 to 27.96) | 22.45 MiB (22.19 to 23.21) | 21.36 MiB (21.23 to 23.07) |

Within each proxy, the range of the limited route and of each hash overlaps the plain route's on every figure both give,
so these runs show no cost of a rate limit or of a hash above what moves from one run to the next. Between the proxies,
the ranges of every time and CPU figure overlap, so those figures are level, but for two requests alone: through nginx
1.30.6 to the `hash.header` service in 0.61 to 0.62 ms and through nginx 1.31.8 to the `hash.client` service in 0.60 to
0.65 ms, where the Envoy output took 0.70 to 0.75 ms and 0.69 to 0.79 ms. nginx held less memory than the Envoy output,
before the first path and after the last, in every run of both versions.

### Passive health checks

With `health`, each server of the service's upstreams, and of the check relay's in the `stream` block, is written
`max_fails=<unhealthy>`, 3 when not written, in an upstream with `zone <upstream> 1m;`, so the workers share one count.
This is nginx's own passive check ([`server`](https://nginx.org/en/docs/http/ngx_http_upstream_module.html#server)): a
backend whose attempts fail `unhealthy` times within 10 s, the default `fail_timeout`, takes no request for 10 s. In the
service's upstreams an error, a timeout and an invalid answer count, under the file's `proxy_next_upstream off` and
`grpc_next_upstream off` too; in the relay's, only an error or a timeout while connecting ([stream
`server`](https://nginx.org/en/docs/stream/ngx_stream_upstream_module.html#server)). nginx never takes a group's one
server out, so a service with one address, and each group of a retried route, writes neither line. A service with
`health` and more than 1,000 addresses is refused, a bound under what its upstream's zone of 1 MiB holds. `path`,
`grpc`, `interval`, `timeout` and `healthy` are not read: open-source nginx sends no check request (`health_check` is
NGINX Plus's).

## The admin port

With `admin`, nginx listens on `127.0.0.1` at that port and answers three paths there. Measured on nginx 1.30.6 and
1.31.8 beside Envoy 1.40.2:

- `/ready` is answered `200` with `LIVE`, as the Envoy output answers it.
- `/stats` is answered `200` with nginx's `stub_status` page, seven counters in all: the line `Active connections`, the
  line `server accepts handled requests` with a line of three numbers under it, and the line with `Reading`, `Writing`
  and `Waiting`. The Envoy output answers with its own statistics, one `name: value` line each. With `tracing` and a
  collector found by name, its count of requests holds each call of the tracer through the relay
  ([Tracing](#what-the-file-writes)).
- `/server_info` is answered `200` as `application/json` with the nginx version, `{"version":"1.30.6"}` or
  `{"version":"1.31.8"}`. The Envoy output answers with its own version, its state and more.
- Any other path, and any request with a query, `/stats?format=json` or `/ready?x=1`, is answered `404` with no body,
  where the Envoy output answers `403` with a text that names the path.
- Each path answers `GET`, `HEAD`, `POST`, `PUT`, `DELETE`, `PATCH` and `OPTIONS` as it answers `GET`, as the Envoy
  output does: `stub_status` refuses every method but `GET` and `HEAD` with `405`, so the file sends that `405` back to
  `/stats`, which nginx then asks for with `GET`. `TRACE`, which nginx refuses before it reads the path, is answered
  `405` with no body, where the Envoy output answers it as `GET`.
- The admin port answers what the public listener refuses before a route as Envoy's admin answers it: a request
  Envoy's parser refuses, such as a method it does not know (`FOO`), is answered `400` with the body `Bad Request` as
  `text/plain`, a `QUERY` sent with no `Content-Type` or with an empty one, which over HTTP/1.1 is also one of spaces
  and tabs alone, and a `#` in the target, `400` with no body and no `content-type`, and HTTP/1.0 `426` with `Upgrade
  Required` as `text/plain`, the connection closed after each.
- An absolute target of `http` or `https` is answered as the path it names, `GET http://localhost/ready` with `200` and
  `LIVE`, where the Envoy output answers every absolute target `404`, a way nginx is less strict than the Envoy output
  ([the list](#where-nginx-is-less-strict-than-the-envoy-output)). One that nginx or the public listener's checks
  refuse, as one with an empty port, user information, a query right after its host or a scheme other than `http` and
  `https`, is answered `400`, where the Envoy output answers `404`.
- The admin port writes no log line for a request, as the Envoy output writes none.

None of the three paths is answered on the public listener: a route takes them as it takes any other path, on both
proxies. The contents of `/stats` and `/server_info`, the status of a refused path, the answer to `TRACE` and the `400`
to a refused absolute target are declared on nginx's `admin` row, and the absolute target served as a path is named
there as a way nginx is less strict.

## The auth check

With `auth.ask` the file asks your check service about a request before it goes on to the route's service, and passes an
asked request on only when the check lets it through. [Auth](auth.md) says what the check does and what it is told; this
section says how nginx asks it and where its answers differ from the Envoy output's.

Three parts do it, and all three are in the two files render writes:

- Each location of a checked route carries `js_access pf.check;`. `check` is a function of `proxyfile.js` that runs in
  nginx's access phase, before nginx reads the request's body, so an upload the check refuses is answered at once: a
  refused `POST` whose body paused for 3 s was answered in under 1 s, where a content handler, which reads the whole
  body first, answered after 3.00 s (measured on nginx 1.30.6 and 1.31.8). It asks the check with a subrequest to the
  location `/.proxyfile/check`, which each environment's server holds. A client's own request for that path is routed
  and asked as any other path.
- That location passes the subrequest to the relay: a server in a `stream` block that listens on a socket file named
  for the listener (`/run/proxyfile-8000/check.sock` for `listen: 8000`), in a folder only nginx's own users can enter
  ([The relay's socket](#the-relays-socket)). The relay is the function `pf.relay` of the same script, set with
  `js_filter`. It sends the check service one HTTP/1.1 request on a new connection, reads the answer itself with the
  bounds Envoy reads, and answers nginx with `200` and a form of its own in which every part is written after its
  length, so no byte the check sent can end or split a part.
- `check` reads that form and lets the request go on, or answers the request itself. A route location passes the
  request to its service only after `check` has let it go on.

The check needs both of njs's modules. `nginx.conf` names them with `load_module modules/ngx_http_js_module.so;` and
`load_module modules/ngx_stream_js_module.so;`, and the two images pinned in `scripts/nginx-images`, nginx 1.30.6 and
1.31.8, carry both: `nginx -t` accepts the rendered file on each. A file with no `auth.ask`, or with `auth.enabled:
false`, loads the http module alone and has no `stream` block.

For the `auth` block of [Your own service](auth.md#your-own-service), with `listen: 8000` and `accounts` at `10.0.0.7`,
`proxyfile render --target nginx` writes these lines, where a line that starts `# ...` stands for lines left out (the
file has no such comment). A route location of the checked route `app` holds:

```
location @pf_r_2 {
  set $pf_log_route "app";
  set $pf_log_upstream "app_http";
  default_type "";
  js_access pf.check;
  proxy_set_header Host $pf_authority;
  proxy_set_header X-Request-Id $pf_request_id;
  proxy_set_header X-Forwarded-Proto $pf_forwarded_proto;
  proxy_set_header Origin $pf_origin;
  proxy_set_header Upgrade $pf_upgrade;
  proxy_set_header Connection $pf_connection;
  proxy_set_header HTTP2-Settings $pf_http2_settings;
  proxy_set_header TE $pf_te;
  # ... lines that set more headers empty
  proxy_set_header "x-user-id" $pf_accept_0;
  proxy_set_header "x-user-roles" $pf_accept_1;
  proxy_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;
  proxy_set_header x-gateway-action "";
  proxy_set_header x-gateway-reports "";
  proxy_pass http://app_http;
  error_page 502 = @pf_502$pf_grpc_call;
  error_page 504 = @pf_504;
}
```

each environment's server holds the check location:

```
location = /.proxyfile/check {
  if ($pf_checking = "") {
    return 418;
  }
  error_page 418 = @pf_r_$pf_route;
  proxy_pass http://unix:/run/proxyfile-8000/check.sock;
  proxy_pass_request_body off;
  proxy_set_header Content-Length "";
  proxy_set_header Connection "";
  proxy_set_header HTTP2-Settings $pf_http2_settings;
  proxy_set_header Host $pf_authority;
  proxy_set_header x-proxyfile-method $request_method;
  proxy_set_header x-proxyfile-path "/check";
  proxy_set_header x-gateway-timeout-ms 2000;
  proxy_set_header x-envoy-internal true;
  proxy_set_header x-envoy-expected-rq-timeout-ms 2000;
  proxy_set_header x-gateway-action $pf_action;
  proxy_set_header x-gateway-reports $pf_reports;
  proxy_set_header X-Request-Id $pf_request_id;
  proxy_set_header X-Forwarded-Proto $pf_forwarded_proto;
  proxy_set_header Origin $pf_origin;
  # ... lines that set more headers empty
  proxy_set_header "x-user-id" "";
  proxy_set_header "x-user-roles" "";
  proxy_set_header x-envoy-auth-failure-mode-allowed "";
  proxy_connect_timeout 2s;
  proxy_read_timeout 3s;
  proxy_buffering off;
  subrequest_output_buffer_size 1200k;
  error_page 502 504 = @pf_check_failed;
}
```

and the `stream` block follows the `http` block:

```
stream {
  js_engine njs;
  js_import pf from proxyfile.js;
  upstream proxyfile_8000_check {
    server 10.0.0.7:7070 max_fails=0;
  }
  server {
    listen unix:/run/proxyfile-8000/check.sock backlog=2147483647;
    set $pf_check_send "authorization cookie x-gateway-timeout-ms x-envoy-internal x-envoy-expected-rq-timeout-ms x-gateway-action x-gateway-reports";
    set $pf_check_ms 2000;
    set $pf_check_uri "";
    proxy_pass proxyfile_8000_check;
    proxy_next_upstream off;
    proxy_connect_timeout 2s;
    proxy_timeout 2s;
    js_filter pf.relay;
  }
}
```

The lines of the check location say why it is built so. The subrequest sends no body and replaces the client's own
`Content-Length` and `Connection`. `proxy_buffering off` and `subrequest_output_buffer_size` let a denial of up to 1 MiB
reach `check` from memory: with buffering on, nginx wrote such a denial to a temporary file, and with
`proxy_max_temp_file_size 0` instead the subrequest never ended and the client got no answer (measured on nginx 1.30.6
and 1.31.8). With these lines the check location writes no temporary file for a denial. `proxy_read_timeout` is one
second above `auth.timeout`, so the handler's own timer ends a slow check first. A subrequest the relay did not answer
`200` goes to `@pf_check_failed`, which tells `check` what nginx got from the relay, its status, the bytes it received
and the time it took to connect, so it can tell a check that was never answered from an answer it could not read, and
from a connection to the relay that nginx never made ([How the answer is read](#how-the-answer-is-read)).

`set $pf_check_uri` names the header the relay reads the path `send_request` tells the check from, and is empty in a
file with no `send_request`, as here. The `default_type ""` line of the route location is for the denials the check
sends with no body ([What the client receives when the check
refuses](#what-the-client-receives-when-the-check-refuses)). `backlog=2147483647` is the longest queue the relay's
`listen` takes: the kernel holds the smaller of that and `net.core.somaxconn`, which was 4,096 in the pinned images, so
the relay's queue held 4,097 connections where nginx's own default of 511 held 512, and a burst of 600 clients filled
the smaller queue while the check was healthy ([How the answer is read](#how-the-answer-is-read)).

### Which requests are asked

The check is asked about a request when all of these hold:

- the header `only_with_header` names has a line that holds a byte as nginx passes it on, or comes on two lines or more;
  with no `only_with_header`, every request passes this test. `--target nginx` refuses a header that `only_with_header`
  names when the proxy removes it from clients, naming the key: a name `remove_from_clients` or `auth.accept` also
  names, a name the Envoy output removes from every client (`proxy-connection`, `x-forwarded-client-cert`,
  `x-envoy-auth-failure-mode-allowed`, the two question headers and the `x-envoy-` headers every file empties), and,
  with `client_address: connection`, one of the five `x-envoy-` names [only a proxy in front may
  send](#the-clients-address-and-removed-headers). The Envoy output removes that header before it reads it and never
  asks the check, where nginx would ask it (measured with both `client_address` modes on Envoy 1.40.2 and nginx 1.30.6;
  with `forwarded` Envoy asks by the five). Envoy reads the header's lines as one value, joined by commas, and asks a
  value that holds a byte. nginx asks every form Envoy asks and a few it does not, which
  [below](#where-nginx-answers-the-check-otherwise-than-the-envoy-output) lists. A second line of `authorization`,
  `if-modified-since`, `if-unmodified-since`, `if-match`, `if-none-match` or `if-range` is answered `400` before the
  check is asked, which the row of the request line declares in
  [Portability](portability.md#support-by-proxy-from-the-generators) (measured with one to three lines of each of an
  empty value, spaces, a tab and a value, in every order, for `authorization`, `x-api-key` and `cookie`, over HTTP/1.1
  and HTTP/2, on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2);
- the route it takes is not `auth: off`: its location carries no `js_access`;
- it is not a CORS preflight that the Envoy output's CORS filter answers: an `OPTIONS` request with an `Origin` and an
  `Access-Control-Request-Method` that is not empty, whose path holds no dot segment with a `;` parameter.

A request whose path still holds a dot segment with a `;` parameter is asked whatever its route, as the Envoy output asks
it: the route that refuses such a path is checked too ([A route with `auth: off`](auth.md#a-route-with-auth-off)). nginx
writes the location `@pf_r_dotparam` for it, which carries the handler and, once the check lets the request through,
passes the request by an internal redirect to `@pf_r_refused`, which answers `400` with no body and closes the connection
after it. The Envoy output also closes the connection after that `400`, at once when it asked the check, and answers no
request sent after it, for each form that both answer `400` (measured on Envoy 1.40.2 and on nginx 1.30.6 and 1.31.8,
with the check allowing); after the check's denial of the same path the Envoy output keeps the connection, `401` and
then `200` on it, and the redirect that closes it is written only for the `400`. Which refusals come before the check
is in [Which answers come before the check](#which-answers-come-before-the-check).

A request is asked for each HTTP/2 stream too: 100 checked streams at once on one connection were each answered by
their own check (a row of the battery).

### What the check is sent

The relay sends the check one HTTP/1.1 request, to the path of `ask`, on a new connection for each check. The path of
`ask` goes as written, a byte of 0x80 or more in it included: with `ask: checks /auth/é/ü?x=ß`, Envoy 1.40.2 and both
nginx versions sent the check `GET /auth/é/ü?x=ß`. The request holds:

- the client's method and the host nginx routes by, which is the `Host` as the client wrote it, or the host of an
  absolute target when the request line holds one, whatever the check service's `host_header` says, and `authorization`;
- `x-gateway-action` and `x-gateway-reports`, for a call of an rpc method that has an `action` or `reports` (below);
- each header a `send` name covers, written once in lower case with all its values joined by commas, as Envoy sends them:
  `X-Country: RO` and `X-Country: FR` on two lines reach the check as `RO,FR`. A `send` name never copies a client's
  `content-length`, `transfer-encoding`, `connection`, `keep-alive`, `te`, `trailer`, `upgrade`, `expect` or
  `proxy-connection`, whatever it names, and `host` is sent once;
- `content-length: 0`, `connection: close` and `x-gateway-timeout-ms`, which holds `auth.timeout` in milliseconds;
- `x-envoy-internal: true` and `x-envoy-expected-rq-timeout-ms`, which holds `auth.timeout` in milliseconds too: the two
  headers Envoy's own client for the check adds, so a check service reads the same names behind either proxy. A
  client's own copies of both are removed before the check is asked, and never reach the service;
- with `tracing`, `traceparent` and `tracestate`, as [Tracing](#tracing) says;
- with `send_request: true`, `x-forwarded-uri`, the path the route matched, normalised, with each byte of 0x80 or more
  escaped as the service receives it, and the query as the client wrote it, before a rewrite; `x-forwarded-method`;
  `x-forwarded-proto`, under `client_address: connection` the connection's scheme, the one the service is sent,
  whatever scheme an absolute target names, and under `forwarded` the scheme of an absolute target, written in any
  letter case, and otherwise the client's own `x-forwarded-proto` as it arrived, whatever word it holds, or the
  connection's scheme: the Envoy output tells the check the connection's scheme for an `x-forwarded-proto` of `ftp`,
  which nginx tells as `ftp` ([the list](#where-nginx-is-less-strict-than-the-envoy-output)); and
  `x-forwarded-host`, the host nginx routes by. A client's own copies of those four are replaced, and a client's own
  `x-gateway-timeout-ms` is never sent.

The check receives well-formed UTF-8 in a header value as the client sent it, and each other byte of 0x80 or more as
`!`. Envoy writes one `!` for each byte that is not part of a well-formed UTF-8 sequence, and the relay writes the same:
`caf` and the bytes `0xC3 0xA9` reach the check as sent, `0xC3 0xA9 0xE9` as `0xC3 0xA9` and `!`, and a truncated,
overlong or surrogate sequence as a `!` for each of its bytes (14 forms measured on Envoy 1.40.2 and on both nginx
versions, with the same answer from each). A value that holds a control byte other than a tab never gets this far:
nginx answers it `400` ([What nginx writes with no key](#what-nginx-writes-with-no-key)).

Over HTTP/2 a path byte of 0x80 or more is told to the check in `x-forwarded-uri` as `%` and two capital hexadecimal
digits, as the service receives it: `/products/caf\xc3\xa9\xe9` is told as `/products/caf%C3%A9%E9`, by the Envoy output
and by nginx alike. Over HTTP/1.1 both answer such a byte `400` before the check is asked. In a file with a pattern
route, nginx answers such an HTTP/2 path `400` itself, which the row of `routes[].pattern` declares.

The headers `x-proxyfile-method` and `x-proxyfile-path` carry the method and the path from the check location to the
relay inside nginx, and the relay writes them as the request line. The check never receives them, and a client's own
copies change nothing the check is told. A client's own copy of any header the proxy writes for the check, and of every
header the Envoy output removes, is removed before the check is asked.

**The RPC question.** For each method with an `action` or `reports`, the location of that method's route sets
`x-gateway-action` and `x-gateway-reports` (the names joined by commas) before the check, and writes both empty for the
service, so a service sees neither. A method with neither gets no line, and a client's own copies of both are removed
first.

### What the service receives

When the check answers `200` and the relay reads the answer whole, the request goes on with each header an `accept` name
names, set from the check's answer; a name the check sent twice keeps its last value, as in Envoy, and a name the check
did not send is not sent to the service, whatever the client sent. The route's location sets each accepted header with
`proxy_set_header`, from the variable `check` filled, beside the location's own lines for the same name.

An accepted header the check sends with an empty value reaches the service empty over HTTP/1.1, as through Envoy: the
handler sets it as one space, which nginx sends as an empty value. A gRPC location sends it through a map that turns
the space into nothing, because over HTTP/2 a value that starts with a space is malformed, so a gRPC service receives
no such header where Envoy sends it empty, which the note on `auth.accept` declares.

The accepted headers count against `limits.request_headers`. Envoy answers `500` with an empty body when the headers
the check's answer sets take the request past the size or the count of the limit, and counts the pseudo-headers and the
headers it adds itself: `x-request-id`, `x-forwarded-proto` and, with `client_address: connection`, `x-forwarded-for`
and `x-envoy-external-address`. The handler counts the same before the request goes on, with the connection's address
in that mode, and answers `500` with an empty body. It errs to the strict side by a margin: measured with
`size: 8KiB` and `count: 30`, nginx answered `500` 15 bytes and 1 field before Envoy with `client_address: forwarded`,
16 bytes and 1 field before it with `connection`, and 30 bytes and 2 fields before it with a client's own
`X-Forwarded-For`, and never after it, on both versions. With an absolute target, such as
`GET http://api.example.com:10000/products/1`, Envoy counts the target's host and port as `:authority` whatever the
`Host` line holds, and the handler counts the longer of the two: counting the `Host` line alone answered `500` 23
bytes after Envoy beside a one-byte `Host`. The note on `auth.accept` declares the margin.

The handler counts only when the check's answer set a header an `accept` name covers, as Envoy counts only then: a `200`
that sets none is served beside 96 numbered header lines by both. A chunked request's `Transfer-Encoding` line is not
counted, as Envoy does not count it, and a request framed by its length is counted with its `Content-Length` line, as by
Envoy: with one accepted header under `client_address: forwarded`, 91 numbered lines with a chunked body are served and
the same lines with a body framed by its length are answered `500`, through Envoy and through nginx. A gRPC call that
the accepted headers take past the limit is answered as Envoy answers a gRPC call it fails itself, `200` with
`grpc-status: 2` and no service, where a call they take to the limit is served. Rows of the battery hold each of these
on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2.

`keep-alive` is never set from the check's answer, as Envoy never sets it. A name written in full that nginx writes
itself on the request it passes on is refused at render, and so is a name with a `*` prefix: the lines of the section on
`auth.accept` [below](#where-nginx-answers-the-check-otherwise-than-the-envoy-output) say which, and that
`x-forwarded-for`, `x-envoy-external-address` and the five `x-envoy-` names Envoy removes from clients are refused with
either `client_address`, so a list stays valid when the mode changes. The file removes a
client's own copies of the `accept` names, of the two question headers and of `x-envoy-auth-failure-mode-allowed` before
the check is asked, so a client cannot forge them. With `when_unavailable: allow` and no answer from the check, the
request goes on with `x-envoy-auth-failure-mode-allowed: true` and none of the accepted headers.

### What the client receives when the check refuses

Any status but `200` from the check is a refusal. The client receives the check's status, a `2xx` other than `200` as
`403`, with every header of the answer but `content-length`, `transfer-encoding`, `connection`, `keep-alive`, `upgrade`
and `proxy-connection`, and the check's body. The Envoy output passes `te`, `trailer` and a header the check's
`Connection` names, and so does the file. A refusal to a request from a listed origin carries that origin's CORS answer,
`Access-Control-Allow-Origin` and, when `cors.expose_headers` is written, `Access-Control-Expose-Headers`, as the Envoy
output's does.

What else the client reads, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with denials of these forms:

- A denial with no body carries no `content-type`, as the Envoy output passes it on: a `401` with `content-length: 0`, a
  `302`, a `304`, a `2xx` answered `403`, and a `401` to a path that ends in `.html`. A denial with a body and no type
  carries `content-type: text/plain`. Each location that carries `js_access` writes `default_type ""`, and the `http`
  block's one `types {}` line empties nginx's types for every server and location, so that nginx gives an answer of its
  own no type from the extension of the path or from its default.
- A `304` carries no `content-length`, and an answer to a `HEAD` request carries no body.
- A `content-type` or a `location` that the check sends on two lines reaches the client on one, the two values joined by
  a comma, as the Envoy output joins them (`a/b,c/d` and `/a,/b`).
- The differences that stay are declared on `auth.ask` ([below](#where-nginx-answers-the-check-otherwise-than-the-envoy-output)):
  `Server: nginx` where the check sent no `server` header, and the check's own `server` header alone where it did; nginx's
  reason phrase for a `302`, `Moved Temporarily` where Envoy writes `Found`; a header line the check sent with an empty
  value is dropped, where Envoy passes it; the check's `access-control-allow-origin: *` arrives beside the listed
  origin's own, where Envoy sends the origin's alone; and a `HEAD` request is answered with `Content-Length: 0`, where
  Envoy sends `transfer-encoding: chunked`.

**A refused request's body is never read as the next request.** A client that reuses its connection sends its next request
right after a request's body, and a body that holds a request would be read as the next request if the proxy answered
and left the body unread. Every answer the handler writes itself goes through `r.return`, which makes nginx discard the
request's body: the check's denial, the `403` for an unanswered check under `deny` and for an answer that cannot be
read, a status the check sent that is passed on, and the `400` for a dot segment with a `;` parameter. Measured on both
versions and on Envoy, a `POST` whose body was a request for `/products/smuggled`, refused by the check, was answered
`401` and nothing served the body's request, and a second request written after that body was served. `r.return` cannot
write a `301`, `302`, `303`, `307` or `308` with the check's body, nor a `304` as the check sent it, so for a request
with a body over HTTP/1.1 it writes nginx's own page for a redirect and `content-length: 0` for a `304`, which `auth.ask`
declares. For a request that has no body by its head, and over HTTP/2, whose body comes in DATA frames, those five are
written as the check sent them.

A `403` of the check to a request that carries an `Origin` is answered with the body `Invalid CORS request` and
`content-type: text/plain` in place of the check's own, for an admitted origin too; a `401` keeps its own body, and a
`2xx` other than `200` answered `403` keeps the check's body (measured on Envoy 1.40.2).

A request whose `content-type` is `application/grpc` alone, or `application/grpc` followed by `+`, is a gRPC call, and
its refusal is answered in gRPC's form, where a plain HTTP `401` reaches a gRPC client as a transport error: `200` with
`content-type: application/grpc`, `grpc-status` from the check's status (400 gives 13, 401 gives 16, 403 gives 7, 404
gives 12, 429, 502, 503 and 504 give 14, any other gives 2), the check's other headers, and the check's body in
`grpc-message`, percent-encoded: every byte outside printable ASCII and `%` is written `%XX`, so a body of
`no entry: 100% sure é`, a line feed and `next line` reaches the client as `no entry: 100%25 sure %C3%A9%0Anext line`.

### How the answer is read

The relay passes a request on only for a `200` it reads whole as RFC 9112 writes it: a status line of `HTTP/1.0` or
`HTTP/1.1`, one space, three digits and then nothing or a space and a reason; header lines of a token name, a colon and
a value with no control byte but a tab; at most one `Content-Length`, digits only and never beside `Transfer-Encoding`;
and a `Transfer-Encoding` of `chunked` alone, each chunk size plain hexadecimal. It skips `1xx` answers before the final
one, and joins a folded header line to the value before it with all its spaces and tabs, dropping its line end, as Envoy
does. It reads each head of the answer, from the status line to the empty line with their line ends, a `1xx` head
included, and the trailer section, to 81,920 bytes, the most Envoy 1.40.2 reads (measured to the byte), and at most 100
header lines in a head, folded lines not counted. It reads a body only to 1 MiB, and a chunk-size line only to 1,024
bytes with its line end: those two bounds are nginx's alone and are stricter than Envoy's. It holds a body for a denial
alone, and reads and drops the body of a `200`.

A line end is CR LF, with these exceptions that Envoy 1.40.2 makes and the relay makes with it: a bare LF ends the data
of a chunk, the last line of the body and a trailer line. A chunk-size line, the last chunk's included, that ends in a
bare LF is an answer Envoy reads as no answer, and the relay reads it as `unreadable`.

An answer it cannot read that way is not a pass. The relay tells nginx one of three things, in the header
`x-proxyfile-check` of its own answer:

- `answered`: the status, the header lines it kept, and for a denial the body, each written after its length
  (`401 2 16 www-authenticate6 Bearer12 content-type10 text/plain6 denied` is a denial with two header lines and the body
  `denied`). `check` reads each part by its length alone, and answers `403` when the lengths do not add up;
- `unanswered`: the check did not answer, as Envoy 1.40.2 counts it. `when_unavailable` decides: with `allow` the
  request goes on with the mark; with `deny` it is answered `403`, with the body `Invalid CORS request` when the request
  carries an `Origin`, and for a gRPC call `200` with `grpc-status` 7 and, with an `Origin`, `grpc-message`
  `Invalid CORS request`;
- `unreadable`: every other answer the relay cannot read, which both modes refuse with that same `403`: a status of 101
  or outside 100 to 599, a line that is not RFC 9112's, a chunk-size line that does not end in CR LF, framing that is not
  one `Content-Length` of digits or one `Transfer-Encoding: chunked`, a line past a bound, and a 101st header line when
  a name came on two lines.

A check counts as unanswered, as it does for Envoy 1.40.2, in these cases:

- the relay cannot connect to it;
- no byte of its answer comes before it closes or before `auth.timeout` passes;
- its status line is a well-formed `5xx`;
- a head of its answer has a 101st header line, folded lines not counted, and no name on two of its lines: Envoy reads
  at most 100. With a name on two lines the relay answers `unreadable`, because Envoy counts a name it keeps inline once
  however many lines carry it (measured with `date`, `via`, `content-type`, `server`, `location` and `cache-control`) and
  then reads such an answer, where it counts `set-cookie`, `www-authenticate` and a name of its own a line each;
- the body of its answer breaks off: the check closes before the bytes that its `Content-Length` or a chunk's size
  promised, or `auth.timeout` passes while the body is read;
- its answer is read whole, but only after `auth.timeout` has passed.

A relay that closes before it writes a byte reads as an unanswered check too, the same as a check service that refuses the
connection: `check` cannot tell the two apart, so with `allow` it lets the request through with the mark.

A connection to the relay that nginx never made is not an unanswered check. `check` reads the status, the bytes and the
connect time that `@pf_check_failed` recorded, and takes a failed subrequest for an unanswered check only when nginx
connected to the relay and the relay wrote no byte. Where nginx never connected, the check was never asked, and the
request is answered `403` under `allow` too, as for any other failure of nginx's own parts. A burst of clients that
fills the relay's queue is such a failure, and the queue is the longest the kernel allows for that reason ([What the
check costs](#what-the-check-costs) gives the bursts).

An exception in `proxyfile.js` itself, in `check` or in the relay's answer, is never a pass: `check` answers it `403`,
with `allow` too.

### The relay

The relay is the `server` at the end of the `stream` block, and the `upstream` above it names the check service. In the
example:

- the `upstream` holds one `server` line for each address of the check service, taken in turn (round robin), each with
  `max_fails=0` so nginx never takes an address out of rotation, or with `health` on a check service of two addresses
  or more [`max_fails=<unhealthy>`](#passive-health-checks);
- `proxy_next_upstream off` makes the relay try one address for each check and no other when it refuses, as the Envoy
  output's check cluster tries no other backend. With two addresses and one refusing, the checks that reach it fail on
  every proxy (a row of the battery, run on Envoy 1.40.2 and on nginx 1.30.6 and 1.31.8 with `when_unavailable: deny`);
- `proxy_connect_timeout` and `proxy_timeout` are `auth.timeout`, so a connect that does not come, and a check that
  sends nothing, are cut off by it;
- `$pf_check_send` lists every header the relay may pass from nginx's request to the check besides `host`:
  `authorization`, the `send` names and the proxy's own. The script holds no list of its own, so `proxyfile.js` is the
  same bytes for every file;
- `$pf_check_ms` is `auth.timeout` in milliseconds.

The relay holds the body of a denial, up to 1 MiB, and reads and drops the body of a `200`. After it has answered nginx
it drops every later byte of the check.

### The relay's socket

The relay listens on a socket file, `/run/proxyfile-<listen>/check.sock`, and the check location passes to it with
`proxy_pass http://unix:/run/proxyfile-8000/check.sock` (for `listen: 8000`). nginx has no socket in Linux's abstract
namespace: a listener written `unix:@name` is a file named `@name` in the folder nginx started from, and the file had the
mode `0666` that nginx gives every unix listener, so that uid 65534 read the check's answer through it (measured on
nginx 1.30.6 and 1.31.8). The file still has that mode, so the folder decides who can reach it.

- **Who can enter the folder.** nginx makes each temporary path of its configuration at start, before it binds a
  listener, owned by the workers' user with the mode `0700`. The `http` block of the file names the relay's folder as
  the temporary path of a module the file never uses:

  ```
  scgi_temp_path /run/proxyfile-8000;
  ```

  Measured on both images, the folder was `drwx------ 2 nginx root`; root and the workers' user, `nginx`, read the
  check's answer through the socket, and uid 65534 got `connect: permission denied` and could not list the folder. The
  folder must not exist before nginx first starts, or must be nginx's own with the mode `0700`: nginx takes over a
  folder it finds and keeps its mode, so one that uid 65534 made first with the mode `0777` became nginx's with the
  mode `0777`, and that user could list it (measured on both images). Two files with different `listen` ports in one
  container each have a folder of their own: with `listen: 10000` and `listen: 10001` both answered every check.
- **What a stop and a kill leave.** nginx removes the file when it stops: after a stop with the image's signal,
  `SIGQUIT`, none was left, and a start, a reload and a start after a stop each answered every check. A kill, `SIGKILL`,
  leaves the file, and nginx then does not start in the same container. It exits with status 1 and writes, for
  `listen: 10000`, `[emerg] bind() to unix:/run/proxyfile-10000/check.sock failed (98: Address already in use)` five
  times and then `[emerg] still could not bind()`. The remedy is to remove the file, or to make `/run` a `tmpfs`: with
  one, a kill and a start in the same container started and answered every check.
- **A read-only root.** The official image's nginx writes its process id to `/run/nginx.pid` and its temporary files
  under `/var/cache/nginx`, so a container with a read-only root already needs both writable, and `nginx -t` refused to
  run with `/run` read-only (`open() "/run/nginx.pid" failed (30: Read-only file system)`). With both as `tmpfs` mounts,
  nginx started and answered every check, and started again after a kill.

`auth.ask` declares the file that a kill leaves ([Where nginx answers the check otherwise than the Envoy
output](#where-nginx-answers-the-check-otherwise-than-the-envoy-output)).

### The deadline and the pass rule

`check` starts a timer of `auth.timeout` when it asks, and races it against the subrequest. With `deny`, a check that
has not answered when the timer fires is answered `403` at `auth.timeout`, as the Envoy output answers it. With `allow`
the request goes on only when the check's subrequest ends, which the relay ends at the first byte the check sends past
the deadline, or after nginx's `proxy_timeout` of `auth.timeout` with no byte from it, so `allow` can hold a request
longer than Envoy does: the note on `auth.timeout` [below](#where-nginx-answers-the-check-otherwise-than-the-envoy-output)
gives the bound. The relay keeps no timer of its own: njs logs an error line for a timer still waiting when a session
ends. The handler clears its own timer on every path, an error included: after a refusal from its `catch`, nginx closed
the request only when the timer fired (measured on nginx 1.30.6 and 1.31.8), and a row of the battery holds that the next
request of a kept-alive client after a refused one is answered at once.

`check` lets a request go on only by returning with no answer, and it does so for exactly three things: a check that
answered `200` and was read whole, an unanswered check under `allow`, and a request that is not asked. Any exception in
it is answered `403` in both modes and writes no line: nginx's error log keeps `emerg` alone, because a request line can
carry a token, and the Envoy output writes no line for a refused check.

### With the check switched off

With `auth.enabled: false` the file writes no handler, no check location, no relay and no `stream` block, and loads no
stream module. It still removes a client's own copies of the `accept` names, the question headers and
`x-envoy-auth-failure-mode-allowed` in every route location, so a client cannot forge them, and the request log still
writes `authz_us` as `null`.

### Which answers come before the check

nginx answers some requests itself before it picks a route or reaches the handler. The table gives, for each kind, what
the Envoy output answers and whether it asks the check first, and what nginx answers, with a token the check denies with
`401` sent on every request (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2):

| The request | The Envoy output | nginx | Compared |
|---|---|---|---|
| an `Origin` on none of the environment's hosts | `403`, not asked | `403`, not asked | the same |
| a `Host` no environment names | `404`, not asked | `404`, not asked | the same |
| a `Host` with a dot at its end | `404` | `404` | the same |
| an upgrade other than WebSocket | `403` | `403` | the same |
| a preflight | `200`, not asked | `200`, not asked | the same |
| a preflight with a dot segment and a `;` parameter, a dot or the `;` escaped | asked, `401` | asked, `401` | made the same: `@pf_r_dotparam` |
| a preflight through a dot segment and a `;` parameter both written, `/..;x/products/1` | `200`, not asked: the preflight of the path it resolves | asked, `401` | declared on the row of the dot segment |
| a dot segment with a `;` parameter, also under a route with `auth: off`, written as `%2e%2e` or `..` | asked, `401` | asked, `401` | made the same: `@pf_r_dotparam` |
| a backslash in the path | asked, `401` | `400` | declared on the row of the path |
| an empty segment before a dot-dot segment, `/` and `.` written or escaped | asked, `401` | `400` | declared on the row of the path |
| an escaped control character in the path | asked, `401` | `400` | declared on the row of the path |
| an escape Envoy keeps, on a route with a `rewrite` | asked, `401` | `400` | declared on `routes[].rewrite` |
| a character Envoy escapes, in a file with a pattern route | asked, `401` | `400` | declared on `routes[].pattern` |
| a path PCRE2 gives up on | asked, `401` | `400` | declared on `routes[].pattern` |
| one header of 62,000 bytes | `431` | `400` | declared on the header limits |
| `QUERY` with no `Content-Type`, or an empty one (over HTTP/1.1 also spaces and tabs alone), over HTTP/1.1 or HTTP/2 | `400`, not asked | `400`, not asked | the same |
| a request over a route's rate limit | `429`, not asked | `429`, not asked | the same |
| `TRACE` | asked, `401` | `405` | declared on the row of the request line |
| TLS with no client certificate, or one of another CA | the handshake ends | `400` | declared on `tls.clients.ca` |
| plain HTTP to the TLS port | the connection closes | `400` | declared on `tls` |
| TLS with a `Host` of another environment than the server name | asked, `401` | `421` | declared on `tls.clients.ca` |
| an admin path on the public port | asked, `401` | asked, `401` | the same |
| the client's own request for `/.proxyfile/check` | asked, `401` | asked, `401`, routed as any other path | the same |
| a service silent past the idle time, token denied, then allowed | `401`, then `504` after the check | `401`, then `504` | the same |
| a service that refuses the connection, token denied, then allowed | `401`, then `502` after the check | `401`, then `502` | the same |
| an admin path on the admin port | `200`, not asked | `200`, not asked | the same |
| no route takes the request, and a preflight no route takes | `404`, not asked | `404`, not asked | the same |

Each answer nginx gives without asking, where the Envoy output asks first, is one the Envoy output serves once the check
lets the request through, so nginx refuses what Envoy serves and never the reverse. The one kind where the Envoy output
asks and then refuses, a dot segment with a `;` parameter, nginx asks about first too, and answers `400` once the check
lets the request through, closing the connection after it, as the Envoy output does.

### Where nginx answers the check otherwise than the Envoy output

Each difference below is declared on nginx's row of its key in
[Portability](portability.md#support-by-proxy-from-the-generators). Four entries of [the list of ways nginx is less
strict](#where-nginx-is-less-strict-than-the-envoy-output) concern the check: past the bound of local ports the
`auth.ask` note gives, a check fails for want of a port, and with `allow` its request is served with the unchecked mark,
so a request the check would refuse can be served; a worker with none of its 8192 connections left cannot ask the check
either, with the same result under `allow`, as [Checks in flight on few workers](#checks-in-flight-on-few-workers) says;
the request of a client that closes its connection while its check is asked can still reach the service once the
check allows it, as the last sentence on the request line says; and under `client_address: forwarded` the check is told
an `x-forwarded-proto` that is neither `http` nor `https` as it arrived, where the Envoy output tells it the
connection's scheme, so a check that decides by the scheme it is told reads the word of the client or of the proxy in
front. In each of the others, a request the check refuses stays refused, and nginx either refuses or holds a request
the Envoy output serves, or answers or writes a line otherwise. The words are the words of the declaration.

<!-- keys: notes: nginx: auth.ask, auth.timeout, auth.when_unavailable, auth.only_with_header, auth.send, auth.send_request, auth.accept, log -->
- **`auth.ask`**: a request whose check answers in one of these forms is answered 403: a status outside 100 to 599, such as 2, 99, 600 or 2000, or 101, where Envoy passes the status on to the client; HTTP/2.0 or HTTP/1.2, two spaces or a tab after the version, a + before the status, a tab between the status and the reason, a lone CR, a space or a tab after a chunk size, a chunk size of nine hex digits, a chunk extension holding a control byte or a quoted CR LF, a chunk-size line past 1,024 bytes, bytes between a chunk's data and its line end, a folded trailer line, or a head of exactly 81,920 bytes with a line in a bare LF, which nginx counts a byte longer, where Envoy reads the answer; a 200 whose body passes 1 MiB, where Envoy serves the request; a denial whose body passes 1 MiB, where Envoy passes the denial on; a denial whose head has more than 100 header lines, folded lines not counted, with a name on two of them, where Envoy counts a name it keeps inline, such as date, once and passes the denial on; over HTTP/2, a gRPC call refused for the check's denial, or with deny for want of a readable answer, is answered as Envoy answers it, with one more header, content-length: 0; a check's 301, 302, 303, 307 or 308 to an HTTP/1.1 request that carries a body reaches the client with nginx's own page in place of the check's body, and a check's 304 to such a request with content-length: 0, as nginx discards the request's body, so that no byte of it is read as the next request; a denial reaches the client with Server: nginx where the check sent no Server header, with nginx's own reason phrase (302 Moved Temporarily, where Envoy writes Found), without a header line the check sent with an empty value, with the check's Access-Control-Allow-Origin beside the listed origin's own, where Envoy sends the origin's alone, and, to a HEAD request, with Content-Length: 0, where Envoy sends transfer-encoding: chunked; an error in proxyfile.js itself as it asks the check or reads its answer is answered 403, with allow too, and writes no log line; the check is sent connection: close on a new connection for each check, where Envoy sends no Connection header and keeps its connections to the check open, so nginx's local ports bound how many checks a second it can send to one check address: near 340,000 a second when the check closes its connection first after it answers, derived from the 0.13% of connections nginx's side then kept in TIME\_WAIT and not reached, and about 470 a second when it does not, at most 28,231 checks to one address in any 60 s from the 28,232 ports of 32768 to 60999 (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2); past that bound a check fails for want of a port, and its request is answered 403 with deny and served with the unchecked mark with allow; the relay listens on a socket file, /run/proxyfile-\<listen>/check.sock, which nginx removes when it stops and a kill leaves, and nginx then does not start, its bind() to the file failing (98: Address already in use), until the file is removed or /run is a tmpfs.
- **`auth.timeout`**: with allow, the request goes on with the unchecked mark at the first byte the check sends after auth.timeout, or after one auth.timeout with no byte from it, so a check that sends a byte just before each auth.timeout of silence holds the request up to twice auth.timeout, where Envoy goes on at auth.timeout.
- **`auth.when_unavailable`**: with allow, a request whose check answers in a form Envoy reads as no answer (a malformed header line, framing or status line, a head or a trailer section past the 81,920 bytes Envoy reads, a head of more than 100 header lines with a name on two of them, such as set-cookie, or a body that never ends) is answered 403, where Envoy serves it with the unchecked mark, x-envoy-auth-failure-mode-allowed: true.
- **`auth.only_with_header`**: over HTTP/1.1 a request whose header is a lone tab is asked, where Envoy does not ask it: nginx keeps the tab, which Envoy removes; over HTTP/2 a cookie sent as two or three empty cookie fields is asked, where Envoy does not ask it. "auth.only\_with\_header" naming a header that remove\_from\_clients, auth.accept or the proxy itself removes from clients is refused by --target nginx: the Envoy output removes that header before it reads it and never asks the check, where nginx asks it; name a header the proxy passes on.
- **`auth.send`**: a client's own x-proxyfile-method and x-proxyfile-path are not sent to the check when send covers them, where Envoy sends them; what the check is asked does not change.
- **`auth.send_request`**: in a file with no pattern route, a path holding a character Envoy escapes before it picks a route (" \< > ^ \` { \| }) is told in x-forwarded-uri as the client wrote that character, where Envoy tells it escaped; the service receives it escaped from both; under client\_address forwarded, over HTTP/2 a request whose :scheme differs from its connection's scheme is told in x-forwarded-proto the x-forwarded-proto it arrived with, or the connection's when it arrived with none, where Envoy tells it the :scheme, and an x-forwarded-proto that is neither http nor https, such as ftp, is told as it arrived, where Envoy tells the connection's scheme.
- **`auth.accept`**: names written in full, none of them a header nginx writes itself on the request it passes on; a request the accepted headers take past limits.request\_headers is answered 500 as by Envoy, counted with the client's own Connection line, its own copies of the headers the proxy sets, each line of a name sent on several lines and each cookie field over HTTP/2, which Envoy drops, replaces or joins into one before it counts, so such a request can be answered 500 by those lines sooner; an accepted header the check sends empty reaches a gRPC service as no header, where Envoy sends it empty. "auth.accept" holding a name with a \* prefix is refused by --target nginx: nginx sets a request header only by a name its configuration writes; write each name in full. "auth.accept" naming a header nginx frames or routes a request by is refused by --target nginx: nginx writes this header itself on the request it passes on, to frame or route it or to keep a client's copy from the service, x-forwarded-for, x-envoy-external-address and the five x-envoy- names Envoy removes from clients only with client\_address: connection, and a check service must never change how nginx frames or routes that request; each is refused with either client\_address, so a list stays valid when it changes; remove the name from auth.accept.
- **`log`**: the time is the moment nginx writes the line, in whole seconds with the offset of nginx's time zone (+00:00 in UTC), where the Envoy output writes the request's start in milliseconds with Z; over HTTP/1.1 bytes\_out counts the chunk lines of an answer nginx sends chunked, where the Envoy output counts its body alone; an answer whose status has one, two or four digits, or that has no status line, logs the status 9, where the Envoy output logs the four digits or 502; a status that starts with 0 logs its number without the zeros (050 as 50), one with spaces between its digits the digits joined (2 0 0 as 200) and one with a fraction its whole number (403.1 as 403), each written only when that number is status\_from or above or sample takes the request, where the Envoy output writes 502 whatever status\_from says; bytes\_in counts the whole request, not its body; upstream\_ms counts from the opening of the service connection of the try that answered, not from the request's start; authz\_us counts the check's time in whole milliseconds, 0, 1000, 2000 and so on, where the Envoy output counts microseconds; there are no flags and details fields; a request of a retried route that is not tried again because of its body, longer than 1 MiB or of a length nginx does not know, writes the upstream-failure flag UF after its one try failed, where the Envoy output, which tries it again, writes URX,UF; a value the Envoy output writes null is written empty, but upstream\_ms; a request the origin or upgrade check refuses logs route and upstream empty, where the Envoy output names them; a request refused for a dot segment with a ; parameter logs route empty on each of its lines, where the Envoy output names the route that refuses it, refused:dot-segment-with-parameter; a request Envoy refuses as it reads it logs its host, method, request id and protocol, which Envoy writes null and HTTP/1.1; a request id or Host holding a byte above 0x7F is logged empty.
<!-- /keys -->
- **the request line and header names read before a route is picked**: a client that closes its side of the connection right after its request gets nginx's answer when nginx refuses the request itself, an Origin on none of the hosts, a target holding a fragment, a dot segment with a ; parameter or HTTP/1.0, where Envoy closes the connection with no answer. For a request a service answers, each proxy gives the client no answer when it reads the FIN before the service answers, and the service's answer when the answer comes first: with the FIN sent with the request, the service received 148 of 300 such requests through Envoy and 1 and 0 of 300 through nginx 1.30.6 and 1.31.8, and no client got an answer; with the FIN 1 ms later, the service received 298, 297 and 299 of 300, and nearly every client got its answer; with a service that answers 50 ms late, no client got an answer (measured with no `auth` beside Envoy 1.40.2, from the host through Docker's port forwarder). The same rule holds for both proxies, so it is not on the list of ways nginx is less strict. With the check asked it does not hold: a client that half-closed while its check was asked got no answer from either proxy (200 of 200 each), but once the check allowed the request nginx still sent it to the service, 17 and 23 times of 30 when it held idle connections to the service, where the Envoy output sent none; that, with a close and a reset, is on [the list of ways nginx is less strict](#where-nginx-is-less-strict-than-the-envoy-output).

### The request log with the check

With `auth.ask` the request line holds one more field, `authz_us`, which comes first in the line's alphabetical order, and
the file writes a third kind of line, as the Envoy output does (measured on Envoy 1.40.2 and on nginx 1.30.6 and
1.31.8):

- `authz_us` is a string of digits when a check ran for the request, and `null` when none ran: no header named by
  `only_with_header`, a route with `auth: off`, a preflight, or `auth.enabled: false`. The Envoy output counts it in
  microseconds, `"authz_us":"897"`; nginx counts the same time in whole milliseconds, so it reads `0`, `1000`, `2000` and
  so on.
- `{"action":"","event":"gateway_auth_unavailable","failure_mode_allowed":"true","request_id":"...","route":"shop","status":200}`
  is written for each request that went on with the mark under `allow`, with the fields of the Envoy output's line in its
  order; `action` holds the `action` of an rpc method (`browse`, `buy`) and is empty on any other route. Under `deny` no
  such line is written.
- A request refused for a dot segment with a `;` parameter logs `route` empty on each of its lines, where the Envoy output
  names the route that refuses it, `refused:dot-segment-with-parameter`, and `action` empty where the Envoy output writes
  `null`.

### What the check costs

nginx opens a new connection to the check service for each check and closes it after the answer. The Envoy output keeps
its connections to the check open, and held none in `TIME_WAIT` in its own network namespace in any run. The figures
below were measured on one Mac running Docker with 12 CPUs, on mains power, with no other container above 3% of one CPU
(programs on the Mac itself, a browser and the window server among them, also ran). They hold for nginx 1.30.6 and
1.31.8 beside the Envoy output on Envoy 1.40.2, for the configuration rendered from a file that asks one check service at
two addresses with `when_unavailable: deny` and `timeout: 1500ms`, with both outputs writing one request log line for
each request. A rate is the answers a second, counted by a client on the same Docker network.

The rates, the latencies, the memory and the reload below were taken before the relay's socket moved to a file in a
folder of its own and before the relay's queue was raised to the longest the kernel allows ([The relay's
socket](#the-relays-socket)), and were not taken again. The bursts below were taken after the queue was raised and before
the socket moved. All of them were taken with 1024 connections a worker, before a file with the check wrote 8192, which
holds 3.4 MiB more in each worker from its start ([Checks in flight on few workers](#checks-in-flight-on-few-workers)).

With a check service that closes its connection after it answers, as the battery's own stub does:

- Over 50 connections for 10 s, as fast as they go, nginx served 20,980 (1.30.6) and 20,273 (1.31.8) checked requests a
  second, where the Envoy output served 45,125: 0.47 and 0.45 of Envoy's rate. Held for 120 s, nginx served 13,392 and
  14,118 a second against Envoy's 35,745 (0.37 and 0.39), and no check failed.
- One checked request at a time, nginx answered at a median of 0.48 to 0.50 ms (p99 0.70 to 0.76 ms), where the Envoy
  output answered at a median of 0.23 ms (p99 0.43 ms).
- A request that is not asked (it carries no header `only_with_header` names) is answered as fast through nginx as
  through the Envoy output: 0.15 ms at the median and about 6,300 a second on one connection, for both.
- nginx's side of the connection to the check kept about 0.13% of its connections in `TIME_WAIT`: about 1,100 after
  60 s at 13,400 to 14,100 checks a second. At that share, one check address would use up nginx's 28,232 local ports
  only near 340,000 checks a second. That figure is derived from the measured share, not reached.

With a check service that does not close its connection right after its answer, nginx closes its connection to it first.
A check that closed 20 ms or more after its answer, and one that never closed, left 50 of 50 connections in `TIME_WAIT`
on nginx's side, on both versions, where a check that closed at once left none there, and the Envoy output kept its
connection and held none. `proxy_half_close on` did not change which side closed first (measured on nginx 1.30.6). Each
check then holds one of nginx's 28,232 local ports (`32768` to `60999`) for 60 s:

- nginx can send at most 28,231 checks to one check address in any 60 s, about 470 a second. Measured: 56,462 checks to
  two addresses in the first 60 s and 112,924 in 120 s, on both versions.
- Past that bound, each further check fails for want of a port, and with `when_unavailable: deny` the request is answered
  `403` with an empty body. With `allow` the request is served with the unchecked mark, so a request the check would
  refuse can be served: this is an entry of `auth.when_unavailable` on [the list of ways nginx is less
  strict](#where-nginx-is-less-strict-than-the-envoy-output). nginx writes two lines for each failed check, `[crit]
  connect() to <address> failed (99: Cannot assign requested address)` and `[error] recv() failed (104: Connection
  reset by peer)`, both below the `emerg` level the rendered file logs at, so the log shows neither. The note on
  `auth.ask` [below](#where-nginx-answers-the-check-otherwise-than-the-envoy-output) gives the same bound.

Clients that arrive at once, measured on mains power: 600, 1,200 and 2,400 connections opened at once, five requests on
each, so 3,000, 6,000 and 12,000 requests, from a container on the same Docker network, to the Envoy output and to each
nginx version in turn. Other containers ran on the Mac and their load was not recorded. The check service denied every
request in the runs under `allow`, and allowed every request in the runs under `deny`.

| Connections at once | Under `allow`, a check that denies every request: Envoy served with the unchecked mark | nginx served | Under `deny`, a check that allows every request: Envoy answered `403` | nginx answered `403` |
|---|---|---|---|---|
| 600 | 13 of 3,000 | none | 12 of 3,000 | none |
| 1,200 | 31 of 6,000 | none | 23 of 6,000 | none |
| 2,400 | 436 of 12,000 | none | 325 of 12,000 | none |

nginx 1.30.6 and 1.31.8 each answered `401` to every request under `allow` and served every request under `deny`. The
Envoy output's other requests were answered as the check said: `401` under `allow`, and served under `deny`.

When the check does not answer well, each run below sent requests on 400 connections for 15 s:

- When the check never answers, nginx answers each request `403` at the 1500ms deadline, at a median of 1,501.5 to
  1,501.7 ms, as the Envoy output does (1,500.7 ms). A request that is not asked, sent beside those 400 connections, is
  answered at a median of 0.16 ms on both.
- With those 400 connections whose check never answers, nginx's 12 workers together grew from 86 to 88 MiB to a peak of
  224 to 229 MiB, where the Envoy output grew from 81 to 99 MiB.
- When the check closes the connection without answering, nginx answers `403` at a median of 12 to 13 ms (432,671 and
  469,521 answers to 400 connections in 15 s), and the Envoy output at 7.5 ms (738,754 answers).
- When the check answers a `200` whose body never ends, nginx refuses it at the 1 MiB bound and answers `403` at a median
  of 68 to 71 ms, with its workers peaking at 1.1 to 1.2 GiB. The Envoy output answers `403` at a median of 593 ms and
  peaked at 8.4 GiB.
- When the check answers a 1 MB denial to 400 connections at once, nginx passed every denial on: 50,960 and 51,288 `401`
  answers in 15 s, none left without an answer. Its check location wrote no temporary file: 0 files under nginx's
  temporary directories during and after, with 1.4 GB free on the disk. In that run nginx's workers together peaked at
  2.2 GiB (1.30.6) and 2.0 GiB (1.31.8) from 87 MiB, and still held 1.8 and 1.4 GiB half a minute after the last answer.
  The Envoy output, for 102,094 such denials, peaked at 219 MiB from 67 MiB. During those denials a request that is not
  asked, on one connection beside them, was answered at a median of 6.0 ms through nginx (p99 15 to 16 ms) against 0.21
  ms through the Envoy output (p99 3.1 ms). The check service decides how large its denials are: these figures are for
  denials of 1 MB.

#### Checks in flight on few workers

Each checked request in flight holds four of one worker's connections: the client's, the check location's to the
relay's socket, the relay's from it and the relay's to the check. A worker with no connection left cannot open the
relay's connection, which reads as an unanswered check, so with `allow` the request went on with the unchecked mark and
the check was never asked. The Envoy output asks at most 1,024 checks at once, one on each of its check cluster's 1,024
connections, holds up to 1,024 more waiting and serves the rest with the mark: of 4,000 requests at once, its check
received 1,024 in the first 200 ms and the next only as those were answered, and with a check that answers after 500 ms
it served some with the mark from 1,200 at once, never asked (measured on Envoy 1.40.2). A file with the check therefore
writes `worker_connections 8192` and `worker_rlimit_nofile 8192`, so one worker carries 2,048 checks, twice what the
Envoy output asks at once, and a host's lower limit of open files does not stop it first; a file with no check keeps
1024 and writes no `worker_rlimit_nofile`.

The table counts the requests the check would refuse that were served with the mark, the check never asked, under
`allow`, with a check that denies each request 500 ms after it is asked: N connections opened at once, one request on
each, from a container on the same Docker network, with `worker_processes` 2 or 4 written in place of `auto` in the
rendered file and nothing else changed, on mains power, beside containers of other programs whose load was not
recorded. Each nginx cell at 1024 gives nginx 1.30.6 and then 1.31.8; the cell at 8192 holds two runs of each version
with each count, and the Envoy output's cell its three runs:

| At once | The Envoy output | nginx at 1024, 2 workers | nginx at 1024, 4 workers | nginx at 8192, 2 or 4 workers |
|---|---|---|---|---|
| 300 | 0 | 0 and 0 | 0 and 0 | none |
| 500 | 0 | 10 and 0 | 0 and 0 | none |
| 800 | 0 | 231 and 234 | 0 and 0 | none |
| 1,200 | 2 to 51 | 389 and 336 | 209 and 221 | none |
| 1,600 | 22 to 79 | 236 and 304 | 323 and 294 | none |
| 2,000 | 101 to 199 | 164 and 236 | 611 and 195 | none |
| 2,400 | 368 to 469 | 187 and 215 | 80 and 617 | none |
| 3,000 | 957 to 1,119 | 420 and 382 | 314 and 697 | none |
| 4,000 | 1,952 to 1,971 | 368 and 390 | 864 and 818 | none |

At 8192 nginx asked the check for every request and answered each `401`. At 1024 it wrote `1024 worker_connections are
not enough` at `alert` for each request it could not ask, a level the file's `emerg` log does not show. Each worker
allocates its table of connections as it starts: at 8192 each worker held 10.6 MiB at start where it held 7.1 MiB at
1024, about 500 bytes for each connection, on both versions, so the 12 workers above held 127 MiB at start where they
held 85 MiB (`VmRSS`, read from `/proc`). Each worker's limit of open files was 8192, where the container's own was
1048576.

One bound is left: nginx counts a client's and a service's connections against the same 8192, so a worker with none
left still serves a request unasked under `allow`, where the Envoy output's check keeps its own 1,024 connections
whatever else the proxy carries, an entry of `auth.when_unavailable` on
[the list of ways nginx is less strict](#where-nginx-is-less-strict-than-the-envoy-output).

### Reloading with checks in flight

Three `nginx -s reload` commands, run 10, 20 and 30 s into 40 s of checks that each took 500 ms, broke no request: each
of the 1,609 (nginx 1.30.6) and 1,607 (nginx 1.31.8) requests was answered `200` after a check of its own of at least
500 ms. None was answered `502`, none was refused and none reached the service without a check. This was measured with
the same file and Mac as above, with `when_unavailable: deny`, on nginx alone. A reload under `allow` and a reload of the
Envoy output were not measured. That run was taken before the relay's socket moved to a file ([The relay's
socket](#the-relays-socket)). After the move, one reload on each image left a request that the check allows answered `200`
and one that it denies answered `401`, as before it.

## The request log

Without `log`, nginx writes one JSON line to standard output for every request. With `log`, it writes a line for a
request whose status is `status_from` or higher, `600` to `999` included, for `sample` of the rest, and for a request
the Envoy output marks with a response flag below `status_from`. nginx has no response flags, so a condition on the answer stands in for them; it
covers the flags NR, DPE, SI, UC, UF and UT and a gRPC call, as measured. A `sample` of `100%` writes every request,
and with `0%` only the status and the flags choose. The share is drawn by a hash of the request id, not by its first
eight hex digits, as the note of the `log.sample` row says. nginx also writes the line for some requests it answers
itself and Envoy serves, such as one it answers `400`. The line holds no path, query, address, user agent or referrer,
as the Envoy output's.

The fields are the Envoy output's but `flags` and `details`, in the same order, which is alphabetical: `bytes_in`,
`bytes_out`, `duration_ms`, `host`, `method`, `protocol`, `request_id`, `route`, `status`, `time`, `upstream` and
`upstream_ms`. With `auth.ask` the line also holds `authz_us`, first in that order ([The request log with the
check](#the-request-log-with-the-check)). `status`, `bytes_in`, `bytes_out` and `duration_ms` are bare numbers, `duration_ms` and `upstream_ms` are
whole milliseconds, and `upstream_ms` is `null` when no service answered. A status is written with no leading zero: `0`
for an HTTP/2 stream a client cut in its headers, as Envoy writes it, and `9` for an answer nginx reads with no status
line, one whose status has one, two or four digits or none, where Envoy writes the status the service wrote, `2000`, for
four digits and `502` for the others; nginx writes that line whatever `status_from` says, as Envoy writes both. A status
nginx reads as three other digits is written as their number, `050` as `50`, `2 0 0` as `200` and `403.1` as `403`, and
its line only when `status_from` or `sample` takes it, where Envoy writes `502` with the flag `UPE` whatever
`status_from` says. `protocol` is `HTTP/2` over HTTP/2. A request id with a quote, a backslash and a tab in it is
escaped as Envoy escapes it, `"q\"b\\c\td"`, and `bytes_out` was equal on the same answer with a `Content-Length` (227).
A service's chunked answer of 5 bytes is sent on chunked over HTTP/1.1, and nginx counts its chunk lines, 15 bytes,
where Envoy counts 5; over HTTP/2 both count 5. The `time` is the moment nginx writes the line,
`2026-11-15T13:12:45+00:00` for a request sent at 13:12:42.880 and answered 2.5 s later, where Envoy writes the
request's start, `2026-11-15T13:12:40.322Z` for one sent at 13:12:40.322. A start time written by `proxyfile.js`, or the
line's time with milliseconds from a map, would cost a worker CPU on every line, so the two stay differences. Measured
on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2. The differences, which nginx's `log` row declares in these words: the
time is the moment nginx writes the line, in whole seconds with the offset of nginx's time zone (`+00:00` in UTC), where
the Envoy output writes the request's start in milliseconds with `Z`; over HTTP/1.1 `bytes_out` counts the chunk lines
of an answer nginx sends chunked, where the Envoy output counts its body alone; an answer whose status has one, two or
four digits, or that has no status line, logs the status 9, where the Envoy output logs the four digits or 502; a status
that starts with 0 logs its number without the zeros (`050` as 50), one with spaces between its digits the digits joined
(`2 0 0` as 200) and one with a fraction its whole number (`403.1` as 403), each written only when that number is
`status_from` or above or `sample` takes the request, where the Envoy output writes 502 whatever `status_from` says;
`bytes_in` counts the whole request, not its body; `upstream_ms` counts from the opening of the service connection of
the try that answered, not from the request's start; `authz_us` counts the check's time in whole milliseconds, `0`, `1000`, `2000` and so on, where
the Envoy output counts microseconds; there are no `flags` and `details` fields; a request of a retried route that is
not tried again because of its body, longer than 1 MiB or of a length nginx does not know, writes the upstream-failure
flag `UF` after its one try failed, where the Envoy output, which tries it again, writes `URX,UF`; a value the Envoy
output writes `null` is written empty, but `upstream_ms`; a request the origin or upgrade check refuses logs `route`
and `upstream` empty, where the Envoy output names them; a request refused for a dot segment with a `;` parameter logs `route` empty on each
of its lines, where the Envoy output names the route that refuses it, `refused:dot-segment-with-parameter`; a request
Envoy refuses as it reads it logs its host, method, request id and protocol, which
Envoy writes `null` and `HTTP/1.1`; a request id or `Host` holding a byte above 0x7F is logged empty.

A second line, `{"event":"gateway_upstream_failure","flags":...,"route":...,"status":...,"upstream":...}`, is written
for a request whose service failed, as the Envoy output writes it: `UF` for a service that refused the connection, `UPE`
for an answer cut short of its `Content-Length`, for a gRPC answer whose body is shorter or longer than its
`Content-Length`, which nginx ends itself, and for one whose head nginx refuses as malformed, and `UC` and `UT` as Envoy
writes them. A call to an RPC route with `retries` whose last try was refused, or did not take the connection within
5 s, gets `URX,UF`, the flags Envoy writes for a call its retries ran out on, after one try at a service of one backend,
which nginx tries no second time, after two at a service of two, and after the one try nginx gives a call that is not a
`POST`. On a route with `retries` the line reads the last try too: `URX,UF` when every try was refused, at a service
of one, two or eight backends, over HTTP/1.1 and HTTP/2 and for a `PUT` with a length; `UT` when the route's 15 s ran
out, a gRPC call's too; no line when the idle time ended the request, as Envoy writes none for it; `UC` for a try
reset after the head that followed a refused one; and no line for a request a retry served. A request the copy that
tries once sent, a body over 1 MiB or of a length nginx does not know, gets `UF` when its one try was refused, where
the Envoy output, which tries such a request again, wrote `URX,UF`, or answered one over its bound `507` with no line.
The request line holds the `upstream_ms` of the try that answered (each measured on nginx 1.30.6 and 1.31.8 beside
Envoy 1.40.2). A gRPC answer whose body matches its `Content-Length` gets no second line, as with Envoy, though
nginx counts the frames of a gRPC answer, not its body, in `$upstream_response_length`; a `HEAD` request gets none, and
a request the idle time ended gets none, as with Envoy. A service's answer whose head nginx refuses is answered `502`,
as Envoy answers it, for each of a space before a header's colon, a NUL in a header value, a `Content-Length` that is
not a number, a head that ends before its blank line and a status line cut in the middle (measured on nginx 1.30.6 and
1.31.8 beside Envoy 1.40.2). Two cases nginx does not write, both in [the list
below](#where-nginx-is-less-strict-than-the-envoy-output): a chunked answer its service cuts short after a chunk, which
Envoy logs with `UPE` and a failure line, is recorded by nginx as complete, and nginx writes neither; and an answer with
a header line nginx passes on where Envoy answers `502`, which Envoy logs with `UPE`.

## Tracing

With `tracing` and its `otel` provider, nginx sends a span of each sampled request to an OpenTelemetry collector through
its OpenTelemetry module, `ngx_otel_module`, which the official images tagged `-otel` carry, such as
`nginx:1.31.8-otel`, and the plain images do not. Line 2 of `nginx.conf` then names the module beside njs, and the file
loads it with `load_module modules/ngx_otel_module.so;`. On a plain image `nginx -t` stops at that line, with `dlopen()
"/etc/nginx/modules/ngx_otel_module.so" failed (... No such file or directory)`, and the same file loads on the image
tagged `-otel` of the same version. A file without `tracing` loads no module and writes none of the tracing lines, so it
loads on either image. Each behaviour this section names was measured on nginx 1.30.6 and 1.31.8, each on its image
tagged `-otel`, beside the Envoy output on Envoy 1.40.2, and the battery's tracing rows run on both
([Conformance](conformance.md)). The `xray` provider is refused ([What nginx refuses of
tracing](#what-nginx-refuses-of-tracing)).

### What the file writes

The spans go as OTLP over gRPC, with no TLS and no credentials, to the endpoint (`otel_exporter`), with `service` as
their `service.name` (`otel_service_name`), as the Envoy output sends them.

- **The collector.** An IP address is asked directly, as in `otel_exporter { endpoint 10.0.3.40:4317; }`. A DNS name,
  which needs `dns.resolver` ([What nginx refuses of tracing](#what-nginx-refuses-of-tracing)), is reached through a
  relay that the file writes in the `http` block, because the module looks a name up only once in each worker, as the
  worker starts. For `listen: 8000`, `dns: {resolver: "10.0.0.2:53"}` and the endpoint `otel-collector:4317`:

  ```
  otel_exporter { endpoint unix:/run/proxyfile-8000/collector.sock; }
  resolver 10.0.0.2:53 valid=5s;
  scgi_temp_path /run/proxyfile-8000;
  upstream proxyfile_8000_collector {
    zone proxyfile_8000_collector 1m;
    server otel-collector:4317 resolve max_fails=0;
  }
  server {
    listen unix:/run/proxyfile-8000/collector.sock;
    http2 on;
    otel_trace off;
    access_log off;
    client_max_body_size 0;
    max_headers 100;
    client_header_buffer_size 64;
    large_client_header_buffers 1 61376;
    client_body_timeout 10s;
    send_timeout 10s;
    location / {
      grpc_pass grpc://proxyfile_8000_collector;
      grpc_connect_timeout 5s;
      grpc_read_timeout 10s;
      grpc_send_timeout 10s;
    }
  }
  ```

  The upstream looks the name up through `dns.resolver` and keeps the answer 5 s, as a service's upstream does, and
  takes each address of it into a zone of 1 MiB, as a service's upstream does: a name giving 1,000 addresses fitted on
  both versions, and for one giving 2,000 nginx wrote `ngx_slab_alloc() failed: no memory in upstream zone` and `cannot
  add new server to upstream ..., memory exhausted`, its requests still answered `200`. It keeps no idle connection, so
  each call of the tracer opens its own, and a call after the name moved goes to the new address. The relay's server
  takes nothing from what the file says of its clients' requests: it writes no span and no request log line, no rate
  limit and no check reach it, and it keeps the header limits of a file with no `limits` key, which hold the tracer's
  few headers whatever `limits` says. With `limits.request_headers` at a count of 3 and those limits taken from the
  file, nginx refused every call of the tracer and no span of 61 arrived; with the relay's own, all 61 did (nginx
  1.31.8). Its calls count in the admin port's `/stats` all the same, as `stub_status` counts every request nginx serves
  and has one directive, `stub_status`, which takes no argument and leaves no server out: after 5 traced requests and a
  read of `/stats`, its count of requests had grown by 11, the tracer's 5 calls among them, on both versions. Its times
  are its own too: 5 s to connect, as the Envoy output's collector cluster, which writes no connect time and so takes
  Envoy's 5 s, and 10 s for each read and write, twice the 5 s the slowest collector measured took to answer, so that a
  call to a collector that never answers ends, and the module's batch comes back. The module gives its calls no deadline
  and keeps four batches of spans in each worker, each back only when its call ends. A batch's body has no size limit,
  as the tracer is the relay's only client, on a socket only nginx's users can reach: the largest file render accepts,
  453 tags each on a header, went through the relay at 100 requests a second for 30 s, with a collector that answers at
  once and with one that answers after 5 s, and at 2,000 a second for 10 s, in batches of up to 512 spans, and every
  span arrived, no call was answered `413`, and nginx wrote no request body to a file.
  The socket file is in the folder of the check's socket, which the file names once with `scgi_temp_path` for one
  relay or both ([The relay's socket](#the-relays-socket)), with the same modes, and both relays work side by side in
  it: with the check asked and the collector by name, 61 checked requests and 61 others were answered `200` and 122
  spans arrived. nginx removes the file when it stops, and a stop and a start, and a reload 10 s into 1,000 requests a
  second for 30 s, answered every request `200`; of three reloads through the relay and two asking the collector's
  address on each version, one lost spans, 394 of 30,001 through the relay on 1.30.6, and the others none. A kill
  leaves the file, as it leaves the check's, and nginx then does not start in the same container, writing `[emerg]
  bind() to unix:/run/proxyfile-10000/collector.sock failed (98: Address already in use)` for `listen: 10000`, until
  the file is removed or `/run` is a `tmpfs`: with either, a kill and a start in the same container started and sent
  every span. The row of `tracing.provider.otel.endpoint` declares it.

- **The draw.** `otel_trace` reads a variable `proxyfile.js` sets to `on` for a request the Envoy output would trace:
  the first eight characters of its `x-request-id`, after an optional `0x`, read as hexadecimal, modulo 10,000, below
  100 times `sample.rate`. An id shorter than eight characters, or with another character among them, is never traced,
  and an id that is not a UUID is drawn by those characters: at `rate: 5%`, `000001f3x` and `00000000z` are traced, and
  `0001f3`, `1234567`, `abc`, `+00001f3...` and `-00004b0...` never, through both proxies. The id is the one the service
  receives: the proxy's own under `client_address: connection`, and under `forwarded` the client's own when it sent one,
  so a client that chooses its id chooses the draw ([Tracing](tracing.md#opentelemetry)). The draw and the span's
  `guid:x-request-id` read the id as the Envoy output reads it, without a space or a tab at either end, and the service
  receives the client's id as sent, as through a file with no `tracing`. The admin listener writes no span (`otel_trace
  off`), as the Envoy output's writes none.
- **Which requests get a span.** nginx's module adds the target as the client wrote it, as `http.target`, and the
  User-Agent as nginx keeps it, as `http.user_agent`, to every span, and no line removes either, so the draw picks only
  among requests whose span holds nothing of either that the Envoy output's does not. A span is written only for a
  request whose target nginx sends the service as the client wrote it, of at most 256 bytes, on a route with no rewrite
  and, under `client_address: forwarded`, with no `x-envoy-original-path` from the client: a target that holds a dot
  segment, `//`, an escaped unreserved character or `%2F`, or a character Envoy escapes, such as `^` in the path, gets
  no span, and neither does one of 257 bytes, where the Envoy output writes a span holding the path it normalised,
  escaped or rewrote, cut to 256 characters, or the client's `x-envoy-original-path`. Over 47 targets and 4 routes with
  a rewrite, each `http.url` and `http.target` nginx wrote held bytes of the Envoy output's `http.url`. A request whose
  User-Agent has a tab at either end over HTTP/1.1, which nginx keeps and the Envoy output's `user_agent` removes, gets
  no span either. Such a request reaches the service and the check with the trace marked not sampled.
- **The client's trace.** `otel_trace_context extract` lets the module continue a client's `traceparent` of version
  `00`, and `proxyfile.js` writes the `traceparent` and `tracestate` each route's location sends the service, in place
  of the client's. A sampled parent whose request the draw picks continues the client's trace: the service receives the
  client's trace id with the span's own id as its parent, and the span is the client's span's child. A sampled parent
  the draw does not pick, and a parent marked not sampled, give no span, and the service receives the client's trace id
  marked not sampled. A `traceparent` that says sampled does not force a trace, and neither do `x-client-trace-id` and
  `x-envoy-force-trace`. A malformed `traceparent`, such as one of version `01` with a fifth part, an all-zero trace or
  parent id, `00-abc-def-01` or the flags `0g`, reaches the service as the client sent it, each line and each byte, 0xFF
  among them, with its `tracestate` lines as sent, and no span, as through the Envoy output. Of two `traceparent` lines
  whose first is well formed the first is read and one line is sent, and flags such as `03`, or hexadecimal written in
  capitals, read as sampled.
- **A `traceparent` the module does not continue.** The module does not continue a `traceparent` of another version than
  `00`, such as `01` or `ff`, nor, over HTTP/1.1, one whose line or whose first `tracestate` line has a tab at either
  end, which it reads with the tab, where the Envoy output continues the client's trace with version `00`, a parent id
  of its own and a span. Such a request gets no span, and the service and the check receive the client's version, trace
  id and parent id with the sampled flag the Envoy output sends: sampled only when the client marked it so and the draw
  picks the request. The forms tried for a version other than `00`: `otel_trace_context propagate`, with which the
  module reads and writes the header itself, started a trace of its own for a parent of version `01` and for one of
  `ff`, with a new trace id, where the Envoy output kept the client's (nginx 1.31.8); `extract`, with the client's
  header passed on as sent, kept the client's flag whatever the draw said, and wrote no span; and a new trace the draw
  decides lost the client's trace id (both versions). The form written keeps the client's trace id and sends the Envoy
  output's flag.
- **A gRPC call with a malformed `traceparent` on more than one line**, or beside more than one `tracestate` line, is
  answered `400` by a traced gRPC location and reaches no service, where the Envoy output passes each line on as a field
  of its own. The form tried: one `grpc_set_header` whose value holds a line break between the lines, the form the
  HTTP/1.1 locations use, made nginx send one field holding the line break, which an HTTP/2 field may not hold; the
  service's HTTP/2 server closed the connection with `PROTOCOL_ERROR`, and the call reached no service while the client
  got `200` with no message (both versions). A `grpc_set_header` for each line a file admits would add two lines to
  every gRPC location for each header line `limits.request_headers` allows, and was not written.
- **`tracestate`.** A client's lines are joined with commas, `a=1` and `b=2` as `a=1,b=2`, each line without a space or
  a tab at either end and its bytes as sent, 0xFF among them, and sent only beside a `traceparent`; a client's
  `tracestate` without one is dropped, as by the Envoy output. The span carries the client's first `tracestate` line,
  which the module reads, where the Envoy output's carries the lines joined, so the two are equal for one line. When no
  `tracestate` goes on, the Envoy output adds an empty `tracestate:` line beside its own `traceparent`, and nginx sends
  none; and of a `traceparent` or a `tracestate` the client sent as one empty line beside a malformed `traceparent`,
  which the Envoy output passes on, nginx sends none. Over 300 random forms of the two headers, half over HTTP/2, the
  service received from nginx what it received from the Envoy output in each but those empty lines.
- **The check.** The check's location sends the check the `traceparent` and `tracestate` the service receives, so the
  check's own spans join the request's trace with the request's span as their parent, and neither beside a malformed
  `traceparent`, as the Envoy output's check request then carries none. Over 32 forms of a client's trace (none, version
  `00` sampled and not, malformed, version `01` and `ff` sampled and not, each without and with a `tracestate`, for a
  request the draw picks and one it does not), whether the check and the service each received each header, the trace
  id, the flag and the `tracestate` equalled the Envoy output's in each of 256 comparisons, an empty `tracestate` line
  read as none. Two differences stay: the Envoy output writes a span for its request to the check, `async <service>
  egress`, which is the parent that request names, and nginx writes no span for the check; and the Envoy output's empty
  `tracestate:` line.
- **The span's name and fields.** A span is named after the route (`otel_span_name`), as the Envoy output names it, and
  `ingress` when no route takes the request or its path is one both proxies refuse. It carries the Envoy output's
  fields, each written with `otel_span_attr`: `node_id`, `zone`, `guid:x-request-id`, `http.url`, `downstream_cluster`,
  `user_agent`, `http.protocol`, `peer.address`, `request_size`, `response_size`, `component`, `upstream_cluster` and
  `upstream_cluster.name`, whose values equalled the Envoy output's over HTTP/1.1 and HTTP/2, with a body, for a `503`
  and for a gRPC call. The scheme of `http.url` is `http` or `https` alone, as in the Envoy output's: an absolute
  target's own, so `https` for one sent over plain HTTP under `client_address: connection` too, though the service and
  the check are sent `http` for it and the Envoy output answers it `403` with no span (measured on nginx 1.30.6 and
  1.31.8 beside Envoy 1.40.2), otherwise, under `client_address: forwarded` over HTTP/1.1, a client's
  `X-Forwarded-Proto` that is one of the two words in any letter case, with spaces or tabs around it, and otherwise the
  connection's; over HTTP/2 nginx writes the connection's, where the Envoy output writes the client's `:scheme` when it
  is `http` or `https`. Its host is the one the Envoy output's holds: the route's service's `host_header` when it writes
  one, and otherwise the host the request names. Beside them the module sets fields of its own, which no line removes:
  `http.method`, `http.target`, `http.route`, `http.scheme`, `http.flavor`, `http.user_agent`, `http.status_code`,
  `http.request_content_length`, `http.response_content_length`, `net.host.name`, `net.host.port`, `net.sock.peer.addr`
  and `net.sock.peer.port`.
  `http.route` holds the name of the location nginx generated for the route, such as `@pf_r_1`, not the route's name,
  which the span's name holds, and `net.host.name` the server's name, or, on the server that answers a host no
  environment names, the `Host` the client sent, which that span's `http.url` holds too. None of them carries a header
  or a token that the Envoy output's fields do not: `http.target` holds bytes of the Envoy output's `http.url`, and
  `http.user_agent` the Envoy output's `user_agent`, as only such requests get a span (above). `http.status_code` is
  nginx's own, a number, `200`, where the Envoy output writes the text `"200"`: an `otel_span_attr` of a name the module
  sets is added beside the module's attribute, not over it, as `http.target` shows ([What nginx refuses of
  tracing](#what-nginx-refuses-of-tracing)). `response_flags`, `error` and the gRPC attributes are not set, and a span's
  status is an error from `500` on.
- **Tags.** One function of `proxyfile.js` reads the sources of a request's tags in one pass over its lines, each header
  and each variable once however many tags read it, and writes each source's value into a variable of its own, which
  each tag's `map` reads. A header tag reads the first line of the client's header as the Envoy output reads it, without
  a space or a tab at either end, and, for a header whose lines the Envoy output joins, such as `accept`,
  `authorization` or `cache-control`, every line, joined with commas as it joins them, `a` and `b` as `a,b`; its value
  keeps a quote, a backslash, a tab inside it and bytes above 0x7F as sent. A tag on a header the proxy writes for the
  service reads what the service receives: on `x-request-id` the id the proxy sends, on an `auth.accept` name the value
  the check accepted, or the tag's default on a route the check is off for, and on `x-forwarded-proto`, and on
  `x-forwarded-for` under `client_address: connection`, the proxy's own values; a tag on a header the proxy removes from
  clients is never set. An `env` tag reads the variable from nginx's environment, which the file keeps for it with `env
  <NAME>;`, each byte as set, 0x01 and 0x08 among them; a variable named like a property every object has, such as
  `constructor` or `toString`, is read as unset, as njs leaves such names out of `process.env`, where the Envoy output
  reads it. A tag named like a field of the span takes the field's place, and keeps the field's value when its source
  gives none or an empty one, as through the Envoy output: `component` stays `proxy`.
- **Requests answered before a route.** Over 50 kinds of request the file answers without passing them on, listed from
  where it answers (each `return` and answer location, the server that answers a host no environment names, the admin
  listener and nginx's own parser) and each sent with an id the draw picks, nginx wrote a span where the Envoy output
  wrote one, or none, and never one where the Envoy output wrote none. Both write a span for a host no environment names
  and a request no route takes (`404`), an origin the origin check refuses (`403`), a preflight, the check's `401` and
  `403`, a check that does not answer in time, the rate limit's `429`, a service that refuses the connection (`502`),
  and an escaped control byte, which the Envoy output serves and nginx answers `400`, each with the same `http.url`.
  Neither writes one for a refused upgrade (`403`), on its own or to a path a route refuses, a fragment in the target,
  `HTTP/1.0`, a method such as `FOO`, a `QUERY` with no `Content-Type`, a header name outside the token characters,
  `HTTP/1.10`, `HTTP/2.0` on an HTTP/1 connection, the byte 0x80 or 0x7F in the target, an escaped NUL, no `Host`,
  `OPTIONS *`, an absolute target of another scheme than `http` and `https`, and over HTTP/2 `te: gzip` and a header
  value holding 0x01. nginx writes none where the Envoy output writes one for the requests it answers `400` or `405`
  before a route, each declared on its row ([What nginx writes with no key](#what-nginx-writes-with-no-key)):
  `Connection: foo`, a dot segment with a `;` parameter, `%5C`, a path to normalise beside an escape Envoy keeps and
  such an escape on a route with a rewrite, an invalid escape such as `%zz`, `TRACE`, `CONNECT` and a tab between the
  method and the target. A path both proxies refuse, `/open/..%3B/`, is answered `400` with a span named `ingress` by
  both.
- **A client's copies of three headers.** With tracing, every location that passes a request on, and the check's, also
  writes `x-envoy-decorator-operation`, `x-envoy-downstream-service-cluster` and `x-envoy-downstream-service-node`
  empty, under either `client_address`, so a client's copies reach neither the service nor the check, as the Envoy
  output removes them with the `otel` provider; `auth.only_with_header` may not name them then ([The auth
  check](#the-auth-check)).
- **A retried route.** Each try of a request sends the service the same `traceparent` and `tracestate`, so the request
  has one span, whose ids are those the service read on the try that served it, with the last try's `upstream_cluster`
  and status, as through the Envoy output: measured with the first connection refused, with every connection refused,
  which gave one span with the status `502`, and with a body over 1 MiB, which is sent once.

### Where nginx traces otherwise than the Envoy output

The differences, which nginx's rows in [the support table](portability.md#support-by-proxy-from-the-generators) declare
in these words:

- **`tracing`**: needs ngx_otel_module, in the nginx images tagged -otel, where the plain images stop at load_module.
- **`tracing.provider.otel`**: spans carry the Envoy output's attribute names with nginx's own beside them, http.route
  holding the name of the location nginx generated; http.status_code is a number, response_flags, error and the gRPC
  attributes are not set, and an attribute with no value, such as upstream_cluster on a request no route takes, is sent
  empty, where Envoy sets none; over HTTP/2 http.url holds the connection's scheme, where Envoy writes the client's
  :scheme when it is http or https; a span's tracestate holds the client's first tracestate line, where Envoy's holds
  the lines joined; a traceparent of another version than 00, or one whose line or first tracestate line has a tab at
  either end over HTTP/1.1, reaches the service and the check with the client's version, trace id and parent id and the
  sampled flag the Envoy output sends, and no span is written, where Envoy continues the trace with version 00, a parent
  id and a span of its own; a request whose User-Agent has a tab at either end over HTTP/1.1, which nginx's own
  http.user_agent would hold, gets no span and is sent on not sampled, where Envoy traces it; a gRPC call whose
  malformed traceparent comes with a second traceparent or tracestate line is answered 400 and reaches no service, where
  Envoy passes each line on as a field of its own; the auth check gets the trace, with the request's span as its parent,
  but no span of its own, where Envoy writes a span for the check; a preflight and a request refused for its origin get
  a span named ingress with upstream_cluster empty, where the Envoy output names the span after the route and sets its
  service; and no empty tracestate line is sent to the service or the check, where Envoy adds one beside each
  traceparent of its own when no tracestate goes on, nor a traceparent or tracestate sent as one empty line, which Envoy
  passes on beside a malformed traceparent.
- **`tracing.provider.otel.endpoint`**: an IP address, or a DNS name with dns.resolver, which nginx follows through a
  relay of its own, a name giving at most about 1,000 addresses, as a service's does; the admin port's /stats counts
  each call of the tracer through the relay as a request; the relay listens on a socket file,
  /run/proxyfile-<listen>/collector.sock, which nginx removes when it stops and a kill leaves, and nginx then does not
  start, its bind() to the file failing (98: Address already in use), until the file is removed or /run is a tmpfs.
- **`tracing.tags.<tag name>.env`**: a variable set to an empty value, or unset with no default, gives the span an empty
  tag, where Envoy sets none, except a tag named like a field of the Envoy output's span, such as component, which keeps
  the field's value, as through Envoy; a variable named like a property every object has, such as constructor or
  toString, is read as unset, where Envoy reads it.
- **`tracing.tags.<tag name>.header`**: a header absent with no default, or sent empty, gives the span an empty tag,
  where Envoy sets none, except a tag named like a field of the Envoy output's span, such as component, which keeps the
  field's value, as through Envoy.
- **`tracing.path_in_url`**: true only; a span is written only for a request whose target nginx sends on as the client
  wrote it, at most 256 bytes, on a route with no rewrite and, under client_address forwarded, with no
  x-envoy-original-path, and any other is sent on not sampled with no span, where Envoy writes the path it normalised,
  escaped or rewrote, cut to 256 characters, or a client's x-envoy-original-path.

Tracing adds no entry to [the list of ways nginx is less strict than the Envoy
output](#where-nginx-is-less-strict-than-the-envoy-output): each difference above was measured and is declared on its
key's row, none sends the collector, the service or the check a header or a token the Envoy output does not send, and
no request waits for the collector ([What tracing costs](#what-tracing-costs)). A tab before or after a header value,
such as a client's `x-request-id` under `forwarded` or a `User-Agent`, reaches the service with tracing as without it,
one of the entries of that list.

### What nginx refuses of tracing

Each is refused at render by the key where the file writes it, in these words:

- **The `xray` provider**, and each key under it: "nginx has no X-Ray tracer; write the otel provider and send its spans
  to an OpenTelemetry Collector with the AWS X-Ray exporter, or render it for envoy". The official images carry one
  tracer, `ngx_otel_module`, in the images tagged `-otel`, and it sends OTLP alone: the modules folder of
  `nginx:1.31.8-otel` holds `acme`, `geoip`, `image_filter`, `js`, `xslt_filter` and `otel`, with the stream modules of
  `geoip` and `js`, and those of the plain images the same without `otel`.
- **`tracing` without `path_in_url: true`**: "nginx's tracer always puts the request's path and query in the span, which
  path_in_url: false keeps out; write path_in_url: true, or render it for envoy". The forms tried to keep the path and
  the query out of a span, each with `GET` and a query holding a token: the module's lines with `http.url` holding the
  scheme and the host alone, as the Envoy output writes it with `path_in_url: false`, gave a span whose `http.target`,
  the module's own, held the path and the query (on nginx 1.31.8, `GET /open/path-u131?token=secret` gave `http.target`
  `/open/path-u131?token=secret` beside `http.url` `http://localhost`, and 1.30.6 the same); and the same with
  `otel_span_attr http.target "";` gave a span with two attributes named `http.target`, the path and the query first,
  then the empty one (on both versions). A file with the `xray` provider is refused for its provider alone.
- **`tracing` beside a `remove_from_clients`, `auth.accept` or `auth.send` that names `traceparent` or `tracestate`**,
  in full or by a `*` prefix: "nginx writes both headers from the trace for the service and the check; leave the name
  out, or render it for envoy". What such a list asks for, a client's copy removed, the check's value set or the
  client's own copy sent to the check, is not what nginx sends, and it was not compared with the Envoy output.
- **A DNS name in `tracing.provider.otel.endpoint` in a file with no `dns.resolver`**: "nginx looks a name up only
  through the name server it is given; write dns.resolver, or write the collector's IP address", the words a service's
  name is refused in. The relay that follows the name looks it up through `dns.resolver`, and without the relay the
  tracer's own lookup follows no move of the name. The forms tried, each on nginx 1.30.6 and 1.31.8 with 50 requests a
  second, three runs of each of two events: the first collector leaving the network 20 s in and, 30 s later, a second
  starting at the name; and the name moving to a second collector with the first still up. The endpoint written as the
  bare name, or as `dns:///<name>:4317`, is looked up by gRPC once in each worker as the worker starts, 12 queries for
  12 workers and none after, and no span reached the second collector in the 300 s after either event. A relay in the
  `stream` block, whose upstream looked the name up every 6 s, sent none to it either: the tracer keeps one connection,
  and a stream relay picks its upstream once, when that connection opens. `dns://<name server>:53/<name>:4317` makes
  gRPC ask a name server the file names, but it asked that server nothing after either event and followed neither. The
  relay in the `http` block followed both ([What tracing costs](#what-tracing-costs)).
- **A tag on a header nginx writes itself**: "nginx rewrites each of host, origin, te, connection, upgrade,
  http2-settings, content-length, transfer-encoding, keep-alive, x-envoy-auth-failure-mode-allowed,
  x-envoy-external-address, traceparent and tracestate for the service, and its value in a span was not compared with
  the Envoy output's; tag another header". The row of `tracing.tags.<tag name>.header` in [the support
  table](portability.md#support-by-proxy-from-the-generators) names the same headers.
- **A tag on `referer` or on `expect`**, at `tracing.tags.<tag name>.header`: "the Envoy output removes a Referer it
  does not accept before its tags read the request, where nginx's tag would read the client's line; tag another header",
  and "the Envoy output answers an Expect itself and removes it before its tags read the request, where nginx's tag
  would read the client's line; tag another header". No list of the Referer forms Envoy removes is kept for it: of 104
  forms of one `Referer` line, Envoy removed 46 and nginx none ([Where nginx is less strict than the Envoy
  output](#where-nginx-is-less-strict-than-the-envoy-output)).
- **A tag longer than nginx reads**, at the tag's key, `tracing.tags.<tag name>`: one "whose default's nginx form is
  longer than nginx reads", as nginx reads no value longer than 4094 bytes between its quotes and the file writes each
  `$` of a default as `${pf_dollar}` and each `"` and `\` with a backslash before it; one "whose header or variable
  name, with those of the tags before it, is longer than nginx reads", as nginx reads the names of all tags from one
  value of at most 4094 bytes between its quotes; and a tag name "whose nginx form is longer than nginx reads", as every
  such value is refused ([the opening](#nginx)). Each says to write a shorter value, or fewer tags, or to render the
  file for Envoy. The longest of each that render, 453 tags on the headers `x-h001` to `x-h453`, a default of 4,094
  letters or of 341 `$`, a header name of `x-` and 4,077 letters, an environment variable's name of 4,079 letters and a
  tag name of 4,094, load on both images tagged `-otel` with no warning from `nginx -t`, and one letter, `$` or tag more
  is refused.

### What tracing costs

The figures below were measured on one Mac running Docker with 12 CPUs, so nginx ran 12 workers, on mains power, with no
other container above 3% of one CPU. Each is the median of five runs with their range in brackets, unless it names
another number of runs, for nginx 1.30.6 and
1.31.8 on their images tagged `-otel` beside the Envoy output on Envoy 1.40.2, all rendered from one file with
`sample.rate` at 100%, so each request makes a span, with two tags, one of an environment variable and one of a header,
and a route with the auth check beside one without; the proxies started afresh for each run, and a client on the same
Docker network sent the requests. One proxy is said to answer sooner, hold less or lose fewer spans than another only
where their ranges do not overlap. The answer times, the memory at rest and the collector's states were measured before
this version changed how an `env` tag's variable is read, and the memory under load with it.

With a collector that answers each call at once, every request was answered `200` and every span reached the collector
in each run: 201 requests one at a time, 201 one at a time through the check, then 1,000 a second for 30 s, of which
nginx sent a span for each of the 30,402 or 30,403 requests and the Envoy output for each of its 30,403 and for each
check. The same file without `tracing` ran beside it.

| | Envoy output | nginx 1.30.6 | nginx 1.31.8 |
|---|---|---|---|
| Median answer, one request at a time, with tracing | 1.43 ms (1.31 to 1.51) | 1.02 ms (0.70 to 1.29) | 1.00 ms (0.66 to 1.17) |
| The same without `tracing` | 1.41 ms (1.14 to 1.47) | 0.78 ms (0.67 to 1.15) | 0.87 ms (0.67 to 1.15) |
| Median answer through the check, one at a time, with tracing | 2.03 ms (1.58 to 2.12) | 2.48 ms (1.62 to 2.66) | 2.27 ms (1.69 to 2.62) |
| The same without `tracing` | 1.80 ms (1.08 to 1.96) | 2.27 ms (1.56 to 2.55) | 2.47 ms (1.55 to 2.54) |
| Median answer at 1,000 a second, with tracing | 0.21 ms (0.19 to 0.28) | 0.19 ms (0.19 to 0.21) | 0.18 ms (0.16 to 0.20) |
| The same without `tracing` | 0.23 ms (0.19 to 0.25) | 0.18 ms (0.17 to 0.29) | 0.18 ms (0.17 to 0.27) |
| Memory at rest, with tracing | 21.0 MiB (20.7 to 22.2) | 57.3 MiB (57.3 to 57.4) | 57.4 MiB (57.3 to 58.0) |
| The same without `tracing` | 20.7 MiB (20.1 to 21.5) | 50.1 MiB (50.0 to 50.2) | 50.2 MiB (50.1 to 50.2) |

On each proxy the answer times with and without tracing overlap, so these runs show no time that tracing adds to a
request. One request at a time, on a route with no check, nginx answered sooner than the Envoy output with tracing;
without it the ranges just overlap, and they overlap through the check and at 1,000 a second.

Tracing costs nginx memory under load, which it keeps. Over 300 s of 1,000 requests a second to the route with no check,
each proxy started afresh with nothing sent to it before, the memory Docker counted, in MiB, was as follows, one run of
each: nginx 1.31.8 and 1.30.6 with the collector asked by its address, and the file without `tracing` and the Envoy
output, taken before with the same file and client. Both runs of nginx with tracing started with no other container
above 3% of one CPU; another project's test containers used about one and a half CPUs at the end of 1.31.8's, and none
was above 3% at the end of 1.30.6's. Neither version was run for 300 s through the relay, with the collector found by
name, with this version's script.

| | At rest | 15 s | 60 s | 120 s | 180 s | 240 s | 300 s | 120 s after the load |
|---|---|---|---|---|---|---|---|---|
| nginx 1.31.8, by address | 58.1 | 208.2 | 216.5 | 219.7 | 222.1 | 223.0 | 222.7 | 223.0 |
| nginx 1.30.6, by address | 58.4 | 141.9 | 147.9 | 160.4 | 171.9 | 175.1 | 175.7 | 175.6 |
| nginx 1.31.8 without `tracing` | 51.3 | 55.3 | 55.1 | 56.6 | 56.7 | 57.9 | 57.6 | 55.8 |
| nginx 1.30.6 without `tracing` | 51.8 | 56.0 | 56.4 | 56.9 | 57.6 | 57.9 | 58.3 | 57.2 |
| Envoy output, by address | 22.9 | 25.3 | 25.0 | 25.1 | 24.9 | 24.6 | 25.3 | 25.3 |

nginx 1.31.8 came within 1 MiB of its highest at 150 s and 1.30.6 at 240 s, and neither gave more than 1 MiB of it back
in the 120 s after the load. The module keeps at most four batches of 512 spans in each worker, its defaults, which the
file keeps, and keeps each span's strings when a batch is sent, to fill them again: memory rises until each worker's
2,048 spans have held their longest values, 24,576 spans for the 12 workers here, and stays there. So the cost grows
with the number of workers, one a CPU (`worker_processes auto`): at the end of the load each worker held 19.9 to 22.4
MiB of its own on 1.31.8 and 15.9 to 20.7 MiB on 1.30.6, against 8.4 MiB at rest.
How `proxyfile.js` reads the tags of each request changes it. On nginx 1.31.8 asked by address, the script of proxyfile
2.2.0, which asked `process.env` with `hasOwnProperty` before it read an `env` tag's variable, held 523.5 MiB at 300 s
(520.9 to 524.5, three runs), each worker 38.7 to 49.8 MiB; the same script asking `process.env` once for each traced
request, with no other change, held 219.7 MiB, and with the tags read as before the script read their sources in one
pass, each source on its own and the values joined in one text, 266.3 MiB (one run each). Another container was above
3% of one CPU at the start of the 219.7 MiB run, and another project's test containers at the end of one of the three
runs and of both of these, up to about two CPUs. The three scripts send the same spans, so the module's batches are not
the one cause of what a worker holds: why the call with `hasOwnProperty` left memory held in each worker, outside the
script's own heap, was not found, and how much of what a worker holds now comes with the script's work for the tags and
how much with the module's spans was not measured. This version asks `process.env` once for each traced request and
takes the variable only when it is a string, which no name every object inherits gives. On nginx 1.30.6 the script of
2.2.0 was measured under load only through the relay, in one run of 900 s, so how much this version saves there was not
measured. A file with the module and one proxied location alone levelled at 57.2 MiB on 1.30.6 and 57.1 MiB on 1.31.8
(one run each).

With the collector in each of five states, 201 requests one at a time and then 5,001 at 500 a second over 16 connections
were sent to each proxy, each proxy started with the collector already in that state: up; an address nothing answers on;
a collector that answers each call after 5 s; one that answers each call `UNAVAILABLE`; and one that takes each call and
never answers. Every request of every run was answered `200`, and no state's median is above the range with the
collector up, on any proxy, so no request waited for the collector:

| Collector | Envoy output, one at a time / at 500 a second | nginx 1.30.6 | nginx 1.31.8 |
|---|---|---|---|
| up | 1.09 ms (0.87 to 1.54) / 0.39 ms (0.38 to 0.40) | 0.98 ms (0.79 to 1.21) / 0.33 ms (0.33 to 0.45) | 0.83 ms (0.74 to 1.25) / 0.34 ms (0.33 to 0.39) |
| nothing answers its address | 1.01 ms (0.91 to 1.37) / 0.38 ms (0.36 to 0.54) | 0.83 ms (0.74 to 1.10) / 0.33 ms (0.32 to 0.34) | 0.75 ms (0.71 to 1.19) / 0.34 ms (0.33 to 0.39) |
| answers after 5 s | 1.06 ms (0.90 to 1.37) / 0.39 ms (0.32 to 0.46) | 0.82 ms (0.70 to 1.01) / 0.34 ms (0.32 to 0.36) | 0.97 ms (0.69 to 1.06) / 0.34 ms (0.33 to 0.36) |
| answers `UNAVAILABLE` | 1.05 ms (0.79 to 1.12) / 0.39 ms (0.36 to 0.40) | 0.73 ms (0.62 to 1.08) / 0.33 ms (0.32 to 0.34) | 0.70 ms (0.63 to 0.99) / 0.34 ms (0.33 to 0.40) |
| never answers | 0.90 ms (0.85 to 1.08) / 0.38 ms (0.37 to 0.41) | 0.80 ms (0.71 to 1.04) / 0.33 ms (0.33 to 0.36) | 0.73 ms (0.70 to 1.03) / 0.33 ms (0.32 to 0.34) |

With the collector up it received 5,202 spans of 5,202 from each proxy in each run. The slow collector and the one that
answers `UNAVAILABLE` each received all 15,606 spans of the three proxies in each run, and the one that never answers
13,640 to 15,513 of them, so a proxy goes on sending to a collector that holds its calls. At 500 a second nginx answered
sooner than the Envoy output, with the ranges apart, with the collector that never answers on both versions and, on
1.30.6, with nothing at the address and with `UNAVAILABLE`; elsewhere the ranges overlap.

Through the relay, with the collector found by name, nginx answered 1,000 requests a second for 30 s to the route with
no check at a median of 0.23 ms (0.18 to 0.32) on 1.31.8 and 0.20 ms (0.18 to 0.23) on 1.30.6, and at the 99th
percentile in 0.43 ms (0.34 to 0.65) and 0.38 ms (0.34 to 0.45); the same file asking the collector's address answered
at 0.23 ms (0.21 to 0.23) and 0.24 ms (0.20 to 0.31), and in 0.42 ms (0.40 to 0.44) and 0.46 ms (0.37 to 0.61), three
runs of each. The ranges overlap, so these runs show no time the relay adds to a request. Every request was answered
`200` and every span reached the collector.

Through the relay, with the collector found by name, the five states got 1,000 requests a second for 30 s, one run on
each version: every request was answered `200`, at a median of 0.18 to 0.32 ms, and the collector up received every
span. The relay ends a call after 10 s, so the module went on sending to the collector that never answers until the
load ended, 17,586 of 30,001 spans in 43 calls on 1.31.8 and 17,350 on 1.30.6. Asked by its address, that collector got
32 calls from each version, two to four on each of the 12 workers' connections, none of which came back, and no call
after the first 15 s: 13,400 and 13,024 spans.

With the collector answering each call after 5 s, 2,000 requests a second for 10 s were each answered `200`, at a median
of 0.19 ms (0.18 to 0.23) through the Envoy output, 0.20 ms (0.19 to 0.24) through nginx 1.30.6 and 0.20 ms (0.19 to
0.22) through 1.31.8, and each proxy dropped the spans it could not send in time. Of the 20,000 or 20,001 spans of a
run, counted at the collector within 40 s of the last request, the Envoy output lost 9,770 (9,765 to 9,775), sending the
rest in 2,052 calls of at most five spans, and nginx 1.30.6 lost 3,417 (2,301 to 5,232) and 1.31.8 4,494 (3,561 to
5,048), sending the rest in 34 to 41 calls of at most 512: nginx lost fewer than the Envoy output in every run.

A collector found by name, through the relay, with `dns.resolver` naming a name server that gives the name with a time
to live of 5 s: each proxy got 50 requests a second over 4 connections, with a collector of its own name, three runs of
each of two events. When the first collector left the network 20 s in and the name stopped resolving, and 30 s later a
second collector started at the name, the spans reached the second 6.78 s (6.78 to 6.79) after it started through nginx
1.31.8, 8.47 s (8.44 to 8.48) through 1.30.6 and 0.19 s (0.17 to 0.19) through the Envoy output; nginx lost 1,751 to
1,753 of a run's 17,501 spans and the Envoy output 1,520, of which 1,500 are those of the 30 s with no collector. When
the name moved to a second collector with the first still up and answering, the spans reached the second 7.02 s (7.00
to 7.02) after the move through nginx 1.31.8, 8.69 s (8.68 to 8.71) through 1.30.6 and 0.43 s (0.43 to 0.44) through
the Envoy output, and no proxy lost a span: the first collector received 250 or 251 more from each nginx and 20 from the
Envoy output, and the second the rest of the 16,001. Every request of every run was answered `200`, at a median of 0.75
to 0.93 ms through nginx and 0.92 to 1.13 ms through the Envoy output. The relay asked the name server every 6 s and the
Envoy output every 5 s, and the module sends the spans it holds every 5 s, so nginx's spans reach a new address some
seconds after the Envoy output's.

## What nginx writes with no key

Each of these is in every file the nginx generator writes, so no row of that table holds it, and each differs from the
Envoy output but the answers to a service that takes no connection or sends nothing, which are here to say what nginx
answers and when. Each was measured on nginx 1.30.6 and 1.31.8, and Envoy's answers on Envoy 1.40.2, unless it says
otherwise.

- **nginx's own answers carry `Server: nginx`**, with no version, where the Envoy output's own answers carry no `Server`
  header ([Defaults](defaults.md)). These are the `400`, `403`, `404` and `502` the file answers with an empty body, to
  a path it refuses, a `QUERY` with no `Content-Type`, a `#` in the target, a path PCRE2 gave up on, an upgrade other
  than WebSocket, a request no route takes and a service that refuses the connection, the `400` with the body `Bad
  Request` to a request Envoy's parser refuses (a method it does not know, a byte above 0x7F in an HTTP/1.1 path), the
  `429` to a request over a rate limit, the `426` to an HTTP/1.0 request and to one of `HTTP/1.2` to `HTTP/1.9`, whose
  status line has no reason phrase, the `403` with the body `Invalid CORS request` to a foreign browser origin, the
  `200` to a preflight, and the `400` nginx answers to a request it cannot read, such as a path with an invalid escape
  or a header line longer than its header buffers read, with its own HTML page naming nginx. The `403` to an upgrade
  other than WebSocket carries the body `Invalid CORS request` when the request has an `Origin`, with or without `cors`,
  as the Envoy output's does. nginx closes the connection after its `426`, on a route, on a host no environment names
  and on the admin listener, and after that `403`, from a foreign origin too, and keeps it after the `403` to any other
  request from a foreign origin, as the Envoy output does (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). A
  service's own `Server` header is passed on, as the Envoy output passes it.
- **Some of nginx's answers carry no `x-request-id` where the Envoy output's carry one**: the `400` nginx's own parser
  answers before any rule of the file runs, as to a path with an invalid escape or to two `Host` lines. Its `400`s to a
  request either set of its request checks refuses, such as a method Envoy's parser does not know or a `QUERY` with no
  `Content-Type`, and its `426` carry none, as the Envoy output's carry none, and its `404` to a host no environment
  names carries one, as the Envoy output's does. A request id nginx makes is a version 4
  UUID, as Envoy's is, and a client's own `x-request-id` is kept under `client_address: forwarded` and replaced under
  `connection`, as [The client's address and removed headers](#the-clients-address-and-removed-headers) says. A client
  that sends `x-request-id` twice is answered `400`, where Envoy joins the two with a comma and serves the request.
- **`Vary: Origin` is sent beside a service's own `Vary`**, as a second header line, not merged into the service's.
- **The error log holds only the lines that stop nginx (`emerg`)**: a file nginx cannot load and a port it cannot bind
  are still printed. At `error` nginx writes the request line of a request whose service refused the connection, and at
  `alert` that of a path PCRE2 gave up on, each with its query, which can hold a token. The same level also hides a
  worker process that crashed (`[alert] worker process 7 exited on signal 11`) and the connections running out
  (`[warn] 4 worker_connections are not enough, reusing connections`), measured on nginx 1.31.8 by holding more
  connections open than `worker_connections` allows and stopping a worker with signal 11: at `emerg` the log held no
  line, and at `info` both. The request log is another log, which holds no path or query: [The request
  log](#the-request-log).
- **A request past the header limits is answered `400`, or `414` for some request lines, where the Envoy output answers
  `431`**, and over HTTP/2 a block past what nginx reads ends the connection, where Envoy resets the stream. More than
  `count` header lines are answered `400`, where Envoy answers `431`. [The header limits](#the-header-limits) gives the
  lengths measured and where each answer comes.
- **A service that does not take the connection within 5 s is answered `502` with no body**, after those 5 s, as the
  Envoy output answers it (measured at 5.0 s on both), and its upstream-failure line carries `UF`; a gRPC call to it
  gets the gRPC answer of the next item after those 5 s. Under a `timeouts.idle` below 5 s the idle time ends the wait
  first, and the answer is `504` with the text `stream timeout`, as the Envoy output answers it. With a `timeouts.idle`
  of 5 s or more the connect time stays 5 s and the answer is the `502`. A call to an RPC route with `retries` whose
  every try waits out those 5 s gets the gRPC answer after its last try, 10 s with one retry, as the Envoy output
  answers it (measured on both).
- **A gRPC call whose service fails gets no `grpc-message` where the Envoy output sends one.** A gRPC call is a
  request whose `content-type` is `application/grpc` or starts `application/grpc+`. When its service refuses the
  connection, takes none within 5 s, resets the connection or the call's stream, closes the connection or answers in
  HTTP/1.1, nginx answers `200` with `grpc-status: 14` and no body, as the Envoy output does, on a gRPC route and on a
  plain one, over HTTP/2 and HTTP/1.1. For a stream reset and for an HTTP/1.1 answer the Envoy output adds a
  `grpc-message`, `upstream connect error or disconnect/reset before headers. reset reason: remote reset` or the same
  ending `protocol error`; no nginx variable tells those two failures from the others, so nginx sends none, the
  difference the `services.<service name>.grpc` note names. A request that is not a gRPC call, to the route of a service
  that resets the stream or answers in HTTP/1.1, gets nginx's `502` with no body, where the Envoy output answers the
  stream reset `503` and the HTTP/1.1 answer `502`, each with that text as `text/plain`, a difference the same note
  names. Measured with grpc-go and with raw HTTP/2.
- **A service that sends nothing is answered `504` when the Envoy output answers it, with its text**: 15 s after the
  request without `timeouts.request`, with `upstream request timeout`, and after 5 minutes with `timeouts.request:
  none`, with `stream timeout` (measured 300.1 s against Envoy's 300.0 s). [The timeouts](#the-timeouts) says what
  `timeouts.idle` changes. nginx counts its limits between two reads or writes, so an answer that keeps coming runs past
  the 15 s at which Envoy ends it, the difference the `timeouts.request` row names.
- **A client connection is closed after 1000 requests**, the 1000th answer carrying `Connection: close`, where Envoy
  served 1200 on one connection and kept it open. nginx also closes a connection that has served requests for an hour
  (`keepalive_time`; read in nginx's documentation, not run).
- **A header block still arriving after 5 minutes gets no answer**: nginx closes the connection at 300.0 s, where Envoy
  answers `408` at 300.0 s and closes it at 301.0 s, measured over HTTP/1.1 with a header line that gained a byte a
  second. A connection that sends nothing to a listener without `tls` is closed at 300.1 s, where Envoy keeps it open
  until 3600.0 s, an hour.
- **A request line with no version, HTTP/0.9's own form, gets the text `Bad Request` alone, with no status line**,
  where Envoy answers `426`.
- **`HTTP/0.0` to `HTTP/0.9` with headers are answered `400`, and `HTTP/2.0` to `HTTP/9.9` on an HTTP/1 connection
  `505`**, where Envoy answers `426` to each, as to every HTTP/1 version but 1.1.
- **An absolute request line with user information, `GET http://user@localhost/z`, or with no `Host` header is answered
  `400` and the connection closed**, where Envoy serves it with the host the line names. One of another scheme than
  `http` or `https`, in any letter case, such as `ftp://`, `ws://`, `h2c://`, `httpx://` or `x-y+z.1://`, is answered
  `400`, as by Envoy, and reaches no service, over 33 forms measured. Any other absolute request line routes by the host
  it names, as Envoy's does, however many spaces come before it, but an `https` one sent over plain HTTP under
  `client_address: connection`, which the Envoy output answers `403`
  ([the list](#where-nginx-is-less-strict-than-the-envoy-output)); one whose host a query or a fragment follows directly,
  `GET http://localhost?x=1`, is answered `400` and the connection closed, where Envoy answers `404` and keeps it. A tab
  between the method and the target is answered `400` and the connection closed, where Envoy serves the request.
- **A `Host` header sent twice is answered `400`**, where Envoy answers `404`; over HTTP/2, a `host` header that differs
  from `:authority` is answered `400` with nginx's own page, where Envoy routes by `:authority`: it serves one whose
  `:authority` is `localhost` and `host` `other.example`, and answers `404` to one whose `:authority` is `other.example`
  and `host` `localhost`, as no environment names that host. An empty `:authority` or `host` is answered `400` with that
  page too, where Envoy closes the connection after 1 s with no answer.
- **Repeated lines of a list header reach the service as separate lines for the names Envoy joins**: sent twice, as
  `accept: a=1` and `accept: b=2`, `accept`, `via` and `accept-encoding`, each a list in RFC 9110, reach the service as
  two lines through nginx and as one line, `accept: a=1,b=2`, through Envoy, over HTTP/1.1 and HTTP/2: the same HTTP
  message, written as separate lines. A second `Content-Type`, `User-Agent` or `Referer` line is answered `400` (above).
  A second line of `authorization`, `if-modified-since`, `if-unmodified-since`, `if-match`, `if-none-match` or
  `if-range` is answered `400` by nginx with its own page, where Envoy joins the lines with a comma and serves the
  request. `cookie`, `range`, `x-real-ip`, `accept-language`, `date` and a header of a name of your own reach the
  service alike through both.
- **Over HTTP/1.1 a tab before or after a header value reaches the service with the value**, where Envoy removes it. A
  header `X-A` with the value `v` and a tab on each side reached the service as `x-a: v` through Envoy and with its two
  tabs through nginx 1.30.6 and 1.31.8, which removes the spaces around a value; a `User-Agent`, and a client's
  `x-request-id` under `client_address: forwarded`, sent with a tab before the value or after it, reached the service
  with that tab too. RFC 9110 does not count the white space around a field value as part of the value, so a service
  that reads the value as it says reads the same value through both, but one that reads the bytes reads the tab: [the
  list of ways nginx is less strict](#where-nginx-is-less-strict-than-the-envoy-output) names it.
- **`TRACE` and `CONNECT` are answered `405`, and `OPTIONS *` `400` with the connection closed**, where Envoy passes
  `TRACE` to the service and answers the other two `404`.
- **A `Connection` header naming a header other than `close`, `keep-alive`, `upgrade`, `http2-settings` and `te` is
  answered `400`**, where Envoy removes the header it names and serves the request: nginx cannot remove a header a
  request names. Envoy also answers `400` when the token names `host` or an `x-forwarded-` header, and so does nginx.
- **Over HTTP/2, a `connection`, `keep-alive`, `proxy-connection`, `transfer-encoding` or `upgrade` header, or a `te`
  other than `trailers`, is answered `400`**, where Envoy closes the connection.
- **Over HTTP/2, a request header whose name holds a character outside RFC 9110's token characters is answered
  `400`**, such as `x@at`, `x(p)` or a byte above 0x7F, where Envoy closes the connection. Over HTTP/1.1 both answer
  `400`. `proxyfile.js` reads each name as the client sent it; names of token characters, `x_und` and `x.dot` among
  them, reach the service as through Envoy.
- **Over HTTP/2, a request header whose value holds a control byte other than a tab is answered `400`**, where Envoy
  closes the connection: the bytes 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F and 0x7F. Over HTTP/1.1 both answer `400` to
  each of those bytes. nginx alone passes such a value to the service, so `proxyfile.js` refuses it. nginx refuses
  0x00 itself, as Envoy does, and a carriage return, which Envoy serves over HTTP/1.1 with the carriage return dropped
  (the service receives `x-a: bc` for `x-a: b`, CR, `c`); over HTTP/1.1 a line feed ends the header line. A tab inside
  the value, a byte above 0x7F and UTF-8 reach the service as through Envoy.
- **A request trailer whose name holds a character outside RFC 9110's token characters, such as `x@at`, or whose value
  holds a control byte, is let through over HTTP/1.1**: nginx serves the request, and the service receives its body and
  no trailer, where Envoy answers `400`. The bytes are 0x00 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F and 0x7F; nginx answers a
  line feed or a carriage return in a trailer value `400` itself. This difference is not stricter: nginx serves a
  request Envoy refuses, because `proxyfile.js` runs before the body arrives and never sees a trailer. The service
  receives no trailer through either proxy, whatever its name (`x-t` too).
- **Over HTTP/2, a request with any trailer ends the connection** (`GOAWAY` with `PROTOCOL_ERROR`), where Envoy serves
  it and the service receives its body and no trailer.
- **A header line folded onto the next, `x-a: 1` and then ` x@at: 2` with a space or a tab first, is answered
  `400`**, where Envoy serves the request and the service receives `x-a` as `1 x@at: 2`, the tab kept when the fold
  starts with one.
- **A `Host` whose port is past 65535 or holds a sign, `localhost:99999` or `localhost:+1`, is answered `400`**, where
  Envoy serves the request with that `Host`.
- **A chunked request body sent with its headers reaches the service with `Content-Length`**, where Envoy sends it on
  chunked; a body that comes later is sent on chunked, as by Envoy.
- **A worker process serves 1024 connections at most**, and 8192 in a file with the auth check ([Checks in flight on
  few workers](#checks-in-flight-on-few-workers)), a client's and a service's counted together (read in nginx's
  documentation of `worker_connections`, not run), and `limits.connections` does not change it ([The connection
  limit](#the-connection-limit)).
- **nginx 1.30 sends a gRPC service a `host` header beside `:authority`**, and nginx 1.31 sends `:authority` alone, as
  Envoy does.
- **A render for nginx reads no key of the [`envoy`](envoy.md) block and refuses none of them**, so tracing written at
  `envoy.tracing` is Envoy's alone; a render for Envoy reads it as before.

## Where nginx is less strict than the Envoy output

These differences are not stricter than the Envoy output: in each, nginx serves a request the Envoy output refuses or
ends, sends a service or a client what the Envoy output would not, or leaves out a log line the Envoy output writes.
`proxyfile render --target nginx` counts them on standard error after it writes the files, and nginx's column in
[Portability](portability.md#support-by-proxy-from-the-generators) names each on its row. Measured on nginx 1.30.6 and
1.31.8 beside Envoy 1.40.2:

<!-- keys: less strict: nginx -->
- **a request trailer whose name is outside the token characters or whose value holds a control byte is served over HTTP/1.1**: A chunked request whose trailer is `x@at: 2`, or whose trailer value holds one of the bytes 0x00 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F, is served, and the service receives its body and no trailer, where Envoy answers `400`: `proxyfile.js` runs before the body arrives and never reads a trailer. nginx answers a line feed or a carriage return in a trailer value `400` itself.
- **a request trailer larger than the header limit is served over HTTP/1.1**: A chunked request whose trailer is larger than the header limit, a trailer line of 4,093 bytes or 25 trailer lines of 200 bytes under `size: 4KiB`, or a trailer line of 61,437 bytes without `limits`, is served, and the service receives its body and no trailer, where Envoy answers `431`: nginx reads a trailer to its end with no limit on its size, and `proxyfile.js` runs before the body arrives and never reads a trailer.
- **a header line with no colon reaches the service as a header with an empty value**: A request holding the line `nocolon`, with no colon, is served over HTTP/1.1, and the service receives `nocolon: `, where Envoy answers `400`. nginx stores such a line as a header with an empty value, as it stores `nocolon:`, which Envoy serves too, so neither `nginx.conf` nor `proxyfile.js` can tell the two apart.
- **a request with no body reaches the service without content-length: 0**: A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`.
- **a Referer the Envoy output removes reaches the service**: Of 104 forms of one Referer line, Envoy removed 46 before the service, such as `a:b`, `/a#f`, `a b`, `/é`, `{`, `http://u@a/`, `http://a/#f`, `http://a:65536/`, `http://a_b/`, `http://[1:2:3]/` and `about:blank`, and nginx passed every one on. The Envoy output removes a Referer that its URL parser reads as a URL with a fragment or user information, or that the parser does not read and that holds a byte outside RFC 3986's query and fragment characters or a colon before its first slash. A rule written from the published grammar of that parser, nodejs's http_parser, answered forms of the 104 otherwise than Envoy: in its strict build it removed `h2://a/` and `x-y://a/`, whose scheme Envoy reads with a digit or a `-` in it, and in its lenient build it also kept `http://a_b/` and `http://a/é`. nginx answers two Referer lines `400`, as the request line's row says.
- **over HTTP/1.1, a tab before or after a header value reaches the service**: A header value sent over HTTP/1.1 with a tab right after its colon or at its end reaches the service with the tab, where Envoy removes it: the service receives `User-Agent` as a tab and `probe/3`, or as `probe/4` and a tab, and `X-A` as a tab and `b5`, where through Envoy it receives `user-agent: probe/3`, `user-agent: probe/4` and `x-a: b5`, under `client_address: forwarded` and under `connection`. Under `forwarded` a client's `X-Request-Id` sent as a tab and `abc-1`, or as `abc-2` and a tab, reaches the service with the tab too, and nginx's answer gives it back to the client with the tab, where Envoy sends `abc-1` and `abc-2` to both; under `connection` each proxy sends an id of its own in its place. A service that reads the value as RFC 9110 says, without the white space around it, reads the same value through both.
- **a WebSocket upgrade a service answers without switching passes the service's body**: A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body.
- **a service's answer with a malformed header line or a malformed status line reaches the client**: A service's answer holding a header line with no colon reaches the client with it as a header with an empty value (`nocolon: `), and one whose header name holds `@` (`x@bad: 1`) or whose header value holds the byte 0x01 reaches the client with that header as the service wrote it, where Envoy answers `502` and writes an upstream-failure line with the flag `UPE`, which nginx does not write. A malformed status line reaches the client too. A status of three digits that starts with 0, `050`, `000` or `099`, reaches it as that status over HTTP/1.1 and HTTP/2, with the headers nginx writes. A status with spaces between its digits, `HTTP/1.1 2 0 0 X`, reaches an HTTP/2 client as `200` and an HTTP/1.1 client with the status line `HTTP/1.1 0 0 X`, and one with a fraction, `HTTP/1.1 403.1 X`, reaches an HTTP/2 client as `403` and an HTTP/1.1 client as the service wrote it: nginx reads three digits with spaces among them, and takes a `.` after them. A status of one or two digits, such as `5`, `20` or `99`, one of four digits, `HTTP/1.1 2000 OK`, and an answer with no status line at all, such as `hello` and a line end, reach an HTTP/1.1 client byte for byte as the service wrote them, with none of the headers nginx writes, such as `x-request-id` and `Vary`, and an HTTP/2 client as the body of an answer with the status `000`: nginx reads such an answer as one with no head and passes it whole. Envoy answers each of these `502` and writes an upstream-failure line with the flag `UPE`, but the status of four digits, which it passes as `2000` with its own headers; nginx writes no upstream-failure line for any of them, and has no setting that refuses them. nginx stores a line with no colon as it stores `nocolon:`, which Envoy passes too, so `proxyfile.js` cannot tell the two apart.
- **over HTTP/2, empty frames in a row are served**: An HTTP/2 request whose HEADERS frame is followed by two empty CONTINUATION frames, or whose body comes after two empty DATA frames, is served, where Envoy takes one frame with no payload in a row and closes the connection at the second: nginx sets no such limit, and neither `nginx.conf` nor `proxyfile.js` sees a frame.
- **the admin listener serves an absolute target of http or https as the path it names**: `GET http://localhost/ready HTTP/1.1` sent to the admin listener is answered `200` with `LIVE`, and so is the same target with `https://` or `HTTP://`, and `http://localhost/stats` and `http://localhost/server_info` are answered `200` with nginx's counters and its version, where the Envoy output's admin listener answers `404` with no body to every absolute target. A path the admin listener does not answer, `http://localhost/nothing`, is answered `404` by both, and a target of another scheme, `ftp://localhost/ready` or `ws://localhost/ready`, `400` with the body `Bad Request` and the connection closed, where the Envoy output answers `404`.
- **under connection, an absolute https target sent over plain HTTP is served**: With `client_address: connection` and no `tls`, an absolute `https` target sent over plain HTTP, such as `GET https://localhost/products/1 HTTP/1.1`, or the same with `HTTPS://`, is served: the service receives `X-Forwarded-Proto: http`, and with `auth.send_request: true` the check is told `x-forwarded-proto: http`, the connection's scheme, where the Envoy output answers `403` with the body `Forbidden`, writes `http1.https_url_on_plaintext_connection` in its request log, closes the connection and asks no check. Under `forwarded` both serve it, and the service receives `x-forwarded-proto: http` through both.
- **without timeouts.request, an answer that keeps arriving runs past 15 s**: Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes.
- **without timeouts.request, a request body its service does not read, or reads slowly, runs past 15 s**: On a route with no retries, and for a request a retried route sends on as it arrives, nginx sends the body with its send time, which counts between two writes and is 5 minutes without `timeouts.idle`. A `PUT` of 1 MiB to a service that takes the connection and does not read it, or reads one byte every 20 s, is answered `504` after 300.1 s (300.0 to 300.2 in three runs), where the Envoy output answers `504` after 15.0 s; a `PUT` of 2 MiB or of 64 MiB, or of 2 MiB in chunks, gets nginx's `504` after 300.1 s too, where the Envoy output answers `408` after 300.0 s (on a retried route, in one of three runs, `504` after 15.0 s). Tried: a send time of the read time, 15 s, in each location that streams a body. It answered every one of those `PUT`s `504` after 15.1 s (15.0 to 15.1), those the Envoy output answers `408` after 300 s among them, to the service that reads one byte every 20 s too, so it is not written; a client that stopped its body for 20 s was served by all three with it and without it. Not written either: reading a body of at most 1 MiB whole before sending it, on every route as a retried route does. That answers such a `PUT` as the Envoy output does, `504` after 15.0 s, as the retried route's location answered it, but a route with no retries would then hold each such body in memory, and its service would get nothing of an upload until all of it had arrived. A service that reads a body slowly but takes a write within the send time each time is sent all of it, on every route, and the send time does not end its try; a retried route's location sends the body it read whole with the route's read time as its send time. A `PUT` of 1 MiB to a service that reads 65,536 bytes every 1.5 s, in three runs of each, is answered `504` after 27.0 s by nginx 1.30.6 and 27.1 s by 1.31.8 on a retried route (27.0 to 27.1) and after 18.1 s by both on a route with no retries (18.1 in every run), where the Envoy output answers `504` after 15.0 s on both (15.0 in every run). The retried route's send time of 15 s did not end it, and no setting ends such a try at the route's time, as none ends an answer that keeps arriving: nginx has no time for a whole try.
- **with allow, a check service that does not close its connection right after its answer leaves requests unchecked past about 470 checks a second**: When the check service does not close its connection right after its answer (measured: 20 ms or more after it, or not at all), nginx closes it first, so each check holds one of nginx's local ports in `TIME_WAIT` for 60 s: with the 28,232 local ports of 32768 to 60999 that is about 470 checks a second to one check address, past which nginx cannot open a connection to the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output keeps its connections to the check open and goes on asking. `proxy_half_close on` does not change which side closes first.
- **with allow, a worker with none of its 8192 connections left serves requests unchecked**: Each checked request in flight holds four of a worker's connections, and every other connection of the worker, a client's or a service's, counts against the same 8192 (`worker_connections`). A worker with none left cannot ask the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output's check has its own 1,024 connections whatever else the proxy carries. With 1024 connections a worker, 231 and 234 of 800 requests sent at once to 2 workers were served unasked (nginx 1.30.6 and 1.31.8), where the Envoy output served none; with 8192, none were, up to 4,000 at once with nothing else open.
- **a request whose client closes its connection during the check can still reach the service**: When a client closes its connection while its check is asked, with a close, a reset or a half-close, and the check then allows the request, nginx can still send the request to the service, where the Envoy output ends the request when its client goes and sends the service nothing. Of 30 clients, one after another, that each sent a request and closed 50 ms into a check that allowed it after 300 ms, the service received 16 to 21 `GET` requests and 17 to 29 `POST` requests with their bodies, 12 and 17 after a reset and 17 and 23 after a half-close, once nginx had served other requests to it and so held idle connections to it; from a just-started nginx it received none, and through the Envoy output none in any round. `proxy_ignore_client_abort` is off, nginx's default.
- **under forwarded, an x-forwarded-proto that is neither http nor https is told to the check as it arrived**: With `client_address: forwarded` and `auth.send_request: true`, a request sent over plain HTTP with `X-Forwarded-Proto: ftp`, such as `GET /products/1 HTTP/1.1`, is told `x-forwarded-proto: ftp` in the request to the check, where the Envoy output tells the check `http`, the connection's scheme. Both send the service `X-Forwarded-Proto: ftp`. nginx tells the check the header's value as it arrived, whatever it holds, so a check that builds a URL from the scheme it is told reads the word of the client, or of the proxy in front. Under `connection` both tell the check and the service the connection's scheme.
- **a retried request or call is sent again at once and with no limit on retries at the same time**: A call whose first connection was refused is sent to the next backend at once, where the Envoy output waits 25 ms, and every such call is retried, where the Envoy output retries at most 3 at the same time and fails the others. A request of a retried route is retried at once and with no limit too: at 1,000 requests a second for 60 s, each first try refused, nginx served every one of the 60,000 or 60,001 requests on a second try in each of ten runs, where the Envoy output served 13,452 to 13,765 and answered the others 502, and nginx's 99th percentile was 0.49 ms on 1.30.6 and 0.45 ms on 1.31.8, where the Envoy output's was 24.4 ms (the medians of four and five runs on mains power; measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
- **a chunked answer its service cuts short writes no upstream-failure line**: A service that closes its connection after the first chunk of a chunked `200` answer reaches the client as it does through the Envoy output, which then writes the request line with the flag `UPE`, whatever `log` says, and a `gateway_upstream_failure` line; nginx writes no upstream-failure line, and the request line only when `log` takes the request by its status or its share: nginx records the answer as complete, and with no content-length there is no length to compare the bytes it received to. An answer cut short before its content-length is logged as the Envoy output logs it.
<!-- /keys -->
