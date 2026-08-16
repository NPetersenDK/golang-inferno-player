package engine

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dante-player/config"
)

// Decoded sounds are cached forever, which only stays affordable because
// SoundboardConfig.MaxSeconds caps how long each one can be.
const (
	soundboardMaxVoices      = 16
	soundboardBytesPerSecond = 48000 * 2 * 4
)

var soundboardExtensions = map[string]bool{
	".mp3": true, ".wav": true, ".ogg": true, ".flac": true,
	".m4a": true, ".aac": true, ".opus": true, ".wma": true,
}

type Sound struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DurationMs int    `json:"duration_ms,omitempty"`
}

// PlayingSound is one sounding pad; the same sound may appear several times.
type PlayingSound struct {
	VoiceID  int    `json:"voice_id"`
	SoundID  string `json:"sound_id"`
	Name     string `json:"name"`
	ZoneID   int    `json:"zone_id"`
	Position int    `json:"position_ms"`
	Length   int    `json:"length_ms"`
}

type SoundboardState struct {
	Enabled bool           `json:"enabled"`
	Path    string         `json:"path,omitempty"`
	Sounds  []Sound        `json:"sounds"`
	Playing []PlayingSound `json:"playing"`
	Error   string         `json:"error,omitempty"`
}

type voice struct {
	id      int
	soundID string
	name    string
	zoneID  int
	samples []int32
	pos     int
}

type cachedSound struct {
	samples []int32
	modTime int64
	size    int64
}

type Soundboard struct {
	cfg    config.SoundboardConfig
	notify func()

	mu      sync.Mutex
	cache   map[string]cachedSound
	voices  []*voice
	scratch []int32
	nextID  int
	lastErr string

	// State() runs on every UI update, far too hot to stat the filesystem.
	listCache []Sound
	listAt    time.Time
}

const soundboardListTTL = 2 * time.Second

// NewSoundboard returns nil when disabled; every method is nil-safe.
func NewSoundboard(cfg *config.AppConfig, notify func()) *Soundboard {
	settings := cfg.SoundboardSettings()
	if settings == nil {
		return nil
	}
	if err := os.MkdirAll(settings.Path, 0o755); err != nil {
		log.Printf("[Soundboard] Cannot use %s: %v", settings.Path, err)
	}
	log.Printf("[Soundboard] Serving sounds from %s (max %d s each)", settings.Path, settings.MaxSeconds)
	return &Soundboard{
		cfg:    *settings,
		notify: notify,
		cache:  make(map[string]cachedSound),
	}
}

func (s *Soundboard) Enabled() bool { return s != nil }

// List re-reads the directory, so a dropped-in file is playable without a
// restart. Results are cached for soundboardListTTL.
func (s *Soundboard) List() []Sound {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.listCache != nil && time.Since(s.listAt) < soundboardListTTL {
		cached := s.listCache
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.cfg.Path)
	if err != nil {
		s.setError(fmt.Sprintf("cannot read %s: %v", s.cfg.Path, err))
		return nil
	}
	s.setError("")

	sounds := make([]Sound, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !soundboardExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		sounds = append(sounds, Sound{
			ID:         name,
			Name:       strings.TrimSuffix(name, filepath.Ext(name)),
			DurationMs: s.cachedDurationMs(name),
		})
	}
	sort.Slice(sounds, func(i, j int) bool {
		return strings.ToLower(sounds[i].Name) < strings.ToLower(sounds[j].Name)
	})

	s.mu.Lock()
	s.listCache = sounds
	s.listAt = time.Now()
	s.mu.Unlock()

	return sounds
}

func (s *Soundboard) cachedDurationMs(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[id]; ok {
		return len(c.samples) / 2 / 48
	}
	return 0
}

