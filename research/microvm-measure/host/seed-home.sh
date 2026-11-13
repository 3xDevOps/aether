#!/usr/bin/env bash
# Writes the real-agent suite's mock sign-ins into an agent home <dir>
# (as internal/server/realagents_integration_test.go does). The key is the
# suite's mock key, not a credential.
set -euo pipefail
dir=$1 url=${2:-http://10.201.0.1:18080}
key=aether-mock-model-api-key
sudo mkdir -p "$dir/.claude" "$dir/.codex"
sudo tee "$dir/.claude.json" >/dev/null <<J
{"hasCompletedOnboarding":true,"customApiKeyResponses":{"approved":["${key: -20}"]},"projects":{"/workspace":{"hasTrustDialogAccepted":true}}}
J
echo '{"skipDangerousModePermissionPrompt": true}' | sudo tee "$dir/.claude/settings.json" >/dev/null
echo "{\"auth_mode\": \"apikey\", \"OPENAI_API_KEY\": \"$key\"}" | sudo tee "$dir/.codex/auth.json" >/dev/null
sudo tee "$dir/.codex/config.toml" >/dev/null <<T
openai_base_url = "$url/v1"
model = "mock-model"
check_for_update_on_startup = false

[projects."/workspace"]
trust_level = "trusted"
T
