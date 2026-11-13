package conformance

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// echo is the stub's echo, read from the body or from field 1 of a gRPC message. encoding/json matches the stub's
// lower-case field names to these without tags.
type echo struct {
	Service, Instance, Port, Proto, Method, Path, Host string
	Attempt                                            int
	Headers                                            map[string]string
}

// result is what came back for one request.
type result struct {
	status          int
	err             error // set when no answer came (a TLS refusal, a closed connection)
	header, trailer http.Header
	echo            *echo // nil when no stub wrote the answer
	elapsed         time.Duration
}

// newClients makes the battery's clients: HTTP/1.1 and HTTP/2 with the client certificate the clients CA signed,
// HTTP/1.1 without one, and HTTP/1.1 with the certificate another CA signed. Each trusts the server certificate alone
// and asks for localhost, the name it carries, whatever host a request names, so a request's Host header can name any
// environment's host.
func newClients(t testing.TB, files tlsFiles) (http1, http2, http1NoCertificate, http1OtherCA *http.Client) {
	server, err := os.ReadFile(filepath.Join(files.tls, "certificate.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(server) {
		t.Fatalf("%s holds no certificate", filepath.Join(files.tls, "certificate.pem"))
	}
	client := func(h2 bool, certificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)) *http.Client {
		var protocols http.Protocols
		protocols.SetHTTP1(!h2)
		protocols.SetHTTP2(h2)
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", GetClientCertificate: certificate},
			Protocols:       &protocols,
		}
		return &http.Client{Transport: transport, Timeout: 20 * time.Second}
	}
	// Each certificate is presented by a callback, which crypto/tls never second-guesses. With Certificates instead,
	// crypto/tls sends no certificate whose issuer is missing from the CAs the server names, so the one another CA
	// signed would not be sent, and its row would repeat the no-certificate row.
	present := func(certificate tls.Certificate) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &certificate, nil }
	}
	return client(false, present(files.client)), client(true, present(files.client)), client(false, nil),
		client(false, present(files.otherCA))
}

// send sends r to the proxy's listener and reads the whole answer, trailers included.
func (w *world) send(r request) result {
	client := w.client(r)
	var body io.Reader
	if r.proto == "grpc" {
		// The empty message: no compression flag and a length of 0.
		body = bytes.NewReader([]byte{0, 0, 0, 0, 0})
	}
	req, err := http.NewRequest(r.method, "https://"+w.listener, body)
	if err != nil {
		// A method net/http refuses is a mistake in the scenario data, as an unknown protocol is (client).
		panic(fmt.Sprintf("the battery cannot send %q: %v", r.method, err))
	}
	// Opaque sends the path exactly as written, so a scenario can send a path a proxy must normalise.
	req.URL.Opaque = r.path
	req.Host = r.host
	if r.proto == "grpc" {
		req.Header.Set("content-type", "application/grpc")
		req.Header.Set("te", "trailers")
	}
	for name, value := range r.headers {
		req.Header.Set(name, value)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{err: err, elapsed: time.Since(start)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	got := result{status: resp.StatusCode, header: resp.Header, trailer: resp.Trailer, elapsed: time.Since(start)}
	if err != nil {
		// An answer cut off before its end is no answer a row can judge.
		got.err = fmt.Errorf("reading the answer: %w", err)
		return got
	}
	got.echo = readEcho(resp.Header.Get("content-type"), data)
	return got
}

// client returns the client for r's protocol. A request the battery has no client for is a mistake in the scenario
// data, and an error would pass a row that wants a refusal, so it panics instead.
func (w *world) client(r request) *http.Client {
	switch {
	case r.noCertificate && r.otherCA:
		panic("a request has no client certificate or another CA's, not both")
	case r.noCertificate && r.proto == "http/1.1":
		return w.http1NoCertificate
	case r.otherCA && r.proto == "http/1.1":
		return w.http1OtherCA
	case r.noCertificate || r.otherCA:
		panic(fmt.Sprintf("the battery sends without a client certificate, or with another CA's, over http/1.1 only, not %q", r.proto))
	case r.proto == "http/1.1":
		return w.http1
	case r.proto == "h2" || r.proto == "grpc":
		return w.http2
	default:
		panic(fmt.Sprintf("the battery has no client for the protocol %q", r.proto))
	}
}

// readEcho returns what the stub that wrote the answer received, or nil when no stub wrote it: a stub's echo is a
// JSON object naming its service, sent as field 1 of the message when the answer is gRPC.
func readEcho(contentType string, body []byte) *echo {
	if strings.HasPrefix(contentType, "application/grpc") {
		body = field1(body)
	}
	var e echo
	if json.Unmarshal(body, &e) != nil || e.Service == "" {
		return nil
	}
	return &e
}

// field1 returns the bytes of field 1 of the one message in a gRPC body, or nil. The body is a flag byte, the
// message's length as a big-endian uint32 and the message; field 1 is the tag 0a, a varint length and the bytes.
func field1(body []byte) []byte {
	if len(body) < 5 {
		return nil
	}
	length := binary.BigEndian.Uint32(body[1:5])
	message := body[5:]
	if uint64(len(message)) < uint64(length) {
		return nil
	}
	message = message[:length]
	if len(message) < 2 || message[0] != 0x0a {
		return nil
	}
	size, n := binary.Uvarint(message[1:])
	if n <= 0 || uint64(len(message)-1-n) < size {
		return nil
	}
	return message[1+n : 1+n+int(size)]
}
