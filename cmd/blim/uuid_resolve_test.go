//go:build test

package main

import (
	"testing"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// UUIDResolveTestSuite device_test UUID resolution functions with mock BLE peripheral
type UUIDResolveTestSuite struct {
	CommandTestSuite
}

// SetupSuite runs once before all device_test in the suite
func (suite *UUIDResolveTestSuite) SetupSuite() {
	suite.CommandTestSuite.SetupSuite()
}

// SetupTest runs before each test in the suite
func (suite *UUIDResolveTestSuite) SetupTest() {
	// Configure peripheral with characteristics and descriptors for resolution testing
	// Uses AmbiguousCharPeripheral which has:
	// - 2a37 in BOTH 180d and 1800 (ambiguity detection)
	// - 2902 (CCCD) in multiple characteristics (descriptor ambiguity)
	// - 2901 only in 2a19 (unique descriptor resolution)
	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			FromJSON(testutils.AmbiguousCharPeripheral)
	})

	suite.CommandTestSuite.SetupTest()
}

// =============================================================================
// doResolveTarget Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestDoResolveTarget() {
	// GOAL: Verify doResolveTarget handles various resolution scenarios correctly
	//
	// TEST SCENARIO: Resolve UUID with various inputs → appropriate success/error → correct return values

	tests := []struct {
		name            string
		targetUUID      string
		serviceUUID     string
		charUUID        string
		descUUID        string
		expectError     bool
		errorContains   []string // multiple substrings to check
		expectChar      bool
		expectDesc      bool
		svcUUIDContains string
	}{
		{
			name:          "characteristic not found",
			targetUUID:    "ffff",
			expectError:   true,
			errorContains: []string{"not found"},
		},
		{
			name:          "ambiguous characteristic",
			targetUUID:    "2a37",
			expectError:   true,
			errorContains: []string{"multiple services", "--service"},
		},
		{
			name:            "ambiguous resolved with explicit service",
			targetUUID:      "2a37",
			serviceUUID:     "180d",
			expectChar:      true,
			svcUUIDContains: "180d",
		},
		{
			name:          "char not in explicit service",
			targetUUID:    "2a19",
			serviceUUID:   "180d",
			expectError:   true,
			errorContains: []string{"not found in service"},
		},
		{
			name:            "unique characteristic",
			targetUUID:      "2a19",
			expectChar:      true,
			svcUUIDContains: "180f",
		},
		// Descriptor resolution cases
		{
			name:          "descriptor not found",
			targetUUID:    "ffff",
			descUUID:      "ffff",
			expectError:   true,
			errorContains: []string{"descriptor", "not found"},
		},
		{
			name:          "ambiguous descriptor",
			targetUUID:    "2902",
			descUUID:      "2902",
			expectError:   true,
			errorContains: []string{"multiple characteristics", "--service", "--char"},
		},
		{
			name:            "descriptor via explicit service and char",
			targetUUID:      "2902",
			serviceUUID:     "180d",
			charUUID:        "2a37",
			descUUID:        "2902",
			expectChar:      true,
			expectDesc:      true,
			svcUUIDContains: "180d",
		},
		{
			name:            "unique descriptor auto-resolve",
			targetUUID:      "2901",
			descUUID:        "2901",
			expectChar:      true,
			expectDesc:      true,
			svcUUIDContains: "180f",
		},
	}

	dev := suite.Connect(TestDeviceAddress2)
	conn := dev.GetConnection()

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			char, desc, svcUUID, err := doResolveTarget(conn, tt.targetUUID, tt.serviceUUID, tt.charUUID, tt.descUUID)

			if tt.expectError {
				suite.Assert().Error(err, "MUST fail")
				for _, substr := range tt.errorContains {
					suite.Assert().Contains(err.Error(), substr, "error MUST indicate cause")
				}
				suite.Assert().Nil(char, "characteristic MUST be nil on error")
				suite.Assert().Nil(desc, "descriptor MUST be nil on error")
				suite.Assert().Empty(svcUUID, "serviceUUID MUST be empty on error")
			} else {
				suite.Assert().NoError(err, "MUST succeed")
				if tt.expectChar {
					suite.Assert().NotNil(char, "characteristic MUST be returned")
				}
				if tt.expectDesc {
					suite.Assert().NotNil(desc, "descriptor MUST be returned")
				} else {
					suite.Assert().Nil(desc, "descriptor MUST be nil when not requested")
				}
				if tt.svcUUIDContains != "" {
					suite.Assert().Contains(svcUUID, tt.svcUUIDContains, "serviceUUID MUST match")
				}
			}
		})
	}
}

