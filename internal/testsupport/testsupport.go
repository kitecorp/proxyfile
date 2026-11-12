// Package testsupport holds the helpers the tests of several packages share. Only _test.go files import it.
package testsupport

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/model"
)

// ProblemStrings returns each problem as the text a user reads, so a test compares problems with literal lines.
func ProblemStrings(problems []model.Problem) []string {
	texts := make([]string, len(problems))
	for i, problem := range problems {
		texts[i] = problem.String()
	}
	return texts
}

// ProblemStringsIn is ProblemStrings with dir taken off the front of each line, so a row names a file written to a
// temporary directory by its base name alone. A line naming a file elsewhere is kept whole.
func ProblemStringsIn(dir string, problems []model.Problem) []string {
	texts := ProblemStrings(problems)
	for i := range texts {
		texts[i] = strings.TrimPrefix(texts[i], dir+string(filepath.Separator))
	}
	return texts
}

// ReadFile returns the bytes of the file at path, ending the test when it cannot.
func ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return data
}

// WriteFile writes text to the file at path, ending the test when it cannot.
func WriteFile(t testing.TB, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// ExampleForm is one folder under examples/ that holds a proxyfile. Each example writes one gateway in more than one
// form, each form in a folder of its own, because render and check read a default file only when the folder holds one.
// The folder also holds the envoy.yaml the proxyfile renders, and rpc-table.json where the example has a table.
type ExampleForm struct {
	Dir  string // under examples/, as <example>/<form>
	File string // the proxyfile in Dir, under a name render and check read when given no file
	// Methods is the --rpc-methods file the form is rendered with, relative to Dir, or "" when the proxyfile writes
	// its methods or has no rpc entry.
	Methods string
	Table   bool // Dir holds rpc-table.json, the table the form's RPC methods give
}

// ExampleForms is every folder under examples/ that holds a proxyfile.
var ExampleForms = []ExampleForm{
	{Dir: "minimal/yaml", File: "proxyfile.yaml"},
	{Dir: "minimal/hocon", File: "Proxyfile"},
	{Dir: "partial/yaml", File: "proxyfile.yaml", Methods: "../methods.json", Table: true},
	{Dir: "partial/hocon", File: "Proxyfile", Table: true},
	{Dir: "partial/proto-methods", File: "Proxyfile", Table: true},
	{Dir: "full/yaml", File: "proxyfile.yaml", Table: true},
	{Dir: "full/hocon", File: "Proxyfile", Table: true},
	{Dir: "tracing/yaml", File: "proxyfile.yaml"},
	{Dir: "tracing/hocon", File: "Proxyfile"},
	{Dir: "tracing/envoy-tracing", File: "proxyfile.yaml"},
	{Dir: "otel/yaml", File: "proxyfile.yaml"},
	{Dir: "otel/hocon", File: "Proxyfile"},
	{Dir: "jwt/yaml", File: "proxyfile.yaml"},
	{Dir: "jwt/hocon", File: "Proxyfile"},
	{Dir: "grpc/yaml", File: "proxyfile.yaml"},
	{Dir: "grpc/hocon", File: "Proxyfile"},
	{Dir: "balanced/yaml", File: "proxyfile.yaml"},
	{Dir: "balanced/hocon", File: "Proxyfile"},
	{Dir: "dns/yaml", File: "proxyfile.yaml"},
	{Dir: "dns/hocon", File: "Proxyfile"},
	{Dir: "tls/yaml", File: "proxyfile.yaml"},
	{Dir: "tls/hocon", File: "Proxyfile"},
	{Dir: "limited/yaml", File: "proxyfile.yaml"},
	{Dir: "limited/hocon", File: "Proxyfile"},
}

// PartialMethods is the rpc.methods block that, appended to examples/partial/yaml/proxyfile.yaml, writes in the
// proxyfile what examples/partial/methods.json lists, so a render that reads it must give that example's committed
// files.
const PartialMethods = "rpc:\n  methods:\n" +
	"    - {service: example.backend.v1.ReportService, method: ListReports, action: read, reports: [export, share]}\n"

// MinimalInlineOrigins is the origins block of examples/minimal/yaml/proxyfile.yaml. The tests that need the file
// form, which no example writes, replace it with "origins: <file>".
const MinimalInlineOrigins = "origins:\n  all: {hosts: [localhost], origins: [\"http://localhost:3000\"]}\n"

// Document is a valid file kept one line per item, so that each test row changes it by line number and the row's
// expected positions can be counted from the change alone.
type Document []string

func (d Document) String() string {
	return strings.Join(d, "\n") + "\n"
}

// Replaced returns the document with its lines first to last, counted from 1, replaced by lines.
func (d Document) Replaced(first, last int, lines ...string) string {
	return slices.Concat(d[:first-1], Document(lines), d[last:]).String()
}

// Inserted returns the document with line put in as line number at.
func (d Document) Inserted(at int, line string) string {
	return slices.Insert(slices.Clone(d), at-1, line).String()
}
