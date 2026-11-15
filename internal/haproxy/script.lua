-- The functions the configuration calls, for HAProxy's Lua 5.4. Each answers what HAProxy's own configuration cannot:
-- pf_join writes the lines of a header as one, pf_escape writes a byte above 0x7F as its escape, pf_route matches a
-- route's pattern without ever giving up, and pf_auth asks the auth check with the client's own method and its own
-- deadline. The script is the same for every file: each pattern's program and the check's settings are written in the
-- configuration, as arguments of pf_route and pf_auth.

-- pf_join writes the lines of each header in joined, the headers Envoy joins, as one line: the values that are not
-- empty, in the order they came, joined with a comma, or one empty line when every line is empty (measured on Envoy
-- 1.40.2). HAProxy passes each line on, and none of its fetches reads every line of a header as one value. It sets
-- txn.joined last, so the configuration answers 400 to a request whose run stopped part way.
core.register_action("pf_join", {"http-req"}, function(txn)
  local headers = txn.http:req_get_headers()
  for _, name in ipairs(joined) do
    -- HAProxy numbers a header's lines from 0, so a second line is at 1.
    local lines = headers[name]
    if lines ~= nil and lines[1] ~= nil then
      local values = {}
      local at = 0
      while lines[at] ~= nil do
        if lines[at] ~= "" then
          values[#values + 1] = lines[at]
        end
        at = at + 1
      end
      txn.http:req_set_header(name, table.concat(values, ","))
    end
  end
  txn:set_var("txn.joined", 1)
end)

-- pf_escape escapes each byte of a path Envoy escapes before it picks a route, escaped, in upper case hex (measured on
-- Envoy 1.40.2). HAProxy's regsub writes one fixed text for every match, so it cannot write the hex of the byte it
-- matched.
core.register_converters("pf_escape", function(path)
  return (path:gsub(escaped, function(byte)
    return string.format("%%%02X", byte:byte())
  end))
end)

-- The kinds of a program's instructions, as Go's regexp/syntax compiles a pattern: an alternative, a step that reads
-- nothing, an empty-width assertion, a byte read, the match and the failure.
local ALT, NOP, EMPTY, BYTE, MATCH, FAIL = 1, 2, 3, 4, 5, 6
local kinds = {a = ALT, n = NOP, e = EMPTY, r = BYTE, m = MATCH, f = FAIL}

-- decode turns a program's text, as the configuration writes it, into its instructions, numbered from 1, and the
-- number of its first. The text is "s" and that number, then one instruction a part, the parts joined with ";": its
-- kind's letter, then its numbers joined with ",": the next instruction and, for an alternative, the other one, for
-- an assertion its bits, and for a byte read the ranges of the bytes from 0 to 127 it takes, low and high.
local function decode(text)
  local program, start = {}, nil
  for part in text:gmatch("[^;]+") do
    local numbers = {}
    for number in part:gmatch("%d+") do
      numbers[#numbers + 1] = tonumber(number)
    end
    local letter = part:sub(1, 1)
    if letter == "s" then
      start = numbers[1]
    else
      local instruction = {kind = kinds[letter], out = numbers[1], arg = numbers[2]}
      if instruction.kind == BYTE then
        local set = {}
        for at = 2, #numbers, 2 do
          for byte = numbers[at], numbers[at + 1] do
            set[byte] = true
          end
        end
        instruction.set = set
      end
      program[#program + 1] = instruction
    end
  end
  return program, start
end

-- The class of the byte before a place, which with the byte after it gives Go's syntax.EmptyOpContext there: a word
-- byte (syntax.IsWordChar), a line feed, any other byte, or none, at the start of the text.
local WORD, NEWLINE, OTHER, START = 1, 2, 3, 4
local classOf = {}
for byte = 0, 255 do
  if (byte >= 48 and byte <= 57) or (byte >= 65 and byte <= 90) or (byte >= 97 and byte <= 122) or byte == 95 then
    classOf[byte] = WORD
  elseif byte == 10 then
    classOf[byte] = NEWLINE
  else
    classOf[byte] = OTHER
  end
end

-- context is the bits of EmptyBeginLine (1), EmptyEndLine (2), EmptyBeginText (4), EmptyEndText (8),
-- EmptyWordBoundary (16) and EmptyNoWordBoundary (32) that hold between a byte of the class before and the byte after,
-- nil at the end of the text.
local function context(before, after)
  local op, boundary = 32, before == WORD
  if before == NEWLINE then
    op = op | 1
  elseif before == START then
    op = op | 4 | 1
  end
  if after == nil then
    op = op | 8 | 2
  elseif classOf[after] == WORD then
    boundary = not boundary
  elseif after == 10 then
    op = op | 2
  end
  if boundary then
    op = op ~ (16 | 32)
  end
  return op
end

local contexts, ends = {}, {}
for before = WORD, START do
  contexts[before] = {}
  for byte = 0, 255 do
    contexts[before][byte] = context(before, byte)
  end
  ends[before] = context(before, nil)
end

-- A machine runs one program as a deterministic automaton built while it reads (RE2's way): a state is the
-- instructions the paths through the program wait at, before their empty-width steps, and the class of the byte read
-- last; reading a byte follows them to the byte reads that take it. A state keeps where each byte took it, so a byte
-- read again from that state costs one table read, and a path's time grows with its length alone, never with the
-- program's size. dead is the state of no path, from which no byte leads anywhere.
local dead = {}
for byte = 0, 255 do
  dead[byte] = dead
end

-- The states the machines build are a cache, and the cache of a thread is bounded as a whole: HAProxy gives each thread
-- a Lua state of its own (lua-load-per-thread), and every machine of the thread counts each state it keeps in held, as
-- perState for the table and its key, one for each instruction it waits on and one for each byte it knows where it
-- leads. Once held reaches mostHeld, every machine of the thread forgets its states and builds them again as it reads,
-- in time the request's budget below bounds. One unit held 26 to 28 bytes of Lua memory on HAProxy 3.2.26 and 3.4.7
-- (measured), so a thread keeps about 1 MB of states at most, and HAProxy that times its threads. 200 paths sent again
-- and again through the 73 pattern routes of the conformance battery's HAProxy file kept about 26,000 units a thread
-- and were served as fast as with 8 MB; a flood of new paths then grew HAProxy by about 4 MB a thread, as much as
-- those 200 paths did, where 8 MB let it grow by about 19 MB (measured on 12 threads).
local held, mostHeld, perState = 0, 40000, 8

-- budget is the most work the patterns of one request may take: a byte read from a state that knows where it leads
-- counts 1, each instruction visited while a new state is built counts building, and each run counts calling and a
-- unit for each 512 bytes of its program's text, which HAProxy copies at each call. One unit took about 47 ns of CPU
-- on HAProxy 3.2.26 and 3.4.7 on an arm64 Mac, whether the work read bytes or built states, and about 65 ns with all
-- 12 threads busy (measured), so the patterns of one request alone take about 24 ms at most, and of each of many at
-- once about 33 ms. A request past it is answered 400 by the configuration, where Envoy's RE2 would always decide;
-- HAProxy runs the action in steps, letting other requests run between them, so no request holds a thread for long.
local budget, building, calling = 500000, 6, 64

local machines = {}

-- forget makes every machine of the thread forget the states it keeps. A run that holds one of them goes on from it,
-- and drops it when the run ends.
local function forget()
  for _, machine in pairs(machines) do
    machine.states = {}
  end
  held = 0
end

-- state returns the state of the instructions waiting, as a list, after a byte of class class, made once.
local function state(machine, waiting, class)
  if #waiting == 0 then
    return dead
  end
  table.sort(waiting)
  local key = class .. ":" .. table.concat(waiting, ",")
  local found = machine.states[key]
  if found == nil then
    found = {waiting = waiting, class = class}
    machine.states[key] = found
    held = held + perState + #waiting
  end
  return found
end

-- follow visits each instruction reached from the state's waiting ones by alternatives, steps that read nothing and
-- the assertions op satisfies, each once, and calls reached with each byte read and match it reaches. It returns how
-- many instructions it visited.
local function follow(program, from, op, reached)
  local seen, stack, visited = {}, {}, 0
  for _, at in ipairs(from.waiting) do
    stack[#stack + 1] = at
  end
  while #stack > 0 do
    local at = stack[#stack]
    stack[#stack] = nil
    if not seen[at] then
      seen[at] = true
      visited = visited + 1
      local instruction = program[at]
      local kind = instruction.kind
      if kind == ALT then
        stack[#stack + 1] = instruction.arg
        stack[#stack + 1] = instruction.out
      elseif kind == NOP then
        stack[#stack + 1] = instruction.out
      elseif kind == EMPTY then
        if (instruction.arg & ~op) == 0 then
          stack[#stack + 1] = instruction.out
        end
      elseif kind == BYTE or kind == MATCH then
        reached(instruction)
      end
    end
  end
  return visited
end

-- step returns the state byte leads to from from, which it keeps in from, and the instructions it visited. Every unit
-- held but the first state of a run is added by a step, so the thread forgets here, before it adds more.
local function step(machine, from, byte)
  if held >= mostHeld then
    forget()
  end
  local next, waiting = {}, {}
  local visited = follow(machine.program, from, contexts[from.class][byte], function(instruction)
    if instruction.kind == BYTE and instruction.set[byte] and not next[instruction.out] then
      next[instruction.out] = true
      waiting[#waiting + 1] = instruction.out
    end
  end)
  local to = state(machine, waiting, classOf[byte])
  from[byte] = to
  held = held + 1
  return to, visited
end

-- accepts says whether a path that ends in from matches, which from keeps, and the instructions it visited.
local function accepts(machine, from)
  if from.accepts ~= nil then
    return from.accepts, 0
  end
  local found = false
  local visited = follow(machine.program, from, ends[from.class], function(instruction)
    if instruction.kind == MATCH then
      found = true
    end
  end)
  from.accepts = found
  return found, visited
end

-- machineOf returns the machine of the route numbered route, made from its program's text the first time the route
-- runs. A process loads one configuration, which gives each route one program, so the route's number names it.
local function machineOf(route, text)
  local machine = machines[route]
  if machine == nil then
    local program, start = decode(text)
    machine = {program = program, start = start, states = {}}
    machines[route] = machine
  end
  return machine
end

-- run says whether the whole of bytes, n of them, matches the machine's program, and how much of left is left; nil
-- when the work would pass left, so the configuration answers 400.
local function run(machine, bytes, n, left)
  if left < 0 then
    return nil, left
  end
  local at = state(machine, {machine.start}, START)
  for i = 1, n do
    local byte = bytes[i]
    local to = at[byte]
    if to == nil then
      local visited
      to, visited = step(machine, at, byte)
      left = left - visited * building
    end
    left = left - 1
    if left < 0 then
      return nil, left
    end
    if to == dead then
      return false, left
    end
    at = to
  end
  local found, visited = accepts(machine, at)
  left = left - visited * building
  if left < 0 then
    return nil, left
  end
  return found, left
end

-- The bytes of the last path read, kept so the routes of one request read the path once.
local lastPath, lastBytes = nil, {}

-- pf_route matches the path against the program of the route numbered route, and sets txn.pattern<route> to "1" or
-- "0". It sets nothing when the request's work would pass the budget, or when the run failed, which the configuration
-- answers 400, never as no match.
core.register_action("pf_route", {"http-req"}, function(txn, route, text)
  local machine = machineOf(route, text)
  local path = txn.f:path()
  if path ~= lastPath then
    local bytes = {}
    for i = 1, #path do
      bytes[i] = path:byte(i)
    end
    lastPath, lastBytes = path, bytes
  end
  local bytes = lastBytes
  local left = (txn:get_var("txn.pf_left") or budget) - calling - #text // 512
  local found
  found, left = run(machine, bytes, #path, left)
  if found == nil then
    return
  end
  txn:set_var("txn.pf_left", left)
  txn:set_var("txn.pattern" .. route, found and "1" or "0")
end, 2)

-- pf_auth asks the check service whether a request may pass, as the Envoy output's ext_authz does in HTTP mode, on a
-- TCP socket (core.tcp) with a small HTTP/1.1 client of its own, so the check is asked with the client's own method. It
-- connects to the relay the configuration writes for the check service, a listen section in TCP mode, because the
-- socket's own timeout does not bound its connect (measured: to an address that never answered, 303 s on HAProxy 3.4.7
-- and 6.0 s on 3.2.26 against a timeout of 1.5 s). Its arguments: the relay, the path of auth.ask, the timeout in
-- milliseconds, allow or deny (when_unavailable), the send names and the accept names joined with commas (a name ending
-- in * is a prefix; "-" for none), and 1 or 0 (send_request). It sets txn.pf_passed only when the request may go on,
-- and the configuration's pass rule refuses an asked request without it, so an error in this script refuses the
-- request: a runtime error in an action lets the request continue to the next rule (measured on HAProxy 3.2.26 and
-- 3.4.7). The script answers only a denial it copies from the check; every refusal of its own (an unanswered check
-- under deny, an answer it cannot read whole, an accepted header that did not fit) it leaves to the pass rule, which
-- answers in the forms Envoy answers an unanswered check with, so those forms are written once.

local function pf_split(list)
  local out = {}
  for item in string.gmatch(list, "[^,]+") do
    if item ~= "-" then out[#out + 1] = item end
  end
  return out
end

local function pf_matches(name, patterns)
  for _, p in ipairs(patterns) do
    if p:sub(-1) == "*" then
      if name:sub(1, #p - 1) == p:sub(1, -2) then return true end
    elseif name == p then
      return true
    end
  end
  return false
end

-- receive("*l") gives at most a buffer (16384 bytes) as a line on HAProxy 3.4.7 and waits for the line's end on
-- 3.2.26 (measured), so a line that long is refused.
local PF_MOST_LINE, PF_MOST_HEADERS = 16383, 100
-- Envoy 1.40.2 reads 81,920 bytes of each head of the check's answer, from the status line to the empty line with
-- their line ends, a 1xx head and folded lines included, and as many of a trailer section, and treats an answer with
-- more as unanswered (measured to the byte). The reader counts each line with a CR and an LF, so it never reads more
-- than Envoy, and refuses an answer past the bound.
local PF_MOST_HEAD = 81920
-- A denial's reply must leave the room the configuration's after-response lines take on every answer (the request id
-- and an echoed Origin, which its bounds keep within 1,024 bytes): HAProxy keeps a header as its name, its value and 8
-- bytes, and with the most those lines add a denial of 15,208 bytes so counted was passed on and one of 15,218 became
-- HAProxy's own 500 (measured on HAProxy 3.2.26, and 3.4.7 from 15,228). The bound leaves room for the longest reason
-- phrase HAProxy writes.
local PF_MOST_DENIAL = 15100
-- The body of a 200 is read and dropped up to this (Envoy 1.40.2 passed 16 MiB and refused 64 MiB: measured).
local PF_MOST_ALLOWED_BODY = 1048576

local function pf_now()
  local t = core.now()
  return t.sec + t.usec / 1e6
end

-- A socket's own timeout counts only the time without data, so a check sending a byte at a time would run past it
-- (measured: a body of 100 bytes sent a byte every 100 ms was read for 10.1 s against a timeout of 1.5 s, on HAProxy
-- 3.2.26 and 3.4.7). Each thread's task closes every socket whose deadline has passed. The action pushes each deadline
-- to pf_deadlines, and the task waits on that queue while no socket waits, so an idle HAProxy spends nothing on it, and
-- looks every 10 ms while one does. HAProxy's Lua API pages for 3.2 and 3.4
-- (https://www.arpalert.org/src/haproxy-lua-api/3.2/index.html and
-- https://www.arpalert.org/src/haproxy-lua-api/3.4/index.html) give core.queue() to the script's body and
-- Queue.pop_wait to a task, which "waits for data if no item is currently available".
local pf_waiting = {}
local pf_deadlines = core.queue()

-- pf_close_due closes each socket whose deadline has passed and forgets each one its action is done with, which the
-- action marks false. HAProxy suspends a task every tune.lua.forced-yield instructions, and the thread's actions add
-- and mark sockets meanwhile: a key removed while a walk with next stands on it makes next fail once the table grows
-- (a Lua simulation of the walk, suspended every 50 of 3,000 sockets while 5 were removed and 8 added, failed in 110
-- rounds of 2,000), so the walk removes nothing, and the sockets are closed and removed once it is over.
local function pf_close_due()
  local now, due = pf_now(), {}
  for sock, deadline in pairs(pf_waiting) do
    if deadline == false or now >= deadline then due[#due + 1] = sock end
  end
  for _, sock in ipairs(due) do
    if pf_waiting[sock] then sock:close() end
    pf_waiting[sock] = nil
  end
end

-- A task that fails is never started again, so an error in a pass, such as one a socket added under the walk could
-- still give, ends that pass alone: the task logs it with the count so far and walks again 10 ms later.
local pf_task_errors = 0
core.register_task(function()
  while true do
    if next(pf_waiting) == nil then
      pf_deadlines:pop_wait()
    end
    core.msleep(10)
    -- Every deadline pushed meanwhile is in pf_waiting already. Dropping them keeps the queue small while sockets
    -- wait, when the task never reaches pop_wait.
    while pf_deadlines:pop() ~= nil do end
    local ok, err = pcall(pf_close_due)
    if not ok then
      pf_task_errors = pf_task_errors + 1
      core.Warning("proxyfile.lua: a pass of the auth check's closing task failed (" .. pf_task_errors ..
        " so far): " .. tostring(err))
    end
  end
end)

local function pf_io(sock, deadline, op, arg)
  local left = deadline - pf_now()
  if left <= 0 then return nil, "timeout" end
  sock:settimeout(left)
  if op == "send" then return sock:send(arg) end
  return sock:receive(arg)
end

-- pf_line reads one line, ended by LF with or without a CR before it. A line of PF_MOST_LINE bytes or more is
-- refused as too long.
local function pf_line(sock, deadline)
  local line, err = pf_io(sock, deadline, "receive", "*l")
  if line and #line >= PF_MOST_LINE then return nil, "too long" end
  return line, err
end

-- The check's answer is read as RFC 9112 writes one: a request passes only on a 200 whose every line this reader
-- takes, and an answer it cannot read is refused under allow as under deny, never passed, never taken as unanswered
-- and never copied as a denial. Envoy 1.40.2 treated each malformed answer measured as unanswered, but read a status
-- line in HTTP/2.0, a status of 600 or more, and a 401 whose status line held 20,000 bytes as a denial (measured), so
-- a reader that guessed which answers Envoy calls unanswered could serve a request Envoy refuses.
-- PF_TEXT is the rest of a header value or a reason: tabs, spaces, visible bytes and bytes above 0x7F, never another
-- control byte. PF_TOKEN is a header name (RFC 9110, section 5.6.2).
local PF_TEXT = "[\t -~\128-\255]*$"
local PF_REASON, PF_VALUE, PF_TOKEN = "^ " .. PF_TEXT, "^" .. PF_TEXT, "^[%w!#$%%&'*+%-.^_`|~]+$"

-- pf_status is the status of a status line: HTTP/1.0 or HTTP/1.1, one space, three digits, then nothing or a space
-- and a reason; nil for any other line.
local function pf_status(line)
  local code, rest = line:match("^HTTP/1%.[01] (%d%d%d)(.*)$")
  if code ~= nil and (rest == "" or rest:find(PF_REASON)) then return tonumber(code) end
  return nil
end

-- pf_header is the name, in lower case, and the value of a header line: a token, a colon, then the value without
-- the spaces and tabs around it; nil for any other line.
local function pf_header(line)
  local name, value = line:match("^([^:]*):[ \t]*(.-)[ \t]*$")
  if name ~= nil and name:find(PF_TOKEN) and value:find(PF_VALUE) then return name:lower(), value end
  return nil
end

-- pf_framing is how the header lines frame the body: "chunked", the content-length as a number, or "close" when
-- the body runs to the close; nil for any other framing: a transfer-encoding other than chunked alone, a
-- content-length other than one line of digits, or both (Envoy 1.40.2 took each such answer as unanswered: measured).
local function pf_framing(headers)
  local te, cl
  for _, h in ipairs(headers) do
    if h[1] == "transfer-encoding" then
      if te ~= nil or h[2]:lower() ~= "chunked" then return nil end
      te = "chunked"
    elseif h[1] == "content-length" then
      if cl ~= nil or not h[2]:find("^%d+$") then return nil end
      cl = tonumber(h[2])
    end
  end
  if te ~= nil and cl ~= nil then return nil end
  return te or cl or "close"
end

-- pf_body reads a body as pf_framing framed it, keeping at most keep bytes and reading at most most. A read that
-- fails returns the socket's error, a chunk written otherwise than RFC 9112 writes it returns "malformed", and a body
-- past most returns "too long". The third value is true when the read that failed was one of the body's own bytes,
-- by count or up to the close, which the check closing or passing the deadline cuts off: Envoy 1.40.2 treats such an
-- answer as unanswered (measured), where a line the read breaks off in, a chunk size or a trailer, stays refused.
local function pf_body(sock, deadline, frame, keep, most)
  local parts, size = {}, 0
  local function take(data)
    size = size + #data
    if size <= keep then parts[#parts + 1] = data end
  end
  -- read reads n bytes, which the caller has bounded.
  local function read(n)
    while n > 0 do
      local data, err = pf_io(sock, deadline, "receive", math.min(n, 16384))
      if not data then return err or "closed" end
      take(data)
      n = n - #data
    end
  end
  if frame == "chunked" then
    while true do
      local line, err = pf_line(sock, deadline)
      if not line then return nil, err end
      -- Hexadecimal digits alone, and few enough that the size is exact: Lua reads 16 of them as a negative number.
      local digits = line:match("^%x+$")
      if digits == nil or #digits > 8 then return nil, "malformed" end
      local n = tonumber(digits, 16)
      if n == 0 then break end
      if size + n > most then return nil, "too long" end
      err = read(n)
      if err then return nil, err, true end
      line, err = pf_line(sock, deadline)
      if not line then return nil, err end
      if line ~= "" then return nil, "malformed" end
    end
    -- The trailer's lines, read up to the empty one, counted against most and PF_MOST_HEAD and never kept.
    local trailer = 0
    while true do
      local line, err = pf_line(sock, deadline)
      if not line then return nil, err end
      trailer = trailer + #line + 2
      if trailer > PF_MOST_HEAD then return nil, "too long" end
      if line == "" then break end
      if not pf_header(line) then return nil, "malformed" end
      size = size + #line
      if size > most then return nil, "too long" end
    end
  elseif frame == "close" then
    while true do
      local data, err, partial = pf_io(sock, deadline, "receive", 16384)
      data = data or partial
      if data and #data > 0 then
        take(data)
        if size > most then return nil, "too long" end
      end
      if err == "connection closed." or err == "closed" then break end
      if err then return nil, err, true end
    end
  else
    if frame > most then return nil, "too long" end
    local err = read(frame)
    if err then return nil, err, true end
  end
  return table.concat(parts)
end

-- pf_ask asks the check and returns how it went: "failure" when no byte of an answer came (the check could not be
-- reached, or it closed or kept silent past the deadline), the answer is a 5xx, a head has a 101st header line with
-- each name once, or its body broke off, the cases Envoy calls unanswered; "refused" for an answer this reader cannot
-- take whole; or "answer" with the status, the header lines and the body of a denial.
local function pf_ask(sock, relay, request, deadline, head)
  if not sock:connect(relay) then return "failure" end
  if not pf_io(sock, deadline, "send", request) then return "failure" end
  while true do
    -- One byte first, so a status line that began is told from silence: 3.2.26 waits on a long line until the task
    -- closes the socket, the error a silent check gives (measured), and Envoy read a 401 whose status line held
    -- 20,000 bytes as a denial, so a status line that began and did not end is refused, never unanswered.
    local first = pf_io(sock, deadline, "receive", 1)
    if not first then return "failure" end
    local rest = pf_line(sock, deadline)
    if not rest then return "refused" end
    local size = #first + #rest + 2
    local status = pf_status(first .. rest)
    if status == nil or status < 100 or status > 599 then return "refused" end
    if status >= 500 then return "failure" end
    local headers, seen, twice = {}, {}, false
    while true do
      local h = pf_line(sock, deadline)
      if not h then return "refused" end
      size = size + #h + 2
      if size > PF_MOST_HEAD then return "refused" end
      if h == "" then break end
      local fold = h:match("^[ \t]+(.-)[ \t]*$")
      if fold ~= nil then
        -- Envoy passes a folded line joined to the one before it by a space (measured). The parts are joined once the
        -- header lines end, so a value of many folded lines is not copied once a line.
        if #headers == 0 or not fold:find(PF_VALUE) then return "refused" end
        local last = headers[#headers]
        last[3] = last[3] or { last[2] }
        last[3][#last[3] + 1] = fold
      else
        local name, value = pf_header(h)
        if not name then return "refused" end
        -- Envoy reads at most 100 header lines in a head, a folded line not counted, and treats one with more as
        -- unanswered. It counts the entries of its header map, in which a name it keeps inline, such as date, via or
        -- cache-control, takes one entry however many lines carry it, and set-cookie an entry a line (measured on
        -- Envoy 1.40.2): with a name on two lines Envoy may read the answer, so a 101st line is then refused.
        twice = twice or seen[name] ~= nil
        seen[name] = true
        if #headers >= PF_MOST_HEADERS then return twice and "refused" or "failure" end
        headers[#headers + 1] = { name, value }
      end
    end
    for _, h in ipairs(headers) do
      if h[3] then h[2], h[3] = table.concat(h[3], " "), nil end
    end
    if status == 101 then return "refused" end
    if status >= 200 then
      local frame = pf_framing(headers)
      if frame == nil then return "refused" end
      local kept = 0
      for _, h in ipairs(headers) do
        if not hop[h[1]] then kept = kept + #h[1] + #h[2] + 8 end
      end
      local body = ""
      if not (head or status == 204 or status == 304) then
        local keep = (status == 200) and 0 or (PF_MOST_DENIAL - kept)
        local most = (status == 200) and PF_MOST_ALLOWED_BODY or keep
        if keep < 0 then return "refused" end
        local _, cut
        body, _, cut = pf_body(sock, deadline, frame, keep, most)
        if body == nil then return cut and "failure" or "refused" end
      elseif status ~= 200 and kept > PF_MOST_DENIAL then
        return "refused"
      end
      return "answer", status, headers, body
    end
  end
end

-- Envoy answers a gRPC request's refusal as a gRPC call that failed: 200, the status mapped as gRPC maps an HTTP one
-- (grpc_status), and the body percent-encoded as grpc-message (measured on Envoy 1.40.2). A reply larger than
-- PF_MOST_DENIAL, counted as HAProxy stores it, is not sent: the configuration's pass rule answers the request, as it
-- answers every refusal of this script's own. A gRPC denial's message can be three times its body.
local function pf_refuse(txn, grpc, status, headers, body)
  local add, size, payload = {}, 0, ""
  local function keep(name, value)
    add[#add + 1] = { name, value }
    size = size + #name + #value + 8
  end
  local typed = false
  for _, h in ipairs(headers or {}) do
    if not hop[h[1]] and not (grpc and h[1] == "content-type") then keep(h[1], h[2]) end
    if h[1] == "content-type" then typed = true end
  end
  if grpc then
    keep("content-type", "application/grpc")
    keep("grpc-status", tostring(grpc_status[status] or 2))
    if body ~= nil and body ~= "" then
      keep("grpc-message", (body:gsub("[^ -$&-~]", function(c) return string.format("%%%02X", c:byte()) end)))
    end
  elseif body ~= nil and body ~= "" then
    if not typed then keep("content-type", "text/plain") end
    payload = body
  end
  if size + #payload > PF_MOST_DENIAL then return end
  local reply = txn:reply({ status = grpc and 200 or status })
  for _, h in ipairs(add) do reply:add_header(h[1], h[2]) end
  if payload ~= "" then reply:set_body(payload) end
  -- HAProxy writes content-length: 0 on a reply with no body, where Envoy writes none on a 304 or an HTTP/2 gRPC
  -- refusal (measured).
  if status == 304 or (grpc and txn.f:req_ver() == "2.0") then txn:set_var("txn.pf_no_length", true) end
  txn:done(reply)
end

-- The four headers send_request writes into the check's request.
local forwarded = { ["x-forwarded-uri"] = true, ["x-forwarded-method"] = true, ["x-forwarded-proto"] = true,
  ["x-forwarded-host"] = true }

core.register_action("pf_auth", { "http-req" }, function(txn, relay, path, timeout_ms, when_unavailable, send, accept, send_request)
  local sendp, acceptp = pf_split(send), pf_split(accept)
  local method = txn.sf:method()
  local req = txn.http:req_get_headers()
  local host = req["host"] and req["host"][0] or ""
  -- Envoy answers a refusal in gRPC's form when the content type is application/grpc alone or followed by +, compared
  -- as written: application/grpc;x=1 and Application/GRPC got the denial's own status (measured on Envoy 1.40.2).
  local ct = req["content-type"] and req["content-type"][0] or ""
  local grpc = ct == "application/grpc" or ct:find("^application/grpc%+") ~= nil
  local lines = { method .. " " .. path .. " HTTP/1.1" }
  local broken = false
  local function add(name, value)
    if (name .. value):find("[\r\n\0]") then broken = true end
    lines[#lines + 1] = name .. ": " .. value
  end
  add("host", host)
  for name, values in pairs(req) do
    -- A client's copy of a header the check's request writes itself never reaches the check, whatever send names:
    -- the framing headers, x-gateway-timeout-ms and, with send_request, the four forwarded ones. The Envoy output
    -- sets each with headers_to_add, which overrides a client's header of the same name (ext_authz.proto, v1.40.2).
    if not framing[name] and name ~= "x-gateway-timeout-ms" and not (send_request == "1" and forwarded[name])
        and (name == "authorization" or name == "x-gateway-action" or name == "x-gateway-reports"
        or pf_matches(name, sendp)) then
      -- Envoy sends a header the client wrote several times as one line, its values joined by commas (measured).
      local all, i = {}, 0
      while values[i] ~= nil do
        all[#all + 1] = values[i]
        i = i + 1
      end
      add(name, table.concat(all, ","))
    end
  end
  add("content-length", "0")
  add("connection", "close")
  add("x-gateway-timeout-ms", timeout_ms)
  add(internal_header, "true")
  add(expected_header, timeout_ms)
  if send_request == "1" then
    add("x-forwarded-uri", txn.sf:pathq())
    add("x-forwarded-method", method)
    add("x-forwarded-proto", req["x-forwarded-proto"] and req["x-forwarded-proto"][0] or "http")
    add("x-forwarded-host", host)
  end
  if broken then
    txn:done(txn:reply({ status = 400 }))
    return
  end
  local ms = tonumber(timeout_ms)
  local deadline = pf_now() + ms / 1000
  local sock = core.tcp()
  sock:settimeout(ms / 1000)
  pf_waiting[sock] = deadline
  pf_deadlines:push(deadline)
  local kind, status, headers, body = pf_ask(sock, relay, table.concat(lines, "\r\n") .. "\r\n\r\n", deadline,
    method == "HEAD")
  pf_waiting[sock] = false
  sock:close()
  -- An answer that ended at or after the deadline is one Envoy would not have waited for: an unanswered check. The
  -- closing task closes a socket from its deadline on, so the test is the same.
  if kind == "answer" and pf_now() >= deadline then kind = "failure" end
  txn:set_var("txn.pf_check", status or kind)
  if kind == "failure" and when_unavailable == "allow" then
    txn.http:req_set_header(unchecked, "true")
    txn:set_var("txn.pf_passed", true)
    return
  end
  if kind ~= "answer" then return end
  if status == 200 then
    local set = {}
    for _, h in ipairs(headers) do
      -- A check never changes how HAProxy frames or routes the request it passes on, so these are never set from
      -- its answer (Envoy 1.40.2 set each of them under an accept prefix that covers it: measured), nor is
      -- keep-alive, which Envoy never sets from it (measured).
      if pf_matches(h[1], acceptp) and not never_accepted[h[1]] then
        txn.http:req_set_header(h[1], h[2])
        set[h[1]] = h[2]
      end
    end
    -- A header that does not fit in the request buffer is dropped without an error (measured), so each is read back.
    local now = txn.http:req_get_headers()
    for name, value in pairs(set) do
      if not now[name] or now[name][0] ~= value or now[name][1] ~= nil then return end
    end
    txn:set_var("txn.pf_passed", true)
    return
  end
  pf_refuse(txn, grpc, (status < 300) and 403 or status, headers, body)
end, 7)
