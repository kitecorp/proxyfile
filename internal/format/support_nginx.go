package format

import (
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// nginx's refusals that several keys share. Each is named for nginx because every target declares its keys in this
// package, and another target's refusals say what that proxy offers instead.
var (
	// nginxPassiveCheck is health's own declaration: nginx's own passive check, the parameters max_fails and fail_timeout
	// of the upstream module's server (nginx.org, ngx_http_upstream_module and ngx_stream_upstream_module). A backend
	// whose attempts fail max_fails times within fail_timeout, 10 s by default, is taken out for fail_timeout; an error,
	// a timeout and an invalid header count under proxy_next_upstream off and grpc_next_upstream off too, which the file
	// writes, and in the check relay's stream block an error or a timeout while connecting; a group's one server is
	// never taken out, so a service with one address writes no check (upstreamLines).
	nginxPassiveCheck = Support{Level: Partial,
		Note:    "nginx's own passive check (max_fails): a backend whose requests fail unhealthy times within 10 s, by an error, a timeout or an invalid answer, takes no request for 10 s; nginx sends no check request of its own, and keeps a service's one backend in use",
		Refuses: []Refusal{nginxHealthOnManyAddresses}}
	// nginxHealthNotRead is the declaration of health's keys the passive check does not read: open-source nginx sends no
	// check request of its own, as health_check is NGINX Plus's (nginx.org, ngx_http_upstream_hc_module, "available as
	// part of our commercial subscription").
	nginxHealthNotRead = Support{Level: Partial,
		Note: "not read: open-source nginx sends no check request (health_check is NGINX Plus's), so its passive check reads the service's own requests"}
	nginxNoJWT = Support{Level: Refused,
		Note: "nginx open source cannot verify a token (nginx Plus's auth_jwt); verify it in the service, or render it for envoy"}
	// The official images hold one tracer, ngx_otel_module, in the images tagged -otel, which exports OTLP alone (the
	// modules folder of nginx 1.31.8-otel holds no other).
	nginxNoXRay = Support{Level: Refused,
		Note: "nginx has no X-Ray tracer; write the otel provider and send its spans to an OpenTelemetry Collector with the AWS X-Ray exporter, or render it for envoy"}
	// nginxNoZone is what nginx says of a backend address that carries an IPv6 zone, such as fe80::1%en0: the reader
	// takes it as an IP address (netip.ParseAddr), and nginx refuses to load it in an upstream ("invalid IPv6 address
	// in upstream", measured on nginx 1.30.6 and 1.31.8).
	nginxNoZone = "nginx reads no zone in a backend's address; give the service an address without one"
)

// nginxHealthOnManyAddresses refuses health on a service with more than 1,000 addresses: the passive check keeps each
// of the service's upstreams, and the check relay's when auth.ask names it, in a zone of 1 MiB, which nginx refuses to
// load when the servers do not fit ("ngx_slab_alloc() failed: no memory in upstream zone"). Measured with nginx -t on
// nginx 1.30.6 and 1.31.8, for a checked service with its http and stream upstreams each in a zone: 1,000 and 1,250
// addresses loaded, and 1,500 did not. A DNS name is one address here, and its answer has a bound of its own (the
// address key's note).
var nginxHealthOnManyAddresses = Refusal{Form: "on a service with more than 1,000 addresses",
	Instead: "nginx's passive check keeps a service's backends in a zone of 1 MiB, which holds 1,000 of them; give the service 1,000 addresses or fewer, remove health, or render it for envoy",
	Applies: func(value, proxyfile *tree.Node) bool {
		services := proxyfile.Get("services")
		if services == nil {
			return false
		}
		for _, entry := range services.Entries {
			if entry.Value.Get("health") != value {
				continue
			}
			addresses := entry.Value.Get("addresses")
			return addresses != nil && len(addresses.Items) > 1000
		}
		return false
	}}

// nginxNameWithoutResolver refuses a service's DNS name in a file with no dns.resolver: nginx looks a name up while it
// runs only through its resolver directive, never through the host's configuration, and refuses to load an upstream
// that resolves a name with none ("no resolver defined to resolve names at run time", measured on nginx 1.30.6 and
// 1.31.8).
var nginxNameWithoutResolver = Refusal{Form: "a DNS name with no dns.resolver",
	Instead: "nginx looks a name up only through the name server it is given; write dns.resolver",
	Applies: func(value, proxyfile *tree.Node) bool {
		_, err := netip.ParseAddr(value.Text)
		return err != nil && proxyfile.Get("dns").Get("resolver") == nil
	}}

// nginxHashOnCheck refuses balance: hash on the service auth.ask names, whose upstream the relay in the stream block
// uses too. The variables a hash on a header or on the script's client key reads are the http block's, so nginx refuses
// to load either there ("unknown variable"); under client_address connection it loads, but the relay's client is nginx
// itself on a socket file, not the request's client (measured on nginx 1.30.6 and 1.31.8). It is refused whether auth
// is enabled or not, so a file stays valid when the check is turned on.
var nginxHashOnCheck = Refusal{Form: "hash on the service auth.ask names",
	Instead: "nginx's check relay cannot read the check's headers to hash them; write another balance on the service",
	Applies: func(value, proxyfile *tree.Node) bool {
		checked, _, _ := strings.Cut(proxyfile.Get("auth").GetText("ask"), " ")
		return value.Text == "hash" && checked != "" && proxyfile.Get("services").Get(checked).Get("balance") == value
	}}

// nginxNoWholeRate refuses a rate limit whose requests in per make no whole number a second or a minute: the rate of
// limit_req_zone is a whole number of requests a second or a minute, in no other unit (nginx.org,
// ngx_http_limit_req_module), and a rate rounded to one would admit other requests than the Envoy output. The rate
// depends on requests too, which Applies reads in the rate limit that holds this per.
var nginxNoWholeRate = Refusal{Form: "giving no whole number of requests a second or a minute",
	Instead: "nginx's rate is a whole number of requests a second or a minute; write requests and per so that one of them is whole",
	Applies: func(value, proxyfile *tree.Node) bool {
		requests, err := strconv.ParseInt(rateLimitHolding(value, proxyfile).GetText("requests"), 10, 64)
		millis, ok := model.ParseTime(value.Text)
		_, _, whole := model.WholeRate(requests, millis)
		return err == nil && ok && millis > 0 && !whole
	}}

// rateLimitHolding returns the rate limit of an entry of routes, or of its rpc, whose per is the node per, nil when
// none is: a refusal is given one key's value, and the rate a per gives depends on requests beside it.
func rateLimitHolding(per, proxyfile *tree.Node) *tree.Node {
	routes := proxyfile.Get("routes")
	if routes == nil {
		return nil
	}
	for _, entry := range routes.Items {
		for _, limit := range []*tree.Node{entry.Get("rate_limit"), entry.Get("rpc").Get("rate_limit")} {
			if limit.Get("per") == per {
				return limit
			}
		}
	}
	return nil
}

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

// NginxNotAccepted are the names auth.accept may not name on nginx, sorted: every header the route's location writes
// itself (writeLocation in internal/nginx: Host, X-Request-Id, X-Forwarded-Proto, Origin, X-Forwarded-For,
// x-envoy-external-address, Upgrade, Connection, HTTP2-Settings and TE, each header the Envoy output removes, which the
// location writes empty, X-Forwarded-For, x-envoy-external-address and generator.RemovedByEnvoyFacingClients with
// client_address connection alone, each refused in both modes so that a list stays valid when the mode changes, and
// with auth.ask the unchecked mark and the two question headers, which it sets or writes empty), and content-length and
// transfer-encoding, which nginx writes itself as it frames the body it passes on. The Envoy output sets the mark from
// a check's 200 that carries it when auth.accept names it, and sends the service neither question header whatever the
// check answers (measured on Envoy 1.40.2). nginx sets an accepted header with a line of the route's location, beside
// the location's own line for the same name, and a check service must never change how nginx frames or routes the
// request it passes on, nor send the service a header the location keeps from it. Envoy 1.40.2 set a framing header
// from the check's answer when auth.accept named it (measured for HAProxyFramedBy), so the declaration refuses each of
// these by name. The format reads header names in lower case only, so each is compared as written.
var NginxNotAccepted = []string{"connection", "content-length", "host", "http2-settings", "origin", "proxy-connection", "te",
	"transfer-encoding", "upgrade", "x-envoy-auth-failure-mode-allowed", "x-envoy-decorator-operation",
	"x-envoy-downstream-service-cluster", "x-envoy-downstream-service-node", "x-envoy-expected-rq-timeout-ms",
	"x-envoy-external-address", "x-envoy-force-trace", "x-envoy-hedge-on-per-try-timeout", "x-envoy-internal",
	"x-envoy-ip-tags", "x-envoy-max-retries", "x-envoy-original-host", "x-envoy-original-path", "x-envoy-original-url",
	"x-envoy-retriable-header-names", "x-envoy-retriable-status-codes", "x-envoy-retry-grpc-on", "x-envoy-retry-on",
	"x-envoy-upstream-alt-stat-name", "x-envoy-upstream-rq-per-try-timeout-ms", "x-envoy-upstream-rq-timeout-alt-response",
	"x-envoy-upstream-rq-timeout-ms", "x-envoy-upstream-stream-duration-ms", "x-forwarded-client-cert", "x-forwarded-for",
	"x-forwarded-proto", "x-gateway-action", "x-gateway-reports", "x-request-id"}

// holdsPrefix reports whether a list of header names holds a * prefix. nginx sets and empties a header only by a name
// its configuration writes (proxy_set_header), and has no directive that takes every header under a prefix.
func holdsPrefix(value *tree.Node) bool {
	return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return strings.HasSuffix(item.Text, "*") })
}

