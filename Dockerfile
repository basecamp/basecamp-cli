# Development Dockerfile for basecamp
#
# Local builds only: `docker build .` (vendor first with `go mod vendor` when
# offline). Release binaries come from GoReleaser, not this image.

FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# Build with vendored deps if available, otherwise download
RUN if [ -d vendor ]; then \
        CGO_ENABLED=0 GOOS=linux go build -mod=vendor \
            -trimpath \
            -ldflags="-s -w -X github.com/basecamp/basecamp-cli/internal/version.Version=${VERSION} -X github.com/basecamp/basecamp-cli/internal/version.Commit=${COMMIT} -X github.com/basecamp/basecamp-cli/internal/version.Date=${BUILD_DATE}" \
            -o /basecamp ./cmd/basecamp; \
    else \
        go mod download && \
        CGO_ENABLED=0 GOOS=linux go build \
            -trimpath \
            -ldflags="-s -w -X github.com/basecamp/basecamp-cli/internal/version.Version=${VERSION} -X github.com/basecamp/basecamp-cli/internal/version.Commit=${COMMIT} -X github.com/basecamp/basecamp-cli/internal/version.Date=${BUILD_DATE}" \
            -o /basecamp ./cmd/basecamp; \
    fi

# Runtime stage using distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /basecamp /basecamp

USER nonroot:nonroot

ENTRYPOINT ["/basecamp"]
