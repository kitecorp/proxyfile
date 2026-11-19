package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// backend is one service instance of the full example. Its controls are request headers, so a scenario steers the
// answer through the proxy without a second channel to the stub.
type backend struct {
	service, instance, state string
	hang                     time.Duration
}

func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok\n")
	case strings.HasPrefix(r.Header.Get("content-type"), "application/grpc"):
		b.grpc(w, r)
	case b.service == "accounts" && r.URL.Path == "/auth/check":
		b.check(w, r)
	default:
		b.reply(w, r)
	}
}

// reply answers with the echo, in the status x-stub-status names, or 200, after the time x-stub-delay names, and with
// x-stub-trickle, such as 2x1250ms, after that many pauses of that time that each end with a space, which JSON reads as
// nothing: a service silent past a proxy's idle time, and one that keeps sending within it.
func (b *backend) reply(w http.ResponseWriter, r *http.Request) {
	attempt, ok := b.firstTry(w, r)
	if !ok {
		return
	}
	// The body is read to its end before the answer, so the request's trailers are read too (describe). A body the
	// stub cannot read, such as one whose trailer has a name outside the token characters, is answered as net/http
	// answers a request it cannot read, so the battery reads it as the stub's own answer (result.stubError) and never
	// as a refusal by the proxy.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "400 Bad Request: the stub could not read the body: "+err.Error(), http.StatusBadRequest)
		return
	}
	status := http.StatusOK
	if code, err := strconv.Atoi(r.Header.Get("x-stub-status")); err == nil {
		status = code
	}
	if delay, err := time.ParseDuration(r.Header.Get("x-stub-delay")); err == nil && !wait(r, delay) {
		return
	}
	askHeaders(w, r)
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	if pauses, pause, ok := trickleOf(r.Header.Get("x-stub-trickle")); ok {
		// The status and headers go out first, so each pause is one inside the body.
		sent := http.NewResponseController(w)
		sent.Flush()
		for range pauses {
			if !wait(r, pause) {
				return
			}
			io.WriteString(w, " ")
			sent.Flush()
		}
	}
	e := b.describe(r, attempt)
	e.Body = string(body)
	if size, err := strconv.Atoi(r.Header.Get("x-stub-pad")); err == nil {
		e.Pad = strings.Repeat("p", size)
	}
	json.NewEncoder(w).Encode(e)
}

// trickleOf reads x-stub-trickle's value, a count of pauses, x and the time of each, such as 2x1250ms.
func trickleOf(value string) (pauses int, pause time.Duration, ok bool) {
	count, each, found := strings.Cut(value, "x")
	pauses, countErr := strconv.Atoi(count)
	pause, pauseErr := time.ParseDuration(each)
	return pauses, pause, found && countErr == nil && pauseErr == nil
}

// wait waits for d, and reports false when the request ended first, as one the proxy gave up on does.
func wait(r *http.Request, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-r.Context().Done():
		return false
	}
}

// asked holds, by the request header that asks for it, an answer header the stub sets to that header's value.
// X-Accel-Redirect: nginx answers with the URI an upstream names there, in place of the upstream's answer, unless told
// to ignore the header (proxy_ignore_headers), so a service could serve the client a location no route gives it; a
// scenario asks for one to see that the proxy passes it on instead. Keep-Alive: the time a service keeps its own
// connection open, which says nothing true of the client's connection, so a scenario asks for one to see that the
// proxy removes it, as Envoy does.
var asked = map[string]string{"x-stub-accel-redirect": "X-Accel-Redirect", "x-stub-keep-alive": "Keep-Alive"}

// askHeaders sets each answer header the request asks for (asked).
func askHeaders(w http.ResponseWriter, r *http.Request) {
	for control, header := range asked {
		if value := r.Header.Get(control); value != "" {
			w.Header().Set(header, value)
		}
	}
}

