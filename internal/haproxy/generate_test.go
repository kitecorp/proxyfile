package haproxy_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/haproxy"
	"github.com/kitecorp/proxyfile/v2/internal/read"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// input reads the proxyfile text as render reads a proxyfile that writes its origins and its RPC methods, and returns
// what the generator is given. A problem ends the test.
func input(t *testing.T, proxyfile string) generator.Input {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxyfile.yaml")
	testsupport.WriteFile(t, path, proxyfile)
	file, problems := read.Proxyfile(path)
	if problems != nil {
		t.Fatalf("read.Proxyfile: %q", testsupport.ProblemStrings(problems))
	}
	in := generator.Input{Gateway: file.Gateway, Origins: file.Origins}
	if file.Methods != nil {
		routes, problems := rpc.Resolve(file.Gateway, file.Methods)
		if problems != nil {
			t.Fatalf("rpc.Resolve: %q", testsupport.ProblemStrings(problems))
		}
		in.RPC = routes
	}
	return in
}

// generate returns the text of the configuration Generate writes for the proxyfile. An error ends the test.
func generate(t *testing.T, proxyfile string) string {
	t.Helper()
	return string(files(t, proxyfile)[0].Bytes)
}

// script returns the text of the script Generate writes beside the configuration for the proxyfile.
func script(t *testing.T, proxyfile string) string {
	t.Helper()
	return string(files(t, proxyfile)[1].Bytes)
}

// files returns the two files Generate writes for the proxyfile: the configuration, named "", and the script it
// loads, named proxyfile.lua. An error or another list ends the test.
func files(t *testing.T, proxyfile string) []generator.File {
	t.Helper()
	files, err := haproxy.Generate(input(t, proxyfile))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name)
	}
	if !slices.Equal(names, []string{"", "proxyfile.lua"}) {
		t.Fatalf("Generate returned the files %q, want the configuration, named \"\", and proxyfile.lua", names)
	}
	return files
}

// smallFile is the small proxyfile the rows change: one service, one environment, and one route, which takes every
// path.
var smallFile = testsupport.Document{
	"listen: 8000",
	"services:",
	"  app: {http: 3000, address: 127.0.0.1}",
	"origins:",
	"  all: {hosts: [localhost]}",
	"routes:",
	"  - {name: app, prefix: /, to: app}",
}

// small returns the small proxyfile with lines added at the end.
func small(lines ...string) string {
	return slices.Concat(smallFile, testsupport.Document(lines)).String()
}

// beforeApp returns the small proxyfile with route put before app's route, so it is route 1, and with lines added at
// the end.
func beforeApp(route string, lines ...string) string {
	return slices.Concat(smallFile[:6], testsupport.Document{route}, smallFile[6:], testsupport.Document(lines)).String()
}

// missing returns the first of the blocks out does not hold as whole lines, "" when it holds them all. The lines of
// a block must follow one another with nothing between them, and each block must come after the one before it.
func missing(out string, blocks [][]string) string {
	lines := strings.Split(out, "\n")
	from := 0
	for _, block := range blocks {
		at := -1
		for i := from; i+len(block) <= len(lines); i++ {
			if slices.Equal(lines[i:i+len(block)], block) {
				at = i
				break
			}
		}
		if at < 0 {
			return strings.Join(block, "\n")
		}
		from = at + len(block)
	}
	return ""
}

// firstDifference names the first line where got and want differ, "" when they are equal.
func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w || i >= len(gotLines) || i >= len(wantLines) {
			return fmt.Sprintf("line %d: got %q, want %q", i+1, g, w)
		}
	}
	return ""
}

const batteryFile = "../../conformance/testdata/haproxy.yaml"

// The battery's file gives the configuration the conformance battery passed on HAProxy 3.2 and 3.4, byte for byte,
// and the same bytes every time.
func TestBatteryFile(t *testing.T) {
	proxyfile := string(testsupport.ReadFile(t, batteryFile))
	want := string(testsupport.ReadFile(t, "testdata/haproxy-file.cfg"))
	first := generate(t, proxyfile)
	if difference := firstDifference(first, want); difference != "" {
		t.Errorf("the battery's file: %s", difference)
	}
	if difference := firstDifference(generate(t, proxyfile), first); difference != "" {
		t.Errorf("a second render: %s", difference)
	}
}

