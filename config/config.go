package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ZoneSource turns a zone into a permanent feed from an external producer
// instead of a station browser slot. The engine only knows how to read raw PCM
// from a FIFO; what writes to that FIFO - librespot, shairport-sync, anything
// else - is entirely outside this project. Leave it unset and the zone behaves
// exactly as before.
type ZoneSource struct {
	// Type selects the mechanism. Only "pipe" exists today.
	Type string `json:"type" yaml:"type"`
	// Path is the FIFO to read. Created at startup if missing.
	Path string `json:"path" yaml:"path"`
	// Label is what the UI shows for this zone, e.g. "Spotify".
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// Raw PCM layout the producer writes. Defaults suit librespot's pipe
	// backend, which emits S16LE stereo at 44.1 kHz.
	Format     string `json:"format,omitempty" yaml:"format,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty" yaml:"sample_rate,omitempty"`
	Channels   int    `json:"channels,omitempty" yaml:"channels,omitempty"`

	// PrebufferMs overrides the zone queue depth. A local producer is far
	// steadier than an internet stream, so a source zone can run much shorter
	// than the default and stay responsive.
	PrebufferMs int `json:"prebuffer_ms,omitempty" yaml:"prebuffer_ms,omitempty"`
}

type ZoneConfig struct {
	ID         int         `json:"id" yaml:"id"`
	Name       string      `json:"name" yaml:"name"`
	DanteLeft  string      `json:"dante_left" yaml:"dante_left"`
	DanteRight string      `json:"dante_right" yaml:"dante_right"`
	AlsaDevice string      `json:"alsa_device,omitempty" yaml:"alsa_device,omitempty"`
	PipePath   string      `json:"pipe_path" yaml:"pipe_path"`
	Source     *ZoneSource `json:"source,omitempty" yaml:"source,omitempty"`
}

type StationPreset struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Category    string `json:"category" yaml:"category"`
	StreamURL   string `json:"stream_url" yaml:"stream_url"`
	LogoURL     string `json:"logo_url,omitempty" yaml:"logo_url,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Bitrate     string `json:"bitrate,omitempty" yaml:"bitrate,omitempty"`
	IsCustom    bool   `json:"is_custom,omitempty" yaml:"is_custom,omitempty"`
}

type AppConfig struct {
	HTTPPort   int             `json:"http_port" yaml:"http_port"`
	PipeDir    string          `json:"pipe_dir" yaml:"pipe_dir"`
	DanteName  string          `json:"dante_name" yaml:"dante_name"`
	SampleRate int             `json:"sample_rate" yaml:"sample_rate"`
	DataDir    string          `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
	Zones      []ZoneConfig    `json:"zones" yaml:"zones"`
	Stations   []StationPreset `json:"stations" yaml:"stations"`
	Presets    []StationPreset `json:"presets,omitempty" yaml:"presets,omitempty"` // backward compat
	mu         sync.RWMutex    `json:"-" yaml:"-"`
}

// applyDefaults fills in the raw PCM layout a producer is assumed to write.
// Called on a nil receiver for every zone without a source, where it does
// nothing.
func (s *ZoneSource) applyDefaults(zone *ZoneConfig, pipeDir string) {
	if s == nil {
		return
	}
	if s.Type == "" {
		s.Type = "pipe"
	}
	if s.Path == "" {
		s.Path = filepath.Join(pipeDir, fmt.Sprintf("source_%d.pcm", zone.ID))
	}
	if s.Label == "" {
		s.Label = zone.Name
	}
	if s.Format == "" {
		s.Format = "s16le"
	}
	if s.SampleRate <= 0 {
		s.SampleRate = 44100
	}
	if s.Channels <= 0 {
		s.Channels = 2
	}
}

func DefaultConfig() *AppConfig {
	pipeDir := "/tmp/dante_player"

	return &AppConfig{
		HTTPPort:   8085,
		PipeDir:    pipeDir,
		DanteName:  "Dante-Pi",
		SampleRate: 48000,
		DataDir:    "./data",
		Zones:      []ZoneConfig{},
		Stations:   []StationPreset{},
	}
}

