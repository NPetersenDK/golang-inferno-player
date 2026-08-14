// Dante Audio Hub Client Application

let systemState = {
  dante_device: "Dante-Pi",
  dante_channels: 8,
  sample_rate: 48000,
  clock_status: "PTP Synced (48.0 kHz)",
  active_streams: 0,
  zones: []
};

let presetsList = [];
let activeZoneId = 1;
let currentCategory = "all";
let searchQuery = "";
let sseSource = null;

// DOM Elements
const zonesContainer = document.getElementById("zonesContainer");
const stationsContainer = document.getElementById("stationsContainer");
const activeZoneSelect = document.getElementById("activeZoneSelect");
const stationSearchInput = document.getElementById("stationSearchInput");
const categoryTabs = document.getElementById("categoryTabs");
const quickP3Btn = document.getElementById("quickP3Btn");
const stopAllBtn = document.getElementById("stopAllBtn");
const openCustomUrlBtn = document.getElementById("openCustomUrlBtn");
const customUrlModal = document.getElementById("customUrlModal");
const closeModalBtn = document.getElementById("closeModalBtn");
const cancelModalBtn = document.getElementById("cancelModalBtn");
const customStreamForm = document.getElementById("customStreamForm");

// Init
document.addEventListener("DOMContentLoaded", () => {
  initSSE();
  fetchPresets();
  setupEventListeners();
});

function setupEventListeners() {
  activeZoneSelect.addEventListener("change", (e) => {
    activeZoneId = parseInt(e.target.value, 10);
    renderZones();
  });

  stationSearchInput.addEventListener("input", (e) => {
    searchQuery = e.target.value.toLowerCase().trim();
    renderStations();
  });

  categoryTabs.addEventListener("click", (e) => {
    if (e.target.classList.contains("tab-btn")) {
      document.querySelectorAll(".tab-btn").forEach(btn => btn.classList.remove("active"));
      e.target.classList.add("active");
      currentCategory = e.target.dataset.category;
      renderStations();
    }
  });

  quickP3Btn.addEventListener("click", () => {
    playPresetOnZone(activeZoneId, "dr-p3");
  });

  stopAllBtn.addEventListener("click", () => {
    fetch("/api/stop-all", { method: "POST" });
  });

  openCustomUrlBtn.addEventListener("click", () => {
    customUrlModal.classList.add("open");
  });

  closeModalBtn.addEventListener("click", () => {
    customUrlModal.classList.remove("open");
  });

  cancelModalBtn.addEventListener("click", () => {
    customUrlModal.classList.remove("open");
  });

  customStreamForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const name = document.getElementById("customStationName").value;
    const url = document.getElementById("customStreamUrl").value;
    const category = document.getElementById("customCategory").value;
    const save = document.getElementById("saveAsPresetCheck").checked;

    if (save) {
      fetch("/api/presets", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, stream_url: url, category, description: "User custom stream" })
      }).then(() => fetchPresets());
    }

    playCustomUrlOnZone(activeZoneId, url, name);
    customUrlModal.classList.remove("open");
    customStreamForm.reset();
  });
}

function initSSE() {
  if (sseSource) {
    sseSource.close();
  }

  sseSource = new EventSource("/api/events");
  sseSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      systemState = data;
      updateHeaderStatus();
      renderZones();
    } catch (err) {
      console.error("SSE parse error", err);
    }
  };

  sseSource.onerror = () => {
    console.warn("SSE connection lost, reconnecting in 2s...");
    sseSource.close();
    setTimeout(initSSE, 2000);
  };
}

function fetchPresets() {
  fetch("/api/presets")
    .then(res => res.json())
    .then(data => {
      presetsList = data;
      renderStations();
    })
    .catch(err => console.error("Error fetching presets:", err));
}

function updateHeaderStatus() {
  const devName = document.getElementById("danteDeviceName");
  const clockText = document.getElementById("danteClockText");
  const flowsText = document.getElementById("danteFlowsText");

  if (devName) devName.textContent = systemState.dante_device || "Dante-Pi";
  if (clockText) clockText.textContent = systemState.clock_status || "PTP Locked (48kHz)";
  if (flowsText) {
    flowsText.textContent = `Persistent (${systemState.dante_channels || 8} Ch)`;
  }
}

