package nginx

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// rateOf is the rate of limit as limit_req_zone reads it, which takes a whole number of requests a second or a minute
// and no other unit (model.WholeRate): "<n>r/s" when requests in per make a whole number a second, else "<n>r/m" when
// they make one a minute, else false, a limit nginx cannot write, which its declaration refuses. The Envoy output refills its bucket
// at requests each per, and nginx refills at the rate, so both admitted and refused the same requests with per 1s, 2s
// and 1m and burst 0 and 5; nginx keeps a rate as thousandths of a request a second, rounded down, so a rate a minute
// that is not a multiple of 3 refills a little slower: 2 a minute admitted the next request from 30.4 s, where the
// Envoy output admitted it from 30.18 s (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
func rateOf(limit *model.RateLimit) (string, bool) {
	count, unit, ok := model.WholeRate(limit.Requests, limit.Per.Millis)
	if !ok {
		return "", false
	}
	return strconv.FormatInt(count, 10) + "r/" + unit, true
}

// limitZones are the http block's lines of the rate limits, nil when no route is limited: the status of a refused
// request, 429, as the Envoy output answers it, where nginx's own is 503; the kind of answer the script picks for it
// (limitedAnswers); and a zone for each limited entry of routes, named by zoneOf with the number routesOf gave its
// routes, from 1 in the written order, as generator.Buckets orders the Envoy output's buckets, so the rpc entry's
// methods share one zone, as they share the Envoy output's bucket. The zone's key is one text for
// every client with by: all, as nginx counts no request whose key is empty; with by: client it is the connection's peer
// under client_address connection and the script's client key under forwarded (clientKeyLine). Every worker charges the
// one zone in shared memory: 200 requests at once from many connections to 12 workers admitted 10 of a limit of 10, as
// the Envoy output did. A zone of 16 MiB remembers more clients than the 65,536 the Envoy output remembers before it
// forgets the oldest, so a client whose bucket is empty is forgotten later, a difference the declaration names
// (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2).
func limitZones(routes []route, e edge) []string {
	var zones []string
	written := 0
	for _, r := range routes {
		// The routes of one entry share its number, and the numbers rise in the routes' order.
		if r.bucket <= written {
			continue
		}
		written = r.bucket
		zone, key := zoneOf(r.limit, r.bucket, e)
		rate, _ := rateOf(r.limit)
		zones = append(zones, fmt.Sprintf("  limit_req_zone %s zone=%s:16m rate=%s;", key, zone, rate))
	}
	if zones == nil {
		return nil
	}
	return slices.Concat([]string{"  limit_req_status 429;", "  js_set $pf_429_kind pf.limitedKind;"}, zones)
}

// zoneOf is the name and the key of the zone of the i-th limited entry, whose limit is limit: pf_rl_<i>_all for the one
// text of by: all, pf_rl_<i>_peer for the connection's peer and pf_rl_<i>_client for the script's client key. The name
// holds the kind of its key because nginx refuses a reload that gives a zone of the same name another key, and then
// keeps the whole old configuration running: `nginx -s reload` exits 0, nginx -t passes the new file, and one [emerg]
// line in the log is the only sign. With the kind in the name, a changed by, a changed client_address or two limited
// entries of other kinds changing places name other zones, and nginx takes the reload; a changed rate keeps the name,
// and nginx keeps the zone's counts across a reload, as for a reload of the same file, so two limited entries of one
// kind that change places take each other's counts until they refill (measured on nginx 1.30.6 and 1.31.8).
func zoneOf(limit *model.RateLimit, i int, e edge) (zone, key string) {
	kind, key := "all", `"all"`
	switch {
	case !limit.ByClient:
	case e.connection:
		kind, key = "peer", "$binary_remote_addr"
	default:
		kind, key = "client", "$pf_client_key"
	}
	return "pf_rl_" + strconv.Itoa(i) + "_" + kind, key
}

