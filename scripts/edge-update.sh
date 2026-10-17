#!/bin/sh
# Updates a Compose-deployed Aether edge to the newest signed release on a
# channel, with a database backup first and an automatic rollback when the
# new edge fails its checks. docs/edge.md, "Automatic updates", has the
# install steps, configuration, state files and recovery.
#
#   aether-edge-update                       # update once (the timer runs this)
#   aether-edge-update --check               # resolve and verify only
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
	echo "edge-update: $*" >&2
}

die() {
	say "$*"
	exit 1
}

usage() {
	cat >&2 <<'EOF'
usage: aether-edge-update [--check] [--channel <tag>] [--manual-steps-done]
       aether-edge-update --rollback | --recover
EOF
	exit 2
}

mode=update
manual_steps_done=0
channel_flag=""
while [ $# -gt 0 ]; do
	case "$1" in
	--check) mode=check ;;
	--rollback) mode=rollback ;;
	--recover) mode=recover ;;
	--manual-steps-done) manual_steps_done=1 ;;
	--channel)
		[ $# -ge 2 ] || usage
		channel_flag="$2"
		shift
		;;
	*) usage ;;
	esac
	shift
done

config="${AETHER_EDGE_UPDATE_CONFIG:-/etc/aether-edge-update.env}"
# shellcheck disable=SC1090 # the operator's settings
[ ! -f "$config" ] || . "$config"
channel="${channel_flag:-${AETHER_EDGE_UPDATE_CHANNEL:-stable}}"
state_dir="${AETHER_EDGE_UPDATE_STATE:-/var/lib/aether-edge-update}"
compose_dir="${AETHER_EDGE_UPDATE_COMPOSE_DIR:-/opt/aether-edge}"
digest_file="${AETHER_EDGE_UPDATE_DIGEST_FILE:-$compose_dir/.env}"
relay_url="${AETHER_EDGE_UPDATE_RELAY_URL:-}"
tolerance="${AETHER_EDGE_UPDATE_SERVER_TOLERANCE:-0}"
server_wait="${AETHER_EDGE_UPDATE_SERVER_WAIT:-120}"

for v in tolerance server_wait; do
	eval "n=\$$v"
	case "$n" in
	'' | *[!0-9]*) die "$v must be a whole number, not '$n' (AETHER_EDGE_UPDATE_SERVER_TOLERANCE, AETHER_EDGE_UPDATE_SERVER_WAIT)" ;;
	esac
done
[ -n "$relay_url" ] || die "AETHER_EDGE_UPDATE_RELAY_URL is not set; set it in $config to the edge's relay origin, such as https://edge.example.com"

# Reads one field of the compact JSON the edge prints. Nested objects are
# flattened, so the first match wins: top-level fields come first.
json_get() {
	tr ',{}' '\n' | sed -n "s/^[[:space:]]*\"$1\"[[:space:]]*:[[:space:]]*//p" | head -n 1 | tr -d '"'
}

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

write_state() {
	state="$1"
	{
		echo "state=$state"
		echo "kind=$kind"
		echo "from_digest=$from_digest"
		echo "from_version=$(quote "$from_version")"
		echo "to_digest=$to_digest"
		echo "to_version=$(quote "$to_version")"
		echo "fingerprint=$(quote "$fingerprint")"
		echo "servers=$servers"
		echo "backup=$(quote "$backup")"
		echo "restore=$(quote "$restore")"
		echo "restored=$restored"
		echo "reason=$(quote "$reason")"
		echo "since=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$state_dir/state.tmp"
	mv "$state_dir/state.tmp" "$state_dir/state"
}

compose() {
	docker compose --env-file "$digest_file" "$@"
}

is_digest() {
	printf '%s\n' "$1" | grep -Eqx 'sha256:[0-9a-f]{64}'
}

current_digest() {
	d="$(sed -n 's/^AETHER_EDGE_DIGEST=//p' "$digest_file" | tail -n 1)"
	is_digest "$d" ||
		die "no AETHER_EDGE_DIGEST=sha256:<digest> line in $digest_file; write the digest the edge runs there (docs/edge.md, \"Automatic updates\")"
	echo "$d"
}

write_digest() {
	others="$(grep -v '^AETHER_EDGE_DIGEST=' "$digest_file")" || [ $? = 1 ] || return 1
	{
		[ -z "$others" ] || printf '%s\n' "$others"
		echo "AETHER_EDGE_DIGEST=$1"
	} >"$digest_file.tmp" && mv "$digest_file.tmp" "$digest_file"
}

# Exits 0 when promote-edge.yml on main attested digest $1 as release $2
# on the channel. The workflow file run from any other ref signs with
# another identity, which cosign refuses.
verify_promotion() {
	atts="$(cosign verify-attestation --type "$promotion_type" \
		--certificate-identity "$promoter" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		--certificate-github-workflow-ref refs/heads/main \
		"$image@$1")" || return 1
	printf '%s\n' "$atts" | while IFS= read -r line; do
		printf '%s\n' "$line" | sed -n 's/.*"payload":"\([^"]*\)".*/\1/p' | base64 -d |
			sed -n 's/.*"predicate":{\([^}]*\)}.*/{\1}/p'
		echo
	done | {
		while IFS= read -r predicate; do
			[ "$(printf '%s\n' "$predicate" | json_get channel)" = "$channel" ] &&
				[ "$(printf '%s\n' "$predicate" | json_get tag)" = "$2" ] &&
				[ "$(printf '%s\n' "$predicate" | json_get digest)" = "$1" ] &&
				exit 0
		done
		say "no promotion attestation of $1 names channel $channel and release $2"
		exit 1
	}
}

image_version() {
	out="$(docker run --rm --network none "$image@$1" version --json)" || return 1
	printf '%s\n' "$out" | json_get version
}

edge_fingerprint() {
	out="$(curl -fsS --max-time 10 -H 'Aether-Edge-Version: 1' "$relay_url/v1/edge")" || return 1
	fp="$(printf '%s\n' "$out" | json_get fingerprint)"
	[ -n "$fp" ] || return 1
	echo "$fp"
}

claimed_servers() {
	out="$(timeout 30 docker exec "$service" aether-edge healthcheck --ready --json)" || return 1
	n="$(printf '%s\n' "$out" | json_get claimed)"
	case "$n" in
	'' | *[!0-9]*) return 1 ;;
	esac
	echo "$n"
}

