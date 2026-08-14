package api

import (
	"encoding/json"
	"fmt"
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
	cfg     *config.AppConfig
	mgr     *engine.PlaybackManager
	webFS   fs.FS
	mux     *http.ServeMux
}

type PlayRequest struct {
	PresetID string `json:"preset_id,omitempty"`
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
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
	s.mux.HandleFunc("/api/events", s.handleSSE)

	// Web UI static files
	if s.webFS != nil {
		fileServer := http.FileServer(http.FS(s.webFS))
		s.mux.Handle("/", fileServer)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers for development/remote controllers
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
		_ = json.NewEncoder(w).Encode(s.cfg.Presets)
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
	// Paths:
	// /api/zones/{id}/play
	// /api/zones/{id}/stop
	// /api/zones/{id}/volume
	// /api/zones/{id}/mute
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
	case "play":
		var req PlayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.PresetID != "" {
			if err := s.mgr.PlayZonePreset(zoneID, req.PresetID); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if req.URL != "" {
			if err := s.mgr.PlayZoneCustom(zoneID, req.URL, req.Title); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Either preset_id or url required", http.StatusBadRequest)
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
