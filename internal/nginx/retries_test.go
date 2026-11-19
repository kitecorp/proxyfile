package nginx_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The rows of an HTTP route's retries, routes[].retries of 1 or more: nginx's own retry stays off, the route's
// error_page jumps only when the failed try sent the service nothing, to a server group without the backends already
// tried, through copies of the route's location that never ask the check or charge the limit again. These rows hold
// the text. The battery runs what the lines do on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 for routes of two
// backends: a try refused, or timed out connecting, going on to the other backend, a request answered once both
// tries timed out connecting, a body read whole or streamed, and a try that reached the service not repeated. It
// runs no second jump, which needs a third backend, so nor the cap of six jumps; nor the time left after a try, as
// both tries of its row end at the connect time, which every copy shares; nor a service by DNS name, the second
// guard $pf_rt_fits, the 413 of an HTTP/2 body of no stated length past 1 MiB, the log flag of the spent answer, or
// how many times the check is asked: for those these rows hold the text alone, and the measures their comments name
// what it does.

// retriedFile is a proxyfile with one environment, the services given and the routes given, and the rest after them.
func retriedFile(services, routes, rest string) string {
	return "listen: 8000\nservices:\n" + services + "origins:\n  local: {hosts: [localhost]}\nroutes:\n" + routes + rest
}

// copiesFile has a checked and limited route with no rewrite (1), a route with a rewrite (2) and a route to a service
// with host_header (3), each retried once to a service of two backends.
var copiesFile = retriedFile(
	"  k2: {http: 8080, addresses: [\"10.0.0.14\", \"10.0.0.11\"]}\n"+
		"  hh: {http: 8080, addresses: [\"10.0.0.14\", \"10.0.0.11\"], host_header: \"svc.example\"}\n"+
		"  checks: {http: 9000}\n",
	"  - {name: r1, prefix: /r1/, to: k2, retries: 1, rate_limit: {requests: 10, per: 1m, by: all}}\n"+
		"  - {name: rw, prefix: /rw/, rewrite: /v2/, to: k2, retries: 1}\n"+
		"  - {name: hh, prefix: /hh/, to: hh, retries: 1}\n",
	"auth: {ask: checks /auth, timeout: 1s, enabled: true, when_unavailable: deny, accept: [x-user-id]}\n")

