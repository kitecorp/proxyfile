package nginx_test

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/nginx"
	"github.com/kitecorp/proxyfile/v2/internal/read"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// batteryFile is the proxyfile the conformance battery runs: examples/full's world with only the keys this generator
// writes, so its output holds every block the generator writes.
const batteryFile = "../../conformance/testdata/nginx.yaml"

// generate returns the text of the configuration nginx.Generate writes for the proxyfile text, read as render reads a
// proxyfile that writes its origins and its RPC methods. A problem or an error ends the test.
func generate(t *testing.T, proxyfile string) string {
	t.Helper()
	config, _ := generateFiles(t, proxyfile)
	return config
}

// generateFiles returns the two files nginx.Generate writes for the proxyfile text: the configuration, named "", and the
// script it loads, named proxyfile.js. A problem, an error or other files end the test.
func generateFiles(t *testing.T, proxyfile string) (config, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxyfile.yaml")
	testsupport.WriteFile(t, path, proxyfile)
	file, problems := read.Proxyfile(path)
	if problems != nil {
		t.Fatalf("read.Proxyfile: %q", testsupport.ProblemStrings(problems))
	}
	var routes []rpc.Route
	if file.MethodsKey != nil {
		if routes, problems = rpc.Resolve(file.Gateway, file.Methods); problems != nil {
			t.Fatalf("rpc.Resolve: %q", testsupport.ProblemStrings(problems))
		}
	}
	files, err := nginx.Generate(generator.Input{Gateway: file.Gateway, Origins: file.Origins, RPC: routes})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"", "proxyfile.js"}) {
		t.Fatalf("Generate returned files named %q, want the configuration, named \"\", and proxyfile.js", names)
	}
	return string(files[0].Bytes), string(files[1].Bytes)
}

// smallFile is a proxyfile with one environment and one service, app on 127.0.0.1 port 3000 over HTTP, whose routes
// are the one route given.
func smallFile(route string) string {
	return "listen: 8000\nservices:\n  app: {http: 3000}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n  - " + route + "\n"
}

// lines returns the lines of a generated file, without their newlines.
func lines(out string) []string {
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// blocks returns each block of lines that opens with a line starting with open and closes at the first later line that
// is a closing brace at open's indentation.
func blocks(all []string, open string) [][]string {
	indent := open[:len(open)-len(strings.TrimLeft(open, " "))]
	var found [][]string
	for i := 0; i < len(all); i++ {
		if !strings.HasPrefix(all[i], open) {
			continue
		}
		end := i + 1
		for end < len(all) && all[end] != indent+"}" {
			end++
		}
		found = append(found, all[i:min(end+1, len(all))])
		i = end
	}
	return found
}

// environmentServers returns the servers that answer an environment's hosts, the ones with a server_name.
func environmentServers(out string) [][]string {
	var found [][]string
	for _, server := range blocks(lines(out), "  server {") {
		if slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    server_name ") }) {
			found = append(found, server)
		}
	}
	return found
}

// hasLine reports whether out holds line as a whole line.
func hasLine(out, line string) bool {
	return slices.Contains(lines(out), line)
}

// TestBatteryFile: the battery's proxyfile gives the file written by hand in testdata, and a second run the same bytes,
// so nothing in the output depends on the order of a Go map or on the run.
func TestBatteryFile(t *testing.T) {
	proxyfile := string(testsupport.ReadFile(t, batteryFile))
	want := testsupport.ReadFile(t, "testdata/nginx-file.conf")
	for run := 1; run <= 2; run++ {
		if got := generate(t, proxyfile); got != string(want) {
			line, inWant, inGot := testsupport.FirstDifferentLine(want, []byte(got))
			t.Errorf("run %d: the output differs from testdata/nginx-file.conf first on line %d:\n  want: %s\n  got:  %s",
				run, line, inWant, inGot)
		}
	}
}

