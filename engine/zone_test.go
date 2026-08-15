package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"dante-player/config"
)

func newTestZone() *ZonePlayer {
	z := NewZonePlayer(config.ZoneConfig{ID: 1, Name: "Zone 1"}, make(chan struct{}, 100))
	z.status = StatusBuffering
	return z
}

func newTestSourceZone(prebufferMs int) *ZonePlayer {
	return NewZonePlayer(config.ZoneConfig{
		ID:   4,
		Name: "Line In",
		Source: &config.ZoneSource{
			Type: "pipe", Path: "/tmp/linein.pcm", Label: "Line In",
			PrebufferMs: prebufferMs,
		},
	}, make(chan struct{}, 100))
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
		Name:   "Line In",
		Source: &config.ZoneSource{Type: "pipe", Path: "/tmp/linein.pcm", Label: "Line In"},
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

	if got := z.GetState().SourceLabel; got != "Line In" {
		t.Errorf("source label %q, want Line In", got)
	}
}

// prebuffer_ms is what keeps an interactive source responsive, so it has to
// actually reach the pull path.
func TestSourcePrebufferOverride(t *testing.T) {
	z := NewZonePlayer(config.ZoneConfig{
		ID:     4,
		Source: &config.ZoneSource{Type: "pipe", Path: "/tmp/linein.pcm", PrebufferMs: 300},
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

// buffer_ms is what sets the latency you hear: a blocked producer keeps the
// queue full, so the cap is the steady state, not the prebuffer.
func TestSourceBufferCapsTheQueue(t *testing.T) {
	z := NewZonePlayer(config.ZoneConfig{
		ID: 4,
		Source: &config.ZoneSource{
			Type: "pipe", Path: "/tmp/linein.pcm",
			PrebufferMs: 300, BufferMs: 600,
		},
	}, make(chan struct{}, 100))

	if want := 600 / zoneChunkMillis; z.queueChunks != want {
		t.Errorf("queue %d chunks, want %d", z.queueChunks, want)
	}
	if got := cap(z.audioChan); got != z.queueChunks {
		t.Errorf("channel capacity %d, want %d", got, z.queueChunks)
	}
	if z.prebufferChunks >= z.queueChunks {
		t.Errorf("prebuffer %d must stay below the queue cap %d", z.prebufferChunks, z.queueChunks)
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

// A source zone is permanently attached to its producer, so a quiet producer
// means idle. Reporting "buffering" there would claim it is working towards
// playback when nothing is coming.
func TestSourceZoneIsIdleWhenProducerIsQuiet(t *testing.T) {
	z := newTestSourceZone(0)
	pushChunks(z, z.prebufferChunks)
	drainZone(t, z, z.prebufferChunks)

	for i := 0; i < zoneStallChunks; i++ {
		if _, ok := z.PullSamples(960); ok {
			t.Fatalf("empty pull %d delivered audio", i)
		}
	}
	if got := z.GetState().Status; got != StatusIdle {
		t.Errorf("status %q with a quiet producer, want %q", got, StatusIdle)
	}

	// An idle source zone must keep pulling, or it could never come back.
	pushChunks(z, z.prebufferChunks)
	if _, ok := z.PullSamples(960); !ok {
		t.Fatal("idle source zone did not resume when the producer returned")
	}
	if got := z.GetState().Status; got != StatusPlaying {
		t.Errorf("status %q after the producer returned, want %q", got, StatusPlaying)
	}
}

// Partial data means it really is buffering, for either kind of zone.
func TestSourceZoneBuffersWhileFilling(t *testing.T) {
	z := newTestSourceZone(0)
	pushChunks(z, 1)

	if _, ok := z.PullSamples(960); ok {
		t.Fatal("delivered below the prebuffer")
	}
	if got := z.GetState().Status; got != StatusBuffering {
		t.Errorf("status %q while filling, want %q", got, StatusBuffering)
	}
}

// Backpressure is the only thing pacing a pipe producer. Without it the
// producer runs at whatever speed we can drain, which is not realtime.
func TestEnqueueBlocksWhenBackpressureRequested(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zoneQueueChunks) // queue full

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- z.enqueue(ctx, make([]int32, 1920), true) }()

	select {
	case <-done:
		t.Fatal("enqueue returned on a full queue instead of blocking the producer")
	case <-time.After(50 * time.Millisecond):
	}

	// Draining one chunk must let it through.
	<-z.audioChan
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enqueue failed after the queue drained: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue stayed blocked after the queue drained")
	}
	cancel()
}

// A live station cannot be told to wait, so a full queue must drop instead.
func TestEnqueueDropsOldestWithoutBackpressure(t *testing.T) {
	z := newTestZone()
	pushChunks(z, zoneQueueChunks)

	done := make(chan error, 1)
	go func() { done <- z.enqueue(context.Background(), make([]int32, 1920), false) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full queue instead of dropping the oldest chunk")
	}
	if got := len(z.audioChan); got != zoneQueueChunks {
		t.Errorf("queue holds %d chunks, want it still full at %d", got, zoneQueueChunks)
	}
}

// Every tuner method has to be safe on a nil receiver, since that is what an
// unconfigured tuner is.
func TestNilTunerIsInert(t *testing.T) {
	var tuner *Tuner

	if st := tuner.State(); st.Enabled || st.Presets == nil {
		t.Errorf("nil tuner reported %+v, want disabled with an empty preset list", st)
	}
	if err := tuner.TunePreset("p3"); err == nil {
		t.Error("TunePreset on a nil tuner should report that it is not enabled")
	}
	if err := tuner.TuneFrequency(96_500_000); err == nil {
		t.Error("TuneFrequency on a nil tuner should report that it is not enabled")
	}
	tuner.Off() // must not panic
}

// rtl_fm's -d takes an index or a serial, so the setting is a string and an
// empty one must still select the first device rather than passing "".
func TestTunerDeviceArg(t *testing.T) {
	if got := (&Tuner{}).deviceArg(); got != "0" {
		t.Errorf("unset device gave %q, want %q", got, "0")
	}
	tuner := &Tuner{cfg: config.TunerConfig{Device: "dante-fm"}}
	if got := tuner.deviceArg(); got != "dante-fm" {
		t.Errorf("device %q, want the configured serial", got)
	}
}

func TestTuneFrequencyRejectsOutOfRange(t *testing.T) {
	tuner := &Tuner{}
	if err := tuner.TuneFrequency(1_000); err == nil {
		t.Error("accepted a frequency below the tuner's range")
	}
	if err := tuner.TuneFrequency(3_000_000_000); err == nil {
		t.Error("accepted a frequency above the tuner's range")
	}
}

// Writing megahertz into a hertz field is the easy mistake, and a preset must
// be checked as strictly as a typed-in frequency.
func TestPresetFrequencyIsValidated(t *testing.T) {
	tuner := &Tuner{cfg: config.TunerConfig{
		Presets: []config.TunerPreset{{ID: "p4", Name: "P4", FrequencyHz: 964_000}},
	}}

	err := tuner.TunePreset("p4")
	if err == nil {
		t.Fatal("preset with 964000 Hz was accepted")
	}
	if !strings.Contains(err.Error(), "p4") {
		t.Errorf("error %q does not name the offending preset", err)
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
