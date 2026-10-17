#!/bin/sh
# Updates a Compose-deployed Aether edge to the newest signed release on a
# channel, with a database backup first and an automatic rollback when the
# new edge fails its checks. docs/edge.md, "Automatic updates", has the
# install steps, configuration, state files and recovery.
#
#   aether-edge-update                       # update once (the timer runs this)
#   aether-edge-update --check               # verify the running and the new release only
#   aether-edge-update --rollback            # back to the previous release
#   aether-edge-update --recover             # only finish an interrupted run
#
# Exit status: 0 updated, up to date or another run holds the lock; 3 held
# for a manual step; 1 anything else, including a completed rollback.

set -eu

image=ghcr.io/3xdevops/aether-edge
service=aether-edge
backups=/var/lib/aether-edge/backups
promotion_type=https://github.com/3xDevOps/aether/edge-promotion/v1
promoter=https://github.com/3xDevOps/aether/.github/workflows/promote-edge.yml@refs/heads/main

say() {
	printf 'edge-update: %s\n' "$*" >&2
}

die() {
	say "$*"
	exit 1
}

usage() {
	cat >&2 <<'EOF'
usage: aether-edge-update [--check] [--channel <tag>] [--manual-steps-done]
       aether-edge-update --rollback [--restore-backup]
       aether-edge-update --recover
EOF
	exit 2
}

mode=update
manual_steps_done=0
restore_backup=0
channel_flag=""
while [ $# -gt 0 ]; do
	case "$1" in
	--check) mode=check ;;
	--rollback) mode=rollback ;;
	--recover) mode=recover ;;
	--manual-steps-done) manual_steps_done=1 ;;
	--restore-backup) restore_backup=1 ;;
	--channel)
		[ $# -ge 2 ] || usage
		channel_flag="$2"
		shift
		;;
	*) usage ;;
	esac
	shift
done
[ "$restore_backup" = 0 ] || [ "$mode" = rollback ] || usage

config="${AETHER_EDGE_UPDATE_CONFIG:-/etc/aether-edge-update.env}"
# shellcheck disable=SC1090 # the operator's settings
[ ! -f "$config" ] || . "$config"
channel="${channel_flag:-${AETHER_EDGE_UPDATE_CHANNEL:-stable}}"
state_dir="${AETHER_EDGE_UPDATE_STATE:-/var/lib/aether-edge-update}"
compose_dir="${AETHER_EDGE_UPDATE_COMPOSE_DIR:-/opt/aether-edge}"
digest_file="${AETHER_EDGE_UPDATE_DIGEST_FILE:-$compose_dir/.env}"
relay_url="${AETHER_EDGE_UPDATE_RELAY_URL:-}"
tolerance="${AETHER_EDGE_UPDATE_SERVER_TOLERANCE:-10%}"
server_wait="${AETHER_EDGE_UPDATE_SERVER_WAIT:-120}"
public_wait="${AETHER_EDGE_UPDATE_PUBLIC_WAIT:-300}"

for n in "$server_wait" "$public_wait" "${tolerance%\%}"; do
	case "$n" in
	'' | *[!0-9]*) die "AETHER_EDGE_UPDATE_SERVER_WAIT and AETHER_EDGE_UPDATE_PUBLIC_WAIT take whole seconds and AETHER_EDGE_UPDATE_SERVER_TOLERANCE a count or a percentage such as 10%; got '$server_wait', '$public_wait' and '$tolerance'" ;;
	esac
done
[ -n "$relay_url" ] || die "AETHER_EDGE_UPDATE_RELAY_URL is not set; set it in $config to the edge's relay origin, such as https://edge.example.com"

command -v jq >/dev/null || die "jq not found; install it (docs/edge.md, \"Installing the updater\")"

