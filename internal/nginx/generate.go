// Package nginx writes the nginx configuration of a gateway, which picks the route of each request as the Envoy
// configuration of the same input does and sends the request to the same service, and the script beside it, which
// reads what nginx's own directives cannot: the name of each header of a request.
package nginx

import (
	_ "embed"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/origins"
	"github.com/kitecorp/proxyfile/v2/internal/pcre"
)

// route is one place of the route choice: a plain route, or one method of the rpc entry, whose methods take the
// entry's place sorted by path, as the Envoy configuration orders its routes.
type route struct {
	key     string // the route map's key: the regular expression the path must match, as the file writes it
	method  string // the method map's key, as the file writes it, or "" for a route that takes any method
	rewrite string // the rewrite line, which sets the path the service receives, or "" for a route with no rewrite
	pass    string // the proxy_pass or grpc_pass line's target
	grpc    bool
	host    string // the Host the service receives, as the file writes it
}

// scriptName is the name of the script the configuration loads, written beside it: nginx reads a relative name in
// js_import from the folder of the configuration it was started with.
const scriptName = "proxyfile.js"

// scriptBody is the script after its header line (script.js beside this file).
//
//go:embed script.js
var scriptBody string

// script is the script beside the configuration: generator.Header in the comment syntax of JavaScript, then the body.
func script() []byte {
	return []byte("// " + strings.TrimPrefix(generator.Header, "# ") + "\n" + scriptBody)
}

// edge is what the gateway does with a client's own headers before it routes the request. connection is true when the
// client's address is the connection's peer, client_address connection, which the reader fills in for a file with tls
// and no client_address; removed holds each remove_from_clients name in lower case, and remove the entries as written,
// in order, which every route location empties.
type edge struct {
	connection bool
	removed    map[string]bool
	remove     []model.Header
}

func edgeOf(g *model.Gateway) edge {
	e := edge{connection: g.ClientAddress == model.ClientConnection, removed: map[string]bool{}, remove: g.RemoveFromClients}
	for _, header := range g.RemoveFromClients {
		e.removed[strings.ToLower(header.Name)] = true
	}
	return e
}

// read is the variable that holds the client's header name, or "" when remove_from_clients removes it: the Envoy output
// removes the client's copy before any filter reads it, so a removed Origin is no origin, a removed
// Access-Control-Request-Method makes no preflight and a removed X-Request-Id gets a new id. The request flags
// (writeRequestChecks, writeUpgradeMaps) read the client's copy whatever is removed: the Envoy output refuses those
// requests before it removes anything, and a flag that reads the client's copy can only refuse more.
func (e edge) read(name string) string {
	if e.removed[name] {
		return ""
	}
	return "$http_" + strings.ReplaceAll(name, "-", "_")
}

// origin is the variable that holds the request's Origin as the Envoy output reads it (writeOrigin), or "" when
// remove_from_clients removes it, as read gives.
func (e edge) origin() string {
	if e.removed["origin"] {
		return ""
	}
	return "$pf_origin"
}

// writeOrigin writes $pf_origin, the Origin every origin rule reads and every route location sends, when
// remove_from_clients does not remove it. Envoy joins the Origin lines that are not empty with a comma, each without
// the spaces and tabs around it over HTTP/1 (its HTTP/2 codec keeps them), and refuses an Origin whose every line is
// empty as an origin on none of the hosts (measured on Envoy 1.40.2); nginx's $http_origin is the same empty text for
// an empty Origin and for none, keeps a tab around a value, and joins two lines with a comma and a space. The script's
// origin reads the lines as Envoy does: n for none, e when every line is empty, and o with the lines joined. e, and
// the empty value of a run that failed, give " ", an origin no key admits, so the check refuses it.
func writeOrigin(w *writer, e edge) {
	if e.origin() == "" {
		return
	}
	w.lines(
		"  js_set $pf_js_origin pf.origin;",
		`  map $pf_js_origin $pf_origin { n ""; `+regexKey(`^o(?<pf_origin_value>.+)\z`)+` $pf_origin_value; default " "; }`,
	)
}

// mapSource is what a map reads: text, or nginx's empty text when text is "", as read gives for a removed header.
func mapSource(text string) string {
	if text == "" {
		return `""`
	}
	return text
}

// valueMap is the map that sets variable to empty when source reads empty and to otherwise when it does not. A source
// that is "" always reads empty, so its map writes empty alone: otherwise may read the removed header itself.
func valueMap(source, variable, empty, otherwise string) string {
	if source == "" {
		return fmt.Sprintf(`  map "" %s { default %s; }`, variable, empty)
	}
	return fmt.Sprintf(`  map %s %s { "" %s; default %s; }`, source, variable, empty, otherwise)
}

// newRequestID writes nginx's own request id, $request_id, 32 random hexadecimal digits in lower case, as Envoy writes
// the id it makes, a version 4 UUID in lower case (RFC 9562; measured on Envoy 1.40.2 and, with this map, on nginx
// 1.30.6 and 1.31.8): the thirteenth digit becomes the version, 4, and the seventeenth the variant, 8, 9, a or b by its
// two low bits, so 122 bits stay random, as in Envoy's. $request_id always matches one key; the default stands for none.
func newRequestID() []string {
	lines := []string{"  map $request_id $pf_new_request_id {"}
	for _, variant := range []struct{ digits, written string }{{"048c", "8"}, {"159d", "9"}, {"26ae", "a"}, {"37bf", "b"}} {
		lines = append(lines, "    "+regexKey(`^(?<pf_id1>[0-9a-f]{8})(?<pf_id2>[0-9a-f]{4})[0-9a-f](?<pf_id3>[0-9a-f]{3})[`+variant.digits+
			`](?<pf_id4>[0-9a-f]{3})(?<pf_id5>[0-9a-f]{12})\z`)+" $pf_id1-$pf_id2-4$pf_id3-"+variant.written+"$pf_id4-$pf_id5;")
	}
	return append(lines, "    default $request_id;", "  }")
}

// writeForwardingMaps writes the values the routes send as X-Request-Id, X-Forwarded-Proto and, with client_address
// connection, X-Forwarded-For. With connection the Envoy output makes a new request id whatever the client sent, sends
// the connection's scheme, and appends the peer's address to the client's X-Forwarded-For with a comma and no space;
// with forwarded it keeps the client's id and scheme when it sent them, and sends X-Forwarded-For as received, which
// nginx does with no line (measured on Envoy 1.40.2). A header remove_from_clients removes reads as empty.
func writeForwardingMaps(w *writer, e edge) {
	requestID, proto := e.read("x-request-id"), e.read("x-forwarded-proto")
	if e.connection {
		requestID, proto = "", ""
	}
	w.lines(newRequestID()...)
	w.lines(
		valueMap(requestID, "$pf_request_id", "$pf_new_request_id", requestID),
		valueMap(proto, "$pf_forwarded_proto", "$scheme", proto),
	)
	if e.connection {
		forwardedFor := e.read("x-forwarded-for")
		w.lines(valueMap(forwardedFor, "$pf_forwarded_for", "$remote_addr", `"`+forwardedFor+`,$remote_addr"`))
	}
}

// readTimeout is how long nginx waits for a service's answer, counted from the request's end and then between two
// reads. Without timeouts.request the Envoy output ends a request 15 s after its end (the route's timeout, Envoy's
// default), which nginx cannot write for a whole answer: 15 s between two reads gives a service that says nothing the
// same 504 at the same time, and lets an answer that keeps coming run past 15 s, the difference timeouts.request's
// declaration names. With timeouts.request: none only Envoy's 5 minutes of silence are left.
func readTimeout(request *model.Time) string {
	if request == nil {
		return "15s"
	}
	return "5m"
}

