//go:build test

package device_test

import (
	"context"
	"testing"
	"time"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

type ConnectionTestSuite struct {
	DeviceTestSuite
}

func (suite *ConnectionTestSuite) TestConnectionServices() {
	// GOAL: Verify connection service discovery and retrieval work correctly
	//
	// TEST SCENARIO: Various service access patterns → services retrieved correctly → proper error handling

	suite.Run("get all services", func() {
		// GOAL: Verify Services() returns all discovered services
		//
		// TEST SCENARIO: Connect to a device with multiple services → Services() called → all services returned in sorted order

		services := suite.connection.Services()

		suite.Assert().Len(services, 3, "MUST return all services")
		suite.Assert().Equal("1800", services[0].UUID(), "first service MUST be 1800 (Generic Access, sorted order)")
		suite.Assert().Equal("180d", services[1].UUID(), "second service MUST be 180d (Heart Rate, sorted order)")
		suite.Assert().Equal("180f", services[2].UUID(), "third service MUST be 180f (Battery, sorted order)")
	})

	suite.Run("get service by UUID", func() {
		// GOAL: Verify GetService() retrieves service by UUID
		//
		// TEST SCENARIO: Request service by UUID → service returned → UUID matches

		svc, err := suite.connection.GetService("180f")

		suite.Assert().NoError(err, "MUST find service")
		suite.Assert().NotNil(svc, "service MUST not be nil")
		suite.Assert().Equal("180f", svc.UUID(), "service UUID MUST match")
	})

	suite.Run("fail when service not found", func() {
		// GOAL: Verify GetService() returns NotFoundError for non-existent service
		//
		// TEST SCENARIO: Request non-existent service → NotFoundError returned → error message describes issue

		svc, err := suite.connection.GetService("ffff")

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

		// Test various UUID formats
		svc1, err1 := suite.connection.GetService("180f")
		svc2, err2 := suite.connection.GetService("180F")
		svc3, err3 := suite.connection.GetService("0000180f-0000-1000-8000-00805f9b34fb")

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

		char, err := suite.connection.GetCharacteristic("180f", "2a19")

		suite.Assert().NoError(err, "MUST find characteristic")
		suite.Assert().NotNil(char, "characteristic MUST not be nil")
		suite.Assert().Equal("2a19", char.UUID(), "characteristic UUID MUST match")
	})

	suite.Run("characteristic not found in service", func() {
		// GOAL: Verify GetCharacteristic() returns NotFoundError for non-existent characteristic
		//
		// TEST SCENARIO: Request non-existent characteristic → NotFoundError returned → error message describes issue

		char, err := suite.connection.GetCharacteristic("180f", "2a37")

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

		char, err := suite.connection.GetCharacteristic("ffff", "2a19")

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

		char1, err1 := suite.connection.GetCharacteristic("180d", "2a37")
		char2, err2 := suite.connection.GetCharacteristic("180d", "2a38")
		char3, err3 := suite.connection.GetCharacteristic("180d", "2a39")

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
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180f",
				Characteristics: []string{"2a19"},
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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
		// TEST SCENARIO: Service with mix of notify and non-notify characteristics → subscribe to all → error returned with unsupported chars listed

		// Attempt to subscribe to all characteristics in service (some don't support notify)
		// Provide a valid callback so validation logic runs (not just nil check)
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service: "180d",
				// Empty Characteristics means subscribe to all in service
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"},
				Indicate:        false, // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
			// Callback receives notifications
		})

		suite.Assert().NoError(err, "subscription MUST succeed for characteristic with notify support")
	})

	suite.Run("subscribe to characteristic with indicate support", func() {
		// GOAL: Verify Indicate subscription succeeds for characteristic with indicate support
		//
		// TEST SCENARIO: Subscribe to indicate-supporting char with Indicate=true → subscription succeeds → no error

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3a"}, // Indicate-only characteristic
				Indicate:        true,             // Indicate mode
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
			// Callback receives indications
		})

		suite.Assert().NoError(err, "subscription MUST succeed for characteristic with indicate support")
	})

	suite.Run("indicate subscription fails on notify-only characteristic", func() {
		// GOAL: Verify Indicate subscription fails when characteristic only supports Notify
		//
		// TEST SCENARIO: Subscribe with Indicate=true to notify-only char → error "does not support indicate"

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"}, // Notify-only characteristic
				Indicate:        true,             // Request Indicate mode
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3a"}, // Indicate-only characteristic
				Indicate:        false,            // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180f",
				Characteristics: []string{"2a19"}, // Read-only characteristic (Battery Level)
				Indicate:        true,             // Request Indicate mode
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3b"}, // Both notify and indicate
				Indicate:        true,             // Explicitly select Indicate
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
			// Callback receives indications
		})

		suite.Assert().NoError(err, "subscription MUST succeed when char supports both and indicate selected")
	})

	suite.Run("both modes supported - notify selected", func() {
		// GOAL: Verify Notify subscription works when characteristic supports both modes
		//
		// TEST SCENARIO: Subscribe without Indicate flag to char supporting both → subscription succeeds → no error

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a3b"}, // Both notify and indicate
				Indicate:        false,            // Notify mode (default)
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "ffee",
				Characteristics: []string{"ffef"},
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2aff"},
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
			suite.Fail("callback MUST NOT be invoked when validation fails")
		})

		suite.Assert().Error(err, "subscription MUST fail for non-existent characteristic")
		suite.Assert().NotErrorIs(err, device.ErrUnsupported, "error MUST NOT wrap ErrUnsupported for missing characteristic")
		suite.Assert().Contains(err.Error(), "missing characteristics", "error message MUST describe missing characteristic")
		suite.Assert().Contains(err.Error(), "2aff", "error message MUST contain characteristic UUID")
	})
}

