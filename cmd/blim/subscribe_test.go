//go:build test

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// Test constants for mock BLE device configuration
const (
	// testCustomCharUUID is a full 128-bit characteristic UUID (prefix "abcdef01" used in output)
	testCustomCharUUID = "abcdef01-1234-5678-1234-567812345678"
)

// SubscribeTestSuite device_test subscribe command with mock BLE peripheral
type SubscribeTestSuite struct {
	CommandTestSuite
	originalFlags struct {
		subscribeServiceUUID string
		subscribeCharUUIDs   string
		subscribeHex         bool
		subscribeTimeout     time.Duration
		subscribeMode        string
		subscribeRate        time.Duration
	}
}

// SetupSuite runs once before all device_test in the suite
func (suite *SubscribeTestSuite) SetupSuite() {
	suite.CommandTestSuite.SetupSuite()

	// Save original flag values
	suite.originalFlags.subscribeServiceUUID = subscribeServiceUUID
	suite.originalFlags.subscribeCharUUIDs = subscribeCharUUIDs
	suite.originalFlags.subscribeHex = subscribeHex
	suite.originalFlags.subscribeTimeout = subscribeTimeout
	suite.originalFlags.subscribeMode = subscribeMode
	suite.originalFlags.subscribeRate = subscribeRate
}

// TearDownSuite runs once after all device_test in the suite
func (suite *SubscribeTestSuite) TearDownSuite() {
	// Restore original flag values
	subscribeServiceUUID = suite.originalFlags.subscribeServiceUUID
	subscribeCharUUIDs = suite.originalFlags.subscribeCharUUIDs
	subscribeHex = suite.originalFlags.subscribeHex
	subscribeTimeout = suite.originalFlags.subscribeTimeout
	subscribeMode = suite.originalFlags.subscribeMode
	subscribeRate = suite.originalFlags.subscribeRate
}

// SetupTest runs before each test in the suite
func (suite *SubscribeTestSuite) SetupTest() {
	// Create a peripheral with notifiable characteristics, including a 128-bit UUID service.
	// Note: Custom UUIDs in fixture must match testCustomServiceUUID and testCustomCharUUID constants.
	// Don't call Build() here - let the parent SetupTest() set up the DeviceFactory
	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			FromJSON(testutils.NotifiablePeripheral)
	})

	// Reset flags to defaults
	subscribeServiceUUID = ""
	subscribeCharUUIDs = ""
	subscribeHex = false
	subscribeTimeout = 5 * time.Second
	subscribeMode = "live"
	subscribeRate = 1 * time.Second

	suite.CommandTestSuite.SetupTest()
}

func (suite *SubscribeTestSuite) TestParseStreamMode() {
	// GOAL: Verify stream mode parsing for valid and invalid inputs
	//
	// TEST SCENARIO: Parse mode strings → valid returns correct mode, invalid returns error

	tests := []struct {
		name      string
		input     string
		expected  device.StreamMode
		expectErr bool
	}{
		// Valid: live mode variants
		{name: "live lowercase", input: "live", expected: device.StreamEveryUpdate},
		{name: "live uppercase", input: "LIVE", expected: device.StreamEveryUpdate},
		{name: "live mixed case", input: "Live", expected: device.StreamEveryUpdate},
		{name: "instant alias", input: "instant", expected: device.StreamEveryUpdate},
		{name: "every alias", input: "every", expected: device.StreamEveryUpdate},

		// Valid: batched mode variants
		{name: "batched lowercase", input: "batched", expected: device.StreamBatched},
		{name: "batched uppercase", input: "BATCHED", expected: device.StreamBatched},
		{name: "batch alias", input: "batch", expected: device.StreamBatched},

		// Valid: latest mode variants
		{name: "latest lowercase", input: "latest", expected: device.StreamAggregated},
		{name: "latest uppercase", input: "LATEST", expected: device.StreamAggregated},
		{name: "aggregated alias", input: "aggregated", expected: device.StreamAggregated},

		// Invalid modes
		{name: "empty string", input: "", expectErr: true},
		{name: "unknown mode", input: "stream", expectErr: true},
		{name: "typo", input: "liev", expectErr: true},
		{name: "numeric", input: "123", expectErr: true},
		{name: "special chars", input: "live!", expectErr: true},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			result, err := parseStreamMode(tt.input)
			if tt.expectErr {
				suite.Assert().Error(err, "MUST fail on invalid mode string")
				suite.Assert().Equal(device.StreamMode(0), result, "result MUST be zero value on error")
				suite.Assert().Contains(err.Error(), "invalid mode", "error MUST indicate invalid mode")
			} else {
				suite.Assert().NoError(err, "MUST parse valid mode string")
				suite.Assert().Equal(tt.expected, result, "StreamMode MUST match expected")
			}
		})
	}
}