// clientKeyLine is the one line that reads the client's address as the Envoy output reads it under client_address
// forwarded, the last X-Forwarded-For entry when it is an address and the peer otherwise, which nginx's realip module
// reads otherwise in five forms (a port, brackets, a leading zero, a tab, another spelling of an IPv6 address) and the
// script's clientKey reads as Envoy does in every form (measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2). It
// is written when a route limits by client or a service hashes the client, nil otherwise and under connection, whose
// key is the peer itself.
func clientKeyLine(g *model.Gateway, e edge) []string {
	if e.connection {
		return nil
	}
	limits := slices.ContainsFunc(g.Routes, func(r model.Route) bool {
		limit := generator.LimitOf(&r)
		return limit != nil && limit.ByClient
	})
	hashes := slices.ContainsFunc(g.Services, func(s model.Service) bool { return s.Hash != nil && s.Hash.Client })
	if !limits && !hashes {
		return nil
	}
	return []string{"  js_set $pf_client_key pf.clientKey;"}
}

// limitLines are the lines of each location of a limited entry's routes, which charge the zone zoneOf names for its
// bucket i. nginx admits at once one request more than its burst, so burst is requests + burst - 1, the tokens the
// Envoy output's bucket holds less one, and nodelay serves those at once, as the bucket does, where nginx would delay
// them; nginx refuses burst=0, so a bucket of one token writes none. The charge falls before the check, whatever the
// order of the lines: nginx runs limit_req in the preaccess phase and the check's js_access in the access phase, so a
// request the check denies spends a token, as in the Envoy output, and one an earlier rule refuses (a foreign origin, a
// refused path) or a preflight reaches no route location and spends none (measured on nginx 1.30.6 and 1.31.8 beside
// Envoy 1.40.2, with every plan of paced requests the same admitted and refused). A refused request goes to the answer
// of its kind (limitedAnswers).
func limitLines(limit *model.RateLimit, i int, e edge) []string {
	zone, _ := zoneOf(limit, i, e)
	charge := "      limit_req zone=" + zone
	if burst := limit.Requests + limit.Burst - 1; burst > 0 {
		charge += " burst=" + strconv.FormatInt(burst, 10)
	}
	return []string{charge + " nodelay;", "      error_page 429 = @pf_429$pf_429_kind;"}
}

// limitedAnswers are an environment server's answers to a request over a limit, whose Access-Control lines are cors
// (corsLines' server), one for each kind the script's limitedKind tells. The Envoy output answers 429 with
// local_rate_limited as text/plain, with x-request-id and Vary: Origin, over HTTP/1.1 and HTTP/2 (@pf_429), and ends
// the connection after a request whose body it did not read, where nginx kept it, so -body closes it; a Connect call
// gets the Connect error resource_exhausted as JSON (-connect, and -connect-body, which closes the connection as -body
// does); a gRPC call gets 200 with grpc-status 8 and grpc-message local_rate_limited, to which nginx adds
// content-length: 0 and its Server header, as on every gRPC answer it makes itself (grpcUnavailable), and -grpc-body
// closes the connection as -body does. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2.
func limitedAnswers(cors []string) []string {
	headers := serverHeaders(cors)
	answer := func(kind, contentType string, closed bool, lines ...string) []string {
		location := slices.Concat([]string{"    location @pf_429" + kind + " {", "      default_type " + contentType + ";"}, headers)
		if closed {
			location = append(location, "      keepalive_timeout 0;")
		}
		return slices.Concat(location, lines, []string{"    }"})
	}
	const text, connect = `      return 429 "local_rate_limited";`, `      return 429 '{"code":"resource_exhausted","message":"local_rate_limited"}';`
	grpc := []string{"      add_header grpc-status 8 always;", "      add_header grpc-message local_rate_limited always;", `      return 200 "$pf_empty";`}
	return slices.Concat(
		answer("", "text/plain", false, text),
		answer("-body", "text/plain", true, text),
		answer("-connect", "application/json", false, connect),
		answer("-connect-body", "application/json", true, connect),
		answer("-grpc", "application/grpc", false, grpc...),
		answer("-grpc-body", "application/grpc", true, grpc...),
	)
}

