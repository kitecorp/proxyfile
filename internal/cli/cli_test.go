package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

const usageText = `usage: proxyfile render [<file>] -o <output> [--target envoy] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile check [<file>] -o <output> [--target envoy] [--rpc-methods <methods.json>] [--rpc-table <table.json>]
       proxyfile schema
       proxyfile version
`

const examples = "../../examples"

const overwrite = "proxyfile render: an output path names an input or the other output\n"

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
				"<tmp>/yaml/proxyfile.yaml:3:1: unknown key \"liste\"; known here: admin, auth, cors, envoy, haproxy, limits, listen, log, nginx, origins, remove_from_clients, routes, rpc, services, timeouts\n",
			also: nothingWritten("minimal")},
		{name: "no arguments", exit: 2, stderr: usageText},
		{name: "help", args: "--help", stdout: usageText},
		{name: "unknown command", args: "rendr", exit: 2, stderr: "proxyfile: unknown command \"rendr\"\n" + usageText},
		{name: "no -o", args: "render a.yaml", exit: 2, stderr: "proxyfile render: -o is required\n"},
		{name: "two files", args: "render a.yaml b.yaml -o c.yaml", exit: 2, stderr: "proxyfile render: give exactly one file\n"},
		// envoy is the one target with a generator. The others are refused before any file is read, so a.yaml need not exist.
		{name: "target envoy", args: "check <ex>/minimal/yaml/proxyfile.yaml -o <ex>/minimal/yaml/envoy.yaml --target envoy"},
		{name: "target nginx", args: "render a.yaml -o b.yaml --target nginx", exit: 2,
			stderr: "proxyfile render: --target nginx: no generator for nginx in this version; see docs/portability.md\n"},
		{name: "target haproxy", args: "check a.yaml -o b.yaml --target haproxy", exit: 2,
			stderr: "proxyfile check: --target haproxy: no generator for haproxy in this version; see docs/portability.md\n"},
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
				if _, err := os.Stat(filepath.Join(tmp, "t.json")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("t.json: Stat error = %v, want that it does not exist", err)
				}
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
		{name: "table names the methods file", setup: func(t *testing.T, tmp string) { copyExample(t, tmp, "partial") },
			args: "render <tmp>/yaml/proxyfile.yaml -o <tmp>/out.yaml --rpc-methods <tmp>/methods.json --rpc-table <tmp>/methods.json",
			exit: 2, stderr: overwrite, also: nothingWritten("partial", "methods.json")},
		// The origins file is named inside the proxyfile, not on the command line. The examples write their origins
		// inline, so the row writes the minimal example in the file form beside the fixture it names.
		{name: "output names the origins file", setup: func(t *testing.T, tmp string) {
			text := string(testsupport.ReadFile(t, examples+"/minimal/yaml/proxyfile.yaml"))
			if !strings.Contains(text, testsupport.MinimalInlineOrigins) {
				t.Fatalf("the minimal example holds no %q to replace", testsupport.MinimalInlineOrigins)
			}
			testsupport.WriteFile(t, filepath.Join(tmp, "proxyfile.yaml"),
				strings.Replace(text, testsupport.MinimalInlineOrigins, "origins: origins.json\n", 1))
			testsupport.WriteFile(t, filepath.Join(tmp, "origins.json"), string(testsupport.ReadFile(t, originsFixture)))
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
// the committed files current, as a user who copies an example and runs the commands with no file would.
func TestCheckEveryExampleWithNoFile(t *testing.T) {
	for _, form := range testsupport.ExampleForms {
		t.Run(form.Dir, func(t *testing.T) {
			// examples is relative to the package directory, so the folder is made absolute before the row moves into it.
			dir, err := filepath.Abs(filepath.Join(examples, form.Dir))
			if err != nil {
				t.Fatalf("Abs: %v", err)
			}
			args := []string{"check", "-o", "envoy.yaml"}
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
		if _, err := os.Stat(filepath.Join(tmp, "out.yaml")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("out.yaml: Stat error = %v, want that it does not exist", err)
		}
		for _, name := range names {
			if !bytes.Equal(testsupport.ReadFile(t, filepath.Join(tmp, name)), testsupport.ReadFile(t, filepath.Join(dir, name))) {
				t.Errorf("%s changed", name)
			}
		}
	}
}
