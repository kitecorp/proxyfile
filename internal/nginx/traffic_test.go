package nginx

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/generator"
	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/read"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The rows read the unexported writers of the route lines, so they are in the package itself, which cannot reach the
// helpers of the package's other tests (nginx_test): configOf reads a proxyfile as they do.

// configOf returns the configuration Generate writes for the proxyfile text, read as render reads a proxyfile that
// writes its origins and its RPC methods. A problem or an error ends the test.
func configOf(t *testing.T, proxyfile string) string {
	t.Helper()
	config, err := generated(t, proxyfile)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return config
}

// generated returns what Generate returns for the proxyfile text, the configuration or the error. A problem ends the
// test.
func generated(t *testing.T, proxyfile string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxyfile.yaml")
	testsupport.WriteFile(t, path, proxyfile)
	file, problems := read.Proxyfile(path)
	if problems != nil {
		t.Fatalf("read.Proxyfile: %q", testsupport.ProblemStrings(problems))
	}
	var routes []rpc.Route
	if file.MethodsKey != nil {
		if routes, problems = rpc.Resolve(file.Gateway, file.Methods); problems != nil {
			t.Fatalf("rpc.Resolve: %q", testsupport.ProblemStrings(problems))
		}
	}
	files, err := Generate(generator.Input{Gateway: file.Gateway, Origins: file.Origins, RPC: routes})
	if err != nil {
		return "", err
	}
	return string(files[0].Bytes), nil
}

// namedLocation returns the lines of the location name of config's first environment server, from its opening line to
// its closing brace, or nil when it holds none. Every environment server writes the same locations, and the files
// below have one; the default server before it writes a location / of its own.
func namedLocation(config, name string) []string {
	all := strings.Split(config, "\n")
	server := slices.IndexFunc(all, func(line string) bool { return strings.HasPrefix(line, "    server_name ") })
	if server < 0 {
		return nil
	}
	at := slices.Index(all[server:], "    location "+name+" {")
	if at < 0 {
		return nil
	}
	at += server
	return all[at : at+slices.Index(all[at:], "    }")+1]
}

// trafficFile is a proxyfile with one environment under client_address, app over HTTP, shop over gRPC with two
// backends, eight plain routes, the first of them with first after its to, and then the rpc entry with rpcKeys after
// its to, so the methods are routes 9 and up, sorted by path: AddItem, GetCart and, with three, RemoveItem.
func trafficFile(clientAddress, first, rpcKeys string, methods int) string {
	text := "listen: 8000\nclient_address: " + clientAddress + "\nservices:\n  app: {http: 3000}\n" +
		"  shop: {grpc: 8081, addresses: [\"10.0.1.11\", \"10.0.1.12\"]}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n" +
		"  - {name: r1, prefix: /r1/, to: app" + first + "}\n"
	for n := 2; n <= 8; n++ {
		text += fmt.Sprintf("  - {name: r%d, prefix: /r%d/, to: app}\n", n, n)
	}
	text += "  - rpc: {to: {\"*\": shop}" + rpcKeys + "}\nrpc:\n  methods:\n"
	for _, method := range []string{"AddItem", "GetCart", "RemoveItem"}[:methods] {
		text += "    - {service: example.shop.v1.CartService, method: " + method + "}\n"
	}
	return text
}

// limitedBy is a limit of requests a period of millis, counted for each client or for all of them together.
func limitedBy(requests, millis int64, byClient bool) *model.RateLimit {
	return &model.RateLimit{Requests: requests, Per: model.Time{Millis: millis}, ByClient: byClient}
}

