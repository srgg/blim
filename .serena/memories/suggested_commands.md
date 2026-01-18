# Essential Development Commands

## Build Commands
```bash
make build           # Build the CLI application with LuaJIT
make clean          # Clean build artifacts
```

## Testing Commands
```bash
make test           # Run all device_test
make test TEST=<name>    # Run specific test by name
make test-race      # Run device_test with race detection
make test-coverage  # Generate coverage report (HTML + summary)
make coverage       # Show coverage summary only
```

## Package-specific Testing
```bash
make test-device    # Test device package (100% coverage)
make test-ble       # Test BLE scanner package (62% coverage)
make test-config    # Test configuration package (100% coverage)
make test-cli       # Test CLI commands (21.6% coverage)
```

## Code Quality
```bash
make fmt            # Format code (go fmt)
make lint           # Run linter (golangci-lint or go vet fallback)
make security       # Run security checks (gosec)
make check          # Full quality check (fmt + lint + test-race + coverage + security)
```

## Benchmarking
```bash
make bench          # Run all benchmarks
make bench-cpu      # Run benchmarks with CPU profiling
make bench-mem      # Run benchmarks with memory profiling
make bench-device   # Device package benchmarks only
make bench-ble      # BLE package benchmarks only
```

## Development Setup
```bash
make install-tools  # Install development tools (golangci-lint, gosec)
make tidy          # Tidy dependencies
make verify        # Verify dependencies
```

## Application Usage
```bash
./blim scan                        # Scan for BLE devices
./blim connect <device-id>         # Connect to device (not implemented)
./blim notify <device-id> <char>   # Subscribe to notifications (not implemented)
./blim write <device-id> <char> <data>  # Write to characteristic (not implemented)
./blim bridge <device-id> <char>   # Create PTY bridge (not implemented)
```

## System Commands (macOS)
```bash
ls                  # List directory contents
find . -name "*.go" # Find Go files
grep -r "pattern"   # Search for patterns (prefer ripgrep/rg if available)
git status          # Git repository status
git log --oneline   # Compact git history
```

## Coverage Viewing
```bash
make test-coverage  # Generate coverage report
open coverage/coverage.html  # View coverage in browser (macOS)
```