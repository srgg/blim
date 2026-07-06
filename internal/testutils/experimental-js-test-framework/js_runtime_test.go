//go:build test

package experimental_js_test_framework

import (
	"testing"

	"github.com/dop251/goja"
	"github.com/srgg/blim/internal/testutils"
	"github.com/stretchr/testify/suite"
)

// JSRuntimeTestSuite tests the JS test framework integration with GenericTestableDevice.
type JSRuntimeTestSuite struct {
	testutils.PeripheralDeviceSuite
}

func (s *JSRuntimeTestSuite) SetupTest() {
	s.PeripheralDeviceSuite.SetupTest()

	s.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.FromJSON(`{
			"services": [
				{
					"uuid": "180D",
					"characteristics": [
						{
							"uuid": "2A37",
							"properties": "read,notify",
							"value": [0, 80]
						}
					]
				}
			]
		}`)
	})
}

// runJS executes JS code using the TestRuntime and returns any error.
func (s *JSRuntimeTestSuite) runJS(jsCode string) error {
	td := s.Connect("1")
	vm := goja.New()
	runtime := NewTestRuntime(td.GenericTestableDevice, vm)
	s.Require().NoError(vm.Set("t", runtime))

	_, err := vm.RunString(jsCode)
	return err
}

// TestSleepFunction verifies the t.Sleep() JS function works.
func (s *JSRuntimeTestSuite) TestSleepFunction() {
	// GOAL: Verify t.Sleep() executes without error
	//
	// TEST SCENARIO: Call t.Sleep(10) → no error → function works

	err := s.runJS(`t.Sleep(10)`)
	s.Require().NoError(err, "t.Sleep() should execute without error")
}

// TestStreamModeConstants verifies stream mode constants are exposed to JS.
func (s *JSRuntimeTestSuite) TestStreamModeConstants() {
	// GOAL: Verify StreamEveryUpdate, StreamBatched, StreamAggregated constants are available in JS
	//
	// TEST SCENARIO: Access stream constants → values match Go constants → constants exposed correctly

	err := s.runJS(`
		if (typeof StreamEveryUpdate !== 'number') throw new Error('StreamEveryUpdate not defined');
		if (typeof StreamBatched !== 'number') throw new Error('StreamBatched not defined');
		if (typeof StreamAggregated !== 'number') throw new Error('StreamAggregated not defined');
	`)
	s.Require().NoError(err, "Stream mode constants should be available in JS")
}

// TestAssertJSONSuccess verifies t.AssertJSON() passes for matching JSON.
func (s *JSRuntimeTestSuite) TestAssertJSONSuccess() {
	// GOAL: Verify t.AssertJSON() passes when expected matches actual
	//
	// TEST SCENARIO: Call AssertJSON with matching objects → no error → assertion passes

	err := s.runJS(`
		t.AssertJSON(
			{ "name": "test", "value": 42 },
			{ "name": "test", "value": 42 },
			[]
		)
	`)
	s.Require().NoError(err, "t.AssertJSON() should pass for matching JSON")
}

// TestAssertJSONFailure verifies t.AssertJSON() fails when JSON doesn't match.
func (s *JSRuntimeTestSuite) TestAssertJSONFailure() {
	// GOAL: Verify t.AssertJSON() returns error when expected != actual
	//
	// TEST SCENARIO: Call AssertJSON with mismatched objects → error returned → assertion fails properly

	err := s.runJS(`
		t.AssertJSON(
			{ "name": "test", "value": 42 },
			{ "name": "test", "value": 99 },
			[]
		)
	`)
	s.Require().Error(err, "t.AssertJSON() should fail for mismatched JSON")
}

func TestJSRuntimeSuite(t *testing.T) {
	suite.Run(t, new(JSRuntimeTestSuite))
}
