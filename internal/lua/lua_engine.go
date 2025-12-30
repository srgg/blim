package lua

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	_ "embed"

	"github.com/aarzilli/golua/lua"
	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/groutine"
)

//go:embed lua-libs/json.lua
var jsonLua string // json.lua is embedded into this string

const (
	// DefaultOutputChannelCapacity is the default capacity for the Lua output ring buffer.
	// This buffer stores print() and io.write() output from Lua scripts.
	// When capacity is reached, the ring buffer overwrites the oldest records (output loss).
	// Increase this value for scripts with high-frequency output to prevent record loss.
	DefaultOutputChannelCapacity int = 100
)

// LuaOutputRecord represents a single output record from Lua script execution
type LuaOutputRecord struct {
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"` // "stdout" or "stderr"
}

// LuaError represents detailed Lua execution errors
type LuaError struct {
	Type       string // "syntax", "runtime", "api"
	Message    string
	Line       int
	Source     string
	StackTrace string
	Underlying error
}

func (e *LuaError) Error() string {
	parts := []string{}
	if e.Source != "" {
		parts = append(parts, fmt.Sprintf("in %s", e.Source))
	}
	if e.Line > 0 {
		parts = append(parts, fmt.Sprintf("line %d", e.Line))
	}

	prefix := "Lua error"
	if len(parts) > 0 {
		prefix = fmt.Sprintf("Lua %s error (%s)", e.Type, strings.Join(parts, ", "))
	}
	result := fmt.Sprintf("%s: %s", prefix, e.Message)
	if e.StackTrace != "" {
		result += "\n" + e.StackTrace
	}
	return result
}

func (e *LuaError) Unwrap() error {
	return e.Underlying
}

func (e *LuaError) Is(target error) bool {
	if target == nil {
		return false
	}
	var luaErr *LuaError
	if errors.As(target, &luaErr) {
		return e.Type == luaErr.Type
	}
	return false
}

// FairLock is a channel-based lock with FIFO fairness guarantees.
// Unlike sync.Mutex, blocked goroutines are served in order they called Lock().
// This prevents starvation when multiple goroutines compete for the lock.
type FairLock chan struct{}

// NewFairLock creates a new fair lock in unlocked state.
func NewFairLock() FairLock {
	lock := make(FairLock, 1)
	lock <- struct{}{} // Start unlocked (token available)
	return lock
}

// Lock acquires the lock. Blocks until lock is available.
// Blocked goroutines are served in FIFO order.
func (l FairLock) Lock() {
	<-l // Receive token (FIFO queue for waiters)
}

// Unlock releases the lock.
func (l FairLock) Unlock() {
	l <- struct{}{} // Return token
}

// LuaEngine represents the Lua engine with full output capture
type LuaEngine struct {
	state      *lua.State
	stateMutex FairLock // Channel-based fair lock (FIFO) to prevent starvation
	logger     *logrus.Logger
	scriptCode string
	outputChan *RingChannel[LuaOutputRecord] // ring buffer for Lua outputs
}

// NewLuaEngine creates a new Lua engine with full stdout/stderr capture using the default channel capacity
func NewLuaEngine(logger *logrus.Logger) *LuaEngine {
	return NewLuaEngineWithOutputChannelCapacity(logger, DefaultOutputChannelCapacity)
}

// NewLuaEngineWithOutputChannelCapacity creates a new Lua engine with a custom output channel capacity.
// The capacity parameter controls the ring buffer capacity for Lua output (print/io.write).
// When capacity is reached, the ring buffer overwrites the oldest records (output loss).
// Use a larger capacity for scripts with high-frequency output to prevent record loss.
func NewLuaEngineWithOutputChannelCapacity(logger *logrus.Logger, capacity int) *LuaEngine {
	engine := &LuaEngine{
		logger:     logger,
		stateMutex: NewFairLock(),
		outputChan: NewRingChannel[LuaOutputRecord](capacity),
	}

	engine.Reset()

	logger.WithField("output_capacity", capacity).Info("LuaEngine initialized with full Lua output capture")
	return engine
}

