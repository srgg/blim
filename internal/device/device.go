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

	// ErrConnectionLost indicates the BLE connection was unexpectedly lost during operation.
	// This is distinct from device.ErrNotConnected, which indicates an attempt to use
	// a device that was never connected or was already disconnected.
	ErrConnectionLost = errors.New("connection lost")
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
	Subscribe(opts []*SubscribeOptions, pattern StreamMode, maxRate time.Duration, drainDuration time.Duration, callback func(*Record)) (cancel func(), err error)
	SubscribeWithName(name string, opts []*SubscribeOptions, pattern StreamMode, maxRate time.Duration, drainDuration time.Duration, callback func(*Record)) (cancel func(), err error)
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

type RecordMeta struct {
	TsUs  int64
	Seq   uint64
	Flags uint32
}

type Record struct {
	RecordMeta

	Values      map[string][]byte        // Single value per characteristic (EveryUpdate/Aggregated modes)
	BatchValues map[string][][]byte      // Multiple values per characteristic (Batched mode)
	Meta        map[string][]*RecordMeta // Metadata per each characteristic (Batched and Aggregate modes)
}

func cloneSlice[T any](src []T, copyFn func(T) T) []T {
	dst := make([]T, len(src))
	for i, v := range src {
		dst[i] = copyFn(v)
	}
	return dst
}

// Clone returns a standalone deep copy of the Record, not from any pool.
// The returned Record is fully owned by the caller and safe to store indefinitely.
// Use when keeping records beyond the subscription callback, as the original
// Record's data references pooled memory that is reused after callback returns.
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}

	// ---- local helpers (fully inlined by compiler) ----

	cloneBytes := func(b []byte) []byte {
		return append([]byte(nil), b...)
	}

	cloneRecordMeta := func(m *RecordMeta) *RecordMeta {
		c := *m
		return &c
	}

	// ---- clone record ----

	clone := &Record{
		RecordMeta: r.RecordMeta,
	}

	if len(r.Values) != 0 {
		clone.Values = make(map[string][]byte, len(r.Values))
		for k, v := range r.Values {
			clone.Values[k] = cloneBytes(v)
		}
	}

	if len(r.BatchValues) != 0 {
		clone.BatchValues = make(map[string][][]byte, len(r.BatchValues))
		for k, batches := range r.BatchValues {
			clone.BatchValues[k] = cloneSlice(batches, cloneBytes)
		}
	}

	if len(r.Meta) != 0 {
		clone.Meta = make(map[string][]*RecordMeta, len(r.Meta))
		for k, metas := range r.Meta {
			clone.Meta[k] = cloneSlice(metas, cloneRecordMeta)
		}
	}

	return clone
}
