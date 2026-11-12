package model

import (
	"math"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		text   string
		want   int64
		wantOK bool
	}{
		{"192MiB", 201326592, true},
		{"128KiB", 131072, true},
		{"1GiB", 1073741824, true},
		{"192MB", 0, false},
		{"0MiB", 0, false},
		{"192", 0, false},
		{"1.5MiB", 0, false},
		{"9999999999999999GiB", 0, false},
		{"8589934591GiB", 9223372035781033984, true},
		{"8589934592GiB", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseSize(c.text)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ParseSize(%q) = %d, %t, want %d, %t", c.text, got, ok, c.want, c.wantOK)
		}
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		text   string
		want   int64
		wantOK bool
	}{
		{"125s", 125000, true},
		{"1500ms", 1500, true},
		{"2m", 120000, true},
		{"1h", 3600000, true},
		{"none", 0, true},
		{"0s", 0, false},
		{"2 s", 0, false},
		{"1.5s", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseTime(c.text)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ParseTime(%q) = %d, %t, want %d, %t", c.text, got, ok, c.want, c.wantOK)
		}
	}
}

func TestParsePercent(t *testing.T) {
	cases := []struct {
		text   string
		want   int
		wantOK bool
	}{
		{"1%", 1, true},
		{"0%", 0, true},
		{"100%", 100, true},
		{"101%", 0, false},
		{"1.5%", 0, false},
		{"1", 0, false},
		{"01%", 0, false},
	}
	for _, c := range cases {
		got, ok := ParsePercent(c.text)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ParsePercent(%q) = %d, %t, want %d, %t", c.text, got, ok, c.want, c.wantOK)
		}
	}
}

func TestParseAgent(t *testing.T) {
	cases := []struct {
		text, address string
		port          int64
		ok            bool
	}{
		{"127.0.0.1:2000", "127.0.0.1", 2000, true},
		{"10.0.0.9:1", "10.0.0.9", 1, true},
		{"255.255.255.255:65535", "255.255.255.255", 65535, true},
		{"300.1.2.3:2000", "", 0, false},
		{"127.0.0.01:2000", "", 0, false},
		{"127.0.0.1:65536", "", 0, false},
		{"127.0.0.1:0", "", 0, false},
		{"127.0.0.1:02000", "", 0, false},
		{"127.0.0.1", "", 0, false},
		{"[::1]:2000", "", 0, false},
		{"localhost:2000", "", 0, false},
	}
	for _, c := range cases {
		address, port, ok := ParseAgent(c.text)
		if address != c.address || port != c.port || ok != c.ok {
			t.Errorf("ParseAgent(%q) = %q, %d, %t, want %q, %d, %t", c.text, address, port, ok, c.address, c.port, c.ok)
		}
	}
}

func TestFormatSeconds(t *testing.T) {
	cases := []struct {
		millis int64
		want   string
	}{
		{125000, "125s"},
		{1500, "1.5s"},
		{250, "0.25s"},
		{1, "0.001s"},
		{0, "0s"},
		{-1500, "-1.5s"},
		{-500, "-0.5s"},
		{math.MinInt64, "-9223372036854775.808s"},
	}
	for _, c := range cases {
		if got := FormatSeconds(c.millis); got != c.want {
			t.Errorf("FormatSeconds(%d) = %q, want %q", c.millis, got, c.want)
		}
	}
}

// A DNS name is an RFC 1123 host name: labels of letters, digits and - that neither start nor end with -, at most 63
// characters each and 253 in all, with no trailing dot and a last label that is not all digits (section 2.1), so a
// mistyped IPv4 address is neither an address nor a name.
func TestIsDNSName(t *testing.T) {
	label61 := strings.Repeat("a", 61)
	cases := []struct {
		text string
		want bool
	}{
		{"catalog", true}, {"catalog.internal.example", true}, {"1st.example", true}, {"a-b.example", true},
		{strings.Repeat("a", 63) + ".example", true}, {strings.Repeat("a", 64) + ".example", false},
		{strings.Repeat(label61+".", 4) + "abcde", true}, {strings.Repeat(label61+".", 4) + "abcdef", false},
		{"localhost:3000", false}, {"10.0.0.256", false}, {"-a.example", false}, {"a-.example", false},
		{"a..example", false}, {"", false}, {"catalog.example.", false}, {"x_y.example", false},
	}
	for _, c := range cases {
		if got := IsDNSName(c.text); got != c.want {
			t.Errorf("IsDNSName(%q) = %t, want %t", c.text, got, c.want)
		}
	}
}

