package read

import (
	"cmp"
	"math"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// decoder fills the model from a tree format.Check accepted, so every key it finds has the kind and the form the
// description asks for, and a key it does not find was not written. What the check cannot see is left to it: a
// service's address that is neither an IP address nor a DNS name, address and addresses written together, addresses
// with fewer than two items, an item of addresses that is not an IP address or is written twice, a health interval
// below 1s, tracing written both at the top and at envoy.tracing, a dns.resolver that is not an IP address with an
// optional port, a size or a time that matches its pattern but does not fit in an int64 (ParseSize and ParseTime both
// refuse one; 9999999999999h is a time of that kind), a size or a time above the largest value Envoy accepts in the
// field it is written to, which Envoy refuses when it loads the file, a tracing agent that matches its pattern but is
// not an IPv4 address and a port from 1 to 65535 (300.1.2.3:2000 is one), an otel endpoint that is not an IP address or
// a DNS name, and a port from 1 to 65535, a rate limit whose per is below 1s or whose requests and burst together are
// above a uint32, an auth that writes neither ask nor jwt, a key of the check written without ask or missing beside
// it, a jwt keys URL that is not https to an IP address or a DNS name, a jwt ca beside a keys file, an empty jwt
// audiences list, a jwt claims header that an entry of send or accept covers, and a header carrying the token that is
// cookie, a claims header or one a remove_from_clients entry covers. A pattern cannot say how large a number with a
// unit may be; the whole numbers' limits are each field's Max in the description, which the check applies.
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
	tls := listenerTLS(root.Get("tls"))
	g := &model.Gateway{
		Listen:            *port(root.Get("listen")),
		Admin:             port(root.Get("admin")),
		TLS:               tls,
		ClientAddress:     clientAddress(root.Get("client_address"), tls != nil),
		Limits:            d.limits(root),
		Timeouts:          d.timeouts(root),
		Services:          d.services(root.Get("services")),
		ServicesPos:       root.Entry("services").KeyPos,
		Resolver:          d.resolver(root),
		Auth:              d.auth(root),
		JWT:               d.jwt(root),
		RemoveFromClients: d.headers(root.Get("remove_from_clients")),
		Origins:           textAt(root.Get("origins")),
		Routes:            d.routes(root.Get("routes")),
		RoutesPos:         root.Entry("routes").KeyPos,
		Log:               logSettings(root.Entry("log")),
		Tracing:           d.tracing(cmp.Or(root.Get("tracing"), root.Get("envoy").Get("tracing"))),
		Envoy:             d.envoy(root),
	}
	// envoy.tracing is the old place of tracing and holds the same keys. A file that writes both would have one of them
	// left unread, so it is refused.
	if old := root.Get("envoy").Entry("tracing"); old != nil && root.Get("tracing") != nil {
		d.Add(old.KeyPos, "write tracing or envoy.tracing, not both")
	}
	d.claims(g.Auth, root.Get("auth").Get("jwt").Get("claims"))
	d.tokenHeader(g, root.Get("auth").Get("jwt"))
	if problems := d.Sorted(); problems != nil {
		return nil, problems
	}
	return g, nil
}

// listenerTLS is nil when tls is not written, so the public listener serves plain HTTP. The check has made each path
// absolute and required certificate, key and, with clients, ca.
func listenerTLS(n *tree.Node) *model.TLS {
	if n == nil {
		return nil
	}
	return &model.TLS{Certificate: n.GetText("certificate"), Key: n.GetText("key"), ClientsCA: n.Get("clients").GetText("ca")}
}

// clientAddress is the written client_address, else connection when the file writes tls and forwarded otherwise. A
// gateway that terminates TLS usually faces its clients, and forwarded there would let each client choose its own
// address by sending X-Forwarded-For; every file without tls keeps forwarded, 2.0's behaviour.
func clientAddress(n *tree.Node, tls bool) model.ClientAddress {
	switch {
	case n != nil:
		return model.ClientAddress(n.Text)
	case tls:
		return model.ClientConnection
	default:
		return model.ClientForwarded
	}
}