// TestRateOf: nginx's rate is a whole number of requests a second or, when a second gives none, a minute; a limit that
// gives neither has no rate.
func TestRateOf(t *testing.T) {
	cases := []struct {
		requests, millis int64
		want             string
		ok               bool
	}{
		{5, 1000, "5r/s", true},
		{4, 2000, "2r/s", true},
		{3, 2000, "90r/m", true},
		{6, 60000, "6r/m", true},
		{2, 60000, "2r/m", true},
		{100, 1000, "100r/s", true},
		{1, 7000, "", false},
		{7, 3600000, "", false},
	}
	for _, c := range cases {
		if got, ok := rateOf(limitedBy(c.requests, c.millis, true)); got != c.want || ok != c.ok {
			t.Errorf("rateOf(%d per %d ms) = %q, %v, want %q, %v", c.requests, c.millis, got, ok, c.want, c.ok)
		}
	}
}

// TestNoRate: a limit whose rate is whole neither a second nor a minute, which nginx's declaration refuses before
// render calls Generate, is an error of Generate's, never a zone nginx would refuse to load.
func TestNoRate(t *testing.T) {
	_, err := generated(t, trafficFile("connection", ", rate_limit: {requests: 1, per: 7s}", "", 2))
	if want := "the entry 1 of routes limits 1 requests each 7000 ms, a rate of no whole number of requests a second or a minute"; err == nil || err.Error() != want {
		t.Errorf("Generate's error is %v, want %q", err, want)
	}
}

// TestLimitLines: each location of a limited entry's routes charges its bucket and sends the 429 to the answer the
// script picks; the location of a route with no limit holds neither line.
func TestLimitLines(t *testing.T) {
	limit := func(requests, burst int64) *model.RateLimit {
		l := limitedBy(requests, 1000, true)
		l.Burst = burst
		return l
	}
	const answer = "      error_page 429 = @pf_429$pf_429_kind;"
	for _, c := range []struct {
		name  string
		limit *model.RateLimit
		want  []string
	}{
		{"requests 5 a second", limit(5, 0), []string{"      limit_req zone=pf_rl_1_peer burst=4 nodelay;", answer}},
		{"requests 5 a second and burst 5", limit(5, 5), []string{"      limit_req zone=pf_rl_1_peer burst=9 nodelay;", answer}},
		// nginx stops at start on burst=0.
		{"requests 1 and burst 0", limit(1, 0), []string{"      limit_req zone=pf_rl_1_peer nodelay;", answer}},
	} {
		if got := limitLines(c.limit, 1, edge{connection: true}); !slices.Equal(got, c.want) {
			t.Errorf("%s: limitLines = %q, want %q", c.name, got, c.want)
		}
	}

	// The lines follow the request log's upstream, and on a retried route the mark of a retried call, before the
	// check's handler would.
	charged := []string{`      set $pf_log_upstream "shop_grpc";`, "      limit_req zone=pf_rl_1_all burst=2 nodelay;", answer}
	retried := []string{`      set $pf_log_upstream "shop_grpc";`, "      set $pf_log_retried 1;", "      limit_req zone=pf_rl_1_all burst=2 nodelay;", answer}
	prefix := configOf(t, trafficFile("connection", ", rate_limit: {requests: 5, per: 1s}", "", 2))
	for _, name := range []string{"@pf_r_1", "@pf_r_1-uri"} {
		want := []string{`      set $pf_log_upstream "app_http";`, "      limit_req zone=pf_rl_1_peer burst=4 nodelay;", answer}
		if missing := testsupport.FirstMissingBlock(strings.Join(namedLocation(prefix, name), "\n"), [][]string{want}); missing != "" {
			t.Errorf("a limited prefix route's %s lacks:\n%s", name, missing)
		}
	}
	// Under forwarded the same route charges the zone of the script's client key, which the http block names.
	forwarded := configOf(t, trafficFile("forwarded", ", rate_limit: {requests: 5, per: 1s}", "", 2))
	if line := "  limit_req_zone $pf_client_key zone=pf_rl_1_client:16m rate=5r/s;"; !slices.Contains(strings.Split(forwarded, "\n"), line) {
		t.Errorf("under forwarded, no line %q", line)
	}
	for _, name := range []string{"@pf_r_1", "@pf_r_1-uri"} {
		want := []string{`      set $pf_log_upstream "app_http";`, "      limit_req zone=pf_rl_1_client burst=4 nodelay;", answer}
		if missing := testsupport.FirstMissingBlock(strings.Join(namedLocation(forwarded, name), "\n"), [][]string{want}); missing != "" {
			t.Errorf("under forwarded, a limited prefix route's %s lacks:\n%s", name, missing)
		}
	}
	for _, name := range []string{"@pf_r_2", "@pf_r_2-uri", "@pf_r_9", "@pf_r_9-uri"} {
		location := namedLocation(prefix, name)
		if location == nil || slices.ContainsFunc(location, func(line string) bool {
			return strings.Contains(line, "limit_req") || strings.Contains(line, "error_page 429")
		}) {
			t.Errorf("an unlimited route's %s is %q, want one with neither line", name, location)
		}
	}

	for _, c := range []struct {
		name      string
		rpcKeys   string
		locations []string
		want      []string
	}{
		{"an rpc entry limited by all", ", rate_limit: {requests: 3, per: 1s, by: all}",
			[]string{"@pf_r_9", "@pf_r_9-uri", "@pf_r_10", "@pf_r_10-uri", "@pf_r_11", "@pf_r_11-uri"}, charged},
		{"an rpc entry limited by all and retried", ", retries: 2, rate_limit: {requests: 3, per: 1s, by: all}",
			[]string{"@pf_r_9", "@pf_r_9-uri", "@pf_r_9-once", "@pf_r_9-uri-once", "@pf_r_11", "@pf_r_11-uri-once"}, retried},
	} {
		config := configOf(t, trafficFile("connection", "", c.rpcKeys, 3))
		if got := strings.Count(config, "limit_req_zone "); got != 1 {
			t.Errorf("%s: %d limit_req_zone lines, want 1", c.name, got)
		}
		for _, name := range c.locations {
			if missing := testsupport.FirstMissingBlock(strings.Join(namedLocation(config, name), "\n"), [][]string{c.want}); missing != "" {
				t.Errorf("%s: %s lacks:\n%s", c.name, name, missing)
			}
		}
	}
}

