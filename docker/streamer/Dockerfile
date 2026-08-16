# syntax=docker/dockerfile:1
# Build context is the repository root:  docker build -f docker/streamer/Dockerfile .
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS go-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/dante-player .

# Shared Rust base so the apt install runs once, not per Rust stage.
FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS rust-base
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    libssl-dev \
    clang \
    libclang-dev \
    llvm-dev \
    git \
    ca-certificates \
    && if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && \
      apt-get update && \
      apt-get install -y --no-install-recommends gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross libasound2-dev:arm64 && \
      rustup target add aarch64-unknown-linux-gnu; \
    else \
      apt-get install -y --no-install-recommends libasound2-dev; \
    fi \
    && rm -rf /var/lib/apt/lists/*

ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc
ENV CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc
ENV CXX_aarch64_unknown_linux_gnu=aarch64-linux-gnu-g++
ENV PKG_CONFIG_PATH_aarch64_unknown_linux_gnu=/usr/lib/aarch64-linux-gnu/pkgconfig
ENV PKG_CONFIG_ALLOW_CROSS=1
# Build time favoured over runtime speed.
ENV CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16
ENV CARGO_PROFILE_RELEASE_LTO=off
ENV CARGO_PROFILE_RELEASE_STRIP=symbols

FROM rust-base AS inferno-builder
ARG TARGETARCH

# Replaces the upstream file wholesale, so it is pinned to the revision it was
# forked from. Move it into a fork once there is one.
COPY docker/streamer/patches/info_mcast_server.rs /patches/info_mcast_server.rs

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    git clone --depth 1 --recurse-submodules --shallow-submodules -b dev https://github.com/teodly/inferno /build/inferno && \
    cd /build/inferno && \
    # sed exits 0 when nothing matched, so every patch is grep-verified: a silent
    # no-op would only surface as odd Dante behaviour days later.
    MDNS=inferno_aoip/src/device_server/mdns_server.rs && \
    MCAST=inferno_aoip/src/device_server/info_mcast_server.rs && \
    grep -q 'add_txt_truncated("")' "$MDNS" && \
    sed -i '/add_txt_truncated("")/d' "$MDNS" && \
    # Fails the build when upstream touches the file, rather than overwriting
    # their changes. Rebase patches/info_mcast_server.rs and update the hash.
    echo "351d7578ad5ce7678e3aec4e57bc76170778f42f25e9a7bfb766da362258b0bf  $MCAST" | sha256sum -c - && \
    cp /patches/info_mcast_server.rs "$MCAST" && \
    # The heartbeat to 224.0.0.233:8708 degrades and eventually kills an Apollo
    # E1X on the same segment. Device info on 224.0.0.231:8702 is untouched, so
    # Dante Controller still works.
    grep -q 'async fn send_heartbeat(&mut self) {$' "$MCAST" && \
    sed -i 's/async fn send_heartbeat(&mut self) {$/async fn send_heartbeat(\&mut self) { return;/' "$MCAST" && \
    grep -q 'send_heartbeat(&mut self) { return;' "$MCAST" && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release --target=aarch64-unknown-linux-gnu -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/aarch64-unknown-linux-gnu/release/libasound_module_pcm_inferno.so /out/; \
    else \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/release/libasound_module_pcm_inferno.so /out/; \
    fi

# Runtime. No PTP daemon: dante-player measures the grandmaster itself.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libasound2 \
    libasound2-plugins \
    alsa-utils \
    ffmpeg \
    ca-certificates \
    iproute2 \
    procps \
    curl \
    rtl-sdr \
    && rm -rf /var/lib/apt/lists/*

# Plugin dir is multiarch-specific, so ask dpkg rather than hardcode it.
COPY --from=inferno-builder /out/libasound_module_pcm_inferno.so /tmp/libasound_module_pcm_inferno.so
RUN ALSA_DIR=$(dpkg -L libasound2 | grep -m1 'libasound\.so\.2$' | xargs dirname)/alsa-lib && \
    mkdir -p "$ALSA_DIR" && \
    cp /tmp/libasound_module_pcm_inferno.so "$ALSA_DIR/" && \
    rm /tmp/libasound_module_pcm_inferno.so

COPY --from=go-builder /out/dante-player /usr/local/bin/

COPY docker/streamer/asound.conf /etc/asound.conf
# --chmod avoids a RUN layer, which on arm64 would run under emulation.
COPY --chmod=0755 docker/streamer/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV INFERNO_NAME="Dante-Pi"
ENV DANTE_TX_CHANNELS=8
ENV HTTP_PORT=8085

# Relative paths in config.yaml resolve against this, not against /.
WORKDIR /opt/dante-player

EXPOSE 8085
VOLUME ["/root/.local/state/inferno_aoip", "/opt/dante-player/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
