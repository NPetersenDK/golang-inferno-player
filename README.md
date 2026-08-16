# Dante Streamer (via Inferno)

A web interface and audio streamer for Raspberry Pi and Linux that streams web radio, HLS, AAC, and MP3 audio directly into a Dante Audio over IP (AoIP) network using Inferno.

## Overview

- Persistent Dante Device: Maintains a fixed device identity and transmit channels in Dante Controller. When streams start or stop, the system outputs digital silence so Dante flows and clock synchronization remain locked.
- Multi-Zone Audio: Supports multiple independent stereo output zones mapped to Dante channel pairs (e.g., Channels 1-2, 3-4, 5-6, 7-8).
- Declarative Configuration: All radio stations, stream URLs, categories, and output zones are configured in config.yaml.
- Dedicated Interface Binding: Binds Dante RTP traffic, mDNS discovery, and PTP clock synchronization to a specified network interface (such as a dedicated USB Ethernet adapter).
- Web Management UI: Dark-mode Bootstrap web interface with real-time zone status, volume controls, mute toggles, stereo peak meters, dynamic category filtering, and custom URL playback.

## Quick Start (Docker Compose)

The recommended way to run Dante Streamer on a Raspberry Pi is using Docker Compose.

### 1. Create a Directory and Copy the Compose File

On your Raspberry Pi, create a folder and create a `compose.yml` (the same file lives in this repo at `docker/compose.yml`):

```yaml
services:
  dante-streamer:
    image: ghcr.io/npetersendk/golang-inferno-player:latest
    container_name: dante-streamer
    restart: unless-stopped
    network_mode: host
    environment:
      - DANTE_INTERFACE=eth1
      - INFERNO_NAME=Dante-Pi
      - DANTE_TX_CHANNELS=8
      - HTTP_PORT=8085
    cap_add:
      - SYS_TIME
      - NET_ADMIN
      - NET_RAW
      - SYS_NICE
    ulimits:
      rtprio: 95
      memlock: -1
    volumes:
      - ./config.yaml:/opt/dante-player/config.yaml:ro
      - dante_state:/root/.local/state/inferno_aoip
      - /dev/shm:/dev/shm

volumes:
  dante_state:
    name: dante_pi_state
```

### 2. Create config.yaml

Copy config.example.yaml to config.yaml in the same directory:

```yaml
dante_name: "Dante-Pi"
http_port: 8085
sample_rate: 48000
pipe_dir: "/tmp/dante_player"

zones:
  - id: 1
    name: "Zone 1 (Living Room)"
    dante_left: "Zone 1 L (Ch 1)"
    dante_right: "Zone 1 R (Ch 2)"
  - id: 2
    name: "Zone 2 (Kitchen)"
    dante_left: "Zone 2 L (Ch 3)"
    dante_right: "Zone 2 R (Ch 4)"
  - id: 3
    name: "Zone 3 (Office)"
    dante_left: "Zone 3 L (Ch 5)"
    dante_right: "Zone 3 R (Ch 6)"
  - id: 4
    name: "Zone 4 (Workshop)"
    dante_left: "Zone 4 L (Ch 7)"
    dante_right: "Zone 4 R (Ch 8)"

stations:
  - id: "dr-p3"
    name: "DR P3"
    category: "Danish Public Radio"
    stream_url: "https://drstream.akamaized.net/live/p3/s/p3_hls.m3u8"
    description: "Danmarks Radio P3"
  - id: "dr-p1"
    name: "DR P1"
    category: "Danish Public Radio"
    stream_url: "https://drstream.akamaized.net/live/p1/s/p1_hls.m3u8"
    description: "Danmarks Radio P1"
  - id: "nova-fm"
    name: "Nova FM"
    category: "Pop & Hits"
    stream_url: "https://stream.bauermedia.dk/nova128"
    description: "Pop and contemporary hits"
```

### 3. Start the Container

```bash
sudo docker compose pull
sudo docker compose up -d
```

Open your browser at:
http://<raspberry-pi-ip>:8085

## Network and Interface Configuration

When running on a Raspberry Pi with multiple network interfaces (for example, built-in eth0 or end0 for management, and a dedicated USB Ethernet adapter eth1 connected to the Dante network):

Set DANTE_INTERFACE in your compose file to the network interface connected to the Dante network:
- DANTE_INTERFACE=eth1

The entrypoint script automatically:
1. Points the PTPv1 listener at that interface, so the media clock is measured from the Dante grandmaster reachable there.
2. Sets INFERNO_BIND_IP to that interface, routing all Dante audio multicast and mDNS traffic through it.
3. Leaves the web management UI reachable across all interfaces on port 8085.

## Dante Controller Setup

