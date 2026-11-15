package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// check answers the full example's auth check by the token after "Bearer " in the authorization header, which
// auth.send passes on. A request with no token it knows, or none at all, is denied with 401, so a row that wants the
// check not to be asked (the example's auth.only_with_header) fails when the proxy asks it anyway.
func (b *backend) check(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("authorization"), "Bearer ")
	if text, ok := rawAnswers[token]; ok {
		rawAnswer(w, text)
		return
	}
	switch token {
	case "allow":
		// x-user-check reports what the proxy told the check, so a scenario reads it back from the service's echo.
		allowed(w, fmt.Sprintf("%s %s action=%s reports=%s country=%s",
			r.Header.Get("x-forwarded-method"), r.Header.Get("x-forwarded-uri"), r.Header.Get("x-gateway-action"),
			r.Header.Get("x-gateway-reports"), r.Header.Get("x-country")))
		w.WriteHeader(http.StatusOK)
	case "report":
		// The two headers the Envoy output's client for the check adds to every check it asks.
		allowed(w, fmt.Sprintf("internal=%s expected=%s", r.Header.Get("x-envoy-internal"),
			r.Header.Get("x-envoy-expected-rq-timeout-ms")))
		w.WriteHeader(http.StatusOK)
	case "report-forwarded":
		allowed(w, fmt.Sprintf("proto=%s host=%s", r.Header.Get("x-forwarded-proto"), r.Host))
		w.WriteHeader(http.StatusOK)
	case "big-accept":
		allowed(w, "")
		w.Header().Set("x-user-id", strings.Repeat("u", 20000))
		w.WriteHeader(http.StatusOK)
	case "deny-401":
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "denied by the check")
	case "deny-401-message":
		// A percent sign, a letter outside ASCII and a line feed, each of which a gRPC denial's grpc-message carries
		// percent-encoded.
		w.Header().Set("content-type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "no entry: 100% sure é\nnext line")
	case "deny-403":
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "denied by the check")
	case "big-denial":
		w.Header().Set("content-type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, strings.Repeat("d", 20000))
	case "big-header":
		w.Header().Set("x-big", strings.Repeat("h", 50000))
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "denied by the check")
	case "many-headers":
		// One header line more than the 100 Envoy reads in an answer.
		for i := 1; i <= 101; i++ {
			w.Header().Set(fmt.Sprintf("x-h-%d", i), "1")
		}
		w.WriteHeader(http.StatusUnauthorized)
	case "status-201":
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "created")
	case "status-503":
		w.WriteHeader(http.StatusServiceUnavailable)
	case "trickle":
		trickle(w, http.StatusUnauthorized)
	case "trickle-200":
		allowed(w, "")
		trickle(w, http.StatusOK)
	case "hang":
		time.Sleep(b.hang)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "denied by the check")
	}
}

// allowed sets the headers of the check's allowing answer: two the full example's auth.accept admits, x-other, which
// it does not, so the proxy must not pass it on to the service, and x-user-check set to report, a scenario reading it
// back from the service's echo.
func allowed(w http.ResponseWriter, report string) {
	w.Header().Set("x-user-id", "u-1")
	w.Header().Set("x-tier-level", "gold")
	w.Header().Set("x-other", "not-accepted")
	w.Header().Set("x-user-check", report)
}

// trickle answers status with a body of 100 bytes, sent one byte every 100 ms, so it ends after 10 s, long past the
// battery files' auth.timeout of 1500 ms: a proxy's own timeout on the socket counts only the time without data, so a
// proxy that waits for the whole answer waits the 10 s.
func trickle(w http.ResponseWriter, status int) {
	w.Header().Set("content-length", "100")
	w.WriteHeader(status)
	flusher := w.(http.Flusher)
	for range 100 {
		if _, err := io.WriteString(w, "t"); err != nil {
			return
		}
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}
}

