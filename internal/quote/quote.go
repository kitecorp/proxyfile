// Package quote writes text as a JSON string, the form the RPC table and the generated Envoy file both carry.
package quote

import (
	"encoding/json"
	"strings"
)

// JSON returns text as a JSON string, with <, > and & written as they are. json.Marshal has no such switch and
// always escapes those three.
func JSON(text string) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	// Encoding a string fails only when the writer does, and a strings.Builder never fails a write.
	_ = encoder.Encode(text)
	// Encode ends each value with a newline.
	return strings.TrimSuffix(out.String(), "\n")
}
