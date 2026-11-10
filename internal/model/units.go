package model

import (
	"fmt"
	"math"
	"net/netip"
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
