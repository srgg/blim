package goble

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/darwin"
	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/bledb"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/groutine"
)

// ----------------------------
// Configuration Constants
// ----------------------------

const (
	// DefaultChannelBuffer is the default buffer size for notification channels
	DefaultChannelBuffer = 128

	// DefaultUpdateInterval is the default polling interval for StreamEveryUpdate mode
	DefaultUpdateInterval = 5 * time.Millisecond

	// DefaultBatchedInterval is the default rate limiting interval for batched/aggregated modes
	DefaultBatchedInterval = 100 * time.Millisecond

	// DefaultDrainDuration is the time to wait before draining CoreBluetooth cached values.
	// CoreBluetooth delivers stale values asynchronously over ~100-200ms after SetNotify(true).
	DefaultDrainDuration = 500 * time.Millisecond
)

// ----------------------------
// Device Factory
// ----------------------------

// DeviceFactory creates ble.Device instances (can be overridden in device_test)
//
//nolint:revive // DeviceFactory name is intentional for test mocking as device.DeviceFactory
var DeviceFactory = func() (ble.Device, error) {
	return darwin.NewDevice()
}

// OnConnected is called after a BLE connection is successfully established.
// Can be used for connection configuration, logging, or instrumentation.
// Must be set before any connections are made; do not modify during operation.
var OnConnected func(*BLEConnection)

// ----------------------------
// BLE Connection
// ----------------------------

// BLEConnection represents a live BLE connection (notifications, writes)
type BLEConnection struct {
	client                ble.Client
	logger                *logrus.Logger
	writeMutex            sync.Mutex
	connMutex             sync.RWMutex
	isConnected           bool
	descriptorReadTimeout time.Duration // Timeout for reading descriptor values during discovery

	services map[string]*BLEService

	subMgr *SubscriptionManager
	ctx    context.Context
	cancel context.CancelCauseFunc

	valuePool *BLEVValuePool
	ConnDiag  *ConnectionDiagnostics // nil in production, populated in test
}

// GetSubscriptionManager returns the subscription manager for testing.
func (c *BLEConnection) GetSubscriptionManager() *SubscriptionManager {
	return c.subMgr
}

// GetCharacteristic retrieves a characteristic by service and characteristic UUID.
// Both UUIDs are normalized for consistent lookup (lowercase, no dashes).
// Returns a NotFoundError if the service or characteristic is not found.
func (c *BLEConnection) GetCharacteristic(service, uuid string) (device.Characteristic, error) {
	// Normalize UUIDs for a consistent lookup
	normalizedServiceUUID := device.NormalizeUUID(service)
	normalizedCharUUID := device.NormalizeUUID(uuid)

	svc, ok := c.services[normalizedServiceUUID]
	if !ok {
		return nil, &device.NotFoundError{Resource: "service", UUIDs: []string{service}}
	}

	char, ok := svc.Characteristics[normalizedCharUUID]
	if !ok {
		return nil, &device.NotFoundError{Resource: "characteristic", UUIDs: []string{service, uuid}}
	}

	return char, nil
}

// Services returns all discovered BLE services for this connection.
// Services are sorted by UUID for consistent ordering. Thread-safe.
func (c *BLEConnection) Services() []device.Service {
	c.connMutex.RLock()
	defer c.connMutex.RUnlock()

	result := make([]device.Service, 0, len(c.services))
	for _, v := range c.services {
		result = append(result, v)
	}
	// Sort by UUID for consistent ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].UUID() < result[j].UUID()
	})
	return result
}

// GetService retrieves a specific service by its UUID.
// The UUID is normalized for consistent lookup (lowercase, no dashes).
// Returns a NotFoundError if the service is not found.
func (c *BLEConnection) GetService(uuid string) (device.Service, error) {
	c.connMutex.RLock()
	defer c.connMutex.RUnlock()

	// Normalize UUID for lookup
	normalizedUUID := device.NormalizeUUID(uuid)
	svc, ok := c.services[normalizedUUID]
	if !ok {
		return nil, &device.NotFoundError{Resource: "service", UUIDs: []string{uuid}}
	}
	return svc, nil
}