1. Open Dante Controller on any computer on the Dante network.
2. Locate the device named Dante-Pi (or the custom name specified in config.yaml).
3. The device exposes 8 transmit channels (Zone 1 L/R, Zone 2 L/R, Zone 3 L/R, Zone 4 L/R).
4. Route the channels to your Dante receivers (such as Dante AVIO adapters, digital mixers, amplifiers, or DSP processors).
5. Routing subscriptions persist across reboots via the dante_pi_state volume.

## REST API Reference

| Method | Endpoint | Description |
|---|---|---|
| GET | /api/status | Returns current Dante device status, PTP state, and zone playback details |
| GET | /api/presets | Returns the list of stations configured in config.yaml |
| POST | /api/presets | Adds a custom station preset |
| DELETE | /api/presets/:id | Removes a custom station preset |
| POST | /api/zones/:id/play | Starts playback on a zone with payload {"preset_id": "dr-p3"} or {"stream_url": "...", "station_name": "..."} |
| POST | /api/zones/:id/stop | Stops playback on a zone |
| POST | /api/zones/:id/volume | Sets zone volume with payload {"volume": 80} (0-100) |
| POST | /api/zones/:id/mute | Toggles mute on a zone |
| POST | /api/stop-all | Stops playback on all zones simultaneously |
| GET | /api/events | Server-Sent Events (SSE) stream for real-time state synchronization |

## Optional: External Audio Sources (Spotify, AirPlay, ...)

A zone can be dedicated to an external producer instead of the station browser. Give it a `source` block in `config.yaml`:

```yaml
  - id: 4
    name: "Spotify"
    dante_left: "Spotify L (Ch 7)"
    dante_right: "Spotify R (Ch 8)"
    source:
      type: pipe
      path: "/tmp/dante_player/spotify.pcm"
      label: "Spotify"
      prebuffer_ms: 300
```

The zone then attaches to that FIFO at startup and stays there permanently: it drops out of the station target list, Stop All leaves it alone, and its Dante channel pair becomes a fixed feed you can patch in your mixer.

The engine only knows how to read raw PCM from a FIFO — it has no idea what Spotify is. Anything that can write to a pipe works, and FFmpeg resamples whatever rate the producer uses to the 48 kHz Dante clock. Nothing in the base image depends on any of this: omit the `source` block and the feature does not exist.

### Spotify Connect

`docker/compose.spotify.yml` is a complete stack — use it **instead of** `docker/compose.yml`, not alongside it:

```bash
docker compose -f docker/compose.spotify.yml up -d
```

Nothing is built locally and nothing is installed on the host. The two compose files duplicate the `dante-streamer` service, so mirror any change you make to the base one.

librespot publishes no official image, so this project publishes one at `ghcr.io/npetersendk/dante-librespot`, built for amd64 and arm64 by the `build-librespot` workflow. It takes the finished `.deb` from **raspotify**'s apt repository and runs `/usr/bin/librespot` directly, bypassing the systemd unit the package ships — a package download rather than a Rust build.

That image has its own lifecycle and is rebuilt monthly to track raspotify releases and Debian updates. The streamer image does not depend on it; if you never enable a Spotify source you never pull it.

**Discovery needs Avahi.** raspotify compiles librespot with only the Avahi zeroconf backend, so `libmdns` and `dns-sd` are not in the binary and it must reach an Avahi daemon over the system D-Bus. Without one it logs `Avahi error: Setting up dns-sd failed` and exits. The compose file lends the container the host's socket:

```yaml
      - /var/run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket
```

Raspberry Pi OS already runs `avahi-daemon` — it is what serves your `.local` hostname — so this costs no installation, and it avoids two daemons competing for UDP 5353 on the host network. Confirm with `systemctl status avahi-daemon`, and `sudo apt install avahi-daemon` if it is genuinely absent.

### Optional: RTL-SDR tuner

An RTL-SDR can feed a source zone with broadcast FM, tuned from the web UI. It is off unless you ask for it: with `DANTE_TUNER_ENABLED` unset nothing starts, no API is served and the UI shows nothing.

```yaml
    environment:
      - DANTE_TUNER_ENABLED=1
    devices:
      - /dev/bus/usb/001:/dev/bus/usb/001
```

Grant only the bus the dongle sits on rather than the whole USB tree. `lsusb` shows which one, and `lsusb -s 001:` confirms nothing else shares it. There is no `/dev/serial/by-id` equivalent to be more precise than this: an RTL-SDR is a raw libusb device, so the kernel creates no persistent node for it, and the per-device path `/dev/bus/usb/001/012` renumbers on every replug. Docker resolves `devices:` at container start, so keep the dongle in the same port and restart the container after replugging it.

Find the dongle with `docker exec dante-streamer rtl_test -t`, which prints each device with its serial:

```
Found 1 device(s):
  0:  Realtek, RTL2838UHIDIR, SN: 00000001
```

The `device` setting takes either that index or the serial, matched exactly or by prefix or suffix. Prefer the serial — indices shift as USB devices come and go. Most dongles ship with the same factory serial, so give yours its own with `rtl_eeprom -d 0 -s dante-fm` if you run more than one. `rtl_fm` prints the same device list on every start, so it also shows up in the container log under `[Tuner]`.

