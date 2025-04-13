#!/bin/bash
set -e

# Default version if not specified
VERSION=${1:-$(git describe --tags --always || echo "v2.0.0")}
BUILD_DATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
COMMIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo "Building Docker image with:"
echo "  Version:    $VERSION"
echo "  Build date: $BUILD_DATE"
echo "  Commit:     $COMMIT_SHA"
echo

# Build the Docker image
docker build \
  --build-arg VERSION="$VERSION" \
  --build-arg BUILD_DATE="$BUILD_DATE" \
  --build-arg COMMIT_SHA="$COMMIT_SHA" \
  -t kpods-monitor:$VERSION \
  -t kpods-monitor:latest \
  .

echo
echo "Build complete!"
echo "You can run the container with:"
echo "  docker run -p 8080:8080 -v \$HOME/.kube/config:/app/.kube/config -v \$(pwd)/config.yaml:/app/config.yaml kpods-monitor:latest"
