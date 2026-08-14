//go:build !linux

package engine

import (
	"net"
	"time"
)

// Kernel receive timestamps are a Linux socket feature; elsewhere (only
// relevant for cross-compiling and local development) we fall back to the
// userspace arrival time.
func enableRxTimestamps(c *net.UDPConn) bool { return false }

func readPacketWithTimestamp(c *net.UDPConn, buf, oob []byte) (n int, rxNs int64, err error) {
	n, _, err = c.ReadFromUDP(buf)
	if err != nil {
		return 0, 0, err
	}
	return n, time.Now().UnixNano(), nil
}
