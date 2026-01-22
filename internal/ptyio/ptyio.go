// Package ptyio provides a Ring-based async PTY master wrapper optimized
// for high-throughput use. It creates a pair with github.com/crack/pty.
//
// # Basic Usage
//
//	// Create PTY pair (returns PTY interface):
//	pty, err := ptyio.NewPty(readCap, writeCap, logger)
//	if err != nil {
//	    return err
//	}
//	// pty.TTYName() -> "/dev/pts/X"
//
//	// Send to slave (non-blocking, may drop data if buffer full):
//	data := []byte("hello\n")
//	n, err := pty.Write(data)
//	if err != nil {
//	    return err
//	}
//	if n < len(data) {
//	    // Buffer overflow - some data was dropped
//	    log.Printf("Warning: only wrote %d of %d bytes", n, len(data))
//	}
//
//	// Read output produced by slave (non-blocking):
//	buf := make([]byte, 4096)
//	n, err := pty.Read(buf)
//
// # Poll Timeout Tuning
//
// The poll timeout controls the maximum time goroutines wait for I/O readiness
// before checking context cancellation. This affects both responsiveness and CPU usage.
//
// Use cases and recommended settings:
//
//	Interactive terminals (low latency required):
//	  PollTimeoutMs: 10-25ms
//	  - Provides sub-frame response time for human interaction
//	  - Shutdown latency: ~10-25ms
//	  - CPU impact: Moderate (goroutines wake up 40-100x per second when idle)
//
//	General purpose (balanced):
//	  PollTimeoutMs: 50ms (default)
//	  - Good balance for most applications
//	  - Shutdown latency: ~50ms
//	  - CPU impact: Low (goroutines wake up 20x per second when idle)
//
//	Batch processing / high-throughput logging:
//	  PollTimeoutMs: 100-200ms
//	  - Minimizes CPU overhead for background tasks
//	  - Shutdown latency: ~100-200ms
//	  - CPU impact: Minimal (goroutines wake up 5-10x per second when idle)
//
//	Real-time data streaming:
//	  PollTimeoutMs: 5-10ms
//	  - Ultra-low latency for time-sensitive data
//	  - Shutdown latency: ~5-10ms
//	  - CPU impact: Higher (goroutines wake up 100-200x per second when idle)
//
// Note: Actual CPU usage depends on I/O activity. When data is flowing continuously,
// poll timeout has minimal impact since goroutines rarely wait for the full timeout.
// The timeout primarily affects idle periods and shutdown responsiveness.
package ptyio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/sirupsen/logrus"
	"github.com/smallnest/ringbuffer"
	"github.com/srg/blim/internal/groutine"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// ErrorCallback is invoked when a critical error occurs in read/write loops.
// This callback is called from background goroutines, so implementations must be thread-safe.
// The PTY remains in a degraded state after the error - Close() should be called.
type ErrorCallback func(err error)

// ReadCallback is invoked when data arrives from the PTY slave (background goroutine).
// Implementations must be thread-safe and must not retain the data slice (copy if needed).
// Panics are recovered and logged, but will unregister the callback to prevent repeated failures.
type ReadCallback func(data []byte)

// PTYOptions configures PTY creation with fine-grained control over behavior.
// Zero values use sensible defaults (see Default* constants).
type PTYOptions struct {
	ReadCap       int            // Ring buffer capacity for data read from PTY (0 = DefaultReadCap)
	WriteCap      int            // Ring buffer capacity for data written to PTY (0 = DefaultWriteCap)
	Logger        *logrus.Logger // Optional logger (nil = no-op logger)
	OnError       ErrorCallback  // Optional callback for critical loop failures
	PollTimeoutMs int            // Poll timeout in milliseconds (0 = DefaultPollTimeoutMs)
}

// PTY provides a non-blocking interface to pseudo-terminal devices.
// Implements io.ReadWriteCloser for compatibility with standard Go interfaces.
type PTY interface {
	io.ReadWriteCloser               // Standard Go read/write/close interface
	Stats() Stats                    // runtime metrics
	TTYName() string                 // path of a tty device, empty if unknown
	SetReadCallback(cb ReadCallback) // set callback for async data arrival (nil to unregister)
}

