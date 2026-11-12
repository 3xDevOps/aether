#!/bin/sh
# docs/testing.md, "Bumping the pinned versions".
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
snapshot=$repo/internal/harness/acpregistry.json
pins=$repo/internal/harness/acp.go
image=$repo/images/agents/Dockerfile

fail() {
	echo "bump-agent-pins: $*" >&2
	exit 1
}

# pin FILE KEY NEW NAME: sets the version right after the one occurrence of
# KEY in FILE to NEW.
pin() {
	old=$(awk -v key="$2" '(i = index($0, key)) { n++; v = substr($0, i + length(key)); match(v, /^[0-9A-Za-z.+-]*/); print substr(v, 1, RLENGTH) }
		END { exit n != 1 }' "$1") || fail "$1 must contain $2 exactly once"
	case $3 in '' | *[!0-9A-Za-z.+-]*) fail "no version found for $4: \"$3\"" ;; esac
	[ "$old" = "$3" ] && return
	awk -v key="$2" -v old="$old" -v new="$3" '(i = index($0, key)) { $0 = substr($0, 1, i + length(key) - 1) new substr($0, i + length(key) + length(old)) }
		{ print }' "$1" >"$1.tmp"
	mv "$1.tmp" "$1"
	echo "$4 $old -> $3"
}

dependency() {
	npm view "$1" dependencies --json | jq -r --arg dep "$2" '.[$dep] // empty'
}

live=$(curl -fsSL --retry 3 https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json)
jq --argjson live "$live" '.agents |= map(.id as $id
	| (first($live.agents[] | select(.id == $id)) // error("the ACP registry no longer lists \($id)"))
	| {id, name, version, repository, license, distribution: {npx: (.distribution.npx // error("\($id) has no npx distribution"))}})' \
	"$snapshot" >"$snapshot.tmp" || {
	rm -f "$snapshot.tmp"
	exit 1
}
mv "$snapshot.tmp" "$snapshot"

for spec in $(jq -r '.agents[].distribution.npx.package' "$snapshot"); do
	package=${spec%@*} version=${spec##*@}
	pin "$pins" "Package: \"$package\", Version: \"" "$version" "$package"
	case $package in
	@agentclientprotocol/claude-agent-acp)
		sdk=$(dependency "$spec" @anthropic-ai/claude-agent-sdk)
		case $sdk in '' | *[!0-9.]*) fail "$spec depends on @anthropic-ai/claude-agent-sdk \"$sdk\", not one exact version, so the Claude Code it bundles is unknown" ;; esac
		pin "$pins" 'CLIEnv: "CLAUDE_CODE_EXECUTABLE", CLIVersion: "' "$(npm view "@anthropic-ai/claude-agent-sdk@$sdk" claudeCodeVersion)" "Claude Code in ${package#*/}"
		;;
	@agentclientprotocol/codex-acp)
		range=$(dependency "$spec" @openai/codex)
		[ -n "$range" ] || fail "$spec no longer depends on @openai/codex"
		pin "$pins" 'CLIEnv: "CODEX_PATH", CLIVersion: "' "$(npm view "@openai/codex@$range" version --json | jq -r 'if type == "array" then .[] else . end' | sort -V | tail -n 1)" "Codex in ${package#*/}"
		;;
	esac
done

pin "$image" 'ARG CLAUDE_CODE_VERSION=' "$(npm view @anthropic-ai/claude-code version)" "Claude Code in the real-agent suite"
pin "$image" 'ARG CODEX_VERSION=' "$(npm view @openai/codex version)" "Codex in the real-agent suite"
