# Build stage
FROM golang:1.25 AS builder

WORKDIR /workspace

# Copy go module files first for better layer caching
COPY go.mod go.sum ./
COPY vendor/ vendor/

# Copy source code
COPY api/ api/
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o openkruise-sandbox-agent ./cmd/openkruise-sandbox-agent/

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/openkruise-sandbox-agent /openkruise-sandbox-agent

# The agent needs to run as root and with hostPID for nsenter to work.
# These are configured in the DaemonSet spec, not in the Dockerfile.
# The distroless image is used for minimal attack surface.

ENTRYPOINT ["/openkruise-sandbox-agent"]