# Exits 0 when release $1 sorts before $2 by SemVer precedence.
version_lt() {
	awk -v a="$1" -v b="$2" '
	function ident(x, y,   xn, yn) {
		xn = (x ~ /^[0-9]+$/); yn = (y ~ /^[0-9]+$/)
		if (xn && yn) return (x + 0 < y + 0) ? -1 : (x + 0 > y + 0)
		if (xn) return -1
		if (yn) return 1
		return (x < y) ? -1 : (x > y)
	}
	function vcmp(a, b,   i, n, m, c, ap, bp, ac, bc, ai, bi) {
		sub(/^v/, "", a); sub(/^v/, "", b); sub(/\+.*/, "", a); sub(/\+.*/, "", b)
		ap = ""; bp = ""
		if ((i = index(a, "-")) > 0) { ap = substr(a, i + 1); a = substr(a, 1, i - 1) }
		if ((i = index(b, "-")) > 0) { bp = substr(b, i + 1); b = substr(b, 1, i - 1) }
		split(a, ac, "."); split(b, bc, ".")
		for (i = 1; i <= 3; i++)
			if (ac[i] + 0 != bc[i] + 0) return (ac[i] + 0 < bc[i] + 0) ? -1 : 1
		if (ap == bp) return 0
		if (ap == "") return 1
		if (bp == "") return -1
		n = split(ap, ai, "."); m = split(bp, bi, ".")
		for (i = 1; i <= n && i <= m; i++)
			if ((c = ident(ai[i], bi[i])) != 0) return c
		return (n < m) ? -1 : (n > m)
	}
	BEGIN { exit !(vcmp(a, b) < 0) }'
}

# Single-quotes $1 for the state file, which this script sources.
quote() {
	printf "'%s'" "$(printf '%s' "$1" | tr '\n' ' ' | sed "s/'/'\\\\''/g")"
}

# Replaces $2 with $1, so that a crash leaves either the old or the new
# file, never a partial one.
replace() {
	sync "$1" && mv "$1" "$2"
}

write_state() {
	state="$1"
	{
		printf 'state=%s\n' "$state"
		printf 'kind=%s\n' "$kind"
		printf 'from_digest=%s\n' "$from_digest"
		printf 'from_version=%s\n' "$(quote "$from_version")"
		printf 'to_digest=%s\n' "$to_digest"
		printf 'to_version=%s\n' "$(quote "$to_version")"
		printf 'fingerprint=%s\n' "$(quote "$fingerprint")"
		printf 'servers=%s\n' "$servers"
		printf 'backup=%s\n' "$(quote "$backup")"
		printf 'restore=%s\n' "$(quote "$restore")"
		printf 'restored=%s\n' "$restored"
		printf 'restore_at=%s\n' "$restore_at"
		printf 'retry=%s\n' "$retry"
		printf 'reason=%s\n' "$(quote "$reason")"
		printf 'since=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$state_dir/state.tmp"
	replace "$state_dir/state.tmp" "$state_dir/state"
}

compose() {
	docker compose --env-file "$digest_file" "$@"
}

is_digest() {
	[ "${#1}" -eq 71 ] || return 1
	case "$1" in
	sha256:*[!0-9a-f]*) return 1 ;;
	sha256:*) return 0 ;;
	esac
	return 1
}

current_digest() {
	d="$(sed -n 's/^AETHER_EDGE_DIGEST=//p' "$digest_file" | tail -n 1)"
	is_digest "$d" ||
		die "no AETHER_EDGE_DIGEST=sha256:<digest> line in $digest_file; write the digest the edge runs there (docs/edge.md, \"Automatic updates\")"
	echo "$d"
}

# Sets data_dir to the host directory Compose binds as the edge's data, so
# that an image reaches it whether or not the edge's container exists.
# Sets why on failure.
resolve_data() {
	[ -z "$data_dir" ] || return 0
	data_dir="$(compose config --format json | jq -er --arg s "$service" \
		'[.services[$s].volumes[]? | select(.type == "bind" and .target == "/var/lib/aether-edge") | .source] | select(length == 1) | .[0]')" && return 0
	data_dir=""
	why="docker compose config shows no host directory bound to /var/lib/aether-edge in $service; bind one, as packaging/edge/compose.yaml does"
	return 1
}

# Runs aether-edge $2... from image digest $1 on the data directory.
run_on_data() {
	d="$1"
	shift
	docker run --rm --network none --user 65532:65532 -v "$data_dir:/var/lib/aether-edge" "$image@$d" "$@"
}

