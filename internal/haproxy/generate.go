// Package haproxy writes the HAProxy configuration of a gateway, for HAProxy 3.2 and 3.4: it builds a view of the
// gateway, every value from the proxyfile in it already quoted, and executes the embedded template with it. Beside it
// goes the script the configuration loads, proxyfile.lua (script.go).
package haproxy

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/origins"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
)

//go:embed haproxy.cfg.tmpl
var source string

var configuration = template.Must(template.New("haproxy.cfg").Parse(source))

// view is what the template reads. Each string that holds a value from the proxyfile holds it quoted (quote.go), but
// a route's method, whose form the format restricts to upper-case letters. Every name the file gives an ACL, a
// variable or a backend is the generator's own, or a service's name, whose form the format restricts too.
type view struct {
	Header        string
	TLS           *tlsView // nil without tls
	Admin         int64    // 0 without admin
	Listen        int64
	ClientTimeout string   // timeout client: timeouts.idle, 300s without it
	ServerTimeout string   // timeout server: 15s without timeouts.request, 300s with none, or timeouts.idle when shorter
	MaxConn       int64    // maxconn on the listener and on each server: limits.connections, 0 without it
	MaxHeaders    int64    // tune.http.maxhdr: the header count (headerCount) and one for the request line
	FieldsRefused int64    // the HTTP/2 header lines and cookies a request is answered 400 at (haproxy.cfg.tmpl)
	BufSize       int64    // tune.bufsize: limits.request_headers.size and format.HAProxyRewriteRoom, 0 without it
	LongestID     int      // idRoom, the longest X-Request-Id a client may send under forwarded
	Removed       []string // del-header's arguments (removal), in the order generator.RemovedFromClients gives
	EnvoyRemoved  []string // generator.RemovedByEnvoy
	Methods       string   // generator.EnvoyMethods, joined by spaces
	// ConnectionTokens is generator.ConnectionTokens, and TooManyConnectionTokens the fewest tokens Envoy refuses.
	ConnectionTokens        string
	TooManyConnectionTokens int
	ClientAddress           model.ClientAddress
	Connection              bool     // client_address is connection
	Faced                   []string // generator.RemovedByEnvoyFacingClients, with connection
	Scheme                  string   // the scheme x-forwarded-proto carries: https with tls, http without
	Environments            []environment
	originCheck
	GRPC       grpcStatuses
	Routes     []route
	Rewrites   []rewrite
	Backends   []backend
	RateLimits []rateLimit // the stick table of each limited entry of the routes, in the routes' order
	Sample     *sample     // log, nil without the key: every request is logged
	// Unchecked is generator.UncheckedMark with auth.ask, enabled or not, whose client's copy is removed after the
	// others; "" without auth.ask.
	Unchecked string
	Check     *checkView // nil without auth.ask or with enabled: false
	// Resolvers is the resolvers section a service found by a DNS name is looked up through, nil when every service is
	// at IP addresses.
	Resolvers *resolversView
}

// sample is log: a request answered StatusFrom or above is logged, and one below it when HAProxy's own rand(100) draws
// below Percent.
type sample struct {
	StatusFrom int64
	Percent    int
}

// resolversView is HAProxy's own resolvers section: its Name, resolversName, and the name server dns.resolver names, its
// address with its port, or "" without the key, for the host's resolv.conf (parse-resolv-conf). Every other setting is
// HAProxy's own: a name is looked up every second (timeout resolve) and an answer kept as hold says.
type resolversView struct {
	Name, Nameserver string
}

// resolversName names the resolvers section, and templateServers the most addresses a name gives a backend: HAProxy's
// server-template writes a fixed number of servers, each taking one address of the answer, and the rest stay down.
const (
	resolversName   = "proxyfile"
	templateServers = 100
)

// checkView is the data of the auth check's lines. The action's arguments come in the order the script reads them:
// Relay, the socket of the relay to the check service, quoted; Path, Send and Accept, quoted, a list's names joined by
// commas, a prefix with its *, and - for none; then Millis, Unavailable (allow or deny) and SendRequest (1 or 0), the
// generator's own words, written bare. Bind is the relay's socket for its bind line, the generator's own text too.
// OnlyWith is auth.only_with_header, whose own line sets txn.pf_header; Ask is the condition that asks a request,
// written after if; Questions is the RPC question of each method that has
// one; Servers is the server lines of the check service (servers).
type checkView struct {
	Relay, Bind                 string
	Path, Send, Accept          string
	Millis                      int64
	Unavailable, SendRequest    string
	OnlyWith                    string // auth.only_with_header, "" without it
	Ask                         string
	Questions                   []question
	Servers                     []string
	Balance                     string      // the relay's balance argument (spread), "" for round robin
	Health                      *healthView // the check service's HTTP check, nil without health.path
	ActionHeader, ReportsHeader string      // generator.QuestionHeaders, set for the check and removed after it
}

