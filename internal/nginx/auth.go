package nginx

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// The auth check is asked by the handler check of proxyfile.js, in the access phase of each checked route's location,
// with a subrequest to checkLocation, which proxies it to the relay, a stream server of the same script on a local
// socket; the relay sends the check its question, reads the answer whole and hands the handler only what it read
// (script.js). Every name below is one the script reads or sets.

// timeoutHeader carries auth.timeout in milliseconds to the check, as the Envoy output's check request does
// (filter-ext-authz.yaml in internal/envoy/fragments).
const timeoutHeader = "x-gateway-timeout-ms"

// toldHeaders are the headers send_request tells the check the request by, as the Envoy output names them.
var toldHeaders = []string{"x-forwarded-uri", "x-forwarded-method", "x-forwarded-proto", "x-forwarded-host"}

// asked returns auth.ask when the check is asked, written and not switched off, or nil.
func asked(g *model.Gateway) *model.Auth {
	if g.Auth == nil || !g.Auth.Enabled {
		return nil
	}
	return g.Auth
}

// handlerAccepts returns the accept names the handler sets from an allowed check's answer, in order: every one but a
// name of generator.DroppedByEnvoy, which the Envoy output never sets on the request it passes on. Name n of the list
// is set in $pf_accept_<n>.
func handlerAccepts(a *model.Auth) []model.Header {
	if a == nil {
		return nil
	}
	var accepted []model.Header
	for _, header := range a.Accept {
		if !slices.Contains(generator.DroppedByEnvoy, strings.ToLower(header.Name)) {
			accepted = append(accepted, header)
		}
	}
	return accepted
}

// relayFolder is the folder of the relay's socket file, one for each listen port, in /run, where the official image's
// nginx writes its pid file. nginx has no socket in Linux's abstract namespace: unix:@name is a file named @name in the
// folder nginx is started from, with the mode 0666 nginx gives every unix listener, which any local user could connect
// to. nginx makes each temporary path of the configuration at start, before it binds a listener, owned by the
// workers' user with mode 0700, so the relayFolder written as the temporary path of a module the file never uses
// (scgi) is a folder only nginx's own users can enter (measured on nginx 1.30.6 and 1.31.8, as uid 65534). nginx makes
// it so only when it is missing: it takes over a folder it finds and keeps its mode, so the folder must not exist before
// nginx first starts, or must be nginx's own with mode 0700 (measured: one uid 65534 made first with mode 0777 became
// nginx's with mode 0777, and that user could list it).
func relayFolder(listen int64) string {
	return "/run/proxyfile-" + strconv.FormatInt(listen, 10)
}

// folderLines name relayFolder as the temporary path of a module the file never uses, so that nginx makes it at start,
// once in a file: with the check's lines when the check is asked, and otherwise with the collector relay's
// (collectorLines). The comment names the sockets the folder holds.
func folderLines(g *model.Gateway) []string {
	sockets := "auth check's socket"
	switch {
	case asked(g) == nil:
		sockets = "collector relay's socket"
	case relayed(otelTracing(g)):
		sockets = "auth check's and the collector relay's sockets"
	}
	return []string{
		"  # The folder of the " + sockets + ": nginx makes a temporary path at start, before it binds, for its workers' user alone.",
		"  scgi_temp_path " + relayFolder(g.Listen.Number) + ";",
	}
}

// relaySocket is the socket file the check location proxies to and the relay listens on. nginx removes it when it
// stops; a kill leaves it, and nginx then does not start again until it is removed (measured), which the auth.ask row
// of the support table and docs/nginx.md state.
func relaySocket(listen int64) string {
	return relayFolder(listen) + "/check.sock"
}