// locationOf returns the lines of the location name of out's first environment server, from its opening line to its
// closing brace, or nil when it holds none. Every environment server writes the same locations.
func locationOf(out, name string) []string {
	servers := environmentServers(out)
	if len(servers) == 0 {
		return nil
	}
	found := blocks(servers[0], "    location "+name+" {")
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

// locationNames returns the names of the locations of out's first environment server that pattern matches, its first
// group, in the order written.
func locationNames(out string, pattern *regexp.Regexp) []string {
	var names []string
	for _, line := range environmentServers(out)[0] {
		if found := pattern.FindStringSubmatch(line); found != nil {
			names = append(names, found[1])
		}
	}
	return names
}

// holdsAny returns the first line of location that starts with one of prefixes, or "".
func holdsAny(location []string, prefixes ...string) string {
	for _, line := range location {
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
	}
	return ""
}

// endsWith says how location's last lines differ from want, or "" when they are want.
func endsWith(name string, location, want []string) string {
	if len(location) < len(want) || !slices.Equal(location[len(location)-len(want):], want) {
		return fmt.Sprintf("%s ends %q, want %q", name, location[max(0, len(location)-len(want)):], want)
	}
	return ""
}

// bodyLines are the lines a retried route location and each of its copies write before the line that passes the
// request on: a body of at most 1 MiB, read whole into a buffer one kilobyte larger, so nginx writes no temporary file.
var bodyLines = []string{"      client_max_body_size 1m;", "      client_body_buffer_size 1025k;", "      proxy_request_buffering on;"}

// TestRetryCopies: a retried route location reads the request's body whole before the first try, sends it for as long
// as it reads (TestRetryTime), and sends nginx's 502 and 504 to the target the maps pick; after it come its copies, -rt for a try with the route's whole time left and
// -rt<k> for one with k seconds of it spent, then -once, the route location as a route with no retries writes it. A
// copy passes the request to the group of the next backend, with the accepted header and the unchecked mark the
// handler set, and neither asks the check nor charges the limit again; it holds no rewrite of the route's, so the path
// it sends is the one the route location rewrote, and a -uri copy keeps the -uri rewrite, which gives the same path
// again; its Host is the route location's.
func TestRetryCopies(t *testing.T) {
	out := generate(t, copiesFile)
	tail := slices.Concat([]string{"      proxy_send_timeout 15s;"}, bodyLines,
		[]string{"      proxy_pass http://k2_http;", "      error_page 502 504 = @pf_$pf_rt_target;", "    }"})
	for _, name := range []string{"@pf_r_1", "@pf_r_1-uri", "@pf_r_2"} {
		location := locationOf(out, name)
		if difference := endsWith(name, location, tail); difference != "" {
			t.Error(difference)
		}
		if line := holdsAny(location, "      error_page 502 =", "      error_page 504 ="); line != "" {
			t.Errorf("%s holds %q", name, line)
		}
	}

	// One jump at the default timers, 15 s and 5 s to connect: one copy for each second a try can take.
	names := locationNames(out, regexp.MustCompile(`^    location (@pf_r_1(?:-[a-z0-9-]+)?) \{$`))
	want := []string{"@pf_r_1", "@pf_r_1-rt", "@pf_r_1-rt1", "@pf_r_1-rt2", "@pf_r_1-rt3", "@pf_r_1-rt4", "@pf_r_1-rt5", "@pf_r_1-once",
		"@pf_r_1-uri", "@pf_r_1-uri-rt", "@pf_r_1-uri-rt1", "@pf_r_1-uri-rt2", "@pf_r_1-uri-rt3", "@pf_r_1-uri-rt4", "@pf_r_1-uri-rt5", "@pf_r_1-uri-once"}
	if !slices.Equal(names, want) {
		t.Errorf("the locations of route 1 are %q, want %q", names, want)
	}

	copyTail := slices.Concat(bodyLines, []string{"      proxy_pass http://$pf_rt_group;", "      error_page 502 504 = @pf_$pf_rt_target;", "    }"})
	for _, c := range []struct {
		name   string
		timers []string // the lines before copyTail
	}{
		{"@pf_r_1-rt", []string{"      proxy_send_timeout 15s;"}},
		{"@pf_r_1-rt1", []string{"      proxy_read_timeout 14s;", "      proxy_send_timeout 14s;"}},
		{"@pf_r_1-rt5", []string{"      proxy_read_timeout 10s;", "      proxy_send_timeout 10s;"}},
		{"@pf_r_1-uri-rt3", []string{"      proxy_read_timeout 12s;", "      proxy_send_timeout 12s;"}},
		{"@pf_r_2-rt", []string{"      proxy_send_timeout 15s;"}},
	} {
		location := locationOf(out, c.name)
		ends := slices.Concat(c.timers, copyTail)
		if difference := endsWith(c.name, location, ends); difference != "" {
			t.Error(difference)
		} else if before := location[len(location)-len(ends)-1]; strings.Contains(before, "_timeout ") {
			t.Errorf("%s holds %q before %q", c.name, before, ends[0])
		}
		if line := holdsAny(location, "      js_access", "      limit_req", "      error_page 429", "      default_type", "      proxy_connect_timeout"); line != "" {
			t.Errorf("%s holds %q", c.name, line)
		}
	}
	for _, name := range []string{"@pf_r_1-rt", "@pf_r_1-rt5", "@pf_r_1-uri-rt1", "@pf_r_2-rt"} {
		location := locationOf(out, name)
		for _, line := range []string{`      set $pf_log_upstream "k2_http";`, `      proxy_set_header "x-user-id" $pf_accept_0;`,
			"      proxy_set_header x-envoy-auth-failure-mode-allowed $pf_unchecked;"} {
			if !slices.Contains(location, line) {
				t.Errorf("%s holds no %q", name, line)
			}
		}
	}

	for _, c := range []struct{ name, rewrite string }{
		{"@pf_r_1-rt", ""},
		{"@pf_r_2-rt", ""},
		{"@pf_r_2-rt3", ""},
		{"@pf_r_1-uri-rt2", "      rewrite ^ $uri break;"},
	} {
		if line := holdsAny(locationOf(out, c.name), "      rewrite "); line != c.rewrite {
			t.Errorf("%s's rewrite is %q, want %q", c.name, line, c.rewrite)
		}
	}
	if line := holdsAny(locationOf(out, "@pf_r_2"), "      rewrite "); line == "" {
		t.Error("@pf_r_2 holds no rewrite")
	}
	for _, name := range []string{"@pf_r_3", "@pf_r_3-rt", "@pf_r_3-rt5", "@pf_r_3-uri-rt"} {
		if line := holdsAny(locationOf(out, name), "      proxy_set_header Host "); line != `      proxy_set_header Host "svc.example";` {
			t.Errorf("%s's Host line is %q", name, line)
		}
	}
}

// capFile is a proxyfile whose one route r1 to the service s is retried retries times; s has the addresses given.
func capFile(addresses, retries string) string {
	return retriedFile("  s: {http: 8080, "+addresses+"}\n", "  - {name: r1, prefix: /r1/, to: s, retries: "+retries+"}\n", "")
}

// TestRetryCap: a route jumps to a next backend at most once for each backend it has not tried, and at most 6 times,
// as nginx follows 10 internal redirects and the chain of a request that fails every try takes the 418, each jump,
// @pf_fail, @pf_504 and @pf_502 (measured on nginx 1.30.6 and 1.31.8: 7 jumps answered 500 after 11 redirects). A
// service of one backend is not tried again, and its route location still sends nginx's 502 and 504 to the target,
// which answers as for a route whose retries ran out; a service found by DNS name has no count of backends, so its
// route jumps up to 6 times, to the same upstream. The jumps are found by the location the route choice names, in a
// hash; a request the server's jump sent to -once has none.
func TestRetryCap(t *testing.T) {
	two := `addresses: ["10.0.0.1", "10.0.0.2"]`
	eight := `addresses: ["10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6", "10.0.0.7", "10.0.0.8"]`
	ten := `addresses: ["10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6", "10.0.0.7", "10.0.0.8", "10.0.0.9", "10.0.0.10"]`
	for _, c := range []struct {
		name, file string
		jumps      string
	}{
		{"2 backends, retries 1", capFile(two, "1"), "1"},
		{"2 backends, retries 4294967295", capFile(two, "4294967295"), "1"},
		{"8 backends, retries 7", capFile(eight, "7"), "6"},
		{"10 backends, retries 3", capFile(ten, "3"), "3"},
		{"1 backend, retries 1", capFile(`address: "10.0.0.1"`, "1"), "0"},
		{"a DNS name, retries 9", capFile(`address: catalog.internal.example`, "9") + "dns: {resolver: \"10.0.0.2:53\"}\n", "6"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.file)
			want := []string{"  map $pf_route $pf_rt_jumps {", "    1 " + c.jumps + ";", "    1-uri " + c.jumps + ";", `    default "";`, "  }"}
			if difference := blockDifference(out, want); difference != "" {
				t.Error(difference)
			}
			if line := `  map $pf_rt_once $pf_rt_cap { "" $pf_rt_jumps; default ""; }`; !hasLine(out, line) {
				t.Errorf("no line %q", line)
			}
		})
	}
	one := generate(t, capFile(`address: "10.0.0.1"`, "1"))
	if location := locationOf(one, "@pf_r_1-rt"); location != nil {
		t.Errorf("a service of one backend has a copy: %q", location)
	}
	if !slices.Contains(locationOf(one, "@pf_r_1"), "      error_page 502 504 = @pf_$pf_rt_target;") {
		t.Errorf("@pf_r_1 of a service of one backend is %q, want the target's error_page in it", locationOf(one, "@pf_r_1"))
	}
}

