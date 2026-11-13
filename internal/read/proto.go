package read

import (
	"path/filepath"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// Proto is rpc.proto as written. render.Read resolves the roots against the proxyfile's directory and compiles the
// .proto files under them, so this holds only the text and the places its problems name.
type Proto struct {
	// Key is the proto key, which the problem of --rpc-methods given as well names. OptionKey is the option key,
	// which every problem of the option names.
	Key, OptionKey model.Pos
	Roots          []model.Text // in written order, each at the place its problems name
	Option         string       // the option's full name
}

// readProto reads rpc.proto, or returns nil when it is not written. The check has already made it a mapping with a
// list of texts under roots and an option's full name. This adds the three rules the description cannot hold: an empty
// list gives no method, a root written twice would read the same files twice, and a root inside another would compile
// the files under it twice, once under each root's import path, where the compiler names every symbol in them as
// defined twice. A root written twice is named at the later place, which Roots leaves out; a root inside another is
// named at its own place, with the root it is inside, each as written and quoted. path is the proxyfile's path, which a
// relative root is read beside.
func readProto(root *tree.Node, path string) (*Proto, []model.Problem) {
	entry := root.Get("rpc").Entry("proto")
	if entry == nil {
		return nil, nil
	}
	roots, option := entry.Value.Entry("roots"), entry.Value.Entry("option")
	proto := &Proto{Key: entry.KeyPos, OptionKey: option.KeyPos, Option: option.Value.Text}
	var problems model.Problems
	if len(roots.Value.Items) == 0 {
		problems.Add(roots.KeyPos, "the roots list is empty")
	}
	first := map[string]model.Pos{}
	for _, item := range roots.Value.Items {
		if earlier, written := first[item.Text]; written {
			problems.Add(item.Pos, "the root %q is written twice (the first %s)", item.Text, earlier.Where())
			continue
		}
		first[item.Text] = item.Pos
		for _, other := range proto.Roots {
			switch {
			case inside(path, item.Text, other.Value):
				problems.Add(item.Pos, insideMessage, item.Text, other.Value)
			case inside(path, other.Value, item.Text):
				problems.Add(other.Pos, insideMessage, other.Value, item.Text)
			}
		}
		proto.Roots = append(proto.Roots, model.Text{Value: item.Text, Pos: item.Pos})
	}
	return proto, problems.Sorted()
}

const insideMessage = "rpc.proto.roots: %q is inside %q"

// inside reports whether the root dir is the root parent or under it, both read beside the proxyfile at path as
// render.Read reads them (tree.Beside), so a relative root and an absolute one compare as the directories they name,
// and with every symbolic link followed, because the compiler reads a root that is a link into another root as the
// directory the link names. filepath.Rel cleans both and compares them by whole segments, so proto/ and ./proto are
// proto, and proto2 is beside proto, not inside it.
func inside(path, dir, parent string) bool {
	rel, err := filepath.Rel(followed(tree.Absolute(tree.Beside(path, parent))),
		followed(tree.Absolute(tree.Beside(path, dir))))
	return err == nil && filepath.IsLocal(rel)
}

// followed returns the absolute path dir with every symbolic link in it followed. The part of dir that does not exist,
// from a missing directory or a link whose target is missing, is kept as written after the part that does, so such a
// root is compared by its text, in the same terms as the others, and is left for the compiler to name as a root that
// cannot be read.
func followed(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return dir
	}
	return filepath.Join(followed(parent), filepath.Base(dir))
}
