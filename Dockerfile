# Build stage
FROM golang:1.24-alpine AS builder

# Set build arguments
ARG VERSION=v2.0.0
ARG BUILD_DATE=unknown
ARG COMMIT_SHA=unknown

# Set working directory
WORKDIR /app

# Install build dependencies
RUN apk --no-cache add git ca-certificates tzdata

# Copy go module files first for better layer caching
COPY go.mod go.sum* ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application with version information
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X kpods-monitor/pkg/version.Version=${VERSION} -X kpods-monitor/pkg/version.BuildDate=${BUILD_DATE} -X kpods-monitor/pkg/version.CommitSHA=${COMMIT_SHA}" \
    -o kpods-monitor ./cmd/server

# Runtime stage
FROM alpine:3.19

# Add labels for better container metadata
LABEL org.opencontainers.image.title="Kubernetes Pod Monitor" \
      org.opencontainers.image.description="A dashboard for monitoring Kubernetes applications and pods" \
      org.opencontainers.image.source="https://github.com/user/kpods-monitor" \
      org.opencontainers.image.vendor="Your Organization" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.revision="${COMMIT_SHA}"

# Create a non-root user and group
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Set working directory
WORKDIR /app

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata curl

# Copy the binary from the builder stage
COPY --from=builder --chown=appuser:appgroup /app/kpods-monitor /app/kpods-monitor

# Copy the UI files
COPY --from=builder --chown=appuser:appgroup /app/ui/public /app/ui/public


# Expose the port the server listens on
EXPOSE 8080

# Switch to non-root user
USER appuser

# Add health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8080/health || exit 1

# Run the application
CMD ["/app/kpods-monitor", "--config", "/app/config.yaml"]
