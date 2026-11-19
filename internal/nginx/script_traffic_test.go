package nginx_test

import (
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The script's functions for the traffic keys (the client's address a rate limit or hash.client counts by, the value
// hash.header hashes, the form of a rate limit's 429) and the two corrections of the auth check's header count. The
// script is the same bytes for every file, so the rows read the minimal example's. They hold the text the battery
// cannot: what a function answers when it throws. What each function answers is held by the battery on nginx 1.30.6
// and 1.31.8 beside Envoy 1.40.2.
func TestScriptTraffic(t *testing.T) {
	_, script := generateFiles(t, string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml")))
	cases := []struct {
		name   string
		blocks [][]string
		absent []string // text the script holds nowhere
	}{
		// Envoy 1.40.2 answers a gRPC call the check's accepted headers take past limits.request_headers 200 with
		// grpc-status 2, where a plain 500 from nginx reached the client, and it counts the request only when the
		// check's answer held a header auth.accept covers other than keep-alive, which $pf_accept leaves out
		// (measured on Envoy 1.40.2).
		{name: "the check's overflow is answered by the refusal, so a gRPC call gets grpc-status 2, and the count runs only when the check set an accepted header",
			blocks: [][]string{{
				"        var set = accept(r, a.headers);",
				"        if (set.fields > 0 && !passable(r, set)) {",
				"            refuse(r, 500, [], Buffer.alloc(0));",
				"            return;",
				"        }",
				"        pass(r);",
			}},
			absent: []string{`r.return(500, "");`}},
		// Envoy takes a chunked request's Transfer-Encoding line off before it counts the request it passes on (a
		// chunked request with 89 fields was served, 24 bytes more than without, measured on Envoy 1.40.2), and still
		// counts the line against the header count of the request it reads, which fieldCount gives headerNames.
		{name: "a request's Transfer-Encoding line is not counted against the header limits the check's headers meet",
			blocks: [][]string{
				{
					"function fieldCount(r) {",
					"    var raw = r.rawHeadersIn, fields = raw.length;",
				},
				{
					"function passable(r, set) {",
					"    var raw = r.rawHeadersIn, scheme = r.variables.scheme.length, size = set.size, host = -1;",
					"    var fields = fieldCount(r) + 3 + 2 + set.fields;",
					"    for (var i = 0; i < raw.length; i++) {",
					"        var name = raw[i][0].toLowerCase();",
					`        if (name == "transfer-encoding") {`,
					"            fields--;",
					"            continue;",
					"        }",
					"        size += Buffer.byteLength(raw[i][0]) + Buffer.byteLength(raw[i][1]);",
					`        if (name == "host") {`,
					"            host = Buffer.byteLength(raw[i][1]);",
					"        }",
					"    }",
				},
			}},
		// Envoy answers gRPC's Unknown for any status its table does not list (generator.GRPCUnknown).
		{name: "grpcCode falls back to grpcUnknown",
			blocks: [][]string{
				{"var grpcUnknown = 2;"},
				{
					"function grpcCode(status) {",
					"    var code = grpcStatus[status];",
					"    return code === undefined ? grpcUnknown : code;",
					"}",
				},
			}},
		{name: "clientKey, hashKey, limitedKind and retrySpent are exported",
			blocks: [][]string{{scriptExport}}},
		// What each of clientKey, hashKey and limitedKind answers is held by the battery's rows (client keys, hash, rate
		// limit); what each answers when it throws is here. nginx counts no request whose zone key is empty, so
		// clientKey's catch answers the peer's key.
		{name: "clientKey answers the peer's key when it throws",
			blocks: [][]string{{
				"    } catch (e) {",
				"        return canonical(r.variables.remote_addr);",
				"    }",
				"}",
			}}},
		// The key of a request whose hashed header nginx could not read is the key of one with no such header.
		{name: "hashKey answers the key of an absent header when it throws",
			blocks: [][]string{{
				`        return values.length ? "v" + values.join("\n") : "";`,
				"    } catch (e) {",
				`        return "";`,
				"    }",
				"}",
			}}},
		// An empty kind keeps the connection, so the catch answers the kind that closes it.
		{name: "limitedKind answers the kind that closes the connection when it throws",
			blocks: [][]string{{
				`        return "";`,
				"    } catch (e) {",
				`        return "-body";`,
				"    }",
				"}",
			}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := testsupport.FirstMissingBlock(script, c.blocks); block != "" {
				t.Errorf("the script does not hold, in its place:\n%s", block)
			}
			for _, text := range c.absent {
				if got := strings.Count(script, text); got != 0 {
					t.Errorf("the script holds %q %d times, want 0", text, got)
				}
			}
		})
	}
}
