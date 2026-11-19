package conformance

import (
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// haproxyMainRows are the rows that run the main behaviour of each key HAProxy writes with its own directive, the only
// rows of those keys HAProxy runs (target.mainRows). The rate limit's first row holds its requests, per, burst and by
// too, so it is the main row of those keys as well. A service's health is HAProxy's own active check, so the rows of
// nginx's passive check, which read the requests that failed, do not run on HAProxy.
var haproxyMainRows = map[string][]string{
	"routes[].rate_limit.requests":        {rateMainRow},
	"routes[].rate_limit.per":             {rateMainRow},
	"routes[].rate_limit.burst":           {rateMainRow},
	"routes[].rate_limit.by":              {rateMainRow},
	"routes[].rate_limit":                 {rateMainRow, "another route is not limited"},
	"routes[].retries":                    {"a retry after a connection failure reaches the other backend"},
	"timeouts.idle":                       {"a service silent past the idle time", "an answer that pauses less than the idle time"},
	"dns.resolver":                        {"a service found by DNS"},
	"services.<service name>.hash.header": {"one hash header value reaches one backend every time"},
	"services.<service name>.health":      {"a backend that fails its health checks gets no request"},
	"limits.request_headers.size":         {"a header block of 50 KiB reaches the service", "a header block of 200 KiB is refused"},
	"limits.request_headers.count":        {"120 header lines reach the service", "200 header lines are refused"},
	"log":                                 {"a request answered 404 is logged"},
}

// rateMainRow is the rate limit's main row, the main row of several keys.
const rateMainRow = "requests and burst are admitted, then the route refuses"

// haproxyEntries returns an entry for each image scripts/haproxy-images pins, one a line: HAProxy 3.2 and 3.4, the
// versions the generator writes for, so a row one of them answers differently fails in that entry alone. The
// validator reads the same file, so an HAProxy upgrade changes one line for both. Each entry runs the battery's three
// files for HAProxy, one whose check lets a request through when it cannot answer, one with a check service at two
// addresses that refuses such a request and one of the keys HAProxy writes with its own directives, then
// examples/full and examples/portable, the files Envoy's entry runs: examples/full's committed haproxy.refused is
// checked by its render alone, and examples/portable is loaded with no row on it, as its rows assert the Envoy output's
// answers to keys HAProxy writes with its own features. Last comes HAProxy's file of the connection limit
// (haproxyLimitsFile). The admin listener answers /ready on 127.0.0.1 only, as Envoy's does.
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
				haproxyTrafficFile,
				{label: "full", proxyfile: "../examples/full/hocon/Proxyfile"},
				{label: "portable", proxyfile: "../examples/portable/hocon/Proxyfile", scenarios: []string{}},
				haproxyLimitsFile,
			},
			mainRows: haproxyMainRows,
			image:    p.image,
			// The image's entrypoint adds -W -db to arguments that start with haproxy, which keeps HAProxy in the
			// foreground, where Docker watches it (read in the image's docker-entrypoint.sh).
			args:  []string{"haproxy", "-f", "/config/" + haproxy.Config},
			ready: "http://127.0.0.1:9901/ready",
			// The header the generator writes for when_unavailable: allow, the one docs/auth.md names whatever the proxy,
			// so a service reads one name.
			mark:    generator.UncheckedMark,
			version: p.version,
		})
	}
	return entries
}