func (suite *ConnectionTestSuite) TestSubscriptions() {
	// GOAL: Verify subscription notification delivery works correctly
	//
	// TEST SCENARIO: Various subscription patterns → notifications delivered → correct data received

	// Time to wait for subscription goroutines to be ready before sending notifications
	const subscriptionReadyDelay = 50 * time.Millisecond

	suite.Run("single subscription receives notifications", func() {
		// GOAL: Verify a single subscription receives all notifications
		//
		// TEST SCENARIO: Subscribe to char → send 3 notifications → callback receives all 3

		suite.ResetDevice() // Clean device state - no leftover subscriptions
		collector := testutils.NewSubscriptionRecordCollector()

		// Delay notifications to ensure subscription goroutine is ready
		// Test 1 values: 0x11, 0x12, 0x13 (unique prefix 0x1x)
		go func() {
			time.Sleep(subscriptionReadyDelay)
			suite.NewPeripheralDataSimulator().AllowMultiValue().
				WithService("180d").
				WithCharacteristic("2a37", []byte{0x11}).
				WithCharacteristic("2a37", []byte{0x12}).
				WithCharacteristic("2a37", []byte{0x13}).
				Simulate(false)
		}()

		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collector.Callback())
		suite.Require().NoError(err, "subscription MUST succeed")

		suite.Require().True(collector.WaitForCount(3, 2*time.Second), "timeout waiting for notifications")

		values := collector.Values("2a37")
		suite.Assert().Len(values, 3, "MUST receive 3 notifications")
		suite.Assert().Equal([]byte{0x11}, values[0], "first notification MUST match")
		suite.Assert().Equal([]byte{0x12}, values[1], "second notification MUST match")
		suite.Assert().Equal([]byte{0x13}, values[2], "third notification MUST match")
	})

	suite.Run("multiple subscriptions same char broadcast", func() {
		// GOAL: Verify multiple subscriptions to the same char expeteec that each receive ALL notifications (broadcast)
		//
		// TEST SCENARIO: Two subscriptions to char → send 4 notifications → BOTH receive all 4

		suite.ResetDevice() // Clean device state - no leftover subscriptions
		collectorA := testutils.NewSubscriptionRecordCollector()
		collectorB := testutils.NewSubscriptionRecordCollector()

		// Subscription A
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collectorA.Callback())
		suite.Require().NoError(err, "subscription A MUST succeed")

		// Subscription B
		_, err = suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collectorB.Callback())
		suite.Require().NoError(err, "subscription B MUST succeed")

		// Delay notifications AFTER all subscriptions are set up
		// Test 2 values: 0x20, 0x21, 0x22, 0x23 (unique prefix 0x2x)
		go func() {
			time.Sleep(subscriptionReadyDelay)
			suite.NewPeripheralDataSimulator().AllowMultiValue().
				WithService("180d").
				WithCharacteristic("2a37", []byte{0x20}).
				WithCharacteristic("2a37", []byte{0x21}).
				WithCharacteristic("2a37", []byte{0x22}).
				WithCharacteristic("2a37", []byte{0x23}).
				Simulate(false)
		}()

		suite.Require().True(collectorA.WaitForCount(4, 2*time.Second), "timeout waiting for subscription A")
		suite.Require().True(collectorB.WaitForCount(4, 2*time.Second), "timeout waiting for subscription B")

		// Both subscriptions MUST receive all four values (broadcast, not competing)
		expected := [][]byte{{0x20}, {0x21}, {0x22}, {0x23}}
		suite.Assert().ElementsMatch(expected, collectorA.Values("2a37"), "subscription A MUST have values 0,1,2,3")
		suite.Assert().ElementsMatch(expected, collectorB.Values("2a37"), "subscription B MUST have values 0,1,2,3")
	})

	suite.Run("StreamBatched collects values", func() {
		// GOAL: Verify StreamBatched mode collects multiple values per characteristic
		//
		// TEST SCENARIO: Subscribe with StreamBatched → send 3 notifications → BatchValues contains all 3

		suite.ResetDevice() // Clean device state - no leftover subscriptions
		collector := testutils.NewSubscriptionRecordCollector()
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamBatched, 100*time.Millisecond, collector.Callback())
		suite.Require().NoError(err, "subscription MUST succeed")

		// Send multiple notifications quickly (within a batch interval)
		suite.NewPeripheralDataSimulator().AllowMultiValue().
			WithService("180d").
			WithCharacteristic("2a37", []byte{0xAA}).
			WithCharacteristic("2a37", []byte{0xBB}).
			WithCharacteristic("2a37", []byte{0xCC}).
			Simulate(false)

		suite.Require().True(collector.WaitForCount(1, 2*time.Second), "timeout waiting for batched values")

		// Verify all 3 values received across batches
		flattenedValues := collector.FlattenedBatchValues("2a37")
		expected := [][]byte{{0xAA}, {0xBB}, {0xCC}}
		suite.Assert().ElementsMatch(expected, flattenedValues, "batch MUST contain all values")
	})

	suite.Run("StreamAggregated keeps latest", func() {
		// GOAL: Verify StreamAggregated mode keeps only the latest value per characteristic
		//
		// TEST SCENARIO: Subscribe with StreamAggregated → send multiple → verify only latest per char

		suite.ResetDevice() // Clean device state - no leftover subscriptions
		collector := testutils.NewSubscriptionRecordCollector()
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamAggregated, 100*time.Millisecond, collector.Callback())
		suite.Require().NoError(err, "subscription MUST succeed")

		// Send multiple notifications quickly
		// Test 4 values: 0xA1, 0xA2, 0xA3 (unique prefix 0xAx)
		suite.NewPeripheralDataSimulator().AllowMultiValue().
			WithService("180d").
			WithCharacteristic("2a37", []byte{0xA1}).
			WithCharacteristic("2a37", []byte{0xA2}).
			WithCharacteristic("2a37", []byte{0xA3}).
			Simulate(false)

		suite.Require().True(collector.WaitForCount(1, 2*time.Second), "timeout waiting for aggregated value")

		// Aggregated mode should deliver the latest value (0xA3)
		suite.Assert().Equal([]byte{0xA3}, collector.LastValue("2a37"), "MUST receive latest value")
	})

	suite.Run("all streaming modes work in parallel", func() {
		// GOAL: Verify all three streaming modes work correctly when subscribed simultaneously
		//
		// TEST SCENARIO: Three subscriptions with different modes → send 5 notifications → each mode receives correct data

		suite.ResetDevice() // Clean device state - no leftover subscriptions
		everyCollector := testutils.NewSubscriptionRecordCollector()
		batchCollector := testutils.NewSubscriptionRecordCollector()
		aggCollector := testutils.NewSubscriptionRecordCollector()

		// Subscription A: StreamEveryUpdate
		_, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, everyCollector.Callback())
		suite.Require().NoError(err, "EveryUpdate subscription MUST succeed")

		// Subscription B: StreamBatched
		_, err = suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamBatched, 100*time.Millisecond, batchCollector.Callback())
		suite.Require().NoError(err, "Batched subscription MUST succeed")

		// Subscription C: StreamAggregated
		_, err = suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamAggregated, 100*time.Millisecond, aggCollector.Callback())
		suite.Require().NoError(err, "Aggregated subscription MUST succeed")

		// Delay notifications to ensure all subscription goroutines are ready
		// Test 5 values: 0x51, 0x52, 0x53, 0x54, 0x55 (unique prefix 0x5x)
		go func() {
			time.Sleep(subscriptionReadyDelay)
			suite.NewPeripheralDataSimulator().AllowMultiValue().
				WithService("180d").
				WithCharacteristic("2a37", []byte{0x51}).
				WithCharacteristic("2a37", []byte{0x52}).
				WithCharacteristic("2a37", []byte{0x53}).
				WithCharacteristic("2a37", []byte{0x54}).
				WithCharacteristic("2a37", []byte{0x55}).
				Simulate(false)
		}()

		// Wait for batch/aggregated timers to fire (100ms interval + buffer for delayed start)
		time.Sleep(300 * time.Millisecond)

		// Verify EveryUpdate received all notifications
		expected := [][]byte{{0x51}, {0x52}, {0x53}, {0x54}, {0x55}}
		suite.Assert().ElementsMatch(expected, everyCollector.Values("2a37"), "EveryUpdate MUST have all values")

		// Verify Batched collected all five values across batches
		suite.Assert().ElementsMatch(expected, batchCollector.FlattenedBatchValues("2a37"), "Batched MUST have all values")

		// Verify Aggregated has the latest value
		suite.Assert().Equal([]byte{0x55}, aggCollector.LastValue("2a37"), "Aggregated MUST have latest value (0x55)")
	})
}

