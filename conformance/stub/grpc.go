package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
)

// grpc answers a unary gRPC call written by hand, which keeps the program on the standard library: a 5-byte frame
// header (byte 0 for no compression, then the message length as a big-endian uint32), the message, and the
// grpc-status trailer.
func (b *backend) grpc(w http.ResponseWriter, r *http.Request) {
	// The answer depends only on the path and the headers, but the call is read to its end before the answer
	// begins, as a gRPC server reads it.
	io.Copy(io.Discard, r.Body)
	attempt, ok := b.firstTry(w, r)
	if !ok {
		return
	}
	var message []byte
	if r.URL.Path == "/grpc.health.v1.Health/Check" {
		message = []byte{0x08, 0x01} // HealthCheckResponse: field 1, status, is 1, SERVING.
	} else {
		body, _ := json.Marshal(b.describe(r, attempt))
		// Field 1 as a string: the tag 0x0a, the length as a varint, then the echo's JSON.
		message = append([]byte{0x0a}, binary.AppendUvarint(nil, uint64(len(body)))...)
		message = append(message, body...)
	}
	askRedirect(w, r)
	w.Header().Set("content-type", "application/grpc")
	w.Header().Set("trailer", "grpc-status")
	w.WriteHeader(http.StatusOK)
	w.Write(append(binary.BigEndian.AppendUint32([]byte{0}, uint32(len(message))), message...))
	w.Header().Set("grpc-status", "0")
}
