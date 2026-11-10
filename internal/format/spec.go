// Package format holds the one description of each input file and checks a file's tree against it.
package format

import (
	"math"
	"slices"

	"github.com/kitecorp/proxyfile/internal/model"
)

// Kind is what a key's value must be. Map has the keys its Fields name and Dict the keys its writer chooses; Either
// is whichever of its Choices fits the value.
type Kind int

const (
	Map Kind = iota + 1
	Dict
	List
	Text
	Int
	Bool
	Enum
	Either
)

// Field describes one key of a file format. The checker reads it, and `proxyfile schema` prints it as JSON Schema,
// so the keys an editor offers are the keys the program accepts.
type Field struct {
	Name     string // the key; "" for the root, list items and dictionary values
	Doc      string // one sentence, the schema's description
	Kind     Kind
	Required bool
	Fields   []Field  // Map
	OneOf    []string // Map: exactly one of these keys
	Item     *Field   // List items; Dict values
	Key      *Field   // Dict: the keys the writer chooses (Name says what they are, Pattern and Expect their rule)
	// Pattern (Text and Key) runs in two engines: Go's RE2 in the checker, and an editor's ECMAScript engine through
	// the JSON Schema, which writes it unchanged. So it keeps to what both read alike. The package fails to load on a
	// form Go refuses, such as a lookahead, an atomic group or a possessive repeat. Of the forms Go accepts,
	// ECMAScript refuses a flag set for the rest of the pattern, such as (?i), and (?P<name>; it reads \A and \z as
	// letters or refuses them; and \s, \S and a . outside brackets match other characters in it: write Go's \S out as
	// [^\t\n\f\r ]. [\s\S], every character, is the same in both.
	Pattern  string
	Expect   string   // Text, Key, Either: completes "must be ..."
	Min, Max int64    // Int; Max 0 is no maximum. int64, not int, so a uint32 limit fits on a 32-bit build too
	Values   []string // Enum
	Choices  []Field  // Either: a Text choice and Map choices
	Select   string   // a Map choice of an Either: chosen when the node has this key; the Map choice without Select is the default
}

const (
	serviceNamePattern = `^[a-z][a-z0-9-]*$`
	serviceNameExpect  = "lower-case letters, digits and -, starting with a letter"
	routeNamePattern   = `^[A-Za-z0-9._-]+$`
	routeNameExpect    = "letters, digits, dots, - and _"
	sizeExpect         = "a size such as 192MiB"
	// Envoy holds header names in lower case (its HTTP/1 codec lowers them, http1/codec_impl.cc; HTTP/2 and HTTP/3
	// allow nothing else), and send, accept and remove_from_clients become string matchers on those names, which
	// compare case by case unless told otherwise (common/matchers.h). A name written with a capital would never match,
	// so a client header the file means to remove would reach the service.
	headerPattern = `^[a-z0-9][a-z0-9-]*\*?$`
	headerExpect  = "a lower-case header name, or a prefix ending in *"
	// Envoy 1.40.2 holds these whole numbers in a uint32 and refuses a larger value when it loads the file:
	// max_headers_count (protocol.proto), which limits.request_headers.count is written to; the circuit breakers'
	// max_connections, max_pending_requests and max_requests (circuit_breaker.proto), which limits.connections is
	// written to; and num_retries (route_components.proto), which retries is written to. As a Max, the limit reaches
	// the JSON Schema too, so an editor refuses what the check refuses.
	largestUint32 = math.MaxUint32
)

func port(name, doc string, required bool) Field {
	return Field{Name: name, Doc: doc, Kind: Int, Required: required, Min: 1, Max: 65535}
}

func serviceName(name, doc string, required bool) Field {
	return Field{Name: name, Doc: doc, Kind: Text, Required: required, Pattern: serviceNamePattern, Expect: serviceNameExpect}
}

func path(name, doc string) Field {
	return Field{Name: name, Doc: doc, Kind: Text, Pattern: `^/`, Expect: "a path starting with /"}
}

func timeOrNone(name, doc string) Field {
	return Field{Name: name, Doc: doc, Kind: Text, Pattern: model.TimeOrNonePattern, Expect: "a time such as 125s, or none"}
}

func headers(name, doc string) Field {
	return Field{Name: name, Doc: doc, Kind: List, Item: &Field{Kind: Text, Pattern: headerPattern, Expect: headerExpect}}
}

func texts(name, doc string, required bool) Field {
	return Field{Name: name, Doc: doc, Kind: List, Required: required, Item: &Field{Kind: Text}}
}

func retries(doc string) Field {
	return Field{Name: "retries", Doc: doc, Kind: Int, Min: 0, Max: largestUint32}
}

// MatchKinds are the keys of which a route writes exactly one, and ServicePorts the port keys of a service, each
// named by its protocol. The decoder reads a route's match and a service's ports through these two lists, so a key
// added to either is read without a second list to keep in step.
var (
	MatchKinds   = []string{"prefix", "path", "pattern"}
	ServicePorts = []Field{port("http", "The service's HTTP port.", false), port("grpc", "The service's gRPC port.", false)}
)