// ProcessCharacteristicNotification processes incoming characteristic notification data
// This method is extracted to allow reuse in both production subscriptions and device_test
func (c *BLEConnection) ProcessCharacteristicNotification(char *BLECharacteristic, data []byte) {
	// Diagnostic logging: trace when notifications arrive from CoreBluetooth
	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"char": char.UUID(),
			"len":  len(data),
			"time": time.Now().Format("15:04:05.000"),
		}).Debug("[BLE] notification received from CoreBluetooth")
	}

	// Create a new BLE value from the received data (from pool for hot-path performance)
	val := c.valuePool.newPooledBLEValue(data)
	val.Char = char

	// Update the characteristic's value
	char.SetValue(data)

	// Enqueue the value for any waiting consumers
	char.EnqueueValue(val)

	// Notify all subscribers
	char.notifySubscribers(val)
}

// SimulateNotification provides a proxy method for testing/simulation capabilities
// This method calls ProcessCharacteristicNotification internally
func (c *BLEConnection) SimulateNotification(char *BLECharacteristic, data []byte) {
	c.ProcessCharacteristicNotification(char, data)
}

func NewBLEConnection(logger *logrus.Logger) *BLEConnection {
	return &BLEConnection{
		client:    nil,
		services:  make(map[string]*BLEService),
		subMgr:    NewSubscriptionManager(logger),
		ctx:       context.Background(),
		cancel:    nil,
		logger:    logger,
		valuePool: NewBLEVValuePool(),
	}
}