func (d *decoder) limits(root *tree.Node) model.Limits {
	limits := model.Limits{Connections: count(root.Get("limits").Get("connections"))}
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

// envoy reads the envoy block, which holds what only the Envoy generator reads.
func (d *decoder) envoy(root *tree.Node) model.Envoy {
	n := root.Get("envoy")
	return model.Envoy{
		Namespace:     cmp.Or(n.GetText("namespace"), "proxyfile"),
		Heap:          d.size(root, "envoy", "heap"),
		ConnectBridge: n.GetText("connect_bridge") != "false",
	}
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
			Balance:    model.Text{Value: "round_robin"},
			HostHeader: entry.Value.GetText("host_header"),
		}
		// Ranged rather than looked up, because the clusters follow the order the ports were written in.
		for _, key := range entry.Value.Entries {
			if slices.ContainsFunc(format.ServicePorts, func(f format.Field) bool { return f.Name == key.Key }) {
				service.Ports = append(service.Ports, model.ServicePort{Protocol: model.Protocol(key.Key), Number: number(key.Value), Pos: key.Value.Pos})
			}
		}
		service.Addresses, service.DNS = d.backends(entry)
		if balance := entry.Value.Get("balance"); balance != nil {
			service.Balance = textAt(balance)
		}
		if hash := entry.Value.Entry("hash"); hash != nil {
			service.Hash = &model.Hash{Header: hash.Value.GetText("header"), Client: hash.Value.Get("client") != nil, Pos: hash.KeyPos}
		}
		service.Health = d.health(entry.Value.Entry("health"))
		services = append(services, service)
	}
	return services
}

// health reads a service's checks with the defaults the description's Docs name: every 5s, within 2s, 2 passes in a
// row to join the pool and 3 failures to leave it. Whether path or grpc is written, and fits the service's ports, is
// the meaning rules' to say.
func (d *decoder) health(entry *tree.Entry) *model.Health {
	if entry == nil {
		return nil
	}
	n := entry.Value
	health := &model.Health{
		Path:      optionalText(n.Get("path")),
		GRPC:      optionalText(n.Get("grpc")),
		Interval:  model.Time{Millis: 5000},
		Timeout:   model.Time{Millis: 2000},
		Healthy:   2,
		Unhealthy: 3,
		Pos:       entry.KeyPos,
	}
	// An interval below a second is refused, so a mistyped 2ms cannot send hundreds of checks a second to each backend.
	if interval := d.timeOf(n.Get("interval"), "health.interval", format.Health.Child("interval").Expect); interval != nil {
		if interval.Millis < 1000 {
			d.Add(interval.Pos, "health.interval must be at least 1s")
		}
		health.Interval = *interval
	}
	if timeout := d.timeOf(n.Get("timeout"), "health.timeout", format.Health.Child("timeout").Expect); timeout != nil {
		health.Timeout = *timeout
	}
	if healthy := n.Get("healthy"); healthy != nil {
		health.Healthy = number(healthy)
	}
	if unhealthy := n.Get("unhealthy"); unhealthy != nil {
		health.Unhealthy = number(unhealthy)
	}
	return health
}

// backends reads a service's address or addresses: 127.0.0.1 when neither is written. dns is true when address is a
// DNS name, which the proxy looks up while it runs. addresses holds IP addresses alone, because a name there would
// give a backend list that changes under the others; they are compared as addresses, so two spellings of one address
// are one backend written twice.
func (d *decoder) backends(entry tree.Entry) (backends []model.Text, dns bool) {
	address, addresses := entry.Value.Get("address"), entry.Value.Entry("addresses")
	switch {
	case address != nil && addresses != nil:
		d.Add(addresses.KeyPos, "service %q: write address or addresses, not both", entry.Key)
		return nil, false
	case address != nil:
		if _, err := netip.ParseAddr(address.Text); err == nil {
			return []model.Text{textAt(address)}, false
		}
		if model.IsDNSName(address.Text) {
			return []model.Text{textAt(address)}, true
		}
		if withPort(address.Text) {
			d.Add(address.Pos, "service %q: the address %q has a port; the port goes in http or grpc", entry.Key, address.Text)
			return nil, false
		}
		d.Add(address.Pos, "service %q: the address %q is not an IP address or a DNS name", entry.Key, address.Text)
		return nil, false
	case addresses != nil:
		items := addresses.Value.Items
		if len(items) < 2 {
			d.Add(addresses.Value.Pos, "service %q: addresses needs two or more addresses; write one in address", entry.Key)
		}
		first := map[netip.Addr]model.Pos{}
		for _, item := range items {
			backends = append(backends, textAt(item))
			addr, err := netip.ParseAddr(item.Text)
			if err != nil && withPort(item.Text) {
				d.Add(item.Pos, "service %q: %q in addresses has a port; the port goes in http or grpc", entry.Key, item.Text)
				continue
			}
			if err != nil {
				d.Add(item.Pos, "service %q: %q in addresses is not an IP address; a DNS name goes in address", entry.Key, item.Text)
				continue
			}
			addr = addr.Unmap()
			if earlier, twice := first[addr]; twice {
				d.Add(item.Pos, "service %q: the address %q is written twice in addresses (the first %s)", entry.Key, item.Text, earlier.Where())
				continue
			}
			first[addr] = item.Pos
		}
		return backends, false
	default:
		return []model.Text{{Value: "127.0.0.1"}}, false
	}
}

