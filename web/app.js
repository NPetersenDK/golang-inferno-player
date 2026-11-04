let systemState = { zones: [] };
let stationsList = [];
let activeZoneId = 1;
let currentCategory = "all";
let searchQuery = "";
let soundSearchQuery = "";
let activePanel = "radio";
let sseSource = null;

// Built once, then updated in place: the server broadcasts 10x/second, and
// rebuilding swaps the element under the pointer, so buttons jump and drags snap back.
const zoneNodes = new Map();
const soundNodes = new Map();
const voiceNodes = new Map();
let zoneDropdownSignature = "";
let soundGridSignature = "";
let voiceListSignature = "";

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
    const link = e.target.closest(".chip");
    if (link) {
      document.querySelectorAll("#categoryNav .chip").forEach(el => el.classList.remove("on"));
      link.classList.add("on");
      currentCategory = link.dataset.category;
      renderStations();
    }
  });

  stopAllBtn.addEventListener("click", () => {
    fetch("/api/stop-all", { method: "POST" });
  });

  sourceTabs.addEventListener("click", (e) => {
    const btn = e.target.closest(".tab");
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

  const customUrlModal = document.getElementById("customUrlModal");
  document.getElementById("openCustomModalBtn").addEventListener("click", () => customUrlModal.showModal());
  document.getElementById("customCancelBtn").addEventListener("click", () => customUrlModal.close());

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

    document.getElementById("customUrlModal").close();
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
    clockStatusText.title = systemState.ptp_status || "";
  }

  if (systemState.zones) {
    const activeCount = systemState.zones.filter(z => z.status === "playing").length;
    activeZonesCount.textContent = `${activeCount} Active`;
  }
}

function syncZoneDropdowns() {
  if (!systemState.zones || systemState.zones.length === 0) return;

  // Source zones are fed by an external producer, so they cannot be play targets.
  const selectable = systemState.zones.filter(z => !z.is_source);
  if (selectable.length === 0) return;

  if (!selectable.some(z => z.id === activeZoneId)) {
    activeZoneId = selectable[0].id;
  }

  // Rewrite the selects only when the zone list changes; doing it every update
  // closes an open dropdown mid-use.
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

  let html = `<button class="chip ${currentCategory === 'all' ? 'on' : ''}" data-category="all">All</button>`;

  categories.forEach(cat => {
    html += `<button class="chip ${currentCategory === cat ? 'on' : ''}" data-category="${escapeHtml(cat)}">${escapeHtml(cat)}</button>`;
  });

  categoryNav.innerHTML = html;
}

