# syntax=docker/dockerfile:1
# ==============================================================================
# Parallel Multi-Stage Fast Dockerfile for Dante Audio Hub (Inferno + Statime)
# All builder stages run concurrently in parallel on native host CPU
# ==============================================================================

# ------------------------------------------------------------------------------
# Stage 1: Build Dante Web Player (Go - runs in ~2 seconds)
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS go-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/dante-player .

# ------------------------------------------------------------------------------
# Stage 2A: Build Upstream Statime (PTPv1 Clock Daemon) in Parallel
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS statime-builder
ARG TARGETARCH

RUN apt-get update && apt-get install -y \
    build-essential \
    pkg-config \
    libssl-dev \
    clang \
    libclang-dev \
    llvm-dev \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && \
      apt-get update && \
      apt-get install -y gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross && \
      rustup target add aarch64-unknown-linux-gnu && \
      rm -rf /var/lib/apt/lists/*; \
    fi

ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc
ENV CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc
ENV CXX_aarch64_unknown_linux_gnu=aarch64-linux-gnu-g++

RUN git clone --depth 1 --recurse-submodules --shallow-submodules -b inferno-dev https://github.com/teodly/statime /tmp/statime && \
    cd /tmp/statime && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      cargo build --release --target=aarch64-unknown-linux-gnu; \
      TARGET_DIR="target/aarch64-unknown-linux-gnu/release"; \
    else \
      cargo build --release; \
      TARGET_DIR="target/release"; \
    fi && \
    mkdir -p /out && \
    (cp $TARGET_DIR/statime /out/ 2>/dev/null || cp $TARGET_DIR/statime-linux /out/statime 2>/dev/null || find $TARGET_DIR -maxdepth 1 -type f -executable -exec cp {} /out/statime \;)

# ------------------------------------------------------------------------------
# Stage 2B: Build Upstream Inferno ALSA Module in Parallel
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS inferno-builder
ARG TARGETARCH

RUN apt-get update && apt-get install -y \
    build-essential \
    pkg-config \
    clang \
    libclang-dev \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN if [ "$TARGETARCH" = "arm64" ]; then \
      dpkg --add-architecture arm64 && \
      apt-get update && \
      apt-get install -y gcc-aarch64-linux-gnu g++-aarch64-linux-gnu libc6-dev-arm64-cross libasound2-dev:arm64 && \
      rustup target add aarch64-unknown-linux-gnu && \
      rm -rf /var/lib/apt/lists/*; \
    else \
      apt-get update && apt-get install -y libasound2-dev && rm -rf /var/lib/apt/lists/*; \
    fi

ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc
ENV CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc
ENV CXX_aarch64_unknown_linux_gnu=aarch64-linux-gnu-g++
ENV PKG_CONFIG_PATH_aarch64_unknown_linux_gnu=/usr/lib/aarch64-linux-gnu/pkgconfig
ENV PKG_CONFIG_ALLOW_CROSS=1

RUN git clone --depth 1 --recurse-submodules --shallow-submodules -b dev https://github.com/teodly/inferno /build/inferno && \
    cd /build/inferno && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release --target=aarch64-unknown-linux-gnu -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/aarch64-unknown-linux-gnu/release/libasound_module_pcm_inferno.so /out/; \
    else \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release -p alsa_pcm_inferno && \
      mkdir -p /out && cp target/release/libasound_module_pcm_inferno.so /out/; \
    fi

# ------------------------------------------------------------------------------
# Stage 3: Minimal Runtime Container
# ------------------------------------------------------------------------------
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    libasound2 \
    libasound2-plugins \
    alsa-utils \
    ffmpeg \
    ca-certificates \
    iproute2 \
    procps \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Inferno ALSA module dynamically into system alsa-lib folder
COPY --from=inferno-builder /out/libasound_module_pcm_inferno.so /tmp/libasound_module_pcm_inferno.so
RUN ALSA_DIR=$(dpkg -L libasound2 | grep -m1 'libasound\.so\.2$' | xargs dirname)/alsa-lib && \
    mkdir -p "$ALSA_DIR" && \
    cp /tmp/libasound_module_pcm_inferno.so "$ALSA_DIR/" && \
    rm /tmp/libasound_module_pcm_inferno.so

COPY --from=statime-builder /out/statime /usr/local/bin/
COPY --from=go-builder /out/dante-player /usr/local/bin/

COPY docker/asound.conf /etc/asound.conf
COPY docker/inferno-ptpv1.toml /etc/inferno/inferno-ptpv1.toml
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV INFERNO_NAME="Dante-Pi"
ENV INFERNO_TX_CHANNELS=8
ENV INFERNO_SAMPLE_RATE=48000
ENV HTTP_PORT=8085

EXPOSE 8085
VOLUME ["/root/.local/state/inferno_aoip", "/opt/dante-player/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
