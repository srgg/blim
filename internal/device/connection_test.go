//go:build test

package device_test

import (
	"context"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/devicefactory"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// Drain duration constants for test readability
const (
	noDrainPlease = 0
	drainFor200ms = 200 * time.Millisecond
	drainFor1sec  = 1 * time.Second
)

type ConnectionTestSuite struct {
	DeviceTestSuite2
}

func (suite *ConnectionTestSuite) TestConnectionServices() {
	// GOAL: Verify connection service discovery and retrieval work correctly
	//
	// TEST SCENARIO: Various service access patterns → services retrieved correctly → proper error handling

	suite.Run("get all services", func() {
		// GOAL: Verify Services() returns all discovered services
		//
		// TEST SCENARIO: Connect to a device with multiple services → Services() called → all services returned in sorted order

		dev := suite.Connect("22")
		services := dev.GetConnection().Services()

		suite.Assert().Len(services, 3, "MUST return all services")
		suite.Assert().Equal("1800", services[0].UUID(), "first service MUST be 1800 (Generic Access, sorted order)")
		suite.Assert().Equal("180d", services[1].UUID(), "second service MUST be 180d (Heart Rate, sorted order)")
		suite.Assert().Equal("180f", services[2].UUID(), "third service MUST be 180f (Battery, sorted order)")
	})

	suite.Run("get service by UUID", func() {
		// GOAL: Verify GetService() retrieves service by UUID
		//
		// TEST SCENARIO: Request service by UUID → service returned → UUID matches
		dev := suite.Connect("22")

		svc, err := dev.GetConnection().GetService("180f")

		suite.Assert().NoError(err, "MUST find service")
		suite.Assert().NotNil(svc, "service MUST not be nil")
		suite.Assert().Equal("180f", svc.UUID(), "service UUID MUST match")
	})

	suite.Run("fail when service not found", func() {
		// GOAL: Verify GetService() returns NotFoundError for non-existent service
		//
		// TEST SCENARIO: Request non-existent service → NotFoundError returned → error message describes issue
		dev := suite.Connect("22")
		svc, err := dev.GetConnection().GetService("ffff")

		suite.Assert().Error(err, "MUST return error for non-existent service")
		suite.Assert().Nil(svc, "service MUST be nil")

		var notFoundErr *device.NotFoundError
		suite.Assert().ErrorAs(err, &notFoundErr, "error MUST be NotFoundError")
		suite.Assert().Equal("service", notFoundErr.Resource, "resource type MUST be 'service'")
		suite.Assert().Equal([]string{"ffff"}, notFoundErr.UUIDs, "UUIDs MUST contain service UUID")
		suite.Assert().Equal("service \"ffff\" not found", err.Error(), "error message MUST match expected format")
	})

	suite.Run("UUID normalization", func() {
		// GOAL: Verify UUID normalization works for service lookup
		//
		// TEST SCENARIO: Request service with various UUID formats → service found → consistent behavior
		dev := suite.Connect("22")
		conn := dev.GetConnection()

		// Test various UUID formats
		svc1, err1 := conn.GetService("180f")
		svc2, err2 := conn.GetService("180F")
		svc3, err3 := conn.GetService("0000180f-0000-1000-8000-00805f9b34fb")

		suite.Assert().NoError(err1, "lowercase UUID MUST work")
		suite.Assert().NoError(err2, "uppercase UUID MUST work")
		suite.Assert().NoError(err3, "full UUID MUST work")
		suite.Assert().Equal(svc1.UUID(), svc2.UUID(), "UUIDs MUST match")
		suite.Assert().Equal(svc1.UUID(), svc3.UUID(), "UUIDs MUST match")
	})
}

func (suite *ConnectionTestSuite) TestConnectionCharacteristics() {
	// GOAL: Verify that connection characteristic discovery and retrieval work correctly
	//
	// TEST SCENARIO: Various characteristic access patterns → characteristics retrieved correctly → proper error handling

	suite.Run("get characteristic by service and UUID", func() {
		// GOAL: Verify GetCharacteristic() retrieves characteristic
		//
		// TEST SCENARIO: Request characteristic by service and UUID → characteristic returned → UUIDs match
		dev := suite.Connect("22")

		char, err := dev.GetConnection().GetCharacteristic("180f", "2a19")

		suite.Assert().NoError(err, "MUST find characteristic")
		suite.Assert().NotNil(char, "characteristic MUST not be nil")
		suite.Assert().Equal("2a19", char.UUID(), "characteristic UUID MUST match")
	})

	suite.Run("characteristic not found in service", func() {
		// GOAL: Verify GetCharacteristic() returns NotFoundError for non-existent characteristic
		//
		// TEST SCENARIO: Request non-existent characteristic → NotFoundError returned → error message describes issue
		dev := suite.Connect("22")

		char, err := dev.GetConnection().GetCharacteristic("180f", "2a37")

		suite.Assert().Error(err, "MUST return error for non-existent characteristic")
		suite.Assert().Nil(char, "characteristic MUST be nil")

		var notFoundErr *device.NotFoundError
		suite.Assert().ErrorAs(err, &notFoundErr, "error MUST be NotFoundError")
		suite.Assert().Equal("characteristic", notFoundErr.Resource, "resource type MUST be 'characteristic'")
		suite.Assert().Equal([]string{"180f", "2a37"}, notFoundErr.UUIDs, "UUIDs MUST contain service and characteristic UUIDs")
		suite.Assert().Contains(err.Error(), "characteristic \"2a37\" not found in service \"180f\"", "error message MUST describe issue")
	})

	suite.Run("fail if service not found", func() {
		// GOAL: Verify GetCharacteristic() returns NotFoundError when service doesn't exist
		//
		// TEST SCENARIO: Request characteristic from non-existent service → NotFoundError returned → error message describes issue

		dev := suite.Connect("22")
		char, err := dev.GetConnection().GetCharacteristic("ffff", "2a19")

		suite.Assert().Error(err, "MUST return error when service not found")
		suite.Assert().Nil(char, "characteristic MUST be nil")

		var notFoundErr *device.NotFoundError
		suite.Assert().ErrorAs(err, &notFoundErr, "error MUST be NotFoundError")
		suite.Assert().Equal("service", notFoundErr.Resource, "resource type MUST be 'service'")
		suite.Assert().Equal([]string{"ffff"}, notFoundErr.UUIDs, "UUIDs MUST contain service UUID")
		suite.Assert().Equal("service \"ffff\" not found", err.Error(), "error message MUST match expected format")
	})

	suite.Run("multiple characteristics in service", func() {
		// GOAL: Verify multiple characteristics can be retrieved from same service
		//
		// TEST SCENARIO: Service with multiple characteristics → all can be retrieved → correct data returned

		dev := suite.Connect("22")
		conn := dev.GetConnection()
		char1, err1 := conn.GetCharacteristic("180d", "2a37")
		char2, err2 := conn.GetCharacteristic("180d", "2a38")
		char3, err3 := conn.GetCharacteristic("180d", "2a39")

		suite.Assert().NoError(err1, "MUST find first characteristic")
		suite.Assert().NoError(err2, "MUST find second characteristic")
		suite.Assert().NoError(err3, "MUST find third characteristic")
		suite.Assert().Equal("2a37", char1.UUID(), "first characteristic UUID MUST match")
		suite.Assert().Equal("2a38", char2.UUID(), "second characteristic UUID MUST match")
		suite.Assert().Equal("2a39", char3.UUID(), "third characteristic UUID MUST match")
	})
}

func (suite *ConnectionTestSuite) TestConnectionSubscriptionValidation() {
	// GOAL: Verify subscription validation works correctly
	//
	// TEST SCENARIO: Various subscription validation scenarios → proper errors returned → ErrUnsupported wrapped when appropriate

	suite.Run("subscribe to characteristic without notify support", func() {
		// GOAL: Verify subscribing to characteristic without notify/indicate returns ErrUnsupported
		//
		// TEST SCENARIO: Attempt subscription to read-only characteristic → error returned → error wraps device.ErrUnsupported

		// Attempt to subscribe to a characteristic without notify/indicate support
		// Provide a valid callback so validation logic runs (not just nil check)
		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180f",
				Characteristics: []string{"2a19"},
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when validation fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for characteristic without notify support")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "characteristics without notification support", "error message MUST describe unsupported operation")
		suite.Assert().Contains(err.Error(), "2a19", "error message MUST contain characteristic UUID")
	})

	suite.Run("subscribe to multiple characteristics with mixed support", func() {
		// GOAL: Verify subscription validation detects characteristics without notify support
		//
		// TEST SCENARIO: Service with a mix of notify and non-notify characteristics → subscribe to all → error returned with unsupported chars listed

		// Attempt to subscribe to all characteristics in service (some don't support notify)
		// Provide a valid callback so validation logic runs (not just nil check)
		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service: "180d",
				// Empty Characteristics means subscribe to all in service
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when validation fails")
		})

		suite.Assert().Error(err, "subscription MUST fail when some characteristics lack notify support")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "characteristics without notification support", "error message MUST describe unsupported operation")
	})

	suite.Run("subscribe to characteristic with notify support", func() {
		// GOAL: Verify Notify subscription succeeds for characteristic with notify support
		//
		// TEST SCENARIO: Subscribe to notify-supporting char without Indicate flag → subscription succeeds → no error

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"},
				Indicate:        false, // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			// Callback receives notifications
		})

		suite.Assert().NoError(err, "subscription MUST succeed for characteristic with notify support")
	})

	suite.Run("subscribe to characteristic with indicate support", func() {
		// GOAL: Verify Indicate subscription succeeds for characteristic with indicate support
		//
		// TEST SCENARIO: Subscribe to indicate-supporting char with Indicate=true → subscription succeeds → no error

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3a"}, // Indicate-only characteristic
				Indicate:        true,             // Indicate mode
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			// Callback receives indications
		})

		suite.Assert().NoError(err, "subscription MUST succeed for characteristic with indicate support")
	})

	suite.Run("indicate subscription fails on notify-only characteristic", func() {
		// GOAL: Verify Indicate subscription fails when characteristic only supports Notify
		//
		// TEST SCENARIO: Subscribe with Indicate=true to notify-only char → error "does not support indicate"

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"}, // Notify-only characteristic
				Indicate:        true,             // Request Indicate mode
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when subscription fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for indicate on notify-only char")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "does not support indicate", "error message MUST describe unsupported mode")
		suite.Assert().Contains(err.Error(), "2a37", "error message MUST contain characteristic UUID")
	})

	suite.Run("notify subscription fails on indicate-only characteristic", func() {
		// GOAL: Verify Notify subscription fails when characteristic only supports Indicate
		//
		// TEST SCENARIO: Subscribe without Indicate flag to indicate-only char → error "does not support notify"

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3a"}, // Indicate-only characteristic
				Indicate:        false,            // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when subscription fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for notify on indicate-only char")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "does not support notify", "error message MUST describe unsupported mode")
		suite.Assert().Contains(err.Error(), "2a3a", "error message MUST contain characteristic UUID")
	})

	suite.Run("indicate subscription fails on read-only characteristic", func() {
		// GOAL: Verify Indicate subscription fails when characteristic has no notification support
		//
		// TEST SCENARIO: Subscribe with Indicate=true to read-only char → error "does not support indicate"

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180f",
				Characteristics: []string{"2a19"}, // Read-only characteristic (Battery Level)
				Indicate:        true,             // Request Indicate mode
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when subscription fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for indicate on read-only char")
		suite.Assert().ErrorIs(err, device.ErrUnsupported, "error MUST wrap device.ErrUnsupported")
		suite.Assert().Contains(err.Error(), "does not support indicate", "error message MUST describe unsupported mode")
		suite.Assert().Contains(err.Error(), "2a19", "error message MUST contain characteristic UUID")
	})

	suite.Run("both modes supported - indicate selected", func() {
		// GOAL: Verify Indicate subscription works when characteristic supports both modes
		//
		// TEST SCENARIO: Subscribe with Indicate=true to char supporting both → subscription succeeds → no error

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3b"}, // Both notify and indicate
				Indicate:        true,             // Explicitly select Indicate
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			// Callback receives indications
		})

		suite.Assert().NoError(err, "subscription MUST succeed when char supports both and indicate selected")
	})

	suite.Run("both modes supported - notify selected", func() {
		// GOAL: Verify Notify subscription works when characteristic supports both modes
		//
		// TEST SCENARIO: Subscribe without Indicate flag to char supporting both → subscription succeeds → no error

		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3b"}, // Both notify and indicate
				Indicate:        false,            // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			// Callback receives notifications
		})

		suite.Assert().NoError(err, "subscription MUST succeed when char supports both and notify selected")
	})

	suite.Run("subscribe to non-existent service", func() {
		// GOAL: Verify subscription validation detects missing services
		//
		// TEST SCENARIO: Subscribe to non-existent service → error returned → error does NOT wrap ErrUnsupported

		// Provide a valid callback so validation logic runs (not just a nil check)
		// Use service UUID that doesn't exist in the device
		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "ffee",
				Characteristics: []string{"ffef"},
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when validation fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for non-existent service")
		suite.Assert().Contains(err.Error(), "service", "error message MUST mention service")
		suite.Assert().Contains(err.Error(), "ffee", "error message MUST contain service UUID")
	})

	suite.Run("subscribe to non-existent characteristic", func() {
		// GOAL: Verify subscription validation detects missing characteristics
		//
		// TEST SCENARIO: Subscribe to non-existent characteristic → error returned → error does NOT wrap ErrUnsupported

		// Provide a valid callback so validation logic runs (not just nil check)
		dev := suite.Connect("22")
		_, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2aff"},
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when validation fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for non-existent characteristic")
		suite.Assert().NotErrorIs(err, device.ErrUnsupported, "error MUST NOT wrap ErrUnsupported for missing characteristic")
		suite.Assert().Contains(err.Error(), "missing characteristics", "error message MUST describe missing characteristic")
		suite.Assert().Contains(err.Error(), "2aff", "error message MUST contain characteristic UUID")
	})
}