// TestLimitZones: each limited entry of routes has a zone, named by the number routesOf gave its routes and the kind
// of its key, the methods of the rpc entry sharing one, and keyed by the client's address as client_address reads it,
// or by one key for all clients, with the status and the answer's kind written once; the script's client key is read
// once for every reader of it. A zone's name holds the kind of its key, so no two files give a zone of one name two
// keys: nginx refuses a reload that gives a zone of the same name another key, and keeps the whole old configuration
// running: `nginx -s reload` exits 0, nginx -t passes the new file, and one [emerg] line in the log is the only sign
// (measured on nginx 1.30.6 and 1.31.8 with a changed by and a changed client_address). A changed rate keeps the name,
// and nginx keeps the zone's counts across the reload.
func TestLimitZones(t *testing.T) {
	limited := func(limit *model.RateLimit, bucket int) route {
		return route{limit: limit, bucket: bucket}
	}
	byPeer := limitedBy(3, 2000, true)
	head := []string{"  limit_req_status 429;", "  js_set $pf_429_kind pf.limitedKind;"}
	for _, c := range []struct {
		name   string
		routes []route
		e      edge
		want   []string
	}{
		{"no limit", []route{{}}, edge{connection: true}, nil},
		{"by all", []route{limited(limitedBy(5, 1000, false), 1)}, edge{connection: true},
			append(slices.Clone(head), `  limit_req_zone "all" zone=pf_rl_1_all:16m rate=5r/s;`)},
		{"by client under connection", []route{limited(limitedBy(5, 1000, true), 1)}, edge{connection: true},
			append(slices.Clone(head), "  limit_req_zone $binary_remote_addr zone=pf_rl_1_peer:16m rate=5r/s;")},
		{"by client under forwarded", []route{limited(limitedBy(5, 1000, true), 1)}, edge{},
			append(slices.Clone(head), "  limit_req_zone $pf_client_key zone=pf_rl_1_client:16m rate=5r/s;")},
		{"by all under forwarded", []route{limited(limitedBy(5, 1000, false), 1)}, edge{},
			append(slices.Clone(head), `  limit_req_zone "all" zone=pf_rl_1_all:16m rate=5r/s;`)},
		{"another rate of the same kind keeps the name", []route{limited(limitedBy(2, 60000, true), 1)}, edge{},
			append(slices.Clone(head), "  limit_req_zone $pf_client_key zone=pf_rl_1_client:16m rate=2r/m;")},
		{"in the order of the limited entries, the rpc entry's two methods sharing one zone",
			[]route{limited(limitedBy(5, 1000, false), 1), {}, limited(byPeer, 2), limited(byPeer, 2)}, edge{connection: true},
			append(slices.Clone(head), `  limit_req_zone "all" zone=pf_rl_1_all:16m rate=5r/s;`,
				"  limit_req_zone $binary_remote_addr zone=pf_rl_2_peer:16m rate=90r/m;")},
	} {
		if got := limitZones(c.routes, c.e); !slices.Equal(got, c.want) {
			t.Errorf("%s: limitZones = %q, want %q", c.name, got, c.want)
		}
	}

	// Two routes limited by client and a service that hashes the client read one key.
	const keyLine = "js_set $pf_client_key pf.clientKey;"
	readers := func(clientAddress string) string {
		return "listen: 8000\nclient_address: " + clientAddress + "\nservices:\n  app: {http: 3000}\n" +
			"  sticky: {http: 3001, balance: hash, hash: {client: true}}\norigins:\n  local: {hosts: [localhost]}\nroutes:\n" +
			"  - {name: one, prefix: /one/, to: app, rate_limit: {requests: 5, per: 1s}}\n" +
			"  - {name: two, prefix: /two/, to: app, rate_limit: {requests: 6, per: 1m}}\n" +
			"  - {name: rest, prefix: /, to: sticky}\n"
	}
	for _, c := range []struct {
		clientAddress string
		want          int
	}{{"forwarded", 1}, {"connection", 0}} {
		if got := strings.Count(configOf(t, readers(c.clientAddress)), keyLine); got != c.want {
			t.Errorf("under %s: %d lines %q, want %d", c.clientAddress, got, keyLine, c.want)
		}
	}
}