# Exits 1 with the manual first upgrade: image $1 predates the commands
# this updater runs from the running image, and running them from the
# candidate instead would trust what is being verified.
bootstrap() {
	to="${new:-sha256:<digest>}"
	say "$image@$1 predates aether-edge version --json, release-check, backup and schema, which this updater runs from the running image. Upgrade once by hand (docs/edge.md, \"First upgrade\"), in $compose_dir:"
	say "  1. verify $to: cosign verify-attestation --new-bundle-format --type $promotion_type --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity $promoter $image@$to"
	say "  2. back up: docker compose stop $service && cp -a data data.pre-update"
	say "  3. pin it: set AETHER_EDGE_DIGEST=$to in $digest_file"
	say "  4. docker compose up -d --wait $service"
	exit 1
}

# Prints version --json of image $1, or exits through bootstrap.
version_json() {
	if ! out="$(docker run --rm --network none "$image@$1" version --json)" ||
		! printf '%s\n' "$out" | jq -e '(.version | strings) and (.store_compat_floor | numbers)' >/dev/null; then
		bootstrap "$1"
	fi
	printf '%s\n' "$out"
}

# Exits 0 when promote-edge.yml on main attested digest $1 as release $2
# on the channel. The workflow file run from any other ref signs with
# another identity, which cosign refuses.
verify_promotion() {
	atts="$(cosign verify-attestation --new-bundle-format --type "$promotion_type" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		--certificate-identity "$promoter" \
		"$image@$1")" || return 1
	printf '%s\n' "$atts" | jq -e --arg c "$channel" --arg t "$2" --arg d "$1" \
		'.payload | @base64d | fromjson | .predicate | select(.channel == $c and .tag == $t and .digest == $d)' >/dev/null && return 0
	say "no promotion attestation of $1 names channel $channel and release $2"
	return 1
}

# Verifies the running digest $1 as an update verifies a new one, so that
# --check vets a digest pinned by hand before its first start. Sets
# from_json.
verify_running() {
	docker pull --quiet "$image@$1" >/dev/null || die "docker pull $image@$1 failed"
	v="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image@$1")" ||
		die "cannot read the version label of $image@$1"
	[ -n "$v" ] || die "$image@$1 has no org.opencontainers.image.version label"
	if [ "$channel" = latest ]; then
		say "latest is not a promoted channel; only the running image's own release-check vouches for $v ($1)"
	else
		verify_promotion "$1" "$v" ||
			die "the running $v ($1) has no valid promotion attestation on $channel from promote-edge.yml on main; do not run it"
	fi
	from_json="$(version_json "$1")" || exit 1
	got="$(printf '%s\n' "$from_json" | jq -r .version)"
	[ "$got" = "$v" ] || die "$image@$1 reports version $got but is labelled $v"
	docker run --rm "$image@$1" release-check --tag "$v" --digest "$1" --from "$v" --json >/dev/null ||
		die "release-check refused the running $v ($1); do not run it"
	say "verified the running $v ($1)"
}

edge_fingerprint() {
	out="$(curl -fsS --max-time 10 -H 'Aether-Edge-Version: 1' "$relay_url/v1/edge")" || return 1
	printf '%s\n' "$out" | jq -er '.fingerprint | strings'
}

# Polls edge_fingerprint for up to public_wait seconds: after a boot the
# proxy may route to the edge only minutes after it is healthy.
public_fingerprint() {
	deadline=$(($(date +%s) + public_wait))
	until edge_fingerprint; do
		[ "$(date +%s)" -lt "$deadline" ] || return 1
		sleep 5
	done
}

claimed_servers() {
	out="$(timeout 30 docker exec "$service" aether-edge healthcheck --ready --json)" || return 1
	printf '%s\n' "$out" | jq -er 'select(.status == "ready") | .servers.claimed | numbers'
}

