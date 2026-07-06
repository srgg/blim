//go:build test

package testutils

import (
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
	devicemocks "github.com/srgg/blim/internal/testutils/mocks/device"
)

// AdvertisementBuilder builds mocked BLE advertisements for testing.
// It provides a fluent API for configuring MockAdvertisement instances
// with explicit field tracking to ensure only set fields have mock expectations.
//
// Type parameter T determines the return type of Build():
//   - *MockAdvertisement for standalone usage
//   - Custom parent type when using parent chaining (buildFunc must be set)
type AdvertisementBuilder[T any] struct {
	name        string
	address     string
	rssi        int
	services    []string
	manufData   []byte
	serviceData map[string][]byte
	txPower     *int
	connectable bool
	logger      *logrus.Logger

	// Track which fields were explicitly set
	nameSet        bool
	addressSet     bool
	rssiSet        bool
	servicesSet    bool
	manufDataSet   bool
	serviceDataSet bool
	txPowerSet     bool
	connectableSet bool

	// Optional parent chaining support
	parent    T
	buildFunc func(T, device.Advertisement) T
}

// NewAdvertisementBuilder creates a new AdvertisementBuilder with default values.
// The builder starts with connectable=true and empty serviceData map.
func NewAdvertisementBuilder() *AdvertisementBuilder[*devicemocks.MockAdvertisement] {
	return &AdvertisementBuilder[*devicemocks.MockAdvertisement]{
		serviceData: make(map[string][]byte),
		connectable: true,
	}
}

// NewAdvertisementBuilderWithParent creates a new AdvertisementBuilder with parent chaining support.
// The builder will call buildFunc with parent and the built advertisement when Build() is called.
func NewAdvertisementBuilderWithParent[T any](parent T, buildFunc func(T, device.Advertisement) T) *AdvertisementBuilder[T] {
	return &AdvertisementBuilder[T]{
		serviceData: make(map[string][]byte),
		connectable: true,
		parent:      parent,
		buildFunc:   buildFunc,
	}
}

// WithName sets the local name for the advertisement.
func (b *AdvertisementBuilder[T]) WithName(name string) *AdvertisementBuilder[T] {
	b.name = name
	b.nameSet = true
	return b
}

// WithAddress sets the device address for the advertisement.
func (b *AdvertisementBuilder[T]) WithAddress(addr string) *AdvertisementBuilder[T] {
	b.address = addr
	b.addressSet = true
	return b
}

// WithRSSI sets the signal strength for the advertisement.
func (b *AdvertisementBuilder[T]) WithRSSI(rssi int) *AdvertisementBuilder[T] {
	b.rssi = rssi
	b.rssiSet = true
	return b
}

// WithServices adds service UUIDs to the advertisement.
// UUIDs can be in short form (e.g., "180D") or full form.
func (b *AdvertisementBuilder[T]) WithServices(uuids ...string) *AdvertisementBuilder[T] {
	b.services = append(b.services, uuids...)
	b.servicesSet = true
	return b
}

// WithManufacturerData sets the manufacturer-specific data.
func (b *AdvertisementBuilder[T]) WithManufacturerData(data []byte) *AdvertisementBuilder[T] {
	b.manufData = data
	b.manufDataSet = true
	return b
}

// WithServiceData adds service-specific data for the given service UUID.
func (b *AdvertisementBuilder[T]) WithServiceData(uuid string, data []byte) *AdvertisementBuilder[T] {
	b.serviceData[uuid] = data
	b.serviceDataSet = true
	return b
}

// WithNoServiceData explicitly sets service data to nil.
// Use this to distinguish between unset and empty service data.
func (b *AdvertisementBuilder[T]) WithNoServiceData() *AdvertisementBuilder[T] {
	b.serviceDataSet = true
	b.serviceData = nil
	return b
}

// WithTxPower sets the transmission power level.
func (b *AdvertisementBuilder[T]) WithTxPower(power int) *AdvertisementBuilder[T] {
	b.txPower = &power
	b.txPowerSet = true
	return b
}

// WithConnectable sets whether the device accepts connections.
func (b *AdvertisementBuilder[T]) WithConnectable(c bool) *AdvertisementBuilder[T] {
	b.connectable = c
	b.connectableSet = true
	return b
}

