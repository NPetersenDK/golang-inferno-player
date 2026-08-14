package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os"
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
	cfg        config.ZoneConfig
	mu         sync.RWMutex
	status     PlaybackStatus
	stationID  string
	stationName string
	streamURL  string
	title      string
	volume     int
	muted      bool
	errMessage string

	peakL      atomic.Uint64 // float64 bits
	peakR      atomic.Uint64 // float64 bits

	cancelFunc context.CancelFunc
	pipeWriter io.WriteCloser
	stateChan  chan struct{}
}

func NewZonePlayer(cfg config.ZoneConfig, stateChan chan struct{}) *ZonePlayer {
	z := &ZonePlayer{
		cfg:        cfg,
		status:     StatusIdle,
		volume:     85,
		muted:      false,
		stateChan:  stateChan,
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
	pipePath := z.cfg.PipePath
	_ = os.MkdirAll(os.Getenv("TMPDIR"), 0755)

	// Ensure FIFO exists or standard file
	createFifoIfNeeded(pipePath)

	log.Printf("[Zone %d] Starting stream for %s (Pipe: %s)", z.cfg.ID, streamURL, pipePath)

	// Open pipe for writing (blocks until reader opens or opens in non-blocking/reopen mode)
	pipeOut, err := openPipeWriter(pipePath)
	if err != nil {
		log.Printf("[Zone %d] Warning opening pipe: %v (will stream to null if Dante daemon not connected)", z.cfg.ID, err)
	} else {
		defer pipeOut.Close()
	}

	// Launch ffmpeg decoder
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
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

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		z.setError(fmt.Sprintf("Failed to start decoder pipe: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		z.setError(fmt.Sprintf("FFmpeg execution error: %v", err))
		return
	}

	z.mu.Lock()
	z.status = StatusPlaying
	z.mu.Unlock()
	z.notifyState()

	// Buffer: 1024 frames (1024 frames * 2 channels * 4 bytes = 8192 bytes)
	buf := make([]byte, 8192)
	decayTicker := time.NewTicker(50 * time.Millisecond)
	defer decayTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-decayTicker.C:
				// Smooth VU decay
				curL := z.getPeakL()
				curR := z.getPeakR()
				z.setPeakL(curL * 0.85)
				z.setPeakR(curR * 0.85)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return
		default:
		}

		n, err := io.ReadFull(stdout, buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Printf("[Zone %d] Stream EOF/disconnect, retrying in 2s...", z.cfg.ID)
				time.Sleep(2 * time.Second)
				if ctx.Err() == nil {
					go z.streamLoop(ctx, streamURL)
				}
				return
			}
			z.setError(fmt.Sprintf("Read error from audio source: %v", err))
			return
		}

		if n > 0 {
			z.mu.RLock()
			vol := z.volume
			muted := z.muted
			z.mu.RUnlock()

			volFactor := float64(vol) / 100.0
			if muted {
				volFactor = 0.0
			}

			// Process 32-bit interleaved PCM samples
			var maxL, maxR int32
			numSamples := n / 4
			for i := 0; i < numSamples; i += 2 {
				// Left sample
				sampleL := int32(binary.LittleEndian.Uint32(buf[i*4 : (i+1)*4]))
				if volFactor != 1.0 {
					sampleL = int32(float64(sampleL) * volFactor)
					binary.LittleEndian.PutUint32(buf[i*4:(i+1)*4], uint32(sampleL))
				}
				absL := absInt32(sampleL)
				if absL > maxL {
					maxL = absL
				}

				// Right sample
				if i+1 < numSamples {
					sampleR := int32(binary.LittleEndian.Uint32(buf[(i+1)*4 : (i+2)*4]))
					if volFactor != 1.0 {
						sampleR = int32(float64(sampleR) * volFactor)
						binary.LittleEndian.PutUint32(buf[(i+1)*4:(i+2)*4], uint32(sampleR))
					}
					absR := absInt32(sampleR)
					if absR > maxR {
						maxR = absR
					}
				}
			}

			// Calculate normalized peaks (0.0 to 1.0)
			peakLNorm := float64(maxL) / float64(math.MaxInt32)
			peakRNorm := float64(maxR) / float64(math.MaxInt32)
			if peakLNorm > z.getPeakL() {
				z.setPeakL(peakLNorm)
			}
			if peakRNorm > z.getPeakR() {
				z.setPeakR(peakRNorm)
			}

			// Write to FIFO pipe
			if pipeOut != nil {
				if _, werr := pipeOut.Write(buf[:n]); werr != nil {
					// Reader might have temporarily dropped, attempt reopen
					_ = pipeOut.Close()
					pipeOut, _ = openPipeWriter(pipePath)
				}
			}
		}
	}
}

func (z *ZonePlayer) setError(msg string) {
	z.mu.Lock()
	z.status = StatusError
	z.errMessage = msg
	z.mu.Unlock()
	z.setPeakL(0)
	z.setPeakR(0)
	z.notifyState()
}

func (z *ZonePlayer) notifyState() {
	if z.stateChan != nil {
		select {
		case z.stateChan <- struct{}{}:
		default:
		}
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
