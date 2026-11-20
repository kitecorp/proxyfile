package conformance

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The battery's own Docker objects. It removes them at the start and at the end of each run and touches nothing
// else, so it never disturbs another program's containers on the same machine. Their names are fixed, so a run holds
// this machine's conformance lock while they exist (lockWorld), and a second run waits for it.
const (
	network  = "proxyfile-conformance"
	volume   = "proxyfile-conformance-state"
	image    = "proxyfile-conformance-stub"
	proxy    = "proxyfile-conformance-proxy"
	accounts = "proxyfile-conformance-accounts"
)

// containers are the battery's containers. Their order does not matter: measured on Docker 29.9.3, removing the
// proxy while accounts still shares its network namespace succeeds, and accounts runs on until it is removed too.
var containers = []string{accounts, proxy, "proxyfile-conformance-shop-1", "proxyfile-conformance-shop-2",
	"proxyfile-conformance-catalog", "proxyfile-conformance-check-1", "proxyfile-conformance-check-2",
	"proxyfile-conformance-resolver", hold, hold2, collector}

// hold and hold2 are the backends that take no connection (stub hold), at holdAddress:8095 and hold2Address:8095, the
// addresses nginx-plain's hold service and its held service name: a client's connect to either waits until the
// client's own time runs out. held has both, so a retried call meets one on each try.
const (
	hold, holdAddress   = "proxyfile-conformance-hold", "10.0.2.30"
	hold2, hold2Address = "proxyfile-conformance-hold-2", "10.0.2.31"
)

// collector is the stub's collector (stub collector) at collectorAddress, the address nginx-tracing.yaml and
// examples/full send their spans to, on port 4317, and the address the world's resolver gives collectorName, the name
// nginx-tracing-by-name.yaml sends them to. The world starts it for a file whose entry asks (file.collector), and the
// battery reads the spans it kept on port 8080 inside its container (world.spans).
const collector, collectorAddress, collectorName = "proxyfile-conformance-collector", "10.0.3.40", "collector.internal.example"

// proxyEnvironment is the environment every proxy runs with, which the env tags of nginx-tracing.yaml read: a variable
// set to a value, and one set under a name every object has as a property, which njs leaves out of process.env. Each is
// a name no other file reads, so only the rows of those tags see it.
var proxyEnvironment = []string{"APP_REGION=eu-1", "toString=ts1"}

// runFolder holds all a run writes to disk: the rendered file in config, the stub's build in stub, and the TLS files
// with the server's key in tls and jwt. The battery names it as it names its Docker objects, so a run killed before
// its cleanup leaves it where the next run's start removes it.
var runFolder = filepath.Join(os.TempDir(), "proxyfile-conformance")

// world is a running proxy among the stubs of the examples the battery runs, reached from the host.
type world struct {
	listener  string            // the host and port Docker published the proxy's listener on
	ports     map[string]string // each backend's ports by its role, as its -ports flag lists them
	files     tlsFiles
	mark      string // the proxy's unchecked mark (target.mark), which send forges and compare reads
	plain     bool   // the listener speaks plain HTTP, as one of a file that writes no tls does (newClients)
	collector bool   // the stub's collector takes the proxy's spans (startWorld)
	// http1, http2, http1NoCertificate, http1OtherCA and http1Expired send the requests (client_test.go): they are
	// made once, so the requests of a burst share their connections as a client's would.
	http1, http2, http1NoCertificate, http1OtherCA, http1Expired *http.Client
	// rawTLS is the TLS a raw request (sendRaw) opens its own connection with: the clients', with the client
	// certificate the clients CA signed.
	rawTLS *tls.Config
}

// apart returns w with clients whose connections no other row shares, and a function that closes them: a row that
// overlaps the rows in sequence (exchange.overlap) then leaves no connection in their pool, where a row such as "a
// request before one whose reused connection the service closes" counts on the one its request before left.
func (w *world) apart() (*world, func()) {
	c := *w
	var transports []*http.Transport
	own := func(client *http.Client) *http.Client {
		transport := client.Transport.(*http.Transport).Clone()
		transports = append(transports, transport)
		return &http.Client{Transport: transport, Timeout: client.Timeout}
	}
	c.http1, c.http2, c.http1NoCertificate = own(w.http1), own(w.http2), own(w.http1NoCertificate)
	c.http1OtherCA, c.http1Expired = own(w.http1OtherCA), own(w.http1Expired)
	return &c, func() {
		for _, transport := range transports {
			transport.CloseIdleConnections()
		}
	}
}

