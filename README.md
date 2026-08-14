# Dante Audio Player & Web Interface (via Inferno)

A full-featured Audio over IP (AoIP) streaming hub and web interface built for Raspberry Pi to stream internet radio (like **DR P3**), web streams, and music directly into a **Dante audio network** using **Inferno**.

---

## 🎯 Key Features

1. **Persistent Dante Device & Flows (No Re-routing Needed!)**:
   - The Dante transmitter runs continuously as a daemon, maintaining a fixed **Device ID**, Device Name (`Dante-Pi`), and fixed Channel Names (`Zone 1 L`, `Zone 1 R`, `Zone 2 L`, `Zone 2 R`, etc.).
   - When you start, stop, or switch radio streams (e.g. from DR P3 to DR P6 Beat), the Dante connection **never drops**; Inferno streams silence when idle, keeping Dante Controller flows 100% locked and happy.

2. **Multi-Stream & Multi-Zone Support**:
   - Supports 4 independent stereo zones (8 Dante channels):
     - **Zone 1 (Main / Stue)**: Dante Channels 1 & 2
     - **Zone 2 (Køkken)**: Dante Channels 3 & 4
     - **Zone 3 (Kontor)**: Dante Channels 5 & 6
     - **Zone 4 (Værksted / Altan)**: Dante Channels 7 & 8
   - Play different stations in different zones simultaneously or control them independently.

3. **Danish Radio & International Presets**:
   - Built-in presets for **DR P3**, **DR P1**, **DR P2 Klassisk**, **DR P4 (København, Fyn, Østjylland)**, **DR P5**, **DR P6 Beat**, **DR P8 Jazz**, **Radio4**, **Nova FM**, **The Voice**, **Pop FM**, **myROCK**, **BBC Radio 1 & 6**, **SomaFM**, **KEXP**, and custom stream URLs.

4. **Modern Responsive Web Interface**:
   - Single-click **"Play DR P3"** action button.
   - Dual-channel real-time **LED Stereo VU Meters** for each zone.
   - Volume sliders (0-100%) and instant mute per zone.
   - Custom stream URL input (supports HLS `.m3u8`, AAC, MP3, Icecast).
   - Real-time Server-Sent Events (SSE) state synchronization across all connected phones, tablets, and computers.

5. **Audio Pipeline**:
   - High-quality 48,000 Hz 24/32-bit PCM audio output matching native Dante standards.
   - Automatic reconnect and buffer protection.

---

## 🏗️ Architecture Overview

```
[ Internet Radio (DR P3 / HLS / MP3) ]
                 │
                 ▼
[ Go Web Engine (golang-infero-player) ]
  - REST & SSE Web Interface (Port 8080)
  - Multi-Zone Volume & Resampling (48kHz S32LE)
                 │
                 ▼ (Raw PCM into /tmp/dante_player/zone_*.pcm)
[ Inferno Dante Transmitter (inferno_player) ]
  - 8 Dante TX Channels with Fixed Identity
  - Continuous RTP stream & silence generation
                 │
                 ▼ (Dante RTP Audio over IP)
[ Dante Network: Amplifiers / Mixers / AVIO / DSP ]
```

---

## 🚀 Quick Start on Raspberry Pi

### Option 1: Automated 1-Click Setup (Recommended)

Copy this repository to your Raspberry Pi and run:

```bash
cd dante/golang-infero-player/deploy
chmod +x setup-rpi.sh
./setup-rpi.sh
```

This script will automatically:
1. Install system prerequisites (`ffmpeg`, `libasound2-dev`, `build-essential`).
2. Build `statime` (Dante PTP clock daemon).
3. Build `inferno_player` (Dante transmitter daemon).
4. Build `dante-player` (Go Web Interface & Stream Engine).
5. Configure and start systemd services (`statime.service`, `inferno-dante.service`, `dante-player.service`).

---

### Option 2: Manual Build & Run

