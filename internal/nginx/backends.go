package nginx

import (
	"iter"
	"net"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// writeUpstream writes the upstream name of one port of a service: the Envoy configuration's cluster, or a group of one
// of its backends (writeGroups). Each worker keeps up to 32 idle connections open for idle, timeouts.upstream_idle or
// Envoy's hour (timersOf); 32 is a number chosen, not measured under load. keepalive follows the balance line, as nginx
// needs any balance but round robin written before it (the upstream keepalive page).
func writeUpstream(w *writer, v *values, name string, service model.Service, port model.ServicePort, idle string, e edge) {
	w.lines(upstreamLines(v, name, service, port, e)...)
	w.lines(
		"    keepalive 32;",
		"    keepalive_timeout "+idle+";",
		"  }",
	)
}

// upstreamLines are the lines of the upstream name that carries port of service to its backends, up to its servers:
// the service's own upstreams and the check relay's in the stream block. max_fails=0 keeps a backend in use after a
// failed attempt, where nginx's default takes it out for 10 seconds; Envoy keeps every backend of a service with no
// health key.
//
// A DNS name is looked up again while nginx runs only by an upstream in shared memory, its zone (the server's resolve
// parameter). Each address of the name's answer takes room in it: 1m held 1,000 addresses of a name and not 2,000, and
// 64k held 100 and not 200, where nginx logged "cannot add new server ... memory exhausted" (measured on nginx 1.30.6
// and 1.31.8), so a name holds about 1,000 addresses for each port, where the Envoy output takes every address. The
// generator never looks a name up itself.
func upstreamLines(v *values, name string, service model.Service, port model.ServicePort, e edge) []string {
	lines := []string{"  upstream " + name + " {"}
	if balance := balanceLine(service, e); balance != "" {
		lines = append(lines, "    "+balance)
	}
	resolve := ""
	if service.DNS {
		lines = append(lines, "    zone "+name+" 1m;")
		resolve = " resolve"
	}
	for _, address := range service.Addresses {
		server := net.JoinHostPort(address.Value, strconv.FormatInt(port.Number, 10))
		lines = append(lines, "    server "+v.keyToken(addressKey(&service), address.Pos, server)+resolve+" max_fails=0;")
	}
	return lines
}

// balanceLine is the line an upstream balances its backends with, "" for round_robin, nginx's own. Measured on nginx
// 1.30.6 and 1.31.8 beside Envoy 1.40.2 with three backends:
//   - least_requests: Envoy draws two backends and sends to the one with fewer active requests (LEAST_REQUEST, two
//     choices by default), nginx draws two and sends to the one with fewer active connections; of 900 requests sent
//     one and eight at a time Envoy gave each backend 296 to 307 and nginx 290 to 317.
//   - random: of 900, Envoy gave each backend 292 to 311 and nginx 284 to 321.
//   - hash.header: the key is the script's hashKey, which tells a request without the header, spread round robin as
//     Envoy spreads it at random, from one with the header empty, which Envoy hashes; $http_<name> is empty for both.
//   - hash.client: the client's address as client_address reads it, the peer under connection, and the script's
//     clientKey under forwarded, as Envoy hashes the address its connection manager takes as the client's.
//
// consistent is nginx's ring, ketama's, and Envoy's RING_HASH is its own, so each value always reaches one backend, not
// the one Envoy picks for it.
func balanceLine(service model.Service, e edge) string {
	switch service.Balance.Value {
	case "least_requests":
		return "random two least_conn;"
	case "random":
		return "random;"
	case "hash":
		switch {
		case service.Hash.Client && e.connection:
			return "hash $binary_remote_addr consistent;"
		case service.Hash.Client:
			return "hash $pf_client_key consistent;"
		}
		return "hash $pf_hash_key consistent;"
	}
	return ""
}

// resolverLine is the name server dns.resolver names, which nginx asks for every name an upstream looks up while it
// runs: it never reads the host's resolver configuration for them. valid=5s keeps each answer 5 s, whatever its time to
// live, and nginx then asks again, as the Envoy output's cluster looks its name up every 5 s (dns_refresh_rate's
// default). nginx asks for the IPv4 and the IPv6 addresses of a name both, its default, as the Envoy output's lookup
// family ALL does, so a service reached on either family gets requests on each address (measured on nginx 1.30.6 and
// 1.31.8 beside Envoy 1.40.2: an address added took requests within the 5 s, one removed took none after them, a name
// with one address of each family sent requests to both). An IPv6 address is written in brackets, as nginx reads one
// with a port.
func resolverLine(r *model.Resolver) string {
	return "resolver " + net.JoinHostPort(r.Address, strconv.FormatInt(r.Port, 10)) + " valid=5s;"
}

// hashNames is the map that gives the script's hashKey the header hash.header names for the upstream a request is
// sent to, the one its route location sets $pf_log_upstream to, each port of a service its own upstream, or "" when no
// service hashes a header.
func hashNames(v *values, services []model.Service) string {
	var keys []string
	for upstream, service := range hashedUpstreams(services) {
		keys = append(keys, upstream+" "+v.token(hashHeaderKey, service.Hash.Pos, service.Hash.Header)+";")
	}
	if keys == nil {
		return ""
	}
	return "  map $pf_log_upstream $pf_hash_name { " + strings.Join(keys, " ") + ` default ""; }`
}

// hashesHeader says whether service's balance hashes a header.
func hashesHeader(service model.Service) bool {
	return service.Hash != nil && service.Hash.Header != ""
}

// hashedUpstreams yields the keys of hashNames' map, the upstream of each port of a service whose balance hashes a
// header, each with its service, so the map and the size of its table (mapHashSizes) read one list.
func hashedUpstreams(services []model.Service) iter.Seq2[string, model.Service] {
	return func(yield func(string, model.Service) bool) {
		for _, service := range services {
			if !hashesHeader(service) {
				continue
			}
			for _, port := range service.Ports {
				if !yield(service.Cluster(port.Protocol), service) {
					return
				}
			}
		}
	}
}

// hashHeaderKey is the key a hashed header's name is refused by when nginx cannot read it.
const hashHeaderKey = "services.<service name>.hash.header"
