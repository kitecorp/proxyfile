package read

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/kitecorp/proxyfile/internal/model"
)

// rules holds the checks no single key can fail: a name that must match a declaration, two entries that clash, and
// a key that means nothing beside another.
type rules struct {
	model.Problems
	g *model.Gateway
}

func meaning(g *model.Gateway) []model.Problem {
	r := rules{g: g}
	r.routes()
	if g.Auth != nil {
		r.service("auth:", g.Auth.Service, model.HTTP)
	}
	// envoy --mode validate generates no network traffic (its command-line documentation says so), so it accepts two
	// listeners on one port; the gateway fails only when it starts.
	if g.Admin != nil && g.Admin.Number == g.Listen.Number {
		r.Add(g.Admin.Pos, "listen and admin are the same port %d", g.Admin.Number)
	}
	// Envoy would accept a gateway with no service or no route and answer 404 to every request.
	if len(g.Services) == 0 {
		r.Add(g.ServicesPos, "no service is declared")
	}
	if len(g.Routes) == 0 {
		r.Add(g.RoutesPos, "the routes list is empty")
	}
	// Every reference to a service needs a port of its protocol (service below refuses one without), so nothing can
	// send a request to a service that declares none.
	for _, service := range g.Services {
		if len(service.Ports) == 0 {
			r.Add(service.Pos, "service %q declares no port", service.Name)
		}
	}
	return r.Sorted()
}

func (r *rules) routes() {
	first := r.g.RPCEntry()
	var plain []*model.Route
	for i := range r.g.Routes {
		route := &r.g.Routes[i]
		if route.RPC == nil {
			r.route(route, plain)
			plain = append(plain, route)
			continue
		}
		if route != first {
			r.Add(route.Pos, "the routes have more than one rpc entry (the first is %s)", first.Pos.Where())
		}
		// With no target the entry sends no RPC method anywhere, the same mistake as a routes list with nothing in it.
		if len(route.RPC.Targets) == 0 {
			r.Add(route.RPC.ToPos, "rpc: the to mapping is empty")
		}
		for _, target := range route.RPC.Targets {
			r.service("rpc:", target.Service, model.GRPC)
		}
	}
}

// route checks one plain route against itself and against the plain routes written before it.
func (r *rules) route(route *model.Route, earlier []*model.Route) {
	name := route.Name.Value
	r.service(fmt.Sprintf("route %q:", name), route.To, model.HTTP)
	if name == "rpc" {
		r.Add(route.Name.Pos, "a route may not be named rpc: that name marks the place of the RPC routes")
	}
	// A route's name is what the request log writes for its requests (route: "%ROUTE_NAME%" in
	// internal/envoy/fragments/log-request.yaml), so two routes with one name could not be told apart there.
	if i := slices.IndexFunc(earlier, func(e *model.Route) bool { return e.Name.Value == name }); i >= 0 {
		r.Add(route.Name.Pos, "route %q is declared twice (the first is %s)", name, earlier[i].Name.Pos.Where())
	}
	// Routes are tried in the order written, so a later route with the same match is never reached.
	sameMatch := func(e *model.Route) bool {
		return e.Match == route.Match && e.Value.Value == route.Value.Value && e.Method == route.Method
	}
	if i := slices.IndexFunc(earlier, sameMatch); i >= 0 {
		r.Add(route.Pos, "route %q matches what route %q %s matches", name, earlier[i].Name.Value, earlier[i].Pos.Where())
	}
	if route.AuthOff != nil && r.g.Auth == nil {
		r.Add(route.AuthOff.Pos, "route %q turns auth off, but no auth is declared", name)
	}
	if route.Match != model.Pattern {
		return
	}
	if route.Rewrite != nil {
		r.Add(route.Rewrite.Pos, "route %q: rewrite works with prefix or path, not with pattern", name)
	}
	// Envoy's safe_regex is RE2, whose syntax Go's regexp accepts except for \C.
	if _, err := regexp.Compile(route.Value.Value); err != nil {
		r.Add(route.Value.Pos, "route %q: the pattern does not compile: %v", name, err)
	}
}

// service reports a reference to a service that is not declared, or that has no port for the protocol the
// reference needs. subject starts the message, naming what refers to the service.
func (r *rules) service(subject string, ref model.Text, protocol model.Protocol) {
	service := r.g.Service(ref.Value)
	if service == nil {
		r.Add(ref.Pos, "%s the service %q is not declared", subject, ref.Value)
		return
	}
	if _, ok := service.Port(protocol); !ok {
		r.Add(ref.Pos, "%s the service %q has no %s port", subject, ref.Value, protocol)
	}
}
