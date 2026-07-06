--- Device Control Bridge
-- Console-based control panel for BLE device with arm/disarm functionality.
-- Service 0xFF30:
--   0xFF31 Control: Write command, Indicate execution result [opcode:1][result:1]
--   0xFF32 Status:  Notify state changes, Read current status
-- @module device-control-bridge

-- Terminal raw mode and single-keypress input come from blim.term
-- (lib/blim.lua) - no per-script termios/FFI plumbing needed.
local blim = require("blim")

--------------------------------------------------------------------------------
-- Constants
--------------------------------------------------------------------------------

-- Service and Characteristic UUIDs
local SERVICE_UUID = "ff30"
local CONTROL_UUID = "ff31"  -- Write command, Indicate execution result
local STATUS_UUID = "ff32"   -- Notify state changes, Read current status

-- Control Commands (wire opcodes from ControlCommand enum)
-- 0x01=Arm, 0x02=Disarm, 0x04=CalibrateMag, 0x08=ConfigReset, 0xFF=Reboot
local CMD = {
    ARM = 0x01,
    DISARM = 0x02,
    CALIBRATE_MAG = 0x04,
    CONFIG_RESET = 0x08,
    REBOOT = 0xFF
}

-- Command names for display
local CMD_NAMES = {
    [CMD.ARM] = "Arm",
    [CMD.DISARM] = "Disarm",
    [CMD.CALIBRATE_MAG] = "Calibrate Mag",
    [CMD.CONFIG_RESET] = "Config Reset",
    [CMD.REBOOT] = "Reboot"
}

-- Result codes (second byte of indicate response) - matches ControlResult enum
local RESULT = {
    SUCCESS = 0x01,
    INVALID_COMMAND = 0x02,
    INVALID_STATE = 0x03,
    INTERNAL_ERROR = 0x04
}

-- Result names for display
local RESULT_NAMES = {
    [RESULT.SUCCESS] = "OK",
    [RESULT.INVALID_COMMAND] = "Invalid Command",
    [RESULT.INVALID_STATE] = "Invalid State",
    [RESULT.INTERNAL_ERROR] = "Internal Error"
}

--------------------------------------------------------------------------------
-- ANSI Color Codes
--------------------------------------------------------------------------------

local ANSI = {
    RESET = "\027[0m\027[40m\027[97m",  -- Reset then re-apply black bg + white fg
    BOLD = "\027[1m",
    DIM = "\027[2m",          -- Dimmed/gray text for disabled items
    BLINK = "\027[5m",        -- Slow blink (terminal support varies)
    RED = "\027[91m",
    GREEN = "\027[92m",
    YELLOW = "\027[93m",
    CYAN = "\027[96m",
    GRAY = "\027[90m",        -- Dark gray for disabled commands
    WHITE = "\027[97m",
    BG_BLACK = "\027[40m",    -- Black background for visibility
    BG_RED = "\027[41m",
    RESET_ALL = "\027[0m"     -- True reset for cleanup
}

-- System states (matches SystemState enum in firmware)
local STATE = {
    DISARMED = 0,
    ARMED = 1,
    FAILURE = 2,
    REBOOTING = 3,
    MAGCALIBRATE = 4
}

-- State names for display
local STATE_NAMES = {
    [STATE.DISARMED] = "DISARMED",
    [STATE.ARMED] = "ARMED",
    [STATE.FAILURE] = "FAILURE",
    [STATE.REBOOTING] = "REBOOTING",
    [STATE.MAGCALIBRATE] = "CALIBRATE:MAG"
}

--------------------------------------------------------------------------------
-- State
--------------------------------------------------------------------------------

local state = {
    system_state = STATE.DISARMED,  -- Current system state
    last_cmd = 0,            -- Last command sent
    last_result = 0,         -- Last result received
    connected = false,       -- Connection status
    log = {},                -- Notification log (max 10 entries)
    log_max = 10,            -- Maximum log entries to keep
    pending_cmd = nil        -- Pending command: {cmd=byte, sent_at=os.clock(), ack=false}
}

--------------------------------------------------------------------------------
-- Helper Functions
--------------------------------------------------------------------------------

--- Add entry to notification log (circular buffer).
-- @param msg Log message string
local function log_add(msg)
    table.insert(state.log, os.date("%H:%M:%S") .. " " .. msg)
    if #state.log > state.log_max then
        table.remove(state.log, 1)
    end
end