// A resolver is an IP address with an optional port, 53 when not written; IPv6 takes brackets with a port.
func TestParseResolver(t *testing.T) {
	cases := []struct {
		text, address string
		port          int64
		ok            bool
	}{
		{"10.0.0.2:53", "10.0.0.2", 53, true}, {"10.0.0.2", "10.0.0.2", 53, true},
		{"[fd00::53]:5353", "fd00::53", 5353, true}, {"fd00::53", "fd00::53", 53, true},
		{"10.0.0.2:0", "", 0, false}, {"10.0.0.2:65536", "", 0, false}, {"10.0.0.256", "", 0, false},
		{"ns.example", "", 0, false}, {"ns.example:53", "", 0, false},
		{"fe80::1%eth0", "", 0, false}, {"[fe80::1%eth0]:53", "", 0, false},
	}
	for _, c := range cases {
		address, port, ok := ParseResolver(c.text)
		if address != c.address || port != c.port || ok != c.ok {
			t.Errorf("ParseResolver(%q) = %q, %d, %t, want %q, %d, %t", c.text, address, port, ok, c.address, c.port, c.ok)
		}
	}
}

// An endpoint is an IP address or a DNS name, and a port from 1 to 65535 that is always written; IPv6 takes brackets
// and no zone, and dns says the host is a name the proxy looks up.
func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		text, host string
		port       int64
		dns, ok    bool
	}{
		{"127.0.0.1:4317", "127.0.0.1", 4317, false, true}, {"[fd00::40]:4317", "fd00::40", 4317, false, true},
		{"otel-collector:4317", "otel-collector", 4317, true, true}, {"collector", "", 0, false, false},
		{"fd00::40:4317", "", 0, false, false}, {"127.0.0.1:0", "", 0, false, false}, {"127.0.0.1:65536", "", 0, false, false},
		{"10.0.0.256:4317", "", 0, false, false}, {"[fe80::1%eth0]:4317", "", 0, false, false},
	}
	for _, c := range cases {
		host, port, dns, ok := ParseEndpoint(c.text)
		if host != c.host || port != c.port || dns != c.dns || ok != c.ok {
			t.Errorf("ParseEndpoint(%q) = %q, %d, %t, %t, want %q, %d, %t, %t", c.text, host, port, dns, ok, c.host, c.port, c.dns, c.ok)
		}
	}
}

// A keys URL is https, with no user part, and its host an IP address (IPv6 in brackets, no zone) or a DNS name; its
// port is 443 when not written, and dns says the host is a name the proxy looks up.
func TestParseKeysURL(t *testing.T) {
	cases := []struct {
		text, host string
		port       int64
		dns, ok    bool
	}{
		{"https://login.example.com/.well-known/jwks.json", "login.example.com", 443, true, true},
		{"https://10.0.3.50:8443/jwks.json", "10.0.3.50", 8443, false, true}, {"https://[fd00::50]/jwks.json", "fd00::50", 443, false, true},
		{"https://", "", 0, false, false}, {"https://user@login.example.com/k", "", 0, false, false},
		{"https://login.example.com:0/k", "", 0, false, false}, {"https://10.0.0.256/k", "", 0, false, false},
		{"http://login.example.com/k", "", 0, false, false},
	}
	for _, c := range cases {
		host, port, dns, ok := ParseKeysURL(c.text)
		if host != c.host || port != c.port || dns != c.dns || ok != c.ok {
			t.Errorf("ParseKeysURL(%q) = %q, %d, %t, %t, want %q, %d, %t, %t", c.text, host, port, dns, ok, c.host, c.port, c.dns, c.ok)
		}
	}
}
