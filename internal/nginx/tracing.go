package nginx

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// tracingNeeds is line 2 of a file with tracing in place of the njs line: the official images tagged -otel ship
// ngx_otel_module beside njs, and the plain ones stop at its load_module (nginx -t: dlopen() of
// ngx_otel_module.so failed, measured on nginx 1.30.6 and 1.31.8).
const tracingNeeds = "# Needs the njs module, 1.0.2 or later, and ngx_otel_module, in the nginx images tagged -otel."

// otelTracing is the file's tracing when it writes the otel provider, or nil: nginx has no X-Ray tracer, and the
// support table refuses that provider before the generator runs.
func otelTracing(g *model.Gateway) *model.Tracing {
	if g.Tracing == nil || g.Tracing.OTel == nil {
		return nil
	}
	return g.Tracing
}

// tracingTop are the lines of the main context a file with tracing adds after the base's load_module lines: the
// tracer's module, and an env line for each variable an env tag reads, once each in the order written, as nginx
// starts its workers with every other variable of its environment removed but TZ (the core module page), and the
// script reads a tag's variable from process.env.
func tracingTop(t *model.Tracing) []string {
	if t == nil {
		return nil
	}
	lines := []string{"load_module modules/ngx_otel_module.so;"}
	var kept []string
	for _, tag := range t.Tags {
		if tag.Env != "" && !slices.Contains(kept, tag.Env) {
			kept = append(kept, tag.Env)
			lines = append(lines, "env "+tag.Env+";")
		}
	}
	return lines
}

// spanField is an attribute of the Envoy output's span, with the value nginx writes it from.
type spanField struct{ name, value string }

// spanFields are the Envoy output's names and values on a request over HTTP/1.1 and HTTP/2, with a body, answered 503
// and over gRPC (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). node_id and zone are the empty node of the
// Envoy output; downstream_cluster is -, as the Envoy output removes the header it would read (format.NginxRemovedTraced);
// user_agent is - for a request with none; request_size is 0 for a request with no body and, for a body sent in
// chunks, its length, which $content_length gives once nginx has read it; http.protocol reads the request log's own
// map of the same values; http.url's scheme and host are the ones urlLines writes.
var spanFields = []spanField{
	{"node_id", `""`},
	{"zone", `""`},
	{"guid:x-request-id", "$pf_trace_request_id"},
	{"http.url", `"$pf_url_scheme://$pf_url_host$request_uri"`},
	{"downstream_cluster", "-"},
	{"user_agent", "$pf_span_user_agent"},
	{"http.protocol", "$pf_log_protocol"},
	{"peer.address", "$remote_addr"},
	{"request_size", "$pf_span_request_size"},
	{"response_size", "$body_bytes_sent"},
	{"component", "proxy"},
	{"upstream_cluster", "$pf_log_upstream"},
	{"upstream_cluster.name", "$pf_log_upstream"},
}

// tracingHTTP are the http block's tracing lines, nil without tracing. They come first in the block after the map
// table's sizes (mapHashSizes), which nginx reads before every map. Each is measured on nginx 1.30.6 and 1.31.8 beside
// Envoy 1.40.2:
//
// The module's start handler, which opens the span, runs at the http level: it is skipped in a named location, which
// every request reaches through error_page as an internal redirect (writeRouteChoice), so otel_trace is written here
// and never in a location. Every otel_span_attr is here too: a location's own list replaces the inherited one, and the
// module reads the span's name and attributes from the location the request ends in, the route's named location or a
// copy of it; the route's name and upstream come from $pf_log_route and $pf_log_upstream, which every route location
// sets, so a request no route takes is named ingress, as the Envoy output names it.
//
// otel_trace_context extract reads the client's traceparent into the span and writes nothing on the request: the
// script chooses what the service receives ($pf_traceparent and $pf_tracestate, sent by every route location) and what
// the check receives ($pf_check_traceparent and $pf_check_tracestate, written when the file asks a check), because the
// module's own propagate or inject would replace a client's malformed traceparent, which the Envoy output passes on as
// sent, and the client's version and ids, which nginx keeps for a traceparent of another version than 00.
// $pf_otel_trace is the script's draw (traced), the Envoy output's rule on the request id, and a client's sampled flag
// never forces a span.
//
// A span holds the target only as nginx sends it on (the script's targetTraced): nginx's tracer always adds the whole
// target as the client wrote it, query included, as http.target, which no line removes, where the Envoy output's URL
// holds the path it normalised, escaped and rewrote, cut at 256 characters, or a client's x-envoy-original-path in its
// place under client_address forwarded. So $pf_target_traced answers 1 only for a target of at most 256 bytes that
// nginx sends on as written, path mode raw or rawk (writePathClasses), with no backslash, which Envoy reads as a
// slash, and no dot segment with a ; parameter, which Envoy resolves (measured on Envoy 1.40.2 beside nginx 1.30.6
// and 1.31.8 over a table of targets); $pf_original_path names the header the script looks for, or nothing under
// connection; and tracingRoutes says which routes rewrite the path.
//
// The tags are read by the script (tagSources, tagLines) and written as attributes after the span's fields
// (attributeLines).
func tracingHTTP(t *model.Tracing, g *model.Gateway, e edge, v *values) []string {
	if t == nil {
		return nil
	}
	sources, read := tagSources(t, g, e, v)
	return slices.Concat(tracerLines(t.OTel, g, e), tagLines(t, sources, read, v), attributeLines(t, v))
}

