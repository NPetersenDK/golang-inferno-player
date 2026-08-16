// ==============================================================================
// Dante Streamer - Frontend Controller (Clean & Maintainable)
// ==============================================================================

let systemState = { zones: [] };
let stationsList = [];
let activeZoneId = 1;
let currentCategory = "all";
let searchQuery = "";
let soundSearchQuery = "";
let activePanel = "radio";
let sseSource = null;

// Everything here is built once and then updated in place. The server
// broadcasts ten times a second, so rebuilding replaces the element under the
// pointer: buttons jump, the cursor flickers and a drag snaps back mid-gesture.
const zoneNodes = new Map();
const soundNodes = new Map();
const voiceNodes = new Map();
let zoneDropdownSignature = "";
let soundGridSignature = "";
let voiceListSignature = "";

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
const sourceTabs = document.getElementById("sourceTabs");
const soundsGrid = document.getElementById("soundsGrid");
const playingSounds = document.getElementById("playingSounds");
const soundSearchInput = document.getElementById("soundSearchInput");
const stopSoundsBtn = document.getElementById("stopSoundsBtn");

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

  sourceTabs.addEventListener("click", (e) => {
    const btn = e.target.closest(".nav-link");
    if (btn) showPanel(btn.dataset.panel);
  });

  soundSearchInput.addEventListener("input", (e) => {
    soundSearchQuery = e.target.value.toLowerCase().trim();
    renderSoundsGrid(true);
  });

  stopSoundsBtn.addEventListener("click", () => {
    fetch("/api/soundboard/stop-all", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({})
    });
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
      renderTuner();
      renderSoundboard();
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
  if (clockStatusText) {
    clockStatusText.textContent = systemState.clock_status || "Clock status unknown";
    // The detail line is long, so it lives in the tooltip rather than the bar.
    clockStatusText.title = systemState.ptp_status || "";
  }

  if (systemState.zones) {
    const activeCount = systemState.zones.filter(z => z.status === "playing").length;
    activeZonesCount.textContent = `${activeCount} Active`;
  }
}

function syncZoneDropdowns() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  // Source zones are permanently fed by an external producer, so they are not
  // valid targets for a station.
  const selectable = systemState.zones.filter(z => !z.is_source);
  if (selectable.length === 0) return;

  if (!selectable.some(z => z.id === activeZoneId)) {
    activeZoneId = selectable[0].id;
  }

  // Only touch the selects when the zone list itself changes. Rewriting their
  // options on every update closes an open dropdown while you are using it.
  const signature = selectable.map(z => `${z.id}:${z.name || ""}`).join("|");
  if (signature !== zoneDropdownSignature) {
    zoneDropdownSignature = signature;
    const zoneOptions = selectable.map(z =>
      `<option value="${z.id}">${escapeHtml(z.name || `Zone ${z.id}`)}</option>`
    ).join("");
    targetZoneSelect.innerHTML = zoneOptions;
    modalZoneSelect.innerHTML = zoneOptions;
  }

  if (targetZoneSelect.value !== String(activeZoneId)) {
    targetZoneSelect.value = String(activeZoneId);
  }
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

// Both the tuner and the soundboard are opt-in: with one disabled the server
// reports enabled:false, its tab never appears and its panel is never drawn.
function showPanel(name) {
  activePanel = name;
  sourceTabs.querySelectorAll(".nav-link").forEach(btn => {
    btn.classList.toggle("active", btn.dataset.panel === name);
  });
  ["radio", "fm", "soundboard"].forEach(panel => {
    const el = document.getElementById(`panel-${panel}`);
    if (el) el.hidden = panel !== name;
  });
}

// setTabEnabled hides a tab whose feature is off, and falls back to the radio
// panel if that tab was the one being looked at.
function setTabEnabled(tabId, enabled, panel) {
  const tab = document.getElementById(tabId);
  if (!tab || tab.hidden === !enabled) return;
  tab.hidden = !enabled;
  if (!enabled && activePanel === panel) showPanel("radio");
}

