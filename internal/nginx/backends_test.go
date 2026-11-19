package nginx_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// balanceFile is a proxyfile under client_address whose service catalog has three addresses over HTTP and the keys
// given after them, and whose one route goes to it.
func balanceFile(clientAddress, keys string) string {
	return "listen: 8000\nclient_address: " + clientAddress + "\nservices:\n" +
		`  catalog: {http: 8090, addresses: ["10.0.2.21", "10.0.2.22", "10.0.2.23"]` + keys + "}\n" +
		"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: catalog, prefix: /, to: catalog}\n"
}

// upstreamBlocks returns each block of out that opens with the line open, a whole line.
func upstreamBlocks(out, open string) [][]string {
	var found [][]string
	for _, block := range blocks(lines(out), open) {
		if block[0] == open {
			found = append(found, block)
		}
	}
	return found
}

// blockDifference says how the one block of out that opens with want's first line differs from want, or "" when it
// equals want.
func blockDifference(out string, want []string) string {
	found := upstreamBlocks(out, want[0])
	if len(found) != 1 {
		return fmt.Sprintf("the file holds %d blocks opening with %q, want 1", len(found), want[0])
	}
	if !slices.Equal(found[0], want) {
		return testsupport.FirstDifferenceMessage(strings.Join(found[0], "\n"), strings.Join(want, "\n"))
	}
	return ""
}

// TestBalanceLines: each balance is the line nginx balances the upstream with, the block's first, and round_robin,
// nginx's own, is none. hash.header names the header the script's hashKey reads by the upstream the request is sent to,
// and hash.client hashes the client's address as client_address reads it.
func TestBalanceLines(t *testing.T) {
	block := func(balance ...string) []string {
		return slices.Concat([]string{"  upstream catalog_http {"}, balance, []string{
			"    server 10.0.2.21:8090 max_fails=0;",
			"    server 10.0.2.22:8090 max_fails=0;",
			"    server 10.0.2.23:8090 max_fails=0;",
			"    keepalive 32;",
			"    keepalive_timeout 1h;",
			"  }",
		})
	}
	const names, key = `  map $pf_log_upstream $pf_hash_name { catalog_http x-session-id; default ""; }`, "  js_set $pf_hash_key pf.hashKey;"
	for _, c := range []struct {
		name, clientAddress, keys string
		want                      []string
		hashed                    []string // the http block's lines of hash.header, or nil for a file that writes none
	}{
		{"round_robin", "connection", ", balance: round_robin", block(), nil},
		{"least_requests", "connection", ", balance: least_requests", block("    random two least_conn;"), nil},
		{"random", "connection", ", balance: random", block("    random;"), nil},
		{"hash.header", "connection", ", balance: hash, hash: {header: x-session-id}", block("    hash $pf_hash_key consistent;"),
			[]string{names, key}},
		{"hash.client under connection", "connection", ", balance: hash, hash: {client: true}",
			block("    hash $binary_remote_addr consistent;"), nil},
		{"hash.client under forwarded", "forwarded", ", balance: hash, hash: {client: true}",
			block("    hash $pf_client_key consistent;"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, balanceFile(c.clientAddress, c.keys))
			if difference := blockDifference(out, c.want); difference != "" {
				t.Error(difference)
			}
			http := blocks(lines(out), "http {")[0]
			var hashed []string
			for _, line := range http {
				// The block's own lines, not an upstream's.
				if strings.HasPrefix(line, "  ") && line[2] != ' ' && strings.Contains(line, "$pf_hash_") {
					hashed = append(hashed, line)
				}
			}
			if !slices.Equal(hashed, c.hashed) {
				t.Errorf("the http block's lines of hash.header are %q, want %q", hashed, c.hashed)
			}
		})
	}

	// A service with both ports hashes on each: the gRPC upstream is a key of its own.
	both := "listen: 8000\nservices:\n  shop: {http: 8080, grpc: 8081, balance: hash, hash: {header: x-cart}}\n" +
		"  app: {http: 3000, balance: hash, hash: {header: x-session-id}}\norigins:\n  local: {hosts: [localhost]}\n" +
		"routes:\n  - {name: app, prefix: /app/, to: app}\n  - {name: shop, prefix: /, to: shop}\n"
	want := `  map $pf_log_upstream $pf_hash_name { shop_http x-cart; shop_grpc x-cart; app_http x-session-id; default ""; }`
	if out := generate(t, both); !hasLine(out, want) {
		t.Errorf("no line %q", want)
	}
}

