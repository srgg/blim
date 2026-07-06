//go:build test

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/srgg/blim/inspector"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// InspectTestSuite device_test the inspect command functionality
type InspectTestSuite struct {
	CommandTestSuite
	originalFlags struct {
		connectTimeout            time.Duration
		descriptorReadTimeout     time.Duration
		preScanTimeout            time.Duration
		characteristicReadTimeout time.Duration
		json                      bool
	}
}

// SetupSuite saves original flags before all device_test
func (suite *InspectTestSuite) SetupSuite() {
	// Call parent SetupSuite first to initialize Logger and other fields
	suite.CommandTestSuite.SetupSuite()

	// Then save our flags
	suite.originalFlags.connectTimeout = inspectConnectTimeout
	suite.originalFlags.descriptorReadTimeout = inspectDescriptorReadTimeout
	suite.originalFlags.preScanTimeout = inspectPreScanTimeout
	suite.originalFlags.characteristicReadTimeout = inspectCharacteristicReadTimeout
	suite.originalFlags.json = inspectJSON
}

// TearDownSuite restores original flags after all device_test
func (suite *InspectTestSuite) TearDownSuite() {
	inspectConnectTimeout = suite.originalFlags.connectTimeout
	inspectDescriptorReadTimeout = suite.originalFlags.descriptorReadTimeout
	inspectPreScanTimeout = suite.originalFlags.preScanTimeout
	inspectCharacteristicReadTimeout = suite.originalFlags.characteristicReadTimeout
	inspectJSON = suite.originalFlags.json
}

// SetupTest initializes each test with a mock peripheral
func (suite *InspectTestSuite) SetupTest() {
	address := "AA:BB:CC:DD:EE:FF"

	// Set up scan to return advertisement with default values
	adv := testutils.NewAdvertisementBuilder().
		WithAddress(address).
		WithName("TestDevice").
		WithRSSI(-50).
		WithConnectable(true).
		WithManufacturerData([]byte{0x01, 0x02}).
		WithNoServiceData().
		WithServices().
		WithTxPower(0).
		Build()

	// Create a peripheral with multiple services and characteristics for testing
	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			WithScanAdvertisements().
			WithAdvertisements(adv).
			Build().
			FromJSON(`{
			"services": [
				{
					"uuid": "180a",
					"characteristics": [
						{"uuid": "2a29", "properties": "read", "value": [84, 101, 115, 116]},
						{"uuid": "2a24", "properties": "read", "value": [77, 111, 100, 101, 108]}
					]
				},
				{
					"uuid": "180d",
					"characteristics": [
						{"uuid": "2a37", "properties": "read,notify", "value": [0, 90]},
						{"uuid": "2a38", "properties": "read", "value": [1]}
					]
				}
			]
		}`)
	})

	// Call parent to apply mock configuration
	suite.CommandTestSuite.SetupTest()

	// Reset flags to defaults
	inspectConnectTimeout = defaultConnectTimeout
	inspectDescriptorReadTimeout = defaultDescriptorReadTimeout
	inspectPreScanTimeout = defaultPreScanTimeout
	inspectCharacteristicReadTimeout = defaultCharacteristicReadTimeout
	inspectJSON = false

	// Reset command flags
	inspectCmd.ResetFlags()
	inspectCmd.Flags().DurationVar(&inspectConnectTimeout, "connect-timeout", defaultConnectTimeout, "Connection timeout")
	inspectCmd.Flags().DurationVar(&inspectDescriptorReadTimeout, "descriptor-timeout", defaultDescriptorReadTimeout, "Timeout for reading descriptor values (default: 2s if unset, 0 to skip descriptor reads)")
	inspectCmd.Flags().DurationVar(&inspectPreScanTimeout, "pre-scan-timeout", defaultPreScanTimeout, "Pre-scan timeout to capture advertisement data (0 to skip)")
	inspectCmd.Flags().DurationVar(&inspectCharacteristicReadTimeout, "characteristic-read-timeout", defaultCharacteristicReadTimeout, "Timeout for reading characteristic values")
	inspectCmd.Flags().BoolVar(&inspectJSON, "json", false, "Output as JSON")
}

// Helper methods

// createTestContext creates a context with a timeout for device_test
func (suite *InspectTestSuite) createTestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// createInspectOptions creates default InspectOptions for device_test
func (suite *InspectTestSuite) createInspectOptions() *inspector.InspectOptions {
	return &inspector.InspectOptions{
		ConnectTimeout:            inspectConnectTimeout,
		DescriptorReadTimeout:     inspectDescriptorReadTimeout,
		CharacteristicReadTimeout: inspectCharacteristicReadTimeout,
	}
}

// Test groups

