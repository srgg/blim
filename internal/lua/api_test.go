//go:build test

package lua

import (
	"fmt"
	"io"
	"runtime"
	"syscall"
	"testing"
	"time"

	_ "embed"

	"github.com/aarzilli/golua/lua"
	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
	"github.com/srgg/blim/internal/groutine"
	"github.com/srgg/blim/internal/testutils"
	suitelib "github.com/stretchr/testify/suite"
)

// MockStrategy implements io.ReadWriter for testing
type MockStrategy struct {
	WriteFunc func(data []byte) (int, error)
	ReadFunc  func(p []byte) (n int, err error)
	CloseFunc func() error
}

func (m *MockStrategy) Write(data []byte) (int, error) {
	if m.WriteFunc != nil {
		return m.WriteFunc(data)
	}
	return 0, fmt.Errorf("PTY operations not available (not running in bridge mode)")
}

func (m *MockStrategy) Read(p []byte) (int, error) {
	if m.ReadFunc != nil {
		return m.ReadFunc(p)
	}
	return 0, fmt.Errorf("PTY operations not available (not running in bridge mode)")
}

func (m *MockStrategy) Close() error {
	if m.CloseFunc != nil {
		return m.CloseFunc()
	}
	return nil
}

// testBridgeInfo implements BridgeInfo for testing
type testBridgeInfo struct {
	ttyName        string
	ttySymlinkPath string
	ptyIO          *MockStrategy
	readCallback   func([]byte) // Store callback for testing
}

func (t *testBridgeInfo) GetTTYName() string {
	return t.ttyName
}

func (t *testBridgeInfo) GetTTYSymlink() string {
	return t.ttySymlinkPath
}

func (t *testBridgeInfo) GetPTY() io.ReadWriter {
	return t.ptyIO
}

func (t *testBridgeInfo) SetPTYReadCallback(cb func([]byte)) {
	t.readCallback = cb
}

// TriggerCallback simulates PTY data arrival for testing
func (t *testBridgeInfo) TriggerCallback(data []byte) {
	if t.readCallback != nil {
		t.readCallback(data)
	}
}

// getBLEConnection extracts the underlying BLEConnection from a LuaAPI for white-box testing.
// Used to verify subscription cleanup at the Go/BLE layer, not just Lua behavior.
func getBLEConnection(api *LuaAPI) *goble.BLEConnection {
	conn := api.device.GetConnection()
	return conn.(*goble.BLEConnection)
}

// BLEAPI2TestSuite
type LuaApiTestSuite struct {
	LuaApiSuite

	// Test execution control flags
	parserAPITestPassed bool // Set to true if Test000_CharacteristicParserAPI_Prerequisite passes
}

// TearDownTest cleans up test resources.
func (suite *LuaApiTestSuite) TearDownTest() {
	suite.LuaApiSuite.TearDownTest()
}

// TestErrorHandling device_test error conditions and recovery
// NOTE: Most error handling device_test have been moved to YAML format in lua-api-test-test-scenarios.yaml
//
//	The following device_test remain in Go because they test Lua syntax errors that cannot be
//	generated through the YAML framework (which always generates valid subscription scripts)
func (suite *LuaApiTestSuite) TestErrorHandling() {
	suite.Run("Lua: Missing callback", func() {
		// GOAL: Verify blim.subscribe() returns a clear error when the Callback field is missing
		//
		// TEST SCENARIO: Call subscribing without Callback field → Lua error raised → verify an error message

		suite.Connect("1").FluentLuaTest().
			ExecuteScript(`
				blim.subscribe{
					services = {
						{
							service = "1234",
							chars = {"5678"}
						}
					},
					Mode = "EveryUpdate",
					MaxRate = 0
					-- Missing Callback
				}`,
			).
			Sleep(time.Millisecond * 50).
			ConsumeLuaError("Error executing subscription: subscription: no callback specified")
	})

	suite.Run("Lua: Invalid argument type", func() {
		// GOAL: Verify blim.subscribe() returns clear error when passed non-table argument
		//
		// TEST SCENARIO: Call subscribe() with string instead of table → Lua error raised → verify error message

		suite.Connect("1").FluentLuaTest().
			ExecuteScript(`blim.subscribe("not a table")`).
			Sleep(time.Millisecond * 30).
			ConsumeLuaError("Error: subscribe() expects a lua table argument")
	})

	suite.Run("Lua: Callback causes panic", func() {
		// GOAL: Verify that panics in subscription callbacks are recovered and don't crash the system
		//
		// TEST SCENARIO: Create a subscription with a callback that causes a Lua panic → send notification → panic recovered → verify error logged

		// Create a subscription with a callback that will cause a panic when it tries to access nil values
		suite.Connect("1").FluentLuaTest().

			// Should successfully create a subscription even if the callback will panic
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					-- This will cause a panic by accessing a nil table index deeply
					local x = nil
					local y = x.foo.bar.baz  -- This should cause a Lua error/panic
				end
			}
		`).
			// Simulate a notification to trigger the callback
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)

			}).

			// Give it time for the async callback to execute and panic
			Sleep(time.Millisecond * 50).

			// The panic should be recovered and logged, but execution should continue.
			// Subscription error Callback errors go to stderr, not executionErr
			ConsumeStderrAsLuaError("attempt to index local 'x'").
			ExecuteScript(`print("Still working after panic")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after panic\n")
	})

	suite.Run("Lua: Callback handles missing UUID in record.Values", func() {
		// GOAL: Verify that accessing a non-existent UUID in the record. 'Values' returns nil and can be gracefully handled
		//
		// TEST SCENARIO: Create subscription → send notification → callback accesses non-existent UUID → nil returned → no crash

		// Should successfully create subscription with nil-safe callback
		ft := suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
				received_count = 0
				nil_access_count = 0
				valid_data_count = 0
	
				blim.subscribe{
					services = {
						{
							service = "1234",
							chars = {"5678"}
						}
					},
					Mode = "EveryUpdate",
					MaxRate = 0,
					Callback = function(record)
						received_count = received_count + 1
	
						-- Access the valid UUID that exists in the notification
						local valid_data = record.Values["5678"]
						if valid_data then
							valid_data_count = valid_data_count + 1
						end
	
						-- Try to access a non-existent UUID - should return nil
						local missing_data = record.Values["9999"]
						if missing_data == nil then
							nil_access_count = nil_access_count + 1
						end
	
						-- Verify we can handle nil gracefully without a crash
						assert(missing_data == nil, "non-existent UUID should return nil")
						assert(valid_data ~= nil, "valid UUID should have data")
					end
				}
			`)

		// Send a notification with the subscribed characteristic
		ft.EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
			emitter.
				WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
				Emit(true)
		})

		// Give time for callback to execute
		time.Sleep(50 * time.Millisecond)

		// Verify callback was invoked and handled nil access gracefully
		// Callback should handle missing UUID access gracefully
		ft.MustExecuteScript(`
			assert(received_count == 1, "callback should be invoked once")
			assert(valid_data_count == 1, "valid UUID should be present")
			assert(nil_access_count == 1, "non-existent UUID should return nil")
		`)
	})

	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("1")
		},
		TestCase{

			Name:         "Lua Callback: subscribe with no services specified",
			Subscription: []*device.SubscribeOptions{},
			ScriptError:  "subscription: no services specified",
			Callback:     "local c", // just a boilerplate
		},
		TestCase{
			Name:         "Lua Callback: subscribe with invalid service UUID",
			Subscription: []*device.SubscribeOptions{{Service: "non-existent-service", Characteristics: []string{"non-existent-char"}}},
			Callback:     "local c",
			ScriptError:  "missing services: nonexistentservice",
		},
		TestCase{
			Name:         "Lua Callback: subscribe with invalid characteristic UUID",
			Subscription: []*device.SubscribeOptions{{Service: "180d", Characteristics: []string{"non-existent-char"}}},
			Callback:     "local c",
			ScriptError:  "missing characteristics: nonexistent",
		},
	)
}

// TestCallbackBlockingOpGuards groups the issue #3 reentrancy guards. Every Lua-exposed operation
// that is unsafe to run while a callback holds the single lua_State must be rejected with a clean,
// recoverable Lua error instead of crashing or freezing the engine. Two hazard classes are covered:
//   - releases stateMutex mid-callback -> reentrant corruption / SIGSEGV: blim.sleep, io.read;
//   - blocks the callback goroutine on a synchronous BLE round-trip while holding the state ->
//     engine freeze / potential deadlock: characteristic.read(), characteristic.write(), blim.subscribe.
//
// Each subtest exercises one operation; they share the subscribe -> notify -> op -> stderr-error ->
// still-alive shape and are grouped here to make that relationship explicit. (Self-cancel via the
// callback's cancel() argument stays allowed and is verified elsewhere.)
func (suite *LuaApiTestSuite) TestCallbackBlockingOpGuards() {
	suite.Run("Lua: blim.sleep inside subscription callback raises guard error", func() {
		// GOAL: Verify blim.sleep issued from a notification callback fails with a deterministic guard
		//       error instead of releasing the shared lua_State for concurrent, corrupting reentry (issue #3)
		//
		// TEST SCENARIO: Subscribe → notification fires callback → callback calls blim.sleep → clean Lua error to stderr → engine keeps running

		// Root cause: a callback runs while its goroutine holds the single lua_State via
		// DoWithState -> stateMutex.Lock(). blim.sleep unconditionally does stateMutex.Unlock(), handing
		// the suspended state to another DoWithState (the main loop or a second subscription); that
		// second entry runs Lua on the same in-flight lua_State, corrupting its CallInfo and segfaulting
		// in lua_getinfo. The real-world trigger is blim.term.read_char(wait_ms), whose 10 ms polling
		// loop calls blim.sleep on every step (a UI redraw waiting on a keypress from a notification
		// callback). The guard refuses to release the mutex while inCallbackCount > 0, turning the
		// SIGSEGV into a deterministic, recoverable failure; the race-driven crash itself is not asserted.
		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					-- Releasing the shared lua_State mid-callback is the corruption vector.
					blim.sleep(10)
				end
			}
		`).
			// Simulate a notification to trigger the callback
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)
			}).

			// Give it time for the async callback to execute
			Sleep(time.Millisecond * 50).

			// The guard raises a clean Lua error routed to stderr; the process MUST NOT crash.
			ConsumeStderrAsLuaError("blim.sleep is not allowed inside a callback").
			ExecuteScript(`print("Still working after guarded sleep")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after guarded sleep\n")
	})

	suite.Run("Lua: io.read inside subscription callback raises guard error", func() {
		// GOAL: Verify io.read issued from a notification callback fails with the same deterministic
		//       guard as blim.sleep — io.read releases the shared state the same way (issue #3)
		//
		// TEST SCENARIO: Subscribe → notification fires callback → callback calls io.read → clean Lua error to stderr → engine keeps running

		// io.read is the second mutex-releasing primitive; the guard short-circuits before any stdin
		// machinery, so this is deterministic and needs no TTY.
		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					io.read()
				end
			}
		`).
			// Simulate a notification to trigger the callback
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)
			}).

			// Give it time for the async callback to execute
			Sleep(time.Millisecond * 50).

			// The guard raises a clean Lua error routed to stderr; the process MUST NOT crash.
			ConsumeStderrAsLuaError("io.read is not allowed inside a callback").
			ExecuteScript(`print("Still working after guarded io.read")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after guarded io.read\n")
	})

	suite.Run("Lua: characteristic.read() inside subscription callback raises guard error", func() {
		// GOAL: Verify a synchronous device read from a notification callback fails with the guard
		//       instead of blocking the engine (up to the read timeout) while holding the state (issue #3)
		//
		// TEST SCENARIO: Subscribe → notification fires callback → callback calls char.read() → clean Lua error to stderr → engine keeps running

		// char.read() does not release the mutex (not the SIGSEGV vector) but blocks the callback
		// goroutine while holding stateMutex; the guard short-circuits before the device op.
		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					local char = blim.characteristic("1234", "5678")
					char.read()
				end
			}
		`).
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)
			}).
			Sleep(time.Millisecond * 50).
			ConsumeStderrAsLuaError("characteristic.read() is not allowed inside a callback").
			ExecuteScript(`print("Still working after guarded read")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after guarded read\n")
	})

	suite.Run("Lua: characteristic.write() inside subscription callback raises guard error", func() {
		// GOAL: Verify a synchronous device write from a notification callback fails with the guard
		//       (distinct from read(): it dispatches a different blocking device op) (issue #3)
		//
		// TEST SCENARIO: Subscribe → notification fires callback → callback calls char.write() → clean Lua error to stderr → engine keeps running

		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					local char = blim.characteristic("1234", "ABCD")
					char.write("x")
				end
			}
		`).
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)
			}).
			Sleep(time.Millisecond * 50).
			ConsumeStderrAsLuaError("characteristic.write() is not allowed inside a callback").
			ExecuteScript(`print("Still working after guarded write")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after guarded write\n")
	})

	suite.Run("Lua: blim.subscribe inside subscription callback raises guard error", func() {
		// GOAL: Verify a dynamic subscription from a notification callback fails with the guard — its
		//       synchronous CCCD write would block the callback goroutine while holding the state (issue #3)
		//
		// TEST SCENARIO: Subscribe → notification fires callback → callback calls blim.subscribe → clean Lua error to stderr → engine keeps running

		// blim.subscribe -> executeSubscription -> client.Subscribe is a blocking BLE round-trip; the
		// guard short-circuits before it. Self-cancel via the callback's cancel() arg stays allowed.
		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					blim.subscribe{
						services = {{ service = "180d", chars = {"2a37"} }},
						Mode = "EveryUpdate",
						Callback = function(r) end
					}
				end
			}
		`).
			EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
					Emit(true)
			}).
			Sleep(time.Millisecond * 50).
			ConsumeStderrAsLuaError("blim.subscribe is not allowed inside a callback").
			ExecuteScript(`print("Still working after guarded subscribe")`).
			AssertNoLuaScriptExecutionError().
			ConsumeStdout("Still working after guarded subscribe\n")
	})
}

