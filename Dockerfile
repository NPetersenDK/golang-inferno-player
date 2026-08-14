# syntax=docker/dockerfile:1
# ==============================================================================
# Fast Multi-Arch Dockerfile for Dante Audio Hub + Upstream Inferno + Statime
# Uses native cross-compilation (no slow QEMU emulation during compilation)
# ==============================================================================

# ------------------------------------------------------------------------------
# Stage 1: Build Dante Web Player (Go - Native cross-compilation in 2 seconds)
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS go-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/dante-player .

# ------------------------------------------------------------------------------
# Stage 2: Build Upstream Inferno & Statime (Rust - Native cross-compilation)
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS rust-builder
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

# Install cross-compilation toolchain and libraries for ARM64
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

# Build upstream Statime (shallow clone for speed)
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

# Build pristine upstream Inferno ALSA virtual soundcard module
RUN git clone --depth 1 --recurse-submodules --shallow-submodules -b dev https://github.com/teodly/inferno /build/inferno && \
    cd /build/inferno && \
    if [ "$TARGETARCH" = "arm64" ]; then \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release --target=aarch64-unknown-linux-gnu -p alsa_pcm_inferno && \
      cp target/aarch64-unknown-linux-gnu/release/libasound_module_pcm_inferno.so /out/; \
    else \
      RUSTFLAGS="-C target-feature=-crt-static" cargo build --release -p alsa_pcm_inferno && \
      cp target/release/libasound_module_pcm_inferno.so /out/; \
    fi

# ------------------------------------------------------------------------------
# Stage 3: Minimal Runtime Container (Target Architecture)
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
