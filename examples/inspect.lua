-- BLE Inspect: Device Inspection
-- This script replicates the output format of the Go outputInspectText function

-- Extract Device Information Service data
local function extract_dis_info(services)
    for _, service in ipairs(services) do
        -- Check if this is the Device Information Service (UUID 180A)
        if string.upper(service.uuid) == "180A" then
            local dis_data = {}
            for _, char in ipairs(service.characteristics) do
                local char_name = blim.format_named(char)
                dis_data[char_name] = blim.bytes_to_hex(char.value)
            end
            return dis_data
        end
    end
    return nil
end

-- Collect all device and GATT data into a structured table
local function collect_device_data()
    local data = {}

    -- Device info
    data.device = {
        id = blim.device.id,
        address = blim.device.address,
        name = blim.device.name,
        rssi = blim.device.rssi,
        connectable = blim.device.connectable,
        tx_power = blim.device.tx_power,
        advertised_services = blim.device.advertised_services or {},
        manufacturer_data = blim.device.manufacturer_data,
        service_data = blim.device.service_data or {}
    }

    -- GATT Services
    data.services = {}
    local services = blim.list()

    -- Note: services table has both array part (for ordered iteration) and hash part (for UUID lookup)
    -- We use ipairs() to iterate in sorted order: services[1], services[2], etc.
    for _, service_uuid in ipairs(services) do
        local service_info = services[service_uuid]  -- Lookup service info by UUID
        local service_data = {
            uuid = service_uuid,
            name = service_info.name,  -- Copy optional name field
            characteristics = {}
        }

        if service_info.characteristics then
            -- Characteristics are already sorted by the BLE API
            for _, char_uuid in ipairs(service_info.characteristics) do
                local char_info = blim.characteristic(service_uuid, char_uuid) or {}

                -- Try to read the characteristic value if it's readable
                local value = nil
                local parsed_value = nil
                if char_info.properties and char_info.properties.read and char_info.read then
                    local val, err = char_info.read()
                    if err == nil then
                        value = val
                        -- Try to parse if parser is available and value is non-empty
                        if char_info.has_parser and char_info.parse and value and value ~= "" then
                            parsed_value = char_info:parse(value)  -- Use colon syntax
                        end
                    end
                    -- Silently ignore read errors in inspect (characteristic may not be readable)
                end

                table.insert(service_data.characteristics, {
                    uuid = char_uuid,
                    name = char_info.name,  -- Copy optional name field
                    properties = char_info.properties,  -- Keep dual-purpose table (array + hash)
                    value = value,
                    parsed_value = parsed_value,  -- Add parsed value
                    has_parser = char_info.has_parser,  -- Add parser availability flag
                    requires_authentication = char_info.requires_authentication,  -- Add authentication flag
                    descriptors = char_info.descriptors or {}
                })
            end
        end

        table.insert(data.services, service_data)
    end

    -- Extract Device Information Service data
    data.device_info = extract_dis_info(data.services)

    return data
end

