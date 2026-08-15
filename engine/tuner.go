package engine

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"dante-player/config"
)

// rtl_fm announces its output rate on stderr, and which rate -M wbfm settles on
// varies between builds. Rather than hardcode a guess, we read that line and
// compare it against what the zone was told to expect.
var rtlOutputRate = regexp.MustCompile(`Output at (\d+) Hz`)

// TunerState is what the UI and API see.
type TunerState struct {
	Enabled     bool                 `json:"enabled"`
	ZoneID      int                  `json:"zone_id"`
	Tuned       bool                 `json:"tuned"`
	PresetID    string               `json:"preset_id,omitempty"`
	FrequencyHz int64                `json:"frequency_hz,omitempty"`
	Error       string               `json:"error,omitempty"`
	Presets     []config.TunerPreset `json:"presets"`
}

// Tuner drives an SDR into a zone's source FIFO.
//
// Retuning restarts the receiver, because that is all rtl_fm offers. The gap
// costs the zone its prebuffer, so it reports idle for a moment and comes back
// on its own.
type Tuner struct {
	cfg      config.TunerConfig
	fifoPath string
	zoneID   int
	zoneRate int

	mu      sync.Mutex
	cmd     *exec.Cmd
	sink    *os.File
	preset  string
	freqHz  int64
	lastErr string

	notify func()
}

// NewTuner returns nil unless the tuner is enabled and its zone is a realtime
// pipe source, so a misconfigured or unwanted tuner simply does not exist.
func NewTuner(cfg *config.AppConfig, notify func()) *Tuner {
	if cfg.Tuner == nil || !cfg.Tuner.Enabled {
		return nil
	}

	var zone *config.ZoneConfig
	for i := range cfg.Zones {
		if cfg.Zones[i].ID == cfg.Tuner.ZoneID {
			zone = &cfg.Zones[i]
			break
		}
	}
	if zone == nil || zone.Source == nil || zone.Source.Type != "pipe" {
		log.Printf("[Tuner] disabled: zone %d has no pipe source to feed", cfg.Tuner.ZoneID)
		return nil
	}
	if !zone.Source.Realtime {
		log.Printf("[Tuner] zone %d source is not marked realtime; an SDR cannot be paused and will overrun", zone.ID)
	}
	if zone.Source.Channels != 1 || zone.Source.Format != "s16le" {
		log.Printf("[Tuner] zone %d source is %s %d ch, but rtl_fm emits s16le mono",
			zone.ID, zone.Source.Format, zone.Source.Channels)
	}

	t := &Tuner{
		cfg:      *cfg.Tuner,
		fifoPath: zone.Source.Path,
		zoneID:   zone.ID,
		zoneRate: zone.Source.SampleRate,
		notify:   notify,
	}
	// rtl_fm lists every attached device with its serial on stderr each time it
	// starts, and that lands in this log, so tuning once is enough to find the
	// value for "device".
	log.Printf("[Tuner] enabled on zone %d, device %q, %d presets",
		t.cfg.ZoneID, t.deviceArg(), len(t.cfg.Presets))
	return t
}

// usesGainFlag reports whether -g should be passed at all.
//
// rtl_fm's -g takes a number in dB, and automatic gain is its default, reached
// only by omitting the flag. Passing a word parses as 0, the lowest gain the
// hardware has, which is indistinguishable from no signal.
func (t *Tuner) usesGainFlag() bool {
	return t.cfg.Gain != "" && !strings.EqualFold(t.cfg.Gain, "auto")
}

// deviceArg is what rtl_fm's -d receives: an index or a serial.
func (t *Tuner) deviceArg() string {
	if t.cfg.Device == "" {
		return "0"
	}
	return t.cfg.Device
}

