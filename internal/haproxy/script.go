package haproxy

import (
	_ "embed"
	"fmt"
	"maps"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/generator"
)

// scriptName is the script beside the configuration, which its global section loads by this name from its own
// folder (default-path config). Its bytes are the same for every file (script), so two files rendered into one folder
// share it.
const scriptName = "proxyfile.lua"

//go:embed script.lua
var scriptBody string

// script is the script beside the configuration: generator.Header as a Lua comment, the headers whose lines pf_join
// joins (generator.JoinedByEnvoy), the bytes pf_escape escapes (generator.EscapedInPath), the lists pf_auth reads
// (generator.FramingHeaders, generator.HopHeaders, format.HAProxyFramedBy with generator.DroppedByEnvoy, and
// generator.DroppedByEnvoy alone), Envoy's gRPC statuses (generator.GRPCStatus and generator.GRPCUnknown), the mark of an unanswered check
// (generator.UncheckedMark), the two headers every check is sent as Envoy sends them (generator.InternalHeader and
// generator.ExpectedTimeoutHeader), and the functions.
func script() []byte {
	var joined strings.Builder
	for _, name := range generator.JoinedByEnvoy {
		fmt.Fprintf(&joined, "  %s,\n", luaString(name))
	}
	return []byte("-- " + strings.TrimPrefix(generator.Header, "# ") + "local joined = {\n" + joined.String() + "}\n" +
		"local escaped = " + luaString(luaClass(generator.EscapedInPath)) + "\n" +
		"local framing = " + luaSet(generator.FramingHeaders) + "\n" +
		"local hop = " + luaSet(generator.HopHeaders) + "\n" +
		"local never_accepted = " + luaSet(slices.Concat(format.HAProxyFramedBy, generator.DroppedByEnvoy)) + "\n" +
		"local dropped = " + luaSet(generator.DroppedByEnvoy) + "\n" +
		"local grpc_status, grpc_unknown = " + luaStatuses(generator.GRPCStatus) + ", " +
		strconv.Itoa(generator.GRPCUnknown) + "\n" +
		"local unchecked = " + luaString(generator.UncheckedMark) + "\n" +
		"local internal_header, expected_header = " + luaString(generator.InternalHeader) + ", " +
		luaString(generator.ExpectedTimeoutHeader) + "\n\n" + scriptBody)
}

// luaSet writes names as a Lua table that holds true at each of them, in their order.
func luaSet(names []string) string {
	keys := make([]string, len(names))
	for i, name := range names {
		keys[i] = luaKey(name) + " = true"
	}
	return "{ " + strings.Join(keys, ", ") + " }"
}

// luaStatuses writes Envoy's gRPC status for each HTTP status as a Lua table, in the order of the HTTP statuses, so
// the script is the same bytes every time.
func luaStatuses(statuses map[int]int) string {
	codes := slices.Sorted(maps.Keys(statuses))
	keys := make([]string, len(codes))
	for i, code := range codes {
		keys[i] = fmt.Sprintf("[%d] = %d", code, statuses[code])
	}
	return "{ " + strings.Join(keys, ", ") + " }"
}

// luaIdentifier is a name Lua reads as a bare key of a table, unless it is one of luaKeywords (the Lua 5.4 manual,
// section 3.1).
var luaIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var luaKeywords = map[string]bool{"and": true, "break": true, "do": true, "else": true, "elseif": true, "end": true,
	"false": true, "for": true, "function": true, "goto": true, "if": true, "in": true, "local": true, "nil": true,
	"not": true, "or": true, "repeat": true, "return": true, "then": true, "true": true, "until": true, "while": true}

// luaKey writes name as a key of a Lua table: bare when Lua reads it so, a string in brackets otherwise.
func luaKey(name string) string {
	if luaIdentifier.MatchString(name) && !luaKeywords[name] {
		return name
	}
	return "[" + luaString(name) + "]"
}

// longestProgram is the most bytes of a pattern's program the file writes. HAProxy 3.2.26 and 3.4.7 loaded an
// argument of 200,000 bytes. A state of the script's machine holds at most the program's instructions, and counts
// them against the budget all the machines of an HAProxy thread share (script.lua, mostHeld, about 1 MB a thread, and
// HAProxy that times its threads), so a long program fills that budget sooner and never passes it by more than two
// states. Envoy 1.40.2 refuses an RE2 program larger than 100 instructions, far below it.
const longestProgram = 65536

// program is the text of the program Go's regexp/syntax compiles pattern to, as Go's regexp runs it, which pf_route
// reads (script.lua, decode): "s" and the number of the first instruction, then each instruction, numbered from 1.
// Envoy matches a pattern with RE2, which never gives up on a path; HAProxy's PCRE2 gives up on some paths (measured:
// ^/g/((a|aa)+c|a+b)$ on /g/ and 40 a's then b), and reads that as no match, so the route after it took the request.
// Go's regexp reads a pattern as RE2 does. A byte read holds the bytes from 0 to 127 it takes, as ranges: the path it
// reads holds no byte above 0x7F, which pf_escape has escaped. Its error means the pattern does not compile, which
// the reader refuses first: a bug.
func program(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	compiled, err := syntax.Compile(re.Simplify())
	if err != nil {
		return "", err
	}
	parts := []string{fmt.Sprintf("s%d", compiled.Start+1)}
	for _, instruction := range compiled.Inst {
		out := instruction.Out + 1
		switch instruction.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			parts = append(parts, fmt.Sprintf("a%d,%d", out, instruction.Arg+1))
		case syntax.InstCapture, syntax.InstNop:
			parts = append(parts, fmt.Sprintf("n%d", out))
		case syntax.InstEmptyWidth:
			parts = append(parts, fmt.Sprintf("e%d,%d", out, instruction.Arg))
		case syntax.InstMatch:
			parts = append(parts, "m")
		case syntax.InstFail:
			parts = append(parts, "f")
		default:
			parts = append(parts, fmt.Sprintf("r%d%s", out, byteRanges(instruction)))
		}
	}
	return strings.Join(parts, ";"), nil
}

// byteRanges is ",low,high" for each run of the bytes from 0 to 127 a rune instruction takes, "" for none.
func byteRanges(instruction syntax.Inst) string {
	takes := func(c rune) bool {
		switch instruction.Op {
		case syntax.InstRuneAny:
			return true
		case syntax.InstRuneAnyNotNL:
			return c != '\n'
		}
		return instruction.MatchRune(c)
	}
	var b strings.Builder
	for low := rune(0); low < 128; low++ {
		if !takes(low) {
			continue
		}
		high := low
		for high+1 < 128 && takes(high+1) {
			high++
		}
		fmt.Fprintf(&b, ",%d,%d", low, high)
		low = high
	}
	return b.String()
}

// luaClass is the Lua pattern of one byte that is one of bytes or above 0x7F. None of the bytes generator.EscapedInPath
// holds is magic inside a Lua class but a ^ first, which it does not hold first.
func luaClass(bytes string) string {
	return "[" + bytes + "\x80-\xff]"
}

// luaString writes text as a Lua string in double quotes: " and \ escaped, and every byte outside printable ASCII as
// \ and its three decimal digits, so no byte of the text ends the string or the line.
func luaString(text string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\%03d", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
