// Package input holds the reading step the readers of the input files share: reading a file as far as a tree its
// description accepts. Each reader uses the same step, so a missing file or a key out of place reads the same
// whichever file it is in.
package input

import (
	"os"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// Checked reads the JSON file at path, an origins or a methods file, with the JSON front.
func Checked(path string, spec format.Field) (*tree.Node, []model.Problem) {
	return CheckedWith(path, spec, JSONFile)
}

// CheckedWith reads the file at path with front and returns its tree when spec accepts it. Otherwise it returns the
// problems of the first step that finds any, sorted: reading the file, its syntax, then the check against spec.
//
// Each front reads its own file, rather than taking bytes read here, because the HOCON library resolves an include
// relative to the including file only when it opens that file itself.
func CheckedWith(path string, spec format.Field,
	front func(path string) (*tree.Node, []model.Problem)) (*tree.Node, []model.Problem) {
	root, problems := front(path)
	if problems != nil {
		return nil, problems
	}
	if problems := format.Check(spec, root); problems != nil {
		return nil, problems
	}
	return root, nil
}

// YAMLFile is the YAML front: it reads the file at path and builds its tree.
func YAMLFile(path string) (*tree.Node, []model.Problem) {
	return readWith(path, tree.FromYAML)
}

// JSONFile is the JSON front: it reads the file at path and builds its tree.
func JSONFile(path string) (*tree.Node, []model.Problem) {
	return readWith(path, tree.FromJSON)
}

// readWith reads the file at path and hands its bytes to front, tree.FromYAML or tree.FromJSON.
func readWith(path string,
	front func(file string, data []byte) (*tree.Node, []model.Problem)) (*tree.Node, []model.Problem) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []model.Problem{model.CannotRead(path, err)}
	}
	return front(path, data)
}
