package tree

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	hocon "github.com/o3co/go.hocon"

	"github.com/kitecorp/proxyfile/internal/model"
)

// FromHOCON reads the HOCON file at path into the tree the YAML front builds for the same gateway, so every check after
// the tree reads one form. It returns either the root or the problems, never both, as FromYAML does. It takes the path,
// not the bytes, because the library reads an include relative to the including file only when it opens that file.
func FromHOCON(path string) (*Node, []model.Problem) {
	// The library's one-step parse resolves substitutions with the environment switched on (o3co hocon.go:91), and
	// resolving falls back to os.LookupEnv for a substitution the files do not satisfy (o3co resolver.go:1135-1141).
	// The same files must give the same bytes on every machine, so the parse leaves the substitutions in place and a
	// second step resolves them without the environment.
	config, err := hocon.ParseFileWithOptions(path, hocon.DefaultParseOptions().WithResolveSubstitutions(false))
	if err == nil {
		config, err = config.Resolve(hocon.DefaultResolveOptions().WithUseSystemEnvironment(false))
	}
	if err != nil {
		return nil, hoconProblems(path, err)
	}
	return walker{file: path}.object(config, ""), nil
}

// The library stops at its first error, so the error is the stage's only problem.
func hoconProblems(file string, err error) []model.Problem {
	var parseErr *hocon.ParseError
	var resolveErr *hocon.ResolveError
	switch {
	case errors.As(err, &parseErr):
		// A file the library cannot read comes back as a ParseError with no line, holding only the text of Go's error,
		// "<op> <path>: <reason>" (o3co hocon.go:44-49), not the error itself. So the reason is cut out of the text and
		// handed to model.CannotRead, which every reader uses, so an unreadable file reads the same in both forms and
		// its path is not written twice.
		_, reason, found := strings.Cut(parseErr.Message, " "+file+": ")
		if found && parseErr.Line == 0 {
			return []model.Problem{model.CannotRead(file, errors.New(reason))}
		}
		// Any other ParseError is a syntax error in the file given. The library sets FilePath to that file on every
		// ParseError it returns (o3co hocon.go:44-49 and 70), and a syntax error in an included file is not a
		// ParseError (the default branch below).
		pos := model.Pos{File: parseErr.FilePath, Line: parseErr.Line, Column: parseErr.Col}
		return []model.Problem{{Pos: pos, Message: parseErr.Message}}
	case errors.As(err, &resolveErr) && resolveErr.FilePath != "":
		return model.FileProblem(resolveErr.FilePath, resolveErr.Message)
	default:
		// No line, even where the text holds one: a syntax error inside an included file comes back as the library's
		// internal parser error, whose line is the included file's but whose text does not name that file (o3co
		// resolver.go:1814-1831), so a line given here would point at the wrong file.
		return model.FileProblem(file, err.Error())
	}
}

// walker builds the tree from the resolved Config. The library keeps no position for a value, only for a syntax error:
// its object, array and scalar values hold no line (o3co internal/resolver/resolver.go:48-52, 307, 324-335), while its
// ParseError does (errors.go:17-23). So every node is placed by its key path instead: the problems found after the
// tree name the file and that path.
type walker struct {
	file string
}

func (w walker) at(path string) model.Pos {
	return model.Pos{File: w.file, Path: path}
}

// Keys gives an object's keys in written order (o3co resolver.go:72-76), which the entries keep.
func (w walker) object(config *hocon.Config, path string) *Node {
	object := &Node{Kind: Map, Pos: w.at(path)}
	for _, key := range config.Keys() {
		value := w.value(config, segment(key), keyPath(path, key))
		object.Entries = append(object.Entries, Entry{Key: key, KeyPos: value.Pos, Value: value})
	}
	return object
}

