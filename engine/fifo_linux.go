//go:build linux

package engine

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ensureFIFO creates path as a named pipe if it is missing, and returns a
// descriptor held open read-write for as long as the zone lives.
//
// That keepalive descriptor is the whole point. A FIFO reports EOF to its
// reader as soon as the last writer closes, which happens every time the
// producer stops - each time you pause Spotify, for instance. Holding a writer
// reference open means the reader blocks waiting for more data instead, so a
// producer that comes and goes never tears the zone down. We never read from
// this descriptor, so it takes no data away from the real reader.
func ensureFIFO(path string) (*os.File, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeNamedPipe == 0 {
			// A producer that started before us and opened the path for writing
			// will have created a plain file instead. The path is dedicated to
			// this zone, so replacing it is the right repair - but say so,
			// because it also catches a genuine misconfiguration.
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
	// Mkfifo is subject to the umask, and the producer normally runs in a
	// different container under a different user.
	_ = os.Chmod(path, 0o666)
	return os.OpenFile(path, os.O_RDWR, 0)
}
