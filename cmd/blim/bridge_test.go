//go:build test

package main

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// BridgeCmdTestSuite device_test the bridge command via the runBridge function
type BridgeCmdTestSuite struct {
	CommandTestSuite
	originalFlags struct {
		serviceUUID    string
		connectTimeout time.Duration
		verbose        bool
		luaScript      string
	}
}

// SetupSuite saves original flags and sets up a mock peripheral
func (suite *BridgeCmdTestSuite) SetupSuite() {
	suite.CommandTestSuite.SetupSuite()

	// Save original flag values
	suite.originalFlags.serviceUUID = bridgeServiceUUID
	suite.originalFlags.connectTimeout = bridgeConnectTimeout
	suite.originalFlags.luaScript = bridgeLuaScript
}

// TearDownSuite restores original flags
func (suite *BridgeCmdTestSuite) TearDownSuite() {
	bridgeServiceUUID = suite.originalFlags.serviceUUID
	bridgeConnectTimeout = suite.originalFlags.connectTimeout
	bridgeLuaScript = suite.originalFlags.luaScript
}

// SetupTest initializes the test environment with a bridge-compatible peripheral
func (suite *BridgeCmdTestSuite) SetupTest() {
	// Create a peripheral with services that bridge.lua expects
	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			FromJSON(`{
			"services": [
				{
					"uuid": "1234",
					"characteristics": [
						{"uuid": "5678", "properties": "read,notify", "value": []}
					]
				},
				{
					"uuid": "180d",
					"characteristics": [
						{"uuid": "2a37", "properties": "read,notify", "value": []},
						{"uuid": "2a38", "properties": "read,notify", "value": []}
					]
				},
				{
					"uuid": "180f",
					"characteristics": [
						{"uuid": "2a19", "properties": "read,notify", "value": []}
					]
				},
				{
					"uuid": "6e400001-b5a3-f393-e0a9-e50e24dcca9e",
					"characteristics": [
						{"uuid": "6e400002-b5a3-f393-e0a9-e50e24dcca9e", "properties": "read,write,notify", "value": []},
						{"uuid": "6e400003-b5a3-f393-e0a9-e50e24dcca9e", "properties": "read,notify", "value": []}
					]
				}
			]
		}`)
	})

	suite.CommandTestSuite.SetupTest()

	// Reset flags to defaults
	bridgeServiceUUID = "6E400001-B5A3-F393-E0A9-E50E24DCCA9E"
	bridgeConnectTimeout = 30 * time.Second
	bridgeLuaScript = ""

	// Reset command flags
	bridgeCmd.ResetFlags()
	bridgeCmd.Flags().StringVar(&bridgeServiceUUID, "service", "6E400001-B5A3-F393-E0A9-E50E24DCCA9E", "BLE service UUID to bridge with")
	bridgeCmd.Flags().DurationVar(&bridgeConnectTimeout, "connect-timeout", 30*time.Second, "Connection timeout")
	bridgeCmd.Flags().StringVar(&bridgeLuaScript, "script", "", "Lua script file")
}

// TestBridgeCmdTestSuite runs the test suite
func TestBridgeCmdTestSuite(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel) // Reduce noise in test output

	bridgeSuite := &BridgeCmdTestSuite{}
	bridgeSuite.Logger = logger

	suite.Run(t, bridgeSuite)
}
