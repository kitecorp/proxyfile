
// headerNames is "0" when the name of every header of the request is RFC 9110's token characters, and "1" when one is
// not; nginx.conf refuses every value but "0", the empty value of a run that failed included. r.rawHeadersIn holds
// each name as the client sent it, which nginx keeps whole with ignore_invalid_headers off and underscores_in_headers
// on. The loop is a plain for, which njs's own engine reads; it reads no for...of.
function headerNames(r) {
    var raw = r.rawHeadersIn;
    for (var i = 0; i < raw.length; i++) {
        if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(raw[i][0])) {
            return "1";
        }
    }
    return "0";
}

export default {headerNames};