// groupsFile has a service of two IPv4 backends, IPv6 backends written in nginx's form and otherwise, the forms nginx
// prints with the last 32 bits dotted, a service found by DNS name and a balanced one, each to a route retried once,
// and an RPC route (7) retried once to the IPv4 service's gRPC port.
var groupsFile = retriedFile(
	"  k2: {http: 8080, grpc: 8081, addresses: [\"10.0.0.14\", \"10.0.0.11\"]}\n"+
		"  v6: {http: 8080, addresses: [\"FD25:59:1:1:0:0:0:14\", \"fd25:0059:0001:0001:0001:0000:0001:0012\"]}\n"+
		"  m4: {http: 8080, addresses: [\"::10.0.0.11\", \"::1\"]}\n"+
		"  mp: {http: 8080, addresses: [\"::ffff:10.0.0.11\", \"10.0.0.12\"]}\n"+
		"  catalog: {http: 8090, address: catalog.internal.example}\n"+
		"  lb: {http: 8080, addresses: [\"10.0.0.21\", \"10.0.0.22\"], balance: least_requests}\n",
	"  - {name: k2, prefix: /k2/, to: k2, retries: 1}\n"+
		"  - {name: v6, prefix: /v6/, to: v6, retries: 1}\n"+
		"  - {name: m4, prefix: /m4/, to: m4, retries: 1}\n"+
		"  - {name: mp, prefix: /mp/, to: mp, retries: 1}\n"+
		"  - {name: catalog, prefix: /catalog/, to: catalog, retries: 1}\n"+
		"  - {name: lb, prefix: /lb/, to: lb, retries: 1}\n"+
		"  - rpc: {to: {\"*\": k2}, retries: 1}\n",
	"dns: {resolver: \"10.0.0.2:53\"}\nrpc:\n  methods:\n    - {service: example.v1.S, method: Call}\n")

