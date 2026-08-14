package engine

import (
	"fmt"
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
	ActiveStreams int         `json:"active_streams"`
	Zones         []ZoneState `json:"zones"`
}

type PlaybackManager struct {
	cfg        *config.AppConfig
	zones      map[int]*ZonePlayer
	stateChan  chan struct{}
	listeners  map[chan SystemStatus]struct{}
	listenerMu sync.Mutex
	mu         sync.RWMutex
}

func NewPlaybackManager(cfg *config.AppConfig) *PlaybackManager {
	stateChan := make(chan struct{}, 100)
	mgr := &PlaybackManager{
		cfg:       cfg,
		zones:     make(map[int]*ZonePlayer),
		stateChan: stateChan,
		listeners: make(map[chan SystemStatus]struct{}),
	}

	for _, zcfg := range cfg.Zones {
		mgr.zones[zcfg.ID] = NewZonePlayer(zcfg, stateChan)
	}

	go mgr.eventBroadcaster()
	return mgr
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

	var zoneStates []ZoneState
	activeCount := 0

	for _, zcfg := range m.cfg.Zones {
		if z, ok := m.zones[zcfg.ID]; ok {
			state := z.GetState()
			if state.Status == StatusPlaying || state.Status == StatusBuffering {
				activeCount++
			}
			zoneStates = append(zoneStates, state)
		}
	}

	return SystemStatus{
		DanteDevice:   m.cfg.DanteName,
		DanteChannels: len(m.cfg.Zones) * 2,
		SampleRate:    m.cfg.SampleRate,
		ClockStatus:   "PTP Dante Clock Synced (48.0 kHz)",
		PTPStatus:     "Active",
		ActiveStreams: activeCount,
		Zones:         zoneStates,
	}
}

func (m *PlaybackManager) SubscribeState() chan SystemStatus {
	ch := make(chan SystemStatus, 10)
	m.listenerMu.Lock()
	m.listeners[ch] = struct{}{}
	m.listenerMu.Unlock()

	// Send immediate initial status
	ch <- m.GetStatus()
	return ch
}

func (m *PlaybackManager) UnsubscribeState(ch chan SystemStatus) {
	m.listenerMu.Lock()
	delete(m.listeners, ch)
	close(ch)
	m.listenerMu.Unlock()
}

func (m *PlaybackManager) eventBroadcaster() {
	// Send status tick every 100ms for smooth VU meters and state updates
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-m.stateChan:
			m.broadcast()
		case <-ticker.C:
			m.broadcast()
		}
	}
}

func (m *PlaybackManager) broadcast() {
	status := m.GetStatus()

	m.listenerMu.Lock()
	for ch := range m.listeners {
		select {
		case ch <- status:
		default:
		}
	}
	m.listenerMu.Unlock()
}
