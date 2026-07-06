//go:build test

package lua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aarzilli/golua/lua"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/suite"
	suitelib "github.com/stretchr/testify/suite"
)

// LuaEngineTestSuite
type LuaEngineTestSuite struct {
	suite.Suite

	logger *logrus.Logger // Structured logger for test output

	luaEngine        *LuaEngine
	luaOutputCapture *LuaOutputCollector
}

func (suite *LuaEngineTestSuite) SetupSuite() {
	suite.logger = logrus.New()
	suite.logger.SetLevel(logrus.DebugLevel)
}

func (suite *LuaEngineTestSuite) SetupTest() {
	suite.luaEngine = NewLuaEngine(suite.logger)

	// Create the string writer
	if lc, err := NewLuaOutputCollector(suite.luaEngine.OutputChannel(), 100, nil); err != nil {
		suite.NoError(fmt.Errorf("error creating lua output collector: %v", err))
	} else {
		suite.luaOutputCapture = lc
		suite.luaOutputCapture.Start()
	}
}

func (suite *LuaEngineTestSuite) TearDownTest() {
	suite.luaOutputCapture.Stop()
	suite.luaEngine.Close()
}

func (suite *LuaEngineTestSuite) SetupSubTest() {
	if suite.luaEngine != nil {
		suite.luaEngine.Close()
	}
	suite.luaEngine = NewLuaEngine(suite.logger)

	if suite.luaOutputCapture != nil {
		suite.luaOutputCapture.Stop()
	}

	if lc, err := NewLuaOutputCollector(suite.luaEngine.OutputChannel(), 100, nil); err != nil {
		suite.NoError(fmt.Errorf("error creating lua output collector: %v", err))
	} else {
		suite.luaOutputCapture = lc
		suite.luaOutputCapture.Start()
	}
}

func (suite *LuaEngineTestSuite) ExecuteScript(script string) error {
	err := suite.luaEngine.LoadScript(script, "test")
	suite.NoError(err, "Should load subscription script with nio errors")
	err = suite.luaEngine.ExecuteScript(context.Background(), "")
	return err
}

func (suite *LuaEngineTestSuite) TestCapturePrintVariants() {
	// GOAL: Verify print() function captures all argument types with correct formatting
	//
	// TEST SCENARIO: Call print() with various types → verify output format → check tab separators and newline behavior
	cases := []struct {
		name     string
		script   string
		expected *regexp.Regexp
	}{
		{"no args", `print()`, regexp.MustCompile(`^\n$`)},
		{"one string", `print("hello")`, regexp.MustCompile(`^hello\n$`)},
		{"two strings", `print("foo", "bar")`, regexp.MustCompile(`^foo\tbar\n$`)},
		{"number", `print(123)`, regexp.MustCompile(`^123\n$`)},
		{"boolean true", `print(true)`, regexp.MustCompile(`^true\n$`)},
		{"boolean false", `print(false)`, regexp.MustCompile(`^false\n$`)},
		{"nil value", `print(nil)`, regexp.MustCompile(`^nil\n$`)},

		// Mixed types
		{"string + number", `print("val:", 42)`, regexp.MustCompile(`^val:\t42\n$`)},
		{"boolean + string", `print(false, "end")`, regexp.MustCompile(`^false\tend\n$`)},

		// Expressions
		{"addition", `print(1+2)`, regexp.MustCompile(`^3\n$`)},
		{"concat", `print("a" .. "b")`, regexp.MustCompile(`^ab\n$`)},
		{"concat mixed", `print("val=" .. 123)`, regexp.MustCompile(`^val=123\n$`)},

		// Tables
		{"empty table", `print({})`, regexp.MustCompile(`^table: 0x[0-9a-fA-F]+\n$`)},
		{"table with values", `print({x=1, y=2})`, regexp.MustCompile(`^table: 0x[0-9a-fA-F]+\n$`)},

		// Functions and userdata
		{"function ref", `print(function() end)`, regexp.MustCompile(`^function: 0x[0-9a-fA-F]+\n$`)},
		{"coroutine", `print(coroutine.create(function() end))`, regexp.MustCompile(`^thread: 0x[0-9a-fA-F]+\n$`)},

		// Multiple args
		{"string num bool nil", `print("s", 9, true, nil)`, regexp.MustCompile(`^s\t9\ttrue\tnil\n$`)},

		// Newline preservation
		{"string with newline", `print("a\nb")`, regexp.MustCompile(`^a\nb\n$`)},

		// Empty string and spaces
		{"empty string", `print("")`, regexp.MustCompile(`^\n$`)},
		{"whitespace string", `print("   ")`, regexp.MustCompile(`^   \n$`)},
	}

	for _, c := range cases {
		suite.Run(c.name, func() {
			err := suite.ExecuteScript(c.script)
			suite.NoError(err, "Lua code should execute")

			// Give the writer a brief moment to consume the channel
			time.Sleep(10 * time.Millisecond)

			// Get all captured output as a single string
			if got, err := suite.luaOutputCapture.ConsumePlainText(); err != nil {
				suite.NoError(fmt.Errorf("should be able to consume plain text: %v", err))
			} else {
				if !c.expected.MatchString(got) {
					suite.Failf("Output mismatch", "got %q, want match %q", got, c.expected.String())
				}
			}
		})
	}
}