// grpcStatuses is the grpc-status of each of the file's own refusals of a gRPC call that Envoy makes too, by the
// refusal's HTTP status (generator.GRPCStatus): Envoy answers a gRPC call it refuses itself 200 with the status mapped,
// and the pass rule answers an unanswered check under deny as Envoy does, with the mapped 403. Unknown is the 500 of a
// call the check's accepted headers take past Envoy's header count (generator.GRPCUnknown).
type grpcStatuses struct {
	BadRequest, Forbidden, NotFound, Unknown int
}

// question is one header of a method's RPC question: Header is a name of generator.QuestionHeaders and Value its value,
// written as a log-format string, set on a request of the RPC group route Route whose path is Path, quoted.
type question struct {
	Header, Value string
	Route         int
	Path          string
}

type tlsView struct {
	Certificate, Key string
	ClientsCA        string // "" without tls.clients: no client certificate is asked for
}

// environment is one environment of the origins. ID names its ACLs and its value of txn.environment by its place,
// env1 onwards: the format admits any text as an environment's name, and HAProxy takes no quotes in an ACL name, reads
// or there as a logical or, and joins every acl line of one name with or (measured on HAProxy 3.4.7), so a name
// written there could break the file or join the file's own ACLs. Comment is the name, Go-quoted, for the comment
// line that says which environment the ID is. Each list holds its values quoted, each written on a line of its own as
// an acl line of the same name, which HAProxy joins with or: a list on one line could hold more words than HAProxy
// reads. Listed says the environment lists origins or patterns.
type environment struct {
	ID       string
	Comment  string
	Hosts    []string
	Origins  []string
	Patterns []string // origins.PatternPCRE of each pattern
	Own      []string // origins.SameOriginRegex of each host: an origin on one of the environment's own hosts
	Listed   bool
}

// originCheck is the data of the browser origin lines, which every file holds: Envoy refuses another site's origin
// with 403 and adds Vary: Origin to every answer whatever the file's cors and lists (measured on Envoy 1.40.2). Listed
// says an environment lists origins or patterns. Answer is the preflight answer's headers every admitted origin gets;
// Expose is the quoted expose_headers, "" when not written. LongestOrigin is the longest admitted Origin the file
// echoes (echoRoom), and LongestEcho the most an echoed Origin and a client's own X-Request-Id take together under
// forwarded (idRoom).
type originCheck struct {
	Listed         bool
	Answer, Expose string
	LongestOrigin  int
	LongestEcho    int
}

// echoRoom is the bytes an echoed Origin and expose_headers may take together. HAProxy adds the after-response lines
// to a service's answer within the 1,024 bytes its buffer keeps for rewrites (tune.maxrewrite), and when the answer
// had filled the rest of the buffer it answered 500 after the service did the work: with the 36 bytes of the request
// id HAProxy writes, an Origin and an expose list of 872 bytes together were echoed in every try, and 882 got 500 in
// most (measured on HAProxy 3.2.26 and 3.4.7 with expose lists of 12 and 32 bytes and an answer of 100 KB, over
// HTTP/1.1 and HTTP/2, where Envoy 1.40.2 served each).
const echoRoom = 872

// ownID is the length of the request id HAProxy writes (uuid(4)), with which echoRoom was measured, and idRoom the
// longest X-Request-Id a client may send under forwarded, where it is kept and echoed on every answer in the same
// 1,024 bytes: with an answer of 100 KB, an id of 984 bytes and no echoed Origin was served in every try and 988 got
// 500 in most, and an id, an echoed Origin and expose_headers of 912 bytes together were served and 918 got 500 in
// most (measured on HAProxy 3.2.26 and 3.4.7, over HTTP/1.1 and HTTP/2, where Envoy 1.40.2 served each). An echoed
// Origin and the id share echoRoom and ownID's bytes, so the bound of each holds with the other at its longest.
const (
	ownID  = 36
	idRoom = 980
)

// route is one route the file picks: ID is its number in the generated order, from 1, and Matches the conditions of
// its rules, one a line: the rpc entry's paths to one service, or the route's one condition. Program is the quoted
// program of a route's pattern, which the script runs before the condition reads its answer, "" for a route with
// none; Pattern is the pattern, Go-quoted, for the comment line that says which pattern the program is. authOff says
// the route is written auth: off, and methods holds the paths of an rpc entry's route, as the methods give them.
type route struct {
	ID      int
	Matches []string
	Backend string
	Program string
	Pattern string
	authOff bool
	methods []string
	retries int64 // routes[].retries
	// limit is routes[].rate_limit, or the rpc entry's, which each of its routes holds, so one table counts them all;
	// nil without the key.
	limit *model.RateLimit
}

// rateLimit is HAProxy's own count of a limited entry's requests: the stick table backend Table, one entry per client
// address (type ipv6, to which HAProxy casts an IPv4 key) or one for every client (Type), counts the requests of each
// of Routes over Period (http_req_rate), and a request past Requests is denied with HAProxy's own 429. No expire is
// written: a full table forgets its oldest entries (the manual, stick-table). Track holds the track-sc0 lines of each
// route, in order.
type rateLimit struct {
	Table, Type, Period string
	Requests            int64
	Routes              []int
	Track               []tracking
}

