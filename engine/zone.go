package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"dante-player/config"
)

type PlaybackStatus string

const (
	StatusIdle      PlaybackStatus = "idle"
	StatusBuffering PlaybackStatus = "buffering"
	StatusPlaying   PlaybackStatus = "playing"
	StatusError     PlaybackStatus = "error"
)

// Buffering geometry, in 20 ms chunks.
//
// A source and the mixer run on unrelated oscillators, so the queue level is a
// random walk with no restoring force. Headroom has to exceed the jitter of the
// source, which for an internet stream means seconds, not milliseconds.
const (
	zoneQueueChunks     = 150 // 3 s, caps latency and drops the oldest when full
	zonePrebufferChunks = 50  // 1 s of slack in either direction
	zoneChunkMillis     = 20

	// How long the queue must stay empty to count as stalled rather than
	// momentarily short. Re-priming costs a whole prebuffer of silence.
	zoneStallChunks = 50 // 1 s

	// Default kernel buffer behind a source FIFO, down from the 64 KiB the
	// kernel would otherwise use. Override with DANTE_FIFO_BYTES.
	defaultFIFOBytes = 16 << 10 // ~90 ms at CD rate stereo
)

type ZoneState struct {
	ID           int            `json:"id"`
	Name         string         `json:"name"`
	DanteLeft    string         `json:"dante_left"`
	DanteRight   string         `json:"dante_right"`
	Status       PlaybackStatus `json:"status"`
	CurrentTitle string         `json:"current_title"`
	CurrentURL   string         `json:"current_url"`
	StationID    string         `json:"station_id"`
	StationName  string         `json:"station_name"`
	Volume       int            `json:"volume"` // 0-100
	Muted        bool           `json:"muted"`
	IsSource     bool           `json:"is_source"`
	SourceLabel  string         `json:"source_label,omitempty"`
	PeakLeft     float64        `json:"peak_left"`  // 0.0 - 1.0
	PeakRight    float64        `json:"peak_right"` // 0.0 - 1.0
	ErrorMessage string         `json:"error_message,omitempty"`
}

type ZonePlayer struct {
	cfg         config.ZoneConfig
	mu          sync.RWMutex
	status      PlaybackStatus
	stationID   string
	stationName string
	streamURL   string
	title       string
	volume      int
	muted       bool
	errMessage  string

	peakL atomic.Uint64 // float64 bits
	peakR atomic.Uint64 // float64 bits

	// primed is false until the queue has built up zonePrebufferChunks, and
	// goes false again only after the queue has stayed empty for zoneStallChunks.
	primed      bool
	emptyPulls  atomic.Int64
	starvations atomic.Uint64

	// Both are per zone: a local producer feeding a FIFO is far steadier than an
	// internet stream and can run much shorter.
	prebufferChunks int
	queueChunks     int

	cancelFunc context.CancelFunc
	stateChan  chan struct{}
	audioChan  chan []int32 // stereo audio frames (interleaved L, R)
	fifoKeep   *os.File     // holds a source FIFO open so it never signals EOF
}

func NewZonePlayer(cfg config.ZoneConfig, stateChan chan struct{}) *ZonePlayer {
	prebuffer, queue := zonePrebufferChunks, zoneQueueChunks
	if src := cfg.Source; src != nil {
		if src.PrebufferMs > 0 {
			prebuffer = clampChunks(src.PrebufferMs, 1, zoneQueueChunks/2)
		}
		if src.BufferMs > 0 {
			queue = clampChunks(src.BufferMs, prebuffer+1, zoneQueueChunks)
		}
	}

	z := &ZonePlayer{
		cfg:             cfg,
		status:          StatusIdle,
		volume:          85,
		muted:           false,
		stateChan:       stateChan,
		audioChan:       make(chan []int32, queue),
		prebufferChunks: prebuffer,
		queueChunks:     queue,
	}
	z.setPeakL(0)
	z.setPeakR(0)
	return z
}

func clampChunks(millis, min, max int) int {
	chunks := millis / zoneChunkMillis
	if chunks < min {
		return min
	}
	if chunks > max {
		return max
	}
	return chunks
}

// IsSource reports whether this zone is permanently fed by an external
// producer rather than being a station browser slot.
func (z *ZonePlayer) IsSource() bool {
	return z.cfg.Source != nil
}