// Stats provides runtime counters useful for monitoring/backpressure.
type Stats struct {
	WriteQueueLen int32 // approximate
	WriteQueueCap int32
	ReadQueueLen  int32
	ReadQueueCap  int32

	DroppedWriteCount uint64 // how many bytes were dropped due to write buffer overflow
	DroppedReadCount  uint64 // how many bytes were dropped due to read buffer overflow
	ReadBytesTotal    uint64
	WriteBytesTotal   uint64
}

const (
	// DefaultPollTimeoutMs is the default poll timeout in milliseconds for I/O operations.
	// This affects shutdown latency (max delay before goroutines detect context cancellation)
	// and CPU usage (shorter = more responsive but higher CPU usage when idle).
	// Exported so users can reference it when creating custom PTYOptions.
	DefaultPollTimeoutMs = 50

	// DefaultReadCap is the default ring buffer capacity for data read from PTY (bytes).
	DefaultReadCap = 4096

	// DefaultWriteCap is the default ring buffer capacity for data written to PTY (bytes).
	DefaultWriteCap = 4096
)

// noopLogger is a shared logger instance that discards all output.
// Used when no logger is provided to avoid allocating a new logger for each PTY.
var noopLogger = func() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}()

// ringPTY implements PTY using ring buffers for non-blocking I/O.
// It wraps a PTY master/slave pair with background goroutines for async read/write,
// providing backpressure management via ring buffer semantics (the oldest data dropped when full).
//
// Embeds BaseBufferedAsyncReader for read loop, dispatcher, and read buffer management.
// PTY-specific functionality (write loop, slave management) is handled locally.
type ringPTY struct {
	*BaseBufferedAsyncReader // embedded - provides read loop, dispatcher, read buffer, callbacks

	tty     *os.File // slave (master is in BaseBufferedAsyncReader.file)
	ttyName string

	// Write-specific (PTY is bidirectional, unlike stdin reader)
	writeBuf       *ringbuffer.RingBuffer
	writeWg        sync.WaitGroup // separate wait group for write loop
	writeErrorOnce sync.Once      // ensures write error callback is called at most once
	droppedWrite   uint64         // bytes dropped due to write buffer overflow
	writeBytes     uint64         // total bytes written to PTY
}

// NewPty creates a new PTY ptyx(master)/tty(slave) pair, wraps the master in ringPTY,
// and returns the PTY interface. The caller may hand off the slaveFile to another
// process. If the logger is nil, a no-op logger will be used.
func NewPty(readCap, writeCap int, logger *logrus.Logger) (PTY, error) {
	return NewPtyWithErrorHandler(readCap, writeCap, logger, nil)
}

// NewPtyWithErrorHandler creates a new PTY with an optional error callback for monitoring
// critical failures in background read/write loops. If onError is nil, errors are only logged.
//
// The callback is invoked at most once per loop (read/write) when a critical error occurs
// that causes the loop to exit. After callback invocation, the PTY remains in a degraded state
// and Close() should be called.
//
// Example usage with error handling:
//
//	pty, err := ptyio.NewPtyWithErrorHandler(1024, 1024, logger, func(err error) {
//	    log.Printf("PTY critical error: %v", err)
//	    // Trigger reconnection, cleanup, or user notification
//	})
func NewPtyWithErrorHandler(readCap, writeCap int, logger *logrus.Logger, onError ErrorCallback) (PTY, error) {
	return NewPtyWithOptions(&PTYOptions{
		ReadCap:  readCap,
		WriteCap: writeCap,
		Logger:   logger,
		OnError:  onError,
	})
}