// tracerLines are the tracer's own lines and the maps and the script's values the span and the trace headers read.
func tracerLines(otel *model.OTel, g *model.Gateway, e edge) []string {
	lines := []string{
		"  otel_exporter { endpoint " + collectorEndpoint(otel, g.Listen.Number) + "; }",
		"  otel_service_name " + quoted(otel.Service) + ";",
		"  otel_trace $pf_otel_trace;",
		"  otel_trace_context extract;",
		"  otel_span_name $pf_span_name;",
		// The share of 10,000 the draw takes: sample.rate in hundredths of a percent.
		fmt.Sprintf(`  map "" $pf_trace_share { default %d; }`, otel.RatePercent*100),
		`  map "$pf_mode:$pf_f_backslash:$request_uri" $pf_target_traced { ` + regexKey(`^rawk?:0:(?=.{0,256}\z)(?![^?]*/\.\.?;)`) + ` 1; default ""; }`,
	}
	lines = append(lines, urlLines(e)...)
	lines = append(lines,
		`  map $pf_log_route $pf_span_name { "" ingress; default $pf_log_route; }`,
		`  map $content_length $pf_span_request_size { "" 0; default $content_length; }`,
		"  js_set $pf_otel_trace pf.traced;",
		"  js_set "+serviceTrace.parent+" pf.traceparent;",
		"  js_set "+serviceTrace.state+" pf.tracestate;",
	)
	if asked(g) != nil {
		lines = append(lines, "  js_set "+checkTrace.parent+" pf.checkTraceparent;",
			"  js_set "+checkTrace.state+" pf.checkTracestate;")
	}
	// The request id as Envoy reads it, for the span; and whether a gRPC location refuses the call (grpcTraceRefusal).
	return append(lines, "  js_set $pf_trace_request_id pf.requestId;", "  js_set $pf_trace_lines pf.traceLines;")
}

// tagSources are the values the script reads for the tags (tagSource), each once however many tags read it: the
// user-agent field's first, then each header or variable no tag before it reads; read[n] is the position from 1 of the
// one tag n reads. They are one value in quotes, so the first tag whose source makes it longer than nginx reads is
// refused, at the tag, by its key, the one position the model keeps for it.
func tagSources(t *model.Tracing, g *model.Gateway, e edge, v *values) (sources []string, read []int) {
	sources = []string{tagSource("user-agent", g, e)}
	read = make([]int, len(t.Tags))
	for n, tag := range t.Tags {
		source := "e:" + tag.Env
		if tag.Env == "" {
			source = tagSource(tag.Header, g, e)
		}
		if read[n] = slices.Index(sources, source) + 1; read[n] == 0 {
			sources = append(sources, source)
			read[n] = len(sources)
		}
	}
	if overLong(quotedSources(sources)) != 0 {
		for n, tag := range t.Tags {
			if overLong(quotedSources(sources[:read[n]])) != 0 {
				v.Refuse(tag.Pos, tagKey, "whose header or variable name, with those of the tags before it, is longer than nginx reads", "nginx",
					"nginx reads the header and variable names of all tags from one value of at most 4094 bytes between its quotes; write fewer tags or shorter names, or render it for envoy")
				break
			}
		}
	}
	return sources, read
}

// quotedSources is sources as one value in quotes.
func quotedSources(sources []string) string {
	return quoted(dollars(strings.Join(sources, " ")))
}