// TestLimitedAnswers: a request over the limit gets the Envoy output's answer for its kind, with the server's own
// headers, and the connection closed after a request with a body.
func TestLimitedAnswers(t *testing.T) {
	cors := []string{"    set $pf_reached 1;", "    add_header Access-Control-Allow-Origin $pf_acao1 always;",
		"    add_header Access-Control-Expose-Headers $pf_aceh1 always;", "    proxy_hide_header Access-Control-Allow-Origin;"}
	withCORS := []string{
		`    location @pf_429 {`,
		`      default_type text/plain;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      return 429 "local_rate_limited";`,
		`    }`,
		`    location @pf_429-body {`,
		`      default_type text/plain;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      keepalive_timeout 0;`,
		`      return 429 "local_rate_limited";`,
		`    }`,
		`    location @pf_429-connect {`,
		`      default_type application/json;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      return 429 '{"code":"resource_exhausted","message":"local_rate_limited"}';`,
		`    }`,
		`    location @pf_429-connect-body {`,
		`      default_type application/json;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      keepalive_timeout 0;`,
		`      return 429 '{"code":"resource_exhausted","message":"local_rate_limited"}';`,
		`    }`,
		`    location @pf_429-grpc {`,
		`      default_type application/grpc;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      add_header grpc-status 8 always;`,
		`      add_header grpc-message local_rate_limited always;`,
		`      return 200 "$pf_empty";`,
		`    }`,
		`    location @pf_429-grpc-body {`,
		`      default_type application/grpc;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header Access-Control-Allow-Origin $pf_acao1 always;`,
		`      add_header Access-Control-Expose-Headers $pf_aceh1 always;`,
		`      keepalive_timeout 0;`,
		`      add_header grpc-status 8 always;`,
		`      add_header grpc-message local_rate_limited always;`,
		`      return 200 "$pf_empty";`,
		`    }`,
	}
	without := []string{
		`    location @pf_429 {`,
		`      default_type text/plain;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      return 429 "local_rate_limited";`,
		`    }`,
		`    location @pf_429-body {`,
		`      default_type text/plain;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      keepalive_timeout 0;`,
		`      return 429 "local_rate_limited";`,
		`    }`,
		`    location @pf_429-connect {`,
		`      default_type application/json;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      return 429 '{"code":"resource_exhausted","message":"local_rate_limited"}';`,
		`    }`,
		`    location @pf_429-connect-body {`,
		`      default_type application/json;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      keepalive_timeout 0;`,
		`      return 429 '{"code":"resource_exhausted","message":"local_rate_limited"}';`,
		`    }`,
		`    location @pf_429-grpc {`,
		`      default_type application/grpc;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      add_header grpc-status 8 always;`,
		`      add_header grpc-message local_rate_limited always;`,
		`      return 200 "$pf_empty";`,
		`    }`,
		`    location @pf_429-grpc-body {`,
		`      default_type application/grpc;`,
		`      add_header x-request-id $pf_answer_id always;`,
		`      add_header Vary Origin always;`,
		`      keepalive_timeout 0;`,
		`      add_header grpc-status 8 always;`,
		`      add_header grpc-message local_rate_limited always;`,
		`      return 200 "$pf_empty";`,
		`    }`,
	}
	for _, c := range []struct {
		name string
		cors []string
		want []string
	}{
		{"a server with Access-Control lines", cors, withCORS},
		{"a server with none", nil, without},
	} {
		if got := limitedAnswers(c.cors); !slices.Equal(got, c.want) {
			t.Errorf("%s: %s", c.name, testsupport.FirstDifferenceMessage(strings.Join(got, "\n"), strings.Join(c.want, "\n")))
		}
	}
}

