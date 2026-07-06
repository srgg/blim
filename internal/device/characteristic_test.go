//go:build test

//go:generate go run github.com/srgg/testify/depend/cmd/dependgen

package device_test

import (
	"testing"
	"time"

	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/testutils"
	"github.com/srgg/testify/depend"
)

// CharacteristicTestSuite tests characteristic Read/Write operations using MockBLEPeripheralSuite.
type CharacteristicTestSuite struct {
	DeviceTestSuite2
}

func (suite *CharacteristicTestSuite) TestCharacteristicRead() {
	// GOAL: Verify characteristic read operations work correctly
	//
	// TEST SCENARIO: Various read scenarios → correct data returned → proper error handling

	suite.Run("success with data", func() {
		// GOAL: Verify characteristic read returns data successfully
		//
		// TEST SCENARIO: Read characteristic with data → data returned → no error

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find characteristic")

		data, err := char.Read(5 * time.Second)

		suite.Assert().NoError(err, "MUST read successfully")
		suite.Assert().Equal([]byte{85}, data, "data MUST match expected value")
	})

	suite.Run("empty data", func() {
		// GOAL: Verify read returns empty data correctly
		//
		// TEST SCENARIO: Read characteristic with empty value → empty array returned → no error

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a20")
		suite.Require().NoError(err, "MUST find characteristic")

		data, err := char.Read(5 * time.Second)

		suite.Assert().NoError(err, "MUST read successfully")
		suite.Assert().Empty(data, "data MUST be empty")
		suite.Assert().NotNil(data, "data MUST not be nil")
	})

	suite.Run("multiple sequential reads", func() {
		// GOAL: Verify multiple sequential reads return the same data
		//
		// TEST SCENARIO: Read twice → both return the same data → no errors

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find characteristic")

		data1, err1 := char.Read(5 * time.Second)
		data2, err2 := char.Read(5 * time.Second)

		suite.Assert().NoError(err1, "first read MUST succeed")
		suite.Assert().NoError(err2, "second read MUST succeed")
		suite.Assert().Equal([]byte{85}, data1, "first read data MUST match")
		suite.Assert().Equal([]byte{85}, data2, "second read data MUST match")
		suite.Assert().Equal(data1, data2, "both reads MUST return same data")
	})

	suite.Run("read from write-only characteristic", func() {
		// GOAL: Verify read from write-only characteristic returns ErrUnsupported error
		//
		// TEST SCENARIO: Read from write-only characteristic → error returned → error wraps device.ErrUnsupported

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		_, err = char.Read(5 * time.Second)

		suite.Assert().Error(err, "read MUST fail on write-only characteristic")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "characteristic 2a39", "error message MUST contain characteristic UUID")
		suite.Assert().Contains(err.Error(), "does not support read operations", "error message MUST describe the unsupported operation")
	})

	suite.Run("read while not connected returns ErrNotConnected", func() {
		// GOAL: Verify ErrNotConnected is returned when reading from disconnected device
		//
		// TEST SCENARIO: Get characteristic → disconnect device → attempt read → ErrNotConnected returned

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find characteristic")

		// Disconnect the device
		err = dev.Disconnect()
		suite.Require().NoError(err, "disconnect MUST succeed")

		// Attempt to read while disconnected
		_, err = char.Read(5 * time.Second)

		suite.Assert().Error(err, "read MUST fail when not connected")
		suite.Assert().ErrorIs(err, device.ErrNotConnected, "error MUST be ErrNotConnected")
		suite.Assert().Contains(err.Error(), "2a19", "error message MUST contain characteristic UUID")
	})

	suite.Run("read timeout returns ErrTimeout", func() {
		// GOAL: Verify ErrTimeout is returned when read operation times out
		//
		// TEST SCENARIO: Read characteristic with 1s delay using 500ms timeout → ErrTimeout returned
		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a41")
		suite.Require().NoError(err, "MUST find characteristic")

		// Attempt to read with timeout shorter than the mock delay (1s)
		_, err = char.Read(500 * time.Millisecond)

		suite.Assert().Error(err, "read MUST fail on timeout")
		suite.Assert().ErrorIs(err, device.ErrTimeout, "error MUST wrap device.ErrTimeout")
		suite.Assert().Contains(err.Error(), "2a41", "error message MUST contain characteristic UUID")
		suite.Assert().Contains(err.Error(), "500ms", "error message MUST contain timeout duration")
	})
}

