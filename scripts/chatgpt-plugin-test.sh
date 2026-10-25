#!/bin/sh
# chatgpt-plugin.sh builds the shipped package and refuses one OpenAI's
# submission checks would reject.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
src=$script_dir/../packaging/chatgpt-plugin
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sh "$script_dir/chatgpt-plugin.sh" "$tmp/plugin.zip" 2>"$tmp/err"
listing=$(python3 -m zipfile -l "$tmp/plugin.zip")
for file in plugin.json mcp.json assets/logo.png assets/composer-icon.png; do
	printf '%s\n' "$listing" | grep -q "^$file " || {
		echo "chatgpt-plugin-test: $file is not in the ZIP:" >&2
		printf '%s\n' "$listing" >&2
		exit 1
	}
done

# refuse <what> <error> <jq edit to plugin.json> <jq edit to mcp.json>
refuse() {
	rm -rf "$tmp/pkg"
	mkdir "$tmp/pkg"
	jq "$3" "$src/plugin.json" >"$tmp/pkg/plugin.json"
	jq "$4" "$src/mcp.json" >"$tmp/pkg/mcp.json"
	if sh "$script_dir/chatgpt-plugin.sh" "$tmp/bad.zip" "$tmp/pkg" 2>"$tmp/err"; then
		echo "chatgpt-plugin-test: built a package with $1" >&2
		exit 1
	fi
	grep -qF "$2" "$tmp/err" || {
		echo "chatgpt-plugin-test: a package with $1 failed without \"$2\":" >&2
		cat "$tmp/err" >&2
		exit 1
	}
}

ui='.extensions["com.openai"].interface'
long=$(printf '%031d' 0)
refuse "a 31-character display name" "displayName is 31 characters, at most 30" "$ui.displayName = \"$long\"" .
refuse "a 31-character short description" "shortDescription is 31 characters, at most 30" "$ui.shortDescription = \"$long\"" .
refuse "21 capabilities" "capabilities has 21 entries" "$ui.capabilities = [range(21) | \"c\"]" .
refuse "four starter prompts" "more than 3 prompts" "$ui.defaultPrompt = [\"a\", \"b\", \"c\", \"d\"]" .
refuse "an http privacy policy" "privacyPolicyURL is not an https URL" "$ui.privacyPolicyURL = \"http://example.com/privacy\"" .
refuse "a logo it does not package" "./assets/missing.png is referenced by plugin.json but not packaged" "$ui.logo = \"./assets/missing.png\"" .
refuse "four positive test cases" "exactly 5 positive" '.extensions["com.openai"].review.test_cases.positive |= .[:4]' .
refuse "reviewer credentials" "test_credentials must be entered in the portal" '.extensions["com.openai"].review.test_credentials = "x"' .
refuse "MCP server headers" "must not set headers" . '.mcpServers.aether.headers = {"Authorization": "x"}'