// checkLines returns the auth check's lines of the http block and the stream block, both nil when the check is not
// asked.
func checkLines(in generator.Input, v *values, e edge) (http, stream []string) {
	a := asked(in.Gateway)
	if a == nil {
		return nil, nil
	}
	// The handler answers a check's redirect itself, and nginx writes a relative Location of its own answer as an
	// absolute one with the server's name and port; Envoy passes the check's Location on as the check sent it
	// (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with Location: /login?next=%2Fx). The directive changes
	// only the answers nginx makes itself, never a service's.
	http = append([]string{"  absolute_redirect off;"}, folderLines(in.Gateway)...)
	// The handler sets these, so each is declared with js_var, which reads empty where nothing set it.
	for _, name := range []string{"checking", "check_failed", "unchecked", "authz_us", "then", "action", "reports"} {
		http = append(http, "  js_var $pf_"+name+";")
	}
	// The handler sets an accepted header the check sent empty as one space, which nginx sends, where it sends no
	// header whose value is empty. A gRPC location sends it as nothing: over HTTP/2 a value that starts with a space is
	// malformed (RFC 9113, section 8.2.1), and a server may reset the call for it, where Envoy sends the value empty.
	// An RPC route's locations send each accepted header through its _grpc map.
	rpc := in.Gateway.RPCEntry() != nil
	for n := range handlerAccepts(a) {
		http = append(http, fmt.Sprintf("  js_var $pf_accept_%d;", n))
		if rpc {
			http = append(http, fmt.Sprintf(`  map $pf_accept_%d $pf_accept_%d_grpc { " " ""; default $pf_accept_%d; }`, n, n, n))
		}
	}
	// The handler answers 500 to a request its accepted headers take past the header size, as Envoy does
	// (passable); the count is $pf_header_count, which every file writes.
	size, _ := headerLimits(in.Gateway.Limits)
	var sizePos model.Pos
	if written := in.Gateway.Limits.RequestHeaders; written != nil {
		sizePos = written.Size.Pos
	}
	http = append(http, `  map "" $pf_header_size { default `+v.token(headerSizeKey, sizePos, strconv.Itoa(size))+"; }")
	// With client_address connection Envoy counts the x-forwarded-for and x-envoy-external-address it adds, each
	// holding the peer's address, so the handler counts them from the address it is given here.
	address := `""`
	if in.Gateway.ClientAddress == model.ClientConnection {
		address = "$remote_addr"
	}
	http = append(http, `  map "" $pf_counted_address { default `+address+"; }")
	if a.SendRequest {
		// The Envoy output tells the check the path its route matched, normalised as the route sends it, before a
		// rewrite, with the query as the client wrote it, and, under forwarded, the scheme of a target written with
		// one, in any letter case, over the connection's or the client's X-Forwarded-Proto (measured on Envoy 1.40.2).
		// Inside the subrequest $request_uri and $request are the client's and $pf_path keeps the value the route
		// choice read; the query comes from $request_uri, as $args is the subrequest's.
		// With connection the client that writes the target is not trusted for its scheme, so the check is told the
		// connection's, the one the service is sent, as docs/auth.md says: the target's would tell it https for an
		// https target sent over plain HTTP, which nginx serves and the Envoy output answers 403 (measured on nginx
		// 1.30.6 and 1.31.8 beside Envoy 1.40.2). Over TLS an http target is then told https, as by the Envoy output,
		// which under connection writes the listener's scheme as text (filter-ext-authz.yaml).
		toldProto := `  map $request $pf_told_proto { ` + quoted(`~*^[^ ]+ +https://`) + ` https; ` + quoted(`~*^[^ ]+ +http://`) +
			` http; default $pf_forwarded_proto; }`
		if e.connection {
			toldProto = `  map "" $pf_told_proto { default $pf_forwarded_proto; }`
		}
		http = append(http,
			`  map $request_uri $pf_query { `+regexKey(`^[^?]*(?<pf_query_text>\?.*)`)+` $pf_query_text; default ""; }`,
			"  map $pf_mode $pf_told_uri { uri $pf_path$pf_query; default $request_uri; }",
			toldProto,
		)
	}
	service := in.Gateway.Service(a.Service.Value)
	port, _ := service.Port(model.HTTP)
	listen := strconv.FormatInt(in.Gateway.Listen.Number, 10)
	// The relay's js_import is read from the configuration's folder in the stream block too (measured). The stream
	// block reads no line of the http block, so a check's service by DNS name is looked up through its own resolver
	// line, and its upstream is the service's, balanced and looked up again as the service's own upstreams are.
	stream = []string{"stream {", "  js_engine njs;", "  js_import pf from " + scriptName + ";"}
	if in.Gateway.Resolver != nil {
		stream = append(stream, "  "+resolverLine(in.Gateway.Resolver))
	}
	stream = append(stream, upstreamLines(v, "proxyfile_"+listen+"_check", *service, port, e)...)
	ms := strconv.FormatInt(a.Timeout.Millis, 10)
	// The relay writes the path send_request tells the check as Envoy tells it, in the header $pf_check_uri names, the
	// proxy's own, and every other header as Envoy sends one; without send_request a client's own copy of that name is
	// any other header.
	told := `""`
	if a.SendRequest {
		told = toldHeaders[0]
	}
	// The relay sends the check no header that $pf_check_send does not cover, so the list names every header the
	// check receives besides Host: authorization, which the Envoy output always sends, the send entries, the proxy's
	// own and, with tracing, the trace, which the Envoy output's check request carries too. A check is tried once, as
	// the Envoy output tries it; proxy_timeout ends one that sends nothing.
	// Every check connects to the relay's socket, and one the socket's listen queue has no room for fails at once: with
	// nginx's own backlog of 511 the kernel held 512 connections, and a burst of 600 clients filled it with a healthy
	// check. The kernel holds the smaller of backlog and net.core.somaxconn, so the largest backlog listen takes leaves
	// the bound to the kernel: 4,097 connections with the somaxconn of 4,096 in the pinned images (measured on nginx
	// 1.30.6 and 1.31.8 with the workers stopped).
	stream = append(stream,
		"  }",
		"  server {",
		"    listen unix:"+relaySocket(in.Gateway.Listen.Number)+" backlog=2147483647;",
		"    set $pf_check_send "+v.token(sendKey(a), sendPos(a), quoted(strings.Join(checkSend(a, e.traced), " ")))+";",
		"    set $pf_check_ms "+ms+";",
		"    set $pf_check_uri "+told+";",
	)
	if e.traced {
		// The trace the script wrote reaches the check with the client's bytes, which asEnvoySends would change.
		stream = append(stream, "    set $pf_check_as_sent "+quoted(strings.Join(traceNames, " "))+";")
	}
	stream = append(stream,
		"    proxy_pass proxyfile_"+listen+"_check;",
		"    proxy_next_upstream off;",
		"    proxy_connect_timeout "+nginxTime(a.Timeout.Millis)+";",
		"    proxy_timeout "+nginxTime(a.Timeout.Millis)+";",
		"    js_filter pf.relay;",
		"  }",
		"}",
	)
	return http, stream
}

