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

// Support is a target's declaration for one row of the support table. Note is "" for Supported; for Partial it says
// the difference in one sentence, and for Refused what the proxy offers instead. The support table and the refusal
// both quote it. Refuses lists, on a Partial key only, the forms of it the target refuses while it carries the others,
// each with its Applies.
type Support struct {
	Level   Level
	Note    string
	Refuses []Refusal
}

// Refusal is one form of a key that a target refuses, by the key's value or for want of another key.
type Refusal struct {
	Form    string // the refused form, in words that follow the key: "of 1 or more"
	Instead string // what to write instead
	// Applies says whether the key's value is of the refused form. It reads the other keys the form depends on in
	// proxyfile, the proxyfile's tree with its includes merged, also when the key is written in an origins file, a
	// methods file or .proto options, because those keys are the proxyfile's.
	Applies func(value, proxyfile *tree.Node) bool
}

// Target is a proxy with a generator: Name is the --target value, Title the support table's column heading, and Keys
// its declaration for each row of MatrixRows: a core key by its path as CoreKeys gives it, a behaviour written with
// no key by its words.
type Target struct {
	Name, Title string
	Keys        map[string]Support
}

// Targets are the proxies with a generator, in the order of the support table's columns, each declared in a file of
// its own. TestTargetDeclarations keeps each target's declarations complete, so a key added to the format cannot go
// undeclared, and keeps each refused form on a partial key with a test of the value.
var Targets = []Target{envoyTarget}

// WrittenWithNoKey are the behaviours every configuration holds with no key for them, named in words. The support
// table lists them after the core keys, so each target declares how it writes them as it declares a key.
var WrittenWithNoKey = []string{"the request's path normalised before a route is picked", "a dot segment with a ; parameter answered 400"}

// MatrixRows are the support table's rows: every core key of the proxyfile, in CoreKeys' order, then WrittenWithNoKey.
func MatrixRows() []string {
	return slices.Concat(CoreKeys(Proxyfile), WrittenWithNoKey)
}

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

// SupportMatrix writes, as a Markdown table, each target's declaration for every row: a column for each target, in
// the order given, and a line for each row, in the order given. A row in WrittenWithNoKey is written as its words, any
// other as a key. The rows are told apart by that list, never by a space in the name, because a core key such as
// services.<service name> holds one.
func SupportMatrix(rows []string, targets []Target) []byte {
	var b strings.Builder
	b.WriteString("| Key |")
	for _, t := range targets {
		b.WriteString(" " + t.Title + " |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(targets)) + "\n")
	for _, key := range rows {
		if slices.Contains(WrittenWithNoKey, key) {
			b.WriteString("| " + escape(key) + " |")
		} else {
			b.WriteString("| `" + key + "` |")
		}
		for _, t := range targets {
			b.WriteString(" " + cell(t.Keys[key]) + " |")
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// cell words one declaration in the support table. A key a target does not declare is written as such, though
// TestTargetDeclarations keeps every shipped target from leaving one out.
func cell(s Support) string {
	switch s.Level {
	case Supported:
		return "supported"
	case Partial:
		text := "partial: " + escape(s.Note)
		for _, refusal := range s.Refuses {
			text += "; refuses " + escape(refusal.Form) + ": " + escape(refusal.Instead)
		}
		return text
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

// Refusals returns one problem for each key written in file whose path t declares Refused, at the key, sorted, so a
// render for t names every key it cannot carry instead of writing a weaker configuration. A Refuses entry of a key's
// declaration is checked at each key of that path, given the key's value and proxyfile, and names the form it refuses.
// It follows the description f as Check does, and reads a file Check accepted. f is the proxyfile's description, with
// file the proxyfile itself, or the origins file's or the methods file's, whose keys are named by the path the
// proxyfile writes them at (Field.Place); proxyfile is the proxyfile's tree in every case.
func Refusals(f Field, file, proxyfile *tree.Node, t Target) []model.Problem {
	var problems model.Problems
	walkKeys(f, file, func(entry tree.Entry, path string) {
		support := t.Keys[path]
		if support.Level == Refused {
			problems.Add(entry.KeyPos, "%s", RefusedMessage(path, t))
		}
		for _, refusal := range support.Refuses {
			if refusal.Applies(entry.Value, proxyfile) {
				problems.Add(entry.KeyPos, "%q %s is refused by --target %s: %s", path, refusal.Form, t.Name, refusal.Instead)
			}
		}
	})
	return problems.Sorted()
}

// WrittenKeys returns the path of every key written in root, a tree Check accepted against f, in CoreKeys' spelling,
// sorted, each once. The conformance battery runs a row on a file only when the file writes the row's keys.
func WrittenKeys(f Field, root *tree.Node) []string {
	var keys []string
	walkKeys(f, root, func(_ tree.Entry, path string) {
		if !slices.Contains(keys, path) {
			keys = append(keys, path)
		}
	})
	slices.Sort(keys)
	return keys
}

// walkKeys calls visit for each key written in n, a value of f, with the key's path, walking as Check walks: a Dict's
// keys by the name of its key, a list's items by [], an Either by the choice that fits n, and each key's value after
// the key, so a key under another is visited too.
func walkKeys(f Field, n *tree.Node, visit func(entry tree.Entry, path string)) {
	var value func(f Field, n *tree.Node, path string)
	key := func(entry tree.Entry, f Field, path string) {
		path = cmp.Or(f.Place, path)
		visit(entry, path)
		value(f, entry.Value, path)
	}
	value = func(f Field, n *tree.Node, path string) {
		path = cmp.Or(f.Place, path)
		switch {
		case f.Kind == Map && n.Kind == tree.Map:
			for _, entry := range n.Entries {
				// A key the description does not hold here, in a reserved block or at its 1.x place, is the check's
				// to refuse, and no target declares it.
				if field := f.Child(entry.Key); field != nil {
					key(entry, *field, join(path, entry.Key))
				}
			}
		case f.Kind == Dict && n.Kind == tree.Map:
			for _, entry := range n.Entries {
				key(entry, *f.Item, values(path, f))
			}
		case f.Kind == List && n.Kind == tree.List:
			for _, item := range n.Items {
				value(*f.Item, item, items(path))
			}
		case f.Kind == Either:
			if choice := choose(f.Choices, n); choice != nil {
				value(*choice, n, path)
			}
		}
	}
	value(f, n, "")
}
