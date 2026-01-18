//go:build test

package lua

import (
	"context"

	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
)

type LuaApiSuite struct {
	testutils.PeripheralDeviceSuite
}

// SetupTest initializes the test environment with a mock BLE peripheral and Lua API instance.
func (suite *LuaApiSuite) SetupTest() {
	suite.PeripheralDeviceSuite.SetupTest()

	suite.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.
			FromJSON(`{
				"services": [
					{
						"uuid": "1234",
						"characteristics": [
							{
								"uuid": "5678",
								"properties": "read,notify",
								"value": []
							},
							{
								"uuid": "ABCD",
								"properties": "write,write-without-response",
								"value": []
							}
						]
					},
					{
						"uuid": "180D",
						"characteristics": [
							{ "uuid": "2A37", "properties": "read,notify", "value": [] },
							{ "uuid": "2A38", "properties": "read,notify", "value": [] }
						]
					},
					{
						"uuid": "180F",
						"characteristics": [
							{ "uuid": "2A19", "properties": "read,notify", "value": [] }
						]
					}
				]
			}`)
	})
}

// Connect returns a LuaTestableDevice for the current test and registers cleanup.
// This is the primary entry point for fluent-style Lua testing.
func (suite *LuaApiSuite) Connect(addr string) *LuaTestableDevice {
	return suite.ConnectDeviceWithContext(context.Background(), addr, nil)
}

// ConnectDeviceWithContext creates a device with context support and returns a cleanup function
func (suite *LuaApiSuite) ConnectDeviceWithContext(ctx context.Context, addr string, opts *device.ConnectOptions) *LuaTestableDevice {
	td, tdCleanup := suite.PeripheralDeviceSuite.ConnectDeviceWithContext(ctx, addr, nil)

	ltd := NewLuaTestableDevice(suite.T(), &suite.Suite, suite.Logger, td.GenericTestableDevice)

	cleanup := func() {
		ltd.cleanup()() // cleanup() returns a func, must call it
		tdCleanup()
	}

	suite.T().Cleanup(cleanup)

	return ltd
}

// SetupSubTest reinitializes test resources for subtests.
func (suite *LuaApiSuite) SetupSubTest() {
	suite.PeripheralDeviceSuite.SetupSubTest()
}

// TearDownSubTest cleans up subtest resources. Must explicitly call the parent's TearDownSubTest
// since Go doesn't auto-chain embedded struct methods when overridden.
func (suite *LuaApiSuite) TearDownSubTest() {
	suite.PeripheralDeviceSuite.TearDownSubTest()
}
