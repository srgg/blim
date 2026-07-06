package goble

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ble/ble"
	"github.com/srgg/blim/internal/bledb"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/groutine"
)

// ----------------------------
// BLECharacteristic
// ----------------------------

type BLECharacteristic struct {
	uuid        string
	knownName   string
	properties  device.Properties
	descriptors []device.Descriptor
	value       []byte
	BLEChar     *ble.Characteristic
	connection  *BLEConnection // reference to parent connection for reading

	updates chan *BLEValue
	closed  atomic.Bool
	mu      sync.RWMutex
	subs    []func(*BLEValue)

	// Fan-out: dynamic registry of subscriber channels for broadcast delivery
	// TODO: switch to Lock Free
	subChannels   []chan *BLEValue
	subChannelsMu sync.Mutex
	fanOutCtx     context.Context
	fanOutCancel  context.CancelFunc
}

func NewCharacteristic(c *ble.Characteristic, buffer int, conn *BLEConnection, descriptors []device.Descriptor) *BLECharacteristic {
	rawUUID := c.UUID.String()
	uuid := device.NormalizeUUID(rawUUID)

	return &BLECharacteristic{
		uuid:        uuid,                                // store normalized
		knownName:   bledb.LookupCharacteristic(rawUUID), // lookup using raw form if DB expects dashed
		BLEChar:     c,
		properties:  NewProperties(c.Property),
		updates:     make(chan *BLEValue, buffer),
		descriptors: descriptors,
		subs:        nil,
		connection:  conn,
	}
}

func (c *BLECharacteristic) EnqueueValue(v *BLEValue) {
	// Recover from panic if the channel closes between the closed check and send.
	// This race window cannot be eliminated without mutex overhead, but is harmless -
	// it only occurs during shutdown when late BLE callbacks arrive.
	defer func() {
		if r := recover(); r != nil {
			if c.connection != nil && c.connection.logger != nil {
				c.connection.logger.Debug("[EnqueueValue] OK: channel closed during send (late callback during shutdown)")
			}
			v.release()
		}
	}()

	// Check if the channel is closed before attempting to send
	// This prevents panic from sending on a closed channel if BLE callbacks fire after shutdown
	if c.closed.Load() {
		v.release()
		return
	}

	if c.connection != nil && c.connection.logger != nil {
		c.connection.logger.WithFields(map[string]interface{}{
			"char": c.uuid,
			"len":  len(v.Data),
		}).Debug("[EnqueueValue] BLE notification arrived, enqueueing")
	}

	select {
	case c.updates <- v:
	default:
		// Channel full, drop the oldest
		old := <-c.updates
		old.Flags |= FlagDropped
		old.release()
		// Recheck closed before second send (could have closed while we were dropping)
		if !c.closed.Load() {
			c.updates <- v
		} else {
			v.release()
		}
	}
}

// Subscribe registers a callback function to be invoked when this characteristic receives notifications.
//
// IMPORTANT: BLEValue objects are pooled and reused for performance. The callback MUST copy
// v.Data immediately if it needs to retain the data beyond the callback invocation, as the
// Data slice becomes invalid after the callback returns and the BLEValue is released back to the pool.
//
// Example:
//
//	char.Subscribe(func(v *BLEValue) {
//	    // Copy data if you need to retain it
//	    dataCopy := make([]byte, len(v.Data))
//	    copy(dataCopy, v.Data)
//	    // Use dataCopy safely after callback returns
//	})
func (c *BLECharacteristic) Subscribe(fn func(*BLEValue)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = append(c.subs, fn)
}

func (c *BLECharacteristic) notifySubscribers(v *BLEValue) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, fn := range c.subs {
		fn(v)
	}
}

func (c *BLECharacteristic) UUID() string {
	return c.uuid
}

func (c *BLECharacteristic) KnownName() string {
	return c.knownName
}

func (c *BLECharacteristic) GetProperties() device.Properties {
	return c.properties
}

func (c *BLECharacteristic) GetDescriptors() []device.Descriptor {
	return c.descriptors
}

