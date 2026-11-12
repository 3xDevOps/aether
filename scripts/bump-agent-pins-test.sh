#!/bin/sh
# Runs bump-agent-pins.sh on copies of the real pin files, with curl and npm
# stubbed.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(dirname -- "$script_dir")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/repo/scripts" "$tmp/repo/internal/harness" "$tmp/repo/images/agents"
cp "$script_dir/bump-agent-pins.sh" "$tmp/repo/scripts/"
cp "$repo/internal/harness/acpregistry.json" "$repo/internal/harness/acp.go" "$tmp/repo/internal/harness/"
cp "$repo/images/agents/Dockerfile" "$tmp/repo/images/agents/"
export FIXTURES="$tmp" PATH="$tmp/bin:$PATH" SDK=0.9.0

cat >"$tmp/bin/curl" <<'STUB'
#!/bin/sh
cat "$FIXTURES/registry.json"
STUB
cat >"$tmp/bin/npm" <<'STUB'
#!/bin/sh
set -eu
[ "$1" = view ] || exit 90
case "$2 $3" in
"@agentclientprotocol/claude-agent-acp@9.0.0 dependencies") echo "{\"zod\":\"4.0.0\",\"@anthropic-ai/claude-agent-sdk\":\"$SDK\"}" ;;
"@anthropic-ai/claude-agent-sdk@0.9.0 claudeCodeVersion") echo 2.9.0 ;;
"@agentclientprotocol/codex-acp@9.0.0 dependencies") echo '{"@openai/codex":"^0.9.0"}' ;;
"@openai/codex@^0.9.0 version")
	[ -z "${NOMATCH-}" ] || { echo '{"error": {"code": "E404", "summary": "No match"}}'; exit 1; }
	echo '["0.9.0","0.9.10","0.9.2"]'
	;;
"@anthropic-ai/claude-code version") echo 2.9.1 ;;
"@openai/codex version") echo 0.9.11 ;;
*) echo "unexpected npm view $*" >&2; exit 91 ;;
esac
STUB
chmod +x "$tmp/bin/curl" "$tmp/bin/npm"

jq '.agents |= map(.version = "9.0.0" | .icon = "icon.svg"
	| .distribution = {binary: {}, npx: {package: ((.distribution.npx.package | sub("@[^@]*$"; "")) + "@9.0.0")}})' \
	"$repo/internal/harness/acpregistry.json" >"$tmp/registry.json"

fail() {
	echo "bump-agent-pins-test: $*" >&2
	exit 1
}

out=$(sh "$tmp/repo/scripts/bump-agent-pins.sh")
[ "$(printf '%s\n' "$out" | wc -l)" -eq 7 ] || fail "want 7 moved pins, got: $out"
printf '%s\n' "$out" | grep -q '^Codex in codex-acp .* -> 0.9.10$' || fail "Codex is not the newest in the range: $out"
for want in \
	'Package: "@agentclientprotocol/claude-agent-acp", Version: "9.0.0"' \
	'CLIEnv: "CLAUDE_CODE_EXECUTABLE", CLIVersion: "2.9.0"' \
	'Package: "@agentclientprotocol/codex-acp", Version: "9.0.0"' \
	'CLIEnv: "CODEX_PATH", CLIVersion: "0.9.10"' \
	'Package: "pi-acp", Version: "9.0.0"'; do
	grep -qF "$want" "$tmp/repo/internal/harness/acp.go" || fail "acp.go lacks $want"
done
grep -qx 'ARG CLAUDE_CODE_VERSION=2.9.1' "$tmp/repo/images/agents/Dockerfile" || fail "the agents image keeps its old Claude Code"
grep -qx 'ARG CODEX_VERSION=0.9.11' "$tmp/repo/images/agents/Dockerfile" || fail "the agents image keeps its old Codex"
jq -e 'all(.agents[]; (keys == ["distribution", "id", "license", "name", "repository", "version"])
	and .version == "9.0.0" and (.distribution | keys == ["npx"]) and (.distribution.npx.package | endswith("@9.0.0")))' \
	"$tmp/repo/internal/harness/acpregistry.json" >/dev/null || fail "acpregistry.json is not the registry's entries in Aether's shape"

before=$(cat "$tmp/repo/internal/harness/"* "$tmp/repo/images/agents/Dockerfile" | cksum)
out=$(sh "$tmp/repo/scripts/bump-agent-pins.sh")
[ -z "$out" ] || fail "a second run moved pins: $out"
[ "$(cat "$tmp/repo/internal/harness/"* "$tmp/repo/images/agents/Dockerfile" | cksum)" = "$before" ] || fail "a second run changed files"

if SDK='^0.9.0' sh "$tmp/repo/scripts/bump-agent-pins.sh" >/dev/null 2>"$tmp/err"; then
	fail "an SDK range was accepted"
fi
grep -q 'not one exact version' "$tmp/err" || fail "unexpected error for an SDK range: $(cat "$tmp/err")"

if NOMATCH=1 sh "$tmp/repo/scripts/bump-agent-pins.sh" >/dev/null 2>"$tmp/err"; then
	fail "an npm error was taken for a version"
fi
grep -q 'no version found for Codex in codex-acp' "$tmp/err" || fail "unexpected error for an npm error: $(cat "$tmp/err")"
grep -qF 'CLIEnv: "CODEX_PATH", CLIVersion: "0.9.10"' "$tmp/repo/internal/harness/acp.go" || fail "an npm error changed the Codex pin"

jq '.agents |= map(select(.id != "pi-acp"))' "$tmp/registry.json" >"$tmp/dropped.json"
mv "$tmp/dropped.json" "$tmp/registry.json"
if sh "$tmp/repo/scripts/bump-agent-pins.sh" >/dev/null 2>"$tmp/err"; then
	fail "an adapter the registry dropped was accepted"
fi
grep -q 'no longer lists pi-acp' "$tmp/err" || fail "unexpected error for a dropped adapter: $(cat "$tmp/err")"
[ ! -e "$tmp/repo/internal/harness/acpregistry.json.tmp" ] || fail "a failed run left acpregistry.json.tmp"