// claimWorld removes what an earlier run left, makes an empty runFolder, and removes the world and the folder when the
// test ends. Its caller holds the conformance lock (lockWorld, called first), so the world it removes at the start is
// a run's that ended or was killed, never one still running, and its removal at the end comes before the lock is
// given back.
func claimWorld(t *testing.T) {
	removeWorld()
	t.Cleanup(func() {
		if t.Failed() {
			// A run that failed before the proxy started has no proxy log to show.
			if logs, err := docker("logs", "--tail", "50", proxy); err == nil {
				t.Logf("the proxy's last log lines:\n%s", logs)
			}
		}
		removeWorld()
		docker("image", "rm", image)
	})
	// The folder is the user's alone, as t.TempDir would make it; the proxy reads only the folders inside it, through
	// their own mounts.
	if err := os.Mkdir(runFolder, 0o700); err != nil {
		t.Fatal(err)
	}
}

// startWorld starts the network of the examples the battery runs and the proxy tg on the rendered file in config, and
// waits until the proxy is ready; claimWorld, called first, removes all of it when the test ends. The addresses are
// those examples/full and examples/portable both write: the shop backends at 10.0.1.11 and 10.0.1.12, the resolver at
// 10.0.0.2 answering catalog.internal.example with 10.0.2.21 and collectorName with collectorAddress, and accounts on
// 127.0.0.1:7070, which only a container in the proxy's own network namespace can answer; the two backends of the check
// service of conformance/testdata/two-checks.yaml, check-1 at 10.0.3.31 and check-2 at 10.0.3.32, each the stub's
// accounts on 7070, so a row can make one of them refuse connections; and, for a file that names it (held), the hold
// backends at holdAddress and hold2Address, which nginx-plain's hold and held services name, as no other file's rows
// send to them and the start of each waits for its queue to fill; and, for a file whose entry asks (collects), the
// stub's collector at collectorAddress, which examples/full and nginx-tracing.yaml send their spans to, started before
// the proxy so its first spans have somewhere to go; for every other file that address is left unreachable, and no
// request waits for it. 10.0.0.0/22 is the smallest block holding every one of them. The ports 10000 and 9901 and the
// folders /config, /etc/gateway/tls and /etc/gateway/jwt are the examples' listen, admin and tls keys and
// examples/full's auth.jwt.ca, the same for every target, and the proxy runs with proxyEnvironment. plain says the
// rendered file's listener speaks plain HTTP, so the clients do too.
func startWorld(t *testing.T, tg target, config string, plain, held, collects bool) *world {
	buildStub(t, filepath.Join(runFolder, "stub"))
	files := certificates(t, runFolder)
	if _, err := docker("network", "create", "--subnet", "10.0.0.0/22", network); err != nil {
		if strings.Contains(err.Error(), "Pool overlaps") {
			t.Fatalf("%v\nanother Docker network holds addresses in 10.0.0.0/22, which the examples' addresses "+
				"need; the battery removes only its own network, %s, so it stops here", err, network)
		}
		t.Fatal(err)
	}
	mustDocker(t, "volume", "create", volume)
	state := volume + ":/state"
	backend := func(role, ip string, args ...string) {
		mustDocker(t, append([]string{"run", "-d", "--name", "proxyfile-conformance-" + role, "--network", network,
			"--ip", ip, "-v", state, image}, args...)...)
	}
	ports := map[string]string{}
	serve := func(role, ip, service, list string) {
		backend(role, ip, "serve", "-service", service, "-instance", role, "-ports", list)
		ports[role] = list
	}
	serve("shop-1", "10.0.1.11", "shop", "8080,8081")
	serve("shop-2", "10.0.1.12", "shop", "8080,8081")
	serve("catalog", "10.0.2.21", "catalog", "8090")
	serve("check-1", "10.0.3.31", "accounts", "7070")
	serve("check-2", "10.0.3.32", "accounts", "7070")
	backend("resolver", "10.0.0.2", "dns", "-listen", ":53", "catalog.internal.example=10.0.2.21", collectorName+"="+collectorAddress)
	if held {
		// The queue of a listener holds one past its backlog, which net.core.somaxconn caps, so at 1 it fills with two
		// of the stub's own connections. The sysctl is the network namespace's own, which docker run sets.
		for name, ip := range map[string]string{hold: holdAddress, hold2: hold2Address} {
			mustDocker(t, "run", "-d", "--name", name, "--network", network, "--ip", ip, "--sysctl", "net.core.somaxconn=1",
				image, "hold", "-port", "8095")
		}
		waitHeld(t, hold)
		waitHeld(t, hold2)
	}
	if collects {
		mustDocker(t, "run", "-d", "--name", collector, "--network", network, "--ip", collectorAddress, image, "collector")
		waitSpans(t)
	}
	// The listener is published on a port Docker picks, so a run never meets a port another program holds.
	run := []string{"run", "-d", "--name", proxy, "--network", network, "--ip", "10.0.0.10",
		"-p", "127.0.0.1::10000", "-v", config + ":/config:ro", "-v", files.tls + ":/etc/gateway/tls:ro",
		"-v", files.jwt + ":/etc/gateway/jwt:ro"}
	for _, variable := range proxyEnvironment {
		run = append(run, "-e", variable)
	}
	mustDocker(t, append(append(run, tg.image), tg.args...)...)
	mustDocker(t, "run", "-d", "--name", accounts, "--network", "container:"+proxy,
		"-v", state, image, "serve", "-service", "accounts", "-instance", "accounts", "-ports", "7070")
	waitReady(t, tg.ready)
	listener := strings.TrimSpace(mustDocker(t, "port", proxy, "10000/tcp"))
	w := &world{listener: listener, ports: ports, files: files, mark: tg.mark, plain: plain, collector: collects}
	w.http1, w.http2, w.http1NoCertificate, w.http1OtherCA, w.http1Expired = newClients(t, files, plain, listener)
	w.rawTLS = clientTLS(t, files, &files.client)
	return w
}

