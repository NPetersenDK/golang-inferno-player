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
}

func NewZonePlayer(cfg config.ZoneConfig, stateChan chan struct{}) *ZonePlayer {
	z := &ZonePlayer{
		cfg:       cfg,
		status:    StatusIdle,
		volume:    85,
		muted:     false,
		stateChan: stateChan,
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
	log.Printf("[Zone %d] Starting stream for %s on ALSA device: %s", z.cfg.ID, streamURL, z.cfg.AlsaDevice)

	// 1. Launch FFmpeg decoder (Decodes MP3/AAC/HLS/Icecast into 48kHz 32-bit PCM)
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
	ffmpegStdout, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		z.setError(fmt.Sprintf("Failed to initialize decoder: %v", err))
		return
	}

	if err := ffmpegCmd.Start(); err != nil {
		z.setError(fmt.Sprintf("FFmpeg start error: %v", err))
		return
	}
	defer func() {
		if ffmpegCmd.Process != nil {
			_ = ffmpegCmd.Process.Kill()
		}
	}()

	// 2. Launch ALSA aplay process to pipe directly to Dante ALSA soundcard
	alsaDevice := z.cfg.AlsaDevice
	if alsaDevice == "" {
		alsaDevice = fmt.Sprintf("dante_zone%d", z.cfg.ID)
	}

	aplayArgs := []string{
		"-D", alsaDevice,
		"-t", "raw",
		"-f", "S32_LE",
		"-r", "48000",
		"-c", "2",
		"-q",
		"-",
	}

	aplayCmd := exec.CommandContext(ctx, "aplay", aplayArgs...)
	alsaStdin, err := aplayCmd.StdinPipe()
	var alsaWriter io.WriteCloser
	if err == nil {
		if err := aplayCmd.Start(); err == nil {
			alsaWriter = alsaStdin
			log.Printf("[Zone %d] Connected directly to Dante ALSA PCM device: %s", z.cfg.ID, alsaDevice)
		} else {
			log.Printf("[Zone %d] Notice: aplay not available or failed (%v). Running in DSP mode.", z.cfg.ID, err)
		}
	}
	defer func() {
		if alsaWriter != nil {
			_ = alsaWriter.Close()
		}
		if aplayCmd != nil && aplayCmd.Process != nil {
			_ = aplayCmd.Process.Kill()
		}
	}()

	// Transition status to Playing
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
				curL := z.getPeakL()
				curR := z.getPeakR()
				z.setPeakL(curL * 0.88)
				z.setPeakR(curR * 0.88)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := io.ReadFull(ffmpegStdout, buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				log.Printf("[Zone %d] Stream EOF/disconnect, reconnecting in 2s...", z.cfg.ID)
				time.Sleep(2 * time.Second)
				if ctx.Err() == nil {
					go z.streamLoop(ctx, streamURL)
				}
				return
			}
			z.setError(fmt.Sprintf("Read error: %v", err))
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

			// Update stereo peak meters
			peakL := float64(maxL) / float64(math.MaxInt32)
			peakR := float64(maxR) / float64(math.MaxInt32)
			if peakL > z.getPeakL() {
				z.setPeakL(peakL)
			}
			if peakR > z.getPeakR() {
				z.setPeakR(peakR)
			}

			// Write audio to ALSA Inferno soundcard
			if alsaWriter != nil {
				_, _ = alsaWriter.Write(buf[:n])
			}
		}
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
