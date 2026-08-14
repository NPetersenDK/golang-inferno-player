//go:build !unix

package engine

import (
	"io"
	"os"
	"path/filepath"
)

func createFifoIfNeeded(path string) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
}

func openPipeWriter(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
}
