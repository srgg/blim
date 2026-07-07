package lua

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aarzilli/golua/lua"
	"github.com/sirupsen/logrus"
	blim "github.com/srgg/blim"
	"github.com/srgg/blim/internal/device"
)

const (
	// DefaultCharacteristicReadTimeout is the default timeout for characteristic read operations
	DefaultCharacteristicReadTimeout = 5 * time.Second
	// DefaultCharacteristicWriteTimeout is the default timeout for characteristic write operations
	DefaultCharacteristicWriteTimeout = 5 * time.Second
	// DefaultDescriptorReadTimeout is the default timeout for descriptor read operations
	DefaultDescriptorReadTimeout = 2 * time.Second
)

// BridgeInfo bridge information exposed to Lua
type BridgeInfo interface {
	GetTTYName() string                 // TTY device name if created
	GetTTYSymlink() string              // Symlink path (empty if not created)
	GetPTY() io.ReadWriter              // PTY I/O as a standard Go interface (non-blocking reads /writes via ring buffers)
	SetPTYReadCallback(cb func([]byte)) // Set callback for PTY data arrival (nil to unregister)
}

// LuaAPIInterface defines the interface for LuaAPI used at factory boundaries.
// This enables test wrappers to intercept SetBridge calls for bridge capture.
type LuaAPIInterface interface {
	// SetBridge Bridge management
	SetBridge(BridgeInfo)

	// GetDevice Device access
	GetDevice() device.Device

	// OutputChannel Output channel for Lua print/error output
	OutputChannel() <-chan LuaOutputRecord

	// ExecuteScript Script execution
	ExecuteScript(ctx context.Context, script string) error

	// SetCharacteristicReadTimeout Configuration setters (for ExecuteDeviceScriptWithOutput)
	SetCharacteristicReadTimeout(d time.Duration)
	SetCharacteristicWriteTimeout(d time.Duration)
	SetPackagePaths(scriptPath string, libPaths []string) error

	// AddShutdownHook registers a callback invoked when script execution context is cancelled.
	// Returns a function to remove the hook. Used to unblock io.read() by closing PTY.
	AddShutdownHook(fn func()) (remove func())

	Close()
}

// LuaSubscriptionTable Lua subscription configuration
type LuaSubscriptionTable struct {
	Services      []device.SubscribeOptions `json:"services"`
	Mode          string                    `json:"mode"`
	MaxRate       int                       `json:"max_rate"`
	DrainDuration int                       `json:"drain_duration"` // milliseconds; 0 = disable drain
	CallbackRef   int                       `json:"-"`              // Lua function reference; lua.LUA_NOREF (-2) means none
}

// LuaAPI represents the new BLE API that supports Lua subscriptions
// This replaces the old TTY-based bridge with direct subscription support
type LuaAPI struct {
	device                     device.Device
	LuaEngine                  *LuaEngine
	logger                     *logrus.Logger
	bridge                     BridgeInfo       // Optional bridge information
	characteristicReadTimeout  time.Duration    // Default timeout for characteristic read operations
	characteristicWriteTimeout time.Duration    // Default timeout for characteristic write operations
	closeOnce                  sync.Once        // Ensures Close() is idempotent
	ptyCallbackRef             int              // Tracks PTY callback ref for cleanup; lua.LUA_NOREF if none
	subscriptionCallbackRefs   map[int]struct{} // Tracks subscription callback refs for cleanup (O(1) add/remove)
	callbackRefsMu             sync.Mutex       // Protects subscriptionCallbackRefs map
	luaCallbackWG              sync.WaitGroup   // Tracks in-flight async callbacks for safe shutdown
	shuttingDown               atomic.Bool      // Set during shutdown to block new callbacks before Wait()
	// Callback-depth tracking (inCallbackCount) lives on LuaEngine now — it is a property of the
	// shared lua_State, and LuaEngine-registered primitives (io.read) must consult it too.
}

// NewBLEAPI2 creates a new BLE API instance with subscription support
func NewBLEAPI2(device device.Device, logger *logrus.Logger) *LuaAPI {
	r := &LuaAPI{
		device:                     device,
		logger:                     logger,
		LuaEngine:                  NewLuaEngine(logger),
		characteristicReadTimeout:  DefaultCharacteristicReadTimeout,
		characteristicWriteTimeout: DefaultCharacteristicWriteTimeout,
		ptyCallbackRef:             lua.LUA_NOREF,          // No callback registered initially
		subscriptionCallbackRefs:   make(map[int]struct{}), // Initialize map for O(1) add/remove
	}

	r.Reset()
	return r
}

func (api *LuaAPI) GetDevice() device.Device {
	return api.device
}

// SetBridge sets the bridge information and updates the PTY strategy.
// When a bridge is set, the ptyio field is updated to use the bridge's PTY I/O strategy.
// When the bridge is nil, the ptyio field reverts to NilPTYIO.
//
// NOTE: We intentionally do NOT register a shutdown hook here to call Disconnect().
// The shutdown hook runs BEFORE the script goroutine exits (see safeExecuteScript),
// and calling Disconnect() while the script is still running causes SIGBUS crashes.
// Disconnect() and WaitForCallbacks() are handled properly in LuaAPI.Close() which
// runs AFTER the script goroutine has fully exited.
func (api *LuaAPI) SetBridge(bridge BridgeInfo) {
	api.logger.WithFields(logrus.Fields{
		"bridge_set": bridge != nil,
		"api_ptr":    fmt.Sprintf("%p", api),
	}).Debug("SetBridge called")
	api.bridge = bridge
}

// SetCharacteristicReadTimeout sets the timeout for characteristic read operations.
func (api *LuaAPI) SetCharacteristicReadTimeout(d time.Duration) {
	api.characteristicReadTimeout = d
}

// SetCharacteristicWriteTimeout sets the timeout for characteristic write operations.
func (api *LuaAPI) SetCharacteristicWriteTimeout(d time.Duration) {
	api.characteristicWriteTimeout = d
}

// SetPackagePaths configures allowed paths for secure module loading.
// Adds script directory and validated library paths to SecureModuleLoader.
func (api *LuaAPI) SetPackagePaths(scriptPath string, libPaths []string) error {
	return api.LuaEngine.SetPackagePaths(scriptPath, libPaths)
}

// AddShutdownHook registers a callback invoked when script execution context is cancelled.
// Returns a function to remove the hook. Used to unblock io.read() by closing PTY.
func (api *LuaAPI) AddShutdownHook(fn func()) (remove func()) {
	return api.LuaEngine.AddShutdownHook(fn)
}

// releasePTYCallbackRef releases the PTY callback reference from Lua registry if one exists.
// Must be called within DoWithState or when L is already available.
func (api *LuaAPI) releasePTYCallbackRef(L *lua.State) {
	if api.ptyCallbackRef != lua.LUA_NOREF {
		L.Unref(lua.LUA_REGISTRYINDEX, api.ptyCallbackRef)
		api.ptyCallbackRef = lua.LUA_NOREF
	}
}

// releaseCallbackRefInternal removes a callback reference from tracking and releases it from the Lua registry.
// Called from Lua context - uses doWithStateInternal (no lock acquisition).
func (api *LuaAPI) releaseCallbackRefInternal(ref int) {
	api.callbackRefsMu.Lock()
	_, exists := api.subscriptionCallbackRefs[ref]
	delete(api.subscriptionCallbackRefs, ref)
	remaining := len(api.subscriptionCallbackRefs)
	api.callbackRefsMu.Unlock()

	if api.logger != nil {
		api.logger.Debugf("subscription:release ref=%d existed=%v (remaining=%d)", ref, exists, remaining)
	}

	api.LuaEngine.doWithStateInternal(func(L *lua.State) interface{} {
		if api.logger != nil {
			api.logger.Debugf("subscription:unref ref=%d", ref)
		}

		if ref != lua.LUA_NOREF {
			L.Unref(lua.LUA_REGISTRYINDEX, ref)
		}
		return nil
	})
}