func (suite *ConnectionTestSuite) TestSubscriptionCancel() {
	// GOAL: Verify the cancel function returned by Subscribe correctly stops notification delivery
	//
	// TEST SCENARIO: Various cancel scenarios → subscription stops → no further notifications received

	// Time to wait for subscription goroutines to be ready before sending notifications
	const subscriptionReadyDelay = 50 * time.Millisecond

	suite.Run("cancel stops notification delivery", func() {
		// GOAL: Verify calling cancel stops all notification delivery for that subscription
		//
		// TEST SCENARIO: Subscribe → receive notifications → cancel → send more → NO further notifications

		suite.ResetDevice()
		collector := testutils.NewSubscriptionRecordCollector()

		// Subscribe and capture the cancel function
		cancel, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collector.Callback())
		suite.Require().NoError(err, "subscription MUST succeed")
		suite.Require().NotNil(cancel, "cancel function MUST be returned")

		// Send notifications BEFORE cancel
		time.Sleep(subscriptionReadyDelay)
		suite.NewPeripheralDataSimulator().AllowMultiValue().
			WithService("180d").
			WithCharacteristic("2a37", []byte{0xB1}).
			WithCharacteristic("2a37", []byte{0xB2}).
			Simulate(false)

		suite.Require().True(collector.WaitForCount(2, 2*time.Second), "timeout waiting for pre-cancel notifications")
		suite.Assert().Len(collector.Values("2a37"), 2, "MUST receive 2 notifications before cancel")

		// Call cancel
		cancel()
		time.Sleep(100 * time.Millisecond) // Wait for goroutine to exit

		countAfterCancel := collector.Count()

		// Send notifications AFTER cancel - should NOT be received
		suite.NewPeripheralDataSimulator().AllowMultiValue().
			WithService("180d").
			WithCharacteristic("2a37", []byte{0xB3}).
			WithCharacteristic("2a37", []byte{0xB4}).
			Simulate(false)

		time.Sleep(200 * time.Millisecond)

		suite.Assert().Equal(countAfterCancel, collector.Count(),
			"MUST NOT receive notifications after cancel")
	})

	suite.Run("cancel is idempotent", func() {
		// GOAL: Verify calling cancel multiple times does not panic or cause errors
		//
		// TEST SCENARIO: Subscribe → cancel → cancel again → no panic

		suite.ResetDevice()

		cancel, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, func(r *device.Record) {})
		suite.Require().NoError(err)
		suite.Require().NotNil(cancel)

		// Call cancel multiple times - should not panic
		suite.NotPanics(func() {
			cancel()
			cancel()
			cancel()
		}, "multiple cancel calls MUST NOT panic")
	})

	suite.Run("cancel one subscription does not affect others", func() {
		// GOAL: Verify canceling one subscription leaves other subscriptions active
		//
		// TEST SCENARIO: Two subscriptions → cancel first → second still receives notifications

		suite.ResetDevice()
		collectorA := testutils.NewSubscriptionRecordCollector()
		collectorB := testutils.NewSubscriptionRecordCollector()

		// Subscription A
		cancelA, err := suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collectorA.Callback())
		suite.Require().NoError(err)

		// Subscription B
		_, err = suite.connection.Subscribe([]*device.SubscribeOptions{
			{Service: "180d", Characteristics: []string{"2a37"}},
		}, device.StreamEveryUpdate, 0, collectorB.Callback())
		suite.Require().NoError(err)

		time.Sleep(subscriptionReadyDelay)

		// Cancel only subscription A
		cancelA()
		time.Sleep(100 * time.Millisecond)

		// Send notifications - only B should receive them
		suite.NewPeripheralDataSimulator().AllowMultiValue().
			WithService("180d").
			WithCharacteristic("2a37", []byte{0xC1}).
			WithCharacteristic("2a37", []byte{0xC2}).
			Simulate(false)

		suite.Require().True(collectorB.WaitForCount(2, 2*time.Second), "subscription B MUST receive notifications")
		suite.Assert().Equal(0, collectorA.Count(), "subscription A MUST NOT receive after cancel")
		suite.Assert().Equal(2, collectorB.Count(), "subscription B MUST still receive notifications")
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

		// suite.device is already connected via SetupTest

		// Attempt to connect again
		ctx := context.Background()
		err := suite.device.Connect(ctx, &device.ConnectOptions{
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

		// Disconnect first
		err := suite.device.Disconnect()
		suite.Require().NoError(err, "disconnect MUST succeed")

		// Attempt to subscribe while disconnected
		_, err = suite.connection.Subscribe([]*device.SubscribeOptions{
			{
				Service:         "180d",
				Characteristics: []string{"2a37"},
			},
		}, device.StreamEveryUpdate, 0, func(record *device.Record) {
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
		suite.setDeviceConnectionToNil()

		ctx := context.Background()
		err := suite.device.Connect(ctx, &device.ConnectOptions{
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

		suite.setDeviceConnectionToNil()

		err := suite.device.Disconnect()

		suite.Assert().Error(err, "disconnect MUST fail when connection is nil")
		suite.Assert().ErrorIs(err, device.ErrNotInitialized, "error MUST be ErrNotInitialized")
		suite.Assert().Contains(err.Error(), "disconnect", "error message MUST mention disconnect")
	})
}

func (suite *ConnectionTestSuite) TestGracefulDisconnect() {
	// GOAL: Verify graceful disconnect handling via CoreBluetooth Disconnected() channel
	//
	// TEST SCENARIO: Close disconnect channel → connection context cancelled → error cause is ErrNotConnected

	suite.Run("CoreBluetooth disconnect cancels connection context", func() {
		// GOAL: Verify that closing the Disconnected() channel cancels the connection context with ErrNotConnected
		//
		// TEST SCENARIO: Close disconnect channel → connection context Done() fires → context.Cause() is ErrNotConnected

		suite.Require().True(suite.device.IsConnected(), "device MUST be connected before test")

		// Get the connection context before disconnect
		conn := suite.device.GetConnection()
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

		// Get the disconnect channel from the peripheral builder
		disconnectChan := suite.PeripheralBuilder.GetDisconnectChannel()
		suite.Require().NotNil(disconnectChan, "disconnect channel MUST exist after Build()")

		// Simulate CoreBluetooth disconnect by closing the channel
		close(disconnectChan)

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

// TestConnectionTestSuite runs the test suite
func TestConnectionTestSuite(t *testing.T) {
	suite.Run(t, new(ConnectionTestSuite))
}