// =============================================================================
// resolveCharacteristics Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestResolveCharacteristics() {
	// GOAL: Verify resolveCharacteristics handles all resolution cases correctly
	//
	// TEST SCENARIO: Various CSV + service combinations → appropriate success/error

	tests := []struct {
		name          string
		charUUIDsCSV  string
		serviceUUID   string
		expectError   bool
		errorContains []string
		expectCount   int
	}{
		{
			name:          "ambiguous char without service",
			charUUIDsCSV:  "2a19,2a37",
			serviceUUID:   "",
			expectError:   true,
			errorContains: []string{"multiple services", "--service"},
		},
		{
			name:          "char not in specified service",
			charUUIDsCSV:  "2a19",
			serviceUUID:   "180d",
			expectError:   true,
			errorContains: []string{"not found in service"},
		},
		{
			name:          "one char not in specified service",
			charUUIDsCSV:  "2a37,2a19",
			serviceUUID:   "180d",
			expectError:   true,
			errorContains: []string{"2a19", "not found in service"},
		},
		{
			name:          "service not found",
			charUUIDsCSV:  "",
			serviceUUID:   "ffff",
			expectError:   true,
			errorContains: []string{"not found"},
		},
		{
			name:          "no targets specified",
			charUUIDsCSV:  "",
			serviceUUID:   "",
			expectError:   true,
			errorContains: []string{"no UUIDs provided"},
		},
		{
			name:         "all chars in service",
			charUUIDsCSV: "",
			serviceUUID:  "180d",
			expectCount:  2,
		},
		{
			name:         "specific chars in service",
			charUUIDsCSV: "2a37,2a38",
			serviceUUID:  "180d",
			expectCount:  2,
		},
		{
			name:         "auto-resolve unique chars",
			charUUIDsCSV: "2a38,2a19",
			serviceUUID:  "",
			expectCount:  2,
		},
	}

	dev := suite.Connect(TestDeviceAddress2)
	conn := dev.GetConnection()

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			_, totalChars, chars, err := resolveCharacteristics(conn, tt.charUUIDsCSV, tt.serviceUUID)

			if tt.expectError {
				suite.Assert().Error(err, "MUST fail")
				for _, substr := range tt.errorContains {
					suite.Assert().Contains(err.Error(), substr, "error MUST contain: "+substr)
				}
				suite.Assert().Nil(chars, "chars MUST be nil on error")
			} else {
				suite.Require().NoError(err, "MUST succeed")
				suite.Assert().Equal(tt.expectCount, totalChars, "total count MUST match")
				suite.Assert().Len(chars, tt.expectCount, "chars map size MUST match")
			}
		})
	}
}

// =============================================================================
// resolveDescriptor Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestResolveDescriptor() {
	// GOAL: Verify resolveDescriptor handles descriptor resolution correctly
	//
	// TEST SCENARIO: Resolve descriptor UUID with various inputs → appropriate success/error

	tests := []struct {
		name           string
		descUUID       string
		serviceUUID    string
		charUUID       string
		expectError    bool
		errorContains  []string
		expectCharUUID string
		expectSvcUUID  string
	}{
		{
			name:          "descriptor not found",
			descUUID:      "ffff",
			expectError:   true,
			errorContains: []string{"descriptor", "not found"},
		},
		{
			name:          "ambiguous descriptor without service",
			descUUID:      "2902",
			expectError:   true,
			errorContains: []string{"multiple characteristics", "--service", "--char"},
		},
		{
			name:           "ambiguous resolved with explicit service and char",
			descUUID:       "2902",
			serviceUUID:    "180d",
			charUUID:       "2a37",
			expectCharUUID: "2a37",
			expectSvcUUID:  "180d",
		},
		{
			name:          "descriptor not in explicit char",
			descUUID:      "2901",
			serviceUUID:   "180d",
			charUUID:      "2a37",
			expectError:   true,
			errorContains: []string{"descriptor", "not found"},
		},
		{
			name:           "unique descriptor auto-resolve",
			descUUID:       "2901",
			expectCharUUID: "2a19",
			expectSvcUUID:  "180f",
		},
	}

	dev := suite.Connect(TestDeviceAddress2)
	conn := dev.GetConnection()

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			char, desc, svcUUID, err := resolveDescriptor(conn, tt.descUUID, tt.serviceUUID, tt.charUUID)

			if tt.expectError {
				suite.Assert().Error(err, "MUST fail")
				for _, substr := range tt.errorContains {
					suite.Assert().Contains(err.Error(), substr, "error MUST contain: "+substr)
				}
				suite.Assert().Nil(char, "characteristic MUST be nil on error")
				suite.Assert().Nil(desc, "descriptor MUST be nil on error")
			} else {
				suite.Require().NoError(err, "MUST succeed")
				suite.Assert().NotNil(char, "characteristic MUST be returned")
				suite.Assert().NotNil(desc, "descriptor MUST be returned")
				suite.Assert().Contains(char.UUID(), tt.expectCharUUID, "parent characteristic UUID MUST match")
				suite.Assert().Contains(desc.UUID(), tt.descUUID, "descriptor UUID MUST match")
				suite.Assert().Contains(svcUUID, tt.expectSvcUUID, "service UUID MUST match")
			}
		})
	}
}