--- Debug print to stderr with timestamp.
-- @param msg Debug message
local function debug_print(msg)
    io.stderr:write(string.format("[DEBUG %s] %s\n", os.date("%H:%M:%S"), msg))
end

--- Format bytes as a hex string.
-- @param data Raw byte string
-- @return Hex string like "01 02 03"
local function to_hex(data)
    if not data or data == "" then return "(empty)" end
    local hex = {}
    for i = 1, #data do
        hex[i] = string.format("%02X", string.byte(data, i))
    end
    return table.concat(hex, " ")
end

--- Clear terminal screen, set black background, and move cursor to top.
local function clear_screen()
    -- Set black bg FIRST, then clear (clear uses current bg color), then home
    io.write(ANSI.BG_BLACK .. ANSI.WHITE .. "\027[2J\027[H")
    io.flush()
end

--- Print status display with colors based on system state.
local function print_status()
    clear_screen()

    print(ANSI.BOLD .. "=== Device Control Panel ===" .. ANSI.RESET)
    print(string.format("Device: %s", blim.device.address))
    print("")

    -- Status display with state-specific coloring
    local state_name = STATE_NAMES[state.system_state] or string.format("Unknown 0x%02X", state.system_state)
    if state.system_state == STATE.ARMED then
        print("State: " .. ANSI.RED .. ANSI.BOLD .. ANSI.BLINK ..
              ">>> ARMED <<<" .. ANSI.RESET)
    elseif state.system_state == STATE.FAILURE then
        print("State: " .. ANSI.RED .. ANSI.BOLD .. "FAILURE" .. ANSI.RESET)
    elseif state.system_state == STATE.REBOOTING then
        print("State: " .. ANSI.YELLOW .. ANSI.BOLD .. "REBOOTING" .. ANSI.RESET)
    elseif state.system_state == STATE.MAGCALIBRATE then
        print("State: " .. ANSI.CYAN .. ANSI.BOLD .. "CALIBRATE MAG" .. ANSI.RESET)
    elseif state.system_state == STATE.DISARMED then
        print("State: " .. ANSI.GREEN .. "DISARMED" .. ANSI.RESET)
    else
        print("State: " .. ANSI.GRAY .. state_name .. ANSI.RESET)
    end
    print("")

    -- Command buttons - based on firmware sec_policy (hardening.h)
    print(ANSI.BOLD .. "Commands:" .. ANSI.RESET)
    if state.system_state == STATE.DISARMED then
        -- kDisarmedPolicy: Arm, ConfigReset, Reboot
        print("  " .. ANSI.YELLOW .. "[a]" .. ANSI.RESET .. " Arm")
        print("  " .. ANSI.YELLOW .. "[c]" .. ANSI.RESET .. " Calibrate Mag")
        print("  " .. ANSI.YELLOW .. "[f]" .. ANSI.RESET .. " Config Reset")
        print("  " .. ANSI.YELLOW .. "[r]" .. ANSI.RESET .. " Reboot")
    elseif state.system_state == STATE.ARMED then
        -- kArmedPolicy: Disarm, Reboot
        print("  " .. ANSI.YELLOW .. "[d]" .. ANSI.RESET .. " Disarm")
        print("  " .. ANSI.YELLOW .. "[r]" .. ANSI.RESET .. " Reboot")
    elseif state.system_state == STATE.FAILURE or state.system_state == STATE.MAGCALIBRATE then
        -- kFailurePolicy / kCalibrateMagPolicy: Disarm, ConfigReset, Reboot
        print("  " .. ANSI.YELLOW .. "[d]" .. ANSI.RESET .. " Disarm")
        print("  " .. ANSI.YELLOW .. "[f]" .. ANSI.RESET .. " Config Reset")
        print("  " .. ANSI.YELLOW .. "[r]" .. ANSI.RESET .. " Reboot")
    elseif state.system_state == STATE.REBOOTING then
        -- kRebootingPolicy: Reboot only
        print("  " .. ANSI.YELLOW .. "[r]" .. ANSI.RESET .. " Reboot")
    else
        print("  " .. ANSI.GRAY .. "(unknown state)" .. ANSI.RESET)
    end
    print("  " .. ANSI.CYAN .. "[q]" .. ANSI.RESET .. " Quit")
    print("")

    -- Notification log
    print(ANSI.BOLD .. "Log:" .. ANSI.RESET)
    if #state.log == 0 then
        print("  (no notifications yet)")
    else
        for _, entry in ipairs(state.log) do
            print("  " .. entry)
        end
    end
    print("")
    io.write("> ")
    io.flush()
