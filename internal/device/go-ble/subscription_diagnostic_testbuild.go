//go:build test

package goble

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/srg/blim/internal/groutine"
)

// =============================================================================
// SUBSCRIPTION DIAGNOSTICS SYSTEM
// =============================================================================
//
// PURPOSE
// -------
// This module provides comprehensive diagnostics for BLE subscription lifecycle
// verification in test builds. It enables automatic detection of resource leaks,
// incomplete cleanup, and goroutine leaks - even when device_test don't explicitly
// call cancel().
//
// ARCHITECTURE
// ------------
//
//   ┌─────────────────────────────────────────────────────────────────────────┐
//   │                         TEST INFRASTRUCTURE                              │
//   │  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────────┐  │
//   │  │ OnConnected     │───▶│ ConnDiag init   │    │ TearDownSuite       │  │
//   │  │ (hook)          │    │ (per connection)│    │ (global leak check) │  │
//   │  └─────────────────┘    └─────────────────┘    └─────────────────────┘  │
//   └─────────────────────────────────────────────────────────────────────────┘
//                                     │
//                                     ▼
//   ┌─────────────────────────────────────────────────────────────────────────┐
//   │                         PER-CONNECTION LAYER                             │
//   │  ┌─────────────────────────────────────────────────────────────────────┐│
//   │  │ ConnectionDiagnostics                                               ││
//   │  │   • Tracks all subscriptions created on this connection             ││
//   │  │   • Records BLEValue pool baseline at connect time                  ││
//   │  │   • VerifyDisconnectCleanup() runs on Disconnect()                  ││
//   │  │   • PANICS on failure → test fails automatically                    ││
//   │  └─────────────────────────────────────────────────────────────────────┘│
//   └─────────────────────────────────────────────────────────────────────────┘
//                                     │
//                                     ▼
//   ┌─────────────────────────────────────────────────────────────────────────┐
//   │                       PER-SUBSCRIPTION LAYER                             │
//   │  ┌─────────────────────────────────────────────────────────────────────┐│
//   │  │ SubscriptionDiagnostics (embedded in Subscription.Diag)             ││
//   │  │                                                                      ││
//   │  │ CREATION STATE (captured under mutex):                               ││
//   │  │   • GoroutinesAtStart  - baseline for leak detection                 ││
//   │  │   • CreatedAt          - timestamp for debugging                     ││
//   │  │   • CharUUIDs          - characteristics we subscribed to            ││
//   │  │   • FirstSubscriberFor - chars where we started fan-out              ││
//   │  │                                                                      ││
//   │  │ RUNTIME STATS (atomic updates):                                      ││
//   │  │   • CallbackCount      - times callback invoked                      ││
//   │  │   • ValuesProcessed    - values successfully processed               ││
//   │  │   • DrainedCount       - values drained (cache + cleanup)            ││
//   │  │   • FirstValueAt       - latency tracking                            ││
//   │  │   • PanicRecovered     - callback panic detection                    ││
//   │  │                                                                      ││
//   │  │ LIFECYCLE TRACKING:                                                  ││
//   │  │   • CancelReason       - Explicit (cancel()) vs Context (cascade)    ││
//   │  │   • CancelledAt        - when cancel triggered                       ││
//   │  │   • CleanedUp          - runSubscription exited cleanly              ││
//   │  │   • CleanedUpAt        - cleanup completion timestamp                ││
//   │  │                                                                      ││
//   │  │ VERIFICATION RESULTS (set after cancel):                             ││
//   │  │   • CleanupVerified    - verification ran                            ││
//   │  │   • CleanupErrors      - list of failures (nil = success)            ││
//   │  └─────────────────────────────────────────────────────────────────────┘│
//   └─────────────────────────────────────────────────────────────────────────┘
//                                     │
//                                     ▼
//   ┌─────────────────────────────────────────────────────────────────────────┐
//   │                         RESOURCE TRACKING                                │
//   │  ┌─────────────────────────────────────────────────────────────────────┐│
//   │  │ BLEValueTracker (global singleton)                                  ││
//   │  │   • Tracks sync.Pool allocations/releases                           ││
//   │  │   • Outstanding() returns current leak count                        ││
//   │  │   • HighWaterMark() for memory pressure analysis                    ││
//   │  │   • Zero overhead in production (no-op stubs)                       ││
//   │  └─────────────────────────────────────────────────────────────────────┘│
//   └─────────────────────────────────────────────────────────────────────────┘
//
// USE CASES
// ---------
//
// 1. EXPLICIT CANCEL VERIFICATION (Reactive)
//    Test calls cancel() → wrapCancelFunc automatically:
//      a) Captures state before cancel (subscriber counts, pool state)
//      b) Calls actual cancel
//      c) Waits CleanupDelay (200ms) for goroutines to exit
//      d) Verifies: context cancelled, channel drained, subscribers removed,
//         fan-outs stopped, goroutines exited, no BLEValue leaks
//      e) Stores results in sub.Diag.CleanupErrors
//
//    Example:
//      cancel, _ := conn.SubscribeWithName("my-sub", opts, mode, rate, cb)
//      cancel()  // ← Auto-verifies in test build
//      assert.Nil(sub.Diag.CleanupErrors)
//
// 2. IMPLICIT CANCEL VERIFICATION (Proactive)
//    Test ends without calling cancel() → TearDownTest calls Disconnect() →
//    Disconnect() cancels connection context → subscriptions cascade cancel →
//    subMgr.Wait() → VerifyDisconnectCleanup() checks:
//      a) All subscriptions have CleanedUp == true
//      b) BLEValue pool returned to connect-time baseline
//      c) PANICS if verification fails → test fails automatically
//
//    Example:
//      _, _ = conn.SubscribeWithName("sub-1", opts1, ...)
//      _, _ = conn.SubscribeWithName("sub-2", opts2, ...)
//      // Test ends, TearDownTest runs Disconnect()
//      // → Automatic verification, panic on failure
//
// 3. MULTI-SUBSCRIBER SCENARIO
//    Multiple subscriptions to same characteristic → fan-out shared →
//    diagnostics track FirstSubscriberFor to know who started fan-out →
//    only verify fan-out stopped when LAST subscriber cancels.
//
//    Example:
//      cancelA, _ := conn.SubscribeWithName("sub-A", opts, ...)
//      cancelB, _ := conn.SubscribeWithName("sub-B", opts, ...)  // same char
//      cancelA()  // fan-out still runs (B still subscribed)
//      assert.True(char.HasActiveFanOut())
//      cancelB()  // fan-out stops (last subscriber)
//      assert.False(char.HasActiveFanOut())
//
// 4. CROSS-TEST LEAK DETECTION
//    TearDownSuite checks global BLEValueTracker.Outstanding() == 0 →
//    catches leaks that span multiple device_test (e.g., forgotten subscriptions).
//
// VERIFICATION CHECKS
// -------------------
//
// Reactive (wrapCancelFunc):
//   | Check ID              | Verifies                                       |
//   |-----------------------|------------------------------------------------|
//   | context               | sub.ctx.Err() != nil                           |
//   | channel               | len(sub.updates) == 0                          |
//   | subscribers           | SubscriberCount decreased for each char        |
//   | fanout                | HasActiveFanOut() false if we were last        |
//   | subscription_goroutine| groutine.IsRunning(name) == false              |
//   | fanout_goroutine      | groutine.IsRunning("fanout-UUID") == false     |
//   | goroutine_leak        | GoroutinesDelta() <= +2                        |
//   | blevalue_pool         | Outstanding() didn't increase                  |
//
// Proactive (VerifyDisconnectCleanup):
//   | Check ID               | Verifies                                      |
//   |------------------------|-----------------------------------------------|
//   | subscription_cleanup   | Every sub.Diag.CleanedUp == true              |
//   | blevalue_pool_disconnect| Pool == ValuesAtConnect baseline             |
//
// PRODUCTION BEHAVIOR
// -------------------
// All diagnostic code is behind //go:build test. In production:
//   • newSubscriptionDiagnostics() returns nil
//   • newConnectionDiagnostics() returns nil
//   • All tracking methods are no-ops (nil receiver safe)
//   • wrapCancelFunc() returns raw cancel (zero overhead)
//   • GetValueTracker() returns nil
//   • trackBLEValueAlloc/Release are no-ops
//
// GOALS FOR FUTURE IMPROVEMENTS
// -----------------------------
// When modifying this diagnostic system, verify:
//   1. Tests still fail automatically on cleanup issues (PANIC preserved)
//   2. Production builds have zero runtime overhead
//   3. All verification checks in tables above still run
//   4. Multi-subscriber scenarios correctly track fan-out ownership
//   5. BLEValue pool tracking catches leaks across connection lifecycle
//   6. Goroutine detection via pprof labels still works
//   7. CleanupDelay is sufficient for goroutine exit (currently 200ms)
//
// =============================================================================

