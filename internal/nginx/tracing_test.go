package nginx_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// tracingNeeds is line 2 of a file with tracing, and njsNeeds line 2 of every other file.
const (
	tracingNeeds = "# Needs the njs module, 1.0.2 or later, and ngx_otel_module, in the nginx images tagged -otel."
	njsNeeds     = "# Needs the njs module, 1.0.2 or later."
)

// tracingStart is the http block's start for the battery's auth file with tracingBlock: the map table's sizes, which
// come before every map, then the tracing lines in the order they are written.
var tracingStart = []string{
	"http {",
	"  map_hash_max_size 2048;",
	"  map_hash_bucket_size 192;",
	"  otel_exporter { endpoint 10.0.3.40:4317; }",
	`  otel_service_name "edge gw";`,
	"  otel_trace $pf_otel_trace;",
	"  otel_trace_context extract;",
	"  otel_span_name $pf_span_name;",
	`  map "" $pf_trace_share { default 500; }`,
	`  map "$pf_mode:$pf_f_backslash:$request_uri" $pf_target_traced { "~^rawk?:0:(?=.{0,256}\\z)(?![^?]*/\\.\\.?;)" 1; default ""; }`,
	`  map "" $pf_original_path { default ""; }`,
	`  map $request $pf_target_scheme { "~*^[^ ]+ +https://" https; "~*^[^ ]+ +http://" http; default ""; }`,
	`  map $pf_target_scheme $pf_url_scheme { "" $scheme; default $pf_target_scheme; }`,
	`  map "" $pf_url_host { default $pf_authority; }`,
	`  map $pf_log_route $pf_span_name { "" ingress; default $pf_log_route; }`,
	`  map $content_length $pf_span_request_size { "" 0; default $content_length; }`,
	"  js_set $pf_otel_trace pf.traced;",
	"  js_set $pf_traceparent pf.traceparent;",
	"  js_set $pf_tracestate pf.tracestate;",
	"  js_set $pf_check_traceparent pf.checkTraceparent;",
	"  js_set $pf_check_tracestate pf.checkTracestate;",
	"  js_set $pf_trace_request_id pf.requestId;",
	"  js_set $pf_trace_lines pf.traceLines;",
	`  map "" $pf_tag_sources { default "j:user-agent e:APP_ENVIRONMENT v:pf_request_id h:x-tenant"; }`,
	"  js_set $pf_tags_read pf.readTags;",
	"  js_var $pf_tv_1;",
	"  js_var $pf_ts_1;",
	"  js_var $pf_tv_2;",
	"  js_var $pf_ts_2;",
	"  js_var $pf_tv_3;",
	"  js_var $pf_ts_3;",
	"  js_var $pf_tv_4;",
	"  js_var $pf_ts_4;",
	`  map "${pf_tags_read}$pf_ts_1" $pf_span_user_agent { n -; default $pf_tv_1; }`,
	`  map "${pf_tags_read}$pf_ts_2" $pf_tag_1 { n "local"; default $pf_tv_2; }`,
	`  map "${pf_tags_read}$pf_ts_3" $pf_tag_2 { n ""; default $pf_tv_3; }`,
	`  map "${pf_tags_read}$pf_ts_4" $pf_tag_3 { n "anon"; default $pf_tv_4; }`,
	`  otel_span_attr node_id "";`,
	`  otel_span_attr zone "";`,
	"  otel_span_attr guid:x-request-id $pf_trace_request_id;",
	`  otel_span_attr http.url "$pf_url_scheme://$pf_url_host$request_uri";`,
	"  otel_span_attr downstream_cluster -;",
	"  otel_span_attr user_agent $pf_span_user_agent;",
	"  otel_span_attr http.protocol $pf_log_protocol;",
	"  otel_span_attr peer.address $remote_addr;",
	"  otel_span_attr request_size $pf_span_request_size;",
	"  otel_span_attr response_size $body_bytes_sent;",
	"  otel_span_attr component proxy;",
	"  otel_span_attr upstream_cluster $pf_log_upstream;",
	"  otel_span_attr upstream_cluster.name $pf_log_upstream;",
	"  otel_span_attr environment $pf_tag_1;",
	"  otel_span_attr request_id $pf_tag_2;",
	"  otel_span_attr tenant $pf_tag_3;",
}

