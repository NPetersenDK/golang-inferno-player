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

mkdir -p /tmp/dante_player /opt/dante-player/data /root/.local/state/inferno_aoip /run
chmod 777 /tmp/dante_player /run

# 2. Bind Inferno to the chosen interface. dante-player measures the Dante
# grandmaster itself over PTPv1 and serves the media clock on this socket, so
# there is no separate PTP daemon to start.
export INFERNO_BIND_IP="$DANTE_IFACE"
export INFERNO_CLOCK_PATH="/tmp/usrvclock"
export DANTE_PTP_IFACE="$DANTE_IFACE"

# Give ourselves a device ID in the form every real Dante device uses: the
# interface MAC widened to EUI-64 by inserting fffe. Inferno otherwise builds
# one from the IP address, which makes us the only device on the segment with a
# malformed ID, and means a new DHCP lease looks like a brand new device and
# loses its routing. Derived from our own OUI, so it cannot collide with an
# Audinate one. Set INFERNO_DEVICE_ID yourself to override.
if [ -z "$INFERNO_DEVICE_ID" ]; then
    IFACE_MAC="$(tr -d ':' < "/sys/class/net/$DANTE_IFACE/address" 2>/dev/null || true)"
    if [ ${#IFACE_MAC} -eq 12 ]; then
        export INFERNO_DEVICE_ID="${IFACE_MAC%??????}fffe${IFACE_MAC#??????}"
        echo "[Dante] Device ID $INFERNO_DEVICE_ID (EUI-64 from $DANTE_IFACE)"
    else
        echo "[Dante] WARNING: could not read $DANTE_IFACE MAC; Inferno will derive a device ID from the IP"
    fi
fi

# Channel count has to agree between the ALSA plugin and the mixer, so one
# variable drives both. Each channel costs a set of mDNS records every device on
# the segment has to cache, so fewer channels means a smaller footprint.
DANTE_TX_CHANNELS="${DANTE_TX_CHANNELS:-8}"
case "$DANTE_TX_CHANNELS" in
    2|4|6|8)
        sed -i "s/^\( *TX_CHANNELS \).*/\1$DANTE_TX_CHANNELS/" /etc/asound.conf
        export DANTE_TX_CHANNELS
        echo "[Dante] Advertising $DANTE_TX_CHANNELS TX channels"
        ;;
    *)
        echo "[Dante] WARNING: DANTE_TX_CHANNELS=$DANTE_TX_CHANNELS is not one of 2/4/6/8, keeping 8"
        DANTE_TX_CHANNELS=8
        export DANTE_TX_CHANNELS
        ;;
esac

# Inferno persists its TX flows and restores them without checking them against
# the current channel count, so a flow left over from a wider configuration
# panics the 'flows TX' thread with an out-of-bounds channel index. Drop the
# saved state whenever the count changes; the routing has to be redone anyway.
STATE_DIR="/root/.local/state/inferno_aoip"
CHANNEL_STAMP="$STATE_DIR/.tx_channels"
if [ -f "$CHANNEL_STAMP" ] && [ "$(cat "$CHANNEL_STAMP")" != "$DANTE_TX_CHANNELS" ]; then
    echo "[Dante] Channel count changed $(cat "$CHANNEL_STAMP") -> $DANTE_TX_CHANNELS, clearing saved flows"
    rm -f "$STATE_DIR"/*
fi
printf '%s' "$DANTE_TX_CHANNELS" > "$CHANNEL_STAMP"

# Ensure kernel routes multicast (Dante PTP 224.0.1.129, mDNS 224.0.0.251, Audio 239.255.0.0/16) to Dante NIC
ip link set "$DANTE_IFACE" multicast on 2>/dev/null || true
ip link set "$DANTE_IFACE" allmulticast on 2>/dev/null || true
sysctl -w "net.ipv4.conf.$DANTE_IFACE.rp_filter=0" 2>/dev/null || true
sysctl -w net.ipv4.conf.all.rp_filter=0 2>/dev/null || true
ip route add 224.0.0.0/4 dev "$DANTE_IFACE" 2>/dev/null || true

# 3. Start Go Dante Web Player. It holds the transmitter back until its PTPv1
# listener has locked onto the grandmaster, so no startup delay is needed here.
echo "[Web Player] Starting Dante Web Player on port ${HTTP_PORT:-8085}..."
exec dante-player -port "${HTTP_PORT:-8085}" -pipe-dir /tmp/dante_player -dante-name "${INFERNO_NAME:-Dante-Pi}" -config "/opt/dante-player/config.yaml"