// Generate returns the whole nginx.conf as the configuration and the script it loads, proxyfile.js. Its error is a
// generator.Refused that names each value whose nginx form is longer than nginx reads, by its key; any other error
// means a route goes to a service that is not declared or has no port of the kind the route needs, a pattern holds
// what the pattern writer does not know, or a token the file writes is longer than nginx reads although no value
// was refused: a bug in proxyfile.
func Generate(in generator.Input) ([]generator.File, error) {
	g := in.Gateway
	e := edgeOf(g)
	var v values
	routes, err := routesOf(in, &v)
	if err != nil {
		return nil, err
	}
	var w writer
	w.WriteString(generator.Header)
	w.lines(
		// The script reads each header's name, which no directive of nginx's own reads (writeRequestChecks); the
		// version is the one the pinned images ship, which the battery and the validator run.
		"# Needs the njs module, 1.0.2 or later.",
		"load_module modules/ngx_http_js_module.so;",
		"worker_processes auto;",
		// nginx logs a request line, with its query, which can carry a token, at error (a refused service) and at
		// alert (a pattern PCRE2 gave up on), so its own log keeps emerg alone, which still shows a file it cannot
		// load, until a later version writes log.
		"error_log stderr emerg;",
		// The official image's own nginx.conf writes 1024; nginx's default is 512.
		"events {",
		"  worker_connections 1024;",
		"}",
		"http {",
		// Envoy admits 100 request headers when limits.request_headers is not written (max_headers_count's default),
		// and nginx 1000 (the core module page). Measured on Envoy 1.40.2 and nginx 1.31.8 with this line: both admit
		// and refuse at the same count.
		"  max_headers 100;",
		// nginx refuses a request body over 1 MiB by default (the core module page); the Envoy configuration writes no
		// limit on a body's size. nginx also keeps a whole body before it sends any of it, where Envoy sends each part
		// on as it arrives: a body sent over 2 s reached the service over 2 s through Envoy and at once through nginx
		// until request buffering was off (measured). The gRPC module never keeps one.
		"  client_max_body_size 0;",
		"  proxy_request_buffering off;",
		// nginx drops a request header whose name holds _ or another character than a letter, a digit or -, where
		// Envoy passes every name of RFC 9110's token characters, x_user_id and x.dot among them (measured). With
		// these, nginx keeps every name, and the request checks refuse one outside the token characters, as Envoy
		// does; without them nginx would drop such a header before the script reads it, and serve the request.
		"  underscores_in_headers on;",
		"  ignore_invalid_headers off;",
		// nginx's own answers name nginx without its version.
		"  server_tokens off;",
		// nginx's request log is not the request log the Envoy configuration writes; a later version writes log.
		"  access_log off;",
		// nginx acts on these headers of a service's answer unless it ignores them: X-Accel-Redirect makes it serve
		// another URI the service names, the others change its caching, rate, buffering and charset. Envoy acts on
		// none of them (proxy_ignore_headers on the proxy module page).
		"  proxy_ignore_headers X-Accel-Redirect X-Accel-Expires X-Accel-Limit-Rate X-Accel-Buffering X-Accel-Charset;",
		"  grpc_ignore_headers X-Accel-Redirect X-Accel-Expires X-Accel-Limit-Rate X-Accel-Buffering X-Accel-Charset;",
		// A service's http port is spoken as HTTP/1.1, which a kept connection and a WebSocket upgrade both need;
		// nginx's default is 1.0.
		"  proxy_http_version 1.1;",
		// Envoy's cluster connect timeout is 5 seconds when not written; nginx's default is 60.
		"  proxy_connect_timeout 5s;",
		"  grpc_connect_timeout 5s;",
		// Envoy ends a request that nothing was read or written on for 5 minutes (stream_idle_timeout, not written by
		// the Envoy output) and closes a client's connection after an hour with no request (idle_timeout); nginx's
		// own limits are 60 s and 75 s. nginx counts its limits between two reads or writes, so they hold the same
		// silences. A service's answer has its own limit, readTimeout.
		"  client_header_timeout 5m;",
		"  client_body_timeout 5m;",
		"  send_timeout 5m;",
		"  proxy_read_timeout "+readTimeout(g.Timeouts.Request)+";",
		"  proxy_send_timeout 5m;",
		"  grpc_read_timeout "+readTimeout(g.Timeouts.Request)+";",
		"  grpc_send_timeout 5m;",
		"  keepalive_timeout 1h;",
		// nginx tries the next backend after an error or a timeout by default; Envoy tries a request once unless the
		// route writes retries, which this version does not write.
		"  proxy_next_upstream off;",
		"  grpc_next_upstream off;",
		// nginx drops a service's Server header and writes its own; the Envoy configuration passes the service's on.
		"  proxy_pass_header Server;",
		"  grpc_pass_header Server;",
	)
	w.lines(hashSizes(in, routes)...)
	writeTLS(&w, &v, g.TLS)
	w.lines(
		"",
		// An answer with no body is written "$pf_empty": return with an empty text answers nginx's own HTML page.
		`  map "" $pf_empty { default ""; }`,
	)
	// $pf_request_id is sent to the service and on every answer.
	writeForwardingMaps(&w, e)
	w.lines(
		// The authority nginx picked the server by, which Envoy sends the service as Host: the Host header (HTTP/2's
		// :authority) for a target in origin form, and the host an absolute target writes before its path. nginx
		// reads one or more spaces after the method, so the keys do too. Any other line, an absolute target whose host
		// a query or a fragment follows among them, names none, and the request flags answer it 400: $http_host would
		// hand the service any Host a client writes beside a line the keys do not read (measured with two spaces). An
		// authority that ends in a colon, an empty port, names none either, as Envoy answers it 400 (measured on Envoy
		// 1.40.2, where nginx sent the service Host: localhost:).
		`  map $request $pf_authority { `+regexKey(`^[^ ]+ +/`)+` $http_host; `+
			regexKey(`^[^ ]+ +[A-Za-z][A-Za-z0-9+.-]*://(?<pf_authority_host>[^/?# ]*[^/?#: ])(?=[/ ])`)+` $pf_authority_host; default ""; }`,
		// Envoy sends TE on only as trailers, and only when the client's TE holds trailers: in any letter case when
		// Connection names te, and in lower case alone when it does not (measured); nginx sends none.
		`  map $http_connection $pf_connection_te { `+quoted(`~*(?:^|,)[ \t]*te[ \t]*(?:,|\z)`)+` 1; default 0; }`,
		`  map "$pf_connection_te:$http_te" $pf_te { `+quoted(`~*^1:(?:.*,)?[ \t]*trailers[ \t]*(?:,|\z)`)+` trailers; `+
			regexKey(`^0:(?:.*,)?[ \t]*trailers[ \t]*(?:,|\z)`)+` trailers; default ""; }`,
		"",
	)
	writeRequestChecks(&w)
	writeUpgradeMaps(&w)
	writePathClasses(&w, slices.ContainsFunc(g.Routes, func(r model.Route) bool { return r.Match == model.Pattern }))
	w.lines(
		// The three path flags read $uri, the path nginx has decoded and normalised, and the route choice answers a
		// path that sets one 400 before any route. The first is a dot segment that still carries a ; parameter, which
		// Envoy's first route refuses too. The second is a backslash, which Envoy reads as a slash and nginx does not,
		// so /open/..\private would stay under /open/. The third is a control character, written or escaped, which
		// nginx alone refuses.
		"  map $uri $pf_f_dotparam { "+regexKey(`/\.\.?;`)+" 1; default 0; }",
		"  map $uri $pf_f_backslash { "+regexKey(`\\`)+" 1; default 0; }",
		"  map $uri $pf_f_control { "+regexKey(`[\x00-\x1F\x7F]`)+" 1; default 0; }",
	)
	writeOrigin(&w, e)
	w.lines(
		// A preflight is an OPTIONS with an Origin and an Access-Control-Request-Method, as Envoy's CORS filter reads
		// one; the route choice answers it when the origin check has let it through.
		`  map `+mapSource(e.origin())+` $pf_has_origin { "" 0; default 1; }`,
		`  map "$request_method:$pf_has_origin:`+e.read("access-control-request-method")+`" $pf_f_preflight { `+
			regexKey(`^OPTIONS:1:.`)+` 1; default 0; }`,
		// The Envoy output answers an upgrade other than WebSocket 403 with this body when the request carries an
		// Origin, and with none when it carries none, with or without cors (measured on Envoy 1.40.2).
		valueMap(e.origin(), "$pf_upgrade_refused", `""`, `"Invalid CORS request"`),
	)
	for i, environment := range in.Origins.Environments {
		writeOriginCheck(&w, &v, i+1, environment, e)
		writeCORSMaps(&w, &v, i+1, environment, in.Origins.CORS, e)
	}
	if hasDollar(g, in.Origins.CORS) {
		// A rewrite, an add_header value and a map's value read $ as the start of a variable, and nginx has no escape
		// for it: this variable holds a $.
		w.lines(`  geo $pf_dollar { default "$"; }`)
	}
	writeRouteChoice(&w, routes)
	for _, service := range g.Services {
		for _, port := range service.Ports {
			writeUpstream(&w, &v, service, port)
		}
	}
	w.lines("")
	listen := strconv.FormatInt(g.Listen.Number, 10)
	if g.TLS != nil {
		// Every public server serves TLS, the default one included, so a TLS server name or a Host no environment
		// names is answered over TLS too; the admin server stays plain on 127.0.0.1.
		listen += " ssl"
	}
	if g.Admin != nil {
		// Envoy's admin listens on 127.0.0.1 too. Of its pages nginx answers /ready alone, as Envoy answers it while
		// the gateway is live.
		w.lines(
			"  server {",
			"    listen 127.0.0.1:"+strconv.FormatInt(g.Admin.Number, 10)+";",
			"    location = /ready {",
			"      default_type text/plain;",
			`      return 200 "LIVE\n";`,
			"    }",
			"    location / {",
			`      return 404 "$pf_empty";`,
			"    }",
			"  }",
			"",
		)
	}
	// A Host that no environment names gets 404, as Envoy answers a request no virtual host takes, after the checks
	// Envoy makes before it reads the host. Every server writes http2 on, so a gRPC client reaches the plain listener
	// speaking HTTP/2 from its first byte.
	w.lines("  server {", "    listen "+listen+" default_server;", "    http2 on;")
	w.lines(requestChecks...)
	w.lines(tlsServerLines(g.TLS, nil)...)
	w.lines(
		"    location / {",
		`      return 404 "$pf_empty";`,
		"    }",
		"  }",
		"",
	)
	locations := routeLocations(routes, &v, e)
	grpc := slices.ContainsFunc(routes, func(r route) bool { return r.grpc })
	for place, environment := range in.Origins.Environments {
		server, preflight := corsLines(&v, place+1, environment, in.Origins.CORS, e, grpc)
		// A location with an add_header line of its own inherits none of the server's (the headers module page). Where
		// the server sends Access-Control lines, the answers no service gives repeat the server's other two lines
		// alone, so they carry no Access-Control line whatever the maps give.
		var own []string
		if server != nil {
			own = answerHeaders
		}
		hosts := make([]string, len(environment.Hosts))
		for i, host := range environment.Hosts {
			hosts[i] = v.token(hostsKey, environment.Pos, quoted(host))
		}
		// The name is written as it is: a comment ends at the end of its line, and no file proxyfile reads lets a
		// name hold a line break, because input.CheckedWith refuses every control character but tab.
		w.lines(
			"  "+v.token("origins.<environment name>", environment.Pos, "# The environment "+environment.Name+"."),
			"  server {",
			"    listen "+listen+";",
			"    http2 on;",
			"    server_name "+strings.Join(hosts, " ")+";",
			// A route's location answers a refused service through a second error_page, which nginx follows only
			// with this.
			"    recursive_error_pages on;",
			"    add_header x-request-id $pf_request_id always;",
			"    add_header Vary Origin always;",
		)
		w.lines(server...)
		w.lines(requestChecks...)
		w.lines(
			// nginx reads a host with a dot at its end as the host without it; Envoy compares the host as written,
			// so such a host is on no virtual host and gets 404 (measured).
			"    if ($pf_f_host_dot) {",
			`      return 404 "$pf_empty";`,
			"    }",
			// The origin check comes before a location is chosen, so before the route choice and its path flags, as
			// Envoy's RBAC filter runs before its router. The 403's body is the Envoy output's.
			fmt.Sprintf("    if ($pf_o%d) {", place+1),
			`      return 403 "Invalid CORS request";`,
			"    }",
		)
		// The TLS answers come after the server's checks, which stay first in the order Envoy makes them; nginx
		// applies an error_page and finds a named location wherever the server writes them.
		w.lines(tlsServerLines(g.TLS, own)...)
		w.lines(
			// Every request goes on to the named location the route choice names: error_page takes a target that holds a
			// variable (the core module page), and the 418 is only the step to it. Every other location is a named one,
			// which no request path can reach: an exact, prefix or regular-expression location would be tried in
			// nginx's own order, before the route choice and in place of it.
			"    location / {",
			"      return 418;",
			"      error_page 418 = @pf_r_$pf_route;",
			"    }",
		)
		w.lines(answerLocation("pf_r_refused", own, `return 400 "$pf_empty";`)...)
		w.lines(answerLocation("pf_r_upgrade-refused", own, `return 403 "$pf_upgrade_refused";`)...)
		// Envoy's CORS filter answers a preflight it lets through itself, 200 with no body, and the service never sees
		// it.
		w.lines(answerLocation("pf_r_preflight", preflight, `return 200 "$pf_empty";`)...)
		w.lines(answerLocation("pf_r_no-route", own, `return 404 "$pf_empty";`)...)
		w.WriteString(locations)
		w.lines(
			"    location @pf_502 {",
			`      return 502 "$pf_empty";`,
			"    }",
			"  }",
		)
	}
	w.lines("}")
	if refused := v.Err(); refused != nil {
		return nil, refused
	}
	out := w.String()
	if line := overLong(out); line != 0 {
		return nil, fmt.Errorf("line %d of the nginx configuration holds a token longer than nginx reads, and no value was refused for it", line)
	}
	return []generator.File{{Bytes: []byte(out)}, {Name: scriptName, Bytes: script()}}, nil
}

