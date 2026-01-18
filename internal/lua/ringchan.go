package lua

import "sync/atomic"

// RingChannel is a bounded channel-like buffer with overwrite-oldest semantics.
//
// It wraps an underlying buffered channel and ensures producers never block
// indefinitely: if the buffer is full, the oldest element is discarded.
//
// # Example
//
//	rc := internal.NewRingChannel
//
//	// Writer: always succeeds, drops oldest if full.
//	for i := 0; i < 10; i++ {
//	    rc.Send(i)
//	}
//
//	// Reader: acts like a normal Go channel.
//	for v := range rc.C() {
//	    fmt.Println("got:", v)
//	}
//
// In the example above, only the *last 3* values will be printed because
// earlier ones were overwritten.
//
// Writers use methods like Send, TrySend, or ForceSend.
// Readers can use C() for a normal <-chan T, or Receive()/TryReceive() for metric tracking.
type RingChannel[T any] struct {
	ch      chan T
	metrics Metrics // lock-free metrics tracking
}

// NewRingChannel New creates a RingChan with the given capacity.
func NewRingChannel[T any](capacity int) *RingChannel[T] {
	if capacity <= 0 {
		panic("ringchan: capacity must be > 0")
	}
	return &RingChannel[T]{ch: make(chan T, capacity)}
}

// C returns the underlying receive-only channel.
// Consumers can range over this until it's closed.
//
// WARNING: Reading from the returned channel bypasses metrics tracking.
// The Processed metric will NOT be incremented for reads via C().
// Use Receive() or TryReceive() if you need metrics tracking.
func (rc *RingChannel[T]) C() <-chan T {
	return rc.ch
}

// Send inserts an item. If the buffer is full, it discards the oldest.
// This call always succeeds and never blocks indefinitely.
func (rc *RingChannel[T]) Send(v T) {
	select {
	case rc.ch <- v:
		rc.metrics.addWritten(1)
	default:
		<-rc.ch // drop oldest
		rc.metrics.addOverwritten(1)
		rc.ch <- v
		rc.metrics.addWritten(1)
	}
}

// TrySend attempts to insert without blocking.
// Returns true if successful, false if the buffer is full.
func (rc *RingChannel[T]) TrySend(v T) bool {
	select {
	case rc.ch <- v:
		rc.metrics.addWritten(1)
		return true
	default:
		return false
	}
}

// ForceSend always succeeds immediately, discarding the oldest if needed.
// It never blocks.
func (rc *RingChannel[T]) ForceSend(v T) bool {
	dropped := false

	select {
	case rc.ch <- v:
		rc.metrics.addWritten(1)
	default:
		select {
		case <-rc.ch: // drop oldest
			rc.metrics.addOverwritten(1)
			dropped = true
		default:
		}
		rc.ch <- v
		rc.metrics.addWritten(1)
	}

	return dropped
}

// Receive blocks until a value is available or the channel is closed.
// The ok result is false if the channel is closed.
func (rc *RingChannel[T]) Receive() (v T, ok bool) {
	v, ok = <-rc.ch
	if ok {
		rc.metrics.addProcessed(1)
	}
	return
}

// TryReceive attempts a non-blocking receive.
// Returns (zero, false) if no value is ready.
func (rc *RingChannel[T]) TryReceive() (v T, ok bool) {
	select {
	case v, ok = <-rc.ch:
		if ok {
			rc.metrics.addProcessed(1)
		}
		return
	default:
		var zero T
		return zero, false
	}
}

// Len returns the number of buffered elements.
func (rc *RingChannel[T]) Len() int {
	return len(rc.ch)
}

// Cap returns the channel capacity.
func (rc *RingChannel[T]) Cap() int {
	return cap(rc.ch)
}

// Close closes the underlying channel. After this, Send/ForceSend panics.
func (rc *RingChannel[T]) Close() {
	close(rc.ch)
}

// GetMetrics returns a snapshot of current metrics values.
// All reads are atomic and thread-safe.
//
// Note: The Processed counter is only incremented by Receive() and TryReceive().
// Reads via C() bypass metrics and will not be counted.
func (rc *RingChannel[T]) GetMetrics() Metrics {
	return Metrics{
		Processed:   atomic.LoadInt64(&rc.metrics.Processed),
		Written:     atomic.LoadInt64(&rc.metrics.Written),
		Overwritten: atomic.LoadInt64(&rc.metrics.Overwritten),
		Errors:      atomic.LoadInt64(&rc.metrics.Errors),
	}
}

// Metrics provides lock-free metrics tracking for RingChannel.
//
// All fields use atomic operations for thread-safe access
type Metrics struct {
	Processed   int64
	Written     int64
	Overwritten int64
	Errors      int64
}

func (m *Metrics) addProcessed(n int) {
	atomic.AddInt64(&m.Processed, int64(n))
}

func (m *Metrics) addWritten(n int) {
	atomic.AddInt64(&m.Written, int64(n))
}

func (m *Metrics) addOverwritten(n int) {
	atomic.AddInt64(&m.Overwritten, int64(n))
}

func (m *Metrics) addError() {
	atomic.AddInt64(&m.Errors, 1)
}