# Checks that the running edge reports version $1, serves the recorded
# fingerprint and has at least $2 claimed servers. Sets why on failure,
# and retry=1 when only the server count failed.
post_checks() {
	got="$(timeout 30 docker exec "$service" aether-edge version --json | jq -r .version)" || got=""
	if [ "$got" != "$1" ]; then
		why="the container reports version '${got}', expected $1"
		return 1
	fi
	if ! fp="$(public_fingerprint)"; then
		why="GET $relay_url/v1/edge failed for ${public_wait}s"
		return 1
	fi
	if [ "$fp" != "$fingerprint" ]; then
		why="$relay_url/v1/edge serves fingerprint $fp instead of $fingerprint: the edge lost its edge_key or its data mount"
		return 1
	fi
	deadline=$(($(date +%s) + server_wait))
	while :; do
		if ! n="$(claimed_servers)"; then
			why="aether-edge healthcheck --ready fails in $service"
			retry=0
		elif [ "$n" -ge "$2" ]; then
			say "checks passed: $1 ready, fingerprint $fp, $n claimed servers (before: $servers)"
			return 0
		else
			why="$n claimed servers reconnected within ${server_wait}s, expected at least $2"
			retry=1
		fi
		[ "$(date +%s)" -lt "$deadline" ] || return 1
		sleep 5
	done
}

# Exits 0 when image $1 cannot run the database and the backup must be
# restored, 1 when it can, and 2 when that cannot be told, setting why.
db_needs_restore() {
	resolve_data || return 2
	out="$(run_on_data "$1" schema --json)" && schema_rc=0 || schema_rc=$?
	if [ "$schema_rc" = 0 ]; then
		if ! floor="$(printf '%s\n' "$out" | jq -er '.compat_floor | numbers')" ||
			! supported="$(printf '%s\n' "$out" | jq -er '.supported | numbers')"; then
			why="aether-edge schema --json printed $out"
			return 2
		fi
		if [ "$floor" -le "$supported" ]; then
			say "the database (compat floor $floor) runs on a release that supports schema $supported; keeping it"
			return 1
		fi
		say "the database (compat floor $floor) is beyond schema $supported, the newest $1 supports"
		return 0
	fi
	if [ "$schema_rc" != 1 ]; then
		why="docker run $image@$1 schema --json exited $schema_rc"
		return 2
	fi
	say "aether-edge schema cannot read the database"
	return 0
}

# Restores backup $2 when image $1 cannot run the database, or always
# when $3 is 1. Restore refuses while an edge holds edge.lock, so the edge
# is stopped only then. The restore is recorded before it starts: a crash
# or a failure after it replaced the database must still be undone, from
# the edge-before-restore copy it saved from restore_at on. Sets why on
# failure.
prepare_db() {
	if [ "$3" != 1 ]; then
		rc=0
		db_needs_restore "$1" || rc=$?
		case "$rc" in
		1) return 0 ;;
		2) return 1 ;;
		esac
	fi
	resolve_data || return 1
	restored=1
	[ -n "$restore_at" ] || restore_at="$(date -u +%Y%m%dT%H%M%S)"
	write_state "$state"
	say "restoring $2"
	if ! compose stop "$service"; then
		why="docker compose stop $service failed"
		return 1
	fi
	if ! run_on_data "$1" restore "$2"; then
		why="aether-edge restore $2 failed"
		return 1
	fi
}

# Runs digest $1, release $2, with at least $5 claimed servers. With a
# backup $3, first restores it as prepare_db decides ($4 forces it).
# Sets why on failure.
switch_to() {
	if [ -n "$3" ]; then
		prepare_db "$1" "$3" "$4" || return 1
	fi
	others="$(grep -v '^AETHER_EDGE_DIGEST=' "$digest_file")" && rc=0 || rc=$?
	if [ "$rc" -gt 1 ] || ! {
		[ -z "$others" ] || printf '%s\n' "$others"
		printf 'AETHER_EDGE_DIGEST=%s\n' "$1"
	} >"$digest_file.tmp" || ! replace "$digest_file.tmp" "$digest_file"; then
		why="cannot write $digest_file"
		return 1
	fi
	if ! compose up -d --wait --wait-timeout 180 "$service"; then
		why="docker compose up $service did not become healthy"
		return 1
	fi
	post_checks "$2" "$5"
}

# Sets undo to the oldest database copy, as the edge sees it, that
# aether-edge restore saved from restore_at on, or to nothing when there is
# none. Sets why on failure.
before_restore() {
	resolve_data || return 1
	undo=""
	for f in "$data_dir"/backups/edge-before-restore-*.db; do
		[ -f "$f" ] || continue
		name="${f##*/}"
		if awk -v a="${name#edge-before-restore-}" -v b="$restore_at" 'BEGIN { exit !(a >= b) }'; then
			undo="$backups/$name"
			return 0
		fi
	done
}