// releaseCallbackRef removes a callback reference from tracking and releases it from the Lua registry.
// Called from Go context - uses DoWithState (acquires lock).
func (api *LuaAPI) releaseCallbackRef(ref int) {
	api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
		api.releaseCallbackRefInternal(ref)
		return nil
	})
}

// registerBridgeInfo registers the blim.bridge table with runtime bridge checking
// This is called internally during API registration within the DoWithState block
// Stack: expects _blim_internal table at top (-1)
func (api *LuaAPI) registerBridgeInfo(L *lua.State) {
	L.PushString("bridge")

	// Create a bridge table with getter functions that check at runtime
	L.NewTable() // Stack: _blim_internal, "bridge", {}

	// Add tty_name as a getter function
	L.PushString("tty_name")
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction("bridge.tty_name", func(L *lua.State) int {
		if api.bridge == nil {
			api.logger.WithField("api_ptr", fmt.Sprintf("%p", api)).Error("bridge.tty_name accessed but api.bridge is nil")
			L.RaiseError("bridge field 'tty_name' is not available (not running in bridge mode)")
			return 0
		}
		L.PushString(api.bridge.GetTTYName())
		return 1
	}))
	L.SetTable(-3)

	// Add symlink_path as a getter function
	L.PushString("tty_symlink")
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction("bridge.tty_symlink", func(L *lua.State) int {
		if api.bridge == nil {
			L.RaiseError("bridge field 'tty_symlink' is not available (not running in bridge mode)")
			return 0
		}
		L.PushString(api.bridge.GetTTYSymlink())
		return 1
	}))
	L.SetTable(-3)

	// Add pty_write function - writes data to PTY via strategy pattern
	// Usage: blim.bridge.pty_write(data)
	// Returns: (bytes_written, nil) on success or (nil, error_message) on failure
	L.PushString("pty_write")
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction("bridge.pty_write", func(L *lua.State) int {
		if api.bridge == nil {
			L.RaiseError("pty_write() is not available (not running in bridge mode)")
			return 0
		}

		// Get PTY I/O strategy (minimal LuaPTY interface - no Close/TTYName exposed)
		ptyIO := api.bridge.GetPTY()

		// Validate argument - check actual type (IsString returns true for numbers too)
		if L.Type(1) != lua.LUA_TSTRING {
			L.PushNil()
			L.PushString("pty_write(data) expects a string argument")
			return 2
		}

		data := L.ToString(1)

		// DEBUG: Log the write attempt
		api.logger.Debugf("[pty_write] Writing %d bytes to PTY", len(data))

		// Write via strategy
		n, err := ptyIO.Write([]byte(data))
		if err != nil {
			api.logger.Warnf("[pty_write] Write failed: %v", err)
			L.PushNil()
			L.PushString(fmt.Sprintf("pty_write() failed: %v", err))
			return 2
		}

		// DEBUG: Log successful write
		api.logger.Debugf("[pty_write] Successfully wrote %d bytes", n)

		// Return (bytes_written, nil) on success
		L.PushInteger(int64(n))
		L.PushNil()
		return 2
	}))
	L.SetTable(-3)

	// Add pty_read function - reads buffered data via io.Reader interface
	// Usage: blim.bridge.pty_read([max_bytes])
	// Returns: (data, nil) on success, ("", nil) if no data available, or (nil, error_message) on failure
	// max_bytes defaults to 4096 if not specified
	L.PushString("pty_read")
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction("bridge.pty_read", func(L *lua.State) int {
		if api.bridge == nil {
			L.RaiseError("pty_read() is not available (not running in bridge mode)")
			return 0
		}

		// Get PTY I/O (io.ReadWriter interface)
		ptyIO := api.bridge.GetPTY()

		// Parse optional max_bytes argument (default: 4096)
		maxBytes := 4096
		if L.GetTop() >= 1 && L.IsNumber(1) {
			maxBytes = L.ToInteger(1)
			if maxBytes <= 0 {
				L.PushNil()
				L.PushString("pty_read(max_bytes) max_bytes must be greater than zero")
				return 2
			}
		}

		// Allocate buffer for io.Reader
		buf := make([]byte, maxBytes)

		// Read via io.Reader (non-blocking, returns buffered data)
		n, err := ptyIO.Read(buf)
		if err != nil {
			// Handle expected non-blocking I/O errors
			if errors.Is(err, syscall.EAGAIN) || err == io.EOF {
				// No data available (EAGAIN) or EOF - return empty string, no error
				L.PushString("")
				L.PushNil()
				return 2
			}
			// Unexpected error - return error
			L.PushNil()
			L.PushString(fmt.Sprintf("pty_read() failed: %v", err))
			return 2
		}

		// Return (data, nil) on success
		L.PushString(string(buf[:n]))
		L.PushNil()
		return 2
	}))
	L.SetTable(-3)

	// Add pty_on_data function - registers Lua callback for PTY data arrival
	// Usage: blim.bridge.pty_on_data(function(data) ... end)
	// Pass nil to unregister: blim.bridge.pty_on_data(nil)
	L.PushString("pty_on_data")
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction("bridge.pty_on_data", func(L *lua.State) int {
		if api.bridge == nil {
			L.RaiseError("pty_on_data() is not available (not running in bridge mode)")
			return 0
		}

		// Check if nil was passed (unregister callback)
		if L.IsNil(1) {
			api.logger.Debug("[pty_on_data] Unregistering PTY callback")
			api.releasePTYCallbackRef(L)
			api.bridge.SetPTYReadCallback(nil)
			return 0
		}

		// Validate argument - must be a function
		if !L.IsFunction(1) {
			L.RaiseError("pty_on_data() expects a function or nil argument")
			return 0
		}

		// Release previous callback ref before creating new one to avoid registry leak
		api.releasePTYCallbackRef(L)

		// Store reference to the callback function in the Lua registry
		callbackRef := L.Ref(lua.LUA_REGISTRYINDEX)
		api.ptyCallbackRef = callbackRef

		api.logger.WithField("callback_ref", callbackRef).Debug("[pty_on_data] Registering PTY callback")

		// Create a Go callback that dispatches to Lua
		api.bridge.SetPTYReadCallback(func(data []byte) {
			api.callPTYDataCallback(callbackRef, data)
		})

		return 0
	}))
	L.SetTable(-3)

	// Set _blim_internal.bridge = <table with getter functions>
	L.SetTable(-3)
}

// SafePushGoFunction pushes a function name and safe-wrapped Go function onto the Lua stack.
// The function will be automatically wrapped with panic recovery and error logging.
// After calling this, you typically call L.SetTable(-3) to add it to the parent table.
//
// Example:
//
//	api.SafePushGoFunction(L, "read", func(L *lua.State) int {
//	    // your implementation
//	})
//	L.SetTable(-3)
func (api *LuaAPI) SafePushGoFunction(L *lua.State, name string, fn func(*lua.State) int) {
	L.PushString(name)
	L.PushGoFunction(api.LuaEngine.SafeWrapGoFunction(name+"()", fn))
}

// stripWrappedGoErrorSuffix removes Go error wrapping suffixes from error messages for cleaner Lua API messages.
// Strips known suffixes like ": unsupported", ": not connected", ": not_connected", ": timeout" while keeping
// the Go code properly structured with error wrapping for errors.Is() checks.
func stripWrappedGoErrorSuffix(errMsg string) string {
	if idx := strings.LastIndex(errMsg, ": "); idx != -1 {
		suffix := errMsg[idx+2:]
		if suffix == "unsupported" || suffix == "not connected" || suffix == "not_connected" || suffix == "timeout" {
			return errMsg[:idx]
		}
	}
	return errMsg
}

