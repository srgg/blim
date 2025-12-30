//go:build test

package testutils

import "github.com/srg/blim/internal/device"

// StandardPeripheral is the universal default peripheral for most command tests.
// Contains Heart Rate (180d) and Battery (180f) services with common characteristics.
const StandardPeripheral = `{
	"services": [
		{
			"uuid": "180d",
			"characteristics": [
				{"uuid": "2a37", "properties": "read,notify", "value": [0, 90]},
				{"uuid": "2a38", "properties": "read", "value": [1]}
			]
		},
		{
			"uuid": "180f",
			"characteristics": [
				{"uuid": "2a19", "properties": "read,notify", "value": [100]}
			]
		}
	]
}`

// AmbiguousCharPeripheral extends StandardPeripheral with GAP service (1800)
// containing a duplicate 2a37 characteristic for UUID ambiguity testing.
// Used by: read_test.go, uuid_resolve_test.go
const AmbiguousCharPeripheral = `{
	"services": [
		{
			"uuid": "180d",
			"characteristics": [
				{
					"uuid": "2a37",
					"properties": "read,notify",
					"value": [0, 90],
					"descriptors": [{"uuid": "2902", "value": [0, 0]}]
				},
				{"uuid": "2a38", "properties": "read", "value": [1]}
			]
		},
		{
			"uuid": "180f",
			"characteristics": [
				{
					"uuid": "2a19",
					"properties": "read,notify",
					"value": [75],
					"descriptors": [
						{"uuid": "2902", "value": [0, 0]},
						{"uuid": "2901", "value": [66, 97, 116, 116, 101, 114, 121]}
					]
				}
			]
		},
		{
			"uuid": "1800",
			"characteristics": [
				{"uuid": "2a37", "properties": "read", "value": [99]}
			]
		}
	]
}`

// NotifiablePeripheral extends StandardPeripheral with a 128-bit UUID service
// for testing long UUID handling in notifications. All characteristics support notify.
// Used by: subscribe_test.go
const NotifiablePeripheral = `{
	"services": [
		{
			"uuid": "180d",
			"characteristics": [
				{"uuid": "2a37", "properties": "read,notify", "value": [0, 90]},
				{"uuid": "2a38", "properties": "read,notify", "value": [1]}
			]
		},
		{
			"uuid": "180f",
			"characteristics": [
				{"uuid": "2a19", "properties": "read,notify", "value": [100]}
			]
		},
		{
			"uuid": "12345678-1234-5678-1234-567812345678",
			"characteristics": [
				{"uuid": "abcdef01-1234-5678-1234-567812345678", "properties": "read,notify", "value": [0]}
			]
		}
	]
}`

// StandardAdvertisement creates a test advertisement with common default values.
// Address is customizable to support multiple device scenarios in tests.
// Used by: scan_interrupt_test.go, inspect_test.go
func StandardAdvertisement(address string) device.Advertisement {
	return NewAdvertisementBuilder().
		WithAddress(address).
		WithName("TestDevice").
		WithRSSI(-50).
		WithConnectable(true).
		WithManufacturerData([]byte{}).
		WithNoServiceData().
		WithServices().
		WithTxPower(0).
		Build()
}