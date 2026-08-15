//go:build linux

package engine

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ensureFIFO creates path as a named pipe if missing, and returns a descriptor
// held open read-write for as long as the zone lives.
//
// The keepalive descriptor is the point: a FIFO reports EOF once the last
// writer closes, which happens every time the producer pauses. Holding a writer
// reference makes the reader block instead. We never read from it.
func ensureFIFO(path string) (*os.File, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeNamedPipe == 0 {
			// A producer that started first will have created a plain file
			// here. Log it, since it also catches a misconfigured path.
			log.Printf("[Source] %s exists as a regular file, replacing it with a FIFO", path)
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("replace %s with a FIFO: %w", path, err)
			}
			return createFIFO(path)
		}
	case os.IsNotExist(err):
		return createFIFO(path)
	default:
		return nil, err
	}

	return os.OpenFile(path, os.O_RDWR, 0)
}

func createFIFO(path string) (*os.File, error) {
	if err := syscall.Mkfifo(path, 0o666); err != nil {
		return nil, fmt.Errorf("mkfifo %s: %w", path, err)
	}
	// Mkfifo is subject to the umask, and the producer runs as another user.
	_ = os.Chmod(path, 0o666)
	return os.OpenFile(path, os.O_RDWR, 0)
}

// F_LINUX_SPECIFIC_BASE + 7. Architecture independent, but the syscall package
// does not expose it.
const fSetPipeSz = 1031

// shrinkPipe reduces the kernel buffer behind a FIFO, which backpressure
// otherwise keeps full and turns into latency.
//
// Best effort: the kernel rounds up to a page and refuses to shrink below what
// is already buffered.
func shrinkPipe(f *os.File, bytes int) {
	rc, err := f.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, fd, fSetPipeSz, uintptr(bytes))
	})
}