func (w walker) value(config *hocon.Config, segment, path string) *Node {
	var decoded any
	unexpected(config.UnmarshalPath(segment, &decoded))
	switch decoded := decoded.(type) {
	case map[string]any:
		return w.object(config.GetConfig(segment), path)
	case []any:
		return w.list(config, segment, decoded, path)
	case nil:
		return w.scalar(nil, "", path)
	default:
		// The text as written: UnmarshalPath turns a number into an int64 or a float64 (o3co unmarshal.go:372-381),
		// where 0100 would become 100, while GetStringE gives the scalar's raw text (o3co config_e.go:96-102).
		text, err := config.GetStringE(segment)
		unexpected(err)
		return w.scalar(decoded, text, path)
	}
}

func (w walker) list(config *hocon.Config, segment string, decoded []any, path string) *Node {
	list := &Node{Kind: List, Pos: w.at(path)}
	if objects, err := config.GetConfigSliceE(segment); err == nil {
		for i, object := range objects {
			list.Items = append(list.Items, w.object(object, itemPath(path, i)))
		}
		return list
	}
	if texts, err := config.GetStringSliceE(segment); err == nil {
		for i, text := range texts {
			list.Items = append(list.Items, w.scalar(decoded[i], text, itemPath(path, i)))
		}
		return list
	}
	// An array mixing objects, scalars or arrays, which no list of the format accepts. The library gives such an
	// array's items only as Go values (o3co config_e.go:219-233 and 293-307 refuse it), so they are built from those,
	// and only a problem message can show one.
	for i, item := range decoded {
		list.Items = append(list.Items, w.fromGo(item, itemPath(path, i)))
	}
	return list
}

// scalar takes the kind from the Go value UnmarshalPath gives: a string for every HOCON string, quoted or not, and an
// int64, a float64 or a bool for a number or a boolean (o3co unmarshal.go:364-385).
func (w walker) scalar(decoded any, text, path string) *Node {
	node := &Node{Kind: Scalar, Pos: w.at(path)}
	switch decoded.(type) {
	case nil:
	case string:
		node.Text, node.Quoted = text, true
	default:
		node.Text, node.NumberOrBool = text, true
	}
	return node
}

// fromGo builds a node from a Go value alone, which loses the written order and text: a map's keys come in byte order,
// which is the same on every run, and a number as Go writes it.
func (w walker) fromGo(value any, path string) *Node {
	switch value := value.(type) {
	case map[string]any:
		object := &Node{Kind: Map, Pos: w.at(path)}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			item := w.fromGo(value[key], keyPath(path, key))
			object.Entries = append(object.Entries, Entry{Key: key, KeyPos: item.Pos, Value: item})
		}
		return object
	case []any:
		list := &Node{Kind: List, Pos: w.at(path)}
		for i, item := range value {
			list.Items = append(list.Items, w.fromGo(item, itemPath(path, i)))
		}
		return list
	case string:
		return w.scalar(value, value, path)
	case int64:
		return w.scalar(value, strconv.FormatInt(value, 10), path)
	case float64:
		return w.scalar(value, strconv.FormatFloat(value, 'g', -1, 64), path)
	case bool:
		return w.scalar(value, strconv.FormatBool(value), path)
	default:
		return w.scalar(nil, "", path)
	}
}

// unexpected stops the run on an error the walk cannot meet. It asks only for keys Keys listed, in a Config that
// Resolve returned with every substitution resolved, and for a scalar's text only after UnmarshalPath decoded a
// scalar that is not null, so an error would be a fault in the library or in this walk, never in the file.
func unexpected(err error) {
	if err != nil {
		panic(fmt.Sprintf("tree: reading a resolved HOCON value: %v", err))
	}
}

// segment writes one key as a lookup path of one segment. The library splits a lookup path at every dot outside
// quotes and takes the character after a backslash as itself inside them (o3co config.go:145-185), so a key holding a
// dot is looked up in quotes, with its backslashes and quotes escaped.
func segment(key string) string {
	return `"` + segmentEscaper.Replace(key) + `"`
}

var segmentEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// plainKey is a key a key path writes as it is. Any other key is written quoted, so a key holding a dot reads as one
// key: routes[1].rpc.to."shop.v1.*".
var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func keyPath(parent, key string) string {
	if !plainKey.MatchString(key) {
		key = strconv.Quote(key)
	}
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func itemPath(array string, index int) string {
	return array + "[" + strconv.Itoa(index) + "]"
}
