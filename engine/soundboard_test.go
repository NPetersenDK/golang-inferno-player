package engine

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"dante-player/config"
)

const testChannels = 8

func newTestBoard() *Soundboard {
	return &Soundboard{
		cache:   make(map[string]cachedSound),
		voices:  make(map[int]*voice),
		warming: make(map[string]bool),
	}
}

// seedSound writes a placeholder file and pre-fills the decode cache for it, so
// Play works without FFmpeg.
func seedSound(t *testing.T, sb *Soundboard, name string, samples []int32) {
	t.Helper()
	path := filepath.Join(sb.cfg.Path, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sb.cache[name] = cachedSound{samples: samples, modTime: info.ModTime().UnixNano(), size: info.Size()}
}

// readFrame pulls one channel's sample out of the interleaved master buffer.
func readFrame(t *testing.T, buf []byte, frame, channel int) int32 {
	t.Helper()
	off := frame*testChannels*4 + channel*4
	return int32(binary.LittleEndian.Uint32(buf[off : off+4]))
}

func makeMaster(frames int) []byte {
	return make([]byte, frames*testChannels*4)
}

func TestPlayReplacesTheCurrentSound(t *testing.T) {
	sb := newTestBoard()
	sb.cfg = config.SoundboardConfig{Enabled: true, Path: t.TempDir(), MaxSeconds: 60}
	seedSound(t, sb, "a.wav", []int32{100, 100})
	seedSound(t, sb, "b.wav", []int32{200, 200})

	first, err := sb.Play("a.wav", 1)
	if err != nil {
		t.Fatalf("Play(a) = %v", err)
	}
	second, err := sb.Play("b.wav", 1)
	if err != nil {
		t.Fatalf("Play(b) = %v", err)
	}

	if len(sb.voices) != 1 {
		t.Fatalf("voices = %d, want 1: a new pad replaces the old one", len(sb.voices))
	}
	if got := sb.voices[1].soundID; got != "b.wav" {
		t.Errorf("sounding %q, want b.wav", got)
	}
	if sb.StopVoice(first) {
		t.Error("the replaced voice is still stoppable, so it was never dropped")
	}
	if !sb.StopVoice(second) {
		t.Error("the current voice should be stoppable")
	}
}

func TestPlayRetriggerRestartsTheSameSound(t *testing.T) {
	sb := newTestBoard()
	sb.cfg = config.SoundboardConfig{Enabled: true, Path: t.TempDir(), MaxSeconds: 60}
	seedSound(t, sb, "a.wav", []int32{10, 10, 20, 20})

	if _, err := sb.Play("a.wav", 1); err != nil {
		t.Fatalf("Play = %v", err)
	}
	sb.MixInto(1, makeMaster(1), 0, 1, testChannels, 1, 1.0)

	if _, err := sb.Play("a.wav", 1); err != nil {
		t.Fatalf("Play again = %v", err)
	}
	master := makeMaster(1)
	sb.MixInto(1, master, 0, 1, testChannels, 1, 1.0)

	if got := readFrame(t, master, 0, 0); got != 10 {
		t.Errorf("frame = %d, want 10: retriggering restarts from the beginning", got)
	}
}

func TestPlayIsPerZone(t *testing.T) {
	sb := newTestBoard()
	sb.cfg = config.SoundboardConfig{Enabled: true, Path: t.TempDir(), MaxSeconds: 60}
	seedSound(t, sb, "a.wav", []int32{100, 100})
	seedSound(t, sb, "b.wav", []int32{200, 200})

	if _, err := sb.Play("a.wav", 1); err != nil {
		t.Fatalf("Play zone 1 = %v", err)
	}
	if _, err := sb.Play("b.wav", 2); err != nil {
		t.Fatalf("Play zone 2 = %v", err)
	}

	if len(sb.voices) != 2 {
		t.Fatalf("voices = %d, want 2: replacing is per zone, not global", len(sb.voices))
	}
}

func TestMixIntoAddsOnTopOfExistingAudio(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{50, 50}}}

	master := makeMaster(1)
	// Pretend the zone already wrote a station into the buffer.
	var station int32 = 1000
	binary.LittleEndian.PutUint32(master[0:4], uint32(station))
	binary.LittleEndian.PutUint32(master[4:8], uint32(-station))

	sb.MixInto(1, master, 0, 1, testChannels, 1, 1.0)

	if got := readFrame(t, master, 0, 0); got != 1050 {
		t.Errorf("left = %d, want 1050: the pad must not replace the station", got)
	}
	if got := readFrame(t, master, 0, 1); got != -950 {
		t.Errorf("right = %d, want -950", got)
	}
}

