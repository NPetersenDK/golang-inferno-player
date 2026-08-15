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

// Re-priming on every dip is what made the zone flap between playing and
// buffering: the queue level drifts freely, so brief shortfalls are normal and
// must cost one 20 ms hole rather than a second of silence.
func TestZoneRidesOutBriefShortfall(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zonePrebufferChunks)
	drainZone(t, z, zonePrebufferChunks)

	if _, ok := z.PullSamples(960); ok {
		t.Fatal("zone delivered from an empty queue")
	}
	if got := z.StarvationCount(); got != 1 {
		t.Errorf("starvation count %d, want 1", got)
	}
	if got := z.GetState().Status; got != StatusPlaying {
		t.Errorf("status %q after one empty pull, want %q", got, StatusPlaying)
	}

	// One chunk arriving is enough to carry on, because we never left playing.
	pushChunks(z, 1)
	if _, ok := z.PullSamples(960); !ok {
		t.Fatal("zone did not resume after the queue refilled")
	}
}

func TestZoneDeclaresStallAfterSustainedGap(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zonePrebufferChunks)
	drainZone(t, z, zonePrebufferChunks)

	for i := 0; i < zoneStallChunks; i++ {
		if _, ok := z.PullSamples(960); ok {
			t.Fatalf("empty pull %d delivered audio", i)
		}
	}

	if got := z.GetState().Status; got != StatusBuffering {
		t.Errorf("status %q after a sustained gap, want %q", got, StatusBuffering)
	}

	// Back to buffering means the full prebuffer has to be rebuilt.
	pushChunks(z, 1)
	if _, ok := z.PullSamples(960); ok {
		t.Fatal("stalled zone resumed on a single chunk")
	}
}

func drainZone(t *testing.T, z *ZonePlayer, chunks int) {
	t.Helper()
	for i := 0; i < chunks; i++ {
		if _, ok := z.PullSamples(960); !ok {
			t.Fatalf("delivery %d failed with a full prebuffer", i)
		}
	}
}

// A source zone belongs to its producer: the station browser must not be able
// to take it over, and Stop All must not silence it.
func TestSourceZoneRejectsStationControl(t *testing.T) {
	z := NewZonePlayer(config.ZoneConfig{
		ID:     4,
		Name:   "Spotify",
		Source: &config.ZoneSource{Type: "pipe", Path: "/tmp/spotify.pcm", Label: "Spotify"},
	}, make(chan struct{}, 100))

	if !z.IsSource() {
		t.Fatal("zone with a source block did not report as a source")
	}
	if err := z.Play(nil, "http://example.com/stream.mp3", "Some Station"); err == nil {
		t.Error("Play on a source zone was accepted")
	}

	z.status = StatusPlaying
	z.Stop()
	if got := z.GetState().Status; got != StatusPlaying {
		t.Errorf("status %q after Stop on a source zone, want it untouched (%q)", got, StatusPlaying)
	}

	if got := z.GetState().SourceLabel; got != "Spotify" {
		t.Errorf("source label %q, want Spotify", got)
	}
}

// prebuffer_ms is what keeps an interactive source responsive, so it has to
// actually reach the pull path.
func TestSourcePrebufferOverride(t *testing.T) {
	z := NewZonePlayer(config.ZoneConfig{
		ID:     4,
		Source: &config.ZoneSource{Type: "pipe", Path: "/tmp/spotify.pcm", PrebufferMs: 300},
	}, make(chan struct{}, 100))

	if want := 300 / zoneChunkMillis; z.prebufferChunks != want {
		t.Fatalf("prebuffer %d chunks, want %d", z.prebufferChunks, want)
	}

	z.status = StatusBuffering
	pushChunks(z, z.prebufferChunks-1)
	if _, ok := z.PullSamples(960); ok {
		t.Error("delivered below the configured prebuffer")
	}
	pushChunks(z, 1)
	if _, ok := z.PullSamples(960); !ok {
		t.Error("did not deliver once the configured prebuffer was reached")
	}
}

func TestZoneWithoutSourceIsUnchanged(t *testing.T) {
	z := newTestZone()
	if z.IsSource() {
		t.Error("plain zone reported as a source")
	}
	if z.prebufferChunks != zonePrebufferChunks {
		t.Errorf("prebuffer %d, want the default %d", z.prebufferChunks, zonePrebufferChunks)
	}
	if st := z.GetState(); st.IsSource || st.SourceLabel != "" {
		t.Errorf("plain zone exposed source fields: %+v", st)
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
