package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// collector plays an OpenTelemetry Collector for the battery's tracing rows: it takes the spans a proxy exports over
// OTLP/gRPC on port 4317 and answers every span it kept, as JSON, on GET /spans on port 8080, which the battery reads
// with fetch inside the container. It decodes the messages by hand, as grpc.go writes its own, so the stub stays on the
// standard library. The field numbers are those of opentelemetry-proto's trace.proto, common.proto and
// trace_service.proto at the commit go.opentelemetry.io/proto/slim/otlp v1.11.1 is built from, the version
// go.opentelemetry.io/collector/pdata names for collector 0.164.0.
func collector(args []string) {
	if len(args) > 0 {
		usageError("collector takes no arguments")
	}
	kept := &spans{}
	export := http.NewServeMux()
	export.HandleFunc("POST /opentelemetry.proto.collector.trace.v1.TraceService/Export", kept.export)
	read := http.NewServeMux()
	read.HandleFunc("GET /spans", kept.list)
	// Both exporters speak gRPC over HTTP/2 without TLS to the endpoint the battery's files name, as the stub's gRPC
	// backends are served.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	errs := make(chan error, 2)
	for _, server := range []*http.Server{{Addr: ":4317", Handler: export, Protocols: &protocols},
		{Addr: ":8080", Handler: read, Protocols: &protocols}} {
		go func() { errs <- server.ListenAndServe() }()
	}
	fmt.Fprintln(os.Stderr, "collector on 4317, its spans on 8080")
	fmt.Fprintln(os.Stderr, <-errs)
	os.Exit(1)
}

// span is one span as GET /spans answers it. Each attribute's value is its text, a string as it is, an integer in
// decimal and a bool as true or false, and types holds each attribute's type beside it, so a row tells a string "200"
// from the number 200. A value of a type with no text here (a double, an array, a list of pairs, bytes or a field this
// reader does not know) is kept with an empty text and its type, never dropped, so a row that wants the attribute
// absent still sees it. A key the span carries twice has the type twice and the text of its last value, which the
// battery fails a row on (compareSpans), as one value of a map could not show both.
type span struct {
	Trace      string            `json:"trace"`
	ID         string            `json:"id"`
	Parent     string            `json:"parent"`
	State      string            `json:"state"`
	Name       string            `json:"name"`
	Kind       uint64            `json:"kind"`
	Status     uint64            `json:"status"`
	Attributes map[string]string `json:"attributes"`
	Types      map[string]string `json:"types"`
}

// spans holds every span the collector has taken, in the order they came.
type spans struct {
	mu   sync.Mutex
	kept []span
}

// maxExport bounds the body of one export call: the battery's proxies send a few spans a call, and a body past it is
// a mistake no span of a row depends on.
const maxExport = 16 << 20

// export answers one Export call: a 5-byte frame header (byte 0 for no compression, then the message length as a
// big-endian uint32), the ExportTraceServiceRequest, and the grpc-status trailer. A compressed message is answered
// grpc-status 12, as a server that knows no compression answers it, and one it cannot read grpc-status 3, so an
// exporter that sends either shows in the proxy's log and its rows find no span.
func (s *spans) export(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExport))
	status, message := "0", ""
	var taken []span
	switch {
	case err != nil:
		status, message = "13", err.Error()
	case len(body) < 5 || uint64(len(body)-5) != uint64(binary.BigEndian.Uint32(body[1:5])):
		status, message = "3", fmt.Sprintf("a body of %d bytes is not one gRPC message", len(body))
	case body[0] != 0:
		status, message = "12", "this collector reads no compressed message"
	default:
		taken, err = readExport(body[5:])
		if err != nil {
			status, message = "3", err.Error()
		}
	}
	if status == "0" {
		s.mu.Lock()
		s.kept = append(s.kept, taken...)
		s.mu.Unlock()
	}
	w.Header().Set("content-type", "application/grpc")
	w.Header().Set("trailer", "grpc-status, grpc-message")
	w.WriteHeader(http.StatusOK)
	if status == "0" {
		// An empty ExportTraceServiceResponse: no partial_success, so every span was taken.
		w.Write([]byte{0, 0, 0, 0, 0})
	}
	w.Header().Set("grpc-status", status)
	if message != "" {
		w.Header().Set("grpc-message", message)
	}
}

// list answers every span kept so far.
func (s *spans) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	kept := append([]span{}, s.kept...)
	s.mu.Unlock()
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(kept)
}

// readExport reads the spans of an ExportTraceServiceRequest: resource_spans (1), in each scope_spans (2), in each
// spans (2).
func readExport(message []byte) ([]span, error) {
	var taken []span
	err := eachField(message, func(number uint64, wire int, value []byte, _ uint64) error {
		if number != 1 || wire != wireBytes {
			return nil
		}
		return eachField(value, func(number uint64, wire int, value []byte, _ uint64) error {
			if number != 2 || wire != wireBytes {
				return nil
			}
			return eachField(value, func(number uint64, wire int, value []byte, _ uint64) error {
				if number != 2 || wire != wireBytes {
					return nil
				}
				one, err := readSpan(value)
				taken = append(taken, one)
				return err
			})
		})
	})
	return taken, err
}

