// Package scanner discovers nearby Bluetooth Low Energy devices.
//
// Scan performs a bounded discovery pass and returns the devices found, keyed
// by address; Events exposes a live stream of discovery events during the scan
// (see Events for its overwrite-oldest buffering). The package is importable
// as a library and also backs the "blim scan" CLI command.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cornelk/hashmap"
	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/devicefactory"
	"github.com/srgg/blim/internal/lua"
)

// ProgressCallback is called when the scan phase changes
type ProgressCallback func(phase string)

// DeviceEventType marks if the device was newly discovered or updated
type DeviceEventType int

const (
	EventNew DeviceEventType = iota
	EventUpdated
)

type DeviceEvent struct {
	Type       DeviceEventType
	DeviceInfo device.DeviceInfo
	Timestamp  time.Time // When this event occurred
}

// DeviceEntry is a discovered device as returned by Scan, keyed by address.
type DeviceEntry struct {
	// Device is the read-only view of the device (identity, RSSI, advertised
	// data). It is the same underlying object as the internal handle, exposed
	// as the immutable DeviceInfo interface so callers cannot connect or mutate it.
	Device device.DeviceInfo
	// device is the scanner's full, mutable handle for the same device; kept
	// unexported so it can be refreshed as new advertisements arrive without
	// widening the public surface.
	device   device.Device
	LastSeen time.Time // wall-clock time this device was last observed
}

// Scanner handles BLE device discovery
type Scanner struct {
	devices *hashmap.Map[string, DeviceEntry]
	events  *lua.RingChannel[DeviceEvent]
	logger  *logrus.Logger

	scanOptions *ScanOptions
	scanDevice  device.Scanner
}

// ScanOptions configures scanning behavior
type ScanOptions struct {
	Duration        time.Duration
	DuplicateFilter bool
	ServiceUUIDs    []string
	AllowList       []string
	BlockList       []string
}

// DefaultScanOptions returns default scanning options
func DefaultScanOptions() *ScanOptions {
	return &ScanOptions{
		Duration:        10 * time.Second,
		DuplicateFilter: true,
	}
}

// NewScanner creates a new BLE scanner
func NewScanner(logger *logrus.Logger) (*Scanner, error) {
	if logger == nil {
		logger = logrus.New()
	}

	return &Scanner{
		events: lua.NewRingChannel[DeviceEvent](100),
		logger: logger,
	}, nil
}

// Scan performs BLE discovery with provided options
func (s *Scanner) Scan(ctx context.Context, opts *ScanOptions, progressCallback ProgressCallback) (map[string]DeviceEntry, error) {
	s.devices = hashmap.New[string, DeviceEntry]()

	if opts == nil {
		opts = DefaultScanOptions()
	}
	if progressCallback == nil {
		progressCallback = func(string) {} // No-op callback
	}

	s.logger.WithField("duration", opts.Duration).Info("Starting BLE scan...")

	// Report scanning phase
	progressCallback("Scanning")

	dev, err := devicefactory.DeviceFactory()
	if err != nil {
		return nil, fmt.Errorf("failed to create BLE device: %w", err)
	}
	s.scanDevice = dev

	s.scanOptions = opts
	defer func() {
		s.scanOptions = nil
	}()

	err = s.scanDevice.Scan(ctx, opts.DuplicateFilter, s.handleAdvertisement)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, device.ErrTimeout) {
		return nil, fmt.Errorf("scan failed: %w", err)
	}

	s.logger.WithField("device_count", s.devices.Len()).Info("BLE scan completed")

	// Report processing phase
	progressCallback("Processing results")

	devices := make(map[string]DeviceEntry, s.devices.Len())
	s.devices.Range(func(key string, value DeviceEntry) bool {
		devices[key] = DeviceEntry{
			Device:   value.device,
			device:   value.device,
			LastSeen: value.LastSeen,
		}
		return true
	})

	return devices, nil
}

// handleAdvertisement updates existing or adds a new device
func (s *Scanner) handleAdvertisement(adv device.Advertisement) {
	deviceID := adv.Addr()

	e, existing := s.devices.Get(deviceID)
	if !existing {
		if !s.shouldIncludeDevice(adv, s.scanOptions) {
			return
		}

		entry := DeviceEntry{
			device:   devicefactory.NewDeviceFromAdvertisement(adv, s.logger),
			LastSeen: time.Now(),
		}

		e, existing = s.devices.GetOrInsert(deviceID, entry)
	}

	event := DeviceEvent{
		DeviceInfo: e.device,
		Timestamp:  e.LastSeen,
	}

	if existing {
		e.device.Update(adv)
		e.LastSeen = time.Now()
		event.Timestamp = e.LastSeen
		event.Type = EventUpdated
	} else {
		s.logger.WithFields(logrus.Fields{
			"device":  e.device.Name(),
			"address": e.device.Address(),
			"rssi":    e.device.RSSI(),
		}).Info("Discovered new device")
		event.Type = EventNew
	}

	s.events.ForceSend(event)
}

// shouldIncludeDevice applies to allow/block/service filters
func (s *Scanner) shouldIncludeDevice(adv device.Advertisement, opts *ScanOptions) bool {
	addr := adv.Addr()

	for _, blocked := range opts.BlockList {
		if addr == blocked {
			return false
		}
	}

	if len(opts.AllowList) > 0 {
		allowed := false
		for _, a := range opts.AllowList {
			if addr == a {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	if len(opts.ServiceUUIDs) > 0 {
		hasRequired := false
		for _, required := range opts.ServiceUUIDs {
			requiredNorm := device.NormalizeUUID(required)
			for _, advUUID := range adv.Services() {
				advNorm := device.NormalizeUUID(advUUID)
				if requiredNorm == advNorm {
					hasRequired = true
					break
				}
			}
			if hasRequired {
				break
			}
		}
		if !hasRequired {
			return false
		}
	}

	return true
}

// makeDeviceList returns a snapshot of discovered devices
func (s *Scanner) makeDeviceList() []DeviceEntry {
	devs := make([]DeviceEntry, 0, s.devices.Len())

	s.devices.Range(func(key string, value DeviceEntry) bool {
		devs = append(devs, DeviceEntry{Device: value.device, LastSeen: value.LastSeen})
		return true
	})

	return devs
}

// Events returns a read-only channel of device events emitted during a scan.
//
// The channel is backed by a bounded ring buffer (capacity 100) with
// overwrite-oldest semantics: the scanner never blocks on a slow consumer, so
// if events are produced faster than they are read, the oldest undelivered
// events are silently dropped. Consumers that must observe every event should
// read the channel promptly; those that only need the final device set can use
// Scan's returned map instead.
func (s *Scanner) Events() <-chan DeviceEvent {
	return s.events.C()
}
