package format

import (
	"net/netip"
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// HAProxy's refusals that several keys share. Each is named for HAProxy because every target declares its keys in this
// package, and another target's refusals say what that proxy offers instead.
var (
	// haproxyNotWritten declares a key HAProxy could carry that this version's generator does not write yet.
	haproxyNotWritten = Support{Level: Refused, Note: "not written by this version's HAProxy generator; render it for envoy"}
	// jwt_verify checks a token's signature against one PEM key file or HMAC secret read with the configuration, and
	// checks no exp, iss or aud claim (HAProxy 3.4 configuration manual, jwt_verify), so it cannot verify the token the
	// format describes: keys from a key set or a URL, an issuer and audiences.
	haproxyNoJWT = Support{Level: Refused,
		Note: "HAProxy's jwt_verify takes one key file, with no key set, URL, issuer or audience check; verify the token in the service auth.ask names"}
	// The official images refuse a filter opentelemetry line, measured on HAProxy 3.2.26 and 3.4.7: that filter is an
	// addon built outside HAProxy's source tree, and the OpenTracing filter is not compiled into the images either.
	haproxyNoTracing = Support{Level: Refused,
		Note: "the official HAProxy images have no tracing filter (the OpenTelemetry filter is an addon built from source); remove tracing, or render it for envoy"}
	// haproxyNoZone is what HAProxy says of a server address that carries an IPv6 zone, such as fe80::1%eth0: the reader
	// takes it as an IP address (netip.ParseAddr), and HAProxy refuses to load it in a server line ("could not resolve
	// address", measured on HAProxy 3.2.26 and 3.4.7, with the brackets and without them).
	haproxyNoZone = "HAProxy 3.2 and 3.4 load no zone in a server's address; give the service an address without one"
)

// haproxyTarget is HAProxy's declaration, one line for each row of the support table in its order, so a reader sees
// which keys this version writes for HAProxy without running anything. TestHAProxyWrites holds every row's words.
var haproxyTarget = Target{Name: "haproxy", Title: "HAProxy", Keys: map[string]Support{
	"listen": {Level: Supported},

	// monitor-uri answers 200 while the process runs, where Envoy's /ready reports the server's state (HAProxy 3.4
	// configuration manual, monitor-uri).
	"admin": {Level: Partial, Note: "/ready answers 200 while HAProxy runs, not when the server is ready; no other admin path in this version"},

	// The versions, the suites and the key exchange groups are Envoy's (measured on HAProxy 3.2.26 and 3.4.7 beside
	// Envoy 1.40.2 with openssl s_client, one version, suite and group at a time); OpenSSL has no suites of equal
	// preference, which BoringSSL gives Envoy, so the suite chosen from a list can differ.
	"tls":             {Level: Partial, Note: "of the TLS 1.2 suites both take, HAProxy picks by its own order, where Envoy takes the client's among suites it holds equal: a client that offers OpenSSL's default list is given ECDHE-RSA-AES128-GCM-SHA256, where Envoy gives ECDHE-RSA-CHACHA20-POLY1305"},
	"tls.certificate": {Level: Supported},
	"tls.key":         {Level: Supported},
	"tls.clients":     {Level: Supported},
	// Every client certificate Envoy refuses, HAProxy refuses in the handshake too (measured: none, another CA's,
	// expired, self-signed, for servers only, an intermediate's without its chain); OpenSSL names one reason otherwise.
	"tls.clients.ca": {Level: Partial, Note: "a client certificate that is not valid yet is refused with the alert bad_certificate, where Envoy sends certificate_expired"},

	"client_address": {Level: Supported},

	"limits":                       haproxyNotWritten,
	"limits.connections":           haproxyNotWritten,
	"limits.request_headers":       haproxyNotWritten,
	"limits.request_headers.size":  haproxyNotWritten,
	"limits.request_headers.count": haproxyNotWritten,

	"timeouts": {Level: Supported},
	// timeout server limits the time without data, not the whole response: measured on HAProxy 3.4.7, a service that
	// sent one byte a second for 10 s under a 2 s timeout answered 200. No directive holds a time for a whole request,
	// so none, which sets no limit, is the one value HAProxy writes as the file says.
	"timeouts.request": {Level: Partial,
		Note: "without the key a service that sends nothing for 15 s is answered 504, as by Envoy; HAProxy's limits count the time without data",
		Refuses: []Refusal{{Form: "a time",
			Instead: "HAProxy has no limit on a whole request (timeout server is the time without data, timeouts.idle); write timeouts.request: none",
			Applies: func(value, _ *tree.Node) bool { return value.Text != "none" }}}},
	"timeouts.idle":          haproxyNotWritten,
	"timeouts.upstream_idle": haproxyNotWritten,

	"services":                            {Level: Supported},
	"services.<service name>":             {Level: Supported},
	"services.<service name>.http":        {Level: Supported},
	"services.<service name>.grpc":        {Level: Supported},
	"services.<service name>.host_header": {Level: Supported},
	// A name HAProxy looks up again while it runs takes a resolvers section and a server-template, which this version
	// does not write yet, so it takes an IP address alone. Applies tells the two apart as the reader does: an address
	// netip.ParseAddr reads is an IP address, and any other a DNS name.
	"services.<service name>.address": {Level: Partial, Note: "an IP address with no zone",
		Refuses: []Refusal{{Form: "a DNS name",
			Instead: "this version's HAProxy generator writes no DNS name; give the service's IP address, or render it for envoy",
			Applies: func(value, _ *tree.Node) bool {
				_, err := netip.ParseAddr(value.Text)
				return err != nil
			}},
			{Form: "a zoned IPv6 address", Instead: haproxyNoZone, Applies: func(value, _ *tree.Node) bool { return zoned(value.Text) }}}},
	"services.<service name>.addresses": {Level: Partial, Note: "IP addresses with no zone",
		Refuses: []Refusal{{Form: "holding a zoned IPv6 address", Instead: haproxyNoZone, Applies: func(value, _ *tree.Node) bool {
			return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return zoned(item.Text) })
		}}}},
	"services.<service name>.balance":          haproxyNotWritten,
	"services.<service name>.hash":             haproxyNotWritten,
	"services.<service name>.hash.header":      haproxyNotWritten,
	"services.<service name>.hash.client":      haproxyNotWritten,
	"services.<service name>.health":           haproxyNotWritten,
	"services.<service name>.health.path":      haproxyNotWritten,
	"services.<service name>.health.grpc":      haproxyNotWritten,
	"services.<service name>.health.interval":  haproxyNotWritten,
	"services.<service name>.health.timeout":   haproxyNotWritten,
	"services.<service name>.health.healthy":   haproxyNotWritten,
	"services.<service name>.health.unhealthy": haproxyNotWritten,

	"dns":          haproxyNotWritten,
	"dns.resolver": haproxyNotWritten,

	"auth":                          haproxyNotWritten,
	"auth.ask":                      haproxyNotWritten,
	"auth.timeout":                  haproxyNotWritten,
	"auth.enabled":                  haproxyNotWritten,
	"auth.when_unavailable":         haproxyNotWritten,
	"auth.only_with_header":         haproxyNotWritten,
	"auth.send":                     haproxyNotWritten,
	"auth.accept":                   haproxyNotWritten,
	"auth.send_request":             haproxyNotWritten,
	"auth.jwt":                      haproxyNoJWT,
	"auth.jwt.issuer":               haproxyNoJWT,
	"auth.jwt.audiences":            haproxyNoJWT,
	"auth.jwt.keys":                 haproxyNoJWT,
	"auth.jwt.ca":                   haproxyNoJWT,
	"auth.jwt.header":               haproxyNoJWT,
	"auth.jwt.optional":             haproxyNoJWT,
	"auth.jwt.claims":               haproxyNoJWT,
	"auth.jwt.claims.<header name>": haproxyNoJWT,

	"remove_from_clients": {Level: Supported},

	"cors":               {Level: Supported},
	"cors.allow_methods": {Level: Supported},
	// http-request return leaves out a header whose value comes out empty: measured on HAProxy 3.4.7, a preflight with
	// no Access-Control-Request-Headers was answered without access-control-allow-headers.
	"cors.allow_headers":  {Level: Partial, Note: "an empty Access-Control-Allow-Headers is left out of a preflight's answer, where Envoy sends it empty"},
	"cors.expose_headers": {Level: Supported},
	"cors.max_age":        {Level: Supported},

	"origins":                             {Level: Supported},
	"origins.<environment name>":          {Level: Supported},
	"origins.<environment name>.hosts":    {Level: Supported},
	"origins.<environment name>.origins":  {Level: Supported},
	"origins.<environment name>.patterns": {Level: Supported},

	"routes":                                    {Level: Supported},
	"routes[].rpc":                              {Level: Supported},
	"routes[].rpc.retries":                      haproxyNotWritten,
	"routes[].rpc.rate_limit":                   haproxyNotWritten,
	"routes[].rpc.rate_limit.requests":          haproxyNotWritten,
	"routes[].rpc.rate_limit.per":               haproxyNotWritten,
	"routes[].rpc.rate_limit.burst":             haproxyNotWritten,
	"routes[].rpc.rate_limit.by":                haproxyNotWritten,
	"routes[].rpc.to":                           {Level: Supported},
	"routes[].rpc.to.<package pattern>":         {Level: Supported},
	"routes[].rpc.to.<package pattern>.service": {Level: Supported},
	"routes[].rpc.to.<package pattern>.name":    {Level: Supported},
	"routes[].name":                             {Level: Supported},
	"routes[].prefix":                           {Level: Supported},
	"routes[].path":                             {Level: Supported},
	// The script runs a request's patterns within a budget of work, so no request holds HAProxy for long, and keeps the
	// states of a thread's patterns within one budget of memory (proxyfile.lua, budget and mostHeld, measured on HAProxy
	// 3.2.26 and 3.4.7 beside Envoy 1.40.2); the generator refuses a program longer than the file writes
	// (values.program).
	"routes[].pattern": {Level: Partial,
		Note: "a request whose pattern routes take more than proxyfile.lua's budget of work, about 24 ms of CPU for one request alone and 33 ms for each of many at once, such as a path of 14,000 bytes that 80 pattern routes read to its end, is answered 400, where Envoy's RE2 always decides, in under 1 ms of CPU; the states the patterns build take up to about 1 MB on each HAProxy thread, and a flood of new paths grew HAProxy by about 48 MB with 12 threads, where it grew Envoy by about 76 MB; a pattern whose program is longer than 65,536 bytes is refused, where Envoy refuses an RE2 program of more than 100 instructions"},
	"routes[].method":              {Level: Supported},
	"routes[].to":                  {Level: Supported},
	"routes[].rewrite":             {Level: Supported},
	"routes[].auth":                haproxyNotWritten,
	"routes[].retries":             haproxyNotWritten,
	"routes[].rate_limit":          haproxyNotWritten,
	"routes[].rate_limit.requests": haproxyNotWritten,
	"routes[].rate_limit.per":      haproxyNotWritten,
	"routes[].rate_limit.burst":    haproxyNotWritten,
	"routes[].rate_limit.by":       haproxyNotWritten,

	"rpc":                   {Level: Supported},
	"rpc.methods":           {Level: Supported},
	"rpc.methods[].service": {Level: Supported},
	"rpc.methods[].method":  {Level: Supported},
	"rpc.methods[].action":  haproxyNotWritten,
	"rpc.methods[].reports": haproxyNotWritten,
	"rpc.proto":             {Level: Supported},
	"rpc.proto.roots":       {Level: Supported},
	"rpc.proto.option":      {Level: Supported},

	// Envoy logs every request with no log key; HAProxy logs none, which lets nothing more through, but is a
	// difference.
	"log": {Level: Refused,
		Note: "not written by this version's HAProxy generator, and without it HAProxy logs no request, where the Envoy output logs every request; render it for envoy"},
	"log.status_from": haproxyNotWritten,
	"log.sample":      haproxyNotWritten,

	"tracing":                                     haproxyNoTracing,
	"tracing.provider":                            haproxyNoTracing,
	"tracing.provider.xray":                       haproxyNoTracing,
	"tracing.provider.xray.agent":                 haproxyNoTracing,
	"tracing.provider.xray.segment":               haproxyNoTracing,
	"tracing.provider.xray.sample":                haproxyNoTracing,
	"tracing.provider.xray.sample.first":          haproxyNoTracing,
	"tracing.provider.xray.sample.rate":           haproxyNoTracing,
	"tracing.provider.xray.sample.rules":          haproxyNoTracing,
	"tracing.provider.xray.sample.rules[].host":   haproxyNoTracing,
	"tracing.provider.xray.sample.rules[].method": haproxyNoTracing,
	"tracing.provider.xray.sample.rules[].path":   haproxyNoTracing,
	"tracing.provider.xray.sample.rules[].first":  haproxyNoTracing,
	"tracing.provider.xray.sample.rules[].rate":   haproxyNoTracing,
	"tracing.provider.otel":                       haproxyNoTracing,
	"tracing.provider.otel.endpoint":              haproxyNoTracing,
	"tracing.provider.otel.service":               haproxyNoTracing,
	"tracing.provider.otel.sample":                haproxyNoTracing,
	"tracing.provider.otel.sample.rate":           haproxyNoTracing,
	"tracing.tags":                                haproxyNoTracing,
	"tracing.tags.<tag name>":                     haproxyNoTracing,
	"tracing.tags.<tag name>.env":                 haproxyNoTracing,
	"tracing.tags.<tag name>.header":              haproxyNoTracing,
	"tracing.tags.<tag name>.default":             haproxyNoTracing,
	"tracing.path_in_url":                         haproxyNoTracing,

	// The normalisation block passes every escape it does not decode as written, in the hex case written, and an invalid
	// one, as Envoy does, and the script beside the file escapes what Envoy escapes and matches patterns as RE2 does:
	// measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2.
	"the request's path normalised before a route is picked": {Level: Supported},
	"a dot segment with a ; parameter answered 400": {Level: Partial,
		Note: "a dot segment whose ; parameter is written with dots, such as /a/..;/b, is answered 400, where Envoy 1.40 drops the parameter and resolves the segment"},
	// The file answers 400 to what Envoy refuses before it reads a request and HAProxy's parsers pass, and to what Envoy
	// serves in a way HAProxy cannot write; its parsers refuse the rest. Each answer the note names was measured on
	// HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2; the two images answered alike.
	"the request line and header names read before a route is picked": {Level: Partial,
		Note: "over HTTP/1.1 an absolute target is answered 400, where Envoy routes it by its host and sends the path alone with that host as Host, or answers 404 when no environment has the host, and OPTIONS * and CONNECT are answered 400, where Envoy answers 404; HTTP/0.9 is answered 400, where Envoy answers 426; a Connection token other than close, keep-alive, upgrade, HTTP2-Settings and te is answered 400, where Envoy removes the header the token names and serves the request; ten Connection elements or more, an empty one counted but for an empty last one, are answered 400, where Envoy counts no empty element and serves a token with ten; an h2c upgrade is answered 400, where Envoy serves it as a plain request; a header line folded onto the next, and a header value that holds three spaces or tabs in a row, are answered 400, where Envoy serves them; a WebSocket upgrade with no Sec-WebSocket-Key is answered 400, where Envoy passes it on, or answers 403 to an origin it does not admit; more than 100 header lines are answered 431 or, from 102, 400, where Envoy answers 431, and 101 lines or more of a header Envoy joins into one line are answered 400, where Envoy serves them; two Host lines of different hosts, and a Host that holds a comma, are answered 400, where Envoy answers 404; Transfer-Encoding: chunked on two lines is answered 400, and Transfer-Encoding: gzip, chunked 422, where Envoy answers 501; a header of 20000 bytes is answered 431 and a path of 20000 bytes 414, where Envoy, under its default limit of 60 KiB, serves both; over HTTP/2 a request with 97 header fields or more is answered 400, where Envoy resets its stream, and so is one with fewer whose cookies, counted one a field, reach 97, and one with 97 fields or more of a header Envoy joins, which Envoy serves, is answered 400 or, with 120, its stream reset; a WebSocket, an extended CONNECT, is answered 400, where Envoy closes the connection; a path whose escaped form outgrows HAProxy's buffer, such as one of 7000 bytes above 0x7F, is answered 400 by HAProxy 3.4.7 and 500 by 3.2.26, where Envoy serves it; a header or trailer name outside RFC 9110's token characters resets the stream, where Envoy closes the connection; a # in :path resets the stream, where Envoy answers 400; and a request with neither :authority nor host is answered 404, where Envoy closes the connection; over HTTP/1.1 a request trailer whose name is outside those characters ends the connection with no answer when the body comes after the headers, where Envoy answers 400"},
}, LessStrict: haproxyLessStrict}