// tagLines are the lines that give each tag its value. The script's one pass (readTags) reads each source and writes
// its value to $pf_tv_<n>, or its state to $pf_ts_<n>, n for none and e for an empty value, so each source has
// variables of its own and no tag's map reads the values of the others: a request whose tags read a long header costs
// a worker no more for each other tag. A tag's default stands where its source gives none. A tag named like a field
// takes the field's place, as a custom tag replaces the Envoy output's value, and gives the field's value where its
// source gives none and it has no default, or gives an empty value, where the Envoy output sets no tag and keeps its
// own value (measured on Envoy 1.40.2 with a header and an environment variable absent and empty, a default written
// and not). A tag whose default nginx would not read from its file is refused at the tag, by its key.
func tagLines(t *model.Tracing, sources []string, read []int, v *values) []string {
	lines := []string{`  map "" $pf_tag_sources { default ` + quotedSources(sources) + "; }", "  js_set $pf_tags_read pf.readTags;"}
	for n := range sources {
		lines = append(lines, fmt.Sprintf("  js_var $pf_tv_%d;", n+1), fmt.Sprintf("  js_var $pf_ts_%d;", n+1))
	}
	// Each map's key starts with $pf_tags_read, which is empty, so the script's pass has written the variables when
	// the map reads them (measured on nginx 1.30.6 and 1.31.8: an attribute that reads a variable the pass writes
	// before the pass runs gets its empty start).
	lines = append(lines, `  map "${pf_tags_read}$pf_ts_1" $pf_span_user_agent { n -; default $pf_tv_1; }`)
	for n, tag := range t.Tags {
		field := slices.IndexFunc(spanFields, func(f spanField) bool { return f.name == tag.Name })
		none := `""`
		if tag.Default != "" {
			if none = quoted(dollars(tag.Default)); overLong(none) != 0 {
				v.Refuse(tag.Pos, tagKey, "whose default's nginx form is longer than nginx reads", "nginx",
					"nginx reads no value longer than 4094 bytes between its quotes, and writes each $ of a default as ${pf_dollar} and each \" and \\ with a backslash before it; write a shorter default, or render it for envoy")
			}
		} else if field >= 0 {
			none = spanFields[field].value
		}
		entries := []string{"n " + none}
		if field >= 0 {
			entries = append(entries, "e "+spanFields[field].value)
		}
		entries = append(entries, fmt.Sprintf("default $pf_tv_%d", read[n]))
		lines = append(lines, fmt.Sprintf(`  map "${pf_tags_read}$pf_ts_%d" $pf_tag_%d { %s; }`, read[n], n+1, strings.Join(entries, "; ")))
	}
	return lines
}

// attributeLines are the span's attributes: each field no tag is named like, then each tag, whose name nginx would
// not read from its file is refused at the tag, by its key.
func attributeLines(t *model.Tracing, v *values) []string {
	var lines []string
	for _, field := range spanFields {
		if !slices.ContainsFunc(t.Tags, func(tag model.Tag) bool { return tag.Name == field.name }) {
			lines = append(lines, "  otel_span_attr "+field.name+" "+field.value+";")
		}
	}
	for n, tag := range t.Tags {
		lines = append(lines, fmt.Sprintf("  otel_span_attr %s $pf_tag_%d;", v.keyToken(tagKey, tag.Pos, tag.Name), n+1))
	}
	return lines
}

// urlLines are the maps of http.url's scheme and host, the Envoy output's :scheme and :authority after its router's
// changes (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). The scheme is an absolute target's, http or https
// in any letter case, written in lower case; with client_address forwarded, over HTTP/1, then a client's
// X-Forwarded-Proto that is http or https in any letter case with spaces and tabs around it, which nginx keeps; and
// otherwise the connection's, which Envoy writes beside any other X-Forwarded-Proto the service receives. Over HTTP/2
// Envoy writes the client's :scheme, which nginx does not read, and nginx the connection's. The host is the authority
// nginx picked the server by, which a route's location to a service with a host_header sets to that host
// (writeLocation): the span reads it once the request is answered. $pf_original_path names the header whose line the
// script looks for (targetTraced), none under connection, whose clients' x-envoy-original-path the Envoy output
// removes.
func urlLines(e edge) []string {
	lines := []string{`  map "" $pf_original_path { default x-envoy-original-path; }`,
		`  map $request $pf_target_scheme { ` + quoted(`~*^[^ ]+ +https://`) + ` https; ` + quoted(`~*^[^ ]+ +http://`) + ` http; default ""; }`}
	if proto := e.read("x-forwarded-proto"); e.connection || proto == "" {
		if e.connection {
			lines[0] = `  map "" $pf_original_path { default ""; }`
		}
		lines = append(lines, `  map $pf_target_scheme $pf_url_scheme { "" $scheme; default $pf_target_scheme; }`)
	} else {
		lines = append(lines, `  map "$pf_target_scheme:$server_protocol:`+proto+`" $pf_url_scheme { `+regexKey(`^(?<pf_url_s>https?):`)+` $pf_url_s; `+
			quoted(`~*^:HTTP/1\.[0-9]:[ \t]*https[ \t]*\z`)+` https; `+quoted(`~*^:HTTP/1\.[0-9]:[ \t]*http[ \t]*\z`)+` http; default $scheme; }`)
	}
	return append(lines, `  map "" $pf_url_host { default $pf_authority; }`)
}

