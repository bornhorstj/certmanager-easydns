# =============================================================================
# Dockerfile for cert-manager easyDNS Webhook
# =============================================================================
#
# This builds a small, secure container image that runs the webhook server.
# We use a two-stage build:
#   Stage 1 (builder): Compiles the Go code into a binary
#   Stage 2 (runtime): Copies just the binary into a tiny final image
#
# Two-stage builds keep the final image small and free of build tools,
# which reduces attack surface in production.
#
# BUILD COMMANDS:
#   docker build -t your-registry/cert-manager-webhook-easydns:latest .
#   docker push your-registry/cert-manager-webhook-easydns:latest
# =============================================================================

# ── Stage 1: Build ──────────────────────────────────────────────────────────
# Use the official Go image to compile our webhook binary.
# We pin to a specific version for reproducibility.
FROM golang:1.21-alpine AS builder

# Install git (needed by Go to fetch dependencies)
RUN apk add --no-cache git

# Set our working directory inside the build container
WORKDIR /build

# Copy dependency files first — Docker caches these layers.
# If only main.go changes, Docker reuses the cached dependency download.
COPY go.mod go.sum ./

# Download all Go dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Compile the Go binary.
# CGO_ENABLED=0 : Pure Go binary, no C library dependencies (required for Alpine/scratch)
# GOOS=linux    : Target Linux (our container OS)
# -o webhook    : Name the output binary "webhook"
# -ldflags '-w -s' : Strip debug symbols to make the binary smaller
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags='-w -s' \
    -o webhook \
    .


# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
# Use a minimal Alpine image for the final container.
# Alpine is ~5MB vs ~900MB for full Ubuntu — much better for production.
FROM alpine:3.19

# Add CA certificates — needed to make HTTPS calls to the easyDNS API.
# Without this, TLS verification would fail.
RUN apk add --no-cache ca-certificates

# Create a non-root user for security.
# Running as root in containers is a security risk.
RUN addgroup -S webhook && adduser -S webhook -G webhook

# Switch to the non-root user
USER webhook

# Copy the compiled binary from the builder stage
COPY --from=builder /build/webhook /usr/local/bin/webhook

# The cert-manager webhook SDK listens on port 443 by default.
# This EXPOSE is documentation only — Kubernetes ignores it.
EXPOSE 443

# Run the webhook server when the container starts
ENTRYPOINT ["/usr/local/bin/webhook"]
