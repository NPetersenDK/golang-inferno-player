package engine

import (
	"testing"

	"dante-player/config"
)

func newTestZone() *ZonePlayer {
	z := NewZonePlayer(config.ZoneConfig{ID: 1, Name: "Zone 1"}, make(chan struct{}, 100))
	z.status = StatusBuffering
	return z
}

func pushChunks(z *ZonePlayer, n int) {
	for i := 0; i < n; i++ {
		z.audioChan <- make([]int32, 1920) // 960 stereo frames
	}
}

func TestZoneWaitsForPrebuffer(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zonePrebufferChunks-1)

	if _, ok := z.PullSamples(960); ok {
		t.Fatal("zone delivered audio before its prebuffer was full")
	}
	if got := z.GetState().Status; got != StatusBuffering {
		t.Errorf("status %q while filling the prebuffer, want %q", got, StatusBuffering)
	}

	pushChunks(z, 1)
	if _, ok := z.PullSamples(960); !ok {
		t.Fatal("zone did not deliver once the prebuffer was full")
	}
	if got := z.GetState().Status; got != StatusPlaying {
		t.Errorf("status %q after priming, want %q", got, StatusPlaying)
	}
}

// Handing out the one chunk that arrives after a dropout would just glitch
// again on the next pull, so the zone has to rebuild its whole prebuffer.
func TestZoneRebuildsPrebufferAfterStarving(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zonePrebufferChunks)

	for i := 0; i < zonePrebufferChunks; i++ {
		if _, ok := z.PullSamples(960); !ok {
			t.Fatalf("delivery %d failed with a full prebuffer", i)
		}
	}

	if _, ok := z.PullSamples(960); ok {
		t.Fatal("zone delivered from an empty queue")
	}
	if got := z.StarvationCount(); got != 1 {
		t.Errorf("starvation count %d, want 1", got)
	}
	if got := z.GetState().Status; got != StatusBuffering {
		t.Errorf("status %q after starving, want %q", got, StatusBuffering)
	}

	pushChunks(z, 1)
	if _, ok := z.PullSamples(960); ok {
		t.Fatal("zone resumed on a single chunk instead of rebuilding the prebuffer")
	}
}

func TestIdleZoneDeliversNothing(t *testing.T) {
	z := newTestZone()
	z.status = StatusIdle
	pushChunks(z, zonePrebufferChunks)

	if _, ok := z.PullSamples(960); ok {
		t.Fatal("idle zone delivered audio")
	}
	if got := z.StarvationCount(); got != 0 {
		t.Errorf("starvation count %d for an idle zone, want 0", got)
	}
}