// TestCaptureIOWriteVariants device_test io.write() capture with various argument types
func (suite *LuaEngineTestSuite) TestCaptureIOWriteVariants() {
	// GOAL: Verify io.write() captures all argument types without automatic newlines
	//
	// TEST SCENARIO: Call io.write() with various types → verify concatenated output → check no automatic newlines added
	cases := []struct {
		name     string
		script   string
		expected *regexp.Regexp
	}{
		// Basic io.write device_test - NO automatic newline unlike print
		{"one string", `io.write("hello")`, regexp.MustCompile(`^hello$`)},
		{"two strings", `io.write("foo", "bar")`, regexp.MustCompile(`^foobar$`)},
		{"number", `io.write(123)`, regexp.MustCompile(`^123$`)},
		{"boolean true", `io.write(true)`, regexp.MustCompile(`^true$`)},
		{"boolean false", `io.write(false)`, regexp.MustCompile(`^false$`)},
		{"nil value", `io.write(nil)`, regexp.MustCompile(`^nil$`)},

		// Mixed types - concatenated without separator
		{"string + number", `io.write("val:", 42)`, regexp.MustCompile(`^val:42$`)},
		{"boolean + string", `io.write(false, "end")`, regexp.MustCompile(`^falseend$`)},

		// Expressions
		{"addition", `io.write(1+2)`, regexp.MustCompile(`^3$`)},
		{"concat", `io.write("a" .. "b")`, regexp.MustCompile(`^ab$`)},
		{"concat mixed", `io.write("val=" .. 123)`, regexp.MustCompile(`^val=123$`)},

		// Manual newlines
		{"with newline", `io.write("hello\n")`, regexp.MustCompile(`^hello\n$`)},
		{"multiple lines", `io.write("line1\nline2\n")`, regexp.MustCompile(`^line1\nline2\n$`)},

		// Empty string and spaces
		{"empty string", `io.write("")`, regexp.MustCompile(`^$`)},
		{"whitespace string", `io.write("   ")`, regexp.MustCompile(`^   $`)},

		// Multiple io.write calls
		{"multiple calls", `io.write("a"); io.write("b")`, regexp.MustCompile(`^ab$`)},
		{"multiple calls with newlines", `io.write("a\n"); io.write("b\n")`, regexp.MustCompile(`^a\nb\n$`)},
	}

	for _, c := range cases {
		suite.Run(c.name, func() {
			err := suite.ExecuteScript(c.script)
			suite.NoError(err, "Lua code should execute")

			// Give the writer a brief moment to consume the channel
			time.Sleep(10 * time.Millisecond)

			// Get all captured output as a single string
			if got, err := suite.luaOutputCapture.ConsumePlainText(); err != nil {
				suite.NoError(fmt.Errorf("should be able to consume plain text: %v", err))
			} else {
				if !c.expected.MatchString(got) {
					suite.Failf("Output mismatch", "got %q, want match %q", got, c.expected.String())
				}
			}
		})
	}
}