// Connect establishes a BLE connection and populates live characteristics
func (c *BLEConnection) Connect(ctx context.Context, address string, opts *device.ConnectOptions) error {
	c.connMutex.Lock()
	defer c.connMutex.Unlock()

	if strings.TrimSpace(address) == "" {
		c.logger.Error("Connection attempt with empty address")
		return fmt.Errorf("device address is empty")
	}

	if c.isConnectedInternal() {
		c.logger.WithField("address", address).Warn("Connection attempt while already connected")
		return device.ErrAlreadyConnected
	}

	// Set descriptor read timeout with default if not explicitly set
	c.descriptorReadTimeout = opts.DescriptorReadTimeout
	if c.descriptorReadTimeout == 0 && opts.DescriptorReadTimeout == 0 {
		// Distinguish between "not set" and "explicitly set to 0"
		// If the field wasn't touched, use default; if explicitly 0, skip reads
		c.descriptorReadTimeout = DefaultDescriptorReadTimeout
	}

	c.logger.WithFields(logrus.Fields{
		"address": address,
		"timeout": opts.ConnectTimeout,
	}).Info("Connecting to BLE device...")

	// Create a BLE device using the factory (allows for mocking in device_test)
	dev, err := DeviceFactory()
	if err != nil {
		c.logger.WithField("error", err).Error("Failed to create BLE device")
		return fmt.Errorf("failed to create BLE device: %w", NormalizeError(err))
	}
	ble.SetDefaultDevice(dev)

	// Timeout context
	connCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	// Connect to BLE device
	c.logger.WithField("address", address).Debug("Dialing BLE device...")
	client, err := ble.Dial(connCtx, ble.NewAddr(address))
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"address": address,
			"error":   err,
		}).Error("Failed to dial BLE device")
		return fmt.Errorf("failed to connect to device with address \"%s\": %w", address, err)
	}

	// Discover services and characteristics
	c.logger.WithField("address", address).Debug("Discovering services and characteristics...")
	bleProfile, err := client.DiscoverProfile(true)
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"address": address,
			"error":   err,
		}).Error("Failed to discover profile")
		if cancelErr := client.CancelConnection(); cancelErr != nil {
			c.logger.WithField("cancel_error", cancelErr).Warn("Failed to cancel connection during profile discovery failure")
		}
		return fmt.Errorf("failed to discover profile: %w", err)
	}

	c.logger.WithFields(logrus.Fields{
		"address":  address,
		"services": len(bleProfile.Services),
	}).Debug("Profile discovered successfully")

	// Populate services and characteristics from BLE Profile
	for _, bleSvc := range bleProfile.Services {
		svcRawUUID := bleSvc.UUID.String()
		svcUUID := device.NormalizeUUID(svcRawUUID)
		c.logger.WithField("service_uuid", svcRawUUID).Trace("Found service UUID")
		svc, ok := c.services[svcUUID]
		if !ok {
			svc = &BLEService{
				uuid:            svcUUID,                         // store normalized
				knownName:       bledb.LookupService(svcRawUUID), // lookup using raw form if DB expects dashed
				Characteristics: make(map[string]*BLECharacteristic),
			}
			c.services[svcUUID] = svc
		}

		for _, bleCharacteristic := range bleSvc.Characteristics {
			charRawUUID := bleCharacteristic.UUID.String()
			charUUID := device.NormalizeUUID(charRawUUID)
			c.logger.WithFields(logrus.Fields{
				"service_uuid": svcUUID,
				"char_uuid":    charRawUUID,
			}).Trace("Found characteristic UUID")
			characteristic, ok := svc.Characteristics[charUUID]
			if !ok {
				// Use descriptors from DiscoverProfile (already discovered)
				// Note: On Darwin/macOS, the BLE library does not populate descriptor Handle fields,
				// which means descriptors cannot be read. This is a limitation of the go-ble/ble Darwin implementation.
				// The descriptors are listed for informational purposes, but their values will be nil.

				// Create descriptors with values (reads are best-effort, won't fail characteristic creation)
				descriptors := make([]device.Descriptor, 0, len(bleCharacteristic.Descriptors))
				for idx, d := range bleCharacteristic.Descriptors {
					descriptors = append(descriptors, newDescriptor(d, c, client, c.descriptorReadTimeout, uint8(idx), c.logger))
				}

				// Parse well-known descriptor formats. if any
				for _, d := range descriptors {
					if d.ParsedValue() != nil {
						continue
					}

					// Parse well-known descriptors automatically
					if parsed, err := device.ParseDescriptorValue(d.UUID(), d.Value(), descriptors); err == nil {
						d.(*BLEDescriptor).parsedValue = parsed
					} else {
						// Parse error - set parsedValue to DescriptorError
						d.(*BLEDescriptor).parsedValue = &device.DescriptorError{
							Reason: "parse_error",
							Err:    err,
						}
						if c.logger != nil {
							c.logger.WithFields(logrus.Fields{
								"descriptor_uuid": d.UUID(),
								"error":           err,
							}).Debug("Failed to parse descriptor value")
						}
					}
				}
				// Sort by UUID for consistent ordering
				sort.Slice(descriptors, func(i, j int) bool {
					return descriptors[i].UUID() < descriptors[j].UUID()
				})

				// Create BLECharacteristic with pre-created descriptors
				characteristic = NewCharacteristic(bleCharacteristic, DefaultChannelBuffer, c, descriptors)
				svc.Characteristics[charUUID] = characteristic
			} else {
				// Reconnecting - update live handle and recreate channel if closed on disconnect
				characteristic.BLEChar = bleCharacteristic
				if characteristic.closed.Load() {
					if err := characteristic.ResetUpdates(DefaultChannelBuffer); err != nil {
						c.logger.WithFields(logrus.Fields{
							"char_uuid": charUUID,
							"error":     err,
						}).Warn("Failed to reset updates channel during reconnection")
					}
				}
			}
		}
	}

	// Mark as connected and assign client
	c.client = client
	c.isConnected = true

	// Set up context for subscriptions - derive from caller's context to tie lifecycle
	// Use WithCancelCause to propagate connection errors to all subscribers
	c.ctx, c.cancel = context.WithCancelCause(ctx)

	// Initialize connection-level diagnostics (nil in production, populated in test)
	c.ConnDiag = newConnectionDiagnostics(c)

	// Monitor go-ble client Disconnected() channel (Darwin-specific)
	// This detects when CoreBluetooth reports a disconnection
	if darwinClient, ok := client.(interface{ Disconnected() <-chan struct{} }); ok {
		// Capture in closure to avoid race with Disconnect() clearing c.cancel/c.ctx
		cancelFunc, connCtx := c.cancel, c.ctx

		groutine.Go(context.Background(), "ble-connection-monitor", func(monitorCtx context.Context) {
			select {
			case <-darwinClient.Disconnected():
				if c.logger != nil {
					c.logger.Warn("CoreBluetooth reported disconnection, cancelling connection context")
				}
				if cancelFunc != nil {
					cancelFunc(device.ErrNotConnected)
				}
			case <-connCtx.Done():
				// Connection context already canceled, exit monitor
			}
		})
	} else if c.logger != nil {
		c.logger.Debug("Client does not support Disconnected() channel (non-Darwin platform?)")
	}

	// Count total characteristics across all services
	totalChars := 0
	for _, svc := range c.services {
		totalChars += len(svc.Characteristics)
	}

	c.logger.WithFields(logrus.Fields{
		"address":         address,
		"services":        len(c.services),
		"characteristics": totalChars,
	}).Info("BLE device connected successfully:\n", c.dumpBLEServicesUnsafe())

	// Invoke a connection callback if set (used for configuration, logging, instrumentation)
	if OnConnected != nil {
		OnConnected(c)
	}

	return nil
}

