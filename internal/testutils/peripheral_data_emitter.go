//go:build test

package testutils

import (
	"fmt"
	"testing"

	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// PeripheralDataEmitter provides a fluent API for simulating a BLE peripheral
// characteristic notifications.
//
// It allows test code to define services, characteristics, and notification
// values that are emitted as if they were coming from a real BLE peripheral.
//
// Notifications respect insertion order: services and characteristics are
// emitted in the order they are added. When multiple values are configured for
// the same characteristic, notifications are sent round-robin across all
// configured characteristics.
//
// The emitter can push notifications using either an explicit connection
// (SimulateFor) or a connection provider function (Simulate) for convenience.
//
// # Basic Usage (explicit connection)
//
//	NewPeripheralDataEmitter().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{0x01}).
//	        WithCharacteristic("2a38", []byte{0x02}).
//	    SimulateFor(conn, false)
//
//	NewPeripheralDataEmitter().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{0x01}).
//	    Emmit(false)
//
// # Multi-Value Notifications,
//
// By default, multiple calls to WithCharacteristic with the same UUID
// overwrite the previous value. Enable AllowMultiValue() to queue multiple
// notification values per characteristic. They are emitted round-robin
// across all configured characteristics:
//
//	NewPeripheralDataEmitter().
//	    AllowMultiValue().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{60}).  // First notification
//	        WithCharacteristic("2a37", []byte{62}).  // Second notification
//	    Emmit(true)
type PeripheralDataEmitter struct {
	t               *testing.T
	allowMultiValue bool
	serviceData     *orderedmap.OrderedMap[string, *orderedmap.OrderedMap[string, [][]byte]]
	logf            func(format string, args ...any)
	connProvider    func() device.Connection // Optional: enables Simulate(verbose) without explicit conn
}

// ServiceDataEmitter emits simulated service notifications for testing.
type ServiceDataEmitter struct {
	parent      *PeripheralDataEmitter
	serviceUUID string
}

// WithCharacteristic adds a characteristic with data to this service.
func (s *ServiceDataEmitter) WithCharacteristic(charUUID string, data ...byte) *ServiceDataEmitter {
	charMap, exists := s.parent.serviceData.Get(s.serviceUUID)
	if !exists {
		panic(fmt.Sprintf("WithCharacteristic: must call WithService() before adding characteristics (attempted to add characteristic %q to service %q)", charUUID, s.serviceUUID))
	}

	if s.parent.allowMultiValue {
		existing, _ := charMap.Get(charUUID)
		charMap.Set(charUUID, append(existing, data))
	} else {
		existing, hasExisting := charMap.Get(charUUID)
		if !hasExisting || len(existing) == 0 {
			charMap.Set(charUUID, [][]byte{data})
		} else {
			existing[0] = data
			charMap.Set(charUUID, existing)
		}
	}
	return s
}

// WithService adds another service to the simulation.
func (s *ServiceDataEmitter) WithService(serviceUUID string) *ServiceDataEmitter {
	return s.parent.WithService(serviceUUID)
}

// Build returns the parent PeripheralDataSimulatorBuilder for continued chaining.
func (s *ServiceDataEmitter) Build() *PeripheralDataEmitter {
	if s.parent == nil {
		panic("BUG: ServiceDataEmitter.parent is nil - builder was not properly initialized")
	}
	return s.parent
}

// Emit executes all configured characteristic data simulations.
// Requires WithConnectionProvider() to be called first, otherwise panics.
// After emitting, accumulated data is cleared so subsequent Emit calls only send new data.
func (s *ServiceDataEmitter) Emit(verbose bool) *ServiceDataEmitter {
	if s.parent.connProvider == nil {
		panic("Simulate: no connection provider set - call WithConnectionProvider() or use SimulateFor(conn, verbose)")
	}
	conn := s.parent.connProvider()
	if _, err := s.parent.EmmitFor(conn, verbose); err != nil {
		panic(fmt.Sprintf("ServiceDataEmitter.Simulate: %v", err))
	}
	s.parent.Clear()
	return s
}

// NewPeripheralDataEmitter creates a new builder for multiservice notification simulation.
func NewPeripheralDataEmitter(t *testing.T) *PeripheralDataEmitter {
	return &PeripheralDataEmitter{
		t:               t,
		serviceData:     orderedmap.New[string, *orderedmap.OrderedMap[string, [][]byte]](),
		allowMultiValue: false,
		logf:            t.Logf,
	}
}

func NewPeripheralDataEmitterWithConnection(t *testing.T, conn device.Connection) *PeripheralDataEmitter {
	return &PeripheralDataEmitter{
		t:               t,
		serviceData:     orderedmap.New[string, *orderedmap.OrderedMap[string, [][]byte]](),
		allowMultiValue: false,
		logf:            t.Logf,
		connProvider: func() device.Connection {
			return conn
		},
	}

}

// AllowMultiValue enables sending multiple values to the same characteristic.
func (b *PeripheralDataEmitter) AllowMultiValue() *PeripheralDataEmitter {
	b.allowMultiValue = true
	return b
}

// Clear resets accumulated characteristic data while preserving service structure.
func (b *PeripheralDataEmitter) Clear() *PeripheralDataEmitter {
	for servicePair := b.serviceData.Oldest(); servicePair != nil; servicePair = servicePair.Next() {
		// Replace each service's characteristic map with an empty one
		b.serviceData.Set(servicePair.Key, orderedmap.New[string, [][]byte]())
	}
	return b
}

// WithConnectionProvider sets a function that provides the connection for Simulate().
// This enables Simulate(verbose) without passing connection explicitly.
func (b *PeripheralDataEmitter) WithConnectionProvider(fn func() device.Connection) *PeripheralDataEmitter {
	b.connProvider = fn
	return b
}

// WithService adds a service to the simulation and returns a service-specific builder.
func (b *PeripheralDataEmitter) WithService(serviceUUID string) *ServiceDataEmitter {
	_, exists := b.serviceData.Get(serviceUUID)
	if !exists {
		b.serviceData.Set(serviceUUID, orderedmap.New[string, [][]byte]())
	}
	return &ServiceDataEmitter{
		parent:      b,
		serviceUUID: serviceUUID,
	}
}

// EmmitFor executes all configured characteristic data simulations using the provided connection.
// It sends notifications index-by-index across all characteristics (round-robin style) in insertion order.
func (b *PeripheralDataEmitter) EmmitFor(conn device.Connection, verbose bool) (*PeripheralDataEmitter, error) {
	if conn == nil {
		return nil, fmt.Errorf("conn must not be nil")
	}

	bleConn, ok := conn.(*goble.BLEConnection)
	if !ok {
		return nil, fmt.Errorf("connection is not a *goble.BLEConnection (got %T)", conn)
	}

	// Find the maximum number of values across all characteristics
	maxIndex := 0
	serviceCount := 0
	charCount := 0

	for servicePair := b.serviceData.Oldest(); servicePair != nil; servicePair = servicePair.Next() {
		serviceCount++
		charMap := servicePair.Value
		for charPair := charMap.Oldest(); charPair != nil; charPair = charPair.Next() {
			charCount++
			dataList := charPair.Value
			if len(dataList) > maxIndex {
				maxIndex = len(dataList)
			}
		}
	}

	if verbose {
		b.logf("Data emitting - services=%d, characteristics=%d, max_notifications_per_char=%d",
			serviceCount, charCount, maxIndex)
	}

	// Iterate index-by-index across all characteristics in insertion order
	notificationCount := 0
	errorCount := 0
	for idx := 0; idx < maxIndex; idx++ {
		for servicePair := b.serviceData.Oldest(); servicePair != nil; servicePair = servicePair.Next() {
			serviceUUID := servicePair.Key
			charMap := servicePair.Value

			for charPair := charMap.Oldest(); charPair != nil; charPair = charPair.Next() {
				charUUID := charPair.Key
				dataList := charPair.Value

				if idx >= len(dataList) {
					continue
				}

				testChar, err := conn.GetCharacteristic(serviceUUID, charUUID)
				if err != nil {
					errorCount++
					b.t.Errorf("ERROR: Failed to get characteristic %s:%s - %v", serviceUUID, charUUID, err)
				}

				data := dataList[idx]
				notificationCount++

				if verbose {
					b.logf("Sending notification #%d: service=%s char=%s data=%q (len=%d)",
						notificationCount, serviceUUID, charUUID, data, len(data))
				}

				bleChar, ok := testChar.(*goble.BLECharacteristic)
				if !ok {
					errorCount++
					b.logf("ERROR: Characteristic %s:%s is not a *goble.BLECharacteristic (got %T)", serviceUUID, charUUID, testChar)
					continue
				}

				// DEBUG: Log characteristic details before notification
				b.logf("DEBUG: Notifying char=%s, char_ptr=%p, subs_count=%d, has_fanout=%v",
					bleChar.UUID(), bleChar, bleChar.SubscriberCount(), bleChar.HasActiveFanOut())

				bleConn.ProcessCharacteristicNotification(bleChar, data)
			}
		}
	}

	if verbose {
		b.logf("BLE notifications emitting completed - sent=%d notifications, errors=%d",
			notificationCount, errorCount)
	}

	return b, nil
}