// TestHealthLines: health is nginx's own passive check, max_fails=<unhealthy> on each server of the service's
// upstreams, 3 when unhealthy is not written, with the upstream in a zone, so the workers share one count of failures;
// a DNS name's upstream, which has its zone already, writes one. A service with one address, and each group of a
// retried route, keeps max_fails=0 and no zone, as nginx ignores max_fails in a group of one server. The check relay's
// upstream in the stream block is the service's, so it counts the check's failed connections the same way.
func TestHealthLines(t *testing.T) {
	servers := func(fails string) []string {
		return []string{
			"    server 10.0.2.21:8090 max_fails=" + fails + ";",
			"    server 10.0.2.22:8090 max_fails=" + fails + ";",
			"    server 10.0.2.23:8090 max_fails=" + fails + ";",
		}
	}
	kept := []string{"    keepalive 32;", "    keepalive_timeout 1h;", "  }"}
	const opening, zone = "  upstream catalog_http {", "    zone catalog_http 1m;"
	byName := "listen: 8000\nservices:\n  catalog: {http: 8090, address: catalog.internal.example, health: {path: /healthz}}\n" +
		"dns: {resolver: \"10.0.0.2:53\"}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: catalog, prefix: /, to: catalog}\n"
	retried := strings.Replace(balanceFile("connection", ", health: {path: /healthz}"), "to: catalog}", "to: catalog, retries: 1}", 1)
	const accounts = `  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070"}`
	checked := rewritten(t, string(testsupport.ReadFile(t, authFile)), accounts,
		`  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070", addresses: ["10.0.3.11", "10.0.3.12"], `+
			`health: {path: /healthz, unhealthy: 2}}`)
	single := "listen: 8000\nservices:\n  catalog: {http: 8090, address: \"10.0.2.21\", health: {path: /healthz}}\n" +
		"origins:\n  local: {hosts: [localhost]}\nroutes:\n  - {name: catalog, prefix: /, to: catalog}\n"
	for _, c := range []struct {
		name, file string
		within     string     // the block the blocks are read in, "" for the whole file
		want       [][]string // each a whole block of the output
	}{
		{"unhealthy 1", balanceFile("connection", ", health: {path: /healthz, unhealthy: 1}"), "",
			[][]string{slices.Concat([]string{opening, zone}, servers("1"), kept)}},
		{"unhealthy not written", balanceFile("connection", ", health: {path: /healthz}"), "",
			[][]string{slices.Concat([]string{opening, zone}, servers("3"), kept)}},
		{"least_requests", balanceFile("connection", ", balance: least_requests, health: {path: /healthz}"), "",
			[][]string{slices.Concat([]string{opening, "    random two least_conn;", zone}, servers("3"), kept)}},
		{"a DNS name with dns.resolver", byName, "",
			[][]string{slices.Concat([]string{opening, zone, "    server catalog.internal.example:8090 resolve max_fails=3;"}, kept)}},
		{"one address", single, "", [][]string{slices.Concat([]string{opening, "    server 10.0.2.21:8090 max_fails=0;"}, kept)}},
		{"a retried route's groups", retried, "", [][]string{
			slices.Concat([]string{opening, zone}, servers("3"), kept),
			slices.Concat([]string{"  upstream catalog_http_b1 {", "    server 10.0.2.21:8090 max_fails=0;"}, kept),
		}},
		{"the check's service", checked, "stream {", [][]string{{"  upstream proxyfile_10000_check {",
			"    zone proxyfile_10000_check 1m;", "    server 10.0.3.11:7070 max_fails=2;", "    server 10.0.3.12:7070 max_fails=2;", "  }"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.file)
			if c.within != "" {
				within := blocks(lines(out), c.within)
				if len(within) != 1 {
					t.Fatalf("the file holds %d blocks opening with %q, want 1", len(within), c.within)
				}
				out = strings.Join(within[0], "\n")
			}
			for _, want := range c.want {
				if difference := blockDifference(out, want); difference != "" {
					t.Error(difference)
				}
			}
		})
	}
}