// firstTry closes the request carrying an x-stub-fail-first key the first time the key reaches any backend sharing
// the state folder, and returns the attempt any other request is: 1 without a key, 2 for a key seen before. The
// folder is shared so "the first try only" holds across the two shop backends a retry may move between, and
// O_EXCL makes exactly one request the first even when two arrive at once. ok is false when the request has
// already been answered or closed.
func (b *backend) firstTry(w http.ResponseWriter, r *http.Request) (attempt int, ok bool) {
	key := r.Header.Get("x-stub-fail-first")
	if key == "" {
		return 1, true
	}
	// The marker is named by the key's SHA-256, so every key has a file of its own inside the state folder: a key
	// such as "..", "/" or "a/once" names no path, and two keys never share a marker.
	sum := sha256.Sum256([]byte(key))
	file, err := os.OpenFile(filepath.Join(b.state, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	switch {
	case err == nil:
		file.Close()
		abort(w)
		return 0, false
	case errors.Is(err, fs.ErrExist):
		return 2, true
	default:
		// A state folder the stub cannot write to answers 500, so it fails the scenario instead of passing as a key
		// seen before.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, false
	}
}

// abort ends a request before any answer, as a backend that fails mid-request does. HTTP/1 has no stream of its
// own, so its connection is closed; on HTTP/2 http.ErrAbortHandler makes the server reset this stream alone.
func abort(w http.ResponseWriter) {
	if hijacker, ok := w.(http.Hijacker); ok {
		if conn, _, err := hijacker.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

// echo is what a backend received, the wire contract the battery parses.
type echo struct {
	Service  string `json:"service"`
	Instance string `json:"instance"`
	// Port is the local port the request arrived on, which tells the battery the proxy's choice of port.
	Port   string `json:"port"`
	Proto  string `json:"proto"`
	Method string `json:"method"`
	// Path is the request target as received, so a scenario sees the path the proxy sent, before any decoding.
	Path    string `json:"path"`
	Host    string `json:"host"`
	Attempt int    `json:"attempt"`
	// Headers holds every request header by its lower-case name, its lines joined with a line break, which no value
	// holds, so a row tells one line holding a comma from two lines: Envoy joins the lines of some headers into one,
	// and a service that reads only the first line of a header reads another value when a proxy passes both.
	Headers map[string]string `json:"headers"`
	// HeaderBytes holds the same text in hexadecimal for each header of Headers that holds a byte outside well-formed
	// UTF-8, which JSON writes as U+FFFD, so the battery reads the bytes the service received.
	HeaderBytes map[string]string `json:"headerbytes,omitempty"`
	// Body is the request body as read, on a plain HTTP request; Trailers holds the request's trailers as Headers holds
	// its headers, read once the body has ended, including those no Trailer header declared.
	Body     string            `json:"body,omitempty"`
	Trailers map[string]string `json:"trailers,omitempty"`
	// Pad is as many bytes as the request's x-stub-pad asks for, so a scenario gets an answer of that size: HAProxy
	// reads the first part of an answer into one buffer, and the lines it adds to an answer must fit beside it.
	Pad string `json:"pad,omitempty"`
}

func (b *backend) describe(r *http.Request, attempt int) echo {
	port := ""
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		_, port, _ = net.SplitHostPort(addr.String())
	}
	headers := map[string]string{}
	var headerBytes map[string]string
	for name, values := range r.Header {
		value := strings.Join(values, "\n")
		headers[strings.ToLower(name)] = value
		if !utf8.ValidString(value) {
			if headerBytes == nil {
				headerBytes = map[string]string{}
			}
			headerBytes[strings.ToLower(name)] = hex.EncodeToString([]byte(value))
		}
	}
	var trailers map[string]string
	for name, values := range r.Trailer {
		if trailers == nil {
			trailers = map[string]string{}
		}
		trailers[strings.ToLower(name)] = strings.Join(values, "\n")
	}
	return echo{Service: b.service, Instance: b.instance, Port: port, Proto: r.Proto, Method: r.Method,
		Path: r.RequestURI, Host: r.Host, Attempt: attempt, Headers: headers, HeaderBytes: headerBytes, Trailers: trailers}
}
