package nginx

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// A plain route's retries, routes[].retries of 1 or more: the Envoy output tries a request again on another backend
// when no service read it, its connection refused, reset before the request or not opened in time, retries times at
// most within the route's time. nginx's own retry stays off (the http block's proxy_next_upstream off), as it sends a
// request again after a service read it unless its method is POST, LOCK or PATCH. A retried route's location instead
// reads the request's body whole before the first try and sends nginx's 502 and 504 to the target $pf_rt_target names
// from the tries so far: a copy of the location that passes the request to the group of the next backend in the
// service's order, when the last try sent the service nothing and ended 502 or 504, a jump and a group are left and
// the body fits; the timer's answer once the route's time is spent; and @pf_fail, which answers as the route location
// would have, otherwise. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2 with backends that refused, did not
// answer ARP or did not take the connection in time, one to eight of them: each request served or answered as the
// Envoy output, at its time, and none read twice.

// maxJumps is the most times a request goes on to a next backend: nginx follows 10 internal redirects of a request,
// and the chain of a request that fails every try takes the 418 to the route's location, each jump, @pf_fail, @pf_504
// and @pf_502 (measured on nginx 1.30.6 and 1.31.8: 7 jumps answered 500 after 11 redirects).
const maxJumps = 6

// retryJumps is how many times a request of a plain route retried retries times goes on to a next backend of service:
// once for each backend it has not tried, at most maxJumps, where the Envoy output may try one backend again. A service
// found by DNS name has no count of backends, so its route jumps up to maxJumps times, to the service's own upstream,
// which picks an address of the name's answer. -1 for a route not retried.
func retryJumps(service *model.Service, retries int64) int {
	if retries == 0 {
		return -1
	}
	jumps := maxJumps
	if !service.DNS {
		jumps = min(jumps, len(service.Addresses)-1)
	}
	return int(min(int64(jumps), retries))
}

// retried says whether a plain route of routes is retried.
func retried(routes []route) bool {
	return slices.ContainsFunc(routes, func(r route) bool { return r.jumps >= 0 })
}

// jumpedTo says whether a request of a route of routes goes on to a next backend of service.
func jumpedTo(service *model.Service, routes []route) bool {
	return slices.ContainsFunc(routes, func(r route) bool { return r.jumps > 0 && r.service == service })
}

// retryCopy is a copy of a retried route's location, which takes a try after the first: the suffix of its name and its
// timer lines.
type retryCopy struct {
	suffix string
	timers []string
}

// retryCopies are the copies of a retried route's location: -rt for a try with the route's whole time left, with the
// route location's timers (retryTimers), and -rt<k> for one after k seconds of tries, whose read and send times are
// the time left, and whose connect time is too where that is shorter. The Envoy output counts its route's time across
// every try, from the complete request (measured on Envoy 1.40.2: 504 at 2, 10 and 12 s with timeouts.idle, at 15 s
// without, whatever the tries), where nginx counts its read time for each try. The script's retrySpent picks k from
// the tries' own times, to the nearest second, so a request whose tries fail, or whose service answers or stays silent,
// ends within half a second of Envoy's time; one whose service keeps reading its body slowly runs past it, as the less
// strict list says under timeouts.request. A try that sent nothing ended within the connect time, so k is at most the
// route's jumps times that time, and under the route's time. A route that jumps to no backend has none.
func retryCopies(r route, tm timers) []retryCopy {
	if r.jumps <= 0 {
		return nil
	}
	copies := []retryCopy{{"-rt", retryTimers(tm)}}
	for k := int64(1); k <= copyCount(r.jumps, tm); k++ {
		left := tm.time - k*1000
		timers := []string{"      proxy_read_timeout " + nginxTime(left) + ";", "      proxy_send_timeout " + nginxTime(left) + ";"}
		if left < tm.connectMillis {
			timers = append(timers, "      proxy_connect_timeout "+nginxTime(left)+";")
		}
		copies = append(copies, retryCopy{"-rt" + strconv.FormatInt(k, 10), timers})
	}
	return copies
}

