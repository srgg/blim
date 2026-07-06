//go:build test

package testutils

import (
	"os"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Stdin preconditions for the process file descriptor 0.
//
// Unlike swapping the os.Stdin variable (see the io.read shutdown test in
// bridge_test.go), dup2 on fd 0 is visible to BOTH Go-level readers (os.Stdin
// wraps fd 0) and LuaJIT FFI calls (read(0), tcgetattr(0)), so these helpers
// work for any stdin consumer. Tests using them MUST NOT call t.Parallel():
// fd 0 is process-global state.

// StdinPTY is a PTY pair whose slave side is attached to fd 0.
// Writing to Master simulates keystrokes arriving on stdin.
type StdinPTY struct {
	Master *os.File
	suite  *PeripheralDeviceSuite
}

// GivenStdinPTY replaces fd 0 with the slave side of a fresh PTY pair, making
// stdin a real terminal. The original stdin is restored and the pair closed
// via t.Cleanup.
func (s *PeripheralDeviceSuite) GivenStdinPTY() *StdinPTY {
	master, slave, err := pty.Open()
	s.Require().NoError(err, "pty.Open MUST succeed")
	s.T().Cleanup(func() {
		// Best-effort close: the test is already over, failures here cannot
		// invalidate it, and there is no meaningful recovery.
		_ = master.Close()
		_ = slave.Close()
	})

	s.redirectStdin(int(slave.Fd()))
	return &StdinPTY{Master: master, suite: s}
}

// Type simulates the user pressing the given keys.
func (s *StdinPTY) Type(keys string) {
	_, err := s.Master.WriteString(keys)
	s.suite.Require().NoError(err, "simulated keypress MUST be written")
}

// GivenStdinPipe replaces fd 0 with the read end of a pipe — a stdin that is
// deliberately NOT a terminal. Returns the write end; closing it delivers EOF
// to stdin readers. Restoration and closing happen via t.Cleanup.
func (s *PeripheralDeviceSuite) GivenStdinPipe() *os.File {
	pipeRead, pipeWrite, err := os.Pipe()
	s.Require().NoError(err, "os.Pipe MUST succeed")
	s.T().Cleanup(func() {
		// Best-effort close: tests may legitimately close the write end
		// themselves to deliver EOF, so a double-close error is expected.
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
	})

	s.redirectStdin(int(pipeRead.Fd()))
	return pipeWrite
}

// redirectStdin points fd 0 at fd and registers restoration of the original
// stdin via t.Cleanup.
func (s *PeripheralDeviceSuite) redirectStdin(fd int) {
	saved, err := unix.Dup(0)
	s.Require().NoError(err, "saving original stdin MUST succeed")

	s.Require().NoError(unix.Dup2(fd, 0), "redirecting stdin MUST succeed")

	s.T().Cleanup(func() {
		// Best-effort restore: dup2 back to a descriptor saved moments ago
		// only fails on process-level fd corruption, which later tests will
		// surface immediately; there is no better recovery than proceeding.
		_ = unix.Dup2(saved, 0)
		_ = unix.Close(saved)
	})
}
