# syntax=docker/dockerfile:1
# Dante Audio Hub. Build context is the repository root, not this directory:
#   docker build -f docker/streamer/Dockerfile .
#
# The Go player and the Inferno ALSA module are cross-compiled on the build
# platform, so only the Debian runtime stage runs under emulation.

# ------------------------------------------------------------------------------
# Stage 1: Dante Web Player (Go)
# ------------------------------------------------------------------------------
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

# ------------------------------------------------------------------------------
# Stage 2: Shared Rust cross-compilation base, so apt runs only once
# ------------------------------------------------------------------------------
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
# Parallel codegen, no whole-program LTO, stripped: build time over runtime speed.
ENV CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16
ENV CARGO_PROFILE_RELEASE_LTO=off
ENV CARGO_PROFILE_RELEASE_STRIP=symbols

# ------------------------------------------------------------------------------
# Stage 3: Upstream Inferno ALSA module
# ------------------------------------------------------------------------------
FROM rust-base AS inferno-builder
ARG TARGETARCH

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    git clone --depth 1 --recurse-submodules --shallow-submodules -b dev https://github.com/teodly/inferno /build/inferno && \
    cd /build/inferno && \
    sed -i '/add_txt_truncated("")/d' inferno_aoip/src/device_server/mdns_server.rs && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release --target=aarch64-unknown-linux-gnu -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/aarch64-unknown-linux-gnu/release/libasound_module_pcm_inferno.so /out/; \
    else \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/release/libasound_module_pcm_inferno.so /out/; \
    fi

# ------------------------------------------------------------------------------
# Stage 4: Runtime. No PTP daemon: dante-player measures the grandmaster itself.
# ------------------------------------------------------------------------------
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

# The alsa-lib plugin directory is multiarch-specific, so ask dpkg where it is.
COPY --from=inferno-builder /out/libasound_module_pcm_inferno.so /tmp/libasound_module_pcm_inferno.so
RUN ALSA_DIR=$(dpkg -L libasound2 | grep -m1 'libasound\.so\.2$' | xargs dirname)/alsa-lib && \
    mkdir -p "$ALSA_DIR" && \
    cp /tmp/libasound_module_pcm_inferno.so "$ALSA_DIR/" && \
    rm /tmp/libasound_module_pcm_inferno.so

COPY --from=go-builder /out/dante-player /usr/local/bin/

COPY docker/streamer/asound.conf /etc/asound.conf
# --chmod avoids a separate RUN layer, which on arm64 would run under emulation
COPY --chmod=0755 docker/streamer/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV INFERNO_NAME="Dante-Pi"
ENV DANTE_TX_CHANNELS=8
ENV HTTP_PORT=8085

# Relative paths in config.yaml, data_dir and the soundboard among them, resolve
# against this rather than against /.
WORKDIR /opt/dante-player

EXPOSE 8085
VOLUME ["/root/.local/state/inferno_aoip", "/opt/dante-player/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