// retryTimers are the timer lines of a retried route's location, whose read time is the http block's: a send time
// equal to it. The location sends a body it has read whole, as the Envoy output does once the request is complete, so
// a service that takes the connection and does not read it is given the route's time, where the http block's send
// time is the idle time, 5 minutes with no timeouts. Measured on nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2, three
// runs: a PUT of 1 MiB to a service that never read it, or read one byte every 20 s, was answered 504 at 15.0 s by all
// three, where a location without the line answered it at 300.1 s. The line counts between two writes, so a service
// that takes a write within it each time is sent the whole body however slowly it reads: a PUT of 1 MiB to a service
// that read 65,536 bytes every 1.5 s was answered 504 at 27.0 to 27.1 s by both nginx versions and at 15.0 s by Envoy,
// a difference no setting closes, which the less strict list names. A location that streams the body keeps the http
// block's send time: there the line answered 504 at 15 s requests the Envoy output answers 408 at 300 s, the
// difference the less strict list names under timeouts.request.
func retryTimers(tm timers) []string {
	return []string{"      proxy_send_timeout " + tm.read + ";"}
}

// copyCount is how many copies -rt<k> a route of jumps has, 0 where no timer ends a request.
func copyCount(jumps int, tm timers) int64 {
	if tm.time == 0 {
		return 0
	}
	return min(seconds(tm.time)-1, seconds(int64(jumps)*tm.connectMillis))
}

// seconds is millis in whole seconds, rounded up.
func seconds(millis int64) int64 {
	return (millis + 999) / 1000
}

// bodyLines read a retried request's body whole before its first try, so each try sends the same body, and keep it in
// memory: a body of at most 1 MiB, which a Content-Length at the server's jump bounds (retryOnce), in a buffer one
// kilobyte larger, as with a buffer of 1 MiB nginx wrote an HTTP/2 body of unknown length to a temporary file and with
// this one it wrote none; a longer such body is answered 413 before any try (measured on nginx 1.30.6 and 1.31.8: 100
// bodies of 1 MiB at once, and 20 over HTTP/2 with no content-length). The buffer bounds the bytes nginx reads, chunk
// lines included, so a request sent in chunks never reaches these lines (retryOnce).
var bodyLines = []string{"      client_max_body_size 1m;", "      client_body_buffer_size 1025k;", "      proxy_request_buffering on;"}

// mebibyteOrLess is a length of at most 1,048,576 bytes as $content_length holds it: the Content-Length as the client
// wrote it, leading zeros and all, or the length nginx read.
const mebibyteOrLess = `0*(?:[0-9]{1,6}|10[0-3][0-9]{4}|104[0-7][0-9]{3}|1048[0-4][0-9]{2}|10485[0-6][0-9]|104857[0-6])`

// jumpEntry is a location of a retried plain route, as $pf_route names it, and its route's jumps.
type jumpEntry struct {
	location string
	jumps    int
}

// jumpEntries are the keys of $pf_rt_jumps: the locations of each retried plain route, the route location and, for a
// route with no rewrite, its -uri twin. nginx finds a key it compares whole in a hash, where a regular expression
// listing the routes would grow past what nginx reads from its file at about 800 routes.
func jumpEntries(routes []route) []jumpEntry {
	var entries []jumpEntry
	for i, r := range routes {
		if r.jumps < 0 {
			continue
		}
		n := strconv.Itoa(i + 1)
		entries = append(entries, jumpEntry{n, r.jumps})
		if r.rewrite == "" {
			entries = append(entries, jumpEntry{n + "-uri", r.jumps})
		}
	}
	return entries
}

// jumpKeys are the keys of $pf_rt_jumps.
func jumpKeys(routes []route) []string {
	var keys []string
	for _, entry := range jumpEntries(routes) {
		keys = append(keys, entry.location)
	}
	return keys
}

// groupEntry is a key of $pf_rt_group and the group it names: hashed, the text nginx hashes for a key it compares whole,
// the upstream a route sends to and a backend's address as $upstream_addr prints it, or "" for a regular expression,
// which nginx does not hash; key, the key as the file writes it.
type groupEntry struct {
	hashed, key, group string
}

