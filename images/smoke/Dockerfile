# Test fixture, not a shipped image. It carries the agent CLIs and no
# credentials, so the real-harness smoke tests
# (internal/harness/smoke_integration_test.go) can prove the argv Aether
# ships is still the argv the current CLI accepts.
#
# The install commands are the vendors' own, deliberately unpinned and
# deliberately duplicated from harness.Profile.InstallScript: this image
# exists to fail when today's release of an agent stops accepting a flag,
# so pinning a version would defeat it. OPENCODE_VERSION is today's release
# resolved by the caller with an authenticated GitHub call: the installer's
# own lookup is anonymous and is rate-limited on CI runners. Empty leaves
# the installer to look it up itself.
ARG BASE=ghcr.io/3xdevops/aether-standard:latest
FROM ${BASE}
ARG OPENCODE_VERSION=

# Root, like a real run on the standard image.
USER root
ENV HOME=/root

# Each installer pulls from its vendor's servers, which fail now and then,
# so each step gets three tries. The installers are saved before they run:
# sh has no pipefail, so curl | bash would run an empty script and pass.
RUN for try in 1 2 3; do \
      curl -fsSL --retry 3 -o /tmp/claude-install.sh https://claude.ai/install.sh && \
      bash /tmp/claude-install.sh && exit 0; sleep 15; \
    done; exit 1
RUN for try in 1 2 3; do \
      npm install -g --prefix "$HOME/.local" @openai/codex && exit 0; sleep 15; \
    done; exit 1
RUN for try in 1 2 3; do \
      curl -fsSL --retry 3 -o /tmp/opencode-install.sh https://opencode.ai/install && \
      bash /tmp/opencode-install.sh ${OPENCODE_VERSION:+--version "$OPENCODE_VERSION"} && exit 0; sleep 15; \
    done; exit 1

ENV PATH="/root/.local/bin:/root/.opencode/bin:${PATH}"

# Fail the build rather than the test run when an installer silently
# changes where it puts the executable.
RUN claude --version && codex --version && opencode --version