// =============================================================================
// ParseCSVUUIDs Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestParseCSVUUIDs() {
	// GOAL: Verify comma-separated UUID parsing handles various input formats
	//
	// TEST SCENARIO: Parse various formats → correct UUIDs extracted → whitespace handled

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "single UUID", input: "2a37", expected: []string{"2a37"}},
		{name: "two UUIDs", input: "2a37,2a38", expected: []string{"2a37", "2a38"}},
		{name: "three UUIDs", input: "2a6e,2a6f,2a19", expected: []string{"2a6e", "2a6f", "2a19"}},
		{name: "UUIDs with spaces", input: "2a37, 2a38, 2a19", expected: []string{"2a37", "2a38", "2a19"}},
		{name: "UUIDs with extra spaces", input: "  2a37 ,  2a38  ", expected: []string{"2a37", "2a38"}},
		{name: "empty elements filtered", input: "2a37,,2a38", expected: []string{"2a37", "2a38"}},
		{name: "mixed case preserved", input: "2A37,2a38,2A6E", expected: []string{"2A37", "2a38", "2A6E"}},
		{name: "empty input", input: "", expected: nil},
		{name: "only commas", input: ",,,", expected: nil},
		{name: "only spaces", input: "   ", expected: nil},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			result := parseCSVUUIDs(tt.input)
			suite.Assert().Equal(tt.expected, result, "parsed UUIDs MUST match expected")
		})
	}
}

// =============================================================================
// ShortenUUID Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestShortenUUID() {
	// GOAL: Verify ShortenUUID truncates long UUIDs for display
	//
	// TEST SCENARIO: Truncate various UUIDs → first 8 chars for long, full for short

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "short UUID", input: "2a19", expected: "2a19"},
		{name: "8 char UUID", input: "12345678", expected: "12345678"},
		{name: "long UUID", input: "12345678-1234-5678-1234-567812345678", expected: "12345678"},
		{name: "empty", input: "", expected: ""},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			result := device.ShortenUUID(tt.input)
			suite.Assert().Equal(tt.expected, result, "shortUUID MUST match expected")
		})
	}
}

// =============================================================================
// UUID Normalization Tests
// =============================================================================

func (suite *UUIDResolveTestSuite) TestNormalizeUUID() {
	// GOAL: Verify UUID normalization extracts short form and lowercases
	//
	// TEST SCENARIO: Normalize various UUID formats → short lowercase form returned

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "short UUID lowercase", input: "2a19", expected: "2a19"},
		{name: "short UUID uppercase", input: "2A19", expected: "2a19"},
		{name: "full UUID extracts short", input: "0000180f-0000-1000-8000-00805f9b34fb", expected: "180f"},
		{name: "mixed case short UUID", input: "180F", expected: "180f"},
		{name: "mixed case full UUID extracts short", input: "0000180F-0000-1000-8000-00805F9B34FB", expected: "180f"},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			normalized := device.NormalizeUUID(tt.input)
			suite.Assert().Equal(tt.expected, normalized, "normalized UUID MUST be short lowercase")
		})
	}
}

// TestUUIDResolveSuite runs the test suite
func TestUUIDResolveSuite(t *testing.T) {
	suite.Run(t, new(UUIDResolveTestSuite))
}