// TestSubscriptionScenarios validates BLE subscription behavior across multiple streaming modes
// by executing YAML-defined test scenarios.
//
// Test test-scenarios are externalized in lua-api-test-test-scenarios.yaml for maintainability
// and clarity. Each scenario defines subscription configuration, simulation steps, and
// expected Lua callback outputs.
//
// See lua-api-test-test-scenarios.yaml for individual test case documentation.
func (suite *LuaApiTestSuite) TestSubscriptionScenarios() {
	//	suite.RunTestCasesFromFile("test-scenarios/lua-api-test-scenarios.yaml")

	defIgnoreField := []string{"TsUs", "Seq"}

	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("00:00:00:00:00:01")
		},
		TestCase{
			Name: "Lua Callback: EveryUpdate Subscription Test",
			Subscription: []*device.SubscribeOptions{
				{Service: "1234", Characteristics: []string{"5678"}},
			},
			Mode: device.StreamEveryUpdate,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				emitter.AllowMultiValue().
					//
					WithService("1234").
					WithCharacteristic("5678", 0x58, 0x59, 0x5A).
					Emit(true).
					//
					WithCharacteristic("5678", 0x01, 0x02, 0x03).
					Emit(true).
					//
					WithCharacteristic("5678", 0x00, 0x5A).
					Emit(true).
					//
					WithCharacteristic("5678", 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(200 * time.Millisecond)
				builder.
					IgnoreFields(defIgnoreField...).
					Record().CallNo(1).Value("5678", 0x58, 0x59, 0x5A).
					Record().CallNo(2).Value("5678", 0x01, 0x02, 0x03).
					Record().CallNo(3).Value("5678", 0x00, 0x5A).
					Record().CallNo(4).Value("5678", 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08)
			},
		},
		TestCase{
			// GOAL: Verify that Aggregated mode delivers only the latest value per characteristic
			//       at ticker intervals (max_rate), discarding intermediate updates

			Name: "Lua Subscription: Aggregation Subscription Test",
			Subscription: []*device.SubscribeOptions{
				{Service: "1234", Characteristics: []string{"5678"}},
			},
			Mode:    device.StreamAggregated,
			MaxRate: 300,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {

				// Ensure after default maxRate, which is 50ms
				time.Sleep(100 * time.Millisecond)

				emitter.AllowMultiValue().
					//
					WithService("1234").
					WithCharacteristic("5678", 0x58, 0x59, 0x5A).
					Emit(true).
					WithCharacteristic("5678", 0x01, 0x02, 0x03).
					Emit(true)

				// Ensure the second emission is after the subscription max rate
				time.Sleep(320 * time.Millisecond)
				emitter.AllowMultiValue().
					WithService("1234").
					WithCharacteristic("5678", 0x00, 0x5A).
					Emit(true).
					WithCharacteristic("5678", 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(1000 * time.Millisecond)
				builder.
					IgnoreFields(defIgnoreField...).
					Record().CallNo(1).Value("5678", 0x01, 0x02, 0x03).
					Record().CallNo(2).Value("5678", 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08)
			},
		},
		TestCase{
			// GOAL: Verify that Batched mode collects all values per characteristic between
			//       ticker intervals and delivers them as arrays in a single callback
			//

			Name: "Lua Subscription: Batched Subscription Test",
			Subscription: []*device.SubscribeOptions{
				{Service: "1234", Characteristics: []string{"5678"}},
			},
			Mode:    device.StreamBatched,
			MaxRate: 300,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {

				// Ensure after default maxRate, which is 50ms
				time.Sleep(120 * time.Millisecond)

				emitter.AllowMultiValue().
					//
					WithService("1234").
					WithCharacteristic("5678", 0x58, 0x59, 0x5A).
					Emit(true).
					WithCharacteristic("5678", 0x01, 0x02, 0x03).
					Emit(true)

				// Ensure the second emission is after the subscription max rate
				time.Sleep(320 * time.Millisecond)
				emitter.AllowMultiValue().
					WithService("1234").
					WithCharacteristic("5678", 0x00, 0x5A).
					Emit(true).
					WithCharacteristic("5678", 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(800 * time.Millisecond)
				builder.
					IgnoreFields(defIgnoreField...).
					Record().CallNo(1).BatchValues("5678", []byte{0x58, 0x59, 0x5A}, []byte{0x01, 0x02, 0x03}).
					Record().CallNo(2).BatchValues("5678", []byte{0x00, 0x5A}, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
			},
		},
		TestCase{
			// GOAL: Verify that EveryUpdate mode correctly handles subscriptions to multiple
			//       characteristics and delivers separate notifications for each

			Name: "Lua Subscription: EveryUpdate Multi-Characteristic Subscription Test",
			Subscription: []*device.SubscribeOptions{
				{Service: "180D", Characteristics: []string{"2A37", "2A38"}},
			},
			Mode: device.StreamEveryUpdate,
			//MaxRate: 300,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				//// Ensure after default maxRate, which is 50ms
				//time.Sleep(100 * time.Millisecond)

				emitter.AllowMultiValue().
					//
					WithService("180D").
					WithCharacteristic("2A37", 0x58, 0x59, 0x5A).
					Emit(true).
					WithCharacteristic("2A38", 0x01, 0x02, 0x03).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(600 * time.Millisecond)
				builder.
					IgnoreFields(defIgnoreField...).
					Record().CallNo(1).Value("2a37", 0x58, 0x59, 0x5A).
					Record().CallNo(2).Value("2a38", 0x01, 0x02, 0x03)
			},
		},
		TestCase{
			// GOAL: Verify that inspect.lua produces the expected text output format, including parsed characteristic values

			Name:   "Inspect Device Test",
			Script: "file://../../examples/inspect.lua",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("1800").                                    // GAP Service
					WithCharacteristic("2a01", "read", []byte{0x40, 0x00}). // Appearance (Phone 0x0040, little-endian)
					WithService("1234").
					WithCharacteristic("5678", "read,notify", nil).
					WithService("180d"). // Heart Rate Service
					WithCharacteristic("2a37", "read,notify", nil).
					WithCharacteristic("2a38", "read,notify", nil).
					WithService("180f"). // Battery Service
					WithCharacteristic("2a19", "read,notify", nil)
			},
			Output: `
					  Device info:
						ID: 00:00:00:00:00:01
						Address: 00:00:00:00:00:01
						Name: 00:00:00:00:00:01
						RSSI: 0
						Connectable: false
						Advertised Services: none
						Manufacturer Data: none
						Service Data: none
						GATT Services: 4
				
					  [1] Service: 0x1234
						[1.1] Characteristic: 0x5678
							properties: Read, Notify
				
					  [2] Service: Generic Access (0x1800)
						[2.1] Characteristic: Appearance (0x2a01)
							properties: Read
							value (hex):   4000
							value (ascii): @.
							value (parsed): Phone
				
					  [3] Service: Heart Rate (0x180d)
						[3.1] Characteristic: Heart Rate Measurement (0x2a37)
							properties: Read, Notify
						[3.2] Characteristic: Body Sensor Location (0x2a38)
							properties: Read, Notify
				
					  [4] Service: Battery Service (0x180f)
						[4.1] Characteristic: Battery Level (0x2a19)
							properties: Read, Notify
`,
		},
		TestCase{
			// GOAL: Verify that inspect.lua produce expected JSON output format

			Name:   "Inspect Device Test (JSON format)",
			Script: "file://../../examples/inspect.lua?format=json",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("1800").                                    // GAP Service
					WithCharacteristic("2a01", "read", []byte{0x40, 0x00}). // Appearance (Phone 0x0040, little-endian)
					WithService("1234").
					WithCharacteristic("5678", "read,notify", nil).
					WithService("180d"). // Heart Rate Service
					WithCharacteristic("2a37", "read,notify", nil).
					WithCharacteristic("2a38", "read,notify", nil).
					WithService("180f"). // Battery Service
					WithCharacteristic("2a19", "read,notify", nil)
			},
			JsonOutput: `
					{
					   "device":{
						  "address":"00:00:00:00:00:01",
						  "name":"00:00:00:00:00:01",
						  "rssi":0,
						  "connectable":false,
						  "advertised_services":[
							 
						  ],
						  "service_data":[
							 
						  ],
						  "id":"00:00:00:00:00:01"
					   },
					   "services":[
						  {
							 "uuid":"1234",
							 "characteristics":[
								{
								   "has_parser":false,
								   "requires_authentication":false,
								   "descriptors":[
									  
								   ],
								   "value":"",
								   "properties":{
									  "read":{
										 "name":"Read",
										 "value":2
									  },
									  "notify":{
										 "name":"Notify",
										 "value":16
									  }
								   },
								   "uuid":"5678"
								}
							 ]
						  },
						  {
							 "name":"Generic Access",
							 "uuid":"1800",
							 "characteristics":[
								{
								   "parsed_value":"Phone",
								   "name":"Appearance",
								   "has_parser":true,
								   "requires_authentication":false,
								   "descriptors":[
									  
								   ],
								   "value":"@\u0000",
								   "properties":{
									  "read":{
										 "name":"Read",
										 "value":2
									  }
								   },
								   "uuid":"2a01"
								}
							 ]
						  },
						  {
							 "name":"Heart Rate",
							 "uuid":"180d",
							 "characteristics":[
								{
								   "name":"Heart Rate Measurement",
								   "has_parser":false,
								   "requires_authentication":false,
								   "descriptors":[
									  
								   ],
								   "value":"",
								   "properties":{
									  "read":{
										 "name":"Read",
										 "value":2
									  },
									  "notify":{
										 "name":"Notify",
										 "value":16
									  }
								   },
								   "uuid":"2a37"
								},
								{
								   "name":"Body Sensor Location",
								   "has_parser":false,
								   "requires_authentication":false,
								   "descriptors":[
									  
								   ],
								   "value":"",
								   "properties":{
									  "read":{
										 "name":"Read",
										 "value":2
									  },
									  "notify":{
										 "name":"Notify",
										 "value":16
									  }
								   },
								   "uuid":"2a38"
								}
							 ]
						  },
						  {
							 "name":"Battery Service",
							 "uuid":"180f",
							 "characteristics":[
								{
								   "name":"Battery Level",
								   "has_parser":false,
								   "requires_authentication":false,
								   "descriptors":[
									  
								   ],
								   "value":"",
								   "properties":{
									  "read":{
										 "name":"Read",
										 "value":2
									  },
									  "notify":{
										 "name":"Notify",
										 "value":16
									  }
								   },
								   "uuid":"2a19"
								}
							 ]
						  }
					   ]
					}
				`,
		},
		TestCase{
			// GOAL: Verify notify subscription fails when the characteristic only supports indicate
			//
			// TEST SCENARIO: Subscribe with Indicate=false (notify) to indicate-only char → error raised

			Name: "Subscription Mode: Notify Fails on Indicate-only Char",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("ff30").
					WithCharacteristic("ff31", "read,indicate", nil) // NO notify support
			},
			Subscription: []*device.SubscribeOptions{
				{Service: "ff30", Characteristics: []string{"ff31"}, Indicate: false}, // Request notify
			},
			Mode:        device.StreamEveryUpdate,
			ScriptError: "does not support notify", // Expected error
		},
		TestCase{
			// GOAL: Verify indicate subscription succeeds when characteristic supports indicate
			//
			// TEST SCENARIO: Subscribe with Indicate=true to indicate-supporting char → success

			Name: "Subscription Mode: Indicate Success on Indicate-only Char",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("ff30").
					WithCharacteristic("ff31", "read,indicate", nil)
			},
			Subscription: []*device.SubscribeOptions{
				{Service: "ff30", Characteristics: []string{"ff31"}, Indicate: true},
			},
			Mode: device.StreamEveryUpdate,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				emitter.WithService("ff30").WithCharacteristic("ff31", 0x42).Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(300 * time.Millisecond)
				builder.IgnoreFields("TsUs", "Seq").
					Record().CallNo(1).Value("ff31", 0x42)
			},
		},
		TestCase{
			// GOAL: Verify Notify subscription (default) succeeds when characteristic supports Notify
			//
			// TEST SCENARIO: Subscribe without Indicate flag to notify-supporting char → subscription succeeds → receive notification

			Name: "Subscription Mode: Notify Success with Notify-only Char",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("ff30").
					WithCharacteristic("ff32", "read,notify", []byte{0x00})
			},
			Subscription: []*device.SubscribeOptions{
				{Service: "ff30", Characteristics: []string{"ff32"}}, // Indicate: false (default - uses Notify)
			},
			Mode: device.StreamEveryUpdate,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("ff30").
					WithCharacteristic("ff32", 0x01, 0x02).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(200 * time.Millisecond)
				builder.
					IgnoreFields("TsUs", "Seq").
					Record().CallNo(1).Value("ff32", 0x01, 0x02)
			},
		},
		TestCase{
			// GOAL: Verify Indicate Subscription works when the characteristic supports both modes
			//
			// TEST SCENARIO: Subscribe with Indicate=true to char supporting both → Subscription succeeds → receive notification

			Name: "Subscription Mode: Char Supports Both - Indicate Selected",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("ff30").
					WithCharacteristic("ff33", "read,notify,indicate", []byte{0x00})
			},
			Subscription: []*device.SubscribeOptions{
				{Service: "ff30", Characteristics: []string{"ff33"}, Indicate: true},
			},
			Mode: device.StreamEveryUpdate,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("ff30").
					WithCharacteristic("ff33", 0x42).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(200 * time.Millisecond)
				builder.
					IgnoreFields("TsUs", "Seq").
					Record().CallNo(1).Value("ff33", 0x42)
			},
		},
		TestCase{
			// GOAL: Verify Notify subscription (default) works when characteristic supports both modes
			//
			// TEST SCENARIO: Subscribe without Indicate flag to char supporting both → subscription succeeds → receive notification

			Name: "Subscription Mode: Char Supports Both - Notify Selected",
			Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("ff30").
					WithCharacteristic("ff33", "read,notify,indicate", []byte{0x00})
			},
			Subscription: []*device.SubscribeOptions{
				{Service: "ff30", Characteristics: []string{"ff33"}}, // Indicate: false (default - uses Notify)
			},
			Mode: device.StreamEveryUpdate,
			EmitData: func(emitter *testutils.PeripheralDataEmitter) {
				emitter.
					WithService("ff30").
					WithCharacteristic("ff33", 0x43).
					Emit(true)
			},
			ExpectedRecords: func(builder *ExpectedRecordsBuilder) {
				time.Sleep(200 * time.Millisecond)
				builder.
					IgnoreFields("TsUs", "Seq").
					Record().CallNo(1).Value("ff33", 0x43)
			},
		},
	)

}

