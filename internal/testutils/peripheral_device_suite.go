//go:build test

package testutils

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	blelib "github.com/go-ble/ble"
	"github.com/sirupsen/logrus"
	"github.com/srgg/blim/internal/device"

	goble "github.com/srgg/blim/internal/device/go-ble"
	"github.com/stretchr/testify/suite"
)

// PeripheralDeviceSuite supports nested subtest builder inheritance.
type PeripheralDeviceSuite struct {
	suite.Suite

	Logger *logrus.Logger

	// BLE device factory
	OriginalDeviceFactory func() (blelib.Device, error)
	TestTimeout           time.Duration

	// Default peripheral builder
	defaultBuilder *PeripheralDeviceBuilder

	// Track per-test builders
	tBuilders map[*testing.T]*PeripheralDeviceBuilder

	// Current builder (for subtest overrides)
	currentBuilder *PeripheralDeviceBuilder

	// Default advertisement builder
	defaultAdsBuilder *AdvertisementArrayBuilder[[]device.Advertisement]

	// Track per-test advertisement builders
	tAds map[*testing.T]*AdvertisementArrayBuilder[[]device.Advertisement]

	// Current ads builder (for subtests)
	currentAdsBuilder *AdvertisementArrayBuilder[[]device.Advertisement]

	muDeviceFactory sync.Mutex

	parentTracker *ParentTracker
}

// ----------------------------
// Suite lifecycle
// ----------------------------

type ParentTracker struct {
	// parent -> children
	children map[*testing.T][]*testing.T

	// child -> parent
	parent map[*testing.T]*testing.T

	lock  sync.Mutex
	stack []*testing.T
}

func NewParentTracker() *ParentTracker {
	return &ParentTracker{
		children: make(map[*testing.T][]*testing.T),
		parent:   make(map[*testing.T]*testing.T),
		stack:    []*testing.T{},
	}
}

func (s *ParentTracker) OnSetup(t *testing.T) {
	s.lock.Lock()
	defer s.lock.Unlock()

	// If stack is not empty, top is parent
	if len(s.stack) > 0 {
		parent := s.stack[len(s.stack)-1]
		s.parent[t] = parent
		s.children[parent] = append(s.children[parent], t)
	}

	// push current test onto stack
	s.stack = append(s.stack, t)
}

func (s *ParentTracker) OnTeardown(t *testing.T) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if len(s.stack) > 0 {
		s.stack = s.stack[:len(s.stack)-1]
	}
}

func (s *ParentTracker) GetParent(t *testing.T) *testing.T {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.parent[t]
}

func (s *PeripheralDeviceSuite) SetupSuite() {
	s.Logger = logrus.New()
	s.TestTimeout = 30 * time.Second
	s.OriginalDeviceFactory = goble.DeviceFactory

	// Restore global state at end
	s.T().Cleanup(func() {
		if s.OriginalDeviceFactory != nil {
			goble.DeviceFactory = s.OriginalDeviceFactory
		}
	})

	s.parentTracker = NewParentTracker()
}

func (s *PeripheralDeviceSuite) SetupTest() {
	// Ensure default builder exists
	t := s.T()
	if s.tBuilders == nil {
		s.tBuilders = make(map[*testing.T]*PeripheralDeviceBuilder)
	}

	if s.currentBuilder == nil {
		s.currentBuilder = s.getOrCreateBuilder(t)
	}

	s.tBuilders[t] = s.currentBuilder

	if s.tAds != nil {
		if ads, ok := s.tAds[t]; ok {
			s.tBuilders[t].WithScanAdvertisements().WithAdvertisements(ads.Build()...).Build()
		}
	}

	// Override device factory for this test
	s.OriginalDeviceFactory = goble.DeviceFactory
	goble.DeviceFactory = func() (blelib.Device, error) {
		t := s.T()

		builder := s.getOrCreateBuilder(t)
		if builder == nil {
			panic(fmt.Errorf("PeripheralDeviceSuite internal error: builder is nil"))
		}

		return builder.Build(), nil
	}

	s.parentTracker.OnSetup(s.T())
}

func (s *PeripheralDeviceSuite) TearDownTest() {
	s.parentTracker.OnTeardown(s.T())

	t := s.T()
	delete(s.tBuilders, t)
	if s.tAds != nil {
		delete(s.tAds, t)
	}
	if s.OriginalDeviceFactory != nil {
		goble.DeviceFactory = s.OriginalDeviceFactory
		s.OriginalDeviceFactory = nil
	}
}

func (s *PeripheralDeviceSuite) SetupSubTest() {
	s.parentTracker.OnSetup(s.T())
}

func (s *PeripheralDeviceSuite) TearDownSubTest() {
	s.parentTracker.OnTeardown(s.T())
}

// ----------------------------
// Public helpers
// ----------------------------

