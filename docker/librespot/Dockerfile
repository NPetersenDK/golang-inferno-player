# syntax=docker/dockerfile:1
# Spotify Connect producer for a pipe source zone, published by the
# build-librespot workflow as ghcr.io/npetersendk/dante-librespot.
#
# librespot has no official image and raspotify's Dockerfile only cross-builds
# .deb packages, so this takes the finished package and runs the binary.

FROM debian:bookworm-slim

# -x names the failing command in the build log instead of collapsing the chain.
SHELL ["/bin/sh", "-eux", "-c"]

# apt-get download drops privileges to _apt, which cannot write to /.
WORKDIR /tmp

# raspotify compiles the ALSA and PulseAudio backends in, so both shared objects
# must exist even though only the pipe backend is used.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      libasound2 \
      libpulse0

RUN curl -fsSL https://dtcooper.github.io/raspotify/key.asc \
      -o /usr/share/keyrings/raspotify_key.asc && \
    chmod 0644 /usr/share/keyrings/raspotify_key.asc && \
    echo 'deb [signed-by=/usr/share/keyrings/raspotify_key.asc] https://dtcooper.github.io/raspotify raspotify main' \
      > /etc/apt/sources.list.d/raspotify.list

# Extract rather than install, because the package's systemd unit has nothing to
# talk to here. Extract to a staging directory, never onto /: Debian is
# usrmerged, so unpacking a package carrying a real ./lib would replace the
# /lib symlink and take the dynamic linker with it.
RUN apt-get update && \
    apt-get download raspotify && \
    dpkg-deb -x raspotify_*.deb /tmp/raspotify && \
    cp /tmp/raspotify/usr/bin/librespot /usr/bin/librespot && \
    chmod 0755 /usr/bin/librespot && \
    rm -rf /tmp/raspotify raspotify_*.deb /var/lib/apt/lists/*

# Catch a missing shared library or wrong architecture before publishing.
RUN /usr/bin/librespot --version

ENTRYPOINT ["/usr/bin/librespot"]
