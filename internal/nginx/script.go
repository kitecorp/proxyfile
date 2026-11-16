package nginx

import (
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
)

// scriptName is the name of the script the configuration loads, written beside it: nginx reads a relative name in
// js_import from the folder of the configuration it was started with.
const scriptName = "proxyfile.js"

// The names the configuration and the script share for the auth check. checkLocation is the path the access handler
// asks with a subrequest, the location that proxies it to the relay. That location sends the client's method in
// methodHeader and the path of auth.ask in pathHeader, which the relay reads for the request line it sends the check
// and never sends on; setting both there replaces a client's copy of either. The relay answers nginx with its verdict
// in answerHeader, in the head of its own answer, and with the check's header lines only inside the body it frames by
// lengths, so a check's header line named answerHeader is read as any other.
const (
	checkLocation = "/.proxyfile/check"
	methodHeader  = "x-proxyfile-method"
	pathHeader    = "x-proxyfile-path"
	answerHeader  = "x-proxyfile-check"
)

// scriptBody is the script after the lines script writes above it (script.js beside this file).
//
//go:embed script.js
var scriptBody string

// script is the script beside the configuration: generator.Header in the comment syntax of JavaScript; the lists the
// auth check reads, generator.FramingHeaders (never sent from a client to the check) and generator.HopHeaders (never
// passed to the client with a denial); Envoy's gRPC status for each HTTP status of a denial (generator.GRPCStatus);
// the names the configuration and the script share; then the body. Only constants are written, so the script is the
// same bytes for every file, and two files rendered into one folder share it.
func script() []byte {
	return []byte("// " + strings.TrimPrefix(generator.Header, "# ") + "\n" +
		"var framing = " + jsList(generator.FramingHeaders) + ";\n" +
		"var hop = " + jsList(generator.HopHeaders) + ";\n" +
		"var grpcStatus = " + jsStatuses(generator.GRPCStatus) + ";\n" +
		"var checkLocation = " + jsString(checkLocation) + ";\n" +
		"var methodHeader = " + jsString(methodHeader) + ", pathHeader = " + jsString(pathHeader) +
		", answerHeader = " + jsString(answerHeader) + ";\n\n" + scriptBody)
}

// jsList writes names as a JavaScript array of strings, in their order.
func jsList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = jsString(name)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// jsStatuses writes Envoy's gRPC status for each HTTP status as a JavaScript object, in the order of the HTTP
// statuses, so the script is the same bytes every time.
func jsStatuses(statuses map[int]int) string {
	codes := slices.Sorted(maps.Keys(statuses))
	pairs := make([]string, len(codes))
	for i, code := range codes {
		pairs[i] = fmt.Sprintf("%d: %d", code, statuses[code])
	}
	return "{" + strings.Join(pairs, ", ") + "}"
}

// jsString writes text, which is ASCII, as a JavaScript string in double quotes: " and \ escaped, and every byte
// outside printable ASCII as \x and two hexadecimal digits, so no byte of the text ends the string or the line.
func jsString(text string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\x%02x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