// NewPtyWithOptions creates a new PTY with full configuration control.
// This is the most flexible constructor, allowing fine-grained tuning of all parameters.
//
// Example usage with custom poll timeout:
//
//	pty, err := ptyio.NewPtyWithOptions(&ptyio.PTYOptions{
//	    ReadCap:       4096,
//	    WriteCap:      4096,
//	    Logger:        logger,
//	    PollTimeoutMs: 10, // Lower latency for interactive applications
//	    OnError: func(err error) {
//	        log.Printf("PTY error: %v", err)
//	    },
//	})
func NewPtyWithOptions(opts *PTYOptions) (PTY, error) {
	if opts == nil {
		return nil, fmt.Errorf("PTYOptions cannot be nil")
	}

	master, slave, err := createPTY()
	if err != nil {
		return nil, err
	}

	// Get slave device path (e.g., "/dev/pts/5") for external processes to open
	slaveName := slave.Name()

	// Design decision: Keep slave FD open for PTY lifetime
	//
	// PTY behavior on modern Unix (Linux/macOS):
	//   - Slave device node (/dev/pts/X) exists as long as master FD is open
	//   - External processes can open slave by path even after original slave FD is closed
	//   - Keeping slave FD open is NOT strictly necessary for device availability
	//
	// Rationale for keeping it open:
	//   - Defensive: Ensures at least one open slave FD exists (prevents edge cases)
	//   - Simplicity: Symmetric lifecycle management (both master+slave closed together)
	//   - Compatibility: Avoids potential issues on exotic Unix variants
	//
	// Trade-off: Consumes one extra file descriptor for PTY lifetime
	// Alternative: Could close slave here and only keep master (would save 1 FD per PTY)

	// Apply defaults for write capacity (read capacity handled by base)
	writeCap := opts.WriteCap
	if writeCap == 0 {
		writeCap = DefaultWriteCap
	}

	// Create base reader with master file - handles read loop, dispatcher, read buffer
	baseOpts := &BaseReaderOptions{
		ReadCap:             opts.ReadCap,
		Logger:              opts.Logger,
		OnError:             opts.OnError,
		PollTimeoutMs:       opts.PollTimeoutMs,
		CloseFileOnShutdown: true, // PTY owns the master file
	}
	base := newBaseBufferedAsyncReader(master, baseOpts)

	p := &ringPTY{
		BaseBufferedAsyncReader: base,
		tty:                     slave,
		ttyName:                 slaveName,
		writeBuf:                ringbuffer.New(writeCap),
	}

	// Start base goroutines (read loop + dispatcher)
	base.startGoroutines("pty")

	// Start write loop with separate wait group
	p.writeWg.Add(1)
	groutine.Go(base.Context(), "pty-write-loop", func(ctx context.Context) {
		p.ttyWriteLoop()
	})

	return p, nil
}

func (p *ringPTY) ttyWriteLoop() {
	// Defensive panic recovery ensures writeWg.Done() always executes
	defer func() {
		if r := recover(); r != nil {
			p.Logger().Errorf("writeLoop panicked (recovered): %v", r)
		}
		p.writeWg.Done()
	}()

	// Capture *os.File reference to prevent nil pointer dereference after Close()
	master := p.File()
	pollFd := []unix.PollFd{{Fd: int32(p.FileFd()), Events: unix.POLLOUT}}
	buf := make([]byte, 4096) // Write buffer for batching bytes from a ring

	for {
		select {
		case <-p.Context().Done():
			return
		default:
		}

		// Check if there's data to write
		if p.writeBuf.IsEmpty() {
			// No data, check context with timeout
			nReady, err := unix.Poll(pollFd, p.PollTimeoutMs())
			if err != nil && !errors.Is(err, syscall.EINTR) {
				p.Logger().Warnf("writeLoop poll error: %v", err)
			}
			if nReady == 0 {
				continue // timeout, check context
			}
		}

		// Read bytes from the ring buffer (blocking read)
		// Using Read() instead of TryRead() because IsEmpty() already confirmed data exists.
		// This preserves the invariant: "Observed data will be consumed."
		n, err := p.writeBuf.Read(buf)
		if err != nil && !errors.Is(err, ringbuffer.ErrIsEmpty) {
			p.Logger().Warnf("writeLoop Read error: %v", err)
			continue
		}
		if n == 0 {
			continue // buffer empty
		}

		// Write collected bytes to PTY (use captured master reference)
		offset := 0
		for offset < n {
			written, err := master.Write(buf[offset:n])
			if written > 0 {
				offset += written
				atomic.AddUint64(&p.writeBytes, uint64(written))
				p.Logger().Debugf("[writeLoop] Wrote %d bytes to PTY master", written)
			}

			if err != nil {
				switch {
				case errors.Is(err, syscall.EINTR):
					continue
				case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
					// Wait until writable again
					if _, pollErr := unix.Poll(pollFd, p.PollTimeoutMs()); pollErr != nil && !errors.Is(pollErr, syscall.EINTR) {
						p.Logger().Warnf("writeLoop poll error: %v", pollErr)
					}
					continue
				case errors.Is(err, syscall.EBADF):
					// FD closed — terminate loop (expected during Close())
					p.Logger().Debug("writeLoop exiting: master FD closed")
					return
				default:
					// Critical error — notify caller and exit
					p.Logger().Warnf("writeLoop exiting on error: %v", err)
					if p.OnError() != nil {
						p.writeErrorOnce.Do(func() {
							p.OnError()(fmt.Errorf("writeLoop critical error: %w", err))
						})
					}
					return
				}
			}
		}
	}
}

