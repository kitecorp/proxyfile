package format

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/model"
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
	// haproxyRemovalWords say why a removal of HAProxyNotRemovable is refused and what to write instead.
	haproxyRemovalWords = "the Envoy output sends the body chunked once Content-Length is removed, and leaves a WebSocket upgrade unanswered once Connection or Upgrade is removed, which HAProxy cannot write; write the other names in full, or a longer prefix, or render it for envoy"
	// haproxyRemovalPrefix refuses a value holding a * prefix that covers a header of HAProxyNotRemovable.
	haproxyRemovalPrefix = Refusal{Form: "holding a * prefix that covers content-length, connection or upgrade", Instead: haproxyRemovalWords,
		Applies: func(value, _ *tree.Node) bool { _, prefix := coveredBy(value, HAProxyNotRemovable); return prefix }}
)

// HAProxyFramedBy are the headers HAProxy frames or routes a request by, so the auth check's answer never sets them on
// the request it passes on: a check service must never be able to change how HAProxy frames that request. Envoy 1.40.2
// set each from the check's answer when auth.accept named it in full or under a prefix (measured), so the declaration
// refuses a name of them written in full, and HAProxy's script never sets one (internal/haproxy/script.go).
var HAProxyFramedBy = []string{"content-length", "transfer-encoding", "host", "te", "connection", "upgrade"}

// HAProxyNotRemovable are the headers Envoy 1.40.2 removes, in full or under a prefix, in a way HAProxy cannot write:
// it sent the body chunked once Content-Length was gone, where HAProxy sent content-length: 0 and lost the body, and it
// left a WebSocket upgrade unanswered once Connection or Upgrade was gone, where HAProxy served it as a plain request
// (measured on HAProxy 3.2.26 and 3.4.7). The declaration refuses them on remove_from_clients and auth.accept, whose
// names are removed from clients too, and the HAProxy generator refuses the same forms.
var HAProxyNotRemovable = []string{"content-length", "connection", "upgrade"}