// groupEntries are the keys of $pf_rt_group, in the services' order: each backend of a service a retried route jumps
// to the next backend of names the group of the backend after it, the last the first, and a service found by DNS name
// names its own upstream. A key or a group nginx cannot read is refused by the service's name, as its upstream is.
func groupEntries(v *values, g *model.Gateway, routes []route) []groupEntry {
	var entries []groupEntry
	for i := range g.Services {
		service := &g.Services[i]
		if !jumpedTo(service, routes) {
			continue
		}
		cluster := service.Cluster(model.HTTP)
		if service.DNS {
			entries = append(entries, groupEntry{key: v.keyToken(serviceKey, service.Pos, regexKey(`^`+regexp.QuoteMeta(cluster)+`\|`)),
				group: v.keyToken(serviceKey, service.Pos, cluster)})
			continue
		}
		port, _ := service.Port(model.HTTP)
		for n, address := range service.Addresses {
			hashed := cluster + "|" + net.JoinHostPort(printedAddress(address.Value), strconv.FormatInt(port.Number, 10))
			entries = append(entries, groupEntry{hashed, v.keyToken(serviceKey, service.Pos, quoted(hashed)),
				v.keyToken(serviceKey, service.Pos, groupName(cluster, (n+1)%len(service.Addresses)))})
		}
	}
	return entries
}

// groupKeys are the keys of $pf_rt_group nginx hashes.
func groupKeys(v *values, g *model.Gateway, routes []route) []string {
	var keys []string
	for _, entry := range groupEntries(v, g, routes) {
		if entry.hashed != "" {
			keys = append(keys, entry.hashed)
		}
	}
	return keys
}

// groupName is the name of the group of the backend at place n of the service whose upstream is cluster, from 0.
func groupName(cluster string, n int) string {
	return cluster + "_b" + strconv.Itoa(n+1)
}

// writeGroups writes the groups of service's port, an upstream of each backend when a retried route jumps to the next
// backend of service, written as the service's upstream writes it with no balance line, as it has one backend.
func writeGroups(w *writer, v *values, service *model.Service, routes []route, port model.ServicePort, idle string, e edge) {
	if service.DNS || !jumpedTo(service, routes) {
		return
	}
	for n := range service.Addresses {
		one := *service
		one.Addresses = service.Addresses[n : n+1]
		one.Balance = model.Text{}
		writeUpstream(w, v, v.keyToken(serviceKey, service.Pos, groupName(service.Cluster(port.Protocol), n)), one, port, idle, e)
	}
}

// printedAddress is a backend's address as nginx prints it in $upstream_addr (ngx_inet6_ntop in src/core/ngx_inet.c,
// the same in nginx 1.30.6 and 1.31.8): an IPv6 address in lower case, each group without its leading zeros and the
// first longest run of two or more zero groups as ::, as Go prints one (RFC 5952), with its last 32 bits dotted where
// its first 80 bits are zero and the next 16 are ffff, as Go prints those too, and where its first 96 bits are zero,
// unless the next 16 are zero as well and the last 16 are under 256 or end in the byte 1, such as ::1. The reader takes
// an address netip reads; nginx prints none with a zone.
func printedAddress(text string) string {
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return text
	}
	addr = addr.WithZone("")
	b := addr.As16()
	if addr.Is6() && [12]byte(b[:12]) == [12]byte{} && (b[12] != 0 || b[13] != 0 || b[14] != 0 && b[15] != 1) {
		return "::" + netip.AddrFrom4([4]byte(b[12:])).String()
	}
	return addr.String()
}