func (suite *CharacteristicTestSuite) TestCharacteristicWrite() {
	// GOAL: Verify characteristic write operations work correctly
	//
	// TEST SCENARIO: Various write scenarios → operations succeed → proper error handling

	suite.Run("success with response", func() {
		// GOAL: Verify characteristic write with response succeeds
		//
		// TEST SCENARIO: Write data with response → operation succeeds → no error

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		err = char.Write([]byte{0x01, 0x02, 0x03}, true, 5*time.Second)

		suite.Assert().NoError(err, "MUST write successfully with response")
	})

	suite.Run("without response", func() {
		// GOAL: Verify write without response succeeds
		//
		// TEST SCENARIO: Write data without response → operation succeeds → no error

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		err = char.Write([]byte{0xFF, 0xFE}, false, 5*time.Second)

		suite.Assert().NoError(err, "MUST write successfully without response")
	})

	suite.Run("empty data rejected", func() {
		// GOAL: Verify writing empty data is rejected (CoreBluetooth crashes with NSInternalInconsistencyException)
		//
		// TEST SCENARIO: Write an empty array → operation fails → error mentions "empty"

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		err = char.Write([]byte{}, true, 5*time.Second)

		suite.Assert().Error(err, "MUST reject empty data")
		suite.Assert().Contains(err.Error(), "empty", "error MUST mention empty data")
	})

	suite.Run("large data", func() {
		// GOAL: Verify large data writes are handled correctly
		//
		// TEST SCENARIO: Write 512 bytes → operation succeeds → no error

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		largeData := make([]byte, 512)
		for i := range largeData {
			largeData[i] = byte(i % 256)
		}

		err = char.Write(largeData, true, 10*time.Second)

		suite.Assert().NoError(err, "MUST write large data successfully")
	})

	suite.Run("multiple sequential writes", func() {
		// GOAL: Verify multiple sequential writes succeed
		//
		// TEST SCENARIO: Write three times sequentially → all succeed → no errors

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		err1 := char.Write([]byte{0x01}, true, 5*time.Second)
		err2 := char.Write([]byte{0x02}, true, 5*time.Second)
		err3 := char.Write([]byte{0x03}, true, 5*time.Second)

		suite.Assert().NoError(err1, "first write MUST succeed")
		suite.Assert().NoError(err2, "second write MUST succeed")
		suite.Assert().NoError(err3, "third write MUST succeed")
	})

	suite.Run("write to read-only characteristic", func() {
		// GOAL: Verify write to read-only characteristic returns ErrUnsupported error
		//
		// TEST SCENARIO: Write to read-only characteristic → error returned → error wraps device.ErrUnsupported

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find characteristic")

		err = char.Write([]byte{0x01}, true, 5*time.Second)

		suite.Assert().Error(err, "write MUST fail on read-only characteristic")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "characteristic 2a19", "error message MUST contain characteristic UUID")
		suite.Assert().Contains(err.Error(), "does not support write operations", "error message MUST describe the unsupported operation")
	})

	suite.Run("write-without-response when write is unavailable", func() {
		// GOAL: Verify write operation succeeds using write-without-response when write-with-response is unavailable
		//
		// TEST SCENARIO: Write with response requested → only write-without-response available → operation succeeds

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find characteristic")

		// Request write with response, but characteristic only supports write-without-response
		// Should succeed by automatically using write-without-response
		err = char.Write([]byte{0x01, 0x02, 0x03}, true, 5*time.Second)

		suite.Assert().NoError(err, "write MUST succeed using write-without-response when write is unavailable")
	})

	suite.Run("write while not connected returns ErrNotConnected", func() {
		// GOAL: Verify ErrNotConnected is returned when writing to disconnected device
		//
		// TEST SCENARIO: Get characteristic → disconnect device → attempt write → ErrNotConnected returned
		dev := suite.Connect("12")

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

	suite.Run("write timeout returns ErrTimeout", func() {
		// GOAL: Verify ErrTimeout is returned when write operation times out
		//
		// TEST SCENARIO: Write characteristic with 1s delay using 500ms timeout → ErrTimeout returned
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "2a42")
		suite.Require().NoError(err, "MUST find characteristic")

		// Attempt to write with timeout shorter than the mock delay (1s)
		err = char.Write([]byte{0x01}, true, 500*time.Millisecond)

		suite.Assert().Error(err, "write MUST fail on timeout")
		suite.Assert().ErrorIs(err, device.ErrTimeout, "error MUST wrap device.ErrTimeout")
		suite.Assert().Contains(err.Error(), "2a42", "error message MUST contain characteristic UUID")
		suite.Assert().Contains(err.Error(), "500ms", "error message MUST contain timeout duration")
	})
}

