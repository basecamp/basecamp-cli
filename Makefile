SHELL := /bin/bash

# basecamp Makefile

# Binary name
BINARY := basecamp

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
VERSION_PKG := github.com/basecamp/basecamp-cli/internal/version

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
	$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/basecamp

# Build with PGO optimization (requires default.pgo)
.PHONY: build-pgo
build-pgo:
	@if [ ! -f $(PGO_PROFILE) ]; then \
		echo "Warning: $(PGO_PROFILE) not found. Run 'make collect-profile' first."; \
		echo "Building without PGO..."; \
		$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/basecamp; \
	else \
		echo "Building with PGO optimization..."; \
		$(GOBUILD) $(BUILD_FLAGS) $(PGO_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/basecamp; \
	fi

# Build for all platforms
.PHONY: build-all
build-all: build-darwin build-linux build-windows build-bsd

# Build for macOS
.PHONY: build-darwin
build-darwin:
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/basecamp
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/basecamp

# Build for Linux
.PHONY: build-linux
build-linux:
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/basecamp
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/basecamp

# Build for Windows
.PHONY: build-windows
build-windows:
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe ./cmd/basecamp
	GOOS=windows GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-windows-arm64.exe ./cmd/basecamp

# Build for BSDs
.PHONY: build-bsd
build-bsd:
	GOOS=freebsd GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-freebsd-amd64 ./cmd/basecamp
	GOOS=freebsd GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-freebsd-arm64 ./cmd/basecamp
	GOOS=openbsd GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-openbsd-amd64 ./cmd/basecamp
	GOOS=openbsd GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY)-openbsd-arm64 ./cmd/basecamp

# Run tests
.PHONY: test
test:
	BASECAMP_NO_KEYRING=1 $(GOTEST) -v ./...

# Run end-to-end tests against Go binary
.PHONY: test-e2e
test-e2e: build
	./e2e/run.sh

# Run tests with race detector
.PHONY: race-test
race-test:
	BASECAMP_NO_KEYRING=1 $(GOTEST) -race -count=1 ./...

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
	BASECAMP_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem ./internal/...

# Run benchmarks with CPU profiling (profiles first package only due to Go limitation)
.PHONY: bench-cpu
bench-cpu:
	@mkdir -p profiles
	@echo "Profiling internal/names (primary hot path)..."
	BASECAMP_NO_KEYRING=1 $(GOTEST) -bench=. -benchtime=1s -cpuprofile=profiles/cpu.pprof ./internal/names
	@echo "CPU profile saved to profiles/cpu.pprof"
	@echo "View with: go tool pprof -http=:8080 profiles/cpu.pprof"
	@echo ""
	@echo "Note: For full multi-package profiling, use 'make collect-profile'"

# Run benchmarks with memory profiling (profiles first package only due to Go limitation)
.PHONY: bench-mem
bench-mem:
	@mkdir -p profiles
	@echo "Profiling internal/names (primary hot path)..."
	BASECAMP_NO_KEYRING=1 $(GOTEST) -bench=. -benchtime=1s -benchmem -memprofile=profiles/mem.pprof ./internal/names
	@echo "Memory profile saved to profiles/mem.pprof"
	@echo "View with: go tool pprof -http=:8080 profiles/mem.pprof"
	@echo ""
	@echo "Note: For full multi-package profiling, use 'make collect-profile'"

# Save current benchmarks as baseline for comparison
.PHONY: bench-save
bench-save:
	BASECAMP_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem -count=5 ./internal/... > benchmarks-baseline.txt
	@echo "Baseline saved to benchmarks-baseline.txt"

# Compare current benchmarks against baseline
.PHONY: bench-compare
bench-compare:
	@if [ ! -f benchmarks-baseline.txt ]; then \
		echo "No baseline found. Run 'make bench-save' first."; \
		exit 1; \
	fi
	BASECAMP_NO_KEYRING=1 $(GOTEST) -bench=. -benchmem -count=5 ./internal/... > benchmarks-current.txt
	@command -v benchstat >/dev/null 2>&1 || go install golang.org/x/perf/cmd/benchstat@latest
	benchstat benchmarks-baseline.txt benchmarks-current.txt

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

# Bump SDK dependency and update provenance
.PHONY: bump-sdk
bump-sdk:
	./scripts/bump-sdk.sh $(REF)