// TestRetryGroups: each backend of a service a retried route goes to is an upstream of its own, written as the
// service's upstream writes a backend, which the jump picks by the upstream the route sends to and the address of the
// last try, as $upstream_addr prints it: nginx's ngx_inet6_ntop prints an IPv6 address in lower case with the longest
// run of two or more zero groups as :: (RFC 5952), and the last 32 bits dotted where the first 80 bits are zero and the
// next 16 are ffff, or the first 96 are zero (src/core/ngx_inet.c, nginx 1.30.6 and 1.31.8). The key names the group
// of the next backend in the service's order. A service found by DNS name has one key, its own upstream. A balanced
// service balances its own upstream, and a group holds one backend. A retried RPC route keeps nginx's own retry. A
// service whose name makes a group's key longer than nginx reads is refused by its name, as its upstream's name is.
func TestRetryGroups(t *testing.T) {
	out := generate(t, groupsFile)
	for _, want := range [][]string{
		{"  upstream k2_http_b1 {", "    server 10.0.0.14:8080 max_fails=0;", "    keepalive 32;", "    keepalive_timeout 1h;", "  }"},
		{"  upstream k2_http_b2 {", "    server 10.0.0.11:8080 max_fails=0;", "    keepalive 32;", "    keepalive_timeout 1h;", "  }"},
		{"  upstream v6_http_b1 {", "    server [FD25:59:1:1:0:0:0:14]:8080 max_fails=0;", "    keepalive 32;", "    keepalive_timeout 1h;", "  }"},
		{"  upstream lb_http_b1 {", "    server 10.0.0.21:8080 max_fails=0;", "    keepalive 32;", "    keepalive_timeout 1h;", "  }"},
		{"  upstream lb_http {", "    random two least_conn;", "    server 10.0.0.21:8080 max_fails=0;", "    server 10.0.0.22:8080 max_fails=0;",
			"    keepalive 32;", "    keepalive_timeout 1h;", "  }"},
	} {
		if difference := blockDifference(out, want); difference != "" {
			t.Error(difference)
		}
	}
	want := []string{
		`  map "$pf_log_upstream|$pf_rt_last" $pf_rt_group {`,
		"    volatile;",
		`    "k2_http|10.0.0.14:8080" k2_http_b2;`,
		`    "k2_http|10.0.0.11:8080" k2_http_b1;`,
		`    "v6_http|[fd25:59:1:1::14]:8080" v6_http_b2;`,
		`    "v6_http|[fd25:59:1:1:1:0:1:12]:8080" v6_http_b1;`,
		`    "m4_http|[::10.0.0.11]:8080" m4_http_b2;`,
		`    "m4_http|[::1]:8080" m4_http_b1;`,
		`    "mp_http|[::ffff:10.0.0.11]:8080" mp_http_b2;`,
		`    "mp_http|10.0.0.12:8080" mp_http_b1;`,
		`    "~^catalog_http\\|" catalog_http;`,
		`    "lb_http|10.0.0.21:8080" lb_http_b2;`,
		`    "lb_http|10.0.0.22:8080" lb_http_b1;`,
		`    default "";`,
		"  }",
	}
	if difference := blockDifference(out, want); difference != "" {
		t.Error(difference)
	}
	for _, pattern := range []string{`(?m)^  upstream catalog_http_b[0-9]+ \{$`, `(?m)^  upstream k2_grpc_b[0-9]+ \{$`} {
		if found := regexp.MustCompile(pattern).FindString(out); found != "" {
			t.Errorf("the file holds %q", found)
		}
	}
	rpc := []string{"      grpc_next_upstream error timeout;", "      grpc_next_upstream_tries 2;", "      grpc_pass grpc://k2_grpc;",
		"      error_page 502 = @pf_502$pf_grpc_call;", "      error_page 504 = @pf_504;", "    }"}
	for _, name := range []string{"@pf_r_7", "@pf_r_7-uri"} {
		location := locationOf(out, name)
		if difference := endsWith(name, location, rpc); difference != "" {
			t.Error(difference)
		}
		if line := holdsAny(location, "      proxy_request_buffering", "      error_page 502 504"); line != "" {
			t.Errorf("%s holds %q", name, line)
		}
	}

	// The longest group key nginx reads before a space is 4,093 bytes between its quotes: a name of 4,040 characters,
	// "_http|", a whole IPv6 address in brackets and port 65535 make it, and one character more is refused. YAML takes
	// a key that long written after ?.
	named := func(name, addresses string) string {
		return "listen: 8000\nservices:\n  ? " + name + "\n  : {http: 65535, addresses: [" + addresses + "]}\n" +
			"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: r1, prefix: /r1/, to: " + name + ", retries: 1}\n"
	}
	const whole = `"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fffe:ffff:ffff:ffff:ffff:ffff:ffff:ffff"`
	longest := strings.Repeat("s", 4040)
	if line := `    "` + longest + `_http|[ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff]:65535" ` + longest + "_http_b2;"; !hasLine(generate(t, named(longest, whole)), line) {
		t.Errorf("no group key of %d bytes between its quotes", len(longest)+47)
	}
	refused := `<tmp>/proxyfile.yaml:3:5: "services.<service name>" whose nginx form is longer than nginx reads is refused by ` +
		"--target nginx: nginx reads no value longer than 4094 bytes between its quotes, or 4095 bytes without them, and " +
		"one byte less where a space follows it, as after a map's key; write a value whose nginx form is at most 4093 " +
		"bytes, or render it for envoy"
	for _, file := range []string{named(longest+"s", whole), named(strings.Repeat("s", 4080), `"10.0.0.14", "10.0.0.11"`)} {
		dir := t.TempDir()
		if _, err := generateIn(t, dir, file); err == nil || strings.ReplaceAll(err.Error(), dir, "<tmp>") != refused {
			t.Errorf("Generate's error is %v, want %q", err, refused)
		}
	}
}

// timedFile is a proxyfile whose route r1 is retried 6 times to a service of 8 backends, so it jumps 6 times, with the
// timeouts given, or none for "".
func timedFile(timeouts string) string {
	rest := ""
	if timeouts != "" {
		rest = "timeouts: " + timeouts + "\n"
	}
	return capFile(`addresses: ["10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6", "10.0.0.7", "10.0.0.8"]`, "6") + rest
}

