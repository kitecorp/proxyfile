package read

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/internal/format"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/tree"
)

// decoder fills the model from a tree format.Check accepted, so every key it finds has the kind and the form the
// description asks for, and a key it does not find was not written. Four things the check cannot see are left to
// it: an address that is not an IP literal, a size or a time that matches its pattern but does not fit in an int64
// (ParseSize and ParseTime both refuse one; 9999999999999h is a time of that kind), a size or a time above the
// largest value Envoy accepts in the field it is written to, which Envoy refuses when it loads the file, and a tracing
// agent that matches its pattern but is not an IPv4 address and a port from 1 to 65535 (300.1.2.3:2000 is one). A
// pattern cannot say how large a number with a unit may be; the whole numbers' limits are each field's Max in the
// description, which the check applies.
type decoder struct {
	model.Problems
}

// Envoy 1.40.2 refuses a max_request_headers_kb above 8192 (http_connection_manager.proto, lte 8192), which the
// generator writes as limits.request_headers.size in KiB. 8192 KiB is a whole 8 MiB, so the message writes the limit
// in MiB from this constant, and the two cannot differ.
const largestRequestHeadersBytes = 8192 << 10

// Envoy 1.40.2 refuses a duration of 9223372036 seconds or more in any field, so that its seconds and nanoseconds
// still fit in an int64 count of nanoseconds (source/common/protobuf/utility.cc, kMaxSecondsValue, lines 38-39 and
// 50-52), far below the 315,576,000,000 seconds google/protobuf/duration.proto allows. auth.timeout is written to the
// ext_authz server_uri's timeout, which Envoy holds below 4294967296 seconds (api/envoy/config/core/v3/http_uri.proto,
// lines 53-57, HttpUri.timeout). Each limit is the largest whole number of seconds below those.
const (
	largestTimeSeconds        = 9223372035
	largestAuthTimeoutSeconds = 4294967295
)

// lowerTimeBounds names, by path, the time fields Envoy holds below largestTimeSeconds. A time field not named here
// takes largestTimeSeconds, so a new time field in the description is bounded without a change here.
var lowerTimeBounds = map[string]int64{"auth.timeout": largestAuthTimeoutSeconds}

func decode(root *tree.Node) (*model.Gateway, []model.Problem) {
	d := decoder{}
	g := &model.Gateway{
		Namespace:         cmp.Or(root.GetText("namespace"), "proxyfile"),
		Listen:            *port(root.Get("listen")),
		Admin:             port(root.Get("admin")),
		Limits:            d.limits(root),
		Timeouts:          d.timeouts(root),
		Services:          d.services(root.Get("services")),
		ServicesPos:       root.Entry("services").KeyPos,
		Auth:              d.auth(root),
		RemoveFromClients: d.headers(root.Get("remove_from_clients")),
		Origins:           textAt(root.Get("origins")),
		Routes:            d.routes(root.Get("routes")),
		RoutesPos:         root.Entry("routes").KeyPos,
		Log:               logSettings(root.Entry("log")),
		Tracing:           d.tracing(root.Get("tracing")),
	}
	if problems := d.Sorted(); problems != nil {
		return nil, problems
	}
	return g, nil
}

func (d *decoder) limits(root *tree.Node) model.Limits {
	limits := model.Limits{Heap: d.size(root, "limits", "heap"), Connections: count(root.Get("limits").Get("connections"))}
	if requestHeaders := root.Get("limits").Get("request_headers"); requestHeaders != nil {
		limits.RequestHeaders = &model.RequestHeaders{Count: *count(requestHeaders.Get("count"))}
		if size := d.size(root, "limits", "request_headers", "size"); size != nil {
			if size.Bytes > largestRequestHeadersBytes {
				d.Add(size.Pos, "limits.request_headers.size must be at most %dMiB", largestRequestHeadersBytes>>20)
			}
			limits.RequestHeaders.Size = *size
		}
	}
	return limits
}

func (d *decoder) timeouts(root *tree.Node) model.Timeouts {
	return model.Timeouts{
		Request:      d.time(root, "timeouts", "request"),
		Idle:         d.time(root, "timeouts", "idle"),
		UpstreamIdle: d.time(root, "timeouts", "upstream_idle"),
	}
}