# Polls readiness, the public fingerprint and the claimed-server count of
# the running edge, which must report version $1 and have at least $2
# claimed servers. Sets why on failure.
post_checks() {
	got="$(timeout 30 docker exec "$service" aether-edge version --json | json_get version)" || got=""
	if [ "$got" != "$1" ]; then
		why="the container reports version '${got}', expected $1"
		return 1
	fi
	want="$2"
	attempts=$((server_wait / 5))
	[ "$attempts" -ge 1 ] || attempts=1
	i=0
	while :; do
		if ! n="$(claimed_servers)"; then
			why="aether-edge healthcheck --ready fails in $service"
		elif ! fp="$(edge_fingerprint)"; then
			why="GET $relay_url/v1/edge fails"
		elif [ "$fp" != "$fingerprint" ]; then
			why="$relay_url/v1/edge serves fingerprint $fp instead of $fingerprint: the edge lost its edge_key or its data mount"
			return 1
		elif [ "$n" -ge "$want" ]; then
			say "checks passed: $1 ready, fingerprint $fp, $n claimed servers (before: $servers)"
			return 0
		else
			why="$n claimed servers reconnected within ${server_wait}s, expected at least $want"
		fi
		i=$((i + 1))
		[ "$i" -lt "$attempts" ] || return 1
		sleep 5
	done
}

# Restores backup $2 when image $1 cannot run the database, or always
# when $3 is 1. Restore refuses while an edge holds edge.lock, so the edge
# is stopped only then. Sets why on failure.
prepare_db() {
	if [ "$3" != 1 ]; then
		if out="$(docker run --rm --network none --user 65532:65532 --volumes-from "$service" "$image@$1" schema --json)"; then
			floor="$(printf '%s\n' "$out" | json_get compat_floor)"
			supported="$(printf '%s\n' "$out" | json_get supported)"
			case "$floor$supported" in
			'' | *[!0-9]*)
				why="aether-edge schema --json printed $out"
				return 1
				;;
			esac
			if [ "$floor" -le "$supported" ]; then
				say "the database (compat floor $floor) runs on a release that supports schema $supported; keeping it"
				return 0
			fi
			say "the database (compat floor $floor) is beyond schema $supported, the newest this release supports; restoring $2"
		else
			rc=$?
			if [ "$rc" != 1 ]; then
				why="docker run $image@$1 schema --json exited $rc"
				return 1
			fi
			say "aether-edge schema cannot read the database; restoring $2"
		fi
	fi
	if ! compose stop "$service"; then
		why="docker compose stop $service failed"
		return 1
	fi
	if ! docker run --rm --network none --user 65532:65532 --volumes-from "$service" "$image@$1" restore "$2"; then
		why="aether-edge restore $2 failed"
		return 1
	fi
	restored=1
	write_state "$state"
}

# Runs digest $1, release $2, with at least $5 claimed servers. With a
# backup $3, first restores it as prepare_db decides ($4 forces it).
# Sets why on failure.
switch_to() {
	if [ -n "$3" ]; then
		prepare_db "$1" "$3" "$4" || return 1
	fi
	if ! write_digest "$1"; then
		why="cannot write $digest_file"
		return 1
	fi
	if ! compose up -d --wait --wait-timeout 180 "$service"; then
		why="docker compose up $service did not become healthy"
		return 1
	fi
	post_checks "$2" "$5"
}

deny() {
	! grep -q "^$1 " "$state_dir/failed" 2>/dev/null || return 0
	printf '%s %s %s\n' "$1" "$2" "$(printf '%s' "$3" | tr '\n' ' ')" >>"$state_dir/failed"
}