// ----------------------------
// Cleanup Error Types
// ----------------------------

type CleanupError struct {
	Check   string
	Message string
}

func (e CleanupError) Error() string {
	return fmt.Sprintf("%s: %s", e.Check, e.Message)
}

type CleanupErrors []CleanupError

func (e CleanupErrors) Error() string {
	if len(e) == 0 {
		return "no errors"
	}
	var msgs []string
	for _, err := range e {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

// ----------------------------
// Cancel Reason
// ----------------------------

type CancelReason int

const (
	CancelReasonNone     CancelReason = iota // Not yet cancelled
	CancelReasonExplicit                     // cancel() was called
	CancelReasonContext                      // Parent context cancelled (disconnect, shutdown)
)

// CleanupDelay is the time to wait for goroutines to exit after cancel
const CleanupDelay = 200 * time.Millisecond

// panicOnCleanupFailure controls whether cleanup verification failures cause panics.
// Default: true (device_test fail automatically on cleanup issues).
// Set to false to suppress panics for specific device_test that need special handling.
// Even when false, errors are still collected in Diag.CleanupErrors for explicit checking.
var panicOnCleanupFailure = true

// PanicOnCleanupFailure returns the current panic behavior setting.
func PanicOnCleanupFailure() bool {
	return panicOnCleanupFailure
}

// SetPanicOnCleanupFailure sets the panic behavior and returns a restore function.
// Usage: defer goble.SetPanicOnCleanupFailure(false)() // Restore in defer
func SetPanicOnCleanupFailure(enabled bool) func() {
	old := panicOnCleanupFailure
	panicOnCleanupFailure = enabled
	return func() { panicOnCleanupFailure = old }
}

// ----------------------------
// Subscription Diagnostics
// ----------------------------

// SubscriptionDiagnostics captures trusted state at subscription creation time.
// Created under connection mutex, so values are consistent.
// Embedded in Subscription for easy access via SubscriptionManager lookup.
type SubscriptionDiagnostics struct {
	// Captured at creation (under mutex)
	GoroutinesAtStart  int                  // runtime.NumGoroutine() at creation
	CreatedAt          time.Time            // Timestamp for debugging
	CharUUIDs          []string             // Characteristics we subscribed to
	FirstSubscriberFor map[string]bool      // Char UUIDs where we started the fan-out
	Chars              []*BLECharacteristic // Direct refs for SubscriberCount checks

	// References needed for verification (set via SetReferences)
	sub  *Subscription
	conn *BLEConnection

	// Runtime stats (updated during subscription lifetime via atomic ops)
	CallbackCount   atomic.Int64 // Times callback was invoked
	ValuesProcessed atomic.Int64 // Values successfully processed
	DrainedCount    atomic.Int64 // Values drained (CoreBluetooth cache + cleanup)
	firstValueOnce  sync.Once    // Ensures FirstValueAt set only once
	FirstValueAt    time.Time    // When first value received (latency tracking)
	PanicRecovered  atomic.Bool  // If callback panicked and was recovered

	// Lifecycle tracking
	CancelReason CancelReason // How subscription was cancelled
	CancelledAt  time.Time    // When cancel was called
	CleanedUp    bool         // runSubscription exited cleanly
	CleanedUpAt  time.Time    // When cleanup completed

	// Captured before cancel (set by wrapped cancel)
	ValuesBeforeCancel    int64
	CallbacksBeforeCancel int64

	// Verification results (set after cancel completes)
	CleanupVerified bool
	CleanupErrors   CleanupErrors
}

// newSubscriptionDiagnostics creates diagnostics capturing current state.
// MUST be called under connection mutex for consistent values.
func newSubscriptionDiagnostics(chars []*BLECharacteristic) *SubscriptionDiagnostics {
	d := &SubscriptionDiagnostics{
		GoroutinesAtStart:  runtime.NumGoroutine(),
		CreatedAt:          time.Now(),
		CharUUIDs:          make([]string, len(chars)),
		FirstSubscriberFor: make(map[string]bool),
		Chars:              chars,
	}
	for i, c := range chars {
		d.CharUUIDs[i] = c.UUID()
	}
	return d
}

// SetReferences stores references needed for cancel verification.
func (d *SubscriptionDiagnostics) SetReferences(sub *Subscription, conn *BLEConnection) {
	if d == nil {
		return
	}
	d.sub = sub
	d.conn = conn
}

// MarkFirstSubscriber records that we started the fan-out for this char
func (d *SubscriptionDiagnostics) MarkFirstSubscriber(charUUID string) {
	if d != nil && d.FirstSubscriberFor != nil {
		d.FirstSubscriberFor[charUUID] = true
	}
}

// MarkExplicitCancel records that cancel() was called
func (d *SubscriptionDiagnostics) MarkExplicitCancel() {
	if d != nil {
		d.CancelReason = CancelReasonExplicit
		d.CancelledAt = time.Now()
	}
}

// MarkImplicitCancel records that context was cancelled without cancel() call
func (d *SubscriptionDiagnostics) MarkImplicitCancel() {
	if d != nil && d.CancelReason == CancelReasonNone {
		d.CancelReason = CancelReasonContext
		d.CancelledAt = time.Now()
	}
}

// MarkCleanedUp records that runSubscription exited cleanly
func (d *SubscriptionDiagnostics) MarkCleanedUp() {
	if d != nil {
		d.CleanedUp = true
		d.CleanedUpAt = time.Now()
	}
}

// MarkPanicRecovered records that callback panicked and was recovered
func (d *SubscriptionDiagnostics) MarkPanicRecovered() {
	if d != nil {
		d.PanicRecovered.Store(true)
	}
}

// MarkFirstValue records timestamp of first value (thread-safe, called once)
func (d *SubscriptionDiagnostics) MarkFirstValue() {
	if d != nil {
		d.firstValueOnce.Do(func() {
			d.FirstValueAt = time.Now()
		})
	}
}

// IncrementCallback increments callback count
func (d *SubscriptionDiagnostics) IncrementCallback() {
	if d != nil {
		d.CallbackCount.Add(1)
	}
}

// IncrementProcessed increments processed value count
func (d *SubscriptionDiagnostics) IncrementProcessed() {
	if d != nil {
		d.ValuesProcessed.Add(1)
	}
}

// AddDrained adds to drained count
func (d *SubscriptionDiagnostics) AddDrained(count int) {
	if d != nil {
		d.DrainedCount.Add(int64(count))
	}
}

// GoroutinesDelta returns current goroutines minus baseline
func (d *SubscriptionDiagnostics) GoroutinesDelta() int {
	if d == nil {
		return 0
	}
	return runtime.NumGoroutine() - d.GoroutinesAtStart
}

// ----------------------------
// Connection-Level Diagnostics
// ----------------------------

// ConnectionDiagnostics tracks all subscriptions on a connection for proactive verification.
// Embedded in BLEConnection, populated only in test builds.
type ConnectionDiagnostics struct {
	mu              sync.Mutex
	Subscriptions   []*Subscription // All subscriptions created
	ValuesAtConnect int64           // BLEValue outstanding at connect time
	connection      *BLEConnection
	// Verification results (set on disconnect)
	DisconnectVerified bool
	DisconnectErrors   CleanupErrors
}

func newConnectionDiagnostics(conn *BLEConnection) *ConnectionDiagnostics {
	return &ConnectionDiagnostics{
		Subscriptions:   make([]*Subscription, 0),
		connection:      conn,
		ValuesAtConnect: conn.valuePool.Outstanding(),
	}
}

func (cd *ConnectionDiagnostics) OutstandingValues() int64 {
	return cd.connection.valuePool.Outstanding()
}

func (cd *ConnectionDiagnostics) ValuesHighWaterMark() int64 {
	return cd.connection.valuePool.HighWaterMark()
}

func (cd *ConnectionDiagnostics) TrackSubscription(sub *Subscription) {
	if cd == nil {
		return
	}
	cd.mu.Lock()
	defer cd.mu.Unlock()
	cd.Subscriptions = append(cd.Subscriptions, sub)
}

func (cd *ConnectionDiagnostics) VerifyDisconnectCleanup() CleanupErrors {
	if cd == nil {
		return nil
	}
	cd.mu.Lock()
	defer cd.mu.Unlock()

	var errors CleanupErrors

	for _, sub := range cd.Subscriptions {
		if sub.Diag == nil {
			continue
		}

		// Every subscription must have cleaned up
		if !sub.Diag.CleanedUp {
			errors = append(errors, CleanupError{
				Check:   "subscription_cleanup",
				Message: fmt.Sprintf("subscription %q did not clean up", sub.Name),
			})
		}

		// At disconnect, verify all chars have zero subscribers
		for _, c := range sub.Diag.Chars {
			if c.SubscriberCount() > 0 {
				errors = append(errors, CleanupError{
					Check:   "char_subscribers_disconnect",
					Message: fmt.Sprintf("char %s has %d subscribers at disconnect (MUST BE 0)", c.UUID(), c.SubscriberCount()),
				})
			}
			if sub.Diag.FirstSubscriberFor[c.UUID()] && c.HasActiveFanOut() {
				errors = append(errors, CleanupError{
					Check:   "fanout_disconnect",
					Message: fmt.Sprintf("fan-out for %s still active at disconnect (MUST BE STOPPED)", c.UUID()),
				})
			}
		}
	}

	// BLEValue pool should return to baseline
	outstanding := cd.OutstandingValues()
	if outstanding != cd.ValuesAtConnect {
		errors = append(errors, CleanupError{
			Check: "blevalue_pool_disconnect",
			Message: fmt.Sprintf("BLEValue leak on disconnect: %d outstanding (was %d at connect)",
				outstanding, cd.ValuesAtConnect),
		})
	}

	cd.DisconnectVerified = true
	cd.DisconnectErrors = errors

	// NOTE: Caller is responsible for handling errors (DiagnosticPanic or AssertDiagnostic).
	// This separation allows:
	//   - Disconnect() to use DiagnosticPanic (fail-fast)
	//   - t.Cleanup() to use AssertDiagnostic (soft failure, all errors reported)
	return errors
}

// ----------------------------
// Cancel Wrapper (wrapCancelFunc)
// ----------------------------

// wrapCancelFunc wraps the cancel function with before/after state verification.
// In test builds: captures state, calls cancel, waits, verifies, stores results.
// Uses sync.Once to ensure verification runs exactly once, even if cancel() is called multiple times.
func wrapCancelFunc(diag *SubscriptionDiagnostics, cancel func()) func() {
	if diag == nil {
		return cancel // Production path: no overhead
	}

	var once sync.Once // Prevent double-call issues
	return func() {
		once.Do(func() {
			// Mark explicit cancel
			diag.MarkExplicitCancel()

			// Capture state BEFORE cancel
			diag.ValuesBeforeCancel = diag.conn.valuePool.Outstanding()
			diag.CallbacksBeforeCancel = diag.CallbackCount.Load()

			subscriberCountsBefore := make(map[string]int)
			for _, c := range diag.Chars {
				subscriberCountsBefore[c.UUID()] = c.SubscriberCount()
			}

			// Call actual cancel
			cancel()

			// Wait for goroutines to exit
			time.Sleep(CleanupDelay)

			// Verify state AFTER cancel
			var errors CleanupErrors

			// Context must be cancelled (FIX: nil check for diag.sub)
			if diag.sub != nil && diag.sub.ctx.Err() == nil {
				errors = append(errors, CleanupError{"context", "not cancelled"})
			}

			// Channel must be drained (allow small buffer for race) - FIX: nil check
			if diag.sub != nil && len(diag.sub.updates) > 0 {
				errors = append(errors, CleanupError{"channel",
					fmt.Sprintf("%d values remaining", len(diag.sub.updates))})
			}

			// Subscriber counts must decrease (we unregistered)
			for _, c := range diag.Chars {
				before := subscriberCountsBefore[c.UUID()]
				after := c.SubscriberCount()
				if after >= before && before > 0 {
					errors = append(errors, CleanupError{"subscribers",
						fmt.Sprintf("char %s: count didn't decrease (%d -> %d)", c.UUID(), before, after)})
				}
			}

			// Fan-outs we started must be stopped (if we were last subscriber)
			for _, c := range diag.Chars {
				if diag.FirstSubscriberFor[c.UUID()] {
					if c.HasActiveFanOut() && subscriberCountsBefore[c.UUID()] == 1 {
						errors = append(errors, CleanupError{"fanout",
							fmt.Sprintf("fan-out for %s still running (we were last)", c.UUID())})
					}
				}
			}

			// Subscription goroutine must have exited - FIX: nil check
			if diag.sub != nil && diag.sub.Name != "" && groutine.IsRunning(diag.sub.Name) {
				errors = append(errors, CleanupError{"subscription_goroutine",
					fmt.Sprintf("%q still running", diag.sub.Name)})
			}

			// Fan-out goroutines we started must have exited
			for _, c := range diag.Chars {
				if diag.FirstSubscriberFor[c.UUID()] && subscriberCountsBefore[c.UUID()] == 1 {
					fanoutName := fmt.Sprintf("fanout-%s", c.UUID())
					if groutine.IsRunning(fanoutName) {
						errors = append(errors, CleanupError{"fanout_goroutine",
							fmt.Sprintf("%q still running", fanoutName)})
					}
				}
			}

			// Goroutine delta check (allow +2 for system goroutines)
			delta := diag.GoroutinesDelta()
			if delta > 2 {
				errors = append(errors, CleanupError{"goroutine_leak",
					fmt.Sprintf("delta is +%d (possible leak)", delta)})
			}

			// Verify chars have zero subscribers when we were last subscriber
			for _, c := range diag.Chars {
				if subscriberCountsBefore[c.UUID()] == 1 && c.SubscriberCount() > 0 {
					errors = append(errors, CleanupError{
						Check:   "char_subscribers_zero",
						Message: fmt.Sprintf("char %s has %d subscribers (MUST BE 0, we were last)", c.UUID(), c.SubscriberCount()),
					})
				}
			}

			// NOTE: Fan-out verification is handled above at lines 578-586 with correct
			// "we were last subscriber" condition. No duplicate check needed here.

			// BLEValue pool check
			outstanding := diag.conn.valuePool.Outstanding()
			if outstanding > diag.ValuesBeforeCancel {
				errors = append(errors, CleanupError{"blevalue_pool",
					fmt.Sprintf("outstanding increased: %d -> %d", diag.ValuesBeforeCancel, outstanding)})
			}

			// Store results
			diag.CleanupVerified = true
			diag.CleanupErrors = errors

			// Call DiagnosticPanic if errors (respects PanicOnCleanupFailure flag)
			if len(errors) > 0 {
				var connDiag *ConnectionDiagnostics
				if diag.conn != nil {
					connDiag = diag.conn.ConnDiag
				}
				DiagnosticPanic(connDiag, errors, fmt.Sprintf("cancel() for subscription %q", diag.sub.Name))
			}
		}) // End of once.Do
	}
}

// ----------------------------
// Diagnostic Functions
// ----------------------------

// buildDiagnosticDump builds a comprehensive debug dump for cleanup errors.
// Used by both DiagnosticPanic (hard failure) and AssertDiagnostic (soft failure).
func buildDiagnosticDump(connDiag *ConnectionDiagnostics, errors CleanupErrors, context string) string {
	var dump strings.Builder
	dump.WriteString("\n")
	dump.WriteString("╔══════════════════════════════════════════════════════════════════════════════╗\n")
	dump.WriteString("║               SUBSCRIPTION CLEANUP VERIFICATION FAILED                       ║\n")
	dump.WriteString("╚══════════════════════════════════════════════════════════════════════════════╝\n")
	dump.WriteString(fmt.Sprintf("\nContext: %s\n", context))
	dump.WriteString(fmt.Sprintf("Errors (%d):\n", len(errors)))
	for i, err := range errors {
		dump.WriteString(fmt.Sprintf("  [%d] %s: %s\n", i+1, err.Check, err.Message))
	}

	// Dump connection diagnostics
	if connDiag != nil {
		dump.WriteString("\n┌─────────────────────────────────────────────────────────────────────────────┐\n")
		dump.WriteString("│ CONNECTION DIAGNOSTICS                                                       │\n")
		dump.WriteString("└─────────────────────────────────────────────────────────────────────────────┘\n")
		dump.WriteString(fmt.Sprintf("  ValuesAtConnect: %d\n", connDiag.ValuesAtConnect))
		dump.WriteString(fmt.Sprintf("  DisconnectVerified: %v\n", connDiag.DisconnectVerified))

		dump.WriteString(fmt.Sprintf("  CurrentOutstanding: %d\n", connDiag.OutstandingValues()))
		dump.WriteString(fmt.Sprintf("  HighWaterMark: %d\n", connDiag.ValuesHighWaterMark()))

		dump.WriteString(fmt.Sprintf("  Subscriptions (%d):\n", len(connDiag.Subscriptions)))
		for i, sub := range connDiag.Subscriptions {
			dump.WriteString(fmt.Sprintf("\n  ── Subscription [%d]: %q ──\n", i+1, sub.Name))
			if sub.Diag != nil {
				d := sub.Diag
				dump.WriteString(fmt.Sprintf("    CreatedAt: %s\n", d.CreatedAt.Format("15:04:05.000")))
				dump.WriteString(fmt.Sprintf("    CharUUIDs: %v\n", d.CharUUIDs))
				dump.WriteString(fmt.Sprintf("    FirstSubscriberFor: %v\n", d.FirstSubscriberFor))
				dump.WriteString(fmt.Sprintf("    GoroutinesAtStart: %d (delta: %+d)\n", d.GoroutinesAtStart, d.GoroutinesDelta()))
				dump.WriteString(fmt.Sprintf("    CallbackCount: %d\n", d.CallbackCount.Load()))
				dump.WriteString(fmt.Sprintf("    ValuesProcessed: %d\n", d.ValuesProcessed.Load()))
				dump.WriteString(fmt.Sprintf("    DrainedCount: %d\n", d.DrainedCount.Load()))
				dump.WriteString(fmt.Sprintf("    CancelReason: %v\n", d.CancelReason))
				if !d.CancelledAt.IsZero() {
					dump.WriteString(fmt.Sprintf("    CancelledAt: %s\n", d.CancelledAt.Format("15:04:05.000")))
				}
				dump.WriteString(fmt.Sprintf("    CleanedUp: %v\n", d.CleanedUp))
				if !d.CleanedUpAt.IsZero() {
					dump.WriteString(fmt.Sprintf("    CleanedUpAt: %s\n", d.CleanedUpAt.Format("15:04:05.000")))
				}
				dump.WriteString(fmt.Sprintf("    PanicRecovered: %v\n", d.PanicRecovered.Load()))
				dump.WriteString(fmt.Sprintf("    CleanupVerified: %v\n", d.CleanupVerified))
				if len(d.CleanupErrors) > 0 {
					dump.WriteString(fmt.Sprintf("    CleanupErrors: %v\n", d.CleanupErrors))
				}

				// Char state with verification warnings
				for _, c := range d.Chars {
					subscriberCount := c.SubscriberCount()
					fanoutActive := c.HasActiveFanOut()
					weStartedFanOut := d.FirstSubscriberFor[c.UUID()]

					subscriberWarning := ""
					if subscriberCount > 0 {
						subscriberWarning = " (MUST BE 0)"
					}
					fanoutWarning := ""
					if weStartedFanOut && fanoutActive {
						fanoutWarning = " (MUST BE STOPPED)"
					}

					dump.WriteString(fmt.Sprintf("    Char %s: subscribers=%d%s, fanoutActive=%v%s\n",
						c.UUID(), subscriberCount, subscriberWarning, fanoutActive, fanoutWarning))
				}
			} else {
				dump.WriteString("    Diag: <nil>\n")
			}
		}
	}

	// Dump goroutines with names/tags
	dump.WriteString("\n┌─────────────────────────────────────────────────────────────────────────────┐\n")
	dump.WriteString("│ GOROUTINE DUMP                                                               │\n")
	dump.WriteString("└─────────────────────────────────────────────────────────────────────────────┘\n")
	dump.WriteString(fmt.Sprintf("  Total goroutines: %d\n\n", runtime.NumGoroutine()))
	dump.WriteString(groutine.Dump())

	dump.WriteString("\n╔══════════════════════════════════════════════════════════════════════════════╗\n")
	dump.WriteString(fmt.Sprintf("║ PanicOnCleanupFailure: %-54v ║\n", panicOnCleanupFailure))
	dump.WriteString("╚══════════════════════════════════════════════════════════════════════════════╝\n")

	return dump.String()
}

// DiagnosticPanic logs cleanup errors and dumps full subscription state for debugging.
// If PanicOnCleanupFailure() is true, panics to fail the test (fail-fast).
// If PanicOnCleanupFailure() is false, just logs and returns (errors still collected).
// Use for: Disconnect() and cancel() verification where immediate failure is desired.
func DiagnosticPanic(connDiag *ConnectionDiagnostics, errors CleanupErrors, context string) {
	if len(errors) == 0 {
		return
	}

	dump := buildDiagnosticDump(connDiag, errors, context)
	fmt.Print(dump)

	if panicOnCleanupFailure {
		panic(fmt.Sprintf("SUBSCRIPTION CLEANUP FAILED: %s", errors.Error()))
	}
}

// AssertDiagnostic reports cleanup errors via t.Errorf() - test continues but is marked failed.
// Use for: t.Cleanup() verification where we want ALL connections' errors reported.
func AssertDiagnostic(t *testing.T, connDiag *ConnectionDiagnostics, errors CleanupErrors, context string) {
	if len(errors) == 0 {
		return
	}

	dump := buildDiagnosticDump(connDiag, errors, context)
	t.Errorf("%s", dump)
}

// ----------------------------
// Test Connection Cleanup Registration
// ----------------------------

// testOwner tracks which test currently owns connections (parallel detection).
var testOwner string

// connectionCount is the refcount of active connections for the current test.
var connectionCount int

// RegisterTestConnectionCleanup registers cleanup verification for a test connection.
// Called from OnConnected hook in test builds. Handles:
//   - Owner tracking (parallel test detection)
//   - Refcount for proper owner reset
//   - t.Cleanup() registration for automatic verification
//
// Error reporting uses AssertDiagnostic (soft failure) so ALL connections' errors are reported.
func RegisterTestConnectionCleanup(testName string, t *testing.T, c *BLEConnection) {
	// baseTestName extracts "TestSuite/TestFunc" from "TestSuite/TestFunc/subtest/...".
	// Allows subtests within the same test function to share owner.
	baseTestName := func(fullName string) string {
		parts := strings.SplitN(fullName, "/", 3)
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
		return fullName
	}

	// Parallel test detection: compare base names to allow subtests of the same test function.
	baseName := baseTestName(testName)
	if testOwner != "" && testOwner != baseName {
		t.Fatalf("BUG: OnConnected hook owned by test %q but connection from %q. "+
			"This indicates parallel test execution which is NOT supported for BLE device_test. "+
			"Ensure device_test run sequentially (no t.Parallel()). connectionCount=%d", testOwner, baseName, connectionCount)
	}
	testOwner = baseName
	connectionCount++
	t.Logf("[DEBUG] RegisterCleanup: conn=%p testOwner=%q connectionCount=%d testName=%q\n", c, testOwner, connectionCount, testName)

	t.Cleanup(func() {
		// ALWAYS decrement via defer (runs even if we somehow panic)
		defer func() {
			connectionCount--
			newOwner := testOwner
			if connectionCount == 0 {
				testOwner = ""
				newOwner = ""
			}
			fmt.Printf("[DEBUG] Cleanup done: conn=%p verified=%v testOwner=%q connectionCount=%d\n", c, c.ConnDiag != nil && c.ConnDiag.DisconnectVerified, newOwner, connectionCount)
		}()

		// Collect ALL errors for this connection
		var allErrors CleanupErrors

		if c.ConnDiag != nil {
			if c.ConnDiag.DisconnectVerified {
				return // Already verified via Disconnect() - all good
			}

			// Connection was never Disconnect()'d - this is a test bug
			allErrors = append(allErrors, CleanupError{
				Check:   "connection_not_disconnected",
				Message: fmt.Sprintf("connection %p was never Disconnect()'d - test must call Disconnect()", c),
			})

			// Also run subscription verification to get the full picture
			subErrors := c.ConnDiag.VerifyDisconnectCleanup()
			allErrors = append(allErrors, subErrors...)
		}

		// Soft failure - report via t.Errorf, test continues
		if len(allErrors) > 0 {
			AssertDiagnostic(t, c.ConnDiag, allErrors, "t.Cleanup - connection cleanup verification")
		}
	})
}