#### 1. Compile Statime (Clock Daemon)
```bash
git clone --recurse-submodules -b inferno-dev https://github.com/teodly/statime /tmp/statime
cd /tmp/statime
cargo build --release
sudo cp target/release/statime /usr/local/bin/
```

#### 2. Compile Inferno Dante Player
```bash
cd dante/inferno
cargo build --release -p inferno_player
sudo cp target/release/inferno_player /usr/local/bin/
```

#### 3. Compile Dante Web Player (Go)
```bash
cd dante/golang-infero-player
go build -o dante-player .
sudo cp dante-player /usr/local/bin/
```

#### 4. Run the Services
Start the three components in order:

```bash
# 1. PTP Clock Sync (syncs with Dante Grandmaster)
sudo statime -c deploy/inferno-ptpv1.toml &

# 2. Inferno Dante Transmitter (advertises 'Dante-Pi' on network)
inferno_player --name Dante-Pi --channels 8 --pipe-dir /tmp/dante_player &

# 3. Dante Web Player
dante-player -port 8080 -pipe-dir /tmp/dante_player -dante-name Dante-Pi
```

Open your browser at:
👉 **`http://<raspberry-pi-ip>:8080`**

---

## 🎛️ Dante Controller Routing (Set & Forget)

1. Open **Dante Controller** on your Mac/PC connected to the same network.
2. You will see **`Dante-Pi`** listed with 8 Transmit (TX) channels:
   - `Zone 1 L` (Ch 1) & `Zone 1 R` (Ch 2)
   - `Zone 2 L` (Ch 3) & `Zone 2 R` (Ch 4)
   - `Zone 3 L` (Ch 5) & `Zone 3 R` (Ch 6)
   - `Zone 4 L` (Ch 7) & `Zone 4 R` (Ch 8)
3. Click to route the channels to your Dante receivers (e.g. Dante AVIO adapter, Behringer Wing/X32, Yamaha console, or Dante speakers).
4. **Done!** Because the Inferno daemon stays running and broadcasts silence when idle, the routing matrix in Dante Controller will **never** need to be touched again.

---

## 📻 Using the Web Interface

- **Instant DR P3**: Click the big red **"Play DR P3"** button in the hero bar to immediately stream DR P3 to your primary zone.
- **Switch Zones**: Select the target zone from the dropdown or click a zone card, then click **Play** on any radio station.
- **Custom Streams**: Click **"Custom Stream URL"** to paste any Icecast, AAC, HLS `.m3u8`, or MP3 stream URL.
- **Live VU Meters**: View real-time stereo audio signal levels on the animated LED meters.
- **Stop All**: Click **"Stop All Zones"** to immediately silence all audio zones.

---

## 🔌 REST API Reference

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/status` | Current Dante and multi-zone status with peak levels |
| `GET` | `/api/presets` | List of all available radio presets |
| `POST` | `/api/presets` | Add a new custom radio preset |
| `DELETE` | `/api/presets/:id` | Delete a custom preset |
| `POST` | `/api/zones/:id/play` | Play preset `{"preset_id": "dr-p3"}` or `{"url": "...", "title": "..."}` |
| `POST` | `/api/zones/:id/stop` | Stop playback on zone |
| `POST` | `/api/zones/:id/volume` | Set zone volume `{"volume": 85}` (0-100) |
| `POST` | `/api/zones/:id/mute` | Toggle zone mute |
| `POST` | `/api/stop-all` | Stop all zones |
| `GET` | `/api/events` | Server-Sent Events (SSE) stream for real-time UI updates |

---

## 🛠️ Troubleshooting

- **No clock lock in Dante Controller**: Ensure `statime` is running and bound to the correct network interface (e.g. `eth0` or `end0`). Check firewall (`sudo ufw allow 319/udp && sudo ufw allow 320/udp`).
- **No sound**: Verify in Dante Controller that the subscriptions for `Dante-Pi` show green checkmarks (✔). Make sure `ffmpeg` is installed (`sudo apt install ffmpeg`).
- **Network ports**: Dante uses standard UDP ports 4455, 8700, 4400, 8800, 5353 (mDNS).
