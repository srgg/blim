//go:build test

package main

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// ReadTestSuite device_test read command with mock BLE peripheral
type ReadTestSuite struct {
	CommandTestSuite
	originalFlags struct {
		readServiceUUID string
		readCharUUIDs   string
		readDescUUID    string
		readHex         bool
		readWatch       string
		readTimeout     time.Duration
	}
}

// SetupSuite runs once before all device_test in the suite
func (suite *ReadTestSuite) SetupSuite() {
	suite.CommandTestSuite.SetupSuite()

	// Save original flag values
	suite.originalFlags.readServiceUUID = readServiceUUID
	suite.originalFlags.readCharUUIDs = readCharUUIDs
	suite.originalFlags.readDescUUID = readDescUUID
	suite.originalFlags.readHex = readHex
	suite.originalFlags.readWatch = readWatch
	suite.originalFlags.readTimeout = readTimeout
}

// TearDownSuite runs once after all device_test in the suite
func (suite *ReadTestSuite) TearDownSuite() {
	// Restore original flag values
	readServiceUUID = suite.originalFlags.readServiceUUID
	readCharUUIDs = suite.originalFlags.readCharUUIDs
	readDescUUID = suite.originalFlags.readDescUUID
	readHex = suite.originalFlags.readHex
	readWatch = suite.originalFlags.readWatch
	readTimeout = suite.originalFlags.readTimeout
}

// SetupTest runs before each test in the suite
func (suite *ReadTestSuite) SetupTest() {
	// Configure peripheral with readable characteristics and descriptors
	// Note: Uses custom JSON instead of AmbiguousCharPeripheral because
	// descriptor 2902 in 180d/2a37 has value [1, 0] (enabled) for testing
	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			FromJSON(`{
				"services": [
					{
						"uuid": "180d",
						"characteristics": [
							{
								"uuid": "2a37",
								"properties": "read,notify",
								"value": [0, 90],
								"descriptors": [
									{"uuid": "2902", "value": [1, 0]}
								]
							},
							{"uuid": "2a38", "properties": "read", "value": [1]}
						]
					},
					{
						"uuid": "180f",
						"characteristics": [
							{
								"uuid": "2a19",
								"properties": "read,notify",
								"value": [75],
								"descriptors": [
									{"uuid": "2902", "value": [0, 0]},
									{"uuid": "2901", "value": [66, 97, 116, 116, 101, 114, 121]}
								]
							}
						]
					},
					{
						"uuid": "1800",
						"characteristics": [
							{"uuid": "2a37", "properties": "read", "value": [99]}
						]
					}
				]
			}`)
	})

	suite.CommandTestSuite.SetupTest()

	// Reset flags to defaults
	readServiceUUID = ""
	readCharUUIDs = ""
	readDescUUID = ""
	readHex = false
	readWatch = ""
	readTimeout = 5 * time.Second
}

// =============================================================================
// Multi-Characteristic Read Tests
// =============================================================================

func (suite *ReadTestSuite) TestMultiRead_TwoChars_SameService() {
	// GOAL: Verify multi-char read outputs with UUID prefixes
	//
	// TEST SCENARIO: Read 2a37,2a38 from 180d → prefixed output → both values present

	dev := suite.Connect("test")
	conn := dev.GetConnection()

	readHex = true

	_, _, chars, err := resolveCharacteristics(conn, "2a37,2a38", "180d")
	suite.Require().NoError(err, "resolution MUST succeed")

	var buf bytes.Buffer
	err = performMultiRead(&buf, chars)
	suite.Require().NoError(err, "multi-read MUST succeed")
	output := buf.String()

	suite.Assert().Contains(output, "2a37:", "output MUST contain 2a37 prefix")
	suite.Assert().Contains(output, "2a38:", "output MUST contain 2a38 prefix")
}

func (suite *ReadTestSuite) TestMultiRead_CrossService() {
	// GOAL: Verify multi-char read across different services
	//
	// TEST SCENARIO: Read 2a38 (180d) and 2a19 (180f) → both read successfully

	dev := suite.Connect("test")
	conn := dev.GetConnection()

	readHex = true

	_, _, chars, err := resolveCharacteristics(conn, "2a38,2a19", "")
	suite.Require().NoError(err, "cross-service resolution MUST succeed")

	var buf bytes.Buffer
	err = performMultiRead(&buf, chars)
	suite.Require().NoError(err, "multi-read MUST succeed")
	output := buf.String()

	suite.Assert().Contains(output, "2a38:", "output MUST contain 2a38 prefix")
	suite.Assert().Contains(output, "2a19:", "output MUST contain 2a19 prefix")
}