// requestChecks answer, in every server and before anything else, the requests Envoy answers before it reads the host:
// what its HTTP parser and codecs refuse with 400 or a closed connection, and HTTP/1.0 and HTTP/1.2 to HTTP/1.9, 426
// (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). Envoy answers 426 to every other version but 1.1 too,
// where nginx's own parser answers HTTP/0.0 to HTTP/0.9 with headers 400 and HTTP/2.0 to HTTP/9.9 505 before these
// checks run; a request line with no version, which nginx reads as HTTP/0.9 with an empty $server_protocol, is
// answered 400 here, and nginx sends that answer in HTTP/0.9's form, the body alone, so the client receives nothing.
var requestChecks = []string{
	"    if ($pf_f_request) {",
	`      return 400 "$pf_empty";`,
	"    }",
	"    if ($pf_f_426) {",
	`      return 426 "Upgrade Required";`,
	"    }",
}

// writeRequestChecks writes the flags requestChecks reads, each measured on Envoy 1.40.2 beside nginx 1.30.6 and
// 1.31.8.
func writeRequestChecks(w *writer) {
	// counted is one element of a Connection list that Envoy counts against MostConnectionTokens: a token with the white
	// space around it, or white space alone. The commas around the counted elements take the empty ones, which Envoy
	// skips, so a list of ten counted elements matches no key however its empty elements fall.
	counted := `(?:[ \t]*` + generator.ConnectionTokens + `[ \t]*|[ \t]+)`
	w.lines(
		// Over HTTP/1 Envoy answers 400 to a method its parser does not know, where nginx sends any method on.
		`  map "$server_protocol:$request_method" $pf_f_method { `+regexKey(`^HTTP/2\.0:`)+` 0; `+
			regexKey(`^[^:]*:(?:`+strings.Join(generator.EnvoyMethods, "|")+`)\z`)+` 0; default 1; }`,
		// A Connection token compares as any letter case (RFC 9110). Envoy serves a list with empty elements, as in
		// close, or te,,,, and counts an element of white space alone as a token, as in te, , (measured).
		`  map $http_connection $pf_f_connection { "" 0; `+
			quoted(`~*^,*(?:`+counted+`(?:,+`+counted+`){0,`+strconv.Itoa(generator.MostConnectionTokens-1)+`})?,*\z`)+` 0; default 1; }`,
		// Envoy's HTTP/1 parser takes no byte outside ! to ~ in a request line's target but a tab and a form feed,
		// where nginx escapes a byte above 0x7F and sends it on.
		`  map "$server_protocol:$request_uri" $pf_f_raw { `+regexKey(`^HTTP/2\.0:`)+` 0; `+
			regexKey(`[\x00-\x08\x0A\x0B\x0D-\x20\x7F-\xFF]`)+` 1; default 0; }`,
		// Over HTTP/2 these headers belong to one connection (RFC 9113): Envoy closes the connection of a request
		// that carries one, or a TE other than trailers, and nginx 1.31 answers it 400, where nginx 1.30 sends it on.
		`  map "$server_protocol:$http_connection$http_keep_alive$http_proxy_connection$http_transfer_encoding$http_upgrade"`+
			` $pf_f_h2_connection { `+regexKey(`^HTTP/2\.0:.`)+` 1; default 0; }`,
		`  map "$server_protocol:$http_te" $pf_f_h2_te { `+regexKey(`^HTTP/2\.0:(?!trailers\z).`)+` 1; default 0; }`,
		// Envoy answers 400 to a # in the target, in its path or its query, over HTTP/1.1 and HTTP/2, where nginx
		// serves the path before it.
		`  map $request_uri $pf_f_fragment { `+regexKey(`#`)+` 1; default 0; }`,
		// A request line $pf_authority does not read gets 400. A version $pf_f_426 reads is left to its own 426, which
		// Envoy answers whatever its line holds.
		`  map "$pf_f_426:$pf_authority" $pf_f_authority { `+regexKey(`^1:`)+` 0; `+regexKey(`^0:.`)+` 0; default 1; }`,
		// Envoy answers 400 to a header name outside RFC 9110's token characters, such as x@at, x(p) or one holding a
		// byte above 0x7F, and closes an HTTP/2 connection that sends one, where nginx sends it on (ignore_invalid_headers
		// off) or drops it and serves the request. The script's headerNames is "0" when every name is token
		// characters, and only "0" passes, so a run of the script that failed, which leaves the value empty, is
		// refused too. The njs engine is named, so a later njs whose default is another engine reads the script alike.
		"  js_engine njs;",
		"  js_import pf from "+scriptName+";",
		"  js_set $pf_js_names pf.headerNames;",
		`  map $pf_js_names $pf_f_names { "0" 0; default 1; }`,
		// Envoy answers 400 to a request line whose version is not HTTP/ and one digit, a dot and one digit, such as
		// HTTP/1.10 or HTTP/1.01, where nginx reads the numbers and serves it as HTTP/1.1.
		`  map $server_protocol $pf_f_version { `+regexKey(`^HTTP/[0-9]\.[0-9]\z`)+` 0; default 1; }`,
		`  map "$pf_f_method$pf_f_connection$pf_f_raw$pf_f_h2_connection$pf_f_h2_te$pf_f_fragment$pf_f_authority$pf_f_names$pf_f_version" $pf_f_request { `+
			regexKey(`1`)+` 1; default 0; }`,
		// Envoy answers HTTP/1.0 and HTTP/1.2 to HTTP/1.9 426, where nginx serves them (HTTP/1.2 to HTTP/1.9 as
		// HTTP/1.1). No request reaches this map as HTTP/0.9: nginx's parser answers HTTP/0.9 with headers 400 itself,
		// and a request line with no version has an empty $server_protocol, which $pf_f_version flags.
		`  map $server_protocol $pf_f_426 { `+regexKey(`^HTTP/(?:0\.9|1\.[02-9])\z`)+` 1; default 0; }`,
		`  map $pf_authority $pf_f_host_dot { `+regexKey(`\.(?::[0-9]*)?\z`)+` 1; default 0; }`,
		"",
	)
}

