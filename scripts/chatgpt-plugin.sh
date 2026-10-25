#!/bin/sh
# Build the ChatGPT plugin directory ZIP from packaging/chatgpt-plugin/.
#
#     sh scripts/chatgpt-plugin.sh dist/aether-chatgpt-plugin.zip [package dir]
#
# Checks plugin.json and mcp.json against OpenAI's submission limits
# (docs/chatgpt-plugin.md), copies the listing icons from web/public/icons/,
# and refuses a package OpenAI would reject. A field still set to a
# "PLACEHOLDER:" value is printed as a warning: the ZIP uploads as a draft but
# cannot be submitted for review until it is replaced. Needs jq and python3.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(dirname -- "$script_dir")
icons=$root/web/public/icons
max_zip=$((100 * 1024 * 1024))
max_image=$((5 * 1024 * 1024))

fail() {
	echo "chatgpt-plugin: $1" >&2
	exit 1
}

[ "$#" -eq 1 ] || [ "$#" -eq 2 ] || {
	echo "usage: chatgpt-plugin.sh <out.zip> [package dir]" >&2
	exit 2
}
out=$1
src=${2:-$root/packaging/chatgpt-plugin}
command -v jq >/dev/null || fail "jq not found; install it (on Debian or Ubuntu: sudo apt-get install jq)"
command -v python3 >/dev/null || fail "python3 not found"

problems=$(jq -r '
	def text($f; $max): if (.[$f] | type) != "string" or (.[$f] | length) == 0 then "\($f) is missing"
		elif (.[$f] | length) > $max then "\($f) is \(.[$f] | length) characters, at most \($max)" else empty end;
	def url($f): if (.[$f] | type) != "string" then "\($f) is missing"
		elif (.[$f] | startswith("PLACEHOLDER:")) then empty
		elif (.[$f] | test("^https://[^@ ]+$") | not) then "\($f) is not an https URL: \(.[$f])"
		elif (.[$f] | length) > 1024 then "\($f) is longer than 1024 characters" else empty end;
	(if ."$schema" != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" then "plugin.json $schema is not Agent Plugins 1.0.0" else empty end),
	(if (.name | type) == "string" and (.name | test("^(?!.*(--|\\.\\.))[a-z0-9]([a-z0-9.-]*[a-z0-9])?$")) and (.name | length) <= 64 then empty
		else "name must be lowercase letters, digits and single hyphens, at most 64 characters" end),
	(.extensions["com.openai"].interface // {} |
		text("displayName"; 30), text("shortDescription"; 30), text("longDescription"; 4000), text("developerName"; 80),
		(if .category != "Developer Tools" then "category is \(.category), want Developer Tools" else empty end),
		(if (.capabilities | length) > 20 then "capabilities has \(.capabilities | length) entries, at most 20" else empty end),
		(.capabilities[]? | select(length > 120) | "capability longer than 120 characters: \(.)"),
		(if (.defaultPrompt | length) > 3 then "defaultPrompt has more than 3 prompts" else empty end),
		(.defaultPrompt[]? | select(length > 128) | "defaultPrompt longer than 128 characters: \(.)"),
		url("websiteURL"), url("supportURL"), url("privacyPolicyURL"), url("termsOfServiceURL"),
		(.brandColor, .brandColorDark | select(. != null and (test("^#[0-9A-Fa-f]{6}$") | not)) | "brand color \(.) is not #RRGGBB"),
		(.logo, .composerIcon | select(. == null) | "logo and composerIcon are both required")),
	(.extensions["com.openai"].review // {} |
		(if (.test_cases.positive | length) != 5 then "review needs exactly 5 positive test cases" else empty end),
		(if (.test_cases.negative | length) != 3 then "review needs exactly 3 negative test cases" else empty end),
		(.test_cases.positive[]? | select(.description == null or .prompt == null or .tools_triggered == null or .expected_behavior == null)
			| "positive test case lacks description, prompt, tools_triggered or expected_behavior: \(.prompt)"),
		(.test_cases.negative[]? | select(.description == null or .prompt == null) | "negative test case lacks description or prompt"),
		(.test_cases.positive[]? | select((.description | length) > 4000) | "positive test case description over 4000 characters")),
	(paths(type == "object") as $p | getpath($p) | keys[] | select(. == "test_credentials" or . == "reviewer_instructions")
		| "\(.) must be entered in the portal, not the package")
' "$src/plugin.json") || fail "$src/plugin.json is not valid JSON"
problems=$problems$(jq -r '
	(if ."$schema" != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" then "mcp.json $schema is not Agent Plugins 1.0.0" else empty end),
	(if (.mcpServers | length) != 1 then "mcp.json must declare exactly one server" else empty end),
	(.mcpServers[]? |
		(if .type != "streamable-http" or (.url | test("^https://[^@ ]+$") | not) then "the MCP server must be streamable-http at an https URL" else empty end),
		(if .headers then "the MCP server must not set headers" else empty end),
		(if .extensions["com.openai"].auth.type != "oauth" then "the MCP server must use OAuth" else empty end))
' "$src/mcp.json") || fail "$src/mcp.json is not valid JSON"
[ -z "$problems" ] || fail "package problems:
$problems"

jq -r 'paths(type == "string") as $p | select(getpath($p) | startswith("PLACEHOLDER:")) | $p | map(tostring) | join(".")' "$src/plugin.json" |
	while IFS= read -r field; do
		echo "chatgpt-plugin: warning: $field is a placeholder; replace it before submitting (docs/chatgpt-plugin.md)" >&2
	done

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/assets"
cp "$src/plugin.json" "$src/mcp.json" "$stage/"
cp "$icons/icon-maskable-512.png" "$stage/assets/logo.png"
cp "$icons/icon-maskable-192.png" "$stage/assets/composer-icon.png"

for path in $(jq -r '.extensions["com.openai"].interface | .logo, .composerIcon' "$src/plugin.json"); do
	image=$stage/${path#./}
	[ -f "$image" ] || fail "$path is referenced by plugin.json but not packaged"
	[ "$(wc -c <"$image")" -le "$max_image" ] || fail "$path is larger than 5 MiB"
	# A PNG's width and height are the big-endian words at bytes 16 and 20.
	set -- $(od -An -tu1 -j16 -N8 "$image")
	width=$(($1 * 16777216 + $2 * 65536 + $3 * 256 + $4))
	height=$(($5 * 16777216 + $6 * 65536 + $7 * 256 + $8))
	[ "$width" -eq "$height" ] && [ "$width" -ge 48 ] && [ "$width" -le 4096 ] ||
		fail "$path is ${width}x$height; icons must be square, 48 to 4096 pixels"
done

mkdir -p "$(dirname -- "$out")"
rm -f "$out"
out_abs=$(CDPATH= cd -- "$(dirname -- "$out")" && pwd)/$(basename -- "$out")
(cd "$stage" && python3 -m zipfile -c "$out_abs" plugin.json mcp.json assets)
[ "$(wc -c <"$out")" -lt "$max_zip" ] || fail "$out is 100 MB or larger"
echo "wrote $out"
