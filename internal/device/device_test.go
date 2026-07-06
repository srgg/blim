//go:build test

//go:generate go run github.com/srgg/testify/depend/cmd/dependgen

package device_test

import (
	"testing"
	"time"

	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/devicefactory"
	"github.com/srgg/blim/internal/testutils"
	"github.com/srgg/testify/depend"
)

// DeviceBasicTestSuite tests device functionality that doesn't require an active connection.
type DeviceBasicTestSuite struct {
	DeviceTestSuite2

	ja *testutils.JSONAsserter
}

func (suite *DeviceBasicTestSuite) SetupTest() {
	suite.ja = testutils.NewJSONAsserter(suite.T())
	suite.DeviceTestSuite2.SetupTest()
}

func (suite *DeviceBasicTestSuite) TestNewDevice() {
	// GOAL: Verify device creation from advertisement data with all fields populated correctly
	//
	// TEST SCENARIO: Advertisement with complete data → device created → all fields match expected values

	suite.Run("creates device with all advertisement data", func() {
		dev := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "Test Device",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -45,
			"services": ["180F", "180A"],
			"manufacturerData": [76,0,1,2],
			"serviceData": {"180F":[100]},
			"txPower": 4,
			"connectable": true
		}`).BuildDevice(suite.Logger)

		actualJSON := testutils.DeviceToJSON(dev)

		const expectedJSON = `{
			"id": "AA:BB:CC:DD:EE:FF",
			"name": "Test Device",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -45,
			"tx_power": 4,
			"connectable": true,
			"manufacturer_data": [76,0,1,2],
			"service_data": {"180f": [100]},
			"services": [{"uuid": "180a", "characteristics": []}, {"uuid": "180f", "characteristics": []}]
		}`

		suite.ja.Assert(actualJSON, expectedJSON)
	})

	suite.Run("handles missing optional data", func() {
		dev := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": null,
			"address": "11:22:33:44:55:66",
			"rssi": -70,
			"manufacturerData": null,
			"serviceData": null,
			"services": null,
			"txPower": null,
			"connectable": false
		}`).BuildDevice(suite.Logger)

		actualJSON := testutils.DeviceToJSON(dev)
		suite.ja.Assert(actualJSON, `{
			"id": "11:22:33:44:55:66",
			"name": "11:22:33:44:55:66",
			"rssi": -70,
			"connectable": false,
			"manufacturer_data": null,
			"service_data": null,
			"services": [],
			"tx_power": null,
			"address": "11:22:33:44:55:66"
		}`)
	})
}

