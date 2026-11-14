package format

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// patterns holds every pattern of the three descriptions, compiled when the package loads so that a pattern Go's
// regexp refuses fails every test of the package, not only a test that reaches its key. It is only looked up, never
// ranged.
var patterns = compiled(Proxyfile, Origins, Methods)

// pattern compiles a pattern of any other description each time it is used and does not store it, because two
// goroutines may run Check at once and a Go map written by both is a data race.
func pattern(text string) *regexp.Regexp {
	if known := patterns[text]; known != nil {
		return known
	}
	return regexp.MustCompile(text)
}

func compiled(specs ...Field) map[string]*regexp.Regexp {
	found := map[string]*regexp.Regexp{}
	var walk func(f Field)
	walk = func(f Field) {
		if f.Pattern != "" && found[f.Pattern] == nil {
			found[f.Pattern] = regexp.MustCompile(f.Pattern)
		}
		for _, child := range slices.Concat(f.Fields, f.Choices) {
			walk(child)
		}
		for _, child := range []*Field{f.Item, f.Key} {
			if child != nil {
				walk(*child)
			}
		}
	}
	for _, spec := range specs {
		walk(spec)
	}
	return found
}

// Check returns every problem of n against spec, sorted, so one run names them all.
func Check(spec Field, n *tree.Node) []model.Problem {
	c := checker{}
	c.value(spec, n, "the file")
	return c.Sorted()
}

type checker struct {
	model.Problems
}

// value checks n against f. subject names n in a message: "the file" for the root, the quoted key for a mapping's
// value, and for a list item its text and its list, as in `"x-secret" in "send"`.
func (c *checker) value(f Field, n *tree.Node, subject string) {
	switch f.Kind {
	case Map:
		if c.kind(n, tree.Map, subject, "a mapping") {
			c.mapping(f, n)
		}
	case Dict:
		if c.kind(n, tree.Map, subject, "a mapping") {
			c.dict(f, n)
		}
	case List:
		if c.kind(n, tree.List, subject, "a list") {
			for _, item := range n.Items {
				c.value(*f.Item, item, itemSubject(item, subject))
			}
		}
	case Text, Int, Bool, True, Enum:
		if c.kind(n, tree.Scalar, subject, "a single value") {
			c.scalar(f, n, subject)
		}
	case Either:
		if choice := choose(f.Choices, n); choice != nil {
			c.value(*choice, n, subject)
		} else {
			c.Add(n.Pos, "%s must be %s", subject, f.Expect)
		}
	}
}

func (c *checker) kind(n *tree.Node, want tree.Kind, subject, expect string) bool {
	if n.Kind != want {
		c.Add(n.Pos, "%s must be %s", subject, expect)
		return false
	}
	return true
}

// A list item that is not a single value has no text to quote, so it is named by its list alone.
func itemSubject(item *tree.Node, list string) string {
	if item.Kind == tree.Scalar {
		return fmt.Sprintf("%q in %s", item.Text, list)
	}
	return "an item in " + list
}

func (c *checker) mapping(f Field, n *tree.Node) {
	for _, entry := range n.Entries {
		field := f.Child(entry.Key)
		switch to, moved := f.Moved[entry.Key]; {
		case field != nil:
			c.value(*field, entry.Value, strconv.Quote(entry.Key))
		case moved:
			c.Add(entry.KeyPos, "%q moved to %s in proxyfile 2.0", entry.Key, to)
		case f.Reserved:
			c.Add(entry.KeyPos, "%q in %s: the %s block takes no key in this version; see docs/portability.md", entry.Key, f.Name, f.Name)
		default:
			c.Add(entry.KeyPos, "unknown key %q; known here: %s", entry.Key, strings.Join(sortedNames(f.Fields), ", "))
		}
	}
	for _, field := range f.Fields {
		if field.Required && n.Get(field.Name) == nil {
			c.Add(n.Pos, "%q is required here", field.Name)
		}
	}
	if len(f.OneOf) > 0 {
		written := 0
		for _, key := range f.OneOf {
			if n.Get(key) != nil {
				written++
			}
		}
		if written != 1 {
			c.Add(n.Pos, "exactly one of %s is needed here", strings.Join(f.OneOf, ", "))
		}
	}
}

// Child returns the description of the key name of a Map, or nil. A later stage that refuses a value the check
// accepted quotes the child's Expect, so both stages describe a value in the same words.
func (f *Field) Child(name string) *Field {
	for i := range f.Fields {
		if f.Fields[i].Name == name {
			return &f.Fields[i]
		}
	}
	return nil
}

