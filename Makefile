# BLIM CLI Tool Makefile
#
# Quick start:
#   make           - Build and test
#   make build     - Build binary
#   make test      - Run device_test
#   make help      - Show all targets

# Shell configuration for safer execution
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DELETE_ON_ERROR:
.SUFFIXES:

#------------------------------------------------------------------------------
# Variables
#------------------------------------------------------------------------------

# Binary configuration
BINARY_NAME := blim
BUILD_DIR := .
COVERAGE_DIR := coverage
CMD_DIR := ./cmd/$(BINARY_NAME)

# Version information (can be overridden during build)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Go configuration
GO := go
GOFLAGS ?=
GO_BUILD_FLAGS := $(GOFLAGS) -tags luajit 
GO_TEST_FLAGS := $(GO_BUILD_FLAGS) -tags test
GO_LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)"

# Detect Homebrew prefix for better portability across macOS systems
HOMEBREW_PREFIX := $(shell brew --prefix 2>/dev/null || echo "/opt/homebrew")

# LuaJIT configuration (uses detected Homebrew prefix)
export CGO_LDFLAGS := -L$(HOMEBREW_PREFIX)/lib
export PKG_CONFIG_PATH := $(HOMEBREW_PREFIX)/lib/pkgconfig

# Tool versions
GOLANGCI_LINT_VERSION := latest
GOSEC_VERSION := latest
MOCKERY_VERSION := v3

# Generated file paths
BLEDB_GENERATED := internal/bledb/bledb_generated.go
MOCKS_DIR := internal/testutils/mocks

# Default goal
.DEFAULT_GOAL := all

#------------------------------------------------------------------------------
# Primary Targets
#------------------------------------------------------------------------------

## all: Build and test (default target)
.PHONY: all
all: build test

