//go:build !test

package goble

// ----------------------------
// Subscription Diagnostics (no-op in production)
// ----------------------------

// SubscriptionDiagnostics captures 	subscription lifecycle for testing.
// In production: nil, all methods are nil-safe no-ops.
type SubscriptionDiagnostics struct {
	CleanedUp bool // Only field needed for nil-safe checks
}

func newSubscriptionDiagnostics(_ []*BLECharacteristic) *SubscriptionDiagnostics {
	return nil
}

func (d *SubscriptionDiagnostics) MarkFirstSubscriber(_ string) {}
func (d *SubscriptionDiagnostics) SetReferences(_ *Subscription, _ *BLEConnection) {
}
func (d *SubscriptionDiagnostics) MarkExplicitCancel() {}
func (d *SubscriptionDiagnostics) MarkImplicitCancel() {}
func (d *SubscriptionDiagnostics) MarkCleanedUp()      {}
func (d *SubscriptionDiagnostics) MarkPanicRecovered() {}
func (d *SubscriptionDiagnostics) MarkFirstValue()     {}
func (d *SubscriptionDiagnostics) IncrementCallback()  {}
func (d *SubscriptionDiagnostics) IncrementProcessed() {}
func (d *SubscriptionDiagnostics) AddDrained(_ int)    {}

// wrapCancelFunc is the nil-safe helper called from SubscribeWithName.
// In production: returns raw cancel (zero overhead).
func wrapCancelFunc(_ *SubscriptionDiagnostics, cancel func()) func() {
	return cancel
}

// PanicOnCleanupFailure returns false in production (no verification runs).
func PanicOnCleanupFailure() bool { return false }

// SetPanicOnCleanupFailure is a no-op in production, returns empty restore function.
func SetPanicOnCleanupFailure(_ bool) func() { return func() {} }

// DiagnosticPanic is a no-op in production.
func DiagnosticPanic(_ *ConnectionDiagnostics, _ CleanupErrors, _ string) {}

// ----------------------------
// Connection Diagnostics (no-op in production)
// ----------------------------

// ConnectionDiagnostics tracks all subscriptions on a connection.
// In production: nil, verification returns nil.
type ConnectionDiagnostics struct{}

// CleanupError represents a single cleanup verification failure.
type CleanupError struct {
	Check   string
	Message string
}

// CleanupErrors is a collection of cleanup verification failures.
type CleanupErrors []CleanupError

func (e CleanupErrors) Error() string { return "" }

func newConnectionDiagnostics(c *BLEConnection) *ConnectionDiagnostics { return nil }
func (cd *ConnectionDiagnostics) TrackSubscription(_ *Subscription)    {}
func (cd *ConnectionDiagnostics) VerifyDisconnectCleanup() CleanupErrors {
	return nil
}
