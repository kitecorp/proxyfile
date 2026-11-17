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
With `auth.ask` it also asks the check service and reads its answer ([The auth check](#the-auth-check)).

This version writes routing, CORS with the origin lists, the client's address, header removal, TLS, the header limits,
the two idle times, the admin port's paths, the request log and the auth check: the listener, the admin port with
`/ready`, `/stats` and `/server_info`, `tls` with the clients CA, `client_address`, `limits.request_headers`,
`timeouts.idle` and `timeouts.upstream_idle`, `log`, `auth` with `ask`, `timeout`, `enabled`, `when_unavailable`,
`only_with_header`, `send`, `accept` and `send_request`, a route's `auth: off` and an rpc method's `action` and
`reports`, the services with their HTTP and gRPC ports, their
addresses (IP addresses with no zone) and `host_header`, `remove_from_clients` by name, `cors`, each environment's hosts
(none that starts or ends with a dot) with its origins and patterns and the refusal of a browser origin on none of
them, the routes by prefix and path (any character but `%`, one Envoy escapes before it picks a route, `"` `<` `>` `^`
`` ` `` `{` `|` `}`, and one outside ASCII), pattern and method with their rewrites, and the rpc entry with its
methods. It refuses every other core key a file writes, naming the key where it is written, so a file that needs one is
never rendered as if the key were not there: `limits.connections` is one, `timeouts.request` with a time is another
([What nginx writes with no key](#what-nginx-writes-with-no-key) and [The timeouts](#the-timeouts) say why), and
`auth.jwt` is a third, because nginx open source has no token verification.
It also refuses, by its key, a value whose nginx form is longer than nginx
reads: 4094 bytes between quotes, or 4095 without them (measured on nginx 1.30.6 and 1.31.8). nginx's column in
[Portability](portability.md#support-by-proxy-from-the-generators) says how it writes each key and, for a key it
refuses, what to write instead.

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
`X-Forwarded-Proto` and `x-request-id` are kept when it sends them. These answers are equal to the Envoy output's but
for three refusals, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 and named on nginx's request-line row of
[Portability](portability.md#support-by-proxy-from-the-generators), which `proxyfile.js` makes before a route is
picked:

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

`timeouts.request` with a time is refused for nginx. Its time limits count the time between two reads or two writes, so
no directive holds a time for a whole request; write `timeouts.request: none`, or render the file for Envoy. Without the
key nginx answers `504` to a service silent for 15 s after the request, as the Envoy output does, and with `none` it
ends a request after 5 minutes with nothing read or written, as Envoy does. An answer that keeps arriving runs past the
15 s at which Envoy ends a request, a difference in [the list below](#where-nginx-is-less-strict-than-the-envoy-output).
What nginx answers when the connect time passes is in [What nginx writes with no
key](#what-nginx-writes-with-no-key).

## The admin port

With `admin`, nginx listens on `127.0.0.1` at that port and answers three paths there. Measured on nginx 1.30.6 and
1.31.8 beside Envoy 1.40.2:

- `/ready` is answered `200` with `LIVE`, as the Envoy output answers it.
- `/stats` is answered `200` with nginx's `stub_status` page, seven counters in all: the line `Active connections`, the
  line `server accepts handled requests` with a line of three numbers under it, and the line with `Reading`,
  `Writing` and `Waiting`. The Envoy output answers with its own statistics, one `name: value` line each.
- `/server_info` is answered `200` as `application/json` with the nginx version, `{"version":"1.30.6"}` or
  `{"version":"1.31.8"}`. The Envoy output answers with its own version, its state and more.
- Any other path, and any request with a query, `/stats?format=json` or `/ready?x=1`, is answered `404` with no body,
  where the Envoy output answers `403` with a text that names the path.
- Each path answers `GET`, `HEAD`, `POST`, `PUT`, `DELETE`, `PATCH` and `OPTIONS` as it answers `GET`, as the Envoy
  output does: `stub_status` refuses every method but `GET` and `HEAD` with `405`, so the file sends that `405` back to
  `/stats`, which nginx then asks for with `GET`. A method Envoy's parser does not know, such as `FOO`, is answered
  `400` with no body, as Envoy answers it, and so is a `QUERY` sent with no `Content-Type`, which Envoy 1.40.2 answers
  `400` too. `TRACE`, which nginx refuses before it reads the path, is answered `405` with no body, where the Envoy
  output answers it as `GET`.
- The admin port writes no log line for a request, as the Envoy output writes none.

None of the three paths is answered on the public listener: a route takes them as it takes any other path, on both
proxies. The contents of `/stats` and `/server_info`, the status of a refused path and the answer to `TRACE` are
declared on nginx's `admin` row.

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
  types {}
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
file with no `send_request`, as here. The `default_type ""` and `types {}` lines of the route location are for the
denials the check sends with no body ([What the client receives when the check refuses](#what-the-client-receives-when-the-check-refuses)).
`backlog=2147483647` is the longest queue the relay's `listen` takes: the kernel holds the smaller of that and
`net.core.somaxconn`, which was 4,096 in the pinned images, so the relay's queue held 4,097 connections where nginx's own
default of 511 held 512, and a burst of 600 clients filled the smaller queue while the check was healthy
([How the answer is read](#how-the-answer-is-read)).

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
- with `send_request: true`, `x-forwarded-uri`, the path the route matched, normalised, with each byte of 0x80 or more
  escaped as the service receives it, and the query as the client wrote it, before a rewrite; `x-forwarded-method`;
  `x-forwarded-proto`, the scheme of an absolute target in the letter case it was written in, and otherwise the
  connection's scheme, or the client's own `x-forwarded-proto` under `client_address: forwarded`; and
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
  carries `content-type: text/plain`. Each location that carries `js_access` writes `default_type ""` and `types {}`, so
  that nginx gives an answer of its own no type from the extension of the path or from its default.
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
  `max_fails=0` so nginx never takes an address out of rotation;
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
| a preflight with a dot segment and a `;` parameter | asked, `401` | asked, `401` | made the same: `@pf_r_dotparam` |
| a dot segment with a `;` parameter, also under a route with `auth: off`, written as `%2e%2e` or `..` | asked, `401` | asked, `401` | made the same: `@pf_r_dotparam` |
| a backslash in the path | asked, `401` | `400` | declared on the row of the path |
| an empty segment before a dot-dot segment, `/` and `.` written or escaped | asked, `401` | `400` | declared on the row of the path |
| an escaped control character in the path | asked, `401` | `400` | declared on the row of the path |
| an escape Envoy keeps, on a route with a `rewrite` | asked, `401` | `400` | declared on `routes[].rewrite` |
| a character Envoy escapes, in a file with a pattern route | asked, `401` | `400` | declared on `routes[].pattern` |
| a path PCRE2 gives up on | asked, `401` | `400` | declared on `routes[].pattern` |
| one header of 62,000 bytes | `431` | `400` | declared on the header limits |
| `QUERY` with no `Content-Type` over HTTP/1.1 | `400`, not asked | `400`, not asked | the same |
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
[Portability](portability.md#support-by-proxy-from-the-generators). Three entries of [the list of ways nginx is less
strict](#where-nginx-is-less-strict-than-the-envoy-output) concern the check: past the bound of local ports the
`auth.ask` note gives, a check fails for want of a port, and with `allow` its request is served with the unchecked mark,
so a request the check would refuse can be served; a worker with none of its 8192 connections left cannot ask the check
either, with the same result under `allow`, as [Checks in flight on few workers](#checks-in-flight-on-few-workers) says;
and the request of a client that closes its connection while its check is asked can still reach the service once the
check allows it, as the last sentence on the request line says. In each of the others, a request the check refuses stays
refused, and nginx either refuses or holds a request the Envoy output serves, or answers or writes a line otherwise. The
words are the words of the declaration.

- **`auth.ask`**: a request whose check answers in one of these forms is answered 403: a status outside 100 to 599, such as 2, 99, 600 or 2000, or 101, where Envoy passes the status on to the client; HTTP/2.0 or HTTP/1.2, two spaces or a tab after the version, a + before the status, a tab between the status and the reason, a lone CR, a space or a tab after a chunk size, a chunk size of nine hex digits, a chunk extension holding a control byte or a quoted CR LF, a chunk-size line past 1,024 bytes, bytes between a chunk's data and its line end, a folded trailer line, or a head of exactly 81,920 bytes with a line in a bare LF, which nginx counts a byte longer, where Envoy reads the answer; a 200 whose body passes 1 MiB, where Envoy serves the request; a denial whose body passes 1 MiB, where Envoy passes the denial on; a denial whose head has more than 100 header lines, folded lines not counted, with a name on two of them, where Envoy counts a name it keeps inline, such as date, once and passes the denial on; over HTTP/2, a gRPC call refused for the check's denial, or with deny for want of a readable answer, is answered as Envoy answers it, with one more header, content-length: 0; a check's 301, 302, 303, 307 or 308 to an HTTP/1.1 request that carries a body reaches the client with nginx's own page in place of the check's body, and a check's 304 to such a request with content-length: 0, as nginx discards the request's body, so that no byte of it is read as the next request; a denial reaches the client with Server: nginx where the check sent no Server header, with nginx's own reason phrase (302 Moved Temporarily, where Envoy writes Found), without a header line the check sent with an empty value, with the check's Access-Control-Allow-Origin beside the listed origin's own, where Envoy sends the origin's alone, and, to a HEAD request, with Content-Length: 0, where Envoy sends transfer-encoding: chunked; an error in proxyfile.js itself as it asks the check or reads its answer is answered 403, with allow too, and writes no log line; the check is sent connection: close on a new connection for each check, where Envoy sends no Connection header and keeps its connections to the check open, so nginx's local ports bound how many checks a second it can send to one check address: near 340,000 a second when the check closes its connection first after it answers, derived from the 0.13% of connections nginx's side then kept in TIME_WAIT and not reached, and about 470 a second when it does not, at most 28,231 checks to one address in any 60 s from the 28,232 ports of 32768 to 60999 (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2); past that bound a check fails for want of a port, and its request is answered 403 with deny and served with the unchecked mark with allow; the relay listens on a socket file, /run/proxyfile-<listen>/check.sock, which nginx removes when it stops and a kill leaves, and nginx then does not start, its bind() to the file failing (98: Address already in use), until the file is removed or /run is a tmpfs.
- **`auth.timeout`**: with allow, the request goes on with the unchecked mark at the first byte the check sends after auth.timeout, or after one auth.timeout with no byte from it, so a check that sends a byte just before each auth.timeout of silence holds the request up to twice auth.timeout, where Envoy goes on at auth.timeout.
- **`auth.when_unavailable`**: with allow, a request whose check answers in a form Envoy reads as no answer (a malformed header line, framing or status line, a head or a trailer section past the 81,920 bytes Envoy reads, a head of more than 100 header lines with a name on two of them, such as set-cookie, or a body that never ends) is answered 403, where Envoy serves it with the unchecked mark, x-envoy-auth-failure-mode-allowed: true.
- **`auth.only_with_header`**: over HTTP/1.1 a request whose header is a lone tab is asked, where Envoy does not ask it: nginx keeps the tab, which Envoy removes; over HTTP/2 a cookie sent as two or three empty cookie fields is asked, where Envoy does not ask it. One form is refused at render, naming the key: `auth.only_with_header` naming a header that remove_from_clients, auth.accept or the proxy itself removes from clients is refused by `--target nginx`: the Envoy output removes that header before it reads it and never asks the check, where nginx asks it; name a header the proxy passes on.
- **`auth.send`**: a client's own x-proxyfile-method and x-proxyfile-path are not sent to the check when send covers them, where Envoy sends them; what the check is asked does not change.
- **`auth.send_request`**: in a file with no pattern route, a path holding a character Envoy escapes before it picks a route (" < > ^ ` { | }) is told in x-forwarded-uri as the client wrote that character, where Envoy tells it escaped; the service receives it escaped from both; over HTTP/2 a request whose :scheme differs from its connection's scheme is told in x-forwarded-proto the connection's, where Envoy tells it the :scheme.
- **`auth.accept`**: names written in full, none of them a header nginx writes itself on the request it passes on; a request the accepted headers take past limits.request_headers is answered 500 as by Envoy, counted with the client's own Connection line, its own copies of the headers the proxy sets, each line of a name sent on several lines and each cookie field over HTTP/2, which Envoy drops, replaces or joins into one before it counts, so such a request can be answered 500 by those lines sooner; a request whose check answers a 200 that holds no accepted header is counted too, and answered 500 past limits.request_headers by its own lines, where Envoy counts only a request the check's answer set a header on and serves it; an accepted header the check sends empty reaches a gRPC service as no header, where Envoy sends it empty. Two forms are refused at render, naming the key: `auth.accept` holding a name with a `*` prefix is refused by `--target nginx`: nginx sets a request header only by a name its configuration writes; write each name in full. And `auth.accept` naming a header nginx frames or routes a request by is refused by `--target nginx`: nginx writes this header itself on the request it passes on, to frame or route it or to keep a client's copy from the service, x-forwarded-for, x-envoy-external-address and the five x-envoy- names Envoy removes from clients only with client_address: connection, and a check service must never change how nginx frames or routes that request; each is refused with either client_address, so a list stays valid when it changes; remove the name from `auth.accept`.
- **`log`**: the request line holds `authz_us` in whole milliseconds, and a request refused for a dot segment with a `;` parameter logs `route` empty on each of its lines, where the Envoy output names the route that refuses it, `refused:dot-segment-with-parameter` ([The request log with the check](#the-request-log-with-the-check)).
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
`bytes_in` counts the whole request, not its body; `upstream_ms` counts from the opening of the service connection, not
from the request's start; `authz_us` counts the check's time in whole milliseconds, `0`, `1000`, `2000` and so on, where
the Envoy output counts microseconds; there are no `flags` and `details` fields; a value the Envoy output writes `null`
is written empty, but `upstream_ms`; a request the origin or upgrade check refuses logs `route` and `upstream` empty,
where the Envoy output names them; a request refused for a dot segment with a `;` parameter logs `route` empty on each
of its lines, where the Envoy output names the route that refuses it, `refused:dot-segment-with-parameter`; a request
Envoy refuses as it reads it logs its host, method, request id and protocol, which
Envoy writes `null` and `HTTP/1.1`; a request id or `Host` holding a byte above 0x7F is logged empty.

A second line, `{"event":"gateway_upstream_failure","flags":...,"route":...,"status":...,"upstream":...}`, is written
for a request whose service failed, as the Envoy output writes it: `UF` for a service that refused the connection, `UPE`
for an answer cut short of its `Content-Length` and for one whose head nginx refuses as malformed, and `UC` and `UT` as
Envoy writes them. A `HEAD` request gets none, and a request the idle time ended gets none, as with Envoy. A service's
answer whose head nginx refuses is answered `502`, as Envoy answers it, for each of a space before a header's colon, a
NUL in a header value, a `Content-Length` that is not a number, a head that ends before its blank line and a status
line cut in the middle (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). Two cases nginx does not write, both
in [the list below](#where-nginx-is-less-strict-than-the-envoy-output): a chunked answer its service cuts short after a
chunk, which Envoy logs with `UPE` and a failure line, is recorded by nginx as complete, and nginx writes neither; and
an answer with a header line nginx passes on where Envoy answers `502`, which Envoy logs with `UPE`.

## What nginx writes with no key

Each of these is in every file the nginx generator writes, so no row of that table holds it, and each differs from the
Envoy output but the answers to a service that takes no connection or sends nothing, which are here to say what nginx
answers and when. Each was measured on nginx 1.30.6 and 1.31.8, and Envoy's answers on Envoy 1.40.2, unless it says
otherwise.

- **nginx's own answers carry `Server: nginx`**, with no version, where the Envoy output's own answers carry no `Server`
  header ([Defaults](defaults.md)). These are the `400`, `403`, `404` and `502` the file answers with an empty body, to
  a path it refuses, a request Envoy's parser refuses (a method it does not know, a byte above 0x7F in an HTTP/1.1
  path), a path PCRE2 gave up on, an upgrade other than WebSocket, a request no route takes and a service that refuses
  the connection, the `426` to an HTTP/1.0 request and to one of `HTTP/1.2` to `HTTP/1.9`, whose status line has no
  reason phrase, the `403` with the body `Invalid CORS request` to a foreign browser origin, the `200` to a preflight,
  and the `400` nginx answers to a request it cannot read, such as a path with an invalid escape or a header line
  longer than its header buffers read, with its own HTML page naming nginx. The `403` to an upgrade other than
  WebSocket carries the body `Invalid CORS request` when the request has an `Origin`, with or without `cors`, as the
  Envoy output's does. A service's own `Server` header is passed on, as the Envoy output passes it.
- **Some of nginx's answers carry no `x-request-id`**: its `404` to a host no environment names and its `400` to a path
  with an invalid escape, where every answer of the Envoy output carries one. A request id nginx makes is a version 4
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
  of 5 s or more the connect time stays 5 s and the answer is the `502`.
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
- **A request line with no version, HTTP/0.9's own form, gets no answer**: nginx closes the connection, where Envoy
  answers `426`.
- **`HTTP/0.0` to `HTTP/0.9` with headers are answered `400`, and `HTTP/2.0` to `HTTP/9.9` on an HTTP/1 connection
  `505`**, where Envoy answers `426` to each, as to every HTTP/1 version but 1.1.
- **An absolute request line with user information, `GET http://user@localhost/z`, or with no `Host` header is
  answered `400`**, where Envoy serves it with the host the line names. Any other absolute request line routes by the
  host it names, as Envoy's does, however many spaces come before it; one whose host a query or a fragment follows
  directly, `GET http://localhost?x=1`, is answered `400`, where Envoy answers `404`. A tab between the method and the
  target is answered `400`, where Envoy serves the request.
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
- **Over HTTP/1.1 a tab before or after a header value reaches the service with the value**, where Envoy removes it, the
  same message written another way: RFC 9110 does not count the white space around a field value as part of the value.
  A header `X-A` with the value `v` and a tab on each side reached the service as `x-a: v` through Envoy and with its two
  tabs through nginx 1.30.6 and 1.31.8, which removes the spaces around a value.
- **`TRACE` and `CONNECT` are answered `405`, and `OPTIONS *` `400`**, where Envoy passes `TRACE` to the service and
  answers the other two `404`.
- **Every `QUERY` over HTTP/1.1 is answered `400`, and over HTTP/2 a `QUERY` with no `Content-Type` or an empty one
  is served**, where Envoy serves a `QUERY` with a `Content-Type` and answers `400` to one with none or an empty one,
  over both (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
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
  (the service receives `x-a: bc` for `x-a: b`, CR, `c`); over HTTP/1.1 a line feed ends the header line. A tab, a byte
  above 0x7F and UTF-8 reach the service as through Envoy.
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
  documentation of `worker_connections`, not run), and `limits.connections` is refused, because no nginx setting holds
  client connections to a number: nginx counts its own listening sockets and every connection to a service against one
  limit, and closes a client's idle connection to take a new one. With `worker_processes 1` and `worker_connections 6`,
  nginx served a seventh client by closing an idle one, and with two requests in flight served one and closed the
  other, where the Envoy output closed the seventh connection and served the six (measured on nginx 1.30.6 and 1.31.8
  beside Envoy 1.40.2).
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
- **a WebSocket upgrade a service answers without switching passes the service's body**: A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body.
- **a service's answer with a malformed header line or a malformed status line reaches the client**: A service's answer holding a header line with no colon reaches the client with it as a header with an empty value (`nocolon: `), and one whose header name holds `@` (`x@bad: 1`) or whose header value holds the byte 0x01 reaches the client with that header as the service wrote it, where Envoy answers `502` and writes an upstream-failure line with the flag `UPE`, which nginx does not write. A malformed status line reaches the client too. A status of three digits that starts with 0, `050`, `000` or `099`, reaches it as that status over HTTP/1.1 and HTTP/2, with the headers nginx writes. A status with spaces between its digits, `HTTP/1.1 2 0 0 X`, reaches an HTTP/2 client as `200` and an HTTP/1.1 client with the status line `HTTP/1.1 0 0 X`, and one with a fraction, `HTTP/1.1 403.1 X`, reaches an HTTP/2 client as `403` and an HTTP/1.1 client as the service wrote it: nginx reads three digits with spaces among them, and takes a `.` after them. A status of one or two digits, such as `5`, `20` or `99`, one of four digits, `HTTP/1.1 2000 OK`, and an answer with no status line at all, such as `hello` and a line end, reach an HTTP/1.1 client byte for byte as the service wrote them, with none of the headers nginx writes, such as `x-request-id` and `Vary`, and an HTTP/2 client as the body of an answer with the status `000`: nginx reads such an answer as one with no head and passes it whole. Envoy answers each of these `502` and writes an upstream-failure line with the flag `UPE`, but the status of four digits, which it passes as `2000` with its own headers; nginx writes no upstream-failure line for any of them, and has no setting that refuses them. nginx stores a line with no colon as it stores `nocolon:`, which Envoy passes too, so `proxyfile.js` cannot tell the two apart.
- **over HTTP/2, empty frames in a row are served**: An HTTP/2 request whose HEADERS frame is followed by two empty CONTINUATION frames, or whose body comes after two empty DATA frames, is served, where Envoy takes one frame with no payload in a row and closes the connection at the second: nginx sets no such limit, and neither `nginx.conf` nor `proxyfile.js` sees a frame.
- **without timeouts.request, an answer that keeps arriving runs past 15 s**: Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes.
- **with allow, a check service that does not close its connection right after its answer leaves requests unchecked past about 470 checks a second**: When the check service does not close its connection right after its answer (measured: 20 ms or more after it, or not at all), nginx closes it first, so each check holds one of nginx's local ports in `TIME_WAIT` for 60 s: with the 28,232 local ports of 32768 to 60999 that is about 470 checks a second to one check address, past which nginx cannot open a connection to the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output keeps its connections to the check open and goes on asking. `proxy_half_close on` does not change which side closes first.
- **with allow, a worker with none of its 8192 connections left serves requests unchecked**: Each checked request in flight holds four of a worker's connections, and every other connection of the worker, a client's or a service's, counts against the same 8192 (`worker_connections`). A worker with none left cannot ask the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output's check has its own 1,024 connections whatever else the proxy carries. With 1024 connections a worker, 231 and 234 of 800 requests sent at once to 2 workers were served unasked (nginx 1.30.6 and 1.31.8), where the Envoy output served none; with 8192, none were, up to 4,000 at once with nothing else open.
- **a request whose client closes its connection during the check can still reach the service**: When a client closes its connection while its check is asked, with a close, a reset or a half-close, and the check then allows the request, nginx can still send the request to the service, where the Envoy output ends the request when its client goes and sends the service nothing. Of 30 clients, one after another, that each sent a request and closed 50 ms into a check that allowed it after 300 ms, the service received 16 to 21 `GET` requests and 17 to 29 `POST` requests with their bodies, 12 and 17 after a reset and 17 and 23 after a half-close, once nginx had served other requests to it and so held idle connections to it; from a just-started nginx it received none, and through the Envoy output none in any round. `proxy_ignore_client_abort` is off, nginx's default.
- **a chunked answer its service cuts short writes no upstream-failure line**: A service that closes its connection after the first chunk of a chunked `200` answer reaches the client as it does through the Envoy output, which then writes the request line with the flag `UPE`, whatever `log` says, and a `gateway_upstream_failure` line; nginx writes no upstream-failure line, and the request line only when `log` takes the request by its status or its share: nginx records the answer as complete, and with no content-length there is no length to compare the bytes it received to. An answer cut short before its content-length is logged as the Envoy output logs it.
<!-- /keys -->
