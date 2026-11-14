package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/render"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

const usageText = `usage: proxyfile render [<file>] -o <output> [--target envoy|nginx] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile check [<file>] -o <output> [--target envoy|nginx] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile schema
       proxyfile version
`

const examples = "../../examples"

const overwrite = "proxyfile render: an output path names an input or another output\n"

// originsFixture is the origins file that says what the minimal example writes inline, which the render tests keep for
// the file form.
const originsFixture = "../render/testdata/origins.json"

// TestRun runs the whole program in memory. In args, <ex> stands for the examples directory and <tmp> for the row's
// own temporary directory, which the outputs name as <tmp> again before they are compared.
func TestRun(t *testing.T) {
	// minimalConf is the minimal example written in HOCON, for the rows that find a default file in that form. It is
	// read here, before any row changes directory, because examples is a path relative to the package directory.
	minimalConf := string(testsupport.ReadFile(t, examples+"/minimal/hocon/Proxyfile"))
	minimalYAML := string(testsupport.ReadFile(t, examples+"/minimal/yaml/proxyfile.yaml"))
	// otherNames copies the minimal example and writes both its forms beside it under names the rule does not read.
	otherNames := func(t *testing.T, tmp string) {
		copyMinimal(t, tmp)
		testsupport.WriteFile(t, filepath.Join(tmp, "example.yaml"), minimalYAML)
		testsupport.WriteFile(t, filepath.Join(tmp, "example.conf"), minimalConf)
	}
	cases := []struct {
		name           string
		setup          func(t *testing.T, tmp string) // runs before Run when set
		chdir          bool                           // Run runs in <tmp>, so the row names nothing under <ex>
		args           string                         // split at spaces
		exit           int
		stdout, stderr string
		also           func(t *testing.T, tmp string) // checks the files after Run when set
	}{
		{name: "check full",
			args: "check <ex>/full/yaml/proxyfile.yaml -o <ex>/full/yaml/envoy.yaml --rpc-table <ex>/full/yaml/rpc-table.json"},
		{name: "file after flags", args: "check -o <ex>/minimal/yaml/envoy.yaml <ex>/minimal/yaml/proxyfile.yaml"},
		{name: "hand edit", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			path := filepath.Join(tmp, "yaml", "envoy.yaml")
			testsupport.WriteFile(t, path, string(testsupport.ReadFile(t, path))+"# edited\n")
		}, args: "check <tmp>/yaml/proxyfile.yaml -o <tmp>/yaml/envoy.yaml", exit: 1,
			stderr: "<tmp>/yaml/envoy.yaml differs from what proxyfile render writes\n"},
		{name: "missing output", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			if err := os.Remove(filepath.Join(tmp, "yaml", "envoy.yaml")); err != nil {
				t.Fatalf("Remove: %v", err)
			}
		}, args: "check <tmp>/yaml/proxyfile.yaml -o <tmp>/yaml/envoy.yaml", exit: 1,
			stderr: "<tmp>/yaml/envoy.yaml differs from what proxyfile render writes\n"},
		// A path that holds a control character is written quoted, as a problem's file is, so each message is one line.
		{name: "a checked path that holds a newline", setup: copyMinimal,
			args: "check <tmp>/yaml/proxyfile.yaml -o <tmp>/a\nb.yaml", exit: 1,
			stderr: "\"<tmp>/a\\nb.yaml\" differs from what proxyfile render writes\n"},
		{name: "a written path that holds a newline", setup: copyMinimal,
			args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/a\nb/out.yaml", exit: 1,
			stderr: "proxyfile render: open \"<tmp>/a\\nb/out.yaml\": no such file or directory\n"},
		{name: "a flag that holds a newline", args: "render -a\nb", exit: 2,
			stderr: "proxyfile render: flag provided but not defined: -a\\nb\n"},
		{name: "render", setup: copyMinimal, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml",
			also: func(t *testing.T, tmp string) {
				if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, "out.yaml")), testsupport.ReadFile(t, examples+"/minimal/yaml/envoy.yaml")) {
					t.Errorf("out.yaml differs from %s/minimal/yaml/envoy.yaml", examples)
				}
			}},
		{name: "problem writes nothing", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			path := filepath.Join(tmp, "yaml", "proxyfile.yaml")
			text := string(testsupport.ReadFile(t, path))
			if !strings.Contains(text, "listen: 8000\n") {
				t.Fatalf("the minimal example holds no line listen: 8000")
			}
			testsupport.WriteFile(t, path, strings.Replace(text, "listen: 8000\n", "liste: 8000\n", 1))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/yaml/proxyfile.yaml:3:1: \"listen\" is required here\n" +
				"<tmp>/yaml/proxyfile.yaml:3:1: unknown key \"liste\"; known here: admin, auth, client_address, cors, dns, envoy, haproxy, limits, listen, log, nginx, origins, remove_from_clients, routes, rpc, services, timeouts, tls, tracing\n",
			also: nothingWritten("minimal")},
		// No shipped target refuses a key, so these rows declare an Envoy that refuses some, and the refusal goes from
		// the --target flag through the render to the printed lines and the exit code.
		{name: "a key the target refuses", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			declare(t, envoyRefusing("listen"))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --target envoy", exit: 1,
			stderr: "<tmp>/yaml/proxyfile.yaml:3:1: \"listen\" is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("minimal")},
		// A key of the proxyfile written in the origins file or the methods file is refused there, at its own line, by
		// the path the proxyfile writes it at.
		{name: "a key the target refuses in the origins file", setup: func(t *testing.T, tmp string) {
			originsFileForm(t, tmp, "{\n"+
				"  \"cors\": {\"max_age\": \"600\"},\n"+
				"  \"environments\": [\n"+
				"    {\"name\": \"all\", \"hosts\": [\"localhost\"], \"origins\": [\"http://localhost:3000\"]}\n"+
				"  ]\n"+
				"}\n")
			declare(t, envoyRefusing("cors.max_age"))
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/origins.json:2:12: \"cors.max_age\" is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("minimal")},
		{name: "a key the target refuses in the methods file", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			declare(t, envoyRefusing("rpc.methods[].action"))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-methods <tmp>/methods.json", exit: 1,
			stderr: "<tmp>/methods.json:2:76: \"rpc.methods[].action\" is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("partial", "methods.json")},
		{name: "a key the target refuses in a .proto option", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			declare(t, envoyRefusing("rpc.methods[].action", "rpc.methods[].reports"))
		}, args: "render <tmp>/proto-methods/Proxyfile -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/proto/example/backend/v1/reports.proto:9:3: \"rpc.methods[].action\" is refused by --target envoy: refused by this test\n" +
				"<tmp>/proto/example/backend/v1/reports.proto:9:3: \"rpc.methods[].reports\" is refused by --target envoy: refused by this test\n",
			also: nothingWritten("partial")},
		{name: "the same keys in the proxyfile are refused once each", setup: func(t *testing.T, tmp string) {
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.yaml"), string(testsupport.ReadFile(t, examples+"/partial/yaml/proxyfile.yaml"))+
				"cors: {max_age: \"600\"}\n"+testsupport.PartialMethods)
			declare(t, envoyRefusing("cors.max_age", "rpc.methods[].action"))
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/proxyfile.yaml:28:8: \"cors.max_age\" is refused by --target envoy: refused by this test\n" +
				"<tmp>/proxyfile.yaml:31:72: \"rpc.methods[].action\" is refused by --target envoy: refused by this test\n",
			also: nothingWritten("partial")},
		// A form refused for a key of the proxyfile is told by the proxyfile's keys wherever the refused key is
		// written: in the origins file, the methods file or the .proto options as in the proxyfile.
		{name: "a form refused for a key of the proxyfile, in the methods file", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			declare(t, envoyRefusingWhen(map[string]string{"rpc.methods[].reports": "auth.ask"}))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-methods <tmp>/methods.json", exit: 1,
			stderr: "<tmp>/methods.json:2:94: \"rpc.methods[].reports\" with auth.ask is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("partial", "methods.json")},
		{name: "a form refused for a key of the proxyfile, in a .proto option", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			declare(t, envoyRefusingWhen(map[string]string{"rpc.methods[].reports": "auth.ask"}))
		}, args: "render <tmp>/proto-methods/Proxyfile -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/proto/example/backend/v1/reports.proto:9:3: \"rpc.methods[].reports\" with auth.ask is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("partial")},
		{name: "a form refused for a key of the proxyfile, in the origins file", setup: func(t *testing.T, tmp string) {
			originsFileForm(t, tmp, string(testsupport.ReadFile(t, originsFixture)))
			declare(t, envoyRefusingWhen(map[string]string{"origins.<environment name>.origins": "listen"}))
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/origins.json:3:45: \"origins.<environment name>.origins\" with listen is refused by --target envoy: refused by this test\n",
			also:   nothingWritten("minimal")},
		{name: "the same forms in the proxyfile are refused once each", setup: func(t *testing.T, tmp string) {
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.yaml"), string(testsupport.ReadFile(t, examples+"/partial/yaml/proxyfile.yaml"))+
				testsupport.PartialMethods)
			declare(t, envoyRefusingWhen(map[string]string{"rpc.methods[].reports": "auth.ask", "origins.<environment name>.origins": "listen"}))
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml", exit: 1,
			stderr: "<tmp>/proxyfile.yaml:19:40: \"origins.<environment name>.origins\" with listen is refused by --target envoy: refused by this test\n" +
				"<tmp>/proxyfile.yaml:30:86: \"rpc.methods[].reports\" with auth.ask is refused by --target envoy: refused by this test\n",
			also: nothingWritten("partial")},
		// The nginx generator writes proxyfile.js beside its configuration. These rows declare an nginx whose generator
		// writes two one-line files under the same names, so each row reads a short literal, and the script goes through
		// render, check and the output-path rule as the shipped one does.
		{name: "a script beside the configuration", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			declareScripted(t)
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --target nginx",
			also: func(t *testing.T, tmp string) {
				for name, want := range map[string]string{"out.conf": "config\n", "proxyfile.js": "script\n"} {
					if got := string(testsupport.ReadFile(t, filepath.Join(tmp, name))); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
			}},
		{name: "check finds a changed script", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			testsupport.WriteFile(t, filepath.Join(tmp, "out.conf"), "config\n")
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.js"), "changed\n")
			declareScripted(t)
		}, args: "check <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --target nginx", exit: 1,
			stderr: "<tmp>/proxyfile.js differs from what proxyfile render writes\n"},
		{name: "check finds a missing script", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			testsupport.WriteFile(t, filepath.Join(tmp, "out.conf"), "config\n")
			declareScripted(t)
		}, args: "check <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --target nginx", exit: 1,
			stderr: "<tmp>/proxyfile.js differs from what proxyfile render writes\n"},
		{name: "the script would overwrite the methods file", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.js"), string(testsupport.ReadFile(t, filepath.Join(tmp, "methods.json"))))
			declareScripted(t)
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --rpc-methods <tmp>/proxyfile.js --target nginx", exit: 2,
			stderr: overwrite,
			also: func(t *testing.T, tmp string) {
				absent(t, tmp, "out.conf")
				if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, "proxyfile.js")), testsupport.ReadFile(t, examples+"/partial/methods.json")) {
					t.Errorf("proxyfile.js changed")
				}
			}},
		{name: "the table names the script", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			declareScripted(t)
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --rpc-methods <tmp>/methods.json --rpc-table <tmp>/proxyfile.js --target nginx",
			exit: 2, stderr: overwrite,
			also: func(t *testing.T, tmp string) {
				nothingWritten("partial", "methods.json")(t, tmp)
				absent(t, tmp, "out.conf", "proxyfile.js")
			}},
		// A script that is a link to an input is that input, so writing the script would overwrite it.
		{name: "the script is a link to the methods file", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			link(t, "methods.json", filepath.Join(tmp, "proxyfile.js"))
			declareScripted(t)
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.conf --rpc-methods <tmp>/methods.json --target nginx", exit: 2,
			stderr: overwrite,
			also: func(t *testing.T, tmp string) {
				nothingWritten("partial", "methods.json")(t, tmp)
				absent(t, tmp, "out.conf")
			}},
		{name: "-o names the script", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			declareScripted(t)
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/proxyfile.js --target nginx", exit: 2, stderr: overwrite,
			also: func(t *testing.T, tmp string) { absent(t, tmp, "proxyfile.js") }},
		{name: "no arguments", exit: 2, stderr: usageText},
		{name: "help", args: "--help", stdout: usageText},
		{name: "unknown command", args: "rendr", exit: 2, stderr: "proxyfile: unknown command \"rendr\"\n" + usageText},
		{name: "no -o", args: "render a.yaml", exit: 2, stderr: "proxyfile render: -o is required\n"},
		{name: "two files", args: "render a.yaml b.yaml -o c.yaml", exit: 2, stderr: "proxyfile render: give exactly one file\n"},
		{name: "target envoy", args: "check <ex>/minimal/yaml/proxyfile.yaml -o <ex>/minimal/yaml/envoy.yaml --target envoy"},
		// haproxy has no generator in this version, so it is refused before any file is read, and a.yaml need not exist.
		{name: "target haproxy", args: "check a.yaml -o b.yaml --target haproxy", exit: 2,
			stderr: "proxyfile check: --target haproxy: no generator for haproxy in this version; see docs/portability.md\n"},
		// The targets with a generator are the ones render.Targets holds, the list render picks the generator from, so
		// a target it does not hold is refused before any file is read.
		{name: "a target with no generator", setup: func(t *testing.T, tmp string) {
			generators := render.Targets
			t.Cleanup(func() { render.Targets = generators })
			render.Targets = nil
		},
			args: "render a.yaml -o b.yaml --target envoy", exit: 2,
			stderr: "proxyfile render: --target envoy: no generator for envoy in this version; see docs/portability.md\n"},
		{name: "unknown target", args: "render a.yaml -o b.yaml --target Envoy", exit: 2,
			stderr: "proxyfile render: --target must be envoy, nginx or haproxy, not \"Envoy\"\n"},
		// With no file given, the file is the one default name in the working directory.
		{name: "default Proxyfile", setup: minimalWith(minimalConf, "Proxyfile"), chdir: true,
			args: "render -o <tmp>/out.yaml",
			also: func(t *testing.T, tmp string) {
				if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, "out.yaml")), testsupport.ReadFile(t, filepath.Join(tmp, "hocon", "envoy.yaml"))) {
					t.Errorf("out.yaml differs from hocon/envoy.yaml, the copy of %s/minimal/hocon/envoy.yaml", examples)
				}
			}},
		{name: "default proxyfile.yaml for check",
			setup: minimalWith(minimalYAML, "proxyfile.yaml"), chdir: true,
			args: "check -o <tmp>/yaml/envoy.yaml"},
		{name: "no default here", setup: otherNames, chdir: true, args: "render -o <tmp>/out.yaml", exit: 2,
			stderr: "proxyfile render: no file given and no Proxyfile, proxyfile.conf, proxyfile.yaml or proxyfile.yml here\n",
			also:   nothingWritten("minimal")},
		{name: "two defaults here", setup: minimalWith(minimalConf, "Proxyfile", "proxyfile.conf"), chdir: true,
			args: "render -o <tmp>/out.yaml", exit: 2,
			stderr: "proxyfile render: more than one default file here: Proxyfile, proxyfile.conf; give the file\n",
			also:   nothingWritten("minimal")},
		// Only a regular file counts as a default, so a directory of that name is passed over.
		{name: "default is a directory", chdir: true, args: "render -o <tmp>/out.yaml", exit: 2,
			setup: func(t *testing.T, tmp string) {
				otherNames(t, tmp)
				if err := os.Mkdir(filepath.Join(tmp, "Proxyfile"), 0o755); err != nil {
					t.Fatalf("Mkdir: %v", err)
				}
			},
			stderr: "proxyfile render: no file given and no Proxyfile, proxyfile.conf, proxyfile.yaml or proxyfile.yml here\n",
			also:   nothingWritten("minimal")},
		// The default file is an input like a named one, so the output-path rule keeps render from overwriting it.
		{name: "default named as the output", setup: minimalWith(minimalConf, "Proxyfile"), chdir: true,
			args: "render -o Proxyfile", exit: 2, stderr: overwrite,
			also: func(t *testing.T, tmp string) {
				if got := string(testsupport.ReadFile(t, filepath.Join(tmp, "Proxyfile"))); got != minimalConf {
					t.Errorf("Proxyfile = %q, want %q", got, minimalConf)
				}
			}},
		{name: "output names the proxyfile by a relative path", setup: copyMinimal, chdir: true,
			args: "render <tmp>/yaml/proxyfile.yaml -o yaml/proxyfile.yaml", exit: 2, stderr: overwrite,
			also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		// The methods may be written in the proxyfile, so the table's need for them is known only after the read.
		{name: "table without methods", setup: copyMinimal,
			args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-table <tmp>/t.json", exit: 2,
			stderr: "proxyfile render: --rpc-table needs rpc.methods, rpc.proto or --rpc-methods\n",
			also: func(t *testing.T, tmp string) {
				nothingWritten("minimal")(t, tmp)
				absent(t, tmp, "t.json")
			}},
		{name: "table from rpc.methods", setup: func(t *testing.T, tmp string) {
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.yaml"),
				string(testsupport.ReadFile(t, examples+"/partial/yaml/proxyfile.yaml"))+testsupport.PartialMethods)
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml --rpc-table <tmp>/t.json",
			also: func(t *testing.T, tmp string) {
				if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, "t.json")), testsupport.ReadFile(t, examples+"/partial/yaml/rpc-table.json")) {
					t.Errorf("t.json differs from %s/partial/yaml/rpc-table.json", examples)
				}
			}},
		// An empty list is a list given, as an empty methods file is, so the table is written and holds no route.
		{name: "table from an empty rpc.methods", setup: func(t *testing.T, tmp string) {
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.yaml"),
				string(testsupport.ReadFile(t, examples+"/partial/yaml/proxyfile.yaml"))+"rpc:\n  methods: []\n")
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/out.yaml --rpc-table <tmp>/t.json",
			also: func(t *testing.T, tmp string) {
				if got := string(testsupport.ReadFile(t, filepath.Join(tmp, "t.json"))); got != "[]\n" {
					t.Errorf("t.json = %q, want %q", got, "[]\n")
				}
			}},
		// The .proto files rpc.proto reads give the methods as well, and check finds the committed table current.
		{name: "table from rpc.proto",
			args: "check <ex>/partial/proto-methods/Proxyfile -o <ex>/partial/proto-methods/envoy.yaml " +
				"--rpc-table <ex>/partial/proto-methods/rpc-table.json"},
		{name: "output names an input", setup: copyMinimal, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/yaml/proxyfile.yaml",
			exit: 2, stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		{name: "output names an input, another spelling", setup: copyMinimal,
			args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/yaml/./proxyfile.yaml", exit: 2,
			stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		// An output that is there already is compared with each input and output by the file it is, so a link, a hard
		// link, a linked folder and another letter case on a disk that does not tell cases apart reach the proxyfile.
		{name: "output is a link to the proxyfile", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			link(t, "yaml/proxyfile.yaml", filepath.Join(tmp, "link.yaml"))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/link.yaml", exit: 2,
			stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		{name: "output names the proxyfile through a linked folder", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			link(t, "yaml", filepath.Join(tmp, "linked"))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/linked/proxyfile.yaml", exit: 2,
			stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		{name: "output is a hard link to the proxyfile", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			if err := os.Link(filepath.Join(tmp, "yaml", "proxyfile.yaml"), filepath.Join(tmp, "hard.yaml")); err != nil {
				t.Fatalf("Link: %v", err)
			}
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/hard.yaml", exit: 2,
			stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		{name: "output names the proxyfile in another letter case", setup: func(t *testing.T, tmp string) {
			copyMinimal(t, tmp)
			if _, err := os.Stat(filepath.Join(tmp, "yaml", "PROXYFILE.YAML")); err != nil {
				t.Skipf("the file system of %s tells letter cases apart, so PROXYFILE.YAML is another file there", tmp)
			}
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/yaml/PROXYFILE.YAML", exit: 2,
			stderr: overwrite, also: nothingWritten("minimal", "yaml/proxyfile.yaml")},
		// Two outputs that are not there yet are compared by their folders, each link in them resolved, and their
		// names, so the table would not replace the configuration through a folder linked to the folder of -o.
		{name: "table names the output through a linked folder", setup: func(t *testing.T, tmp string) {
			copyExample(t, tmp, "partial")
			link(t, ".", filepath.Join(tmp, "linked"))
		}, args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-methods <tmp>/methods.json --rpc-table <tmp>/linked/out.yaml",
			exit: 2, stderr: overwrite, also: nothingWritten("partial", "methods.json")},
		{name: "table names the methods file", setup: func(t *testing.T, tmp string) { copyExample(t, tmp, "partial") },
			args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-methods <tmp>/methods.json --rpc-table <tmp>/methods.json",
			exit: 2, stderr: overwrite, also: nothingWritten("partial", "methods.json")},
		// The origins file is named inside the proxyfile, not on the command line. The examples write their origins
		// inline, so the row writes the minimal example in the file form beside the fixture it names.
		{name: "output names the origins file", setup: func(t *testing.T, tmp string) {
			originsFileForm(t, tmp, string(testsupport.ReadFile(t, originsFixture)))
		}, args: "render <tmp>/proxyfile.yaml -o <tmp>/origins.json", exit: 2, stderr: overwrite,
			also: func(t *testing.T, tmp string) {
				nothingWritten("minimal")(t, tmp)
				if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, "origins.json")), testsupport.ReadFile(t, originsFixture)) {
					t.Errorf("origins.json changed")
				}
			}},
		// A file the proxyfile includes is an input too, though no flag and no key names it.
		{name: "output names an included file", setup: func(t *testing.T, tmp string) { copyExample(t, tmp, "partial") },
			args: "render <tmp>/hocon/Proxyfile -o <tmp>/rpc-methods.conf", exit: 2, stderr: overwrite,
			also: nothingWritten("partial", "rpc-methods.conf")},
		{name: "table names an included file", setup: func(t *testing.T, tmp string) { copyExample(t, tmp, "partial") },
			args: "render <tmp>/hocon/Proxyfile -o <tmp>/out.yaml --rpc-table <tmp>/rpc-methods.conf", exit: 2, stderr: overwrite,
			also: nothingWritten("partial", "rpc-methods.conf")},
		{name: "output names a .proto file", setup: func(t *testing.T, tmp string) { copyExample(t, tmp, "partial") },
			args: "render <tmp>/proto-methods/Proxyfile -o <tmp>/proto/example/backend/v1/reports.proto", exit: 2, stderr: overwrite,
			also: nothingWritten("partial", "proto/example/backend/v1/reports.proto")},
		{name: "version", args: "version", stdout: "proxyfile v9.9.9\n"},
		{name: "schema", args: "schema", stdout: string(testsupport.ReadFile(t, "../../schema/proxyfile.schema.json"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			if c.setup != nil {
				c.setup(t, tmp)
			}
			if c.chdir {
				t.Chdir(tmp)
			}
			var args []string
			if c.args != "" {
				args = strings.Split(strings.NewReplacer("<ex>", examples, "<tmp>", tmp).Replace(c.args), " ")
			}
			var stdout, stderr bytes.Buffer
			exit := Run(args, &stdout, &stderr, "v9.9.9")
			if exit != c.exit {
				t.Errorf("exit = %d, want %d", exit, c.exit)
			}
			if got := strings.ReplaceAll(stdout.String(), tmp, "<tmp>"); got != c.stdout {
				t.Errorf("stdout = %q, want %q", got, c.stdout)
			}
			if got := strings.ReplaceAll(stderr.String(), tmp, "<tmp>"); got != c.stderr {
				t.Errorf("stderr = %q, want %q", got, c.stderr)
			}
			if c.also != nil {
				c.also(t, tmp)
			}
		})
	}
}

// Every example folder holds its proxyfile under a default name, so check run there with no file reads it and finds
// the committed files current for each target that renders it, as a user who copies an example and runs the commands
// with no file would. A form a target refuses commits <target>.refused instead, whose lines TestExamplesAreCurrent
// compares.
func TestCheckEveryExampleWithNoFile(t *testing.T) {
	for _, target := range render.Targets {
		for _, form := range testsupport.ExampleForms {
			// examples is relative to the package directory, so the folder is made absolute before the row moves into it.
			dir, err := filepath.Abs(filepath.Join(examples, form.Dir))
			if err != nil {
				t.Fatalf("Abs: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, target.Name+".refused")); err == nil {
				continue
			}
			t.Run(target.Name+"/"+form.Dir, func(t *testing.T) {
				args := []string{"check", "-o", target.Config, "--target", target.Name}
				if form.Methods != "" {
					args = append(args, "--rpc-methods", form.Methods)
				}
				if form.Table {
					args = append(args, "--rpc-table", "rpc-table.json")
				}
				t.Chdir(dir)
				var stdout, stderr bytes.Buffer
				if exit := Run(args, &stdout, &stderr, "v9.9.9"); exit != 0 {
					t.Errorf("exit = %d, want 0", exit)
				}
				if got := stdout.String(); got != "" {
					t.Errorf("stdout = %q, want %q", got, "")
				}
				if got := stderr.String(); got != "" {
					t.Errorf("stderr = %q, want %q", got, "")
				}
			})
		}
	}
}

// originsFileForm writes into dir the minimal example in the file form, which no example writes: proxyfile.yaml,
// whose origins name origins.json, and origins.json holding origins.
func originsFileForm(t *testing.T, dir, origins string) {
	t.Helper()
	text := string(testsupport.ReadFile(t, examples+"/minimal/yaml/proxyfile.yaml"))
	if !strings.Contains(text, testsupport.MinimalInlineOrigins) {
		t.Fatalf("the minimal example holds no %q to replace", testsupport.MinimalInlineOrigins)
	}
	testsupport.WriteFile(t, filepath.Join(dir, "proxyfile.yaml"),
		strings.Replace(text, testsupport.MinimalInlineOrigins, "origins: origins.json\n", 1))
	testsupport.WriteFile(t, filepath.Join(dir, "origins.json"), origins)
}

// declare makes targets the declared targets for the row and puts the declared list back when the row ends. Swapping
// format.Targets is safe only while no test of the package runs in parallel.
func declare(t *testing.T, targets ...format.Target) {
	t.Helper()
	declared := format.Targets
	t.Cleanup(func() { format.Targets = declared })
	format.Targets = targets
}

// declareScripted declares, for the row, an nginx that refuses no key and whose generator writes the configuration
// "config\n" and the script proxyfile.js holding "script\n", in place of the shipped nginx, which a lookup by name
// would otherwise find first, and puts both lists back when the row ends.
func declareScripted(t *testing.T) {
	t.Helper()
	declare(t, testsupport.ReplaceOrAppend(format.Targets, format.Target{Name: "nginx", Title: "nginx", Keys: map[string]format.Support{}},
		func(target format.Target) bool { return target.Name == "nginx" })...)
	generators := render.Targets
	t.Cleanup(func() { render.Targets = generators })
	render.Targets = testsupport.ReplaceOrAppend(render.Targets, render.Target{Name: "nginx", Config: "nginx.conf",
		Generate: func(generator.Input) ([]generator.File, error) {
			return []generator.File{{Bytes: []byte("config\n")}, {Name: "proxyfile.js", Bytes: []byte("script\n")}}, nil
		}}, func(target render.Target) bool { return target.Name == "nginx" })
}

// envoyRefusing is an Envoy that refuses keys, each with the note "refused by this test", and declares no other key.
func envoyRefusing(keys ...string) format.Target {
	refused := map[string]format.Support{}
	for _, key := range keys {
		refused[key] = format.Support{Level: format.Refused, Note: "refused by this test"}
	}
	return format.Target{Name: "envoy", Title: "Envoy", Keys: refused}
}

// envoyRefusingWhen is an Envoy that declares each key of forms partial and refuses the key when the proxyfile writes
// the key forms maps it to, a path of keys joined by dots, in the form "with <that key>" and with the words "refused by
// this test". It declares no other key.
func envoyRefusingWhen(forms map[string]string) format.Target {
	declared := map[string]format.Support{}
	for key, other := range forms {
		declared[key] = format.Support{Level: format.Partial, Note: "partial in this test", Refuses: []format.Refusal{{
			Form: "with " + other, Instead: "refused by this test",
			Applies: func(_, proxyfile *tree.Node) bool {
				for _, step := range strings.Split(other, ".") {
					proxyfile = proxyfile.Get(step)
				}
				return proxyfile != nil
			}}}}
	}
	return format.Target{Name: "envoy", Title: "Envoy", Keys: declared}
}

// link makes a symbolic link at path holding target, which is read from the link's own folder.
func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
}

// copyExample copies examples/<name> into dir, its directories included.
func copyExample(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join(examples, name))); err != nil {
		t.Fatalf("CopyFS: %v", err)
	}
}

