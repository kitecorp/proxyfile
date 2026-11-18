package conformance

import (
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// nginxEntries returns an entry for each image scripts/nginx-images pins, one a line: nginx's stable and mainline
// versions, so a row one of them answers differently fails in that entry alone. The validator reads the same file, so
// an nginx upgrade changes one line for both, and scripts/pinned-images.sh, which the validator reads it with, holds
// the rule pinnedImages holds, so the file the two read is refused by both or by neither. Each entry runs the
// battery's two files for nginx, one with pattern routes and examples/full's traffic keys (rate limits, retries, a
// service found by DNS, balance and hash) and one with no pattern route, whose clients are in front of the proxy, with
// a route limited by client; then its two files with the auth check, one asked under deny at two check addresses and
// one under allow beside the other keys the generator writes and a limited route; then examples/full, whose committed
// nginx.refused is checked by its render alone, and examples/portable, the full example's world with only the keys
// every generator is to render alike, which Envoy's entry runs too.
// The admin listener answers /ready on 127.0.0.1 only, as Envoy's does.
func nginxEntries(t testing.TB) []target {
	nginx, _ := render.TargetNamed("nginx")
	var entries []target
	for _, p := range pinnedImages(t, "nginx") {
		entries = append(entries, target{
			name:  "nginx",
			label: p.label,
			files: []file{
				{label: "nginx-file", proxyfile: "testdata/nginx.yaml"},
				{label: "nginx-plain", proxyfile: "testdata/nginx-plain.yaml"},
				{label: "nginx-checks", proxyfile: "testdata/nginx-checks.yaml"},
				{label: "nginx-auth", proxyfile: "testdata/nginx-auth.yaml"},
				{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
				{label: "portable", proxyfile: "../examples/portable/hocon/Proxyfile"},
			},
			image: p.image,
			// The image's entrypoint runs its arguments as the command, so they start with the program; daemon off
			// keeps nginx in the foreground, where Docker watches it.
			args:  []string{"nginx", "-c", "/config/" + nginx.Config, "-g", "daemon off;"},
			ready: "http://127.0.0.1:9901/ready",
			// The header docs/auth.md names for when_unavailable: allow, whatever the proxy, so a service reads one name.
			mark: "x-envoy-auth-failure-mode-allowed",
		})
	}
	return entries
}