func (t *Tuner) State() TunerState {
	if t == nil {
		return TunerState{Presets: []config.TunerPreset{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	presets := t.cfg.Presets
	if presets == nil {
		presets = []config.TunerPreset{}
	}
	return TunerState{
		Enabled:     true,
		ZoneID:      t.cfg.ZoneID,
		Tuned:       t.cmd != nil,
		PresetID:    t.preset,
		FrequencyHz: t.freqHz,
		Error:       t.lastErr,
		Presets:     presets,
	}
}

// TunePreset tunes to a configured preset by id.
func (t *Tuner) TunePreset(id string) error {
	if t == nil {
		return fmt.Errorf("tuner is not enabled")
	}
	for _, p := range t.cfg.Presets {
		if p.ID == id {
			if err := checkFrequency(p.FrequencyHz); err != nil {
				return fmt.Errorf("preset %q: %w", id, err)
			}
			return t.tune(p.ID, p.FrequencyHz)
		}
	}
	return fmt.Errorf("unknown preset %q", id)
}

// TuneFrequency tunes to an arbitrary frequency in Hz.
func (t *Tuner) TuneFrequency(hz int64) error {
	if t == nil {
		return fmt.Errorf("tuner is not enabled")
	}
	if err := checkFrequency(hz); err != nil {
		return err
	}
	return t.tune("", hz)
}

// checkFrequency bounds the value to what an R820T tuner covers. The common
// mistake is writing megahertz into a hertz field, which this catches.
func checkFrequency(hz int64) error {
	if hz < 24_000_000 || hz > 1_766_000_000 {
		return fmt.Errorf("%d Hz is outside the tuner's 24 MHz - 1766 MHz range (96.4 MHz is 96400000)", hz)
	}
	return nil
}

func (t *Tuner) tune(presetID string, hz int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stopLocked()

	sink, err := os.OpenFile(t.fifoPath, os.O_WRONLY, 0)
	if err != nil {
		t.lastErr = fmt.Sprintf("open %s: %v", t.fifoPath, err)
		return fmt.Errorf("%s", t.lastErr)
	}

	// Plain -M wbfm, with no rate flags of our own: it is the documented
	// broadcast FM mode and outputs mono s16le at TunerOutputRate, which FFmpeg
	// resamples onto the Dante clock. Overriding -s and -r here produced a
	// stream at a rate the zone was not told about.
	args := []string{
		"-d", t.deviceArg(),
		"-f", strconv.FormatInt(hz, 10),
		"-M", "wbfm",
	}
	if t.usesGainFlag() {
		args = append(args, "-g", t.cfg.Gain)
	}
	if t.cfg.Squelch > 0 {
		args = append(args, "-l", strconv.Itoa(t.cfg.Squelch))
	}
	args = append(args, "-")

	cmd := exec.Command("rtl_fm", args...)
	cmd.Stdout = sink
	cmd.Stderr = logWriter{prefix: "[Tuner]", inspect: t.checkOutputRate}
	if err := cmd.Start(); err != nil {
		_ = sink.Close()
		t.lastErr = fmt.Sprintf("start rtl_fm: %v", err)
		return fmt.Errorf("%s", t.lastErr)
	}

	t.cmd, t.sink, t.preset, t.freqHz, t.lastErr = cmd, sink, presetID, hz, ""
	log.Printf("[Tuner] tuned to %.3f MHz", float64(hz)/1e6)

	go func() {
		_ = cmd.Wait()
		t.mu.Lock()
		if t.cmd == cmd {
			t.cmd = nil
			t.lastErr = "receiver exited"
		}
		t.mu.Unlock()
		t.changed()
	}()

	t.changed()
	return nil
}

// Off stops the receiver, leaving the zone silent.
func (t *Tuner) Off() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.stopLocked()
	t.preset, t.freqHz = "", 0
	t.mu.Unlock()
	t.changed()
}

func (t *Tuner) stopLocked() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}
	t.cmd = nil
	if t.sink != nil {
		_ = t.sink.Close()
		t.sink = nil
	}
}

func (t *Tuner) changed() {
	if t.notify != nil {
		t.notify()
	}
}

// checkOutputRate compares the rate rtl_fm reports against what the zone was
// configured for. Getting this wrong is not subtle in the log and completely
// inaudible in the audio, where it just sounds like a bad signal.
func (t *Tuner) checkOutputRate(line string) {
	m := rtlOutputRate.FindStringSubmatch(line)
	if m == nil {
		return
	}
	rate, err := strconv.Atoi(m[1])
	if err != nil || rate == t.zoneRate {
		return
	}
	log.Printf("[Tuner] RATE MISMATCH: rtl_fm outputs %d Hz but zone %d declares %d Hz. "+
		"Set sample_rate: %d on that source, or it will only ever sound like noise.",
		rate, t.zoneID, t.zoneRate, rate)
}

// logWriter forwards a child process's stderr into our log, a line at a time,
// optionally handing each line to a hook.
type logWriter struct {
	prefix  string
	inspect func(line string)
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		log.Printf("%s %s", w.prefix, line)
		if w.inspect != nil {
			w.inspect(line)
		}
	}
	return len(p), nil
}