function renderTuner() {
  const panel = document.getElementById("panel-fm");
  if (!panel) return;

  const tuner = systemState.tuner;
  setTabEnabled("tabFm", !!(tuner && tuner.enabled), "fm");
  if (!tuner || !tuner.enabled) return;

  const signature = JSON.stringify(tuner);
  if (panel.dataset.signature === signature) return;
  panel.dataset.signature = signature;

  const mhz = tuner.frequency_hz ? (tuner.frequency_hz / 1e6).toFixed(1) : null;
  const status = tuner.error
    ? `<span class="text-danger">${escapeHtml(tuner.error)}</span>`
    : tuner.tuned
      ? `<span class="text-success">Tuned to ${mhz} MHz</span>`
      : `<span class="text-muted">Off</span>`;

  const presets = (tuner.presets || []).map(p => `
    <button class="btn btn-sm ${p.id === tuner.preset_id ? "btn-primary" : "btn-outline-secondary"} js-preset"
            data-preset="${escapeHtml(p.id)}">
      ${escapeHtml(p.name)}
    </button>`).join("");

  panel.innerHTML = `
    <div class="d-flex justify-content-between align-items-center mb-3">
      <span class="fw-semibold"><i class="fa-solid fa-tower-broadcast me-1"></i> FM Tuner</span>
      <span class="small">${status}</span>
    </div>

    <div class="d-flex flex-wrap gap-2 mb-3">${presets || '<span class="text-muted small">No presets configured.</span>'}</div>

    <div class="d-flex gap-2" style="max-width: 360px;">
      <input type="number" class="form-control form-control-sm js-freq" step="0.1" min="87.5" max="108"
             placeholder="MHz" value="${mhz || ""}">
      <button class="btn btn-sm btn-outline-primary text-nowrap js-tune">Tune</button>
      <button class="btn btn-sm btn-outline-danger js-off">Off</button>
    </div>`;

  panel.querySelectorAll(".js-preset").forEach(btn => {
    btn.addEventListener("click", () => tune({ preset_id: btn.dataset.preset }));
  });
  panel.querySelector(".js-tune").addEventListener("click", () => {
    const mhzValue = parseFloat(panel.querySelector(".js-freq").value);
    if (!isNaN(mhzValue)) tune({ frequency_hz: Math.round(mhzValue * 1e6) });
  });
  panel.querySelector(".js-off").addEventListener("click", () => {
    fetch("/api/tuner/off", { method: "POST" });
  });
}

function tune(body) {
  fetch("/api/tuner/tune", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body)
  });
}

function renderSoundboard() {
  const sb = systemState.soundboard;
  setTabEnabled("tabSoundboard", !!(sb && sb.enabled), "soundboard");
  if (!sb || !sb.enabled) return;

  renderSoundsGrid(false);
  renderPlaying(sb.playing || []);
}

// The pad grid only changes when the directory does, so it is rebuilt on a
// signature. The per-pad counter changes constantly and is written in place.
function renderSoundsGrid(force) {
  const sb = systemState.soundboard || {};
  const sounds = (sb.sounds || []).filter(s =>
    !soundSearchQuery || s.name.toLowerCase().includes(soundSearchQuery)
  );

  const signature = `${sounds.map(s => s.id).join("|")}#${sb.error || ""}#${sb.path || ""}`;
  if (force || signature !== soundGridSignature) {
    soundGridSignature = signature;
    buildSoundsGrid(sounds, sb);
  }

  const counts = new Map();
  (sb.playing || []).forEach(v => counts.set(v.sound_id, (counts.get(v.sound_id) || 0) + 1));

  soundNodes.forEach((refs, id) => {
    // One copy is already shown by the pad lighting up; the badge only earns
    // its place once the same sound is layered over itself.
    const n = counts.get(id) || 0;
    refs.root.classList.toggle("sound-pad-active", n > 0);
    refs.count.hidden = n < 2;
    setText(refs.count, n > 1 ? `${n}x` : "");
  });
}

function buildSoundsGrid(sounds, sb) {
  soundsGrid.innerHTML = "";
  soundNodes.clear();

  if (sb.error) {
    soundsGrid.innerHTML = `
      <div class="col-12 text-center text-muted py-5">
        <p class="mb-1"><i class="fa-solid fa-triangle-exclamation me-1"></i> ${escapeHtml(sb.error)}</p>
      </div>`;
    return;
  }

  if (sounds.length === 0) {
    soundsGrid.innerHTML = `
      <div class="col-12 text-center text-muted py-5">
        <p class="mb-1"><i class="fa-solid fa-folder-open me-1"></i> No sounds found.</p>
        <small class="opacity-75">Drop audio files into <code>${escapeHtml(sb.path || "the sounds directory")}</code> — they appear here without a restart.</small>
      </div>`;
    return;
  }

  sounds.forEach(snd => {
    const col = document.createElement("div");
    col.className = "col-6 col-md-4 col-xl-3";
    col.innerHTML = `
      <button class="sound-pad w-100">
        <span class="sound-pad-name text-truncate">${escapeHtml(snd.name)}</span>
        <span class="badge text-bg-success sound-pad-count js-count" hidden></span>
      </button>`;

    const root = col.querySelector(".sound-pad");
    root.title = snd.duration_ms ? `${snd.name} (${(snd.duration_ms / 1000).toFixed(1)}s)` : snd.name;
    root.addEventListener("click", () => playSound(snd.id));

    soundNodes.set(snd.id, { root, count: col.querySelector(".js-count") });
    soundsGrid.appendChild(col);
  });
}