// parseStreamPattern converts a string pattern to a device.StreamPattern
func parseStreamPattern(pattern string) device.StreamMode {
	switch pattern {
	case "EveryUpdate":
		return device.StreamEveryUpdate
	case "Batched":
		return device.StreamBatched
	case "Aggregated":
		return device.StreamAggregated
	default:
		return device.StreamEveryUpdate // Default fallback
	}
}

func (api *LuaAPI) ExecuteScript(ctx context.Context, script string) error {
	// Shutdown hook: detects context cancellation and sets the shuttingDown flag.
	// Returns true to skip RaiseError only when inside a callback (prevents SIGSEGV).
	// Returns false when outside a callback to allow RaiseError to stop the script.
	hook := func(_ *lua.State) bool {
		inCallback := api.LuaEngine.inCallback()

		// Already shutting down - check if safe to RaiseError
		if api.shuttingDown.Load() {
			if inCallback {
				api.logger.Debug("[LuaAPI:ShutdownHook] shuttingDown=true, inCallback=true, skipping RaiseError (unsafe)")
				return true // Skip RaiseError - we're in a callback, unsafe
			}
			api.logger.Debug("[LuaAPI:ShutdownHook] shuttingDown=true, inCallback=false, allowing RaiseError")
			return false // Allow RaiseError - not in callback, safe to stop
		}

		// Check if context is done
		select {
		case <-ctx.Done():
			// First tick to see shutdown - set flag atomically
			api.shuttingDown.Store(true)
			api.logger.Debugf("[LuaAPI:ShutdownHook] ctx.Done detected, set shuttingDown=true, inCallback=%v", inCallback)
			if inCallback {
				api.logger.Debug("[LuaAPI:ShutdownHook] in callback, skipping RaiseError (unsafe)")
				return true // Skip - let callback finish first
			}
			api.logger.Debug("[LuaAPI:ShutdownHook] not in callback, allowing RaiseError")
			return false // Safe to stop immediately
		default:
			// Normal operation - let safeExecuteScript's default hook run
			return false
		}
	}

	hookedUp := WithShutdownHook(ctx, hook)
	return api.LuaEngine.ExecuteScript(hookedUp, script)
}

func (api *LuaAPI) LoadScriptFile(filename string) error {
	return api.LuaEngine.LoadScriptFile(filename)
}

func (api *LuaAPI) LoadScript(script, name string) error {
	return api.LuaEngine.LoadScript(script, name)
}

func (api *LuaAPI) Reset() {
	api.LuaEngine.Reset()
	api.registerBlimAPI() // Register _blim_internal for Lua wrapper
}

func (api *LuaAPI) OutputChannel() <-chan LuaOutputRecord {
	return api.LuaEngine.OutputChannel()
}

// registerBlimAPI registers the internal BLE API (_blim_internal) for Lua wrapper
// This demonstrates the CGO-like approach where Lua wraps Go functions
func (api *LuaAPI) registerBlimAPI() {
	api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
		// Create _blim_internal table
		L.NewTable()

		// Register API functions (same as ble)
		api.registerSubscribeFunction(L)
		api.registerListFunction(L)
		api.registerDeviceInfo(L)
		api.registerCharacteristicFunction(L)

		// Register utility functions
		api.registerSleepFunction(L)

		// Register bridge info if set
		api.registerBridgeInfo(L)

		// Set global '_blim_internal' variable
		L.SetGlobal("_blim_internal")

		// Preload the embedded Lua libraries (creates global blim + ffi_buffer).
		// PreloadLuaLibrary panics on failure — these are go:embed assets, so a
		// failure means the binary is broken (see its doc comment).
		api.LuaEngine.PreloadLuaLibrary(blim.BlimLuaScript, "blim", "blim.lua")
		api.LuaEngine.PreloadLuaLibrary(blim.FfiBufferLibLuaScript, "ffi_buffer", "ffi.buffer.lua")

		return nil
	})
}

// registerSubscribeFunction registers the ble.subscribe() function.
// Returns a cancel function to Lua for external subscription control.
func (api *LuaAPI) registerSubscribeFunction(L *lua.State) {
	api.SafePushGoFunction(L, "subscribe", func(L *lua.State) int {
		// NOTE (issue #3): subscribe is NOT guarded against being called from a callback. It never
		// releases stateMutex and never re-enters Lua — its synchronous CCCD write completes via the
		// go-ble connection-event path, independent of the notification fan-out — so from a callback it
		// is at most a brief stall, not corruption. The previous guard raised a Go-side error, which is
		// itself what corrupted the lua_State on LuaJIT (a recovered Go panic leaves the VM's cframe
		// dangling). Making subscribe non-blocking is a separate follow-up.

		// Expect a table as the first argument
		if !L.IsTable(1) {
			L.RaiseError("Error: subscribe() expects a lua table argument")
			return 0
		}

		// Parse the subscription table
		config, err := api.parseSubscriptionTable(L, 1)
		if err != nil {
			L.RaiseError("Error parsing subscription config: " + err.Error())
			return 0
		}

		// Execute the subscription and get cancel function
		cancel, err := api.executeSubscription(config)
		if err != nil {
			L.RaiseError("Error executing subscription: " + err.Error())
			return 0
		}

		// Return cancel function to Lua for external control
		api.SafePushGoFunction(L, "cancel", func(L *lua.State) int {
			if cancel != nil {
				cancel()
			}
			return 0
		})
		// SafePushGoFunction pushes name and function; we only need the function
		L.Remove(-2)

		return 1 // Return 1 value (the cancel function)
	})
	L.SetTable(-3)
}

// registerListFunction registers the ble.list() function
//
// Returns a dual-purpose Lua table with both array and hash parts:
//
// In Lua, a table can have both an array part and a hash part at the same time:
//
// Array part (numeric indices):
//   - Keys: [1], [2], [3], etc.
//   - Accessed with ipairs() for ordered iteration
//   - Preserves insertion order
//
// Hash part (string/any keys):
//   - Keys: ["uuid"], ["name"], etc.
//   - Accessed with pairs() (order not guaranteed) or direct lookup table["uuid"]
//
// Example:
//
//	local t = {}
//	t[1] = "service1"           -- array part
//	t[2] = "service2"           -- array part
//	t["service1"] = {data=123}  -- hash part
//	t["service2"] = {data=456}  -- hash part
//
// For ble.list(), this allows:
//  1. Ordered iteration: for i, uuid in ipairs(services) do ... end
//  2. UUID-based lookup: services[uuid] to get service info
func (api *LuaAPI) registerListFunction(L *lua.State) {
	api.SafePushGoFunction(L, "list", func(L *lua.State) int {
		// Get connection when function is called, not when registered
		connection := api.device.GetConnection()
		if connection == nil {
			L.NewTable() // Return empty table if no connection
			return 1
		}
		services := connection.Services()
		L.NewTable()

		// Add both an indexed array (for ordered iteration) and keyed access (for lookup)
		arrayIndex := 1
		for _, service := range services {
			uuid := service.UUID()

			// Create service info table
			L.NewTable()

			// Add name field (only if known)
			if knownName := service.KnownName(); knownName != "" {
				L.PushString("name")
				L.PushString(knownName)
				L.SetTable(-3)
			}

			// Add a characteristic array
			L.PushString("characteristics")
			L.NewTable()
			charIndex := 1
			for _, c := range service.GetCharacteristics() {
				L.PushInteger(int64(charIndex))
				L.PushString(c.UUID())
				L.SetTable(-3)
				charIndex++
			}
			L.SetTable(-3)

			// Stack: [main_table, service_info]
			// Store service info with UUID key (for lookup: table["uuid"])
			L.PushString(uuid) // Stack: [main_table, service_info, uuid]
			L.PushValue(-2)    // Stack: [main_table, service_info, uuid, service_info]
			L.SetTable(-4)     // main_table[uuid] = service_info; Stack: [main_table, service_info]

			// Store UUID in array part (for iteration: ipairs(table))
			L.PushInteger(int64(arrayIndex)) // Stack: [main_table, service_info, arrayIndex]
			L.PushString(uuid)               // Stack: [main_table, service_info, arrayIndex, uuid]
			L.SetTable(-4)                   // main_table[arrayIndex] = uuid; Stack: [main_table, service_info]

			// Pop the service info table
			L.Pop(1)

			arrayIndex++
		}
		return 1
	})
	L.SetTable(-3)
}

