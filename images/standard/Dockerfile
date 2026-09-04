FROM ubuntu:24.04

# Every toolchain version is pinned here so a bump is a one-line diff.
# Nothing in this file installs "latest".
ARG GO_VERSION=1.27.0
ARG NODE_VERSION=24.20.0
ARG FNM_VERSION=1.39.0
ARG UV_VERSION=0.12.7
ARG RUST_VERSION=1.98.0

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        build-essential \
        ca-certificates \
        curl \
        findutils \
        git \
        grep \
        jq \
        pkg-config \
        python3 \
        python3-venv \
        ripgrep \
        sudo \
        unzip \
    && rm -rf /var/lib/apt/lists/*

# Go from the official tarball, on PATH for every uid via /usr/local/bin.
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
        | tar -C /usr/local -xz \
    && ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Node LTS via fnm. FNM_DIR is shared and world-readable; the installed
# version's binaries are shimmed into /usr/local/bin so no shell setup is
# needed to use them.
ENV FNM_DIR=/usr/local/share/fnm
RUN case "$(dpkg --print-architecture)" in \
        amd64) fnm_asset=fnm-linux.zip ;; \
        arm64) fnm_asset=fnm-arm64.zip ;; \
        *) echo "unsupported architecture for fnm" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/fnm.zip \
        "https://github.com/Schniz/fnm/releases/download/v${FNM_VERSION}/${fnm_asset}" \
    && unzip -q /tmp/fnm.zip -d /usr/local/bin \
    && chmod 0755 /usr/local/bin/fnm \
    && rm /tmp/fnm.zip \
    && fnm install "v${NODE_VERSION}" \
    && for tool in node npm npx corepack; do \
        ln -s "${FNM_DIR}/node-versions/v${NODE_VERSION}/installation/bin/${tool}" \
            "/usr/local/bin/${tool}"; \
    done \
    && chmod -R a+rX "${FNM_DIR}"

# uv's standalone binary; its installer only fetches and unpacks the
# pinned release.
RUN curl -fsSL "https://astral.sh/uv/${UV_VERSION}/install.sh" \
        | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh

# Rust via rustup with shared, writable homes (the official rust image's
# layout) so any container uid can run and extend the toolchain.
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo
RUN curl -fsSL https://sh.rustup.rs \
        | sh -s -- -y --no-modify-path --profile minimal \
            --default-toolchain "${RUST_VERSION}" \
    && for tool in rustc cargo rustup rustdoc; do \
        ln -s "${CARGO_HOME}/bin/${tool}" "/usr/local/bin/${tool}"; \
    done \
    && chmod -R a+w "${RUSTUP_HOME}" "${CARGO_HOME}"

WORKDIR /workspace

CMD ["/bin/bash"]