func TestMixIntoClipsInsteadOfWrapping(t *testing.T) {
	sb := newTestBoard()
	loud := int32(math.MaxInt32 - 10)
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{loud, loud}}}

	// A loud pad over loud music must distort at the ceiling, not tear through
	// zero.
	master := makeMaster(1)
	binary.LittleEndian.PutUint32(master[0:4], uint32(loud))
	sb.MixInto(1, master, 0, 1, testChannels, 1, 1.0)

	if got := readFrame(t, master, 0, 0); got != math.MaxInt32 {
		t.Errorf("left = %d, want MaxInt32", got)
	}
}

func TestMixIntoOnlyTouchesItsOwnZone(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{2: {id: 1, zoneID: 2, samples: []int32{500, 500}}}

	master := makeMaster(1)
	// Zone 1 occupies channels 0/1, zone 2 occupies 2/3.
	sb.MixInto(1, master, 0, 1, testChannels, 1, 1.0)

	if got := readFrame(t, master, 0, 0); got != 0 {
		t.Errorf("zone 1 left = %d, want 0: a zone 2 voice must not leak into zone 1", got)
	}

	sb.MixInto(2, master, 2, 3, testChannels, 1, 1.0)
	if got := readFrame(t, master, 0, 2); got != 500 {
		t.Errorf("zone 2 left = %d, want 500", got)
	}
}

func TestMixIntoAppliesZoneGain(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{1000, 1000}}}

	master := makeMaster(1)
	sb.MixInto(1, master, 0, 1, testChannels, 1, 0.0)

	if got := readFrame(t, master, 0, 0); got != 0 {
		t.Errorf("left = %d, want 0 while the zone is muted", got)
	}
}

func TestMixIntoRetiresFinishedVoices(t *testing.T) {
	sb := newTestBoard()
	// Two frames' worth of audio, consumed by a single four-frame pull.
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{1, 1, 2, 2}}}

	master := makeMaster(4)
	sb.MixInto(1, master, 0, 1, testChannels, 4, 1.0)

	if len(sb.voices) != 0 {
		t.Fatalf("voices left = %d, want 0: a voice that ran out must be dropped", len(sb.voices))
	}
	if got := readFrame(t, master, 3, 0); got != 0 {
		t.Errorf("frame 3 = %d, want 0 past the end of the sound", got)
	}
}

func TestMixIntoAdvancesAcrossCalls(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{10, 10, 20, 20}}}

	first := makeMaster(1)
	sb.MixInto(1, first, 0, 1, testChannels, 1, 1.0)
	second := makeMaster(1)
	sb.MixInto(1, second, 0, 1, testChannels, 1, 1.0)

	if got := readFrame(t, first, 0, 0); got != 10 {
		t.Errorf("first call = %d, want 10", got)
	}
	if got := readFrame(t, second, 0, 0); got != 20 {
		t.Errorf("second call = %d, want 20: playback must continue where it left off", got)
	}
}

func TestMixIntoReportsPeakSoTheMetersMove(t *testing.T) {
	sb := newTestBoard()
	half := int32(math.MaxInt32 / 2)
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{half, 0}}}

	peakL, peakR := sb.MixInto(1, makeMaster(1), 0, 1, testChannels, 1, 1.0)
	if peakL < 0.49 || peakL > 0.51 {
		t.Errorf("peakL = %v, want about 0.5", peakL)
	}
	if peakR != 0 {
		t.Errorf("peakR = %v, want 0: the right channel was silent", peakR)
	}

	clear(sb.voices)
	if l, r := sb.MixInto(1, makeMaster(1), 0, 1, testChannels, 1, 1.0); l != 0 || r != 0 {
		t.Errorf("peaks = %v/%v with no voices, want 0/0", l, r)
	}
}

func TestMixIntoPeakFollowsZoneGain(t *testing.T) {
	sb := newTestBoard()
	full := int32(math.MaxInt32)
	sb.voices = map[int]*voice{1: {id: 1, zoneID: 1, samples: []int32{full, full}}}

	peakL, _ := sb.MixInto(1, makeMaster(1), 0, 1, testChannels, 1, 0.0)
	if peakL != 0 {
		t.Errorf("peakL = %v while muted, want 0", peakL)
	}
}

func TestZoneSummaryNamesThePadPerZone(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{
		2: {id: 1, zoneID: 2, name: "Airhorn", samples: []int32{1}},
		3: {id: 2, zoneID: 3, name: "Drumroll", samples: []int32{1}},
	}

	summary := sb.ZoneSummary()
	if got := summary[2]; got != "Airhorn" {
		t.Errorf("zone 2 = %q, want Airhorn", got)
	}
	if got := summary[3]; got != "Drumroll" {
		t.Errorf("zone 3 = %q, want Drumroll", got)
	}
	if _, ok := summary[1]; ok {
		t.Error("zone 1 has no pad and should not appear in the summary")
	}

	clear(sb.voices)
	if summary := sb.ZoneSummary(); summary != nil {
		t.Errorf("ZoneSummary() = %v with nothing playing, want nil", summary)
	}
}

