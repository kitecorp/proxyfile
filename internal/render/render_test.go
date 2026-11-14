package render

import (
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

var update = flag.Bool("update", false, "rewrite the generated files of examples/")

// TestExampleTrees is the generator's proof: the tree it writes for each of the three 1.1 examples equals a tree
// written apart from it, list order included, and a second render gives the same bytes. The minimal and partial trees
// are written by hand. The full tree was built from the 2.0 generator's render by exact replacement and loaded by
// Envoy 1.40.2. The rest of the tracing example's tree is the 1.1 generator's, which these three pin;
// TestTracingExample is the proof of its tracing parts.
func TestExampleTrees(t *testing.T) {
	for _, name := range []string{"minimal", "partial", "full"} {
		t.Run(name, func(t *testing.T) {
			req := requestFor(t, name+"/yaml", "envoy.yaml")
			files, problems := render(req)
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			if got, want := paths(files), outputs(req); !slices.Equal(got, want) {
				t.Fatalf("render paths = %q, want %q", got, want)
			}
			want := decode(t, testsupport.ReadFile(t, filepath.Join("testdata", name+".expected.yaml")))
			if d := firstDifference(want, decode(t, files[0].Bytes), ""); d != "" {
				t.Error(d)
			}
			again, _ := render(req)
			if len(again) == 0 || !bytes.Equal(again[0].Bytes, files[0].Bytes) {
				t.Error("a second render of the same input gave other bytes")
			}
		})
	}
}

// TestExamplesAreCurrent keeps each example folder's committed files equal to what render writes for each target: the
// outputs, or <target>.refused, the lines render prints from the repository root when the target refuses the form. A
// folder never holds both kinds for one target, so neither can stay behind stale; -update writes both kinds.
//
// A form's rpc-table.json is compared only by a target that renders the form, so a form whose table every target
// refuses fails here, by name: nothing else would compare that table.
func TestExamplesAreCurrent(t *testing.T) {
	// ran counts the targets whose subtest ran for each form, and rendered says whether one rendered it. A form is
	// judged only when every target ran, so a run narrowed by -run to one target does not fail the others' tables.
	ran, rendered := map[string]int{}, map[string]bool{}
	for _, target := range Targets {
		for _, form := range testsupport.ExampleForms {
			t.Run(target.Name+"/"+form.Dir, func(t *testing.T) {
				ran[form.Dir]++
				req := requestFor(t, form.Dir, filepath.Join(exampleDir(form.Dir), target.Config))
				req.Target = target.Name
				refused := filepath.Join(exampleDir(form.Dir), target.Name+".refused")
				run, problems := Read(req)
				rendered[form.Dir] = rendered[form.Dir] || problems == nil
				var files []File
				if problems == nil {
					if files, problems = run.Files(); problems != nil {
						t.Fatalf("Files problems = %q", testsupport.ProblemStrings(problems))
					}
				} else {
					var text strings.Builder
					for _, problem := range problems {
						text.WriteString(strings.TrimPrefix(problem.String(), "../../") + "\n")
					}
					files = []File{{Path: refused, Bytes: []byte(text.String())}}
				}
				// The kind render does not write for this target is the one that must not be committed.
				leftover := refused
				if problems != nil {
					leftover = req.Out
				}
				for _, file := range files {
					if *update {
						if err := os.WriteFile(file.Path, file.Bytes, 0o644); err != nil {
							t.Fatalf("write %s: %v", file.Path, err)
						}
						continue
					}
					if d := committedDifference(t, form.Dir, file); d != "" {
						t.Errorf("%s\n  run the test with -update to write what render writes", d)
					}
				}
				if *update {
					// Usually absent, so its error says nothing; a leftover that stays fails the next run below.
					os.Remove(leftover)
				} else if _, err := os.Stat(leftover); err == nil {
					t.Errorf("%s is committed beside what render writes for %s; run the test with -update to remove it", leftover, target.Name)
				}
			})
		}
	}
	for _, form := range testsupport.ExampleForms {
		if form.Table && ran[form.Dir] == len(Targets) && !rendered[form.Dir] {
			t.Errorf("examples/%s commits rpc-table.json, which no target renders, so no test compares it", form.Dir)
		}
	}
}

// Every proxy with a generator declares its support of every key, and every declared proxy has a generator: cli
// accepts a --target that render.Targets holds, and render refuses one that either list lacks, so two lists that
// disagree would let the command line accept a target that render then refuses.
func TestEveryGeneratorIsDeclared(t *testing.T) {
	var generators, declared []string
	for _, target := range Targets {
		generators = append(generators, target.Name)
	}
	for _, target := range format.Targets {
		declared = append(declared, target.Name)
	}
	if want := []string{"envoy", "nginx"}; !slices.Equal(generators, want) || !slices.Equal(declared, want) {
		t.Errorf("render.Targets = %q, format.Targets = %q, want both %q", generators, declared, want)
	}
}

// testsupport.ExampleForms is the list every example test reads, so for each target it names each folder that commits
// the target's configuration or <target>.refused, and says Table exactly where the folder commits rpc-table.json; a
// folder or a table it leaves out is never compared.
func TestExampleFormsMatchTheFolders(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	var listed []string
	for _, form := range testsupport.ExampleForms {
		listed = append(listed, fmt.Sprintf("%s table=%t", form.Dir, form.Table))
	}
	// Both lists are sorted as strings, because Glob sorts by path, which can differ from the order of these strings.
	slices.Sort(listed)
	for _, target := range Targets {
		t.Run(target.Name, func(t *testing.T) {
			var onDisk []string
			for _, name := range []string{target.Config, target.Name + ".refused"} {
				committed, err := filepath.Glob(filepath.Join(root, "*", "*", name))
				if err != nil {
					t.Fatal(err)
				}
				for _, file := range committed {
					dir := filepath.Dir(file)
					rel, err := filepath.Rel(root, dir)
					if err != nil {
						t.Fatal(err)
					}
					_, statErr := os.Stat(filepath.Join(dir, "rpc-table.json"))
					onDisk = append(onDisk, fmt.Sprintf("%s table=%t", filepath.ToSlash(rel), statErr == nil))
				}
			}
			slices.Sort(onDisk)
			if !slices.Equal(listed, onDisk) {
				t.Errorf("testsupport.ExampleForms = %q, want the folders on disk %q", listed, onDisk)
			}
		})
	}
}

// One gateway, whichever way it is written: for each target, a render of each other form of an example is refused
// exactly when the example's yaml folder commits <target>.refused, and otherwise every file it writes equals the file
// the yaml folder commits, byte for byte. TestExamplesAreCurrent keeps the yaml folder's files equal to the render of
// the YAML form, and each form's own refusal lines, which name its own file, line and column.
func TestBothFormsGiveTheSameFiles(t *testing.T) {
	for _, target := range Targets {
		for _, form := range testsupport.ExampleForms {
			example, _, _ := strings.Cut(form.Dir, "/")
			yamlDir := example + "/yaml"
			if form.Dir == yamlDir {
				continue
			}
			t.Run(target.Name+"/"+form.Dir, func(t *testing.T) {
				req := requestFor(t, form.Dir, target.Config)
				req.Target = target.Name
				files, problems := render(req)
				_, statErr := os.Stat(filepath.Join(exampleDir(yamlDir), target.Name+".refused"))
				if refused, yamlRefused := problems != nil, statErr == nil; refused != yamlRefused {
					t.Fatalf("render problems = %q; refused = %t, want %t, as %s commits a %s.refused or not",
						testsupport.ProblemStrings(problems), refused, yamlRefused, yamlDir, target.Name)
				}
				if problems != nil {
					return
				}
				// The YAML form's render names the files, so a generator that writes a file beside its
				// configuration, as nginx writes proxyfile.js, is counted with it.
				yamlReq := requestFor(t, yamlDir, target.Config)
				yamlReq.Target = target.Name
				yamlFiles, _ := render(yamlReq)
				if got, want := baseNames(files), baseNames(yamlFiles); !slices.Equal(got, want) {
					t.Fatalf("render wrote %q, want %q, the files %s commits", got, want, yamlDir)
				}
				for _, file := range files {
					if d := committedDifference(t, yamlDir, file); d != "" {
						t.Errorf("rendered from %s: %s", req.Proxyfile, d)
					}
				}
			})
		}
	}
}

// committedDifference returns "" when file holds the bytes of the file of the same name committed in the example
// folder dir, <example>/<form>, and otherwise names the line, counted from 1, where the two first differ, with that
// line of each, so a failure points at the line to read instead of printing two whole files.
func committedDifference(t *testing.T, dir string, file File) string {
	t.Helper()
	path := filepath.Join(exampleDir(dir), filepath.Base(file.Path))
	committed := testsupport.ReadFile(t, path)
	if bytes.Equal(file.Bytes, committed) {
		return ""
	}
	line, inCommitted, rendered := testsupport.FirstDifferentLine(committed, file.Bytes)
	return fmt.Sprintf("%s differs first on line %d:\n  committed: %s\n  rendered:  %s", path, line, inCommitted, rendered)
}

// minimalNaming returns the minimal example with its inline origins replaced by "origins: <file>": the file form,
// which no example writes.
func minimalNaming(t *testing.T, file string) string {
	t.Helper()
	example := string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml"))
	if !strings.Contains(example, testsupport.MinimalInlineOrigins) {
		t.Fatalf("the minimal example holds no %q to replace", testsupport.MinimalInlineOrigins)
	}
	return strings.Replace(example, testsupport.MinimalInlineOrigins, "origins: "+file+"\n", 1)
}

// Inputs is what the command line's output-path rule protects, so it lists the files a HOCON proxyfile includes and
// the .proto files rpc.proto reads, and the origins file and the methods file only when the run read them: origins
// and rpc.methods written in the proxyfile have no path of their own.
func TestInputs(t *testing.T) {
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), minimalNaming(t, "origins.json"))
	testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(testsupport.ReadFile(t, "testdata/origins.json")))
	cases := []struct {
		name string
		req  Request
		want []string // <dir> stands for the test's temporary directory
	}{
		{name: "inline origins", req: Request{Proxyfile: "../../examples/minimal/yaml/proxyfile.yaml"},
			want: []string{"../../examples/minimal/yaml/proxyfile.yaml"}},
		{name: "origins file", req: Request{Proxyfile: filepath.Join(dir, "g.yaml")},
			want: []string{"<dir>/g.yaml", "<dir>/origins.json"}},
		{name: "inline methods", req: Request{Proxyfile: "../../examples/full/yaml/proxyfile.yaml"},
			want: []string{"../../examples/full/yaml/proxyfile.yaml"}},
		{name: "methods file",
			req:  Request{Proxyfile: "../../examples/partial/yaml/proxyfile.yaml", RPCMethods: "../../examples/partial/methods.json"},
			want: []string{"../../examples/partial/yaml/proxyfile.yaml", "../../examples/partial/methods.json"}},
		{name: "included file", req: Request{Proxyfile: "../../examples/partial/hocon/Proxyfile"},
			want: []string{"../../examples/partial/hocon/Proxyfile", "../../examples/partial/rpc-methods.conf"}},
		{name: ".proto files", req: Request{Proxyfile: "../../examples/partial/proto-methods/Proxyfile"},
			want: []string{"../../examples/partial/proto-methods/Proxyfile",
				"../../examples/partial/proto/example/backend/v1/reports.proto",
				"../../examples/partial/proto/example/gateway/v1/route.proto"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, problems := Read(c.req)
			if problems != nil {
				t.Fatalf("read problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			var got []string
			for _, path := range run.Inputs() {
				got = append(got, strings.Replace(path, dir, "<dir>", 1))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Inputs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderProblems(t *testing.T) {
	// The minimal example in the file form, naming an origins file that is not there, and one that is.
	minimal, beside := minimalNaming(t, "missing.json"), minimalNaming(t, "origins.json")
	cases := []struct {
		name, proxyfile string // proxyfile is written to g.yaml in a temporary directory
		target          string // the request's Target, "" for envoy
		// named is, when set, the name of the file beside the configuration that the generator of a target nginx
		// declared for the row returns.
		named string
		want  []string
	}{
		// A generator names each file beside the configuration by a name alone, so a name that holds a folder is a bug
		// in proxyfile, and no file is written outside the folder of -o.
		{name: "a generator's file in the folder above", proxyfile: beside, target: "nginx", named: "../escape.js",
			want: []string{`envoy.yaml: proxyfile could not write this file, which is a bug in proxyfile: the generator named a file "../escape.js", which is not a name alone`}},
		{name: "a generator's file at an absolute path", proxyfile: beside, target: "nginx", named: "/escape.js",
			want: []string{`envoy.yaml: proxyfile could not write this file, which is a bug in proxyfile: the generator named a file "/escape.js", which is not a name alone`}},
		{name: "a generator's file in a folder below", proxyfile: beside, target: "nginx", named: "sub/inner.js",
			want: []string{`envoy.yaml: proxyfile could not write this file, which is a bug in proxyfile: the generator named a file "sub/inner.js", which is not a name alone`}},
		// The command line refuses a target render.Targets does not hold before it asks render, and render refuses one
		// as well, so no caller gets an Envoy file for another proxy.
		{name: "a target with no generator", proxyfile: minimal, target: "caddy",
			want: []string{"g.yaml: --target caddy: no generator for caddy in this version; see docs/portability.md"}},
		{name: "rpc entry without methods",
			proxyfile: "listen: 8000\nservices:\n  backend: {grpc: 5000}\norigins: origins.json\nroutes:\n  - rpc: {to: {\"*\": backend}}\n",
			want:      []string{"g.yaml:6:5: the routes have an rpc entry, so rpc.methods, rpc.proto or --rpc-methods must list the RPC methods"}},
		{name: "origins file missing", proxyfile: minimal,
			want: []string{"missing.json: the file cannot be read: no such file or directory"}},
		// An absolute path is read as written, not under the proxyfile's directory, so the problem names it whole.
		{name: "origins file missing at an absolute path",
			proxyfile: strings.Replace(minimal, "origins: missing.json", "origins: /proxyfile-test-absent/origins.json", 1),
			want:      []string{"/proxyfile-test-absent/origins.json: the file cannot be read: no such file or directory"}},
		// render reads the origins file before it applies the rpc-entry rule, and g.yaml sorts before origins-absent.json,
		// so the two come out together and in this order only when the stages run together and their problems are sorted.
		{name: "problems of two inputs, together and sorted",
			proxyfile: "listen: 8000\nservices:\n  backend: {grpc: 5000}\norigins: origins-absent.json\nroutes:\n  - rpc: {to: {\"*\": backend}}\n",
			want: []string{
				"g.yaml:6:5: the routes have an rpc entry, so rpc.methods, rpc.proto or --rpc-methods must list the RPC methods",
				"origins-absent.json: the file cannot be read: no such file or directory",
			}},
	}
	origins := testsupport.ReadFile(t, "testdata/origins.json")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), c.proxyfile)
			testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(origins))
			if c.named != "" {
				declareNginx(t, []generator.File{{Bytes: []byte("config\n")}, {Name: c.named, Bytes: []byte("script\n")}})
			}
			files, problems := render(Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml"), Target: c.target})
			if files != nil {
				t.Errorf("render returned files %q next to problems", paths(files))
			}
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Errorf("problems = %q, want %q", got, c.want)
			}
		})
	}
}