// Each rule a security review reads, on the battery's file: the line, and its place where the place matters.
func TestSecurityLines(t *testing.T) {
	out := generate(t, string(testsupport.ReadFile(t, batteryFile)))
	cases := []struct {
		name string
		want [][]string
	}{
		{"the version guard, after the line that names the script", [][]string{{
			"# Generated by proxyfile. Do not edit: change the proxyfile beside it and run proxyfile render; proxyfile check fails on a hand edit.",
			"# Loads proxyfile.lua, the script proxyfile render writes beside this file.",
			".if version_before(3.2)",
			`  .alert "proxyfile writes this file for HAProxy 3.2 and 3.4; this HAProxy is older, so it stops here"`,
			".endif",
		}}},
		{"remove_from_clients first", [][]string{{
			"  bind :10000 ssl crt @/public alpn h2,http/1.1 verify required ca-file '/etc/gateway/tls/clients-ca.pem'",
			"  # remove_from_clients, before any other rule",
			"  http-request del-header 'x-client-id'",
		}}},
		{"connection's client address: the headers only a proxy in front may send removed, the peer's address on the client's line", [][]string{
			slices.Concat([]string{"  # client_address: connection"}, delHeaders(generator.RemovedByEnvoyFacingClients), []string{
				`  http-request set-header x-forwarded-for "%[req.fhdr(x-forwarded-for)],%[src]" if { req.fhdr(x-forwarded-for) -m len gt 0 }`,
				"  http-request set-header x-forwarded-for %[src] unless { req.fhdr(x-forwarded-for) -m len gt 0 }",
				"  http-request set-header x-envoy-external-address %[src]",
				"  http-request set-header x-forwarded-proto https",
			}),
			{
				"  http-request set-header x-request-id %[uuid(4)]",
				"  http-request set-var(txn.request_id) req.fhdr(x-request-id)",
			},
		}},
		{"the lines of each header Envoy joins, joined after the removals and before the client address", [][]string{
			removedByEnvoy(),
			{
				"  # the lines of each header Envoy joins, as one line, before anything reads them",
				"  http-request lua.pf_join",
				"  http-request return status 400 if !{ var(txn.joined) -m found }",
				"  # client_address: connection",
			},
		}},
		{"the script, loaded by every thread", [][]string{{"global"}, {"  tune.lua.bool-sample-conversion normal", "  lua-load-per-thread proxyfile.lua"}}},
		{"the normalisation block", [][]string{{
			"  http-request normalize-uri fragment-strip",
			`  http-request set-var(txn.path) path,lua.pf_escape,regsub(%2f,/,gi),regsub(%5c,/,gi),regsub('\\x5c',/,g)`,
			"  http-request return status 400 if !{ var(txn.path) -m found }",
			"  http-request set-path %[var(txn.path)]",
			"  http-request normalize-uri percent-decode-unreserved",
			"  http-request normalize-uri path-merge-slashes",
			"  http-request normalize-uri path-strip-dot",
			"  http-request normalize-uri path-strip-dotdot full",
			"  http-request return status 400 if { path -m sub %00 }",
		}}},
		// Envoy answers a request no route takes 404 before it reads its upgrade or its origin, an upgrade other than
		// WebSocket 403 before its origin, and a dot segment with a parameter, which a route of its own refuses, 400
		// after both, with no CORS header (measured on Envoy 1.40.2).
		{"the order of the answers: no route, the upgrade, the origin, the dot segment, the preflight", [][]string{
			{"  http-request normalize-uri path-strip-dotdot full"},
			{
				`  # route 1 matches the pattern "^/events/[a-z0-9-]+$"`,
				"  http-request lua.pf_route 1 's2;f;e3,4;r4,47,47;r5,101,101;r6,118,118;r7,101,101;r8,110,110;r9,116,116;r10,115,115;r11,47,47;r12,45,45,48,57,97,122;a11,13;e14,8;m' if !{ var(txn.route) -m found }",
				"  http-request return status 400 if !{ var(txn.route) -m found } !{ var(txn.pattern1) -m found }",
				"  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } { var(txn.pattern1) -m str 1 }",
			},
			{
				"  # a dot segment with a ; parameter, written or escaped once, reaches no service",
				`  acl dot_segment path_reg '/\.\.?(;|%3[Bb])'`,
				"  http-request return status 404 if !{ var(txn.route) -m found } !dot_segment",
				"  # WebSocket is the one upgrade passed on",
				"  acl has_origin req.fhdr(origin) -m found",
				"  acl upgrade req.fhdr(upgrade) -m found",
				"  acl websocket req.fhdr(upgrade) -i -m str websocket",
				`  http-request return status 403 content-type text/plain string "Invalid CORS request" if upgrade !websocket has_origin`,
				"  http-request return status 403 if upgrade !websocket",
				"  # browser origins: none, an environment's own host, or its origins and patterns",
			},
			{
				`  http-request return status 403 content-type text/plain string "Invalid CORS request" if !{ var(txn.admitted) -m bool }`,
				"  http-request return status 400 if dot_segment",
				"  acl preflight method OPTIONS",
				"  acl preflight_method req.fhdr(access-control-request-method) -m len gt 0",
			},
			{`  http-request replace-path '^/api/webhooks(.*)$' '/hooks/payments\1' if { var(txn.route) -m str 4 }`},
		}},
		{"each origin read as its whole line", [][]string{{
			"  acl env1 req.fhdr(origin) -i -m str -- 'https://example.com'",
			"  acl env1 req.fhdr(origin) -i -m str -- 'https://www.example.com'",
			`  acl env1 req.fhdr(origin) -m reg -- '^https://.*\.example\.com$'`,
			`  acl env1_own req.fhdr(origin) -m reg -- '^https?://api\.example\.com(:[0-9]+)?$'`,
		}}},
		{"the environment from the Host header", [][]string{{"  http-request set-var(txn.host) req.hdr(host),host_only,lower"}}},
		{"the request checks, after remove_from_clients and before the client address", [][]string{
			{"  http-request del-header 'x-client-id'"},
			{
				"  # what Envoy refuses before it reads a request further, and the headers it removes",
				"  http-request return status 426 if { req.ver 1.0 }",
				"  http-request return status 400 if { req.ver 1.1 } !{ method " + strings.Join(generator.EnvoyMethods, " ") + " }",
				"  http-request return status 400 if { req.ver 1.1 } !{ url_beg / }",
				"  http-request return status 400 if { req.ver 2.0 } { req.fhdr(upgrade) -m found }",
				`  http-request return status 400 if { req.hdr(connection) -i -m reg '^(?!(?:close|keep-alive|upgrade|http2-settings|te)?\z)' }`,
				"  http-request return status 400 if { req.hdr_cnt(connection) ge 10 }",
				"  http-request return status 400 if { req.hdr(connection) -i -m str http2-settings }",
				`  http-request return status 400 if { req.hdrs -m reg '[ ][ ][ \t]' } || !{ req.hdrs -m found }`,
				"  http-request set-var(txn.cookies) req.cook_cnt",
				"  http-request return status 400 if { req.ver 2.0 } { req.fhdr_cnt,add(txn.cookies) ge 98 }",
				"  http-request set-header te trailers if { req.hdr(connection) -i -m str te } { req.hdr(te) -i -m str trailers }",
				"  http-request del-header connection unless { req.hdr(upgrade) -i -m str websocket }",
				"  http-request del-header keep-alive",
			},
			{"  # client_address: connection"},
		}},
		{"the headers Envoy removes, each by its own rule", [][]string{removedByEnvoy()}},
		// A service's Keep-Alive reached an HTTP/1.1 client through HAProxy 3.2.26 and 3.4.7, and through neither Envoy
		// 1.40.2 nor nginx 1.30.6 and 1.31.8 (measured).
		{"a service's Keep-Alive removed from its answer", [][]string{{"  http-request del-header keep-alive", "  http-response del-header keep-alive"}}},
		{"the header lines HAProxy reads, the request line among them", [][]string{{"  tune.http.maxhdr 101"}}},
		// A service that closed a connection HAProxy reused got the request again on a new one, where Envoy answers 502
		// (measured on HAProxy 3.2.26 and 3.4.7 beside Envoy 1.40.2).
		{"each request on a connection of its own to the service", [][]string{
			{"  retries 0", "  option http-server-close", "  option http-pretend-keepalive"},
			{"backend shop_grpc", "  server shop1 10.0.1.11:8081 proto h2 max-reuse 0", "  server shop2 10.0.1.12:8081 proto h2 max-reuse 0"},
		}},
		{"a service out of reach", [][]string{
			{
				"defaults",
				"  mode http",
				"  retries 0",
				"  option http-server-close",
				"  option http-pretend-keepalive",
				"  timeout connect 5s",
				"  timeout client 300s",
				"  timeout server 15s",
				"  option http-drop-request-trailers",
				"  http-error status 503",
				"  http-error status 502",
				"",
			},
			{"  http-after-response set-status 502 if { txn.sess_term_state -m reg ^S[CH] } || { txn.sess_term_state -m str sC }"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := missing(out, c.want); block != "" {
				t.Errorf("the output does not hold, in its place:\n%s", block)
			}
		})
	}
}

// removedByEnvoy is the del-header line of each header the Envoy output removes, in generator.RemovedByEnvoy's order.
func removedByEnvoy() []string {
	return delHeaders(generator.RemovedByEnvoy)
}

// delHeaders is the del-header line of each of names, in their order.
func delHeaders(names []string) []string {
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = "  http-request del-header " + name
	}
	return lines
}