end

--------------------------------------------------------------------------------
-- BLE Operations
--------------------------------------------------------------------------------

--- Get the control characteristic handle (write commands, indicate results).
-- @return Characteristic object or nil
local function get_control_char()
    return blim.characteristic(SERVICE_UUID, CONTROL_UUID)
end

--- Get the status characteristic handle (notify state changes, read status).
-- @return Characteristic object or nil
local function get_status_char()
    return blim.characteristic(SERVICE_UUID, STATUS_UUID)
end

-- Command execution notification polling
local POLL_INTERVAL_MS = 100      -- Check every 10ms
local POLL_MAX_ITERATIONS = 50   -- 20 * 10ms = 200ms timeout

--- Update state from status characteristic (ff32).
-- @param data Raw status byte(s)
local function update_state_from_status(data)
    if not data or #data < 1 then
        log_add("Invalid status data: " .. to_hex(data))
        return
    end

    local status_byte = string.byte(data, 1)
    local old_state = state.system_state
    state.system_state = status_byte

    if state.system_state ~= old_state then
        local state_name = STATE_NAMES[status_byte] or string.format("Unknown 0x%02X", status_byte)
        log_add("State: " .. state_name)
    end
end

--- Send a command to the device control characteristic.
-- @param cmd Command byte (from CMD table)
-- @return true on success, nil + error message on failure
local function send_command(cmd)
    local char = get_control_char()
    if not char then
        return nil, "Control characteristic not found"
    end

    local cmd_name = CMD_NAMES[cmd] or string.format("0x%02X", cmd)
    log_add("Sending: " .. cmd_name)

    -- Track pending command for timeout detection
    state.pending_cmd = {cmd = cmd, sent_at = os.clock(), ack = false}
    state.last_cmd = cmd

    -- Write command byte (with response)
    local _, err = char.write(string.char(cmd), true)
    if err then
        log_add("ERROR: " .. err)
        state.pending_cmd = nil
        return nil, err
    end

    -- Poll for indicate response
    debug_print("Starting poll loop for " .. cmd_name)
    for i = 1, POLL_MAX_ITERATIONS do
        blim.sleep(POLL_INTERVAL_MS)
        debug_print(string.format("Poll %d: ack=%s", i, tostring(state.pending_cmd.ack)))
        if state.pending_cmd.ack then
            debug_print("ACK received, exiting loop")
            break
        end
    end

    -- Check for timeout
    if not state.pending_cmd.ack then
        log_add("TIMEOUT: No response for " .. cmd_name)
        debug_print("TIMEOUT - no ack")
    end
    state.pending_cmd = nil

    return true
end

--- Parse control indicate data (ff31) and update state.
-- @param data Raw bytes (2 bytes: command + result)
local function parse_control_indicate(data)
    debug_print("parse_control_indicate called: " .. to_hex(data))
    if not data or #data < 2 then
        log_add("Invalid control data: " .. to_hex(data))
        return
    end

    local cmd_byte = string.byte(data, 1)
    local result_byte = string.byte(data, 2)
    debug_print(string.format("cmd=0x%02X result=0x%02X pending=%s",
        cmd_byte, result_byte, state.pending_cmd and "yes" or "no"))

    -- Acknowledge pending command and log latency
    local latency_str = ""
    if state.pending_cmd and state.pending_cmd.cmd == cmd_byte then
        local latency_ms = (os.clock() - state.pending_cmd.sent_at) * 1000
        latency_str = string.format(" (%.1fms)", latency_ms)
        state.pending_cmd.ack = true
    end

    -- Note: System state is updated via ff32 status notifications, not here
    state.last_cmd = cmd_byte
    state.last_result = result_byte

    local cmd_name = CMD_NAMES[cmd_byte] or string.format("0x%02X", cmd_byte)
    local result_str = RESULT_NAMES[result_byte] or string.format("Unknown 0x%02X", result_byte)
    log_add(string.format("%s -> %s%s", cmd_name, result_str, latency_str))
end

--------------------------------------------------------------------------------
-- Command Handlers
--------------------------------------------------------------------------------