function renderZones() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  zonesContainer.innerHTML = "";

  systemState.zones.forEach(zone => {
    const isSelected = (zone.id === activeZoneId);
    const isPlaying = (zone.status === "playing");
    const isBuffering = (zone.status === "buffering");

    const card = document.createElement("div");
    card.className = `zone-card ${isSelected ? "active-control" : ""} ${isPlaying ? "playing" : ""}`;
    card.dataset.zoneId = zone.id;

    let statusBadgeHtml = "";
    if (isPlaying) {
      statusBadgeHtml = `<span class="zone-status-badge status-playing">● Playing</span>`;
    } else if (isBuffering) {
      statusBadgeHtml = `<span class="zone-status-badge status-buffering">◐ Buffering</span>`;
    } else {
      statusBadgeHtml = `<span class="zone-status-badge status-idle">○ Idle</span>`;
    }

    const peakLPct = Math.min(100, Math.round((zone.peak_left || 0) * 100));
    const peakRPct = Math.min(100, Math.round((zone.peak_right || 0) * 100));

    card.innerHTML = `
      <div class="zone-card-top">
        <div class="zone-name-wrap">
          <span class="zone-title">${escapeHtml(zone.name)}</span>
          <span class="zone-dante-route">Dante: ${escapeHtml(zone.dante_left)} & ${escapeHtml(zone.dante_right)}</span>
        </div>
        ${statusBadgeHtml}
      </div>

      <div class="zone-now-playing">
        <div class="now-playing-label">CURRENT AUDIO SOURCE</div>
        <div class="now-playing-title">${zone.station_name ? escapeHtml(zone.station_name) : '<span style="color: var(--text-dim);">No stream active (Dante idle)</span>'}</div>
      </div>

      <div class="vu-meter-wrap">
        <div class="vu-channel">
          <span class="vu-label">L</span>
          <div class="vu-bar-bg">
            <div class="vu-bar-fill" style="width: ${isPlaying ? peakLPct : 0}%"></div>
          </div>
        </div>
        <div class="vu-channel">
          <span class="vu-label">R</span>
          <div class="vu-bar-bg">
            <div class="vu-bar-fill" style="width: ${isPlaying ? peakRPct : 0}%"></div>
          </div>
        </div>
      </div>

      <div class="zone-controls">
        <div class="vol-slider-wrap">
          <button class="btn-icon btn-mute" title="${zone.muted ? 'Unmute' : 'Mute'}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              ${zone.muted || zone.volume === 0 ? 
                '<line x1="1" y1="1" x2="23" y2="23"></line><path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6"></path><path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23"></path>' :
                '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M19.07 4.93a10 10 0 0 1 0 14.14M15.54 8.46a5 5 0 0 1 0 7.07"></path>'
              }
            </svg>
          </button>
          <input type="range" min="0" max="100" value="${zone.volume}" class="vol-slider" />
          <span class="vol-value">${zone.volume}%</span>
        </div>

        ${isPlaying || isBuffering ? `
          <button class="btn btn-danger-outline btn-stop-zone" style="padding: 6px 12px; font-size: 0.78rem;">
            Stop
          </button>
        ` : `
          <button class="btn btn-secondary btn-select-zone" style="padding: 6px 12px; font-size: 0.78rem;">
            Select
          </button>
        `}
      </div>
    `;

    // Event handlers on zone card controls
    const volSlider = card.querySelector(".vol-slider");
    volSlider.addEventListener("input", (e) => {
      const val = parseInt(e.target.value, 10);
      card.querySelector(".vol-value").textContent = `${val}%`;
    });

    volSlider.addEventListener("change", (e) => {
      const val = parseInt(e.target.value, 10);
      setZoneVolume(zone.id, val);
    });

    const muteBtn = card.querySelector(".btn-mute");
    muteBtn.addEventListener("click", () => {
      toggleZoneMute(zone.id);
    });

    const stopBtn = card.querySelector(".btn-stop-zone");
    if (stopBtn) {
      stopBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        stopZone(zone.id);
      });
    }

    const selectBtn = card.querySelector(".btn-select-zone");
    if (selectBtn) {
      selectBtn.addEventListener("click", () => {
        activeZoneId = zone.id;
        activeZoneSelect.value = zone.id;
        renderZones();
      });
    }

    card.addEventListener("click", (e) => {
      if (e.target.tagName === 'INPUT' || e.target.tagName === 'BUTTON' || e.target.closest('button')) return;
      activeZoneId = zone.id;
      activeZoneSelect.value = zone.id;
      renderZones();
    });

    zonesContainer.appendChild(card);
  });
}