// coveredBy reports whether a list of header names holds an entry written in full that is one of names, and whether
// it holds a * prefix that covers one of them, as model.Header.Covers reads an entry.
func coveredBy(value *tree.Node, names []string) (whole, prefix bool) {
	for _, item := range value.Items {
		text, isPrefix := strings.CutSuffix(item.Text, "*")
		entry := model.Header{Name: text, Prefix: isPrefix}
		if slices.ContainsFunc(names, func(name string) bool { return entry.Covers(model.Header{Name: name}) }) {
			whole, prefix = whole || !isPrefix, prefix || isPrefix
		}
	}
	return whole, prefix
}

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

	// Under forwarded the client's own X-Request-Id is kept and echoed on every answer, in the 1,024 bytes HAProxy keeps
	// for the lines it adds, beside an echoed Origin: an id that outgrew them got 500 after the service did the work,
	// so the file refuses one longer than the room left before the check and the service. HAProxy's HTTP/2 decoder drops
	// the spaces of a value, so X-Forwarded-Proto of spaces alone reads as empty. Measured on HAProxy 3.2.26 and 3.4.7
	// beside Envoy 1.40.2 (haproxy.cfg.tmpl, idRoom in generate.go).
	"client_address": {Level: Partial,
		Note: "under forwarded, a client's own X-Request-Id longer than 980 bytes, or than 908 bytes less an echoed Origin and cors.expose_headers, is answered 400 before the check and the service, where the Envoy output serves it: HAProxy echoes the id in the 1,024 bytes it keeps for the lines it adds to an answer; over HTTP/2 an X-Forwarded-Proto of spaces alone is answered 404, where the Envoy output serves it"},

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

	"services":                     {Level: Supported},
	"services.<service name>":      {Level: Supported},
	"services.<service name>.http": {Level: Supported},
	// A gRPC service's connection is HTTP/2, which HAProxy 3.2.26 and 3.4.7 did not bound by timeout connect: a
	// service that never took the connection ended at timeout server, 15.02 s (txn.sess_term_state sH, measured). A
	// request that is not a gRPC call, to a gRPC service that reset its stream or answered in HTTP/1.1, got the file's
	// empty 502, where Envoy writes its text (measured on 3.2.26 and 3.4.7 beside Envoy 1.40.2, over HTTP/2).
	"services.<service name>.grpc": {Level: Partial,
		Note: "a service that never takes the connection is answered after 15 s, where Envoy answers after its 5 s connect timeout, a gRPC call with 200 and grpc-status 14 as by Envoy and any other request with 504 and upstream request timeout, where Envoy's is an empty 502: HAProxy does not bound an HTTP/2 connection to a service by its connect timeout, and the 15 s a service that sends nothing gets end the wait (measured without timeouts.request); a gRPC call HAProxy alone refuses before any service, such as one with a client's X-Request-Id of 1,000 bytes under forwarded, gets HAProxy's plain answer, here 400, where Envoy serves the call; a request that is not a gRPC call, to a gRPC service that resets its stream or answers in HTTP/1.1, is answered 502 with no body, where the Envoy output answers the stream reset 503 and the HTTP/1.1 answer 502, each with the text upstream connect error or disconnect/reset before headers. reset reason: remote reset, or protocol error, as text/plain"},
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

	// The check is a Lua action in proxyfile.lua that asks the check service through a TCP listen section of the file.
	// Each limit below is the script's own: HAProxy gives a Lua action no HTTP client of its own, so the script
	// reads the check's answer itself, and keeps what it holds within the request buffer HAProxy hands it. Every note
	// was measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2.
	"auth": {Level: Supported},
	// A Lua reply past about 16,100 bytes became HAProxy's own 500, and the lines HAProxy adds to an answer share the
	// buffer, so a denial is copied only within 15,100 bytes counted as HAProxy stores it; a line within 16,383 bytes, a
	// head and a trailer section within 81,920 bytes, as Envoy reads them, and a 200's body within 1 MiB, read and
	// dropped, bound the time and the bytes the script reads. The reader passes only what RFC 9112 writes, so a
	// malformed answer is never taken for a pass.
	"auth.ask": {Level: Partial,
		Note: "an answer of the check HAProxy cannot read whole is answered 403, where Envoy passes it on: a denial whose header lines and body pass 15,100 bytes, each line counted as its name, its value and 8 bytes, a header line of 16,383 bytes or more, a 200 whose body passes 1 MiB, a head of more than 100 header lines, folded lines not counted, with a name on two of them, where Envoy counts a name it keeps inline, such as date, once, and a status of 101, which a gRPC call gets as grpc-status 7, where Envoy gives 2; an answer in HTTP/1.2, a chunk extension or a space after a chunk size, two spaces or a tab after the version, a lone CR in a header value, a status line of 16,383 bytes or more, or a line whose CR falls at a multiple of 16,384 bytes into the answer (measured at 16,384 and 81,920), which HAProxy's socket hands to the script with the CR kept, is answered 403, where Envoy reads it; each head of the answer and its trailer section are read to 81,920 bytes, Envoy's bound, each line counted with a CR and an LF, so lines that end in LF alone reach it sooner; a status outside 100 to 599 or a status line in HTTP/2.0 is answered 403, where Envoy passes the check's status on; the check is sent connection: close on a connection of its own, where Envoy sends no Connection header and keeps its connections to the check open; after a check's 304 HAProxy closes the client's connection, so a request the client sends next on it gets no answer, where Envoy answers it"},
	"auth.timeout": {Level: Supported},
	"auth.enabled": {Level: Supported},
	// The script goes on with the unchecked mark only for an answer it cannot have misread: a body read that fails, an
	// answer that ends after the deadline and a 101st header line with each name once; any other answer it cannot read
	// whole is 403.
	"auth.when_unavailable": {Level: Partial,
		Note: "with allow, an answer that breaks off inside its header lines, by a close or by passing auth.timeout, a 200 or a denial whose body never ends, an answer whose head or trailer section passes 81,920 bytes, a head of more than 100 header lines with a name on two of them, such as set-cookie, and an answer HAProxy cannot read as RFC 9112 writes one (a Content-Length that is not one line of digits, a control byte in a header value, a status line that does not end) are answered 403, where Envoy serves the request with the unchecked mark, x-envoy-auth-failure-mode-allowed: true; a 200 whose chunk-size line ends in a bare LF, the last chunk's included, is read as allowed, where Envoy reads it as no answer, and answers 403 with deny and serves the request with the unchecked mark with allow"},
	// HAProxy reads an HTTP/2 value of spaces alone as empty, so the ask condition cannot tell it from an empty value,
	// and asks both, where Envoy asks the first alone.
	"auth.only_with_header": {Level: Partial,
		Note: "over HTTP/2 a request that carries the header with every value empty is asked, where Envoy does not ask it: HAProxy reads an HTTP/2 value of spaces alone as empty, and Envoy asks one, so HAProxy asks both"},
	// Measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2 with x-country: caf and the byte 0xE9, and with an
	// overlong form, a surrogate, a code point past U+10FFFF and a sequence cut at the value's end; the file's own lines
	// are unchanged.
	"auth.send": {Level: Partial,
		Note: "the check receives each byte of 0x80 or more that is not part of well-formed UTF-8 in a header it is sent as the client sent it, where Envoy writes a ! for each such byte"},
	// A header that does not fit in the request buffer is dropped without an error, so the script reads each back and
	// refuses the request when one is missing. A header HAProxy frames or routes a request by is never set from the
	// answer, so naming one in full is refused, and a prefix that covers it leaves it unset.
	"auth.accept": {Level: Partial,
		Note: "an accepted header that does not fit beside the request's own header lines in HAProxy's request buffer is answered 403, where Envoy sets it on the request (measured up to 50,000 bytes; it answered 500 at 70,000): beside a request of 118 bytes, one of 8,000 or 15,000 bytes is set by both versions and one of 15,843 bytes or more (3.4.7: 15,851) is answered 403, a bound the request's own header lines lower by as many bytes as they hold; an accepted header whose line reaches 16,383 bytes is answered 403 by auth.ask's line bound first; a * prefix never sets transfer-encoding, host or te from the check's answer, where Envoy sets each; a folded header line of the check's answer reaches the service joined to the value before it by one space, where Envoy keeps the spaces and tabs of both lines and drops the line end; a request the accepted headers take past 100 header fields, as Envoy counts them with its pseudo-headers and the x-request-id and x-forwarded-proto it sets, reaches the service, where Envoy answers 500",
		Refuses: []Refusal{{Form: "naming content-length, transfer-encoding, host, te, connection or upgrade in full",
			Instead: "HAProxy frames or routes a request by these headers, so the check's answer never sets them; remove the name from auth.accept",
			Applies: func(value, _ *tree.Node) bool { whole, _ := coveredBy(value, HAProxyFramedBy); return whole }},
			haproxyRemovalPrefix}},
	// The check is told the scheme of the client's connection (measured on HAProxy 3.2.26 and 3.4.7 beside Envoy
	// 1.40.2, :scheme http over TLS and https without).
	"auth.send_request": {Level: Partial,
		Note: "over HTTP/2 a request whose :scheme differs from its connection's scheme is told in x-forwarded-proto the connection's, where Envoy tells it the :scheme"},
	"auth.jwt":                      haproxyNoJWT,
	"auth.jwt.issuer":               haproxyNoJWT,
	"auth.jwt.audiences":            haproxyNoJWT,
	"auth.jwt.keys":                 haproxyNoJWT,
	"auth.jwt.ca":                   haproxyNoJWT,
	"auth.jwt.header":               haproxyNoJWT,
	"auth.jwt.optional":             haproxyNoJWT,
	"auth.jwt.claims":               haproxyNoJWT,
	"auth.jwt.claims.<header name>": haproxyNoJWT,

	// A * prefix leaves Host and Transfer-Encoding, and Transfer-Encoding written in full is left, as Envoy leaves them;
	// what HAProxy cannot remove as Envoy does is refused (HAProxyNotRemovable).
	"remove_from_clients": {Level: Partial, Note: "names and * prefixes that cover none of content-length, connection and upgrade",
		Refuses: []Refusal{{Form: "holding content-length, connection or upgrade, in full or under a * prefix", Instead: haproxyRemovalWords,
			Applies: func(value, _ *tree.Node) bool {
				whole, prefix := coveredBy(value, HAProxyNotRemovable)
				return whole || prefix
			}}}},

	"cors":               {Level: Supported},
	"cors.allow_methods": {Level: Supported},
	// http-request return leaves out a header whose value comes out empty: measured on HAProxy 3.4.7, a preflight with
	// no Access-Control-Request-Headers was answered without access-control-allow-headers.
	"cors.allow_headers":  {Level: Partial, Note: "an empty Access-Control-Allow-Headers is left out of a preflight's answer, where Envoy sends it empty"},
	"cors.expose_headers": {Level: Supported},
	"cors.max_age":        {Level: Supported},

	"origins":                          {Level: Supported},
	"origins.<environment name>":       {Level: Supported},
	"origins.<environment name>.hosts": {Level: Supported},
	// HAProxy adds the lines of an answer (x-request-id, Access-Control-Allow-Origin, Access-Control-Expose-Headers,
	// Vary) from the 1,024 bytes it keeps for them, so an echoed Origin longer than the room left is refused before the
	// check and the service, where HAProxy answered 500 after the service did the work.
	"origins.<environment name>.origins": {Level: Partial,
		Note: "an admitted Origin longer than 872 bytes less the length of cors.expose_headers is answered 403 with the body Invalid CORS request before the check and the service, where the Envoy output echoes it: HAProxy has 1,024 bytes for the lines it adds to an answer"},
	"origins.<environment name>.patterns": {Level: Partial,
		Note: "an admitted Origin longer than 872 bytes less the length of cors.expose_headers is answered 403 with the body Invalid CORS request before the check and the service, where the Envoy output echoes it: HAProxy has 1,024 bytes for the lines it adds to an answer"},

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
	"routes[].auth":                {Level: Supported},
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
	"rpc.methods[].action":  {Level: Supported},
	"rpc.methods[].reports": {Level: Supported},
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
	// measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2. In the query it decodes the escape of an unreserved
	// character too, for the service and the check alike, where Envoy keeps the query as written (measured with
	// /products/1?q=%41, /catalog/../products/1?q=%41, /products/%41?q=%41, /products/1?q=%7e and
	// /products/1?q=%2F%26%3d on both images).
	"the request's path normalised before a route is picked": {Level: Partial,
		Note: "an escape of an unreserved character in the query, such as ?q=%41 or ?q=%7e, reaches the service and the check decoded, as ?q=A and ?q=~, with or without a dot segment in the path, where Envoy passes the query as the client wrote it; an escape of a reserved character, such as %2F, %26 or %3d, reaches both as written, as through Envoy; a path that holds an empty segment before a dot-dot segment, such as /catalog//../products/1, with / written or escaped, over HTTP/1.1 and HTTP/2, takes the route of the path with its slashes merged first, /products/1, where Envoy resolves the dot-dot segment first and routes /catalog/products/1"},
	// HAProxy cannot resolve such a segment, so the file asks the check about a request with one whatever its route, as
	// Envoy asks about one escaped once, which it does not resolve either: the check's denial is passed on as through
	// Envoy, and a request it lets through is answered 400 (measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2).
	"a dot segment with a ; parameter answered 400": {Level: Partial,
		Note: "a dot segment whose ; parameter is written with dots, such as /a/..;/b, is answered 400, and a gRPC call 200 with grpc-status 13, where Envoy 1.40 drops the parameter and resolves the segment; with the auth check, such a request is asked about whatever route its resolved path takes, and gets the check's denial or, once the check lets it through, 400, where Envoy asks only when that route is checked and, with send_request, sends the check the resolved path; after the 400 of such a segment with the ; written %3B, which both answer, HAProxy keeps the connection open, where Envoy closes it"},
	// The file answers 400 to what Envoy refuses before it reads a request and HAProxy's parsers pass, and to what Envoy
	// serves in a way HAProxy cannot write; its parsers refuse the rest. Each answer the note names was measured on
	// HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2; the two images answered alike where the note names no version.
	"the request line and header names read before a route is picked": {Level: Partial,
		Note: "over HTTP/1.1 an absolute target is answered 400, where Envoy routes it by its host and sends the path alone with that host as Host, or answers 404 when no environment has the host, and OPTIONS * and CONNECT are answered 400, where Envoy answers 404; a request line with no version, HTTP/0.9's form, is answered 400, where Envoy answers 426; a Connection token other than close, keep-alive, upgrade, HTTP2-Settings and te is answered 400, where Envoy removes the header the token names and serves the request; ten Connection elements or more, an empty one counted but for an empty last one, are answered 400, where Envoy counts no empty element and serves a token with ten; an h2c upgrade is answered 400, where Envoy serves it as a plain request; a header line folded onto the next, and a header value that holds three spaces or tabs in a row, are answered 400, where Envoy serves them; over HTTP/2 a cookie field that starts with two spaces or tabs, as one of spaces alone does, or ends with three is answered 400 too, where Envoy serves it, as HAProxy keeps the spaces around a cookie field's value and drops them around any other field's; a WebSocket upgrade with no Sec-WebSocket-Key is answered 400, where Envoy passes it on, or answers 403 to an origin it does not admit; more than 100 header lines are answered 431 or, from 102, 400, where Envoy answers 431, and 101 lines or more of a header Envoy joins into one line are answered 400, where Envoy serves them; two Host lines of different hosts, and a Host that holds a comma, are answered 400, where Envoy answers 404; Transfer-Encoding: chunked on two lines is answered 400, and Transfer-Encoding: gzip, chunked 422, where Envoy answers 501; a request line and header lines of more than 15,277 bytes together, counted as sent with their line ends, Host alone beside the line and 4 bytes fewer for each further header line (3.4.7: 8 bytes more), are answered 431, and a request line of 16,319 bytes or more (3.4.7: 16,327) 414, where Envoy serves both up to its default limit of 60 KiB; over HTTP/2 a request with 97 header fields or more is answered 400, where Envoy resets its stream, and so is one with fewer whose cookies, counted one a field, reach 97, and one with 97 fields or more of a header Envoy joins, which Envoy serves, is answered 400 or, with 120, its stream reset; a WebSocket, an extended CONNECT, is answered 400, where Envoy closes the connection; a path whose escaped form outgrows HAProxy's buffer, such as one of 7000 bytes above 0x7F, is answered 400 by HAProxy 3.4.7 and 500 by 3.2.26, where Envoy serves it; a header or trailer name outside RFC 9110's token characters resets the stream, where Envoy closes the connection; a # in :path resets the stream, where Envoy answers 400; and a request with neither :authority nor host is answered 404, where Envoy closes the connection; over HTTP/1.1 a request trailer whose name is outside those characters ends the connection with no answer when the body comes after the headers, where Envoy answers 400; QUERY over HTTP/1.1 is sent on, where Envoy answers 400"},
}, LessStrict: haproxyLessStrict}