// tracingRoutes are the lines a file with tracing adds after the route choice, whose value, choice, is a route's
// number followed by w for a route with a rewrite (writeRouteChoice): $pf_rewritten is 1 when the route the request
// takes rewrites its path, or the route a path it refuses is on would, as the Envoy output's span then holds the path
// it rewrote, where nginx's holds the client's (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8).
func tracingRoutes(t *model.Tracing, choice string) []string {
	if t == nil {
		return nil
	}
	return []string{`  map "$pf_route:` + choice + `" $pf_rewritten { ` + regexKey(`^(?:[0-9]+|refused|dotparam):[0-9]+w\z`) + ` 1; default 0; }`, ""}
}

// tagKey is the key the support table names a tag by.
const tagKey = "tracing.tags.<tag name>"

// tagSource is where a tag on header reads its value, as the Envoy output's tag reads the request's header after the
// gateway's own changes (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2): v:<variable> for a header the
// proxy writes itself, the variable it writes it from, so the span holds what the service receives (the request id,
// the scheme, the client's address with client_address connection, and with the check asked an accepted name, empty
// off a checked route, so its default applies); a: for a header the proxy removes from every client, which never
// reaches the service, so its default applies (remove_from_clients and the names the early header removal adds,
// generator.RemovedByEnvoy, generator.RemovedByEnvoyFacingClients with connection, and format.NginxRemovedTraced); j:<name> for a
// header whose lines Envoy joins (generator.JoinedByEnvoy), its lines joined as Envoy joins them; and h:<name> for any
// other, the client's first line. A tag on any other name nginx writes itself, such as host, origin, te or expect, is
// refused before the generator runs.
func tagSource(header string, g *model.Gateway, e edge) string {
	switch {
	case header == "x-request-id":
		return "v:pf_request_id"
	case header == "x-forwarded-proto":
		return "v:pf_forwarded_proto"
	case header == "x-forwarded-for" && e.connection:
		return "v:pf_forwarded_for"
	}
	if asked(g) != nil {
		for n, accepted := range e.accept {
			if strings.ToLower(accepted.Name) == header {
				return fmt.Sprintf("v:pf_accept_%d", n)
			}
		}
	}
	if e.removed[header] || slices.Contains(generator.RemovedByEnvoy, header) ||
		e.connection && slices.Contains(generator.RemovedByEnvoyFacingClients, header) || e.traced && slices.Contains(format.NginxRemovedTraced, header) {
		return "a:"
	}
	if slices.Contains(generator.JoinedByEnvoy, header) {
		return "j:" + header
	}
	return "h:" + header
}

// traceNames are the trace's headers, which the script writes for the service and the check.
var traceNames = []string{"traceparent", "tracestate"}

// grpcTraceRefusal are a gRPC route location's first lines with tracing: a call whose traceparent is malformed and
// comes with a second line of it or of tracestate ($pf_trace_lines) is answered as a request refused before any route,
// before the location sets or asks anything. The Envoy output passes each line on as a field of its own, and nginx's
// gRPC module sends one field for each grpc_set_header line, with a line break in a value sent inside the field, which
// the service refuses (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
var grpcTraceRefusal = []string{"      error_page 418 = @pf_r_bad-request;", "      if ($pf_trace_lines) {", "        return 418;", "      }"}

// trace names the variables the script writes a request's traceparent and tracestate in: the service's, or the
// check's, which are the service's but empty beside a malformed traceparent, as the Envoy output's check then receives
// neither header (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
type trace struct{ parent, state string }

var (
	serviceTrace = trace{"$pf_traceparent", "$pf_tracestate"}
	checkTrace   = trace{"$pf_check_traceparent", "$pf_check_tracestate"}
)

// traceHeaders sends the trace the script chose, from the variables of to, on the request a location passes on, with
// tracing, through the location's own list, so each name is written once and counted in its header table.
func (h *locationHeaders) traceHeaders(e edge, to trace) {
	if !e.traced {
		return
	}
	h.send(traceNames[0], traceNames[0], to.parent)
	h.send(traceNames[1], traceNames[1], to.state)
}
