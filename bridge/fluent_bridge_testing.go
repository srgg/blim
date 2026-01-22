//go:build test

package bridge

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/devicefactory"
	"github.com/srg/blim/internal/lua"
	"github.com/srg/blim/internal/ptyio"
)

// ChannelPTY is the channel name for PTY output (bridge-specific).
const ChannelPTY = "pty"

// BridgeTestableDevice wraps LuaTestableDevice with bridge-specific fluent methods.
type BridgeTestableDevice struct {
	*lua.LuaTestableDevice
	ctx              context.Context // Context for bridge operations (nil = use context.Background())
	fluentBridgeTest *FluentBridgeTest
}

// FluentBridgeTest returns a FluentBridgeTest for this device (cached).
func (d *BridgeTestableDevice) FluentBridgeTest() *FluentBridgeTest {
	if d.fluentBridgeTest == nil {
		d.fluentBridgeTest = NewFluentBridgeTest(d.LuaTestableDevice, d.ctx)
	}
	return d.fluentBridgeTest
}

// cleanup returns cleanup function for auto-registration with t.Cleanup().
func (d *BridgeTestableDevice) cleanup() func() {
	return func() {
		if d.fluentBridgeTest != nil {
			d.fluentBridgeTest.Close()
		}
	}
}

// BridgeRunContext tracks a single bridge run and its result.
type BridgeRunContext struct {
	runErr error // Error returned by RunDeviceBridge (nil = success)
}

// FluentBridgeTest extends GenericLuaFluentTest with bridge support.
// Inherits ALL Lua methods with proper *FluentBridgeTest return type.
//
// Three states for bridge configuration:
//   - WithBridge(mock) → uses provided mock bridge
//   - WithBridge(nil) → explicitly opts out of bridge (no auto-creation)
//   - Neither called → auto-creates real bridge on first ExecuteScript
type FluentBridgeTest struct {
	*lua.GenericLuaFluentTest[*FluentBridgeTest]

	ctx          context.Context // Context for bridge operations (nil = use context.Background())
	bridge       Bridge
	bridgeOptOut bool      // True if WithBridge(nil) was called
	customPTY    ptyio.PTY // Custom PTY for injection (nil = use real PTY)
	lifecycle    *BridgeLifecycle

	// Current bridge run context (nil = no pending run)
	currentBridgeRun *BridgeRunContext
}

// NewFluentBridgeTest creates a FluentBridgeTest from a LuaTestableDevice.
// ctx can be nil (defaults to context.Background()).
func NewFluentBridgeTest(device *lua.LuaTestableDevice, ctx context.Context) *FluentBridgeTest {
	fbt := &FluentBridgeTest{ctx: ctx}
	fbt.GenericLuaFluentTest = lua.NewGenericLuaFluentTest(device, fbt)
	return fbt
}

// getContext returns the context, defaulting to context.Background() if nil.
func (f *FluentBridgeTest) getContext() context.Context {
	if f.ctx != nil {
		return f.ctx
	}
	return context.Background()
}

// WithBridge injects a mock bridge or opts out (nil).
// Must be called before ExecuteScript.
func (f *FluentBridgeTest) WithBridge(bridge Bridge) *FluentBridgeTest {
	if f.lifecycle != nil {
		f.Require().Fail("cannot set bridge: already auto-created. Call WithBridge() before ExecuteScript()")
	}
	if bridge == nil {
		f.bridgeOptOut = true
		return f
	}
	f.bridge = bridge
	f.LuaAPI().SetBridge(bridge)
	return f
}

// WithPTY injects a custom PTY (nil = use real PTY from factory).
// Must be called before ExecuteScript.
func (f *FluentBridgeTest) WithPTY(pty ptyio.PTY) *FluentBridgeTest {
	if f.lifecycle != nil {
		f.Require().Fail("cannot set PTY: already auto-created. Call WithPTY() before ExecuteScript()")
	}
	f.customPTY = pty
	return f
}