// TestCharacteristicFunction device_test the blim.characteristic() function
func (suite *LuaApiTestSuite) TestCharacteristicFunction() {

	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("00:00:00:00:00:01")
		},
		TestCase{
			// Goal: Verify that a characteristic handle has an empty descriptors array when no descriptors exist.
			//
			// Scenario: Look up a characteristic without descriptors and verify the descriptors table length is 0.

			Name: "Characteristic without descriptors",
			// Should handle an empty descriptors array
			Script: `
				local char = blim.characteristic("1234", "5678")
				assert(char ~= nil, "characteristic should not be nil")
				assert(type(char.descriptors) == "table", "descriptors should be a table")
				assert(#char.descriptors == 0, "should have 0 descriptors")
			`,
		},
		TestCase{
			// Goal: Verify that the 'property' field is a table containing at least one property sub-table (read/write/notify/indicate).
			//
			// Scenario: Look up a characteristic → confirm properties is a table → ensure at least one property is truthy.

			Name: "Properties field validation",
			// Should have a valid properties field
			Script: `
				local char = blim.characteristic("180D", "2A37")
				assert(char.properties ~= nil, "properties should not be nil")
				assert(type(char.properties) == "table", "properties should be a table")
				-- Check that at least one property is set
				local has_property = char.properties.read or char.properties.write or
									 char.properties.notify or char.properties.indicate
				assert(has_property, "at least one property should be set")`,
		},
		TestCase{
			// GOAL: Verify blim.characteristic() raises an error when service UUID not found
			//
			// TEST SCENARIO: Lookup with non-existent service UUID → Lua error raised → verify an error message

			Name:        "Error: Invalid service UUID",
			Script:      `local char = blim.characteristic("9999", "5678")`,
			ScriptError: "characteristic not found",
		},
		TestCase{
			// GOAL: Verify blim.characteristic() raises error when the characteristic UUID not found in the service
			//
			// TEST SCENARIO: Lookup with valid service but invalid char UUID → Lua error raised → verify an error message

			Name:        "Error: Invalid characteristic UUID",
			Script:      `local char = blim.characteristic("1234", "9999")`,
			ScriptError: "characteristic not found",
		},
		TestCase{
			// GOAL: Verify blim.characteristic() raises an error when no arguments provided
			//
			// TEST SCENARIO: Call with zero or one argument → Lua error raised → verify error mentions two arguments required

			Name:        "Error: blim.characteristic() insufficient arguments if no arguments provided",
			Script:      `local char = blim.characteristic()`,
			ScriptError: "expects two string arguments",
		},
		TestCase{
			// GOAL: Verify blim.characteristic() raises an error when one of the two arguments provided
			//
			// TEST SCENARIO: Call with zero or one argument → Lua error raised → verify error mentions two arguments required

			Name:        "Error: blim.characteristic() insufficient arguments if one argument provided",
			Script:      `local char = blim.characteristic("1234")`,
			ScriptError: "expects two string arguments",
		},
		TestCase{
			// GOAL: Verify blim.characteristic() handles Lua's implicit number-to-string conversion and fails lookup
			//       (Note: Lua ToString() converts numbers to strings, so 123 becomes "123", then lookup fails)
			//
			// TEST SCENARIO: Call with number instead of string → number converted to string → lookup fails → error raised

			Name:        "Error: Invalid argument type - number converted to string",
			Script:      `local char = blim.characteristic(123, "5678")`,
			ScriptError: "characteristic not found",
		},
		TestCase{
			// GOAL: Verify blim.characteristic() raises error when passed table instead of string UUID
			//
			// TEST SCENARIO: Call with table as service UUID → Lua error raised → verify error mentions string arguments

			Name:        "Error: Invalid argument type - table instead of string",
			Script:      `local char = blim.characteristic({service="1234"}, "5678")`,
			ScriptError: "expects two string arguments",
		},
		TestCase{
			// GOAL: Verify characteristic handle contains all required metadata fields with correct types
			//
			// TEST SCENARIO: Lookup characteristic → verify uuid/service/properties/descriptors exist → verify correct types

			Name: "All metadata fields present",
			// Should have all required metadata fields
			Script: `
				local char = blim.characteristic("180F", "2A19")
				-- Verify all required fields exist
				assert(char.uuid ~= nil, "uuid field should exist")
				assert(char.service ~= nil, "service field should exist")
				assert(char.properties ~= nil, "properties field should exist")
				assert(char.descriptors ~= nil, "descriptors field should exist")
	
				-- Verify field types
				assert(type(char.uuid) == "string", "uuid should be string")
				assert(type(char.service) == "string", "service should be string")
				assert(type(char.properties) == "table", "properties should be table")
				assert(type(char.descriptors) == "table", "descriptors should be table")
			`,
		},
		TestCase{
			// GOAL: Verify descriptors array follows Lua 1-indexed convention (index 0 is nil)
			//
			// TEST SCENARIO: Get characteristic → access descriptors[0] → verify it's nil (Lua arrays start at 1)

			Name: "Descriptor array is 1-indexed",
			Script: `
				local char = blim.characteristic("180D", "2A37")
				-- Lua arrays are 1-indexed (even if empty)
				assert(char.descriptors[0] == nil, "index 0 should be nil")
				-- Note: descriptor count varies by characteristic
			`,
		},
		TestCase{
			// GOAL: Verify blim.characteristic() returns consistent metadata across multiple calls for the same characteristic
			//
			// TEST SCENARIO: Call twice for same characteristic → compare all fields → verify identical metadata

			Name: "Multiple calls return consistent data",
			// Should return consistent data across calls
			Script: `
				local char1 = blim.characteristic("1234", "5678")
				local char2 = blim.characteristic("1234", "5678")
				assert(char1.uuid == char2.uuid, "uuid should be consistent")
				assert(char1.service == char2.service, "service should be consistent")
	
				-- Compare properties: check both presence (truthy/falsy) matches
				-- Properties are now tables with value/name, so compare their presence, not reference
				assert((char1.properties.read ~= nil) == (char2.properties.read ~= nil), "read property presence should be consistent")
				assert((char1.properties.write ~= nil) == (char2.properties.write ~= nil), "write property presence should be consistent")
				assert((char1.properties.notify ~= nil) == (char2.properties.notify ~= nil), "notify property presence should be consistent")
				assert((char1.properties.indicate ~= nil) == (char2.properties.indicate ~= nil), "indicate property presence should be consistent")
	
				-- Verify property values match when both present - unconditional assertion using logical implication
				local both_have_read = (char1.properties.read ~= nil) and (char2.properties.read ~= nil)
				local neither_has_read = (char1.properties.read == nil) and (char2.properties.read == nil)
				assert(both_have_read or neither_has_read, "read property presence MUST match (verified above)")
	
				-- Unconditional assertion: if both have read property, values MUST match
				assert((not both_have_read) or (char1.properties.read.value == char2.properties.read.value),
					"read property value MUST be consistent when both have read property")
	
				assert(#char1.descriptors == #char2.descriptors, "descriptor count should be consistent")
			`,
		},
	)
}

// TestCharacteristicRead device_test the characteristic.read() method
func (suite *LuaApiTestSuite) TestCharacteristicRead() {
	// Set up a custom peripheral with Device Information Service and other services for read device_test
	suite.GivenPeripheral(func(b *testutils.PeripheralDeviceBuilder) {
		b.FromJSON(`{
			"services": [
				{
					"uuid": "180A",
					"characteristics": [
						{ "uuid": "2A29", "properties": "read", "value": [66, 76, 73, 77, 67, 111] }
					]
				},
				{
					"uuid": "180F",
					"characteristics": [
						{ "uuid": "2A19", "properties": "read,notify", "value": [85] }
					]
				},
				{
					"uuid": "180D",
					"characteristics": [
						{ "uuid": "2A37", "properties": "read,notify", "value": [0, 75] }
					]
				},
				{
					"uuid": "1234",
					"characteristics": [
						{ "uuid": "5678", "properties": "read,notify", "value": [42] }
					]
				},
				{
					"uuid": "AAAA",
					"characteristics": [
						{ "uuid": "BBBB", "properties": "write", "value": [99] }
					]
				}
			]
		}`)
	})

	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("00:00:00:00:00:01")
		},
		TestCase{
			// GOAL: Verify read() returns a non-nil value and nil error on successful read of a readable characteristic
			//
			// TEST SCENARIO: Read from readable characteristic → value returned with no error → verify value is non-empty string

			Name: "char.read() succeeds on readable characteristic",
			// Should successfully read from a readable characteristic
			Script: `
				local char = blim.characteristic("180A", "2A29")  -- Device Info: Manufacturer Name
	
				if not char.properties.read then
					error("Test setup error: characteristic should be readable")
				end
	
				local value, err = char.read()
				assert(value ~= nil, "read should return value")
				assert(err == nil, "read should not return error")
				assert(type(value) == "string", "value should be string")
				assert(#value > 0, "value should not be empty")
			`,
		},
		TestCase{
			// GOAL: Verify read() returns nil value and error when reading write-only characteristic
			//
			// TEST SCENARIO: Read the write-only characteristic (BBBB) → returns (nil, error)

			Name: "char.read() fails on non-readable characteristic",
			// Should properly error on non-readable characteristic
			Script: `
				local char = blim.characteristic("AAAA", "BBBB")
				local value, err = char.read()
	
				-- MUST fail because the characteristic doesn't support read
				assert(value == nil, "value MUST be nil when error occurs")
				assert(err == "read() failed: characteristic bbbb does not support read operations", "error message MUST be exact, got: " .. tostring(err))
			`,
		},
		TestCase{
			// GOAL: Verify read() can successfully read from multiple different characteristics in a loop
			//
			// TEST SCENARIO: Loop through all characteristics → read readable ones → count successful reads ≥ 1

			Name: "char.read() works for multiple characteristics",
			// Should read multiple characteristics
			Script: `
				local services = blim.list()
				local read_count = 0
				local total_checked = 0
	
				for _, service_uuid in ipairs(services) do
					local service_info = services[service_uuid]
					for _, char_uuid in ipairs(service_info.characteristics) do
						local char = blim.characteristic(service_uuid, char_uuid)
						total_checked = total_checked + 1
	
						-- Read ALWAYS and verify based on properties
						local value, err = char.read()
	
						-- MUST verify every read operation - unconditional assertions
						local is_readable = (char.properties.read ~= nil)
						local read_succeeded = (err == nil)
						local has_value = (value ~= nil)
	
						-- Assertions ALWAYS execute - test the relationship between the property and the result
						assert(is_readable == read_succeeded,
							"read result MUST match readable property for " .. char_uuid ..
							" (readable=" .. tostring(is_readable) .. ", succeeded=" .. tostring(read_succeeded) .. ")")
						assert(is_readable == has_value,
							"value presence MUST match readable property for " .. char_uuid ..
							" (readable=" .. tostring(is_readable) .. ", has_value=" .. tostring(has_value) .. ")")
	
						if is_readable then
							read_count = read_count + 1
						end
					end
				end
	
				-- Unconditional assertions ALWAYS execute
				assert(total_checked > 0, "MUST have checked at least one characteristic")
				assert(read_count > 0, "MUST successfully read at least one characteristic")
				print("Successfully read " .. read_count .. " of " .. total_checked .. " characteristics")
			`,
		},
		TestCase{
			// GOAL: Verify read() is idempotent and can be called multiple times on the same characteristic
			//
			// TEST SCENARIO: Read the same characteristic 3 times → all return success → verify consistent behavior

			Name: "char.read() supports repeated reads",
			// Should allow multiple reads
			Script: `
				local char = blim.characteristic("180f", "2a19")  -- Battery Level
	
				local value1, err1 = char.read()
				local value2, err2 = char.read()
				local value3, err3 = char.read()
	
				assert(value1 ~= nil, "first read should succeed")
				assert(value2 ~= nil, "second read should succeed")
				assert(value3 ~= nil, "third read should succeed")
	
				-- All reads should succeed consistently
				assert(err1 == nil, "first read should not error")
				assert(err2 == nil, "second read should not error")
				assert(err3 == nil, "third read should not error")
			`,
		},
		TestCase{
			// GOAL: Verify read() returns binary-safe byte string accessible via string.byte
			//
			// TEST SCENARIO: Read characteristic with byte value 85 → access via string.byte → verify numeric value in range 0-255

			Name: "char.read() returns byte-addressable string",
			// Should handle binary data correctly
			Script: `
				local char = blim.characteristic("180f", "2a19")  -- Battery Level
				local value, err = char.read()
	
				assert(err == nil, "read should succeed")
				assert(value ~= nil, "value should not be nil")
				assert(#value > 0, "value should not be empty")
	
				-- Test binary data access using string.byte
				local first_byte = string.byte(value, 1)
				assert(type(first_byte) == "number", "string.byte should return number")
				assert(first_byte >= 0 and first_byte <= 255, "byte should be 0-255")
				assert(first_byte == 85, "battery level should be 85")
			`,
		},
		TestCase{
			// GOAL: Verify multibyte characteristic values can be parsed using string.byte for individual bytes
			//
			// TEST SCENARIO: Read Heart Rate characteristic (2 bytes: flags + bpm) → extract both bytes → verify numeric values

			Name: "char.read() value is byte-parsable",
			// Should parse multi-byte binary data
			Script: `
				-- Read multi-byte characteristic (Heart Rate: flag byte + value)
				local hr = blim.characteristic("180d", "2a37")
				local hr_value, err = hr.read()
	
				assert(err == nil, "read should succeed")
				assert(hr_value ~= nil, "value should not be nil")
				assert(#hr_value >= 2, "heart rate value should have at least 2 bytes")
	
				local flags = string.byte(hr_value, 1)
				local bpm = string.byte(hr_value, 2)
				assert(type(flags) == "number", "flags should be number")
				assert(type(bpm) == "number", "bpm should be number")
				assert(flags >= 0 and flags <= 255, "flags byte should be 0-255")
				assert(bpm >= 0 and bpm <= 255, "bpm byte should be 0-255")
			`,
		},
		TestCase{
			// GOAL: Verify read() is exposed as a callable method (userdata type in aarzilli/golua, not a field)
			//
			// TEST SCENARIO: Get a characteristic handle → check a read type is userdata/function → verify callable

			Name: "char.read() is a callable method",
			// Should parse multi-byte binary data
			Script: `
				local char = blim.characteristic("180f", "2a19")
	
				-- read should be a callable (in aarzilli/golua, Go functions are userdata type, not "function")
				-- The important thing is that it's not nil and can be called
				assert(char.read ~= nil, "read should not be nil")
				assert(type(char.read) == "function" or type(char.read) == "userdata",
					   "read should be callable (function or userdata), got: " .. type(char.read))
	
				-- Calling it should work
				local value, err = char.read()
				-- Result validation
				assert(value ~= nil or err ~= nil, "should return either value or error")
			`,
		},
		TestCase{
			// GOAL: Verify read() handles values of any length, including zero-length strings
			//
			// TEST SCENARIO: Read characteristic → verify a string type and non-negative length (including zero)

			Name: "char.read() supports zero-length values",
			// Should handle empty values
			Script: `
				local char = blim.characteristic("180a", "2a29")
				local value, err = char.read()
	
				-- Read should succeed
				assert(err == nil, "read should not error")
				assert(value ~= nil, "value should not be nil")
				-- Empty string is valid (length 0)
				assert(type(value) == "string", "should be string type")
				assert(#value >= 0, "length should be non-negative")
			`,
		},
	)

	suite.Run("Error: read() on non-connected device", func() {
		// GOAL: Verify read() returns error when called on disconnected device
		//
		// TEST SCENARIO: Disconnect device → attempt read → returns (nil, error)

		// Disconnect the device first
		// disconnectErr := suite.LuaApi.GetDevice().Disconnect()
		dev := suite.Connect("1")
		suite.NoError(dev.Disconnect(), "Should disconnect successfully")

		dev.FluentLuaTest().
			ExecuteScript(`
			local char = blim.characteristic("1234", "5678")
			local value, err = char.read()

			-- MUST fail because device is not connected
			assert(value == nil, "value MUST be nil when error occurs")	
			assert(err == "read() failed: read characteristic 5678", "error message MUST be exact, got: " .. tostring(err))
		`).
			AssertNoLuaScriptExecutionError()
	})
}