// FromJSON fills builder fields from a JSON string with format support.
// Panics on invalid JSON as this is intended for test data setup.
func (b *AdvertisementBuilder[T]) FromJSON(jsonStrFmt string, args ...interface{}) *AdvertisementBuilder[T] {
	jsonStr := fmt.Sprintf(jsonStrFmt, args...)

	// First, detect which fields are present in the JSON (even if null)
	var fieldPresence map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &fieldPresence); err != nil {
		panic(fmt.Sprintf("FromJSON: failed to unmarshal field presence: %v", err))
	}

	// Then unmarshal into typed struct
	var data struct {
		Name             *string           `json:"name"`
		Address          *string           `json:"address"`
		RSSI             *int              `json:"rssi"`
		Services         []string          `json:"services"`
		ManufacturerData []byte            `json:"manufacturerData"`
		ServiceData      map[string][]byte `json:"serviceData"`
		TxPower          *int              `json:"txPower"`
		Connectable      *bool             `json:"connectable"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		panic(err)
	}

	// Set flags based on field presence, not just non-nil values
	if _, exists := fieldPresence["name"]; exists {
		if data.Name != nil {
			b.name = *data.Name
		} else {
			b.name = ""
		}
		b.nameSet = true
	}
	if _, exists := fieldPresence["address"]; exists {
		if data.Address != nil {
			b.address = *data.Address
		} else {
			b.address = ""
		}
		b.addressSet = true
	}
	if _, exists := fieldPresence["rssi"]; exists {
		if data.RSSI != nil {
			b.rssi = *data.RSSI
		} else {
			b.rssi = -50 // default
		}
		b.rssiSet = true
	}
	if _, exists := fieldPresence["services"]; exists {
		b.services = data.Services // nil becomes empty slice
		b.servicesSet = true
	}
	if _, exists := fieldPresence["manufacturerData"]; exists {
		b.manufData = data.ManufacturerData // nil becomes empty slice
		b.manufDataSet = true
	}
	if _, exists := fieldPresence["serviceData"]; exists {
		if data.ServiceData != nil {
			b.serviceData = data.ServiceData
		} else {
			b.serviceData = make(map[string][]byte)
		}
		b.serviceDataSet = true
	}
	if _, exists := fieldPresence["txPower"]; exists {
		b.txPower = data.TxPower // can be nil
		b.txPowerSet = true
	}
	if _, exists := fieldPresence["connectable"]; exists {
		if data.Connectable != nil {
			b.connectable = *data.Connectable
		} else {
			b.connectable = true // default
		}
		b.connectableSet = true
	}
	return b
}

// Build creates a MockAdvertisement that implements device.Advertisement interface.
// All mock expectations are set based on explicitly configured fields only.
// Following testify best practices for mock setup.
//
// If buildFunc is set (parent chaining), calls buildFunc with the advertisement and returns type T.
// Otherwise returns the MockAdvertisement (when T is *MockAdvertisement).
func (b *AdvertisementBuilder[T]) Build() T {
	adv := &devicemocks.MockAdvertisement{}

	// Convert service data to the expected format
	var deviceServiceData []struct {
		UUID string
		Data []byte
	}
	for uuid, data := range b.serviceData {
		deviceServiceData = append(deviceServiceData, struct {
			UUID string
			Data []byte
		}{
			UUID: uuid,
			Data: data,
		})
	}

	// Setup mock expectations using testify best practices
	// Only set expectations for explicitly configured fields
	if b.addressSet {
		adv.On("Addr").Return(b.address)
	}
	if b.nameSet {
		adv.On("LocalName").Return(b.name)
	}
	if b.rssiSet {
		adv.On("RSSI").Return(b.rssi)
	}
	if b.manufDataSet {
		adv.On("ManufacturerData").Return(b.manufData)
	}
	if b.serviceDataSet {
		adv.On("ServiceData").Return(deviceServiceData)
	}
	if b.servicesSet {
		adv.On("Services").Return(b.services)
	}
	if b.connectableSet {
		adv.On("Connectable").Return(b.connectable)
	}
	if b.txPowerSet {
		if b.txPower != nil {
			adv.On("TxPowerLevel").Return(*b.txPower)
		} else {
			adv.On("TxPowerLevel").Return(127) // BLE spec default for unavailable
		}
	}

	// Set up OverflowService and SolicitedService - always return empty slices
	// These are rarely used but part of the Advertisement interface
	// Mark as Maybe() since they're only called in scan simulation, not by device code
	adv.On("OverflowService").Return([]string{}).Maybe()
	adv.On("SolicitedService").Return([]string{}).Maybe()

	// Note: Appearance is NOT part of advertisements
	// It is read from the GAP service (0x1800) during connection, not from advertisement packets

	// If parent chaining is configured, call buildFunc and return its result
	if b.buildFunc != nil {
		return b.buildFunc(b.parent, adv)
	}

	// Otherwise return the advertisement (cast to T)
	var result interface{} = adv
	return result.(T)
}

// BuildDevice creates a device.Device by building a fresh MockAdvertisement internally.
// Convenience method for creating Device instances in device_test without needing to call Build() separately.
func (b *AdvertisementBuilder[T]) BuildDevice(logger *logrus.Logger) device.Device {
	// Build returns T, but we know for BuildDevice it must be *MockAdvertisement
	var result interface{} = b.Build()
	adv := result.(*devicemocks.MockAdvertisement)
	return goble.NewBLEDeviceFromAdvertisement(adv, logger)
}

// AdvertisementArrayBuilder builds arrays of device.Advertisement with generic parent support.
// It provides a fluent API for creating multiple advertisements with different configurations
// and supports returning to parent builders through the generic type parameter T.
//
// The builder supports two main patterns:
//   - WithAdvertisements(ads...) adds pre-existing device.Advertisement(s) to the array
//   - WithNewAdvertisement() returns an AdvertisementBuilder for fluent configuration
//
// Type Parameter:
//
//	T: The type to return from Build(). Common values:
//	  - []device.Advertisement for standalone usage
//	  - *PeripheralDeviceBuilder for integration with device builders
//
// Example usage with pre-existing advertisements:
//
//	// Create advertisements separately
//	ad1 := NewAdvertisementBuilder().WithName("Device1").Build()
//	ad2 := NewAdvertisementBuilder().WithName("Device2").Build()
//
//	// Build an array with a mix of pre-existing and new advertisements
//	 := NewAdvertisementArrayBuilder[[]device.Advertisement]().
//	    WithAdvertisements(ad1, ad2). // Add multiple at once
//	    WithNewAdvertisement().
//	        WithName("HeartRate3").
//	        WithAddress("11:22:33:44:55:66").
//	        WithRSSI(-55).
//	        Build().
//	    Build() // Returns []device.Advertisement
//
// Integration with PeripheralDeviceBuilder:
//
//	// Create advertisements separately
//	existingAds := []device.Advertisement{ad1, ad2}
//
//	peripheral := NewPeripheralDeviceBuilder(t).
//	    WithScanAdvertisements().
//	        WithAdvertisements(existingAds...). // Spread slice
//	        WithNewAdvertisement().WithName("Device3").Build().
//	        Build(). // Returns *PeripheralDeviceBuilder
//	    WithService("180D").
//	    Build()
type AdvertisementArrayBuilder[T any] struct {
	advertisements []device.Advertisement
	parent         T
	buildFunc      func(T, int, []device.Advertisement) T
	scanDelayMs    int // Delay in milliseconds before emitting each advertisement
}

// NewAdvertisementArrayBuilder creates a new array builder with the specified generic type.
func NewAdvertisementArrayBuilder[T any]() *AdvertisementArrayBuilder[T] {
	return &AdvertisementArrayBuilder[T]{
		advertisements: make([]device.Advertisement, 0),
	}
}

// WithScanDelay sets the delay in milliseconds before emitting advertisements during scan.
// This simulates real-world BLE scanning where advertisements arrive over time.
// Useful for testing timeout behavior.
func (sb *AdvertisementArrayBuilder[T]) WithScanDelay(delayMs int) *AdvertisementArrayBuilder[T] {
	sb.scanDelayMs = delayMs
	return sb
}

// WithBlockingScan configures the scan to block indefinitely after emitting advertisements.
// This simulates real-world BLE scan behavior where the scan continues until the context is canceled.
// Useful for testing interrupt handling (SIGINT) and watch mode behavior.
func (sb *AdvertisementArrayBuilder[T]) WithBlockingScan() *AdvertisementArrayBuilder[T] {
	sb.scanDelayMs = -1 // Sentinel value for blocking mode
	return sb
}

// WithAdvertisements adds pre-existing Advertisements to the array and returns the array builder for chaining.
// Supports adding multiple advertisements in a single call.
func (ab *AdvertisementArrayBuilder[T]) WithAdvertisements(ads ...device.Advertisement) *AdvertisementArrayBuilder[T] {
	ab.advertisements = append(ab.advertisements, ads...)
	return ab
}

// WithNewAdvertisement adds a new advertisement to the array and returns an AdvertisementBuilder.
// When Build() is called on the returned AdvertisementBuilder, it will add the advertisement
// to the array and return the AdvertisementArrayBuilder for method chaining.
func (ab *AdvertisementArrayBuilder[T]) WithNewAdvertisement() *AdvertisementArrayBuilderItem[T] {
	builder := NewAdvertisementBuilder()

	// Create a custom builder that knows about its parent array builder
	customBuilder := &AdvertisementBuilder[*devicemocks.MockAdvertisement]{
		serviceData: make(map[string][]byte),
		connectable: true,
		name:        builder.name,
		address:     builder.address,
		rssi:        builder.rssi,
		services:    builder.services,
		manufData:   builder.manufData,
		txPower:     builder.txPower,
		logger:      builder.logger,
	}

	return &AdvertisementArrayBuilderItem[T]{
		AdvertisementBuilder: customBuilder,
		parent:               ab,
	}
}

// Build returns the parent if it exists and has a buildFunc, otherwise returns the array
func (ab *AdvertisementArrayBuilder[T]) Build() T {
	if ab.buildFunc != nil {
		return ab.buildFunc(ab.parent, ab.scanDelayMs, ab.advertisements)
	}
	// If no buildFunc, cast advertisements to T (this works for []*mocks.MockAdvertisement)
	var result interface{} = ab.advertisements
	return result.(T)
}

// AdvertisementArrayBuilderItem wraps AdvertisementBuilder to provide array functionality.
// It embeds AdvertisementBuilder and adds the capability to return to the parent array builder.
type AdvertisementArrayBuilderItem[T any] struct {
	*AdvertisementBuilder[*devicemocks.MockAdvertisement]
	parent *AdvertisementArrayBuilder[T]
}

// Build adds the advertisement to the parent array and returns the array builder
func (abi *AdvertisementArrayBuilderItem[T]) Build() *AdvertisementArrayBuilder[T] {
	advertisement := abi.AdvertisementBuilder.Build()
	abi.parent.advertisements = append(abi.parent.advertisements, advertisement)
	return abi.parent
}