// drainAndReleaseChannel drains all pending BLEValue objects from a channel and releases them to the pool.
func drainAndReleaseChannel(ch chan *BLEValue) {
	for {
		select {
		case v := <-ch:
			if v == nil {
				return
			}
			v.release()
		default:
			return
		}
	}
}

func (c *BLEConnection) Disconnect() error {
	// Acquire and snapshot state, cancel subs under lock
	c.connMutex.Lock()

	// Grab the client and cancel the function to release the lock before blocking waits
	cancel := c.cancel
	client := c.client
	var servicesCopy map[string]*BLEService

	if c.client == nil || !c.isConnected {
		c.connMutex.Unlock()
		if c.logger != nil {
			c.logger.Debug("Disconnect called but already disconnected")
		}
		return nil
	} else {
		// Connection mutex scope
		defer c.connMutex.Unlock()

		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"connection_ptr": fmt.Sprintf("%p", c),
				"services":       len(c.services),
			}).Info("Disconnecting BLE device...")
		}

		// Cancel all subscriptions via the manager
		if c.logger != nil {
			c.logger.Debug("Cancelling all active subscriptions...")
		}
		c.subMgr.CancelAll()

		// Snapshot services to drain channels outside the lock
		servicesCopy = make(map[string]*BLEService)
		for k, v := range c.services {
			servicesCopy[k] = v
		}

		// set fields to nil/false while still holding lock
		c.client = nil
		c.cancel = nil
		c.isConnected = false
	}

	if c.logger != nil {
		c.logger.Debug("Connection state transitioned to disconnected")
	}

	// Cancel the connection-level context (if present)
	if cancel != nil {
		if c.logger != nil {
			c.logger.Debug("Cancelling connection context...")
		}
		cancel(nil) // Normal disconnection, no error cause
	}

	// Wait for all subscription goroutines to exit via manager
	if c.logger != nil {
		c.logger.Debug("Waiting for subscription goroutines to exit...")
	}
	c.subMgr.Wait()
	if c.logger != nil {
		c.logger.Debug("All subscription goroutines exited")
	}

	// Unsubscribe from remote BLE notifications before canceling the connection
	if c.logger != nil {
		c.logger.Debug("Unsubscribing from remote BLE notifications...")
	}
	unsubscribeErrors := c.unsubscribeAllCharacteristics(client, servicesCopy)
	if len(unsubscribeErrors) > 0 && c.logger != nil {
		c.logger.WithField("errors", strings.Join(unsubscribeErrors, "; ")).Warn("Failed to unsubscribe from some characteristics during disconnect")
	}

	// Drain and close per-characteristic update channels
	for _, service := range servicesCopy {
		for _, char := range service.Characteristics {
			drainAndReleaseChannel(char.updates)
			// Close channel to signal EOF - will be recreated on reconnect
			char.CloseUpdates()
		}
	}

	// Verify cleanup AFTER draining all channels (test build only, no-op in prod)
	// VerifyDisconnectCleanup returns errors; caller must handle them.
	// Use DiagnosticPanic for fail-fast behavior on explicit Disconnect().
	if errors := c.ConnDiag.VerifyDisconnectCleanup(); len(errors) > 0 {
		DiagnosticPanic(c.ConnDiag, errors, "Disconnect")
	}

	// Finally, disconnect the BLE client (network call) outside the lock
	var disconnectErr error
	if client != nil {
		disconnectErr = client.CancelConnection()
	}

	if c.logger != nil {
		if disconnectErr != nil {
			c.logger.WithField("error", disconnectErr).Warn("BLE device disconnected with errors")
		} else {
			c.logger.Info("BLE device disconnected successfully")
		}
	}

	return disconnectErr
}