Then give a zone a realtime pipe source and add a `tuner` block naming it — see `config.example.yaml`. `realtime: true` matters: an SDR keeps sampling whether you read it or not, so blocking it overruns its buffer. That flag makes the zone drop the oldest chunk instead, exactly as it does for a live network stream.

One dongle tunes one frequency at a time, so FM and DAB+ cannot run in parallel from a single stick. Retuning restarts the receiver, which costs the zone its prebuffer: it reports idle for a moment and comes back on its own. With squelch enabled for scanner use, the zone shows **Waiting** between transmissions and **Playing** when there is traffic.

### Tuning latency

A pipe producer is held back by backpressure rather than paced by a network, so it keeps its queue full — the queue cap, not the prebuffer, is what you hear. These override the config for every source zone, so they can be tuned from the compose file without editing a mounted `config.yaml`:

| Variable | Default | Effect |
|---|---|---|
| `DANTE_SOURCE_BUFFER_MS` | 2× prebuffer | Queue cap. The largest single contributor |
| `DANTE_SOURCE_PREBUFFER_MS` | 1000 | Fill level before a zone starts delivering |

The two source values are defaults for zones that state nothing; a zone with its own `prebuffer_ms` or `buffer_ms` keeps them. Sources differ: an interactive producer wants the shortest buffer it can hold, while a radio wants a deep one because latency does not matter and its clock is its own.
| `DANTE_ALSA_BUFFER_US` | 250000 | ALSA playback buffer, all zones |
| `DANTE_FIFO_BYTES` | 16384 | Kernel buffer behind a source FIFO |

Start with `DANTE_SOURCE_BUFFER_MS`. Lowering the others trades margin for responsiveness, and `[Audio Health]` in the log counts the resulting holes once a minute — no output means none. Spotify Connect will not go below a few hundred milliseconds regardless, since librespot decodes ahead and exposes no control over it.

### Pointing at your config

The compose files default to `../config.yaml`, which is relative to **the compose file**, so it resolves to the repository root. If you copy a compose file somewhere else, set `DANTE_CONFIG` to match:

```bash
DANTE_CONFIG=./config.yaml docker compose up -d
```

Get this wrong and there is no friendly error: Docker silently creates a *directory* at a bind source that does not exist, and the player logs `Could not read config file ...: is a directory`. Delete the stray directory before retrying.

Raspotify's own `Dockerfile` is not usable here — it is the build container they cross-compile the packages in, with no `ENTRYPOINT`.

### How the pipe is shared

A FIFO is an inode on a filesystem, not a network object, so mounting the same named volume at the same path in both containers is enough for them to be looking at the same pipe.

The player creates the FIFO with mode `0666`, because the producer runs as its own user, and holds it open read-write for its whole life so a producer that pauses or disconnects never reaches the reader as EOF. If the producer starts first and creates a plain file at the path, the player replaces it with a real FIFO and logs that it did.

The same arrangement works for any producer that can write raw PCM to a pipe — shairport-sync for AirPlay, for instance. Point it at the FIFO and set `format`, `sample_rate` and `channels` in the source block to match what it emits.

## PTP Clock Synchronization & Embedded `usrvclock` Driver

Dante Audio over IP requires high-precision PTPv1 clock synchronization (IEEE 1588-2002).

To ensure rock-solid stability and zero startup latency, this project includes a native implementation of the **[usrvclock](https://gitlab.com/lumifaza/usrvclock)** protocol directly inside the Go stream engine ([`engine/usrvclock.go`](file:///e:/Git/tmpshit/dante/golang-inferno-player/engine/usrvclock.go)):

- **Instant ALSA Startup:** Standard Inferno setups can hit 5-second ALSA startup timeouts waiting for external clock daemon sockets. The embedded Go `usrvclock` driver responds immediately (<0.1ms), preventing driver timeouts and audio stream resets.
- **No PTP Daemon:** A passive PTPv1 listener ([`engine/ptpv1.go`](file:///e:/Git/tmpshit/dante/golang-inferno-player/engine/ptpv1.go)) measures the grandmaster's phase and rate straight off the wire and feeds them into the overlay. It never transmits, never runs the BMCA and never touches the system clock, so no external daemon and no `SYS_TIME` capability are required.
- **Continuous Phase Locking:** The measured offset is slewed rather than stepped once locked, so the media clock stays continuous under a running Dante flow. The status bar reports the live sync error and oscillator drift.

## Upstream Projects and Dependencies

This project relies on the following open source software:

- Inferno (Dante Audio over IP implementation for Linux): https://github.com/teodly/inferno
- FFmpeg (Audio stream decoding and format conversion): https://ffmpeg.org
- usrvclock (Userspace Virtual Clock Protocol): https://gitlab.com/lumifaza/usrvclock