// A render for one proxy reads the core keys and its own block and leaves the other blocks alone, so one file carries
// what each proxy needs. A render for nginx reads nothing of the envoy block, refuses none of it and writes the grpc
// example's own nginx.conf and proxyfile.js whatever the block holds, while a render for envoy reads each key as
// written.
func TestReadLeavesTheEnvoyBlockToEnvoy(t *testing.T) {
	example := string(testsupport.ReadFile(t, filepath.Join(exampleDir("grpc/yaml"), "proxyfile.yaml")))
	// The example ends with its own envoy block, which each row replaces with the block it reads.
	base, found := strings.CutSuffix(example, "envoy:\n  connect_bridge: false\n")
	if !found {
		t.Fatal("examples/grpc/yaml/proxyfile.yaml does not end with its envoy block")
	}
	// Absolute, because each row runs in a folder of its own, so that the positions it reads name g.yaml alone.
	committed, err := filepath.Abs(exampleDir("grpc/yaml"))
	if err != nil {
		t.Fatal(err)
	}
	xray := "envoy:\n  tracing:\n    provider:\n      xray: {segment: example-gateway}\n"
	cases := []struct {
		name, block string
		envoy       model.Envoy    // what a render for envoy reads as the block
		tracing     *model.Tracing // what a render for envoy reads as tracing
		problems    []string       // what a render for either target answers instead
	}{
		{name: "namespace", block: "envoy: {namespace: edge}\n", envoy: model.Envoy{Namespace: "edge", ConnectBridge: true}},
		{name: "heap", block: "envoy: {heap: 192MiB}\n", envoy: model.Envoy{Namespace: "proxyfile", ConnectBridge: true,
			Heap: &model.Size{Bytes: 201326592, Pos: model.Pos{File: "g.yaml", Line: 15, Column: 15}}}},
		{name: "connect_bridge", block: "envoy: {connect_bridge: true}\n", envoy: model.Envoy{Namespace: "proxyfile", ConnectBridge: true}},
		{name: "tracing", block: xray, envoy: model.Envoy{Namespace: "proxyfile", ConnectBridge: true},
			tracing: &model.Tracing{XRay: &model.XRay{Address: "127.0.0.1", Port: 2000, Segment: "example-gateway"}}},
		// The reader refuses a file that writes both places before the target's rule runs, so a Tracing read with
		// envoy.tracing written came from the block.
		{name: "both places", block: "tracing:\n  provider:\n    xray: {segment: s}\n" + xray,
			problems: []string{"g.yaml:19:3: write tracing or envoy.tracing, not both"}},
	}
	for _, c := range cases {
		for _, target := range []string{"nginx", "envoy"} {
			t.Run(c.name+"/"+target, func(t *testing.T) {
				t.Chdir(t.TempDir())
				testsupport.WriteFile(t, "g.yaml", base+c.block)
				run, problems := Read(Request{Proxyfile: "g.yaml", Out: "nginx.conf", Target: target})
				if got := testsupport.ProblemStrings(problems); !slices.Equal(got, c.problems) {
					t.Fatalf("Read problems = %q, want %q", got, c.problems)
				}
				if problems != nil {
					return
				}
				envoy, tracing := c.envoy, c.tracing
				if target == "nginx" {
					envoy, tracing = model.Envoy{}, nil
				}
				if got := run.input.Gateway; !reflect.DeepEqual(got.Envoy, envoy) || !reflect.DeepEqual(got.Tracing, tracing) {
					t.Errorf("Envoy = %s, Tracing = %s, want %s and %s", asJSON(t, got.Envoy), asJSON(t, got.Tracing),
						asJSON(t, envoy), asJSON(t, tracing))
				}
				if target != "nginx" {
					return
				}
				files, problems := run.Files()
				if got := paths(files); problems != nil || !slices.Equal(got, []string{"nginx.conf", "proxyfile.js"}) {
					t.Fatalf("Files = %q, problems %q, want nginx.conf and proxyfile.js", got, testsupport.ProblemStrings(problems))
				}
				for _, file := range files {
					path := filepath.Join(committed, file.Path)
					if want := testsupport.ReadFile(t, path); !bytes.Equal(file.Bytes, want) {
						line, inCommitted, rendered := testsupport.FirstDifferentLine(want, file.Bytes)
						t.Errorf("%s differs first on line %d:\n  committed: %s\n  rendered:  %s", path, line, inCommitted, rendered)
					}
				}
			})
		}
	}
}

// asJSON is value as JSON, so a failure shows what a pointer field holds rather than its address.
func asJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(data)
}