func (z *ZonePlayer) GetState() ZoneState {
	z.mu.RLock()
	defer z.mu.RUnlock()

	return ZoneState{
		ID:           z.cfg.ID,
		Name:         z.cfg.Name,
		DanteLeft:    z.cfg.DanteLeft,
		DanteRight:   z.cfg.DanteRight,
		Status:       z.status,
		CurrentTitle: z.title,
		CurrentURL:   z.streamURL,
		StationID:    z.stationID,
		StationName:  z.stationName,
		Volume:       z.volume,
		Muted:        z.muted,
		IsSource:     z.cfg.Source != nil,
		SourceLabel:  sourceLabel(z.cfg.Source),
		PeakLeft:     z.getPeakL(),
		PeakRight:    z.getPeakR(),
		ErrorMessage: z.errMessage,
	}
}

func sourceLabel(src *config.ZoneSource) string {
	if src == nil {
		return ""
	}
	return src.Label
}

func (z *ZonePlayer) SetVolume(vol int) {
	z.mu.Lock()
	if vol < 0 {
		vol = 0
	} else if vol > 100 {
		vol = 100
	}
	z.volume = vol
	z.mu.Unlock()
	z.notifyState()
}

func (z *ZonePlayer) ToggleMute() bool {
	z.mu.Lock()
	z.muted = !z.muted
	isMuted := z.muted
	z.mu.Unlock()
	z.notifyState()
	return isMuted
}

func (z *ZonePlayer) Stop() {
	// A source zone is a permanent feed from an external producer, not
	// something the station browser owns. Stop All must leave it alone.
	if z.IsSource() {
		return
	}

	z.mu.Lock()
	if z.cancelFunc != nil {
		z.cancelFunc()
		z.cancelFunc = nil
	}
	z.status = StatusIdle
	z.stationID = ""
	z.stationName = ""
	z.streamURL = ""
	z.title = ""
	z.errMessage = ""
	z.primed = false
	z.mu.Unlock()

	// Drain any pending audio frames
	for len(z.audioChan) > 0 {
		<-z.audioChan
	}

	z.setPeakL(0)
	z.setPeakR(0)
	z.notifyState()
}

// StartSource attaches the zone to its configured producer for good. It is a
// no-op for zones without a source, so callers need no special casing.
func (z *ZonePlayer) StartSource() error {
	if !z.IsSource() {
		return nil
	}
	src := z.cfg.Source
	if src.Type != "pipe" {
		return fmt.Errorf("zone %d: unknown source type %q", z.cfg.ID, src.Type)
	}

	keep, err := ensureFIFO(src.Path)
	if err != nil {
		z.setError(fmt.Sprintf("Source pipe unavailable: %v", err))
		return err
	}
	z.fifoKeep = keep
	shrinkPipe(keep, envInt("DANTE_FIFO_BYTES", defaultFIFOBytes))

	z.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	z.cancelFunc = cancel
	// Idle until the producer actually sends something.
	z.status = StatusIdle
	z.stationID = "source"
	z.stationName = src.Label
	z.title = src.Label
	z.streamURL = src.Path
	z.errMessage = ""
	z.primed = false
	z.mu.Unlock()
	z.notifyState()

	log.Printf("[Zone %d] Source %q reading %s PCM %d Hz from %s (prebuffer %d ms, buffer %d ms)",
		z.cfg.ID, src.Label, src.Format, src.SampleRate, src.Path,
		z.prebufferChunks*zoneChunkMillis, z.queueChunks*zoneChunkMillis)

	go z.sourceLoop(ctx, *src)
	return nil
}

