package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/srgg/blim"
	"github.com/srgg/blim/bridge"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/lua"
	"golang.org/x/term"
)

// bridgeCmd represents the bridge command
var bridgeCmd = &cobra.Command{
	Use:   "bridge <device-address>",
	Short: "Create a PTY bridge to a BLE device",
	Long: fmt.Sprintf(`Creates a bidirectional PTY (pseudoterminal) bridge to a BLE device,
allowing applications that expect a serial port to communicate with BLE devices.

The bridge creates a virtual serial device (e.g., /dev/ttys001) that applications
can connect to. Data written to the PTY is sent to the BLE device via the Nordic
UART Service, and data received from the device is written to the PTY.

This is useful for:
- Connecting terminal emulators to BLE devices
- Using existing serial applications with BLE devices
- Testing and debugging BLE serial communication
- Integrating BLE devices with legacy serial software

Example:
  blim bridge %s
  blim bridge --service=custom-uuid %s

%s`, exampleDeviceAddress, exampleDeviceAddress, deviceAddressNote),
	Args: cobra.ExactArgs(1),
	RunE: runBridge,
}

var (
	bridgeServiceUUID                string
	bridgeConnectTimeout             time.Duration
	bridgeDescriptorReadTimeout      time.Duration
	bridgeCharacteristicReadTimeout  time.Duration
	bridgeCharacteristicWriteTimeout time.Duration
	bridgeLuaScript                  string
	bridgeLuaLibs                    []string
	bridgeSymlink                    string
)

func init() {
	bridgeCmd.Flags().StringVar(&bridgeServiceUUID, "service", "6E400001-B5A3-F393-E0A9-E50E24DCCA9E", "BLE service UUID to bridge with")
	bridgeCmd.Flags().DurationVar(&bridgeConnectTimeout, "connect-timeout", 30*time.Second, "Connection timeout")
	bridgeCmd.Flags().DurationVar(&bridgeDescriptorReadTimeout, "descriptor-timeout", 0, "Timeout for reading descriptor values (default: 2s if unset, 0 to skip descriptor reads)")
	bridgeCmd.Flags().DurationVar(&bridgeCharacteristicReadTimeout, "characteristic-read-timeout", 0, "Timeout for characteristic read operations (0 = use default: 5s)")
	bridgeCmd.Flags().DurationVar(&bridgeCharacteristicWriteTimeout, "characteristic-write-timeout", 0, "Timeout for characteristic write operations (0 = use default: 5s)")
	bridgeCmd.Flags().StringVar(&bridgeLuaScript, "script", "", "Lua script file with ble_to_tty() and tty_to_ble() functions")
	bridgeCmd.Flags().StringSliceVar(&bridgeLuaLibs, "lua-lib", nil, "Additional directories to search for Lua modules (validated, can be specified multiple times)")
	bridgeCmd.Flags().StringVar(&bridgeSymlink, "symlink", "", "Create a symlink to the PTY device (e.g., /tmp/ble-device)")
}

// runBridge is the cobra entry point. It owns the process-level wiring — the cancellation context
// and the SIGINT/SIGTERM handler — and delegates the actual work to runBridgeCtx. Keeping the ctx
// creation here (and injecting it into runBridgeCtx) lets tests drive cancellation directly, without
// sending a process-global signal.
func runBridge(cmd *cobra.Command, args []string) error {
	// Configure logger based on --log-level and --verbose flags
	logger, err := configureLogger(cmd, "verbose")
	if err != nil {
		return err
	}

	// All arguments validated - don't show usage on runtime errors
	cmd.SilenceUsage = true

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupts gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	exitProgress := NewProgressPrinter("Shutting down", "Cleaning up", "Done")
	defer exitProgress.Stop()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("Panic in signal handler: %v", r)
				cancel() // Ensure shutdown proceeds even after panic
			}
		}()
		<-sigChan
		exitProgress.Start()
		exitProgress.Callback()("Closing connections")
		logger.Info("Received interrupt signal, shutting down...")
		exitProgress.Callback()("Stopping script")
		cancel()
	}()

	return runBridgeCtx(ctx, logger, args[0])
}