// NginxRemovedFromClients are the headers the nginx output removes from every client's request itself, sorted: each
// header the Envoy output removes (generator.RemovedByEnvoy), the unchecked mark and the two question headers; and
// NginxRemovedFacingClients are those it removes too with client_address connection alone
// (generator.RemovedByEnvoyFacingClients). The Envoy output removes each before its check's only_with_header condition
// reads the request, so a client that sends one is never asked about, where nginx's handler reads the client's own
// copy and asks; with forwarded both pass the second list on and ask by it (measured on Envoy 1.40.2 beside nginx
// 1.30.6, each name sent with a token the check denies). The format reads header names in lower case only.
var (
	NginxRemovedFromClients = []string{"proxy-connection", "x-envoy-auth-failure-mode-allowed", "x-envoy-expected-rq-timeout-ms",
		"x-envoy-force-trace", "x-envoy-hedge-on-per-try-timeout", "x-envoy-internal", "x-envoy-ip-tags", "x-envoy-max-retries",
		"x-envoy-original-url", "x-envoy-retriable-header-names", "x-envoy-retriable-status-codes", "x-envoy-retry-grpc-on",
		"x-envoy-retry-on", "x-envoy-upstream-alt-stat-name", "x-envoy-upstream-rq-per-try-timeout-ms",
		"x-envoy-upstream-rq-timeout-alt-response", "x-envoy-upstream-rq-timeout-ms", "x-envoy-upstream-stream-duration-ms",
		"x-forwarded-client-cert", "x-gateway-action", "x-gateway-reports"}
	NginxRemovedFacingClients = []string{"x-envoy-decorator-operation", "x-envoy-downstream-service-cluster",
		"x-envoy-downstream-service-node", "x-envoy-original-host", "x-envoy-original-path"}
)

// NginxRemovedTraced are the headers the nginx output removes from every client's request with the otel provider,
// under either client_address, sorted, which the nginx generator reads too: a client's copy would name the span or
// fill its downstream_cluster, and reach the service. The Envoy output removes them with otel (downstreamHeaders and
// decoratorHeader in internal/envoy) in the same early header removal as NginxRemovedFromClients
// (downstreamRemoved in internal/envoy), which Envoy runs before every filter, the check's
// only_with_header condition among them, so it never asks the check about a client's copy, where nginx's handler reads
// the client's own copy and asks (asked in proxyfile.js): read from the code, where the other names' removal was
// measured. With client_address connection they are among NginxRemovedFacingClients already.
var NginxRemovedTraced = []string{"x-envoy-decorator-operation", "x-envoy-downstream-service-cluster", "x-envoy-downstream-service-node"}

// nginxOnlyWithListed refuses auth.only_with_header naming a header the proxy removes from clients: one
// remove_from_clients or auth.accept also covers, or one of NginxRemovedFromClients, of NginxRemovedFacingClients with
// client_address connection, which a file with tls has when it writes none, or of NginxRemovedTraced with the otel
// provider. The Envoy output removes that header before its check reads it, so it never asks the check, where nginx's
// handler reads the client's own copy and asks (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8).
var nginxOnlyWithListed = Refusal{Form: "naming a header that remove_from_clients, auth.accept or the proxy itself removes from clients",
	Instead: "the Envoy output removes that header before it reads it and never asks the check, where nginx asks it; name a header the proxy passes on",
	Applies: func(value, proxyfile *tree.Node) bool {
		name := strings.ToLower(value.Text)
		mode := proxyfile.GetText("client_address")
		facing := mode == "connection" || mode == "" && proxyfile.Get("tls") != nil
		traced := proxyfile.Get("tracing").Get("provider").Get("otel") != nil
		if slices.Contains(NginxRemovedFromClients, name) || facing && slices.Contains(NginxRemovedFacingClients, name) ||
			traced && slices.Contains(NginxRemovedTraced, name) {
			return true
		}
		for _, list := range []*tree.Node{proxyfile.Get("remove_from_clients"), proxyfile.Get("auth").Get("accept")} {
			if list == nil {
				continue
			}
			if whole, prefix := coveredBy(list, []string{strings.ToLower(value.Text)}); whole || prefix {
				return true
			}
		}
		return false
	}}

// nginxAcceptPrefix and nginxAcceptFramed are the forms of auth.accept nginx refuses.
var (
	nginxAcceptPrefix = Refusal{Form: "holding a name with a * prefix",
		Instead: "nginx sets a request header only by a name its configuration writes; write each name in full",
		Applies: func(value, _ *tree.Node) bool { return holdsPrefix(value) }}
	nginxAcceptFramed = Refusal{Form: "naming a header nginx frames or routes a request by",
		Instead: "nginx writes this header itself on the request it passes on, to frame or route it or to keep a client's copy from the service, x-forwarded-for, x-envoy-external-address and the five x-envoy- names Envoy removes from clients only with client_address: connection, and a check service must never change how nginx frames or routes that request; each is refused with either client_address, so a list stays valid when it changes; remove the name from auth.accept",
		Applies: func(value, _ *tree.Node) bool { whole, _ := coveredBy(value, NginxNotAccepted); return whole }}
)

// nginxTracingInURL refuses tracing without path_in_url: true. nginx's tracer adds the request's target, query
// included, to every span as http.target, and an otel_span_attr of that name is added beside it, not over it (with
// otel_span_attr http.target "" a span carried both, the target and its query first, measured on nginx 1.31.8), so
// no line keeps the path out of a span as path_in_url: false keeps it out of the Envoy output's.
var nginxTracingInURL = Refusal{Form: "without path_in_url: true",
	Instead: "nginx's tracer always puts the request's path and query in the span, which path_in_url: false keeps out; write path_in_url: true, or render it for envoy",
	// A file with the xray provider is refused for its provider alone (nginxNoXRay).
	Applies: func(value, _ *tree.Node) bool {
		return value.Get("provider").Get("otel") != nil && value.GetText("path_in_url") != "true"
	}}

// traceHeaders are the headers nginx writes from the trace on every request it passes on, to a service and to the
// check, whatever the client sent (traceHeaders and the check location in internal/nginx).
var traceHeaders = []string{"traceparent", "tracestate"}