// withPort says whether text is a host followed by a port, the form other proxies write a backend in. A service's
// ports are its http and grpc keys, so the problem says where the port goes.
func withPort(text string) bool {
	host, port, err := net.SplitHostPort(text)
	return err == nil && host != "" && port != ""
}

// resolver is nil when dns.resolver is not written, so each cluster that looks a name up asks the host's resolver
// configuration.
func (d *decoder) resolver(root *tree.Node) *model.Resolver {
	n := root.Get("dns").Get("resolver")
	if n == nil {
		return nil
	}
	address, port, ok := model.ParseResolver(n.Text)
	if !ok {
		d.tooLarge(n, []string{"dns", "resolver"})
		return nil
	}
	return &model.Resolver{Address: address, Port: port}
}

// auth reads the check, nil when ask is not written: auth.jwt alone asks no service. auth writes ask, jwt or both, and
// the keys that set the check are required with ask and refused without it, in the check's own words where it would
// say them itself.
func (d *decoder) auth(root *tree.Node) *model.Auth {
	n := root.Get("auth")
	if n == nil {
		return nil
	}
	ask := n.Get("ask")
	if ask == nil {
		if n.Get("jwt") == nil {
			d.Add(n.Pos, "auth needs ask or jwt")
		}
		for _, key := range format.CheckKeys {
			if entry := n.Entry(key); entry != nil {
				d.Add(entry.KeyPos, "auth: %s is written, but ask is not", key)
			}
		}
		return nil
	}
	for _, key := range format.CheckRequired {
		if n.Get(key) == nil {
			d.Add(n.Pos, "%q is required here", key)
		}
	}
	// The check's pattern for ask is one service name, one space and a path.
	service, path, _ := strings.Cut(ask.Text, " ")
	auth := &model.Auth{
		Service:        model.Text{Value: service, Pos: ask.Pos},
		Path:           path,
		Enabled:        n.GetText("enabled") == "true",
		Allow:          n.GetText("when_unavailable") == "allow",
		OnlyWithHeader: n.GetText("only_with_header"),
		Send:           d.headers(n.Get("send")),
		Accept:         d.headers(n.Get("accept")),
		SendRequest:    n.GetText("send_request") == "true",
	}
	if timeout := d.time(root, "auth", "timeout"); timeout != nil {
		auth.Timeout = *timeout
	}
	return auth
}

// systemCA is the CA bundle a keys URL's certificate must chain to when auth.jwt.ca is not written: the one bundle in
// Envoy's own image (measured on Envoy 1.40.2), at the path Debian, Ubuntu and Alpine give theirs.
const systemCA = "/etc/ssl/certs/ca-certificates.crt"

// jwt reads auth.jwt, nil when not written, with the defaults the description's Docs name: the authorization header,
// and with a keys URL the system CA bundle and port 443. The check has made keys an absolute path or a text starting
// with https://, and audiences a list or any.
func (d *decoder) jwt(root *tree.Node) *model.JWT {
	n := root.Get("auth").Get("jwt")
	if n == nil {
		return nil
	}
	jwt := &model.JWT{
		Issuer:   n.GetText("issuer"),
		Header:   cmp.Or(n.GetText("header"), "authorization"),
		Optional: n.GetText("optional") == "true",
	}
	// An empty list would accept no audience at all, so every token would be refused; any says every audience.
	if audiences := n.Get("audiences"); audiences.Kind == tree.List {
		if len(audiences.Items) == 0 {
			d.Add(audiences.Pos, "auth.jwt: audiences needs one audience or more; any accepts every audience")
		}
		for _, item := range audiences.Items {
			jwt.Audiences = append(jwt.Audiences, item.Text)
		}
	}
	keys := n.Get("keys")
	if strings.HasPrefix(keys.Text, "/") {
		jwt.File = keys.Text
		// The bundle checks the keys URL's certificate, so beside a file it would check nothing.
		if ca := n.Entry("ca"); ca != nil {
			d.Add(ca.KeyPos, "auth.jwt: ca is written, but keys is not a URL")
		}
	} else {
		host, port, dns, ok := model.ParseKeysURL(keys.Text)
		if !ok {
			d.Add(keys.Pos, `"keys" must be an https URL of an IP address or a DNS name, with no user part, and with a port from 1 to 65535 when it writes one`)
		}
		jwt.URL, jwt.Host, jwt.Port, jwt.DNS, jwt.CA = keys.Text, host, port, dns, cmp.Or(n.GetText("ca"), systemCA)
	}
	// Ranged rather than looked up, because the headers are set in the order the claims were written in.
	if claims := n.Get("claims"); claims != nil {
		for _, entry := range claims.Entries {
			jwt.Claims = append(jwt.Claims, model.Claim{Header: entry.Key, Name: entry.Value.Text})
		}
	}
	return jwt
}