// tracking is one track-sc0 line: Key is the table's key for the request, and Condition what the line asks beside the
// route, "" for nothing.
type tracking struct {
	Key, Condition string
}

// lastForwarded is the last X-Forwarded-For address, the client's under forwarded, which the hash of a client and a
// rate limit read.
const lastForwarded = "req.hdr_ip(x-forwarded-for,-1)"

// rateLimits returns the table of each limited entry of the routes. The routes of the rpc entry come one after another
// and hold the entry's one limit, so a route whose limit is the one before it shares that table.
func rateLimits(routes []route, connection bool) []rateLimit {
	var list []rateLimit
	var last *model.RateLimit
	for _, r := range routes {
		if r.limit == nil {
			continue
		}
		if r.limit != last {
			list = append(list, newRateLimit(r.ID, r.limit, connection))
			last = r.limit
		}
		list[len(list)-1].Routes = append(list[len(list)-1].Routes, r.ID)
	}
	return list
}

// newRateLimit is the table of limit, named by its first route, id. A client is the connection's peer (src), or under
// forwarded the last X-Forwarded-For address and the peer when the request holds none, each through clientNetwork;
// by: all keys every request alike.
// clientNetwork is HAProxy's own ipmask on a client's key: an IPv4 address is kept whole and an IPv6 address is cut
// to its /64 network, the block one client is given, so a client cannot start a new count by moving inside its own
// network (the manual, ipmask).
const clientNetwork = ",ipmask(32,64)"

func newRateLimit(id int, limit *model.RateLimit, connection bool) rateLimit {
	r := rateLimit{Table: "proxyfile_rate" + strconv.Itoa(id), Type: "integer size 1", Period: timeText(limit.Per.Millis),
		Requests: limit.Requests, Track: []tracking{{Key: "int(1)"}}}
	if limit.ByClient {
		r.Type, r.Track = "ipv6 size 100k", []tracking{{Key: "src" + clientNetwork}}
		if !connection {
			found := "{ " + lastForwarded + " -m found }"
			r.Track = []tracking{{Key: lastForwarded + clientNetwork, Condition: found}, {Key: "src" + clientNetwork, Condition: "!" + found}}
		}
	}
	return r
}

type rewrite struct {
	ID            int
	Regex, Format string
}

type backend struct {
	Name       string
	Balance    string      // balance's argument (balance), "" for round robin, HAProxy's default
	Health     *healthView // the HTTP check of an http backend, nil without health.path
	Retries    []retry     // the retries of each retried route the backend serves, in the routes' order
	HostHeader string      // the service's quoted host_header, "" when not written
	Servers    []string    // the server lines (servers)
}

// retry is HAProxy's own retries for the requests of route Route, Count, set by http-request set-retries in the
// backend the route reaches: the backend's other routes keep the defaults' retries 0. retry-on stays at HAProxy's
// default, conn-failure, a connection that failed, and option redispatch, written once in a backend with a retry, lets
// a try go to another server (the manual, retries, retry-on and option redispatch). Measured on HAProxy 3.2.26 and
// 3.4.7: round robin and leastconn sent the first retry at once to the other address, a hash sent every try to the
// address it picks, and a service of one address was tried again after 1 s.
type retry struct {
	Count int64
	Route int
}

// addRetries gives each backend the retries of the routes that reach it.
func addRetries(backends []backend, routes []route) {
	for _, r := range routes {
		if r.retries == 0 {
			continue
		}
		at := slices.IndexFunc(backends, func(b backend) bool { return b.Name == r.Backend })
		backends[at].Retries = append(backends[at].Retries, retry{Count: r.retries, Route: r.ID})
	}
}

// healthView is HAProxy's own HTTP check of health.path: option httpchk GET with Path, quoted, http-check expect for a
// 2xx, and timeout check, Timeout, health.timeout. HAProxy sends it over HTTP/1.0 with no Host header, its defaults
// (the manual, option httpchk), and a server's connect for it takes the shorter of timeout connect and inter.
type healthView struct {
	Path, Timeout string
}

// health is the HTTP check of service, nil without health.path.
func health(service *model.Service) *healthView {
	if service.Health == nil || service.Health.Path == nil {
		return nil
	}
	return &healthView{Path: quote(service.Health.Path.Value), Timeout: timeText(service.Health.Timeout.Millis)}
}

