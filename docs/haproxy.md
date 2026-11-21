# HAProxy

`proxyfile render --target haproxy` writes `haproxy.cfg`, a whole HAProxy configuration, from the same proxyfile as
Envoy's, and beside it `proxyfile.lua`, a script the configuration loads (below). It is written for HAProxy 3.2 and
3.4: line 2 names the script, lines 3 to 5 stop an older HAProxy, and
[`scripts/validate-haproxy.sh`](../scripts/validate-haproxy.sh) loads every `haproxy.cfg` the examples commit, with
its script, with `haproxy -c` on HAProxy 3.2.26 and 3.4.7, the two images
[`scripts/haproxy-images`](../scripts/haproxy-images) pins, a warning counted as an error. `proxyfile check` compares
both files.

HAProxy's column in [Portability](portability.md#support-by-proxy-from-the-generators) says, for each key, whether
this version writes it and how, and for a key or a form it refuses, what to write instead. A render refuses such a key
where the file writes it, so a file that needs one is never rendered as if the key were not there. A key HAProxy has a
directive of its own for is written with that directive, the file's values and HAProxy's defaults for the rest: the
sections from [Limits and timeouts](#limits-and-timeouts) on name each directive. [The auth check](#the-auth-check)
says how it asks a check service.

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

## The auth check

With `auth.ask` the file asks your check service about a request before it goes on to the route's service, and passes
an asked request on only when the check lets it through. [Auth](auth.md) says what the check does and what it is told;
this section says how HAProxy asks it and where its answers differ from the Envoy output's. The file asks with
`pf_auth`, an action in `proxyfile.lua`, which connects to the relay, a TCP `listen` section at the end of
`haproxy.cfg` that passes the connection on to the check service. For the `auth` block of
[Your own service](auth.md#your-own-service), with `listen: 8000` and `accounts` at `10.0.0.7`,
`proxyfile render --target haproxy` writes this call:

```
http-request lua.pf_auth 'abns@proxyfile-8000-check' '/check' 2000 deny 'cookie' 'x-user-id,x-user-roles' 0 if { var(txn.pf_ask) -m bool }
```

Its words are the relay's address, the path of `ask`, `timeout` in milliseconds, `when_unavailable`, the `send` names,
the `accept` names and `send_request` as `1` or `0`; a name that ends in `*` is a prefix, and `-` stands for no name.
The script reads nothing else of the proxyfile, so its bytes are the same for every file. A request is never sent to
the check twice, and an answer is used for its own request alone: each call opens a socket of its own.

### Which requests are asked

The check is asked about a request when all of these hold, which the file writes once, as the variable `txn.pf_ask`:

- the header `only_with_header` names has a line that is not empty, or comes on two lines or more, or comes at all over
  HTTP/2; with no `only_with_header`, every request passes this test. Envoy reads the header's lines as one value,
  joined by commas, and asks a value that holds a byte: two empty lines of a header the script passes on line by line,
  such as `x-api-key`, hold a comma and are asked, and two empty lines of one it joins, such as `authorization`, are one
  empty line and are not. HAProxy reads an HTTP/2 value of spaces alone as empty, where Envoy asks it, so it asks every
  HTTP/2 request that carries the header, an empty one too, which Envoy does not ask, but a `cookie` field of two spaces
  or more, which is answered `400` first ([the request line and the headers](#the-request-line-and-the-headers))
  (measured with one to three lines of empty, spaces and a value in each order, for `authorization`, `x-api-key` and
  `cookie`, over HTTP/1.1 and HTTP/2, on Envoy 1.40.2, HAProxy 3.2.26 and 3.4.7);
- the route it takes is not `auth: off`;
- it is not a CORS preflight that the Envoy output's CORS filter answers: an `OPTIONS` request with an `Origin`, an
  `Access-Control-Request-Method` that is not empty and no dot segment in its path.

A request whose path still holds a dot segment with a `;` parameter is asked whatever its route, as the Envoy output asks
it: the route that refuses such a path is checked too ([A route with `auth: off`](auth.md#a-route-with-auth-off)). The
check comes after the file's refusals of an unknown host, of a request no route takes, of an upgrade other than WebSocket
and of an origin the environment does not admit, and before its `400` for a dot segment, as the Envoy output asks after
CORS and before the route that answers a dot segment. A request one of those refusals answered is not asked.

### What the check is sent

The script sends the check one HTTP/1.1 request, to the path of `ask`, on a connection of its own that the check
closes after its answer. It holds:

- the client's method, its `Host` as the client wrote it, and `authorization`;
- `x-gateway-action` and `x-gateway-reports`, for a call of an rpc method that has an `action` or `reports` (below);
- each header a `send` name covers, written once with all its values joined by commas, as Envoy sends them:
  `X-Country: RO` and `X-Country: FR` on two lines reach the check as `RO,FR`, and two `Cookie` lines as `a=1,b=2`. A
  `send` name never copies a client's `content-length`, `transfer-encoding`, `connection`, `keep-alive`, `te`,
  `trailer`, `upgrade`, `expect` or `proxy-connection`, whatever it names, and `host` is sent once;
- `content-length: 0`, `connection: close` and `x-gateway-timeout-ms`, which holds `auth.timeout` in milliseconds;
- `x-envoy-internal: true` and `x-envoy-expected-rq-timeout-ms`, which holds `auth.timeout` in milliseconds too: the
  two headers Envoy's own client for the check adds (measured on Envoy 1.40.2 with a `timeout` of `1500ms`: `true` and
  `1500`), so a check service reads the same names behind either proxy. A client's own copies of both are removed
  before the check is asked, and never reach the service;
- with `send_request: true`, `x-forwarded-uri`, the path and query after the file's normalisation and before a rewrite,
  as the Envoy output sends it, and `x-forwarded-method`, `x-forwarded-proto` and `x-forwarded-host`. The client's own
  copies of those four are dropped, and a client's own `x-gateway-timeout-ms` is never sent.

A value that holds CR, LF or NUL is answered `400`: HAProxy's parser refuses such a request first, and the script checks
again.

**The RPC question.** For each method with an `action` or `reports`, the file sets `x-gateway-action` and
`x-gateway-reports` (the names joined by commas) on a request for that method's path, before the check, and removes
both right after it, so a service sees neither. A method with neither gets no line, and a client's own copies of both
are removed first.

### What the service receives

When the check answers `200` and the script reads the answer whole, the request goes on with each header an `accept`
name covers, set in the order of the check's answer; a name the check sent twice keeps its last value, as in Envoy. The
script reads each header back after it sets it: HAProxy drops a header that does not fit in its request buffer without
an error, so a request whose accepted header did not fit is answered `403`, and the service never receives it without
the identity.

A request the accepted headers take past 100 header fields is answered `500` with no body, a gRPC call `200` with
`grpc-status` 2, each with an admitted origin's CORS answer, as Envoy 1.40.2 answers it. The script counts the fields as
Envoy counts the request it passes on: each header line the request holds once the accepted headers are set, Host as
`:authority`, with `:method`, `:path` and `:scheme`. By then the file has removed `Connection` and a client's own copies
of the headers it sets, and joined the lines of each header Envoy joins, as Envoy has before it counts. The
`x-request-id` and `x-forwarded-proto` the file sets count, and with `client_address: connection` the `x-forwarded-for`
and `x-envoy-external-address` too. A chunked request's `Transfer-Encoding` line is not counted, and over HTTP/2 a
`host` field beside `:authority` is one line, as for Envoy (measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2,
with 100 and 101 fields). A `te` or a `transfer-encoding` the check's answer holds under an `accept` name counts as one
field each, though the script sets neither, as Envoy sets each and counts it; a `host` takes the place of `:authority`
and adds none (measured). The script counts only when the check's answer held a header an `accept` name covers other
than `keep-alive`, as Envoy does: a `200` with none, or with `keep-alive` alone, is served up to the 100 header lines
HAProxy reads, and one with `host` alone is counted (measured).

The check's answer never sets `content-length`, `transfer-encoding`, `host`, `te`, `connection`, `upgrade` or
`keep-alive` on the request; naming one of the first six in full is refused, and Envoy never sets `keep-alive` either.
HAProxy frames or routes a request by the first six, so a check service can never change how the request it passes on
is framed.

The file removes a client's own copies of the `accept` names, of the two question headers and of
`x-envoy-auth-failure-mode-allowed` before any rule reads them, so a client cannot forge them. With
`when_unavailable: allow` and no answer from the check, the request goes on with `x-envoy-auth-failure-mode-allowed:
true` and none of the accepted headers.

### What the client receives when the check refuses

Any status but `200` from the check is a refusal. The client receives the check's status, a `2xx` other than `200` as
`403`, with every header of the answer but `content-length`, `transfer-encoding`, `connection`, `keep-alive`, `upgrade`
and `proxy-connection`, and the check's body, with `content-type: text/plain` when the check sent a body and no type.
The Envoy output passes `te`, `trailer` and a header the check's `Connection` names, and so does the file. A refusal to a
request from a listed origin carries that origin's CORS answer, `Access-Control-Allow-Origin` and, when
`cors.expose_headers` is written, `Access-Control-Expose-Headers`, as the Envoy output's does (measured on HAProxy
3.2.26 and 3.4.7 beside Envoy 1.40.2).

A request whose `content-type` is `application/grpc` alone, or `application/grpc` followed by `+`, compared as written,
is a gRPC call, and its refusal is answered in gRPC's form, where a plain HTTP `401` reaches a gRPC client as a
transport error: `200` with
`content-type: application/grpc`, `grpc-status` from the check's status (400 gives 13, 401 gives 16, 403 gives 7, 404
gives 12, 429, 502, 503 and 504 give 14, any other gives 2), the check's other headers, and the check's body in
`grpc-message`, percent-encoded: every byte outside printable ASCII and `%` is written `%XX`, so a body of
`no entry: 100% sure é`, a line feed and `next line` reaches the client as `no entry: 100%25 sure %C3%A9%0Anext line`.

HAProxy writes `content-length: 0` on a `304` and on an HTTP/2 gRPC refusal, where Envoy writes none, and the file
removes it there; over HTTP/1.1 both write `content-length: 0`.

### How the answer is read

The script passes a request only on a `200` it reads whole as RFC 9112 writes it: a status line of `HTTP/1.0` or
`HTTP/1.1`, one space, three digits and then nothing or a space and a reason; header lines of a token name, a colon and
a value with no control byte but a tab; at most one `Content-Length`, digits only and never beside `Transfer-Encoding`;
and a `Transfer-Encoding` of `chunked` alone, each chunk size plain hexadecimal on a line that ends in CR LF, where
Envoy 1.40.2 reads a bare LF at the end of every other line. It skips `1xx` answers before the final
one and joins a folded header line to the value before it by one space, where Envoy keeps the spaces and tabs of both
lines. It reads each head of the answer, from the status
line to the empty line with their line ends, a `1xx` head included, and the trailer section, to 81,920 bytes, the
most Envoy 1.40.2 reads (measured to the byte). An answer it cannot read that way is not a pass: it is
answered `403` under `deny`, as Envoy answers every such form measured. Under `allow` it depends on the answer: the
six cases below go on with the mark, and the note on `auth.when_unavailable`
[below](#where-haproxy-answers-the-check-otherwise-than-the-envoy-output) lists the others, which HAProxy answers `403`
where Envoy serves the request with the unchecked mark.

A check counts as unanswered, as it does for Envoy 1.40.2, in these six cases:

- HAProxy cannot connect to it;
- no byte of its answer comes before it closes or before `auth.timeout` passes;
- its status line is a well-formed `5xx`;
- a head of its answer has a 101st header line, folded lines not counted, and no name on two of its lines: Envoy reads
  at most 100. With a name on two lines HAProxy answers `403`, because Envoy counts a name it keeps inline once however
  many lines carry it (measured with `date`, `via`, `content-type`, `server`, `location` and `cache-control`) and then
  reads such an answer, where it counts `set-cookie`, `www-authenticate` and a name of its own a line each;
- the body of its answer breaks off: the check closes before the bytes that its `Content-Length` or a chunk's size
  promised, or `auth.timeout` passes while the body is read;
- its answer is read whole, but only after `auth.timeout` has passed.

`when_unavailable` decides. With `allow`, the request goes on with the mark. With `deny`, HAProxy answers an unanswered
check as Envoy does: `403`, with the body `Invalid CORS request` when the request carries an `Origin`, and for a gRPC
call `200` with `grpc-status` 7 and, with an `Origin`, `grpc-message` `Invalid CORS request`.

Every other answer the script cannot read whole is not an unanswered check: a header line, a chunk-size line or a
trailer line that breaks off, an answer that RFC 9112 does not write and an answer past the script's size bounds are
answered `403` under `allow` too. The notes on `auth.ask` and `auth.when_unavailable` say what Envoy does with each.

### The relay

The script's own connect is not bounded by its socket's timeout: to an address that never answered, it took 303 s on
HAProxy 3.4.7 and 6.0 s on 3.2.26 against a timeout of 1.5 s. So it connects to the relay, a `listen` section in `mode
tcp` with a `defaults` section of its own. For the example above:

```
# the relay to the check service
defaults
  mode tcp

listen check
  bind abns@proxyfile-8000-check
  timeout connect 2000ms
  timeout client 2000ms
  timeout server 2000ms
  retries 0
  option nolinger
  server accounts1 10.0.0.7:7070
```

- The three timeouts are `auth.timeout`, so a connect that does not come is cut off by it.
- `retries 0`: the relay tries no other server when one refuses, as the Envoy output's check cluster tries no other
  backend. With two addresses and one refusing, the checks that reach it fail on every proxy (measured on Envoy 1.40.2,
  HAProxy 3.2.26 and 3.4.7 with `when_unavailable: deny`).
- `option nolinger`: without it the relay kept reading a check that went on sending after HAProxy had its answer, for as
  long as the check sent.
- One `server` line for each address of the check service, taken in turn (round robin).
- The second `defaults` section keeps the HTTP options of the first off the relay: without it HAProxy warns that two of
  them are ignored.

HAProxy opens one connection to the check for each check, and the check closes it after its answer, where Envoy keeps
a pool of connections to the check. Held for 120 s at 1,000 and at 2,000 checked requests a second over 50 client
connections (one run of each proxy alone, Envoy 1.40.2, HAProxy 3.2.26 and 3.4.7, on an Apple M5 Max with Docker 29.9.3
and 12 CPUs), every request was answered `200`, and the check and the service each counted as many requests as were
sent, 120,000 and 240,000. The median at 2,000 a second was 0.43 ms on Envoy, 0.61 ms on 3.2.26 and 0.58 ms on 3.4.7,
and the 99th percentile 0.80, 1.25 and 1.08 ms.

### The deadline and the pass rule

A socket's own timeout counts only the time without data, so a check that sent one byte every 100 ms was read for 10.1 s
against a timeout of 1.5 s (measured on HAProxy 3.2.26 and 3.4.7, with the task below switched off). So a task of each
thread, started when the script loads, closes every socket whose deadline has passed, and `auth.timeout` bounds the
whole check, as Envoy's does. An answer that ends after the deadline is never used. The task waits on a queue while no
check waits (`core.queue` and its `pop_wait`, which the Lua API of HAProxy 3.2 and 3.4 both have) and looks every 10 ms
while one does. A pass of the task that fails ends alone, with a warning in HAProxy's log, and the task looks again 10
ms later: with its first three passes made to fail, each thread's task still closed a check that answered after 3 s at
the timeout of 1.5 s (measured on HAProxy 3.2.26 and 3.4.7).

A runtime error in a Lua action lets HAProxy 3.2.26 and 3.4.7 go on with the next rule, and on 3.4.7 a client that
closes its side right after its request aborts the action; either would reach the service unchecked. So the script sets
`txn.pf_passed` only when the request may go on, and the rules right after the action answer every asked request that
does not have it with the refusal an unanswered check gets under `deny`
([How the answer is read](#how-the-answer-is-read)).

### With the check switched off

With `auth.enabled: false` the file writes no check call, no relay and no question line, and still removes a client's
own copies of the `accept` names, the question headers and `x-envoy-auth-failure-mode-allowed`. A denying token and a
forged `x-user-id`, `x-gateway-action` and mark reached the service as `200` with none of the three, and the check was
not asked, as through the Envoy output (measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2).

### Reloading with checks in flight

A reload, `SIGUSR2` to HAProxy's master, while checks are in flight loses none and answers none for another request.
On HAProxy 3.2.26 and 3.4.7 (one run each, on an Apple M5 Max with Docker 29.9.3 and 12 CPUs) 2,400 checked requests
were sent over 60 s on 20 connections, each to its own path, to a check that answered `allow` after 500 ms. The load
was 40 requests a second, which is all that 20 connections carry against a check that answers after 500 ms, so about 20
checks were in flight at each reload. The master was reloaded at 10, 20 and 30 s. Every request was answered `200`, the check counted 2,400
checks and the service 2,400 requests, and the `x-forwarded-uri` the check had been told was the request's own path in
all 2,400. Each of the three reloads on each version printed `Loading success.`, and each former worker that the log
reaches exited with code 0 after relaying 400 checks (the log of 3.4.7 that was kept ends before the third former
worker's exit line).

### Where HAProxy answers the check otherwise than the Envoy output

Each difference below is declared on HAProxy's row of its key in
[Portability](portability.md#support-by-proxy-from-the-generators), and one alone is on
[the list of ways HAProxy is less strict](#where-haproxy-is-less-strict-than-the-envoy-output): under `forwarded` the
check is told an `x-forwarded-proto` that is neither `http` nor `https` as it arrived, where the Envoy output tells it
the connection's scheme, so a check that decides by the scheme it is told reads the word of the client or of the proxy
in front. In every case measured
on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2, the service received a request the check was asked about only after the
check let it through; each case counted the requests the service and the check received.

<!-- keys: notes: haproxy: auth.ask, auth.only_with_header, auth.send, auth.when_unavailable, auth.send_request, auth.accept, remove_from_clients, origins.<environment name>.origins, origins.<environment name>.patterns, client_address -->
- **`auth.ask`**: an answer of the check HAProxy cannot read whole is answered 403, where Envoy passes it on: a denial whose header lines and body pass 15,100 bytes, each line counted as its name, its value and 8 bytes, a header line of 16,383 bytes or more, a 200 whose body passes 1 MiB, a head of more than 100 header lines, folded lines not counted, with a name on two of them, where Envoy counts a name it keeps inline, such as date, once, and a status of 101, which a gRPC call gets as grpc-status 7, where Envoy gives 2; an answer in HTTP/1.2, a chunk size of 9 hexadecimal digits or more, a chunk extension or a space or a tab after a chunk size, CR CR LF after a chunk's data or a trailer value, a folded trailer line, two spaces or a tab after the version, a lone CR in the status line or in a header value, a status line of 16,383 bytes or more, or a line other than a chunk-size line whose CR falls at a multiple of 16,384 bytes into the answer (measured at 16,384, 32,768 and 81,920), which HAProxy's socket hands to the script with the CR kept, is answered 403, where Envoy reads it; each head of the answer and its trailer section are read to 81,920 bytes, Envoy's bound, each line counted with a CR and an LF, so lines that end in LF alone reach it sooner; a status outside 100 to 599 or a status line in HTTP/2.0 is answered 403, where Envoy passes the check's status on; the check is sent connection: close on a connection of its own, where Envoy sends no Connection header and keeps its connections to the check open; after a check's 304 HAProxy closes the client's connection, so a request the client sends next on it gets no answer, where Envoy answers it.
- **`auth.only_with_header`**: over HTTP/2 a request that carries the header with every value empty is asked, where Envoy does not ask it: HAProxy reads an HTTP/2 value of spaces alone as empty, and Envoy asks one, so HAProxy asks both.
- **`auth.send`**: the check receives each byte of 0x80 or more that is not part of well-formed UTF-8 in a header it is sent as the client sent it, where Envoy writes a ! for each such byte.
- **`auth.when_unavailable`**: with allow, an answer that breaks off inside its header lines, a chunk-size line or a trailer line, by a close or by passing auth.timeout, a 200 or a denial whose body never ends, an answer whose head or trailer section passes 81,920 bytes, a head of more than 100 header lines with a name on two of them, such as set-cookie, and an answer HAProxy cannot read as RFC 9112 writes one (a Content-Length that is not one line of digits, a control byte in a header value, a status line that does not end, a chunk-size line that ends in a bare LF) are answered 403, where Envoy serves the request with the unchecked mark, x-envoy-auth-failure-mode-allowed: true.
- **`auth.send_request`**: under client\_address forwarded, over HTTP/2 a request whose :scheme differs from its connection's scheme is told in x-forwarded-proto the x-forwarded-proto it arrived with, or the connection's when it arrived with none, where Envoy tells it the :scheme, and an x-forwarded-proto that is neither http nor https, such as ftp, is told as it arrived, where Envoy tells the connection's scheme.
- **`auth.accept`**: an accepted header that does not fit beside the request's own header lines in HAProxy's request buffer is answered 403, where Envoy sets it on the request (measured up to 50,000 bytes; it answered 500 at 70,000): beside a request of 118 bytes, one of 8,000 or 15,000 bytes is set by both versions and one of 15,843 bytes or more (3.4.7: 15,851) is answered 403, a bound the request's own header lines lower by as many bytes as they hold; an accepted header whose line reaches 16,383 bytes is answered 403 by auth.ask's line bound first; a \* prefix never sets transfer-encoding, host or te from the check's answer, where Envoy sets each; a folded header line of the check's answer reaches the service joined to the value before it by one space, where Envoy keeps the spaces and tabs of both lines and drops the line end. "auth.accept" naming content-length, transfer-encoding, host, te, connection or upgrade in full is refused by --target haproxy: HAProxy frames or routes a request by these headers, so the check's answer never sets them; remove the name from auth.accept. "auth.accept" holding a \* prefix that covers content-length, connection or upgrade is refused by --target haproxy: the Envoy output sends the body chunked once Content-Length is removed, and leaves a WebSocket upgrade unanswered once Connection or Upgrade is removed, which HAProxy cannot write; write the other names in full, or a longer prefix, or render it for envoy.
- **`remove_from_clients`**: names and \* prefixes that cover none of content-length, connection and upgrade. "remove\_from\_clients" holding content-length, connection or upgrade, in full or under a \* prefix is refused by --target haproxy: the Envoy output sends the body chunked once Content-Length is removed, and leaves a WebSocket upgrade unanswered once Connection or Upgrade is removed, which HAProxy cannot write; write the other names in full, or a longer prefix, or render it for envoy.
- **`origins.<environment name>.origins` and `origins.<environment name>.patterns`**: an admitted Origin longer than 872 bytes less the length of cors.expose\_headers is answered 403 with the body Invalid CORS request before the check and the service, where the Envoy output echoes it: HAProxy has 1,024 bytes for the lines it adds to an answer.
- **`client_address`**: under forwarded, a client's own X-Request-Id longer than 980 bytes, or than 908 bytes less an echoed Origin and cors.expose\_headers, is answered 400 before the check and the service, where the Envoy output serves it: HAProxy echoes the id in the 1,024 bytes it keeps for the lines it adds to an answer; over HTTP/2 an X-Forwarded-Proto of spaces alone is answered 404, where the Envoy output serves it; under connection, a client on a loopback address is appended to X-Forwarded-For as that address, where the Envoy output appends its own address, as Envoy does for every loopback peer (measured with a client at 127.0.0.1 on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2).
<!-- /keys -->
- **A dot segment with a `;` parameter answered `400`**: a dot segment whose ; parameter is written with dots, such as /a/..;/b, is answered 400, and a gRPC call 200 with grpc-status 13, where Envoy 1.40 drops the parameter and resolves the segment; with the auth check, such a request is asked about whatever route its resolved path takes, and gets the check's denial or, once the check lets it through, 400, where Envoy asks only when that route is checked and, with send_request, sends the check the resolved path.

A client that sends its request and closes its own side never gets the service's answer, on Envoy or on HAProxy. This is
not declared on a key: every file holds `option abortonclose`, with which HAProxy answers such a client `400` or `403`,
or nothing, where Envoy only closes. A request the check is asked about reaches no service, and a request it is not asked
about may still reach the service, as through Envoy (measured with and without the check on HAProxy 3.2.26 and 3.4.7
beside Envoy 1.40.2). It is not on the list of ways HAProxy is less strict either.

### What the check costs

A checked request costs more on HAProxy than on Envoy, which has the check as a filter in the proxy. These figures were
measured on an Apple M5 Max (arm64) with Docker 29.9.3 (12 CPUs, 34 GB), on Envoy 1.40.2
(`envoyproxy/envoy:distroless-v1.40.2`), HAProxy 3.2.26 and HAProxy 3.4.7 (`haproxy:3.2.26` and `haproxy:3.4.7`). One
file, with one check service, `when_unavailable: deny` and `only_with_header`, was rendered for Envoy and for HAProxy.
The check and the service were one throwaway Go stub each, the load came from a container on CPUs 8 to 11, and the
proxies were not pinned. "Alone" is 2,000 requests over one connection and "under load" is 20,000 over 50. CPU is the
proxy container's own, for each request, from Docker's API.

Each range of the first table is four runs, and each other range two where it does not say. A run started only while no
other container used more than 5% of a CPU and no process on the host more than 40%, and was run again when it ended
busy; a round taken while the machine was busy was slower for every proxy, Envoy included, and is left out. A few runs
give a range and no more, and the figures move with what the machine is doing, so measure your own file and your own
check before you rely on them. A machine with fewer CPUs was not measured.

| | Envoy 1.40.2 | HAProxy 3.2.26 | HAProxy 3.4.7 |
|---|---|---|---|
| alone, checked, median | 0.24 to 0.37 ms | 0.37 to 0.60 ms | 0.37 to 0.55 ms |
| alone, checked, 99th percentile | 0.61 to 0.73 ms | 0.69 to 1.15 ms | 0.68 to 0.94 ms |
| alone, CPU a checked request (unchecked in brackets) | 108 to 170 µs (65 to 106) | 366 to 578 µs (93 to 171) | 365 to 526 µs (101 to 149) |
| under load, checked, requests a second | 24,260 to 29,884 | 10,908 to 18,920 | 12,035 to 18,603 |
| under load, checked, median | 1.44 to 1.82 ms | 2.38 to 4.12 ms | 2.40 to 3.81 ms |
| under load, checked, 99th percentile | 4.39 to 5.51 ms | 6.45 to 11.47 ms | 6.95 to 9.73 ms |
| under load, CPU a checked request | 107 to 132 µs | 235 to 395 µs | 236 to 383 µs |
| the check service's CPU a check, under load | 49 to 62 µs | 77 to 127 µs | 79 to 122 µs |
| memory over 20,000 checked requests, at most | +4.2 to +4.7 MiB | +3.1 to +8.4 MiB | +3.3 to +7.9 MiB |

**Where HAProxy is slower.** Under load HAProxy served fewer checked requests a second than Envoy in every run: its
best run, 18,920, is below Envoy's worst, 24,260. A checked request had a higher median latency and cost more CPU: a
median of 2.38 to 4.12 ms under load against Envoy's 1.44 to 1.82 ms, and 235 to 395 µs of CPU against 107 to 132 µs.
Alone, the median is 0.37 to 0.60 ms against 0.24 to 0.37 ms. The check service itself spent more CPU on each check
behind HAProxy, 77 to 127 µs against 49 to 62 µs. HAProxy opens a connection for each check, and Envoy keeps a pool.

**Connections and ports** (20,000 checked requests over 50 client connections; the open and `TIME_WAIT` counts are one
run, the source ports three). Envoy opened 50 connections to the check, the check saw 50 source ports for the 20,000 checks, and the 50
connections were still open right after and 65 s later, with none in `TIME_WAIT`. HAProxy used 11,249 to 11,330
distinct source ports on 3.2.26 and 11,214 to 11,316 on 3.4.7, none was left on HAProxy's side (`option nolinger` resets
it), and 11,124 (3.2.26) and 10,990 (3.4.7) were in `TIME_WAIT` on the check's side right after, none 65 s later.

**A check that misbehaves** (two runs each, each proxy restarted before each kind; while the flood ran, 300 unchecked
requests on one connection measured whether other requests were held). Every flood request was answered `403` and every
unchecked request meanwhile `200`, and no proxy exited or restarted. Each cell gives the median time of the flood's
answers, or for the last two rows their rate; the 99th percentile of the unchecked requests meanwhile; and the memory
the proxy gained:

| The check | Envoy 1.40.2 | HAProxy 3.2.26 | HAProxy 3.4.7 |
|---|---|---|---|
| answers after 3 s, 4,000 requests over 1,000 connections | 1.50 s; 0.65 to 0.68 ms; +70 to +71 MiB | 1.50 s; 0.59 to 0.69 ms; +51 to +53 MiB | 1.50 s; 0.58 to 0.83 ms; +51 to +53 MiB |
| never answers, 2,000 over 1,000 | 1.50 to 1.51 s; 0.55 to 0.76 ms; +70 to +72 MiB | 1.50 to 1.51 s; 0.61 to 0.70 ms; +45 to +46 MiB | 1.50 to 1.51 s; 0.64 to 0.65 ms; +46 to +50 MiB |
| closes at once, 20,000 over 50 | 26,801 to 30,044 a second; 0.65 to 0.71 ms; +5.2 to +5.7 MiB | 20,604 to 21,822 a second; 0.98 to 2.07 ms; +9.6 to +9.8 MiB | 22,849 to 23,052 a second; 0.66 to 0.68 ms; +10.3 to +10.8 MiB |
| answers `200` and sends without end, 1,000 over 50 | 366 to 383 a second; 24.24 to 31.28 ms; +1,081 to +1,129 MiB | 3,302 to 3,451 a second; 0.86 to 1.04 ms; +20.1 to +25.4 MiB | 3,448 to 3,958 a second; 0.59 to 0.76 ms; +17.9 to +21.5 MiB |

The other rounds gave the same statuses and the same 1.50 to 1.51 s medians, and their rates and memory moved with the
machine: over three runs, one of which ended with a busy container, Envoy's rate under the endless `200` ranged from
277 to 565 a second and its memory from +1,100 to +1,217 MiB.

**Where HAProxy holds less.** After the last flood, the endless `200`, both HAProxy versions were at 0% of a CPU from
the first half second to the end of the 10 s they were watched, and held 92 MiB (3.2.26) and 101 MiB (3.4.7), flat.
Envoy was at 0 to 2% and kept 775 to 790 MiB (701 to 771 MiB in earlier rounds). Idle, with no request for 10 s, Envoy
used 0.5 to 0.6% of one CPU and HAProxy 0.0% on both versions, with the check and without it. HAProxy holds more
memory idle (two runs): Envoy 22.4 to 23.2 MiB; 3.2.26 80.4 to 81.6 MiB with the check and 81.0 to 83.1 MiB without it;
3.4.7 88.5 to 89.6 MiB with it and 91.2 MiB without it.

## The path a route is matched on

The file normalises the path before any rule reads it, as the Envoy output does: the script escapes what Envoy escapes,
`%2F`, `%5C` and a backslash are read as `/`, an escaped letter, digit, `-`, `.`, `_` or `~` is decoded, dot segments
are resolved and then repeated slashes merged, so a `..` removes an empty segment before it, as through Envoy
(`/catalog//../products/1` is `/catalog/products/1`), and the route and the service read that path. Every escape it does
not decode reaches the service as written, in the case of its hex digits, and so does an invalid one such as `%zz`;
`%00` in a path is answered `400`, as by Envoy. A path that still holds a dot segment with a `;` parameter, the `;`
written or escaped once, is answered `400` once its route, its upgrade and its origin are checked, as by Envoy, whose
file refuses it by a route of its own (below). Measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2; two differences
stay, each named on HAProxy's row of it in [Portability](portability.md#support-by-proxy-from-the-generators):

- **A dot segment whose `;` parameter is written with dots, such as `/a/..;/b`, is answered `400`**, where Envoy 1.40
  drops the parameter and resolves the segment: HAProxy's `path-strip-dot` and `path-strip-dotdot` do not take `..;`
  or `.;` as a dot segment, so the rule above refuses the path. With the auth check, such a request is asked about
  whatever route its resolved path takes, and gets the check's denial or, once the check lets it through, `400`, where
  Envoy asks only when that route is checked and, with `send_request`, sends the check the resolved path. After the
  `400` of such a segment with the `;` written `%3B`, which both answer, HAProxy keeps the connection open, where Envoy
  closes it.
- **An escape of an unreserved character in the query, such as `?q=%41` or `?q=%7e`, reaches the service and the
  check decoded**, as `?q=A` and `?q=~`, with or without a dot segment in the path, where Envoy passes the query as
  the client wrote it; an escape of a reserved character, such as `%2F`, `%26` or `%3d`, reaches both as written, as
  through Envoy.

## The request line and the headers

Before any other rule the file answers what Envoy refuses and HAProxy's own parsers pass: every HTTP/1 version but 1.1
with `426`, from `HTTP/0.0` to `HTTP/9.9`, and with `400` a method Envoy's HTTP/1 parser does not know (a known one in
another letter case too), a `QUERY` with no `Content-Type` or with none but empty lines, over HTTP/1.1 and HTTP/2, which
Envoy 1.40.2 answers `400` before it picks a route and so before the `404` of an unknown host, the origin check and the
auth check, and over HTTP/2 a request past Envoy's 100 header fields. A `QUERY` with a `Content-Type` is served, and
over HTTP/2 so is `query` in another letter case, as by Envoy. It removes the headers Envoy removes: `Keep-Alive`,
`Proxy-Connection`, `X-Forwarded-Client-Cert`, the 16 `x-envoy-` headers that steer an Envoy, `Connection` but a
WebSocket upgrade's, and a request's trailers. From a service's answer it removes `Keep-Alive`, which HAProxy would pass
on to an HTTP/1.1 client and Envoy does not. It sends `TE: trailers` in any letter case on when `Connection` names `te`,
as Envoy does. HAProxy counts the request line among its 101 header lines, so 100 header lines are served and 101
refused, as by Envoy.

A header value that holds a byte from 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F is refused before any other rule, as
Envoy refuses it: with `400` over HTTP/1.1, and by resetting the stream over HTTP/2, where Envoy closes the connection.
HAProxy's own parser refuses 0x00, a line feed and a carriage return in a header or a trailer value, with `400` over
HTTP/1.1 and a reset stream over HTTP/2. No fetch reads a request trailer, so a trailer value that holds a control byte
is served, an entry of [the list below](#where-haproxy-is-less-strict-than-the-envoy-output).

The file removes the headers `remove_from_clients` names, and the names `auth.accept` lists, from every request before any
other rule but that refusal. A prefix leaves `Host` and `Transfer-Encoding`, and `Transfer-Encoding` written in full is
left, as Envoy leaves them; what HAProxy cannot remove as Envoy does is refused at render
([the differences of the auth check](#where-haproxy-answers-the-check-otherwise-than-the-envoy-output)).

HAProxy answers `Expect: 100-continue` itself, at once, as Envoy does, and passes the header to no service: for a request
with a body it sends `100 Continue` before it routes the request or asks the check, and lets the request go on at the
body's first byte (`http-request wait-for-body ... at-least 1`), so an upload takes the time and memory it takes without
the header (measured on Envoy 1.40.2 beside HAProxy 3.2.26 and 3.4.7, with an upload of 128 MiB).

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
  folded or not. Over HTTP/2 HAProxy keeps the spaces and tabs around a `cookie` field's value, where it drops those
  around any other field's, so a `cookie` field that starts with two of them, as one of spaces alone does, or ends with
  three is answered `400` too, where Envoy serves it.
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
- **A request line and header lines of more than 15,277 bytes together are answered `431`, and a request line of 16,319
  bytes or more `414`**, counted as sent with their line ends, with Host alone beside the request line and 4 bytes fewer
  for each further header line (HAProxy 3.4.7: 8 bytes more for each bound), where Envoy serves both up to its default
  limit of 60 KiB. Over HTTP/2, a path whose escaped form outgrows HAProxy's buffer, such as one of 7,000 bytes above
  0x7F, is answered `400` by HAProxy 3.4.7 and `500` by 3.2.26, where Envoy serves it.
- **A request line with no version, HTTP/0.9's own form, is answered `400`**, where Envoy answers `426`.
- **Over HTTP/2 a `QUERY` whose `content-type` field is spaces alone is answered `400`**, where Envoy serves it:
  HAProxy reads an HTTP/2 value of spaces alone as empty, as on `auth.only_with_header`, so the rule that answers `400`
  to a `QUERY` with no `Content-Type` or an empty one, as Envoy does, answers this one too.
- **Over HTTP/2, a header or trailer name outside RFC 9110's token characters or a `#` in `:path` resets the stream**,
  where Envoy closes the connection or answers `400`, and a request with neither `:authority` nor `host` is answered
  `404`, where Envoy closes the connection.
- **Over HTTP/1.1, a request trailer whose name is outside those characters ends the connection with no answer** when
  the body comes after the headers, where Envoy answers `400`.

## Where HAProxy is less strict than the Envoy output

These differences are not stricter than the Envoy output: in each, HAProxy serves a request the Envoy output refuses or
ends, or sends a service what the Envoy output would not. HAProxy's parsers decide each of the request line's before
any rule of the file reads the request, its timeout counts the time without data, and no fetch reads a request trailer,
which `option http-drop-request-trailers` removes before the service, so the file cannot close them. A header value that
holds a control byte is not among them: the file refuses it with a rule, as Envoy does, and the entry on a trailer's
value is the one that stays. The admin listener's is HAProxy's `monitor-uri`, which answers an absolute target by the
path it names. The last is the file's own: under `client_address: forwarded` it keeps the `X-Forwarded-Proto` a
request arrived with, as the Envoy output keeps it for the service, and tells the check that header.
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
- **a trailer value holding a control byte is served**: A request trailer value holding one of the bytes 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F is served, the service receiving the body and no trailer, where Envoy answers `400` over HTTP/1.1 and closes the connection over HTTP/2. A header value holding one is answered `400` over HTTP/1.1, as by Envoy, and its stream is reset over HTTP/2, where Envoy closes the connection; HAProxy refuses 0x00, a line feed and a carriage return in either value, with `400` over HTTP/1.1 and a reset stream over HTTP/2.
- **the admin listener serves an absolute target of any scheme as the path it names**: `GET http://localhost/ready HTTP/1.1` with `Host: localhost`, sent to the admin listener, is answered `200` with HAProxy's page `Service ready.`, and so is the same target with `https://`, `HTTP://`, `ftp://` or `ws://`, or with user information, `http://u@localhost/ready`, where the Envoy output's admin listener answers `404` with no body to every absolute target. `http://localhost/nothing` and `http://localhost/ready?x=1` are answered `404` by both, and a target whose host or port is not the Host header's, such as `http://other.example/ready` or `http://localhost:9901/ready`, `400`, where the Envoy output answers `404`.
- **without timeouts.request, an answer that keeps arriving runs past 15 s**: Without `timeouts.request`, a service that keeps sending runs past 15 s, where the Envoy output ends a request 15 s after it starts: HAProxy's `timeout server` counts the time without data (on HAProxy 3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served).
- **under forwarded, an x-forwarded-proto that is neither http nor https is told to the check as it arrived**: With `client_address: forwarded` and `auth.send_request: true`, a request sent over plain HTTP with `X-Forwarded-Proto: ftp`, such as `GET /products/1 HTTP/1.1`, is told `x-forwarded-proto: ftp` in the request to the check, where the Envoy output tells the check `http`, the connection's scheme. Both send the service `X-Forwarded-Proto: ftp`. Under `connection`, over TLS, both tell the check and the service `https`, the connection's scheme.
<!-- /keys -->

Two ways [nginx's list](nginx.md#where-nginx-is-less-strict-than-the-envoy-output) names do not hold here (measured on
HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2). Under `client_address: connection` an absolute `https` target sent over
plain HTTP, `GET https://localhost/products/1 HTTP/1.1`, is answered `400` and asks no check, as the public listener
answers every absolute target over HTTP/1.1, where the Envoy output answers `403`. A header line sent over HTTP/1.1 as
`X-A:`, a tab and `b5`, or as `X-B: b6` and a tab, reaches the service as `x-a: b5` and `x-b: b6`, as through the Envoy
output, under `forwarded` and under `connection`.

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

An origin an environment admits but that is too long for the file to echo is answered `403`, before the check and the
service ([the differences of the auth check](#where-haproxy-answers-the-check-otherwise-than-the-envoy-output)).

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

Under `forwarded`, a request is answered `404` when its `X-Forwarded-Proto` is empty, comes as two empty lines or, over
HTTP/1.1, is spaces alone, before the file reads the request's origin, preflight, dot segment or upgrade, as Envoy does.
HAProxy's HTTP/2 decoder drops the spaces of a value, so over HTTP/2 spaces alone are answered `404` too, where Envoy
serves them. A client's own
`X-Request-Id` that is too long for the room HAProxy keeps for the lines it adds to an answer is answered `400` before the
check and the service
([the differences of the auth check](#where-haproxy-answers-the-check-otherwise-than-the-envoy-output)).

## Limits and timeouts

- `limits.connections` is HAProxy's own [`maxconn`](https://docs.haproxy.org/3.4/configuration.html#4.2-maxconn) on the
  listener and on each server line of a service: a connection past the listener's waits in the listen queue, and a
  request past a server's waits in HAProxy's queue for up to 5 s, `timeout connect` when
  [`timeout queue`](https://docs.haproxy.org/3.4/configuration.html#4.2-timeout%20queue) is not written, and is then
  answered `503`.
- `limits.request_headers` is HAProxy's own
  [`tune.bufsize`](https://docs.haproxy.org/3.4/configuration.html#3.2-tune.bufsize), the size and the 1,024 bytes
  HAProxy keeps for rewrites, and
  [`tune.http.maxhdr`](https://docs.haproxy.org/3.4/configuration.html#3.2-tune.http.maxhdr), the count and one for the
  request line; a request past either is answered `400`. A size below 15KiB is refused, as HTTP/2 needs a buffer of
  16,384 bytes, and a count above 32766, the most `tune.http.maxhdr` takes. Every buffer of every connection takes the
  size, so the memory each connection takes grows with it, and the manual asks for fewer connections when the buffers
  grow.
- `timeouts.idle` is HAProxy's own
  [`timeout client`](https://docs.haproxy.org/3.4/configuration.html#4.2-timeout%20client) and
  [`timeout server`](https://docs.haproxy.org/3.4/configuration.html#4.2-timeout%20server), the time without data on
  each side, the server's the shorter of it and the 15 s, or 5 minutes, written without the key; `none` is
  2147483647 ms, HAProxy's longest timer, and a longer time is refused.
- `timeouts.upstream_idle` writes no line: HAProxy opens a connection to a service for each request and closes it after
  the answer, so no connection to a service waits idle.

## Services found by name

- A service whose `address` is a DNS name is HAProxy's own [`server-template`](https://docs.haproxy.org/3.4/configuration.html#4.2-server-template) of 100
  servers, which the [`resolvers`](https://docs.haproxy.org/3.4/configuration.html#5.2-resolvers) section `proxyfile` fills with up to 100 of the name's
  addresses; each server starts down with no address ([`init-addr none`](https://docs.haproxy.org/3.4/configuration.html#5.2-init-addr)) until the
  resolvers answer.
- The section asks the name server `dns.resolver` names ([`nameserver`](https://docs.haproxy.org/3.4/configuration.html#5.3.2-nameserver)), or without the
  key the name servers of the host's `/etc/resolv.conf` ([`parse-resolv-conf`](https://docs.haproxy.org/3.4/configuration.html#5.3.2-parse-resolv-conf)),
  and looks each name up every second, HAProxy's default. It takes DNS answers of up to 4,096 bytes
  ([`accepted_payload_size`](https://docs.haproxy.org/3.4/configuration.html#5.3.2-accepted_payload_size)): with
  HAProxy's default size an answer held 30 IPv4 addresses of a name, and with 4,096 bytes a name of 100 was served by 99
  to 100 of them (measured on HAProxy 3.2.26 and 3.4.7).
- When the name server stops answering, HAProxy keeps the addresses it has for 30 s, its default
  ([`hold`](https://docs.haproxy.org/3.4/configuration.html#5.3.2-hold)), and then takes the name's servers down, so
  the service has no backend until the name server answers again.
- The servers take the addresses of one family, IPv6 when the name has one
  ([`resolve-prefer`](https://docs.haproxy.org/3.4/configuration.html#5.2-resolve-prefer)), where the Envoy output takes both; a file whose every service is
  at IP addresses writes no resolvers section.

## Balance and hash

Each backend of a service takes HAProxy's own [`balance`](https://docs.haproxy.org/3.4/configuration.html#4.2-balance), and `hash-type` stays at HAProxy's
default, map-based:

| The file writes | HAProxy's own | Which backend takes a request |
|---|---|---|
| no `balance`, or `round_robin` | no line: `roundrobin`, HAProxy's default | the backends in turn |
| `least_requests` | `balance leastconn` | the one with the fewest connections, one a request here |
| `random` | `balance random(1)` | one drawn at random, one draw |
| `hash: {header: <name>}` | `balance hdr(<name>)` | the one the header's value hashes to; without the header, or with it empty, the backends in turn |
| `hash: {client: true}` | `balance source` under `client_address: connection`, `balance hash req.hdr_ip(x-forwarded-for,-1)` under `forwarded` | the one the client's address hashes to, the last `X-Forwarded-For` address under `forwarded`; with no such address, the backends in turn |

The check relay of the service `auth.ask` names takes `leastconn` and `random(1)` as its backends do, and goes round
robin for a hash: it is a TCP section whose one client is HAProxy itself.

## Health checks

- `health.path` is HAProxy's own [`option httpchk`](https://docs.haproxy.org/3.4/configuration.html#4.2-option%20httpchk) `GET <path>` in the service's http
  backend and in the check relay, sent over HTTP/1.0 with no Host header, with
  [`http-check expect`](https://docs.haproxy.org/3.4/configuration.html#4.2-http-check%20expect) `status 200-299`; each server takes
  [`check`](https://docs.haproxy.org/3.4/configuration.html#5.2-check) with [`inter`](https://docs.haproxy.org/3.4/configuration.html#5.2-inter), `fall` and `rise` from `interval`, `unhealthy` and
  `healthy`, and is in the pool from HAProxy's start until it fails its checks.
- `health.timeout` is HAProxy's own [`timeout check`](https://docs.haproxy.org/3.4/configuration.html#4.2-timeout%20check), the wait for the answer; the
  connection has the shorter of 5 s and the interval. An interval or a timeout above 2147483647 ms, HAProxy's longest
  timer, is refused.
- The gRPC backend of a service at IP addresses leaves each server out with the http server at its address
  ([`track`](https://docs.haproxy.org/3.4/configuration.html#5.2-track)); a service found by a DNS name keeps every gRPC backend, as a server-template tracks no
  server.
- `health.grpc` is refused: [`http-check send`](https://docs.haproxy.org/3.4/configuration.html#4.2-http-check%20send) writes a text body, not the health
  service's gRPC message, so HAProxy has no gRPC health check; write `health.path` on the service's http port.

## Retries

- A route's `retries` is HAProxy's own
  [`http-request set-retries`](https://docs.haproxy.org/3.4/configuration.html#4.4-set-retries) in the service's
  backend, for that route's requests alone; a count above 100, the most `set-retries` takes, is refused.
- HAProxy tries a request again when its connection to the service failed
  ([`retry-on`](https://docs.haproxy.org/3.4/configuration.html#4.2-retry-on) `conn-failure`, its default), with
  [`option redispatch`](https://docs.haproxy.org/3.4/configuration.html#4.2-option%20redispatch). Measured on HAProxy
  3.2.26 and 3.4.7 with one of two addresses refusing connections: a service that balances round robin or by
  `least_requests` sent the retry at once to the other address, and a hashed service sent every try to the address its
  hash picks. A service of one address is tried again after a pause of the shorter of `timeout connect` and 1 s
  ([`retries`](https://docs.haproxy.org/3.4/configuration.html#4.2-retries)).
- The rpc entry's `retries` is refused: with `retry-on` at its default, HAProxy did not send a gRPC call again after its
  connection to the service failed, and `retry-on all-retryable-errors`, which did, also sends again a request a
  service may have read.

## Rate limits

- A route's `rate_limit`, and the rpc entry's for all its routes, is HAProxy's own
  [`stick-table`](https://docs.haproxy.org/3.4/configuration.html#4.2-stick-table) `backend proxyfile_rate<n>`, which
  counts the route's requests over `per`
  ([`sc_http_req_rate`](https://docs.haproxy.org/3.4/configuration.html#7.3.3-sc_http_req_rate)), refused ones too, so
  a client that keeps sending stays refused.
- [`track-sc0`](https://docs.haproxy.org/3.4/configuration.html#4.4-track-sc0) keys each request by the client's
  address, the connection's peer or under `forwarded` the last `X-Forwarded-For` address, in a table of up to 100k
  clients that forgets its oldest when full, or with `by: all` by one key for every client. An IPv6 client is counted
  by its /64 network ([`ipmask(32,64)`](https://docs.haproxy.org/3.4/configuration.html#7.3.1-ipmask)), so a client
  that moves inside its own network keeps its count.
- A request past `requests`, a gRPC call too, gets HAProxy's own `429`
  ([`http-request deny`](https://docs.haproxy.org/3.4/configuration.html#4.4-deny)), after the origin is admitted and
  before the auth check.
- `burst` above 0 is refused, as HAProxy's rate counts the requests of a period and has no burst, and so is a `per`
  above 2147483647 ms, HAProxy's longest timer.

## The request log

- Every file logs each request of the public listener on standard output
  ([`log`](https://docs.haproxy.org/3.4/configuration.html#4.2-log) `stdout format raw daemon`), with no `log` key too,
  in HAProxy's own [`log-format`](https://docs.haproxy.org/3.4/configuration.html#4.2-log-format) of its own fields: the
  date, the frontend, the backend and server, the timers, the status, the bytes each way, the termination state, the
  method, the HTTP version and the request id, quoted and escaped, as under `forwarded` it is the client's own. The
  version is read from the first 1,024 bytes of the request line that HAProxy keeps for the log
  ([`tune.http.logurilen`](https://docs.haproxy.org/3.4/configuration.html#3.2-tune.http.logurilen)), so a longer
  request line is logged with `HTTP/0.9`.
- No line holds a path, a query, the client's address, a user agent or a referrer, as [Defaults](defaults.md) promises
  of every proxy: a connection that fails before its request, such as a failed TLS handshake, and a request HAProxy's
  parser refuses are logged by
  [`error-log-format`](https://docs.haproxy.org/3.4/configuration.html#4.2-error-log-format) with the same fields and
  HAProxy's error, where HAProxy's default line holds the client's address; a refused request's line holds its status,
  `400`, and `<BADREQ>` in place of its method and version.
- With `log`, [`set-log-level silent`](https://docs.haproxy.org/3.4/configuration.html#4.4-set-log-level) drops a
  request answered below `status_from` unless HAProxy's own
  [`rand(100)`](https://docs.haproxy.org/3.4/configuration.html#7.3.2-rand) draws below `sample`, a draw for each
  request.

## What HAProxy writes with no key

Each of these is in every file the HAProxy generator writes. Each was measured on HAProxy 3.2.26 and 3.4.7, and
Envoy's answers on Envoy 1.40.2, unless it says otherwise.

- **No request is tried again** (`retries 0`) but on a route whose `retries` asks ([Retries](#retries)): without the
  line, HAProxy 3.4.7 tried a connection the service refused three more times.
- **A service has 5 s to take the connection** (`timeout connect 5s`), Envoy's default connect timeout (Envoy
  answered a service that never took the connection after 5.01 s). A gRPC service has 15 s: HAProxy does not bound an
  HTTP/2 connection to a service by `timeout connect`, so one that never takes the connection is answered after the
  15 s a service that sends nothing gets (15.02 s), a gRPC call as below and any other request `504`, where Envoy's
  answer to it is an empty `502` (declared on `services.<service name>.grpc`).
- **5 minutes with no data from the client end a request** (`timeout client 300s`), Envoy's default stream idle
  timeout (read in each proxy's documentation, not run), when `timeouts.idle` is not written. **A service that sends nothing for 15 s is answered `504`**
  (`timeout server 15s`), at the same time as by the Envoy output, which ends a request 15 s after it starts when
  `timeouts.request` is not written (measured over HTTP/1.1 and HTTP/2: `504` after 15.01 s, Envoy's after 15.00 s).
  The `504` carries Envoy's body, `upstream request timeout` as `text/plain`, and Envoy's reason, `Gateway Timeout`,
  where HAProxy's own is an HTML page with the reason `Gateway Time-out`: the file writes the body from a variable the
  frontend sets for every request but a gRPC call, whose `504` has none, and the reason with `set-status`.
  HAProxy counts its limits in time without data, so a service that keeps sending runs past 15 s (measured on HAProxy
  3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served), the difference
  HAProxy's row of `timeouts.request` names. With `timeouts.request: none` the service has 5 minutes of silence
  (`timeout server 300s`), as with Envoy.
- **A service that refuses the connection is answered `502` with an empty body**, at once over HTTP/1.1 and HTTP/2,
  as by the Envoy output: HAProxy's own answer is a `503`, which the file writes with an empty body
  (`http-error status 503`) and turns into `502`. Over HTTP/1.1, a service that does not take the connection within
  the 5 s gets the same `502`. A service that closes the connection before its answer gets an empty `502` too
  (`http-error status 502`), where HAProxy's own is an HTML page. The `502` carries Envoy's reason, `Bad Gateway`,
  where HAProxy's `set-status` wrote `Bad Gateway or Proxy Error` over HTTP/1.1. Over HTTP/2 it carries
  `content-length: 0`, where Envoy's carries none; over HTTP/1.1 both carry it.
- **A gRPC call the file refuses before any service is answered as Envoy answers it**: `200` with
  `content-type: application/grpc` and the `grpc-status` of the refusal's status, where HAProxy answered the status
  itself. A host no environment names, an empty `X-Forwarded-Proto` under `forwarded` and no route get 12, where a
  request that is not gRPC gets `404`; an upgrade other than WebSocket gets 7, with `grpc-message: Invalid CORS request`
  when the call carries an `Origin`, and a refused origin 7 with that message, where such a request gets `403`; `%00` in
  the path and a dot segment with an escaped `;` parameter get 13, where such a request gets `400`. Over HTTP/2 the
  answer is one header frame with no `content-length`, and over HTTP/1.1 it keeps `content-length: 0`, as Envoy's does.
  A call only HAProxy refuses, where Envoy serves it, gets the plain answer, such as the `400` to a client's
  `X-Request-Id` too long for HAProxy (declared on `services.<service name>.grpc`), and a dot segment whose `;` is
  written with dots gets 13, where Envoy resolves the segment and routes the call (declared on the dot segment's row).
- **A gRPC call HAProxy gets no answer for is answered as Envoy answers it**: `200` with
  `content-type: application/grpc` and `grpc-status: 14` in one header frame, where HAProxy answered `502` with an
  empty body, and `504` with its HTML page. That is a service that refuses the connection, resets it, closes it before
  its answer, resets the stream, answers in HTTP/1.1 or sends nothing for 15 s, and the last three carry Envoy's
  `grpc-message`: `upstream connect error or disconnect/reset before headers. reset reason: remote reset`, the same
  with `protocol error`, and `upstream request timeout`. A gRPC call is told by its content type, as the check's
  refusal tells one: `application/grpc` alone or followed by `+`, compared as written. Over HTTP/1.1 the answer keeps
  `content-length: 0`, as Envoy's does. A gRPC call to an HTTP service gets the same, except the `REFUSED_STREAM` below.
  A request that is not a gRPC call, to a gRPC service that resets the stream or answers in HTTP/1.1, gets the empty
  `502` above, where Envoy answers the stream reset `503` and the HTTP/1.1 answer `502`, each with its text as
  `text/plain` (declared on `services.<service name>.grpc`).
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
  up. A gRPC call to such a service gets the same reset, where Envoy answers it `200` with `grpc-status: 14`.
- **Every response carries `x-request-id`**, as with Envoy: a version 4 UUID HAProxy makes (`uuid(4)`), as Envoy
  makes one, or with `client_address: forwarded` the client's own, which the service receives too; with `connection`
  a client's own id is replaced, as Envoy replaces it.
- **Every request is logged** on standard output ([The request log](#the-request-log)), as by the Envoy output with no
  `log` key. A worker that stopped on a signal 11 printed
  `Current worker (8) exited with code 139 (Segmentation fault)`, and HAProxy then stopped every process and exited
  with 139. HAProxy sets its connection limit from the limit on open files at start and prints it
  (`Automatically setting global.maxconn to 120.` with 300 files): with the table full, a new connection waited
  unanswered and nothing was printed, and it was served once others closed.
- **WebSocket is the one upgrade passed on**, as by Envoy: a request with another `Upgrade`, or with an `Upgrade` that
  holds more than `websocket` on one line or two, is answered `403`, and an `Upgrade` that `Connection` does not name
  is removed. An `h2c` upgrade is answered `400` (above).
- **A request that carries `Expect: 100-continue` and no body gets no `100 Continue` from HAProxy**, where Envoy sends one
  before its answer; no client waits for it, since a client sends the expectation only with a body.
- **Over HTTP/2, an empty answer HAProxy makes itself carries `content-length: 0`**, where Envoy's carries none: a `404`
  for an unknown host, a `400` and, in a file with the check, the `403` of an unanswered check. Over HTTP/1.1 both send
  `content-length: 0`.
- **A render for HAProxy reads no key of the [`envoy`](envoy.md) block and refuses none of them**, so tracing written
  at `envoy.tracing` is Envoy's alone; a render for Envoy reads it as before.