// The RPC methods written in the proxyfile, as rpc.methods or read from the .proto files rpc.proto names, give what the
// methods file gives, either one together with the methods file is refused, and the methods' own rules hold in every
// form.
func TestInlineMethods(t *testing.T) {
	partial := string(testsupport.ReadFile(t, "../../examples/partial/yaml/proxyfile.yaml")) + testsupport.PartialMethods
	// The minimal example has no rpc entry, so methods written into it are given for no RPC route.
	minimalMethods := string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml")) +
		"rpc:\n  methods:\n    - {service: example.backend.v1.ReportService, method: ListReports}\n"
	// The HOCON row writes its own list in place of the partial example's include line. strings.Replace changes nothing
	// when that line is missing, and the row would then fail on another problem, which does not say why; the guard
	// does.
	partialConf := string(testsupport.ReadFile(t, "../../examples/partial/hocon/Proxyfile"))
	const include = `include required("../rpc-methods.conf")` + "\n"
	if !strings.Contains(partialConf, include) {
		t.Fatalf("the partial HOCON example holds no %q to replace", include)
	}
	twice := strings.Replace(partialConf, include, `rpc { methods = [ { service = "example.backend.v1.ReportService", method = ListReports }, `+
		`{ service = "example.backend.v1.ReportService", method = ListReports } ] }`+"\n", 1)
	// The partial example's .proto files by their absolute path, which is read as written.
	protoRoot, err := filepath.Abs("../../examples/partial/proto")
	if err != nil {
		t.Fatal(err)
	}
	partialYAML := string(testsupport.ReadFile(t, "../../examples/partial/yaml/proxyfile.yaml"))
	partialProto := partialYAML + "rpc:\n  proto: {roots: [" + protoRoot + "], option: example.gateway.v1.route}\n"
	// The compiler's test tree, whose package o.v1 the mapping example.* does not match, so its methods' problems name
	// their .proto lines.
	formsRoot, err := filepath.Abs("../protofiles/testdata/forms")
	if err != nil {
		t.Fatal(err)
	}
	const anyPackage = `"*": backend`
	if !strings.Contains(partialYAML, anyPackage) {
		t.Fatalf("the partial example holds no %q to replace", anyPackage)
	}
	unmatched := strings.Replace(partialYAML, anyPackage, `"example.*": backend`, 1) +
		"rpc:\n  proto: {roots: [" + formsRoot + "], option: o.v1.route}\n"
	// The partial example's proto-methods Proxyfile with its root written whole, so the row reads the same files from a
	// temporary directory.
	partialProxyfile := string(testsupport.ReadFile(t, "../../examples/partial/proto-methods/Proxyfile"))
	const relativeRoot = "roots = [../proto]"
	if !strings.Contains(partialProxyfile, relativeRoot) {
		t.Fatalf("the partial Proxyfile holds no %q to replace", relativeRoot)
	}
	proxyfileProto := strings.Replace(partialProxyfile, relativeRoot, "roots = [\""+protoRoot+"\"]", 1)
	// The compiler's own problems stop the run and name the place written: a root's at the root, an option's at the
	// option key, which this one writes on a line of its own.
	emptyRoot, err := filepath.Abs("../protofiles/testdata/empty")
	if err != nil {
		t.Fatal(err)
	}
	noProtoFile := partialYAML + "rpc:\n  proto: {roots: [" + emptyRoot + "], option: example.gateway.v1.route}\n"
	noOption := partialYAML + "rpc:\n  proto:\n    roots: [" + protoRoot + "]\n    option: example.gateway.v1.nowhere\n"
	// A tree whose methods hold a control character in an action or a report, which the partial example's "*" mapping
	// routes.
	controlRoot, err := filepath.Abs("../protofiles/testdata/control")
	if err != nil {
		t.Fatal(err)
	}
	controlProto := partialYAML + "rpc:\n  proto: {roots: [" + controlRoot + "], option: o.v1.route}\n"
	cases := []struct {
		name, file, proxyfile string // proxyfile is written to file in a temporary directory
		rpcMethods            string // given as --rpc-methods when set
		// want is the problems; a row that wants none compares each file with the partial example's committed one.
		want []string
	}{
		{name: "inline gives the flag's table", file: "g.yaml", proxyfile: partial},
		{name: "both given", file: "g.yaml", proxyfile: partial, rpcMethods: "../../examples/partial/methods.json",
			want: []string{"g.yaml:29:3: rpc.methods is written here, so --rpc-methods must not be given"}},
		{name: "rpc.proto at an absolute root gives the flag's table", file: "g.yaml", proxyfile: partialProto},
		{name: "rpc.proto and the flag", file: "g.yaml", proxyfile: partialProto, rpcMethods: "../../examples/partial/methods.json",
			want: []string{"g.yaml:29:3: rpc.proto is written here, so --rpc-methods must not be given"}},
		{name: "HOCON: rpc.proto and the flag", file: "Proxyfile", proxyfile: proxyfileProto, rpcMethods: "../../examples/partial/methods.json",
			want: []string{"Proxyfile: rpc.proto: rpc.proto is written here, so --rpc-methods must not be given"}},
		{name: "rpc.proto: a method no mapping matches, at its .proto line", file: "g.yaml", proxyfile: unmatched, want: []string{
			formsRoot + "/o/v1/s.proto:8:3: no rpc mapping matches o.v1.S",
			formsRoot + "/o/v1/s.proto:12:3: no rpc mapping matches o.v1.S",
		}},
		{name: "rpc.proto: a control character in an action or a report, at its .proto line", file: "g.yaml", proxyfile: controlProto, want: []string{
			controlRoot + `/o/v1/s.proto:18:3: the action "a\nb" contains a newline; no key or value may hold a control character other than tab`,
			controlRoot + `/o/v1/s.proto:21:3: the action "a\rb" contains a carriage return; no key or value may hold a control character other than tab`,
			controlRoot + `/o/v1/s.proto:24:3: the action "a\x00b" contains NUL; no key or value may hold a control character other than tab`,
			controlRoot + `/o/v1/s.proto:27:3: the report "c\x01d" contains the control character U+0001; no key or value may hold a control character other than tab`,
		}},
		{name: "rpc.proto: a root with no .proto file, at the root", file: "g.yaml", proxyfile: noProtoFile,
			want: []string{"g.yaml:29:19: the root " + strconv.Quote(emptyRoot) + " holds no .proto file"}},
		{name: "rpc.proto: an option no file defines, at the option key", file: "g.yaml", proxyfile: noOption, want: []string{
			"g.yaml:31:5: no compiled file defines the option example.gateway.v1.nowhere; the compiled files are those under " +
				"the roots that declare a service, and the files they import"}},
		{name: "HOCON: a method listed twice", file: "g.conf", proxyfile: twice, want: []string{
			"g.conf: rpc.methods[1]: the method example.backend.v1.ReportService/ListReports is listed twice (first at rpc.methods[0])"}},
		{name: "inline, no rpc entry", file: "g.yaml", proxyfile: minimalMethods,
			want: []string{"g.yaml:8:1: RPC methods are given, but the routes have no rpc entry"}},
		// With both given, one run names the inline methods' own problem as well as the second list, sorted.
		{name: "both given, no rpc entry", file: "g.yaml", proxyfile: minimalMethods, rpcMethods: "../../examples/partial/methods.json",
			want: []string{
				"g.yaml:8:1: RPC methods are given, but the routes have no rpc entry",
				"g.yaml:11:3: rpc.methods is written here, so --rpc-methods must not be given",
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			req := Request{Proxyfile: filepath.Join(dir, c.file), Out: filepath.Join(dir, "envoy.yaml"),
				RPCMethods: c.rpcMethods, RPCTable: filepath.Join(dir, "rpc-table.json")}
			testsupport.WriteFile(t, req.Proxyfile, c.proxyfile)
			files, problems := render(req)
			if got := testsupport.ProblemStringsIn(dir, problems); !slices.Equal(got, c.want) {
				t.Fatalf("problems = %q, want %q", got, c.want)
			}
			if c.want != nil {
				return
			}
			if got, want := paths(files), outputs(req); !slices.Equal(got, want) {
				t.Fatalf("render paths = %q, want %q", got, want)
			}
			for _, file := range files {
				if d := committedDifference(t, "partial/yaml", file); d != "" {
					t.Error(d)
				}
			}
		})
	}
}

// A tool may switch the check by editing this line of the generated file as text, so in every example the line appears
// once, in the state the file's check has, and the other state's line never does: a tool that switches the check back
// must not find a second line either. An example without a check has neither.
func TestAuthSwitchLine(t *testing.T) {
	const (
		on  = "default_value: {numerator: 100, denominator: HUNDRED}"
		off = "default_value: {numerator: 0, denominator: HUNDRED}"
	)
	// By example: how often the line reads on, and how often off.
	want := map[string][2]int{"full": {1, 0}, "tracing": {1, 0}, "partial": {0, 1}, "portable": {1, 0}}
	for _, form := range testsupport.ExampleForms {
		t.Run(form.Dir, func(t *testing.T) {
			files, problems := render(requestFor(t, form.Dir, "envoy.yaml"))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			example, _, _ := strings.Cut(form.Dir, "/")
			got := [2]int{bytes.Count(files[0].Bytes, []byte(on)), bytes.Count(files[0].Bytes, []byte(off))}
			if got != want[example] {
				t.Errorf("the lines on and off appear %v times, want %v", got, want[example])
			}
		})
	}
}

// An RPC route's question for the check: a header only when it has a value, no entry at all for a method that asks
// nothing, and the question's entry beside the route's own bridge and Lua entries and its metadata.
func TestRPCQuestion(t *testing.T) {
	const proxyfile = `listen: 8000
services:
  q: {http: 3000, grpc: 3001}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow}
origins: origins.json
routes:
  - rpc: {to: {"*": q}}
  - {name: q, prefix: /, to: q}
`
	const methods = `[
  {"service": "q.v1.S", "method": "Nothing"},
  {"service": "q.v1.S", "method": "ActionAlone", "action": "use"},
  {"service": "q.v1.S", "method": "ReportsAlone", "reports": ["operate", "use"]},
  {"service": "q.v1.S", "method": "Both", "action": "use", "reports": ["operate"]}
]
`
	cases := []struct{ name, path, want string }{
		{name: "nothing asked", path: "/q.v1.S/Nothing", want: `
metadata: {filter_metadata: {proxyfile: {action: ""}}}
typed_per_filter_config:
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "action alone", path: "/q.v1.S/ActionAlone", want: `
metadata: {filter_metadata: {proxyfile: {action: use}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-action, value: use}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "reports alone", path: "/q.v1.S/ReportsAlone", want: `
metadata: {filter_metadata: {proxyfile: {action: ""}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-reports, value: "operate,use"}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
		{name: "both, beside the route's own entries", path: "/q.v1.S/Both", want: `
metadata: {filter_metadata: {proxyfile: {action: use}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-action, value: use}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
      - append: {header: {key: x-gateway-reports, value: operate}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
`},
	}
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), proxyfile)
	testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), string(testsupport.ReadFile(t, "testdata/origins.json")))
	testsupport.WriteFile(t, filepath.Join(dir, "methods.json"), methods)
	files, problems := render(Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml"),
		RPCMethods: filepath.Join(dir, "methods.json")})
	if problems != nil {
		t.Fatalf("render problems = %q, want none", testsupport.ProblemStringsIn(dir, problems))
	}
	routes := listAt(t, decode(t, files[0].Bytes), "static_resources", "listeners", 0, "filter_chains", 0, "filters", 0,
		"typed_config", "route_config", "virtual_hosts", 0, "routes")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := slices.IndexFunc(routes, func(route any) bool {
				return valueAt(route, "match", "path") == c.path
			})
			if i < 0 {
				t.Fatalf("no route of the first virtual host matches the path %s", c.path)
			}
			route := routes[i].(map[string]any)
			got := map[string]any{"metadata": route["metadata"], "typed_per_filter_config": route["typed_per_filter_config"]}
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// TestExampleParts pins the parts of the 2.1 examples' trees that are new in 2.1, each against a tree Envoy validated.
func TestExampleParts(t *testing.T) {
	manager := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config"}
	routes := slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes"}) // 0 the refusal, 1 and 2 the RPC routes, 3 login, 4 shop
	cases := []struct {
		name, dir string // dir is an example folder, <example>/<form>
		at        []any  // the sub-tree the row compares, as valueAt's steps
		// names compares only the name of each item of the list at at, as in TestPartsNoExampleCombines.
		names bool
		want  string
	}{
		{name: "balanced, shop's cluster", dir: "balanced/yaml", at: []any{"static_resources", "clusters", 0}, want: `
name: shop_http
type: STATIC
lb_policy: LEAST_REQUEST
load_assignment:
  cluster_name: shop_http
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 10.0.1.11, port_value: 8080}}}},
    {endpoint: {address: {socket_address: {address: 10.0.1.12, port_value: 8080}}}},
    {endpoint: {address: {socket_address: {address: 10.0.1.13, port_value: 8080}}}}]}]
health_checks:
- {timeout: 2s, interval: 5s, no_traffic_interval: 5s, healthy_threshold: 2, unhealthy_threshold: 3,
   http_health_check: {path: /healthz, expected_statuses: [{start: 200, end: 300}], retriable_statuses: [{start: 100, end: 600}]}}