// registerDeviceInfo registers the ble.device table with device information
func (api *LuaAPI) registerDeviceInfo(L *lua.State) {
	dev := api.device

	L.PushString("device")
	L.NewTable()

	if dev != nil {
		// Device ID
		L.PushString("id")
		L.PushString(dev.ID())
		L.SetTable(-3)

		// Device BleAddress
		L.PushString("address")
		L.PushString(dev.Address())
		L.SetTable(-3)

		// Device Name
		L.PushString("name")
		L.PushString(dev.Name())
		L.SetTable(-3)

		// RSSI
		L.PushString("rssi")
		L.PushInteger(int64(dev.RSSI()))
		L.SetTable(-3)

		// Connectable
		L.PushString("connectable")
		L.PushBoolean(dev.IsConnectable())
		L.SetTable(-3)

		// TX Power (optional)
		if txPower := dev.TxPower(); txPower != nil {
			L.PushString("tx_power")
			L.PushInteger(int64(*txPower))
			L.SetTable(-3)
		}

		// Advertised Services
		L.PushString("advertised_services")
		L.NewTable()
		uuids := dev.AdvertisedServices()
		for i, uuid := range uuids {
			L.PushInteger(int64(i + 1))
			L.PushString(uuid)
			L.SetTable(-3)
		}
		L.SetTable(-3)

		// Manufacturer Data (table with value and optional parsed_value, or nil if no data)
		manufData := dev.ManufacturerData()
		L.PushString("manufacturer_data")
		if len(manufData) > 0 {
			L.NewTable()

			// Raw value field
			L.PushString("value")
			L.PushString(fmt.Sprintf("%X", manufData))
			L.SetTable(-3)

			// Parsed value field (optional, includes vendor if parser implements VendorInfo)
			parsedManufData := dev.ParsedManufacturerData()
			if parsedManufData != nil {
				L.PushString("parsed_value")
				api.pushManufacturerParsedData(L, parsedManufData)
				L.SetTable(-3)
			}

			L.SetTable(-3)
		} else {
			L.PushNil()
			L.SetTable(-3)
		}

		// Service Data
		L.PushString("service_data")
		L.NewTable()
		serviceData := dev.ServiceData()
		for uuid, data := range serviceData {
			L.PushString(uuid)
			L.PushString(fmt.Sprintf("%X", data))
			L.SetTable(-3)
		}
		L.SetTable(-3)
	}

	L.SetTable(-3) // Set device subtable in ble table
}

// parseSubscriptionTable parses the Lua table into a LuaSubscriptionTable
func (api *LuaAPI) parseSubscriptionTable(L *lua.State, tableIndex int) (*LuaSubscriptionTable, error) {
	config := &LuaSubscriptionTable{
		CallbackRef: lua.LUA_NOREF, // Explicit "no callback" marker; 0 is a valid ref
	}

	// Convert relative index to absolute index
	if tableIndex < 0 {
		tableIndex = L.GetTop() + tableIndex + 1
	}

	// Parse services array
	L.PushString("services")
	L.GetTable(tableIndex)
	if L.IsTable(-1) {
		services, err := api.parseServicesArray(L, -1)
		if err != nil {
			L.Pop(1)
			return nil, err
		}
		config.Services = services
	}
	L.Pop(1)

	// Parse Mode
	L.PushString("Mode")
	L.GetTable(tableIndex)
	if L.IsString(-1) {
		config.Mode = L.ToString(-1)
	} else {
		config.Mode = "EveryUpdate" // Default
	}
	L.Pop(1)

	// Parse MaxRate
	L.PushString("MaxRate")
	L.GetTable(tableIndex)
	if L.IsNumber(-1) {
		config.MaxRate = L.ToInteger(-1)
	} else {
		config.MaxRate = 0 // Default
	}
	L.Pop(1)

	// Parse DrainDuration (milliseconds)
	L.PushString("DrainDuration")
	L.GetTable(tableIndex)
	if L.IsNumber(-1) {
		config.DrainDuration = L.ToInteger(-1)
	} else {
		config.DrainDuration = 0 // Default: drain disabled
	}
	L.Pop(1)

	// Parse Callback function
	L.PushString("Callback")
	L.GetTable(tableIndex)
	if L.IsFunction(-1) {
		// Store reference to the function
		config.CallbackRef = L.Ref(lua.LUA_REGISTRYINDEX)
	} else {
		L.Pop(1) // Pop non-function value
	}

	return config, nil
}

// parseServicesArray parses the service array from the Lua table
func (api *LuaAPI) parseServicesArray(L *lua.State, tableIndex int) ([]device.SubscribeOptions, error) {
	var services []device.SubscribeOptions

	// Convert relative index to absolute index for L.Next()
	if tableIndex < 0 {
		tableIndex = L.GetTop() + tableIndex + 1
	}

	// Iterate through the service array
	L.PushNil()
	for L.Next(tableIndex) != 0 {
		if L.IsTable(-1) {
			service := device.SubscribeOptions{}

			// Parse service UUID
			L.PushString("service")
			L.GetTable(-2)
			if L.IsString(-1) {
				// Normalize service UUID
				normalizedService := device.NormalizeUUID(L.ToString(-1))
				service.Service = normalizedService
			}
			L.Pop(1)

			// Parse chars array
			L.PushString("chars")
			L.GetTable(-2)
			if L.IsTable(-1) {
				// Convert relative index to absolute index
				charsIndex := L.GetTop()
				chars := api.parseCharsArray(L, charsIndex)
				service.Characteristics = chars
			}
			L.Pop(1)

			// Parse indicate flag (per-service)
			L.PushString("indicate")
			L.GetTable(-2)
			if L.IsBoolean(-1) {
				service.Indicate = L.ToBoolean(-1)
			}
			L.Pop(1)

			services = append(services, service)
		}
		L.Pop(1) // Pop value, keep key for next iteration
	}

	return services, nil
}

// parseCharsArray parses the characteristic array from a service
func (api *LuaAPI) parseCharsArray(L *lua.State, tableIndex int) []string {
	var chars []string

	// Convert relative index to absolute index for L.Next()
	if tableIndex < 0 {
		tableIndex = L.GetTop() + tableIndex + 1
	}

	L.PushNil()
	for L.Next(tableIndex) != 0 {
		if L.IsString(-1) {
			// Normalize characteristic UUID
			normalizedChar := device.NormalizeUUID(L.ToString(-1))
			chars = append(chars, normalizedChar)
		}
		L.Pop(1) // Pop value, keep key for next iteration
	}

	return chars
}

