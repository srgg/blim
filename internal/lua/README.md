# BLIM Lua API Reference

Lua scripting interface for Bluetooth Low Energy device interaction.

## Output Functions

### `print(...)`
Prints values to stdout with tab separators and automatic newline.

```lua
print("Hello", 42, true)  -- Output: Hello\t42\ttrue\n
```

### `io.write(...)`
Writes values to stdout without separators or automatic newline.

```lua
io.write("Hello")         -- Output: Hello
io.write("Line\n")        -- Output: Line\n
```

### `io.stderr:write(...)`
Writes values to stderr without separators or automatic newline.

```lua
io.stderr:write("Error: ")       -- Output to stderr: Error:
io.stderr:write("failed\n")      -- Output to stderr: failed\n

-- Combined error message
io.stderr:write("Error: ", "connection failed\n")  -- Output to stderr: Error: connection failed\n
```

## JSON Library

### `json.encode(table)`
Encodes a Lua table to JSON string.

```lua
local json = require("json")
local data = {name = "sensor", value = 42}
print(json.encode(data))  -- {"name":"sensor","value":42}
```

### `json.decode(string)`
Decodes a JSON string to Lua table.

```lua
local json = require("json")
local obj = json.decode('{"temp":23.5}')
print(obj.temp)  -- 23.5
```

## BLIM API

The global `blim` table provides BLE functionality.

### `blim.device`
Read-only table containing device information.

**Fields:**
- `id` (string) - Device ID
- `address` (string) - MAC address (e.g., "AA:BB:CC:DD:EE:FF")
- `name` (string) - Device name (may be empty)
- `rssi` (number) - Signal strength in dBm
- `connectable` (boolean) - Whether device accepts connections
- `tx_power` (number, optional) - Transmit power in dBm
- `advertised_services` (array) - Service UUIDs from advertisements
- `manufacturer_data` (table or nil) - Manufacturer data object (nil if no manufacturer data), with:
  - `value` (string) - Hex-encoded raw manufacturer data
  - `parsed_value` (table, optional) - Parsed manufacturer data structure (only if parser registered for this manufacturer)
    - `vendor` (table, optional) - Vendor information (present if parser implements VendorInfo interface)
      - `id` (number) - Bluetooth SIG Company Identifier
      - `name` (string, optional) - Human-readable vendor name (nil if vendor unknown in database)
    - **Note:** Format of additional fields varies by manufacturer and device type
    - Example for BLIMCo devices (vendor ID 0xFFFE):
      - `vendor` (table) - `{id = 0xFFFE, name = "BLIMCo"}`
      - `device_type` (string) - Device type name (e.g., "BLE Test Device", "IMU Streamer")
      - `hardware_version` (string) - Hardware version (e.g., "1.0")
      - `firmware_version` (string) - Firmware version (e.g., "2.1.3")
- `service_data` (table) - Map of service UUID to hex-encoded data

**Example:**
```lua
print("Device:", blim.device.name)
print("Address:", blim.device.address)
print("RSSI:", blim.device.rssi, "dBm")

-- Iterate advertised services
for i, uuid in ipairs(blim.device.advertised_services) do
    print("Service:", uuid)
end

-- Access service data
for uuid, data in pairs(blim.device.service_data) do
    print(uuid, "=>", data)
end
```

### `blim.bridge`
Read-only table containing bridge information (only available when running in bridge mode).

**Getter Functions** (call them — they are functions, not fields):
- `tty_name()` - Returns TTY device path (e.g., "/dev/ttys010")
- `tty_symlink()` - Returns tty symlink (empty if not created)

Both raise an error when called outside bridge mode, so a bridge script (which
always runs under `blim bridge`) can call them directly.

**Example:**
```lua
-- A bridge script always runs in bridge mode; the getters are safe to call.
print("Bridge PTY:", blim.bridge.tty_name())

local symlink = blim.bridge.tty_symlink()
if symlink ~= "" then
    print("TTY Symlink:", symlink)
end
```

### `blim.bridge.pty_write(data)`
Writes data to the PTY master (only available in bridge mode).

**Parameters:**
- `data` (string) - Data to write to the PTY

**Returns:** `(bytes_written, nil)` on success or `(nil, error_message)` on failure

**Example:**
```lua
-- Write data to PTY
local bytes, err = blim.bridge.pty_write("Hello from Lua\n")
if err then
    print("Write failed:", err)
else
    print("Wrote", bytes, "bytes to PTY")
end

-- Write binary data
local binary_data = "\x01\x02\x03\xFF"
local bytes, err = blim.bridge.pty_write(binary_data)
if err then
    print("Write failed:", err)
else
    print("Wrote", bytes, "bytes of binary data")
end
```

### `blim.bridge.pty_read([max_bytes])`
Reads data from the PTY master in non-blocking mode (only available in bridge mode).

