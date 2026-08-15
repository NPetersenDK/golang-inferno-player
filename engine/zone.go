package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
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
// FFmpeg produces at whatever rate the source server hands the stream over,
// while the mixer consumes at the Dante media clock rate. Those are two
// unrelated oscillators, so the queue level is a random walk with no restoring
// force: whatever it is primed to, it drifts from there. The only defence is to
// make the walk's headroom much larger than the jitter of an internet stream,
// which is why web radio players buffer seconds rather than milliseconds.
const (
	// zoneQueueChunks caps the queue, and so caps added latency. The producer
	// drops the oldest chunk once it is reached, which is what handles a source
	// that runs faster than the Dante clock.
	zoneQueueChunks = 150 // 3 s

	// zonePrebufferChunks is the level a zone fills to before it starts
	// delivering, leaving roughly a second of slack in both directions.
	zonePrebufferChunks = 50 // 1 s

	// zoneStallChunks is how long the queue must stay empty before the zone is
	// treated as stalled rather than momentarily short. Rebuilding the whole
	// prebuffer costs a second of silence, so it must not be triggered by the
	// odd missing chunk.
	zoneStallChunks = 50 // 1 s
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

	cancelFunc context.CancelFunc
	stateChan  chan struct{}
	audioChan  chan []int32 // stereo audio frames (interleaved L, R)
}

func NewZonePlayer(cfg config.ZoneConfig, stateChan chan struct{}) *ZonePlayer {
	z := &ZonePlayer{
		cfg:       cfg,
		status:    StatusIdle,
		volume:    85,
		muted:     false,
		stateChan: stateChan,
		audioChan: make(chan []int32, zoneQueueChunks),
	}
	z.setPeakL(0)
	z.setPeakR(0)
	return z
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
		PeakLeft:     z.getPeakL(),
		PeakRight:    z.getPeakR(),
		ErrorMessage: z.errMessage,
	}
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

func (z *ZonePlayer) Play(preset *config.StationPreset, customURL string, customTitle string) error {
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

	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	stdout, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		z.setError(fmt.Sprintf("Failed to open decoder pipe: %v", err))
		return
	}

	if err := ffmpegCmd.Start(); err != nil {
		z.setError(fmt.Sprintf("Decoder start failed: %v", err))
		return
	}
	defer func() {
		if ffmpegCmd.Process != nil {
			_ = ffmpegCmd.Process.Kill()
		}
	}()

	// Status stays Buffering until PullSamples has built the prebuffer, so the
	// UI reflects when a zone is actually feeding the mixer. This also covers
	// the reconnect path, where the zone was Playing a moment ago.
	z.mu.Lock()
	z.status = StatusBuffering
	z.primed = false
	z.mu.Unlock()
	z.emptyPulls.Store(0)
	z.notifyState()

	// Chunk size: 960 frames (20ms at 48kHz) * 2 channels * 4 bytes = 7680 bytes
	rawBuf := make([]byte, 7680)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := io.ReadFull(stdout, rawBuf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Printf("[Zone %d] Stream EOF, reconnecting in 2s...", z.cfg.ID)
				time.Sleep(2 * time.Second)
				if ctx.Err() == nil {
					go z.streamLoop(ctx, streamURL)
				}
				return
			}
			z.setError(fmt.Sprintf("Stream read error: %v", err))
			return
		}

		if n > 0 {
			numSamples := n / 4
			samples := make([]int32, numSamples)
			for i := 0; i < numSamples; i++ {
				samples[i] = int32(binary.LittleEndian.Uint32(rawBuf[i*4 : (i+1)*4]))
			}

			// Send samples non-blocking to master audio mixer
			select {
			case z.audioChan <- samples:
			case <-ctx.Done():
				return
			default:
				// If channel is full, drop older frame to keep realtime sync
				select {
				case <-z.audioChan:
				default:
				}
				z.audioChan <- samples
			}
		}
	}
}

// PullSamples pulls up to numFrames stereo samples (numFrames * 2 int32s), applies volume and updates peaks.
//
// A zone delivers nothing until its queue holds zonePrebufferChunks, and it
// goes back to rebuilding that prebuffer the moment the queue runs dry. Handing
// out the single chunk that happens to be available would just glitch again on
// the next tick.
func (z *ZonePlayer) PullSamples(numFrames int) ([]int32, bool) {
	z.mu.RLock()
	vol := z.volume
	muted := z.muted
	status := z.status
	primed := z.primed
	z.mu.RUnlock()

	if status != StatusPlaying && status != StatusBuffering {
		z.setPeakL(0)
		z.setPeakR(0)
		return nil, false
	}

	if !primed {
		if len(z.audioChan) < zonePrebufferChunks {
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
		// A momentary shortfall costs one 20 ms hole. Dropping back to
		// buffering here would instead cost a full prebuffer of silence, and
		// since the queue level drifts freely it would happen over and over -
		// that is what made the zone flap between playing and buffering. Only a
		// sustained gap means the source has actually stalled.
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
	if z.primed == primed {
		z.mu.Unlock()
		return
	}
	z.primed = primed
	if primed && z.status == StatusBuffering {
		z.status = StatusPlaying
	} else if !primed && z.status == StatusPlaying {
		z.status = StatusBuffering
	}
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
