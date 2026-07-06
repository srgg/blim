package devicefactory

import (
	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/device/go-ble"
)

// DeviceFactory creates device.Scanner instances for BLE scanning operations.
// This is a variable so that it can be overridden in tests.
var DeviceFactory = func() (device.Scanner, error) {
	return goble.NewScanner()
}

// NewDevice creates a new BLE device with the specified address.
// This is the primary constructor for creating device instances.
func NewDevice(address string, logger *logrus.Logger) device.Device {
	return goble.NewBLEDeviceWithAddress(address, logger)
}

// NewDeviceFromAdvertisement creates a new BLE device from a device.Advertisement.
// This is used during scanning to create device instances from discovered advertisements.
func NewDeviceFromAdvertisement(adv device.Advertisement, logger *logrus.Logger) device.Device {
	return goble.NewBLEDeviceFromAdvertisement(adv, logger)
}
