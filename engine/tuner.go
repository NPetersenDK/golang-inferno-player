package engine

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"dante-player/config"
)

// Broadcast FM, fixed rather than left to rtl_fm's own defaults: 240 kHz in
// decimates evenly by 5 to the 48 kHz Dante runs at, and everything above
// 15 kHz is the 19 kHz stereo pilot and noise, never programme. Left at the
// -M wbfm default of 32 kHz out, that pilot folds down to 13 kHz and whistles.
const (
	tunerInputRate  = 240000
	tunerOutputRate = 48000
	tunerAudioHz    = 15000
)

// How long to wait for the zone to have the FIFO open before giving up. A var
// so the test for that path does not have to sit and wait it out.
var fifoOpenTimeout = 5 * time.Second

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

// Tuner drives an SDR into a zone's source FIFO. Retuning restarts rtl_fm, which
// costs the zone its prebuffer, so it reports idle for a moment.
type Tuner struct {
	cfg      config.TunerConfig
	fifoPath string
	zoneID   int
	zoneRate int

	// One tune at a time. Held across process startup, which can block, so it
	// is never the lock the API waits behind.
	tuneMu sync.Mutex

	// mu guards the fields below and is never held across a blocking call:
	// State() takes it, and /api/status takes State().
	mu      sync.Mutex
	cmd     *exec.Cmd // rtl_fm
	filter  *exec.Cmd // ffmpeg, between rtl_fm and the FIFO
	preset  string
	freqHz  int64
	lastErr string

	notify func()
}

// NewTuner returns nil unless the tuner is enabled and its zone has a pipe source.
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
		log.Printf("[Tuner] zone %d source is %s %d ch, but the tuner emits s16le mono",
			zone.ID, zone.Source.Format, zone.Source.Channels)
	}
	if zone.Source.SampleRate != tunerOutputRate {
		log.Printf("[Tuner] zone %d declares %d Hz but the tuner emits %d Hz. "+
			"Set sample_rate: %d on that source, or it will only ever sound like noise.",
			zone.ID, zone.Source.SampleRate, tunerOutputRate, tunerOutputRate)
	}

	t := &Tuner{
		cfg:      *cfg.Tuner,
		fifoPath: zone.Source.Path,
		zoneID:   zone.ID,
		zoneRate: zone.Source.SampleRate,
		notify:   notify,
	}
	log.Printf("[Tuner] enabled on zone %d, device %q, %d presets",
		t.cfg.ZoneID, t.deviceArg(), len(t.cfg.Presets))
	return t
}

// usesGainFlag reports whether -g should be passed at all. rtl_fm's -g takes dB
// and reaches auto gain only by omitting the flag; "auto" would parse as 0 dB,
// the lowest gain the hardware has.
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

// checkFrequency bounds hz to an R820T's range, catching megahertz written into
// a hertz field.
func checkFrequency(hz int64) error {
	if hz < 24_000_000 || hz > 1_766_000_000 {
		return fmt.Errorf("%d Hz is outside the tuner's 24 MHz - 1766 MHz range (96.4 MHz is 96400000)", hz)
	}
	return nil
}