## build: Build the application with LuaJIT
.PHONY: build
build: $(BLEDB_GENERATED)
	@echo "Building $(BINARY_NAME) with LuaJIT..."
	@$(GO) build $(GO_BUILD_FLAGS) $(GO_LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "✓ Built: $(BUILD_DIR)/$(BINARY_NAME)"

## run: Build and run the application (use ARGS="..." to pass arguments)
.PHONY: run
run: build
	@echo "Running $(BINARY_NAME) $(ARGS)..."
	@./$(BINARY_NAME) $(ARGS)

## clean: Remove all build artifacts and generated files
.PHONY: clean
clean: clean-mocks clean-docs clean-bledb clean-depend
	@echo "Cleaning build artifacts..."
	@rm -f $(BUILD_DIR)/$(BINARY_NAME)
	@rm -rf $(COVERAGE_DIR)
	@rm -rf .tmp
	@echo "✓ Cleaned"

#------------------------------------------------------------------------------
# Code Generation
#------------------------------------------------------------------------------

## generate: Generate all code (BLE database, mocks, depend device_test, etc.)
.PHONY: generate
generate: $(BLEDB_GENERATED) generate-mocks generate-depend

## generate-bledb: Generate BLE UUID database
$(BLEDB_GENERATED): internal/bledb/*.go
	@echo "Generating BLE database..."
	@$(GO) generate ./internal/bledb
	@echo "✓ BLE database generated"

.PHONY: generate-bledb
generate-bledb: $(BLEDB_GENERATED)

## generate-mocks: Generate test mocks using mockery
.PHONY: generate-mocks
generate-mocks:
	@if ! command -v mockery >/dev/null 2>&1; then \
		echo "Installing mockery..."; \
		$(GO) install github.com/vektra/mockery/$(MOCKERY_VERSION)@latest; \
	fi
	@echo "Generating mocks..."
	@mockery --quiet 2>/dev/null || mockery
	@echo "✓ Mocks generated"

## generate-depend: Generate dependency test infrastructure using dependgen
.PHONY: generate-depend
generate-depend:
	@echo "Generating depend test code..."
	@$(GO) generate -tags test ./internal/device
	@echo "✓ Depend test code generated"

## clean-bledb: Remove generated BLE database
.PHONY: clean-bledb
clean-bledb:
	@echo "Cleaning generated BLE database..."
	@rm -f $(BLEDB_GENERATED)
	@echo "✓ BLE database cleaned"

## clean-mocks: Remove generated mocks
.PHONY: clean-mocks
clean-mocks:
	@echo "Cleaning generated mocks..."
	@find "$(MOCKS_DIR)" -type f -name 'mock_*.go' -exec rm -f {} +
	@echo "✓ Mocks cleaned"

## clean-depend: Remove generated depend test files
.PHONY: clean-depend
clean-depend:
	@echo "Cleaning generated depend test files..."
	@find ./internal/device -type f -name '*_depend_test.go' -exec rm -f {} +
	@echo "✓ Depend test files cleaned"

#------------------------------------------------------------------------------
# Testing
#------------------------------------------------------------------------------

## test: Run all device_test (use TEST=<pattern> for specific device_test)
.PHONY: test
test: generate
	@if [ -z "$(TEST)" ]; then \
		echo "Running all tests..."; \
		$(GO) test $(GO_TEST_FLAGS) -v ./... 2>&1; \
	else \
		echo "Running tests matching: $(TEST)"; \
		$(GO) test $(GO_TEST_FLAGS) -v -run $(TEST) ./... 2>&1; \
	fi

## test-race: Run device_test with race detector
.PHONY: test-race
test-race: generate
	@echo "Running tests with race detection..."
	@$(GO) test $(GO_TEST_FLAGS) -race -v ./...

## test-coverage: Generate test coverage report
.PHONY: test-coverage
test-coverage: generate
	@echo "Running tests with coverage..."
	@mkdir -p $(COVERAGE_DIR)
	@$(GO) test $(GO_TEST_FLAGS) -coverprofile=$(COVERAGE_DIR)/coverage.out ./...
	@$(GO) tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "✓ Coverage report: $(COVERAGE_DIR)/coverage.html"

## coverage: Show coverage summary
.PHONY: coverage
coverage: test-coverage
	@echo ""
	@echo "Coverage summary:"
	@$(GO) tool cover -func=$(COVERAGE_DIR)/coverage.out

## test-cmd: Run command (CLI) package device_test only
.PHONY: test-cmd
test-cmd: generate
	@echo "Running CLI tests..."
	@$(GO) test $(GO_TEST_FLAGS) -v $(CMD_DIR)

#------------------------------------------------------------------------------
# Benchmarking
#------------------------------------------------------------------------------

## bench: Run all benchmarks
.PHONY: bench
bench:
	@echo "Running benchmarks..."
	@$(GO) test $(GO_BUILD_FLAGS) -bench=. -benchmem -run=^$$ ./...

## bench-cpu: Run benchmarks with CPU profiling
.PHONY: bench-cpu
bench-cpu:
	@echo "Running benchmarks with CPU profiling..."
	@mkdir -p $(COVERAGE_DIR)
	@$(GO) test $(GO_BUILD_FLAGS) -bench=. -benchmem -cpuprofile=$(COVERAGE_DIR)/cpu.prof -run=^$$ ./...
	@echo "✓ CPU profile: $(COVERAGE_DIR)/cpu.prof"
	@echo "  View with: go tool pprof $(COVERAGE_DIR)/cpu.prof"

## bench-mem: Run benchmarks with memory profiling
.PHONY: bench-mem
bench-mem:
	@echo "Running benchmarks with memory profiling..."
	@mkdir -p $(COVERAGE_DIR)
	@$(GO) test $(GO_BUILD_FLAGS) -bench=. -benchmem -memprofile=$(COVERAGE_DIR)/mem.prof -run=^$$ ./...
	@echo "✓ Memory profile: $(COVERAGE_DIR)/mem.prof"
	@echo "  View with: go tool pprof $(COVERAGE_DIR)/mem.prof"

#------------------------------------------------------------------------------
# Code Quality
#------------------------------------------------------------------------------

## fmt: Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	@$(GO) fmt ./...
	@echo "✓ Code formatted"

## lint: Run linter
.PHONY: lint
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
		echo "✓ Lint passed"; \
	else \
		echo "⚠ golangci-lint not found, falling back to go vet..."; \
		$(GO) vet ./...; \
		echo "Install golangci-lint: make install-tools"; \
	fi

## security: Run security checks
.PHONY: security
security:
	@echo "Running security checks..."
	@if command -v gosec >/dev/null 2>&1; then \
		gosec -quiet ./...; \
		echo "✓ Security check passed"; \
	else \
		echo "⚠ gosec not found"; \
		echo "Install with: go install github.com/securego/gosec/v2/cmd/gosec@latest"; \
	fi

## check: Run full quality check (fmt, lint, test-race, test-coverage, security)
.PHONY: check
check: fmt lint test-race test-coverage security
	@echo ""
	@echo "✓ All quality checks completed!"

#------------------------------------------------------------------------------
# Dependency Management
#------------------------------------------------------------------------------

## tidy: Tidy dependencies
.PHONY: tidy
tidy:
	@echo "Tidying dependencies..."
	@$(GO) mod tidy
	@echo "✓ Dependencies tidied"

## verify: Verify dependencies
.PHONY: verify
verify:
	@echo "Verifying dependencies..."
	@$(GO) mod verify
	@echo "✓ Dependencies verified"

## download: Download dependencies
.PHONY: download
download:
	@echo "Downloading dependencies..."
	@$(GO) mod download
	@echo "✓ Dependencies downloaded"

#------------------------------------------------------------------------------
# Documentation
#------------------------------------------------------------------------------

## docs: Generate static documentation for GitHub/Cloudflare Pages
.PHONY: docs
docs:
	@if [ -f "./scripts/generate-docs.sh" ]; then \
		./scripts/generate-docs.sh; \
	else \
		echo "⚠ scripts/generate-docs.sh not found"; \
	fi

## docs-serve: Serve generated documentation locally
.PHONY: docs-serve
docs-serve:
	@if [ ! -d ".tmp/docs-build" ]; then \
		echo "Error: Documentation not generated. Run 'make docs' first."; \
		exit 1; \
	fi
	@echo "Starting local preview server..."
	@echo "Documentation will be available at http://127.0.0.1:8000"
	@echo "Press Ctrl+C to stop"
	@cd .tmp/docs-build && ../../.tmp/venv-docs/bin/mkdocs serve

## clean-docs: Remove generated documentation
.PHONY: clean-docs
clean-docs:
	@echo "Cleaning generated documentation..."
	@rm -rf .tmp/docs-build .tmp/venv-docs site
	@echo "✓ Documentation cleaned"

#------------------------------------------------------------------------------
# Development Tools
#------------------------------------------------------------------------------

## install-tools: Install development tools
.PHONY: install-tools
install-tools:
	@echo "Installing development tools..."
	@$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@$(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	@$(GO) install github.com/vektra/mockery/$(MOCKERY_VERSION)@latest
	@echo "✓ Development tools installed"

#------------------------------------------------------------------------------
# Help
#------------------------------------------------------------------------------

## help: Show this help message
.PHONY: help
help:
	@echo "$(BINARY_NAME) - BLE CLI Tool for macOS"
	@echo ""
	@echo "Usage: make [target] [VARIABLE=value]"
	@echo ""
	@echo "Primary Targets:"
	@echo "  all              Build and test (default)"
	@echo "  build            Build the application"
	@echo "  run              Build and run (use ARGS=\"...\" for arguments)"
	@echo "  clean            Remove all build artifacts"
	@echo ""
	@echo "Testing:"
	@echo "  test             Run all tests (use TEST=<pattern> for specific tests)"
	@echo "  test-race        Run tests with race detector"
	@echo "  test-coverage    Generate test coverage report"
	@echo "  coverage         Show coverage summary"
	@echo "  test-cmd         Run CLI package tests only"
	@echo ""
	@echo "Benchmarking:"
	@echo "  bench            Run all benchmarks"
	@echo "  bench-cpu        Run benchmarks with CPU profiling"
	@echo "  bench-mem        Run benchmarks with memory profiling"
	@echo ""
	@echo "Code Quality:"
	@echo "  fmt              Format code"
	@echo "  lint             Run linter"
	@echo "  security         Run security checks"
	@echo "  check            Run full quality check"
	@echo ""
	@echo "Code Generation:"
	@echo "  generate         Generate all code (BLE database, mocks, depend tests)"
	@echo "  generate-bledb   Generate BLE UUID database"
	@echo "  generate-mocks   Generate test mocks"
	@echo "  generate-depend  Generate dependency test infrastructure"
	@echo "  clean-bledb      Remove generated BLE database"
	@echo "  clean-mocks      Remove generated mocks"
	@echo "  clean-depend     Remove generated depend test files"
	@echo ""
	@echo "Dependencies:"
	@echo "  tidy             Tidy dependencies"
	@echo "  verify           Verify dependencies"
	@echo "  download         Download dependencies"
	@echo ""
	@echo "Documentation:"
	@echo "  docs             Generate static documentation"
	@echo "  docs-serve       Serve documentation locally"
	@echo "  clean-docs       Remove generated documentation"
	@echo ""
	@echo "Development:"
	@echo "  install-tools    Install development tools"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION          Build version (default: dev)"
	@echo "  COMMIT           Git commit hash (default: auto-detected)"
	@echo "  BUILD_DATE       Build timestamp (default: auto-generated)"
	@echo "  ARGS             Arguments for 'make run'"
	@echo "  TEST             Test pattern for 'make test'"
	@echo ""
	@echo "Examples:"
	@echo "  make build VERSION=1.0.0           Build with version"
	@echo "  make run ARGS=\"scan --help\"         Run with arguments"
	@echo "  make test TEST=TestScan            Run specific test"
	@echo "  make bench                         Run benchmarks"
	@echo ""
	@echo "For more information, see README.md"
