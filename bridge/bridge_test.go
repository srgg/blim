//go:build test

package bridge

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/lua"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

const (
	// testSyncWait is the delay to allow async operations to complete in device_test
	testSyncWait = 100 * time.Millisecond

	// maxShutdownDuration is the maximum acceptable time for bridge shutdown
	maxShutdownDuration = 1 * time.Second

	// signalTestTimeout is the timeout for signal handling test (uses timeout to simulate Ctrl+C)
	signalTestTimeout = 2 * time.Second
)

// BridgeTestSuite runs device_test for Bridge using the BridgeSuite infrastructure.
type BridgeTestSuite struct {
	lua.LuaApiSuite
}

// Connect overrides LuaApiSuite.Connect to return BridgeTestableDevice.
// Cleanup is auto-registered with t.Cleanup().
func (suite *BridgeTestSuite) Connect(addr string) *BridgeTestableDevice {
	return suite.ConnectWithContext(context.Background(), addr)
}

// ConnectWithContext connects with a custom context for bridge operations.
// The context is passed through to FluentBridgeTest for use in MustBridgeCallbackRun/FailBridgeCallbackRun.
func (suite *BridgeTestSuite) ConnectWithContext(ctx context.Context, addr string) *BridgeTestableDevice {
	ltd := suite.LuaApiSuite.Connect(addr)
	btd := &BridgeTestableDevice{LuaTestableDevice: ltd, ctx: ctx}
	suite.T().Cleanup(btd.cleanup())
	return btd
}

// ConnectDeviceWithContext overrides LuaApiSuite.ConnectDeviceWithContext to return BridgeTestableDevice.
// The context is passed through to FluentBridgeTest for use in MustBridgeCallbackRun/FailBridgeCallbackRun.
func (suite *BridgeTestSuite) ConnectDeviceWithContext(ctx context.Context, addr string) *BridgeTestableDevice {
	return suite.ConnectWithContext(ctx, addr)
}

