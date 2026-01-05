--- MotionCal Bridge
-- Bidirectional bridge: BLE IMU sensor ↔ MotionCal desktop app via PTY.
-- Receives 50Hz IMU data (accel/gyro/mag), forwards to MotionCal,
-- parses calibration data responses.
-- @module motioncal-bridge
-- @see https://learn.adafruit.com/adafruit-sensorlab-magnetometer-calibration/magnetic-calibration-with-motioncal/

local ffi = require("ffi")
local bit = require("bit")
local FfiBuffer = require("ffi_buffer")

ffi.cdef[[
typedef struct {
    float accel_zerog[3];
    float gyro_zerorate[3];
    float mag_hardiron[3];
    float mag_field;
    float mag_softiron[9];
} ImuCal;
]]

local rxbuf = FfiBuffer.new(1024)
local cal = ffi.new("ImuCal")
local cal_buf = ffi.new("char[512]")  -- Pre-allocated buffer for printCalibration()
local HEADER1, HEADER2 = 117, 84
local PACKET_SIZE = 68

--- Update CRC16 with one byte (MODBUS polynomial 0xA001).
-- @param crc Current CRC value
-- @param b Byte to process (0-255)
-- @return Updated 16-bit CRC value
local function crc16_update(crc, b)
    crc = bit.bxor(crc, b)
    for _ = 1, 8 do
        if bit.band(crc, 1) ~= 0 then
            crc = bit.bxor(bit.rshift(crc, 1), 0xA001)
        else
            crc = bit.rshift(crc, 1)
        end
    end
    return bit.band(crc, 0xFFFF)
end

--- Verify CRC16 checksum for packet.
-- @param buf Buffer containing packet data
-- @param offset Starting offset of packet in buffer
-- @return true if CRC valid (computed CRC == 0)
local function verify_crc(buf, offset)
    local crc = 0xFFFF
    for i = 0, PACKET_SIZE - 1 do
        crc = crc16_update(crc, buf:byte(offset + i))
    end
    return crc == 0
end

--- Extract 16 floats from buffer via FFI memory copy.
-- @param buf Buffer containing float data
-- @param offset Starting offset in buffer
-- @return float[16] cdata array (64 bytes, little-endian IEEE 754)
local function extract_floats(buf, offset)
    local floats = ffi.new("float[16]")
    ffi.copy(floats, buf.data + buf.offset + offset, 64)
    return floats
end

--- Print magnetometer calibration parameters (optimized FFI version).
-- Displays: mag hard-iron (µT), mag field (µT), mag soft-iron matrix (3×3).
-- Note: Accel/gyro calibration from MotionCal is always zero and not saved to the device.
-- Performance: Single snprintf() + single print() call, zero GC pressure, maximum throughput.
local function printCalibration()
    -- Single FFI snprintf call - absolute maximum performance
    local len = ffi.C.snprintf(cal_buf, 512,
        "\n✓ New magnetometer calibration from MotionCal:\n" ..
        "  Hard-iron offset: %.4f %.4f %.4f µT\n" ..
        "  Field strength:   %.4f µT\n" ..
        "  Soft-iron matrix:\n" ..
        "    %.4f %.4f %.4f\n" ..
        "    %.4f %.4f %.4f\n" ..
        "    %.4f %.4f %.4f\n",
        cal.mag_hardiron[0], cal.mag_hardiron[1], cal.mag_hardiron[2],
        cal.mag_field,
        cal.mag_softiron[0], cal.mag_softiron[1], cal.mag_softiron[2],
        cal.mag_softiron[3], cal.mag_softiron[4], cal.mag_softiron[5],
        cal.mag_softiron[6], cal.mag_softiron[7], cal.mag_softiron[8])

    print(ffi.string(cal_buf, len))
end

--- Save magnetometer calibration to the BLE device.
-- Writes calibration data to service 0xff20, characteristic 0xff21 in JSON format.
-- Format matches device settings schema with hard-iron offsets, field strength, and soft-iron matrix.
-- After writing, reads back the settings to verify they were saved correctly.
local function saveCalibration()
    -- Build JSON string with current magnetometer calibration
    local json = string.format([[{
  "imu": {
    "mag": {
      "hardiron": [%.6f, %.6f, %.6f],
      "field": %.6f,
      "softiron": [
        [%.6f, %.6f, %.6f],
        [%.6f, %.6f, %.6f],
        [%.6f, %.6f, %.6f]
      ]
    }
  },
  "metadata": {
    "version": 1
  }
}]],
        cal.mag_hardiron[0], cal.mag_hardiron[1], cal.mag_hardiron[2],
        cal.mag_field,
        cal.mag_softiron[0], cal.mag_softiron[1], cal.mag_softiron[2],
        cal.mag_softiron[3], cal.mag_softiron[4], cal.mag_softiron[5],
        cal.mag_softiron[6], cal.mag_softiron[7], cal.mag_softiron[8])

    -- Get characteristic handle for device settings ()
    local settings_char = blim.characteristic("ff20", "ff21")

    -- Write JSON to device settings characteristic with response
    local _result, err = settings_char.write(json, true)
    if err then
        print("ERROR: Failed to save calibration: " .. err)
        return false
    end

    -- Read back the settings to see what the device actually saved (device merges with existing settings)
    local settings_data, read_err = settings_char.read()
    if read_err then
        print("WARNING: Failed to read back settings: " .. read_err)
        return false
    end

    -- Print the actual merged settings from the device
    print("\n✓ Calibration saved. Device settings:")
    print(settings_data)
    return true
