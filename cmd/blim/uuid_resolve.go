package main

import (
	"fmt"
	"strings"

	"github.com/srgg/blim/internal/device"
)

// doResolveTarget resolves a characteristic or descriptor from various input combinations.
// Returns the characteristic, optional descriptor, resolved service UUID, and any error.
//
// Resolution cases:
//  1. Explicit service + char + optional desc: Direct lookup in a specified service
//  2. Auto-resolve: Search all services for matching target UUID
//
// Parameters:
//   - conn: Active BLE connection
//   - targetUUID: Primary UUID to resolve (positional argument)
//   - serviceUUID: Optional explicit service UUID (--service flag)
//   - charUUID: Optional explicit characteristic UUID (--char flag)
//   - descUUID: Optional explicit descriptor UUID (--desc flag)
func doResolveTarget(conn device.Connection, targetUUID, serviceUUID, charUUID, descUUID string) (device.Characteristic, device.Descriptor, string, error) {
	normalizedTarget := device.NormalizeUUID(targetUUID)
	normalizedService := device.NormalizeUUID(serviceUUID)
	normalizedChar := device.NormalizeUUID(charUUID)
	normalizedDesc := device.NormalizeUUID(descUUID)
	isDescriptor := descUUID != ""

	// Case 1: Explicit service provided
	if serviceUUID != "" && (charUUID != "" || normalizedTarget != "") {
		charToFind := normalizedChar
		if charToFind == "" {
			charToFind = normalizedTarget
		}

		char, err := conn.GetCharacteristic(normalizedService, charToFind)
		if err != nil {
			return nil, nil, "", fmt.Errorf("characteristic %s not found in service %s: %w", charToFind, serviceUUID, err)
		}

		if isDescriptor {
			descToFind := normalizedDesc
			if descToFind == "" {
				descToFind = normalizedTarget
			}
			desc := findDescriptor(char, descToFind)
			if desc == nil {
				return nil, nil, "", fmt.Errorf("descriptor %s not found in characteristic %s", descToFind, charToFind)
			}
			return char, desc, normalizedService, nil
		}

		return char, nil, normalizedService, nil
	}

	// Case 2: Auto-resolve by searching all services
	type charWithService struct {
		char        device.Characteristic
		serviceUUID string
	}
	var foundChars []charWithService
	var foundDescs []device.Descriptor
	var foundCharForDesc device.Characteristic
	var foundServiceForDesc string

	for _, svc := range conn.Services() {
		svcUUID := svc.UUID()
		for _, char := range svc.GetCharacteristics() {
			if device.NormalizeUUID(char.UUID()) == normalizedTarget {
				foundChars = append(foundChars, charWithService{char: char, serviceUUID: svcUUID})
			}

			if isDescriptor {
				for _, desc := range char.GetDescriptors() {
					if device.NormalizeUUID(desc.UUID()) == normalizedTarget {
						foundDescs = append(foundDescs, desc)
						foundCharForDesc = char
						foundServiceForDesc = svcUUID
					}
				}
			}
		}
	}

	if isDescriptor {
		if len(foundDescs) == 0 {
			return nil, nil, "", fmt.Errorf("descriptor %s not found", normalizedTarget)
		}
		if len(foundDescs) > 1 {
			return nil, nil, "", fmt.Errorf("descriptor %s found in multiple characteristics, specify --service and --char", normalizedTarget)
		}
		return foundCharForDesc, foundDescs[0], foundServiceForDesc, nil
	}

	if len(foundChars) == 0 {
		return nil, nil, "", fmt.Errorf("characteristic %s not found", normalizedTarget)
	}
	if len(foundChars) > 1 {
		return nil, nil, "", fmt.Errorf("characteristic %s found in multiple services, specify --service", normalizedTarget)
	}

	return foundChars[0].char, nil, foundChars[0].serviceUUID, nil
}

// findDescriptor searches a characteristic's descriptors for one matching the UUID.
// Returns nil if not found.
func findDescriptor(char device.Characteristic, descUUID string) device.Descriptor {
	for _, desc := range char.GetDescriptors() {
		if device.NormalizeUUID(desc.UUID()) == descUUID {
			return desc
		}
	}
	return nil
}

