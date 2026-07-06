// Package bridge relays a Bluetooth Low Energy device to a local PTY, running a
// Lua script that mediates traffic between the BLE characteristics and the
// pseudo-terminal.
//
// RunDeviceBridge is the generic library entry point (connect, set up the PTY,
// invoke a caller callback); RunCliBridge wraps it for the "blim bridge" CLI
// command, executing a script with stdout/stderr streaming. The package is
// importable as a library.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/devicefactory"
	"github.com/srgg/blim/internal/groutine"
	"github.com/srgg/blim/internal/lua"
	"github.com/srgg/blim/internal/ptyio"
)

const (
	// DefaultPtyStdoutBufferSize is the default size, in bytes, of the ring buffer used for PTY stdout input.
	DefaultPtyStdoutBufferSize = 1000

	// DefaultPtyStdinBufferSize is the default size, in bytes, of the ring buffer used for PTY stdin input.
	DefaultPtyStdinBufferSize = 1000
)

// Bridge represents a running BLE-PTY bridge with access to the device and PTY
type Bridge interface {
	GetLuaAPI() lua.LuaAPIInterface
	GetTTYName() string                 // TTY device name for display
	GetTTYSymlink() string              // Symlink path (empty if not created)
	GetPTY() io.ReadWriter              // PTY I/O as a standard Go interface (for Lua exposure)
	GetPTYIO() ptyio.PTY                // PTY I/O interface (never nil)
	SetPTYReadCallback(cb func([]byte)) // Set callback for PTY data arrival (nil to unregister)
}

// BridgeOptions contains all the configuration for running a bridge
type BridgeOptions struct {
	BleAddress               string                      // BLE device address
	BleConnectTimeout        time.Duration               // BLE Connection timeout
	BleDescriptorReadTimeout time.Duration               // Timeout for reading descriptor values (0 = skip reads)
	BleSubscribeOptions      []device.SubscribeOptions   // BLE subscribe options
	Logger                   *logrus.Logger              // Logger instance
	PtyStdinBufferSize       int                         // PTY stdin ring buffer size in bytes (0 = use default)
	PtyStdoutBufferSize      int                         // PTY stdout ring buffer size in bytes (0 = use default)
	TTYSymlinkPath           string                      // Optional tty symlink path for PTY slave (e.g., /tmp/ble-device)
	Factory                  devicefactory.BridgeFactory // Factory for creating bridge components (nil = use default)
}

// ProgressCallback is called when the bridge phase changes
type ProgressCallback func(phase string)

// BridgeCallback is executed with the running bridge (mirrors InspectCallback)
// The context is bridgeCtx which gets cancelled on BLE disconnect or parent cancellation.
type BridgeCallback[R any] func(ctx context.Context, b Bridge) (R, error)

// bridgeImpl implements the Bridge interface
type bridgeImpl struct {
	luaApi         lua.LuaAPIInterface
	ttySymlinkPath string    // TTY Symlink (empty if not created)
	pty            ptyio.PTY // PTY I/O interface for async monitoring
}

func (b *bridgeImpl) GetLuaAPI() lua.LuaAPIInterface {
	return b.luaApi
}

func (b *bridgeImpl) GetTTYName() string {
	if b.pty != nil {
		return b.pty.TTYName()
	}
	return ""
}

func (b *bridgeImpl) GetTTYSymlink() string {
	return b.ttySymlinkPath
}

func (b *bridgeImpl) GetPTY() io.ReadWriter {
	return b.pty
}

func (b *bridgeImpl) GetPTYIO() ptyio.PTY {
	return b.pty
}

func (b *bridgeImpl) SetPTYReadCallback(cb func([]byte)) {
	if b.pty != nil {
		b.pty.SetReadCallback(cb)
	}
}