# Verify sdk-provenance.json matches go.mod
# Skips when a replace directive is active (local dev with go.work or go.mod replace)
.PHONY: provenance-check
provenance-check:
	@REPLACE=$$(go list -m -f '{{.Replace}}' github.com/basecamp/basecamp-sdk/go) && \
	if [ -n "$$REPLACE" ] && [ "$$REPLACE" != "<nil>" ]; then \
		echo "SDK provenance check skipped (replace active: $$REPLACE)"; \
		exit 0; \
	fi && \
	MOD_VER=$$(go list -m -f '{{.Version}}' github.com/basecamp/basecamp-sdk/go) && \
	PROV_VER=$$(jq -r '.sdk.version' internal/version/sdk-provenance.json) && \
	if [ "$$MOD_VER" != "$$PROV_VER" ]; then \
		echo "ERROR: SDK provenance drift detected"; \
		echo "  go.mod:              $$MOD_VER"; \
		echo "  sdk-provenance.json: $$PROV_VER"; \
		echo ""; \
		echo "Run 'make bump-sdk' to update provenance."; \
		exit 1; \
	fi && \
	echo "SDK provenance OK ($$MOD_VER)"

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
# Prefer GOBIN/GOPATH binary (matches active Go toolchain) over PATH (may be Homebrew/system)
GOLANGCI_LINT := $(shell go env GOBIN)/golangci-lint
ifeq ($(wildcard $(GOLANGCI_LINT)),)
  GOLANGCI_LINT := $(shell go env GOPATH)/bin/golangci-lint
endif
ifeq ($(wildcard $(GOLANGCI_LINT)),)
  GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null)
endif

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...

# Tidy dependencies
.PHONY: tidy
tidy:
	$(GOMOD) tidy

# Verify go.mod/go.sum are tidy (CI gate)
# Restores original files on any failure so the check is non-mutating.
.PHONY: tidy-check
tidy-check:
	@set -e; cp go.mod go.mod.tidycheck; cp go.sum go.sum.tidycheck; \
	restore() { mv go.mod.tidycheck go.mod; mv go.sum.tidycheck go.sum; }; \
	if ! $(GOMOD) tidy; then \
		restore; \
		echo "'go mod tidy' failed. Restored original go.mod/go.sum."; \
		exit 1; \
	fi; \
	if ! git diff --quiet -- go.mod go.sum; then \
		restore; \
		echo "go.mod/go.sum are not tidy. Run 'make tidy' and commit the result."; \
		exit 1; \
	fi; \
	rm -f go.mod.tidycheck go.sum.tidycheck

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
	$(GOCMD) install ./cmd/basecamp

# Guard against local replace directives in go.mod
.PHONY: replace-check
replace-check:
	@if grep -q '^\s*replace\s' go.mod; then \
		echo "ERROR: go.mod contains replace directives"; \
		grep '^\s*replace\s' go.mod; \
		echo ""; \
		echo "Remove replace directives before releasing."; \
		exit 1; \
	fi
	@echo "Replace check passed (no local replace directives)"

# Run all checks (local CI gate)
.PHONY: check
check: fmt-check vet lint test test-e2e check-naming check-surface provenance-check tidy-check

# Full pre-flight for release: check + replace-check + vuln + race + surface compat
.PHONY: release-check
release-check: check replace-check vuln race-test check-surface-compat

# Cut a release (delegates to scripts/release.sh)
.PHONY: release
release:
	scripts/release.sh $(VERSION)

# Generate CLI surface snapshot (validates binary produces valid output)
.PHONY: check-surface
check-surface: build
	scripts/check-cli-surface.sh $(BUILD_DIR)/$(BINARY) /tmp/cli-surface.txt
	@echo "CLI surface snapshot generated ($$(wc -l < /tmp/cli-surface.txt) entries)"

# Compare CLI surface against baseline (fails on removals)
.PHONY: check-surface-diff
check-surface-diff:
	scripts/check-cli-surface-diff.sh $(BASELINE) $(CURRENT)