--- Handle keyboard input command.
-- Command handling based on firmware sec_policy (hardening.h)
-- @param key Single character key pressed
-- @return true to continue, false to quit
local function handle_command(key)
    if key == "q" or key == "Q" then
        return false
    end

    -- State-aware command handling based on firmware policies
    if state.system_state == STATE.DISARMED then
        -- kDisarmedPolicy: Arm, CalibrateMag, ConfigReset, Reboot
        if key == "a" or key == "A" then
            send_command(CMD.ARM)
        elseif key == "c" or key == "C" then
            send_command(CMD.CALIBRATE_MAG)
        elseif key == "f" or key == "F" then
            send_command(CMD.CONFIG_RESET)
        elseif key == "r" or key == "R" then
            send_command(CMD.REBOOT)
        end
    elseif state.system_state == STATE.ARMED then
        -- kArmedPolicy: Disarm, Reboot
        if key == "d" or key == "D" then
            send_command(CMD.DISARM)
        elseif key == "r" or key == "R" then
            send_command(CMD.REBOOT)
        end
    elseif state.system_state == STATE.FAILURE or state.system_state == STATE.MAGCALIBRATE then
        -- kFailurePolicy / kCalibrateMagPolicy: Disarm, ConfigReset, Reboot
        if key == "d" or key == "D" then
            send_command(CMD.DISARM)
        elseif key == "f" or key == "F" then
            send_command(CMD.CONFIG_RESET)
        elseif key == "r" or key == "R" then
            send_command(CMD.REBOOT)
        end
    elseif state.system_state == STATE.REBOOTING then
        -- kRebootingPolicy: Reboot only
        if key == "r" or key == "R" then
            send_command(CMD.REBOOT)
        end
    end

    return true
end

--------------------------------------------------------------------------------
-- Main
--------------------------------------------------------------------------------

-- Verify service exists
local services = blim.list()
if not services[SERVICE_UUID] then
    print("")
    print(string.format("Device: %s", blim.device.address))
    print(string.format("ERROR: Service 0x%s not found", SERVICE_UUID))
    print("")
    error("Required service not available")
end

-- Subscribe to both characteristics with appropriate modes:
-- ff31 Control: Indicate with command execution result
-- ff32 Status: Notify on state changes
blim.subscribe{
    services = {
        { service = SERVICE_UUID, chars = { CONTROL_UUID }, indicate = true },
        { service = SERVICE_UUID, chars = { STATUS_UUID } }
    },
    Mode = "EveryUpdate",
    MaxRate = 0,
    Callback = function(record)
        debug_print("Callback invoked")
        -- Handle control indicate (ff31)
        local control_data = record.Values[CONTROL_UUID]
        if control_data then
            debug_print("Control data received: " .. to_hex(control_data))
            parse_control_indicate(control_data)
        end

        -- Handle status notify (ff32)
        local status_data = record.Values[STATUS_UUID]
        if status_data then
            debug_print("Status data received: " .. to_hex(status_data))
            update_state_from_status(status_data)
        end

        print_status()
    end
}

state.connected = true
log_add("Connected to device")

-- Read initial state from ff32 (using same handler as notifications)
local status_char = get_status_char()
if status_char then
    local initial_status, err = status_char.read()
    if initial_status then
        update_state_from_status(initial_status)
    elseif err then
        log_add("Failed to read state: " .. err)
    end
end

-- Initial display
print_status()

-- Enable raw terminal mode for single-keypress input
local ok, err = blim.term.enable_raw()
if not ok then
    print("Warning: Could not enable raw mode: " .. (err or "unknown"))
    print("Falling back to line-based input (press Enter after each key)")
end

-- Ensure terminal is restored on exit
local function cleanup(silent)
    blim.term.disable_raw()
    io.write(ANSI.RESET_ALL)  -- Reset ALL colors to terminal default
    if not silent then
        print("\nExiting...")
    end
end

-- Main input loop: single keypress (no Enter needed). read_char(200) waits
-- via blim.sleep, so status notifications keep arriving while idle (a
-- blocking read would freeze BLE callbacks between keypresses); on shutdown
-- the context-aware wait lets the engine abort the loop. When raw mode could
-- not be enabled, the same loop degrades to line-based input: keys become
-- available after Enter, and read_char never blocks (poll-gated).
while true do
    local key, err = blim.term.read_char(200)
    if key then
        -- Handle Ctrl+C (ASCII 3) and Ctrl+D (ASCII 4)
        if key == "\3" or key == "\4" then
            cleanup()
            break
        end

        local should_continue = handle_command(key)
        if should_continue then
            print_status()
        else
            cleanup()
            break
        end
    elseif err then
        -- terminal condition (stdin closed on shutdown, or a read error) -
        -- restore the terminal and exit silently
        cleanup(true)
        break
    end
    -- bare nil: no key within the poll window - keep waiting
end