func (suite *SubscribeTestSuite) TestSubscribeCmd() {
	// GOAL: Verify subscribe command definition, flags, and argument validation
	//
	// TEST SCENARIO: Check command structure → flags with defaults → argument validation

	suite.Run("command definition", func() {
		suite.Assert().NotNil(subscribeCmd, "subscribe command MUST be defined")
		suite.Assert().Equal("subscribe <device-address> [uuid]", subscribeCmd.Use, "command usage MUST match expected format")
	})

	suite.Run("flags", func() {
		flags := []struct {
			name         string
			defaultValue string
			descContains []string
		}{
			{name: "service", defaultValue: "", descContains: []string{"Service UUID", "optional"}},
			{name: "char", defaultValue: "", descContains: []string{"Characteristic UUID", "comma-separated"}},
			{name: "hex", defaultValue: "false", descContains: []string{"hex string"}},
			{name: "timeout", defaultValue: "30s", descContains: []string{"Connection timeout"}},
			{name: "mode", defaultValue: "live", descContains: []string{"Stream mode", "live", "batched", "latest"}},
			{name: "rate", defaultValue: "1s", descContains: []string{"Rate limit", "interval"}},
		}

		for _, f := range flags {
			suite.Run(f.name, func() {
				flag := subscribeCmd.Flags().Lookup(f.name)
				suite.Require().NotNil(flag, "flag MUST exist")
				suite.Assert().Equal(f.defaultValue, flag.DefValue, "default value MUST match")
				for _, desc := range f.descContains {
					suite.Assert().Contains(flag.Usage, desc, "flag usage MUST contain %q", desc)
				}
			})
		}
	})

	suite.Run("args validation", func() {
		validator := subscribeCmd.Args
		suite.Require().NotNil(validator, "args validator MUST be defined")

		tests := []struct {
			name      string
			args      []string
			shouldErr bool
		}{
			{name: "address only", args: []string{"AA:BB:CC:DD:EE:FF"}, shouldErr: false},
			{name: "address and UUID", args: []string{"AA:BB:CC:DD:EE:FF", "2a37"}, shouldErr: false},
			{name: "address and multiple UUIDs", args: []string{"AA:BB:CC:DD:EE:FF", "2a37,2a38"}, shouldErr: false},
			{name: "no arguments", args: []string{}, shouldErr: true},
			{name: "too many arguments", args: []string{"AA:BB:CC:DD:EE:FF", "2a37", "extra"}, shouldErr: true},
		}

		for _, tt := range tests {
			suite.Run(tt.name, func() {
				err := validator(subscribeCmd, tt.args)
				if tt.shouldErr {
					suite.Assert().Error(err, "MUST reject invalid argument count")
				} else {
					suite.Assert().NoError(err, "MUST accept valid argument count")
				}
			})
		}
	})
}

func (suite *SubscribeTestSuite) TestOutputSubscribeRecord() {
	// GOAL: Verify outputSubscribeRecord correctly transforms device.Record to stdout output
	//
	// TEST SCENARIO: Create Record → call outputSubscribeRecord → verify stdout format

	tests := []struct {
		name           string
		record         *device.Record
		multiChar      bool
		hexMode        bool
		expectedOutput string
	}{
		{
			name: "single char hex output",
			record: &device.Record{
				Values: map[string][]byte{"2a37": {0xAB, 0xCD}},
			},
			multiChar:      false,
			hexMode:        true,
			expectedOutput: "abcd\n",
		},
		{
			name: "single char raw output",
			record: &device.Record{
				Values: map[string][]byte{"2a37": []byte("Hello")},
			},
			multiChar:      false,
			hexMode:        false,
			expectedOutput: "Hello\n",
		},
		{
			name: "multi char with UUID prefix hex",
			record: &device.Record{
				Values: map[string][]byte{
					"2a37": {0x01},
					"2a38": {0x02},
				},
			},
			multiChar:      true,
			hexMode:        true,
			expectedOutput: "2a37: 01\n2a38: 02\n",
		},
		{
			name: "multi char with UUID prefix raw",
			record: &device.Record{
				Values: map[string][]byte{
					"2a37": []byte("A"),
					"2a38": []byte("B"),
				},
			},
			multiChar:      true,
			hexMode:        false,
			expectedOutput: "2a37: A\n2a38: B\n",
		},
		{
			name: "empty data hex",
			record: &device.Record{
				Values: map[string][]byte{"2a37": {}},
			},
			multiChar:      false,
			hexMode:        true,
			expectedOutput: "\n",
		},
		{
			name: "long UUID truncated in prefix",
			record: &device.Record{
				Values: map[string][]byte{
					testCustomCharUUID: {0xFF},
					"2a37":             {0xAA},
				},
			},
			multiChar:      true,
			hexMode:        true,
			expectedOutput: "2a37: aa\nabcdef01: ff\n",
		},
		{
			name: "batch mode single char",
			record: &device.Record{
				BatchValues: map[string][][]byte{
					"2a37": {{0x01}, {0x02}, {0x03}},
				},
			},
			multiChar:      false,
			hexMode:        true,
			expectedOutput: "01\n02\n03\n",
		},
		{
			name: "batch mode multi char",
			record: &device.Record{
				BatchValues: map[string][][]byte{
					"2a37": {{0x01}, {0x02}},
					"2a38": {{0xAA}},
				},
			},
			multiChar:      true,
			hexMode:        true,
			expectedOutput: "2a37: 01\n2a37: 02\n2a38: aa\n",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			oldHex := subscribeHex
			subscribeHex = tt.hexMode
			defer func() { subscribeHex = oldHex }()

			var buf bytes.Buffer
			outputSubscribeRecord(&buf, tt.record, tt.multiChar)
			output := buf.String()

			suite.Assert().Equal(tt.expectedOutput, output, "output MUST match expected format")
		})
	}
}

// TestSubscribeCommandSuite runs the test suite
func TestSubscribeCommandSuite(t *testing.T) {
	suite.Run(t, new(SubscribeTestSuite))
}