func (suite *InspectTestSuite) TestPreScanForAdvertisement() {
	// Local helper: create test advertisement with default values
	createTestAd := func(address string) device.Advertisement {
		return testutils.NewAdvertisementBuilder().
			WithAddress(address).
			WithName("TestDevice").
			WithRSSI(-50).
			WithConnectable(true).
			WithManufacturerData([]byte{0x01, 0x02}).
			WithNoServiceData().
			WithServices().
			WithTxPower(0).
			Build()
	}

	// Local helper: assert timing with tolerance
	assertTiming := func(duration, expected time.Duration, label string) {
		tolerance := 50 * time.Millisecond
		suite.Assert().GreaterOrEqual(duration, expected,
			"%s duration MUST be at least %v", label, expected)
		suite.Assert().LessOrEqual(duration, expected+tolerance,
			"%s duration MUST NOT exceed %v by more than %v", label, expected, tolerance)
	}

	// Local helper: create JSON asserter with common options
	createJSONAsserter := func() *testutils.JSONAsserter {
		return testutils.NewJSONAsserter(suite.T()).WithOptions(
			testutils.WithCompareOnlyExpectedKeys(true),
		)
	}

	suite.Run("Success", func() {
		// GOAL: Verify pre-scan successfully finds a device and captures advertisement
		//
		// TEST SCENARIO: Pre-scan with valid address → device found → advertisement returned

		ctx, cancel := suite.createTestContext(5 * time.Second)
		defer cancel()

		address := "AA:BB:CC:DD:EE:FF"

		// Create timeout context for pre-scan
		scanCtx, scanCancel := context.WithTimeout(ctx, 3*time.Second)
		defer scanCancel()

		adv, err := preScanForAdvertisement(scanCtx, address, suite.Logger)

		suite.Assert().NoError(err, "MUST complete pre-scan without error")
		suite.Assert().NotNil(adv, "MUST return advertisement data")

		// Convert advertisement to JSON for verification
		advJSON := testutils.AdvertisementToJSON(adv)

		// Verify advertisement fields match SetupTest configuration
		createJSONAsserter().Assert(advJSON, `{
			"address": "AA:BB:CC:DD:EE:FF",
			"name": "TestDevice",
			"rssi": -50,
			"connectable": true
		}`)
	})

	suite.Run("Timeout", func() {
		// GOAL: Verify pre-scan returns DeadlineExceeded when device not found within timeout
		//
		// TEST SCENARIO: Configure scan with 200ms delay, use 100ms timeout → timeout occurs → DeadlineExceeded error

		ctx, cancel := suite.createTestContext(5 * time.Second)
		defer cancel()

		address := "AA:BB:CC:DD:EE:FF"
		scanTimeout := 100 * time.Millisecond
		scanDelay := 200 // 200ms delay, longer than 100ms timeout

		// Configure peripheral with scan delay that exceeds timeout
		adv := createTestAd(address)

		// Create a fresh PeripheralBuilder to avoid accumulating advertisements from SetupTest
		suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
			builder.WithScanAdvertisements().
				WithScanDelay(scanDelay). // Delay longer than timeout
				WithAdvertisements(adv).Build()
		})

		// Search for a DIFFERENT address so mock never finds a match and waits full timeout
		nonExistentAddress := "00:00:00:00:00:00"

		// Create timeout context for pre-scan
		scanCtx, scanCancel := context.WithTimeout(ctx, scanTimeout)
		defer scanCancel()

		// Measure scan duration to verify timeout is respected
		startTime := time.Now()
		resultAdv, err := preScanForAdvertisement(scanCtx, nonExistentAddress, suite.Logger)
		duration := time.Since(startTime)

		// Should return ErrTimeout error when the device not found
		suite.Assert().ErrorIs(err, device.ErrTimeout, "MUST return device.ErrTimeout when device not found")
		suite.Assert().Nil(resultAdv, "MUST return nil advertisement on timeout")

		// Verify timeout was respected
		assertTiming(duration, scanTimeout, "scan")
	})

	suite.Run("Canceled", func() {
		// GOAL: Verify pre-scan returns Canceled when parent context is canceled
		//
		// TEST SCENARIO: Cancel context before pre-scan → scan returns context.Canceled error

		// Create a fresh peripheral builder with no delay
		address := "AA:BB:CC:DD:EE:FF"
		adv := createTestAd(address)

		suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
			builder.WithScanAdvertisements().
				WithAdvertisements(adv)
		})

		ctx, cancel := context.WithCancel(context.Background())

		// Cancel immediately
		cancel()

		resultAdv, err := preScanForAdvertisement(ctx, address, suite.Logger)

		// Should return Canceled error when context canceled
		suite.Assert().ErrorIs(err, context.Canceled, "MUST return context.Canceled when context is canceled")
		suite.Assert().Nil(resultAdv, "MUST return nil advertisement when canceled")
	})
}

