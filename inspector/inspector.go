// Package inspector connects to a Bluetooth Low Energy device and enumerates
// its GATT services, characteristics, and descriptors.
//
// InspectDevice handles the connect/inspect/disconnect lifecycle and returns a
// caller-defined result built from the discovered attributes. The package is
// importable as a library and also backs the "blim inspect" CLI command.
package inspector

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
)

// ProgressCallback is called when the inspection phase changes
type ProgressCallback func(phase string)

// InspectOptions defines options for inspecting a BLE device profile
type InspectOptions struct {
	ConnectTimeout            time.Duration
	DescriptorReadTimeout     time.Duration // Timeout for reading descriptor values (0 = skip reads)
	CharacteristicReadTimeout time.Duration // Timeout for reading characteristic values
}

// InspectCallback processes a connected device and produces output of type R
type InspectCallback[R any] func(device.Device) (R, error)

// InspectDevice connects to a device, discovers its profile, and executes the callback with the connected device.
// The device lifecycle (connection and disconnection) is managed automatically.
// The callback receives the connected device and can return any result type R along with an error.
// Optional progressCallback can be provided for connection progress updates.
func InspectDevice[R any](ctx context.Context, address string, opts *InspectOptions, logger *logrus.Logger, progressCallback ProgressCallback, callback InspectCallback[R]) (R, error) {
	var zero R
	if opts == nil {
		opts = &InspectOptions{ConnectTimeout: 30 * time.Second}
	}
	if logger == nil {
		logger = logrus.New()
	}
	if progressCallback == nil {
		progressCallback = func(string) {} // No-op callback
	}

	// Report phase change: starting connection
	progressCallback("Connecting")

	// Create device and connect (reuses BLEConnection.Connect logic - no duplication!)
	dev := goble.NewBLEDeviceWithAddress(address, logger)
	connectOpts := &device.ConnectOptions{
		ConnectTimeout:        opts.ConnectTimeout,
		DescriptorReadTimeout: opts.DescriptorReadTimeout,
	}

	err := dev.Connect(ctx, connectOpts)

	if err != nil {
		progressCallback("Failed")
		return zero, err
	}

	// Ensure the device is disconnected after the callback completes
	defer func(dev device.Device) {
		err := dev.Disconnect()
		if err != nil {
			logger.WithError(err).Error("failed to disconnect device")
		}
	}(dev)

	// Report phase change: connected
	progressCallback("Connected")

	// Report phase change: processing results
	progressCallback("Processing results")

	// Execute callback with a connected device
	return callback(dev)
}
