// Package ptyio provides async-buffered I/O infrastructure for PTY and file reading.
//
// BaseBufferedAsyncReader is the foundation for both PTY master reading and generic
// file reading (e.g., stdin). It provides poll-based async I/O with ring buffer
// semantics, callback dispatch, and proper shutdown coordination.
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

	"github.com/sirupsen/logrus"
	"github.com/smallnest/ringbuffer"
	"github.com/srgg/blim/internal/groutine"
	"golang.org/x/sys/unix"
)

// BaseReaderOptions configures BaseBufferedAsyncReader with fine-grained control.
// Zero values use sensible defaults.
type BaseReaderOptions struct {
	ReadCap       int            // Ring buffer capacity (0 = DefaultReadCap)
	Logger        *logrus.Logger // Optional logger (nil = no-op logger)
	OnError       ErrorCallback  // Optional callback for critical loop failures
	PollTimeoutMs int            // Poll timeout in milliseconds (0 = DefaultPollTimeoutMs)

	// CloseFileOnShutdown controls whether the file is closed during shutdown.
	// Set to true for PTY (owned file), false for stdin (shared resource).
	CloseFileOnShutdown bool
}

// BaseReaderStats provides runtime counters for the reader.
type BaseReaderStats struct {
	QueueLen         int32  // Current bytes in buffer
	QueueCap         int32  // Buffer capacity
	DroppedReadCount uint64 // Bytes dropped due to buffer overflow
	ReadBytesTotal   uint64 // Total bytes successfully buffered
}

// BaseBufferedAsyncReader provides shared read infrastructure for async buffered I/O.
// Both ringPTY and BufferedReader embed this to share the poll-based read loop,
// callback dispatch, and lifecycle management.
//
// Design rationale:
//   - Poll-based I/O works with both blocking and non-blocking fds
//   - Ring buffer prevents blocking when consumer is slow (oldest data dropped)
//   - Callback dispatch runs in dedicated goroutine with panic recovery
//   - Chunk pooling reduces GC pressure in high-throughput scenarios
type BaseBufferedAsyncReader struct {
	logger *logrus.Logger
	file   *os.File // source file for reading
	fileFd int      // cached at construction; avoids race between Fd() and Close()

	readBuf    *ringbuffer.RingBuffer
	readCb     atomic.Value  // ReadCallback or nil
	readNotify chan struct{} // buffered(1), non-blocking signal

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed uint32 // atomic boolean

	// Metrics (atomic)
	droppedRead uint64
	readBytes   uint64

	// Configuration
	pollTimeoutMs       int
	onError             ErrorCallback
	readErrorOnce       sync.Once
	closeFileOnShutdown bool

	// chunkPool reduces GC pressure in high-throughput callback scenarios.
	// Slices are allocated once, reused across callbacks, returned to pool after use.
	chunkPool sync.Pool
}

// newBaseBufferedAsyncReader creates a new base reader with the given file and options.
// The file must be open and readable. This is an internal constructor used by
// ringPTY and BufferedReader.
func newBaseBufferedAsyncReader(file *os.File, opts *BaseReaderOptions) *BaseBufferedAsyncReader {
	if opts == nil {
		opts = &BaseReaderOptions{}
	}

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

	ctx, cancel := context.WithCancel(context.Background())

	return &BaseBufferedAsyncReader{
		logger:              logger,
		file:                file,
		fileFd:              int(file.Fd()),
		readBuf:             ringbuffer.New(readCap),
		readNotify:          make(chan struct{}, 1), // buffered so signal never blocks
		ctx:                 ctx,
		cancel:              cancel,
		pollTimeoutMs:       pollTimeout,
		onError:             opts.OnError,
		closeFileOnShutdown: opts.CloseFileOnShutdown,
	}
}

// startGoroutines launches the read loop and dispatcher goroutines.
// Must be called after construction to begin async I/O.
// Use startReadLoopOnly for direct buffer access (BufferedReader) to avoid
// dispatcher competing for notification signals.
func (b *BaseBufferedAsyncReader) startGoroutines(name string) {
	b.wg.Add(2)

	groutine.Go(b.ctx, name+"-read-loop", func(ctx context.Context) {
		b.readLoop()
	})

	groutine.Go(b.ctx, name+"-dispatcher", func(ctx context.Context) {
		b.runDispatcher()
	})
}

// startReadLoopOnly launches only the read loop goroutine (no dispatcher).
// Use this for direct buffer access (BufferedReader.ReadBytes/ReadLine) where
// the dispatcher would compete for notification signals.
func (b *BaseBufferedAsyncReader) startReadLoopOnly(name string) {
	b.wg.Add(1)

	groutine.Go(b.ctx, name+"-read-loop", func(ctx context.Context) {
		b.readLoop()
	})
}

