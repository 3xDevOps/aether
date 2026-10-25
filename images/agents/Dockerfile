# Test fixture, not a shipped image. It carries the agent CLIs the
# real-agent suite (internal/server/realagents_integration_test.go) runs,
# pinned, so a vendor release reaches CI only when the versions here are
# bumped. The ACP adapters are the versions Aether installs for members, read
# from internal/harness/acpregistry.json. docs/testing.md, "Real agents
# against a mock model".
ARG BASE=ghcr.io/3xdevops/aether-standard:latest
FROM ${BASE}
ARG CLAUDE_CODE_VERSION=2.1.308
ARG CODEX_VERSION=0.164.0

# A run mounts the member's home over $HOME and sets its own PATH, so the
# CLIs go where that PATH looks: /usr/local/bin.
COPY internal/harness/acpregistry.json /tmp/acpregistry.json
RUN npm install -g --prefix /usr/local --no-fund --no-audit \
        "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}" \
        $(node -p 'require("/tmp/acpregistry.json").agents.filter(a => ["claude-acp", "codex-acp"].includes(a.id)).map(a => a.distribution.npx.package).join(" ")') \
    && rm /tmp/acpregistry.json

# Claude Code would otherwise update itself into ~/.local/bin, which is ahead
# of the pinned CLI on a run's PATH.
ENV DISABLE_AUTOUPDATER=1

RUN claude --version && codex --version && command -v claude-agent-acp codex-acp