// claims refuses a claims header that an entry of send or accept covers. The token filter sets a claims header before
// the check runs, and the early header removal takes a client's copy, so a header send names would reach the check
// as the claim, never as the client's; and the check's answer to a header accept names would replace the verified
// claim. Without ask, send and accept are refused already, so the rule needs the check.
func (d *decoder) claims(check *model.Auth, claims *tree.Node) {
	if check == nil || claims == nil {
		return
	}
	for _, entry := range claims.Entries {
		header := model.Header{Name: entry.Key}
		for _, list := range []struct {
			key     string
			entries []model.Header
		}{{"send", check.Send}, {"accept", check.Accept}} {
			if slices.ContainsFunc(list.entries, func(e model.Header) bool { return e.Covers(header) }) {
				d.Add(entry.KeyPos, "auth.jwt.claims: %q is a header %s names", entry.Key, list.key)
			}
		}
	}
}

// tokenHeader refuses a file in which no request can carry a token. The early header removal takes every claims header
// and every remove_from_clients entry from the client's request before the token filter reads it, so the header that
// carries the token can be neither. cookie is refused too: the filter reads a header other than authorization whole,
// as the bare token, and a Cookie header holds name=value pairs.
func (d *decoder) tokenHeader(g *model.Gateway, jwt *tree.Node) {
	if g.JWT == nil {
		return
	}
	header := model.Header{Name: g.JWT.Header}
	if written := jwt.Get("header"); written != nil && written.Text == "cookie" {
		d.Add(written.Pos, "auth.jwt: header cannot be cookie; a token inside a cookie is not read")
	}
	if claims := jwt.Get("claims"); claims != nil {
		for _, entry := range claims.Entries {
			if entry.Key == header.Name {
				d.Add(entry.KeyPos, "auth.jwt.claims: %q is the header that carries the token", entry.Key)
			}
		}
	}
	for _, entry := range g.RemoveFromClients {
		if entry.Covers(header) {
			d.Add(entry.Pos, "remove_from_clients: this entry removes %q, the header that carries the token of auth.jwt", header.Name)
		}
	}
}