func (suite *ConnectionTestSuite) TestConnectionErrors() {
	// GOAL: Verify ConnectionError types are returned correctly for connection state issues
	//
	// TEST SCENARIO: Various connection state scenarios → proper ConnectionError types returned → error messages are informative

	suite.Run("already connected error uses ErrAlreadyConnected", func() {
		// GOAL: Verify ErrAlreadyConnected is returned when connecting while already connected
		//
		// TEST SCENARIO: Already connected device → attempt connect → ErrAlreadyConnected returned

		ctx := context.Background()
		dev := suite.Connect("22")

		// Attempt to connect again
		err := dev.Connect(ctx, &device.ConnectOptions{
			ConnectTimeout:        5 * time.Second,
			DescriptorReadTimeout: 1 * time.Second,
		})

		suite.Assert().Error(err, "connect MUST fail when already connected")
		suite.Assert().ErrorIs(err, device.ErrAlreadyConnected, "error MUST be ErrAlreadyConnected")
		suite.Assert().Contains(err.Error(), "already_connected", "error message MUST contain connection state")
	})

	suite.Run("subscribe while not connected returns ErrNotConnected", func() {
		// GOAL: Verify ErrNotConnected is returned when subscribing to a disconnected device
		//
		// TEST SCENARIO: Disconnect device → attempt subscribe → ErrNotConnected returned

		dev := suite.Connect("22")

		// Disconnect first
		err := dev.Disconnect()
		suite.Require().NoError(err, "disconnect MUST succeed")

		// Attempt to subscribe while disconnected
		_, err = dev.GetConnection().Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"},
			},
		}, device.StreamEveryUpdate, 0, noDrainPlease, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when not connected")
		})

		suite.Assert().Error(err, "subscribe MUST fail when not connected")
		suite.Assert().ErrorIs(err, device.ErrNotConnected, "error MUST be ErrNotConnected")
		suite.Assert().Contains(err.Error(), "not_connected", "error message MUST contain connection state")
	})

	suite.Run("connect with nil connection returns ErrNotInitialized", func() {
		// GOAL: Verify ErrNotInitialized is returned when connection is nil in Connect
		//
		// TEST SCENARIO: Set connection to nil → attempt connect → ErrNotInitialized returned

		// Use reflection to set connection to nil (should never happen in production)
		bleDev := devicefactory.NewDevice("12", suite.Logger)
		suite.setDeviceConnectionToNil(bleDev)

		ctx := context.Background()
		err := bleDev.Connect(ctx, &device.ConnectOptions{
			ConnectTimeout:        5 * time.Second,
			DescriptorReadTimeout: 1 * time.Second,
		})

		suite.Assert().Error(err, "connect MUST fail when connection is nil")
		suite.Assert().ErrorIs(err, device.ErrNotInitialized, "error MUST be ErrNotInitialized")
		suite.Assert().Contains(err.Error(), "connect", "error message MUST mention connect")
	})

	suite.Run("disconnect with nil connection returns ErrNotInitialized", func() {
		// GOAL: Verify ErrNotInitialized is returned when the connection is nil in Disconnect
		//
		// TEST SCENARIO: Set connection to nil → attempt disconnect → ErrNotInitialized returned

		bleDev := devicefactory.NewDevice("12", suite.Logger)
		suite.setDeviceConnectionToNil(bleDev)

		err := bleDev.Disconnect()

		suite.Assert().Error(err, "disconnect MUST fail when connection is nil")
		suite.Assert().ErrorIs(err, device.ErrNotInitialized, "error MUST be ErrNotInitialized")
		suite.Assert().Contains(err.Error(), "disconnect", "error message MUST mention disconnect")
	})
}

