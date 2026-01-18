package device

import (
	"encoding/binary"
	"fmt"

	"github.com/srg/blim/internal/bledb"
)

// Well-known GATT characteristic UUIDs (16-bit short form, normalized without dashes)
const (
	CharacteristicAppearance = "2a01"
)

// CharacteristicParser is a function that parses a characteristic value
type CharacteristicParser func([]byte) (interface{}, error)

// parseAppearance parses the Appearance characteristic (0x2A01) value
// Returns human-readable appearance name (e.g., "Phone"), or nil if unknown
func parseAppearance(value []byte) (interface{}, error) {
	if len(value) != 2 {
		return nil, fmt.Errorf("appearance value must be 2 bytes, got %d", len(value))
	}

	code := binary.LittleEndian.Uint16(value)
	name := bledb.LookupAppearanceCode(code)

	// Return nil if unknown (bledb returns empty string for unknown codes)
	if name == "" {
		return nil, nil
	}

	return name, nil
}

// characteristicParsers maps normalized characteristic UUIDs to their parser functions
var characteristicParsers = map[string]CharacteristicParser{
	CharacteristicAppearance: parseAppearance,
}

// IsParsableCharacteristic returns true if the characteristic UUID supports value parsing
func IsParsableCharacteristic(uuid string) bool {
	normalizedUUID := NormalizeUUID(uuid)
	_, exists := characteristicParsers[normalizedUUID]
	return exists
}

// ParseCharacteristicValue parses a characteristic value based on its UUID.
// Returns the parsed value for well-known characteristic value, or nil for not-unknown descriptors.
// Returns (nil, nil) for empty data.
func ParseCharacteristicValue(uuid string, value []byte) (interface{}, error) {
	// Normalize UUID for comparison (remove dashes, lowercase)
	normalizedUUID := NormalizeUUID(uuid)

	parser, exists := characteristicParsers[normalizedUUID]
	if !exists {
		// Unknown characteristic UUID, return nil
		return nil, nil
	}

	return parser(value)
}
