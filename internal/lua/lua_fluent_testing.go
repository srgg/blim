//go:build test

package lua

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/cornelk/hashmap"
	"github.com/sirupsen/logrus"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// LuaTestableDevice wraps a TestableDevice with Lua-specific functionality.
type LuaTestableDevice struct {
	*testutils.GenericTestableDevice
	t      *testing.T   // Captured at creation for cleanup assertions
	suite  *suite.Suite // For GenericFluentTest compatibility
	logger *logrus.Logger

	fluentLuaTest *FluentLuaTest
}

// NewLuaTestableDevice creates a LuaTestableDevice from a TestableDevice.
func NewLuaTestableDevice(t *testing.T, s *suite.Suite, logger *logrus.Logger, gtd *testutils.GenericTestableDevice) *LuaTestableDevice {
	return &LuaTestableDevice{
		GenericTestableDevice: gtd,
		t:                     t,
		suite:                 s,
		logger:                logger,
	}
}

// FluentLuaTest returns a FluentLuaTest for fluent-style testing.
// Lazily created on the first call.
func (d *LuaTestableDevice) FluentLuaTest() *FluentLuaTest {
	if d.fluentLuaTest == nil {
		d.fluentLuaTest = newFluentLuaTest(d)
	}
	return d.fluentLuaTest
}

// cleanup returns a cleanup function that closes LuaAPI resources.
func (d *LuaTestableDevice) cleanup() func() {
	return func() {
		if d.fluentLuaTest != nil {
			d.fluentLuaTest.Close()
		}
	}
}

// Channel constants for output collection.
const (
	ChannelStdout = "stdout"
	ChannelStderr = "stderr"
)

// ScriptExecutionContext tracks a single script execution and its results.
type ScriptExecutionContext struct {
	script       string
	executionErr error
	collector    *LuaOutputCollector

	// Cached output records by channel (appended as consumed from collector)
	// Keys: "stdout", "stderr", "pty", etc.
	lines *hashmap.Map[string, []string]
}

// ensureLines initializes the lines map if nil.
func (c *ScriptExecutionContext) ensureLines() {
	if c.lines == nil {
		c.lines = hashmap.New[string, []string]()
	}
}

// appendLine appends content to a channel's cached lines.
func (c *ScriptExecutionContext) appendLine(channel, content string) {
	c.ensureLines()
	existing, _ := c.lines.Get(channel)
	c.lines.Set(channel, append(existing, content))
}

// getLines returns the lines for a channel.
func (c *ScriptExecutionContext) getLines(channel string) []string {
	if c.lines == nil {
		return nil
	}
	lines, _ := c.lines.Get(channel)
	return lines
}

// clearChannel removes all cached content for a channel.
func (c *ScriptExecutionContext) clearChannel(channel string) {
	if c.lines != nil {
		c.lines.Del(channel)
	}
}

// drain drains the collector into cached lines.
func (c *ScriptExecutionContext) drain() {
	if c.collector == nil {
		return
	}
	_ = c.collector.Flush(500 * time.Millisecond)
	consumer := func(record *LuaOutputRecord) (struct{}, error) {
		if record == nil {
			return struct{}{}, nil
		}
		c.appendLine(record.Source, record.Content)
		return struct{}{}, nil
	}
	ConsumeRecords(c.collector, consumer)
}

// ConsumeAll returns joined content for a channel and clears it.
func (c *ScriptExecutionContext) ConsumeAll(channel string) string {
	c.drain()
	content := strings.Join(c.getLines(channel), "")
	c.clearChannel(channel)
	return content
}

// ConsumeLines returns raw lines for a channel and clears it.
func (c *ScriptExecutionContext) ConsumeLines(channel string) []string {
	c.drain()
	lines := c.getLines(channel)
	c.clearChannel(channel)
	return lines
}

func formatLines(lines []string) string {
	if len(lines) == 0 {
		return "<no lines captured>"
	}

	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "  %3d | %s\n", i, line)
	}
	return b.String()
}

// ConsumeLine finds and removes the first line in the given channel
// that contains the expected substring.
// Returns the matched line or an error if no match is found.
func (c *ScriptExecutionContext) ConsumeLine(channel, expected string) (string, error) {
	c.drain()

	lines := c.getLines(channel)
	for i, line := range lines {
		if strings.Contains(line, expected) {
			lines = append(lines[:i], lines[i+1:]...)
			c.lines.Set(channel, lines)
			return line, nil
		}
	}

	return "", fmt.Errorf(
		"failed to consume line: expected substring %q not found in channel %q\ncaptured lines:\n%s",
		expected,
		channel,
		formatLines(lines),
	)
}

// GenericLuaFluentTest provides Lua testing capabilities with Self-typed chaining.
// Self type parameter enables subclasses (like FluentBridgeTest) to return their own type from methods.
//
// Embeds GenericFluentTest to inherit device-level testing methods:
//   - Sleep(duration)
//   - EmmitData(fn)
//   - Subscribe(), SubscribeOnEach(), SubscribeBatched(), SubscribeAggregated()
//   - CancelSubscription(), WaitSubscription()
//   - ConsumeSubscription(), ConsumeEmptySubscription()
type GenericLuaFluentTest[Self any] struct {
	*testutils.GenericFluentTest[Self]

	device *LuaTestableDevice

	// Lazily created LuaAPI (shared across script executions)
	luaApi *LuaAPI

	// Current script execution context (nil = no pending execution)
	current *ScriptExecutionContext

	// Mutex protecting concurrent access to current (especially executionErr)
	// Required because bridge tests run in background goroutines that may
	// write to current.executionErr while the main test reads it.
	currentMu sync.Mutex

	// Optional assertions override for guinea pig testing pattern
	assertions *require.Assertions

	// Self reference for method chaining (set by constructor)
	selfRef Self
}

