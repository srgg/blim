//go:build test

package main

import (
	"io"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/devicefactory"
	"github.com/srg/blim/internal/testutils"
	"github.com/srg/blim/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// ScanTestSuite provides testify/suite for proper test isolation
type ScanTestSuite struct {
	CommandTestSuite
	originalDeviceFactory func() (device.Scanner, error)
	originalFlags         struct {
		scanDuration    time.Duration
		scanFormat      string
		scanVerbose     bool
		scanServices    []string
		scanAllowList   []string
		scanBlockList   []string
		scanNoDuplicate bool
		scanWatch       bool
	}
}

// SetupSuite runs once before all device_test in the suite
func (suite *ScanTestSuite) SetupSuite() {
	// Save original flag values
	suite.originalFlags.scanDuration = scanDuration
	suite.originalFlags.scanFormat = scanFormat
	suite.originalFlags.scanServices = scanServices
	suite.originalFlags.scanAllowList = scanAllowList
	suite.originalFlags.scanBlockList = scanBlockList
	suite.originalFlags.scanNoDuplicate = scanNoDuplicate
	suite.originalFlags.scanWatch = scanWatch

	suite.CommandTestSuite.SetupSuite()
}

// TearDownSuite runs once after all device_test in the suite
func (suite *ScanTestSuite) TearDownSuite() {
	// Restore original factories and flag values
	devicefactory.DeviceFactory = suite.originalDeviceFactory
	scanDuration = suite.originalFlags.scanDuration
	scanFormat = suite.originalFlags.scanFormat
	scanServices = suite.originalFlags.scanServices
	scanAllowList = suite.originalFlags.scanAllowList
	scanBlockList = suite.originalFlags.scanBlockList
	scanNoDuplicate = suite.originalFlags.scanNoDuplicate
	scanWatch = suite.originalFlags.scanWatch
}

// SetupTest runs before each test in the suite
func (suite *ScanTestSuite) SetupTest() {
	suite.CommandTestSuite.SetupTest()

	// Reset flags before each test for proper isolation
	resetScanFlags()

	// Reset the scanCmd and re-initialize flags to ensure a clean state for each test
	// This prevents command state pollution between device_test
	scanCmd.ResetFlags()

	// Re-add all the flags with their default values
	scanCmd.Flags().DurationVarP(&scanDuration, "duration", "d", 10*time.Second, "Scan duration (0 for indefinite)")
	scanCmd.Flags().StringVarP(&scanFormat, "format", "f", "table", "Output format (table, json)")
	scanCmd.Flags().StringSliceVarP(&scanServices, "services", "s", nil, "Filter by service UUIDs")
	scanCmd.Flags().StringSliceVar(&scanAllowList, "allow", nil, "Only show devices with these addresses")
	scanCmd.Flags().StringSliceVar(&scanBlockList, "block", nil, "Hide devices with these addresses")
	scanCmd.Flags().BoolVar(&scanNoDuplicate, "no-duplicates", true, "Filter duplicate advertisements")
	scanCmd.Flags().BoolVarP(&scanWatch, "watch", "w", false, "Continuously scan and update results")
}

func (suite *ScanTestSuite) TestScanCmd_Help() {
	// GOAL: Verify scan command displays help text with all flags
	//
	// TEST SCENARIO: Execute scan --help → returns success → output contains description and flag documentation

	cmd := &cobra.Command{}
	cmd.AddCommand(scanCmd)

	output, err := suite.ExecuteCommand(cmd, "scan", "--help")
	suite.Require().NoError(err, "help command MUST succeed")

	suite.Assert().Contains(output, "Scan for and display Bluetooth Low Energy devices", "help MUST contain command description")
	suite.Assert().Contains(output, "--duration", "help MUST document --duration flag")
	suite.Assert().Contains(output, "--format", "help MUST document --format flag")
	suite.CommandTestSuite.TearDownTest()
}

func (suite *ScanTestSuite) TestScanCmd_InvalidFormat() {
	// GOAL: Verify scan command rejects invalid format values
	//
	// TEST SCENARIO: Execute scan with invalid format → returns error → error message lists valid formats

	resetScanFlags()

	cmd := &cobra.Command{}
	cmd.AddCommand(scanCmd)

	_, err := suite.ExecuteCommand(cmd, "scan", "--format=invalid")

	suite.Require().Error(err, "invalid format MUST return error")
	suite.Assert().Contains(err.Error(), "invalid format 'invalid': must be one of [table json]", "error MUST list valid formats")
}

func (suite *ScanTestSuite) TestScanCmd_Flags() {
	// GOAL: Verify scan command parses all flags correctly
	//
	// TEST SCENARIO: Execute scan with various flags → parsing succeeds → flag values set correctly

	tests := []struct {
		name     string
		args     []string
		expected map[string]interface{}
	}{
		{
			name: "default flags",
			args: []string{"scan"},
			expected: map[string]interface{}{
				"duration":      10 * time.Second,
				"format":        "table",
				"verbose":       false,
				"no-duplicates": true,
				"watch":         false,
			},
		},
		{
			name: "custom duration",
			args: []string{"scan", "--duration=30s"},
			expected: map[string]interface{}{
				"duration": 30 * time.Second,
			},
		},
		{
			name: "json format",
			args: []string{"scan", "--format=json"},
			expected: map[string]interface{}{
				"format": "json",
			},
		},
		{
			name: "verbose mode",
			args: []string{"scan", "--verbose"},
			expected: map[string]interface{}{
				"verbose": true,
			},
		},
		{
			name: "service filter",
			args: []string{"scan", "--services=180F,180A"},
			expected: map[string]interface{}{
				"services": []string{"180F", "180A"},
			},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			resetScanFlags()

			cmd := &cobra.Command{}
			cmd.AddCommand(scanCmd)

			cmd.SetArgs(tt.args)
			_ = cmd.Execute() // Error expected in test environment, but flags are still parsed

			for key, expected := range tt.expected {
				switch key {
				case "duration":
					suite.Assert().Equal(expected, scanDuration, "duration flag MUST be parsed correctly")
				case "format":
					suite.Assert().Equal(expected, scanFormat, "format flag MUST be parsed correctly")
				case "no-duplicates":
					suite.Assert().Equal(expected, scanNoDuplicate, "no-duplicates flag MUST be parsed correctly")
				case "watch":
					suite.Assert().Equal(expected, scanWatch, "watch flag MUST be parsed correctly")
				case "services":
					suite.Assert().Equal(expected, scanServices, "services flag MUST be parsed correctly")
				}
			}
		})
	}
}

