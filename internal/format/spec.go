// Package format holds the one description of each input file and checks a file's tree against it.
package format

import (
	"math"
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/model"
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
	Name string // the key; "" for the root, list items and dictionary values
	Doc  string // one sentence, the schema's description; on a Key, what a key means, which the schema has no place for
	// Rule completes the key's row in the documentation's key tables (Reference) with what the other fields cannot
	// say, such as a rule the reader checks across keys or a default of Envoy's. It is Markdown, and the schema
	// leaves it out.
	Rule     string
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
	Choices  []Field  // Either: a Text choice, and Map or Dict choices
	Select   string   // a Map choice of an Either: chosen when the node has this key; the Map or Dict choice without Select is the default
	// Moved names, by key, the keys of a Map that proxyfile 1.x read here and 2.0 reads at another place, with that place,
	// so the check names it instead of calling the key unknown.
	Moved map[string]string
	// Reserved marks the block of a proxy proxyfile has no generator for yet (Map): it is accepted empty, and every key
	// written in it is refused with NoGenerator.
	Reserved bool
	// Class is the proxy whose generator alone reads the key and every key under it that names no class of its own:
	// envoy, nginx or haproxy. A key that names none, held by none that does, is core: every proxy can carry it
	// (docs/portability.md).
	Class string
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
	// A header named exactly, for the rules that take no prefix: auth.only_with_header and a tracing tag's header.
	lowerHeaderPattern = `^[a-z0-9][a-z0-9-]*$`
	lowerHeaderExpect  = "a lower-case header name"
	// Envoy 1.40.2 holds these whole numbers in a uint32 and refuses a larger value when it loads the file:
	// max_headers_count (protocol.proto), which limits.request_headers.count is written to; the circuit breakers'
	// max_connections, max_pending_requests and max_requests (circuit_breaker.proto), which limits.connections is
	// written to; num_retries (route_components.proto), which retries is written to; and tracing's first, the
	// fixed_target the X-Ray sampler holds as a uint32 (localized_sampling.cc line 35). As a Max, the limit reaches
	// the JSON Schema too, so an editor refuses what the check refuses.
	largestUint32 = math.MaxUint32
)

// AgentExpect is exported because the reader's message for an agent its pattern admits but ParseAgent refuses must be
// the check's, and the reader's tooLarge cannot walk the provider's Either to read it from the description.
const AgentExpect = "an IPv4 address and a port from 1 to 65535, such as 127.0.0.1:2000"

// NoGenerator says why a target and a key of its block are refused: proxyfile has no generator for that proxy yet.
func NoGenerator(backend string) string {
	return "no generator for " + backend + " in this version; see docs/portability.md"
}

// core is the class of a key every proxy can carry: one that names no class and is held by no key that names one.
const core = "core"

// withRule returns f, written by one of the helpers here, with the Rule its key's row adds.
func withRule(f Field, rule string) Field {
	f.Rule = rule
	return f
}

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

func first(doc string, required bool) Field {
	return Field{Name: "first", Doc: doc, Kind: Int, Required: required, Min: 0, Max: largestUint32}
}

func rate(doc string, required bool) Field {
	return Field{Name: "rate", Doc: doc, Kind: Text, Required: required, Pattern: model.PercentPattern, Expect: "a whole percentage such as 5%",
		Rule: "From `0%` to `100%`, the form of `log.sample`."}
}

// MatchKinds are the keys of which a route writes exactly one, and ServicePorts the port keys of a service, each
// named by its protocol. The decoder reads a route's match and a service's ports through these two lists, so a key
// added to either is read without a second list to keep in step.
var (
	MatchKinds   = []string{"prefix", "path", "pattern"}
	ServicePorts = []Field{withRule(port("http", "The service's HTTP port.", false), "Spoken as HTTP/1.1."),
		withRule(port("grpc", "The service's gRPC port.", false), "Spoken as HTTP/2 without TLS (h2c).")}
)