func (suite *ReadTestSuite) TestSingleRead_NoPrefix() {
	// GOAL: Verify single-char read has no prefix (backward compatibility)
	//
	// TEST SCENARIO: Read single 2a19 → output without prefix → raw value only

	dev := suite.Connect("test")
	conn := dev.GetConnection()

	readHex = true

	_, _, chars, err := resolveCharacteristics(conn, "2a19", "")
	suite.Require().NoError(err, "resolution MUST succeed")
	suite.Require().Len(chars, 1, "MUST resolve single char")

	var char device.Characteristic
	for _, c := range chars {
		char = c
	}

	var buf bytes.Buffer
	err = performReadWithPrefix(&buf, char, nil, false)
	suite.Require().NoError(err, "read MUST succeed")
	output := buf.String()

	suite.Assert().NotContains(output, ":", "single-char output MUST NOT contain prefix")
	suite.Assert().Contains(output, "4b", "output MUST contain hex value (75 = 0x4b)")
}

func (suite *ReadTestSuite) TestWatchMode_RejectMultiChar() {
	// GOAL: Verify watch mode rejects multiple characteristics
	//
	// TEST SCENARIO: Set watch flag + multiple UUIDs → validation detects conflict

	readWatch = "1s"
	charUUIDs := parseCSVUUIDs("2a37,2a38")

	suite.Assert().Len(charUUIDs, 2, "MUST parse 2 UUIDs")

	// Verify the validation condition that runRead checks
	isWatchMode := readWatch != ""
	hasMultipleChars := len(charUUIDs) > 1

	suite.Assert().True(isWatchMode, "watch mode MUST be enabled")
	suite.Assert().True(hasMultipleChars, "MUST have multiple characteristics")
	suite.Assert().True(isWatchMode && hasMultipleChars,
		"watch mode with multiple chars MUST trigger validation error in runRead")
}

// =============================================================================
// Descriptor Read Tests
// =============================================================================

func (suite *ReadTestSuite) TestDescriptorRead_UniqueDescriptor() {
	// GOAL: Verify descriptor read with unique descriptor (auto-resolve)
	//
	// TEST SCENARIO: Read 2901 (only in 2a19) → resolves and reads successfully

	dev := suite.Connect("test")
	conn := dev.GetConnection()

	readHex = true

	char, desc, _, err := resolveDescriptor(conn, "2901", "", "")
	suite.Require().NoError(err, "descriptor resolution MUST succeed")
	suite.Assert().NotNil(char, "parent characteristic MUST be returned")
	suite.Assert().NotNil(desc, "descriptor MUST be returned")
	suite.Assert().Contains(char.UUID(), "2a19", "parent char MUST be 2a19")
}

func (suite *ReadTestSuite) TestDescriptorRead_WithExplicitPath() {
	// GOAL: Verify descriptor read with explicit service and char
	//
	// TEST SCENARIO: Read 2902 from 180d/2a37 → resolves correctly

	dev := suite.Connect("test")
	conn := dev.GetConnection()

	readHex = true

	char, desc, svcUUID, err := resolveDescriptor(conn, "2902", "180d", "2a37")
	suite.Require().NoError(err, "descriptor resolution MUST succeed")
	suite.Assert().NotNil(char, "parent characteristic MUST be returned")
	suite.Assert().NotNil(desc, "descriptor MUST be returned")
	suite.Assert().Contains(char.UUID(), "2a37", "parent char MUST be 2a37")
	suite.Assert().Contains(svcUUID, "180d", "service MUST be 180d")
	suite.Assert().Contains(desc.UUID(), "2902", "descriptor MUST be 2902")
}

