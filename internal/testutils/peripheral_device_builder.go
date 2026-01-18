//go:build test

package testutils

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	blelib "github.com/go-ble/ble"
	"github.com/srg/blim/internal/device"
	goble "github.com/srg/blim/internal/device/go-ble"
	blemocks "github.com/srg/blim/internal/testutils/mocks/goble"
	"github.com/stretchr/testify/mock"
	"gopkg.in/yaml.v3"
)

// createMockUUID creates a ble.UUID from a string for testing
func createMockUUID(name string) blelib.UUID {
	// Parse as proper UUID - will panic if invalid, which is fine for device_test
	return blelib.MustParse(name)
}

// DescriptorReadBehavior specifies error behavior when reading a descriptor
type DescriptorReadBehavior int

const (
	// DescriptorReadTimeout - read blocks and times out
	DescriptorReadTimeout DescriptorReadBehavior = iota + 1 // Start from 1, 0 means normal behavior
	// DescriptorReadError - read returns an error
	DescriptorReadError
)

// UnmarshalYAML implements custom YAML unmarshalling for DescriptorReadBehavior.
// Converts YAML string values to enum constants:
//   - "timeout" → DescriptorReadTimeout
//   - any other non-empty string → DescriptorReadError
//   - empty string or omitted → 0 (no error, normal behavior)
func (d *DescriptorReadBehavior) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	switch s {
	case "timeout":
		*d = DescriptorReadTimeout
	case "":
		*d = 0 // Normal behavior (no error)
	default:
		// Any other non-empty string (like "permission denied") means error
		*d = DescriptorReadError
	}
	return nil
}

// DescriptorConfig represents a BLE descriptor configuration for mocking
type DescriptorConfig struct {
	UUID              string                 `json:"uuid" yaml:"uuid"`
	Value             []byte                 `json:"value,omitempty" yaml:"value,omitempty"`
	ReadErrorBehavior DescriptorReadBehavior `json:"-" yaml:"read_error,omitempty"` // Maps YAML read_error field to enum
}

// CharacteristicConfig represents a BLE characteristic configuration for mocking
type CharacteristicConfig struct {
	UUID         string             `json:"uuid" yaml:"uuid"`
	Properties   string             `json:"properties,omitempty" yaml:"properties,omitempty"` // e.g., "read,write,notify"
	NoProperties bool               `json:"-" yaml:"-"`                                       // If true, characteristic has no properties (property flags = 0)
	Value        []byte             `json:"value,omitempty" yaml:"value,omitempty"`
	Descriptors  []DescriptorConfig `json:"descriptors,omitempty" yaml:"descriptors,omitempty"`
	ReadDelay    time.Duration      `json:"-" yaml:"-"` // Delay before returning read response (for timeout testing)
	WriteDelay   time.Duration      `json:"-" yaml:"-"` // Delay before returning write response (for timeout testing)
}

// ServiceConfig represents a BLE service configuration for mocking
type ServiceConfig struct {
	UUID            string                 `json:"uuid" yaml:"service"`
	Characteristics []CharacteristicConfig `json:"characteristics,omitempty" yaml:"characteristics,omitempty"`
}

// DeviceProfileConfig represents the complete device profile for mocking
type DeviceProfileConfig struct {
	Services []ServiceConfig `json:"services"`
}

// AggregateFormatDescriptorBuilder builds an Aggregate Format descriptor (0x2905)
// along with its referenced Presentation Format descriptors (0x2904).
//
// The builder automatically:
//   - Adds all Presentation Format descriptors to the characteristic
//   - Calculates their ATT handles based on descriptor position
//   - Creates the Aggregate Format descriptor with encoded handle references
//   - Returns to the characteristic builder for continued fluent chaining
//
// # Usage
//
//	builder.WithService("1234").
//	    WithCharacteristic("5678").
//	        WithAggregateFormatDescriptor().
//	            WithPresentationFormat([]byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}).
//	            WithPresentationFormat([]byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}).
//	            Build()
type AggregateFormatDescriptorBuilder struct {
	parentBuilder       *PeripheralDeviceBuilder
	presentationFormats [][]byte // Values for 0x2904 descriptors
}

// WithPresentationFormat adds a Presentation Format descriptor (0x2904) that will be
// referenced by the aggregate. Multiple calls add multiple format descriptors.
func (b *AggregateFormatDescriptorBuilder) WithPresentationFormat(value []byte) *AggregateFormatDescriptorBuilder {
	b.presentationFormats = append(b.presentationFormats, value)
	return b
}

