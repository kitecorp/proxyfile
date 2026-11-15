package nginx_test

import (
	"flag"
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

// update rewrites testdata/nginx-file.conf from the generator's output for the battery's proxyfile:
// go test ./internal/nginx -run TestBatteryFile -update
var update = flag.Bool("update", false, "rewrite testdata/nginx-file.conf from the battery's proxyfile")

// TestBatteryFile: the battery's proxyfile gives testdata/nginx-file.conf, and a second run the same bytes, so nothing
// in the output depends on the order of a Go map or on the run. The file is the generator's output, which -update
// writes, committed so a change to the generator shows in a pull request as a change to the whole configuration.
func TestBatteryFile(t *testing.T) {
	proxyfile := string(testsupport.ReadFile(t, batteryFile))
	if *update {
		testsupport.WriteFile(t, "testdata/nginx-file.conf", generate(t, proxyfile))
	}
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
	// The battery file writes examples/full's tls with client certificates, client_address connection, cors and the
	// lists of every environment, and removes x-client-id from client requests.
	battery := string(testsupport.ReadFile(t, batteryFile))
	out := generate(t, battery)
	// The battery file with five headers removed from every client request: one nginx never reads, Origin, which the
	// origin check reads, and the three the routes send themselves.
	edge := generate(t, rewritten(t, battery, "remove_from_clients: [x-client-id]\n",
		"remove_from_clients: [x-client-id, origin, x-request-id, x-forwarded-proto, x-forwarded-for]\n"))
	// The battery file whose test environment, the second, holds no list, so one environment server admits only the
	// origins of its own hosts; the same without cors, and the same without client certificates.
	oneWithout := rewritten(t, battery, testLists, "  test:\n    hosts: [api.test.example.com]\n")
	cors := generate(t, oneWithout)
	noCORS := generate(t, rewritten(t, oneWithout, corsLine, ""))
	tlsAlone := generate(t, rewritten(t, oneWithout, "  clients: {ca: /etc/gateway/tls/clients-ca.pem}\n", ""))
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
				`  map "$pf_f_dotparam$pf_f_backslash$pf_f_control$pf_f_upgrade$pf_f_preflight:$pf_mode:$pf_g3" $pf_route {`,
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
			return missing(out, `  map $pf_path $pf_m1 { "~(*LIMIT_MATCH=100000)\\A(?:\\A\\/events\\/[\\-0-9a-z]+\\z)\\z" 1; "~^" 0; default failed; }`)
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
			// The battery file's 80 places of a route are read in groups of 64, and a third map reads the two groups.
			var first []string
			for n := 1; n <= 64; n++ {
				first = append(first, fmt.Sprintf("$pf_m%d", n))
			}
			pick := `{ "~^(?:0:)*(?<pf_first>[^0:][^:]*):" $pf_first; "~^(?:0:)*\\z" 0; default failed; }`
			for _, line := range []string{
				`  map "$pf_p5:$request_method" $pf_m5 { "~^1:POST\\z" 5w; "~^e:" failed; default 0; }`,
				`  map "` + strings.Join(first, ":") + `:" $pf_g1 ` + pick,
				`  map "$pf_m65:$pf_m66:$pf_m67:$pf_m68:$pf_m69:$pf_m70:$pf_m71:$pf_m72:$pf_m73:$pf_m74:$pf_m75:$pf_m76:$pf_m77:$pf_m78:$pf_m79:$pf_m80:" $pf_g2 ` + pick,
				`  map "$pf_g1:$pf_g2:" $pf_g3 ` + pick,
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
			// 80 places of a route, one of them with a method, which maps its path and then its method.
			if count != 81 {
				return fmt.Sprintf("%d route maps, want 81", count)
			}
			return ""
		}},
		{"an environment server holds only location / and named ones", func() string {
			servers := environmentServers(out)
			if len(servers) != 4 {
				return fmt.Sprintf("%d environment servers, want 4", len(servers))
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
			// 80 places of a route in each of four environments, each with a second location but the two with a
			// rewrite.
			if routeLocations != 632 {
				return fmt.Sprintf("%d route locations, want 632", routeLocations)
			}
			return ""
		}},
		{"the hosts each environment admits a browser origin on", func() string {
			for _, line := range []string{
				`  map $pf_origin $pf_o1 { "" 0; "~," 1; "~^https?://api\\.example\\.com(:[0-9]+)?\\z" 0; "~^https?://api\\.example\\.org(:[0-9]+)?\\z" 0; "~*^https://example\\.com\\z" 0; "~*^https://www\\.example\\.com\\z" 0; "~^https://.*\\.example\\.com\\z" 0; default 1; }`,
				`  map $pf_origin $pf_o2 { "" 0; "~," 1; "~^https?://api\\.test\\.example\\.com(:[0-9]+)?\\z" 0; "~*^http://localhost:3000\\z" 0; "~*^https://test\\.example\\.com\\z" 0; "~^https://.*\\.test\\.example\\.com\\z" 0; default 1; }`,
				`  map $pf_origin $pf_o3 { "" 0; "~," 1; "~^https?://127\\.0\\.0\\.1(:[0-9]+)?\\z" 0; "~^https?://localhost(:[0-9]+)?\\z" 0; "~^http://(?>.*?\\.example\\.com:).*\\z" 0; "~^http://localhost:.*\\z" 0; default 1; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			return ""
		}},
		{"the Origin as the Envoy output reads it", func() string {
			// nginx's $http_origin is empty for an empty Origin as for none, and joins two lines with a comma and a space;
			// the script reads the lines as Envoy does (n: none, e: every line empty, o and the lines that are not empty
			// joined with a comma), and an empty Origin, or a run of the script that failed, reads as " ", which every
			// origin check refuses. Every route location sends the service that one line.
			for _, line := range []string{
				"  js_set $pf_js_origin pf.origin;",
				`  map $pf_js_origin $pf_origin { n ""; "~^o(?<pf_origin_value>.+)\\z" $pf_origin_value; default " "; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			return everyRouteLocation(out, func(location []string, set string) string {
				if line := "      " + set + " Origin $pf_origin;"; !slices.Contains(location, line) {
					return fmt.Sprintf("%q holds no %q", location[0], line)
				}
				return ""
			})
		}},
		{"a foreign origin is refused before any location", func() string {
			servers := environmentServers(out)
			if len(servers) != 4 {
				return fmt.Sprintf("%d environment servers, want 4", len(servers))
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
				`  map $pf_origin $pf_has_origin { "" 0; default 1; }`,
				`  map "$request_method:$pf_has_origin:$http_access_control_request_method" $pf_f_preflight { "~^OPTIONS:1:." 1; default 0; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			// With cors written each environment answers a preflight with its values, "*" echoing what the request asks
			// for; the live environment, which holds lists, also names the origin it admits and the headers it exposes.
			// Without cors and lists the answer is 200 with no body and no line of its own.
			plain := []string{"    location @pf_r_preflight {", `      return 200 "$pf_empty";`, "    }"}
			answer := []string{"    location @pf_r_preflight {", "      add_header x-request-id $pf_request_id always;",
				"      add_header Vary Origin always;", "      add_header Access-Control-Allow-Methods $http_access_control_request_method always;",
				"      add_header Access-Control-Allow-Headers $http_access_control_request_headers always;",
				"      add_header Access-Control-Max-Age 600 always;", `      return 200 "$pf_empty";`, "    }"}
			// An environment with lists names the origin it admits, and with cors the headers it exposes.
			listed := func(n int) []string {
				return []string{"    location @pf_r_preflight {", "      add_header x-request-id $pf_request_id always;",
					"      add_header Vary Origin always;", fmt.Sprintf("      add_header Access-Control-Allow-Origin $pf_acao%d always;", n),
					`      return 200 "$pf_empty";`, "    }"}
			}
			withCORS := func(n int) []string {
				return slices.Concat(answer[:3], []string{fmt.Sprintf("      add_header Access-Control-Allow-Origin $pf_acao%d always;", n)}, answer[3:6],
					[]string{fmt.Sprintf("      add_header Access-Control-Expose-Headers $pf_aceh%d always;", n)}, answer[6:])
			}
			for _, want := range []struct {
				out    string
				blocks [][]string // the preflight location of each environment server, in order
			}{{noCORS, [][]string{listed(1), plain, listed(3), listed(4)}}, {cors, [][]string{withCORS(1), answer, withCORS(3), withCORS(4)}}} {
				var got [][]string
				for _, server := range environmentServers(want.out) {
					got = append(got, blocks(server, "    location @pf_r_preflight {")...)
				}
				if !slices.EqualFunc(got, want.blocks, slices.Equal) {
					return fmt.Sprintf("the preflight locations are %q, want %q", got, want.blocks)
				}
			}
			return ""
		}},
		{"an Origin holding a comma is refused first", func() string {
			// The Envoy output joins two Origin lines with a comma, and a pattern can take the joined text, which no
			// browser sends; nginx refuses any Origin with a comma before its lists read it.
			for _, prefix := range []string{`  map $pf_origin $pf_o1 { "" 0; "~," 1; `, `  map $pf_origin $pf_c1 { "~," 0; `} {
				if !slices.ContainsFunc(lines(out), func(line string) bool { return strings.HasPrefix(line, prefix) }) {
					return fmt.Sprintf("no line starts %q", prefix)
				}
			}
			return ""
		}},
		{"the origin lists", func() string {
			// An exact origin compares ignoring case and a pattern in the case written, as the Envoy output compares them;
			// the origin check admits what the lists admit, and $pf_c1 says the origin is on a list.
			for _, want := range []struct{ prefix, value string }{{`  map $pf_origin $pf_o1 {`, "0"}, {`  map $pf_origin $pf_c1 {`, "1"}} {
				at := slices.IndexFunc(lines(out), func(line string) bool { return strings.HasPrefix(line, want.prefix) })
				if at < 0 {
					return fmt.Sprintf("no line starts %q", want.prefix)
				}
				for _, key := range []string{`"~*^https://example\\.com\\z"`, `"~*^https://www\\.example\\.com\\z"`, `"~^https://.*\\.example\\.com\\z"`} {
					if entry := " " + key + " " + want.value + ";"; !strings.Contains(lines(out)[at], entry) {
						return fmt.Sprintf("%q holds no %q", lines(out)[at], entry)
					}
				}
			}
			return ""
		}},
		{"the CORS answer only on a routed request", func() string {
			// A request the request flags, the host-dot check or the route choice refuse, a preflight no route takes, an
			// upgrade refused and a request nginx refuses while it reads it, which never reaches the server's set line,
			// carry no Access-Control-* line of the gateway's, and the service's own is never sent.
			for _, line := range []string{
				`  map "$pf_reached:$pf_f_request$pf_f_426$pf_f_host_dot:$pf_route:$pf_c1" $pf_cors1 { "~^1:000:(?:[0-9]+(?:-uri)?|preflight):1\\z" 1; default 0; }`,
				`  map $pf_cors1 $pf_acao1 { 1 $pf_origin; default $upstream_http_access_control_allow_origin; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			live := environmentServers(out)[0]
			for _, line := range []string{"    add_header Access-Control-Allow-Origin $pf_acao1 always;",
				"    proxy_hide_header Access-Control-Allow-Origin;", "    grpc_hide_header Access-Control-Allow-Origin;"} {
				if !slices.Contains(live, line) {
					return fmt.Sprintf("the live server holds no %q", line)
				}
			}
			// The set line comes before the server's first if, so a request any of them answers has reached it too.
			if set, first := slices.Index(live, "    set $pf_reached 1;"), slices.IndexFunc(live, func(line string) bool {
				return strings.HasPrefix(line, "    if (")
			}); set < 0 || set > first {
				return fmt.Sprintf("the live server sets $pf_reached at line %d, want before its first if at line %d", set, first)
			}
			for _, name := range []string{"refused", "upgrade-refused", "no-route"} {
				location := blocks(live, "    location @pf_r_"+name+" {")
				if len(location) != 1 || !slices.Equal(location[0][1:3], []string{"      add_header x-request-id $pf_request_id always;", "      add_header Vary Origin always;"}) ||
					slices.ContainsFunc(location[0], func(line string) bool { return strings.Contains(line, "Access-Control") }) {
					return fmt.Sprintf("the live server's @pf_r_%s is %q, want its own x-request-id and Vary lines and no Access-Control line", name, location)
				}
			}
			return ""
		}},
		{"the case-blind keys", func() string {
			// RFC 9110 compares a Connection token and an upgrade's protocol name ignoring case, and the Envoy output an
			// exact origin; a route, a pattern and a host compare in the case written.
			want := []string{
				`"~*^,*(?:(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)(?:,+(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)){0,8})?,*\\z"`,
				`"~*(?:^|,)[ \\t]*te[ \\t]*(?:,|\\z)"`,
				`"~*^1:(?:.*,)?[ \\t]*trailers[ \\t]*(?:,|\\z)"`,
				`"~*(?:^|,)[ \\t]*upgrade[ \\t]*(?:,|\\z)"`,
				`"~*^1:websocket\\z"`,
				`"~*^1:h2c\\z"`,
				`"~*^https://example\\.com\\z"`, `"~*^https://example\\.com\\z"`,
				`"~*^https://www\\.example\\.com\\z"`, `"~*^https://www\\.example\\.com\\z"`,
				`"~*^http://localhost:3000\\z"`, `"~*^http://localhost:3000\\z"`,
				`"~*^https://test\\.example\\.com\\z"`, `"~*^https://test\\.example\\.com\\z"`,
			}
			if got := regexp.MustCompile(`"~\*(?:[^"\\]|\\.)*"`).FindAllString(out, -1); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				return fmt.Sprintf("the keys that ignore case are %q, want %q", got, want)
			}
			return ""
		}},
		{"an upgrade other than WebSocket", func() string {
			// The Envoy output answers it 403 with the body Invalid CORS request when the request carries an Origin, and
			// with no body when it carries none, with or without cors.
			if broken := missing(out, `  map $pf_origin $pf_upgrade_refused { "" ""; default "Invalid CORS request"; }`); broken != "" {
				return broken
			}
			for _, server := range environmentServers(out) {
				location := blocks(server, "    location @pf_r_upgrade-refused {")
				if len(location) != 1 || !slices.Contains(location[0], `      return 403 "$pf_upgrade_refused";`) {
					return fmt.Sprintf("an environment server's @pf_r_upgrade-refused is %q, want it to return 403 \"$pf_upgrade_refused\"", location)
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
			return everyRouteLocation(out, func(location []string, set string) string {
				for _, name := range removed {
					if line := "      " + set + " " + name + ` "";`; !slices.Contains(location, line) {
						return fmt.Sprintf("%q holds no %q", location[0], line)
					}
				}
				return ""
			})
		}},
		{"connection: the address is the peer's", func() string {
			// The Envoy output with client_address connection makes a new request id, sends the connection's scheme,
			// appends the peer's address to the client's X-Forwarded-For and sends it as x-envoy-external-address, and
			// removes five more x-envoy- headers; with X-Forwarded-For removed it sends the peer's address alone.
			for _, want := range []struct{ out, line string }{
				{out, `  map "" $pf_request_id { default $pf_new_request_id; }`},
				{out, `  map "" $pf_forwarded_proto { default $scheme; }`},
				{out, `  map $http_x_forwarded_for $pf_forwarded_for { "" $remote_addr; default "$http_x_forwarded_for,$remote_addr"; }`},
				{edge, `  map "" $pf_forwarded_for { default $remote_addr; }`},
			} {
				if broken := missing(want.out, want.line); broken != "" {
					return broken
				}
			}
			return everyRouteLocation(out, func(location []string, set string) string {
				for _, header := range []string{"X-Forwarded-For $pf_forwarded_for;", "x-envoy-external-address $remote_addr;",
					`x-envoy-decorator-operation "";`, `x-envoy-downstream-service-cluster "";`, `x-envoy-downstream-service-node "";`,
					`x-envoy-original-host "";`, `x-envoy-original-path "";`} {
					if line := "      " + set + " " + header; !slices.Contains(location, line) {
						return fmt.Sprintf("%q holds no %q", location[0], line)
					}
				}
				return ""
			})
		}},
		{"each removed name emptied in every location", func() string {
			return everyRouteLocation(edge, func(location []string, set string) string {
				for _, line := range []string{set + ` "x-client-id" "";`, set + ` "origin" "";`} {
					if !slices.Contains(location, "      "+line) {
						return fmt.Sprintf("%q holds no %q", location[0], line)
					}
				}
				// A removed name the location sends itself is set once, to the value with no client copy.
				for _, name := range []string{"x-request-id", "x-forwarded-proto", "x-forwarded-for"} {
					setsName := regexp.MustCompile(`(?i)^      ` + set + ` "?` + regexp.QuoteMeta(name) + `"? `)
					if count := len(slices.DeleteFunc(slices.Clone(location), func(line string) bool { return !setsName.MatchString(line) })); count != 1 {
						return fmt.Sprintf("%q sets %s %d times, want once", location[0], name, count)
					}
				}
				return ""
			})
		}},
		{"a removed name nginx reads is read empty", func() string {
			for _, variable := range []string{"$http_origin", "$pf_origin", "$pf_js_origin", "$http_x_request_id", "$http_x_forwarded_proto", "$http_x_forwarded_for"} {
				if strings.Contains(edge, variable) {
					return fmt.Sprintf("the output with the header removed reads %s", variable)
				}
			}
			return ""
		}},
		{"the service receives the authority nginx picked the server by as Host", func() string {
			// The origin form after one or more spaces names the Host header, the absolute form the host it writes
			// before its path, and any other line nothing, which the request flags refuse.
			for _, line := range []string{
				`  map $request $pf_authority { "~^[^ ]+ +/" $http_host; "~^[^ ]+ +[A-Za-z][A-Za-z0-9+.-]*://(?<pf_authority_host>[^/?# ]*[^/?#: ])(?=[/ ])" $pf_authority_host; default ""; }`,
				`  map "$pf_f_426:$pf_authority" $pf_f_authority { "~^1:" 0; "~^0:." 0; default 1; }`,
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
				`  map $http_connection $pf_f_connection { "" 0; "~*^,*(?:(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)(?:,+(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)){0,8})?,*\\z" 0; default 1; }`,
				`  map "$server_protocol:$request_uri" $pf_f_raw { "~^HTTP/2\\.0:" 0; "~[\\x00-\\x08\\x0A\\x0B\\x0D-\\x20\\x7F-\\xFF]" 1; default 0; }`,
				`  map "$server_protocol:$http_connection$http_keep_alive$http_proxy_connection$http_transfer_encoding$http_upgrade" $pf_f_h2_connection { "~^HTTP/2\\.0:." 1; default 0; }`,
				`  map "$server_protocol:$http_te" $pf_f_h2_te { "~^HTTP/2\\.0:(?!trailers\\z)." 1; default 0; }`,
				`  map $server_protocol $pf_f_version { "~^HTTP/[0-9]\\.[0-9]\\z" 0; default 1; }`,
				`  map "$pf_f_method$pf_f_connection$pf_f_raw$pf_f_h2_connection$pf_f_h2_te$pf_f_fragment$pf_f_authority$pf_f_names$pf_f_version" $pf_f_request { "~1" 1; default 0; }`,
				`  map $server_protocol $pf_f_426 { "~^HTTP/(?:0\\.9|1\\.[02-9])\\z" 1; default 0; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			checks := []string{"    if ($pf_f_request) {", `      return 400 "$pf_empty";`, "    }",
				"    if ($pf_f_426) {", `      return 426 "Upgrade Required";`, "    }"}
			servers := blocks(lines(out), "  server {")
			// The default server and four environment servers; the admin server answers on 127.0.0.1 alone.
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
			if checked != 5 {
				return fmt.Sprintf("%d servers on the listener, want 5", checked)
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
		{"every public server listens with TLS", func() string {
			// The admin server answers on 127.0.0.1 alone, as Envoy's admin does, and stays plain.
			want := []string{"    listen 127.0.0.1:9901;", "    listen 10000 ssl default_server;", "    listen 10000 ssl;",
				"    listen 10000 ssl;", "    listen 10000 ssl;", "    listen 10000 ssl;"}
			for _, file := range []string{out, tlsAlone} {
				var got []string
				for _, line := range lines(file) {
					if strings.HasPrefix(line, "    listen ") {
						got = append(got, line)
					}
				}
				if !slices.Equal(got, want) {
					return fmt.Sprintf("the listen lines are %q, want %q", got, want)
				}
			}
			return ""
		}},
		{"the protocols, ciphers and groups", func() string {
			// TLS 1.2 and 1.3 alone, the Envoy output's TLS 1.2 ciphers, and its key exchange groups, the post-quantum
			// one first (measured on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8).
			for _, file := range []string{out, tlsAlone} {
				first := strings.Index(file, "\n  server {\n")
				for _, line := range []string{
					`  ssl_certificate "/etc/gateway/tls/certificate.pem";`,
					`  ssl_certificate_key "/etc/gateway/tls/key.pem";`,
					"  ssl_protocols TLSv1.2 TLSv1.3;",
					"  ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305;",
					"  ssl_ecdh_curve X25519MLKEM768:X25519:prime256v1;",
				} {
					if at := strings.Index(file, "\n"+line+"\n"); at < 0 || at > first {
						return fmt.Sprintf("no line %q before the first server", line)
					}
				}
			}
			return ""
		}},
		{"a client certificate is asked for and checked", func() string {
			clients := []string{"  ssl_verify_client on;", "  ssl_verify_depth 100;", `  ssl_client_certificate "/etc/gateway/tls/clients-ca.pem";`}
			for _, line := range clients {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			for _, text := range []string{"ssl_verify_client", "ssl_verify_depth", "ssl_client_certificate"} {
				if strings.Contains(tlsAlone, text) {
					return fmt.Sprintf("the file with tls alone holds %q", text)
				}
			}
			return ""
		}},
		{"every TLS failure answered empty", func() string {
			// A client certificate that fails, plain HTTP on the TLS port and, with client certificates, a Host that
			// names another environment than the TLS server name are answered before a location is chosen, so only a
			// server's own error_page catches them; each answer is empty and reaches no service. Where the server sends
			// Access-Control lines, the two answers repeat its other two lines alone, as its other refusals do.
			refused := []string{"    error_page 495 496 497 = @pf_tls_refused;", "    error_page 421 = @pf_tls_misdirected;",
				"    location @pf_tls_refused {", `      return 400 "$pf_empty";`, "    }",
				"    location @pf_tls_misdirected {", `      return 421 "$pf_empty";`, "    }"}
			own := slices.Concat(refused[:3], []string{"      add_header x-request-id $pf_request_id always;", "      add_header Vary Origin always;"},
				refused[3:6], []string{"      add_header x-request-id $pf_request_id always;", "      add_header Vary Origin always;"}, refused[6:])
			alone := slices.Concat(refused[:1], refused[2:5])
			aloneOwn := slices.Concat(refused[:1], refused[2:3], []string{"      add_header x-request-id $pf_request_id always;",
				"      add_header Vary Origin always;"}, refused[3:5])
			for _, want := range []struct {
				file   string
				blocks [][]string // the default server's, then each environment server's, in order
			}{{cors, [][]string{refused, own, refused, own, own}}, {tlsAlone, [][]string{alone, aloneOwn, alone, aloneOwn, aloneOwn}}} {
				var got [][]string
				for _, server := range blocks(lines(want.file), "  server {") {
					if !slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    listen 10000") }) {
						continue
					}
					// The lines follow every check the server makes and come before its first location.
					at, lastCheck, first := slices.Index(server, want.blocks[0][0]), -1, slices.Index(server, "    location / {")
					for i, line := range server {
						if strings.HasPrefix(line, "    if (") {
							lastCheck = i
						}
					}
					if at < 0 || at < lastCheck || at > first {
						return fmt.Sprintf("a server holds no %q between its checks and its first location", want.blocks[0][0])
					}
					size := 0
					if len(got) < len(want.blocks) {
						size = len(want.blocks[len(got)])
					}
					got = append(got, server[at:min(at+size, len(server))])
				}
				if !slices.EqualFunc(got, want.blocks, slices.Equal) {
					return fmt.Sprintf("the TLS answers are %q, want %q", got, want.blocks)
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

// everyRouteLocation calls check on each route location of the environment servers of out, the ones that send the
// request to a service, with the directive that sets a header there, and returns the first problem check names. The
// battery file has 77 HTTP routes and three RPC methods in each of four environments, each with a second location but
// the two HTTP routes with a rewrite, and a row on fewer locations fails.
func everyRouteLocation(out string, check func(location []string, set string) string) string {
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
			if broken := check(location, set); broken != "" {
				return broken
			}
		}
	}
	if kinds["proxy_set_header"] != 608 || kinds["grpc_set_header"] != 24 {
		return fmt.Sprintf("%d HTTP and %d gRPC route locations, want 608 and 24", kinds["proxy_set_header"], kinds["grpc_set_header"])
	}
	return ""
}

// corsLine is the battery file's cors key, examples/full's.
const corsLine = `cors: {allow_methods: "*", allow_headers: "*", expose_headers: "Content-Disposition,X-Request-Id", max_age: "600"}` + "\n"

// testLists is the battery file's test environment with examples/full's lists.
const testLists = "  test:\n    hosts: [api.test.example.com]\n    origins: [\"http://localhost:3000\", \"https://test.example.com\"]\n" +
	"    patterns: [\"https://*.test.example.com\"]\n"

// rewritten returns the battery file with old, which it must hold once, written as new, so a test of a key's absence
// or of other values reads the battery file's other lines as they are.
func rewritten(t *testing.T, battery, old, new string) string {
	t.Helper()
	if strings.Count(battery, old) != 1 {
		t.Fatalf("the battery file holds %d times the text %q, want once", strings.Count(battery, old), old)
	}
	return strings.Replace(battery, old, new, 1)
}

// TestCORS: each cors line is written only when its key is, and a written value is sent as written. Without cors an
// environment with lists names the origin it admits and nothing more; a value other than "*" is sent in place of
// what the request asks for (measured on Envoy 1.40.2: the service's own Access-Control-Expose-Headers passes when
// expose_headers is not written).
func TestCORS(t *testing.T) {
	battery := string(testsupport.ReadFile(t, batteryFile))
	cases := []struct {
		name, proxyfile string
		want            []string // whole lines the file holds
		absent          []string // texts no line holds
	}{
		{"no cors key", rewritten(t, battery, corsLine, ""), []string{
			"    add_header Access-Control-Allow-Origin $pf_acao1 always;",
			"    proxy_hide_header Access-Control-Allow-Origin;",
			"      add_header Access-Control-Allow-Origin $pf_acao1 always;",
		}, []string{"Access-Control-Expose-Headers", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Max-Age", "$pf_aceh"}},
		{"values written in place of what the request asks for", rewritten(t, battery, corsLine, `cors: {allow_methods: "GET, POST", allow_headers: content-type}`+"\n"), []string{
			`      add_header Access-Control-Allow-Methods "GET, POST" always;`,
			`      add_header Access-Control-Allow-Headers "content-type" always;`,
		}, []string{"$http_access_control_request_headers", "Access-Control-Max-Age", "Access-Control-Expose-Headers"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.proxyfile)
			for _, line := range c.want {
				if broken := missing(out, line); broken != "" {
					t.Error(broken)
				}
			}
			for _, text := range c.absent {
				if strings.Contains(out, text) {
					t.Errorf("the file holds %q", text)
				}
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

// TestRequestID: a request that carries no x-request-id gets an id nginx makes, written as Envoy writes its own, a
// version 4 UUID in lower case, from nginx's 32 random hexadecimal digits; with client_address forwarded a client's
// own id is kept, as the Envoy output keeps it.
func TestRequestID(t *testing.T) {
	battery := string(testsupport.ReadFile(t, batteryFile))
	cases := []struct {
		name, proxyfile string
		want            []string // consecutive whole lines the file holds
	}{
		{"nginx's own id is a version 4 UUID", battery, []string{
			"  map $request_id $pf_new_request_id {",
			`    "~^(?<pf_id1>[0-9a-f]{8})(?<pf_id2>[0-9a-f]{4})[0-9a-f](?<pf_id3>[0-9a-f]{3})[048c](?<pf_id4>[0-9a-f]{3})(?<pf_id5>[0-9a-f]{12})\\z" $pf_id1-$pf_id2-4$pf_id3-8$pf_id4-$pf_id5;`,
			`    "~^(?<pf_id1>[0-9a-f]{8})(?<pf_id2>[0-9a-f]{4})[0-9a-f](?<pf_id3>[0-9a-f]{3})[159d](?<pf_id4>[0-9a-f]{3})(?<pf_id5>[0-9a-f]{12})\\z" $pf_id1-$pf_id2-4$pf_id3-9$pf_id4-$pf_id5;`,
			`    "~^(?<pf_id1>[0-9a-f]{8})(?<pf_id2>[0-9a-f]{4})[0-9a-f](?<pf_id3>[0-9a-f]{3})[26ae](?<pf_id4>[0-9a-f]{3})(?<pf_id5>[0-9a-f]{12})\\z" $pf_id1-$pf_id2-4$pf_id3-a$pf_id4-$pf_id5;`,
			`    "~^(?<pf_id1>[0-9a-f]{8})(?<pf_id2>[0-9a-f]{4})[0-9a-f](?<pf_id3>[0-9a-f]{3})[37bf](?<pf_id4>[0-9a-f]{3})(?<pf_id5>[0-9a-f]{12})\\z" $pf_id1-$pf_id2-4$pf_id3-b$pf_id4-$pf_id5;`,
			"    default $request_id;",
			"  }",
		}},
		{"the client's own id is kept with forwarded", rewritten(t, battery, "client_address: connection\n", "client_address: forwarded\n"), []string{
			`  map $http_x_request_id $pf_request_id { "" $pf_new_request_id; default $http_x_request_id; }`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			all := lines(generate(t, c.proxyfile))
			at := slices.Index(all, c.want[0])
			if at < 0 || at+len(c.want) > len(all) {
				t.Fatalf("no line %q", c.want[0])
			}
			if got := all[at : at+len(c.want)]; !slices.Equal(got, c.want) {
				t.Errorf("the lines are %q, want %q", got, c.want)
			}
		})
	}
}

// TestQuoting: every text from the input goes into the file through one rule, the text in double quotes with each \
// and " escaped, after a prefix or a path is made a regular expression that matches it as written. A $ in a rewrite is
// written as a variable that holds $, and the file declares that variable only when a rewrite needs it.
func TestQuoting(t *testing.T) {
	cases := []struct {
		name, route string
		want        []string // whole lines the file holds
		absent      string   // a text no line holds; "" for none
		file        string   // the whole proxyfile, read in place of smallFile(route) when set
	}{
		{"a double quote in a prefix", `{name: app, prefix: '/a"b', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/a\"b" 1; "~^" 0; default failed; }`}, "", ""},
		{"a backslash in a path", `{name: app, path: '/q\w', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/q\\\\w\\z" 1; "~^" 0; default failed; }`}, "", ""},
		{"nginx's own syntax in a prefix", `{name: app, prefix: '/s;c{d}#e/', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~^/s;c\\{d\\}#e/" 1; "~^" 0; default failed; }`}, "", ""},
		{"a pattern", `{name: app, pattern: '^/p\.q$', to: app}`,
			[]string{`  map $pf_path $pf_m1 { "~(*LIMIT_MATCH=100000)\\A(?:\\A\\/p\\.q\\z)\\z" 1; "~^" 0; default failed; }`}, "", ""},
		{"a $ in a rewrite", `{name: app, path: /hook, to: app, rewrite: '/x$y'}`,
			[]string{`      rewrite "^/hook((?s).*)\\z" "/x${pf_dollar}y$1" break;`, `  geo $pf_dollar { default "$"; }`}, "", ""},
		{"a rewrite with no $", `{name: app, path: /hook, to: app, rewrite: /xy}`,
			[]string{`      rewrite "^/hook((?s).*)\\z" "/xy$1" break;`}, "geo ", ""},
		// A cors value is sent as written: nginx reads \" in quotes as ", and a $ as the start of a variable.
		{name: "cors values", file: "listen: 8000\nservices:\n  app: {http: 3000}\n" +
			`cors: {allow_methods: 'GET, "X"', expose_headers: "a$x"}` + "\norigins:\n  local: {hosts: [localhost], origins: [\"http://localhost:3000\"]}\n" +
			"routes:\n  - {name: app, prefix: /, to: app}\n", want: []string{
			`      add_header Access-Control-Allow-Methods "GET, \"X\"" always;`,
			`  map $pf_cors1 $pf_aceh1 { 1 "a${pf_dollar}x"; default $upstream_http_access_control_expose_headers; }`,
			`  geo $pf_dollar { default "$"; }`}},
		// A certificate's or a key's path holding $ is refused before the generator runs (format's TestRefusals); the
		// CA's path takes no variable, and nginx reads its $ as written (measured on nginx 1.30.6 and 1.31.8), so it
		// needs no $pf_dollar.
		{name: "tls paths", file: smallFile("{name: app, prefix: /, to: app}") +
			`tls: {certificate: '/etc/a b/c "d".pem', key: '/etc/a;b/k\e.pem', clients: {ca: '/etc/a$b/ca.pem'}}` + "\n", want: []string{
			`  ssl_certificate "/etc/a b/c \"d\".pem";`,
			`  ssl_certificate_key "/etc/a;b/k\\e.pem";`,
			`  ssl_client_certificate "/etc/a$b/ca.pem";`}, absent: "geo "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proxyfile := smallFile(c.route)
			if c.file != "" {
				proxyfile = c.file
			}
			out := generate(t, proxyfile)
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
			if broken := missing(out, `  map $pf_path $pf_m1 { "~(*LIMIT_MATCH=100000)\\A(?:`+c.want+`)\\z" 1; "~^" 0; default failed; }`); broken != "" {
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
		"  variables_hash_max_size 7476;",
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
	// 77 HTTP routes in each of four environments, each with a second location but the two with a rewrite.
	if proxied != 608 {
		t.Errorf("%d locations hold proxy_pass, want 608", proxied)
	}
}

// TestConnectionElements: the request flag of a Connection header is 1, answered 400, where Envoy 1.40.2 answers 400:
// Envoy counts every element of the list that is not empty, one of white space alone too, and refuses ten (measured
// beside nginx 1.30.6 and 1.31.8 with elements of spaces, of tabs and of both, alone, mixed with empty ones and before
// and after a token). Each value is the header as nginx reads it, without the white space around the whole value.
func TestConnectionElements(t *testing.T) {
	flagMap := regexp.MustCompile(`^  map \$http_connection \$pf_f_connection \{ "" 0; "~\*((?:[^"\\]|\\.)*)" 0; default 1; \}$`)
	var connection *regexp.Regexp
	for _, line := range lines(generate(t, smallFile("{name: app, prefix: /, to: app}"))) {
		if m := flagMap.FindStringSubmatch(line); m != nil {
			// nginx reads \\ between quotes as \ and \" as ", and ~* as a match in any letter case.
			connection = regexp.MustCompile(`(?i)` + strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(m[1]))
		}
	}
	if connection == nil {
		t.Fatal("the file holds no map of $http_connection to $pf_f_connection with one expression")
	}
	cases := []struct {
		name, value string
		want        int
	}{
		{"the token alone", "te", 0},
		{"a token and eight elements of one space", "te," + strings.Repeat(" ,", 8), 0},
		{"a token and nine elements of one space", "te," + strings.Repeat(" ,", 9), 1},
		{"a token and nine elements of one tab", "te," + strings.Repeat("\t,", 9), 1},
		{"a token and eight elements of a space and a tab", "te," + strings.Repeat(" \t,", 8), 0},
		{"a token and nine elements of a space and a tab", "te," + strings.Repeat(" \t,", 9), 1},
		{"a token and twenty empty elements", "te" + strings.Repeat(",", 20), 0},
		{"a token and eight elements of white space between empty ones", "te," + strings.Repeat(", ,", 8), 0},
		{"a token and nine elements of white space between empty ones", "te," + strings.Repeat(", ,", 9), 1},
		{"eight elements of white space before the token", "," + strings.Repeat(" ,", 8) + "te", 0},
		{"nine elements of white space before the token", "," + strings.Repeat(" ,", 9) + "te", 1},
		{"two elements of white space alone", ", , ,", 0},
		{"ten elements of white space alone", "," + strings.Repeat(" ,", 10), 1},
		{"close and nine elements of white space", "close," + strings.Repeat(" ,", 9), 1},
		{"eight tokens and one element of white space", strings.Repeat("te, ", 8) + ",", 0},
		{"nine tokens and one element of white space", strings.Repeat("te, ", 9) + ",", 1},
		{"nine tokens", "te" + strings.Repeat(", te", 8), 0},
		{"ten tokens", "te" + strings.Repeat(", te", 9), 1},
		{"two tokens with white space around them", "te \t,\t close", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := 1
			if connection.MatchString(c.value) {
				got = 0
			}
			if got != c.want {
				t.Errorf("$pf_f_connection for %q = %d, want %d", c.value, got, c.want)
			}
		})
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
	if !slices.Equal(named, []string{"headerNames", "origin"}) {
		t.Fatalf("the configuration names the functions %q, want headerNames and origin", named)
	}
	// A header value holding a control byte other than a tab is refused: nginx passes one to the service, where Envoy
	// answers 400 over HTTP/1.1 and closes the connection over HTTP/2 (measured on nginx 1.30.6 and 1.31.8 beside
	// Envoy 1.40.2).
	if check := "\n        if (/[\\x00-\\x08\\x0A-\\x1F\\x7F]/.test(raw[i][1])) {\n            return \"1\";\n        }\n"; !strings.Contains(script, check) {
		t.Errorf("the script holds no %q", check)
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
