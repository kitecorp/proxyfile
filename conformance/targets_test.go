// Package conformance runs a proxy among stub backends on what proxyfile renders for it from each proxyfile an entry
// of targets names (for Envoy, examples/full, examples/portable and each other target's own files in testdata), and
// checks that it answers every row as the Proxyfile says. Its files are test files only, so the released program
// never carries them, and only battery_test.go, built with the tag conformance, starts containers.
package conformance

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// target is one proxy the battery runs the scenarios against. Its rendered file is mounted under the name
// render.Targets gives the target's configuration, and its arguments take the name from there, so it is written once.
type target struct {
	name  string   // the --target the files are rendered with, a name in format.Targets and render.Targets
	label string   // the entry's subtest name: the proxy and its version line, such as envoy-1.40
	files []file   // the proxyfiles it runs, in order
	image string   // the proxy's image, pinned by digest
	args  []string // the proxy's arguments after the image
	ready string   // fetched inside the proxy's network namespace until it answers 200
	// mark is the header the proxy adds to a request the check could not answer, which when_unavailable = allow lets
	// through, so the service can tell it went unchecked. The battery forges the entry's own mark and reads it, so a
	// proxy that writes another name is held to removing its own.
	mark string
	// version is the proxy's major.minor, such as 1.40. One version can have entries on more than one image, such as
	// nginx's plain images and those tagged -otel, each running the files its image loads, so the coverage checks judge
	// the entries of one name and version together (versions).
	version string
}

// file is one proxyfile an entry runs: label names it in the subtests and in an exchange's on, and proxyfile is its
// path from this package's folder. collector starts the stub's collector at the address the file sends its spans to,
// so the rows read them (startWorld); every other file's collector is left unreachable. scenarios, when set, names
// the only scenarios whose rows run on the file, for a file that is there for those rows alone.
type file struct {
	label, proxyfile string
	collector        bool
	scenarios        []string
}

// tracingFile is the battery's file with tracing, which Envoy's entry and each nginx entry on an image tagged -otel
// run, and tracingByNameFile the same tracing with the collector found by name: nginx writes a relay for a name, and
// asks an IP address directly, so each form runs on every entry that traces. The second runs the tracing rows alone, as
// the first runs every other row on the same proxy.
var (
	tracingFile       = file{label: "nginx-tracing", proxyfile: "testdata/nginx-tracing.yaml", collector: true}
	tracingByNameFile = file{label: "nginx-tracing-by-name", proxyfile: "testdata/nginx-tracing-by-name.yaml", collector: true,
		scenarios: []string{tracing.name}}
)

// targets returns every proxy the battery runs. Envoy's image is read from the file scripts/validate-envoy.sh reads,
// so an Envoy upgrade changes one line for both. Its admin listener answers /ready on 127.0.0.1 only, which is why
// the battery fetches it from inside the proxy's network namespace. Every other proxy's entries come from a file of
// their own (nginxEntries, haproxyEntries), so a proxy added later adds its file and one call on the return line.
// Envoy runs each other target's own file too, so each literal on it is Envoy's measured answer, as rowProblems
// requires of every row.
func targets(t testing.TB) []target {
	pin, err := os.ReadFile("../scripts/envoy-image")
	if err != nil {
		t.Fatalf("reading the Envoy image: %v", err)
	}
	// An empty file would fail later, in docker run, with Docker's words about an image reference, and a name with no
	// digest would run whatever its tag pointed to that day.
	image := strings.TrimSpace(string(pin))
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("../scripts/envoy-image holds %q, not the Envoy image pinned by digest (<image>@sha256:<digest>)", image)
	}
	envoy, _ := render.TargetNamed("envoy")
	envoyEntry := target{
		name:    "envoy",
		label:   "envoy-1.40",
		version: "1.40",
		files: []file{
			{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
			{label: "portable", proxyfile: "../examples/portable/hocon/Proxyfile"},
			{label: "nginx-file", proxyfile: "testdata/nginx.yaml"},
			{label: "nginx-plain", proxyfile: "testdata/nginx-plain.yaml"},
			{label: "haproxy-file", proxyfile: "testdata/haproxy.yaml"},
			{label: "two-checks", proxyfile: "testdata/two-checks.yaml"},
			{label: "nginx-checks", proxyfile: "testdata/nginx-checks.yaml"},
			{label: "nginx-auth", proxyfile: "testdata/nginx-auth.yaml"},
			tracingFile,
			tracingByNameFile,
		},
		image: image,
		// Envoy writes its log lines to stdout every 10 s by default; every 100 ms, a row that reads them (answer.logged)
		// finds them at once. Only the battery's own run of Envoy takes the flag, so no rendered file changes.
		args:  []string{"-c", "/config/" + envoy.Config, "--file-flush-interval-msec", "100"},
		ready: "http://127.0.0.1:9901/ready",
		// The header docs/auth.md names for when_unavailable: allow, whatever the proxy, so a service reads one name.
		mark: "x-envoy-auth-failure-mode-allowed",
	}
	return slices.Concat([]target{envoyEntry}, nginxEntries(t), haproxyEntries(t))
}

// pinnedImage is a line of scripts/<proxy>-images: an image tagged with its version, with -otel after it in a file
// named <proxy>-otel-images, which pins nginx's images that carry the tracing module, and in no other file, and pinned
// by digest. The validators read the same files with the same rule, in scripts/pinned-images.sh.
var pinnedImage = regexp.MustCompile(`^[^\s@]+:([0-9]+)\.([0-9]+)\.[0-9]+(-otel)?@sha256:[0-9a-f]{64}$`)

// pin is one line of scripts/<proxy>-images: the image, the label of the entry that runs it and the image's
// major.minor.
type pin struct{ image, label, version string }

// pinnedImages returns the images scripts/<proxy>-images pins, one a line, in the file's order, each labelled
// <proxy>-<major>.<minor>, so a row one version answers differently fails in that entry alone. A missing or empty file,
// or a line that is not an image tagged with its version and pinned by digest, or tagged -otel in a file other than
// <proxy>-otel-images or without it in that file, fails the run with the file's name: a name with no digest would run
// whatever its tag pointed to that day, and an image in the other file would run one entry's rows on the other's
// image. The message names the image form as scripts/pinned-images.sh does.
func pinnedImages(t testing.TB, proxy string) []pin {
	path := "../scripts/" + proxy + "-images"
	lines, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the %s images: %v", proxy, err)
	}
	var pins []pin
	for _, image := range strings.Split(strings.TrimSuffix(string(lines), "\n"), "\n") {
		version := pinnedImage.FindStringSubmatch(image)
		repository, otel := strings.CutSuffix(proxy, "-otel")
		if version == nil || (version[3] == "-otel") != otel {
			form := repository + ":<major>.<minor>.<patch>@sha256:<digest>"
			if otel {
				form = repository + ":<major>.<minor>.<patch>-otel@sha256:<digest>"
			}
			t.Fatalf("%s holds %q, not one %s image a line, tagged with its version and pinned by digest (%s)",
				path, image, repository, form)
		}
		majorMinor := version[1] + "." + version[2]
		pins = append(pins, pin{image: image, label: proxy + "-" + majorMinor, version: majorMinor})
	}
	return pins
}