// TestRetryTime: the route's time counts every try, as the Envoy output's route timeout and idle timer count them from
// the complete request (measured on Envoy 1.40.2: 504 at 2, 10 and 12 s with idle, 15 s without, whatever the tries).
// nginx counts its read time for each try, so the next try goes to the copy -rt<k> whose read time is what is left
// once k seconds are spent, k being the tries' own times added up by the script ($upstream_response_time, which holds
// a refused try's 0.000, an unreachable one's 3.06 to 3.10 and one that timed out connecting 5.00 to 5.01, and neither
// the client's upload nor the check's time: measured on nginx 1.30.6 and 1.31.8), to the nearest second, so a request
// whose tries fail, or whose service answers or stays silent, ends within half a second of Envoy's time. There is a
// copy for each second a jump can follow, the route's jumps times the connect time, while time is left; with nothing
// left the request is answered as the timer answers it. Where no timer ends a silent request there is no time to count.
// The route location and each copy send the body they read whole for the same time they read, so a service that takes
// the connection and reads nothing ends the request at the route's time, as through the Envoy output, and not at the
// idle time the http block gives a send; one that reads it slowly, taking a write within that time each time, is sent
// all of it, past the route's time.
func TestRetryTime(t *testing.T) {
	type copied struct {
		name   string
		timers []string // the copy's timer lines
	}
	for _, c := range []struct {
		name, timeouts string
		time, copies   string   // $pf_rt_time's and $pf_rt_copies's lines, "" for none
		names          []string // the copies of @pf_r_1, nil for the 31 of request none
		timers         []copied
		text           string // @pf_rt_spent's text, "" for no such location
	}{
		{"the Envoy output's 15 s", "", `  map "" $pf_rt_time { default 15000; }`, "  map $pf_rt_cap $pf_rt_copies { 6 14; default 0; }",
			[]string{"@pf_r_1-rt", "@pf_r_1-rt1", "@pf_r_1-rt2", "@pf_r_1-rt3", "@pf_r_1-rt4", "@pf_r_1-rt5", "@pf_r_1-rt6", "@pf_r_1-rt7",
				"@pf_r_1-rt8", "@pf_r_1-rt9", "@pf_r_1-rt10", "@pf_r_1-rt11", "@pf_r_1-rt12", "@pf_r_1-rt13", "@pf_r_1-rt14"},
			[]copied{{"@pf_r_1", []string{"      proxy_send_timeout 15s;"}}, {"@pf_r_1-uri", []string{"      proxy_send_timeout 15s;"}},
				{"@pf_r_1-rt", []string{"      proxy_send_timeout 15s;"}},
				{"@pf_r_1-rt1", []string{"      proxy_read_timeout 14s;", "      proxy_send_timeout 14s;"}},
				{"@pf_r_1-rt5", []string{"      proxy_read_timeout 10s;", "      proxy_send_timeout 10s;"}},
				{"@pf_r_1-uri-rt10", []string{"      proxy_read_timeout 5s;", "      proxy_send_timeout 5s;"}},
				{"@pf_r_1-rt11", []string{"      proxy_read_timeout 4s;", "      proxy_send_timeout 4s;", "      proxy_connect_timeout 4s;"}},
				{"@pf_r_1-rt14", []string{"      proxy_read_timeout 1s;", "      proxy_send_timeout 1s;", "      proxy_connect_timeout 1s;"}}},
			"upstream request timeout"},
		{"idle 2s", "{idle: 2s}", `  map "" $pf_rt_time { default 2000; }`, "  map $pf_rt_cap $pf_rt_copies { 6 1; default 0; }",
			[]string{"@pf_r_1-rt", "@pf_r_1-rt1"},
			[]copied{{"@pf_r_1", []string{"      proxy_send_timeout 2s;"}}, {"@pf_r_1-rt", []string{"      proxy_send_timeout 2s;"}},
				{"@pf_r_1-rt1", []string{"      proxy_read_timeout 1s;", "      proxy_send_timeout 1s;", "      proxy_connect_timeout 1s;"}}},
			"stream timeout"},
		{"idle 12s", "{idle: 12s}", `  map "" $pf_rt_time { default 12000; }`, "  map $pf_rt_cap $pf_rt_copies { 6 11; default 0; }",
			[]string{"@pf_r_1-rt", "@pf_r_1-rt1", "@pf_r_1-rt2", "@pf_r_1-rt3", "@pf_r_1-rt4", "@pf_r_1-rt5", "@pf_r_1-rt6", "@pf_r_1-rt7",
				"@pf_r_1-rt8", "@pf_r_1-rt9", "@pf_r_1-rt10", "@pf_r_1-rt11"},
			[]copied{{"@pf_r_1", []string{"      proxy_send_timeout 12s;"}},
				{"@pf_r_1-rt1", []string{"      proxy_read_timeout 11s;", "      proxy_send_timeout 11s;"}},
				{"@pf_r_1-rt7", []string{"      proxy_read_timeout 5s;", "      proxy_send_timeout 5s;"}},
				{"@pf_r_1-rt8", []string{"      proxy_read_timeout 4s;", "      proxy_send_timeout 4s;", "      proxy_connect_timeout 4s;"}},
				{"@pf_r_1-rt11", []string{"      proxy_read_timeout 1s;", "      proxy_send_timeout 1s;", "      proxy_connect_timeout 1s;"}}},
			"stream timeout"},
		// Envoy's 5 minutes: 6 jumps of 5 s at most give 30 copies.
		{"request none", "{request: none}", `  map "" $pf_rt_time { default 300000; }`, "  map $pf_rt_cap $pf_rt_copies { 6 30; default 0; }",
			nil,
			[]copied{{"@pf_r_1", []string{"      proxy_send_timeout 5m;"}},
				{"@pf_r_1-rt1", []string{"      proxy_read_timeout 299s;", "      proxy_send_timeout 299s;"}},
				{"@pf_r_1-rt5", []string{"      proxy_read_timeout 295s;", "      proxy_send_timeout 295s;"}},
				{"@pf_r_1-rt30", []string{"      proxy_read_timeout 270s;", "      proxy_send_timeout 270s;"}}},
			"stream timeout"},
		{"no timer", "{request: none, idle: none}", "", "", []string{"@pf_r_1-rt"},
			[]copied{{"@pf_r_1", []string{"      proxy_send_timeout 9223372036854775807ms;"}}, {"@pf_r_1-rt", []string{"      proxy_send_timeout 9223372036854775807ms;"}}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, timedFile(c.timeouts))
			names := locationNames(out, regexp.MustCompile(`^    location (@pf_r_1-rt[0-9]*) \{$`))
			switch {
			case c.names != nil && !slices.Equal(names, c.names):
				t.Errorf("the copies of @pf_r_1 are %q, want %q", names, c.names)
			case c.names == nil && (len(names) != 31 || names[30] != "@pf_r_1-rt30"):
				t.Errorf("the copies of @pf_r_1 are %q, want 31, the last @pf_r_1-rt30", names)
			}
			for _, copy := range c.timers {
				location := locationOf(out, copy.name)
				at := slices.Index(location, bodyLines[0])
				ends := slices.Concat(copy.timers, bodyLines)
				if at < len(copy.timers)+1 || !slices.Equal(location[at-len(copy.timers):at+len(bodyLines)], ends) ||
					strings.Contains(location[at-len(copy.timers)-1], "_timeout ") {
					t.Errorf("%s is %q, want %q before its proxy_pass", copy.name, location, ends)
				}
			}
			if c.text == "" {
				for _, name := range []string{"$pf_rt_time", "$pf_rt_copies", "$pf_rt_spent", "@pf_rt_spent"} {
					if strings.Contains(out, name) {
						t.Errorf("a file with no time to count names %s", name)
					}
				}
				return
			}
			for _, line := range []string{c.time, c.copies, "  js_set $pf_rt_spent pf.retrySpent nocache;"} {
				if !hasLine(out, line) {
					t.Errorf("no line %q", line)
				}
			}
			if want := []string{"    location @pf_rt_spent {", `      return 504 "` + c.text + `";`, "    }"}; !slices.Equal(locationOf(out, "@pf_rt_spent"), want) {
				t.Errorf("@pf_rt_spent is %q, want %q", locationOf(out, "@pf_rt_spent"), want)
			}
			want := []string{"    location @pf_rt_spent-grpc {", "      default_type application/grpc;", "      add_header x-request-id $pf_answer_id always;",
				"      add_header Vary Origin always;", "      add_header grpc-status 14 always;", `      add_header grpc-message "` + c.text + `" always;`,
				`      return 200 "$pf_empty";`, "    }"}
			if got := locationOf(out, "@pf_rt_spent-grpc"); !slices.Equal(got, want) {
				t.Error(testsupport.FirstDifferenceMessage(strings.Join(got, "\n"), strings.Join(want, "\n")))
			}
		})
	}
}

