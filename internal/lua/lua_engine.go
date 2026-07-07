package lua

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "embed"

	"github.com/aarzilli/golua/lua"
	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/groutine"
	"github.com/srgg/blim/internal/ptyio"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

//go:embed lua-libs/json.lua
var jsonLua string // json.lua is embedded into this string

const (
	// DefaultOutputChannelCapacity is the default capacity for the Lua output ring buffer.
	// This buffer stores print() and io.write() output from Lua scripts.
	// When capacity is reached, the ring buffer overwrites the oldest records (output loss).
	// Increase this value for scripts with high-frequency output to prevent record loss.
	DefaultOutputChannelCapacity int = 100

	// DefaultScriptCancellationTimeout bounds how long safeExecuteScript waits for the script
	// goroutine to exit after the context is cancelled before it gives up and returns a diagnostic
	// error. A script with no cancellable boundary (e.g. a pure JIT/FFI loop that never calls a
	// blocking API) cannot be stopped cooperatively; this prevents an unbounded hang (issue #4).
	DefaultScriptCancellationTimeout = 5 * time.Second
)

// Package-level errors.
var (
	// ErrScriptCancellationTimeout is returned by ExecuteScript when a script does not stop within
	// the cancellation grace period — it has no cancellable boundary (e.g. a pure JIT/FFI loop that
	// never calls a blocking API) and could not be stopped cooperatively; the script goroutine stays
	// parked in cgo until process exit. Detect with errors.Is.
	ErrScriptCancellationTimeout = errors.New("script did not terminate within the cancellation grace period")
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

// shutdownHookKey is the context key for passing shutdown hooks to LuaEngine.
type shutdownHookKey struct{}

// ShutdownHook is a function that returns true if shutdown is in progress.
// When set via WithShutdownHook, LuaEngine checks this before context cancellation
// in the VM instruction hook, allowing graceful shutdown without raising errors.
// The hook receives the Lua state for potential state inspection during shutdown.
type ShutdownHook func(L *lua.State) bool

// WithShutdownHook returns a context with the given shutdown hook attached.
// The hook is checked every 200 VM instructions during script execution.
// If the hook returns true, the instruction hook returns early without error.
func WithShutdownHook(ctx context.Context, hook ShutdownHook) context.Context {
	return context.WithValue(ctx, shutdownHookKey{}, hook)
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
	state        *lua.State
	stateMutex   FairLock // Channel-based fair lock (FIFO) to prevent starvation
	logger       *logrus.Logger
	scriptCode   string
	outputChan   *RingChannel[LuaOutputRecord] // ring buffer for Lua outputs
	secureLoader *SecureModuleLoader           // Singleton: manages allowed paths for require()

	// scriptExecutionCtx is the context for the currently executing script.
	// Set at the start of safeExecuteScript, used by context-aware functions like blim.sleep().
	// Enables cancellation of blocking Go operations when the execution context is canceled.
	scriptExecutionCtx context.Context

	// Shutdown callbacks - invoked IN REGISTRATION ORDER when context is canceled,
	// before waiting for script goroutine. Uses an ordered map to guarantee FIFO execution.
	// Used by bridge to: 1) cancel subscriptions, 2) wait for callbacks, 3) close PTY.
	shutdownCallbacks   *orderedmap.OrderedMap[int, func()]
	shutdownCallbacksMu sync.Mutex
	nextShutdownID      int

	// stdinReader provides shared buffered reading from stdin.
	// Singleton: lazily initialized on first io.read() call to avoid overhead when unused.
	// Pre-reads from stdin into ring buffer, serves concurrent io.read() calls safely.
	stdinReader     *ptyio.PrefetchFileReader
	stdinReaderOnce sync.Once

	// inCallbackCount counts active callback L.Call() frames executing on the single lua_State
	// (BLE subscription callbacks + PTY data callbacks). Because execution is serialized by
	// stateMutex, only one goroutine runs Lua at a time, so >0 means "the current Lua executor is
	// inside a callback". Two consumers rely on this:
	//   - the shutdown hook skips RaiseError while >0 (an async raise from a debug hook is unsafe);
	//   - mutex-releasing primitives (blim.sleep, io.read) refuse to release while >0, since handing
	//     the in-flight state to another DoWithState reenters it from another goroutine and corrupts
	//     it (SIGSEGV in lua_getinfo — issue #3 / RFC-001).
	// Lives on LuaEngine (owner of the state and mutex) so LuaEngine-registered primitives like
	// io.read can consult it without depending upward on LuaAPI.
	inCallbackCount atomic.Int32

	// scriptCancellationTimeout bounds the post-cancellation wait for the script goroutine to exit
	// (see DefaultScriptCancellationTimeout). Configurable so tests can shorten it.
	scriptCancellationTimeout time.Duration
}

// enterCallback marks that a callback's L.Call() is starting on the shared state. MUST be paired
// with a deferred exitCallback so the count is balanced even if L.Call panics on a StackTrace crash;
// a leaked count would make later main-loop blim.sleep/io.read wrongly appear to be in-callback.
func (e *LuaEngine) enterCallback() { e.inCallbackCount.Add(1) }

// exitCallback marks that a callback's L.Call() has finished (or unwound via panic).
func (e *LuaEngine) exitCallback() { e.inCallbackCount.Add(-1) }

// inCallback reports whether the currently executing Lua context is a callback (BLE subscription or
// PTY data). Valid as an "in callback" indicator because execution is serialized by stateMutex.
// Primitives that are unsafe inside a callback consult this and fail by RETURNING an error value
// (nil, msg) — never via a Go-side RaiseError, which is a Go panic that bypasses the LuaJIT VM
// unwinder and corrupts the recovered lua_State (GH issue #3).
func (e *LuaEngine) inCallback() bool { return e.inCallbackCount.Load() > 0 }

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
		logger:                    logger,
		stateMutex:                NewFairLock(),
		outputChan:                NewRingChannel[LuaOutputRecord](capacity),
		scriptCancellationTimeout: DefaultScriptCancellationTimeout,
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

// AddShutdownHook registers a callback to be invoked when the context is canceled,
// before waiting for the script goroutine to finish. Returns a function to
// remove the hook. Hooks are invoked in registration order (FIFO).
// Used by bridge to: 1) cancel subscriptions, 2) wait for callbacks, 3) close PTY.
func (e *LuaEngine) AddShutdownHook(fn func()) (remove func()) {
	e.shutdownCallbacksMu.Lock()
	defer e.shutdownCallbacksMu.Unlock()

	if e.shutdownCallbacks == nil {
		e.shutdownCallbacks = orderedmap.New[int, func()]()
	}

	id := e.nextShutdownID
	e.nextShutdownID++
	e.shutdownCallbacks.Set(id, fn)

	return func() {
		e.shutdownCallbacksMu.Lock()
		defer e.shutdownCallbacksMu.Unlock()
		e.shutdownCallbacks.Delete(id)
	}
}

// invokeShutdownHooks calls all registered shutdown hooks in registration order (FIFO).
// Called when context is canceled, before waiting for the script goroutine.
func (e *LuaEngine) invokeShutdownHooks() {
	e.shutdownCallbacksMu.Lock()
	if e.shutdownCallbacks == nil {
		e.shutdownCallbacksMu.Unlock()
		return
	}
	// Collect callbacks in registration order
	callbacks := make([]func(), 0)
	for pair := e.shutdownCallbacks.Oldest(); pair != nil; pair = pair.Next() {
		callbacks = append(callbacks, pair.Value)
	}
	e.shutdownCallbacksMu.Unlock()

	for _, fn := range callbacks {
		fn()
	}
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

// getStdinReader returns the singleton BufferedReader for stdin.
// Lazily initialized on first call to avoid overhead when io.read() is unused.
func (e *LuaEngine) getStdinReader() *ptyio.PrefetchFileReader {
	e.stdinReaderOnce.Do(func() {
		var err error
		e.stdinReader, err = ptyio.NewPrefetchFileReader(os.Stdin, &ptyio.PrefetchFileReaderOptions{
			BufferCap: 4096,
			Logger:    e.logger,
		})
		if err != nil {
			e.logger.Errorf("Failed to create stdin reader: %v", err)
		}
	})
	return e.stdinReader
}

// registerIOReadContextAwareInternal overrides io.read() to be context-aware and release mutex during blocking.
// Uses a singleton BufferedReader for stdin to handle concurrent io.read() calls safely.
// Pattern: Table setup like io.write, mutex handling like blim.sleep()
func (e *LuaEngine) registerIOReadContextAwareInternal() {
	e.doWithStateInternal(func(L *lua.State) interface{} {
		// Same pattern as io.write: get io table, set read function
		L.GetGlobal("io")
		if L.IsTable(-1) {
			L.PushString("read")
			L.PushGoFunction(e.SafeWrapGoFunction("io.read()", func(L *lua.State) int {
				// Parse format argument: number means read N bytes, default "*l" for line
				var count int
				if L.GetTop() >= 1 && L.IsNumber(1) {
					count = L.ToInteger(1)
				}

				// Reentrancy guard (issue #3): io.read releases stateMutex to block. From inside a
				// callback that would hand the in-flight lua_State to another goroutine and corrupt it.
				// Fail by RETURNING (nil, msg) — io.read-style — BEFORE the mutex release, not via
				// RaiseError: golua RaiseError is a Go panic that bypasses the VM unwinder and corrupts
				// the recovered lua_State (crashing the process later). Scripts handle this like EOF.
				if e.inCallback() {
					L.PushNil()
					L.PushString("io.read() is not allowed inside a subscribe/PTY callback; defer to the main loop")
					return 2
				}

				// Get execution context for cancellation support
				ctx := e.scriptExecutionCtx
				if ctx == nil {
					ctx = context.Background()
				}

				// Get singleton stdin reader (lazy initialization)
				reader := e.getStdinReader()
				if reader == nil {
					e.logger.Error("[io.read] stdin reader unavailable")
					L.PushNil()
					return 1
				}

				// CRITICAL: Release mutex before blocking (like blim.sleep)
				// This allows other goroutines (e.g., subscription callbacks) to execute
				e.stateMutex.Unlock()

				var data string
				var err error

				if count > 0 {
					// Read exactly N bytes
					var bytes []byte
					bytes, err = reader.ReadBytes(ctx, count)
					if err == nil {
						data = string(bytes)
					}
				} else {
					// Read line (default behavior)
					data, err = reader.ReadLine(ctx)
					// Strip trailing newline for Lua compatibility
					data = strings.TrimSuffix(data, "\n")
				}

				// CRITICAL: Reacquire mutex BEFORE any Lua stack operations
				// (Using explicit Lock instead of defer to ensure mutex is held during L.Push*)
				e.stateMutex.Lock()

				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						e.logger.Debug("[io.read] interrupted by context cancellation")
					} else if !errors.Is(err, ptyio.ErrReaderClosed) {
						e.logger.Debugf("[io.read] error: %v", err)
					}
					L.PushNil()
					return 1
				}

				L.PushString(data)
				return 1
			}))
			L.SetTable(-3)
		}
		L.Pop(1) // Pop io table
		return nil
	})
}