// TestDNSUpstream: a service whose address is a DNS name is an upstream in shared memory, whose server nginx looks up
// again while it runs through the resolver dns.resolver names, written once in the http block and once in the stream
// block of the check's relay, which each read their own; an IPv6 resolver is written in brackets.
func TestDNSUpstream(t *testing.T) {
	battery := string(testsupport.ReadFile(t, authFile))
	named := rewritten(t, battery, `  catalog: {http: 8090, address: "10.0.2.21"}`, "  catalog: {http: 8090, address: catalog.internal.example}")
	want := []string{
		"  upstream catalog_http {",
		"    zone catalog_http 1m;",
		"    server catalog.internal.example:8090 resolve max_fails=0;",
		"    keepalive 32;",
		"    keepalive_timeout 3s;",
		"  }",
	}
	for _, c := range []struct {
		name, file, resolver string // resolver is the line each block holds once, or "" for none
	}{
		{"an IPv4 resolver", named + "dns: {resolver: \"10.0.0.2:53\"}\n", "  resolver 10.0.0.2:53 valid=5s;"},
		{"an IPv6 resolver with a port", named + "dns: {resolver: \"[fd00::53]:5353\"}\n", "  resolver [fd00::53]:5353 valid=5s;"},
		{"no dns", battery, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, c.file)
			if c.resolver != "" {
				if difference := blockDifference(out, want); difference != "" {
					t.Error(difference)
				}
			}
			for _, open := range []string{"http {", "stream {"} {
				var resolvers []string
				for _, line := range blocks(lines(out), open)[0] {
					if strings.HasPrefix(strings.TrimLeft(line, " "), "resolver ") {
						resolvers = append(resolvers, line)
					}
				}
				var wanted []string
				if c.resolver != "" {
					wanted = []string{c.resolver}
				}
				if !slices.Equal(resolvers, wanted) {
					t.Errorf("the block %s holds the resolver lines %q, want %q", open, resolvers, wanted)
				}
			}
		})
	}
}

// TestCheckServiceByName: the service auth.ask names, by a DNS name, is the relay's upstream in shared memory, looked up
// again while nginx runs as the service's own upstreams are, and balanced as they are; the relay still tries each check
// once, at one address, so no check is asked twice.
func TestCheckServiceByName(t *testing.T) {
	const accounts = `  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070"}`
	byName := func(keys string) string {
		return rewritten(t, string(testsupport.ReadFile(t, authFile)), accounts,
			`  accounts: {grpc: 7070, http: 7070, host_header: "127.0.0.1:7070", address: check.internal.example`+keys+"}") +
			"dns: {resolver: \"10.0.0.2:53\"}\n"
	}
	upstream := func(balance ...string) []string {
		return slices.Concat([]string{"  upstream proxyfile_10000_check {"}, balance, []string{
			"    zone proxyfile_10000_check 1m;",
			"    server check.internal.example:7070 resolve max_fails=0;",
			"  }",
		})
	}
	for _, c := range []struct {
		name, keys string
		want       []string
	}{
		{"round_robin", "", upstream()},
		{"least_requests", ", balance: least_requests", upstream("    random two least_conn;")},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := generate(t, byName(c.keys))
			stream := blocks(lines(out), "stream {")
			if len(stream) != 1 {
				t.Fatalf("the file holds %d stream blocks, want 1", len(stream))
			}
			if difference := blockDifference(strings.Join(stream[0], "\n"), c.want); difference != "" {
				t.Error(difference)
			}
			if !slices.Contains(stream[0], "    proxy_next_upstream off;") {
				t.Error("the relay's server does not hold \"    proxy_next_upstream off;\"")
			}
		})
	}
}