// CurrentBuilder returns the current peripheral builder (may be nil if GivenPeripheral not called)
func (s *PeripheralDeviceSuite) CurrentBuilder() *PeripheralDeviceBuilder {
	return s.currentBuilder
}

// GivenPeripheral creates a **new builder** for the current T, inheriting from parent if any.
func (s *PeripheralDeviceSuite) GivenPeripheral(fn func(*PeripheralDeviceBuilder)) {
	t := s.T()
	if s.tBuilders == nil {
		s.tBuilders = make(map[*testing.T]*PeripheralDeviceBuilder)
	}

	newBuilder := NewPeripheralDeviceBuilder(t)

	if s.currentAdsBuilder != nil {
		newBuilder.WithScanAdvertisements().WithAdvertisements(s.currentAdsBuilder.Build()...).Build()
		newBuilder.scanDelayMs = s.currentAdsBuilder.scanDelayMs
	}

	// Apply user customization
	fn(newBuilder)

	// Store it
	s.tBuilders[t] = newBuilder
	s.currentBuilder = newBuilder
}

// GivenAdvertisements creates/updates advertisement builder for current T
func (s *PeripheralDeviceSuite) GivenAdvertisements(fn func(*AdvertisementArrayBuilder[[]device.Advertisement])) {
	t := s.T()
	if s.tAds == nil {
		s.tAds = make(map[*testing.T]*AdvertisementArrayBuilder[[]device.Advertisement])
	}

	// Always create a new builder
	newAds := NewAdvertisementArrayBuilder[[]device.Advertisement]()

	// Apply user customization
	fn(newAds)

	// Store per test
	s.tAds[t] = newAds
	s.currentAdsBuilder = newAds
}

// TestableDevice - concrete type with FluentTest
type TestableDevice struct {
	*GenericTestableDevice
	fluentTest *FluentTest
}

func (d *TestableDevice) FluentTest() *FluentTest {
	if d.fluentTest == nil {
		d.fluentTest = NewFluentTest(&d.suite.Suite, d.Device)
	}
	return d.fluentTest
}

func NewTestableDevice(suite *PeripheralDeviceSuite, device device.Device, disconnectChan chan struct{}) *TestableDevice {
	return &TestableDevice{
		GenericTestableDevice: NewGenericTestableDevice(suite, device, disconnectChan),
	}
}

// Connect returns a device for the current T and registers cleanup automatically
func (s *PeripheralDeviceSuite) Connect(addr string) *TestableDevice {
	t := s.T()
	dev, cleanup := s.ConnectDevice(addr, nil)
	t.Cleanup(cleanup)
	return dev
}

// ConnectDevice creates a device and returns a cleanup function
func (s *PeripheralDeviceSuite) ConnectDevice(addr string, opts *device.ConnectOptions) (*TestableDevice, func()) {
	return s.ConnectDeviceWithContext(context.Background(), addr, opts)
}

func (s *PeripheralDeviceSuite) getOrCreateBuilder(t *testing.T) *PeripheralDeviceBuilder {
	current := t
	var builder *PeripheralDeviceBuilder

	for current != nil {
		builder = s.tBuilders[current] // whatever you store per test
		if builder != nil {
			// found it, do something
			break
		}
		current = s.parentTracker.GetParent(current) // move up to parent
	}

	if builder == nil {
		builder = createDefaultPeripheralBuilder2(t)
		s.tBuilders[t] = builder
	}

	return builder
}

// ConnectDeviceWithContext creates a device with context support and returns a cleanup function
func (s *PeripheralDeviceSuite) ConnectDeviceWithContext(ctx context.Context, addr string, opts *device.ConnectOptions) (*TestableDevice, func()) {
	if opts == nil {
		opts = &device.ConnectOptions{}
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 5 * time.Second
	}

	t := s.T()

	builder := s.getOrCreateBuilder(t)

	var dev device.Device
	var dc chan struct{}
	s.muDeviceFactory.Lock()
	{
		defer s.muDeviceFactory.Unlock()

		// Override device factory for this test
		original := goble.DeviceFactory
		goble.DeviceFactory = func() (blelib.Device, error) {
			return builder.Build(), nil
		}

		// Connect to peripheral
		dev = goble.NewBLEDeviceWithAddress(addr, s.Logger)
		err := dev.Connect(ctx, opts)

		// Restore original device factory
		goble.DeviceFactory = original

		dc = builder.GetDisconnectChannel()
		s.Require().NoError(err)
	}

	cleanup := func() { _ = dev.Disconnect() }
	t.Cleanup(cleanup)

	return NewTestableDevice(s, dev, dc), cleanup
}

func createDefaultPeripheralBuilder2(t *testing.T) *PeripheralDeviceBuilder {
	return NewPeripheralDeviceBuilder(t).
		FromJSON(`
{
  "services": [
    {
      "uuid": "180F",
      "characteristics": [
        { "uuid": "2A19", "properties": "read,notify", "value": [50] }
      ]
    }
  ]
}`)
}
