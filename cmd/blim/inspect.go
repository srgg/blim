package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/srg/blim"
	"github.com/srg/blim/inspector"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/devicefactory"
	"github.com/srg/blim/internal/lua"
)

const (
	defaultConnectTimeout            = 30 * time.Second
	defaultPreScanTimeout            = 3 * time.Second
	defaultDescriptorReadTimeout     = 0               // 0 means use inspector's default (2s); explicit 0 skips reads
	defaultCharacteristicReadTimeout = 5 * time.Second // Timeout for characteristic read operations
	luaOutputPollInterval            = 50 * time.Millisecond
)

// inspectCmd represents the inspect command
var inspectCmd = &cobra.Command{
	Use:   "inspect <device-address>",
	Short: "Inspect services, characteristics, and descriptors of a BLE device",
	Long: `Connects to a BLE device by address and discovers its services,
characteristics, and descriptors. Attempts to read characteristic values when possible.`,
	Args: cobra.ExactArgs(1),
	RunE: runInspect,
}

var (
	inspectConnectTimeout            time.Duration
	inspectDescriptorReadTimeout     time.Duration
	inspectPreScanTimeout            time.Duration
	inspectCharacteristicReadTimeout time.Duration
	inspectJSON                      bool
)

func init() {
	inspectCmd.Flags().DurationVar(&inspectConnectTimeout, "connect-timeout", defaultConnectTimeout, "Connection timeout")
	inspectCmd.Flags().DurationVar(&inspectDescriptorReadTimeout, "descriptor-timeout", defaultDescriptorReadTimeout, "Timeout for reading descriptor values (default: 2s if unset, 0 to skip descriptor reads)")
	inspectCmd.Flags().DurationVar(&inspectPreScanTimeout, "pre-scan-timeout", defaultPreScanTimeout, "Pre-scan timeout to capture advertisement data (0 to skip)")
	inspectCmd.Flags().DurationVar(&inspectCharacteristicReadTimeout, "characteristic-read-timeout", defaultCharacteristicReadTimeout, "Timeout for reading characteristic values")
	inspectCmd.Flags().BoolVar(&inspectJSON, "json", false, "Output as JSON")
}

// preScanForAdvertisement performs a brief scan to find the target device and capture its advertisement.
// The ctx should already have a timeout configured by the caller.
func preScanForAdvertisement(ctx context.Context, address string, logger *logrus.Logger) (device.Advertisement, error) {
	// Create cancellable child context so we can stop scanning immediately when the device is found
	// (parent ctx has the timeout, child allows early cancellation)
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use a low-level scanner to capture the advertisement
	scanner, err := devicefactory.DeviceFactory()
	if err != nil {
		return nil, err
	}

	var foundAdv device.Advertisement
	handler := func(adv device.Advertisement) {
		if adv.Addr() == address {
			foundAdv = adv
			cancel() // Stop scanning immediately when a device found
		}
	}

	err = scanner.Scan(scanCtx, false, handler)

	// If a device was found, return it (ignore context.Canceled from our cancel())
	if foundAdv != nil {
		return foundAdv, nil
	}

	// Device not found - return the scanner error
	// Scanner properly returns context errors (DeadlineExceeded, Canceled) or other errors (ErrBluetoothOff, etc.)
	return nil, err
}

func runInspect(cmd *cobra.Command, args []string) error {
	address := args[0]

	// Configure logger based on --log-level and --verbose flags
	logger, err := configureLogger(cmd, "verbose")
	if err != nil {
		return err
	}

	// All arguments validated - don't show usage on runtime errors
	cmd.SilenceUsage = true

	// Setup context with cancellation for graceful shutdown on SIGINT/SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Setup progress printer (disabled for JSON output)
	var progressCallback func(string)
	if !inspectJSON {
		progress := NewProgressPrinter(fmt.Sprintf("Inspecting device %s", address), "Connecting", "Processing results")
		progress.Start()
		defer progress.Stop()
		progressCallback = progress.Callback()
	} else {
		progressCallback = NoOpProgressCallback()
	}

	// Attempt pre-scan to capture advertisement data (manufacturer data, RSSI, etc.)
	var adv device.Advertisement
	if inspectPreScanTimeout > 0 {
		progressCallback("Pre-scanning")

		// Create timeout context for pre-scan
		scanCtx, scanCancel := context.WithTimeout(ctx, inspectPreScanTimeout)
		defer scanCancel()

		var scanErr error
		adv, scanErr = preScanForAdvertisement(scanCtx, address, logger)

		// Handle pre-scan result
		if adv != nil {
			// Device found successfully
			progressCallback("Pre-scanning complete")
		} else if errors.Is(scanErr, device.ErrTimeout) {
			// Timeout - device not found in time (this is expected/common)
			progressCallback("Pre-scanning timeout")
			logger.Warn("Pre-scan timeout, device not found - connecting without advertisement data")
		} else if errors.Is(scanErr, context.Canceled) {
			// Parent context canceled (user interrupted) - ABORT inspect
			return scanErr
		} else if scanErr != nil {
			// Actual error (Bluetooth off, etc.) - log it but continue
			progressCallback("Pre-scanning failed")
			logger.WithError(scanErr).Warn("Pre-scan failed, connecting without advertisement data")
		}
	}

	// Build inspect options
	opts := &inspector.InspectOptions{
		ConnectTimeout:            inspectConnectTimeout,
		DescriptorReadTimeout:     inspectDescriptorReadTimeout,
		CharacteristicReadTimeout: inspectCharacteristicReadTimeout,
	}

	// Use a Lua script for output generation
	processDevice := func(dev device.Device) (any, error) {
		// Update the device with advertisement data if we have it
		if adv != nil {
			dev.Update(adv)
		}
		return nil, executeInspectLuaScript(ctx, dev, logger, opts.CharacteristicReadTimeout)
	}

	_, err = inspector.InspectDevice(ctx, address, opts, logger, progressCallback, processDevice)
	return err
}

// executeInspectLuaScript runs the embedded inspect.lua script with the connected device
func executeInspectLuaScript(ctx context.Context, dev device.Device, logger *logrus.Logger, characteristicReadTimeout time.Duration) error {
	// Determine format based on --json flag
	format := "text"
	if inspectJSON {
		format = "json"
	}

	// Prepare script arguments
	args := map[string]string{
		"format": format,
	}

	// Execute the embedded script with output streaming
	// Note: Write timeout is 0 because the inspect command only does read characteristics
	// Note: No ScriptOptions needed for embedded scripts (no external module loading)
	return lua.ExecuteDeviceScriptWithOutput(
		ctx,
		dev,
		nil,
		logger,
		blecli.DefaultInspectLuaScript,
		args,
		os.Stdout,
		os.Stderr,
		luaOutputPollInterval,
		characteristicReadTimeout,
		0,   // write timeout not needed for inspect
		nil, // no script options for embedded scripts
	)
}