// TestScanCmd_WatchMode device_test watch mode starts and runs indefinitely
func (suite *ScanTestSuite) TestScanCmd_WatchMode() {
	// GOAL: Verify watch mode starts and runs indefinitely (doesn't exit on its own)
	//
	// TEST SCENARIO: Execute scan --watch → still running after 3s → watch flag set correctly

	cmd := &cobra.Command{}
	cmd.AddCommand(scanCmd)

	done := make(chan error)

	go func() {
		_, err := suite.ExecuteCommand(cmd, "scan", "--watch")
		done <- err
	}()

	select {
	case <-done:
		suite.Fail("watch mode MUST NOT exit without interrupt")
	case <-time.After(3 * time.Second):
		// Expected - watch mode still running after 3 seconds
		suite.Assert().True(scanWatch, "watch flag MUST be set")
	}
}

func TestDisplayDevicesTable(t *testing.T) {
	// GOAL: Verify displayDevicesTable outputs without errors
	//
	// TEST SCENARIO: Display table with multiple devices → completes without error

	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)

	device1 := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "Test Device 1",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -45,
			"manufacturerData": null,
			"serviceData": null,
			"services": ["180F", "180A"],
			"txPower": 0,
			"connectable": true
		}`).BuildDevice(logger)

	device2 := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "",
			"address": "11:22:33:44:55:66",
			"rssi": -70,
			"manufacturerData": null,
			"serviceData": null,
			"services": null,
			"txPower": 0,
			"connectable": true
		}`).BuildDevice(logger)

	devices := []scanner.DeviceEntry{
		{Device: device1, LastSeen: time.Now()},
		{Device: device2, LastSeen: time.Now()},
	}

	err := displayDevicesTable(io.Discard, devices)
	assert.NoError(t, err, "displayDevicesTable MUST NOT return error")
}

func TestDisplayDevicesJSON(t *testing.T) {
	// GOAL: Verify device properties are accessible for JSON serialization
	//
	// TEST SCENARIO: Create device from JSON → access properties → values match input

	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)

	dev := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "Test Device",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -45,
			"services": ["180F", "180A"],
			"connectable": true,
			"manufacturerData": null,
			"serviceData": null,
			"txPower": 0
		}`).BuildDevice(logger)

	assert.Equal(t, "AA:BB:CC:DD:EE:FF", dev.ID(), "device ID MUST match")
	assert.Equal(t, "Test Device", dev.Name(), "device name MUST match")
	assert.Equal(t, -45, dev.RSSI(), "device RSSI MUST match")
}

func TestDevice_DisplayName_Integration(t *testing.T) {
	// GOAL: Verify device Name() returns correct display name
	//
	// TEST SCENARIO: Create devices with various names → Name() returns name or address fallback

	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)

	tests := []struct {
		name      string
		localName string
		address   string
		expected  string
	}{
		{
			name:      "returns device name when available",
			localName: "My BLE Device",
			address:   "AA:BB:CC:DD:EE:FF",
			expected:  "My BLE Device",
		},
		{
			name:      "returns address when name is empty",
			localName: "",
			address:   "11:22:33:44:55:66",
			expected:  "11:22:33:44:55:66",
		},
		{
			name:      "handles long device names",
			localName: "Very Long Device Name That Exceeds Limit",
			address:   "AA:BB:CC:DD:EE:FF",
			expected:  "Very Long Device Name That Exceeds Limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": "%s",
				"address": "%s",
				"rssi": -50,
				"connectable": true,
				"manufacturerData": null,
				"serviceData": null,
				"services": null,
				"txPower": 0
			}`, tt.localName, tt.address).BuildDevice(logger)

			result := dev.Name()
			assert.Equal(t, tt.expected, result, "Name() MUST return expected value")
		})
	}
}

func TestClearScreen(t *testing.T) {
	// GOAL: Verify clearScreen executes without panicking
	//
	// TEST SCENARIO: Call clearScreen() → completes without panic

	assert.NotPanics(t, func() {
		clearScreen(io.Discard)
	}, "clearScreen MUST NOT panic")
}

// Helper functions for testing

func resetScanFlags() {
	scanDuration = 10 * time.Second
	scanFormat = "table"
	scanServices = nil
	scanAllowList = nil
	scanBlockList = nil
	scanNoDuplicate = true
	scanWatch = false
}

// TestScanCommandSuite runs the test suite
func TestScanCommandSuite(t *testing.T) {
	suite.Run(t, new(ScanTestSuite))
}
