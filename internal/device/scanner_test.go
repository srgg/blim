//go:build test

package device_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-ble/ble"
	"github.com/srgg/blim/internal/device"
	goble "github.com/srgg/blim/internal/device/go-ble"
	blemocks "github.com/srgg/blim/internal/testutils/mocks/goble"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type ScannerErrorTestSuite struct {
	suite.Suite
	originalFactory func() (ble.Device, error)
}

func (suite *ScannerErrorTestSuite) SetupSuite() {
	suite.originalFactory = goble.DeviceFactory
}

func (suite *ScannerErrorTestSuite) TearDownSuite() {
	goble.DeviceFactory = suite.originalFactory
}

func (suite *ScannerErrorTestSuite) TestScanner_NormalizesErrors() {
	// GOAL: Verify scanner normalizes platform-specific and context errors to domain sentinel errors
	//
	// TEST SCENARIO: Scan with various error conditions → errors normalized or passed through → error chain preserved

	testsWithSentinelError := []struct {
		name          string
		mockErr       error
		expectIsError error
		expectMessage string
	}{
		{
			name:          "normalizes darwin Bluetooth off error",
			mockErr:       fmt.Errorf("central manager has invalid state: have=4 want=5: is Bluetooth turned on?"),
			expectIsError: device.ErrBluetoothOff,
			expectMessage: "bluetooth is turned off",
		},
		{
			name:          "normalizes generic Bluetooth off error",
			mockErr:       fmt.Errorf("bluetooth is turned off"),
			expectIsError: device.ErrBluetoothOff,
			expectMessage: "bluetooth is turned off",
		},
		{
			name:          "normalizes context timeout to ErrTimeout",
			mockErr:       context.DeadlineExceeded,
			expectIsError: device.ErrTimeout,
			expectMessage: "timeout",
		},
		{
			name:          "passes through context canceled",
			mockErr:       context.Canceled,
			expectIsError: context.Canceled,
			expectMessage: "context canceled",
		},
	}

	for _, tt := range testsWithSentinelError {
		suite.Run(tt.name, func() {
			goble.DeviceFactory = func() (ble.Device, error) {
				mockDev := &blemocks.MockDevice{}
				mockDev.On("Scan",
					mock.Anything,
					mock.Anything,
					mock.Anything).
					Return(tt.mockErr)
				return mockDev, nil
			}

			scanner, err := goble.NewScanner()
			suite.NoError(err, "scanner creation MUST succeed")

			err = scanner.Scan(context.Background(), false, func(adv device.Advertisement) {})

			suite.Error(err, "scan MUST return error when mock returns error")
			suite.Contains(err.Error(), tt.expectMessage, "error message MUST contain expected text")
			suite.ErrorIs(err, tt.expectIsError, "error chain MUST contain expected sentinel error")
		})
	}

	suite.Run("passes through unknown errors", func() {
		goble.DeviceFactory = func() (ble.Device, error) {
			mockDev := &blemocks.MockDevice{}
			mockDev.On("Scan",
				mock.Anything,
				mock.Anything,
				mock.Anything).
				Return(fmt.Errorf("some other error"))
			return mockDev, nil
		}

		scanner, err := goble.NewScanner()
		suite.NoError(err, "scanner creation MUST succeed")

		err = scanner.Scan(context.Background(), false, func(adv device.Advertisement) {})

		suite.Error(err, "scan MUST return error when mock returns error")
		suite.Contains(err.Error(), "some other error", "error message MUST contain original error text")
		suite.NotErrorIs(err, device.ErrBluetoothOff, "unknown errors MUST NOT be normalized to ErrBluetoothOff")
		suite.NotErrorIs(err, context.Canceled, "unknown errors MUST NOT be normalized to context.Canceled")
	})
}

func TestScannerErrorTestSuite(t *testing.T) {
	suite.Run(t, new(ScannerErrorTestSuite))
}
