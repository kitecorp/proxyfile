package format

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reference writes, as a Markdown table, the key f at path and every key under it, depth first in the description's
// order, so the documentation lists the keys, kinds and rules the checker reads. With path "", f is the root of a
// file, which has no row. A row gives the key's path, its Doc, its rule: Required when it is, what the description
// says the value must be, then the key's own Rule; and its class: the Class it names, else its holder's, and class for
// f, which is the class of the key that holds f. One description always gives the same bytes.
func Reference(f Field, path, class string) []byte {
	var b strings.Builder
	b.WriteString("| Key | Meaning | Rule | Class |\n|---|---|---|---|\n")
	if path == "" {
		under(&b, f, "", class)
	} else {
		key(&b, f, path, Field{}, class)
	}
	return []byte(b.String())
}

// key writes the row of f, a key of the Map holder, and the rows under it. The key that selects a choice of an
// Either is required by the choice, not by the file, so its row does not say Required. The OneOf sentence comes after
// the key's Rule, so a Rule that starts with "It" still speaks of the key. class is the holder's class, which f takes
// unless it names its own, and every key under f takes f's.
func key(b *strings.Builder, f Field, path string, holder Field, class string) {
	class = cmp.Or(f.Class, class)
	rule := []string{sentence(form(f))}
	if f.Required && f.Name != holder.Select {
		rule = slices.Insert(rule, 0, "Required.")
	}
	rule = append(rule, f.Rule)
	if slices.Contains(holder.OneOf, f.Name) {
		rule = append(rule, "Exactly one of "+codes(holder.OneOf, "and")+" is written.")
	}
	row(b, path, escape(f.Doc), class, rule...)
	under(b, f, path, class)
}

// under writes the rows of the keys in the value f at path; class is the class of the key that holds the value. A
// list's items and an Either's choices are values, not keys, so they have no row; a Dict's value has one, at the key
// its writer chooses, with that key's rule and the Dict's class.
func under(b *strings.Builder, f Field, path, class string) {
	switch f.Kind {
	case Map:
		for _, field := range f.Fields {
			key(b, field, join(path, field.Name), f, class)
		}
	case List:
		items := path
		if path != "" {
			items += "[]"
		}
		under(b, *f.Item, items, class)
	case Dict:
		value := join(path, "<"+f.Key.Name+">")
		meaning := strings.TrimSpace(escape(f.Key.Doc) + " " + escape(f.Item.Doc))
		row(b, value, meaning, class, sentence(form(*f.Item)), "The "+escape(f.Key.Name)+" is "+escape(f.Key.Expect)+".", f.Item.Rule)
		under(b, *f.Item, value, class)
	case Either:
		for _, choice := range f.Choices {
			under(b, choice, path, class)
		}
	}
}

func row(b *strings.Builder, path, meaning, class string, rule ...string) {
	fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", path, meaning, strings.Join(slices.DeleteFunc(rule, func(s string) bool { return s == "" }), " "), class)
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// form says what a value of f must be, in the words that complete "must be", as the checker's messages do.
func form(f Field) string {
	switch f.Kind {
	case Map:
		return "a mapping"
	case Dict:
		return "a mapping keyed by " + escape(f.Key.Name)
	case List:
		return "a list; each item is " + form(*f.Item)
	case Text:
		if f.Expect == "" {
			return "text"
		}
		return escape(f.Expect)
	case Int:
		if f.Max == 0 {
			return fmt.Sprintf("a whole number of at least %d", f.Min)
		}
		return fmt.Sprintf("a whole number from %d to %d", f.Min, f.Max)
	case Bool:
		return "`true` or `false`"
	case Enum:
		return codes(f.Values, "or")
	case Either:
		// A Text choice's words are added unless the Either's already say them, as "or a file name" does.
		words := escape(f.Expect)
		for _, choice := range f.Choices {
			if choice.Kind == Text && choice.Expect != "" && !strings.Contains(f.Expect, choice.Expect) {
				words += "; as text, " + escape(choice.Expect)
			}
		}
		return words
	}
	return ""
}

// codes writes values in code, joined by commas and the last by conjunction: `a`, `b` and `c`.
func codes(values []string, conjunction string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "`" + value + "`"
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " " + conjunction + " " + quoted[len(quoted)-1]
}

func sentence(words string) string {
	first, size := utf8.DecodeRuneInString(words)
	return string(unicode.ToUpper(first)) + words[size:] + "."
}

// escape keeps the description's plain text as written in Markdown: a * or an _ is not read as emphasis, a [ or a <
// does not start a link or a tag, and a | does not end a table cell.
var escape = strings.NewReplacer(`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`, `<`, `\<`, `|`, `\|`).Replace