// checkSend is every entry the relay passes to the check, in lower case, a prefix written with its *: with traced, the
// trace's two names after the others, each unless send names it already.
func checkSend(a *model.Auth, traced bool) []string {
	send := []string{"authorization"}
	for _, header := range a.Send {
		name := strings.ToLower(header.Name)
		if header.Prefix {
			name += "*"
		}
		if !slices.Contains(send, name) {
			send = append(send, name)
		}
	}
	send = append(send, timeoutHeader, generator.InternalHeader, generator.ExpectedTimeoutHeader,
		generator.QuestionHeaders[0].Name, generator.QuestionHeaders[1].Name)
	if a.SendRequest {
		send = append(send, toldHeaders...)
	}
	if traced {
		for _, name := range traceNames {
			if !slices.Contains(send, name) {
				send = append(send, name)
			}
		}
	}
	return send
}

// sendKey and sendPos name the value the relay's list is refused by when nginx cannot read it: auth.send, or auth.ask
// when send is not written and the list holds the proxy's own names alone.
func sendKey(a *model.Auth) string {
	if len(a.Send) == 0 {
		return "auth.ask"
	}
	return "auth.send"
}

func sendPos(a *model.Auth) model.Pos {
	if len(a.Send) == 0 {
		return a.Service.Pos
	}
	return a.Send[0].Pos
}