common_lb_config: {healthy_panic_threshold: {value: 0}}
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http_protocol_options: {}}
`},
		{name: "balanced, orders' cluster", dir: "balanced/yaml", at: []any{"static_resources", "clusters", 1}, want: `
name: orders_grpc
type: STATIC
lb_policy: RANDOM
load_assignment:
  cluster_name: orders_grpc
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 10.0.2.21, port_value: 9000}}}},
    {endpoint: {address: {socket_address: {address: 10.0.2.22, port_value: 9000}}}}]}]
health_checks:
- {timeout: 1s, interval: 10s, no_traffic_interval: 10s, healthy_threshold: 1, unhealthy_threshold: 2,
   grpc_health_check: {}}
common_lb_config: {healthy_panic_threshold: {value: 0}}
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http2_protocol_options: {}}
`},
		{name: "dns, catalog's cluster", dir: "dns/yaml", at: []any{"static_resources", "clusters", 0}, want: `
name: catalog_http
cluster_type:
  name: envoy.cluster.dns
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.clusters.dns.v3.DnsCluster
    dns_lookup_family: ALL
    typed_dns_resolver_config:
      name: envoy.network.dns_resolver.cares
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.network.dns_resolver.cares.v3.CaresDnsResolverConfig
        resolvers: [{socket_address: {address: 10.0.0.2, port_value: 53}}]
lb_policy: RING_HASH
load_assignment:
  cluster_name: catalog_http
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: catalog.internal.example, port_value: 8080}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    hash_policy: [{header: {header_name: x-session-id}}]
    explicit_http_config: {http_protocol_options: {}}
`},
		{name: "dns, search's cluster", dir: "dns/yaml", at: []any{"static_resources", "clusters", 1}, want: `
name: search_http
cluster_type:
  name: envoy.cluster.dns
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.clusters.dns.v3.DnsCluster
    dns_lookup_family: ALL
    typed_dns_resolver_config:
      name: envoy.network.dns_resolver.cares
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.network.dns_resolver.cares.v3.CaresDnsResolverConfig
        resolvers: [{socket_address: {address: 10.0.0.2, port_value: 53}}]
lb_policy: RING_HASH
load_assignment:
  cluster_name: search_http
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: search.internal.example, port_value: 8090}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    hash_policy: [{connection_properties: {source_ip: true}}]
    explicit_http_config: {http_protocol_options: {}}
`},
		{name: "tls, the transport socket", dir: "tls/yaml", at: []any{"static_resources", "listeners", 0, "filter_chains", 0, "transport_socket"}, want: `
name: envoy.transport_sockets.tls
typed_config:
  "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
  common_tls_context:
    alpn_protocols: [h2, http/1.1]
    tls_certificates: [{certificate_chain: {filename: /etc/gateway/tls/certificate.pem}, private_key: {filename: /etc/gateway/tls/key.pem}}]
    validation_context: {trusted_ca: {filename: /etc/gateway/tls/clients-ca.pem}}
  require_client_certificate: true
`},
		// The example writes no client_address; tls makes it connection.
		{name: "tls, the client's address", dir: "tls/yaml",
			at: []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config", "use_remote_address"}, want: "true"},
		{name: "limited, the filter after CORS", dir: "limited/yaml", at: slices.Concat(manager, []any{"http_filters", 3}), want: `
name: envoy.filters.http.local_ratelimit
typed_config:
  "@type": type.googleapis.com/envoy.extensions.filters.http.local_ratelimit.v3.LocalRateLimit
  stat_prefix: rate_limit
  filter_enabled: {default_value: {numerator: 100, denominator: HUNDRED}}
  filter_enforced: {default_value: {numerator: 100, denominator: HUNDRED}}
  rate_limited_as_resource_exhausted: true
  always_consume_default_token_bucket: false
  max_dynamic_descriptors: 65536
  rate_limits:
  - actions:
    - metadata: {descriptor_key: bucket, metadata_key: {key: proxyfile.rate_limit, path: [{key: all}]}, source: ROUTE_ENTRY}
  - actions:
    - metadata: {descriptor_key: bucket, metadata_key: {key: proxyfile.rate_limit, path: [{key: client}]}, source: ROUTE_ENTRY}
    - remote_address: {}
  descriptors:
  - entries: [{key: bucket, value: rpc}]
    token_bucket: {max_tokens: 100, tokens_per_fill: 100, fill_interval: 1s}
  - entries: [{key: bucket, value: login}, {key: remote_address}]
    token_bucket: {max_tokens: 10, tokens_per_fill: 5, fill_interval: 60s}
`},
		{name: "limited, an RPC route's bucket", dir: "limited/yaml", at: slices.Concat(routes, []any{2, "metadata"}),
			want: `{filter_metadata: {proxyfile.rate_limit: {all: rpc}}}`},
		{name: "limited, login's bucket", dir: "limited/yaml", at: slices.Concat(routes, []any{3, "metadata"}),
			want: `{filter_metadata: {proxyfile.rate_limit: {client: login}}}`},
		{name: "limited, the route with no limit", dir: "limited/yaml", at: slices.Concat(routes, []any{4}),
			want: `{name: shop, match: {prefix: /}, route: {cluster: shop_http}}`},
		{name: "limited, the Connect answer", dir: "limited/yaml", at: slices.Concat(manager, []any{"local_reply_config", "mappers", 2}), want: `
filter:
  and_filter:
    filters:
    - status_code_filter:
        comparison: {op: EQ, value: {default_value: 429, runtime_key: gateway.local_reply.rate_limited_status}}
    - header_filter:
        header: {name: connect-protocol-version, present_match: true}
body: {inline_string: '{"code":"resource_exhausted","message":"local_rate_limited"}'}
body_format_override:
  text_format_source: {inline_string: "%LOCAL_REPLY_BODY%"}
  content_type: application/json
`},
		{name: "otel, the tracer", dir: "otel/yaml", at: slices.Concat(manager, []any{"tracing"}), want: `
client_sampling: {value: 0}
random_sampling: {value: 10}
max_path_tag_length: 0
custom_tags:
- tag: environment
  environment: {name: APP_ENVIRONMENT, default_value: local}
- tag: request_id
  request_header: {name: x-request-id}
provider:
  name: envoy.tracers.opentelemetry
  typed_config:
    "@type": type.googleapis.com/envoy.config.trace.v3.OpenTelemetryConfig
    grpc_service:
      envoy_grpc: {cluster_name: otel_collector}
    service_name: example-gateway
`},
		{name: "otel, the collector's cluster", dir: "otel/yaml", at: []any{"static_resources", "clusters", 1}, want: `
name: otel_collector
cluster_type:
  name: envoy.cluster.dns
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.clusters.dns.v3.DnsCluster
    dns_lookup_family: ALL
load_assignment:
  cluster_name: otel_collector
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: otel-collector, port_value: 4317}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http2_protocol_options: {}}
`},
		{name: "otel, a route names its span", dir: "otel/yaml", at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}),
			want: `{name: api, match: {prefix: /api/}, decorator: {operation: api, propagate: false}, route: {cluster: app_http}}`},
		{name: "otel, the early removal", dir: "otel/yaml", at: slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}), want: `
- remove: x-envoy-downstream-service-cluster
- remove: x-envoy-downstream-service-node
- remove: x-envoy-decorator-operation
`},
		{name: "jwt, its filters", dir: "jwt/yaml", at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors, envoy.filters.http.jwt_authn, envoy.filters.http.router]`},
		{name: "jwt, the token filter", dir: "jwt/yaml", at: slices.Concat(manager, []any{"http_filters", 3, "typed_config"}), want: `
"@type": type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication
providers:
  jwt:
    issuer: https://login.example.com
    audiences: [api.example.com]
    require_expiration: true
    local_jwks: {filename: /etc/gateway/jwt/jwks.json}
    forward: true
    from_headers: [{name: authorization, value_prefix: "Bearer "}]
    claim_to_headers: [{header_name: x-user-id, claim_name: sub}, {header_name: x-user-roles, claim_name: realm.roles}]
rules:
- match: {prefix: /}
  requires: {provider_name: jwt}
`},
		// With no ask there is no ext_authz filter, so the route turns off the token check alone.
		{name: "jwt, the sign-in route", dir: "jwt/yaml", at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}), want: `
name: sign-in
match: {prefix: /login/}
route: {cluster: identity_http}
typed_per_filter_config:
  envoy.filters.http.jwt_authn:
    "@type": type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.PerRouteConfig
    disabled: true
`},
		{name: "jwt, a client's claims headers removed", dir: "jwt/yaml",
			at: slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}), want: "[{remove: x-user-id}, {remove: x-user-roles}]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files, problems := render(requestFor(t, c.dir, "envoy.yaml"))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			got := valueAt(decode(t, files[0].Bytes), c.at...)
			if c.names {
				got = names(got)
			}
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// A route's retries and the rpc entry's retries write one retry policy: retried only when the request never reached
// the service, and to a backend not tried yet when the service has another (the previous_hosts predicate, whose count
// route-action.yaml explains), so a retry after a backend refused the connection reaches another backend. The full
// example writes retries: 1 on the shop route and on its rpc entry, whose routes go to shop's two backends and to
// accounts' one; a route without retries writes no policy.
func TestRetryPolicy(t *testing.T) {
	const policy = `{retry_on: "connect-failure,reset-before-request", num_retries: 1,
  retry_host_predicate: [{name: envoy.retry_host_predicates.previous_hosts,
    typed_config: {"@type": type.googleapis.com/envoy.extensions.retry.host.previous_hosts.v3.PreviousHostsPredicate}}],
  host_selection_retry_max_attempts: 20}`
	// 0 the refusal, 1 accounts-events, 2 to 4 the RPC routes (accounts, then shop's two), 5 accounts-webhooks, 6
	// catalog, 7 shop.
	routes := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config", "route_config",
		"virtual_hosts", 0, "routes"}
	cases := []struct {
		name  string
		route int
		want  string // the route's action
	}{
		{name: "a route's retries, to a service with two backends", route: 7,
			want: "{cluster: shop_http, timeout: 0s, idle_timeout: 90s, retry_policy: " + policy + "}"},
		{name: "the rpc entry's retries, to a service with two backends", route: 4,
			want: "{cluster: shop_grpc, timeout: 0s, idle_timeout: 90s, retry_policy: " + policy + "}"},
		{name: "the rpc entry's retries, to a service with one backend", route: 2,
			want: `{cluster: accounts_grpc, host_rewrite_literal: "127.0.0.1:7070", timeout: 0s, idle_timeout: 90s, retry_policy: ` + policy + "}"},
		{name: "a route without retries", route: 6, want: "{cluster: catalog_http, timeout: 0s, idle_timeout: 90s}"},
	}
	files, problems := render(requestFor(t, "full/yaml", "envoy.yaml"))
	if problems != nil {
		t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	tree := decode(t, files[0].Bytes)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := valueAt(tree, slices.Concat(routes, []any{c.route, "route"})...)
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// Every answer says that it depends on the Origin, and with auth.ask no answer carries x-envoy-upstream-service-time:
// Envoy's client for the check adds it to the check's answer, which a denial passes on with the check's own headers
// (measured on Envoy 1.40.2), so the header mutation filter removes it from every answer. A gateway without auth.ask
// has no source of it, and its filter is as before.
func TestResponseMutations(t *testing.T) {
	const vary = `{append: {header: {key: vary, value: Origin}, append_action: APPEND_IF_EXISTS_OR_ADD}}`
	cases := []struct {
		dir  string // an example folder, <example>/<form>
		want string
	}{
		{dir: "full/yaml", want: "[" + vary + ", {remove: x-envoy-upstream-service-time}]"},
		{dir: "minimal/yaml", want: "[" + vary + "]"},
	}
	filters := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config", "http_filters"}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			files, problems := render(requestFor(t, c.dir, "envoy.yaml"))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			list := listAt(t, decode(t, files[0].Bytes), filters...)
			i := slices.IndexFunc(list, func(filter any) bool { return valueAt(filter, "name") == "envoy.filters.http.header_mutation" })
			if i < 0 {
				t.Fatal("no envoy.filters.http.header_mutation filter")
			}
			got := valueAt(list[i], "typed_config", "mutations", "response_mutations")
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// The first route of every virtual host answers 400 to a path that still holds a dot segment with a ; parameter after
// normalisation, the ; written or escaped once, so no route of the file takes such a path: in a file with one
// environment and in the full example, whose three environments each start with it.
func TestRefusedRouteComesFirst(t *testing.T) {
	const refused = `
name: "refused:dot-segment-with-parameter"
match: {safe_regex: {regex: ".*/\\.\\.?(?:;|%3[Bb]).*"}}
direct_response: {status: 400}
`
	at := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config", "route_config", "virtual_hosts"}
	cases := []struct {
		dir   string // an example folder, <example>/<form>
		hosts int    // how many virtual hosts the file holds
	}{
		{dir: "minimal/yaml", hosts: 1},
		{dir: "full/yaml", hosts: 3},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			files, problems := render(requestFor(t, c.dir, "envoy.yaml"))
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
			}
			tree := decode(t, files[0].Bytes)
			virtualHosts := listAt(t, tree, at...)
			if got := len(virtualHosts); got != c.hosts {
				t.Fatalf("virtual hosts = %d, want %d", got, c.hosts)
			}
			for i := range virtualHosts {
				if d := firstDifference(decode(t, []byte(refused)), valueAt(virtualHosts[i], "routes", 0), ""); d != "" {
					t.Errorf("virtual host %d, route 0: %s", i, d)
				}
			}
		})
	}
}