// SafeWrapGoFunction wraps a Go function with panic recovery and error logging.
// ALL Lua-exposed Go functions MUST be wrapped to ensure:
// - Expected Lua errors (from L.RaiseError) propagate correctly
// - Unexpected panics are logged with full stack trace for debugging
// - Unexpected panics are converted to clean Lua errors (L.RaiseError)
// - Consistent error handling across all Go-Lua boundaries
func (e *LuaEngine) SafeWrapGoFunction(name string, fn func(*lua.State) int) func(*lua.State) int {
	return func(L *lua.State) int {
		defer func() {
			if r := recover(); r != nil {
				// If it's a *lua.LuaError, it's from L.RaiseError - re-panic as-is
				if _, ok := r.(*lua.LuaError); ok {
					panic(r)
				}

				// Otherwise it's an unexpected panic (including string panics) - LOG and raise generic error
				stack := string(debug.Stack())
				e.logger.Errorf("%s unexpected panic: %v\nStack:\n%s", name, r, stack)
				L.RaiseError(fmt.Sprintf("%s panicked in Go", name))
			}
		}()
		return fn(L)
	}
}

func (e *LuaEngine) DoWithState(callback func(*lua.State) interface{}) interface{} {
	e.logger.Debug("[DoWithState] waiting for mutex...")
	e.stateMutex.Lock()
	e.logger.Debug("[DoWithState] mutex acquired")
	defer e.stateMutex.Unlock()

	if e.state == nil {
		return nil
	}
	return callback(e.state)
}

func (e *LuaEngine) doWithStateInternal(callback func(*lua.State) interface{}) interface{} {
	if e.state == nil {
		return nil
	}
	return callback(e.state)
}

func (e *LuaEngine) registerBlockedLuaFunctions() {

	blockingLuaFunctions := []string{
		"os.execute",
		"os.exit",
		"os.remove",
		"os.rename",
		//"io.read", // allowed for interactive bridge scripts
		"io.lines",
		"file:read",
		//"require",
		"dofile",
		"loadfile",
	}

	e.doWithStateInternal(func(L *lua.State) interface{} {
		for _, fullName := range blockingLuaFunctions {
			parts := strings.Split(fullName, ".")
			blockFn := func(full string) lua.LuaGoFunction {
				return func(L *lua.State) int {
					L.RaiseError(full + " is blocked")
					return 0
				}
			}(fullName)

			if len(parts) == 1 {
				// Global function
				L.PushGoFunction(blockFn)
				L.SetGlobal(parts[0])
			} else if len(parts) == 2 {
				// Table function
				tableName, funcName := parts[0], parts[1]
				L.GetGlobal(tableName)
				if L.IsNil(-1) {
					L.Pop(1)
					L.NewTable()
					L.PushValue(-1)
					L.SetGlobal(tableName)
				}
				L.PushGoFunction(blockFn)
				L.SetField(-2, funcName)
				L.Pop(1)
			}
		}

		return nil
	})
}

func (e *LuaEngine) registerPrintCaptureInternal() {
	e.doWithStateInternal(func(L *lua.State) interface{} {
		// Override print
		L.PushGoFunction(e.SafeWrapGoFunction("print()", func(L *lua.State) int {
			top := L.GetTop()
			parts := make([]string, 0, top)

			for i := 1; i <= top; i++ {
				if L.IsNil(i) {
					parts = append(parts, "nil")
				} else if L.IsBoolean(i) {
					if L.ToBoolean(i) {
						parts = append(parts, "true")
					} else {
						parts = append(parts, "false")
					}
				} else if L.IsNumber(i) {
					parts = append(parts, fmt.Sprintf("%v", L.ToNumber(i)))
				} else if L.IsString(i) {
					parts = append(parts, L.ToString(i))
				} else {
					// For tables, functions, threads, userdata: call Lua tostring()
					L.GetGlobal("tostring") // push global tostring
					L.PushValue(i)          // push value as argument
					L.Call(1, 1)            // call tostring(value)
					parts = append(parts, L.ToString(-1))
					L.Pop(1) // pop result
				}
			}

			// Join with tabs and append a newline
			line := strings.Join(parts, "\t") + "\n"

			// Send to RingChannel
			e.outputChan.ForceSend(LuaOutputRecord{
				Content:   line,
				Timestamp: time.Now(),
				Source:    "stdout",
			})

			return 0
		}))

		L.SetGlobal("print")

		return nil
	})
}

