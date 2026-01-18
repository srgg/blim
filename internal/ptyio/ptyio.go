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
	"runtime"
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
type ringPTY struct {
	logger         *logrus.Logger
	tty            *os.File      // slave
	pty            *os.File      // master
	ptyFd          int           // cached at construction; avoids race between Fd() and Close()
	onError        ErrorCallback // optional callback for critical errors
	writeErrorOnce sync.Once     // ensures the write error callback is called at most once
	readErrorOnce  sync.Once     // ensures read error callback is called at most once
	pollTimeoutMs  int           // poll timeout in milliseconds

	writeBuf *ringbuffer.RingBuffer // bytes to write to PTY
	readBuf  *ringbuffer.RingBuffer // bytes read from PTY

	// internals
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// readCb stores ReadCallback (or nil to unregister)
	// INVARIANT: Must ONLY contain ReadCallback type or nil, never any other type
	// Violation will cause type assertion panic in dispatcher (recovered but logs error)
	readCb     atomic.Value
	readNotify chan struct{} // signals dispatcher that data is available

	closed uint32 // atomic boolean

	// metrics
	droppedWrite uint64
	droppedRead  uint64
	readBytes    uint64
	writeBytes   uint64

	ttyName string

	// chunkPool reduces GC pressure in high-throughput callback scenarios
	// Slices are allocated once, reused across callbacks, returned to pool after use
	chunkPool sync.Pool
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

	ctx, cancel := context.WithCancel(context.Background())

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

	// Apply defaults
	logger := opts.Logger
	if logger == nil {
		logger = noopLogger
	}

	pollTimeout := opts.PollTimeoutMs
	if pollTimeout == 0 {
		pollTimeout = DefaultPollTimeoutMs
	}

	readCap := opts.ReadCap
	if readCap == 0 {
		readCap = DefaultReadCap
	}

	writeCap := opts.WriteCap
	if writeCap == 0 {
		writeCap = DefaultWriteCap
	}

	p := &ringPTY{
		logger:        logger,
		pty:           master,
		ptyFd:         int(master.Fd()),
		tty:           slave, // keep slave open for PTY state
		ttyName:       slaveName,
		writeBuf:      ringbuffer.New(writeCap),
		readBuf:       ringbuffer.New(readCap),
		ctx:           ctx,
		cancel:        cancel,
		onError:       opts.OnError,
		pollTimeoutMs: pollTimeout,
		readNotify:    make(chan struct{}, 1), // buffered so the signal never blocks
	}

	// start goroutines
	p.wg.Add(3)

	groutine.Go(ctx, "tty-read-loop", func(ctx context.Context) {
		p.ttyReadLoop()
	})

	groutine.Go(ctx, "tty-write-loop", func(ctx context.Context) {
		p.ttyWriteLoop()
	})

	groutine.Go(ctx, "tty-stdio-async-dispatcher", func(ctx context.Context) {
		p.ttyStdioAsyncDispatcher()
	})

	return p, nil
}

func (p *ringPTY) ttyWriteLoop() {
	// Defensive panic recovery ensures wg.Done() always executes
	defer func() {
		if r := recover(); r != nil {
			p.logger.Errorf("writeLoop panicked (recovered): %v", r)
		}
		p.wg.Done()
	}()

	// Capture *os.File reference to prevent nil pointer dereference after Close()
	master := p.pty
	pollFd := []unix.PollFd{{Fd: int32(p.ptyFd), Events: unix.POLLOUT}}
	buf := make([]byte, 4096) // Write buffer for batching bytes from a ring

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		// Check if there's data to write
		if p.writeBuf.IsEmpty() {
			// No data, check context with timeout
			nReady, err := unix.Poll(pollFd, p.pollTimeoutMs)
			if err != nil && !errors.Is(err, syscall.EINTR) {
				p.logger.Warnf("writeLoop poll error: %v", err)
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
			p.logger.Warnf("writeLoop Read error: %v", err)
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
				p.logger.Debugf("[writeLoop] Wrote %d bytes to PTY master", written)
			}

			if err != nil {
				switch {
				case errors.Is(err, syscall.EINTR):
					continue
				case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
					// Wait until writable again
					if _, pollErr := unix.Poll(pollFd, p.pollTimeoutMs); pollErr != nil && !errors.Is(pollErr, syscall.EINTR) {
						p.logger.Warnf("writeLoop poll error: %v", pollErr)
					}
					continue
				case errors.Is(err, syscall.EBADF):
					// FD closed — terminate loop (expected during Close())
					p.logger.Debug("writeLoop exiting: master FD closed")
					return
				default:
					// Critical error — notify caller and exit
					p.logger.Warnf("writeLoop exiting on error: %v", err)
					if p.onError != nil {
						p.writeErrorOnce.Do(func() {
							p.onError(fmt.Errorf("writeLoop critical error: %w", err))
						})
					}
					return
				}
			}
		}
	}
}

