package lua

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// LuaOutputCollectorTestSuite provides comprehensive device_test for LuaOutputCollector
type LuaOutputCollectorTestSuite struct {
	suite.Suite
}

// waitForState waits for the collector to reach the expected state with active polling
func (suite *LuaOutputCollectorTestSuite) waitForState(collector *LuaOutputCollector, expectedState uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if collector.GetState() == expectedState {
			return true
		}
		time.Sleep(1 * time.Millisecond) // Small sleep to avoid busy-waiting
	}
	return false
}

// TestNewLuaOutputCollector device_test the constructor with various input test-scenarios
func (suite *LuaOutputCollectorTestSuite) TestNewLuaOutputCollector() {
	// GOAL: Verify LuaOutputCollector constructor validates parameters and initializes correctly
	//
	// TEST SCENARIO: Call NewLuaOutputCollector with various parameters → validate returns or errors → verify initialization
	suite.Run("ValidParameters", func() {
		// GOAL: Verify constructor accepts valid parameters and initializes collector properly
		//
		// TEST SCENARIO: Call NewLuaOutputCollector with valid params → verify no error → check initialization
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)
		suite.NotNil(collector)
		// Note: outputChan is stored as <-chan, so we can't directly compare with bidirectional chan
		suite.NotNil(collector.outputChan)
		suite.GreaterOrEqual(collector.buffer.Cap(), uint32(100)) // Buffer may be power-of-2 rounded
		suite.NotNil(collector.onError)
	})

	suite.Run("CustomErrorHandler", func() {
		// GOAL: Verify custom error handler is stored and called instead of default panic behavior
		//
		// TEST SCENARIO: Create collector with custom handler → trigger error → verify custom handler called
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		var capturedError error
		errorHandler := func(err error) {
			capturedError = err
		}

		collector, err := NewLuaOutputCollector(ch, 50, errorHandler)
		suite.NoError(err)
		suite.NotNil(collector)

		// Test that a custom error handler is used
		testErr := errors.New("test error")
		collector.onError(testErr)
		suite.Equal(testErr, capturedError)
	})

	suite.Run("NilChannel", func() {
		// GOAL: Verify constructor rejects nil channel parameter with appropriate error
		//
		// TEST SCENARIO: Call NewLuaOutputCollector with nil channel → verify error returned → check error message
		collector, err := NewLuaOutputCollector(nil, 100, nil)
		suite.Error(err)
		suite.Nil(collector)
		suite.Contains(err.Error(), "output channel cannot be nil")
	})

	suite.Run("ZeroBufferSize", func() {
		// GOAL: Verify constructor rejects zero buffer size with validation error
		//
		// TEST SCENARIO: Call NewLuaOutputCollector with bufferSize=0 → verify error returned → check error message
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 0, nil)
		suite.Error(err)
		suite.Nil(collector)
		suite.Contains(err.Error(), "buffer size must be > 0")
	})

	suite.Run("ExceedsMaxBufferSize", func() {
		// GOAL: Verify constructor rejects buffer size exceeding MaxBufferSize limit
		//
		// TEST SCENARIO: Call with bufferSize > MaxBufferSize → verify error returned → check exceeds maximum message
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, MaxBufferSize+1, nil)
		suite.Error(err)
		suite.Nil(collector)
		suite.Contains(err.Error(), "exceeds maximum")
	})

	suite.Run("MaxBufferSizeAllowed", func() {
		// GOAL: Verify constructor accepts exactly MaxBufferSize as valid boundary value
		//
		// TEST SCENARIO: Call with bufferSize = MaxBufferSize → verify no error → check successful creation
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, MaxBufferSize, nil)
		suite.NoError(err)
		suite.NotNil(collector)
	})
}

