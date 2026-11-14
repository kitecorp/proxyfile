package conformance

import (
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
	// repeat sends the request this many times back to back, and at least one answer after the first admitted must be
	// what want asks. "At least one" because a bucket that refills while the requests go out may admit one of them.
	repeat   int
	admitted int  // with repeat, the first admitted answers must be 200
	every    bool // with repeat, every answer must be what want asks, instead of at least one
	// refusing names a backend container by its role, such as shop-2, that refuses connections on every port while
	// the row's requests go out, as a backend that is down does (world.refuse).
	refusing string
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
	// forgedMark sends the target's unchecked mark with the value true, a copy no proxy may pass on: only the proxy
	// may tell the service that a request went through unchecked.
	forgedMark bool
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
	marked                          string            // the value of the target's unchecked mark the stub must receive
	unmarked                        bool              // the stub must not receive the target's unchecked mark
	attempt                         int
	atLeast, below                  time.Duration
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
		if got.err != nil || got.echo == nil && got.status >= 400 {
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
	case want.service == "" && got.echo != nil:
		differ("service: got %q, want no service", e.Service)
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
	for _, name := range want.absent {
		if value, ok := e.Headers[name]; ok {
			differ("received %s: got %q, want none", name, value)
		}
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
