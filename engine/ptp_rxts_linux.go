//go:build linux

package engine

import (
	"net"
	"syscall"
	"time"
	"unsafe"
)

// enableRxTimestamps asks the kernel to attach a CLOCK_REALTIME receive
// timestamp to every datagram. Taking the arrival time in the softirq path
// instead of after the Go scheduler wakes us removes the largest error source
// in the offset measurement.
func enableRxTimestamps(c *net.UDPConn) bool {
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	if err := rc.Control(func(fd uintptr) {
		ok = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_TIMESTAMPNS, 1) == nil
	}); err != nil {
		return false
	}
	return ok
}

func readPacketWithTimestamp(c *net.UDPConn, buf, oob []byte) (n int, rxNs int64, err error) {
	n, oobn, _, _, err := c.ReadMsgUDP(buf, oob)
	if err != nil {
		return 0, 0, err
	}
	rxNs = time.Now().UnixNano()
	if oobn == 0 {
		return n, rxNs, nil
	}
	msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn])
	if perr != nil {
		return n, rxNs, nil
	}
	for _, msg := range msgs {
		// SCM_TIMESTAMPNS has the same numeric value as SO_TIMESTAMPNS, which
		// is the one the syscall package exposes.
		if msg.Header.Level != syscall.SOL_SOCKET || msg.Header.Type != syscall.SO_TIMESTAMPNS {
			continue
		}
		if len(msg.Data) < int(unsafe.Sizeof(syscall.Timespec{})) {
			continue
		}
		ts := (*syscall.Timespec)(unsafe.Pointer(&msg.Data[0]))
		return n, ts.Nano(), nil
	}
	return n, rxNs, nil
}
