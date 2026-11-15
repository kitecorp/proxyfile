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
admits](#the-browser-origins-an-environment-admits) describes.

This version writes routing, CORS with the origin lists, the client's address, header removal and TLS: the listener, the
admin port's `/ready`, `tls` with the clients CA, `client_address`, the services with their HTTP and gRPC ports, their
addresses (IP addresses with no zone) and `host_header`, `remove_from_clients` by name, `cors`, each environment's hosts
(none that starts or ends with a dot) with its origins and patterns and the refusal of a browser origin on none of
them, the routes by prefix and path (any character but `%`, one Envoy escapes before it picks a route, `"` `<` `>` `^`
`` ` `` `{` `|` `}`, and one outside ASCII), pattern and method with their rewrites, and the rpc entry with its
methods. It refuses every other core key a file writes, naming the key where it is written, so a file that needs one is
never rendered as if the key were not there. It also refuses, by its key, a value whose nginx form is longer than nginx
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
nginx refuses while it reads it, such as one with a NUL byte in a header or a header line over 8 KiB, carry none of
them, as with the Envoy output. Five differences, measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, which nginx's
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
with one worker unless it says otherwise; a time is the least and the most of three requests sent one at a time:

- **The longest path and `Origin` nginx reads.** A `GET` of a path of 8,177 bytes over HTTP/1.1, `/api/` and 8,172
  `a`, which the 24 pattern routes `/api/.*[0-9a-f]{20}\.jsN` read, is served in 5.7 to 5.8 ms, where Envoy took 1.6
  to 1.8 ms; over HTTP/2 a path of 13,106 bytes in 8.6 to 9.1 ms, where Envoy took 2.0 to 2.2 ms. A path of 8,177
  bytes of `a` and `b`, which the 40 routes `/kNN/(a|b)*a(a|b){12}c` read, is served in 1.7 to 2.2 ms, where Envoy
  took 1.0 to 1.3 ms. An `Origin` of 8,182 bytes over HTTP/1.1, and of 11,913 bytes over HTTP/2, that 64 origin
  patterns of the form `https://*.*.exampleN.org` read is refused `403` in 14.9 to 15.2 ms and 21.4 to 21.9 ms, where
  Envoy took 1.9 to 2.2 ms and 2.5 to 2.7 ms.
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
  written one each is answered `400` in 2.1 to 2.5 ms, where Envoy served both in 1.5 to 7.4 ms. Paths of 8,177
  bytes built against each pattern of the battery's files and the examples were all decided as Envoy decides them;
  with 10,000 steps nginx gave up on 8,177 `a` that `^/give-up/(a|aa)+$` takes.
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

  One worker refuses the costliest `Origin` about 90 times a second, where one of Envoy's refuses it about 1,000
  times.
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

## What nginx writes with no key

Each of these is in every file the nginx generator writes and differs from the Envoy output, so no row of that table
holds it. Each was measured on nginx 1.30.6 and 1.31.8, and Envoy's answers on Envoy 1.40.2, unless it says otherwise.

- **nginx's own answers carry `Server: nginx`**, with no version, where the Envoy output's own answers carry no `Server`
  header ([Defaults](defaults.md)). These are the `400`, `403`, `404` and `502` the file answers with an empty body, to
  a path it refuses, a request Envoy's parser refuses (a method it does not know, a byte above 0x7F in an HTTP/1.1
  path), a path PCRE2 gave up on, an upgrade other than WebSocket, a request no route takes and a service that refuses
  the connection, the `426` to an HTTP/1.0 request and to one of `HTTP/1.2` to `HTTP/1.9`, whose status line has no
  reason phrase, the `403` with the body `Invalid CORS request` to a foreign browser origin, the `200` to a preflight,
  and the `400` nginx answers to a request it cannot read, such as a path with an invalid escape or a header line over
  8 KiB, with its own HTML page naming nginx. The `403` to an upgrade other than WebSocket carries the body
  `Invalid CORS request` when the request has an `Origin`, with or without `cors`, as the Envoy output's does. A
  service's own `Server` header is passed on, as the Envoy output passes it.
- **Some of nginx's answers carry no `x-request-id`**: its `404` to a host no environment names and its `400` to a path
  with an invalid escape, where every answer of the Envoy output carries one. A request id nginx makes is a version 4
  UUID, as Envoy's is, and a client's own `x-request-id` is kept under `client_address: forwarded` and replaced under
  `connection`, as [The client's address and removed headers](#the-clients-address-and-removed-headers) says. A client
  that sends `x-request-id` twice is answered `400`, where Envoy joins the two with a comma and serves the request.
- **`Vary: Origin` is sent beside a service's own `Vary`**, as a second header line, not merged into the service's.
- **No request log is written, and the error log holds only the lines that stop nginx (`emerg`)**: a file nginx cannot
  load and a port it cannot bind are still printed. At `error` nginx writes the request line of a request whose service
  refused the connection, and at `alert` that of a path PCRE2 gave up on, each with its query, which can hold a token.
  The same level also hides a worker process that crashed (`[alert] worker process 7 exited on signal 11`) and the
  connections running out (`[warn] 4 worker_connections are not enough, reusing connections`), measured on nginx
  1.31.8 by holding more connections open than `worker_connections` allows and stopping a worker with signal 11: at
  `emerg` the log held no line, and at `info` both. The `log` key is refused until a later version writes it.
- **A request header line over 8 KiB is answered `400`**, where Envoy, under a larger limit of its own, passes it on:
  nginx reads a line of 8,190 bytes before its CRLF, `x-long` with a value of 8,182 bytes, and answers one byte more
  `400`, where the value reaches the service through Envoy; one of 70000 bytes is answered `400` by nginx and `431` by
  Envoy. More than 100 header lines are answered `400`, where Envoy answers `431`. A request line over 8 KiB is
  answered `414`: nginx reads a `GET` of a path of 8,177 bytes and answers one of 8,178 bytes `414`, where Envoy serves
  a path of 61,406 bytes in the same request and answers `431` past it. Over HTTP/2 a header field of more than 8,192
  bytes as HPACK sends it, such as a path of 13,108 `a`, ends the connection (`GOAWAY` with `ENHANCE_YOUR_CALM`), where
  Envoy serves it.
- **A service that does not take the connection within 5 s is answered `504`**, after those 5 s, where the Envoy output
  answers `502` to a service it cannot reach.
- **A service that sends nothing is answered `504` when the Envoy output answers it**: 15 s after the request without
  `timeouts.request`, and after 5 minutes with `timeouts.request: none`. nginx counts its limits between two reads or
  writes, so an answer that keeps coming runs past the 15 s at which Envoy ends it, the difference the
  `timeouts.request` row names.
- **A client connection is closed after 1000 requests**, the 1000th answer carrying `Connection: close`, where Envoy
  served 1200 on one connection and kept it open. nginx also closes a connection that has served requests for an hour
  (`keepalive_time`; read in nginx's documentation, not run).
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
  from `:authority` is answered `400`, where Envoy routes by `:authority`.
- **Repeated lines of a list header reach the service as separate lines for the names Envoy joins**: sent twice, as
  `accept: a=1` and `accept: b=2`, `accept`, `via` and `accept-encoding`, each a list in RFC 9110, reach the service as
  two lines through nginx and as one line, `accept: a=1,b=2`, through Envoy, over HTTP/1.1 and HTTP/2: the same HTTP
  message, written as separate lines. A second `Content-Type`, `User-Agent` or `Referer` line is answered `400` (above).
  A second line of `authorization`, `if-modified-since`, `if-unmodified-since`, `if-match`, `if-none-match` or
  `if-range` is answered `400` by nginx with its own page, where Envoy joins the lines with a comma and serves the
  request. `cookie`, `range`, `x-real-ip`, `accept-language`, `date` and a header of a name of your own reach the
  service alike through both.
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
- **A worker process serves 1024 connections at most**, a client's and a service's counted together;
  `limits.connections` is refused until a later version writes it. Read in nginx's documentation of
  `worker_connections`, not run.
- **nginx 1.30 sends a gRPC service a `host` header beside `:authority`**, and nginx 1.31 sends `:authority` alone, as
  Envoy does.
- **A render for nginx reads no key of the [`envoy`](envoy.md) block and refuses none of them**, so tracing written at
  `envoy.tracing` is Envoy's alone; a render for Envoy reads it as before.

## Where nginx is less strict than the Envoy output

These differences are not stricter than the Envoy output: in each, nginx serves a request the Envoy output refuses or
ends, or sends a service what the Envoy output would not. `proxyfile render --target nginx` counts them on standard
error after it writes the files, and nginx's column in [Portability](portability.md#support-by-proxy-from-the-generators)
names each on its row. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2:

<!-- keys: less strict: nginx -->
- **a request trailer whose name is outside the token characters or whose value holds a control byte is served over HTTP/1.1**: A chunked request whose trailer is `x@at: 2`, or whose trailer value holds one of the bytes 0x00 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F, is served, and the service receives its body and no trailer, where Envoy answers `400`: `proxyfile.js` runs before the body arrives and never reads a trailer. nginx answers a line feed or a carriage return in a trailer value `400` itself.
- **a header line with no colon reaches the service as a header with an empty value**: A request holding the line `nocolon`, with no colon, is served over HTTP/1.1, and the service receives `nocolon: `, where Envoy answers `400`. nginx stores such a line as a header with an empty value, as it stores `nocolon:`, which Envoy serves too, so neither `nginx.conf` nor `proxyfile.js` can tell the two apart.
- **a request with no body reaches the service without content-length: 0**: A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`.
- **a WebSocket upgrade a service answers without switching passes the service's body**: A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body.
- **without timeouts.request, an answer that keeps arriving runs past 15 s**: Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes.
<!-- /keys -->
