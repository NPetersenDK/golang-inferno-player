package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type ZoneConfig struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	DanteLeft  string `json:"dante_left"`
	DanteRight string `json:"dante_right"`
	AlsaDevice string `json:"alsa_device,omitempty"`
	PipePath   string `json:"pipe_path"`
}

type StationPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	StreamURL   string `json:"stream_url"`
	LogoURL     string `json:"logo_url"`
	Description string `json:"description"`
	Bitrate     string `json:"bitrate"`
	IsCustom    bool   `json:"is_custom"`
}

type AppConfig struct {
	HTTPPort    int             `json:"http_port"`
	PipeDir     string          `json:"pipe_dir"`
	DanteName   string          `json:"dante_name"`
	SampleRate  int             `json:"sample_rate"`
	DataDir     string          `json:"data_dir"`
	Zones       []ZoneConfig    `json:"zones"`
	Presets     []StationPreset `json:"presets"`
	mu          sync.RWMutex    `json:"-"`
}

func DefaultConfig() *AppConfig {
	dataDir := "./data"
	pipeDir := "/tmp/dante_player"

	return &AppConfig{
		HTTPPort:   8080,
		PipeDir:    pipeDir,
		DanteName:  "Dante-Pi",
		SampleRate: 48000,
		DataDir:    dataDir,
		Zones: []ZoneConfig{
			{ID: 1, Name: "Zone 1 (Main / Stue)", DanteLeft: "Zone 1 L (Ch 1)", DanteRight: "Zone 1 R (Ch 2)", AlsaDevice: "dante_zone1", PipePath: filepath.Join(pipeDir, "zone_1.pcm")},
			{ID: 2, Name: "Zone 2 (Køkken)", DanteLeft: "Zone 2 L (Ch 3)", DanteRight: "Zone 2 R (Ch 4)", AlsaDevice: "dante_zone2", PipePath: filepath.Join(pipeDir, "zone_2.pcm")},
			{ID: 3, Name: "Zone 3 (Kontor)", DanteLeft: "Zone 3 L (Ch 5)", DanteRight: "Zone 3 R (Ch 6)", AlsaDevice: "dante_zone3", PipePath: filepath.Join(pipeDir, "zone_3.pcm")},
			{ID: 4, Name: "Zone 4 (Værksted / Altan)", DanteLeft: "Zone 4 L (Ch 7)", DanteRight: "Zone 4 R (Ch 8)", AlsaDevice: "dante_zone4", PipePath: filepath.Join(pipeDir, "zone_4.pcm")},
		},
		Presets: GetDefaultPresets(),
	}
}

func LoadConfig(path string) (*AppConfig, error) {
	cfg := DefaultConfig()
	if path == "" {
		path = filepath.Join(cfg.DataDir, "config.json")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = cfg.Save(path)
			return cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *AppConfig) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if path == "" {
		path = filepath.Join(c.DataDir, "config.json")
	}

	_ = os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *AppConfig) AddCustomPreset(p StationPreset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p.IsCustom = true
	c.Presets = append(c.Presets, p)
}

func (c *AppConfig) DeletePreset(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Presets {
		if p.ID == id && p.IsCustom {
			c.Presets = append(c.Presets[:i], c.Presets[i+1:]...)
			return true
		}
	}
	return false
}

func (c *AppConfig) GetPreset(id string) (StationPreset, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.Presets {
		if p.ID == id {
			return p, true
		}
	}
	return StationPreset{}, false
}
