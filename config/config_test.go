package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Falling back to the station default would leave a source zone on seconds of latency.
func TestSourceBufferDefaultsToTwicePrebuffer(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 4
    name: Line In
    source:
      type: pipe
      path: /tmp/linein.pcm
      prebuffer_ms: 300
`))
	if err != nil {
		t.Fatal(err)
	}

	src := cfg.Zones[0].Source
	if src == nil {
		t.Fatal("source block was dropped")
	}
	if src.BufferMs != 600 {
		t.Errorf("buffer_ms %d, want 600 (twice the prebuffer)", src.BufferMs)
	}
	if src.Format != "s16le" || src.SampleRate != 44100 || src.Channels != 2 {
		t.Errorf("PCM defaults wrong: %s %d Hz %d ch", src.Format, src.SampleRate, src.Channels)
	}
	if src.Label != "Line In" {
		t.Errorf("label %q, want the zone name", src.Label)
	}
}

func TestExplicitBufferIsKept(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 4
    source:
      type: pipe
      path: /tmp/linein.pcm
      prebuffer_ms: 200
      buffer_ms: 1000
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Zones[0].Source.BufferMs; got != 1000 {
		t.Errorf("buffer_ms %d, want the configured 1000", got)
	}
}

// The env knobs are defaults for zones that say nothing: an interactive producer
// and a radio have opposite needs, so a zone stating its own values must keep them.
func TestSourceBufferEnvIsOnlyADefault(t *testing.T) {
	t.Setenv("DANTE_SOURCE_PREBUFFER_MS", "150")
	t.Setenv("DANTE_SOURCE_BUFFER_MS", "300")

	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 3
    source:
      type: pipe
      path: /tmp/radio.pcm
      realtime: true
      prebuffer_ms: 1000
      buffer_ms: 4000
  - id: 4
    source:
      type: pipe
      path: /tmp/linein.pcm
`))
	if err != nil {
		t.Fatal(err)
	}

	radio := cfg.Zones[0].Source
	if radio.PrebufferMs != 1000 || radio.BufferMs != 4000 {
		t.Errorf("configured zone got prebuffer %d / buffer %d, want its own 1000 / 4000",
			radio.PrebufferMs, radio.BufferMs)
	}

	quiet := cfg.Zones[1].Source
	if quiet.PrebufferMs != 150 || quiet.BufferMs != 300 {
		t.Errorf("unconfigured zone got prebuffer %d / buffer %d, want the env 150 / 300",
			quiet.PrebufferMs, quiet.BufferMs)
	}
}

func TestEnvPrebufferGetsItsOwnBufferDefault(t *testing.T) {
	t.Setenv("DANTE_SOURCE_PREBUFFER_MS", "150")

	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 4
    source:
      type: pipe
      path: /tmp/linein.pcm
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Zones[0].Source.BufferMs; got != 300 {
		t.Errorf("buffer_ms %d, want 300 (twice the env prebuffer)", got)
	}
}

func TestRealtimeSourceIsParsed(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 3
    source:
      type: pipe
      path: /tmp/radio.pcm
      realtime: true
      channels: 1
      sample_rate: 48000
`))
	if err != nil {
		t.Fatal(err)
	}
	src := cfg.Zones[0].Source
	if !src.Realtime {
		t.Error("realtime was dropped")
	}
	if src.Channels != 1 {
		t.Errorf("channels %d, want the configured 1 (mono upmixed by FFmpeg)", src.Channels)
	}
}

func TestTunerIsOffByDefault(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, "zones:\n  - id: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tuner != nil && cfg.Tuner.Enabled {
		t.Error("tuner enabled without being configured")
	}
}

func TestTunerEnvEnablesIt(t *testing.T) {
	t.Setenv("DANTE_TUNER_ENABLED", "1")
	cfg, err := LoadConfig(writeConfig(t, "zones:\n  - id: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tuner == nil || !cfg.Tuner.Enabled {
		t.Error("DANTE_TUNER_ENABLED did not enable the tuner")
	}
}

func TestZoneWithoutSourceKeepsDefaults(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 1
    name: Zone 1
`))
	if err != nil {
		t.Fatal(err)
	}
	zone := cfg.Zones[0]
	if zone.Source != nil {
		t.Error("a source block appeared where none was configured")
	}
	if zone.PipePath == "" || zone.AlsaDevice == "" {
		t.Errorf("derived fields missing: %+v", zone)
	}
}