// retryMaps are the http block's maps of a file with a retried plain route, nil without one. They read the variables
// of the tries so far at each jump, so each is volatile, but $pf_rt_jumps, $pf_rt_cap, $pf_rt_copies and $pf_rt_time,
// which are the same at every jump.
//   - $pf_rt_unsent: the last try sent the service 0 bytes and ended 502 or 504, as nginx lists each try's bytes and
//     status, ", " between the tries of one upstream and " : " between the locations: a refused, unreached or
//     unopened connection, which the Envoy output tries again; never a try that sent its request.
//   - $pf_rt_last: the last try's address; $pf_rt_group: the group of the backend after it (groupEntries).
//   - $pf_rt_jumps: the route's jumps (retryJumps), by the location the route choice names (retryOnce reads it);
//     $pf_rt_cap: the same for a request the server's jump sent to the route location, none for one it sent to -once.
//   - $pf_rt_copies and $pf_rt_time: the copies a route of each count of jumps has (retryCopies) and the route's time
//     in milliseconds, which retrySpent reads; $pf_rt_spent: the copy for the next try, or s once the time is spent.
//   - $pf_rt_fits: a second guard of the body's length, at most 1 MiB, read at the jump; "" is a request with no
//     body, as nginx tries no regular expression on an empty value (measured on nginx 1.30.6 and 1.31.8).
//   - $pf_rt_target: the location the next step takes, rt_spent (the timer's answer) once the time is spent, the next
//     copy while a jump is left (the tries $upstream_addr lists against the jumps) and a group is left, fail otherwise.
//   - $pf_rt_last504: the last try ended 504, which @pf_fail answers as @pf_504 does.
func retryMaps(v *values, g *model.Gateway, routes []route, tm timers) []string {
	if !retried(routes) {
		return nil
	}
	lines := []string{
		`  map "$upstream_bytes_sent|$upstream_status" $pf_rt_unsent { volatile; ` + regexKey(`(?:^| )0\|(?:.* )?50[24]\z`) + ` 1; default 0; }`,
		`  map $upstream_addr $pf_rt_last { volatile; ` + regexKey(`(?:^| : )(?<pf_rt_a>[^ ]+)\z`) + ` $pf_rt_a; default ""; }`,
		`  map "$pf_log_upstream|$pf_rt_last" $pf_rt_group {`,
		"    volatile;",
	}
	for _, entry := range groupEntries(v, g, routes) {
		lines = append(lines, "    "+entry.key+" "+entry.group+";")
	}
	lines = append(lines, `    default "";`, "  }", "  map $pf_route $pf_rt_jumps {")
	counts := map[int]int64{}
	for _, entry := range jumpEntries(routes) {
		lines = append(lines, "    "+entry.location+" "+strconv.Itoa(entry.jumps)+";")
		if n := copyCount(entry.jumps, tm); n > 0 {
			counts[entry.jumps] = n
		}
	}
	lines = append(lines, `    default "";`, "  }", `  map $pf_rt_once $pf_rt_cap { "" $pf_rt_jumps; default ""; }`)
	source, next := "$pf_rt_unsent$pf_rt_fits|$pf_rt_cap|$upstream_addr|$pf_rt_group", "r_$pf_route-rt"
	var spent []string
	if tm.time > 0 {
		copies := "  map $pf_rt_cap $pf_rt_copies {"
		for _, jumps := range slices.Sorted(maps.Keys(counts)) {
			copies += fmt.Sprintf(" %d %d;", jumps, counts[jumps])
		}
		lines = append(lines, copies+" default 0; }",
			fmt.Sprintf(`  map "" $pf_rt_time { default %d; }`, tm.time),
			"  js_set $pf_rt_spent pf.retrySpent nocache;")
		source, next = source+"|$pf_rt_spent", next+"$pf_rt_spent"
		spent = []string{"    " + regexKey(fmt.Sprintf(`^1[01]\|[0-%d]\|[^|]+\|[^|]*\|s\z`, maxJumps)) + " rt_spent$pf_grpc_call;"}
	}
	lines = append(lines,
		`  map $content_length $pf_rt_fits { volatile; "" 1; `+regexKey(`^`+mebibyteOrLess+`\z`)+` 1; default 0; }`,
		`  map "`+source+`" $pf_rt_target {`, "    volatile;")
	lines = append(lines, spent...)
	for jumps := 1; jumps <= maxJumps; jumps++ {
		lines = append(lines, "    "+regexKey(fmt.Sprintf(`^11\|%d\|[^ |]+(?: : [^ |]+){0,%d}\|[^|]`, jumps, jumps-1))+" "+next+";")
	}
	return append(lines, "    default fail;", "  }",
		`  map $upstream_status $pf_rt_last504 { volatile; `+regexKey(`(?:^| )504\z`)+` 1; default 0; }`)
}

