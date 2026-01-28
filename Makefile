# bcq Makefile

# Binary name
BINARY := bcq

# Build directory
BUILD_DIR := ./bin

# Version info (can be overridden: make build VERSION=1.0.0)
VERSION ?= dev
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOTEST := $(GOCMD) test
GOVET := $(GOCMD) vet
GOFMT := gofmt
GOMOD := $(GOCMD) mod

# Version package path
VERSION_PKG := github.com/basecamp/bcq/internal/version

# Build flags
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)
BUILD_FLAGS := -ldflags "$(LDFLAGS)"

# PGO (Profile-Guided Optimization)
PGO_PROFILE := default.pgo
HAS_PGO := $(shell test -f $(PGO_PROFILE) && echo 1 || echo 0)
ifeq ($(HAS_PGO),1)
    PGO_FLAGS := -pgo=$(PGO_PROFILE)
else
    PGO_FLAGS :=
endif

# Default target
.PHONY: all
all: check

# Build the binary
.PHONY: build
build:
	$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/bcq

# Build with PGO optimization (requires default.pgo)
.PHONY: build-pgo
build-pgo:
	@if [ ! -f $(PGO_PROFILE) ]; then \
		echo "Warning: $(PGO_PROFILE) not found. Run 'make collect-profile' first."; \
		echo "Building without PGO..."; \
		$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/bcq; \
	else \
		echo "Building with PGO optimization..."; \
		$(GOBUILD) $(BUILD_FLAGS) $(PGO_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/bcq; \
	fi

# Build for all platforms
.PHONY: build-all
build-all: build-darwin build-linux build-windows build-bsd

# Build for macOS
.PHONY: build-darwin
build-darwin:
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/bcq
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/bcq

# Build for Linux
.PHONY: build-linux
build-linux:
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/bcq
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/bcq

# Build for Windows
.PHONY: build-windows
build-windows:
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe ./cmd/bcq
	GOOS=windows GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-arm64.exe ./cmd/bcq

# Build for BSDs
.PHONY: build-bsd
build-bsd:
	GOOS=freebsd GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-freebsd-amd64 ./cmd/bcq
	GOOS=freebsd GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-freebsd-arm64 ./cmd/bcq
	GOOS=openbsd GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-openbsd-amd64 ./cmd/bcq
	GOOS=openbsd GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-openbsd-arm64 ./cmd/bcq

# Run tests
.PHONY: test
test:
	BCQ_NO_KEYRING=1 $(GOTEST) -v ./...

# Run end-to-end tests against Go binary
.PHONY: test-e2e
test-e2e: build
	./e2e/run.sh

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# ============================================================================
# Benchmarking & Performance
# ============================================================================

# Run all benchmarks
.PHONY: bench
bench:
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem ./internal/...

# Run benchmarks with CPU profiling (profiles first package only due to Go limitation)
.PHONY: bench-cpu
bench-cpu:
	@mkdir -p profiles
	@echo "Profiling internal/names (primary hot path)..."
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchtime=1s -cpuprofile=profiles/cpu.pprof ./internal/names
	@echo "CPU profile saved to profiles/cpu.pprof"
	@echo "View with: go tool pprof -http=:8080 profiles/cpu.pprof"
	@echo ""
	@echo "Note: For full multi-package profiling, use 'make collect-profile'"

# Run benchmarks with memory profiling (profiles first package only due to Go limitation)
.PHONY: bench-mem
bench-mem:
	@mkdir -p profiles
	@echo "Profiling internal/names (primary hot path)..."
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchtime=1s -benchmem -memprofile=profiles/mem.pprof ./internal/names
	@echo "Memory profile saved to profiles/mem.pprof"
	@echo "View with: go tool pprof -http=:8080 profiles/mem.pprof"
	@echo ""
	@echo "Note: For full multi-package profiling, use 'make collect-profile'"

# Save current benchmarks as baseline for comparison
.PHONY: bench-save
bench-save:
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem -count=5 ./internal/... > benchmarks-baseline.txt
	@echo "Baseline saved to benchmarks-baseline.txt"

# Compare current benchmarks against baseline
.PHONY: bench-compare
bench-compare:
	@if [ ! -f benchmarks-baseline.txt ]; then \
		echo "No baseline found. Run 'make bench-save' first."; \
		exit 1; \
	fi
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem -count=5 ./internal/... > benchmarks-current.txt
	@command -v benchstat >/dev/null 2>&1 || go install golang.org/x/perf/cmd/benchstat@latest
	benchstat benchmarks-baseline.txt benchmarks-current.txt

# Check benchmarks against performance targets (CI gate)
.PHONY: bench-gate
bench-gate:
	@if [ ! -f perf-targets.yaml ]; then \
		echo "Error: perf-targets.yaml not found"; \
		exit 1; \
	fi
	@command -v yq >/dev/null 2>&1 || (echo "Error: yq is required. Install with: brew install yq" && exit 1)
	BCQ_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem ./internal/... | ./scripts/check-perf-targets.sh

# ============================================================================
# Profile-Guided Optimization (PGO)
# ============================================================================

# Collect PGO profile from benchmarks
.PHONY: collect-profile
collect-profile:
	./scripts/collect-profile.sh

# Clean PGO and profiling artifacts
.PHONY: clean-pgo
clean-pgo:
	rm -f $(PGO_PROFILE)
	rm -rf profiles/
	rm -f benchmarks-*.txt

# Run go vet
.PHONY: vet
vet:
	$(GOVET) ./...

# Format code
.PHONY: fmt
fmt:
	$(GOFMT) -s -w .

# Check formatting (for CI)
.PHONY: fmt-check
fmt-check:
	@test -z "$$($(GOFMT) -s -l . | tee /dev/stderr)" || (echo "Code is not formatted. Run 'make fmt'" && exit 1)

# Run linter (requires golangci-lint)
.PHONY: lint
lint:
	golangci-lint run ./...

# Tidy dependencies
.PHONY: tidy
tidy:
	$(GOMOD) tidy

# Verify dependencies
.PHONY: verify
verify:
	$(GOMOD) verify

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Clean all (including PGO artifacts)
.PHONY: clean-all
clean-all: clean clean-pgo

# Install to GOPATH/bin
.PHONY: install
install:
	$(GOCMD) install ./cmd/bcq

# Run all checks (local CI gate)
.PHONY: check
check: fmt-check vet lint test test-e2e

# Development: build and run
.PHONY: run
run: build
	$(BUILD_DIR)/$(BINARY)

# --- Security targets ---

# Run all security checks
.PHONY: security
security: lint vuln secrets

# Run vulnerability scanner
.PHONY: vuln
vuln:
	@echo "Running govulncheck..."
	govulncheck ./...

# Run secret scanner
.PHONY: secrets
secrets:
	@command -v gitleaks >/dev/null || (echo "Install gitleaks: brew install gitleaks" && exit 1)
	gitleaks detect --source . --verbose

# Run fuzz tests (30s each by default)
.PHONY: fuzz
fuzz:
	@echo "Running dateparse fuzz test..."
	go test -fuzz=FuzzParseFrom -fuzztime=30s ./internal/dateparse/
	@echo "Running URL parsing fuzz test..."
	go test -fuzz=FuzzURLPathParsing -fuzztime=30s ./internal/commands/

# Run quick fuzz tests (10s each, for CI)
.PHONY: fuzz-quick
fuzz-quick:
	go test -fuzz=FuzzParseFrom -fuzztime=10s ./internal/dateparse/
	go test -fuzz=FuzzURLPathParsing -fuzztime=10s ./internal/commands/

# Install development tools
.PHONY: tools
tools:
	$(GOCMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.8.0
	$(GOCMD) install golang.org/x/vuln/cmd/govulncheck@v1.1.4
	$(GOCMD) install golang.org/x/perf/cmd/benchstat@v0.0.0-20250106171221-62ad9bd2d39e
	$(GOCMD) install github.com/zricethezav/gitleaks/v8@v8.21.2
	$(GOCMD) install github.com/mikefarah/yq/v4@v4.50.1

# Show help
.PHONY: help
help:
	@echo "bcq Makefile targets:"
	@echo ""
	@echo "Build:"
	@echo "  build          Build the binary"
	@echo "  build-pgo      Build with PGO optimization (requires profile)"
	@echo "  build-all      Build for all platforms"
	@echo "  build-darwin   Build for macOS (arm64 + amd64)"
	@echo "  build-linux    Build for Linux (arm64 + amd64)"
	@echo "  build-windows  Build for Windows (arm64 + amd64)"
	@echo "  build-bsd      Build for FreeBSD + OpenBSD (arm64 + amd64)"
	@echo ""
	@echo "Test:"
	@echo "  test           Run Go unit tests"
	@echo "  test-e2e       Run end-to-end tests against Go binary"
	@echo "  test-coverage  Run tests with coverage report"
	@echo ""
	@echo "Performance:"
	@echo "  bench          Run all benchmarks"
	@echo "  bench-cpu      Run benchmarks with CPU profiling"
	@echo "  bench-mem      Run benchmarks with memory profiling"
	@echo "  bench-save     Save current benchmarks as baseline"
	@echo "  bench-compare  Compare against baseline (requires benchstat)"
	@echo "  bench-gate     Check against performance targets (CI gate)"
	@echo ""
	@echo "PGO (Profile-Guided Optimization):"
	@echo "  collect-profile  Generate PGO profile from benchmarks"
	@echo "  clean-pgo        Remove PGO artifacts"
	@echo ""
	@echo "Code Quality:"
	@echo "  vet            Run go vet"
	@echo "  fmt            Format code"
	@echo "  fmt-check      Check code formatting"
	@echo "  lint           Run golangci-lint"
	@echo "  check          Run all checks (fmt-check, vet, lint, test, test-e2e)"
	@echo ""
	@echo "Other:"
	@echo "  tools          Install development tools (golangci-lint, govulncheck, etc.)"
	@echo "  tidy           Tidy go.mod dependencies"
	@echo "  verify         Verify dependencies"
	@echo "  clean          Remove build artifacts"
	@echo "  clean-all      Remove all artifacts (including PGO)"
	@echo "  install        Install to GOPATH/bin"
	@echo "  check          Run all checks (local CI gate)"
	@echo "  run            Build and run"
	@echo ""
	@echo "Security:"
	@echo "  security       Run all security checks (lint, vuln, secrets)"
	@echo "  vuln           Run govulncheck for dependency vulnerabilities"
	@echo "  secrets        Run gitleaks for secret detection"
	@echo "  fuzz           Run fuzz tests (30s each)"
	@echo "  fuzz-quick     Run quick fuzz tests (10s each, for CI)"
	@echo ""
	@echo "  help           Show this help"