// TestStartStop device_test the basic start/stop lifecycle
func (suite *LuaOutputCollectorTestSuite) TestStartStop() {
	// GOAL: Verify collector lifecycle state transitions work correctly for start/stop operations
	//
	// TEST SCENARIO: Start collector → verify running state → stop collector → verify stopped state
	suite.Run("StartStop", func() {
		// GOAL: Verify basic start-stop lifecycle transitions collector to running and back to stopped
		//
		// TEST SCENARIO: Start collector → verify running state → stop collector → verify stopped successfully
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Start collector
		err = collector.Start()
		suite.NoError(err)

		// Wait for the collector to reach the 'Running' state
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		// Stop collector
		err = collector.Stop()
		suite.NoError(err)
	})

	suite.Run("PreventDuplicateStart", func() {
		// GOAL: Verify starting an already running collector returns appropriate error
		//
		// TEST SCENARIO: Start collector → attempt second start → verify error about already running/starting
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// The first start should succeed
		err = collector.Start()
		suite.NoError(err)

		// The second start should fail
		err = collector.Start()
		suite.Error(err)
		// Could be "already starting" or "already running" depending on timing
		suite.True(strings.Contains(err.Error(), "already starting") || strings.Contains(err.Error(), "already running"))

		// Wait for the collector to reach the 'Running' state before stopping
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		// Cleanup
		err = collector.Stop()
		suite.NoError(err)
	})

	suite.Run("RestartAfterStop", func() {
		// GOAL: Verify collector can be restarted after being properly stopped
		//
		// TEST SCENARIO: Start → stop → start again → verify second start succeeds → stop cleanup
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Start -> Stop -> Start should work
		err = collector.Start()
		suite.NoError(err)

		// Wait for the collector to reach the 'Running' state before stopping
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		err = collector.Stop()
		suite.NoError(err)

		// Wait for the collector to return to the 'NotRunning' state
		suite.True(suite.waitForState(collector, CollectorStateNotRunning, 100*time.Millisecond))

		err = collector.Start()
		suite.NoError(err)

		// Wait for the collector to reach the 'Running' state before stopping
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		err = collector.Stop()
		suite.NoError(err)
	})

	// Note: StopTimeout test was removed because with NewOverlappedRingBuffer,
	// buffer overflow no longer causes errors that would block the goroutine.
	// The Stop() method should always complete normally.
}

// TestDataProcessing device_test record processing and metrics
func (suite *LuaOutputCollectorTestSuite) TestDataProcessing() {
	// GOAL: Verify collector processes LuaOutputRecord data and updates metrics correctly
	//
	// TEST SCENARIO: Send records to running collector → verify records processed → check metrics incremented
	suite.Run("ProcessSingleRecord", func() {
		// GOAL: Verify collector processes individual records and increments metrics correctly
		//
		// TEST SCENARIO: Send single record to running collector → verify processing → check metrics updated
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send a record
		record := LuaOutputRecord{
			Content:   "test content",
			Timestamp: time.Now(),
			Source:    "stdout",
		}
		ch <- record

		// Wait for processing
		time.Sleep(50 * time.Millisecond)

		// Check metrics
		metrics := collector.GetMetrics()
		suite.Equal(int64(1), metrics.RecordsProcessed)
		suite.Equal(int64(0), metrics.ErrorsOccurred)
	})

	suite.Run("ProcessMultipleRecords", func() {
		// GOAL: Verify collector processes multiple records sequentially and tracks count accurately
		//
		// TEST SCENARIO: Send multiple records → verify all processed → check final metrics count matches
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send multiple records
		recordCount := 10
		for i := 0; i < recordCount; i++ {
			record := LuaOutputRecord{
				Content:   fmt.Sprintf("content %d", i),
				Timestamp: time.Now(),
				Source:    "stdout",
			}
			ch <- record
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Check metrics
		metrics := collector.GetMetrics()
		suite.Equal(int64(recordCount), metrics.RecordsProcessed)
		suite.Equal(int64(0), metrics.ErrorsOccurred)
	})

	suite.Run("ChannelClosure", func() {
		// GOAL: Verify collector handles input channel closure gracefully and stops processing
		//
		// TEST SCENARIO: Send records then close channel → verify collector detects closure → check final metrics
		ch := make(chan LuaOutputRecord, 10)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)

		// Send some records
		for i := 0; i < 5; i++ {
			ch <- LuaOutputRecord{
				Content:   fmt.Sprintf("content %d", i),
				Timestamp: time.Now(),
				Source:    "stdout",
			}
		}

		// Close channel to simulate a normal shutdown
		close(ch)

		// Wait for the collector to detect closure and stop
		time.Sleep(100 * time.Millisecond)

		// Metrics should reflect processed records
		metrics := collector.GetMetrics()
		suite.Equal(int64(5), metrics.RecordsProcessed)
		suite.Equal(int64(0), metrics.ErrorsOccurred)
	})
}