// The script is the same bytes for every file, nginx's script.js is, so two files rendered into one folder share it:
// it holds the headers pf_join joins and the bytes pf_escape escapes, both the generator's own, and no program.
func TestScript(t *testing.T) {
	plain := script(t, small())
	if withPattern := script(t, beforeApp(`  - {name: p, pattern: "^/a$", to: app}`)); withPattern != plain {
		t.Errorf("the script of a file with a pattern route differs from a file's with none: %s", firstDifference(withPattern, plain))
	}
	want := [][]string{
		{
			"local joined = {",
			`  "accept",`,
			`  "accept-encoding",`,
			`  "access-control-request-headers",`,
			`  "access-control-request-method",`,
			`  "access-control-request-private-network",`,
			`  "authentication",`,
			`  "authorization",`,
			`  "cache-control",`,
			`  "cdn-loop",`,
			`  "content-encoding",`,
			`  "content-type",`,
			`  "expect",`,
			`  "grpc-accept-encoding",`,
			`  "grpc-timeout",`,
			`  "if-match",`,
			`  "if-modified-since",`,
			`  "if-none-match",`,
			`  "if-range",`,
			`  "if-unmodified-since",`,
			`  "origin",`,
			`  "pragma",`,
			`  "proxy-status",`,
			`  "referer",`,
			`  "upgrade",`,
			`  "user-agent",`,
			`  "via",`,
			`  "x-client-trace-id",`,
			`  "x-envoy-attempt-count",`,
			`  "x-envoy-decorator-operation",`,
			`  "x-envoy-downstream-service-cluster",`,
			`  "x-envoy-downstream-service-node",`,
			`  "x-envoy-external-address",`,
			`  "x-envoy-is-timeout-retry",`,
			`  "x-envoy-original-host",`,
			`  "x-envoy-original-path",`,
			`  "x-forwarded-for",`,
			`  "x-forwarded-host",`,
			`  "x-forwarded-port",`,
			`  "x-forwarded-proto",`,
			`  "x-request-id",`,
			"}",
			"local escaped = \"[\\\"<>^`{|}\\128-\\255]\"",
		},
		{`core.register_action("pf_join", {"http-req"}, function(txn)`},
		{`core.register_action("pf_route", {"http-req"}, function(txn, route, text)`},
	}
	if block := missing(plain, want); block != "" {
		t.Errorf("the script does not hold, in its place:\n%s", block)
	}
}