func (suite *ConnectionTestSuite) TestGracefulDisconnect() {
	// GOAL: Verify graceful disconnect handling via CoreBluetooth Disconnected() channel
	//
	// TEST SCENARIO: Close a device disconnect channel → connection context canceled → error cause is ErrNotConnected

	suite.Run("CoreBluetooth disconnect cancels connection context", func() {
		// GOAL: Verify that closing the Disconnected() channel cancels the connection context with ErrNotConnected
		//
		// TEST SCENARIO: Close a disconnect channel → connection context Done() fires → context.Cause() is ErrNotConnected

		dev := suite.Connect("22")

		suite.Require().True(dev.IsConnected(), "device MUST be connected before test")

		// Get the connection context before disconnect
		conn := dev.GetConnection()
		suite.Require().NotNil(conn, "connection MUST exist")
		ctx := conn.ConnectionContext()
		suite.Require().NotNil(ctx, "connection context MUST exist")

		// Verify context is not canceled yet
		select {
		case <-ctx.Done():
			suite.Fail("context MUST NOT be cancelled before disconnect")
		default:
			// Expected: context still active
		}

		// Simulate CoreBluetooth disconnect by closing the channel
		dev.SimulateBluetoothDisconnect()

		// Give the monitoring goroutine a moment to process the disconnect
		time.Sleep(10 * time.Millisecond)

		// Verify connection context was canceled
		select {
		case <-ctx.Done():
			// Expected: context canceled
		case <-time.After(100 * time.Millisecond):
			suite.Fail("connection context MUST be cancelled after disconnect channel closes")
		}

		// Verify the context was canceled with ErrNotConnected cause
		cause := context.Cause(ctx)
		suite.Assert().ErrorIs(cause, device.ErrNotConnected, "context MUST be cancelled with ErrNotConnected")
	})
}