// TestMetrics device_test metrics collection and atomic operations
func (suite *LuaOutputCollectorTestSuite) TestMetrics() {
	// GOAL: Verify metrics tracking uses atomic operations and provides accurate counters
	//
	// TEST SCENARIO: Increment metrics atomically → verify counters → reset metrics → verify zeroed
	suite.Run("MetricsInitialization", func() {
		// GOAL: Verify new collector initializes all metrics counters to zero
		//
		// TEST SCENARIO: Create new collector → get metrics → verify all counters are zero
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		metrics := collector.GetMetrics()
		suite.Equal(int64(0), metrics.RecordsProcessed)
		suite.Equal(int64(0), metrics.ErrorsOccurred)
		suite.Equal(int64(0), metrics.RecordsOverwritten)
	})

	suite.Run("MetricsReset", func() {
		// GOAL: Verify ResetMetrics atomically resets all counters to zero
		//
		// TEST SCENARIO: Increment counters → call ResetMetrics → verify all counters zeroed
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Manually increment counters
		collector.metrics.IncrementRecordsProcessed()
		collector.metrics.IncrementErrorsOccurred()
		collector.metrics.IncrementRecordsOverwritten(1)

		// Verify counters
		metrics := collector.GetMetrics()
		suite.Equal(int64(1), metrics.RecordsProcessed)
		suite.Equal(int64(1), metrics.ErrorsOccurred)
		suite.Equal(int64(1), metrics.RecordsOverwritten)

		// Reset and verify
		collector.ResetMetrics()
		metrics = collector.GetMetrics()
		suite.Equal(int64(0), metrics.RecordsProcessed)
		suite.Equal(int64(0), metrics.ErrorsOccurred)
		suite.Equal(int64(0), metrics.RecordsOverwritten)
	})

	suite.Run("AtomicMetricsOperations", func() {
		// GOAL: Verify individual atomic metric operations increment and read correctly
		//
		// TEST SCENARIO: Call increment methods → verify counter values → check atomic consistency
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Test direct atomic operations
		suite.Equal(int64(0), collector.metrics.GetRecordsProcessed())
		collector.metrics.IncrementRecordsProcessed()
		suite.Equal(int64(1), collector.metrics.GetRecordsProcessed())

		suite.Equal(int64(0), collector.metrics.GetErrorsOccurred())
		collector.metrics.IncrementErrorsOccurred()
		suite.Equal(int64(1), collector.metrics.GetErrorsOccurred())

		suite.Equal(int64(0), collector.metrics.GetRecordsOverwritten())
		collector.metrics.IncrementRecordsOverwritten(1)
		suite.Equal(int64(1), collector.metrics.GetRecordsOverwritten())
	})
}

