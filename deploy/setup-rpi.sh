#!/usr/bin/env bash
# ==============================================================================
# Dante Audio Hub Setup Script for Raspberry Pi (Debian/Raspbian 64-bit)
# ==============================================================================

set -e

echo "=== Installing Dependencies for Dante & Inferno ==="
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  pkg-config \
  libasound2-dev \
  ffmpeg \
  curl \
  git \
  ca-certificates

# Ensure Rust is installed if building on device
if ! command -v cargo &> /dev/null; then
    echo "=== Installing Rust ==="
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
    source "$HOME/.cargo/env"
fi

# Ensure Go is installed if building on device
if ! command -v go &> /dev/null; then
    echo "=== Installing Go ==="
    sudo apt-get install -y golang-go || true
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "=== Building Inferno Dante Engine ==="
cd "$ROOT_DIR/inferno"
cargo build --release -p inferno_player
sudo cp target/release/inferno_player /usr/local/bin/

echo "=== Building Statime (Dante Clock Daemon) ==="
if [ ! -d "/tmp/statime" ]; then
    git clone --recurse-submodules -b inferno-dev https://github.com/teodly/statime /tmp/statime
fi
cd /tmp/statime
cargo build --release
sudo cp target/release/statime /usr/local/bin/

echo "=== Building Dante Web Player (Go) ==="
cd "$ROOT_DIR/golang-infero-player"
CGO_ENABLED=0 go build -o dante-player .
sudo cp dante-player /usr/local/bin/

echo "=== Configuring Services & Directories ==="
sudo mkdir -p /etc/inferno
sudo mkdir -p /opt/dante-player/data
sudo mkdir -p /tmp/dante_player
sudo chmod 777 /tmp/dante_player

# Auto-detect default network interface for Statime
PRIMARY_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -z "$PRIMARY_IFACE" ]; then
    PRIMARY_IFACE="eth0"
fi
echo "Detected primary network interface: $PRIMARY_IFACE"

# Copy and configure statime config
sudo cp "$SCRIPT_DIR/inferno-ptpv1.toml" /etc/inferno/inferno-ptpv1.toml
sudo sed -i "s/interface = \"eth0\"/interface = \"$PRIMARY_IFACE\"/g" /etc/inferno/inferno-ptpv1.toml

# Copy systemd units
sudo cp "$SCRIPT_DIR/statime.service" /etc/systemd/system/
sudo cp "$SCRIPT_DIR/inferno-dante.service" /etc/systemd/system/
sudo cp "$SCRIPT_DIR/dante-player.service" /etc/systemd/system/

echo "=== Reloading & Starting Systemd Services ==="
sudo systemctl daemon-reload
sudo systemctl enable --now statime.service
sudo systemctl enable --now inferno-dante.service
sudo systemctl enable --now dante-player.service

echo ""
echo "=========================================================================="
echo "  DANTE AUDIO HUB INSTALLED SUCCESSFULLY!"
echo "=========================================================================="
echo "  Web Interface: http://$(hostname -I | awk '{print $1}'):8080"
echo "  Dante Device:  Dante-Pi"
echo "  Status:        PTP Clock Synced & Dante Flows Active"
echo "=========================================================================="
