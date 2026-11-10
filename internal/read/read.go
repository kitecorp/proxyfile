// Package read turns a proxyfile into the model, or into the problems that stop the run.
package read

import (
	"path/filepath"

	"github.com/kitecorp/proxyfile/internal/format"
	"github.com/kitecorp/proxyfile/internal/input"
	"github.com/kitecorp/proxyfile/internal/model"
)

// Proxyfile reads a proxyfile and returns its model, or the problems that stop the run. It chooses the syntax by
// the file name; this version reads YAML only.
//
// The stages run in order and the first that finds a problem ends the run with all of its problems, sorted. Each
// stage may then rely on the one before: decoding reads only a tree the check accepted, and the meaning rules read
// only a complete model.
func Proxyfile(path string) (*model.Gateway, []model.Problem) {
	if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
		return nil, model.FileProblem(path, "proxyfile reads files ending in .yaml or .yml")
	}
	root, problems := input.Checked(path, format.Proxyfile)
	if problems != nil {
		return nil, problems
	}
	g, problems := decode(root)
	if problems != nil {
		return nil, problems
	}
	if problems := meaning(g); problems != nil {
		return nil, problems
	}
	return g, nil
}