func (t *Tuner) tune(presetID string, hz int64) error {
	t.tuneMu.Lock()
	defer t.tuneMu.Unlock()

	t.stop()

	sink, err := t.openSink()
	if err != nil {
		t.setError(err.Error())
		return err
	}

	args := []string{
		"-d", t.deviceArg(),
		"-f", strconv.FormatInt(hz, 10),
		"-M", "wbfm",
		"-s", strconv.Itoa(tunerInputRate),
		"-r", strconv.Itoa(tunerOutputRate),
	}
	if t.usesGainFlag() {
		args = append(args, "-g", t.cfg.Gain)
	}
	if t.cfg.Squelch > 0 {
		args = append(args, "-l", strconv.Itoa(t.cfg.Squelch))
	}
	args = append(args, "-")

	// rtl_fm writes into ffmpeg, which filters and lifts before the FIFO. Both
	// ends are *os.File, so the kernel joins them and nothing is copied here.
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = sink.Close()
		return t.setError(fmt.Sprintf("pipe: %v", err))
	}

	filter := exec.Command("ffmpeg", t.filterArgs()...)
	filter.Stdin = pr
	filter.Stdout = sink
	filter.Stderr = logWriter{prefix: "[Tuner/ffmpeg]"}

	cmd := exec.Command("rtl_fm", args...)
	cmd.Stdout = pw
	cmd.Stderr = logWriter{prefix: "[Tuner]"}

	if err := filter.Start(); err != nil {
		_, _ = pr.Close(), pw.Close()
		_ = sink.Close()
		return t.setError(fmt.Sprintf("start ffmpeg: %v", err))
	}
	if err := cmd.Start(); err != nil {
		_ = filter.Process.Kill()
		_ = filter.Wait()
		_, _ = pr.Close(), pw.Close()
		_ = sink.Close()
		return t.setError(fmt.Sprintf("start rtl_fm: %v", err))
	}
	// The children hold their own copies now. Ours have to go, or ffmpeg never
	// sees EOF when rtl_fm stops, and the zone never sees it when ffmpeg does.
	_, _ = pr.Close(), pw.Close()
	_ = sink.Close()

	t.mu.Lock()
	t.cmd, t.filter = cmd, filter
	t.preset, t.freqHz, t.lastErr = presetID, hz, ""
	t.mu.Unlock()
	log.Printf("[Tuner] tuned to %.3f MHz", float64(hz)/1e6)

	// The only Wait on these two, so nothing else can block on a process this
	// goroutine has already reaped.
	go func() {
		_ = cmd.Wait()
		if filter.Process != nil {
			_ = filter.Process.Kill()
		}
		_ = filter.Wait()

		t.mu.Lock()
		if t.cmd == cmd {
			t.cmd, t.filter = nil, nil
			t.lastErr = "receiver exited"
		}
		t.mu.Unlock()
		t.changed()
	}()

	t.changed()
	return nil
}

// openSink opens the FIFO the zone reads from. That blocks until the zone has
// its end open, so it happens without the state lock and gives up rather than
// leaving the API waiting behind it forever.
func (t *Tuner) openSink() (*os.File, error) {
	type opened struct {
		file *os.File
		err  error
	}
	ch := make(chan opened, 1)
	go func() {
		f, err := os.OpenFile(t.fifoPath, os.O_WRONLY, 0)
		ch <- opened{f, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("open %s: %w", t.fifoPath, r.err)
		}
		return r.file, nil
	case <-time.After(fifoOpenTimeout):
		// If a reader ever turns up, that goroutine returns and tidies up.
		go func() {
			if r := <-ch; r.file != nil {
				_ = r.file.Close()
			}
		}()
		return nil, fmt.Errorf("nothing is reading %s after %s: is zone %d running?",
			t.fifoPath, fifoOpenTimeout, t.zoneID)
	}
}

// setError records why a tune failed and returns it for the caller to pass on.
func (t *Tuner) setError(msg string) error {
	t.mu.Lock()
	t.lastErr = msg
	t.mu.Unlock()
	return fmt.Errorf("%s", msg)
}

// Off stops the receiver, leaving the zone silent.
func (t *Tuner) Off() {
	if t == nil {
		return
	}
	t.tuneMu.Lock()
	t.stop()
	t.mu.Lock()
	t.preset, t.freqHz = "", 0
	t.mu.Unlock()
	t.tuneMu.Unlock()
	t.changed()
}

// stop kills the receiver but does not reap it: the goroutine tune started is
// the only Wait, so no two callers ever wait on the same process.
func (t *Tuner) stop() {
	t.mu.Lock()
	cmd, filter := t.cmd, t.filter
	t.cmd, t.filter = nil, nil
	t.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if filter != nil && filter.Process != nil {
		_ = filter.Process.Kill()
	}
}

func (t *Tuner) changed() {
	if t.notify != nil {
		t.notify()
	}
}

// filterArgs builds the ffmpeg stage: cut everything above the 15 kHz FM audio
// band, then lift the level if the config asks for it.
func (t *Tuner) filterArgs() []string {
	af := fmt.Sprintf("lowpass=f=%d", tunerAudioHz)
	if t.cfg.BoostDB != 0 {
		af += ",volume=" + strconv.FormatFloat(t.cfg.BoostDB, 'g', -1, 64) + "dB"
	}
	rate := strconv.Itoa(tunerOutputRate)
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", rate, "-ac", "1", "-i", "-",
		"-af", af,
		"-f", "s16le", "-ar", rate, "-ac", "1", "-",
	}
}

// logWriter forwards a child's stderr into our log a line at a time.
type logWriter struct {
	prefix string
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		log.Printf("%s %s", w.prefix, line)
	}
	return len(p), nil
}
