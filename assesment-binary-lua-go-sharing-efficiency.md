# Binary Data Sharing Efficiency Assessment: Go ↔ Lua (golua/LuaJIT)

## Executive Summary

The current implementation passes binary data between Go and Lua using string conversion (`string([]byte)` and `[]byte(string)`), which introduces unnecessary memory allocations and GC pressure. For high-frequency BLE data streams (100+ notifications/sec), this creates measurable performance overhead.

---

## Current Implementation Analysis

### What's Wrong

1. **Double/Triple Allocation on Push**
   - Current: `L.PushString(string(data))` performs:
     1. Go allocation: `string([]byte)` copies bytes to create immutable string
     2. CGO allocation: `C.CString()` allocates C memory and copies string
     3. Lua allocation: `lua_pushlstring()` copies into Lua heap
   - Each BLE notification triggers 3 memory allocations for a single value

2. **Double Allocation on Read**
   - Current: `[]byte(L.ToString(1))` performs:
     1. CGO allocation: `C.GoStringN()` creates Go string from Lua data
     2. Go allocation: `[]byte(string)` copies to mutable byte slice
   - Each characteristic write triggers 2 allocations

3. **GC Pressure**
   - Short-lived allocations increase GC frequency
   - For 200 notifications/sec with 20-byte payloads: ~1000 allocations/sec just for data transfer
   - GC pauses can cause BLE notification lag and buffer overruns

4. **CGO Overhead**
   - Each boundary crossing has fixed CGO call overhead (~100ns)
   - String conversion adds type checking overhead in Go runtime

### Why It Matters

- BLE subscriptions can generate 100-1000 notifications/second per characteristic
- Multi-characteristic subscriptions multiply this load
- Real-time applications (motion sensors, audio streams) are latency-sensitive
- The existing comment at `api.go:924-925` acknowledges: *"Using byte arrays (60x more operations) kills performance"*

---

## Boundary Crossing Function Inventory

### Go → Lua (Push Operations)

| Location | Function | Line | Operation | Data Type |
|----------|----------|------|-----------|-----------|
| `api.go` | `callPTYDataCallback` | 820 | `L.PushString(string(data))` | PTY read data |
| `api.go` | `callLuaCallback` | 926 | `L.PushString(string(data))` | BLE notification Values |
| `api.go` | `callLuaCallback` | 944 | `L.PushString(string(data))` | BLE notification BatchValues |
| `api.go` | `registerCharacteristicFunction.read` | 1103 | `L.PushString(string(value))` | Characteristic read result |
| `api.go` | `registerBridgeInfo.pty_read` | 248 | `L.PushString(string(buf[:n]))` | PTY read buffer |

### Lua → Go (Read Operations)

| Location | Function | Line | Operation | Data Type |
|----------|----------|------|-----------|-----------|
| `api.go` | `registerBridgeInfo.pty_write` | 179 | `L.ToString(1)` | PTY write data |
| `api.go` | `registerBridgeInfo.pty_write` | 185 | `[]byte(data)` | Conversion for ptyIO.Write |
| `api.go` | `registerCharacteristicFunction.write` | 1122 | `[]byte(L.ToString(1))` | Characteristic write data |
| `api.go` | `registerCharacteristicFunction.parse` | 1164 | `[]byte(L.ToString(2))` | Value to parse |

---

## Top 3 Best Options

### Option 1: Use PushBytes/ToBytes Directly (Recommended)

**Description**: Replace `L.PushString(string(data))` with `L.PushBytes(data)` and `[]byte(L.ToString())` with `L.ToBytes()`.

**Implementation**:
```go
// Before (current)
L.PushString(string(data))
value := []byte(L.ToString(1))

// After (optimized)
L.PushBytes(data)
value := L.ToBytes(1)
```

**Pros**:
- Eliminates one allocation per push operation (no `string([]byte)` conversion)
- Eliminates one allocation per read operation (no `[]byte(string)` conversion)
- Zero API change for Lua scripts (Lua strings are binary-safe)
- Minimal code changes (~10 lines)
- golua already provides these methods

**Cons**:
- Still requires CGO crossing and Lua internal copy (unavoidable with standard API)
- No zero-copy semantics
- Minor: `PushBytes` panics on empty slice (needs nil check)

**Estimated Improvement**: ~30-40% reduction in allocations for data transfer operations.

**Drawbacks**:
- None significant; this is a drop-in improvement