// sourceLoop keeps a decoder attached to the FIFO, with FFmpeg resampling the
// producer's rate onto the Dante clock.
//
// A pausable producer is read with backpressure, which is what paces it: a full
// queue blocks the reader, FFmpeg's pipe fills, and the producer's write blocks.
// Drain the FIFO as fast as it is written and it has no clock at all. A
// realtime producer is the opposite and must never be blocked.
func (z *ZonePlayer) sourceLoop(ctx context.Context, src config.ZoneSource) {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		// FFmpeg otherwise holds a few hundred ms on each side of the resampler.
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-f", src.Format,
		"-ar", strconv.Itoa(src.SampleRate),
		"-ac", strconv.Itoa(src.Channels),
		"-i", src.Path,
		"-vn",
		"-f", "s32le",
		"-ar", "48000",
		"-ac", "2",
		"-flush_packets", "1",
		"-",
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if err := z.decodeInto(ctx, args, !src.Realtime); err != nil && ctx.Err() == nil {
			log.Printf("[Zone %d] Source decoder stopped: %v, restarting", z.cfg.ID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (z *ZonePlayer) Play(preset *config.StationPreset, customURL string, customTitle string) error {
	if z.IsSource() {
		return fmt.Errorf("zone %d is a %s source and cannot play stations", z.cfg.ID, z.cfg.Source.Label)
	}
	z.Stop()

	var streamURL, stationID, stationName string
	if preset != nil {
		streamURL = preset.StreamURL
		stationID = preset.ID
		stationName = preset.Name
	} else {
		streamURL = customURL
		stationID = "custom"
		stationName = customTitle
		if stationName == "" {
			stationName = "Custom Stream"
		}
	}

	if streamURL == "" {
		return fmt.Errorf("empty stream URL")
	}

	z.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	z.cancelFunc = cancel
	z.status = StatusBuffering
	z.streamURL = streamURL
	z.stationID = stationID
	z.stationName = stationName
	z.title = stationName
	z.errMessage = ""
	z.mu.Unlock()

	z.notifyState()

	go z.streamLoop(ctx, streamURL)
	return nil
}

func (z *ZonePlayer) streamLoop(ctx context.Context, streamURL string) {
	log.Printf("[Zone %d] Decoding stream for %s (%s)", z.cfg.ID, z.stationName, streamURL)

	// Launch FFmpeg to decode audio into 48kHz 32-bit stereo PCM
	ffmpegArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-reconnect", "1",
		"-reconnect_at_eof", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "4",
		"-i", streamURL,
		"-vn",
		"-f", "s32le",
		"-ar", "48000",
		"-ac", "2",
		"-",
	}

	// Status stays Buffering until PullSamples has built the prebuffer, so the
	// UI reflects when a zone is actually feeding the mixer. This also covers
	// the reconnect path, where the zone was Playing a moment ago.
	z.mu.Lock()
	z.status = StatusBuffering
	z.primed = false
	z.mu.Unlock()
	z.emptyPulls.Store(0)
	z.notifyState()

	err := z.decodeInto(ctx, ffmpegArgs, false)
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		log.Printf("[Zone %d] Stream EOF, reconnecting in 2s...", z.cfg.ID)
		time.Sleep(2 * time.Second)
		if ctx.Err() == nil {
			go z.streamLoop(ctx, streamURL)
		}
		return
	}
	z.setError(fmt.Sprintf("Stream read error: %v", err))
}

// decodeInto runs FFmpeg with the given arguments and pumps its raw 48 kHz
// stereo output into the zone queue until it exits or the context is cancelled.
// Both the station path and the source path share it; backpressure is what
// distinguishes them, see enqueue.
func (z *ZonePlayer) decodeInto(ctx context.Context, args []string, backpressure bool) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open decoder pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("decoder start: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// Chunk size: 960 frames (20ms at 48kHz) * 2 channels * 4 bytes = 7680 bytes
	rawBuf := make([]byte, 7680)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, err := io.ReadFull(stdout, rawBuf)
		if err != nil {
			return err
		}
		if n <= 0 {
			continue
		}

		numSamples := n / 4
		samples := make([]int32, numSamples)
		for i := 0; i < numSamples; i++ {
			samples[i] = int32(binary.LittleEndian.Uint32(rawBuf[i*4 : (i+1)*4]))
		}

		if err := z.enqueue(ctx, samples, backpressure); err != nil {
			return err
		}
	}
}