// retryFlags are the maps of the flags the upstream-failure line writes in a file with a retried plain route (writeLog).
// $pf_log_retry_flags reads at the log what the jumps read: a request the route location took (a jump count in
// $pf_rt_cap) whose last try sent its service nothing, with a body that fits, and that no copy took further is one the
// Envoy output's retries ran out on, flagged URX before its last try's flag, at a service of one backend too; one the
// route's time ended is flagged as the timer that ended it, UT for the route's timeout and no line for the idle timer,
// which Envoy writes none for (timers.readFlag); any other request keeps its own flag. Measured on nginx 1.30.6 and
// 1.31.8 beside Envoy 1.40.2, each line as Envoy wrote it: URX,UF when every try was refused, at one, two and eight
// backends; UT at 15 s when four or eight backends did not take the connection, a gRPC call's too; no line when the
// idle time of 10 s ended such a request; UC for a try reset after the head that followed a refused one; no line for a
// request a retry served. A request -once sent and refused is flagged UF by its one try, where the Envoy output, which
// tries such a request again, wrote URX,UF, or answered one over its bound 507 with no line.
//
// $pf_log_flags, which the line writes, takes $pf_log_retry_flags only for a request with a jump count: one of a route
// with no retries, of an RPC route (whose URX its own flag holds) or of -once takes $pf_log_failure. A map evaluates
// the value it picks alone, so nginx reads $pf_rt_target, and through it runs the script's retrySpent, at the log of a
// request a retried route location took and of no other (measured on nginx 1.30.6 and 1.31.8 with a copy of the
// script that logs each call).
func retryFlags(tm timers) []string {
	spent := ""
	if tm.time > 0 {
		flag := tm.readFlag
		if flag == "" {
			flag = `""`
		}
		spent = regexKey(`^[^|]*\|[01][01]\|rt_spent(?:-grpc)?\|`) + " " + flag + "; "
	}
	return []string{
		`  map "$pf_rt_cap|$pf_rt_unsent$pf_rt_fits|$pf_rt_target|$pf_log_failure" $pf_log_retry_flags { ` + spent +
			regexKey(fmt.Sprintf(`^[0-%d]\|11\|[^|]*\|(?<pf_lf>[^|]+)\z`, maxJumps)) + " URX,$pf_lf; default $pf_log_failure; }",
		`  map $pf_rt_cap $pf_log_flags { "" $pf_log_failure; default $pf_log_retry_flags; }`,
	}
}

// retryAnswers are the named locations of a server whose file has a retried plain route, nil without one: the timer's
// answer once the route's time is spent, in gRPC's form for a gRPC call, as the Envoy output answers both (measured on
// Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8), where a timer ends a request; and @pf_fail, which answers a try no
// next one follows as the route location would have, through @pf_502 or @pf_504 by its status. cors are the server's
// Access-Control lines.
func retryAnswers(routes []route, tm timers, cors []string) []string {
	if !retried(routes) {
		return nil
	}
	var answers []string
	if tm.time > 0 {
		answers = append(answers, "    location @pf_rt_spent {", `      return 504 "`+tm.text+`";`, "    }")
		answers = append(answers, grpcAnswer("pf_rt_spent-grpc", cors, `      add_header grpc-message "`+tm.text+`" always;`)...)
	}
	return append(answers,
		"    location @pf_fail {",
		"      error_page 502 = @pf_502$pf_grpc_call;",
		"      error_page 504 = @pf_504;",
		"      if ($pf_rt_last504) {",
		"        return 504;",
		"      }",
		"      return 502;",
		"    }",
	)
}

// mapHashSizes sizes the hash table of each map, which nginx builds from the keys it compares whole when it reads the
// map, so the lines come before the first map. keys are the keys of each map whose keys the file names: the retries'
// locations and backends, and the upstreams a balance hashes a header by; every other map has a few fixed keys of at
// most 8 bytes, which the smallest bucket holds. nginx tries each table size up to the maximum and warns when none
// gives every key room in its bucket (ngx_hash_init): 4 slots for each key of the largest map, and a bucket that holds
// four of the longest key, leave each bucket room for more keys than a slot draws.
func mapHashSizes(keys ...[]string) []string {
	longest, most := 0, 0
	for _, list := range keys {
		most = max(most, len(list))
		for _, key := range list {
			longest = max(longest, len(key))
		}
	}
	return []string{fmt.Sprintf("  map_hash_max_size %d;", max(2048, 4*most)), fmt.Sprintf("  map_hash_bucket_size %d;", hashBucket(longest, 4))}
}