// readLoop is the generic poll-based read loop that works with any *os.File.
// It polls the file for readability, reads available data into the ring buffer,
// and signals the dispatcher when data arrives.
func (b *BaseBufferedAsyncReader) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Errorf("readLoop panicked (recovered): %v", r)
		}
		b.wg.Done()
	}()

	b.logger.Debugf("[Read Loop] STARTING for fd %d", b.fileFd)

	// Capture *os.File reference to prevent nil pointer dereference after Close()
	file := b.file
	pollFd := []unix.PollFd{{Fd: int32(b.fileFd), Events: unix.POLLIN}}
	buf := make([]byte, 4096)

	for {
		// Check context cancellation (COLD path - checked every poll timeout)
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		// Wait for readable data or timeout
		nReady, err := unix.Poll(pollFd, b.pollTimeoutMs)
		if err != nil && !errors.Is(err, syscall.EINTR) {
			b.logger.Warnf("readLoop poll error: %v", err)
			continue
		}
		if nReady == 0 {
			continue // timeout, check context
		}

		n, err := file.Read(buf)

		if n > 0 {
			// Write bytes to ring buffer (bulk operation)
			written, writeErr := b.readBuf.Write(buf[:n])
			if writeErr != nil && !errors.Is(writeErr, ringbuffer.ErrIsFull) {
				b.logger.Warnf("readLoop Write error: %v", writeErr)
				continue
			}

			// Track and warn about dropped bytes
			if written < n {
				dropped := n - written
				atomic.AddUint64(&b.droppedRead, uint64(dropped))
				b.logger.Warnf("Read buffer overflow: dropped %d bytes (received %d, buffered %d)",
					dropped, n, written)
			}

			atomic.AddUint64(&b.readBytes, uint64(written))

			// Notify that data is available (for both callback dispatch and direct buffer reads)
			if written > 0 {
				b.SignalDataAvailable()
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
				b.logger.Debug("readLoop exiting: FD closed")
				return
			case errors.Is(err, io.EOF):
				// EOF means source closed (expected if external process exits or stdin closes)
				b.logger.Debug("readLoop exiting: EOF")
				return
			default:
				// Critical error — notify caller and exit
				b.logger.Warnf("readLoop exiting on error: %v", err)
				if b.onError != nil {
					b.readErrorOnce.Do(func() {
						b.onError(fmt.Errorf("readLoop critical error: %w", err))
					})
				}
				return
			}
		}
	}
}

// runDispatcher is the callback dispatcher goroutine.
// It waits for data availability signals and invokes the registered callback
// with batched data from the ring buffer.
func (b *BaseBufferedAsyncReader) runDispatcher() {
	defer func() {
		if r := recover(); r != nil {
			b.logger.Errorf("dispatcher panicked (recovered): %v", r)
		}
		b.wg.Done()
	}()

	tmp := make([]byte, 4096)
	const maxChunksPerIteration = 16

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-b.readNotify:
			// Process all available data with callback protection
			for {
				// Check for cancellation before processing next batch
				select {
				case <-b.ctx.Done():
					return
				default:
				}

				cbIface := b.readCb.Load()
				if cbIface == nil {
					// No callback registered - drain notification and return to outer loop
					break
				}

				// Type assertion with safety check
				cb, ok := cbIface.(ReadCallback)
				if !ok {
					// Invariant violation - should never happen
					b.logger.Errorf("dispatcher: invalid type in readCb: %T (expected ReadCallback)", cbIface)
					b.readCb.Store(nil)
					break
				}

				chunksProcessed := 0
				for chunksProcessed < maxChunksPerIteration {
					// Check for cancellation during chunk processing
					select {
					case <-b.ctx.Done():
						return
					default:
					}

					n, err := b.readBuf.TryRead(tmp)
					if n == 0 || errors.Is(err, ringbuffer.ErrIsEmpty) {
						break
					}

					// Get slice from pool (reduces GC pressure)
					var chunk []byte
					if pooled := b.chunkPool.Get(); pooled != nil {
						chunk = pooled.([]byte)
					}
					if cap(chunk) < n {
						chunk = make([]byte, n)
					} else {
						chunk = chunk[:n]
					}
					copy(chunk, tmp[:n])

					// Protect against callback panics
					panicked := false
					func() {
						defer func() {
							if r := recover(); r != nil {
								panicked = true
								b.logger.Errorf("ReadCallback panicked: %v", r)
								b.readCb.Store(nil)
								if b.onError != nil {
									b.readErrorOnce.Do(func() {
										b.onError(fmt.Errorf("read callback panic: %v", r))
									})
								}
							}
							b.chunkPool.Put(chunk)
						}()
						cb(chunk)
					}()

					if panicked {
						break
					}

					chunksProcessed++
				}

				if b.readBuf.Length() == 0 || chunksProcessed == 0 {
					break
				}

				// Yield to scheduler before next batch
				runtime.Gosched()
			}
		}
	}
}

// SignalDataAvailable sends a non-blocking signal to the dispatcher.
// Exported for embedders (like ringPTY) that need to signal during Close().
func (b *BaseBufferedAsyncReader) SignalDataAvailable() {
	select {
	case b.readNotify <- struct{}{}:
	default:
		// Signal already pending
	}
}