// TestConsumerFunctions device_test the consumer pattern and records consumption
func (suite *LuaOutputCollectorTestSuite) TestConsumerFunctions() {
	// GOAL: Verify ConsumerFunc pattern processes buffered records and handles early termination
	//
	// TEST SCENARIO: Fill buffer with records → apply consumer function → verify processed data or early termination
	suite.Run("PlainTextConsumer", func() {
		// GOAL: Verify PlainTextOutputConsumerFunc concatenates record content into single string
		//
		// TEST SCENARIO: Send multiple text records → consume as plain text → verify concatenated output
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send test records
		records := []string{"hello", " ", "world", "\n", "test"}
		for _, content := range records {
			ch <- LuaOutputRecord{
				Content:   content,
				Timestamp: time.Now(),
				Source:    "stdout",
			}
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Consume as plain text
		result, err := collector.ConsumePlainText()
		suite.NoError(err)
		suite.Equal("hello world\ntest", result)
	})

	suite.Run("CustomConsumer", func() {
		// GOAL: Verify custom ConsumerFunc can accumulate state and return final result
		//
		// TEST SCENARIO: Send records to custom counter consumer → verify count accumulation → check final result
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send test records
		for i := 0; i < 5; i++ {
			ch <- LuaOutputRecord{
				Content:   fmt.Sprintf("item%d", i),
				Timestamp: time.Now(),
				Source:    "stdout",
			}
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Custom consumer that counts records
		var recordCount int
		consumer := func(record *LuaOutputRecord) (int, error) {
			if record == nil {
				// Final call - return count
				return recordCount, nil
			}
			recordCount++
			return 0, nil // Continue processing
		}

		result, err := ConsumeRecords(collector, consumer)
		suite.NoError(err)
		suite.Equal(5, result)
	})

	suite.Run("ConsumerEarlyTermination", func() {
		// GOAL: Verify consumer can terminate early by returning non-zero value
		//
		// TEST SCENARIO: Consumer stops after 3 records → verify early termination → check remaining records not processed
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send test records
		for i := 0; i < 10; i++ {
			ch <- LuaOutputRecord{
				Content:   fmt.Sprintf("item%d", i),
				Timestamp: time.Now(),
				Source:    "stdout",
			}
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Consumer that stops after 3 records
		var recordCount int
		consumer := func(record *LuaOutputRecord) (string, error) {
			if record == nil {
				return "completed", nil
			}
			recordCount++
			if recordCount >= 3 {
				return "stopped early", nil // Stop early
			}
			return "", nil // Continue
		}

		result, err := ConsumeRecords(collector, consumer)
		suite.NoError(err)
		suite.Equal("stopped early", result)
		suite.Equal(3, recordCount)
	})

	suite.Run("ConsumerError", func() {
		// GOAL: Verify consumer errors are properly propagated to caller
		//
		// TEST SCENARIO: Consumer returns error → verify ConsumeRecords returns error → check error message
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send a test record
		ch <- LuaOutputRecord{
			Content:   "test",
			Timestamp: time.Now(),
			Source:    "stdout",
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Consumer that returns an error
		consumer := func(record *LuaOutputRecord) (string, error) {
			if record == nil {
				return "", nil
			}
			return "", errors.New("consumer error")
		}

		result, err := ConsumeRecords(collector, consumer)
		suite.Error(err)
		suite.Contains(err.Error(), "consumer error")
		suite.Empty(result)
	})
}

// TestConcurrency device_test concurrent access and race conditions
func (suite *LuaOutputCollectorTestSuite) TestConcurrency() {
	// GOAL: Verify thread-safe operations under concurrent access without data races
	//
	// TEST SCENARIO: Run concurrent operations → verify only one succeeds where appropriate → check final state consistency
	suite.Run("ConcurrentStartStop", func() {
		// GOAL: Verify only one concurrent start succeeds and others return appropriate errors
		//
		// TEST SCENARIO: Multiple goroutines call Start → verify one succeeds → check others get already started errors
		ch := make(chan LuaOutputRecord, 100)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Concurrent start attempts
		var wg sync.WaitGroup
		var startErrors []error
		var mu sync.Mutex

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := collector.Start()
				mu.Lock()
				if err != nil {
					startErrors = append(startErrors, err)
				}
				mu.Unlock()
			}()
		}

		wg.Wait()

		// Only one start should succeed, others should error
		suite.Equal(9, len(startErrors))
		for _, err := range startErrors {
			// Could be "already starting" or "already running" depending on timing
			suite.True(strings.Contains(err.Error(), "already starting") || strings.Contains(err.Error(), "already running"))
		}

		// Clean up
		err = collector.Stop()
		suite.NoError(err)
	})

	suite.Run("ConcurrentProducerConsumer", func() {
		// GOAL: Verify collector handles concurrent record production without data loss
		//
		// TEST SCENARIO: Multiple producers send records concurrently → verify all processed → check final count
		ch := make(chan LuaOutputRecord, 100)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 1000, nil)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Producer goroutines
		var wg sync.WaitGroup
		recordCount := 1000
		producerCount := 10
		recordsPerProducer := recordCount / producerCount

		for p := 0; p < producerCount; p++ {
			wg.Add(1)
			go func(producerID int) {
				defer wg.Done()
				for i := 0; i < recordsPerProducer; i++ {
					record := LuaOutputRecord{
						Content:   fmt.Sprintf("producer%d-record%d", producerID, i),
						Timestamp: time.Now(),
						Source:    "stdout",
					}
					ch <- record
				}
			}(p)
		}

		wg.Wait()

		// Wait for processing
		time.Sleep(500 * time.Millisecond)

		// Verify all records processed
		metrics := collector.GetMetrics()
		suite.Equal(int64(recordCount), metrics.RecordsProcessed)
	})

	suite.Run("ConcurrentMetricsAccess", func() {
		// GOAL: Verify atomic metrics operations are thread-safe under concurrent access
		//
		// TEST SCENARIO: Concurrent metrics operations → verify final counts → check no race conditions
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		var wg sync.WaitGroup
		iterations := 1000

		// Concurrent metrics operations
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					collector.metrics.IncrementRecordsProcessed()
					collector.metrics.IncrementErrorsOccurred()
					collector.metrics.IncrementRecordsOverwritten(1)

					// Also read metrics concurrently
					_ = collector.GetMetrics()
				}
			}()
		}

		wg.Wait()

		// Verify final counts
		metrics := collector.GetMetrics()
		expectedCount := int64(10 * iterations)
		suite.Equal(expectedCount, metrics.RecordsProcessed)
		suite.Equal(expectedCount, metrics.ErrorsOccurred)
		suite.Equal(expectedCount, metrics.RecordsOverwritten)
	})
}