// writeCheck writes the auth check's lines of an environment server: the values the handler reads, the check
// location, which proxies the handler's subrequest to the relay and sends a client's own request for its path on to
// target, the named location location / sends every request to, and the location its failures go to.
func writeCheck(w *writer, v *values, a *model.Auth, listen int64, e edge, target string) {
	unavailable := "deny"
	if a.Allow {
		unavailable = "allow"
	}
	names := make([]string, 0, len(e.accept))
	for _, header := range e.accept {
		names = append(names, strings.ToLower(header.Name))
	}
	accept := `""`
	if len(names) > 0 {
		accept = v.token("auth.accept", e.accept[0].Pos, quoted(strings.Join(names, " ")))
	}
	ms := strconv.FormatInt(a.Timeout.Millis, 10)
	// The model keeps no position of only_with_header, so its refusal names ask's.
	onlyWith := v.token("auth.only_with_header", a.Service.Pos, quoted(strings.ToLower(a.OnlyWithHeader)))
	w.lines(
		"    set $pf_when_unavailable "+unavailable+";",
		"    set $pf_only_with "+onlyWith+";",
		"    set $pf_accept "+accept+";",
		"    set $pf_check_ms "+ms+";",
		// A client's own request for this path is routed as any other: only the handler's subrequest has set
		// $pf_checking.
		"    location = "+checkLocation+" {",
		`      if ($pf_checking = "") {`,
		"        return 418;",
		"      }",
		"      error_page 418 = "+target+";",
	)
	h := locationHeaders{w: w, set: "proxy_set_header", sent: map[string]bool{}}
	// With tracing the check's request carries the trace the route's request carries, the server span's, so the
	// check's own spans join the request's trace (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, whose check
	// request carries its client span's), and beside a malformed traceparent neither header, as the Envoy output's.
	h.traceHeaders(e, checkTrace)
	w.lines(
		"      proxy_pass http://unix:"+relaySocket(listen)+";",
		"      proxy_pass_request_body off;",
	)
	h.send("content-length", "Content-Length", `""`)
	h.send("connection", "Connection", `""`)
	// The HTTP2-Settings the route sends the service: the Envoy output's check never receives an h2c upgrade's, and
	// receives a client's other one when send covers it (measured on Envoy 1.40.2); without this line the subrequest
	// kept an h2c upgrade's, and the relay passed it on.
	h.send("http2-settings", "HTTP2-Settings", "$pf_http2_settings")
	// The check is asked with the host nginx routes by, never the Host a client wrote beside another target.
	h.send("host", "Host", "$pf_authority")
	h.send(methodHeader, methodHeader, "$request_method")
	h.send(pathHeader, pathHeader, v.token("auth.ask", a.Service.Pos, quoted(dollars(a.Path))))
	h.send(timeoutHeader, timeoutHeader, ms)
	h.send(generator.InternalHeader, generator.InternalHeader, "true")
	h.send(generator.ExpectedTimeoutHeader, generator.ExpectedTimeoutHeader, ms)
	// The route location of an RPC method sets its question; every other leaves both empty, and nginx sends no
	// header whose value is empty.
	h.send(generator.QuestionHeaders[0].Name, generator.QuestionHeaders[0].Name, "$pf_action")
	h.send(generator.QuestionHeaders[1].Name, generator.QuestionHeaders[1].Name, "$pf_reports")
	if a.SendRequest {
		h.send(toldHeaders[0], toldHeaders[0], "$pf_told_uri")
		h.send(toldHeaders[1], toldHeaders[1], "$request_method")
		h.send(toldHeaders[2], toldHeaders[2], "$pf_told_proto")
		h.send(toldHeaders[3], toldHeaders[3], "$pf_authority")
	}
	// The check receives the client's other headers as the service does, each the relay passes on only when
	// $pf_check_send covers it: the same values, the same removals, and no client's copy of a name the check sets or
	// is asked with.
	h.forwarding(e)
	h.removedByEnvoy(e)
	h.fromClients(v, e)
	h.done(v)
	w.lines(
		"      proxy_connect_timeout "+nginxTime(a.Timeout.Millis)+";",
		// Above auth.timeout, so the handler's own timer ends a slow check first.
		"      proxy_read_timeout "+nginxTime(a.Timeout.Millis+1000)+";",
		// With buffering on, nginx wrote a denial of 1 MiB to a temporary file, and with proxy_max_temp_file_size 0
		// instead the subrequest never ended and the client got no answer; unbuffered, it is answered from memory
		// (measured on nginx 1.30.6 and 1.31.8).
		"      proxy_buffering off;",
		// The relay's answer is at most 1,131,712 bytes: its lengths, the 81,920 bytes of a check's head the relay
		// reads, its count of lines and a body of 1 MiB.
		"      subrequest_output_buffer_size 1200k;",
		"      error_page 502 504 = @pf_check_failed;",
		"    }",
		// The handler tells a check that was never answered from one it could not read by what nginx got from the
		// relay, and a connection to the relay that nginx never made, whose connect time is "-", from a relay that was
		// reached.
		"    location @pf_check_failed {",
		`      set $pf_check_failed "s=$upstream_status b=$upstream_bytes_received c=$upstream_connect_time ";`,
		"      return 502;",
		"    }",
	)
}