func (d *decoder) services(n *tree.Node) []model.Service {
	var services []model.Service
	for _, entry := range n.Entries {
		service := model.Service{
			Name:       entry.Key,
			Pos:        entry.KeyPos,
			Address:    model.Text{Value: "127.0.0.1"},
			HostHeader: entry.Value.GetText("host_header"),
		}
		// Ranged rather than looked up, because the clusters follow the order the ports were written in.
		for _, key := range entry.Value.Entries {
			if slices.ContainsFunc(format.ServicePorts, func(f format.Field) bool { return f.Name == key.Key }) {
				service.Ports = append(service.Ports, model.ServicePort{Protocol: model.Protocol(key.Key), Number: number(key.Value), Pos: key.Value.Pos})
			}
		}
		if address := entry.Value.Get("address"); address != nil {
			if _, err := netip.ParseAddr(address.Text); err != nil {
				d.Add(address.Pos, "service %q: the address %q is not an IP address", entry.Key, address.Text)
			}
			service.Address = textAt(address)
		}
		services = append(services, service)
	}
	return services
}

func (d *decoder) auth(root *tree.Node) *model.Auth {
	n := root.Get("auth")
	if n == nil {
		return nil
	}
	ask := n.Get("ask")
	// The check's pattern for ask is one service name, one space and a path.
	service, path, _ := strings.Cut(ask.Text, " ")
	auth := &model.Auth{
		Service:        model.Text{Value: service, Pos: ask.Pos},
		Path:           path,
		Enabled:        n.Get("enabled").Text == "true",
		Allow:          n.Get("when_unavailable").Text == "allow",
		OnlyWithHeader: n.GetText("only_with_header"),
		Send:           d.headers(n.Get("send")),
		Accept:         d.headers(n.Get("accept")),
	}
	if timeout := d.time(root, "auth", "timeout"); timeout != nil {
		auth.Timeout = *timeout
	}
	return auth
}

func (d *decoder) routes(n *tree.Node) []model.Route {
	var list []model.Route
	for _, item := range n.Items {
		if rpc := item.Get("rpc"); rpc != nil {
			list = append(list, model.Route{Pos: item.Pos, RPC: rpcEntry(rpc)})
			continue
		}
		route := model.Route{
			Pos:     item.Pos,
			Name:    textAt(item.Get("name")),
			Method:  item.GetText("method"),
			To:      textAt(item.Get("to")),
			Rewrite: optionalText(item.Get("rewrite")),
			AuthOff: optionalText(item.Get("auth")),
			Retries: number(item.Get("retries")),
		}
		// The check lets exactly one of them through.
		for _, kind := range format.MatchKinds {
			if value := item.Get(kind); value != nil {
				route.Match, route.Value = model.MatchKind(kind), textAt(value)
			}
		}
		list = append(list, route)
	}
	return list
}

func rpcEntry(n *tree.Node) *model.RPC {
	rpc := &model.RPC{Retries: number(n.Get("retries")), ToPos: n.Entry("to").KeyPos}
	for _, entry := range n.Get("to").Entries {
		// A target is a service name, or a mapping with the service and the routes' name.
		service := entry.Value
		if entry.Value.Kind == tree.Map {
			service = entry.Value.Get("service")
		}
		rpc.Targets = append(rpc.Targets, model.RPCTarget{
			Pattern:   entry.Key,
			Service:   textAt(service),
			RouteName: cmp.Or(entry.Value.GetText("name"), service.Text+"-rpc"),
			Pos:       entry.KeyPos,
		})
	}
	return rpc
}

func logSettings(entry *tree.Entry) *model.Log {
	if entry == nil {
		return nil
	}
	return &model.Log{StatusFrom: number(entry.Value.Get("status_from")), SamplePercent: percent(entry.Value.Get("sample")), Pos: entry.KeyPos}
}

// percent reads a percentage the check accepted. ParsePercent compiles the pattern the check matched, and every whole
// percentage fits in an int, so it cannot refuse the text here.
func percent(n *tree.Node) int {
	value, _ := model.ParsePercent(n.Text)
	return value
}

// tracing fills the defaults the description's Docs name. The check let through only the xray provider, so it is
// read without asking which provider was written.
func (d *decoder) tracing(n *tree.Node) *model.Tracing {
	if n == nil {
		return nil
	}
	xray := n.Get("provider").Get("xray")
	tracing := &model.Tracing{
		XRay:      model.XRay{Address: "127.0.0.1", Port: 2000, Segment: xray.GetText("segment"), Sample: sample(xray.Get("sample"))},
		PathInURL: n.GetText("path_in_url") == "true",
	}
	if agent := xray.Get("agent"); agent != nil {
		address, port, ok := model.ParseAgent(agent.Text)
		if !ok {
			d.Add(agent.Pos, "%q must be %s", "agent", format.AgentExpect)
		}
		tracing.XRay.Address, tracing.XRay.Port = address, port
	}
	// Ranged rather than looked up, because the segment's tags follow the order they were written in.
	if tags := n.Get("tags"); tags != nil {
		for _, entry := range tags.Entries {
			tracing.Tags = append(tracing.Tags, model.Tag{
				Name:    entry.Key,
				Env:     entry.Value.GetText("env"),
				Header:  entry.Value.GetText("header"),
				Default: entry.Value.GetText("default"),
			})
		}
	}
	return tracing
}

