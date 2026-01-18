-- BLIM API - Lua wrapper around Go-implemented functions
-- CGO-like approach: Lua functions call Go backends via _blim_internal

local blim = {}
local native = _blim_internal

-- Direct assignments (zero overhead - references to Go functions)
blim.subscribe = native.subscribe
blim.list = native.list
blim.characteristic = native.characteristic
blim.device = native.device
blim.bridge = native.bridge
blim.sleep = native.sleep



-- Helper functions for Lua scripts

-- Format a named object as "<name> (0xuuid)" or just the uuid with 0x prefix
-- Short UUIDs (4-8 hex chars) get 0x prefix, long UUIDs stay as-is
-- Examples:
--   maybe_named = { name = "Heart Rate", uuid = "180d" }
--   -> "Heart Rate (0x180d)"
--   maybe_named = { name = "Apple Service", uuid = "6e400001b5a3f393e0a9e50e24dcca9e" }
--   -> "Apple Service (6e400001b5a3f393e0a9e50e24dcca9e)"
function blim.format_named(maybe_named)
    if not maybe_named or not maybe_named.uuid then
        return ""
    end
    local uuid_lower = string.lower(maybe_named.uuid)

    -- Add 0x prefix for short UUIDs (16-bit: 4 chars, 32-bit: 8 chars)
    -- Long 128-bit UUIDs (32 chars) don't get prefix as they're obviously hex
    local uuid_display = uuid_lower
    if #uuid_lower <= 8 then
        uuid_display = "0x" .. uuid_lower
    end

    if maybe_named.name and maybe_named.name ~= "" then
        return string.format("%s (%s)", maybe_named.name, uuid_display)
    else
        return uuid_display
    end
end

-- Convert byte string to hex representation with spaces between bytes
-- Example: "AB\x01" -> "41 42 01"
function blim.to_hex(data)
    if not data or data == "" then
        return ""
    end
    local hex = {}
    for i = 1, #data do
        hex[i] = string.format("%02X", string.byte(data, i))
    end
    return table.concat(hex, " ")
end

function blim.to_little_endian_bytes(data)
    if not data or data == "" then return "" end
    local result = {}
    for i = #data, 1, -1 do
        table.insert(result, data:sub(i, i))
    end
    return table.concat(result)
end

-- Convert byte string to hex representation without spaces (uppercase)
-- Example: "AB\x01" -> "4142FF"
function blim.bytes_to_hex(data)
    if not data or data == "" then
        return ""
    end
    return string.upper(data:gsub(".", function(c)
        return string.format("%02X", string.byte(c))
    end))
end

-- Convert byte string to printable ASCII (non-printable chars become '.')
-- Example: "Hello\x00World" -> "Hello.World"
function blim.to_ascii(data)
    if not data or data == "" then
        return ""
    end
    local ascii = {}
    for i = 1, #data do
        local b = string.byte(data, i)
        ascii[i] = (b >= 32 and b <= 126) and string.char(b) or "."
    end
    return table.concat(ascii)
end


-- Shorten UUID (show only first eight chars for long UUIDs)
-- Example: "6e400001-b5a3-f393-e0a9-e50e24dcca9e" -> "6e400001"
function blim.short_uuid(uuid)
    if not uuid then
        return ""
    end
    if #uuid > 8 then
        return uuid:sub(1, 8)
    end
    return uuid
end


