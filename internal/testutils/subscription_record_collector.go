//go:build test

package testutils

import (
	"sync"
	"time"

	"github.com/srg/blim/internal/device"
)

// SubscriptionRecordCollector captures subscription records for test verification.
// Pure capture - no expectations, supports multi-char subscriptions.
//
// Usage:
//
//	collector := NewSubscriptionRecordCollector()
//	conn.Subscribe(opts, mode, rate, collector.Callback())
//	simulator.Simulate(false)
//	require.True(t, collector.WaitForCount(3, 2*time.Second), "timeout")
//	assert.Len(t, collector.Values("2a37"), 3)
//	assert.Len(t, collector.Values("2a38"), 3)  // multi-char support
type SubscriptionRecordCollector struct {
	mu      sync.Mutex
	records []*device.Record
}

// NewSubscriptionRecordCollector creates a collector for subscription records.
func NewSubscriptionRecordCollector() *SubscriptionRecordCollector {
	return &SubscriptionRecordCollector{
		records: make([]*device.Record, 0),
	}
}

// Callback returns func(*device.Record) for use with Subscribe().
// Thread-safe: can be called concurrently from subscription goroutines.
// Clones records since original data references pooled memory reused after callback.
func (c *SubscriptionRecordCollector) Callback() func(*device.Record) {
	return func(record *device.Record) {
		c.mu.Lock()
		c.records = append(c.records, record.Clone())
		c.mu.Unlock()
	}
}

// WaitForCount blocks until count records captured or timeout.
// Returns true if count reached, false on timeout.
func (c *SubscriptionRecordCollector) WaitForCount(count int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.records)
		c.mu.Unlock()
		if n >= count {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Records returns all captured records.
func (c *SubscriptionRecordCollector) Records() []*device.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]*device.Record, len(c.records))
	copy(result, c.records)
	return result
}

// Count returns number of records captured.
func (c *SubscriptionRecordCollector) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

// Values extracts record.Values[charUUID] from each record.
func (c *SubscriptionRecordCollector) Values(charUUID string) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][]byte
	for _, r := range c.records {
		if v, ok := r.Values[charUUID]; ok {
			result = append(result, v)
		}
	}
	return result
}

// BatchValues extracts record.BatchValues[charUUID] preserving batch boundaries.
func (c *SubscriptionRecordCollector) BatchValues(charUUID string) [][][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][][]byte
	for _, r := range c.records {
		if v, ok := r.BatchValues[charUUID]; ok {
			result = append(result, v)
		}
	}
	return result
}

// FlattenedBatchValues returns all batch values for charUUID flattened.
func (c *SubscriptionRecordCollector) FlattenedBatchValues(charUUID string) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result [][]byte
	for _, r := range c.records {
		if batches, ok := r.BatchValues[charUUID]; ok {
			result = append(result, batches...)
		}
	}
	return result
}

// LastValue returns most recent value for charUUID, or nil.
func (c *SubscriptionRecordCollector) LastValue(charUUID string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.records) - 1; i >= 0; i-- {
		if v, ok := c.records[i].Values[charUUID]; ok {
			return v
		}
	}
	return nil
}