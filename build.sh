#!/usr/bin/env bash
set -euo pipefail

BUILD_DIR="build"
LDFLAGS="-s -w"

bash scripts/sync-version.sh

if [ ! -s internal/web/dist/index.html ] || [ -z "$(find internal/web/dist/assets -type f -print -quit 2>/dev/null)" ]; then
    echo "ERROR: internal/web/dist does not contain a complete frontend snapshot" >&2
    exit 1
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR/release/linux" "$BUILD_DIR/release/windows"

echo "Building RelayDock Backend for linux/amd64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$LDFLAGS" \
    -o "$BUILD_DIR/arcway-linux-amd64" ./cmd/server

echo "Building expiry guards for linux/amd64 and linux/arm64..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$LDFLAGS" \
    -o "$BUILD_DIR/arcway-expiry-guard-linux-amd64" ./cmd/arcway-expiry-guard
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$LDFLAGS" \
    -o "$BUILD_DIR/arcway-expiry-guard-linux-arm64" ./cmd/arcway-expiry-guard

echo "Building RelayDock Backend for windows/amd64..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$LDFLAGS" \
    -o "$BUILD_DIR/arcway-windows-amd64.exe" ./cmd/server

cp "$BUILD_DIR/arcway-linux-amd64" "$BUILD_DIR/release/linux/"
cp "$BUILD_DIR/arcway-windows-amd64.exe" "$BUILD_DIR/release/windows/"
cp "$BUILD_DIR/arcway-expiry-guard-linux-amd64" "$BUILD_DIR/release/linux/"
cp "$BUILD_DIR/arcway-expiry-guard-linux-arm64" "$BUILD_DIR/release/linux/"
cp "$BUILD_DIR/arcway-expiry-guard-linux-amd64" "$BUILD_DIR/release/windows/"
cp "$BUILD_DIR/arcway-expiry-guard-linux-arm64" "$BUILD_DIR/release/windows/"
chmod +x "$BUILD_DIR/release/linux/arcway-linux-amd64"
chmod +x "$BUILD_DIR/release/linux/arcway-expiry-guard-linux-amd64"
chmod +x "$BUILD_DIR/release/linux/arcway-expiry-guard-linux-arm64"

echo "Build complete:"
echo "  $BUILD_DIR/arcway-linux-amd64"
echo "  $BUILD_DIR/arcway-windows-amd64.exe"