func (suite *BridgeTestSuite) TestBridgeScenarios() {
	defaultPeripheral := func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			WithService("1234").
			WithCharacteristic("5678", "notify", []byte{0x01, 0x02}).
			WithService("180d"). // Heart Rate
			WithCharacteristic("2a37", "notify", nil).
			WithCharacteristic("2a38", "notify", nil).
			WithService("180f"). // Battery Service
			WithCharacteristic("2a19", "notify", nil).
			WithService("6e400001b5a3f393e0a9e50e24dcca9e"). // Nordic UART
			WithCharacteristic("6e400002b5a3f393e0a9e50e24dcca9e", "notify", nil).
			WithCharacteristic("6e400003b5a3f393e0a9e50e24dcca9e", "notify", nil)
	}

	defaultSubscribe := []*device.SubscribeOptions{
		{Service: "1234", Characteristics: []string{"5678"}},
	}

	defaultEmit := func(emitter *testutils.PeripheralDataEmitter) {
		time.Sleep(testSyncWait)
		emitter.
			WithService("1234").
			WithCharacteristic("5678", 0x01, 0x02).
			Emit(true)
	}

	expectedDefaultBridgeScriptTemplate := `=== BLE-PTY Bridge is Active ===
Device: {{.DeviceAddress}}
TTY: {{.TTY}}
Service: 1234
Characteristics: 1
  - 5678
Service: 180d
Characteristics: 2
  - 2a37
  - 2a38
Service: 180f
Characteristics: 1
  - 2a19
Service: 6e400001b5a3f393e0a9e50e24dcca9e
Characteristics: 2
  - 6e400002b5a3f393e0a9e50e24dcca9e
  - 6e400003b5a3f393e0a9e50e24dcca9e
Bridge is running. Press Ctrl+C to stop the bridge.
`

	tests := []struct {
		name            string
		peripheral      func(builder *testutils.PeripheralDeviceBuilder)
		subscribe       []*device.SubscribeOptions
		callback        string
		emitData        func(*testutils.PeripheralDataEmitter)
		luaStdError     string
		stdout          string
		script          string
		ptySlaveRx      string
		expectedRecords func(*lua.ExpectedRecordsBuilder) // nil = don't check records
	}{
		{
			// GOAL: Verify that Bridge2 properly captures Lua stderr errors via custom ErrorHandler
			//
			// TEST SCENARIO: Lua callback throws error → captured by customErrorHandler → appears in capturedErrors

			name:        "handle subscription callback runtime Error",
			peripheral:  defaultPeripheral,
			subscribe:   defaultSubscribe,
			emitData:    defaultEmit,
			callback:    `error("Intentional bridge test error in Lua callback")`,
			luaStdError: "Intentional bridge test error in Lua callback",
		},
		{
			// GOAL: Verify that calling undefined functions produces a proper error, not SIGSEGV
			//
			// TEST SCENARIO: Lua callback calls undefined function → error logged after each step

			name:        "handle undefined function call",
			peripheral:  defaultPeripheral,
			subscribe:   defaultSubscribe,
			emitData:    defaultEmit,
			callback:    "undefined_function(record)",
			luaStdError: "attempt to call global 'undefined_function' (a nil value)",
		},
		{
			// GOAL: Verify that Bridge captures Lua stdout as stdout
			//
			// TEST SCENARIO: Lua prints to stdout → captured by output collector → written to PTY slave
			name:       "bridge captures lua stdout",
			peripheral: defaultPeripheral,
			subscribe:  defaultSubscribe,
			emitData:   defaultEmit,
			callback:   `print("Battery notification received")`,
			stdout:     "Battery notification received\n",
		},
		{
			// GOAL: Verify that Bridge correctly separates stdout and stderr (to error handler)
			//
			// TEST SCENARIO: Lua script outputs both stdout and stderr → properly routed to respective handlers
			name:       "routes lua stdout_and_stderr",
			peripheral: defaultPeripheral,
			subscribe:  defaultSubscribe,
			emitData:   defaultEmit,
			callback: `
				print("Callback executed")
				io.stderr:write("Error from callback")
			`,
			luaStdError: "Error from callback",
			stdout:      "Callback executed\n",
		},
		{
			// GOAL: Verify a complete BLE notify → Lua callback path with multiple services and characteristics
			//
			// TEST SCENARIO: Multiple BLE notifications (text and binary) → JSON callbacks → verify callback structure and data
			name: "handle multiservice notification",
			peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("1234").
					WithCharacteristic("5678", "notify", []byte{}).
					WithCharacteristic("90ab", "notify", []byte{}).

					//
					WithService("180f").
					WithCharacteristic("2a19", "notify", []byte{})
			},
			subscribe: []*device.SubscribeOptions{
				{Service: "1234", Characteristics: []string{"5678", "90ab"}},
				{Service: "180f", Characteristics: []string{"2a19"}},
			},
			emitData: func(emitter *testutils.PeripheralDataEmitter) {
				time.Sleep(50 * time.Millisecond)
				emitter.
					WithService("1234").
					WithCharacteristic("5678", 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x20, 0x57, 0x6f, 0x72, 0x6c, 0x64, 0x21). // "Hello World!"
					WithCharacteristic("90ab", 0x42, 0x3A, 0x7D, 0x8E).                                                 // ASCII-safe binary
					//
					WithService("180f").
					WithCharacteristic("2a19", 0x42).
					//
					Emit(true)
			},
			//callback: USE DEFAULT callback to dump incoming data as JSON
			expectedRecords: func(b *lua.ExpectedRecordsBuilder) {
				b.IgnoreFields("TsUs", "callNo", "Seq").
					Record().Value("5678", 0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x20, 0x57, 0x6f, 0x72, 0x6c, 0x64, 0x21). // "Hello World!"
					Record().Value("90ab", 0x42, 0x3A, 0x7D, 0xEF, 0xBF, 0xBD).                                     // 0x8E becomes UTF-8 replacement char
					Record().Value("2a19", 0x42)
			},
		},
		{
			// GOAL: Verify non-UTF8 binary data is handled correctly through JSON encoding
			//
			// TEST SCENARIO: BLE notification with non-UTF8 bytes (0x80, 0x8E, 0xFF) → Lua json.encode →
			//                verify bytes are converted to UTF-8 replacement character (U+FFFD = 0xEF 0xBF 0xBD)
			//
			// WHY THIS MATTERS: BLE characteristics often contain raw binary sensor data that may include
			// any byte value (0x00-0xFF). Lua's json.encode treats strings as UTF-8, replacing invalid
			// byte sequences with the replacement character. Tests must account for this transformation.
			name: "handle non-UTF8 binary data in JSON callback",
			peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("1234").
					WithCharacteristic("5678", "notify", []byte{})
			},
			subscribe: []*device.SubscribeOptions{
				{Service: "1234", Characteristics: []string{"5678"}},
			},
			emitData: func(emitter *testutils.PeripheralDataEmitter) {
				time.Sleep(50 * time.Millisecond)
				emitter.
					WithService("1234").
					// Non-UTF8 bytes: 0x80-0xFF are invalid as standalone UTF-8 bytes
					WithCharacteristic("5678", 0x01, 0x80, 0x8E, 0xFF, 0x02).
					Emit(true)
			},
			expectedRecords: func(b *lua.ExpectedRecordsBuilder) {
				b.IgnoreFields("TsUs", "callNo", "Seq").
					// Each non-UTF8 byte (0x80, 0x8E, 0xFF) becomes 0xEF 0xBF 0xBD (replacement char)
					Record().Value("5678", 0x01, 0xEF, 0xBF, 0xBD, 0xEF, 0xBF, 0xBD, 0xEF, 0xBF, 0xBD, 0x02)
			},
		},
		{
			// GOAL: Verify that the default bridge script loads via file://,
			//       prints bridge status to stdout,
			//       and writes compact, human-readable BLE notifications to the PTY slave
			//       for consumption by external software.
			//
			// TEST SCENARIO: Load bridge.lua from file:// →
			/*                script auto-subscribes to all notifiable characteristics → */
			//
			//                prints bridge header (device, PTY, services) to stdout →
			//                BLE notifications received →
			//                formatted notification output written to PTY slave →
			//                verify header content and PTY notification format.
			//
			// NOTE: bridge.lua auto-discovers and subscribes to all notifiable characteristics.
			// NOTE: Validates stdout (bridge header) and PTY slave output (notifications via pty_write).
			name:   "default bridge script output",
			script: "file://../examples/bridge.lua",
			peripheral: func(builder *testutils.PeripheralDeviceBuilder) {
				builder.
					WithService("1234").
					WithCharacteristic("5678", "notify", []byte{}).
					WithService("180d"). // Heart Rate
					WithCharacteristic("2a37", "notify", nil).
					WithCharacteristic("2a38", "notify", nil).
					WithService("180f"). // Battery Service
					WithCharacteristic("2a19", "notify", nil).
					WithService("6e400001b5a3f393e0a9e50e24dcca9e"). // Nordic UART
					WithCharacteristic("6e400002b5a3f393e0a9e50e24dcca9e", "notify", nil).
					WithCharacteristic("6e400003b5a3f393e0a9e50e24dcca9e", "notify", nil)
			},
			emitData: func(emitter *testutils.PeripheralDataEmitter) {
				time.Sleep(50 * time.Millisecond)
				// Emit notifications separately with delays to ensure ordering
				emitter.
					WithService("180f").
					WithCharacteristic("2a19", 0x42). // 'B'
					Emit(true)
				time.Sleep(50 * time.Millisecond)
				emitter.
					WithService("180d").
					WithCharacteristic("2a37", 0x48, 0x65, 0x6c, 0x6c, 0x6f). // "Hello"
					Emit(true)
				time.Sleep(50 * time.Millisecond)
				emitter.
					WithService("180f").
					WithCharacteristic("2a19", 0xFF, 0xAA, 0xBB). // Binary data
					Emit(true)
			},
			stdout: expectedDefaultBridgeScriptTemplate,
			ptySlaveRx: `[1] 2a19: 42 | B (1b)
[2] 2a37: 48 65 6C 6C 6F | Hello (5b)
[3] 2a19: FF AA BB | ... (3b)
`,
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			// Configure peripheral BEFORE connecting
			if tt.peripheral != nil {
				suite.GivenPeripheral(tt.peripheral)
			}

			// Use tt.script if provided, otherwise generate from TestCase
			script := tt.script
			if script != "" {
				// Resolve file:// URLs to actual script content
				content, _, err := lua.ResolveScript(script)
				require.NoError(suite.T(), err, "failed to resolve script")
				script = content
			} else {
				var err error
				script, err = lua.TestCase{
					Mode:         device.StreamEveryUpdate,
					Peripheral:   tt.peripheral,
					Subscription: tt.subscribe,
					Callback:     tt.callback,
				}.LuaTestScript()
				require.NoError(suite.T(), err, "failed to render Lua script")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2000*time.Millisecond)
			defer cancel()

			ft := suite.ConnectWithContext(ctx, "12").FluentBridgeTest().
				MustBridgeRun(script, nil)

			if tt.emitData != nil {
				ft.EmmitData(tt.emitData)
			}

			ft.Sleep(5 * testSyncWait)

			if tt.luaStdError != "" {
				ft.ConsumeStderrAsLuaError(tt.luaStdError)
			}

			if tt.stdout != "" {
				b := ft.Bridge()
				ft.ConsumeStdoutTemplate(tt.stdout, map[string]interface{}{
					"TTY":           b.GetTTYName(),
					"TTYSymlink":    b.GetTTYSymlink(),
					"DeviceAddress": ft.LuaAPI().GetDevice().Address(),
				})
			}

			if tt.expectedRecords != nil {
				builder := lua.NewExpectedRecordsBuilder()
				tt.expectedRecords(builder)
				ft.ConsumeRecordsFromBuilder(builder)
			}

			if tt.ptySlaveRx != "" {
				ft.ConsumePTYSlaveRx([]byte(tt.ptySlaveRx))
			}
		})
	}
}