// refuse makes the backend role refuse connections on every port until the test ends, as a backend that is down
// does: it signals the stub (serve in conformance/stub) and waits until each port refuses a connection made inside
// the backend's container, and when the test ends it opens the ports again and waits until each answers. It returns that
// opening as a function too, for a row that waits on after its requests, such as for its spans, while the rows after it
// run: the test's end then opens nothing a second time. A row this short never takes the backend out of the proxy's
// pool on the full example, whose health check needs three failures 10 s apart, so the rows after it find both
// backends as before. On nginx-limits it does on nginx, for 10 s, as the row after it reads (backendLimits).
func (w *world) refuse(t *testing.T, role string) (accept func()) {
	ports, ok := w.ports[role]
	if !ok {
		// A role the world does not serve is a mistake in the scenario data, which panics, as send does.
		panic(fmt.Sprintf("the battery serves no backend %q, so it cannot make it refuse connections", role))
	}
	container := "proxyfile-conformance-" + role
	mustDocker(t, "kill", "--signal", "USR1", container)
	waitPorts(t, container, ports, false)
	var once sync.Once
	accept = func() {
		once.Do(func() {
			mustDocker(t, "kill", "--signal", "USR2", container)
			waitPorts(t, container, ports, true)
		})
	}
	t.Cleanup(accept)
	return accept
}