// FluentLuaTest is the concrete instantiation of GenericLuaFluentTest for direct use.
// Lua API tests use this type. Bridge tests use FluentBridgeTest which also embeds GenericLuaFluentTest.
type FluentLuaTest struct {
	*GenericLuaFluentTest[*FluentLuaTest]
}

// NewGenericLuaFluentTest creates a GenericLuaFluentTest with the given self reference.
// Used by FluentBridgeTest and other types that embed GenericLuaFluentTest.
func NewGenericLuaFluentTest[Self any](d *LuaTestableDevice, self Self) *GenericLuaFluentTest[Self] {
	glt := &GenericLuaFluentTest[Self]{
		device:  d,
		selfRef: self,
	}

	// Wire up GenericFluentTest with self-reference
	glt.GenericFluentTest = testutils.NewGenericFluentTest(
		self,
		d.suite,
		d.Device,
	)

	return glt
}

// newFluentLuaTest creates a FluentLuaTest referencing the LuaTestableDevice.
func newFluentLuaTest(d *LuaTestableDevice) *FluentLuaTest {
	flt := &FluentLuaTest{}
	flt.GenericLuaFluentTest = NewGenericLuaFluentTest(d, flt)
	return flt
}

// suite returns the test suite for assertions.
func (f *GenericLuaFluentTest[Self]) suite() *suite.Suite {
	return f.device.suite
}

// Require returns assertions - uses injected assertions if set, otherwise suite's default.
// This enables guinea pig testing pattern where assertions can be intercepted.
func (f *GenericLuaFluentTest[Self]) Require() *require.Assertions {
	if f.assertions != nil {
		return f.assertions
	}
	return f.suite().Require()
}

// WithAssertions injects custom assertions for guinea pig testing pattern.
// Pass require.New(interceptor) to capture failures without propagating them.
func (f *GenericLuaFluentTest[Self]) WithAssertions(r *require.Assertions) Self {
	f.assertions = r
	return f.selfRef
}

// isGuineaPigMode returns true when custom assertions have been injected.
//
// "Guinea pig" testing pattern: A parent test deliberately runs code expected to fail,
// using a TBInterceptor to capture failures WITHOUT propagating them to Go's test framework.
// This allows the parent test to verify the failure message content (e.g., "does the error
// message correctly describe what went wrong?").
//
// When in guinea pig mode:
//   - Assertion failures are captured by TBInterceptor, not the real *testing.T
//   - The real test hasn't "failed" from Go's perspective
//   - Cleanup code should NOT report additional errors to the real *testing.T
//   - The parent test will inspect captured failures and make its own assertions
func (f *GenericLuaFluentTest[Self]) isGuineaPigMode() bool {
	return f.assertions != nil
}

// ensureLuaAPI lazily creates the LuaAPI.
func (f *GenericLuaFluentTest[Self]) ensureLuaAPI() {
	if f.luaApi == nil {
		f.luaApi = NewBLEAPI2(f.device.Device, f.device.logger)
	}
}

// LuaAPI returns the LuaAPI instance, creating it if needed.
func (f *GenericLuaFluentTest[Self]) LuaAPI() *LuaAPI {
	f.ensureLuaAPI()
	return f.luaApi
}

// Logger returns the logger instance.
func (f *GenericLuaFluentTest[Self]) Logger() *logrus.Logger {
	return f.device.logger
}

// Device returns the underlying LuaTestableDevice.
func (f *GenericLuaFluentTest[Self]) Device() *LuaTestableDevice {
	return f.device
}

// Collector returns the current context's output collector.
func (f *GenericLuaFluentTest[Self]) Collector() *LuaOutputCollector {
	if f.current == nil {
		return nil
	}
	return f.current.collector
}

// Close cleans up resources and fails if there's an unhandled execution context.
func (f *GenericLuaFluentTest[Self]) Close() {
	// Check for unhandled context - will fail test if unconsumed data exists
	f.CheckUnconsumedData("Close() called with unhandled script execution")

	// Always stop collector on close (prevents leak when data was consumed but test ends)
	f.clearContext()

	if f.luaApi != nil {
		f.luaApi.Close()
	}
}

