package haproxy

import "strings"

// quote writes text as one single-quoted HAProxy argument. Inside single quotes HAProxy reads every character as
// written, a space, #, ", \, $ and braces included, and no quote can be escaped, so each ' closes the quotes, adds an
// escaped ' and opens them again (measured on HAProxy 3.2.26 and 3.4.7).
func quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// logFormat quotes text for an argument HAProxy reads as a log-format string, such as a header's value, where % starts
// a sample and %% is one %. A \ is no escape there and stays as written (measured on HAProxy 3.2.26 and 3.4.7).
func logFormat(text string) string {
	return quote(strings.ReplaceAll(text, "%", "%%"))
}

// rewriteFormat is the replacement of replace-path for a route's rewrite: the rewrite, then \1, the rest of the path
// after the matched prefix or path. The replacement is a log-format string whose \ also escapes, as \1 shows, so the
// rewrite's own \ is written \\ before its % is written %% (measured on HAProxy 3.2.26 and 3.4.7).
func rewriteFormat(rewrite string) string {
	return logFormat(strings.ReplaceAll(rewrite, `\`, `\\`) + `\1`)
}