// dotparamLocation is the location the route choice names for a path with a dot segment that still carries a ;
// parameter when the check is asked. The Envoy output's route that refuses such a path is checked, whatever route the
// path would take, a route with auth: off and a preflight included, so the handler asks first and answers the 400
// itself once the check lets the request through ($pf_then): a return here would answer before the access phase.
func dotparamLocation(own []string) []string {
	return slices.Concat([]string{"    location @pf_r_dotparam {"}, own, []string{"      set $pf_then refused;"}, handlerLines, []string{"    }"})
}

// handlerLines are the lines of a location the handler checks. A refusal the handler writes with no body carries no
// Content-Type, as the Envoy output's carries none: nginx gives an answer it writes with no type of its own the type of
// the path's extension in its types, which the http block empties, or else default_type, text/plain, which the
// location empties, and the handler writes text/plain itself for a body the check sent with no type (measured on nginx
// 1.30.6 and 1.31.8 beside Envoy 1.40.2).
var handlerLines = slices.Concat(untyped, []string{"      js_access pf.check;"})

// questionLines are the lines of a checked route location of an RPC method with a question, which the check location
// sends in the question headers, each value from the file through quoted and $pf_dollar.
func questionLines(v *values, q generator.Question, pos model.Pos) []string {
	var lines []string
	if q.Action != "" {
		lines = append(lines, "      set $pf_action "+v.token("rpc.methods[].action", pos, quoted(dollars(q.Action)))+";")
	}
	if q.Reports != nil {
		lines = append(lines, "      set $pf_reports "+v.token("rpc.methods[].reports", pos, quoted(dollars(strings.Join(q.Reports, ","))))+";")
	}
	return lines
}

// locationHeaders writes the request header lines of one location, each name once: the first line written for a name is
// the one nginx sends, and a later one is dropped.
type locationHeaders struct {
	w    *writer
	set  string // proxy_set_header or grpc_set_header
	sent map[string]bool
	// names are the names written, in order, and written the key and position of each that a user's list gave, for
	// the table nginx builds of them (headerTables).
	names   []string
	written map[string]written
}