// TestRetryScript: the script's retrySpent adds up the tries' own times nginx lists, by ", " within an upstream and
// " : " across the copies' groups, and names the copy for the next try by the seconds spent, to the nearest, at most
// the copies written; spent once nothing is left, and spent for a time it cannot read, so such a request is answered,
// never tried again. It reads variables alone, so the script is the same for every file.
func TestRetryScript(t *testing.T) {
	_, script := generateFiles(t, copiesFile)
	want := []string{
		"function retrySpent(r) {",
		"    try {",
		`        var v = r.variables, tries = v.upstream_response_time.split(/, | : /), spent = 0;`,
		"        for (var i = 0; i < tries.length; i++) {",
		`            if (!/^[0-9]+\.[0-9]{3}$/.test(tries[i])) {`,
		`                return "s";`,
		"            }",
		`            spent += Number(tries[i].replace(".", ""));`,
		"        }",
		"        var k = Math.round(spent / 1000);",
		"        if (!(k * 1000 < Number(v.pf_rt_time))) {",
		`            return "s";`,
		"        }",
		"        k = Math.min(k, Number(v.pf_rt_copies));",
		`        return k > 0 ? String(k) : "";`,
		"    } catch (e) {",
		`        return "s";`,
		"    }",
		"}",
	}
	if missing := testsupport.FirstMissingBlock(script, [][]string{want}); missing != "" {
		t.Errorf("the script lacks:\n%s", missing)
	}
	if !strings.Contains(script, scriptExport) {
		t.Errorf("the script does not end %q", scriptExport)
	}
}

// fitsMap is the second guard at the jump: a request whose body nginx read is tried again only when its length is at
// most 1,048,576 bytes. $content_length holds the Content-Length as the client wrote it, leading zeros and all, the
// length nginx read for a body that had none, and nothing for a request with no body, which the key "" takes, as nginx
// tries no regular expression on an empty value.
const fitsMap = `  map $content_length $pf_rt_fits { volatile; "" 1; "~^0*(?:[0-9]{1,6}|10[0-3][0-9]{4}|104[0-7][0-9]{3}|1048[0-4][0-9]{2}|10485[0-6][0-9]|104857[0-6])\\z" 1; default 0; }`

// TestRetryFit: the second guard reads a length as nginx's map reads it.
func TestRetryFit(t *testing.T) {
	out := generate(t, copiesFile)
	if !hasLine(out, fitsMap) {
		t.Fatalf("no line %q", fitsMap)
	}
	fits := mapOf(t, out, "pf_rt_fits")
	for key, want := range map[string]string{"1048576": "1", "0001048576": "1", "1048577": "0", "": "1", "16777216": "0", "0": "1", "999999": "1"} {
		if got := fits(key); got != want {
			t.Errorf("$pf_rt_fits of %q: %q, want %q", key, got, want)
		}
	}
}

// TestRetryTarget: after a failed try the target names, in order, the timer's answer when the route's time is spent,
// the copy for the next try when the try sent nothing, the body fits, a jump is left (the route's jumps against the
// tries $upstream_addr lists, " : " between them) and a group is left, and @pf_fail otherwise. With no timer to count
// there is no spent key, and the copy is -rt.
func TestRetryTarget(t *testing.T) {
	timed := []string{`  map "$pf_rt_unsent$pf_rt_fits|$pf_rt_cap|$upstream_addr|$pf_rt_group|$pf_rt_spent" $pf_rt_target {`, "    volatile;",
		`    "~^1[01]\\|[0-6]\\|[^|]+\\|[^|]*\\|s\\z" rt_spent$pf_grpc_call;`}
	untimed := []string{`  map "$pf_rt_unsent$pf_rt_fits|$pf_rt_cap|$upstream_addr|$pf_rt_group" $pf_rt_target {`, "    volatile;"}
	for jumps := 1; jumps <= 6; jumps++ {
		key := fmt.Sprintf(`    "~^11\\|%d\\|[^ |]+(?: : [^ |]+){0,%d}\\|[^|]" r_$pf_route-rt`, jumps, jumps-1)
		timed = append(timed, key+"$pf_rt_spent;")
		untimed = append(untimed, key+";")
	}
	timed = append(timed, "    default fail;", "  }")
	untimed = append(untimed, "    default fail;", "  }")
	for _, c := range []struct {
		name, file string
		want       []string
	}{
		{"timed", copiesFile, timed},
		{"no timer", timedFile("{request: none, idle: none}"), untimed},
	} {
		if difference := blockDifference(generate(t, c.file), c.want); difference != "" {
			t.Errorf("%s: %s", c.name, difference)
		}
	}
	out := generate(t, copiesFile)
	for _, line := range []string{
		`  map "$upstream_bytes_sent|$upstream_status" $pf_rt_unsent { volatile; "~(?:^| )0\\|(?:.* )?50[24]\\z" 1; default 0; }`,
		`  map $upstream_addr $pf_rt_last { volatile; "~(?:^| : )(?<pf_rt_a>[^ ]+)\\z" $pf_rt_a; default ""; }`,
		`  map $upstream_status $pf_rt_last504 { volatile; "~(?:^| )504\\z" 1; default 0; }`,
	} {
		if !hasLine(out, line) {
			t.Errorf("no line %q", line)
		}
	}
	fail := []string{"    location @pf_fail {", "      error_page 502 = @pf_502$pf_grpc_call;", "      error_page 504 = @pf_504;",
		"      if ($pf_rt_last504) {", "        return 504;", "      }", "      return 502;", "    }"}
	if got := locationOf(out, "@pf_fail"); !slices.Equal(got, fail) {
		t.Errorf("@pf_fail is %q, want %q", got, fail)
	}
}

