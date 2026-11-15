# HAProxy

`proxyfile render --target haproxy` writes `haproxy.cfg`, a whole HAProxy configuration, from the same proxyfile as
Envoy's, and beside it `proxyfile.lua`, a script the configuration loads (below). It is written for HAProxy 3.2 and
3.4: line 2 names the script, lines 3 to 5 stop an older HAProxy, and
[`scripts/validate-haproxy.sh`](../scripts/validate-haproxy.sh) loads every `haproxy.cfg` the examples commit, with
its script, with `haproxy -c` on HAProxy 3.2.26 and 3.4.7, the two images
[`scripts/haproxy-images`](../scripts/haproxy-images) pins, a warning counted as an error. `proxyfile check` compares
both files.

This version writes routing, TLS and CORS: the listener, the admin port's `/ready`, `tls` with the clients CA,
`client_address`, the services with their HTTP and gRPC ports, their IP addresses (with no zone) and `host_header`,
`remove_from_clients` by name and by prefix, `cors`, each environment's hosts, origins and patterns, the routes by
prefix, path, pattern and method with their rewrites, and the rpc entry with its methods. It refuses every other core
key a file writes, naming the key where it is written, so a file that needs one is never rendered as if the key were
not there: `limits`, a time in `timeouts.request`, `timeouts.idle`, `timeouts.upstream_idle`, a DNS name in a
service's address, `dns`, `auth` and a route's `auth`, a service's `balance`, `hash` and `health`, `retries` and
`rate_limit` on a route or the rpc entry, an RPC method's `action` and `reports`, `log` and `tracing`. HAProxy's column
in [Portability](portability.md#support-by-proxy-from-the-generators) says how it writes each key and, for a key it
refuses, what to write instead.

## Running the file

The official HAProxy image runs `haproxy -f /usr/local/etc/haproxy/haproxy.cfg`, so the file goes at that path,
mounted or copied into the image, with `proxyfile.lua` beside it: the configuration reads the script from its own
folder (`default-path config`) and does not load without it. With `tls`, HAProxy reads the certificate, its key and the clients CA at the paths
the `tls` keys give, and refuses the file when one is missing; the image runs HAProxy as the user `haproxy`, which must
be able to read them. The admin port is bound on `127.0.0.1` alone. Measured on HAProxy 3.2.26 and 3.4.7: each image,
given [`examples/minimal/yaml/haproxy.cfg`](../examples/minimal/yaml/haproxy.cfg) in `/usr/local/etc/haproxy` and no
command of its own, printed `Loading success.` and answered requests on the file's port, and `haproxy -c` refused
[`examples/tls/yaml/haproxy.cfg`](../examples/tls/yaml/haproxy.cfg) on HAProxy 3.4.7 with no certificate at its path
(`cannot open the file '/etc/gateway/tls/certificate.pem'`).

## The versions

HAProxy 3.0.30 stops at the file's version guard, before it reads the script: `haproxy -c` prints
`proxyfile writes this file for HAProxy 3.2 and 3.4; this HAProxy is older, so it stops here` and exits 1.

The path rules below are HAProxy's `normalize-uri` actions, which its manual calls an experimental technical preview.
The file turns them on with `expose-experimental-directives`, without which HAProxy 3.2.26 and 3.4.7 refuse it
(`'normalize-uri' action is experimental, must be allowed via a global 'expose-experimental-directives'`). HAProxy's
manual says such features "may break during the maintenance cycle", so a maintenance release of 3.2 or 3.4 may change
how a path is normalised; the images `scripts/haproxy-images` pins are the ones this version was checked on.

## The script beside the file

`proxyfile.lua` holds what HAProxy's own configuration cannot write, for HAProxy's Lua 5.4.7, which both images carry,
loaded by every thread (`lua-load-per-thread`). Line 1 is the same header as the configuration's, so `proxyfile check`
fails on a hand edit of either file. Its bytes are the same for every file: what differs from one file to another, each
pattern's program, is written in the configuration, so two files rendered into one folder share the script and
`proxyfile check` finds each current.

- **`pf_join` writes the lines of each header Envoy joins as one line**, before any rule reads them: the values that
  are not empty, in the order they came, joined with a comma, or one empty line when every line is empty, as Envoy
  1.40.2 does. Envoy joins 40 headers, the ones of the request header map it prints at start that it neither removes
  nor reads as the request's framing, among them `Origin`, `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Request-Id`,
  `User-Agent`, `Authorization` and the CORS request headers; the lines of any other header, such as `Cookie`, reach
  the service as sent through both. HAProxy passes each line on, and none of its fetches reads every line of a header
  as one value. Every rule after it reads a header's whole line, so a single `Origin` line that holds a listed origin
  after a comma is not that origin, as for Envoy.
- **`pf_escape` escapes what Envoy escapes before it picks a route**: a raw `"`, `<`, `>`, `^`, a backquote, `{`, a
  vertical bar, `}` and every byte above 0x7F, in upper case hex, as `%C3%A9` for `é` over HTTP/2. HAProxy's `regsub`
  writes one fixed text for every match, so it cannot write the hex of the byte it found.
- **`pf_route` matches a route's pattern as RE2 does, and never gives up.** Envoy matches a pattern with RE2, whose
  time grows with the path's length and never runs out. HAProxy's own regular expressions are PCRE2, which gives up
  on some paths and reads that as no match: with `^/g/((a|aa)+c|a+b)$`, `/g/` with 40 `a` and a `b` went to the
  prefix route after it on HAProxy 3.2.26 and 3.4.7, where Envoy took the pattern route. The configuration gives
  `pf_route` the program Go's `regexp/syntax` compiles the pattern to, as one argument, and Go's regular expressions
  read a pattern as RE2 does: `$` only at the end, `\b`, `(?i)`, classes, and `{,3}` as text. The script runs the
  program as RE2 does, as an automaton it builds while it reads, so a byte read again from a state it has seen costs one
  table read and the time grows with the path's length, never with the program's size.

A request the script did not finish with, headers it did not join, a path it did not escape or a pattern it did not
finish matching, is answered `400`, so a failed run never reads as no match.

### What a pattern route costs

`pf_route` is an action, which HAProxy runs in steps, letting the other requests of its thread run between them; a
sample fetch, which the first form of this file used, runs at once. With 24 pattern routes such as
`/api/.*[0-9a-f]{20}\.js1` and a path of 14,000 bytes, that fetch held a thread past HAProxy's watchdog, which stopped
HAProxy 3.2.26 and 3.4.7 (`PANIC! Thread 1 is about to kill the process`, exit 134); Envoy 1.40.2 answered in 2.6 ms.
Measured on an arm64 Mac, with HAProxy's 12 threads:

- **The same request is now served**: 50 sent one at a time took 10.8 to 10.9 ms at the median and 20 ms at most on
  both images, where Envoy took 1.1 ms at the median and 3.4 ms at most. A path HAProxy reads costs time for each
  pattern route that reads it to its end; a route whose program cannot take the path stops at the first byte that ends
  every path through it.
- **A request's patterns have a budget** of 500,000 units of work: a byte read counts 1, each instruction visited while
  a new state is built 6, and each run 64 and one for each 512 bytes of its program. A request past it is answered
  `400`, where Envoy's RE2 always decides, the difference HAProxy's row of `routes[].pattern` names. The worst built:
  80 such routes and that path, answered `400` after 19 to 29 ms, and four patterns of the form
  `^/s/(a|b)*a(a|b){12}c1$`, whose states grow with each byte, with a path of 14,000 random `a` and `b`, answered
  `400` after 25 to 31 ms; Envoy served both in 2 to 18 ms, and both images answered the next request.
- **The budget is about 24 ms of CPU for one request alone, and about 33 ms for each of many at once.** With 40
  routes `/s/(a|b)*a(a|b){12}cNN`, `NN` from `01` to `40`, and paths of `/s/` and 400 random `a` and `b`, which spend
  the whole budget:

  | Requests | HAProxy 3.2.26 | HAProxy 3.4.7 | Envoy 1.40.2 |
  |---|---|---|---|
  | 50, one at a time | `400`, 23.6 ms of CPU each, 23.9 ms from send to answer at the median | `400`, 23.8 ms of CPU each, 23.7 ms at the median | `200`, 3.7 ms at the median |
  | 6,000 over 48 connections at once | `400` in 16.0 s, 32 ms of CPU each, 128 ms at the median and 182 ms at most | `400` in 16.7 s, 33 ms of CPU each, 133 ms at the median and 189 ms at most | `200` in 0.62 s, 0.8 ms of CPU each, 4.8 ms at the median and 31 ms at most |

  About 370 such requests a second keep HAProxy's 12 threads busy. A request that reads no pattern, sent beside the
  6,000, took 4.8 to 5.1 ms at the median and 28 ms at most, because the action runs in steps.
- **The states of a thread's patterns share one budget** of 40,000 units: a state counts 8, one for each instruction
  it waits on, and one for each byte whose next state it knows. A unit held 26 to 28 bytes of Lua's memory, so a
  thread keeps about 1 MB of states at most, and HAProxy that times its threads, about 13 MB with 12. When a thread
  reaches the budget, it forgets every state it keeps and builds them again as it reads, within each request's budget.
- **The states are kept for paths that come again**: a byte read from a state the thread keeps costs 1 unit, where
  building the state costs 6 for each instruction visited. 200 paths sent again and again through the 73 pattern routes
  of the battery's HAProxy file kept about 26,000 units a thread, and 6,000 of them over 24 connections took 0.37 to
  0.47 s a round with a budget of about 1, 2, 4 or 8 MB a thread alike, and 1.5 to 1.6 s in a copy of the script that
  keeps no state; Envoy took 0.30 s. The 24-route request above, with its path of 14,000 bytes, was served in 10.6 to
  11.0 ms at the median with each of those budgets, where Envoy took 1.1 ms; with no state kept it ran past its budget
  and was answered `400`.
- **A flood of new paths reads no state twice**, so the budget only bounds what it keeps. Three rounds of 6,000
  requests over 24 connections, the memory at the start and after the third, with each budget a thread:

  | Budget | 40 routes `/kNN/(a\|b)*a(a\|b){12}c`, paths of `/kNN/` and 1,000 random `a` and `b`: 3.2.26, 3.4.7 | One route `/g/.*[a-z]{1,1000}x`, paths of `/g/` and 1,000 random letters: 3.2.26, 3.4.7 |
  |---|---|---|
  | About 1 MB, the script's | 82 to 130 MB, 91 to 139 MB; every request served, 5.9 to 6.4 s a round | 82 to 190 MB, 89 to 194 MB; every request answered `400`, 23 to 25 s a round |
  | About 2 MB | 83 to 157 MB, 91 to 166 MB; every request served, 5.9 to 6.5 s a round | 82 to 197 MB, 91 to 205 MB; every request answered `400`, 24 to 26 s a round |
  | About 4 MB | 81 to 208 MB, 91 to 218 MB; every request served, 6.3 to 7.2 s a round | 82 to 243 MB, 89 to 248 MB; every request answered `400`, 24 to 26 s a round |
  | About 8 MB | 80 to 310 MB, 88 to 317 MB; every request served, 6.1 to 6.8 s a round | 80 to 344 MB, 88 to 352 MB; every request answered `400`, 23 to 27 s a round |
  | None, each pattern keeping up to 2,000 states a thread | 80 to 648 MB, 88 to 649 MB; every request served, 7.2 to 8.5 s a round | 80 to 787 MB, 88 to 797 MB; 661 to 816 of each 6,000 served, 20 to 28 s a round |
  | Envoy 1.40.2 | 20 to 99 MB; every request served, 0.34 to 0.41 s a round | refuses the file: the program is longer than 100 instructions |

  With about 1 MB, the first flood grew HAProxy by about 48 MB, about 4 MB a thread, as much as the 200 paths that come
  again did, so what it adds over them is not the states. Lua frees a forgotten state when its collector runs. A
  collector pause of 120, where Lua's default is 200, set in a copy of the script, held the first flood to 110 and
  121 MB, but its rounds took up to 40% longer and the longest 24-route request 30 to 32 ms where it took 20 ms; a
  pause of 100 made a round take over 140 s. The script leaves the collector as HAProxy starts it.
- **Each thread reads a program once**, at its first request: with 80 pattern routes a thread's first request took 6.5
  to 10 ms and every later one 1.1 to 2.2 ms, as Envoy's.
- **Lua's own limit on an action's time** (`tune.lua.session-timeout`, 4 s by default) is never reached, the budget
  ending a run first. With the limit lowered to 5 ms in a copy of the file, the run that reached it was answered `400`,
  HAProxy printed `Lua function 'pf_route': execution timeout.`, and it served the next request.

## The path a route is matched on

The file normalises the path before any rule reads it, as the Envoy output does: the script escapes what Envoy
escapes, `%2F`, `%5C` and a backslash are read as `/`, an escaped letter, digit, `-`, `.`, `_` or `~` is decoded,
repeated slashes are merged and dot segments resolved, and the route and the service read that path. Every escape it
does not decode reaches the service as written, in the case of its hex digits, and so does an invalid one such as
`%zz`; `%00` in a path is answered `400`, as by Envoy. A path that still holds a dot segment with a `;` parameter, the
`;` written or escaped once, is answered `400` once its route, its upgrade and its origin are checked, as by Envoy,
whose file refuses it by a route of its own (below). Measured on
HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2; one difference stays, named on HAProxy's row of it in
[Portability](portability.md#support-by-proxy-from-the-generators):

- **A dot segment whose `;` parameter is written with dots, such as `/a/..;/b`, is answered `400`**, where Envoy 1.40
  drops the parameter and resolves the segment: HAProxy's `path-strip-dot` and `path-strip-dotdot` do not take `..;`
  or `.;` as a dot segment, so the rule above refuses the path.

## The request line and the headers

Before any other rule the file answers what Envoy refuses and HAProxy's own parsers pass: every HTTP/1 version but 1.1
with `426`, from `HTTP/0.0` to `HTTP/9.9`, and with `400` a method Envoy's HTTP/1 parser does not know (a known one in
another letter case too), and over HTTP/2 a request past Envoy's 100 header fields. It removes the headers Envoy
removes: `Keep-Alive`, `Proxy-Connection`, `X-Forwarded-Client-Cert`, the 16 `x-envoy-` headers that steer an Envoy,
`Connection` but a WebSocket upgrade's, and a request's trailers. From a service's answer it removes `Keep-Alive`, which
HAProxy would pass on to an HTTP/1.1 client and Envoy does not. It sends `TE: trailers` in any letter case on when
`Connection` names `te`, as Envoy does. HAProxy counts the request line among its 101 header lines, so 100 header lines
are served and 101 refused, as by Envoy.

Measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2, the differences below refuse a request Envoy serves or
answer one it refuses with another status. Each is named on HAProxy's row of the request line in
[Portability](portability.md#support-by-proxy-from-the-generators). The ones that serve what Envoy refuses, or send a
service what Envoy would not, are in [the next section](#where-haproxy-is-less-strict-than-the-envoy-output).

- **An absolute target is answered `400` over HTTP/1.1**, where Envoy routes it by its host and sends the path alone,
  with that host as `Host`. HAProxy sends the target on in absolute form, and before any rule reads the request it
  drops an empty or default port from the target and the `Host` header, which Envoy keeps or, empty, answers `400`.
  `OPTIONS *` and `CONNECT` are answered `400`, where Envoy answers `404`.
- **A `Connection` token other than `close`, `keep-alive`, `upgrade`, `HTTP2-Settings` and `te` is answered `400`**,
  where Envoy removes the header the token names and serves the request. HAProxy passes the token and the header on.
- **Ten `Connection` elements or more are answered `400`**, as Envoy answers ten tokens or more, but HAProxy counts an
  empty element too, all but an empty last one, where Envoy does not: `Connection: te,,,,,,,,,,` is answered `400`,
  where Envoy serves it.
- **An `h2c` upgrade is answered `400`**, where Envoy serves it as a plain request without its `Upgrade` and
  `HTTP2-Settings`. HAProxy drops the upgrade's `Upgrade` header and its `upgrade` token before any rule reads the
  request and keeps the token `HTTP2-Settings`, so the file answers `400` to a request whose `Connection` names
  `HTTP2-Settings`; one that carries the header alone is served and passes it on, as through Envoy. A cleartext
  `curl --http2` sends that upgrade, so it gets `400`, where `curl --http2-prior-knowledge` is served (measured with
  curl 8.7.1).
- **A WebSocket over HTTP/2, an extended `CONNECT` (RFC 8441), is answered `400`**, where Envoy closes the connection.
  HAProxy 3.2.26 sent it to the service as an HTTP/1.1 upgrade with a `Sec-WebSocket-Key` of its own; HAProxy's parser
  refuses an `Upgrade` field a client sends over HTTP/2, so the file answers `400` to an HTTP/2 request that holds one.
- **A header line folded onto the next is answered `400`**, where Envoy serves it and drops the line break. HAProxy
  writes the line break as two spaces, so the file refuses a header value that holds three spaces or tabs in a row,
  folded or not.
- **A WebSocket upgrade with no `Sec-WebSocket-Key` is answered `400`** by HAProxy's parser, where Envoy passes it on,
  or answers `403` to an origin it does not admit.
- **More than 100 header lines are answered `431`, or `400` from 102 lines**, where Envoy answers `431`, and over
  HTTP/2 a request with 97 header fields or more is answered `400`, where Envoy resets its stream. HAProxy joins the
  cookie fields of an HTTP/2 request into one line, so the file counts each cookie as a field: a request with fewer
  fields whose cookies reach 97 is answered `400` too. Envoy counts a header's lines after it joins them, so it serves
  101 lines or more of one header it joins, which HAProxy answers `400`, and over HTTP/2 97 fields or more of one,
  which HAProxy answers `400` or, with 120, resets.
- **Two `Host` lines of different hosts, and a `Host` that holds a comma, are answered `400`** by HAProxy's parser,
  where Envoy finds no environment for the joined or whole value and answers `404`.
- **`Transfer-Encoding: chunked` on two lines is answered `400`, and `Transfer-Encoding: gzip, chunked` `422`**, where
  Envoy answers `501`.
- **A header of 20,000 bytes is answered `431`, and a path of 20,000 bytes `414`**, where Envoy, under its default limit
  of 60 KiB, serves both. Over HTTP/2, a path whose escaped form outgrows HAProxy's buffer, such as one of 7,000 bytes
  above 0x7F, is answered `400` by HAProxy 3.4.7 and `500` by 3.2.26, where Envoy serves it.
- **A request line with no version, HTTP/0.9's own form, is answered `400`**, where Envoy answers `426`.
- **Over HTTP/2, a header or trailer name outside RFC 9110's token characters or a `#` in `:path` resets the stream**,
  where Envoy closes the connection or answers `400`, and a request with neither `:authority` nor `host` is answered
  `404`, where Envoy closes the connection.
- **Over HTTP/1.1, a request trailer whose name is outside those characters ends the connection with no answer** when
  the body comes after the headers, where Envoy answers `400`.

## Where HAProxy is less strict than the Envoy output

These differences are not stricter than the Envoy output: in each, HAProxy serves a request the Envoy output refuses or
ends, or sends a service what the Envoy output would not. HAProxy's parsers decide each before any rule of the file
reads the request, and its timeout counts the time without data, so the file cannot close them, but for the one on a
control byte in a value: that one is measured for every control byte, and this version writes no rule for it.
`proxyfile render --target haproxy` counts them on standard error after it writes the files, and HAProxy's column in
[Portability](portability.md#support-by-proxy-from-the-generators) names each on its row. Measured on HAProxy 3.2.26 and
3.4.7 beside Envoy 1.40.2:

<!-- keys: less strict: haproxy -->
- **an HTTP/2 :authority with a default or empty port reaches the service without it**: Over HTTP/2, `:authority: api.example.com:` and, over plain HTTP, `api.example.com:80` reach the service as `Host: api.example.com`, where the Envoy output sends them as written.
- **ten Connection tokens close or keep-alive are served**: `Connection: close` ten times on one line is served, where Envoy answers ten tokens `400`: HAProxy removes `close` and `keep-alive` from the header before any rule reads it, so the file's count of ten elements does not see them.
- **a request with no body reaches the service without content-length: 0**: A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`, as it does to `PUT`, `PATCH`, `OPTIONS`, `PROPFIND` and an unknown method; a rule that set the header did not reach the service.
- **two Host lines of one host are served**: `Host: api.example.com` on two lines reaches the service as one line, where Envoy reads the two as one value no environment names and answers `404`.
- **two Content-Length lines of one value are served**: `Content-Length: 3` on two lines, with a body of 3 bytes, reaches the service as one line, where Envoy answers `400`; over HTTP/2 two `content-length: 0` fields are served, where Envoy closes the connection.
- **Content-Length beside Transfer-Encoding: chunked is served**: `Content-Length: 3` beside `Transfer-Encoding: chunked`, with a chunked body, reaches the service chunked and with no `Content-Length`, where Envoy answers `400`.
- **over HTTP/2, two host fields and no :authority are served**: Over HTTP/2 with no `:authority`, `host: api.example.com` and then `host: evil.example.net` reach the service as the first, and the same host twice as one, where Envoy closes the connection.
- **a WebSocket upgrade a service answers without switching passes the service's body**: A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body.
- **a header or trailer value holding a control byte is served**: A header value holding one of the bytes 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F reaches the service as sent, over HTTP/1.1 and HTTP/2, and a chunked request whose trailer value holds one is served, the service receiving its body and no trailer, where Envoy answers `400` over HTTP/1.1 and closes the connection over HTTP/2. HAProxy refuses 0x00, a line feed and a carriage return in either value, with `400` over HTTP/1.1 and a reset stream over HTTP/2.
- **without timeouts.request, an answer that keeps arriving runs past 15 s**: Without `timeouts.request`, a service that keeps sending runs past 15 s, where the Envoy output ends a request 15 s after it starts: HAProxy's `timeout server` counts the time without data (on HAProxy 3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served).
<!-- /keys -->

## What a file holds to load

HAProxy reads at most 65 words on a line of its file: it refused a line of 66 with `too many words, truncating after
word 64` (measured on HAProxy 3.2.26 and 3.4.7). The file writes each list of the proxyfile one value a line, as acl
lines of one name, which HAProxy joins with or: an environment's hosts, origins, patterns and its own hosts' origins,
the rpc entry's paths, and one rule per environment where an environment's lists are read. Each value of an acl line
comes after `--`, the end of the line's flags, so a host the reader admits that starts with `-` is a host: without it
HAProxy 3.4.7 read `-f` as a flag that names a pattern file and refused the file, and read `-i` and `-n` as flags,
which left the line no host to match. A file of 600 prefix routes,
50 pattern routes and an environment of 100 hosts, 100 origins and 100 patterns loaded on both and routed a request to
its 600th route and its 50th pattern. `Generate` ends with a scan of every line, so a line of more words is a bug
proxyfile reports, never a file HAProxy refuses.

HAProxy read a word of 5,000,000 bytes on both, so no value is refused for its length but one written into a regular
expression, which PCRE2 compiles to a size it limits: HAProxy loaded an expression of 32700 letters and refused one of
32800. A host and a pattern of an environment, and the prefix or path of a route with a `rewrite`, are written into one,
and render refuses each, by its key, whose expression is longer than 16384 bytes: a letter takes two bytes compiled,
the most of any character those expressions hold. A route's pattern is written as its program, `pf_route`'s argument,
and render refuses one whose program is longer than 65,536 bytes: HAProxy loaded an argument of 200,000 bytes, and
Envoy 1.40.2 refuses an RE2 program of more than 100 instructions, a pattern far shorter.

A host written with a dot at its start or its end, such as `.dot.example.com` or `dot.example.com.`, is compared with
the `Host` header as written, as Envoy compares it. A service address with an IPv6 zone is refused (HAProxy's row of
`services.<service name>.address` in [Portability](portability.md#support-by-proxy-from-the-generators)).

## The browser origins an environment admits

Every file checks the browser origin once a route is picked, as the Envoy output does, with or without `cors` and
lists of origins: a request or a preflight whose `Origin` an environment neither lists, covers with a pattern nor
serves as one of its own hosts is answered `403` with the body `Invalid CORS request`; an origin on one of the
environment's own hosts is admitted with no `Access-Control-*` header; and every answer carries `Vary: Origin`.
Measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2 with `examples/minimal` and `examples/grpc`, and with lists of
origins and patterns over HTTP/1.1 and HTTP/2:

- **The `Origin` header is read as one line.** Two `Origin` lines are joined with a comma first (`pf_join`), and a line
  that holds a comma is compared whole: `https://evil.example.net,https://example.com` is neither the listed
  `https://example.com` nor an origin on the environment's own host, so it is refused, and a pattern that covers the
  whole line, as `https://*.example.com` covers `https://evil.example.net,https://www.example.com`, admits it with
  `Access-Control-Allow-Origin` holding the whole line, as Envoy does. An empty `Origin` line beside another is left
  out. A listed origin is compared in any letter case and a pattern in the case written; an origin with a port its
  entry does not name, with a trailing dot, or `null` is refused.
- **The answers come in Envoy's order.** A request no route takes is answered `404` before its upgrade or its origin is
  read; an upgrade other than WebSocket is answered `403` next, with the body `Invalid CORS request` when the request
  carries an `Origin`, as every `403` of Envoy's own; then the origin; and a dot segment with a `;` parameter is
  answered `400` after it. None of these answers carries an `Access-Control-*` header, a preflight to a refused path
  included, as with Envoy.
- **A preflight's answer copies the request's whole lines**: `Access-Control-Allow-Methods` and
  `Access-Control-Allow-Headers` hold what the request asks for, joined with a comma when it asks on two lines.
- **A pattern with more than one `*` is matched in a time that grows with the `Origin`'s length alone.** An origin
  matches a pattern when it starts with the part before the first `*`, ends with the part after the last, and holds
  each part between them in order; the file matches each such part at its first place, in a group PCRE2 never tries
  again elsewhere, so the patterns admit and refuse as Envoy's RE2 does. Written with `.*` for each `*`, 64 patterns
  such as `https://*.*.example1.org` and an `Origin` of 8,000 bytes stopped HAProxy 3.2.26 and 3.4.7 (exit 134), where
  Envoy answered `403` in 2.2 ms; written so, both images answer it `403` in 1.5 to 2.3 ms. The worst built: 100
  patterns of eight `*` and an `Origin` of 15,000 bytes, near the most HAProxy reads, answered `403` in 1.4 to 2.4 ms,
  Envoy's in 3 to 5.6 ms.

With `allow_headers: "*"`, a preflight that asks for no headers gets no `Access-Control-Allow-Headers`, where Envoy
sends the header empty: the difference HAProxy's row of `cors.allow_headers` names.

## TLS

The listener takes TLS 1.2 and 1.3, of TLS 1.2 the ECDHE suites with AES-GCM or ChaCha20 alone, and the key exchange
groups X25519MLKEM768, X25519 and P-256 alone, as Envoy 1.40.2 does (`ssl-default-bind-ciphers` and
`ssl-default-bind-curves`; scanned with `openssl s_client` one version, suite and group at a time), and answers ALPN
`h2` and `http/1.1`. A server name that matches no host, in capitals, with a trailing dot, or no server name, is served
with the one certificate; with `tls.clients`, a `Host` that names another environment than the server name is served;
and plain HTTP to the TLS port has its connection closed, each as by Envoy. With `tls.clients`, every client
certificate Envoy refuses is refused in the handshake: none, one another CA of the same name signed, an expired one, one
not valid yet, a self-signed one, one for servers only, and one under an intermediate sent without it. Measured on
HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2; two differences stay, named on HAProxy's rows of `tls` and
`tls.clients.ca`:

- **Of the TLS 1.2 suites both take, HAProxy picks by its own order**: a client that offers OpenSSL's default list is
  given `ECDHE-RSA-AES128-GCM-SHA256`, where Envoy gives `ECDHE-RSA-CHACHA20-POLY1305`, because BoringSSL holds those
  two equal and takes the client's order, which OpenSSL cannot write. Over TLS 1.3 both gave `TLS_AES_256_GCM_SHA384`
  with the group X25519MLKEM768.
- **A client certificate that is not valid yet is refused with the alert `bad_certificate`**, where Envoy sends
  `certificate_expired`.

## The client's address

With `client_address: connection` the file takes each client for one it faces, as the Envoy output does: the service
receives the client's `X-Forwarded-For` with the peer's address added on the same line, as `1.1.1.1,10.0.0.5`, or the
peer's address alone when the client sent none or an empty one; `x-envoy-external-address`, `x-forwarded-proto` and
`x-request-id` are the file's own; and the headers only a proxy in front may send, `x-envoy-decorator-operation`,
`x-envoy-downstream-service-cluster`, `x-envoy-downstream-service-node`, `x-envoy-original-host` and
`x-envoy-original-path`, are removed. With `forwarded` those pass, and the client's `X-Forwarded-For`,
`X-Forwarded-Proto` and request id are kept, each joined into one line when sent on two. A header spelled with
underscores, such as `x_forwarded_proto`, is another header to both proxies and reaches the service as sent. Measured
on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2 over HTTP/1.1 and HTTP/2.

## What HAProxy writes with no key

Each of these is in every file the HAProxy generator writes. Each was measured on HAProxy 3.2.26 and 3.4.7, and
Envoy's answers on Envoy 1.40.2, unless it says otherwise.

- **No request is tried again** (`retries 0`): without the line, HAProxy 3.4.7 tried a connection the service refused
  three more times, where the Envoy output tries a request again only when a route's `retries` asks, a key this
  version refuses.
- **A service has 5 s to take the connection** (`timeout connect 5s`), Envoy's default connect timeout (read in
  Envoy's documentation, not run).
- **5 minutes with no data from the client end a request** (`timeout client 300s`), Envoy's default stream idle
  timeout (read in each proxy's documentation, not run). **A service that sends nothing for 15 s is answered `504`**
  (`timeout server 15s`), at the same time as by the Envoy output, which ends a request 15 s after it starts when
  `timeouts.request` is not written (measured over HTTP/1.1 and HTTP/2: `504` after 15.01 s, Envoy's after 15.00 s).
  HAProxy counts its limits in time without data, so a service that keeps sending runs past 15 s (measured on HAProxy
  3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served), the difference
  HAProxy's row of `timeouts.request` names. With `timeouts.request: none` the service has 5 minutes of silence
  (`timeout server 300s`), as with Envoy.
- **A service that refuses the connection is answered `502` with an empty body**, at once over HTTP/1.1 and HTTP/2,
  as by the Envoy output: HAProxy's own answer is a `503`, which the file writes with an empty body
  (`http-error status 503`) and turns into `502`. Over HTTP/1.1, a service that does not take the connection within
  the 5 s gets the same `502`. A service that closes the connection before its answer gets an empty `502` too
  (`http-error status 502`), where HAProxy's own is an HTML page.
- **Each request reaches a service on a connection of its own** (`option http-server-close` and
  `option http-pretend-keepalive`, and `max-reuse 0` on a gRPC server's line): an HTTP service's connection is closed
  after its answer, and a gRPC service's carries one call. HAProxy kept its connections to a service, and when the
  service closed one it reused before its answer, HAProxy sent the request again on a new one, over HTTP/1.1 by
  closing the client's connection, which the client tried again, and over HTTP/2 by refusing the stream: the service
  received a `POST` twice, where Envoy answers `502` and sends it once. Now the service receives each request once,
  and one it closes before its answer gets `502`, as by Envoy (measured over HTTP/1.1 and HTTP/2 clients, to an HTTP
  and a gRPC service). The service receives no `Connection` header, as from Envoy; without the second option HAProxy
  sent `Connection: close`. **The cost is a new connection to the service for every request**, where Envoy keeps a
  pool: a service sees one connection a request, and under heavy load the connections HAProxy opens, and the ports
  they hold on its side, become a limit.
- **An HTTP/2 client whose service never answers the connect is told to retry the request on another connection after
  5 s** (`REFUSED_STREAM`), where an HTTP/1.1 client gets the `502` and Envoy answers `502` over HTTP/2 after the same
  5 s. No service received the request, as the reset says, and curl retried it five times over 30 s before it gave
  up.
- **Every response carries `x-request-id`**, as with Envoy: a version 4 UUID HAProxy makes (`uuid(4)`), as Envoy
  makes one, or with `client_address: forwarded` the client's own, which the service receives too; with `connection`
  a client's own id is replaced, as Envoy replaces it.
- **HAProxy logs no request**, where the Envoy output logs every request with no `log` key: the file writes no `log`
  line, a key this version refuses, as HAProxy's row of `log` says, so no request line, path or query reaches HAProxy's
  output. A worker that stopped on a signal 11 printed
  `Current worker (8) exited with code 139 (Segmentation fault)`, and HAProxy then stopped every process and exited
  with 139. HAProxy sets its connection limit from the limit on open files at start and prints it
  (`Automatically setting global.maxconn to 120.` with 300 files): with the table full, a new connection waited
  unanswered and nothing was printed, and it was served once others closed.
- **WebSocket is the one upgrade passed on**, as by Envoy: a request with another `Upgrade`, or with an `Upgrade` that
  holds more than `websocket` on one line or two, is answered `403`, and an `Upgrade` that `Connection` does not name
  is removed. An `h2c` upgrade is answered `400` (above).
- **A render for HAProxy reads no key of the [`envoy`](envoy.md) block and refuses none of them**, so tracing written
  at `envoy.tracing` is Envoy's alone; a render for Envoy reads it as before.