// RequiresAuthentication returns true if the characteristic requires pairing/authentication.
// Detection heuristic:
//   - AuthenticatedSignedWrites property indicates explicit authentication requirement
//   - No properties exposed (props == 0) likely indicates hidden due to encryption/pairing requirements
//     (Common on macOS/iOS when peripheral requires encryption before revealing properties)
func (c *BLECharacteristic) RequiresAuthentication() bool {
	if c.properties == nil {
		return false
	}

	// Check if ALL properties are nil (equivalent to underlying ble.Property == 0)
	// macOS/iOS CoreBluetooth limitation: When a peripheral requires encryption/pairing,
	// the OS hides characteristic properties (returns 0) until pairing completes.
	// The peripheral's GATT database may define properties, but CoreBluetooth masks them
	// for security reasons. This is a common indicator that authentication is required.
	hasAnyProperty := c.properties.Broadcast() != nil ||
		c.properties.Read() != nil ||
		c.properties.Write() != nil ||
		c.properties.WriteWithoutResponse() != nil ||
		c.properties.Notify() != nil ||
		c.properties.Indicate() != nil ||
		c.properties.AuthenticatedSignedWrites() != nil ||
		c.properties.ExtendedProperties() != nil

	if !hasAnyProperty {
		return true // No properties exposed - macOS/iOS hides properties until paired
	}

	// AuthenticatedSignedWrites explicitly requires authentication
	if c.properties.AuthenticatedSignedWrites() != nil {
		return true
	}

	// Note: ExtendedProperties alone is not a reliable security indicator
	// Would need to read descriptor 0x2900 to confirm security requirements

	return false
}

func (c *BLECharacteristic) HasParser() bool {
	return device.IsParsableCharacteristic(c.uuid)
}

func (c *BLECharacteristic) ParseValue(value []byte) (interface{}, error) {
	return device.ParseCharacteristicValue(c.uuid, value)
}

// GetValue returns the current cached value of the characteristic.
// IMPORTANT: The returned slice is READ-ONLY. Callers MUST NOT modify it.
// Modifying the returned slice will cause data races and undefined behavior.
func (c *BLECharacteristic) GetValue() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value
}

func (c *BLECharacteristic) SetValue(value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
}

// Read reads the current value of the characteristic from the device with the specified timeout.
// This implements the device.CharacteristicReader interface.
func (c *BLECharacteristic) Read(timeout time.Duration) ([]byte, error) {
	return c.ReadWithTimeout(timeout)
}

// ReadWithTimeout reads the current value of the characteristic from the device with the specified timeout.
// This prevents indefinite blocking if the device becomes unresponsive during a read operation.
func (c *BLECharacteristic) ReadWithTimeout(timeout time.Duration) ([]byte, error) {
	if c.connection == nil {
		return nil, fmt.Errorf("no connection available for reading characteristic %s", c.uuid)
	}

	if c.BLEChar == nil {
		return nil, fmt.Errorf("characteristic %s not initialized", c.uuid)
	}

	// Check read property before attempting read
	readProps := c.properties.Read()
	canRead := readProps != nil && readProps.Value() != 0

	if !canRead {
		return nil, fmt.Errorf("characteristic %s does not support read operations: %w", c.uuid, device.ErrUnsupported)
	}

	// Add connection mutex locking to prevent race condition
	c.connection.connMutex.RLock()
	if c.connection.client == nil {
		c.connection.connMutex.RUnlock()
		return nil, fmt.Errorf("read characteristic %s: %w", c.uuid, device.ErrNotConnected)
	}
	client := c.connection.client
	c.connection.connMutex.RUnlock()

	// Perform read with timeout to prevent indefinite blocking
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)

	groutine.Go(context.Background(), fmt.Sprintf("ble-characteristic-read-%s", c.uuid), func(ctx context.Context) {
		data, err := client.ReadCharacteristic(c.BLEChar)
		resultCh <- readResult{data: data, err: err}
	})

	select {
	case result := <-resultCh:
		if result.err != nil {
			normalizedErr := NormalizeError(result.err)
			// If disconnected, cancel connection context with cause to notify all subscribers
			if errors.Is(normalizedErr, device.ErrNotConnected) {
				if c.connection.cancel != nil {
					if c.connection.logger != nil {
						c.connection.logger.WithError(normalizedErr).Warn("Read detected disconnection, cancelling connection context")
					}
					c.connection.cancel(device.ErrNotConnected)
				} else if c.connection.logger != nil {
					c.connection.logger.Warn("Read detected disconnection but cancel func is nil")
				}
			}
			return nil, fmt.Errorf("failed to read characteristic %s: %w", c.uuid, normalizedErr)
		}
		return result.data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("read characteristic %s after %v: %w", c.uuid, timeout, device.ErrTimeout)
	}
}

