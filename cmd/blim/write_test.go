//go:build test

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// WriteTestSuite provides testify/suite for proper test isolation
type WriteTestSuite struct {
	CommandTestSuite
	originalFlags struct {
		writeServiceUUID string
		writeCharUUID    string
		writeDescUUID    string
		writeHex         bool
		writeNoResponse  bool
		writeChunkSize   int
		writeTimeout     time.Duration
	}
}

// SetupSuite runs once before all device_test in the suite
func (suite *WriteTestSuite) SetupSuite() {
	suite.CommandTestSuite.SetupSuite()

	// Save original flag values
	suite.originalFlags.writeServiceUUID = writeServiceUUID
	suite.originalFlags.writeCharUUID = writeCharUUID
	suite.originalFlags.writeDescUUID = writeDescUUID
	suite.originalFlags.writeHex = writeHex
	suite.originalFlags.writeNoResponse = writeNoResponse
	suite.originalFlags.writeChunkSize = writeChunkSize
	suite.originalFlags.writeTimeout = writeTimeout
}

// TearDownSuite runs once after all device_test in the suite
func (suite *WriteTestSuite) TearDownSuite() {
	// Restore original flag values
	writeServiceUUID = suite.originalFlags.writeServiceUUID
	writeCharUUID = suite.originalFlags.writeCharUUID
	writeDescUUID = suite.originalFlags.writeDescUUID
	writeHex = suite.originalFlags.writeHex
	writeNoResponse = suite.originalFlags.writeNoResponse
	writeChunkSize = suite.originalFlags.writeChunkSize
	writeTimeout = suite.originalFlags.writeTimeout
}

// SetupTest runs before each test in the suite
func (suite *WriteTestSuite) SetupTest() {
	// Reset flags before each test for proper isolation
	writeServiceUUID = ""
	writeCharUUID = ""
	writeDescUUID = ""
	writeHex = false
	writeNoResponse = false
	writeChunkSize = 0
	writeTimeout = 5 * time.Second

	suite.CommandTestSuite.SetupTest()
}

func (suite *WriteTestSuite) TestParseWriteData_HexFormats() {
	// GOAL: Verify hex data parsing handles various separator formats
	//
	// TEST SCENARIO: Parse hex with separators → cleaned and decoded → correct bytes returned

	tests := []struct {
		name     string
		input    string
		expected []byte
	}{
		{
			name:     "simple hex no separators",
			input:    "0102FF",
			expected: []byte{0x01, 0x02, 0xFF},
		},
		{
			name:     "hex with spaces",
			input:    "01 02 FF",
			expected: []byte{0x01, 0x02, 0xFF},
		},
		{
			name:     "hex with colons",
			input:    "01:02:FF",
			expected: []byte{0x01, 0x02, 0xFF},
		},
		{
			name:     "hex with dashes",
			input:    "01-02-FF",
			expected: []byte{0x01, 0x02, 0xFF},
		},
		{
			name:     "hex with 0x prefixes",
			input:    "0x01 0x02 0xFF",
			expected: []byte{0x01, 0x02, 0xFF},
		},
		{
			name:     "mixed separators",
			input:    "0x01:02-03 04",
			expected: []byte{0x01, 0x02, 0x03, 0x04},
		},
		{
			name:     "single byte",
			input:    "AB",
			expected: []byte{0xAB},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			writeHex = true

			result, err := parseWriteData(tt.input)
			suite.Assert().NoError(err, "MUST parse valid hex data")
			suite.Assert().Equal(tt.expected, result, "decoded bytes MUST match expected")
		})
	}
}

func (suite *WriteTestSuite) TestParseWriteData_InvalidHex() {
	// GOAL: Verify error on malformed hex input
	//
	// TEST SCENARIO: Parse invalid hex characters → error returned → result is nil

	writeHex = true

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "non-hex characters",
			input: "ZZZZ",
		},
		{
			name:  "odd length hex",
			input: "0",
		},
		{
			name:  "invalid after cleaning",
			input: "GG",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			result, err := parseWriteData(tt.input)
			suite.Assert().Error(err, "MUST fail on invalid hex")
			suite.Assert().Nil(result, "result MUST be nil on error")
			suite.Assert().Contains(err.Error(), "invalid hex data", "error MUST indicate hex failure")
		})
	}
}