// Write queues data for async sending to the PTY slave.
// This is a NON-BLOCKING write that always returns immediately.
//
// Behavior:
//   - Data bytes are enqueued to the ring buffer for background transmission
//   - If buffer is full, oldest bytes are dropped (ring buffer semantics) and only
//     partial data is queued
//   - Caller should check the returned byte count to detect buffer overflow
//
// Return values:
//   - (n, nil) where n = bytes queued: Successfully queued n bytes (may be < len(data))
//   - (0, os.ErrClosed): PTY has been closed
//
// IMPORTANT: This follows io.Writer contract - if n < len(data), caller must check
// the return value to detect data loss. Monitor DroppedWriteCount in Stats() for
// aggregate dropped byte count.
//
// Note: Returning n bytes does NOT guarantee data was transmitted to PTY,
// only that it was queued. Use Stats() to monitor actual transmission progress.
func (p *ringPTY) Write(data []byte) (int, error) {
	if p.IsClosed() {
		return 0, os.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}

	// Write bytes to ring buffer (bulk operation)
	written, err := p.writeBuf.Write(data)
	if err != nil && !errors.Is(err, ringbuffer.ErrIsFull) {
		p.Logger().Warnf("Write error: %v", err)
		return 0, err
	}

	// Track and warn about dropped bytes
	// Note: smallnest/ringbuffer.Write() returns how many bytes were actually written
	if written < len(data) {
		dropped := len(data) - written
		atomic.AddUint64(&p.droppedWrite, uint64(dropped))
		p.Logger().Warnf("Write buffer overflow: dropped %d bytes (tried to write %d, only queued %d)",
			dropped, len(data), written)
	}

	// Return actual bytes written (follows io.Writer contract)
	// Note: writeBytes is counted in writeLoop() when actually transmitted to PTY
	return written, nil
}

// Read implements io.Reader by reading up to len(b) bytes from the buffered input.
// This is a NON-BLOCKING read that returns immediately.
//
// Return values:
//   - (n, nil) where n > 0: Successfully read n bytes
//   - (0, syscall.EAGAIN): No data currently available (standard non-blocking I/O semantics)
//   - (0, os.ErrClosed): PTY has been closed
//   - (0, nil): Only when len(b) == 0 (standard io.Reader contract)
//
// This implements io.Reader with non-blocking semantics. Callers should check for
// syscall.EAGAIN using errors.Is() and retry with poll/select/timer.
func (p *ringPTY) Read(b []byte) (n int, err error) {
	if p.IsClosed() {
		return 0, os.ErrClosed
	}
	if len(b) == 0 {
		return 0, nil
	}

	// Read bytes from the ring buffer (bulk operation, from base)
	n, err = p.ReadBuffer().TryRead(b)
	if err != nil && !errors.Is(err, ringbuffer.ErrIsEmpty) {
		p.Logger().Warnf("Read TryRead error: %v", err)
		return 0, err
	}

	// If no data available, return EAGAIN (standard non-blocking I/O semantics)
	// This complies with io.Reader contract: (0, nil) only when len(b) == 0
	if n == 0 {
		return 0, syscall.EAGAIN
	}

	return n, nil
}