// parseCSVUUIDs parses a comma-separated string of UUIDs into a slice.
// Handles whitespace and filters empty elements.
//
// Examples:
//
//	"2a37" -> []string{"2a37"}
//	"2a37,2a38" -> []string{"2a37", "2a38"}
//	"2a37, 2a38, 2a19" -> []string{"2a37", "2a38", "2a19"}
func parseCSVUUIDs(input string) []string {
	var result []string
	for _, u := range strings.Split(input, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			result = append(result, u)
		}
	}
	return result
}

// resolveDescriptor resolves a descriptor by UUID, optionally within a specific service/characteristic.
// Returns the parent characteristic, descriptor, and service UUID.
func resolveDescriptor(conn device.Connection, descUUID, serviceUUID, charUUID string) (device.Characteristic, device.Descriptor, string, error) {
	return doResolveTarget(conn, descUUID, serviceUUID, charUUID, descUUID)
}

// resolveCharacteristics resolves characteristics from a CSV string or returns all in a service.
//
// Resolution cases:
//  1. charUUIDsCSV provided + serviceUUID: Resolve specific chars in that service
//  2. charUUIDsCSV provided + no serviceUUID: Auto-resolve each char across all services
//  3. charUUIDsCSV empty + serviceUUID: Return ALL characteristics in that service
//  4. charUUIDsCSV empty + no serviceUUID: Error (no targets specified)
//
// Returns a map of serviceUUID → []charUUID, total count, and a map of UUID to Characteristic.
func resolveCharacteristics(conn device.Connection, charUUIDsCSV, serviceUUID string) (serviceChars map[string][]string, totalChars int, chars map[string]device.Characteristic, err error) {
	charUUIDs := parseCSVUUIDs(charUUIDsCSV)

	serviceChars = make(map[string][]string)
	chars = make(map[string]device.Characteristic)

	// Case 3: All characteristics in a specific service
	if len(charUUIDs) == 0 && serviceUUID != "" {
		svcUUID := device.NormalizeUUID(serviceUUID)
		svc, err := conn.GetService(svcUUID)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("service %s not found: %w", serviceUUID, err)
		}

		for _, char := range svc.GetCharacteristics() {
			normalizedCharUUID := device.NormalizeUUID(char.UUID())
			serviceChars[svcUUID] = append(serviceChars[svcUUID], normalizedCharUUID)
			chars[normalizedCharUUID] = char
		}

		if len(chars) == 0 {
			return nil, 0, nil, fmt.Errorf("no characteristics found in service %s", serviceUUID)
		}

		return serviceChars, len(chars), chars, nil
	}

	// Case 4: No targets specified
	if len(charUUIDs) == 0 {
		return nil, 0, nil, fmt.Errorf("no UUIDs provided")
	}

	// Case 1: Service specified explicitly - all chars must be in this service
	if serviceUUID != "" {
		svcUUID := device.NormalizeUUID(serviceUUID)

		for _, charUUID := range charUUIDs {
			char, err := conn.GetCharacteristic(svcUUID, charUUID)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("characteristic %s not found in service %s: %w", charUUID, serviceUUID, err)
			}
			normalizedCharUUID := device.NormalizeUUID(char.UUID())
			serviceChars[svcUUID] = append(serviceChars[svcUUID], normalizedCharUUID)
			chars[normalizedCharUUID] = char
		}
	} else {
		// Case 2: No service specified - auto-resolve each characteristic
		for _, charUUID := range charUUIDs {
			char, _, svcUUID, err := doResolveTarget(conn, charUUID, "", "", "")
			if err != nil {
				return nil, 0, nil, err
			}
			normalizedCharUUID := device.NormalizeUUID(char.UUID())
			serviceChars[svcUUID] = append(serviceChars[svcUUID], normalizedCharUUID)
			chars[normalizedCharUUID] = char
		}
	}

	// Count total characteristics
	for _, svcChars := range serviceChars {
		totalChars += len(svcChars)
	}

	return serviceChars, totalChars, chars, nil
}