// ExecuteScript overrides to auto-create bridge before execution.
// If called after MustBridgeRun, uses the existing drainer/collector instead of creating new ones.
func (f *FluentBridgeTest) ExecuteScript(script string) *FluentBridgeTest {
	f.ensureBridge()

	// If in bridge context (from MustBridgeRun), use existing collector
	if f.bridge != nil {
		f.GenericLuaFluentTest.ExecuteScriptWithCollector(f.getContext(), script, f.Collector())
		// Wait for drainer to flush output to collector (async ring channel processing)
		time.Sleep(1500 * time.Millisecond)
		return f
	}

	// No bridge context - use generic path (creates new collector)
	f.GenericLuaFluentTest.ExecuteScript(script)
	return f
}

// MustExecuteScript executes a Lua script and asserts no runtime error occurred.
func (f *FluentBridgeTest) MustExecuteScript(script string) *FluentBridgeTest {
	f.ExecuteScript(script)
	return f.AssertNoLuaScriptExecutionError()
}

// FailExecuteScript executes a Lua script and asserts it fails with expected error.
func (f *FluentBridgeTest) FailExecuteScript(expectedError, script string) *FluentBridgeTest {
	f.ExecuteScript(script)
	return f.ConsumeLuaError(expectedError)
}

// ensureBridge auto-creates bridge if not mocked/opted-out.
func (f *FluentBridgeTest) ensureBridge() {
	if f.bridgeOptOut || f.bridge != nil {
		return
	}

	f.lifecycle = &BridgeLifecycle{}
	f.lifecycle.StartWithCallback(
		f.getContext(),
		f.Device().Address(),
		f.Logger(),
		f.LuaAPI(),
		f.customPTY,
	)

	err := f.lifecycle.WaitReady()
	f.Require().NoError(err, "failed to create test bridge")

	f.bridge = f.lifecycle.Bridge()
	f.LuaAPI().SetBridge(f.bridge)
}

// Close cleans up bridge lifecycle then delegates to parent.
func (f *FluentBridgeTest) Close() {
	if f.lifecycle != nil {
		f.lifecycle.Close()
	}
	f.GenericLuaFluentTest.Close()
}

// Bridge returns the current bridge (mock or auto-created).
func (f *FluentBridgeTest) Bridge() Bridge {
	return f.bridge
}

// WritePTY writes data to PTY master (same as Lua's pty_write).
func (f *FluentBridgeTest) WritePTY(data []byte) *FluentBridgeTest {
	f.Require().NotNil(f.bridge, "WritePTY requires bridge to be initialized")
	_, err := f.bridge.GetPTYIO().Write(data)
	f.Require().NoError(err, "WritePTY failed")
	return f
}

// WritePTYSlave writes data to PTY slave (for Lua to read via pty_read).
func (f *FluentBridgeTest) WritePTYSlave(data []byte) *FluentBridgeTest {
	f.Require().NotNil(f.bridge, "WritePTYSlave requires bridge to be initialized")

	slavePath := f.bridge.GetTTYName()
	slave, err := os.OpenFile(slavePath, os.O_WRONLY, 0)
	f.Require().NoError(err, "WritePTYSlave: failed to open slave %s", slavePath)
	defer slave.Close()

	_, err = slave.Write(data)
	f.Require().NoError(err, "WritePTYSlave: failed to write to slave")
	return f
}

