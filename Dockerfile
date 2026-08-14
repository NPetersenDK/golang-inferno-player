# ==============================================================================
# Multi-Stage Dockerfile for Dante Audio Hub + Upstream Inferno + Statime
# Build Context: golang-inferno-player repo
# Supports Multi-Arch: linux/amd64, linux/arm64 (Raspberry Pi 4/5)
# ==============================================================================

# ------------------------------------------------------------------------------
# Stage 1: Build Dante Web Player & Stream Engine (Go)
# ------------------------------------------------------------------------------
FROM golang:1.22-alpine AS go-builder
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/dante-player .

# ------------------------------------------------------------------------------
# Stage 2: Build Upstream Inferno ALSA Plugin & Statime (Rust)
# ------------------------------------------------------------------------------
FROM rust:1-slim-bookworm AS rust-builder

RUN apt-get update && apt-get install -y \
    build-essential \
    pkg-config \
    libasound2-dev \
    libssl-dev \
    clang \
    libclang-dev \
    llvm-dev \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Build upstream Statime (PTP clock daemon for Dante)
RUN git clone --recurse-submodules -b inferno-dev https://github.com/teodly/statime /tmp/statime && \
    cd /tmp/statime && \
    cargo build --release && \
    mkdir -p /out && \
    (cp target/release/statime /out/ 2>/dev/null || cp target/release/statime-linux /out/statime 2>/dev/null || find target/release -maxdepth 1 -type f -executable -exec cp {} /out/statime \;)

# Build pristine upstream Inferno ALSA virtual soundcard module
RUN git clone --recurse-submodules -b dev https://github.com/teodly/inferno /build/inferno && \
    cd /build/inferno && \
    RUSTFLAGS="-C target-feature=-crt-static" cargo build --release -p alsa_pcm_inferno && \
    cp target/release/libasound_module_pcm_inferno.so /out/

# ------------------------------------------------------------------------------
# Stage 3: Minimal Runtime Container
# ------------------------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    libasound2 \
    libasound2-plugins \
    ffmpeg \
    ca-certificates \
    iproute2 \
    procps \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Inferno ALSA module dynamically in the system alsa-lib directory (multi-arch compatible)
COPY --from=rust-builder /out/libasound_module_pcm_inferno.so /tmp/libasound_module_pcm_inferno.so
RUN ALSA_DIR=$(dpkg -L libasound2 | grep -m1 'libasound\.so\.2$' | xargs dirname)/alsa-lib && \
    mkdir -p "$ALSA_DIR" && \
    cp /tmp/libasound_module_pcm_inferno.so "$ALSA_DIR/" && \
    rm /tmp/libasound_module_pcm_inferno.so

COPY --from=rust-builder /out/statime /usr/local/bin/
COPY --from=go-builder /out/dante-player /usr/local/bin/

# Copy ALSA configuration & Startup scripts
COPY docker/asound.conf /etc/asound.conf
COPY docker/inferno-ptpv1.toml /etc/inferno/inferno-ptpv1.toml
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV INFERNO_NAME="Dante-Pi"
ENV INFERNO_TX_CHANNELS=8
ENV INFERNO_SAMPLE_RATE=48000
ENV HTTP_PORT=8080

EXPOSE 8080
VOLUME ["/root/.local/state/inferno_aoip", "/opt/dante-player/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