// Build finalizes the aggregate descriptor and returns to the characteristic builder.
// Adds all Presentation Format descriptors (0x2904) followed by the Aggregate Format
// descriptor (0x2905) with properly encoded handle references.
func (b *AggregateFormatDescriptorBuilder) Build() *PeripheralDeviceBuilder {
	// Get the current descriptor count using the same pattern as addDescriptor
	lastServiceIdx := len(b.parentBuilder.profile.Services) - 1
	lastCharIdx := len(b.parentBuilder.profile.Services[lastServiceIdx].Characteristics) - 1
	currentDescriptorCount := len(b.parentBuilder.profile.Services[lastServiceIdx].Characteristics[lastCharIdx].Descriptors)

	// Add all 0x2904 Presentation Format descriptors
	formatDescriptorStartIndex := currentDescriptorCount
	for _, formatValue := range b.presentationFormats {
		b.parentBuilder.WithDescriptor("2904", formatValue...)
	}

	// Calculate handles for the presentation format descriptors
	// Handles are assigned sequentially: 0x0100 + descriptor index
	var aggregateValue []byte
	for i := 0; i < len(b.presentationFormats); i++ {
		handle := uint16(0x0100 + formatDescriptorStartIndex + i)
		// Encode as little-endian uint16
		aggregateValue = append(aggregateValue, byte(handle), byte(handle>>8))
	}

	// Add the 0x2905 Aggregate Format descriptor with encoded handles
	b.parentBuilder.WithDescriptor("2905", aggregateValue...)

	return b.parentBuilder
}

// PeripheralDeviceBuilder builds a mocked BLE Device with full service/characteristic support.
//
// Supports building complex BLE device profiles with services, characteristics, and descriptors.
// Includes error simulation for descriptor reads (timeout, permission errors).
//
// # Basic Usage
//
//	builder := NewPeripheralDeviceBuilder(t)
//	builder.WithService("180d").
//	    WithCharacteristic("2a37", "read,notify", []byte{60}).
//	    WithDescriptor("2902", []byte{0x01, 0x00})
//	device := builder.Build()
//
// # Error Simulation
//
// Simulate descriptor read errors for testing error handling:
//
//	builder.WithService("180d").
//	    WithCharacteristic("2a37", "read,notify", []byte{}).
//	    WithDescriptorReadTimeout("2902")  // Simulates 10s timeout
//
//	builder.WithService("180d").
//	    WithCharacteristic("2a37", "read,notify", []byte{}).
//	    WithDescriptorReadError("2902")  // Returns permission denied error
//
// # YAML Integration
//
// The builder config structs support YAML/JSON unmarshaling with automatic error behavior conversion:
//
//	descriptors:
//	  - uuid: "2902"
//	    read_error: "timeout"  # Automatically converts to DescriptorReadTimeout
//	  - uuid: "2901"
//	    read_error: "permission denied"  # Converts to DescriptorReadError
//	  - uuid: "2900"
//	    value: [0x00, 0x00]  # Normal descriptor with value
type PeripheralDeviceBuilder struct {
	profile            DeviceProfileConfig
	scanAdvertisements []device.Advertisement
	scanDelayMs        int           // Delay in milliseconds before emitting each advertisement during scan
	t                  *testing.T    // Testing instance for automatic cleanup registration
	disconnectChan     chan struct{} // Disconnect channel for graceful disconnect testing
}

// NewPeripheralDeviceBuilder creates a new peripheral device builder.
// The testing instance t is required for automatic cleanup of disconnect channels via t.Cleanup().
func NewPeripheralDeviceBuilder(t *testing.T) *PeripheralDeviceBuilder {
	return &PeripheralDeviceBuilder{
		profile: DeviceProfileConfig{
			Services: []ServiceConfig{},
		},
		t: t,
	}
}

// WithService adds a service to the device profile
func (b *PeripheralDeviceBuilder) WithService(uuid string) *PeripheralDeviceBuilder {
	b.profile.Services = append(b.profile.Services, ServiceConfig{
		UUID:            uuid,
		Characteristics: []CharacteristicConfig{},
	})
	return b
}

// CharacteristicOption is a functional option for configuring characteristics
type CharacteristicOption func(*CharacteristicConfig)

