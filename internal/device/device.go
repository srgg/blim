package device

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NotFoundError represents an error when a BLE resource is not found
type NotFoundError struct {
	Resource string   // "service", "characteristic", "descriptor"
	UUIDs    []string // One or more UUIDs (e.g., [serviceUUID] or [serviceUUID, charUUID])
}

func (e *NotFoundError) Error() string {
	if len(e.UUIDs) == 0 {
		return fmt.Sprintf("%s not found", e.Resource)
	}
	if len(e.UUIDs) == 1 {
		return fmt.Sprintf("%s %q not found", e.Resource, e.UUIDs[0])
	}
	// Multiple UUIDs (e.g., characteristic in service, descriptor in characteristic)
	// For BLE hierarchy: characteristic is in service, descriptor is in characteristic
	parentResource := "service"
	if e.Resource == "descriptor" {
		parentResource = "characteristic"
	}
	return fmt.Sprintf("%s %q not found in %s %q", e.Resource, e.UUIDs[len(e.UUIDs)-1], parentResource, e.UUIDs[0])
}

// ConnectionState represents the specific kind of connection state failure
type ConnectionState string

const (
	NotConnected     ConnectionState = "not_connected"
	AlreadyConnected ConnectionState = "already_connected"
	NotInitialized   ConnectionState = "not_initialized"
)

// ConnectionError represents any connection-related problem
type ConnectionError struct {
	State ConnectionState
	Msg   string
}

// Error implements the error interface
func (e *ConnectionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Msg == "" {
		return string(e.State)
	}
	return fmt.Sprintf("%s: %s", e.State, e.Msg)
}

// Is allows errors.Is to compare ConnectionError values by State
func (e *ConnectionError) Is(target error) bool {
	if e == nil {
		return false
	}
	t, ok := target.(*ConnectionError)
	if !ok {
		return false
	}
	return e.State == t.State
}

// Predefined sentinel errors for connection states
var (
	ErrNotConnected     = &ConnectionError{State: NotConnected}
	ErrAlreadyConnected = &ConnectionError{State: AlreadyConnected}
	ErrNotInitialized   = &ConnectionError{State: NotInitialized}
)

// Operation errors
var (
	ErrTimeout      = errors.New("timeout")
	ErrUnsupported  = errors.New("unsupported")
	ErrBluetoothOff = errors.New("bluetooth is turned off")
)

// IsConnectionState reports whether err is a ConnectionError with the given state
func IsConnectionState(err error, state ConnectionState) bool {
	var cerr *ConnectionError
	if errors.As(err, &cerr) {
		return cerr.State == state
	}
	return false
}

// Scanner represents a BLE device capable of scanning for advertisements
type Scanner interface {
	Scan(ctx context.Context, allowDup bool, handler func(Advertisement)) error
}

type Advertisement interface {
	LocalName() string
	ManufacturerData() []byte
	ServiceData() []struct {
		UUID string
		Data []byte
	}

	Services() []string
	OverflowService() []string
	TxPowerLevel() int
	Connectable() bool
	SolicitedService() []string

	RSSI() int
	Addr() string
}

//nolint:revive // DeviceInfo name is intentional for clarity when used as a device.DeviceInfo
type DeviceInfo interface {
	ID() string
	Name() string
	Address() string
	RSSI() int
	TxPower() *int
	IsConnectable() bool
	AdvertisedServices() []string
	ManufacturerData() []byte
	ParsedManufacturerData() interface{} // Parsed manufacturer data (e.g., *BlimManufacturerData) or nil
	ServiceData() map[string][]byte
}

// Device defines the interface for all device types
type Device interface {
	DeviceInfo

	Connect(ctx context.Context, opts *ConnectOptions) error
	Disconnect() error
	IsConnected() bool
	Update(adv Advertisement)
	GetConnection() Connection
}

// Connection represents a BLE connection interface
type Connection interface {
	Services() []Service
	GetService(uuid string) (Service, error)
	GetCharacteristic(service, uuid string) (Characteristic, error)
	Subscribe(opts []*SubscribeOptions, pattern StreamMode, maxRate time.Duration, callback func(*Record)) (cancel func(), err error)
	SubscribeWithName(name string, opts []*SubscribeOptions, pattern StreamMode, maxRate time.Duration, callback func(*Record)) (cancel func(), err error)
	ConnectionContext() context.Context // Returns context that's cancelled when connection errors occur
}

