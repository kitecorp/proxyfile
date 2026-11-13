package format

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// Level is how a target's generator carries a core key.
type Level int

const (
	Supported Level = iota + 1 // written as the format describes it
	Partial                    // written with a difference, which the declaration's Note says
	Refused                    // not written: render refuses a file that writes the key, rather than write less
)

// Support is a target's declaration for one core key. Note is "" for Supported; for Partial it says the difference in
// one sentence, and for Refused what the proxy offers instead. The support table and the refusal both quote it.
type Support struct {
	Level Level
	Note  string
}

// Target is a proxy with a generator: Name is the --target value, Title the support table's column heading, and Keys
// its declaration for each core key, by the key's path as CoreKeys gives it.
type Target struct {
	Name, Title string
	Keys        map[string]Support
}

// Targets are the proxies with a generator, in the order of the support table's columns. The Envoy generator writes
// every key of the format, so it declares each one supported. TestTargetsDeclareEveryCoreKey keeps each target's
// declarations complete, so a key added to the format cannot go undeclared.
var Targets = []Target{{Name: "envoy", Title: "Envoy", Keys: allSupported(CoreKeys(Proxyfile))}}

func allSupported(keys []string) map[string]Support {
	declared := make(map[string]Support, len(keys))
	for _, key := range keys {
		declared[key] = Support{Level: Supported}
	}
	return declared
}

// TargetNamed returns the target whose Name is name. It reads Targets at each call, so a test may declare a target of
// its own.
func TargetNamed(name string) (Target, bool) {
	i := slices.IndexFunc(Targets, func(t Target) bool { return t.Name == name })
	if i < 0 {
		return Target{}, false
	}
	return Targets[i], true
}

// SupportMatrix writes, as a Markdown table, each target's declaration for every core key under f: a column for each
// target, in the order given, and a row for each key, in CoreKeys' order.
func SupportMatrix(f Field, targets []Target) []byte {
	var b strings.Builder
	b.WriteString("| Key |")
	for _, t := range targets {
		b.WriteString(" " + t.Title + " |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(targets)) + "\n")
	for _, key := range CoreKeys(f) {
		b.WriteString("| `" + key + "` |")
		for _, t := range targets {
			b.WriteString(" " + cell(t.Keys[key]) + " |")
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// cell words one declaration in the support table. A key a target does not declare is written as such, though
// TestTargetsDeclareEveryCoreKey keeps every shipped target from leaving one out.
func cell(s Support) string {
	switch s.Level {
	case Supported:
		return "supported"
	case Partial:
		return "partial: " + escape(s.Note)
	case Refused:
		return "refused: " + escape(s.Note)
	}
	return "not declared"
}

// RefusedMessage is the problem of key, written in a file rendered for t, which refuses it: the key as the support
// table names it, and what the proxy offers instead.
func RefusedMessage(key string, t Target) string {
	return fmt.Sprintf("%q is refused by --target %s: %s", key, t.Name, t.Keys[key].Note)
}

// Refusals returns one problem for each key written in root whose path t declares Refused, at the key, sorted, so a
// render for t names every key it cannot carry instead of writing a weaker configuration. It follows the description
// f as Check does, and reads a root Check accepted. f is the proxyfile's description, or the origins file's or the
// methods file's, whose keys are named by the path the proxyfile writes them at (Field.Place).
func Refusals(f Field, root *tree.Node, t Target) []model.Problem {
	r := refuser{target: t}
	r.value(f, root, "")
	return r.Sorted()
}

type refuser struct {
	model.Problems
	target Target
}

// value walks n, a value of f at path, as Check walks it: a Dict's keys by the name of its key, a list's items by [],
// and an Either by the choice that fits n.
func (r *refuser) value(f Field, n *tree.Node, path string) {
	path = cmp.Or(f.Place, path)
	switch {
	case f.Kind == Map && n.Kind == tree.Map:
		for _, entry := range n.Entries {
			// A key the description does not hold here, in a reserved block or at its 1.x place, is the check's to
			// refuse, and no target declares it.
			if field := f.Child(entry.Key); field != nil {
				r.key(entry, *field, join(path, entry.Key))
			}
		}
	case f.Kind == Dict && n.Kind == tree.Map:
		for _, entry := range n.Entries {
			r.key(entry, *f.Item, values(path, f))
		}
	case f.Kind == List && n.Kind == tree.List:
		for _, item := range n.Items {
			r.value(*f.Item, item, items(path))
		}
	case f.Kind == Either:
		if choice := choose(f.Choices, n); choice != nil {
			r.value(*choice, n, path)
		}
	}
}

// key refuses the key of entry when the target refuses its path, and walks its value, a value of f, so a refused key
// under it is named too.
func (r *refuser) key(entry tree.Entry, f Field, path string) {
	path = cmp.Or(f.Place, path)
	if r.target.Keys[path].Level == Refused {
		r.Add(entry.KeyPos, "%s", RefusedMessage(path, r.target))
	}
	r.value(f, entry.Value, path)
}