// WithReadDelay sets the read delay for timeout testing
func WithReadDelay(delay time.Duration) CharacteristicOption {
	return func(c *CharacteristicConfig) {
		c.ReadDelay = delay
	}
}

// WithWriteDelay sets the writing delay for timeout testing
func WithWriteDelay(delay time.Duration) CharacteristicOption {
	return func(c *CharacteristicConfig) {
		c.WriteDelay = delay
	}
}

// WithCharacteristic adds a characteristic to the last added service
func (b *PeripheralDeviceBuilder) WithCharacteristic(uuid, properties string, value []byte, opts ...CharacteristicOption) *PeripheralDeviceBuilder {
	if len(b.profile.Services) == 0 {
		panic("WithCharacteristic: no service added yet, call WithService first")
	}

	lastServiceIdx := len(b.profile.Services) - 1
	char := CharacteristicConfig{
		UUID:        uuid,
		Properties:  properties,
		Value:       value,
		Descriptors: []DescriptorConfig{},
	}

	// Apply functional options
	for _, opt := range opts {
		opt(&char)
	}

	b.profile.Services[lastServiceIdx].Characteristics = append(
		b.profile.Services[lastServiceIdx].Characteristics, char)
	return b
}

// WithCharacteristicNoProperties adds a characteristic with no properties (property flags = 0) to the last added service.
// This is useful for testing characteristics that require pairing/authentication, where macOS/iOS CoreBluetooth
// hides the properties until the device is paired.
func (b *PeripheralDeviceBuilder) WithCharacteristicNoProperties(uuid string, value []byte, opts ...CharacteristicOption) *PeripheralDeviceBuilder {
	if len(b.profile.Services) == 0 {
		panic("WithCharacteristicNoProperties: no service added yet, call WithService first")
	}

	lastServiceIdx := len(b.profile.Services) - 1
	char := CharacteristicConfig{
		UUID:         uuid,
		NoProperties: true, // Skip property parsing and use 0
		Properties:   "",   // Ignored when NoProperties is true
		Value:        value,
		Descriptors:  []DescriptorConfig{},
	}

	// Apply functional options
	for _, opt := range opts {
		opt(&char)
	}

	b.profile.Services[lastServiceIdx].Characteristics = append(
		b.profile.Services[lastServiceIdx].Characteristics, char)
	return b
}

// addDescriptor adds a descriptor to the last added characteristic (internal helper)
func (b *PeripheralDeviceBuilder) addDescriptor(desc DescriptorConfig) *PeripheralDeviceBuilder {
	if len(b.profile.Services) == 0 {
		panic("addDescriptor: no service added yet, call WithService first")
	}

	lastServiceIdx := len(b.profile.Services) - 1
	if len(b.profile.Services[lastServiceIdx].Characteristics) == 0 {
		panic("addDescriptor: no characteristic added yet, call WithCharacteristic first")
	}

	lastCharIdx := len(b.profile.Services[lastServiceIdx].Characteristics) - 1
	b.profile.Services[lastServiceIdx].Characteristics[lastCharIdx].Descriptors = append(
		b.profile.Services[lastServiceIdx].Characteristics[lastCharIdx].Descriptors, desc)
	return b
}

// WithDescriptor adds a descriptor to the last added characteristic
func (b *PeripheralDeviceBuilder) WithDescriptor(uuid string, value ...byte) *PeripheralDeviceBuilder {
	return b.addDescriptor(DescriptorConfig{UUID: uuid, Value: value})
}

// WithAggregateFormatDescriptor creates a builder for adding an Aggregate Format descriptor
// with its referenced Presentation Format descriptors.
func (b *PeripheralDeviceBuilder) WithAggregateFormatDescriptor() *AggregateFormatDescriptorBuilder {
	return &AggregateFormatDescriptorBuilder{
		parentBuilder:       b,
		presentationFormats: [][]byte{},
	}
}

// WithDescriptorReadTimeout adds a descriptor that times out when read.
// The value parameter specifies what the mock will return after the delay.
func (b *PeripheralDeviceBuilder) WithDescriptorReadTimeout(uuid string, value ...byte) *PeripheralDeviceBuilder {
	return b.addDescriptor(DescriptorConfig{UUID: uuid, Value: value, ReadErrorBehavior: DescriptorReadTimeout})
}