--- Write to characteristic and wait for notification/indication response (sync, blocking).
-- @param opts.service Service UUID (required)
-- @param opts.write_char Char to write to (required)
-- @param opts.data Bytes to write (required)
-- @param opts.match Matcher function(data) -> -1/0/1 (required)
--   -1 = operation failed (returns data, "failed")
--    0 = not our response, keep waiting
--    1 = success (returns data, nil)
-- @param opts.notify_char Char to receive from (default: write_char)
-- @param opts.write_response Write with response (default: true)
-- @param opts.indicate Use indicate vs notify (default: false)
-- @param opts.timeout Timeout in ms (default: 5000)
-- @return data, err
--   data, nil       = success (matcher returned 1)
--   data, "failed"  = operation failed (matcher returned -1)
--   nil, "timeout"  = no matching response within timeout
--   nil, "write: <msg>"       = write to characteristic failed
--   nil, "subscribe: <msg>"   = subscription setup failed
--   nil, "characteristic not found: <uuid>" = char not available
function blim.write_receive(opts)
    -- Validate required arguments
    if not opts then return nil, "opts required" end
    if not opts.service then return nil, "service required" end
    if not opts.write_char then return nil, "write_char required" end
    if not opts.data then return nil, "data required" end
    if not opts.match then return nil, "match required" end
    if type(opts.match) ~= "function" then return nil, "match must be function" end

    local notify_char = opts.notify_char or opts.write_char

    -- Resolve characteristic before subscribing (fail fast)
    local char = blim.characteristic(opts.service, opts.write_char)
    if not char then
        return nil, "characteristic not found: " .. opts.write_char
    end

    local status = nil  -- nil=pending, false=failed, true=succeeded
    local response_data = nil

    -- Create temporary subscription
    local cancel, sub_err = blim.subscribe{
        services = {{
            service = opts.service,
            chars = { notify_char },
            indicate = opts.indicate or false
        }},
        Callback = function(record)
            local data = record.Values[notify_char]
            if data then
                local m = opts.match(data)
                if m == 1 then
                    status = true
                    response_data = data
                elseif m == -1 then
                    status = false
                    response_data = data
                end
                -- m == 0: not our response, keep waiting
            end
        end
    }
    if sub_err then
        return nil, "subscribe: " .. sub_err
    end

    -- Write data
    local _, write_err = char.write(opts.data, opts.write_response ~= false)
    if write_err then
        cancel()
        return nil, "write: " .. write_err
    end

    -- Poll until status set or timeout
    local poll_ms = 50
    local iterations = math.ceil((opts.timeout or 5000) / poll_ms)
    for i = 1, iterations do
        blim.sleep(poll_ms)
        if status ~= nil then break end
    end

    cancel()

    if status == nil then
        return nil, "timeout"
    elseif status == false then
        return response_data, "failed"
    end
    return response_data, nil
end


--- Write to characteristic and subscribe for response (async, non-blocking).
-- Returns immediately after write. User's callback receives (cancel, data).
-- User MUST call cancel() when done to cleanup subscription.
-- @param opts.service Service UUID (required)
-- @param opts.write_char Char to write to (required)
-- @param opts.data Bytes to write (required)
-- @param opts.callback User callback: callback(cancel, data) - user MUST call cancel() to cleanup
-- @param opts.notify_char Char to receive from (default: write_char)
-- @param opts.write_response Write with response (default: true)
-- @param opts.indicate Use indicate vs notify (default: false)
-- @return err (nil on success)
--   nil = success, subscription active
--   "subscribe: <msg>" = subscription failed
--   "write: <msg>" = write failed
--   "characteristic not found: <uuid>" = char unavailable
function blim.write_receive_async(opts)
    -- Validate required arguments
    if not opts then return "opts required" end
    if not opts.service then return "service required" end
    if not opts.write_char then return "write_char required" end
    if not opts.data then return "data required" end
    if not opts.callback then return "callback required" end
    if type(opts.callback) ~= "function" then return "callback must be function" end

    local notify_char = opts.notify_char or opts.write_char

    -- Resolve characteristic before subscribing (fail fast)
    local char = blim.characteristic(opts.service, opts.write_char)
    if not char then
        return "characteristic not found: " .. opts.write_char
    end

    -- Use table indirection to avoid closure timing issue:
    -- cancel is assigned AFTER subscribe returns, but callback may fire
    -- before assignment completes. Table reference allows late binding.
    local state = { cancel = function() end }  -- no-op placeholder

    local cancel_fn, sub_err = blim.subscribe{
        services = {{
            service = opts.service,
            chars = { notify_char },
            indicate = opts.indicate or false
        }},
        Callback = function(record)
            local data = record.Values[notify_char]
            if data then
                opts.callback(state.cancel, data)
            end
        end
    }

    if sub_err then
        return "subscribe: " .. sub_err
    end

    state.cancel = cancel_fn  -- Now safe - callback will see real cancel

    -- Write data
    local _, write_err = char.write(opts.data, opts.write_response ~= false)
    if write_err then
        cancel_fn()
        return "write: " .. write_err
    end

    return nil  -- Success
end


-- Export as global blim
_G.blim = blim

return blim