func (suite *BridgeTestSuite) TestPTYWrite() {
	// GOAL: Verify the pty_write() function works correctly with various scenarios
	//
	// TEST SCENARIO: Table-driven tests for PTY write operations

	type step struct {
		lua                  string // Lua script to execute
		expectedPtySlaveRead []byte // Expected data to read from PTY slave
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			// GOAL: Verify pty_write() writes data to PTY master that appears on slave
			//
			// TEST SCENARIO: Lua calls blim.bridge.pty_write("test data") → test reads from slave
			name: "Basic Write Operation",
			steps: []step{
				{
					lua: `
						local bytes, err = blim.bridge.pty_write("test data")
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 9, "Expected 9 bytes written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte("test data"),
				},
			},
		},
		{
			// GOAL: Verify pty_write() correctly handles binary data with null bytes
			//
			// TEST SCENARIO: Lua writes binary string with 0x00, 0x01, 0x02, 0xFF → all bytes preserved
			name: "Binary Data",
			steps: []step{
				{
					lua: `
						local data = "test\x00\x01\x02\xFF"
						local bytes, err = blim.bridge.pty_write(data)
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 8, "Expected 8 bytes written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte{0x74, 0x65, 0x73, 0x74, 0x00, 0x01, 0x02, 0xFF}, // "test" + binary
				},
			},
		},
		{
			// GOAL: Verify pty_write() handles moderately-sized data blocks without blocking
			//       (regression test for PTY buffer overflow - 128 bytes is safe, 4KB+ blocks)
			//
			// TEST SCENARIO: Lua writes 128 bytes → all bytes correctly written to slave
			name: "Large Data Block",
			steps: []step{
				{
					lua: `
						local data = string.rep("A", 128)
						local bytes, err = blim.bridge.pty_write(data)
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 128, "Expected 128 bytes written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
				},
			},
		},
		{
			// GOAL: Verify pty_write() returns error for non-string arguments
			//
			// TEST SCENARIO: Call pty_write(123) → returns (nil, error)
			name: "Invalid Argument Type",
			steps: []step{
				{
					lua: `
						local bytes, err = blim.bridge.pty_write(123)
						assert(bytes == nil, "pty_write(123) MUST return nil for invalid argument")
						assert(err ~= nil, "pty_write(123) MUST return error for invalid argument")
						assert(string.find(tostring(err), "expects a string"), "Error message MUST mention 'expects a string', got: " .. tostring(err))
					`,
				},
			},
		},
		{
			// GOAL: Verify multiple pty_write() calls in sequence
			//
			// TEST SCENARIO: Write "Hello", then " ", then "World" → all data arrives in order
			name: "Multiple Sequential Writes",
			steps: []step{
				{
					lua: `
						local bytes, err = blim.bridge.pty_write("Hello")
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 5, "Expected 5 bytes written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte("Hello"),
				},
				{
					lua: `
						local bytes, err = blim.bridge.pty_write(" ")
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 1, "Expected 1 byte written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte(" "),
				},
				{
					lua: `
						local bytes, err = blim.bridge.pty_write("World")
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
						assert(bytes == 5, "Expected 5 bytes written, got " .. tostring(bytes))
					`,
					expectedPtySlaveRead: []byte("World"),
				},
			},
		},
		{
			// GOAL: Verify pty_write() handles empty string gracefully
			//
			// TEST SCENARIO: Write empty string → returns 0 bytes written, no error
			name: "Empty String",
			steps: []step{
				{
					lua: `
						local bytes, err = blim.bridge.pty_write("")
						assert(err == nil, "pty_write MUST succeed for empty string, got error: " .. tostring(err))
						assert(bytes == 0, "Expected 0 bytes written, got " .. tostring(bytes))
					`,
				},
			},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ft := suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
				MustBridgeRun(`print("PTY Write Test Started")`, nil).
				Sleep(testSyncWait).
				ConsumeStdout("PTY Write Test Started\n")

			for _, step := range tt.steps {
				// Execute Lua script
				if step.lua != "" {
					ft.MustExecuteScript(step.lua)
				}

				// Verify PTY slave read if expected
				if len(step.expectedPtySlaveRead) > 0 {
					ft.Sleep(100 * time.Millisecond).
						ConsumePTYSlaveRx(step.expectedPtySlaveRead)
				}
			}
		})
	}
}

func (suite *BridgeTestSuite) TestPTYRead() {
	// GOAL: Verify the pty_read() function works correctly with various scenarios
	//
	// TEST SCENARIO: Table-driven tests for PTY read operations

	type step struct {
		ptySlaveWrite        []byte // Data to write to PTY slave before Lua execution
		lua                  string // Lua script to execute
		expectedPtySlaveRead string // Expected data to read from slave (for roundtrip)
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			// GOAL: Verify pty_read() reads data from PTY master (written to slave by test)
			//
			// TEST SCENARIO: Test writes to slave → Lua reads via pty_read()
			name: "Basic Read Operation",
			steps: []step{
				{
					ptySlaveWrite: []byte("hello from slave"),
					lua: `
						blim.sleep(50)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(data == "hello from slave", "Expected 'hello from slave', got '" .. data .. "'")
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read() returns an empty string when no data is available
			//
			// TEST SCENARIO: Don't write anything → pty_read() returns ("", nil)
			name: "Non-Blocking Empty Read",
			steps: []step{
				{
					lua: `
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed when no data, got error: " .. tostring(err))
						assert(data == "", "Expected empty string, got '" .. data .. "'")
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read(max_bytes) respects buffer size limit
			//
			// TEST SCENARIO: Write 128 bytes → pty_read(64) returns only 64 bytes
			name: "Custom Buffer Size",
			steps: []step{
				{
					ptySlaveWrite: []byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
					lua: `
						blim.sleep(100)
						local expected = string.rep("B", 64)
						local data, err = blim.bridge.pty_read(64)
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(#data == 64, "Expected 64 bytes, got " .. #data)
						assert(data == expected, "Data mismatch: expected all B's")
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read() returns only available data when buffer size exceeds available bytes
			//
			// TEST SCENARIO: Write 32 bytes → pty_read(100) returns only 32 bytes
			name: "Oversized Buffer Request",
			steps: []step{
				{
					ptySlaveWrite: []byte("ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"),
					lua: `
						blim.sleep(100)
						local expected = string.rep("Z", 32)
						local data, err = blim.bridge.pty_read(100)
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(#data == 32, "Expected 32 bytes (all available), got " .. #data)
						assert(data == expected, "Data mismatch: expected all Z's")
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read() correctly handles binary data
			//
			// TEST SCENARIO: Write binary bytes to slave → Lua reads exact binary data
			name: "Binary Data",
			steps: []step{
				{
					ptySlaveWrite: []byte{0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD},
					lua: `
						blim.sleep(100)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(#data == 6, "Expected 6 bytes, got " .. #data)
						assert(string.byte(data, 1) == 0x01, "Byte 1 MUST be 0x01")
						assert(string.byte(data, 2) == 0x02, "Byte 2 MUST be 0x02")
						assert(string.byte(data, 3) == 0x03, "Byte 3 MUST be 0x03")
						assert(string.byte(data, 4) == 0xFF, "Byte 4 MUST be 0xFF")
						assert(string.byte(data, 5) == 0xFE, "Byte 5 MUST be 0xFE")
						assert(string.byte(data, 6) == 0xFD, "Byte 6 MUST be 0xFD")
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read() returns error for invalid buffer size
			//
			// TEST SCENARIO: Call pty_read(-1) and pty_read(0) → both return (nil, error)
			name: "Invalid Buffer Size",
			steps: []step{
				{
					lua: `
						-- Test negative buffer size
						local data, err = blim.bridge.pty_read(-1)
						assert(data == nil, "pty_read(-1) MUST return nil")
						assert(err ~= nil, "pty_read(-1) MUST return error")
						assert(string.find(tostring(err), "greater than zero"), "Error message MUST mention 'greater than zero', got: " .. tostring(err))

						-- Test zero buffer size
						local data2, err2 = blim.bridge.pty_read(0)
						assert(data2 == nil, "pty_read(0) MUST return nil")
						assert(err2 ~= nil, "pty_read(0) MUST return error")
						assert(string.find(tostring(err2), "greater than zero"), "Error message MUST mention 'greater than zero', got: " .. tostring(err2))
					`,
				},
			},
		},
		{
			// GOAL: Verify pty_read() handles moderately-sized data blocks without blocking
			//
			// TEST SCENARIO: Write 128 bytes to slave → pty_read() returns all 128 bytes
			name: "Large Data Block",
			steps: []step{
				{
					ptySlaveWrite: []byte("XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"),
					lua: `
						blim.sleep(200)
						local expected = string.rep("X", 128)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(#data == 128, "Expected 128 bytes, got " .. #data)
						assert(data == expected, "Data mismatch: expected all X's")
					`,
				},
			},
		},
		{
			// GOAL: Verify complete bidirectional PTY communication
			//
			// TEST SCENARIO: Lua writes "ping" → test reads → test writes "pong" → Lua reads
			name: "Write-Read Roundtrip",
			steps: []step{
				{
					lua: `
						local bytes, err = blim.bridge.pty_write("ping")
						assert(err == nil, "pty_write MUST succeed, got error: " .. tostring(err))
					`,
					expectedPtySlaveRead: "ping",
				},
				{
					ptySlaveWrite: []byte("pong"),
					lua: `
						blim.sleep(100)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(data == "pong", "Expected 'pong', got '" .. data .. "'")
					`,
				},
			},
		},
		{
			// GOAL: Verify multiple pty_read() calls in sequence
			//
			// TEST SCENARIO: Slave writes "Hello", then "World" → Lua reads both in order
			name: "Multiple Sequential Reads",
			steps: []step{
				{
					ptySlaveWrite: []byte("Hello"),
					lua: `
						blim.sleep(100)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(data == "Hello", "Expected 'Hello', got '" .. data .. "'")
					`,
				},
				{
					ptySlaveWrite: []byte("World"),
					lua: `
						blim.sleep(100)
						local data, err = blim.bridge.pty_read()
						assert(err == nil, "pty_read MUST succeed, got error: " .. tostring(err))
						assert(data == "World", "Expected 'World', got '" .. data .. "'")
					`,
				},
			},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ft := suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
				MustBridgeRun(`print("PTY Read Test Started")`, nil).
				Sleep(testSyncWait).
				ConsumeStdout("PTY Read Test Started\n")

			for _, step := range tt.steps {
				// Write to PTY slave if specified
				if len(step.ptySlaveWrite) > 0 {
					ft.WritePTYSlave(step.ptySlaveWrite).
						Sleep(testSyncWait)
				}

				// Execute Lua script
				if step.lua != "" {
					ft.MustExecuteScript(step.lua)
				}

				// Verify PTY slave read if expected
				if step.expectedPtySlaveRead != "" {
					ft.Sleep(testSyncWait).
						ConsumePTYSlaveRx([]byte(step.expectedPtySlaveRead))
				}
			}
		})
	}
}

func (suite *BridgeTestSuite) TestBridgeSymlinkLifecycle() {
	suite.Run("symlink is created during bridge execution", func() {
		// GOAL: Verify the symlink is created pointing to the PTY slave device
		//
		// TEST SCENARIO: Create a bridge with a symlink path → symlink exists → points to the PTY slave → bridge exits → symlink is removed

		bridgeCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Use a temporary symlink path
		symlinkPath := fmt.Sprintf("/tmp/blim-test-symlink-%d", time.Now().UnixNano())

		var actualPTYPath string

		suite.ConnectWithContext(bridgeCtx, "test-addr").FluentBridgeTest().
			// Bridge must run successfully
			MustBridgeCallbackRun(func(b Bridge) error {
				actualPTYPath = b.GetTTYName()

				// Verify symlink exists and points to PTY
				linkTarget, err := os.Readlink(symlinkPath)
				suite.NoError(err, "Symlink file must exist before bridge callback execution")
				suite.Equal(actualPTYPath, linkTarget, "Symlink must point to PTY slave")
				return nil
			}, &BridgeOptions{TTYSymlinkPath: symlinkPath})

		// Verify symlink is cleaned up after bridge exits
		suite.NoFileExists(symlinkPath, "Bridge must remove the symlink on exits")
	})

	suite.Run("bridge cleans up symlink on callback error", func() {
		// GOAL: Verify the symlink is removed even when the bridge fails.
		//
		// TEST SCENARIO: Create a bridge with symlink → error occurs → symlink is removed.

		symlinkPath := fmt.Sprintf("/tmp/blim-test-symlink-error-%d", time.Now().UnixNano())
		bridgeCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		suite.ConnectWithContext(bridgeCtx, "test-addr").FluentBridgeTest().
			FailBridgeCallbackRun("simulated bridge error", func(b Bridge) error {
				suite.FileExists(symlinkPath, "Symlink must exist before bridge callback execution")
				return fmt.Errorf("simulated bridge error")
			}, &BridgeOptions{TTYSymlinkPath: symlinkPath})

		suite.NoFileExists(symlinkPath, "Bridge must remove the symlink even if it exits with an error")
	})

	suite.Run("bridge runs without symlink", func() {
		// GOAL: Verify bridge callback works normally without a symlink
		//
		// TEST SCENARIO: Create bridge without SymlinkPath → bridge runs → no symlink created

		bridgeCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var ptyPath string
		suite.ConnectDeviceWithContext(bridgeCtx, "test-addr").FluentBridgeTest().
			MustBridgeCallbackRun(func(b Bridge) error {
				symlinkPath := b.GetTTYSymlink()
				suite.Empty(symlinkPath, "Symlink path must be empty when not specified")

				ptyPath = b.GetTTYName()
				suite.NotEmpty(ptyPath, "PTY must be created before bridge callback execution")

				return nil
			}, nil)

		suite.NotEmpty(ptyPath, "PTY must be created")
		suite.NoFileExists(ptyPath, "PTY must be removed when the bridge exits")
	})

	suite.Run("bridge fails to run when tty symlink already exists", func() {
		// GOAL: Verify bridge fails gracefully when tty symlink path already exists
		//
		// TEST SCENARIO: Pre-create symlink → create bridge with the same path → error returned

		symlinkPath := fmt.Sprintf("/tmp/blim-test-symlink-exists-%d", time.Now().UnixNano())

		// Pre-create a symlink
		err := os.Symlink("/dev/null", symlinkPath)
		suite.NoError(err, "Failed to pre-create TTY symlink for test")
		defer func(name string) {
			err := os.Remove(name)
			if err != nil {
				suite.Logger.Errorf("Failed to remove symlink %s: %v", name, err)
			}
		}(symlinkPath)

		bridgeCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		expectedError := fmt.Sprintf("failed to create tty symlink %s", symlinkPath)
		suite.ConnectDeviceWithContext(bridgeCtx, "test-addr").FluentBridgeTest().
			FailBridgeCallbackRun(expectedError, func(b Bridge) error {
				suite.Fail("Callback should not be reached")
				return fmt.Errorf("callback should not be reached")
			}, &BridgeOptions{TTYSymlinkPath: symlinkPath})
	})
}

func (suite *BridgeTestSuite) TestRunCliBridgeIntegration() {
	suite.Run("collects stdout from Lua script", func() {
		// GOAL: Verify print() output is captured by the collector
		//
		// TEST SCENARIO: Run script with print() → timeout → verify stdout captured

		ctx, cancel := context.WithTimeout(context.Background(), 500*testSyncWait)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			MustBridgeRun(`print("hello from bridge")`, nil).
			Sleep(testSyncWait).
			ConsumeStdout("hello from bridge\n")
	})

	suite.Run("collects stderr from Lua script", func() {
		// GOAL: Verify io.stderr:write() output is captured
		//
		// TEST SCENARIO: Run script with stderr write → timeout → verify stderr captured

		ctx, cancel := context.WithTimeout(context.Background(), 5*testSyncWait)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			MustBridgeRun(`io.stderr:write("error message\n")`, nil).
			Sleep(3 * testSyncWait).
			ConsumeStderrAsLuaError("error message")
	})

	suite.Run("gracefully exits on context cancellation", func() {
		// GOAL: Verify bridge exits cleanly when context times out
		//
		// TEST SCENARIO: Start bridge → timeout → verify no error, clean exit

		ctx, cancel := context.WithTimeout(context.Background(), 5*testSyncWait)
		defer cancel()

		// MustBridgeRun should return without error on timeout (clean shutdown)
		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			MustBridgeRun(`print("started")`, nil).
			Sleep(3 * testSyncWait).
			ConsumeStdout("started\n")
	})

	suite.Run("captures output after MustBridgeRun", func() {
		// GOAL: Verify output from ExecuteScript is captured when called after MustBridgeRun
		//
		// CONTEXT: MustBridgeRun sets up a drainer that routes OutputChannel() to collector.
		// ExecuteScript must reuse this collector (not create a new one) to avoid racing
		// with the drainer for output.
		//
		// TEST SCENARIO: MustBridgeRun → ConsumeStdout → ExecuteScript → ConsumeStdout

		ctx, cancel := context.WithTimeout(context.Background(), 5*testSyncWait)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			MustBridgeRun(`print("from bridge")`, nil).
			Sleep(testSyncWait).
			ConsumeStdout("from bridge\n").
			MustExecuteScript(`print("from script")`).
			Sleep(testSyncWait).
			ConsumeStdout("from script\n")
	})

	suite.Run("FailBridgeRun catches Lua runtime errors", func() {
		// GOAL: Verify FailBridgeRun properly detects Lua runtime errors
		//
		// TEST SCENARIO: Run script with runtime error → verify error is caught and stderr captured

		ctx, cancel := context.WithTimeout(context.Background(), 5*testSyncWait)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			FailBridgeRun("undefined_function", `undefined_function()`, nil)

	})
}

func (suite *BridgeTestSuite) TestPTYIntegration() {
	suite.Run("Lua reads from PTY slave and writes back", func() {
		// GOAL: Verify bidirectional PTY communication between test and Lua
		//
		// TEST SCENARIO: Test writes "hello" to PTY slave → Lua reads via pty_read() →
		//                Lua writes "got: hello" via pty_write() → Test reads from PTY slave
		//
		// NOTE: Uses polling loop instead of fixed sleep to handle timing variations.
		// The test writes to PTY slave while the script polls for data.

		// Timeout must account for: sleeps + ExecuteScript internal 1.5s flush + script polling (~1s)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			// Start bridge with a boilerplate script, just to get bridge create PTYs
			MustBridgeRun(`print("Bridge started")`, nil).
			// Give it time to settle
			Sleep(testSyncWait).
			ConsumeStdout("Bridge started\n").
			//
			WritePTYSlave([]byte("hello")).
			MustExecuteScript(`
				-- Poll for data with timeout (100 iterations × 10ms = 1 second max)
				local data = ""
				for i = 1, 100 do
					data = blim.bridge.pty_read()
					if data ~= "" then break end
					blim.sleep(10)
				end
				if data == "" then
					error("timeout waiting for PTY data")
				end
				blim.bridge.pty_write("got: " .. data)
			`).
			Sleep(testSyncWait).
			ConsumePTYSlaveRx([]byte("got: hello"))
	})

	suite.Run("Lua writes to PTY without reading first", func() {
		// GOAL: Verify Lua can write to PTY independently
		//
		// TEST SCENARIO: Lua writes "ping" via pty_write() → Test reads "ping" from PTY slave

		ctx, cancel := context.WithTimeout(context.Background(), 5*testSyncWait)
		defer cancel()

		suite.ConnectWithContext(ctx, "test-addr").FluentBridgeTest().
			MustBridgeRun(`
				blim.bridge.pty_write("ping")
			`, nil).
			Sleep(testSyncWait).
			ConsumePTYSlaveRx([]byte("ping"))
	})
}

// TestBridgeTestSuite runs the test suite using testify/suite
func TestBridgeTestSuite(t *testing.T) {
	suite.Run(t, new(BridgeTestSuite))
}
