//go:build test

package testutils

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	blelib "github.com/go-ble/ble"
	"github.com/srg/blim/internal/device"
	devicemocks "github.com/srg/blim/internal/testutils/mocks/device"
	"github.com/stretchr/testify/suite"
)

// PeripheralDeviceBuilderTestSuite device_test PeripheralDeviceBuilder functionality
type PeripheralDeviceBuilderTestSuite struct {
	suite.Suite
}

// assertHandlesAndIndexesInDeviceProfile validates the device profile has sequential handles and correct aggregate format references
func (s *PeripheralDeviceBuilderTestSuite) assertHandlesAndIndexesInDeviceProfile(profile *blelib.Profile) {
	// Build expected JSON by assigning sequential ATT handles and generating correct aggregate format descriptor values
	services := make([]map[string]interface{}, len(profile.Services))
	currentHandle := uint16(0x0001)

	for svcIdx, svc := range profile.Services {
		currentHandle++ // Service consumes one handle
		characteristics := make([]map[string]interface{}, len(svc.Characteristics))

		for charIdx, char := range svc.Characteristics {
			currentHandle++ // Characteristic consumes one handle
			descriptors := make([]map[string]interface{}, len(char.Descriptors))

			// First pass: collect Presentation Format descriptor handles
			var formatHandles []uint16
			descriptorHandles := make([]uint16, len(char.Descriptors))
			for descIdx := range char.Descriptors {
				descriptorHandles[descIdx] = currentHandle
				if uuidContains(char.Descriptors[descIdx].UUID.String(), "2904") {
					formatHandles = append(formatHandles, currentHandle)
				}
				currentHandle++
			}

			// Second pass: build descriptor JSON with proper aggregate values
			for descIdx, desc := range char.Descriptors {
				value := desc.Value

				// Re-encode aggregate format with format descriptor handles
				if uuidContains(desc.UUID.String(), "2905") && len(value) > 0 {
					var aggregateValue []byte
					for _, handle := range formatHandles {
						aggregateValue = append(aggregateValue,
							byte(handle&0xFF),
							byte(handle>>8))
					}
					value = aggregateValue
				}

				descriptors[descIdx] = map[string]interface{}{
					"uuid":   desc.UUID.String(),
					"handle": descriptorHandles[descIdx],
					"value":  value,
				}
			}

			characteristics[charIdx] = map[string]interface{}{
				"uuid":        char.UUID.String(),
				"descriptors": descriptors,
			}
		}

		services[svcIdx] = map[string]interface{}{
			"uuid":            svc.UUID.String(),
			"characteristics": characteristics,
		}
	}

	expectedJSON := MustJSON(map[string]interface{}{"services": services})
	actualJSON := s.profileToJSON(profile)

	NewJSONAsserter(s.T()).Assert(actualJSON, expectedJSON)
}

// uuidContains checks if the UUID string contains the short form UUID
func uuidContains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					(len(s) >= 36 && s[4:8] == substr))))
}

// calculateHandles computes sequential ATT handles for descriptors in a profile
func (s *PeripheralDeviceBuilderTestSuite) calculateHandles(profile DeviceProfileConfig) [][][]uint16 {
	result := make([][][]uint16, len(profile.Services))
	currentHandle := uint16(0x0001)

	for svcIdx, svc := range profile.Services {
		result[svcIdx] = make([][]uint16, len(svc.Characteristics))
		currentHandle++ // Service consumes one handle

		for charIdx, char := range svc.Characteristics {
			result[svcIdx][charIdx] = make([]uint16, len(char.Descriptors))
			currentHandle++ // Characteristic consumes one handle

			for descIdx := range char.Descriptors {
				result[svcIdx][charIdx][descIdx] = currentHandle
				currentHandle++
			}
		}
	}
	return result
}