// readSpan reads the fields of a Span the rows read: trace_id (1), span_id (2), trace_state (3), parent_span_id (4),
// name (5), kind (6), attributes (9), each a KeyValue, and status (15), whose code is field 3.
func readSpan(message []byte) (span, error) {
	one := span{Attributes: map[string]string{}, Types: map[string]string{}}
	err := eachField(message, func(number uint64, wire int, value []byte, varint uint64) error {
		switch {
		case number == 1 && wire == wireBytes:
			one.Trace = hex.EncodeToString(value)
		case number == 2 && wire == wireBytes:
			one.ID = hex.EncodeToString(value)
		case number == 3 && wire == wireBytes:
			one.State = string(value)
		case number == 4 && wire == wireBytes:
			one.Parent = hex.EncodeToString(value)
		case number == 5 && wire == wireBytes:
			one.Name = string(value)
		case number == 6 && wire == wireVarint:
			one.Kind = varint
		case number == 9 && wire == wireBytes:
			return readAttribute(value, one.Attributes, one.Types)
		case number == 15 && wire == wireBytes:
			return eachField(value, func(number uint64, wire int, _ []byte, varint uint64) error {
				if number == 3 && wire == wireVarint {
					one.Status = varint
				}
				return nil
			})
		}
		return nil
	})
	return one, err
}

// readAttribute reads a KeyValue, its key (1) and its AnyValue (2), into values and types.
func readAttribute(message []byte, values, types map[string]string) error {
	var key string
	var value []byte
	err := eachField(message, func(number uint64, wire int, field []byte, _ uint64) error {
		switch {
		case number == 1 && wire == wireBytes:
			key = string(field)
		case number == 2 && wire == wireBytes:
			value = field
		}
		return nil
	})
	if err != nil {
		return err
	}
	// An AnyValue with no field set is empty, which a row tells from an empty string by its type.
	text, kind := "", "empty"
	err = eachField(value, func(number uint64, wire int, field []byte, varint uint64) error {
		switch {
		case number == 1 && wire == wireBytes:
			text, kind = string(field), "string"
		case number == 2 && wire == wireVarint:
			text, kind = strconv.FormatBool(varint != 0), "bool"
		case number == 3 && wire == wireVarint:
			text, kind = strconv.FormatInt(int64(varint), 10), "int"
		case number == 4 && wire == wireFixed64:
			text, kind = strconv.FormatFloat(math.Float64frombits(binary.LittleEndian.Uint64(field)), 'g', -1, 64), "double"
		case number == 5:
			text, kind = "", "array"
		case number == 6:
			text, kind = "", "kvlist"
		case number == 7:
			text, kind = "", "bytes"
		default:
			text, kind = "", fmt.Sprintf("field %d", number)
		}
		return nil
	})
	if _, seen := types[key]; seen {
		kind = "twice"
	}
	values[key], types[key] = text, kind
	return err
}

// The wire types a field can have (protobuf's encoding): a varint, 8 bytes, a length and its bytes, or 4 bytes.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// eachField calls do with every field of message in order: its number, its wire type, and its bytes (for a length or
// a fixed width) or its varint. A field of any other wire type, or one cut short, ends the message with an error.
func eachField(message []byte, do func(number uint64, wire int, value []byte, varint uint64) error) error {
	for len(message) > 0 {
		tag, n := binary.Uvarint(message)
		if n <= 0 {
			return errors.New("a field's tag is cut short")
		}
		message = message[n:]
		number, wire := tag>>3, int(tag&7)
		var value []byte
		var varint uint64
		switch wire {
		case wireVarint:
			varint, n = binary.Uvarint(message)
			if n <= 0 {
				return fmt.Errorf("field %d's varint is cut short", number)
			}
			message = message[n:]
		case wireFixed64, wireFixed32:
			size := 8
			if wire == wireFixed32 {
				size = 4
			}
			if len(message) < size {
				return fmt.Errorf("field %d is cut short", number)
			}
			value, message = message[:size], message[size:]
		case wireBytes:
			length, n := binary.Uvarint(message)
			if n <= 0 || uint64(len(message)-n) < length {
				return fmt.Errorf("field %d's bytes are cut short", number)
			}
			value, message = message[n:n+int(length)], message[n+int(length):]
		default:
			return fmt.Errorf("field %d has wire type %d, which no field of a span's messages has", number, wire)
		}
		if err := do(number, wire, value, varint); err != nil {
			return err
		}
	}
	return nil
}
