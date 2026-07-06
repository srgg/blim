package goble

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/srgg/blim/internal/device"
)

// NormalizeError maps known go-ble error strings to structured ConnectionError types.
// It ensures consistent handling even if the upstream library changes messages slightly.
// Returns wrapped errors to preserve original context.
func NormalizeError(err error) error {
	if err == nil {
		return nil
	}

	// Check context errors first (these are common across all operations)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %v", device.ErrTimeout, err)
	case errors.Is(err, context.Canceled):
		return err // Don't wrap - cancellation is explicit user action
	}

	// Check platform-specific error messages
	msg := err.Error()
	switch {
	case msg == "central manager has invalid state: have=4 want=5: is Bluetooth turned on?":
		return fmt.Errorf("%w: %v", device.ErrBluetoothOff, err)
	case containsIgnoreCase(msg, "bluetooth is turned off"):
		return fmt.Errorf("%w: %v", device.ErrBluetoothOff, err)
	case containsIgnoreCase(msg, "device not connected"):
		return fmt.Errorf("%w: %v", device.ErrNotConnected, err)
	case containsIgnoreCase(msg, "disconnected"):
		return fmt.Errorf("%w: %v", device.ErrNotConnected, err)
	case containsIgnoreCase(msg, "device already connected"):
		return fmt.Errorf("%w: %v", device.ErrAlreadyConnected, err)
	case containsIgnoreCase(msg, "connection is not initialized"):
		return fmt.Errorf("%w: %v", device.ErrNotInitialized, err)
	default:
		return err
	}
}

// containsIgnoreCase checks the substring case-insensitively
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
