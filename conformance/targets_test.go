// Package conformance runs a proxy among stub backends on what proxyfile renders for it from each example an entry
// of targets names (examples/full and examples/portable for Envoy), and checks that it answers every row as the
// Proxyfile says. Its files are test files only, so the released program never carries them, and only
// battery_test.go, built with the tag conformance, starts containers.
package conformance

import (
	"os"
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
}

// file is one proxyfile an entry runs: label names it in the subtests and in an exchange's on, and proxyfile is its
// path from this package's folder.
type file struct{ label, proxyfile string }

// targets returns every proxy the battery runs. Envoy's image is read from the file scripts/validate-envoy.sh reads,
// so an Envoy upgrade changes one line for both. Its admin listener answers /ready on 127.0.0.1 only, which is why
// the battery fetches it from inside the proxy's network namespace.
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
	return []target{{
		name:  "envoy",
		label: "envoy-1.40",
		files: []file{
			{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
			{label: "portable", proxyfile: "../examples/portable/hocon/Proxyfile"},
		},
		image: image,
		args:  []string{"-c", "/config/" + envoy.Config},
		ready: "http://127.0.0.1:9901/ready",
		// The header docs/auth.md names for when_unavailable: allow, whatever the proxy, so a service reads one name.
		mark: "x-envoy-auth-failure-mode-allowed",
	}}
}
