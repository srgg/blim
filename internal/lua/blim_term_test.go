//go:build test

package lua

import (
	"testing"
	"time"

	"github.com/srgg/blim/internal/testutils"
	suitelib "github.com/stretchr/testify/suite"
)

// BlimTermTestSuite exercises the blim.term API of the embedded blim.lua
// library against a real PTY attached to the process stdin.
//
// blim.term operates on file descriptor 0 directly (tcgetattr/read via FFI),
// so tests use the GivenStdinPTY/GivenStdinPipe suite preconditions, which
// dup2-replace fd 0. Tests MUST NOT use t.Parallel(): fd 0 is process-global.
type BlimTermTestSuite struct {
	LuaApiSuite
}

func (suite *BlimTermTestSuite) TestRawModeLifecycle() {
	// GOAL: Verify raw mode can be enabled on a real TTY, makes reads
	//       non-blocking (VMIN=0: empty input queue yields nil instead of
	//       blocking the engine), and both switches are idempotent.
	//
	// TEST SCENARIO: enable raw twice → non-blocking read with no input returns nil → disable twice

	suite.GivenStdinPTY()

	suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`
			assert(blim.term.enable_raw(), "enable_raw MUST succeed on a TTY")
			assert(blim.term.enable_raw(), "enable_raw MUST be idempotent")
			assert(blim.term.read_char() == nil,
				"read_char MUST return nil immediately when no input is pending")
			blim.term.disable_raw()
			blim.term.disable_raw() -- MUST be idempotent (asserted by not erroring)
		`)
}

func (suite *BlimTermTestSuite) TestReadCharDeliversPendingKeypress() {
	// GOAL: Verify a key that is already pending on stdin is returned by a
	//       polling read (separate from TestRawModeLifecycle which verifies
	//       the empty-queue path).
	//
	// TEST SCENARIO: enable raw → keypress arrives → polling read returns that key

	stdin := suite.GivenStdinPTY()

	ft := suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`assert(blim.term.enable_raw(), "enable_raw MUST succeed on a TTY")`)

	stdin.Type("y")

	ft.MustExecuteScript(`
		local ch = blim.term.read_char(1000)
		blim.term.disable_raw()
		assert(ch == "y", "read_char MUST return the pressed key, got: " .. tostring(ch))
	`)
}

func (suite *BlimTermTestSuite) TestReadCharPollsUntilKeypressArrives() {
	// GOAL: Verify a bounded polling read picks up a key that arrives only
	//       mid-wait (separate from TestReadCharDeliversPendingKeypress where
	//       the key is already queued before the read starts).
	//
	// TEST SCENARIO: polling read starts with empty input → keypress arrives a few poll steps later → read returns that key before the deadline

	stdin := suite.GivenStdinPTY()

	go func() {
		// 30ms = a few 10ms poll steps into the wait; the 2s read deadline
		// leaves ample margin.
		time.Sleep(30 * time.Millisecond)
		// Write directly instead of Type(): Type asserts via require, which
		// must not run outside the test goroutine. A write failure here still
		// surfaces as the primary read_char assertion failing below.
		_, _ = stdin.Master.WriteString("x")
	}()

	suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`
			assert(blim.term.enable_raw(), "enable_raw MUST succeed on a TTY")
			local ch = blim.term.read_char(2000)
			blim.term.disable_raw()
			assert(ch == "x", "read_char MUST return the key arriving mid-wait, got: " .. tostring(ch))
		`)
}

