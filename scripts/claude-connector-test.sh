#!/bin/sh
# claude-connector.sh stages the shipped listing and refuses one Anthropic's
# submission checks would reject.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
src=$script_dir/../packaging/claude-connector
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sh "$script_dir/claude-connector.sh" "$tmp/out" 2>"$tmp/err"
for file in listing.json icon.png; do
	[ -s "$tmp/out/$file" ] || {
		echo "claude-connector-test: $file was not staged" >&2
		exit 1
	}
done

# refuse <what> <error> <jq edit to listing.json>
refuse() {
	rm -rf "$tmp/pkg"
	mkdir "$tmp/pkg"
	jq "$3" "$src/listing.json" >"$tmp/pkg/listing.json"
	if sh "$script_dir/claude-connector.sh" "$tmp/bad" "$tmp/pkg" 2>"$tmp/err"; then
		echo "claude-connector-test: staged a listing with $1" >&2
		exit 1
	fi
	grep -qF "$2" "$tmp/err" || {
		echo "claude-connector-test: a listing with $1 failed without \"$2\":" >&2
		cat "$tmp/err" >&2
		exit 1
	}
}

refuse "a 101-character name" "name is 101 characters, at most 100" ".name = \"$(printf '%0101d' 0)\""
refuse "a 201-character one-liner" "one_liner is 201 characters, at most 200" ".one_liner = \"$(printf '%0201d' 0)\""
refuse "a 2001-character description" "description is 2001 characters, at most 2000" ".description = \"$(printf '%02001d' 0)\""
refuse "six categories" "categories needs 1 to 5 entries" '.categories = ["a", "b", "c", "d", "e", "f"]'
refuse "no categories" "categories needs 1 to 5 entries" '.categories = []'
refuse "an uppercase slug" "slug must be lowercase" '.slug = "Aether"'
refuse "another server URL" "differs from the ChatGPT plugin URL" '.server_url = "https://edge.onaether.dev/mcp/srv-1"'
refuse "dynamic client registration" "want oauth_cimd" '.auth_type = "oauth_dcr"'
refuse "an http privacy policy" "privacy_policy_url is not an https URL" '.privacy_policy_url = "http://example.com/privacy"'
refuse "two use cases" "use_cases needs at least 3" '.use_cases |= .[:2]'
refuse "six acknowledgments" "seven policy acknowledgments" '.compliance_acknowledgments |= .[:6]'
refuse "a money-moving connector" "does not accept connectors that move money" '.data_handling.financial_transactions = true'
refuse "reviewer credentials" "test_password must be entered in the portal" '.test_password = "x"'
refuse "a missing icon" "does not exist" '.icon = "web/public/icons/missing.png"'
refuse "a string for categories" "categories needs 1 to 5 entries" '.categories = "Developer tools"'
refuse "no company" "company is missing" 'del(.company)'

mkdir "$tmp/full"
touch "$tmp/full/keep"
if sh "$script_dir/claude-connector.sh" "$tmp/full" 2>"$tmp/err" || [ ! -e "$tmp/full/keep" ]; then
	echo "claude-connector-test: wrote into a directory holding other files" >&2
	exit 1
fi
grep -qF "give an empty or new directory" "$tmp/err" || {
	echo "claude-connector-test: a full output directory failed without naming it:" >&2
	cat "$tmp/err" >&2
	exit 1
}
