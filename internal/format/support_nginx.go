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

	"admin": {Level: Partial, Note: "answers /ready alone; every other path of the admin port is 404"},

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

	"client_address": {Level: Supported},

	"limits":                       nginxNotWritten,
	"limits.connections":           nginxNotWritten,
	"limits.request_headers":       nginxNotWritten,
	"limits.request_headers.size":  nginxNotWritten,
	"limits.request_headers.count": nginxNotWritten,

	"timeouts": {Level: Supported},
	// nginx's proxy timeouts are set only between two successive reads or writes, not for a whole response (nginx.org,
	// ngx_http_proxy_module), so no directive holds a time for a whole request, and none, which sets no limit, is the
	// one value nginx writes as the file says.
	"timeouts.request": {Level: Partial,
		Note: "without the key nginx answers 504 to a service silent for 15 s after the request, as the Envoy output does; with none, nginx ends a request after 5 minutes with nothing read or written, as Envoy does",
		Refuses: []Refusal{{Form: "a time",
			Instead: "nginx has no limit on a whole request (its time limits count the time between two reads or writes); write timeouts.request: none, or render it for envoy",
			Applies: func(value, _ *tree.Node) bool { return value.Text != "none" }}}},
	"timeouts.idle":          nginxNotWritten,
	"timeouts.upstream_idle": nginxNotWritten,

	"services":                            {Level: Supported},
	"services.<service name>":             {Level: Supported},
	"services.<service name>.http":        {Level: Supported},
	"services.<service name>.grpc":        {Level: Supported},
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
	// The Envoy output joins two Origin lines with a comma before its patterns read them, which no browser sends.
	"origins.<environment name>.patterns": {Level: Partial,
		Note: "an Origin holding a comma is answered 403, where the Envoy output can match two Origin lines joined with a comma"},

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

	"log":             nginxNotWritten,
	"log.status_from": nginxNotWritten,
	"log.sample":      nginxNotWritten,

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
	"the request line and header names read before a route is picked": {Level: Partial,
		Note: "a tab between the method and the target, an absolute target with user information, with no Host header or with a query or a fragment right after its host, and OPTIONS * are answered 400, TRACE and CONNECT 405, and a request line with no version, HTTP/0.9's form, gets no answer, where Envoy serves the first two and TRACE, answers OPTIONS *, CONNECT and that absolute target 404, and that request line 426; HTTP/0.0 to HTTP/0.9 with headers are answered 400 and HTTP/2.0 to HTTP/9.9 on an HTTP/1 connection 505, where Envoy answers 426; a header name outside RFC 9110's token characters, or a header value holding a control byte other than a tab, is answered 400 over HTTP/1.1 and HTTP/2, where Envoy answers 400 over HTTP/1.1 and closes the connection over HTTP/2; over HTTP/2 any request trailer ends the connection, where Envoy serves the request; a header line folded onto the next and a Host whose port is past 65535 or holds a sign are answered 400, where Envoy serves them; a request holding a header name with _ that nginx reads as one of its own headers, or a second X-Forwarded-For, X-Forwarded-Proto, X-Request-Id, Content-Type, User-Agent or Referer line, is refused with 400, where Envoy reads the name as another header and joins the lines with a comma; a Connection header that names a header other than close, keep-alive, upgrade, http2-settings and te is answered 400, where Envoy removes the header it names and serves the request; a second line of authorization, if-modified-since, if-unmodified-since, if-match, if-none-match or if-range is answered 400, where Envoy joins the lines with a comma and serves the request; the lines of accept, via and accept-encoding, each a list, reach the service as separate lines, where Envoy joins them with a comma into one line, the same message written as separate lines; two Host lines are answered 400, where Envoy answers 404; a request line over 8 KiB is answered 414 and a header line over 8 KiB 400, and over HTTP/2 a header field over 8 KiB as HPACK sends it ends the connection, where Envoy reads up to 60 KiB of them"},
}, LessStrict: nginxLessStrict}

// nginxLessStrict are the ways the nginx output is less strict than the Envoy output, measured on nginx 1.30.6 and
// 1.31.8 beside Envoy 1.40.2.
var nginxLessStrict = []LessStrict{
	{Row: requestLine, Name: "a request trailer whose name is outside the token characters or whose value holds a control byte is served over HTTP/1.1",
		Measured: "A chunked request whose trailer is `x@at: 2`, or whose trailer value holds one of the bytes 0x00 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F, is served, and the service receives its body and no trailer, where Envoy answers `400`: `proxyfile.js` runs before the body arrives and never reads a trailer. nginx answers a line feed or a carriage return in a trailer value `400` itself."},
	{Row: requestLine, Name: "a header line with no colon reaches the service as a header with an empty value",
		Measured: "A request holding the line `nocolon`, with no colon, is served over HTTP/1.1, and the service receives `nocolon: `, where Envoy answers `400`. nginx stores such a line as a header with an empty value, as it stores `nocolon:`, which Envoy serves too, so neither `nginx.conf` nor `proxyfile.js` can tell the two apart."},
	{Row: requestLine, Name: "a request with no body reaches the service without content-length: 0",
		Measured: "A `POST` with no body reaches the service with no `Content-Length`, over HTTP/1.1 and HTTP/2, where Envoy adds `content-length: 0`."},
	{Row: requestLine, Name: "a WebSocket upgrade a service answers without switching passes the service's body",
		Measured: "A WebSocket upgrade a service answers `200` without switching reaches the client with the service's body, where Envoy passes the status and headers with no body."},
	{Row: "timeouts.request", Name: "without timeouts.request, an answer that keeps arriving runs past 15 s",
		Measured: "Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes."},
}