// ExecuteScript executes a Lua script and creates a new execution context.
// Parse/load errors fail immediately. Runtime errors are collected for ConsumeLuaError.
//
// Supports two script formats:
//   - Inline Lua code: executed directly
//   - file:// URL: loads script from file, query params become arg[] table
//     Example: "file:///internal/lua/test-scenarios/test.lua?format=json&verbose=true"
//
// IMPORTANT: Each execution MUST be handled via ConsumeLuaError, ExpectNoError, or Ignore
// BEFORE calling ExecuteScript again. Unhandled context fails with dump of unconsumed data.
func (f *GenericLuaFluentTest[Self]) ExecuteScript(script string) Self {
	// Fail if previous context has unconsumed data
	f.CheckUnconsumedData("ExecuteScript called with unhandled previous execution")

	// Stop old collector before creating new one (prevents leak when data was consumed)
	if f.current != nil && f.current.collector != nil {
		f.current.collector.Stop()
		f.current = nil
	}

	f.Require().NotEmpty(script, "Script must not be empty")
	f.ensureLuaAPI()

	// Resolve script content and args from file:// URL or inline script
	scriptContent, args := f.resolveScript(script)

	// Create new context with its own collector
	collector, err := NewLuaOutputCollector(f.luaApi.OutputChannel(), 100, nil)
	if err != nil {
		panic(fmt.Sprintf("failed to create LuaOutputCollector: %v", err))
	}
	collector.Start()

	f.current = &ScriptExecutionContext{
		script:    scriptContent,
		collector: collector,
	}

	// Execute via script_executor (handles arg table, load, and execution)
	f.current.executionErr = ExecuteDeviceScriptWithOutput(
		context.Background(),
		nil,             // dev - not used when luaAPI provided
		f.luaApi,        // luaAPI - reuse existing instance
		f.device.logger, // logger
		scriptContent,   // script content (loaded from file or inline)
		args,            // args from URL query params (or nil for inline)
		nil,             // stdout - collector already reading from OutputChannel()
		nil,             // stderr - collector already reading from OutputChannel()
		0,               // characteristicReadTimeout - use defaults
		0,               // characteristicWriteTimeout - use defaults
		nil,             // opts - no script path config
	)

	return f.selfRef
}

// SetScriptExecutionContext sets up the script execution context for ConsumeStdout/ConsumeLuaError.
// Used by MustBridgeRun which runs scripts via RunCliBridge instead of ExecuteScript.
func (f *GenericLuaFluentTest[Self]) SetScriptExecutionContext(script string, collector *LuaOutputCollector) {
	f.current = &ScriptExecutionContext{
		script:    script,
		collector: collector,
	}
}

// SetScriptExecutionError stores the execution error for ConsumeLuaError to check.
// Used after SetScriptExecutionContext when the script is run externally (e.g., via RunCliBridge).
// Safe to call after Close() - logs warning if context was already cleared.
// Thread-safe: protected by currentMu.
func (f *GenericLuaFluentTest[Self]) SetScriptExecutionError(err error) {
	f.currentMu.Lock()
	defer f.currentMu.Unlock()

	if f.current == nil {
		f.device.logger.Warnf("SetScriptExecutionError called after context cleared (err=%v)", err)
		return
	}
	f.current.executionErr = err
}

// ExecuteScriptWithCollector executes a script using an existing collector.
// Used when output is already being routed to the collector (e.g., via a drainer in bridge mode).
// Unlike ExecuteScript, this does NOT create a new collector - it reuses the provided one.
func (f *GenericLuaFluentTest[Self]) ExecuteScriptWithCollector(ctx context.Context, script string, collector *LuaOutputCollector) Self {
	// Fail if previous context has unconsumed data
	f.CheckUnconsumedData("ExecuteScriptWithCollector called with unhandled previous execution")

	f.Require().NotEmpty(script, "Script must not be empty")
	f.Require().NotNil(collector, "Collector must not be nil")
	f.ensureLuaAPI()

	// Resolve script content (ignore args - bridge scripts are inline)
	scriptContent, _ := f.resolveScript(script)

	// Set up context with the provided collector
	f.current = &ScriptExecutionContext{
		script:    scriptContent,
		collector: collector,
	}

	// Execute via LuaAPI directly (output flows through existing drainer → collector)
	f.current.executionErr = f.luaApi.ExecuteScript(ctx, scriptContent)

	return f.selfRef
}

// resolveScript resolves a script string to content and args.
//
// Supports two formats:
//   - Inline Lua code: returned as-is with nil args
//   - file:// URL: loads content from file, query params become args map
//
// Example file:// URL:
//
//	"file:///internal/lua/test-scenarios/test.lua?format=json&verbose=true"
//	→ content: <file contents>
//	→ args: {"format": "json", "verbose": "true"}
func (f *GenericLuaFluentTest[Self]) resolveScript(script string) (content string, args map[string]string) {
	content, args, err := ResolveScript(script)
	if err != nil {
		f.Require().NoError(err, "Failed to resolve script: %s", script)
		return "", nil
	}
	return content, args
}

// MustExecuteScript executes a Lua script and asserts no runtime error occurred.
// Convenience wrapper for ExecuteScript + AssertNoLuaScriptExecutionError.
func (f *GenericLuaFluentTest[Self]) MustExecuteScript(script string) Self {
	f.ExecuteScript(script)
	return f.AssertNoLuaScriptExecutionError()
}

// FailExecuteScript executes a Lua script and asserts it fails with expected error.
// Convenience wrapper for ExecuteScript + ConsumeLuaError.
func (f *GenericLuaFluentTest[Self]) FailExecuteScript(expectedError, script string) Self {
	f.ExecuteScript(script)
	return f.ConsumeLuaError(expectedError)
}

// extractLuaError attempts to extract a *LuaError from a wrapped error chain.
func extractLuaError(err error) *LuaError {
	if err == nil {
		return nil
	}
	var luaErr *LuaError
	if errors.As(err, &luaErr) {
		return luaErr
	}
	return nil
}