// Proxyfile describes the file proxyfile reads: every key, its kind and its rule. The meaning rules that need more
// than one key, such as a route naming a declared service, belong to the reader.
var Proxyfile = Field{
	Doc:  "A proxyfile: one gateway, the services behind it and the routes to them, from which proxyfile writes an Envoy configuration.",
	Kind: Map,
	Fields: []Field{
		{Name: "namespace", Doc: "The namespace of the gateway's own metadata, which the routes and the request log use; proxyfile when not written.",
			Kind: Text, Pattern: `^[a-z][a-z0-9._-]*$`, Expect: "lower-case letters, digits, dots, - and _, starting with a letter"},
		port("listen", "The port of the public listener, on 0.0.0.0.", true),
		port("admin", "The port of the admin listener, on 127.0.0.1; no admin listener when not written.", false),
		{Name: "limits", Doc: "Limits on memory, connections and request headers.", Kind: Map, Fields: []Field{
			{Name: "heap", Doc: "The heap budget: the gateway shrinks its heap at 90% of it and refuses new requests at 98%.",
				Kind: Text, Pattern: model.SizePattern, Expect: sizeExpect},
			{Name: "connections", Doc: "The most connections at once, from clients and to each service.", Kind: Int, Min: 1, Max: largestUint32},
			{Name: "request_headers", Doc: "The largest header block and the most header fields a request may carry.", Kind: Map, Fields: []Field{
				{Name: "size", Doc: "The largest header block of a request.", Kind: Text, Required: true, Pattern: model.SizePattern, Expect: sizeExpect},
				{Name: "count", Doc: "The most header fields in a request.", Kind: Int, Required: true, Min: 1, Max: largestUint32},
			}},
		}},
		{Name: "timeouts", Doc: "How long a request, an idle request and an idle connection to a service may last.", Kind: Map, Fields: []Field{
			timeOrNone("request", "The longest a whole request may take."),
			timeOrNone("idle", "The longest a request may go without sending or receiving data."),
			timeOrNone("upstream_idle", "The longest a connection to a service is kept open with no request on it."),
		}},
		{Name: "services", Doc: "The services behind the gateway, on the same host, by name.", Kind: Dict, Required: true,
			Key: &Field{Name: "service name", Kind: Text, Pattern: serviceNamePattern, Expect: serviceNameExpect},
			Item: &Field{Doc: "One service: a port per protocol, an optional Host header and an optional address.", Kind: Map, Fields: slices.Concat(ServicePorts, []Field{
				{Name: "host_header", Doc: "The Host header a request carries when a route sends it to this service.",
					Kind: Text, Pattern: `^[A-Za-z0-9.:\[\]-]+$`, Expect: "a host, with an optional port"},
				{Name: "address", Doc: "The service's IP address; 127.0.0.1 when not written.", Kind: Text, Pattern: `^[^\t\n\f\r ]+$`, Expect: "an IP address"},
			})}},
		{Name: "auth", Doc: "The external check every request goes through before it reaches a service.", Kind: Map, Fields: []Field{
			{Name: "ask", Doc: "The service and the path the gateway asks.", Kind: Text, Required: true,
				Pattern: `^[a-z][a-z0-9-]* /[^\t\n\f\r ]*$`, Expect: "a service and a path, such as accounts /check"},
			{Name: "timeout", Doc: "How long the gateway waits for the answer.", Kind: Text, Required: true,
				Pattern: model.TimePattern, Expect: "a time such as 2s"},
			{Name: "enabled", Doc: "true runs the check on every request it applies to, false on none.", Kind: Bool, Required: true},
			{Name: "when_unavailable", Doc: "What happens to a request when the check cannot be asked: allow lets it through, deny refuses it.",
				Kind: Enum, Required: true, Values: []string{"allow", "deny"}},
			// send, accept, remove_from_clients and only_with_header are checked alike. Envoy lowers only the
			// header-matcher names it reads itself, this one among them (the header match input keeps it as a
			// LowerCaseString, http/matching/inputs.h).
			{Name: "only_with_header", Doc: "The check runs only on requests that carry this header.",
				Kind: Text, Pattern: `^[a-z0-9][a-z0-9-]*$`, Expect: "a lower-case header name"},
			headers("send", "The request headers the check is sent; a name ending in * is a prefix."),
			headers("accept", "The headers of the check's answer that reach the service; a name ending in * is a prefix."),
		}},
		headers("remove_from_clients", "Headers removed from every client request before routing and before any filter; a name ending in * is a prefix."),
		{Name: "origins", Doc: "The origins file, relative to this file: the CORS settings and, for each environment, its hosts and the browser origins it admits.",
			Kind: Text, Required: true, Pattern: `^[^\t\n\f\r ]+$`, Expect: "a file name"},
		{Name: "routes", Doc: "The routes, tried in the order written; the rpc entry marks where the RPC routes go.", Kind: List, Required: true,
			Item: &Field{Kind: Either, Expect: "a mapping: a route or an rpc entry", Choices: []Field{
				{Doc: "The place in the order where the RPC routes go.", Kind: Map, Select: "rpc", Fields: []Field{
					{Name: "rpc", Doc: "The RPC routes: one exact-path route for each RPC method.", Kind: Map, Required: true, Fields: []Field{
						retries("How many times an RPC call that never reached its service is tried again."),
						{Name: "to", Doc: "The service the RPC methods of each package go to.", Kind: Dict, Required: true,
							Key: &Field{Name: "package pattern", Kind: Text,
								Pattern: `^(\*|[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*(\.\*)?)$`,
								Expect:  "*, a package name ending in .*, or a full service name"},
							Item: &Field{Kind: Either, Expect: "a service name, or a mapping with service and name", Choices: []Field{
								serviceName("", "The service the methods go to.", false),
								{Doc: "The service the methods go to, and the name of their routes.", Kind: Map, Fields: []Field{
									serviceName("service", "The service the methods go to.", true),
									{Name: "name", Doc: "The name of the routes; the service's name followed by -rpc when not written.",
										Kind: Text, Pattern: routeNamePattern, Expect: routeNameExpect},
								}},
							}}},
					}},
				}},
				{Doc: "A route: a name, what it matches and the service it goes to.", Kind: Map, OneOf: MatchKinds, Fields: []Field{
					{Name: "name", Doc: "The route's name.", Kind: Text, Required: true, Pattern: routeNamePattern, Expect: routeNameExpect},
					path("prefix", "Matches paths that start with this text."),
					path("path", "Matches this path exactly."),
					// Envoy refuses an empty regex (min_len 1 on the regex field, api/envoy/type/matcher/v3/regex.proto).
					// [\s\S] is any one character, a line end included, in RE2 and in ECMA-262, the syntax of JSON Schema
					// patterns, so the check and the schema both refuse the empty text and nothing else.
					{Name: "pattern", Doc: "Matches paths by this regular expression, in RE2 syntax.", Kind: Text,
						Pattern: `[\s\S]`, Expect: "a regular expression that is not empty"},
					{Name: "method", Doc: "Matches only requests with this HTTP method.", Kind: Text, Pattern: `^[A-Z]+$`, Expect: "an HTTP method in capitals, such as POST"},
					serviceName("to", "The service the route goes to.", true),
					path("rewrite", "Replaces the matched prefix or path before the request goes to the service."),
					{Name: "auth", Doc: "off sends the route's requests to the service without the check.", Kind: Enum, Values: []string{"off"}},
					retries("How many times a request that never reached the service is tried again."),
				}},
			}}},
		{Name: "log", Doc: "Which requests the request log writes.", Kind: Map, Fields: []Field{
			{Name: "status_from", Doc: "The lowest status that is always logged.", Kind: Int, Required: true, Min: 100, Max: 599},
			{Name: "sample", Doc: "The share of the other requests that is logged.", Kind: Text, Required: true,
				Pattern: model.PercentPattern, Expect: "a whole percentage such as 1%"},
		}},
	},
}