// The refused route's pattern takes a segment that is . or .. followed by ;, written or escaped once in either case of
// hex, and nothing else. Envoy's safe_regex is RE2 matched against the whole path without its query, and Go's regexp
// reads RE2's syntax, so the pattern is anchored at both ends here. Each row is a path as Envoy can hold it after
// normalisation, which decodes escaped dots and keeps %3B and its case as sent; a dot segment with a written ; is
// still there where Envoy does not strip the parameter itself. The conformance battery sends the spellings to Envoy.
func TestRefusedRoutePattern(t *testing.T) {
	files, problems := render(requestFor(t, "minimal/yaml", "envoy.yaml"))
	if problems != nil {
		t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	at := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config", "route_config", "virtual_hosts"}
	route := valueAt(listAt(t, decode(t, files[0].Bytes), at...)[0], "routes", 0)
	pattern, ok := valueAt(route, "match", "safe_regex", "regex").(string)
	if !ok {
		t.Fatalf("route 0 has no match.safe_regex.regex: %v", route)
	}
	whole, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		t.Fatalf("the refused route's pattern %q is not RE2: %v", pattern, err)
	}
	cases := []struct {
		path    string
		refused bool
	}{
		{path: "/catalog/..;/products/1", refused: true},
		{path: "/catalog/..%3B/products/1", refused: true},
		{path: "/catalog/..%3b/products/1", refused: true},
		{path: "/catalog/items/.;/x", refused: true},
		{path: "/catalog/items/.%3B/x", refused: true},
		{path: "/catalog/items/.%3b/x", refused: true},
		{path: "/catalog/..%3Bx=1/products/1", refused: true},
		{path: "/catalog/..%3B", refused: true},
		{path: "/..%3B/products/1", refused: true},
		{path: "/a;v=1/b", refused: false},
		{path: "/catalog/readme.txt;x=1", refused: false},
		{path: "/catalog/.well-known;x/y", refused: false},
		{path: "/catalog/...;/x", refused: false},
		{path: "/catalog/...%3B/x", refused: false},
		{path: "/catalog/x..%3B/y", refused: false},
		{path: "/catalog/a%3Bb/c", refused: false},
		{path: "/catalog/..%253B/products/1", refused: false},
		{path: "/catalog/..%3Fx/y", refused: false},
	}
	for _, c := range cases {
		if got := whole.MatchString(c.path); got != c.refused {
			t.Errorf("pattern %q takes %q = %t, want %t", pattern, c.path, got, c.refused)
		}
	}
}

