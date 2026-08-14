#!/usr/bin/env bash
set -e

echo "=================================================="
echo " Starting Dante Audio Hub Container (via Inferno) "
echo "=================================================="

# 1. Detect primary network interface if not specified
PRIMARY_IFACE="${DANTE_INTERFACE:-$(ip route | grep default | awk '{print $5}' | head -n1)}"
if [ -z "$PRIMARY_IFACE" ]; then
    PRIMARY_IFACE="eth0"
fi
echo "Using network interface: $PRIMARY_IFACE"

mkdir -p /etc/inferno /tmp/dante_player /opt/dante-player/data /root/.local/state/inferno_aoip
chmod 777 /tmp/dante_player

# Update network interface in statime config
sed -i "s/interface = \".*\"/interface = \"$PRIMARY_IFACE\"/g" /etc/inferno/inferno-ptpv1.toml

# 2. Start Statime PTP clock daemon in background
echo "Starting Statime PTP clock daemon..."
statime -c /etc/inferno/inferno-ptpv1.toml &
STATIME_PID=$!

# Trap signals for graceful shutdown
cleanup() {
    echo "Shutting down Dante services..."
    kill $STATIME_PID 2>/dev/null || true
    exit 0
}
trap cleanup SIGINT SIGTERM

sleep 1

# 3. Start Go Dante Web Player
echo "Starting Dante Web Player on port ${HTTP_PORT:-8080}..."
exec dante-player -port "${HTTP_PORT:-8080}" -pipe-dir /tmp/dante_player -dante-name "${INFERNO_NAME:-Dante-Pi}"
