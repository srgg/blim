//go:build test

package main

import (
	"bytes"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/srg/blim/internal/device"
	"github.com/srg/blim/internal/testutils"
)

// Test device addresses for consistent mock device identification
const (
	TestDeviceAddress1 = "00:00:00:00:00:01"
	TestDeviceAddress2 = "00:00:00:00:00:02"
)

// CommandTestSuite extends MockBLEPeripheralSuite with command testing utilities.
// All cmd/blim test suites should embed this instead of MockBLEPeripheralSuite.
type CommandTestSuite struct {
	testutils.MockBLEPeripheralSuite
}

// ConnectDevice connects to mock device and returns cleanup function.
// Uses TestDeviceAddress1 if address is empty.
// DrainDuration is set to 0 via OnConnected (mocks don't have CoreBluetooth cached values to drain).
func (s *CommandTestSuite) ConnectDevice(address string) (device.Device, func()) {
	if address == "" {
		address = TestDeviceAddress1
	}
	return s.MockBLEPeripheralSuite.ConnectDevice(address, nil)
}

// CaptureStdout executes fn while capturing stdout, returns captured output.
// Stdout is restored even if fn panics.
func (s *CommandTestSuite) CaptureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	s.Require().NoError(err, "pipe creation MUST succeed")
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	fn()

	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// ExecuteCommand runs a cobra command with args, returns output and error.
func (s *CommandTestSuite) ExecuteCommand(cmd *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}
