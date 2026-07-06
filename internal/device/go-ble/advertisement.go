package goble

import (
	"github.com/go-ble/ble"
	"github.com/srgg/blim/internal/device"
)

// BLEAdvertisement wraps ble.Advertisement to implement device.Advertisement interface
type BLEAdvertisement struct {
	adv ble.Advertisement
}

// NewBLEAdvertisement creates a new BLEAdvertisement wrapper
func NewBLEAdvertisement(adv ble.Advertisement) device.Advertisement {
	return &BLEAdvertisement{adv: adv}
}

func (a *BLEAdvertisement) LocalName() string        { return a.adv.LocalName() }
func (a *BLEAdvertisement) ManufacturerData() []byte { return a.adv.ManufacturerData() }
func (a *BLEAdvertisement) TxPowerLevel() int        { return int(a.adv.TxPowerLevel()) }
func (a *BLEAdvertisement) Connectable() bool        { return a.adv.Connectable() }
func (a *BLEAdvertisement) RSSI() int                { return a.adv.RSSI() }
func (a *BLEAdvertisement) Addr() string             { return a.adv.Addr().String() }

func (a *BLEAdvertisement) ServiceData() []struct {
	UUID string
	Data []byte
} {
	bleServiceData := a.adv.ServiceData()
	result := make([]struct {
		UUID string
		Data []byte
	}, len(bleServiceData))
	for i, sd := range bleServiceData {
		result[i].UUID = sd.UUID.String()
		result[i].Data = sd.Data
	}
	return result
}

func (a *BLEAdvertisement) Services() []string {
	bleServices := a.adv.Services()
	result := make([]string, len(bleServices))
	for i, svc := range bleServices {
		result[i] = svc.String()
	}
	return result
}

func (a *BLEAdvertisement) OverflowService() []string {
	bleServices := a.adv.OverflowService()
	result := make([]string, len(bleServices))
	for i, svc := range bleServices {
		result[i] = svc.String()
	}
	return result
}

func (a *BLEAdvertisement) SolicitedService() []string {
	bleServices := a.adv.SolicitedService()
	result := make([]string, len(bleServices))
	for i, svc := range bleServices {
		result[i] = svc.String()
	}
	return result
}

// Unwrap returns the underlying ble.Advertisement for internal use within go-ble package
func (a *BLEAdvertisement) Unwrap() ble.Advertisement {
	return a.adv
}