// executeSubscription creates and starts the actual BLE subscription.
// Returns a cancel function that cancels the subscription and releases the Lua callback reference.
// The cancel function is safe to call multiple times (idempotent via sync.Once).
func (api *LuaAPI) executeSubscription(config *LuaSubscriptionTable) (func(), error) {
	api.logger.WithFields(logrus.Fields{
		"services": len(config.Services),
		"mode":     config.Mode,
	}).Debug("executeSubscription called")

	// Convert SubscriptionConfig to a device.SubscribeOptions
	var opts []*device.SubscribeOptions
	for _, serviceConfig := range config.Services {
		opt := &device.SubscribeOptions{
			Service:         serviceConfig.Service,
			Characteristics: serviceConfig.Characteristics,
			Indicate:        serviceConfig.Indicate, // Use per-service Indicate flag
		}
		opts = append(opts, opt)
	}

	// Parse mode, max rate, and drain duration
	pattern := parseStreamPattern(config.Mode)
	maxRate := time.Duration(config.MaxRate) * time.Millisecond
	drainDuration := time.Duration(config.DrainDuration) * time.Millisecond

	// Capture callback ref for use in callback and cancel closures
	callbackRef := config.CallbackRef

	// cancelMu protects access to cancel function. The callback closure may be invoked
	// by Subscribe's goroutine before we set cancel below, so we need synchronization.
	var cancelMu sync.RWMutex
	var cancel func()

	// Create a callback that calls the Lua function (nil if no callback provided)
	var callback func(*device.Record)
	if callbackRef != lua.LUA_NOREF {
		// Track ref for cleanup in Close()
		api.callbackRefsMu.Lock()
		api.subscriptionCallbackRefs[callbackRef] = struct{}{}
		if api.logger != nil {
			api.logger.Debugf("subscription:create ref=%d (total tracked=%d)", callbackRef, len(api.subscriptionCallbackRefs))
		}
		api.callbackRefsMu.Unlock()

		callback = func(record *device.Record) {
			// Read cancel safely - may be nil if called before Subscribe returns
			cancelMu.RLock()
			cancelFn := cancel
			cancelMu.RUnlock()
			api.callLuaCallback(callbackRef, record, cancelFn)
		}
	}

	// Call Subscribe on the connection
	rawCancel, err := api.device.GetConnection().Subscribe(opts, pattern, maxRate, drainDuration, callback)
	if err != nil {
		// Clean up tracked ref on subscribe failure - use single source of truth
		if callbackRef != lua.LUA_NOREF {
			api.releaseCallbackRefInternal(callbackRef)
		}
		return nil, err
	}

	// Wrap rawCancel to also release the Lua callback reference (idempotent).
	// Uses releaseCallbackRefInternal since cancel is always called from Lua context.
	var cancelOnce sync.Once
	wrappedCancel := func() {
		cancelOnce.Do(func() {
			if api.logger != nil {
				api.logger.Debugf("subscription:cancel invoked for ref=%d", callbackRef)
			}
			rawCancel()
			if callbackRef != lua.LUA_NOREF {
				api.releaseCallbackRefInternal(callbackRef)
			}
		})
	}

	// Set cancel with synchronization - callbacks may already be running
	cancelMu.Lock()
	cancel = wrappedCancel
	cancelMu.Unlock()

	return wrappedCancel, nil
}

// callPTYDataCallback calls the Lua callback function when PTY data arrives
func (api *LuaAPI) callPTYDataCallback(callbackRef int, data []byte) error {
	// Check shuttingDown BEFORE Add() to prevent new callbacks during shutdown.
	if api.shuttingDown.Load() {
		api.logger.Debugf("pty:lua-callback skipped ref=%d reason=shutting_down", callbackRef)
		return nil
	}

	api.luaCallbackWG.Add(1)
	defer api.luaCallbackWG.Done()

	if callbackRef == lua.LUA_NOREF {
		return nil
	}

	// Outer panic handler: catches ALL panics (including LuaError from StackTrace crashes)
	// This ensures one callback's error doesn't crash the PTY dispatcher
	defer func() {
		if r := recover(); r != nil {
			// Log ALL panics (LuaError or otherwise) and recover gracefully
			stack := string(debug.Stack())
			api.logger.Errorf("PTY Lua callback panic (recovered): %v\nStack:\n%s", r, stack)

			// Send error to stderr for user visibility
			api.LuaEngine.outputChan.ForceSend(LuaOutputRecord{
				Content:   fmt.Sprintf("PTY callback error: %v", r),
				Timestamp: time.Now(),
				Source:    "stderr",
			})

			// DANGEROUS - DO NOT DO THIS:
			// Attempting to clean up Lua state after panic is unsafe because:
			// 1. SIGSEGV from Lua FFI code means the Lua VM is corrupted
			// 2. Calling L.SetTop(0) on corrupted state → another SIGSEGV
			// 3. Go's recover() cannot catch SIGSEGV - process will crash
			// 4. When L.Call() returns error normally, stack is already cleaned
			//
			// OLD DANGEROUS CODE (commented out):
			// api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
			//     L.SetTop(0) // ← SIGSEGV if state is corrupted
			//     return nil
			// })

			// DO NOT re-panic - allow PTY reading to continue
		}
	}()

	api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
		// Inner panic handler: catches panics from L.Call() (including StackTrace crashes)
		defer func() {
			if r := recover(); r != nil {
				// Re-panic to outer handler for cleanup
				panic(r)
			}
		}()

		// Save stack position before pushing callback items.
		// On error, we restore to this position (not 0) to preserve any stack frames
		// from the main script when callbacks run during blim.sleep().
		stackTop := L.GetTop()

		// Push the callback function onto the stack using reference
		L.RawGeti(lua.LUA_REGISTRYINDEX, callbackRef)

		// Push data as string argument (Lua string can contain binary data)
		L.PushString(string(data))

		// Call the function with one argument (the data string)
		// This can panic if StackTrace() crashes while building LuaError.
		// Track callback depth (panic-safe): the decrement MUST run even if L.Call panics, or the
		// count leaks and later main-loop blim.sleep/io.read would be wrongly rejected as in-callback.
		err := func() error {
			api.LuaEngine.enterCallback()
			defer api.LuaEngine.exitCallback()
			return L.Call(1, 0)
		}()
		if err != nil {
			// Log the error for debugging
			api.logger.Errorf("PTY Lua callback execution failed: %v", err)

			// Send error to an output channel for user visibility
			api.LuaEngine.outputChan.ForceSend(LuaOutputRecord{
				Content:   fmt.Sprintf("PTY callback error: %v", err),
				Timestamp: time.Now(),
				Source:    "stderr",
			})

			// Restore stack to pre-callback state to clean up callback items while
			// preserving any stack frames from the main script (callbacks may run
			// during blim.sleep). Using SetTop(0) would wipe the script's stack.
			//L.SetTop(0)
			L.SetTop(stackTop)
		}

		return nil
	})

	return nil
}

