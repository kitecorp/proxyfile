# nginx

`proxyfile render --target nginx` writes `nginx.conf`, a whole nginx configuration, from the same proxyfile as Envoy's,
and `proxyfile.js` beside it. The two go together: `nginx.conf` loads nginx's njs module and imports `proxyfile.js` from
its own folder, and nginx refuses to load the configuration when the script or the module's file is missing (measured
on nginx 1.30.6 and 1.31.8). The official nginx images carry njs; a distribution's nginx may need its njs package
installed. Line 2 of `nginx.conf` names the version it needs, njs 1.0.2 or later, the one measured. `proxyfile check`
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

This version writes routing, CORS with the origin lists, the client's address, header removal, TLS, the header limits,
the two idle times, the admin port's paths and the request log: the listener, the admin port with `/ready`, `/stats`
and `/server_info`, `tls` with the clients CA, `client_address`, `limits.request_headers`, `timeouts.idle` and
`timeouts.upstream_idle`, `log`, the services with their HTTP and gRPC ports, their
addresses (IP addresses with no zone) and `host_header`, `remove_from_clients` by name, `cors`, each environment's hosts
(none that starts or ends with a dot) with its origins and patterns and the refusal of a browser origin on none of
them, the routes by prefix and path (any character but `%`, one Envoy escapes before it picks a route, `"` `<` `>` `^`
`` ` `` `{` `|` `}`, and one outside ASCII), pattern and method with their rewrites, and the rpc entry with its
methods. It refuses every other core key a file writes, naming the key where it is written, so a file that needs one is
never rendered as if the key were not there: `limits.connections` is one, and `timeouts.request` with a time is
another ([What nginx writes with no key](#what-nginx-writes-with-no-key) and [The timeouts](#the-timeouts) say why).
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
never matches; a kept escape on a route with a `rewrite`, because nginx rewrites the decoded path; and an empty query,
a `?` with nothing after it, wherever nginx would rewrite the path, because nginx drops the `?` Envoy sends on.

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
  `400` with no body, as Envoy answers it. `TRACE`, which nginx refuses before it reads the path, is answered `405` with
  no body, where the Envoy output answers it as `GET`.
- The admin port writes no log line for a request, as the Envoy output writes none.

None of the three paths is answered on the public listener: a route takes them as it takes any other path, on both
proxies. The contents of `/stats` and `/server_info`, the status of a refused path and the answer to `TRACE` are
declared on nginx's `admin` row.

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
`upstream_ms`. `status`, `bytes_in`, `bytes_out` and `duration_ms` are bare numbers, `duration_ms` and `upstream_ms` are
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
from the request's start; there are no `flags` and `details` fields; a value the Envoy output writes `null` is written
empty, but `upstream_ms`; a request the origin or upgrade check refuses logs `route` and `upstream` empty, where the
Envoy output names them; a request Envoy refuses as it reads it logs its host, method, request id and protocol, which
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
- **A worker process serves 1024 connections at most**, a client's and a service's counted together (read in nginx's
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
- **a chunked answer its service cuts short writes no upstream-failure line**: A service that closes its connection after the first chunk of a chunked `200` answer reaches the client as it does through the Envoy output, which then writes the request line with the flag `UPE`, whatever `log` says, and a `gateway_upstream_failure` line; nginx writes no upstream-failure line, and the request line only when `log` takes the request by its status or its share: nginx records the answer as complete, and with no content-length there is no length to compare the bytes it received to. An answer cut short before its content-length is logged as the Envoy output logs it.
<!-- /keys -->
