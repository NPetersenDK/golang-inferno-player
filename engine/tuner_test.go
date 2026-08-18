//go:build unix

package engine

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opening a FIFO for writing blocks until the zone has its end open, which is
// exactly what happens while a zone restarts. Held under the state lock, that
// took /api/status down with it and the UI sat at "Connecting to Dante
// engine..." until the container was restarted.
func TestTuneWaitingOnTheFIFODoesNotBlockState(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fm.pcm")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo is unavailable here: %v", err)
	}

	defer func(d time.Duration) { fifoOpenTimeout = d }(fifoOpenTimeout)
	fifoOpenTimeout = 200 * time.Millisecond

	tuner := &Tuner{fifoPath: fifo, zoneID: 4}
	tuned := make(chan error, 1)
	go func() { tuned <- tuner.tune("", 94_400_000) }()

	// Nothing reads the FIFO, so the tune is now stuck in the open.
	time.Sleep(50 * time.Millisecond)

	state := make(chan TunerState, 1)
	go func() { state <- tuner.State() }()
	select {
	case <-state:
	case <-time.After(2 * time.Second):
		t.Fatal("State() blocked behind a tune waiting on the FIFO")
	}

	select {
	case err := <-tuned:
		if err == nil {
			t.Fatal("tune reported success with nothing reading the FIFO")
		}
		if !strings.Contains(err.Error(), fifo) {
			t.Errorf("the error does not name the FIFO: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tune never gave up on the FIFO")
	}
}

// Killing without reaping is deliberate: the goroutine tune starts owns Wait.
func TestStopWithoutASessionIsHarmless(t *testing.T) {
	tuner := &Tuner{fifoPath: "/nonexistent", zoneID: 4}
	tuner.stop()
	tuner.stop()
	if st := tuner.State(); st.Tuned {
		t.Error("stop left the tuner reporting that it is tuned")
	}
}
