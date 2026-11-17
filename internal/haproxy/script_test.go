package haproxy_test

import (
	"strings"
	"testing"

	"github.com/kitecorp/proxyfile/v2/internal/testsupport"
)

// The lines of the auth check's answer reader that decide an answer, which the rows below name.
var (
	statusLine = []string{
		`  local code, rest = line:match("^HTTP/1%.[01] (%d%d%d)(.*)$")`,
		`  if code ~= nil and (rest == "" or rest:find(PF_REASON)) then return tonumber(code) end`,
	}
	statusRange = []string{
		`    local status = pf_status(first .. rest)`,
		`    if status == nil or status < 100 or status > 599 then return "refused" end`,
	}
	headerLine = []string{
		`  local name, value = line:match("^([^:]*):[ \t]*(.-)[ \t]*$")`,
		`  if name ~= nil and name:find(PF_TOKEN) and value:find(PF_VALUE) then return name:lower(), value end`,
	}
	headerRefused = []string{
		`        local name, value = pf_header(h)`,
		`        if not name then return "refused" end`,
	}
	text           = []string{`local PF_TEXT = "[\t -~\128-\255]*$"`}
	contentLength  = []string{`      if cl ~= nil or not h[2]:find("^%d+$") then return nil end`}
	framingRefused = []string{
		`      local frame = pf_framing(headers)`,
		`      if frame == nil then return "refused" end`,
	}
	chunkSize = []string{
		`  for _ = 1, 10 do`,
		`    local byte, err = pf_io(sock, deadline, "receive", 1)`,
		`    if not byte then return nil, err end`,
		`    if byte == "\n" then`,
		`      local digits = line:match("^(%x+)\r$")`,
		`      if digits == nil then return nil, "malformed" end`,
		`      return tonumber(digits, 16)`,
		`    end`,
		`    line = line .. byte`,
		`  end`,
		`  return nil, "malformed"`,
	}
	chunkSizeRefused = []string{
		`      local n, err = pf_size_line(sock, deadline)`,
		`      if not n then return nil, err end`,
	}
)

