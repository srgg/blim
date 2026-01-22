package ptyio

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/smallnest/ringbuffer"
)

// ErrReaderClosed is returned when operations are attempted on a closed reader.
var ErrReaderClosed = errors.New("reader is closed")

// PrefetchFileReaderOptions configures PrefetchFileReader creation.
type PrefetchFileReaderOptions struct {
	BufferCap     int            // Ring buffer capacity (0 = DefaultReadCap)
	Logger        *logrus.Logger // Optional logger (nil = no-op logger)
	OnError       ErrorCallback  // Optional callback for critical loop failures
	PollTimeoutMs int            // Poll timeout in milliseconds (0 = DefaultPollTimeoutMs)
}

// PrefetchFileReader provides prefetched async reading from any *os.File.
// It embeds BaseBufferedAsyncReader for read loop infrastructure,
// and adds blocking ReadBytes/ReadLine methods with FIFO serialization.
//
// Use case: Singleton reader for os.Stdin in Lua scripts, where multiple
// subscription handlers may call io.read() concurrently.
//
// Design:
//   - Prefetches from file into ring buffer (background goroutine)
//   - ReadBytes/ReadLine block until data available or context cancelled
//   - consumeMu serializes concurrent read requests (FIFO via mutex)
//   - Does NOT close the file on shutdown (suitable for stdin)
type PrefetchFileReader struct {
	*BaseBufferedAsyncReader

	// consumeMu serializes multi-byte/line reads to prevent interleaving.
	// Multiple concurrent callers are queued and served FIFO.
	consumeMu sync.Mutex
}

// NewPrefetchFileReader creates a prefetching reader for any file.
// The file is NOT closed when the reader is closed (suitable for os.Stdin).
//
// Example:
//
//	reader, err := NewPrefetchFileReader(os.Stdin, nil)
//	if err != nil {
//	    return err
//	}
//	defer reader.Close()
//
//	data, err := reader.ReadBytes(ctx, 10)
func NewPrefetchFileReader(file *os.File, opts *PrefetchFileReaderOptions) (*PrefetchFileReader, error) {
	if file == nil {
		return nil, errors.New("file cannot be nil")
	}

	var baseOpts *BaseReaderOptions
	if opts != nil {
		baseOpts = &BaseReaderOptions{
			ReadCap:             opts.BufferCap,
			Logger:              opts.Logger,
			OnError:             opts.OnError,
			PollTimeoutMs:       opts.PollTimeoutMs,
			CloseFileOnShutdown: false, // Don't close stdin or other shared files
		}
	} else {
		baseOpts = &BaseReaderOptions{
			CloseFileOnShutdown: false,
		}
	}

	base := newBaseBufferedAsyncReader(file, baseOpts)

	r := &PrefetchFileReader{
		BaseBufferedAsyncReader: base,
	}

	// Start only the read loop (no dispatcher) - PrefetchFileReader uses direct
	// buffer access via ReadBytes/ReadLine, so dispatcher would compete for
	// notification signals and cause "press twice" bugs
	base.startReadLoopOnly("prefetch-file-reader")

	return r, nil
}

// ReadBytes reads exactly n bytes, blocking until available or ctx cancelled.
// Multiple concurrent callers are serialized (FIFO via consumeMu).
//
// Returns:
//   - (data, nil): Successfully read n bytes
//   - (nil, context.Canceled): Context was cancelled
//   - (nil, context.DeadlineExceeded): Context deadline exceeded
//   - (nil, ErrReaderClosed): Reader has been closed
func (r *PrefetchFileReader) ReadBytes(ctx context.Context, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}

	// Serialize concurrent read requests
	r.consumeMu.Lock()
	defer r.consumeMu.Unlock()

	if r.IsClosed() {
		return nil, ErrReaderClosed
	}

	result := make([]byte, 0, n)
	buf := make([]byte, n)

	for len(result) < n {
		// Check context first
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if r.IsClosed() {
			return nil, ErrReaderClosed
		}

		// Try to read from buffer
		needed := n - len(result)
		got, err := r.ReadBuffer().TryRead(buf[:needed])
		if got > 0 {
			result = append(result, buf[:got]...)
			continue
		}

		// No data available - wait for notification or context cancellation
		if err == nil || errors.Is(err, ringbuffer.ErrIsEmpty) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-r.ReadNotifyChannel():
				// Data might be available, try again
				continue
			}
		}

		// Unexpected error from ring buffer
		return nil, err
	}

	return result, nil
}

// ReadLine reads until newline (\n), blocking until available or ctx cancelled.
// The returned string includes the newline character if present.
// Multiple concurrent callers are serialized (FIFO via consumeMu).
//
// Returns:
//   - (line, nil): Successfully read line (includes \n)
//   - ("", context.Canceled): Context was cancelled
//   - ("", context.DeadlineExceeded): Context deadline exceeded
//   - ("", ErrReaderClosed): Reader has been closed
func (r *PrefetchFileReader) ReadLine(ctx context.Context) (string, error) {
	// Serialize concurrent read requests
	r.consumeMu.Lock()
	defer r.consumeMu.Unlock()

	if r.IsClosed() {
		return "", ErrReaderClosed
	}

	var result bytes.Buffer
	buf := make([]byte, 1)

	for {
		// Check context first
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if r.IsClosed() {
			return "", ErrReaderClosed
		}

		// Try to read one byte
		got, err := r.ReadBuffer().TryRead(buf)
		if got > 0 {
			result.WriteByte(buf[0])
			if buf[0] == '\n' {
				return result.String(), nil
			}
			continue
		}

		// No data available - wait for notification or context cancellation
		if err == nil || errors.Is(err, ringbuffer.ErrIsEmpty) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-r.ReadNotifyChannel():
				// Data might be available, try again
				continue
			}
		}

		// Unexpected error from ring buffer
		return "", err
	}
}

// Close shuts down the reader and its goroutines.
// The underlying file is NOT closed (to preserve stdin for other uses).
func (r *PrefetchFileReader) Close() error {
	// closeBase with no additional wait group and 0 extra goroutines
	r.closeBase(nil, 0)
	return nil
}