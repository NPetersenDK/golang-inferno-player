package engine

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"sync"
	"time"

	"dante-player/config"
)

type SystemStatus struct {
	DanteDevice   string      `json:"dante_device"`
	DanteChannels int         `json:"dante_channels"`
	SampleRate    int         `json:"sample_rate"`
	ClockStatus   string      `json:"clock_status"`
	PTPStatus     string      `json:"ptp_status"`
	PTP           PTPStats    `json:"ptp"`
	ActiveStreams int         `json:"active_streams"`
	Zones         []ZoneState `json:"zones"`
}

type PlaybackManager struct {
	cfg        *config.AppConfig
	zones      map[int]*ZonePlayer
	zoneOrder  []int
	stateChan  chan struct{}
	listeners  map[chan SystemStatus]struct{}
	listenerMu sync.Mutex
	mu         sync.RWMutex
	ptp        *PTPMonitor
	discipline *ClockDiscipline
}

func NewPlaybackManager(cfg *config.AppConfig) *PlaybackManager {
	stateChan := make(chan struct{}, 100)
	mgr := &PlaybackManager{
		cfg:       cfg,
		zones:     make(map[int]*ZonePlayer),
		zoneOrder: make([]int, 0),
		stateChan: stateChan,
		listeners: make(map[chan SystemStatus]struct{}),
	}

	for _, zcfg := range cfg.Zones {
		mgr.zones[zcfg.ID] = NewZonePlayer(zcfg, stateChan)
		mgr.zoneOrder = append(mgr.zoneOrder, zcfg.ID)
	}
	sort.Ints(mgr.zoneOrder)

	// Measure the Dante grandmaster passively, then serve the resulting media
	// clock to Inferno on both socket paths it may look for.
	mgr.ptp = StartPTPMonitor()
	mgr.discipline = StartClockDiscipline(mgr.ptp)
	_, _ = StartUsrvclockServer("/tmp/usrvclock", mgr.discipline)
	_, _ = StartUsrvclockServer("/run/ptp-usrvclock", mgr.discipline)

	go mgr.eventBroadcaster()
	go mgr.masterDanteAudioLoop()

	return mgr
}

// masterDanteAudioLoop keeps a single continuous 8-channel 48kHz audio stream running into pcm.inferno.
func (m *PlaybackManager) masterDanteAudioLoop() {
	const (
		sampleRate    = 48000
		numChannels   = 8
		framesPerTick = 960 // 20ms block
		bytesPerTick  = framesPerTick * numChannels * 4
	)

	masterBuf := make([]byte, bytesPerTick)

	for {
		log.Printf("[Dante Master] Starting continuous 8-channel Dante ALSA transmitter (pcm.inferno)...")

		// Launch aplay with 250ms buffer to prevent underruns
		cmd := exec.Command("aplay", "-D", "inferno", "-t", "raw", "-f", "S32_LE", "-r", "48000", "-c", "8", "--buffer-time=250000", "-")
		
		stdin, err := cmd.StdinPipe()
		if err != nil {
			log.Printf("[Dante Master] Notice: Could not open aplay stdin: %v. Running in emulation mode.", err)
			m.emulateMasterLoop(framesPerTick)
			continue
		}

		stderr, _ := cmd.StderrPipe()
		if stderr != nil {
			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					log.Printf("[Inferno ALSA] %s", scanner.Text())
				}
			}()
		}

		if err := cmd.Start(); err != nil {
			log.Printf("[Dante Master] Notice: Could not start aplay: %v. Running in emulation mode.", err)
			_ = stdin.Close()
			m.emulateMasterLoop(framesPerTick)
			continue
		}

		log.Printf("[Dante Master] Dante ALSA audio transmitter active (8 TX channels). Dante-Pi is now broadcasting.")

		// Pre-fill ALSA buffer with 100ms of silence to ensure no initial underruns
		for i := 0; i < 5; i++ {
			_, _ = stdin.Write(masterBuf)
		}

		ticker := time.NewTicker(20 * time.Millisecond)
		aplayAlive := true

		for aplayAlive {
			<-ticker.C

			// 1. Clear 8-channel master buffer (silence by default)
			for i := range masterBuf {
				masterBuf[i] = 0
			}

			// 2. Mix active zone audio into respective stereo channel slots
			m.mu.RLock()
			for zoneIdx, zoneID := range m.zoneOrder {
				if zoneIdx*2+1 >= numChannels {
					break
				}
				player := m.zones[zoneID]
				if player == nil {
					continue
				}

				samples, hasAudio := player.PullSamples(framesPerTick)
				if hasAudio && len(samples) > 0 {
					// Slot into channels: Left = zoneIdx*2, Right = zoneIdx*2 + 1
					chL := zoneIdx * 2
					chR := zoneIdx * 2 + 1

					numStereo := len(samples) / 2
					for f := 0; f < numStereo && f < framesPerTick; f++ {
						frameOffset := f * numChannels * 4
						// Left sample
						binary.LittleEndian.PutUint32(masterBuf[frameOffset+chL*4:frameOffset+(chL+1)*4], uint32(samples[f*2]))
						// Right sample
						binary.LittleEndian.PutUint32(masterBuf[frameOffset+chR*4:frameOffset+(chR+1)*4], uint32(samples[f*2+1]))
					}
				}
			}
			m.mu.RUnlock()

			// 3. Send 8-channel frame to Inferno ALSA soundcard
			if _, err := stdin.Write(masterBuf); err != nil {
				log.Printf("[Dante Master] ALSA write error (%v). Restarting transmitter...", err)
				aplayAlive = false
			}
		}

		ticker.Stop()
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		time.Sleep(2 * time.Second)
	}
}

