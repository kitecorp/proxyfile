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

// PartialMethods is the rpc.methods block that, appended to examples/partial/gateway.yaml, writes in the proxyfile
// what examples/partial/methods.json lists, so a render that reads it must give that example's committed files.
const PartialMethods = "rpc:\n  methods:\n" +
	"    - {service: example.backend.v1.ReportService, method: ListReports, action: read, reports: [export, share]}\n"

// MinimalInlineOrigins is the origins block of examples/minimal/gateway.yaml. The tests that need the file form, which
// no example writes, replace it with "origins: <file>".
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