// ConsumePTYSlaveRx validates what Lua wrote via pty_write() and clears PTY channel.
// Uses polling to wait for data, handling timing variations in PTY processing.
func (f *FluentBridgeTest) ConsumePTYSlaveRx(expected []byte) *FluentBridgeTest {
	f.Require().NotNil(f.bridge, "ConsumePTYSlaveRead requires bridge to be initialized")

	// Poll for PTY data with timeout (20 iterations × 50ms = 1 second max)
	var actual string
	for i := 0; i < 20; i++ {
		actual = f.ConsumeChannel(ChannelPTY)
		if actual != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	f.Require().Equal(string(expected), actual, "%s mismatch", ChannelPTY)

	return f
}

// runBridge is the internal implementation for MustBridgeCallbackRun/FailBridgeCallbackRun.
func (f *FluentBridgeTest) runBridge(callback func(Bridge) error, opts *BridgeOptions) {
	if f.lifecycle != nil {
		f.Require().Fail("cannot run bridge: already started via ensureBridge()?")
	}
	if f.bridge != nil {
		f.Require().Fail("cannot run bridge: already set via WithBridge()")
	}

	// Apply defaults but fail if the caller tries to override BleAddress or Logger
	if opts == nil {
		opts = &BridgeOptions{}
	}
	if opts.BleAddress != "" && opts.BleAddress != f.Device().Address() {
		f.Require().Fail("BridgeOptions.BleAddress override not allowed: use FluentBridgeTest's device address")
	}
	if opts.Logger != nil && opts.Logger != f.Logger() {
		f.Require().Fail("BridgeOptions.Logger override not allowed: use FluentBridgeTest's logger")
	}
	opts.BleAddress = f.Device().Address()
	opts.Logger = f.Logger()

	// Inject test factory via options (avoids global state mutation)
	opts.Factory = &testBridgeFactory{
		wrapper: &luaAPIWrapper{
			LuaAPI:     f.LuaAPI(),
			fluentTest: f,
			logger:     f.Logger(),
		},
		customPTY:       f.customPTY,
		originalFactory: devicefactory.DefaultBridgeFactory,
	}

	// Run bridge with callback (uses context from ConnectWithContext if provided)
	_, err := RunDeviceBridge(f.getContext(), opts, nil,
		func(_ context.Context, b Bridge) (struct{}, error) {
			f.bridge = b
			f.LuaAPI().SetBridge(b)
			return struct{}{}, callback(b)
		},
	)

	f.currentBridgeRun = &BridgeRunContext{runErr: err}
}

// TerminalBridgeCallbackTest signals the end of a callback-based bridge test chain.
// This interface intentionally hides Lua script methods (ExecuteScript, MustExecuteScript, etc.)
// because callback-based tests control bridge behavior directly via the callback.
// For Lua script execution tests, use MustBridgeRun/FailBridgeRun (with default CLI callback).
type TerminalBridgeCallbackTest interface{}

// MustBridgeCallbackRun runs the bridge with a callback and asserts no error.
// Bridge must run successfully.
// Returns TerminalBridgeCallbackTest to signal chain termination (no Lua script methods available).
func (f *FluentBridgeTest) MustBridgeCallbackRun(callback func(Bridge) error, opts *BridgeOptions) TerminalBridgeCallbackTest {
	f.runBridge(callback, opts)
	f.Require().NoError(f.currentBridgeRun.runErr, "bridge must run successfully")
	return f
}

// FailBridgeCallbackRun runs the bridge with a callback and asserts the expected error.
// Bridge run must fail with the specified error.
// Returns TerminalBridgeCallbackTest to signal chain termination (no Lua script methods available).
func (f *FluentBridgeTest) FailBridgeCallbackRun(expectedError string, callback func(Bridge) error, opts *BridgeOptions) TerminalBridgeCallbackTest {
	f.runBridge(callback, opts)
	f.Require().Error(f.currentBridgeRun.runErr, "bridge run must fail")
	f.Require().Contains(f.currentBridgeRun.runErr.Error(), expectedError)
	f.currentBridgeRun = nil
	return f
}

// BridgeRunOptions configures MustBridgeRun/FailBridgeRun behavior.
type BridgeRunOptions struct {
	TTYSymlinkPath string            // Optional symlink to PTY device
	ScriptArgs     map[string]string // Script arguments (become Lua arg[] table)
	ScriptOpts     *lua.ScriptOptions
}

// runCliBridge runs the bridge using the production RunCliBridge code path.
func (f *FluentBridgeTest) runCliBridge(script string, opts *BridgeRunOptions) error {
	if f.lifecycle != nil {
		f.Require().Fail("cannot run bridge: already started via ensureBridge()?")
		return nil
	}
	if f.bridge != nil {
		f.Require().Fail("cannot run bridge: already set via WithBridge()")
		return nil
	}

	// Build CLI lifecycle options
	cliOpts := &CLILifecycleOptions{
		Script:        script,
		DeviceAddress: f.Device().Address(),
		Logger:        f.Logger(),
		LuaAPI:        f.LuaAPI(),
		CustomPTY:     f.customPTY,
		FluentTest:    f,
	}
	if opts != nil {
		cliOpts.ScriptArgs = opts.ScriptArgs
		cliOpts.ScriptOpts = opts.ScriptOpts
		cliOpts.SymlinkPath = opts.TTYSymlinkPath
	}

	// Use BridgeLifecycle to manage goroutine and resources
	f.lifecycle = &BridgeLifecycle{}
	if err := f.lifecycle.StartCLI(f.getContext(), cliOpts); err != nil {
		return err
	}

	// Wait for bridge to be fully ready (script loaded and executed successfully)
	if err := f.lifecycle.WaitReady(); err != nil {
		return err
	}

	return nil
}

// MustBridgeRun runs the bridge using RunCliBridge (production CLI path) and asserts no error.
// The bridge runs until the context is canceled.
// Returns *FluentBridgeTest to allow chaining Lua assertion methods.
func (f *FluentBridgeTest) MustBridgeRun(script string, opts *BridgeRunOptions) *FluentBridgeTest {
	err := f.runCliBridge(script, opts)
	f.Require().NoError(err, "bridge must run successfully")
	return f
}

// FailBridgeRun runs the bridge using RunCliBridge and asserts expected error.
// Returns *FluentBridgeTest to allow chaining Lua assertion methods.
func (f *FluentBridgeTest) FailBridgeRun(expectedError, script string, opts *BridgeRunOptions) *FluentBridgeTest {
	err := f.runCliBridge(script, opts)
	f.Require().Error(err, "bridge run must fail")

	f.ConsumeLuaError(expectedError)
	f.Require().Contains(err.Error(), expectedError)
	return f
}

// BridgeLifecycle manages bridge lifecycle in a background goroutine.
// Supports two modes:
//   - Callback mode (StartWithCallback): RunDeviceBridge with blocking callback
//   - CLI mode (StartCLI): RunCliBridge that blocks on context cancellation
type BridgeLifecycle struct {
	bridge    Bridge
	err       error
	done      chan struct{}
	doneOnce  sync.Once // Ensures done channel is closed only once
	ready     chan struct{}
	readyOnce sync.Once
	wg        sync.WaitGroup
	cancel    context.CancelFunc // Cancels the bridge context on Close()

	// Resources managed by StartCLI (cleaned up automatically)
	pty       ptyio.PTY
	collector *lua.LuaOutputCollector
}

// StartWithCallback launches bridge using RunDeviceBridge with a blocking callback.
// The callback blocks until Close() is called.
func (lc *BridgeLifecycle) StartWithCallback(
	ctx context.Context,
	bleAddress string,
	logger *logrus.Logger,
	luaAPI *lua.LuaAPI,
	customPTY ptyio.PTY,
) {
	lc.done = make(chan struct{})
	lc.ready = make(chan struct{})

	// Create test factory (injected via BridgeOptions.Factory, not global)
	// Note: fluentTest is nil here - BridgeLifecycle captures bridge directly in callback
	factory := &testBridgeFactory{
		wrapper: &luaAPIWrapper{
			LuaAPI:     luaAPI,
			fluentTest: nil,
			logger:     logger,
		},
		customPTY:       customPTY,
		originalFactory: devicefactory.DefaultBridgeFactory,
	}

	lc.wg.Add(1)
	go func() {
		defer lc.wg.Done()
		defer lc.readyOnce.Do(func() { close(lc.ready) })
		defer func() {
			if r := recover(); r != nil {
				lc.err = fmt.Errorf("panic in StartWithCallback: %v", r)
			}
		}()

		opts := &BridgeOptions{
			BleAddress: bleAddress,
			Logger:     logger,
			Factory:    factory,
		}

		_, lc.err = RunDeviceBridge(ctx, opts, nil,
			func(_ context.Context, b Bridge) (struct{}, error) {
				lc.bridge = b
				lc.readyOnce.Do(func() { close(lc.ready) })
				<-lc.done
				return struct{}{}, nil
			},
		)
	}()
}

// CLILifecycleOptions contains parameters for StartCLI.
type CLILifecycleOptions struct {
	Script        string
	ScriptArgs    map[string]string
	ScriptOpts    *lua.ScriptOptions
	DeviceAddress string
	Logger        *logrus.Logger
	LuaAPI        *lua.LuaAPI
	CustomPTY     ptyio.PTY // nil = create new PTY
	SymlinkPath   string
	FluentTest    *FluentBridgeTest // For bridge capture and error reporting
}

// StartCLI launches bridge using RunCliBridge (production CLI code path).
// Handles PTY creation, output collector setup, and cleanup automatically.
// Bridge capture happens via luaAPIWrapper.SetBridge interception.
func (lc *BridgeLifecycle) StartCLI(ctx context.Context, opts *CLILifecycleOptions) error {
	lc.done = make(chan struct{})
	lc.ready = make(chan struct{})

	// Create cancellable context for bridge shutdown
	ctx, lc.cancel = context.WithCancel(ctx)

	// Create PTY if not provided
	lc.pty = opts.CustomPTY
	if lc.pty == nil {
		var err error
		lc.pty, err = devicefactory.DefaultBridgeFactory.CreatePTY(0, 0, opts.Logger)
		if err != nil {
			return err
		}
	}

	// Create test factory
	testFactory := &testBridgeFactory{
		wrapper: &luaAPIWrapper{
			LuaAPI:     opts.LuaAPI,
			fluentTest: opts.FluentTest,
			logger:     opts.Logger,
		},
		customPTY:       lc.pty,
		originalFactory: devicefactory.DefaultBridgeFactory,
	}

	// Create output collector
	var err error
	lc.collector, err = lua.NewLuaOutputCollector(opts.LuaAPI.OutputChannel(), 100, nil)
	if err != nil {
		return err
	}

	// Get writers that route to the collector
	stdoutWriter := lc.collector.StdoutWriter()
	stderrWriter := lc.collector.StderrWriter()

	// Start PTY slave reader to capture pty_write() output
	ptySlaveReader, err := lc.collector.FileReader(lc.pty.TTYName(), "pty")
	if err != nil {
		return err
	}

	// Set up script execution context for ConsumeStdout/ConsumeLuaError
	opts.FluentTest.GenericLuaFluentTest.SetScriptExecutionContext(opts.Script, lc.collector)

	// Build CLIBridgeConfig
	cfg := CLIBridgeConfig{
		DeviceAddress: opts.DeviceAddress,
		ScriptContent: opts.Script,
		Logger:        opts.Logger,
		Factory:       testFactory,
		Stdout:        stdoutWriter,
		Stderr:        stderrWriter,
		Symlink:       opts.SymlinkPath,
		ScriptArgs:    opts.ScriptArgs,
		ScriptOpts:    opts.ScriptOpts,
	}

	// Capture variables for cleanup closure
	customPTY := opts.CustomPTY
	fluentTest := opts.FluentTest

	lc.wg.Add(1)
	go func() {
		defer lc.wg.Done()
		defer lc.readyOnce.Do(func() { close(lc.ready) })
		defer func() {
			if r := recover(); r != nil {
				lc.err = fmt.Errorf("panic in StartCLI: %v", r)
			}
		}()

		// Cleanup when bridge completes
		defer func() {
			// Store error for assertions before cleanup
			fluentTest.GenericLuaFluentTest.SetScriptExecutionError(lc.err)

			_ = ptySlaveReader.Close()
			_ = stdoutWriter.Close()
			_ = stderrWriter.Close()
			_ = lc.collector.Stop()
			if customPTY == nil {
				_ = lc.pty.Close()
			}
		}()

		lc.err = RunCliBridge(ctx, func(phase string) {
			if phase == "Ready" {
				opts.Logger.Debugf("CLI bridge ready: %s", opts.Script)
				lc.readyOnce.Do(func() { close(lc.ready) })
			}
		}, cfg)
	}()

	return nil
}

// WaitReady blocks until the bridge is ready or an error occurs.
func (lc *BridgeLifecycle) WaitReady() error {
	<-lc.ready
	return lc.err
}

// Bridge returns the current bridge (nil if not yet ready).
func (lc *BridgeLifecycle) Bridge() Bridge {
	return lc.bridge
}

// SetBridge sets the bridge (used by CLI mode where bridge is captured externally).
func (lc *BridgeLifecycle) SetBridge(b Bridge) {
	lc.bridge = b
	lc.readyOnce.Do(func() { close(lc.ready) })
}

// Error returns the error from the bridge run (nil if still running or success).
func (lc *BridgeLifecycle) Error() error {
	return lc.err
}

// Close signals the bridge to shut down and waits for cleanup.
func (lc *BridgeLifecycle) Close() {
	if lc.done == nil {
		return
	}
	// Cancel context first to unblock RunCliBridge's select on ctx.Done()
	if lc.cancel != nil {
		lc.cancel()
	}
	lc.doneOnce.Do(func() { close(lc.done) })
	lc.wg.Wait()
}

// testBridgeFactory implements devicefactory.BridgeFactory for testing.
// Returns pre-created LuaAPI wrapper and optionally a custom PTY.
type testBridgeFactory struct {
	wrapper         *luaAPIWrapper
	customPTY       ptyio.PTY
	originalFactory devicefactory.BridgeFactory
}

// CreateLuaAPI returns the pre-created wrapper.
func (f *testBridgeFactory) CreateLuaAPI(ctx context.Context, opts *device.ConnectOptions, logger *logrus.Logger) (lua.LuaAPIInterface, error) {
	return f.wrapper, nil
}

// luaAPIWrapper wraps *lua.LuaAPI to intercept SetBridge for bridge capture in tests.
type luaAPIWrapper struct {
	*lua.LuaAPI
	fluentTest *FluentBridgeTest
	logger     *logrus.Logger
}

// SetBridge intercepts SetBridge calls to capture the bridge instance.
func (w *luaAPIWrapper) SetBridge(b lua.BridgeInfo) {
	w.LuaAPI.SetBridge(b)
	if w.fluentTest == nil {
		return
	}
	prevBridge := w.fluentTest.bridge
	if b == nil {
		w.fluentTest.bridge = nil
		if prevBridge != nil && w.logger != nil {
			w.logger.Debugf("luaAPIWrapper: bridge %p released", prevBridge)
		}
		return
	}
	if bridge, ok := b.(Bridge); ok {
		w.fluentTest.bridge = bridge
		if w.logger != nil {
			w.logger.Debugf("luaAPIWrapper: bridge %p captured", bridge)
		}
	}
}

// Close is a no-op because the underlying LuaAPI is owned by the test, not the bridge.
// This prevents RunDeviceBridge's defer cleanup from closing the test's LuaAPI.
func (w *luaAPIWrapper) Close() {
	// No-op: test owns the LuaAPI lifecycle
}

// CreatePTY returns custom PTY if provided, otherwise delegates to original factory.
func (f *testBridgeFactory) CreatePTY(inBuf, outBuf int, logger *logrus.Logger) (ptyio.PTY, error) {
	if f.customPTY != nil {
		return f.customPTY, nil
	}
	return f.originalFactory.CreatePTY(inBuf, outBuf, logger)
}

// CreateSymlink delegates to the original factory to create real symlinks.
func (f *testBridgeFactory) CreateSymlink(target, linkPath string) error {
	return f.originalFactory.CreateSymlink(target, linkPath)
}