// isConnectedInternal checks the connection status without acquiring locks.
// Should only be called when the caller already holds connMutex.RLock() or connMutex.Lock().
func (c *BLEConnection) isConnectedInternal() bool {
	return c.client != nil && c.isConnected
}

// tryUnsubscribe attempts to unsubscribe from a characteristic using both notify and indicate modes.
// Returns error only if both modes fail. Logs success/failure appropriately.
func (c *BLEConnection) tryUnsubscribe(client ble.Client, char *BLECharacteristic, serviceUUID, charUUID string) error {
	if char.BLEChar == nil {
		return nil
	}

	err1 := NormalizeError(client.Unsubscribe(char.BLEChar, false)) // notify
	err2 := NormalizeError(client.Unsubscribe(char.BLEChar, true))  // indicate

	// Only return error if both notify and indicate failed
	if err1 != nil && err2 != nil {
		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"serviceUUID": serviceUUID,
				"charUUID":    charUUID,
				"notifyErr":   err1,
				"indicateErr": err2,
			}).Error("Failed to unsubscribe from characteristic notifications")
		}
		return fmt.Errorf("%s: notify=%v, indicate=%v", charUUID, err1, err2)
	}

	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"serviceUUID": serviceUUID,
			"charUUID":    charUUID,
		}).Debug("Unsubscribed from characteristic notifications")
	}
	return nil
}

// unsubscribeAllCharacteristics unsubscribes from all characteristics in the given services.
// Returns a list of error messages for failed unsubscriptions.
// Should be called without holding locks.
func (c *BLEConnection) unsubscribeAllCharacteristics(client ble.Client, services map[string]*BLEService) []string {
	var unsubscribeErrors []string

	if client == nil {
		return unsubscribeErrors
	}

	for serviceUUID, service := range services {
		for charUUID, char := range service.Characteristics {
			if err := c.tryUnsubscribe(client, char, serviceUUID, charUUID); err != nil {
				unsubscribeErrors = append(unsubscribeErrors, fmt.Sprintf("%s (in service %s): %v", charUUID, serviceUUID, err))
			}
		}
	}

	return unsubscribeErrors
}

func (c *BLEConnection) IsConnected() bool {
	c.connMutex.RLock()
	defer c.connMutex.RUnlock()
	connected := c.isConnectedInternal()
	if c.logger != nil {
		c.logger.WithFields(logrus.Fields{
			"connected": connected,
			"client":    c.client != nil,
			"flag":      c.isConnected,
		}).Debug("IsConnected() called")
	}
	return connected
}

// ConnectionContext returns the connection context canceled when the connection
// experiences errors or is disconnected. All subscribers should monitor this context.
// Returns nil if not connected.
func (c *BLEConnection) ConnectionContext() context.Context {
	return c.ctx
}

