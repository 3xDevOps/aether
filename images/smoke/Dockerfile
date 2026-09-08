# Test fixture, not a shipped image. It carries the agent CLIs and no
# credentials, so the real-harness smoke tests
# (internal/harness/smoke_integration_test.go) can prove the argv Aether
# ships is still the argv the current CLI accepts.
#
# The install commands are the vendors' own, deliberately unpinned and
# deliberately duplicated from harness.Profile.InstallScript: this image
# exists to fail when today's release of an agent stops accepting a flag,
# so pinning a version would defeat it.
ARG BASE=ghcr.io/3xdevops/aether-standard:latest
FROM ${BASE}

# Root, like a real run on the standard image.
USER root
ENV HOME=/root

RUN curl -fsSL https://claude.ai/install.sh | bash
RUN npm install -g --prefix "$HOME/.local" @openai/codex
RUN curl -fsSL https://opencode.ai/install | bash

ENV PATH="/root/.local/bin:/root/.opencode/bin:${PATH}"

# Fail the build rather than the test run when an installer silently
# changes where it puts the executable.
RUN claude --version && codex --version && opencode --version