function renderStations() {
  stationsContainer.innerHTML = "";

  const filtered = presetsList.filter(preset => {
    const matchCategory = (currentCategory === "all") || 
      (currentCategory === "custom" && preset.is_custom) ||
      (preset.category === currentCategory);

    const matchSearch = !searchQuery || 
      preset.name.toLowerCase().includes(searchQuery) || 
      (preset.description && preset.description.toLowerCase().includes(searchQuery)) ||
      preset.id.toLowerCase().includes(searchQuery);

    return matchCategory && matchSearch;
  });

  if (filtered.length === 0) {
    stationsContainer.innerHTML = `
      <div style="grid-column: 1 / -1; padding: 40px; text-align: center; color: var(--text-dim);">
        <p style="font-size: 16px; margin-bottom: 8px;">No stations found.</p>
        <p style="font-size: 13px; opacity: 0.8;">Add your custom stations to <code>config.yaml</code> or use the "Custom Stream URL" button above.</p>
      </div>
    `;
    return;
  }

  filtered.forEach(preset => {
    const isDR = preset.name.startsWith("DR ");
    const card = document.createElement("div");
    card.className = "station-card";

    card.innerHTML = `
      <div class="station-card-top">
        <div class="station-badge ${isDR ? 'dr-badge' : ''}">${isDR ? preset.name : (preset.category.substring(0, 10))}</div>
        <div class="station-info">
          <div class="station-name">${escapeHtml(preset.name)}</div>
          <div class="station-meta">
            <span>${escapeHtml(preset.category)}</span>
            <span>•</span>
            <span>${escapeHtml(preset.bitrate || '192k')}</span>
          </div>
        </div>
      </div>
      <div class="station-desc">${escapeHtml(preset.description || '')}</div>
      <div class="station-card-actions">
        <button class="btn btn-primary btn-play-station" data-preset-id="${preset.id}">
          <svg viewBox="0 0 24 24" fill="currentColor"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>
          Play on Zone ${activeZoneId}
        </button>
        ${preset.is_custom ? `
          <button class="btn-icon btn-delete-preset" data-preset-id="${preset.id}" title="Delete station" style="margin-left: 8px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
          </button>
        ` : ''}
      </div>
    `;

    card.querySelector(".btn-play-station").addEventListener("click", () => {
      playPresetOnZone(activeZoneId, preset.id);
    });

    const delBtn = card.querySelector(".btn-delete-preset");
    if (delBtn) {
      delBtn.addEventListener("click", () => {
        if (confirm(`Delete custom preset "${preset.name}"?`)) {
          fetch(`/api/presets/${preset.id}`, { method: "DELETE" })
            .then(() => fetchPresets());
        }
      });
    }

    stationsContainer.appendChild(card);
  });
}

function playPresetOnZone(zoneId, presetId) {
  fetch(`/api/zones/${zoneId}/play`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ preset_id: presetId })
  }).catch(err => console.error("Error playing preset:", err));
}

function playCustomUrlOnZone(zoneId, url, title) {
  fetch(`/api/zones/${zoneId}/play`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ url, title })
  }).catch(err => console.error("Error playing custom url:", err));
}

function stopZone(zoneId) {
  fetch(`/api/zones/${zoneId}/stop`, { method: "POST" })
    .catch(err => console.error("Error stopping zone:", err));
}

function setZoneVolume(zoneId, volume) {
  fetch(`/api/zones/${zoneId}/volume`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ volume })
  }).catch(err => console.error("Error setting volume:", err));
}

function toggleZoneMute(zoneId) {
  fetch(`/api/zones/${zoneId}/mute`, { method: "POST" })
    .catch(err => console.error("Error toggling mute:", err));
}

function escapeHtml(text) {
  if (!text) return "";
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}