deny() {
	! grep -q "^$1 " "$state_dir/failed" 2>/dev/null || return 0
	printf '%s %s %s\n' "$1" "$2" "$(printf '%s' "$3" | tr '\n' ' ')" >>"$state_dir/failed"
}

# Returns to the release before the switch. A database the switch restored
# is put back from the copy aether-edge restore saved of it; when there is
# none, the restore never replaced it. A database the update migrated is
# restored from the pre-update backup when the old release cannot run it.
# The server count is
# not required here: the release ran before, and a server that went away
# meanwhile must not leave the edge broken. With retry=1 the release is
# not denied, and the next run tries it again.
revert() {
	reason="$1"
	say "rolling back to $from_version ($from_digest): $reason"
	write_state rolling-back
	if [ "$retry" != 1 ]; then
		deny "$to_digest" "$to_version" "$reason"
	fi
	undo="$backup"
	[ "$kind" != rollback ] || undo=""
	if [ "$kind" = rollback ] && [ "$restored" = 1 ] && ! before_restore ||
		! switch_to "$from_digest" "$from_version" "$undo" "$restored" 0; then
		reason="rollback to $from_version failed: $why; after: $reason"
		write_state broken
		die "$reason. State is broken; docs/edge.md, \"Automatic updates\", has the recovery"
	fi
	if [ "$retry" = 1 ]; then
		reason="rolled back from $to_version, which the next run retries: $reason"
		write_state idle
		die "rolled back to $from_version after $to_version failed; the next run retries $to_digest"
	fi
	reason=""
	write_state idle
	die "rolled back to $from_version after $to_version failed; $to_digest is in $state_dir/failed"
}

succeed() {
	if [ "$kind" = rollback ]; then
		deny "$from_digest" "$from_version" "rolled back by an operator"
		rm -f "$state_dir/previous"
	else
		printf '%s %s %s\n' "$from_digest" "$from_version" "$backup" >"$state_dir/previous.tmp"
		replace "$state_dir/previous.tmp" "$state_dir/previous"
	fi
	printf '%s %s\n' "$to_digest" "$to_version" >"$state_dir/current.tmp"
	replace "$state_dir/current.tmp" "$state_dir/current"
	reason=""
	write_state idle
	say "now running $to_version ($to_digest)"
}

# Finishes the switch recorded in the state file, or rolls it back. A
# tolerance percentage rounds up, so 10% lets one of a few servers stay
# away.
finish() {
	retry=0
	case "$tolerance" in
	*%) away=$(((servers * ${tolerance%\%} + 99) / 100)) ;;
	*) away="$tolerance" ;;
	esac
	if switch_to "$to_digest" "$to_version" "$restore" 0 $((servers - away)); then
		succeed
	else
		revert "$why"
	fi
}

# Records the fingerprint and claimed servers of the running digest $2 and
# backs up its database as $1. A rollback works on an edge that is down:
# it falls back to the fingerprint of the last update and to a backup from
# a stopped container.
record_pre_state() {
	if [ "$mode" != rollback ] && fp="$(public_fingerprint)"; then
		fingerprint="$fp"
	elif [ "$mode" = rollback ] && fp="$(edge_fingerprint)"; then
		fingerprint="$fp"
	elif [ "$mode" = rollback ] && [ -n "$fingerprint" ]; then
		say "GET $relay_url/v1/edge failed; the rolled-back edge must serve $fingerprint, recorded by the last update"
	else
		die "GET $relay_url/v1/edge failed; the running edge must answer before an update"
	fi
	if n="$(claimed_servers)"; then
		servers="$n"
	elif [ "$mode" = rollback ]; then
		servers=0
	else
		die "aether-edge healthcheck --ready --json failed in $service; the running edge must be ready before an update"
	fi
	backup="$backups/$1-$(date -u +%Y%m%dT%H%M%SZ).db"
	if ! timeout 300 docker exec "$service" aether-edge backup "$backup"; then
		[ "$mode" = rollback ] || die "aether-edge backup $backup failed; nothing changed"
		resolve_data || die "$why; nothing changed"
		run_on_data "$2" backup "$backup" || die "aether-edge backup $backup failed; nothing changed"
	fi
	say "backed up the database to $backup; fingerprint $fingerprint, $servers claimed servers"
}

