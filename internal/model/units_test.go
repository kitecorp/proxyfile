package model

import (
	"math"
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