// TestCaptureIOStderrWriteVariants device_test io.stderr:write() capture with various argument types
func (suite *LuaEngineTestSuite) TestCaptureIOStderrWriteVariants() {
	// GOAL: Verify that io.stderr:write() captures stderr output correctly with various argument types
	//
	// TEST SCENARIO: Call io.stderr:write() with different arguments → output captured to stderr → verify content

	cases := []struct {
		name           string
		script         string
		expectedStderr string
	}{
		// Basic io.stderr:write device_test - NO automatic newline
		{"one string", `io.stderr:write("error message")`, "error message"},
		{"two strings", `io.stderr:write("error: ", "failed")`, "error: failed"},
		{"number", `io.stderr:write(500)`, "500"},
		{"boolean true", `io.stderr:write(true)`, "true"},
		{"boolean false", `io.stderr:write(false)`, "false"},
		{"nil value", `io.stderr:write(nil)`, "nil"},

		// Mixed types - concatenated without separator
		{"string + number", `io.stderr:write("code: ", 404)`, "code: 404"},
		{"boolean + string", `io.stderr:write(false, " result")`, "false result"},

		// Manual newlines
		{"with newline", `io.stderr:write("error\n")`, "error\n"},
		{"multiple lines", `io.stderr:write("err1\nerr2\n")`, "err1\nerr2\n"},

		// Empty string and spaces
		{"empty string", `io.stderr:write("")`, ""},
		{"whitespace string", `io.stderr:write("   ")`, "   "},

		// Multiple io.stderr:write calls
		{"multiple calls", `io.stderr:write("a"); io.stderr:write("b")`, "ab"},
		{"multiple calls with newlines", `io.stderr:write("a\n"); io.stderr:write("b\n")`, "a\nb\n"},
	}

	for _, c := range cases {
		suite.Run(c.name, func() {
			err := suite.ExecuteScript(c.script)
			suite.NoError(err, "Lua code should execute")

			time.Sleep(10 * time.Millisecond)

			// Consume records and separate stdout/stderr
			var stderrContent strings.Builder
			consumer := func(record *LuaOutputRecord) (string, error) {
				if record == nil {
					return stderrContent.String(), nil
				}
				if record.Source == "stderr" {
					stderrContent.WriteString(record.Content)
				}
				return "", nil
			}

			got, err := ConsumeRecords(suite.luaOutputCapture, consumer)
			suite.NoError(err, "should be able to consume records")
			suite.Equal(c.expectedStderr, got, "stderr content should match")
		})
	}
}

// TestCaptureMixedPrintAndIOWrite device_test that print() and io.write() can be mixed
func (suite *LuaEngineTestSuite) TestCaptureMixedPrintAndIOWrite() {
	// GOAL: Verify print() and io.write() can be interleaved and preserve order
	//
	// TEST SCENARIO: Mix io.write() and print() calls → verify correct output order → check newline behavior preserved
	script := `
		io.write("line1")
		print("line2")
		io.write("line3\n")
	`
	err := suite.ExecuteScript(script)
	suite.NoError(err, "Lua code should execute")

	time.Sleep(10 * time.Millisecond)

	got, err := suite.luaOutputCapture.ConsumePlainText()
	suite.NoError(err, "should be able to consume plain text")

	// Expected: "line1" + "line2\n" + "line3\n"
	expected := "line1line2\nline3\n"
	suite.Equal(expected, got, "Mixed print and io.write should preserve order and newline behavior")
}

// TestJSONLibraryAvailability device_test if the JSON library is properly loaded
func (suite *LuaEngineTestSuite) TestJSONLibraryAvailability() {
	// GOAL: Verify embedded JSON library is available and functional in Lua environment
	//
	// TEST SCENARIO: Require json module → encode table to JSON → verify valid JSON output
	suite.NotEmpty(jsonLua, "Embedded JSON library should not be empty")

	// Test JSON library availability
	jsonTestScript := `
		-- Try to require the module
		local json = require("json")
		test_obj = {test = "hello", number = 42}
		json_string = json.encode(test_obj)
		print(json_string)
	`
	err := suite.ExecuteScript(jsonTestScript)
	suite.NoError(err, "JSON library should be available and working")

	// Allow some time for processing
	time.Sleep(10 * time.Millisecond)

	// Get captured JSON output
	if output, err := suite.luaOutputCapture.ConsumePlainText(); err != nil {
		suite.NoError(fmt.Errorf("should be able to consume plain text: %v", err))
	} else {

		suite.NotEmpty(output, "Should have captured JSON output")

		// Parse the JSON output to verify it's valid
		var jsonData struct {
			Test   string `json:"test"`
			Number int    `json:"number"`
		}

		err = json.Unmarshal([]byte(strings.TrimSpace(output)), &jsonData)
		suite.NoError(err, "Should be able to parse JSON output: %s", output)

		suite.Equal("hello", jsonData.Test, "JSON should contain correct test field")
		suite.Equal(42, jsonData.Number, "JSON should contain correct number field")
	}
}

