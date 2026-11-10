package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/internal/testsupport"
)

var update = flag.Bool("update", false, "rewrite schema/proxyfile.schema.json")

const schemaFile = "../../schema/proxyfile.schema.json"

// The committed file is the one editors name by URL and the release ships, so it must hold the bytes Schema writes
// from the description today.
func TestSchemaIsCurrent(t *testing.T) {
	got, err := Schema(Proxyfile)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if *update {
		testsupport.WriteFile(t, schemaFile, string(got))
	}
	want, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Schema(Proxyfile) differs from %s; rewrite it with go test ./internal/format/ -run TestSchemaIsCurrent -update", schemaFile)
	}
}

// Editors check files against the schema, so the schema must accept what the program accepts, and a file it refuses
// is refused for the key or the value the row changes.
func TestSchemaAcceptsTheExamples(t *testing.T) {
	schema := compiledSchema(t)
	cases := []struct {
		name, file, extra string
		from, to          string   // replaced once in the example before extra is appended
		want              []string // the innermost validation errors; none when the schema accepts the file
	}{
		{name: "minimal", file: "minimal"},
		{name: "partial", file: "partial"},
		{name: "full", file: "full"},
		{name: "tracing", file: "tracing"},
		// The checker refuses it too (TestCheck, "tag with env and header"); oneOf admits a tag with exactly one of the
		// two.
		{name: "tag with env and header", file: "tracing", from: "request_id: {header: x-request-id}",
			to:   "request_id: {header: x-request-id, env: A}",
			want: []string{`at '/tracing/tags/request_id': 'oneOf' failed, subschemas 0, 1 matched`}},
		{name: "unknown key", file: "full", extra: "listne: 1\n",
			want: []string{`at '': additional properties 'listne' not allowed`}},
		{name: "size in MB", file: "full", from: "heap: 256MiB", to: "heap: 256MB",
			want: []string{`at '/limits/heap': '256MB' does not match pattern '^[1-9][0-9]*(KiB|MiB|GiB)$'`}},
		// The checker refuses it too (TestCheck, "plain number as text"). oneOf reports why each choice refused the item:
		// the first two lines are the rpc entry's choice, the last the route's.
		{name: "plain number as text", file: "full", from: "name: shop,", to: "name: 404,", want: []string{
			`at '/routes/3': additional properties 'name', 'prefix', 'retries', 'to' not allowed`,
			`at '/routes/3': missing property 'rpc'`,
			`at '/routes/3/name': got number, want string`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := os.ReadFile("../../examples/" + c.file + "/gateway.yaml")
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			document := string(data)
			if c.from != "" {
				if !strings.Contains(document, c.from) {
					t.Fatalf("the %s example holds no %q to replace", c.file, c.from)
				}
				document = strings.Replace(document, c.from, c.to, 1)
			}
			var got []string
			if err := schema.Validate(instance(t, document+c.extra)); err != nil {
				var invalid *jsonschema.ValidationError
				if !errors.As(err, &invalid) {
					t.Fatalf("Validate: %v", err)
				}
				got = innermost(invalid)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Validate errors = %q, want %q", got, c.want)
			}
		})
	}
}

