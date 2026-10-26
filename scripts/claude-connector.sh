#!/bin/sh
# Check the Claude connector directory listing in packaging/claude-connector/
# and stage it for the submission portal.
#
#     sh scripts/claude-connector.sh dist/aether-claude-connector [package dir]
#
# Checks listing.json against Anthropic's submission limits
# (docs/claude-connector.md), and writes listing.json and icon.png to the
# output directory. A field still set to a "PLACEHOLDER:" value is printed as
# a warning: the listing cannot be submitted until it is replaced. Needs jq.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(dirname -- "$script_dir")

fail() {
	echo "claude-connector: $1" >&2
	exit 1
}

[ "$#" -eq 1 ] || [ "$#" -eq 2 ] || {
	echo "usage: claude-connector.sh <out dir> [package dir]" >&2
	exit 2
}
out=$1
src=${2:-$root/packaging/claude-connector}
command -v jq >/dev/null || fail "jq not found; install it (on Debian or Ubuntu: sudo apt-get install jq)"

chatgpt_url=$(jq -r '.mcpServers[].url' "$root/packaging/chatgpt-plugin/mcp.json") ||
	fail "packaging/chatgpt-plugin/mcp.json is not valid JSON"

problems=$(jq -r --arg chatgpt_url "$chatgpt_url" --argjson acks '["Directory guidelines", "First-party API usage",
	"Financial transactions", "AI media generation", "Prompt injection", "Conversation data collection", "Public documentation"]' '
	def text($f; $max): if (.[$f] | type) != "string" or (.[$f] | length) == 0 then "\($f) is missing"
		elif (.[$f] | length) > $max then "\($f) is \(.[$f] | length) characters, at most \($max)" else empty end;
	def url($f): if (.[$f] | type) != "string" then "\($f) is missing"
		elif (.[$f] | startswith("PLACEHOLDER:")) then empty
		elif (.[$f] | test("^https://[^@ ]+$") | not) then "\($f) is not an https URL: \(.[$f])" else empty end;
	text("name"; 100), text("one_liner"; 200), text("description"; 2000),
	(if (.categories | type) != "array" or (.categories | length) < 1 or (.categories | length) > 5
		then "categories needs 1 to 5 entries"
		elif any(.categories[]; type != "string" or length == 0) then "categories entries must be non-empty text"
		elif (.categories | unique | length) != (.categories | length) then "categories repeats an entry" else empty end),
	(if (.slug | type) == "string" and (.slug | test("^[a-z0-9]+(-[a-z0-9]+)*$")) then empty
		else "slug must be lowercase letters and digits joined by single hyphens; it is permanent once published" end),
	url("server_url"),
	(if .server_url != $chatgpt_url then "server_url \(.server_url) differs from the ChatGPT plugin URL \($chatgpt_url); both listings serve one URL" else empty end),
	(if .auth_type != "oauth_cimd" then "auth_type is \(.auth_type), want oauth_cimd: the edge advertises CIMD and no registration endpoint" else empty end),
	(if .callback_url != "https://claude.ai/api/mcp/auth_callback" then "callback_url must be https://claude.ai/api/mcp/auth_callback" else empty end),
	url("documentation_url"), url("privacy_policy_url"),
	(if has("terms_of_service_url") then url("terms_of_service_url") else empty end),
	(if (.support_contact | type) == "string" and (.support_contact | test("^(https://[^@ ]+|[^@ ]+@[^@ ]+\\.[a-z]+)$")) then empty
		else "support_contact must be an https URL or an email address" end),
	(if (.access | IN("read only", "write only", "read and write")) then empty
		else "access must be read only, write only or read and write" end),
	text("prerequisites"; 2000),
	(if (.use_cases | type) != "array" or (.use_cases | length) < 3 then "use_cases needs at least 3 entries" else empty end),
	(if (.example_prompts | type) != "array" or (.example_prompts | length) < 3 then "example_prompts needs at least 3 entries" else empty end),
	(.use_cases[]?, .example_prompts[]? | select(type != "string" or length == 0) | "use_cases and example_prompts entries must be non-empty text"),
	(if (.company | type) != "object" then "company is missing" else (.company | text("name"; 200), url("website"), text("contact"; 320)) end),
	(if (.data_handling | type) != "object" then "data_handling is missing" else .data_handling |
		text("api_ownership"; 2000),
		(.personal_health_data, .sponsored_content | select(type != "boolean") | "data_handling flags must be true or false"),
		(if .financial_transactions != false then "the directory does not accept connectors that move money" else empty end),
		(if .ai_media_generation != false then "the directory does not accept connectors that generate images, video or audio" else empty end) end),
	(if (.compliance_acknowledgments | type) != "array" or (.compliance_acknowledgments | sort) != ($acks | sort)
		then "compliance_acknowledgments must list each of the seven policy acknowledgments once: \($acks | join(", "))" else empty end),
	(.. | objects | keys[] | select(test("credential|password|secret|token|api_?key|username|login|otp|instructions"; "i"))
		| "\(.) must be entered in the portal, not the package")
' "$src/listing.json") || fail "$src/listing.json failed to check; jq's error is above"
[ -z "$problems" ] || fail "listing problems:
$problems"

jq -r 'paths(type == "string") as $p | select(getpath($p) | startswith("PLACEHOLDER:")) | $p | map(tostring) | join(".")' "$src/listing.json" |
	while IFS= read -r field; do
		echo "claude-connector: warning: $field is a placeholder; replace it before submitting (docs/claude-connector.md)" >&2
	done

icon=$root/$(jq -r .icon "$src/listing.json")
[ -f "$icon" ] || fail "icon $icon does not exist"
[ "$(od -An -tx1 -N8 "$icon" | tr -d ' \n')" = 89504e470d0a1a0a ] || fail "icon $icon is not a PNG"
# A PNG's width and height are the big-endian words at bytes 16 and 20.
set -- $(od -An -tu1 -j16 -N8 "$icon")
width=$(($1 * 16777216 + $2 * 65536 + $3 * 256 + $4))
height=$(($5 * 16777216 + $6 * 65536 + $7 * 256 + $8))
[ "$width" -eq "$height" ] || fail "icon is ${width}x$height; it must be square"

if [ -e "$out" ]; then
	[ "$(realpath -- "$out")" != "$(realpath -- "$src")" ] || fail "$out is the package directory; give an empty or new directory"
	extra=$(find "$out" -mindepth 1 ! -name listing.json ! -name icon.png | head -n 1)
	[ -z "$extra" ] || fail "$out holds $extra; give an empty or new directory"
fi
mkdir -p "$out"
jq '.icon = "icon.png"' "$src/listing.json" >"$out/listing.json"
cp "$icon" "$out/icon.png"
echo "wrote $out"