func (suite *DeviceBasicTestSuite) TestDeviceUpdate() {
	// GOAL: Verify device updates correctly from new advertisement data
	//
	// TEST SCENARIO: Device created → advertisement update applied → all fields reflect new values

	// Create initial device
	// Note: All BLE advertisement fields must be present because device creation
	// always calls all advertisement methods. Empty values ([], {}, null) represent
	// the default behavior when real BLE devices don't advertise that data.
	initialAdv := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "Initial Name",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -50,
			"manufacturerData": [1],
			"serviceData": {},
			"services": [],
			"txPower": 0,
			"connectable": true
		}`).Build()

	dev := devicefactory.NewDeviceFromAdvertisement(initialAdv, suite.Logger)
	initialAdv.AssertExpectations(suite.T())

	// Create update advertisement
	updateAdv := testutils.NewAdvertisementBuilder().FromJSON(`{
		"name": "Updated Name",
		"rssi": -40,
		"manufacturerData": [2, 3],
		"services": [],
		"serviceData": {"180F": [80]},
		"txPower": 8
	}`).Build()

	// Update device
	dev.Update(updateAdv)

	// Verify updates
	suite.ja.AssertDevice(dev, `{
			"id": "AA:BB:CC:DD:EE:FF",
			"name": "Updated Name",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -40,
			"manufacturer_data": [2, 3],
			"service_data": {"180f": [80]},
			"services": [],
			"tx_power": 8,
			"connectable": true
	}`)

	updateAdv.AssertExpectations(suite.T())
}

func (suite *DeviceBasicTestSuite) TestExtractNameFromManufacturerData() {
	// GOAL: Verify device name extraction from manufacturer data with various data patterns
	//
	// TEST SCENARIO: Advertisement with manufacturer data → device created → name extracted correctly based on data content

	tests := []struct {
		name         string
		manufData    []byte
		expectedName string
	}{
		{
			name:         "extracts simple ASCII device name",
			manufData:    []byte{0x4C, 0x00, 'T', 'e', 's', 't', 'D', 'e', 'v', 'i', 'c', 'e'},
			expectedName: "TestDevice",
		},
		{
			name:         "extracts name with spaces",
			manufData:    []byte{0x00, 0x01, 'M', 'y', ' ', 'D', 'e', 'v', 'i', 'c', 'e'},
			expectedName: "My Device",
		},
		{
			name:         "ignores short strings",
			manufData:    []byte{0x00, 0x01, 'A', 'B'},
			expectedName: "AA:BB:CC:DD:EE:FF",
		},
		{
			name:         "ignores data without letters",
			manufData:    []byte{0x00, 0x01, '1', '2', '3', '4', '5'},
			expectedName: "AA:BB:CC:DD:EE:FF",
		},
		{
			name:         "extracts name from middle of data",
			manufData:    []byte{0x4C, 0x00, 0x01, 0x02, 'D', 'e', 'v', 'i', 'c', 'e', 'X', 0x00},
			expectedName: "DeviceX",
		},
		{
			name:         "handles empty manufacturer data",
			manufData:    []byte{},
			expectedName: "AA:BB:CC:DD:EE:FF",
		},
		{
			name:         "handles short manufacturer data",
			manufData:    []byte{0x4C},
			expectedName: "AA:BB:CC:DD:EE:FF",
		},
		{
			name:         "extracts name from real device data",
			manufData:    []byte{0x4C, 0x00, 'Z', 'c', 'm', 0x00, 0x01, 0x02},
			expectedName: "Zcm",
		},
		{
			name:         "limits name length",
			manufData:    append([]byte{0x00, 0x01}, []byte("VeryLongDeviceNameThatShouldBeLimited1234567890")...),
			expectedName: "VeryLongDeviceNameThatShouldBeLi",
		},
		{
			name:         "ignores non-printable characters",
			manufData:    []byte{0x00, 0x01, 'T', 'e', 's', 't', 0x00, 0x01, 'D', 'e', 'v'},
			expectedName: "Test",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			adv := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": null,
				"address": "AA:BB:CC:DD:EE:FF",
				"rssi": -50,
				"manufacturerData": %s,
				"serviceData": null,
				"services": [],
				"txPower": 127,
				"connectable": true
			}`, testutils.MustJSON(tt.manufData)).Build()

			dev := devicefactory.NewDeviceFromAdvertisement(adv, suite.Logger)

			suite.Assert().Equal(tt.expectedName, dev.Name())
		})
	}
}