**Parameters:**
- `max_bytes` (number, optional) - Maximum bytes to read (default: 4096, must be positive)

**Returns:**
- `(data, nil)` on success - `data` contains the bytes read
- `("", nil)` if no data available (non-blocking)
- `(nil, error_message)` on failure

**Example:**
```lua
-- Read available data (up to 4096 bytes)
local data, err = blim.bridge.pty_read()
if err then
    print("Read failed:", err)
elseif data == "" then
    print("No data available")
else
    print("Read", #data, "bytes:", data)
end

-- Read with custom buffer size
local data, err = blim.bridge.pty_read(128)
if err then
    print("Read failed:", err)
elseif data == "" then
    print("No data available")
else
    print("Read", #data, "bytes (max 128):", data)
end

-- Process binary data
local data, err = blim.bridge.pty_read()
if err then
    print("Read failed:", err)
elseif data ~= "" then
    -- Convert to hex for display
    for i = 1, #data do
        io.write(string.format("%02X ", string.byte(data, i)))
    end
    print()
end
```

**Example: Bidirectional PTY Communication**
```lua
-- Write a command to PTY
local bytes, err = blim.bridge.pty_write("ping\n")
if err then
    print("Write failed:", err)
    return
end

-- Read response (non-blocking, may need to poll)
local data, err = blim.bridge.pty_read()
if err then
    print("Read failed:", err)
elseif data == "" then
    print("No response yet")
else
    print("Response:", data)
end
```

### `blim.bridge.pty_on_data(callback)`
Registers a callback function to be invoked asynchronously when data arrives on the PTY (only available in bridge mode).

**Parameters:**
- `callback` (function or nil) - Callback function with signature `function(data)` where `data` is a string containing the received bytes
  - Pass `nil` to unregister the callback

**Returns:** Nothing

