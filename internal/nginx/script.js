// read holds the headers nginx.conf reads through an $http_ variable that takes a name written with _ for - as well
// (underscores_in_headers on): x_request_id would reach the service as its x-request-id, and
// access_control_request_method would make a preflight, where Envoy reads such a name as another header. A name with no
// - has no other spelling, and nginx's own $http_x_forwarded_for reads only the name written with -, so neither is here
// (measured on nginx 1.30.6 and 1.31.8). once holds the three forwarding headers the routes send, whose two lines
// reached the service as two lines or joined with ", " through nginx, where Envoy joins them with "," (measured on
// nginx 1.30.6 and 1.31.8 beside Envoy 1.40.2), as one $http_ variable reads both lines; and three headers that hold
// one value, not a list, whose two lines nginx passes as two lines, where Envoy joins them into one value, so a service
// that reads the first line would read another value through nginx. The configuration cannot tell the spellings or the
// lines apart, so headerNames refuses both. Each table is read with === 1: every object inherits properties such as
// constructor and __proto__, which a name could otherwise find.
var read = {"access-control-request-method": 1, "access-control-request-headers": 1, "x-request-id": 1,
    "x-forwarded-proto": 1, "http2-settings": 1, "keep-alive": 1, "proxy-connection": 1, "transfer-encoding": 1};
var once = {"x-forwarded-for": 1, "x-forwarded-proto": 1, "x-request-id": 1, "content-type": 1, "user-agent": 1,
    "referer": 1};

// headerNames is "0" when the name of every header of the request is RFC 9110's token characters, no value holds a
// control byte other than a tab, none is a name of read written with _, and no name of once comes twice, and "1"
// otherwise; nginx.conf refuses every value but "0", the empty value of a run that failed included. nginx passes a
// value holding 0x01 to 0x08, 0x0B, 0x0C, 0x0E to 0x1F or 0x7F to the service over HTTP/1.1 and HTTP/2, where Envoy
// answers 400 over HTTP/1.1 and closes the connection over HTTP/2 (measured on nginx 1.30.6 and 1.31.8 beside Envoy
// 1.40.2); nginx refuses 0x00 and a carriage return itself, and over HTTP/1.1 a line feed ends the line. r.rawHeadersIn
// holds each name as the client sent it, which nginx keeps whole with ignore_invalid_headers off and
// underscores_in_headers on. The loop is a plain for, which njs's own engine reads; it reads no for...of.
function headerNames(r) {
    var raw = r.rawHeadersIn;
    var seen = {};
    for (var i = 0; i < raw.length; i++) {
        var name = raw[i][0];
        if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name)) {
            return "1";
        }
        if (/[\x00-\x08\x0A-\x1F\x7F]/.test(raw[i][1])) {
            return "1";
        }
        var low = name.toLowerCase();
        if (once[low] === 1 && seen[low] === 1) {
            return "1";
        }
        seen[low] = 1;
        if (name.indexOf("_") >= 0 && read[low.replace(/_/g, "-")] === 1) {
            return "1";
        }
    }
    return "0";
}

// origin is the request's Origin as the Envoy output reads it, which nginx's $http_origin cannot give: "n" when the
// request has no Origin line; "e" when every line is empty; otherwise "o" and the lines that are not empty, joined with
// a comma in the order sent. Over HTTP/1 each line is read without the spaces and tabs around it, as Envoy's HTTP/1
// parser reads it, where nginx keeps a tab; over HTTP/2 each is read as sent, as Envoy's codec reads it (measured on
// Envoy 1.40.2 beside nginx 1.30.6 and 1.31.8). nginx.conf refuses "e", and the empty value of a run that failed.
function origin(r) {
    var raw = r.rawHeadersIn;
    var present = false;
    var values = [];
    for (var i = 0; i < raw.length; i++) {
        if (raw[i][0].toLowerCase() != "origin") {
            continue;
        }
        present = true;
        var value = raw[i][1];
        if (r.httpVersion != "2.0") {
            value = value.replace(/^[ \t]+|[ \t]+$/g, "");
        }
        if (value != "") {
            values.push(value);
        }
    }
    if (!present) {
        return "n";
    }
    if (values.length == 0) {
        return "e";
    }
    return "o" + values.join(",");
}

export default {headerNames, origin};
