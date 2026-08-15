# syntax=docker/dockerfile:1
# ==============================================================================
# Spotify Connect producer for a pipe source zone.
#
# Published by the build-librespot workflow as
# ghcr.io/npetersendk/dante-librespot - users pull it, they do not build it.
#
# Needs no build context of its own; everything comes from the apt repository.
#
# librespot publishes no official image, and raspotify's own Dockerfile is a
# build container for cross-compiling its .deb packages: it has no ENTRYPOINT
# and does not run the player. So this takes the finished package from
# raspotify's apt repository and runs the binary directly. No Rust build.
#
# The package is prebuilt for amd64, arm64, armhf and riscv64. ARMv6 (Pi 1,
# Pi Zero v1) is not supported upstream.
# ==============================================================================

FROM debian:bookworm-slim

# libasound2 and libpulse0 are needed even though we only use the pipe backend:
# raspotify builds librespot with the ALSA and PulseAudio backends compiled in,
# so the dynamic linker wants both shared objects present.
#
# The package is extracted rather than installed, because it ships a systemd
# unit whose maintainer scripts have nothing to talk to inside a container. All
# we want is /usr/bin/librespot.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      libasound2 \
      libpulse0 \
    && curl -fsSL https://dtcooper.github.io/raspotify/key.asc \
         -o /usr/share/keyrings/raspotify_key.asc \
    && chmod 0644 /usr/share/keyrings/raspotify_key.asc \
    && echo 'deb [signed-by=/usr/share/keyrings/raspotify_key.asc] https://dtcooper.github.io/raspotify raspotify main' \
         > /etc/apt/sources.list.d/raspotify.list \
    && apt-get update \
    && apt-get download raspotify \
    && dpkg-deb -x raspotify_*.deb / \
    && rm -f raspotify_*.deb \
    && apt-get purge -y curl \
    && rm -rf /var/lib/apt/lists/*

ENTRYPOINT ["/usr/bin/librespot"]