// TestCharacteristicWrite device_test the characteristic.write() method
// Uses the default peripheral's writable characteristic (1234:ABCD), which supports both writing modes
func (suite *LuaApiTestSuite) TestCharacteristicWrite() {
	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("00:00:00:00:00:01")
		},
		TestCase{
			// GOAL: Verify write() returns true and nil error on successful write with response (default)
			//
			// TEST SCENARIO: Write to writable characteristic with default with_response → success → verify (true, nil) returned

			Name: "char.write() with response (default) succeeds",
			// Should successfully write to a writable characteristic
			Script: `
				local char = blim.characteristic("1234", "ABCD")
	
				if not char.properties.write then
					error("Test setup error: characteristic should be writable")
				end
	
				local result, err = char.write("test data")
				assert(result == true, "write should return true on success")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() accepts explicit with_response=true parameter
			//
			// TEST SCENARIO: Write with with_response=true explicitly → success → verify (true, nil) returned

			Name: "char.write() with response (explicit) succeeds",
			// Should successfully write without response
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("test", true)
				assert(result == true, "write with explicit with_response=true should succeed")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() returns true and nil error when writing without response
			//
			// TEST SCENARIO: Write to characteristic with with_response=false → success → verify (true, nil) returned

			Name: "char.write() without response succeeds",
			// Should successfully write without response
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("test data", false)
				assert(result == true, "write without response should return true on success")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() returns nil and error when writing to a read-only characteristic
			//
			// TEST SCENARIO: Write to read-only characteristic (5678) → returns (nil, error)

			Name: "char.write() fails on non-writable characteristic",
			// Should properly error on non-writable characteristic
			Script: `
				local char = blim.characteristic("1234", "5678")
				local result, err = char.write("data")
	
				-- MUST fail because the characteristic doesn't support write
				assert(result == nil, "result MUST be nil when error occurs")
				assert(err == "write() failed: characteristic 5678 does not support write operations", "error message MUST be exact, got: " .. tostring(err))
			`,
		},
		TestCase{
			// GOAL: Verify write() correctly sends binary data including null bytes
			//
			// TEST SCENARIO: Write binary string with \x00 bytes → success → verify (true, nil) returned

			Name: "char.write() supports binary data",
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local binary_data = "\x01\x02\x00\xFF\x03"
				local result, err = char.write(binary_data)
				assert(result == true, "write should succeed with binary data")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() rejects empty string (CoreBluetooth crashes with NSInternalInconsistencyException)
			//
			// TEST SCENARIO: Write empty string "" → error → verify error message mentions empty data

			Name: "char.write() rejects empty string",
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("")
				assert(result == nil, "write should fail with empty string")
				assert(err ~= nil, "write should return error")
				assert(string.find(err, "empty"), "error should mention empty data, got: " .. tostring(err))
			`,
		},
		TestCase{
			// GOAL: Verify write() is idempotent and can be called multiple times
			//
			// TEST SCENARIO: Write to the same characteristic 3 times → all return success → verify consistent behavior

			Name: "char.write() supports repeated writes",
			// Should allow multiple writes
			Script: `
				local char = blim.characteristic("1234", "ABCD")
	
				local result1, err1 = char.write("first")
				local result2, err2 = char.write("second")
				local result3, err3 = char.write("third")
	
				assert(result1 == true, "first write should succeed")
				assert(result2 == true, "second write should succeed")
				assert(result3 == true, "third write should succeed")
	
				assert(err1 == nil, "first write should not error")
				assert(err2 == nil, "second write should not error")
				assert(err3 == nil, "third write should not error")
			`,
		},
		TestCase{
			// GOAL: Verify write() handles Lua's implicit number-to-string conversion (consistent with characteristic())
			//       (Note: Lua IsString() returns true for numbers due to implicit conversion, so 123 becomes "123")
			//
			// TEST SCENARIO: Call write(123) with number → number converted to string "123" → write succeeds with converted data

			Name: "char.write() accepts numeric values",
			// Should handle implicit number-to-string conversion
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write(123)
				-- Number 123 gets converted to string "123" by Lua
				assert(result == true, "write should succeed with implicitly converted number")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() raises error when with_response parameter is not boolean
			//
			// TEST SCENARIO: Call write("data", "not a boolean") → Lua error raised → verify error message

			Name: "char.write() fails with non-boolean with_response",
			// Should handle implicit number-to-string conversion
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("data", "not boolean")
			`,
			ScriptError: "expects boolean as second argument",
		},
		TestCase{
			// GOAL: Verify write() can use both with_response and without_response modes on the same characteristic
			//
			// TEST SCENARIO: Write twice to ABCD characteristic with both modes → both succeed → verify returns

			Name: "char.write() supports both write modes on same characteristic",
			// Should support both write modes on the same characteristic
			Script: `
				local char = blim.characteristic("1234", "ABCD")
	
				-- Verify characteristic supports both modes
				if not char.properties.write then
					error("Test setup error: characteristic should support write with response")
				end
				if not char.properties.write_without_response then
					error("Test setup error: characteristic should support write without response")
				end
	
				-- Write with response (default)
				local result1, err1 = char.write("with response")
				assert(result1 == true, "write with response should succeed")
				assert(err1 == nil, "write with response should not error")
	
				-- Write without response
				local result2, err2 = char.write("without response", false)
				assert(result2 == true, "write without response should succeed")
				assert(err2 == nil, "write without response should not error")
			`,
		},
		TestCase{
			// GOAL: Verify write() handles large data payloads (MTU chunking is transparent to Lua API)
			//
			// TEST SCENARIO: Write 512 bytes of data → success → verify (true, nil) returned

			Name: "char.write() succeeds with large payload",
			// Should handle large data payloads
			Script: `
				local char = blim.characteristic("1234", "ABCD")
	
				-- Generate 512 bytes of data
				local large_data = string.rep("A", 512)
	
				local result, err = char.write(large_data)
				assert(result == true, "write should succeed with large payload")
				assert(err == nil, "write should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify write() is exposed as a callable method (userdata type in aarzilli/golua, not a field)
			//
			// TEST SCENARIO: Get a characteristic handle → check write type is userdata/function → verify callable

			Name: "char.write() is a callable method",
			// Write should be a callable method
			Script: `
				local char = blim.characteristic("1234", "ABCD")
		
					-- write should be a callable (in aarzilli/golua, Go functions are userdata type, not "function")
					assert(char.write ~= nil, "write should not be nil")
					assert(type(char.write) == "function" or type(char.write) == "userdata",
						   "write should be callable (function or userdata), got: " .. type(char.write))
		
					-- Calling it should work
					local result, err = char.write("test")
					assert(result ~= nil or err ~= nil, "should return either result or error")
			`,
		},
		TestCase{
			// GOAL: Verify write() treats nil with_response parameter as default (true)
			//
			// TEST SCENARIO: Call write("data", nil) → defaults to with_response=true → success

			Name: "char.write() treats nil with_response as true",
			// Write should be a callable method
			Script: `
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("data", nil)
				assert(result == true, "write with nil with_response should succeed (default to true)")
				assert(err == nil, "write should not return error")
			`,
		},
	)

	suite.Run("char.write() fails when device is disconnected", func() {
		// GOAL: Verify write() returns error when called on disconnected device
		//
		// TEST SCENARIO: Disconnect device → attempt write → returns (nil, error)

		// Disconnect the device first
		dev := suite.Connect("1")
		suite.NoError(dev.Disconnect(), "Should disconnect successfully")

		dev.FluentLuaTest().
			// Should properly error on disconnected device
			MustExecuteScript(`
				local char = blim.characteristic("1234", "ABCD")
				local result, err = char.write("data")
	
				-- MUST fail because device is not connected
				assert(result == nil, "result MUST be nil when error occurs")
				assert(err == "write() failed: write characteristic abcd", "error message MUST be exact, got: " .. tostring(err))
			`)
	})
}

// TestLuaBridgeAccess device_test blim.bridge exposure to Lua
func (suite *LuaApiTestSuite) TestLuaBridgeAccess() {

	suite.Run("blim.bridge getters fail when bridge not set", func() {
		// GOAL: Verify blim.bridge exists, but raises an error when calling getter functions in non-bridge mode
		//
		// TEST SCENARIO: No SetBridge() called → blim.bridge exists → calling getter functions raises error

		// First verify blim.bridge exists for bridge mode detection
		suite.Connect("1").FluentLuaTest().
			// blim.bridge should exist for mode detection
			MustExecuteScript(`
				assert(blim.bridge ~= nil, "blim.bridge should exist for mode detection")
				assert(type(blim.bridge) == "table", "blim.bridge should be table")
			`).

			// Verify calling tty_name() raises error (error goes to both executionErr and stderr)
			FailExecuteScript("not available (not running in bridge mode)", `local pty = blim.bridge.tty_name()`).

			// Verify calling tty_symlink() raises error (error goes to both executionErr and stderr)
			FailExecuteScript("not available (not running in bridge mode)", `local symlink = blim.bridge.tty_symlink()`)
	})

	suite.Run("blim.bridge is populated with tty_name and tty_symlink", func() {
		// GOAL: Verify blim.bridge contains tty_name and tty_symlink when bridge is set
		//
		// TEST SCENARIO: SetBridge() called with test bridge → blim.bridge populated → verify fields accessible

		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(
				&testBridgeInfo{
					ttyName:        "/dev/ttys999",
					ttySymlinkPath: "/tmp/test-bridge-link",
					ptyIO:          &MockStrategy{},
				}).

			// Should access bridge info when set
			MustExecuteScript(`
				-- Verify blim.bridge table exists
				assert(blim.bridge ~= nil, "blim.bridge should exist")
				assert(type(blim.bridge) == "table", "blim.bridge should be table")
	
				-- Verify tty_name field
				assert(blim.bridge.tty_name() ~= nil, "tty_name should be set")
				assert(type(blim.bridge.tty_name()) == "string", "tty_name should be string")
				assert(blim.bridge.tty_name() == "/dev/ttys999", "tty_name should match mock value")
	
				-- Verify tty_symlink field
				assert(blim.bridge.tty_symlink() ~= nil, "tty_symlink should be set")
				assert(type(blim.bridge.tty_symlink()) == "string", "tty_symlink should be string")
				assert(blim.bridge.tty_symlink() == "/tmp/test-bridge-link", "tty_symlink should match mock value")
	
				print("✓ blim.bridge.tty_name: " .. blim.bridge.tty_name())
				print("✓ blim.bridge.tty_symlink: " .. blim.bridge.tty_symlink())
			`).
			ConsumeStdoutTrimmed(`
			✓ blim.bridge.tty_name: /dev/ttys999
			✓ blim.bridge.tty_symlink: /tmp/test-bridge-link
		`)
	})
}

// TestPTYWrite device_test pty_write() function through the Lua API
func (suite *LuaApiTestSuite) TestPTYWrite() {
	var writtenData []byte

	clearBridge := func() *testBridgeInfo {
		writtenData = writtenData[:0]
		return &testBridgeInfo{
			ttyName:        "/dev/ttys999",
			ttySymlinkPath: "",
			ptyIO: &MockStrategy{
				WriteFunc: func(data []byte) (int, error) {
					writtenData = append(writtenData, data...)
					return len(data), nil
				},
			},
		}
	}

	writeBridge := func(writeFn func(data []byte) (int, error)) *testBridgeInfo {
		writtenData = writtenData[:0]
		return &testBridgeInfo{
			ttyName:        "/dev/ttys999",
			ttySymlinkPath: "",
			ptyIO: &MockStrategy{
				WriteFunc: writeFn,
			},
		}
	}

	suite.Run("bridge.pty_write() writes data and returns byte count", func() {
		// GOAL: Verify pty_write() successfully writes data and returns byte count
		//
		// TEST SCENARIO: Set up bridge → call pty_write("test") → verify returns byte count and no error

		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(clearBridge()).
			MustExecuteScript(`
				local bytes, err = blim.bridge.pty_write("Hello PTY")
				assert(err == nil, "pty_write should not return error, got: " .. tostring(err))
				assert(bytes == 9, "should write 9 bytes, got: " .. tostring(bytes))
			`)

		suite.Equal("Hello PTY", string(writtenData), "Should write correct data")
	})

	suite.Run("bridge.pty_write() handles binary data", func() {
		// GOAL: Verify pty_write() correctly handles binary data with null bytes and non-printable characters
		//
		// TEST SCENARIO: Write binary data with \x00 bytes → verify exact bytes written

		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(clearBridge()).
			MustExecuteScript(`
				local binary_data = "\x01\x02\x00\xFF\x03"
				local bytes, err = blim.bridge.pty_write(binary_data)
				assert(err == nil, "pty_write should not return error")
				assert(bytes == 5, "should write 5 bytes")
			`)
		suite.Equal([]byte{0x01, 0x02, 0x00, 0xFF, 0x03}, writtenData, "Should preserve binary data")
	})

	suite.Run("bridge.pty_write() returns error on write failure", func() {
		// GOAL: Verify pty_write() returns error when Write() fails
		//
		// TEST SCENARIO: Mock Write() returns error → pty_write() returns (nil, error_message)

		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(
				&testBridgeInfo{
					ttyName: "/dev/ttys999",
					ptyIO: &MockStrategy{
						WriteFunc: func(data []byte) (int, error) {
							return 0, fmt.Errorf("simulated write error")
						},
					},
				}).
			MustExecuteScript(`
				local bytes, err = blim.bridge.pty_write("test")
				assert(bytes == nil, "bytes should be nil on error")
				assert(err ~= nil, "should return error")
				assert(type(err) == "string", "error should be string")
			`)
	})

	suite.Run("bridge.pty_write() errors on non-string argument", func() {
		// GOAL: Verify pty_write() returns error when called with non-string argument
		//
		// TEST SCENARIO: Call pty_write(123) → error returned

		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(
				&testBridgeInfo{
					ttyName: "/dev/ttys999",
					ptyIO:   &MockStrategy{},
				}).
			MustExecuteScript(`
				local bytes, err = blim.bridge.pty_write(123)
				assert(bytes == nil, "bytes should be nil on type error")
				assert(err ~= nil, "should return error for invalid type")
			`)
	})

	suite.Run("bridge.pty_write() errors when bridge not set", func() {
		// GOAL: Verify pty_write() returns error when bridge not set
		//
		// TEST SCENARIO: Call pty_write() without SetBridge() → error returned

		// Don't set the bridge
		const err = "not available (not running in bridge mode)"
		// FailExecuteScript calls ConsumeLuaError which already consumes stderr
		suite.Connect("1").FluentLuaTest().
			FailExecuteScript(err, `blim.bridge.pty_write("test")`)
	})

	suite.Run("bridge.pty_write() handles empty string", func() {
		// GOAL: Verify pty_write() correctly handles empty string (0 bytes written)
		//
		// TEST SCENARIO: Write empty string → returns 0 bytes and no error

		var writeCallCount int
		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(writeBridge(func(data []byte) (int, error) {
				writeCallCount++
				return len(data), nil
			})).
			MustExecuteScript(`
				local bytes, err = blim.bridge.pty_write("")
				assert(err == nil, "pty_write MUST not return error for empty string")
				assert(bytes == 0, "MUST write 0 bytes for empty string, got: " .. tostring(bytes))
			`)

		suite.Equal(1, writeCallCount, "Write should be called once even for empty string")
	})
}