func sortedNames(fields []Field) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	slices.Sort(names)
	return names
}

// A key that breaks the rule is reported and its value is still checked, so one run names both.
func (c *checker) dict(f Field, n *tree.Node) {
	for _, entry := range n.Entries {
		if !pattern(f.Key.Pattern).MatchString(entry.Key) {
			c.Add(entry.KeyPos, "the %s %q must be %s", f.Key.Name, entry.Key, f.Key.Expect)
		}
		c.value(*f.Item, entry.Value, strconv.Quote(entry.Key))
	}
}

func (c *checker) scalar(f Field, n *tree.Node, subject string) {
	switch f.Kind {
	case Text:
		// A value that breaks the pattern gets the pattern's message alone, because quoting it would not be enough.
		if f.Pattern != "" && !pattern(f.Pattern).MatchString(n.Text) {
			c.Add(n.Pos, "%s must be %s", subject, f.Expect)
		} else if n.NumberOrBool {
			c.Add(n.Pos, "%s must be text; quote it", subject)
		}
	case Int:
		if n.Quoted || !wholeNumberIn(n.Text, f.Min, f.Max) {
			if f.Max == 0 {
				c.Add(n.Pos, "%s must be a whole number of at least %d%s", subject, f.Min, quotedNote(n))
			} else {
				c.Add(n.Pos, "%s must be a whole number from %d to %d%s", subject, f.Min, f.Max, quotedNote(n))
			}
		}
	case Bool:
		if n.Quoted || (n.Text != "true" && n.Text != "false") {
			c.Add(n.Pos, "%s must be true or false%s", subject, quotedNote(n))
		}
	case True:
		if n.Quoted || n.Text != "true" {
			c.Add(n.Pos, "%s must be true%s", subject, quotedNote(n))
		}
	case Enum:
		if !slices.Contains(f.Values, n.Text) {
			c.Add(n.Pos, "%s must be one of: %s", subject, strings.Join(f.Values, ", "))
		}
	}
}

// A value written in quotes or as a block is text to a YAML reader, so the JSON Schema, which asks for a number or a
// boolean there, refuses it, and the check refuses it too; a plain number or boolean where text belongs is refused
// for the same reason (scalar's Text case). The two agree on kinds, required keys and unknown keys. Where they differ,
// the check is the stricter side: where a boolean or a whole number belongs it refuses YAML 1.2 forms such as True and
// 8000.0, which an editor may accept; where text belongs it refuses YAML 1.1 spellings of a number such as 1_000 and
// 0b1, which an editor reading YAML 1.2 takes as text (tree.Node.NumberOrBool). An Enum value is the same text quoted
// or not, and is accepted either way.
func quotedNote(n *tree.Node) string {
	if n.Quoted {
		return ", not quoted or block text"
	}
	return ""
}

// Only plain decimal digits, with a minus sign when negative, are a whole number here, because YAML readers
// disagree on the other forms: YAML 1.1 reads 010 as 8 and 1_000 as 1000, the YAML 1.2 core schema reads 010 as 10
// and 1_000 as text. Writing the number back must give the same text, which refuses a plus sign, a leading zero, an
// underscore and more digits than an int64 holds. It parses 64 bits whatever the platform's int, so a uint32 limit
// such as 4294967295 reads the same on a 32-bit build.
func wholeNumberIn(text string, lowest, highest int64) bool {
	number, err := strconv.ParseInt(text, 10, 64)
	if err != nil || strconv.FormatInt(number, 10) != text {
		return false
	}
	return number >= lowest && (highest == 0 || number <= highest)
}

// choose picks the choice that fits n: for a mapping, the Map choice whose Select key it has, else the Map or Dict
// choice without Select, as a Dict's keys are the writer's and none can select it; for a single value, the Text
// choice; for a list, the List choice. It returns nil when no choice fits n's kind, and a mapping that has the Select
// key of two choices fits none, so it gets the Either's message instead of one choice's unknown key.
func choose(choices []Field, n *tree.Node) *Field {
	var selected, fallback *Field
	for i := range choices {
		choice := &choices[i]
		switch {
		case n.Kind == tree.Map && choice.Kind == Map && choice.Select != "":
			if n.Get(choice.Select) != nil {
				if selected != nil {
					return nil
				}
				selected = choice
			}
		case n.Kind == tree.Map && (choice.Kind == Map || choice.Kind == Dict), n.Kind == tree.Scalar && choice.Kind == Text,
			n.Kind == tree.List && choice.Kind == List:
			fallback = choice
		}
	}
	if selected != nil {
		return selected
	}
	return fallback
}
