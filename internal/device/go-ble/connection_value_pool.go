package goble

import (
	"sync"
	"sync/atomic"
	"time"
)

// ----------------------------
// Flags
// ----------------------------
const (
	FlagDropped uint32 = 1 << iota
	FlagMissing
)

// ----------------------------
// BLEValue with Pooling
// ----------------------------

const (
	// DefaultBLEValueCapacity is the default buffer capacity for pooled BLEValue objects
	DefaultBLEValueCapacity = 256

	// MaxPooledBufferSize is the maximum buffer size to keep in the pool.
	// Buffers larger than this are replaced with default-sized buffers to prevent
	// memory bloat in the pool.
	MaxPooledBufferSize = 1024

	// DefaultReadTimeout is the default timeout for characteristic read operations.
	// This prevents indefinite blocking if a device becomes unresponsive during a read.
	DefaultReadTimeout = 5 * time.Second
)

// BLEValue represents a BLE notification value.
// IMPORTANT: BLEValue objects are pooled and reused. The Data slice is only valid
// until the value is released back to the pool. Subscribers MUST copy Data immediately
// if they need to retain it beyond the callback invocation.
type BLEValue struct {
	TsUs  int64
	Data  []byte
	Seq   uint64
	Flags uint32
	Char  *BLECharacteristic // Source characteristic for fan-out routing
}

type BLEVValuePool struct {
	// tracks BLEValue allocations/releases for leak detection in device_test.
	outstanding   atomic.Int64
	highWaterMark atomic.Int64

	seq       atomic.Int64
	valuePool *sync.Pool
}

func NewBLEVValuePool() *BLEVValuePool {
	return &BLEVValuePool{
		seq: atomic.Int64{},
		valuePool: &sync.Pool{
			New: func() interface{} {
				return &BLEValue{Data: make([]byte, 0, DefaultBLEValueCapacity)}
			},
		},
	}
}

// NewBLEValue creates a standalone BLEValue, NOT from the pool.
// The returned value is fully owned by the caller with no pool lifecycle.
// Use for device_test or cases where pool management overhead isn't needed.
func (vp *BLEVValuePool) newBLEValue(data []byte) *BLEValue {
	return &BLEValue{
		TsUs: time.Now().UnixMicro(),
		Seq:  0,
		Data: append([]byte(nil), data...),
	}
}

func (vp *BLEVValuePool) Get() *BLEValue {
	v := vp.valuePool.Get().(*BLEValue)

	v.TsUs = time.Now().UnixMicro()
	v.Seq = uint64(vp.seq.Add(1))
	v.Flags = 0

	curr := vp.outstanding.Add(1)
	// Update high water mark (CAS loop for thread safety)
	for {
		hwm := vp.highWaterMark.Load()
		if curr <= hwm || vp.highWaterMark.CompareAndSwap(hwm, curr) {
			break
		}
	}

	return v
}

func (vp *BLEVValuePool) Put(v *BLEValue) {
	// Reset fields to zero to avoid keeping stale data
	v.TsUs = 0
	v.Seq = 0
	v.Flags = 0
	v.Char = nil

	// Prevent keeping large buffers in the pool
	if cap(v.Data) > MaxPooledBufferSize {
		// Buffer too large, reallocate to the default size
		v.Data = make([]byte, 0, DefaultBLEValueCapacity)
	} else {
		// Normal size, just reset length
		v.Data = v.Data[:0]
	}

	vp.valuePool.Put(v)
	vp.outstanding.Add(-1)
}

func (vp *BLEVValuePool) Outstanding() int64 {
	return vp.outstanding.Load()
}

func (vp *BLEVValuePool) HighWaterMark() int64 {
	return vp.highWaterMark.Load()
}

func (vp *BLEVValuePool) Reset() {
	vp.outstanding.Store(0)
	vp.highWaterMark.Store(0)
}

// newPooledBLEValue creates a BLEValue from the pool.
// The returned value MUST be released via release() when done.
// Use for hot-path notification handling where pooling benefits apply.
func (vp *BLEVValuePool) newPooledBLEValue(data []byte) *BLEValue {
	v := vp.Get()

	if cap(v.Data) < len(data) {
		v.Data = make([]byte, len(data))
	}
	v.Data = v.Data[:len(data)]
	copy(v.Data, data)
	return v
}

// Clone returns a standalone deep copy of the BLEValue, NOT from the pool.
// The returned value is fully owned by the caller and safe to store indefinitely.
// Use when keeping values beyond the subscription callback, as pooled BLEValue
// objects are reused after release.
func (v *BLEValue) Clone() *BLEValue {
	if v == nil {
		return nil
	}
	return &BLEValue{
		TsUs:  v.TsUs,
		Seq:   v.Seq,
		Flags: v.Flags,
		Char:  v.Char,
		Data:  append([]byte(nil), v.Data...),
	}
}

// poolCopy returns a copy of the BLEValue from the pool.
// The returned value MUST be released via release() when done.
// Use for transient copies (e.g., fan-out broadcast) where pooling benefits apply.
func (v *BLEValue) poolCopy() *BLEValue {
	if v.Char == nil || v.Char.connection == nil {
		panic("BLEValue.poolCopy: invariant violation - Char or connection is nil (misuse of pooled value)")
	}
	cpy := v.Char.connection.valuePool.Get()
	cpy.TsUs = v.TsUs
	cpy.Seq = v.Seq
	cpy.Flags = v.Flags
	cpy.Char = v.Char
	if cap(cpy.Data) < len(v.Data) {
		cpy.Data = make([]byte, len(v.Data))
	}
	cpy.Data = cpy.Data[:len(v.Data)]
	copy(cpy.Data, v.Data)
	return cpy
}

// release returns this BLEValue to the pool for reuse.
// After calling release, the value MUST NOT be used - all fields become invalid.
// Only call on values obtained from newPooledBLEValue() or poolCopy().
// Calling release on standalone values (from NewBLEValue or Clone) is safe but wasteful.
func (v *BLEValue) release() {
	if v == nil {
		return
	}
	if v.Char == nil || v.Char.connection == nil {
		panic("BLEValue.release: invariant violation - Char or connection is nil (misuse of pooled value)")
	}
	v.Char.connection.valuePool.Put(v)
}