func copyMinimal(t *testing.T, dir string) {
	t.Helper()
	copyExample(t, dir, "minimal")
}

// minimalWith returns a setup that copies the minimal example and writes text into each named file beside it.
func minimalWith(text string, names ...string) func(t *testing.T, tmp string) {
	return func(t *testing.T, tmp string) {
		copyMinimal(t, tmp)
		for _, name := range names {
			testsupport.WriteFile(t, filepath.Join(tmp, name), text)
		}
	}
}

// nothingWritten returns a check that out.yaml was not written and that each named file of the row's directory still
// holds the bytes of the same file in examples/<example>.
func nothingWritten(example string, names ...string) func(t *testing.T, tmp string) {
	// examples is relative to the package directory, so the example is found here, when the table is built, and not in
	// the check, which a row that changes directory runs from its own directory.
	dir, absErr := filepath.Abs(filepath.Join(examples, example))
	return func(t *testing.T, tmp string) {
		if absErr != nil {
			t.Fatalf("Abs: %v", absErr)
		}
		absent(t, tmp, "out.yaml")
		for _, name := range names {
			if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, name)), testsupport.ReadFile(t, filepath.Join(dir, name))) {
				t.Errorf("%s changed", name)
			}
		}
	}
}

// absent checks that the row's directory holds no file of each name.
func absent(t *testing.T, tmp string, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(tmp, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: Stat error = %v, want that it does not exist", name, err)
		}
	}
}

