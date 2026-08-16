//go:build !linux

package engine

import (
	"net"
	"time"
)

// Off Linux there are no kernel receive timestamps; fall back to userspace
// arrival time.
func enableRxTimestamps(c *net.UDPConn) bool { return false }

func readPacketWithTimestamp(c *net.UDPConn, buf, oob []byte) (n int, rxNs int64, err error) {
	n, _, err = c.ReadFromUDP(buf)
	if err != nil {
		return 0, 0, err
	}
	return n, time.Now().UnixNano(), nil
}