// TestRetriedCallsArePostOnly: a retried RPC route tries the next backend for a POST alone; any other method goes to
// a copy of its locations without the retry, from location / and from the check's location, and a file whose routes
// retry nothing writes neither.
func TestRetriedCallsArePostOnly(t *testing.T) {
	retried := configOf(t, trafficFile("connection", "", ", retries: 2", 2))
	if line := `  map "$pf_route:$request_method" $pf_rt_once { "~^(?:9|10)(?:-uri)?:(?!POST\\z)" -once; default ""; }`; !slices.Contains(strings.Split(retried, "\n"), line) {
		t.Errorf("no line %q", line)
	}
	if line := "      error_page 418 = @pf_r_$pf_route$pf_rt_once;"; !slices.Contains(namedLocation(retried, "/"), line) {
		t.Errorf("location / is %q, want %q in it", namedLocation(retried, "/"), line)
	}
	// A client's own request for the check's path goes on as location / sends it, so a call that is not a POST reaches
	// the copy that tries once there too.
	checked := configOf(t, strings.Replace(trafficFile("connection", "", ", retries: 2", 2), "services:\n", "services:\n  checks: {http: 9000}\n", 1)+
		"auth: {ask: checks /auth, timeout: 1s, enabled: true, when_unavailable: deny}\n")
	if line := "      error_page 418 = @pf_r_$pf_route$pf_rt_once;"; !slices.Contains(namedLocation(checked, "= /.proxyfile/check"), line) {
		t.Errorf("the check location is %q, want %q in it", namedLocation(checked, "= /.proxyfile/check"), line)
	}
	tries := []string{"      grpc_next_upstream error timeout;", "      grpc_next_upstream_tries 3;", "      grpc_pass grpc://shop_grpc;"}
	for _, name := range []string{"@pf_r_9", "@pf_r_9-uri", "@pf_r_10", "@pf_r_10-uri"} {
		if missing := testsupport.FirstMissingBlock(strings.Join(namedLocation(retried, name), "\n"), [][]string{tries}); missing != "" {
			t.Errorf("%s lacks:\n%s", name, missing)
		}
	}
	for _, name := range []string{"@pf_r_9-once", "@pf_r_9-uri-once", "@pf_r_10-once", "@pf_r_10-uri-once"} {
		location := namedLocation(retried, name)
		if !slices.Contains(location, tries[2]) || slices.ContainsFunc(location, func(line string) bool { return strings.Contains(line, "_next_upstream") }) {
			t.Errorf("%s is %q, want one that passes the call with no retry line", name, location)
		}
	}

	once := configOf(t, trafficFile("connection", "", ", retries: 0", 2))
	if strings.Contains(once, "$pf_rt_once") || strings.Contains(once, "-once {") {
		t.Error("a file whose routes retry nothing names $pf_rt_once or a -once location")
	}
	if line := "      error_page 418 = @pf_r_$pf_route;"; !slices.Contains(namedLocation(once, "/"), line) {
		t.Errorf("location / is %q, want %q in it", namedLocation(once, "/"), line)
	}
}

