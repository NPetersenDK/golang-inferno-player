package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dante-player/config"
	"dante-player/engine"
)

type Server struct {
	cfg   *config.AppConfig
	mgr   *engine.PlaybackManager
	webFS fs.FS
	mux   *http.ServeMux
}

type PlayRequest struct {
	PresetID    string `json:"preset_id,omitempty"`
	URL         string `json:"url,omitempty"`
	StreamURL   string `json:"stream_url,omitempty"`
	Title       string `json:"title,omitempty"`
	StationName string `json:"station_name,omitempty"`
}

type VolumeRequest struct {
	Volume int `json:"volume"`
}

func NewServer(cfg *config.AppConfig, mgr *engine.PlaybackManager, webFS fs.FS) *Server {
	s := &Server{
		cfg:   cfg,
		mgr:   mgr,
		webFS: webFS,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/presets", s.handlePresets)
	s.mux.HandleFunc("/api/presets/", s.handlePresetItem)
	s.mux.HandleFunc("/api/zones/", s.handleZoneAction)
	s.mux.HandleFunc("/api/stop-all", s.handleStopAll)
	s.mux.HandleFunc("/api/tuner/", s.handleTuner)
	s.mux.HandleFunc("/api/soundboard", s.handleSoundboard)
	s.mux.HandleFunc("/api/soundboard/", s.handleSoundboardAction)
	s.mux.HandleFunc("/api/events", s.handleSSE)

	if s.webFS != nil {
		fileServer := http.FileServer(http.FS(s.webFS))
		s.mux.Handle("/", fileServer)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.mgr.GetStatus())
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.cfg.Stations)
	case http.MethodPost:
		var p config.StationPreset
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if p.ID == "" {
			p.ID = fmt.Sprintf("custom-%d", time.Now().Unix())
		}
		if p.Name == "" || p.StreamURL == "" {
			http.Error(w, "Name and StreamURL are required", http.StatusBadRequest)
			return
		}
		s.cfg.AddCustomPreset(p)
		_ = s.cfg.Save("")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePresetItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/presets/")
	if id == "" {
		http.Error(w, "Preset ID required", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodDelete {
		if ok := s.cfg.DeletePreset(id); ok {
			_ = s.cfg.Save("")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "Preset not found or is default preset", http.StatusNotFound)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleZoneAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	zoneID, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "Invalid zone ID", http.StatusBadRequest)
		return
	}

	action := parts[3]
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "play", "preset":
		var req PlayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		streamURL := req.URL
		if streamURL == "" {
			streamURL = req.StreamURL
		}
		title := req.Title
		if title == "" {
			title = req.StationName
		}

		if req.PresetID != "" {
			if err := s.mgr.PlayZonePreset(zoneID, req.PresetID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if streamURL != "" {
			if err := s.mgr.PlayZoneCustom(zoneID, streamURL, title); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Either preset_id or stream_url required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "zone": zoneID, "action": "play"})

	case "stop":
		if err := s.mgr.StopZone(zoneID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "zone": zoneID, "action": "stop"})

	case "volume":
		var req VolumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.mgr.SetZoneVolume(zoneID, req.Volume); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "zone": zoneID, "volume": req.Volume})

	case "mute":
		isMuted, err := s.mgr.ToggleZoneMute(zoneID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "zone": zoneID, "muted": isMuted})

	default:
		http.Error(w, "Unknown action", http.StatusNotFound)
	}
}

type TuneRequest struct {
	PresetID    string `json:"preset_id,omitempty"`
	FrequencyHz int64  `json:"frequency_hz,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ServiceID   string `json:"service_id,omitempty"`
}

// Tuner state rides along in /api/status, so this only handles commands, plus
// the DAB ensemble listing, which is too large to broadcast with every update.
func (s *Server) handleTuner(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/tuner/")

	if action == "services" {
		mux, err := s.mgr.TunerServices()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mux)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var err error
	switch action {
	case "tune":
		var req TuneRequest
		if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil {
			http.Error(w, derr.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case req.PresetID != "":
			err = s.mgr.TunePreset(req.PresetID)
		case req.Channel != "":
			err = s.mgr.TuneDAB(req.Channel, req.ServiceID)
		case req.FrequencyHz > 0:
			err = s.mgr.TuneFrequency(req.FrequencyHz)
		default:
			http.Error(w, "Either preset_id, frequency_hz or channel required", http.StatusBadRequest)
			return
		}
	case "off":
		s.mgr.TunerOff()
	default:
		http.Error(w, "Unknown action", http.StatusNotFound)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "action": action})
}

type SoundRequest struct {
	SoundID string `json:"sound_id,omitempty"`
	ZoneID  int    `json:"zone_id,omitempty"`
	VoiceID int    `json:"voice_id,omitempty"`
}

// Soundboard state also rides along in /api/status; this lets a controller poll it alone.
func (s *Server) handleSoundboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.mgr.SoundboardState())
}

func (s *Server) handleSoundboardAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/api/soundboard/")

	var req SoundRequest
	// stop-all takes no body, so an empty one is not an error.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	switch action {
	case "play":
		if req.SoundID == "" {
			http.Error(w, "sound_id required", http.StatusBadRequest)
			return
		}
		voiceID, err := s.mgr.PlaySound(req.SoundID, req.ZoneID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "sound_id": req.SoundID, "zone": req.ZoneID, "voice_id": voiceID,
		})

	case "stop":
		if req.VoiceID == 0 {
			http.Error(w, "voice_id required", http.StatusBadRequest)
			return
		}
		if err := s.mgr.StopSound(req.VoiceID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "voice_id": req.VoiceID})

	case "stop-all":
		// Zero means every zone.
		s.mgr.StopSounds(req.ZoneID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "action": "stop_all", "zone": req.ZoneID})

	default:
		http.Error(w, "Unknown action", http.StatusNotFound)
	}
}

func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mgr.StopAll()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "action": "stop_all"})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	statusCh := s.mgr.SubscribeState()
	defer s.mgr.UnsubscribeState(statusCh)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case status, ok := <-statusCh:
			if !ok {
				return
			}
			data, err := json.Marshal(status)
			if err == nil {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.HTTPPort)
	log.Printf("Starting Dante Web Player interface on http://0.0.0.0:%d", s.cfg.HTTPPort)
	return http.ListenAndServe(addr, s)
}