// Each pattern route runs the program Go's regexp/syntax compiles its pattern to, written as pf_route's argument after
// a comment that names the pattern, Go-quoted: the first instruction's number after s, then each instruction from 1,
// its kind's letter and its numbers. A run that sets nothing is answered 400, before the route's own rule reads it.
func TestPrograms(t *testing.T) {
	route := func(id int, comment, program string) []string {
		n := strconv.Itoa(id)
		return []string{
			"  # route " + n + " matches the pattern " + comment,
			"  http-request lua.pf_route " + n + " '" + program + "' if !{ var(txn.route) -m found }",
			"  http-request return status 400 if !{ var(txn.route) -m found } !{ var(txn.pattern" + n + ") -m found }",
			"  http-request set-var(txn.route) str(" + n + ") if !{ var(txn.route) -m found } { var(txn.pattern" + n + ") -m str 1 }",
		}
	}
	cases := []struct {
		name, proxyfile string
		want            []string
	}{
		{"a pattern's program", beforeApp(`  - {name: p, pattern: "^/a$", to: app}`),
			route(1, `"^/a$"`, "s2;f;e3,4;r4,47,47;r5,97,97;e6,8;m")},
		{"a class, a repeat and an alternative", beforeApp(`  - {name: p, pattern: "^/[b-dx]+|y$", to: app}`),
			route(1, `"^/[b-dx]+|y$"`, "s8;f;e3,4;r4,47,47;r5,98,100,120,120;a4,9;r7,121,121;e9,8;a2,6;m")},
		{"a byte above 0x7F, which a byte read never takes, and the quotes of the comment", beforeApp(`  - {name: p, pattern: "^/q\"\\\\é\t$", to: app}`),
			route(1, `"^/q\"\\\\é\t$"`, "s2;f;e3,4;r4,47,47;r5,113,113;r6,34,34;r7,92,92;r8;r9,9,9;e10,8;m")},
		{"a method route after a pattern route", beforeApp(`  - {name: p, pattern: "^/a$", to: app, method: POST}`),
			[]string{
				"  http-request lua.pf_route 1 's2;f;e3,4;r4,47,47;r5,97,97;e6,8;m' if !{ var(txn.route) -m found }",
				"  http-request return status 400 if !{ var(txn.route) -m found } !{ var(txn.pattern1) -m found }",
				"  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } { var(txn.pattern1) -m str 1 } { method POST }",
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := missing(generate(t, c.proxyfile), [][]string{c.want}); block != "" {
				t.Errorf("the output does not hold, in its place:\n%s", block)
			}
		})
	}
}