// manyCalls is a file whose rpc entry, retried once, has methods methods to one service of two backends, each its own
// route, numbered from 1 in the written order.
func manyCalls(methods int) string {
	text := "listen: 8000\nservices:\n  shop: {grpc: 8081, addresses: [\"10.0.1.11\", \"10.0.1.12\"]}\norigins:\n  local: {hosts: [localhost]}\n" +
		"routes:\n  - rpc: {to: {\"*\": shop}, retries: 1}\nrpc:\n  methods:\n"
	for n := 1; n <= methods; n++ {
		text += fmt.Sprintf("    - {service: example.shop.v1.S, method: M%d}\n", n)
	}
	return text
}

// TestManyRetriedCalls: the key that sends a retried call that is not a POST to -once names its routes in pieces of
// groupSize, each its own key, in the routes' order, so a file of any count of retried methods writes keys nginx
// reads: one key of 1,100 routes is 4,419 bytes, past the 4,093 nginx reads before a space. A file of few
// methods keeps one key.
func TestManyRetriedCalls(t *testing.T) {
	for _, c := range []struct {
		methods, keys int
	}{{3, 1}, {64, 1}, {65, 2}, {1100, 18}, {4096, 64}} {
		out := configOf(t, manyCalls(c.methods))
		line := regexp.MustCompile(`(?m)^  map "\$pf_route:\$request_method" \$pf_rt_once \{ (.*) default ""; \}$`).FindStringSubmatch(out)
		if line == nil {
			t.Fatalf("%d methods: no map sets $pf_rt_once from the route and the method", c.methods)
		}
		keys := regexp.MustCompile(`"~\^\(\?:([0-9|]+)\)\(\?:-uri\)\?:\(\?!POST\\\\z\)" -once;`).FindAllStringSubmatch(line[1], -1)
		var routes []string
		for _, key := range keys {
			piece := strings.Split(key[1], "|")
			if len(piece) > 64 {
				t.Errorf("%d methods: a key names %d routes, want at most 64", c.methods, len(piece))
			}
			routes = append(routes, piece...)
		}
		if len(keys) != c.keys || len(routes) != c.methods || routes[0] != "1" || routes[len(routes)-1] != strconv.Itoa(c.methods) {
			t.Errorf("%d methods: %d keys naming %d routes from %q to %q, want %d keys naming routes 1 to %d", c.methods, len(keys),
				len(routes), routes[0], routes[len(routes)-1], c.keys, c.methods)
		}
	}
}