// validateSubscribeOptions validates service and characteristics existence and notification support
func (c *BLEConnection) validateSubscribeOptions(opts *device.SubscribeOptions, requireNotificationSupport bool) (map[string]*BLECharacteristic, error) {
	// Comprehensive validation - collect ALL issues before failing
	var missingServices []string
	var missingChars []string
	var unsupportedChars []string
	characteristicsToProcess := make(map[string]*BLECharacteristic)

	// Normalize UUIDs for consistent lookup (BLE library uses lowercase, no dashes)
	normalizedServiceUUID := device.NormalizeUUID(opts.Service)
	normalizedCharUUIDs := device.NormalizeUUIDs(opts.Characteristics)

	// Validate service exists using normalized UUID
	service, serviceExists := c.services[normalizedServiceUUID]
	if !serviceExists {
		missingServices = append(missingServices, opts.Service)
	} else {
		// Service exists, now validate characteristics
		if len(opts.Characteristics) == 0 {
			// Validate all characteristics in service
			for charUUID, char := range service.Characteristics {
				if char.BLEChar == nil {
					missingChars = append(missingChars, fmt.Sprintf("%s (in service %s)", charUUID, opts.Service))
				} else if requireNotificationSupport {
					// Check specific mode support (Indicate vs Notify)
					if opts.Indicate {
						if char.BLEChar.Property&ble.CharIndicate == 0 {
							unsupportedChars = append(unsupportedChars, fmt.Sprintf("%s: does not support indicate", charUUID))
						} else {
							characteristicsToProcess[charUUID] = char
						}
					} else {
						if char.BLEChar.Property&ble.CharNotify == 0 {
							unsupportedChars = append(unsupportedChars, fmt.Sprintf("%s: does not support notify", charUUID))
						} else {
							characteristicsToProcess[charUUID] = char
						}
					}
				} else {
					characteristicsToProcess[charUUID] = char
				}
			}
		} else {
			// Validate specific requested characteristics using normalized UUIDs
			for i, charUUID := range opts.Characteristics {
				normalizedCharUUID := normalizedCharUUIDs[i]
				char, charExists := service.Characteristics[normalizedCharUUID]
				if !charExists {
					missingChars = append(missingChars, fmt.Sprintf("%s (in service %s)", charUUID, opts.Service))
				} else if char.BLEChar == nil {
					missingChars = append(missingChars, fmt.Sprintf("%s (in service %s)", charUUID, opts.Service))
				} else if requireNotificationSupport {
					// Check specific mode support (Indicate vs Notify)
					if opts.Indicate {
						if char.BLEChar.Property&ble.CharIndicate == 0 {
							unsupportedChars = append(unsupportedChars, fmt.Sprintf("%s: does not support indicate", charUUID))
						} else {
							characteristicsToProcess[normalizedCharUUID] = char
						}
					} else {
						if char.BLEChar.Property&ble.CharNotify == 0 {
							unsupportedChars = append(unsupportedChars, fmt.Sprintf("%s: does not support notify", charUUID))
						} else {
							characteristicsToProcess[normalizedCharUUID] = char
						}
					}
				} else {
					characteristicsToProcess[normalizedCharUUID] = char
				}
			}
		}
	}

	// Report comprehensive validation results
	if len(missingServices) > 0 || len(missingChars) > 0 || len(unsupportedChars) > 0 {
		var errorParts []string

		if len(missingServices) > 0 {
			errorParts = append(errorParts, fmt.Sprintf("missing services: %s", strings.Join(missingServices, ", ")))
		}
		if len(missingChars) > 0 {
			errorParts = append(errorParts, fmt.Sprintf("missing characteristics: %s", strings.Join(missingChars, ", ")))
		}
		if len(unsupportedChars) > 0 {
			errorParts = append(errorParts, fmt.Sprintf("characteristics without notification support: %s", strings.Join(unsupportedChars, ", ")))
		}

		// Wrap with device.ErrUnsupported when there are characteristics without notification support
		if len(unsupportedChars) > 0 {
			return nil, fmt.Errorf("validation failed - %s: %w", strings.Join(errorParts, "; "), device.ErrUnsupported)
		}
		return nil, fmt.Errorf("validation failed - %s", strings.Join(errorParts, "; "))
	}

	return characteristicsToProcess, nil
}

