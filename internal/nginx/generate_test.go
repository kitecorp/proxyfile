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

// batteryLimits, batteryTimeouts and batteryLog are the battery file's limits, timeouts and log, and batteryRetried its
// two retried routes, which a test replaces (rewritten) to read the output of other values, or of none.
const (
	batteryLimits   = "limits:\n  request_headers: {size: 60KiB, count: 100}\n"
	batteryTimeouts = "timeouts:\n  idle: 2s\n  upstream_idle: 3s\n"
	batteryLog      = "log: {status_from: 400, sample: 1%}\n"
	batteryRetried  = "  - {name: retried, prefix: /retried/, to: shop, retries: 1}\n" +
		"  - {name: retried-limited, prefix: /retried-limited/, to: shop, retries: 1, rate_limit: {requests: 10, per: 1m, by: all}}\n"
)

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
	files, err := generateIn(t, t.TempDir(), proxyfile)
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

// generateIn returns what nginx.Generate returns for the proxyfile text, written as proxyfile.yaml in dir and read as
// render reads a proxyfile that writes its origins and its RPC methods: the files, or the error, which names the file by
// its path in dir. A problem ends the test.
func generateIn(t *testing.T, dir, proxyfile string) ([]generator.File, error) {
	t.Helper()
	path := filepath.Join(dir, "proxyfile.yaml")
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
	return nginx.Generate(generator.Input{Gateway: file.Gateway, Origins: file.Origins, RPC: routes})
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
	// The battery file with no timeouts, whose lines are then nginx's own for none.
	noTimeouts := generate(t, rewritten(t, battery, batteryTimeouts, ""))
	// The battery file with limits.request_headers of the size given and 20 fields.
	headerLimits := func(size string) string {
		return generate(t, rewritten(t, battery, batteryLimits, "limits: {request_headers: {size: "+size+", count: 20}}\n"))
	}
	// The battery file whose client's X-Forwarded-Proto is read: with client_address forwarded, with client_address not
	// written and no tls, which reads as forwarded, and with forwarded and the header removed from every client request,
	// which leaves nothing of the client's to read.
	forwardedFile := rewritten(t, battery, "client_address: connection\n", "client_address: forwarded\n")
	forwarded := generate(t, forwardedFile)
	unset := generate(t, rewritten(t, rewritten(t, battery, tlsLines, ""), "client_address: connection\n", ""))
	forwardedRemoved := generate(t, rewritten(t, forwardedFile, "remove_from_clients: [x-client-id]\n",
		"remove_from_clients: [x-client-id, x-forwarded-proto]\n"))
	// The battery file with the timeouts given in place of its own.
	withTimeouts := func(timeouts string) string {
		return generate(t, rewritten(t, battery, batteryTimeouts, "timeouts: "+timeouts+"\n"))
	}
	// The battery file with the log given in place of its own.
	withLog := func(log string) string {
		return generate(t, rewritten(t, battery, batteryLog, "log: "+log+"\n"))
	}
	// The battery file with no retried route, with the timeouts given in place of its own, or none for "": the
	// upstream-failure line of a file with none.
	notRetried := func(timeouts string) string {
		if timeouts != "" {
			timeouts = "timeouts: " + timeouts + "\n"
		}
		return generate(t, rewritten(t, rewritten(t, battery, batteryRetried, ""), batteryTimeouts, timeouts))
	}
	// The battery file whose shop route, to a service of two backends, is retried once, with the timeouts given in place
	// of its own, or none for "".
	retriedShop := func(timeouts string) string {
		file := rewritten(t, battery, "{name: shop, prefix: /, to: shop, retries: 0}", "{name: shop, prefix: /, to: shop, retries: 1}")
		if timeouts != "" {
			timeouts = "timeouts: " + timeouts + "\n"
		}
		return generate(t, rewritten(t, file, batteryTimeouts, timeouts))
	}
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
			// A request goes on through named locations, as a redirect to a URI would search the locations again for
			// another path. The admin server, which "the admin server" holds line by line, sends stub_status's 405 back
			// to /stats on purpose, so the request is read without it.
			public := out
			for _, server := range blocks(lines(out), "  server {") {
				if slices.Contains(server, "    listen 127.0.0.1:9901;") {
					public = strings.Replace(public, strings.Join(server, "\n"), "", 1)
				}
			}
			if public == out {
				return "the file has no admin server"
			}
			redirect := regexp.MustCompile(`(?m)^\s*(rewrite\s.*\s(last|redirect|permanent);|try_files\s|index\s|error_page\s[0-9 ]+(=[0-9]*)?\s*/)`)
			if found := redirect.FindAllString(public, -1); found != nil {
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
				`    "~^1" refused;`, `    "~^01" refused;`, `    "~^001" refused;`, `    "~^0001" upgrade-refused-$pf_has_origin;`,
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
				`  map $pf_rawpath $pf_c_n { "~^(?>.*?(?:/|%2[Ff])(?=(?:/|%2[Ff]))).*(?:/|%2[Ff])(?:\\.|%2[Ee])(?:\\.|%2[Ee])(?:/|%2[Ff]|\\z)" d; "~//|/\\.\\.?(?:/|\\z)|%(?:2[DEFdef]|3[0-9]|4[1-9A-Fa-f]|5[0-9ACFacf]|6[1-9A-Fa-f]|7[0-9AEae])" 1; "~^/" 0; default e; }`,
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
			// catalog, route 10, has no rewrite; accounts-webhooks, route 5, has one, which reads $uri in both modes. Each
			// location names its route and upstream for the request log on its first two lines, a limited route's its
			// limit on the next two (the battery file limits catalog), then rewrites the path.
			for _, server := range environmentServers(out) {
				for _, want := range []struct {
					open, line string
					after      int // the lines between open and line
				}{
					{"    location @pf_r_10 {", "      proxy_set_header Host $pf_authority;", 4},
					{"    location @pf_r_10-uri {", "      rewrite ^ $uri break;", 4},
					{"    location @pf_r_5 {", `      rewrite "^/api/webhooks((?s).*)\\z" "/hooks/payments$1" break;`, 2},
				} {
					at := slices.Index(server, want.open) + want.after + 1
					if at <= want.after || at >= len(server) || server[at] != want.line {
						return fmt.Sprintf("an environment server holds no %q with %q after its log and limit lines", want.open, want.line)
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
			// The battery file's 83 places of a route are read in groups of 64, and a third map reads the two groups.
			var first []string
			for n := 1; n <= 64; n++ {
				first = append(first, fmt.Sprintf("$pf_m%d", n))
			}
			pick := `{ "~^(?:0:)*(?<pf_first>[^0:][^:]*):" $pf_first; "~^(?:0:)*\\z" 0; default failed; }`
			for _, line := range []string{
				`  map "$pf_p5:$request_method" $pf_m5 { "~^1:POST\\z" 5w; "~^e:" failed; default 0; }`,
				`  map "` + strings.Join(first, ":") + `:" $pf_g1 ` + pick,
				`  map "$pf_m65:$pf_m66:$pf_m67:$pf_m68:$pf_m69:$pf_m70:$pf_m71:$pf_m72:$pf_m73:$pf_m74:$pf_m75:$pf_m76:$pf_m77:$pf_m78:$pf_m79:$pf_m80:$pf_m81:$pf_m82:$pf_m83:" $pf_g2 ` + pick,
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
			// 83 places of a route, one of them with a method, which maps its path and then its method.
			if count != 84 {
				return fmt.Sprintf("%d route maps, want 84", count)
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
		{"a service that fails is answered 502 with no body, and a gRPC call 200 with grpc-status 14", func() string {
			// A request nginx answers 502 itself, because its service refused the connection, took none in its connect
			// time, reset the connection or the call's stream, closed it or answered in a form nginx cannot read, is
			// answered 502 with no body; a gRPC call, a request whose content-type is application/grpc or starts
			// application/grpc+, is answered as the Envoy output answers it, 200 with grpc-status 14 and no body in one
			// header frame, with the server's add_header lines repeated, as a location with one of its own inherits none,
			// and with no grpc-message, where Envoy names a stream reset or an HTTP/1.1 answer in one
			// (services.<service name>.grpc). Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, over HTTP/2 and
			// HTTP/1.1, to a gRPC route and a plain one.
			if broken := missing(out, `  map $http_content_type $pf_grpc_call { "~^application/grpc(?:\\+|\\z)" -grpc; default ""; }`); broken != "" {
				return broken
			}
			servers := environmentServers(out)
			routeLocations, jumping := 0, 0
			for n, server := range servers {
				for _, location := range blocks(server, "    location @pf_r_") {
					if !regexp.MustCompile(`^    location @pf_r_[0-9]+(-uri)?(-once|-rt[0-9]*)? \{$`).MatchString(location[0]) {
						continue
					}
					routeLocations++
					// A retried route's location and its copies that take a next try send a failed try to the next, or
					// to @pf_fail, which answers it with the lines every other route location holds.
					if retryJumps(server, location) {
						jumping++
						if !slices.Contains(location, retryJump) || slices.Contains(location, "      error_page 502 = @pf_502$pf_grpc_call;") {
							return fmt.Sprintf("%q holds no %q in place of error_page 502 = @pf_502$pf_grpc_call", location[0], retryJump)
						}
						continue
					}
					if !slices.Contains(location, "      error_page 502 = @pf_502$pf_grpc_call;") {
						return fmt.Sprintf("%q holds no error_page 502 = @pf_502$pf_grpc_call", location[0])
					}
				}
				if fail := blocks(server, "    location @pf_fail {"); len(fail) != 1 || !slices.Contains(fail[0], "      error_page 502 = @pf_502$pf_grpc_call;") {
					return fmt.Sprintf("the server holds %q, want one location @pf_fail with error_page 502 = @pf_502$pf_grpc_call", fail)
				}
				name := server[slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    server_name ") })]
				// The 502 carries no Content-Type, as the Envoy output's carries none: nginx would give it default_type,
				// or the type of the path's extension.
				answer := []string{"    location @pf_502 {", `      default_type "";`, `      return 502 "$pf_empty";`, "    }"}
				if got := blocks(server, "    location @pf_502 {"); len(got) != 1 || !slices.Equal(got[0], answer) {
					return fmt.Sprintf("the server of %q holds %q, want %q", name, got, answer)
				}
				grpc := blocks(server, "    location @pf_502-grpc {")
				want := []string{"    location @pf_502-grpc {", "      default_type application/grpc;",
					"      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;",
					fmt.Sprintf("      add_header Access-Control-Allow-Origin $pf_acao%d always;", n+1),
					fmt.Sprintf("      add_header Access-Control-Expose-Headers $pf_aceh%d always;", n+1),
					"      add_header grpc-status 14 always;", `      return 200 "$pf_empty";`, "    }"}
				if len(grpc) != 1 || !slices.Equal(grpc[0], want) {
					return fmt.Sprintf("the server of %q holds %q, want %q", name, grpc, want)
				}
			}
			// A server with no Access-Control lines repeats the other two.
			want := []string{"    location @pf_502-grpc {", "      default_type application/grpc;",
				"      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;",
				"      add_header grpc-status 14 always;", `      return 200 "$pf_empty";`, "    }"}
			if got := blocks(lines(generate(t, string(testsupport.ReadFile(t, "../../examples/grpc/yaml/proxyfile.yaml")))),
				"    location @pf_502-grpc {"); len(got) != 1 || !slices.Equal(got[0], want) {
				return fmt.Sprintf("the gRPC example holds %q, want %q", got, want)
			}
			// 83 places of a route in each of four environments, each with a second location but the two with a
			// rewrite, a copy that tries once of each of the six locations of the three RPC methods, which the file
			// retries, and for each of the two retried routes the two copies that try once and the four that take a
			// next try: (83 * 2 - 2 + 6 + 2 * 6) * 4 = 728, of which the two retried routes' two locations and their four
			// copies that take a next try jump: 2 * 6 * 4 = 48.
			if routeLocations != 728 || jumping != 48 {
				return fmt.Sprintf("%d route locations of which %d jump to a next try, want 728 and 48", routeLocations, jumping)
			}
			return ""
		}},
		{"the hosts each environment admits a browser origin on", func() string {
			for _, line := range []string{
				`  map $pf_origin $pf_o1 { "" 0; "~," 1; "~^https?://api\\.example\\.com(:[0-9]+)?\\z" 0; "~^https?://api\\.example\\.org(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~*^https://example\\.com\\z" 0; "~*^https://www\\.example\\.com\\z" 0; "~^https://.*\\.example\\.com\\z" 0; default 1; }`,
				`  map $pf_origin $pf_o2 { "" 0; "~," 1; "~^https?://api\\.test\\.example\\.com(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~*^http://localhost:3000\\z" 0; "~*^https://test\\.example\\.com\\z" 0; "~^https://.*\\.test\\.example\\.com\\z" 0; default 1; }`,
				`  map $pf_origin $pf_o3 { "" 0; "~," 1; "~^https?://127\\.0\\.0\\.1(:[0-9]+)?\\z" 0; "~^https?://localhost(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~^http://(?>.*?\\.example\\.com:).*\\z" 0; "~^http://localhost:.*\\z" 0; default 1; }`,
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
				check := []string{fmt.Sprintf("    if ($pf_o%d) {", i+1), "      set $pf_route $pf_foreign_refusal;", "      break;", "    }"}
				at := slices.Index(server, check[0])
				firstLocation := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    location ") })
				if at < 0 || at+len(check) > len(server) || !slices.Equal(server[at:at+len(check)], check) || at > firstLocation {
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
			// Without cors and lists the answer is 200 with no body and no line of its own. Every one carries no
			// Content-Type, as the Envoy output's carries none, where nginx would send text/plain (measured on Envoy 1.40.2
			// beside nginx 1.30.6 and 1.31.8).
			plain := []string{"    location @pf_r_preflight {", `      default_type "";`, `      return 200 "$pf_empty";`, "    }"}
			answer := []string{"    location @pf_r_preflight {", "      add_header x-request-id $pf_answer_id always;",
				"      add_header Vary Origin always;", "      add_header Access-Control-Allow-Methods $http_access_control_request_method always;",
				"      add_header Access-Control-Allow-Headers $http_access_control_request_headers always;",
				"      add_header Access-Control-Max-Age 600 always;", `      default_type "";`, `      return 200 "$pf_empty";`, "    }"}
			// An environment with lists names the origin it admits, and with cors the headers it exposes.
			listed := func(n int) []string {
				return []string{"    location @pf_r_preflight {", "      add_header x-request-id $pf_answer_id always;",
					"      add_header Vary Origin always;", fmt.Sprintf("      add_header Access-Control-Allow-Origin $pf_acao%d always;", n),
					`      default_type "";`, `      return 200 "$pf_empty";`, "    }"}
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
			for _, name := range []string{"refused", "upgrade-required", "upgrade-refused-0", "upgrade-refused-1", "origin-refused", "no-route"} {
				location := blocks(live, "    location @pf_r_"+name+" {")
				if len(location) != 1 || !slices.Equal(location[0][1:3], []string{"      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;"}) ||
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
			// The Envoy output answers it 403 with the body Invalid CORS request as text/plain when the request carries an
			// Origin, and with no body and no Content-Type when it carries none, with or without cors, and closes the
			// connection after it, from an origin the origin check refuses too, where it keeps the connection after that
			// check's 403 to any other request (measured on Envoy 1.40.2): the route choice names the location by
			// $pf_has_origin, and the origin check by $pf_foreign_refusal.
			if broken := missing(out, "  map $pf_f_upgrade $pf_foreign_refusal { 1 upgrade-refused-1; default origin-refused; }"); broken != "" {
				return broken
			}
			for _, server := range environmentServers(out) {
				refused := blocks(server, "    location @pf_r_origin-refused {")
				if len(refused) != 1 || !slices.Equal(refused[0][len(refused[0])-2:], []string{`      return 403 "Invalid CORS request";`, "    }"}) ||
					slices.Contains(refused[0], "      keepalive_timeout 0;") || slices.Contains(refused[0], `      default_type "";`) {
					return fmt.Sprintf("an environment server's @pf_r_origin-refused is %q, want the 403's text, the type kept and the connection too", refused)
				}
				none, origin := blocks(server, "    location @pf_r_upgrade-refused-0 {"), blocks(server, "    location @pf_r_upgrade-refused-1 {")
				if len(none) != 1 || !slices.Equal(none[0][len(none[0])-4:], []string{"      keepalive_timeout 0;", `      default_type "";`, `      return 403 "$pf_empty";`, "    }"}) {
					return fmt.Sprintf("an environment server's @pf_r_upgrade-refused-0 is %q, want it to end closing the connection with no type and an empty 403", none)
				}
				if len(origin) != 1 || !slices.Equal(origin[0][len(origin[0])-3:], []string{"      keepalive_timeout 0;", `      return 403 "Invalid CORS request";`, "    }"}) ||
					slices.Contains(origin[0], `      default_type "";`) {
					return fmt.Sprintf("an environment server's @pf_r_upgrade-refused-1 is %q, want it to end closing the connection with the 403's text and keep the type", origin)
				}
			}
			if strings.Contains(out, "$pf_upgrade_refused") {
				return "the file still holds $pf_upgrade_refused"
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
				`  map "$server_protocol:$request_method:$http_content_type" $pf_f_method { "~^[^:]*:QUERY:\\z" 2; "~^HTTP/1\\.[0-9]:QUERY:[ \\t]+\\z" 2; "~^HTTP/2\\.0:" 0; "~^[^:]*:QUERY:" 0; "~^[^:]*:(?:ACL|BIND|CHECKOUT|CONNECT|COPY|DELETE|GET|HEAD|LINK|LOCK|MERGE|MKACTIVITY|MKCALENDAR|MKCOL|MOVE|MSEARCH|NOTIFY|OPTIONS|PATCH|POST|PROPFIND|PROPPATCH|PURGE|PUT|REBIND|REPORT|SEARCH|SOURCE|SUBSCRIBE|TRACE|UNBIND|UNLINK|UNLOCK|UNSUBSCRIBE):" 0; default 1; }`,
				`  map $http_connection $pf_f_connection { "" 0; "~*^,*(?:(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)(?:,+(?:[ \\t]*(?:close|keep-alive|upgrade|http2-settings|te)[ \\t]*|[ \\t]+)){0,8})?,*\\z" 0; default 1; }`,
				`  map "$server_protocol:$request_uri" $pf_f_raw { "~^HTTP/2\\.0:" 0; "~[\\x00-\\x08\\x0A\\x0B\\x0D-\\x20\\x7F-\\xFF]" 1; default 0; }`,
				`  map "$server_protocol:$http_connection$http_keep_alive$http_proxy_connection$http_transfer_encoding$http_upgrade" $pf_f_h2_connection { "~^HTTP/2\\.0:." 1; default 0; }`,
				`  map "$server_protocol:$http_te" $pf_f_h2_te { "~^HTTP/2\\.0:(?!trailers\\z)." 1; default 0; }`,
				`  map $server_protocol $pf_f_version { "~^HTTP/[0-9]\\.[0-9]\\z" 0; default 1; }`,
				`  map "$pf_f_method$pf_f_connection$pf_f_raw$pf_f_h2_connection$pf_f_h2_te$pf_f_authority$pf_f_names$pf_f_version" $pf_f_request { "~1" 1; default 0; }`,
				`  map "$pf_f_method$pf_f_fragment" $pf_f_refused { "~^2|1\\z" 1; default 0; }`,
				`  map "$pf_f_request$pf_f_refused$pf_f_426" $pf_refusal { "~^1" bad-request; "~^01" refused; "~^001" upgrade-required; default 0; }`,
				`  map "$pf_reached:$pf_refusal" $pf_answer_id { "~^1:0\\z" $pf_request_id; default ""; }`,
				`  map $server_protocol $pf_f_426 { "~^HTTP/(?:0\\.9|1\\.[02-9])\\z" 1; default 0; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			// Envoy answers what its parser refuses 400 with the body Bad Request as text/plain, which @pf_r_bad-request
			// answers, a QUERY with no Content-Type or a # in the target 400 with no body and no Content-Type, which
			// @pf_r_refused answers, and HTTP/1.0 and HTTP/1.2 to HTTP/1.9 426 with the body Upgrade Required as
			// text/plain, which @pf_r_upgrade-required answers, each closing the connection.
			// The default server, whose location / reads $pf_refusal itself, sets no route.
			routed := []string{"    if ($pf_refusal) {", "      set $pf_route $pf_refusal;", "      break;", "    }"}
			unrouted := []string{"    if ($pf_refusal) {", "      break;", "    }"}
			servers := blocks(lines(out), "  server {")
			// The default server and four environment servers; the admin server answers on 127.0.0.1 alone.
			checked := 0
			for _, server := range servers {
				if !slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    listen 10000") }) {
					continue
				}
				checked++
				checks := routed
				if slices.Contains(server, "    listen 10000 ssl default_server;") {
					checks = unrouted
				}
				at := slices.Index(server, checks[0])
				firstOther := slices.IndexFunc(server, func(line string) bool {
					return strings.HasPrefix(line, "    location ") || (strings.HasPrefix(line, "    if (") && !slices.Contains(checks, line))
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
		{"a host no environment names is answered 404 with no Content-Type", func() string {
			// The Envoy output answers it 404 with no body and no Content-Type, where nginx would send text/plain, and,
			// whatever the host, what its parser refuses 400 with the body Bad Request as text/plain, a QUERY with no
			// Content-Type or a # in the target 400 with no body and no Content-Type, and HTTP/1.0 and HTTP/1.2 to
			// HTTP/1.9 426 with the body Upgrade Required as text/plain, closing the connection after each (measured on
			// Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8).
			want := [][]string{
				{"    location / {", "      return 418;", "      error_page 418 = @pf_default-$pf_refusal;", "    }"},
				{"    location @pf_default-0 {", `      default_type "";`, `      return 404 "$pf_empty";`, "    }"},
				{"    location @pf_default-refused {", "      keepalive_timeout 0;", `      default_type "";`, `      return 400 "$pf_empty";`, "    }"},
				{"    location @pf_default-bad-request {", "      keepalive_timeout 0;", "      default_type text/plain;",
					`      return 400 "Bad Request";`, "    }"},
				{"    location @pf_default-upgrade-required {", "      keepalive_timeout 0;", "      default_type text/plain;",
					`      return 426 "Upgrade Required";`, "    }"},
			}
			for _, server := range blocks(lines(out), "  server {") {
				if slices.Contains(server, "    listen 10000 ssl default_server;") {
					for _, block := range want {
						if got := blocks(server, block[0]); len(got) != 1 || !slices.Equal(got[0], block) {
							return fmt.Sprintf("the default server holds %q, want %q", got, block)
						}
					}
					// Envoy 1.40.2 sends vary: Origin on every answer to such a host, its 404, both 400s and its 426, and
					// x-request-id on its 404 alone, which the server's lines give them: no location of the server writes an
					// add_header of its own, and $pf_answer_id is empty for the 400s and the 426 and for a request nginx
					// refuses before the server's rules set $pf_reached.
					var added []string
					for _, line := range server {
						if strings.Contains(line, "add_header ") {
							added = append(added, line)
						}
					}
					if want := []string{"    add_header x-request-id $pf_answer_id always;", "    add_header Vary Origin always;"}; !slices.Equal(added, want) {
						return fmt.Sprintf("the default server's add_header lines are %q, want %q", added, want)
					}
					return ""
				}
			}
			return "no default server"
		}},
		{"a host with a dot at its end is on no environment", func() string {
			if broken := missing(out, `  map $pf_authority $pf_f_host_dot { "~\\.(?::[0-9]*)?\\z" 1; default 0; }`); broken != "" {
				return broken
			}
			// The Envoy output answers it 404 with no body and no Content-Type before its origin check (measured on Envoy
			// 1.40.2): the check names @pf_r_no-route and ends the server's rewrite lines, so the origin check after it
			// never runs.
			for _, server := range environmentServers(out) {
				check := []string{"    if ($pf_f_host_dot) {", "      set $pf_route no-route;", "      break;", "    }"}
				at := slices.Index(server, check[0])
				origin := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    if ($pf_o") })
				if at < 0 || !slices.Equal(server[at:at+len(check)], check) || at > origin {
					return fmt.Sprintf("an environment server holds no %q before its origin check", check)
				}
			}
			return ""
		}},
		{"a request no route takes is answered 404 with no Content-Type", func() string {
			// Measured on a file with no / route, on Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8. The host-dot check and
			// the X-Forwarded-Proto check name the same location.
			want := []string{"    location @pf_r_no-route {", "      add_header x-request-id $pf_answer_id always;",
				"      add_header Vary Origin always;", `      default_type "";`, `      return 404 "$pf_empty";`, "    }"}
			for _, server := range environmentServers(out) {
				if got := blocks(server, "    location @pf_r_no-route {"); len(got) != 1 || !slices.Equal(got[0], want) {
					return fmt.Sprintf("an environment server holds %q, want %q", got, want)
				}
			}
			return ""
		}},
		{"every public server sets $pf_reached before its first if", func() string {
			// $pf_answer_id and the CORS maps read it: only the server's rewrite phase sets it, so a request nginx's own
			// parser refuses before that phase leaves it empty. The default server and every environment server, with
			// cors and lists and without.
			for _, file := range []string{out, noCORS} {
				for _, server := range blocks(lines(file), "  server {") {
					if !slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    listen 10000") }) {
						continue
					}
					set := slices.Index(server, "    set $pf_reached 1;")
					first := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    if (") })
					if set < 0 || set > first || slices.Index(server[set+1:], "    set $pf_reached 1;") >= 0 {
						return fmt.Sprintf("a server sets $pf_reached at line %d, want once before its first if at line %d", set, first)
					}
				}
			}
			return ""
		}},
		{"what Envoy's parser refuses is answered 400 Bad Request and the connection closed", func() string {
			// Envoy 1.40.2 answers an unknown method, ten Connection tokens, a byte above 0x7F in the target, an empty port,
			// a header name outside the token characters, a control byte in a header value and HTTP/1.10 with the same
			// answer, 400 with the body Bad Request as text/plain, and closes the connection, where nginx's return in a
			// server's if kept it and sent no body (measured beside nginx 1.30.6 and 1.31.8).
			want := []string{"    location @pf_r_bad-request {", "      add_header x-request-id $pf_answer_id always;",
				"      add_header Vary Origin always;", "      keepalive_timeout 0;", "      default_type text/plain;",
				`      return 400 "Bad Request";`, "    }"}
			for _, server := range environmentServers(out) {
				if got := blocks(server, "    location @pf_r_bad-request {"); len(got) != 1 || !slices.Equal(got[0], want) {
					return fmt.Sprintf("an environment server holds %q, want %q", got, want)
				}
			}
			return ""
		}},
		{"an answer of nginx's own takes no type from the path's ending", func() string {
			// nginx's own types give a path ending .html text/html and .jpg image/jpeg (measured on nginx 1.30.6 and
			// 1.31.8), where the Envoy output's answers carry text/plain or none; a server's if cannot hold a types block.
			if at := strings.Index(out, "\n  types {}\n"); at < 0 || at > firstServer {
				return "no line \"  types {}\" before the first server"
			}
			if n := strings.Count(out, "types {"); n != 1 {
				return fmt.Sprintf("%d types blocks, want the http block's alone", n)
			}
			return ""
		}},
		{"a request's body, header names and silences are Envoy's", func() string {
			for _, line := range []string{
				"  proxy_request_buffering off;", "  underscores_in_headers on;", "  ignore_invalid_headers off;",
				"  client_header_timeout 5m;", "  client_body_timeout 5m;", "  send_timeout 5m;", "  proxy_send_timeout 5m;",
				"  grpc_send_timeout 5m;", "  keepalive_timeout 1h;",
			} {
				if at := strings.Index(noTimeouts, "\n"+line+"\n"); at < 0 || at > strings.Index(noTimeouts, "\n  server {\n") {
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
			own := slices.Concat(refused[:3], []string{"      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;"},
				refused[3:6], []string{"      add_header x-request-id $pf_answer_id always;", "      add_header Vary Origin always;"}, refused[6:])
			alone := slices.Concat(refused[:1], refused[2:5])
			aloneOwn := slices.Concat(refused[:1], refused[2:3], []string{"      add_header x-request-id $pf_answer_id always;",
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
		{"the header limits", func() string {
			// Without limits.request_headers, Envoy's own limits: a header block of 60 KiB and 100 fields. nginx reads a
			// request into a first buffer of 64 bytes and moves the line that does not fit, with the lines after it, into
			// a large buffer, never splitting a line, so the head stays within the size: one buffer of the size less 64
			// bytes up to 65,600 bytes, and above it as many buffers of at most 64 KiB as the size needs.
			for _, want := range []struct {
				file  string
				lines []string // lines of the http block
			}{
				{out, []string{"  max_headers 100;", "  client_header_buffer_size 64;", "  large_client_header_buffers 1 61376;", `  map "" $pf_header_count { default 100; }`}},
				{headerLimits("4KiB"), []string{"  max_headers 20;", "  client_header_buffer_size 64;", "  large_client_header_buffers 1 4032;", `  map "" $pf_header_count { default 20; }`}},
				{headerLimits("96KiB"), []string{"  large_client_header_buffers 2 49120;"}},
				{headerLimits("8MiB"), []string{"  large_client_header_buffers 128 65535;"}},
			} {
				first := strings.Index(want.file, "\n  server {\n")
				for _, line := range want.lines {
					if at := strings.Index(want.file, "\n"+line+"\n"); at < 0 || at > first {
						return fmt.Sprintf("no line %q before the first server", line)
					}
				}
			}
			return ""
		}},
		{"a long Origin", func() string {
			// An Origin over 8,192 bytes is answered as one no list takes, before an origin pattern reads it, in each map
			// that runs the patterns: the origin check ($pf_o<n>, 1) and the CORS answer ($pf_c<n>, 0).
			for _, fragment := range []string{
				`"~^https?://api\\.example\\.org(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~*^https://example\\.com\\z" 0;`,
				`  map $pf_origin $pf_c1 { "~," 0; "~(?s)\\A.{8193}" 0; "~*^https://example\\.com\\z" 1;`,
				`"~^https?://api\\.test\\.example\\.com(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~*^http://localhost:3000\\z" 0;`,
				`  map $pf_origin $pf_c2 { "~," 0; "~(?s)\\A.{8193}" 0; "~*^http://localhost:3000\\z" 1;`,
				`"~^https?://localhost(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~^http://(?>.*?\\.example\\.com:).*\\z" 0;`,
				`  map $pf_origin $pf_c3 { "~," 0; "~(?s)\\A.{8193}" 0; "~^http://(?>.*?\\.example\\.com:).*\\z" 1;`,
				`"~^https?://patterns\\.example\\.net(:[0-9]+)?\\z" 0; "~(?s)\\A.{8193}" 1; "~^https://(?>.*?\\.).*\\.example1\\.org\\z" 0;`,
				`  map $pf_origin $pf_c4 { "~," 0; "~(?s)\\A.{8193}" 0; "~^https://(?>.*?\\.).*\\.example1\\.org\\z" 1;`,
			} {
				if !strings.Contains(out, fragment) {
					return fmt.Sprintf("the file holds no %q", fragment)
				}
			}
			// The test environment with origins and no pattern reads no Origin's length.
			originsAlone := generate(t, rewritten(t, battery, testLists, "  test:\n    hosts: [api.test.example.com]\n    origins: [\"http://localhost:3000\"]\n"))
			for _, line := range []string{
				`  map $pf_origin $pf_o2 { "" 0; "~," 1; "~^https?://api\\.test\\.example\\.com(:[0-9]+)?\\z" 0; "~*^http://localhost:3000\\z" 0; default 1; }`,
				`  map $pf_origin $pf_c2 { "~," 0; "~*^http://localhost:3000\\z" 1; default 0; }`,
			} {
				if broken := missing(originsAlone, line); broken != "" {
					return broken
				}
			}
			return ""
		}},
		{"an empty X-Forwarded-Proto", func() string {
			// With client_address forwarded, or not written in a file without tls, the Envoy output answers an
			// X-Forwarded-Proto line that is present and empty 404 before it reads the host or the origin, where nginx's
			// $http_x_forwarded_proto reads it as none and would send the service the connection's scheme. Every server
			// refuses it after the request checks, and a refused request carries no Access-Control line.
			proto := []string{"    if ($pf_f_proto) {", "      set $pf_route no-route;", "      break;", "    }"}
			for _, file := range []string{forwarded, unset} {
				for _, line := range []string{"  js_set $pf_js_proto pf.forwardedProto;", `  map $pf_js_proto $pf_f_proto { "0" 0; default 1; }`} {
					if at := strings.Index(file, "\n"+line+"\n"); at < 0 || at > strings.Index(file, "\n  server {\n") {
						return fmt.Sprintf("no line %q before the first server", line)
					}
				}
				checked := 0
				for _, server := range blocks(lines(file), "  server {") {
					if !slices.ContainsFunc(server, func(line string) bool { return strings.HasPrefix(line, "    listen 10000") }) {
						continue
					}
					checked++
					// The default server's refusal check is three lines, and its check sets no route.
					want, after := proto, 4
					if slices.ContainsFunc(server, func(line string) bool { return strings.HasSuffix(line, " default_server;") }) {
						want, after = []string{"    if ($pf_f_proto) {", "      break;", "    }"}, 3
					}
					at := slices.Index(server, "    if ($pf_refusal) {") + after
					firstLocation := slices.IndexFunc(server, func(line string) bool { return strings.HasPrefix(line, "    location ") })
					if at < after || at+len(want) > len(server) || !slices.Equal(server[at:at+len(want)], want) || at > firstLocation {
						return fmt.Sprintf("a server holds no %q right after its request checks", want)
					}
				}
				if checked != 5 {
					return fmt.Sprintf("%d servers on the listener, want 5", checked)
				}
			}
			if broken := missing(forwarded, `  map "$pf_reached:$pf_f_request$pf_f_426$pf_f_host_dot$pf_f_proto:$pf_route:$pf_c1" $pf_cors1 { "~^1:0000:(?:[0-9]+(?:-uri)?|preflight):1\\z" 1; default 0; }`); broken != "" {
				return broken
			}
			// With client_address connection the Envoy output sends the connection's scheme whatever the client wrote,
			// and with the header removed it reads no client copy, so neither file reads it.
			for _, file := range []string{out, forwardedRemoved} {
				for _, text := range []string{"pf_f_proto", "forwardedProto"} {
					if strings.Contains(file, text) {
						return fmt.Sprintf("a file that reads no client X-Forwarded-Proto holds %q", text)
					}
				}
			}
			return ""
		}},
		{"a 504 from the idle time", func() string {
			// A service that sends nothing for the read time is answered with the Envoy output's text: "stream timeout"
			// when timeouts.idle is the read time, and when Envoy's 5 minutes of silence are, "upstream request timeout"
			// when the route's 15 s are. A service that does not take the connection in the cluster's 5 s is answered 502
			// with no body, as the Envoy output answers it, wherever the connect time is those 5 s, through @pf_502 so a
			// gRPC call gets the gRPC answer; under idle: 2s Envoy's idle timer ends it first, with its text. With idle
			// and request none nothing but the connection is timed. The check reads the last try's connect time, the
			// value after the last space, as nginx lists one for each try: "-, -" for a retried call whose two tries
			// timed out connecting, and "- : -" for a retried plain route's request that went on to a next backend.
			idleText := []string{"    location @pf_504 {", `      return 504 "stream timeout";`, "    }"}
			checked := func(answer string) []string {
				return []string{"    location @pf_504 {", "      error_page 502 = @pf_502$pf_grpc_call;",
					`      if ($upstream_connect_time ~ "(?:^| )-\\z") {`, "        return 502;", "      }", answer, "    }"}
			}
			// A retried route's location and its copies that take a next try send a failed try to the next, or to
			// @pf_fail, which holds the two lines every other route location holds, its copies that try once among them.
			routeLocation := regexp.MustCompile(`^    location @pf_r_[0-9]+(-uri)?(-once|-rt[0-9]*)? \{$`)
			twoLines := func(location []string) bool {
				at := slices.Index(location, "      error_page 502 = @pf_502$pf_grpc_call;")
				return at >= 0 && at+1 < len(location) && location[at+1] == "      error_page 504 = @pf_504;"
			}
			for _, want := range []struct {
				file   string
				answer []string // the server's @pf_504 location
				copies int      // the copies that take a next try of each location of a retried route, by its timers
			}{
				{withTimeouts("{idle: 2s}"), idleText, 2},
				{withTimeouts("{idle: 10s}"), checked(`      return 504 "stream timeout";`), 6},
				{withTimeouts("{request: none, idle: 6m}"), checked(`      return 504 "stream timeout";`), 6},
				{noTimeouts, checked(`      return 504 "upstream request timeout";`), 6},
				{withTimeouts("{idle: 90s}"), checked(`      return 504 "upstream request timeout";`), 6},
				{withTimeouts("{request: none}"), checked(`      return 504 "stream timeout";`), 6},
				{withTimeouts("{request: none, idle: none}"), checked("      return 504;"), 1},
			} {
				servers, locations, jumping := 0, 0, 0
				for _, server := range environmentServers(want.file) {
					servers++
					at := slices.Index(server, want.answer[0])
					if at < 0 || at+len(want.answer) > len(server) || !slices.Equal(server[at:at+len(want.answer)], want.answer) {
						return fmt.Sprintf("a server holds no %q", want.answer)
					}
					if fail := blocks(server, "    location @pf_fail {"); len(fail) != 1 || !twoLines(fail[0]) {
						return fmt.Sprintf("the server holds %q, want one location @pf_fail with error_page 504 = @pf_504 after its error_page 502", fail)
					}
					for _, location := range blocks(server, "    location @pf_r_") {
						if !routeLocation.MatchString(location[0]) {
							continue
						}
						locations++
						switch jumps := retryJumps(server, location); {
						case jumps && (!slices.Contains(location, retryJump) || slices.Contains(location, "      error_page 504 = @pf_504;")):
							return fmt.Sprintf("%s holds no %q in place of error_page 502 and 504", location[0], retryJump)
						case jumps:
							jumping++
						case !twoLines(location):
							return fmt.Sprintf("%s holds no error_page 504 = @pf_504 after its error_page 502", location[0])
						}
					}
				}
				// 83 places of a route in each of four environments, each with a second location but the two with a
				// rewrite, a copy that tries once of each of the six locations of the three RPC methods, which the file
				// retries, and for each of the two retried routes the two copies that try once:
				// (83 * 2 - 2 + 6 + 2 * 2) * 4 = 696, and the copies that take a next try of the two retried routes' two
				// locations, 2 * 2 * copies * 4; those two locations and their copies jump: 2 * 2 * (1 + copies) * 4.
				all, jumps := 696+2*2*want.copies*4, 2*2*(1+want.copies)*4
				if servers != 4 || locations != all || jumping != jumps {
					return fmt.Sprintf("%d servers, %d route locations of which %d jump to a next try, want 4, %d and %d", servers,
						locations, jumping, all, jumps)
				}
			}
			return ""
		}},
		{"the admin server", func() string {
			// The admin server, on 127.0.0.1 alone, answers every method on /ready, /stats and /server_info as Envoy's
			// admin answers it: what Envoy's parser refuses 400 Bad Request as text/plain, a QUERY with no Content-Type or
			// a # in the target 400 with no body and no Content-Type, and HTTP/1.0 426 with Upgrade Required as
			// text/plain, each closing the connection, then a query 404 before a location reads the path; /stats asks stub_status again with GET, as error_page does, for a
			// method stub_status refuses with 405, and nginx's own 405 to TRACE has no body (measured on Envoy 1.40.2
			// beside nginx 1.30.6 and 1.31.8 with GET, HEAD, POST, PUT, DELETE, PATCH, OPTIONS, TRACE and FOO, and with each
			// kind of request flag). No public server serves stub_status.
			if broken := missing(out, `  map $request_uri $pf_admin_query { "~\\?" 1; default 0; }`); broken != "" {
				return broken
			}
			want := []string{
				"  server {",
				"    listen 127.0.0.1:9901;",
				"    access_log off;",
				"    error_page 405 = @pf_admin_405;",
				"    if ($pf_refusal) {",
				"      rewrite ^ / break;",
				"    }",
				"    if ($pf_admin_query) {",
				`      return 404 "$pf_empty";`,
				"    }",
				"    location = /ready {",
				"      default_type text/plain;",
				`      return 200 "LIVE\n";`,
				"    }",
				"    location = /stats {",
				"      stub_status;",
				"      error_page 405 = /stats;",
				"    }",
				"    location = /server_info {",
				"      default_type application/json;",
				`      return 200 '{"version":"$nginx_version"}\n';`,
				"    }",
				"    location / {",
				"      return 418;",
				"      error_page 418 = @pf_admin-$pf_refusal;",
				"    }",
				"    location @pf_admin-0 {",
				`      return 404 "$pf_empty";`,
				"    }",
				"    location @pf_admin-refused {",
				"      keepalive_timeout 0;",
				`      default_type "";`,
				`      return 400 "$pf_empty";`,
				"    }",
				"    location @pf_admin-bad-request {",
				"      keepalive_timeout 0;",
				"      default_type text/plain;",
				`      return 400 "Bad Request";`,
				"    }",
				"    location @pf_admin-upgrade-required {",
				"      keepalive_timeout 0;",
				"      default_type text/plain;",
				`      return 426 "Upgrade Required";`,
				"    }",
				"    location @pf_admin_405 {",
				`      return 405 "$pf_empty";`,
				"    }",
				"  }",
			}
			var admins [][]string
			for _, server := range blocks(lines(out), "  server {") {
				if slices.Contains(server, "    listen 127.0.0.1:9901;") {
					admins = append(admins, server)
				} else if slices.Contains(server, "      stub_status;") {
					return "a public server serves stub_status"
				}
			}
			if len(admins) != 1 {
				return fmt.Sprintf("%d admin servers, want 1", len(admins))
			}
			if !slices.Equal(admins[0], want) {
				return fmt.Sprintf("the admin server is\n%s\nwant\n%s", strings.Join(admins[0], "\n"), strings.Join(want, "\n"))
			}
			return ""
		}},
		{"the log line", func() string {
			// A log line holds no request line, path or query, which can carry a token: its format reads the variables
			// of its own maps and five of nginx's, compared by whole name. A request id or Host holding a byte above 0x7F
			// is logged empty, so every line stays UTF-8, and the admin server writes no line, as Envoy's admin writes
			// none.
			own := []string{"request_length", "body_bytes_sent", "request_method", "time_iso8601"}
			format := ""
			for _, line := range lines(out) {
				if strings.HasPrefix(line, "  log_format pf_request escape=json '") {
					format = line
				}
			}
			if format == "" {
				return "no log_format pf_request escape=json"
			}
			for _, name := range variable.FindAllStringSubmatch(format, -1) {
				if !strings.HasPrefix(name[1], "pf_") && !slices.Contains(own, name[1]) {
					return fmt.Sprintf("the log format reads $%s", name[1])
				}
			}
			// The status is a number with no leading zero, as Envoy writes it, where nginx's $status has three digits:
			// 009 for an answer nginx reads with no status line, which Envoy logs 2000 or 502, and 000 for an HTTP/2
			// stream a client cut in its headers, which Envoy logs 0 (measured on nginx 1.30.6 and 1.31.8 beside Envoy
			// 1.40.2). Neither is a number a JSON reader takes.
			code := regexp.MustCompile(`(?m)^  map \$status \$pf_log_code \{ "~(.*)" \$pf_log_c; \}$`).FindStringSubmatch(out)
			if code == nil {
				return "no map of $status to $pf_log_code"
			}
			// The key read as nginx reads it, the quoted string's \\ as one \, then run on $status as nginx writes it.
			key := regexp.MustCompile(strings.ReplaceAll(code[1], `\\`, `\`))
			for status, want := range map[string]string{"000": "0", "009": "9", "200": "200", "404": "404"} {
				if got := key.FindStringSubmatch(status); got == nil || got[key.SubexpIndex("pf_log_c")] != want {
					return fmt.Sprintf("$status %s logs %q, want %s", status, got, want)
				}
			}
			for _, field := range []string{`"request_id":"$pf_log_request_id"`, `"host":"$pf_log_host"`, `"status":$pf_log_code,`} {
				if !strings.Contains(format, field) {
					return fmt.Sprintf("the log format holds no %s", field)
				}
			}
			for _, line := range []string{
				`  map $pf_request_id $pf_log_request_id { "~[\\x80-\\xFF]" ""; default $pf_request_id; }`,
				`  map $pf_authority $pf_log_host { "~[\\x80-\\xFF]" ""; default $pf_authority; }`,
			} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			// With no log every request is logged.
			if broken := missing(generate(t, rewritten(t, battery, batteryLog, "")), "  access_log /dev/stdout pf_request;"); broken != "" {
				return broken
			}
			// The battery file's own log, {status_from: 400, sample: 1%}; TestLogStatuses holds the statuses each
			// status_from takes.
			for _, line := range []string{"  split_clients $pf_request_id $pf_log_sampled { 1% 1; * 0; }",
				`  map "$pf_log_status$pf_log_sampled$pf_log_flagged" $pf_log { "000" 0; default 1; }`,
				// A response the Envoy output flags is logged below status_from too: nginx's own answers but its preflight
				// 200 and its 403s, which Envoy answers with no flag, and every failure of a service. A request a retry
				// served is not flagged, as its last try's header time says.
				`  map "$pf_last_header:$status:$pf_log_cut" $pf_log_flagged { "~:UPE\\z" 1; "~^[0-9][^:]*:(?!499:)" 0; "~^:(?:200|403):" 0; default 1; }`,
				"  access_log /dev/stdout pf_request if=$pf_log;"} {
				if broken := missing(out, line); broken != "" {
					return broken
				}
			}
			// The log reads the last try of a request nginx tried again, as the Envoy output logs the try that answered:
			// nginx writes a value of each $upstream_ variable for each try, joined by ", " within an upstream and by
			// " : " across the locations a retried route's request goes through, and no value holds a space.
			// upstream_ms is the last try's header time. A file with no retried route writes the same maps.
			for _, file := range []string{out, notRetried("{idle: 2s, upstream_idle: 3s}")} {
				for _, line := range []string{
					`  map $upstream_status $pf_last_status { "~(?:^| )(?<pf_l1>[^ ]+)\\z" $pf_l1; default ""; }`,
					`  map $upstream_connect_time $pf_last_connect { "~(?:^| )(?<pf_l1>[^ ]+)\\z" $pf_l1; default ""; }`,
					`  map $upstream_header_time $pf_last_header { "~(?:^| )(?<pf_l1>[^ ]+)\\z" $pf_l1; default ""; }`,
					`  map $upstream_bytes_received $pf_last_received { "~(?:^| )(?<pf_l1>[^ ]+)\\z" $pf_l1; default ""; }`,
					`  map $upstream_response_length $pf_last_length { "~(?:^| )(?<pf_l1>[^ ]+)\\z" $pf_l1; default ""; }`,
					`  map $pf_last_header $pf_log_upstream_ms { "~^0\\.0*(?<pf_log_u1>[0-9]+)\\z" $pf_log_u1; "~^(?<pf_log_u2>[0-9]+)\\.(?<pf_log_u3>[0-9]{3})\\z" $pf_log_u2$pf_log_u3; default null; }`,
				} {
					if broken := missing(file, line); broken != "" {
						return broken
					}
				}
			}
			byStatus := withLog("{status_from: 404, sample: 0%}")
			if strings.Contains(byStatus, "split_clients") {
				return "sample: 0% writes split_clients, which nginx refuses at 0%"
			}
			if broken := missing(byStatus, `  map "$pf_log_status$pf_log_flagged" $pf_log { "00" 0; default 1; }`); broken != "" {
				return broken
			}
			if broken := missing(withLog("{status_from: 500, sample: 100%}"), "  access_log /dev/stdout pf_request;"); broken != "" {
				return broken
			}
			for _, server := range blocks(lines(out), "  server {") {
				if slices.Contains(server, "    listen 127.0.0.1:9901;") && !slices.Contains(server, "    access_log off;") {
					return "the admin server writes log lines"
				}
			}
			// One location of the first route in each of the battery file's four environments.
			first := blocks(lines(out), "    location @pf_r_1 {")
			if len(first) != 4 {
				return fmt.Sprintf("%d locations @pf_r_1, want 4", len(first))
			}
			for _, location := range first {
				if len(location) < 3 || location[1] != `      set $pf_log_route "accounts-events";` || location[2] != `      set $pf_log_upstream "accounts_http";` {
					return fmt.Sprintf("%q does not name its route and upstream first", location[0])
				}
			}
			return ""
		}},
		{"the upstream-failure line", func() string {
			// A service that fails before its answer, or cuts an answer it started with a content-length, writes the
			// Envoy output's second line with the flag Envoy gives it; a timeout Envoy counts as its idle timer's writes
			// none, as Envoy writes none for it. A 502 after the connection opened is UC when the service sent nothing
			// and UPE when it sent an answer nginx could not read, as Envoy flags them. Each key reads the last try.
			cut := `  map "$request_method:$pf_last_status:$pf_last_length:$upstream_http_content_length:$request_completion:$pf_log_upstream" $pf_log_cut { "~^HEAD:" ""; "~^[A-Z]+:(?:1[0-9][0-9]|204|304):" ""; "~^[A-Z]+:[0-9]+:[^:]*:[0-9]+::[^:]*_grpc\\z" UPE; "~_grpc\\z" ""; "~^[A-Z]+:[0-9]+:(?<pf_log_got>[0-9]+):(?!\\k<pf_log_got>:)[0-9]+:" UPE; default ""; }`
			failure := `  map "$pf_log_retried:$pf_last_status:$pf_last_connect:$pf_last_header:$pf_log_cut:$pf_last_received" $pf_log_failure { "~:UPE:" UPE; "~^1:502:-:" URX,UF; "~^[01]:502:-:" UF; "~^[01]:502:[0-9][^:]*:-::0\\z" UC; "~^[01]:502:[0-9][^:]*:-:" UPE; `
			connect, read := `"~^1:504:-:" URX,UF; "~^[01]:504:-:" UF; `, `"~^[01]:504:[0-9][^:]*:-:" UT; `
			// In a file with a retried route the line is written by $pf_log_flags, which a request a retried route
			// location took (a jump count in $pf_rt_cap, not the "" of -once or of a route with no retries) reads from
			// $pf_log_retry_flags, and any other from $pf_log_failure, so the retry maps are read for the first alone
			// (TestRetryLogReadsRetriesOnlyWhenRetried). $pf_log_retry_flags flags a request retries ran out on URX with
			// its last try's flag: one whose last try sent the service nothing, with a body that fits, and no copy left
			// to take the next. One the route's time ended is flagged as the timer that ended it: UT for the route's
			// 15 s, and no line for the idle timer, as Envoy writes none for it. Any other request keeps the failure
			// line's own flag.
			picked := `  map $pf_rt_cap $pf_log_flags { "" $pf_log_failure; default $pf_log_retry_flags; }`
			flags := `  map "$pf_rt_cap|$pf_rt_unsent$pf_rt_fits|$pf_rt_target|$pf_log_failure" $pf_log_retry_flags { `
			ranOut := `"~^[0-6]\\|11\\|[^|]*\\|(?<pf_lf>[^|]+)\\z" URX,$pf_lf; default $pf_log_failure; }`
			spent := `"~^[^|]*\\|[01][01]\\|rt_spent(?:-grpc)?\\|" `
			for _, want := range []struct{ name, file, failure, flags string }{
				{"no timeouts", notRetried(""), failure + connect + read + `default ""; }`, ""},
				{"idle 2s", notRetried("{idle: 2s}"), failure + `default ""; }`, ""},
				{"idle 10s", notRetried("{idle: 10s}"), failure + connect + `default ""; }`, ""},
				{"retried, no timeouts", retriedShop(""), failure + connect + read + `default ""; }`, flags + spent + "UT; " + ranOut},
				{"retried, idle 2s", retriedShop("{idle: 2s}"), failure + `default ""; }`, flags + spent + `""; ` + ranOut},
				{"retried, idle 10s", retriedShop("{idle: 10s}"), failure + connect + `default ""; }`, flags + spent + `""; ` + ranOut},
				{"retried, no timer", retriedShop("{request: none, idle: none}"), failure + connect + `default ""; }`, flags + ranOut},
			} {
				written := "$pf_log_failure"
				if want.flags != "" {
					written = "$pf_log_flags"
				} else if strings.Contains(want.file, "$pf_log_flags") {
					return want.name + ": a file with no retried route names $pf_log_flags, which reads the retry maps it does not declare"
				}
				for _, line := range []string{
					`  log_format pf_upstream_failure escape=json '{"event":"gateway_upstream_failure","flags":"` + written + `","route":"$pf_log_route","status":$pf_log_code,"upstream":"$pf_log_upstream"}';`,
					"  access_log /dev/stdout pf_upstream_failure if=" + written + ";", want.failure, cut,
				} {
					if broken := missing(want.file, line); broken != "" {
						return want.name + ": " + broken
					}
				}
				if want.flags == "" {
					continue
				}
				for _, line := range []string{picked, want.flags} {
					if broken := missing(want.file, line); broken != "" {
						return want.name + ": " + broken
					}
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
// battery file has 80 HTTP routes and three RPC methods in each of four environments, each with a second location but
// the two HTTP routes with a rewrite, each of the two retried HTTP routes with the two copies that try once and the
// four that take a next try, and each of the six locations of the RPC methods, which it retries, with a copy that tries
// once: (80 * 2 - 2 + 2 * 6) * 4 = 680 HTTP and 6 * 2 * 4 = 48 gRPC locations. A row on fewer locations fails.
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
	if kinds["proxy_set_header"] != 680 || kinds["grpc_set_header"] != 48 {
		return fmt.Sprintf("%d HTTP and %d gRPC route locations, want 680 and 48", kinds["proxy_set_header"], kinds["grpc_set_header"])
	}
	return ""
}

// retryJump is the last line of a retried route's location and of each of its copies that take a next try: a try that
// failed goes to the next copy, or to @pf_fail when none is left or the try reached the service.
const retryJump = "      error_page 502 504 = @pf_$pf_rt_target;"

// retryJumps reports whether location, a route location of server, is a retried route's location or one of its copies
// that take a next try: a copy is named -rt or -rt<k>, and the location it copies has a copy named -rt in the server.
func retryJumps(server, location []string) bool {
	name := strings.TrimSuffix(location[0], " {")
	return regexp.MustCompile(`-rt[0-9]*$`).MatchString(name) || slices.Contains(server, name+"-rt {")
}

// corsLine is the battery file's cors key, examples/full's.
const corsLine = `cors: {allow_methods: "*", allow_headers: "*", expose_headers: "Content-Disposition,X-Request-Id", max_age: "600"}` + "\n"

// tlsLines is the battery file's tls key, examples/full's with client certificates.
const tlsLines = "tls:\n  certificate: /etc/gateway/tls/certificate.pem\n  key: /etc/gateway/tls/key.pem\n  clients: {ca: /etc/gateway/tls/clients-ca.pem}\n"

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
// variables' hash for the 691 variables its http block declares, 4 for each (counted by a script apart from the
// generator, which nginx 1.30.6 and 1.31.8 loaded at that size), and chooses its route through a tree of maps two
// levels deep: nginx finds no variable
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
		"  variables_hash_max_size 2788;",
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

// TestAcceptNamesCounted: the variables' hash counts each accept name of the auth check once for its js_var, and once
// more for its _grpc map in a file with an RPC route, which declares one for each name (checkLines): the counts are
// those of a script apart from the generator, at which nginx 1.30.6 and 1.31.8 loaded each file.
func TestAcceptNamesCounted(t *testing.T) {
	names := make([]string, 200)
	for i := range names {
		names[i] = fmt.Sprintf("x%d", i+1)
	}
	var routes strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&routes, "  - {name: r%d, prefix: /r%d/, to: app}\n", i, i)
	}
	file := func(rpc bool) string {
		out := "listen: 8000\nservices:\n  app: {http: 3000, grpc: 3001}\n  checks: {http: 9000}\norigins:\n  local: {hosts: [localhost]}\n" +
			"auth: {ask: checks /auth, timeout: 1s, enabled: true, when_unavailable: deny, accept: [" + strings.Join(names, ", ") + "]}\n" +
			"routes:\n" + routes.String()
		if rpc {
			out += `  - rpc: {to: {"*": app}}` + "\nrpc:\n  methods:\n    - {service: example.shop.v1.CartService, method: GetCart}\n"
		}
		return out
	}
	for _, tc := range []struct {
		name string
		rpc  bool
		want string
	}{
		// 808 variables, four times over: the 607 of the file without one, 200 _grpc maps and the RPC route's own.
		{"with an RPC route", true, "  variables_hash_max_size 3232;"},
		// 607 variables, four times over.
		{"without one", false, "  variables_hash_max_size 2428;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if broken := missing(generate(t, file(tc.rpc)), tc.want); broken != "" {
				t.Error(broken)
			}
		})
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
// timeouts.request (the route's timeout the Envoy output keeps), after 5 minutes with timeouts.request: none (Envoy's
// stream idle timeout), and after timeouts.idle when it is shorter. timeouts.idle also holds a client's body, the answer
// to the client, the request to the service and the service's connection, nginx's own timer for each standing for the
// one idle timer of the Envoy output; none is the longest time nginx reads.
func TestReadTimeouts(t *testing.T) {
	cases := []struct {
		name, timeouts string
		want           []string
	}{
		{"without timeouts.request", "", []string{"  proxy_read_timeout 15s;", "  grpc_read_timeout 15s;",
			"  proxy_connect_timeout 5s;", "  grpc_connect_timeout 5s;", "  client_body_timeout 5m;", "  send_timeout 5m;",
			"  proxy_send_timeout 5m;", "  grpc_send_timeout 5m;"}},
		{"with timeouts.request: none", "timeouts: {request: none}\n", []string{"  proxy_read_timeout 5m;", "  grpc_read_timeout 5m;"}},
		{"idle: 2s", "timeouts: {idle: 2s}\n", []string{"  proxy_read_timeout 2s;", "  grpc_read_timeout 2s;",
			"  proxy_connect_timeout 2s;", "  grpc_connect_timeout 2s;", "  client_body_timeout 2s;", "  send_timeout 2s;",
			"  proxy_send_timeout 2s;", "  grpc_send_timeout 2s;"}},
		{"idle: 1500ms", "timeouts: {idle: 1500ms}\n", []string{"  proxy_read_timeout 1500ms;", "  proxy_connect_timeout 1500ms;",
			"  client_body_timeout 1500ms;"}},
		{"idle: 90s", "timeouts: {idle: 90s}\n", []string{"  proxy_read_timeout 15s;", "  grpc_read_timeout 15s;",
			"  proxy_connect_timeout 5s;", "  grpc_connect_timeout 5s;", "  client_body_timeout 90s;", "  send_timeout 90s;",
			"  proxy_send_timeout 90s;", "  grpc_send_timeout 90s;"}},
		{"idle: 90s, request: none", "timeouts: {request: none, idle: 90s}\n", []string{"  proxy_read_timeout 90s;",
			"  grpc_read_timeout 90s;", "  proxy_connect_timeout 5s;"}},
		{"idle: 6m, request: none", "timeouts: {request: none, idle: 6m}\n", []string{"  proxy_read_timeout 6m;",
			"  grpc_read_timeout 6m;", "  proxy_connect_timeout 5s;", "  client_body_timeout 6m;"}},
		{"idle: none", "timeouts: {idle: none}\n", []string{"  proxy_read_timeout 15s;", "  grpc_read_timeout 15s;",
			"  proxy_connect_timeout 5s;", "  grpc_connect_timeout 5s;", "  client_body_timeout 9223372036854775807ms;",
			"  send_timeout 9223372036854775807ms;", "  proxy_send_timeout 9223372036854775807ms;",
			"  grpc_send_timeout 9223372036854775807ms;"}},
		{"idle: none, request: none", "timeouts: {request: none, idle: none}\n", []string{
			"  proxy_read_timeout 9223372036854775807ms;", "  grpc_read_timeout 9223372036854775807ms;",
			"  proxy_connect_timeout 5s;", "  client_body_timeout 9223372036854775807ms;"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.timeouts+smallFile("{name: app, prefix: /, to: app}"))
			for _, line := range c.want {
				if broken := missing(out, line); broken != "" {
					t.Error(broken)
				}
			}
		})
	}
}

// TestUpstreamIdle: each service's idle connections are kept as long as the Envoy output keeps them: an hour, Envoy's
// default, without timeouts.upstream_idle, the time written with it, and the longest time nginx reads with none.
func TestUpstreamIdle(t *testing.T) {
	cases := []struct{ name, timeouts, want string }{
		{"without timeouts.upstream_idle", "", "    keepalive_timeout 1h;"},
		{"upstream_idle: 45s", "timeouts: {upstream_idle: 45s}\n", "    keepalive_timeout 45s;"},
		{"upstream_idle: none", "timeouts: {upstream_idle: none}\n", "    keepalive_timeout 9223372036854775807ms;"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.timeouts+"listen: 8000\nservices:\n  app: {http: 3000}\n  api: {http: 3001}\n"+
				"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: api, prefix: /api/, to: api}\n"+
				"  - {name: app, prefix: /, to: app}\n")
			upstreams := blocks(lines(out), "  upstream ")
			if len(upstreams) != 2 {
				t.Fatalf("%d upstreams, want 2", len(upstreams))
			}
			for _, upstream := range upstreams {
				if !slices.Contains(upstream, c.want) {
					t.Errorf("%s holds no line %q", upstream[0], c.want)
				}
			}
		})
	}
}

// TestLogStatuses: log.status_from logs every status at or above it, as the Envoy output's filter does, written as
// nginx's three digits, and 009, which nginx writes for an answer it reads with no status line: one whose status has
// four digits, which Envoy logs at any status_from, and one with no status line at all, which Envoy answers 502 with the
// flag UPE and logs too (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with answers of 600, 999, 2000 and
// none).
func TestLogStatuses(t *testing.T) {
	file := func(statusFrom int) string {
		return generate(t, fmt.Sprintf("log: {status_from: %d, sample: 0%%}\n", statusFrom)+
			"listen: 8000\nservices:\n  app: {http: 3000}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n"+
			"  - {name: app, prefix: /, to: app}\n")
	}
	cases := []struct {
		statusFrom int
		want       string
	}{
		{100, `  map $status $pf_log_status { "~^(?:10[0-9]|1[1-9][0-9]|[2-9][0-9][0-9]|009)\\z" 1; default 0; }`},
		{480, `  map $status $pf_log_status { "~^(?:48[0-9]|49[0-9]|[5-9][0-9][0-9]|009)\\z" 1; default 0; }`},
		{499, `  map $status $pf_log_status { "~^(?:499|[5-9][0-9][0-9]|009)\\z" 1; default 0; }`},
		{590, `  map $status $pf_log_status { "~^(?:59[0-9]|[6-9][0-9][0-9]|009)\\z" 1; default 0; }`},
		{599, `  map $status $pf_log_status { "~^(?:599|[6-9][0-9][0-9]|009)\\z" 1; default 0; }`},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.statusFrom), func(t *testing.T) {
			if broken := missing(file(c.statusFrom), c.want); broken != "" {
				t.Error(broken)
			}
		})
	}
	// The map's key read as nginx reads it, the quoted string's \\ as one \, then run on $status as nginx writes it.
	key := regexp.MustCompile(`(?m)^  map \$status \$pf_log_status \{ "~(.*)" 1; default 0; \}$`).FindStringSubmatch(file(400))
	if key == nil {
		t.Fatal("status_from: 400 writes no map of $status to $pf_log_status")
	}
	taken := regexp.MustCompile(strings.ReplaceAll(key[1], `\\`, `\`))
	for _, c := range []struct {
		status string
		want   bool
	}{{"399", false}, {"400", true}, {"599", true}, {"600", true}, {"999", true}, {"009", true}, {"000", false}} {
		if got := taken.MatchString(c.status); got != c.want {
			t.Errorf("status_from: 400 takes $status %s: %t, want %t", c.status, got, c.want)
		}
	}
}

// tried is what nginx writes for a request's tries, one value for each try: retried is $pf_log_retried, and status,
// connect, header and received are $upstream_status, $upstream_connect_time, $upstream_header_time and
// $upstream_bytes_received.
type tried struct{ retried, status, connect, header, received string }

// triedLog reads out's request log as nginx reads it for a request it tried as listed: each value list through the map
// of its last try, then into the failure line's flag, and the header times into upstream_ms. Each answer the rows hold
// came whole or not at all, so $pf_log_cut is "".
func triedLog(t *testing.T, out string) (failure func(tried) string, upstreamMs func(header string) string) {
	t.Helper()
	status, connect, header, received := mapOf(t, out, "pf_last_status"), mapOf(t, out, "pf_last_connect"),
		mapOf(t, out, "pf_last_header"), mapOf(t, out, "pf_last_received")
	flag, ms := mapOf(t, out, "pf_log_failure"), mapOf(t, out, "pf_log_upstream_ms")
	failure = func(c tried) string {
		return flag(strings.Join([]string{c.retried, status(c.status), connect(c.connect), header(c.header), "", received(c.received)}, ":"))
	}
	return failure, func(times string) string { return ms(header(times)) }
}

// TestRetriedLog: a call to a retried RPC route is logged by the try that answered, the last of the values nginx writes
// for each try, joined by ", ", and one whose last try was refused is flagged URX,UF, as the Envoy output flags a call
// retries ran out on, also through the copy that tries once. Each row is what nginx 1.30.6 and 1.31.8 logged for such a
// call, and each flag what Envoy 1.40.2 logged for it.
func TestRetriedLog(t *testing.T) {
	out := generate(t, "listen: 8000\nservices:\n  down: {grpc: 8081, addresses: [\"10.0.0.1\", \"10.0.0.2\"]}\n"+
		"  app: {http: 8080}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n"+
		"  - rpc: {to: {\"*\": {service: down, name: down}}, retries: 1}\n  - {name: app, prefix: /, to: app}\n"+
		"rpc:\n  methods:\n    - {service: example.v1.S, method: Call}\n")
	all := lines(out)
	for _, c := range []struct {
		location string
		marked   bool
	}{{"@pf_r_1", true}, {"@pf_r_1-once", true}, {"@pf_r_1-uri", true}, {"@pf_r_1-uri-once", true}, {"@pf_r_2", false}, {"@pf_r_2-uri", false}} {
		found := blocks(all, "    location "+c.location+" {")
		if len(found) != 1 {
			t.Fatalf("%d locations %s, want 1", len(found), c.location)
		}
		if got := slices.Contains(found[0], "      set $pf_log_retried 1;"); got != c.marked {
			t.Errorf("location %s sets $pf_log_retried 1: %t, want %t", c.location, got, c.marked)
		}
	}
	failure, upstreamMs := triedLog(t, out)
	for _, c := range []struct {
		tried
		want string
	}{
		{tried{"1", "502, 502", "-, -", "-, -", "0, 0"}, "URX,UF"},
		{tried{"1", "502", "-", "-", "0"}, "URX,UF"},
		{tried{"1", "502, 200", "-, 0.000", "-, 0.006", "0, 208"}, ""},
		{tried{"0", "502", "-", "-", "0"}, "UF"},
		{tried{"0", "200", "0.000", "0.001", "140"}, ""},
		{tried{"0", "502", "0.000", "-", "0"}, "UC"},
		{tried{"0", "502", "0.000", "-", "40"}, "UPE"},
		{tried{"1", "504, 504", "-, -", "-, -", "0, 0"}, "URX,UF"},
		{tried{"0", "504", "-", "-", "0"}, "UF"},
	} {
		if got := failure(c.tried); got != c.want {
			t.Errorf("the failure line's flag of %+v: %q, want %q", c.tried, got, c.want)
		}
	}
	for times, want := range map[string]string{"-, 0.006": "6", "0.006": "6", "-, 1.250": "1250", "0.000": "0", "-, -": "null", "-": "null"} {
		if got := upstreamMs(times); got != want {
			t.Errorf("upstream_ms of $upstream_header_time %q: %q, want %q", times, got, want)
		}
	}
}

// TestRetryLog: a request to a retried route is logged by its last try, the values after the last " : " nginx writes
// across the locations the request went through, and the failure line is written by $pf_log_flags, read as nginx
// reads it: by $pf_rt_cap from $pf_log_retry_flags or $pf_log_failure (TestRetryLogReadsRetriesOnlyWhenRetried). A request whose
// last try sent its service nothing, with a body that fits, and that no copy took further is flagged URX with its last
// try's flag, as the Envoy output flags a request retries ran out on, at a service of one backend too, which nginx does
// not try again; one the route's time ended is flagged as the timer that ended it, UT for the route's 15 s and no line
// for the idle timer, as Envoy writes none for it. A request the server's jump sent to -once, which nginx tries once,
// and an RPC call keep their failure line's flag. Each row is what nginx 1.30.6 and 1.31.8 wrote for a request to
// backends that refused, reset or did not take the connection: its tries, and $pf_rt_cap, $pf_rt_unsent with
// $pf_rt_fits, and $pf_rt_target as the log read them.
func TestRetryLog(t *testing.T) {
	type row struct {
		name string
		tried
		retry     string // $pf_rt_cap|$pf_rt_unsent$pf_rt_fits|$pf_rt_target
		flags, ms string // the failure line's flags, "" for no line, and upstream_ms
	}
	two := `addresses: ["10.0.0.1", "10.0.0.2"]`
	for _, file := range []struct {
		name, timeouts string
		rows           []row
	}{
		{"the route's 15 s", "", []row{
			// Envoy 1.40.2: upstream_ms a number, no failure line.
			{"refused, then served", tried{"0", "502 : 200", "- : 0.000", "- : 0.001", "0 : 70"}, "1|01|fail", "", "1"},
			// Two backends refusing, retries 1 and 3; an HTTP/2 GET and a PUT with a length the same. Envoy: URX,UF.
			{"every try refused", tried{"0", "502 : 502", "- : -", "- : -", "0 : 0"}, "1|11|fail", "URX,UF", "null"},
			{"eight refusing, 6 jumps", tried{"0", "502 : 502 : 502 : 502 : 502 : 502 : 502", "- : - : - : - : - : - : -",
				"- : - : - : - : - : - : -", "0 : 0 : 0 : 0 : 0 : 0 : 0"}, "6|11|fail", "URX,UF", "null"},
			{"one backend refusing", tried{"0", "502", "-", "-", "0"}, "0|11|fail", "URX,UF", "null"},
			// Envoy: UC, not tried again after the head.
			{"refused, then reset after the head", tried{"0", "502 : 502", "- : 0.000", "- : -", "0 : 0"}, "1|01|fail", "UC", "null"},
			// Four and eight backends whose connection attempts time out; a gRPC call too. Envoy: UT at 15 s.
			{"the time spent", tried{"0", "504 : 504 : 504", "- : - : -", "- : - : -", "0 : 0 : 0"}, "3|11|rt_spent", "UT", "null"},
			{"the time spent, a gRPC call", tried{"0", "504 : 504 : 504", "- : - : -", "- : - : -", "0 : 0 : 0"}, "6|11|rt_spent-grpc", "UT", "null"},
			// -once tries once: a chunked PUT and a PUT over the bound, refused, are flagged by that try, where the Envoy
			// output tried the chunked one again (URX,UF) and answered the other 507 with no line.
			{"-once, a chunked PUT refused", tried{"0", "502", "-", "-", "0"}, "|11|fail", "UF", "null"},
			{"-once, a PUT over the bound refused", tried{"0", "502", "-", "-", "0"}, "|10|fail", "UF", "null"},
			{"-once, a PUT over the bound served", tried{"0", "200", "0.000", "0.002", "70"}, "|00|fail", "", "2"},
			// An RPC call beside a retried route keeps its own flag. Envoy: URX,UF, and a number after a retry.
			{"an RPC call, both refusing", tried{"1", "502, 502", "-, -", "-, -", "0, 0"}, "|11|fail", "URX,UF", "null"},
			{"an RPC call, refused, then served", tried{"1", "502, 200", "-, 0.000", "-, 0.001", "0, 208"}, "|01|fail", "", "1"},
		}},
		{"idle 10s", "timeouts: {idle: 10s}\n", []row{
			// Envoy: SI, no failure line, at 10 s.
			{"the time spent", tried{"0", "504 : 504", "- : -", "- : -", "0 : 0"}, "3|11|rt_spent", "", "null"},
		}},
	} {
		t.Run(file.name, func(t *testing.T) {
			out := generate(t, capFile(two, "1")+file.timeouts)
			failure, upstreamMs := triedLog(t, out)
			read, flags := mapOf(t, out, "pf_log_flags"), mapOf(t, out, "pf_log_retry_flags")
			for _, c := range file.rows {
				flag := failure(c.tried)
				// Each map's value is a variable nginx then reads, as the failure line does.
				got, _, _ := strings.Cut(c.retry, "|")
				if got = read(got); got == "$pf_log_retry_flags" {
					got = flags(c.retry + "|" + flag)
				}
				if got == "$pf_log_failure" {
					got = flag
				}
				if got != c.flags {
					t.Errorf("%s: the failure line's flags %q, want %q", c.name, got, c.flags)
				}
				if got := upstreamMs(c.header); got != c.ms {
					t.Errorf("%s: upstream_ms %q, want %q", c.name, got, c.ms)
				}
			}
		})
	}
}

// TestRetryLogReadsRetriesOnlyWhenRetried: the failure line's flags are picked by $pf_rt_cap, the jump count of the
// retried route location that took the request, so a request with none, to a route with no retries, an RPC route or
// -once, takes $pf_log_failure and nginx reads no retry map for it: a map evaluates the value it picks alone, and
// $pf_log_retry_flags reads $pf_rt_target, whose copy name runs the script's retrySpent. Measured on nginx 1.30.6 and
// 1.31.8 with a copy of the script that logs each call: no call for a request to a route with no retries in a file
// with retried routes, and one at the log for each request to a retried route.
func TestRetryLogReadsRetriesOnlyWhenRetried(t *testing.T) {
	out := generate(t, retriedFile("  s: {http: 8080, addresses: [\"10.0.0.1\", \"10.0.0.2\"]}\n",
		"  - {name: r1, prefix: /r1/, to: s, retries: 1}\n  - {name: r2, prefix: /r2/, to: s}\n", ""))
	if line := `  map $pf_rt_cap $pf_log_flags { "" $pf_log_failure; default $pf_log_retry_flags; }`; !hasLine(out, line) {
		t.Errorf("no line %q", line)
	}
	read := mapOf(t, out, "pf_log_flags")
	for capped, want := range map[string]string{"": "$pf_log_failure", "0": "$pf_log_retry_flags", "1": "$pf_log_retry_flags",
		"6": "$pf_log_retry_flags"} {
		if got := read(capped); got != want {
			t.Errorf("$pf_log_flags for $pf_rt_cap %q: %q, want %q", capped, got, want)
		}
	}
}

// mapOf returns the map of out that sets $name, read as nginx reads it (ngx_http_map_find): its key "", if written,
// for an empty value, and its regular expressions in the order written for any other, as nginx tries none on an empty
// value (measured on nginx 1.30.6 and 1.31.8: an empty $content_length took the default of a map whose expression
// matches an empty text); each quoted string's \\ as one \, the first that matches giving its value with each
// $capture filled in, and default when none does; volatile, which nginx reads before the keys, is no key. A map with
// another key that is not a regular expression ends the test, as it would need nginx's exact match first, and so does
// a key the function is asked about that reaches an expression Go's regexp cannot compile, such as one with a
// lookahead or a backreference, which PCRE2 reads.
func mapOf(t *testing.T, out, name string) func(key string) string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^  map (?:"[^"]*"|\S+) \$` + name + ` \{ (.*) default (\S*); \}$`).FindStringSubmatch(out)
	if line == nil {
		t.Fatalf("no map sets $%s", name)
	}
	type entry struct {
		key         *regexp.Regexp // nil when Go's regexp cannot compile text
		text, value string
	}
	var entries []entry
	rest := strings.TrimPrefix(line[1], "volatile; ") + " "
	empty, emptyKey := regexp.MustCompile(`^"" (\S+); `).FindStringSubmatch(rest), false
	if empty != nil {
		rest, emptyKey = rest[len(empty[0]):], true
	}
	entryText := regexp.MustCompile(`^"~((?:[^"\\]|\\.)*)" (\S+); `)
	for rest != "" {
		found := entryText.FindStringSubmatch(rest)
		if found == nil {
			t.Fatalf("the map of $%s holds %q, which is not a regular expression and its value", name, rest)
		}
		key, _ := regexp.Compile(strings.ReplaceAll(found[1], `\\`, `\`))
		entries = append(entries, entry{key, found[1], found[2]})
		rest = rest[len(found[0]):]
	}
	unquoted := func(value string) string {
		if value == `""` {
			return ""
		}
		return value
	}
	return func(key string) string {
		if key == "" && emptyKey {
			return unquoted(empty[1])
		}
		for _, e := range entries {
			if key == "" {
				break
			}
			if e.key == nil {
				t.Fatalf("%q reaches %q in the map of $%s, which Go's regexp cannot compile", key, e.text, name)
			}
			if match := e.key.FindStringSubmatch(key); match != nil {
				return regexp.MustCompile(`\$(\w+)`).ReplaceAllStringFunc(unquoted(e.value), func(variable string) string {
					return match[e.key.SubexpIndex(variable[1:])]
				})
			}
		}
		return unquoted(line[2])
	}
}

// TestCutLog: an answer from a gRPC port, which nginx reads over HTTP/2, is flagged UPE when nginx ended the call
// because its body did not match its content-length, so the request did not complete, and never by
// $upstream_response_length, which counts the frames there. The keys are the values nginx 1.30.6 and 1.31.8 wrote for a
// Go service that sends content-length: 5 with the empty message (47 bytes of frames), and for one that sends 10 or 3
// with it (35 bytes), beside Envoy 1.40.2, which flags the last two UPE and the first nothing. Each key holds nginx's
// own values in the order of the map's source, a status and a response length for each try, which the map reads
// through the maps of the last try.
func TestCutLog(t *testing.T) {
	out := generate(t, "listen: 8000\nservices:\n  up: {grpc: 8081}\n  app: {http: 8080}\norigins:\n  local: {hosts: [localhost]}\n"+
		"routes:\n  - rpc: {to: {\"*\": {service: up, name: up}}}\n  - {name: app, prefix: /, to: app}\n"+
		"rpc:\n  methods:\n    - {service: example.v1.S, method: Call}\n")
	cut, status, length := mapOf(t, out, "pf_log_cut"), mapOf(t, out, "pf_last_status"), mapOf(t, out, "pf_last_length")
	for _, c := range []struct{ key, want string }{
		{"POST:200:47:5:OK:up_grpc", ""},
		{"POST:200:35:10::up_grpc", "UPE"},
		{"POST:200:35:3::up_grpc", "UPE"},
		{"POST:502, 200:0, 47:5:OK:up_grpc", ""},
		{"POST:200:47::OK:up_grpc", ""},
		{"POST:200:47:::up_grpc", ""},
		{"HEAD:200:0:5::app_http", ""},
		{"GET:304:0:5::app_http", ""},
	} {
		parts := strings.Split(c.key, ":")
		parts[1], parts[2] = status(parts[1]), length(parts[2])
		if got := cut(strings.Join(parts, ":")); got != c.want {
			t.Errorf("$pf_log_cut of %q: %q, want %q", c.key, got, c.want)
		}
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

// TestHeaderHash: the table of the header names a proxy location sets holds the longest name of auth.accept and
// remove_from_clients, which nginx's default bucket of 64 bytes holds none of 47 bytes or more of, and has 4 slots for
// each name of the largest table, where nginx's default of 512 warned at 130 names of 20 bytes (measured on nginx
// 1.30.6 and 1.31.8).
func TestHeaderHash(t *testing.T) {
	const base = "listen: 8000\nservices:\n  app: {http: 3000}\n  checks: {http: 7070}\norigins:\n  local: {hosts: [localhost]}\n" +
		"routes:\n  - {name: app, prefix: /, to: app}\n"
	long := "x-" + strings.Repeat("a", 45)
	many := make([]string, 300)
	for i := range many {
		many[i] = fmt.Sprintf("x-%d-%s", i, strings.Repeat("a", 16-len(fmt.Sprint(i))))
	}
	cases := []struct {
		name, proxyfile string
		want            []string
	}{
		{"no list", base, []string{"  proxy_headers_hash_bucket_size 64;", "  proxy_headers_hash_max_size 512;"}},
		{"a removed name of 47 bytes", base + "remove_from_clients: [" + long + "]\n",
			[]string{"  proxy_headers_hash_bucket_size 128;", "  proxy_headers_hash_max_size 512;"}},
		{"an accepted name of 47 bytes", base + "auth: {ask: checks /auth, timeout: 1s, enabled: true, when_unavailable: deny, accept: [" + long + "]}\n",
			[]string{"  proxy_headers_hash_bucket_size 128;", "  proxy_headers_hash_max_size 512;"}},
		{"300 removed names of 20 bytes", base + "remove_from_clients: [" + strings.Join(many, ", ") + "]\n",
			[]string{"  proxy_headers_hash_bucket_size 128;", "  proxy_headers_hash_max_size 1320;"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.proxyfile)
			for _, line := range c.want {
				if broken := missing(out, line); broken != "" {
					t.Error(broken)
				}
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
	// 80 HTTP routes in each of four environments, each with a second location but the two with a rewrite, and for each
	// of the two retried routes the two copies that try once and the four that take a next try:
	// (80 * 2 - 2 + 2 * 6) * 4 = 680.
	if proxied != 680 {
		t.Errorf("%d locations hold proxy_pass, want 680", proxied)
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
// configuration whose function the script lacks, and nginx then answers every request 500. The battery file is read
// with client_address forwarded, a route limited by client and a service that hashes a header, so the configuration
// names every function the script holds.
func TestScript(t *testing.T) {
	file := rewritten(t, string(testsupport.ReadFile(t, authFile)), "client_address: connection\n", "client_address: forwarded\n")
	file = rewritten(t, file, "  - {name: public, prefix: /public/, to: shop, auth: off}",
		"  - {name: public, prefix: /public/, to: shop, auth: off, rate_limit: {requests: 20, per: 1s, by: client}, retries: 1}")
	file = rewritten(t, file, `  catalog: {http: 8090, address: "10.0.2.21"}`,
		`  catalog: {http: 8090, address: "10.0.2.21", balance: hash, hash: {header: x-session-id}}`)
	config, script := generateFiles(t, file)
	if first, _, _ := strings.Cut(script, "\n"); first != "// Generated by proxyfile. Do not edit: change the proxyfile beside it and run proxyfile render; proxyfile check fails on a hand edit." {
		t.Errorf("the script's first line is %q", first)
	}
	// Each function the configuration calls, once, in the order it first calls it: the client key, the rate limit's
	// answer, the retries' time spent and the hash key in the http block, the handler on every checked route location
	// and the relay in the stream block among them.
	var named []string
	for _, m := range regexp.MustCompile(`(?m)^\s*js_(?:set \$\w+|content|header_filter|body_filter|access|filter) pf\.(\w+)(?: nocache)?;$`).FindAllStringSubmatch(config, -1) {
		if !slices.Contains(named, m[1]) {
			named = append(named, m[1])
		}
	}
	if want := []string{"headerNames", "forwardedProto", "origin", "clientKey", "limitedKind", "retrySpent", "hashKey", "check", "relay"}; !slices.Equal(named, want) {
		t.Fatalf("the configuration names the functions %q, want %q", named, want)
	}
	// A header value holding a control byte other than a tab is refused: nginx passes one to the service, where Envoy
	// answers 400 over HTTP/1.1 and closes the connection over HTTP/2 (measured on nginx 1.30.6 and 1.31.8 beside
	// Envoy 1.40.2).
	if check := "\n        if (/[\\x00-\\x08\\x0A-\\x1F\\x7F]/.test(raw[i][1])) {\n            return \"1\";\n        }\n"; !strings.Contains(script, check) {
		t.Errorf("the script holds no %q", check)
	}
	// Over HTTP/2 a request with more fields than limits.request_headers.count as Envoy counts them is refused: with all
	// four pseudo-headers, :authority counted whether or not the client sent it, because nginx gives :authority, a host
	// field or both one host line, and max_headers counts none of them; and with each cookie field, which nginx joins
	// into one line and max_headers does not count either (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
	for _, check := range []string{
		"\n    var h2 = r.httpVersion == \"2.0\";\n    var fields = fieldCount(r) + 4;\n",
		"\n        if (r.httpVersion == \"2.0\" && raw[i][0].toLowerCase() == \"cookie\") {\n            fields += raw[i][1].split(\"; \").length - 1;\n        }\n",
		"\n    if (h2 && fields > Number(r.variables.pf_header_count)) {\n        return \"1\";\n    }\n    return \"0\";\n}\n",
		// An X-Forwarded-Proto line that is empty as Envoy reads it, over HTTP/1 once the spaces and tabs around it are
		// left out, is flagged.
		"\n        if (raw[i][0].toLowerCase() == \"x-forwarded-proto\" && asEnvoyReads(r, raw[i][1]) == \"\") {\n            return \"1\";\n        }\n",
		"\nfunction asEnvoyReads(r, value) {\n    if (r.httpVersion == \"2.0\") {\n        return value;\n    }\n    return trimmed(value);\n}\n",
	} {
		if !strings.Contains(script, check) {
			t.Errorf("the script holds no %q", check)
		}
	}
	exported := regexp.MustCompile(`(?m)^export default \{(.*)\};$`).FindStringSubmatch(script)
	if exported == nil {
		t.Fatal("the script exports no default object")
	}
	// The handler is asynchronous, as it waits for the check, and the relay is a stream filter, which takes a session.
	definitions := map[string]string{"check": "\nasync function check(r) {\n", "relay": "\nfunction relay(s) {\n"}
	for _, name := range named {
		definition, ok := definitions[name]
		if !ok {
			definition = "\nfunction " + name + "(r) {\n"
		}
		if !strings.Contains(script, definition) {
			t.Errorf("the script holds no %q", definition)
		}
	}
	if got := strings.Split(exported[1], ", "); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(named))) {
		t.Errorf("the script exports %q, want the functions the configuration calls, %q", got, named)
	}
}
