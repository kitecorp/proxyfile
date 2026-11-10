-- Connect's status today for each code whose status the bridge writes from the old table.
local CONNECT_STATUS = {
  canceled = "499",
  deadline_exceeded = "504",
  failed_precondition = "400",
  unimplemented = "501",
}
local OLD_STATUS = {["404"] = true, ["408"] = true, ["412"] = true}

function envoy_on_response(response_handle)
  local headers = response_handle:headers()
  if not OLD_STATUS[headers:get(":status")] or headers:get("content-type") ~= "application/json" then
    return
  end
  local body = response_handle:body()
  if body == nil then
    return
  end
  -- The bridge writes the error with protobuf's JSON printer: its keys come in no fixed order
  -- (message before code, as measured), and a quote inside a string is always escaped, so the
  -- only "code":"..." pair in the body is the error's own.
  local code = body:getBytes(0, body:length()):match('"code"%s*:%s*"([%l_]+)"')
  local status = CONNECT_STATUS[code]
  if status ~= nil then
    -- Asked for again: the header object from before body() died while the body was awaited.
    response_handle:headers():replace(":status", status)
  end
end