// SetReadCallback sets or clears the callback for data arrival notifications.
// Pass nil to unregister the callback. Thread-safe.
func (b *BaseBufferedAsyncReader) SetReadCallback(cb ReadCallback) {
	if atomic.LoadUint32(&b.closed) == 1 {
		return
	}

	b.readCb.Store(cb)

	// Wake up dispatcher to process with new callback
	select {
	case b.readNotify <- struct{}{}:
	default:
		// Notification already pending
	}
}

// ReadStats returns current read statistics.
func (b *BaseBufferedAsyncReader) ReadStats() BaseReaderStats {
	return BaseReaderStats{
		QueueLen:         int32(b.readBuf.Length()),
		QueueCap:         int32(b.readBuf.Capacity()),
		DroppedReadCount: atomic.LoadUint64(&b.droppedRead),
		ReadBytesTotal:   atomic.LoadUint64(&b.readBytes),
	}
}

// ReadBuffer returns the underlying ring buffer for direct access.
// Used by BufferedReader for blocking read operations.
func (b *BaseBufferedAsyncReader) ReadBuffer() *ringbuffer.RingBuffer {
	return b.readBuf
}

// ReadNotifyChannel returns the notification channel for blocking reads.
// Used by BufferedReader to wait for data availability.
func (b *BaseBufferedAsyncReader) ReadNotifyChannel() <-chan struct{} {
	return b.readNotify
}

// Context returns the reader's context for cancellation checks.
func (b *BaseBufferedAsyncReader) Context() context.Context {
	return b.ctx
}

// IsClosed returns true if the reader has been closed.
func (b *BaseBufferedAsyncReader) IsClosed() bool {
	return atomic.LoadUint32(&b.closed) == 1
}

// File returns the underlying file for direct access.
// Used by ringPTY write loop to access PTY master.
func (b *BaseBufferedAsyncReader) File() *os.File {
	return b.file
}

// FileFd returns the cached file descriptor.
func (b *BaseBufferedAsyncReader) FileFd() int {
	return b.fileFd
}

// Logger returns the configured logger.
func (b *BaseBufferedAsyncReader) Logger() *logrus.Logger {
	return b.logger
}

// PollTimeoutMs returns the configured poll timeout.
func (b *BaseBufferedAsyncReader) PollTimeoutMs() int {
	return b.pollTimeoutMs
}

// OnError returns the error callback.
func (b *BaseBufferedAsyncReader) OnError() ErrorCallback {
	return b.onError
}

// Cancel cancels the reader's context to signal goroutines to exit.
// This is exposed for embedders that need to cancel before doing additional cleanup.
func (b *BaseBufferedAsyncReader) Cancel() {
	b.cancel()
}

// WaitGroup returns the base's WaitGroup for coordination.
// Embedders can use this to wait for base goroutines.
func (b *BaseBufferedAsyncReader) WaitGroup() *sync.WaitGroup {
	return &b.wg
}

// MarkClosed atomically marks the reader as closed.
// Returns true if this call performed the transition from open to closed.
func (b *BaseBufferedAsyncReader) MarkClosed() bool {
	return atomic.CompareAndSwapUint32(&b.closed, 0, 1)
}

// closeBase performs base shutdown: cancels context, optionally closes file,
// waits for goroutines. Returns true if this call performed the close.
func (b *BaseBufferedAsyncReader) closeBase(additionalWaitGroup *sync.WaitGroup, timeoutMultiplier int) bool {
	if !b.MarkClosed() {
		return false
	}

	// Cancel context to signal goroutines to exit
	b.cancel()

	// Signal notification channel to unblock any waiters (e.g., BufferedReader.ReadBytes)
	b.SignalDataAvailable()

	// Close file to unblock any I/O operations (if configured)
	if b.closeFileOnShutdown && b.file != nil {
		if err := b.file.Close(); err != nil {
			b.logger.Warnf("failed to close file: %v", err)
		}
	}

	// Wait for goroutines with timeout
	done := make(chan struct{})
	groutine.Go(context.Background(), "base-reader-wait-close", func(ctx context.Context) {
		b.wg.Wait()
		if additionalWaitGroup != nil {
			additionalWaitGroup.Wait()
		}
		close(done)
	})

	// Calculate timeout: goroutines * pollTimeout + safety margin
	numGoroutines := 2 + timeoutMultiplier // base has 2, PTY adds 1 more
	timeout := time.Duration(b.pollTimeoutMs)*time.Millisecond*time.Duration(numGoroutines) + time.Second
	if timeout < 5*time.Second {
		timeout = 5 * time.Second
	}

	select {
	case <-done:
		// All goroutines exited cleanly
	case <-time.After(timeout):
		b.logger.Errorf("Close() timed out after %v waiting for goroutines", timeout)
	}

	return true
}
