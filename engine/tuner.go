package engine

import (
	"fmt"
	"io"
	"log"
	"net/http"
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

	// DAB+ is stereo and FM is not, so the FM stage upmixes and the zone sees one
	// format either way.
	tunerChannels = 2

	// welle-cli only reaches audio through its own HTTP server, so it gets one on
	// loopback. It has to lock onto the ensemble before it will serve anything.
	dabPort         = 7979
	dabReadyTimeout = 40 * time.Second
)

// How long to wait for the zone to have the FIFO open before giving up. A var
// so the test for that path does not have to sit and wait it out.
var fifoOpenTimeout = 5 * time.Second

// TunerState is what the UI and API see.
type TunerState struct {
	Enabled     bool                 `json:"enabled"`
	ZoneID      int                  `json:"zone_id"`
	Tuned       bool                 `json:"tuned"`
	Mode        string               `json:"mode,omitempty"` // "fm" or "dab"
	PresetID    string               `json:"preset_id,omitempty"`
	FrequencyHz int64                `json:"frequency_hz,omitempty"`
	Channel     string               `json:"channel,omitempty"`
	ServiceID   string               `json:"service_id,omitempty"`
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
	cmd     *exec.Cmd // rtl_fm or welle-cli
	filter  *exec.Cmd // ffmpeg, between the receiver and the FIFO
	mode    string
	preset  string
	freqHz  int64
	channel string
	service string
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
	if zone.Source.Channels != tunerChannels || zone.Source.Format != "s16le" {
		log.Printf("[Tuner] zone %d source is %s %d ch, but the tuner emits s16le stereo",
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
		Mode:        t.mode,
		PresetID:    t.preset,
		FrequencyHz: t.freqHz,
		Channel:     t.channel,
		ServiceID:   t.service,
		Error:       t.lastErr,
		Presets:     presets,
	}
}

// TunePreset tunes to a configured preset by id, in whichever mode it declares.
func (t *Tuner) TunePreset(id string) error {
	if t == nil {
		return fmt.Errorf("tuner is not enabled")
	}
	for _, p := range t.cfg.Presets {
		if p.ID != id {
			continue
		}
		if p.IsDAB() {
			if p.Channel == "" || p.ServiceID == "" {
				return fmt.Errorf("preset %q: a dab preset needs both channel and service_id", id)
			}
			return t.tuneDAB(p.ID, p.Channel, p.ServiceID)
		}
		if err := checkFrequency(p.FrequencyHz); err != nil {
			return fmt.Errorf("preset %q: %w", id, err)
		}
		return t.tuneFM(p.ID, p.FrequencyHz)
	}
	return fmt.Errorf("unknown preset %q", id)
}

// TuneFrequency tunes FM to an arbitrary frequency in Hz.
func (t *Tuner) TuneFrequency(hz int64) error {
	if t == nil {
		return fmt.Errorf("tuner is not enabled")
	}
	if err := checkFrequency(hz); err != nil {
		return err
	}
	return t.tuneFM("", hz)
}

// TuneDAB tunes to a service in an arbitrary ensemble, e.g. "12A" and "0x9001".
func (t *Tuner) TuneDAB(channel, serviceID string) error {
	if t == nil {
		return fmt.Errorf("tuner is not enabled")
	}
	if channel == "" || serviceID == "" {
		return fmt.Errorf("both channel and service_id are required")
	}
	return t.tuneDAB("", channel, serviceID)
}

// Services returns welle-cli's view of the tuned ensemble, which is the only
// place the service IDs a DAB preset needs can be read off.
func (t *Tuner) Services() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("tuner is not enabled")
	}
	t.mu.Lock()
	mode := t.mode
	t.mu.Unlock()
	if mode != "dab" {
		return nil, fmt.Errorf("no DAB ensemble is tuned")
	}
	return fetchMux(dabPort, 5*time.Second)
}

// checkFrequency bounds hz to an R820T's range, catching megahertz written into
// a hertz field.
func checkFrequency(hz int64) error {
	if hz < 24_000_000 || hz > 1_766_000_000 {
		return fmt.Errorf("%d Hz is outside the tuner's 24 MHz - 1766 MHz range (96.4 MHz is 96400000)", hz)
	}
	return nil
}

func (t *Tuner) tuneFM(presetID string, hz int64) error {
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

	cmd := exec.Command("rtl_fm", args...)
	cmd.Stderr = logWriter{prefix: "[Tuner]"}

	if err := t.launch(cmd, "rtl_fm", t.fmFilterArgs(), sink, true); err != nil {
		return err
	}

	t.mu.Lock()
	t.mode, t.preset, t.freqHz = "fm", presetID, hz
	t.channel, t.service, t.lastErr = "", "", ""
	t.mu.Unlock()

	log.Printf("[Tuner] FM tuned to %.3f MHz", float64(hz)/1e6)
	t.changed()
	return nil
}

