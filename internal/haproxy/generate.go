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
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

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
	Removed       []string // del-header's arguments, in the order generator.RemovedFromClients gives
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
	Routes   []route
	Rewrites []rewrite
	Backends []backend
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
	Patterns []string // origins.PatternRegex of each pattern
	Own      []string // origins.SameOriginRegex of each host: an origin on one of the environment's own hosts
	Listed   bool
}

// originCheck is the data of the browser origin lines, which every file holds: Envoy refuses another site's origin
// with 403 and adds Vary: Origin to every answer whatever the file's cors and lists (measured on Envoy 1.40.2). Listed
// says an environment lists origins or patterns. Answer is the preflight answer's headers every admitted origin gets;
// Expose is the quoted expose_headers, "" when not written.
type originCheck struct {
	Listed         bool
	Answer, Expose string
}

// route is one route the file picks: ID is its number in the generated order, from 1, and Matches the conditions of
// its rules, one a line: the rpc entry's paths to one service, or the route's one condition. Program is the quoted
// program of a route's pattern, which the script runs before the condition reads its answer, "" for a route with
// none; Pattern is the pattern, Go-quoted, for the comment line that says which pattern the program is.
type route struct {
	ID      int
	Matches []string
	Backend string
	Program string
	Pattern string
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
		ConnectionTokens: generator.ConnectionTokens, TooManyConnectionTokens: generator.MostConnectionTokens + 1}
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
		argument := quote(header.Name)
		if header.Prefix {
			argument += " -m beg"
		}
		v.Removed = append(v.Removed, argument)
	}
	var values values
	v.Environments, v.originCheck = environments(in.Origins, &values)
	var err error
	v.Routes, v.Rewrites, err = routes(in, &values)
	if err != nil {
		return nil, fmt.Errorf("haproxy: %w", err)
	}
	if refused := values.refused(); refused != nil {
		return nil, refused
	}
	v.Backends = backends(g)
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
		environment := environment{ID: "env" + strconv.Itoa(i+1), Comment: strconv.Quote(e.Name),
			Listed: len(e.Origins) > 0 || len(e.Patterns) > 0}
		for _, host := range e.Hosts {
			environment.Hosts = append(environment.Hosts, quote(host))
			environment.Own = append(environment.Own, v.regex(hostsKey, e.Pos, origins.SameOriginRegex(host)))
		}
		for _, origin := range e.Origins {
			environment.Origins = append(environment.Origins, quote(origin))
		}
		for _, pattern := range e.Patterns {
			environment.Patterns = append(environment.Patterns, v.regex(patternsKey, e.Pos, patternRegex(pattern)))
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
	return list, originCheck{Listed: listed, Answer: answer, Expose: expose}
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
					matches = append(matches, "{ path "+path+" }")
				}
				list = append(list, route{ID: len(list) + 1, Matches: matches, Backend: group.backend})
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
		list = append(list, route{ID: id, Matches: []string{match}, Backend: g.Service(r.To.Value).Cluster(model.HTTP), Program: text, Pattern: pattern})
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

// patternRegex is the PCRE2 expression of an origin pattern: origins.PatternRegex reads each * as .*, which PCRE2 tries
// at every length when the text does not match, so one Origin of 8 KB against 64 patterns of two * each stopped
// HAProxy 3.2.26 and 3.4.7 (measured). A text matches the pattern when it starts with the part before the first *, ends
// with the part after the last, and holds each part between them in order; the first place of each such part leaves
// the most room to the others, so each is matched there by an atomic group, which PCRE2 never tries again elsewhere,
// and the time grows with the Origin's length alone. Each part is quoted by regexp.QuoteMeta, as PatternRegex quotes
// it.
func patternRegex(pattern string) string {
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	written := "^" + parts[0]
	if last := len(parts) - 1; last > 0 {
		for _, part := range parts[1:last] {
			written += "(?>.*?" + part + ")"
		}
		written += ".*" + parts[last]
	}
	return written + "$"
}

// rpcGroup is the quoted paths of the RPC methods that go to one service, and that service's gRPC backend.
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
		groups[at].paths = append(groups[at].paths, quote(method.Path()))
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
				// JoinHostPort writes an IPv6 address in brackets, which HAProxy reads as the address and the port after it
				// (measured on HAProxy 3.2.26 and 3.4.7).
				server := fmt.Sprintf("%s%d %s", service.Name, i+1, net.JoinHostPort(address.Value, strconv.FormatInt(port.Number, 10)))
				// A gRPC service's connection carries one call and is never used again, as an HTTP service's is closed
				// after each answer (the template's defaults): HAProxy sent a call again on a new connection when the
				// service closed one it reused (measured on HAProxy 3.2.26 and 3.4.7).
				if port.Protocol == model.GRPC {
					server += " proto h2 max-reuse 0"
				}
				b.Servers = append(b.Servers, server)
			}
			list = append(list, b)
		}
	}
	return list
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
		{"auth.ask", g.Auth != nil},
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
		if r.AuthOff != nil {
			refuse("route %q: auth is not written in this version", r.Name.Value)
		}
		if r.Retries > 0 {
			refuse("route %q: retries is not written in this version", r.Name.Value)
		}
		if r.RateLimit != nil {
			refuse("route %q: rate_limit is not written in this version", r.Name.Value)
		}
	}
	for _, method := range in.RPC {
		if method.Action != "" {
			refuse("method %s/%s: action is not written in this version", method.Service, method.Method.Method)
		}
		if method.Reports != nil {
			refuse("method %s/%s: reports is not written in this version", method.Service, method.Method.Method)
		}
	}
	return errors.Join(errs...)
}