// Play layers another voice rather than restarting the sound.
func (s *Soundboard) Play(soundID string, zoneID int) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("soundboard is disabled")
	}

	// Only names the scan turned up are playable, so a crafted ID cannot reach
	// a file outside the directory.
	var match *Sound
	for _, snd := range s.List() {
		if snd.ID == soundID {
			found := snd
			match = &found
			break
		}
	}
	if match == nil {
		return 0, fmt.Errorf("unknown sound %q", soundID)
	}

	samples, err := s.load(match.ID)
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	if len(s.voices) >= soundboardMaxVoices {
		s.mu.Unlock()
		return 0, fmt.Errorf("too many sounds playing at once (max %d)", soundboardMaxVoices)
	}
	s.nextID++
	id := s.nextID
	s.voices = append(s.voices, &voice{
		id:      id,
		soundID: match.ID,
		name:    match.Name,
		zoneID:  zoneID,
		samples: samples,
	})
	s.mu.Unlock()

	s.notifyState()
	return id, nil
}

func (s *Soundboard) StopVoice(voiceID int) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	kept := s.voices[:0]
	removed := false
	for _, v := range s.voices {
		if v.id == voiceID {
			removed = true
			continue
		}
		kept = append(kept, v)
	}
	s.voices = kept
	s.mu.Unlock()

	if removed {
		s.notifyState()
	}
	return removed
}

// StopAll silences every voice. With zoneID zero it covers all zones.
func (s *Soundboard) StopAll(zoneID int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	kept := s.voices[:0]
	removed := false
	for _, v := range s.voices {
		if zoneID == 0 || v.zoneID == zoneID {
			removed = true
			continue
		}
		kept = append(kept, v)
	}
	s.voices = kept
	s.mu.Unlock()

	if removed {
		s.notifyState()
	}
}

// MixInto adds a zone's voices on top of what is already in master and returns
// the peak of what it added, 0 to 1 per channel. Runs 50 times a second on the
// Dante loop, so it allocates nothing beyond the scratch buffer it keeps.
func (s *Soundboard) MixInto(zoneID int, master []byte, chL, chR, numChannels, frames int, gain float64) (peakL, peakR float64) {
	if s == nil {
		return 0, 0
	}

	need := frames * 2

	s.mu.Lock()
	var mix []int32
	for _, v := range s.voices {
		if v.zoneID != zoneID {
			continue
		}
		if mix == nil {
			if cap(s.scratch) < need {
				s.scratch = make([]int32, need)
			}
			mix = s.scratch[:need]
			for i := range mix {
				mix[i] = 0
			}
		}
		remaining := len(v.samples) - v.pos
		if remaining > need {
			remaining = need
		}
		for i := 0; i < remaining; i++ {
			mix[i] = saturateAdd(mix[i], v.samples[v.pos+i])
		}
		v.pos += remaining
	}
	if mix == nil {
		s.mu.Unlock()
		return 0, 0
	}

	kept := s.voices[:0]
	finished := false
	for _, v := range s.voices {
		if v.pos >= len(v.samples) {
			finished = true
			continue
		}
		kept = append(kept, v)
	}
	s.voices = kept
	s.mu.Unlock()

	var maxL, maxR int32
	for f := 0; f < frames; f++ {
		base := f * numChannels * 4
		mixL := mix[f*2]
		mixR := mix[f*2+1]
		if gain != 1.0 {
			mixL = int32(float64(mixL) * gain)
			mixR = int32(float64(mixR) * gain)
		}
		if a := absInt32(mixL); a > maxL {
			maxL = a
		}
		if a := absInt32(mixR); a > maxR {
			maxR = a
		}
		addSample(master, base+chL*4, mixL)
		addSample(master, base+chR*4, mixR)
	}

	if finished {
		s.notifyState()
	}
	return float64(maxL) / float64(math.MaxInt32), float64(maxR) / float64(math.MaxInt32)
}

func addSample(buf []byte, offset int, delta int32) {
	if delta == 0 {
		return
	}
	current := int32(binary.LittleEndian.Uint32(buf[offset : offset+4]))
	binary.LittleEndian.PutUint32(buf[offset:offset+4], uint32(saturateAdd(current, delta)))
}

// saturateAdd clips instead of wrapping, which would tear through zero.
func saturateAdd(a, b int32) int32 {
	sum := int64(a) + int64(b)
	if sum > math.MaxInt32 {
		return math.MaxInt32
	}
	if sum < math.MinInt32 {
		return math.MinInt32
	}
	return int32(sum)
}

