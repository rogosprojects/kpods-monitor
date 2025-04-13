#!/bin/bash

# This script demonstrates how to build the application with a specific version

# Default version if not specified
VERSION=${1:-v2.0.0}

# Create bin directory if it doesn't exist
mkdir -p bin

echo "Building kpods-monitor version $VERSION..."

# Build the application with the specified version
go build -ldflags="-X kpods-monitor/pkg/version.Version=$VERSION" -o bin/kpods-monitor ./cmd/server

echo "Build complete. Binary is at bin/kpods-monitor"
echo "Testing version output:"
./bin/kpods-monitor --version