function showPanel(name) {
  activePanel = name;
  sourceTabs.querySelectorAll(".tab").forEach(btn => {
    btn.classList.toggle("on", btn.dataset.panel === name);
  });
  ["radio", "fm", "soundboard"].forEach(panel => {
    const el = document.getElementById(`panel-${panel}`);
    if (el) el.hidden = panel !== name;
  });
}

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
  const tunedTo = tuner.mode === "dab"
    ? `${escapeHtml(tuner.channel)}${tuner.service_id ? ` · service ${escapeHtml(tuner.service_id)}` : " · no service selected"}`
    : `${mhz} MHz`;
  const status = tuner.error
    ? `<span class="error">${escapeHtml(tuner.error)}</span>`
    : tuner.tuned
      ? `<span class="success">${tuner.mode === "dab" ? "DAB+" : "FM"} — ${tunedTo}</span>`
      : `<span class="muted">Off</span>`;

  // One dongle serves both modes, so the presets are one list and picking any of
  // them takes the tuner away from whatever it was doing.
  const presets = (tuner.presets || []).map(p => `
    <button class="btn sm ${p.id === tuner.preset_id ? "primary" : ""} js-preset"
            data-preset="${escapeHtml(p.id)}">
      <span class="badge muted">${p.mode === "dab" ? "DAB+" : "FM"}</span> ${escapeHtml(p.name)}
    </button>`).join("");

  panel.innerHTML = `
    <div class="row">
      <strong><i class="fa-solid fa-tower-broadcast"></i> Radio</strong>
      <span class="spacer"></span>
      <span class="small">${status}</span>
    </div>

    <div class="toolbar">${presets || '<span class="muted small">No presets configured.</span>'}</div>

    <p class="note">FM and DAB+ share the one receiver, so only one plays at a time.</p>

    <div class="toolbar" style="max-width:420px">
      <input type="number" class="grow js-freq" step="0.1" min="87.5" max="108"
             placeholder="FM MHz" value="${mhz || ""}">
      <button class="btn sm nowrap js-tune">Tune FM</button>
    </div>

    <div class="toolbar" style="max-width:420px">
      <input type="text" class="grow js-chan" style="min-width:0" placeholder="Channel, e.g. 12A"
             value="${escapeHtml(tuner.channel || "")}">
      <input type="text" class="grow js-sid" style="min-width:0" placeholder="Service, e.g. 0x9001"
             value="${escapeHtml(tuner.service_id || "")}">
      <button class="btn sm nowrap js-dab">Tune DAB+</button>
    </div>

    <div class="toolbar">
      <button class="btn sm js-scan">List services</button>
      <button class="btn sm ghost danger js-off">Off</button>
    </div>
    <pre class="log small js-services" style="margin:0" hidden></pre>`;

  panel.querySelectorAll(".js-preset").forEach(btn => {
    btn.addEventListener("click", () => tune({ preset_id: btn.dataset.preset }));
  });
  panel.querySelector(".js-tune").addEventListener("click", () => {
    const mhzValue = parseFloat(panel.querySelector(".js-freq").value);
    if (!isNaN(mhzValue)) tune({ frequency_hz: Math.round(mhzValue * 1e6) });
  });
  panel.querySelector(".js-dab").addEventListener("click", () => {
    const channel = panel.querySelector(".js-chan").value.trim();
    const serviceID = panel.querySelector(".js-sid").value.trim();
    if (channel && serviceID) tune({ channel, service_id: serviceID });
  });
  panel.querySelector(".js-scan").addEventListener("click", async () => {
    const out = panel.querySelector(".js-services");
    out.hidden = false;

    // Listing needs a locked ensemble, so tune the channel first when the tuner
    // is elsewhere. No service is decoded by that, it only locks the mux.
    const channel = panel.querySelector(".js-chan").value.trim();
    if (tuner.mode !== "dab" || tuner.channel !== channel) {
      if (!channel) {
        out.textContent = "Enter a channel first, e.g. 12A.";
        return;
      }
      out.textContent = `Locking onto ${channel}, this takes a few seconds...`;
      const tuned = await fetch("/api/tuner/tune", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ channel })
      });
      if (!tuned.ok) {
        out.textContent = await tuned.text();
        return;
      }
    }

    out.textContent = "Reading ensemble...";
    const resp = await fetch("/api/tuner/services");
    out.textContent = resp.ok
      ? JSON.stringify(await resp.json(), null, 2)
      : await resp.text();
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

// Rebuilt only when the sound list changes; the per-pad counter updates in place.
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

  const sounding = new Set((sb.playing || []).map(v => v.sound_id));
  soundNodes.forEach((refs, id) => {
    refs.root.classList.toggle("sound-pad-active", sounding.has(id));
  });
}

function buildSoundsGrid(sounds, sb) {
  soundsGrid.innerHTML = "";
  soundNodes.clear();

  if (sb.error) {
    soundsGrid.innerHTML = `
      <div class="empty">
        <p style="margin:0"><i class="fa-solid fa-triangle-exclamation"></i> ${escapeHtml(sb.error)}</p>
      </div>`;
    return;
  }

  if (sounds.length === 0) {
    soundsGrid.innerHTML = `
      <div class="empty">
        <p style="margin:0"><i class="fa-solid fa-folder-open"></i> No sounds found.</p>
        <small>Drop audio files into <code>${escapeHtml(sb.path || "the sounds directory")}</code> — they appear here without a restart.</small>
      </div>`;
    return;
  }

  sounds.forEach(snd => {
    const root = document.createElement("button");
    root.className = "sound-pad";
    root.innerHTML = `<span>${escapeHtml(snd.name)}</span>`;
    root.title = snd.duration_ms ? `${snd.name} (${(snd.duration_ms / 1000).toFixed(1)}s)` : snd.name;
    root.addEventListener("click", () => playSound(snd.id));

    soundNodes.set(snd.id, { root });
    soundsGrid.appendChild(root);
  });
}