func LoadConfig(path string) (*AppConfig, error) {
	cfg := DefaultConfig()

	// 1. Search for config file if not explicitly specified
	if path == "" {
		candidates := []string{
			"config.yaml",
			"config.yml",
			"/opt/dante-player/config.yaml",
			"/etc/dante-player/config.yaml",
			"config.example.yaml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}

	// 2. Read declarative config file
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			log.Printf("[Config] Loaded declarative configuration from %s", path)
			if strings.HasSuffix(path, ".json") {
				_ = json.Unmarshal(data, cfg)
			} else {
				_ = yaml.Unmarshal(data, cfg)
			}
		} else {
			log.Printf("[Config] Notice: Could not read config file %s: %v", path, err)
		}
	} else {
		log.Printf("[Config] Notice: No config.yaml found. Using empty declarative state.")
	}

	// If no zones were defined in YAML, set default 4 stereo zones
	if len(cfg.Zones) == 0 {
		cfg.Zones = []ZoneConfig{
			{ID: 1, Name: "Zone 1", DanteLeft: "Zone 1 L (Ch 1)", DanteRight: "Zone 1 R (Ch 2)", AlsaDevice: "dante_zone1", PipePath: filepath.Join(cfg.PipeDir, "zone_1.pcm")},
			{ID: 2, Name: "Zone 2", DanteLeft: "Zone 2 L (Ch 3)", DanteRight: "Zone 2 R (Ch 4)", AlsaDevice: "dante_zone2", PipePath: filepath.Join(cfg.PipeDir, "zone_2.pcm")},
			{ID: 3, Name: "Zone 3", DanteLeft: "Zone 3 L (Ch 5)", DanteRight: "Zone 3 R (Ch 6)", AlsaDevice: "dante_zone3", PipePath: filepath.Join(cfg.PipeDir, "zone_3.pcm")},
			{ID: 4, Name: "Zone 4", DanteLeft: "Zone 4 L (Ch 7)", DanteRight: "Zone 4 R (Ch 8)", AlsaDevice: "dante_zone4", PipePath: filepath.Join(cfg.PipeDir, "zone_4.pcm")},
		}
	}

	// Ensure PipePath and AlsaDevice are set for all zones
	for i := range cfg.Zones {
		if cfg.Zones[i].PipePath == "" {
			cfg.Zones[i].PipePath = filepath.Join(cfg.PipeDir, fmt.Sprintf("zone_%d.pcm", cfg.Zones[i].ID))
		}
		if cfg.Zones[i].AlsaDevice == "" {
			cfg.Zones[i].AlsaDevice = fmt.Sprintf("dante_zone%d", cfg.Zones[i].ID)
		}
		cfg.Zones[i].Source.applyDefaults(&cfg.Zones[i], cfg.PipeDir)
	}

	// Sync Stations & Presets for API backward compatibility
	if len(cfg.Stations) == 0 && len(cfg.Presets) > 0 {
		cfg.Stations = cfg.Presets
	} else if len(cfg.Stations) > 0 && len(cfg.Presets) == 0 {
		cfg.Presets = cfg.Stations
	}

	// 3. Apply Environment Overrides
	cfg.applyEnvOverrides()

	return cfg, nil
}

func (c *AppConfig) applyEnvOverrides() {
	if port := os.Getenv("HTTP_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil && p > 0 {
			c.HTTPPort = p
		}
	}
	if name := os.Getenv("DANTE_NAME"); name != "" {
		c.DanteName = name
	} else if name := os.Getenv("INFERNO_NAME"); name != "" {
		c.DanteName = name
	}
	if rate := os.Getenv("SAMPLE_RATE"); rate != "" {
		if r, err := strconv.Atoi(rate); err == nil && r > 0 {
			c.SampleRate = r
		}
	} else if rate := os.Getenv("INFERNO_SAMPLE_RATE"); rate != "" {
		if r, err := strconv.Atoi(rate); err == nil && r > 0 {
			c.SampleRate = r
		}
	}
	if pipe := os.Getenv("PIPE_DIR"); pipe != "" {
		c.PipeDir = pipe
	}
}

func (c *AppConfig) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if path == "" {
		path = "config.yaml"
	}

	_ = os.MkdirAll(filepath.Dir(path), 0755)
	var data []byte
	var err error

	if strings.HasSuffix(path, ".json") {
		data, err = json.MarshalIndent(c, "", "  ")
	} else {
		data, err = yaml.Marshal(c)
	}

	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *AppConfig) AddCustomPreset(p StationPreset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p.IsCustom = true
	c.Stations = append(c.Stations, p)
	c.Presets = c.Stations
}

func (c *AppConfig) DeletePreset(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Stations {
		if p.ID == id && p.IsCustom {
			c.Stations = append(c.Stations[:i], c.Stations[i+1:]...)
			c.Presets = c.Stations
			return true
		}
	}
	return false
}

func (c *AppConfig) GetPreset(id string) (StationPreset, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.Stations {
		if p.ID == id {
			return p, true
		}
	}
	return StationPreset{}, false
}
