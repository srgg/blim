//go:build test

package ptyio

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type PrefetchFileReaderTestSuite struct {
	suite.Suite
}

func (s *PrefetchFileReaderTestSuite) TestReadBytesBasic() {
	// GOAL: Verify basic ReadBytes functionality
	//
	// TEST SCENARIO: Write data to pipe → ReadBytes returns exactly N bytes

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	// Write data to pipe (with delay to ensure reader is waiting)
	data := []byte("hello world")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(data)
	}()

	// Read exact bytes
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := reader.ReadBytes(ctx, 5)
	s.Require().NoError(err)
	s.Require().Equal("hello", string(result))
}

func (s *PrefetchFileReaderTestSuite) TestReadLineBasic() {
	// GOAL: Verify basic ReadLine functionality
	//
	// TEST SCENARIO: Write line with newline → ReadLine returns line with newline stripped

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	// Write line to pipe
	go func() {
		_, _ = w.Write([]byte("hello world\n"))
	}()

	// Read line
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	line, err := reader.ReadLine(ctx)
	s.Require().NoError(err)
	s.Require().Equal("hello world\n", line) // Note: ReadLine includes newline
}

func (s *PrefetchFileReaderTestSuite) TestConcurrentReadBytesSerialization() {
	// GOAL: Verify concurrent ReadBytes calls are serialized (FIFO)
	//
	// TEST SCENARIO: Multiple goroutines call ReadBytes concurrently →
	//                Data is distributed correctly without interleaving

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	// Write enough data for all readers
	// Each reader will request 4 bytes, we have 3 readers
	go func() {
		// Small delay to ensure readers are waiting
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("AAAABBBBCCCC"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	results := make([]string, 3)
	errors := make([]error, 3)

	// Launch 3 concurrent readers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			data, err := reader.ReadBytes(ctx, 4)
			results[idx] = string(data)
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// All reads should succeed
	for i, err := range errors {
		s.Require().NoError(err, "Reader %d should succeed", i)
	}

	// Collect all results and verify no interleaving
	allData := results[0] + results[1] + results[2]
	s.Require().Equal(12, len(allData), "All 12 bytes should be read")

	// Each result should be a complete 4-byte chunk (no interleaving)
	for i, result := range results {
		s.Require().Equal(4, len(result), "Reader %d should get exactly 4 bytes", i)
		// Each chunk should be homogeneous (AAAA, BBBB, or CCCC)
		firstChar := result[0]
		for _, c := range result {
			s.Require().Equal(firstChar, byte(c),
				"Reader %d got interleaved data: %q (expected all %c)", i, result, firstChar)
		}
	}
}

func (s *PrefetchFileReaderTestSuite) TestConcurrentReadLineSerialization() {
	// GOAL: Verify concurrent ReadLine calls are serialized (FIFO)
	//
	// TEST SCENARIO: Multiple goroutines call ReadLine concurrently →
	//                Each gets a complete line without interleaving

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	// Write multiple lines
	lines := []string{"line1\n", "line2\n", "line3\n"}
	go func() {
		time.Sleep(50 * time.Millisecond)
		for _, line := range lines {
			_, _ = w.Write([]byte(line))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	results := make([]string, 3)
	errors := make([]error, 3)

	// Launch 3 concurrent readers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			line, err := reader.ReadLine(ctx)
			results[idx] = line
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// All reads should succeed
	for i, err := range errors {
		s.Require().NoError(err, "Reader %d should succeed", i)
	}

	// Verify each result is a complete line (ends with newline)
	for i, result := range results {
		s.Require().True(len(result) > 0, "Reader %d should get data", i)
		s.Require().Equal(byte('\n'), result[len(result)-1],
			"Reader %d line should end with newline: %q", i, result)
	}

	// Verify all lines were read (order may vary due to scheduling)
	resultSet := make(map[string]bool)
	for _, result := range results {
		resultSet[result] = true
	}
	for _, line := range lines {
		s.Require().True(resultSet[line], "Line %q should be read by some reader", line)
	}
}

func (s *PrefetchFileReaderTestSuite) TestContextCancellation() {
	// GOAL: Verify ReadBytes respects context cancellation
	//
	// TEST SCENARIO: Start ReadBytes → Cancel context → Returns context error

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Start read in goroutine
	done := make(chan error, 1)
	go func() {
		_, err := reader.ReadBytes(ctx, 100)
		done <- err
	}()

	// Cancel after short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Verify read returns with context error
	select {
	case err := <-done:
		s.Require().ErrorIs(err, context.Canceled)
	case <-time.After(2 * time.Second):
		s.Fail("ReadBytes should return after context cancellation")
	}
}

func (s *PrefetchFileReaderTestSuite) TestReaderClose() {
	// GOAL: Verify Close unblocks waiting readers
	//
	// TEST SCENARIO: Start ReadBytes → Close reader → Returns ErrReaderClosed

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)

	ctx := context.Background()

	// Start read in goroutine
	done := make(chan error, 1)
	go func() {
		_, err := reader.ReadBytes(ctx, 100)
		done <- err
	}()

	// Close after short delay
	time.Sleep(50 * time.Millisecond)
	reader.Close()

	// Verify read returns with closed error
	select {
	case err := <-done:
		s.Require().ErrorIs(err, ErrReaderClosed)
	case <-time.After(2 * time.Second):
		s.Fail("ReadBytes should return after reader closed")
	}
}

func (s *PrefetchFileReaderTestSuite) TestReadZeroBytes() {
	// GOAL: Verify ReadBytes with n=0 returns immediately
	//
	// TEST SCENARIO: ReadBytes(ctx, 0) → Returns nil, nil immediately

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	ctx := context.Background()
	result, err := reader.ReadBytes(ctx, 0)
	s.Require().NoError(err)
	s.Require().Nil(result)
}

func (s *PrefetchFileReaderTestSuite) TestReadNegativeBytes() {
	// GOAL: Verify ReadBytes with n<0 returns immediately
	//
	// TEST SCENARIO: ReadBytes(ctx, -1) → Returns nil, nil immediately

	r, w, err := os.Pipe()
	s.Require().NoError(err)
	defer r.Close()
	defer w.Close()

	reader, err := NewPrefetchFileReader(r, nil)
	s.Require().NoError(err)
	defer reader.Close()

	ctx := context.Background()
	result, err := reader.ReadBytes(ctx, -1)
	s.Require().NoError(err)
	s.Require().Nil(result)
}

func TestPrefetchFileReaderTestSuite(t *testing.T) {
	suite.Run(t, new(PrefetchFileReaderTestSuite))
}
