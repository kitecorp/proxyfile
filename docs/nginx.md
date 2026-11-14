# nginx

`proxyfile render --target nginx` writes `nginx.conf`, a whole nginx configuration, from the same proxyfile as Envoy's,
and `proxyfile.js` beside it. The two go together: `nginx.conf` loads nginx's njs module and imports `proxyfile.js` from
its own folder, and nginx refuses to load the configuration when the script or the module's file is missing (measured
on nginx 1.30.6 and 1.31.8). The official nginx images carry njs; a distribution's nginx may need its njs package
installed. Line 2 of `nginx.conf` names the version it needs, njs 1.0.2 or later, the one measured. `proxyfile check`
compares both files.

This version writes routing: the listener, the admin port's `/ready`, the services with their HTTP and gRPC ports, their
addresses (IP addresses with no zone) and `host_header`, each environment's hosts (none that starts or ends with a dot)
with the refusal of a browser origin on none of them, the routes by prefix and path (any character but `%`, one Envoy
escapes before it picks a route, `"` `<` `>` `^` `` ` `` `{` `|` `}`, and one outside ASCII), pattern and method with
their rewrites, and the rpc entry with its methods. It refuses every other core key a file writes, naming the key where
it is written, so a file that needs one is never rendered as if the key were not there. It also refuses, by its key, a
value whose nginx form is longer than nginx reads: 4094 bytes between quotes, or 4095 without them (measured on nginx
1.30.6 and 1.31.8). nginx's column in [Portability](portability.md#support-by-proxy-from-the-generators) says how it
writes each key and, for a key it refuses, what to write instead.

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

Every file refuses a browser origin on none of the environment's hosts, as the Envoy output does: a request or a
preflight whose `Origin` names another host is answered `403` before a route is picked, so it reaches no service, a
WebSocket upgrade included. An origin on one of the environment's own hosts is admitted on either scheme and any port,
and compared as written: `http://LOCALHOST`, `http://localhost.` and `null` are refused where the host is `localhost`.
A preflight from an admitted origin is answered `200` by nginx itself, with no `Access-Control-*` header, when a route
takes its path. A request with no `Origin` is not checked. This version writes no `cors` key and no list of
`origins` or `patterns`, and refuses a file that writes one, so no other origin is admitted. Two differences, measured
on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, which nginx's row of `origins.<environment name>.hosts` names:

- **A present but empty `Origin` is let through as no `Origin`**, where the Envoy output answers it `403`: nginx gives
  the same empty value for an empty header as for none. No browser sends one, because a browser's `Origin` is an
  origin or `null` ([the Fetch standard](https://fetch.spec.whatwg.org/#origin-header):
  `origin-or-null = serialized-origin / %s"null"`), and a client that is not a browser can leave the header out with
  the same effect.
- **A foreign origin to a path no route takes is answered `403`**, where the Envoy output answers `404`: nginx checks
  the origin before it picks a route.

## What nginx writes with no key

Each of these is in every file the nginx generator writes and differs from the Envoy output, so no row of that table
holds it. Each was measured on nginx 1.30.6 and 1.31.8, and Envoy's answers on Envoy 1.40.2, unless it says otherwise.

- **nginx's own answers carry `Server: nginx`**, with no version, where the Envoy output's own answers carry no `Server`
  header ([Defaults](defaults.md)). These are the `400`, `403`, `404` and `502` the file answers with an empty body, to
  a path it refuses, a request Envoy's parser refuses (a method it does not know, a byte above 0x7F in an HTTP/1.1
  path), a path PCRE2 gave up on, an upgrade other than WebSocket, a request no route takes and a service that refuses
  the connection, the `426` to an HTTP/1.0 request, whose status line has no reason phrase, the `403` with the body
  `Invalid CORS request` to a foreign browser origin, the `200` to a preflight, and the `400` nginx answers to a request
  it cannot read, such as a path with an invalid escape or a header line over 8 KiB, with its own HTML page naming
  nginx. A service's own `Server` header is passed on, as the Envoy output passes it.
- **A request id nginx makes is 32 hexadecimal digits**, where Envoy makes a UUID. A client's own `x-request-id` is
  kept: the service receives it and the answer carries it. nginx's `404` to a host no environment names and its `400`
  to a path with an invalid escape carry no `x-request-id`, where every answer of the Envoy output carries one. A client
  that sends `x-request-id` twice: the service receives the two joined as `one, two`, where Envoy joins them as
  `one,two`.
- **`Vary: Origin` is sent beside a service's own `Vary`**, as a second header line, not merged into the service's.
- **No request log is written, and the error log holds only the lines that stop nginx (`emerg`)**: a file nginx cannot
  load and a port it cannot bind are still printed. At `error` nginx writes the request line of a request whose service
  refused the connection, and at `alert` that of a path PCRE2 gave up on, each with its query, which can hold a token.
  The same level also hides a worker process that crashed (`[alert] worker process 7 exited on signal 11`) and the
  connections running out (`[warn] 4 worker_connections are not enough, reusing connections`), measured on nginx
  1.31.8 by holding more connections open than `worker_connections` allows and stopping a worker with signal 11: at
  `emerg` the log held no line, and at `info` both. The `log` key is refused until a later version writes it.
- **A request header line over 8 KiB is answered `400`**, where Envoy, under a larger limit of its own, passes it on: a
  header of 9000 bytes is answered `400` by nginx and reaches the service through Envoy, and one of 70000 bytes is
  answered `400` by nginx and `431` by Envoy. More than 100 header lines are answered `400`, where Envoy answers `431`.
  A request line over 8 KiB is answered `414`, where Envoy passes a path of 9000 bytes on.
- **A service that does not take the connection within 5 s is answered `504`**, after those 5 s, where the Envoy output
  answers `502` to a service it cannot reach.
- **A service that sends nothing is answered `504` when the Envoy output answers it**: 15 s after the request without
  `timeouts.request`, and after 5 minutes with `timeouts.request: none`. nginx counts its limits between two reads or
  writes, so an answer that keeps coming runs past the 15 s at which Envoy ends it, the difference the
  `timeouts.request` row names.
- **A client connection is closed after 1000 requests**, the 1000th answer carrying `Connection: close`, where Envoy
  served 1200 on one connection and kept it open. nginx also closes a connection that has served requests for an hour
  (`keepalive_time`; read in nginx's documentation, not run).
- **HTTP/0.9 gets no answer**: nginx closes the connection, where Envoy answers `426`.
- **An absolute request line with user information, `GET http://user@localhost/z`, or with no `Host` header is
  answered `400`**, where Envoy serves it with the host the line names. Any other absolute request line routes by the
  host it names, as Envoy's does, however many spaces come before it; one whose host a query or a fragment follows
  directly, `GET http://localhost?x=1`, is answered `400`, where Envoy answers `404`. A tab between the method and the
  target is answered `400`, where Envoy serves the request.
- **A `Host` header sent twice is answered `400`**, where Envoy answers `404`; over HTTP/2, a `host` header that differs
  from `:authority` is answered `400`, where Envoy routes by `:authority`.
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
- **A request trailer whose name holds a character outside RFC 9110's token characters, such as `x@at`, is let
  through over HTTP/1.1**: nginx serves the request, and the service receives its body and no trailer, where Envoy
  answers `400`. This difference is not stricter: nginx serves a request Envoy refuses, because `proxyfile.js` runs
  before the body arrives and never sees a trailer. The service receives no trailer through either proxy, whatever
  its name (`x-t` too).
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
