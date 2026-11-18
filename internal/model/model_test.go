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

// TestWholeRate: a rate is a whole number of requests a second, or, when a second gives none, a minute, and none when
// neither is whole or the period is not above 0.
func TestWholeRate(t *testing.T) {
	for _, c := range []struct {
		requests, millis int64
		count            int64
		unit             string
		ok               bool
	}{
		{5, 1000, 5, "s", true},
		{4, 2000, 2, "s", true},
		{3, 2000, 90, "m", true},
		{1, 60000, 1, "m", true},
		{120, 60000, 2, "s", true},
		{1, 7000, 0, "", false},
		{4294967295, 1000, 4294967295, "s", true},
		{1, 0, 0, "", false},
	} {
		count, unit, ok := WholeRate(c.requests, c.millis)
		if count != c.count || unit != c.unit || ok != c.ok {
			t.Errorf("WholeRate(%d, %d) = %d, %q, %t, want %d, %q, %t", c.requests, c.millis, count, unit, ok, c.count, c.unit, c.ok)
		}
	}
}