// callLuaCallback calls the Lua callback function with the record data and cancel function.
// The cancel function is passed as the second argument to enable self-cancellation from Lua.
func (api *LuaAPI) callLuaCallback(callbackRef int, record *device.Record, cancelFn func()) error {
	// Check shuttingDown BEFORE Add() to prevent new callbacks during shutdown.
	// This ensures luaCallbackWG.Wait() in Close() won't block on new callbacks.
	if api.shuttingDown.Load() {
		api.logger.Debugf("subscription:lua-callback skipped ref=%d reason=shutting_down", callbackRef)
		return nil
	}

	api.luaCallbackWG.Add(1)
	defer api.luaCallbackWG.Done()

	api.logger.Debugf("[callLuaCallback] entry, callbackRef=%d", callbackRef)
	if callbackRef == lua.LUA_NOREF {
		api.logger.Debug("[callLuaCallback] LUA_NOREF, returning")
		return nil
	}

	// Outer panic handler: catches ALL panics (including LuaError from StackTrace crashes)
	// This ensures one callback's error doesn't crash other subscriptions
	defer func() {
		if r := recover(); r != nil {
			// Log ALL panics (LuaError or otherwise) and recover gracefully
			stack := string(debug.Stack())
			api.logger.Errorf("Lua subscribe callback panic (recovered): %v\nStack:\n%s", r, stack)

			// Send error to stderr for user visibility
			api.LuaEngine.outputChan.ForceSend(LuaOutputRecord{
				Content:   fmt.Sprintf("Subscribe callback error: %v", r),
				Timestamp: time.Now(),
				Source:    "stderr",
			})

			// DANGEROUS - DO NOT DO THIS:
			// Attempting to clean up Lua state after panic is unsafe because:
			// 1. SIGSEGV from Lua FFI code means the Lua VM is corrupted
			// 2. Calling L.SetTop(0) on corrupted state → another SIGSEGV
			// 3. Go's recover() cannot catch SIGSEGV - process will crash
			// 4. When L.Call() returns error normally, stack is already cleaned
			//
			// OLD DANGEROUS CODE (commented out):
			// api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
			//     L.SetTop(0) // ← SIGSEGV if state is corrupted
			//     return nil
			// })

			// DO NOT re-panic - allow other subscriptions to continue
		}
	}()

	api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
		defer func() {
			if r := recover(); r != nil {
				// Re-panic to outer handler for cleanup
				panic(r)
			}
		}()

		// Save stack position before pushing callback items.
		// On error, we restore to this position (not 0) to preserve any stack frames
		// from the main script when callbacks run during blim.sleep().
		stackTop := L.GetTop()

		// Push the callback function onto the stack using reference
		L.RawGeti(lua.LUA_REGISTRYINDEX, callbackRef)

		// Create a record table (first argument)
		L.NewTable()

		// Set TsUs
		L.PushString("TsUs")
		L.PushInteger(record.TsUs)
		L.SetTable(-3)

		// Set Seq
		L.PushString("Seq")
		L.PushInteger(int64(record.Seq))
		L.SetTable(-3)

		// Set Flags
		L.PushString("Flags")
		L.PushInteger(int64(record.Flags))
		L.SetTable(-3)

		// Set Values table (for EveryUpdate/Aggregated modes)
		if record.Values != nil {
			L.PushString("Values")
			L.NewTable()
			for uuid, data := range record.Values {
				L.PushString(uuid)
				// Note: Converting []byte to string may cause issues with binary data,
				// but using byte arrays (60x more operations) kills performance for high-frequency BLE data.
				// Lua can still manipulate bytes using string.byte() on the result.
				L.PushString(string(data))
				L.SetTable(-3)
			}
			L.SetTable(-3)
		}

		// Set the BatchValues table (for Batched mode)
		if record.BatchValues != nil {
			L.PushString("BatchValues")
			L.NewTable()
			for uuid, dataArray := range record.BatchValues {
				L.PushString(uuid)
				L.NewTable()
				for i, data := range dataArray {
					L.PushInteger(int64(i + 1)) // Lua arrays are 1-indexed
					// Note: Converting []byte to string may cause issues with binary data,
					// but using byte arrays (60x more operations) kills performance for high-frequency BLE data.
					// Lua can still manipulate bytes using string.byte() on the result.
					L.PushString(string(data))
					L.SetTable(-3)
				}
				L.SetTable(-3)
			}
			L.SetTable(-3)
		}

		// Push cancel function (second argument) for self-cancellation from Lua
		api.SafePushGoFunction(L, "cancel", func(L *lua.State) int {
			if cancelFn != nil {
				cancelFn()
			}
			return 0
		})
		// SafePushGoFunction pushes name and function; we only need the function, so drop the name
		L.Remove(-2)

		// Call the function with 2 arguments (record table, cancel function).
		// This can panic if StackTrace() crashes while building LuaError.
		// Track callback depth (panic-safe): the decrement MUST run even if L.Call panics, or the
		// count leaks and later main-loop blim.sleep/io.read would be wrongly rejected as in-callback.
		err := func() error {
			api.LuaEngine.enterCallback()
			defer api.LuaEngine.exitCallback()
			return L.Call(2, 0)
		}()
		if err != nil {
			// Log the error for debugging
			api.logger.Errorf("Lua callback execution failed: %v", err)

			// Send error to an output channel for user visibility
			api.LuaEngine.outputChan.ForceSend(LuaOutputRecord{
				Content:   fmt.Sprintf("Callback error: %v", err),
				Timestamp: time.Now(),
				Source:    "stderr",
			})

			// Restore stack to pre-callback state to clean up callback items while
			// preserving any stack frames from the main script (callbacks may run
			// during blim.sleep). Using SetTop(0) would wipe the script's stack.
			L.SetTop(stackTop)
		}

		return nil
	})

	return nil
}

