package conformance

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// sendRaw sends r by hand, for what net/http will not send: a header name outside RFC 9110's token characters, which
// both its clients refuse, a request line with other than one space between its parts, a header value that holds a
// line break, and a trailer of any name. Over http/1.1 it writes the request line, r.line when set and otherwise the
// method, the path and HTTP/1.1 with one space between them, then Host and each header in the order of their names,
// then, with r.body or r.trailers, Transfer-Encoding: chunked and the body as one chunk ended by the trailers, and
// reads the answer with net/http's reader. Over h2 it writes the connection preface, an empty SETTINGS frame and one
// HEADERS frame of the same fields, each a literal HPACK field with no index and no Huffman coding, then the body as a
// DATA frame and the trailers as a second HEADERS frame, and reads frames until the stream ends. A closed or reset connection, or a
// GOAWAY, before the answer is an error, as a client sees a proxy that refused the request so. The connection is the
// clients' own (TLS with the client certificate, or plain), opened for this request alone.
func (w *world) sendRaw(r request) result {
	start := time.Now()
	fail := func(err error) result { return result{err: err, elapsed: time.Since(start)} }
	conn, err := w.dialRaw(r.proto)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return fail(err)
	}
	names := slices.Sorted(maps.Keys(r.headers))
	if r.proto == "h2" {
		got := w.sendFrames(conn, r, names)
		got.elapsed = time.Since(start)
		return got
	}
	line := r.line
	if line == "" {
		line = r.method + " " + r.path + " HTTP/1.1"
	}
	var text strings.Builder
	text.WriteString(line + "\r\nHost: " + r.host + "\r\n")
	for _, name := range names {
		text.WriteString(name + ": " + r.headers[name] + "\r\n")
	}
	if r.body == "" && r.trailers == nil {
		text.WriteString("\r\n")
	} else {
		text.WriteString("Transfer-Encoding: chunked\r\n\r\n")
		if r.body != "" {
			fmt.Fprintf(&text, "%x\r\n%s\r\n", len(r.body), r.body)
		}
		text.WriteString("0\r\n")
		for _, name := range slices.Sorted(maps.Keys(r.trailers)) {
			text.WriteString(name + ": " + r.trailers[name] + "\r\n")
		}
		text.WriteString("\r\n")
	}
	if _, err := io.WriteString(conn, text.String()); err != nil {
		return fail(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	got := result{status: resp.StatusCode, header: resp.Header, trailer: resp.Trailer, elapsed: time.Since(start)}
	if err != nil {
		got.err = fmt.Errorf("reading the answer: %w", err)
		return got
	}
	got.echo = readEcho(resp.Header.Get("content-type"), data)
	got.stubError = resp.Header.Get("content-type") == "text/plain; charset=utf-8" &&
		strings.HasPrefix(string(data), fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	return got
}

// dialRaw opens a connection to the proxy's listener as the clients do: plain, or TLS with the client certificate the
// clients CA signed and ALPN asking for h2 or http/1.1, as proto says. A raw request is a mistake in the scenario data
// over any other protocol, as a request the clients cannot send is (client), so it panics.
func (w *world) dialRaw(proto string) (net.Conn, error) {
	if proto != "http/1.1" && proto != "h2" {
		panic(fmt.Sprintf("the battery sends a raw request over http/1.1 or h2, not %q", proto))
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	if w.plain {
		return dialer.Dial("tcp", w.listener)
	}
	config := w.rawTLS.Clone()
	// The protocol names the battery uses are their ALPN names.
	config.NextProtos = []string{proto}
	conn, err := tls.DialWithDialer(dialer, "tcp", w.listener, config)
	if err != nil {
		return nil, err
	}
	if got := conn.ConnectionState().NegotiatedProtocol; got != config.NextProtos[0] {
		conn.Close()
		return nil, fmt.Errorf("the listener chose ALPN %q, not %q", got, config.NextProtos[0])
	}
	return conn, nil
}

// HTTP/2's frame types and flags this sender reads or writes (RFC 9113, section 6).
const (
	frameData, frameHeaders, frameRSTStream, frameSettings, frameGoAway, frameContinuation = 0x0, 0x1, 0x3, 0x4, 0x7, 0x9
	flagEndStream, flagAck, flagEndHeaders, flagPadded, flagPriority                       = 0x1, 0x1, 0x4, 0x8, 0x20
)

// sendFrames sends r over h2 on conn, its headers in the order of names, and reads the answer of stream 1.
func (w *world) sendFrames(conn net.Conn, r request, names []string) result {
	scheme := "https"
	if w.plain {
		scheme = "http"
	}
	var block []byte
	// field appends a literal field with no index and a new name (RFC 7541, section 6.2.2) to block.
	field := func(name, value string) {
		// 0x00, then each string with its length in a 7-bit prefix and no Huffman coding.
		block = append(block, 0)
		for _, s := range []string{name, value} {
			block = appendHPACKInt(block, 7, 0, len(s))
			block = append(block, s...)
		}
	}
	field(":method", r.method)
	field(":scheme", scheme)
	field(":authority", r.host)
	field(":path", r.path)
	for _, name := range names {
		field(name, r.headers[name])
	}
	write := func(kind, flags byte, stream uint32, payload []byte) error {
		head := make([]byte, 9, 9+len(payload))
		head[0], head[1], head[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
		head[3], head[4] = kind, flags
		binary.BigEndian.PutUint32(head[5:], stream)
		_, err := conn.Write(append(head, payload...))
		return err
	}
	if _, err := io.WriteString(conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"); err != nil {
		return result{err: err}
	}
	if err := write(frameSettings, 0, 0, nil); err != nil {
		return result{err: err}
	}
	// The stream ends on the last frame the request has: its HEADERS, its body's DATA, or its trailers' HEADERS.
	endHeaders, endData := byte(flagEndStream), byte(flagEndStream)
	if r.body != "" || r.trailers != nil {
		endHeaders = 0
	}
	if r.trailers != nil {
		endData = 0
	}
	if err := write(frameHeaders, endHeaders|flagEndHeaders, 1, block); err != nil {
		return result{err: err}
	}
	if r.body != "" {
		if err := write(frameData, endData, 1, []byte(r.body)); err != nil {
			return result{err: err}
		}
	}
	if r.trailers != nil {
		block = nil
		for _, name := range slices.Sorted(maps.Keys(r.trailers)) {
			field(name, r.trailers[name])
		}
		if err := write(frameHeaders, flagEndStream|flagEndHeaders, 1, block); err != nil {
			return result{err: err}
		}
	}
	var got result
	var headers, body []byte
	reader := bufio.NewReader(conn)
	for {
		head := make([]byte, 9)
		if _, err := io.ReadFull(reader, head); err != nil {
			return result{err: fmt.Errorf("the connection ended before the answer did: %w", err)}
		}
		payload := make([]byte, int(head[0])<<16|int(head[1])<<8|int(head[2]))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return result{err: fmt.Errorf("the connection ended inside a frame: %w", err)}
		}
		kind, flags, stream := head[3], head[4], binary.BigEndian.Uint32(head[5:])&0x7fffffff
		switch {
		case kind == frameSettings && flags&flagAck == 0:
			if err := write(frameSettings, flagAck, 0, nil); err != nil {
				return result{err: err}
			}
		case kind == frameGoAway:
			return result{err: errors.New("the proxy sent GOAWAY")}
		case kind == frameRSTStream && stream == 1:
			return result{err: errors.New("the proxy reset the stream")}
		case (kind == frameHeaders || kind == frameContinuation || kind == frameData) && stream == 1:
			content := payload
			if kind != frameContinuation && flags&flagPadded != 0 && len(content) > 0 {
				pad := int(content[0])
				content = content[1:max(1, len(content)-pad)]
			}
			if kind == frameHeaders && flags&flagPriority != 0 && len(content) >= 5 {
				content = content[5:]
			}
			if kind == frameData {
				body = append(body, content...)
			} else {
				headers = append(headers, content...)
				if flags&flagEndHeaders != 0 && got.status == 0 {
					got.status = statusOf(headers)
				}
			}
			if kind != frameContinuation && flags&flagEndStream != 0 {
				got.echo = readEcho("", body)
				// The stub's own answer to a request its server could not read (result.stubError) is told by its body
				// alone: this reader reads no content-type, which a proxy codes with Huffman, and neither proxy's own
				// answer starts with the status code and its text.
				got.stubError = got.status != 0 &&
					strings.HasPrefix(string(body), fmt.Sprintf("%d %s", got.status, http.StatusText(got.status)))
				return got
			}
		}
	}
}

// appendHPACKInt appends n as an HPACK integer with a prefix of bits bits, after the high bits first (RFC 7541,
// section 5.1).
func appendHPACKInt(b []byte, bits uint, first byte, n int) []byte {
	limit := 1<<bits - 1
	if n < limit {
		return append(b, first|byte(n))
	}
	b = append(b, first|byte(limit))
	for n -= limit; n >= 128; n >>= 7 {
		b = append(b, byte(n%128)|0x80)
	}
	return append(b, byte(n))
}

// readHPACKInt reads an HPACK integer with a prefix of bits bits from the start of b, and returns it and the bytes it
// took, or 0 bytes when b ends inside it.
func readHPACKInt(b []byte, bits uint) (int, int) {
	if len(b) == 0 {
		return 0, 0
	}
	limit := 1<<bits - 1
	n := int(b[0]) & limit
	if n < limit {
		return n, 1
	}
	for i, shift := 1, 0; i < len(b) && shift < 28; i, shift = i+1, shift+7 {
		n += int(b[i]&0x7f) << shift
		if b[i]&0x80 == 0 {
			return n, i + 1
		}
	}
	return 0, 0
}

// staticStatus is each :status HPACK's static table holds, by its index (RFC 7541, appendix A).
var staticStatus = map[int]int{8: 200, 9: 204, 10: 206, 11: 304, 12: 400, 13: 404, 14: 500}

// statusOf returns the :status a response's header block names, or 0 when it names none this reader can read: an entry
// of the static table, or a literal whose value has no Huffman coding. A status it cannot read gives 0, which no row
// takes for a refusal or an answer.
func statusOf(block []byte) int {
	for len(block) > 0 {
		b := block[0]
		switch {
		case b&0x80 != 0: // an indexed field
			index, n := readHPACKInt(block, 7)
			if n == 0 {
				return 0
			}
			if status, ok := staticStatus[index]; ok {
				return status
			}
			block = block[n:]
		case b&0xe0 == 0x20: // a change of the dynamic table's size
			_, n := readHPACKInt(block, 5)
			if n == 0 {
				return 0
			}
			block = block[n:]
		default: // a literal field, indexed (01), not indexed (0000) or never indexed (0001)
			bits := uint(4)
			if b&0xc0 == 0x40 {
				bits = 6
			}
			index, n := readHPACKInt(block, bits)
			if n == 0 {
				return 0
			}
			block = block[n:]
			name, plain := "", true
			if index == 0 {
				var ok bool
				if name, plain, block, ok = readHPACKString(block); !ok {
					return 0
				}
			} else if _, ok := staticStatus[index]; ok {
				name = ":status"
			}
			value, valuePlain, rest, ok := readHPACKString(block)
			if !ok {
				return 0
			}
			block = rest
			if name == ":status" && plain && valuePlain {
				status, err := strconv.Atoi(value)
				if err != nil {
					return 0
				}
				return status
			}
		}
	}
	return 0
}

// readHPACKString reads an HPACK string from the start of b: its text, whether it has no Huffman coding (the text is
// the coded bytes otherwise), the rest of b, and false when b ends inside it.
func readHPACKString(b []byte) (string, bool, []byte, bool) {
	if len(b) == 0 {
		return "", false, nil, false
	}
	plain := b[0]&0x80 == 0
	length, n := readHPACKInt(b, 7)
	if n == 0 || n+length > len(b) {
		return "", false, nil, false
	}
	return string(b[n : n+length]), plain, b[n+length:], true
}