-- Recursively print descriptor information with proper indentation
-- depth controls indentation level (0 = base level, 1 = nested, etc.)
local function print_descriptor(descriptor, indent, depth)
    local descriptor_display = blim.format_named(descriptor)
    io.write(string.format("%sdescriptor: %s, Handle: %s\n", indent, descriptor_display, descriptor.handle))

    -- Show descriptor value if available and non-empty
    if descriptor.value and descriptor.value ~= "" then
        io.write(string.format("%s  value: %s\n", indent, descriptor.value))
    end

    -- Show parsed value if available
    if descriptor.parsed_value then
        if type(descriptor.parsed_value) == "table" then
            -- Check if it's an error
            if descriptor.parsed_value.error then
                io.write(string.format("%s  parsed: ERROR: %s\n", indent, descriptor.parsed_value.error))
            -- Check if it's an aggregate format (array of descriptors)
            elseif descriptor.parsed_value[1] and descriptor.parsed_value[1].uuid then
                io.write(string.format("%s  parsed: Aggregate Format (%d descriptors)\n", indent, #descriptor.parsed_value))
                -- Print each referenced descriptor
                for i, ref_desc in ipairs(descriptor.parsed_value) do
                    local ref_display = blim.format_named(ref_desc)
                    io.write(string.format("%s    [%d] descriptor: %s, Handle: %s\n",
                        indent, i, ref_display, ref_desc.handle))

                    -- Show value if available
                    if ref_desc.value and ref_desc.value ~= "" then
                        io.write(string.format("%s        value: %s\n", indent, ref_desc.value))
                    end

                    -- Show parsed value if available
                    if ref_desc.parsed_value and type(ref_desc.parsed_value) == "table"
                       and not ref_desc.parsed_value.error then
                        io.write(string.format("%s        parsed:\n", indent))
                        for k, v in pairs(ref_desc.parsed_value) do
                            io.write(string.format("%s          %s: %s\n", indent, k, tostring(v)))
                        end
                    end
                end
            else
                -- Regular parsed table (not aggregate format, not error)
                io.write(string.format("%s  parsed:\n", indent))
                for k, v in pairs(descriptor.parsed_value) do
                    io.write(string.format("%s    %s: %s\n", indent, k, tostring(v)))
                end
            end
        else
            -- String or other simple type
            io.write(string.format("%s  parsed: %s\n", indent, tostring(descriptor.parsed_value)))
        end
    end
end

-- Format and output as human-readable text
local function output_text(data)
    -- Device info section
    io.write("Device info:\n")
    io.write(string.format("  ID: %s\n", data.device.id))
    io.write(string.format("  Address: %s\n", data.device.address))

    if data.device.name and data.device.name ~= "" then
        io.write(string.format("  Name: %s\n", data.device.name))
    end

    io.write(string.format("  RSSI: %d\n", data.device.rssi))
    io.write(string.format("  Connectable: %s\n", tostring(data.device.connectable)))

    if data.device.tx_power then
        io.write(string.format("  TxPower: %d dBm\n", data.device.tx_power))
    end

    -- Advertised Services section
    if #data.device.advertised_services > 0 then
        io.write("  Advertised Services:\n")
        for _, service_uuid in ipairs(data.device.advertised_services) do
            io.write(string.format("    - %s\n", service_uuid))
        end
    else
        io.write("  Advertised Services: none\n")
    end

    -- Manufacturer Data section
    if data.device.manufacturer_data then
        io.write(string.format("  Manufacturer Data: %s\n", data.device.manufacturer_data.value))

        -- Show parsed manufacturer data if available
        if data.device.manufacturer_data.parsed_value then
            io.write("    Parsed:\n")

            -- Show vendor info first if available
            if data.device.manufacturer_data.parsed_value.vendor then
                io.write(string.format("      Vendor ID: 0x%04X\n", data.device.manufacturer_data.parsed_value.vendor.id))
                if data.device.manufacturer_data.parsed_value.vendor.name then
                    io.write(string.format("      Vendor: %s\n", data.device.manufacturer_data.parsed_value.vendor.name))
                end
            end

            -- Show other parsed fields
            for k, v in pairs(data.device.manufacturer_data.parsed_value) do
                if k ~= "vendor" and type(v) ~= "table" then
                    io.write(string.format("      %s: %s\n", k, tostring(v)))
                end
            end
        end
    else
        io.write("  Manufacturer Data: none\n")
    end

    -- Service Data section
    local service_data_keys = {}
    for k in pairs(data.device.service_data) do
        table.insert(service_data_keys, k)
    end

    if #service_data_keys > 0 then
        io.write("  Service Data:\n")
        table.sort(service_data_keys)
        for _, k in ipairs(service_data_keys) do
            io.write(string.format("    - %s: %s\n", k, blim.bytes_to_hex(data.device.service_data[k])))
        end
    else
        io.write("  Service Data: none\n")
    end

    -- Device Information Service section
    if data.device_info and next(data.device_info) ~= nil then
        io.write("  Device Information Service:\n")
        -- Define display order for DIS fields
        local dis_order = {
            "Manufacturer Name",
            "Model Number",
            "Serial Number",
            "Hardware Revision",
            "Firmware Revision",
            "Software Revision",
            "System ID",
            "PnP ID"
        }
        for _, field_name in ipairs(dis_order) do
            if data.device_info[field_name] then
                io.write(string.format("    %s: %s\n", field_name, data.device_info[field_name]))
            end
        end
    end

    -- GATT Services section
    io.write(string.format("  GATT Services: %d\n", #data.services))

    -- List services with characteristics
    for service_index, service in ipairs(data.services) do
        ---- Show service name if it's DIS
        --local service_name = service.uuid
        --if string.upper(service.uuid) == "180A" then
        --    service_name = "Device Information Service (0x180A)"
        --else
        --    service_name = string.format("0x%s", service.uuid)
        --end

        local service_name = blim.format_named(service)
        io.write(string.format("\n[%d] Service: %s\n", service_index, service_name))

        for char_index, char in ipairs(service.characteristics) do
            -- Show characteristic name
            local char_display = blim.format_named(char)
            io.write(string.format("  [%d.%d] Characteristic: %s\n",
                service_index, char_index, char_display))

            -- Show properties on separate line
            if char.properties then
                local prop_names = {}
                for _, prop in ipairs(char.properties) do
                    local prop_name = prop.name

                    -- Add (Auth) suffix for authenticated properties
                    if prop_name == "AuthenticatedSignedWrites" then
                        prop_name = prop_name .. "(Auth)"
                    end

                    table.insert(prop_names, prop_name)
                end
                local props_display = table.concat(prop_names, ", ")
                if props_display ~= "" then
                    io.write(string.format("      properties: %s\n", props_display))
                else
                    -- No properties found - potentially hidden due to security
                    io.write("      properties: (none - may be hidden)\n")
                end
            end

            -- Show warning if pairing/authentication is required
            -- (Show outside properties block to catch cases where properties are missing/hidden)
            if char.requires_authentication then
                io.write("      ⚠️  Pairing may be required for full access\n")
            end

            -- Show characteristic value if available
            if char.value and char.value ~= "" then
                local value_hex = blim.bytes_to_hex(char.value)
                local value_ascii = blim.to_ascii(char.value)

                if value_hex ~= "" then
                    io.write(string.format("      value (hex):   %s\n", value_hex))
                end
                if value_ascii ~= "" then
                    io.write(string.format("      value (ascii): %s\n", value_ascii))
                end

                -- Show parsed value if available (similar to descriptor pattern)
                if char.parsed_value ~= nil then
                    io.write(string.format("      value (parsed): %s\n", tostring(char.parsed_value)))
                end
            end

            -- Show descriptors if available (with recursive printing for aggregate formats)
            if #char.descriptors > 0 then
                -- Build set of descriptor indices referenced by 2905 aggregate descriptors
                local referenced_indices = {}
                for _, desc in ipairs(char.descriptors) do
                    if string.upper(desc.uuid) == "2905" and desc.parsed_value and desc.parsed_value[1] then
                        -- The parsed_value is an array of referenced descriptors
                        for _, ref_desc in ipairs(desc.parsed_value) do
                            if ref_desc.index then
                                referenced_indices[ref_desc.index] = true
                            end
                        end
                    end
                end

                for _, descriptor in ipairs(char.descriptors) do
                    -- Skip 2904 only if THIS specific 2904's index is referenced by a 2905
                    if string.upper(descriptor.uuid) == "2904" and descriptor.index
                       and referenced_indices[descriptor.index] then
                        -- Skip: this specific 2904 is already shown inside a 2905
                    else
                        print_descriptor(descriptor, "      ", 0)
                    end
                end
            end
        end
    end
end

-- Format and output as JSON using the json library
local function output_json(data)
    -- Clean properties tables: convert dual-purpose tables (array+hash) to hash-only
    -- This ensures JSON encoder sees only named keys, not numeric array indices
    for _, service in ipairs(data.services) do
        for _, char in ipairs(service.characteristics) do
            if char.properties then
                local clean_props = {}
                -- Iterate using ipairs to preserve bit order
                for _, prop in ipairs(char.properties) do
                    -- Convert property name to lowercase snake_case for key
                    local key = string.lower(string.gsub(prop.name, " ", "_"))
                    clean_props[key] = {
                        value = prop.value,
                        name = prop.name
                    }
                end
                char.properties = clean_props
            end
        end
    end

    -- Simplify manufacturer_data for JSON output if no parsed_value
    -- (Backward compatibility: device_test expect simple string when no parser available)
    if data.device.manufacturer_data and not data.device.manufacturer_data.parsed_value then
        data.device.manufacturer_data = data.device.manufacturer_data.value
    end
    -- If manufacturer_data is nil, keep it as nil (outputs as JSON null)

    local json = require("json")
    print(json.encode(data))
end

-- Check for format argument (default to "text")
-- Supports both URL params (arg["format"]) and positional args (arg[1])
local format = "text"
if arg then
    if arg["format"] and arg["format"] ~= "" then
        format = arg["format"]
    elseif arg[1] and arg[1] ~= "" then
        format = arg[1]
    end
end

-- Collect device data once
local data = collect_device_data()

-- Output in requested format
if format == "json" then
    output_json(data)
else
    output_text(data)
end