// writeUpgradeMaps writes the upgrade rule. Envoy reads a request as an upgrade only when its Connection names upgrade:
// it passes a WebSocket upgrade on, serves an h2c upgrade as a plain request without it and its HTTP2-Settings, and
// answers any other upgrade 403; with no upgrade in Connection it removes Upgrade and serves the request, its
// HTTP2-Settings kept (measured on Envoy 1.40.2). The Connection and Upgrade keys are the file's only keys that ignore
// case: RFC 9110 asks a recipient to compare a Connection token and an upgrade's protocol name so.
func writeUpgradeMaps(w *writer) {
	w.lines(
		`  map $http_connection $pf_connection_upgrade { `+quoted(`~*(?:^|,)[ \t]*upgrade[ \t]*(?:,|\z)`)+` 1; default 0; }`,
		`  map "$pf_connection_upgrade:$http_upgrade" $pf_upgrade_kind { `+quoted(`~*^1:websocket\z`)+` websocket; `+
			quoted(`~*^1:h2c\z`)+` h2c; `+regexKey(`^1:.`)+` refused; default none; }`,
		`  map $pf_upgrade_kind $pf_f_upgrade { `+regexKey(`^refused\z`)+` 1; default 0; }`,
		`  map $pf_upgrade_kind $pf_upgrade { `+regexKey(`^websocket\z`)+` $http_upgrade; default ""; }`,
		`  map $pf_upgrade_kind $pf_connection { `+regexKey(`^websocket\z`)+` upgrade; default ""; }`,
		`  map $pf_upgrade_kind $pf_http2_settings { `+regexKey(`^h2c\z`)+` ""; default $http_http2_settings; }`,
		"",
	)
}

// decodedHex matches the hex of the escape of an unreserved character (RFC 3986: a letter, a digit, -, ., _ and ~), of
// / and of \, in either case: the escapes Envoy decodes before it picks a route (measured on Envoy 1.40.2).
const decodedHex = `2[DEFdef]|3[0-9]|4[1-9A-Fa-f]|5[0-9ACFacf]|6[1-9A-Fa-f]|7[0-9AEae]`

// writePathClasses writes the path the routes are matched on, $pf_path, chosen by what the path as the client wrote it
// holds, so a route takes the request Envoy's would and the service receives the path Envoy's would (measured on Envoy
// 1.40.2). Envoy picks a route on the path as written when it needs no normalising, and otherwise on the path it has
// normalised, which keeps every escape it does not decode as written, in the hex case written; it escapes a raw " < >
// ^ ` { | } first, and a byte above 0x7F, in upper case hex. nginx's $uri decodes every escape. Three classes are read
// on the path as written, $request_uri up to its ?: n, a path that needs normalising (// , a dot segment, or an escape
// Envoy decodes); k, an escape Envoy keeps; e, a byte Envoy escapes. A fourth, q, reads the whole target: a ? with
// nothing after it, an empty query, which Envoy sends on and nginx drops from every path it rewrites. With none, or k
// or q alone or together, the routes match the path as written and the service receives the target as written (a
// route location with no rewrite); a route with a rewrite cannot keep an escape or an empty query, so mode rawk
// refuses it. With n alone they match $uri and the service receives $uri escaped again, which is Envoy's path then.
// Envoy's path cannot be built for n with k, for e with k, or for n or e with q, so those are refused. With e and no k,
// a file with no pattern route matches $uri, because a prefix and a path hold no % and none of those bytes, so they
// take the byte and its escape alike, and $uri escaped again is Envoy's path; a pattern could take the byte where
// Envoy's never does, so a file with one refuses the path. Every class's default is e, which no mode takes, so a run
// PCRE2 gave up on refuses the path.
func writePathClasses(w *writer, hasPattern bool) {
	modes := "0000 raw; 0001 rawk; 0100 rawk; 0101 rawk; 1000 uri; "
	if !hasPattern {
		modes += "0010 uri; 1010 uri; "
	}
	w.lines(
		// An absolute target with no path is the path /, as Envoy reads it; a target that is no path, such as OPTIONS's
		// *, gets the default, which every class refuses.
		`  map $request_uri $pf_rawpath { `+regexKey(`^(?<pf_p>/[^?]*)`)+` $pf_p; `+regexKey(`^\z`)+` /; default ""; }`,
		`  map $pf_rawpath $pf_c_n { `+regexKey(`//|/\.\.?(?:/|\z)|%(?:`+decodedHex+`)`)+` 1; `+regexKey(`^/`)+` 0; default e; }`,
		`  map $pf_rawpath $pf_c_k { `+regexKey(`%(?!`+decodedHex+`)[0-9A-Fa-f]{2}`)+` 1; `+regexKey(`^/`)+` 0; default e; }`,
		`  map $pf_rawpath $pf_c_e { `+regexKey("["+generator.EscapedInPath+`\x80-\xFF]`)+` 1; `+regexKey(`^/`)+` 0; default e; }`,
		`  map $request_uri $pf_c_q { `+regexKey(`^[^?]*\?\z`)+` 1; `+regexKey(`^`)+` 0; default e; }`,
		`  map "$pf_c_n$pf_c_k$pf_c_e$pf_c_q" $pf_mode { `+modes+`default refused; }`,
		`  map $pf_mode $pf_path { raw $pf_rawpath; rawk $pf_rawpath; uri $uri; default ""; }`,
		"",
	)
}