// TestRetryBody: at the server's jump, a request to a retried route takes the route location, which reads its body
// whole, when it carries no Transfer-Encoding and either its Content-Length is at most 1,048,576 bytes, whatever the
// method and the protocol, or it has neither header over HTTP/1.x, which is a request with no body, whatever the
// method, or it is a GET, HEAD, OPTIONS or DELETE over HTTP/2 with no content-length, whose body then may not pass
// 1 MiB (413); any other request goes to -once, which streams the body and never tries it again. A Transfer-Encoding
// in any letter case takes -once: nginx's buffer bounds the bytes it reads, chunk lines included, so a body under 1 MiB
// in one-byte chunks passed it and was written to a temporary file (measured on nginx 1.30.6 and 1.31.8). nginx cannot
// tell at the jump an HTTP/2 request whose headers end the stream from one whose body follows with no content-length:
// $content_length and every header read alike (measured on nginx 1.30.6 and 1.31.8), so there the method decides. The
// map reads the route's jumps, which only a retried plain route has, after the route; a retried RPC route's key stays
// the POST test.
func TestRetryBody(t *testing.T) {
	const jump = `"~^[^:]*:[0-6]:[^:]+:0*(?:[0-9]{1,6}|10[0-3][0-9]{4}|104[0-7][0-9]{3}|1048[0-4][0-9]{2}|10485[0-6][0-9]|104857[0-6]):[^:]*:\\z" ""; ` +
		`"~^[^:]*:[0-6]:[^:]+::HTTP/1\\.[01]:\\z" ""; "~^[^:]*:[0-6]:(?:GET|HEAD|OPTIONS|DELETE)::HTTP/2\\.0:\\z" ""; "~^[^:]*:[0-6]:" -once; default ""; }`
	const source = `  map "$pf_route:$pf_rt_jumps:$request_method:$content_length:$server_protocol:$http_transfer_encoding" $pf_rt_once { `
	out := generate(t, copiesFile)
	if line := source + jump; !hasLine(out, line) {
		t.Fatalf("no line %q", line)
	}
	if line := source + `"~^(?:7)(?:-uri)?::(?!POST:)" -once; ` + jump; !hasLine(generate(t, groupsFile), line) {
		t.Errorf("a file with a retried RPC route holds no line %q", line)
	}
	once := mapOf(t, out, "pf_rt_once")
	for key, want := range map[string]string{
		"1:1:GET::HTTP/1.1:":             "",
		"1:1:GET::HTTP/2.0:":             "",
		"1:1:PUT:1048576:HTTP/1.1:":      "",
		"1:1:PUT:1048576:HTTP/2.0:":      "",
		"1-uri:1:DELETE::HTTP/2.0:":      "",
		"2:1:HEAD::HTTP/1.1:":            "",
		"3:1:OPTIONS::HTTP/2.0:":         "",
		"1:1:PATCH:0001048576:HTTP/1.1:": "",
		"1:0:PUT:0:HTTP/1.1:":            "",
		"1:1:POST::HTTP/1.1:":            "",
		"1:1:PATCH::HTTP/1.1:":           "",
		"1-uri:1:PROPFIND::HTTP/1.0:":    "",
		"1:1:PUT:1048577:HTTP/1.1:":      "-once",
		"1:1:PUT::HTTP/2.0:":             "-once",
		"3-uri:1:POST::HTTP/2.0:":        "-once",
		"1:6:GET:1048577:HTTP/2.0:":      "-once",
		"1:1:GET::HTTP/1.1:chunked":      "-once",
		"1:1:DELETE::HTTP/1.1:Chunked":   "-once",
		"2:1:HEAD::HTTP/1.1:CHUNKED":     "-once",
		"3:1:OPTIONS::HTTP/1.1:chunked":  "-once",
		"1:1:PUT::HTTP/1.1:chunked":      "-once",
		"1:1:GET:5:HTTP/1.1:chunked":     "-once",
		"1:1:GET::HTTP/2.0:chunked":      "-once",
		"4::PUT::HTTP/1.1:":              "",
		"no-route::POST::HTTP/1.1:":      "",
	} {
		if got := once(key); got != want {
			t.Errorf("$pf_rt_once of %q: %q, want %q", key, got, want)
		}
	}
	streamed := []string{"      proxy_pass http://k2_http;", "      error_page 502 = @pf_502$pf_grpc_call;", "      error_page 504 = @pf_504;", "    }"}
	for _, name := range []string{"@pf_r_1-once", "@pf_r_1-uri-once"} {
		location := locationOf(out, name)
		if difference := endsWith(name, location, streamed); difference != "" {
			t.Error(difference)
		}
		for _, line := range []string{"      js_access pf.check;", "      limit_req zone=pf_rl_1_all burst=9 nodelay;", "      error_page 429 = @pf_429$pf_429_kind;"} {
			if !slices.Contains(location, line) {
				t.Errorf("%s holds no %q", name, line)
			}
		}
		if line := holdsAny(location, "      proxy_request_buffering", "      client_body_buffer_size", "      client_max_body_size", "      error_page 502 504"); line != "" {
			t.Errorf("%s holds %q", name, line)
		}
	}
	if line := holdsAny(locationOf(out, "@pf_r_2-once"), "      rewrite "); line == "" {
		t.Error("@pf_r_2-once holds no rewrite")
	}
	if line := `  map $pf_rt_once $pf_rt_cap { "" $pf_rt_jumps; default ""; }`; !hasLine(out, line) {
		t.Errorf("no line %q", line)
	}
}