// enqueue hands one chunk to the mixer.
//
// A station stream is paced by the network and cannot be told to wait, so a
// full queue drops the oldest chunk. A pipe producer is the opposite: it writes
// as fast as we drain, so it has to be blocked or it has no clock at all.
func (z *ZonePlayer) enqueue(ctx context.Context, samples []int32, backpressure bool) error {
	if backpressure {
		select {
		case z.audioChan <- samples:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}

	select {
	case z.audioChan <- samples:
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Queue full: drop the oldest chunk, which bounds added latency when
		// the source outruns the Dante clock.
		select {
		case <-z.audioChan:
		default:
		}
		z.audioChan <- samples
	}
	return nil
}

// PullSamples pulls up to numFrames stereo samples (numFrames * 2 int32s),
// applies volume and updates peaks. Delivers nothing until the queue holds
// prebufferChunks.
func (z *ZonePlayer) PullSamples(numFrames int) ([]int32, bool) {
	z.mu.RLock()
	vol := z.volume
	muted := z.muted
	status := z.status
	primed := z.primed
	z.mu.RUnlock()

	// A source zone is permanently attached to its producer, so it keeps pulling
	// even while idle. A station zone only pulls once someone pressed play.
	if !z.IsSource() && status != StatusPlaying && status != StatusBuffering {
		z.setPeakL(0)
		z.setPeakR(0)
		return nil, false
	}

	if !primed {
		if queued := len(z.audioChan); queued < z.prebufferChunks {
			// Something is arriving, we are just short: that is buffering. An
			// empty queue is handled by the stall path below, which for a
			// source zone reports idle rather than buffering.
			if queued > 0 {
				z.setStatus(StatusBuffering)
			}
			z.decayPeaks()
			return nil, false
		}
		z.setPrimed(true)
	}

	var samples []int32
	select {
	case samples = <-z.audioChan:
		z.emptyPulls.Store(0)
	default:
		// A momentary shortfall costs one 20 ms hole. Re-priming here would
		// cost a whole prebuffer of silence, repeatedly, since the queue level
		// drifts freely. Only a sustained gap means the source stalled.
		z.starvations.Add(1)
		if z.emptyPulls.Add(1) >= zoneStallChunks {
			z.setPrimed(false)
		}
		z.decayPeaks()
		return nil, false
	}

	volFactor := float64(vol) / 100.0
	if muted {
		volFactor = 0.0
	}

	var maxL, maxR int32
	for i := 0; i < len(samples); i += 2 {
		if volFactor != 1.0 {
			samples[i] = int32(float64(samples[i]) * volFactor)
		}
		absL := absInt32(samples[i])
		if absL > maxL {
			maxL = absL
		}

		if i+1 < len(samples) {
			if volFactor != 1.0 {
				samples[i+1] = int32(float64(samples[i+1]) * volFactor)
			}
			absR := absInt32(samples[i+1])
			if absR > maxR {
				maxR = absR
			}
		}
	}

	z.setPeakL(float64(maxL) / float64(math.MaxInt32))
	z.setPeakR(float64(maxR) / float64(math.MaxInt32))

	return samples, true
}

// StarvationCount is the number of times the queue ran dry since start, i.e.
// how many 20 ms holes were mixed into the Dante stream.
func (z *ZonePlayer) StarvationCount() uint64 {
	return z.starvations.Load()
}

func (z *ZonePlayer) setPrimed(primed bool) {
	z.emptyPulls.Store(0)

	z.mu.Lock()
	changed := z.primed != primed
	z.primed = primed
	z.mu.Unlock()
	if !changed {
		return
	}

	switch {
	case primed:
		z.setStatus(StatusPlaying)
	case z.IsSource():
		// The producer went quiet. A source zone has nothing queued and nothing
		// on the way, which is idle - it is not working towards playback.
		z.setStatus(StatusIdle)
	default:
		z.setStatus(StatusBuffering)
	}
}

func (z *ZonePlayer) setStatus(status PlaybackStatus) {
	z.mu.Lock()
	if z.status == status {
		z.mu.Unlock()
		return
	}
	z.status = status
	z.mu.Unlock()
	z.notifyState()
}

func (z *ZonePlayer) decayPeaks() {
	z.setPeakL(z.getPeakL() * 0.9)
	z.setPeakR(z.getPeakR() * 0.9)
}

func (z *ZonePlayer) setError(msg string) {
	z.mu.Lock()
	z.status = StatusError
	z.errMessage = msg
	z.mu.Unlock()
	log.Printf("[Zone %d] Error: %s", z.cfg.ID, msg)
	z.notifyState()
}

func (z *ZonePlayer) notifyState() {
	select {
	case z.stateChan <- struct{}{}:
	default:
	}
}

func (z *ZonePlayer) setPeakL(v float64) {
	z.peakL.Store(math.Float64bits(v))
}

func (z *ZonePlayer) getPeakL() float64 {
	return math.Float64frombits(z.peakL.Load())
}

func (z *ZonePlayer) setPeakR(v float64) {
	z.peakR.Store(math.Float64bits(v))
}

func (z *ZonePlayer) getPeakR() float64 {
	return math.Float64frombits(z.peakR.Load())
}

func absInt32(n int32) int32 {
	if n < 0 {
		if n == math.MinInt32 {
			return math.MaxInt32
		}
		return -n
	}
	return n
}