// WithDescriptorReadError adds a descriptor that returns an error when read
func (b *PeripheralDeviceBuilder) WithDescriptorReadError(uuid string) *PeripheralDeviceBuilder {
	return b.addDescriptor(DescriptorConfig{UUID: uuid, ReadErrorBehavior: DescriptorReadError})
}

// FromJSON fills the device profile from JSON
func (b *PeripheralDeviceBuilder) FromJSON(jsonStrFmt string, args ...interface{}) *PeripheralDeviceBuilder {
	jsonStr := fmt.Sprintf(jsonStrFmt, args...)

	var config DeviceProfileConfig
	if err := json.Unmarshal([]byte(jsonStr), &config); err != nil {
		panic(fmt.Sprintf("PeripheralDeviceBuilder.FromJSON: failed to unmarshal: %v", err))
	}

	b.profile = config
	return b
}

// ScanAdvertisementArrayBuilder wraps AdvertisementArrayBuilder to add scan delay support.
// This allows configuring timing behavior for scan mock testing.
type ScanAdvertisementArrayBuilder struct {
	*AdvertisementArrayBuilder[*PeripheralDeviceBuilder]
	scanDelayMs int // Delay in milliseconds before emitting each advertisement
}

// WithAdvertisements wraps the embedded method to return the correct type for fluent chaining.
func (sb *ScanAdvertisementArrayBuilder) WithAdvertisements(ads ...device.Advertisement) *ScanAdvertisementArrayBuilder {
	sb.AdvertisementArrayBuilder.WithAdvertisements(ads...)
	return sb
}

// Build completes the advertisement configuration and passes scan delay to the peripheral builder.
func (sb *ScanAdvertisementArrayBuilder) Build() *PeripheralDeviceBuilder {
	parent := sb.AdvertisementArrayBuilder.Build()
	parent.scanDelayMs = sb.scanDelayMs // Transfer delay to peripheral builder
	return parent
}

// WithScanAdvertisements returns a ScanAdvertisementArrayBuilder that supports scan delay configuration
func (b *PeripheralDeviceBuilder) WithScanAdvertisements() *ScanAdvertisementArrayBuilder {
	arrayBuilder := NewAdvertisementArrayBuilder[*PeripheralDeviceBuilder]()
	arrayBuilder.parent = b

	arrayBuilder.buildFunc = func(parent *PeripheralDeviceBuilder, scanDelayMs int, ads []device.Advertisement) *PeripheralDeviceBuilder {
		// Add ble.Advertisements directly to scan advertisements
		parent.scanAdvertisements = append(parent.scanAdvertisements, ads...)
		parent.scanDelayMs = scanDelayMs
		return parent
	}

	return &ScanAdvertisementArrayBuilder{
		AdvertisementArrayBuilder: arrayBuilder,
	}
}

// remapAggregateIndices decodes placeholder handles from aggregate value and replaces them
// with actual descriptor handles. The builder uses placeholder handles (0x0100 + index) to
// represent descriptor indices, which are replaced with real ATT handles during Build().
func remapAggregateIndices(aggregateValue []byte, descriptorHandles []uint16) []byte {
	if len(aggregateValue) == 0 {
		return aggregateValue
	}

	var result []byte
	for i := 0; i < len(aggregateValue); i += 2 {
		// Decode placeholder handle (little-endian)
		placeholderHandle := uint16(aggregateValue[i]) | uint16(aggregateValue[i+1])<<8

		// Extract index from placeholder (0x0100 + index)
		index := int(placeholderHandle - 0x0100)

		// Validate index bounds
		if index < 0 || index >= len(descriptorHandles) {
			panic(fmt.Sprintf("aggregate format references invalid descriptor index %d (valid: 0-%d)", index, len(descriptorHandles)-1))
		}

		// Replace with actual handle
		actualHandle := descriptorHandles[index]
		result = append(result, byte(actualHandle), byte(actualHandle>>8))
	}
	return result
}

