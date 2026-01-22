package ptyio

//import (
//	"fmt"
//	"os"
//	"os/signal"
//	"sync"
//	"syscall"
//
//	"github.com/creack/pty"
//	"github.com/sirupsen/logrus"
//	"golang.org/x/sys/unix"
//	"golang.org/x/term"
//)NO
//
//// PTYRelay replaces stdin (fd 0) with a closeable descriptor for graceful shutdown.
////
//// Problem: Lua scripts using LuaJIT FFI (e.g., ffi.C.read(STDIN_FILENO, ...))
//// perform blocking reads that cannot be interrupted by Go's context cancellation.
//// When the user presses Ctrl+C, the Go runtime cancels the context, but the FFI
//// read syscall continues blocking indefinitely, preventing clean shutdown.
////
//// Solution: PTYRelay replaces stdin with a closeable descriptor. Real stdin is
//// forwarded through via a goroutine. On Close(), the relay signals EOF to pending
//// reads, allowing the Lua script to exit gracefully.
////
//// TTY preservation: When stdin is a terminal, PTYRelay uses a PTY pair instead of
//// a plain pipe. This preserves terminal attributes so Lua FFI calls like
//// tcgetattr()/tcsetattr() work correctly for raw mode input.
////
//// Lifecycle:
////   - Close() sends EOF to unblock reads (call from signal handler)
////   - Cleanup() restores original stdin (call via defer after script exits)
//type PTYRelay struct {
//	eofSender   func() // Closes PTY master/pipe writer to send EOF
//	cleanupFunc func() // Restores fd 0 and terminal state
//	logger      *logrus.Logger
//	closeOnce   sync.Once
//	cleanupOnce sync.Once
//	closed      bool
//	mu          sync.Mutex
//}
//
//// NewPTYRelay creates a PTYRelay that replaces fd 0 with a closeable descriptor.
//// Uses PTY when stdin is a TTY (preserves terminal attributes), plain pipe otherwise.
//func NewPTYRelay(logger *logrus.Logger) (*PTYRelay, error) {
//	if term.IsTerminal(0) {
//		return newPTYRelayWithPTY(logger)
//	}
//	return newPTYRelayWithPipe(logger)
//}
//
//// Close signals EOF to unblock any FFI read() calls on stdin.
//// Call this from the signal handler to unblock blocking reads.
//// Safe to call multiple times.
//func (r *PTYRelay) Close() error {
//	r.closeOnce.Do(func() {
//		r.mu.Lock()
//		r.closed = true
//		r.mu.Unlock()
//
//		// Send EOF by closing PTY master / pipe writer
//		if r.eofSender != nil {
//			r.eofSender()
//		}
//	})
//	return nil
//}
//
//// Cleanup restores the original stdin and terminal state.
//// Call this via defer after the script exits to restore the process state.
//// Safe to call multiple times.
//func (r *PTYRelay) Cleanup() {
//	r.cleanupOnce.Do(func() {
//		if r.cleanupFunc != nil {
//			r.cleanupFunc()
//		}
//	})
//}
//
//// IsClosed returns true if Close() has been called.
//func (r *PTYRelay) IsClosed() bool {
//	r.mu.Lock()
//	defer r.mu.Unlock()
//	return r.closed
//}
//
//// newPTYRelayWithPTY creates a PTY pair to replace stdin while preserving terminal attributes.
//// Used when stdin is a TTY so Lua FFI terminal operations (tcgetattr, etc.) work.
//func newPTYRelayWithPTY(logger *logrus.Logger) (*PTYRelay, error) {
//	// Create PTY pair
//	ptyMaster, ptySlave, err := pty.Open()
//	if err != nil {
//		return nil, fmt.Errorf("failed to create stdin PTY: %w", err)
//	}
//
//	// Duplicate original stdin fd before replacing
//	originalStdinFd, err := syscall.Dup(0)
//	if err != nil {
//		ptyMaster.Close()
//		ptySlave.Close()
//		return nil, fmt.Errorf("failed to dup stdin: %w", err)
//	}
//
//	// Save the original terminal state for restoration on cleanup
//	originalState, err := term.GetState(originalStdinFd)
//	if err != nil {
//		syscall.Close(originalStdinFd)
//		ptyMaster.Close()
//		ptySlave.Close()
//		return nil, fmt.Errorf("failed to get terminal state: %w", err)
//	}
//
//	// Copy terminal settings from original stdin to PTY slave
//	// This ensures the Lua script sees the same terminal configuration
//	if slaveTermios, err := unix.IoctlGetTermios(originalStdinFd, unix.TIOCGETA); err == nil {
//		_ = unix.IoctlSetTermios(int(ptySlave.Fd()), unix.TIOCSETA, slaveTermios)
//	}
//
//	// Copy window size from the original terminal to PTY slave
//	// This ensures ANSI escape codes for cursor positioning work correctly
//	if err := pty.InheritSize(os.Stdin, ptySlave); err == nil {
//		// Also set on master for completeness
//		_ = pty.InheritSize(os.Stdin, ptyMaster)
//	}
//
//	// Put the original terminal into "raw input" mode: disable line buffering and echo
//	// but keep output processing (OPOST) so \n -> \r\n translation works
//	if err := setRawInputMode(originalStdinFd); err != nil {
//		syscall.Close(originalStdinFd)
//		ptyMaster.Close()
//		ptySlave.Close()
//		return nil, fmt.Errorf("failed to set terminal to raw input mode: %w", err)
//	}
//
//	// Replace fd 0 with the PTY slave
//	if err := syscall.Dup2(int(ptySlave.Fd()), 0); err != nil {
//		term.Restore(originalStdinFd, originalState)
//		syscall.Close(originalStdinFd)
//		ptyMaster.Close()
//		ptySlave.Close()
//		return nil, fmt.Errorf("failed to replace stdin with PTY: %w", err)
//	}
//	ptySlave.Close() // fd 0 now owns this descriptor
//
//	// Handle window resize signals - propagate to PTY
//	sigWinch := make(chan os.Signal, 1)
//	signal.Notify(sigWinch, syscall.SIGWINCH)
//	go func() {
//		for range sigWinch {
//			// Copy new size from original terminal to PTY (fd 0)
//			if ws, err := unix.IoctlGetWinsize(originalStdinFd, unix.TIOCGWINSZ); err == nil {
//				_ = unix.IoctlSetWinsize(0, unix.TIOCSWINSZ, ws)
//			}
//		}
//	}()
//
//	// Create a Go file wrapper for original stdin - needed for proper cleanup coordination
//	originalStdin := os.NewFile(uintptr(originalStdinFd), "stdin")
//
//	// Forward real stdin to PTY master so scripts can read user input
//	go func() {
//		buf := make([]byte, 1024)
//		for {
//			n, readErr := originalStdin.Read(buf)
//			if n > 0 {
//				if _, writeErr := ptyMaster.Write(buf[:n]); writeErr != nil {
//					if logger != nil {
//						logger.Debugf("PTYRelay PTY: write error: %v", writeErr)
//					}
//					return
//				}
//			}
//			if readErr != nil {
//				return
//			}
//		}
//	}()
//
//	// eofSender closes the PTY master to send EOF to the slave (fd 0).
//	// This unblocks any read() calls waiting on stdin.
//	eofSender := func() {
//		if logger != nil {
//			logger.Debug("PTYRelay: closing PTY master to send EOF")
//		}
//		ptyMaster.Close()
//	}
//
//	// Cleanup restores terminal state and fd 0. Called after script exits.
//	// Must use Go's (*os.File).Close() instead of syscall.Close() for originalStdin.
//	// The forwarding goroutine is blocked in originalStdin.Read() waiting for terminal input.
//	// Using syscall.Close() bypasses Go's runtime - the blocked Read won't be woken up properly,
//	// causing hangs. Go's Close() coordinates with the I/O poller to wake blocked goroutines.
//	cleanup := func() {
//		if logger != nil {
//			logger.Debug("PTYRelay cleanup: starting")
//		}
//		signal.Stop(sigWinch)
//		close(sigWinch)
//		term.Restore(originalStdinFd, originalState)
//		originalStdin.Close()
//		syscall.Dup2(originalStdinFd, 0)
//		if logger != nil {
//			logger.Debug("PTYRelay cleanup: done")
//		}
//	}
//
//	return &PTYRelay{
//		eofSender:   eofSender,
//		cleanupFunc: cleanup,
//		logger:      logger,
//	}, nil
//}
//
//// newPTYRelayWithPipe creates a plain pipe to replace stdin.
//// Used when stdin is not a TTY (e.g., piped input).
//func newPTYRelayWithPipe(logger *logrus.Logger) (*PTYRelay, error) {
//	pipeReader, pipeWriter, err := os.Pipe()
//	if err != nil {
//		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
//	}
//
//	originalStdinFd, err := syscall.Dup(0)
//	if err != nil {
//		pipeReader.Close()
//		pipeWriter.Close()
//		return nil, fmt.Errorf("failed to dup stdin: %w", err)
//	}
//
//	if err := syscall.Dup2(int(pipeReader.Fd()), 0); err != nil {
//		syscall.Close(originalStdinFd)
//		pipeReader.Close()
//		pipeWriter.Close()
//		return nil, fmt.Errorf("failed to replace stdin: %w", err)
//	}
//	pipeReader.Close() // fd 0 now owns this descriptor
//
//	// Create Go file wrapper for original stdin BEFORE goroutine starts.
//	// This ensures cleanup can close it properly to wake the blocked Read().
//	originalStdin := os.NewFile(uintptr(originalStdinFd), "stdin")
//
//	// Forward real stdin to the pipe so scripts can read user input
//	go func() {
//		buf := make([]byte, 1024)
//		for {
//			n, readErr := originalStdin.Read(buf)
//			if n > 0 {
//				if _, writeErr := pipeWriter.Write(buf[:n]); writeErr != nil {
//					if logger != nil {
//						logger.Debugf("PTYRelay pipe: write error: %v", writeErr)
//					}
//					return
//				}
//			}
//			if readErr != nil {
//				return
//			}
//		}
//	}()
//
//	// eofSender closes the pipe writer to send EOF to the reader (fd 0).
//	// This unblocks any read() calls waiting on stdin.
//	eofSender := func() {
//		if logger != nil {
//			logger.Debug("PTYRelay: closing pipe writer to send EOF")
//		}
//		pipeWriter.Close()
//	}
//
//	// Cleanup restores fd 0. Called after script exits.
//	// Must use Go's (*os.File).Close() instead of syscall.Close() for originalStdin.
//	// The forwarding goroutine is blocked in originalStdin.Read() waiting for input.
//	// Using syscall.Close() bypasses Go's runtime - the blocked Read won't be woken up properly,
//	// causing hangs. Go's Close() coordinates with the I/O poller to wake blocked goroutines.
//	cleanup := func() {
//		if logger != nil {
//			logger.Debug("PTYRelay pipe cleanup: starting")
//		}
//		originalStdin.Close() // Wakes blocked Read() in forwarding goroutine
//		syscall.Dup2(originalStdinFd, 0)
//		if logger != nil {
//			logger.Debug("PTYRelay pipe cleanup: done")
//		}
//	}
//
//	return &PTYRelay{
//		eofSender:   eofSender,
//		cleanupFunc: cleanup,
//		logger:      logger,
//	}, nil
//}
//
//// setRawInputMode configures terminal for raw input while preserving output and signals.
//// Unlike term.MakeRaw(), this only disables line buffering and echo, but keeps:
//// - OPOST enabled so \n -> \r\n translation works for output
//// - ISIG enabled so Ctrl+C generates SIGINT for clean shutdown
//func setRawInputMode(fd int) error {
//	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
//	if err != nil {
//		return err
//	}
//
//	// Disable line buffering and echo, but keep ISIG for Ctrl+C signal handling
//	termios.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ECHOK |
//		unix.ECHONL | unix.IEXTEN
//
//	// Disable special input processing (except signal-related)
//	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
//		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
//
//	// Read returns immediately with whatever is available
//	termios.Cc[unix.VMIN] = 1
//	termios.Cc[unix.VTIME] = 0
//
//	return unix.IoctlSetTermios(fd, unix.TIOCSETA, termios)
//}
