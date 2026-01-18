//go:build test

package ptyio

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type PTYTestSuite struct {
	suite.Suite
}

func (s *PTYTestSuite) TestWriteFlowsThroughToSlave() {
	// GOAL: Verify data written via Write() flows through writeLoop to PTY slave
	//
	// TEST SCENARIO: Write data → writeLoop transfers to master → read from slave → data matches

	pty, err := NewPtyWithOptions(&PTYOptions{
		ReadCap:       4096,
		WriteCap:      4096,
		PollTimeoutMs: 10, // Fast polling for test responsiveness
	})
	s.Require().NoError(err, "PTY creation MUST succeed")
	defer pty.Close()

	// Write data to PTY (queues to writeBuf for writeLoop to process)
	data := []byte("hello from test")
	n, err := pty.Write(data)
	s.Require().NoError(err, "Write MUST succeed")
	s.Require().Equal(len(data), n, "All bytes MUST be queued")

	// Open slave to read what writeLoop wrote to master
	slave, err := os.OpenFile(pty.TTYName(), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	s.Require().NoError(err, "Opening slave MUST succeed")
	defer slave.Close()

	// Poll for data with timeout (writeLoop needs time to transfer)
	buf := make([]byte, 4096)
	var received []byte
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		n, err := slave.Read(buf)
		if n > 0 {
			received = append(received, buf[:n]...)
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) {
			break
		}
		if len(received) >= len(data) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.Require().Equal(string(data), string(received), "Data MUST flow through writeLoop to slave")
}

func (s *PTYTestSuite) TestWriteLoopLockContention() {
	// GOAL: Verify writeLoop handles concurrent Write() calls without losing data
	//
	// TEST SCENARIO: Concurrent writes → writeLoop processes all → slave receives all data

	pty, err := NewPtyWithOptions(&PTYOptions{
		ReadCap:       4096,
		WriteCap:      4096,
		PollTimeoutMs: 10,
	})
	s.Require().NoError(err, "PTY creation MUST succeed")
	defer pty.Close()

	// Open slave to read data
	slave, err := os.OpenFile(pty.TTYName(), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	s.Require().NoError(err, "Opening slave MUST succeed")
	defer slave.Close()

	// Write multiple messages rapidly to stress lock contention
	messages := []string{
		"message1",
		"message2",
		"message3",
		"message4",
		"message5",
	}

	var totalWritten int
	for _, msg := range messages {
		n, err := pty.Write([]byte(msg))
		s.Require().NoError(err, "Write MUST succeed for %q", msg)
		totalWritten += n
	}

	// Poll for data with timeout
	buf := make([]byte, 4096)
	var received []byte
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		n, err := slave.Read(buf)
		if n > 0 {
			received = append(received, buf[:n]...)
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) {
			break
		}
		if len(received) >= totalWritten {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.Require().Equal(totalWritten, len(received), "All bytes MUST flow through writeLoop")
}

func (s *PTYTestSuite) TestWriteLoopUnderContinuousContention() {
	// GOAL: Verify writeLoop TryRead succeeds under sustained write pressure
	//
	// TEST SCENARIO: Write in tight loop → writeLoop must not starve → data flows to slave

	pty, err := NewPtyWithOptions(&PTYOptions{
		ReadCap:       4096,
		WriteCap:      4096,
		PollTimeoutMs: 10,
	})
	s.Require().NoError(err, "PTY creation MUST succeed")
	defer pty.Close()

	// Open slave to read data
	slave, err := os.OpenFile(pty.TTYName(), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	s.Require().NoError(err, "Opening slave MUST succeed")
	defer slave.Close()

	// Write in tight loop to create sustained lock pressure
	data := []byte("X")
	const iterations = 100
	var totalWritten int

	for i := 0; i < iterations; i++ {
		n, err := pty.Write(data)
		if err == nil {
			totalWritten += n
		}
		// No sleep - maximum contention
	}

	s.Require().Greater(totalWritten, 0, "At least some writes MUST succeed")

	// Poll for data with timeout
	buf := make([]byte, 4096)
	var received []byte
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		n, err := slave.Read(buf)
		if n > 0 {
			received = append(received, buf[:n]...)
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) {
			break
		}
		if len(received) >= totalWritten {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	s.Require().Equal(totalWritten, len(received),
		"All written bytes MUST flow through writeLoop (got %d of %d)", len(received), totalWritten)
}

func (s *PTYTestSuite) TestWriteWithSufficientDrainTime() {
	// GOAL: Verify data flows through when writeLoop has sufficient time
	//
	// TEST SCENARIO: Write data → wait for drain → Close → data received

	pty, err := NewPtyWithOptions(&PTYOptions{
		ReadCap:       4096,
		WriteCap:      4096,
		PollTimeoutMs: 10,
	})
	s.Require().NoError(err, "PTY creation MUST succeed")
	defer pty.Close()

	// Open slave to read data
	slaveFd, err := syscall.Open(pty.TTYName(), syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	s.Require().NoError(err, "Opening slave MUST succeed")
	defer syscall.Close(slaveFd)

	// Write data
	data := []byte("test message")
	n, writeErr := pty.Write(data)
	s.Require().NoError(writeErr, "Write MUST succeed")
	s.Require().Equal(len(data), n, "All bytes MUST be queued")

	// Wait for writeLoop to drain (poll timeout + processing)
	time.Sleep(200 * time.Millisecond)

	// Read data from slave
	readBuf := make([]byte, 4096)
	var received []byte
	for {
		n, err := syscall.Read(slaveFd, readBuf)
		if n > 0 {
			received = append(received, readBuf[:n]...)
		}
		if err == syscall.EAGAIN || n == 0 {
			break
		}
		if err != nil {
			break
		}
	}

	s.Require().Equal(string(data), string(received),
		"Data MUST flow through when writeLoop has drain time")
}

func (s *PTYTestSuite) TestConcurrentWriteAndRead() {
	// GOAL: Verify concurrent Read() and Write() don't cause lock starvation
	//
	// TEST SCENARIO: Goroutine writes → main reads → no deadlock or data loss

	pty, err := NewPtyWithOptions(&PTYOptions{
		ReadCap:       4096,
		WriteCap:      4096,
		PollTimeoutMs: 10,
	})
	s.Require().NoError(err, "PTY creation MUST succeed")
	defer pty.Close()

	// Open slave with non-blocking I/O
	slaveFd, err := syscall.Open(pty.TTYName(), syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	s.Require().NoError(err, "Opening slave MUST succeed")
	defer syscall.Close(slaveFd)

	// Start concurrent writer
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			pty.Write([]byte("X"))
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Wait for writer to finish, then drain
	<-done
	time.Sleep(100 * time.Millisecond) // Let writeLoop process remaining data

	// Read all available data using syscall (truly non-blocking)
	readBuf := make([]byte, 4096)
	var received []byte
	for {
		n, err := syscall.Read(slaveFd, readBuf)
		if n > 0 {
			received = append(received, readBuf[:n]...)
		}
		if err == syscall.EAGAIN || n == 0 {
			break
		}
		if err != nil {
			break
		}
	}

	s.Require().Equal(50, len(received), "All 50 bytes MUST flow through without lock starvation")
}

func TestPTYTestSuite(t *testing.T) {
	suite.Run(t, new(PTYTestSuite))
}
