# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /usr/src/app

# Install git for go mod download
RUN apk add --no-cache git

# Pre-copy go.mod and go.sum for better caching
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o watchdog ./cmd/watchdog

# Final stage
FROM alpine:3.19

# Install only necessary packages
RUN apk add --no-cache ca-certificates tzdata wget

# Create non-root user and group
RUN addgroup -S watchdog && adduser -S watchdog -G watchdog

WORKDIR /app

# Copy binary from builder
COPY --from=builder --chown=watchdog:watchdog /usr/src/app/watchdog .

# Use non-root user
USER watchdog

# Expose port
EXPOSE 8080

# Set healthcheck
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/v1/healthcheck || exit 1

# Run the application
ENTRYPOINT ["/app/watchdog"]