// buildExpectedJSON constructs expected JSON from config with calculated handles
func (s *PeripheralDeviceBuilderTestSuite) buildExpectedJSON(config DeviceProfileConfig, handles [][][]uint16) string {
	services := make([]interface{}, len(config.Services))

	for svcIdx, svc := range config.Services {
		characteristics := make([]interface{}, len(svc.Characteristics))

		for charIdx, char := range svc.Characteristics {
			descriptors := make([]interface{}, len(char.Descriptors))

			for descIdx, desc := range char.Descriptors {
				// Build descriptor value - handle aggregate format specially
				value := desc.Value
				if desc.UUID == "2905" {
					// Aggregate format: encode handles as little-endian uint16
					var aggregateValue []byte
					for i := 0; i < len(desc.Value); i += 2 {
						// Extract index from placeholder
						idx := int(desc.Value[i]) | int(desc.Value[i+1])<<8
						idx -= 0x0100

						// Encode actual handle
						actualHandle := handles[svcIdx][charIdx][idx]
						aggregateValue = append(aggregateValue,
							byte(actualHandle&0xFF),
							byte(actualHandle>>8))
					}
					value = aggregateValue
				}

				descriptors[descIdx] = map[string]interface{}{
					"uuid":   desc.UUID,
					"handle": handles[svcIdx][charIdx][descIdx],
					"value":  value,
				}
			}

			characteristics[charIdx] = map[string]interface{}{
				"uuid":        char.UUID,
				"descriptors": descriptors,
			}
		}

		services[svcIdx] = map[string]interface{}{
			"uuid":            svc.UUID,
			"characteristics": characteristics,
		}
	}

	return MustJSON(map[string]interface{}{
		"services": services,
	})
}

// getBuiltProfile extracts the BLE profile from a built device
func (s *PeripheralDeviceBuilderTestSuite) getBuiltProfile(device blelib.Device) *blelib.Profile {
	client, _ := device.Dial(nil, nil)
	profile, _ := client.DiscoverProfile(true)
	return profile
}

// profileToJSON serializes BLE profile to JSON
func (s *PeripheralDeviceBuilderTestSuite) profileToJSON(profile *blelib.Profile) string {
	type descriptorJSON struct {
		UUID   string `json:"uuid"`
		Handle uint16 `json:"handle"`
		Value  []byte `json:"value"`
	}
	type characteristicJSON struct {
		UUID        string           `json:"uuid"`
		Descriptors []descriptorJSON `json:"descriptors"`
	}
	type serviceJSON struct {
		UUID            string               `json:"uuid"`
		Characteristics []characteristicJSON `json:"characteristics"`
	}
	type profileJSON struct {
		Services []serviceJSON `json:"services"`
	}

	result := profileJSON{}
	for _, svc := range profile.Services {
		svcJSON := serviceJSON{UUID: svc.UUID.String()}
		for _, char := range svc.Characteristics {
			charJSON := characteristicJSON{UUID: char.UUID.String()}
			for _, desc := range char.Descriptors {
				descJSON := descriptorJSON{
					UUID:   desc.UUID.String(),
					Handle: desc.Handle,
					Value:  desc.Value,
				}
				charJSON.Descriptors = append(charJSON.Descriptors, descJSON)
			}
			svcJSON.Characteristics = append(svcJSON.Characteristics, charJSON)
		}
		result.Services = append(result.Services, svcJSON)
	}

	jsonBytes, _ := json.MarshalIndent(result, "", "  ")
	return string(jsonBytes)
}

func (s *PeripheralDeviceBuilderTestSuite) TestWithAggregateFormatDescriptor() {
	s.Run("MultipleFormats", func() {
		// GOAL: Verify WithAggregateFormatDescriptor adds Presentation Format descriptors and creates an Aggregate Format descriptor with correct handle references
		//
		// TEST SCENARIO: Build characteristic with 2 presentation formats via aggregate builder → JSON comparison validates handles and aggregate value

		format1 := []byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}
		format2 := []byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}

		builder := NewPeripheralDeviceBuilder(s.T()).
			WithService("1234").
			WithCharacteristic("5678", "read,notify", []byte{0x00}).
			WithAggregateFormatDescriptor().
			WithPresentationFormat(format1).
			WithPresentationFormat(format2).
			Build()

		dev := builder.Build()
		profile := s.getBuiltProfile(dev)
		s.assertHandlesAndIndexesInDeviceProfile(profile)
	})

	s.Run("EmptyAggregate", func() {
		// GOAL: Verify WithAggregateFormatDescriptor creates an empty aggregate when no formats are added
		//
		// TEST SCENARIO: Build aggregate without presentation formats → verify aggregate exists with empty value

		builder := NewPeripheralDeviceBuilder(s.T()).
			WithService("1234").
			WithCharacteristic("5678", "read,notify", []byte{0x00}).
			WithAggregateFormatDescriptor().
			Build()

		services := builder.GetServices()
		descriptors := services[0].Characteristics[0].Descriptors

		s.Assert().Len(descriptors, 1, "MUST have only aggregate descriptor")
		s.Assert().Equal("2905", descriptors[0].UUID)
		s.Assert().Len(descriptors[0].Value, 0, "aggregate value MUST be empty")
	})

	s.Run("MixedDescriptors", func() {
		// GOAL: Verify aggregate builder works alongside regular descriptors
		//
		// TEST SCENARIO: Add CCCD, then aggregate with 2 formats → JSON comparison validates all descriptors with correct handles

		cccdValue := []byte{0x01, 0x00}
		format1 := []byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}
		format2 := []byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}

		builder := NewPeripheralDeviceBuilder(s.T()).
			WithService("1234").
			WithCharacteristic("5678", "read,notify", []byte{0x00}).
			WithDescriptor("2902", cccdValue...).
			WithAggregateFormatDescriptor().
			WithPresentationFormat(format1).
			WithPresentationFormat(format2).
			Build()

		dev := builder.Build()
		profile := s.getBuiltProfile(dev)
		s.assertHandlesAndIndexesInDeviceProfile(profile)
	})

	s.Run("BuilderChaining", func() {
		// GOAL: Verify builder chaining works correctly after aggregate Build()
		//
		// TEST SCENARIO: Build aggregate, then add another characteristic → JSON comparison validates both characteristics with correct handles

		builder := NewPeripheralDeviceBuilder(s.T()).
			WithService("1234").
			WithCharacteristic("5678", "read,notify", []byte{0x00}).
			WithAggregateFormatDescriptor().
			WithPresentationFormat([]byte{0x04, 0x00, 0x01, 0x29, 0x01, 0x00, 0x00}).
			Build().
			WithCharacteristic("abcd", "read", []byte{0x01})

		dev := builder.Build()
		profile := s.getBuiltProfile(dev)
		s.assertHandlesAndIndexesInDeviceProfile(profile)
	})
}