// Generate returns the HAProxy configuration and the script it loads, proxyfile.lua. Its error is a generator.Refused
// that names each value written into a regular expression or a program longer than HAProxy loads, by its key, a
// problem in the input (values); any other error means the gateway holds a form this generator does not write
// (notWritten), which HAProxy's declaration refuses before render calls Generate, a pattern that does not compile,
// which the reader refuses first, or that the template did not fill: a bug in proxyfile each time.
func Generate(in generator.Input) ([]generator.File, error) {
	if err := notWritten(in); err != nil {
		return nil, err
	}
	g := in.Gateway
	v := view{Header: generator.Header, Listen: g.Listen.Number, ClientAddress: model.ClientForwarded, Scheme: "http",
		EnvoyRemoved: generator.RemovedByEnvoy, Methods: strings.Join(generator.EnvoyMethods, " "),
		ConnectionTokens: generator.ConnectionTokens, TooManyConnectionTokens: generator.MostConnectionTokens + 1, LongestID: idRoom,
		GRPC: grpcStatuses{BadRequest: generator.GRPCStatus[http.StatusBadRequest], Forbidden: generator.GRPCStatus[http.StatusForbidden],
			NotFound: generator.GRPCStatus[http.StatusNotFound], Unknown: generator.GRPCUnknown}}
	v.ClientTimeout, v.ServerTimeout = timeouts(g.Timeouts)
	count := int64(envoyHeaderCount)
	if headers := g.Limits.RequestHeaders; headers != nil {
		count, v.BufSize = headers.Count.Number, headers.Size.Bytes+format.HAProxyRewriteRoom
	}
	v.MaxHeaders, v.FieldsRefused = count+1, max(count-2, 0)
	if g.Limits.Connections != nil {
		v.MaxConn = g.Limits.Connections.Number
	}
	// Only connection takes the peer of the connection, so a Gateway built with the zero value writes forwarded, as
	// Envoy's file does.
	if g.ClientAddress == model.ClientConnection {
		v.ClientAddress, v.Connection, v.Faced = model.ClientConnection, true, generator.RemovedByEnvoyFacingClients
	}
	if g.TLS != nil {
		v.TLS = &tlsView{Certificate: quote(g.TLS.Certificate), Key: quote(g.TLS.Key)}
		if g.TLS.ClientsCA != "" {
			v.TLS.ClientsCA = quote(g.TLS.ClientsCA)
		}
		v.Scheme = "https"
	}
	if g.Admin != nil {
		v.Admin = g.Admin.Number
	}
	if g.Log != nil {
		v.Sample = &sample{StatusFrom: g.Log.StatusFrom, Percent: g.Log.SamplePercent}
	}
	for _, header := range generator.RemovedFromClients(g) {
		if line := removal(header); line != "" {
			v.Removed = append(v.Removed, line)
		}
	}
	if g.Auth != nil {
		v.Unchecked = generator.UncheckedMark
	}
	var values values
	v.Environments, v.originCheck = environments(in.Origins, &values)
	var err error
	v.Routes, v.Rewrites, err = routes(in, &values)
	if err != nil {
		return nil, fmt.Errorf("haproxy: %w", err)
	}
	if refused := values.Err(); refused != nil {
		return nil, refused
	}
	v.Backends = backends(g)
	addRetries(v.Backends, v.Routes)
	v.RateLimits = rateLimits(v.Routes, v.Connection)
	if slices.ContainsFunc(g.Services, func(s model.Service) bool { return s.DNS }) {
		v.Resolvers = &resolversView{Name: resolversName}
		if g.Resolver != nil {
			v.Resolvers.Nameserver = net.JoinHostPort(g.Resolver.Address, strconv.FormatInt(g.Resolver.Port, 10))
		}
	}
	if g.Auth != nil && g.Auth.Enabled {
		v.Check = check(in, v.Routes)
	}
	var out bytes.Buffer
	if err := configuration.Execute(&out, v); err != nil {
		return nil, fmt.Errorf("haproxy: %w", err)
	}
	if line := overLong(out.String()); line != 0 {
		return nil, fmt.Errorf("haproxy: line %d of the configuration holds more than the %d words HAProxy reads", line, mostWords)
	}
	return []generator.File{{Bytes: out.Bytes()}, {Name: scriptName, Bytes: script()}}, nil
}

// envoyHeaderCount is the header count a file without limits.request_headers writes, Envoy's default of 100.
const envoyHeaderCount = 100

// timeouts returns timeout client and timeout server. Without timeouts.idle they are the 5 minutes of Envoy's stream
// idle timeout and, on the server's side, the 15 s of a route's timeout, or the 5 minutes with timeouts.request: none
// (haproxy.cfg.tmpl). timeouts.idle is HAProxy's own time without data on each side, and on the server's side the
// shorter of it and the time written without it; none is the longest time a timer holds.
func timeouts(t model.Timeouts) (client, server string) {
	clientMillis, serverMillis := int64(300000), int64(15000)
	if t.Request != nil {
		serverMillis = 300000
	}
	if t.Idle == nil {
		return timeText(clientMillis), timeText(serverMillis)
	}
	if t.Idle.Millis == 0 {
		return timeText(format.HAProxyLongestTime), timeText(serverMillis)
	}
	return timeText(t.Idle.Millis), timeText(min(t.Idle.Millis, serverMillis))
}

// timeText is a time HAProxy reads: in seconds when whole, else in milliseconds.
func timeText(millis int64) string {
	if millis%1000 == 0 {
		return strconv.FormatInt(millis/1000, 10) + "s"
	}
	return strconv.FormatInt(millis, 10) + "ms"
}

