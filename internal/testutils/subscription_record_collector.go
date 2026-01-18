//go:build test

package testutils

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hedzr/go-ringbuf/v2/mpmc"
	"github.com/srg/blim/internal/device"
)

// SubscriptionRecordCollector captures subscription records for test verification.
// It performs pure capture (no assertions or expectations) and supports
// multi-characteristic subscriptions.
//
// Usage:
//
//	collector := NewSubscriptionRecordCollector()
//	conn.Subscribe(opts, mode, rate, collector.Callback())
//	simulator.Simulate(false)
//	require.True(t, collector.WaitForCount(3, 2*time.Second), "timeout")
//	assert.Len(t, collector.Values("2a37"), 3)
//	assert.Len(t, collector.Values("2a38"), 3)
type SubscriptionRecordCollector struct {
	records mpmc.RichOverlappedRingBuffer[*device.Record]
}

// NewSubscriptionRecordCollector creates a collector for subscription records.
func NewSubscriptionRecordCollector(capacity uint32) *SubscriptionRecordCollector {
	return &SubscriptionRecordCollector{
		records: mpmc.NewOverlappedRingBuffer[*device.Record](capacity),
	}
}

// Callback returns func(*device.Record) for use with Subscribe().
// Thread-safe: can be called concurrently from subscription goroutines.
// Clones records since original data references pooled memory reused after callback.
func (c *SubscriptionRecordCollector) Callback() func(*device.Record) {
	return func(record *device.Record) {
		c.records.Enqueue(record.Clone()) // MUST clone record as after callback fired record will be reused
	}
}

// WaitForCount blocks until count records captured or timeout.
// Returns true if count reached, false on timeout.
func (c *SubscriptionRecordCollector) WaitForCount(count uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n := c.records.Size()
		if n >= count {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// ConsumeRecords returns all currently queued records as a slice and removes
// them from the collector.
func (c *SubscriptionRecordCollector) ConsumeRecords() ([]*device.Record, error) {
	var results []*device.Record

	_, err := ConsumeRecords(c, func(r *device.Record) (struct{}, bool, error) {
		// Final call: no more records
		if r == nil {
			return struct{}{}, true, nil
		}

		results = append(results, r)
		return struct{}{}, false, nil
	})

	return results, err
}

func (c *SubscriptionRecordCollector) IsEmpty() bool {
	return c.records.IsEmpty()
}

// ConsumeValues consumes all records and extracts values for the specified
// characteristic UUID. The accumulated values are returned after the final
// record has been processed.
func (c *SubscriptionRecordCollector) ConsumeValues(charUUID string) ([][]byte, error) {
	var result [][]byte

	_, err := ConsumeRecords(c, func(r *device.Record) ([]byte, bool, error) {
		// Final call: no more records
		if r == nil {
			return nil, true, nil
		}

		if v, ok := r.Values[charUUID]; ok {
			result = append(result, v)
		}

		// Continue processing remaining records
		return nil, false, nil
	})

	return result, err
}

// ConsumeRecordsAsJSONString consumes all records and returns a single JSON array
// string, with Values and BatchValues represented as hex strings.
func (c *SubscriptionRecordCollector) ConsumeRecordsAsJSONString() (string, error) {
	var jsonStrs []string

	_, err := ConsumeRecords(c, func(r *device.Record) (struct{}, bool, error) {
		// Final call: no more records
		if r == nil {
			return struct{}{}, true, nil
		}

		wrapped := DeviceRecordJSON{r}
		b, err := json.Marshal(wrapped)
		if err != nil {
			return struct{}{}, true, err
		}

		jsonStrs = append(jsonStrs, string(b))
		return struct{}{}, false, nil
	})
	if err != nil {
		return "", err
	}

	if len(jsonStrs) == 0 {
		return "[]", nil
	}
	return "[" + strings.Join(jsonStrs, ",") + "]", nil
}

// ConsumerFunc processes a record and optionally signals early termination.
//
// Protocol:
//   - When record != nil:
//   - Process the record.
//   - Return (result, false, nil) to continue.
//   - Return (result, true, nil) to stop early.
//   - When record == nil:
//   - No more records remain.
//   - Return the final accumulated result with done=true.
type ConsumerFunc[T any] func(record *device.Record) (result T, done bool, err error)

// ConsumeRecords drains records from the collector and invokes the consumer
// sequentially until completion or early termination.
func ConsumeRecords[T any](c *SubscriptionRecordCollector, consumer ConsumerFunc[T]) (T, error) {
	for !c.records.IsEmpty() {
		rec, err := c.records.Dequeue()
		if err != nil {
			var zero T
			return zero, fmt.Errorf("record dequeue error: %w", err)
		}

		result, done, err := consumer(rec)
		if err != nil {
			return result, err
		}
		if done {
			return result, nil
		}
	}

	// Signal completion and retrieve the final result
	result, _, err := consumer(nil)
	return result, err
}
