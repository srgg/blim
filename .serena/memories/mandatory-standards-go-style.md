# MANDATORY: Go Style Standards

## Naming Conventions

**MUST follow standard Go conventions:**

- **Packages:** lowercase, single word (`ble`, `device`, `config`)
- **Structs:** PascalCase (`Device`, `Scanner`, `ScanOptions`)
- **Methods:** PascalCase (exported), camelCase (unexported)
- **Variables:** camelCase (`deviceFactory`, `scanOptions`)
- **Constants:** PascalCase (exported), camelCase (unexported)

## Select Clause Ordering Rule
MANDATORY: Order select cases by execution frequency: HOTTEST → COLDEST:
- HOT      → Main data/work channel (checked every loop iteration)
- WARM     → Tickers, timeouts (checked periodically)
- COLD     → Context cancellation (rare)
- COLDEST  → Shutdown, one-time signals


## Struct Patterns

**MUST use clean struct design with appropriate tags:**

```go
type Device struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Address     string            `json:"address"`
    RSSI        int               `json:"rssi"`
    Services    []Service         `json:"services,omitempty"`
    ManufData   []byte            `json:"manufacturer_data,omitempty"`
    ServiceData map[string][]byte `json:"service_data,omitempty"`
    TxPower     *int              `json:"tx_power,omitempty"`
    Connectable bool              `json:"connectable"`
    LastSeen    time.Time         `json:"last_seen"`
}
```

**Requirements:**
- Use `omitempty` for optional fields
- Use pointers (`*int`) for nullable fields
- Use `time.Time` for timestamps
- Include JSON tags for serialization

## Method Patterns

**MUST follow Go idioms:**

```go
// Constructor pattern
func NewDevice(id string) *Device { ... }

// Pointer receivers for mutation
func (d *Device) Connect() error { ... }

// Value receivers for read-only
func (d Device) IsConnectable() bool { ... }

// Interface satisfaction (implicit)
type Scanner interface {
    Scan(duration time.Duration) error
}
```

## Error Handling

**CRITICAL:** Follow Go error patterns without exception.

**MUST:**
- Return errors, NEVER panic in normal operation
- Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`
- Check errors immediately after function calls
- Use sentinel errors for expected conditions

**NEVER:**
- Ignore errors with `_` without justification
- Use panic for recoverable errors
- Return nil error with nil value

```go
// ✅ CORRECT
result, err := operation()
if err != nil {
    return fmt.Errorf("failed to process: %w", err)
}

// ❌ WRONG
result, _ := operation()  // Ignored error
```

## Testing Standards

**MUST follow Go testing conventions:**

### File Organization
- Test files: `*_test.go` suffix
- Place in same package: `package ble` not `package ble_test` (unless testing external API)

### Test Patterns
```go
// Test function
func TestDeviceConnect(t *testing.T) { ... }

// Benchmark function
func BenchmarkScan(b *testing.B) { ... }

// Table-driven device_test
func TestDeviceValidation(t *testing.T) {
    tests := []struct {
        name    string
        input   Device
        wantErr bool
    }{
        // test cases
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test logic
        })
    }
}
```

### Mocking
- Use `testify/mock` for mocks
- Create mock interfaces in test files
- Use `t.Cleanup()` for resource cleanup

## Package Organization

**MUST structure packages correctly:**

```
project/
├── internal/           # Private implementation (not importable)
│   ├── device/
│   └── parser/
├── ble/               # Public API
├── config/            # Configuration
└── cmd/               # Executables
```

**Rules:**
- Use `internal/` for private packages
- Define interfaces in consumer packages
- Keep package scope small and focused

## Logging

**MUST use structured logging:**

```go
import "github.com/sirupsen/logrus"

logger.WithFields(logrus.Fields{
    "device_id": deviceID,
    "operation": "connect",
}).Info("connecting to device")
```

**Levels:**
- `Error`: Operation failures
- `Warn`: Recoverable issues
- `Info`: Important state changes
- `Debug`: Detailed diagnostic info

## Build Configuration

**Project-specific requirements:**

```bash
# CGO with LuaJIT
CGO_ENABLED=1 go build -tags luajit

# Build tags for Lua integration
//go:build luajit
```

**MUST:**
- Enable CGO for LuaJIT performance
- Use appropriate build tags
- Test on target platform (macOS focus)

## Code Quality

**MUST maintain:**
- `gofmt` formatting (automatic)
- `golint` compliance
- No naked returns in long functions
- Meaningful variable names (no single letters except loops/short scopes)
- Comments for exported functions/types

## Enforcement

These standards are **NON-NEGOTIABLE**. ALL Go code MUST comply.