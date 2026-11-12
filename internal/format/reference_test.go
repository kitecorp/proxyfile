package format

import (
	"cmp"
	"testing"
)

const tableHead = "| Key | Meaning | Rule | Class |\n|---|---|---|---|\n"

// Each kind gives its rule in words, a key's own Rule follows them, and the rows come depth first in the
// description's order, with a list's items and an Either's choices under the key that holds them.
func TestReference(t *testing.T) {
	services := Field{Name: "services", Doc: "The services, by name.", Kind: Dict, Required: true,
		Key: &Field{Name: "service name", Kind: Text, Pattern: `^[a-z]+$`, Expect: "lower-case letters"},
		Item: &Field{Doc: "One service.", Kind: Map, Rule: "It has a port.", Fields: []Field{
			{Name: "http", Doc: "Its port.", Kind: Int, Min: 1, Max: 9},
		}}}
	cases := []struct {
		name string
		spec Field
		path string
		// class is the class Reference is given for the key at path; every case that names none is given core.
		class string
		want  string
	}{
		{name: "scalars at the root, with a rule of their own", path: "", spec: Field{Kind: Map, Fields: []Field{
			{Name: "listen", Doc: "The port.", Kind: Int, Required: true, Min: 1, Max: 65535, Rule: "Not the same as `admin`."},
			{Name: "tries", Doc: "How many tries.", Kind: Int},
			{Name: "level", Doc: "How much is logged.", Kind: Enum, Values: []string{"low", "mid", "high"}},
			{Name: "auth", Doc: "off skips the check.", Kind: Enum, Values: []string{"off"}},
			{Name: "on", Doc: "true turns it on.", Kind: Bool},
			{Name: "only", Doc: "true keeps it on.", Kind: True},
			{Name: "note", Doc: "Any text.", Kind: Text},
		}}, want: tableHead +
			"| `listen` | The port. | Required. A whole number from 1 to 65535. Not the same as `admin`. | core |\n" +
			"| `tries` | How many tries. | A whole number of at least 0. | core |\n" +
			"| `level` | How much is logged. | `low`, `mid` or `high`. | core |\n" +
			"| `auth` | off skips the check. | `off`. | core |\n" +
			"| `on` | true turns it on. | `true` or `false`. | core |\n" +
			"| `only` | true keeps it on. | `true`. | core |\n" +
			"| `note` | Any text. | Text. | core |\n"},
		// The description's plain text is escaped, so a * or an _ in a Doc or an Expect is shown, not read as
		// emphasis, and a | does not end the cell.
		{name: "a list of text, escaped", path: "", spec: Field{Kind: Map, Fields: []Field{
			{Name: "send", Doc: "Headers | names; a name ending in * is a prefix.", Kind: List,
				Item: &Field{Kind: Text, Pattern: `^[a-z_]+\*?$`, Expect: "letters and _, or a prefix ending in *"}},
		}}, want: tableHead +
			"| `send` | Headers \\| names; a name ending in \\* is a prefix. | A list; each item is letters and \\_, or a prefix ending in \\*. | core |\n"},
		// The key that selects a choice is required only by being written, so its row does not say Required, and
		// each key of a OneOf says which keys it excludes, after its own Rule, which speaks of the key.
		{name: "a list of choices", path: "", spec: Field{Kind: Map, Fields: []Field{
			{Name: "routes", Doc: "The routes.", Kind: List, Required: true, Item: &Field{Kind: Either, Expect: "a route or a mark",
				Choices: []Field{
					{Doc: "A mark.", Kind: Map, Select: "mark", Fields: []Field{
						{Name: "mark", Doc: "Where the marked routes go.", Kind: Map, Required: true, Fields: []Field{
							{Name: "tries", Doc: "How many tries.", Kind: Int, Min: 0, Max: 3},
						}},
					}},
					{Doc: "A route.", Kind: Map, OneOf: []string{"prefix", "path"}, Fields: []Field{
						{Name: "prefix", Doc: "A prefix.", Kind: Text},
						{Name: "path", Doc: "A path.", Kind: Text, Rule: "It starts with /."},
					}},
				}}},
		}}, want: tableHead +
			"| `routes` | The routes. | Required. A list; each item is a route or a mark. | core |\n" +
			"| `routes[].mark` | Where the marked routes go. | A mapping. | core |\n" +
			"| `routes[].mark.tries` | How many tries. | A whole number from 0 to 3. | core |\n" +
			"| `routes[].prefix` | A prefix. | Text. Exactly one of `prefix` and `path` is written. | core |\n" +
			"| `routes[].path` | A path. | Text. It starts with /. Exactly one of `prefix` and `path` is written. | core |\n"},
		// A key with a path has its own row first. A Dict's value has a row at the key its writer chooses, which
		// says the key's rule.
		{name: "a dictionary by its path", path: "services", spec: services, want: tableHead +
			"| `services` | The services, by name. | Required. A mapping keyed by service name. | core |\n" +
			"| `services.<service name>` | One service. | A mapping. The service name is lower-case letters. It has a port. | core |\n" +
			"| `services.<service name>.http` | Its port. | A whole number from 1 to 9. | core |\n"},
		// A Dict choice's value has its row at the path of the Either, and a Text choice adds its words to the
		// Either's unless they already hold them. A key's Doc says what a key means when the value has none.
		{name: "an either", path: "origins", spec: Field{Name: "origins", Doc: "The origins, or a file.", Kind: Either,
			Expect: "a mapping, or a file name", Choices: []Field{
				{Kind: Text, Pattern: `^\S+$`, Expect: "a file name"},
				{Kind: Dict, Key: &Field{Name: "environment", Doc: "The environment's name.", Kind: Text, Pattern: `.`, Expect: "one character or more"},
					Item: &Field{Kind: Either, Expect: "a host or a mapping", Rule: "Not empty.", Choices: []Field{
						{Kind: Text, Expect: "a host name"},
						{Kind: Map, Fields: []Field{{Name: "hosts", Doc: "The hosts.", Kind: List, Required: true, Item: &Field{Kind: Text}}}},
					}}},
			}}, want: tableHead +
			"| `origins` | The origins, or a file. | A mapping, or a file name. | core |\n" +
			"| `origins.<environment>` | The environment's name. | A host or a mapping; as text, a host name. The environment is one character or more. Not empty. | core |\n" +
			"| `origins.<environment>.hosts` | The hosts. | Required. A list; each item is text. | core |\n"},
		// A file that is a list, as the methods file is, names its items' keys alone.
		{name: "a list at the root", path: "", spec: Field{Kind: List, Item: &Field{Doc: "One method.", Kind: Map, Fields: []Field{
			{Name: "method", Doc: "The method's name.", Kind: Text, Required: true, Pattern: `^[A-Za-z]+$`, Expect: "a name such as AddItem"},
		}}}, want: tableHead +
			"| `method` | The method's name. | Required. A name such as AddItem. | core |\n"},
		// A class written on a key holds for every key under it, and a key that names none takes the class it is given.
		{name: "a class holds for the keys under it", path: "", spec: Field{Kind: Map, Fields: []Field{
			{Name: "listen", Doc: "The port.", Kind: Int},
			{Name: "proxy", Doc: "Its own settings.", Kind: Map, Class: "proxy", Fields: []Field{
				{Name: "heap", Doc: "Its heap.", Kind: Text},
				{Name: "tags", Doc: "Its tags.", Kind: Dict, Key: &Field{Name: "tag name", Kind: Text, Pattern: `^[a-z]+$`, Expect: "lower-case letters"},
					Item: &Field{Doc: "One tag.", Kind: Text}},
			}},
		}}, want: tableHead +
			"| `listen` | The port. | A whole number of at least 0. | core |\n" +
			"| `proxy` | Its own settings. | A mapping. | proxy |\n" +
			"| `proxy.heap` | Its heap. | Text. | proxy |\n" +
			"| `proxy.tags` | Its tags. | A mapping keyed by tag name. | proxy |\n" +
			"| `proxy.tags.<tag name>` | One tag. | Text. The tag name is lower-case letters. | proxy |\n"},
		{name: "a key by its path, in the class of its holder", path: "proxy.trace", class: "proxy", spec: Field{Name: "trace",
			Doc: "Its tracing.", Kind: Map, Fields: []Field{{Name: "rate", Doc: "How many are traced.", Kind: Int}}}, want: tableHead +
			"| `proxy.trace` | Its tracing. | A mapping. | proxy |\n" +
			"| `proxy.trace.rate` | How many are traced. | A whole number of at least 0. | proxy |\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Reference(c.spec, c.path, cmp.Or(c.class, core))); got != c.want {
				t.Errorf("Reference =\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}
