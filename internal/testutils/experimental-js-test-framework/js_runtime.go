package experimental_js_test_framework

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/dop251/goja"
	"github.com/srgg/blim/internal/device"
	"github.com/srgg/blim/internal/testutils"
)

// SubscriptionWrapper JS helper for a subscription with a separate collector
type SubscriptionWrapper struct {
	Collector *testutils.SubscriptionRecordCollector
	cancel    func()
}

// Cancel JS-callable
func (s *SubscriptionWrapper) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

// ConsumeRecordsAsJSON JS-callable
func (s *SubscriptionWrapper) ConsumeRecordsAsJSON() string {
	data, err := s.Collector.ConsumeRecordsAsJSONString()
	if err != nil {
		panic(err)
	}
	return data
}

// ConsumeRecords JS-callable
func (s *SubscriptionWrapper) ConsumeRecords() []*device.Record {
	data, err := s.Collector.ConsumeRecords()
	if err != nil {
		panic(err)
	}
	return data
}

type MockData struct {
	Service        string
	Characteristic string
	Value          []byte
}

// TestRuntime wraps Go api as methods and exposes to JS.
// Uses GenericTestableDevice to integrate with the fluent testing infrastructure,
// delegating to EmmitData pattern for data simulation and accessing connection via GetConnection().
// NOTE: limitation of goja - it can only expose methods to JS
type TestRuntime struct {
	device *testutils.GenericTestableDevice
	vm     *goja.Runtime
}

// bindMethods returns a JS object exposing only the listed methods of a Go object
func (r *TestRuntime) bindMethods(vm *goja.Runtime, obj interface{}, methods ...string) *goja.Object {
	v := reflect.ValueOf(obj)
	t := v.Type()
	jsObj := vm.NewObject()

	for _, name := range methods {
		m, ok := t.MethodByName(name)
		if !ok {
			panic(fmt.Sprintf("method %s not found on %T", name, obj))
		}

		// Bind the method
		_ = jsObj.Set(name, func(method reflect.Method) func(goja.FunctionCall) goja.Value {
			return func(fc goja.FunctionCall) goja.Value {
				// Call the Go method
				out := method.Func.Call([]reflect.Value{v})

				// Convert result (if any) to JS value
				if len(out) > 0 {
					return vm.ToValue(out[0].Interface())
				}
				return goja.Undefined()
			}
		}(m))
	}

	return jsObj
}

// PushMockData pushes mock data asynchronously using the fluent testing infrastructure.
// Delegates to NewPeripheralDataEmitterWithConnection pattern from GenericFluentTest.
func (r *TestRuntime) PushMockData(data []MockData) {
	go func() {
		conn := r.device.GetConnection()
		b := testutils.NewPeripheralDataEmitterWithConnection(r.device.T(), conn).AllowMultiValue()

		for _, d := range data {
			b.WithService(d.Service).WithCharacteristic(d.Characteristic, d.Value...)
		}

		_, err := b.EmmitFor(conn, true)
		if err != nil {
			panic(err)
		}
	}()
}

// Subscribe and return wrapper
func (r *TestRuntime) Subscribe(
	opts []*device.SubscribeOptions,
	modeVal interface{}, // numeric from JS
	maxRateMs int,
) *goja.Object {

	var mode device.StreamMode

	switch v := modeVal.(type) {
	case int64:
		mode = device.StreamMode(v)
	default:
		panic(fmt.Sprintf("invalid mode type: %T", modeVal))
	}

	collector := testutils.NewSubscriptionRecordCollector(10)

	cancel, err := r.device.GetConnection().Subscribe(opts, mode, time.Duration(maxRateMs)*time.Millisecond, 0, collector.Callback())
	if err != nil {
		panic(err)
	}

	wrapper := &SubscriptionWrapper{
		Collector: collector,
		cancel:    cancel,
	}

	// Automatically bind only the methods JS should see
	return r.bindMethods(r.vm, wrapper, "Cancel", "ConsumeRecords", "ConsumeRecordsAsJSON")
}

// Sleep helper
func (r *TestRuntime) Sleep(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// AssertJSON expected can be a string or a JS object
func (r *TestRuntime) AssertJSON(expected interface{}, actual interface{}, ignored []string) {
	// Convert expected to JSON string if not a string
	var expectedJSON string
	switch v := expected.(type) {
	case string:
		expectedJSON = v
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		expectedJSON = string(bytes)
	}

	// Convert actual to JSON string if not a string
	var actualJSON string
	switch v := actual.(type) {
	case string:
		actualJSON = v
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		actualJSON = string(bytes)
	}

	jsonAssert := testutils.NewJSONAsserter(r.device.T())

	if err := jsonAssert.WithOptions(
		testutils.WithIgnoreArrayOrder(true),
		testutils.WithIgnoreExtraKeys(false),
		testutils.WithNilToEmptyArray(true),
		testutils.WithCompareOnlyExpectedKeys(false),
		testutils.WithIgnoredFields(ignored...),
	).AssertErr(expectedJSON, actualJSON); err != nil {
		r.vm.NewTypeError()
		panic(r.vm.ToValue(err))
	}
}

// NewTestRuntime creates a TestRuntime from a GenericTestableDevice.
// The device provides access to the connection, testing.T, and fluent testing infrastructure.
func NewTestRuntime(td *testutils.GenericTestableDevice, vm *goja.Runtime) *TestRuntime {
	vm.Set("StreamEveryUpdate", int(device.StreamEveryUpdate))
	vm.Set("StreamBatched", int(device.StreamBatched))
	vm.Set("StreamAggregated", int(device.StreamAggregated))

	return &TestRuntime{
		device: td,
		vm:     vm,
	}
}