func (c *BLEConnection) BLESubscribe(opts *device.SubscribeOptions) error {
	// Acquire lock, validate, copy characteristics, then release lock before network calls
	c.connMutex.RLock()

	// Check if connected
	if !c.isConnectedInternal() {
		c.connMutex.RUnlock()
		return device.ErrNotConnected
	}

	// Validate subscription options and get characteristics
	characteristicsToSubscribe, err := c.validateSubscribeOptions(opts, true)
	if err != nil {
		c.connMutex.RUnlock()
		return fmt.Errorf("subscription validation failed: %w", err)
	}

	// If no characteristics support notifications after validation
	if len(characteristicsToSubscribe) == 0 {
		c.connMutex.RUnlock()
		return fmt.Errorf("no characteristics available for subscription in service %s", opts.Service)
	}

	// Copy characteristics and get client reference
	characteristicsCopy := make(map[string]*BLECharacteristic)
	for k, v := range characteristicsToSubscribe {
		characteristicsCopy[k] = v
	}
	client := c.client
	c.connMutex.RUnlock()

	// All validation passed - proceed with subscriptions outside the lock
	var subscriptionErrors []string
	subscriptionMode := "notify"
	if opts.Indicate {
		subscriptionMode = "indicate"
	}

	for charUUID, char := range characteristicsCopy {
		// Mode validation already done in validateSubscribeOptions
		// Create a local variable to capture the current char
		charCapture := char
		err := NormalizeError(client.Subscribe(char.BLEChar, opts.Indicate, func(data []byte) {
			c.ProcessCharacteristicNotification(charCapture, data)
		}))

		if err != nil {
			subscriptionErrors = append(subscriptionErrors, fmt.Sprintf("%s: %v", charUUID, err))
			if c.logger != nil {
				c.logger.WithFields(logrus.Fields{
					"serviceUUID": opts.Service,
					"charUUID":    charUUID,
					"mode":        subscriptionMode,
					"error":       err,
				}).Error("Failed to subscribe to characteristic")
			}
		} else {
			if c.logger != nil {
				c.logger.WithFields(logrus.Fields{
					"serviceUUID": opts.Service,
					"charUUID":    charUUID,
					"mode":        subscriptionMode,
				}).Info("Successfully subscribed to characteristic")
			}
		}
	}

	// Return error if any subscriptions failed
	if len(subscriptionErrors) > 0 {
		return fmt.Errorf("subscription failures - %s", strings.Join(subscriptionErrors, "; "))
	}

	return nil
}

func (c *BLEConnection) BLEUnsubscribe(opts *device.SubscribeOptions) error {
	// Acquire lock, validate, copy characteristics, then release lock before network calls
	c.connMutex.RLock()

	// Validate specific subscription options (don't require notification support for unsubscribe)
	characteristicsToUnsubscribe, err := c.validateSubscribeOptions(opts, false)
	if err != nil {
		c.connMutex.RUnlock()
		return fmt.Errorf("unsubscribe validation failed: %w", err)
	}

	// If no characteristics found after validation
	if len(characteristicsToUnsubscribe) == 0 {
		c.connMutex.RUnlock()
		return fmt.Errorf("no characteristics available for unsubscribe in service %s", opts.Service)
	}

	// Copy characteristics and get client reference
	characteristicsCopy := make(map[string]*BLECharacteristic)
	for k, v := range characteristicsToUnsubscribe {
		characteristicsCopy[k] = v
	}
	client := c.client
	c.connMutex.RUnlock()

	// All validation passed - proceed with unsubscriptions outside the lock
	var unsubscribeErrors []string
	for charUUID, char := range characteristicsCopy {
		if err := c.tryUnsubscribe(client, char, opts.Service, charUUID); err != nil {
			unsubscribeErrors = append(unsubscribeErrors, err.Error())
		}
	}

	// Return error if any unsubscriptions failed
	if len(unsubscribeErrors) > 0 {
		return fmt.Errorf("unsubscribe failures - %s", strings.Join(unsubscribeErrors, "; "))
	}

	return nil
}

