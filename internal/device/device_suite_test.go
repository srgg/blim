//go:build test

package device_test

import (
	"reflect"
	"time"
	"unsafe"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
)

type DeviceTestSuite2 struct {
	testutils.PeripheralDeviceSuite
}

// SetupTest configures a default peripheral with Generic Access (1800), Battery Service (180F), and Heart Rate Service (180D)
func (suite *DeviceTestSuite2) SetupTest() {
	// Call parent to apply the configuration and set up the device factory
	suite.PeripheralDeviceSuite.SetupTest()

	suite.GivenPeripheral(
		func(b *testutils.PeripheralDeviceBuilder) {
			b.WithService("1800"). // Generic Access
						WithCharacteristic("2A00", "read", []byte("Test Device")).                                  // Device Name (mandatory, read)
						WithCharacteristic("2A01", "read", []byte{0x40, 0x00}).                                     // Appearance (mandatory, read) - Phone (0x0040, little-endian)
						WithCharacteristic("2A04", "read", []byte{0x08, 0x00, 0x10, 0x00, 0x00, 0x00, 0xE8, 0x03}). // Peripheral Preferred Connection Parameters (optional, read) - min=10ms, max=20ms, latency=0, timeout=10s
						WithService("180F").                                                                        // Battery Service
						WithCharacteristic("2A19", "read", []byte{85}).                                             // Battery Level (mandatory, read)
						WithCharacteristic("2A20", "read", []byte{}).
						WithService("180D").                                                                    // Heart Rate Service
						WithCharacteristic("2A37", "notify", []byte{0, 75}).                                    // Heart Rate Measurement (mandatory, notify)
						WithCharacteristic("2A38", "read", []byte{1}).                                          // Body Sensor Location (optional, read)
						WithCharacteristic("2A39", "write", []byte{}).                                          // Heart Rate Control Point (optional, write)
						WithCharacteristic("2A3A", "indicate", []byte{0x00}).                                   // Indicate-only characteristic (for subscription mode device_test)
						WithCharacteristic("2A3B", "notify,indicate", []byte{0x00}).                            // Both notify and indicate (for subscription mode device_test)
						WithCharacteristic("2A40", "read,write", []byte{0x00}).                                 // Test characteristic (read, write)
						WithCharacteristic("2A41", "read", []byte{42}, testutils.WithReadDelay(1*time.Second)). // Test characteristic with read delay
						WithCharacteristic("2A42", "write", []byte{}, testutils.WithWriteDelay(1*time.Second)). // Test characteristic with write delay
						WithCharacteristic("FFFF", "read", []byte{0xAA, 0xBB})                                  // Unknown characteristic UUID for testing

		},
	)
}

// setDeviceConnectionToNil uses unsafe reflection to set the device's connection field to nil.
// This enables testing defensive checks for error paths that should never happen in production.
// Uses unsafe.Pointer to bypass Go's unexported field access restrictions.
func (suite *DeviceTestSuite2) setDeviceConnectionToNil(device device.Device) {
	// Disconnect first to satisfy cleanup verification (RegisterTestConnectionCleanup).
	// Without this, the connection remains registered but unreachable, causing cleanup failures.
	_ = device.Disconnect()

	devValue := reflect.ValueOf(device).Elem()
	connectionField := devValue.FieldByName("connection")

	// Use unsafe to bypass unexported field restrictions
	reflect.NewAt(connectionField.Type(), unsafe.Pointer(connectionField.UnsafeAddr())).
		Elem().
		Set(reflect.Zero(connectionField.Type()))
}