// Each row holds a combination no example has, so it is the only proof of the part it names: an rpc entry without auth
// adds the Lua filter and the bridge but not set_metadata or ext_authz, and its RPC routes carry no metadata and no
// question; a gateway with auth and no rpc entry has no Lua filter and no bridge; connections without heap watch
// connections alone; with the Connect bridge off there is no Lua filter and no bridge, and an RPC route carries its
// question alone; the check's switch header is the one only_with_header names; with auth, the early header removal adds
// the question headers and the accept names that remove_from_clients does not already cover; with tracing, the early
// header removal is written for the two downstream headers alone and leaves out one an entry covers; without sample no
// manifest is written; path_in_url: true writes no max_path_tag_length; with xray no route names its span; with otel
// the collector's name is asked of dns.resolver and an RPC route names its span; an environment that admits no
// browser origin writes an empty CORS list; with jwt keys at a URL, the token filter fetches them through a TLS cluster
// that names the URL's host, asked of dns.resolver, or matches its IP address; with jwt and ask, the token filter
// sits between the rate limit and the check, and a route with auth off turns both off; and a gateway with no auth
// normalises the request's path as every gateway does.
func TestPartsNoExampleCombines(t *testing.T) {
	manager := []any{"static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config"}
	const rpcWithoutAuth = `listen: 8000
services:
  q: {http: 3000, grpc: 3001}
origins: origins.json
routes:
  - rpc: {to: {"*": q}}
  - {name: q, prefix: /, to: q}
`
	const rpcMethod = `[{"service": "q.v1.S", "method": "Do", "action": "use", "reports": ["operate"]}]`
	// tracingBase is a gateway with neither auth nor remove_from_clients; each tracing row appends tracing or
	// envoy.tracing.
	const tracingBase = `listen: 8000
services:
  q: {http: 3000}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`
	// bridgeOff turns the Connect bridge off; the rows append it to a gateway with an rpc entry.
	const bridgeOff = "envoy: {connect_bridge: false}\n"
	// limitedWithAuth limits the rpc entry for every client together and two routes by client, one with auth off, in a
	// gateway with auth, two environments and its own namespace.
	const limitedWithAuth = `listen: 8000
services:
  shop: {http: 8080, grpc: 8081}
auth: {ask: shop /check, timeout: 1s, enabled: true, when_unavailable: deny}
origins:
  live: {hosts: [live.example]}
  test: {hosts: [test.example]}
routes:
  - rpc: {to: {"*": shop}, rate_limit: {requests: 40, per: 1s, by: all}}
  - {name: login, path: /login, to: shop, auth: off, rate_limit: {requests: 5, per: 1m}}
  - {name: shop, prefix: /, to: shop, rate_limit: {requests: 100, per: 1s, burst: 50}}
rpc:
  methods:
    - {service: example.shop.v1.CartService, method: AddItem, action: buy}
envoy: {namespace: edge}
`
	// askWithRequest is a gateway whose check is told which request it is asked about. Its filters are set_metadata,
	// header_mutation, rbac, cors, ext_authz and router.
	const askWithRequest = `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: deny, send_request: true}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`
	// jwtByURL verifies tokens with keys fetched from a URL whose host is a name, asked of dns.resolver, with no check;
	// jwtAtAnAddress fetches them from an IP address and a port.
	const jwtByURL = "listen: 8000\nservices:\n  q: {http: 3000}\ndns: {resolver: \"10.0.0.2:53\"}\n" +
		"auth: {jwt: {issuer: https://login.example.com, audiences: any, keys: \"https://login.example.com/.well-known/jwks.json\", " +
		"ca: /etc/gateway/jwt/ca.pem, header: x-access-token, optional: true}}\norigins: origins.json\nroutes:\n  - {name: q, prefix: /, to: q}\n"
	jwtAtAnAddress := strings.Replace(jwtByURL, "https://login.example.com/.well-known/jwks.json", "https://10.0.3.50:8443/jwks.json", 1)
	// limitedWithJWT is limitedWithAuth with a token verified as well as the check.
	limitedWithJWT := strings.Replace(limitedWithAuth, "when_unavailable: deny}", "when_unavailable: deny, jwt: {issuer: i, audiences: [a], keys: /k.json}}", 1)
	// limiter is the rate limit filter's typed_config in limitedWithAuth: the fifth filter, after CORS and before the
	// check.
	limiter := slices.Concat(manager, []any{"http_filters", 4, "typed_config"})
	// healthGateway is rpcWithoutAuth with its service q written as given.
	healthGateway := func(service string) string {
		return strings.Replace(rpcWithoutAuth, "q: {http: 3000, grpc: 3001}", service, 1)
	}
	cases := []struct {
		name, proxyfile, methods string // methods is written to methods.json and given to render when not empty
		origins                  string // written to origins.json; testdata/origins.json when empty
		at                       []any  // the sub-tree the row compares, as valueAt's steps
		// names compares only the name of each item of the list at at, which keeps the Lua script out of a filter row.
		names bool
		// keys compares only these keys of the mapping at at, so one row pins a few settings of a large mapping.
		keys []string
		want string
	}{
		{name: "rpc without auth, its filters", proxyfile: rpcWithoutAuth, methods: rpcMethod,
			at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors, envoy.filters.http.lua,
  envoy.filters.http.connect_grpc_bridge, envoy.filters.http.router]`},
		{name: "rpc without auth", proxyfile: rpcWithoutAuth, methods: rpcMethod,
			at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}),
			want: `
name: q-rpc
match: {path: /q.v1.S/Do}
typed_per_filter_config:
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
route: {cluster: q_grpc}
`},
		// With the Connect bridge off, the Lua filter and the bridge are left out, and an RPC route carries no entry for
		// them; with auth, the question's entry stays, alone.
		{name: "bridge off, its filters", proxyfile: rpcWithoutAuth + bridgeOff, methods: rpcMethod,
			at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors, envoy.filters.http.router]`},
		{name: "bridge off", proxyfile: rpcWithoutAuth + bridgeOff, methods: rpcMethod,
			at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}),
			want: `
name: q-rpc
match: {path: /q.v1.S/Do}
route: {cluster: q_grpc}
`},
		{name: "bridge off, with auth",
			proxyfile: strings.Replace(rpcWithoutAuth, "origins:", "auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow}\norigins:", 1) + bridgeOff,
			methods:   rpcMethod, at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}),
			want: `
name: q-rpc
match: {path: /q.v1.S/Do}
metadata: {filter_metadata: {proxyfile: {action: use}}}
typed_per_filter_config:
  envoy.filters.http.header_mutation:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutationPerRoute
    mutations:
      request_mutations:
      - append: {header: {key: x-gateway-action, value: use}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
      - append: {header: {key: x-gateway-reports, value: operate}, append_action: OVERWRITE_IF_EXISTS_OR_ADD}
route: {cluster: q_grpc}
`},
		{name: "auth without rpc",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: slices.Concat(manager, []any{"http_filters"}),
			want: `
- name: envoy.filters.http.set_metadata
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.set_metadata.v3.Config
    metadata: [{metadata_namespace: proxyfile, value: {filtered: true}}]
- name: envoy.filters.http.header_mutation
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutation
    mutations: {response_mutations: [{append: {header: {key: vary, value: Origin}, append_action: APPEND_IF_EXISTS_OR_ADD}},
      {remove: x-envoy-upstream-service-time}]}
- {name: envoy.filters.http.rbac, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBAC}}
- {name: envoy.filters.http.cors, typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.cors.v3.Cors}}
- name: envoy.filters.http.ext_authz
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
    http_service:
      server_uri: {uri: "http://127.0.0.1:3000/check", cluster: q_http, timeout: 1s}
      path_override: /check
      authorization_request: {headers_to_add: [{key: x-gateway-timeout-ms, value: "1000"}]}
    failure_mode_allow: true
    failure_mode_allow_header_add: true
    allowed_headers: {patterns: [{exact: x-gateway-action}, {exact: x-gateway-reports}]}
    emit_filter_state_stats: true
    filter_enabled: {default_value: {numerator: 100, denominator: HUNDRED}, runtime_key: gateway.ext_authz.enabled}
- name: envoy.filters.http.router
  typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router, suppress_envoy_headers: true}
`},
		{name: "connections without heap",
			proxyfile: `listen: 8000
limits: {connections: 100}
services:
  q: {http: 3000}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: []any{"overload_manager"},
			want: `
refresh_interval: 0.25s
resource_monitors:
- name: envoy.resource_monitors.global_downstream_max_connections
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig
    max_active_downstream_connections: 100
`},
		{name: "only_with_header other than authorization",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow, only_with_header: x-api-key}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			// The filters before ext_authz are set_metadata, the header mutation, RBAC and CORS.
			at: slices.Concat(manager, []any{"http_filters", 4, "typed_config", "xds_matcher"}),
			want: `
matcher_list:
  matchers:
  - predicate:
      not_matcher:
        single_predicate:
          input:
            name: x-api-key
            typed_config:
              "@type": type.googleapis.com/envoy.type.matcher.v3.HttpRequestHeaderMatchInput
              header_name: x-api-key
          value_match: {safe_regex: {google_re2: {}, regex: ".+"}}
    on_match:
      action:
        name: skip
        typed_config: {"@type": type.googleapis.com/envoy.extensions.filters.common.matcher.action.v3.SkipFilter}
`},
		// x-identity is covered by the user's own entry, so it is not removed twice; x-tier-* is not, so its prefix is
		// added after the question headers.
		{name: "auth removes the check's headers from clients",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
auth: {ask: q /check, timeout: 1s, enabled: true, when_unavailable: allow, accept: [x-identity, x-tier-*]}
remove_from_clients: [x-identity]
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			at: slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}),
			want: `
- remove: x-identity
- remove: x-gateway-action
- remove: x-gateway-reports
- remove_on_match: {key_matcher: {prefix: x-tier-}}
- remove: x-envoy-auth-failure-mode-allowed
`},
		// With tracing and nothing else to remove, the early header removal is written for the two tracing headers alone.
		{name: "tracing alone removes the two downstream headers", proxyfile: tracingBase + "envoy: {tracing: {provider: {xray: {segment: s}}}}\n",
			at: slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}),
			want: `
- remove: x-envoy-downstream-service-cluster
- remove: x-envoy-downstream-service-node
`},
		// Without sample no manifest is written, so Envoy's own rule applies; without tags there is no custom_tags.
		{name: "tracing with the fewest keys", proxyfile: tracingBase + "envoy: {tracing: {provider: {xray: {segment: s}}}}\n", at: slices.Concat(manager, []any{"tracing"}),
			want: `
max_path_tag_length: 0
provider:
  name: envoy.tracers.xray
  typed_config:
    "@type": type.googleapis.com/envoy.config.trace.v3.XRayConfig
    daemon_endpoint: {protocol: UDP, address: 127.0.0.1, port_value: 2000}
    segment_name: s
`},
		// path_in_url leaves Envoy's default length; two rules are written in the order given, the second under the first.
		{name: "tracing with the path and two rules",
			proxyfile: tracingBase + `envoy: {tracing: {provider: {xray: {segment: s, agent: 10.0.0.9:2001, sample: {first: 3, rate: 50%, rules: [{path: /a, first: 1, rate: 100%}, {host: api.example.com, method: GET, first: 0, rate: 10%}]}}}, path_in_url: true}}` + "\n",
			at:        slices.Concat(manager, []any{"tracing"}),
			want: `
provider:
  name: envoy.tracers.xray
  typed_config:
    "@type": type.googleapis.com/envoy.config.trace.v3.XRayConfig
    daemon_endpoint: {protocol: UDP, address: 10.0.0.9, port_value: 2001}
    segment_name: s
    sampling_rule_manifest:
      inline_string: |
        {"version": 2,
         "default": {"fixed_target": 3, "rate": 0.5},
         "rules": [{"host": "*", "http_method": "*", "url_path": "/a", "fixed_target": 1, "rate": 1},
                   {"host": "api.example.com", "http_method": "GET", "url_path": "*", "fixed_target": 0, "rate": 0.1}]}
`},
		// An entry of remove_from_clients that covers both removes them, so neither is written again.
		{name: "remove_from_clients covers the two downstream headers",
			proxyfile: tracingBase + "remove_from_clients: [x-envoy-*]\nenvoy: {tracing: {provider: {xray: {segment: s}}}}\n",
			at:        slices.Concat(manager, []any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}),
			want: `
- remove_on_match: {key_matcher: {prefix: x-envoy-}}
`},
		// X-Ray keeps 2.0's bytes: a client's x-envoy-decorator-operation renames no segment (measured on Envoy 1.40.2), so
		// no route names its span and the header is not removed.
		{name: "xray, no decorator", proxyfile: tracingBase + "tracing: {provider: {xray: {segment: s}}}\n",
			at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}), want: `{name: q, match: {prefix: /}, route: {cluster: q_http}}`},
		// The collector's name is asked of dns.resolver, as a service's address is.
		{name: "otel endpoint by name, the resolver", proxyfile: tracingBase + "dns: {resolver: \"10.0.0.2:53\"}\n" +
			`tracing: {provider: {otel: {endpoint: "collector.internal:4317", service: s}}}` + "\n",
			at:   []any{"static_resources", "clusters", 1, "cluster_type", "typed_config", "typed_dns_resolver_config", "typed_config", "resolvers"},
			want: `[{socket_address: {address: 10.0.0.2, port_value: 53}}]`},
		{name: "otel, an RPC route names its span", proxyfile: rpcWithoutAuth + `tracing: {provider: {otel: {endpoint: "127.0.0.1:4317", service: s}}}` + "\n",
			methods: rpcMethod, at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1}), want: `
name: q-rpc
match: {path: /q.v1.S/Do}
decorator: {operation: q-rpc, propagate: false}
typed_per_filter_config:
  envoy.filters.http.connect_grpc_bridge: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
  envoy.filters.http.lua: {"@type": type.googleapis.com/envoy.config.route.v3.FilterConfig, config: {}}
route: {cluster: q_grpc}
`},
		// RBAC admits only a request without an Origin and an origin on the environment's own host, and the CORS policy
		// keeps its list, written empty.
		{name: "an environment with no origins and no patterns",
			proxyfile: `listen: 8000
services:
  q: {http: 3000}
origins: origins.json
routes:
  - {name: q, prefix: /, to: q}
`,
			origins: `{"environments": [{"name": "closed", "hosts": ["localhost"]}]}`,
			at:      slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "typed_per_filter_config"}),
			want: `
envoy.filters.http.rbac:
  "@type": type.googleapis.com/envoy.extensions.filters.http.rbac.v3.RBACPerRoute
  rbac:
    rules:
      action: ALLOW
      policies:
        browser-origins:
          permissions: [{any: true}]
          principals:
          - {not_id: {header: {name: origin, present_match: true}}}
          - {header: {name: origin, string_match: {safe_regex: {regex: '^https?://localhost(:[0-9]+)?$'}}}}
envoy.filters.http.cors:
  "@type": type.googleapis.com/envoy.extensions.filters.http.cors.v3.CorsPolicy
  allow_origin_string_match: []
  forward_not_matching_preflights: false
`},
		// A balance written as its default renders 2.0's cluster, with no lb_policy.
		{name: "balance round_robin written", proxyfile: strings.Replace(tracingBase, "q: {http: 3000}", "q: {http: 3000, balance: round_robin}", 1),
			at: []any{"static_resources", "clusters", 0}, want: `
name: q_http
type: STATIC
load_assignment:
  cluster_name: q_http
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 127.0.0.1, port_value: 3000}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http_protocol_options: {}}
`},
		// The hash policy sits on the cluster, so the gRPC cluster of an RPC route carries it too; with no dns.resolver
		// the cluster asks the host's resolver configuration and writes no typed_dns_resolver_config.
		{name: "hash on a gRPC service found by name",
			proxyfile: `listen: 8000
services:
  q: {grpc: 3001, address: q.internal, balance: hash, hash: {header: x-user}}
origins: origins.json
routes:
  - rpc: {to: {"*": q}}
`,
			methods: rpcMethod, at: []any{"static_resources", "clusters", 0}, want: `
name: q_grpc
cluster_type:
  name: envoy.cluster.dns
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.clusters.dns.v3.DnsCluster
    dns_lookup_family: ALL
lb_policy: RING_HASH
load_assignment:
  cluster_name: q_grpc
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: q.internal, port_value: 3001}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    hash_policy: [{header: {header_name: x-user}}]
    explicit_http_config: {http2_protocol_options: {}}
`},
		// A resolver is written only on a cluster that looks a name up, so a gateway with no name renders 2.0's cluster.
		{name: "a resolver with no name", proxyfile: tracingBase + "dns: {resolver: \"10.0.0.2\"}\n",
			at: []any{"static_resources", "clusters", 0}, want: `
