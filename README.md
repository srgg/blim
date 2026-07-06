# Blim

**Blim** is a lightweight, reusable Go library and CLI tool for working with Bluetooth Low Energy (BLE) devices.
It provides high-level building blocks for scanning, bridging, and inspecting BLE devices — all in a clean, developer-friendly API.

## Key Features

- **Scan** for BLE devices quickly and reliably.
- **Bridge** BLE devices to serial, TCP, or other transport layers.
- **Inspect** BLE device data and characteristics programmatically.
- **Read/Write** BLE characteristic values from the command line.
- **CLI + Library**: Use `blim` from the command line, or import its packages into your Go projects.
- **Testable & Reusable**: Each core component (`scanner`, `bridge`, `inspector`) is a standalone package.
- **Lua Integration**: Extend functionality with Lua scripts for custom device interactions.

## Installation

### Install via Homebrew (macOS, Apple Silicon)

```bash
brew install srgg/blim/blim
```

Or explicitly tap first:
```bash
brew tap srgg/blim
brew install blim
```

### Install via Go

```bash
go install github.com/srgg/blim/cmd/blim@latest
```

### Install as Library

```bash
go get github.com/srgg/blim
```

## Requirements

- **Go:** 1.24 or later
- **Platform:** macOS (tested)
- **Bluetooth:** BLE adapter required

## Usage

### Scan for BLE Devices

Discover nearby BLE devices:

```bash
blim scan --timeout 10s
```

Example output:
```
EF-R3P42406           4839279b49adcbe13f1fb430ab325aba  -78 dBm             3s ago
BLIM IMU Stream       e20e664a4716aba3abc6b9a0329b5b2e  -50 dBm  180a,ff10  3s ago
```

### Inspect a BLE Device

View device services, characteristics, and descriptors:

```bash
blim inspect e20e664a-4716-aba3-abc6-b9a0329b5b2e  --json
```

Use `--json` for structured output.

### Read Characteristic Value

Read a BLE characteristic value:

```bash
blim read e20e664a-4716-aba3-abc6-b9a0329b5b2e 0xff21 
```

### Write Characteristic Value

Write data to a BLE characteristic:

```bash
blim write e20e664a-4716-aba3-abc6-b9a0329b5b2e 0xff21 '{"settings": {"apply_calibration":true}}' 
```

### Bridge BLE to Serial/PTY

Bridge a BLE device to a pseudo-terminal or serial port using Lua scripts:

```bash
blim bridge e20e664a-4716-aba3-abc6-b9a0329b5b2e --script examples/motioncal-bridge.lua --symlin /tmp/motioncal.serial
```

See the [examples/](examples/) directory for Lua bridge scripts including:
- `bridge.lua` - Basic BLE-to-PTY bridge
- `motioncal-bridge.lua` - IMU data bridging for motion calibration
- `inspect.lua` - Device inspection script

## Library Usage

Use Blim as a library in your Go projects:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/sirupsen/logrus"
    "github.com/srgg/blim/scanner"
)

func main() {
    // Create a scanner
    s, err := scanner.NewScanner(logrus.New())
    if err != nil {
        panic(err)
    }

    // Scan for 10 seconds
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    opts := scanner.DefaultScanOptions()
    devices, err := s.Scan(ctx, opts, func(phase string) {
        fmt.Println("scan:", phase)
    })
    if err != nil {
        panic(err)
    }

    // Scan returns a map keyed by device address.
    fmt.Printf("Discovered %d devices\n", len(devices))
    for addr, entry := range devices {
        fmt.Printf("  %s  %q  %d dBm\n", addr, entry.Device.Name(), entry.Device.RSSI())
    }
}
```

## Building from Source

Clone the repository and build:

```bash
git clone https://github.com/srgg/blim.git
cd blim
make build
```

The binary will be available at `./blim`.

## Testing

Run the test suite:

```bash
# go clean -testcache
make test-race 2>&1 | grep -E "^(FAIL\t|ok\t|--- FAIL)" | head -30
```

Run tests with coverage:

```bash
make test-coverage
```

## Project Structure

```
blim/
├── cmd/blim/          # CLI application (scan, inspect, read, write, bridge)
├── scanner/           # BLE device scanning library (importable package)
├── bridge/            # BLE bridging library (importable package)
├── inspector/         # BLE device inspection library (importable package)
├── internal/
│   ├── device/        # BLE device abstraction layer
│   ├── lua/           # Lua integration and scripting engine
│   ├── devicefactory/ # Device factory for dependency injection
│   └── testutils/     # Testing utilities and mock builders
├── examples/          # Example Lua scripts for bridging and inspection
└── README.md
```

## Lua Script Examples

See the [examples/](examples/) directory for:
- **bridge.lua** - Basic BLE-to-serial bridging
- **inspect.lua** - Device inspection and discovery
- **motioncal-bridge.lua** - IMU sensor data bridging

### Lua API notes

**Full API reference:** [internal/lua/README.md](internal/lua/README.md) documents
every `blim.*` function (device info, subscriptions, PTY bridge, `blim.term`,
`blim.pcall`, binary helpers) with examples.

The scripting sandbox runs on LuaJIT with a few deliberate differences from
stock Lua that script authors should know:

- **`blim.pcall(f, ...)`** — use this instead of the standard `pcall`, which is
  removed from the sandbox. It catches errors raised by Lua code and C built-ins
  (`error()`, `ffi.cdef`, ...). **Limitation:** errors raised by Go-backed API
  functions (`require`, `blim.subscribe`, `blim.characteristic`, ...) are *not*
  catchable and abort the script by design — the sandbox cannot safely unwind a
  Lua-level catch across Go call frames. Handle those via the `nil, err` return
  values that the Go-backed functions already provide, not `blim.pcall`.

- **`blim.term`** — terminal helpers for interactive bridge scripts (raw mode +
  single-keypress input):
  - `blim.term.enable_raw()` → `true` | `nil, err` (idempotent; fails when stdin
    is not a TTY)
  - `blim.term.disable_raw()` — restore the terminal (idempotent)
  - `blim.term.read_char(wait_ms?)` → `char` | `nil` | `nil, msg, code`
    (io.read semantics). With no argument it is non-blocking; with `wait_ms` it
    waits up to that long, yielding via `blim.sleep` so BLE callbacks keep
    flowing. A bare `nil` means "no key yet"; a `nil, msg, code` triple is a
    terminal condition — `code == blim.term.EOF` means stdin was closed
    (interactive loops must exit), any other `code` is the read `errno`.

  See [examples/vehicle-control-bridge.lua](examples/vehicle-control-bridge.lua)
  for a full interactive control panel using these.

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Releasing

Releases are fully automated via [GitHub Actions](.github/workflows/release.yaml). To cut a release, push a version tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The workflow then:

1. Builds `blim` for macOS arm64 with statically linked LuaJIT (verifies no dynamic LuaJIT dependency via `otool`)
2. Creates a GitHub Release with the `blim_darwin_arm64.tar.gz` archive, checksums, and auto-generated release notes
3. Updates the Homebrew formula in the [srgg/homebrew-blim](https://github.com/srgg/homebrew-blim) tap (requires the `HOMEBREW_TAP_TOKEN` repository secret)

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with:
- [go-ble/ble](https://github.com/go-ble/ble) - Cross-platform BLE library (built against the maintained [srgg/go-ble](https://github.com/srgg/go-ble) fork, which adds a macOS mid-scan Bluetooth-state fix absent from the dormant upstream)
- [golua](https://github.com/aarzilli/golua) - Go bindings for Lua, statically linked against LuaJIT
- [testify](https://github.com/stretchr/testify) - Testing toolkit