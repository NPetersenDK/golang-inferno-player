//go:build linux

package engine

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ensureFIFO creates path as a named pipe if missing. The returned descriptor is
// held open O_RDWR for the zone's lifetime and never read from: a FIFO reports
// EOF once the last writer closes, and holding a writer makes readers block instead.
func ensureFIFO(path string) (*os.File, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeNamedPipe == 0 {
			// A producer that started first leaves a plain file here.
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

// F_SETPIPE_SZ: F_LINUX_SPECIFIC_BASE + 7, architecture independent, but not
// exposed by the syscall package.
const fSetPipeSz = 1031

// shrinkPipe trims the kernel buffer behind a FIFO, which backpressure otherwise
// keeps full and turns into latency. Best effort: the kernel rounds up to a page
// and refuses to shrink below what is already buffered.
func shrinkPipe(f *os.File, bytes int) {
	rc, err := f.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, fd, fSetPipeSz, uintptr(bytes))
	})
}
