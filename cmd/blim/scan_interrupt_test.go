//go:build test

package main

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	blelib "github.com/go-ble/ble"
	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
	"github.com/srgg/blim/internal/devicefactory"
	"github.com/srgg/blim/internal/testutils"
	"github.com/srgg/blim/scanner"
	"github.com/stretchr/testify/suite"
)

// bleScanningDeviceMock wraps ble.Device to implement device.Scanner for device_test
type bleScanningDeviceMock struct {
	blelib.Device
}

// Scan adapts ble.Device.Scan to use device.Advertisement
func (s *bleScanningDeviceMock) Scan(ctx context.Context, allowDup bool, handler func(device.Advertisement)) error {
	bleHandler := func(adv blelib.Advertisement) {
		handler(goble.NewBLEAdvertisement(adv))
	}
	return s.Device.Scan(ctx, allowDup, bleHandler)
}

// ScanInterruptSuite device_test scan interrupt behavior with proper mock setup
type ScanInterruptSuite struct {
	CommandTestSuite
}

// createTestLogger creates a configured logger for device_test
func (s *ScanInterruptSuite) createTestLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.RFC3339,
	})
	return logger
}

// createTestScanner creates a scanner with test logger
func (s *ScanInterruptSuite) createTestScanner() *scanner.Scanner {
	scan, err := scanner.NewScanner(s.createTestLogger())
	s.Require().NoError(err, "scanner creation MUST succeed")
	return scan
}

// SetupTest configures mock advertisements for scan device_test
func (s *ScanInterruptSuite) SetupTest() {
	// Configure mock advertisements for scanning
	adv1 := testutils.StandardAdvertisement("AA:BB:CC:DD:EE:FF")
	adv2 := testutils.NewAdvertisementBuilder().
		WithAddress("11:22:33:44:55:66").
		WithName("TestDevice2").
		WithRSSI(-60).
		WithConnectable(true).
		WithManufacturerData([]byte{}).
		WithNoServiceData().
		WithServices().
		WithTxPower(0).
		Build()

	// Use WithBlockingScan() to make the scan block after emitting advertisements,
	// mimicking BLE scanning behavior for interrupt testing.
	s.GivenPeripheral(func(builder *testutils.PeripheralDeviceBuilder) {
		builder.WithScanAdvertisements().
			WithBlockingScan().WithAdvertisements(adv1, adv2).Build()
	})

	// Call parent to apply mock configuration
	s.CommandTestSuite.SetupTest()
}

// hangingScanDevice simulates Bluetooth being disabled mid-scan by emitting one ad then hanging
type hangingScanDevice struct {
	adv device.Advertisement
}

// Scan emits one advertisement, then returns Bluetooth error (simulating Bluetooth disabled)
func (h *hangingScanDevice) Scan(ctx context.Context, allowDup bool, handler func(device.Advertisement)) error {
	// Emit one advertisement (scan starts working)
	handler(h.adv)

	// Then return Bluetooth error (simulating Bluetooth disabled mid-scan)
	time.Sleep(10 * time.Millisecond) // Small delay to simulate the scan working briefly
	return device.ErrBluetoothOff
}

// TestSingleScanInterrupt device_test that a single scan with duration responds to SIGINT
func (s *ScanInterruptSuite) TestSingleScanInterrupt() {
	// GOAL: Verify a single scan with duration exists cleanly on SIGINT
	//
	// TEST SCENARIO: Start timed scan → send SIGINT after 100ms → scan completes within 5s

	logger := s.createTestLogger()
	scan := s.createTestScanner()

	cfg := &scanConfig{
		scanTimeout:  20 * time.Second,
		outputFormat: "table",
	}

	scanOpts := &scanner.ScanOptions{
		Duration:        20 * time.Second,
		DuplicateFilter: true,
	}

	done := make(chan error, 1)
	go func() {
		done <- runSingleScan(io.Discard, scan, scanOpts, cfg, logger)
	}()

	time.Sleep(100 * time.Millisecond)

	process, _ := os.FindProcess(os.Getpid())
	if err := process.Signal(syscall.SIGINT); err != nil {
		s.Require().NoError(err)
	}

	select {
	case <-done:
		// Test passes - scan completed (either with or without error is acceptable for interrupt)
	case <-time.After(5 * time.Second):
		s.Fail("single scan MUST complete within 5s after SIGINT")
	}
}