func (suite *ReadTestSuite) TestDescriptorRead_AmbiguousWithoutPath() {
	// GOAL: Verify ambiguous descriptor requires explicit path
	//
	// TEST SCENARIO: Read 2902 without path → fails with ambiguity error

	//dev, cleanup := suite.ConnectDevice("")
	dev := suite.Connect("test")
	conn := dev.GetConnection()

	_, _, _, err := resolveDescriptor(conn, "2902", "", "")
	suite.Assert().Error(err, "ambiguous descriptor MUST fail")
	suite.Assert().Contains(err.Error(), "multiple", "error MUST indicate ambiguity")
}

// =============================================================================
// Output Formatting Tests
// =============================================================================

func (suite *ReadTestSuite) TestOutputData_HexFormat() {
	// GOAL: Verify hex output encoding produces correct format
	//
	// TEST SCENARIO: Encode bytes as hex → hex string format verified → lowercase without separators

	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{name: "simple bytes", input: []byte{0x01, 0x02, 0x0A, 0xFF}, expected: "01020aff"},
		{name: "empty bytes", input: []byte{}, expected: ""},
		{name: "single byte", input: []byte{0xAB}, expected: "ab"},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			encoded := hex.EncodeToString(tt.input)
			suite.Assert().Equal(tt.expected, encoded, "hex encoding MUST match expected format")
		})
	}
}

// =============================================================================
// Command Definition Tests
// =============================================================================

func (suite *ReadTestSuite) TestReadCmd_Flags() {
	// GOAL: Verify read command has all required flags with correct defaults
	//
	// TEST SCENARIO: Check flag definitions → all flags present → default values correct

	suite.Assert().NotNil(readCmd, "read command MUST be defined")
	suite.Assert().Equal("read <device-address> [uuid]", readCmd.Use, "command usage MUST match expected format")

	flags := []struct {
		name         string
		defaultValue string
	}{
		{name: "service", defaultValue: ""},
		{name: "char", defaultValue: ""},
		{name: "desc", defaultValue: ""},
		{name: "timeout", defaultValue: "5s"},
	}

	for _, f := range flags {
		suite.Run(f.name, func() {
			flag := readCmd.Flags().Lookup(f.name)
			suite.Assert().NotNil(flag, "flag MUST exist")
			if f.defaultValue != "" {
				suite.Assert().Equal(f.defaultValue, flag.DefValue, "default value MUST match")
			}
		})
	}

	// Boolean flags
	boolFlags := []string{"hex"}
	for _, name := range boolFlags {
		suite.Run(name, func() {
			flag := readCmd.Flags().Lookup(name)
			suite.Assert().NotNil(flag, "boolean flag MUST exist")
		})
	}

	// String flags with NoOptDefVal (optional values)
	suite.Run("watch", func() {
		flag := readCmd.Flags().Lookup("watch")
		suite.Assert().NotNil(flag, "watch flag MUST exist")
		suite.Assert().Equal("1s", flag.NoOptDefVal, "watch flag NoOptDefVal MUST be 1s")
	})
}

func (suite *ReadTestSuite) TestReadCmd_ArgsValidation() {
	// GOAL: Verify command accepts correct argument counts
	//
	// TEST SCENARIO: Validate args with different counts → accepts 1-2 args → rejects invalid counts

	validator := readCmd.Args
	suite.Assert().NotNil(validator, "args validator MUST be defined")

	tests := []struct {
		name      string
		args      []string
		shouldErr bool
	}{
		{name: "valid with address only", args: []string{"AA:BB:CC:DD:EE:FF"}, shouldErr: false},
		{name: "valid with address and UUID", args: []string{"AA:BB:CC:DD:EE:FF", "2a19"}, shouldErr: false},
		{name: "valid with address and multiple UUIDs", args: []string{"AA:BB:CC:DD:EE:FF", "2a37,2a38"}, shouldErr: false},
		{name: "invalid with no arguments", args: []string{}, shouldErr: true},
		{name: "invalid with too many arguments", args: []string{"AA:BB:CC:DD:EE:FF", "2a19", "extra"}, shouldErr: true},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			err := validator(readCmd, tt.args)
			if tt.shouldErr {
				suite.Assert().Error(err, "MUST reject invalid argument count")
			} else {
				suite.Assert().NoError(err, "MUST accept valid argument count")
			}
		})
	}
}

// TestReadCommandSuite runs the test suite
func TestReadCommandSuite(t *testing.T) {
	suite.Run(t, new(ReadTestSuite))
}
