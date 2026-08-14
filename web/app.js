// ==============================================================================
// Dante Streamer - Frontend Controller (Clean & Maintainable)
// ==============================================================================

let systemState = { zones: [] };
let stationsList = [];
let activeZoneId = 1;
let currentCategory = "all";
let searchQuery = "";
let sseSource = null;

// DOM Elements
const zonesList = document.getElementById("zonesList");
const stationsGrid = document.getElementById("stationsGrid");
const categoryNav = document.getElementById("categoryNav");
const stationSearchInput = document.getElementById("stationSearchInput");
const targetZoneSelect = document.getElementById("targetZoneSelect");
const modalZoneSelect = document.getElementById("modalZoneSelect");
const clockStatusText = document.getElementById("clockStatusText");
const danteDeviceText = document.getElementById("danteDeviceText");
const activeZonesCount = document.getElementById("activeZonesCount");
const stopAllBtn = document.getElementById("stopAllBtn");
const customStreamForm = document.getElementById("customStreamForm");

document.addEventListener("DOMContentLoaded", () => {
  initSSE();
  fetchStations();
  setupEventListeners();
});

function setupEventListeners() {
  targetZoneSelect.addEventListener("change", (e) => {
    activeZoneId = parseInt(e.target.value, 10);
    renderZones();
  });

  stationSearchInput.addEventListener("input", (e) => {
    searchQuery = e.target.value.toLowerCase().trim();
    renderStations();
  });

  categoryNav.addEventListener("click", (e) => {
    const link = e.target.closest(".nav-link");
    if (link) {
      document.querySelectorAll("#categoryNav .nav-link").forEach(el => el.classList.remove("active"));
      link.classList.add("active");
      currentCategory = link.dataset.category;
      renderStations();
    }
  });

  stopAllBtn.addEventListener("click", () => {
    fetch("/api/stop-all", { method: "POST" });
  });

  customStreamForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const zoneId = parseInt(modalZoneSelect.value, 10) || activeZoneId;
    const url = document.getElementById("customUrlInput").value.trim();
    const name = document.getElementById("customNameInput").value.trim() || "Custom Stream";

    if (!url) return;

    fetch(`/api/zones/${zoneId}/play`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stream_url: url, station_name: name })
    });

    const modalEl = document.getElementById("customUrlModal");
    const modal = bootstrap.Modal.getInstance(modalEl);
    if (modal) modal.hide();
    customStreamForm.reset();
  });
}

function initSSE() {
  if (sseSource) sseSource.close();

  sseSource = new EventSource("/api/events");
  sseSource.onmessage = (event) => {
    try {
      systemState = JSON.parse(event.data);
      updateHeader();
      renderZones();
    } catch (err) {
      console.error("SSE parse error:", err);
    }
  };

  sseSource.onerror = () => {
    sseSource.close();
    setTimeout(initSSE, 2500);
  };
}

function fetchStations() {
  fetch("/api/presets")
    .then(res => res.json())
    .then(data => {
      stationsList = data || [];
      renderCategoryPills();
      renderStations();
    })
    .catch(err => console.error("Error fetching stations:", err));
}

function updateHeader() {
  if (danteDeviceText) danteDeviceText.textContent = systemState.dante_device || "Dante-Pi";
  if (clockStatusText) clockStatusText.textContent = systemState.clock_status || "PTP Locked (48kHz)";

  if (systemState.zones) {
    const activeCount = systemState.zones.filter(z => z.status === "playing").length;
    activeZonesCount.textContent = `${activeCount} Active`;
  }
}

function syncZoneDropdowns() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  const currentVal = targetZoneSelect.value;
  const zoneOptions = systemState.zones.map(z => 
    `<option value="${z.id}" ${z.id === activeZoneId ? "selected" : ""}>${escapeHtml(z.name || `Zone ${z.id}`)}</option>`
  ).join("");

  targetZoneSelect.innerHTML = zoneOptions;
  modalZoneSelect.innerHTML = zoneOptions;
}

function renderCategoryPills() {
  const categories = Array.from(new Set(stationsList.map(s => s.category).filter(Boolean)));

  if (currentCategory !== "all" && !categories.includes(currentCategory)) {
    currentCategory = "all";
  }

  let html = `<li class="nav-item"><button class="nav-link ${currentCategory === 'all' ? 'active' : ''}" data-category="all">All</button></li>`;

  categories.forEach(cat => {
    html += `<li class="nav-item"><button class="nav-link ${currentCategory === cat ? 'active' : ''}" data-category="${escapeHtml(cat)}">${escapeHtml(cat)}</button></li>`;
  });

  categoryNav.innerHTML = html;
}

