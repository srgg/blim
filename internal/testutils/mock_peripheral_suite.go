//go:build test

package testutils

import (
	"context"
	"fmt"
	"testing"
	"time"

	blelib "github.com/go-ble/ble"
	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
	goble "github.com/srg/blim/internal/device/go-ble"
	"github.com/stretchr/testify/suite"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// MockBLEPeripheralSuite provides a reusable test suite with mock BLE peripheral support.
// It follows testify/suite best practices and provides standardized BLE mocking capabilities.
//
// The suite automatically handles device factory lifecycle management and provides
// a fluent API for configuring mock BLE peripherals with services, characteristics,
// and advertisements.
//
// Basic usage (automatic setup with default battery service):
//
//	type SimpleSuite struct {
//	    testutils.MockBLEPeripheralSuite
//	}
//
//	func TestSimpleSuite(t *testing.T) {
//	    suite.Run(t, new(SimpleSuite))
//	}
//
// Custom device profile usage:
//
//	type InspectSuite struct {
//	    testutils.MockBLEPeripheralSuite
//	}
//
//	func (s *InspectSuite) SetupTest() {
//	    // Configure custom peripheral with Heart Rate service first
//	    s.WithPeripheral().
//	        WithService("180D"). // Heart Rate Service
//	        WithCharacteristic("2A37", "read,notify", []byte{80}) // 80 BPM
//
//	    s.MockBLEPeripheralSuite.SetupTest() // Call parent last to apply configuration
//	}
//
// Scanner with advertisement usage:
//
//	type ScannerSuite struct {
//	    testutils.MockBLEPeripheralSuite
//	}
//
//	func (s *ScannerSuite) SetupTest() {
//	    // Configure scan advertisements first
//	    adv1 := testutils.NewAdvertisementBuilder().
//	        WithAddress("AA:BB:CC:DD:EE:FF").WithName("HeartRate1").Build()
//	    adv2 := testutils.NewAdvertisementBuilder().
//	        WithAddress("11:22:33:44:55:66").WithName("HeartRate2").Build()
//
//	    s.WithAdvertisements().
//	        WithAdvertisements(adv1, adv2).
//	        Build()
//
//	    s.MockBLEPeripheralSuite.SetupTest() // Call parent last to apply configuration
//	}
//
// MockBLEPeripheralSuite embeds testify/suite.Suite and provides BLE-specific test utilities.
type MockBLEPeripheralSuite struct {
	suite.Suite

	// Core test utilities
	Helper *TestHelper    // Test helper with logging and assertions
	Logger *logrus.Logger // Structured logger for test output

	// BLE device factory management
	OriginalDeviceFactory func() (blelib.Device, error) // Backup of the original factory
	TestTimeout           time.Duration                 // Default timeout for BLE operations

	// Mock peripheral configuration
	PeripheralBuilder *PeripheralDeviceBuilder // Builder for configuring mock devices

	// Mock advertisements configuration
	AdvertisementsBuilder *AdvertisementArrayBuilder[[]device.Advertisement] // Builder for configuring mocked Advertisements for Scan
}

// SetupSuite initializes the test suite following testify/suite best practices.
// Called once before all tests in the suite.
func (s *MockBLEPeripheralSuite) SetupSuite() {
	s.Helper = NewTestHelper(s.T())
	s.Logger = s.Helper.Logger
	s.TestTimeout = 30 * time.Second

	// Save the original BLE device factory for restoration
	s.OriginalDeviceFactory = goble.DeviceFactory

	// Configure all test connections: disable drain delay for mock connections.
	// Real BLE connections coild wait DrainDuration (~500ms) after enabling notifications to let
	// CoreBluetooth delivers stale cached values, then drains them. Mock connections don't
	// have this behavior, so setting to 0 avoids unnecessary delays in tests.
	goble.OnConnected = func(c *goble.BLEConnection) {
		c.DrainDuration = 0
	}

	// Use t.Cleanup for automatic resource restoration (testify/suite best practice)
	s.T().Cleanup(func() {
		if s.OriginalDeviceFactory != nil {
			goble.DeviceFactory = s.OriginalDeviceFactory
			s.Logger.Debug("Device factory restored via t.Cleanup")
		}
		goble.OnConnected = nil
	})

	s.Logger.Debug("Suite setup completed")
}

// SetupTest configures the mock device factory before each test.
// Called before each test method.
func (s *MockBLEPeripheralSuite) SetupTest() {
	if s.PeripheralBuilder == nil {
		s.PeripheralBuilder = createDefaultPeripheralBuilder(s.T())
	}

	if s.AdvertisementsBuilder != nil {
		s.PeripheralBuilder.
			WithScanAdvertisements().
			WithAdvertisements(s.AdvertisementsBuilder.Build()...).
			Build()

	}

	// Set up the default device factory
	s.OriginalDeviceFactory = goble.DeviceFactory
	goble.DeviceFactory = func() (blelib.Device, error) {
		return s.PeripheralBuilder.Build(), nil
	}

	s.Logger.Debug("Test setup completed - ready for execution")
}

// TearDownTest resets the peripheral builder after each test.
// Called after each test method.
// Note: Disconnect channel cleanup is handled automatically via t.Cleanup() registered in Build().
func (s *MockBLEPeripheralSuite) TearDownTest() {
	// Restore the device factory to prevent nil pointer panics in subsequent tests
	if s.OriginalDeviceFactory != nil {
		goble.DeviceFactory = s.OriginalDeviceFactory
	}

	// Reset peripheral builder to clean state
	s.PeripheralBuilder = nil
	s.AdvertisementsBuilder = nil
}

// TearDownSuite performs final cleanup after all tests complete.
// Device factory restoration is handled automatically via t.Cleanup().
func (s *MockBLEPeripheralSuite) TearDownSuite() {
	s.Logger.Debug("Suite teardown completed")
}

// ConnectDeviceWithContext creates a mock BLE device, connects it with the given context, and configures it for testing.
// DrainDuration is set to 0 via OnConnected (mocks don't have CoreBluetooth cached values to drain).
// Returns the device and a cleanup function that disconnects the device.
// If opts is nil or ConnectTimeout is 0, defaults to 5 seconds.
func (s *MockBLEPeripheralSuite) ConnectDeviceWithContext(ctx context.Context, address string, opts *device.ConnectOptions) (device.Device, func()) {
	if opts == nil {
		opts = &device.ConnectOptions{}
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 5 * time.Second
	}

	dev := goble.NewBLEDeviceWithAddress(address, s.Logger)
	err := dev.Connect(ctx, opts)
	s.Require().NoError(err, "connection MUST succeed")

	return dev, func() { _ = dev.Disconnect() }
}

// ConnectDevice creates a mock BLE device, connects it, and configures it for testing.
// Convenience wrapper for ConnectDeviceWithContext using context.Background().
func (s *MockBLEPeripheralSuite) ConnectDevice(address string, opts *device.ConnectOptions) (device.Device, func()) {
	return s.ConnectDeviceWithContext(context.Background(), address, opts)
}

// WithPeripheral returns the peripheral builder for fluent configuration.
// Use this method to configure custom device profiles in the test setup.
func (s *MockBLEPeripheralSuite) WithPeripheral() *PeripheralDeviceBuilder {
	if s.PeripheralBuilder == nil {
		s.PeripheralBuilder = NewPeripheralDeviceBuilder(s.T())
	}

	s.Logger.Debug("Peripheral configuration started")
	return s.PeripheralBuilder
}

// WithAdvertisements returns the advertisement array builder for configuring scan advertisements.
// Use this method to set up scan advertisements in the test setup.
func (s *MockBLEPeripheralSuite) WithAdvertisements() *AdvertisementArrayBuilder[[]device.Advertisement] {

	if s.AdvertisementsBuilder == nil {
		s.AdvertisementsBuilder = NewAdvertisementArrayBuilder[[]device.Advertisement]()
	}

	s.Logger.Debug("Advertisements configuration started")
	return s.AdvertisementsBuilder
}

// createDefaultPeripheralBuilder creates a default PeripheralDeviceBuilder for testing.
// Returns a PeripheralDeviceBuilder that creates a mock peripheral with Battery Service (180F)
// and Battery Level characteristic (2A19) set to 50%.
func createDefaultPeripheralBuilder(t *testing.T) *PeripheralDeviceBuilder {
	return NewPeripheralDeviceBuilder(t).
		FromJSON(`
		{
			"services": [
				{
					"uuid": "180F",
					"characteristics": [
						{ "uuid": "2A19", "properties": "read,notify", "value": [50] }
					]
				}
			]
		}`)
}

// PeripheralDataSimulatorBuilder provides a fluent API for simulating BLE characteristic notifications.
// It maintains insertion order for services and characteristics, sending notifications round-robin
// style when multiple values are configured.
//
// # Basic Usage (explicit connection)
//
//	suite.NewPeripheralDataSimulator().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{0x01}).
//	        WithCharacteristic("2a38", []byte{0x02}).
//	    Build().
//	    SimulateFor(conn, false)
//
// # Connection Provider Pattern
//
// For test suites that have a known connection source (e.g., LuaApiSuite),
// use WithConnectionProvider to enable the simpler Simulate(verbose) API:
//
//	// In your suite's factory method override:
//	func (suite *MySuite) NewPeripheralDataSimulator() *testutils.PeripheralDataSimulatorBuilder {
//	    return suite.MockBLEPeripheralSuite.NewPeripheralDataSimulator().
//	        WithConnectionProvider(func() device.Connection {
//	            return suite.GetConnection()
//	        })
//	}
//
//	// Then in tests:
//	suite.NewPeripheralDataSimulator().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{0x01}).
//	    Simulate(false)  // No connection parameter needed
//
// # Multi-Value Mode
//
// By default, calling WithCharacteristic() multiple times with the same UUID
// overwrites the previous value. Enable AllowMultiValue() to queue multiple
// notifications per characteristic (sent round-robin across all characteristics):
//
//	suite.NewPeripheralDataSimulator().
//	    AllowMultiValue().
//	    WithService("180d").
//	        WithCharacteristic("2a37", []byte{60}).  // First notification
//	        WithCharacteristic("2a37", []byte{62}).  // Second notification
//	    Simulate(true)
type PeripheralDataSimulatorBuilder struct {
	suite           *suite.Suite
	allowMultiValue bool
	serviceData     *orderedmap.OrderedMap[string, *orderedmap.OrderedMap[string, [][]byte]]
	logf            func(format string, args ...any)
	connProvider    func() device.Connection // Optional: enables Simulate(verbose) without explicit conn
}

// ServiceDataSimulatorBuilder builds notification simulation for a specific service.
type ServiceDataSimulatorBuilder struct {
	parent      *PeripheralDataSimulatorBuilder
	serviceUUID string
}

// NewPeripheralDataSimulator creates a new builder for multi-service notification simulation.
func (s *MockBLEPeripheralSuite) NewPeripheralDataSimulator() *PeripheralDataSimulatorBuilder {
	return &PeripheralDataSimulatorBuilder{
		suite:           &s.Suite,
		serviceData:     orderedmap.New[string, *orderedmap.OrderedMap[string, [][]byte]](),
		allowMultiValue: false,
		logf:            s.T().Logf,
	}
}

// AllowMultiValue enables sending multiple values to the same characteristic.
func (b *PeripheralDataSimulatorBuilder) AllowMultiValue() *PeripheralDataSimulatorBuilder {
	b.allowMultiValue = true
	return b
}

// WithConnectionProvider sets a function that provides the connection for Simulate().
// This enables Simulate(verbose) without passing connection explicitly.
func (b *PeripheralDataSimulatorBuilder) WithConnectionProvider(fn func() device.Connection) *PeripheralDataSimulatorBuilder {
	b.connProvider = fn
	return b
}

// WithService adds a service to the simulation and returns a service-specific builder.
func (b *PeripheralDataSimulatorBuilder) WithService(serviceUUID string) *ServiceDataSimulatorBuilder {
	_, exists := b.serviceData.Get(serviceUUID)
	if !exists {
		b.serviceData.Set(serviceUUID, orderedmap.New[string, [][]byte]())
	}
	return &ServiceDataSimulatorBuilder{
		parent:      b,
		serviceUUID: serviceUUID,
	}
}

// WithCharacteristic adds a characteristic with data to this service.
func (s *ServiceDataSimulatorBuilder) WithCharacteristic(charUUID string, data []byte) *ServiceDataSimulatorBuilder {
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
func (s *ServiceDataSimulatorBuilder) WithService(serviceUUID string) *ServiceDataSimulatorBuilder {
	return s.parent.WithService(serviceUUID)
}

// Build returns the parent PeripheralDataSimulatorBuilder for continued chaining.
func (s *ServiceDataSimulatorBuilder) Build() *PeripheralDataSimulatorBuilder {
	if s.parent == nil {
		panic("BUG: ServiceDataSimulatorBuilder.parent is nil - builder was not properly initialized")
	}
	return s.parent
}

// Simulate executes all configured characteristic data simulations.
// Requires WithConnectionProvider() to be called first, otherwise panics.
func (s *ServiceDataSimulatorBuilder) Simulate(verbose bool) *ServiceDataSimulatorBuilder {
	if s.parent.connProvider == nil {
		panic("Simulate: no connection provider set - call WithConnectionProvider() or use SimulateFor(conn, verbose)")
	}
	conn := s.parent.connProvider()
	if _, err := s.parent.SimulateFor(conn, verbose); err != nil {
		panic(fmt.Sprintf("ServiceDataSimulatorBuilder.Simulate: %v", err))
	}
	return s
}

// SimulateFor executes all configured characteristic data simulations using the provided connection.
// It sends notifications index-by-index across all characteristics (round-robin style) in insertion order.
func (b *PeripheralDataSimulatorBuilder) SimulateFor(conn device.Connection, verbose bool) (*PeripheralDataSimulatorBuilder, error) {
	b.suite.NotNil(conn, "Connection should be available")

	bleConn, ok := conn.(*goble.BLEConnection)
	if !ok {
		return nil, fmt.Errorf("connection is not a *goble.BLEConnection (got %T)", conn)
	}

	// Find maximum number of values across all characteristics
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
		b.logf("Starting BLE simulation - services=%d characteristics=%d max_notifications_per_char=%d",
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
					b.logf("ERROR: Failed to get characteristic %s:%s - %v", serviceUUID, charUUID, err)
					if !b.suite.NoError(err, "Should be able to get characteristic %s:%s", serviceUUID, charUUID) {
						continue
					}
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

				bleConn.ProcessCharacteristicNotification(bleChar, data)
			}
		}
	}

	if verbose {
		b.logf("BLE simulation completed - sent=%d notifications, errors=%d", notificationCount, errorCount)
	}

	return b, nil
}
