package device

import (
	"fmt"

	"github.com/srgg/blim/internal/bledb"
)

// NormalizeUUID is re-exported from bledb for convenience.
// It converts a UUID string to the internal BLE library format (lowercase, no dashes).
// Handles both standard UUID format (with dashes) and already normalized format (without dashes).
// Also strips 0x prefix if present (e.g., "0x2902" -> "2902").
// For full 128-bit UUIDs in Bluetooth SIG base format (0000xxxx-0000-1000-8000-00805f9b34fb),
// extracts the 16-bit short form (xxxx).
func NormalizeUUID(uuid string) string {
	return bledb.NormalizeUUID(uuid)
}

// NormalizeUUIDs is re-exported from bledb for convenience.
// It normalizes a slice of UUID strings to internal format.
func NormalizeUUIDs(uuids []string) []string {
	return bledb.NormalizeUUIDs(uuids)
}

// ShortenUUID returns a truncated version of a UUID for display purposes.
// Returns the first eight characters for long UUIDs and short UUIDs by themselves.
func ShortenUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

// ValidateUUID validates that UUID strings are non-empty and well-formed.
// Returns normalized UUID strings or an error.
// Accepts one or more UUIDs as variadic arguments.
func ValidateUUID(uuids ...string) ([]string, error) {
	if len(uuids) == 0 {
		return nil, fmt.Errorf("at least one UUID is required")
	}

	result := make([]string, 0, len(uuids))
	for i, uuid := range uuids {
		if uuid == "" {
			return nil, fmt.Errorf("UUID at index %d cannot be empty", i)
		}
		// Normalize and validate
		normalized := NormalizeUUID(uuid)
		if normalized == "" {
			return nil, fmt.Errorf("invalid UUID format at index %d: %s", i, uuid)
		}
		result = append(result, normalized)
	}
	return result, nil
}