func (d *decoder) routes(n *tree.Node) []model.Route {
	var list []model.Route
	for _, item := range n.Items {
		if rpc := item.Get("rpc"); rpc != nil {
			list = append(list, model.Route{Pos: item.Pos, RPC: d.rpcEntry(rpc)})
			continue
		}
		route := model.Route{
			Pos:       item.Pos,
			Name:      textAt(item.Get("name")),
			Method:    item.GetText("method"),
			To:        textAt(item.Get("to")),
			Rewrite:   optionalText(item.Get("rewrite")),
			AuthOff:   optionalText(item.Get("auth")),
			Retries:   number(item.Get("retries")),
			RateLimit: d.rateLimit(item.Get("rate_limit")),
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

// The bucket holds requests + burst tokens, which the generator writes to the token bucket's max_tokens, a uint32 in
// Envoy 1.40.2 (token_bucket.proto); the check holds each of the two alone to that limit.
const largestBucketTokens = math.MaxUint32

// rateLimit reads a route's or the rpc entry's limit with the defaults the description's Docs name: no burst, and each
// client address counted on its own. A per below a second is refused, though Envoy takes a fill interval down to 50ms
// (local_ratelimit_impl.cc): nginx's limit_req writes a rate per second or per minute, so it could not carry one.
func (d *decoder) rateLimit(n *tree.Node) *model.RateLimit {
	if n == nil {
		return nil
	}
	limit := &model.RateLimit{Requests: number(n.Get("requests")), Burst: number(n.Get("burst")), ByClient: n.GetText("by") != "all"}
	if per := d.timeOf(n.Get("per"), "rate_limit.per", format.RateLimit.Child("per").Expect); per != nil {
		if per.Millis < 1000 {
			d.Add(per.Pos, "rate_limit.per must be at least 1s")
		}
		limit.Per = *per
	}
	if burst := n.Get("burst"); burst != nil && limit.Requests+limit.Burst > largestBucketTokens {
		d.Add(burst.Pos, "rate_limit: requests and burst together must be at most %d", largestBucketTokens)
	}
	return limit
}

func (d *decoder) rpcEntry(n *tree.Node) *model.RPC {
	rpc := &model.RPC{Retries: number(n.Get("retries")), RateLimit: d.rateLimit(n.Get("rate_limit")), ToPos: n.Entry("to").KeyPos}
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

// tracing fills the defaults the description's Docs name. The check let through a provider with exactly one key, xray
// or otel (envoy.tracing takes xray alone), so the model holds the one written.
func (d *decoder) tracing(n *tree.Node) *model.Tracing {
	if n == nil {
		return nil
	}
	tracing := &model.Tracing{PathInURL: n.GetText("path_in_url") == "true"}
	if otel := n.Get("provider").Get("otel"); otel != nil {
		tracing.OTel = d.otel(otel)
	} else {
		tracing.XRay = d.xray(n.Get("provider").Get("xray"))
	}
	// Ranged rather than looked up, because the span's tags follow the order they were written in.
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

// xray sends the segments to 127.0.0.1:2000 when agent is not written.
func (d *decoder) xray(n *tree.Node) *model.XRay {
	xray := &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: n.GetText("segment"), Sample: sample(n.Get("sample"))}
	if agent := n.Get("agent"); agent != nil {
		address, port, ok := model.ParseAgent(agent.Text)
		if !ok {
			d.Add(agent.Pos, "%q must be %s", "agent", format.AgentExpect)
		}
		xray.Address, xray.Port = address, port
	}
	return xray
}

// otel traces 5% of the requests when sample.rate is not written.
func (d *decoder) otel(n *tree.Node) *model.OTel {
	otel := &model.OTel{Service: n.GetText("service"), RatePercent: 5}
	endpoint := n.Get("endpoint")
	host, port, dns, ok := model.ParseEndpoint(endpoint.Text)
	if !ok {
		d.Add(endpoint.Pos, "%q must be %s", "endpoint", format.EndpointExpect)
	}
	otel.Host, otel.Port, otel.DNS = host, port, dns
	if rate := n.Get("sample").Get("rate"); rate != nil {
		otel.RatePercent = percent(rate)
	}
	return otel
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

// time reads the time at path, with the bound and the Expect of its field read from the path.
func (d *decoder) time(root *tree.Node, path ...string) *model.Time {
	return d.timeOf(at(root, path), strings.Join(path, "."), expectAt(path))
}

// timeOf reads none as 0, which the model takes as no time limit. A time that must not be none has a pattern without
// none, so the check has already refused it there. Every time field is read here, and name, the field's path joined by
// dots, chooses its bound and starts the message of one above it, so no caller can read a time without the bound of
// its field. expect completes the message for a time the parser refuses, as the check words one that does not match.
func (d *decoder) timeOf(n *tree.Node, name, expect string) *model.Time {
	if n == nil {
		return nil
	}
	millis, ok := model.ParseTime(n.Text)
	if !ok {
		d.Add(n.Pos, "%q must be %s", name[strings.LastIndex(name, ".")+1:], expect)
		return nil
	}
	if largestSeconds := cmp.Or(lowerTimeBounds[name], largestTimeSeconds); millis > largestSeconds*1000 {
		d.Add(n.Pos, "%s must be at most %ds", name, largestSeconds)
	}
	return &model.Time{Millis: millis, Pos: n.Pos}
}

// tooLarge reports a value the check accepted but the parser refused, in the words the check uses for a value that
// does not match, read from the same description.
func (d *decoder) tooLarge(n *tree.Node, path []string) {
	d.Add(n.Pos, "%q must be %s", path[len(path)-1], expectAt(path))
}

// expectAt is the Expect of the key at path in the proxyfile's description.
func expectAt(path []string) string {
	spec := &format.Proxyfile
	for _, key := range path {
		spec = spec.Child(key)
	}
	return spec.Expect
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