// registerIOWriteCaptureInternal overrides io.write and io.stderr:write to capture output to the output channel
func (e *LuaEngine) registerIOWriteCaptureInternal() {
	e.doWithStateInternal(func(L *lua.State) interface{} {
		// Override io.write to capture output
		L.GetGlobal("io")
		if L.IsTable(-1) {
			L.PushString("write")
			L.PushGoFunction(e.SafeWrapGoFunction("io.write()", func(L *lua.State) int {
				top := L.GetTop()
				var output strings.Builder

				for i := 1; i <= top; i++ {
					if L.IsString(i) {
						output.WriteString(L.ToString(i))
					} else if L.IsNumber(i) {
						output.WriteString(fmt.Sprintf("%v", L.ToNumber(i)))
					} else if L.IsBoolean(i) {
						if L.ToBoolean(i) {
							output.WriteString("true")
						} else {
							output.WriteString("false")
						}
					} else if L.IsNil(i) {
						output.WriteString("nil")
					} else {
						// For other types, use tostring
						L.GetGlobal("tostring")
						L.PushValue(i)
						L.Call(1, 1)
						output.WriteString(L.ToString(-1))
						L.Pop(1)
					}
				}

				// Send to RingChannel (no automatic newline like print)
				if output.Len() > 0 {
					e.outputChan.ForceSend(LuaOutputRecord{
						Content:   output.String(),
						Timestamp: time.Now(),
						Source:    "stdout",
					})
				}

				return 0
			}))
			L.SetTable(-3)

			// Create io.stderr table with write method for stderr capture
			// Stack: io table at -1
			L.PushString("stderr") // Stack: io, "stderr"
			L.NewTable()           // Stack: io, "stderr", stderr_table
			L.PushString("write")  // Stack: io, "stderr", stderr_table, "write"
			L.PushGoFunction(e.SafeWrapGoFunction("io.stderr:write()", func(L *lua.State) int {
				top := L.GetTop()
				var output strings.Builder

				// When called as io.stderr:write(), first arg is the table (self), skip it
				startIdx := 1
				if top > 0 && L.IsTable(1) {
					startIdx = 2
				}

				for i := startIdx; i <= top; i++ {
					if L.IsString(i) {
						output.WriteString(L.ToString(i))
					} else if L.IsNumber(i) {
						output.WriteString(fmt.Sprintf("%v", L.ToNumber(i)))
					} else if L.IsBoolean(i) {
						if L.ToBoolean(i) {
							output.WriteString("true")
						} else {
							output.WriteString("false")
						}
					} else if L.IsNil(i) {
						output.WriteString("nil")
					} else {
						// For other types, use tostring
						L.GetGlobal("tostring")
						L.PushValue(i)
						L.Call(1, 1)
						output.WriteString(L.ToString(-1))
						L.Pop(1)
					}
				}

				// Send to RingChannel as stderr
				if output.Len() > 0 {
					e.outputChan.ForceSend(LuaOutputRecord{
						Content:   output.String(),
						Timestamp: time.Now(),
						Source:    "stderr",
					})
				}

				return 0
			}))
			L.SetTable(-3) // Stack: io, "stderr", stderr_table (with write method set)
			L.SetTable(-3) // Stack: io (with stderr table set)
		}
		L.Pop(1) // Pop io table

		return nil
	})
}

