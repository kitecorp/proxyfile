//go:build conformance

package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
// file (fileKeys.runs) is sent and must get its literal for the target and the file (literalFor). It needs Docker, so
// it is built only with the tag conformance:
// go test -tags conformance -count=1 -timeout 30m ./conformance/...
func TestBattery(t *testing.T) {
	// No unit test checks the battery's own rows, because the battery is the test, so it checks them here, before any
	// container starts: each row names core keys of the format, the keys a target's declaration speaks of; each
	// literal sits on a file the row runs on, each target's own literal names a row its target declares partial and
	// sits on a file of the target's entries the row runs on, and each row runs on a file of an Envoy entry, where its
	// literals were measured (rowProblems); every key Envoy's rows exercise is exercised on the files of each version's
	// entries or refused by its target (coverage); every row that runs on an Envoy entry's file runs on one of the files
	// of each version's entries or names a key its target refuses (rowCoverage), the entries of one target and version
	// judged together (versions); and each entry's target has a declaration and a generator (in its subtest).
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
	w := startWorld(t, tg, mounted, !slices.Contains(written, "tls"), bytes.Contains(source, []byte(holdAddress)), f.collector)
	keys := fileKeys{label: f.label, keys: written, scenarios: f.scenarios, mainRows: tg.mainRows}
	limited := slices.Contains(written, "routes[].rate_limit")
	// In order and never in parallel, but for the rows that overlap: a scenario's quiet and burst count on nothing else
	// reaching the proxy.
	for _, s := range scenarios {
		var rows []exchange
		for _, e := range s.exchanges {
			if keys.runs(s, e) {
				rows = append(rows, e)
			}
		}
		// A scenario with no row on this file gets no subtest, so the run lists no pass that checked nothing.
		if rows == nil {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			time.Sleep(s.quiet)
			reads := newSpanReads(w, rows, tg.name, f.label)
			// A row that overlaps sends its request here, at the start, and calls t.Parallel, so the testing package
			// runs the rest of its subtest once this function has run the rows in sequence; the scenario's subtest ends
			// after it, so no row of the next scenario runs beside it. Its connections are its own (world.apart).
			for _, e := range rows {
				if !e.overlap {
					continue
				}
				if e.refusing != "" || e.settle > 0 || e.flood != nil || e.holds != nil || e.repeat > 0 || e.streams > 0 || e.refill > 0 {
					panic(fmt.Sprintf("exchange %q overlaps the rows in sequence with a refused backend, a settle, a flood, holds, a repeat, streams or a refill", e.name))
				}
				t.Run(e.name, func(t *testing.T) {
					l := literalFor(e, tg.name, f.label)
					apart, closeIdle := w.apart()
					done := make(chan []string, 1)
					var requests []sentRequest
					sent := reads.start(l)
					go func() {
						defer closeIdle()
						defer sent()
						done <- sendRepeated(apart, e, l, &requests)
					}()
					t.Parallel()
					for _, difference := range slices.Concat(<-done, reads.judge(l, requests)) {
						t.Error(difference)
					}
				})
			}
			for _, e := range rows {
				if e.overlap {
					continue
				}
				t.Run(e.name, func(t *testing.T) {
					l := literalFor(e, tg.name, f.label)
					// A row that fails before its requests are sent still counts as sent, so the rows that wait for the
					// spans do not wait for it.
					sent := reads.start(l)
					defer sent()
					if limited {
						time.Sleep(e.refill)
					}
					var accepts []func()
					if e.refusing != "" {
						for _, role := range strings.Split(e.refusing, ",") {
							accepts = append(accepts, w.refuse(t, role))
						}
					}
					time.Sleep(e.settle)
					var differences []string
					var requests []sentRequest
					switch {
					case wantsSpans(l) && (e.flood != nil || e.holds != nil || e.streams > 0):
						panic(fmt.Sprintf("exchange %q wants spans of a flood, holds or streams, whose requests carry no id of their own", e.name))
					case e.flood != nil:
						differences = sendFlood(w, e, l, t.Logf)
					case e.holds != nil:
						differences = sendHolds(w, e, l, t.Logf)
					case e.streams > 0:
						differences = sendStreams(w, e, l)
					default:
						differences = sendRepeated(w, e, l, &requests)
					}
					if wantsSpans(l) {
						// The spans are read once the rows in sequence are done, so the backends this row refused
						// are opened first, as the rows after it must find them, and the rest of the row waits beside
						// the rows that overlap.
						for _, accept := range accepts {
							accept()
						}
						sent()
						t.Parallel()
						differences = append(differences, reads.judge(l, requests)...)
					}
					for _, difference := range differences {
						t.Error(difference)
					}
				})
			}
		})
	}
}

