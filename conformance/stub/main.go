// Stub plays every backend of examples/full, its auth check and its DNS resolver for the conformance battery. It
// uses the standard library only, so the battery adds nothing to go.mod and nothing to the released program.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const usage = `usage: stub serve -service <name> -instance <id> -ports <port>[,<port>...] [-state <dir>] [-hang <duration>]
       stub dns -listen <addr> <name>=<ipv4>...
       stub fetch <url>`

func main() {
	if len(os.Args) < 2 {
		usageError("no command")
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "dns":
		dns(os.Args[2:])
	case "fetch":
		fetch(os.Args[2:])
	default:
		usageError(fmt.Sprintf("unknown command %q", os.Args[1]))
	}
}

// usageError exits 2, the code the flag package exits with on a flag it cannot read, so every wrong command line
// ends the same way.
func usageError(problem string) {
	fmt.Fprintf(os.Stderr, "stub: %s\n%s\n", problem, usage)
	os.Exit(2)
}

// serve answers on every port of one backend with the same handler.
func serve(args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	service := flags.String("service", "", "the service the echo names: shop, catalog or accounts")
	instance := flags.String("instance", "", "the instance the echo names, such as shop-1")
	ports := flags.String("ports", "", "the ports to answer on, separated by commas")
	state := flags.String("state", "/state", "the folder every backend shares, which remembers each x-stub-fail-first key")
	// 3s is twice the full example's auth.timeout of 1500ms, so the proxy gives up on a check that hangs.
	hang := flags.Duration("hang", 3*time.Second, "how long the auth check waits before it answers the token hang")
	flags.Parse(args)
	if *service == "" || *instance == "" || *ports == "" || flags.NArg() > 0 {
		usageError("serve needs -service, -instance and -ports, and nothing after them")
	}
	handler := &backend{service: *service, instance: *instance, state: *state, hang: *hang}
	// One port answers HTTP/1.1 and HTTP/2 without TLS, because the full example sends HTTP and gRPC to the same
	// port of accounts.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	errs := make(chan error)
	for _, port := range strings.Split(*ports, ",") {
		server := &http.Server{Addr: ":" + port, Handler: handler, Protocols: &protocols}
		go func() { errs <- server.ListenAndServe() }()
	}
	fmt.Fprintf(os.Stderr, "stub %s/%s on %s\n", *service, *instance, *ports)
	// No server is ever closed, so a server that returns has failed, and a backend missing a port must not go on.
	fmt.Fprintln(os.Stderr, <-errs)
	os.Exit(1)
}

// dns answers the queries the proxy's resolver sends, from the records on the command line.
func dns(args []string) {
	flags := flag.NewFlagSet("dns", flag.ExitOnError)
	listen := flags.String("listen", ":53", "the UDP address to answer on")
	flags.Parse(args)
	records := map[string]net.IP{}
	for _, record := range flags.Args() {
		name, address, _ := strings.Cut(record, "=")
		ip := net.ParseIP(address).To4()
		if name == "" || ip == nil {
			usageError(fmt.Sprintf("dns record %q is not <name>=<ipv4>", record))
		}
		records[strings.ToLower(name)] = ip
	}
	conn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "dns on %s answering %s\n", *listen, strings.Join(flags.Args(), " "))
	resolve(conn, records)
}

// fetch prints the status and body of one GET and exits 0 only on 200. The battery runs it inside the proxy's
// network namespace to ask whether the proxy is ready, because the proxy's image has no shell and its admin
// listener answers on 127.0.0.1 only.
func fetch(args []string) {
	if len(args) != 1 {
		usageError("fetch needs one URL")
	}
	// A timeout keeps one stalled try from using up the battery's whole wait for the proxy.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%d %s", resp.StatusCode, body)
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