// Each press layers another voice, so the same sound can appear here several
// times over and each copy is stopped on its own.
function renderPlaying(playing) {
  const signature = playing.map(v => `${v.voice_id}:${v.name}`).join("|");
  if (signature !== voiceListSignature) {
    voiceListSignature = signature;
    playingSounds.innerHTML = "";
    voiceNodes.clear();

    playing.forEach(v => {
      const row = document.createElement("div");
      row.className = "playing-sound";
      row.innerHTML = `
        <button class="btn btn-sm btn-outline-danger py-0 px-2 js-stop" title="Stop this sound">
          <i class="fa-solid fa-stop fa-xs"></i>
        </button>
        <span class="small text-truncate flex-grow-1">${escapeHtml(v.name)}</span>
        <div class="playing-sound-bar"><div class="playing-sound-fill js-bar"></div></div>`;

      row.querySelector(".js-stop").addEventListener("click", () => {
        fetch("/api/soundboard/stop", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ voice_id: v.voice_id })
        });
      });

      voiceNodes.set(v.voice_id, { root: row, bar: row.querySelector(".js-bar") });
      playingSounds.appendChild(row);
    });
  }

  playing.forEach(v => {
    const refs = voiceNodes.get(v.voice_id);
    if (!refs) return;
    const pct = v.length_ms ? Math.min(100, (v.position_ms / v.length_ms) * 100) : 0;
    refs.bar.style.width = `${pct}%`;
  });

  stopSoundsBtn.disabled = playing.length === 0;
}

function playSound(soundId) {
  fetch("/api/soundboard/play", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sound_id: soundId, zone_id: activeZoneId })
  }).then(res => {
    if (!res.ok) return res.text().then(showSoundboardMessage);
  }).catch(err => showSoundboardMessage(String(err)));
}

let soundboardMessageTimer = null;
function showSoundboardMessage(text) {
  const box = document.getElementById("soundboardMsg");
  if (!box) return;
  box.textContent = text.trim();
  box.hidden = false;
  clearTimeout(soundboardMessageTimer);
  soundboardMessageTimer = setTimeout(() => { box.hidden = true; }, 4000);
}

function renderZones() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  syncZoneDropdowns();

  // The static "Connecting to Dante engine" placeholder lives inside the list.
  // The old full-rebuild wiped it implicitly; updating in place has to drop it
  // explicitly the first time real zones arrive.
  const placeholder = document.getElementById("zonesPlaceholder");
  if (placeholder) placeholder.remove();

  systemState.zones.forEach((zone, index) => {
    let refs = zoneNodes.get(zone.id);
    if (!refs) {
      refs = createZoneNode(zone);
      zoneNodes.set(zone.id, refs);
    }
    if (zonesList.children[index] !== refs.root) {
      zonesList.insertBefore(refs.root, zonesList.children[index] || null);
    }
    updateZoneNode(refs, zone);
  });

  const liveIds = new Set(systemState.zones.map(z => z.id));
  zoneNodes.forEach((refs, id) => {
    if (!liveIds.has(id)) {
      refs.root.remove();
      zoneNodes.delete(id);
    }
  });
}

