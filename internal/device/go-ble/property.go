package goble

import (
	"github.com/go-ble/ble"
	"github.com/srgg/blim/internal/device"
)

// BLEProperty represents a single BLE characteristic property with its bit flag value and human-readable name.
// It implements the Property interface.
type BLEProperty struct {
	value ble.Property
	name  string
}

// Value returns the bit flag value of the property.
func (p *BLEProperty) Value() int {
	return int(p.value)
}

// KnownName returns the human-readable name of the property.
func (p *BLEProperty) KnownName() string {
	return p.name
}

// BLEProperties represents a collection of BLE characteristic properties.
// It implements the Properties interface and provides structured access to individual properties.
type BLEProperties struct {
	broadcast                 *BLEProperty
	read                      *BLEProperty
	writeWithoutResponse      *BLEProperty
	write                     *BLEProperty
	notify                    *BLEProperty
	indicate                  *BLEProperty
	authenticatedSignedWrites *BLEProperty
	extendedProperties        *BLEProperty
}

// NewProperties creates a Properties instance from ble.Property bit flags.
func NewProperties(p ble.Property) device.Properties {
	props := &BLEProperties{}

	if p&ble.CharBroadcast != 0 {
		props.broadcast = &BLEProperty{value: ble.CharBroadcast, name: "Broadcast"}
	}
	if p&ble.CharRead != 0 {
		props.read = &BLEProperty{value: ble.CharRead, name: "Read"}
	}
	if p&ble.CharWriteNR != 0 {
		props.writeWithoutResponse = &BLEProperty{value: ble.CharWriteNR, name: "WriteWithoutResponse"}
	}
	if p&ble.CharWrite != 0 {
		props.write = &BLEProperty{value: ble.CharWrite, name: "Write"}
	}
	if p&ble.CharNotify != 0 {
		props.notify = &BLEProperty{value: ble.CharNotify, name: "Notify"}
	}
	if p&ble.CharIndicate != 0 {
		props.indicate = &BLEProperty{value: ble.CharIndicate, name: "Indicate"}
	}
	if p&ble.CharSignedWrite != 0 {
		props.authenticatedSignedWrites = &BLEProperty{value: ble.CharSignedWrite, name: "AuthenticatedSignedWrites"}
	}
	if p&ble.CharExtended != 0 {
		props.extendedProperties = &BLEProperty{value: ble.CharExtended, name: "ExtendedProperties"}
	}

	return props
}

// Broadcast returns the Broadcast property if present, nil otherwise.
func (p *BLEProperties) Broadcast() device.Property {
	if p.broadcast == nil {
		return nil
	}
	return p.broadcast
}

// Read returns the Read property if present, nil otherwise.
func (p *BLEProperties) Read() device.Property {
	if p.read == nil {
		return nil
	}
	return p.read
}

// Write returns the Write property if present, nil otherwise.
func (p *BLEProperties) Write() device.Property {
	if p.write == nil {
		return nil
	}
	return p.write
}

// WriteWithoutResponse returns the WriteWithoutResponse property if present, nil otherwise.
func (p *BLEProperties) WriteWithoutResponse() device.Property {
	if p.writeWithoutResponse == nil {
		return nil
	}
	return p.writeWithoutResponse
}

// Notify returns the Notify property if present, nil otherwise.
func (p *BLEProperties) Notify() device.Property {
	if p.notify == nil {
		return nil
	}
	return p.notify
}

// Indicate returns the Indicate property if present, nil otherwise.
func (p *BLEProperties) Indicate() device.Property {
	if p.indicate == nil {
		return nil
	}
	return p.indicate
}

// AuthenticatedSignedWrites returns the AuthenticatedSignedWrites property if present, nil otherwise.
func (p *BLEProperties) AuthenticatedSignedWrites() device.Property {
	if p.authenticatedSignedWrites == nil {
		return nil
	}
	return p.authenticatedSignedWrites
}

// ExtendedProperties returns the ExtendedProperties property if present, nil otherwise.
func (p *BLEProperties) ExtendedProperties() device.Property {
	if p.extendedProperties == nil {
		return nil
	}
	return p.extendedProperties
}
