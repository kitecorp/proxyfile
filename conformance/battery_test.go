//go:build conformance

package conformance

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitecorp/proxyfile/v2/internal/cli"
	"github.com/kitecorp/proxyfile/v2/internal/format"
)

// TestBattery renders the full example for each target through the program's own command line, starts the target
// on it among the stubs, and checks every exchange of every scenario; a target whose render refuses a key is checked
// by its refusals instead (checkRefusals). It needs Docker, so it is built only with the tag conformance:
// go test -tags conformance ./conformance/...
func TestBattery(t *testing.T) {
	// No unit test checks the battery's own data, because the battery is the test, so it checks two things before it
	// renders: each scenario names core keys of the format, the keys a target's declaration speaks of, and each
	// target has a declaration (below).
	keys := format.CoreKeys(format.Proxyfile)
	for _, s := range scenarios {
		for _, key := range s.keys {
			if !slices.Contains(keys, key) {
				t.Errorf("scenario %q names %q, which is no core key of the format", s.name, key)
			}
		}
	}
	for _, tg := range targets(t) {
		t.Run(tg.name, func(t *testing.T) {
			declared, ok := format.TargetNamed(tg.name)
			if !ok {
				t.Fatalf("target %q has no declaration in format.Targets", tg.name)
			}
			claimWorld(t)
			// The proxy runs as an unprivileged user in its image, so the mounted folder must be readable by others.
			config := filepath.Join(runFolder, "config")
			if err := os.Mkdir(config, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(config, 0o755); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"render", "../examples/full/hocon/Proxyfile", "-o", filepath.Join(config, tg.file), "--target", tg.name}
			if code := cli.Run(args, &stdout, &stderr, "(devel)"); code != 0 {
				checkRefusals(t, declared, stderr.String())
				return
			}
			w := startWorld(t, tg, config)
			// In order and never in parallel: a scenario's quiet and burst count on nothing else reaching the proxy.
			for _, s := range scenarios {
				t.Run(s.name, func(t *testing.T) {
					// The full example writes every key a scenario names, so a key the target refuses must have
					// stopped the render; a file rendered anyway carries a key the target says it cannot, and the
					// scenario's rows would check what the target disowns.
					if keys := refusedKeys(declared, s); keys != nil {
						for _, key := range keys {
							t.Errorf("render wrote examples/full for %s, which refuses %q", declared.Name, key)
						}
						return
					}
					time.Sleep(s.quiet)
					for _, e := range s.exchanges {
						t.Run(e.name, func(t *testing.T) {
							if e.refusing != "" {
								w.refuse(t, e.refusing)
							}
							for _, difference := range sendRepeated(w, e) {
								t.Error(difference)
							}
						})
					}
				})
			}
		})
	}
}

// sendRepeated sends e's request e.repeat times back to back, or once, and returns each way the answers differ from
// the row: each of the first e.admitted answers that is not 200, and, when no later answer is what e.want asks, the
// last one's differences, or with e.every each later answer's differences. Back to back, one after the other on the
// same client, is how a rate limit's burst is sent, so a failure of a repeated row also says how long its requests
// took: a runner too slow for the burst shows there.
func sendRepeated(w *world, e exchange) []string {
	count := max(e.repeat, 1)
	if e.admitted >= count {
		// A row that compares no answer with want is a mistake in the scenario data, which panics, as send does.
		panic(fmt.Sprintf("exchange %q admits all %d answers, so none is compared with want", e.name, count))
	}
	var differences, last []string
	matched := false
	start := time.Now()
	for i := 1; i <= count; i++ {
		got := w.send(e.send)
		switch {
		case i <= e.admitted && got.err != nil:
			differences = append(differences, fmt.Sprintf("answer %d: error: %v", i, got.err))
		case i <= e.admitted && got.status != http.StatusOK:
			differences = append(differences, fmt.Sprintf("answer %d: got %d, want 200", i, got.status))
		case i > e.admitted && e.every:
			for _, difference := range compare(e.want, got) {
				differences = append(differences, fmt.Sprintf("answer %d: %s", i, difference))
			}
		case i > e.admitted && !matched:
			last = compare(e.want, got)
			matched = last == nil
		}
	}
	differences = append(differences, last...)
	if differences != nil && count > 1 {
		differences = append(differences, fmt.Sprintf("the %d requests took %s", count, time.Since(start).Round(time.Millisecond)))
	}
	return differences
}

// checkRefusals judges each scenario by a render that failed, so no world starts. A target that refuses a key the
// full example writes cannot render it, and a scenario of that key passes when the render named each refusal as the
// user sees it, at the end of a line of its stderr; a scenario none of whose keys the target refuses fails, because
// it could not run.
func checkRefusals(t *testing.T, declared format.Target, stderr string) {
	lines := strings.Split(stderr, "\n")
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			refusals := expectedRefusals(declared, s)
			if refusals == nil {
				t.Fatalf("examples/full did not render for %s, so this scenario could not run: %s", declared.Name, stderr)
			}
			for _, refusal := range refusals {
				if !slices.ContainsFunc(lines, func(line string) bool { return strings.HasSuffix(line, refusal) }) {
					t.Errorf("render's stderr lacks %s", refusal)
				}
			}
		})
	}
}
