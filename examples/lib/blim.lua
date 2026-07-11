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

-- Protected calls. golua hides the stock pcall/xpcall (renaming them to
-- unsafe_pcall/unsafe_xpcall) because Go panics used to bypass them and
-- corrupt the VM; since the golua panic-containment fix (upstream PR
-- aarzilli/golua#131) Go panics are contained at the cgo boundary and
-- re-raised as regular Lua errors, so the natives are safe again. Restore
-- the standard globals: scripts get plain Lua semantics — pcall/xpcall catch
-- errors from Lua code, C built-ins AND Go-backed functions (require,
-- blim.*). blim.pcall is a DEPRECATED backward-compatible alias — use the
-- standard pcall; the alias will be removed in a future release.
pcall = pcall or unsafe_pcall
xpcall = xpcall or unsafe_xpcall
blim.pcall = pcall



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

-- =============================================================================
-- blim.term — raw terminal mode + single-keypress input for interactive scripts
-- =============================================================================
-- Interactive bridges need single-key input that does NOT block the main loop
-- (BLE subscription callbacks only run while the loop yields in blim.sleep).
-- This module absorbs two classic pitfalls:
--   * struct termios layout and cc-index constants DIFFER per OS: Darwin has
--     VMIN=16/VTIME=17, Linux VMIN=6/VTIME=5. Using Linux indices on macOS
--     silently leaves VMIN=1 and every read blocks until a keypress.
--   * with VMIN=0 an empty stdio read (io.read) latches the FILE* EOF flag and
--     the keyboard goes dead permanently — read the raw fd via FFI instead.
--
-- API (all methods raise on unsupported platforms):
--   blim.term.enable_raw()       -> true | nil, err   (idempotent)
--   blim.term.disable_raw()      restore original settings (idempotent)
--   blim.term.read_char(wait_ms) -> char | nil | nil, msg, code
--       no wait_ms: non-blocking — returns nil immediately when no input
--       wait_ms:    wait up to wait_ms for a key; waiting yields via
--                   blim.sleep, so the engine mutex is released and BLE
--                   callbacks keep flowing while the script waits
--       Follows io.read semantics: a bare nil means "no key yet", while a
--       terminal condition returns nil, msg, code. code == blim.term.EOF
--       means stdin is closed/hung up (interactive loops MUST exit); any
--       other code is the read() errno.

blim.term = {}

do
    -- require() is a Go function, so it must never run under Lua-side
    -- protection (see the blim.pcall contract above) — probe package.preload
    -- instead (LuaJIT registers built-in modules there). protect guards only
    -- pure-C ffi.cdef calls, which is the safe use of blim.pcall.
    local protect = blim.pcall
    local ffi = package.preload.ffi and require("ffi") or nil
    local os_supported = ffi ~= nil and (ffi.os == "OSX" or ffi.os == "Linux")

    if not os_supported then
        local why = ffi and ("platform " .. ffi.os) or "missing LuaJIT FFI"
        local function unsupported()
            error("blim.term is not supported: " .. why, 2)
        end
        blim.term.enable_raw = unsupported
        blim.term.disable_raw = unsupported
        blim.term.read_char = unsupported
    else
        -- TODO(blim.term): consider moving enable_raw/read_char to the Go side
        -- (registered like blim.sleep): a poll(2) on fd 0 with the engine mutex
        -- released would remove the 10 ms polling granularity of read_char, and
        -- termios handling via x/sys/unix would replace the hand-maintained
        -- per-OS cdefs below.

        local STDIN_FILENO = 0
        local TCSANOW = 0

        -- termios constant VALUES are per-OS too
        local ICANON, ECHO, VMIN, VTIME
        if ffi.os == "OSX" then
            ICANON, ECHO, VMIN, VTIME = 0x00000100, 0x00000008, 16, 17
        else
            ICANON, ECHO, VMIN, VTIME = 0x0002, 0x0008, 6, 5
        end

        -- poll(2) event flags and EINTR (same values on Darwin and Linux)
        local POLLIN, POLLERR, POLLHUP = 0x0001, 0x0008, 0x0010
        local EINTR = 4

        -- read_char terminal codes (io.read-style third return value). EOF has
        -- no OS errno, so it gets a stable sentinel a caller can compare against
        -- blim.term.EOF; real read errors carry their actual errno.
        local ECODE_EOF = -1
        blim.term.EOF = ECODE_EOF

        local orig_termios, raw_termios, read_buf, poll_fds
        local raw_enabled = false

        -- Lazy FFI setup: blim.lua is preloaded into EVERY engine (go:embed),
        -- but most scripts never touch blim.term — defer cdef parsing and
        -- cdata allocation to first use.
        --
        -- struct termios layout differs between Darwin and Linux (field widths,
        -- c_line, c_cc size). cdefs are protect-guarded: scripts written before
        -- blim.term may declare the same types themselves.
        local function init_ffi()
            if orig_termios then return end -- already initialized
            if ffi.os == "OSX" then
                protect(ffi.cdef, [[
                    typedef unsigned long tcflag_t;
                    typedef unsigned char cc_t;
                    typedef unsigned long speed_t;

                    struct termios {
                        tcflag_t c_iflag;
                        tcflag_t c_oflag;
                        tcflag_t c_cflag;
                        tcflag_t c_lflag;
                        cc_t c_cc[20];
                        speed_t c_ispeed;
                        speed_t c_ospeed;
                    };
                ]])
            else
                protect(ffi.cdef, [[
                    typedef unsigned int tcflag_t;
                    typedef unsigned char cc_t;
                    typedef unsigned int speed_t;

                    struct termios {
                        tcflag_t c_iflag;
                        tcflag_t c_oflag;
                        tcflag_t c_cflag;
                        tcflag_t c_lflag;
                        cc_t c_line;
                        cc_t c_cc[32];
                        speed_t c_ispeed;
                        speed_t c_ospeed;
                    };
                ]])
            end
            protect(ffi.cdef, [[
                int tcgetattr(int fd, struct termios *termios_p);
                int tcsetattr(int fd, int optional_actions, const struct termios *termios_p);
                ssize_t read(int fd, void *buf, size_t count);

                struct pollfd {
                    int fd;
                    short events;
                    short revents;
                };
                int poll(struct pollfd *fds, unsigned long nfds, int timeout);
            ]])
            orig_termios = ffi.new("struct termios")
            raw_termios = ffi.new("struct termios")
            read_buf = ffi.new("unsigned char[1]")
            poll_fds = ffi.new("struct pollfd[1]")
        end

        ---Enable raw mode: no echo, no line buffering, non-blocking reads
        ---(VMIN=0/VTIME=0). Idempotent.
        ---@return boolean|nil ok, string|nil err
        function blim.term.enable_raw()
            if raw_enabled then return true end
            init_ffi()

            if ffi.C.tcgetattr(STDIN_FILENO, orig_termios) ~= 0 then
                return nil, "tcgetattr failed"
            end
            ffi.copy(raw_termios, orig_termios, ffi.sizeof("struct termios"))

            raw_termios.c_lflag = bit.band(raw_termios.c_lflag,
                                           bit.bnot(bit.bor(ICANON, ECHO)))
            raw_termios.c_cc[VMIN] = 0   -- don't wait for characters
            raw_termios.c_cc[VTIME] = 0  -- return immediately

            if ffi.C.tcsetattr(STDIN_FILENO, TCSANOW, raw_termios) ~= 0 then
                return nil, "tcsetattr failed"
            end
            raw_enabled = true
            return true
        end

        ---Restore original terminal settings. Idempotent.
        function blim.term.disable_raw()
            if not raw_enabled then return end
            ffi.C.tcsetattr(STDIN_FILENO, TCSANOW, orig_termios)
            raw_enabled = false
        end

        -- One non-blocking read attempt, gated by poll(2). The gate serves
        -- two purposes: read() is only called when input is actually pending
        -- (so this never blocks, even on a canonical-mode tty where raw mode
        -- could not be enabled), and it disambiguates read()==0 — with
        -- VMIN=0 that value means both "no key" and "stdin closed", but a
        -- POLLIN/POLLHUP event with zero bytes can only be EOF.
        --
        -- Returns, io.read style:
        --   char              a byte was read
        --   nil               nothing pending yet (transient; keep polling)
        --   nil, msg, code    terminal: stdin closed (code == ECODE_EOF) or a
        --                     hard read error (code == errno)
        local function try_read_char()
            poll_fds[0].fd = STDIN_FILENO
            poll_fds[0].events = POLLIN
            poll_fds[0].revents = 0
            if ffi.C.poll(poll_fds, 1, 0) <= 0 then
                return nil -- no pending input (or transient poll error)
            end
            local n = ffi.C.read(STDIN_FILENO, read_buf, 1)
            if n == 1 then
                return string.char(read_buf[0])
            end
            if n == 0 or bit.band(poll_fds[0].revents, bit.bor(POLLERR, POLLHUP)) ~= 0 then
                -- POLLIN with zero bytes / POLLHUP can only mean stdin closed.
                return nil, "end of file", ECODE_EOF
            end
            -- n < 0: EINTR is transient (retry later); any other errno is a
            -- terminal read error, reported io.read style.
            local errno = ffi.errno()
            if errno == EINTR then
                return nil
            end
            return nil, "read failed", errno
        end

        ---Read a single character from stdin.
        ---@param wait_ms number|nil max time to wait for a key; nil = non-blocking
        ---@return string|nil char nil when no input arrived in time
        ---@return string|nil err error message on a terminal condition
        ---@return integer|nil code errno, or blim.term.EOF when stdin is closed
        function blim.term.read_char(wait_ms)
            init_ffi()
            local ch, err, code = try_read_char()
            if ch or err or not wait_ms then
                return ch, err, code
            end
            -- Poll in 10 ms steps up to the deadline. blim.sleep is a
            -- Go-registered function that releases the engine mutex while
            -- sleeping, so BLE callbacks keep flowing during the wait.
            local step_ms = 10
            local steps = math.ceil(wait_ms / step_ms)
            for _ = 1, steps do
                blim.sleep(step_ms)
                ch, err, code = try_read_char()
                if ch or err then return ch, err, code end
            end
            return nil
        end
    end
end

-- Export as global blim
_G.blim = blim

return blim