update() {
	if [ "$channel" != latest ]; then
		command -v cosign >/dev/null ||
			die "cosign not found; install it to verify that $channel was promoted (docs/edge.md, \"Installing the updater\")"
	fi
	cur="$(current_digest)"
	docker pull --quiet "$image:$channel" >/dev/null || die "docker pull $image:$channel failed"
	new="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image:$channel" | sed -n "s|^$image@||p")"
	is_digest "$new" || die "$image:$channel has no single repository digest: '${new}'"
	[ "$mode" != check ] || verify_running "$cur"
	if [ "$new" = "$cur" ]; then
		say "up to date: $channel is $cur"
		exit 0
	fi
	if failed="$(grep "^$new " "$state_dir/failed" 2>/dev/null)"; then
		say "skipping $new, which failed before: ${failed#"$new" }; delete its line from $state_dir/failed to retry it"
		exit 0
	fi
	new_version="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image@$new")" ||
		die "cannot read the version label of $image@$new"
	[ -n "$new_version" ] || die "$image@$new has no org.opencontainers.image.version label"
	[ -n "$from_json" ] || from_json="$(version_json "$cur")" || exit 1
	cur_version="$(printf '%s\n' "$from_json" | jq -r .version)"
	version_lt "$cur_version" "$new_version" ||
		die "refusing $new_version ($new) on $channel: not newer than the running $cur_version"
	if [ "$channel" = latest ]; then
		say "latest is not a promoted channel; trusting the release signature alone"
	else
		verify_promotion "$new" "$new_version" ||
			die "$new_version ($new) on $channel has no valid promotion attestation from promote-edge.yml on main; nothing changed"
	fi

	report="$(docker run --rm "$image@$cur" release-check --tag "$new_version" --digest "$new" --from "$cur_version" --json)" && rc=0 || rc=$?
	case "$rc" in
	0) ;;
	2) bootstrap "$cur" ;;
	3)
		reasons="$(printf '%s\n' "$report" | jq -er '.reasons | join("; ")')" ||
			die "release-check held $new_version but printed no reasons: $report"
		steps_only=0
		! printf '%s\n' "$report" | jq -e '(.reasons | length) == ([.applicable_steps[] | select(.blocking)] | length)' >/dev/null ||
			steps_only=1
		if [ "$manual_steps_done" = 1 ] && [ "$held_digest" = "$new" ]; then
			[ "$steps_only" = 1 ] ||
				die "--manual-steps-done passes only blocking manual steps, and release-check holds $new_version for more: $reasons"
			say "release-check holds $new_version for manual steps; continuing because of --manual-steps-done"
		else
			[ "$manual_steps_done" = 0 ] ||
				say "--manual-steps-done applies only to a held release, and $new_version ($new) was not held before"
			if [ "$mode" = update ]; then
				kind=update from_digest="$cur" from_version="$cur_version" to_digest="$new" to_version="$new_version"
				reason="release-check holds $new_version: $reasons"
				if [ "$steps_only" = 1 ]; then
					reason="$reason; do the manual steps, then run aether-edge-update --manual-steps-done"
				fi
				write_state held
			fi
			say "held: $new_version ($new): $reasons"
			exit 3
		fi
		;;
	*) die "release-check refused $new_version ($new); nothing changed" ;;
	esac

	if [ "$mode" = check ]; then
		say "would update $cur_version ($cur) to $new_version ($new)"
		exit 0
	fi

	to_json="$(docker run --rm --network none "$image@$new" version --json)" || die "aether-edge version --json failed in $image@$new"
	got="$(printf '%s\n' "$to_json" | jq -r .version)"
	[ "$got" = "$new_version" ] || die "$image@$new reports version $got but is labelled $new_version"
	printf '%s\n' "$from_json" >"$state_dir/from.json"
	printf '%s\n' "$to_json" >"$state_dir/to.json"

	record_pre_state "pre-update-$new_version" "$cur"
	kind=update from_digest="$cur" from_version="$cur_version" to_digest="$new" to_version="$new_version"
	restore="" restored=0 restore_at="" retry=0 reason=""
	write_state switching
	say "updating $cur_version ($cur) to $new_version ($new)"
	finish
}

