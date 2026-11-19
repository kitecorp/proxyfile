package conformance

import (
	"crypto/tls"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A scenario asserts only what every proxy must answer for the core keys it names: the status, which stub answered
// and what it received, the headers that matter, grpc-status and a time window. It never asserts a local reply's
// body, or that an answer carries a header a proxy names after itself, which docs/portability.md classes as the
// proxy's own, so a new target changes nothing here. A row may assert such a header absent, which every proxy answers.
type scenario struct {
	name      string
	quiet     time.Duration // waited before the first exchange
	exchanges []exchange
}

// exchange is one request and the answer it must get.
type exchange struct {
	name string
	// keys are the core keys the row's answer depends on (format.CoreKeys spelling). The row runs only on a file that
	// writes every one of them, because a file without one gives another answer, which says nothing of the proxy; a
	// row with none runs on every file.
	keys []string
	send request
	want answer
	// on holds, by a file's label, the row's literal on a file whose values give another answer than want and
	// admitted, measured on Envoy.
	on map[string]literal
	// by holds, by a target's name, the target's own literal on every file of its entries, where the target answers
	// otherwise than Envoy by a difference its declaration names, measured on that target. It comes before on and
	// want (literalFor), and the battery fails one whose difference the target does not declare partial, so a row
	// never takes a target's answer that docs/portability.md does not show.
	by map[string]own
	// repeat sends the request this many times back to back, and at least one answer after the first admitted must be
	// what want asks. "At least one" because a bucket that refills while the requests go out may admit one of them.
	repeat   int
	admitted int  // with repeat, the first admitted answers must be 200
	every    bool // with repeat, every answer must be what want asks, instead of at least one
	// alternate, with repeat, is sent in place of send by every second request, so a row's answers are to two requests:
	// with oneInstance, both must reach one backend.
	alternate *request
	// refusing names the backend containers, by their roles and separated by commas, such as shop-2 or shop-1,shop-2,
	// that refuse connections on every port while the row's requests go out, as a backend that is down does
	// (world.refuse).
	refusing string
	// flood, when set, sends the row's request many times at once with other paths, and wants the proxy's memory to
	// stay within a bound (sendFlood).
	flood *flood
	// holds, when set, keeps many connections open at once, each sending the start of a request's head and then
	// nothing, and wants the proxy's memory to stay within a bound (sendHolds).
	holds *holds
	// overlap sends the row's request at the start of its scenario, on connections of its own, and compares its answer
	// once the rows in sequence are done (runFile): a row that waits for a proxy's timer, such as auth.timeout's, waits
	// beside the others. It is for a row that sends one request, refuses no backend and floods nothing.
	overlap bool
	// streams, when set, sends the row's request this many times at once over one HTTP/2 connection, each with its own
	// number in its query, and wants each answer to be want with that number in place of {i} in its received values
	// (sendStreams): each stream answered by its own check.
	streams int
	// refill, on a file that writes routes[].rate_limit, is waited before the row is sent, so that a limited route's
	// bucket fills again: rows a limited route serves, sent one after the other on a fast runner, can spend its bucket
	// before it refills, and a row past that gets 429 for no fault of the proxy. The rate limit scenario's quiet is the
	// same wait for a whole scenario, before its first row. A row that overlaps sends its request before the rows in
	// sequence, so it names none.
	refill time.Duration
}

// flood is what a row floods the proxy with: requests requests from connections HTTP/1.1 connections at once, request
// i to path(i), each answer what the row wants. The proxy's memory, read before and after (world.memory), must grow by
// less than fixed bytes and perThread bytes for each CPU Docker gives a container (dockerCPUs): HAProxy runs a thread
// for each, and each thread keeps the states of its own patterns, where Envoy's RE2 keeps a cache for each pattern,
// whatever its workers.
type flood struct {
	connections, requests int
	path                  func(i int) string
	fixed, perThread      int64
}

// holds is what a row keeps open on the proxy: connections connections, each opened as the row's raw request would be
// (dialRaw), sending head, a request's head with no end, then silent. The proxy's memory, read before the first opens
// and settle after the last did (world.memory), must grow by less than perConnection bytes for each, and each must
// still be open with no answer right after, so the memory read is that of connections the proxy kept. The row compares
// no answer, so its want is empty.
type holds struct {
	connections   int
	head          string
	settle        time.Duration
	perConnection int64
}

// own is a target's own literal for a row, and difference the row of the support table (format.MatrixRows) whose
// partial cell names the difference that gives it. files, when set, are the labels of the files of the target's
// entries it holds on, where the difference depends on what a file writes; on the target's other files the row wants
// what it wants of every target.
type own struct {
	difference string
	files      []string
	literal
	// other, when set, is the target's literal on its files outside files, by another difference: a request one file
	// makes the target answer otherwise by one row of the support table and another file by another.
	other *own
}

// literal is what a row wants on one file: the answer, and with repeat how many answers before it must be 200.
type literal struct {
	want     answer
	admitted int
	// also, on a row that says every, is the other answer the answers after the first admitted may be, on a file whose
	// values give only some of the requests want: each must be want or also, and at least one must be each. On a file
	// whose route writes retries: 0, a retry row's requests that the refusing backend draws get the proxy's answer, and
	// the others the service's.
	also *answer
}

// request is what the battery sends to the proxy's listener.
type request struct {
	proto         string // "http/1.1", "h2", or "grpc": HTTP/2 POST with content-type application/grpc, te trailers and the empty message 00 00 00 00 00
	method        string
	host          string
	path          string // sent exactly as written (URL.Opaque)
	headers       map[string]string
	noCertificate bool
	otherCA       bool // the client certificate is one a CA other than the clients CA signed, with the same name
	expired       bool // the client certificate is one the clients CA signed whose validity ended
	// serverName, when set, is the TLS server name the client asks for in place of the host's (serverNameFor).
	serverName string
	// offer, when set, is what the client offers in its TLS handshake in place of crypto/tls's defaults, over
	// http/1.1 with the client certificate the clients CA signed.
	offer *tlsOffer
	// forgedMark sends the target's unchecked mark with the value true, a copy no proxy may pass on: only the proxy
	// may tell the service that a request went through unchecked.
	forgedMark bool
	// raw sends the request by hand (sendRaw), with the header names as written and, over http/1.1, line as the
	// request line when set: net/http refuses a name outside RFC 9110's token characters and writes one space between
	// the parts of a request line.
	raw  bool
	line string
	// body and trailers, on a raw request, are sent as a chunked body that ends with these trailers, over http/1.1
	// with Transfer-Encoding: chunked, and over h2 as a DATA frame and a second HEADERS frame. On a request that is
	// not raw, body is sent by net/http, framed by its length, and no trailers are sent.
	body     string
	trailers map[string]string
	// unframed, on a raw http/1.1 request, writes body after the headers as it is, with no Transfer-Encoding of the
	// sender's own, so the row's headers frame it: two Content-Length lines, or one beside Transfer-Encoding.
	unframed bool
	// halfClose, on a raw http/1.1 request, closes the sender's side of the connection once the request is written, and
	// reads the answer on the side left open, as a client does that sends its request and then a FIN.
	halfClose bool
	// pause, on an unframed request, sends the second half of body this long after the rest, the answer read
	// meanwhile, so a proxy that answers, or closes the connection, before the body ends is read as doing so then.
	pause time.Duration
	// scheme, on a raw h2 request, is the :scheme sent in place of the connection's own, https with tls and http
	// without, as a client in front may send one that differs from the connection it uses.
	scheme string
	// then, on a raw unframed http/1.1 request, is a second request written on the same connection right after the
	// body, in the same write, as a client that reuses its connection sends its next request: the answer compared is
	// the second's, and the first's status is answer.first. A proxy that answers the first without reading its body
	// must still read the body as the body, never as a request.
	then string
}

// tlsOffer is a client's TLS offer: versions from and to version, or crypto/tls's when 0, and only these TLS 1.2
// cipher suites and these key exchange groups, or crypto/tls's when empty.
type tlsOffer struct {
	version uint16
	ciphers []uint16
	groups  []tls.CurveID
}

// answer is what a row wants back. A zero value of a field is not compared, except service, whose "" wants no stub
// to have answered.
type answer struct {
	status int // 0 is not compared
	// refused wants a TLS error, or a 4xx or 5xx no stub wrote, and nothing else: compare checks nothing more of a
	// refusal, so a row that sets another field beside it fails rather than read as checking that field.
	refused                         bool
	headers                         map[string]string // response headers that must hold exactly these values
	grpcStatus                      string            // grpc-status from the trailers, or from the headers of a trailers-only answer
	service                         string            // the stub that answered; "" means none may have
	instance                        string            // the backend of service that answered; "" is not compared
	port, proto, method, path, host string            // what that stub received; "" is not compared
	received                        map[string]string // headers the stub received, with these values
	absent                          []string          // headers the stub must not have received
	oneLine                         []string          // headers the stub must have received as one line each
	replaced                        map[string]string // headers the client sent with these values; the stub must receive each with another value
	// forms holds headers the stub must have received with a value the regular expression matches, and that the answer
	// must carry with the same value: a value the proxy makes anew for each request, such as a request id.
	forms map[string]*regexp.Regexp
	// made holds response headers the answer must carry with a value the regular expression matches, for a value the
	// proxy makes anew on an answer no stub wrote, such as the request id of its own 404.
	made       map[string]*regexp.Regexp
	body       string // the body the stub received; "" is not compared
	noTrailers bool   // the stub must have received no trailer
	marked     string // the value of the target's unchecked mark the stub must receive
	unmarked   bool   // the stub must not receive the target's unchecked mark
	// first, with request.then, is the status the answer to the first request must have; 0 is not compared.
	first int
	// unreadable wants a stub's own answer to a request its HTTP server could not read (result.stubError): the proxy
	// passed the request on as sent. No service is compared then, because that answer names none.
	unreadable bool
	// oneInstance, on a row with repeat, wants every answer after the first admitted from one backend of the service,
	// whichever it is: a request whose hash picks the backend reaches the same one each time. compare reads one answer,
	// so sendRepeated, which reads them all, checks it.
	oneInstance    bool
	attempt        int
	atLeast, below time.Duration
	// logged holds, by the event a line of the proxy's log names, "request" for the request log's own line, which
	// names none, the fields of the one line of that event the proxy wrote for this request, each the JSON text of its
	// value matched by the regular expression; a nil entry wants no line of that event. The lines are found by the
	// request id the answer carries, x-request-id, which every proxy writes as the line's request_id (logLines).
	logged map[string]map[string]*regexp.Regexp
	// counted, on a row sent in sequence, wants lines of the proxy's log of an event that carries no request id, which
	// logged cannot find, such as gateway_upstream_failure (sendRepeated reads them).
	counted *countedLines
	// receivedForms holds headers the stub must have received with a value the regular expression matches, which the
	// answer need not carry, unlike forms: a value the proxy makes anew for the service alone, such as a traceparent.
	receivedForms map[string]*regexp.Regexp
	// spanned holds what the one span the collector kept for the request must carry: the span whose guid:x-request-id
	// is the x-request-id the row sent, two of which fail the row. Each key is an attribute's name, or name, parent or
	// state, or type:<attribute name> for the attribute's type, and its regular expression must match the value; a nil
	// expression wants the attribute absent. The span's trace id and span id must be those of the traceparent the service
	// received, when it received one of version 00, so the ids the service reads are the span's (compareSpans).
	spanned map[string]*regexp.Regexp
	// noSpan wants no span carrying the x-request-id the row sent, on the last read of the spans. A row that wants a span
	// or none runs only on a file whose world starts the collector (file.collector), and its spans are read with the
	// other such rows of its scenario (spanReads).
	noSpan bool
}

// countedLines are the lines of event a row wants among the lines the proxy wrote after the row began: those whose find
// fields match are the row's, exactly one for each request it sent, and each must match fields too. Each value is
// matched against the JSON text of the field's value, as logged is. The lines are told from another row's only by what
// find reads, so a row that wants them sends to a route no row beside it sends to.
type countedLines struct {
	event        string
	find, fields map[string]*regexp.Regexp
}

// compare returns each way got differs from want, in a fixed order, so a failing row reads the same on every run.
// mark is the target's unchecked mark, which marked and unmarked speak of.
func compare(want answer, got result, mark string) []string {
	var e echo
	if got.echo != nil {
		e = *got.echo
	}
	if got.unread != "" {
		return []string{got.unread}
	}
	if want.refused {
		if !reflect.DeepEqual(want, answer{refused: true}) {
			return []string{"a refused row compares nothing else"}
		}
		// A stub's own answer to a request it could not read carries no echo either, but the proxy passed the request on.
		if got.err != nil || got.echo == nil && !got.stubError && got.status >= 400 {
			return nil
		}
		return []string{fmt.Sprintf("refused: got %d from %q, want a TLS error or a refusal no service wrote",
			got.status, e.Service)}
	}
	if got.err != nil {
		return []string{fmt.Sprintf("error: %v", got.err)}
	}
	var diffs []string
	differ := func(format string, args ...any) { diffs = append(diffs, fmt.Sprintf(format, args...)) }
	// A gRPC row that fails on purpose leaves status 0: one proxy answers HTTP 200 with grpc-status, another a 5xx a
	// gRPC client reads as the same status.
	if want.status != 0 && got.status != want.status {
		differ("status: got %d, want %d", got.status, want.status)
	}
	if want.first != 0 && got.first != want.first {
		differ("first answer's status: got %d, want %d", got.first, want.first)
	}
	switch {
	case want.unreadable && !got.stubError:
		differ("service: got %d, want the stub's own answer to a request it could not read", got.status)
	case want.unreadable: // that answer names no service, so none is compared
	case want.service == "" && got.echo != nil:
		differ("service: got %q, want no service", e.Service)
	case want.service == "" && got.stubError:
		differ("service: got the stub's own %d for a request it could not read, want no service", got.status)
	case want.service != "" && got.echo == nil:
		differ("service: got no service, want %q", want.service)
	case want.service != e.Service:
		differ("service: got %q, want %q", e.Service, want.service)
	}
	for _, field := range []struct{ name, got, want string }{
		{"instance", e.Instance, want.instance}, {"port", e.Port, want.port}, {"proto", e.Proto, want.proto}, {"method", e.Method, want.method},
		{"path", e.Path, want.path}, {"host", e.Host, want.host},
	} {
		if field.want != "" && field.got != field.want {
			differ("%s: got %q, want %q", field.name, field.got, field.want)
		}
	}
	if want.attempt != 0 && e.Attempt != want.attempt {
		differ("attempt: got %d, want %d", e.Attempt, want.attempt)
	}
	for _, name := range slices.Sorted(maps.Keys(want.headers)) {
		if value := strings.Join(got.header.Values(name), ","); value != want.headers[name] {
			differ("header %s: got %q, want %q", name, value, want.headers[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.made)) {
		if value := strings.Join(got.header.Values(name), ","); !want.made[name].MatchString(value) {
			differ("header %s: got %q, want a value %s matches", name, value, want.made[name])
		}
	}
	if want.grpcStatus != "" {
		status := got.trailer.Get("grpc-status")
		if status == "" {
			// A proxy that refuses a call before any message sends grpc-status in the headers, with no trailers.
			status = got.header.Get("grpc-status")
		}
		if status != want.grpcStatus {
			differ("grpc-status: got %q, want %q", status, want.grpcStatus)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.received)) {
		if value := e.Headers[name]; value != want.received[name] {
			differ("received %s: got %q, want %q", name, value, want.received[name])
		}
	}
	for _, name := range want.oneLine {
		if value, ok := e.Headers[name]; !ok || strings.Contains(value, "\n") {
			differ("received %s: got %q, want one line", name, value)
		}
	}
	for _, name := range want.absent {
		if value, ok := e.Headers[name]; ok {
			differ("received %s: got %q, want none", name, value)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.replaced)) {
		if value, ok := e.Headers[name]; !ok || value == want.replaced[name] {
			differ("received %s: got %q, want a value other than the client's %q", name, value, want.replaced[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.forms)) {
		value, ok := e.Headers[name]
		if !ok || !want.forms[name].MatchString(value) {
			differ("received %s: got %q, want a value %s matches", name, value, want.forms[name])
		}
		if answered := strings.Join(got.header.Values(name), ","); answered != value {
			differ("header %s: got %q, want the %q the service received", name, answered, value)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.receivedForms)) {
		if value, ok := e.Headers[name]; !ok || !want.receivedForms[name].MatchString(value) {
			differ("received %s: got %q, want a value %s matches", name, value, want.receivedForms[name])
		}
	}
	if want.body != "" && e.Body != want.body {
		differ("received body: got %q, want %q", e.Body, want.body)
	}
	if want.noTrailers && len(e.Trailers) != 0 {
		differ("received trailers: got %q, want none", e.Trailers)
	}
	value, marked := e.Headers[mark]
	switch {
	case want.marked != "" && value != want.marked:
		differ("received the unchecked mark %s: got %q, want %q", mark, value, want.marked)
	case want.unmarked && marked:
		differ("received the unchecked mark %s: got %q, want none", mark, value)
	}
	if want.logged != nil && got.header.Get("x-request-id") == "" {
		differ("the answer carries no x-request-id, by which the proxy's log lines for the request are found")
	}
	for _, event := range slices.Sorted(maps.Keys(want.logged)) {
		lines, fields := got.logs[event], want.logged[event]
		switch {
		case fields == nil && len(lines) > 0:
			differ("logged %s: got %q, want no line", event, lines)
		case fields == nil:
		case len(lines) != 1:
			differ("logged %s: got %d lines, want one", event, len(lines))
		default:
			for _, name := range slices.Sorted(maps.Keys(fields)) {
				if value, ok := lines[0][name]; !ok || !fields[name].MatchString(value) {
					differ("logged %s %s: got %q, want a value %s matches", event, name, value, fields[name])
				}
			}
		}
	}
	if want.atLeast != 0 && got.elapsed < want.atLeast {
		differ("time: got %s, want at least %s", got.elapsed, want.atLeast)
	}
	if want.below != 0 && got.elapsed >= want.below {
		differ("time: got %s, want below %s", got.elapsed, want.below)
	}
	return diffs
}