// unsubscribeInternal performs unsubscribe operations
// Acquires and releases locks as needed to avoid deadlocks
func (c *BLEConnection) unsubscribeInternal(opts *device.SubscribeOptions) error {
	// Handle unsubscribes from all subscriptions when opts is nil
	if opts == nil {
		var unsubscribeErrors []string

		// Acquire lock to cancel subscriptions and snapshot state
		c.connMutex.Lock()

		// Cancel all subscriptions via the manager
		c.subMgr.CancelAll()

		// Snapshot client and services for operations outside the lock
		client := c.client
		servicesCopy := make(map[string]*BLEService)
		for k, v := range c.services {
			servicesCopy[k] = v
		}

		c.connMutex.Unlock()

		// Wait for all subscription goroutines to exit via manager (outside lock to avoid deadlock)
		c.subMgr.Wait()

		// Unsubscribe from remote BLE notifications
		unsubscribeErrors = c.unsubscribeAllCharacteristics(client, servicesCopy)

		// Drain per-characteristic update channels and release BLEValue objects
		for _, service := range servicesCopy {
			for _, char := range service.Characteristics {
				drainAndReleaseChannel(char.updates)
			}
		}

		if len(unsubscribeErrors) > 0 {
			return fmt.Errorf("unsubscribe failures - %s", strings.Join(unsubscribeErrors, "; "))
		}

		if c.logger != nil {
			c.logger.Info("Successfully unsubscribed from all characteristic notifications (local cleanup done)")
		}
		return nil
	}

	// Acquire lock to validate and snapshot client
	c.connMutex.RLock()

	// Validate specific subscription options (don't require notification support for unsubscribe)
	characteristicsToUnsubscribe, err := c.validateSubscribeOptions(opts, false)
	if err != nil {
		c.connMutex.RUnlock()
		return fmt.Errorf("unsubscribe validation failed: %w", err)
	}

	// If no characteristics found after validation
	if len(characteristicsToUnsubscribe) == 0 {
		c.connMutex.RUnlock()
		return fmt.Errorf("no characteristics available for unsubscribe in service %s", opts.Service)
	}

	// Snapshot client for network operations outside the lock
	client := c.client
	c.connMutex.RUnlock()

	// All validation passed - proceed with unsubscriptions
	var unsubscribeErrors []string
	for charUUID, char := range characteristicsToUnsubscribe {
		if err := c.tryUnsubscribe(client, char, opts.Service, charUUID); err != nil {
			unsubscribeErrors = append(unsubscribeErrors, err.Error())
		}
	}

	// Return error if any unsubscriptions failed
	if len(unsubscribeErrors) > 0 {
		return fmt.Errorf("unsubscribe failures - %s", strings.Join(unsubscribeErrors, "; "))
	}

	return nil
}

// dumpBLEServicesUnsafe returns a concise, human-readable string of the BLEConnection services.
func (c *BLEConnection) dumpBLEServicesUnsafe() string {
	if len(c.services) == 0 {
		return "<no services discovered>"
	}

	var b strings.Builder
	for svcUUID, svc := range c.services {
		fmt.Fprintf(&b, "Service: %s (%s)\n", svcUUID, svc.KnownName())
		chars := svc.GetCharacteristics()
		if len(chars) == 0 {
			continue
		}

		for _, ch := range chars {
			props := ch.GetProperties()
			propNames := []string{}

			addProp := func(p device.Property) {
				if p != nil && p.Value() != 0 {
					propNames = append(propNames, p.KnownName())
				}
			}

			addProp(props.Broadcast())
			addProp(props.Read())
			addProp(props.Write())
			addProp(props.WriteWithoutResponse())
			addProp(props.Notify())
			addProp(props.Indicate())
			addProp(props.AuthenticatedSignedWrites())
			addProp(props.ExtendedProperties())

			_, _ = fmt.Fprintf(&b, "  Characteristic: %s (%s)", ch.UUID(), ch.KnownName())
			if len(propNames) > 0 {
				_, _ = fmt.Fprintf(&b, " %s", strings.Join(propNames, ", "))
			}
			_, _ = fmt.Fprintf(&b, "\n")

			descs := ch.GetDescriptors()
			if len(descs) == 0 {
				continue
			}
			for _, d := range descs {
				_, _ = fmt.Fprintf(&b, "    Descriptor: %s (%s)\n", d.UUID(), d.KnownName())
			}
		}
	}

	return b.String()
}
