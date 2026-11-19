package nginx

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
	"github.com/kitecorp/proxyfile/v2/internal/tree"
)

// TestTagOnHeaderNginxWrites: every header nginx writes itself on the request it passes to a service, each one a
// route's location sets and each one the proxy and gRPC modules add (moduleHeaders), is either read by a tag from what nginx
// writes, the variable it writes it from or nothing for one it removes (tagSource), or refused as a tag's header for
// nginx, under either client_address, so no tag reads a client's line where the service receives nginx's own.
func TestTagOnHeaderNginxWrites(t *testing.T) {
	nginx, _ := format.TargetNamed("nginx")
	set := regexp.MustCompile(`^      (?:proxy|grpc)_set_header (\S+) `)
	sources := regexp.MustCompile(`(?m)^  map "" \$pf_tag_sources \{ default "\S+ (\S+)"; \}$`)
	base := string(testsupport.ReadFile(t, "../../conformance/testdata/nginx-auth.yaml"))
	for _, mode := range []string{"connection", "forwarded"} {
		t.Run(mode, func(t *testing.T) {
			tagged := func(header string) string {
				return strings.Replace(base, "client_address: connection\n", "client_address: "+mode+"\n", 1) +
					"tracing:\n  provider:\n    otel: {endpoint: \"10.0.3.40:4317\", service: gw}\n  tags:\n    t: {header: " + header + "}\n  path_in_url: true\n"
			}
			refused := func(header string) bool {
				root, problems := tree.FromYAML("g.yaml", []byte(tagged(header)))
				if problems != nil {
					t.Fatalf("FromYAML: %q", testsupport.ProblemStrings(problems))
				}
				return format.Refusals(format.Proxyfile, root, root, nginx) != nil
			}
			if refused("x-tenant") {
				t.Fatal("the file with a tag on x-tenant is refused, want it read")
			}
			names := slices.Clone(moduleHeaders)
			inRoute := false
			for line := range strings.Lines(configOf(t, tagged("x-tenant"))) {
				switch {
				case strings.HasPrefix(line, "    location @pf_r_"):
					inRoute = true
				case line == "    }\n":
					inRoute = false
				case inRoute:
					if m := set.FindStringSubmatch(line); m != nil {
						names = append(names, strings.ToLower(m[1]))
					}
				}
			}
			slices.Sort(names)
			var fromClient []string
			for _, name := range slices.Compact(names) {
				if refused(name) {
					continue
				}
				source := sources.FindStringSubmatch(configOf(t, tagged(name)))
				if source == nil || !strings.HasPrefix(source[1], "v:") && !strings.HasPrefix(source[1], "a:") {
					fromClient = append(fromClient, name)
				}
			}
			if fromClient != nil {
				t.Errorf("a tag on %q reads the client's line, want each read from what nginx writes or refused", fromClient)
			}
		})
	}
}