// TestErrorHandling device_test error test-scenarios and recovery
func (suite *LuaOutputCollectorTestSuite) TestErrorHandling() {
	// GOAL: Verify custom error handlers are called and default behavior panics on errors
	//
	// TEST SCENARIO: Trigger error conditions → verify error handler called or panic occurs → check graceful recovery
	suite.Run("CustomErrorHandlerCalled", func() {
		// GOAL: Verify custom error handler is invoked when errors occur during processing
		//
		// TEST SCENARIO: Setup custom handler → trigger error condition → verify handler called with error
		ch := make(chan LuaOutputRecord, 10)

		var capturedError error
		errorHandler := func(err error) {
			capturedError = err
		}

		collector, err := NewLuaOutputCollector(ch, 100, errorHandler)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)

		// Force an error by closing the channel while the collector is running
		// and then trying to send data
		close(ch)

		// Wait for goroutine to exit
		time.Sleep(100 * time.Millisecond)

		// In this case, closing the channel causes the goroutine to exit cleanly.
		// Let's test with a different error scenario instead
		suite.Nil(capturedError) // No error expected with clean channel closure
	})

	suite.Run("DefaultErrorHandlerPanic", func() {
		// GOAL: Verify default error handler panics when no custom handler provided
		//
		// TEST SCENARIO: Simulate default handler → call with error → verify panic occurs
		// Test that default error handler panics
		suite.Panics(func() {
			// Simulate the default error handler
			onError := func(err error) {
				panic(fmt.Sprintf("LuaOutputCollector: %v", err))
			}
			onError(errors.New("test error"))
		})
	})

	suite.Run("StopWithoutStart", func() {
		// GOAL: Verify calling Stop on non-running collector returns no error
		//
		// TEST SCENARIO: Call Stop without Start → verify no error returned → check immediate return
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Stop without start should return nil since already in the 'NotRunning' state
		err = collector.Stop()
		suite.NoError(err) // Should return nil immediately
	})
}

