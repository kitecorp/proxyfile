package format

import (
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// nginx's refusals that several keys share. Each is named for nginx because every target declares its keys in this
// package, and another target's refusals say what that proxy offers instead.
var (
	// nginxNotWritten declares a key nginx could carry that this version's generator does not write yet.
	nginxNotWritten = Support{Level: Refused, Note: "not written by this version's nginx generator; render it for envoy"}
	nginxNoHealth   = Support{Level: Refused,
		Note: "nginx open source has no active health checks (nginx Plus's health_check); remove health, and nginx keeps every backend in the pool"}
	// A refusal's advice names no key this version also refuses: auth.ask and the otel provider are written by a later
	// version.
	nginxNoJWT = Support{Level: Refused,
		Note: "nginx open source cannot verify a token (nginx Plus's auth_jwt); verify it in the service, or render it for envoy"}
	nginxNoXRay = Support{Level: Refused,
		Note: "nginx has no X-Ray tracer; render it for envoy, or, once a later version writes the otel provider, send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter"}
	// nginxNoZone is what nginx says of a backend address that carries an IPv6 zone, such as fe80::1%en0: the reader
	// takes it as an IP address (netip.ParseAddr), and nginx refuses to load it in an upstream ("invalid IPv6 address
	// in upstream", measured on nginx 1.30.6 and 1.31.8).
	nginxNoZone = "nginx reads no zone in a backend's address; give the service an address without one"
)

// nginxPathAlone is what a prefix or a path may not hold on nginx: %, a character Envoy escapes before it picks a route,
// in upper case hex, and one outside ASCII. nginx matches a path holding such a character on the path it has decoded
// (the path classes of the nginx generator), where Envoy matches the escape: a prefix /p%7C took /p|x on Envoy and
// /p| would take it on nginx (measured on Envoy 1.40.2). Every other character is matched alike, on the path as the
// client wrote it or on the one both proxies normalise.
var nginxPathAlone = regexp.MustCompile("[%\"<>^`{|}]|[^\\x00-\\x7F]")

// nginxRewriteText is what a rewrite may hold on nginx: the characters nginx sends as written when it escapes the new
// path again before proxy_pass. A rewrite holding another, such as %, a space, #, " or a letter outside ASCII, reached
// the service escaped through nginx and as written through Envoy (measured); ? has its own refusal.
var nginxRewriteText = regexp.MustCompile(`^[A-Za-z0-9._~/!$&'()*+,;=:@\[\]?-]*$`)

// nginxPathNote is what a prefix or a path may hold on nginx.
const nginxPathNote = "any character but %, one Envoy escapes before it picks a route (\" < > ^ ` { | }) and one outside ASCII"

// nginxDollarPath refuses a certificate's or a key's path holding $: ssl_certificate and ssl_certificate_key read a $
// as the start of a variable, and with a variable nginx reads the file at each handshake, so nginx -t passes a file
// that is missing and every handshake then fails, where the Envoy output refuses to start (measured on nginx 1.30.6 and
// 1.31.8). ssl_client_certificate reads its $ as written and refuses a missing file at start, so a CA's path keeps it.
var nginxDollarPath = Refusal{Form: "holding $",
	Instead: "nginx reads a $ in a certificate's or a key's path as the start of a variable, and a path written to keep the $ is read at each handshake, which nginx -t does not check; put the file at a path without $, or render it for envoy",
	Applies: func(value, _ *tree.Node) bool { return strings.Contains(value.Text, "$") }}

// nginxPathRefusal refuses a prefix or a path that would take other requests on nginx than on Envoy.
var nginxPathRefusal = Refusal{Form: "holding %, a character Envoy escapes before it picks a route (\" < > ^ ` { | }) or one outside ASCII",
	Instead: "nginx matches a path holding a character Envoy escapes as it decodes it, where Envoy matches the escape, so a prefix or a path with % or such a character would take other requests on nginx than on Envoy; write it without them, as a pattern, or render it for envoy",
	Applies: func(value, _ *tree.Node) bool { return nginxPathAlone.MatchString(value.Text) }}

// nginxFramedBy are the request headers nginx reads itself to frame the request, route it and keep or upgrade its
// connection, whatever a location sets, so remove_from_clients may not name them on nginx. The Envoy output breaks on
// five of them (measured on Envoy 1.40.2): with host removed every request was answered 404, with content-length and
// transfer-encoding removed a body was sent on chunked, and with upgrade and connection removed a WebSocket upgrade hung.
var nginxFramedBy = []string{"host", "content-length", "transfer-encoding", "connection", "upgrade", "te", "keep-alive", "http2-settings"}

// zoned reports whether text is an IPv6 address with a zone, read as the reader reads an address.
func zoned(text string) bool {
	addr, err := netip.ParseAddr(text)
	return err == nil && addr.Zone() != ""
}

// nginxTarget is nginx's declaration, one line for each row of the support table in its order, so a reader sees which
// keys this version writes for nginx without running anything. TestTargetDeclarations fails a row left out.
var nginxTarget = Target{Name: "nginx", Title: "nginx", Keys: map[string]Support{
	"listen": {Level: Supported},

	// Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2: /ready answered as Envoy answers it, a query and every
	// other path 404 where Envoy answers 403, each method on each path as Envoy answers it but TRACE, and no log line
	// for an admin request from either.
	"admin": {Level: Partial, Note: "/stats is nginx's seven counters (active connections, accepts, handled, requests, reading, writing and waiting) and /server_info its version, not Envoy's; any other path or a query is answered 404, where the Envoy output answers 403; TRACE is answered 405 with no body, where the Envoy output answers it as GET"},

	// nginx answers what fails at the TLS level after the handshake, where Envoy ends the connection or the handshake.
	// From the same TLS 1.2 ciphers and key exchange groups it chose others than Envoy for one offer: for openssl
	// s_client's, ECDHE-RSA-AES256-GCM-SHA384 where Envoy chose ECDHE-RSA-CHACHA20-POLY1305, and P-256 for an offer
	// that names it first, where Envoy chose X25519, or X25519MLKEM768 over TLS 1.3. With client certificates nginx
	// answers 421 to a request whose Host is another name than its TLS server name, when that name is an environment's
	// host, even one of the same environment (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"tls": {Level: Partial,
		Note: "plain HTTP sent to the TLS port is answered 400, where the Envoy output closes the connection; from the same ciphers and key exchange groups nginx can choose others for the same client"},
	"tls.certificate": {Level: Partial, Note: "a path without $", Refuses: []Refusal{nginxDollarPath}},
	"tls.key":         {Level: Partial, Note: "a path without $", Refuses: []Refusal{nginxDollarPath}},
	"tls.clients":     {Level: Supported},
	"tls.clients.ca": {Level: Partial,
		Note: "a client certificate that is missing or fails is answered 400 after the handshake, where the Envoy output ends the handshake; a request whose Host names another host than its TLS server name, a host of an environment, is answered 421; neither reaches a service"},

	// The Envoy output answers an X-Forwarded-Proto line with no colon 400, and nginx stores it as an empty value,
	// which the generator answers 404, as the Envoy output answers an empty one (measured on Envoy 1.40.2 beside nginx
	// 1.30.6 and 1.31.8).
	"client_address": {Level: Partial,
		Note: "with forwarded, an X-Forwarded-Proto line with no colon is answered 404, where the Envoy output answers 400"},

	"limits": {Level: Supported},
	// With worker_processes 1 and worker_connections 6, nginx served a seventh client by closing an idle one, and with
	// two requests in flight served one and closed the other, where the Envoy output closed the seventh connection and
	// served the six (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"limits.connections": {Level: Refused,
		Note: "nginx counts its own listening sockets and every connection to a service against one limit, and closes a client's idle connection to take a new one, so no nginx setting holds client connections to a number; remove limits.connections, or render it for envoy"},
	"limits.request_headers": {Level: Supported},
	// The generator's headerLines says how nginx's buffers hold the size; measured on nginx 1.30.6 and 1.31.8 beside
	// Envoy 1.40.2 at 4 KiB, 60 KiB, 96 KiB, 128 KiB and 8 MiB.
	"limits.request_headers.size": {Level: Partial,
		Note: "a header block within 64 bytes of the limit can be refused, where the Envoy output takes it; above 64 KiB a line longer than one of nginx's buffers (32 KiB to 64 KiB) is refused, and a block of long lines can be refused under the limit; a request over it is answered 400, or 414 for a request line, where the Envoy output answers 431, and over HTTP/2 it ends the connection, where the Envoy output resets the stream"},
	// Envoy joins the lines of some headers, such as accept, into one and counts one field, and nginx counts each line;
	// over HTTP/2 the script counts the four pseudo-headers and each cookie field from what nginx keeps (headerNames in
	// the generator's script), :authority whether or not the client sent it, as nginx gives :authority and a host field
	// one host line: measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 at the default 100, nginx served 95 fields
	// beside :authority alone, a host field alone or both, where Envoy served 96, 96 and 95.
	"limits.request_headers.count": {Level: Partial,
		Note: "a request over it is answered 400, where the Envoy output answers 431 or resets the HTTP/2 stream; the lines of a header Envoy joins into one, such as accept, count once each; over HTTP/2 a cookie field holding several cookies counts once for each, where the Envoy output counts it once, and a request that sends :authority alone or a host field alone is refused one field under the limit, where the Envoy output takes it"},

	"timeouts": {Level: Supported},
	// nginx's proxy timeouts are set only between two successive reads or writes, not for a whole response (nginx.org,
	// ngx_http_proxy_module), so no directive holds a time for a whole request, and none, which sets no limit, is the
	// one value nginx writes as the file says.
	"timeouts.request": {Level: Partial,
		Note: "without the key nginx answers 504 to a service silent for 15 s after the request, as the Envoy output does; with none, nginx ends a request after 5 minutes with nothing read or written, as Envoy does",
		Refuses: []Refusal{{Form: "a time",
			Instead: "nginx has no limit on a whole request (its time limits count the time between two reads or writes); write timeouts.request: none, or render it for envoy",
			Applies: func(value, _ *tree.Node) bool { return value.Text != "none" }}}},
	// Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with idle: 2s, 10s, 90s and none: a service silent for
	// longer, a service that takes no connection, an answer trickling in, a body and a header block that pause, a gRPC
	// call to a silent service, and with idle and request none a service silent for 6 minutes, served by both.
	"timeouts.idle": {Level: Partial,
		Note: "a request body that stops for longer is ended with no answer, where the Envoy output answers 408; four per-operation timers stand for Envoy's one; none is written as 9223372036854775807ms, the longest time nginx loads, its form of no limit; a gRPC call it ends is answered 504 with the text stream timeout, where the Envoy output answers grpc-status 14 with that message"},
	// A service connection reused after 1 s and opened again after 4 s with upstream_idle: 3s, and reused after 70 s with
	// none, by both (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"timeouts.upstream_idle": {Level: Supported},

	"services":                     {Level: Supported},
	"services.<service name>":      {Level: Supported},
	"services.<service name>.http": {Level: Supported},
	// A gRPC call nginx answers itself because its service failed, refusing the connection, taking none within 5 s,
	// resetting the connection or the call's stream, closing the connection or answering in HTTP/1.1, is answered as
	// the Envoy output answers it, 200 with grpc-status 14 and no body, but with no grpc-message: nginx keeps no
	// variable that tells a stream reset or an HTTP/1.1 answer from the others, which Envoy names in its message
	// (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, with grpc-go and raw HTTP/2, over HTTP/2 and HTTP/1.1).
	// A request that is not a gRPC call, to the route of such a service, gets nginx's 502 with no body for the same
	// reason, where Envoy writes its text.
	"services.<service name>.grpc": {Level: Partial,
		Note: "a call whose service resets its stream, or answers in HTTP/1.1, is answered 200 with grpc-status 14 and no grpc-message, where the Envoy output adds the grpc-message upstream connect error or disconnect/reset before headers. reset reason: remote reset, or for the HTTP/1.1 answer reset reason: protocol error; a request that is not a gRPC call is answered 502 with no body in both cases, where the Envoy output answers the stream reset 503 and the HTTP/1.1 answer 502, each with that text as text/plain"},
	"services.<service name>.host_header": {Level: Supported},
	// nginx looks a name up again while it runs only through the server's resolve parameter and a resolver directive
	// (nginx.org, ngx_http_upstream_module), which this version does not write yet, so it takes an IP address alone.
	// The reader takes an address as an IP address exactly when netip.ParseAddr reads it, so that parser decides here.
	"services.<service name>.address": {Level: Partial, Note: "an IP address with no zone",
		Refuses: []Refusal{{Form: "a DNS name", Instead: nginxNotWritten.Note,
			Applies: func(value, _ *tree.Node) bool { _, err := netip.ParseAddr(value.Text); return err != nil }},
			{Form: "a zoned IPv6 address", Instead: nginxNoZone, Applies: func(value, _ *tree.Node) bool { return zoned(value.Text) }}}},
	"services.<service name>.addresses": {Level: Partial, Note: "IP addresses with no zone",
		Refuses: []Refusal{{Form: "holding a zoned IPv6 address", Instead: nginxNoZone, Applies: func(value, _ *tree.Node) bool {
			return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return zoned(item.Text) })
		}}}},
	"services.<service name>.balance":          nginxNotWritten,
	"services.<service name>.hash":             nginxNotWritten,
	"services.<service name>.hash.header":      nginxNotWritten,
	"services.<service name>.hash.client":      nginxNotWritten,
	"services.<service name>.health":           nginxNoHealth,
	"services.<service name>.health.path":      nginxNoHealth,
	"services.<service name>.health.grpc":      nginxNoHealth,
	"services.<service name>.health.interval":  nginxNoHealth,
	"services.<service name>.health.timeout":   nginxNoHealth,
	"services.<service name>.health.healthy":   nginxNoHealth,
	"services.<service name>.health.unhealthy": nginxNoHealth,

	"dns":          nginxNotWritten,
	"dns.resolver": nginxNotWritten,

	"auth":                          nginxNotWritten,
	"auth.ask":                      nginxNotWritten,
	"auth.timeout":                  nginxNotWritten,
	"auth.enabled":                  nginxNotWritten,
	"auth.when_unavailable":         nginxNotWritten,
	"auth.only_with_header":         nginxNotWritten,
	"auth.send":                     nginxNotWritten,
	"auth.accept":                   nginxNotWritten,
	"auth.send_request":             nginxNotWritten,
	"auth.jwt":                      nginxNoJWT,
	"auth.jwt.issuer":               nginxNoJWT,
	"auth.jwt.audiences":            nginxNoJWT,
	"auth.jwt.keys":                 nginxNoJWT,
	"auth.jwt.ca":                   nginxNoJWT,
	"auth.jwt.header":               nginxNoJWT,
	"auth.jwt.optional":             nginxNoJWT,
	"auth.jwt.claims":               nginxNoJWT,
	"auth.jwt.claims.<header name>": nginxNoJWT,

	// nginx empties a header by its name in each route location, and has no directive that removes headers by a prefix.
	"remove_from_clients": {Level: Partial, Note: "names written in full, none of them a header nginx frames or routes the request by",
		Refuses: []Refusal{{Form: "holding a name with a * prefix", Instead: "nginx takes each header by name; write each name in full",
			Applies: func(value, _ *tree.Node) bool {
				return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return strings.HasSuffix(item.Text, "*") })
			}},
			{Form: "holding " + strings.Join(nginxFramedBy[:len(nginxFramedBy)-1], ", ") + " or " + nginxFramedBy[len(nginxFramedBy)-1],
				Instead: "nginx reads these headers itself to frame and route the request; remove the name from remove_from_clients",
				Applies: func(value, _ *tree.Node) bool {
					return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return slices.Contains(nginxFramedBy, item.Text) })
				}}}},

	"cors": {Level: Supported},
	// nginx's $http_ variable joins two lines of a header with a comma and a space, and sends no header whose value is
	// empty (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"cors.allow_methods": {Level: Partial,
		Note: "two Access-Control-Request-Method lines are answered joined with a comma and a space, where the Envoy output joins them with a comma"},
	"cors.allow_headers": {Level: Partial,
		Note: "nginx sends no Access-Control-Allow-Headers line where the Envoy output sends an empty one, for a preflight that asks for no header; two Access-Control-Request-Headers lines are answered joined with a comma and a space, where the Envoy output joins them with a comma"},
	"cors.expose_headers": {Level: Supported},
	"cors.max_age":        {Level: Supported},

	"origins":                    {Level: Supported},
	"origins.<environment name>": {Level: Supported},
	// The check runs before the route choice, so it also refuses an origin on none of the hosts, or an empty Origin, on
	// a path no route takes, which the Envoy output answers 404 (measured on Envoy 1.40.2 beside nginx 1.30.6 and
	// 1.31.8, over HTTP/1.1 and HTTP/2).
	// nginx reads a host written with a dot at its start as the name and every name under it, refuses to load the host
	// ., and compares a host written with a dot at its end without the dot, where Envoy compares a host as written
	// (measured on nginx 1.30.6 and 1.31.8 and Envoy 1.40.2).
	"origins.<environment name>.hosts": {Level: Partial,
		Note: "a foreign or empty Origin to a path no route takes gets 403, where Envoy answers 404",
		Refuses: []Refusal{{Form: "holding a host that starts or ends with .",
			Instead: "nginx reads a dot at a host's start as every name under it and drops one at its end; write the host without it, or render it for envoy",
			Applies: func(value, _ *tree.Node) bool {
				return slices.ContainsFunc(value.Items, func(item *tree.Node) bool {
					return strings.HasPrefix(item.Text, ".") || strings.HasSuffix(item.Text, ".")
				})
			}}}},
	"origins.<environment name>.origins": {Level: Supported},
	// The Envoy output joins two Origin lines with a comma before its patterns read them, which no browser sends. The
	// patterns read no Origin over 8,192 bytes, which beside the longest path nginx reads held nginx longer than a
	// request should take (the generator's longestMatchedOrigin).
	"origins.<environment name>.patterns": {Level: Partial,
		Note: "an Origin holding a comma is answered 403, where the Envoy output can match two Origin lines joined with a comma; an Origin longer than 8,192 bytes is not matched against the patterns, so one a pattern would take is refused"},

	"routes":                                    {Level: Supported},
	"routes[].rpc":                              {Level: Supported},
	"routes[].rpc.retries":                      nginxNotWritten,
	"routes[].rpc.rate_limit":                   nginxNotWritten,
	"routes[].rpc.rate_limit.requests":          nginxNotWritten,
	"routes[].rpc.rate_limit.per":               nginxNotWritten,
	"routes[].rpc.rate_limit.burst":             nginxNotWritten,
	"routes[].rpc.rate_limit.by":                nginxNotWritten,
	"routes[].rpc.to":                           {Level: Supported},
	"routes[].rpc.to.<package pattern>":         {Level: Supported},
	"routes[].rpc.to.<package pattern>.service": {Level: Supported},
	"routes[].rpc.to.<package pattern>.name":    {Level: Supported},
	"routes[].name":                             {Level: Supported},
	"routes[].prefix":                           {Level: Partial, Note: nginxPathNote, Refuses: []Refusal{nginxPathRefusal}},
	"routes[].path":                             {Level: Partial, Note: nginxPathNote, Refuses: []Refusal{nginxPathRefusal}},
	// nginx's map reads a run of a regular expression that PCRE2 gave up on, past the match limit the generator writes
	// into each pattern, as no match, which the route choice refuses (RE2 runs in linear time). A pattern whose written
	// form is longer than nginx reads is refused by the generator, which measures the form it writes.
	"routes[].pattern": {Level: Partial,
		Note: "matched on the path as the client wrote it, or on the one nginx has normalised when it holds //, a dot segment, an escaped unreserved character or %2F, as Envoy matches it; in a file with a pattern route, a path holding a character Envoy escapes before it matches (\" < > ^ ` { | } or one outside ASCII) is answered 400, where Envoy matches the escape; a path PCRE2 gives up on past the 100,000 steps the generator gives each pattern, as it does for (a+)+ and a run of 20 a with another character after it, is answered 400, where Envoy's RE2 always decides whether the pattern takes it"},
	"routes[].method": {Level: Supported},
	"routes[].to":     {Level: Supported},
	// nginx's rewrite reads a ? in its replacement as the start of new arguments and adds the client's after them
	// (nginx.org, ngx_http_rewrite_module), so the path the service gets would not be the one the file writes.
	"routes[].rewrite": {Level: Partial, Note: "letters, digits and - . _ ~ / ! $ & ' ( ) * + , ; = : @ [ ] alone; a request whose path keeps an escape Envoy does not decode, such as %3B or %20, or whose target ends in an empty query, a ? with nothing after it, is answered 400 on a route with a rewrite, where Envoy sends the rewritten path on",
		Refuses: []Refusal{{Form: "holding ?",
			Instead: "nginx reads a ? in a rewrite as the start of new arguments, after which it adds the client's; write the path without ?",
			Applies: func(value, _ *tree.Node) bool { return strings.Contains(value.Text, "?") }},
			{Form: "holding a character nginx escapes again",
				Instead: "nginx escapes the new path before it sends it, so the service would receive that character escaped, where the Envoy output sends it as written; write the rewrite without it, or render it for envoy",
				Applies: func(value, _ *tree.Node) bool { return !nginxRewriteText.MatchString(value.Text) }}}},
	"routes[].auth":                nginxNotWritten,
	"routes[].retries":             nginxNotWritten,
	"routes[].rate_limit":          nginxNotWritten,
	"routes[].rate_limit.requests": nginxNotWritten,
	"routes[].rate_limit.per":      nginxNotWritten,
	"routes[].rate_limit.burst":    nginxNotWritten,
	"routes[].rate_limit.by":       nginxNotWritten,

	"rpc":                   {Level: Supported},
	"rpc.methods":           {Level: Supported},
	"rpc.methods[].service": {Level: Supported},
	"rpc.methods[].method":  {Level: Supported},
	"rpc.methods[].action":  nginxNotWritten,
	"rpc.methods[].reports": nginxNotWritten,
	"rpc.proto":             {Level: Supported},
	"rpc.proto.roots":       {Level: Supported},
	"rpc.proto.option":      {Level: Supported},

	// The fields measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 for a served request, a service's 404 and 503,
	// a host no environment names, the requests each refuses before a route, an id and a Host holding 0xE9 or a quote, a
	// failed service, a gRPC call and an answer cut short; the keys, numbers and milliseconds are written as Envoy
	// writes them (writeLog), and so is the status of an HTTP/2 stream a client cut in its headers, 0. The request's
	// start in milliseconds, which proxyfile.js can write, and the line's time with milliseconds, which a map of
	// $time_iso8601 and $msec can write, each cost a worker CPU on every line (measured on nginx 1.30.6 and 1.31.8), and
	// no variable counts an answer's body without its chunk lines, so both stay differences. An answer nginx reads
	// with no status line, one whose status has one, two or four digits or none, logs 9, where Envoy logs 2000 or 502,
	// and is logged at any status_from, as Envoy logs it. One whose status nginx reads as three other digits, 050, 2 0 0
	// or 403.1, logs that number, 50, 200 or 403, and is logged only by a status_from that takes it, where Envoy logs
	// 502 with UPE at any (measured with each form on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"log":             {Level: Partial, Note: "the time is the moment nginx writes the line, in whole seconds with the offset of nginx's time zone (+00:00 in UTC), where the Envoy output writes the request's start in milliseconds with Z; over HTTP/1.1 bytes_out counts the chunk lines of an answer nginx sends chunked, where the Envoy output counts its body alone; an answer whose status has one, two or four digits, or that has no status line, logs the status 9, where the Envoy output logs the four digits or 502; a status that starts with 0 logs its number without the zeros (050 as 50), one with spaces between its digits the digits joined (2 0 0 as 200) and one with a fraction its whole number (403.1 as 403), each written only when that number is status_from or above or sample takes the request, where the Envoy output writes 502 whatever status_from says; bytes_in counts the whole request, not its body; upstream_ms counts from the opening of the service connection, not from the request's start; there are no flags and details fields; a value the Envoy output writes null is written empty, but upstream_ms; a request the origin or upgrade check refuses logs route and upstream empty, where the Envoy output names them; a request Envoy refuses as it reads it logs its host, method, request id and protocol, which Envoy writes null and HTTP/1.1; a request id or Host holding a byte above 0x7F is logged empty"},
	"log.status_from": {Level: Supported},
	"log.sample":      {Level: Partial, Note: "the share is drawn by a hash of the request id, not by its first eight hex digits"},

	"tracing":                                     nginxNotWritten,
	"tracing.provider":                            nginxNotWritten,
	"tracing.provider.xray":                       nginxNoXRay,
	"tracing.provider.xray.agent":                 nginxNoXRay,
	"tracing.provider.xray.segment":               nginxNoXRay,
	"tracing.provider.xray.sample":                nginxNoXRay,
	"tracing.provider.xray.sample.first":          nginxNoXRay,
	"tracing.provider.xray.sample.rate":           nginxNoXRay,
	"tracing.provider.xray.sample.rules":          nginxNoXRay,
	"tracing.provider.xray.sample.rules[].host":   nginxNoXRay,
	"tracing.provider.xray.sample.rules[].method": nginxNoXRay,
	"tracing.provider.xray.sample.rules[].path":   nginxNoXRay,
	"tracing.provider.xray.sample.rules[].first":  nginxNoXRay,
	"tracing.provider.xray.sample.rules[].rate":   nginxNoXRay,
	"tracing.provider.otel":                       nginxNotWritten,
	"tracing.provider.otel.endpoint":              nginxNotWritten,
	"tracing.provider.otel.service":               nginxNotWritten,
	"tracing.provider.otel.sample":                nginxNotWritten,
	"tracing.provider.otel.sample.rate":           nginxNotWritten,
	"tracing.tags":                                nginxNotWritten,
	"tracing.tags.<tag name>":                     nginxNotWritten,
	"tracing.tags.<tag name>.env":                 nginxNotWritten,
	"tracing.tags.<tag name>.header":              nginxNotWritten,
	"tracing.tags.<tag name>.default":             nginxNotWritten,
	"tracing.path_in_url":                         nginxNotWritten,

	"the request's path normalised before a route is picked": {Level: Partial,
		Note: "a path that holds //, a dot segment, an escaped unreserved character or %2F and also an escape Envoy keeps, such as %3B, is answered 400, and so is one that holds such an escape beside a character Envoy escapes before it matches (\" < > ^ ` { | } or one outside ASCII), where Envoy normalises the path and keeps the escape; an empty query, a ? with nothing after it, after a path that needs normalising or holds such a character is answered 400, where Envoy sends the ? on; a backslash, %5C and a control character, written or escaped, are answered 400, and so is an invalid escape such as %zz, which Envoy passes on"},
	"a dot segment with a ; parameter answered 400": {Level: Partial, Note: "/a/..;/b is answered 400 where Envoy 1.40 resolves it"},
	// Over HTTP/2, :authority and a host field that agree, differ or are empty, and over HTTP/1.1 a header block that
	// gains a byte a second and a connection that sends nothing to a listener without tls, each measured on nginx 1.30.6
	// and 1.31.8 beside Envoy 1.40.2 at the full time: the 5 minutes of nginx's client_header_timeout, written in the
	// http block, end both of the last two, where Envoy answers the first 408 at its own 5 minutes and closes the second
	// after an hour, its idle time for a connection with no request. nginx sets that timer on every new connection, with
	// tls or without (ngx_http_init_connection in src/http/ngx_http_request.c, line 360 of 1.30.6 and 366 of 1.31.8);
	// Envoy's time for a connection with tls that sends nothing was not measured.
	"the request line and header names read before a route is picked": {Level: Partial,
		Note: "a tab between the method and the target, an absolute target with user information, with no Host header or with a query or a fragment right after its host, and OPTIONS * are answered 400, TRACE and CONNECT 405, and a request line with no version, HTTP/0.9's form, gets no answer, where Envoy serves the first two and TRACE, answers OPTIONS *, CONNECT and that absolute target 404, and that request line 426; HTTP/0.0 to HTTP/0.9 with headers are answered 400 and HTTP/2.0 to HTTP/9.9 on an HTTP/1 connection 505, where Envoy answers 426; a header name outside RFC 9110's token characters, or a header value holding a control byte other than a tab, is answered 400 over HTTP/1.1 and HTTP/2, where Envoy answers 400 over HTTP/1.1 and closes the connection over HTTP/2; over HTTP/2 any request trailer ends the connection, where Envoy serves the request; a header line folded onto the next and a Host whose port is past 65535 or holds a sign are answered 400, where Envoy serves them; a request holding a header name with _ that nginx reads as one of its own headers, or a second X-Forwarded-For, X-Forwarded-Proto, X-Request-Id, Content-Type, User-Agent or Referer line, is refused with 400, where Envoy reads the name as another header and joins the lines with a comma; a Connection header that names a header other than close, keep-alive, upgrade, http2-settings and te is answered 400, where Envoy removes the header it names and serves the request; a second line of authorization, if-modified-since, if-unmodified-since, if-match, if-none-match or if-range is answered 400, where Envoy joins the lines with a comma and serves the request; the lines of accept, via and accept-encoding, each a list, reach the service as separate lines, where Envoy joins them with a comma into one line, the same message written as separate lines; over HTTP/1.1 a tab before or after a header value reaches the service with the value, where Envoy removes it, the same message written another way; two Host lines are answered 400, where Envoy answers 404; over HTTP/2 a host field that differs from :authority, and an empty :authority or host field, is answered 400 with nginx's own page, where Envoy routes by :authority and closes the connection with no answer to an empty one; over HTTP/1.1 a header block still arriving after 5 minutes is ended with no answer, where Envoy answers 408 then and closes the connection a second later, and a connection that sends nothing to a listener without tls is closed after 5 minutes, where Envoy keeps it for an hour; without limits.request_headers nginx holds the Envoy output's 60 KiB and 100 header fields, with the differences the rows of its keys name"},
}, LessStrict: nginxLessStrict}

