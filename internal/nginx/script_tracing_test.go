package nginx_test

import (
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// tracingBlock is a tracing key with the OpenTelemetry provider, a tag of an environment variable and two of headers,
// one with a default, and path_in_url, which a test appends to a battery file to read the output of a file that
// writes tracing.
const tracingBlock = "tracing:\n" +
	"  provider:\n" +
	"    otel: {endpoint: \"10.0.3.40:4317\", service: \"edge gw\", sample: {rate: 5%}}\n" +
	"  tags:\n" +
	"    environment: {env: APP_ENVIRONMENT, default: local}\n" +
	"    request_id: {header: x-request-id}\n" +
	"    tenant: {header: x-tenant, default: anon}\n" +
	"  path_in_url: true\n"

// scriptExport is the script's last line, which exports every function a configuration may call: the tests that read
// the exports hold this one line, so a function the script gains changes it here alone.
const scriptExport = "export default {headerNames, forwardedProto, origin, relay, check, clientKey, hashKey, limitedKind, " +
	"retrySpent, traced, traceparent, tracestate, checkTraceparent, checkTracestate, traceLines, requestId, readTags};"

// TestScriptTracing: the functions a file with tracing calls to draw a span as the Envoy output draws it and to read
// each tag's value in one pass. The script holds no value of either file, so a file with tracing gets the script of
// one without, and a file without tracing never calls them. Each function is one try whose catch answers the stricter
// value, no span and no tag, which no request can make run, so the rows hold the text; what each function answers is
// measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2.
func TestScriptTracing(t *testing.T) {
	file := string(testsupport.ReadFile(t, authFile))
	_, without := generateFiles(t, file)
	_, with := generateFiles(t, file+tracingBlock)
	t.Run("a file with tracing and one without get the same script", func(t *testing.T) {
		if with != without {
			t.Errorf("the script of the file with tracing differs from the one without: %s",
				testsupport.FirstDifferenceMessage(with, without))
		}
	})
	t.Run("the script holds the tracing functions before the export line", func(t *testing.T) {
		if block := testsupport.FirstMissingBlock(with, tracingFunctions); block != "" {
			t.Errorf("the script holds no block, after the ones before it:\n%s", block)
		}
		all := lines(with)
		if last := all[len(all)-1]; last != scriptExport {
			t.Errorf("the script's last line is %q, want %q", last, scriptExport)
		}
	})
}

// tracingFunctions are the functions tracing adds to the script, and the byte helpers it reads a header's bytes with,
// each with its comment, in their order.
var tracingFunctions = [][]string{
	lines(`// sequence is, for the bytes of b from i on, the length of the well-formed UTF-8 sequence of 2 to 4 bytes that starts
// there, as RFC 3629 writes one (no overlong form, no surrogate, nothing above U+10FFFF), or, where none starts, minus
// the number of bytes a decoder reads as one U+FFFD: the byte and each byte after it that such a sequence could still
// hold, the maximal subpart of the Unicode Standard (section 3.9), which njs replaces with one U+FFFD (measured on
// nginx 1.30.6 and 1.31.8).
function sequence(b, i) {
    var c = b[i], n = 0, low = 0x80, high = 0xBF;
    if (c >= 0xC2 && c <= 0xDF) {
        n = 2;
    } else if (c >= 0xE0 && c <= 0xEF) {
        n = 3;
        low = c == 0xE0 ? 0xA0 : 0x80;
        high = c == 0xED ? 0x9F : 0xBF;
    } else if (c >= 0xF0 && c <= 0xF4) {
        n = 4;
        low = c == 0xF0 ? 0x90 : 0x80;
        high = c == 0xF4 ? 0x8F : 0xBF;
    }
    if (n == 0 || i + 1 >= b.length || b[i + 1] < low || b[i + 1] > high) {
        return -1;
    }
    for (var k = 2; k < n; k++) {
        if (i + k >= b.length || b[i + k] < 0x80 || b[i + k] > 0xBF) {
            return -k;
        }
    }
    return n;
}`),
	lines(`// utf8 is the length of the well-formed UTF-8 sequence of 2 to 4 bytes that starts at b[i], or 0 when none starts
// there (sequence).
function utf8(b, i) {
    var n = sequence(b, i);
    return n > 0 ? n : 0;
}`),
	lines(`// firstLine is the value of name's first line, or null: Envoy reads the first of two where $http_<name> joins them.
function firstLine(r, name) {
    var raw = r.rawHeadersIn;
    for (var i = 0; i < raw.length; i++) {
        if (raw[i][0].toLowerCase() == name) {
            return raw[i][1];
        }
    }
    return null;
}`),
	lines(`// allLines is the value of each of name's lines in the order sent, each apart, where $http_<name> joins them.
function allLines(r, name) {
    var raw = r.rawHeadersIn, values = [];
    for (var i = 0; i < raw.length; i++) {
        if (raw[i][0].toLowerCase() == name) {
            values.push(raw[i][1]);
        }
    }
    return values;
}`),
	lines(`// lineBytes is each of name's lines, which holds no -, as a Buffer of the bytes the client sent, read as Envoy reads a
// value (asEnvoyReads), in the order sent. A string of r.rawHeadersIn holds one U+FFFD for each run of bytes outside
// well-formed UTF-8 (sequence), and r.rawVariables holds the lines' bytes joined with ", " (measured on nginx 1.30.6
// and 1.31.8), so each line's bytes are read from the second in step with its string from the first, a U+FFFD the
// client did not send standing for the run the decoder read as one. A step the two do not agree on throws, and the
// caller's catch sends nothing.
function lineBytes(r, name) {
    var lines = allLines(r, name), out = [];
    if (lines.length == 0) {
        return out;
    }
    var raw = r.rawVariables["http_" + name], p = 0;
    for (var i = 0; i < lines.length; i++) {
        if (i > 0) {
            if (raw[p] != 0x2C || raw[p + 1] != 0x20) {
                throw "unread";
            }
            p += 2;
        }
        var from = p, text = Buffer.from(lines[i]);
        for (var q = 0; q < text.length;) {
            if (text[q] == 0xEF && text[q + 1] == 0xBF && text[q + 2] == 0xBD &&
                !(raw[p] == 0xEF && raw[p + 1] == 0xBF && raw[p + 2] == 0xBD)) {
                var n = raw[p] >= 0x80 ? sequence(raw, p) : 0;
                if (n >= 0) {
                    throw "unread";
                }
                p -= n;
                q += 3;
            } else if (raw[p] === text[q]) {
                p++;
                q++;
            } else {
                throw "unread";
            }
        }
        var v = r.httpVersion == "2.0" ? [from, p] : trim(raw, from, p);
        out.push(raw.subarray(v[0], v[1]));
    }
    if (p != raw.length) {
        throw "unread";
    }
    return out;
}`),
	lines(`// joined is one Buffer of the Buffers of lines with the text between after each but the last. A line break and a
// header's name between them make nginx write each line as a header line of its own, as a line break in a value nginx
// sends ends the line it writes (measured on nginx 1.30.6 and 1.31.8).
function joined(lines, between) {
    var parts = [];
    for (var i = 0; i < lines.length; i++) {
        if (i > 0) {
            parts.push(Buffer.from(between));
        }
        parts.push(lines[i]);
    }
    return Buffer.concat(parts);
}`),
	lines(`// parentOf reads the client's traceparent as Envoy 1.40.2 reads it, its first line as asEnvoyReads gives it: null
// when the client sent none, "bad" when that is not 55 characters of four hexadecimal parts of 2, 32, 16 and 2 digits,
// or either id is all zeros, and otherwise its value, its version, whether its flags say sampled, and whether nginx's
// tracer continues it as the Envoy output does. Envoy continues any version; nginx's tracer reads only 00, and reads
// the traceparent and the first tracestate line as nginx keeps them, with a tab beside the value: it does not continue
// such a traceparent, and it puts such a tracestate into the span with its tab. So a request of another version, or
// with a tab beside either, gets no span and goes on with the client's version and ids (measured on nginx 1.30.6 and
// 1.31.8 beside Envoy 1.40.2).
function parentOf(r) {
    var line = firstLine(r, "traceparent");
    if (line === null) {
        return null;
    }
    var value = asEnvoyReads(r, line);
    if (value.length != 55 || !/^[0-9a-fA-F]{2}-[0-9a-fA-F]{32}-[0-9a-fA-F]{16}-[0-9a-fA-F]{2}$/.test(value) ||
        /^..-0{32}-/.test(value) || /-0{16}-..$/.test(value)) {
        return "bad";
    }
    var version = value.substring(0, 2), state = firstLine(r, "tracestate");
    return {value: value, version: version, sampled: (parseInt(value.substring(53, 55), 16) & 1) === 1,
        continued: version == "00" && value === line && (state === null || state === asEnvoyReads(r, state))};
}`),
	lines(`// drawn is Envoy's draw: the first eight characters of the request id, after an optional 0x, read as hexadecimal,
// modulo 10000, below share (sample.rate times 100). An id shorter than eight characters, or with another character
// among them, is never drawn, so an id that is not a UUID is drawn by its first eight characters.
function drawn(id, share) {
    if (id.length < 8) {
        return false;
    }
    var head = id.substring(0, 8);
    if (head[0] == "0" && (head[1] == "x" || head[1] == "X")) {
        head = head.substring(2);
    }
    if (!/^[0-9a-fA-F]+$/.test(head)) {
        return false;
    }
    return parseInt(head, 16) % 10000 < share;
}`),
	lines(`// requestId is the request id as Envoy reads it (asEnvoyReads), which the draw and the span read: nginx keeps a tab
// beside a client's id.
function requestId(r) {
    try {
        return asEnvoyReads(r, r.variables.pf_request_id);
    } catch (e) {
        return "";
    }
}`),
	lines(`// targetTraced says whether a span may hold the request's target. nginx's tracer adds the target as the client wrote
// it, as http.target, which no line removes, where the Envoy output's URL holds the path it sends on, and in its
// place a client's x-envoy-original-path under client_address forwarded. So only a target nginx sends on as written
// and of at most 256 bytes ($pf_target_traced), on a route with no rewrite in a server of an environment
// ($pf_rewritten), beside no line of the header $pf_original_path names, is held by a span.
function targetTraced(r) {
    var original = r.variables.pf_original_path;
    return r.variables.pf_target_traced === "1" && (r.variables.server_name === "" || r.variables.pf_rewritten === "0") &&
        (original === "" || firstLine(r, original) === null);
}`),
	lines(`// userAgentTraced says whether a span may hold the request's User-Agent. nginx's tracer adds it as nginx keeps it, as
// http.user_agent, which no line removes, where the Envoy output's user_agent holds it as Envoy reads it: over HTTP/1
// nginx keeps a tab at either end, which Envoy removes (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
function userAgentTraced(r) {
    var agent = firstLine(r, "user-agent");
    return agent === null || agent === asEnvoyReads(r, agent);
}`),
	lines(`// sampledByDraw says whether the Envoy output marks the request sampled, whatever the traceparent's version: never
// with a malformed traceparent, one that says not sampled, or a target or a User-Agent no span may hold
// (targetTraced, userAgentTraced), which nginx sends on not sampled where the Envoy output samples it, and otherwise
// by the draw on the request id (requestId). A traceparent that says sampled does not force a trace, as with Envoy.
function sampledByDraw(r, parent) {
    if (parent === "bad" || (parent !== null && !parent.sampled)) {
        return false;
    }
    if (!targetTraced(r) || !userAgentTraced(r)) {
        return false;
    }
    return drawn(requestId(r), Number(r.variables.pf_trace_share));
}`),
	lines(`// traced says whether the request gets a span: when the Envoy output marks it sampled, but never for a traceparent
// nginx's tracer does not continue (parentOf), for a request refused before any route ($pf_refusal: a method, version,
// scheme, target or header Envoy refuses, a fragment, a QUERY with no Content-Type, HTTP/1.0), or for an upgrade the
// route choice of a server of an environment refuses ($pf_route), for which the Envoy output writes no span either
// (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). The default server, which answers an upgrade 404 as it
// answers any request, traces it, as the Envoy output does on a host no environment names.
function traced(r) {
    try {
        var parent = parentOf(r);
        if (r.variables.pf_refusal !== "0" || (parent !== null && parent !== "bad" && !parent.continued) ||
            (r.variables.server_name !== "" && /^upgrade-refused-/.test(r.variables.pf_route))) {
            return "off";
        }
        return sampledByDraw(r, parent) ? "on" : "off";
    } catch (e) {
        return "off";
    }
}`),
	lines(`// traceparent is what the service receives: a malformed traceparent as the client sent it, each line as a line of its
// own, as Envoy passes them on (lineBytes); one nginx's tracer does not continue (parentOf) with the client's version
// and ids and the flag the Envoy output writes for it; and otherwise the span's own, sampled when the request is
// traced.
function traceparent(r) {
    try {
        var parent = parentOf(r);
        if (parent === "bad") {
            return joined(lineBytes(r, "traceparent"), "\r\ntraceparent: ");
        }
        if (parent !== null && !parent.continued) {
            return parent.value.substring(0, 53) + (sampledByDraw(r, parent) ? "01" : "00");
        }
        return "00-" + r.variables.otel_trace_id + "-" + r.variables.otel_span_id + "-0" +
            (r.variables.pf_otel_trace === "on" ? "1" : "0");
    } catch (e) {
        return "";
    }
}`),
	lines(`// tracestate is the client's tracestate lines as it sent them (lineBytes), sent only beside a traceparent, as the
// Envoy output drops a client's tracestate that comes without one: beside a malformed traceparent each line as a line
// of its own, as Envoy passes them on, and otherwise joined by commas, an empty line too, as Envoy joins them.
function tracestate(r) {
    try {
        var parent = parentOf(r);
        if (parent === null) {
            return "";
        }
        return joined(lineBytes(r, "tracestate"), parent === "bad" ? "\r\ntracestate: " : ",");
    } catch (e) {
        return "";
    }
}`),
	lines(`// checkTraceparent and checkTracestate are what the check receives: the service's, but neither beside a malformed
// traceparent, as the Envoy output's check request carries no trace when its span cannot read the client's.
function checkTraceparent(r) {
    try {
        return parentOf(r) === "bad" ? "" : traceparent(r);
    } catch (e) {
        return "";
    }
}`),
	lines(`function checkTracestate(r) {
    try {
        return parentOf(r) === "bad" ? "" : tracestate(r);
    } catch (e) {
        return "";
    }
}`),
	lines(`// traceLines is "1" when the client's traceparent is malformed and it, or the tracestate beside it, comes on more than
// one line, and on a run that fails, and "0" otherwise. A gRPC location refuses such a call: the Envoy output passes
// each line on as a field of its own, and nginx's gRPC module sends one field for each header line it writes.
function traceLines(r) {
    try {
        return parentOf(r) === "bad" && (allLines(r, "traceparent").length > 1 || allLines(r, "tracestate").length > 1) ?
            "1" : "0";
    } catch (e) {
        return "1";
    }
}`),
	lines(`// readTags reads, in one pass over the request's lines, each source $pf_tag_sources names, once however many tags read
// it: h:<name> the first line of the client's header as Envoy reads it, j:<name> the lines of a header Envoy joins,
// joined as it joins them (joinedValues), v:<variable> a header the proxy writes itself, read from the variable it
// writes it with as Envoy reads it, none when empty and empty when it holds the one space the check's handler stores
// for a value the check sent empty, a: a header the proxy removes, always none, e:<name> the environment's variable of
// that name, none when unset: a name every object inherits, such as constructor, reads a value that is no string, which
// counts as none. The type is read where hasOwnProperty would ask the same, because that call on process.env in each
// traced request left each worker of nginx 1.31.8 holding about 25 MiB more at 1,000 requests a second. For the source
// at position n from 1 it writes the value to $pf_tv_<n>, or n to $pf_ts_<n> for none and e for an empty value; each
// tag's map reads its source's two variables, so no map reads another source's value. Nothing is written unless every
// source was read, and it answers "", which each map's key starts with, so the pass runs before a map reads them.
function readTags(r) {
    try {
        var sources = r.variables.pf_tag_sources.split(" "), raw = r.rawHeadersIn, lines = {}, values = [];
        for (var i = 0; i < raw.length; i++) {
            var key = " " + raw[i][0].toLowerCase();
            if (lines[key] === undefined) {
                lines[key] = [];
            }
            lines[key].push(raw[i][1]);
        }
        for (var i = 0; i < sources.length; i++) {
            var kind = sources[i].substring(0, 2), name = sources[i].substring(2), value = null, sent = lines[" " + name];
            if (kind == "h:") {
                value = sent === undefined ? null : asEnvoyReads(r, sent[0]);
            } else if (kind == "j:") {
                value = sent === undefined ? null : joinedValues(r, sent);
            } else if (kind == "v:") {
                value = r.variables[name] || null;
                if (value === " ") {
                    value = "";
                } else if (value !== null) {
                    value = asEnvoyReads(r, value);
                }
            } else if (kind == "e:") {
                value = process.env[name];
                if (typeof value != "string") {
                    value = null;
                }
            }
            values.push(value);
        }
        for (var i = 0; i < values.length; i++) {
            if (values[i] === null) {
                r.variables["pf_ts_" + (i + 1)] = "n";
            } else if (values[i] === "") {
                r.variables["pf_ts_" + (i + 1)] = "e";
            } else {
                r.variables["pf_tv_" + (i + 1)] = values[i];
            }
        }
    } catch (e) {
    }
    return "";
}`),
}