func (suite *InspectTestSuite) TestInspectDevice() {
	tests := []struct {
		name           string
		preScanTimeout time.Duration
		scanDelay      int // milliseconds, 0 means no delay
		expectedJSON   string
		expectError    error
		testGoal       string
		testScenario   string
	}{
		{
			name:           "WithPreScanEnabled",
			preScanTimeout: 500 * time.Millisecond,
			scanDelay:      0,
			testGoal:       "Verify runInspect with pre-scan enabled shows advertisement-derived fields in JSON output",
			testScenario:   "Run inspect with pre-scan timeout > 0 → scan returns advertisement → JSON contains name, RSSI, manufacturer data",
			expectedJSON: `{
				"device": {
					"name": "TestDevice",
					"rssi": -50,
					"tx_power": 0,
					"manufacturer_data": "0102"
				}
			}`,
		},
		{
			name:           "WithPreScanDisabled",
			preScanTimeout: 0,
			scanDelay:      0,
			testGoal:       "Verify runInspect with pre-scan disabled shows address but NOT advertisement fields in JSON output",
			testScenario:   "Run inspect with pre-scan timeout = 0 → no advertisement captured → JSON lacks RSSI (manufacturer_data omitted when nil)",
			expectedJSON: `{
				"device": {
					"address": "AA:BB:CC:DD:EE:FF",
					"name": "AA:BB:CC:DD:EE:FF",
					"rssi": 0
				}
			}`,
		},
		{
			name:           "PreScanTimeout",
			preScanTimeout: 100 * time.Millisecond,
			scanDelay:      200, // Delay longer than timeout
			testGoal:       "Verify inspect continues when pre-scan times out",
			testScenario:   "Pre-scan times out → inspect continues without advertisement data → succeeds with default values (manufacturer_data omitted when nil)",
			expectedJSON: `{
				"device": {
					"address": "AA:BB:CC:DD:EE:FF",
					"name": "AA:BB:CC:DD:EE:FF",
					"rssi": 0
				}
			}`,
		},
		// Note: Cancellation during pre-scan is tested at the lower level in TestPreScanForAdvertisement/Canceled
		// because runInspect creates its own signal.NotifyContext which cannot be easily mocked
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {

			address := "AA:BB:CC:DD:EE:FF"

			// Configure scan delay if specified
			if tt.scanDelay > 0 {
				// Create a fresh peripheral builder with scan delay
				adv := testutils.NewAdvertisementBuilder().
					WithAddress(address).
					WithName("TestDevice").
					WithRSSI(-50).
					WithConnectable(true).
					WithManufacturerData([]byte{0x01, 0x02}).
					WithNoServiceData().
					WithServices().
					WithTxPower(0).
					Build()

				suite.GivenAdvertisements(func(a *testutils.AdvertisementArrayBuilder[[]device.Advertisement]) {
					a.WithScanDelay(tt.scanDelay).
						WithAdvertisements(adv)
				})
				suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
					builder.
						FromJSON(`{
							"services": [
								{
									"uuid": "180a",
									"characteristics": [
										{"uuid": "2a29", "properties": "read", "value": [84, 101, 115, 116]},
										{"uuid": "2a24", "properties": "read", "value": [77, 111, 100, 101, 108]}
									]
								},
								{
									"uuid": "180d",
									"characteristics": [
										{"uuid": "2a37", "properties": "read,notify", "value": [0, 90]},
										{"uuid": "2a38", "properties": "read", "value": [1]}
									]
								}
							]
						}`)
				})
			}

			// Configure flags
			inspectPreScanTimeout = tt.preScanTimeout
			inspectConnectTimeout = 5 * time.Second
			inspectJSON = true

			// Set command output buffer and run inspect command
			var buf bytes.Buffer
			inspectCmd.SetOut(&buf)
			inspectCmd.SetErr(&buf)
			err := runInspect(inspectCmd, []string{address})
			outputStr := buf.String()

			// Verify error expectation
			if tt.expectError != nil {
				suite.Assert().ErrorIs(err, tt.expectError, "MUST return expected error")
				return
			}

			suite.Assert().NoError(err, "runInspect MUST succeed")

			// Verify JSON output
			ja := testutils.NewJSONAsserter(suite.T()).WithOptions(
				testutils.WithCompareOnlyExpectedKeys(true),
			)
			ja.Assert(outputStr, tt.expectedJSON)
		})
	}
}

// TestInspectTestSuite runs the test suite
func TestInspectTestSuite(t *testing.T) {
	suite.Run(t, new(InspectTestSuite))
}
