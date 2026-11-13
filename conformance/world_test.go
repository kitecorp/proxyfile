package conformance

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The battery's own Docker objects. It removes them at the start and at the end of each run and touches nothing
// else, so it never disturbs another program's containers on the same machine.
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
	"proxyfile-conformance-catalog", "proxyfile-conformance-resolver"}

// runFolder holds all a run writes to disk: the rendered file in config, the stub's build in stub, and the TLS files
// with the server's key in tls and jwt. The battery names it as it names its Docker objects, so a run killed before
// its cleanup leaves it where the next run's start removes it.
var runFolder = filepath.Join(os.TempDir(), "proxyfile-conformance")

// world is a running proxy among the stubs of the full example, reached from the host.
type world struct {
	listener string // the host and port Docker published the proxy's listener on
	files    tlsFiles
	// http1, http2, http1NoCertificate and http1OtherCA send the requests (client_test.go): they are made once, so
	// the requests of a burst share their connections as a client's would.
	http1, http2, http1NoCertificate, http1OtherCA *http.Client
}

// claimWorld removes what an earlier run left, makes an empty runFolder, and removes the world and the folder when the
// test ends.
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

// startWorld starts the network of the full example and the proxy tg on the rendered file in config, and waits until
// the proxy is ready; claimWorld, called first, removes all of it when the test ends. The addresses are the example's:
// the shop backends at 10.0.1.11 and 10.0.1.12, the resolver at 10.0.0.2 answering catalog.internal.example with
// 10.0.2.21, and accounts on 127.0.0.1:7070, which only a container in the proxy's own network namespace can answer.
// 10.0.0.0/22 is the smallest block holding every one of them. The collector at 10.0.3.40 is left unreachable: no
// request waits for it. The ports 10000 and 9901 and the folders /config, /etc/gateway/tls and /etc/gateway/jwt are
// the example's listen, admin, tls keys and auth.jwt.ca, the same for every target.
func startWorld(t *testing.T, tg target, config string) *world {
	buildStub(t, filepath.Join(runFolder, "stub"))
	files := certificates(t, runFolder)
	if _, err := docker("network", "create", "--subnet", "10.0.0.0/22", network); err != nil {
		if strings.Contains(err.Error(), "Pool overlaps") {
			t.Fatalf("%v\nanother Docker network holds addresses in 10.0.0.0/22, which the full example's addresses "+
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
	backend("shop-1", "10.0.1.11", "serve", "-service", "shop", "-instance", "shop-1", "-ports", "8080,8081")
	backend("shop-2", "10.0.1.12", "serve", "-service", "shop", "-instance", "shop-2", "-ports", "8080,8081")
	backend("catalog", "10.0.2.21", "serve", "-service", "catalog", "-instance", "catalog", "-ports", "8090")
	backend("resolver", "10.0.0.2", "dns", "-listen", ":53", "catalog.internal.example=10.0.2.21")
	// The listener is published on a port Docker picks, so a run never meets a port another program holds.
	mustDocker(t, append([]string{"run", "-d", "--name", proxy, "--network", network, "--ip", "10.0.0.10",
		"-p", "127.0.0.1::10000", "-v", config + ":/config:ro", "-v", files.tls + ":/etc/gateway/tls:ro",
		"-v", files.jwt + ":/etc/gateway/jwt:ro", tg.image}, tg.args...)...)
	mustDocker(t, "run", "-d", "--name", accounts, "--network", "container:"+proxy,
		"-v", state, image, "serve", "-service", "accounts", "-instance", "accounts", "-ports", "7070")
	waitReady(t, tg.ready)
	listener := strings.TrimSpace(mustDocker(t, "port", proxy, "10000/tcp"))
	w := &world{listener: listener, files: files}
	w.http1, w.http2, w.http1NoCertificate, w.http1OtherCA = newClients(t, files)
	return w
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

// docker runs one Docker command and returns its output. Every Docker call of the battery goes through it, so a
// failure always names the command and what Docker answered.
func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %v\n%s", strings.Join(args, " "), err, bytes.TrimSpace(out))
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