// nginxTraceNamed refuses tracing beside a list that names or covers a header of traceHeaders: nginx writes each of
// them from the trace, so what such a list asks for, a client's copy removed, the check's value set or the client's own
// copy sent to the check, is not what nginx sends, and it was not compared with the Envoy output.
var nginxTraceNamed = Refusal{Form: "beside a remove_from_clients, auth.accept or auth.send that names traceparent or tracestate",
	Instead: "nginx writes both headers from the trace for the service and the check; leave the name out, or render it for envoy",
	Applies: func(_, proxyfile *tree.Node) bool {
		auth := proxyfile.Get("auth")
		for _, list := range []*tree.Node{proxyfile.Get("remove_from_clients"), auth.Get("accept"), auth.Get("send")} {
			if list == nil {
				continue
			}
			if whole, prefix := coveredBy(list, traceHeaders); whole || prefix {
				return true
			}
		}
		return false
	}}

// nginxCollectorWithoutResolver refuses a collector's DNS name in a file with no dns.resolver, in the words of a
// service's name (nginxNameWithoutResolver), with the collector's IP address as the other way: the relay that follows
// the name looks it up through dns.resolver, and the tracer's own lookup, in each worker as it starts, sent no span to
// the collector the name moved to in 300 s, until a reload (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
var nginxCollectorWithoutResolver = Refusal{Form: nginxNameWithoutResolver.Form,
	Instead: nginxNameWithoutResolver.Instead + ", or write the collector's IP address",
	Applies: func(value, proxyfile *tree.Node) bool {
		_, _, name, ok := model.ParseEndpoint(value.Text)
		return ok && name && proxyfile.Get("dns").Get("resolver") == nil
	}}

// nginxTagWritten are the headers nginx writes itself on the request it passes on, beyond the ones a tag reads from the
// variable nginx writes them from or finds removed (tagSource in internal/nginx): nginx's value of each was not
// compared with the value the Envoy output's tag reads.
var nginxTagWritten = []string{"host", "origin", "te", "connection", "upgrade", "http2-settings", "content-length",
	"transfer-encoding", "keep-alive", "x-envoy-auth-failure-mode-allowed", "x-envoy-external-address", "traceparent", "tracestate"}

// nginxTagOnReferer and nginxTagOnExpect refuse a tag on a header the Envoy output changes before its tags read the
// request, each in words of its own: it removes a Referer it does not accept (its rule in source/common/http/
// utility.cc), and answers an Expect itself and removes it, where nginx's tag would read the client's line.
var (
	nginxTagOnReferer = Refusal{Form: "naming referer",
		Instead: "the Envoy output removes a Referer it does not accept before its tags read the request, where nginx's tag would read the client's line; tag another header",
		Applies: func(value, _ *tree.Node) bool { return value.Text == "referer" }}
	nginxTagOnExpect = Refusal{Form: "naming expect",
		Instead: "the Envoy output answers an Expect itself and removes it before its tags read the request, where nginx's tag would read the client's line; tag another header",
		Applies: func(value, _ *tree.Node) bool { return value.Text == "expect" }}
)