// nginxLessStrict are the ways the nginx output is less strict than the Envoy output, measured on nginx 1.30.6 and
// 1.31.8 beside Envoy 1.40.2.
var nginxLessStrict = []LessStrict{
	{Row: requestLine, Name: "a request trailer whose name is outside the token characters or whose value holds a control byte is served over HTTP/1.1",
		Measured: "A chunked request whose trailer is `x@at: 2`, or whose trailer value holds one of the bytes 0x00 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F, is served, and the service receives its body and no trailer, where Envoy answers `400`: `proxyfile.js` runs before the body arrives and never reads a trailer. nginx answers a line feed or a carriage return in a trailer value `400` itself."},
	{Row: "limits.request_headers.size", Name: "a request trailer larger than the header limit is served over HTTP/1.1",
		Measured: "A chunked request whose trailer is larger than the header limit, a trailer line of 4,093 bytes or 25 trailer lines of 200 bytes under `size: 4KiB`, or a trailer line of 61,437 bytes without `limits`, is served, and the service receives its body and no trailer, where Envoy answers `431`: nginx reads a trailer to its end with no limit on its size, and `proxyfile.js` runs before the body arrives and never reads a trailer."},
	{Row: requestLine, Name: "a header line with no colon reaches the service as a header with an empty value",
		Measured: "A request holding the line `nocolon`, with no colon, is served over HTTP/1.1, and the service receives `nocolon: `, where Envoy answers `400`. nginx stores such a line as a header with an empty value, as it stores `nocolon:`, which Envoy serves too, so neither `nginx.conf` nor `proxyfile.js` can tell the two apart."},
	{Row: requestLine, Name: "a request with no body reaches the service without content-length: 0",
		Measured: "A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`."},
	{Row: requestLine, Name: "a WebSocket upgrade a service answers without switching passes the service's body",
		Measured: "A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body."},
	// A script filter on every answer (js_header_filter with js_body_filter) turned the second and third into 502 with
	// no body on both images; it is not written, as it would run every answer's body through the script. It cannot
	// tell the first from a line with an empty value, and it can only drop an answer nginx reads with no head. nginx's
	// status line parser (ngx_http_parse_status_line in src/http/ngx_http_parse.c, its state sw_status at line 1798 of
	// nginx 1.30.6 and 1814 of 1.31.8) skips a space among the digits and takes any three with no check of their range,
	// the status text it sends on starting two bytes before the third digit (so 2 0 0 X is sent as 0 0 X), and the
	// state after them takes a . (lines 1822 and 1838, for 403.1). A line it refuses, with fewer or more digits or none,
	// goes to ngx_http_proxy_process_status_line (src/http/modules/ngx_http_proxy_module.c), which reads the answer as
	// HTTP/0.9, its refusal of one compiled out (#if 0), so no setting refuses any of them; nginx's proxy module page
	// names none.
	{Row: requestLine, Name: "a service's answer with a malformed header line or a malformed status line reaches the client",
		Measured: "A service's answer holding a header line with no colon reaches the client with it as a header with an empty value (`nocolon: `), and one whose header name holds `@` (`x@bad: 1`) or whose header value holds the byte 0x01 reaches the client with that header as the service wrote it, where Envoy answers `502` and writes an upstream-failure line with the flag `UPE`, which nginx does not write. A malformed status line reaches the client too. A status of three digits that starts with 0, `050`, `000` or `099`, reaches it as that status over HTTP/1.1 and HTTP/2, with the headers nginx writes. A status with spaces between its digits, `HTTP/1.1 2 0 0 X`, reaches an HTTP/2 client as `200` and an HTTP/1.1 client with the status line `HTTP/1.1 0 0 X`, and one with a fraction, `HTTP/1.1 403.1 X`, reaches an HTTP/2 client as `403` and an HTTP/1.1 client as the service wrote it: nginx reads three digits with spaces among them, and takes a `.` after them. A status of one or two digits, such as `5`, `20` or `99`, one of four digits, `HTTP/1.1 2000 OK`, and an answer with no status line at all, such as `hello` and a line end, reach an HTTP/1.1 client byte for byte as the service wrote them, with none of the headers nginx writes, such as `x-request-id` and `Vary`, and an HTTP/2 client as the body of an answer with the status `000`: nginx reads such an answer as one with no head and passes it whole. Envoy answers each of these `502` and writes an upstream-failure line with the flag `UPE`, but the status of four digits, which it passes as `2000` with its own headers; nginx writes no upstream-failure line for any of them, and has no setting that refuses them. nginx stores a line with no colon as it stores `nocolon:`, which Envoy passes too, so `proxyfile.js` cannot tell the two apart."},
	{Row: requestLine, Name: "over HTTP/2, empty frames in a row are served",
		Measured: "An HTTP/2 request whose HEADERS frame is followed by two empty CONTINUATION frames, or whose body comes after two empty DATA frames, is served, where Envoy takes one frame with no payload in a row and closes the connection at the second: nginx sets no such limit, and neither `nginx.conf` nor `proxyfile.js` sees a frame."},
	{Row: "timeouts.request", Name: "without timeouts.request, an answer that keeps arriving runs past 15 s",
		Measured: "Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes."},
	{Row: "log", Name: "a chunked answer its service cuts short writes no upstream-failure line",
		Measured: "A service that closes its connection after the first chunk of a chunked `200` answer reaches the client as it does through the Envoy output, which then writes the request line with the flag `UPE`, whatever `log` says, and a `gateway_upstream_failure` line; nginx writes no upstream-failure line, and the request line only when `log` takes the request by its status or its share: nginx records the answer as complete, and with no content-length there is no length to compare the bytes it received to. An answer cut short before its content-length is logged as the Envoy output logs it."},
}
