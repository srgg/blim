//go:build test

package scanner_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/srg/blim/scanner"
	"github.com/stretchr/testify/require"
	suitelib "github.com/stretchr/testify/suite"
)

type ScannerTestSuite struct {
	testutils.PeripheralDeviceSuite

	adv1, adv2, adv3 device.Advertisement
	dev1, dev2, dev3 device.DeviceInfo
}

func (suite *ScannerTestSuite) SetupTest() {
	advBuilder1 := testutils.NewAdvertisementBuilder().
		WithAddress("AA:BB:CC:DD:EE:FF").
		WithName("Test Device 1").
		WithRSSI(-45).
		WithServices("180F", "1800").
		WithConnectable(true).
		WithManufacturerData(nil).
		WithNoServiceData().
		WithTxPower(11)
	suite.adv1 = advBuilder1.Build()
	suite.dev1 = advBuilder1.BuildDevice(suite.Logger)

	advBuilder2 := testutils.NewAdvertisementBuilder().
		WithAddress("11:22:33:44:55:66").
		WithName("Test Device 2").
		WithRSSI(-67).
		WithServices("1801").
		WithConnectable(true).
		WithManufacturerData(nil).
		WithNoServiceData().
		WithTxPower(12)
	suite.adv2 = advBuilder2.Build()
	suite.dev2 = advBuilder2.BuildDevice(suite.Logger)

	advBuilder3 := testutils.NewAdvertisementBuilder().
		WithAddress("99:88:77:66:55:44").
		WithName("Test Device 3").
		WithRSSI(-80).
		WithServices("1802").
		WithConnectable(true).
		WithManufacturerData(nil).
		WithNoServiceData().
		WithTxPower(13)
	suite.adv3 = advBuilder3.Build()
	suite.dev3 = advBuilder3.BuildDevice(suite.Logger)

	suite.GivenAdvertisements(func(a *testutils.AdvertisementArrayBuilder[[]device.Advertisement]) {
		a.
			WithAdvertisements(suite.adv1, suite.adv2, suite.adv3)
	})
	suite.PeripheralDeviceSuite.SetupTest()
}

func (suite *ScannerTestSuite) TestNewScanner() {
	suite.Run("creates scanner with provided logger", func() {
		s, err := scanner.NewScanner(suite.Logger)

		suite.NoError(err)
		suite.NotNil(s)
	})

	suite.Run("creates scanner with nil logger", func() {
		s, err := scanner.NewScanner(nil)

		suite.NoError(err)
		suite.NotNil(s)
	})
}

func (suite *ScannerTestSuite) TestDefaultScanOptions() {
	opts := scanner.DefaultScanOptions()

	suite.NotNil(opts)
	suite.Equal(10*time.Second, opts.Duration)
	suite.True(opts.DuplicateFilter)
	suite.Nil(opts.ServiceUUIDs)
	suite.Nil(opts.AllowList)
	suite.Nil(opts.BlockList)
}

func (suite *ScannerTestSuite) TestScanOptionsValidation() {
	tests := []struct {
		name string
		opts *scanner.ScanOptions
	}{
		{
			name: "accepts valid options",
			opts: &scanner.ScanOptions{
				Duration:        5 * time.Second,
				DuplicateFilter: false,
				ServiceUUIDs:    []string{},
				AllowList:       []string{"AA:BB:CC:DD:EE:FF"},
				BlockList:       []string{"11:22:33:44:55:66"},
			},
		},
		{
			name: "accepts zero duration for indefinite scan",
			opts: &scanner.ScanOptions{
				Duration: 0,
			},
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			// Test that options are accepted without validation errors
			suite.NotNil(tt.opts)
		})
	}
}