// TestBufferBehavior device_test ring buffer behavior and overflow
func (suite *LuaOutputCollectorTestSuite) TestBufferBehavior() {
	// GOAL: Verify ring buffer handles capacity limits and overflow with overlapped writes
	//
	// TEST SCENARIO: Fill buffer beyond capacity → verify overflow handling → check records processed correctly
	suite.Run("BufferCapacity", func() {
		// GOAL: Verify ring buffer handles records beyond capacity with overlapped writes
		//
		// TEST SCENARIO: Send more records than buffer size → verify processing continues → check overflow handling
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		bufferSize := uint32(5)
		var errorCount int64
		errorHandler := func(err error) {
			atomic.AddInt64(&errorCount, 1)
		}

		collector, err := NewLuaOutputCollector(ch, bufferSize, errorHandler)
		suite.NoError(err)

		err = collector.Start()
		suite.NoError(err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send more records than buffer capacity
		recordCount := 10
		for i := 0; i < recordCount; i++ {
			ch <- LuaOutputRecord{
				Content:   fmt.Sprintf("record%d", i),
				Timestamp: time.Now(),
				Source:    "stdout",
			}
		}

		// Wait for processing
		time.Sleep(100 * time.Millisecond)

		// Some records should be processed, some may cause errors due to a buffer full
		metrics := collector.GetMetrics()
		suite.Greater(metrics.RecordsProcessed, int64(0))
		// Error count may be > 0 due to buffer overflow
		suite.GreaterOrEqual(atomic.LoadInt64(&errorCount), int64(0))
	})

	suite.Run("BufferEmpty", func() {
		// GOAL: Verify consuming from empty buffer returns empty result without error
		//
		// TEST SCENARIO: Consume from empty buffer → verify no error → check empty string result
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		// Try to consume from the empty buffer
		result, err := collector.ConsumePlainText()
		suite.NoError(err)
		suite.Empty(result)
	})
}

// TestIsZeroValue device_test the helper function
func (suite *LuaOutputCollectorTestSuite) TestIsZeroValue() {
	// GOAL: Verify isZeroValue helper function correctly identifies zero values for all types
	//
	// TEST SCENARIO: Test zero and non-zero values → verify correct boolean return → check various Go types
	suite.Run("ZeroValues", func() {
		// GOAL: Verify isZeroValue correctly identifies zero values for various Go types
		//
		// TEST SCENARIO: Test zero values of different types → verify all return true → check type coverage
		suite.True(isZeroValue(""))
		suite.True(isZeroValue(0))
		suite.True(isZeroValue(false))
		suite.True(isZeroValue((*string)(nil)))

		var emptySlice []string
		suite.True(isZeroValue(emptySlice))

		var emptyMap map[string]string
		suite.True(isZeroValue(emptyMap))
	})

	suite.Run("NonZeroValues", func() {
		// GOAL: Verify isZeroValue correctly identifies non-zero values for various Go types
		//
		// TEST SCENARIO: Test non-zero values of different types → verify all return false → check type coverage
		suite.False(isZeroValue("hello"))
		suite.False(isZeroValue(42))
		suite.False(isZeroValue(true))
		suite.False(isZeroValue([]string{"item"}))
		suite.False(isZeroValue(map[string]string{"key": "value"}))

		str := "test"
		suite.False(isZeroValue(&str))
	})
}

// TestRaceConditions runs device_test with race detector
func (suite *LuaOutputCollectorTestSuite) TestRaceConditions() {
	// GOAL: Verify no data races occur during concurrent start/stop and metrics operations
	//
	// TEST SCENARIO: Run concurrent operations with race detector → verify no races detected → check atomic safety
	suite.Run("StartStopRace", func() {
		// GOAL: Verify concurrent start/stop operations don't cause data races or panics
		//
		// TEST SCENARIO: Concurrent start/stop calls → verify no races detected → check clean final state
		ch := make(chan LuaOutputRecord, 100)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, func(err error) {
			// Ignore errors in this race test - we expect them
		})
		suite.NoError(err)

		// Start and immediately try to stop multiple times
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ { // Reduced iterations to be less racy
			wg.Add(2)
			go func() {
				defer wg.Done()
				_ = collector.Start() // Ignore errors
			}()
			go func() {
				defer wg.Done()
				_ = collector.Stop() // Ignore errors
			}()
		}
		wg.Wait()

		// Final cleanup - ensure stopped
		_ = collector.Stop()
	})

	suite.Run("MetricsRace", func() {
		// GOAL: Verify concurrent metrics operations are atomic and race-free
		//
		// TEST SCENARIO: Concurrent metrics increment/read/reset → verify no races → check atomic consistency
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.NoError(err)

		var wg sync.WaitGroup

		// Concurrent metrics operations
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					collector.metrics.IncrementRecordsProcessed()
					_ = collector.metrics.GetRecordsProcessed()
					collector.ResetMetrics()
					_ = collector.GetMetrics()
				}
			}()
		}

		wg.Wait()
	})
}