// Each kind becomes its JSON Schema form. A Doc becomes the "description" of the value it describes, and a field
// without one gets no "description".
func TestSchemaKinds(t *testing.T) {
	const schemaKey = `"$schema":"https://json-schema.org/draft/2020-12/schema"`
	cases := []struct {
		name string
		spec Field
		want string // the schema with its whitespace removed
	}{
		{name: "text with a pattern and a description", spec: Field{Doc: "A word.", Kind: Text, Pattern: `^[a-z]+$`},
			want: `{` + schemaKey + `,"description":"A word.","pattern":"^[a-z]+$","title":"proxyfile","type":"string"}`},
		{name: "text with neither", spec: Field{Kind: Text},
			want: `{` + schemaKey + `,"title":"proxyfile","type":"string"}`},
		{name: "int with a maximum", spec: Field{Kind: Int, Min: 1, Max: 65535},
			want: `{` + schemaKey + `,"maximum":65535,"minimum":1,"title":"proxyfile","type":"integer"}`},
		{name: "int with no maximum", spec: Field{Kind: Int, Min: 0},
			want: `{` + schemaKey + `,"minimum":0,"title":"proxyfile","type":"integer"}`},
		{name: "bool", spec: Field{Kind: Bool},
			want: `{` + schemaKey + `,"title":"proxyfile","type":"boolean"}`},
		{name: "enum", spec: Field{Kind: Enum, Values: []string{"allow", "deny"}},
			want: `{` + schemaKey + `,"enum":["allow","deny"],"title":"proxyfile"}`},
		{name: "list", spec: Field{Kind: List, Item: &Field{Kind: Text, Pattern: `^/`}},
			want: `{` + schemaKey + `,"items":{"pattern":"^/","type":"string"},"title":"proxyfile","type":"array"}`},
		{name: "dict with a described value", spec: Field{Kind: Dict,
			Key:  &Field{Name: "country code", Kind: Text, Pattern: `^[A-Z]{2}$`, Expect: "two capitals"},
			Item: &Field{Doc: "The country's name.", Kind: Text}},
			want: `{` + schemaKey + `,"additionalProperties":{"description":"The country's name.","type":"string"},` +
				`"propertyNames":{"pattern":"^[A-Z]{2}$"},"title":"proxyfile","type":"object"}`},
		{name: "map with a required key and one of two keys", spec: Field{Kind: Map, OneOf: []string{"prefix", "path"},
			Fields: []Field{{Name: "name", Kind: Text, Required: true}, {Name: "prefix", Kind: Text}, {Name: "path", Kind: Text}}},
			want: `{` + schemaKey + `,"additionalProperties":false,"oneOf":[{"required":["prefix"]},{"required":["path"]}],` +
				`"properties":{"name":{"type":"string"},"path":{"type":"string"},"prefix":{"type":"string"}},` +
				`"required":["name"],"title":"proxyfile","type":"object"}`},
		{name: "map with no required key", spec: Field{Kind: Map, Fields: []Field{{Name: "admin", Kind: Bool}}},
			want: `{` + schemaKey + `,"additionalProperties":false,"properties":{"admin":{"type":"boolean"}},` +
				`"title":"proxyfile","type":"object"}`},
		{name: "either", spec: Field{Kind: Either, Expect: "a name, or a mapping with service", Choices: []Field{
			{Kind: Text, Pattern: `^[a-z]+$`},
			{Doc: "A target.", Kind: Map, Select: "service", Fields: []Field{{Name: "service", Kind: Text, Required: true}}},
		}},
			want: `{` + schemaKey + `,"oneOf":[{"pattern":"^[a-z]+$","type":"string"},{"additionalProperties":false,` +
				`"description":"A target.","properties":{"service":{"type":"string"}},"required":["service"],"type":"object"}],` +
				`"title":"proxyfile"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Schema(c.spec)
			if err != nil {
				t.Fatalf("Schema: %v", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, got); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if compact.String() != c.want {
				t.Errorf("Schema = %s, want %s", compact.String(), c.want)
			}
		})
	}
}

// A field whose kind the schema does not know would come out as a schema that accepts any value, so Schema refuses
// the description instead.
func TestSchemaUnknownKind(t *testing.T) {
	_, err := Schema(Field{Kind: Map, Fields: []Field{{Name: "port"}}})
	want := `the key "port" has kind 0, which has no JSON Schema form`
	if err == nil || err.Error() != want {
		t.Errorf("Schema error = %v, want %s", err, want)
	}
}

func compiledSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := Schema(Proxyfile)
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("proxyfile.schema.json", doc); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	schema, err := compiler.Compile("proxyfile.schema.json")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return schema
}

// instance reads a YAML document into the JSON value a schema check validates.
func instance(t *testing.T, document string) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal([]byte(document), &value); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	return inst
}

// innermost returns the errors at the bottom of err's tree, each naming a value and the rule it breaks. The error at
// the top names the schema by an absolute file URL, which changes with the checkout's directory. The validator lists
// the keys a mapping may not have in the order it ranges over the instance's Go map (validator.go, the
// AdditionalProperties error), which changes from run to run, so they are sorted before the message is written.
func innermost(err *jsonschema.ValidationError) []string {
	if len(err.Causes) == 0 {
		if additional, ok := err.ErrorKind.(*kind.AdditionalProperties); ok {
			slices.Sort(additional.Properties)
		}
		return []string{err.Error()}
	}
	var found []string
	for _, cause := range err.Causes {
		found = append(found, innermost(cause)...)
	}
	slices.Sort(found)
	return found
}
