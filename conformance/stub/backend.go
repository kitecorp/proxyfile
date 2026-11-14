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

// reply answers with the echo, in the status x-stub-status names, or 200.
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
	askRedirect(w, r)
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	e := b.describe(r, attempt)
	e.Body = string(body)
	json.NewEncoder(w).Encode(e)
}

// askRedirect answers X-Accel-Redirect with the value of the request's x-stub-accel-redirect, when it has one. nginx
// answers with the URI an upstream names there, in place of the upstream's answer, unless told to ignore the header
// (proxy_ignore_headers), so a service could serve the client a location no route gives it; a scenario asks for one
// to see that the proxy passes it on instead.
func askRedirect(w http.ResponseWriter, r *http.Request) {
	if value := r.Header.Get("x-stub-accel-redirect"); value != "" {
		w.Header().Set("X-Accel-Redirect", value)
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
	// Headers holds every request header by its lower-case name, its values joined with ",".
	Headers map[string]string `json:"headers"`
	// Body is the request body as read, on a plain HTTP request; Trailers holds the request's trailers as Headers holds
	// its headers, read once the body has ended, including those no Trailer header declared.
	Body     string            `json:"body,omitempty"`
	Trailers map[string]string `json:"trailers,omitempty"`
}

func (b *backend) describe(r *http.Request, attempt int) echo {
	port := ""
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		_, port, _ = net.SplitHostPort(addr.String())
	}
	headers := map[string]string{}
	for name, values := range r.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ",")
	}
	var trailers map[string]string
	for name, values := range r.Trailer {
		if trailers == nil {
			trailers = map[string]string{}
		}
		trailers[strings.ToLower(name)] = strings.Join(values, ",")
	}
	return echo{Service: b.service, Instance: b.instance, Port: port, Proto: r.Proto, Method: r.Method,
		Path: r.RequestURI, Host: r.Host, Attempt: attempt, Headers: headers, Trailers: trailers}
}
