package nginx_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// checksFile and authFile are the battery's files with the auth check: under deny with two check addresses, and
// under allow beside every form of the keys the generator writes.
const (
	checksFile = "../../conformance/testdata/nginx-checks.yaml"
	authFile   = "../../conformance/testdata/nginx-auth.yaml"
)

// removedByEnvoyBeforeTheCheck are the check location's lines for generator.RemovedByEnvoy but the two the check is sent
// with values of the proxy's own, in their order.
var removedByEnvoyBeforeTheCheck = []string{
	`      proxy_set_header proxy-connection "";`,
	`      proxy_set_header x-envoy-force-trace "";`,
	`      proxy_set_header x-envoy-hedge-on-per-try-timeout "";`,
	`      proxy_set_header x-envoy-ip-tags "";`,
	`      proxy_set_header x-envoy-max-retries "";`,
	`      proxy_set_header x-envoy-original-url "";`,
	`      proxy_set_header x-envoy-retriable-header-names "";`,
	`      proxy_set_header x-envoy-retriable-status-codes "";`,
	`      proxy_set_header x-envoy-retry-grpc-on "";`,
	`      proxy_set_header x-envoy-retry-on "";`,
	`      proxy_set_header x-envoy-upstream-alt-stat-name "";`,
	`      proxy_set_header x-envoy-upstream-rq-per-try-timeout-ms "";`,
	`      proxy_set_header x-envoy-upstream-rq-timeout-alt-response "";`,
	`      proxy_set_header x-envoy-upstream-rq-timeout-ms "";`,
	`      proxy_set_header x-envoy-upstream-stream-duration-ms "";`,
	`      proxy_set_header x-forwarded-client-cert "";`,
}

// checkStart is the start of every check location of a file with a retried route, as both battery files with the check
// are: a client's own request for its path is routed as any other, to the route location's copy that tries once when
// the jump map sends it there, and the handler's subrequest is sent to the relay with no body, the HTTP2-Settings the
// service receives, and the question's host, method and path.
func checkStart(path string) []string {
	return []string{
		"    location = /.proxyfile/check {",
		`      if ($pf_checking = "") {`,
		"        return 418;",
		"      }",
		"      error_page 418 = @pf_r_$pf_route$pf_rt_once;",
		"      proxy_pass http://unix:/run/proxyfile-10000/check.sock;",
		"      proxy_pass_request_body off;",
		`      proxy_set_header Content-Length "";`,
		`      proxy_set_header Connection "";`,
		"      proxy_set_header HTTP2-Settings $pf_http2_settings;",
		"      proxy_set_header Host $pf_authority;",
		"      proxy_set_header x-proxyfile-method $request_method;",
		"      proxy_set_header x-proxyfile-path " + path + ";",
		"      proxy_set_header x-gateway-timeout-ms 1500;",
		"      proxy_set_header x-envoy-internal true;",
		"      proxy_set_header x-envoy-expected-rq-timeout-ms 1500;",
		"      proxy_set_header x-gateway-action $pf_action;",
		"      proxy_set_header x-gateway-reports $pf_reports;",
		"      proxy_set_header x-forwarded-uri $pf_told_uri;",
		"      proxy_set_header x-forwarded-method $request_method;",
		"      proxy_set_header x-forwarded-proto $pf_told_proto;",
		"      proxy_set_header x-forwarded-host $pf_authority;",
	}
}

// checkEnd is the end of every check location of a timeout of 1500ms and the location its failures go to.
var checkEnd = []string{
	"      proxy_connect_timeout 1500ms;",
	"      proxy_read_timeout 2500ms;",
	"      proxy_buffering off;",
	"      subrequest_output_buffer_size 1200k;",
	"      error_page 502 504 = @pf_check_failed;",
	"    }",
	"    location @pf_check_failed {",
	`      set $pf_check_failed "s=$upstream_status b=$upstream_bytes_received c=$upstream_connect_time ";`,
	"      return 502;",
	"    }",
}