// written is the key and the position of a header name a user's list gave.
type written struct {
	key string
	pos model.Pos
}

// done keeps the location's names for headerTables.
func (h *locationHeaders) done(v *values) {
	v.tables = append(v.tables, headerTable{grpc: h.set == "grpc_set_header", names: h.names, written: h.written})
}

func (h *locationHeaders) send(name, written, value string) {
	if h.sent[name] {
		return
	}
	h.sent[name] = true
	h.names = append(h.names, name)
	h.w.lines("      " + h.set + " " + written + " " + value + ";")
}

// forwarding sends the request id, the scheme, the Origin and, with client_address connection, the client's address,
// as the Envoy output sets them.
func (h *locationHeaders) forwarding(e edge) {
	h.send("x-request-id", "X-Request-Id", "$pf_request_id")
	h.send("x-forwarded-proto", "X-Forwarded-Proto", "$pf_forwarded_proto")
	// The service receives the Origin as one line, the lines Envoy joins (writeOrigin).
	if origin := e.origin(); origin != "" {
		h.send("origin", "Origin", origin)
	}
	if e.connection {
		h.send("x-forwarded-for", "X-Forwarded-For", "$pf_forwarded_for")
		h.send("x-envoy-external-address", "x-envoy-external-address", "$remote_addr")
	}
}

// removedByEnvoy empties each header the Envoy output removes, which nginx then does not send: with tracing
// format.NginxRemovedTraced under either client_address, which connection removes already.
func (h *locationHeaders) removedByEnvoy(e edge) {
	for _, header := range generator.RemovedByEnvoy {
		h.send(header, header, `""`)
	}
	if e.connection {
		for _, header := range generator.RemovedByEnvoyFacingClients {
			h.send(header, header, `""`)
		}
	}
	if e.traced {
		for _, header := range format.NginxRemovedTraced {
			h.send(header, header, `""`)
		}
	}
}

// accepted sets each accept name the handler read from an allowed check's answer, and the unchecked mark, from the
// handler's variables, on a checked route's location.
func (h *locationHeaders) accepted(v *values, e edge) {
	for n, header := range e.accept {
		name := strings.ToLower(header.Name)
		value := fmt.Sprintf("$pf_accept_%d", n)
		if h.set == "grpc_set_header" {
			value += "_grpc"
		}
		h.from(name, "auth.accept", header.Pos)
		h.send(name, v.keyToken("auth.accept", header.Pos, quoted(name)), value)
	}
	h.send(generator.UncheckedMark, generator.UncheckedMark, "$pf_unchecked")
}

// fromClients empties each name of the early header removal not sent already, then, with auth.ask, the unchecked
// mark, so a client's own copy reaches neither the service nor the check.
func (h *locationHeaders) fromClients(v *values, e edge) {
	for _, removal := range e.remove {
		name := strings.ToLower(removal.header.Name)
		form := name
		if removal.key != "" {
			form = v.keyToken(removal.key, removal.header.Pos, quoted(name))
			h.from(name, removal.key, removal.header.Pos)
		}
		h.send(name, form, `""`)
	}
	if e.mark {
		h.send(generator.UncheckedMark, generator.UncheckedMark, `""`)
	}
}

// from keeps the key and position a user's list gave name at, the first one only.
func (h *locationHeaders) from(name, key string, pos model.Pos) {
	if h.written == nil {
		h.written = map[string]written{}
	}
	if _, ok := h.written[name]; !ok {
		h.written[name] = written{key, pos}
	}
}

// addressKey is the key of a service's addresses as the support table names it: addresses holds two addresses or
// more, so one is address's.
func addressKey(service *model.Service) string {
	if len(service.Addresses) == 1 {
		return "services.<service name>.address"
	}
	return "services.<service name>.addresses"
}