---

### Option 2: LuaJIT FFI Shared Memory Buffer

**Description**: Use LuaJIT FFI to create a shared memory region accessible from both Go and Lua without copying.

**Implementation**:
```go
// Go side: expose buffer pointer via cgo
//export GetSharedBuffer
func GetSharedBuffer() unsafe.Pointer { return unsafe.Pointer(&sharedBuf[0]) }
```
```lua
-- Lua side: access via FFI
local ffi = require("ffi")
ffi.cdef[[ void* GetSharedBuffer(); ]]
local buf = ffi.cast("uint8_t*", ffi.C.GetSharedBuffer())
```

**Pros**:
- True zero-copy for read operations on Lua side
- Eliminates all allocations for reading data
- Can batch multiple values in single buffer
- Already have partial infrastructure (`ffi_buffer.lua`)

**Cons**:
- Requires careful synchronization (Go may write while Lua reads)
- LuaJIT-specific (won't work with standard Lua 5.1)
- Complex lifecycle management (who owns the buffer?)
- FFI has its own overhead for small operations
- Requires protocol for buffer layout (offsets, lengths)

**Estimated Improvement**: ~70-80% reduction in allocations for subscription callbacks.

**Drawbacks**:
- Significant complexity increase
- Potential for memory corruption bugs
- Debugging is harder (crosses language boundaries)
- Not portable to standard Lua implementations

---

### Option 3: Userdata with Buffer Pool

**Description**: Push binary data as Lua userdata backed by a Go-side buffer pool, with lazy copy-on-access semantics.

**Implementation**:
```go
type BinaryBuffer struct {
    data []byte
    pool *sync.Pool
}

func (api *LuaAPI) pushBinaryUserdata(L *lua.State, data []byte) {
    buf := api.bufPool.Get().(*BinaryBuffer)
    buf.data = buf.data[:0]
    buf.data = append(buf.data, data...)
    L.PushGoStruct(buf)
}
```

**Pros**:
- Buffer reuse reduces allocation frequency
- Userdata can have methods (`:byte(i)`, `:tostring()`, `:len()`)
- Go maintains ownership and can pool buffers
- Works with any Lua version

**Cons**:
- Lua scripts must change to use userdata API instead of string
- Breaking API change for existing scripts
- Userdata access slower than string indexing in Lua
- Complex metatable setup for userdata methods
- Pool management overhead for small buffers

**Estimated Improvement**: ~50-60% reduction in allocations with buffer reuse.

**Drawbacks**:
- API breaking change requiring script modifications
- More complex implementation
- Potential memory leaks if buffers not returned to pool

---

## Recommendation

**Start with Option 1** (`PushBytes`/`ToBytes`) immediately. It's a low-risk, high-reward optimization that:
- Requires minimal code changes
- Has no API impact on Lua scripts
- Provides measurable improvement (~30-40% fewer allocations)
- Can be implemented and tested in under an hour

**Consider Option 2** (FFI Shared Memory) only if profiling shows Option 1 is insufficient for specific high-throughput use cases (e.g., audio streaming, high-frequency sensor fusion). The complexity cost is only justified for extreme performance requirements.

**Avoid Option 3** unless you're planning a major API revision, as the breaking changes outweigh the benefits compared to Option 1.

---

## Implementation Checklist for Option 1

1. [ ] Replace `L.PushString(string(data))` with `L.PushBytes(data)` at all 5 Go→Lua locations
2. [ ] Add nil/empty slice guard before `PushBytes` calls (panics on empty slice)
3. [ ] Replace `[]byte(L.ToString(n))` with `L.ToBytes(n)` at all 4 Lua→Go locations
4. [ ] Verify `L.ToString()` still works for Lua scripts using `tostring()` on values
5. [ ] Run existing test suite
6. [ ] Benchmark subscription callback throughput before/after

---

## Appendix: golua Method Signatures

```go
// Current inefficient path
func (L *State) PushString(str string)  // calls C.CString() - extra allocation
func (L *State) ToString(index int) string  // calls C.GoStringN()

// Optimal path for binary data
func (L *State) PushBytes(b []byte)  // direct pointer, no C.CString()
func (L *State) ToBytes(index int) []byte  // calls C.GoBytes()
```

Both pairs internally call the same Lua C API (`lua_pushlstring`/`lua_tolstring`), but the `Bytes` variants avoid Go string ↔ []byte conversion overhead.
