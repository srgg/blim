package goble

import (
	"context"
	"fmt"
	"time"

	"github.com/go-ble/ble"
	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/bledb"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/groutine"
)

const (
	// DefaultDescriptorReadTimeout is the default timeout for descriptor read operations.
	// Used when ConnectOptions.DescriptorReadTimeout is not explicitly set (via CLI flag).
	DefaultDescriptorReadTimeout = 2 * time.Second
)

// BLEDescriptor implements the Descriptor interface for BLE GATT descriptors.
// It stores both raw descriptor values and parsed representations for well-known descriptor types.
type BLEDescriptor struct {
	uuid        string
	handle      uint16
	index       uint8
	knownName   string
	value       []byte
	parsedValue interface{}
	BLEDesc     *ble.Descriptor // Reference to underlying BLE descriptor for potential re-reads
	connection  *BLEConnection  // Reference to parent connection for on-demand reads
}

// newDescriptor creates a BLEDescriptor and attempts to read its value with a timeout.
// If timeout is 0, descriptor reads are skipped entirely (fast path - no blocking).
// If timeout > 0, attempts to read with that timeout (best-effort, won't fail on error/timeout).
// For well-known descriptor UUIDs (0x2900-0x2906), values are automatically parsed.
func newDescriptor(d *ble.Descriptor, conn *BLEConnection, client ble.Client, timeout time.Duration, index uint8, logger *logrus.Logger) *BLEDescriptor {
	descRawUUID := d.UUID.String()
	descUUID := device.NormalizeUUID(descRawUUID)

	bleDesc := &BLEDescriptor{
		uuid:       descUUID,
		handle:     d.Handle,
		index:      index,
		knownName:  bledb.LookupDescriptor(descRawUUID),
		BLEDesc:    d,
		connection: conn,
	}

	// Fast path: skip descriptor reads if timeout is 0 or no client available
	if timeout == 0 || client == nil {
		return bleDesc
	}

	// Slow path: read descriptor value with timeout
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)

	groutine.Go(context.Background(), fmt.Sprintf("ble-descriptor-read-%s", descUUID), func(ctx context.Context) {
		// First check if descriptor already has a value from discovery
		if len(d.Value) > 0 {
			resultCh <- readResult{data: d.Value, err: nil}
			return
		}

		// Perform explicit read using the BLE client.
		// On Darwin/macOS, this works via CoreBluetooth's object-based API
		// (doesn't require handles like Linux/Windows implementations).
		data, err := client.ReadDescriptor(d)
		resultCh <- readResult{data: data, err: NormalizeError(err)}
	})

	select {
	case result := <-resultCh:
		if result.err == nil {
			bleDesc.value = result.data
		} else {
			// Read error - set parsedValue to DescriptorError
			bleDesc.parsedValue = &device.DescriptorError{
				Reason: "read_error",
				Err:    result.err,
			}
			if logger != nil {
				logger.WithFields(logrus.Fields{
					"descriptor_uuid": descUUID,
					"error":           result.err,
				}).Debug("Failed to read descriptor value")
			}
		}
	case <-time.After(timeout):
		// Timeout - set parsedValue to DescriptorError
		bleDesc.parsedValue = &device.DescriptorError{
			Reason: "read_timeout",
			Err:    nil,
		}
		if logger != nil {
			logger.WithFields(logrus.Fields{
				"descriptor_uuid": descUUID,
				"timeout":         timeout,
			}).Debug("Timeout reading descriptor value")
		}
	}

	return bleDesc
}

// UUID returns the normalized descriptor UUID (lowercase, without dashes for 16-bit UUIDs).
func (d *BLEDescriptor) UUID() string {
	return d.uuid
}

// KnownName returns the human-readable name for well-known descriptor UUIDs.
// Returns empty string for unknown descriptors.
func (d *BLEDescriptor) KnownName() string {
	return d.knownName
}

// Value returns the raw descriptor value bytes.
// Returns nil if the value was not successfully read or was skipped.
func (d *BLEDescriptor) Value() []byte {
	return d.value
}

func (d *BLEDescriptor) Handle() uint16 {
	return d.handle
}

// Index returns the descriptor index within the characteristic.
func (d *BLEDescriptor) Index() uint8 {
	return d.index
}

// ParsedValue returns the parsed descriptor value for well-known descriptor types.
// Returns *DescriptorError if read/parse failed, nil if descriptor read was skipped.
//
// Type assertions can be used to access specific descriptor types:
//   - *ExtendedProperties for 0x2900
//   - string for 0x2901 (User Description)
//   - *ClientConfig for 0x2902
//   - *ServerConfig for 0x2903
//   - *PresentationFormat for 0x2904
//   - *ValidRange for 0x2906
//   - []byte for unknown descriptor types
//   - *DescriptorError if read/parse failed
func (d *BLEDescriptor) ParsedValue() interface{} {
	return d.parsedValue
}

// Read performs an on-demand read of the descriptor value from the device.
// Updates the cached value and re-parses for well-known descriptor types.
// This implements the device.DescriptorReader interface.
func (d *BLEDescriptor) Read(timeout time.Duration) ([]byte, error) {
	if d.connection == nil {
		return nil, fmt.Errorf("no connection available for reading descriptor %s", d.uuid)
	}

	if d.BLEDesc == nil {
		return nil, fmt.Errorf("descriptor %s not initialized", d.uuid)
	}

	// Lock connection mutex to safely access client
	d.connection.connMutex.RLock()
	if d.connection.client == nil {
		d.connection.connMutex.RUnlock()
		return nil, fmt.Errorf("read descriptor %s: %w", d.uuid, device.ErrNotConnected)
	}
	client := d.connection.client
	d.connection.connMutex.RUnlock()

	// Perform read with timeout
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)

	groutine.Go(context.Background(), fmt.Sprintf("ble-descriptor-read-%s", d.uuid), func(ctx context.Context) {
		data, err := client.ReadDescriptor(d.BLEDesc)
		resultCh <- readResult{data: data, err: NormalizeError(err)}
	})

	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, fmt.Errorf("failed to read descriptor %s: %w", d.uuid, result.err)
		}
		// Update cached value
		d.value = result.data
		// Re-parse for well-known descriptor types
		if parsed, err := device.ParseDescriptorValue(d.uuid, result.data, nil); err == nil {
			d.parsedValue = parsed
		} else {
			d.parsedValue = &device.DescriptorError{
				Reason: "parse_error",
				Err:    err,
			}
		}
		return result.data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("read descriptor %s after %v: %w", d.uuid, timeout, device.ErrTimeout)
	}
}
