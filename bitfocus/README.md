# Dante Streamer — Bitfocus Companion Module

Bitfocus Companion module for controlling Dante Streamer (`golang-inferno-player`) soundboards and audio zones.

## Features

- **Dynamic Soundboard Sync:** Polls `/api/soundboard` and updates actions, variables, and presets automatically when audio files are added or removed on the server.
- **Button Presets:** Ready-to-use buttons for each sound with active playback feedback (lights up while sounding).
- **Zone Controls:** Mute, volume, and stop-all controls.

## Installation in Companion

1. Download the `companion-module-dante-streamer.tar.gz` artifact from the GitHub Actions build.
2. In Companion:
   - Go to **Connections** -> **Import / Custom module**.
   - Upload the `.tar.gz` archive.
   - Add the **Dante Streamer Soundboard** connection.
3. Configure **Host / IP** (e.g. `10.0.15.75`) and **HTTP Port** (`8085`).

## Configuration

| Option | Default | Description |
|---|---|---|
| Host / IP | `127.0.0.1` | Dante Streamer IP address |
| HTTP Port | `8085` | Web API port |
| Default Zone | `1` | Default zone for soundboard pads |
| Polling Interval | `2` | Update interval in seconds |

## Actions & Feedbacks

- `Play Soundboard Sound`: Plays a chosen sound on a target zone.
- `Stop All Sounds`: Silences all active soundboard voices.
- `Toggle Zone Mute` / `Set Zone Volume`: Controls zone levels.
- `Sound is Actively Playing`: Button feedback that turns active while the sound plays.