// environments is the data of the environment rules and of the browser origin lines. A host and a pattern are written
// into a regular expression too, which v refuses when HAProxy would not load it.
func environments(file *origins.File, v *values) ([]environment, originCheck) {
	var list []environment
	listed := false
	for i, e := range file.Environments {
		environment := environment{ID: "env" + strconv.Itoa(i+1), Comment: strconv.Quote(e.Name), Listed: e.Listed()}
		for _, host := range e.Hosts {
			environment.Hosts = append(environment.Hosts, quote(host))
			environment.Own = append(environment.Own, v.regex(hostsKey, e.Pos, origins.SameOriginRegex(host)))
		}
		for _, origin := range e.Origins {
			environment.Origins = append(environment.Origins, quote(origin))
		}
		for _, pattern := range e.Patterns {
			environment.Patterns = append(environment.Patterns, v.regex(patternsKey, e.Pos, origins.PatternPCRE(pattern)))
		}
		listed = listed || environment.Listed
		list = append(list, environment)
	}
	c := file.CORS
	// With *, Envoy answers a preflight with the method and the headers it asks for, so the answer copies them
	// (measured on Envoy 1.40.2). HAProxy leaves out a header whose value comes out empty, such as the copy of no
	// headers, where Envoy sends it empty. Each other value is a log-format string, so its % is written %%.
	answer := ""
	switch c.AllowMethods {
	case "":
	case "*":
		answer += ` hdr access-control-allow-methods "%[req.fhdr(access-control-request-method)]"`
	default:
		answer += " hdr access-control-allow-methods " + logFormat(c.AllowMethods)
	}
	switch c.AllowHeaders {
	case "":
	case "*":
		answer += ` hdr access-control-allow-headers "%[req.fhdr(access-control-request-headers)]"`
	default:
		answer += " hdr access-control-allow-headers " + logFormat(c.AllowHeaders)
	}
	if c.MaxAge != "" {
		answer += " hdr access-control-max-age " + logFormat(c.MaxAge)
	}
	expose := ""
	if c.ExposeHeaders != "" {
		expose = logFormat(c.ExposeHeaders)
	}
	return list, originCheck{Listed: listed, Answer: answer, Expose: expose, LongestOrigin: max(0, echoRoom-len(c.ExposeHeaders)),
		LongestEcho: max(0, echoRoom+ownID-len(c.ExposeHeaders))}
}

// keptByPrefix are the headers a prefix of remove_from_clients or auth.accept leaves on the request, as Envoy 1.40.2
// does: a del-header by the prefix removed Host, so that every request was answered 404, and Transfer-Encoding, so
// that the service lost the body (measured on HAProxy 3.2.26 and 3.4.7). Envoy removes TE under a prefix, as HAProxy
// does, and keep-alive, trailer, expect, proxy-connection and http2-settings, as HAProxy does (measured). keptWhole are
// the ones a name written in full leaves: Envoy sent Transfer-Encoding on with the body chunked, and answered 404
// without Host, as HAProxy does. notWritten refuses a prefix or a name that covers format.HAProxyNotRemovable.
var (
	keptByPrefix = []string{"host", "transfer-encoding"}
	keptWhole    = []string{"transfer-encoding"}
)

// removal is del-header's argument for one header removed from clients: its quoted name, "" for a name of keptWhole,
// which no line removes, or for a prefix the prefix matched at the start of a name, as a regular expression that
// leaves out the names of keptByPrefix it covers.
func removal(header model.Header) string {
	if !header.Prefix {
		if slices.Contains(keptWhole, header.Name) {
			return ""
		}
		return quote(header.Name)
	}
	var kept []string
	for _, name := range keptByPrefix {
		if rest, ok := strings.CutPrefix(name, header.Name); ok {
			kept = append(kept, regexp.QuoteMeta(rest)+"$")
		}
	}
	if kept == nil {
		return quote(header.Name) + " -m beg"
	}
	return quote("^"+regexp.QuoteMeta(header.Name)+"(?!"+strings.Join(kept, "|")+")") + " -m reg"
}