// registerCharacteristicFunction registers the ble.characteristic() function
func (api *LuaAPI) registerCharacteristicFunction(L *lua.State) {
	api.SafePushGoFunction(L, "characteristic", func(L *lua.State) int {
		// Validate arguments
		if !L.IsString(1) || !L.IsString(2) {
			L.RaiseError("characteristic(service_uuid, char_uuid) expects two string arguments")
			return 0
		}

		serviceUUID := L.ToString(1)
		charUUID := L.ToString(2)

		// Get connection when a function is called, not when registered
		connection := api.device.GetConnection()
		if connection == nil {
			L.RaiseError("no connection available")
			return 0
		}

		// Get characteristic from connection
		char, err := connection.GetCharacteristic(serviceUUID, charUUID)
		if err != nil {
			L.RaiseError(fmt.Sprintf("characteristic not found: %v", err))
			return 0
		}

		// Create a handle table with metadata fields
		L.NewTable()

		// Field: uuid
		L.PushString("uuid")
		L.PushString(char.UUID())
		L.SetTable(-3)

		// Field: name (only if known)
		if knownName := char.KnownName(); knownName != "" {
			L.PushString("name")
			L.PushString(knownName)
			L.SetTable(-3)
		}

		// Field: service_uuid
		L.PushString("service")
		L.PushString(serviceUUID)
		L.SetTable(-3)

		// Field: properties (dual-purpose table with array and hash parts)
		// - Array part: ordered iteration with ipairs() in bit order
		// - Hash part: boolean checks (if properties.read then)
		L.PushString("properties")
		L.NewTable()

		// Convert Properties struct to Lua table
		props := char.GetProperties()

		// Helper function to add a property to both array and hash parts
		arrayIndex := 1
		addProp := func(prop device.Property, key string) {
			if prop != nil {
				// Create property sub-table
				L.NewTable()
				L.PushString("value")
				L.PushInteger(int64(prop.Value()))
				L.SetTable(-3)
				L.PushString("name")
				L.PushString(prop.KnownName())
				L.SetTable(-3)

				// Add to hash part (for named access: properties.read)
				L.PushString(key) // Stack: [props_table, prop_table, key]
				L.PushValue(-2)   // Stack: [props_table, prop_table, key, prop_table]
				L.SetTable(-4)    // props_table[key] = prop_table; Stack: [props_table, prop_table]

				// Add to array part (for ordered iteration: ipairs(properties))
				L.PushInteger(int64(arrayIndex)) // Stack: [props_table, prop_table, arrayIndex]
				L.PushValue(-2)                  // Stack: [props_table, prop_table, arrayIndex, prop_table]
				L.SetTable(-4)                   // props_table[arrayIndex] = prop_table; Stack: [props_table, prop_table]

				// Pop the property table
				L.Pop(1)
				arrayIndex++
			}
		}

		// Add properties in bit order (0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80)
		addProp(props.Broadcast(), "broadcast")
		addProp(props.Read(), "read")
		addProp(props.WriteWithoutResponse(), "write_without_response")
		addProp(props.Write(), "write")
		addProp(props.Notify(), "notify")
		addProp(props.Indicate(), "indicate")
		addProp(props.AuthenticatedSignedWrites(), "authenticated_signed_writes")
		addProp(props.ExtendedProperties(), "extended_properties")

		L.SetTable(-3)

		// Field: descriptors (array of objects with uuid, handle, name, value, and parsed_value)
		L.PushString("descriptors")
		L.NewTable()
		descriptors := char.GetDescriptors()
		for i, desc := range descriptors {
			L.PushInteger(int64(i + 1))
			api.pushDescriptor(L, desc)
			L.SetTable(-3)
		}
		L.SetTable(-3)

		// Field: has_parser (true if a parser is registered for this characteristic type)
		L.PushString("has_parser")
		L.PushBoolean(char.HasParser())
		L.SetTable(-3)

		// Field: requires_authentication (true if characteristic requires pairing/authentication)
		L.PushString("requires_authentication")
		L.PushBoolean(char.RequiresAuthentication())
		L.SetTable(-3)

		// Method: read() - reads the characteristic value from the device
		// Returns (value, nil) on success or (nil, error_message) on failure
		api.SafePushGoFunction(L, "read", func(L *lua.State) int {
			// Guard (issue #3): a device read from inside a callback blocks the callback goroutine while
			// holding the state, and reading a subscribed characteristic from its own callback
			// self-deadlocks (CoreBluetooth routes the read response through the blocked notification
			// fan-out). Fail by RETURNING (nil, msg), NOT via RaiseError: golua RaiseError is a Go panic
			// that bypasses the LuaJIT VM unwinder and, when recovered mid-callback, corrupts the
			// lua_State (crashing the process later). read() already reports failures as (nil, msg), so
			// scripts handle this like any read error.
			if api.LuaEngine.inCallback() {
				L.PushNil()
				L.PushString("read() is not allowed inside a subscribe/PTY callback; defer to the main loop")
				return 2
			}

			value, err := char.Read(api.characteristicReadTimeout)
			if err != nil {
				L.PushNil()
				L.PushString(fmt.Sprintf("read() failed: %s", stripWrappedGoErrorSuffix(err.Error())))
				return 2
			}

			L.PushString(string(value))
			L.PushNil()
			return 2 // (value, nil)
		})
		L.SetTable(-3)

		// Method: write(data, [with_response]) - writes data to the characteristic
		// Parameters:
		//   - data: string - data to write (will be converted to bytes)
		//   - with_response: boolean (optional) - whether to wait for write response (default: true)
		// Returns (true, nil) on success or (nil, error_message) on failure
		api.SafePushGoFunction(L, "write", func(L *lua.State) int {
			// Guard (issue #3): like read(), a device write from inside a callback is unsafe. Fail by
			// RETURNING (nil, msg), not via RaiseError (a Go panic that corrupts the recovered
			// lua_State). Checked first so it fires regardless of argument shape.
			if api.LuaEngine.inCallback() {
				L.PushNil()
				L.PushString("write() is not allowed inside a subscribe/PTY callback; defer to the main loop")
				return 2
			}

			// Validate first argument (data)
			if !L.IsString(1) {
				L.RaiseError("write(data, [with_response]) expects string as first argument")
				return 0
			}

			data := []byte(L.ToString(1))

			// Parse optional with_response parameter (default: true)
			withResponse := true
			if L.GetTop() >= 2 {
				if !L.IsBoolean(2) && !L.IsNil(2) {
					L.RaiseError("write(data, [with_response]) expects boolean as second argument")
					return 0
				}
				if L.IsBoolean(2) {
					withResponse = L.ToBoolean(2)
				}
			}

			// Use the abstracted CharacteristicWriter interface with timeout
			err := char.Write(data, withResponse, api.characteristicWriteTimeout)
			if err != nil {
				// Return (nil, error_message) for expected errors
				// Strip wrapped Go error suffix for cleaner Lua messages
				L.PushNil()
				L.PushString(fmt.Sprintf("write() failed: %s", stripWrappedGoErrorSuffix(err.Error())))
				return 2
			}
			// Return (true, nil) on success
			L.PushBoolean(true)
			L.PushNil()
			return 2
		})
		L.SetTable(-3)

		// Method: parse(value) - parses characteristic value (only for characteristics with registered parsers)
		// Returns parsed value or nil if parse error
		if char.HasParser() {
			api.SafePushGoFunction(L, "parse", func(L *lua.State) int {
				// Validate argument
				// Note: when called as char:parse(value), char is passed as arg 1, value as arg 2 (colon syntax)
				if !L.IsString(2) {
					L.RaiseError("parse(value) expects a string argument")
					return 0
				}

				// Get value to parse (argument 2 due to colon syntax)
				value := []byte(L.ToString(2))

				// Parse the value using the characteristic's registered parser
				parsed, err := char.ParseValue(value)
				if err != nil || parsed == nil {
					L.PushNil()
					return 1
				}

				// Return parsed value (currently only a string type is supported for Appearance)
				if str, ok := parsed.(string); ok {
					L.PushString(str)
				} else {
					L.PushNil()
				}
				return 1
			})
			L.SetTable(-3)
		}

		// TODO: Add methods (subscribe, unsubscribe) if needed

		return 1
	})
	L.SetTable(-3)
}

// registerSleepFunction registers the blim.sleep() utility function
// Usage: blim.sleep(milliseconds)
// Sleeps for the specified number of milliseconds.
// IMPORTANT: sleep releases the Lua state mutex during sleep to allow subscription
// callbacks to execute. This enables polling loops to receive BLE notifications.
// The sleep is context-aware and will return early if the execution context is cancelled.
func (api *LuaAPI) registerSleepFunction(L *lua.State) {
	api.SafePushGoFunction(L, "sleep", func(L *lua.State) int {
		// Validate argument
		if !L.IsNumber(1) {
			L.RaiseError("sleep(milliseconds) expects a number argument")
			return 0
		}

		ms := L.ToInteger(1)
		if ms < 0 {
			L.RaiseError("sleep(milliseconds) expects a non-negative number")
			return 0
		}

		// Guard (issue #3): blim.sleep releases stateMutex so other work runs during the wait; from
		// inside a callback that hands the in-flight lua_State to another goroutine and corrupts it.
		// Fail by RETURNING (nil, msg) BEFORE the release, not via RaiseError (a Go panic that bypasses
		// the LuaJIT VM unwinder and corrupts the recovered lua_State). Also covers
		// blim.term.read_char(wait_ms). (Cancellation below still raises Go-side — it is terminal.)
		if api.LuaEngine.inCallback() {
			L.PushNil()
			L.PushString("blim.sleep is not allowed inside a subscribe/PTY callback; defer to the main loop")
			return 2
		}

		// Get execution context for cancellation support
		ctx := api.LuaEngine.scriptExecutionCtx

		// Release mutex to allow callbacks to execute during the wait.
		// Re-lock EXPLICITLY (not via defer) before any RaiseError below: RaiseError walks the Lua
		// stack (StackTrace -> lua_getinfo), which is unsafe while the mutex is released — it races
		// callback goroutines on the same lua_State (the corruption class of #3). Keep the window
		// between Unlock and Lock free of any early return or panic.
		api.LuaEngine.stateMutex.Unlock()
		cancelled := false
		if ctx != nil {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
				// Sleep completed normally
			case <-ctx.Done():
				cancelled = true
			}
		} else {
			// Fallback to regular sleep if no context (shouldn't happen in normal usage)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		api.LuaEngine.stateMutex.Lock()

		// Deterministic cancellation (issue #4): raise here instead of relying on the debug count
		// hook, which LuaJIT never invokes inside compiled traces — an FFI-hot loop would otherwise
		// spin forever after Ctrl+C (blim.sleep used to just return on ctx.Done). This Go-side raise
		// is not catchable by Lua pcall (Go-backed errors abort by design), so scripts cannot swallow
		// it; safeExecuteScript maps the "cancelled" message to context.Canceled.
		if cancelled {
			api.logger.Debug("[blim.sleep] interrupted by context cancellation")
			L.RaiseError("script execution cancelled")
		}

		return 0
	})
	L.SetTable(-3)
}

// NOTE: blim.pcall is deliberately NOT a Go-registered function (it lives in
// blim.lua as LuaJIT's native pcall). golua raises errors as Go panics
// (RaiseError, golua_default_msghandler) that are recoverable ONLY at the
// outermost golua entry point (DoString/Call): recovering them in a nested
// Go function abandons live LuaJIT C frames and leaves the state's cframe
// chain dangling — SIGSEGV on the next throw or lua_close. Do not reintroduce
// a Go-side protected call unless golua's error architecture changes.

