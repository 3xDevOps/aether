#!/usr/bin/env bash
# Starts a pinned agent CLI in a tmux session inside VM or container <target>
# with a first prompt that plays mock script <script>.
#   agent.sh vm <n> claude|codex <script>
#   agent.sh ctr <name> claude|codex <script>
#   agent.sh prompt vm|ctr <target> <script>   send a later prompt
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
key=aether-mock-model-api-key
url=${MOCK_URL:-http://10.201.0.1:18080}

in_target() { # kind target command...
  case "$1" in
    vm) "$HERE/vm.sh" ssh "$2" "${@:3}" ;;
    ctr) docker exec "$2" bash -c "${*:3}" ;;
  esac
}

if [ "$1" = prompt ]; then
  in_target "$2" "$3" "tmux send-keys -t agent -l 'aether-script:$4; run the command, then stop.' && sleep 0.2 && tmux send-keys -t agent Enter"
  exit
fi

kind=$1 target=$2 agent=$3 script=$4
env="ANTHROPIC_API_KEY=$key ANTHROPIC_BASE_URL=$url IS_SANDBOX=1 DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 TERM=xterm-256color"
case "$agent" in
  claude) cmd="claude --dangerously-skip-permissions" ;;
  codex) cmd="codex --dangerously-bypass-approvals-and-sandbox" ;;
esac
in_target "$kind" "$target" "cd /workspace && tmux new-session -d -s agent -x 160 -y 48 \"env $env $cmd 'aether-script:$script; run the command, then stop.'\""