// check is the data of the auth check's lines, for a gateway whose auth.ask is enabled, with the routes the file picks.
// The read rules have made the check service a declared service with an http port.
func check(in generator.Input, routes []route) *checkView {
	a := in.Gateway.Auth
	service := in.Gateway.Service(a.Service.Value)
	port, _ := service.Port(model.HTTP)
	socket := "abns@proxyfile-" + strconv.FormatInt(in.Gateway.Listen.Number, 10) + "-check"
	c := &checkView{Relay: quote(socket), Bind: socket, Path: quote(a.Path), Send: headerList(a.Send), Accept: headerList(a.Accept),
		Millis: a.Timeout.Millis, Unavailable: "deny", SendRequest: "0",
		ActionHeader: generator.QuestionHeaders[0].Name, ReportsHeader: generator.QuestionHeaders[1].Name}
	if a.Allow {
		c.Unavailable = "allow"
	}
	if a.SendRequest {
		c.SendRequest = "1"
	}
	// Envoy asks a request whose only_with_header holds a byte, on a route that is not auth: off, that its CORS filter
	// does not answer as a preflight; and a request with a dot segment and a parameter whatever its route, as the route
	// of Envoy's own that refuses one is checked (measured on Envoy 1.40.2). It reads the header's lines as one value,
	// joined by commas, so the template's line sets txn.pf_header when a line is not empty (req.fhdr reads each line
	// whole, where req.hdr would split it at its commas) or when the header comes on two lines or more, empty ones
	// too; a header pf_join joins is one line by then. Over HTTP/2 HAProxy reads a value of spaces as empty, which
	// Envoy asks, so the line sets it for every HTTP/2 request that carries the header, an empty one too, which Envoy
	// does not ask (auth.only_with_header). A condition cannot hold an OR inside the ask line's AND, hence the variable.
	header := ""
	if a.OnlyWithHeader != "" {
		c.OnlyWith = a.OnlyWithHeader
		header = "{ var(txn.pf_header) -m bool } "
	}
	c.Ask = header
	routeOf := map[string]int{} // the route of each RPC method's path
	for _, r := range routes {
		if r.authOff {
			c.Ask += "!{ var(txn.route) -m str " + strconv.Itoa(r.ID) + " } "
		}
		for _, path := range r.methods {
			routeOf[path] = r.ID
		}
	}
	c.Ask += "!{ var(txn.pf_preflight) -m bool } || " + header + "dot_segment"
	// A question's value is set-header's, a log-format string, where % starts a sample.
	for _, q := range generator.Questions(in) {
		if q.Action != "" {
			c.Questions = append(c.Questions, question{Header: c.ActionHeader, Value: logFormat(q.Action), Route: routeOf[q.Path], Path: quote(q.Path)})
		}
		if q.Reports != nil {
			c.Questions = append(c.Questions, question{Header: c.ReportsHeader, Value: logFormat(strings.Join(q.Reports, ",")), Route: routeOf[q.Path], Path: quote(q.Path)})
		}
	}
	c.Servers, c.Balance, c.Health = servers(in.Gateway, service, port), spread(service), health(service)
	return c
}

// headerList is the quoted names of an auth list joined by commas, a prefix written with its *, as the script reads
// them; - when the list is empty.
func headerList(headers []model.Header) string {
	if len(headers) == 0 {
		return quote("-")
	}
	names := make([]string, len(headers))
	for i, header := range headers {
		names[i] = header.Name
		if header.Prefix {
			names[i] += "*"
		}
	}
	return quote(strings.Join(names, ","))
}

// routes is the rule of each route, in the written order, with the rpc entry's at its place: one rule per service the
// entry reaches, in the order of each service's first path in in.RPC, which is sorted by path. Each rule sets the
// route's number only when no rule before it matched, because HAProxy runs every http-request rule and Envoy takes the
// first route that matches. A rewrite is written for each route that has one, and the program of each pattern.
func routes(in generator.Input, v *values) ([]route, []rewrite, error) {
	g := in.Gateway
	var list []route
	var rewrites []rewrite
	for _, r := range g.Routes {
		if r.RPC != nil {
			for _, group := range rpcGroups(in.RPC) {
				var matches []string
				for _, path := range group.paths {
					matches = append(matches, "{ path "+quote(path)+" }")
				}
				list = append(list, route{ID: len(list) + 1, Matches: matches, Backend: group.backend, methods: group.paths, limit: r.RPC.RateLimit})
			}
			continue
		}
		id := len(list) + 1
		var match, text, pattern string
		switch r.Match {
		case model.Prefix:
			match = "{ path_beg " + quote(r.Value.Value) + " }"
		case model.Path:
			match = "{ path " + quote(r.Value.Value) + " }"
		case model.Pattern:
			compiled, err := program(r.Value.Value)
			if err != nil {
				return nil, nil, fmt.Errorf("route %q: %w", r.Name.Value, err)
			}
			text, pattern = v.program(r.Value.Pos, compiled), strconv.Quote(r.Value.Value)
			match = "{ var(txn.pattern" + strconv.Itoa(id) + ") -m str 1 }"
		}
		// The format admits only upper-case letters in a method, so it needs no quotes.
		if r.Method != "" {
			match += " { method " + r.Method + " }"
		}
		list = append(list, route{ID: id, Matches: []string{match}, Backend: g.Service(r.To.Value).Cluster(model.HTTP), Program: text, Pattern: pattern,
			authOff: r.AuthOff != nil, retries: r.Retries, limit: r.RateLimit})
		if r.Rewrite != nil {
			key := "routes[].prefix"
			if r.Match == model.Path {
				key = "routes[].path"
			}
			rewrites = append(rewrites, rewrite{ID: id, Regex: v.regex(key, r.Value.Pos, "^"+regexp.QuoteMeta(r.Value.Value)+"(.*)$"),
				Format: rewriteFormat(r.Rewrite.Value)})
		}
	}
	return list, rewrites, nil
}

// rpcGroup is the paths of the RPC methods that go to one service, and that service's gRPC backend.
type rpcGroup struct {
	backend string
	paths   []string
}

