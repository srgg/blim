//go:build test

package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/lua"
)

const (
	// scriptOutputTickInterval is the interval for polling script output in tests
	scriptOutputTickInterval = 50 * time.Millisecond

	// bridgeStartupWait is the delay to allow bridge initialization to complete in tests
	bridgeStartupWait = 50 * time.Millisecond
)

// BridgeSuite provides test infrastructure for Bridge tests.
//
// Design: Embeds internal.LuaApiSuite to reuse all test infrastructure (YAML parsing,
// BLE simulation, peripheral mocks, step execution) while adding Bridge-specific features:
//   - Integrates Bridge's LuaAPI into parent suite lifecycle for automatic output validation
//   - Bridge lifecycle management (activeBridge tracking for cleanup)
//
// All validation logic (stdout, stderr) is inherited from the parent suite.
//
// Thread Safety: activeBridge field does not require synchronization because testify/suite
// guarantees single-threaded test execution (SetupTest, test method, TearDownTest run sequentially).
type BridgeSuite struct {
	lua.LuaApiSuite

	originalExecutor lua.ScriptExecutor
}

// SetupTest initializes the test environment and sets the executor for polymorphic dispatch
func (suite *BridgeSuite) SetupTest() {
	suite.LuaApiSuite.SetupTest()

	// Set executor to self for polymorphic ExecuteScriptWithCallbacks dispatch
	// This is set once here and persists across the subtests
	suite.originalExecutor = suite.Executor
	suite.Executor = suite
}

// TearDownTest ensures a bridge is stopped before the parent's teardown to prevent race conditions.
// This prevents the Lua state from being closed while BLE callbacks are still active.
func (suite *BridgeSuite) TearDownTest() {
	if suite.originalExecutor != nil {
		suite.Executor = suite.originalExecutor
		suite.originalExecutor = nil
	}

	// Call parent teardown to close Lua state and other resources
	suite.LuaApiSuite.TearDownTest()
}

// RunBridgeTestCasesFromYAML parses YAML and executes Bridge test cases
func (suite *BridgeSuite) RunBridgeTestCasesFromYAML(yamlContent string) {
	suite.RunTestCasesFromYAML(yamlContent)
}

// ExecuteScriptWithCallbacks overrides the parent's template method to use Bridge's LuaAPI.
// Manages full bridge lifecycle: start bridge, execute callbacks, stop bridge.
// Provides real PTY slave operations via ptySlaveWrite and ptySlaveRead functions.
func (suite *BridgeSuite) ExecuteScriptWithCallbacks(
	script string,
	before func(luaApi *lua.LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
	after func(luaApi *lua.LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
) error {
	bridgeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get address from parents' device
	address := suite.LuaApi.GetDevice().Address()

	// Re-build subscribe options from peripheral configuration
	var subscribeOptions []device.SubscribeOptions
	if suite.PeripheralBuilder != nil {
		for _, svc := range suite.PeripheralBuilder.GetServices() {
			var characteristics []string
			for _, char := range svc.Characteristics {
				characteristics = append(characteristics, char.UUID)
			}
			subscribeOptions = append(subscribeOptions, device.SubscribeOptions{
				Service:         svc.UUID,
				Characteristics: characteristics,
			})
		}
	}

	var scriptErr error

	bridgeCallback := func(b Bridge) (error, error) {
		bridgeLuaApi := b.GetLuaAPI()

		// Set bridge info in Lua API (Bridge interface has GetTTYName/GetSymlinkPath)
		bridgeLuaApi.SetBridge(b)

		suite.Logger.WithField("lua_api_ptr", fmt.Sprintf("%p", bridgeLuaApi)).Debug("BridgeSuite.ExecuteScriptWithCallbacks: SetBridge called")

		// Open PTY slave for test operations in non-blocking mode
		ptySlavePath := b.GetTTYName()
		ptySlave, err := os.OpenFile(ptySlavePath, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to open PTY slave: %w", err)
		}
		defer ptySlave.Close()

		// Create a real ptySlaveWrite function that writes to a PTY slave
		ptySlaveWrite := func(data []byte) error {
			_, err := ptySlave.Write(data)
			return err
		}

		// Create a real ptySlaveRead function that reads from the PTY slave (non-blocking)
		ptySlaveRead := func() ([]byte, error) {
			buffer := make([]byte, 4096)
			n, err := ptySlave.Read(buffer)
			if err != nil {
				// Non-blocking read with no data available returns EAGAIN/EWOULDBLOCK - not an error
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
					return []byte{}, nil
				}
				return nil, err
			}
			return buffer[:n], nil
		}

		// Setup: output collector, connection
		before(bridgeLuaApi, ptySlaveWrite, ptySlaveRead)

		// SetTemplateData sets template variables for text assertion
		// Provides bridge-specific data like PTY path and device address
		data := make(map[string]interface{})

		data["TTY"] = b.GetTTYName()
		data["TTYSymlink"] = b.GetTTYSymlink()
		data["DeviceAddress"] = b.GetLuaAPI().GetDevice().Address()

		suite.SetTemplateData(data)

		// Execute script
		err = lua.ExecuteDeviceScriptWithOutput(
			bridgeCtx,
			nil,
			bridgeLuaApi,
			suite.Logger,
			script,
			nil, // no args
			nil, // stdout - collector handles
			nil, // stderr - collector handles
			scriptOutputTickInterval,
			0,   // use LuaAPI defaults for characteristic read timeout
			0,   // use LuaAPI defaults for characteristic write timeout
			nil, // no script options for test scripts
		)
		scriptErr = err

		// Execute test steps and validate (blocks until complete)
		after(bridgeLuaApi, ptySlaveWrite, ptySlaveRead)

		return nil, nil
	}

	// Run bridge synchronously - blocks until bridgeCallback returns
	_, bridgeErr := RunDeviceBridge(
		bridgeCtx,
		&BridgeOptions{
			BleAddress:          address,
			BleConnectTimeout:   5 * time.Second,
			BleSubscribeOptions: subscribeOptions,
			Logger:              suite.Logger,
		},
		nil,
		bridgeCallback,
	)

	// Return script error if present, otherwise bridge error
	if scriptErr != nil {
		return scriptErr
	}
	return bridgeErr
}