// A value whose nginx form is longer than nginx reads from its file is refused by its key, as a key nginx cannot carry
// is, with exit 1 and no file written: nginx reads no value longer than 4094 bytes between its quotes, or 4095 bytes
// without them (measured on nginx 1.30.6 and 1.31.8), and the form grows by key, by 12 bytes for each $ of a rewrite,
// which nginx writes as a variable, and by 3 for each dot of a host in its origin check. A service's and an
// environment's name of 5000 characters is written in HOCON, which takes a key that long.
func TestLongValuesForNginx(t *testing.T) {
	const words = " whose nginx form is longer than nginx reads is refused by --target nginx: nginx reads no value longer than " +
		"4094 bytes between its quotes, or 4095 bytes without them; write a value whose nginx form is at most 4094 bytes, " +
		"or render it for envoy\n"
	host := strings.Repeat("a", 4795) + ".example"
	name := strings.Repeat("s", 5000)
	// yaml is a proxyfile whose service line, environment line and route are the ones given, on lines 3, 5 and 7.
	yaml := func(service, environment, route string) string {
		return "listen: 8000\nservices:\n  " + service + "\norigins:\n  " + environment + "\nroutes:\n  - " + route + "\n"
	}
	const app, local = "app: {http: 3000}", "local: {hosts: [localhost]}"
	cases := []struct {
		name, file, proxyfile, stderr string
	}{
		{"a prefix", "p.yaml", yaml(app, local, "{name: app, to: app, prefix: /"+strings.Repeat("a", 5000)+"/}"),
			`<tmp>/p.yaml:7:34: "routes[].prefix"` + words},
		{"a path", "p.yaml", yaml(app, local, "{name: app, to: app, path: /"+strings.Repeat("a", 5000)+"}"),
			`<tmp>/p.yaml:7:32: "routes[].path"` + words},
		{"a rewrite", "p.yaml", yaml(app, local, "{name: app, to: app, prefix: /, rewrite: /"+strings.Repeat("a", 5000)+"/}"),
			`<tmp>/p.yaml:7:46: "routes[].rewrite"` + words},
		{"a rewrite of 400 $", "p.yaml", yaml(app, local, "{name: app, to: app, prefix: /, rewrite: /"+strings.Repeat("$", 400)+"/}"),
			`<tmp>/p.yaml:7:46: "routes[].rewrite"` + words},
		// A pattern's written form grows by the class each \b is written as.
		{"a pattern", "p.yaml", yaml(app, local, "{name: app, to: app, pattern: '^/"+strings.Repeat(`\b`, 60)+"'}"),
			`<tmp>/p.yaml:7:35: "routes[].pattern"` + words},
		{"a host of an environment", "p.yaml", yaml(app, "local: {hosts: ["+host+"]}", "{name: app, to: app, prefix: /}"),
			`<tmp>/p.yaml:5:3: "origins.<environment name>.hosts"` + words},
		{"a host_header", "p.yaml", yaml("app: {http: 3000, host_header: "+host+"}", local, "{name: app, to: app, prefix: /}"),
			`<tmp>/p.yaml:3:3: "services.<service name>.host_header"` + words},
		{"a service's name", "Proxyfile",
			"listen = 8000\nservices { " + name + " { http = 3000 } }\norigins { local { hosts = [localhost] } }\n" +
				"routes = [ { name = app, prefix = \"/\", to = " + name + " } ]\n",
			"<tmp>/Proxyfile: services." + name + `: "services.<service name>"` + words},
		{"an environment's name", "Proxyfile",
			"listen = 8000\nservices { app { http = 3000 } }\norigins { " + name + " { hosts = [localhost] } }\n" +
				"routes = [ { name = app, prefix = \"/\", to = app } ]\n",
			"<tmp>/Proxyfile: origins." + name + `: "origins.<environment name>"` + words},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			testsupport.WriteFile(t, filepath.Join(tmp, c.file), c.proxyfile)
			var stdout, stderr bytes.Buffer
			exit := Run([]string{"render", filepath.Join(tmp, c.file), "-o", filepath.Join(tmp, "nginx.conf"), "--target", "nginx"},
				&stdout, &stderr, "v9.9.9")
			if exit != 1 {
				t.Errorf("exit = %d, want 1", exit)
			}
			if got := strings.ReplaceAll(stderr.String(), tmp, "<tmp>"); got != c.stderr {
				t.Errorf("stderr = %.400q, want %.400q", got, c.stderr)
			}
			absent(t, tmp, "nginx.conf", "proxyfile.js")
		})
	}
}
