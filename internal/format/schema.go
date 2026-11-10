package format

import (
	"encoding/json"
	"fmt"
)

// Schema writes spec as a JSON Schema (draft 2020-12), so an editor checks a file with the keys, kinds and patterns
// the checker uses. Every schema object is a Go map, which encoding/json writes with its keys sorted, and every list
// keeps the description's order, so one description always gives the same bytes.
func Schema(spec Field) ([]byte, error) {
	root, err := schemaOf(spec)
	if err != nil {
		return nil, err
	}
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = "proxyfile"
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// schemaOf returns the JSON Schema of f. Each pattern is written through unchanged, so an editor runs the text the
// checker compiles; Field.Pattern says which forms that allows.
func schemaOf(f Field) (map[string]any, error) {
	s := map[string]any{}
	if f.Doc != "" {
		s["description"] = f.Doc
	}
	switch f.Kind {
	case Map:
		properties := map[string]any{}
		var required []string
		for _, field := range f.Fields {
			child, err := schemaOf(field)
			if err != nil {
				return nil, err
			}
			properties[field.Name] = child
			if field.Required {
				required = append(required, field.Name)
			}
		}
		s["type"] = "object"
		s["properties"] = properties
		// A key the description does not name is refused, as the checker refuses it. That also keeps a mapping in one
		// Map choice of an Either, as oneOf needs. In routes, the rpc entry's choice requires rpc, its Select key, and
		// the route's choice requires name and to; each choice refuses the other's keys, because it does not name them.
		// That rests on the description: this code reads no Select.
		s["additionalProperties"] = false
		if len(required) > 0 {
			s["required"] = required
		}
		if len(f.OneOf) > 0 {
			branches := make([]any, len(f.OneOf))
			for i, key := range f.OneOf {
				branches[i] = map[string]any{"required": []string{key}}
			}
			s["oneOf"] = branches
		}
	case Dict:
		value, err := schemaOf(*f.Item)
		if err != nil {
			return nil, err
		}
		s["type"] = "object"
		s["propertyNames"] = map[string]any{"pattern": f.Key.Pattern}
		s["additionalProperties"] = value
	case List:
		item, err := schemaOf(*f.Item)
		if err != nil {
			return nil, err
		}
		s["type"] = "array"
		s["items"] = item
	case Text:
		s["type"] = "string"
		if f.Pattern != "" {
			s["pattern"] = f.Pattern
		}
	case Int:
		s["type"] = "integer"
		s["minimum"] = f.Min
		if f.Max != 0 {
			s["maximum"] = f.Max
		}
	case Bool:
		s["type"] = "boolean"
	case Enum:
		s["enum"] = f.Values
	case Either:
		choices := make([]any, len(f.Choices))
		for i, choice := range f.Choices {
			var err error
			if choices[i], err = schemaOf(choice); err != nil {
				return nil, err
			}
		}
		s["oneOf"] = choices
	default:
		return nil, fmt.Errorf("the key %q has kind %d, which has no JSON Schema form", f.Name, f.Kind)
	}
	return s, nil
}