// haproxyLessStrict are the ways the HAProxy output is less strict than the Envoy output. HAProxy's parsers decide
// each before any rule of the file reads the request, and its timeout counts the time without data, so the file can
// close none of them; each was measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2, the two images alike.
var haproxyLessStrict = []LessStrict{
	{Row: requestLine, Name: "an HTTP/2 :authority with a default or empty port reaches the service without it",
		Measured: "Over HTTP/2, `:authority: api.example.com:` and, over plain HTTP, `api.example.com:80` reach the service as `Host: api.example.com`, where the Envoy output sends them as written."},
	{Row: requestLine, Name: "ten Connection tokens close or keep-alive are served",
		Measured: "`Connection: close` ten times on one line is served, where Envoy answers ten tokens `400`: HAProxy removes `close` and `keep-alive` from the header before any rule reads it, so the file's count of ten elements does not see them."},
	{Row: requestLine, Name: "a request with no body reaches the service without content-length: 0",
		Measured: "A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`, as it does to `PUT`, `PATCH`, `OPTIONS`, `PROPFIND` and an unknown method; a rule that set the header did not reach the service."},
	{Row: requestLine, Name: "two Host lines of one host are served",
		Measured: "`Host: api.example.com` on two lines reaches the service as one line, where Envoy reads the two as one value no environment names and answers `404`."},
	{Row: requestLine, Name: "two Content-Length lines of one value are served",
		Measured: "`Content-Length: 3` on two lines, with a body of 3 bytes, reaches the service as one line, where Envoy answers `400`; over HTTP/2 two `content-length: 0` fields are served, where Envoy closes the connection."},
	{Row: requestLine, Name: "Content-Length beside Transfer-Encoding: chunked is served",
		Measured: "`Content-Length: 3` beside `Transfer-Encoding: chunked`, with a chunked body, reaches the service chunked and with no `Content-Length`, where Envoy answers `400`."},
	{Row: requestLine, Name: "over HTTP/2, two host fields and no :authority are served",
		Measured: "Over HTTP/2 with no `:authority`, `host: api.example.com` and then `host: evil.example.net` reach the service as the first, and the same host twice as one, where Envoy closes the connection."},
	{Row: requestLine, Name: "a WebSocket upgrade a service answers without switching passes the service's body",
		Measured: "A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body."},
	{Row: "timeouts.request", Name: "without timeouts.request, an answer that keeps arriving runs past 15 s",
		Measured: "Without `timeouts.request`, a service that keeps sending runs past 15 s, where the Envoy output ends a request 15 s after it starts: HAProxy's `timeout server` counts the time without data (on HAProxy 3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served)."},
}
