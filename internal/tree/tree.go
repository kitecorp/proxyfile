// Package tree is the boundary between a file's syntax and everything that reads it. Each syntax front (YAML and
// HOCON) fills a Node, and the format check and the decoder read only Nodes, so nothing after this package knows
// which syntax a file used.
package tree

import "github.com/kitecorp/proxyfile/internal/model"

type Kind int

const (
	Scalar Kind = iota + 1
	List
	Map
)

// Node is a value of either syntax: a scalar is its text without the quotes (a YAML null is ""), a map keeps its
// entries in written order.
type Node struct {
	Kind Kind
	Text string
	// Quoted is true for a scalar a YAML reader takes as text whatever it holds: one written in quotes, or as a
	// literal or folded block. The format check refuses one where a number or a boolean is expected, because the JSON
	// Schema an editor checks the file with refuses text there. The HOCON front sets it for every HOCON string, quoted
	// or not, because HOCON types unquoted text as a string.
	Quoted bool
	// NumberOrBool is true for a plain scalar go-yaml takes as a number or a boolean, such as 404, 1.5 or true. The
	// format check refuses one where text is expected, because the JSON Schema refuses a number or a boolean there; the
	// origins and methods files follow the same rule, because one checker serves all three files. go-yaml also takes
	// some YAML 1.1 spellings as numbers (1_000, 0b1, 0X1F) that YAML 1.2's core schema reads as text, so an editor
	// reading YAML 1.2 accepts them as text where the check refuses them: there the check is the stricter side. The
	// HOCON front sets it for every HOCON number and boolean.
	NumberOrBool bool
	Items        []*Node
	Entries      []Entry
	Pos          model.Pos
}

// Entry carries the key's own position so that a problem with the key, such as an unknown key, points at the
// key, and a problem with the value points at the value.
type Entry struct {
	Key    string
	KeyPos model.Pos
	Value  *Node
}

// Get returns nil when n is nil, is not a Map or lacks the key, so a reader can follow a path such as
// root.Get("services").Get("shop") and test only the end.
func (n *Node) Get(key string) *Node {
	if entry := n.Entry(key); entry != nil {
		return entry.Value
	}
	return nil
}

// GetText is the text of the key's single value, or "" for a key that was not written, so a reader of an optional
// key needs no nil test. A Text method cannot exist beside the Text field, so the name follows Get.
func (n *Node) GetText(key string) string {
	if value := n.Get(key); value != nil {
		return value.Text
	}
	return ""
}

// Entry is Get for a reader that also needs the key's position, such as a problem that names the key.
func (n *Node) Entry(key string) *Entry {
	if n == nil || n.Kind != Map {
		return nil
	}
	for i := range n.Entries {
		if n.Entries[i].Key == key {
			return &n.Entries[i]
		}
	}
	return nil
}