name: q_http
type: STATIC
load_assignment:
  cluster_name: q_http
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 127.0.0.1, port_value: 3000}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http_protocol_options: {}}
`},
		// Measured on Envoy 1.40.2: without ignore_health_on_host_removal a backend that left the name's answer stayed in
		// the pool while it passed its checks. A STATIC cluster never writes the key: the balanced rows of
		// TestExampleParts compare the whole cluster.
		{name: "health on a named service", proxyfile: healthGateway("q: {http: 3000, grpc: 3001, address: q.internal, health: {path: /healthz}}"),
			methods: rpcMethod, at: []any{"static_resources", "clusters", 0, "ignore_health_on_host_removal"}, want: `true`},
		{name: "health with host_header, the HTTP check",
			proxyfile: healthGateway("q: {http: 3000, grpc: 3001, host_header: q.internal, health: {path: /healthz, grpc: true}}"),
			methods:   rpcMethod, at: []any{"static_resources", "clusters", 0, "health_checks"}, want: `[{timeout: 2s, interval: 5s,
  no_traffic_interval: 5s, healthy_threshold: 2, unhealthy_threshold: 3, http_health_check: {path: /healthz,
  expected_statuses: [{start: 200, end: 300}], retriable_statuses: [{start: 100, end: 600}], host: q.internal}}]`},
		{name: "health with host_header, the gRPC check",
			proxyfile: healthGateway("q: {http: 3000, grpc: 3001, host_header: q.internal, health: {path: /healthz, grpc: true}}"),
			methods:   rpcMethod, at: []any{"static_resources", "clusters", 1, "health_checks"}, want: `[{timeout: 2s, interval: 5s,
  no_traffic_interval: 5s, healthy_threshold: 2, unhealthy_threshold: 3, grpc_health_check: {authority: q.internal}}]`},
		// A service with both ports and only path checks its HTTP side alone; its gRPC cluster is 2.0's.
		{name: "health path only, the gRPC cluster", proxyfile: healthGateway("q: {http: 3000, grpc: 3001, health: {path: /healthz}}"),
			methods: rpcMethod, at: []any{"static_resources", "clusters", 1}, want: `
name: q_grpc
type: STATIC
load_assignment:
  cluster_name: q_grpc
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 127.0.0.1, port_value: 3001}}}}]}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http2_protocol_options: {}}
`},
		{name: "tls without clients", proxyfile: tracingBase + "tls: {certificate: /c.pem, key: /k.pem}\n",
			at: []any{"static_resources", "listeners", 0, "filter_chains", 0, "transport_socket"}, want: `
name: envoy.transport_sockets.tls
typed_config:
  "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
  common_tls_context:
    alpn_protocols: [h2, http/1.1]
    tls_certificates: [{certificate_chain: {filename: /c.pem}, private_key: {filename: /k.pem}}]
`},
		{name: "tls behind a proxy", proxyfile: tracingBase + "tls: {certificate: /c.pem, key: /k.pem}\nclient_address: forwarded\n",
			at: slices.Concat(manager, []any{"use_remote_address"}), want: `false`},
		{name: "connection without tls", proxyfile: tracingBase + "client_address: connection\n",
			at: slices.Concat(manager, []any{"use_remote_address"}), want: `true`},
		// The limiter runs before the check, so a request over a limit never reaches it.
		{name: "rate limits with auth, its filters", proxyfile: limitedWithAuth, at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.set_metadata, envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors,
  envoy.filters.http.local_ratelimit, envoy.filters.http.ext_authz, envoy.filters.http.lua, envoy.filters.http.connect_grpc_bridge,
  envoy.filters.http.router]`},
		{name: "rate limits with auth, the namespace", proxyfile: limitedWithAuth, at: slices.Concat(limiter, []any{"rate_limits"}), want: `
- actions:
  - metadata: {descriptor_key: bucket, metadata_key: {key: edge.rate_limit, path: [{key: all}]}, source: ROUTE_ENTRY}
- actions:
  - metadata: {descriptor_key: bucket, metadata_key: {key: edge.rate_limit, path: [{key: client}]}, source: ROUTE_ENTRY}
  - remote_address: {}
`},
		// One bucket for each limited entry of routes, in the written order, whatever the number of environments.
		{name: "rate limits with auth, one bucket list", proxyfile: limitedWithAuth, at: slices.Concat(limiter, []any{"descriptors"}), want: `
- entries: [{key: bucket, value: rpc}]
  token_bucket: {max_tokens: 40, tokens_per_fill: 40, fill_interval: 1s}
- entries: [{key: bucket, value: login}, {key: remote_address}]
  token_bucket: {max_tokens: 5, tokens_per_fill: 5, fill_interval: 60s}
- entries: [{key: bucket, value: shop}, {key: remote_address}]
  token_bucket: {max_tokens: 150, tokens_per_fill: 100, fill_interval: 1s}
`},
		{name: "rate limits with auth, an RPC route", proxyfile: limitedWithAuth,
			at:   slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 1, "metadata"}),
			want: `{filter_metadata: {edge: {action: buy}, edge.rate_limit: {all: rpc}}}`},
		// A route with auth off carries its bucket alone.
		{name: "rate limits with auth, a route with auth off, second environment", proxyfile: limitedWithAuth,
			at:   slices.Concat(manager, []any{"route_config", "virtual_hosts", 1, "routes", 2, "metadata"}),
			want: `{filter_metadata: {edge.rate_limit: {client: login}}}`},
		{name: "rate limits with auth, a checked route, second environment", proxyfile: limitedWithAuth,
			at:   slices.Concat(manager, []any{"route_config", "virtual_hosts", 1, "routes", 3, "metadata"}),
			want: `{filter_metadata: {edge: {action: ""}, edge.rate_limit: {client: shop}}}`},
		// The first mapper that matches decides, so a check's 2xx other than 200 becomes 403 before the pass-through on
		// UAEX keeps a check's own refusal as the check wrote it (measured on Envoy 1.40.2: a check's 202 and 204 give
		// 403, a Connect call the check answers 429 keeps the check's body, a Connect call over the limit gets the JSON
		// error and the check is not asked).
		{name: "rate limits with auth, the mappers in order", proxyfile: limitedWithAuth,
			at: slices.Concat(manager, []any{"local_reply_config", "mappers"}), want: `
- filter: {response_flag_filter: {flags: [UF, UH, UC, URX]}}
  status_code: 502
  body: {inline_string: ""}
- filter:
    and_filter:
      filters:
      - status_code_filter:
          comparison: {op: EQ, value: {default_value: 403, runtime_key: gateway.local_reply.origin_refused_status}}
      - header_filter:
          header: {name: origin, present_match: true}
  body: {inline_string: "Invalid CORS request"}
- filter:
    and_filter:
      filters:
      - response_flag_filter: {flags: [UAEX]}
      - status_code_filter: {comparison: {op: GE, value: {default_value: 200}}}
      - status_code_filter: {comparison: {op: LE, value: {default_value: 299}}}
  status_code: 403
- filter: {response_flag_filter: {flags: [UAEX]}}
- filter:
    and_filter:
      filters:
      - status_code_filter:
          comparison: {op: EQ, value: {default_value: 429, runtime_key: gateway.local_reply.rate_limited_status}}
      - header_filter:
          header: {name: connect-protocol-version, present_match: true}
  body: {inline_string: '{"code":"resource_exhausted","message":"local_rate_limited"}'}
  body_format_override:
    text_format_source: {inline_string: "%LOCAL_REPLY_BODY%"}
    content_type: application/json
`},
		// The four headers forward-auth services read replace a client's copies (measured on Envoy 1.40.2).
		{name: "send_request, what the check is told", proxyfile: askWithRequest,
			at: slices.Concat(manager, []any{"http_filters", 4, "typed_config", "http_service", "authorization_request"}), want: `
headers_to_add: [{key: x-gateway-timeout-ms, value: "1000"}, {key: x-forwarded-uri, value: "%REQ(:PATH)%"},
  {key: x-forwarded-method, value: "%REQ(:METHOD)%"}, {key: x-forwarded-proto, value: "%REQ(:SCHEME)%"},
  {key: x-forwarded-host, value: "%REQ(:AUTHORITY)%"}]
`},
		{name: "send_request, the path normalised", proxyfile: askWithRequest, at: slices.Concat(manager, []any{"normalize_path"}), want: "true"},
		{name: "send_request, the slashes merged", proxyfile: askWithRequest, at: slices.Concat(manager, []any{"merge_slashes"}), want: "true"},
		{name: "send_request, escaped slashes unescaped", proxyfile: askWithRequest,
			at: slices.Concat(manager, []any{"path_with_escaped_slashes_action"}), want: "UNESCAPE_AND_FORWARD"},
		// A gateway with no auth normalises the path too, so the route and a limit read the path the service receives:
		// without these three settings /open/../private/report.txt took a route with prefix /open/ and auth off, and with
		// them it is answered 401 and reaches no service (measured on Envoy 1.40.2).
		{name: "no auth, the path normalised", proxyfile: tracingBase, at: manager,
			keys: []string{"normalize_path", "merge_slashes", "path_with_escaped_slashes_action"}, want: `
normalize_path: true
merge_slashes: true
path_with_escaped_slashes_action: UNESCAPE_AND_FORWARD
`},
		{name: "jwt keys by URL, optional", proxyfile: jwtByURL, at: slices.Concat(manager, []any{"http_filters", 3, "typed_config"}), want: `
"@type": type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication
providers:
  jwt:
    issuer: https://login.example.com
    require_expiration: true
    remote_jwks:
      http_uri: {uri: "https://login.example.com/.well-known/jwks.json", cluster: jwks, timeout: 2s}
      retry_policy: {num_retries: 1}
    forward: true
    from_headers: [{name: x-access-token}]
rules:
- match: {prefix: /}
  requires:
    requires_any:
      requirements: [{provider_name: jwt}, {allow_missing: {}}]
`},
		{name: "jwt keys by URL, the keys' cluster", proxyfile: jwtByURL, at: []any{"static_resources", "clusters", 1}, want: `
name: jwks
cluster_type:
  name: envoy.cluster.dns
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.clusters.dns.v3.DnsCluster
    dns_lookup_family: ALL
    typed_dns_resolver_config:
      name: envoy.network.dns_resolver.cares
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.network.dns_resolver.cares.v3.CaresDnsResolverConfig
        resolvers: [{socket_address: {address: 10.0.0.2, port_value: 53}}]
load_assignment:
  cluster_name: jwks
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: login.example.com, port_value: 443}}}}]}]
transport_socket:
  name: envoy.transport_sockets.tls
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
    sni: login.example.com
    common_tls_context:
      validation_context:
        trusted_ca: {filename: /etc/gateway/jwt/ca.pem}
        match_typed_subject_alt_names: [{san_type: DNS, matcher: {exact: login.example.com}}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http_protocol_options: {}}
`},
		// RFC 6066 section 3 allows no IP address as a server name, so an address gets no sni and an IP_ADDRESS match.
		{name: "jwt keys at an IP address", proxyfile: jwtAtAnAddress, at: []any{"static_resources", "clusters", 1}, want: `
name: jwks
type: STATIC
load_assignment:
  cluster_name: jwks
  endpoints: [{lb_endpoints: [{endpoint: {address: {socket_address: {address: 10.0.3.50, port_value: 8443}}}}]}]
transport_socket:
  name: envoy.transport_sockets.tls
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
    common_tls_context:
      validation_context:
        trusted_ca: {filename: /etc/gateway/jwt/ca.pem}
        match_typed_subject_alt_names: [{san_type: IP_ADDRESS, matcher: {exact: 10.0.3.50}}]
typed_extension_protocol_options:
  envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
    "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
    explicit_http_config: {http_protocol_options: {}}
`},
		// A request over a limit is refused before its token is verified, and a token is verified before the check is
		// asked (measured on Envoy 1.40.2).
		{name: "jwt with a limit and a check, its filters", proxyfile: limitedWithJWT, at: slices.Concat(manager, []any{"http_filters"}), names: true,
			want: `[envoy.filters.http.set_metadata, envoy.filters.http.header_mutation, envoy.filters.http.rbac, envoy.filters.http.cors,
  envoy.filters.http.local_ratelimit, envoy.filters.http.jwt_authn, envoy.filters.http.ext_authz, envoy.filters.http.lua,
  envoy.filters.http.connect_grpc_bridge, envoy.filters.http.router]`},
		{name: "jwt with a check, a route with auth off", proxyfile: limitedWithJWT,
			at: slices.Concat(manager, []any{"route_config", "virtual_hosts", 0, "routes", 2, "typed_per_filter_config"}), want: `
envoy.filters.http.jwt_authn:
  "@type": type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.PerRouteConfig
  disabled: true
envoy.filters.http.ext_authz:
  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute
  disabled: true
`},
	}
	defaultOrigins := string(testsupport.ReadFile(t, "testdata/origins.json"))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(dir, "g.yaml"), c.proxyfile)
			testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), cmp.Or(c.origins, defaultOrigins))
			req := Request{Proxyfile: filepath.Join(dir, "g.yaml"), Out: filepath.Join(dir, "envoy.yaml")}
			if c.methods != "" {
				req.RPCMethods = filepath.Join(dir, "methods.json")
				testsupport.WriteFile(t, req.RPCMethods, c.methods)
			}
			files, problems := render(req)
			if problems != nil {
				t.Fatalf("render problems = %q, want none", testsupport.ProblemStringsIn(dir, problems))
			}
			got := valueAt(decode(t, files[0].Bytes), c.at...)
			if c.names {
				got = names(got)
			}
			if c.keys != nil {
				got = only(got, c.keys)
			}
			if d := firstDifference(decode(t, []byte(c.want)), got, ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// The tracing example's connection manager holds the request id extension and the tracing tree written below by hand,
// the manifest's text byte for byte, and the early header removal in its order: remove_from_clients, the check's
// headers, Envoy's auth mark, then the two tracing removals.
func TestTracingExample(t *testing.T) {
	files, problems := render(requestFor(t, "tracing/yaml", "envoy.yaml"))
	if problems != nil {
		t.Fatalf("render problems = %q, want none", testsupport.ProblemStrings(problems))
	}
	manager := valueAt(decode(t, files[0].Bytes), "static_resources", "listeners", 0, "filter_chains", 0, "filters", 0, "typed_config")
	cases := []struct {
		at   []any
		want string
	}{
		{[]any{"request_id_extension"}, `
typed_config:
  "@type": type.googleapis.com/envoy.extensions.request_id.uuid.v3.UuidRequestIdConfig
  pack_trace_reason: false
`},
		{[]any{"tracing"}, `
max_path_tag_length: 0
custom_tags:
- tag: environment
  environment: {name: APP_ENVIRONMENT, default_value: local}
- tag: request_id
  request_header: {name: x-request-id}
provider:
  name: envoy.tracers.xray
  typed_config:
    "@type": type.googleapis.com/envoy.config.trace.v3.XRayConfig
    daemon_endpoint: {protocol: UDP, address: 127.0.0.1, port_value: 2000}
    segment_name: example-gateway
    sampling_rule_manifest:
      inline_string: |
        {"version": 2,
         "default": {"fixed_target": 1, "rate": 0.05},
         "rules": [{"host": "*", "http_method": "*", "url_path": "/live*", "fixed_target": 0, "rate": 0}]}
`},
		{[]any{"early_header_mutation_extensions", 0, "typed_config", "mutations"}, `
- remove_on_match: {key_matcher: {prefix: x-debug-}}
- remove: x-gateway-action
- remove: x-gateway-reports
- remove: x-user-id
- remove: x-envoy-auth-failure-mode-allowed
- remove: x-envoy-downstream-service-cluster
- remove: x-envoy-downstream-service-node
`},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.at[0]), func(t *testing.T) {
			if d := firstDifference(decode(t, []byte(c.want)), valueAt(manager, c.at...), ""); d != "" {
				t.Error(d)
			}
		})
	}
}