// tuneDAB brings up welle-cli and reads one service back out of its HTTP server.
// welle-cli has no raw output, so this is the only way to a pipe.
func (t *Tuner) tuneDAB(presetID, channel, serviceID string) error {
	t.tuneMu.Lock()
	defer t.tuneMu.Unlock()
	t.stop()

	sink, err := t.openSink()
	if err != nil {
		t.setError(err.Error())
		return err
	}

	// -C 1 decodes a single programme: a Pi cannot keep a whole ensemble going,
	// and -T drops TII decoding it has no use for either.
	args := []string{
		"-w", strconv.Itoa(dabPort),
		"-c", channel,
		"-C", "1",
		"-F", "rtl_sdr",
		"-T",
	}
	gain := "-1" // welle-cli spells auto gain -1, unlike rtl_fm's omitted flag.
	if t.usesGainFlag() {
		gain = t.cfg.Gain
	}
	args = append(args, "-g", gain)

	cmd := exec.Command("welle-cli", args...)
	cmd.Stderr = logWriter{prefix: "[Tuner/DAB]"}
	if err := cmd.Start(); err != nil {
		_ = sink.Close()
		return t.setError(fmt.Sprintf("start welle-cli: %v", err))
	}

	// Serving audio before the ensemble is locked just yields an empty stream, so
	// FFmpeg must not be pointed at it yet.
	if err := waitForEnsemble(dabReadyTimeout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = sink.Close()
		return t.setError(err.Error())
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/mp3/%s", dabPort, serviceID)
	if err := t.launch(cmd, "welle-cli", dabFilterArgs(url, t.cfg.BoostDB), sink, false); err != nil {
		return err
	}

	t.mu.Lock()
	t.mode, t.preset = "dab", presetID
	t.channel, t.service, t.freqHz, t.lastErr = channel, serviceID, 0, ""
	t.mu.Unlock()

	log.Printf("[Tuner] DAB tuned to %s service %s", channel, serviceID)
	t.changed()
	return nil
}

// launch starts the FFmpeg stage and adopts both processes. joinStdout pipes the
// receiver into FFmpeg; welle-cli instead writes nothing and FFmpeg reads its
// HTTP server, so the two are never joined.
func (t *Tuner) launch(cmd *exec.Cmd, name string, filterArgs []string, sink *os.File, joinStdout bool) error {
	filter := exec.Command("ffmpeg", filterArgs...)
	filter.Stdout = sink
	filter.Stderr = logWriter{prefix: "[Tuner/ffmpeg]"}

	var pr, pw *os.File
	if joinStdout {
		// Both ends are *os.File, so the kernel joins them and nothing is copied.
		var err error
		pr, pw, err = os.Pipe()
		if err != nil {
			_ = sink.Close()
			return t.setError(fmt.Sprintf("pipe: %v", err))
		}
		filter.Stdin = pr
		cmd.Stdout = pw
	}

	cleanup := func() {
		if pr != nil {
			_, _ = pr.Close(), pw.Close()
		}
		_ = sink.Close()
	}

	if err := filter.Start(); err != nil {
		cleanup()
		return t.setError(fmt.Sprintf("start ffmpeg: %v", err))
	}
	if joinStdout {
		if err := cmd.Start(); err != nil {
			_ = filter.Process.Kill()
			_ = filter.Wait()
			cleanup()
			return t.setError(fmt.Sprintf("start %s: %v", name, err))
		}
	}
	// The children hold their own copies now. Ours have to go, or ffmpeg never
	// sees EOF when the receiver stops, and the zone never sees it when ffmpeg does.
	cleanup()

	t.mu.Lock()
	t.cmd, t.filter = cmd, filter
	t.mu.Unlock()

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
	return nil
}

// waitForEnsemble polls welle-cli until it reports a service. It deliberately
// does not reap the process to detect an early exit: launch's goroutine is the
// only Wait, and a second one would race it.
func waitForEnsemble(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		body, err := fetchMux(dabPort, 2*time.Second)
		if err == nil && strings.Contains(string(body), "\"sid\"") {
			return nil
		}
	}
	return fmt.Errorf("welle-cli found no ensemble within %s: is there DAB coverage on this channel?", timeout)
}

func fetchMux(port int, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/mux.json", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mux.json: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
	t.mode, t.preset, t.freqHz = "", "", 0
	t.channel, t.service = "", ""
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

// fmFilterArgs builds the ffmpeg stage: cut everything above the 15 kHz FM audio
// band, lift the level if the config asks for it, and upmix to the stereo the
// zone expects.
func (t *Tuner) fmFilterArgs() []string {
	af := fmt.Sprintf("lowpass=f=%d", tunerAudioHz)
	if t.cfg.BoostDB != 0 {
		af += ",volume=" + strconv.FormatFloat(t.cfg.BoostDB, 'g', -1, 64) + "dB"
	}
	rate := strconv.Itoa(tunerOutputRate)
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", rate, "-ac", "1", "-i", "-",
		"-af", af,
		"-f", "s16le", "-ar", rate, "-ac", strconv.Itoa(tunerChannels), "-",
	}
}

// dabFilterArgs decodes welle-cli's HTTP stream. No lowpass: DAB+ is already
// band-limited, and nobody is folding a stereo pilot down into the audio.
func dabFilterArgs(url string, boostDB float64) []string {
	rate := strconv.Itoa(tunerOutputRate)
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay",
		// welle-cli serves an endless stream and closes it when the service drops.
		"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "4",
		"-i", url,
	}
	if boostDB != 0 {
		args = append(args, "-af", "volume="+strconv.FormatFloat(boostDB, 'g', -1, 64)+"dB")
	}
	return append(args,
		"-f", "s16le", "-ar", rate, "-ac", strconv.Itoa(tunerChannels),
		"-flush_packets", "1", "-")
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
