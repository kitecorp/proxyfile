package model

import "testing"

func TestServiceCluster(t *testing.T) {
	shop := Service{Name: "shop"}
	cases := []struct {
		protocol Protocol
		want     string
	}{
		{HTTP, "shop_http"},
		{GRPC, "shop_grpc"},
	}
	for _, c := range cases {
		if got := shop.Cluster(c.protocol); got != c.want {
			t.Errorf("Cluster(%q) = %q, want %q", c.protocol, got, c.want)
		}
	}
}