// PreloadLuaLibrary loads a Lua library script into package.loaded[libraryName]
// This generic function follows the RegisterLibrary pattern and avoids package.preload callback issues.
func (e *LuaEngine) PreloadLuaLibrary(libraryCode, libraryName, errorContext string) {
	e.doWithStateInternal(func(L *lua.State) interface{} {
		// Load and execute the Lua module
		if err := L.LoadString(libraryCode); err != 0 {
			e.logger.Errorf("Failed to load embedded %s", errorContext)
			return nil
		}

		// Execute the chunk to get the module table
		L.Call(0, 1) // runs chunk -> pushes module table

		// Put it directly into package.loaded[libraryName] like RegisterLibrary does
		L.GetField(lua.LUA_GLOBALSINDEX, "package")
		L.GetField(-1, "loaded")
		L.PushValue(-3)             // Push the module table
		L.SetField(-2, libraryName) // package.loaded[libraryName] = module
		L.Pop(2)                    // Pop package and loaded
		return nil
	})
}

// preloadJSONLibInternal loads the embedded JSON.lua library directly into the package.loaded["json"]
// This avoids the package.preload callback issues and follows the RegisterLibrary pattern.
// The embedded JSON library provides json.encode() and json.decode() functions to Lua scripts.
func (e *LuaEngine) preloadJSONLibInternal() {
	e.PreloadLuaLibrary(jsonLua, "json", "json.lua")
}

// OutputChannel returns the output channel
func (e *LuaEngine) OutputChannel() <-chan LuaOutputRecord {
	return e.outputChan.C()
}

// parseLuaError extracts detailed info from Lua error messages
func (e *LuaEngine) parseLuaError(errType, source string) *LuaError {
	if e.state.GetTop() == 0 {
		return &LuaError{Type: errType, Message: "unknown Lua error", Source: source}
	}

	errMsg := ""
	if e.state.IsString(-1) {
		errMsg = e.state.ToString(-1)
	} else {
		errMsg = "non-string error object"
	}
	e.state.Pop(1)

	line := 0
	message := errMsg
	if strings.Contains(errMsg, ":") {
		parts := strings.SplitN(errMsg, ":", 3)
		if len(parts) >= 3 {
			if parsed, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &line); err == nil && parsed == 1 {
				message = strings.TrimSpace(parts[2])
			}
		}
	}

	return &LuaError{
		Type:    errType,
		Message: message,
		Line:    line,
		Source:  source,
	}
}

// safeExecuteScript executes a Lua script in the persistent state with context cancellation support.
//
// Cancellation mechanism:
//   - Runs script execution in a goroutine to avoid blocking
//   - Sets a Lua hook that checks ctx.Done() every 200 instructions
//   - If context is cancelled, raises a Lua error to stop execution
//   - Returns context.Canceled on cancellation, or the actual error otherwise.
func (e *LuaEngine) safeExecuteScript(ctx context.Context, script string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan error, 1)

	name := fmt.Sprintf("lua-script-executor-%d", len(script))
	groutine.Go(ctx, name, func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				e.outputChan.ForceSend(LuaOutputRecord{
					Content:   fmt.Sprintf("Panic during Lua execution: %v", r),
					Timestamp: time.Now(),
					Source:    "stderr",
				})
				done <- fmt.Errorf("panic during lua execution: %v", r)
			}
		}()

		var execErr error
		e.DoWithState(func(L *lua.State) interface{} {
			// Hook for cooperative cancellation
			L.SetHook(func(L *lua.State) {
				select {
				case <-ctx.Done():
					L.RaiseError("script execution cancelled")
				default:
				}
			}, 200)

			if err := L.DoString(script); err != nil {
				// Check if cancellation
				if ctx.Err() != nil || strings.Contains(err.Error(), "cancelled") {
					e.outputChan.ForceSend(LuaOutputRecord{
						Content:   "Lua execution cancelled",
						Timestamp: time.Now(),
						Source:    "stderr",
					})
					execErr = context.Canceled
					return nil
				}

				// Other Lua errors
				luaErr := e.parseLuaError("runtime", err.Error())
				e.outputChan.ForceSend(LuaOutputRecord{
					Content:   fmt.Sprintf("Lua error: %s", luaErr.Message),
					Timestamp: time.Now(),
					Source:    "stderr",
				})
				execErr = fmt.Errorf("lua execution failed: %w", err)
			}
			return nil
		})
		done <- execErr
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return context.Canceled
	}
}

