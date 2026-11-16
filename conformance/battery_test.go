//go:build conformance

package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
// proxy runs on it among the stubs, reached over plain HTTP when the file writes no tls; each row that runs on the
// file (runsOn) is sent and must get its literal for the target and the file (literalFor). It needs Docker, so it is
// built only with the tag conformance:
// go test -tags conformance ./conformance/...
func TestBattery(t *testing.T) {
	// No unit test checks the battery's own rows, because the battery is the test, so it checks them here, before any
	// container starts: each row names core keys of the format, the keys a target's declaration speaks of; each
	// literal sits on a file the row runs on, each target's own literal names a row its target declares partial and
	// sits on a file of the target's entries the row runs on, and each row runs on a file of an Envoy entry, where its
	// literals were measured (rowProblems); every key Envoy's rows exercise is exercised on each entry's files or
	// refused by its target (coverage); every row that runs on an Envoy entry's file runs on one of each entry's files
	// or names a key its target refuses (rowCoverage); and each entry's target has a declaration and a generator (in its
	// subtest).
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
	source, err := os.ReadFile(f.proxyfile)
	if err != nil {
		t.Fatal(err)
	}
	w := startWorld(t, tg, mounted, !slices.Contains(written, "tls"), bytes.Contains(source, []byte(holdAddress)))
	limited := slices.Contains(written, "routes[].rate_limit")
	// In order and never in parallel, but for the rows that overlap: a scenario's quiet and burst count on nothing else
	// reaching the proxy.
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
			// A row that overlaps sends its request here, at the start, and calls t.Parallel, so the testing package
			// runs the rest of its subtest once this function has run the rows in sequence; the scenario's subtest ends
			// after it, so no row of the next scenario runs beside it. Its connections are its own (world.apart).
			for _, e := range rows {
				if !e.overlap {
					continue
				}
				if e.refusing != "" || e.flood != nil || e.holds != nil || e.repeat > 0 || e.streams > 0 || e.refill > 0 {
					panic(fmt.Sprintf("exchange %q overlaps the rows in sequence with a refused backend, a flood, holds, a repeat, streams or a refill", e.name))
				}
				t.Run(e.name, func(t *testing.T) {
					apart, closeIdle := w.apart()
					done := make(chan []string, 1)
					go func() {
						defer closeIdle()
						done <- sendRepeated(apart, e, literalFor(e, tg.name, f.label))
					}()
					t.Parallel()
					for _, difference := range <-done {
						t.Error(difference)
					}
				})
			}
			for _, e := range rows {
				if e.overlap {
					continue
				}
				t.Run(e.name, func(t *testing.T) {
					if limited {
						time.Sleep(e.refill)
					}
					if e.refusing != "" {
						for _, role := range strings.Split(e.refusing, ",") {
							w.refuse(t, role)
						}
					}
					l := literalFor(e, tg.name, f.label)
					var differences []string
					switch {
					case e.flood != nil:
						differences = sendFlood(w, e, l, t.Logf)
					case e.holds != nil:
						differences = sendHolds(w, e, l, t.Logf)
					case e.streams > 0:
						differences = sendStreams(w, e, l)
					default:
						differences = sendRepeated(w, e, l)
					}
					for _, difference := range differences {
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
	// A row that wants no line of an event waits for the request log's line, written with it (logLines).
	var wait []string
	for event, fields := range l.want.logged {
		if fields != nil {
			wait = append(wait, event)
		} else if l.want.logged["request"] == nil {
			panic(fmt.Sprintf("exchange %q wants no %s line and no request log line, so it cannot tell when the proxy has written its lines", e.name, event))
		}
	}
	for i := 1; i <= count; i++ {
		got := w.send(e.send)
		if l.want.logged != nil {
			got.logs = w.logLines(got.header.Get("x-request-id"), wait)
		}
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

// sendStreams sends e's request e.streams times at once over HTTP/2, request i with i=<i> as its query, on a client of
// its own whose one connection the same request with i=0 opens first: Go's HTTP/2 client sends the requests to one host
// on one connection while the proxy's limit of streams allows, 128 on nginx and more on Envoy. It returns the first three
// answers that differ from l.want with i in place of each {i} in its received values, with how many did, so each stream
// must be answered by its own check, whose x-user-check reports the path it was told.
func sendStreams(w *world, e exchange, l literal) []string {
	if l.admitted > 0 || e.repeat > 0 || e.send.raw || e.send.proto != "h2" || strings.Contains(e.send.path, "?") {
		// Streams are sent as they are, over h2 through net/http, to a path with no query; anything else is a mistake in
		// the scenario data.
		panic(fmt.Sprintf("exchange %q sends streams with a repeat, an admitted count, a raw request, a query or %q", e.name, e.send.proto))
	}
	transport := w.http2.Transport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: w.http2.Timeout}
	defer transport.CloseIdleConnections()
	send := func(i int) []string {
		want := l.want
		want.received = map[string]string{}
		for name, value := range l.want.received {
			want.received[name] = strings.ReplaceAll(value, "{i}", strconv.Itoa(i))
		}
		r := e.send
		r.path += "?i=" + strconv.Itoa(i)
		return compare(want, w.sendOn(client, r), w.mark)
	}
	if differences := send(0); differences != nil {
		return append([]string{"the request that opens the connection:"}, differences...)
	}
	var (
		mu        sync.Mutex
		different []string
		count     int
		wg        sync.WaitGroup
	)
	for i := 1; i <= e.streams; i++ {
		wg.Go(func() {
			if differences := send(i); differences != nil {
				mu.Lock()
				if count++; count <= 3 {
					different = append(different, fmt.Sprintf("answer %d: %s", i, strings.Join(differences, "; ")))
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if count > 0 {
		different = append(different, fmt.Sprintf("%d of the %d answers differ", count, e.streams))
	}
	return different
}

// sendFlood sends e's flood and returns each way it differs from l: the first three answers that differ from l.want,
// with how many did, and the proxy's memory when it grew by its bound or more (flood), or could not be read. It logs
// the memory it read with logf, so a passing run shows how far under the bound it stayed. The flood has a client of
// its own that keeps every connection it opens, so its connections stay the given number, as kept-alive clients' do,
// and the other rows' connections are left as they were.
func sendFlood(w *world, e exchange, l literal, logf func(format string, args ...any)) []string {
	f := e.flood
	if l.admitted > 0 || e.repeat > 0 || e.send.raw || e.send.proto != "http/1.1" {
		// A flood is sent as it is, over http/1.1 through net/http; anything else is a mistake in the scenario data.
		panic(fmt.Sprintf("exchange %q floods with a repeat, an admitted count, a raw request or %q", e.name, e.send.proto))
	}
	transport := w.http1.Transport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = f.connections
	client := &http.Client{Transport: transport, Timeout: w.http1.Timeout}
	defer transport.CloseIdleConnections()
	before, err := w.memory()
	if err != nil {
		return []string{err.Error()}
	}
	var (
		next      atomic.Int64
		mu        sync.Mutex
		different []string
		count     int
		wg        sync.WaitGroup
	)
	start := time.Now()
	for range f.connections {
		wg.Go(func() {
			for i := next.Add(1); i <= int64(f.requests); i = next.Add(1) {
				r := e.send
				r.path = f.path(int(i))
				if differences := compare(l.want, w.sendOn(client, r), w.mark); differences != nil {
					mu.Lock()
					if count++; count <= 3 {
						different = append(different, fmt.Sprintf("answer %d: %s", i, strings.Join(differences, "; ")))
					}
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	took := time.Since(start).Round(time.Millisecond)
	if count > 0 {
		different = append(different, fmt.Sprintf("%d of the %d answers differ; the flood took %s", count, f.requests, took))
	}
	after, err := w.memory()
	if err != nil {
		return append(different, err.Error())
	}
	threads, err := dockerCPUs()
	if err != nil {
		return append(different, err.Error())
	}
	bound := f.fixed + f.perThread*int64(threads)
	// The memory can also fall over the flood, and the negative change shifted to MiB reads -1 for any fall under
	// 1 MiB, so the line says which way it went.
	change, by := "grew", after-before
	if by < 0 {
		change, by = "fell", -by
	}
	measured := fmt.Sprintf("the proxy's memory %s from %d MiB to %d MiB, by %d MiB, against a bound of %d MiB, %d MiB "+
		"and %d MiB for each of %d threads; %d requests over %d connections took %s", change, before>>20, after>>20,
		by>>20, bound>>20, f.fixed>>20, f.perThread>>20, threads, f.requests, f.connections, took)
	logf("%s", measured)
	if after-before >= bound {
		different = append(different, "the memory grew by the bound or more: "+measured)
	}
	return different
}

// sendHolds keeps e's holds open and returns each way they differ from what holds asks: the connections that could not
// be opened or send their head, and those closed or answered by the time the memory was read, with how many, and the proxy's
// memory when it grew by its bound or more, or could not be read. It logs the memory it read with logf, so a passing
// run shows how far under the bound it stayed. It opens 32 connections at a time and closes every one when it returns.
func sendHolds(w *world, e exchange, l literal, logf func(format string, args ...any)) []string {
	h := e.holds
	if l.admitted > 0 || e.repeat > 0 || !e.send.raw || e.send.proto != "http/1.1" || !reflect.DeepEqual(l.want, answer{}) {
		// A hold opens raw http/1.1 connections and compares no answer; anything else is a mistake in the scenario data.
		panic(fmt.Sprintf("exchange %q holds with a repeat, an admitted count, an answer to compare, or a request other than a raw http/1.1 one", e.name))
	}
	before, err := w.memory()
	if err != nil {
		return []string{err.Error()}
	}
	conns := make([]net.Conn, h.connections)
	failures := make([]error, h.connections)
	var (
		next atomic.Int64
		wg   sync.WaitGroup
	)
	start := time.Now()
	for range 32 {
		wg.Go(func() {
			for i := int(next.Add(1)) - 1; i < h.connections; i = int(next.Add(1)) - 1 {
				conn, err := w.dialRaw(e.send.proto, serverNameFor(e.send))
				if err == nil {
					if _, err = io.WriteString(conn, h.head); err != nil {
						conn.Close()
						conn = nil
					}
				}
				conns[i], failures[i] = conn, err
			}
		})
	}
	wg.Wait()
	opened := time.Now()
	defer func() {
		for _, conn := range conns {
			if conn != nil {
				conn.Close()
			}
		}
	}()
	time.Sleep(h.settle)
	after, memoryErr := w.memory()
	// Each connection is read for at most 100 ms, all at once: one still open gives a timeout, one answered gives
	// bytes, and one closed an end or an error. An answer or a close already sent is read at once.
	states := make([]string, h.connections)
	for i, conn := range conns {
		if conn == nil {
			continue
		}
		wg.Go(func() {
			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(make([]byte, 256))
			switch {
			case n > 0:
				states[i] = "answered"
			case errors.Is(err, os.ErrDeadlineExceeded):
				states[i] = "open"
			default:
				states[i] = "closed"
			}
		})
	}
	wg.Wait()
	var different []string
	failed, firstFailure := 0, error(nil)
	for _, err := range failures {
		if err != nil {
			if failed++; firstFailure == nil {
				firstFailure = err
			}
		}
	}
	if failed > 0 {
		different = append(different, fmt.Sprintf("%d of the %d connections could not be opened or send their head; the first: %v",
			failed, h.connections, firstFailure))
	}
	ended := map[string]int{}
	for _, state := range states {
		ended[state]++
	}
	if ended["closed"]+ended["answered"] > 0 {
		different = append(different, fmt.Sprintf("%d connections were closed and %d answered within %s of the last one "+
			"opening, want each still open with no answer", ended["closed"], ended["answered"], h.settle))
	}
	if memoryErr != nil {
		return append(different, memoryErr.Error())
	}
	bound := h.perConnection * int64(h.connections)
	measured := fmt.Sprintf("the proxy's memory grew from %d MiB to %d MiB, by %d KiB, %.1f KiB for each of the %d "+
		"connections, against a bound of %d MiB, %d KiB a connection; each sent %d bytes, and opening them took %s",
		before>>20, after>>20, (after-before)>>10, float64(after-before)/1024/float64(h.connections), h.connections,
		bound>>20, h.perConnection>>10, len(h.head), opened.Sub(start).Round(time.Millisecond))
	logf("%s", measured)
	if after-before >= bound {
		different = append(different, "the memory grew by the bound or more: "+measured)
	}
	return different
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