func (suite *BlimTermTestSuite) TestReadCharWaitDoesNotBlockBleCallbacks() {
	// GOAL: Verify a waiting read yields the engine to BLE traffic: a
	//       subscription callback for a notification arriving mid-wait MUST
	//       run before the wait finishes. Unlike the other read tests, which
	//       verify key delivery, this pins the concurrency contract — waiting
	//       for a key must not freeze callback delivery.
	//
	// TEST SCENARIO: subscribe → waiting read starts with no key pressed → notification arrives mid-wait → callback has run by the time the read times out

	suite.GivenStdinPTY()

	ft := suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`
			assert(blim.term.enable_raw(), "enable_raw MUST succeed on a TTY")
			notify_count = 0
			blim.subscribe{
				services = {
					{
						service = "1234",
						chars = {"5678"}
					}
				},
				Mode = "EveryUpdate",
				MaxRate = 0,
				Callback = function() notify_count = notify_count + 1 end
			}
		`)

	// EmmitData runs its closure in a goroutine; the extra delay (a few 10ms
	// poll steps) makes the notification land while the next script is
	// blocked waiting for a key.
	ft.EmmitData(func(emitter *testutils.PeripheralDataEmitter) {
		time.Sleep(30 * time.Millisecond)
		emitter.
			WithService("1234").WithCharacteristic("5678", 0x01, 0x02).
			Emit(true)
	})

	ft.MustExecuteScript(`
		-- 200ms: the notification lands at ~30ms, so the callback has ample
		-- time to run before this read times out without a keypress.
		local ch = blim.term.read_char(200)
		blim.term.disable_raw()
		assert(ch == nil, "no key was pressed, read MUST time out")
		assert(notify_count > 0,
			"BLE callback MUST run while the read is waiting - engine was frozen")
	`)
}

func (suite *BlimTermTestSuite) TestReadCharSignalsEOFWhenStdinCloses() {
	// GOAL: Verify a closed stdin is reported as a distinct EOF signal
	//       instead of being indistinguishable from "no key pressed", so
	//       interactive loops can exit instead of spinning forever with the
	//       terminal stuck in raw mode.
	//
	// TEST SCENARIO: enable raw → stdin closes → waiting read returns io.read-style nil,msg,EOF → non-blocking read agrees

	stdin := suite.GivenStdinPTY()

	ft := suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`assert(blim.term.enable_raw(), "enable_raw MUST succeed on a TTY")`)

	suite.Require().NoError(stdin.Master.Close(), "closing the PTY master MUST succeed")

	ft.MustExecuteScript(`
		local ch, err, code = blim.term.read_char(1000)
		assert(ch == nil, "no key can arrive from a closed stdin")
		assert(err ~= nil, "waiting read MUST signal a terminal condition")
		assert(code == blim.term.EOF, "code MUST be the EOF sentinel, got: " .. tostring(code))

		local ch2, err2, code2 = blim.term.read_char()
		blim.term.disable_raw()
		assert(ch2 == nil and code2 == blim.term.EOF, "non-blocking read MUST signal EOF too")
	`)
}

func (suite *BlimTermTestSuite) TestReadCharDoesNotBlockWithoutRawMode() {
	// GOAL: Verify reads are safe when raw mode was never enabled (the
	//       line-based fallback of interactive scripts): with no pending
	//       input the read returns immediately instead of blocking the
	//       engine on a canonical-mode tty, and a buffered line is still
	//       delivered after Enter.
	//
	// TEST SCENARIO: raw mode NOT enabled → non-blocking read returns nil immediately → full line arrives → waiting read returns its first byte

	stdin := suite.GivenStdinPTY()

	ft := suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`assert(blim.term.read_char() == nil, "empty canonical stdin MUST NOT block")`)

	stdin.Type("y\n")

	ft.MustExecuteScript(`
		local ch = blim.term.read_char(1000)
		assert(ch == "y", "first byte of the buffered line MUST be delivered, got: " .. tostring(ch))
	`)
}

func (suite *BlimTermTestSuite) TestEnableRawFailsWhenStdinIsNotATTY() {
	// GOAL: Verify the graceful failure path when stdin is not a terminal:
	//       enable_raw MUST report an error instead of raising or crashing.
	//
	// TEST SCENARIO: stdin replaced with a pipe → enable_raw returns nil plus error → error names tcgetattr

	suite.GivenStdinPipe()

	suite.Connect("1").FluentLuaTest().
		MustExecuteScript(`
			local ok, err = blim.term.enable_raw()
			assert(ok == nil, "enable_raw MUST fail when stdin is not a TTY")
			assert(err == "tcgetattr failed", "unexpected error: " .. tostring(err))
		`)
}

func TestBlimTermTestSuite(t *testing.T) {
	suitelib.Run(t, new(BlimTermTestSuite))
}