# Check CLI surface compatibility against previous tag (mirrors CI gate)
.PHONY: check-surface-compat
check-surface-compat: build
	@scripts/check-cli-surface.sh $(BUILD_DIR)/$(BINARY) /tmp/current-surface.txt
	@PREV_TAG=$$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || echo ""); \
	if [ -n "$$PREV_TAG" ]; then \
		SCRIPT_DIR="$$(pwd)/scripts"; \
		git worktree add /tmp/baseline-tree "$$PREV_TAG" 2>/dev/null; \
		cd /tmp/baseline-tree && make build && \
		"$$SCRIPT_DIR/check-cli-surface.sh" ./bin/basecamp /tmp/baseline-surface.txt; \
		cd - >/dev/null; \
		git worktree remove /tmp/baseline-tree --force 2>/dev/null; \
		scripts/check-cli-surface-diff.sh /tmp/baseline-surface.txt /tmp/current-surface.txt; \
	else \
		echo "First release — no baseline to compare against"; \
	fi

# Guard against bcq/BCQ creeping back (allowlist in .naming-allowlist)
.PHONY: check-naming
check-naming:
	@HITS=$$(rg -n --hidden --type-add 'bats:*.bats' -t go -t sh -t yaml -t json -t md -t bats -t toml 'bcq|BCQ' -g '!.git/' . 2>/dev/null) || true; \
	if [ -n "$$HITS" ]; then \
		while IFS= read -r line; do \
			line=$${line%%\#*}; \
			line=$$(echo "$$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$$//'); \
			[ -z "$$line" ] && continue; \
			HITS=$$(echo "$$HITS" | grep -v -F "$$line" || true); \
		done < .naming-allowlist; \
	fi; \
	if [ -n "$$HITS" ]; then \
		echo "ERROR: Legacy bcq/BCQ references found outside allowlist:"; \
		echo "$$HITS"; \
		echo ""; \
		echo "Either rename the reference or add the path to .naming-allowlist"; \
		exit 1; \
	fi; \
	echo "Naming check passed (no stale bcq/BCQ references)"

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
	$(GOCMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GOCMD) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GOCMD) install golang.org/x/perf/cmd/benchstat@latest
	$(GOCMD) install github.com/zricethezav/gitleaks/v8@latest
	@command -v jq >/dev/null 2>&1 || echo "NOTE: jq is also required (install via your package manager)"


# Show help
.PHONY: help
help:
	@echo "basecamp Makefile targets:"
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
	@echo "  race-test      Run tests with race detector"
	@echo "  test-coverage  Run tests with coverage report"
	@echo ""
	@echo "Performance:"
	@echo "  bench          Run all benchmarks"
	@echo "  bench-cpu      Run benchmarks with CPU profiling"
	@echo "  bench-mem      Run benchmarks with memory profiling"
	@echo "  bench-save     Save current benchmarks as baseline"
	@echo "  bench-compare  Compare against baseline (requires benchstat)"
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
	@echo "  tidy-check     Verify go.mod/go.sum are tidy"
	@echo "  check          Run all checks (local CI gate)"
	@echo "  check-surface  Generate CLI surface snapshot (validates --help --agent output)"
	@echo "  check-surface-diff  Compare CLI surface snapshots (fails on removals)"
	@echo ""
	@echo "Dependencies:"
	@echo "  bump-sdk          Bump SDK and update provenance (REF=<git-ref>)"
	@echo "  provenance-check  Verify sdk-provenance.json matches go.mod"
	@echo ""
	@echo "Other:"
	@echo "  tools          Install development tools (golangci-lint, govulncheck, etc.)"
	@echo "  tidy           Tidy go.mod dependencies"
	@echo "  verify         Verify dependencies"
	@echo "  clean          Remove build artifacts"
	@echo "  clean-all      Remove all artifacts (including PGO)"
	@echo "  install        Install to GOPATH/bin"
	@echo "  check            Run all checks (local CI gate)"
	@echo "  check-naming     Guard against stale bcq/BCQ references"
	@echo "  replace-check    Guard against local replace directives in go.mod"
	@echo "  run              Build and run"
	@echo ""
	@echo "Release:"
	@echo "  release-check    Full pre-flight (check + replace-check + vuln + race + surface compat)"
	@echo "  release          Cut a release (VERSION=x.y.z, DRY_RUN=1 optional)"
	@echo ""
	@echo "Security:"
	@echo "  security       Run all security checks (lint, vuln, secrets)"
	@echo "  vuln           Run govulncheck for dependency vulnerabilities"
	@echo "  secrets        Run gitleaks for secret detection"
	@echo "  fuzz           Run fuzz tests (30s each)"
	@echo "  fuzz-quick     Run quick fuzz tests (10s each, for CI)"
	@echo ""
	@echo "  help           Show this help"
