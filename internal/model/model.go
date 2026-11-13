package model

import "strings"

// Gateway is one proxyfile after reading: every default filled in and every rule checked. Each value a rule or a
// later stage may name in a problem keeps the position it was written at; a default that was not written has no
// position, and nothing refuses a default.
type Gateway struct {
	Listen            Port
	Admin             *Port
	TLS               *TLS          // nil when not written: the public listener serves plain HTTP
	ClientAddress     ClientAddress // when not written, ClientConnection with TLS and ClientForwarded without
	Limits            Limits
	Timeouts          Timeouts
	Services          []Service // written order
	ServicesPos       Pos       // the `services` key
	Resolver          *Resolver // nil when not written
	Auth              *Auth     // the check, auth.ask; nil when ask is not written
	JWT               *JWT      // auth.jwt; nil when not written
	RemoveFromClients []Header
	Origins           Text    // the origins file as written, absolute or relative to the proxyfile's directory; "" when the proxyfile writes the origins
	Routes            []Route // written order
	RoutesPos         Pos     // the `routes` key
	Log               *Log
	Tracing           *Tracing // nil when neither tracing nor envoy.tracing is written
	Envoy             Envoy
}

// Envoy is the envoy block: what only the Envoy generator reads.
type Envoy struct {
	Namespace     string // "proxyfile" when not written
	Heap          *Size  // nil when not written
	ConnectBridge bool   // true when not written
}

// TLS is the public listener's certificate and key, and the CA a client's certificate must chain to when tls.clients
// is written. Each is an absolute path to a PEM file on the proxy's host.
type TLS struct {
	Certificate, Key string
	ClientsCA        string // "" without tls.clients: no client certificate is asked for
}

// ClientAddress says which address counts as the client's.
type ClientAddress string

const (
	// ClientForwarded is the last address a proxy in front appended to X-Forwarded-For.
	ClientForwarded ClientAddress = "forwarded"
	// ClientConnection is the peer of the TCP connection.
	ClientConnection ClientAddress = "connection"
)

type Port struct {
	Number int64
	Pos    Pos
}

type Text struct {
	Value string
	Pos   Pos
}

type Limits struct {
	Connections    *Count
	RequestHeaders *RequestHeaders
}

type Size struct {
	Bytes int64
	Pos   Pos
}

type Count struct {
	Number int64
	Pos    Pos
}

type RequestHeaders struct {
	Size  Size
	Count Count
}

type Timeouts struct {
	Request, Idle, UpstreamIdle *Time
}

type Time struct {
	Millis int64 // 0 is none
	Pos    Pos
}

type Protocol string

const (
	HTTP Protocol = "http"
	GRPC Protocol = "grpc"
)

type Service struct {
	Name string
	Pos  Pos // the service's name
	// Addresses are the backends: address, or 127.0.0.1 when neither address nor addresses is written, as one item,
	// or each of addresses in written order.
	Addresses  []Text
	DNS        bool // Addresses holds one DNS name, which the proxy looks up while it runs
	Balance    Text // "round_robin", without a position, when not written
	Hash       *Hash
	Health     *Health // nil when not written: every backend stays in the pool
	HostHeader string
	Ports      []ServicePort // written order
}

// Hash is what balance hash hashes: exactly one of Header and Client is set.
type Hash struct {
	Header string
	Client bool // the client's address
	Pos    Pos  // the hash key
}

// Health is the service's active checks. Path and GRPC are nil when not written; GRPC keeps the true for its position,
// as Route.AuthOff keeps off. Interval, Timeout, Healthy and Unhealthy are 5s, 2s, 2 and 3, without a position, when
// not written.
type Health struct {
	Path, GRPC         *Text
	Interval, Timeout  Time
	Healthy, Unhealthy int64
	Pos                Pos // the health key
}

// Resolver is the name server dns.resolver names.
type Resolver struct {
	Address string
	Port    int64 // 53 when not written
}

type ServicePort struct {
	Protocol Protocol
	Number   int64
	Pos      Pos
}

// Port returns the service's port for p; false when the service has none.
func (s *Service) Port(p Protocol) (ServicePort, bool) {
	for _, port := range s.Ports {
		if port.Protocol == p {
			return port, true
		}
	}
	return ServicePort{}, false
}

// Cluster names the cluster that carries p to the service, one per protocol: "<name>_<protocol>". Every stage that
// names a cluster asks here, so no two of them can spell it differently.
func (s *Service) Cluster(p Protocol) string {
	return s.Name + "_" + string(p)
}

type Auth struct {
	Service        Text // the service of `ask`, at the position of `ask`
	Path           string
	Timeout        Time
	Enabled        bool
	Allow          bool // when_unavailable: allow
	OnlyWithHeader string
	Send, Accept   []Header
	SendRequest    bool // send_request: true, which tells the check the request's path, method, scheme and host
}

// JWT is auth.jwt: the proxy verifies a signed token on each request itself.
type JWT struct {
	Issuer    string
	Audiences []string // nil for any, which accepts every audience
	// File or URL is set: the keys, as a JSON Web Key Set, in a file on the proxy's host or at an https URL.
	File, URL string
	// Host, Port and DNS describe the URL's host, "" and 0 with a file: an IP address (an IPv6 one without brackets) or a
	// DNS name, which the proxy looks up while it runs; Port is 443 when the URL writes none.
	Host     string
	Port     int64
	DNS      bool
	CA       string  // the CA bundle the URL's certificate must chain to: /etc/ssl/certs/ca-certificates.crt when not written, "" with a file
	Header   string  // the header that carries the token; authorization when not written
	Optional bool    // a request with no token goes through unverified
	Claims   []Claim // written order
}

