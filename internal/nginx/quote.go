package nginx

import "strings"

// quoter writes \ as \\ and " as \". nginx reads \", \' and \\ as the character after the backslash, \t, \n and \r as
// control characters, and keeps any other backslash as it is (ngx_conf_read_token in src/core/ngx_conf_file.c).
// Escaping every backslash is the one way a text never meets the three control escapes: a regular expression's \t or
// \n reaches PCRE as written only when the file holds \\t or \\n.
var quoter = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// quoted is text as nginx reads it back from the file: in double quotes, so a space, ;, { or # in it is not read as
// the end of the value, a block or a comment.
func quoted(text string) string {
	return `"` + quoter.Replace(text) + `"`
}

// regexKey is a map key that matches the case-sensitive regular expression re. A map key that does not start with ~
// is a string, which a map compares ignoring case, and one that starts with ~* is a regular expression that ignores
// case (https://nginx.org/en/docs/http/ngx_http_map_module.html), while Envoy compares a path with its case.
func regexKey(re string) string {
	return quoted("~" + re)
}
