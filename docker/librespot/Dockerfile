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
# Built for amd64 and arm64 only.
# ==============================================================================

FROM debian:bookworm-slim

# -x echoes each command, so a failure in the build log names the command that
# caused it rather than leaving a collapsed && chain to guess at.
SHELL ["/bin/sh", "-eux", "-c"]

# apt-get download drops privileges to _apt, which cannot write to /.
WORKDIR /tmp

# raspotify builds librespot with the ALSA and PulseAudio backends compiled in,
# so both shared objects must be present even though we only use the pipe one.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      libasound2 \
      libpulse0

# raspotify's apt repository, set up exactly as their install.sh does.
RUN curl -fsSL https://dtcooper.github.io/raspotify/key.asc \
      -o /usr/share/keyrings/raspotify_key.asc && \
    chmod 0644 /usr/share/keyrings/raspotify_key.asc && \
    echo 'deb [signed-by=/usr/share/keyrings/raspotify_key.asc] https://dtcooper.github.io/raspotify raspotify main' \
      > /etc/apt/sources.list.d/raspotify.list

# Extract rather than install: the package ships a systemd unit whose maintainer
# scripts have nothing to talk to inside a container, and all we want is
# /usr/bin/librespot.
#
# Extract into a staging directory, never onto /. Debian is usrmerged, so /lib
# is a symlink to usr/lib - unpacking a package that carries a real ./lib
# directory replaces that symlink with an empty directory, which takes the
# dynamic linker with it and leaves every binary in the image unrunnable.
RUN apt-get update && \
    apt-get download raspotify && \
    dpkg-deb -x raspotify_*.deb /tmp/raspotify && \
    cp /tmp/raspotify/usr/bin/librespot /usr/bin/librespot && \
    chmod 0755 /usr/bin/librespot && \
    rm -rf /tmp/raspotify raspotify_*.deb /var/lib/apt/lists/*

# Fail the build here rather than at runtime if the binary cannot start - a
# missing shared library or a bad architecture shows up immediately.
RUN /usr/bin/librespot --version

ENTRYPOINT ["/usr/bin/librespot"]