// wantsSpans reports whether l wants a span of its request, or none, which the collector's spans answer (spanReads).
func wantsSpans(l literal) bool {
	return l.want.spanned != nil || l.want.noSpan
}

// sentRequest is one request a row that wants spans sent: the x-request-id it carried, which names its span
// (answer.spanned), and its answer, whose echo says the traceparent the service received.
type sentRequest struct {
	id  string
	got result
}

// spanReadAfter is how long after the last request of a scenario's span rows its spans are read, and read again for
// a row whose span is still missing or that wants none: nginx's exporter and the Envoy output's tracer each send the
// spans they hold every 5 s, so a span is sent within 5 s of its request's end.
const spanReadAfter = 6 * time.Second

// spanReads reads the collector's spans for the rows of one scenario that want spans (wantsSpans), once for them all:
// spanReadAfter after the last of their requests was sent, and once more spanReadAfter later when a row's span is
// still missing or a row wants none, which is judged on that last read alone. Each such row is counted when its subtest
// starts and until its requests are sent (start), so the first read waits for every one of them: a row judges its spans
// only after t.Parallel, once every row of the scenario has started, and a row the -run flag leaves out is never counted.
type spanReads struct {
	w           *world
	pending     sync.WaitGroup
	mu          sync.Mutex
	last        time.Time
	first, then func() ([]span, error)
}

// newSpanReads returns the reads of rows on the file labelled label of the target named target, and panics when one
// of them wants spans on a file whose world starts no collector: a mistake in the scenario data.
func newSpanReads(w *world, rows []exchange, target, label string) *spanReads {
	r := &spanReads{w: w}
	for _, e := range rows {
		if l := literalFor(e, target, label); wantsSpans(l) {
			if !w.collector {
				panic(fmt.Sprintf("exchange %q wants spans on %s, whose world starts no collector (file.collector)", e.name, label))
			}
			if _, ok := e.send.headers["x-request-id"]; !ok || e.repeat > 1 && !strings.Contains(e.send.headers["x-request-id"], "{i}") {
				panic(fmt.Sprintf("exchange %q wants spans of requests that do not each carry an x-request-id of their own", e.name))
			}
		}
	}
	r.first = sync.OnceValues(func() ([]span, error) {
		r.pending.Wait()
		r.mu.Lock()
		last := r.last
		r.mu.Unlock()
		time.Sleep(time.Until(last.Add(spanReadAfter)))
		return w.spans()
	})
	r.then = sync.OnceValues(func() ([]span, error) {
		r.first()
		time.Sleep(spanReadAfter)
		return w.spans()
	})
	return r
}

// start counts a row whose literal is l, when l wants spans, and returns the function that marks the end of its
// requests, which does nothing after its first call.
func (r *spanReads) start(l literal) (sent func()) {
	if !wantsSpans(l) {
		return func() {}
	}
	r.pending.Add(1)
	return sync.OnceFunc(func() {
		r.mu.Lock()
		r.last = time.Now()
		r.mu.Unlock()
		r.pending.Done()
	})
}

// judge returns each way the spans of requests differ from what l wants (compareSpans), or nothing when l wants none.
func (r *spanReads) judge(l literal, requests []sentRequest) []string {
	if !wantsSpans(l) {
		return nil
	}
	first, err := r.first()
	if err != nil {
		return []string{fmt.Sprintf("reading the spans: %v", err)}
	}
	var differences []string
	for _, request := range requests {
		found := spansOf(first, request.id)
		if l.want.noSpan || len(found) == 0 {
			then, err := r.then()
			if err != nil {
				return append(differences, fmt.Sprintf("reading the spans again: %v", err))
			}
			found = spansOf(then, request.id)
		}
		differences = append(differences, compareSpans(l.want, request, found)...)
	}
	return differences
}