// hashSizes sizes the hash tables nginx builds from the names a file declares, which nginx refuses to load when a name
// does not fit in a bucket or the names in one table: a host of 48 characters failed at the default bucket of 64
// bytes, and the variables of 2,000 routes at the default size (measured on nginx 1.30.6 and 1.31.8). A name takes 8
// bytes and its length with 2 more rounded up to 8, and a bucket 8 more (ngx_hash_init); nginx rounds a bucket up to
// its cache line.
func hashSizes(in generator.Input, routes []route) []string {
	longest, hosts := 0, 0
	for _, environment := range in.Origins.Environments {
		for _, host := range environment.Hosts {
			longest = max(longest, len(host))
			hosts++
		}
	}
	bucket := max(64, (16+(longest+2+7)/8*8+63)/64*64)
	// Each route declares up to two variables and its share of the groups' (writeRouteChoice), fewer than three, and each
	// environment up to five (writeOriginCheck, writeCORSMaps), beside the 57 or fewer the file declares once: its maps,
	// its flag, the variables of the script and the named captures (54 with client_address connection).
	variables := 3*len(routes) + 5*len(in.Origins.Environments) + 64
	return []string{
		fmt.Sprintf("  server_names_hash_bucket_size %d;", bucket),
		fmt.Sprintf("  server_names_hash_max_size %d;", max(512, 4*hosts)),
		fmt.Sprintf("  variables_hash_max_size %d;", max(1024, 4*variables)),
	}
}

// routesOf is every place of the route choice, in order: the plain routes as written, with the methods of the rpc
// entry, sorted by path as in.RPC is, at the entry's place. Each value of the input goes into the file through v.
func routesOf(in generator.Input, v *values) ([]route, error) {
	g := in.Gateway
	var routes []route
	for i := range g.Routes {
		entry := &g.Routes[i]
		if entry.RPC != nil {
			for _, method := range in.RPC {
				r, err := toService(v, method.Target, model.GRPC, "the RPC method "+method.Path())
				if err != nil {
					return nil, err
				}
				r.key = v.token("rpc.methods", method.Pos, regexKey("^"+regexp.QuoteMeta(method.Path())+`\z`))
				routes = append(routes, r)
			}
			continue
		}
		r, err := toService(v, g.Service(entry.To.Value), model.HTTP, "the route "+entry.Name.Value)
		if err != nil {
			return nil, err
		}
		written, err := match(entry)
		if err != nil {
			return nil, err
		}
		r.key = v.token(matchKey(entry), entry.Value.Pos, regexKey(written))
		if entry.Method != "" {
			r.method = v.token("routes[].method", entry.Pos, regexKey(`^1:`+regexp.QuoteMeta(entry.Method)+`\z`))
		}
		r.rewrite = rewriteLine(v, entry)
		routes = append(routes, r)
	}
	return routes, nil
}

// matchKey is the key of a route's prefix, path or pattern, as the support table names it.
func matchKey(r *model.Route) string {
	return "routes[]." + string(r.Match)
}

// toService is a route to the service's port of protocol, or the error of a service that is nil or has no such port.
// The service receives its host_header as Host, or the authority nginx picked the server by, as Envoy sends it.
func toService(v *values, service *model.Service, protocol model.Protocol, what string) (route, error) {
	if service == nil {
		return route{}, fmt.Errorf("%s goes to a service that is not declared", what)
	}
	if _, ok := service.Port(protocol); !ok {
		return route{}, fmt.Errorf("%s goes to %s, which has no %s port", what, service.Name, protocol)
	}
	host := "$pf_authority"
	if service.HostHeader != "" {
		// The model keeps no position of host_header, so the refusal names the service's.
		host = v.token("services.<service name>.host_header", service.Pos, quoted(service.HostHeader))
	}
	scheme := "http://"
	if protocol == model.GRPC {
		scheme = "grpc://"
	}
	return route{pass: v.token(serviceKey, service.Pos, scheme+service.Cluster(protocol)), grpc: protocol == model.GRPC, host: host}, nil
}

// serviceKey and hostsKey are the keys of a service's name, which its upstreams are named by, and of an
// environment's hosts, as the support table names them.
const (
	serviceKey = "services.<service name>"
	hostsKey   = "origins.<environment name>.hosts"
)

// match is the regular expression that takes what the route's match takes. A prefix or a path is quoted for PCRE, so
// it matches as written, and a path is anchored at the end with \z, which unlike $ never matches before a final line
// break (PCRE2's pattern page). A pattern is written from Go's parse of it (package pcre) and matched against the
// whole path, as Envoy matches it, within patternSteps.
func match(r *model.Route) (string, error) {
	switch r.Match {
	case model.Prefix:
		return "^" + regexp.QuoteMeta(r.Value.Value), nil
	case model.Path:
		return "^" + regexp.QuoteMeta(r.Value.Value) + `\z`, nil
	}
	written, err := pcre.Write(r.Value.Value)
	if err != nil {
		return "", fmt.Errorf("the route %s: %w", r.Name.Value, err)
	}
	return patternSteps + `\A(?:` + written + `)\z`, nil
}

// patternSteps bounds the steps PCRE2 takes for one pattern and one path; past them the run gives up, and the route
// choice answers 400 (writeRouteChoice). A worker serves no other request while PCRE2 runs, and under PCRE2's own
// limit, ten million steps, a path of 44 bytes against ^/g/((a|aa)+c|a+b)$ held one for 213 to 227 ms before that 400,
// where Envoy's RE2 served it in 2 ms (nginx 1.30.6 and 1.31.8, PCRE2 10.46, on an arm64 Mac). With 100,000 steps the
// 400 comes in about 2 ms. Paths of 8,177 bytes, the longest nginx reads over HTTP/1.1, built against each pattern of
// the battery's files and the examples were all decided as Envoy decides them; with 10,000 steps nginx gave up on 8,177
// a that ^/give-up/(a|aa)+$ takes, so the limit is not lower.
const patternSteps = "(*LIMIT_MATCH=100000)"

// keepPath sends the service the path nginx has normalised, escaped again, which is Envoy's path when the path needs
// normalising and keeps no escape (writePathClasses). It adds the query when there is one and drops an empty one, a ?
// with nothing after it, which is why an empty query is refused in every mode that reaches it. Without a rewrite, proxy_pass and grpc_pass send the target as
// the client wrote it, byte for byte, from a location an error_page reached (measured on nginx 1.30.6 and 1.31.8).
const keepPath = "rewrite ^ $uri break;"

// rewriteLine is the route's rewrite, or "" for a route with none: the matched prefix or path replaced by the rewrite,
// the rest of $uri kept, which is the path as written when it needs no normalising and holds no escape, and Envoy's
// normalised path when it needs normalising and keeps no escape; a path that keeps an escape, or a target with an empty
// query, which nginx drops from a rewritten path, is refused on such a route (writeRouteChoice). (?s) lets the rest hold a line break. A $ in the rewrite is written ${pf_dollar}. break keeps the
// request in the route's location, where last would search the locations again for the new path.
func rewriteLine(v *values, r *model.Route) string {
	if r.Rewrite == nil {
		return ""
	}
	replacement := dollars(r.Rewrite.Value) + "$1"
	return "rewrite " + v.token(matchKey(r), r.Value.Pos, quoted("^"+regexp.QuoteMeta(r.Value.Value)+`((?s).*)\z`)) + " " +
		v.token("routes[].rewrite", r.Rewrite.Pos, quoted(replacement)) + " break;"
}