**Notes:**
- Callback is invoked asynchronously on a background thread
- Only one callback can be registered at a time (registering a new one replaces the old one)
- Callback receives binary-safe data (can contain null bytes)
- Errors in callback are caught and logged (won't crash the process)

**Example: Async PTY data handling**
```lua
-- Register callback for incoming PTY data
blim.bridge.pty_on_data(function(data)
    print("Received from PTY:", data)

    -- Process binary data
    for i = 1, #data do
        local byte = string.byte(data, i)
        io.write(string.format("%02X ", byte))
    end
    print()
end)

-- Main script continues executing...
print("Waiting for PTY data (callback will handle it asynchronously)")
blim.sleep(5000)

-- Unregister callback when done
blim.bridge.pty_on_data(nil)
print("Callback unregistered")
```

**Example: Protocol handler with state**
```lua
-- State machine for processing protocol messages
local buffer = ""
local message_count = 0

blim.bridge.pty_on_data(function(data)
    -- Append to buffer
    buffer = buffer .. data

    -- Process complete messages (example: newline-delimited)
    while true do
        local newline_pos = string.find(buffer, "\n")
        if not newline_pos then
            break  -- No complete message yet
        end

        -- Extract message
        local message = string.sub(buffer, 1, newline_pos - 1)
        buffer = string.sub(buffer, newline_pos + 1)

        -- Process message
        message_count = message_count + 1
        print("Message #" .. message_count .. ":", message)

        -- Echo back with BLE data
        local char = blim.characteristic("180f", "2a19")
        local battery, err = char.read()
        if battery then
            local response = "Battery: " .. string.byte(battery, 1) .. "%\n"
            blim.bridge.pty_write(response)
        end
    end
end)

-- Keep script running
while true do
    blim.sleep(1000)
end
```

**Example: Replace old callback**
```lua
-- First callback
blim.bridge.pty_on_data(function(data)
    print("Handler A:", data)
end)

blim.sleep(2000)

-- Replace with new callback (old one is automatically unregistered)
blim.bridge.pty_on_data(function(data)
    print("Handler B:", data)
end)
```

### `blim.list()`
Returns a table mapping service UUIDs to service info.

**Returns:** `{ [service_uuid] = { name = "...", characteristics = {char_uuid, ...} } }`

**Service info fields:**
- `name` (string, optional) - Human-readable service name (e.g., "Heart Rate" for UUID "180d"). Only present for standard BLE services.
- `characteristics` (array) - Array of characteristic UUIDs

**Example:**
```lua
local services = blim.list()

for service_uuid, service_info in pairs(services) do
    print("Service:", service_uuid)

    for i, char_uuid in ipairs(service_info.characteristics) do
        print("  Char:", char_uuid)
    end
end
```

**Output:**
```
Service: 180d
  Char: 2a37
  Char: 2a38
Service: 180f
  Char: 2a19
```

### `blim.subscribe(config)` → `cancel`
Subscribes to BLE characteristic notifications/indications.

**Parameters:**
- `config` (table) - Subscription configuration

**Returns:**
- `cancel` (function) - Call to stop the subscription and release resources. Safe to call multiple times (idempotent).

**Config fields:**
- `services` (array) - List of service/characteristic subscriptions
  - Each entry: `{service="UUID", chars={"UUID", ...}, indicate=bool}`
  - `indicate` (boolean, optional) - Subscription mode per service (default: false)
    - `false` - Subscribe to Notify (default). Fails if characteristic doesn't support Notify.
    - `true` - Subscribe to Indicate. Fails if characteristic doesn't support Indicate.
- `Mode` (string, optional) - Streaming mode (default: "EveryUpdate")
  - `"EveryUpdate"` - Every characteristic update triggers callback
  - `"Batched"` - Multiple updates batched together
  - `"Aggregated"` - Latest value per characteristic
- `MaxRate` (number, optional) - Max callback rate in milliseconds (0 = unlimited)
- `DrainDuration` (number, optional) - Milliseconds to discard stale cached values before delivering fresh notifications (0 = disabled). Useful for characteristics that buffer old values.
- `Callback` (function) - Called with each record: `function(record, cancel)`
  - `record` (table) - The notification data
  - `cancel` (function) - Call to stop this subscription from within the callback (self-cancellation)

**Record structure:**
- `TsUs` (number) - Timestamp in microseconds
- `Seq` (number) - Sequence number
- `Flags` (number) - Record flags
- `Values` (table, EveryUpdate/Aggregated) - Map of characteristic UUID to byte string
- `BatchValues` (table, Batched) - Map of characteristic UUID to array of byte strings

**Example: EveryUpdate mode**
```lua
local json = require("json")

blim.subscribe{
    services = {
        {service="180d", chars={"2a37"}},  -- Heart Rate
        {service="180f", chars={"2a19"}}   -- Battery
    },
    Mode = "EveryUpdate",
    MaxRate = 100,  -- Max 10 Hz
    Callback = function(record)
        -- Access characteristic values
        for uuid, data in pairs(record.Values) do
            -- Convert byte string to hex
            local hex = string.format("%02X", string.byte(data, 1))
            print(json.encode{
                seq = record.Seq,
                char = uuid,
                value = hex
            })
        end
    end
}
```

**Example: Batched mode**
```lua
blim.subscribe{
    services = {
        {service="180d", chars={"2a37", "2a38"}}
    },
    Mode = "Batched",
    MaxRate = 1000,  -- Max 1 Hz
    Callback = function(record)
        for uuid, values in pairs(record.BatchValues) do
            print("Characteristic:", uuid)
            print("  Received", #values, "updates")
            -- values is an array of byte strings
            for i, data in ipairs(values) do
                print("  [" .. i .. "]", string.byte(data, 1))
            end
        end
    end
}
```

**Example: Aggregated mode**
```lua
blim.subscribe{
    services = {
        {service="180d", chars={"2a37"}}
    },
    Mode = "Aggregated",
    MaxRate = 500,  -- Max 2 Hz
    Callback = function(record)
        -- Only latest value per characteristic
        for uuid, data in pairs(record.Values) do
            local value = string.byte(data, 1)
            print("Latest " .. uuid .. ":", value)
        end
    end
}
```

**Example: Subscribe to Indicate (instead of Notify)**
```lua
-- For characteristics that use Indicate (requires client acknowledgment)
-- Common for command/control characteristics that need reliable delivery
blim.subscribe{
    services = {
        {service="ff30", chars={"ff31"}, indicate=true}  -- Control characteristic with Indicate
    },
    Mode = "EveryUpdate",
    Callback = function(record)
        local data = record.Values["ff31"]
        if data and #data >= 2 then
            local opcode = string.byte(data, 1)
            local result = string.byte(data, 2)
            print(string.format("Command 0x%02X result: 0x%02X", opcode, result))
        end
    end
}
```

**Example: Mixed Indicate and Notify subscriptions**
```lua
-- Subscribe to multiple characteristics with different modes in one call
blim.subscribe{
    services = {
        {service="ff30", chars={"ff31"}, indicate=true},  -- Control: Indicate
        {service="ff30", chars={"ff32"}}                   -- Status: Notify (default)
    },
    Mode = "EveryUpdate",
    Callback = function(record)
        -- Handle control indicate (ff31)
        local control_data = record.Values["ff31"]
        if control_data then
            local opcode = string.byte(control_data, 1)
            local result = string.byte(control_data, 2)
            print(string.format("Command 0x%02X result: 0x%02X", opcode, result))
        end

        -- Handle status notify (ff32)
        local status_data = record.Values["ff32"]
        if status_data then
            print("Status:", string.byte(status_data, 1))
        end
    end
}
```

**Example: Subscription cancellation**
```lua
-- Store the cancel function returned by subscribe
local cancel = blim.subscribe{
    services = {
        {service="180d", chars={"2a37"}}
    },
    Mode = "EveryUpdate",
    Callback = function(record)
        print("Heart rate update received")
    end
}

-- Later, stop the subscription
blim.sleep(5000)  -- Run for 5 seconds
cancel()          -- Stop subscription and release resources
print("Subscription stopped")
```

**Example: Self-cancellation from callback**
```lua
-- Cancel subscription after receiving N samples
local sample_count = 0
local max_samples = 10

blim.subscribe{
    services = {
        {service="180d", chars={"2a37"}}
    },
    Mode = "EveryUpdate",
    Callback = function(record, cancel)
        sample_count = sample_count + 1
        print("Sample", sample_count)

        if sample_count >= max_samples then
            cancel()  -- Stop from within callback
            print("Collected", max_samples, "samples, stopping")
        end
    end
}
```

**Example: Conditional self-cancellation**
```lua
-- Stop subscription when a specific value is received
blim.subscribe{
    services = {
        {service="180f", chars={"2a19"}}  -- Battery
    },
    Mode = "EveryUpdate",
    Callback = function(record, cancel)
        local battery = string.byte(record.Values["2a19"], 1)
        print("Battery:", battery, "%")

        if battery < 20 then
            print("Low battery detected, stopping monitoring")
            cancel()
        end
    end
}
```

## Working with Binary Data

Characteristic values are Lua strings containing raw bytes.

**Convert bytes to hex:**
```lua
local function to_hex(bytes)
    local hex = {}
    for i = 1, #bytes do
        hex[i] = string.format("%02X", string.byte(bytes, i))
    end
    return table.concat(hex)
end

-- Usage in callback
Callback = function(record)
    for uuid, data in pairs(record.Values) do
        print(uuid, "=", to_hex(data))
    end
end
```

**Extract multi-byte values:**
```lua
-- Little-endian uint16
local function read_uint16_le(bytes, offset)
    offset = offset or 1
    local b1 = string.byte(bytes, offset)
    local b2 = string.byte(bytes, offset + 1)
    return b1 + b2 * 256
end

-- Parse heart rate measurement
Callback = function(record)
    local hr_data = record.Values["2a37"]
    if hr_data then
        local flags = string.byte(hr_data, 1)
        local is_16bit = (flags & 0x01) ~= 0

        local bpm
        if is_16bit then
            bpm = read_uint16_le(hr_data, 2)
        else
            bpm = string.byte(hr_data, 2)
        end

        print("Heart Rate:", bpm, "bpm")
    end
end
```

## Output Capture and Error Handling

All Lua script output is captured and logged:

- **`print()` output** → Captured to stdout channel
- **`io.write()` output** → Captured to stdout channel
- **`io.stderr:write()` output** → Captured to stderr channel
- **Lua errors (including `error()` function)** → Sent to stderr channel and logged

**Note:** Output is captured in real-time and can be accessed programmatically via the output channel.

**Error Capture:** When Lua's `error()` function is called or any runtime error occurs, the error message is automatically captured to the stderr channel.

```lua
-- Standard output (captured to stdout)
print("Status: OK")
io.write("Progress: 50%\n")

-- Error output (captured to stderr)
io.stderr:write("Warning: Low battery\n")

-- Lua errors (captured to stderr)
blim.subscribe{
    services = {},  -- ERROR: empty services array
    Callback = function(record) end
}

-- This will also raise an error
blim.subscribe("invalid")  -- ERROR: expects table
```

## Complete Example: Heart Rate Monitor

```lua
local json = require("json")

print("Starting Heart Rate Monitor")
print("Device:", blim.device.name)

local sample_count = 0

blim.subscribe{
    services = {
        {service="180d", chars={"2a37"}}  -- Heart Rate Measurement
    },
    Mode = "EveryUpdate",
    MaxRate = 0,  -- No rate limiting
    Callback = function(record)
        sample_count = sample_count + 1

        local hr_data = record.Values["2a37"]
        if hr_data then
            local flags = string.byte(hr_data, 1)
            local is_16bit = (flags & 0x01) ~= 0

            local bpm
            if is_16bit then
                local b1 = string.byte(hr_data, 2)
                local b2 = string.byte(hr_data, 3)
                bpm = b1 + b2 * 256
            else
                bpm = string.byte(hr_data, 2)
            end

            print(json.encode{
                timestamp = record.TsUs,
                sample = sample_count,
                heart_rate = bpm
            })
        end
    end
}
```

### `blim.characteristic(service_uuid, char_uuid)` → `handle`
Returns a characteristic handle with metadata and methods.

**Handle fields:**
- `uuid` (string) - Characteristic UUID
- `service` (string) - Parent service UUID
- `name` (string, optional) - Human-readable characteristic name (e.g., "Heart Rate Measurement" for UUID "2a37"). Only present for standard BLE characteristics.
- `has_parser` (boolean) - True if characteristic has registered parser
- `requires_authentication` (boolean) - True if characteristic requires pairing/authentication to access
- `properties` (table) - Boolean flags for each property:
  - `read` (boolean) - Supports read operations
  - `write` (boolean) - Supports write operations
  - `notify` (boolean) - Supports notifications
  - `indicate` (boolean) - Supports indications
- `descriptors` (array) - Array of descriptor objects (1-indexed), each containing:
  - `uuid` (string) - Descriptor UUID
  - `name` (string, optional) - Human-readable descriptor name. Only present for standard BLE descriptors.

**Handle methods:**
- `read()` → `data, error` - Reads characteristic value from device
- `write(data, [with_response])` → `success, error` - Writes data to characteristic
- `parse` (function or nil) - Parses raw value to human-readable format. `nil` when parser is not available (`has_parser` returns false).

**Example: Read characteristic value**
```lua
local char = blim.characteristic("180a", "2a29")  -- Device Info: Manufacturer Name

if char.properties.read then
    local value, err = char.read()
    if value then
        print("Manufacturer:", value)
    else
        print("Read failed:", err)
    end
end
```

**Example: Write characteristic value**
```lua
local char = blim.characteristic("1234", "ABCD")  -- Custom Service/Characteristic

-- Write with response (default, waits for acknowledgment)
local success, err = char.write("\x01\x02\x03")
if success then
    print("Write successful")
else
    print("Write failed:", err)
end

-- Write without response (faster, no acknowledgment)
local success, err = char.write("\x01\x02\x03", false)
if success then
    print("Write sent")
end
```

**Example: Parse characteristic value**
```lua
-- Appearance characteristic (0x2A01) has a registered parser
local char = blim.characteristic("1800", "2a01")  -- GAP: Appearance

if char.has_parser then
    local value, err = char.read()
    if value then
        -- Raw bytes (little-endian uint16)
        local byte1 = string.byte(value, 1)
        local byte2 = string.byte(value, 2)
        local appearance_value = byte1 + (byte2 * 256)
        print("Appearance (hex):", string.format("0x%04X", appearance_value))

        -- Parse to human-readable string
        local parsed = char.parse(value)
        if parsed then
            print("Appearance (parsed):", parsed)  -- e.g., "Phone", "Computer"
        else
            print("Unknown appearance value")
        end
    end
end
```

**Example: Inspect and read all readable characteristics**
```lua
local services = blim.list()

for service_uuid, service_info in pairs(services) do
    print("Service:", service_uuid)

    for _, char_uuid in ipairs(service_info.characteristics) do
        local char = blim.characteristic(service_uuid, char_uuid)

        io.write("  Char: " .. char_uuid .. " [")
        if char.properties.read then io.write("R") end
        if char.properties.write then io.write("W") end
        if char.properties.notify then io.write("N") end
        if char.properties.indicate then io.write("I") end
        io.write("]\n")

        -- Read value if readable
        if char.properties.read then
            local value, err = char.read()
            if value then
                print("    Value:", value)
            end
        end
    end
end
```

**Output:**
```
Service: 180d
  Char: 2a37 [RN]
  Char: 2a38 [R]
    Value: Body Sensor Location
Service: 180f
  Char: 2a19 [RN]
    Value: 85
```

### `blim.sleep(milliseconds)`
Pauses execution for the specified duration.

**Parameters:**
- `milliseconds` (number) - Duration to sleep (must be non-negative)

**Returns:** Nothing

**Example: Simple delay**
```lua
print("Starting...")
blim.sleep(1000)  -- Sleep for 1 second
print("Done!")
```

**Example: Rate-limited data collection**
```lua
-- Read battery level every 5 seconds
for i = 1, 10 do
    local char = blim.characteristic("180f", "2a19")
    local value, err = char.read()
    if value then
        local battery = string.byte(value, 1)
        print("Battery level:", battery, "%")
    end

    if i < 10 then
        blim.sleep(5000)  -- Wait 5 seconds before next read
    end
end
```

**Example: Polling with timeout**
```lua
-- Poll for data with timeout
local max_attempts = 10
local attempt = 0

while attempt < max_attempts do
    local data, err = blim.bridge.pty_read()
    if data and data ~= "" then
        print("Received:", data)
        break
    end

    attempt = attempt + 1
    blim.sleep(100)  -- Poll every 100ms
end

if attempt >= max_attempts then
    print("Timeout waiting for data")
end
```

### `blim.write_receive(opts)` → `data, err`
Writes to a characteristic and waits for a notification/indication response (synchronous, blocking).

This is the primary pattern for request-response BLE protocols where you write a command and wait for the device to respond via indication or notification on the same (or different) characteristic.

**Parameters (opts table):**
- `service` (string, required) - Service UUID
- `write_char` (string, required) - Characteristic UUID to write to
- `data` (string, required) - Bytes to write
- `match` (function, required) - Matcher function `function(data) -> number`:
  - Return `1` = success, stop waiting
  - Return `-1` = operation failed, stop waiting
  - Return `0` = not our response, keep waiting
- `notify_char` (string, optional) - Characteristic to receive from (default: `write_char`)
- `write_response` (boolean, optional) - Write with response (default: `true`)
- `indicate` (boolean, optional) - Use indicate vs notify (default: `false`)
- `timeout` (number, optional) - Timeout in milliseconds (default: `5000`)

**Returns:**
- `data, nil` - Success (matcher returned `1`)
- `data, "failed"` - Operation failed (matcher returned `-1`)
- `nil, "timeout"` - No matching response within timeout
- `nil, "write: <msg>"` - Write to characteristic failed
- `nil, "subscribe: <msg>"` - Subscription setup failed
- `nil, "characteristic not found: <uuid>"` - Characteristic not available

**Example: Command with result code**
```lua
-- Send command and wait for response with result byte
local channel = 0x00  -- Front channel
local opcode = 0x01   -- CENTER command

local data, err = blim.write_receive{
    service = "ff40",
    write_char = "ff42",
    data = string.char(channel, opcode),
    indicate = true,
    timeout = 2000,
    match = function(d)
        if #d < 3 then return 0 end  -- Incomplete, keep waiting
        if d:byte(1) ~= channel or d:byte(2) ~= opcode then return 0 end
        -- Check result byte: 0x00 = OK, anything else = error
        return d:byte(3) == 0x00 and 1 or -1
    end,
}

if err then
    print("Command failed:", err)
elseif data then
    local result = data:byte(3)
    print("Command result:", result)
end
```

**Example: Different write and notify characteristics**
```lua
-- Write to control char, receive response on status char
local data, err = blim.write_receive{
    service = "ff30",
    write_char = "ff31",      -- Control characteristic
    notify_char = "ff32",     -- Status characteristic (different)
    data = "\x01\x00",
    indicate = false,         -- Use notify (not indicate)
    timeout = 3000,
    match = function(d)
        if #d < 1 then return 0 end
        return d:byte(1) == 0x00 and 1 or -1
    end,
}
```

### `blim.write_receive_async(opts)` → `err`
Writes to a characteristic and subscribes for response (asynchronous, non-blocking).

Returns immediately after write. The callback receives each notification/indication until cancelled. **User MUST call `cancel()` to cleanup the subscription.**

Use this for long-running operations where you don't want to block the main script (e.g., animated sequences, continuous tests).

**Parameters (opts table):**
- `service` (string, required) - Service UUID
- `write_char` (string, required) - Characteristic UUID to write to
- `data` (string, required) - Bytes to write
- `callback` (function, required) - Callback `function(cancel, data)`:
  - `cancel` - Function to call when done (MUST be called to cleanup)
  - `data` - Received notification/indication bytes
- `notify_char` (string, optional) - Characteristic to receive from (default: `write_char`)
- `write_response` (boolean, optional) - Write with response (default: `true`)
- `indicate` (boolean, optional) - Use indicate vs notify (default: `false`)

**Returns:**
- `nil` - Success, subscription active
- `"subscribe: <msg>"` - Subscription failed
- `"write: <msg>"` - Write failed
- `"characteristic not found: <uuid>"` - Characteristic unavailable

**Example: Long-running command with callback**
```lua
-- Start a sweep animation (takes several seconds)
local channel = 0xFF  -- All channels
local opcode = 0x04   -- SWEEP command

local err = blim.write_receive_async{
    service = "ff40",
    write_char = "ff42",
    data = string.char(channel, opcode),
    indicate = true,
    callback = function(cancel, data)
        if #data < 3 then return end  -- Not our response, keep waiting
        if data:byte(1) ~= channel or data:byte(2) ~= opcode then return end

        -- Got our response
        local result = data:byte(3)
        print("Sweep complete, result:", result)
        cancel()  -- IMPORTANT: cleanup subscription
    end,
}

if err then
    print("Failed to start sweep:", err)
end

-- Script continues immediately while sweep runs in background
print("Sweep started, doing other work...")
```

**Example: Continuous monitoring until condition**
```lua
-- Monitor status updates until error detected
local sample_count = 0

local err = blim.write_receive_async{
    service = "ff40",
    write_char = "ff47",
    data = "\x01",  -- Start monitoring command
    callback = function(cancel, data)
        sample_count = sample_count + 1
        local status = data:byte(1)
        print("Status update #" .. sample_count .. ":", status)

        -- Stop on error flag
        if bit.band(status, 0x08) ~= 0 then
            print("Error detected, stopping")
            cancel()
        end

        -- Or stop after max samples
        if sample_count >= 100 then
            print("Max samples reached")
            cancel()
        end
    end,
}
```

**When to use sync vs async:**

| Pattern | Use Case |
|---------|----------|
| `write_receive` (sync) | Short commands with immediate response (< 2s) |
| `write_receive_async` (async) | Long operations, animations, continuous monitoring |

---

### `blim.pcall(f, ...)` → `ok, ...`

Protected call — the sandbox substitute for Lua's standard `pcall`, which is
removed (its longjmp-based unwinding cannot cross Go call frames safely).

**Parameters:**
- `f` (function) - Function to call
- `...` - Arguments passed to `f`

**Returns:**
- On success: `true` followed by all of `f`'s return values
- On failure: `false` followed by the error message

**Limitation:** `blim.pcall` catches errors raised by Lua code and C built-ins
(`error()`, `ffi.cdef`, ...). Errors raised by **Go-backed API functions**
(`require`, `blim.subscribe`, `blim.characteristic`, ...) are Go-side panics and
are **not** catchable — they abort the script by design. Handle those via the
`nil, err` return values those functions already provide, not `blim.pcall`.

**Example:**
```lua
-- Catch a Lua-level error
local ok, err = blim.pcall(function() error("boom") end)
if not ok then
    print("caught:", err)  -- caught: ...: boom
end

-- Forward return values on success
local ok, sum = blim.pcall(function(a, b) return a + b end, 2, 3)
assert(ok and sum == 5)
```

---

### `blim.term`

Raw terminal mode and single-keypress input for interactive bridge scripts.
Requires LuaJIT (uses FFI); every method raises on unsupported platforms.

**Functions:**
- `blim.term.enable_raw()` → `true` | `nil, err` - Enable raw mode (no echo, no
  line buffering, non-blocking reads). Idempotent. Returns `nil, "tcgetattr failed"`
  when stdin is not a TTY.
- `blim.term.disable_raw()` - Restore the original terminal settings. Idempotent.
- `blim.term.read_char([wait_ms])` → `char` | `nil` | `nil, msg, code` - Read a
  single character, following `io.read` semantics (see below).
- `blim.term.EOF` - Sentinel `code` value returned by `read_char` when stdin is closed.

**`read_char` return values:**
- `char` - a key was read
- `nil` - no key available yet (non-blocking, or the wait window elapsed)
- `nil, msg, code` - terminal condition: `code == blim.term.EOF` means stdin was
  closed/hung up (interactive loops MUST exit); any other `code` is the read `errno`

With no `wait_ms` the read is non-blocking. With `wait_ms` it waits up to that
many milliseconds, yielding via `blim.sleep` so the engine mutex is released and
BLE callbacks keep flowing while the script waits.

**Example: interactive keypress loop**
```lua
assert(blim.term.enable_raw())

while true do
    local key, err, code = blim.term.read_char(200)
    if key == "q" then
        break
    elseif code == blim.term.EOF then
        break  -- stdin closed (shutdown / input exhausted)
    end
    -- bare nil: no key this window; BLE callbacks ran during the wait
end

blim.term.disable_raw()
```

See [examples/vehicle-control-bridge.lua](../../examples/vehicle-control-bridge.lua)
for a full interactive control panel built on `blim.term`.

---

## TODO: Upcoming API Extensions

The following functions are planned for future implementation to provide complete BLE interaction capabilities.

### Simple Function-based API (One-off Operations)

For simple scripts and one-time operations:

#### `ble.read(service_uuid, char_uuid)` → `data`
Performs a one-time read of a characteristic value.

```lua
-- Read battery level once
local battery_data = ble.read("180f", "2a19")
local battery_percent = string.byte(battery_data)
print("Battery:", battery_percent, "%")
```

#### `ble.write(service_uuid, char_uuid, data)`
Writes data to a characteristic (write without response).

```lua
-- Write configuration value
ble.write("1234", "5678", "\x01\x00")
```

#### `ble.write_with_response(service_uuid, char_uuid, data)`
Writes data to a characteristic and waits for a response.

```lua
-- Write with acknowledgment
ble.write_with_response("1234", "5678", "\x01\x00")
```

**Note:** The handle-based `read()` and `write()` methods are already implemented. Subscription cancellation is available via the cancel function returned by `blim.subscribe()`. The function-based API will be added in future updates.

---

### API Design Rationale

**Function-based** (`ble.read()`, `ble.write()`) is ideal for:
- Simple scripts with occasional operations
- One-off reads/writes
- Maximum code clarity

**Handle-based** (`blim.characteristic()` → handle) is ideal for:
- Tight loops with repeated operations
- Bulk data transfers
- When metadata access is needed
- 6× performance improvement for 1000+ operations

Both approaches will coexist - use whichever fits your use case.

---

## Current Status

**✅ Available features:**
- ✅ **Read operations** - `handle.read()` reads characteristic values on demand
- ✅ **Write operations** - `handle.write(data, [with_response])` writes to characteristics with or without acknowledgment
- ✅ **Write-receive patterns** - `blim.write_receive()` (sync) and `blim.write_receive_async()` (async) for request-response BLE protocols
- ✅ **Value parsing** - `handle.parse(value)` parses known characteristic types (e.g., Appearance)
- ✅ **Characteristic inspection** - `blim.characteristic()` returns metadata (UUID, service, properties, descriptors, has_parser)
- ✅ **Service listing** - `blim.list()` enumerates all GATT services and characteristics
- ✅ **Device information** - `blim.device` provides device metadata and advertisement data
- ✅ **Subscriptions** - `blim.subscribe()` supports notifications/indications with multiple streaming modes
- ✅ **Subscription cancellation** - `blim.subscribe()` returns cancel function; callback receives cancel for self-cancellation
- ✅ **PTY bridge** - `blim.bridge.pty_write()`, `pty_read()`, and `pty_on_data()` for async PTY communication
- ✅ **Protected calls** - `blim.pcall()` catches Lua/C errors (Go-backed errors abort by design)
- ✅ **Interactive terminal** - `blim.term` raw mode and single-keypress input for interactive bridge scripts

**⚠️ Planned features:**
- ⚠️ **Function-based API** - Simplified `ble.read()`, `ble.write()` not yet available
- ⚠️ **More parsers** - Currently only Appearance characteristic has a registered parser

These will be addressed by the upcoming API extensions described above.

---

# Developer Documentation

## Panic Recovery for Go Functions Exposed to Lua

### Critical Requirement

**ALL Go functions exposed to Lua MUST be wrapped with panic recovery** to prevent crashes and ensure proper error handling.

### Implementation

#### For BLE API Functions

Use `BLEAPI2.SafePushGoFunction()` helper:

```go
// Correct - wrapped with SafePushGoFunction
api.SafePushGoFunction(L, "read", func(L *lua.State) int {
    // Your implementation
    // Can safely use L.RaiseError() for expected errors
    L.RaiseError("invalid argument")
    return 0
})
L.SetTable(-3)
```

#### For Engine-Level Functions

Use `LuaEngine.SafeWrapGoFunction()`:

```go
// Correct - wrapped with SafeWrapGoFunction
L.PushGoFunction(e.SafeWrapGoFunction("print()", func(L *lua.State) int {
    // Your implementation
    return 0
}))
L.SetGlobal("print")
```

### Panic Recovery Behavior

The wrapper handles panics as follows:

1. **Expected Lua Errors** (`*lua.LuaError` from `L.RaiseError()`)
   - Re-panicked as-is to propagate to Lua runtime
   - Error message preserved exactly

2. **Unexpected Panics** (strings, structs, nil pointer, etc.)
   - Caught and logged with full stack trace for debugging
   - Converted to clean Lua error: `"function_name() panicked in Go"`
   - Prevents process crash

### Currently Wrapped Functions

All Go functions exposed to Lua are wrapped:

**BLE API (`api.go`):**
- ✅ `blim.subscribe()`
- ✅ `blim.list()`
- ✅ `blim.characteristic()`
- ✅ `char.read()` (characteristic handle method)
- ✅ `char.write(data, [with_response])` (characteristic handle method)
- ✅ `char.parse(value)` (characteristic handle method)
- ✅ `blim.bridge.pty_write()` (bridge PTY write)
- ✅ `blim.bridge.pty_read()` (bridge PTY read)
- ✅ `blim.bridge.pty_on_data(callback)` (bridge PTY async callback)
- ✅ `blim.sleep()` (utility function for delays)
- ✅ `blim.pcall()` (protected call for Lua/C errors)
- ✅ `blim.term.enable_raw()` / `disable_raw()` / `read_char()` (interactive terminal input)

**Engine Functions (`lua_engine.go`):**
- ✅ `print()` (overridden for output capture)
- ✅ `io.write()` (overridden for stdout capture)
- ✅ `io.stderr:write()` (overridden for stderr capture)

**Blocked Functions:**
- Stub functions that call `L.RaiseError()` are simple and less critical

### Adding New Lua-Exposed Functions

When adding a new Go function for Lua:

```go
// ❌ WRONG - Direct PushGoFunction (crashes on panic)
L.PushGoFunction(func(L *lua.State) int {
    // dangerous - any panic crashes the process
    return 0
})

// ✅ CORRECT - Wrapped with SafePushGoFunction
api.SafePushGoFunction(L, "new_function", func(L *lua.State) int {
    // safe - panics are caught and converted to Lua errors
    return 0
})
```

### Testing

See `lua_engine_test.go::TestSafeWrapGoFunction` for comprehensive panic recovery tests covering:
- Expected Lua errors (from `L.RaiseError`)
- Unexpected panics (strings, structs, etc.)
- Normal execution
- Multiple wrapped functions