// LoadScriptFile loads a Lua script from a file
func (e *LuaEngine) LoadScriptFile(filename string) error {
	content, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read script %s: %w", filename, err)
	}
	return e.LoadScript(string(content), filename)
}

// LoadScript loads a Lua script string and validates it
func (e *LuaEngine) LoadScript(script, name string) error {
	if script == "" {
		return &LuaError{Type: "api", Message: "empty script", Source: name}
	}

	e.scriptCode = script

	var loadErr error
	e.DoWithState(func(L *lua.State) interface{} {
		if status := L.LoadString(script); status != 0 {
			luaErr := e.parseLuaError("syntax", name)
			e.outputChan.Send(LuaOutputRecord{
				Content:   fmt.Sprintf("Lua syntax error: %s", luaErr.Message),
				Timestamp: time.Now(),
				Source:    "stderr",
			})
			L.Pop(1)
			loadErr = luaErr
			return nil
		}
		L.Pop(1)
		return nil
	})
	return loadErr
}

// ExecuteScript runs the loaded Lua script with context cancellation support in persistent state
func (e *LuaEngine) ExecuteScript(ctx context.Context, script string) error {
	if script != "" {
		e.LoadScript(script, "ad-hoc script")
	}
	if e.scriptCode == "" {
		return &LuaError{Type: "api", Message: "no script loaded"}
	}
	return e.safeExecuteScript(ctx, e.scriptCode)
}

func (e *LuaEngine) resetInternal() {
	if e.state != nil {
		e.state.Close()
	}

	e.state = lua.NewState()
	e.state.OpenLibs()

	e.registerPrintCaptureInternal()
	e.registerIOWriteCaptureInternal()
	e.preloadJSONLibInternal()
	e.registerBlockedLuaFunctions()
}

// Reset recreates the Lua state
func (e *LuaEngine) Reset() {
	e.stateMutex.Lock()
	defer e.stateMutex.Unlock()
	e.resetInternal()
}

// Close cleans up the engine
func (e *LuaEngine) Close() {
	e.stateMutex.Lock()
	defer e.stateMutex.Unlock()

	if e.state != nil {
		e.state.Close()
		e.state = nil
	}
}

// SetScriptSearchPaths configures Lua's package.path for module loading.
// It prepends the script's directory and any additional paths to the search path,
// enabling require() to find modules relative to the script location.
//
// Parameters:
//   - scriptPath: path to the main script file (its directory is added to search path)
//   - additionalPaths: optional extra directories to search for modules
func (e *LuaEngine) SetScriptSearchPaths(scriptPath string, additionalPaths []string) {
	e.DoWithState(func(L *lua.State) interface{} {
		// Get current package.path
		L.GetGlobal("package")
		L.GetField(-1, "path")
		currentPath := L.ToString(-1)
		L.Pop(1) // pop current path value

		// Build new path: script dir + additional paths + original path
		var pathParts []string

		// Add script's directory if provided
		if scriptPath != "" {
			absPath, err := filepath.Abs(scriptPath)
			if err == nil {
				scriptDir := filepath.Dir(absPath)
				pathParts = append(pathParts, scriptDir+"/?.lua")
				pathParts = append(pathParts, scriptDir+"/?/init.lua")
				e.logger.WithField("script_dir", scriptDir).Debug("Added script directory to Lua package.path")
			}
		}

		// Add additional paths
		for _, p := range additionalPaths {
			absPath, err := filepath.Abs(p)
			if err == nil {
				pathParts = append(pathParts, absPath+"/?.lua")
				pathParts = append(pathParts, absPath+"/?/init.lua")
				e.logger.WithField("path", absPath).Debug("Added additional path to Lua package.path")
			}
		}

		// Append original path at the end
		pathParts = append(pathParts, currentPath)

		// Set new package.path
		newPath := strings.Join(pathParts, ";")
		L.PushString(newPath)
		L.SetField(-2, "path")
		L.Pop(1) // pop package table

		e.logger.WithField("package_path", newPath).Debug("Configured Lua package.path")
		return nil
	})
}