// CheckUnconsumedData checks if the current context has unconsumed error or output.
// If unconsumed data exists, fails the test with a dump and returns true.
// Skips dump if test already failed (e.g., assertion failed before cleanup).
func (f *GenericLuaFluentTest[Self]) CheckUnconsumedData(reason string) bool {
	if f.current == nil {
		return false
	}

	// Skip dump if test already failed. This happens when an assertion (e.g.,
	// AssertNoLuaScriptExecutionError) fails but cleanup still runs. The error
	// was already reported by the assertion, so dumping again would be redundant.
	if f.device.t.Failed() {
		f.clearContext()
		return false
	}

	// In guinea pig mode, skip unconsumed data warnings (see isGuineaPigMode() docs).
	if f.isGuineaPigMode() {
		f.clearContext()
		return false
	}

	ctx := f.current

	// Drain any remaining collector data into cached arrays
	ctx.drain()

	// Check if any channel has unconsumed data
	hasUnconsumedLines := false
	if ctx.lines != nil {
		ctx.lines.Range(func(channel string, lines []string) bool {
			if len(lines) > 0 {
				hasUnconsumedLines = true
				return false // stop iteration
			}
			return true
		})
	}
	hasUnconsumed := ctx.executionErr != nil || hasUnconsumedLines

	if !hasUnconsumed {
		return false
	}

	var msg strings.Builder
	msg.WriteString("\n")
	msg.WriteString("╔══════════════════════════════════════════════════════════════════════════════╗\n")
	if strings.Contains(reason, "Close()") {
		msg.WriteString("║                     TEST CLEANUP: UNCONSUMED DATA                            ║\n")
		msg.WriteString("║              Script output/errors were not consumed before test ended        ║\n")
	} else {
		msg.WriteString("║                     LUA SCRIPT EXECUTION BLOCKED                             ║\n")
		msg.WriteString("║              Unconsumed data from previous script execution                  ║\n")
	}
	msg.WriteString("║                                                                              ║\n")
	msg.WriteString(fmt.Sprintf("║  ➤ %-73s ║\n", reason))
	msg.WriteString("╚══════════════════════════════════════════════════════════════════════════════╝\n")

	msg.WriteString("│ SCRIPT:\n")
	msg.WriteString(ctx.script)
	if !strings.HasSuffix(ctx.script, "\n") {
		msg.WriteString("\n")
	}

	if ctx.executionErr != nil {
		msg.WriteString("│ UNCONSUMED SCRIPT EXECUTION ERROR:\n")
		if luaErr := extractLuaError(ctx.executionErr); luaErr != nil {
			msg.WriteString(fmt.Sprintf("  Type: %s\n", luaErr.Type))
			msg.WriteString(fmt.Sprintf("  Message: %s\n", luaErr.Message))
			if luaErr.Line > 0 {
				msg.WriteString(fmt.Sprintf("  Line: %d\n", luaErr.Line))
			}
			if luaErr.Source != "" {
				msg.WriteString(fmt.Sprintf("  Source: %s\n", luaErr.Source))
			}
			if luaErr.StackTrace != "" {
				msg.WriteString(fmt.Sprintf("  Stack:\n%s\n", luaErr.StackTrace))
			}
		} else {
			msg.WriteString(fmt.Sprintf("  %s\n", ctx.executionErr.Error()))
		}
	}

	// Report each channel's unconsumed data
	if ctx.lines != nil {
		ctx.lines.Range(func(channel string, lines []string) bool {
			if len(lines) > 0 {
				msg.WriteString(fmt.Sprintf("│ UNCONSUMED %s:\n", strings.ToUpper(channel)))
				msg.WriteString(strings.Join(lines, ""))
			}
			return true
		})
	}

	msg.WriteString("\n════════════════════════════════════════════════════════════════════════════════\n")

	if ctx.collector != nil {
		ctx.collector.Stop()
	}
	f.current = nil
	f.device.t.Fatalf("%s", msg.String())
	return true
}

// ConsumeLuaError validates that script execution produced an error containing expected substring.
// Thread-safe: protected by currentMu for accessing executionErr.
func (f *GenericLuaFluentTest[Self]) ConsumeLuaError(expectedError string) Self {
	f.currentMu.Lock()
	ctx := f.current
	var execErr error
	if ctx != nil {
		execErr = ctx.executionErr
	}
	f.currentMu.Unlock()

	f.Require().NotNil(ctx, "No script execution to consume")
	f.Require().Error(execErr, "Expected Lua runtime error but got none")
	f.Require().Contains(execErr.Error(), expectedError, "Lua error should contain expected substring")

	// Also consume stderr (Lua engine writes errors there)
	f.ConsumeStderrAsLuaError(expectedError)

	// Clear the error after consuming
	f.currentMu.Lock()
	if f.current != nil {
		f.current.executionErr = nil
	}
	f.currentMu.Unlock()

	return f.selfRef
}

// ConsumeStderrAsLuaError finds stderr line containing expected Lua error, removes it, returns self.
// Use for async callback errors which are sent to stderr, not executionErr.
func (f *GenericLuaFluentTest[Self]) ConsumeStderrAsLuaError(expected string) Self {
	f.Require().NotNil(f.current, "No script execution context")

	_, err := f.current.ConsumeLine(ChannelStderr, expected)
	f.Require().NoError(err, "Expected %s substring %q not found", ChannelStderr, expected)

	return f.selfRef
}

