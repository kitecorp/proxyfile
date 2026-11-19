package nginx_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// nameServer is a dns.resolver key, which a test adds to a file whose collector it names.
const nameServer = "dns: {resolver: \"10.0.0.2:53\"}\n"

// collectorFile is smallFile with the lines of rest, then tracing to the collector at endpoint.
func collectorFile(endpoint string, rest ...string) string {
	return smallFile("{name: app, prefix: /, to: app}") + strings.Join(rest, "") +
		"tracing:\n  provider:\n    otel: {endpoint: \"" + endpoint + "\", service: gw}\n  path_in_url: true\n"
}

// relayUpstream and relayServer are the http block's relay to the collector otel-collector:4317 of a file that listens
// on port listen.
func relayUpstream(listen string) []string {
	return []string{
		"  upstream proxyfile_" + listen + "_collector {",
		"    zone proxyfile_" + listen + "_collector 1m;",
		"    server otel-collector:4317 resolve max_fails=0;",
		"  }",
	}
}

func relayServer(listen string) []string {
	return []string{
		"  server {",
		"    listen unix:/run/proxyfile-" + listen + "/collector.sock;",
		"    http2 on;",
		"    otel_trace off;",
		"    access_log off;",
		"    client_max_body_size 0;",
		"    max_headers 100;",
		"    client_header_buffer_size 64;",
		"    large_client_header_buffers 1 61376;",
		"    client_body_timeout 10s;",
		"    send_timeout 10s;",
		"    location / {",
		"      grpc_pass grpc://proxyfile_" + listen + "_collector;",
		"      grpc_connect_timeout 5s;",
		"      grpc_read_timeout 10s;",
		"      grpc_send_timeout 10s;",
		"    }",
		"  }",
	}
}

// relayServerOf returns the server block of out that listens on the collector relay's socket for port listen, or nil.
func relayServerOf(out, listen string) []string {
	for _, server := range blocks(lines(out), "  server {") {
		if len(server) > 1 && server[1] == "    listen unix:/run/proxyfile-"+listen+"/collector.sock;" {
			return server
		}
	}
	return nil
}

// TestCollectorRelay: with dns.resolver a collector by DNS name is reached through a relay in the http block, which
// looks the name up through the name server as a service's upstream does, and the tracer sends its spans to the relay's
// socket in the relay's folder, the check's folder when the check is asked, which the file names once as a temporary
// path so nginx makes it at start; the file writes the resolver line once in the http block (measured on nginx 1.30.6
// and 1.31.8 beside Envoy 1.40.2: through the relay the spans reached a collector whose name moved, and the tracer's own
// lookup reached none).
func TestCollectorRelay(t *testing.T) {
	auth := string(testsupport.ReadFile(t, authFile))
	byName := rewritten(t, tracingBlock, "10.0.3.40:4317", "otel-collector:4317")
	rows := []struct {
		name, proxyfile, listen, folder string
	}{
		{"a file with no check", collectorFile("otel-collector:4317", nameServer), "8000",
			"  # The folder of the collector relay's socket: nginx makes a temporary path at start, before it binds, for its workers' user alone."},
		{"a file that asks the check", auth + nameServer + byName, "10000",
			"  # The folder of the auth check's and the collector relay's sockets: nginx makes a temporary path at start, before it binds, for its workers' user alone."},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			out := generate(t, row.proxyfile)
			http := httpLines(out)
			wanted := [][]string{
				{"  otel_exporter { endpoint unix:/run/proxyfile-" + row.listen + "/collector.sock; }"},
				{row.folder, "  scgi_temp_path /run/proxyfile-" + row.listen + ";"},
				{"  resolver 10.0.0.2:53 valid=5s;"},
				slices.Concat(relayUpstream(row.listen), relayServer(row.listen)),
			}
			for _, block := range wanted {
				if missed := testsupport.FirstMissingBlock(strings.Join(http, "\n"), [][]string{block}); missed != "" {
					t.Errorf("the http block holds no block:\n%s", missed)
				}
			}
			for _, line := range []string{"  resolver 10.0.0.2:53 valid=5s;", "  scgi_temp_path /run/proxyfile-" + row.listen + ";"} {
				if n := countOf(http, line); n != 1 {
					t.Errorf("the http block holds %q %d times, want once", line, n)
				}
			}
		})
	}
}