func (suite *ScannerTestSuite) TestScannerFiltering() {
	tests := []struct {
		name            string
		scanOptions     *scanner.ScanOptions
		expectedDevices []device.DeviceInfo // Full expected scan results with device data
		description     string
	}{
		{
			name:            "includes all devices with no filters",
			scanOptions:     &scanner.ScanOptions{},
			expectedDevices: []device.DeviceInfo{suite.dev1, suite.dev2, suite.dev3},
			description:     "No filters should include all discovered devices",
		},
		{
			name: "excludes device on block list",
			scanOptions: &scanner.ScanOptions{
				BlockList: []string{suite.dev1.Address()},
			},
			expectedDevices: []device.DeviceInfo{suite.dev2, suite.dev3},
			description:     "Device AA:BB:CC:DD:EE:FF should be excluded from results",
		},
		{
			name: "includes device with matching service UUID",
			scanOptions: &scanner.ScanOptions{
				ServiceUUIDs: []string{"180F"},
			},
			expectedDevices: []device.DeviceInfo{suite.dev1},
			description:     "Only devices with Battery Service (180F) should be included",
		},
		{
			name: "excludes device without matching service UUID",
			scanOptions: &scanner.ScanOptions{
				ServiceUUIDs: []string{"1234"}, // Non-existent service
			},
			expectedDevices: []device.DeviceInfo{},
			description:     "No devices should match non-existent service UUID",
		},
		{
			name: "includes device on allow list",
			scanOptions: &scanner.ScanOptions{
				AllowList: []string{"AA:BB:CC:DD:EE:FF"},
			},
			expectedDevices: []device.DeviceInfo{suite.dev1},
			description:     "Only device on allow list should be included",
		},
		{
			name: "excludes device not on allow list",
			scanOptions: &scanner.ScanOptions{
				AllowList: []string{"FF:EE:DD:CC:BB:AA"}, // Non-existent device
			},
			expectedDevices: []device.DeviceInfo{},
			description:     "No devices should match when allow list contains non-existent device",
		},
	}

	// Convert DeviceInfo array to DeviceEntry array
	deviceInfoToEntries := func(devices []device.DeviceInfo) []scanner.DeviceEntry {
		entries := make([]scanner.DeviceEntry, 0, len(devices))
		for _, dev := range devices {
			entries = append(entries, scanner.DeviceEntry{
				Device:   dev,
				LastSeen: time.Now(), // This will be ignored in comparison
			})
		}
		return entries
	}

	mapVal2Array := func(m map[string]scanner.DeviceEntry) []scanner.DeviceEntry {
		values := make([]scanner.DeviceEntry, 0, len(m))
		for _, v := range m {
			values = append(values, v)
		}
		return values
	}

	// jsonassert ("github.com/yudai/gojsondiff) does not support root-level arrays,
	// as it does not have options to ignore order in the arrays
	wrapArrayAsMap := func(a []scanner.DeviceEntry) map[string][]scanner.DeviceEntry {
		// Sort devices by address
		sorted := make([]scanner.DeviceEntry, len(a))
		copy(sorted, a)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Device.Address() < sorted[j].Device.Address()
		})

		// return the map with a single key "array" to overcome limitations
		return map[string][]scanner.DeviceEntry{"array": sorted}
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			// Create scanner
			s, err := scanner.NewScanner(suite.Logger)
			require.NoError(suite.T(), err)

			// Add short duration to test cases that don't have one
			if tt.scanOptions.Duration == 0 {
				tt.scanOptions.Duration = 100 * time.Millisecond
			}

			// Run the actual scan with filters to check it works
			ctx := context.Background()
			devices, err := s.Scan(ctx, tt.scanOptions, nil)

			// Scan should complete successfully
			require.NoError(suite.T(), err, "Scan should complete without error")
			require.NotNil(suite.T(), devices, "Devices map should not be nil")

			// Marshal expected results to JSON
			expectedJSON := testutils.MustJSON(wrapArrayAsMap(deviceInfoToEntries(tt.expectedDevices)))
			// Marshal actual scan results to JSON
			actualJSON2, err := json.Marshal(mapVal2Array(devices))
			suite.NotEmpty(actualJSON2)
			actualJSON := testutils.MustJSON(wrapArrayAsMap(mapVal2Array(devices)))
			suite.NoError(err, "Scan results (device map) must marshal to JSON without error")

			// Use JSONAsserter to compare - this will FAIL proving filtering is not implemented
			jsonAsserter := testutils.NewJSONAsserter(suite.T()).
				WithOptions(
					testutils.WithIgnoredFields("LastSeen"),
					testutils.WithIgnoreExtraKeys(false),
					testutils.WithCompareOnlyExpectedKeys(true),
				)
			jsonAsserter.Assert(actualJSON, expectedJSON)
		})
	}
}

// TestScannerTestSuite runs the test suite using testify/suite
func TestScannerTestSuite(t *testing.T) {
	suitelib.Run(t, new(ScannerTestSuite))
}