// ConsumeChannel returns joined content for a channel and clears it.
// Use for custom channels (e.g., PTY in bridge tests).
func (f *GenericLuaFluentTest[Self]) ConsumeChannel(channel string) string {
	f.Require().NotNil(f.current, "No script execution context")
	return f.current.ConsumeAll(channel)
}

// ConsumeStdout asserts stdout matches expected content exactly, then clears it.
func (f *GenericLuaFluentTest[Self]) ConsumeStdout(expected string) Self {
	f.Require().NotNil(f.current, "No script execution context")

	actual := f.current.ConsumeAll(ChannelStdout)
	f.Require().Equal(expected, actual, "%s mismatch", ChannelStdout)

	return f.selfRef
}

// ConsumeStdoutTemplate asserts stdout matches expected template after rendering, then clears it.
// Uses text/template to render the expected string with provided data before comparison.
//
// Example:
//
//	ft.ConsumeStdoutTemplate(`Device: {{.DeviceAddress}}`, map[string]interface{}{"DeviceAddress": "00:11:22"})
func (f *GenericLuaFluentTest[Self]) ConsumeStdoutTemplate(expectedTemplate string, templateData map[string]interface{}) Self {
	f.Require().NotNil(f.current, "No script execution context")

	actual := f.current.ConsumeAll(ChannelStdout)

	// Use TextAsserter for template-based comparison
	ta := testutils.NewTextAsserter(f.device.t).WithOptions(
		testutils.WithTrimSpace(true),
		testutils.WithIgnoreEmptyLines(true),
	)
	ta.AssertWithTemplate(actual, expectedTemplate, templateData)

	return f.selfRef
}

// ConsumeStdoutTrimmed asserts stdout matches expected after trimming each line.
// Handles Go raw string indentation by trimming leading/trailing whitespace per line.
func (f *GenericLuaFluentTest[Self]) ConsumeStdoutTrimmed(expected string) Self {
	f.Require().NotNil(f.current, "No script execution context")

	// Trim each actual line
	var actualLines []string
	for _, line := range f.current.ConsumeLines(ChannelStdout) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			actualLines = append(actualLines, trimmed)
		}
	}

	// Trim each expected line
	var expectedLines []string
	for _, line := range strings.Split(expected, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			expectedLines = append(expectedLines, trimmed)
		}
	}

	f.Require().Equal(expectedLines, actualLines, "%s mismatch", ChannelStdout)

	return f.selfRef
}

// ConsumeStdoutTrimmedf is like ConsumeStdoutTrimmed but accepts format args.
func (f *GenericLuaFluentTest[Self]) ConsumeStdoutTrimmedf(format string, args ...interface{}) Self {
	return f.ConsumeStdoutTrimmed(fmt.Sprintf(format, args...))
}

// ConsumeOutput asserts stdout matches expected template, then clears it.
// Uses TextAsserter.AssertWithTemplate for template rendering and comparison.
// The templateData variadic accepts a single map[string]interface{} for template context.
//
// Example:
//
//	ft.ConsumeOutput(`Device: {{.Address}}`, map[string]interface{}{"Address": "00:11:22"})
func (f *GenericLuaFluentTest[Self]) ConsumeOutput(expectedTemplate string, templateData ...interface{}) Self {
	f.Require().NotNil(f.current, "No script execution context")

	actual := f.current.ConsumeAll(ChannelStdout)

	// Build template data from variadic args
	var data interface{}
	if len(templateData) > 0 {
		data = templateData[0]
	}

	// Use TextAsserter for template-based comparison
	ta := testutils.NewTextAsserter(f.device.t).WithOptions(
		testutils.WithTrimSpace(true),
		testutils.WithIgnoreEmptyLines(true),
	)
	ta.AssertWithTemplate(actual, expectedTemplate, data)

	return f.selfRef
}

// ConsumeJSONOutput compares stdout JSON output with expected JSON (order-independent).
// Uses JSONAsserter2 for structural comparison that ignores key ordering.
func (f *GenericLuaFluentTest[Self]) ConsumeJSONOutput(expectedJSON string) Self {
	f.Require().NotNil(f.current, "No script execution context")

	actual := f.current.ConsumeAll(ChannelStdout)

	testutils.NewJSONAsserter(f.device.t).
		WithOptions(
			testutils.WithIgnoreExtraKeys(true),
		).
		Assert(actual, expectedJSON)

	return f.selfRef
}

// ConsumeOutputDedent is like ConsumeOutput but dedents the expected template first.
// This allows nicely indented multi-line expectations in test code (like YAML's "|").
//
// Example:
//
//	ft.ConsumeOutputDedent(`
//	    arg["format"] = "json"
//	    arg["verbose"] = "true"
//	`, nil)
func (f *GenericLuaFluentTest[Self]) ConsumeOutputDedent(expectedTemplate string, templateData ...interface{}) Self {
	return f.ConsumeOutput(dedent(expectedTemplate), templateData...)
}