// passingLocations returns every location of the environment servers of out that passes a request to a service or
// to the check's relay, the route locations and the check location, each with the directive that sets its headers.
func passingLocations(out string) (found [][]string, sets []string) {
	found, sets = routeLocations(out)
	for _, server := range environmentServers(out) {
		for _, location := range blocks(server, "    location = /.proxyfile/check {") {
			found, sets = append(found, location), append(sets, "proxy_set_header")
		}
	}
	return found, sets
}

// httpLines returns the lines of out's http block, from its opening line to its closing brace.
func httpLines(out string) []string {
	all := lines(out)
	start := slices.Index(all, "http {")
	if start < 0 {
		return nil
	}
	end := start + slices.Index(all[start:], "}")
	return all[start : end+1]
}

// countOf returns how many of all are line.
func countOf(all []string, line string) int {
	n := 0
	for _, l := range all {
		if l == line {
			n++
		}
	}
	return n
}

// TestTracingLines: a file with tracing loads ngx_otel_module and keeps the variables its env tags read, writes the
// tracing lines first in the http block after the map table's sizes, and sends the trace on every request it passes:
// to a service the service's values, and to the check the check's, which the script leaves empty beside a malformed
// traceparent, as the Envoy output's check then receives neither header (measured on nginx 1.30.6 and 1.31.8 beside
// Envoy 1.40.2).
func TestTracingLines(t *testing.T) {
	out := generate(t, string(testsupport.ReadFile(t, authFile))+tracingBlock)
	all := lines(out)
	cases := []struct {
		name  string
		check func() string
	}{
		{"the module, the env line and the http lines are written in this order", func() string {
			top := []string{"load_module modules/ngx_http_js_module.so;", "load_module modules/ngx_stream_js_module.so;",
				"load_module modules/ngx_otel_module.so;", "env APP_ENVIRONMENT;", "worker_processes auto;"}
			if len(all) < 7 || !slices.Equal(all[2:7], top) {
				return fmt.Sprintf("lines 3 to 7 are %q, want %q", all[2:min(7, len(all))], top)
			}
			for _, line := range []string{"load_module modules/ngx_otel_module.so;", "env APP_ENVIRONMENT;"} {
				if n := countOf(all, line); n != 1 {
					return fmt.Sprintf("the file holds %q %d times, want once", line, n)
				}
			}
			if block := testsupport.FirstMissingBlock(out, [][]string{tracingStart}); block != "" {
				return "the file holds no block:\n" + block
			}
			return ""
		}},
		{"every location that passes a request carries the trace", func() string {
			http := httpLines(out)
			passing := 0
			for _, line := range http {
				if word := strings.TrimLeft(line, " \t"); strings.HasPrefix(word, "proxy_pass ") || strings.HasPrefix(word, "grpc_pass ") {
					passing++
				}
			}
			traced := countOf(http, "      proxy_set_header traceparent $pf_traceparent;") +
				countOf(http, "      grpc_set_header traceparent $pf_traceparent;") +
				countOf(http, "      proxy_set_header traceparent $pf_check_traceparent;")
			if passing != 676 || traced != 676 {
				return fmt.Sprintf("the http block holds %d lines that pass a request and %d that send traceparent, want 676 of each", passing, traced)
			}
			found, sets := passingLocations(out)
			if len(found) != 676 {
				return fmt.Sprintf("the environment servers hold %d locations that pass a request, want 676", len(found))
			}
			checks := 0
			for i, location := range found {
				values := "$pf_"
				if location[0] == "    location = /.proxyfile/check {" {
					values = "$pf_check_"
					checks++
				}
				pair := []string{"      " + sets[i] + " traceparent " + values + "traceparent;", "      " + sets[i] + " tracestate " + values + "tracestate;"}
				if block := testsupport.FirstMissingBlock(strings.Join(location, "\n"), [][]string{pair}); block != "" {
					return location[0] + " lacks:\n" + block
				}
			}
			if checks != 4 {
				return fmt.Sprintf("the file holds %d check locations, want 4", checks)
			}
			return ""
		}},
		{"the check's relay sends the trace", func() string {
			return missing(out, `    set $pf_check_send "authorization x-country x-client-* http2-settings x-gateway-timeout-ms x-envoy-internal `+
				`x-envoy-expected-rq-timeout-ms x-gateway-action x-gateway-reports x-forwarded-uri x-forwarded-method x-forwarded-proto `+
				`x-forwarded-host traceparent tracestate";`)
		}},
		{"the check's relay writes the trace's bytes as the script made them, in a file with tracing alone", func() string {
			if broken := missing(out, `    set $pf_check_as_sent "traceparent tracestate";`); broken != "" {
				return broken
			}
			if without := generate(t, string(testsupport.ReadFile(t, authFile))); strings.Contains(without, "pf_check_as_sent") {
				return "the file without tracing names $pf_check_as_sent"
			}
			return ""
		}},
		{"a gRPC location refuses a malformed traceparent of more than one line before anything else", func() string {
			// nginx's gRPC module sends one field for each grpc_set_header line, where the Envoy output passes each line
			// of a malformed traceparent, and of the tracestate beside it, as a field of its own.
			refusal := []string{"      error_page 418 = @pf_r_bad-request;", "      if ($pf_trace_lines) {", "        return 418;", "      }"}
			found, sets := routeLocations(out)
			grpc := 0
			for i, location := range found {
				if sets[i] != "grpc_set_header" {
					continue
				}
				grpc++
				if !slices.Equal(location[1:5], refusal) {
					return fmt.Sprintf("%s's lines 2 to 5 are %q, want %q", location[0], location[1:5], refusal)
				}
			}
			if grpc != 24 {
				return fmt.Sprintf("the environment servers hold %d gRPC locations, want 24", grpc)
			}
			return ""
		}},
		{"a span holds no target nginx rewrites, nor one beside a client's x-envoy-original-path under forwarded", func() string {
			// The route choice's value ends in w for a route with a rewrite, which $pf_route drops: the route taken, or
			// the one a refused path is on, rewrites the path the service receives.
			if broken := missing(out, `  map "$pf_route:$pf_g3" $pf_rewritten { "~^(?:[0-9]+|refused|dotparam):[0-9]+w\\z" 1; default 0; }`); broken != "" {
				return broken
			}
			return missing(generate(t, tagFile(t, "forwarded", "x-tenant")), `  map "" $pf_original_path { default x-envoy-original-path; }`)
		}},
		{"the URL's scheme and host are the ones the Envoy output's URL holds", func() string {
			// Under forwarded an absolute target's scheme, then over HTTP/1 a client's X-Forwarded-Proto that is http or
			// https in any letter case with spaces and tabs around it, then the connection's; a route to a service with a
			// host_header holds that host.
			forwarded := generate(t, tagFile(t, "forwarded", "x-tenant"))
			if broken := missing(forwarded, `  map "$pf_target_scheme:$server_protocol:$http_x_forwarded_proto" $pf_url_scheme { "~^(?<pf_url_s>https?):" $pf_url_s; `+
				`"~*^:HTTP/1\\.[0-9]:[ \\t]*https[ \\t]*\\z" https; "~*^:HTTP/1\\.[0-9]:[ \\t]*http[ \\t]*\\z" http; default $scheme; }`); broken != "" {
				return broken
			}
			// The auth file's first route is to accounts, whose host_header is 127.0.0.1:7070.
			found := 0
			for _, server := range environmentServers(out) {
				for _, location := range blocks(server, "    location @pf_r_1 {") {
					found++
					if !slices.Contains(location, `      set $pf_url_host "127.0.0.1:7070";`) {
						return location[0] + " sets no $pf_url_host"
					}
				}
			}
			if found != 4 {
				return fmt.Sprintf("the environment servers hold %d locations @pf_r_1, want 4", found)
			}
			return ""
		}},
		{"line 2 names the module and the -otel images", func() string {
			if want := []string{tracingNeeds, "load_module modules/ngx_http_js_module.so;"}; len(all) < 3 || !slices.Equal(all[1:3], want) {
				return fmt.Sprintf("lines 2 and 3 are %q, want %q", all[1:min(3, len(all))], want)
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if broken := c.check(); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// tagFile is the battery's auth file under client_address mode with tracing and the one tag tag, on header.
func tagFile(t *testing.T, mode, header string) string {
	t.Helper()
	file := rewritten(t, string(testsupport.ReadFile(t, authFile)), "client_address: connection\n", "client_address: "+mode+"\n")
	return file + "tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n" +
		"  tags:\n    tag: {header: " + header + "}\n  path_in_url: true\n"
}

// TestTagSource: a tag on a header the proxy writes itself reads the value the service receives, from the variable
// the proxy writes it from, as the Envoy output's tag reads the header after its own changes; a tag on a header the
// proxy removes reads none, so its default applies; any other reads the client's first line (measured on nginx 1.30.6
// and 1.31.8 beside Envoy 1.40.2). The battery's auth file accepts x-user-id first and removes x-client-id from clients.
func TestTagSource(t *testing.T) {
	rows := []struct {
		name, header, connection, forwarded string
	}{
		{"the request id", "x-request-id", "v:pf_request_id", "v:pf_request_id"},
		{"the scheme", "x-forwarded-proto", "v:pf_forwarded_proto", "v:pf_forwarded_proto"},
		{"the client's address, written with connection alone", "x-forwarded-for", "v:pf_forwarded_for", "j:x-forwarded-for"},
		{"the first accepted name", "x-user-id", "v:pf_accept_0", "v:pf_accept_0"},
		{"a name remove_from_clients lists", "x-client-id", "a:", "a:"},
		{"a name the Envoy output removes", "x-envoy-ip-tags", "a:", "a:"},
		{"a name the Envoy output removes with otel", "x-envoy-decorator-operation", "a:", "a:"},
		{"a header Envoy joins", "accept", "j:accept", "j:accept"},
		{"any other header", "x-tenant", "h:x-tenant", "h:x-tenant"},
	}
	for _, row := range rows {
		for _, mode := range []struct{ name, want string }{{"connection", row.connection}, {"forwarded", row.forwarded}} {
			t.Run(row.name+", "+mode.name, func(t *testing.T) {
				out := generate(t, tagFile(t, mode.name, row.header))
				if broken := missing(out, `  map "" $pf_tag_sources { default "j:user-agent `+mode.want+`"; }`); broken != "" {
					t.Error(broken)
				}
			})
		}
	}
	// With the check switched off no handler sets an accepted name, which the proxy then removes from clients.
	t.Run("an accepted name with the check switched off", func(t *testing.T) {
		out := generate(t, rewritten(t, tagFile(t, "connection", "x-user-id"), "  enabled: true\n", "  enabled: false\n"))
		if broken := missing(out, `  map "" $pf_tag_sources { default "j:user-agent a:"; }`); broken != "" {
			t.Error(broken)
		}
	})
}

// TestTracingQuoting: the service name and a tag's default reach the span as written: nginx reads \ and " in quotes as
// escapes and $ in a map's value as a variable, so each is written escaped and a $ as ${pf_dollar}. The format
// refuses " in a service name, and a header or variable name holds none of the three.
func TestTracingQuoting(t *testing.T) {
	out := generate(t, smallFile("{name: app, prefix: /, to: app}")+"tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: 'a\\b'}\n"+
		"  tags:\n    odd: {header: x-odd, default: 'x$y\"z\\w'}\n  path_in_url: true\n")
	for _, line := range []string{
		`  otel_service_name "a\\b";`,
		`  map "${pf_tags_read}$pf_ts_2" $pf_tag_1 { n "x${pf_dollar}y\"z\\w"; default $pf_tv_2; }`,
		`  geo $pf_dollar { default "$"; }`,
	} {
		t.Run(line, func(t *testing.T) {
			if broken := missing(out, line); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// fieldTags names a tag after each field the tracing lines write with no dot in its name, which the format lets a tag
// name take, with the value the tracing lines write the field from.
var fieldTags = []struct{ name, value string }{
	{"node_id", `""`}, {"zone", `""`}, {"downstream_cluster", "-"}, {"user_agent", "$pf_span_user_agent"},
	{"request_size", "$pf_span_request_size"}, {"response_size", "$body_bytes_sent"}, {"component", "proxy"},
	{"upstream_cluster", "$pf_log_upstream"},
}

// tagMap is the map of tag n named like a field, which reads source n+1, the user-agent field's source being the
// first: none for a source that gives none, empty for one that gives an empty value, and otherwise the value.
func tagMap(n int, none, empty string) string {
	return fmt.Sprintf(`  map "${pf_tags_read}$pf_ts_%d" $pf_tag_%d { n %s; e %s; default $pf_tv_%d; }`, n+1, n, none, empty, n+1)
}

// TestTracingAround: what tracing changes beyond its own lines. The admin server writes no span, as Envoy's admin writes
// none; a client's own x-envoy-downstream-service-cluster, x-envoy-downstream-service-node and
// x-envoy-decorator-operation reach neither a service nor the check under either client_address, as the Envoy output
// removes them with otel; a tag named like a field replaces the field, as a custom tag replaces Envoy's value, and
// keeps the field's value where its source gives none and it has no default, or gives an empty value, as the Envoy
// output then sets no tag (measured on Envoy 1.40.2 with headers and environment variables absent and empty); and the
// variables table holds the block's variables.
func TestTracingAround(t *testing.T) {
	auth := string(testsupport.ReadFile(t, authFile))
	forwarded := rewritten(t, auth, "client_address: connection\n", "client_address: forwarded\n")
	traced := generate(t, auth+tracingBlock)
	forwardedTraced := generate(t, forwarded+tracingBlock)
	forwardedPlain := generate(t, forwarded)
	var named strings.Builder
	named.WriteString("tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n  tags:\n")
	for _, field := range fieldTags {
		named.WriteString("    " + field.name + ": {header: x-" + strings.ReplaceAll(field.name, "_", "-") + "}\n")
	}
	named.WriteString("  path_in_url: true\n")
	fields := generate(t, auth+named.String())
	// A tag named like a field with a default, and one on an environment variable.
	defaulted := generate(t, auth+"tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n  tags:\n"+
		"    downstream_cluster: {header: x-dc, default: dflt}\n    request_size: {env: PF_RS}\n  path_in_url: true\n")
	var many strings.Builder
	many.WriteString("tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n  tags:\n")
	for n := 1; n <= 150; n++ {
		fmt.Fprintf(&many, "    t%d: {header: x-t%d}\n", n, n)
	}
	many.WriteString("  path_in_url: true\n")
	manyTags := generate(t, auth+many.String())
	// Tags that share a header or a variable, the first header read again after the variable.
	shared := generate(t, auth+"tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n  tags:\n"+
		"    a: {header: x-a}\n    b: {header: x-a, default: d}\n    c: {env: PF_A}\n    d: {env: PF_A}\n    e: {header: x-a}\n  path_in_url: true\n")
	removed := []string{"x-envoy-decorator-operation", "x-envoy-downstream-service-cluster", "x-envoy-downstream-service-node"}
	cases := []struct {
		name  string
		check func() string
	}{
		{"the admin server writes no span", func() string {
			if block := testsupport.FirstMissingBlock(traced, [][]string{{"    listen 127.0.0.1:9901;", "    access_log off;", "    otel_trace off;"}}); block != "" {
				return "the file holds no block:\n" + block
			}
			return ""
		}},
		{"with forwarded every location and the check remove the three tracing headers", func() string {
			found, sets := passingLocations(forwardedTraced)
			if len(found) != 676 {
				return fmt.Sprintf("the environment servers hold %d locations that pass a request, want 676", len(found))
			}
			for i, location := range found {
				for _, name := range removed {
					if line := "      " + sets[i] + " " + name + ` "";`; !slices.Contains(location, line) {
						return fmt.Sprintf("%s holds no %q", location[0], line)
					}
				}
			}
			return ""
		}},
		// The support table refuses only_with_header naming one of them with tracing, from its own copy of the names.
		{"format's list of the headers tracing removes holds the three", func() string {
			if !slices.Equal(format.NginxRemovedTraced, removed) {
				return fmt.Sprintf("format.NginxRemovedTraced = %q, want %q, the names every location removes with tracing", format.NginxRemovedTraced, removed)
			}
			return ""
		}},
		{"with forwarded and no tracing the three pass, as the base writes them", func() string {
			for _, name := range removed {
				if strings.Contains(forwardedPlain, "_set_header "+name+" ") {
					return fmt.Sprintf("the file without tracing removes %s", name)
				}
			}
			return ""
		}},
		{"each location writes traceparent and tracestate once", func() string {
			found, sets := passingLocations(traced)
			for i, location := range found {
				for _, name := range []string{"traceparent", "tracestate"} {
					n := 0
					for _, line := range location {
						if strings.HasPrefix(line, "      "+sets[i]+" "+name+" ") {
							n++
						}
					}
					if n != 1 {
						return fmt.Sprintf("%s writes %s %d times, want once", location[0], name, n)
					}
				}
			}
			return ""
		}},
		{"the variables table holds the tracing block's variables", func() string {
			// The auth file declares 238 variables. The block adds 21 with the check's two, three more for each tag that
			// reads a source no tag before it reads: with 3 tags 4 times 268 is under nginx's floor of 2048, and with 150
			// tags 4 times 709 is 2836.
			if broken := missing(traced, "  variables_hash_max_size 2048;"); broken != "" {
				return broken
			}
			return missing(manyTags, "  variables_hash_max_size 2836;")
		}},
	}
	for n, field := range fieldTags {
		tag := fmt.Sprintf("  otel_span_attr %s $pf_tag_%d;", field.name, n+1)
		cases = append(cases, struct {
			name  string
			check func() string
		}{"a tag named " + field.name + " replaces the field and keeps its value when it gives none", func() string {
			for _, line := range lines(fields) {
				if strings.HasPrefix(line, "  otel_span_attr "+field.name+" ") && line != tag {
					return fmt.Sprintf("the file holds the field's own line %q beside the tag's", line)
				}
			}
			if broken := missing(fields, tag); broken != "" {
				return broken
			}
			return missing(fields, tagMap(n+1, field.value, field.value))
		}})
	}
	cases = append(cases, struct {
		name  string
		check func() string
	}{"tags that read one header or one variable read it once, each from its one place", func() string {
		for _, line := range []string{
			`  map "" $pf_tag_sources { default "j:user-agent h:x-a e:PF_A"; }`,
			`  map "${pf_tags_read}$pf_ts_2" $pf_tag_1 { n ""; default $pf_tv_2; }`,
			`  map "${pf_tags_read}$pf_ts_2" $pf_tag_2 { n "d"; default $pf_tv_2; }`,
			`  map "${pf_tags_read}$pf_ts_3" $pf_tag_3 { n ""; default $pf_tv_3; }`,
			`  map "${pf_tags_read}$pf_ts_3" $pf_tag_4 { n ""; default $pf_tv_3; }`,
			`  map "${pf_tags_read}$pf_ts_2" $pf_tag_5 { n ""; default $pf_tv_2; }`,
		} {
			if broken := missing(shared, line); broken != "" {
				return broken
			}
		}
		return ""
	}})
	cases = append(cases, struct {
		name  string
		check func() string
	}{"a tag named like a field takes its default when its source gives none, and the field's value when empty", func() string {
		for _, line := range []string{
			`  map "" $pf_tag_sources { default "j:user-agent h:x-dc e:PF_RS"; }`,
			tagMap(1, `"dflt"`, "-"),
			tagMap(2, "$pf_span_request_size", "$pf_span_request_size"),
		} {
			if broken := missing(defaulted, line); broken != "" {
				return broken
			}
		}
		return ""
	}})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if broken := c.check(); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// TestNoTracingNoModule: a file without tracing loads no tracing module and writes none of its lines, so the plain
// images keep running it and its configuration keeps the bytes it had before tracing was written: every committed
// example that renders for nginx and every battery file.
func TestNoTracingNoModule(t *testing.T) {
	examples, err := filepath.Glob("../../examples/*/*/proxyfile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, path := range examples {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "nginx.conf")); err == nil {
			paths = append(paths, path)
		}
	}
	battery, err := filepath.Glob("../../conformance/testdata/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range battery {
		if !strings.Contains(string(testsupport.ReadFile(t, path)), "tracing") {
			paths = append(paths, path)
		}
	}
	if len(paths) != 12 {
		t.Fatalf("found %d files, want the 6 examples that render for nginx and the 6 battery files: %q", len(paths), paths)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			files, err := generateAt(t, path)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			out := string(files[0].Bytes)
			for _, text := range []string{"otel", "pf_traceparent", "pf_tag_"} {
				if strings.Contains(out, text) {
					t.Errorf("the configuration holds %q", text)
				}
			}
			if all := lines(out); len(all) < 2 || all[1] != njsNeeds {
				t.Errorf("line 2 is %q, want %q", all[min(1, len(all)-1)], njsNeeds)
			}
		})
	}
}