end

local function onNewCalibration()
    printCalibration()
    saveCalibration()
end

--- Process calibration data from MotionCal via PTY.
-- Streaming protocol: header sync, CRC validation, auto-resync.
-- Packet: [117, 84, 64B data, 2B CRC16] = 68 bytes.
-- @param ptr Raw binary data chunk from PTY
function receiveCalibrationChunk(ptr)
    if not ptr or #ptr == 0 then return end

    rxbuf:append(ptr)

    while rxbuf.length >= PACKET_SIZE do
        -- Check for header at current position
        if rxbuf:byte(0) ~= HEADER1 or rxbuf:byte(1) ~= HEADER2 then
            -- Search for header
            local found = nil
            for i = 1, rxbuf.length - 2 do
                if rxbuf:byte(i) == HEADER1 and rxbuf:byte(i+1) == HEADER2 then
                    found = i
                    break
                end
            end

            if not found then
                -- No header found, keep last byte in case it's 117
                rxbuf:discard(rxbuf.length - 1)
                return
            end

            rxbuf:discard(found)

            ---@diagnostic disable-next-line: unnecessary-if
            if rxbuf.length < PACKET_SIZE then
                return
            end
        end

        -- Verify CRC
        if not verify_crc(rxbuf, 0) then
            -- CRC failed - search for next header within this packet
            local found = nil
            for i = 2, PACKET_SIZE - 2 do
                if rxbuf:byte(i) == HEADER1 and rxbuf:byte(i+1) == HEADER2 then
                    found = i
                    break
                end
            end

            ---@diagnostic disable-next-line: unnecessary-if
            if found then
                rxbuf:discard(found)
            else
                -- Check if last byte is 117 (potential split header)
                if rxbuf:byte(PACKET_SIZE - 1) == HEADER1 then
                    rxbuf:discard(PACKET_SIZE - 1)
                else
                    rxbuf:discard(PACKET_SIZE)
                end
            end

            goto next
        end

        -- Extract floats
        local f = extract_floats(rxbuf, 2)

        -- Map to calibration struct (matches Arduino exactly)
        cal.accel_zerog[0] = f[0]
        cal.accel_zerog[1] = f[1]
        cal.accel_zerog[2] = f[2]

        cal.gyro_zerorate[0] = f[3]
        cal.gyro_zerorate[1] = f[4]
        cal.gyro_zerorate[2] = f[5]

        cal.mag_hardiron[0] = f[6]
        cal.mag_hardiron[1] = f[7]
        cal.mag_hardiron[2] = f[8]

        cal.mag_field = f[9]

        -- Soft-iron matrix: corrects magnetic distortion from nearby ferromagnetic materials.
        -- The correction is a symmetric 3x3 matrix because magnetic permeability is symmetric
        -- (physics: the material's response to magnetic fields is the same in both directions).
        -- Arduino protocol exploits this symmetry to send only 6 values instead of 9:
        --   [0 1 2]     [f10 f13 f14]
        --   [1 4 5]  =  [f13 f11 f15]  (symmetric: [1]=[3], [2]=[6], [5]=[7])
        --   [2 5 8]     [f14 f15 f12]
        cal.mag_softiron[0] = f[10]
        cal.mag_softiron[1] = f[13]
        cal.mag_softiron[2] = f[14]
        cal.mag_softiron[3] = f[13] -- Mirror of [1]
        cal.mag_softiron[4] = f[11]
        cal.mag_softiron[5] = f[15]
        cal.mag_softiron[6] = f[14] -- Mirror of [2]
        cal.mag_softiron[7] = f[15] -- Mirror of [5]
        cal.mag_softiron[8] = f[12]

        onNewCalibration()
        rxbuf:discard(PACKET_SIZE)

        ::next::
    end
end

blim.bridge.pty_on_data(receiveCalibrationChunk)

----------------------------------------------------------------------
-- TTY MotionCal raw data writer
----------------------------------------------------------------------

-- Define C struct matching the binary layout
ffi.cdef[[
  typedef struct {
      float accel_x, accel_y, accel_z;
      float gyro_x,  gyro_y,  gyro_z;
      float mag_x,   mag_y,   mag_z;
  } imu_data_t;

  int snprintf(char *str, size_t size, const char *format, ...);
]]

-- Allocate once, then reuse
local imu_ptr = ffi.new("imu_data_t[1]")

-- Pre-calculate conversion constants (performance: avoid recomputing at 50Hz)
-- Values match Adafruit_SensorLab Arduino reference implementation
local ACCEL_SCALE = 8192/9.8  -- ≈836.735 LSB per m/s²
local GYRO_SCALE = 57.2957795130823 * 16  -- ≈916.732 (rad/s → deg/s * 16, full precision)
local MAG_SCALE = 10  -- µT → MotionCal format

-- Pre-allocate output buffer (zero-allocation formatting at 50Hz)
local output_buf = ffi.new("char[128]")
local format_str = "Raw:%d,%d,%d,%d,%d,%d,%d,%d,%d\r\n"

-- Pre-allocate result table (IMPORTANT: reused on every call)
local result = {
    accel = { x = 0, y = 0, z = 0 },
    gyro  = { x = 0, y = 0, z = 0 },
    mag   = { x = 0, y = 0, z = 0 }
}

--- Parse IMU binary data (zero-allocation).
-- Format: 9×float (36 bytes, little-endian IEEE 754).
-- WARNING: Returns the same table every call (reused for performance).
-- Copy values if storage is needed.
-- @param data Binary data (exactly 36 bytes)
-- @return table with accel/gyro/mag fields (reused, ~0.1-0.5µs)
function parse_imu_data(data)
    --  little-endian byte order
    --  4-byte IEEE 754 float (repeated 9 times)

    -- Validate data length BEFORE FFI operations
    if #data ~= 36 then
        error(string.format("Invalid IMU data length: expected 36 bytes, got %d", #data))
        return nil
    end

    -- Copy binary data directly into C struct (fast!)
    ffi.copy(imu_ptr, data, 36)
    local imu = imu_ptr[0]

    -- Update the reused table in-place
    result.accel.x = imu.accel_x
    result.accel.y = imu.accel_y
    result.accel.z = imu.accel_z
    result.gyro.x = imu.gyro_x
    result.gyro.y = imu.gyro_y
    result.gyro.z = imu.gyro_z
    result.mag.x = imu.mag_x
    result.mag.y = imu.mag_y
    result.mag.z = imu.mag_z

    return result
end

--- Subscribe
blim.subscribe {
    services = {
        {
            service = "FF10",
            chars = { "FF11" }
        }
    },

    Mode = "EveryUpdate",
    --MaxRate = 0,
    Callback = function(record)
        local data = record.Values["ff11"]

        -- Fast path: skip parse_imu_data() overhead, use FFI struct directly
        if #data ~= 36 then
            error(string.format("Invalid IMU data length: expected 36 bytes, got %d", #data))
            return
        end

        ffi.copy(imu_ptr, data, 36)
        local imu = imu_ptr[0]

        -- Print for debug: Accel (m/s²), Gyro (rad/s), Mag (µT)
        --print(string.format("Accel: %.2f,%.2f,%.2f | Gyro: %.2f,%.2f,%.2f | Mag: %.2f,%.2f,%.2f",
        --    imu.accel_x, imu.accel_y, imu.accel_z,
        --    imu.gyro_x, imu.gyro_y, imu.gyro_z,
        --    imu.mag_x, imu.mag_y, imu.mag_z))

        -- MotionCal expects:
        --   Accel: ±2g range as 16-bit signed integers (±8192 = ±2g)
        --   Gyro: degrees/s * 16 (fixed-point with four fractional bits)
        --   Mag: microtesla * 10 (single decimal precision)
        --  See https://github.com/PaulStoffregen/MotionCal/issues/12

        -- Zero-allocation formatting: write directly to pre-allocated C buffer
        -- CRITICAL: Cast to int for C snprintf (Lua numbers are doubles, %d expects int)
        local len = ffi.C.snprintf(output_buf, 128, format_str,
          -- Convert m/s² to ±2g raw format (8192 LSB/g, 9.8 m/s² = 1g)
          ffi.cast("int", imu.accel_x * ACCEL_SCALE),
          ffi.cast("int", imu.accel_y * ACCEL_SCALE),
          ffi.cast("int", imu.accel_z * ACCEL_SCALE),

          -- Convert rad/s to deg/s (* 180/π ≈ 57.29577793) then to raw (* 16 LSB/deg/s)
          ffi.cast("int", imu.gyro_x * GYRO_SCALE),
          ffi.cast("int", imu.gyro_y * GYRO_SCALE),
          ffi.cast("int", imu.gyro_z * GYRO_SCALE),

          -- Convert µT to MotionCal format (µT * 10)
          ffi.cast("int", imu.mag_x * MAG_SCALE),
          ffi.cast("int", imu.mag_y * MAG_SCALE),
          ffi.cast("int", imu.mag_z * MAG_SCALE))

        blim.bridge.pty_write(ffi.string(output_buf, len))
    end
}

-- Print header information after successful subscription
print("\n===  MotionCal Bridge  ===\n")
print(string.format("Device: %s", blim.device.address))

-- Display bridge information if available
if blim.bridge.tty_name() and blim.bridge.tty_name() ~= "" then
    print(string.format("TTY: %s", blim.bridge.tty_name()))
    if blim.bridge.tty_symlink() and blim.bridge.tty_symlink() ~= "" then
        print(string.format("Symlink: %s", blim.bridge.tty_symlink()))
    end
    print("")
end