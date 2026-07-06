//go:build test

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
	"golang.org/x/term"
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

// TestBridgeRestoresTerminalOnExit verifies that the bridge restores the terminal on exit even when
// a Lua script left it in raw mode and cancellation aborted the script before its own cleanup ran
// (issue #4). Black-box: it drives the real runBridgeCtx over a fake-tty stdin and inspects fd 0.
func (suite *BridgeCmdTestSuite) TestBridgeRestoresTerminalOnExit() {
	// GOAL: runBridge restores the terminal to its pre-run (cooked) mode on exit, independent of the
	//       script's own disable_raw — so Ctrl+C never leaves the user's shell stuck in raw mode.
	//
	// TEST SCENARIO: fake-tty stdin → script enables raw and never disables it → cancel mid-run →
	//                runBridgeCtx returns → fd 0 is back to its original mode

	suite.GivenStdinPTY() // fd 0 becomes a real terminal (visible to Go and LuaJIT FFI)

	r := suite.Require()
	r.True(term.IsTerminal(0), "precondition: stdin must be a terminal")
	before, err := term.GetState(0)
	r.NoError(err, "snapshotting the original terminal state must succeed")

	// Script switches the terminal to raw and loops. It never calls disable_raw, so ONLY Go's
	// terminal restore can bring it back.
	scriptFile := filepath.Join(suite.T().TempDir(), "raw.lua")
	r.NoError(os.WriteFile(scriptFile, []byte(`
		assert(blim.term.enable_raw(), "enable_raw must succeed on the fake tty")
		while true do blim.sleep(20) end
	`), 0o600))
	bridgeLuaScript = scriptFile

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runBridgeCtx(ctx, logger, TestDeviceAddress1) }()

	// Let the bridge connect and the script enter raw mode.
	time.Sleep(500 * time.Millisecond)
	raw, err := term.GetState(0)
	r.NoError(err)
	r.NotEqual(before, raw, "precondition: the script must have put the terminal into raw mode")

	// Cancel and wait for the run to unwind (the #4 sleep-raise makes the script abort, so
	// runBridgeCtx returns and its deferred restore fires).
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		suite.Fail("runBridgeCtx did not return within 5s of cancellation")
	}

	after, err := term.GetState(0)
	r.NoError(err)
	if !reflect.DeepEqual(before, after) {
		suite.Fail("terminal was not restored on exit",
			"the terminal is still in a different mode than before the bridge ran — the script left it raw and Go did not restore it")
	}
}

// TestBridgeCmdTestSuite runs the test suite
func TestBridgeCmdTestSuite(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel) // Reduce noise in test output

	bridgeSuite := &BridgeCmdTestSuite{}
	bridgeSuite.Logger = logger

	suite.Run(t, bridgeSuite)
}
