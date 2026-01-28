# Development Dockerfile for bcq
#
# NOTE: This Dockerfile requires vendored dependencies or BuildKit secrets
# for the private basecamp-sdk. For local builds, either:
#   1. Run `go mod vendor` first, then build with: docker build .
#   2. Use GoReleaser for release builds (handles auth automatically)
#
# For CI/release builds, use Dockerfile.goreleaser instead.

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# Build with vendored deps if available, otherwise try to download (may fail without auth for private SDK)
RUN if [ -d vendor ]; then \
        CGO_ENABLED=0 GOOS=linux go build -mod=vendor \
            -trimpath \
            -ldflags="-s -w -X github.com/basecamp/bcq/internal/version.Version=${VERSION} -X github.com/basecamp/bcq/internal/version.Commit=${COMMIT} -X github.com/basecamp/bcq/internal/version.Date=${BUILD_DATE}" \
            -o /bcq ./cmd/bcq; \
    else \
        go mod download && \
        CGO_ENABLED=0 GOOS=linux go build \
            -trimpath \
            -ldflags="-s -w -X github.com/basecamp/bcq/internal/version.Version=${VERSION} -X github.com/basecamp/bcq/internal/version.Commit=${COMMIT} -X github.com/basecamp/bcq/internal/version.Date=${BUILD_DATE}" \
            -o /bcq ./cmd/bcq; \
    fi

# Runtime stage using distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /bcq /bcq

USER nonroot:nonroot

ENTRYPOINT ["/bcq"]
