package generator

import (
	"reflect"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/model"
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
