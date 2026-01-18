package lua

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
)

// ScriptOptions holds configuration for Lua script execution.
type ScriptOptions struct {
	// ScriptPath is the filesystem path to the main script file.
	// Its directory is automatically added to package.path for require().
	ScriptPath string

	// LibraryPaths are extra directories to search for Lua modules.
	// These are validated and added to SecureModuleLoader's allowed paths.
	LibraryPaths []string
}

// ExecuteDeviceScriptWithOutput executes a Lua script with the given device and arguments,
// writing all output to the provided writers.
// The script is executed synchronously, and all output is drained from the channel.
//
// Parameters:
//   - ctx: Context for cancellation
//   - dev: The BLE device to provide to the Lua script
//   - logger: Logger for the Lua engine
//   - script: The Lua script code to execute
//   - args: Map of arguments to pass to the script via the arg[] table
//   - stdout: Writer for standard output (if nil, output is discarded)
//   - stderr: Writer for error output (if nil, errors are discarded)
//   - characteristicReadTimeout: Timeout for characteristic read operations (0 = use default)
//   - characteristicWriteTimeout: Timeout for characteristic write operations (0 = use default)
//   - opts: Optional script options for module path configuration (can be nil)
//
// Returns an error if script execution fails.
func ExecuteDeviceScriptWithOutput(
	ctx context.Context,
	dev device.Device,
	luaAPI LuaAPIInterface,
	logger *logrus.Logger,
	script string,
	args map[string]string,
	stdout, stderr io.Writer,
	characteristicReadTimeout time.Duration,
	characteristicWriteTimeout time.Duration,
	opts *ScriptOptions,
) error {

	if luaAPI == nil {
		// Create a Lua API with the connected device
		luaAPI = NewBLEAPI2(dev, logger)
		defer luaAPI.Close()

		// Configure timeouts if provided (0 = use defaults from LuaAPI)
		if characteristicReadTimeout > 0 {
			luaAPI.SetCharacteristicReadTimeout(characteristicReadTimeout)
		}
		if characteristicWriteTimeout > 0 {
			luaAPI.SetCharacteristicWriteTimeout(characteristicWriteTimeout)
		}
	}

	// Configure secure module loading paths
	if opts != nil && (opts.ScriptPath != "" || len(opts.LibraryPaths) > 0) {
		if err := luaAPI.SetPackagePaths(opts.ScriptPath, opts.LibraryPaths); err != nil {
			return fmt.Errorf("invalid library path: %w", err)
		}
	}

	logger.WithField("script_size", len(script)).Debug("Starting Lua script execution")
	defer func() {
		logger.Debug("Lua script execution completed")
	}()

	// Build arg[] table initialization from provided arguments
	// Using strings.Builder for efficient string concatenation
	var argBuilder strings.Builder
	argBuilder.WriteString("arg = {}\n")
	for key, value := range args {
		// strings.Builder.Write never returns an error, safe to ignore
		_, _ = fmt.Fprintf(&argBuilder, "arg[%q] = %q\n", key, value)
	}

	// Combine arg initialization with script content
	scriptWithArgs := argBuilder.String() + "\n-- User script\n" + script

	// Only drain output if at least one writer is provided
	var drainer *OutputDrainer

	// If both stdout and stderr are nil, skip consumption and let the caller handle OutputChannel
	if stdout != nil || stderr != nil {
		drainer = NewOutputDrainer(ctx, luaAPI.OutputChannel(), logger, stdout, stderr)

		defer func() {
			// Stop the drainer and wait for it to fully exit.
			// The drainer has an internal 100ms timeout in drainWithTimeout()
			// to prevent indefinite blocking.
			drainer.Cancel()
			drainer.Wait()
		}()
	}

	// Execute the script (blocking)
	if err := luaAPI.ExecuteScript(ctx, scriptWithArgs); err != nil {
		return fmt.Errorf("failed to execute script: %w", err)
	}

	return nil
}
