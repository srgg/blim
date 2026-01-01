//go:build test

package lua

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/srg/blim/internal/device"
	goble "github.com/srg/blim/internal/device/go-ble"
	"github.com/srg/blim/internal/devicefactory"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultStepWaitDuration is the default time to wait after executing a test step before capturing output.
	// This allows asynchronous BLE callbacks and aggregated subscriptions time to process and deliver data.
	DefaultStepWaitDuration = 200 * time.Millisecond
)

// TestCase represents a complete BLE test case, including subscription configuration and steps.
type TestCase struct {
	// Name of the test case
	Name string `json:"name" yaml:"name"`

	// Script is an optional standalone Lua script to execute (mutually exclusive with Subscription)
	// Supports file:// URLs (e.g., "file://examples/inspect.lua?format=json&verbose=true")
	// URL query parameters are passed to Lua via arg[] table
	Script string `json:"script,omitempty" yaml:"script,omitempty"`

	// Peripheral is an optional explicit peripheral service configuration
	// If provided, these services will be used to configure the mock peripheral
	// If not provided, peripheral services will be auto-populated from Subscription.Services
	Peripheral []testutils.ServiceConfig `json:"peripheral,omitempty" yaml:"peripheral,omitempty"`

	Subscription TestSubscriptionOptions `json:"subscription,omitempty" yaml:"subscription,omitempty"`

	// Steps is an ordered list of steps that occur in this test case
	Steps []TestStep `json:"steps" yaml:"steps"`

	// AllowMultiValue enables sending multiple values to the same characteristic (default: false)
	AllowMultiValue bool `json:"allow_multi_value,omitempty" yaml:"allow_multi_value,omitempty"`

	// ExpectErrorMessage is the expected error message substring (test expects error if non-empty)
	ExpectErrorMessage string `json:"expect_error_message,omitempty" yaml:"expect_error_message,omitempty"`

	// ExpectedJSONOutput is the expected JSON output from Lua callbacks
	ExpectedJSONOutput []map[string]interface{} `json:"expected_json_output,omitempty" yaml:"expected_json_output,omitempty"`

	// WaitAfter is the duration to wait after all steps complete before validating output (default: DefaultStepWaitDuration)
	WaitAfter time.Duration `json:"wait_after,omitempty" yaml:"wait_after,omitempty"`

	// Bridge-specific fields (optional - used only for Bridge tests):

	// ExpectedStdout is the expected stdout output (Bridge tests only - for PTY validation)
	ExpectedStdout string `json:"expected_stdout,omitempty" yaml:"expected_stdout,omitempty"`

	// ExpectedErrors is a list of expected error message substrings (Bridge tests only)
	ExpectedErrors []string `json:"expected_errors,omitempty" yaml:"expected_errors,omitempty"`

	// ExpectedPTYSlaveRead is the expected data read from PTY slave after all steps complete (Bridge tests only)
	// Written by Lua via pty_write, validated at TestCase level after all steps execute
	// Can be a string or byte array
	ExpectedPTYSlaveRead interface{} `json:"expected_pty_slave_read,omitempty" yaml:"expected_pty_slave_read,omitempty"`

	// Skip marks this test case as skipped with the provided reason.
	// When set, the test will be skipped and the skip reason will be logged.
	// Useful for tests that cannot be programmatically validated or have known issues.
	// Example: skip: "Cannot programmatically validate - manual verification required"
	Skip string `json:"skip,omitempty" yaml:"skip,omitempty"`
}

