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

// Omitting buffer_ms must not leave the queue at the station default, or a
// source zone would sit on seconds of latency.
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

// The env knobs exist so latency can be tuned from the compose file without
// editing a mounted config, so they have to beat the file.
func TestSourceBufferEnvOverridesConfig(t *testing.T) {
	t.Setenv("DANTE_SOURCE_PREBUFFER_MS", "150")
	t.Setenv("DANTE_SOURCE_BUFFER_MS", "300")

	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 4
    source:
      type: pipe
      path: /tmp/linein.pcm
      prebuffer_ms: 1000
      buffer_ms: 4000
`))
	if err != nil {
		t.Fatal(err)
	}
	src := cfg.Zones[0].Source
	if src.PrebufferMs != 150 || src.BufferMs != 300 {
		t.Errorf("got prebuffer %d / buffer %d, want 150 / 300", src.PrebufferMs, src.BufferMs)
	}
}

// Overriding only the prebuffer must recompute the default rather than leave
// the file's larger buffer in place.
func TestPrebufferEnvRecomputesBufferDefault(t *testing.T) {
	t.Setenv("DANTE_SOURCE_PREBUFFER_MS", "150")

	cfg, err := LoadConfig(writeConfig(t, `
zones:
  - id: 4
    source:
      type: pipe
      path: /tmp/linein.pcm
      prebuffer_ms: 1000
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Zones[0].Source.BufferMs; got != 300 {
		t.Errorf("buffer_ms %d, want 300 (twice the overridden prebuffer)", got)
	}
}

// A zone without a source block must come out exactly as before.
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