func (m *PlaybackManager) emulateMasterLoop(framesPerTick int) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		m.mu.RLock()
		for _, player := range m.zones {
			player.PullSamples(framesPerTick)
		}
		m.mu.RUnlock()
	}
}

func (m *PlaybackManager) GetZone(id int) (*ZonePlayer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	z, ok := m.zones[id]
	if !ok {
		return nil, fmt.Errorf("zone %d not found", id)
	}
	return z, nil
}

func (m *PlaybackManager) PlayZonePreset(zoneID int, presetID string) error {
	z, err := m.GetZone(zoneID)
	if err != nil {
		return err
	}

	preset, ok := m.cfg.GetPreset(presetID)
	if !ok {
		return fmt.Errorf("preset %s not found", presetID)
	}

	return z.Play(&preset, "", "")
}

func (m *PlaybackManager) PlayZoneCustom(zoneID int, url string, title string) error {
	z, err := m.GetZone(zoneID)
	if err != nil {
		return err
	}

	return z.Play(nil, url, title)
}

func (m *PlaybackManager) StopZone(zoneID int) error {
	z, err := m.GetZone(zoneID)
	if err != nil {
		return err
	}
	z.Stop()
	return nil
}

func (m *PlaybackManager) StopAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, z := range m.zones {
		z.Stop()
	}
}

func (m *PlaybackManager) SetZoneVolume(zoneID int, volume int) error {
	z, err := m.GetZone(zoneID)
	if err != nil {
		return err
	}
	z.SetVolume(volume)
	return nil
}

func (m *PlaybackManager) ToggleZoneMute(zoneID int) (bool, error) {
	z, err := m.GetZone(zoneID)
	if err != nil {
		return false, err
	}
	return z.ToggleMute(), nil
}

func (m *PlaybackManager) GetStatus() SystemStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	active := 0
	zones := make([]ZoneState, 0, len(m.zoneOrder))
	for _, id := range m.zoneOrder {
		if z, ok := m.zones[id]; ok {
			st := z.GetState()
			if st.Status == StatusPlaying {
				active++
			}
			zones = append(zones, st)
		}
	}

	stats, clockStatus, ptpStatus := m.clockStatus()

	return SystemStatus{
		DanteDevice:   m.cfg.DanteName,
		DanteChannels: 8,
		SampleRate:    m.cfg.SampleRate,
		ClockStatus:   clockStatus,
		PTPStatus:     ptpStatus,
		PTP:           stats,
		ActiveStreams: active,
		Zones:         zones,
	}
}

func (m *PlaybackManager) clockStatus() (stats PTPStats, clock string, ptp string) {
	if m.ptp == nil {
		return PTPStats{LastSyncAgo: "never"}, "Media clock: static offset", "PTP monitor not started"
	}
	stats = m.ptp.Stats()
	if !stats.Locked {
		return stats,
			fmt.Sprintf("Free-running on static offset (%d Sync packets seen)", stats.SyncPackets),
			"No PTPv1 grandmaster locked"
	}
	clock = fmt.Sprintf("PTP Locked (%d Hz)", m.cfg.SampleRate)
	ptp = fmt.Sprintf("Slave to %s: offset %.3f ms, drift %.2f ppm",
		stats.MasterID, float64(stats.OffsetNs)/1e6, stats.DriftPPM)
	return stats, clock, ptp
}

func (m *PlaybackManager) SubscribeState() chan SystemStatus {
	return m.Subscribe()
}

func (m *PlaybackManager) UnsubscribeState(ch chan SystemStatus) {
	m.Unsubscribe(ch)
}

func (m *PlaybackManager) Subscribe() chan SystemStatus {
	m.listenerMu.Lock()
	defer m.listenerMu.Unlock()

	ch := make(chan SystemStatus, 10)
	m.listeners[ch] = struct{}{}
	return ch
}

func (m *PlaybackManager) Unsubscribe(ch chan SystemStatus) {
	m.listenerMu.Lock()
	defer m.listenerMu.Unlock()

	delete(m.listeners, ch)
	close(ch)
}

func (m *PlaybackManager) eventBroadcaster() {
	broadcastTicker := time.NewTicker(100 * time.Millisecond)
	defer broadcastTicker.Stop()

	for {
		select {
		case <-m.stateChan:
			m.broadcast()
		case <-broadcastTicker.C:
			m.broadcast()
		}
	}
}

func (m *PlaybackManager) broadcast() {
	st := m.GetStatus()

	m.listenerMu.Lock()
	defer m.listenerMu.Unlock()

	for ch := range m.listeners {
		select {
		case ch <- st:
		default:
		}
	}
}