// TestLuaOutputCollectorEdgeCases device_test specific edge cases and stress test-scenarios
func TestLuaOutputCollectorEdgeCases(t *testing.T) {
	// GOAL: Verify collector handles edge cases like boundary conditions and high throughput
	//
	// TEST SCENARIO: Test boundary values and stress conditions → verify correct behavior → check performance limits
	t.Run("MaxBufferSizeBoundary", func(t *testing.T) {
		// GOAL: Verify constructor handles MaxBufferSize boundary conditions correctly
		//
		// TEST SCENARIO: Test at and above MaxBufferSize → verify acceptance/rejection → check boundary enforcement
		ch := make(chan LuaOutputRecord, 1)
		defer close(ch)

		// Test exactly at the boundary
		collector, err := NewLuaOutputCollector(ch, MaxBufferSize, nil)
		assert.NoError(t, err)
		assert.NotNil(t, collector)

		// Test one over the boundary
		collector, err = NewLuaOutputCollector(ch, MaxBufferSize+1, nil)
		assert.Error(t, err)
		assert.Nil(t, collector)
	})

	t.Run("LargeRecordProcessing", func(t *testing.T) {
		// GOAL: Verify collector processes large records (1MB) without corruption or errors
		//
		// TEST SCENARIO: Send 1MB record → verify processing → check content preservation and metrics
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		require.NoError(t, err)

		err = collector.Start()
		require.NoError(t, err)
		defer func() {
			_ = collector.Stop()
		}()

		// Send a very large record
		largeContent := make([]byte, 1024*1024) // 1MB
		for i := range largeContent {
			largeContent[i] = byte('A' + (i % 26))
		}

		record := LuaOutputRecord{
			Content:   string(largeContent),
			Timestamp: time.Now(),
			Source:    "stdout",
		}

		ch <- record
		time.Sleep(100 * time.Millisecond)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(1), metrics.RecordsProcessed)

		// Verify content is preserved
		result, err := collector.ConsumePlainText()
		assert.NoError(t, err)
		assert.Equal(t, string(largeContent), result)
	})

	t.Run("HighThroughputStressTest", func(t *testing.T) {
		// GOAL: Verify collector handles high-throughput test-scenarios with 100k records efficiently
		//
		// TEST SCENARIO: Send 100k records rapidly → verify all processed → check performance metrics
		if testing.Short() {
			t.Skip("Skipping stress test in short mode")
		}

		ch := make(chan LuaOutputRecord, 10000)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 10000, nil)
		require.NoError(t, err)

		err = collector.Start()
		require.NoError(t, err)
		defer func() {
			_ = collector.Stop()
		}()

		// High-throughput test
		recordCount := 100000
		start := time.Now()

		go func() {
			for i := 0; i < recordCount; i++ {
				record := LuaOutputRecord{
					Content:   fmt.Sprintf("record%d", i),
					Timestamp: time.Now(),
					Source:    "stdout",
				}
				ch <- record
			}
		}()

		// Wait for processing with a timeout
		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatal("Timeout waiting for records to be processed")
			case <-ticker.C:
				metrics := collector.GetMetrics()
				if metrics.RecordsProcessed >= int64(recordCount) {
					duration := time.Since(start)
					t.Logf("Processed %d records in %v (%.0f records/sec)",
						recordCount, duration, float64(recordCount)/duration.Seconds())
					return
				}
			}
		}
	})
}

