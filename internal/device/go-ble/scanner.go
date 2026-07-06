package goble

import (
	"context"

	ble "github.com/go-ble/ble"
	"github.com/srgg/blim/internal/device"
)

// bleScanner wraps ble.Device to implement a device.Scanner interface
type bleScanner struct {
	dev ble.Device
}

// Scan wraps the raw ble.Device.Scan to convert ble.Advertisement to the device.Advertisement
func (s *bleScanner) Scan(ctx context.Context, allowDup bool, handler func(device.Advertisement)) error {
	// Adapter: convert a handler expecting a device.Advertisement to the one expecting ble.Advertisement
	bleHandler := func(adv ble.Advertisement) {
		handler(NewBLEAdvertisement(adv))
	}
	err := s.dev.Scan(ctx, allowDup, bleHandler)
	if err != nil {
		return NormalizeError(err)
	}
	return nil
}

// NewScanner creates a device.Scanner instance for BLE scanning operations.
func NewScanner() (device.Scanner, error) {
	dev, err := DeviceFactory()
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &bleScanner{dev: dev}, nil
}
