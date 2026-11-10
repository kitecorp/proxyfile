// Package input holds the reading step the readers of the input files share: reading a file as far as a tree its
// description accepts. Each reader uses the same step, so a missing file or a key out of place reads the same
// whichever file it is in.
package input

import (
	"errors"
	"io/fs"
	"os"

	"github.com/kitecorp/proxyfile/internal/format"
	"github.com/kitecorp/proxyfile/internal/model"
	"github.com/kitecorp/proxyfile/internal/tree"
)

// Checked reads the file at path as YAML and returns its tree when spec accepts it. Otherwise it returns the
// problems of the first step that finds any, sorted: reading the file, its syntax, then the check against spec.
func Checked(path string, spec format.Field) (*tree.Node, []model.Problem) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.FileProblem(path, "the file cannot be read: "+withoutPath(err))
	}
	root, problems := tree.FromYAML(path, data)
	if problems != nil {
		return nil, problems
	}
	if problems := format.Check(spec, root); problems != nil {
		return nil, problems
	}
	return root, nil
}

// The problem already starts with the path, so the path inside an os error would be written twice.
func withoutPath(err error) string {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return pathErr.Err.Error()
	}
	return err.Error()
}