// dollars is text with each $ written ${pf_dollar}: nginx reads a $ in a rewrite, an add_header value and a map's
// value as the start of a variable, and has no escape for it.
func dollars(text string) string {
	return strings.ReplaceAll(text, "$", "${pf_dollar}")
}

// hasDollar says whether a rewrite or a cors value holds a $, which needs $pf_dollar. A certificate's or a key's path
// holding $ is refused before the generator runs (format's nginxDollarPath).
func hasDollar(g *model.Gateway, cors origins.CORS) bool {
	for _, r := range g.Routes {
		if r.Rewrite != nil && strings.Contains(r.Rewrite.Value, "$") {
			return true
		}
	}
	return slices.ContainsFunc([]string{cors.AllowMethods, cors.AllowHeaders, cors.ExposeHeaders, cors.MaxAge},
		func(value string) bool { return strings.Contains(value, "$") })
}

// groupSize is how many routes, or groups of routes, one map of the route choice reads: its source stays far under the
// 4096 bytes nginx reads in one value, and 4,096 routes need two levels.
const groupSize = 64

// writeRouteChoice writes the route choice, which names the location of the first route that takes the request, as
// Envoy takes the first route that matches. Each route has a map that is the route's number when its path, and its
// method if it names one, take the request, and 0 when they do not. A group's map reads up to groupSize of them in
// order and is the first that is not 0, or 0; groups are read by groups in the same way until one map is left. The
// tree stays a few maps deep, as nginx needs: it finds no variable nested 100 deep (ngx_http_variables.c), so a chain
// of one map a route sent every request past the 96th route to 400 (measured). Every key is a case-sensitive regular
// expression, which a map tries in the written order (the map module page).
//
// A route map's last key takes every path, so its default, failed, stands only for a run of the regular expression
// that PCRE2 gave up on (a pattern that backtracks past PCRE2's match limit), which nginx's map reads as no match. A
// request whose first route that is not 0 is failed is refused, where nginx would go on to a later route Envoy's RE2
// never sends it to; a failed run of a later route changes nothing, as Envoy never runs it.
//
// The choice reads the three path flags, the upgrade flag and the preflight flag, then the path's mode
// (writePathClasses), then the tree: a set flag wins over every route, and a path the mode refuses is refused. The
// preflight is answered only when a route takes its path, as Envoy 1.40.2 answers a preflight (measured): an upgrade
// other than WebSocket and a dot segment with a ; parameter are refused on a preflight too, and a preflight to a path
// no route takes gets 404. A path nginx alone refuses (a backslash, a control character, a mode that refuses it) is
// refused on a preflight as on every other request.
//
// A route's map is its number, followed by w for a route with a rewrite. A route with no rewrite has two locations:
// @pf_r_<n> sends the target as the client wrote it, for a path matched as written, and @pf_r_<n>-uri sends $uri
// escaped again, for a path matched on $uri. A route with a rewrite has one, whose rewrite reads $uri, which is the path
// as written when the path needs no normalising and keeps no escape; a path that keeps an escape or a target with an
// empty query (mode rawk) is refused on it, because nginx would send the escape decoded and drop the ?.
func writeRouteChoice(w *writer, routes []route) {
	var level []string
	for i, r := range routes {
		n := i + 1
		value := strconv.Itoa(n)
		if r.rewrite != "" {
			value += "w"
		}
		if r.method == "" {
			w.lines(fmt.Sprintf("  map $pf_path $pf_m%d { %s %s; %s 0; default failed; }", n, r.key, value, regexKey("^")))
		} else {
			// The method joins the path in a second map, so a request with another method tries the next route.
			w.lines(
				fmt.Sprintf("  map $pf_path $pf_p%d { %s 1; %s 0; default e; }", n, r.key, regexKey("^")),
				fmt.Sprintf("  map %s $pf_m%d { %s %s; %s failed; default 0; }", quoted(fmt.Sprintf("$pf_p%d:$request_method", n)), n,
					r.method, value, regexKey(`^e:`)),
			)
		}
		level = append(level, fmt.Sprintf("$pf_m%d", n))
	}
	// Each map's value is a route's number, 0 or failed, so a group's first key names the first value that is not 0;
	// its default, like a route map's, stands only for a run PCRE2 gave up on.
	groups := 0
	for len(level) > 1 || groups == 0 {
		var next []string
		for start := 0; start < len(level); start += groupSize {
			groups++
			name := fmt.Sprintf("$pf_g%d", groups)
			w.lines(fmt.Sprintf("  map %s %s { %s $pf_first; %s 0; default failed; }",
				quoted(strings.Join(level[start:min(start+groupSize, len(level))], ":")+":"), name,
				regexKey(`^(?:0:)*(?<pf_first>[^0:][^:]*):`), regexKey(`^(?:0:)*\z`)))
			next = append(next, name)
		}
		level = next
	}
	choice := level[0]
	w.lines(
		`  map "$pf_f_dotparam$pf_f_backslash$pf_f_control$pf_f_upgrade$pf_f_preflight:$pf_mode:`+choice+`" $pf_route {`,
		"    "+regexKey("^1")+" refused;",
		"    "+regexKey("^01")+" refused;",
		"    "+regexKey("^001")+" refused;",
		"    "+regexKey("^0001")+" upgrade-refused;",
		"    "+regexKey("^0000[01]:refused:")+" refused;",
		"    "+regexKey(`^0000[01]:[a-z]+:failed\z`)+" refused;",
		"    "+regexKey("^00001:[a-z]+:[1-9]")+" preflight;",
		"    "+regexKey(`^00000:rawk:[0-9]+w\z`)+" refused;",
		"    "+regexKey(`^00000:uri:(?<pf_n>[1-9][0-9]*)\z`)+" $pf_n-uri;",
		"    "+regexKey(`^00000:[a-z]+:(?<pf_n>[1-9][0-9]*)w?\z`)+" $pf_n;",
		"    default no-route;",
		"  }",
		"",
	)
}

// writeOriginCheck writes the origin check of the environment at place n, the Envoy output's RBAC rule for a file with
// no list (filter-rbac.yaml and virtual-host.yaml in internal/envoy/fragments): $pf_o<n> is 1 when the request carries
// an Origin on none of the environment's hosts, which its server refuses with 403. An origin on one of them, on either
// scheme and any port, is admitted as origins.SameOriginRegex writes it for Envoy, with its $ written \z, and each host
// quoted, so a host's dot takes a dot alone; an origin in other letter case is another origin, as Envoy compares it. A
// request with no Origin passes; an empty one reads as " " ($pf_origin, writeOrigin), which no key admits, so it is
// refused, as Envoy refuses it. With Origin removed every request carries none, and passes. An environment with lists admits what they admit (listKeys), and refuses an Origin holding
// a comma first: the Envoy output joins two Origin lines with a comma, and a pattern can take the joined text, as
// https://evil.example.net,https://www.example.com is taken by https://*.example.com (measured on Envoy 1.40.2).
func writeOriginCheck(w *writer, v *values, n int, environment origins.Environment, e edge) {
	keys := ""
	if environment.Listed() {
		keys = regexKey(",") + " 1; "
	}
	for _, host := range environment.Hosts {
		keys += v.token(hostsKey, environment.Pos, regexKey(strings.TrimSuffix(origins.SameOriginRegex(host), "$")+`\z`)) + " 0; "
	}
	for _, key := range listKeys(v, environment) {
		keys += key + " 0; "
	}
	w.lines(fmt.Sprintf(`  map %s $pf_o%d { "" 0; %sdefault 1; }`, mapSource(e.origin()), n, keys))
}

// listKeys are the map keys of the environment's lists: an exact origin compared ignoring case, as the Envoy output
// compares it (HTTPS://WWW.EXAMPLE.COM is admitted by https://www.example.com), and a pattern in the case written, as
// the Envoy output compares it (HTTPS://SHOP.EXAMPLE.COM is refused by https://*.example.com), both measured on Envoy
// 1.40.2. A pattern is written so that an Origin costs time in its length alone (origins.PatternPCRE): nginx's map
// reads a run PCRE2 gave up on as no match, so the first form also refused an Origin a later pattern takes. Each is
// anchored with \z, which unlike $ never matches before a final line break.
func listKeys(v *values, environment origins.Environment) []string {
	var keys []string
	for _, origin := range environment.Origins {
		keys = append(keys, v.token("origins.<environment name>.origins", environment.Pos, quoted("~*^"+regexp.QuoteMeta(origin)+`\z`)))
	}
	for _, pattern := range environment.Patterns {
		keys = append(keys, v.token("origins.<environment name>.patterns", environment.Pos,
			regexKey(strings.TrimSuffix(origins.PatternPCRE(pattern), "$")+`\z`)))
	}
	return keys
}