// The auth check is in the script, which is the same bytes for every file: the lists it reads are generated from the
// generator's names, its closing task waits on a queue while no check waits, and it passes a request only on a 200
// whose every line it reads as RFC 9112 writes it. Each answer form below was sent to Envoy 1.40.2 first and then to
// HAProxy 3.2.26 and 3.4.7 with this script, with when_unavailable deny and allow; the rows hold the lines that decide
// each, as a unit test has no Lua to run them in.
func TestScriptCheck(t *testing.T) {
	minimal := script(t, string(testsupport.ReadFile(t, "../../examples/minimal/yaml/proxyfile.yaml")))
	if battery := script(t, string(testsupport.ReadFile(t, batteryFile))); battery != minimal {
		t.Errorf("the battery file's script differs from the minimal example's: %s",
			testsupport.FirstDifferenceMessage(battery, minimal))
	}
	cases := []struct {
		name   string
		blocks [][]string
	}{
		{"the lists are the generator's, in its order", [][]string{{
			`local framing = { host = true, ["content-length"] = true, ["transfer-encoding"] = true, connection = true, ["keep-alive"] = true, te = true, trailer = true, upgrade = true, expect = true, ["proxy-connection"] = true }`,
			`local hop = { ["content-length"] = true, ["transfer-encoding"] = true, connection = true, ["keep-alive"] = true, upgrade = true, ["proxy-connection"] = true }`,
			`local never_accepted = { ["content-length"] = true, ["transfer-encoding"] = true, host = true, te = true, connection = true, upgrade = true, ["keep-alive"] = true }`,
			`local dropped = { ["keep-alive"] = true }`,
			`local grpc_status, grpc_unknown = { [400] = 13, [401] = 16, [403] = 7, [404] = 12, [429] = 14, [502] = 14, [503] = 14, [504] = 14 }, 2`,
			`local unchecked = "x-envoy-auth-failure-mode-allowed"`,
			`local internal_header, expected_header = "x-envoy-internal", "x-envoy-expected-rq-timeout-ms"`,
		}}},
		// A Lua local is seen only by the code after it, so each part comes after the parts it calls.
		{"each part of the script comes after the parts it calls", [][]string{
			{`core.register_action("pf_route", {"http-req"}, function(txn, route, text)`},
			{`local function pf_split(list)`},
			{`local function pf_matches(name, patterns)`},
			{`local PF_MOST_LINE, PF_MOST_HEADERS = 16383, 100`},
			{`local PF_MOST_DENIAL = 15100`},
			{`local PF_MOST_ALLOWED_BODY = 1048576`},
			{`local function pf_now()`},
			{`local pf_waiting = {}`},
			{`local function pf_io(sock, deadline, op, arg)`},
			{`local function pf_line(sock, deadline)`},
			{`local function pf_size_line(sock, deadline)`},
			{`local function pf_body(sock, deadline, frame, keep, most)`},
			{`local function pf_ask(sock, relay, request, deadline, head)`},
			{`local function pf_refuse(txn, grpc, status, headers, body)`},
			{`local function pf_fields(headers, unset)`},
			{`core.register_action("pf_auth", { "http-req" }, function(txn, relay, path, timeout_ms, when_unavailable, send, accept, send_request)`},
			{`end, 7)`},
		}},
		{"the closing task waits on the queue while no socket waits, and looks every 10 ms while one does", [][]string{
			{`local pf_deadlines = core.queue()`},
			{`core.register_task(function()`},
			{
				`    if next(pf_waiting) == nil then`,
				`      pf_deadlines:pop_wait()`,
				`    end`,
				`    core.msleep(10)`,
			},
			{`    while pf_deadlines:pop() ~= nil do end`},
			{
				`  pf_waiting[sock] = deadline`,
				`  pf_deadlines:push(deadline)`,
			},
		}},
		// HAProxy suspends a task every tune.lua.forced-yield instructions, and the thread's actions add and mark
		// sockets meanwhile: a key removed under a walk with next makes next fail once the table grows, and a task
		// that fails is never started again.
		{"the closing task's walk removes nothing, and an error ends one pass, not the task", [][]string{
			{`local function pf_close_due()`},
			{
				`  for sock, deadline in pairs(pf_waiting) do`,
				`    if deadline == false or now >= deadline then due[#due + 1] = sock end`,
				`  end`,
				`  for _, sock in ipairs(due) do`,
				`    if pf_waiting[sock] then sock:close() end`,
				`    pf_waiting[sock] = nil`,
				`  end`,
			},
			{
				`    local ok, err = pcall(pf_close_due)`,
				`    if not ok then`,
				`      pf_task_errors = pf_task_errors + 1`,
			},
			{
				`  pf_waiting[sock] = false`,
				`  sock:close()`,
			},
		}},
		// The Envoy output's client for the check sends both on every check, the deadline as auth.timeout in
		// milliseconds (measured on Envoy 1.40.2); a client's own copies are removed before the check is asked
		// (generator.RemovedByEnvoy).
		{"the check is told it is internal and its deadline, as Envoy tells it", [][]string{{
			`  add("x-gateway-timeout-ms", timeout_ms)`,
			`  add(internal_header, "true")`,
			`  add(expected_header, timeout_ms)`,
		}}},
		{"a client's own x-gateway-timeout-ms never reaches the check", [][]string{
			{`    if not framing[name] and name ~= "x-gateway-timeout-ms" and not (send_request == "1" and forwarded[name])`},
		}},
		{"an answer never sets a header HAProxy frames or routes a request by", [][]string{
			{`      if pf_matches(h[1], acceptp) then`},
			{
				`        if not never_accepted[h[1]] then`,
				`          txn.http:req_set_header(h[1], h[2])`,
			},
		}},
		{"an unanswered check under allow sets the generated mark", [][]string{
			{`    txn.http:req_set_header(unchecked, "true")`},
		}},
		// Envoy 1.40.2 answers an unanswered check under deny, and each answer it treats as one, with its own 403, in
		// the forms the configuration's pass rule writes (TestCheckLines), so the script leaves every refusal of its own
		// to that rule and answers only a denial it copies.
		{"an unanswered check under deny, an answer the reader refuses and an accepted header that did not fit are left to the pass rule", [][]string{
			{
				`  if kind == "failure" and when_unavailable == "allow" then`,
				`    txn.http:req_set_header(unchecked, "true")`,
				`    txn:set_var("txn.pf_passed", true)`,
				`    return`,
				`  end`,
				`  if kind ~= "answer" then return end`,
			},
			{`      if not now[name] or now[name][0] ~= value or now[name][1] ~= nil then return end`},
			{`  pf_refuse(txn, grpc, (status < 300) and 403 or status, headers, body)`},
		}},
		// HAProxy keeps a header as its name, its value and 8 bytes, and a check's denial of 15,140 bytes beside the most
		// that the configuration's after-response lines add got HAProxy's own 500 where Envoy 1.40.2 passes the denial on,
		// as did a gRPC denial whose message, percent-encoded, outgrew the buffer (measured on HAProxy 3.2.26 and 3.4.7),
		// so a denial counts as HAProxy stores it, the gRPC form with its message, and one past the bound is left to the
		// pass rule.
		{"a denial counts as HAProxy stores it, and one past the bound is left to the pass rule", [][]string{
			{`        if not hop[h[1]] then kept = kept + #h[1] + #h[2] + 8 end`},
			{`  if size + #payload > PF_MOST_DENIAL then return end`},
		}},
		// Envoy 1.40.2 answers a gRPC call it refuses with an HTTP status GRPCStatus does not list with grpc-status 2.
		{"a denial of a status gRPC maps to no other code gets grpc-status 2", [][]string{
			{`    keep("grpc-status", tostring(grpc_status[status] or grpc_unknown))`},
		}},
		// Envoy 1.40.2 answers 500 to a request the check's accepted headers take past its 100 header fields. It counts
		// the request after its own removals and joins, which the file has made by the check, with :method, :path,
		// :authority and :scheme, the x-request-id and x-forwarded-proto it sets, with client_address connection the
		// x-forwarded-for and x-envoy-external-address too, and no Transfer-Encoding line (measured), so the script counts
		// the lines HAProxy holds once the accepted headers are set, and leaves the 500 to the configuration. It also
		// counts a te and a transfer-encoding the answer holds under an accepted name, which Envoy sets and counts and the
		// script never sets. Envoy counts only when the answer held a header an accepted name covers other than
		// keep-alive, which it never sets: a 200 with none, or with keep-alive alone, is served up to the line limit.
		{"a request the accepted headers take past 100 fields is left to the configuration's 500", [][]string{
			{`local PF_MOST_FIELDS = 100`},
			{`local PF_COUNTED_NOT_SET = { te = true, ["transfer-encoding"] = true }`},
			{
				`local function pf_fields(headers, unset)`,
				`  local n = 3`,
				`  for name, lines in pairs(headers) do`,
				`    if name ~= "transfer-encoding" then`,
				`      local i = 0`,
				`      while lines[i] ~= nil do i = i + 1 end`,
				`      n = n + i`,
				`    end`,
				`  end`,
				`  for _ in pairs(unset) do n = n + 1 end`,
				`  return n`,
				`end`,
			},
			{`    local set, unset, counted = {}, {}, false`},
			{
				`      if pf_matches(h[1], acceptp) then`,
				`        counted = counted or not dropped[h[1]]`,
			},
			{
				`        elseif PF_COUNTED_NOT_SET[h[1]] then`,
				`          unset[h[1]] = true`,
				`        end`,
			},
			{
				`    local now = txn.http:req_get_headers()`,
				`    for name, value in pairs(set) do`,
				`      if not now[name] or now[name][0] ~= value or now[name][1] ~= nil then return end`,
				`    end`,
				`    if counted and pf_fields(now, unset) > PF_MOST_FIELDS then`,
				`      txn:set_var("txn.pf_too_many", true)`,
				`      return`,
				`    end`,
				`    txn:set_var("txn.pf_passed", true)`,
			},
		}},
		// Envoy answers a refusal in gRPC's form when the content type is application/grpc alone or followed by +, as
		// written, and answers application/grpc;x=1 and Application/GRPC with the denial's own status (measured on
		// Envoy 1.40.2).
		{"a gRPC call is told apart as Envoy tells it", [][]string{{
			`  local ct = req["content-type"] and req["content-type"][0] or ""`,
			`  local grpc = ct == "application/grpc" or ct:find("^application/grpc%+") ~= nil`,
		}}},

		// One row for each set of lines that decides an answer the reader refuses, under allow as under deny, with the
		// forms it decides: where Envoy 1.40.2 treats the form as an unanswered check (403 under deny, served with the
		// mark under allow), reads it as a denial, or reads it (a stricter difference, declared on auth.ask).
		//
		// A Content-Length with a minus sign, a plus sign or a fraction, one written in hexadecimal, and two lines
		// with the same value: unanswered on Envoy.
		{"a Content-Length that is not one line of digits is refused", [][]string{contentLength, framingRefused}},
		// Unanswered on Envoy.
		{"a Content-Length beside Transfer-Encoding is refused", [][]string{
			{`  if te ~= nil and cl ~= nil then return nil end`}, framingRefused,
		}},
		// A name holding a byte outside a token, a space before the colon, and such a line in a 1xx answer before the
		// 200: unanswered on Envoy.
		{"a header line that is not a token, a colon and a value is refused", [][]string{headerLine, headerRefused}},
		// A control byte, NUL or DEL: unanswered on Envoy; a CR alone: read by Envoy.
		{"a header value holding a control byte other than a tab is refused", [][]string{text, headerLine, headerRefused}},
		// Unanswered on Envoy.
		{"a reason holding a control byte is refused", [][]string{text, statusLine, statusRange}},
		// No space before the reason: unanswered on Envoy; a status of four digits or a line in HTTP/2.0: a denial on
		// Envoy; HTTP/1.2, or a tab between the version and the status: read by Envoy.
		{"a status line that is not HTTP/1.0 or HTTP/1.1, a space and three digits is refused", [][]string{statusLine, statusRange}},
		// A status of 600 or more: a denial on Envoy; one below 100, or a first line that is not a status line.
		{"a status outside 100 to 599 is refused", [][]string{statusRange}},
		{"a status line that began and did not end, or of 16,383 bytes or more, is refused", [][]string{{
			`    local first = pf_io(sock, deadline, "receive", 1)`,
			`    if not first then return "failure" end`,
			`    local rest = pf_line(sock, deadline)`,
			`    if not rest then return "refused" end`,
		}}},
		// A byte that is not hexadecimal, or a size line of a chunk, or of the last chunk, that ends in a bare LF:
		// unanswered on Envoy, which reads a bare LF at the end of every other line of an answer; more digits than the
		// body bound needs, a chunk extension, or a space or a tab after the size: read by Envoy. The socket's line read
		// drops the CR, so the size line is read a byte at a time.
		{"a chunk-size line that is not plain hexadecimal of at most 8 digits ended by CR LF is refused", [][]string{chunkSize, chunkSizeRefused}},
		// Unanswered on Envoy.
		{"a malformed trailer line is refused", [][]string{{`      if not pf_header(line) then return nil, "malformed" end`}}},

		// Answers Envoy 1.40.2 treats as unanswered, which go on with the mark under allow: a body that breaks off, by a
		// close or by the deadline, while its bytes are read by count or up to the close; a 101st header line; and an
		// answer read whole at or after the deadline. A line that breaks off stays refused.
		{"a body that breaks off is an unanswered check, and a line that breaks off is refused", [][]string{
			{`      if not data then return err or "closed" end`},
			{
				`      line, err = pf_line(sock, deadline)`,
				`      if not line then return nil, err end`,
			},
			{`      if err then return nil, err, true end`},
			{`    local err = read(frame)`, `    if err then return nil, err, true end`},
			{
				`      local h = pf_line(sock, deadline)`,
				`      if not h then return "refused" end`,
			},
			{
				`        local _, cut`,
				`        body, _, cut = pf_body(sock, deadline, frame, keep, most)`,
				`        if body == nil then return cut and "failure" or "refused" end`,
			},
		}},
		// Envoy 1.40.2 reads at most 100 header lines in a head and does not count a folded line among them, and it
		// counts a name it keeps inline, such as date, once however many lines carry it (measured), so the count is
		// tested only for a line that starts a header, and a 101st is unanswered only when each name came once.
		{"a 101st header line, folded lines not counted, is unanswered when each name came once and refused otherwise", [][]string{
			{`    local headers, seen, twice = {}, {}, false`},
			{
				`      if h == "" then break end`,
				`      local fold = h:match("^[ \t]+(.-)[ \t]*$")`,
				`      if fold ~= nil then`,
			},
			{
				`      else`,
				`        local name, value = pf_header(h)`,
				`        if not name then return "refused" end`,
			},
			{
				`        twice = twice or seen[name] ~= nil`,
				`        seen[name] = true`,
				`        if #headers >= PF_MOST_HEADERS then return twice and "refused" or "failure" end`,
				`        headers[#headers + 1] = { name, value }`,
			},
		}},
		// Envoy 1.40.2 reads 81,920 bytes of each head of the check's answer, from the status line to the empty line,
		// a 1xx head and folded lines included, and as many of a trailer section, and treats an answer with more as
		// unanswered (measured to the byte). The reader counts each line with a CR and an LF and refuses an answer
		// past the bound, under allow too.
		{"a head or a trailer section past 81,920 bytes is refused", [][]string{
			{`local PF_MOST_HEAD = 81920`},
			{
				`      trailer = trailer + #line + 2`,
				`      if trailer > PF_MOST_HEAD then return nil, "too long" end`,
			},
			{`    local size = #first + #rest + 2`},
			{
				`      size = size + #h + 2`,
				`      if size > PF_MOST_HEAD then return "refused" end`,
				`      if h == "" then break end`,
			},
		}},
		// The closing task closes a socket at its deadline and after it, so an answer ended at the deadline is as late.
		{"an answer read whole at or after the deadline is an unanswered check", [][]string{
			{`  if kind == "answer" and pf_now() >= deadline then kind = "failure" end`},
		}},

		// Forms the reader takes, as Envoy does.
		{"lines other than a chunk size's that end in LF alone are read, as Envoy reads them", [][]string{
			{`  local line, err = pf_io(sock, deadline, "receive", "*l")`},
		}},
		{"a status line with no reason is taken", [][]string{statusLine}},
		{"a byte above 0x7F in a value or a reason is taken", [][]string{text}},
		{"Transfer-Encoding: Chunked in capitals is taken", [][]string{
			{`      if te ~= nil or h[2]:lower() ~= "chunked" then return nil end`},
		}},
		// The parts are joined once the header lines end, so a value of many folded lines is not copied once a line.
		{"a folded line is joined to the one before it by a space", [][]string{
			{
				`        if #headers == 0 or not fold:find(PF_VALUE) then return "refused" end`,
				`        local last = headers[#headers]`,
				`        last[3] = last[3] or { last[2] }`,
				`        last[3][#last[3] + 1] = fold`,
			},
			{`      if h[3] then h[2], h[3] = table.concat(h[3], " "), nil end`},
		}},
		{"a 200 with neither framing header is read to the close", [][]string{
			{`  return te or cl or "close"`}, {`  elseif frame == "close" then`},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if block := testsupport.FirstMissingBlock(minimal, c.blocks); block != "" {
				t.Errorf("the script does not hold, in its place:\n%s", block)
			}
		})
	}
	t.Run("txn.pf_passed is set by an allowed answer and an unanswered check under allow alone", func(t *testing.T) {
		if got := strings.Count(minimal, `txn:set_var("txn.pf_passed", true)`); got != 2 {
			t.Errorf("txn.pf_passed is set on %d lines, want 2", got)
		}
	})
	t.Run("the script calls pf_refuse only to copy the check's denial", func(t *testing.T) {
		if got := strings.Count(minimal, "pf_refuse("); got != 2 {
			t.Errorf("pf_refuse is named on %d lines, want 2: its definition and the call that copies a denial", got)
		}
	})
}