function createZoneNode(zone) {
  const root = document.createElement("div");
  root.className = "zone-item";
  root.innerHTML = `
    <div class="d-flex justify-content-between align-items-center mb-1">
      <div>
        <span class="fw-semibold small js-name"></span>
        <span class="text-muted d-block js-channels" style="font-size: 0.7rem;"></span>
      </div>
      <div class="js-badge"></div>
    </div>

    <div class="small text-truncate mb-2 text-secondary" style="font-size: 0.75rem;">
      <i class="fa-solid fa-music me-1"></i> <span class="js-station"></span>
    </div>

    <!-- Peak Meter Bars -->
    <div class="d-flex flex-column gap-1 mb-2">
      <div class="d-flex align-items-center gap-1">
        <span style="font-size: 0.65rem; width: 8px;" class="text-muted">L</span>
        <div class="vu-meter-bar flex-grow-1"><div class="vu-meter-fill js-vu-l" style="width: 0%;"></div></div>
      </div>
      <div class="d-flex align-items-center gap-1">
        <span style="font-size: 0.65rem; width: 8px;" class="text-muted">R</span>
        <div class="vu-meter-bar flex-grow-1"><div class="vu-meter-fill js-vu-r" style="width: 0%;"></div></div>
      </div>
    </div>

    <!-- Volume & Stop Controls -->
    <div class="d-flex align-items-center gap-2">
      <button class="btn btn-sm btn-outline-secondary py-0 px-2 btn-mute">
        <i class="fa-solid js-mute-icon"></i>
      </button>
      <input type="range" class="form-range flex-grow-1 vol-slider" min="0" max="100" value="${zone.volume}"/>
      <span class="small text-muted text-end js-vol-text" style="width: 32px; font-size: 0.75rem;"></span>
      <button class="btn btn-sm btn-outline-danger py-0 px-2 btn-stop" title="Stop Zone">
        <i class="fa-solid fa-stop"></i>
      </button>
    </div>
  `;

  const refs = {
    root,
    name: root.querySelector(".js-name"),
    channels: root.querySelector(".js-channels"),
    badge: root.querySelector(".js-badge"),
    station: root.querySelector(".js-station"),
    vuL: root.querySelector(".js-vu-l"),
    vuR: root.querySelector(".js-vu-r"),
    muteBtn: root.querySelector(".btn-mute"),
    muteIcon: root.querySelector(".js-mute-icon"),
    slider: root.querySelector(".vol-slider"),
    volText: root.querySelector(".js-vol-text"),
    stopBtn: root.querySelector(".btn-stop"),
    lastStatus: null,
    dragging: false
  };

  refs.muteBtn.addEventListener("click", () => {
    fetch(`/api/zones/${zone.id}/mute`, { method: "POST" });
  });

  refs.stopBtn.addEventListener("click", () => {
    fetch(`/api/zones/${zone.id}/stop`, { method: "POST" });
  });

  // Track the drag so an incoming state event cannot yank the slider away
  // mid-gesture.
  refs.slider.addEventListener("pointerdown", () => { refs.dragging = true; });
  refs.slider.addEventListener("pointerup", () => { refs.dragging = false; });
  refs.slider.addEventListener("blur", () => { refs.dragging = false; });
  refs.slider.addEventListener("input", (e) => {
    const vol = parseInt(e.target.value, 10);
    refs.volText.textContent = `${vol}%`;
    fetch(`/api/zones/${zone.id}/volume`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ volume: vol })
    });
  });

  root.addEventListener("click", (e) => {
    if (e.target.tagName === "BUTTON" || e.target.tagName === "INPUT" || e.target.closest("button")) return;
    if (zone.is_source) return; // cannot be a station target
    activeZoneId = zone.id;
    targetZoneSelect.value = zone.id;
    renderZones();
  });

  return refs;
}

function updateZoneNode(refs, zone) {
  const isPlaying = (zone.status === "playing");
  const isBuffering = (zone.status === "buffering");

  refs.root.classList.toggle("active-zone", !zone.is_source && zone.id === activeZoneId);
  refs.root.classList.toggle("playing-zone", isPlaying);

  setText(refs.name, zone.name || "");
  setText(refs.channels, `${zone.dante_left || ""} / ${zone.dante_right || ""}`);
  setText(refs.station, zone.station_name || "No stream");

  const statusKey = `${zone.status}/${zone.is_source}`;
  if (statusKey !== refs.lastStatus) {
    refs.lastStatus = statusKey;
    // A source zone is never idle in the station sense: it is wired to a
    // producer and simply waiting for it to send something.
    refs.badge.innerHTML = isPlaying
      ? '<span class="badge text-bg-success"><i class="fa-solid fa-play fa-xs me-1"></i>Playing</span>'
      : isBuffering
        ? '<span class="badge text-bg-warning"><i class="fa-solid fa-spinner fa-spin fa-xs me-1"></i>Buffering</span>'
        : zone.is_source
          ? '<span class="badge text-bg-secondary"><i class="fa-solid fa-plug fa-xs me-1"></i>Waiting</span>'
          : '<span class="badge text-bg-secondary">Idle</span>';
  }

  const peakLPct = isPlaying ? Math.min(100, Math.round((zone.peak_left || 0) * 100)) : 0;
  const peakRPct = isPlaying ? Math.min(100, Math.round((zone.peak_right || 0) * 100)) : 0;
  refs.vuL.style.width = `${peakLPct}%`;
  refs.vuR.style.width = `${peakRPct}%`;

  const muted = zone.muted || zone.volume === 0;
  refs.muteBtn.title = zone.muted ? "Unmute" : "Mute";
  refs.muteIcon.className = `fa-solid js-mute-icon ${muted ? "fa-volume-xmark text-danger" : "fa-volume-high"}`;

  if (!refs.dragging && document.activeElement !== refs.slider) {
    if (refs.slider.value !== String(zone.volume)) refs.slider.value = zone.volume;
    setText(refs.volText, `${zone.volume}%`);
  }
  refs.slider.title = `Volume: ${zone.volume}%`;

  // Stop is meaningless on a source zone: the producer owns playback.
  refs.stopBtn.hidden = !!zone.is_source;
  const canStop = !zone.is_source && (isPlaying || isBuffering);
  if (refs.stopBtn.disabled === canStop) refs.stopBtn.disabled = !canStop;
}

function setText(el, value) {
  if (el.textContent !== value) el.textContent = value;
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
      fetch(`/api/zones/${activeZoneId}/play`, {
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