// pushDescriptor pushes a descriptor object onto the Lua stack as a table.
// Creates a table with uuid, handle, index, name, value, and parsed_value fields.
// Stack effect: pushes one table
func (api *LuaAPI) pushDescriptor(L *lua.State, desc device.Descriptor) {
	// Create descriptor object
	L.NewTable()
	// Add uuid
	L.PushString("uuid")
	L.PushString(desc.UUID())
	L.SetTable(-3)
	// Add a handle
	L.PushString("handle")
	L.PushInteger(int64(desc.Handle()))
	L.SetTable(-3)
	// Add index
	L.PushString("index")
	L.PushInteger(int64(desc.Index()))
	L.SetTable(-3)
	// Add name (only if known)
	if knownName := desc.KnownName(); knownName != "" {
		L.PushString("name")
		L.PushString(knownName)
		L.SetTable(-3)
	}
	// Add value (raw bytes as hex string)
	if value := desc.Value(); value != nil {
		L.PushString("value")
		L.PushString(fmt.Sprintf("%X", value))
		L.SetTable(-3)
	}
	// Add parsed_value (structured table for known descriptors)
	if parsedValue := desc.ParsedValue(); parsedValue != nil {
		L.PushString("parsed_value")
		api.pushDescriptorParsedValue(L, parsedValue)
		L.SetTable(-3)
	}
}

// pushDescriptorParsedValue pushes a parsed descriptor value onto the Lua stack as a table.
// Handles all known descriptor types (ExtendedProperties, ClientConfig, etc.) and error states.
// Stack effect: pushes one value (table or string)
func (api *LuaAPI) pushDescriptorParsedValue(L *lua.State, parsedValue interface{}) {
	switch v := parsedValue.(type) {
	case *device.ExtendedProperties:
		// Push ExtendedProperties as {reliable_write=bool, writable_auxiliaries=bool}
		L.NewTable()
		L.PushString("reliable_write")
		L.PushBoolean(v.ReliableWrite)
		L.SetTable(-3)
		L.PushString("writable_auxiliaries")
		L.PushBoolean(v.WritableAuxiliaries)
		L.SetTable(-3)

	case *device.ClientConfig:
		// Push ClientConfig as {notifications=bool, indications=bool}
		L.NewTable()
		L.PushString("notifications")
		L.PushBoolean(v.Notifications)
		L.SetTable(-3)
		L.PushString("indications")
		L.PushBoolean(v.Indications)
		L.SetTable(-3)

	case *device.ServerConfig:
		// Push ServerConfig as {broadcasts=bool}
		L.NewTable()
		L.PushString("broadcasts")
		L.PushBoolean(v.Broadcasts)
		L.SetTable(-3)

	case *device.PresentationFormat:
		// Push PresentationFormat as {format=int, exponent=int, unit=int, namespace=int, description=int}
		L.NewTable()
		L.PushString("format")
		L.PushInteger(int64(v.Format))
		L.SetTable(-3)
		L.PushString("exponent")
		L.PushInteger(int64(v.Exponent))
		L.SetTable(-3)
		L.PushString("unit")
		L.PushInteger(int64(v.Unit))
		L.SetTable(-3)
		L.PushString("namespace")
		L.PushInteger(int64(v.Namespace))
		L.SetTable(-3)
		L.PushString("description")
		L.PushInteger(int64(v.Description))
		L.SetTable(-3)

	case *device.ValidRange:
		// Push ValidRange as {min=hex_string, max=hex_string}
		L.NewTable()
		L.PushString("min")
		L.PushString(fmt.Sprintf("%X", v.MinValue))
		L.SetTable(-3)
		L.PushString("max")
		L.PushString(fmt.Sprintf("%X", v.MaxValue))
		L.SetTable(-3)
	case *device.AggregateFormat:
		// Push AggregateFormat as an indexed array of descriptor objects
		// Each descriptor has the same structure as char.descriptor
		L.NewTable()
		for i, desc := range *v {
			L.PushInteger(int64(i + 1)) // Lua arrays are 1-indexed
			api.pushDescriptor(L, desc)
			L.SetTable(-3) // array[i] = descriptor_table
		}

	case string:
		// User Description - push as a plain string
		L.PushString(v)

	case []byte:
		// Unknown descriptor type - push raw bytes as hex string
		L.PushString(fmt.Sprintf("%X", v))

	case *device.DescriptorError:
		// Push DescriptorError as {error=string}
		L.NewTable()
		L.PushString("error")
		L.PushString(v.Error())
		L.SetTable(-3)

	default:
		// Fallback for unexpected types - push nil
		L.PushNil()
	}
}

// pushManufacturerParsedData pushes parsed manufacturer data onto the Lua stack as a table.
// Handles all known manufacturer data types (BlimManufacturerData, etc.).
// If parsedData implements the VendorInfo interface, vendor info is included first.
// Stack effect: pushes one value (table)
func (api *LuaAPI) pushManufacturerParsedData(L *lua.State, parsedData interface{}) {
	L.NewTable()

	// Add vendor info first if parsedData implements VendorInfo
	if vendorInfo, ok := parsedData.(device.VendorInfo); ok {
		L.PushString("vendor")
		L.NewTable()

		L.PushString("id")
		L.PushInteger(int64(vendorInfo.VendorID()))
		L.SetTable(-3)

		if vendorName := vendorInfo.VendorName(); vendorName != "" {
			L.PushString("name")
			L.PushString(vendorName)
			L.SetTable(-3)
		}

		L.SetTable(-3) // Set vendor table
	}

	// Add manufacturer-specific fields
	switch v := parsedData.(type) {
	case *device.BlimManufacturerData:
		L.PushString("device_type")
		L.PushString(v.DeviceType.String())
		L.SetTable(-3)
		L.PushString("hardware_version")
		L.PushString(v.HardwareVersion)
		L.SetTable(-3)
		L.PushString("firmware_version")
		L.PushString(v.FirmwareVersion)
		L.SetTable(-3)

	default:
		// Unknown manufacturer data type - table already created with vendor if available
	}
}

// Close cleans up the API resources. Idempotent and thread-safe.
// Shutdown sequence ensures no race between BLE callbacks and Lua state destruction:
// 1. Disconnect device first to stop new notifications/callbacks
// 2. Wait for all in-flight callbacks to complete
// 3. Clean up Lua registry refs (safe now, no callbacks accessing state)
// 4. Close Lua engine
func (api *LuaAPI) Close() {
	api.closeOnce.Do(func() {
		if api.logger != nil {
			api.logger.WithField("lua_api_ptr", fmt.Sprintf("%p", api)).Debug("Closing lua api...")
		}

		// 1. Set shuttingDown flag to block new callbacks from starting
		api.shuttingDown.Store(true)

		// 2. Disconnect device to stop new notifications/callbacks
		api.device.Disconnect()

		// 3. Wait for all in-flight callbacks to complete (they will finish naturally)
		api.luaCallbackWG.Wait()

		// 3. Now safe to clean up Lua registry refs
		api.LuaEngine.DoWithState(func(L *lua.State) interface{} {
			api.releasePTYCallbackRef(L)

			// Collect refs to release (snapshot under lock)
			api.callbackRefsMu.Lock()
			refsToRelease := make([]int, 0, len(api.subscriptionCallbackRefs))
			for ref := range api.subscriptionCallbackRefs {
				refsToRelease = append(refsToRelease, ref)
			}
			api.callbackRefsMu.Unlock()

			if api.logger != nil {
				api.logger.Debugf("subscription:cleanup starting refs=%v", refsToRelease)
			}

			// Release each ref via releaseCallbackRefInternal()
			for _, ref := range refsToRelease {
				api.releaseCallbackRefInternal(ref)
			}

			if api.logger != nil {
				api.logger.Debugf("subscription:cleanup complete")
			}

			return nil
		})

		// 4. Close Lua engine
		api.LuaEngine.Close()

		if api.logger != nil {
			api.logger.WithField("lua_api_ptr", fmt.Sprintf("%p", api)).Debug("Lua api closed")
		}
	})
}
