// Package origins reads the browser origins: the CORS settings every environment shares and, for each environment,
// its hosts and the browser origins it admits. They are written in the proxyfile, or in an origins file it names.
package origins

import (
	"regexp"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/input"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// File is the origins after reading, from either place, with every rule below checked.
type File struct {
	CORS         CORS
	Environments []Environment // written order
}

type CORS struct{ AllowMethods, AllowHeaders, ExposeHeaders, MaxAge string } // "" when not written

type Environment struct {
	Name                     string
	Hosts, Origins, Patterns []string  // nil when not written or empty
	Pos                      model.Pos // the environment's mapping in an origins file; its key in the proxyfile
}

// list is the rule of one list of an environment: each value has the form valid admits, and the values are sorted
// in byte order with none twice, so each value has one place, and adding or removing one moves no other.
type list struct {
	key, value string // the list's key and one value of it, as the messages name them
	valid      func(string) bool
	expect     string // completes `the <value> "<text>" is not ...`
}

var (
	// Envoy lowers each domain before it stores it (RouteMatcher in source/common/router/config_impl.cc), so two
	// hosts that differ only in case would be one domain written twice there; admitting lower case only makes the
	// check's comparison the same as Envoy's.
	hostForm = regexp.MustCompile(`^[a-z0-9.-]+$`)
	// A browser sends Origin as scheme://host[:port] with no path (the HTML standard's serialization of an origin), so
	// an origin with a path would never match. Case would not stop a match, because the generator writes each origin's
	// matcher with ignore_case: true; lower case keeps one spelling of each origin, so the order rule sees the same
	// origin written twice.
	originForm  = regexp.MustCompile(`^https?://[a-z0-9.-]+(:[0-9]{1,5})?$`)
	patternForm = regexp.MustCompile(`^https?://[a-z0-9.*-]+(:([0-9]{1,5}|\*))?$`)

	hostList   = list{key: "hosts", value: "host", valid: hostForm.MatchString, expect: "a lower-case host name without a port"}
	originList = list{key: "origins", value: "origin", valid: originForm.MatchString, expect: "scheme://host[:port] in lower case"}
	// A value without a * belongs in origins, which compares it exactly.
	patternList = list{key: "patterns", value: "pattern", expect: "scheme://host[:port] with a *",
		valid: func(p string) bool { return strings.Contains(p, "*") && patternForm.MatchString(p) }}
)

// Read reads the origins file at path, which is JSON read as YAML so that a problem names its line. It returns the
// problems of the first step that finds any: reading the file, its syntax, the format check, then the rules below.
func Read(path string) (*File, []model.Problem) {
	root, problems := input.Checked(path, format.Origins)
	if problems != nil {
		return nil, problems
	}
	holder := root.Get("environments")
	var list []writtenEnvironment
	for _, item := range holder.Items {
		name := item.Get("name")
		list = append(list, writtenEnvironment{name: name.Text, namePos: name.Pos, pos: item.Pos, item: item})
	}
	return read(root.Get("cors"), holder, "the file names no environment", list)
}

// Inline reads the origins written in the proxyfile: environments is its origins mapping, which the format check
// accepted, and cors its cors mapping, or nil when not written. Each entry is one environment, named by its key.
func Inline(cors, environments *tree.Node) (*File, []model.Problem) {
	var list []writtenEnvironment
	for _, entry := range environments.Entries {
		list = append(list, writtenEnvironment{name: entry.Key, namePos: entry.KeyPos, pos: entry.KeyPos, item: entry.Value})
	}
	return read(cors, environments, `"origins" names no environment`, list)
}

// writtenEnvironment is one environment as either place writes it: its name, where a problem with the name points,
// where the environment is, and the mapping of its lists.
type writtenEnvironment struct {
	name         string
	namePos, pos model.Pos
	item         *tree.Node
}

// read fills the File from the CORS mapping and the environments, applying every rule below, and returns it or the
// problems, sorted. holder is the node that holds the environments, and none the message when it holds none, which
// each place words for itself.
func read(cors, holder *tree.Node, none string, list []writtenEnvironment) (*File, []model.Problem) {
	r := reader{names: map[string]bool{}, firstWithHost: map[string]int{}}
	r.corsValues(cors)
	file := &File{
		CORS: CORS{
			AllowMethods:  cors.GetText("allow_methods"),
			AllowHeaders:  cors.GetText("allow_headers"),
			ExposeHeaders: cors.GetText("expose_headers"),
			MaxAge:        cors.GetText("max_age"),
		},
		Environments: r.environments(holder, none, list),
	}
	if problems := r.Sorted(); problems != nil {
		return nil, problems
	}
	return file, nil
}

// corsHeaders names the response header the CORS filter sends each cors value in. max_age is left out: its pattern
// admits digits only.
var corsHeaders = []struct{ key, header string }{
	{"allow_methods", "Access-Control-Allow-Methods"},
	{"allow_headers", "Access-Control-Allow-Headers"},
	{"expose_headers", "Access-Control-Expose-Headers"},
}

// corsValues refuses a cors value holding NUL, a newline or a carriage return (model.HeaderValueRefused). Envoy 1.40.2
// loads a file with one there (scripts/validate-envoy.sh), so without this the browser would be the first to meet it.
func (r *reader) corsValues(cors *tree.Node) {
	for _, c := range corsHeaders {
		value := cors.Get(c.key)
		if value == nil {
			continue
		}
		for _, name := range model.HeaderValueRefused(value.Text) {
			r.Add(value.Pos, "the value %q of cors.%s contains %s, which the header %s cannot hold", value.Text, c.key, name, c.header)
		}
	}
}

// reader applies the rules while it fills the File, because each problem names the position of a value and the
// File keeps none.
type reader struct {
	model.Problems
	// Both are only looked up, never ranged.
	names         map[string]bool
	firstWithHost map[string]int // the index of the first environment that lists the host
}

func (r *reader) environments(holder *tree.Node, none string, list []writtenEnvironment) []Environment {
	// The generator writes one virtual host per environment, and Envoy's router answers a request no route takes with
	// 404 (source/common/router/router.cc), so origins with no environment would refuse every request.
	if len(list) == 0 {
		r.Add(holder.Pos, "%s", none)
	}
	var environments []Environment
	for i, w := range list {
		name, item, hosts := w.name, w.item, w.item.Get("hosts")
		// The generator names each virtual host after its environment, and Envoy refuses a virtual host with an empty
		// name (min_len 1 on VirtualHost.name) or with no domain (min_items 1 on VirtualHost.domains), both in
		// api/envoy/config/route/v3/route_components.proto. The message about an empty name counts the environment from
		// 1, because there is no name to quote; the environment's other messages quote the empty name. A second
		// environment with no name is not also reported as listed twice.
		switch {
		case name == "":
			r.Add(w.namePos, "environment %d: the name is empty", i+1)
		case r.names[name]:
			r.Add(w.pos, "environment %q is listed twice", name)
		}
		r.names[name] = true
		if len(hosts.Items) == 0 {
			r.Add(hosts.Pos, "environment %q: hosts is empty", name)
		}
		r.hostsOnce(i, name, hosts)
		environments = append(environments, Environment{
			Name:     name,
			Hosts:    r.values(name, item, hostList),
			Origins:  r.values(name, item, originList),
			Patterns: r.values(name, item, patternList),
			Pos:      w.pos,
		})
	}
	return environments
}

// values returns the texts of one list of an environment, after it checks each against the list's form and the
// whole against the list's order.
func (r *reader) values(environment string, item *tree.Node, rule list) []string {
	n := item.Get(rule.key)
	if n == nil {
		return nil
	}
	var texts []string
	for _, value := range n.Items {
		if !rule.valid(value.Text) {
			r.Add(value.Pos, "environment %q: the %s %q is not %s", environment, rule.value, value.Text, rule.expect)
		}
		texts = append(texts, value.Text)
	}
	if !slices.Equal(texts, slices.Compact(slices.Sorted(slices.Values(texts)))) {
		r.Add(n.Pos, "environment %q: the %s are not sorted, or list one twice", environment, rule.key)
	}
	return texts
}

// Envoy refuses a domain written twice in one route configuration ("Only unique values for domains are permitted",
// source/common/router/config_impl.cc), and the hosts of every environment go into the one route table. A host
// listed twice in one environment is left to the order rule, which refuses a value listed twice, so each mistake
// gets the one message that describes it.
func (r *reader) hostsOnce(environment int, name string, hosts *tree.Node) {
	for _, host := range hosts.Items {
		first, seen := r.firstWithHost[host.Text]
		if !seen {
			r.firstWithHost[host.Text] = environment
		} else if first != environment {
			r.Add(host.Pos, "environment %q: the host %q is in two environments", name, host.Text)
		}
	}
}

// PatternRegex keeps the meaning a Spring allowed-origin-pattern has: every character literal but *, which
// matches any run, dots included.
func PatternRegex(pattern string) string {
	// QuoteMeta quotes every character RE2 reads as an operator, * among them, so the pattern is split at each *
	// first and the parts are joined again with .*.
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return "^" + strings.Join(parts, ".*") + "$"
}

// SameOriginRegex admits the service's own origin on one of its hosts, on either scheme and any port.
func SameOriginRegex(host string) string {
	return "^https?://" + regexp.QuoteMeta(host) + "(:[0-9]+)?$"
}
