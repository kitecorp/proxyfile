// Package rpc reads the RPC methods file, gives each method the service the proxyfile's rpc entry maps it to, and
// writes the RPC table: the routes as JSON, one object per line.
package rpc

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/internal/format"
	"github.com/kitecorp/proxyfile/internal/input"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/quote"
	"github.com/kitecorp/proxyfile/internal/tree"
)

// Method is one entry of the methods file.
type Method struct {
	Service, Method, Action string    // Action is "" when not written
	Reports                 []string  // nil when not written, and when written as []
	Pos                     model.Pos // the entry's mapping
}

// Route is a method with the service it goes to.
type Route struct {
	Method
	Target  *model.Service
	Cluster string // Target.Cluster(model.GRPC)
	Name    string // the target's RouteName
}

// Path is the path a gRPC call of the method carries, which its route matches exactly.
func (r Route) Path() string {
	return grpcPath(r.Method)
}

func grpcPath(m Method) string {
	return "/" + m.Service + "/" + m.Method
}

// ReadMethods reads the methods file at path, which is JSON read as YAML so that a problem names its line. It
// returns the problems of the first step that finds any: reading the file, its syntax, then the format check. Every
// rule of the methods is Resolve's, the two that need no proxyfile included (a report holding a comma, a method
// listed twice), so one run names the problems of both kinds.
func ReadMethods(path string) ([]Method, []model.Problem) {
	root, problems := input.Checked(path, format.Methods)
	if problems != nil {
		return nil, problems
	}
	var methods []Method
	for _, item := range root.Items {
		methods = append(methods, Method{
			Service: item.Get("service").Text,
			Method:  item.Get("method").Text,
			Action:  item.GetText("action"),
			Reports: texts(item.Get("reports")),
			Pos:     item.Pos,
		})
	}
	return methods, nil
}

// texts is nil for a list that was not written and for an empty one, so a reader sees no difference between the two.
func texts(list *tree.Node) []string {
	if list == nil {
		return nil
	}
	var values []string
	for _, item := range list.Items {
		values = append(values, item.Text)
	}
	return values
}

// Resolve gives each method one target: an exact service name first, else the matching pattern with the longest
// text, else "*". Ranking exact names first settles the one tie length alone cannot (a.b.C against a.b.*); two
// different patterns that both match a service never have the same length, so the written order never matters.
//
// g is a gateway read.Proxyfile returned, whose rules have made every target name a declared service with a gRPC
// port. Resolve returns either the routes, sorted by path, or every problem it finds, sorted.
func Resolve(g *model.Gateway, methods []Method) ([]Route, []model.Problem) {
	var problems model.Problems
	entry := g.RPCEntry()
	if entry == nil {
		problems.Add(g.RoutesPos, "RPC methods are given, but the routes have no rpc entry")
	}
	// Only looked up, never ranged: the routes are sorted below.
	firstLine := map[string]int{}
	var routes []Route
	for _, method := range methods {
		path := grpcPath(method)
		if line, seen := firstLine[path]; seen {
			problems.Add(method.Pos, "the method %s/%s is listed twice (first on line %d)", method.Service, method.Method, line)
		} else {
			firstLine[path] = method.Pos.Line
		}
		// The route sends the reports to the check as one header value, joined by commas, so a report holding a comma
		// would arrive as two.
		for _, report := range method.Reports {
			if strings.Contains(report, ",") {
				problems.Add(method.Pos, "the report %q of %s/%s contains a comma, the separator of x-gateway-reports",
					report, method.Service, method.Method)
			}
		}
		if entry == nil {
			continue
		}
		target := targetOf(entry.RPC.Targets, method.Service)
		if target == nil {
			problems.Add(method.Pos, "no rpc mapping matches %s", method.Service)
			continue
		}
		service := g.Service(target.Service.Value)
		routes = append(routes, Route{Method: method, Target: service, Cluster: service.Cluster(model.GRPC), Name: target.RouteName})
	}
	if problems := problems.Sorted(); problems != nil {
		return nil, problems
	}
	// No two routes share a path, because a method listed twice is refused above.
	slices.SortFunc(routes, func(a, b Route) int { return strings.Compare(a.Path(), b.Path()) })
	return routes, nil
}

// targetOf tries the targets in the written order and keeps the best that matches service, or returns nil.
func targetOf(targets []model.RPCTarget, service string) *model.RPCTarget {
	var best *model.RPCTarget
	for i := range targets {
		target := &targets[i]
		if matches(target.Pattern, service) && (best == nil || better(target.Pattern, best.Pattern)) {
			best = target
		}
	}
	return best
}

// The format check admits three forms of pattern: "*", a package followed by ".*", and a full service name.
func matches(pattern, service string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(service, strings.TrimSuffix(pattern, "*"))
	default:
		return pattern == service
	}
}

// better reports whether pattern a ranks above pattern b, both matching one service: a full service name above
// any pattern, then the longer text.
func better(a, b string) bool {
	if aExact, bExact := !strings.HasSuffix(a, "*"), !strings.HasSuffix(b, "*"); aExact != bExact {
		return aExact
	}
	return len(a) > len(b)
}

// Table writes the routes, in the order given, as a JSON array with one object per line, so a diff of two tables
// shows one line for each route that changed. Resolve sorts the routes by path, so the table's order does not depend
// on the methods file's.
//
// Each string is written by quote.JSON, whose result equals what Jackson's writeValueAsString writes for any text
// with no control character and no U+2028 or U+2029: Go escapes those two always and writes \u00XX in lower case,
// Jackson leaves the two as they are and writes upper case. Both write <, > and & as they are.
func Table(routes []Route) []byte {
	if len(routes) == 0 {
		return []byte("[]\n")
	}
	lines := make([]string, len(routes))
	for i, route := range routes {
		reports := make([]string, len(route.Reports))
		for j, report := range route.Reports {
			reports[j] = quote.JSON(report)
		}
		lines[i] = fmt.Sprintf(`  {"service": %s, "method": %s, "action": %s, "reports": [%s], "cluster": %s}`,
			quote.JSON(route.Service), quote.JSON(route.Method.Method), quote.JSON(route.Action),
			strings.Join(reports, ", "), quote.JSON(route.Cluster))
	}
	return []byte("[\n" + strings.Join(lines, ",\n") + "\n]\n")
}