// TestPTYRead device_test pty_read() function through the Lua API
func (suite *LuaApiTestSuite) TestPTYRead() {
	readBridge := func(readFn func(data []byte) (int, error)) BridgeInfo {
		return &testBridgeInfo{
			ttyName:        "/dev/ttys999",
			ttySymlinkPath: "",
			ptyIO: &MockStrategy{
				ReadFunc: readFn,
			},
		}
	}

	ExecuteScenarios(
		func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("00:00:00:00:00:01")
		},
		TestCase{
			// GOAL: Verify pty_read() successfully reads buffered data
			//
			// TEST SCENARIO: Mock Read() returns data → pty_read() returns (data, nil)

			Name: "bridge.pty_read() returns buffered data on success",
			Bridge: readBridge(func(p []byte) (int, error) {
				data := []byte("Response data")
				copy(p, data)
				return len(data), nil

			}),
			Script: `
				local data, err = blim.bridge.pty_read()
				assert(err == nil, "pty_read should not return error")
				assert(data == "Response data", "should read correct data")
			`,
		},
		TestCase{
			// GOAL: Verify pty_read() preserves binary data including null bytes
			//
			// TEST SCENARIO: Read binary data with \x00 → verify exact bytes returned

			Name: "bridge.pty_read() preserves binary data",
			Bridge: readBridge(func(p []byte) (int, error) {
				data := []byte{0xFF, 0x00, 0x01, 0x7F}
				copy(p, data)
				return len(data), nil
			}),
			Script: `
				local data, err = blim.bridge.pty_read()
				assert(err == nil, "should not return error")
				assert(#data == 4, "should read 4 bytes")
				assert(string.byte(data, 1) == 0xFF, "first byte should be 0xFF")
				assert(string.byte(data, 2) == 0x00, "second byte should be 0x00")
				assert(string.byte(data, 3) == 0x01, "third byte should be 0x01")
				assert(string.byte(data, 4) == 0x7F, "fourth byte should be 0x7F")
			`,
		},
		TestCase{
			// GOAL: Verify pty_read() returns ("", nil) when no data available (EAGAIN)
			//
			// TEST SCENARIO: Mock Read() returns EAGAIN → pty_read() returns ("", nil)

			Name: "bridge.pty_read() returns empty string when no data available",
			Bridge: readBridge(func(p []byte) (int, error) {
				return 0, syscall.EAGAIN
			}),
			Script: `
				local data, err = blim.bridge.pty_read()
				assert(err == nil, "should not return error for EAGAIN")
				assert(data == "", "should return empty string when no data available")
			`,
		},
		TestCase{
			// GOAL: Verify pty_read() respects custom max_bytes parameter
			//
			// TEST SCENARIO: Call pty_read(128) → verify buffer size passed to Read()

			Name: "bridge.pty_read() respects max_bytes parameter",
			Bridge: readBridge(func(p []byte) (int, error) {
				if len(p) != 128 {
					panic(fmt.Errorf("expected buffer size 127, got %d", len(p)))
				}
				data := []byte("passed")
				copy(p, data)
				return len(data), nil
			}),
			Script: `
				local data, err = blim.bridge.pty_read(128)
				assert(err == nil, "should not return error")
			`,
		},
		TestCase{
			// GOAL: Verify pty_read() returns error when bridge not set
			//
			// TEST SCENARIO: Call pty_read() without SetBridge() → error returned

			Name: "bridge.pty_read() errors when bridge not set",

			// Don't set the bridge
			//bridge: func(func(p []byte) {}
			Script: `
				blim.bridge.pty_read()
			`,
			ScriptError: "not available (not running in bridge mode)",
		},
		TestCase{
			// GOAL: Verify pty_read() returns error when max_bytes is zero
			//
			// TEST SCENARIO: Call pty_read(0) → error returned with nil data

			Name: "bridge.pty_read() errors when max_bytes is zero",
			Bridge: &testBridgeInfo{
				ttyName: "/dev/ttys999",
				ptyIO:   &MockStrategy{},
			},
			Script: `
				local data, err = blim.bridge.pty_read(0)
				assert(data == nil, "data MUST be nil on error")
				assert(err ~= nil, "MUST return error for zero max_bytes")
				assert(type(err) == "string", "error MUST be string")
				error(err)
			`,
			ScriptError: "max_bytes must be greater than zero",
		},
		TestCase{
			// GOAL: Verify pty_read() returns error when max_bytes is negative
			//
			// TEST SCENARIO: Call pty_read(-1) → error returned with nil data

			Name: "bridge.pty_read() errors when max_bytes is negative",
			Bridge: &testBridgeInfo{
				ttyName: "/dev/ttys999",
				ptyIO:   &MockStrategy{},
			},
			Script: `
				local data, err = blim.bridge.pty_read(-1)
				assert(data == nil, "data MUST be nil on error")
				assert(err ~= nil, "MUST return error for negative max_bytes")
				assert(type(err) == "string", "error MUST be string")
				error(err)
			`,
			ScriptError: "max_bytes must be greater than zero",
		},
		TestCase{
			// GOAL: Verify pty_read() returns ("", nil) when Read() returns EOF
			//
			// TEST SCENARIO: Mock Read() returns EOF → pty_read() returns ("", nil)

			Name: "bridge.pty_read() returns empty string on EOF",
			Bridge: readBridge(func(p []byte) (int, error) {
				return 0, io.EOF
			}),
			Script: `
				local data, err = blim.bridge.pty_read()
				assert(err == nil, "MUST not return error for EOF")
				assert(data == "", "MUST return empty string on EOF")
			`,
		},
		TestCase{
			// GOAL: Verify pty_read() returns error when Read() fails with non-EAGAIN error
			//
			// TEST SCENARIO: Mock Read() returns generic error → pty_read() returns (nil, error_message)

			Name: "bridge.pty_read() errors on read failure",
			Bridge: readBridge(func(p []byte) (int, error) {
				return 0, fmt.Errorf("simulated read failure")
			}),
			Script: `
				local data, err = blim.bridge.pty_read()
				assert(data == nil, "data MUST be nil on error")
				assert(err ~= nil, "MUST return error on read failure")
				assert(type(err) == "string", "error MUST be string")
			`,
		},
	)
}

// TestPTYOnData device_test pty_on_data() callback registration and invocation
func (suite *LuaApiTestSuite) TestPTYOnData() {
	suite.Run("Register callback and receive data", func() {
		// GOAL: Verify pty_on_data() registers callback that receives data when PTY data arrives
		//
		// TEST SCENARIO: Register callback → simulate PTY data → callback invoked with correct data

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		// Should register callback
		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).
			MustExecuteScript(`
				-- Storage for callback data
				received_data = nil
	
				-- Register callback
				blim.bridge.pty_on_data(function(data)
					received_data = data
				end)
	
				-- Callback registered, waiting for data
				assert(received_data == nil, "should not have received data yet")
			`)

		// Simulate PTY data arrival
		testBridge.TriggerCallback([]byte("test data"))

		// Verify callback was invoked
		// Callback should receive data
		ft.MustExecuteScript(`
			assert(received_data ~= nil, "callback MUST receive data")
			assert(received_data == "test data", "callback MUST receive correct data")
		`)
	})

	suite.Run("Unregister callback with nil", func() {
		// GOAL: Verify passing nil to pty_on_data() unregisters the callback
		//
		// TEST SCENARIO: Register callback → unregister with nil → trigger data → callback not invoked

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		// Should register callback
		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).
			MustExecuteScript(`
				-- Storage for callback data
				call_count = 0
	
				-- Register callback
				blim.bridge.pty_on_data(function(data)
					call_count = call_count + 1
				end)
			`)

		// Trigger once to verify it works
		testBridge.TriggerCallback([]byte("first"))

		// Should unregister callback
		ft.MustExecuteScript(`
			assert(call_count == 1, "callback MUST be called once")

			-- Unregister callback
			blim.bridge.pty_on_data(nil)
		`)

		// Trigger again - should not increment
		testBridge.TriggerCallback([]byte("second"))

		// Callback should not be invoked after unregister
		ft.MustExecuteScript(`
			assert(call_count == 1, "callback MUST not be called after unregister")
		`)
	})

	suite.Run("Error when called without bridge", func() {
		// GOAL: Verify pty_on_data() returns error when bridge not set
		//
		// TEST SCENARIO: Call pty_on_data() without SetBridge() → error returned

		// Don't set the bridge
		const err = "not available (not running in bridge mode)"
		// FailExecuteScript calls ConsumeLuaError which already consumes stderr
		suite.Connect("1").FluentLuaTest().
			FailExecuteScript(err, "blim.bridge.pty_on_data(function(data) end)")
	})

	suite.Run("Error with invalid argument type", func() {
		// GOAL: Verify pty_on_data() returns error when called with non-function argument
		//
		// TEST SCENARIO: Call pty_on_data("string") → error returned

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		err := "expects a function or nil argument"
		suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).
			// FailExecuteScript calls ConsumeLuaError which already consumes stderr
			FailExecuteScript(err, `blim.bridge.pty_on_data("not a function")`)
	})

	suite.Run("Callback receives binary data", func() {
		// GOAL: Verify pty_on_data() callback receives binary-safe data including null bytes
		//
		// TEST SCENARIO: Register callback → send binary data with \x00 → callback receives exact bytes

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).

			// Should register callback
			MustExecuteScript(`
				-- Storage for callback data
				received_bytes = nil
	
				-- Register callback
				blim.bridge.pty_on_data(function(data)
					received_bytes = {}
					for i = 1, #data do
						received_bytes[i] = string.byte(data, i)
					end
				end)
			`)

		// Trigger with binary data
		testBridge.TriggerCallback([]byte{0x01, 0x00, 0xFF, 0x7F})

		// Callback should receive binary data
		ft.MustExecuteScript(`
			assert(received_bytes ~= nil, "callback MUST receive data")
			assert(#received_bytes == 4, "callback MUST receive 4 bytes")
			assert(received_bytes[1] == 0x01, "first byte MUST be 0x01")
			assert(received_bytes[2] == 0x00, "second byte MUST be 0x00")
			assert(received_bytes[3] == 0xFF, "third byte MUST be 0xFF")
			assert(received_bytes[4] == 0x7F, "fourth byte MUST be 0x7F")
		`)
	})

	suite.Run("Multiple data arrivals invoke callback correctly", func() {
		// GOAL: Verify callback receives multiple consecutive data arrivals correctly
		//
		// TEST SCENARIO: Register callback → trigger 3 times with different data → verify all 3 received

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).

			// Should register callback
			MustExecuteScript(`
				-- Storage for callback data
				received_data = {}
	
				-- Register callback
				blim.bridge.pty_on_data(function(data)
					table.insert(received_data, data)
				end)
			`)

		// Trigger 3 times with different data
		testBridge.TriggerCallback([]byte("first"))
		testBridge.TriggerCallback([]byte("second"))
		testBridge.TriggerCallback([]byte("third"))

		// Callback should receive all data arrivals
		ft.MustExecuteScript(`
			assert(#received_data == 3, "callback MUST be invoked 3 times")
			assert(received_data[1] == "first", "first data MUST be correct")
			assert(received_data[2] == "second", "second data MUST be correct")
			assert(received_data[3] == "third", "third data MUST be correct")
		`)
	})

	suite.Run("Callback replacement updates handler correctly", func() {
		// GOAL: Verify registering a new callback replaces the old one, and the old callback is not invoked
		//
		// TEST SCENARIO: Register callback A → trigger data → register callback B → trigger data → only B receives a second

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).

			// Should register first callback
			MustExecuteScript(`
				-- Storage for callbacks
				callback_a_count = 0
				callback_b_count = 0
	
				-- Register first callback
				blim.bridge.pty_on_data(function(data)
					callback_a_count = callback_a_count + 1
				end)
			`)

		// Trigger once for callback A
		testBridge.TriggerCallback([]byte("for_a"))

		// Should register second callback
		ft.MustExecuteScript(`
			assert(callback_a_count == 1, "callback A MUST be called once")
			assert(callback_b_count == 0, "callback B MUST not be called yet")

			-- Register second callback (should replace first)
			blim.bridge.pty_on_data(function(data)
				callback_b_count = callback_b_count + 1
			end)
		`)

		// Trigger once for callback B
		testBridge.TriggerCallback([]byte("for_b"))

		// Only new callback should be invoked
		ft.MustExecuteScript(`
			assert(callback_a_count == 1, "callback A MUST not be called again")
			assert(callback_b_count == 1, "callback B MUST be called once")
		`)
	})

	suite.Run("Callback error recovery prevents crash", func() {
		// GOAL: Verify panic in the callback is recovered and the Lua API continues working
		//
		// TEST SCENARIO: Register callback that panics → trigger data → panic recovered → the system still works

		mockStrategy := &MockStrategy{}
		testBridge := &testBridgeInfo{
			ttyName: "/dev/ttys999",
			ptyIO:   mockStrategy,
		}

		ft := suite.Connect("1").FluentLuaTest().
			// Create test bridge with mock strategy
			WithBridge(testBridge).

			// Should register callback even if it will panic
			MustExecuteScript(`
				-- Register callback that will panic
				blim.bridge.pty_on_data(function(data)
					-- This will cause a Lua error/panic
					local x = nil
					local y = x.foo.bar  -- attempt to index nil
				end)
			`)

		// Give time for the async callback to execute and panic to be recovered
		time.Sleep(200 * time.Millisecond)

		// Trigger callback - should panic but be recovered
		testBridge.TriggerCallback([]byte("trigger panic"))

		// Give time for the async callback to execute and panic to be recovered
		time.Sleep(200 * time.Millisecond)

		ft.ConsumeStderrAsLuaError("attempt to index local 'x' (a nil value)")

		// Verify system still works by registering a new callback
		// Should register new callback after panic recovery
		ft.MustExecuteScript(`
			good_callback_count = 0

			-- Register new callback that works
			blim.bridge.pty_on_data(function(data)
				good_callback_count = good_callback_count + 1
			end)
		`)

		// Trigger new callback to verify system works
		testBridge.TriggerCallback([]byte("after recovery"))

		// System should continue working after callback panic
		ft.MustExecuteScript(`
			assert(good_callback_count == 1, "new callback MUST work after panic recovery")
		`)
	})
}

var dumpChar = func(service string, char string, ignoreKeys ...string) string {
	// Convert Go slice to Lua table literal for ignoreKeys
	ignoreKeysLua := "{"
	for i, k := range ignoreKeys {
		if i > 0 {
			ignoreKeysLua += ","
		}
		ignoreKeysLua += fmt.Sprintf("%q", k)
	}
	ignoreKeysLua += "}"

	return fmt.Sprintf(`
        local json = require("json")

		if char == nil then
	        char = blim.characteristic("%s", "%s")
		end

        local ignore_keys = %s

        local function is_ignored(key)
            for _, k in ipairs(ignore_keys) do
                if k == key then return true end
            end
            return false
        end

        local function serialize_table(t)
            local ttype = type(t)
            if ttype ~= "table" then
                if ttype == "userdata" then
                    return nil -- skip userdata
                end
                return t
            end

            local has_array = false
            local has_hash = false
            local array_part = {}
            local hash_part = {}

            for k, v in pairs(t) do
                if type(k) == "string" and is_ignored(k) then
                    -- skip ignored key
                elseif type(k) == "number" then
                    has_array = true
                    array_part[k] = serialize_table(v)
                elseif type(k) == "string" then
                    has_hash = true
                    hash_part[k] = serialize_table(v)
                end
            end

            if has_array and has_hash then
                return { ipair = array_part, named = hash_part }
            elseif has_array then
                return array_part
            else
                return hash_part
            end
        end

        local serialized_char = serialize_table(char)
        print(json.encode(serialized_char))
    `, service, char, ignoreKeysLua)
}

