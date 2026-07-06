//go:build test

package lua

import (
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/srgg/blim/internal/testutils"
	"github.com/stretchr/testify/require"
	suitelib "github.com/stretchr/testify/suite"
)

// FluentLuaTestSuite tests the FluentLuaTest API itself
type FluentLuaTestSuite struct {
	LuaApiSuite
}

// TBInterceptor wraps *testing.T to intercept FailNow and prevent test failure propagation.
// Used for "guinea pig" tests that verify failure behavior without marking parent test as failed.
type TBInterceptor struct {
	realT     *testing.T
	failed    bool
	failedNow bool
	errorMsgs []string
}

func (tb *TBInterceptor) Errorf(format string, args ...interface{}) {
	tb.failed = true
	tb.errorMsgs = append(tb.errorMsgs, fmt.Sprintf(format, args...))
	tb.realT.Logf("[INTERCEPTED] "+format, args...)
}

func (tb *TBInterceptor) FailNow() {
	tb.failed = true
	tb.failedNow = true
	// Use runtime.Goexit() like Go's testing.T.FailNow() does.
	// This terminates the goroutine cleanly without propagating as a panic,
	// which prevents testify/suite's panic recovery from marking the test as failed.
	runtime.Goexit()
}

func (tb *TBInterceptor) Helper() {}

// GuineaPigResult captures the outcome of a guinea pig test
type GuineaPigResult struct {
	Failed    bool
	FailedNow bool
	ErrorMsgs []string
}

// runGuineaPig executes a test function expected to fail, capturing the failure
// without propagating it to the parent test. Returns the captured result for verification.
//
// Usage:
//
//	result := runGuineaPig(suite.T(), func(r *require.Assertions) {
//	    r.Error(nil, "Expected error") // This will fail
//	})
//	suite.True(result.Failed)
//	suite.Contains(result.ErrorMsgs[0], "expected message")
func runGuineaPig(t *testing.T, fn func(r *require.Assertions)) GuineaPigResult {
	interceptor := &TBInterceptor{realT: t}
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer func() { recover() }() // Catch TBInterceptor.FailNow panic

		r := require.New(interceptor)
		fn(r)
	}()

	<-done

	return GuineaPigResult{
		Failed:    interceptor.failed,
		FailedNow: interceptor.failedNow,
		ErrorMsgs: interceptor.errorMsgs,
	}
}

func (suite *FluentLuaTestSuite) TestConsumeLuaError() {
	// GOAL: Verify ConsumeLuaError produces clear error messages when assertions fail
	//
	// TEST SCENARIO: Call ConsumeLuaError with mismatched expectations → fail → verify error message content

	suite.Run("fail on error expected but got nil", func() {
		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			var nilError error = nil
			r.Error(nilError, "Expected Lua runtime error but got none")
		})

		suite.Require().True(result.Failed)
		suite.Require().True(result.FailedNow)
		suite.Require().Contains(result.ErrorMsgs[0], "An error is expected but got nil")
	})

	suite.Run("fail on expected error message mismatch with both actual and expected", func() {
		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			actualError := errors.New("actual error message")
			r.Contains(actualError.Error(), "expected substring", "Lua error should contain expected substring")
		})

		suite.Require().True(result.Failed)
		suite.Require().Contains(result.ErrorMsgs[0], "expected substring")
		suite.Require().Contains(result.ErrorMsgs[0], "actual error message")
	})
}

func (suite *FluentLuaTestSuite) TestConsumeOutput() {
	// GOAL: Verify ConsumeOutput produces unified diff showing actual vs expected
	//
	// TEST SCENARIO: Output mismatch → fail → verify diff contains both values

	suite.Run("fail on output mismatch with unified diff  actual vs expected lines", func() {
		interceptor := &TBInterceptor{realT: suite.T()}

		ta := testutils.NewTextAsserterWithInterface(interceptor).WithOptions(
			testutils.WithTrimSpace(true),
		)
		ta.Assert("actual line 1\nactual line 2", "expected line 1\nexpected line 2")

		suite.Require().True(interceptor.failed)
		suite.Require().NotEmpty(interceptor.errorMsgs)
	})
}

func (suite *FluentLuaTestSuite) TestExecuteScript() {
	suite.Run("fail MustExecuteScript on script execution error", func() {
		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			suite.Connect("1").
				FluentLuaTest().
				WithAssertions(r). // <-- Inject guinea pig assertions
				MustExecuteScript(`nonexistent_function()`)
		})

		suite.Require().True(result.Failed, "MustExecuteScript MUST fail on script error")
		suite.Require().Contains(result.ErrorMsgs[0], "Unexpected Lua runtime error")
	})

	suite.Run("fail FailExecuteScript on no script execution error", func() {
		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			suite.Connect("1").
				FluentLuaTest().
				WithAssertions(r). // <-- Inject guinea pig assertions
				FailExecuteScript("expected error", "local c = 1")
		})

		suite.Require().True(result.Failed)
		suite.Require().NotEmpty(result.ErrorMsgs)
		suite.Require().Contains(result.ErrorMsgs[0], "An error is expected but got nil")
	})

	suite.Run("fail TestCase.script on script execution error", func() {
		// GOAL: Verify MustExecuteScript fails on script runtime error
		//
		// TEST SCENARIO: Execute bad script (no scriptError) → MustExecuteScript fails → "Unexpected Lua runtime error"

		connect := func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("1")
		}

		//connect().FluentLuaTest().MustExecuteScript(`nonexistent_function()`)
		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			executeScenarios(
				connect,
				r,
				TestCase{
					Name:   "script calls nonexistent function",
					Script: `nonexistent_function()`,
				},
			)
		})

		suite.Require().True(result.Failed, "MustExecuteScript MUST fail on script error")
		suite.Require().Contains(result.ErrorMsgs[0], "Unexpected Lua runtime error")
	})

	suite.Run("fail TestCase.script on no script execution error", func() {
		// GOAL: Verify FailExecuteScript error message when script succeeds but error expected
		//
		// TEST SCENARIO: Script succeeds + scriptError expected → fail → "An error is expected but got nil"

		connect := func(tc TestCase) *LuaTestableDevice {
			if tc.Peripheral != nil {
				suite.GivenPeripheral(tc.Peripheral)
			}
			return suite.Connect("1")
		}

		result := runGuineaPig(suite.T(), func(r *require.Assertions) {
			executeScenarios(
				connect,
				r,
				TestCase{
					Name:        "script succeeds but error was expected",
					Script:      `print("Hello World")`,
					ScriptError: "expected error",
				},
			)
		})

		suite.Require().True(result.Failed)
		suite.Require().NotEmpty(result.ErrorMsgs)
		suite.Require().Contains(result.ErrorMsgs[0], "An error is expected but got nil")
	})
}

// TestFluentLuaTestSuite runs the FluentLuaTest API tests
func TestFluentLuaTestSuite(t *testing.T) {
	suitelib.Run(t, new(FluentLuaTestSuite))
}