func (suite *LuaEngineTestSuite) TestExecuteScript2_ContextCancellation() {
	suite.Run("CancellationInfiniteLoop", func() {
		script := `
			local count = 0
			while true do
				count = count + 1
			end
		`

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		//err := suite.luaEngine.ExecuteScript2(ctx, script, nil)
		err := suite.luaEngine.ExecuteScript(ctx, script)
		suite.Error(err, "Should return error on cancellation")
		suite.True(context.Canceled == err, "Error should be context.Canceled")
	})

	suite.Run("CompletesBeforeTimeout", func() {
		script := `print("Quick execution")`

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		//err := suite.luaEngine.ExecuteScript2(ctx, script, nil)
		err := suite.luaEngine.ExecuteScript(ctx, script)
		suite.NoError(err, "Should complete successfully before timeout")
	})

	suite.Run("AlreadyCancelled", func() {
		script := `print("Should not run")`

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel before execution

		//err := suite.luaEngine.ExecuteScript2(ctx, script, nil)
		err := suite.luaEngine.ExecuteScript(ctx, script)
		suite.Error(err, "Should fail with already cancelled context")
		suite.True(context.Canceled == err, "Should return context.Canceled")
	})
}

// TestExecuteScript2_CancellationTimeout verifies that safeExecuteScript does not wait forever for a
// script that ignores cancellation: after scriptCancellationTimeout it returns a diagnostic error
// instead of hanging (issue #4 residual case — a pure JIT/FFI loop has no cancellable boundary and
// the LuaJIT count hook never fires inside a compiled trace).
func (suite *LuaEngineTestSuite) TestExecuteScript2_CancellationTimeout() {
	// GOAL: The post-cancellation wait is bounded — an unstoppable script yields a timeout error
	//       within the grace period rather than an unbounded hang.
	//
	// TEST SCENARIO: Script blocks ignoring the context → context cancelled → ExecuteScript returns a
	//                grace-period error within the (shortened) timeout, well before the block finishes

	e := suite.luaEngine
	e.scriptCancellationTimeout = 150 * time.Millisecond

	// Register a test-only blocker that sleeps ignoring the context. It stands in for an
	// uncancellable JIT/FFI loop but finishes on its own, so it neither depends on JIT warmup nor
	// leaks a goroutine forever.
	e.DoWithState(func(L *lua.State) interface{} {
		L.PushGoFunction(func(L *lua.State) int {
			time.Sleep(time.Duration(L.ToInteger(1)) * time.Millisecond)
			return 0
		})
		L.SetGlobal("_test_block_ignoring_cancel")
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	// The blocker sleeps 1000ms ignoring the context; the grace timeout is 150ms.
	err := e.ExecuteScript(ctx, `_test_block_ignoring_cancel(1000)`)

	// The bounded-timeout branch is the only path that yields a grace-period timeout error, so its
	// presence proves both facts at once: the wait was bounded (it did not block for the full 1000ms
	// script) and the forced, ungraceful stop is diagnosable. On the unfixed code the wait is
	// unbounded — ExecuteScript only returns after the whole block with a plain context.Canceled.
	// Report in plain language instead of testify's opaque "does not contain" output.
	if !errors.Is(err, ErrScriptCancellationTimeout) {
		suite.Failf("script ignoring cancellation was not stopped after the grace period",
			"got: %v (expected ErrScriptCancellationTimeout)", err)
	}
}

func (suite *LuaEngineTestSuite) TestExecuteScript_BlockedFunctions() {
	blockedFunctions := map[string]string{
		"os.execute": `os.execute("echo test")`,
		"os.exit":    `os.exit(0)`,
		"os.remove":  `os.remove("file.txt")`,
		"os.rename":  `os.rename("old.txt", "new.txt")`,
		// "io.read" is allowed for interactive bridge scripts
		"io.lines": `io.lines("file.txt")`,
		//"require":    `require("module")`,
		"dofile":   `dofile("script.lua")`,
		"loadfile": `loadfile("script.lua")`,
	}

	for name, script := range blockedFunctions {
		suite.Run(name, func() {
			//err := suite.luaEngine.ExecuteScript2(context.Background(), script, nil)
			err := suite.luaEngine.ExecuteScript(context.Background(), script)
			suite.Error(err, "Should error on blocked function: "+name)
			suite.Contains(err.Error(), "is blocked", "Error should mention function is blocked")
		})
	}
}

// TestSafeWrapGoFunction device_test that SafeWrapGoFunction properly handles panics
func (suite *LuaEngineTestSuite) TestSafeWrapGoFunction() {
	suite.Run("ExpectedLuaError_PropagatesCorrectly", func() {
		// Register a Go function that raises a Lua error
		suite.luaEngine.DoWithState(func(L *lua.State) interface{} {
			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("test_expected_error", func(L *lua.State) int {
				L.RaiseError("expected error message")
				return 0
			}))
			L.SetGlobal("test_expected_error")
			return nil
		})

		// Execute script that calls the function
		script := `test_expected_error()`
		err := suite.luaEngine.ExecuteScript(context.Background(), script)

		// Should get a Lua error with the original message
		suite.Error(err, "Should return error from L.RaiseError")
		suite.Contains(err.Error(), "expected error message", "Error message should propagate correctly")
	})

	suite.Run("UnexpectedPanic_ConvertedToLuaError", func() {
		// Reset engine for this sub-test
		suite.luaEngine.Reset()

		// Register a Go function that panics with a non-Lua error
		suite.luaEngine.DoWithState(func(L *lua.State) interface{} {
			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("test_unexpected_panic", func(L *lua.State) int {
				panic("unexpected Go panic")
			}))
			L.SetGlobal("test_unexpected_panic")
			return nil
		})

		// Execute script that calls the function
		script := `test_unexpected_panic()`
		err := suite.luaEngine.ExecuteScript(context.Background(), script)

		// Should get a Lua error indicating panic in Go
		suite.Error(err, "Should return error from unexpected panic")
		suite.Contains(err.Error(), "panicked in Go", "Error should indicate panic in Go function")
	})

	suite.Run("NormalExecution_WorksCorrectly", func() {
		// Register a Go function that works normally
		suite.luaEngine.DoWithState(func(L *lua.State) interface{} {
			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("test_normal", func(L *lua.State) int {
				L.PushString("success")
				return 1
			}))
			L.SetGlobal("test_normal")
			return nil
		})

		// Execute script that calls the function
		script := `
			result = test_normal()
			print(result)
		`
		err := suite.luaEngine.ExecuteScript(context.Background(), script)

		// Should execute successfully
		suite.NoError(err, "Normal function should execute without error")

		time.Sleep(10 * time.Millisecond)
		output, _ := suite.luaOutputCapture.ConsumePlainText()
		suite.Contains(output, "success", "Function should return correct value")
	})

	suite.Run("StructPanic_ConvertedToLuaError", func() {
		type CustomError struct {
			Message string
		}

		// Reset engine for this sub-test
		suite.luaEngine.Reset()

		// Register a Go function that panics with a struct
		suite.luaEngine.DoWithState(func(L *lua.State) interface{} {
			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("test_struct_panic", func(L *lua.State) int {
				panic(CustomError{Message: "custom error"})
			}))
			L.SetGlobal("test_struct_panic")
			return nil
		})

		// Execute script that calls the function
		script := `test_struct_panic()`
		err := suite.luaEngine.ExecuteScript(context.Background(), script)

		// Should get a Lua error indicating panic in Go
		suite.Error(err, "Should return error from struct panic")
		suite.Contains(err.Error(), "panicked in Go", "Error should indicate panic in Go function")
	})

	suite.Run("MultipleWrappedFunctions_IndependentErrorHandling", func() {
		// Register two functions - one that errors, one that works
		suite.luaEngine.DoWithState(func(L *lua.State) interface{} {
			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("good_func", func(L *lua.State) int {
				L.PushString("good")
				return 1
			}))
			L.SetGlobal("good_func")

			L.PushGoFunction(suite.luaEngine.SafeWrapGoFunction("bad_func", func(L *lua.State) int {
				L.RaiseError("bad function error")
				return 0
			}))
			L.SetGlobal("bad_func")
			return nil
		})

		// Test good function works
		script1 := `print(good_func())`
		err1 := suite.luaEngine.ExecuteScript(context.Background(), script1)
		suite.NoError(err1, "Good function should work")

		// Test bad function errors correctly
		script2 := `bad_func()`
		err2 := suite.luaEngine.ExecuteScript(context.Background(), script2)
		suite.Error(err2, "Bad function should error")
		suite.Contains(err2.Error(), "bad function error", "Error should have correct message")
	})
}

// TestLuaEngineTestSuite runs the test suite using testify/suite
func TestLuaEngineTestSuite(t *testing.T) {
	suitelib.Run(t, new(LuaEngineTestSuite))
}