// PreloadLuaLibrary loads a Lua library script into package.loaded[libraryName].
// This generic function follows the RegisterLibrary pattern and avoids package.preload callback issues.
//
// PANICS on failure: every caller preloads a go:embed asset compiled into the
// binary, so a load/execute failure means the binary itself is broken and MUST
// NOT degrade into a partially-initialized engine (scripts would later die with
// opaque nil-index errors). Callers must not recover this.
func (e *LuaEngine) PreloadLuaLibrary(libraryCode, libraryName, errorContext string) {
	result := e.doWithStateInternal(func(L *lua.State) interface{} {
		// Load and execute the Lua module
		if err := L.LoadString(libraryCode); err != 0 {
			msg := L.ToString(-1)
			L.Pop(1) // drop the error message; leaving it corrupts the persistent stack
			return fmt.Errorf("failed to load embedded %s: %s", errorContext, msg)
		}

		// Execute the chunk to get the module table. On failure the error message
		// stays on the stack; it MUST be popped, otherwise every later script error
		// walks a corrupted stack (LUA_ERRERR or SIGSEGV in lua_getinfo).
		if err := L.Call(0, 1); err != nil {
			L.Pop(1)
			return fmt.Errorf("failed to execute embedded %s: %w", errorContext, err)
		}

		// Put it directly into package.loaded[libraryName] like RegisterLibrary does
		L.GetField(lua.LUA_GLOBALSINDEX, "package")
		L.GetField(-1, "loaded")
		L.PushValue(-3)             // Push the module table
		L.SetField(-2, libraryName) // package.loaded[libraryName] = module
		L.Pop(2)                    // Pop package and loaded
		return nil
	})
	if err, ok := result.(error); ok {
		panic(err)
	}
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

// parseLuaErrorFromErr extracts detailed info from a *lua.LuaError using type assertion.
// This preserves the rich stack trace information from the golua library.
func (e *LuaEngine) parseLuaErrorFromErr(errType string, err error) *LuaError {
	luaErr, ok := err.(*lua.LuaError)
	if !ok {
		// Fallback: parse from error string
		return e.parseLuaErrorFromString(errType, err.Error())
	}

	result := &LuaError{
		Type:    errType,
		Message: luaErr.Error(),
	}

	// Extract line and source from stack trace
	// Skip C frames (CurrentLine = -1), find the first Lua frame
	stackTrace := luaErr.StackTrace()
	var stackLines []string
	for _, entry := range stackTrace {
		if entry.CurrentLine > 0 && result.Line == 0 {
			result.Line = entry.CurrentLine
			result.Source = entry.ShortSource
		}
		// Build stack trace string
		if entry.CurrentLine > 0 {
			stackLines = append(stackLines, fmt.Sprintf("  at %s (%s:%d)", entry.Name, entry.ShortSource, entry.CurrentLine))
		} else if entry.Name != "" {
			stackLines = append(stackLines, fmt.Sprintf("  at %s (%s)", entry.Name, entry.ShortSource))
		}
	}
	if len(stackLines) > 0 {
		result.StackTrace = strings.Join(stackLines, "\n")
	}

	// Clean up the message by removing the line prefix if present
	// Format: "[string "..."]:2: actual message"
	if strings.HasPrefix(result.Message, "[") && strings.Contains(result.Message, "]:") {
		if idx := strings.Index(result.Message, "]: "); idx != -1 {
			// Find the colon after line number
			afterBracket := result.Message[idx+2:]
			if colonIdx := strings.Index(afterBracket, ": "); colonIdx != -1 {
				result.Message = strings.TrimSpace(afterBracket[colonIdx+2:])
			}
		}
	}

	return result
}

// parseLuaErrorFromString extracts info from the Lua error message string.
// Used for syntax errors from LoadString where no *lua.LuaError is available.
func (e *LuaEngine) parseLuaErrorFromString(errType, source string) *LuaError {
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

	// Fail fast if context already set - indicates nested/concurrent execution bug
	if e.scriptExecutionCtx != nil {
		return fmt.Errorf("scriptExecutionCtx already set: nested or concurrent script execution not supported")
	}

	name := fmt.Sprintf("lua-script-executor-%d", len(script))
	groutine.Go(ctx, name, func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				e.outputChan.ForceSend(LuaOutputRecord{
					Content:   fmt.Sprintf("Panic during Lua execution: %v", r),
					Timestamp: time.Now(),
					Source:    "stderr",
				})
				e.scriptExecutionCtx = nil // Clear before signaling completion
				done <- fmt.Errorf("panic during lua execution: %v", r)
			}
		}()

		// Store context for use by context-aware functions (e.g., blim.sleep)
		e.scriptExecutionCtx = ctx

		var execErr error
		e.DoWithState(func(L *lua.State) interface{} {
			// Hook for cooperative cancellation - fires every 200 VM instructions.
			// Check shutdown hook first (if provided via context) to allow graceful
			// shutdown without raising errors - the hook returns early silently.
			shutdownHook, _ := ctx.Value(shutdownHookKey{}).(ShutdownHook)
			L.SetHook(func(L *lua.State) {
				// Shutdown hook check first - allows graceful exit without error
				if shutdownHook != nil && shutdownHook(L) {
					e.logger.Debug("[LuaHook] context provided shutdown hook triggered, skipping")
					return
				}
				select {
				case <-ctx.Done():
					e.logger.Debug("[LuaHook] context done, raising cancellation error")
					L.RaiseError("script execution cancelled")
				default:
					e.logger.Debug("[LuaHook] tick (context still active)")
				}
			}, 200)

			defer L.SetHook(nil, 0)

			e.logger.Debug("[safeExecuteScript] starting L.DoString (Lua script execution)...")
			err := L.DoString(script)
			e.logger.Debug("[safeExecuteScript] L.DoString returned (Lua script completed)")
			if err != nil {
				// Check if cancellation - stay silent, let the caller handle messaging
				if ctx.Err() != nil || strings.Contains(err.Error(), "cancelled") {
					// e.outputChan.ForceSend(LuaOutputRecord{
					// 	Content:   "Lua execution cancelled",
					// 	Timestamp: time.Now(),
					// 	Source:    "stderr",
					// })
					execErr = context.Canceled
					return nil
				}

				// Other Lua errors - use type assertion to preserve stack trace
				luaErr := e.parseLuaErrorFromErr("runtime", err)
				e.outputChan.ForceSend(LuaOutputRecord{
					Content:   fmt.Sprintf("Lua error: %s", luaErr.Message),
					Timestamp: time.Now(),
					Source:    "stderr",
				})
				execErr = fmt.Errorf("lua execution failed: %w", err)
			}
			return nil
		})
		e.scriptExecutionCtx = nil // Clear before signaling completion to avoid race
		done <- execErr
	})

	select {
	case err := <-done:
		e.logger.Debug("[safeExecuteScript] goroutine finished normally")
		return err
	case <-ctx.Done():
		// Invoke shutdown hooks first to unblock any blocking operations (e.g., close PTY to unblock io.read())
		e.logger.Debug("[safeExecuteScript] context cancelled, invoking shutdown hooks...")
		e.invokeShutdownHooks()

		// Wait for the script goroutine (L.DoString) to finish, but BOUND the wait. With the
		// deterministic cancellation of blocking APIs (blim.sleep raises on ctx.Done) the goroutine
		// returns almost immediately. A script with no cancellable boundary (a pure JIT/FFI loop that
		// never calls a blocking API) can never be stopped cooperatively — the LuaJIT count hook does
		// not fire inside compiled traces. Rather than hang forever (issue #4), give up after
		// scriptCancellationTimeout, dump goroutines for diagnosis, and return. The abandoned
		// goroutine stays parked in cgo until process exit.
		e.logger.Debug("[safeExecuteScript] waiting for goroutine...")
		select {
		case <-done:
			e.logger.Debug("[safeExecuteScript] goroutine finished after cancellation")
			return context.Canceled
		case <-time.After(e.scriptCancellationTimeout):
			e.logger.Errorf("[safeExecuteScript] script did not stop within %s of cancellation; dumping goroutines", e.scriptCancellationTimeout)
			_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
			return fmt.Errorf("%w (%s)", ErrScriptCancellationTimeout, e.scriptCancellationTimeout)
		}
	}
}