// writeCORSMaps writes the CORS answer of the environment at place n when it has lists. $pf_c<n> is 1 when the
// request's Origin is on a list and holds no comma; $pf_cors<n> is 1 when it is and the request also reached the
// server's rules ($pf_reached, corsLines), passed the request flags and the host-dot check and reached a route or the
// preflight answer, the requests the Envoy output's CORS filter answers; a refused request, a request no route takes,
// an upgrade refused and a request nginx refuses while it reads it carry no Access-Control line of the gateway's.
// $pf_acao<n> and $pf_aceh<n> are the gateway's lines then, and otherwise the service's own, which the server hides
// from the answer and sends again through these: the Envoy output replaces the service's lines for an origin on a list
// and passes them on for any other (measured on Envoy 1.40.2).
func writeCORSMaps(w *writer, v *values, n int, environment origins.Environment, cors origins.CORS, e edge) {
	if !environment.Listed() {
		return
	}
	origin := mapSource(e.origin())
	keys := ""
	for _, key := range listKeys(v, environment) {
		keys += key + " 1; "
	}
	w.lines(
		fmt.Sprintf(`  map %s $pf_c%d { %s 0; %sdefault 0; }`, origin, n, regexKey(","), keys),
		fmt.Sprintf(`  map "$pf_reached:$pf_f_request$pf_f_426$pf_f_host_dot:$pf_route:$pf_c%d" $pf_cors%d { %s 1; default 0; }`, n, n,
			regexKey(`^1:000:(?:[0-9]+(?:-uri)?|preflight):1\z`)),
		fmt.Sprintf(`  map $pf_cors%d $pf_acao%d { 1 %s; default $upstream_http_access_control_allow_origin; }`, n, n, origin),
	)
	if cors.ExposeHeaders != "" {
		w.lines(fmt.Sprintf(`  map $pf_cors%d $pf_aceh%d { 1 %s; default $upstream_http_access_control_expose_headers; }`, n, n,
			corsValue(v, "cors.expose_headers", cors, cors.ExposeHeaders)))
	}
}

// answerHeaders are the add_header lines of every environment server that are not Access-Control lines, which a
// location with add_header lines of its own writes again.
var answerHeaders = []string{"      add_header x-request-id $pf_request_id always;", "      add_header Vary Origin always;"}

// corsLines returns the Access-Control lines of the environment at place n: server, the environment server's, where
// it has lists, and preflight, the add_header lines of its preflight answer, nil when it writes none. A cors line is
// written only when its key is; "*" for allow_methods or allow_headers sends what the request asks for, as the Envoy
// output does, and any other value is sent as written. The origin and the exposed headers are named only where the
// environment has lists, as the Envoy output names them only for an origin on one. grpc says whether a route
// location sends to a gRPC service, which hides the service's lines through its own directive.
func corsLines(v *values, n int, environment origins.Environment, cors origins.CORS, e edge, grpc bool) (server, preflight []string) {
	list, expose := environment.Listed(), cors.ExposeHeaders != ""
	var answer []string
	if list {
		// nginx answers a request it cannot read, such as one with a NUL byte in a header or a header line over 8 KiB,
		// before the server's rules run, and its answer still takes the server's add_header lines: $pf_cors<n> reads
		// this flag, which only the server's rewrite phase sets, so such an answer carries no Access-Control line, as the
		// Envoy output's refusals carry none (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). It comes before
		// the server's if lines, so a request one of them answers has set it too.
		server = append(server, "    set $pf_reached 1;")
		server = append(server, fmt.Sprintf("    add_header Access-Control-Allow-Origin $pf_acao%d always;", n))
		answer = append(answer, fmt.Sprintf("      add_header Access-Control-Allow-Origin $pf_acao%d always;", n))
		hidden := []string{"Access-Control-Allow-Origin"}
		if expose {
			server = append(server, fmt.Sprintf("    add_header Access-Control-Expose-Headers $pf_aceh%d always;", n))
			hidden = append(hidden, "Access-Control-Expose-Headers")
		}
		for _, header := range hidden {
			server = append(server, "    proxy_hide_header "+header+";")
			if grpc {
				server = append(server, "    grpc_hide_header "+header+";")
			}
		}
	}
	echoed := func(key, value, asked string) string {
		if value == "*" {
			return mapSource(e.read(asked))
		}
		return corsValue(v, key, cors, value)
	}
	if cors.AllowMethods != "" {
		answer = append(answer, "      add_header Access-Control-Allow-Methods "+
			echoed("cors.allow_methods", cors.AllowMethods, "access-control-request-method")+" always;")
	}
	if cors.AllowHeaders != "" {
		answer = append(answer, "      add_header Access-Control-Allow-Headers "+
			echoed("cors.allow_headers", cors.AllowHeaders, "access-control-request-headers")+" always;")
	}
	if cors.MaxAge != "" {
		// The format takes digits alone for max_age.
		answer = append(answer, "      add_header Access-Control-Max-Age "+v.token("cors.max_age", cors.KeyPos["cors.max_age"], cors.MaxAge)+" always;")
	}
	if list && expose {
		answer = append(answer, fmt.Sprintf("      add_header Access-Control-Expose-Headers $pf_aceh%d always;", n))
	}
	if answer != nil {
		preflight = slices.Concat(answerHeaders, answer)
	}
	return server, preflight
}

// corsValue is a cors value as nginx reads it back, in quotes with each $ written ${pf_dollar}, refused at its own
// key when its form is longer than nginx reads.
func corsValue(v *values, key string, cors origins.CORS, value string) string {
	return v.token(key, cors.KeyPos[key], quoted(dollars(value)))
}

// answerLocation is the named location @<name> of a server, which answers with answer itself, sending the add_header
// lines headers, or the server's when headers is nil.
func answerLocation(name string, headers []string, answer string) []string {
	return slices.Concat([]string{"    location @" + name + " {"}, headers, []string{"      " + answer, "    }"})
}

// tlsCiphers are the TLS 1.2 ciphers the Envoy output offers, Envoy's default list (measured on Envoy 1.40.2: with an
// RSA certificate it takes the three RSA ones and refuses every other TLS 1.2 cipher). nginx keeps OpenSSL's TLS 1.3
// suites, the three Envoy takes.
var tlsCiphers = []string{"ECDHE-ECDSA-AES128-GCM-SHA256", "ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-ECDSA-AES256-GCM-SHA384",
	"ECDHE-RSA-AES256-GCM-SHA384", "ECDHE-ECDSA-CHACHA20-POLY1305", "ECDHE-RSA-CHACHA20-POLY1305"}

// writeTLS writes the http block's TLS lines when tls is written: the certificate and key every public server serves,
// and the protocols, ciphers and key exchange groups the Envoy output takes, TLS 1.2 and 1.3, its TLS 1.2 ciphers and
// X25519MLKEM768, X25519 and P-256 (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). With tls.clients every
// public server asks for a client certificate and checks it against the clients CA, through a chain of up to 100
// certificates: nginx's default depth, 1, refuses a certificate under an intermediate (the ssl module page), and 100 is
// the default of Envoy's BoringSSL.
func writeTLS(w *writer, v *values, tls *model.TLS) {
	if tls == nil {
		return
	}
	w.lines(
		"  ssl_certificate "+v.token("tls.certificate", tls.Pos, quoted(tls.Certificate))+";",
		"  ssl_certificate_key "+v.token("tls.key", tls.Pos, quoted(tls.Key))+";",
		"  ssl_protocols TLSv1.2 TLSv1.3;",
		"  ssl_ciphers "+strings.Join(tlsCiphers, ":")+";",
		"  ssl_ecdh_curve X25519MLKEM768:X25519:prime256v1;",
	)
	if tls.ClientsCA != "" {
		// nginx reads this path as written, $ included: the directive takes no variable.
		w.lines(
			"  ssl_client_certificate "+v.token("tls.clients.ca", tls.Pos, quoted(tls.ClientsCA))+";",
			"  ssl_verify_client on;",
			"  ssl_verify_depth 100;",
		)
	}
}