// dedent strips common leading indentation from a multi-line string.
// Similar to Python's textwrap.dedent or YAML's "|" block scalar.
//
// Algorithm:
//  1. Find minimum indentation across all non-empty lines
//  2. Strip that amount from each line
//  3. Normalize tabs to 4 spaces
//
// Example:
//
//	dedent(`
//	    line1
//	    line2
//	`)
//	// Returns:
//	// "\nline1\nline2\n"
func dedent(s string) string {
	const tabWidth = 4
	lines := strings.Split(s, "\n")

	// find min indent
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for _, ch := range line {
			if ch == ' ' {
				indent++
			} else if ch == '\t' {
				indent += tabWidth
			} else {
				break
			}
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
	}

	// strip min indent, normalize tabs → spaces
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		indent, j := 0, 0
		for j < len(line) && indent < minIndent {
			if line[j] == ' ' {
				indent++
			} else if line[j] == '\t' {
				indent += tabWidth
			} else {
				break
			}
			j++
		}
		out = append(out, strings.Repeat(" ", indent-minIndent)+strings.ReplaceAll(line[j:], "\t", strings.Repeat(" ", tabWidth)))
	}
	return strings.Join(out, "\n")
}

// CallbackRecordOutput matches the JSON structure from Lua subscription callback output.
// Format: {"callNo": N, "Values": {...}, ...} (callNo embedded in record)
type CallbackRecordOutput struct {
	CallNo int `json:"callNo"`
	*device.Record
}

