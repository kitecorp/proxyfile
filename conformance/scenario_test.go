package conformance

import (
	"crypto/tls"
	"fmt"
	"maps"
	"reflect"
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
	// refusing names a backend container by its role, such as shop-2, that refuses connections on every port while
	// the row's requests go out, as a backend that is down does (world.refuse).
	refusing string
	// flood, when set, sends the row's request many times at once with other paths, and wants the proxy's memory to
	// stay within a bound (sendFlood).
	flood *flood
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

// own is a target's own literal for a row, and difference the row of the support table (format.MatrixRows) whose
// partial cell names the difference that gives it. files, when set, are the labels of the files of the target's
// entries it holds on, where the difference depends on what a file writes; on the target's other files the row wants
// what it wants of every target.
type own struct {
	difference string
	files      []string
	literal
}

// literal is what a row wants on one file: the answer, and with repeat how many answers before it must be 200.
type literal struct {
	want     answer
	admitted int
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
	// with Transfer-Encoding: chunked, and over h2 as a DATA frame and a second HEADERS frame.
	body     string
	trailers map[string]string
	// unframed, on a raw http/1.1 request, writes body after the headers as it is, with no Transfer-Encoding of the
	// sender's own, so the row's headers frame it: two Content-Length lines, or one beside Transfer-Encoding.
	unframed bool
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
	body                            string            // the body the stub received; "" is not compared
	noTrailers                      bool              // the stub must have received no trailer
	marked                          string            // the value of the target's unchecked mark the stub must receive
	unmarked                        bool              // the stub must not receive the target's unchecked mark
	// unreadable wants a stub's own answer to a request its HTTP server could not read (result.stubError): the proxy
	// passed the request on as sent. No service is compared then, because that answer names none.
	unreadable     bool
	attempt        int
	atLeast, below time.Duration
}

// compare returns each way got differs from want, in a fixed order, so a failing row reads the same on every run.
// mark is the target's unchecked mark, which marked and unmarked speak of.
func compare(want answer, got result, mark string) []string {
	var e echo
	if got.echo != nil {
		e = *got.echo
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
	if want.atLeast != 0 && got.elapsed < want.atLeast {
		differ("time: got %s, want at least %s", got.elapsed, want.atLeast)
	}
	if want.below != 0 && got.elapsed >= want.below {
		differ("time: got %s, want below %s", got.elapsed, want.below)
	}
	return diffs
}