// The rules for hosts, origins and patterns belong to the origins reader, which names the environment in its
// messages; this description checks only the keys and the kinds.
var Origins = Field{
	Doc:  "The origins file: the CORS settings and, for each environment, its hosts and the browser origins it admits.",
	Kind: Map,
	Fields: []Field{
		{Name: "cors", Doc: "The CORS settings every environment shares.", Kind: Map, Fields: []Field{
			{Name: "allow_methods", Doc: "The value of Access-Control-Allow-Methods.", Kind: Text},
			{Name: "allow_headers", Doc: "The value of Access-Control-Allow-Headers.", Kind: Text},
			{Name: "expose_headers", Doc: "The value of Access-Control-Expose-Headers.", Kind: Text},
			{Name: "max_age", Doc: "The value of Access-Control-Max-Age: how long a browser may keep a preflight answer.",
				Kind: Text, Pattern: `^[0-9]+$`, Expect: "a number of seconds"},
		}},
		{Name: "environments", Doc: "The environments, each with its hosts and the browser origins it admits.", Kind: List, Required: true,
			Item: &Field{Doc: "One environment.", Kind: Map, Fields: []Field{
				{Name: "name", Doc: "The environment's name.", Kind: Text, Required: true},
				texts("hosts", "The host names the environment serves, without a port.", true),
				texts("origins", "The browser origins admitted exactly, each scheme://host[:port].", false),
				texts("patterns", "The browser origins admitted by pattern, where * matches any run of characters.", false),
			}}},
	},
}

// Methods describes the RPC methods file: one entry per method. Which service a method goes to is decided by the
// rpc entry of the proxyfile, not here.
var Methods = Field{
	Doc:  "The RPC methods the gateway routes, each with what the check is told about it.",
	Kind: List,
	Item: &Field{Doc: "One RPC method.", Kind: Map, Fields: []Field{
		{Name: "service", Doc: "The full name of the method's service.", Kind: Text, Required: true,
			Pattern: `^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`, Expect: "a full service name such as shop.v1.CartService"},
		{Name: "method", Doc: "The method's name.", Kind: Text, Required: true,
			Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`, Expect: "a method name such as AddItem"},
		{Name: "action", Doc: "The action the check is told for this method.", Kind: Text},
		texts("reports", "The reports the check is told for this method.", false),
	}},
}