func rpcGroups(methods []rpc.Route) []rpcGroup {
	var groups []rpcGroup
	for _, method := range methods {
		at := slices.IndexFunc(groups, func(group rpcGroup) bool { return group.backend == method.Cluster })
		if at < 0 {
			groups = append(groups, rpcGroup{backend: method.Cluster})
			at = len(groups) - 1
		}
		groups[at].paths = append(groups[at].paths, method.Path())
	}
	return groups
}

// backends is one backend per service and port, as Envoy's clusters are: the services in the written order, each
// service's ports in the written order. A backend's name is the cluster's name, which every route rule names too.
// host_header is set in the backend, so it applies to every route that uses the backend, as Envoy's
// host_rewrite_literal does on each route to the service.
func backends(g *model.Gateway) []backend {
	var list []backend
	for _, service := range g.Services {
		for _, port := range service.Ports {
			b := backend{Name: service.Cluster(port.Protocol), Balance: balance(g, &service)}
			if port.Protocol == model.HTTP {
				b.Health = health(&service)
			}
			if service.HostHeader != "" {
				b.HostHeader = logFormat(service.HostHeader)
			}
			b.Servers = servers(g, &service, port)
			list = append(list, b)
		}
	}
	return list
}

// balance is the argument of HAProxy's own balance for service, "" for round_robin, HAProxy's default: spread's for
// least_requests and random, and for hash, hdr() of the header, source for the client's address under connection, or
// under forwarded the last X-Forwarded-For address, which the client in front of the proxy is. hash-type is left at
// HAProxy's default, map-based.
func balance(g *model.Gateway, service *model.Service) string {
	if b := spread(service); b != "" {
		return b
	}
	switch {
	case service.Hash != nil && service.Hash.Header != "":
		return "hdr(" + service.Hash.Header + ")"
	case service.Hash != nil && g.ClientAddress == model.ClientConnection:
		return "source"
	case service.Hash != nil:
		return "hash " + lastForwarded
	}
	return ""
}

// spread is HAProxy's own balance for a balance that reads no request: leastconn for least_requests, which counts
// connections, one a request here, and random(1), one draw, for random; "" for round robin, HAProxy's default. It is
// also the check relay's whole balance for the service auth.ask names, round robin for a hash: the relay is a TCP
// section whose one client is HAProxy itself, so it reads no header and every connection has one source, and HAProxy
// 3.2.26 and 3.4.7 warn at balance hdr() in such a section and go round robin ("Layer 7 hash not possible", measured
// with the validator).
func spread(service *model.Service) string {
	switch service.Balance.Value {
	case "least_requests":
		return "leastconn"
	case "random":
		return "random(1)"
	}
	return ""
}

// servers returns the server lines of service on port, the check relay's as a backend's: one for each address, in the
// written order (server), or for a DNS name one server-template of templateServers servers, named as server names
// them, which HAProxy's own resolvers fill with the name's addresses, each starting down with no address until they
// answer (init-addr none). Each line ends with the options every server of a service takes.
func servers(g *model.Gateway, service *model.Service, port model.ServicePort) []string {
	var lines []string
	for i, address := range service.Addresses {
		line := "server " + server(service.Name, i, address.Value, port.Number)
		if service.DNS {
			line = fmt.Sprintf("server-template %s 1-%d %s resolvers %s init-addr none", service.Name, templateServers,
				net.JoinHostPort(address.Value, strconv.FormatInt(port.Number, 10)), resolversName)
		}
		// A gRPC service's connection carries one call and is never used again, as an HTTP service's is closed after
		// each answer (the template's defaults): HAProxy sent a call again on a new connection when the service closed
		// one it reused (measured on HAProxy 3.2.26 and 3.4.7).
		if port.Protocol == model.GRPC {
			line += " proto h2 max-reuse 0"
		}
		// limits.connections holds each server to the count: a request past it waits in HAProxy's queue.
		if g.Limits.Connections != nil {
			line += " maxconn " + strconv.FormatInt(g.Limits.Connections.Number, 10)
		}
		line += checked(service, port, i)
		lines = append(lines, line)
	}
	return lines
}

// checked is what a server line of service on port adds for health.path: the check's interval and counts on an http
// server, which health checks, and on a gRPC server of a service at IP addresses track of the http server at the same
// address, so it leaves the pool with it. A server-template tracks no server: HAProxy 3.2.26 and 3.4.7 looked for one
// named as the template's prefix and refused the file (measured with the validator), so the gRPC servers of a service
// found by a DNS name take no check and stay in the pool, as without health.
func checked(service *model.Service, port model.ServicePort, i int) string {
	h := service.Health
	switch {
	case h == nil || h.Path == nil:
		return ""
	case port.Protocol == model.HTTP:
		return fmt.Sprintf(" check inter %s fall %d rise %d", timeText(h.Interval.Millis), h.Unhealthy, h.Healthy)
	case !service.DNS:
		return fmt.Sprintf(" track %s/%s%d", service.Cluster(model.HTTP), service.Name, i+1)
	}
	return ""
}