func (suite *LuaApiTestSuite) TestDescriptor() {
	//suite.RunTestCasesFromFile("test-scenarios/lua-api-well-known-descriptor-types-scenarios.yaml")

	dumpFirstDescriptor := func(service string, char string) string {
		return fmt.Sprintf(`
					local json = require("json")
					local char = blim.characteristic("%s", "%s")
					local desc = char.descriptors[1]
					print(json.encode(desc))
				`, service, char)
	}

	suite.Run("Descriptor parsing", func() {
		suite.Run("Client Characteristic Configuration (0x2902)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify CCCD with notifications enabled is correctly parsed
					//
					// TEST SCENARIO: CCCD with value 0x0100 (notifications=true, indications=false) → verify parsed_value structure

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "", []byte{}).
							WithDescriptor("2902", 0x01, 0x00) // Notifications enabled
					},
					Name:   "CCCD descriptor exposes parsed_value with notifications and indications",
					Script: dumpFirstDescriptor("180d", "2a37"),
					JsonOutput: `{
						"handle":3,
						"index":0,
						"value":"0100",
						"parsed_value":{
							"indications":false,
							"notifications":true
						},
						"name":"Client Characteristic Configuration",
						"uuid":"2902"
					}`,
				},
				TestCase{
					// GOAL: Verify CCCD with indications enabled is correctly parsed
					//
					// TEST SCENARIO: CCCD with value 0x0200 (notifications=false, indications=true) → verify parsed_value structure

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "", []byte{}).
							WithDescriptor("2902", 0x02, 0x00) // Indications enabled
					},
					Name:   "CCCD descriptor exposes parsed_value with indications enabled",
					Script: dumpFirstDescriptor("180d", "2a37"),
					JsonOutput: `{
				   "handle":3,
				   "index":0,
				   "value":"0200",
				   "parsed_value":{
					  "notifications":false,
					  "indications":true
				   },
				   "uuid":"2902",
				   "name":"Client Characteristic Configuration"
				}`,
				},
				TestCase{
					// GOAL: Verify CCCD with both notifications and indications enabled
					//
					// TEST SCENARIO: CCCD with value 0x0300 → verify both flags are true

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "", []byte{}).
							WithDescriptor("2902", 0x03, 0x00) // Both enabled
					},
					Name:   "CCCD parsed_value exposes notifications and indications",
					Script: dumpFirstDescriptor("180d", "2a37"),
					JsonOutput: `{
				   "name":"Client Characteristic Configuration",
				   "index":0,
				   "parsed_value":{
					  "indications":true,
					  "notifications":true
				   },
				   "uuid":"2902",
				   "value":"0300",
				   "handle":3
				}`,
				},
			)
		})

		suite.Run("User Description (0x2901)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify User Description descriptor is correctly parsed as UTF-8 string
					//
					// TEST SCENARIO: User Description with "Hello" → verify parsed_value is string

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2901", 0x48, 0x65, 0x6C, 0x6C, 0x6F) // "Hello" in ASCII
					},
					Name:   "User Description descriptor exposes parsed UTF-8 string",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "handle":3,
				   "index":0,
				   "name":"Characteristic User Descriptor",
				   "value":"48656C6C6F",
				   "uuid":"2901",
				   "parsed_value":"Hello"
				}`,
				},
			)
		})

		suite.Run("Characteristic Extended Properties (0x2900)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify Extended Properties descriptor is correctly parsed
					//
					// TEST SCENARIO: Extended Properties with reliable_write + writable_auxiliaries → verify boolean flags

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2900", 0x03, 0x00) // Reliable Write + Writable Auxiliaries
					},
					Name:   "Extended Properties: Reliable Write and Writable Auxiliaries",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "index":0,
				   "name":"Characteristic Extended Properties",
				   "parsed_value":{
					  "writable_auxiliaries":true,
					  "reliable_write":true
				   },
				   "uuid":"2900",
				   "value":"0300",
				   "handle":3
				}`,
				},
			)
		})

		suite.Run("Characteristic Presentation Format (0x2904)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify Presentation Format descriptor is correctly parsed
					//
					// TEST SCENARIO: Presentation Format for battery percentage → verify all fields

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180f").
							WithCharacteristic("2a19", "", []byte{}).
							WithDescriptor("2904", 0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00) // uint8, exponent=0, unit=0x2901 (percentage)
					},
					Name:   "Presentation Format: Battery Level Percentage",
					Script: dumpFirstDescriptor("180f", "2a19"),
					JsonOutput: `{
					   "index":0,
					   "uuid":"2904",
					   "value":"04000129010000",
					   "name":"Characteristic Presentation Format",
					   "parsed_value":{
						  "exponent":0,
						  "format":4,
						  "unit":10497,
						  "namespace":1,
						  "description":0
					   },
					   "handle":3
				}`,
				},
			)
		})

		suite.Run("Server Configuration (0x2903)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify Server Characteristic Configuration descriptor is correctly parsed
					//
					// TEST SCENARIO: Server Config with broadcasts enabled → verify boolean flag

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2903", 0x01, 0x00) // Broadcasts enabled
					},
					Name:   "Server Config: Broadcasts Enabled",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "name":"Server Characteristic Configuration",
				   "handle":3,
				   "index":0,
				   "value":"0100",
				   "uuid":"2903",
				   "parsed_value":{
					  "broadcasts":true
				   }
				}`,
				},
			)
		})

		suite.Run("Valid Range (0x2906)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify Valid Range descriptor is correctly parsed
					//
					// TEST SCENARIO: Valid Range with min=0x0000, max=0x00FF → verify hex strings

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2906", 0x00, 0x00, 0x00, 0xFF) // min=0x0000, max=0x00FF
					},
					Name:   "Valid Range descriptor exposes min and max",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "uuid":"2906",
				   "parsed_value":{
					  "max":"00FF",
					  "min":"0000"
				   },
				   "handle":3,
				   "name":"Valid Range",
				   "index":0,
				   "value":"000000FF"
				}`,
				},
			)
		})

		suite.Run("Aggregate Format Descriptor (0x2905)", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify Characteristic Aggregate Format descriptor contains an array of descriptor references
					//
					// TEST SCENARIO: Read Aggregate Format descriptor → verify parsed_value is table → verify contains descriptor objects with full structure

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "read", []byte{}).
							WithDescriptor("2904", 0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00).
							WithDescriptor("2904", 0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00).
							WithDescriptor("2905", 0x00, 0x01, 0x01, 0x01) // Applies to Handles 0x0100, 0x0101 (little-endian)
					},
					Name:   "Aggregate Format",
					Script: dumpChar("180d", "2a37"),
					JsonOutput: `{
					  "name": "Heart Rate Measurement",
					  "uuid": "2a37",
					  "service": "180d",
					  "properties": {
						"ipair": [
						  {
							"name": "Read",
							"value": 2
						  }
						],
						"named": {
						  "read": {
							"name": "Read",
							"value": 2
						  }
						}
					  },
					  "has_parser": false,
					  "requires_authentication": false,
					  "descriptors": [
						{
						  "handle": 3,
						  "uuid": "2904",
						  "index": 0,
						  "value": "04000129010000",
						  "parsed_value": {
							"format": 4,
							"exponent": 0,
							"unit": 10497,
							"namespace": 1,
							"description": 0
						  },
						  "name": "Characteristic Presentation Format"
						},
						{
						  "handle": 4,
						  "uuid": "2904",
						  "index": 1,
						  "value": "04000129010000",
						  "parsed_value": {
							"format": 4,
							"exponent": 0,
							"unit": 10497,
							"namespace": 1,
							"description": 0
						  },
						  "name": "Characteristic Presentation Format"
						},
						{
						  "handle": 5,
						  "uuid": "2905",
						  "index": 2,
						  "value": "03000400",
						  "parsed_value": [
							{
							  "handle": 3,
							  "uuid": "2904",
							  "index": 0,
							  "value": "04000129010000",
							  "parsed_value": {
								"format": 4,
								"exponent": 0,
								"unit": 10497,
								"namespace": 1,
								"description": 0
							  },
							  "name": "Characteristic Presentation Format"
							},
							{
							  "handle": 4,
							  "uuid": "2904",
							  "index": 1,
							  "value": "04000129010000",
							  "parsed_value": {
								"format": 4,
								"exponent": 0,
								"unit": 10497,
								"namespace": 1,
								"description": 0
							  },
							  "name": "Characteristic Presentation Format"
							}
						  ],
						  "name": "Characteristic Aggregate Format"
						}
					  ]
				}`,
				},
				TestCase{
					// GOAL: Verify Aggregate Format with no references returns an empty array
					//
					// TEST SCENARIO: Read Aggregate Format with no descriptor references → verify parsed_value is empty table

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "read", []byte{}).
							WithDescriptor("2905") // NO descriptor handles provided
					},
					Name: "Aggregate Format no descriptor references",
					Script: `
						local json = require("json")
						local char = blim.characteristic("180d", "2a37")
						local desc = char.descriptors[1]
						print(json.encode(desc))
					`,
					JsonOutput: `{
					   "name":"Characteristic Aggregate Format",
					   "index":0,
					   "parsed_value":[
						  
					   ],
					   "uuid":"2905",
					   "handle":3
					}`,
				},
			)

		})
		suite.Run("Custom descriptor", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify unknown descriptor UUIDs return raw byte values
					//
					// TEST SCENARIO: Custom descriptor UUID → verify value is hex string, parsed_value is also hex string

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("ff01", 0xDE, 0xAD, 0xBE, 0xEF) // Custom data
					},
					Name:   "Custom UUID Returns Raw Bytes",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "value": "DEADBEEF",
				   "uuid":"ff01",
				   "parsed_value": "DEADBEEF",
				   "index":0,
				   "handle":3
				}`,
				},
			)
		})

		suite.Run("Expose parse error", func() {
			ExecuteScenarios(
				func(tc TestCase) *LuaTestableDevice {
					if tc.Peripheral != nil {
						suite.GivenPeripheral(tc.Peripheral)
					}
					return suite.Connect("00:00:00:00:00:01")
				},
				TestCase{
					// GOAL: Verify that parse errors are correctly represented in parsed_value
					//
					// TEST SCENARIO: CCCD with invalid length (1 byte instead of 2) → verify parsed_value contains error

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2902", 0x01) // Invalid: only 1 byte (should be 2)
					},
					Name:   "CCCD invalid length",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "uuid":"2902",
				   "parsed_value":{
					  "error":"parse_error: invalid length for client config: expected 2, got 1"
				   },
				   "name":"Client Characteristic Configuration",
				   "handle":3,
				   "index":0,
				   "value":"01"
				}`,
				},
				TestCase{
					// GOAL: Verify parse errors are correctly handled for Extended Properties
					//
					// TEST SCENARIO: Extended Properties with wrong length → verify error in parsed_value

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2900", 0x03) // Invalid: only 1 byte (should be 2)
					},
					Name:   "Extended Properties invalid length",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "uuid":"2900",
				   "parsed_value":{
					  "error":"parse_error: invalid length for extended properties: expected 2, got 1"
				   },
				   "name":"Characteristic Extended Properties",
				   "handle":3,
				   "index":0,
				   "value":"03"
				}`,
				},
				TestCase{
					// GOAL: Verify that invalid UTF-8 in User Description produces parse error
					//
					// TEST SCENARIO: User Description with invalid UTF-8 bytes → verify error in parsed_value

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("1234").
							WithCharacteristic("5678", "", []byte{}).
							WithDescriptor("2901", 0xFF, 0xFE, 0xFD) // Invalid UTF-8 sequence
					},
					Name:   "User Description invalid UTF-8 bytes",
					Script: dumpFirstDescriptor("1234", "5678"),
					JsonOutput: `{
				   "value":"FFFEFD",
				   "uuid":"2901",
				   "parsed_value":{
					  "error":"parse_error: invalid UTF-8 in user description"
				   },
				   "name":"Characteristic User Descriptor",
				   "handle":3,
				   "index":0
				}`,
				},
				TestCase{
					// GOAL: Verify that descriptor read timeouts are correctly represented
					//
					// TEST SCENARIO: Descriptor that times out during read → verify no value, parsed_value contains error

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "read", []byte{}).
							WithDescriptorReadTimeout("2901", []byte("Read-Timeout")...)
					},
					Name:   "Descriptor Read Timeout",
					Script: dumpFirstDescriptor("180d", "2a37"),
					JsonOutput: `{
				  "uuid" : "2901",
				  "handle" : 3,
				  "index" : 0,
				  "name" : "Characteristic User Descriptor",
				  "parsed_value" : {
					"error" : "read_timeout"
				  }
				}`,
				},
				TestCase{
					// GOAL: Verify that descriptor read errors are correctly represented
					//
					// TEST SCENARIO: Descriptor that fails to read → verify no value, parsed_value contains error

					Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
						builder.
							WithService("180d").
							WithCharacteristic("2a37", "read", []byte{}).
							WithDescriptorReadError("2901")
					},
					Name: "Descriptor Read Error",
					Script: `
					local json = require("json")
					local char = blim.characteristic("180d", "2a37")
					local desc = char.descriptors[1]
					print(json.encode(desc))
				`,
					JsonOutput: `{
				   "uuid":"2901",
				   "handle":3,
				   "index":0,
				   "parsed_value":{
					  "error": "read_error: permission denied"
				   },
				   "name": "Characteristic User Descriptor"
				}`,
				},
			)
		})
	})

	suite.Run("OS X specific test", func() {
		ExecuteScenarios(
			func(tc TestCase) *LuaTestableDevice {
				if tc.Peripheral != nil {
					suite.GivenPeripheral(tc.Peripheral)
				}
				return suite.Connect("00:00:00:00:00:01")
			},
			TestCase{
				// On Darwin/macOS, the BLE library does not populate descriptor Handle fields, which means descriptors cannot be read
				//
				// GOAL: Ensure each descriptor has a hndle and an index, which are unique and increasing across all descriptors,
				//       even when a characteristic has multiple descriptors with the same UUID
				//
				// TEST SCENARIO: Read characteristic with multiple descriptors → verify index field → verify sequential values starting from 0

				Name: "Index Field: Sequential Numbering",
				Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
					builder.
						WithService("180d").
						WithCharacteristic("2a37", "read", []byte{}).
						WithDescriptor("2902", 0x01, 0x00).
						WithDescriptor("2901", 0x48, 0x65, 0x61, 0x72, 0x74, 0x20, 0x52, 0x61, 0x74, 0x65).
						WithDescriptor("2902", 0x04, 0x2).
						WithDescriptor("2901", 12, 21).
						WithDescriptor("2900", 0x00, 0x00)
				},
				Script: `
					-- Track last seen descriptor handle and index globally
					local last_desc_handle = -1
					local last_desc_index = -1
					local used_desc_handles = {}
					local used_desc_indices = {}
					
					-- Get the list of services
					local services_table = blim.list()
					
					-- Iterate over all services
					for _, service_uuid in pairs(services_table) do
						local service_info = services_table[service_uuid]
						if service_info and service_info.characteristics then
							for _, char_uuid in ipairs(service_info.characteristics) do
								local char_info = blim.characteristic(service_uuid, char_uuid) or {}
					
								-- Only check if descriptors exist
								if char_info.descriptors and #char_info.descriptors > 0 then
									-- Sort descriptors by handle to reflect BLE GATT order
									table.sort(char_info.descriptors, function(a, b)
										return (a.handle or 0) < (b.handle or 0)
									end)
					
									for _, desc in ipairs(char_info.descriptors) do
										-- --- HANDLE CHECK ---
										if not desc.handle then
											error("Descriptor of " .. char_uuid .. " missing handle")
										end
										if type(desc.handle) ~= "number" then
											error("Descriptor of " .. char_uuid .. " handle is not a number")
										end
										if used_desc_handles[desc.handle] then
											error("Descriptor handle " .. desc.handle .. " is duplicated")
										end
										if desc.handle <= last_desc_handle then
											error("Descriptor handle " .. desc.handle ..
												  " is not greater than previous handle " .. last_desc_handle)
										end
										used_desc_handles[desc.handle] = true
										last_desc_handle = desc.handle
					
										-- --- INDEX CHECK (optional) ---
										if desc.index then
											if type(desc.index) ~= "number" then
												error("Descriptor of " .. char_uuid .. " index is not a number")
											end
											if used_desc_indices[desc.index] then
												error("Descriptor index " .. desc.index .. " is duplicated")
											end
											if desc.index <= last_desc_index then
												error("Descriptor index " .. desc.index ..
													  " is not greater than previous index " .. last_desc_index)
											end
											used_desc_indices[desc.index] = true
											last_desc_index = desc.index
										end
									end
								end
							end
						end
					end
					
					print("All descriptor handles and indices (if present) are unique and strictly increasing")
				`,
				Output: "All descriptor handles and indices (if present) are unique and strictly increasing",
			},
		)
	})

	suite.Run("TestDescriptorReadErrors", func() {
		//dumpAppDescriptor := func(service string, char string) string {
		//	return fmt.Sprintf(`
		//			local json = require("json")
		//			local char = blim.characteristic("%s", "%s")
		//			local desc = char.descriptors[1]
		//			print(json.encode(desc))
		//		`, service, char)
		//}

		testScript := `
			local c = blim.characteristic("1800", "2a01")
			assert(c ~= nil, "appearance characteristic MUST exist")
			
			local v, err = c:read()
			if err ~= nil then
				error(err)
			end

			
			
			local pv
			if c.has_parser then
				pv = c:parse(v)
			end
			
			v = blim.to_little_endian_bytes(v)
			char = {
				characteristic = c,
				value = blim.bytes_to_hex(v),
				parsed_value = pv
			}

		` + dumpChar("1800", "2a01", "properties", "requires_authentication", "descriptors")

		ExecuteScenarios(
			func(tc TestCase) *LuaTestableDevice {
				if tc.Peripheral != nil {
					suite.GivenPeripheral(tc.Peripheral)
				}
				return suite.Connect("00:00:00:00:00:01")
			},
			TestCase{
				Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
					builder.
						WithService("1800").
						WithCharacteristic("2a01", "read", []byte{0x40, 0x00})
				},
				Name:   "Appearance: Known Value - Phone (0x0040)",
				Script: testScript,
				JsonOutput: `{
					"characteristic": {
						"name": "Appearance",
						"uuid": "2a01",
						"service": "1800",
						"has_parser": true
					},
					"value": "0040",
					"parsed_value": "Phone"
				}`,
			},
		)
	})
}

