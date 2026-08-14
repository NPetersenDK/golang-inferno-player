#!/usr/bin/env bash
set -e

echo "=================================================="
echo " Starting Dante Audio Hub Container (via Inferno) "
echo "=================================================="

# 1. Detect or use specified network interface for Dante (e.g. dedicated USB NIC)
DANTE_IFACE="${DANTE_INTERFACE:-${INFERNO_BIND_IP:-}}"

if [ -z "$DANTE_IFACE" ]; then
    # Auto-detect default active interface
    DANTE_IFACE="$(ip route | grep default | awk '{print $5}' | head -n1)"
    if [ -z "$DANTE_IFACE" ]; then
        DANTE_IFACE="eth0"
    fi
    echo "[Network] Auto-detected primary network interface: $DANTE_IFACE"
else
    echo "[Network] Using configured Dante interface: $DANTE_IFACE (e.g. dedicated USB adapter)"
fi

# Verify interface exists
if ip link show "$DANTE_IFACE" >/dev/null 2>&1; then
    IFACE_IP="$(ip -4 addr show "$DANTE_IFACE" 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | head -n1 || true)"
    echo "[Network] Interface $DANTE_IFACE is UP. IP: ${IFACE_IP:-<no IPv4 yet - waiting for DHCP/Link-Local>}"
else
    echo "[Network] WARNING: Interface $DANTE_IFACE not found yet in system. Available interfaces:"
    ip -brief link
fi

mkdir -p /etc/inferno /tmp/dante_player /opt/dante-player/data /root/.local/state/inferno_aoip /run
chmod 777 /tmp/dante_player /run

# 2. Configure and bind Statime & Inferno to the chosen interface
export INFERNO_BIND_IP="$DANTE_IFACE"
sed -i "s/interface = \".*\"/interface = \"$DANTE_IFACE\"/g" /etc/inferno/inferno-ptpv1.toml

# 3. Start Statime PTP clock daemon in background on Dante interface
echo "[PTP] Starting Statime clock daemon on interface $DANTE_IFACE..."
statime -c /etc/inferno/inferno-ptpv1.toml &
STATIME_PID=$!

# Trap signals for graceful shutdown
cleanup() {
    echo "Shutting down Dante services..."
    kill $STATIME_PID 2>/dev/null || true
    exit 0
}
trap cleanup SIGINT SIGTERM

# Give Statime PTP clock daemon a moment to lock and open usrvclock socket
sleep 2

# 4. Start Go Dante Web Player
echo "[Web Player] Starting Dante Web Player on port ${HTTP_PORT:-8085}..."
exec dante-player -port "${HTTP_PORT:-8085}" -pipe-dir /tmp/dante_player -dante-name "${INFERNO_NAME:-Dante-Pi}" -config "/opt/dante-player/config.yaml"
