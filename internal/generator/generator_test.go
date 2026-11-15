package generator

import (
	"reflect"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/model"
	"github.com/kitecorp/proxyfile/v2/internal/rpc"
)

// The headers removed from a client's request: remove_from_clients as written, then the check's question headers and
// accept names, then the claims headers, each that no entry before it covers.
func TestRemovedFromClients(t *testing.T) {
	cases := []struct {
		name string
		g    model.Gateway
		want []model.Header
	}{
		{name: "remove_from_clients alone", g: model.Gateway{RemoveFromClients: []model.Header{{Name: "x-client-", Prefix: true}}},
			want: []model.Header{{Name: "x-client-", Prefix: true}}},
		{name: "with auth.ask, the question headers and the accept names after it",
			g:    model.Gateway{RemoveFromClients: []model.Header{{Name: "x-debug"}}, Auth: &model.Auth{Accept: []model.Header{{Name: "x-user-id"}}}},
			want: []model.Header{{Name: "x-debug"}, {Name: "x-gateway-action"}, {Name: "x-gateway-reports"}, {Name: "x-user-id"}}},
		{name: "an entry that covers a header adds nothing for it",
			g:    model.Gateway{RemoveFromClients: []model.Header{{Name: "x-gateway-", Prefix: true}}, Auth: &model.Auth{Accept: []model.Header{{Name: "x-user-id"}}}},
			want: []model.Header{{Name: "x-gateway-", Prefix: true}, {Name: "x-user-id"}}},
		{name: "with auth.jwt, each claims header",
			g:    model.Gateway{JWT: &model.JWT{Claims: []model.Claim{{Header: "x-subject", Name: "sub"}}}},
			want: []model.Header{{Name: "x-subject"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RemovedFromClients(&c.g); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RemovedFromClients = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The names the auth check's generators share, each the literal the Envoy output uses for the same request or answer.
func TestCheckNames(t *testing.T) {
	cases := []struct {
		name      string
		got, want any
	}{
		{"UncheckedMark", UncheckedMark, "x-envoy-auth-failure-mode-allowed"},
		{"QuestionHeaders", QuestionHeaders, []model.Header{{Name: "x-gateway-action"}, {Name: "x-gateway-reports"}}},
		{"FramingHeaders", FramingHeaders, []string{"host", "content-length", "transfer-encoding", "connection", "keep-alive",
			"te", "trailer", "upgrade", "expect", "proxy-connection"}},
		{"HopHeaders", HopHeaders, []string{"content-length", "transfer-encoding", "connection", "keep-alive", "upgrade",
			"proxy-connection"}},
		{"GRPCStatus", GRPCStatus, map[int]int{400: 13, 401: 16, 403: 7, 404: 12, 429: 14, 502: 14, 503: 14, 504: 14}},
		{"InternalHeader", InternalHeader, "x-envoy-internal"},
		{"ExpectedTimeoutHeader", ExpectedTimeoutHeader, "x-envoy-expected-rq-timeout-ms"},
		{"DroppedByEnvoy", DroppedByEnvoy, []string{"keep-alive"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !reflect.DeepEqual(c.got, c.want) {
				t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
			}
		})
	}
}

// The RPC question of each method that has an action or reports: its path with both, in the order of in.RPC, and none
// when the check is not asked.
func TestQuestions(t *testing.T) {
	// cart is a method of example.shop.v1.CartService; no reports leaves Reports nil, as rpc reads a method without them.
	cart := func(method, action string, reports ...string) rpc.Route {
		return rpc.Route{Method: rpc.Method{Service: "example.shop.v1.CartService", Method: method, Action: action, Reports: reports}}
	}
	asked := &model.Gateway{Auth: &model.Auth{Enabled: true}}
	getCart := Question{Path: "/example.shop.v1.CartService/GetCart", Action: "browse"}
	addItem := Question{Path: "/example.shop.v1.CartService/AddItem", Action: "buy", Reports: []string{"browse"}}
	cases := []struct {
		name string
		in   Input
		want []Question
	}{
		{"no auth", Input{Gateway: &model.Gateway{}, RPC: []rpc.Route{cart("GetCart", "browse")}}, nil},
		{"auth not enabled", Input{Gateway: &model.Gateway{Auth: &model.Auth{}}, RPC: []rpc.Route{cart("GetCart", "browse")}}, nil},
		{"a method with an action", Input{Gateway: asked, RPC: []rpc.Route{cart("GetCart", "browse")}}, []Question{getCart}},
		{"a method with an action and reports", Input{Gateway: asked, RPC: []rpc.Route{cart("AddItem", "buy", "browse")}},
			[]Question{addItem}},
		{"a method with reports alone", Input{Gateway: asked, RPC: []rpc.Route{cart("ListItems", "", "browse")}},
			[]Question{{Path: "/example.shop.v1.CartService/ListItems", Reports: []string{"browse"}}}},
		{"a method with neither", Input{Gateway: asked, RPC: []rpc.Route{cart("RemoveItem", "")}}, nil},
		{"the order of in.RPC is kept",
			Input{Gateway: asked, RPC: []rpc.Route{cart("GetCart", "browse"), cart("RemoveItem", ""), cart("AddItem", "buy", "browse")}},
			[]Question{getCart, addItem}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Questions(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Questions = %+v, want %+v", got, c.want)
			}
		})
	}
}

// One bucket for each limited entry of routes, in the written order: the route's name or rpc, requests + burst tokens,
// refilled by requests each per.
func TestBuckets(t *testing.T) {
	second := model.Time{Millis: 1000}
	g := model.Gateway{Routes: []model.Route{
		{Name: model.Text{Value: "catalog"}, RateLimit: &model.RateLimit{Requests: 20, Burst: 10, Per: second, ByClient: true}},
		{RPC: &model.RPC{RateLimit: &model.RateLimit{Requests: 100, Per: second}}},
		{Name: model.Text{Value: "shop"}},
		{Name: model.Text{Value: "login"}, RateLimit: &model.RateLimit{Requests: 5, Per: model.Time{Millis: 60000}, ByClient: true}},
	}}
	want := []Bucket{
		{ID: "catalog", ByClient: true, MaxTokens: 30, TokensPerFill: 20, FillInterval: "1s"},
		{ID: "rpc", MaxTokens: 100, TokensPerFill: 100, FillInterval: "1s"},
		{ID: "login", ByClient: true, MaxTokens: 5, TokensPerFill: 5, FillInterval: "60s"},
	}
	if got := Buckets(&g); !reflect.DeepEqual(got, want) {
		t.Errorf("Buckets = %+v, want %+v", got, want)
	}
}

// The values a generator refuses as it writes them: each once by its key and position, however many places the file
// writes it, in the file's order, in the words a target's declaration refuses a form with; nothing refused is no error.
func TestRefusals(t *testing.T) {
	at := func(line, column int) model.Pos { return model.Pos{File: "p.yaml", Line: line, Column: column} }
	type refusal struct {
		pos                        model.Pos
		key, form, target, instead string
	}
	prefix := refusal{at(7, 34), "routes[].prefix", "whose nginx form is too long", "nginx", "write a shorter prefix"}
	host := refusal{at(5, 3), "origins.<environment name>.hosts", "whose regular expression is too long", "haproxy", "write a shorter host"}
	// tls.certificate and tls.key are both refused at the tls key, the one position a generator has for its paths.
	certificate := refusal{at(8, 1), "tls.certificate", "whose nginx form is too long", "nginx", "write a shorter path"}
	key := refusal{at(8, 1), "tls.key", "whose nginx form is too long", "nginx", "write a shorter path"}
	const (
		prefixLine      = `p.yaml:7:34: "routes[].prefix" whose nginx form is too long is refused by --target nginx: write a shorter prefix`
		hostLine        = `p.yaml:5:3: "origins.<environment name>.hosts" whose regular expression is too long is refused by --target haproxy: write a shorter host`
		certificateLine = `p.yaml:8:1: "tls.certificate" whose nginx form is too long is refused by --target nginx: write a shorter path`
		keyLine         = `p.yaml:8:1: "tls.key" whose nginx form is too long is refused by --target nginx: write a shorter path`
	)
	cases := []struct {
		name    string
		refused []refusal
		want    []string // the lines of the Refused error, nil for no error
	}{
		{"nothing refused is no error", nil, nil},
		{"a value written at two places is refused once", []refusal{prefix, prefix}, []string{prefixLine}},
		{"the refusals are in the file's order", []refusal{prefix, host}, []string{hostLine, prefixLine}},
		{"two keys at one position are refused each", []refusal{key, certificate, key}, []string{certificateLine, keyLine}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var refusals Refusals
			for _, r := range c.refused {
				refusals.Refuse(r.pos, r.key, r.form, r.target, r.instead)
			}
			err := refusals.Err()
			if c.want == nil {
				if err != nil {
					t.Fatalf("Err() = %v, want nil", err)
				}
				return
			}
			refused, ok := err.(Refused)
			if !ok {
				t.Fatalf("Err() = %#v, want a Refused", err)
			}
			var got []string
			for _, problem := range refused.Problems {
				got = append(got, problem.String())
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Err() lines = %q, want %q", got, c.want)
			}
		})
	}
}