func (s *PeripheralDeviceBuilderTestSuite) TestWithScanDelay() {
	// Shared constants
	const (
		tolerance      = 50 * time.Millisecond
		testTimeout    = 1 * time.Second
		defaultAddress = "AA:BB:CC:DD:EE:FF"
		defaultName    = "TestDevice"
	)

	// Local helper: create a fully configured advertisement
	createAd := func(address, name string) *devicemocks.MockAdvertisement {
		return NewAdvertisementBuilder().
			WithAddress(address).
			WithName(name).
			WithRSSI(-50).
			WithConnectable(true).
			WithManufacturerData([]byte{}).
			WithNoServiceData().
			WithServices().
			WithTxPower(0).
			Build()
	}

	// Local helper: build a device with scan delay
	buildDevice := func(scanDelay int, ads ...device.Advertisement) blelib.Device {
		builder := NewPeripheralDeviceBuilder(s.T())
		builder.WithScanAdvertisements().
			WithScanDelay(scanDelay).
			WithAdvertisements(ads...).
			Build()
		return builder.Build()
	}

	// Local helper: assert timing within tolerance
	assertTiming := func(duration, expected time.Duration, label string) {
		s.Assert().GreaterOrEqual(duration, expected,
			"%s duration MUST be at least %v", label, expected)
		s.Assert().LessOrEqual(duration, expected+tolerance,
			"%s duration MUST NOT exceed %v by more than %v", label, expected, tolerance)
	}

	// Local helper: run scan with timing
	runScan := func(ctx context.Context, dev blelib.Device, handler func(blelib.Advertisement)) (time.Duration, error) {
		start := time.Now()
		err := dev.Scan(ctx, false, handler)
		return time.Since(start), err
	}

	s.Run("NoDelay", func() {
		// GOAL: Verify scan without delay emits advertisements immediately
		//
		// TEST SCENARIO: Configure scan with 0ms delay → advertisements emitted with no sleep → scan completes quickly

		adv := createAd(defaultAddress, defaultName)
		dev := buildDevice(0, adv)

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		var receivedAddr string
		handler := func(a blelib.Advertisement) {
			receivedAddr = a.Addr().String()
		}

		duration, err := runScan(ctx, dev, handler)

		s.Assert().NoError(err, "scan MUST complete without error")
		s.Assert().Equal(defaultAddress, receivedAddr, "MUST receive advertisement from configured device")
		s.Assert().Less(duration, tolerance, "scan with no delay MUST complete quickly")
	})

	s.Run("WithDelay", func() {
		// GOAL: Verify scan with delay waits before emitting advertisements
		//
		// TEST SCENARIO: Configure scan with 100ms delay → wait for 100ms → advertisement emitted

		scanDelay := 100 // milliseconds
		adv := createAd(defaultAddress, defaultName)
		dev := buildDevice(scanDelay, adv)

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		var receivedAddr string
		handler := func(a blelib.Advertisement) {
			receivedAddr = a.Addr().String()
		}

		duration, err := runScan(ctx, dev, handler)

		s.Assert().NoError(err, "scan MUST complete without error")
		s.Assert().Equal(defaultAddress, receivedAddr, "MUST receive advertisement from configured device")

		assertTiming(duration, time.Duration(scanDelay)*time.Millisecond, "scan")
	})

	s.Run("Timeout", func() {
		// GOAL: Verify scan returns DeadlineExceeded when context times out
		//
		// TEST SCENARIO: Scan with 200ms delay, 100ms timeout → timeout occurs → DeadlineExceeded returned

		scanDelay := 200 // milliseconds
		timeout := 100 * time.Millisecond

		adv := createAd(defaultAddress, defaultName)
		dev := buildDevice(scanDelay, adv)

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		advReceived := false
		handler := func(a blelib.Advertisement) {
			advReceived = true
		}

		duration, err := runScan(ctx, dev, handler)

		s.Assert().ErrorIs(err, context.DeadlineExceeded, "MUST return DeadlineExceeded when context times out")
		s.Assert().False(advReceived, "MUST NOT receive advertisement when timeout occurs")

		assertTiming(duration, timeout, "scan")
	})

	s.Run("Canceled", func() {
		// GOAL: Verify scan returns Canceled when context is canceled
		//
		// TEST SCENARIO: Scan with 200ms delay, cancel after 100ms → cancellation occurs → Canceled returned

		scanDelay := 200 // milliseconds
		cancelAfter := 100 * time.Millisecond

		adv := createAd(defaultAddress, defaultName)
		dev := buildDevice(scanDelay, adv)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel after 100ms
		go func() {
			time.Sleep(cancelAfter)
			cancel()
		}()

		advReceived := false
		handler := func(a blelib.Advertisement) {
			advReceived = true
		}

		start := time.Now()
		err := dev.Scan(ctx, false, handler)
		duration := time.Since(start)

		s.Assert().ErrorIs(err, context.Canceled, "MUST return Canceled when context is canceled")
		s.Assert().False(advReceived, "MUST NOT receive advertisement when canceled")

		assertTiming(duration, cancelAfter, "scan")
	})

	s.Run("MultipleAdvertisementsWithDelay", func() {
		// GOAL: Verify scan delay applies to each advertisement individually
		//
		// TEST SCENARIO: Configure 3 advertisements with 50ms delay → each wait 50ms → total ~150ms

		scanDelay := 50 // milliseconds
		numAds := 3

		adv1 := createAd("AA:BB:CC:DD:EE:01", "Device1")
		adv2 := createAd("AA:BB:CC:DD:EE:02", "Device2")
		adv3 := createAd("AA:BB:CC:DD:EE:03", "Device3")

		dev := buildDevice(scanDelay, adv1, adv2, adv3)

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		advCount := 0
		handler := func(a blelib.Advertisement) {
			advCount++
		}

		duration, err := runScan(ctx, dev, handler)

		s.Assert().NoError(err, "scan MUST complete without error")
		s.Assert().Equal(numAds, advCount, "MUST receive all %d advertisements", numAds)

		assertTiming(duration, time.Duration(numAds*scanDelay)*time.Millisecond, "scan")
	})

	s.Run("ContextErrorPropagation", func() {
		// GOAL: Verify mock scanner Return function properly propagates ctx.Err()
		//
		// TEST SCENARIO: Scan with various context states → scanner returns matching ctx.Err() value

		adv := createAd(defaultAddress, defaultName)

		s.Run("NoError", func() {
			// Context is valid → ctx.Err() returns nil → scanner returns nil
			dev := buildDevice(0, adv)
			ctx := context.Background()

			err := dev.Scan(ctx, false, func(a blelib.Advertisement) {})

			s.Assert().NoError(err, "MUST return nil when context has no error")
		})

		s.Run("DeadlineExceeded", func() {
			// Context is timed out → ctx.Err() returns DeadlineExceeded → scanner returns DeadlineExceeded
			dev := buildDevice(0, adv)
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
			defer cancel()
			time.Sleep(10 * time.Millisecond) // Wait for timeout

			err := dev.Scan(ctx, false, func(a blelib.Advertisement) {})

			s.Assert().ErrorIs(err, context.DeadlineExceeded, "MUST return DeadlineExceeded when ctx.Err() is DeadlineExceeded")
		})

		s.Run("Canceled", func() {
			// Context is canceled → ctx.Err() returns Canceled → scanner returns Canceled
			dev := buildDevice(0, adv)
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel immediately

			err := dev.Scan(ctx, false, func(a blelib.Advertisement) {})

			s.Assert().ErrorIs(err, context.Canceled, "MUST return Canceled when ctx.Err() is Canceled")
		})
	})
}

func TestPeripheralDeviceBuilder(t *testing.T) {
	suite.Run(t, new(PeripheralDeviceBuilderTestSuite))
}
