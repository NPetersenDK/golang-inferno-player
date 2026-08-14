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
		audioChan: make(chan []int32, 20),
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

	z.mu.Lock()
	z.status = StatusPlaying
	z.mu.Unlock()
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

// PullSamples pulls up to numFrames stereo samples (numFrames * 2 int32s), applies volume and updates peaks
func (z *ZonePlayer) PullSamples(numFrames int) ([]int32, bool) {
	select {
	case samples := <-z.audioChan:
		z.mu.RLock()
		vol := z.volume
		muted := z.muted
		isPlaying := (z.status == StatusPlaying)
		z.mu.RUnlock()

		if !isPlaying {
			z.setPeakL(0)
			z.setPeakR(0)
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

		peakL := float64(maxL) / float64(math.MaxInt32)
		peakR := float64(maxR) / float64(math.MaxInt32)
		z.setPeakL(peakL)
		z.setPeakR(peakR)

		return samples, true
	default:
		// No new audio frames (decay peaks)
		curL := z.getPeakL()
		curR := z.getPeakR()
		z.setPeakL(curL * 0.9)
		z.setPeakR(curR * 0.9)
		return nil, false
	}
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