func (suite *CharacteristicTestSuite) TestCharacteristicReadWrite() {
	// GOAL: Verify read and write operations work together
	//
	// TEST SCENARIO: Combined read/write scenarios → both operations succeed → proper coordination

	suite.Run("integration", func() {
		// GOAL: Verify read and write operations work together
		//
		// TEST SCENARIO: Read initial value then write → both succeed → no errors

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "2a40")
		suite.Require().NoError(err, "MUST find characteristic")

		initialData, readErr := char.Read(5 * time.Second)
		writeErr := char.Write([]byte{0xFF}, true, 5*time.Second)

		suite.Assert().NoError(readErr, "read MUST succeed")
		suite.Assert().NoError(writeErr, "write MUST succeed")
		suite.Assert().Equal([]byte{0x00}, initialData, "initial data MUST match")
	})
}

func (suite *CharacteristicTestSuite) TestCharacteristicKnownName() {
	// GOAL: Verify characteristic KnownName() returns correct names populated by go-ble from bledb
	//
	// TEST SCENARIO: Get various characteristics → check KnownName() → verify go-ble populated names correctly

	suite.Run("Battery Level characteristic (2a19)", func() {
		// GOAL: Verify Battery Level characteristic has correct known name
		//
		// TEST SCENARIO: Get characteristic 2a19 → check KnownName() → returns "Battery Level"

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find characteristic")

		knownName := char.KnownName()
		suite.Assert().Equal("Battery Level", knownName, "go-ble MUST populate KnownName with 'Battery Level' for UUID 2a19")
	})

	suite.Run("Heart Rate Measurement characteristic (2a37)", func() {
		// GOAL: Verify Heart Rate Measurement characteristic has correct known name
		//
		// TEST SCENARIO: Get characteristic 2a37 → check KnownName() → returns "Heart Rate Measurement"
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "2a37")
		suite.Require().NoError(err, "MUST find characteristic")

		knownName := char.KnownName()
		suite.Assert().Equal("Heart Rate Measurement", knownName, "go-ble MUST populate KnownName from bledb for standard characteristics")
	})

	suite.Run("unknown characteristic has empty known name", func() {
		// GOAL: Verify unknown/custom characteristic UUIDs return empty known name
		//
		// TEST SCENARIO: Get characteristic with unknown UUID (ffff) → check KnownName() → returns empty string
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "ffff")
		suite.Require().NoError(err, "MUST find characteristic")

		knownName := char.KnownName()
		suite.Assert().Empty(knownName, "go-ble MUST return empty string for unknown characteristic UUIDs")
	})
}