# Returns to the release before the switch. A database the switch restored
# is always put back from the backup taken before it. The server count is
# not required here: the release ran before, and a server that went away
# meanwhile must not leave the edge broken.
revert() {
	reason="$1"
	say "rolling back to $from_version ($from_digest): $reason"
	deny "$to_digest" "$to_version" "$reason"
	write_state rolling-back
	if ! switch_to "$from_digest" "$from_version" "$backup" "$restored" 0; then
		reason="rollback to $from_version failed: $why; after: $reason"
		write_state broken
		die "$reason. State is broken; docs/edge.md, \"Automatic updates\", has the recovery"
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
		echo "$from_digest $from_version $backup" >"$state_dir/previous.tmp"
		mv "$state_dir/previous.tmp" "$state_dir/previous"
	fi
	echo "$to_digest $to_version" >"$state_dir/current.tmp"
	mv "$state_dir/current.tmp" "$state_dir/current"
	reason=""
	write_state idle
	say "now running $to_version ($to_digest)"
}

# Finishes the switch recorded in the state file, or rolls it back.
finish() {
	if switch_to "$to_digest" "$to_version" "$restore" 0 $((servers - tolerance)); then
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
	if fp="$(edge_fingerprint)"; then
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
		if [ "$mode" != rollback ] ||
			! docker run --rm --network none --user 65532:65532 --volumes-from "$service" "$image@$2" backup "$backup"; then
			die "aether-edge backup $backup failed; nothing changed"
		fi
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
	cur_version="$(image_version "$cur")" || die "aether-edge version --json failed in $image@$cur"
	version_lt "$cur_version" "$new_version" ||
		die "refusing $new_version ($new) on $channel: not newer than the running $cur_version"
	if [ "$channel" = latest ]; then
		say "latest is not a promoted channel; trusting the release signature alone"
	else
		verify_promotion "$new" "$new_version" ||
			die "$new_version ($new) on $channel has no valid promotion attestation from promote-edge.yml on main; nothing changed"
	fi

	if docker run --rm "$image@$cur" release-check --tag "$new_version" --digest "$new" --from "$cur_version" --json >/dev/null; then
		:
	else
		rc=$?
		[ "$rc" = 3 ] || die "release-check refused $new_version ($new); nothing changed"
		if [ "$manual_steps_done" = 1 ] && [ "$held_digest" = "$new" ]; then
			say "release-check holds $new_version; continuing because of --manual-steps-done"
		else
			[ "$manual_steps_done" = 0 ] ||
				say "--manual-steps-done applies only to a held release, and $new_version ($new) was not held before"
			if [ "$mode" = update ]; then
				kind=update from_digest="$cur" from_version="$cur_version" to_digest="$new" to_version="$new_version"
				reason="release-check holds $new_version: do the manual steps it printed, then run aether-edge-update --manual-steps-done"
				write_state held
			fi
			say "held: $new_version ($new) needs a manual step; see the release-check output above"
			exit 3
		fi
	fi

	if [ "$mode" = check ]; then
		say "would update $cur_version ($cur) to $new_version ($new)"
		exit 0
	fi

	from_json="$(docker run --rm --network none "$image@$cur" version --json)" || die "aether-edge version --json failed in $image@$cur"
	to_json="$(docker run --rm --network none "$image@$new" version --json)" || die "aether-edge version --json failed in $image@$new"
	got="$(printf '%s\n' "$to_json" | json_get version)"
	[ "$got" = "$new_version" ] || die "$image@$new reports version $got but is labelled $new_version"
	printf '%s\n' "$from_json" >"$state_dir/from.json"
	printf '%s\n' "$to_json" >"$state_dir/to.json"

	record_pre_state "pre-update-$new_version" "$cur"
	kind=update from_digest="$cur" from_version="$cur_version" to_digest="$new" to_version="$new_version"
	restore="" restored=0 reason=""
	write_state switching
	say "updating $cur_version ($cur) to $new_version ($new)"
	finish
}

rollback() {
	[ -s "$state_dir/previous" ] || die "no previous release recorded in $state_dir/previous"
	read -r prev_digest prev_version prev_backup <"$state_dir/previous"
	cur="$(current_digest)"
	cur_version="$(image_version "$cur")" || die "aether-edge version --json failed in $image@$cur"
	record_pre_state "pre-rollback-$cur_version" "$cur"
	kind=rollback from_digest="$cur" from_version="$cur_version" to_digest="$prev_digest" to_version="$prev_version"
	restore="$prev_backup" restored=0 reason=""
	write_state switching
	say "rolling back $cur_version ($cur) to $prev_version ($prev_digest)"
	finish
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
fingerprint="" servers=0 backup="" restore="" restored=0 reason="" since=""
# shellcheck disable=SC1091 # written by write_state
[ ! -f "$state_dir/state" ] || . "$state_dir/state"

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
