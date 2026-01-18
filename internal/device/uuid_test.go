package device_test

import (
	"strings"
	"testing"

	"github.com/srg/blim/internal/device"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeUUID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// 16-bit UUID formats
		{
			name:     "16-bit UUID lowercase",
			input:    "2902",
			expected: "2902",
		},
		{
			name:     "16-bit UUID uppercase",
			input:    "2902",
			expected: "2902",
		},
		{
			name:     "16-bit UUID with 0x prefix lowercase",
			input:    "0x2902",
			expected: "2902",
		},
		{
			name:     "16-bit UUID with 0X prefix uppercase",
			input:    "0X2902",
			expected: "2902",
		},

		// Bluetooth SIG base UUID format (should extract 16-bit form)
		{
			name:     "Full Bluetooth SIG UUID with dashes",
			input:    "0000-2902-0000-1000-8000-00805f9b34fb",
			expected: "2902",
		},
		{
			name:     "Full Bluetooth SIG UUID without dashes",
			input:    "0000290200001000800000805f9b34fb",
			expected: "2902",
		},
		{
			name:     "Full Bluetooth SIG UUID uppercase",
			input:    "00002902-0000-1000-8000-00805F9B34FB",
			expected: "2902",
		},
		{
			name:     "Full Bluetooth SIG UUID - different 16-bit value",
			input:    "0000180d-0000-1000-8000-00805f9b34fb",
			expected: "180d",
		},

		// Custom 128-bit UUIDs (should NOT be shortened)
		{
			name:     "Custom UUID - wrong prefix",
			input:    "AA002902-0000-1000-8000-00805f9b34fb",
			expected: "aa00290200001000800000805f9b34fb",
		},
		{
			name:     "Custom UUID - wrong suffix",
			input:    "00002902-1234-5678-9abc-def012345678",
			expected: "00002902123456789abcdef012345678",
		},
		{
			name:     "Custom UUID - completely different",
			input:    "6e400001-b5a3-f393-e0a9-e50e24dcca9e",
			expected: "6e400001b5a3f393e0a9e50e24dcca9e",
		},

		// Edge cases
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "UUID with multiple dashes",
			input:    "0000-2902-0000-1000-8000-00805f9b34fb",
			expected: "2902",
		},
		{
			name:     "Mixed case",
			input:    "0000-2A02-0000-1000-8000-00805F9B34FB",
			expected: "2a02",
		},

		// 32-bit and other length UUIDs
		{
			name:     "32-bit UUID format",
			input:    "12345678",
			expected: "12345678",
		},
		{
			name:     "Partial UUID",
			input:    "00002902",
			expected: "00002902",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := device.NormalizeUUID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeUUIDs(t *testing.T) {
	input := []string{
		"2902",
		"0x180d",
		"0000-2a37-0000-1000-8000-00805f9b34fb",
		"6e400001-b5a3-f393-e0a9-e50e24dcca9e",
	}

	expected := []string{
		"2902",
		"180d",
		"2a37",
		"6e400001b5a3f393e0a9e50e24dcca9e",
	}

	result := device.NormalizeUUIDs(input)
	assert.Equal(t, expected, result)
}

// Test that normalization is consistent
func TestNormalizeUUID_Consistency(t *testing.T) {
	// All these should normalize to the same value
	uuidVariants := []string{
		"2902",
		"0x2902",
		"0X2902",
		"00002902-0000-1000-8000-00805f9b34fb",
		"0000-2902-0000-1000-8000-00805f9b34fb",
	}

	expected := "2902"

	for _, uuid := range uuidVariants {
		t.Run(uuid, func(t *testing.T) {
			result := device.NormalizeUUID(uuid)
			assert.Equal(t, expected, result, "UUID %s should normalize to %s", uuid, expected)
		})
	}
}

// Test edge cases that should NOT be shortened
func TestNormalizeUUID_NoShortening(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		reason string
	}{
		{
			name:   "Wrong prefix - AA00 instead of 0000",
			input:  "AA002902-0000-1000-8000-00805f9b34fb",
			reason: "prefix is not 0000",
		},
		{
			name:   "Wrong suffix - custom UUID",
			input:  "00002902-1234-5678-9abc-def012345678",
			reason: "suffix doesn't match Bluetooth SIG base",
		},
		{
			name:   "Too short",
			input:  "00002902",
			reason: "only 8 chars, not 32",
		},
		{
			name:   "Too long",
			input:  "0000290200001000800000805f9b34fb00",
			reason: "34 chars, not 32",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := device.NormalizeUUID(tt.input)
			// Should be normalized (lowercase, no dashes) but NOT shortened to 4 chars
			assert.NotEqual(t, "2902", result, "Should NOT shorten: %s", tt.reason)
			assert.NotContains(t, result, "-", "Should have dashes removed")
			// Compare normalized forms: result should equal input with dashes removed and lowercased
			expectedNormalized := strings.ToLower(strings.ReplaceAll(tt.input, "-", ""))
			assert.Equal(t, expectedNormalized, result)
		})
	}
}