// Close shuts down goroutines and closes both master and slave FDs.
func (p *ringPTY) Close() error {
	// 1. Close slave FD to unblock any operations
	//    Note: Safe to call even if already closing (os.File.Close is idempotent)
	if p.tty != nil {
		if err := p.tty.Close(); err != nil {
			p.Logger().Warnf("failed to close PTY(tty): %v", err)
		}
	}

	// 2. closeBase handles: mark closed, cancel context, signal channel, close master, wait for base goroutines
	//    Returns false if already closed (idempotent)
	//    timeoutMultiplier=0 since we handle write loop separately
	if !p.closeBase(nil, 0) {
		return nil // already closed
	}

	// 3. Wait for write loop with timeout
	done := make(chan struct{})
	go func() {
		p.writeWg.Wait()
		close(done)
	}()

	timeout := time.Duration(p.PollTimeoutMs())*time.Millisecond + time.Second
	if timeout < 2*time.Second {
		timeout = 2 * time.Second
	}

	select {
	case <-done:
		// Write loop exited cleanly
	case <-time.After(timeout):
		p.Logger().Errorf("Close() timed out waiting for write loop. PTY=%s", p.ttyName)
	}

	// 4. Nil out slave reference (master is handled by base)
	p.tty = nil

	return nil
}

// Stats return instantaneous stats for monitoring.
func (p *ringPTY) Stats() Stats {
	// Get read stats from base
	baseStats := p.ReadStats()

	return Stats{
		WriteQueueLen:     int32(p.writeBuf.Length()),
		WriteQueueCap:     int32(p.writeBuf.Capacity()),
		ReadQueueLen:      baseStats.QueueLen,
		ReadQueueCap:      baseStats.QueueCap,
		DroppedWriteCount: atomic.LoadUint64(&p.droppedWrite),
		DroppedReadCount:  baseStats.DroppedReadCount,
		ReadBytesTotal:    baseStats.ReadBytesTotal,
		WriteBytesTotal:   atomic.LoadUint64(&p.writeBytes),
	}
}

// TTYName returns the filesystem path to the slave (e.g., "/dev/pts/5")
// if known (only when created via NewPty).
func (p *ringPTY) TTYName() string {
	return p.ttyName
}

// createPTY creates a pseudo-terminal and configures it for raw mode.
func createPTY() (master *os.File, slave *os.File, err error) {
	master, slave, err = pty.Open()
	if err != nil {
		// Enhance an error message for common permission/resource issues
		return nil, nil, fmt.Errorf("failed to create PTY (check permissions and available PTY devices): %w", err)
	}

	// Set PTY slave to raw mode for proper terminal behavior
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		ptyPath := slave.Name()

		// Cleanup FDs - collect any errors to include in the returned error
		var cleanupErrs []error
		if closeErr := master.Close(); closeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close PTY(ptyx): %w", closeErr))
		}
		if closeErr := slave.Close(); closeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close PTY(tty): %w", closeErr))
		}

		// Build error message including cleanup failures
		if len(cleanupErrs) > 0 {
			return nil, nil, fmt.Errorf("failed to set PTY(tty) %s to raw mode: %w (cleanup errors: %v)", ptyPath, err, cleanupErrs)
		}
		return nil, nil, fmt.Errorf("failed to set PTY(tty) %s to raw mode: %w", ptyPath, err)
	}

	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		ptyPath := slave.Name()

		// Cleanup FDs - collect any errors to include in the returned error
		var cleanupErrs []error
		if closeErr := master.Close(); closeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close PTY(ptyx): %w", closeErr))
		}
		if closeErr := slave.Close(); closeErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("close PTY(tty): %w", closeErr))
		}

		// Build error message including cleanup failures
		if len(cleanupErrs) > 0 {
			return nil, nil, fmt.Errorf("failed to set PTY(ptyx) %s to nonblocking mode: %w (cleanup errors: %v)", ptyPath, err, cleanupErrs)
		}
		return nil, nil, fmt.Errorf("failed to set PTY(ptyx) %s to nonblocking mode: %w", ptyPath, err)
	}

	return master, slave, nil
}

