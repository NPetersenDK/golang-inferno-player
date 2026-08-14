#!/usr/bin/env bash
# ==============================================================================
# Cross-Compilation Script for Raspberry Pi 64-bit (aarch64-unknown-linux-gnu)
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_DIR="$ROOT_DIR/dist/rpi-arm64"

mkdir -p "$OUTPUT_DIR"

echo "=== Cross-compiling Dante Web Player (Go) for ARM64 ==="
cd "$ROOT_DIR/golang-infero-player"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$OUTPUT_DIR/dante-player" .
echo "Created: $OUTPUT_DIR/dante-player"

echo "=== Cross-compiling Inferno Dante Player (Rust) for aarch64 ==="
cd "$ROOT_DIR/inferno"
if command -v cross &> /dev/null; then
    cross build --release --target=aarch64-unknown-linux-gnu -p inferno_player
    cp target/aarch64-unknown-linux-gnu/release/inferno_player "$OUTPUT_DIR/"
    echo "Created: $OUTPUT_DIR/inferno_player"
else
    echo "Note: 'cross' tool not installed. Install with: cargo install cross"
    echo "Or compile directly on your Raspberry Pi using deploy/setup-rpi.sh"
fi

echo "=== Copying Service Configurations ==="
cp "$SCRIPT_DIR/statime.service" "$OUTPUT_DIR/"
cp "$SCRIPT_DIR/inferno-dante.service" "$OUTPUT_DIR/"
cp "$SCRIPT_DIR/dante-player.service" "$OUTPUT_DIR/"
cp "$SCRIPT_DIR/inferno-ptpv1.toml" "$OUTPUT_DIR/"

echo "=== Output package ready in $OUTPUT_DIR ==="