func (suite *WriteTestSuite) TestParseWriteData_UTF8Default() {
	// GOAL: Verify default UTF-8 string conversion
	//
	// TEST SCENARIO: Parse without flags → UTF-8 encoding → bytes match string encoding

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "ASCII string",
			input: "Hello, World!",
		},
		{
			name:  "UTF-8 multibyte",
			input: "Test 世界 123",
		},
		{
			name:  "special characters",
			input: "!@#$%^&*()",
		},
		{
			name:  "empty string",
			input: "",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			writeHex = false

			result, err := parseWriteData(tt.input)
			suite.Assert().NoError(err, "MUST parse UTF-8 string")
			suite.Assert().Equal([]byte(tt.input), result, "UTF-8 bytes MUST match input")
		})
	}
}

func (suite *WriteTestSuite) TestWriteCmd_Flags() {
	// GOAL: Verify write command has all required flags configured correctly
	//
	// TEST SCENARIO: Check flag definitions → all flags present → default values match spec

	suite.Assert().NotNil(writeCmd, "write command MUST be defined")
	suite.Assert().Equal("write <device-address> <uuid> <data>", writeCmd.Use, "command usage MUST match expected format")

	stringFlags := []struct {
		name         string
		defaultValue string
	}{
		{name: "service", defaultValue: ""},
		{name: "char", defaultValue: ""},
		{name: "desc", defaultValue: ""},
	}

	for _, f := range stringFlags {
		suite.Run(f.name, func() {
			flag := writeCmd.Flags().Lookup(f.name)
			suite.Assert().NotNil(flag, "flag MUST exist")
			suite.Assert().Equal(f.defaultValue, flag.DefValue, "default value MUST match")
		})
	}

	// Boolean flags
	boolFlags := []string{"hex", "without-response"}
	for _, name := range boolFlags {
		suite.Run(name, func() {
			flag := writeCmd.Flags().Lookup(name)
			suite.Assert().NotNil(flag, "boolean flag MUST exist")
		})
	}

	// Timeout flag
	suite.Run("timeout", func() {
		flag := writeCmd.Flags().Lookup("timeout")
		suite.Assert().NotNil(flag, "timeout flag MUST exist")
		suite.Assert().Equal("5s", flag.DefValue, "default timeout MUST be 5 seconds")
	})

	// Chunk flag
	suite.Run("chunk", func() {
		flag := writeCmd.Flags().Lookup("chunk")
		suite.Assert().NotNil(flag, "chunk flag MUST exist")
		suite.Assert().Equal("0", flag.DefValue, "default chunk size MUST be 0 (auto)")
	})
}

func (suite *WriteTestSuite) TestWriteCmd_ArgsValidation() {
	// GOAL: Verify command accepts 2-3 arguments (address, UUID, optional data)
	//
	// TEST SCENARIO: Validate args with different counts → accepts 2-3 args → rejects invalid counts

	validator := writeCmd.Args
	suite.Assert().NotNil(validator, "args validator MUST be defined")

	tests := []struct {
		name      string
		args      []string
		shouldErr bool
	}{
		{
			name:      "valid with address and UUID",
			args:      []string{"AA:BB:CC:DD:EE:FF", "2a06"},
			shouldErr: false,
		},
		{
			name:      "valid with address, UUID, and data",
			args:      []string{"AA:BB:CC:DD:EE:FF", "2a06", "test"},
			shouldErr: false,
		},
		{
			name:      "invalid with only address",
			args:      []string{"AA:BB:CC:DD:EE:FF"},
			shouldErr: true,
		},
		{
			name:      "invalid with no arguments",
			args:      []string{},
			shouldErr: true,
		},
		{
			name:      "invalid with too many arguments",
			args:      []string{"AA:BB:CC:DD:EE:FF", "2a06", "data", "extra"},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			err := validator(writeCmd, tt.args)
			if tt.shouldErr {
				suite.Assert().Error(err, "MUST reject invalid argument count")
			} else {
				suite.Assert().NoError(err, "MUST accept valid argument count")
			}
		})
	}
}

// TestWriteCommandSuite runs the test suite
func TestWriteCommandSuite(t *testing.T) {
	suite.Run(t, new(WriteTestSuite))
}
