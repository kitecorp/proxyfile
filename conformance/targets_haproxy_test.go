package conformance

import (
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// haproxyEntries returns an entry for each image scripts/haproxy-images pins, one a line: HAProxy 3.2 and 3.4, the
// versions the generator writes for, so a row one of them answers differently fails in that entry alone. The
// validator reads the same file, so an HAProxy upgrade changes one line for both. Each entry runs the battery's two
// files for HAProxy, one whose check lets a request through when it cannot answer and one with a check service at two
// addresses that refuses such a request, then examples/full and examples/portable, the files Envoy's entry runs, whose
// committed haproxy.refused is checked by its render alone. The admin listener answers /ready on 127.0.0.1 only, as
// Envoy's does.
func haproxyEntries(t testing.TB) []target {
	haproxy, _ := render.TargetNamed("haproxy")
	var entries []target
	for _, p := range pinnedImages(t, "haproxy") {
		entries = append(entries, target{
			name:  "haproxy",
			label: p.label,
			files: []file{
				{label: "haproxy-file", proxyfile: "testdata/haproxy.yaml"},
				{label: "two-checks", proxyfile: "testdata/two-checks.yaml"},
				{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
				{label: "portable", proxyfile: "../examples/portable/hocon/Proxyfile"},
			},
			image: p.image,
			// The image's entrypoint adds -W -db to arguments that start with haproxy, which keeps HAProxy in the
			// foreground, where Docker watches it (read in the image's docker-entrypoint.sh).
			args:  []string{"haproxy", "-f", "/config/" + haproxy.Config},
			ready: "http://127.0.0.1:9901/ready",
			// The header the generator writes for when_unavailable: allow, the one docs/auth.md names whatever the proxy,
			// so a service reads one name.
			mark: generator.UncheckedMark,
		})
	}
	return entries
}
