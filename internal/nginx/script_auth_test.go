package nginx_test

import (
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The auth check is in the script, which is the same bytes for every file: the lists it reads are written from the
// generator's constants alone. The rows hold what no battery row can: a scenario cannot make the script throw, and
// never asserts the body of an answer the proxy makes itself. The rest of the check is held by the battery on nginx
// 1.30.6 and 1.31.8 beside Envoy 1.40.2.
func TestScriptChecks(t *testing.T) {
	_, minimal := generateFiles(t, string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml")))
	if _, battery := generateFiles(t, string(testsupport.ReadFile(t, batteryFile))); battery != minimal {
		t.Errorf("the battery file's script differs from the minimal example's: %s",
			testsupport.FirstDifferenceMessage(battery, minimal))
	}
	unavailable := []string{
		"function unavailable(r) {",
		"    refuse(r, 403, [], Buffer.alloc(0));",
		"}",
	}
	cases := []struct {
		name   string
		blocks [][]string
	}{
		// An exception the handler does not catch leaves the request to nginx, which answered 500 at the deadline
		// (measured on nginx 1.30.6 and 1.31.8), and a deadline timer still waiting held the connection until it fired,
		// so every path clears it.
		{"the handler's body is one try whose catch answers as an unanswered check under deny, and whose finally clears the deadline timer", [][]string{
			unavailable,
			{
				"async function check(r) {",
				"    try {",
				"        var timer = null;",
			},
			{
				`        if (verdict == "unanswered" && r.variables.pf_when_unavailable == "allow") {`,
				`            r.variables.pf_unchecked = "true";`,
				"            pass(r);",
				"            return;",
				"        }",
				`        if (verdict != "answered") {`,
				"            unavailable(r);",
				"            return;",
				"        }",
			},
			{
				"    } catch (e) {",
				"        unavailable(r);",
				"    } finally {",
				"        if (timer !== null) {",
				"            clearTimeout(timer);",
				"        }",
				"    }",
				"}",
			},
		}},
		// Envoy sends the check the ask path as written, a character outside ASCII as its UTF-8 bytes (measured on Envoy
		// 1.40.2); the request line is read with the bytes of 0x80 or more in the path and none in the method.
		{"the relay's question takes a byte of 0x80 or more in the ask path", [][]string{
			{
				"        if (b[i] <= 0x20 || b[i] == 0x7F || (b[i] > 0x7F && !high)) {",
			},
			{
				"    if (method === null || path === null || host === null || !plain(method) || !plain(path, true)) {",
			},
		}},
		// A relay that closes before it writes a byte reads as an unanswered check, which allow passes with the mark
		// (measured: an exception in either handler of the relay served the request with the mark), so each handler
		// is one try whose catch writes unreadable, which both modes refuse; decide marks the answer written only once
		// it is sent, so a catch after a failed send still writes one.
		{"each handler of the relay is one try whose catch writes the unreadable answer", [][]string{
			{
				"    function decide(verdict, framed) {",
				"        if (decided) {",
				"            return;",
				"        }",
				"        s.sendDownstream(answer(verdict, framed), {last: true});",
				"        decided = true;",
				"    }",
			},
			{
				`    s.on("upstream", function (data, flags) {`,
				"        try {",
			},
			{
				"        } catch (e) {",
				`            decide("unreadable");`,
				"        }",
				"    });",
			},
			{
				`    s.on("downstream", function (data, flags) {`,
				"        try {",
			},
			{
				"        } catch (e) {",
				`            decide("unreadable");`,
				"        }",
				"    });",
			},
		}},
		// A connection to the relay that nginx never made, such as one its full listen queue refused, holds no connect
		// time, "-" (measured on nginx 1.30.6 and 1.31.8), and is a failure of nginx's own parts, which both modes
		// refuse; a relay that was reached and wrote nothing, as for a check that refused its connection or never
		// answered, holds the time it took, and is an unanswered check.
		{"a connection to the relay that was never made is unreadable", [][]string{
			{
				"    if (reply.status != 200) {",
				`        return /^s=50[24] b=0 c=[0-9]+\.[0-9]+ $/.test(r.variables.pf_check_failed || "") ? "unanswered" : "unreadable";`,
				"    }",
			},
		}},
		// The Envoy output answers an unanswered check under deny, and its own 403 for a check's 403, with the body
		// Invalid CORS request and content-type text/plain when the request carries an Origin, and a gRPC call with
		// the same text as grpc-message (measured on Envoy 1.40.2).
		{"the refusal of an unanswered or unreadable check writes the body Invalid CORS request when the request carries an Origin", [][]string{
			{"function refuse(r, status, headers, body) {"},
			{
				`    var cors = status == 403 && r.variables.pf_has_origin == "1";`,
				"    if (cors) {",
				`        body = Buffer.from("Invalid CORS request");`,
				"    }",
			},
			{
				"    if (grpc) {",
				`        r.headersOut["content-type"] = "application/grpc";`,
				`        r.headersOut["grpc-status"] = String(grpcCode(answered));`,
				"        if (body.length) {",
				`            r.headersOut["grpc-message"] = grpcMessage(body);`,
				"        }",
				"        r.return(200);",
				"        return;",
				"    }",
				"    if (cors || (body.length && !typed)) {",
				`        r.headersOut["content-type"] = "text/plain";`,
				"    }",
			},
			unavailable,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := testsupport.FirstMissingBlock(minimal, c.blocks); block != "" {
				t.Errorf("the script does not hold, in its place:\n%s", block)
			}
		})
	}
}