// requestLog is the start of the request log line of a file with auth.ask, whose authz_us is written as given.
func requestLog(name, authz string) string {
	return "  log_format " + name + ` escape=json '{"authz_us":` + authz + `,"bytes_in":$request_length,"bytes_out":$body_bytes_sent,` +
		`"duration_ms":$pf_log_duration,"host":"$pf_log_host","method":"$request_method","protocol":"$pf_log_protocol",` +
		`"request_id":"$pf_log_request_id","route":"$pf_log_route","status":$pf_log_code,"time":"$time_iso8601",` +
		`"upstream":"$pf_log_upstream","upstream_ms":$pf_log_upstream_ms}';`
}

// authUnavailableLog is the gateway_auth_unavailable line and its access_log, written for a request that went on with
// the unchecked mark, with the Envoy output's fields in its order.
var authUnavailableLog = []string{
	`  log_format pf_auth_unavailable escape=json '{"action":"$pf_action","event":"gateway_auth_unavailable",` +
		`"failure_mode_allowed":"$pf_unchecked","request_id":"$pf_log_request_id","route":"$pf_log_route","status":$pf_log_code}';`,
	"  access_log /dev/stdout pf_auth_unavailable if=$pf_unchecked;",
}

// removalLines are the lines a route location writes with set for the names of nginx-auth.yaml's early removal, then
// the mark, each emptied: remove_from_clients, the two question headers, each accept name.
func removalLines(set string) []string {
	return []string{
		"      " + set + ` "x-client-id" "";`,
		"      " + set + ` x-gateway-action "";`,
		"      " + set + ` x-gateway-reports "";`,
		"      " + set + ` "x-user-id" "";`,
		"      " + set + ` "x-tier-level" "";`,
		"      " + set + ` "x-user-check" "";`,
		"      " + set + ` x-envoy-auth-failure-mode-allowed "";`,
	}
}

// routeLocations returns each location @pf_r_<n> and @pf_r_<n>-uri block of the environment servers of out, the ones
// that pass the request on, with the directive that sets their headers.
func routeLocations(out string) (found [][]string, sets []string) {
	for _, server := range environmentServers(out) {
		for _, location := range blocks(server, "    location @pf_r_") {
			switch {
			case slices.ContainsFunc(location, func(line string) bool { return strings.HasPrefix(line, "      grpc_pass ") }):
				sets = append(sets, "grpc_set_header")
			case slices.ContainsFunc(location, func(line string) bool { return strings.HasPrefix(line, "      proxy_pass ") }):
				sets = append(sets, "proxy_set_header")
			default:
				continue
			}
			found = append(found, location)
		}
	}
	return found, sets
}

// passes reports whether line sends the request on to a service.
func passes(line string) bool {
	return strings.HasPrefix(line, "      proxy_pass ") || strings.HasPrefix(line, "      grpc_pass ")
}

// everyLocationRemoves returns "" when every route location of out writes the removal lines, or the first that does
// not, and the count of route locations, which a row compares with a literal so it is seen to find what it reads.
func everyLocationRemoves(out string) (string, int) {
	found, sets := routeLocations(out)
	for i, location := range found {
		if missing := testsupport.FirstMissingBlock(strings.Join(location, "\n")+"\n", [][]string{removalLines(sets[i])}); missing != "" {
			return location[0] + " lacks:\n" + missing, len(found)
		}
	}
	return "", len(found)
}

