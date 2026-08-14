# Dante Streamer (via Inferno)

A web interface and audio streamer for Raspberry Pi and Linux that streams web radio, HLS, AAC, and MP3 audio directly into a Dante Audio over IP (AoIP) network using Inferno and Statime.

## Overview

- Persistent Dante Device: Maintains a fixed device identity and transmit channels in Dante Controller. When streams start or stop, the system outputs digital silence so Dante flows and clock synchronization remain locked.
- Multi-Zone Audio: Supports multiple independent stereo output zones mapped to Dante channel pairs (e.g., Channels 1-2, 3-4, 5-6, 7-8).
- Declarative Configuration: All radio stations, stream URLs, categories, and output zones are configured in config.yaml.
- Dedicated Interface Binding: Binds Dante RTP traffic, mDNS discovery, and PTP clock synchronization to a specified network interface (such as a dedicated USB Ethernet adapter).
- Web Management UI: Dark-mode Bootstrap web interface with real-time zone status, volume controls, mute toggles, stereo peak meters, dynamic category filtering, and custom URL playback.

## Quick Start (Docker Compose)

The recommended way to run Dante Streamer on a Raspberry Pi is using Docker Compose.

### 1. Create a Directory and Copy the Compose File

On your Raspberry Pi, create a folder and create docker-compose.yml:

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
      - INFERNO_TX_CHANNELS=8
      - INFERNO_SAMPLE_RATE=48000
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

Set DANTE_INTERFACE in docker-compose.yml to the network interface connected to the Dante network:
- DANTE_INTERFACE=eth1

The entrypoint script automatically:
1. Configures the Statime PTP daemon to synchronize clocks on that interface.
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

## PTP Clock Synchronization & Embedded `usrvclock` Driver

Dante Audio over IP requires high-precision PTPv1 clock synchronization (IEEE 1588-2002).

To ensure rock-solid stability and zero startup latency, this project includes a native implementation of the **[usrvclock](https://gitlab.com/lumifaza/usrvclock)** protocol directly inside the Go stream engine ([`engine/usrvclock.go`](file:///e:/Git/tmpshit/dante/golang-inferno-player/engine/usrvclock.go)):

- **Instant ALSA Startup:** Standard Inferno setups can hit 5-second ALSA startup timeouts waiting for external clock daemon sockets. The embedded Go `usrvclock` driver responds immediately (<0.1ms), preventing driver timeouts and audio stream resets.
- **Continuous Phase Locking:** While Statime synchronizes the Linux system clock with the network's Dante Grandmaster, the embedded driver serves smooth, continuous clock overlays to the Inferno ALSA module, ensuring uninterrupted, dropout-free audio streaming.

## Upstream Projects and Dependencies

This project relies on the following open source software:

- Inferno (Dante Audio over IP implementation for Linux): https://github.com/teodly/inferno
- Statime (PTPv1 fork for Dante clock synchronization): https://github.com/teodly/statime
- Statime (Upstream Project Pendulum): https://github.com/pendulum-project/statime
- FFmpeg (Audio stream decoding and format conversion): https://ffmpeg.org
- usrvclock (Userspace Virtual Clock Protocol): https://gitlab.com/lumifaza/usrvclock