// TestAppearanceCharacteristic validates appearance characteristic (0x2A01 in GAP service 0x1800) access and parsing via Lua API.
//
// GOAL: Verify appearance characteristic (0x2A01 in GAP service 0x1800) access and parsing via Lua API
//
// TEST SCENARIO: Access appearance characteristic → verify metadata (UUID, name), value read/parsing (little-endian uint16), and graceful degradation for unknown values
//
// DEPENDENCY: This test requires Test000_CharacteristicParserAPI_Prerequisite to pass first
func (suite *LuaApiTestSuite) TestAppearanceCharacteristic() {
	if !suite.parserAPITestPassed {
		suite.T().Skip("Skipping TestAppearanceCharacteristic: prerequisite Test000_CharacteristicParserAPI_Prerequisite did not pass")
		return
	}

	// Scenario struct to reduce boilerplate - contains only variable data
	type scenario struct {
		name            string
		appearanceValue []byte // Little-endian uint16 value
		expectedJson    string
	}

	scenarios := []scenario{
		{
			name:            "Appearance: Known Value - Phone (0x0040)",
			appearanceValue: []byte{0x40, 0x00},
			expectedJson: `{
				"characteristic": {
					"name": "Appearance",
					"uuid": "2a01",
					"service": "1800",
					"has_parser": true
				},
				"value": "0040",
				"parsed_value": "Phone"
			}`,
		},
		{
			name:            "Appearance: Known Value - Computer (0x0080)",
			appearanceValue: []byte{0x80, 0x00},
			expectedJson: `{
				"characteristic": {
					"name": "Appearance",
					"uuid": "2a01",
					"service": "1800",
					"has_parser": true
				},
				"value": "0080",
				"parsed_value": "Computer"
			}`,
		},
		{
			name:            "Appearance: Known Value with Subcategory - Computer Desktop Workstation (0x0081)",
			appearanceValue: []byte{0x81, 0x00},
			expectedJson: `{
				"characteristic": {
					"name": "Appearance",
					"uuid": "2a01",
					"service": "1800",
					"has_parser": true
				},
				"value": "0081",
				"parsed_value": "Computer: Desktop Workstation"
			}`,
		},
		{
			name:            "Appearance: Unknown Value degradation",
			appearanceValue: []byte{0xFF, 0xFF},
			expectedJson: `{
				"characteristic": {
					"name": "Appearance",
					"uuid": "2a01",
					"service": "1800",
					"has_parser": true
				},
				"value": "FFFF",
				"parsed_value": null
			}`,
		},
		{
			name:            "Appearance: Zero Value Degradation (0x0000)",
			appearanceValue: []byte{0x00, 0x00},
			expectedJson: `{
				"characteristic": {
					"name": "Appearance",
					"uuid": "2a01",
					"service": "1800",
					"has_parser": true
				},
				"value": "0000",
				"parsed_value": null
			}`,
		},
	}

	testScript := `
			local c = blim.characteristic("1800", "2a01")
			assert(c ~= nil, "appearance characteristic MUST exist")
			
			local v, err = c:read()
			if err ~= nil then
				error(err)
			end

			local pv
			if c.has_parser then
				pv = c:parse(v)
			end
			
			v = blim.to_little_endian_bytes(v)
			char = {
				characteristic = c,
				value = blim.bytes_to_hex(v),
				parsed_value = pv
			}

		` + dumpChar("1800", "2a01", "properties", "requires_authentication", "descriptors")

	for _, tc := range scenarios {
		// Capture scenario for closure
		scenario := tc

		ExecuteScenarios(
			func(tc TestCase) *LuaTestableDevice {
				if tc.Peripheral != nil {
					suite.GivenPeripheral(tc.Peripheral)
				}
				return suite.Connect("00:00:00:00:00:01")
			},
			TestCase{
				Peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
					builder.
						WithService("1800").
						WithCharacteristic("2a01", "read", tc.appearanceValue)
				},
				Name:       tc.name,
				Script:     testScript,
				JsonOutput: scenario.expectedJson,
			},
		)
	}
}

// Test000_CharacteristicParserAPI_Prerequisite validates has_parser field and parse() method behavior across all scenarios.
// This test runs FIRST (alphabetically) and MUST pass before TestAppearanceCharacteristic runs.
//
// GOAL: Verify has_parser field and the parse() method work correctly for characteristics with/without parsers and handle edge cases
//
// TEST SCENARIO: Test multiple scenarios → verify has_parser field → verify parse field/method → verify parsing behavior → verify error handling
func (suite *LuaApiTestSuite) Test000_CharacteristicParserAPI_Prerequisite() {
	type scenario struct {
		name        string
		serviceUUID string
		charUUID    string
		charValue   []byte
		testScript  string
	}

	scenarios := []scenario{
		// Subtest group: Characteristics WITH parser (Appearance)
		{
			name:        "Known Value - Parser exists, returns string",
			serviceUUID: "1800",
			charUUID:    "2a01",
			charValue:   []byte{0x40, 0x00}, // Phone
			testScript: `
				local char = blim.characteristic("1800", "2a01")
				assert(char ~= nil, "characteristic MUST exist")

				-- Verify has_parser is true
				assert(char.has_parser == true, string.format("has_parser MUST be true for Appearance, got: %s", tostring(char.has_parser)))

				-- Verify parse exists and is callable
				assert(char.parse ~= nil, "parse MUST exist when has_parser is true")
				assert(type(char.parse) == "function" or type(char.parse) == "userdata", "parse MUST be callable")

				-- Parse the value
				local value, err = char:read()
				assert(err == nil, string.format("read MUST succeed, got error: %s", tostring(err)))

				local parsed = char:parse(value)
				assert(parsed == "Phone", string.format("parse MUST return 'Phone', got: %s", tostring(parsed)))
			`,
		},
		{
			name:        "Unknown Value - Parser exists, returns nil",
			serviceUUID: "1800",
			charUUID:    "2a01",
			charValue:   []byte{0xFF, 0xFF}, // Unknown
			testScript: `
				local char = blim.characteristic("1800", "2a01")
				assert(char ~= nil, "characteristic MUST exist")

				-- Verify has_parser is true
				assert(char.has_parser == true, "has_parser MUST be true for Appearance")

				-- Verify parse exists
				assert(char.parse ~= nil, "parse MUST exist when has_parser is true")

				-- Parse unknown value returns nil
				local value, err = char:read()
				assert(err == nil, "read MUST succeed")

				local parsed = char:parse(value)
				assert(parsed == nil, string.format("parse MUST return nil for unknown value, got: %s", tostring(parsed)))
			`,
		},

		// Subtest group: Characteristics WITHOUT parser
		{
			name:        "Battery Level - No parser, has_parser=false, parse=nil",
			serviceUUID: "180f",
			charUUID:    "2a19",
			charValue:   []byte{0x64}, // 100%
			testScript: `
				local char = blim.characteristic("180f", "2a19")
				assert(char ~= nil, "characteristic MUST exist")

				-- CRITICAL: Verify has_parser is false
				assert(char.has_parser == false, string.format("has_parser MUST be false for Battery Level, got: %s", tostring(char.has_parser)))

				-- CRITICAL: Verify parse is nil when has_parser is false
				assert(char.parse == nil, string.format("parse MUST be nil when has_parser is false, got: %s", type(char.parse)))

				-- Read should still work
				local value, err = char:read()
				assert(err == nil, "read MUST succeed")
				assert(value == "\x64", "value MUST be 0x64")
			`,
		},
		{
			name:        "Custom characteristic - No parser, has_parser=false, parse=nil",
			serviceUUID: "12345678-1234-1234-1234-123456789abc",
			charUUID:    "87654321-4321-4321-4321-cba987654321",
			charValue:   []byte{0x01, 0x02, 0x03},
			testScript: `
				local char = blim.characteristic("12345678-1234-1234-1234-123456789abc", "87654321-4321-4321-4321-cba987654321")
				assert(char ~= nil, "characteristic MUST exist")

				-- Verify has_parser is false for custom characteristic
				assert(char.has_parser == false, "has_parser MUST be false for custom characteristic")

				-- Verify parse is nil
				assert(char.parse == nil, "parse MUST be nil when has_parser is false")

				-- Read should work
				local value, err = char:read()
				assert(err == nil, "read MUST succeed")
			`,
		},

		// Subtest group: Edge cases - graceful degradation
		{
			name:        "Edge case - parse() with malformed data (1 byte instead of 2)",
			serviceUUID: "1800",
			charUUID:    "2a01",
			charValue:   []byte{0x40, 0x00},
			testScript: `
				local char = blim.characteristic("1800", "2a01")
				assert(char ~= nil, "characteristic MUST exist")
				assert(char.has_parser == true, "has_parser MUST be true")

				-- Parse with malformed data (1 byte instead of 2)
				local parsed = char:parse("\x40")
				assert(parsed == nil, "parse MUST return nil for malformed data (wrong length)")
			`,
		},
		{
			name:        "Edge case - parse() with empty string",
			serviceUUID: "1800",
			charUUID:    "2a01",
			charValue:   []byte{0x40, 0x00},
			testScript: `
				local char = blim.characteristic("1800", "2a01")
				assert(char ~= nil, "characteristic MUST exist")

				-- Parse with empty string
				local parsed = char:parse("")
				assert(parsed == nil, "parse MUST return nil for empty string")
			`,
		},
		{
			name:        "Edge case - parse() with 3 bytes instead of 2",
			serviceUUID: "1800",
			charUUID:    "2a01",
			charValue:   []byte{0x40, 0x00},
			testScript: `
				local char = blim.characteristic("1800", "2a01")
				assert(char ~= nil, "characteristic MUST exist")

				-- Parse with 3 bytes instead of 2
				local parsed = char:parse("\x40\x00\xFF")
				assert(parsed == nil, "parse MUST return nil for malformed data (wrong length)")
			`,
		},
	}

	for _, tc := range scenarios {
		scenario := tc

		suite.Run(scenario.name, func() {
			// Configure peripheral for this subtest
			suite.GivenPeripheral(func(b *testutils.PeripheralDeviceBuilder) {
				b.WithService(scenario.serviceUUID).
					WithCharacteristic(scenario.charUUID, "read", scenario.charValue)
			})

			suite.Connect("1").FluentLuaTest().
				// Test scenario should pass
				MustExecuteScript(scenario.testScript)
		})
	}

	// All subtests passed - set flag so dependent device_test can run
	suite.parserAPITestPassed = true
}

// TestManufacturerData device_test manufacturer_data field exposure and parsing via Lua API
func (suite *LuaApiTestSuite) TestManufacturerData() {
	// GOAL: Verify manufacturer_data field exposure and parsing for various scenarios
	//
	// TEST SCENARIO: Test multiple manufacturer data scenarios → verify field structure → verify parsing → verify graceful degradation

	// Local helper: create test advertisement with manufacturer data
	createTestAd := func(manufData []byte) device.Advertisement {
		return testutils.NewAdvertisementBuilder().
			WithAddress("00:00:00:00:00:01").
			WithName("TestDevice").
			WithRSSI(-50).
			WithConnectable(true).
			WithManufacturerData(manufData).
			WithServices().
			WithNoServiceData().
			WithTxPower(0).
			Build()
	}

	tests := []struct {
		name         string
		manufData    []byte
		testScript   string
		testGoal     string
		testScenario string
	}{
		{
			name:         "NoData",
			manufData:    nil,
			testGoal:     "Verify manufacturer_data is nil when device has no manufacturer data",
			testScenario: "Device without manufacturer data → blim.device.manufacturer_data is nil → verified",
			testScript: `
				assert(blim.device ~= nil, "blim.device MUST exist")
				assert(blim.device.manufacturer_data == nil, "manufacturer_data MUST be nil when absent")
			`,
		},
		{
			name:         "WithValue",
			manufData:    []byte{0xFE, 0xFF, 0x01, 0x10, 0x02, 0x01, 0x03},
			testGoal:     "Verify manufacturer_data contains a value field with raw hex data",
			testScenario: "Device with raw manufacturer data → manufacturer_data.value contains hex string → verified",
			testScript: `
				assert(blim.device.manufacturer_data ~= nil, "manufacturer_data MUST be present")
				assert(type(blim.device.manufacturer_data) == "table", "manufacturer_data MUST be table")

				assert(blim.device.manufacturer_data.value ~= nil, "value field MUST exist")
				assert(type(blim.device.manufacturer_data.value) == "string", "value MUST be string")
				assert(blim.device.manufacturer_data.value == "FEFF01100201" .. "03", "value MUST match hex data")
			`,
		},
		{
			name:         "BLIMCoIMU",
			manufData:    []byte{0xFE, 0xFF, 0x01, 0x10, 0x02, 0x01, 0x03},
			testGoal:     "Verify parsed_value contains vendor info and BLIMCo IMU Streamer fields",
			testScenario: "BLIMCo IMU device with manufacturer data → parsed_value populated → vendor and device fields verified",
			testScript: `
				assert(blim.device.manufacturer_data ~= nil, "manufacturer_data MUST be present")
				assert(blim.device.manufacturer_data.parsed_value ~= nil, "parsed_value MUST exist for BLIMCo")

				local parsed = blim.device.manufacturer_data.parsed_value
				assert(type(parsed) == "table", "parsed_value MUST be table")

				-- Verify vendor info
				assert(parsed.vendor ~= nil, "vendor field MUST exist")
				assert(type(parsed.vendor) == "table", "vendor MUST be table")
				assert(parsed.vendor.id == 0xFFFE, "vendor.id MUST be 0xFFFE")
				assert(parsed.vendor.name == "BLIMCo", "vendor.name MUST be BLIMCo")

				-- Verify BLIMCo-specific fields
				assert(parsed.device_type == "IMU Streamer", "device_type MUST be IMU Streamer")
				assert(parsed.hardware_version == "1.0", "hardware_version MUST be 1.0")
				assert(parsed.firmware_version == "2.1.3", "firmware_version MUST be 2.1.3")
			`,
		},
		{
			name:         "BLIMCoTestDevice",
			manufData:    []byte{0xFE, 0xFF, 0x00, 0x20, 0x01, 0x00, 0x00},
			testGoal:     "Verify parsed_value correctly identifies BLIMCo BLE Test Device",
			testScenario: "BLE Test Device manufacturer data → device_type field shows correct type → verified",
			testScript: `
				assert(blim.device.manufacturer_data ~= nil, "manufacturer_data MUST be present")
				assert(blim.device.manufacturer_data.parsed_value ~= nil, "parsed_value MUST exist")

				local parsed = blim.device.manufacturer_data.parsed_value

				-- Verify vendor
				assert(parsed.vendor.id == 0xFFFE, "vendor.id MUST be 0xFFFE")
				assert(parsed.vendor.name == "BLIMCo", "vendor.name MUST be BLIMCo")

				-- Verify different device type and versions
				assert(parsed.device_type == "BLE Test Device", "device_type MUST be BLE Test Device")
				assert(parsed.hardware_version == "2.0", "hardware_version MUST be 2.0")
				assert(parsed.firmware_version == "1.0.0", "firmware_version MUST be 1.0.0")
			`,
		},
		{
			name:         "Unknown",
			manufData:    []byte{0x34, 0x12, 0xAA, 0xBB, 0xCC},
			testGoal:     "Verify unknown manufacturer data has value but no parsed_value",
			testScenario: "Unknown manufacturer data → value field exists → parsed_value is nil → verified",
			testScript: `
				assert(blim.device.manufacturer_data ~= nil, "manufacturer_data MUST be present")
				assert(blim.device.manufacturer_data.value == "3412AABBCC", "value MUST contain raw hex")
				assert(blim.device.manufacturer_data.parsed_value == nil, "parsed_value MUST be nil for unknown manufacturer")
			`,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {

			var ft *FluentLuaTest

			// Rebuild peripheral with manufacturer data if needed
			if tt.manufData != nil {
				// Create advertisement with manufacturer data
				adv := createTestAd(tt.manufData)

				// Rebuild peripheral with manufacturer data
				suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
					builder.
						WithScanAdvertisements().
						WithAdvertisements(adv).
						Build().
						Build()

				})

				ft = suite.Connect("1").FluentLuaTest()

				// Update the device with advertisement data BEFORE creating the LuaAPI
				ft.device.Update(adv)
			}

			if ft == nil {
				ft = suite.Connect("1").FluentLuaTest()
			}

			// Execute test script
			// Lua script MUST execute without errors
			ft.MustExecuteScript(tt.testScript)
		})
	}
}