func (suite *CharacteristicTestSuite) TestCharacteristicParserAPI() {
	// GOAL: Verify parser API infrastructure (routing, registration, idempotency)
	//
	// TEST SCENARIO: Test API layer → verify HasParser() routing → verify ParseValue() routing → verify graceful degradation

	suite.Run("HasParser returns true for registered parsers", func() {
		// GOAL: Verify HasParser() returns true for characteristics with registered parsers
		//
		// TEST SCENARIO: Get Appearance characteristic (has parser) → HasParser() returns true

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
		suite.Require().NoError(err, "MUST find Appearance characteristic")

		hasParser := char.HasParser()
		suite.Assert().True(hasParser, "HasParser() MUST return true for registered parser (Appearance)")
	})

	suite.Run("HasParser returns false for unregistered parsers", func() {
		// GOAL: Verify HasParser() returns false for characteristics without registered parsers
		//
		// TEST SCENARIO: Get Battery Level characteristic (no parser) → HasParser() returns false

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find Battery Level characteristic")

		hasParser := char.HasParser()
		suite.Assert().False(hasParser, "HasParser() MUST return false when no parser registered")
	})

	suite.Run("HasParser returns false for custom/unknown UUIDs", func() {
		// GOAL: Verify HasParser() returns false for custom characteristics
		//
		// TEST SCENARIO: Get custom characteristic → HasParser() returns false

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180d", "ffff")
		suite.Require().NoError(err, "MUST find custom characteristic")

		hasParser := char.HasParser()
		suite.Assert().False(hasParser, "HasParser() MUST return false for custom/unknown UUIDs")
	})

	suite.Run("ParseValue routes to registered parser correctly", func() {
		// GOAL: Verify ParseValue() routes to correct parser implementation
		//
		// TEST SCENARIO: Get characteristic with parser → call ParseValue() → parser is invoked and returns result
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
		suite.Require().NoError(err, "MUST find Appearance characteristic")

		value, err := char.Read(5 * time.Second)
		suite.Require().NoError(err, "read MUST succeed")

		parsed, err := char.ParseValue(value)
		suite.Assert().NoError(err, "ParseValue() MUST succeed when parser exists")
		suite.Assert().NotNil(parsed, "parsed value MUST not be nil for valid data")
	})

	suite.Run("ParseValue returns (nil, nil) for unregistered parsers", func() {
		// GOAL: Verify ParseValue() gracefully handles characteristics without parsers
		//
		// TEST SCENARIO: Get characteristic without parser → call ParseValue() → returns (nil, nil)

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find Battery Level characteristic")

		value, err := char.Read(5 * time.Second)
		suite.Require().NoError(err, "read MUST succeed")

		parsed, err := char.ParseValue(value)
		suite.Assert().NoError(err, "ParseValue() MUST not error when no parser registered (graceful degradation)")
		suite.Assert().Nil(parsed, "ParseValue() MUST return nil when no parser registered")
	})

	suite.Run("HasParser is idempotent", func() {
		// GOAL: Verify HasParser() returns consistent results across multiple calls
		//
		// TEST SCENARIO: Call HasParser() multiple times → all return same value
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
		suite.Require().NoError(err, "MUST find Appearance characteristic")

		result1 := char.HasParser()
		result2 := char.HasParser()
		result3 := char.HasParser()

		suite.Assert().True(result1, "first call MUST return true")
		suite.Assert().Equal(result1, result2, "results MUST be consistent")
		suite.Assert().Equal(result2, result3, "results MUST be consistent")
	})

	suite.Run("ParseValue is idempotent", func() {
		// GOAL: Verify ParseValue() returns consistent results across multiple calls
		//
		// TEST SCENARIO: Call ParseValue() multiple times with same data → all return same result
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
		suite.Require().NoError(err, "MUST find Appearance characteristic")

		value, err := char.Read(5 * time.Second)
		suite.Require().NoError(err, "read MUST succeed")

		parsed1, err1 := char.ParseValue(value)
		parsed2, err2 := char.ParseValue(value)
		parsed3, err3 := char.ParseValue(value)

		suite.Assert().NoError(err1, "first parse MUST succeed")
		suite.Assert().NoError(err2, "second parse MUST succeed")
		suite.Assert().NoError(err3, "third parse MUST succeed")
		suite.Assert().Equal(parsed1, parsed2, "parsed values MUST be consistent")
		suite.Assert().Equal(parsed2, parsed3, "parsed values MUST be consistent")
	})
}