// RunDeviceBridge connects to a BLE device, creates a PTY bridge, and executes the callback with the bridge.
// This function blocks until the context is canceled or an error occurs.
// It follows the same pattern as inspector.InspectDevice for consistency.
func RunDeviceBridge[R any](
	ctx context.Context,
	opts *BridgeOptions,
	progressCallback ProgressCallback,
	callback BridgeCallback[R],
) (R, error) {
	var zero R

	// Validate options
	if opts == nil {
		return zero, fmt.Errorf("failed to execute bridge: options are required")
	}
	if opts.BleAddress == "" {
		return zero, fmt.Errorf("failed to execute bridge: device address is required")
	}

	// Set defaults
	logger := opts.Logger
	if logger == nil {
		logger = logrus.New()
	}
	if progressCallback == nil {
		progressCallback = func(string) {} // No-op callback
	}
	if opts.BleConnectTimeout == 0 {
		opts.BleConnectTimeout = 30 * time.Second
	}

	// Create context for cancellation with cause support (enables disconnect detection)
	bridgeCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Setup cleanup on error
	var (
		luaApi         lua.LuaAPIInterface
		ttySymlinkPath string
		pty            ptyio.PTY
	)

	defer func() {
		// Clear bridge reference before cleanup (prevents Lua from using stale bridge)
		if luaApi != nil {
			luaApi.SetBridge(nil)
		}

		// Remove tty symlink before closing PTY (cleanup order matters)
		if ttySymlinkPath != "" {
			if err := os.Remove(ttySymlinkPath); err != nil {
				logger.WithError(err).WithField("ttySymlink", ttySymlinkPath).Warn("Failed to remove tty symlink")
			} else {
				logger.WithField("ttySymlink", ttySymlinkPath).Debug("Removed tty symlink")
			}
		}

		// Close PTY I/O strategy (stops background monitoring and closes master/slave)
		if pty != nil {
			if err := pty.Close(); err != nil {
				logger.WithError(err).Warnf("Failed to close PTY: %s", pty.TTYName())
			} else {
				logger.Infof("Closed PTY: %s", pty.TTYName())
			}
		}

		if luaApi != nil {
			// Disconnect is now handled by Close() which disconnects first,
			// then waits for in-flight callbacks before closing Lua state
			luaApi.Close()
		}
	}()

	// Use factory from options (nil = default)
	factory := opts.Factory
	if factory == nil {
		factory = devicefactory.DefaultBridgeFactory
	}

	// Report phase: Connecting
	progressCallback("Connecting")

	// Connect to device
	connectOpts := &device.ConnectOptions{
		Address:               opts.BleAddress,
		ConnectTimeout:        opts.BleConnectTimeout,
		DescriptorReadTimeout: opts.BleDescriptorReadTimeout,
		Services:              opts.BleSubscribeOptions,
	}

	luaApi, err := factory.CreateLuaAPI(bridgeCtx, connectOpts, logger)
	if err != nil {
		return zero, fmt.Errorf("failed to create Lua API for device %s: %w", opts.BleAddress, err)
	}

	// Monitor connection context and cancel bridgeCtx on disconnection.
	// This ensures callbacks receive context cancellation when BLE device disconnects.
	conn := luaApi.GetDevice().GetConnection()
	if conn != nil {
		connCtx := conn.ConnectionContext()
		if connCtx != nil {
			groutine.Go(ctx, "ble-conn-monitor", func(_ context.Context) {
				select {
				case <-connCtx.Done():
					logger.Debug("[BLEConMon] Ble connection context is done")
					cause := context.Cause(connCtx)
					if cause != nil && errors.Is(cause, device.ErrNotConnected) {
						logger.Info("[BLEConMon] Device disconnected, cancelling bridge context with ErrConnectionLost...")
						cancel(device.ErrConnectionLost)
					} else if cause != nil {
						logger.Infof("[BLEConMon] Device disconnected, cancelling bridge context with cause: %v...", cause)
						cancel(cause)
					} else {
						logger.Info("[BLEConMon] Device disconnected, cancelling bridge context without Canceled")
						cancel(context.Canceled)
					}
				case <-bridgeCtx.Done():
					// Bridge context already cancelled (e.g., user Ctrl+C)
					logger.Debug("[BLEConMon] bridge context is already canceled, exiting")
				}

				logger.Debug("[BLEConMon] Ble connection monitor finished")
			})
		}
	}

	// Report phase: Connected
	progressCallback("Connected")

	// Report phase: Setting up PTY
	progressCallback("Setting up PTY")

	// Create and configure PTY

	outputBufferSize := opts.PtyStdoutBufferSize
	if outputBufferSize == 0 {
		outputBufferSize = DefaultPtyStdoutBufferSize
	}
	inputBufferSize := opts.PtyStdinBufferSize
	if inputBufferSize == 0 {
		inputBufferSize = DefaultPtyStdinBufferSize
	}

	// Create PTY I/O strategy with background slave monitoring
	pty, err = factory.CreatePTY(inputBufferSize, outputBufferSize, logger)
	if err != nil {
		return zero, fmt.Errorf("failed to create PTY: %w", err)
	}

	logger.Infof("Created PTY: %s", pty.TTYName())

	// Create symlink to a PTY slave if requested
	if opts.TTYSymlinkPath != "" {
		if err := factory.CreateSymlink(pty.TTYName(), opts.TTYSymlinkPath); err != nil {
			return zero, fmt.Errorf("failed to create tty symlink %s -> %s: %w", opts.TTYSymlinkPath, pty.TTYName(), err)
		}
		ttySymlinkPath = opts.TTYSymlinkPath
		logger.Infof("Created PTY symlink: %s -> %s", ttySymlinkPath, pty.TTYName())
	}

	// Report phase: Running
	progressCallback("Running")

	// Create bridge implementation
	bridge := &bridgeImpl{
		luaApi:         luaApi,
		ttySymlinkPath: ttySymlinkPath,
		pty:            pty,
	}

	// Set bridge info on Lua API (enables pty_write/pty_read via strategy)
	luaApi.SetBridge(bridge)

	// Register shutdown hook to close PTY on context cancellation.
	// This unblocks any PTY write operations (e.g., buffer full) so the script can exit cleanly
	// when device disconnects. Unlike Disconnect() which causes SIGBUS if called during script
	// execution, PTY close is safe and just sends EOF to unblock I/O operations.
	luaApi.AddShutdownHook(func() {
		logger.Debug("Shutdown hook: closing PTY to unblock operations")
		if pty != nil {
			_ = pty.Close()
		}
	})

	// Execute callback with the bridge context and bridge
	return callback(bridgeCtx, bridge)
}

