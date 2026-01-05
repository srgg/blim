//go:build test

package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// Test constants for mock BLE device configuration
const (
	// testCustomServiceUUID is a full 128-bit UUID for testing long UUID handling
	testCustomServiceUUID = "12345678-1234-5678-1234-567812345678"

	// testCustomCharUUID is a full 128-bit characteristic UUID (prefix "abcdef01" used in output)
	testCustomCharUUID = "abcdef01-1234-5678-1234-567812345678"
)

// SubscribeTestSuite tests subscribe command with mock BLE peripheral
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

// SetupSuite runs once before all tests in the suite
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

// TearDownSuite runs once after all tests in the suite
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
	// Create peripheral with notifiable characteristics including 128-bit UUID service.
	// Note: Custom UUIDs in fixture must match testCustomServiceUUID and testCustomCharUUID constants.
	suite.WithPeripheral().
		FromJSON(testutils.NotifiablePeripheral).
		Build()

	suite.CommandTestSuite.SetupTest()

	// Reset flags to defaults
	subscribeServiceUUID = ""
	subscribeCharUUIDs = ""
	subscribeHex = false
	subscribeTimeout = 5 * time.Second
	subscribeMode = "live"
	subscribeRate = 1 * time.Second
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

func (suite *SubscribeTestSuite) TestNotificationFlow() {
	// GOAL: Verify full notification lifecycle for various subscription configurations
	//
	// TEST SCENARIO: Connect → subscribe → inject notifications → verify output

	type notification struct {
		service string
		char    string
		data    []byte
	}

	tests := []struct {
		name            string
		subscribeOpts   []*device.SubscribeOptions
		hexMode         bool
		notifications   []notification
		expectedOutputs []string // use Contains check for each
	}{
		{
			name: "single char hex output",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
			},
			hexMode:         true,
			notifications:   []notification{{service: "180d", char: "2a37", data: []byte{0xAB, 0xCD}}},
			expectedOutputs: []string{"abcd\n"},
		},
		{
			name: "single char raw output",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
			},
			hexMode:         false,
			notifications:   []notification{{service: "180d", char: "2a37", data: []byte("Hello")}},
			expectedOutputs: []string{"Hello\n"},
		},
		{
			name: "multiple chars same service with prefix",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37", "2a38"}},
			},
			hexMode: true,
			notifications: []notification{
				{service: "180d", char: "2a37", data: []byte{0x01}},
				{service: "180d", char: "2a38", data: []byte{0x02}},
			},
			expectedOutputs: []string{"2a37: 01", "2a38: 02"},
		},
		{
			name: "cross-service subscription",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
				{Service: "180f", Characteristics: []string{"2a19"}},
			},
			hexMode: true,
			notifications: []notification{
				{service: "180d", char: "2a37", data: []byte{0xAA}},
				{service: "180f", char: "2a19", data: []byte{0xBB}},
			},
			expectedOutputs: []string{"2a37: aa", "2a19: bb"},
		},
		{
			name: "empty data",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
			},
			hexMode:         true,
			notifications:   []notification{{service: "180d", char: "2a37", data: []byte{}}},
			expectedOutputs: []string{"\n"},
		},
		{
			name: "multiple notifications same char",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
			},
			hexMode: true,
			notifications: []notification{
				{service: "180d", char: "2a37", data: []byte{0x01}},
				{service: "180d", char: "2a37", data: []byte{0x02}},
				{service: "180d", char: "2a37", data: []byte{0x03}},
			},
			expectedOutputs: []string{"01\n", "02\n", "03\n"},
		},
		{
			name: "long UUID truncated in prefix",
			subscribeOpts: []*device.SubscribeOptions{
				{Service: testCustomServiceUUID, Characteristics: []string{testCustomCharUUID}},
				{Service: "180d", Characteristics: []string{"2a37"}},
			},
			hexMode: true,
			notifications: []notification{
				{service: testCustomServiceUUID, char: testCustomCharUUID, data: []byte{0xFF}},
			},
			expectedOutputs: []string{"abcdef01: ff"},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			oldHex := subscribeHex
			subscribeHex = tt.hexMode
			defer func() { subscribeHex = oldHex }()

			// Connect to mock device using inherited helper
			dev, cleanup := suite.ConnectDevice("")
			defer cleanup()

			conn := dev.GetConnection()
			suite.Require().NotNil(conn, "connection MUST exist")

			// Determine if multi-char subscription
			totalChars := 0
			for _, opt := range tt.subscribeOpts {
				totalChars += len(opt.Characteristics)
			}
			multiChar := totalChars > 1

			var notificationCount atomic.Int32
			expectedCount := int32(len(tt.notifications))
			allReceived := make(chan struct{})

			// Capture stdout for entire notification flow (subscribe → simulate → wait)
			var subscribeErr, simErr error
			capturedOutput := suite.CaptureStdout(func() {
				_, subscribeErr = conn.Subscribe(
					tt.subscribeOpts,
					device.StreamEveryUpdate,
					0,
					func(record *device.Record) {
						outputSubscribeRecord(record, multiChar)
						if notificationCount.Add(1) >= expectedCount {
							close(allReceived)
						}
					},
				)
				if subscribeErr != nil {
					return
				}

				// Inject all notifications using simulator
				simulator := suite.NewPeripheralDataSimulator().AllowMultiValue()
				for _, n := range tt.notifications {
					simulator.WithService(n.service).WithCharacteristic(n.char, n.data)
				}
				_, simErr = simulator.SimulateFor(conn, false)
				if simErr != nil {
					return
				}

				// Wait for all callbacks
				select {
				case <-allReceived:
				case <-time.After(2 * time.Second):
					// Will be checked after CaptureStdout returns
				}
			})

			suite.Require().NoError(subscribeErr, "subscribe MUST succeed")
			suite.Require().NoError(simErr, "notification simulation MUST succeed")

			// Verify expected outputs
			for _, expected := range tt.expectedOutputs {
				suite.Assert().Contains(capturedOutput, expected, "output MUST contain %q", expected)
			}
		})
	}
}

// TestSubscribeCommandSuite runs the test suite
func TestSubscribeCommandSuite(t *testing.T) {
	suite.Run(t, new(SubscribeTestSuite))
}