// UnmarshalJSON parses the flat record structure with embedded callNo.
func (c *CallbackRecordOutput) UnmarshalJSON(data []byte) error {
	// Use DeviceRecordJSON for parsing the record fields
	var recordJSON testutils.DeviceRecordJSON
	if err := recordJSON.UnmarshalJSON(data); err != nil {
		return err
	}
	c.Record = recordJSON.Record

	// Extract callNo from raw JSON
	var wrapper struct {
		CallNo int `json:"callNo"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	c.CallNo = wrapper.CallNo

	return nil
}

// MarshalJSON produces test-friendly JSON with callNo and byte payloads as hex strings.
func (c *CallbackRecordOutput) MarshalJSON() ([]byte, error) {
	recordJSON := testutils.DeviceRecordJSON{Record: c.Record}
	recordBytes, err := recordJSON.MarshalJSON()
	if err != nil {
		return nil, err
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(recordBytes, &obj); err != nil {
		return nil, err
	}
	obj["callNo"] = c.CallNo

	return json.Marshal(obj)
}

// ConsumeRecords validates stdout JSON output against expected CallbackRecordOutput values.
// Parses each stdout line as JSON {"callNo": N, "Values": {...}, ...} and compares using JSONAsserter.
func (f *GenericLuaFluentTest[Self]) ConsumeRecords(ignoreFields []string, expected ...*CallbackRecordOutput) Self {
	f.Require().NotNil(f.current, "No script execution context")

	// Parse actual JSON records from stdout
	var actualOutputs []*CallbackRecordOutput
	for _, line := range f.current.ConsumeLines(ChannelStdout) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var output CallbackRecordOutput
		err := json.Unmarshal([]byte(line), &output)
		f.Require().NoError(err, "Failed to parse JSON: %s", line)
		actualOutputs = append(actualOutputs, &output)
	}

	// Sort by Seq (BLE reception order) to get deterministic ordering.
	// Multi-characteristic subscriptions have non-deterministic delivery order
	// due to goroutine scheduling, but Seq reflects true BLE reception order.
	sort.Slice(actualOutputs, func(i, j int) bool {
		return actualOutputs[i].Seq < actualOutputs[j].Seq
	})

	// Reassign callNo based on sorted position
	for i := range actualOutputs {
		actualOutputs[i].CallNo = i + 1
	}

	actualJSON, err := json.Marshal(actualOutputs)
	f.Require().NoError(err, "Failed to marshal actual records")

	expectedJSON, err := json.Marshal(expected)
	f.Require().NoError(err, "Failed to marshal expected records")

	assertErr := testutils.NewJSONAsserter(f.device.t).
		WithOptions(
			testutils.WithIgnoreExtraKeys(false),
			testutils.WithCompareOnlyExpectedKeys(true),
			testutils.WithIgnoredFields(ignoreFields...),
		).
		AssertErr(string(actualJSON), string(expectedJSON))

	if assertErr != nil {
		f.Require().NoError(assertErr, "JSON mismatch\n=== ACTUAL ===\n%s\n=== EXPECTED ===\n%s", string(actualJSON), string(expectedJSON))
	}

	return f.selfRef
}

// ConsumeRecordsFromBuilder validates stdout JSON output against expected records from builder.
// Convenience wrapper that extracts ignoreFields and records from the builder.
func (f *GenericLuaFluentTest[Self]) ConsumeRecordsFromBuilder(builder *ExpectedRecordsBuilder) Self {
	return f.ConsumeRecords(builder.ignoreFields, builder.records...)
}

// AssertNoLuaScriptExecutionError asserts no runtime error and returns self for chaining.
// Thread-safe: protected by currentMu for reading executionErr.
func (f *GenericLuaFluentTest[Self]) AssertNoLuaScriptExecutionError() Self {
	f.currentMu.Lock()
	current := f.current
	var execErr error
	if current != nil {
		execErr = current.executionErr
	}
	f.currentMu.Unlock()

	f.Require().NotNil(current, "No script execution to check")
	f.Require().NoError(execErr, "Unexpected Lua runtime error")
	return f.selfRef
}

// Ignore explicitly discards the execution result without validation.
// Use for setup scripts where you don't care about the result.
func (f *GenericLuaFluentTest[Self]) Ignore() {
	f.clearContext()
}

// clearContext cleans up and clears the current execution context.
func (f *GenericLuaFluentTest[Self]) clearContext() {
	if f.current == nil {
		return
	}
	if f.current.collector != nil {
		f.current.collector.Stop()
	}
	f.current = nil
}

// WithBridge sets a bridge on the LuaAPI for PTY operations.
func (f *GenericLuaFluentTest[Self]) WithBridge(bridge BridgeInfo) Self {
	f.LuaAPI().SetBridge(bridge)
	return f.selfRef
}

type TestCase struct {
	Name       string
	Peripheral func(*testutils.PeripheralDeviceBuilder) // Custom peripheral config (nil = use default)
	Bridge     BridgeInfo

	Subscription []*device.SubscribeOptions
	MaxRate      uint32
	Mode         device.StreamMode // Use existing enum
	Script       string
	Callback     string
	EmitData     func(*testutils.PeripheralDataEmitter)

	ScriptError     string
	CallbackError   string
	Output          string
	JsonOutput      string // JSON output comparison (order-independent)
	ExpectedRecords func(*ExpectedRecordsBuilder)
}

func (tc TestCase) LuaTestScript() (string, error) {
	const scriptTemplate = `
local json = require("json")
callNo = 0

blim.subscribe{
    services = {
        {{- range .SubscribeOptions }}
        {
            service = "{{ .Service }}",
            {{- if .Characteristics }}
            chars = {
                {{- range $i, $c := .Characteristics -}}
                {{ if $i }}, {{ end }}"{{ $c }}"
                {{- end -}}
            },
            {{- end }}
            {{- if .Indicate }}
            indicate = true,
            {{- end }}
        },
        {{- end }}
    },

    Mode = "{{ modeStr .Mode }}",
    MaxRate = {{ if .MaxRate }}{{ .MaxRate }}{{ else }}50{{ end }},

    Callback = function(record)
	{{- if .CallbackBody }}
		{{ indent 4 .CallbackBody }}
	{{- else }}
		callNo = callNo + 1
		record.callNo = callNo
		print(json.encode(record))
	{{- end }}
	end
}
`
	script := tc.Script
	if len(tc.Script) == 0 {
		// Render script template
		data := struct {
			SubscribeOptions []*device.SubscribeOptions
			CallbackBody     string
			MaxRate          uint32
			Mode             device.StreamMode
		}{
			SubscribeOptions: tc.Subscription,
			CallbackBody:     tc.Callback,
			MaxRate:          tc.MaxRate,
			Mode:             tc.Mode,
		}

		tmpl, err := template.New(tc.Name).Funcs(template.FuncMap{
			"indent": func(n int, str string) string {
				pad := strings.Repeat(" ", n)
				lines := strings.Split(strings.TrimRight(str, "\n"), "\n")
				for i, l := range lines {
					lines[i] = pad + l
				}
				return strings.Join(lines, "\n")
			},
			"modeStr": func(m device.StreamMode) string {
				switch m {
				case device.StreamEveryUpdate:
					return "EveryUpdate"
				case device.StreamBatched:
					return "Batched"
				case device.StreamAggregated:
					return "Aggregated"
				default:
					return "EveryUpdate"
				}
			},
		}).Parse(scriptTemplate)

		//require.NoError(err, "failed to parse script template")
		if err != nil {
			return "", fmt.Errorf("failed to parse script template: %w", err)
		}

		var buf bytes.Buffer
		err = tmpl.Execute(&buf, data)

		// require.NoError(err, "failed to render script template")
		if err != nil {
			return "", fmt.Errorf("failed to render script template: %w", err)
		}

		script = buf.String()
	}

	return script, nil
}

// ExpectedRecordsBuilder builds expected subscription callback output for assertions.
type ExpectedRecordsBuilder struct {
	records      []*CallbackRecordOutput
	ignoreFields []string
}

// NewExpectedRecordsBuilder creates a new ExpectedRecordsBuilder.
func NewExpectedRecordsBuilder() *ExpectedRecordsBuilder {
	return &ExpectedRecordsBuilder{}
}

// ExpectedRecord is a fluent builder for a single expected record.
type ExpectedRecord struct {
	builder *ExpectedRecordsBuilder
	rec     *CallbackRecordOutput
}

// IgnoreFields sets fields to ignore during JSON comparison.
func (b *ExpectedRecordsBuilder) IgnoreFields(fields ...string) *ExpectedRecordsBuilder {
	b.ignoreFields = append(b.ignoreFields, fields...)
	return b
}

// Clone creates a deep copy of the builder with the same configuration but no records.
func (b *ExpectedRecordsBuilder) Clone() *ExpectedRecordsBuilder {
	clone := &ExpectedRecordsBuilder{
		ignoreFields: make([]string, len(b.ignoreFields)),
	}
	copy(clone.ignoreFields, b.ignoreFields)
	return clone
}

// Record adds a new expected record and returns it for fluent configuration.
func (b *ExpectedRecordsBuilder) Record() *ExpectedRecord {
	r := &ExpectedRecord{
		builder: b,
		rec: &CallbackRecordOutput{Record: &device.Record{
			Values:      make(map[string][]byte),
			BatchValues: make(map[string][][]byte),
		}},
	}
	b.records = append(b.records, r.rec)
	return r
}

// CallNo sets the expected call number for this record.
func (r *ExpectedRecord) CallNo(n int) *ExpectedRecord {
	r.rec.CallNo = n
	return r
}

// Value sets an expected value for a characteristic UUID (for EveryUpdate/Aggregated modes).
func (r *ExpectedRecord) Value(uuid string, bytes ...byte) *ExpectedRecord {
	r.rec.Record.Values[uuid] = bytes
	return r
}

// BatchValue adds a batch value for a characteristic UUID (for Batched mode).
// Call multiple times to add multiple values to the batch.
func (r *ExpectedRecord) BatchValue(uuid string, bytes ...byte) *ExpectedRecord {
	r.rec.Record.BatchValues[uuid] = append(r.rec.Record.BatchValues[uuid], bytes)
	return r
}

// BatchValues sets all batch values for a characteristic UUID at once (for Batched mode).
func (r *ExpectedRecord) BatchValues(uuid string, values ...[]byte) *ExpectedRecord {
	r.rec.Record.BatchValues[uuid] = values
	return r
}

// Meta sets the record metadata (TsUs, Seq, Flags).
func (r *ExpectedRecord) Meta(tsUs int64, seq uint64, flags uint32) *ExpectedRecord {
	r.rec.Record.TsUs = tsUs
	r.rec.Record.Seq = seq
	r.rec.Record.Flags = flags
	return r
}

// Record chains to create the next expected record.
func (r *ExpectedRecord) Record() *ExpectedRecord {
	return r.builder.Record()
}

func ExecuteScenarios(connectionFactoryFn func(TestCase) *LuaTestableDevice, testCases ...TestCase) {
	executeScenarios(connectionFactoryFn, nil, testCases...)
}

// ExecuteScenarios runs test scenarios with Lua script execution and verification.
// connectionFactoryFn creates a new LuaTestableDevice for each test case.
// assertions is optional - if nil, uses suite's default Require(); pass custom assertions
// for guinea pig testing pattern where you want to intercept failures.
func executeScenarios(connectionFactoryFn func(TestCase) *LuaTestableDevice, assertions *require.Assertions, testCases ...TestCase) {
	// A trick to get a *suite.Suite (use empty TestCase just to get the suite reference)
	s := connectionFactoryFn(TestCase{}).FluentLuaTest().suite()

	// Test body that runs a single test case
	runTestCase := func(tc TestCase) {
		// Get assertions: use injected ones (guinea pig mode) or suite's current context.
		// IMPORTANT: s.Require() must be called HERE (inside s.Run callback for normal mode)
		// so it uses the SUBTEST's T, not the parent test's T.
		require := assertions
		if require == nil {
			require = s.Require()
		}
		// Inject assertions into FluentLuaTest so that assertion failures
		// (e.g., ConsumeLuaError, MustExecuteScript) use the same assertions
		// as executeScenarios. This enables guinea pig testing pattern where
		// a TBInterceptor can capture ALL failures from the entire test flow.
		ft := connectionFactoryFn(tc).FluentLuaTest().WithAssertions(require)

		// Execute script
		if len(tc.Callback) > 0 && len(tc.Script) > 0 {
			require.Fail("cannot specify both script and callback")
		}

		//if len(tc.callback) == 0 && len(tc.script) == 0 {
		//	require.Fail("must specify either script or callback")
		//}

		if tc.Bridge != nil {
			ft.WithBridge(tc.Bridge)
		}

		script, err := tc.LuaTestScript()
		require.NoError(err, "failed to render Lua script")

		if tc.ScriptError != "" {
			// FailExecuteScript calls ConsumeLuaError which already consumes stderr
			ft.FailExecuteScript(tc.ScriptError, script)
		} else {
			ft.MustExecuteScript(script)
		}

		// Emit data to trigger subscription, if provided
		if tc.EmitData != nil {
			ft.EmmitData(tc.EmitData).
				// Give it time for the async callback to execute
				Sleep(30 * time.Millisecond)
		}

		// Verify error message, if provided
		if tc.CallbackError != "" {
			ft.ConsumeStderrAsLuaError(tc.CallbackError)
		}

		if tc.Output != "" {
			ft.ConsumeOutputDedent(tc.Output)
		}

		// JSON output comparison (order-independent)
		if tc.JsonOutput != "" {
			ft.ConsumeJSONOutput(tc.JsonOutput)
		}

		// Verify expected records if provided
		if tc.ExpectedRecords != nil {
			builder := &ExpectedRecordsBuilder{}
			tc.ExpectedRecords(builder)
			ft.ConsumeRecords(builder.ignoreFields, builder.records...)
		}
	}

	for _, tc := range testCases {
		// When custom assertions are provided (guinea pig mode), run the test directly
		// without creating a real Go subtest. This prevents testify/suite's panic recovery
		// from intercepting the test failure and allows TBInterceptor to capture failures.
		if assertions != nil {
			runTestCase(tc)
		} else {
			// Normal mode: create a real Go subtest for proper test isolation and reporting
			s.Run(tc.Name, func() {
				runTestCase(tc)
			})
		}
	}
}
