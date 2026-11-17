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
	ServerTimeout string   // timeout server: 15s without timeouts.request, 300s with none
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
	GRPC     grpcStatuses
	Routes   []route
	Rewrites []rewrite
	Backends []backend
	// Unchecked is generator.UncheckedMark with auth.ask, enabled or not, whose client's copy is removed after the
	// others; "" without auth.ask.
	Unchecked string
	Check     *checkView // nil without auth.ask or with enabled: false
}

// checkView is the data of the auth check's lines. The action's arguments come in the order the script reads them:
// Relay, the socket of the relay to the check service, quoted; Path, Send and Accept, quoted, a list's names joined by
// commas, a prefix with its *, and - for none; then Millis, Unavailable (allow or deny) and SendRequest (1 or 0), the
// generator's own words, written bare. Bind is the relay's socket for its bind line, the generator's own text too.
// OnlyWith is auth.only_with_header, whose own line sets txn.pf_header; Ask is the condition that asks a request,
// written after if; Questions is the RPC question of each method that has
// one; Servers is one server line's arguments for each address of the check service.
type checkView struct {
	Relay, Bind                 string
	Path, Send, Accept          string
	Millis                      int64
	Unavailable, SendRequest    string
	OnlyWith                    string // auth.only_with_header, "" without it
	Ask                         string
	Questions                   []question
	Servers                     []string
	ActionHeader, ReportsHeader string // generator.QuestionHeaders, set for the check and removed after it
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
}

type rewrite struct {
	ID            int
	Regex, Format string
}

type backend struct {
	Name       string
	HostHeader string // the service's quoted host_header, "" when not written
	Servers    []string
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
	v.ServerTimeout = "15s"
	if g.Timeouts.Request != nil {
		v.ServerTimeout = "300s"
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
	for i, address := range service.Addresses {
		c.Servers = append(c.Servers, server(service.Name, i, address.Value, port.Number))
	}
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
				list = append(list, route{ID: len(list) + 1, Matches: matches, Backend: group.backend, methods: group.paths})
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
			authOff: r.AuthOff != nil})
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
			b := backend{Name: service.Cluster(port.Protocol)}
			if service.HostHeader != "" {
				b.HostHeader = logFormat(service.HostHeader)
			}
			for i, address := range service.Addresses {
				line := server(service.Name, i, address.Value, port.Number)
				// A gRPC service's connection carries one call and is never used again, as an HTTP service's is closed
				// after each answer (the template's defaults): HAProxy sent a call again on a new connection when the
				// service closed one it reused (measured on HAProxy 3.2.26 and 3.4.7).
				if port.Protocol == model.GRPC {
					line += " proto h2 max-reuse 0"
				}
				b.Servers = append(b.Servers, line)
			}
			list = append(list, b)
		}
	}
	return list
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
		{"limits.connections", g.Limits.Connections != nil},
		{"limits.request_headers", g.Limits.RequestHeaders != nil},
		// none, Millis 0, is what HAProxy does with no line: nothing limits a whole request.
		{"timeouts.request as a time", g.Timeouts.Request != nil && g.Timeouts.Request.Millis != 0},
		{"timeouts.idle", g.Timeouts.Idle != nil},
		{"timeouts.upstream_idle", g.Timeouts.UpstreamIdle != nil},
		{"dns.resolver", g.Resolver != nil},
		{"auth.jwt", g.JWT != nil},
		{"log", g.Log != nil},
		{"tracing", g.Tracing != nil},
	}
	for _, k := range keys {
		if k.written {
			refuse("%s is not written in this version", k.key)
		}
	}
	for _, service := range g.Services {
		for _, address := range service.Addresses {
			// The reader admits an IPv6 address with a zone, such as fe80::1%eth0. HAProxy 3.2.26 and 3.4.7 refused to
			// load one as [::1%lo]:3000, ::1%lo:3000 and ipv6@::1%lo:3000, with lo an interface they had (measured).
			switch ip, err := netip.ParseAddr(address.Value); {
			case err != nil:
				refuse("service %s: address %q is not an IP address", service.Name, address.Value)
			case ip.Zone() != "":
				refuse("service %s: address %q names a zone, which HAProxy 3.2 and 3.4 load in no server address", service.Name, address.Value)
			}
		}
		// The reader gives a service with no balance round_robin, which is HAProxy's own default and needs no line;
		// the declaration refuses any balance a file writes, round_robin too, before render calls Generate.
		if service.Balance.Value != "round_robin" {
			refuse("service %s: balance %s is not written in this version", service.Name, service.Balance.Value)
		}
		if service.Hash != nil {
			refuse("service %s: hash is not written in this version", service.Name)
		}
		if service.Health != nil {
			refuse("service %s: health is not written in this version", service.Name)
		}
	}
	for _, r := range g.Routes {
		if r.RPC != nil {
			if r.RPC.Retries > 0 {
				refuse("the rpc entry: retries is not written in this version")
			}
			if r.RPC.RateLimit != nil {
				refuse("the rpc entry: rate_limit is not written in this version")
			}
			continue
		}
		if r.Retries > 0 {
			refuse("route %q: retries is not written in this version", r.Name.Value)
		}
		if r.RateLimit != nil {
			refuse("route %q: rate_limit is not written in this version", r.Name.Value)
		}
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
