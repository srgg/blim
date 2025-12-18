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
go install github.com/srg/blim/cmd/blim@latest
```

### Install as Library

```bash
go get github.com/srg/blim
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

    "github.com/srg/blim/scanner"
)

func main() {
    // Create a scanner
    s := scanner.NewScanner()

    // Scan for 10 seconds
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    devices, err := s.Scan(ctx, false, func(adv scanner.Advertisement) {
        fmt.Printf("Found: %s (%s)\n", adv.LocalName(), adv.Addr())
    })

    if err != nil {
        panic(err)
    }

    fmt.Printf("Discovered %d devices\n", len(devices))
}
```

## Building from Source

Clone the repository and build:

```bash
git clone https://github.com/srg/blim.git
cd blim
make build
```

The binary will be available at `./blim`.

## Testing

Run the test suite:

```bash
make test
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
├── docs/              # Documentation
└── README.md
```

## Lua Script Examples

See the [examples/](examples/) directory for:
- **bridge.lua** - Basic BLE-to-serial bridging
- **inspect.lua** - Device inspection and discovery
- **motioncal-bridge.lua** - IMU sensor data bridging

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with:
- [go-ble/ble](https://github.com/go-ble/ble) - Cross-platform BLE library
- [gopher-lua](https://github.com/yuin/gopher-lua) - Lua VM in Go
- [testify](https://github.com/stretchr/testify) - Testing toolkit