// haproxyLessStrict are the ways the HAProxy output is less strict than the Envoy output. HAProxy's parsers decide
// each before any rule of the file reads the request, and its timeout counts the time without data, so the file can
// close none of them: the file refuses a control byte in a header value with a rule, but no fetch reads a request
// trailer, which option http-drop-request-trailers removes before the service. Each was measured on HAProxy 3.2.26 and
// 3.4.7 beside Envoy 1.40.2, the two images alike.
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
	{Row: requestLine, Name: "a trailer value holding a control byte is served",
		Measured: "A request trailer value holding one of the bytes 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F is served, the service receiving the body and no trailer, where Envoy answers `400` over HTTP/1.1 and closes the connection over HTTP/2. A header value holding one is answered `400` over HTTP/1.1, as by Envoy, and its stream is reset over HTTP/2, where Envoy closes the connection; HAProxy refuses 0x00, a line feed and a carriage return in either value, with `400` over HTTP/1.1 and a reset stream over HTTP/2."},
	{Row: "timeouts.request", Name: "without timeouts.request, an answer that keeps arriving runs past 15 s",
		Measured: "Without `timeouts.request`, a service that keeps sending runs past 15 s, where the Envoy output ends a request 15 s after it starts: HAProxy's `timeout server` counts the time without data (on HAProxy 3.4.7, a service that sent one byte a second for 10 s under a 2 s `timeout server` was served)."},
}