// runBridgeCtx runs the bridge under an injectable context. It restores the terminal on exit
// regardless of how the run ended: a Lua script may switch the terminal to raw mode via
// blim.term.enable_raw, and cancellation can abort the script before blim.term.disable_raw runs
// (issue #4), so the deferred guardTerminal puts the user's terminal back.
func runBridgeCtx(ctx context.Context, logger *logrus.Logger, deviceAddress string) error {
	// Validate and normalize service UUID
	serviceUUIDs, err := device.ValidateUUID(bridgeServiceUUID)
	if err != nil {
		return fmt.Errorf("invalid service UUID: %w", err)
	}
	serviceUUID := serviceUUIDs[0]

	// Restore the terminal on exit even if a script left it in raw mode (issue #4).
	defer guardTerminal(int(os.Stdin.Fd()), logger)()

	// Load script content before creating the callback
	var scriptContent string
	if bridgeLuaScript != "" {
		// Read the custom script file (supports project-root-relative paths starting with "/")
		logger.Infof("Loading custom Lua script from file %s", bridgeLuaScript)
		content, err := lua.LoadScriptFromPath(bridgeLuaScript)
		if err != nil {
			return fmt.Errorf("failed to read script file: %w", err)
		}
		scriptContent = content
	} else {
		// Use default bridge script
		logger.Info("Using default bridge script")
		scriptContent = blecli.DefaultBridgeLuaScript
	}

	var scriptArgs map[string]string

	// Configure secure Lua module loading paths:
	// - Script's directory is automatically included (enables require() for co-located modules)
	// - Additional paths from --lua-lib flag are validated and added to allowed paths
	var scriptOpts *lua.ScriptOptions
	if bridgeLuaScript != "" || len(bridgeLuaLibs) > 0 {
		scriptOpts = &lua.ScriptOptions{
			ScriptPath:   bridgeLuaScript,
			LibraryPaths: bridgeLuaLibs,
		}
	}

	// Setup progress printer
	progress := NewProgressPrinter(fmt.Sprintf("Starting bridge for %s", deviceAddress), "Connecting", "Running")
	progress.Start()
	defer progress.Stop()

	return bridge.RunCliBridge(ctx, progress.Callback(), bridge.CLIBridgeConfig{
		DeviceAddress:  deviceAddress,
		ConnectTimeout: bridgeConnectTimeout,
		Symlink:        bridgeSymlink,

		ScriptContent: scriptContent,
		ScriptArgs:    scriptArgs,
		ScriptOpts:    scriptOpts,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Logger:        logger,
		ServiceUUID:   serviceUUID,

		CharacteristicReadTimeout:  bridgeCharacteristicReadTimeout,
		CharacteristicWriteTimeout: bridgeCharacteristicWriteTimeout,
		DescriptorReadTimeout:      bridgeDescriptorReadTimeout,
	})
}

// guardTerminal snapshots the terminal connected to fd and returns a function that restores it on
// exit. A Lua script may switch the terminal to raw mode via blim.term.enable_raw; if cancellation
// aborts the script before blim.term.disable_raw runs (issue #4), the deferred restore puts the
// user's terminal back into its original mode — the way ssh does. No-op when fd is not a terminal
// (e.g. piped stdin). The snapshot is taken now (before the script can change the mode); the restore
// runs on the returned func — safe to defer, and idempotent if the script already restored the mode.
func guardTerminal(fd int, logger *logrus.Logger) func() {
	if !term.IsTerminal(fd) {
		return func() {}
	}
	state, err := term.GetState(fd)
	if err != nil {
		logger.WithError(err).Debug("could not snapshot terminal state; will not restore on exit")
		return func() {}
	}
	return func() {
		if err := term.Restore(fd, state); err != nil {
			logger.WithError(err).Warn("failed to restore terminal state on exit")
		}
	}
}