// TestSubscriptionOptions configures BLE characteristic subscription behavior and callbacks.
// Used in TestCase to define how characteristic notifications are monitored and processed.
type TestSubscriptionOptions struct {
	// Mode defines the subscription streaming mode (EveryUpdate, Batched, Aggregated)
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`

	// SubscriptionMaxRate defines the maximum rate for subscription updates
	MaxRate time.Duration `json:"max_rate,omitempty" yaml:"max_rate,omitempty"`

	// WaitAfter is the default duration to wait after each test step (used if a step doesn't specify its own wait_after)
	WaitAfter time.Duration `json:"wait_after,omitempty" yaml:"wait_after,omitempty"`

	// Subscriptions define which services/characteristics to subscribe to
	Services []device.SubscribeOptions `json:"services" yaml:"services"`

	// CallbackScript is optional custom Lua code for the callback function body
	// If empty, uses the default callback that prints JSON output
	// The code has access to 'record' parameter and 'call_count' variable
	CallbackScript string `json:"callback_script,omitempty" yaml:"callback_script,omitempty"`
}

// TestStep represents one point in time where one or more services'
// / characteristics are updated.
// If At is zero or omitted, the step is applied immediately when reached.
type TestStep struct {
	// At specifies the time relative to the start of the test case.
	At time.Duration `json:"at,omitempty" yaml:"at,omitempty"`

	// Services is a list of service updates to apply at this step.
	Services []ServiceValues `json:"services" yaml:"services"`

	// WaitAfter waits this duration after step execution before capturing output (default: DefaultStepWaitDuration if zero).
	WaitAfter time.Duration `json:"wait_after,omitempty" yaml:"wait_after,omitempty"`

	// ExpectedJSONOutput validates Lua output immediately after this step (optional, validated during step execution).
	ExpectedJSONOutput []map[string]interface{} `json:"expected_json_output,omitempty" yaml:"expected_json_output,omitempty"`

	// ExpectedErrors is a list of expected error message substrings to validate after this step (optional)
	ExpectedErrors []string `json:"expected_errors,omitempty" yaml:"expected_errors,omitempty"`

	// Lua is optional Lua code to execute during this step (Bridge tests)
	Lua string `json:"lua,omitempty" yaml:"lua,omitempty"`

	// PTYSlaveWrite contains data to write to PTY slave (for Lua to read via pty_read)
	// Can be a string or byte array
	PTYSlaveWrite interface{} `json:"pty_slave_write,omitempty" yaml:"pty_slave_write,omitempty"`

	// ExpectedPTYSlaveRead is the expected data read from PTY slave (written by Lua via pty_write)
	// Can be a string or byte array
	ExpectedPTYSlaveRead interface{} `json:"expected_pty_slave_read,omitempty" yaml:"expected_pty_slave_read,omitempty"`
}

// ServiceValues represents updates to all characteristics of a single service.
type ServiceValues struct {
	// Service is the UUID or name of the BLE service.
	Service string `json:"service" yaml:"service"`

	// Values is a list of characteristics and their data for this service.
	Values []CharacteristicValue `json:"values" yaml:"values"`
}

// CharacteristicValue represents the value of a single characteristic.
type CharacteristicValue struct {
	// Characteristic is the UUID or name of the characteristic.
	Characteristic string `json:"char" yaml:"char"`

	// Value is the raw byte value to be applied to the characteristic.
	Value []byte `json:"value" yaml:"value"`
}

// LuaErrorInfo captures detailed error information from Lua execution
type LuaErrorInfo struct {
	Message   string `json:"message"`             // Error message content
	Source    string `json:"source,omitempty"`    // Source of the error (e.g., "callback", "script")
	Line      int    `json:"line,omitempty"`      // Line number if available
	Timestamp string `json:"timestamp,omitempty"` // When the error occurred
}

// LuaSubscriptionCallbackData represents the JSON structure of Lua subscription callback output.
// Used for validation of subscription test results with support for errors, call counts, and BLE notification data.
type LuaSubscriptionCallbackData struct {
	CallCount int            `json:"call_count"`
	Errors    []LuaErrorInfo `json:"errors,omitempty"` // Collected stderr errors with details
	Record    struct {
		Values      map[string]interface{}   `json:"Values,omitempty"`
		BatchValues map[string][]interface{} `json:"BatchValues,omitempty"`
	} `json:"record"`
}

// ScriptExecutor defines the interface for executing Lua scripts with callbacks.
// This enables polymorphic behavior for different execution contexts (e.g., standard vs bridge mode).
// ptySlaveWrite and ptySlaveRead enable Bridge-specific PTY operations (both passed to both callbacks).
type ScriptExecutor interface {
	ExecuteScriptWithCallbacks(
		script string,
		before func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
		after func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
	) error
}

// LuaApiSuite provides test infrastructure for BLE API testing with Lua integration.
//
// It embeds MockBLEPeripheralSuite for peripheral simulation and manages a LuaAPI instance
// with an output collection for testing Lua script execution.
//
// # Overview
//
// Supports two types of Lua-based BLE tests:
//
// 1. **Subscription Tests**: Test BLE characteristic notifications with callbacks
// 2. **Script Tests**: Execute standalone Lua scripts with file:// URL support
//
// # Subscription Tests
//
// Subscription tests monitor BLE characteristic notifications and validate callback output.
// Supports three streaming modes: EveryUpdate, Batched, and Aggregated.
//
// Example YAML test case:
//
//	test_cases:
//	  - name: "Heart Rate Monitor Test"
//	    subscription:
//	      mode: EveryUpdate
//	      max_rate: 100ms
//	      services:
//	        - service: "180d"
//	          characteristics: ["2a37"]
//	    steps:
//	      - services:
//	          - service: "180d"
//	            values:
//	              - char: "2a37"
//	                value: [0x60]  # 96 bpm
//	    expected_json_output:
//	      - record:
//	          Values:
//	            "2a37": [0x60]
//
// # Script Tests
//
// Script tests execute standalone Lua scripts for device inspection or custom logic.
// Supports file:// URLs with query parameters passed via arg[] table.
//
// Example YAML test case:
//
//	test_cases:
//	  - name: "Inspect Device Test"
//	    script: "file://examples/inspect.lua?format=json&verbose=true"
//	    wait_after: 500ms
//	    expected_json_output:
//	      - device:
//	          id: "00:00:00:00:00:01"
//	          name: "Test Device"
//	        services:
//	          - uuid: "180d"
//	            characteristics:
//	              - uuid: "2a37"
//
// URL query parameters (format=json, verbose=true) are available in Lua via arg[] table:
//
//	local format = arg["format"]  -- "json"
//	local verbose = arg["verbose"] -- "true"
//
// # Peripheral Configuration and Descriptor Support
//
// Tests can configure mock BLE peripherals with services, characteristics, and descriptors.
// The Peripheral field in TestCase supports full descriptor configuration including error simulation.
//
// Example YAML with descriptor configuration:
//
//	test_cases:
//	  - name: "Test Descriptor Reading"
//	    peripheral:
//	      - service: "180d"
//	        characteristics:
//	          - uuid: "2a37"
//	            properties: "read,notify"
//	            descriptors:
//	              - uuid: "2902"  # Client Characteristic Configuration
//	                value: [0x01, 0x00]
//	              - uuid: "2901"  # User Description
//	                value: [0x48, 0x65, 0x61, 0x72, 0x74, 0x20, 0x52, 0x61, 0x74, 0x65]  # "Heart Rate"
//	    script: |
//	      local char = blim.characteristic("180d", "2a37")
//	      for i, desc in ipairs(char.descriptors) do
//	        print(desc.uuid .. ": " .. desc.parsed_value.value)
//	      end
//
// # Descriptor Error Simulation
//
// The test infrastructure supports simulating descriptor read errors for testing error handling.
// Use the read_error field in descriptor configuration to specify error behavior:
//
//	descriptors:
//	  - uuid: "2902"
//	    read_error: "timeout"  # Simulates 10-second timeout (should be handled with DescriptorReadTimeout)
//	  - uuid: "2901"
//	    read_error: "permission denied"  # Returns permission error immediately
//	  - uuid: "2900"
//	    value: [0x01, 0x00]  # Normal descriptor with value (no error)
//
// The read_error field accepts:
//   - "timeout": Blocks for 10 seconds (panics if timeout not handled properly)
//   - Any other non-empty string: Returns an error immediately (e.g., "permission denied")
//   - Omitted or empty: Normal descriptor read returning the value
//
// These error behaviors map to the DescriptorReadBehavior enum in PeripheralDeviceBuilder:
//   - DescriptorReadTimeout (1): For timeout simulation
//   - DescriptorReadError (2): For immediate error returns
//   - Normal (0): For successful reads with value
//
// Example test case with error simulation:
//
//	test_cases:
//	  - name: "Error: Descriptor Read Timeout"
//	    peripheral:
//	      - service: "1234"
//	        characteristics:
//	          - uuid: "5678"
//	            descriptors:
//	              - uuid: "2902"
//	                read_error: "timeout"
//	    script: |
//	      local char = blim.characteristic("1234", "5678")
//	      local desc = char.descriptors[1]
//	      if desc.parsed_value.error then
//	        print("Error: " .. desc.parsed_value.error)
//	      end
//	    expected_stdout: |
//	      Error: timeout reading descriptor 0x2902
//
// # Error Handling Strategy
//
// This test infrastructure uses a deliberate error-handling strategy to distinguish between
// programmer errors and runtime failures:
//
// **Panics** are used for test suite misuse (programmer errors):
//   - Invalid test setup or configuration
//   - Contract violations in builder patterns (e.g., calling methods out of order)
//   - Missing required parameters
//   - Improper initialization
//
// Examples: WithCharacteristic called before WithService, nil builder parent.
//
// **Errors** are returned for runtime failures:
//   - External system failures (Lua execution, channel communication)
//   - Resource allocation failures
//   - Timeout conditions
//   - Unexpected data format issues
//
// Examples: Lua script execution errors, output collector failures, JSON parsing errors.
//
// This approach ensures that test suite bugs (programmer errors) are caught immediately
// during development with clear panic messages, while runtime issues are handled gracefully
// with proper error propagation.
type LuaApiSuite struct {
	testutils.MockBLEPeripheralSuite

	LuaApi           *LuaAPI
	luaOutputCapture *LuaOutputCollector
	Executor         ScriptExecutor
	templateData     map[string]interface{} // Template data for dynamic test assertions
}

// NewPeripheralDataSimulator creates a builder with automatic connection access via LuaApi.
// Overrides the embedded MockBLEPeripheralSuite method to provide connection provider.
func (suite *LuaApiSuite) NewPeripheralDataSimulator() *testutils.PeripheralDataSimulatorBuilder {
	return suite.MockBLEPeripheralSuite.NewPeripheralDataSimulator().
		WithConnectionProvider(func() device.Connection {
			return suite.LuaApi.GetDevice().GetConnection()
		})
}

// ExecuteScript loads and executes a Lua script for testing
func (suite *LuaApiSuite) ExecuteScript(script string) error {
	err := suite.LuaApi.LoadScript(script, "test")
	suite.NoError(err, "Should load script without errors")
	err = suite.LuaApi.ExecuteScript(context.Background(), "")
	return err
}

// SetupTest initializes the test environment with a mock BLE peripheral and Lua API instance.
// Creates default services (1234, 180D, 180F) if no custom peripheral is configured.
// Sets up output collector to capture Lua stdout/stderr for validation.
// Called automatically before each test case by the testing framework.
func (suite *LuaApiSuite) SetupTest() {
	// -- Set up a Mock device factory only if not already configured
	if suite.PeripheralBuilder == nil || len(suite.PeripheralBuilder.GetServices()) == 0 {
		suite.WithPeripheral().
			FromJSON(`{
				"services": [
					{
						"uuid": "1234",
						"characteristics": [
							{
								"uuid": "5678",
								"properties": "read,notify",
								"value": []
							},
							{
								"uuid": "ABCD",
								"properties": "write,write-without-response",
								"value": []
							}
						]
					},
					{
						"uuid": "180D",
						"characteristics": [
							{ "uuid": "2A37", "properties": "read,notify", "value": [] },
							{ "uuid": "2A38", "properties": "read,notify", "value": [] }
						]
					},
					{
						"uuid": "180F",
						"characteristics": [
							{ "uuid": "2A19", "properties": "read,notify", "value": [] }
						]
					}
				]
			}`).
			Build()
	}

	suite.MockBLEPeripheralSuite.SetupTest()

	// -- Create lua api
	suite.LuaApi = suite.createLuaApi()

	// Initialize executor to self for polymorphic ExecuteScriptWithCallbacks
	suite.Executor = suite

	// Clear template data for clean test state
	suite.templateData = nil

	// Create and start a Lua output collector with proper error handling
	if err := suite.setupOutputCollector(); err != nil {
		suite.T().Fatalf("Failed to setup output collector: %v", err)
	}
}

func (suite *LuaApiSuite) createLuaApi() *LuaAPI {
	// Create a BLE Device with mocked ble.Device
	dev := devicefactory.NewDevice("00:00:00:00:00:01", suite.Logger)

	// Set up mock connection with test services and characteristics
	// Use context with 10s timeout for safety, but don't cancel it immediately
	// The context lives until timeout expires or Disconnect() is called
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)

	// Define the services and characteristics needed for tests
	// Get services from the configured peripheral builder
	var subscribeOptions []device.SubscribeOptions
	if suite.PeripheralBuilder != nil && len(suite.PeripheralBuilder.GetServices()) > 0 {
		// Use the services configured in the peripheral builder
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
	} else {
		// Fallback to default services if no peripheral configured
		subscribeOptions = []device.SubscribeOptions{
			{
				Service:         "1234",
				Characteristics: []string{"5678"},
			},
			{
				Service:         "180d",
				Characteristics: []string{"2a37", "2a38"},
			},
			{
				Service:         "180f",
				Characteristics: []string{"2a19"},
			},
		}
	}

	opts := &device.ConnectOptions{
		ConnectTimeout:        5 * time.Second,
		DescriptorReadTimeout: 2 * time.Second, // Enable descriptor reading for tests
		Services:              subscribeOptions,
	}

	// Connect with mocked device factory (should succeed since we set up mocks in SetupSuite)
	err := dev.Connect(ctx, opts)
	suite.NoError(err, "Mock connection should succeed with mocked device factory")

	// Disable CoreBluetooth drain delay in tests - not needed for mock connections
	if bleConn, ok := dev.GetConnection().(*goble.BLEConnection); ok {
		bleConn.DrainDuration = 0
	}

	return NewBLEAPI2(dev, suite.Logger)
}

// setupOutputCollector creates and starts the Lua output collector with proper error handling
func (suite *LuaApiSuite) setupOutputCollector() error {
	lc, err := NewLuaOutputCollector(suite.LuaApi.OutputChannel(), 100, nil)
	if err != nil {
		return fmt.Errorf("creating lua output collector: %w", err)
	}

	if err := lc.Start(); err != nil {
		// Attempt cleanup on start failure - if stop also fails, log it but return the original error
		if stopErr := lc.Stop(); stopErr != nil {
			suite.T().Logf("Warning: failed to stop collector after start failure: %v", stopErr)
		}
		return fmt.Errorf("starting lua output collector: %w", err)
	}

	suite.luaOutputCapture = lc
	return nil
}

// TearDownTest cleans up test resources in proper order: device, output collector, Lua API, then peripheral.
// Called automatically after each test case by the testing framework.
// Logs warnings for cleanup errors but does not fail the test.
//
// CRITICAL ORDER: Disconnect device FIRST to stop subscriptions and prevent writes to closed channels,
// THEN stop output collector to drain remaining output, THEN close Lua API.
func (suite *LuaApiSuite) TearDownTest() {
	var errors []error

	// Step 1: Disconnect device FIRST to stop subscriptions and prevent writes to output channels
	if suite.LuaApi != nil {
		suite.Logger.WithField("lua_api_ptr", fmt.Sprintf("%p", suite.LuaApi)).Debug("TearDownTest: About to disconnect device")
		if dev := suite.LuaApi.GetDevice(); dev != nil && dev.IsConnected() {
			suite.Logger.WithFields(map[string]interface{}{
				"device_ptr":     fmt.Sprintf("%p", dev),
				"device_address": dev.Address(),
			}).Debug("TearDownTest: Disconnecting device...")
			if err := dev.Disconnect(); err != nil {
				errors = append(errors, fmt.Errorf("disconnecting device: %w", err))
			}
			suite.Logger.Debug("TearDownTest: Device disconnected")
		}
	}

	// Step 2: Stop output collector AFTER device disconnect to drain any final output
	if suite.luaOutputCapture != nil {
		if err := suite.luaOutputCapture.Stop(); err != nil {
			errors = append(errors, fmt.Errorf("stopping lua output collector: %w", err))
		}
		suite.luaOutputCapture = nil
	}

	// Step 3: Close Lua API
	if suite.LuaApi != nil {
		suite.Logger.WithField("lua_api_ptr", fmt.Sprintf("%p", suite.LuaApi)).Debug("TearDownTest: About to close Lua API")
		suite.LuaApi.Close()
		suite.Logger.Debug("TearDownTest: Lua API closed")
		suite.LuaApi = nil
	}

	// Step 4: Call parent teardown
	suite.MockBLEPeripheralSuite.TearDownTest()

	// Report any cleanup errors (but don't fail the test)
	if len(errors) > 0 {
		for _, err := range errors {
			suite.T().Logf("Cleanup warning: %v", err)
		}
	}
}

// SetTemplateData sets template data for dynamic test assertions.
// Used by subclasses like BridgeSuite to provide runtime values (PTY paths, device addresses)
// for template-based validation of test output.
func (suite *LuaApiSuite) SetTemplateData(data map[string]interface{}) {
	suite.templateData = data
}

// SetupSubTest reinitializes test resources for subtests.
// Cleans up existing output collector and Lua API, then creates fresh instances.
// Enables running multiple test cases in the same test function using suite.Run().
// Called automatically before each subtest by the testing framework.
func (suite *LuaApiSuite) SetupSubTest() {
	// Skip reinitialization for step subtests (Step_1, Step_2, etc.)
	// Steps share the same LuaApi and output collector as the parent test case
	testName := suite.T().Name()
	if strings.HasPrefix(testName, "Step_") || strings.Contains(testName, "/Step_") {
		return
	}

	// Clear template data for clean test case state
	suite.templateData = nil

	// Clean up existing resources in proper order (for test case subtests only)
	if suite.luaOutputCapture != nil {
		if err := suite.luaOutputCapture.Stop(); err != nil {
			suite.T().Fatalf("Failed to stop lua output collector during subtest setup: %v", err)
		}
		suite.luaOutputCapture = nil
	}

	if suite.LuaApi != nil {
		suite.LuaApi.Close()
		suite.LuaApi = nil
	}

	// Create a new resources
	suite.LuaApi = suite.createLuaApi()

	// Setup output collector with fail-fast error handling
	if err := suite.setupOutputCollector(); err != nil {
		suite.T().Fatalf("Failed to setup output collector in subtest: %v", err)
	}
}

// CreateSubscriptionJsonScript generates a Lua script that subscribes to BLE characteristics and processes notifications.
// Returns a complete Lua script string with JSON callback logic for use in tests.
//
// Error Handling: Panics on invalid test configuration (programmer errors):
//   - Empty service/characteristic UUIDs → test YAML is malformed
//   - Negative maxRate → test configuration bug
//
// Note: Invalid UUID *format* (like "not-a-uuid") passes through to BLE code for error testing.
// Only structural omissions (empty strings, missing arrays) cause panics.
func (suite *LuaApiSuite) CreateSubscriptionJsonScript(pattern string, maxRate time.Duration, callbackScript string, subscription ...device.SubscribeOptions) string {
	// Validate test configuration (panics on programmer errors)
	if maxRate < 0 {
		panic(fmt.Sprintf("CreateSubscriptionJsonScript: maxRate cannot be negative (got %v)", maxRate))
	}

	var services strings.Builder
	services.WriteString("{")
	for i, sub := range subscription {
		// Validate each subscription
		if sub.Service == "" {
			panic(fmt.Sprintf("CreateSubscriptionJsonScript: subscription[%d] has empty service UUID", i))
		}
		if len(sub.Characteristics) == 0 {
			panic(fmt.Sprintf("CreateSubscriptionJsonScript: subscription[%d] (service=%q) has no characteristics", i, sub.Service))
		}
		for j, char := range sub.Characteristics {
			if char == "" {
				panic(fmt.Sprintf("CreateSubscriptionJsonScript: subscription[%d] (service=%q) has empty characteristic at index %d", i, sub.Service, j))
			}
		}

		if i > 0 {
			services.WriteString(",")
		}
		// Build service entry with optional indicate flag
		indicateStr := ""
		if sub.Indicate {
			indicateStr = ",indicate=true"
		}
		if _, err := fmt.Fprintf(&services, `{service="%s",chars={"%s"}%s}`, sub.Service, strings.Join(sub.Characteristics, `","`), indicateStr); err != nil {
			panic(fmt.Sprintf("CreateSubscriptionJsonScript: failed to build services string for subscription[%d] (service=%q, chars=%v): %v", i, sub.Service, sub.Characteristics, err))
		}
	}
	services.WriteString("}")

	// Use custom callback if provided, otherwise use default JSON output callback
	var callbackBody string
	if callbackScript != "" {
		callbackBody = callbackScript
	} else {
		callbackBody = `
				call_count = call_count + 1

				local output = {
					call_count = call_count,
					record = record
				}

				-- print(json.encode{call_count = call_count, record = record})
				print(json.encode(output))`
	}

	return fmt.Sprintf(`
		local json = require("json")
		call_count = 0

		blim.subscribe{
			services = %s,
			Mode = "%s",
			MaxRate = %d,
			Callback = function(record)%s
			end
		}`, services.String(), pattern, maxRate.Milliseconds(), callbackBody)
}

// ensurePeripheralService ensures a service and its characteristics exist in the peripheral builder
func (suite *LuaApiSuite) ensurePeripheralService(serviceUUID string, characteristicUUIDs []string) {
	// Check if service already exists in the peripheral builder
	if suite.PeripheralBuilder == nil {
		return
	}

	services := suite.PeripheralBuilder.GetServices()
	serviceExists := false
	for _, svc := range services {
		if strings.EqualFold(svc.UUID, serviceUUID) {
			serviceExists = true
			break
		}
	}

	// Add missing service with all its characteristics
	if !serviceExists {
		svcBuilder := suite.PeripheralBuilder.WithService(serviceUUID)
		for _, charUUID := range characteristicUUIDs {
			// Use empty byte slice so char.read() returns empty string (no value in output)
			svcBuilder.WithCharacteristic(charUUID, "read,notify", []byte{})
		}
	}
}

// RunTestCasesFromFile loads YAML test case definitions from a file and executes them.
// The file path is relative to the project root (uses testutils.LoadScript for finding the root).
// Expects a "test_cases" array at the root level in the YAML file.
// Each test case runs as a separate subtest with full isolation.
func (suite *LuaApiSuite) RunTestCasesFromFile(filePath string) {
	content, err := testutils.LoadScript(filePath)
	suite.Require().NoError(err, "Failed to load test cases from file: %s", filePath)
	suite.RunTestCasesFromYAML(content)
}

// RunTestCasesFromYAML parses YAML test case definitions and executes them.
// Automatically dedents the YAML content to support inline test definitions.
// Expects a "test_cases" array at the root level.
// Each test case runs as a separate subtest with full isolation.
func (suite *LuaApiSuite) RunTestCasesFromYAML(yamlContent string) {
	// Dedent the YAML content
	yamlContent = dedent(yamlContent)

	var scenario struct {
		TestCases []TestCase `yaml:"test_cases"`
	}
	err := yaml.Unmarshal([]byte(yamlContent), &scenario)
	suite.Require().NoError(err, "Failed to parse YAML test cases")

	suite.RunTestCases(scenario.TestCases...)
}

func dedent(s string) string {
	const tabWidth = 4
	lines := strings.Split(s, "\n")

	// find min indent
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for _, ch := range line {
			if ch == ' ' {
				indent++
			} else if ch == '\t' {
				indent += tabWidth
			} else {
				break
			}
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
	}

	// strip min indent, normalize tabs → spaces
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		indent, j := 0, 0
		for j < len(line) && indent < minIndent {
			if line[j] == ' ' {
				indent++
			} else if line[j] == '\t' {
				indent += tabWidth
			} else {
				break
			}
			j++
		}
		out = append(out, strings.Repeat(" ", indent-minIndent)+strings.ReplaceAll(line[j:], "\t", strings.Repeat(" ", tabWidth)))
	}
	return strings.Join(out, "\n")
}

// parseFileURL parses a file:// URL and extracts the path and query parameters.
// Returns the file path and a map of query parameters.
// Example: "file://examples/inspect.lua?format=json&verbose=true" → ("examples/inspect.lua", {"format": "json", "verbose": "true"})
func parseFileURL(fileURL string) (string, map[string]string, error) {
	// Parse the URL
	u, err := url.Parse(fileURL)
	if err != nil {
		return "", nil, fmt.Errorf("invalid file URL: %w", err)
	}

	// Extract the path (everything after file://)
	filePath := strings.TrimPrefix(fileURL, "file://")
	if idx := strings.Index(filePath, "?"); idx != -1 {
		filePath = filePath[:idx]
	}

	// Extract query parameters
	params := make(map[string]string)
	for key, values := range u.Query() {
		if len(values) > 0 {
			params[key] = values[0] // Take first value if multiple
		}
	}

	return filePath, params, nil
}

// RunTestCases executes one or more test cases with full isolation.
// Configures the peripheral based on test case requirements before setup.
// Runs each test case as a separate subtest using suite.Run().
// Handles both subscription-based tests and standalone script tests.
func (suite *LuaApiSuite) RunTestCases(testCases ...TestCase) {
	for _, tc := range testCases {
		// Capture test case for closure
		testCase := tc

		// Configure peripheral BEFORE SetupSubTest runs (which calls createLuaApi)
		if len(testCase.Peripheral) > 0 {
			// Reset peripheral builder to clean state for this test case
			suite.PeripheralBuilder = testutils.NewPeripheralDeviceBuilder(suite.T())

			// Explicit peripheral configuration provided - use it with full builder support
			for _, svcCfg := range testCase.Peripheral {
				svcBuilder := suite.WithPeripheral().WithService(svcCfg.UUID)
				for _, charCfg := range svcCfg.Characteristics {
					svcBuilder.WithCharacteristic(charCfg.UUID, charCfg.Properties, charCfg.Value)
					for i, descCfg := range charCfg.Descriptors {
						suite.Logger.WithFields(map[string]interface{}{
							"test_name":       testCase.Name,
							"descriptor_idx":  i,
							"descriptor_uuid": descCfg.UUID,
						}).Debug("Adding descriptor to characteristic")

						// Check ReadErrorBehavior and call appropriate builder method
						switch descCfg.ReadErrorBehavior {
						case testutils.DescriptorReadTimeout:
							svcBuilder.WithDescriptorReadTimeout(descCfg.UUID)
						case testutils.DescriptorReadError:
							svcBuilder.WithDescriptorReadError(descCfg.UUID)
						default:
							svcBuilder.WithDescriptor(descCfg.UUID, descCfg.Value)
						}
					}
				}
			}
			// Peripheral will be automatically built during SetupSubTest() -> createLuaApi()
		} else if len(testCase.Subscription.Services) > 0 && testCase.ExpectErrorMessage == "" {
			// Auto-populate peripheral services from subscription configuration
			// Skip for error test cases (ExpectErrorMessage != "") as they may have invalid UUIDs
			for _, svcOpts := range testCase.Subscription.Services {
				suite.ensurePeripheralService(svcOpts.Service, svcOpts.Characteristics)
			}
		}

		suite.Run(testCase.Name, func() {
			suite.RunTestCase(testCase)
		})
	}
}

// buildScriptFromTestCase builds a Lua script from the test case configuration.
// Handles both standalone scripts (Script field) and subscription-based tests (Subscription field).
// Returns the complete script ready for execution and any build errors.
//
// Error Handling: Returns errors for runtime failures (external system issues):
//   - File loading failures → file moved/deleted/permissions
//   - URL parsing failures → could be runtime file path changes
//
// These are recoverable errors that tests can validate with ExpectErrorMessage.
func (suite *LuaApiSuite) buildScriptFromTestCase(testCase TestCase) (string, error) {
	// Handle standalone Script field
	if testCase.Script != "" {
		var scriptContent string
		var params map[string]string

		// Check if Script is a file:// URL
		if strings.HasPrefix(testCase.Script, "file://") {
			filePath, urlParams, err := parseFileURL(testCase.Script)
			if err != nil {
				return "", fmt.Errorf("failed to parse script URL %q: %w", testCase.Script, err)
			}
			params = urlParams

			content, err := testutils.LoadScript(filePath)
			if err != nil {
				return "", fmt.Errorf("failed to load script file %q: %w", filePath, err)
			}
			scriptContent = content
		} else {
			// Inline script
			scriptContent = testCase.Script
		}

		// Build arg[] table initialization
		var argTable strings.Builder
		argTable.WriteString("arg = {}\n")
		for key, value := range params {
			escapedValue := strings.ReplaceAll(value, "\\", "\\\\")
			escapedValue = strings.ReplaceAll(escapedValue, "\"", "\\\"")
			fmt.Fprintf(&argTable, "arg[%q] = %q\n", key, escapedValue)
		}

		return argTable.String() + scriptContent, nil
	}

	// Handle subscription-based tests
	subscription := testCase.Subscription
	if len(subscription.Services) > 0 || testCase.ExpectErrorMessage != "" {
		mode := subscription.Mode
		if mode == "" {
			mode = "EveryUpdate"
		}

		// Check if CallbackScript is a file reference
		callbackScript := subscription.CallbackScript
		if strings.HasPrefix(callbackScript, "file://") {
			filePath := strings.TrimPrefix(callbackScript, "file://")
			fileContent, err := testutils.LoadScript(filePath)
			if err != nil {
				return "", fmt.Errorf("failed to load script file %q: %w", filePath, err)
			}
			callbackScript = fileContent
		}

		script := suite.CreateSubscriptionJsonScript(mode, subscription.MaxRate, callbackScript, subscription.Services...)
		return script, nil
	}

	return "", nil
}

// executeTestSteps executes all test steps with timing and peripheral simulation.
// Helper method extracted for reuse in template method pattern.
// Each step runs as a subtest for clear visibility and isolation.
// Accepts PTY functions for Bridge tests (stub functions for non-bridge tests).
func (suite *LuaApiSuite) executeTestSteps(
	testCase TestCase,
	conn device.Connection,
	collector *LuaOutputCollector,
	luaApi *LuaAPI,
	ptySlaveWrite func([]byte) error,
	ptySlaveRead func() ([]byte, error),
) {
	// Sort steps by time to ensure the correct execution order
	steps := make([]TestStep, len(testCase.Steps))
	copy(steps, testCase.Steps)
	sort.Slice(steps, func(i, j int) bool {
		return steps[i].At < steps[j].At
	})

	// Execute test steps with timing
	startTime := time.Now()
	for stepIdx, step := range steps {
		// Capture step for closure
		stepNum := stepIdx + 1
		currentStep := step

		// Run each step as a subtest for clear identification and proper SIGSEGV attribution
		// Use suite.Run() so testify can properly log SIGSEGV under the step
		// SetupSubTest will skip reinitialization for Step_* subtests
		suite.Run(fmt.Sprintf("Step_%d", stepNum), func() {
			if currentStep.At > 0 {
				elapsed := time.Since(startTime)
				if currentStep.At > elapsed {
					time.Sleep(currentStep.At - elapsed)
				}
			}

			// Simulate peripheral data
			simulator := suite.NewPeripheralDataSimulator()
			if testCase.AllowMultiValue {
				simulator.AllowMultiValue()
			}
			for _, svc := range currentStep.Services {
				svcBuilder := simulator.WithService(svc.Service)
				for _, charVal := range svc.Values {
					svcBuilder.WithCharacteristic(charVal.Characteristic, charVal.Value)
				}
			}

			// Use SimulateFor with explicit connection
			_, err := simulator.SimulateFor(conn, true)
			suite.Require().NoError(err)

			// PTY Slave Write: Write data to the PTY slave for Lua to read via pty_read()
			if currentStep.PTYSlaveWrite != nil {
				data, err := convertToBytes(currentStep.PTYSlaveWrite)
				suite.Require().NoError(err, "Failed to convert PTY slave write data")
				err = ptySlaveWrite(data)
				suite.Require().NoError(err, "Failed to write to PTY slave")
			}

			// Execute Lua code if provided
			if currentStep.Lua != "" {
				err := luaApi.LoadScript(currentStep.Lua, "step")
				suite.Require().NoError(err, "Failed to load step Lua script")
				err = luaApi.ExecuteScript(context.Background(), "")
				suite.Require().NoError(err, "Failed to execute step Lua script")
			}

			// Expected PTY Slave Read: Read from the PTY slave and validate (written by Lua via pty_write)
			if currentStep.ExpectedPTYSlaveRead != nil {
				actualData, err := ptySlaveRead()
				suite.Require().NoError(err, "Failed to read from PTY slave")

				expectedData, err := convertToBytes(currentStep.ExpectedPTYSlaveRead)
				suite.Require().NoError(err, "Failed to convert expected PTY slave read data")

				suite.Require().Equal(expectedData, actualData, "PTY slave read data mismatch")
			}

			// Wait after step execution
			if waitAfter := currentStep.WaitAfter; waitAfter > 0 {
				time.Sleep(waitAfter)
			} else if waitAfter := testCase.WaitAfter; waitAfter > 0 {
				time.Sleep(waitAfter)
			} else {
				time.Sleep(DefaultStepWaitDuration)
			}

			// Validate step output
			if len(currentStep.ExpectedJSONOutput) > 0 {
				suite.ValidateOutput2(collector, currentStep.ExpectedJSONOutput, "", true)
			}

			// Validate step errors
			if len(currentStep.ExpectedErrors) > 0 {
				suite.ValidateOutput2(collector, nil, "", true, currentStep.ExpectedErrors...)
			}
		})
	}
}

// validateFinalOutput performs final validation after all test steps complete.
// Helper method extracted for reuse in template method pattern.
func (suite *LuaApiSuite) validateFinalOutput(testCase TestCase, collector *LuaOutputCollector) {
	if len(testCase.ExpectedJSONOutput) > 0 || testCase.ExpectedStdout != "" || len(testCase.ExpectedErrors) > 0 {
		waitDuration := testCase.WaitAfter
		if waitDuration == 0 {
			waitDuration = DefaultStepWaitDuration
		}
		time.Sleep(waitDuration)

		isSubscription := testCase.Script == ""
		var expectedJson interface{}
		if len(testCase.ExpectedJSONOutput) > 0 {
			expectedJson = testCase.ExpectedJSONOutput
		}
		suite.ValidateOutput2(collector, expectedJson, testCase.ExpectedStdout, isSubscription, testCase.ExpectedErrors...)
	}
}

// ExecuteScriptWithCallbacks is a template method that executes a Lua script with before/after callbacks.
// The default implementation uses suite.LuaApi. BridgeSuite can override to use Bridge's LuaAPI.
// Both callbacks receive both PTY functions (write and read) for maximum flexibility.
func (suite *LuaApiSuite) ExecuteScriptWithCallbacks(
	script string,
	before func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
	after func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)),
) error {
	// Stub functions for non-bridge tests - return clear errors if called
	stubPTYSlaveWrite := func(data []byte) error {
		return fmt.Errorf("PTY operations not supported in non-bridge tests (attempted pty_slave_write)")
	}
	stubPTYSlaveRead := func() ([]byte, error) {
		return nil, fmt.Errorf("PTY operations not supported in non-bridge tests (attempted expected_pty_slave_read)")
	}

	before(suite.LuaApi, stubPTYSlaveWrite, stubPTYSlaveRead)

	var err error
	if script != "" {
		if loadErr := suite.LuaApi.LoadScript(script, "test"); loadErr != nil {
			err = loadErr
		} else if execErr := suite.LuaApi.ExecuteScript(context.Background(), ""); execErr != nil {
			err = execErr
		}
	}

	after(suite.LuaApi, stubPTYSlaveWrite, stubPTYSlaveRead)
	return err
}

// RunTestCase executes a single test case using the ExecuteScriptWithCallbacks template method
func (suite *LuaApiSuite) RunTestCase(testCase TestCase) {
	// Skip test if skip reason is provided
	if testCase.Skip != "" {
		suite.T().Skip(testCase.Skip)
		return
	}

	// Stop suite's collector before creating new one to avoid dual-collector race
	if suite.luaOutputCapture != nil {
		if err := suite.luaOutputCapture.Stop(); err != nil {
			suite.T().Logf("Warning: failed to stop suite output collector: %v", err)
		}
		suite.luaOutputCapture = nil
	}

	// Build script
	script, buildErr := suite.buildScriptFromTestCase(testCase)

	// Collector and connection in closure context
	var collector *LuaOutputCollector
	var conn device.Connection

	// Execute via executor interface for polymorphic dispatch
	scriptErr := suite.Executor.ExecuteScriptWithCallbacks(
		script,
		// Before: setup output collector (both PTY functions available)
		func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)) {
			var err error
			collector, err = NewLuaOutputCollector(luaApi.OutputChannel(), 1024, func(err error) {
				suite.T().Errorf("Output collector error: %v", err)
			})
			suite.Require().NoError(err, "Failed to create output collector")
			suite.Require().NoError(collector.Start(), "Failed to start output collector")

			// Get connection for test steps
			conn = luaApi.GetDevice().GetConnection()
		},

		// After: execute steps and validate (both PTY functions available)
		func(luaApi *LuaAPI, ptySlaveWrite func([]byte) error, ptySlaveRead func() ([]byte, error)) {
			defer func() {
				if err := collector.Stop(); err != nil {
					suite.T().Logf("Warning: failed to stop test collector: %v", err)
				}
			}()

			// Check for script build errors first
			if buildErr != nil {
				return // Error will be checked below
			}

			// Execute test steps
			suite.executeTestSteps(testCase, conn, collector, luaApi, ptySlaveWrite, ptySlaveRead)

			// Final validation
			suite.validateFinalOutput(testCase, collector)

			// TestCase-level PTY slave read validation (after all steps complete)
			if testCase.ExpectedPTYSlaveRead != nil {
				actualData, err := ptySlaveRead()
				suite.Require().NoError(err, "Failed to read from PTY slave at TestCase level")

				expectedData, err := convertToBytes(testCase.ExpectedPTYSlaveRead)
				suite.Require().NoError(err, "Failed to convert expected PTY slave read data at TestCase level")

				suite.Require().Equal(expectedData, actualData, "PTY slave read data mismatch at TestCase level")
			}
		},
	)

	// Combine build and script errors
	if buildErr != nil {
		scriptErr = buildErr
	}

	// Handle error validation
	if testCase.ExpectErrorMessage != "" {
		suite.Require().Error(scriptErr, "Expected an error but got none")
		suite.Require().Contains(scriptErr.Error(), testCase.ExpectErrorMessage, "Error message should contain expected text")
		return
	}

	// If we didn't expect an error, require no error
	suite.Require().NoError(scriptErr)
}

// ValidateOutput2 validates Lua output with flexible parameters, agnostic to test case or test step structure.
// Consumes output once and validates JSON, stdout text, or stderr errors independently.
//
// Parameters:
//   - collector: Output collector to consume from
//   - expectedJson: Expected JSON structure from stdout (nil to skip) - accepts any type: string, map, struct, and array of any of those
//   - expectedStdout: Expected stdout text (empty to skip)
//   - isSubscription: true for subscription callbacks (ignores call_count, TsUs, etc.), false for freeform JSON
//   - expectedErrors: Expected error substrings in stderr (variadic, empty to skip)
//
// JSON validation supports flexible formats for both expected and actual: string, map, struct, array, or array of maps/structs.
// Reuses normalization and hex conversion for consistent validation.
func (suite *LuaApiSuite) ValidateOutput2(collector *LuaOutputCollector, expectedJson interface{}, expectedStdout string, isSubscription bool, expectedErrors ...string) {
	// testify automatically handles T substitution when using suite.Run()
	t := suite.T()
	req := require.New(t)

	// Consumer: split stdout/stderr
	var stdoutBuf strings.Builder
	var stderrLines []string

	type Output struct {
		Stdout string
		Stderr []string
	}

	consumer := func(record *LuaOutputRecord) (Output, error) {
		if record == nil {
			return Output{Stdout: stdoutBuf.String(), Stderr: stderrLines}, nil
		}
		if record.Source == "stdout" {
			stdoutBuf.WriteString(record.Content)
		} else if record.Source == "stderr" {
			stderrLines = append(stderrLines, record.Content)
		}
		return Output{}, nil
	}

	output, err := ConsumeRecords(collector, consumer)
	req.NoError(err, "Failed to consume output")

	// Validate JSON (flexible: string/array/map/struct)
	if expectedJson != nil {
		var actualJson interface{}
		stdout := strings.TrimSpace(output.Stdout)

		if isSubscription {
			// Subscription mode: Parse as JSONL (JSON Lines) or single JSON object
			// Try single JSON object first
			var singleData LuaSubscriptionCallbackData
			if err := json.Unmarshal([]byte(stdout), &singleData); err == nil {
				// Successfully parsed as a single JSON-convert to a generic format
				actualJson = []LuaSubscriptionCallbackData{singleData}
			} else {
				// Failed as a single JSON, try JSONL (multiple lines)
				lines := strings.Split(stdout, "\n")
				var arrayData []LuaSubscriptionCallbackData
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					var data LuaSubscriptionCallbackData
					if err := json.Unmarshal([]byte(line), &data); err != nil {
						req.NoError(err, "Failed to parse subscription JSON line: %q", line)
					}
					arrayData = append(arrayData, data)
				}
				actualJson = arrayData
			}
		} else {
			// Script mode: Parse stdout as raw JSON
			// The script output is typically a map, but expected is an array (from YAML)
			// so we wrap it in an array to match the original validateJSONOutput behavior
			var scriptData map[string]interface{}
			err := json.Unmarshal([]byte(stdout), &scriptData)
			req.NoError(err, "Failed to parse JSON output from script")
			actualJson = []map[string]interface{}{scriptData}
		}

		// Normalize byte arrays to strings (matches Lua's JSON encoding)
		normalized := suite.normalizeValue(expectedJson)

		// KEY FIX: Add default call_count to expected (matches original validateJSONOutput behavior)
		// This must be done BEFORE hex conversion, working with the normalized data
		if isSubscription {
			// expectedJson should be []map[string]interface{} for subscriptions
			if normalizedArray, ok := normalized.([]map[string]interface{}); ok {
				for i, item := range normalizedArray {
					if _, hasCallCount := item["call_count"]; !hasCallCount {
						item["call_count"] = i + 1
					}
				}
			}
		}

		// Convert to hex for better diff readability
		actualHex := suite.convertStringsToHex(actualJson)
		expectedHex := suite.convertStringsToHex(normalized)

		// Wrap arrays in objects since jsonassert doesn't support root-level arrays
		actualWrapped := map[string]interface{}{"array": actualHex}
		expectedWrapped := map[string]interface{}{"array": expectedHex}

		// JSONAsserter with mode-specific options (use current test context)
		ja := testutils.NewJSONAsserter(t)
		if isSubscription {
			ja = ja.WithOptions(
				testutils.WithIgnoredFields("TsUs", "Seq", "Flags", "timestamp", "call_count"),
				testutils.WithIgnoreArrayOrder(true),
			)
		}

		actualJSON, _ := json.Marshal(actualWrapped)
		expectedJSON, _ := json.Marshal(expectedWrapped)
		ja.Assert(string(actualJSON), string(expectedJSON))
	}

	// Validate stdout text (using TextAsserter for better diff output, use current test context)
	if expectedStdout != "" {
		ta := testutils.NewTextAsserter(t)
		// Use template-based assertion for bridges
		ta.AssertWithTemplate(output.Stdout, expectedStdout, suite.templateData)
	}

	// Validate stderr errors (substring matching, following bridge pattern)
	if len(expectedErrors) > 0 {
		if len(output.Stderr) == 0 {
			req.Fail("Expected errors but got none",
				"Expected errors: %q\nActual errors: none", expectedErrors)
			return
		}

		// Check that all expected error substrings are found in captured errors
		for _, expectedErr := range expectedErrors {
			found := false
			for _, capturedErr := range output.Stderr {
				if strings.Contains(capturedErr, expectedErr) {
					found = true
					break
				}
			}
			req.True(found,
				"Expected error substring %q not found in stderr: %v",
				expectedErr, output.Stderr)
		}
	}
}

// convertStringsToHex converts binary string values to hex representation for better diff readability
func (suite *LuaApiSuite) convertStringsToHex(data interface{}) interface{} {
	switch v := data.(type) {
	case []LuaSubscriptionCallbackData:
		result := make([]map[string]interface{}, len(v))
		for i, item := range v {
			recordMap := make(map[string]interface{})
			if item.Record.Values != nil {
				recordMap["Values"] = suite.convertValuesMapToHex(item.Record.Values)
			}
			if item.Record.BatchValues != nil {
				recordMap["BatchValues"] = suite.convertBatchValuesMapToHex(item.Record.BatchValues)
			}
			itemMap := map[string]interface{}{
				"record": recordMap,
			}
			// Include call_count (will be ignored by JSONAsserter with WithIgnoredFields)
			if item.CallCount > 0 {
				itemMap["call_count"] = item.CallCount
			}
			// Include errors if present
			if len(item.Errors) > 0 {
				itemMap["errors"] = item.Errors
			}
			result[i] = itemMap
		}
		return result
	case []map[string]interface{}:
		result := make([]map[string]interface{}, len(v))
		for i, item := range v {
			result[i] = suite.convertMapToHex(item)
		}
		return result
	default:
		return data
	}
}

// convertMapToHex recursively converts string values to hex in a map
func (suite *LuaApiSuite) convertMapToHex(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		switch val := v.(type) {
		case string:
			result[k] = suite.stringToHex(val)
		case map[string]interface{}:
			if k == "Values" {
				result[k] = suite.convertValuesMapToHex(val)
			} else if k == "BatchValues" {
				// Convert map[string]interface{} to map[string][]interface{} for BatchValues
				batchValues := make(map[string][]interface{})
				for charUUID, arrayVal := range val {
					if arr, ok := arrayVal.([]interface{}); ok {
						batchValues[charUUID] = arr
					}
				}
				result[k] = suite.convertBatchValuesMapToHex(batchValues)
			} else {
				result[k] = suite.convertMapToHex(val)
			}
		default:
			result[k] = v
		}
	}
	return result
}

// convertValuesMapToHex converts characteristic values to hex representation
func (suite *LuaApiSuite) convertValuesMapToHex(values map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range values {
		if str, ok := v.(string); ok {
			result[k] = suite.stringToHex(str)
		} else {
			result[k] = v
		}
	}
	return result
}

// convertBatchValuesMapToHex converts batched characteristic values to a hex representation
func (suite *LuaApiSuite) convertBatchValuesMapToHex(batchValues map[string][]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, values := range batchValues {
		hexValues := make([]interface{}, len(values))
		for i, v := range values {
			if str, ok := v.(string); ok {
				hexValues[i] = suite.stringToHex(str)
			} else {
				hexValues[i] = v
			}
		}
		result[k] = hexValues
	}
	return result
}

// stringToHex converts a string to a hex representation
func (suite *LuaApiSuite) stringToHex(s string) string {
	if len(s) == 0 {
		return s
	}

	// Check if a string contains non-printable characters
	hasNonPrintable := false
	for _, b := range []byte(s) {
		if b < 32 || b > 126 {
			hasNonPrintable = true
			break
		}
	}

	if !hasNonPrintable {
		return s // Keep printable strings as-is
	}

	// Convert to hex representation
	var hexStr strings.Builder
	bytes := []byte(s)
	hexStr.Grow(len(bytes)*3 - 1) // Pre-allocate: 2 hex chars + 1 space per byte, minus 1 for no trailing space
	for i, b := range bytes {
		if i > 0 {
			hexStr.WriteByte(' ')
		}
		fmt.Fprintf(&hexStr, "%02x", b)
	}
	return hexStr.String()
}

// normalizeMapInPlace recursively converts byte arrays to strings in a map (modifies in-place)
func (suite *LuaApiSuite) normalizeMapInPlace(m map[string]interface{}) {
	for k, v := range m {
		m[k] = suite.normalizeValue(v)
	}
}

// normalizeValue converts a value, handling byte arrays specially
func (suite *LuaApiSuite) normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case []map[string]interface{}:
		// Handle array of maps (e.g., YAML test cases)
		for i := range val {
			suite.normalizeMapInPlace(val[i])
		}
		return val
	case []interface{}:
		// Check if this is a byte array (all elements are numbers 0-255)
		if isByteArray(val) {
			bytes := make([]byte, len(val))
			for i, item := range val {
				if num, ok := toNumber(item); ok {
					bytes[i] = byte(num)
				}
			}
			return string(bytes)
		}
		// Otherwise normalize each element
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = suite.normalizeValue(item)
		}
		return result
	case map[string]interface{}:
		// Normalize nested maps in-place
		suite.normalizeMapInPlace(val)
		return val
	default:
		return v
	}
}

// isByteArray checks if an array contains only numbers 0-255
func isByteArray(arr []interface{}) bool {
	if len(arr) == 0 {
		return false
	}
	for _, item := range arr {
		if num, ok := toNumber(item); !ok || num < 0 || num > 255 {
			return false
		}
	}
	return true
}

// toNumber converts various numeric types to int
func toNumber(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

// convertToBytes converts interface{} (string or byte array from YAML) to []byte.
// Supports both string values and arrays of integers (0-255).
func convertToBytes(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case string:
		return []byte(val), nil
	case []interface{}:
		// Handle byte array from YAML (array of integers)
		bytes := make([]byte, len(val))
		for i, item := range val {
			if num, ok := toNumber(item); ok {
				if num < 0 || num > 255 {
					return nil, fmt.Errorf("byte value out of range: %d (must be 0-255)", num)
				}
				bytes[i] = byte(num)
			} else {
				return nil, fmt.Errorf("expected number at index %d, got %T", i, item)
			}
		}
		return bytes, nil
	case []byte:
		return val, nil
	default:
		return nil, fmt.Errorf("unsupported type %T for byte conversion (expected string or array)", v)
	}
}