// Claim is one entry of auth.jwt.claims: the token's claim Name, copied into the request header Header.
type Claim struct {
	Header, Name string
}

type Header struct {
	Name   string // without the trailing * of a prefix
	Prefix bool   // written with a trailing *
	Pos    Pos
}

// Covers reports whether the entry h names every header other names: an exact entry covers the one name equal to it,
// and a prefix entry every name and prefix that starts with it. The early header removal adds no header an entry before
// it covers, and the reader refuses a claims header an entry of send or accept covers.
func (h Header) Covers(other Header) bool {
	if h.Prefix {
		return strings.HasPrefix(other.Name, h.Name)
	}
	return !other.Prefix && h.Name == other.Name
}

// headerValueRefused names the three characters RFC 9110, section 5.5 calls invalid and dangerous in a field value
// (it calls the other control characters invalid too), which are the ones Envoy's header value pattern,
// ^[^\x00\n\r]*$, refuses.
var headerValueRefused = []struct{ char, name string }{{"\x00", "NUL"}, {"\n", "a newline"}, {"\r", "a carriage return"}}

// HeaderValueRefused names, in that order, each of NUL, a newline and a carriage return that value holds, or returns nil
// when it holds none. proxyfile writes the RPC action and reports, the cors values and a route's rewrite into header
// values as they are written, so each reader refuses these where its problem can name the value.
func HeaderValueRefused(value string) []string {
	var names []string
	for _, refused := range headerValueRefused {
		if strings.Contains(value, refused.char) {
			names = append(names, refused.name)
		}
	}
	return names
}

type MatchKind string

const (
	Prefix  MatchKind = "prefix"
	Path    MatchKind = "path"
	Pattern MatchKind = "pattern"
)

// Route is one entry of `routes`: the `- rpc:` entry when RPC is set, else a plain route.
type Route struct {
	Pos       Pos
	RPC       *RPC // the `- rpc:` entry; the other fields are empty then
	Name      Text
	Match     MatchKind
	Value     Text // the prefix, path or pattern
	Method    string
	To        Text
	Rewrite   *Text
	AuthOff   *Text // the `auth: off` value, kept for its position
	Retries   int64
	RateLimit *RateLimit // nil when not written: the route is never limited
}

type RPC struct {
	Retries   int64
	RateLimit *RateLimit  // nil when not written; one limit for every RPC route of the entry
	Targets   []RPCTarget // written order
	ToPos     Pos         // the `to` key
}

// RateLimit is a token bucket: it holds at most Requests + Burst tokens and refills at Requests tokens per Per, and
// each request takes one. Burst is 0 and ByClient true when not written; ByClient gives each client address a bucket
// of its own, and false counts every client together.
type RateLimit struct {
	Requests, Burst int64
	Per             Time
	ByClient        bool
}

type RPCTarget struct {
	Pattern   string
	Service   Text
	RouteName string // "<service>-rpc" when not written
	Pos       Pos    // the pattern
}

type Log struct {
	StatusFrom    int64
	SamplePercent int
	Pos           Pos // the `log` key
}

// Tracing is the tracing key, or envoy.tracing, its old place, which holds the same keys: a trace span for each
// sampled request.
type Tracing struct {
	// XRay and OTel are the provider written: exactly one is set, and envoy.tracing takes XRay alone.
	XRay      *XRay
	OTel      *OTel
	Tags      []Tag // written order
	PathInURL bool
}

// OTel is tracing.provider.otel: an OpenTelemetry span for each sampled request, sent as OTLP over gRPC to a collector.
type OTel struct {
	Host        string // the collector's IP address, an IPv6 one without brackets, or its DNS name
	Port        int64
	DNS         bool   // Host is a DNS name, which the proxy looks up while it runs
	Service     string // the service.name the spans carry
	RatePercent int    // 5 when sample.rate is not written
}

// XRay is tracing.provider.xray: an X-Ray segment for each sampled request, sent to an agent over UDP.
type XRay struct {
	Address string // the agent's IPv4 address; 127.0.0.1 when not written
	Port    int64  // the agent's UDP port; 2000 when not written
	Segment string
	Sample  *Sample // nil when not written: no manifest, and Envoy's own rule applies
}

// Sample is what the sampling manifest holds. The default rule matches every request, so its Host, Method and Path
// are empty.
type Sample struct {
	Default SampleRule
	Rules   []SampleRule // written order
}

type SampleRule struct {
	Host, Method, Path string // "*" when not written
	First              int64
	RatePercent        int
}

// Tag is one custom tag: exactly one of Env and Header is set.
type Tag struct {
	Name, Env, Header string
	Default           string // "" when not written; the check refuses an empty one
}

// Service returns the service declared with name, or nil.
func (g *Gateway) Service(name string) *Service {
	for i := range g.Services {
		if g.Services[i].Name == name {
			return &g.Services[i]
		}
	}
	return nil
}

// RPCEntry returns the first `- rpc:` entry of the routes, or nil.
func (g *Gateway) RPCEntry() *Route {
	for i := range g.Routes {
		if g.Routes[i].RPC != nil {
			return &g.Routes[i]
		}
	}
	return nil
}