func (p *ringPTY) ttyReadLoop() {
	// Defensive panic recovery ensures wg.Done() always executes
	defer func() {
		if r := recover(); r != nil {
			p.logger.Errorf("readLoop panicked (recovered): %v", r)
		}
		p.wg.Done()
	}()

	p.logger.Infof("[TTY Read Loop] STARTING for slave %s", p.ttyName)

	// Capture *os.File reference to prevent nil pointer dereference after Close()
	master := p.pty
	pollFd := []unix.PollFd{{Fd: int32(p.ptyFd), Events: unix.POLLIN}}
	buf := make([]byte, 4096) // Read buffer for PTY reads

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		// Wait for readable data or timeout
		nReady, err := unix.Poll(pollFd, p.pollTimeoutMs)
		if err != nil && !errors.Is(err, syscall.EINTR) {
			p.logger.Warnf("readLoop poll error: %v", err)
			continue
		}
		if nReady == 0 {
			continue // timeout, check context
		}

		n, err := master.Read(buf)

		if n > 0 {
			// Write bytes to ring buffer (bulk operation)
			written, writeErr := p.readBuf.Write(buf[:n])
			if writeErr != nil && !errors.Is(writeErr, ringbuffer.ErrIsFull) {
				p.logger.Warnf("readLoop Write error: %v", writeErr)
				continue
			}

			// Track and warn about dropped bytes from PTY
			// Note: smallnest/ringbuffer.Write() returns how many bytes were actually written
			if written < n {
				dropped := n - written
				atomic.AddUint64(&p.droppedRead, uint64(dropped))
				p.logger.Warnf("Read buffer overflow: dropped %d bytes from PTY (received %d, only buffered %d)",
					dropped, n, written)
			}

			atomic.AddUint64(&p.readBytes, uint64(written))

			// Notify the async dispatcher that data is available for read
			if written > 0 && p.readCb.Load() != nil {
				select {
				case p.readNotify <- struct{}{}:
				default:
					// signal already pending, don't block
				}
			}
		}

		if err != nil {
			switch {
			case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
				continue
			case errors.Is(err, syscall.EINTR):
				continue
			case errors.Is(err, syscall.EBADF):
				// FD closed — exit immediately (expected during Close())
				p.logger.Debug("readLoop exiting: master FD closed")
				return
			case errors.Is(err, io.EOF):
				// EOF means slave side closed (expected if external process exits)
				p.logger.Debug("readLoop exiting: EOF")
				return
			default:
				// Critical error — notify caller and exit
				p.logger.Warnf("readLoop exiting on error: %v", err)
				if p.onError != nil {
					p.readErrorOnce.Do(func() {
						p.onError(fmt.Errorf("readLoop critical error: %w", err))
					})
				}
				return
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
	if atomic.LoadUint32(&p.closed) == 1 {
		return 0, os.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}

	// Write bytes to ring buffer (bulk operation)
	written, err := p.writeBuf.Write(data)
	if err != nil && !errors.Is(err, ringbuffer.ErrIsFull) {
		p.logger.Warnf("Write error: %v", err)
		return 0, err
	}

	// Track and warn about dropped bytes
	// Note: smallnest/ringbuffer.Write() returns how many bytes were actually written
	if written < len(data) {
		dropped := len(data) - written
		atomic.AddUint64(&p.droppedWrite, uint64(dropped))
		p.logger.Warnf("Write buffer overflow: dropped %d bytes (tried to write %d, only queued %d)",
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
	if atomic.LoadUint32(&p.closed) == 1 {
		return 0, os.ErrClosed
	}
	if len(b) == 0 {
		return 0, nil
	}

	// Read bytes from the ring buffer (bulk operation)
	n, err = p.readBuf.TryRead(b)
	if err != nil && !errors.Is(err, ringbuffer.ErrIsEmpty) {
		p.logger.Warnf("Read TryRead error: %v", err)
		return 0, err
	}

	// If no data available, return EAGAIN (standard non-blocking I/O semantics)
	// This complies with io.Reader contract: (0, nil) only when len(b) == 0
	if n == 0 {
		return 0, syscall.EAGAIN
	}

	return n, nil
}

// Close shuts down goroutines and closes the master FD.
func (p *ringPTY) Close() error {
	if !atomic.CompareAndSwapUint32(&p.closed, 0, 1) {
		return nil
	}

	// 1. Cancel context to signal goroutines to exit
	p.cancel()

	// 2. Close FDs to unblock any I/O operations immediately with EBADF
	//    Note: os.File.Close() always closes the FD even if it returns an error.
	//    We don't use syscall.Close() as a fallback to avoid double-close bugs
	//    where the OS reuses the FD number for a different file.
	//
	//    CRITICAL: Do NOT set p.pty = nil here. Goroutines have captured local
	//    references (master := p.pty) but setting to nil before they exit is
	//    poor defensive practice. Wait for goroutines first, THEN nil the fields.
	if p.pty != nil {
		if err := p.pty.Close(); err != nil {
			p.logger.Warnf("failed to close PTY(ptyx): %v", err)
		}
	}

	if p.tty != nil {
		if err := p.tty.Close(); err != nil {
			p.logger.Warnf("failed to close PTY(tty): %v", err)
		}
	}

	// 3. Wait for goroutines to exit cleanly with timeout
	//    Goroutines will exit via context cancellation (checked every poll timeout)
	//    or EBADF from closed FDs. Use a generous timeout to handle slow systems.
	done := make(chan struct{})

	groutine.Go(context.Background(), "pty-wait-close", func(ctx context.Context) {
		p.wg.Wait()
		close(done)
	})

	// Wait with timeout: 3 goroutines * max(pollTimeout, 200ms) + 1s safety margin
	timeout := time.Duration(p.pollTimeoutMs)*time.Millisecond*3 + time.Second
	if timeout < 5*time.Second {
		timeout = 5 * time.Second // Minimum 5s timeout
	}

	select {
	case <-done:
		// All goroutines exited cleanly
	case <-time.After(timeout):
		// Goroutine leak scenario: 1-3 goroutines didn't exit within the timeout
		//
		// Root cause: Goroutines may be blocked in poll() or processing callbacks
		// They WILL eventually exit (max pollTimeoutMs after context cancel + FD close)
		// but remain invisible "zombies" until then.
		//
		// Impact: In high-churn scenarios (rapid PTY create/close cycles), these
		// zombie goroutines accumulate and waste resources until they self-terminate.
		//
		// Mitigation: Log detailed warning with expected self-termination time.
		// The goroutines are now orphaned and will clean up themselves.
		maxAdditionalWait := time.Duration(p.pollTimeoutMs) * time.Millisecond
		p.logger.Errorf("Close() timed out after %v waiting for goroutines to exit. "+
			"Goroutines will self-terminate within %v (pollTimeout). "+
			"PTY=%s. In high-churn scenarios, this indicates zombie goroutine accumulation.",
			timeout, maxAdditionalWait, p.ttyName)
		// Continue anyway to avoid blocking forever - goroutines are orphaned but will exit
	}

	// 4. Goroutines have exited (or timed out and are orphaned)
	//    Safe to nil out file references now
	p.pty = nil
	p.tty = nil

	// Note: Ring buffers don't need explicit Close() like channels do
	// The smallnest/ringbuffer library handles cleanup automatically

	return nil
}

// Stats return instantaneous stats for monitoring.
func (p *ringPTY) Stats() Stats {
	return Stats{
		WriteQueueLen:     int32(p.writeBuf.Length()),
		WriteQueueCap:     int32(p.writeBuf.Capacity()),
		ReadQueueLen:      int32(p.readBuf.Length()),
		ReadQueueCap:      int32(p.readBuf.Capacity()),
		DroppedWriteCount: atomic.LoadUint64(&p.droppedWrite),
		DroppedReadCount:  atomic.LoadUint64(&p.droppedRead),
		ReadBytesTotal:    atomic.LoadUint64(&p.readBytes),
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

// SetReadCallback sets or clears the callback for data arrival notifications.
// Pass nil to unregister the callback and stop notifications.
// The callback is invoked from a background goroutine and must be thread-safe.
// When setting a new callback, any buffered data will trigger an immediate notification.
func (p *ringPTY) SetReadCallback(cb ReadCallback) {
	// Guard against calls after Close() - dispatcher goroutine is dead so no-op
	if atomic.LoadUint32(&p.closed) == 1 {
		return
	}

	// Store new callback atomically (atomic.Value provides linearizable semantics)
	p.readCb.Store(cb)

	// Wake up dispatcher to process with new callback
	//
	// Memory ordering guarantees (Go Memory Model):
	//   If send succeeds: Store(cb) -[program-order]-> send -[channel-sync]-> receive -[program-order]-> Load()
	//   Therefore dispatcher is GUARANTEED to see the updated callback via happens-before transitivity.
	//
	//   If send hits default (notification already pending from previous data arrival):
	//   No happens-before relationship exists between this Store() and the pending receive.
	//   However, dispatcher reloads callback on EACH iteration (see cbIface := p.readCb.Load()),
	//   so it will see the new callback on the next batch (at most one batch processed with old callback).
	//   This is acceptable because the callback is reloaded frequently, ensuring eventual consistency.
	//
	// Correctness: atomic.Value prevents torn reads/writes, and the dispatcher's per-iteration
	// reload ensures the new callback is visible within one batch processing cycle (~16 chunks max).
	select {
	case p.readNotify <- struct{}{}:
		// Send succeeded - dispatcher will see updated callback (happens-before guaranteed)
	default:
		// Notification already pending - dispatcher will reload callback on next iteration
	}
}

func (p *ringPTY) ttyStdioAsyncDispatcher() {
	// Defensive panic recovery ensures wg.Done() always executes
	defer func() {
		if r := recover(); r != nil {
			p.logger.Errorf("dispatcher panicked (recovered): %v", r)
		}
		p.wg.Done()
	}()

	tmp := make([]byte, 4096)
	const maxChunksPerIteration = 16

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.readNotify:
			// Process all available data with callback protection
			for {
				// Check for cancellation before processing next batch
				select {
				case <-p.ctx.Done():
					return
				default:
				}

				cbIface := p.readCb.Load()
				if cbIface == nil {
					// No callback registered - drain notification and return to outer loop
					break
				}
				// Type assertion with safety check
				// INVARIANT: readCb must only contain ReadCallback or nil
				cb, ok := cbIface.(ReadCallback)
				if !ok {
					// Invariant violation - should never happen in production
					p.logger.Errorf("dispatcher: invalid type in readCb: %T (expected ReadCallback)", cbIface)
					p.readCb.Store(nil) // Clear invalid value
					break
				}

				chunksProcessed := 0
				for chunksProcessed < maxChunksPerIteration {
					// Check for cancellation during chunk processing
					select {
					case <-p.ctx.Done():
						return
					default:
					}

					n, err := p.readBuf.TryRead(tmp)
					if n == 0 || errors.Is(err, ringbuffer.ErrIsEmpty) {
						break
					}

					// Get slice from pool (reduces GC pressure in high-throughput scenarios)
					var chunk []byte
					if pooled := p.chunkPool.Get(); pooled != nil {
						chunk = pooled.([]byte)
					}
					// Ensure capacity (pool may return smaller slices or nil)
					if cap(chunk) < n {
						chunk = make([]byte, n)
					} else {
						chunk = chunk[:n]
					}
					copy(chunk, tmp[:n])

					// Protect against callback panics to prevent goroutine death
					// If callback panics, unregister it and notify error handler
					panicked := false
					func() {
						defer func() {
							if r := recover(); r != nil {
								panicked = true
								p.logger.Errorf("ReadCallback panicked: %v", r)
								// Unregister broken callback to prevent repeated panics
								p.readCb.Store(nil)
								// Notify error handler about callback failure
								if p.onError != nil {
									p.readErrorOnce.Do(func() {
										p.onError(fmt.Errorf("read callback panic: %v", r))
									})
								}
							}
							// Return slice to pool (always, even if callback panicked)
							// Pool accepts any size; it's ok if callback retained and we return different slice
							p.chunkPool.Put(chunk)
						}()
						cb(chunk)
					}()

					// Stop processing if callback panicked
					if panicked {
						break
					}

					chunksProcessed++
				}

				if p.readBuf.Length() == 0 || chunksProcessed == 0 {
					break
				}

				// Yield to scheduler before next batch
				runtime.Gosched()
			}
		}
	}
}
