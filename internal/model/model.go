package model

// Gateway is one proxyfile after reading: every default filled in and every rule checked. Each value a rule or a
// later stage may name in a problem keeps the position it was written at; a default that was not written has no
// position, and nothing refuses a default.
type Gateway struct {
	Namespace         string // "proxyfile" when not written
	Listen            Port
	Admin             *Port
	Limits            Limits
	Timeouts          Timeouts
	Services          []Service // written order
	ServicesPos       Pos       // the `services` key
	Auth              *Auth
	RemoveFromClients []Header
	Origins           Text    // the origins file as written, absolute or relative to the proxyfile's directory; "" when the proxyfile writes the origins
	Routes            []Route // written order
	RoutesPos         Pos     // the `routes` key
	Log               *Log
}

type Port struct {
	Number int64
	Pos    Pos
}

type Text struct {
	Value string
	Pos   Pos
}

type Limits struct {
	Heap           *Size
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
	Name       string
	Pos        Pos  // the service's name
	Address    Text // "127.0.0.1" when not written
	HostHeader string
	Ports      []ServicePort // written order
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
}

type Header struct {
	Name   string // without the trailing * of a prefix
	Prefix bool   // written with a trailing *
	Pos    Pos
}

type MatchKind string

const (
	Prefix  MatchKind = "prefix"
	Path    MatchKind = "path"
	Pattern MatchKind = "pattern"
)

// Route is one entry of `routes`: the `- rpc:` entry when RPC is set, else a plain route.
type Route struct {
	Pos     Pos
	RPC     *RPC // the `- rpc:` entry; the other fields are empty then
	Name    Text
	Match   MatchKind
	Value   Text // the prefix, path or pattern
	Method  string
	To      Text
	Rewrite *Text
	AuthOff *Text // the `auth: off` value, kept for its position
	Retries int64
}

type RPC struct {
	Retries int64
	Targets []RPCTarget // written order
	ToPos   Pos         // the `to` key
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