// Service represents a GATT service interface
type Service interface {
	UUID() string
	KnownName() string
	GetCharacteristics() []Characteristic
}

// CharacteristicInfo represents characteristic metadata
type CharacteristicInfo interface {
	UUID() string
	KnownName() string
	GetProperties() Properties
	GetDescriptors() []Descriptor
	RequiresAuthentication() bool // Returns true if characteristic requires pairing/authentication
}

// DescriptorInfo represents descriptor metadata
type DescriptorInfo interface {
	UUID() string
	Handle() uint16
	Index() uint8
	KnownName() string
	Value() []byte            // Returns raw descriptor value bytes, nil if read failed or skipped
	ParsedValue() interface{} // Returns parsed value, *DescriptorError if read failed, nil if skipped
}

// CharacteristicReader provides read operations
type CharacteristicReader interface {
	Read(timeout time.Duration) ([]byte, error)
}

// CharacteristicWriter provides write operations
type CharacteristicWriter interface {
	Write(data []byte, withResponse bool, timeout time.Duration) error
}

// DescriptorReader provides on-demand read operations for descriptors
type DescriptorReader interface {
	Read(timeout time.Duration) ([]byte, error)
}

// Characteristic combines info + operations
type Characteristic interface {
	CharacteristicInfo
	CharacteristicReader
	CharacteristicWriter

	HasParser() bool                              // Returns true if a parser is registered for this characteristic type
	ParseValue(value []byte) (interface{}, error) // Parses value using registered parser
}

// Descriptor combines descriptor information with read operations
type Descriptor interface {
	DescriptorInfo
	DescriptorReader
}

// Property represents a single BLE characteristic property
type Property interface {
	Value() int
	KnownName() string
}

// Properties represent a collection of BLE characteristic properties
type Properties interface {
	Broadcast() Property
	Read() Property
	Write() Property
	WriteWithoutResponse() Property
	Notify() Property
	Indicate() Property
	AuthenticatedSignedWrites() Property
	ExtendedProperties() Property
}

// SubscribeOptions defined BLE Characteristics subscriptions
type SubscribeOptions struct {
	Service         string
	Characteristics []string // can be empty
	Indicate        bool     // true = Indicate, false = Notify (default)
}

// ConnectOptions defines BLE connection options
type ConnectOptions struct {
	Address               string
	ConnectTimeout        time.Duration
	DescriptorReadTimeout time.Duration // Timeout for reading descriptor values (0 = skip reads)
	Services              []SubscribeOptions
}

// StreamMode defines how subscription data is delivered
type StreamMode int

const (
	StreamEveryUpdate StreamMode = iota
	StreamBatched
	StreamAggregated
)

// Record represents a subscription notification record
type Record struct {
	TsUs        int64
	Seq         uint64
	Values      map[string][]byte   // Single value per characteristic (EveryUpdate/Aggregated modes)
	BatchValues map[string][][]byte // Multiple values per characteristic (Batched mode)
	Flags       uint32
}

// Clone returns a standalone deep copy of the Record, not from any pool.
// The returned Record is fully owned by the caller and safe to store indefinitely.
// Use when keeping records beyond the subscription callback, as the original
// Record's data references pooled memory that is reused after callback returns.
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	clone := &Record{
		TsUs:  r.TsUs,
		Seq:   r.Seq,
		Flags: r.Flags,
	}
	if r.Values != nil {
		clone.Values = make(map[string][]byte, len(r.Values))
		for k, v := range r.Values {
			clone.Values[k] = append([]byte(nil), v...)
		}
	}
	if r.BatchValues != nil {
		clone.BatchValues = make(map[string][][]byte, len(r.BatchValues))
		for k, batches := range r.BatchValues {
			clonedBatches := make([][]byte, len(batches))
			for i, b := range batches {
				clonedBatches[i] = append([]byte(nil), b...)
			}
			clone.BatchValues[k] = clonedBatches
		}
	}
	return clone
}