// nginxTagOnWritten refuses a tag on a header of nginxTagWritten. Its words name the headers, from that list, so the
// support table's cell and the message a file meets say which they are.
var nginxTagOnWritten = Refusal{Form: "naming a header nginx writes itself",
	Instead: "nginx rewrites each of " + joined(nginxTagWritten, "and") + " for the service, and its value in a span was not compared with the Envoy output's; tag another header",
	Applies: func(value, _ *tree.Node) bool { return slices.Contains(nginxTagWritten, value.Text) }}

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
	"admin": {Level: Partial, Note: "/stats is nginx's seven counters (active connections, accepts, handled, requests, reading, writing and waiting) and /server_info its version, not Envoy's; any other path or a query is answered 404, where the Envoy output answers 403; TRACE is answered 405 with no body, where the Envoy output answers it as GET; an absolute target nginx or the public listener's checks refuse, as one of a scheme other than http and https, with an empty port, user information or a query right after its host, is answered 400, where the Envoy output answers 404"},

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
	// nginx's own limit_conn (nginx.org, ngx_http_limit_conn_module): a connection counts only while it has a request in
	// process whose header was read, each HTTP/2 stream counts as one, and a request past the number is answered 503,
	// limit_conn_status's default. It runs in the preaccess phase (ngx_http_limit_conn_module.c, the same at both tags
	// below), after a return has ended a request and before js_access asks the check, so it counts and refuses only a
	// request on its way to a service (the generator's connectionLimit). nginx refuses to load a number above 65535
	// (src/http/modules/ngx_http_limit_conn_module.c line 705 at tags release-1.30.6 and release-1.31.8: "connection
	// limit must be less 65536").
	"limits.connections": {Level: Partial,
		Note: "nginx's own limit_conn: a request on its way to a service past the number of such requests in flight is answered 503, before its check, if any, is asked, each HTTP/2 stream counted as one and an idle connection as none; an answer the file gives itself, such as a 404 or a preflight's 200, is neither counted nor refused, and no line limits a service's connections",
		Refuses: []Refusal{{Form: "above 65535",
			Instead: "nginx's limit_conn takes at most 65535; write 65535 or less, or render it for envoy",
			Applies: countAbove(65535)}}},
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
	// A name's upstream keeps the addresses the name gives in a zone of 1 MiB, which held 1,000 addresses and not 2,000
	// ("memory exhausted" in nginx's log); a name's addresses added, removed, answered no more and answered again were
	// used or left within the 5 s refresh by both, with IPv4 and IPv6 addresses, and every request was served with the
	// last answer while the name server was down (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). The reader
	// takes an address as an IP address exactly when netip.ParseAddr reads it, so that parser decides here.
	"services.<service name>.address": {Level: Partial, Note: "a name gives at most about 1,000 addresses for each port",
		Refuses: []Refusal{nginxNameWithoutResolver,
			{Form: "a zoned IPv6 address", Instead: nginxNoZone, Applies: func(value, _ *tree.Node) bool { return zoned(value.Text) }}}},
	"services.<service name>.addresses": {Level: Partial, Note: "IP addresses with no zone",
		Refuses: []Refusal{{Form: "holding a zoned IPv6 address", Instead: nginxNoZone, Applies: func(value, _ *tree.Node) bool {
			return slices.ContainsFunc(value.Items, func(item *tree.Node) bool { return zoned(item.Text) })
		}}}},
	// round_robin is nginx's own default, least_requests random two least_conn and random random, which spread 900
	// requests on three backends as Envoy does, another draw of the same kind (measured on nginx 1.30.6 and 1.31.8
	// beside Envoy 1.40.2).
	"services.<service name>.balance": {Level: Partial, Note: "hash on the check's service is refused", Refuses: []Refusal{nginxHashOnCheck}},
	// A hash puts each header value, or each client address as client_address reads it, on one backend, as Envoy
	// does, on another ring of its own: the header's lines are joined in sorted order, as Envoy joins them, an empty
	// header is hashed and an absent one goes round robin, and no backend that refuses is tried again (measured on
	// nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with 300 values over HTTP/1.1 and HTTP/2).
	"services.<service name>.hash":            {Level: Supported},
	"services.<service name>.hash.header":     {Level: Supported},
	"services.<service name>.hash.client":     {Level: Supported},
	"services.<service name>.health":          nginxPassiveCheck,
	"services.<service name>.health.path":     nginxHealthNotRead,
	"services.<service name>.health.grpc":     nginxHealthNotRead,
	"services.<service name>.health.interval": nginxHealthNotRead,
	"services.<service name>.health.timeout":  nginxHealthNotRead,
	"services.<service name>.health.healthy":  nginxHealthNotRead,
	// max_fails counts failed attempts within fail_timeout, not checks in a row (ngx_http_upstream_module, server).
	"services.<service name>.health.unhealthy": {Level: Partial, Note: "the failed requests within 10 s that take a backend out (max_fails), not checks in a row"},

	// resolver with valid=5s in the http block, and in the stream block of the check's relay, asks for IPv4 and IPv6
	// addresses and asks again over TCP after an answer cut short, as Envoy does (measured on nginx 1.30.6 and 1.31.8
	// beside Envoy 1.40.2 with 100 and 1,000 addresses).
	"dns":          {Level: Supported},
	"dns.resolver": {Level: Supported},

	// The route's location asks the check from proxyfile.js through a location of its own, which reaches a relay in the
	// stream block: the relay sends the check its question and reads the answer itself, and hands nginx only what it
	// read whole. Each way nginx reads an answer with a reader of its own (a subrequest straight to the check, ngx.fetch
	// and auth_request) took 10 to 23 of 131 malformed answers that Envoy refuses or marks as a pass, and the relay took
	// none, so an answer the relay cannot read is a refusal, never a pass. Every note below was measured on nginx 1.30.6
	// and 1.31.8 beside Envoy 1.40.2.
	"auth": {Level: Supported},
	// With content-length: 0 nginx ends a gRPC refusal's stream with its HEADERS frame, as Envoy does; without it nginx
	// sent an empty DATA frame after them. The check's connection is closed after each answer, so each check leaves a
	// local port of nginx's or of the check's waiting for a while, and a check that does not close first leaves it on
	// nginx's side.
	"auth.ask": {Level: Partial,
		Note: "a request whose check answers in one of these forms is answered 403: a status outside 100 to 599, such as 2, 99, 600 or 2000, or 101, where Envoy passes the status on to the client; HTTP/2.0 or HTTP/1.2, two spaces or a tab after the version, a + before the status, a tab between the status and the reason, a lone CR, a space or a tab after a chunk size, a chunk size of nine hex digits, a chunk extension holding a control byte or a quoted CR LF, a chunk-size line past 1,024 bytes, bytes between a chunk's data and its line end, a folded trailer line, or a head of exactly 81,920 bytes with a line in a bare LF, which nginx counts a byte longer, where Envoy reads the answer; a 200 whose body passes 1 MiB, where Envoy serves the request; a denial whose body passes 1 MiB, where Envoy passes the denial on; a denial whose head has more than 100 header lines, folded lines not counted, with a name on two of them, where Envoy counts a name it keeps inline, such as date, once and passes the denial on; over HTTP/2, a gRPC call refused for the check's denial, or with deny for want of a readable answer, is answered as Envoy answers it, with one more header, content-length: 0; a check's 301, 302, 303, 307 or 308 to an HTTP/1.1 request that carries a body reaches the client with nginx's own page in place of the check's body, and a check's 304 to such a request with content-length: 0, as nginx discards the request's body, so that no byte of it is read as the next request; a denial reaches the client with Server: nginx where the check sent no Server header, with nginx's own reason phrase (302 Moved Temporarily, where Envoy writes Found), without a header line the check sent with an empty value, with the check's Access-Control-Allow-Origin beside the listed origin's own, where Envoy sends the origin's alone, and, to a HEAD request, with Content-Length: 0, where Envoy sends transfer-encoding: chunked; an error in proxyfile.js itself as it asks the check or reads its answer is answered 403, with allow too, and writes no log line; the check is sent connection: close on a new connection for each check, where Envoy sends no Connection header and keeps its connections to the check open, so nginx's local ports bound how many checks a second it can send to one check address: near 340,000 a second when the check closes its connection first after it answers, derived from the 0.13% of connections nginx's side then kept in TIME_WAIT and not reached, and about 470 a second when it does not, at most 28,231 checks to one address in any 60 s from the 28,232 ports of 32768 to 60999 (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2); past that bound a check fails for want of a port, and its request is answered 403 with deny and served with the unchecked mark with allow; the relay listens on a socket file, /run/proxyfile-<listen>/check.sock, which nginx removes when it stops and a kill leaves, and nginx then does not start, its bind() to the file failing (98: Address already in use), until the file is removed or /run is a tmpfs"},
	// With deny the handler's timer answers 403 at auth.timeout, as Envoy does. With allow the request goes on only when
	// the check's subrequest ends, which the relay ends at the first piece of the answer past the deadline, or after
	// nginx's proxy_timeout of auth.timeout with none. The relay keeps no timer of its own: njs logs an error line for a
	// timer still waiting when the session ends. Measured, the answers that trickled in went on within 0.07 s of
	// auth.timeout.
	"auth.timeout": {Level: Partial,
		Note: "with allow, the request goes on with the unchecked mark at the first byte the check sends after auth.timeout, or after one auth.timeout with no byte from it, so a check that sends a byte just before each auth.timeout of silence holds the request up to twice auth.timeout, where Envoy goes on at auth.timeout"},
	"auth.enabled": {Level: Supported},
	// A status of 500 or more, a check that refuses the connection and one that sends nothing within auth.timeout go on
	// with the mark with allow, as by Envoy. An answer nginx cannot be sure it read right is never taken for no answer:
	// 46 of the 131 forms measured are answered 403 here, where Envoy marks them. A head of more than 100 header lines
	// is unanswered only when each name came once: Envoy counts a name it keeps inline once, so with a name on two
	// lines it may read the answer, and the relay refuses it, as HAProxy's script does.
	"auth.when_unavailable": {Level: Partial,
		Note: "with allow, a request whose check answers in a form Envoy reads as no answer (a malformed header line, framing or status line, a head or a trailer section past the 81,920 bytes Envoy reads, a head of more than 100 header lines with a name on two of them, such as set-cookie, or a body that never ends) is answered 403, where Envoy serves it with the unchecked mark, x-envoy-auth-failure-mode-allowed: true"},
	// The handler asks when a line of the header holds a byte as nginx passes it on, or the header comes on two lines or
	// more. Of 510 cases measured (authorization, x-api-key and cookie, each in 85 sets of lines over HTTP/1.1 and
	// HTTP/2), 345 were asked or not as by Envoy, 160 were nginx's own 400 for a second authorization line, which the
	// row of the request line declares, and 5 are the note's.
	"auth.only_with_header": {Level: Partial, Refuses: []Refusal{nginxOnlyWithListed},
		Note: "over HTTP/1.1 a request whose header is a lone tab is asked, where Envoy does not ask it: nginx keeps the tab, which Envoy removes; over HTTP/2 a cookie sent as two or three empty cookie fields is asked, where Envoy does not ask it"},
	// The check receives a ! for each byte of a header it is sent that is not part of well-formed UTF-8, and
	// well-formed UTF-8 as the client sent it, as Envoy writes them.
	// The check location writes the relay's own two question headers under these names, and the relay reads them and
	// sends the check neither, so no line passes a client's own copies on (measured with send: ["x-proxyfile-*"] on
	// nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	"auth.send": {Level: Partial, Note: "a client's own x-proxyfile-method and x-proxyfile-path are not sent to the check when send covers them, where Envoy sends them; what the check is asked does not change"},
	"auth.accept": {Level: Partial, Note: "names written in full, none of them a header nginx writes itself on the request it passes on; a request the accepted headers take past limits.request_headers is answered 500 as by Envoy, counted with the client's own Connection line, its own copies of the headers the proxy sets, each line of a name sent on several lines and each cookie field over HTTP/2, which Envoy drops, replaces or joins into one before it counts, so such a request can be answered 500 by those lines sooner; an accepted header the check sends empty reaches a gRPC service as no header, where Envoy sends it empty",
		Refuses: []Refusal{nginxAcceptPrefix, nginxAcceptFramed}},
	// The check is told the path the route matched, the one nginx has normalised where Envoy normalises it. In a file
	// with no pattern route a path holding such a character is matched on $uri, which holds it decoded, and nginx
	// escapes it again only as it sends it to the service. nginx's $scheme is the connection's, and no variable holds
	// an HTTP/2 request's :scheme, so no line can tell the check one that differs (measured on nginx 1.30.6 and 1.31.8
	// beside Envoy 1.40.2, :scheme http over TLS and https without). Under client_address connection the file tells the
	// check the connection's scheme over HTTP/1.1 too, whatever scheme an absolute target names, as a client writes the
	// target: GET http://localhost/products/1 over TLS is told https, where the Envoy output tells http (the battery's
	// row "an absolute target's host is the host the check is told", on nginx-auth and portable). Under forwarded the
	// file tells the check the client's X-Forwarded-Proto as it arrived, the word a proxy in front writes, which the
	// generator does not bound: X-Forwarded-Proto: ftp is told ftp, where the Envoy output tells the connection's
	// scheme, and both send the service ftp (the battery's row "an x-forwarded-proto that is neither http nor https",
	// on nginx-checks and nginx-tracing; the list of ways nginx is less strict names it).
	"auth.send_request": {Level: Partial,
		Note: "in a file with no pattern route, a path holding a character Envoy escapes before it picks a route (\" < > ^ ` { | }) is told in x-forwarded-uri as the client wrote that character, where Envoy tells it escaped; the service receives it escaped from both; over HTTP/2 a request whose :scheme differs from its connection's scheme is told in x-forwarded-proto the connection's under client_address connection, and under forwarded the x-forwarded-proto it arrived with, or the connection's when it arrived with none, where Envoy tells it the :scheme; under client_address connection, so is an absolute target over HTTP/1.1 whose scheme differs from its connection's: an http target sent over TLS is told https, where Envoy tells it http; under client_address forwarded, an x-forwarded-proto that is neither http nor https, such as ftp, is told as it arrived, where Envoy tells the connection's scheme"},
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
			Applies: func(value, _ *tree.Node) bool { return holdsPrefix(value) }},
			{Form: "holding " + joined(nginxFramedBy, "or"),
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

	"routes":       {Level: Supported},
	"routes[].rpc": {Level: Supported},
	// A call is tried on the next backend after a connection the service refused or did not open in time, as the
	// Envoy output tries it, and a gRPC call nginx sent is never sent again: 40 calls with one of two backends refusing
	// were all served by the other, and a call whose stream or connection the service reset after it read it reached
	// it once through both. A call with another method, GET or PUT with gRPC's content type over HTTP/1.1, is not tried
	// again, because nginx sent it again after the service read it (measured on nginx 1.30.6 and 1.31.8 beside Envoy
	// 1.40.2).
	"routes[].rpc.retries": {Level: Partial,
		Note: "a call whose method is not POST is not tried again; a service with one backend is not tried again, where the Envoy output tries the same backend"},
	"routes[].rpc.rate_limit":                   {Level: Supported},
	"routes[].rpc.rate_limit.requests":          {Level: Supported},
	"routes[].rpc.rate_limit.per":               nginxRatePer,
	"routes[].rpc.rate_limit.burst":             {Level: Supported},
	"routes[].rpc.rate_limit.by":                nginxRateBy,
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
	"routes[].auth": {Level: Supported},
	// nginx's own retry stays off, as it sent a GET and a PUT to the service again after the service read them. A
	// retried route's location instead reads the body whole first and jumps, after a try that sent the service nothing,
	// to a copy that tries the next backend: no request was read twice, the check was asked once and the rate limit
	// charged once a request, and each request was served or answered as through the Envoy output at its time, but in
	// the cases this note names (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). The forms that would start
	// the first try before the body is whole, the jump with proxy_request_buffering off and nginx's own retry with it,
	// each sent a service a short body or a request it had read again (docs/nginx.md, Retries).
	"routes[].retries": {Level: Partial,
		Note: "a request is tried again only when its last try sent the service nothing, at once and with no limit on retries at the same time, which the less strict list names, on the next backend in the service's order rather than one the balance or the hash picks, at most as many times as the service has other backends and never more than 6; " +
			"a service with one backend is not tried again, where the Envoy output tries the same backend; " +
			"a service found by a DNS name is tried again on the same name, where nginx may pick the backend that failed; " +
			"a request is tried again only when it has no Transfer-Encoding and a Content-Length of at most 1 MiB, when it has neither header over HTTP/1.x, which gives it no body, or when it is a GET, HEAD, OPTIONS or DELETE over HTTP/2 with no content-length, and nginx reads its body into memory before the first try, so the service gets it only once all of it arrived, and with a slow client a request whose tries fail ends up to the upload's time later than through the Envoy output, which tries while the body arrives, and can be answered 504 where the Envoy output answers 502 or serves it; " +
			"such a GET, HEAD, OPTIONS or DELETE over HTTP/2 with more than 1 MiB of body is answered 413, where the Envoy output serves it; " +
			"any other request, a body longer than 1 MiB, a body sent in chunks whatever its method, or one of another method over HTTP/2 with no content-length, a gRPC call's among them, is sent as it arrives and never tried again, and a first try of it that fails is answered as on a route with no retries, where the Envoy output tries again a body of unknown length under its bound of 1 MiB, holds up to 16 MiB over HTTP/2, and answers 507 to a client that sent a longer body quickly and serves one still sending; " +
			"a request nginx wrote on a kept-alive connection the service had closed is not tried again, where the Envoy output tries it again on another backend"},
	"routes[].rate_limit":          {Level: Supported},
	"routes[].rate_limit.requests": {Level: Supported},
	"routes[].rate_limit.per":      nginxRatePer,
	"routes[].rate_limit.burst":    {Level: Supported},
	"routes[].rate_limit.by":       nginxRateBy,

	"rpc":                   {Level: Supported},
	"rpc.methods":           {Level: Supported},
	"rpc.methods[].service": {Level: Supported},
	"rpc.methods[].method":  {Level: Supported},
	// A value holding " or $ reaches the check as written, as through Envoy (quoted, and $ as ${pf_dollar}).
	"rpc.methods[].action":  {Level: Supported},
	"rpc.methods[].reports": {Level: Supported},
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
	"log":             {Level: Partial, Note: "the time is the moment nginx writes the line, in whole seconds with the offset of nginx's time zone (+00:00 in UTC), where the Envoy output writes the request's start in milliseconds with Z; over HTTP/1.1 bytes_out counts the chunk lines of an answer nginx sends chunked, where the Envoy output counts its body alone; an answer whose status has one, two or four digits, or that has no status line, logs the status 9, where the Envoy output logs the four digits or 502; a status that starts with 0 logs its number without the zeros (050 as 50), one with spaces between its digits the digits joined (2 0 0 as 200) and one with a fraction its whole number (403.1 as 403), each written only when that number is status_from or above or sample takes the request, where the Envoy output writes 502 whatever status_from says; bytes_in counts the whole request, not its body; upstream_ms counts from the opening of the service connection of the try that answered, not from the request's start; authz_us counts the check's time in whole milliseconds, 0, 1000, 2000 and so on, where the Envoy output counts microseconds; there are no flags and details fields; a request of a retried route that is not tried again because of its body, longer than 1 MiB or of a length nginx does not know, writes the upstream-failure flag UF after its one try failed, where the Envoy output, which tries it again, writes URX,UF; a value the Envoy output writes null is written empty, but upstream_ms; a request the origin or upgrade check refuses logs route and upstream empty, where the Envoy output names them; a request refused for a dot segment with a ; parameter logs route empty on each of its lines, where the Envoy output names the route that refuses it, refused:dot-segment-with-parameter; a request Envoy refuses as it reads it logs its host, method, request id and protocol, which Envoy writes null and HTTP/1.1; a request id or Host holding a byte above 0x7F is logged empty"},
	"log.status_from": {Level: Supported},
	"log.sample":      {Level: Partial, Note: "the share is drawn by a hash of the request id, not by its first eight hex digits"},

	// The tracer is ngx_otel_module, which the official images tagged -otel hold and the plain ones do not: on them nginx
	// -t stops at its load_module, "dlopen() ... failed" (measured on nginx 1.30.6 and 1.31.8). A file without tracing
	// loads no module and keeps its bytes.
	"tracing":                                     {Level: Partial, Note: "needs ngx_otel_module, in the nginx images tagged -otel, where the plain images stop at load_module", Refuses: []Refusal{nginxTracingInURL, nginxTraceNamed}},
	"tracing.provider":                            {Level: Supported},
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
	// Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2: the draw on 2,000 ids and 17 crafted ones, 15 forms of a
	// client's traceparent and tracestate, and 32 at the check, every field of a span over HTTP/1.1, HTTP/2, with a body,
	// a 503, no route and gRPC, the requests either proxy refuses, a retried route, and the collector up, stopped, slow,
	// answering an error and refusing connections, which no request waited on. The module cannot continue a parent of
	// another version than 00: with otel_trace_context propagate it started a trace of its own for a version 01 or ff
	// parent (nginx 1.31.8), where the Envoy output kept the client's trace id, so such a parent goes on with no span.
	// Each of the ten kinds of refusal measured that the Envoy output writes no span for gets none from nginx either.
	"tracing.provider.otel": {Level: Partial,
		Note: "spans carry the Envoy output's attribute names with nginx's own beside them, http.route holding the name of the location nginx generated; http.status_code is a number, response_flags, error and the gRPC attributes are not set, and an attribute with no value, such as upstream_cluster on a request no route takes, is sent empty, where Envoy sets none; over HTTP/2 http.url holds the connection's scheme, where Envoy writes the client's :scheme when it is http or https; a span's tracestate holds the client's first tracestate line, where Envoy's holds the lines joined; a traceparent of another version than 00, or one whose line or first tracestate line has a tab at either end over HTTP/1.1, reaches the service and the check with the client's version, trace id and parent id and the sampled flag the Envoy output sends, and no span is written, where Envoy continues the trace with version 00, a parent id and a span of its own; a request whose User-Agent has a tab at either end over HTTP/1.1, which nginx's own http.user_agent would hold, gets no span and is sent on not sampled, where Envoy traces it; a gRPC call whose malformed traceparent comes with a second traceparent or tracestate line is answered 400 and reaches no service, where Envoy passes each line on as a field of its own; the auth check gets the trace, with the request's span as its parent, but no span of its own, where Envoy writes a span for the check; a preflight and a request refused for its origin get a span named ingress with upstream_cluster empty, where the Envoy output names the span after the route and sets its service; and no empty tracestate line is sent to the service or the check, where Envoy adds one beside each traceparent of its own when no tracestate goes on, nor a traceparent or tracestate sent as one empty line, which Envoy passes on beside a malformed traceparent"},
	// A collector's name goes through the relay the file writes for it (collectorLines), whose upstream is written as a
	// service's name is, in a zone of 1 MiB, which held a name of 1,000 addresses and not one of 2,000 (no memory in
	// upstream zone, on nginx 1.30.6 and 1.31.8). stub_status, the admin port's /stats, counts every request nginx serves
	// and has no directive that leaves a server out, so it counts each call of the tracer through the relay (after 5
	// traced requests and a read of /stats its requests had grown by 11, the tracer's 5 calls among them, on both
	// versions). After the first collector left and a second started at the name, and after the name moved with the first
	// still up, the spans reached the second 6.8 to 8.7 s after the change through nginx, where the Envoy output's did in
	// 0.2 to 0.4 s, and none was lost in the second case, so the seconds are the page's alone (three runs of each event on
	// nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). The relay's socket file is left by a kill as the check relay's is, and
	// nginx then does not start until the file is removed or /run is a tmpfs (measured on both versions).
	"tracing.provider.otel.endpoint":    {Level: Partial, Note: "an IP address, or a DNS name with dns.resolver, which nginx follows through a relay of its own, a name giving at most about 1,000 addresses, as a service's does; the admin port's /stats counts each call of the tracer through the relay as a request; the relay listens on a socket file, /run/proxyfile-<listen>/collector.sock, which nginx removes when it stops and a kill leaves, and nginx then does not start, its bind() to the file failing (98: Address already in use), until the file is removed or /run is a tmpfs", Refuses: []Refusal{nginxCollectorWithoutResolver}},
	"tracing.provider.otel.service":     {Level: Supported},
	"tracing.provider.otel.sample":      {Level: Supported},
	"tracing.provider.otel.sample.rate": {Level: Supported},
	"tracing.tags":                      {Level: Supported},
	"tracing.tags.<tag name>":           {Level: Supported},
	// A tag named like a field of the Envoy output's span takes the field's place, and where its source gives none and
	// it has no default, or gives an empty value, keeps the field's value, as the Envoy output keeps its own. njs leaves
	// a variable named like a property every object has (__proto__, constructor, valueOf, toString, hasOwnProperty,
	// propertyIsEnumerable, isPrototypeOf) out of process.env, so the script reads it as unset, where Envoy read
	// toString=ts1 (measured on njs 1.0.2 of both images beside Envoy 1.40.2).
	"tracing.tags.<tag name>.env": {Level: Partial,
		Note: "a variable set to an empty value, or unset with no default, gives the span an empty tag, where Envoy sets none, except a tag named like a field of the Envoy output's span, such as component, which keeps the field's value, as through Envoy; a variable named like a property every object has, such as constructor or toString, is read as unset, where Envoy reads it"},
	"tracing.tags.<tag name>.header": {Level: Partial,
		Note:    "a header absent with no default, or sent empty, gives the span an empty tag, where Envoy sets none, except a tag named like a field of the Envoy output's span, such as component, which keeps the field's value, as through Envoy",
		Refuses: []Refusal{nginxTagOnWritten, nginxTagOnReferer, nginxTagOnExpect}},
	"tracing.tags.<tag name>.default": {Level: Supported},
	// nginx's tracer adds the target as the client wrote it, as http.target, so a span is written only for a target
	// nginx sends on as written and of at most 256 bytes, beside no client's x-envoy-original-path under forwarded and
	// on a route with no rewrite; such a request is sent on not sampled. Over a table of targets on Envoy 1.40.2 beside
	// nginx 1.30.6 and 1.31.8, each http.url and http.target nginx wrote held bytes of the Envoy output's http.url.
	"tracing.path_in_url": {Level: Partial,
		Note: "true only; a span is written only for a request whose target nginx sends on as the client wrote it, at most 256 bytes, on a route with no rewrite and, under client_address forwarded, with no x-envoy-original-path, and any other is sent on not sampled with no span, where Envoy writes the path it normalised, escaped or rewrote, cut to 256 characters, or a client's x-envoy-original-path"},

	"the request's path normalised before a route is picked": {Level: Partial,
		Note: "a path that holds //, a dot segment, an escaped unreserved character or %2F and also an escape Envoy keeps, such as %3B, is answered 400, and so is one that holds such an escape beside a character Envoy escapes before it matches (\" < > ^ ` { | } or one outside ASCII), where Envoy normalises the path and keeps the escape; an empty query, a ? with nothing after it, after a path that needs normalising or holds such a character is answered 400, where Envoy sends the ? on; a backslash, %5C and a control character, written or escaped, are answered 400, and so is an invalid escape such as %zz, which Envoy passes on; a path with an empty segment before a dot-dot segment, / and . written or escaped, such as /catalog//../products/1, is answered 400, where Envoy resolves the dot-dot segment before it merges the slashes and routes /catalog/products/1"},
	"a dot segment with a ; parameter answered 400": {Level: Partial, Note: "/a/..;/b is answered 400 where Envoy 1.40 resolves it, and nginx closes the connection after it, as after every 400 it answers for a path before a route; so is a CORS preflight through such a segment, where Envoy answers it as the preflight of the path it resolves"},
	// Over HTTP/2, :authority and a host field that agree, differ or are empty, and over HTTP/1.1 a header block that
	// gains a byte a second and a connection that sends nothing to a listener without tls, each measured on nginx 1.30.6
	// and 1.31.8 beside Envoy 1.40.2 at the full time: the 5 minutes of nginx's client_header_timeout, written in the
	// http block, end both of the last two, where Envoy answers the first 408 at its own 5 minutes and closes the second
	// after an hour, its idle time for a connection with no request. nginx sets that timer on every new connection, with
	// tls or without (ngx_http_init_connection in src/http/ngx_http_request.c, line 360 of 1.30.6 and 366 of 1.31.8);
	// Envoy's time for a connection with tls that sends nothing was not measured. A client that closes its own side right
	// after its request, measured with no auth on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2: no service received the
	// request through either, and a Host no environment names was answered 404 by both.
	"the request line and header names read before a route is picked": {Level: Partial,
		Note: "a tab between the method and the target, an absolute target with user information, with no Host header or with a query or a fragment right after its host, and OPTIONS * are answered 400 and the connection closed, TRACE and CONNECT 405, and a request line with no version, HTTP/0.9's form, gets the text Bad Request alone, with no status line, where Envoy serves the first two and TRACE, answers OPTIONS *, CONNECT and that absolute target 404, and that request line 426; HTTP/0.0 to HTTP/0.9 with headers are answered 400 and HTTP/2.0 to HTTP/9.9 on an HTTP/1 connection 505, where Envoy answers 426; a request nginx's own parser refuses before any rule of the file runs, such as one with a control byte in the target, a space in a header name or no Host line, is answered 400 with nginx's HTML page as text/html, where the Envoy output's 400 carries the body Bad Request as text/plain, or no body for a request with no Host line; the 426 nginx answers carries no reason phrase on its status line, HTTP/1.1 426 alone, where the Envoy output's reads 426 Upgrade Required; a header name outside RFC 9110's token characters, or a header value holding a control byte other than a tab, is answered 400 over HTTP/1.1 and HTTP/2, where Envoy answers 400 over HTTP/1.1 and closes the connection over HTTP/2; over HTTP/2 any request trailer ends the connection, where Envoy serves the request; a header line folded onto the next and a Host whose port is past 65535 or holds a sign are answered 400, where Envoy serves them; a request holding a header name with _ that nginx reads as one of its own headers, or a second X-Forwarded-For, X-Forwarded-Proto, X-Request-Id, Content-Type, User-Agent or Referer line, is refused with 400, where Envoy reads the name as another header and joins the lines with a comma; a Connection header that names a header other than close, keep-alive, upgrade, http2-settings and te is answered 400, where Envoy removes the header it names and serves the request; a second line of authorization, if-modified-since, if-unmodified-since, if-match, if-none-match or if-range is answered 400, where Envoy joins the lines with a comma and serves the request; the lines of accept, via and accept-encoding, each a list, reach the service as separate lines, where Envoy joins them with a comma into one line, the same message written as separate lines; two Host lines are answered 400, where Envoy answers 404; over HTTP/2 a host field that differs from :authority, and an empty :authority or host field, is answered 400 with nginx's own page, where Envoy routes by :authority and closes the connection with no answer to an empty one; over HTTP/1.1 a header block still arriving after 5 minutes is ended with no answer, where Envoy answers 408 then and closes the connection a second later, and a connection that sends nothing to a listener without tls is closed after 5 minutes, where Envoy keeps it for an hour; a client that closes its side of the connection right after its request gets nginx's answer when nginx refuses the request itself, an Origin on none of the hosts, a target holding a fragment, a dot segment with a ; parameter or HTTP/1.0, where Envoy closes the connection with no answer; without limits.request_headers nginx holds the Envoy output's 60 KiB and 100 header fields, with the differences the rows of its keys name"},
}, LessStrict: nginxLessStrict}

