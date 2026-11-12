package model

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ParseSize, ParseTime, ParsePercent and ParseAgent compile SizePattern,
// TimeOrNonePattern, PercentPattern and AgentPattern, the same text the
// schema's "pattern" carries, so the schema and the parser accept the same
// forms. They differ in three places. No parser compiles TimePattern, and
// ParseTime accepts "none", so a field whose schema uses TimePattern relies on
// the schema check alone to refuse "none". A value can match its pattern yet
// overflow an int64, which only the parser refuses. And an agent can match its
// pattern, which takes digits and dots, and still hold an octet above 255, an
// octet with a leading zero or a port above 65535, which only ParseAgent
// refuses. The two time patterns share timeFragment so their digits and units
// cannot drift apart.
const (
	SizePattern       = `^[1-9][0-9]*(KiB|MiB|GiB)$`
	timeFragment      = `[1-9][0-9]*(ms|s|m|h)`
	TimePattern       = `^` + timeFragment + `$`
	TimeOrNonePattern = `^(none|` + timeFragment + `)$`
	PercentPattern    = `^(100|[1-9]?[0-9])%$`
	AgentPattern      = `^[0-9]{1,3}(\.[0-9]{1,3}){3}:[1-9][0-9]{0,4}$`
)

var (
	sizeExpression       = regexp.MustCompile(SizePattern)
	timeOrNoneExpression = regexp.MustCompile(TimeOrNonePattern)
	percentExpression    = regexp.MustCompile(PercentPattern)
	agentExpression      = regexp.MustCompile(AgentPattern)
)

var sizeFactors = map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}

var timeFactors = map[string]int64{"ms": 1, "s": 1000, "m": 60 * 1000, "h": 60 * 60 * 1000}

// ParseSize returns the size in bytes. It refuses a size whose byte count does
// not fit in an int64 instead of letting it wrap to a wrong or negative limit.
func ParseSize(text string) (bytes int64, ok bool) {
	match := sizeExpression.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	unit := match[1]
	return scale(strings.TrimSuffix(text, unit), sizeFactors[unit])
}

// ParseTime returns the time in milliseconds. "none" is 0: the caller decides
// what "none" means for the field it fills.
func ParseTime(text string) (millis int64, ok bool) {
	match := timeOrNoneExpression.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	if text == "none" {
		return 0, true
	}
	unit := match[2]
	return scale(strings.TrimSuffix(text, unit), timeFactors[unit])
}

func ParsePercent(text string) (percent int, ok bool) {
	match := percentExpression.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	percent, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return percent, true
}

// ParseAgent returns the IPv4 address and the UDP port of an agent written as address:port. The pattern refuses
// a port of 0 or one with a leading zero, which netip.ParseAddrPort would take; netip refuses what the pattern
// cannot, an octet above 255 or with a leading zero and a port above 65535.
func ParseAgent(text string) (address string, port int64, ok bool) {
	if !agentExpression.MatchString(text) {
		return "", 0, false
	}
	addrPort, err := netip.ParseAddrPort(text)
	if err != nil || !addrPort.Addr().Is4() {
		return "", 0, false
	}
	return addrPort.Addr().String(), int64(addrPort.Port()), true
}