func TestStopAllByZone(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{
		1: {id: 1, zoneID: 1, samples: []int32{1, 1}},
		2: {id: 2, zoneID: 2, samples: []int32{1, 1}},
	}

	sb.StopAll(1)
	if len(sb.voices) != 1 || sb.voices[2] == nil {
		t.Fatalf("voices = %+v, want only the zone 2 voice left", sb.voices)
	}

	sb.StopAll(0)
	if len(sb.voices) != 0 {
		t.Errorf("voices = %d, want 0: zone 0 means every zone", len(sb.voices))
	}
}

func TestStopVoiceReportsWhetherItWasPlaying(t *testing.T) {
	sb := newTestBoard()
	sb.voices = map[int]*voice{1: {id: 7, zoneID: 1, samples: []int32{1, 1}}}

	if !sb.StopVoice(7) {
		t.Error("StopVoice(7) = false, want true for a sounding voice")
	}
	if sb.StopVoice(7) {
		t.Error("StopVoice(7) = true on the second call, want false")
	}
}

func TestNilSoundboardIsInert(t *testing.T) {
	var sb *Soundboard

	if sb.Enabled() {
		t.Error("Enabled() = true on a nil soundboard")
	}
	if got := sb.List(); got != nil {
		t.Errorf("List() = %v, want nil", got)
	}
	if _, err := sb.Play("x", 1); err == nil {
		t.Error("Play() on a nil soundboard should report the feature is off")
	}
	sb.StopAll(0)
	if l, r := sb.MixInto(1, makeMaster(1), 0, 1, testChannels, 1, 1.0); l != 0 || r != 0 {
		t.Errorf("MixInto on a nil soundboard = %v/%v, want 0/0", l, r)
	}
	if sb.ZoneSummary() != nil {
		t.Error("ZoneSummary() on a nil soundboard should be nil")
	}
	if st := sb.State(); st.Enabled {
		t.Error("State().Enabled = true on a nil soundboard")
	}
}

func TestListSkipsNonAudioAndSortsByName(t *testing.T) {
	dir := t.TempDir()
	sb := newTestBoard()
	sb.cfg = config.SoundboardConfig{Enabled: true, Path: dir, MaxSeconds: 60}

	// Seeded rather than written raw, so the scan's background warm-up finds
	// them cached and never reaches for FFmpeg.
	seedSound(t, sb, "Zebra.mp3", []int32{1, 1})
	seedSound(t, sb, "alpha.wav", []int32{1, 1})
	for _, name := range []string{"notes.txt", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sounds := sb.List()
	if len(sounds) != 2 {
		t.Fatalf("got %d sounds, want 2 (the text and image files are not audio): %+v", len(sounds), sounds)
	}
	if sounds[0].Name != "alpha" || sounds[1].Name != "Zebra" {
		t.Errorf("order = %q, %q; want alpha then Zebra, sorted case-insensitively", sounds[0].Name, sounds[1].Name)
	}
	if sounds[0].ID != "alpha.wav" {
		t.Errorf("ID = %q, want the filename including its extension", sounds[0].ID)
	}
}

func TestPlayRejectsUnknownSound(t *testing.T) {
	sb := newTestBoard()
	sb.cfg = config.SoundboardConfig{Enabled: true, Path: t.TempDir(), MaxSeconds: 60}

	// A crafted ID must not reach a file outside the directory.
	for _, id := range []string{"missing.mp3", "../../etc/passwd", "/etc/passwd"} {
		if _, err := sb.Play(id, 1); err == nil {
			t.Errorf("Play(%q) succeeded, want an error", id)
		}
	}
}

func TestSoundboardSettingsDefaults(t *testing.T) {
	cfg := &config.AppConfig{DataDir: "/opt/dante-player/data"}
	got := cfg.SoundboardSettings()
	if got == nil {
		t.Fatal("SoundboardSettings() = nil, want the feature on by default")
	}
	if want := filepath.Join("/opt/dante-player/data", "sounds"); got.Path != want {
		t.Errorf("Path = %q, want %q", got.Path, want)
	}
	if got.MaxSeconds != 60 {
		t.Errorf("MaxSeconds = %d, want 60", got.MaxSeconds)
	}

	cfg.Soundboard = &config.SoundboardConfig{Enabled: false}
	if cfg.SoundboardSettings() != nil {
		t.Error("SoundboardSettings() with enabled:false should report the feature off")
	}
}