// The rows of a rate limit, the same for an entry of routes and for its rpc. Each limited entry has a zone of shared
// memory that every worker charges, keyed as client_address reads the client, before the check, and a request over the
// limit gets the Envoy output's answer for its kind; the same requests were admitted and refused at every instant of
// paced plans with per 1s, 2s and 1m and burst 0 and 5, and 200 requests sent at once to 12 workers admitted the 10 of
// a limit of 10 (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). Both end the connection after every HTTP/1.1
// request over the limit with a body, a gRPC call and a Connect call among them, and keep it after one with none.
// nginx keeps a rate as thousandths of a request a second, rounded down. Its zone of 16 MiB held 120,000 clients,
// where Envoy forgets the oldest past 65,536.
var (
	nginxRatePer = Support{Level: Partial,
		Note:    "a rate a minute is kept in thousandths of a request a second, rounded down, so 1 a minute refills every 62.5 s where the Envoy output refills every 60 s (nginx refused at 62.1 s and admitted at 63.3 s)",
		Refuses: []Refusal{nginxNoWholeRate}}
	nginxRateBy = Support{Level: Partial,
		Note: "each limited route remembers about 120,000 clients in up to 16 MiB of shared memory, where the Envoy output remembers 65,536, so a client is forgotten later"}
)

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
	// The Envoy output's rule is isValidRefererValue (source/common/http/utility.cc at v1.40.2, which
	// conn_manager_utility.cc calls on every request): a value its URL parser, http_parser_parse_url, reads as a URL
	// with a scheme or a host is kept when it holds no fragment and no user information, and any other only when each
	// byte is one of RFC 3986's query and fragment characters and no colon comes before its first slash. The code of the
	// parser Envoy links was not at hand, and the published grammar does not give Envoy's answers, so no rule is written.
	{Row: requestLine, Name: "a Referer the Envoy output removes reaches the service",
		Measured: "Of 104 forms of one Referer line, Envoy removed 46 before the service, such as `a:b`, `/a#f`, `a b`, `/é`, `{`, `http://u@a/`, `http://a/#f`, `http://a:65536/`, `http://a_b/`, `http://[1:2:3]/` and `about:blank`, and nginx passed every one on. The Envoy output removes a Referer that its URL parser reads as a URL with a fragment or user information, or that the parser does not read and that holds a byte outside RFC 3986's query and fragment characters or a colon before its first slash. A rule written from the published grammar of that parser, nodejs's http_parser, answered forms of the 104 otherwise than Envoy: in its strict build it removed `h2://a/` and `x-y://a/`, whose scheme Envoy reads with a digit or a `-` in it, and in its lenient build it also kept `http://a_b/` and `http://a/é`. nginx answers two Referer lines `400`, as the request line's row says."},
	// nginx stores a header value without the spaces around it but with a tab there, and each route's location passes a
	// client's header as nginx stores it. RFC 9110 does not count the white space around a field value as part of the
	// value, but a service that reads the bytes reads the tab, so the way is named here rather than in the row's note.
	{Row: requestLine, Name: "over HTTP/1.1, a tab before or after a header value reaches the service",
		Measured: "A header value sent over HTTP/1.1 with a tab right after its colon or at its end reaches the service with the tab, where Envoy removes it: the service receives `User-Agent` as a tab and `probe/3`, or as `probe/4` and a tab, and `X-A` as a tab and `b5`, where through Envoy it receives `user-agent: probe/3`, `user-agent: probe/4` and `x-a: b5`, under `client_address: forwarded` and under `connection`. Under `forwarded` a client's `X-Request-Id` sent as a tab and `abc-1`, or as `abc-2` and a tab, reaches the service with the tab too, and nginx's answer gives it back to the client with the tab, where Envoy sends `abc-1` and `abc-2` to both; under `connection` each proxy sends an id of its own in its place. A service that reads the value as RFC 9110 says, without the white space around it, reads the same value through both."},
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
	// nginx matches a location by the path of an absolute target, and the admin server refuses one only when nginx or
	// the public listener's checks do; Envoy's admin listener answers every absolute target 404.
	{Row: "admin", Name: "the admin listener serves an absolute target of http or https as the path it names",
		Measured: "`GET http://localhost/ready HTTP/1.1` sent to the admin listener is answered `200` with `LIVE`, and so is the same target with `https://` or `HTTP://`, and `http://localhost/stats` and `http://localhost/server_info` are answered `200` with nginx's counters and its version, where the Envoy output's admin listener answers `404` with no body to every absolute target. A path the admin listener does not answer, `http://localhost/nothing`, is answered `404` by both, and a target of another scheme, `ftp://localhost/ready` or `ws://localhost/ready`, `400` with the body `Bad Request` and the connection closed, where the Envoy output answers `404`."},
	// Measured with the Envoy output of client_address: connection, which writes use_remote_address: true, and of
	// forwarded, which writes false; the request checks of the nginx output take an absolute http and https target alike.
	// The battery's row "an absolute https target", of the scheme the check is told, holds it on nginx-connection.
	{Row: "client_address", Name: "under connection, an absolute https target sent over plain HTTP is served",
		Measured: "With `client_address: connection` and no `tls`, an absolute `https` target sent over plain HTTP, such as `GET https://localhost/products/1 HTTP/1.1`, or the same with `HTTPS://`, is served: the service receives `X-Forwarded-Proto: http`, and with `auth.send_request: true` the check is told `x-forwarded-proto: http`, the connection's scheme, where the Envoy output answers `403` with the body `Forbidden`, writes `http1.https_url_on_plaintext_connection` in its request log, closes the connection and asks no check. Under `forwarded` both serve it, and the service receives `x-forwarded-proto: http` through both."},
	{Row: "timeouts.request", Name: "without timeouts.request, an answer that keeps arriving runs past 15 s",
		Measured: "Without `timeouts.request`, an answer that keeps coming runs past the 15 s at which the Envoy output ends a request: nginx counts its limits between two reads or writes."},
	// proxy_send_timeout counts from the last write nginx could make, so it ends a try whose service reads too slowly
	// to take another write as it ends one whose service reads nothing; it does not end a try whose service takes a
	// write within it each time, however long the body takes, nor a client's pause, which leaves nginx nothing to
	// write. The streamed locations keep the http block's send time.
	{Row: "timeouts.request", Name: "without timeouts.request, a request body its service does not read, or reads slowly, runs past 15 s",
		Measured: "On a route with no retries, and for a request a retried route sends on as it arrives, nginx sends the body with its send time, which counts between two writes and is 5 minutes without `timeouts.idle`. A `PUT` of 1 MiB to a service that takes the connection and does not read it, or reads one byte every 20 s, is answered `504` after 300.1 s (300.0 to 300.2 in three runs), where the Envoy output answers `504` after 15.0 s; a `PUT` of 2 MiB or of 64 MiB, or of 2 MiB in chunks, gets nginx's `504` after 300.1 s too, where the Envoy output answers `408` after 300.0 s (on a retried route, in one of three runs, `504` after 15.0 s). Tried: a send time of the read time, 15 s, in each location that streams a body. It answered every one of those `PUT`s `504` after 15.1 s (15.0 to 15.1), those the Envoy output answers `408` after 300 s among them, to the service that reads one byte every 20 s too, so it is not written; a client that stopped its body for 20 s was served by all three with it and without it. Not written either: reading a body of at most 1 MiB whole before sending it, on every route as a retried route does. That answers such a `PUT` as the Envoy output does, `504` after 15.0 s, as the retried route's location answered it, but a route with no retries would then hold each such body in memory, and its service would get nothing of an upload until all of it had arrived. A service that reads a body slowly but takes a write within the send time each time is sent all of it, on every route, and the send time does not end its try; a retried route's location sends the body it read whole with the route's read time as its send time. A `PUT` of 1 MiB to a service that reads 65,536 bytes every 1.5 s, in three runs of each, is answered `504` after 27.0 s by nginx 1.30.6 and 27.1 s by 1.31.8 on a retried route (27.0 to 27.1) and after 18.1 s by both on a route with no retries (18.1 in every run), where the Envoy output answers `504` after 15.0 s on both (15.0 in every run). The retried route's send time of 15 s did not end it, and no setting ends such a try at the route's time, as none ends an answer that keeps arriving: nginx has no time for a whole try."},
	// nginx closed its connection to the check first whenever the check closed 20 ms or more after its answer, or not at
	// all (50 of 50 in TIME_WAIT on nginx's side), and proxy_half_close on changed nothing, so the relay cannot wait for
	// the check's close; the bound follows from 60 s of TIME_WAIT on each of the local ports.
	{Row: "auth.when_unavailable", Name: "with allow, a check service that does not close its connection right after its answer leaves requests unchecked past about 470 checks a second",
		Measured: "When the check service does not close its connection right after its answer (measured: 20 ms or more after it, or not at all), nginx closes it first, so each check holds one of nginx's local ports in `TIME_WAIT` for 60 s: with the 28,232 local ports of 32768 to 60999 that is about 470 checks a second to one check address, past which nginx cannot open a connection to the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output keeps its connections to the check open and goes on asking. `proxy_half_close on` does not change which side closes first."},
	// worker_connections 8192 lets one worker carry twice the 1,024 checks the Envoy output asks at once, at four
	// connections each, but nginx counts a client's and a service's connections against the same number, so no setting
	// gives the check connections of its own.
	{Row: "auth.when_unavailable", Name: "with allow, a worker with none of its 8192 connections left serves requests unchecked",
		Measured: "Each checked request in flight holds four of a worker's connections, and every other connection of the worker, a client's or a service's, counts against the same 8192 (`worker_connections`). A worker with none left cannot ask the check, and with `allow` the request is then served with the unchecked mark, where the Envoy output's check has its own 1,024 connections whatever else the proxy carries. With 1024 connections a worker, 231 and 234 of 800 requests sent at once to 2 workers were served unasked (nginx 1.30.6 and 1.31.8), where the Envoy output served none; with 8192, none were, up to 4,000 at once with nothing else open."},
	// Each service's upstream keeps up to 32 idle connections in each worker; with none, as in a just-started nginx, no
	// such request reached the service. proxy_ignore_client_abort is already off, nginx's default, so no directive
	// is left that makes nginx drop the request.
	{Row: "auth.ask", Name: "a request whose client closes its connection during the check can still reach the service",
		Measured: "When a client closes its connection while its check is asked, with a close, a reset or a half-close, and the check then allows the request, nginx can still send the request to the service, where the Envoy output ends the request when its client goes and sends the service nothing. Of 30 clients, one after another, that each sent a request and closed 50 ms into a check that allowed it after 300 ms, the service received 16 to 21 `GET` requests and 17 to 29 `POST` requests with their bodies, 12 and 17 after a reset and 17 and 23 after a half-close, once nginx had served other requests to it and so held idle connections to it; from a just-started nginx it received none, and through the Envoy output none in any round. `proxy_ignore_client_abort` is off, nginx's default."},
	// The generator keeps nginx's own reading of the header, as it keeps nginx's own answers elsewhere: under forwarded
	// the header is the word of whoever is in front. The battery's row "an x-forwarded-proto that is neither http nor
	// https" holds it.
	{Row: "auth.send_request", Name: "under forwarded, an x-forwarded-proto that is neither http nor https is told to the check as it arrived",
		Measured: "With `client_address: forwarded` and `auth.send_request: true`, a request sent over plain HTTP with `X-Forwarded-Proto: ftp`, such as `GET /products/1 HTTP/1.1`, is told `x-forwarded-proto: ftp` in the request to the check, where the Envoy output tells the check `http`, the connection's scheme. Both send the service `X-Forwarded-Proto: ftp`. nginx tells the check the header's value as it arrived, whatever it holds, so a check that builds a URL from the scheme it is told reads the word of the client, or of the proxy in front. Under `connection` both tell the check and the service the connection's scheme."},
	// grpc_next_upstream tries the next backend as soon as a connection fails, and a retried route's error_page goes to
	// the next backend's copy as soon as a try fails: no directive of nginx's gRPC module or of error_page sets a wait
	// before a retry or a limit on the retries in flight (nginx.org, ngx_http_grpc_module and ngx_http_core_module).
	{Row: "routes[].rpc.retries", Name: "a retried request or call is sent again at once and with no limit on retries at the same time",
		Measured: "A call whose first connection was refused is sent to the next backend at once, where the Envoy output waits 25 ms, and every such call is retried, where the Envoy output retries at most 3 at the same time and fails the others. A request of a retried route is retried at once and with no limit too: at 1,000 requests a second for 60 s, each first try refused, nginx served every one of the 60,000 or 60,001 requests on a second try in each of ten runs, where the Envoy output served 13,452 to 13,765 and answered the others 502, and nginx's 99th percentile was 0.49 ms on 1.30.6 and 0.45 ms on 1.31.8, where the Envoy output's was 24.4 ms (the medians of four and five runs on mains power; measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2)."},
	{Row: "log", Name: "a chunked answer its service cuts short writes no upstream-failure line",
		Measured: "A service that closes its connection after the first chunk of a chunked `200` answer reaches the client as it does through the Envoy output, which then writes the request line with the flag `UPE`, whatever `log` says, and a `gateway_upstream_failure` line; nginx writes no upstream-failure line, and the request line only when `log` takes the request by its status or its share: nginx records the answer as complete, and with no content-length there is no length to compare the bytes it received to. An answer cut short before its content-length is logged as the Envoy output logs it."},
}