// names returns the name of each item of a list, or the value unchanged when it is not a list, so a difference
// names what was found.
func names(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	found := make([]any, len(list))
	for i, item := range list {
		found[i] = valueAt(item, "name")
	}
	return found
}

// only returns the entries of a mapping that keys names, or the value unchanged when it is not a mapping. A key the
// mapping lacks is left out, so a difference names it as absent.
func only(value any, keys []string) any {
	mapping, ok := value.(map[string]any)
	if !ok {
		return value
	}
	kept := map[string]any{}
	for _, key := range keys {
		if entry, found := mapping[key]; found {
			kept[key] = entry
		}
	}
	return kept
}

// valueAt follows keys of maps and indexes of lists down a decoded tree, and returns nil when a step is missing.
func valueAt(tree any, steps ...any) any {
	for _, step := range steps {
		switch key := step.(type) {
		case string:
			m, _ := tree.(map[string]any)
			tree = m[key]
		case int:
			list, _ := tree.([]any)
			if key >= len(list) {
				return nil
			}
			tree = list[key]
		}
	}
	return tree
}

// listAt is valueAt for a step that must end at a list, and ends the test when it does not.
func listAt(t *testing.T, tree any, steps ...any) []any {
	t.Helper()
	list, ok := valueAt(tree, steps...).([]any)
	if !ok {
		t.Fatalf("no list at %v", steps)
	}
	return list
}

// requestFor points a request at the example folder dir, <example>/<form>: at its proxyfile, with the methods file the
// form is rendered with, and with the RPC table, beside out, when the folder commits one. The two are given apart,
// because a form may write its methods in the proxyfile and still commit a table.
func requestFor(t *testing.T, dir, out string) Request {
	t.Helper()
	i := slices.IndexFunc(testsupport.ExampleForms, func(form testsupport.ExampleForm) bool { return form.Dir == dir })
	if i < 0 {
		t.Fatalf("testsupport.ExampleForms names no folder %s", dir)
	}
	form := testsupport.ExampleForms[i]
	req := Request{Proxyfile: filepath.Join(exampleDir(dir), form.File), Out: out}
	if form.Methods != "" {
		req.RPCMethods = filepath.Join(exampleDir(dir), form.Methods)
	}
	if form.Table {
		req.RPCTable = filepath.Join(filepath.Dir(out), "rpc-table.json")
	}
	return req
}

// declareNginx declares, for the test, an nginx that refuses no key and whose generator returns files, in place of the
// shipped one, which TargetNamed would otherwise find first, and puts both lists back when the test ends. Swapping the
// lists is safe only while no test of the package runs in parallel.
func declareNginx(t *testing.T, files []generator.File) {
	t.Helper()
	declared, generators := format.Targets, Targets
	t.Cleanup(func() { format.Targets, Targets = declared, generators })
	format.Targets = testsupport.ReplaceOrAppend(declared, format.Target{Name: "nginx", Title: "nginx", Keys: map[string]format.Support{}},
		func(target format.Target) bool { return target.Name == "nginx" })
	Targets = testsupport.ReplaceOrAppend(generators, Target{Name: "nginx", Config: "nginx.conf",
		Generate: func(generator.Input) ([]generator.File, error) { return files, nil }},
		func(target Target) bool { return target.Name == "nginx" })
}

// exampleDir is the path of the example folder dir, <example>/<form>, from this package's directory.
func exampleDir(dir string) string {
	return filepath.Join("..", "..", "examples", filepath.FromSlash(dir))
}

// render reads every input and returns every output: Read and Files in one step, as the command line runs them, but
// without the command line's rule that no output path names an input or another output.
func render(req Request) ([]File, []model.Problem) {
	run, problems := Read(req)
	if problems != nil {
		return nil, problems
	}
	return run.Files()
}

// outputs is the paths render must return for req, in its order.
func outputs(req Request) []string {
	if req.RPCTable == "" {
		return []string{req.Out}
	}
	return []string{req.Out, req.RPCTable}
}

func paths(files []File) []string {
	var list []string
	for _, file := range files {
		list = append(list, file.Path)
	}
	return list
}

// baseNames is the name of each file render returns, without its folder, so renders into two folders compare.
func baseNames(files []File) []string {
	var list []string
	for _, file := range files {
		list = append(list, filepath.Base(file.Path))
	}
	return list
}

func decode(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return value
}

// firstDifference compares two decoded trees and names the first place they differ, or returns "". Maps compare
// over the sorted union of their keys, lists item by item and then by length, and scalars with reflect.DeepEqual,
// so 0.90 equals 0.9 and the text "2000" differs from the number 2000.
func firstDifference(want, got any, path string) string {
	wantMap, wantIsMap := want.(map[string]any)
	gotMap, gotIsMap := got.(map[string]any)
	if wantIsMap && gotIsMap {
		keys := slices.Collect(maps.Keys(wantMap))
		for key := range gotMap {
			if _, both := wantMap[key]; !both {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			wantValue, inWant := wantMap[key]
			gotValue, inGot := gotMap[key]
			switch {
			case !inWant:
				return difference(join(path, key), absent, gotValue)
			case !inGot:
				return difference(join(path, key), wantValue, absent)
			}
			if d := firstDifference(wantValue, gotValue, join(path, key)); d != "" {
				return d
			}
		}
		return ""
	}
	wantList, wantIsList := want.([]any)
	gotList, gotIsList := got.([]any)
	if wantIsList && gotIsList {
		for i := range min(len(wantList), len(gotList)) {
			if d := firstDifference(wantList[i], gotList[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		if len(wantList) != len(gotList) {
			return fmt.Sprintf("first difference at %s: want %d items, got %d", path, len(wantList), len(gotList))
		}
		return ""
	}
	if !reflect.DeepEqual(want, got) {
		return difference(path, want, got)
	}
	return ""
}

// absent stands for a key one side does not have, and prints as <absent>.
var absent = absentValue{}

type absentValue struct{}

func (absentValue) String() string { return "<absent>" }

func difference(path string, want, got any) string {
	return fmt.Sprintf("first difference at %s: want %v, got %v", path, want, got)
}

// join writes a key after a path with a dot, or as ["key"] when the key holds a dot itself, so the path can be read
// back without guessing where a key ends.
func join(path, key string) string {
	if strings.Contains(key, ".") {
		return fmt.Sprintf("%s[%q]", path, key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}
