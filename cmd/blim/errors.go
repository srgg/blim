package main

import (
	"context"
	"errors"

	"github.com/srg/blim/internal/device"
)

// FormatUserError converts errors to user-friendly messages without technical details.
// Handles common error types (connection loss, cancellation, timeouts) with clear messages.
// For other errors, returns the error message as-is without wrapping chains.
func FormatUserError(err error) string {
	if err == nil {
		return ""
	}

	// Handle specific known errors with clean messages
	if errors.Is(err, device.ErrConnectionLost) {
		return "connection lost"
	}

	if errors.Is(err, context.Canceled) {
		return "operation cancelled"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "operation timed out"
	}

	// Default: return the error message as-is
	return err.Error()
}