// TestRetryHashSizes: the maps of a retried route's jumps hold a key for each route location and each backend, which
// nginx hashes when it reads the map, sized as each map is read, so the map hash is sized before the first map: 4
// slots a key of the largest map, and a bucket that holds four of the longest key, grown to the cache line, as no
// size up to the largest gave a bucket that holds fewer (ngx_hash_init). The service names the balance hashes a header
// by are keys too.
func TestRetryHashSizes(t *testing.T) {
	routes := make([]string, 4096)
	for i := range routes {
		routes[i] = fmt.Sprintf("  - {name: r%04d, prefix: /r%04d/, to: s, retries: 7}\n", i, i)
	}
	many := generate(t, retriedFile(`  s: {http: 8080, addresses: ["10.0.0.100", "10.0.0.101", "10.0.0.102", "10.0.0.103", "10.0.0.104", "10.0.0.105", "10.0.0.106", "10.0.0.107"]}`+"\n",
		strings.Join(routes, ""), ""))
	for _, line := range []string{
		// The jumps map's 8,192 keys, a route location and its -uri twin for each route, 4 slots each.
		"  map_hash_max_size 32768;",
		// The longest key, "s_http|10.0.0.100:8080" of the group map, 22 bytes, takes 32 bytes of a bucket, and four
		// of them with the bucket's 8 take 136, which the cache line of 64 rounds up to 192.
		"  map_hash_bucket_size 192;",
		// 4 slots for each variable the http block declares: the 4,096 route maps and 65 group maps of the route
		// choice, the 87 the rest of the file declares, and the retries' 16, their request log's two maps of flags
		// among them.
		"  variables_hash_max_size 17056;",
	} {
		if !hasLine(many, line) {
			t.Errorf("no line %q", line)
		}
	}
	if at, first := strings.Index(many, "\n  map_hash_max_size "), strings.Index(many, "\n  map "); at < 0 || at > first {
		t.Error("the map hash is not sized before the first map")
	}
	long := strings.Repeat("s", 3000)
	service := "listen: 8000\nservices:\n  ? " + long + "\n  : {http: 8080, addresses: [\"10.0.0.14\", \"10.0.0.11\"]%s}\n" +
		"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: r1, prefix: /r1/, to: " + long + "%s}\n"
	for _, c := range []struct{ name, file, line string }{
		// The group key, the name, "_http|" and "10.0.0.14:8080", is 3,020 bytes, which take 3,032 of a bucket: four
		// with its 8 are 12,136, rounded up to 12,160.
		{"a group key", fmt.Sprintf(service, "", ", retries: 1"), "  map_hash_bucket_size 12160;"},
		// The upstream's name, the name and "_http", is 3,005 bytes, which take 3,016: four with 8 are 12,072, rounded
		// up to 12,096.
		{"a hashed service's name", fmt.Sprintf(service, ", balance: hash, hash: {header: x-k}", ""), "  map_hash_bucket_size 12096;"},
	} {
		if !hasLine(generate(t, c.file), c.line) {
			t.Errorf("%s: no line %q", c.name, c.line)
		}
	}
}

// TestRelayNeverRetries: a retried route to the check's own service gives that service's groups in the http block,
// and the relay that asks the check still tries each check once, with no jump.
func TestRelayNeverRetries(t *testing.T) {
	out := generate(t, retriedFile("  checks: {http: 9000, addresses: [\"10.0.0.31\", \"10.0.0.32\"]}\n",
		"  - {name: c, prefix: /c/, to: checks, retries: 1}\n", "auth: {ask: checks /auth, timeout: 1s, enabled: true, when_unavailable: deny}\n"))
	http := blocks(lines(out), "http {")
	if len(http) != 1 || !slices.Contains(http[0], "  upstream checks_http_b1 {") {
		t.Fatal("the http block holds no group of checks")
	}
	stream := blocks(lines(out), "stream {")
	if len(stream) != 1 {
		t.Fatalf("the file holds %d stream blocks, want 1", len(stream))
	}
	for _, line := range []string{"    proxy_pass proxyfile_8000_check;", "    proxy_next_upstream off;"} {
		if !slices.Contains(stream[0], line) {
			t.Errorf("the relay holds no %q", line)
		}
	}
	group := regexp.MustCompile(`^  upstream \S+_b[0-9]+ \{$`)
	for _, line := range stream[0] {
		if strings.Contains(line, "error_page") || group.MatchString(line) {
			t.Errorf("the stream block holds %q", line)
		}
	}
}