// TestCollectorByAddress: a collector by IP address is asked directly, with or without dns.resolver, so its file writes
// no relay and no folder for one: the output of such a file is the one written before the relay was.
func TestCollectorByAddress(t *testing.T) {
	rows := []struct {
		name, proxyfile, endpoint string
	}{
		{"an IPv4 address", collectorFile("10.0.3.40:4317"), "10.0.3.40:4317"},
		{"an IPv4 address beside dns.resolver", collectorFile("10.0.3.40:4317", nameServer), "10.0.3.40:4317"},
		{"an IPv6 address beside dns.resolver", collectorFile("[fd00::1]:4317", nameServer), "[fd00::1]:4317"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			out := generate(t, row.proxyfile)
			if broken := missing(out, "  otel_exporter { endpoint "+row.endpoint+"; }"); broken != "" {
				t.Error(broken)
			}
			for _, text := range []string{"collector", "unix:", "grpc_pass grpc://", "scgi_temp_path"} {
				if strings.Contains(out, text) {
					t.Errorf("the file holds %q", text)
				}
			}
		})
	}
}

// TestCollectorNames: the relay's upstream, its zone and its socket take names no service and no check relay can take,
// as a service's name holds no _ and its upstreams are named <service>_<protocol>, with _b<n> for each backend of a
// retried route, and the check relay's socket is check.sock. Two services named as close to the relay's names as a
// service's name may be keep their own: one with two backends a route retries, and one by DNS name, which the check
// asks, so each upstream and each zone a file writes is there.
func TestCollectorNames(t *testing.T) {
	out := generate(t, "listen: 8000\nservices:\n  proxyfile: {http: 3000, grpc: 3001, addresses: [10.0.1.1, 10.0.1.2]}\n"+
		"  proxyfile-8000-collector: {http: 3000, grpc: 3001, address: app.internal}\n"+
		"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: other, prefix: /other/, to: proxyfile-8000-collector}\n"+
		"  - {name: app, prefix: /, to: proxyfile, retries: 1}\n"+
		"auth: {ask: proxyfile-8000-collector /check, timeout: 1s, enabled: true, when_unavailable: deny}\n"+nameServer+
		"tracing:\n  provider:\n    otel: {endpoint: \"otel-collector:4317\", service: gw}\n  path_in_url: true\n")
	var upstreams, zones, sockets []string
	for _, line := range lines(out) {
		word := strings.Fields(line)
		switch {
		case len(word) == 3 && word[0] == "upstream" && word[2] == "{":
			upstreams = append(upstreams, word[1])
		case len(word) == 3 && word[0] == "zone":
			zones = append(zones, word[1])
		case len(word) >= 2 && word[0] == "listen" && strings.HasPrefix(word[1], "unix:"):
			sockets = append(sockets, strings.TrimSuffix(word[1], ";"))
		}
	}
	want := []struct {
		kind        string
		found, want []string
	}{
		{"upstreams", upstreams, []string{"proxyfile_http", "proxyfile_http_b1", "proxyfile_http_b2", "proxyfile_grpc",
			"proxyfile-8000-collector_http", "proxyfile-8000-collector_grpc", "proxyfile_8000_collector", "proxyfile_8000_check"}},
		{"zones", zones, []string{"proxyfile-8000-collector_http", "proxyfile-8000-collector_grpc", "proxyfile_8000_collector", "proxyfile_8000_check"}},
		{"sockets", sockets, []string{"unix:/run/proxyfile-8000/collector.sock", "unix:/run/proxyfile-8000/check.sock"}},
	}
	for _, names := range want {
		if !slices.Equal(names.found, names.want) {
			t.Errorf("the file names the %s %q, want %q", names.kind, names.found, names.want)
		}
	}
}

// TestCollectorRelayServer: the relay's server takes nothing from what the file says of its clients' requests, so the
// tracer's calls are not traced, not rate limited, held to no limit of the file, never checked and never logged: its
// block is the same in a file that writes each of those keys as in the plain one.
func TestCollectorRelayServer(t *testing.T) {
	limited := "{name: app, prefix: /, to: app, rate_limit: {requests: 10, per: 1s, by: all}}"
	rows := []struct {
		name, proxyfile string
	}{
		{"no other key", collectorFile("otel-collector:4317", nameServer)},
		{"a rate limit on the route", strings.Replace(collectorFile("otel-collector:4317", nameServer), "{name: app, prefix: /, to: app}", limited, 1)},
		{"limits of one header line in 1 KiB", collectorFile("otel-collector:4317", nameServer, "limits:\n  request_headers: {size: 1KiB, count: 1}\n")},
		{"timeouts of 1 ms", collectorFile("otel-collector:4317", nameServer, "timeouts:\n  idle: 1ms\n  request: 1ms\n")},
		{"a log of every request", collectorFile("otel-collector:4317", nameServer, "log: {status_from: 100, sample: 100%}\n")},
		{"the check asked", collectorFile("otel-collector:4317", nameServer, "auth: {ask: app /check, timeout: 1s, enabled: true, when_unavailable: deny}\n")},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got, want := relayServerOf(generate(t, row.proxyfile), "8000"), relayServer("8000"); !slices.Equal(got, want) {
				t.Errorf("the relay's server is\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