// spansOf returns the spans whose guid:x-request-id is id.
func spansOf(kept []span, id string) []span {
	var found []span
	for _, s := range kept {
		if value, ok := s.Attributes["guid:x-request-id"]; ok && value == id {
			found = append(found, s)
		}
	}
	return found
}

// traceparent00 is a traceparent of version 00, its trace id and parent id captured.
var traceparent00 = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

// compareSpans returns each way found, the spans that carry request's x-request-id, differ from want: none when want
// says noSpan; otherwise exactly one, holding each value of want.spanned, whose trace id and span id are those of the
// traceparent the service received, when it received one of version 00.
func compareSpans(want answer, request sentRequest, found []span) []string {
	if want.noSpan {
		if len(found) > 0 {
			return []string{fmt.Sprintf("span of %s: got %d named %q, want none", request.id, len(found), found[0].Name)}
		}
		return nil
	}
	if len(found) != 1 {
		return []string{fmt.Sprintf("span of %s: got %d, want one", request.id, len(found))}
	}
	s := found[0]
	var differences []string
	// The collector stub gives a key the span carries twice the type twice, whose one value a row cannot read.
	for _, key := range slices.Sorted(maps.Keys(s.Types)) {
		if s.Types[key] == "twice" {
			differences = append(differences, fmt.Sprintf("span of %s %s: sent twice", request.id, key))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(want.spanned)) {
		pattern := want.spanned[key]
		var value string
		var ok bool
		switch {
		case key == "name":
			value, ok = s.Name, true
		case key == "parent":
			value, ok = s.Parent, true
		case key == "state":
			value, ok = s.State, true
		case strings.HasPrefix(key, "type:"):
			value, ok = s.Types[strings.TrimPrefix(key, "type:")]
		default:
			value, ok = s.Attributes[key]
		}
		switch {
		case pattern == nil && ok:
			differences = append(differences, fmt.Sprintf("span of %s %s: got %q, want none", request.id, key, value))
		case pattern != nil && (!ok || !pattern.MatchString(value)):
			differences = append(differences, fmt.Sprintf("span of %s %s: got %q, want a value %s matches", request.id, key, value, pattern))
		}
	}
	if request.got.echo != nil {
		if ids := traceparent00.FindStringSubmatch(request.got.echo.Headers["traceparent"]); ids != nil && (ids[1] != s.Trace || ids[2] != s.ID) {
			differences = append(differences, fmt.Sprintf("span of %s: trace %s and id %s, where the service received the traceparent %s",
				request.id, s.Trace, s.ID, request.got.echo.Headers["traceparent"]))
		}
	}
	return differences
}

// sendRepeated sends e's request e.repeat times back to back, or once, and every second time e.alternate when e names
// it, and returns each way the answers differ from l, the row's literal on the file: each of the first l.admitted
// answers that is not 200, and, when no later answer is what l.want asks, the last one's differences, or with e.every
// each later answer's differences, from l.also's too when l names it and the answer is neither, and a line when no
// later answer is l.want or none is l.also; and, when l.want asks for one instance, the instances the later answers
// came from when they are more than one. Back to back, one after the other on the same client, is how a rate limit's
// burst is sent, so a failure of a repeated row also says how long its requests took: a runner too slow for the burst
// shows there. When l wants spans, each request and its answer is added to requests, its x-request-id with the
// request's number in place of {i}, so each of a repeated row's requests names a span of its own.
func sendRepeated(w *world, e exchange, l literal, requests *[]sentRequest) []string {
	count := max(e.repeat, 1)
	if l.admitted >= count {
		// A row that compares no answer with want is a mistake in the scenario data, which panics, as send does.
		panic(fmt.Sprintf("exchange %q admits all %d answers, so none is compared with want", e.name, count))
	}
	if l.want.oneInstance && count-l.admitted < 2 {
		// One answer comes from one instance whatever the proxy does, so the row checks nothing.
		panic(fmt.Sprintf("exchange %q wants its answers from one instance but compares fewer than two", e.name))
	}
	if e.alternate != nil && count < 2 {
		panic(fmt.Sprintf("exchange %q alternates its request but sends it once", e.name))
	}
	if l.also != nil && !e.every {
		panic(fmt.Sprintf("exchange %q has a second answer on a file but does not say every", e.name))
	}
	var differences, last []string
	var sawWant, sawAlso bool
	instances := map[string]int{}
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
	mark := 0
	if l.want.counted != nil || l.want.lineAfter != nil {
		if e.overlap {
			panic(fmt.Sprintf("exchange %q reads lines a row beside it could write too, so it must not overlap", e.name))
		}
		mark = w.logMark()
	}
	for i := 1; i <= count; i++ {
		r := e.send
		if e.alternate != nil && i%2 == 0 {
			r = *e.alternate
		}
		if id := r.headers["x-request-id"]; strings.Contains(id, "{i}") {
			r.headers = maps.Clone(r.headers)
			r.headers["x-request-id"] = strings.ReplaceAll(id, "{i}", strconv.Itoa(i))
		}
		got := w.send(r)
		if wantsSpans(l) {
			*requests = append(*requests, sentRequest{id: r.headers["x-request-id"], got: got})
		}
		if l.want.logged != nil {
			got.logs = w.logLines(got.header.Get("x-request-id"), wait)
		}
		if i > l.admitted && l.want.oneInstance {
			instance := "no service"
			if got.echo != nil {
				instance = got.echo.Instance
			}
			instances[instance]++
		}
		switch {
		case i <= l.admitted && got.unread != "":
			differences = append(differences, fmt.Sprintf("answer %d: %s", i, got.unread))
		case i <= l.admitted && got.err != nil:
			differences = append(differences, fmt.Sprintf("answer %d: error: %v", i, got.err))
		case i <= l.admitted && got.status != http.StatusOK:
			differences = append(differences, fmt.Sprintf("answer %d: got %d, want 200", i, got.status))
		case i > l.admitted && e.every:
			wrong, wanted, other := either(l.want, l.also, got, w.mark)
			sawWant, sawAlso = sawWant || wanted, sawAlso || other
			for _, difference := range wrong {
				differences = append(differences, fmt.Sprintf("answer %d: %s", i, difference))
			}
		case i > l.admitted && !matched:
			last = compare(l.want, got, w.mark)
			matched = last == nil
		}
	}
	differences = append(differences, last...)
	if c := l.want.counted; c != nil {
		lines := w.countedAfter(mark, *c, count)
		if len(lines) != count {
			differences = append(differences, fmt.Sprintf("logged %s: got %d lines that find matches, want %d, one for each request: %q",
				c.event, len(lines), count, lines))
		}
		for _, line := range lines {
			for _, name := range slices.Sorted(maps.Keys(c.fields)) {
				if value, ok := line[name]; !ok || !c.fields[name].MatchString(value) {
					differences = append(differences, fmt.Sprintf("logged %s %s: got %q, want a value %s matches", c.event, name, value, c.fields[name]))
				}
			}
		}
	}
	if p := l.want.lineAfter; p != nil && !w.lineAfter(mark, p) {
		differences = append(differences, fmt.Sprintf("logged: no line the proxy wrote after the row began matches %s", p))
	}
	differences = append(differences, bothSeen(l.also, sawWant, sawAlso)...)
	if len(instances) > 1 || instances["no service"] > 0 {
		var from []string
		for _, instance := range slices.Sorted(maps.Keys(instances)) {
			from = append(from, fmt.Sprintf("%s %d", instance, instances[instance]))
		}
		differences = append(differences, fmt.Sprintf("the answers came from %s, want one instance", strings.Join(from, ", ")))
	}
	if differences != nil && count > 1 {
		differences = append(differences, fmt.Sprintf("the %d requests took %s", count, time.Since(start).Round(time.Millisecond)))
	}
	return differences
}

// sendStreams sends e's request e.streams times at once over HTTP/2, request i with i=<i> as its query, on a client of
// its own whose one connection the same request with i=0 opens first: Go's HTTP/2 client sends the requests to one host
// on one connection while the proxy's limit of streams allows, 128 on nginx and more on Envoy. It returns the first three
// answers that differ from l.want with i in place of each {i} in its received values, with how many did, so each stream
// must be answered by its own check, whose x-user-check reports the path it was told. With l.also, as sendRepeated reads
// it, each stream's answer may be l.want or l.also, and at least one must be each, for a file whose values give only
// some of the streams want; the request that opens the connection goes alone, so it must still be l.want.
func sendStreams(w *world, e exchange, l literal) []string {
	if l.admitted > 0 || e.repeat > 0 || e.send.raw || e.send.proto != "h2" || strings.Contains(e.send.path, "?") {
		// Streams are sent as they are, over h2 through net/http, to a path with no query; anything else is a mistake in
		// the scenario data.
		panic(fmt.Sprintf("exchange %q sends streams with a repeat, an admitted count, a raw request, a query or %q", e.name, e.send.proto))
	}
	transport := w.http2.Transport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: w.http2.Timeout}
	defer transport.CloseIdleConnections()
	// numbered is a with i in place of each {i} in its received values. A nil map stays nil, so a refused answer still
	// compares nothing else (compare).
	numbered := func(a answer, i int) answer {
		if a.received != nil {
			received := map[string]string{}
			for name, value := range a.received {
				received[name] = strings.ReplaceAll(value, "{i}", strconv.Itoa(i))
			}
			a.received = received
		}
		return a
	}
	send := func(i int) result {
		r := e.send
		r.path += "?i=" + strconv.Itoa(i)
		return w.sendOn(client, r)
	}
	if differences := compare(numbered(l.want, 0), send(0), w.mark); differences != nil {
		return append([]string{"the request that opens the connection:"}, differences...)
	}
	var (
		mu               sync.Mutex
		different        []string
		count            int
		sawWant, sawAlso bool
		wg               sync.WaitGroup
	)
	for i := 1; i <= e.streams; i++ {
		wg.Go(func() {
			var also *answer
			if l.also != nil {
				also = new(numbered(*l.also, i))
			}
			differences, wanted, other := either(numbered(l.want, i), also, send(i), w.mark)
			mu.Lock()
			defer mu.Unlock()
			sawWant, sawAlso = sawWant || wanted, sawAlso || other
			if differences != nil {
				if count++; count <= 3 {
					different = append(different, fmt.Sprintf("answer %d: %s", i, strings.Join(differences, "; ")))
				}
			}
		})
	}
	wg.Wait()
	if count > 0 {
		different = append(different, fmt.Sprintf("%d of the %d answers differ", count, e.streams))
	}
	return append(different, bothSeen(l.also, sawWant, sawAlso)...)
}

// either compares got with want and, when it differs and also is set, with also, a literal's two answers, as
// sendRepeated and sendStreams read them: it returns no difference when got is one of the two, and otherwise want's
// differences with each of also's after "or"; wanted and other say which of the two got was.
func either(want answer, also *answer, got result, mark string) (differences []string, wanted, other bool) {
	differences = compare(want, got, mark)
	if differences == nil || also == nil {
		return differences, differences == nil, false
	}
	others := compare(*also, got, mark)
	if others == nil {
		return nil, false, true
	}
	for _, difference := range others {
		differences = append(differences, "or "+difference)
	}
	return differences, false, false
}

// bothSeen returns a line for each of want and also that no answer compared was, or nil without also: a literal with
// also wants at least one answer of each.
func bothSeen(also *answer, sawWant, sawAlso bool) []string {
	if also == nil {
		return nil
	}
	var lines []string
	if !sawWant {
		lines = append(lines, "no answer compared was what want asks")
	}
	if !sawAlso {
		lines = append(lines, "no answer compared was what also asks")
	}
	return lines
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
