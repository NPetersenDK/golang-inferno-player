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

// Buffering geometry, in 20 ms chunks. Source and mixer run on unrelated
// oscillators, so headroom must exceed the source's jitter: seconds, not ms.
const (
	zoneQueueChunks     = 150 // 3 s
	zonePrebufferChunks = 50  // 1 s
	zoneChunkMillis     = 20

	// Empty for this long counts as stalled; re-priming costs a whole
	// prebuffer of silence, so a shorter gap must be ridden out.
	zoneStallChunks = 50 // 1 s

	// Kernel FIFO buffer, down from 64 KiB. Override with DANTE_FIFO_BYTES.
	defaultFIFOBytes = 16 << 10 // ~90 ms at CD rate stereo

	// Headroom between prebuffer and cap. Warned about, not enforced.
	minZoneHeadroomMs = 250
)

const (
	// FFmpeg's own -reconnect flags do not cover a server that accepts the
	// socket and then says nothing, and it will sit on that forever.
	streamReadTimeout = 20 * time.Second

	streamRetryMin = 2 * time.Second
	streamRetryMax = 30 * time.Second

	// Ran this long, so the next failure restarts backoff from the minimum.
	streamHealthyRun = time.Minute
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
	SoundLabel   string         `json:"sound_label,omitempty"`
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

	primed      bool
	emptyPulls  atomic.Int64
	starvations atomic.Uint64

	// Per zone: a FIFO producer is steadier than a stream and can run shorter.
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

	// Backpressure keeps a pipe producer's queue full, so the gap between the
	// two is all the slack there is: pause longer and the zone drops out.
	if cfg.Source != nil && (queue-prebuffer)*zoneChunkMillis < minZoneHeadroomMs {
		log.Printf("[Zone %d] buffer_ms leaves only %d ms above prebuffer_ms; a producer pausing longer than that will drop out mid-track",
			cfg.ID, (queue-prebuffer)*zoneChunkMillis)
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
	// Stop All must leave a producer's feed alone.
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

	for len(z.audioChan) > 0 {
		<-z.audioChan
	}

	z.setPeakL(0)
	z.setPeakR(0)
	z.notifyState()
}

// StartSource attaches the zone to its producer for good; a no-op without one.
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

// sourceLoop keeps a decoder attached to the FIFO. Backpressure is what paces a
// pausable producer; a realtime one must never be blocked.
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
		// No idle timeout: a pipe producer is entitled to be quiet.
		if err := z.decodeInto(ctx, args, !src.Realtime, 0); err != nil && ctx.Err() == nil {
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

// streamLoop treats every way a stream can end as temporary, with backoff.
func (z *ZonePlayer) streamLoop(ctx context.Context, streamURL string) {
	log.Printf("[Zone %d] Decoding stream for %s (%s)", z.cfg.ID, z.stationName, streamURL)

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

	retry := streamRetryMin
	for {
		if ctx.Err() != nil {
			return
		}

		z.mu.Lock()
		z.status = StatusBuffering
		z.primed = false
		z.mu.Unlock()
		z.emptyPulls.Store(0)
		z.notifyState()

		started := time.Now()
		err := z.decodeInto(ctx, ffmpegArgs, false, streamReadTimeout)
		if ctx.Err() != nil {
			return
		}

		if time.Since(started) >= streamHealthyRun {
			retry = streamRetryMin
		}

		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			log.Printf("[Zone %d] Stream ended, reconnecting in %s", z.cfg.ID, retry)
		default:
			log.Printf("[Zone %d] Stream failed (%v), reconnecting in %s", z.cfg.ID, err, retry)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}

		if retry *= 2; retry > streamRetryMax {
			retry = streamRetryMax
		}
	}
}

// decodeInto pumps FFmpeg's 48 kHz stereo output into the zone queue. A nonzero
// idleTimeout kills it after that long without audio; only network streams want
// that, since a pipe producer going quiet is normal.
func (z *ZonePlayer) decodeInto(ctx context.Context, args []string, backpressure bool, idleTimeout time.Duration) error {
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

	var lastRead atomic.Int64
	lastRead.Store(time.Now().UnixNano())
	if idleTimeout > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go z.watchDecoder(cmd, &lastRead, idleTimeout, stop)
	}

	rawBuf := make([]byte, 7680) // 960 frames (20 ms) * 2 ch * 4 bytes

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
		lastRead.Store(time.Now().UnixNano())

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

// watchDecoder kills FFmpeg when it stops producing audio: a stalled connection
// never closes the pipe, so killing it is what turns the hang into a read error.
func (z *ZonePlayer) watchDecoder(cmd *exec.Cmd, lastRead *atomic.Int64, timeout time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, lastRead.Load())) < timeout {
				continue
			}
			log.Printf("[Zone %d] No audio from the decoder for %s, restarting it", z.cfg.ID, timeout)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return
		}
	}
}

// enqueue drops the oldest chunk for a network stream, which cannot be told to
// wait, but blocks a pipe producer, which otherwise has no clock at all.
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
		select {
		case <-z.audioChan:
		default:
		}
		z.audioChan <- samples
	}
	return nil
}

// PullSamples returns interleaved stereo with volume applied, and nothing at
// all until the prebuffer is full.
func (z *ZonePlayer) PullSamples(numFrames int) ([]int32, bool) {
	z.mu.RLock()
	vol := z.volume
	muted := z.muted
	status := z.status
	primed := z.primed
	z.mu.RUnlock()

	// A source zone keeps pulling while idle, or it could never come back.
	if !z.IsSource() && status != StatusPlaying && status != StatusBuffering {
		z.setPeakL(0)
		z.setPeakR(0)
		return nil, false
	}

	if !primed {
		if queued := len(z.audioChan); queued < z.prebufferChunks {
			// Short but arriving is buffering; empty is left to the stall path.
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
		// A momentary shortfall costs one 20 ms hole. Re-priming here would cost
		// a whole prebuffer of silence, repeatedly, since the level drifts freely.
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

// BumpPeaks raises the meters (0 to 1) for audio PullSamples never sees.
func (z *ZonePlayer) BumpPeaks(l, r float64) {
	if l > z.getPeakL() {
		z.setPeakL(l)
	}
	if r > z.getPeakR() {
		z.setPeakR(r)
	}
}

// GainFactor is the zone's volume as a multiplier, zero while muted.
func (z *ZonePlayer) GainFactor() float64 {
	z.mu.RLock()
	defer z.mu.RUnlock()
	if z.muted {
		return 0
	}
	return float64(z.volume) / 100.0
}

// StarvationCount is how many 20 ms holes went into the Dante stream.
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
		// A quiet producer is idle, not working towards playback.
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
