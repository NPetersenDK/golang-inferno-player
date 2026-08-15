//go:build !linux

package engine

import (
	"errors"
	"os"
)

// Named pipes with the semantics we rely on are a POSIX feature; this build
// exists so the project still compiles for local development on Windows.
func ensureFIFO(path string) (*os.File, error) {
	return nil, errors.New("pipe sources are only supported on Linux")
}