// retryOnce is the map that sends a request to the copy of a retried route's locations that tries once, nil when no
// route retries. The Envoy output tries again only a request that never reached the service (connect-failure and
// reset-before-request); nginx tries the next backend after an error or a timeout, and once it has sent a request it
// sends it again only when its method is not POST, LOCK or PATCH, which it sends again after the service read it. A
// gRPC call is a POST, which nginx then tries again only when no service read it, as the Envoy output does, but a call
// with another method, GET or PUT with gRPC's content type over HTTP/1.1, reached the service twice, so every method
// but POST goes to the -once copy, which reached it once. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with
// a service that reset or closed the connection, or reset the call's stream, after it read the call.
//
// A retried plain route's location reads the body whole before its first try (bodyLines), so it takes a request that
// carries no Transfer-Encoding and either has a Content-Length of at most 1 MiB, whatever its method and protocol, or
// has no body: over HTTP/1.x a request with neither header has none (RFC 9112, section 6.3), whatever its method, and
// the Envoy output tried a POST, PUT, PATCH, LOCK or PROPFIND with neither again after a refused connection; over
// HTTP/2 nginx cannot tell at the jump a request whose headers end the stream from one whose body follows with no
// content-length, as $content_length and every header read the same, so a GET, HEAD, OPTIONS or DELETE with none is
// taken, its body then bounded by 1 MiB. Every other request goes to -once, which streams its body as the Envoy output
// does and never tries it again, a Transfer-Encoding in any letter case among them: nginx's buffer bounds the bytes it
// reads, chunk lines included, so a GET whose 300,000 bytes came in one-byte chunks passed the buffer and nginx wrote
// its body to a temporary file. nginx answers a coding other than chunked 501, and chunked beside a Content-Length, on
// two lines or over HTTP/2 400, before the map is read. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2. The
// map reads the route's jumps
// ($pf_rt_jumps, retryMaps), which only a retried plain route's locations have, the length after the method, then the
// protocol, and last the Transfer-Encoding, a client's text, which only an empty value matches; a call's key ends the
// method with :.
func retryOnce(routes []route) []string {
	var calls, plain []string
	for i, r := range routes {
		switch {
		case r.retries > 0:
			calls = append(calls, strconv.Itoa(i+1))
		case r.jumps >= 0:
			plain = append(plain, strconv.Itoa(i+1))
		}
	}
	if calls == nil && plain == nil {
		return nil
	}
	source, call, end := `"$pf_route:$request_method"`, ":", `\z`
	if plain != nil {
		source, call, end = `"$pf_route:$pf_rt_jumps:$request_method:$content_length:$server_protocol:$http_transfer_encoding"`, "::", `:`
	}
	var keys []string
	// The calls' routes are named groupSize to a key, so a key stays far under the 4,093 bytes nginx reads before a
	// space whatever the count of retried methods: one key of 1,100 routes took 4,419.
	for piece := range slices.Chunk(calls, groupSize) {
		keys = append(keys, regexKey(`^(?:`+strings.Join(piece, "|")+`)(?:-uri)?`+call+`(?!POST`+end+`)`)+" -once;")
	}
	if plain != nil {
		location := fmt.Sprintf(`^[^:]*:[0-%d]:`, maxJumps)
		keys = append(keys, regexKey(location+`[^:]+:`+mebibyteOrLess+`:[^:]*:\z`)+` "";`, regexKey(location+`[^:]+::HTTP/1\.[01]:\z`)+` "";`,
			regexKey(location+`(?:GET|HEAD|OPTIONS|DELETE)::HTTP/2\.0:\z`)+` "";`, regexKey(location)+" -once;")
	}
	return []string{"  map " + source + " $pf_rt_once { " + strings.Join(keys, " ") + ` default ""; }`}
}

// retryLines are the lines of a retried RPC route's locations, which try each call on the next backend after a
// connection the service refused or did not open in time, retries times at most, as the Envoy output does: nginx
// counts the first try among its tries. nginx tries again at once and with no limit on the retries at the same time,
// where the Envoy output waits 25 ms and retries at most 3 at the same time, and it tries no backend twice, where the
// Envoy output tries a service of one backend on it again: both differences the declaration names (measured on nginx
// 1.30.6 and 1.31.8 beside Envoy 1.40.2).
func retryLines(retries int64) []string {
	return []string{"      grpc_next_upstream error timeout;", "      grpc_next_upstream_tries " + strconv.FormatInt(retries+1, 10) + ";"}
}

// serverHeaders are the add_header lines a named location of an environment server writes when it writes one of its
// own, whose server's Access-Control lines are cors (corsLines' server): such a location inherits none of the
// server's (the headers module page), so it repeats them.
func serverHeaders(cors []string) []string {
	headers := slices.Clone(answerHeaders)
	for _, line := range cors {
		if strings.HasPrefix(line, "    add_header ") {
			headers = append(headers, "  "+line)
		}
	}
	return headers
}
