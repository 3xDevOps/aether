#!/bin/sh
set -eu

old_brand="$(printf 'Ica%s' 'rus')"
old_brand_lower="$(printf 'ica%s' 'rus')"
old_server="$(printf 'ica%s-server' 'rus')"
old_cli_path="$(printf 'cmd/%s' "$old_brand_lower")"
old_server_path="$(printf 'cmd/%s' "$old_server")"
old_unit_path="$(printf '%s.service' "$old_server")"
old_module="github.com/3xDevOps/${old_brand}"
old_operator="$(printf 'ica%s-operator' 'rus')"
plan_dir_name="$(printf 'super%s' 'powers')"
private_plan_dir="$(printf '%s/%s' 'docs' "$plan_dir_name")"
linear_host="$(printf '%s.%s/%s' 'linear' 'app' 'supersuccessfulstartup')"
private_path_one="$(printf '%s/%s' '/home' 'varram')"
private_path_two="$(printf '%s/%s' '/home' 'huang')"
issue_prefix="$(printf 'SU%s' 'P-')"
provider_prefix="$(printf '%s%s' 'sk-' 'ant-')"
provider_pattern="$(printf '%s%s' 'api' '03-')"
invite="$(printf '%s%s' '7f3c1a9e0b2d4f6a8c5e1b7d9a0f' '2c4e')"
failed=0

check_fixed() {
	needle=$1
	if matches=$(git grep --untracked -n -I -F -- "$needle" -- . ':(exclude)scripts/public-audit.sh'); then
		printf '%s\n' "$matches"
		failed=1
	fi
}

check_regex() {
	pattern=$1
	if matches=$(git grep --untracked -n -I -E -- "$pattern" -- . ':(exclude)scripts/public-audit.sh'); then
		printf '%s\n' "$matches"
		failed=1
	fi
}

check_fixed "$old_brand"
check_fixed "$old_brand_lower"
check_fixed "$old_server"
check_fixed "$old_module"
check_fixed "$old_operator"
check_fixed "$linear_host"
check_fixed "$plan_dir_name"
check_fixed "$private_path_one"
check_fixed "$private_path_two"
check_fixed '$25'
check_fixed '$150'
check_fixed '$20k'
check_fixed "$provider_prefix"
check_fixed "$provider_pattern"
check_fixed "$invite"
check_regex "${issue_prefix}[0-9]+"

legacy_path_pattern="(^|/)${private_plan_dir}(/|$)|(^|/)${old_cli_path}(/|$)|(^|/)${old_server_path}(/|$)|(^|/)${old_unit_path}$|(^|/)aether\\.db(-wal|-shm)?$|(^|/)aether-data/"
if paths=$(git ls-files --cached --others --exclude-standard | grep -E "$legacy_path_pattern"); then
	printf '%s\n' "$paths"
	failed=1
fi

# The product is named Cloudfield. Each line below still says the old name
# on purpose and goes when what it names is renamed: the connector entry a
# member adds in ChatGPT or Claude, the Slack bot and the desktop app, the
# server's wake prompt, the symbol fonts, and two listing images not yet
# shot again.
old_name_kept='Name it <strong>Aether</strong>
Install Aether from the
Add Aether again in
Connect Aether again
the Aether bot
or quit Aether.
/^Aether has
Aether Symbols
Aether Nerd Symbols
The Aether mark and wordmark
The Aether board on a phone'
if matches=$(git grep --untracked -n -I -w -e Aether -- \
	'web/src' 'web/public/sw.js' 'internal/edge/service/templates' \
	'packaging/claude-connector' 'packaging/chatgpt-plugin' 'android/listing' \
	':(exclude)web/src/**/*.test.ts' ':(exclude)web/src/**/*.test.tsx' |
	grep -v -E '^[^:]+:[0-9]+:[[:space:]]*(//|/\*|\*)' |
	grep -v -F -e "$old_name_kept"); then
	printf '%s\n' "$matches"
	failed=1
fi

if [ "$failed" -ne 0 ]; then
	echo 'public audit: FAIL' >&2
	exit 1
fi

echo 'public audit: PASS'