// An origin pattern's * is matched at the first place where the rest can follow, by an atomic group PCRE2 never tries
// again elsewhere, so the time grows with the Origin's length alone; one * needs no group.
func TestOriginPatterns(t *testing.T) {
	cases := []struct{ name, pattern, want string }{
		{"one *", "https://*.example.org", `  acl env1 req.fhdr(origin) -m reg -- '^https://.*\.example\.org$'`},
		{"two *", "https://*.*.example.org", `  acl env1 req.fhdr(origin) -m reg -- '^https://(?>.*?\.).*\.example\.org$'`},
		{"a * in the port", "http://*.example.com:*", `  acl env1 req.fhdr(origin) -m reg -- '^http://(?>.*?\.example\.com:).*$'`},
		{"three * and two side by side", "https://**.a*.example.org",
			`  acl env1 req.fhdr(origin) -m reg -- '^https://(?>.*?)(?>.*?\.a).*\.example\.org$'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, smallFile.Replaced(5, 5, `  all: {hosts: [localhost], patterns: ["`+c.pattern+`"]}`))
			if block := missing(out, [][]string{{c.want}}); block != "" {
				t.Errorf("the output does not hold:\n%s", block)
			}
		})
	}
}

// luaNames is each of names as a line of a Lua table: a string in double quotes and a comma.
func luaNames(names []string) []string {
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = `  "` + name + `",`
	}
	return lines
}

// A list of the proxyfile is written one value a line, so no line holds more words than HAProxy reads (65, measured on
// HAProxy 3.2.26 and 3.4.7): a file of 600 routes and an environment of 100 hosts, origins and patterns renders, and
// Generate's own scan of its words finds no line over.
func TestManyValues(t *testing.T) {
	document := testsupport.Document{"listen: 8000", "services:", "  app: {http: 3000, address: 127.0.0.1}", "origins:", "  all:"}
	var hosts, origins, patterns []string
	for i := 1; i <= 100; i++ {
		hosts = append(hosts, fmt.Sprintf("h%03d.example.com", i))
		origins = append(origins, fmt.Sprintf("https://o%03d.example.com", i))
		patterns = append(patterns, fmt.Sprintf("https://*.p%03d.example.com", i))
	}
	document = append(document, "    hosts: ["+strings.Join(hosts, ", ")+"]", "    origins: ["+strings.Join(origins, ", ")+"]",
		"    patterns: [\""+strings.Join(patterns, "\", \"")+"\"]", "routes:")
	for i := 1; i <= 600; i++ {
		document = append(document, fmt.Sprintf("  - {name: r%d, prefix: /r%d/, to: app}", i, i))
	}
	want := [][]string{
		{"  acl env1_host var(txn.host) -m str -- 'h001.example.com'", "  acl env1_host var(txn.host) -m str -- 'h002.example.com'"},
		{"  acl env1_host var(txn.host) -m str -- 'h100.example.com'", "  http-request set-var(txn.environment) str(env1) if env1_host"},
		{"  http-request set-var(txn.route) str(600) if !{ var(txn.route) -m found } { path_beg '/r600/' }"},
		{"  acl env1 req.fhdr(origin) -i -m str -- 'https://o100.example.com'"},
		{`  acl env1 req.fhdr(origin) -m reg -- '^https://.*\.p100\.example\.com$'`},
		{`  acl env1_own req.fhdr(origin) -m reg -- '^https?://h100\.example\.com(:[0-9]+)?$'`},
		{"  use_backend app_http if { var(txn.route) -m str 600 }"},
	}
	if block := missing(generate(t, document.String()), want); block != "" {
		t.Errorf("the output does not hold, in its place:\n%s", block)
	}
}

// A host written with a dot at its start or its end is compared as written, as Envoy compares it.
func TestHostsWithADot(t *testing.T) {
	out := generate(t, smallFile.Replaced(5, 5, "  all: {hosts: [.dot.example.com, dot.example.com.]}"))
	want := [][]string{{
		"  acl env1_host var(txn.host) -m str -- '.dot.example.com'",
		"  acl env1_host var(txn.host) -m str -- 'dot.example.com.'",
	}, {
		`  acl env1_own req.fhdr(origin) -m reg -- '^https?://\.dot\.example\.com(:[0-9]+)?$'`,
		`  acl env1_own req.fhdr(origin) -m reg -- '^https?://dot\.example\.com\.(:[0-9]+)?$'`,
	}}
	if block := missing(out, want); block != "" {
		t.Errorf("the output does not hold, in its place:\n%s", block)
	}
}

// A host the reader admits may start with -: each value of an acl line comes after --, so HAProxy reads -f, -i and -n
// as hosts, where HAProxy 3.4.7 read -f as a flag naming a pattern file and refused the file, and -i and -n as flags
// that left no host to match.
func TestHostsThatStartWithADash(t *testing.T) {
	out := generate(t, smallFile.Replaced(5, 5, "  all: {hosts: [-f, -i, -n]}"))
	want := [][]string{{
		"  acl env1_host var(txn.host) -m str -- '-f'",
		"  acl env1_host var(txn.host) -m str -- '-i'",
		"  acl env1_host var(txn.host) -m str -- '-n'",
		"  http-request set-var(txn.environment) str(env1) if env1_host",
	}, {
		`  acl env1_own req.fhdr(origin) -m reg -- '^https?://-f(:[0-9]+)?$'`,
		`  acl env1_own req.fhdr(origin) -m reg -- '^https?://-i(:[0-9]+)?$'`,
		`  acl env1_own req.fhdr(origin) -m reg -- '^https?://-n(:[0-9]+)?$'`,
	}}
	if block := missing(out, want); block != "" {
		t.Errorf("the output does not hold, in its place:\n%s", block)
	}
}

// A value written into a regular expression is refused by its key when the expression is longer than 16384 bytes:
// HAProxy 3.2.26 and 3.4.7 loaded an expression of 32700 letters and refused one of 32800 (PCRE2's compiled limit), and
// a letter, the form that grows most, takes two bytes compiled.
func TestLongValues(t *testing.T) {
	long := strings.Repeat("a", 16384)
	cases := []struct{ name, proxyfile, want string }{
		{"a host", smallFile.Replaced(5, 5, "  all: {hosts: ["+long+"]}"),
			`proxyfile.yaml:5:3: "origins.<environment name>.hosts" whose HAProxy regular expression is longer than HAProxy loads is refused by --target haproxy: ` + longRegex},
		{"a pattern", smallFile.Replaced(5, 5, `  all: {hosts: [localhost], patterns: ["https://*.`+long+`"]}`),
			`proxyfile.yaml:5:3: "origins.<environment name>.patterns" whose HAProxy regular expression is longer than HAProxy loads is refused by --target haproxy: ` + longRegex},
		{"a prefix with a rewrite", beforeApp("  - {name: q, prefix: /" + long + "/, to: app, rewrite: /x/}"),
			`proxyfile.yaml:7:23: "routes[].prefix" whose HAProxy regular expression is longer than HAProxy loads is refused by --target haproxy: ` + longRegex},
		// Seven runs of 1000 letters compile to 7000 byte reads of 11 bytes each, past the 65536 bytes the file writes.
		{"a pattern whose program is longer than the file writes", beforeApp(`  - {name: q, pattern: "^/` + strings.Repeat("[a-z]{1000}", 7) + `$", to: app}`),
			`proxyfile.yaml:7:24: "routes[].pattern" whose program is longer than this version writes is refused by --target haproxy: ` +
				"this version's HAProxy file carries a pattern's program of at most 65536 bytes; write a shorter pattern, or render it for envoy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := haproxy.Generate(input(t, c.proxyfile))
			var refused generator.Refused
			if !errors.As(err, &refused) {
				t.Fatalf("Generate: %v, want a generator.Refused", err)
			}
			if got := filepath.Base(refused.Error()); got != c.want {
				t.Errorf("Generate refused:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

const longRegex = "HAProxy loads no regular expression longer than 16384 bytes as the file writes it; write a shorter value, or render it for envoy"

// The forms the generator writes for the keys of the format, each on a file that writes it.
func TestForms(t *testing.T) {
	const tls = "tls: {certificate: /etc/gateway/tls/certificate.pem, key: /etc/gateway/tls/key.pem}"
	// Without timeouts.request a service has 15 s of silence, Envoy's route timeout; with none, Envoy's 5 minutes.
	defaults := func(server string) []string {
		return []string{
			"defaults",
			"  mode http",
			"  retries 0",
			"  option http-server-close",
			"  option http-pretend-keepalive",
			"  timeout connect 5s",
			"  timeout client 300s",
			"  timeout server " + server,
			"  option http-drop-request-trailers",
			"  http-error status 503",
			"  http-error status 502",
			"",
		}
	}
	cases := []struct {
		name      string
		proxyfile string
		want      [][]string
		none      []string // no line holds any of these
	}{
		{name: "minimal", proxyfile: small(),
			want: [][]string{
				{"  bind :8000"},
				{"  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } { path_beg '/' }"},
				{"  use_backend app_http if { var(txn.route) -m str 1 }"},
				{"backend app_http", "  server app1 127.0.0.1:3000"},
			},
			none: []string{"option forwardfor", "crt-store", "ssl-default-bind-ciphers", "ssl-default-bind-curves", "frontend admin", "x-envoy-original-path"}},
		{name: "forwarded by default without tls", proxyfile: small(),
			want: [][]string{
				// No rule reads the client's address, so the file sets none: the service receives the client's
				// X-Forwarded-For as it came, as from Envoy.
				{"  # client_address: forwarded", "  http-request set-header x-forwarded-proto http unless { req.hdr(x-forwarded-proto) -m found }"},
				{"  http-request set-header x-request-id %[uuid(4)] unless { req.hdr(x-request-id) -m found }"},
				{"  http-request set-var(txn.request_id) req.fhdr(x-request-id)"},
			},
			none: []string{"set-src"}},
		{name: "forwarded with tls", proxyfile: small(tls, "client_address: forwarded"),
			want: [][]string{
				{"  http-request set-header x-forwarded-proto https unless { req.hdr(x-forwarded-proto) -m found }"},
			}},
		{name: "connection without tls", proxyfile: small("client_address: connection"),
			want: [][]string{
				delHeaders(generator.RemovedByEnvoyFacingClients),
				{"  http-request set-header x-forwarded-for %[src] unless { req.fhdr(x-forwarded-for) -m len gt 0 }"},
				{"  http-request set-header x-forwarded-proto http"},
			},
			none: []string{"option forwardfor"}},
		{name: "tls without clients", proxyfile: small(tls),
			want: [][]string{
				{
					"  ssl-default-bind-ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305",
					"  ssl-default-bind-curves X25519MLKEM768:X25519:P-256",
				},
				{"crt-store", "  load crt '/etc/gateway/tls/certificate.pem' key '/etc/gateway/tls/key.pem' alias public"},
				{"  bind :8000 ssl crt @/public alpn h2,http/1.1"},
			}},
		{name: "the grpc example", proxyfile: string(testsupport.ReadFile(t, "../../examples/grpc/yaml/proxyfile.yaml")),
			want: [][]string{
				{"  bind :8000"},
				{
					"  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } { path '/example.orders.v1.OrderService/GetOrder' }",
					"  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } { path '/example.orders.v1.OrderService/ListOrders' }",
				},
				{"  use_backend orders_grpc if { var(txn.route) -m str 1 }"},
				{"  server orders1 127.0.0.1:9000 proto h2 max-reuse 0"},
			}},
		{name: "host_header on the service's backends only", proxyfile: testsupport.Document{
			"listen: 8000",
			"services:",
			"  app: {http: 3000, address: 127.0.0.1}",
			`  api: {http: 3001, grpc: 3002, host_header: "api.internal:443"}`,
			"origins:",
			"  all: {hosts: [localhost]}",
			"routes:",
			"  - {name: api, prefix: /api/, to: api}",
			`  - rpc: {to: {"*": api}}`,
			"  - {name: app, prefix: /, to: app}",
			"rpc:",
			"  methods:",
			"    - {service: example.v1.Api, method: Get}",
		}.String(),
			want: [][]string{
				{"  use_backend api_http if { var(txn.route) -m str 1 }", "  use_backend api_grpc if { var(txn.route) -m str 2 }", "  use_backend app_http if { var(txn.route) -m str 3 }"},
				{"backend app_http", "  server app1 127.0.0.1:3000", ""},
				{"backend api_http", "  http-request set-header host 'api.internal:443'", "  server api1 127.0.0.1:3001", ""},
				{"backend api_grpc", "  http-request set-header host 'api.internal:443'", "  server api1 127.0.0.1:3002 proto h2 max-reuse 0", ""},
			}},
		{name: "an IPv6 address", proxyfile: smallFile.Replaced(3, 3, `  app: {http: 3000, addresses: ["::1", "fd00::2"]}`),
			want: [][]string{{"  server app1 [::1]:3000", "  server app2 [fd00::2]:3000"}}},
		{name: "a prefix in remove_from_clients", proxyfile: small("remove_from_clients: [x-debug-*, x-secret]"),
			want: [][]string{{"  http-request del-header 'x-debug-' -m beg", "  http-request del-header 'x-secret'"}}},
		{name: "admin", proxyfile: small("admin: 9901"),
			want: [][]string{{"frontend admin", "  bind 127.0.0.1:9901", "  monitor-uri /ready", "  http-request return status 404", ""}}},
		{name: "no timeouts", proxyfile: small(), want: [][]string{defaults("15s")}},
		{name: "timeouts.request none", proxyfile: small("timeouts: {request: none}"), want: [][]string{defaults("300s")}},
		{name: "an empty timeouts", proxyfile: small("timeouts: {}"), want: [][]string{defaults("15s")}},
		{name: "origins and no cors", proxyfile: string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml")),
			want: [][]string{{
				"  # browser origins: none, an environment's own host, or its origins and patterns",
				"  acl env1 req.fhdr(origin) -i -m str -- 'http://localhost:3000'",
				"  acl env1_own req.fhdr(origin) -m reg -- '^https?://localhost(:[0-9]+)?$'",
				"  http-request set-var(txn.cors) bool(true) if { var(txn.environment) -m str env1 } env1",
				"  http-request set-var(txn.admitted) bool(true) if !has_origin",
				"  http-request set-var(txn.admitted) bool(true) if { var(txn.cors) -m bool }",
				"  http-request set-var(txn.admitted) bool(true) if { var(txn.environment) -m str env1 } env1_own",
				`  http-request return status 403 content-type text/plain string "Invalid CORS request" if !{ var(txn.admitted) -m bool }`,
				"  http-request return status 400 if dot_segment",
				"  acl preflight method OPTIONS",
				"  acl preflight_method req.fhdr(access-control-request-method) -m len gt 0",
				`  http-request return status 200 hdr access-control-allow-origin "%[req.fhdr(origin)]" if has_origin preflight preflight_method { var(txn.cors) -m bool }`,
				"  http-request return status 200 if has_origin preflight preflight_method",
				"  http-request set-var(txn.origin) req.fhdr(origin) if { var(txn.cors) -m bool }",
				`  http-after-response set-header access-control-allow-origin "%[var(txn.origin)]" if { var(txn.origin) -m found }`,
				"  http-after-response add-header vary Origin",
			}}},
		{name: "cors with lists and only an environment's own hosts", proxyfile: small(`cors: {allow_methods: "GET,POST", allow_headers: "content-type", max_age: "60"}`),
			want: [][]string{{
				"  # browser origins: none, an environment's own host, or its origins and patterns",
				"  acl env1_own req.fhdr(origin) -m reg -- '^https?://localhost(:[0-9]+)?$'",
				"  http-request set-var(txn.admitted) bool(true) if !has_origin",
				"  http-request set-var(txn.admitted) bool(true) if { var(txn.environment) -m str env1 } env1_own",
				`  http-request return status 403 content-type text/plain string "Invalid CORS request" if !{ var(txn.admitted) -m bool }`,
				"  http-request return status 400 if dot_segment",
				"  acl preflight method OPTIONS",
				"  acl preflight_method req.fhdr(access-control-request-method) -m len gt 0",
				"  http-request return status 200 hdr access-control-allow-methods 'GET,POST' hdr access-control-allow-headers 'content-type' hdr access-control-max-age '60' if has_origin preflight preflight_method",
				"  http-after-response add-header vary Origin",
			}}},
		// Envoy refuses another site's origin in every file, with or without cors or a list (measured on Envoy 1.40.2
		// with examples/grpc), so only the lines that read a list depend on one.
		{name: "no cors and no listed origin: the origin check and Vary all the same", proxyfile: small(),
			want: [][]string{{
				"  # browser origins: none, an environment's own host, or its origins and patterns",
				"  acl env1_own req.fhdr(origin) -m reg -- '^https?://localhost(:[0-9]+)?$'",
				"  http-request set-var(txn.admitted) bool(true) if !has_origin",
				"  http-request set-var(txn.admitted) bool(true) if { var(txn.environment) -m str env1 } env1_own",
				`  http-request return status 403 content-type text/plain string "Invalid CORS request" if !{ var(txn.admitted) -m bool }`,
				"  http-request return status 400 if dot_segment",
				"  acl preflight method OPTIONS",
				"  acl preflight_method req.fhdr(access-control-request-method) -m len gt 0",
				"  http-request return status 200 if has_origin preflight preflight_method",
				"  http-after-response add-header vary Origin",
			}}},
		// render passes an rpc entry whose methods are given but empty; the file then has no use_backend, and HAProxy
		// 3.2.26 and 3.4.7 load it with warnings fatal and answer 404 to every path (measured).
		{name: "an rpc entry with no method", proxyfile: testsupport.Document{
			"listen: 8000",
			"services:",
			"  orders: {grpc: 9000}",
			"origins:",
			"  all: {hosts: [localhost]}",
			"routes:",
			`  - rpc: {to: {"*": orders}}`,
			"rpc:",
			"  methods: []",
		}.String(),
			want: [][]string{{
				"  # the routes, in the written order: the first that matches is the request's route",
				"  # a dot segment with a ; parameter, written or escaped once, reaches no service",
				`  acl dot_segment path_reg '/\.\.?(;|%3[Bb])'`,
				"  http-request return status 404 if !{ var(txn.route) -m found } !dot_segment",
			}, {
				"  # a service that cannot be reached: 502 with an empty body",
				"  http-after-response set-status 502 if { txn.sess_term_state -m reg ^S[CH] } || { txn.sess_term_state -m str sC }",
				"",
				"backend orders_grpc",
				"  server orders1 127.0.0.1:9000 proto h2 max-reuse 0",
				"",
			}},
			none: []string{"use_backend", "set-var(txn.route)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.proxyfile)
			if block := missing(out, c.want); block != "" {
				t.Errorf("the output does not hold, in its place:\n%s", block)
			}
			for _, line := range strings.Split(out, "\n") {
				for _, text := range c.none {
					if strings.Contains(line, text) {
						t.Errorf("the line %q holds %q", line, text)
					}
				}
			}
		})
	}
}

// Every value from the proxyfile reaches HAProxy as written: single-quoted, so no character of it is read as the
// configuration's own, and in a log-format argument with % written %%, so none is read as a sample.
func TestQuoting(t *testing.T) {
	const route1 = "  http-request set-var(txn.route) str(1) if !{ var(txn.route) -m found } "
	cases := []struct {
		name      string
		proxyfile string
		want      [][]string
	}{
		{"a single quote in a prefix", beforeApp(`  - {name: q, prefix: "/it's/", to: app}`),
			[][]string{{route1 + `{ path_beg '/it'\''s/' }`}}},
		{"a double quote, braces and a # in a prefix", beforeApp(`  - {name: q, prefix: '/a"b{c}#d/', to: app}`),
			[][]string{{route1 + `{ path_beg '/a"b{c}#d/' }`}}},
		{"a space in a prefix", beforeApp(`  - {name: q, prefix: "/sp ace/", to: app}`),
			[][]string{{route1 + `{ path_beg '/sp ace/' }`}}},
		{"a backslash in a path, a backslash and a % in its rewrite", beforeApp(`  - {name: q, path: '/q\w', to: app, rewrite: '/h%2F\x'}`),
			[][]string{
				{route1 + `{ path '/q\w' }`},
				{`  http-request replace-path '^/q\\w(.*)$' '/h%%2F\\x\1' if { var(txn.route) -m str 1 }`},
			}},
		{"a % in a cors value", small(`cors: {allow_methods: "GET,X%41"}`),
			[][]string{{"  http-request return status 200 hdr access-control-allow-methods 'GET,X%%41' if has_origin preflight preflight_method"}}},
		{"a single quote in a tls path", small("tls: {certificate: \"/etc/it's/certificate.pem\", key: /etc/gateway/tls/key.pem}"),
			[][]string{{`  load crt '/etc/it'\''s/certificate.pem' key '/etc/gateway/tls/key.pem' alias public`}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := missing(generate(t, c.proxyfile), c.want); block != "" {
				t.Errorf("the output does not hold, in its place:\n%s", block)
			}
		})
	}
}

// An environment's name names nothing in the file: its ACLs and its value of txn.environment are named by its place,
// env1 onwards, and its name is written only in a comment. The format admits any name of one character or more with
// no control character but tab, so a name HAProxy would read as its own (or, preflight) or could not hold in an ACL
// name renders as any other.
func TestEnvironmentNames(t *testing.T) {
	const ownHost = "  acl env1_own req.fhdr(origin) -m reg -- '^https?://localhost(:[0-9]+)?$'"
	cases := []struct{ name, environment, comment string }{
		{"or", "or", `  # env1: "or"`},
		{"preflight, one of the file's own ACLs", "preflight", `  # env1: "preflight"`},
		{"a space, a quote, a # and a tab", `"my 'env' #1\tx"`, `  # env1: "my 'env' #1\tx"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, smallFile.Replaced(5, 5, "  "+c.environment+": {hosts: [localhost]}"))
			want := [][]string{
				{c.comment, "  acl env1_host var(txn.host) -m str -- 'localhost'", "  http-request set-var(txn.environment) str(env1) if env1_host"},
				{ownHost},
				{"  http-request set-var(txn.admitted) bool(true) if { var(txn.environment) -m str env1 } env1_own"},
			}
			if block := missing(out, want); block != "" {
				t.Errorf("the output does not hold, in its place:\n%s", block)
			}
		})
	}
}

// A form the generator does not write is an error, never a file without it: the declaration refuses each before
// render calls Generate, so the error is a bug in proxyfile, and a file that left the form out would serve requests
// the proxyfile limits, checks or refuses.
func TestRefusedForms(t *testing.T) {
	cases := []struct {
		name, proxyfile, want string
	}{
		{"an address naming a host", smallFile.Replaced(3, 3, "  app: {http: 3000, address: app.internal}"),
			`haproxy: service app: address "app.internal" is not an IP address`},
		{"a whole request's time", small("timeouts: {request: 30s}"),
			"haproxy: timeouts.request as a time is not written in this version"},
		{"every other key of the full example this version does not write",
			string(testsupport.ReadFile(t, "../../examples/full/yaml/proxyfile.yaml")),
			strings.Join([]string{
				"haproxy: limits.connections is not written in this version",
				"haproxy: limits.request_headers is not written in this version",
				"haproxy: timeouts.idle is not written in this version",
				"haproxy: timeouts.upstream_idle is not written in this version",
				"haproxy: dns.resolver is not written in this version",
				"haproxy: auth.ask is not written in this version",
				"haproxy: auth.jwt is not written in this version",
				"haproxy: log is not written in this version",
				"haproxy: tracing is not written in this version",
				"haproxy: service shop: balance least_requests is not written in this version",
				"haproxy: service shop: health is not written in this version",
				`haproxy: service catalog: address "catalog.internal.example" is not an IP address`,
				"haproxy: service catalog: balance hash is not written in this version",
				"haproxy: service catalog: hash is not written in this version",
				`haproxy: route "accounts-events": auth is not written in this version`,
				"haproxy: the rpc entry: retries is not written in this version",
				"haproxy: the rpc entry: rate_limit is not written in this version",
				`haproxy: route "accounts-webhooks": auth is not written in this version`,
				`haproxy: route "catalog": rate_limit is not written in this version`,
				`haproxy: route "shop": retries is not written in this version`,
				"haproxy: method example.shop.v1.CartService/AddItem: action is not written in this version",
				"haproxy: method example.shop.v1.CartService/AddItem: reports is not written in this version",
				"haproxy: method example.shop.v1.CartService/GetCart: action is not written in this version",
			}, "\n")},
		// The reader admits an IPv6 address with a zone, and HAProxy 3.2.26 and 3.4.7 refuse to load one in every form
		// tried, [::1%lo]:3000, ::1%lo:3000 and ipv6@::1%lo:3000, with an interface that exists (measured).
		{"an IPv6 address with a zone", smallFile.Replaced(3, 3, `  app: {http: 3000, address: "::1%lo"}`),
			`haproxy: service app: address "::1%lo" names a zone, which HAProxy 3.2 and 3.4 load in no server address`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files, err := haproxy.Generate(input(t, c.proxyfile))
			if err == nil {
				t.Fatalf("Generate returned %d files and no error, want the error %q", len(files), c.want)
			}
			if err.Error() != c.want {
				t.Errorf("Generate's error:\n%s\nwant:\n%s", err, c.want)
			}
		})
	}
}
