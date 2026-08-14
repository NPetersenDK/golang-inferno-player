//go:build unix

package engine

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func createFifoIfNeeded(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode()&os.ModeNamedPipe != 0 {
			return
		}
		_ = os.Remove(path)
	}
	_ = syscall.Mkfifo(path, 0666)
}

func openPipeWriter(path string) (io.WriteCloser, error) {
	// Open pipe for writing
	return os.OpenFile(path, os.O_WRONLY, 0666)
}