// CLIBridgeConfig configures the CLI bridge runner.
type CLIBridgeConfig struct {
	DeviceAddress  string
	ConnectTimeout time.Duration
	Symlink        string // Create a symlink to the PTY device (e.g., /tmp/ble-device)

	ScriptContent string
	ScriptArgs    map[string]string
	ScriptOpts    *lua.ScriptOptions
	Stdout        io.Writer // nil = discard
	Stderr        io.Writer // nil = discard
	ServiceUUID   string    // BLE service UUID to bridge

	CharacteristicReadTimeout  time.Duration // Timeout for characteristic read operations
	CharacteristicWriteTimeout time.Duration // Timeout for characteristic write operations
	DescriptorReadTimeout      time.Duration // Timeout for reading descriptor values (default: 2s if unset, 0 to skip descriptor reads)

	Logger  *logrus.Logger
	Factory devicefactory.BridgeFactory // Factory for creating bridge components (nil = use default)
}

// RunCliBridge runs a device bridge for the CLI: it connects to the device in
// opts, executes opts.ScriptContent against the bridge, and streams the script's
// output to opts.Stdout/opts.Stderr. It blocks until the context is canceled,
// the device disconnects, or the script finishes, reporting progress through
// progressCallback. It is a thin CLI-oriented wrapper over RunDeviceBridge.
func RunCliBridge(ctx context.Context, progressCallback ProgressCallback, opts CLIBridgeConfig) error {
	// Bridge callback - executes the Lua script with output streaming.
	// The ctx parameter is bridgeCtx from RunDeviceBridge, which gets cancelled on disconnect.
	bridgeCallback := func(ctx context.Context, b Bridge) (any, error) {
		// Create an output drainer to capture output from the Lua API.
		// The bridge keeps Lua state open after script execution completes,
		// using it to call Lua callbacks until the bridge is canceled.
		drainer := lua.NewOutputDrainer(ctx, b.GetLuaAPI().OutputChannel(), opts.Logger, opts.Stdout, opts.Stderr)

		defer func() {
			// Stop the drainer after the bridge completes
			drainer.Cancel()
			drainer.Wait()
		}()

		// Execute the Lua script
		err := lua.ExecuteDeviceScriptWithOutput(
			ctx,
			nil,
			b.GetLuaAPI(),
			opts.Logger,
			opts.ScriptContent,
			opts.ScriptArgs,
			nil,
			nil,
			opts.CharacteristicReadTimeout,
			opts.CharacteristicWriteTimeout,
			opts.ScriptOpts,
		)
		if err != nil {
			// Check if error was due to disconnection
			cause := context.Cause(ctx)
			if cause != nil && errors.Is(cause, device.ErrConnectionLost) {
				return nil, device.ErrConnectionLost
			}
			return nil, err
		}

		// Signal that bridge is fully ready (script loaded and executed successfully)
		if progressCallback != nil {
			progressCallback("Ready")
		}

		opts.Logger.Info("Bridge monitoring started, waiting for shutdown or disconnect...")

		// Wait for context cancellation (handles both disconnect AND Ctrl+C)
		// RunDeviceBridge cancels ctx with ErrConnectionLost on disconnect
		<-ctx.Done()
		cause := context.Cause(ctx)
		if cause != nil && errors.Is(cause, device.ErrConnectionLost) {
			return nil, device.ErrConnectionLost
		}
		opts.Logger.Info("Bridge shutting down...")
		return nil, nil
	}

	// Run the bridge with callback
	_, err := RunDeviceBridge(
		ctx,
		&BridgeOptions{
			BleAddress:               opts.DeviceAddress,
			BleConnectTimeout:        opts.ConnectTimeout,
			BleDescriptorReadTimeout: opts.DescriptorReadTimeout,
			BleSubscribeOptions: []device.SubscribeOptions{
				{
					Service: opts.ServiceUUID,
				},
			},
			Logger:         opts.Logger,
			TTYSymlinkPath: opts.Symlink,
			Factory:        opts.Factory,
		},
		progressCallback,
		bridgeCallback,
	)

	return err
}