func (suite *ConnectionTestSuite) TestSubscriptions() {
	// GOAL: Verify subscription notification delivery works correctly
	//
	// TEST SCENARIO: Various subscription patterns → notifications delivered → correct data received

	// Time to wait for subscription goroutines to be ready before sending notifications
	const subscriptionReadyDelay = 50 * time.Millisecond

	suite.Run("Ensure expected format for each stream mode", func() {
		type SubscriptionTest struct {
			subscribe []*device.SubscribeOptions
			mode      device.StreamMode
			maxRate   time.Duration
			expected  []device.Record
		}
		everyUpdate := SubscriptionTest{
			[]*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37", "2a3b"}},
			},
			device.StreamEveryUpdate,
			0,
			[]device.Record{
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   1,
					},
					Values: map[string][]byte{
						"2a37": {0x11, 0x12},
					},
				},
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   2,
					},
					Values: map[string][]byte{
						"2a3b": {0x21, 0x22},
					},
				},
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   3,
					},
					Values: map[string][]byte{
						"2a37": {0x13, 0x14},
					},
				},
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   4,
					},
					Values: map[string][]byte{
						"2a3b": {0x23, 0x24},
					},
				},
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   5,
					},
					Values: map[string][]byte{
						"2a37": {0x15, 0x16},
					},
				},
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   6,
					},
					Values: map[string][]byte{
						"2a3b": {0x25},
					},
				},
			}}

		batched := SubscriptionTest{
			[]*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37", "2a3b"}},
			},
			device.StreamBatched,
			99 * time.Millisecond,
			[]device.Record{
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   6,
					},
					Meta: map[string][]*device.RecordMeta{
						"2a37": {
							{TsUs: 1767723913275056, Seq: 1, Flags: 0},
							{TsUs: 1767723913275083, Seq: 3, Flags: 0},
							{TsUs: 1767723913275099, Seq: 5, Flags: 0},
						},
						"2a3b": {
							{TsUs: 1767723913275073, Seq: 2, Flags: 0},
							{TsUs: 1767723913275091, Seq: 4, Flags: 0},
							{TsUs: 1767723913275107, Seq: 6, Flags: 0},
						},
					},
					BatchValues: map[string][][]byte{
						"2a37": {
							{0x11, 0x12},
							{0x13, 0x14},
							{0x15, 0x16},
						},
						"2a3b": {
							{0x21, 0x22},
							{0x23, 0x24},
							{0x25},
						},
					},
				},
			},
		}

		aggregated := SubscriptionTest{
			[]*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37", "2a3b"}},
			},
			device.StreamAggregated, 99 * time.Millisecond,
			[]device.Record{
				{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   6,
						TsUs:  1767723913275107,
					},

					Meta: map[string][]*device.RecordMeta{
						"2a37": {
							{TsUs: 1767723913275056, Seq: 1, Flags: 0},
							{TsUs: 1767723913275083, Seq: 3, Flags: 0},
							{TsUs: 1767723913275099, Seq: 5, Flags: 0},
						},
						"2a3b": {
							{TsUs: 1767723913275073, Seq: 2, Flags: 0},
							{TsUs: 1767723913275091, Seq: 4, Flags: 0},
							{TsUs: 1767723913275107, Seq: 6, Flags: 0},
						},
					},
					Values: map[string][]byte{
						"2a37": {0x15, 0x16},
						"2a3b": {0x25},
					},
				},
			},
		}

		tests := []struct {
			name          string
			subscriptions map[string]SubscriptionTest
			ignoredFields []string
		}{
			{
				"StreamEveryUpdate",
				map[string]SubscriptionTest{
					"every": everyUpdate,
				},
				[]string{"TsUs"},
			},
			{
				// GOAL: Verify StreamBatched mode collects multiple values per characteristic
				//
				// TEST SCENARIO: Subscribe with StreamBatched → send several notifications to different chars → BatchValues and Meta contain all
				"StreamBatched collects values",
				map[string]SubscriptionTest{
					"batched": batched,
				},
				[]string{"TsUs"},
			},
			{
				// GOAL: Verify StreamAggregated mode keeps only the latest value per characteristic
				//
				// TEST SCENARIO: Subscribe with StreamAggregated → send multiple → verify only the latest per char
				"Aggregated keeps the latest",
				map[string]SubscriptionTest{
					"aggregated": aggregated,
				},
				[]string{"TsUs"},
			},
			{
				// GOAL: Verify all three streaming modes work correctly when subscribed simultaneously
				//
				// TEST SCENARIO: Three subscriptions with different modes → send 5 notifications → each mode receives correct data

				"all streaming modes work in parallel",
				map[string]SubscriptionTest{
					"every":      everyUpdate,
					"batched":    batched,
					"aggregated": aggregated,
				},
				[]string{"TsUs", "Seq"},
			},
		}

		for _, tt := range tests {
			tt := tt // <-- shadow loop variable for closure safety
			suite.Run(tt.name, func() {
				var maxRate time.Duration = 0

				ft := suite.Connect("22").FluentTest()

				// -- Subscribe to characteristics
				for key, sub := range tt.subscriptions {
					maxRate = max(maxRate, sub.maxRate)
					ft.Subscribe(key, sub.subscribe, sub.mode, sub.maxRate, noDrainPlease)
				}

				// -- Spawn goroutine for simulated peripheral data and do simulation
				ft.EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					// Simulate a small delay before pushing notifications
					time.Sleep(subscriptionReadyDelay)

					emitter.AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0x11, 0x12).
						WithCharacteristic("2a3b", 0x21, 0x22).
						WithCharacteristic("2a37", 0x13, 0x14).
						WithCharacteristic("2a37", 0x15, 0x16).
						WithCharacteristic("2a3b", 0x23, 0x24).
						WithCharacteristic("2a3b", 0x25).
						Emit(true)
				}).
					// wait just a bit longer than maxRate
					Sleep(max(maxRate+20*time.Millisecond, 100*time.Millisecond))

				for key, s := range tt.subscriptions {
					ft.ConsumeSubscription(key, tt.ignoredFields, s.expected...)
				}
			})
		}
	})

	suite.Run("Cancel subscription", func() {
		// GOAL: Verify the cancel function returned by Subscribe correctly stops notification delivery
		//
		// TEST SCENARIO: Various cancel scenarios → subscription stops → no further notifications received
		//
		// DIAGNOSTIC CONTEXT: This test heavily exercises subscription/connection cleanup verification.
		// Each subtest calls ResetDevice(), which creates fresh connections with cleanup tracking.
		// The diagnostic infrastructure (SubscriptionDiagnostics, ConnectionDiagnostics) validates:
		// - Subscriptions are properly canceled before disconnect
		// - No BLEValue leaks occur
		// - Goroutines exit cleanly
		// Failures here often indicate cleanup bugs rather than functional issues.
		// Time to wait for subscription goroutines to be ready before sending notifications
		const subscriptionReadyDelay = 50 * time.Millisecond

		suite.Run("cancel stops notification delivery", func() {
			// GOAL: Verify calling cancel stops all notifications delivered to that subscription
			//
			// TEST SCENARIO: Subscribe → receive notifications → cancel → send more → NO further notifications

			subscriptionOpts := device.SubscribeOptions{
				Service: "180d", Characteristics: []string{"2a37"},
			}

			suite.Connect("22").FluentTest().
				SubscribeOnEach("sub1", noDrainPlease, &subscriptionOpts).

				// Send notifications BEFORE cancel
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xB1).
						WithCharacteristic("2a37", 0xB2).
						Emit(true)
				}).
				WaitSubscription("sub1", 2, 200*time.Millisecond).
				ConsumeSubscription("sub1", []string{"TsUs"}, device.Record{
					RecordMeta: device.RecordMeta{
						Flags: 0,
						Seq:   1,
					},
					Values: map[string][]byte{
						"2a37": {0xb1}, // single byte value
					},
				},
					device.Record{
						RecordMeta: device.RecordMeta{
							Flags: 0,
							Seq:   2,
						},
						Values: map[string][]byte{
							"2a37": {0xb2}, // single byte value
						},
					}).
				CancelSubscription("sub1").
				Sleep(100 * time.Millisecond).

				// Send notifications AFTER cancel - should NOT be received
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xB3).
						WithCharacteristic("2a37", 0xB4).
						Emit(true)
				}).
				Sleep(300 * time.Millisecond).

				// Canceled subscription MUST NOT receive notifications after cancel
				ConsumeEmptySubscription("sub1")
		})

		suite.Run("cancel is idempotent", func() {
			// GOAL: Verify calling cancel multiple times does not panic or cause errors
			//
			// TEST SCENARIO: Subscribe → cancel → cancel again → no panic

			dev := suite.Connect("22")

			cancel, err := dev.GetConnection().Subscribe([]*device.SubscribeOptions{
				{Service: "180d", Characteristics: []string{"2a37"}},
			}, device.StreamEveryUpdate, 0, 0, func(r *device.Record) {})
			suite.Require().NoError(err)
			suite.Require().NotNil(cancel)

			// Call subscription cancel() multiple times - should not panic
			suite.NotPanics(func() {
				cancel()
				cancel()
				cancel()
			}, "multiple cancel calls MUST NOT panic")
		})

		suite.Run("cancel one subscription does not affect others", func() {
			// GOAL: Verify that canceling one subscription leaves other subscriptions active
			//
			// TEST SCENARIO: Two subscriptions → cancel first → second still receives notifications

			suite.Connect("22").FluentTest().
				SubscribeOnEach("subA", noDrainPlease, &device.SubscribeOptions{
					Service: "180d", Characteristics: []string{"2a37"},
				}).
				SubscribeOnEach("subB", noDrainPlease, &device.SubscribeOptions{
					Service: "180d", Characteristics: []string{"2a37"},
				}).
				Sleep(subscriptionReadyDelay).

				// Cancel only subscription A
				CancelSubscription("subA").
				Sleep(100*time.Millisecond).

				// Send notifications - only B should receive them
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xC1).
						WithCharacteristic("2a37", 0xC2).
						Emit(true)
				}).
				WaitSubscription("subB", 2, 100*time.Millisecond).

				// Subscription A MUST NOT receive after cancel
				ConsumeEmptySubscription("subA").

				// Subscription B MUST still receive notifications
				ConsumeSubscription("subB", []string{"TsUs"},
					device.Record{
						RecordMeta: device.RecordMeta{
							Flags: 0,
							Seq:   1,
						},
						Values: map[string][]byte{
							"2a37": {0xC1},
						},
					},
					device.Record{
						RecordMeta: device.RecordMeta{
							Flags: 0,
							Seq:   2,
						},
						Values: map[string][]byte{
							"2a37": {0xC2},
						},
					},
				)
		})
	})

	suite.Run("side effects", func() {
		subscriptionOpts := device.SubscribeOptions{
			Service: "180d", Characteristics: []string{"2a37"},
		}

		expected := []device.Record{
			{
				RecordMeta: device.RecordMeta{
					Flags: 0,
					Seq:   1,
				},
				Values: map[string][]byte{
					"2a37": {0xb1}, // single byte value
				},
			},
			{
				RecordMeta: device.RecordMeta{
					Flags: 0,
					Seq:   2,
				},
				Values: map[string][]byte{
					"2a37": {0xb2}, // single byte value
				},
			},
		}

		suite.Run("without drain, stale cached notifications leak into subscription", func() {
			// GOAL: Verify stale cached notifications pass through when drain is disabled (by design)
			//
			// TEST SCENARIO: Emit before subscribe with no drain → subscribe → stale notifications received

			suite.Connect("22").FluentTest().
				// Send notifications BEFORE any subscriptions
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xB1).
						WithCharacteristic("2a37", 0xB2).
						Emit(true)
				}).

				// Ensure data emitted
				Sleep(300*time.Millisecond).

				// Subscription receives stale notifications as drain is disabled (zero)
				SubscribeOnEach("sub1", noDrainPlease, &subscriptionOpts).
				WaitSubscription("sub1", 2, 200*time.Millisecond).
				ConsumeSubscription("sub1", []string{"TsUs"}, expected...)
		})

		suite.Run("with drain, stale notifications discarded and fresh ones pass through", func() {
			// GOAL: Verify that the drain mechanism discards stale notifications while passing fresh ones
			//
			// TEST SCENARIO: Emit stale → subscribe with drain → emit fresh → only fresh received

			freshExpected := []device.Record{
				{
					RecordMeta: device.RecordMeta{Flags: 0, Seq: 1},
					Values:     map[string][]byte{"2a37": {0xF1}},
				},
				{
					RecordMeta: device.RecordMeta{Flags: 0, Seq: 2},
					Values:     map[string][]byte{"2a37": {0xF2}},
				},
			}

			suite.Connect("22").FluentTest().
				// Emit stale notifications BEFORE subscribing
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xA1). // stale
						WithCharacteristic("2a37", 0xA2). // stale
						Emit(true)
				}).
				Sleep(100*time.Millisecond). // ensure stale emissions complete

				// Subscribe with drain - will discard stale notifications
				SubscribeOnEach("sub1", drainFor200ms, &subscriptionOpts).

				// Wait for the drain to complete
				Sleep(250*time.Millisecond).

				// Emit fresh notifications AFTER drain completes
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						AllowMultiValue().
						WithService("180d").
						WithCharacteristic("2a37", 0xF1). // fresh
						WithCharacteristic("2a37", 0xF2). // fresh
						Emit(true)
				}).
				WaitSubscription("sub1", 2, 200*time.Millisecond).

				// Only fresh notifications should be received (stale were drained)
				// Note: Seq is ignored because drain discards values, but Seq counter continues
				ConsumeSubscription("sub1", []string{"TsUs", "Seq"}, freshExpected...)
		})

		suite.Run("drain exits early when stale flow stops", func() {
			// GOAL: Ensure that drain exits early when no more stale values arrive (quiet period detection)
			//
			// TEST SCENARIO: Emit stale → subscribe with long drain → stale stops → fresh arrives before drain expires

			// Expected: only fresh notification (0xF1), stale (0xA1) discarded
			freshExpected := []device.Record{
				{
					RecordMeta: device.RecordMeta{Flags: 0, Seq: 1},
					Values:     map[string][]byte{"2a37": {0xF1}},
				},
			}

			// Track time to prove drain exited early (< 1s drain duration)
			start := time.Now()

			suite.Connect("22").FluentTest().
				// 1. Emit stale before subscribing
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						WithService("180d").
						WithCharacteristic("2a37", 0xA1). // stale
						Emit(true)
				}).
				Sleep(50*time.Millisecond). // ensure stale emission completes

				// 2. Subscribe with 1s drain (will exit early after ~50ms quiet period)
				SubscribeOnEach("sub1", 1*time.Second, &subscriptionOpts).

				// 3. Wait 100ms - stale flow stopped, drain should exit early
				Sleep(100*time.Millisecond).

				// 4. Emit fresh - arrives immediately if drain exited early
				EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
					emitter.
						WithService("180d").
						WithCharacteristic("2a37", 0xF1). // fresh
						Emit(true)
				}).
				// 5. Wait for fresh notification (300ms timeout proves early exit)
				WaitSubscription("sub1", 1, 300*time.Millisecond).
				// 6. Verify only fresh received (Seq ignored - counter continues after drain)
				ConsumeSubscription("sub1", []string{"TsUs", "Seq"}, freshExpected...)

			// Assert: total time < 800ms proves drain didn't wait full 1s
			elapsed := time.Since(start)
			suite.Assert().Less(elapsed, 800*time.Millisecond,
				"drain MUST exit early when stale flow stops, not wait full duration")
		})

	})
}

// TestConnectionTestSuite runs the test suite
func TestConnectionTestSuite(t *testing.T) {
	suite.Run(t, new(ConnectionTestSuite))
}
