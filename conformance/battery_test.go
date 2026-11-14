//go:build conformance

package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitecorp/proxyfile/v2/internal/cli"
	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// TestBattery checks its own rows first, then runs each entry of targets on each of its files, in a subtest per
// entry label and in it one per file label. A file whose folder commits <target>.refused is checked by its render
// alone (checkRefused), and starts no container. Any other file is rendered for the target through the program's own
// command line under the target's configuration name, with this machine's conformance lock held (lockWorld), and the
// proxy runs on it among the stubs; each row that runs on the file (runsOn) is sent and must get its literal on the
// file (literalOn). It needs Docker, so it is built only with the tag conformance:
// go test -tags conformance ./conformance/...
func TestBattery(t *testing.T) {
	// No unit test checks the battery's own rows, because the battery is the test, so it checks them here, before any
	// container starts: each row names core keys of the format, the keys a target's declaration speaks of; each
	// literal sits on a file the row runs on, and each row runs on a file of an Envoy entry, where its literals were
	// measured (rowProblems); every key Envoy's rows exercise is exercised on each entry's files or refused by its
	// target (coverage); every row that runs on an Envoy entry's file runs on one of each entry's files or names a key
	// its target refuses (rowCoverage); and each entry's target has a declaration and a generator (in its subtest).
	keys := format.CoreKeys(format.Proxyfile)
	for _, s := range scenarios {
		for _, e := range s.exchanges {
			for _, key := range e.keys {
				if !slices.Contains(keys, key) {
					t.Errorf("row %q of %q names %q, which is no core key of the format", e.name, s.name, key)
				}
			}
		}
	}
	entries := targets(t)
	byEntry, err := rendered(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range slices.Concat(rowProblems(entries, byEntry), coverage(entries, byEntry), rowCoverage(entries, byEntry)) {
		t.Error(problem)
	}
	// The run takes the conformance lock once, when its first file needs Docker, and gives it back when TestBattery
	// ends, after every world is removed (lockWorld): so it waits at most once, and a run of the checks alone, such
	// as -run TestBattery/none, takes none. A run that could not take it starts no world after.
	var tried, locked bool
	lock := func(ft *testing.T) {
		switch {
		case locked:
		case tried:
			ft.Fatalf("this run could not take the conformance lock %s (above), so it starts no world", lockPath)
		default:
			tried = true
			lockWorld(ft, t)
			locked = true
		}
	}
	for _, tg := range entries {
		t.Run(tg.label, func(t *testing.T) {
			if _, ok := format.TargetNamed(tg.name); !ok {
				t.Fatalf("target %q has no declaration in format.Targets", tg.name)
			}
			chosen, ok := render.TargetNamed(tg.name)
			if !ok {
				t.Fatalf("target %q has no generator in render.Targets", tg.name)
			}
			for _, f := range tg.files {
				t.Run(f.label, func(t *testing.T) {
					runFile(t, tg, f, chosen.Config, lock)
				})
			}
		})
	}
}

// runFile runs tg on f, whose render is written as config, as TestBattery says; lock takes the run's conformance lock
// before the file's world is claimed.
func runFile(t *testing.T, tg target, f file, config string, lock func(*testing.T)) {
	refused, err := os.ReadFile(filepath.Join(filepath.Dir(f.proxyfile), tg.name+".refused"))
	switch {
	case err == nil:
		checkRefused(t, tg, f, config, string(refused))
		return
	case !errors.Is(err, fs.ErrNotExist):
		t.Fatal(err)
	}
	written, problem := writtenKeys(f.proxyfile)
	if problem != "" {
		t.Fatal(problem)
	}
	lock(t)
	claimWorld(t)
	// The proxy runs as an unprivileged user in its image, so the mounted folder must be readable by others.
	mounted := filepath.Join(runFolder, "config")
	if err := os.Mkdir(mounted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mounted, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"render", f.proxyfile, "-o", filepath.Join(mounted, config), "--target", tg.name}
	if code := cli.Run(args, &stdout, &stderr, "(devel)"); code != 0 {
		t.Fatalf("render of %s for %s: exit %d with no committed %s.refused beside it; stderr:\n%s", f.proxyfile, tg.name, code,
			tg.name, stderr.String())
	}
	w := startWorld(t, tg, mounted)
	// In order and never in parallel: a scenario's quiet and burst count on nothing else reaching the proxy.
	for _, s := range scenarios {
		var rows []exchange
		for _, e := range s.exchanges {
			if runsOn(e, written) {
				rows = append(rows, e)
			}
		}
		// A scenario with no row on this file gets no subtest, so the run lists no pass that checked nothing.
		if rows == nil {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			time.Sleep(s.quiet)
			for _, e := range rows {
				t.Run(e.name, func(t *testing.T) {
					if e.refusing != "" {
						w.refuse(t, e.refusing)
					}
					for _, difference := range sendRepeated(w, e, literalOn(e, f.label)) {
						t.Error(difference)
					}
				})
			}
		})
	}
}

// sendRepeated sends e's request e.repeat times back to back, or once, and returns each way the answers differ from
// l, the row's literal on the file: each of the first l.admitted answers that is not 200, and, when no later answer is
// what l.want asks, the last one's differences, or with e.every each later answer's differences. Back to back, one
// after the other on the same client, is how a rate limit's burst is sent, so a failure of a repeated row also says
// how long its requests took: a runner too slow for the burst shows there.
func sendRepeated(w *world, e exchange, l literal) []string {
	count := max(e.repeat, 1)
	if l.admitted >= count {
		// A row that compares no answer with want is a mistake in the scenario data, which panics, as send does.
		panic(fmt.Sprintf("exchange %q admits all %d answers, so none is compared with want", e.name, count))
	}
	var differences, last []string
	matched := false
	start := time.Now()
	for i := 1; i <= count; i++ {
		got := w.send(e.send)
		switch {
		case i <= l.admitted && got.err != nil:
			differences = append(differences, fmt.Sprintf("answer %d: error: %v", i, got.err))
		case i <= l.admitted && got.status != http.StatusOK:
			differences = append(differences, fmt.Sprintf("answer %d: got %d, want 200", i, got.status))
		case i > l.admitted && e.every:
			for _, difference := range compare(l.want, got, w.mark) {
				differences = append(differences, fmt.Sprintf("answer %d: %s", i, difference))
			}
		case i > l.admitted && !matched:
			last = compare(l.want, got, w.mark)
			matched = last == nil
		}
	}
	differences = append(differences, last...)
	if differences != nil && count > 1 {
		differences = append(differences, fmt.Sprintf("the %d requests took %s", count, time.Since(start).Round(time.Millisecond)))
	}
	return differences
}

// checkRefused judges a file whose folder commits tg's refusal, so no proxy runs: render must fail for tg with
// exactly the committed lines. They name each path from the repository root, as render prints it run from there; run
// from this package's folder, one below the root, render starts each path with ../, which is trimmed from each line.
func checkRefused(t *testing.T, tg target, f file, config, want string) {
	var stdout, stderr bytes.Buffer
	args := []string{"render", f.proxyfile, "-o", filepath.Join(t.TempDir(), config), "--target", tg.name}
	code := cli.Run(args, &stdout, &stderr, "(devel)")
	var got strings.Builder
	for _, line := range strings.SplitAfter(stderr.String(), "\n") {
		got.WriteString(strings.TrimPrefix(line, "../"))
	}
	if code != 1 || got.String() != want {
		t.Errorf("render of %s for %s: exit %d and stderr %q, want exit 1 and the committed %s.refused %q",
			f.proxyfile, tg.name, code, got.String(), tg.name, want)
	}
}