func (suite *DeviceBasicTestSuite) TestNameResolutionPrecedence() {
	// GOAL: Verify device name resolution follows the correct precedence order
	//
	// TEST SCENARIO: Advertisements with different name sources → device name resolved → precedence rules applied correctly

	tests := []struct {
		name         string
		localName    string
		manufData    []byte
		expectedName string
		description  string
	}{
		{
			name:         "LocalName takes precedence over manufacturer data",
			localName:    "OfficialName",
			manufData:    []byte{0x00, 0x01, 'M', 'a', 'n', 'u', 'f', 'N', 'a', 'm', 'e'},
			expectedName: "OfficialName",
			description:  "Local name should override manufacturer data name",
		},
		{
			name:         "Uses manufacturer data when no LocalName",
			localName:    "",
			manufData:    []byte{0x00, 0x01, 'E', 'x', 't', 'r', 'a', 'c', 't', 'e', 'd'},
			expectedName: "Extracted",
			description:  "Should extract from manufacturer data when no local name",
		},
		{
			name:         "Uses address when no name available",
			localName:    "",
			manufData:    []byte{0x00, 0x01, 0x02, 0x03},
			expectedName: "AA:BB:CC:DD:EE:FF",
			description:  "Should fall back to address when no name available",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			adv := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": %s,
				"address": "AA:BB:CC:DD:EE:FF",
				"rssi": -50,
				"manufacturerData": %s,
				"serviceData": null,
				"services": [],
				"txPower": 127,
				"connectable": true
			}`,
				testutils.MustJSON(tt.localName),
				testutils.MustJSON(tt.manufData),
			).Build()

			dev := devicefactory.NewDeviceFromAdvertisement(adv, suite.Logger)
			suite.Assert().Equal(tt.expectedName, dev.Name(), tt.description)
		})
	}
}

func (suite *DeviceBasicTestSuite) TestNameUpdateBehavior() {
	// GOAL: Verify device name update behavior across multiple advertisements
	//
	// TEST SCENARIO: Device created with extracted name → LocalName advertised → name updated → subsequent updates preserve LocalName

	// Create an initial device with no name
	adv1 := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": "",
				"address": "AA:BB:CC:DD:EE:FF",
				"rssi": -50,
				"manufacturerData": %s,
				"serviceData": null,
				"services": [],
				"txPower": 127,
				"connectable": true
			}`, testutils.MustJSON([]byte{0x00, 0x01, 'E', 'x', 't', 'r', 'a', 'c', 't', 'e', 'd'})).Build()

	dev := devicefactory.NewDeviceFromAdvertisement(adv1, suite.Logger)
	suite.Assert().Equal("Extracted", dev.Name(), "Should extract name from manufacturer data initially")

	// Update with advertisement that has LocalName
	adv2 := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": "OfficialName",
				"rssi": -45,
				"manufacturerData": %s,
				"serviceData": null,
				"services": [],
				"txPower": 127
			}`,
		testutils.MustJSON([]byte{0x00, 0x01, 'D', 'i', 'f', 'f', 'e', 'r', 'e', 'n', 't'})).Build()

	dev.Update(adv2)

	suite.ja.AssertDevice(dev, `{
		"name": "OfficialName",
		"rssi": -45
	}`)

	// Update with an advertisement that has no LocalName
	adv3 := testutils.NewAdvertisementBuilder().FromJSON(`{
				"name": "",
				"rssi": -40,
				"manufacturerData": %s,
				"serviceData": null,
				"services": [],
				"txPower": 127
			}`,
		testutils.MustJSON([]byte{0x00, 0x01, 'N', 'e', 'w', 'N', 'a', 'm', 'e'})).Build()

	dev.Update(adv3)
	suite.ja.AssertDevice(dev, `{
		"name": "OfficialName",
		"rssi": -40
	}`)
}

func (suite *DeviceBasicTestSuite) TestDeviceWriteToCharacteristicErrors() {
	// GOAL: Verify characteristic write returns appropriate errors for invalid operations
	//
	// TEST SCENARIO: Various error conditions → proper device errors returned → error types match expectations

	suite.Run("write while not connected returns ErrNotConnected", func() {
		// GOAL: Verify ErrNotConnected is returned when writing to a disconnected device
		//
		// TEST SCENARIO: Disconnect device → attempt characteristic write → ErrNotConnected returned

		dev := suite.Connect("12")
		// Get writable characteristic while connected
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		// Disconnect the device
		err = dev.Disconnect()
		suite.Require().NoError(err, "disconnect MUST succeed")

		// Attempt to write while disconnected
		err = char.Write([]byte{0x01}, true, 5*time.Second)

		suite.Assert().Error(err, "write MUST fail when not connected")
		suite.Assert().ErrorIs(err, device.ErrNotConnected, "error MUST be ErrNotConnected")
		suite.Assert().Contains(err.Error(), "2a39", "error message MUST contain characteristic UUID")
	})

	suite.Run("get non-existent characteristic returns NotFoundError", func() {
		// GOAL: Verify NotFoundError is returned for non-existent characteristic
		//
		// TEST SCENARIO: Get invalid characteristic UUID → NotFoundError returned → error identifies missing resource

		dev := suite.Connect("12")

		_, err := dev.GetConnection().GetCharacteristic("180d", "fffe")

		suite.Assert().Error(err, "GetCharacteristic MUST fail for non-existent characteristic")

		var notFoundErr *device.NotFoundError
		suite.Assert().ErrorAs(err, &notFoundErr, "error MUST be NotFoundError")
		suite.Assert().Equal("characteristic", notFoundErr.Resource, "resource type MUST be 'characteristic'")
		suite.Assert().Contains(notFoundErr.UUIDs, "fffe", "UUIDs MUST contain characteristic UUID")
	})
}

// Note: Appearance is now accessed as a regular characteristic (UUID 0x2A01 in GAP service 0x1800).
// Tests for appearance parsing are in CharacteristicTestSuite.TestWellKnownCharacteristicParsers.

func (suite *DeviceBasicTestSuite) TestParsedManufacturerData() {
	// GOAL: Verify device properly parses and exposes manufacturer data via DeviceInfo interface
	//
	// TEST SCENARIO: Create device from advertisement with manufacturer data → ParsedManufacturerData() returns parsed struct

	suite.Run("Blim manufacturer data", func() {
		tests := []struct {
			name               string
			manufData          []byte
			expectedDeviceType device.BlimDeviceType
			expectedHWVersion  string
			expectedFWVersion  string
		}{
			{
				name: "BLE Test Device v1.0 fw2.1.3",
				manufData: []byte{
					0xFE, 0xFF, // Company ID (0xFFFE = BLIMCo)
					0x00,             // Device Type (BLE Test Device)
					0x10,             // HW Version (1.0)
					0x02, 0x01, 0x03, // FW Version (2.1.3)
				},
				expectedDeviceType: device.BlimDeviceTypeBLETest,
				expectedHWVersion:  "1.0",
				expectedFWVersion:  "2.1.3",
			},
			{
				name: "IMU Streamer v2.3 fw1.0.0",
				manufData: []byte{
					0xFE, 0xFF, // Company ID (0xFFFE = BLIMCo)
					0x01,             // Device Type (IMU Streamer)
					0x23,             // HW Version (2.3)
					0x01, 0x00, 0x00, // FW Version (1.0.0)
				},
				expectedDeviceType: device.BlimDeviceTypeIMU,
				expectedHWVersion:  "2.3",
				expectedFWVersion:  "1.0.0",
			},
			{
				name: "Unknown device type v1.5 fw3.2.1",
				manufData: []byte{
					0xFE, 0xFF, // Company ID (0xFFFE = BLIMCo)
					0x42,             // Device Type (unknown)
					0x15,             // HW Version (1.5)
					0x03, 0x02, 0x01, // FW Version (3.2.1)
				},
				expectedDeviceType: device.BlimDeviceType(0x42),
				expectedHWVersion:  "1.5",
				expectedFWVersion:  "3.2.1",
			},
		}

		for _, tt := range tests {
			suite.Run(tt.name, func() {
				// Create a device from advertisement with Blim manufacturer data
				adv := testutils.NewAdvertisementBuilder().FromJSON(`{
					"name": "Blim Device",
					"address": "AA:BB:CC:DD:EE:FF",
					"rssi": -50,
					"manufacturerData": %s,
					"serviceData": null,
					"services": [],
					"txPower": 0,
					"connectable": true
				}`, testutils.MustJSON(tt.manufData)).Build()

				dev := devicefactory.NewDeviceFromAdvertisement(adv, suite.Logger)

				// Verify raw manufacturer data is stored
				suite.Assert().Equal(tt.manufData, dev.ManufacturerData(), "raw manufacturer data MUST match")

				// Verify parsed manufacturer data is available
				parsed := dev.ParsedManufacturerData()
				suite.Require().NotNil(parsed, "ParsedManufacturerData() MUST return parsed data for Blim devices")

				blimData, ok := parsed.(*device.BlimManufacturerData)
				suite.Require().True(ok, "MUST return *BlimManufacturerData type")

				// Verify parsed fields
				suite.Assert().Equal(tt.expectedDeviceType, blimData.DeviceType, "device type MUST match")
				suite.Assert().Equal(tt.expectedHWVersion, blimData.HardwareVersion, "hardware version MUST match")
				suite.Assert().Equal(tt.expectedFWVersion, blimData.FirmwareVersion, "firmware version MUST match")
			})
		}
	})

	suite.Run("Unknown company IDs", func() {
		tests := []struct {
			name      string
			manufData []byte
		}{
			{
				name:      "Apple company ID (0x004C)",
				manufData: []byte{0x4C, 0x00, 0x01, 0x02, 0x03},
			},
			{
				name:      "Google company ID (0x00E0)",
				manufData: []byte{0xE0, 0x00, 0x05, 0x06},
			},
			{
				name:      "Unknown company ID",
				manufData: []byte{0x12, 0x34, 0xFF, 0xFF},
			},
		}

		for _, tt := range tests {
			suite.Run(tt.name, func() {
				// Create device from advertisement with unknown manufacturer data
				adv := testutils.NewAdvertisementBuilder().FromJSON(`{
					"name": "Unknown Device",
					"address": "AA:BB:CC:DD:EE:FF",
					"rssi": -50,
					"manufacturerData": %s,
					"serviceData": null,
					"services": [],
					"txPower": 0,
					"connectable": true
				}`, testutils.MustJSON(tt.manufData)).Build()

				dev := devicefactory.NewDeviceFromAdvertisement(adv, suite.Logger)

				// Verify raw manufacturer data is stored
				suite.Assert().Equal(tt.manufData, dev.ManufacturerData(), "raw manufacturer data MUST match")

				// Verify parsed manufacturer data is nil for unknown companies
				parsed := dev.ParsedManufacturerData()
				suite.Assert().Nil(parsed, "ParsedManufacturerData() MUST return nil for unknown company IDs")
			})
		}
	})

	suite.Run("No manufacturer data", func() {
		// GOAL: Verify device without manufacturer data returns nil for parsed data
		//
		// TEST SCENARIO: Create device without manufacturer data → ParsedManufacturerData() returns nil

		adv := testutils.NewAdvertisementBuilder().FromJSON(`{
			"name": "No Manuf Data",
			"address": "AA:BB:CC:DD:EE:FF",
			"rssi": -50,
			"manufacturerData": null,
			"serviceData": null,
			"services": [],
			"txPower": 0,
			"connectable": true
		}`).Build()

		dev := devicefactory.NewDeviceFromAdvertisement(adv, suite.Logger)

		// Verify no manufacturer data
		suite.Assert().Nil(dev.ManufacturerData(), "ManufacturerData() MUST return nil")
		suite.Assert().Nil(dev.ParsedManufacturerData(), "ParsedManufacturerData() MUST return nil")
	})
}

// @dependsOn TestParsedManufacturerData
func (suite *DeviceBasicTestSuite) TestBlimDeviceTypeString() {
	// GOAL: Verify BlimDeviceType.String() returns correct human-readable names
	//
	// TEST SCENARIO: Known and unknown device types → String() called → proper names returned

	tests := []struct {
		deviceType   device.BlimDeviceType
		expectedName string
	}{
		{
			deviceType:   device.BlimDeviceTypeBLETest,
			expectedName: "BLE Test Device",
		},
		{
			deviceType:   device.BlimDeviceTypeIMU,
			expectedName: "IMU Streamer",
		},
		{
			deviceType:   device.BlimDeviceType(0x42),
			expectedName: "Unknown (0x42)",
		},
		{
			deviceType:   device.BlimDeviceType(0xFF),
			expectedName: "Unknown (0xFF)",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.expectedName, func() {
			result := tt.deviceType.String()
			suite.Assert().Equal(tt.expectedName, result, "device type name MUST match")
		})
	}
}

func TestDeviceBasicTestSuite(t *testing.T) {
	depend.RunSuite(t, new(DeviceBasicTestSuite))
}