function renderZones() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  syncZoneDropdowns();
  zonesList.innerHTML = "";

  systemState.zones.forEach(zone => {
    const isPlaying = (zone.status === "playing");
    const isBuffering = (zone.status === "buffering");
    const isSelected = (zone.id === activeZoneId);

    const peakLPct = Math.min(100, Math.round((zone.peak_left || 0) * 100));
    const peakRPct = Math.min(100, Math.round((zone.peak_right || 0) * 100));

    const item = document.createElement("div");
    item.className = `zone-item ${isSelected ? "active-zone" : ""} ${isPlaying ? "playing-zone" : ""}`;

    item.innerHTML = `
      <div class="d-flex justify-content-between align-items-center mb-1">
        <div>
          <span class="fw-semibold small">${escapeHtml(zone.name)}</span>
          <span class="text-muted d-block" style="font-size: 0.7rem;">${escapeHtml(zone.dante_left || '')} / ${escapeHtml(zone.dante_right || '')}</span>
        </div>
        <div>
          ${isPlaying ? '<span class="badge text-bg-success"><i class="fa-solid fa-play fa-xs me-1"></i>Playing</span>' :
            isBuffering ? '<span class="badge text-bg-warning"><i class="fa-solid fa-spinner fa-spin fa-xs me-1"></i>Buffering</span>' :
            '<span class="badge text-bg-secondary">Idle</span>'}
        </div>
      </div>

      <div class="small text-truncate mb-2 text-secondary" style="font-size: 0.75rem;">
        <i class="fa-solid fa-music me-1"></i> ${zone.station_name ? escapeHtml(zone.station_name) : 'Digital silence (ready for playback)'}
      </div>

      <!-- Peak Meter Bars -->
      <div class="d-flex flex-column gap-1 mb-2">
        <div class="d-flex align-items-center gap-1">
          <span style="font-size: 0.65rem; width: 8px;" class="text-muted">L</span>
          <div class="vu-meter-bar flex-grow-1">
            <div class="vu-meter-fill" style="width: ${isPlaying ? peakLPct : 0}%;"></div>
          </div>
        </div>
        <div class="d-flex align-items-center gap-1">
          <span style="font-size: 0.65rem; width: 8px;" class="text-muted">R</span>
          <div class="vu-meter-bar flex-grow-1">
            <div class="vu-meter-fill" style="width: ${isPlaying ? peakRPct : 0}%;"></div>
          </div>
        </div>
      </div>

      <!-- Volume & Stop Controls -->
      <div class="d-flex align-items-center gap-2">
        <button class="btn btn-sm btn-outline-secondary py-0 px-2 btn-mute" title="${zone.muted ? 'Unmute' : 'Mute'}">
          <i class="fa-solid ${zone.muted || zone.volume === 0 ? 'fa-volume-xmark text-danger' : 'fa-volume-high'}"></i>
        </button>

        <input type="range" class="form-range flex-grow-1 vol-slider" min="0" max="100" value="${zone.volume}" title="Volume: ${zone.volume}%"/>

        <span class="small text-muted text-end" style="width: 32px; font-size: 0.75rem;">${zone.volume}%</span>

        <button class="btn btn-sm btn-outline-danger py-0 px-2 btn-stop" title="Stop Zone" ${!isPlaying && !isBuffering ? "disabled" : ""}>
          <i class="fa-solid fa-stop"></i>
        </button>
      </div>
    `;

    // Event Handlers
    item.querySelector(".btn-mute").addEventListener("click", () => {
      fetch(`/api/zones/${zone.id}/mute`, { method: "POST" });
    });

    item.querySelector(".btn-stop").addEventListener("click", () => {
      fetch(`/api/zones/${zone.id}/stop`, { method: "POST" });
    });

    const volSlider = item.querySelector(".vol-slider");
    volSlider.addEventListener("input", (e) => {
      const vol = parseInt(e.target.value, 10);
      fetch(`/api/zones/${zone.id}/volume`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ volume: vol })
      });
    });

    item.addEventListener("click", (e) => {
      if (e.target.tagName === "BUTTON" || e.target.tagName === "INPUT" || e.target.closest("button")) return;
      activeZoneId = zone.id;
      targetZoneSelect.value = zone.id;
      renderZones();
    });

    zonesList.appendChild(item);
  });
}

function renderStations() {
  stationsGrid.innerHTML = "";

  const filtered = stationsList.filter(st => {
    const matchCategory = (currentCategory === "all") || (st.category === currentCategory);
    const matchSearch = !searchQuery || 
      st.name.toLowerCase().includes(searchQuery) || 
      (st.description && st.description.toLowerCase().includes(searchQuery));
    return matchCategory && matchSearch;
  });

  if (filtered.length === 0) {
    stationsGrid.innerHTML = `
      <div class="col-12 text-center text-muted py-5">
        <p class="mb-1"><i class="fa-solid fa-circle-exclamation me-1"></i> No stations found.</p>
        <small class="opacity-75">Define your stations in <code>config.yaml</code> or click "Custom URL" in the top bar.</small>
      </div>
    `;
    return;
  }

  filtered.forEach(st => {
    const col = document.createElement("div");
    col.className = "col-sm-6 col-md-4 col-xl-3";

    col.innerHTML = `
      <div class="station-item">
        <div>
          <div class="d-flex justify-content-between align-items-start gap-1 mb-1">
            <span class="fw-semibold text-truncate small">${escapeHtml(st.name)}</span>
            <span class="badge text-bg-dark border" style="font-size: 0.65rem;">${escapeHtml(st.category || 'Stream')}</span>
          </div>
          <p class="text-muted small mb-2" style="font-size: 0.75rem; min-height: 2.2em; line-height: 1.2;">
            ${escapeHtml(st.description || '')}
          </p>
        </div>

        <div class="d-flex justify-content-between align-items-center pt-2 border-top">
          <span class="text-muted" style="font-size: 0.7rem;"><i class="fa-solid fa-wave-square me-1"></i>${escapeHtml(st.bitrate || '192k')}</span>
          <button class="btn btn-sm btn-primary py-0 px-2 btn-play" style="font-size: 0.75rem;">
            <i class="fa-solid fa-play fa-xs me-1"></i> Play
          </button>
        </div>
      </div>
    `;

    col.querySelector(".btn-play").addEventListener("click", () => {
      fetch(`/api/zones/${activeZoneId}/preset`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ preset_id: st.id })
      });
    });

    stationsGrid.appendChild(col);
  });
}

function escapeHtml(str) {
  if (!str) return "";
  return String(str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}
