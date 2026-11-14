package nginx

import (
	"strings"
	"testing"
)

// TestOverLong: the scan Generate ends with finds the first token longer than nginx reads from its file, measured on
// nginx 1.30.6 and 1.31.8: 4094 bytes between quotes, a word of 4095 bytes and a comment of 4095 bytes from its #, each
// counted as the file writes it, a quote's backslash included.
func TestOverLong(t *testing.T) {
	cases := []struct {
		name, text string
		want       int // the line of the first token longer than nginx reads, 0 for none
	}{
		{"4094 bytes in quotes", `x "` + strings.Repeat("a", 4094) + `";`, 0},
		{"4095 bytes in quotes", `x "` + strings.Repeat("a", 4095) + `";`, 1},
		{"an escaped quote counted as written", `x "` + strings.Repeat("a", 4093) + `\"";`, 1},
		{"a word of 4095 bytes", "x " + strings.Repeat("a", 4095) + ";", 0},
		{"a word of 4096 bytes", "x " + strings.Repeat("a", 4096) + ";", 1},
		{"a comment of 4095 bytes", "#" + strings.Repeat("a", 4094) + "\n", 0},
		{"a comment of 4096 bytes", "#" + strings.Repeat("a", 4095) + "\n", 1},
		{"a long token on a later line", "a;\n\"b\nc\";\nx " + strings.Repeat("a", 4096) + ";\n", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := overLong(c.text); got != c.want {
				t.Errorf("overLong = %d, want %d", got, c.want)
			}
		})
	}
}