// TestCheckLines: with auth.ask enabled the configuration writes the relay's stream block, the check location in every
// environment server, the handler on every checked route location with the accepted headers set from it, and the
// request log's lines for the check; switched off, or on a route with auth: off, it writes no handler and still
// removes a client's copy of each name the check would set or be asked with.
func TestCheckLines(t *testing.T) {
	checks := string(testsupport.ReadFile(t, checksFile))
	auth := string(testsupport.ReadFile(t, authFile))
	checksOut := generate(t, checks)
	authOut := generate(t, auth)
	off := generate(t, rewritten(t, auth, "  enabled: true\n", "  enabled: false\n"))
	// A file whose check is asked at a path holding $ and ", and one whose RPC method's action holds both.
	escapedPath := generate(t, "listen: 8000\nservices:\n  app: {http: 3000}\n  checks: {http: 7070}\n"+
		"auth: {ask: checks /a$b\"c, timeout: 1500ms, enabled: true, when_unavailable: deny}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: app, prefix: /, to: app}\n")
	// A file whose check is asked at a path holding characters outside ASCII.
	unicodePath := generate(t, rewritten(t, checks, "  ask: checks /auth/check\n", "  ask: \"checks /auth/é/ü?x=ß\"\n"))
	escapedQuestion := generate(t, rewritten(t, auth, "method: GetCart, action: browse}", `method: GetCart, action: 'read "q" $x'}`))
	keepAlive := generate(t, rewritten(t, checks, "  accept: [x-user-check]\n", "  accept: [x-user-id, keep-alive]\n"))
	// A name remove_from_clients lists that accept names too.
	both := generate(t, rewritten(t, checks, "remove_from_clients: [x-request-id]\n", "remove_from_clients: [x-request-id, x-user-check]\n"))
	cases := []struct {
		name   string
		out    string
		want   [][]string // blocks of whole lines, each after the one before it
		absent []string   // whole lines out never holds
		check  func() string
	}{
		{name: "deny, two check addresses", out: checksOut, want: [][]string{
			{"# Needs the njs module, 1.0.2 or later.", "load_module modules/ngx_http_js_module.so;", "load_module modules/ngx_stream_js_module.so;",
				"worker_processes auto;", "worker_rlimit_nofile 8192;", "error_log stderr emerg;", "events {", "  worker_connections 8192;", "}"},
			{
				"  absolute_redirect off;",
				"  # The folder of the auth check's socket: nginx makes a temporary path at start, before it binds, for its workers' user alone.",
				"  scgi_temp_path /run/proxyfile-10000;",
				"  js_var $pf_checking;",
				"  js_var $pf_check_failed;",
				"  js_var $pf_unchecked;",
				"  js_var $pf_authz_us;",
				"  js_var $pf_then;",
				"  js_var $pf_action;",
				"  js_var $pf_reports;",
				"  js_var $pf_accept_0;",
				`  map $pf_accept_0 $pf_accept_0_grpc { " " ""; default $pf_accept_0; }`,
				`  map "" $pf_header_size { default 61440; }`,
				`  map "" $pf_counted_address { default ""; }`,
				`  map $request_uri $pf_query { "~^[^?]*(?<pf_query_text>\\?.*)" $pf_query_text; default ""; }`,
				"  map $pf_mode $pf_told_uri { uri $pf_path$pf_query; default $request_uri; }",
				`  map $request $pf_told_proto { "~*^[^ ]+ +https://" https; "~*^[^ ]+ +http://" http; default $pf_forwarded_proto; }`,
			},
			{`    "~^1" dotparam;`},
			{
				"    set $pf_when_unavailable deny;",
				`    set $pf_only_with "x-api-key";`,
				`    set $pf_accept "x-user-check";`,
				"    set $pf_check_ms 1500;",
			},
			slices.Concat(checkStart(`"/auth/check"`), []string{
				"      proxy_set_header X-Request-Id $pf_request_id;",
				"      proxy_set_header Origin $pf_origin;",
			}, removedByEnvoyBeforeTheCheck, []string{
				`      proxy_set_header "x-user-check" "";`,
				`      proxy_set_header x-envoy-auth-failure-mode-allowed "";`,
			}, checkEnd),
			{"    location @pf_r_dotparam {", "      set $pf_then refused;", `      default_type "";`, "      js_access pf.check;", "    }"},
			{
				"stream {",
				"  js_engine njs;",
				"  js_import pf from proxyfile.js;",
				"  upstream proxyfile_10000_check {",
				"    server 10.0.3.31:7070 max_fails=0;",
				"    server 10.0.3.32:7070 max_fails=0;",
				"  }",
				"  server {",
				"    listen unix:/run/proxyfile-10000/check.sock backlog=2147483647;",
				`    set $pf_check_send "authorization x-gateway-timeout-ms x-envoy-internal x-envoy-expected-rq-timeout-ms x-gateway-action x-gateway-reports x-forwarded-uri x-forwarded-method x-forwarded-proto x-forwarded-host";`,
				"    set $pf_check_ms 1500;",
				"    set $pf_check_uri x-forwarded-uri;",
				"    proxy_pass proxyfile_10000_check;",
				"    proxy_next_upstream off;",
				"    proxy_connect_timeout 1500ms;",
				"    proxy_timeout 1500ms;",
				"    js_filter pf.relay;",
				"  }",
				"}",
			},
		}, absent: []string{`    "~^1" refused;`, "      types {}"}},
		{name: "deny, the request log", out: checksOut, want: [][]string{
			{requestLog("pf_request", `"$pf_authz_us"`), requestLog("pf_request_not_asked", "null")},
			{
				`  map $pf_authz_us $pf_log_asked { "" 0; default 1; }`,
				`  map $pf_authz_us $pf_log_not_asked { "" 1; default 0; }`,
				"  access_log /dev/stdout pf_request if=$pf_log_asked;",
				"  access_log /dev/stdout pf_request_not_asked if=$pf_log_not_asked;",
			},
			authUnavailableLog,
		}, absent: []string{"  access_log /dev/stdout pf_request;"}},
		{name: "allow", out: authOut, want: [][]string{
			{"    set $pf_when_unavailable allow;", `    set $pf_only_with "authorization";`, `    set $pf_accept "x-user-id x-tier-level x-user-check";`, "    set $pf_check_ms 1500;"},
			slices.Concat(checkStart(`"/auth/check"`), []string{
				"      proxy_set_header X-Request-Id $pf_request_id;",
				"      proxy_set_header Origin $pf_origin;",
				"      proxy_set_header X-Forwarded-For $pf_forwarded_for;",
				"      proxy_set_header x-envoy-external-address $remote_addr;",
			}, removedByEnvoyBeforeTheCheck, []string{
				`      proxy_set_header x-envoy-decorator-operation "";`,
				`      proxy_set_header x-envoy-downstream-service-cluster "";`,
				`      proxy_set_header x-envoy-downstream-service-node "";`,
				`      proxy_set_header x-envoy-original-host "";`,
				`      proxy_set_header x-envoy-original-path "";`,
				`      proxy_set_header "x-client-id" "";`,
				`      proxy_set_header "x-user-id" "";`,
				`      proxy_set_header "x-tier-level" "";`,
				`      proxy_set_header "x-user-check" "";`,
				`      proxy_set_header x-envoy-auth-failure-mode-allowed "";`,
			}, checkEnd),
			{
				"    location @pf_r_dotparam {",
				"      add_header x-request-id $pf_answer_id always;",
				"      add_header Vary Origin always;",
				"      set $pf_then refused;",
				`      default_type "";`,
				"      js_access pf.check;",
				"    }",
			},
			{
				"    location @pf_r_8 {",
				`      set $pf_log_route "braces";`,
				`      set $pf_log_upstream "accounts_http";`,
				`      default_type "";`,
				"      js_access pf.check;",
			},
			{
				`      proxy_set_header x-forwarded-client-cert "";`,
				`      proxy_set_header x-envoy-decorator-operation "";`,
				`      proxy_set_header x-envoy-downstream-service-cluster "";`,
				`      proxy_set_header x-envoy-downstream-service-node "";`,
				`      proxy_set_header x-envoy-original-host "";`,
				`      proxy_set_header x-envoy-original-path "";`,
				`      proxy_set_header "x-user-id" $pf_accept_0;`,
				`      proxy_set_header "x-tier-level" $pf_accept_1;`,
				`      proxy_set_header "x-user-check" $pf_accept_2;`,
				"      proxy_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;",
				`      proxy_set_header "x-client-id" "";`,
				`      proxy_set_header x-gateway-action "";`,
				`      proxy_set_header x-gateway-reports "";`,
				"      proxy_pass http://accounts_http;",
			},
			{`    set $pf_check_send "authorization x-country x-client-* http2-settings x-gateway-timeout-ms x-envoy-internal x-envoy-expected-rq-timeout-ms x-gateway-action x-gateway-reports x-forwarded-uri x-forwarded-method x-forwarded-proto x-forwarded-host";`},
		}, absent: []string{"      types {}"}},
		{name: "allow, an RPC method's question", out: authOut, want: [][]string{{
			"    location @pf_r_3 {",
			`      set $pf_log_route "shop";`,
			`      set $pf_log_upstream "shop_grpc";`,
			`      set $pf_action "buy";`,
			`      set $pf_reports "browse";`,
			`      default_type "";`,
			"      js_access pf.check;",
		}, {
			`      grpc_set_header "x-user-id" $pf_accept_0_grpc;`,
			`      grpc_set_header "x-tier-level" $pf_accept_1_grpc;`,
			`      grpc_set_header "x-user-check" $pf_accept_2_grpc;`,
			"      grpc_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;",
			`      grpc_set_header "x-client-id" "";`,
			`      grpc_set_header x-gateway-action "";`,
			`      grpc_set_header x-gateway-reports "";`,
			"      grpc_pass grpc://shop_grpc;",
		}}},
		{name: "allow, the request log sampled", out: authOut, want: [][]string{
			{requestLog("pf_request", `"$pf_authz_us"`), requestLog("pf_request_not_asked", "null")},
			{
				`  map "$pf_log:$pf_authz_us" $pf_log_asked { "~^1:." 1; default 0; }`,
				`  map "$pf_log:$pf_authz_us" $pf_log_not_asked { "1:" 1; default 0; }`,
				"  access_log /dev/stdout pf_request if=$pf_log_asked;",
				"  access_log /dev/stdout pf_request_not_asked if=$pf_log_not_asked;",
			},
			authUnavailableLog,
		}, absent: []string{"  access_log /dev/stdout pf_request if=$pf_log;"}},
		{name: "switched off", out: off, want: [][]string{
			{"worker_processes auto;", "error_log stderr emerg;", "events {", "  worker_connections 1024;", "}"},
			{requestLog("pf_request", "null")},
			{"  access_log /dev/stdout pf_request if=$pf_log;"},
			{`    "~^1" refused;`},
		}, absent: []string{"load_module modules/ngx_stream_js_module.so;", "    location = /.proxyfile/check {", "stream {",
			"      js_access pf.check;", "    location @pf_r_dotparam {", authUnavailableLog[1], "  absolute_redirect off;"},
			check: func() string {
				// 82 places of a route in each of four environments, each with a second location but the two with a
				// rewrite, and the retried route's two copies that try once and its four that take a next try:
				// (82 * 2 - 2 + 6) * 4 = 672.
				if broken, count := everyLocationRemoves(off); broken != "" || count != 672 {
					return fmt.Sprintf("%d route locations, want 672; %s", count, broken)
				}
				return ""
			}},
		{name: "auth off", out: authOut, want: [][]string{
			{
				"    location @pf_r_6 {",
				`      set $pf_log_route "public";`,
				`      set $pf_log_upstream "shop_http";`,
				"      proxy_set_header Host $pf_authority;",
			},
			slices.Concat([]string{`      proxy_set_header x-envoy-original-path "";`}, removalLines("proxy_set_header"),
				[]string{"      proxy_pass http://shop_http;"}),
			{
				"    location @pf_r_6-uri {",
				`      set $pf_log_route "public";`,
				`      set $pf_log_upstream "shop_http";`,
				"      rewrite ^ $uri break;",
				"      proxy_set_header Host $pf_authority;",
			},
		}},
		{name: "an escaped ask path", out: escapedPath, want: [][]string{
			{`  geo $pf_dollar { default "$"; }`},
			{`      proxy_set_header x-proxyfile-path "/a${pf_dollar}b\"c";`},
			// Without send_request no header carries the path the proxy tells the check.
			{"    set $pf_check_ms 1500;", `    set $pf_check_uri "";`},
		}},
		{name: "an ask path outside ASCII", out: unicodePath, want: [][]string{
			{`      proxy_set_header x-proxyfile-path "/auth/é/ü?x=ß";`},
		}},
		{name: "an escaped question", out: escapedQuestion, want: [][]string{
			{`  geo $pf_dollar { default "$"; }`},
			{`      set $pf_action "read \"q\" ${pf_dollar}x";`, `      default_type "";`, "      js_access pf.check;"},
		}},
		{name: "the check path routes a client", out: checksOut, want: [][]string{
			{"    location = /.proxyfile/check {", `      if ($pf_checking = "") {`, "        return 418;", "      }", "      error_page 418 = @pf_r_$pf_route$pf_rt_once;"},
		}},
		{name: "the dot segment is asked", out: authOut, want: [][]string{
			{`    "~^1" dotparam;`, `    "~^01" refused;`},
			{"    location @pf_r_refused {", "      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;",
				"      keepalive_timeout 0;", `      default_type "";`, `      return 400 "$pf_empty";`, "    }", "    location @pf_r_dotparam {"},
		}, absent: []string{`    "~^1" refused;`}},
		{name: "keep-alive is never set", out: keepAlive, want: [][]string{
			{`    set $pf_accept "x-user-id";`},
			{`      proxy_set_header "x-user-id" $pf_accept_0;`, "      proxy_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;",
				`      proxy_set_header x-gateway-action "";`, `      proxy_set_header x-gateway-reports "";`, `      proxy_set_header "keep-alive" "";`},
		}, absent: []string{"  js_var $pf_accept_1;", `      proxy_set_header "keep-alive" $pf_accept_1;`}},
		{name: "a name both lists hold gets the accept's line alone", out: both, want: [][]string{
			{`      proxy_set_header "x-user-check" $pf_accept_0;`, "      proxy_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;",
				`      proxy_set_header x-gateway-action "";`, `      proxy_set_header x-gateway-reports "";`, "      proxy_pass http://shop_http;"},
		}, check: func() string {
			if n := strings.Count(both, `      proxy_set_header "x-user-check" "";`); n != 3 {
				return fmt.Sprintf("%d lines empty x-user-check, want 3, the check location's in each environment server", n)
			}
			return ""
		}},
		{name: "the names nginx writes itself", out: authOut, check: func() string {
			// The names a route location writes from the generator's own lists, not quoted as a name from the file is,
			// with the two nginx frames a body by.
			names := []string{"content-length", "transfer-encoding"}
			written := regexp.MustCompile(`^      (?:proxy|grpc)_set_header ([^" ]+) `)
			found, _ := routeLocations(authOut)
			for _, location := range found {
				for _, line := range location {
					if m := written.FindStringSubmatch(line); m != nil && !slices.Contains(names, strings.ToLower(m[1])) {
						names = append(names, strings.ToLower(m[1]))
					}
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, format.NginxNotAccepted) {
				return fmt.Sprintf("the route locations write %q, want format.NginxNotAccepted, %q", names, format.NginxNotAccepted)
			}
			return ""
		}},
		// The names only_with_header may not name are the ones the route locations empty from the generator's own lists.
		{name: "the names nginx removes from clients itself", out: authOut, check: func() string {
			both := []string{generator.UncheckedMark, generator.QuestionHeaders[0].Name, generator.QuestionHeaders[1].Name}
			both = append(both, generator.RemovedByEnvoy...)
			slices.Sort(both)
			facing := slices.Sorted(slices.Values(generator.RemovedByEnvoyFacingClients))
			if !slices.Equal(both, format.NginxRemovedFromClients) || !slices.Equal(facing, format.NginxRemovedFacingClients) {
				return fmt.Sprintf("format's lists are %q and %q, want %q and %q", format.NginxRemovedFromClients,
					format.NginxRemovedFacingClients, both, facing)
			}
			return ""
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.want != nil {
				if missing := testsupport.FirstMissingBlock(c.out, c.want); missing != "" {
					t.Errorf("the configuration holds no block, or not in this order:\n%s", missing)
				}
			}
			for _, line := range c.absent {
				if hasLine(c.out, line) {
					t.Errorf("the configuration holds the line %q", line)
				}
			}
			if c.check != nil {
				if broken := c.check(); broken != "" {
					t.Error(broken)
				}
			}
		})
	}
}

// TestCheckedRouteLocations: no request reaches a service unless the check let it through, so on a file whose every
// route is checked no route location goes without the handler, and on one with auth: off exactly those routes'
// locations do. A copy of a retried route's location that takes a next try (@pf_r_<n>-rt or -rt<k>, and their -uri
// forms) holds no handler, so that a retried request is asked once: it counts as checked when its route location holds
// the handler and no line leads to it but the jump of a checked route location and of its copies, whose target names
// the request's own route. The locations are found by blocks, never listed by hand, and on a file with no auth the
// count of locations without the handler is every route location's, so the count is seen to find what it counts.
func TestCheckedRouteLocations(t *testing.T) {
	copyOf := regexp.MustCompile(`^@pf_r_(.+)-rt[0-9]*$`)
	// The value after each quoted key of a map on one line, and after its default.
	mapValue := regexp.MustCompile(`(?:"(?:[^"\\]|\\.)*"|default) ([^ ;]+);`)
	jump := regexp.MustCompile(`^ +(?:error_page [0-9 ]+= |try_files .* )(@[^ ;]+);$`)
	name := func(location []string) string {
		return strings.TrimSuffix(strings.TrimPrefix(location[0], "    location "), " {")
	}
	unchecked := func(out string) []string {
		// The check location's jump goes to the route location or to its copy that tries once, never to one that takes
		// a next try: every value of the jump map is one of those two suffixes.
		maps := 0
		for _, line := range lines(out) {
			if !strings.HasPrefix(line, "  map ") || !strings.Contains(line, " $pf_rt_once { ") {
				continue
			}
			maps++
			for _, value := range mapValue.FindAllStringSubmatch(line, -1) {
				if value[1] != `""` && value[1] != "-once" {
					return []string{fmt.Sprintf("the jump map gives %q, a suffix that is neither \"\" nor -once: %q", value[1], line)}
				}
			}
		}
		if maps != 1 {
			return []string{fmt.Sprintf("%d jump maps $pf_rt_once, want 1", maps)}
		}
		names := []string{}
		for _, server := range environmentServers(out) {
			checked := map[string]bool{}
			for _, location := range blocks(server, "    location @pf_r_") {
				checked[name(location)] = slices.Contains(location, "      js_access pf.check;")
			}
			// Every line of the server that can jump to a copy: the retry's jump, whose target is a copy of the
			// request's own route, in a route location or its copy, and nothing else. A copy of an unchecked route
			// location is counted below as unchecked itself.
			for _, location := range blocks(server, "    location ") {
				for _, line := range location {
					m := jump.FindStringSubmatch(line)
					if m == nil {
						continue
					}
					target, base := m[1], name(location)
					if c := copyOf.FindStringSubmatch(base); c != nil {
						base = "@pf_r_" + c[1]
					}
					_, routeLocation := checked[base]
					switch {
					case target == "@pf_$pf_rt_target" && routeLocation:
					case target == "@pf_$pf_rt_target":
						names = append(names, name(location)+" jumps to a copy and is no route location")
					case target == "@pf_r_$pf_route$pf_rt_once":
					case copyOf.MatchString(target):
						names = append(names, name(location)+" jumps to the copy "+target)
					case strings.Contains(target, "$") && strings.HasPrefix("@pf_r_", strings.Split(target, "$")[0]),
						strings.Contains(target, "$") && strings.HasPrefix(strings.Split(target, "$")[0], "@pf_r_"):
						names = append(names, name(location)+" jumps to "+target+", which may name a copy")
					}
				}
			}
			for _, location := range blocks(server, "    location @pf_r_") {
				if !slices.ContainsFunc(location, passes) || checked[name(location)] {
					continue
				}
				if c := copyOf.FindStringSubmatch(name(location)); c != nil && checked["@pf_r_"+c[1]] {
					continue
				}
				names = append(names, name(location))
			}
		}
		return names
	}
	if got := unchecked(generate(t, string(testsupport.ReadFile(t, checksFile)))); !slices.Equal(got, []string{}) {
		t.Errorf("nginx-checks.yaml: route locations with no handler %q, want none", got)
	}
	perServer := []string{"@pf_r_1", "@pf_r_1-uri", "@pf_r_5", "@pf_r_6", "@pf_r_6-uri"}
	if got := unchecked(generate(t, string(testsupport.ReadFile(t, authFile)))); !slices.Equal(got, slices.Concat(perServer, perServer, perServer, perServer)) {
		t.Errorf("nginx-auth.yaml: route locations with no handler %q, want %q in each of the four environment servers", got, perServer)
	}
	// nginx.yaml writes no auth: its 83 places of a route in each of four environments, each with a second location but
	// the two with a rewrite, a copy that tries once of each of the six locations of its three RPC methods, which it
	// retries, and for each of its two retried routes the two copies that try once and the four that take a next try:
	// (83 * 2 - 2 + 6 + 2 * 6) * 4 = 728.
	if got := len(unchecked(generate(t, string(testsupport.ReadFile(t, batteryFile))))); got != 728 {
		t.Errorf("nginx.yaml: %d route locations with no handler, want 728, every one", got)
	}
}
