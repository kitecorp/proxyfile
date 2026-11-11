package format

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

const readmeFile = "../../README.md"

// The key tables of the README and the pages under docs/ are written from the description, so a key added, renamed
// or given another rule shows in them, and a table edited by hand fails here. -update rewrites every table.
func TestDocsAreCurrent(t *testing.T) {
	for _, file := range documentation(t) {
		text := string(testsupport.ReadFile(t, file))
		tables, err := keyTables(text)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		var current strings.Builder
		written := 0
		for _, table := range tables {
			current.WriteString(text[written:table.start])
			written = table.start
			want, err := tableNamed(table.name)
			if err != nil {
				t.Errorf("%s: %v", file, err)
				continue
			}
			if text[table.start:table.end] != want && !*update {
				t.Errorf("%s: the table of %q differs from what Reference writes; rewrite it with "+
					"go test ./internal/format -run TestDocsAreCurrent -update", file, table.name)
			}
			current.WriteString(want)
			written = table.end
		}
		current.WriteString(text[written:])
		if *update && current.String() != text {
			testsupport.WriteFile(t, file, current.String())
		}
	}
}

// The README lists every key at the top of a proxyfile, in the description's order and each with its class and its
// Doc, as a link to the page under docs/ whose key tables hold its row. A key added to the description fails here until
// it has its line and its page.
func TestReadmeLinksEveryKey(t *testing.T) {
	readme := string(testsupport.ReadFile(t, readmeFile))
	_, section, found := strings.Cut(readme, "\n## Keys\n")
	if !found {
		t.Fatalf("%s has no ## Keys section", readmeFile)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	line := regexp.MustCompile("^- \\[`([^`]+)`\\]\\((docs/[^)#]+\\.md)\\) \\(([a-z]+)\\): (.+)$")
	var keys []string
	for _, text := range strings.Split(section, "\n") {
		if !strings.HasPrefix(text, "- ") {
			continue
		}
		m := line.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("README.md, Keys: %q is not of the form - [`<key>`](docs/<page>.md) (<its class>): <its Doc>", text)
			continue
		}
		key, page, class, doc := m[1], m[2], m[3], m[4]
		keys = append(keys, key)
		field := Proxyfile.Child(key)
		if field == nil {
			continue // the comparison of the whole list below names it
		}
		if want := cmp.Or(field.Class, core); class != want {
			t.Errorf("README.md, Keys: the line of %s gives the class %q, want %q", key, class, want)
		}
		if want := escape(field.Doc); doc != want {
			t.Errorf("README.md, Keys: the line of %s says %q, want its Doc, %q", key, doc, want)
		}
		pageText, err := os.ReadFile(filepath.Join("..", "..", page))
		if err != nil {
			t.Errorf("README.md, Keys: the line of %s links %s: %v", key, page, err)
			continue
		}
		if !holdsRow(string(pageText), key) {
			t.Errorf("README.md, Keys: the line of %s links %s, which holds no key table with a row for it", key, page)
		}
	}
	if want := names(Proxyfile.Fields); !slices.Equal(keys, want) {
		t.Errorf("README.md, Keys: the keys listed = %q, want %q", keys, want)
	}
}

// A relative link in the README or a page under docs/ names a file or a directory of the repository, and its
// fragment, when it has one, names a heading of the Markdown file it leads to, so moving a page, a heading or an
// example cannot leave a link that leads nowhere.
func TestDocLinksResolve(t *testing.T) {
	var broken []string
	for _, file := range documentation(t) {
		for _, target := range relativeLinks(string(testsupport.ReadFile(t, file))) {
			if problem := resolve(file, target); problem != "" {
				broken = append(broken, fmt.Sprintf("%s: %s %s", file, target, problem))
			}
		}
	}
	if !slices.Equal(broken, nil) {
		t.Errorf("links that lead nowhere = %q, want none", broken)
	}
}