type ZoneSounds struct {
	Count int
	Label string
}

// ZoneSummary labels each zone's pads, collapsing repeats into "Airhorn x3".
func (s *Soundboard) ZoneSummary() map[int]ZoneSounds {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.voices) == 0 {
		return nil
	}

	// First-appearance order, or the label reshuffles between updates.
	order := make(map[int][]string)
	counts := make(map[int]map[string]int)
	total := make(map[int]int)

	for _, v := range s.voices {
		if counts[v.zoneID] == nil {
			counts[v.zoneID] = make(map[string]int)
		}
		if counts[v.zoneID][v.name] == 0 {
			order[v.zoneID] = append(order[v.zoneID], v.name)
		}
		counts[v.zoneID][v.name]++
		total[v.zoneID]++
	}

	out := make(map[int]ZoneSounds, len(total))
	for zoneID, names := range order {
		parts := make([]string, 0, len(names))
		for _, name := range names {
			if n := counts[zoneID][name]; n > 1 {
				parts = append(parts, fmt.Sprintf("%s x%d", name, n))
			} else {
				parts = append(parts, name)
			}
		}
		out[zoneID] = ZoneSounds{Count: total[zoneID], Label: strings.Join(parts, ", ")}
	}
	return out
}

func (s *Soundboard) State() SoundboardState {
	if s == nil {
		return SoundboardState{Enabled: false}
	}

	sounds := s.List()

	s.mu.Lock()
	playing := make([]PlayingSound, 0, len(s.voices))
	for _, v := range s.voices {
		playing = append(playing, PlayingSound{
			VoiceID:  v.id,
			SoundID:  v.soundID,
			Name:     v.name,
			ZoneID:   v.zoneID,
			Position: v.pos / 2 / 48,
			Length:   len(v.samples) / 2 / 48,
		})
	}
	lastErr := s.lastErr
	s.mu.Unlock()

	return SoundboardState{
		Enabled: true,
		Path:    s.cfg.Path,
		Sounds:  sounds,
		Playing: playing,
		Error:   lastErr,
	}
}

// load decodes on first use and whenever the file changed underneath us.
func (s *Soundboard) load(id string) ([]int32, error) {
	path := filepath.Join(s.cfg.Path, id)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat sound: %w", err)
	}

	s.mu.Lock()
	if c, ok := s.cache[id]; ok && c.modTime == info.ModTime().UnixNano() && c.size == info.Size() {
		samples := c.samples
		s.mu.Unlock()
		return samples, nil
	}
	s.mu.Unlock()

	samples, err := s.decode(path)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[id] = cachedSound{samples: samples, modTime: info.ModTime().UnixNano(), size: info.Size()}
	s.mu.Unlock()

	log.Printf("[Soundboard] Decoded %s (%d ms)", id, len(samples)/2/48)
	return samples, nil
}

// decode resamples the file onto the Dante rate and layout via FFmpeg.
func (s *Soundboard) decode(path string) ([]int32, error) {
	limit := s.cfg.MaxSeconds * soundboardBytesPerSecond

	cmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-i", path,
		"-vn",
		"-f", "s32le",
		"-ar", "48000",
		"-ac", "2",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open decoder pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("decoder start: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// One byte past the limit separates "too long" from "exactly at the cap".
	raw, err := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("%s is longer than the %d s limit", filepath.Base(path), s.cfg.MaxSeconds)
	}
	if len(raw) < 4 {
		return nil, fmt.Errorf("%s decoded to nothing", filepath.Base(path))
	}

	count := len(raw) / 4
	samples := make([]int32, count)
	for i := 0; i < count; i++ {
		samples[i] = int32(binary.LittleEndian.Uint32(raw[i*4 : (i+1)*4]))
	}
	return samples, nil
}

func (s *Soundboard) setError(msg string) {
	s.mu.Lock()
	changed := s.lastErr != msg
	s.lastErr = msg
	s.mu.Unlock()
	if changed && msg != "" {
		log.Printf("[Soundboard] %s", msg)
	}
}

func (s *Soundboard) notifyState() {
	if s.notify != nil {
		s.notify()
	}
}
