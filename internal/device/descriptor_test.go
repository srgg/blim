//go:build test

package device_test

import (
	"testing"

	"github.com/srgg/blim/internal/device"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------
// DescriptorError Tests
// ----------------------------

func TestDescriptorError_Error(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		err      error
		expected string
	}{
		{
			name:     "timeout without underlying error",
			reason:   "timeout",
			err:      nil,
			expected: "timeout",
		},
		{
			name:     "read_error with underlying error",
			reason:   "read_error",
			err:      assert.AnError,
			expected: "read_error: assert.AnError general error for testing",
		},
		{
			name:     "parse_error with underlying error",
			reason:   "parse_error",
			err:      assert.AnError,
			expected: "parse_error: assert.AnError general error for testing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descErr := &device.DescriptorError{
				Reason: tt.reason,
				Err:    tt.err,
			}
			assert.Equal(t, tt.expected, descErr.Error())
		})
	}
}

// ----------------------------
// ParseExtendedProperties Tests
// ----------------------------

func TestParseExtendedProperties(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *device.ExtendedProperties
		wantErr  bool
	}{
		{
			name: "both properties disabled",
			data: []byte{0x00, 0x00},
			expected: &device.ExtendedProperties{
				ReliableWrite:       false,
				WritableAuxiliaries: false,
			},
			wantErr: false,
		},
		{
			name: "reliable write enabled",
			data: []byte{0x01, 0x00},
			expected: &device.ExtendedProperties{
				ReliableWrite:       true,
				WritableAuxiliaries: false,
			},
			wantErr: false,
		},
		{
			name: "writable auxiliaries enabled",
			data: []byte{0x02, 0x00},
			expected: &device.ExtendedProperties{
				ReliableWrite:       false,
				WritableAuxiliaries: true,
			},
			wantErr: false,
		},
		{
			name: "both properties enabled",
			data: []byte{0x03, 0x00},
			expected: &device.ExtendedProperties{
				ReliableWrite:       true,
				WritableAuxiliaries: true,
			},
			wantErr: false,
		},
		{
			name:     "invalid length - too short",
			data:     []byte{0x01},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "invalid length - too long",
			data:     []byte{0x01, 0x00, 0x00},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "empty data",
			data:     []byte{},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseExtendedProperties(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParseClientConfig Tests
// ----------------------------

func TestParseClientConfig(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *device.ClientConfig
		wantErr  bool
	}{
		{
			name: "both disabled",
			data: []byte{0x00, 0x00},
			expected: &device.ClientConfig{
				Notifications: false,
				Indications:   false,
			},
			wantErr: false,
		},
		{
			name: "notifications enabled",
			data: []byte{0x01, 0x00},
			expected: &device.ClientConfig{
				Notifications: true,
				Indications:   false,
			},
			wantErr: false,
		},
		{
			name: "indications enabled",
			data: []byte{0x02, 0x00},
			expected: &device.ClientConfig{
				Notifications: false,
				Indications:   true,
			},
			wantErr: false,
		},
		{
			name: "both enabled",
			data: []byte{0x03, 0x00},
			expected: &device.ClientConfig{
				Notifications: true,
				Indications:   true,
			},
			wantErr: false,
		},
		{
			name:     "invalid length - too short",
			data:     []byte{0x01},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "invalid length - too long",
			data:     []byte{0x01, 0x00, 0x00},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseClientConfig(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParseServerConfig Tests
// ----------------------------

func TestParseServerConfig(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *device.ServerConfig
		wantErr  bool
	}{
		{
			name: "broadcasts disabled",
			data: []byte{0x00, 0x00},
			expected: &device.ServerConfig{
				Broadcasts: false,
			},
			wantErr: false,
		},
		{
			name: "broadcasts enabled",
			data: []byte{0x01, 0x00},
			expected: &device.ServerConfig{
				Broadcasts: true,
			},
			wantErr: false,
		},
		{
			name:     "invalid length - too short",
			data:     []byte{0x01},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "invalid length - too long",
			data:     []byte{0x01, 0x00, 0x00},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseServerConfig(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParseUserDescription Tests
// ----------------------------

func TestParseUserDescription(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
		wantErr  bool
	}{
		{
			name:     "simple string",
			data:     []byte("Temperature"),
			expected: "Temperature",
			wantErr:  false,
		},
		{
			name:     "string with null termination",
			data:     []byte("Heart Rate\x00"),
			expected: "Heart Rate",
			wantErr:  false,
		},
		{
			name:     "string with multiple null terminators",
			data:     []byte("Battery Level\x00\x00\x00"),
			expected: "Battery Level",
			wantErr:  false,
		},
		{
			name:     "empty string",
			data:     []byte{},
			expected: "",
			wantErr:  false,
		},
		{
			name:     "only null terminators",
			data:     []byte("\x00\x00\x00"),
			expected: "",
			wantErr:  false,
		},
		{
			name:     "string with spaces",
			data:     []byte("Device Name"),
			expected: "Device Name",
			wantErr:  false,
		},
		{
			name:     "invalid UTF-8",
			data:     []byte{0xff, 0xfe, 0xfd},
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseUserDescription(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParsePresentationFormat Tests
// ----------------------------

func TestParsePresentationFormat(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *device.PresentationFormat
		wantErr  bool
	}{
		{
			name: "valid format - uint8",
			data: []byte{0x04, 0x00, 0x00, 0x27, 0x01, 0x00, 0x00},
			expected: &device.PresentationFormat{
				Format:      device.FormatUint8,
				Exponent:    0,
				Unit:        0x2700, // unitless
				Namespace:   0x01,   // Bluetooth SIG
				Description: 0x0000,
			},
			wantErr: false,
		},
		{
			name: "valid format - sint16 with exponent",
			data: []byte{0x0E, 0xFE, 0x2F, 0x27, 0x01, 0x01, 0x00},
			expected: &device.PresentationFormat{
				Format:      device.FormatSint16,
				Exponent:    -2, // int8(-2)
				Unit:        0x272F,
				Namespace:   0x01,
				Description: 0x0001,
			},
			wantErr: false,
		},
		{
			name: "valid format - UTF8 string",
			data: []byte{0x19, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			expected: &device.PresentationFormat{
				Format:      device.FormatUTF8,
				Exponent:    0,
				Unit:        0x0000,
				Namespace:   0x00,
				Description: 0x0000,
			},
			wantErr: false,
		},
		{
			name:     "invalid length - too short",
			data:     []byte{0x04, 0x00, 0x00, 0x27, 0x01, 0x00},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "invalid length - too long",
			data:     []byte{0x04, 0x00, 0x00, 0x27, 0x01, 0x00, 0x00, 0x00},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "empty data",
			data:     []byte{},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParsePresentationFormat(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParseValidRange Tests
// ----------------------------

func TestParseValidRange(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *device.ValidRange
		wantErr  bool
	}{
		{
			name: "2 byte range",
			data: []byte{0x00, 0xFF},
			expected: &device.ValidRange{
				MinValue: []byte{0x00},
				MaxValue: []byte{0xFF},
			},
			wantErr: false,
		},
		{
			name: "4 byte range (uint16 min/max)",
			data: []byte{0x00, 0x00, 0xFF, 0xFF},
			expected: &device.ValidRange{
				MinValue: []byte{0x00, 0x00},
				MaxValue: []byte{0xFF, 0xFF},
			},
			wantErr: false,
		},
		{
			name: "odd length range",
			data: []byte{0x00, 0x00, 0xFF},
			expected: &device.ValidRange{
				MinValue: []byte{0x00},
				MaxValue: []byte{0x00, 0xFF},
			},
			wantErr: false,
		},
		{
			name:     "too short - single byte",
			data:     []byte{0x00},
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "empty data",
			data:     []byte{},
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseValidRange(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// ----------------------------
// ParseDescriptorValue Tests (Dispatcher)
// ----------------------------

func TestParseDescriptorValue(t *testing.T) {
	tests := []struct {
		name         string
		uuid         string
		data         []byte
		expectedType interface{}
		wantErr      bool
	}{
		{
			name:         "extended properties descriptor",
			uuid:         "2900",
			data:         []byte{0x01, 0x00},
			expectedType: &device.ExtendedProperties{},
			wantErr:      false,
		},
		{
			name:         "extended properties descriptor with dashes",
			uuid:         "0x2900",
			data:         []byte{0x01, 0x00},
			expectedType: &device.ExtendedProperties{},
			wantErr:      false,
		},
		{
			name:         "user description descriptor",
			uuid:         "2901",
			data:         []byte("Test"),
			expectedType: "",
			wantErr:      false,
		},
		{
			name:         "client config descriptor",
			uuid:         "2902",
			data:         []byte{0x01, 0x00},
			expectedType: &device.ClientConfig{},
			wantErr:      false,
		},
		{
			name:         "server config descriptor",
			uuid:         "2903",
			data:         []byte{0x01, 0x00},
			expectedType: &device.ServerConfig{},
			wantErr:      false,
		},
		{
			name:         "presentation format descriptor",
			uuid:         "2904",
			data:         []byte{0x04, 0x00, 0x00, 0x27, 0x01, 0x00, 0x00},
			expectedType: &device.PresentationFormat{},
			wantErr:      false,
		},
		{
			name:         "valid range descriptor",
			uuid:         "2906",
			data:         []byte{0x00, 0xFF},
			expectedType: &device.ValidRange{},
			wantErr:      false,
		},
		{
			name:         "unknown descriptor UUID - returns raw bytes",
			uuid:         "1234",
			data:         []byte{0xAA, 0xBB, 0xCC},
			expectedType: []byte{},
			wantErr:      false,
		},
		{
			name:         "empty data - returns nil",
			uuid:         "2902",
			data:         []byte{},
			expectedType: nil,
			wantErr:      false,
		},
		{
			name:         "invalid extended properties data",
			uuid:         "2900",
			data:         []byte{0x01}, // too short
			expectedType: nil,
			wantErr:      true,
		},
		{
			name:         "invalid user description - bad UTF-8",
			uuid:         "2901",
			data:         []byte{0xff, 0xfe},
			expectedType: nil,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseDescriptorValue(tt.uuid, tt.data, nil)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if len(tt.data) == 0 {
					assert.Nil(t, result)
				} else {
					assert.IsType(t, tt.expectedType, result)
				}
			}
		})
	}
}

// ----------------------------
// Edge Cases and Integration Tests
// ----------------------------

func TestParseDescriptorValue_NormalizedUUID(t *testing.T) {
	// Test that UUID normalization works correctly
	testData := []byte{0x01, 0x00}

	uuidVariants := []string{
		"2902",
		"0x2902",
		"0000-2902-0000-1000-8000-00805f9b34fb",
		"00002902-0000-1000-8000-00805f9b34fb",
	}

	for _, uuid := range uuidVariants {
		t.Run("uuid_variant_"+uuid, func(t *testing.T) {
			result, err := device.ParseDescriptorValue(uuid, testData, nil)
			require.NoError(t, err)
			assert.IsType(t, &device.ClientConfig{}, result)

			clientConfig := result.(*device.ClientConfig)
			assert.True(t, clientConfig.Notifications)
			assert.False(t, clientConfig.Indications)
		})
	}
}

func TestParsePresentationFormat_AllFormatTypes(t *testing.T) {
	// Test various format type constants
	formatTests := []struct {
		format uint8
		name   string
	}{
		{device.FormatBoolean, "boolean"},
		{device.FormatUint8, "uint8"},
		{device.FormatUint16, "uint16"},
		{device.FormatSint8, "sint8"},
		{device.FormatSint16, "sint16"},
		{device.FormatFloat32, "float32"},
		{device.FormatUTF8, "utf8"},
	}

	for _, tt := range formatTests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte{tt.format, 0x00, 0x00, 0x27, 0x01, 0x00, 0x00}
			result, err := device.ParsePresentationFormat(data)
			require.NoError(t, err)
			assert.Equal(t, tt.format, result.Format)
		})
	}
}

func TestParseExtendedProperties_BitMasking(t *testing.T) {
	// Test that only bits 0 and 1 are used
	tests := []struct {
		name             string
		data             []byte
		expectedReliable bool
		expectedWritable bool
	}{
		{
			name:             "no bits set",
			data:             []byte{0x00, 0x00},
			expectedReliable: false,
			expectedWritable: false,
		},
		{
			name:             "bit 0 only (reliable write)",
			data:             []byte{0x01, 0x00},
			expectedReliable: true,
			expectedWritable: false,
		},
		{
			name:             "bit 1 only (writable auxiliaries)",
			data:             []byte{0x02, 0x00},
			expectedReliable: false,
			expectedWritable: true,
		},
		{
			name:             "bits 0 and 1 (both enabled)",
			data:             []byte{0x03, 0x00},
			expectedReliable: true,
			expectedWritable: true,
		},
		{
			name:             "extra bits ignored",
			data:             []byte{0xFF, 0xFF}, // all bits set
			expectedReliable: true,
			expectedWritable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := device.ParseExtendedProperties(tt.data)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedReliable, result.ReliableWrite)
			assert.Equal(t, tt.expectedWritable, result.WritableAuxiliaries)
		})
	}
}