// TestWatchModeInterrupt device_test that watch mode responds to SIGINT
func (s *ScanInterruptSuite) TestWatchModeInterrupt() {
	// GOAL: Verify watch mode exits cleanly on SIGINT without hanging
	//
	// TEST SCENARIO: Start watch mode → send SIGINT after 100ms → watch mode completes within 5s

	logger := s.createTestLogger()
	scan := s.createTestScanner()

	cfg := &scanConfig{
		scanTimeout:  0,
		outputFormat: "table",
	}

	watchOpts := &scanner.ScanOptions{
		Duration:        0,
		DuplicateFilter: true,
	}

	done := make(chan error, 1)
	go func() {
		done <- runWatchMode(io.Discard, scan, watchOpts, cfg, logger)
	}()

	time.Sleep(100 * time.Millisecond)

	process, _ := os.FindProcess(os.Getpid())
	if err := process.Signal(syscall.SIGINT); err != nil {
		s.Require().NoError(err)
	}

	select {
	case <-done:
		// Test passes - watch mode completed (either with or without error is acceptable for interrupt)
	case <-time.After(5 * time.Second):
		s.Fail("watch mode MUST complete within 5s after SIGINT")
	}
}

// TestWatchModeHangAfterScanFinishes device_test that watch mode runs indefinitely and responds to an interrupt
func (s *ScanInterruptSuite) TestWatchModeHangAfterScanFinishes() {
	// GOAL: Verify watch mode runs indefinitely until interrupted
	//
	// TEST SCENARIO: Start watch mode → verify still running after 100ms → send SIGINT → completes within 5s

	logger := s.createTestLogger()
	scan := s.createTestScanner()

	cfg := &scanConfig{
		scanTimeout:  0,
		outputFormat: "table",
	}

	shortOpts := &scanner.ScanOptions{
		DuplicateFilter: true,
	}

	// Run scan till interrupt
	done := make(chan error, 1)
	go func() {
		done <- runWatchMode(io.Discard, scan, shortOpts, cfg, logger)
	}()

	time.Sleep(500 * time.Millisecond)

	select {
	case err := <-done:
		s.Fail("watch mode MUST NOT exit without interrupt: %v", err)
	default:
		// Expected - still running
	}

	process, _ := os.FindProcess(os.Getpid())
	if err := process.Signal(syscall.SIGINT); err != nil {
		s.Require().NoError(err)
	}

	select {
	case <-done:
		// Test passes - watch mode completed after interrupt
	case <-time.After(5 * time.Second):
		s.Fail("watch mode MUST complete within 5s after SIGINT")
	}
}

// TestWatchModeBluetoothDisabled verifies watch mode detects stalled scans when Bluetooth is disabled
func (s *ScanInterruptSuite) TestWatchModeBluetoothDisabled() {
	// GOAL: Verify watch mode exits with an error when Bluetooth is disabled mid-scan
	//
	// TEST SCENARIO: Bluetooth disabled during scan → returns ErrBluetoothOff → watch mode exits with error

	adv := testutils.StandardAdvertisement("AA:BB:CC:DD:EE:FF")
	hangingDev := &hangingScanDevice{adv: adv}

	// Save and restore original factory to avoid polluting other tests
	originalFactory := devicefactory.DeviceFactory
	defer func() { devicefactory.DeviceFactory = originalFactory }()

	devicefactory.DeviceFactory = func() (device.Scanner, error) {
		return hangingDev, nil
	}

	logger := s.createTestLogger()
	scan := s.createTestScanner()

	cfg := &scanConfig{
		scanTimeout:  0,
		outputFormat: "table",
	}

	watchOpts := &scanner.ScanOptions{
		Duration:        0,
		DuplicateFilter: true,
	}

	done := make(chan error, 1)
	go func() {
		done <- runWatchMode(io.Discard, scan, watchOpts, cfg, logger)
	}()

	time.Sleep(100 * time.Millisecond)

	select {
	case err := <-done:
		s.Assert().Error(err, "watch mode MUST return error when Bluetooth disabled")
		s.Assert().ErrorIs(err, device.ErrBluetoothOff, "error MUST be device.ErrBluetoothOff")
	case <-time.After(500 * time.Millisecond):
		s.Fail("watch mode MUST exit within 500ms when Bluetooth disabled")
	}
}

// TestScanInterrupt is the test entry point
func TestScanInterrupt(t *testing.T) {
	suite.Run(t, new(ScanInterruptSuite))
}