// One row per zone: a zone sounds at most one pad at a time.
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
        <button class="btn sm ghost danger js-stop" title="Stop this sound">
          <i class="fa-solid fa-stop"></i>
        </button>
        <span class="small truncate grow">${escapeHtml(v.name)}</span>
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

  // Updating in place never clears the list, so the static placeholder must go.
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
    <div class="row">
      <div class="grow">
        <div class="small"><strong class="js-name"></strong></div>
        <div class="muted small js-channels"></div>
      </div>
      <div class="js-badge"></div>
    </div>

    <div class="small muted truncate">
      <i class="fa-solid fa-music"></i> <span class="js-station"></span>
    </div>

    <div class="small truncate js-sounds" style="color:var(--ok)" hidden>
      <i class="fa-solid fa-grip"></i> <span class="js-sound-label"></span>
    </div>

    <div class="stack" style="gap:4px">
      <div class="row" style="gap:6px">
        <span class="muted small" style="width:8px">L</span>
        <div class="vu-bar"><div class="vu-fill js-vu-l" style="width: 0%;"></div></div>
      </div>
      <div class="row" style="gap:6px">
        <span class="muted small" style="width:8px">R</span>
        <div class="vu-bar"><div class="vu-fill js-vu-r" style="width: 0%;"></div></div>
      </div>
    </div>

    <div class="row" style="gap:8px">
      <button class="btn sm btn-mute">
        <i class="fa-solid js-mute-icon"></i>
      </button>
      <input type="range" class="grow vol-slider" min="0" max="100" value="${zone.volume}"/>
      <span class="small muted vol-text js-vol-text"></span>
      <button class="btn sm ghost danger btn-stop" title="Stop Zone">
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
    sounds: root.querySelector(".js-sounds"),
    soundLabel: root.querySelector(".js-sound-label"),
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

  // Track the drag so an incoming state event cannot yank the slider mid-gesture.
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
  const pad = zone.sound_label || "";
  // A pad over an idle zone still puts audio on the wire, so it counts as live.
  const audible = isPlaying || pad !== "";

  refs.root.classList.toggle("active-zone", !zone.is_source && zone.id === activeZoneId);
  refs.root.classList.toggle("playing-zone", audible);

  setText(refs.name, zone.name || "");
  setText(refs.channels, `${zone.dante_left || ""} / ${zone.dante_right || ""}`);
  setText(refs.station, zone.station_name || "No stream");

  refs.sounds.hidden = pad === "";
  setText(refs.soundLabel, pad);

  const statusKey = `${zone.status}/${zone.is_source}/${pad !== ""}`;
  if (statusKey !== refs.lastStatus) {
    refs.lastStatus = statusKey;
    refs.badge.innerHTML = isPlaying
      ? '<span class="badge ok"><i class="fa-solid fa-play"></i> Playing</span>'
      : isBuffering
        ? '<span class="badge warn"><i class="fa-solid fa-spinner fa-spin"></i> Buffering</span>'
        : pad !== ""
          ? '<span class="badge ok"><i class="fa-solid fa-grip"></i> Sounds</span>'
          : zone.is_source
            ? '<span class="badge muted"><i class="fa-solid fa-plug"></i> Waiting</span>'
            : '<span class="badge muted">Idle</span>';
  }

  const peakLPct = audible ? Math.min(100, Math.round((zone.peak_left || 0) * 100)) : 0;
  const peakRPct = audible ? Math.min(100, Math.round((zone.peak_right || 0) * 100)) : 0;
  refs.vuL.style.width = `${peakLPct}%`;
  refs.vuR.style.width = `${peakRPct}%`;

  const muted = zone.muted || zone.volume === 0;
  refs.muteBtn.title = zone.muted ? "Unmute" : "Mute";
  refs.muteIcon.className = `fa-solid js-mute-icon ${muted ? "fa-volume-xmark error" : "fa-volume-high"}`;

  if (!refs.dragging && document.activeElement !== refs.slider) {
    if (refs.slider.value !== String(zone.volume)) refs.slider.value = zone.volume;
    setText(refs.volText, `${zone.volume}%`);
  }
  refs.slider.title = `Volume: ${zone.volume}%`;

  // The producer owns playback on a source zone, so stop is meaningless there.
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
      <div class="empty">
        <p style="margin:0"><i class="fa-solid fa-circle-exclamation"></i> No stations found.</p>
        <small>Define your stations in <code>config.yaml</code> or click "Custom URL" in the top bar.</small>
      </div>
    `;
    return;
  }

  filtered.forEach(st => {
    const col = document.createElement("div");
    col.className = "station-item";

    col.innerHTML = `
      <div>
        <div class="row" style="align-items:flex-start;gap:6px">
          <strong class="small truncate grow">${escapeHtml(st.name)}</strong>
          <span class="badge muted">${escapeHtml(st.category || 'Stream')}</span>
        </div>
        <p class="muted small station-desc" style="margin:4px 0 0">${escapeHtml(st.description || '')}</p>
      </div>

      <div class="row" style="border-top:1px solid var(--border);padding-top:8px">
        <span class="muted small grow"><i class="fa-solid fa-wave-square"></i> ${escapeHtml(st.bitrate || '192k')}</span>
        <button class="btn sm primary btn-play"><i class="fa-solid fa-play"></i> Play</button>
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
