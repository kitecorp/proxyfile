package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

const (
	typeA         = 1
	rcodeNXDomain = 3
)

// resolve answers every query that reaches conn and logs each answer, so a scenario that fails on a name shows
// what the proxy asked.
func resolve(conn net.PacketConn, records map[string]net.IP) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			// The socket is never closed, so a read error means it is broken: stop rather than spin.
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		reply, err := answer(buf[:n], records)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dns query from %s not answered: %v\n", from, err)
			continue
		}
		name, qtype, _, _ := question(reply)
		fmt.Fprintf(os.Stderr, "dns %s type %d rcode %d answers %d\n", name, qtype, reply[3]&0x0f,
			binary.BigEndian.Uint16(reply[6:]))
		if _, err := conn.WriteTo(reply, from); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
}

// answer writes the reply to one query: one A record for a known name asked for type A, no record and rcode 0 for
// any other type of a known name, and rcode 3 (NXDOMAIN) for any other name. It writes the form Envoy 1.40.2's
// c-ares resolver accepted when measured (an A query answered with one record, an AAAA query of the same name with
// none, an unknown name with NXDOMAIN): the query's id, QR and RA set, RD kept, the question copied, and the
// record's name written as a pointer to the question's name.
func answer(query []byte, records map[string]net.IP) ([]byte, error) {
	name, qtype, end, err := question(query)
	if err != nil {
		return nil, err
	}
	// The reply copies one question, so it is a true reply only to a query that holds one.
	if binary.BigEndian.Uint16(query[4:]) != 1 {
		return nil, errors.New("query does not hold exactly one question")
	}
	ip, known := records[strings.ToLower(name)]
	rcode, count := byte(0), uint16(0)
	switch {
	case !known:
		rcode = rcodeNXDomain
	case qtype == typeA:
		count = 1
	}
	reply := []byte{query[0], query[1], 0x80 | query[2]&0x01, 0x80 | rcode, 0, 1}
	reply = binary.BigEndian.AppendUint16(reply, count)
	reply = append(reply, 0, 0, 0, 0) // no authority and no additional records
	reply = append(reply, query[12:end]...)
	if count == 1 {
		// The name as a pointer to offset 12, type A, class IN, TTL 5 seconds, then the 4 bytes of the address.
		reply = append(reply, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 5, 0, 4)
		reply = append(reply, ip...)
	}
	return reply, nil
}

// question reads the question that follows a DNS header: its name, its type and the offset just past its class.
func question(message []byte) (name string, qtype uint16, end int, err error) {
	// A header is 12 bytes, and the shortest question is the root name's one byte plus type and class.
	if len(message) < 17 {
		return "", 0, 0, errors.New("message shorter than a header and a question")
	}
	var labels []string
	i := 12
	for message[i] != 0 {
		length := int(message[i])
		// A query's first name has nothing earlier to point to, so a compression pointer here is malformed.
		if length > 63 || i+1+length >= len(message) {
			return "", 0, 0, errors.New("malformed name in the question")
		}
		labels = append(labels, string(message[i+1:i+1+length]))
		i += 1 + length
	}
	if i+5 > len(message) {
		return "", 0, 0, errors.New("question runs past the message")
	}
	return strings.Join(labels, "."), binary.BigEndian.Uint16(message[i+1:]), i + 5, nil
}
