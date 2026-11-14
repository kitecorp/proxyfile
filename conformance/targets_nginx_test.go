package conformance

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// pinnedImage is a line of scripts/nginx-images: an image tagged with its version and pinned by digest.
var pinnedImage = regexp.MustCompile(`^[^\s@]+:([0-9]+)\.([0-9]+)\.[0-9]+@sha256:[0-9a-f]{64}$`)

// nginxEntries returns an entry for each image scripts/nginx-images pins, one a line: nginx's stable and mainline
// versions, so a row one of them answers differently fails in that entry alone. The validator reads the same file, so
// an nginx upgrade changes one line for both. Each entry runs the battery's two files for nginx, one with pattern
// routes and one with none, then examples/full, whose committed nginx.refused is checked by its render alone. The
// admin listener answers /ready on 127.0.0.1 only, as Envoy's does.
func nginxEntries(t testing.TB) []target {
	pins, err := os.ReadFile("../scripts/nginx-images")
	if err != nil {
		t.Fatalf("reading the nginx images: %v", err)
	}
	nginx, _ := render.TargetNamed("nginx")
	var entries []target
	for _, image := range strings.Split(strings.TrimSuffix(string(pins), "\n"), "\n") {
		// A name with no digest would run whatever its tag pointed to that day. scripts/validate-nginx.sh holds the
		// same rule, so the file the two read is refused by both or by neither.
		version := pinnedImage.FindStringSubmatch(image)
		if version == nil {
			t.Fatalf("../scripts/nginx-images holds %q, not one nginx image a line, tagged with its version and pinned by digest (nginx:<major>.<minor>.<patch>@sha256:<digest>)", image)
		}
		entries = append(entries, target{
			name:  "nginx",
			label: "nginx-" + version[1] + "." + version[2],
			files: []file{
				{label: "nginx-file", proxyfile: "testdata/nginx.yaml"},
				{label: "nginx-plain", proxyfile: "testdata/nginx-plain.yaml"},
				{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
			},
			image: image,
			// The image's entrypoint runs its arguments as the command, so they start with the program; daemon off
			// keeps nginx in the foreground, where Docker watches it.
			args:  []string{"nginx", "-c", "/config/" + nginx.Config, "-g", "daemon off;"},
			ready: "http://127.0.0.1:9901/ready",
			// The header docs/auth.md names for when_unavailable: allow, whatever the proxy, so a service reads one name.
			mark: "x-envoy-auth-failure-mode-allowed",
		})
	}
	if entries == nil {
		t.Fatal("../scripts/nginx-images names no image")
	}
	return entries
}
