//go:build !linux

package engine

import (
	"errors"
	"os"
)

// Stubs so the project still builds off Linux for local development.
func ensureFIFO(path string) (*os.File, error) {
	return nil, errors.New("pipe sources are only supported on Linux")
}

func shrinkPipe(f *os.File, bytes int) {}