// dnsLabel is one label of an RFC 1123 host name: letters, digits and -, at most 63 characters, neither starting nor
// ending with - (RFC 1123 section 2.1, which lets a label start with a digit, and RFC 1035 section 2.3.4).
var dnsLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// IsDNSName says whether text is an RFC 1123 host name, without a trailing dot and at most 253 characters. A name
// whose last label is all digits is refused, because RFC 1123 section 2.1 says a host name never has the dotted
// decimal form: a mistyped IPv4 address such as 10.0.0.256 is then neither an address nor a name, and is refused
// instead of being looked up.
func IsDNSName(text string) bool {
	if len(text) > 253 {
		return false
	}
	labels := strings.Split(text, ".")
	for _, label := range labels {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// ParseResolver returns the IP address and the port of a name server written as an address with an optional port,
// 53 when not written. An IPv6 address with a port takes brackets, as netip.ParseAddrPort reads it. Port 0 is
// refused: no name server answers there. A zone (fe80::1%eth0) is refused: Envoy 1.40.2 aborts on one in a resolver's
// address when it loads the file (measured).
func ParseResolver(text string) (address string, port int64, ok bool) {
	if addrPort, err := netip.ParseAddrPort(text); err == nil {
		if addrPort.Port() == 0 || addrPort.Addr().Zone() != "" {
			return "", 0, false
		}
		return addrPort.Addr().String(), int64(addrPort.Port()), true
	}
	if addr, err := netip.ParseAddr(text); err == nil && addr.Zone() == "" {
		return addr.String(), 53, true
	}
	return "", 0, false
}

// ParseEndpoint returns the host and the port of an endpoint written as host:port, where the host is an IP address or a
// DNS name; dns is true for a name, which the proxy looks up while it runs. An IPv6 address takes brackets, as
// netip.ParseAddrPort reads it, and host holds it without them. The port is always written, from 1 to 65535: no
// collector listens on port 0. A zone is refused, as ParseResolver refuses one.
func ParseEndpoint(text string) (host string, port int64, dns bool, ok bool) {
	if addrPort, err := netip.ParseAddrPort(text); err == nil {
		if addrPort.Port() == 0 || addrPort.Addr().Zone() != "" {
			return "", 0, false, false
		}
		return addrPort.Addr().String(), int64(addrPort.Port()), false, true
	}
	// A DNS name holds no colon, so the first colon ends it; a second one leaves the port unreadable.
	name, digits, found := strings.Cut(text, ":")
	number, err := strconv.ParseUint(digits, 10, 16)
	if !found || !IsDNSName(name) || err != nil || number == 0 {
		return "", 0, false, false
	}
	return name, int64(number), true, true
}

// ParseKeysURL returns the host and the port of an auth.jwt.keys URL: https, with no user part, and a host that is an IP
// address or a DNS name; dns is true for a name, which the proxy looks up while it runs. An IPv6 address takes
// brackets, which net/url requires, and host holds it without them. The port is 443 when not written, and otherwise
// from 1 to 65535. A zone is refused, as ParseResolver refuses one.
func ParseKeysURL(text string) (host string, port int64, dns bool, ok bool) {
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", 0, false, false
	}
	port = 443
	if digits := u.Port(); digits != "" {
		number, err := strconv.ParseUint(digits, 10, 16)
		if err != nil || number == 0 {
			return "", 0, false, false
		}
		port = int64(number)
	}
	host = u.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", 0, false, false
		}
		return addr.String(), port, false, true
	}
	if !IsDNSName(host) {
		return "", 0, false, false
	}
	return host, port, true, true
}

// ParseSize and ParseTime share scale so that one place refuses an int64
// overflow, both in the digits and in their product with the unit's factor.
func scale(digits string, factor int64) (int64, bool) {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n > math.MaxInt64/factor {
		return 0, false
	}
	return n * factor, true
}

// FormatSeconds writes a duration the way Envoy reads it: the ProtoJSON form
// of google.protobuf.Duration, which accepts any number of fractional digits
// down to nanoseconds, so trailing zeros are dropped. Integer arithmetic keeps
// the text exact, because a float64 cannot hold every int64 millisecond count.
// Go's / and % truncate toward zero, so for a negative count both parts carry
// the sign; it is written once in front and the parts are made positive. The
// parts are negated rather than millis itself, because -math.MinInt64 does not
// fit in an int64.
func FormatSeconds(millis int64) string {
	sign, seconds, rest := "", millis/1000, millis%1000
	if millis < 0 {
		sign, seconds, rest = "-", -seconds, -rest
	}
	if rest == 0 {
		return fmt.Sprintf("%s%ds", sign, seconds)
	}
	return fmt.Sprintf("%s%d.%ss", sign, seconds, strings.TrimRight(fmt.Sprintf("%03d", rest), "0"))
}
