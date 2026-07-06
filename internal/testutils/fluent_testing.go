//go:build test

package testutils

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/srgg/blim/internal/device"
	"github.com/stretchr/testify/suite"
)

const SubscriptionCollectorCapacity = 10

type SubscriptionContext struct {
	collector  *SubscriptionRecordCollector
	cancelFunc func()
	maxRate    time.Duration
}

type GenericFluentTest[Self any] struct {
	suite         *suite.Suite
	device        device.Device
	subscriptions map[string]*SubscriptionContext
	self          Self
}

type FluentTest struct {
	*GenericFluentTest[*FluentTest]
}

type GenericTestableDevice struct {
	suite *PeripheralDeviceSuite
	device.Device

	disconnectChan chan struct{}
}

func NewGenericTestableDevice(suite *PeripheralDeviceSuite, device device.Device, disconnectChan chan struct{}) *GenericTestableDevice {
	return &GenericTestableDevice{
		suite:          suite,
		Device:         device,
		disconnectChan: disconnectChan,
	}
}

func (d *GenericTestableDevice) SimulateBluetoothDisconnect() {
	close(d.disconnectChan)
}

func (d *GenericTestableDevice) EmmitData(fn func(*PeripheralDataEmitter)) {
	go func() {
		de := NewPeripheralDataEmitterWithConnection(d.suite.T(), d.GetConnection())
		fn(de)
	}()
}

// T returns the testing.T for this device's test.
func (d *GenericTestableDevice) T() *testing.T {
	return d.suite.T()
}

func NewFluentTest(suite *suite.Suite, device device.Device) *FluentTest {
	ft := &FluentTest{}
	ft.GenericFluentTest = NewGenericFluentTest(ft, suite, device)
	return ft
}

func NewGenericFluentTest[Self any](self Self, suite *suite.Suite, device device.Device) *GenericFluentTest[Self] {
	return &GenericFluentTest[Self]{
		self:          self,
		suite:         suite,
		device:        device,
		subscriptions: map[string]*SubscriptionContext{},
	}
}

func (ft *GenericFluentTest[Self]) Sleep(timeout time.Duration) Self {
	time.Sleep(timeout)
	return ft.self
}

func (ft *GenericFluentTest[Self]) EmmitData(fn func(emitter *PeripheralDataEmitter)) Self {
	go func() {
		de := NewPeripheralDataEmitterWithConnection(ft.suite.T(), ft.device.GetConnection())
		fn(de)
	}()
	return ft.self
}

func (ft *GenericFluentTest[Self]) Subscribe(
	name string,
	opts []*device.SubscribeOptions,
	mode device.StreamMode,
	maxRate time.Duration,
	drainDuration time.Duration,
) Self {
	ctx := SubscriptionContext{
		collector: NewSubscriptionRecordCollector(SubscriptionCollectorCapacity),
		maxRate:   maxRate,
	}

	cancel, err := ft.device.GetConnection().Subscribe(
		opts,
		mode,
		maxRate,
		drainDuration,
		ctx.collector.Callback(),
	)
	ctx.cancelFunc = cancel

	ft.suite.Require().NoErrorf(err, "%s: subscription MUST succeed", name)
	ft.subscriptions[name] = &ctx
	return ft.self
}

// SubscribeOnEach creates a StreamEveryUpdate subscription. See Subscribe for drainDuration semantics.
func (ft *GenericFluentTest[Self]) SubscribeOnEach(name string, drainDuration time.Duration, opts ...*device.SubscribeOptions) Self {
	return ft.Subscribe(name, opts, device.StreamEveryUpdate, 0, drainDuration)
}

// SubscribeBatched creates a StreamBatched subscription. See Subscribe for drainDuration semantics.
func (ft *GenericFluentTest[Self]) SubscribeBatched(name string, rate time.Duration, drainDuration time.Duration, opts ...*device.SubscribeOptions) Self {
	return ft.Subscribe(name, opts, device.StreamBatched, rate, drainDuration)
}

// SubscribeAggregated creates a StreamAggregated subscription. See [Subscribe] for drainDuration semantics.
func (ft *GenericFluentTest[Self]) SubscribeAggregated(name string, rate time.Duration, drainDuration time.Duration, opts ...*device.SubscribeOptions) Self {
	return ft.Subscribe(name, opts, device.StreamAggregated, rate, drainDuration)
}

func (ft *GenericFluentTest[Self]) CancelSubscription(name string) Self {
	ft.subscriptions[name].cancelFunc()
	return ft.self
}

func (ft *GenericFluentTest[Self]) WaitSubscription(name string, count uint32, timeout time.Duration) Self {
	ft.subscriptions[name].collector.WaitForCount(count, timeout*time.Second)
	return ft.self
}

func (ft *GenericFluentTest[Self]) consumeSubscription(name string, ignoreFields []string, expected []device.Record) Self {
	// Wrap each expected record in RecordJSON
	wrapped := make([]DeviceRecordJSON, len(expected))
	for i := range expected {
		wrapped[i] = DeviceRecordJSON{&expected[i]}
	}

	expectedJson, err := json.Marshal(wrapped)
	if err != nil {
		ft.suite.Require().NoErrorf(err, "%s: failed to marshal expected records to  json", name)
	}

	actualJson, err := ft.subscriptions[name].collector.ConsumeRecordsAsJSONString()
	ft.suite.Require().NoErrorf(err, "%s: failed to consume records from collector", name)

	NewJSONAsserter(ft.suite.T()).
		WithOptions(
			WithIgnoreArrayOrder(true),
			WithIgnoreExtraKeys(false),
			WithIgnoredFields(ignoreFields...),
		).
		Assert(string(actualJson), string(expectedJson))

	return ft.self
}

func (ft *GenericFluentTest[Self]) ConsumeEmptySubscription(name string) Self {
	return ft.consumeSubscription(name, []string{}, []device.Record{})
}

func (ft *GenericFluentTest[Self]) ConsumeSubscription(name string, ignoreFields []string, expected ...device.Record) Self {
	return ft.consumeSubscription(name, ignoreFields, expected)
}