// documentation returns the README and the pages under docs/, the files whose key tables and links are checked.
func documentation(t *testing.T) []string {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join("..", "..", "docs", "*.md"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return append([]string{readmeFile}, pages...)
}

// keyTable is a table of a documentation file between a <!-- keys: <name> --> line and a <!-- /keys --> line: the
// name the marker gives and where the table starts and ends in the file's text.
type keyTable struct {
	name       string
	start, end int
}

var (
	keysOpening = regexp.MustCompile(`(?m)^<!-- keys: (.+) -->\n`)
	keysClosing = regexp.MustCompile(`(?m)^<!-- /keys -->$`)
)

func keyTables(text string) ([]keyTable, error) {
	var tables []keyTable
	openings := keysOpening.FindAllStringSubmatchIndex(text, -1)
	for i, opening := range openings {
		start, next := opening[1], len(text)
		if i+1 < len(openings) {
			next = openings[i+1][0]
		}
		closing := keysClosing.FindStringIndex(text[start:next])
		if closing == nil {
			return nil, fmt.Errorf("the table of %q has no <!-- /keys --> line before the next table or the end", text[opening[2]:opening[3]])
		}
		tables = append(tables, keyTable{name: text[opening[2]:opening[3]], start: start, end: start + closing[0]})
	}
	return tables, nil
}

// tableNamed returns the table a marker names: "." holds the keys at the top of a proxyfile that hold no keys of
// their own, "origins file" and "methods file" the keys of those files, and any other name is the path of a key of
// the proxyfile, whose table holds that key and every key under it, in the class of the key that holds it.
func tableNamed(name string) (string, error) {
	switch name {
	case ".":
		leaves := slices.DeleteFunc(slices.Clone(Proxyfile.Fields), holdsKeys)
		return string(Reference(Field{Kind: Map, Fields: leaves}, "", core)), nil
	case "origins file":
		return string(Reference(Origins, "", core)), nil
	case "methods file":
		return string(Reference(Methods, "", core)), nil
	}
	spec, class := &Proxyfile, core
	for _, key := range strings.Split(name, ".") {
		class = cmp.Or(spec.Class, class)
		if spec = spec.Child(key); spec == nil {
			return "", fmt.Errorf("a table names %q, which is no key of the proxyfile, nor ., origins file or methods file", name)
		}
	}
	return string(Reference(*spec, name, class)), nil
}

// holdsKeys says whether the value of f has keys under it, which the table of f lists.
func holdsKeys(f Field) bool {
	switch f.Kind {
	case Map, Dict:
		return true
	case List:
		return holdsKeys(*f.Item)
	case Either:
		return slices.ContainsFunc(f.Choices, holdsKeys)
	}
	return false
}

// holdsRow says whether text holds the table of key, or the table of the keys at the top that hold none, with a row
// for key. A row of the same name in another table, such as the cors row of the origins file's, does not count.
func holdsRow(text, key string) bool {
	tables, err := keyTables(text)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(tables, func(table keyTable) bool {
		return (table.name == key || table.name == ".") && strings.Contains(text[table.start:table.end], "\n| `"+key+"` |")
	})
}

func names(fields []Field) []string {
	list := make([]string, len(fields))
	for i, field := range fields {
		list[i] = field.Name
	}
	return list
}

var inlineLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// relativeLinks returns the target of every inline link outside a fenced code block that is not a URL with a scheme;
// a link to a heading of the same file starts with #.
func relativeLinks(text string) []string {
	var links []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			if !strings.Contains(m[1], "://") {
				links = append(links, m[1])
			}
		}
	}
	return links
}

// resolve returns "" when target, a link in file, leads to a file or a directory and to a heading the fragment names,
// and otherwise what is missing.
func resolve(file, target string) string {
	name, fragment, _ := strings.Cut(target, "#")
	path := file
	if name != "" {
		path = filepath.Join(filepath.Dir(file), name)
		if _, err := os.Stat(path); err != nil {
			return "leads to no file"
		}
	}
	if fragment == "" {
		return ""
	}
	text, err := os.ReadFile(path)
	if err != nil || !slices.Contains(anchors(string(text)), fragment) {
		return "names no heading of " + filepath.Base(path)
	}
	return ""
}

// anchors returns the fragment GitHub gives each heading of a Markdown file outside its code blocks: the heading in
// lower case, with its spaces as - and every other character that is not a letter, a digit, - or _ removed; a
// heading written twice gets -1 on its second fragment.
func anchors(text string) []string {
	var found []string
	seen := map[string]int{}
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		heading := strings.TrimLeft(line, "#")
		if fenced || heading == line || !strings.HasPrefix(heading, " ") {
			continue
		}
		var slug strings.Builder
		for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
			switch {
			case r == ' ':
				slug.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				slug.WriteRune(r)
			}
		}
		anchor := slug.String()
		if n := seen[anchor]; n > 0 {
			seen[anchor]++
			anchor = fmt.Sprintf("%s-%d", anchor, n)
		} else {
			seen[anchor] = 1
		}
		found = append(found, anchor)
	}
	return found
}
