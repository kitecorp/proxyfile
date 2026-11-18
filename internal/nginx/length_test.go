package nginx

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestOverLong: the scan Generate ends with finds the first token longer than nginx reads from its file, measured on
// nginx 1.30.6 and 1.31.8: 4094 bytes between quotes, a word of 4095 bytes and a comment of 4095 bytes from its #, each
// counted as the file writes it, a quote's backslash included; a token a space follows, such as a map's key, one byte
// less: 4093 bytes between quotes and a word of 4094 (each loaded wherever it met nginx's read buffer, and one byte
// more refused).
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
		{"4093 bytes in quotes before a space", `x { "` + strings.Repeat("a", 4093) + `" 1; }`, 0},
		{"4094 bytes in quotes before a space", `x { "` + strings.Repeat("a", 4094) + `" 1; }`, 1},
		{"a word of 4094 bytes before a space", "x { " + strings.Repeat("a", 4094) + " 1; }", 0},
		{"a word of 4095 bytes before a space", "x { " + strings.Repeat("a", 4095) + " 1; }", 1},
		{"a word of 4095 bytes before a line break", "x\n" + strings.Repeat("a", 4095) + "\n;", 2},
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

// TestDeclared: the variables table counts each variable the http block declares once: each one a map, js_set, js_var,
// set, geo or split_clients line names, and each named capture of a regular expression; a variable a line reads, a
// lookbehind and a comment declare none.
func TestDeclared(t *testing.T) {
	text := strings.Join([]string{
		`  map "$host:$uri" $pf_a { "~^(?<pf_b>[a-z]+):" $pf_b; "~(?<=x)y" 1; default ""; }`,
		`  map $request_uri $pf_c { default 0; }`,
		"  js_set $pf_d pf.d;",
		"  js_var $pf_e;",
		`  geo $pf_f { default "$"; }`,
		"  split_clients $pf_request_id $pf_g { 1% 1; * 0; }",
		"    set $pf_h 1;",
		"      set $pf_h 2;",
		"      proxy_set_header Host $pf_i;",
		"  # map $x $pf_j { }",
		`  map $pf_a $pf_c { default 1; }`,
	}, "\n")
	got := slices.Sorted(maps.Keys(declared(text)))
	if want := []string{"pf_a", "pf_b", "pf_c", "pf_d", "pf_e", "pf_f", "pf_g", "pf_h"}; !slices.Equal(got, want) {
		t.Errorf("declared = %q, want %q", got, want)
	}
	if got, want := variablesHash(text), []string{"  variables_hash_bucket_size 128;", "  variables_hash_max_size 2048;"}; !slices.Equal(got, want) {
		t.Errorf("variablesHash = %q, want %q", got, want)
	}
}