// waitPorts fetches /healthz on each of ports, a list such as 8080,8081, from inside container every 100 ms until
// every port answers 200, when open, or refuses the connection, when not, for 10 s at most.
func waitPorts(t testing.TB, container, ports string, open bool) {
	want := "refuse connections"
	if open {
		want = "answer"
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, port := range strings.Split(ports, ",") {
		for {
			_, err := docker("exec", container, "/stub", "fetch", "http://127.0.0.1:"+port+"/healthz")
			if open && err == nil || !open && err != nil && strings.Contains(err.Error(), "connection refused") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("port %s of %s did not %s within 10s; the last try: %v", port, container, want, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// waitHeld waits until the hold backend name says its queue is full, every 100 ms for 10 s at most: until then a
// connection to it is taken, and a row that wants none would read the proxy's answer to a connection that never answers.
func waitHeld(t testing.TB, name string) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		logs, err := docker("logs", name)
		if err == nil && strings.Contains(logs, "stub holds") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not fill its queue within 10s; its log:\n%s", name, logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// span is one span the stub's collector kept, as its GET /spans answers it (conformance/stub/collector.go): the ids in
// hex, the parent's empty for a span with none, and each attribute's value as text with its type beside it.
type span struct {
	Trace, ID, Parent, State, Name string
	Kind, Status                   int
	Attributes, Types              map[string]string
}

// spans returns every span the collector has kept, read inside its container, where the stub's fetch asks its port
// 8080: no port of it is published, so a run never meets one another program holds.
func (w *world) spans() ([]span, error) {
	out, err := dockerStdout("exec", collector, "/stub", "fetch", "http://127.0.0.1:8080/spans")
	if err != nil {
		return nil, err
	}
	body, ok := strings.CutPrefix(out, "200 ")
	var kept []span
	if !ok || json.Unmarshal([]byte(body), &kept) != nil {
		return nil, fmt.Errorf("the collector answered %q, not 200 and its spans", out)
	}
	return kept, nil
}

// waitSpans fetches the collector's spans every 100 ms until it answers, for 10 s at most, so the proxy starts with
// its collector listening.
func waitSpans(t testing.TB) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := docker("exec", collector, "/stub", "fetch", "http://127.0.0.1:8080/spans")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the collector did not answer within 10s; the last try: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitReady fetches ready from inside the proxy's network namespace every 100 ms until it answers 200, for 30 s at
// most. The proxy's image has no shell, so the stub in accounts, which shares the namespace, does the fetching.
func waitReady(t testing.TB, ready string) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := docker("exec", accounts, "/stub", "fetch", ready)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the proxy was not ready: %s did not answer 200 within 30s; the last try: %v", ready, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// buildStub builds the stub in dir for the platform the Docker server runs, which differs from the host's on a Mac,
// into an image holding the program alone. Built without cgo, the stub needs no C library, so the image needs nothing
// else.
func buildStub(t testing.TB, dir string) {
	platform := strings.TrimSpace(mustDocker(t, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"))
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok {
		t.Fatalf("docker version answered %q, not <os>/<arch>", platform)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(dir, "stub"), "./stub")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build of the stub for %s: %v\n%s", platform, err, out)
	}
	dockerfile := "FROM scratch\nCOPY stub /stub\nENTRYPOINT [\"/stub\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	mustDocker(t, "build", "-q", "-t", image, dir)
}

// removeWorld removes the battery's containers, network, volume and run folder. A run that stopped half way leaves
// some of them and a clean machine has none, so each removal's error is ignored.
func removeWorld() {
	docker(append([]string{"rm", "-f"}, containers...)...)
	docker("network", "rm", network)
	docker("volume", "rm", volume)
	os.RemoveAll(runFolder)
}

// memory returns the memory the proxy's container uses, in bytes, as docker stats shows it: the cgroup's usage less
// its inactive file cache, total_inactive_file under cgroup v1 and inactive_file under v2. It asks Docker's API for one
// sample (one-shot), which answered in about 0.04 s, where docker stats took a second, waiting for two.
func (w *world) memory() (int64, error) {
	client, err := dockerAPI()
	if err != nil {
		return 0, err
	}
	answer, err := client.Get("http://docker/containers/" + proxy + "/stats?stream=false&one-shot=true")
	if err != nil {
		return 0, err
	}
	defer answer.Body.Close()
	var stats struct {
		Memory struct {
			Usage int64            `json:"usage"`
			Stats map[string]int64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(answer.Body).Decode(&stats); err != nil || answer.StatusCode != http.StatusOK || stats.Memory.Usage == 0 {
		return 0, fmt.Errorf("Docker's API gave no memory of %s: %s, %v", proxy, answer.Status, err)
	}
	inactive, v1 := stats.Memory.Stats["total_inactive_file"]
	if !v1 {
		inactive = stats.Memory.Stats["inactive_file"]
	}
	return stats.Memory.Usage - inactive, nil
}

// logLines returns the lines of the proxy's log whose request_id is id, by the event each names, "request" for the
// request log's own line, which names none, each line the JSON text of each field's value by its name. It reads the log
// every 100 ms until a line of each event of wait is there, for 5 s at most: the battery's Envoy writes its log every
// 100 ms (targets), its request log and gateway_auth_unavailable lines of a request together, and nginx writes each
// line as the request ends, so a row that wants no line of an event waits for the request log's line (want.logged). A
// line that is not a JSON object, such as one of the proxy's own messages, is skipped.
func (w *world) logLines(id string, wait []string) map[string][]map[string]string {
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := map[string][]map[string]string{}
		out, _ := docker("logs", proxy)
		for _, text := range strings.Split(out, "\n") {
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(text), &fields) != nil || id == "" || string(fields["request_id"]) != strconv.Quote(id) {
				continue
			}
			event := "request"
			if raw, ok := fields["event"]; ok && json.Unmarshal(raw, &event) != nil {
				event = string(raw)
			}
			line := map[string]string{}
			for name, value := range fields {
				line[name] = string(value)
			}
			found[event] = append(found[event], line)
		}
		missing := slices.ContainsFunc(wait, func(event string) bool { return found[event] == nil })
		if !missing || time.Now().After(deadline) {
			return found
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// logMark returns how many lines the proxy has written to its stdout, where its log lines are, once two reads 250 ms
// apart give the same count, for 2 s at most: the point a row's counted lines are read after (countedAfter). The wait
// lets a line of the row before reach the log first, as the battery's Envoy writes its log every 100 ms.
func (w *world) logMark() int {
	deadline := time.Now().Add(2 * time.Second)
	count := len(stdoutLines())
	for {
		time.Sleep(250 * time.Millisecond)
		now := len(stdoutLines())
		if now == count || time.Now().After(deadline) {
			return now
		}
		count = now
	}
}

// countedAfter returns the lines of c.event the proxy wrote to its stdout after its first mark lines whose c.find fields
// all match, each the JSON text of each field's value by its name. It reads the log every 100 ms until want of them are
// there, for 5 s at most, and then once more 300 ms later, so a line written after the ones wanted is counted too.
func (w *world) countedAfter(mark int, c countedLines, want int) []map[string]string {
	read := func() []map[string]string {
		var found []map[string]string
		lines := stdoutLines()
		for _, text := range lines[min(mark, len(lines)):] {
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(text), &fields) != nil || string(fields["event"]) != strconv.Quote(c.event) {
				continue
			}
			line, matches := map[string]string{}, true
			for name, value := range fields {
				line[name] = string(value)
			}
			for name, pattern := range c.find {
				value, ok := line[name]
				matches = matches && ok && pattern.MatchString(value)
			}
			if matches {
				found = append(found, line)
			}
		}
		return found
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(read()) < want && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	return read()
}

// lineAfter reports whether a line the proxy wrote to its stdout after its first mark lines matches p. It reads the log
// every 100 ms for 5 s at most, as countedAfter does: a proxy writes a request's log line once the request ends.
func (w *world) lineAfter(mark int, p *regexp.Regexp) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines := stdoutLines()
		if slices.ContainsFunc(lines[min(mark, len(lines)):], p.MatchString) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stdoutLines returns the lines the proxy has written to its stdout, or none when Docker cannot read them, which fails
// a row that wants lines. Reading stdout alone keeps the proxy's messages, on stderr, from moving a count of lines
// between two reads. The newline that ends the last line starts no line, so the count is the lines written and a mark
// taken from it (logMark) is the index of the first line written after it.
func stdoutLines() []string {
	out, err := dockerStdout("logs", proxy)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// dockerAPI is a client of the Docker daemon on the unix socket the docker command's current context names.
var dockerAPI = sync.OnceValues(func() (*http.Client, error) {
	out, err := docker("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return nil, err
	}
	socket, ok := strings.CutPrefix(strings.TrimSpace(out), "unix://")
	if !ok {
		return nil, fmt.Errorf("the battery reads the proxy's memory on Docker's unix socket, and the context's host is %q", strings.TrimSpace(out))
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
	return &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 10 * time.Second}, nil
})

// dockerCPUs is the number of CPUs Docker gives a container, read once: HAProxy runs a thread for each, and Envoy a
// worker for each.
var dockerCPUs = sync.OnceValues(func() (int, error) {
	out, err := docker("info", "--format", "{{.NCPU}}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
})

// docker runs one Docker command and returns its output, stdout and stderr together. Every Docker call of the battery
// goes through it or dockerStdout, so a failure always names the command and what Docker answered.
func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %v\n%s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// dockerStdout runs one Docker command and returns its stdout alone: docker logs writes a container's stdout to its own
// stdout and the container's stderr to its own stderr, which a failure names.
func dockerStdout(args ...string) (string, error) {
	var stderr bytes.Buffer
	command := exec.Command("docker", args...)
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %v\n%s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return string(out), nil
}

// mustDocker runs one Docker command and fails the test when it fails.
func mustDocker(t testing.TB, args ...string) string {
	out, err := docker(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
