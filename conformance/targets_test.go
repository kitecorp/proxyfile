// Package conformance runs a proxy on the rendered full example among stub backends and checks that it answers
// every scenario as the Proxyfile says. Its files are test files only, so the released program never carries them,
// and only battery_test.go, built with the tag conformance, starts containers.
package conformance

import (
	"os"
	"strings"
	"testing"
)

// target is one proxy the battery runs the scenarios against.
type target struct {
	name  string   // the --target the file is rendered with, a name in format.Targets
	image string   // the proxy's image, pinned by digest
	file  string   // the rendered file's name, mounted read-only at /config/<file>
	args  []string // the proxy's arguments after the image
	ready string   // fetched inside the proxy's network namespace until it answers 200
}

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
	return []target{{
		name:  "envoy",
		image: image,
		file:  "envoy.yaml",
		args:  []string{"-c", "/config/envoy.yaml"},
		ready: "http://127.0.0.1:9901/ready",
	}}
}
