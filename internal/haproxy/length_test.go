package haproxy

import (
	"strings"
	"testing"
)

// TestOverLong: the scan Generate ends with finds the first line with more words than HAProxy reads, 65, measured on
// HAProxy 3.2.26 and 3.4.7: a word ends at a space or a tab outside quotes, a quoted value is one word with its quotes,
// a ' closed and opened again around an escaped ' joins one word, and a # outside quotes starts a comment.
func TestOverLong(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("w ", n)) }
	cases := []struct {
		name, text string
		want       int // the line of the first line over, 0 for none
	}{
		{"65 words", words(65), 0},
		{"66 words", words(66), 1},
		{"a quoted value with spaces is one word", words(64) + " 'a b c'", 0},
		{"a quote closed around an escaped quote is one word", words(64) + ` 'it'\''s here'`, 0},
		{"a comment's words are not read", words(60) + " # " + words(10), 0},
		{"a # inside quotes is no comment", words(64) + " '#' w", 1},
		{"a line over after shorter ones", "a\n  " + words(3) + "\n" + words(66) + "\n", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := overLong(c.text); got != c.want {
				t.Errorf("overLong = %d, want %d", got, c.want)
			}
		})
	}
}
