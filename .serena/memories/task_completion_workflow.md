# Task Completion Workflow

## Essential Steps After Code Changes

### 1. Code Quality Checks
```bash
make fmt            # Format code first
make lint           # Run linter to catch issues
```

### 2. Testing Requirements
```bash
make test           # Run all unit device_test
make test-race      # Check for race conditions
make test-coverage  # Ensure coverage is maintained
```

### 3. Security and Dependencies
```bash
make security       # Run security analysis with gosec
make tidy           # Clean up dependencies
make verify         # Verify dependency integrity
```

### 4. Full Quality Gate
```bash
make check          # Run complete quality pipeline
# This runs: fmt + lint + test-race + test-coverage + security
```

### 5. Build Verification
```bash
make build          # Ensure clean build with LuaJIT
./blim --help     # Verify CLI functionality
```

## Coverage Requirements
- **Maintain existing coverage levels**:
  - pkg/device: 100% (critical - device modeling)
  - pkg/config: 100% (critical - configuration)
  - pkg/ble: 62% (should be improved when adding features)
  - cmd/blecli: 21.6% (limited by hardware requirements)

## Performance Considerations
- Run benchmarks after performance-critical changes:
```bash
make bench          # Basic benchmarks
make bench-cpu      # CPU profiling
make bench-mem      # Memory profiling
```

## Platform-Specific Notes
- **macOS Requirements**: Ensure Xcode Command Line Tools for CGO
- **LuaJIT Integration**: All builds use LuaJIT tags for performance
- **Bluetooth Permissions**: CLI requires Bluetooth permissions in Terminal

## Git Workflow
- No specific git hooks mentioned
- Standard commit practices
- Consider CI/CD integration (GitHub Actions mentioned)

## Error Handling Standards
- All public functions should return meaningful errors
- Use structured logging for debugging
- Mock external dependencies in tests
- Validate input parameters appropriately

## Documentation Updates
- Update CLAUDE.md if adding new commands or changing architecture
- Maintain Makefile help target if adding new make targets
- Update coverage reports after significant changes