// server is a server line's name and address for the address at index i of a service: the service's name and i+1, and
// the address with its port. JoinHostPort writes an IPv6 address in brackets, which HAProxy reads as the address and
// the port after it (measured on HAProxy 3.2.26 and 3.4.7).
func server(service string, i int, address string, port int64) string {
	return fmt.Sprintf("%s%d %s", service, i+1, net.JoinHostPort(address, strconv.FormatInt(port, 10)))
}

// notWritten returns an error for each form of the input this version does not write, nil when there is none. Leaving
// one out would serve requests the proxyfile limits, checks or refuses, so the generator writes no file then.
func notWritten(in generator.Input) error {
	g := in.Gateway
	var errs []error
	refuse := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("haproxy: "+format, args...))
	}
	keys := []struct {
		key     string
		written bool
	}{
		// none, Millis 0, is what HAProxy does with no line: nothing limits a whole request.
		{"timeouts.request as a time", g.Timeouts.Request != nil && g.Timeouts.Request.Millis != 0},
		{"auth.jwt", g.JWT != nil},
		{"tracing", g.Tracing != nil},
	}
	for _, k := range keys {
		if k.written {
			refuse("%s is not written in this version", k.key)
		}
	}
	// The forms the declaration refuses past HAProxy's bounds (format.HAProxySmallestHeaders and the constants beside it).
	if headers := g.Limits.RequestHeaders; headers != nil {
		if headers.Size.Bytes < format.HAProxySmallestHeaders {
			refuse("limits.request_headers.size below 15KiB is not written in this version")
		}
		if headers.Count.Number > format.HAProxyMostHeaderFields {
			refuse("limits.request_headers.count %d is not written in this version", headers.Count.Number)
		}
	}
	if g.Timeouts.Idle != nil && g.Timeouts.Idle.Millis > format.HAProxyLongestTime {
		refuse("timeouts.idle above %dms is not written in this version", format.HAProxyLongestTime)
	}
	for _, service := range g.Services {
		for _, address := range service.Addresses {
			// The reader admits an IPv6 address with a zone, such as fe80::1%eth0. HAProxy 3.2.26 and 3.4.7 refused to
			// load one as [::1%lo]:3000, ::1%lo:3000 and ipv6@::1%lo:3000, with lo an interface they had (measured).
			if ip, err := netip.ParseAddr(address.Value); !service.DNS && err == nil && ip.Zone() != "" {
				refuse("service %s: address %q names a zone, which HAProxy 3.2 and 3.4 load in no server address", service.Name, address.Value)
			}
		}
		if service.Health != nil && service.Health.GRPC != nil {
			refuse("service %s: health.grpc is not written in this version", service.Name)
		}
		// check inter and timeout check are timers.
		if service.Health != nil && service.Health.Interval.Millis > format.HAProxyLongestTime {
			refuse("service %s: health.interval above %dms is not written in this version", service.Name, format.HAProxyLongestTime)
		}
		if service.Health != nil && service.Health.Timeout.Millis > format.HAProxyLongestTime {
			refuse("service %s: health.timeout above %dms is not written in this version", service.Name, format.HAProxyLongestTime)
		}
	}
	// HAProxy's rate has no burst, and its period is a timer.
	limited := func(entry string, limit *model.RateLimit) {
		if limit == nil {
			return
		}
		if limit.Burst > 0 {
			refuse("%s: rate_limit.burst %d is not written in this version", entry, limit.Burst)
		}
		if limit.Per.Millis > format.HAProxyLongestTime {
			refuse("%s: rate_limit.per above %dms is not written in this version", entry, format.HAProxyLongestTime)
		}
	}
	for _, r := range g.Routes {
		if r.RPC != nil {
			// With retry-on at its default, HAProxy 3.2.26 and 3.4.7 did not send a gRPC call again after its connection
			// to the service failed; the retry-on that does also sends again a request a service may have read.
			if r.RPC.Retries > 0 {
				refuse("the rpc entry: retries is not written in this version")
			}
			limited("the rpc entry", r.RPC.RateLimit)
			continue
		}
		if r.Retries > format.HAProxyMostRetries {
			refuse("route %q: retries %d is not written in this version", r.Name.Value, r.Retries)
		}
		limited(fmt.Sprintf("route %q", r.Name.Value), r.RateLimit)
	}
	removals := func(key string, headers []model.Header) {
		for _, header := range headers {
			if !header.Prefix {
				if slices.Contains(format.HAProxyNotRemovable, header.Name) {
					refuse("%s: %s is not written in this version", key, header.Name)
				}
				continue
			}
			var covered []string
			for _, name := range format.HAProxyNotRemovable {
				if strings.HasPrefix(name, header.Name) {
					covered = append(covered, name)
				}
			}
			if covered != nil {
				refuse("%s: %s*, a prefix that covers %s, is not written in this version", key, header.Name, strings.Join(covered, " and "))
			}
		}
	}
	removals("remove_from_clients", g.RemoveFromClients)
	if g.Auth != nil {
		removals("auth.accept", g.Auth.Accept)
	}
	return errors.Join(errs...)
}