// rawAnswers are the check's answers net/http will not write, by token, written as they are before the connection
// closes: each is an answer RFC 9112 does not read whole, which Envoy 1.40.2 treats as an unanswered check, but
// status-600, whose status Envoy passes on to the client (measured).
var rawAnswers = map[string]string{
	"length-not-a-number":     "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nContent-Length: 0x0\r\n\r\n",
	"two-lengths":             "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nContent-Length: 0\r\nContent-Length: 0\r\n\r\n",
	"control-byte":            "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nx-a: a\x01b\r\nContent-Length: 0\r\n\r\n",
	"status-not-three-digits": "HTTP/1.1 2x0 OK\r\nx-user-id: u-1\r\nContent-Length: 0\r\n\r\n",
	"no-space-before-reason":  "HTTP/1.1 200OK\r\nx-user-id: u-1\r\nContent-Length: 0\r\n\r\n",
	"status-600":              "HTTP/1.1 600 Other\r\nx-user-id: u-1\r\nContent-Length: 0\r\n\r\n",
	"name-not-a-token":        "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nx@y: 1\r\nContent-Length: 0\r\n\r\n",
	"length-and-chunked":      "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
	"chunk-not-hex":           "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nTransfer-Encoding: chunked\r\n\r\n0z\r\n\r\n",
	"bad-line-in-1xx":         "HTTP/1.1 100 Continue\r\nx@y: 1\r\n\r\nHTTP/1.1 200 OK\r\nx-user-id: u-1\r\nContent-Length: 0\r\n\r\n",
	"bad-trailer":             "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nTransfer-Encoding: chunked\r\n\r\n0\r\nx@y: 1\r\n\r\n",
	"cut-body":                "HTTP/1.1 401 Unauthorized\r\ncontent-type: text/plain\r\ncontent-length: 100\r\n\r\n" + strings.Repeat("c", 10),
	// Envoy 1.40.2 reads 81,920 bytes of a head, and of a trailer section, and treats an answer with more as
	// unanswered (measured to the byte): each answer below is 20 bytes within the bound or 20 past it. x-folded, which
	// no battery file accepts, takes folded lines, so no accepted header outgrows the request.
	"head-within": padded(allowedHead("head-within")+"content-length: 0\r\n", 81900, false),
	"head-past":   padded(allowedHead("head-past")+"content-length: 0\r\n", 81940, false),
	"fold-past":   padded(allowedHead("fold-past")+"content-length: 0\r\nx-folded: a\r\n", 81940, true),
	"1xx-past":    padded("HTTP/1.1 100 Continue\r\n", 81940, false) + allowedHead("1xx-past") + "content-length: 0\r\n\r\n",
	"trailer-within": allowedHead("trailer-within") + "transfer-encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n" +
		padded("", 81900, false),
	"trailer-past": allowedHead("trailer-past") + "transfer-encoding: chunked\r\n\r\n4\r\nbody\r\n0\r\n" +
		padded("", 81940, false),
	// Envoy 1.40.2 reads at most 100 header lines in a head and does not count a folded line among them; it counts the
	// entries of its header map, in which a name it keeps inline, such as date, takes one entry however many lines
	// carry it, and set-cookie an entry a line (measured).
	"fold-after-100":       "HTTP/1.1 401 Unauthorized\r\ncontent-length: 0\r\n" + numbered(99) + " f\r\n\r\n",
	"allow-fold-after-100": allowedHead("allow-fold-after-100") + "content-length: 0\r\n" + numbered(97) + " f\r\n\r\n",
	"1xx-fold-after-100": "HTTP/1.1 100 Continue\r\n" + numbered(100) + " f\r\n\r\n" + allowedHead("1xx-fold-after-100") +
		"content-length: 0\r\n\r\n",
	"date-twice-in-101":   "HTTP/1.1 401 Unauthorized\r\ncontent-length: 0\r\ndate: v1\r\n" + numbered(98) + "date: v2\r\n\r\n",
	"cookie-twice-in-101": "HTTP/1.1 401 Unauthorized\r\ncontent-length: 0\r\nset-cookie: a=1\r\n" + numbered(98) + "set-cookie: b=2\r\n\r\n",
}

// numbered is n header lines, x-n-001: v and on, each name once.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "x-n-%03d: v\r\n", i)
	}
	return b.String()
}

// allowedHead is the start of a 200 whose x-user-check reports the token, for the raw answers a row reads back.
func allowedHead(token string) string {
	return "HTTP/1.1 200 OK\r\nx-user-id: u-1\r\nx-user-check: " + token + "\r\n"
}

// padded is lines followed by header lines of at most 7,900 bytes of value, x-pad-<n> or, with fold, folded lines,
// and the empty line that ends them, size bytes in all. In the answers above no line's CR is a multiple of 16,384
// bytes into the answer, where HAProxy's socket buffer splits a CR from its LF and the reader refuses the line
// (auth.ask).
func padded(lines string, size int, fold bool) string {
	var b strings.Builder
	b.WriteString(lines)
	for i := 0; ; i++ {
		name := fmt.Sprintf("x-pad-%02d: ", i)
		if fold {
			name = " "
		}
		if left := size - b.Len() - len(name) - 4; left <= 7900 {
			b.WriteString(name + strings.Repeat("p", left) + "\r\n\r\n")
			return b.String()
		}
		b.WriteString(name + strings.Repeat("p", 7900) + "\r\n")
	}
}

// rawAnswer writes text on the request's connection and closes it. A connection net/http cannot hand over, an
// HTTP/2 one, gets a 500 that names why, so the row that asked for the answer fails instead of reading another.
func rawAnswer(w http.ResponseWriter, text string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "a raw answer needs an HTTP/1 connection", http.StatusInternalServerError)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	buffered.WriteString(text)
	buffered.Flush()
}