// TestSecurityLines holds a row for each line a security reviewer must read in the output: each row fails when the
// line it reads is removed from the generator or changed.
func TestSecurityLines(t *testing.T) {
	out := generate(t, string(testsupport.ReadFile(t, batteryFile)))
	firstServer := strings.Index(out, "\n  server {\n")
	cases := []struct {
		name  string
		check func() string // "" when the output keeps the rule, else what breaks it
	}{
		{"the X-Accel lines", func() string {
			for _, line := range []string{
				"  proxy_ignore_headers X-Accel-Redirect X-Accel-Expires X-Accel-Limit-Rate X-Accel-Buffering X-Accel-Charset;",
				"  grpc_ignore_headers X-Accel-Redirect X-Accel-Expires X-Accel-Limit-Rate X-Accel-Buffering X-Accel-Charset;",
			} {
				if at := strings.Index(out, "\n"+line+"\n"); at < 0 || at > firstServer {
					return fmt.Sprintf("no line %q before the first server", line)
				}
			}
			return ""
		}},
		{"no redirect to a URI", func() string {
			redirect := regexp.MustCompile(`(?m)^\s*(rewrite\s.*\s(last|redirect|permanent);|try_files\s|index\s|error_page\s[0-9 ]+(=[0-9]*)?\s*/)`)
			if found := redirect.FindAllString(out, -1); found != nil {
				return fmt.Sprintf("lines that send the request to a URI: %q", found)
			}
			return ""
		}},
		{"the dot-parameter flag", func() string {
			return missing(out, `  map $uri $pf_f_dotparam { "~/\\.\\.?;" 1; default 0; }`)
		}},
		{"the backslash flag", func() string {
			return missing(out, `  map $uri $pf_f_backslash { "~\\\\" 1; default 0; }`)
		}},
		{"the control-character flag", func() string {
			return missing(out, `  map $uri $pf_f_control { "~[\\x00-\\x1F\\x7F]" 1; default 0; }`)
		}},
		{"the flags come first in the route choice", func() string {
			// A preflight is answered only when a route takes its path, as Envoy answers one, so its key asks for a
			// route's number after its own; a path the classes refuse, and a route whose run PCRE2 gave up on, are
			// refused before any route is named. A route with a rewrite (its number with w) is refused for a path that
			// keeps an escape; a route without one goes to the location that sends the path nginx has normalised when
			// the path needs it (-uri), and otherwise to the one that sends the path as the client wrote it.
			want := []string{
				`  map "$pf_f_dotparam$pf_f_backslash$pf_f_control$pf_f_upgrade$pf_f_preflight:$pf_mode:$pf_g1" $pf_route {`,
				`    "~^1" refused;`, `    "~^01" refused;`, `    "~^001" refused;`, `    "~^0001" upgrade-refused;`,
				`    "~^0000[01]:refused:" refused;`, `    "~^0000[01]:[a-z]+:failed\\z" refused;`,
				`    "~^00001:[a-z]+:[1-9]" preflight;`, `    "~^00000:rawk:[0-9]+w\\z" refused;`,
				`    "~^00000:uri:(?<pf_n>[1-9][0-9]*)\\z" $pf_n-uri;`, `    "~^00000:[a-z]+:(?<pf_n>[1-9][0-9]*)w?\\z" $pf_n;`,
				"    default no-route;", "  }",
			}
			all := lines(out)
			at := slices.Index(all, want[0])
			if at < 0 || at+len(want) > len(all) {
				return fmt.Sprintf("no line %q", want[0])
			}
			if got := all[at : at+len(want)]; !slices.Equal(got, want) {
				return fmt.Sprintf("the route choice is %q, want %q", got, want)
			}
			return ""
		}},
		{"a pattern", func() string {
			return missing(out, `  map $pf_path $pf_m1 { "~\\A(?:\\A\\/events\\/[\\-0-9a-z]+\\z)\\z" 1; "~^" 0; default failed; }`)
		}},
		{"the path is read as the client wrote it, and its classes choose what a route matches", func() string {
			// The file has pattern routes, so a byte Envoy escapes before it matches is refused with the rest.
			for _, line := range []string{
				`  map $request_uri $pf_rawpath { "~^(?<pf_p>/[^?]*)" $pf_p; "~^\\z" /; default ""; }`,
				`  map $pf_rawpath $pf_c_n { "~//|/\\.\\.?(?:/|\\z)|%(?:2[DEFdef]|3[0-9]|4[1-9A-Fa-f]|5[0-9ACFacf]|6[1-9A-Fa-f]|7[0-9AEae])" 1; "~^/" 0; default e; }`,
				`  map $pf_rawpath $pf_c_k { "~%(?!2[DEFdef]|3[0-9]|4[1-9A-Fa-f]|5[0-9ACFacf]|6[1-9A-Fa-f]|7[0-9AEae])[0-9A-Fa-f]{2}" 1; "~^/" 0; default e; }`,
				`  map $pf_rawpath $pf_c_e { "~[\"<>^` + "`" + `{|}\\x80-\\xFF]" 1; "~^/" 0; default e; }`,
				`  map $request_uri $pf_c_q { "~^[^?]*\\?\\z" 1; "~^" 0; default e; }`,
				`  map "$pf_c_n$pf_c_k$pf_c_e$pf_c_q" $pf_mode { 0000 raw; 0001 rawk; 0100 rawk; 0101 rawk; 1000 uri; default refused; }`,
				`  map $pf_mode $pf_path { raw $pf_rawpath; rawk $pf_rawpath; uri $uri; default ""; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			return ""
		}},
		{"a route without a rewrite sends the path as written, or as nginx has normalised it", func() string {
			// catalog, route 10, has no rewrite; accounts-webhooks, route 5, has one, which reads $uri in both modes.
			for _, server := range environmentServers(out) {
				for _, want := range [][2]string{
					{"    location @pf_r_10 {", "      proxy_set_header Host $pf_authority;"},
					{"    location @pf_r_10-uri {", "      rewrite ^ $uri break;"},
					{"    location @pf_r_5 {", `      rewrite "^/api/webhooks((?s).*)\\z" "/hooks/payments$1" break;`},
				} {
					at := slices.Index(server, want[0])
					if at < 0 || at+1 >= len(server) || server[at+1] != want[1] {
						return fmt.Sprintf("an environment server holds no %q followed by %q", want[0], want[1])
					}
				}
				if slices.Contains(server, "    location @pf_r_5-uri {") {
					return "an environment server holds a second location for the route with a rewrite"
				}
			}
			return ""
		}},
		{"a # in the target is refused before the host is read", func() string {
			return missing(out, `  map $request_uri $pf_f_fragment { "~#" 1; default 0; }`)
		}},
		{"the njs module is loaded first and named on line 2", func() string {
			all := lines(out)
			if want := []string{"# Needs the njs module, 1.0.2 or later.", "load_module modules/ngx_http_js_module.so;"}; len(all) < 3 ||
				!slices.Equal(all[1:3], want) {
				return fmt.Sprintf("lines 2 and 3 are %q, want %q", all[1:min(3, len(all))], want)
			}
			return ""
		}},
		{"a header name outside the token characters is refused before the host is read", func() string {
			// Only "0" passes, so the empty value of a script that failed is refused too.
			for _, line := range []string{
				"  js_engine njs;",
				"  js_import pf from proxyfile.js;",
				"  js_set $pf_js_names pf.headerNames;",
				`  map $pf_js_names $pf_f_names { "0" 0; default 1; }`,
			} {
				if at := strings.Index(out, "\n"+line+"\n"); at < 0 || at > firstServer {
					return fmt.Sprintf("no line %q before the first server", line)
				}
			}
			return ""
		}},
		{"the first route that is not 0 wins, and a failed run is refused", func() string {
			for _, line := range []string{
				`  map "$pf_p5:$request_method" $pf_m5 { "~^1:POST\\z" 5w; "~^e:" failed; default 0; }`,
				`  map "$pf_m1:$pf_m2:$pf_m3:$pf_m4:$pf_m5:$pf_m6:$pf_m7:$pf_m8:$pf_m9:$pf_m10:$pf_m11:$pf_m12:$pf_m13:$pf_m14:$pf_m15:$pf_m16:" $pf_g1 ` +
					`{ "~^(?:0:)*(?<pf_first>[^0:][^:]*):" $pf_first; "~^(?:0:)*\\z" 0; default failed; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			return ""
		}},
		{"nginx's own log keeps no request line", func() string {
			return missing(out, "error_log stderr emerg;")
		}},
		{"route maps are case-sensitive regular expressions", func() string {
			// The flags are left to their own rows, so removing one fails that row alone.
			routeMap := regexp.MustCompile(`^  map (\$pf_path \$pf_m([0-9]+) \{ "~[^*].* ([0-9]+)w?; "~\^" 0; default failed; \}|` +
				`\$pf_path \$pf_p[0-9]+ \{ "~[^*].* 1; "~\^" 0; default e; \}|` +
				`"\$pf_p([0-9]+):\$request_method" \$pf_m([0-9]+) \{ "~\^1:[A-Z]+\\\\z" ([0-9]+)w?; "~\^e:" failed; default 0; \})$`)
			count := 0
			for _, line := range lines(out) {
				if !strings.HasPrefix(line, "  map $pf_path $pf_m") && !strings.HasPrefix(line, "  map $pf_path $pf_p") &&
					!strings.HasPrefix(line, `  map "$pf_p`) {
					continue
				}
				count++
				m := routeMap.FindStringSubmatch(line)
				if m == nil {
					return fmt.Sprintf("a route map that is not one case-sensitive regular expression and the failed run: %q", line)
				}
				// A route's map is its own number when the route takes the request.
				if m[2] != m[3] || m[4] != m[5] || m[5] != m[6] {
					return fmt.Sprintf("a route map that names another route: %q", line)
				}
			}
			// Sixteen routes, one of them with a method, which maps its path and then its method.
			if count != 17 {
				return fmt.Sprintf("%d route maps, want 17", count)
			}
			return ""
		}},
		{"an environment server holds only location / and named ones", func() string {
			servers := environmentServers(out)
			if len(servers) != 3 {
				return fmt.Sprintf("%d environment servers, want 3", len(servers))
			}
			for _, server := range servers {
				for _, line := range server {
					if strings.HasPrefix(strings.TrimLeft(line, " "), "location ") && line != "    location / {" &&
						!strings.HasPrefix(line, "    location @pf_") {
						return fmt.Sprintf("an environment server holds %q", line)
					}
				}
			}
			return ""
		}},
		{"a refused service is answered 502 with no body", func() string {
			servers := environmentServers(out)
			routeLocations := 0
			for _, server := range servers {
				for _, location := range blocks(server, "    location @pf_r_") {
					if !regexp.MustCompile(`^    location @pf_r_[0-9]+(-uri)? \{$`).MatchString(location[0]) {
						continue
					}
					routeLocations++
					if !slices.Contains(location, "      error_page 502 = @pf_502;") {
						return fmt.Sprintf("%q holds no error_page 502 = @pf_502", location[0])
					}
				}
				at := slices.Index(server, "    location @pf_502 {")
				if at < 0 || at+1 >= len(server) || server[at+1] != `      return 502 "$pf_empty";` {
					name := server[slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    server_name ") })]
					return fmt.Sprintf("the server of %q holds no location @pf_502 answering 502 with no body", name)
				}
			}
			// Sixteen routes in each of three environments, each with a second location but the two with a rewrite.
			if routeLocations != 90 {
				return fmt.Sprintf("%d route locations, want 90", routeLocations)
			}
			return ""
		}},
		{"the hosts each environment admits a browser origin on", func() string {
			for _, line := range []string{
				`  map $http_origin $pf_o1 { "" 0; "~^https?://api\\.example\\.com(:[0-9]+)?\\z" 0; "~^https?://api\\.example\\.org(:[0-9]+)?\\z" 0; default 1; }`,
				`  map $http_origin $pf_o2 { "" 0; "~^https?://api\\.test\\.example\\.com(:[0-9]+)?\\z" 0; default 1; }`,
				`  map $http_origin $pf_o3 { "" 0; "~^https?://127\\.0\\.0\\.1(:[0-9]+)?\\z" 0; "~^https?://localhost(:[0-9]+)?\\z" 0; default 1; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			return ""
		}},
		{"a foreign origin is refused before any location", func() string {
			servers := environmentServers(out)
			if len(servers) != 3 {
				return fmt.Sprintf("%d environment servers, want 3", len(servers))
			}
			for i, server := range servers {
				check := []string{fmt.Sprintf("    if ($pf_o%d) {", i+1), `      return 403 "Invalid CORS request";`, "    }"}
				at := slices.Index(server, check[0])
				firstLocation := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    location ") })
				if at < 0 || at+3 > len(server) || !slices.Equal(server[at:at+3], check) || at > firstLocation {
					return fmt.Sprintf("environment server %d holds no %q before its first location", i+1, check)
				}
			}
			return ""
		}},
		{"the preflight from an environment's own host is answered", func() string {
			for _, line := range []string{
				`  map $http_origin $pf_has_origin { "" 0; default 1; }`,
				`  map "$request_method:$pf_has_origin:$http_access_control_request_method" $pf_f_preflight { "~^OPTIONS:1:." 1; default 0; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			for _, server := range environmentServers(out) {
				at := slices.Index(server, "    location @pf_r_preflight {")
				if at < 0 || at+1 >= len(server) || server[at+1] != `      return 200 "$pf_empty";` {
					return "an environment server holds no location @pf_r_preflight answering 200 with no body"
				}
			}
			return ""
		}},
		{"the headers Envoy removes are set empty in every route location", func() string {
			removed := []string{"proxy-connection", "x-envoy-expected-rq-timeout-ms", "x-envoy-force-trace",
				"x-envoy-hedge-on-per-try-timeout", "x-envoy-internal", "x-envoy-ip-tags", "x-envoy-max-retries",
				"x-envoy-original-url", "x-envoy-retriable-header-names", "x-envoy-retriable-status-codes",
				"x-envoy-retry-grpc-on", "x-envoy-retry-on", "x-envoy-upstream-alt-stat-name",
				"x-envoy-upstream-rq-per-try-timeout-ms", "x-envoy-upstream-rq-timeout-alt-response",
				"x-envoy-upstream-rq-timeout-ms", "x-envoy-upstream-stream-duration-ms", "x-forwarded-client-cert"}
			kinds := map[string]int{}
			for _, server := range environmentServers(out) {
				for _, location := range blocks(server, "    location @pf_r_") {
					set := ""
					switch {
					case slices.ContainsFunc(location, func(line string) bool { return strings.HasPrefix(line, "      proxy_pass ") }):
						set = "proxy_set_header"
					case slices.ContainsFunc(location, func(line string) bool { return strings.HasPrefix(line, "      grpc_pass ") }):
						set = "grpc_set_header"
					default:
						continue
					}
					kinds[set]++
					for _, name := range removed {
						if line := "      " + set + " " + name + ` "";`; !slices.Contains(location, line) {
							return fmt.Sprintf("%q holds no %q", location[0], line)
						}
					}
				}
			}
			// Thirteen HTTP routes and three RPC methods in each of three environments, each with a second location but
			// the two HTTP routes with a rewrite.
			if kinds["proxy_set_header"] != 72 || kinds["grpc_set_header"] != 18 {
				return fmt.Sprintf("%d HTTP and %d gRPC route locations, want 72 and 18", kinds["proxy_set_header"], kinds["grpc_set_header"])
			}
			return ""
		}},
		{"the service receives the authority nginx picked the server by as Host", func() string {
			// The origin form after one or more spaces names the Host header, the absolute form the host it writes
			// before its path, and any other line nothing, which the request flags refuse.
			for _, line := range []string{
				`  map $request $pf_authority { "~^[^ ]+ +/" $http_host; "~^[^ ]+ +[A-Za-z][A-Za-z0-9+.-]*://(?<pf_authority_host>[^/?# ]*[^/?#: ])(?=[/ ])" $pf_authority_host; default ""; }`,
				`  map "$pf_f_http10:$pf_authority" $pf_f_authority { "~^1:" 0; "~^0:." 0; default 1; }`,
				"      proxy_set_header Host $pf_authority;",
				"      grpc_set_header Host $pf_authority;",
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			for _, server := range environmentServers(out) {
				for _, location := range blocks(server, "    location ") {
					if slices.ContainsFunc(location, func(line string) bool { return strings.Contains(line, "$http_host") }) {
						return fmt.Sprintf("%q sends $http_host", location[0])
					}
				}
			}
			return ""
		}},
		{"what Envoy answers before it reads the host is answered first in every server", func() string {
			for _, line := range []string{
				`  map "$server_protocol:$request_method" $pf_f_method { "~^HTTP/2\\.0:" 0; "~^[^:]*:(?:ACL|BIND|CHECKOUT|CONNECT|COPY|DELETE|GET|HEAD|LINK|LOCK|MERGE|MKACTIVITY|MKCALENDAR|MKCOL|MOVE|MSEARCH|NOTIFY|OPTIONS|PATCH|POST|PROPFIND|PROPPATCH|PURGE|PUT|QUERY|REBIND|REPORT|SEARCH|SOURCE|SUBSCRIBE|TRACE|UNBIND|UNLINK|UNLOCK|UNSUBSCRIBE)\\z" 0; default 1; }`,
				`  map $http_connection $pf_f_connection { "" 0; "~*^[ \\t,]*(?:(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*(?:,[ \\t,]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*){0,8})?[ \\t,]*\\z" 0; default 1; }`,
				`  map "$server_protocol:$request_uri" $pf_f_raw { "~^HTTP/2\\.0:" 0; "~[\\x00-\\x08\\x0A\\x0B\\x0D-\\x20\\x7F-\\xFF]" 1; default 0; }`,
				`  map "$server_protocol:$http_connection$http_keep_alive$http_proxy_connection$http_transfer_encoding$http_upgrade" $pf_f_h2_connection { "~^HTTP/2\\.0:." 1; default 0; }`,
				`  map "$server_protocol:$http_te" $pf_f_h2_te { "~^HTTP/2\\.0:(?!trailers\\z)." 1; default 0; }`,
				`  map "$pf_f_method$pf_f_connection$pf_f_raw$pf_f_h2_connection$pf_f_h2_te$pf_f_fragment$pf_f_authority$pf_f_names" $pf_f_request { "~1" 1; default 0; }`,
				`  map $server_protocol $pf_f_http10 { "~^HTTP/(?:0\\.9|1\\.0)\\z" 1; default 0; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			checks := []string{"    if ($pf_f_request) {", `      return 400 "$pf_empty";`, "    }",
				"    if ($pf_f_http10) {", `      return 426 "Upgrade Required";`, "    }"}
			servers := blocks(lines(out), "  server {")
			// The default server and three environment servers; the admin server answers on 127.0.0.1 alone.
			checked := 0
			for _, server := range servers {
				if !slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    listen 10000") }) {
					continue
				}
				checked++
				at := slices.Index(server, checks[0])
				firstOther := slices.IndexFunc(server, func(line string) bool {
					return strings.HasPrefix(line, "    location ") || (strings.HasPrefix(line, "    if (") && line != checks[0] && line != checks[3])
				})
				if at < 0 || at+len(checks) > len(server) || !slices.Equal(server[at:at+len(checks)], checks) || at > firstOther {
					return fmt.Sprintf("a server holds no %q before its other checks and locations", checks)
				}
			}
			if checked != 4 {
				return fmt.Sprintf("%d servers on the listener, want 4", checked)
			}
			return ""
		}},
		{"a host with a dot at its end is on no environment", func() string {
			if broken := missing(out, `  map $pf_authority $pf_f_host_dot { "~\\.(?::[0-9]*)?\\z" 1; default 0; }`); broken != "" {
				return broken
			}
			for _, server := range environmentServers(out) {
				check := []string{"    if ($pf_f_host_dot) {", `      return 404 "$pf_empty";`, "    }"}
				at := slices.Index(server, check[0])
				origin := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    if ($pf_o") })
				if at < 0 || !slices.Equal(server[at:at+3], check) || at > origin {
					return fmt.Sprintf("an environment server holds no %q before its origin check", check)
				}
			}
			return ""
		}},
		{"a request's body, header names and silences are Envoy's", func() string {
			for _, line := range []string{
				"  proxy_request_buffering off;", "  underscores_in_headers on;", "  ignore_invalid_headers off;",
				"  client_header_timeout 5m;", "  client_body_timeout 5m;", "  send_timeout 5m;", "  proxy_send_timeout 5m;",
				"  grpc_send_timeout 5m;", "  keepalive_timeout 1h;",
			} {
				if at := strings.Index(out, "\n"+line+"\n"); at < 0 || at > firstServer {
					return fmt.Sprintf("no line %q before the first server", line)
				}
			}
			return ""
		}},
		{"TE is sent on as trailers alone", func() string {
			// trailers in any letter case when Connection names te, and in lower case alone when it does not.
			for _, line := range []string{
				`  map $http_connection $pf_connection_te { "~*(?:^|,)[ \\t]*te[ \\t]*(?:,|\\z)" 1; default 0; }`,
				`  map "$pf_connection_te:$http_te" $pf_te { "~*^1:(?:.*,)?[ \\t]*trailers[ \\t]*(?:,|\\z)" trailers; "~^0:(?:.*,)?[ \\t]*trailers[ \\t]*(?:,|\\z)" trailers; default ""; }`,
				"      proxy_set_header TE $pf_te;",
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
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

// missing returns "" when out holds line as a whole line, and otherwise says it does not.
func missing(out, line string) string {
	if hasLine(out, line) {
		return ""
	}
	return fmt.Sprintf("no line %q", line)
}

// TestQuoting: every text from the input goes into the file through one rule, the text in double quotes with each \
// and " escaped, after a prefix or a path is made a regular expression that matches it as written. A $ in a rewrite is
// written as a variable that holds $, and the file declares that variable only when a rewrite needs it.
func TestQuoting(t *testing.T) {
	cases := []struct {
		name, route string
		want        []string // whole lines the file holds
		absent      string   // a text no line holds; "" for none
	}{
		{"a double quote in a prefix", `{name: app, prefix: '/a"b', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/a\"b" 1; "~^" 0; default failed; }`}, ""},
		{"a backslash in a path", `{name: app, path: '/q\w', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/q\\\\w\\z" 1; "~^" 0; default failed; }`}, ""},
		{"nginx's own syntax in a prefix", `{name: app, prefix: '/s;c{d}#e/', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/s;c\\{d\\}#e/" 1; "~^" 0; default failed; }`}, ""},
		{"a pattern", `{name: app, pattern: '^/p\.q$', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~\\A(?:\\A\\/p\\.q\\z)\\z" 1; "~^" 0; default failed; }`}, ""},
		{"a $ in a rewrite", `{name: app, path: /hook, to: app, rewrite: '/x$y'}`,
			[]string{`      rewrite "^/hook((?s).*)\\z" "/x${pf_dollar}y$1" break;`, `  geo $pf_dollar { default "$"; }`}, ""},
		{"a rewrite with no $", `{name: app, path: /hook, to: app, rewrite: /xy}`,
			[]string{`      rewrite "^/hook((?s).*)\\z" "/xy$1" break;`}, "geo "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, smallFile(c.route))
			for _, line := range c.want {
				if broken := missing(out, line); broken != "" {
					t.Error(broken)
				}
			}
			if c.absent != "" && strings.Contains(out, c.absent) {
				t.Errorf("the file holds %q", c.absent)
			}
		})
	}
}

// TestPatterns: a pattern is written from Go's parse of it, so PCRE2 reads what RE2 reads: every character written is
// a literal, a class is its ranges, a repetition is its numbers, and \Q quotes up to the end of the pattern alone. A
// character outside ASCII matches nothing, as on the paths Envoy matches, and a class takes a byte above 0x7F only
// when it takes every character outside ASCII.
func TestPatterns(t *testing.T) {
	cases := []struct{ name, pattern, want string }{
		{"{,3} is text", `^/x{,3}$`, `\\A\\/x\\{\\,3\\}\\z`},
		{"{ 2 } is text", `^/s{ 2 }$`, `\\A\\/s\\{\\x202\\x20\\}\\z`},
		{"the vertical tab escape is the vertical tab alone", `^/v\v$`, `\\A\\/v\\x0B\\z`},
		{"a quote with no end quotes up to the end of the pattern", `^/a\Q.b`, `\\A\\/a\\.b`},
		{"a class with a negation", `^/n/[^/]+$`, `\\A\\/n\\/[\\x00-\\.0-\\xFF]+\\z`},
		{"a case-insensitive flag", `(?i)^/ci/k$`, `\\A\\/[Cc][Ii]\\/[Kk]\\z`},
		{"an alternation", `^/(a|bc)/x$`, `\\A\\/(?:a|bc)\\/x\\z`},
		{"a non-greedy repetition", `^/g/.+?$`, `\\A\\/g\\/[\\x00-\\x09\\x0B-\\xFF]+?\\z`},
		{"a counted repetition", `^/r/[0-9]{2,4}$`, `\\A\\/r\\/[0-9]{2,4}\\z`},
		{"a character outside ASCII", `^/café$`, `\\A\\/caf(?!)\\z`},
		{"a word boundary", `^/w\b`, `\\A\\/w(?:(?<=[0-9A-Z\\_a-z])(?![0-9A-Z\\_a-z])|(?<![0-9A-Z\\_a-z])(?=[0-9A-Z\\_a-z]))`},
		{"a multi-line end", `(?m)^/m$`, `(?:\\A|(?<=\\n))\\/m(?:\\z|(?=\\n))`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, smallFile(`{name: app, pattern: '`+c.pattern+`', to: app}`))
			if broken := missing(out, `  map $pf_path $pf_m1 { "~\\A(?:`+c.want+`)\\z" 1; "~^" 0; default failed; }`); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// TestManyRoutes: a file of 600 routes holds no line nginx refuses (nginx reads no value over 4096 bytes), sizes the
// variables' hash for them, and chooses its route through a tree of maps two levels deep: nginx finds no variable
// nested 100 deep (ngx_http_variables.c), which a chain of one map a route reached past its 96th route, where every
// request was answered 400 (measured on 1.30.6 and 1.31.8 with 2,000 routes).
func TestManyRoutes(t *testing.T) {
	var routes strings.Builder
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&routes, "  - {name: r%d, prefix: /r%d/, to: app}\n", i, i)
	}
	out := generate(t, "listen: 8000\nservices:\n  app: {http: 3000}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n"+routes.String())
	for i, line := range lines(out) {
		if len(line) > 4096 {
			t.Errorf("line %d holds %d bytes", i+1, len(line))
		}
	}
	var last, top []string
	for n := 577; n <= 600; n++ {
		last = append(last, fmt.Sprintf("$pf_m%d", n))
	}
	for n := 1; n <= 10; n++ {
		top = append(top, fmt.Sprintf("$pf_g%d", n))
	}
	group := ` { "~^(?:0:)*(?<pf_first>[^0:][^:]*):" $pf_first; "~^(?:0:)*\\z" 0; default failed; }`
	for _, line := range []string{
		`  map $pf_path $pf_m600 { "~^/r600/" 600; "~^" 0; default failed; }`,
		`  map "` + strings.Join(last, ":") + `:" $pf_g10` + group,
		`  map "` + strings.Join(top, ":") + `:" $pf_g11` + group,
		`  map "$pf_f_dotparam$pf_f_backslash$pf_f_control$pf_f_upgrade$pf_f_preflight:$pf_mode:$pf_g11" $pf_route {`,
		"  variables_hash_max_size 7456;",
	} {
		if broken := missing(out, line); broken != "" {
			t.Error(broken)
		}
	}
	// The route choice reads two levels of groups, which read the route maps, which read the path the mode chooses,
	// which reads the three classes of the path as written, which read the path out of the request line.
	if got := mapDepth(out, "pf_route"); got != 8 {
		t.Errorf("the route choice nests %d maps, want 8", got)
	}
}

// mapDepth returns how many maps deep the map of the variable name reads: 1 for a map that reads no other map.
func mapDepth(out, name string) int {
	reads := map[string][]string{}
	var defining string
	for _, line := range lines(out) {
		if m := mapLine.FindStringSubmatch(line); m != nil {
			defining = m[1]
		}
		if defining == "" {
			continue
		}
		for _, v := range variable.FindAllStringSubmatch(line, -1) {
			if v[1] != defining {
				reads[defining] = append(reads[defining], v[1])
			}
		}
		if !strings.HasSuffix(line, "{") {
			defining = ""
		}
	}
	var depth func(string) int
	depth = func(name string) int {
		if _, isMap := reads[name]; !isMap {
			return 0
		}
		deepest := 0
		for _, read := range reads[name] {
			deepest = max(deepest, depth(read))
		}
		return deepest + 1
	}
	return depth(name)
}

var (
	mapLine  = regexp.MustCompile(`^  map (?:"[^"]*"|\S+) \$(\w+) \{`)
	variable = regexp.MustCompile(`\$(\w+)`)
)

// TestReadTimeouts: a service that says nothing is answered 504 when Envoy answers it: 15 s after the request without
// timeouts.request (the route's timeout the Envoy output keeps), and after 5 minutes with timeouts.request: none
// (Envoy's stream idle timeout).
func TestReadTimeouts(t *testing.T) {
	cases := []struct{ name, timeouts, want string }{
		{"without timeouts.request", "", "15s"},
		{"with timeouts.request: none", "timeouts: {request: none}\n", "5m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.timeouts+smallFile("{name: app, prefix: /, to: app}"))
			for _, line := range []string{"  proxy_read_timeout " + c.want + ";", "  grpc_read_timeout " + c.want + ";"} {
				if broken := missing(out, line); broken != "" {
					t.Error(broken)
				}
			}
		})
	}
}

// TestHostHash: the server-name hash holds the longest host a file writes; nginx's default bucket of 64 bytes holds
// none of 47 characters or more.
func TestHostHash(t *testing.T) {
	cases := []struct{ name, host, want string }{
		{"a short host", "localhost", "  server_names_hash_bucket_size 64;"},
		{"a host of 48 characters", strings.Repeat("a", 40) + ".example", "  server_names_hash_bucket_size 128;"},
		{"a host of 120 characters", strings.Repeat("a", 112) + ".example", "  server_names_hash_bucket_size 192;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, "listen: 8000\nservices:\n  app: {http: 3000}\norigins:\n  local: {hosts: ["+c.host+"]}\nroutes:\n"+
				"  - {name: app, prefix: /, to: app}\n")
			if broken := missing(out, c.want); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// TestUpgradeMaps: a request is an upgrade only when its Connection names upgrade. Then WebSocket passes with its
// Upgrade and Connection, h2c is stripped with its HTTP2-Settings and the request served, and any other upgrade is
// flagged; otherwise Upgrade is removed and HTTP2-Settings kept, as Envoy answers each. Every HTTP route sends what the
// maps give.
func TestUpgradeMaps(t *testing.T) {
	out := generate(t, string(testsupport.ReadFile(t, batteryFile)))
	for _, line := range []string{
		`  map $http_connection $pf_connection_upgrade { "~*(?:^|,)[ \\t]*upgrade[ \\t]*(?:,|\\z)" 1; default 0; }`,
		`  map "$pf_connection_upgrade:$http_upgrade" $pf_upgrade_kind { "~*^1:websocket\\z" websocket; "~*^1:h2c\\z" h2c; "~^1:." refused; default none; }`,
		`  map $pf_upgrade_kind $pf_f_upgrade { "~^refused\\z" 1; default 0; }`,
		`  map $pf_upgrade_kind $pf_upgrade { "~^websocket\\z" $http_upgrade; default ""; }`,
		`  map $pf_upgrade_kind $pf_connection { "~^websocket\\z" upgrade; default ""; }`,
		`  map $pf_upgrade_kind $pf_http2_settings { "~^h2c\\z" ""; default $http_http2_settings; }`,
	} {
		if broken := missing(out, line); broken != "" {
			t.Error(broken)
		}
	}
	proxied := 0
	for _, server := range environmentServers(out) {
		for _, location := range blocks(server, "    location ") {
			if !slices.ContainsFunc(location, func(line string) bool { return strings.HasPrefix(line, "      proxy_pass ") }) {
				continue
			}
			proxied++
			for _, line := range []string{
				"      proxy_set_header Upgrade $pf_upgrade;",
				"      proxy_set_header Connection $pf_connection;",
				"      proxy_set_header HTTP2-Settings $pf_http2_settings;",
			} {
				if !slices.Contains(location, line) {
					t.Errorf("%q holds no %q", location[0], line)
				}
			}
		}
	}
	// Thirteen HTTP routes in each of three environments, each with a second location but the two with a rewrite.
	if proxied != 72 {
		t.Errorf("%d locations hold proxy_pass, want 72", proxied)
	}
}

// TestWhatIsLeftOut: a block is written only for what the input holds. Without admin there is no admin server, without
// a plain route no HTTP upstream or proxy_pass, and without an rpc entry no gRPC upstream or grpc_pass.
func TestWhatIsLeftOut(t *testing.T) {
	cases := []struct {
		name, proxyfile string
		written         string // a line the file holds, so the row reads a file with routes in it
		// Texts no line holds. A directive is named with the space after it: every file writes proxy_pass_header and
		// grpc_pass_header.
		absent []string
	}{
		{"no admin and no plain route", string(testsupport.ReadFile(t, "../../examples/grpc/yaml/proxyfile.yaml")),
			"      grpc_pass grpc://orders_grpc;", []string{"listen 127.0.0.1:", "proxy_pass ", "_http {"}},
		{"no rpc entry", smallFile("{name: app, prefix: /, to: app}"),
			"      proxy_pass http://app_http;", []string{"grpc_pass ", "_grpc {"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.proxyfile)
			if broken := missing(out, c.written); broken != "" {
				t.Error(broken)
			}
			for _, text := range c.absent {
				if strings.Contains(out, text) {
					t.Errorf("the file holds %q", text)
				}
			}
		})
	}
}

// TestPathModes: a file with no pattern route matches a path holding a byte Envoy escapes before it matches (E, alone
// or with a path that needs normalising) on the path nginx has normalised, because a prefix or a path holds no % and
// no such byte, so it takes the byte and its escape alike; a file with a pattern route refuses such a path, which the
// pattern could take as the byte where Envoy matches its escape.
func TestPathModes(t *testing.T) {
	cases := []struct{ name, route, want string }{
		{"no pattern route", "{name: app, prefix: /, to: app}",
			`  map "$pf_c_n$pf_c_k$pf_c_e$pf_c_q" $pf_mode { 0000 raw; 0001 rawk; 0100 rawk; 0101 rawk; 1000 uri; 0010 uri; 1010 uri; default refused; }`},
		{"a pattern route", "{name: app, pattern: '^/a$', to: app}",
			`  map "$pf_c_n$pf_c_k$pf_c_e$pf_c_q" $pf_mode { 0000 raw; 0001 rawk; 0100 rawk; 0101 rawk; 1000 uri; default refused; }`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if broken := missing(generate(t, smallFile(c.route)), c.want); broken != "" {
				t.Error(broken)
			}
		})
	}
}

// TestScript: the script beside the configuration starts with the header every generated file carries, in the
// script's own comment syntax, and defines and exports each function the configuration names: nginx -t passes a
// configuration whose function the script lacks, and nginx then answers every request 500.
func TestScript(t *testing.T) {
	config, script := generateFiles(t, string(testsupport.ReadFile(t, batteryFile)))
	if first, _, _ := strings.Cut(script, "\n"); first != "// Generated by proxyfile. Do not edit: change the proxyfile beside it and run proxyfile render; proxyfile check fails on a hand edit." {
		t.Errorf("the script's first line is %q", first)
	}
	var named []string
	for _, m := range regexp.MustCompile(`(?m)^\s*js_(?:set \$\w+|content|header_filter|body_filter|access) pf\.(\w+);$`).FindAllStringSubmatch(config, -1) {
		named = append(named, m[1])
	}
	if !slices.Equal(named, []string{"headerNames"}) {
		t.Fatalf("the configuration names the functions %q, want headerNames", named)
	}
	exported := regexp.MustCompile(`(?m)^export default \{(.*)\};$`).FindStringSubmatch(script)
	if exported == nil {
		t.Fatal("the script exports no default object")
	}
	for _, name := range named {
		if !strings.Contains(script, "\nfunction "+name+"(r) {\n") || !slices.Contains(strings.Split(exported[1], ", "), name) {
			t.Errorf("the script does not define and export %s", name)
		}
	}
}