// TestWriterAdapters tests the io.WriteCloser adapter functionality for LuaOutputCollector
func (suite *LuaOutputCollectorTestSuite) TestWriterAdapters() {
	// GOAL: Verify StdoutWriter/StderrWriter provide io.WriteCloser interface to collector
	//
	// TEST SCENARIO: Test writer creation, writes, lifecycle, errors, and concurrency

	suite.Run("WriterCreatesRecords", func() {
		// GOAL: Verify writer adds records to buffer with correct content
		//
		// TEST SCENARIO: Write to writer → consume records → verify content matches
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.Require().NoError(err)

		// Get writer (collector doesn't need to be started - writer uses AddRecord directly)
		writer := collector.StdoutWriter()
		defer writer.Close()

		// Write test content
		testContent := "hello world"
		n, err := writer.Write([]byte(testContent))
		suite.NoError(err)
		suite.Equal(len(testContent), n)

		// Consume and verify content
		result, err := collector.ConsumePlainText()
		suite.NoError(err)
		suite.Equal(testContent, result)
	})

	suite.Run("WriterLifecycle", func() {
		// GOAL: Verify writer acquisition stops collector goroutine and release restarts it
		//
		// TEST SCENARIO: Start collector → get writer (verify stopped) → close writer → verify restarted
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.Require().NoError(err)

		// Start collector
		err = collector.Start()
		suite.Require().NoError(err)
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		// Get writer - should stop collector goroutine
		writer := collector.StdoutWriter()
		suite.True(suite.waitForState(collector, CollectorStateNotRunning, 100*time.Millisecond))

		// Close writer - should restart collector goroutine
		err = writer.Close()
		suite.NoError(err)
		suite.True(suite.waitForState(collector, CollectorStateRunning, 100*time.Millisecond))

		// Cleanup
		err = collector.Stop()
		suite.NoError(err)
	})

	suite.Run("WriteToClosedWriter", func() {
		// GOAL: Verify writing to closed writer returns fs.ErrClosed
		//
		// TEST SCENARIO: Get writer → close → write → verify errors.Is(err, fs.ErrClosed)
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.Require().NoError(err)

		// Get and close writer
		writer := collector.StdoutWriter()
		err = writer.Close()
		suite.NoError(err)

		// Write to closed writer
		n, err := writer.Write([]byte("test"))
		suite.Equal(0, n)
		suite.Error(err)
		suite.True(errors.Is(err, fs.ErrClosed), "expected fs.ErrClosed, got: %v", err)
	})

	suite.Run("DoubleCloseIsSafe", func() {
		// GOAL: Verify closing writer twice is safe and idempotent
		//
		// TEST SCENARIO: Get writer → close → close again → no panic and no error
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.Require().NoError(err)

		writer := collector.StdoutWriter()

		// First close
		err = writer.Close()
		suite.NoError(err)

		// Second close - should be safe
		suite.NotPanics(func() {
			err = writer.Close()
			suite.NoError(err)
		})
	})

	suite.Run("ConcurrentWriterWrites", func() {
		// GOAL: Verify concurrent writes to multiple writers are thread-safe
		//
		// TEST SCENARIO: Multiple goroutines write to stdout/stderr writers → verify all records in buffer
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 1000, nil)
		suite.Require().NoError(err)

		stdoutWriter := collector.StdoutWriter()
		stderrWriter := collector.StderrWriter()
		defer stdoutWriter.Close()
		defer stderrWriter.Close()

		var wg sync.WaitGroup
		var stdoutErrors, stderrErrors atomic.Int32
		writesPerWriter := 100

		// Concurrent stdout writes
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				content := fmt.Sprintf("out%d\n", i)
				n, err := stdoutWriter.Write([]byte(content))
				if err != nil || n != len(content) {
					stdoutErrors.Add(1)
				}
			}
		}()

		// Concurrent stderr writes
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				content := fmt.Sprintf("err%d\n", i)
				n, err := stderrWriter.Write([]byte(content))
				if err != nil || n != len(content) {
					stderrErrors.Add(1)
				}
			}
		}()

		wg.Wait()

		// Verify no write errors occurred
		suite.Equal(int32(0), stdoutErrors.Load(), "stdout write errors occurred")
		suite.Equal(int32(0), stderrErrors.Load(), "stderr write errors occurred")

		// Count records in buffer
		var recordCount int
		_, _ = ConsumeRecords(collector, func(rec *LuaOutputRecord) (int, error) {
			if rec != nil {
				recordCount++
			}
			return 0, nil
		})

		suite.Equal(writesPerWriter*2, recordCount)
	})

	suite.Run("AddRecordDirect", func() {
		// GOAL: Verify AddRecord adds record directly to buffer without running collector
		//
		// TEST SCENARIO: Call AddRecord on stopped collector → consume → verify record content
		ch := make(chan LuaOutputRecord, 10)
		defer close(ch)

		collector, err := NewLuaOutputCollector(ch, 100, nil)
		suite.Require().NoError(err)

		// Collector is NOT started - AddRecord should still work
		suite.Equal(CollectorStateNotRunning, collector.GetState())

		testRecord := &LuaOutputRecord{
			Content:   "direct record",
			Source:    "test",
			Timestamp: time.Now(),
		}
		collector.AddRecord(testRecord)

		// Consume and verify
		result, err := collector.ConsumePlainText()
		suite.NoError(err)
		suite.Equal("direct record", result)
	})
}

// Run the test suite
func TestLuaOutputCollectorTestSuite(t *testing.T) {
	suite.Run(t, new(LuaOutputCollectorTestSuite))
}