// tlsServerLines are a public server's answers to what fails at the TLS level, nil when tls is not written. nginx
// completes the handshake where the Envoy output ends it, and raises 495 for a client certificate that fails, 496 for
// none, 497 for plain HTTP on the TLS port and, with tls.clients, 421 for a Host that names another server than the TLS
// server name; it raises each before a location is chosen, so only the server's own error_page catches it. Each is
// answered empty and reaches no service. headers are the add_header lines each answer sends, or nil for the server's.
func tlsServerLines(tls *model.TLS, headers []string) []string {
	if tls == nil {
		return nil
	}
	pages := []string{"    error_page 495 496 497 = @pf_tls_refused;"}
	locations := answerLocation("pf_tls_refused", headers, `return 400 "$pf_empty";`)
	if tls.ClientsCA != "" {
		pages = append(pages, "    error_page 421 = @pf_tls_misdirected;")
		locations = append(locations, answerLocation("pf_tls_misdirected", headers, `return 421 "$pf_empty";`)...)
	}
	return slices.Concat(pages, locations)
}

// writeUpstream writes the upstream of one port of a service, named as the Envoy configuration names its cluster.
// max_fails=0 keeps a backend in use after a failed attempt, where nginx's default takes it out for 10 seconds; Envoy
// keeps every backend of a service with no health key. Each worker keeps up to 32 idle connections open for an hour,
// Envoy's default upstream idle timeout; 32 is a number chosen, not measured under load.
func writeUpstream(w *writer, v *values, service model.Service, port model.ServicePort) {
	w.lines("  upstream " + v.token(serviceKey, service.Pos, service.Cluster(port.Protocol)) + " {")
	// addresses holds two addresses or more, so one is address's.
	key := "services.<service name>.addresses"
	if len(service.Addresses) == 1 {
		key = "services.<service name>.address"
	}
	for _, address := range service.Addresses {
		w.lines("    server " + v.token(key, address.Pos, net.JoinHostPort(address.Value, strconv.FormatInt(port.Number, 10))) + " max_fails=0;")
	}
	w.lines(
		"    keepalive 32;",
		"    keepalive_timeout 1h;",
		"  }",
	)
}

// routeLocations is the named locations of each route, the same in every environment (writeRouteChoice): one that
// sends the target as written and one that sends $uri for a route with no rewrite, and one with the rewrite for a route
// with one.
func routeLocations(routes []route, v *values, e edge) string {
	var w writer
	for i, r := range routes {
		name := fmt.Sprintf("@pf_r_%d", i+1)
		if r.rewrite != "" {
			writeLocation(&w, v, name, r.rewrite, r, e)
			continue
		}
		writeLocation(&w, v, name, "", r, e)
		writeLocation(&w, v, name+"-uri", keepPath, r, e)
	}
	return w.String()
}

// writeLocation writes the named location name of route r, which sets the path the service receives with path, or
// sends the target as written when path is "". It writes every header it sends, because a location that writes one
// proxy_set_header inherits none from the levels above it (the proxy module page), and sets each header Envoy removes
// empty, which nginx then does not send, and each header remove_from_clients removes, but one the location sends
// itself, whose value then reads no client copy (edge.read). A service that refuses the connection is answered 502 with
// no body, through a named location: error_page to a URI would search the locations again.
func writeLocation(w *writer, v *values, name, path string, r route, e edge) {
	w.lines("    location " + name + " {")
	if path != "" {
		w.lines("      " + path)
	}
	set := "proxy_set_header"
	if r.grpc {
		set = "grpc_set_header"
	}
	sent := map[string]bool{}
	send := func(header, written, value string) {
		sent[header] = true
		w.lines("      " + set + " " + written + " " + value + ";")
	}
	// Without Host nginx sends a gRPC service the upstream's name as :authority (measured on nginx 1.30.6 and 1.31.8).
	// A gRPC location sends TE: trailers itself.
	send("host", "Host", r.host)
	send("x-request-id", "X-Request-Id", "$pf_request_id")
	send("x-forwarded-proto", "X-Forwarded-Proto", "$pf_forwarded_proto")
	// The service receives the Origin as one line, the lines Envoy joins (writeOrigin).
	if origin := e.origin(); origin != "" {
		send("origin", "Origin", origin)
	}
	if e.connection {
		send("x-forwarded-for", "X-Forwarded-For", "$pf_forwarded_for")
		send("x-envoy-external-address", "x-envoy-external-address", "$remote_addr")
	}
	if !r.grpc {
		send("upgrade", "Upgrade", "$pf_upgrade")
		send("connection", "Connection", "$pf_connection")
		send("http2-settings", "HTTP2-Settings", "$pf_http2_settings")
		send("te", "TE", "$pf_te")
	}
	for _, header := range generator.RemovedByEnvoy {
		send(header, header, `""`)
	}
	if e.connection {
		for _, header := range generator.RemovedByEnvoyFacingClients {
			send(header, header, `""`)
		}
	}
	for _, header := range e.remove {
		if name := strings.ToLower(header.Name); !sent[name] {
			send(name, v.token("remove_from_clients", header.Pos, quoted(name)), `""`)
		}
	}
	if r.grpc {
		w.lines("      grpc_pass " + r.pass + ";")
	} else {
		w.lines("      proxy_pass " + r.pass + ";")
	}
	w.lines(
		"      error_page 502 = @pf_502;",
		"    }",
	)
}

// writer builds the file.
type writer struct{ strings.Builder }

// lines writes each text as one line.
func (w *writer) lines(texts ...string) {
	for _, text := range texts {
		w.WriteString(text)
		w.WriteByte('\n')
	}
}

// values writes each value of the input into the file through one call, token, which keeps a refusal of each value
// whose nginx form is longer than nginx reads, by the key that holds it, as the support table names the key. The form
// decides, not the value: a $ of a rewrite is written as a 12-byte variable, and a dot of an origin's host as \\.
type values struct{ generator.Refusals }

// token returns written, the nginx form of a value of key at pos, and keeps the value's refusal when nginx would not
// read written from its file.
func (v *values) token(key string, pos model.Pos, written string) string {
	if overLong(written) != 0 {
		v.Refuse(pos, key, "whose nginx form is longer than nginx reads", "nginx",
			"nginx reads no value longer than 4094 bytes between its quotes, or 4095 bytes without them; write a value whose nginx form is at most 4094 bytes, or render it for envoy")
	}
	return written
}

// longestToken is the most bytes nginx reads from its file for one token with the byte that ends it, the size of its
// buffer for a file (NGX_CONF_BUFFER, 4096): a value of 4094 bytes between its quotes, a word of 4095 bytes before a
// space or ;, and a comment of 4095 bytes from its # before the end of its line (measured with nginx -t on nginx
// 1.30.6 and 1.31.8, which refuses a longer one with "too long parameter").
const longestToken = 4096

// overLong returns the line of the first token in text that nginx would not read, or 0: a value in double or single
// quotes counted from its opening quote to its closing one, a word up to the space, ;, { or } after it, and a comment
// from its # to the end of its line, each as text writes it, a backslash included.
func overLong(text string) int {
	line := 1
	for i := 0; i < len(text); {
		start, end := i, i
		switch c := text[i]; {
		case c == '\n':
			line++
			i++
			continue
		case strings.IndexByte(" \t\r;{}", c) >= 0:
			i++
			continue
		case c == '#':
			end = i + strings.IndexByte(text[i:]+"\n", '\n')
			i = end
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(text) && text[j] != c {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			end = min(j+1, len(text))
			i = end
		default:
			for end < len(text) && strings.IndexByte(" \t\r\n;{}", text[end]) < 0 {
				end++
			}
			i = end
		}
		length := end - start
		if text[start] != '"' && text[start] != '\'' {
			// A word or a comment is read with the byte that ends it.
			length++
		}
		if length > longestToken {
			return line
		}
		line += strings.Count(text[start:end], "\n")
	}
	return 0
}