// parseCharacteristicProperties converts the property string to BLE.Property flags
func parseCharacteristicProperties(props string) blelib.Property {
	if props == "" {
		return blelib.CharRead | blelib.CharWrite | blelib.CharNotify // default
	}

	var property blelib.Property

	// Split by comma, trim whitespace, and parse each property individually
	parts := strings.Split(props, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch part {
		case "read":
			property |= blelib.CharRead
		case "write":
			property |= blelib.CharWrite
		case "write-without-response":
			property |= blelib.CharWriteNR
		case "notify":
			property |= blelib.CharNotify
		case "indicate":
			property |= blelib.CharIndicate
		case "authenticated_signed_writes":
			property |= blelib.CharSignedWrite
		default:
			// FAIL FAST on unknown properties to catch configuration errors
			panic(fmt.Sprintf("parseCharacteristicProperties: unknown property '%s' in '%s'", part, props))
		}
	}

	// If no valid properties were parsed, return default
	if property == 0 {
		return blelib.CharRead | blelib.CharWrite | blelib.CharNotify
	}

	return property
}

// Build creates a mocked ble.Device with the configured profile.
// Automatically registers cleanup via b.t.Cleanup() for the disconnect channel created by this call.
func (b *PeripheralDeviceBuilder) Build() blelib.Device {
	mockDevice := &blemocks.MockDevice{}
	mockClient := &blemocks.MockClient{}

	// Calculate sequential ATT handles for the entire profile
	// Handles increment: service → characteristic → descriptors
	currentHandle := uint16(0x0001)

	// Create the BLE profile with services and characteristics
	var bleServices []*blelib.Service
	for _, svcConfig := range b.profile.Services {
		bleService := &blelib.Service{
			UUID: createMockUUID(svcConfig.UUID),
		}
		currentHandle++ // Service consumes one handle

		var bleCharacteristics []*blelib.Characteristic
		for _, charConfig := range svcConfig.Characteristics {
			currentHandle++ // Characteristic consumes one handle

			// Track descriptor handles for this characteristic
			var descriptorHandles []uint16
			var bleDescriptors []*blelib.Descriptor
			for _, descConfig := range charConfig.Descriptors {
				descriptorHandles = append(descriptorHandles, currentHandle)
				bleDesc := &blelib.Descriptor{
					UUID:   createMockUUID(descConfig.UUID),
					Handle: currentHandle,
				}
				// Only populate Value for normal descriptors (not timeout/error).
				// For timeout/error descriptors, leave Value empty to force ReadDescriptor call.
				if descConfig.ReadErrorBehavior != DescriptorReadTimeout && descConfig.ReadErrorBehavior != DescriptorReadError {
					bleDesc.Value = descConfig.Value
				}
				bleDescriptors = append(bleDescriptors, bleDesc)
				currentHandle++ // Descriptor consumes one handle
			}

			// Fix up Aggregate Format descriptors: replace placeholder indices with actual handles
			aggregateUUID := createMockUUID("2905")
			for i, desc := range bleDescriptors {
				if desc.UUID.String() == aggregateUUID.String() { // 0x2905 Aggregate Format
					bleDescriptors[i].Value = remapAggregateIndices(desc.Value, descriptorHandles)
				}
			}

			// Determine property flags: use 0 if NoProperties flag is set, otherwise parse Properties string
			var property blelib.Property
			if charConfig.NoProperties {
				property = 0 // No properties (macOS hidden until paired)
			} else {
				property = parseCharacteristicProperties(charConfig.Properties)
			}

			bleChar := &blelib.Characteristic{
				UUID:        createMockUUID(charConfig.UUID),
				Property:    property,
				Value:       charConfig.Value,
				Descriptors: bleDescriptors,
			}
			bleCharacteristics = append(bleCharacteristics, bleChar)
		}
		bleService.Characteristics = bleCharacteristics
		bleServices = append(bleServices, bleService)
	}

	// Create the profile that will be returned by DiscoverProfile
	mockProfile := &blelib.Profile{
		Services: bleServices,
	}

	// Set up mock expectations
	mockDevice.On("Dial", mock.Anything, mock.Anything).Return(mockClient, nil)
	mockClient.On("DiscoverProfile", true).Return(mockProfile, nil)
	mockClient.On("CancelConnection").Return(nil)

	// Set up disconnect channel expectation for graceful disconnect handling.
	// Each Build() creates a new disconnect channel to support the monitoring goroutine
	// in connection.go (lines 300-316), which detects CoreBluetooth disconnections.
	// Store the channel in the builder for test access via GetDisconnectChannel().
	// Register cleanup to close the channel and prevent goroutine leak.
	b.disconnectChan = make(chan struct{})
	b.t.Cleanup(func() {
		// Close channel if not already closed (idempotent cleanup)
		select {
		case <-b.disconnectChan:
			// Channel already closed
		default:
			close(b.disconnectChan)
		}
	})
	mockClient.On("Disconnected").Return((<-chan struct{})(b.disconnectChan))

	// Set up subscription expectations for all characteristics
	for svcIdx, svc := range bleServices {
		svcConfig := b.profile.Services[svcIdx]
		for charIdx, char := range svc.Characteristics {
			charConfig := svcConfig.Characteristics[charIdx]

			mockClient.On("Subscribe", char, false, mock.Anything).Return(nil)
			mockClient.On("Subscribe", char, true, mock.Anything).Return(nil) // Indicate mode
			mockClient.On("Unsubscribe", char, false).Return(nil)
			mockClient.On("Unsubscribe", char, true).Return(nil)

			// Add read expectations - return value only if characteristic supports reading
			if char.Property&blelib.CharRead != 0 {
				if charConfig.ReadDelay > 0 {
					// Add delay for timeout testing
					mockClient.On("ReadCharacteristic", char).Run(func(args mock.Arguments) {
						time.Sleep(charConfig.ReadDelay)
					}).Return(char.Value, nil)
				} else {
					mockClient.On("ReadCharacteristic", char).Return(char.Value, nil)
				}
			} else {
				mockClient.On("ReadCharacteristic", char).Return(nil, fmt.Errorf("characteristic does not support read"))
			}

			// Add write expectations - accept writes if characteristic supports writing
			if char.Property&blelib.CharWrite != 0 || char.Property&blelib.CharWriteNR != 0 {
				if charConfig.WriteDelay > 0 {
					// Add delay for timeout testing
					mockClient.On("WriteCharacteristic", char, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
						time.Sleep(charConfig.WriteDelay)
					}).Return(nil)
				} else {
					mockClient.On("WriteCharacteristic", char, mock.Anything, mock.Anything).Return(nil)
				}
			} else {
				mockClient.On("WriteCharacteristic", char, mock.Anything, mock.Anything).Return(fmt.Errorf("characteristic does not support write"))
			}

			// Add descriptor read expectations based on ReadErrorBehavior
			for descIdx, desc := range char.Descriptors {
				descConfig := charConfig.Descriptors[descIdx]
				switch descConfig.ReadErrorBehavior {
				case DescriptorReadTimeout:
					// Timeout: simulate slow BLE operation that takes 10 seconds.
					// Caller should timeout before this completes.
					// After delay, return the configured value (goroutine writes to buffered channel and exits cleanly).
					mockClient.On("ReadDescriptor", desc).Run(func(args mock.Arguments) {
						time.Sleep(10 * time.Second)
					}).Return(desc.Value, nil)
				case DescriptorReadError:
					// Error: return an error
					mockClient.On("ReadDescriptor", desc).Return(nil, fmt.Errorf("permission denied"))
				default:
					// Normal: return the value
					mockClient.On("ReadDescriptor", desc).Return(desc.Value, nil)
				}
			}
		}
	}

	// Set up scan expectations using configured advertisements
	mockDevice.On("Scan", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		ctx := args.Get(0).(context.Context)
		handler := args.Get(2).(blelib.AdvHandler)

		if len(b.scanAdvertisements) > 0 {
			b.t.Logf("Mock scan: emitting %d advertisement(s) with %dms delay", len(b.scanAdvertisements), b.scanDelayMs)
		}

		// Simulate discovering all configured advertisements
		for i, devAdv := range b.scanAdvertisements {
			// Apply scan delay if configured, respecting context cancellation
			if b.scanDelayMs > 0 {
				b.t.Logf("Mock scan: waiting %dms before emitting advertisement %d/%d (addr=%s)",
					b.scanDelayMs, i+1, len(b.scanAdvertisements), devAdv.Addr())
				select {
				case <-time.After(time.Duration(b.scanDelayMs) * time.Millisecond):
					// Delay completed, proceed to emit advertisement
				case <-ctx.Done():
					b.t.Logf("Mock scan: context cancelled during delay, stopping scan")
					return
				}
			}

			// Check context before emitting an advertisement
			select {
			case <-ctx.Done():
				b.t.Logf("Mock scan: context cancelled, stopping scan")
				return
			default:
			}

			// Create an inline adapter that wraps the device.Advertisement as ble.Advertisement
			mockAddr := &blemocks.MockAddr{}
			mockAddr.On("String").Return(devAdv.Addr())

			// Create ble.Advertisement adapter
			adapter := &blemocks.MockAdvertisement{}
			adapter.On("LocalName").Return(devAdv.LocalName())
			adapter.On("ManufacturerData").Return(devAdv.ManufacturerData())
			adapter.On("TxPowerLevel").Return(devAdv.TxPowerLevel())
			adapter.On("Connectable").Return(devAdv.Connectable())
			adapter.On("RSSI").Return(devAdv.RSSI())
			adapter.On("Addr").Return(mockAddr)

			// Convert ServiceData
			deviceServiceData := devAdv.ServiceData()
			bleServiceData := make([]blelib.ServiceData, len(deviceServiceData))
			for i, sd := range deviceServiceData {
				bleServiceData[i] = blelib.ServiceData{
					UUID: blelib.MustParse(sd.UUID),
					Data: sd.Data,
				}
			}
			adapter.On("ServiceData").Return(bleServiceData)

			// Convert Services
			deviceServices := devAdv.Services()
			bleServices := make([]blelib.UUID, len(deviceServices))
			for i, svc := range deviceServices {
				bleServices[i] = blelib.MustParse(svc)
			}
			adapter.On("Services").Return(bleServices)

			// Convert OverflowService and SolicitedService
			deviceOverflow := devAdv.OverflowService()
			bleOverflow := make([]blelib.UUID, len(deviceOverflow))
			for i, svc := range deviceOverflow {
				bleOverflow[i] = blelib.MustParse(svc)
			}
			adapter.On("OverflowService").Return(bleOverflow)

			deviceSolicited := devAdv.SolicitedService()
			bleSolicited := make([]blelib.UUID, len(deviceSolicited))
			for i, svc := range deviceSolicited {
				bleSolicited[i] = blelib.MustParse(svc)
			}
			adapter.On("SolicitedService").Return(bleSolicited)

			b.t.Logf("Mock scan: emitting advertisement %d/%d (addr=%s, name=%s)",
				i+1, len(b.scanAdvertisements), devAdv.Addr(), devAdv.LocalName())
			handler(adapter)
		}

		// If blocking mode is enabled (scanDelayMs == -1), block until context is canceled
		// This simulates real-world BLE scanning where the scan continues indefinitely
		if b.scanDelayMs == -1 {
			b.t.Logf("Mock scan: blocking mode enabled, waiting for context cancellation")
			<-ctx.Done()
			b.t.Logf("Mock scan: context cancelled in blocking mode, stopping scan")
		}
	}).Return(func(ctx context.Context, _ bool, _ blelib.AdvHandler) error {
		// Return raw context errors like the actual ble.Device does
		// The bleScanner wrapper (go-ble/scanner.go) will normalize them
		return ctx.Err()
	})

	// OnConnected hook for automatic verification of proper connection cleanup.
	//
	// WHAT: For every BLE connection created during device_test, automatically verify that
	// Disconnect() was called and cleanup completed properly - even if the test forgot.
	//
	// HOW: OnConnected is called by BLEConnection.Connect() for every connection.
	// We register t.Cleanup(), which runs AFTER the test completes (including TearDown).
	// If Disconnect() was called → DisconnectVerified=true → cleanup is a no-op.
	// If Disconnect() was NOT called → we run verification and panic on errors.
	//
	// SAFETY: Detect if another test overwrites OnConnected (would indicate parallel
	// test execution, which corrupts cleanup registration. Fail loudly if detected.
	t := b.t
	testName := t.Name()
	goble.OnConnected = func(c *goble.BLEConnection) {
		// Register cleanup verification - handles parallel detection and t.Cleanup() registration
		goble.RegisterTestConnectionCleanup(testName, t, c)
	}

	return mockDevice
}

// GetServices returns the configured services for use in creating connection options
func (b *PeripheralDeviceBuilder) GetServices() []ServiceConfig {
	return b.profile.Services
}

// GetProfile returns the device profile configuration
func (b *PeripheralDeviceBuilder) GetProfile() DeviceProfileConfig {
	return b.profile
}

// GetDisconnectChannel returns the disconnect channel created by Build().
// This channel can be closed by device_test to simulate a graceful disconnect from CoreBluetooth.
// Returns nil if Build() has not been called yet.
func (b *PeripheralDeviceBuilder) GetDisconnectChannel() chan struct{} {
	return b.disconnectChan
}
