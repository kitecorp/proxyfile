package read

import (
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
// list of texts under roots and an option's full name. This adds the two rules the description cannot hold, because an
// empty list gives no method, and a root written twice would read the same files twice. A root written twice is named
// at the later place, which Roots leaves out.
func readProto(root *tree.Node) (*Proto, []model.Problem) {
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
		proto.Roots = append(proto.Roots, model.Text{Value: item.Text, Pos: item.Pos})
	}
	return proto, problems.Sorted()
}