// ExecuteFunction executes a specific Lua function by name
func (e *LuaEngine) ExecuteFunction(functionName string) error {
	var funcErr error
	e.DoWithState(func(L *lua.State) interface{} {
		// Get the function from a global scope
		L.GetGlobal(functionName)
		if !L.IsFunction(-1) {
			L.Pop(1)
			funcErr = fmt.Errorf("function %s not found or not a function", functionName)
			return nil
		}

		// Call the function with no arguments
		if err := L.Call(0, 0); err != nil {
			funcErr = fmt.Errorf("failed to call function %s: %w", functionName, err)
		}
		return nil
	})

	if funcErr == nil && e.state == nil {
		return fmt.Errorf("lua state not initialized")
	}

	return funcErr
}

// SetGlobal sets a global variable in the Lua state
func (e *LuaEngine) SetGlobal(name string, value interface{}) error {
	res := e.DoWithState(func(state *lua.State) any {
		switch v := value.(type) {
		case string:
			state.PushString(v)
		case int:
			state.PushInteger(int64(v))
		case int64:
			state.PushInteger(v)
		case float64:
			state.PushNumber(v)
		case bool:
			state.PushBoolean(v)
		default:
			return fmt.Errorf("unsupported type for global variable %s", name)
		}

		state.SetGlobal(name)
		return nil
	})

	// Type assert the result as an error
	if err, ok := res.(error); ok {
		return err
	}
	return nil
}

// GetGlobal gets a global variable from the Lua state
func (e *LuaEngine) GetGlobal(name string) interface{} {
	return e.DoWithState(func(state *lua.State) any {
		state.GetGlobal(name)
		defer state.Pop(1)

		switch {
		case state.IsString(-1):
			return state.ToString(-1)
		case state.IsNumber(-1):
			return state.ToNumber(-1)
		case state.IsBoolean(-1):
			return state.ToBoolean(-1)
		default:
			return nil // unsupported type
		}
	})
}

// GetGlobalInteger gets an integer global variable from the Lua state
func (e *LuaEngine) GetGlobalInteger(name string) (int, error) {
	var result int
	var err error

	e.DoWithState(func(state *lua.State) interface{} {
		state.GetGlobal(name)
		defer state.Pop(1)

		if !state.IsNumber(-1) {
			err = fmt.Errorf("global variable %s is not a number", name)
			return nil
		}

		result = int(state.ToInteger(-1))
		return nil
	})

	return result, err
}

// GetTableValue gets a string value from a Lua table by key
func (e *LuaEngine) GetTableValue(tableName, key string) (string, error) {
	var result string
	var err error

	e.DoWithState(func(state *lua.State) interface{} {
		state.GetGlobal(tableName)
		if !state.IsTable(-1) {
			state.Pop(1)
			err = fmt.Errorf("global %s is not a table", tableName)
			return nil
		}

		state.PushString(key)
		state.GetTable(-2)
		defer state.Pop(2) // pop value and table

		switch {
		case state.IsString(-1):
			result = state.ToString(-1)
		case state.IsNil(-1):
			err = fmt.Errorf("key %s not found in table %s", key, tableName)
		default:
			err = fmt.Errorf("value for key %s in table %s is not a string", key, tableName)
		}

		return nil
	})

	return result, err
}

// GetGlobalString gets a string global variable from the Lua state
func (e *LuaEngine) GetGlobalString(name string) (string, error) {
	var result string
	var err error

	e.DoWithState(func(state *lua.State) interface{} {
		state.GetGlobal(name)
		defer state.Pop(1)

		if !state.IsString(-1) {
			err = fmt.Errorf("global variable %s is not a string", name)
			return nil
		}

		result = state.ToString(-1)
		return nil
	})

	return result, err
}