func (suite *CharacteristicTestSuite) TestRequiresAuthentication() {
	// GOAL: Verify RequiresAuthentication() detects pairing requirements using CoreBluetooth heuristics
	//
	// TEST SCENARIO: Various property configurations → correct authentication detection → macOS hidden properties handled

	suite.Run("explicit AuthenticatedSignedWrites property requires authentication", func() {
		// GOAL: Verify RequiresAuthentication() detects explicit AuthenticatedSignedWrites property
		//
		// TEST SCENARIO: Characteristic with authenticated_signed_writes property → RequiresAuthentication() returns true

		suite.GivenPeripheral(func(b *testutils.PeripheralDeviceBuilder) {
			b.
				WithService("1234").
				WithCharacteristic("5678", "authenticated_signed_writes", []byte{0x01})
		})

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("1234", "5678")
		suite.Require().NoError(err, "MUST find characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().True(requiresAuth, "RequiresAuthentication() MUST return true for AuthenticatedSignedWrites property")
	})

	suite.Run("no properties exposed indicates macOS hidden due to encryption requirement", func() {
		// GOAL: Verify RequiresAuthentication() detects macOS/iOS hidden properties pattern
		//
		// TEST SCENARIO: Characteristic with no properties (macOS hides until paired) → RequiresAuthentication() returns true
		//
		// macOS/iOS CoreBluetooth behavior: When a peripheral requires encryption/pairing,
		// the OS hides characteristic properties (returns 0) until pairing completes.
		// This is a common indicator that authentication is required.

		suite.GivenPeripheral(func(b *testutils.PeripheralDeviceBuilder) {
			b.
				WithService("1234").
				WithCharacteristicNoProperties("ABCD", []byte{0x01})
		})

		dev := suite.Connect("12")
		char, err := dev.GetConnection().GetCharacteristic("1234", "ABCD")
		suite.Require().NoError(err, "MUST find characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().True(requiresAuth, "RequiresAuthentication() MUST return true when properties hidden (macOS pairing required)")
	})

	suite.Run("normal read characteristic does not require authentication", func() {
		// GOAL: Verify RequiresAuthentication() returns false for standard readable characteristics
		//
		// TEST SCENARIO: Characteristic with read property → RequiresAuthentication() returns false
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")
		suite.Require().NoError(err, "MUST find Battery Level characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().False(requiresAuth, "RequiresAuthentication() MUST return false for standard read characteristic")
	})

	suite.Run("read/write characteristic does not require authentication", func() {
		// GOAL: Verify RequiresAuthentication() returns false for read/write characteristics
		//
		// TEST SCENARIO: Characteristic with read,write properties → RequiresAuthentication() returns false
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "2a40")
		suite.Require().NoError(err, "MUST find read/write characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().False(requiresAuth, "RequiresAuthentication() MUST return false for read/write characteristic")
	})

	suite.Run("notify characteristic does not require authentication", func() {
		// GOAL: Verify RequiresAuthentication() returns false for notification characteristics
		//
		// TEST SCENARIO: Characteristic with notify property → RequiresAuthentication() returns false
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "2a37")
		suite.Require().NoError(err, "MUST find Heart Rate Measurement characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().False(requiresAuth, "RequiresAuthentication() MUST return false for notify characteristic")
	})

	suite.Run("write-only characteristic does not require authentication", func() {
		// GOAL: Verify RequiresAuthentication() returns false for write-only characteristics
		//
		// TEST SCENARIO: Characteristic with write property → RequiresAuthentication() returns false
		dev := suite.Connect("12")

		char, err := dev.GetConnection().GetCharacteristic("180d", "2a39")
		suite.Require().NoError(err, "MUST find write-only characteristic")

		requiresAuth := char.RequiresAuthentication()
		suite.Assert().False(requiresAuth, "RequiresAuthentication() MUST return false for write-only characteristic")
	})
}

// @dependsOn TestCharacteristicParserAPI
func (suite *CharacteristicTestSuite) TestWellKnownCharacteristicParsers() {
	// GOAL: Verify each well-known parser implementation correctness
	//
	// TEST SCENARIO: Test specific parsers → verify value parsing → verify validation → verify error handling

	suite.Run("Appearance (0x2A01)", func() {
		suite.Run("known value 0x0040 returns Phone", func() {
			// GOAL: Verify Appearance parser correctly parses known appearance codes
			//
			// TEST SCENARIO: Read Appearance characteristic with value 0x0040 → ParseValue() returns "Phone"
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			value, err := char.Read(5 * time.Second)
			suite.Require().NoError(err, "read MUST succeed")

			parsed, err := char.ParseValue(value)
			suite.Assert().NoError(err, "ParseValue() MUST succeed for known Appearance value")
			suite.Assert().Equal("Phone", parsed, "ParseValue() MUST return 'Phone' for 0x0040")
		})

		suite.Run("unknown value 0xFFFF returns nil gracefully", func() {
			// GOAL: Verify Appearance parser gracefully handles unknown appearance codes
			//
			// TEST SCENARIO: Read Appearance with unknown value 0xFFFF → ParseValue() returns (nil, nil)

			// Create peripheral with unknown appearance value
			suite.GivenPeripheral(func(b *testutils.PeripheralDeviceBuilder) {
				b.
					WithService("1800").
					WithCharacteristic("2a01", "read", []byte{0xFF, 0xFF})
			})
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			value, err := char.Read(5 * time.Second)
			suite.Require().NoError(err, "read MUST succeed")

			parsed, err := char.ParseValue(value)
			suite.Assert().NoError(err, "ParseValue() MUST succeed for unknown value")
			suite.Assert().Nil(parsed, "ParseValue() MUST return nil for unknown Appearance code")
		})

		suite.Run("malformed data - 1 byte returns error", func() {
			// GOAL: Verify Appearance parser validates data length (2 bytes required)
			//
			// TEST SCENARIO: Call ParseValue() with 1 byte → returns error
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			parsed, err := char.ParseValue([]byte{0x40})
			suite.Assert().Error(err, "ParseValue() MUST return error for malformed data")
			suite.Assert().Nil(parsed, "ParseValue() MUST return nil when error occurs")
			suite.Assert().Contains(err.Error(), "appearance value must be 2 bytes", "error message MUST describe validation failure")
		})

		suite.Run("malformed data - empty slice returns error", func() {
			// GOAL: Verify Appearance parser rejects empty data
			//
			// TEST SCENARIO: Call ParseValue() with empty slice → returns error
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			parsed, err := char.ParseValue([]byte{})
			suite.Assert().Error(err, "ParseValue() MUST return error for empty data")
			suite.Assert().Nil(parsed, "ParseValue() MUST return nil when error occurs")
			suite.Assert().Contains(err.Error(), "appearance value must be 2 bytes", "error message MUST describe validation failure")
		})

		suite.Run("malformed data - nil returns error", func() {
			// GOAL: Verify Appearance parser rejects nil data
			//
			// TEST SCENARIO: Call ParseValue() with nil → returns error
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			parsed, err := char.ParseValue(nil)
			suite.Assert().Error(err, "ParseValue() MUST return error for nil data")
			suite.Assert().Nil(parsed, "ParseValue() MUST return nil when error occurs")
			suite.Assert().Contains(err.Error(), "appearance value must be 2 bytes", "error message MUST describe validation failure")
		})

		suite.Run("malformed data - 3 bytes returns error", func() {
			// GOAL: Verify Appearance parser validates exact length requirement
			//
			// TEST SCENARIO: Call ParseValue() with 3 bytes → returns error
			dev := suite.Connect("12")

			char, err := dev.GetConnection().GetCharacteristic("1800", "2a01")
			suite.Require().NoError(err, "MUST find Appearance characteristic")

			parsed, err := char.ParseValue([]byte{0x40, 0x00, 0xFF})
			suite.Assert().Error(err, "ParseValue() MUST return error for data with wrong length")
			suite.Assert().Nil(parsed, "ParseValue() MUST return nil when error occurs")
			suite.Assert().Contains(err.Error(), "appearance value must be 2 bytes", "error message MUST describe validation failure")
		})
	})
}

// TestCharacteristicTestSuite runs the test suite
func TestCharacteristicTestSuite(t *testing.T) {
	depend.RunSuite(t, new(CharacteristicTestSuite))
}