// Proxyfile describes the file proxyfile reads: every key, its kind and its rule. The meaning rules that need more
// than one key, such as a route naming a declared service, belong to the reader.
var Proxyfile = Field{
	Doc:   "A proxyfile: one gateway, the services behind it and the routes to them, from which proxyfile writes an Envoy configuration.",
	Kind:  Map,
	Moved: map[string]string{"namespace": "envoy.namespace", "tracing": "envoy.tracing"},
	Fields: []Field{
		port("listen", "The port of the public listener, on 0.0.0.0.", true),
		withRule(port("admin", "The port of the admin listener, on 127.0.0.1; no admin listener when not written.", false),
			"Not the same as `listen`. It answers only `/ready`, `/stats` and `/server_info`."),
		{Name: "limits", Doc: "Limits on connections and request headers.", Kind: Map, Moved: map[string]string{"heap": "envoy.heap"}, Fields: []Field{
			{Name: "connections", Doc: "The most connections at once, from clients and to each service.", Kind: Int, Min: 1, Max: largestUint32},
			{Name: "request_headers", Doc: "The largest header block and the most header fields a request may carry.", Kind: Map, Fields: []Field{
				// Its own example, not the heap's: 192MiB is above the 8MiB limit the reader holds a header block to.
				{Name: "size", Doc: "The largest header block of a request.", Kind: Text, Required: true, Pattern: model.SizePattern,
					Expect: "a size such as 96KiB", Rule: "At most `8MiB`."},
				{Name: "count", Doc: "The most header fields in a request.", Kind: Int, Required: true, Min: 1, Max: largestUint32},
			}},
		}},
		{Name: "timeouts", Doc: "How long a request, an idle request and an idle connection to a service may last.", Kind: Map, Fields: []Field{
			withRule(timeOrNone("request", "The longest a whole request may take."),
				"Without it a route has Envoy's default of 15 seconds, counted from the end of the request. `none` removes the limit."),
			timeOrNone("idle", "The longest a request may go without sending or receiving data."),
			withRule(timeOrNone("upstream_idle", "The longest a connection to a service is kept open with no request on it."),
				"Envoy's default is 1 hour."),
		}},
		{Name: "services", Doc: "The services behind the gateway, on the same host, by name.", Kind: Dict, Required: true, Rule: "At least one service.",
			Key: &Field{Name: "service name", Kind: Text, Pattern: serviceNamePattern, Expect: serviceNameExpect},
			Item: &Field{Doc: "One service: a port per protocol, an optional Host header and an optional address.", Kind: Map, Rule: "It has at least one port.", Fields: slices.Concat(ServicePorts, []Field{
				{Name: "host_header", Doc: "The Host header a request carries when a route sends it to this service.",
					Kind: Text, Pattern: `^[A-Za-z0-9.:\[\]-]+$`, Expect: "a host, with an optional port", Rule: "Without it a request keeps the client's Host."},
				{Name: "address", Doc: "The service's IP address; 127.0.0.1 when not written.", Kind: Text, Pattern: `^[^\t\n\f\r ]+$`, Expect: "an IP address",
					Rule: "Not a name: Envoy looks up no name for a static cluster."},
			})}},
		{Name: "auth", Doc: "The external check every request goes through before it reaches a service.", Kind: Map, Rule: "Without `auth` there is no check.", Fields: []Field{
			{Name: "ask", Doc: "The service and the path the gateway asks.", Kind: Text, Required: true,
				Pattern: `^[a-z][a-z0-9-]* /[^\t\n\f\r ]*$`, Expect: "a service and a path, such as accounts /check", Rule: "The service needs an `http` port."},
			{Name: "timeout", Doc: "How long the gateway waits for the answer.", Kind: Text, Required: true,
				Pattern: model.TimePattern, Expect: "a time such as 2s", Rule: "At most `4294967295s`. `none` is not accepted."},
			{Name: "enabled", Doc: "true runs the check on every request it applies to, false on none.", Kind: Bool, Required: true},
			{Name: "when_unavailable", Doc: "What happens to a request when the check cannot be asked: allow lets it through, deny refuses it.",
				Kind: Enum, Required: true, Values: []string{"allow", "deny"}},
			// send, accept, remove_from_clients and only_with_header are checked alike. Envoy lowers only the
			// header-matcher names it reads itself, this one among them (the header match input keeps it as a
			// LowerCaseString, http/matching/inputs.h).
			{Name: "only_with_header", Doc: "The check runs only on requests that carry this header.",
				Kind: Text, Pattern: lowerHeaderPattern, Expect: lowerHeaderExpect, Rule: "A request without it, or with it empty, goes through with no check."},
			headers("send", "The request headers the check is sent; a name ending in * is a prefix."),
			headers("accept", "The headers of the check's answer that reach the service; a name ending in * is a prefix."),
		}},
		withRule(headers("remove_from_clients", "Headers removed from every client request before routing and before any filter; a name ending in * is a prefix."),
			"No route, filter or service sees a client's copy. Envoy holds header names in lower case, so a name with a capital would never match."),
		withRule(corsField, "Not written when `origins` names a file, which holds its own `cors`."),
		{Name: "origins", Doc: "The browser origins each environment admits, by environment name; or, as in 1.0, the name of an origins file, relative to this file.",
			Kind: Either, Required: true, Expect: "a mapping of environments by name, or a file name", Rule: "At least one environment.", Choices: []Field{
				{Kind: Text, Pattern: `^[^\t\n\f\r ]+$`, Expect: "a file name"},
				{Kind: Dict, Key: &Field{Name: "environment name", Kind: Text, Pattern: `[\s\S]`, Expect: "one character or more"},
					Item: &Field{Doc: "One environment: its hosts and the browser origins it admits.", Kind: Map, Fields: environmentLists}},
			}},
		{Name: "routes", Doc: "The routes, tried in the order written; the rpc entry marks where the RPC routes go.", Kind: List, Required: true,
			Rule: "Not empty. The rpc entry is `- rpc:` in YAML and `{ rpc { ... } }` in HOCON.",
			Item: &Field{Kind: Either, Expect: "a mapping: a route or an rpc entry", Choices: []Field{
				{Doc: "The place in the order where the RPC routes go.", Kind: Map, Select: "rpc", Fields: []Field{
					{Name: "rpc", Doc: "The RPC routes: one exact-path route for each RPC method.", Kind: Map, Required: true,
						Rule: "At most one rpc entry in a file. It needs methods from exactly one of `rpc.methods`, `rpc.proto` and `--rpc-methods`.", Fields: []Field{
							retries("How many times an RPC call that never reached its service is tried again."),
							{Name: "to", Doc: "The service the RPC methods of each package go to.", Kind: Dict, Required: true, Rule: "Not empty.",
								Key: &Field{Name: "package pattern", Kind: Text,
									Doc: "Which services' methods go to the value: * matches every service, a package name ending in .* every service " +
										"whose full name starts with that package and a dot, and a full service name that service alone.",
									Pattern: `^(\*|[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*(\.\*)?)$`,
									Expect:  "*, a package name ending in .*, or a full service name"},
								Item: &Field{Kind: Either, Expect: "a service name, or a mapping with service and name", Rule: "The service needs a `grpc` port.", Choices: []Field{
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
					{Name: "name", Doc: "The route's name.", Kind: Text, Required: true, Pattern: routeNamePattern, Expect: routeNameExpect,
						Rule: "It appears in the request log. Not used twice, and not `rpc`."},
					path("prefix", "Matches paths that start with this text."),
					path("path", "Matches this path exactly."),
					// Envoy refuses an empty regex (min_len 1 on the regex field, api/envoy/type/matcher/v3/regex.proto).
					// [\s\S] is any one character, a line end included, in RE2 and in ECMA-262, the syntax of JSON Schema
					// patterns, so the check and the schema both refuse the empty text and nothing else.
					{Name: "pattern", Doc: "Matches paths by this regular expression, in RE2 syntax.", Kind: Text,
						Pattern: `[\s\S]`, Expect: "a regular expression that is not empty", Rule: "It must compile."},
					{Name: "method", Doc: "Matches only requests with this HTTP method.", Kind: Text, Pattern: `^[A-Z]+$`, Expect: "an HTTP method in capitals, such as POST"},
					withRule(serviceName("to", "The service the route goes to.", true), "A declared service with an `http` port."),
					withRule(path("rewrite", "Replaces the matched prefix or path before the request goes to the service."), "Not allowed with `pattern`."),
					{Name: "auth", Doc: "off sends the route's requests to the service without the check.", Kind: Enum, Values: []string{"off"},
						Rule: "Needs an `auth` key."},
					retries("How many times a request that never reached the service is tried again."),
				}},
			}}},
		{Name: "rpc", Doc: "The RPC methods the gateway routes; the routes' rpc entry maps them to services.", Kind: Map,
			Rule:  "Written here or read from `.proto` files, instead of in a methods file. Not given together with `--rpc-methods`.",
			OneOf: []string{"methods", "proto"}, Fields: []Field{
				{Name: "methods", Doc: "One entry for each RPC method the gateway routes, as a build writes them from the gRPC descriptors.",
					Kind: List, Item: &methodField, Rule: "It may be empty. Which service a method goes to is decided by the rpc entry of `routes`."},
				protoField,
			}},
		{Name: "log", Doc: "Which requests the request log writes.", Kind: Map, Rule: "Without `log`, every request is logged.", Fields: []Field{
			{Name: "status_from", Doc: "The lowest status that is always logged.", Kind: Int, Required: true, Min: 100, Max: 599},
			{Name: "sample", Doc: "The share of the other requests that is logged.", Kind: Text, Required: true,
				Pattern: model.PercentPattern, Expect: "a whole percentage such as 1%"},
		}},
		envoyField, reserved("nginx", "nginx"), reserved("haproxy", "HAProxy"),
	},
}

// envoyField is the envoy block: the keys only the Envoy generator reads. proxyfile 1.x read them at the top of the
// file and under limits; the Moved of the root and of limits names each one's new place, so a 1.x file is refused with
// it.
var envoyField = Field{Name: "envoy", Doc: "The settings only the Envoy generator reads: the namespace of the gateway's metadata, its heap budget, X-Ray tracing and the Connect bridge.",
	Kind: Map, Class: "envoy", Fields: []Field{
		{Name: "namespace", Doc: "The namespace of the gateway's own metadata, which the routes and the request log use; proxyfile when not written.",
			Kind: Text, Pattern: `^[a-z][a-z0-9._-]*$`, Expect: "lower-case letters, digits, dots, - and _, starting with a letter"},
		{Name: "heap", Doc: "The heap budget: the gateway shrinks its heap at 90% of it and refuses new requests at 98%.",
			Kind: Text, Pattern: model.SizePattern, Expect: sizeExpect},
		tracingField,
		{Name: "connect_bridge", Doc: "true turns each Connect call on an RPC route into gRPC for the service, and the answer back into Connect; false routes gRPC calls alone. true when not written.",
			Kind: Bool, Rule: "`false` also leaves out the Lua filter that corrects the status of the bridge's errors."},
	}}

// reserved is the block, named name, of a proxy that has no generator in this version. It is accepted empty, so a file
// can hold it before the generator comes, and a key in it is refused rather than left unread.
func reserved(name, proxy string) Field {
	return Field{Name: name, Doc: "The settings only " + proxy + " will read; this version has no " + proxy + " generator, so it takes no key.",
		Kind: Map, Reserved: true, Class: name}
}

// protoField is the proto key of rpc (design section 5, "RPC methods from the protobuf sources"). The reader adds the
// two rules a description cannot hold: the roots list is not empty, and no root is written twice.
var protoField = Field{Name: "proto", Doc: "The RPC methods, read from the .proto files under roots: each method that carries the method option named by option.",
	Kind: Map, Rule: "Compiled each time proxyfile runs. Not given with `--rpc-methods`.", Fields: []Field{
		withRule(texts("roots", "The directories of .proto files, each an import root: relative to the proxyfile's directory, or absolute.", true),
			"Not empty, and no directory written twice. Each is an import root, as protoc's `--proto_path` is: a file's import path "+
				"is its path under its root, written with `/`. A relative directory is read from the proxyfile's own directory, as "+
				"`origins: <file>` is, also when the key is written in an included file; an absolute one as written."),
		{Name: "option", Doc: "The full name of the method option that marks a method the gateway routes.", Kind: Text, Required: true,
			Pattern: `^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`, Expect: "an option's full name such as example.gateway.v1.route",
			Rule: "Written `<package>.<name>`, with no leading dot."},
	}}

// tracingField is the tracing key (design section 5, "Tracing"). The reader adds the one rule a pattern cannot hold:
// an agent that is an IPv4 address and a port.
var tracingField = Field{Name: "tracing", Doc: "An X-Ray segment for each sampled request, sent to an agent over UDP.", Kind: Map, Fields: []Field{
	// One Map choice chosen by its key, as the rpc entry of routes is chosen by rpc, so another provider is one more
	// choice with its own keys, and a mapping without xray gets the Either's message.
	{Name: "provider", Doc: "The tracing system the segments go to: a mapping with one key that names it.", Kind: Either, Required: true,
		Expect: "a mapping with the one key xray", Choices: []Field{
			{Doc: "AWS X-Ray.", Kind: Map, Select: "xray", Fields: []Field{
				{Name: "xray", Doc: "AWS X-Ray: where the segments go, their name and which requests are traced.", Kind: Map, Required: true, Fields: []Field{
					{Name: "agent", Doc: "Where the segments go over UDP: an X-Ray daemon, or an agent that takes X-Ray segments; 127.0.0.1:2000 when not written.",
						Kind: Text, Pattern: model.AgentPattern, Expect: AgentExpect},
					// X-Ray's characters (its segment documents, "Required Segment Fields"), ASCII only, so the check and an
					// editor's ECMAScript engine count the same characters.
					{Name: "segment", Doc: "The name of the gateway's segments, which names its node in X-Ray's service map.", Kind: Text, Required: true,
						Pattern: `^[A-Za-z0-9 _.:/%&#=+\\@-]{1,200}$`, Expect: `1 to 200 characters: letters, digits, spaces and _ . : / % & # = + \ - @`},
					{Name: "sample", Doc: "Which requests are traced when the request does not say: the rules, then the default. Without it, Envoy traces the first request each second and 5% of the others.",
						Kind: Map, Rule: "Without it no manifest is written and Envoy's own rule applies, which is `first: 1, rate: 5%`.", Fields: []Field{
							first("How many requests each second are traced before rate applies; 1 when not written.", false),
							rate("The share of the other requests that is traced; 5% when not written.", false),
							{Name: "rules", Doc: "Rules tried in the order written, before the default; the first that matches a request decides.", Kind: List,
								Rule: "It may be empty. Each rule decides with its own `first` and `rate`.",
								Item: &Field{Doc: "One rule: what it matches, and its own first and rate.", Kind: Map, Fields: []Field{
									{Name: "host", Doc: "Matches the Host header, with its port when the request has one; * when not written.", Kind: Text,
										Pattern: `^[A-Za-z0-9.:\[\]*?-]+$`, Expect: "a pattern of letters, digits, ., -, :, [, ], * and ?"},
									{Name: "method", Doc: "Matches the request's method; * when not written.", Kind: Text,
										Pattern: `^[A-Z*?]+$`, Expect: "a method in capitals, or a pattern of capitals, * and ?"},
									{Name: "path", Doc: "Matches the request's path, with its query; * when not written.", Kind: Text,
										Pattern: `^[/*][!-~]*$`, Expect: "a pattern of printable characters without spaces, starting with / or *"},
									first("How many matching requests each second are traced before rate applies.", true),
									rate("The share of the other matching requests that is traced.", true),
								}}},
						}},
				}},
			}},
		}},
	{Name: "tags", Doc: "Annotations on each segment, by name.", Kind: Dict, Rule: "Written order is kept, in both forms.",
		Key: &Field{Name: "tag name", Kind: Text, Pattern: `^[A-Za-z0-9_]+$`, Expect: "letters, digits and _"},
		Item: &Field{Doc: "One tag: the environment variable or the request header it reads, and an optional default.", Kind: Map,
			OneOf: []string{"env", "header"}, Fields: []Field{
				{Name: "env", Doc: "An environment variable of the gateway's process, read once when Envoy starts.", Kind: Text,
					Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`, Expect: "a variable name: letters, digits and _, not starting with a digit"},
				{Name: "header", Doc: "A request header.", Kind: Text, Pattern: lowerHeaderPattern, Expect: lowerHeaderExpect},
				{Name: "default", Doc: "The value when the variable is unset or the header is absent; without it the segment gets no tag then.",
					Kind: Text, Pattern: `[\s\S]`, Expect: "one character or more"},
			}}},
	{Name: "path_in_url", Doc: "true keeps the request's path in the segment's URL, its first 256 characters; false, the default, keeps the scheme and the host only.",
		Kind: Bool},
}}

// corsField and environmentLists are written once and used by both places the origins may be written: the proxyfile,
// where each environment is named by its key, and an origins file, where it is named by its name key. So a key means
// the same in both places and is checked by the same rule.
var (
	corsField = Field{Name: "cors", Doc: "The CORS settings every environment shares.", Kind: Map, Fields: []Field{
		{Name: "allow_methods", Doc: "The value of Access-Control-Allow-Methods.", Kind: Text},
		{Name: "allow_headers", Doc: "The value of Access-Control-Allow-Headers.", Kind: Text},
		{Name: "expose_headers", Doc: "The value of Access-Control-Expose-Headers.", Kind: Text},
		{Name: "max_age", Doc: "The value of Access-Control-Max-Age: how long a browser may keep a preflight answer.",
			Kind: Text, Pattern: `^[0-9]+$`, Expect: "a number of seconds", Rule: "Written as text."},
	}}
	environmentLists = []Field{
		withRule(texts("hosts", "The host names the environment serves, without a port.", true),
			"Not empty. Lower-case host names, such as `api.example.com`. No host is in two environments."),
		withRule(texts("origins", "The browser origins admitted exactly, each scheme://host[:port].", false),
			"Lower case, `http` or `https`, no path."),
		withRule(texts("patterns", "The browser origins admitted by pattern, where * matches any run of characters.", false),
			"A `*` matches dots too. Each is `scheme://host[:port]` with at least one `*`."),
	}
)

// The rules for hosts, origins and patterns belong to the origins reader, which names the environment in its
// messages; this description checks only the keys and the kinds.
var Origins = Field{
	Doc:  "The origins file: the CORS settings and, for each environment, its hosts and the browser origins it admits.",
	Kind: Map,
	Fields: []Field{
		corsField,
		{Name: "environments", Doc: "The environments, each with its hosts and the browser origins it admits.", Kind: List, Required: true,
			Rule: "At least one environment.",
			Item: &Field{Doc: "One environment.", Kind: Map, Fields: slices.Concat([]Field{
				{Name: "name", Doc: "The environment's name.", Kind: Text, Required: true,
					Rule: "It names the environment's virtual host. Not empty, and not used by another environment."},
			}, environmentLists)}},
	},
}

// Methods describes the RPC methods file: one entry per method. Which service a method goes to is decided by the
// rpc entry of the proxyfile, not here.
var Methods = Field{
	Doc:  "The RPC methods the gateway routes, each with what the check is told about it.",
	Kind: List,
	Item: &methodField,
}

// methodField is written once and used by both places the RPC methods may be written: the methods file and the
// proxyfile's rpc.methods, so a method is checked by the same rules in both.
var methodField = Field{Doc: "One RPC method.", Kind: Map, Fields: []Field{
	{Name: "service", Doc: "The full name of the method's service.", Kind: Text, Required: true,
		Pattern: `^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`, Expect: "a full service name such as shop.v1.CartService"},
	{Name: "method", Doc: "The method's name.", Kind: Text, Required: true,
		Pattern: `^[A-Za-z_][A-Za-z0-9_]*$`, Expect: "a method name such as AddItem"},
	{Name: "action", Doc: "The action the check is told for this method.", Kind: Text,
		Rule: "Sent to the check in `x-gateway-action`; empty when not written."},
	withRule(texts("reports", "The reports the check is told for this method.", false),
		"Sent to the check in `x-gateway-reports`. No report contains a comma, because the list is sent as one value joined by commas."),
}}