# Exits 1 without a change: rolling back to $prev_version needs the
# pre-update backup, and restoring it loses everything written since.
refuse_restore() {
	host_backup="$data_dir/${prev_backup#/var/lib/aether-edge/}"
	taken="$(stat -c %Y "$host_backup")" ||
		die "$prev_version cannot run the database, and its pre-update backup $host_backup is missing; nothing changed"
	die "$prev_version cannot run the database $cur_version migrated, so this rollback restores $prev_backup, taken $(date -u -d "@$taken" +%Y-%m-%dT%H:%M:%SZ), $((($(date +%s) - taken) / 86400)) days ago. Every account, device, sign-in and server change since then is lost. Run aether-edge-update --rollback --restore-backup to roll back anyway; nothing changed"
}

rollback() {
	[ -s "$state_dir/previous" ] || die "no previous release recorded in $state_dir/previous"
	read -r prev_digest prev_version prev_backup <"$state_dir/previous"
	cur="$(current_digest)"
	cur_json="$(version_json "$cur")" || exit 1
	cur_version="$(printf '%s\n' "$cur_json" | jq -r .version)"
	rc=0
	db_needs_restore "$prev_digest" || rc=$?
	case "$rc" in
	0) [ "$restore_backup" = 1 ] || refuse_restore ;;
	1) prev_backup="" ;;
	2) die "$why; nothing changed" ;;
	esac
	record_pre_state "pre-rollback-$cur_version" "$cur"
	kind=rollback from_digest="$cur" from_version="$cur_version" to_digest="$prev_digest" to_version="$prev_version"
	restore="$prev_backup" restored=0 restore_at="" retry=0 reason=""
	write_state switching
	say "rolling back $cur_version ($cur) to $prev_version ($prev_digest)"
	finish
}

# systemd ends a run that outlives TimeoutStartSec with SIGTERM. A switch
# it interrupts becomes a rollback, which the unit's ExecStopPost= runs
# with --recover.
interrupted() {
	trap - TERM INT
	if [ "$state" = switching ]; then
		retry=1
		reason="stopped by a signal during the switch to $to_version"
		write_state rolling-back
		die "$reason; aether-edge-update --recover rolls it back"
	fi
	die "stopped by a signal in state $state"
}

mkdir -p "$state_dir"
chmod 0700 "$state_dir"
exec 9>"$state_dir/lock"
if ! flock -n 9; then
	say "another run holds $state_dir/lock; exiting"
	[ "$mode" != rollback ] && [ "$manual_steps_done" = 0 ] || exit 1
	exit 0
fi
cd "$compose_dir"

state=idle kind=update from_digest="" from_version="" to_digest="" to_version=""
fingerprint="" servers=0 backup="" restore="" restored=0 restore_at="" retry=0 reason="" since=""
data_dir="" new="" from_json=""
if [ -e "$state_dir/state" ]; then
	state=""
	[ -r "$state_dir/state" ] || die "cannot read $state_dir/state"
	# shellcheck disable=SC1091 # written by write_state
	. "$state_dir/state"
	case "$state" in
	idle | held | switching | rolling-back | broken) ;;
	*) die "$state_dir/state is empty or has no valid state= line, so the state of the edge is unknown. Check the edge (docs/edge.md, \"Automatic updates\"), then delete the file" ;;
	esac
fi
trap interrupted TERM INT

case "$state" in
broken) die "state is broken since $since: $reason. Fix the edge, then delete $state_dir/state (docs/edge.md, \"Automatic updates\")" ;;
switching | rolling-back)
	if [ "$mode" = check ]; then
		say "an interrupted run ($state to $to_version) will be finished or rolled back first"
	elif [ "$state" = switching ]; then
		say "finishing the interrupted switch to $to_version ($to_digest)"
		finish
		[ "$mode" = rollback ] || exit 0
	else
		revert "$reason"
	fi
	;;
esac

held_digest=""
[ "$state" != held ] || held_digest="$to_digest"

case "$mode" in
rollback) rollback ;;
recover) say "no interrupted run to recover" ;;
*) update ;;
esac