// sample is nil when sample is not written, so the generator writes no manifest and Envoy's own rule applies.
func sample(n *tree.Node) *model.Sample {
	if n == nil {
		return nil
	}
	s := &model.Sample{Default: model.SampleRule{First: 1, RatePercent: 5}}
	if first := n.Get("first"); first != nil {
		s.Default.First = number(first)
	}
	if rate := n.Get("rate"); rate != nil {
		s.Default.RatePercent = percent(rate)
	}
	if rules := n.Get("rules"); rules != nil {
		for _, item := range rules.Items {
			s.Rules = append(s.Rules, model.SampleRule{
				Host:        cmp.Or(item.GetText("host"), "*"),
				Method:      cmp.Or(item.GetText("method"), "*"),
				Path:        cmp.Or(item.GetText("path"), "*"),
				First:       number(item.Get("first")),
				RatePercent: percent(item.Get("rate")),
			})
		}
	}
	return s
}

// size and time take one path to the value and, through tooLarge, to its description, so a problem quotes the rule
// of the key it names.
func (d *decoder) size(root *tree.Node, path ...string) *model.Size {
	n := at(root, path)
	if n == nil {
		return nil
	}
	bytes, ok := model.ParseSize(n.Text)
	if !ok {
		d.tooLarge(n, path)
		return nil
	}
	return &model.Size{Bytes: bytes, Pos: n.Pos}
}

// time reads none as 0, which the model takes as no time limit. The one time that must not be none, auth's timeout,
// has a pattern without none, so the check has already refused it there. Every time field is read here, and the bound
// is chosen here from the path, so no caller can read a time without the bound of its field.
func (d *decoder) time(root *tree.Node, path ...string) *model.Time {
	n := at(root, path)
	if n == nil {
		return nil
	}
	millis, ok := model.ParseTime(n.Text)
	if !ok {
		d.tooLarge(n, path)
		return nil
	}
	name := strings.Join(path, ".")
	if largestSeconds := cmp.Or(lowerTimeBounds[name], largestTimeSeconds); millis > largestSeconds*1000 {
		d.Add(n.Pos, "%s must be at most %ds", name, largestSeconds)
	}
	return &model.Time{Millis: millis, Pos: n.Pos}
}

// tooLarge reports a value the check accepted but the parser refused, in the words the check uses for a value that
// does not match, read from the same description.
func (d *decoder) tooLarge(n *tree.Node, path []string) {
	spec := &format.Proxyfile
	for _, key := range path {
		spec = spec.Child(key)
	}
	d.Add(n.Pos, "%q must be %s", path[len(path)-1], spec.Expect)
}

func at(root *tree.Node, path []string) *tree.Node {
	n := root
	for _, key := range path {
		n = n.Get(key)
	}
	return n
}

func (d *decoder) headers(n *tree.Node) []model.Header {
	if n == nil {
		return nil
	}
	list := make([]model.Header, 0, len(n.Items))
	for _, item := range n.Items {
		name, prefix := strings.CutSuffix(item.Text, "*")
		list = append(list, model.Header{Name: name, Prefix: prefix, Pos: item.Pos})
	}
	return list
}

// number reads a whole number the check accepted, which it tested with the same strconv.ParseInt, so the error is
// never set; a key that was not written is 0. It reads 64 bits whatever the platform's int, as the check does, so a
// uint32 limit such as 4294967295 is not cut short on a 32-bit build.
func number(n *tree.Node) int64 {
	if n == nil {
		return 0
	}
	value, _ := strconv.ParseInt(n.Text, 10, 64)
	return value
}

// port and count are nil for a key that was not written; a required key is always there.
func port(n *tree.Node) *model.Port {
	if n == nil {
		return nil
	}
	return &model.Port{Number: number(n), Pos: n.Pos}
}

func count(n *tree.Node) *model.Count {
	if n == nil {
		return nil
	}
	return &model.Count{Number: number(n), Pos: n.Pos}
}

func textAt(n *tree.Node) model.Text {
	return model.Text{Value: n.Text, Pos: n.Pos}
}

func optionalText(n *tree.Node) *model.Text {
	if n == nil {
		return nil
	}
	value := textAt(n)
	return &value
}
