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

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	$(GOBUILD) $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/bcq

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
	BCQ_NO_KEYRING=1 BCQ_BIN=./bin/bcq bats e2e/

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

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

# Install to GOPATH/bin
.PHONY: install
install:
	$(GOCMD) install ./cmd/bcq

# Run all checks (for CI/pre-commit)
.PHONY: check
check: fmt-check vet test

# Development: build and run
.PHONY: run
run: build
	$(BUILD_DIR)/$(BINARY)

# Show help
.PHONY: help
help:
	@echo "bcq Makefile targets:"
	@echo ""
	@echo "  build          Build the binary"
	@echo "  build-all      Build for all platforms"
	@echo "  build-darwin   Build for macOS (arm64 + amd64)"
	@echo "  build-linux    Build for Linux (arm64 + amd64)"
	@echo "  build-windows  Build for Windows (arm64 + amd64)"
	@echo "  build-bsd      Build for FreeBSD + OpenBSD (arm64 + amd64)"
	@echo "  test           Run Go unit tests"
	@echo "  test-e2e       Run end-to-end tests against Go binary"
	@echo "  test-coverage  Run tests with coverage report"
	@echo "  vet            Run go vet"
	@echo "  fmt            Format code"
	@echo "  fmt-check      Check code formatting"
	@echo "  lint           Run golangci-lint"
	@echo "  tidy           Tidy go.mod dependencies"
	@echo "  verify         Verify dependencies"
	@echo "  clean          Remove build artifacts"
	@echo "  install        Install to GOPATH/bin"
	@echo "  check          Run all checks (fmt-check, vet, test)"
	@echo "  run            Build and run"
	@echo "  help           Show this help"