// LoadScriptFile loads a Lua script from a file.
// Uses LoadScriptFromPath, which supports project-root-relative paths (starting with "/").
func (e *LuaEngine) LoadScriptFile(filename string) error {
	content, err := LoadScriptFromPath(filename)
	if err != nil {
		return err
	}
	return e.LoadScript(content, filename)
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
			luaErr := e.parseLuaErrorFromString("syntax", name)
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
		err := e.LoadScript(script, "ad-hoc script")
		if err != nil {
			return err
		}
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
	e.registerIOReadContextAwareInternal()
	e.preloadJSONLibInternal()
	e.registerBlockedLuaFunctions()

	// Create SecureModuleLoader singleton (empty allowedPaths by default)
	// and register secure require() - must be AFTER blocking dangerous functions
	e.secureLoader = NewSecureModuleLoader(e.logger)
	e.secureLoader.Register(e)
}

// Reset recreates the Lua state
func (e *LuaEngine) Reset() {
	e.stateMutex.Lock()
	defer e.stateMutex.Unlock()
	e.resetInternal()
}

// Close cleans up the engine
func (e *LuaEngine) Close() {
	e.logger.Debug("[LuaEngine.Close] acquiring mutex...")
	e.stateMutex.Lock()
	e.logger.Debug("[LuaEngine.Close] mutex acquired")
	defer e.stateMutex.Unlock()

	// Close stdin reader if it was initialized (stops background goroutines)
	if e.stdinReader != nil {
		e.logger.Debug("[LuaEngine.Close] closing stdin reader...")
		_ = e.stdinReader.Close()
		e.stdinReader = nil
	}

	if e.state != nil {
		// Clear the debug hook before closing - the hook closure may reference
		// an invalid context after cancellation. Use no-op function (not nil)
		// as golua may not handle nil safely.
		e.logger.Debug("[LuaEngine.Close] clearing debug hook...")
		e.state.SetHook(func(*lua.State) {}, 0)

		// TEMPORARILY DISABLED: GC calls moved crash earlier, suggesting corruption happens before close
		// // Run full GC cycle while state is still valid. This ensures all __gc
		// // finalizers (for Go functions registered via PushGoFunction) run while
		// // the Lua registry is intact. Without this, lua_close() runs finalizers
		// // during teardown, when the registry is partially destroyed, causing
		// // gchook_wrapper to crash when accessing LUA_REGISTRYINDEX.
		// e.logger.Debug("[LuaEngine.Close] running full GC cycle...")
		// e.state.GC(lua.LUA_GCCOLLECT, 0)

		// // Stop GC so no finalizers run during lua_close() teardownBut ed
		// e.logger.Debug("[LuaEngine.Close] stopping GC...")
		// e.state.GC(lua.LUA_GCSTOP, 0)

		e.logger.Debugf("[LuaEngine.Close] goroutine dump before lua_close:\n%s", groutine.Dump())
		e.logger.Debug("[LuaEngine.Close] calling state.Close()...")
		e.state.Close()
		e.logger.Debug("[LuaEngine.Close] state closed")
		e.state = nil
	}
}

// SetPackagePaths configures allowed paths for secure module loading.
// This method adds the script's directory and validated library paths to the
// SecureModuleLoader's allowedPaths, enabling require() to find modules.
//
// Parameters:
//   - scriptPath: path to the main script file (its directory is added to allowed paths)
//   - libPaths: optional extra directories validated and added to allowed paths
//
// Returns error if any libPath is invalid (doesn't exist, not a directory, or blocked system path).
func (e *LuaEngine) SetPackagePaths(scriptPath string, libPaths []string) error {
	return e.secureLoader.AddPaths(scriptPath, libPaths)
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