// Write writes data to the characteristic with the specified parameters.
// This implements the device.CharacteristicWriter interface.
// The withResponse parameter determines if write-with-response (true) or write-without-response (false) is used.
func (c *BLECharacteristic) Write(data []byte, withResponse bool, timeout time.Duration) error {
	// CoreBluetooth crashes with NSInternalInconsistencyException if data is nil
	if len(data) == 0 {
		return fmt.Errorf("cannot write empty data to characteristic %s", c.uuid)
	}

	if c.connection == nil {
		return fmt.Errorf("no connection available for writing characteristic %s", c.uuid)
	}

	if c.BLEChar == nil {
		return fmt.Errorf("characteristic %s not initialized", c.uuid)
	}

	// Check write properties before attempting write
	writeProps := c.properties.Write()
	writeNoRespProps := c.properties.WriteWithoutResponse()
	canWrite := writeProps != nil && writeProps.Value() != 0
	canWriteNoResponse := writeNoRespProps != nil && writeNoRespProps.Value() != 0

	if !canWrite && !canWriteNoResponse {
		return fmt.Errorf("characteristic %s does not support write operations: %w", c.uuid, device.ErrUnsupported)
	}

	// Add connection mutex locking to prevent race conditions
	c.connection.connMutex.RLock()
	if c.connection.client == nil {
		c.connection.connMutex.RUnlock()
		return fmt.Errorf("write characteristic %s: %w", c.uuid, device.ErrNotConnected)
	}
	client := c.connection.client
	c.connection.connMutex.RUnlock()

	// Acquire write mutex to serialize writes
	c.connection.writeMutex.Lock()
	defer c.connection.writeMutex.Unlock()

	// Perform write with timeout to prevent indefinite blocking
	type writeResult struct {
		err error
	}
	resultCh := make(chan writeResult, 1)

	groutine.Go(context.Background(), fmt.Sprintf("ble-characteristic-write-%s", c.uuid), func(ctx context.Context) {
		// BLE client WriteCharacteristic: noResponse parameter is opposite of withResponse
		err := client.WriteCharacteristic(c.BLEChar, data, !withResponse)
		resultCh <- writeResult{err: err}
	})

	select {
	case result := <-resultCh:
		if result.err != nil {
			normalizedErr := NormalizeError(result.err)
			// If disconnected, cancel connection context with cause to notify all subscribers
			if errors.Is(normalizedErr, device.ErrNotConnected) && c.connection.cancel != nil {
				c.connection.cancel(device.ErrNotConnected)
			}
			return fmt.Errorf("failed to write characteristic %s: %w", c.uuid, normalizedErr)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("write characteristic %s after %v: %w", c.uuid, timeout, device.ErrTimeout)
	}
}

// CloseUpdates safely closes the updates channel (once only, thread-safe)
func (c *BLECharacteristic) CloseUpdates() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.updates)
	}
}

// ResetUpdates recreates the update channel (for reconnection).
// MUST only be called after CloseUpdates() has been called.
// Returns an error if the channel is not closed.
func (c *BLECharacteristic) ResetUpdates(buffer int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Verify channel is closed before resetting
	if !c.closed.Load() {
		return fmt.Errorf("cannot reset updates channel: channel is still open")
	}

	c.updates = make(chan *BLEValue, buffer)
	c.closed.Store(false)
	return nil
}

// HasActiveFanOut returns true if a fan-out goroutine is running for this characteristic.
func (c *BLECharacteristic) HasActiveFanOut() bool {
	c.subChannelsMu.Lock()
	defer c.subChannelsMu.Unlock()
	return c.fanOutCtx != nil
}

// SubscriberCount returns the number of active subscribers to this characteristic.
func (c *BLECharacteristic) SubscriberCount() int {
	c.subChannelsMu.Lock()
	defer c.subChannelsMu.Unlock()
	return len(c.subChannels)
}