// TestSleepReleasesLuaStateMutex verifies that blim.sleep() releases the Lua state mutex,
// allowing subscription callbacks to execute during the sleep period.
func (suite *LuaApiTestSuite) TestSleepReleasesLuaStateMutex() {
	// GOAL: Verify blim.sleep() releases Lua state mutex to allow callbacks during sleep
	//
	// TEST SCENARIO: Start delayed notification → setup subscription + sleep(100ms) → notification arrives at 50ms → callback executes during sleep → verified

	suite.Connect("01").FluentLuaTest().
		EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
			time.Sleep(50 * time.Millisecond)
			emitter.
				WithService("1234").
				WithCharacteristic("5678", 0x42).
				Emit(true)
		}).

		// NOTE: This test assumes that subscription setup completes in <50ms. The goroutine injects
		// a notification at 50ms, which must arrive during the 100ms sleep window. If subscription
		// setup becomes slow, increase the delays proportionally.
		MustExecuteScript(`
			callback_received = false
	
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function(record)
					callback_received = true
				end
			}

			-- Sleep for 100ms; notification arrives at 50ms
			-- Sleep should release mutex allowing callback execution
			blim.sleep(100)
	
			-- Callback MUST have been invoked during sleep (proves mutex was released)
			assert(callback_received == true, "callback MUST be invoked during blim.sleep() (internal lua state mutex must be released durign sleep)")
		`)
}

func (suite *LuaApiTestSuite) TestSubscribersDoNotCompeteForNotifications() {
	// GOAL: Verify multiple subscriptions to the same characteristic each receives ALL notifications (broadcast)
	//
	// TEST SCENARIO: Two subscriptions to char 5678 → 4 notifications (0,1,2,3) → BOTH receive all 4

	suite.Connect("01").FluentLuaTest().
		EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
			time.Sleep(50 * time.Millisecond)
			for i := 0; i < 4; i++ {
				emitter.
					WithService("1234").
					WithCharacteristic("5678", byte(i)).
					Emit(true)
			}
		}).
		MustExecuteScript(`
			vals_A, vals_B = {}, {}
			blim.subscribe{
				services = {{ service = "1234", chars = {"5678"} }},
				Mode = "EveryUpdate",
				Callback = function(r) table.insert(vals_A, string.byte(r.Values["5678"])) end
			}
			blim.subscribe{
				services = {{ service = "1234", chars = {"5678"} }},
				Mode = "EveryUpdate",
				Callback = function(r) table.insert(vals_B, string.byte(r.Values["5678"])) end
			}
			blim.sleep(200)
	
			-- Verify broadcast: both receive all 4 values
			assert(#vals_A == 4, "Subscription A MUST receive all 4 values, got " .. #vals_A)
			assert(#vals_B == 4, "Subscription B MUST receive all 4 values, got " .. #vals_B)
	
			-- Verify correctness: each subscription received all values 0,1,2,3
			local function verify_all_values(vals, name)
				local seen = {}
				for _, v in ipairs(vals) do seen[v] = true end
				for i = 0, 3 do
					assert(seen[i], name .. " MUST have value " .. i)
				end
			end
			verify_all_values(vals_A, "A")
			verify_all_values(vals_B, "B")
		`)
}

// TestSubscriptionCancellation verifies subscription cleanup at the Go level.
// These tests validate white-box behavior: that cancel() properly cleans up Go-level resources
// (BLE connection subscriptions, callback refs) - not just Lua behavior.
func (suite *LuaApiTestSuite) TestSubscriptionCancellation() {
	suite.Run("External cancel verifies Go-level cleanup", func() {
		// GOAL: Verify cancel() triggers explicit cancellation at BLE connection level
		//
		// TEST SCENARIO: Subscribe → cancel → verify Diag.CancelReason == Explicit and CleanedUp == true

		ft := suite.Connect("01").FluentLuaTest()
		api := ft.LuaAPI()
		bleConn := getBLEConnection(api)

		ft.MustExecuteScript(`
			cancel = blim.subscribe{
				services = {{ service = "1234", chars = {"5678"} }},
				Mode = "EveryUpdate",
				Callback = function(r) end
			}
		`)

		// Verify subscription tracked
		suite.Equal(1, len(bleConn.ConnDiag.Subscriptions))
		sub := bleConn.ConnDiag.Subscriptions[0]

		ft.MustExecuteScript(`cancel()`)

		// Wait for cleanup goroutine
		time.Sleep(50 * time.Millisecond)

		// Verify Go-level cancellation
		suite.Equal(goble.CancelReasonExplicit, sub.Diag.CancelReason)
		suite.True(sub.Diag.CleanedUp())

		// Verify Lua callback ref released
		api.callbackRefsMu.Lock()
		suite.Equal(0, len(api.subscriptionCallbackRefs))
		api.callbackRefsMu.Unlock()
	})

	suite.Run("Self-cancel from callback verifies Go-level cleanup", func() {
		// GOAL: Verify self-cancellation from callback triggers explicit cancel at BLE level
		//
		// TEST SCENARIO: Subscribe with self-cancel → emit notification → verify explicit cancel

		ft := suite.Connect("01").FluentLuaTest()
		api := ft.LuaAPI()
		bleConn := getBLEConnection(api)

		ft.EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
			time.Sleep(50 * time.Millisecond)
			emitter.WithService("1234").WithCharacteristic("5678", 0x01).Emit(true)
		})

		ft.MustExecuteScript(`
			blim.subscribe{
				services = {{ service = "1234", chars = {"5678"} }},
				Mode = "EveryUpdate",
				Callback = function(record, cancel)
					cancel()  -- Self-cancel on first notification
				end
			}
			blim.sleep(100)
		`)

		// Verify Go-level cancellation
		suite.Equal(1, len(bleConn.ConnDiag.Subscriptions))
		sub := bleConn.ConnDiag.Subscriptions[0]
		suite.Equal(goble.CancelReasonExplicit, sub.Diag.CancelReason)
		suite.True(sub.Diag.CleanedUp())
	})

	suite.Run("Cancel is idempotent", func() {
		// GOAL: Verify multiple cancel() calls don't crash or double-release
		//
		// TEST SCENARIO: Subscribe → cancel 3 times → no panic, still shows explicit cancel

		ft := suite.Connect("01").FluentLuaTest()
		api := ft.LuaAPI()
		bleConn := getBLEConnection(api)

		ft.MustExecuteScript(`
			cancel = blim.subscribe{
				services = {{ service = "1234", chars = {"5678"} }},
				Mode = "EveryUpdate",
				Callback = function(r) end
			}
			cancel()
			cancel()
			cancel()
		`)

		time.Sleep(50 * time.Millisecond)

		// Verify single explicit cancel recorded
		sub := bleConn.ConnDiag.Subscriptions[0]
		suite.Equal(goble.CancelReasonExplicit, sub.Diag.CancelReason)
		suite.True(sub.Diag.CleanedUp())

		// Verify callback ref released exactly once
		api.callbackRefsMu.Lock()
		suite.Equal(0, len(api.subscriptionCallbackRefs))
		api.callbackRefsMu.Unlock()
	})

	suite.Run("Stress test: no goroutine leaks after repeated subscribe/cancel cycles", func() {
		// GOAL: Verify no goroutine leaks after many subscribe/cancel cycles
		//
		// TEST SCENARIO: 100 iterations of (10x subscribe, emit data, 10x cancel)
		// Then verify goroutine count returns to baseline

		ft := suite.Connect("01").FluentLuaTest()

		// Force GC and stabilize goroutine count
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		initialGoroutines := runtime.NumGoroutine()

		const iterations = 100
		const subscriptionsPerIteration = 10

		for i := 0; i < iterations; i++ {
			// Start background data emission for this iteration
			ft.EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
				time.Sleep(10 * time.Millisecond)
				for j := 0; j < subscriptionsPerIteration; j++ {
					emitter.WithService("1234").WithCharacteristic("5678", byte(j)).Emit(true)
				}
			})

			// Create 10 subscriptions and cancel them
			ft.MustExecuteScript(fmt.Sprintf(`
				local cancels = {}
				for i = 1, %d do
					cancels[i] = blim.subscribe{
						services = {{ service = "1234", chars = {"5678"} }},
						Mode = "EveryUpdate",
						Callback = function(r) end
					}
				end
				blim.sleep(20)  -- Allow some data to arrive
				for i = 1, %d do
					cancels[i]()
				end
			`, subscriptionsPerIteration, subscriptionsPerIteration))
		}

		// Stop the last collector goroutine (without closing the whole FluentLuaTest)
		if c := ft.Collector(); c != nil {
			_ = c.Stop()
		}

		// Allow cleanup goroutines to finish
		time.Sleep(1 * time.Second)
		runtime.GC()
		time.Sleep(200 * time.Millisecond)
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		finalGoroutines := runtime.NumGoroutine()
		leakedGoroutines := finalGoroutines - initialGoroutines

		// Dump named goroutines if leak detected
		const maxAllowedLeak = 0
		if leakedGoroutines > maxAllowedLeak {
			suite.T().Logf("Goroutines:\n%s", groutine.Dump())
		}

		suite.LessOrEqual(leakedGoroutines, maxAllowedLeak,
			"Goroutine leak detected after %d iterations: initial=%d, final=%d, leaked=%d",
			iterations, initialGoroutines, finalGoroutines, leakedGoroutines)
	})
}

// TestProtectedCall verifies blim.pcall — the sandbox substitute for the
// hidden Lua pcall (golua removes pcall/xpcall because their longjmp-based
// unwinding across active Go frames corrupts the Go stack). blim.pcall is
// LuaJIT's native pcall: it catches Lua/C errors; errors raised by Go-backed
// functions are Go panics and abort the script by design.
func (suite *LuaApiTestSuite) TestProtectedCall() {
	suite.Run("success passes through all results", func() {
		// GOAL: Verify a protected call reports success and forwards every
		//       return value of the wrapped function unchanged.
		//
		// TEST SCENARIO: protected call of a multi-result function → true plus all results → values match

		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
				local ok, a, b, c = blim.pcall(function(x) return x, x + 1, "z" end, 1)
				assert(ok == true, "protected call MUST succeed")
				assert(a == 1 and b == 2 and c == "z", "all results MUST pass through unchanged")
			`)
	})

	suite.Run("Lua error is caught as a value", func() {
		// GOAL: Verify an error raised by plain Lua code inside a protected
		//       call is reported as a return value instead of aborting the
		//       script.
		//
		// TEST SCENARIO: protected call of an erroring function → false plus message → script continues running

		suite.Connect("1").FluentLuaTest().
			MustExecuteScript(`
				local ok, err = blim.pcall(function() error("boom") end)
				assert(ok == false, "protected call MUST report failure")
				assert(tostring(err):find("boom"), "error message MUST be preserved, got: " .. tostring(err))
			`)
	})

	suite.Run("error from a Go-backed function aborts the script", func() {
		// GOAL: Pin the documented blim.pcall limitation: errors raised by
		//       Go-backed functions are Go-side panics that bypass the native
		//       pcall — the script aborts with the original error, and the
		//       engine survives it (the same path as any unprotected script
		//       error).
		//
		// TEST SCENARIO: protected call of require with a missing module → script aborts with the require error → engine still runs the next script

		suite.Connect("1").FluentLuaTest().
			FailExecuteScript("no_such_module", `
				blim.pcall(require, "no_such_module")
			`).
			MustExecuteScript(`
				assert(blim.pcall(function() return true end) == true,
					"engine MUST stay healthy after an uncaught Go-side error")
			`)
	})
}

// TestSubscribeParsesDrainDuration is a white-box regression test for the
// DrainDuration subscription option, which was silently dropped: the parser
// checked IsNumber on the pushed key instead of the table value, so any
// DrainDuration a script passed was ignored and drain stayed disabled.
//
// The value is asserted at the parse boundary (parseSubscriptionTable) rather
// than end-to-end: drainDuration is consumed below the ble.Client mock (in the
// real BLEConnection.Subscribe) and its effect is timing-dependent, so the
// deterministic pin is that the parsed config carries the requested value.
func (suite *LuaApiTestSuite) TestSubscribeParsesDrainDuration() {
	// GOAL: Verify blim.subscribe reads the DrainDuration option from the Lua
	//       table, and defaults it to 0 when omitted.
	//
	// TEST SCENARIO: parse a table with DrainDuration=250 → config carries 250 → omitting it yields 0

	api := suite.Connect("00:00:00:00:00:01").FluentLuaTest().LuaAPI()

	parseDrain := func(fields string) int {
		var got int
		api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
			suite.Require().Zero(L.DoString(`return {
				services = {{ service = "1234", chars = {"5678"} }},
				Callback = function() end,
				`+fields+`
			}`), "table snippet MUST load")
			cfg, err := api.parseSubscriptionTable(L, L.GetTop())
			suite.Require().NoError(err, "parse MUST succeed")
			got = cfg.DrainDuration
			L.Pop(1)
			return nil
		})
		return got
	}

	suite.Equal(250, parseDrain("DrainDuration = 250"),
		"DrainDuration MUST be read from the table")
	suite.Equal(0, parseDrain(""),
		"omitted DrainDuration MUST default to 0")
}

// TestLuaAPITestSuite runs the test suite using testify/suite
func TestLuaAPITestSuite(t *testing.T) {
	suitelib.Run(t, new(LuaApiTestSuite))
}
