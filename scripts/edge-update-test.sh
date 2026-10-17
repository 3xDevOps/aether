#!/bin/sh
# Exercises scripts/edge-update.sh against stubbed docker, curl, cosign,
# flock, sleep and date, the same style as deploy-test.sh. The docker stub keeps
# a small world in files: which digest runs, what the channel tag points
# to, the database and its backups, and which releases fail. A real edge
# is not involved.
set -eu

script_dir=$(CDPATH="" cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/edge-update.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

bin="$tmp/bin"
log="$tmp/cmd.log"
world="$tmp/world"
mkdir -p "$bin"

image=ghcr.io/3xdevops/aether-edge
A=sha256:$(printf '%064d' 0 | tr 0 a)
B=sha256:$(printf '%064d' 0 | tr 0 b)
C=sha256:$(printf '%064d' 0 | tr 0 c)
D=sha256:$(printf '%064d' 0 | tr 0 d)

# World files, per digest under $WORLD/<name>/<digest>:
#   version     the release the image is (label and version --json)
#   label       a version label that differs from version
#   reports     a version the running container reports instead
#   supports    the highest database schema the image supports
#   floor       the compat floor its migrations leave in the database
#   legacy      the image predates version --json and release-check
#   no-release-check  the image has version --json but not release-check
#   up-fails    docker compose up fails for it
#   gone-after-up  its container is removed right after it starts
#   term-on-up  docker compose up sends SIGTERM to the updater once
#   unready     healthcheck --ready fails in it
#   claimed     claimed servers while it runs (default 3)
#   slow        readiness reports 0 claimed servers this many times first
#   fingerprint served on /v1/edge while it runs (default SHA256:good)
# and $WORLD/channel, $WORLD/running, $WORLD/db ("schema floor data"),
# $WORLD/data/backups/<name>, $WORLD/release-check ("<exit> [min]"),
# $WORLD/unrouted (how many /v1/edge requests the proxy answers 404
# first), $WORLD/extra-digest (a second repository digest), $WORLD/reasons
# (the JSON reasons of a min_upgrade_from hold), $WORLD/clock (the seconds
# date +%s prints and sleep advances), and the switches backup-fails,
# restore-fails, schema-fails, gone (no container until the next up),
# hang (a refused /v1/edge request takes curl's 10 s timeout),
# write-on-stop (the edge writes "last" as it stops), and the one-shot
# restore-fails-after (restore fails after replacing the database) and
# term-on-restore (SIGTERM to the updater once restore replaced it). The JSON is what aether-edge prints:
# version and schema --json (#453), healthcheck --ready --json (#455) and
# release-check --json (#454).
cat >"$bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf 'docker %s\n' "$*" >>"$CMD_LOG"
w="$WORLD"
get() { cat "$w/$1/$2" 2>/dev/null || echo "${3:-}"; }
backup() {
	[ ! -f "$w/backup-fails" ] || { echo "aether-edge: backup: disk full" >&2; exit 1; }
	cp "$w/db" "$w/data/backups/$(basename "$1")"
}
version_json() {
	printf '{"version":"%s","commit":"abc","store_schema":%s,"store_compat_floor":%s,"protocol":{"version":1,"min_version":1}}\n' \
		"$1" "$(get supports "$2" 1)" "$(get floor "$2" "$(get supports "$2" 1)")"
}
image=ghcr.io/3xdevops/aether-edge
case "$1" in
pull) exit 0 ;;
image)
	d="${5#*@}"
	case "$4" in
	*RepoDigests*)
		echo "$image@$(cat "$w/channel")"
		[ ! -f "$w/extra-digest" ] || echo "$image@$(cat "$w/extra-digest")"
		;;
	*image.version*) get label "$d" "$(get version "$d")" ;;
	esac
	exit 0
	;;
run)
	shift
	while [ $# -gt 0 ]; do
		case "$1" in
		--volumes-from) [ ! -f "$w/gone" ] || { echo "docker: Error response from daemon: No such container: $2." >&2; exit 125; } ;;
		-v) [ "$2" = "$w/data:/var/lib/aether-edge" ] || { echo "docker stub: unexpected mount $2" >&2; exit 2; } ;;
		*@sha256:*) break ;;
		esac
		shift
	done
	d="${1#*@}"
	shift
	if [ -f "$w/legacy/$d" ]; then
		[ "$1" = version ] || { echo "aether-edge: unknown command \"$1\"" >&2; exit 2; }
		echo "aether-edge $(get version "$d") (abc)"
		exit 0
	fi
	case "$1" in
	version) version_json "$(get version "$d")" "$d" ;;
	release-check)
		[ ! -f "$w/no-release-check/$d" ] || { echo "aether-edge: unknown command \"release-check\"" >&2; exit 2; }
		tag="$3" digest="$5" from="$7"
		rc=0 kind=""
		[ ! -f "$w/release-check" ] || read -r rc kind <"$w/release-check"
		from="${from:?release-check requires --from}"
		[ "$rc" != 1 ] || { echo "aether-edge: release $tag signs the edge image sha256:0, not $digest; do not run it" >&2; exit 1; }
		steps='[]' reasons='[]' hold=false reason=""
		if [ "$rc" = 3 ] && [ "$kind" = min ]; then
			reason="$tag upgrades only from v1.0.5 or later; upgrade $from to v1.0.5 first"
			reasons="[\"$reason\"]" hold=true
			[ ! -f "$w/reasons" ] || reasons="$(cat "$w/reasons")"
		elif [ "$rc" = 3 ]; then
			reason="manual step rotate-key (since v1.1.0) must be done first: rotate the edge key; see https://example.com/rotate"
			steps='[{"id":"rotate-key","since":"v1.1.0","summary":"rotate the edge key","doc_url":"https://example.com/rotate","blocking":true}]'
			reasons="[\"$reason\"]" hold=true
		fi
		printf '{"version":"%s","commit":"abc","edge":{"image":"%s","image_digest":"%s","store_schema":1,"store_compat_floor":1,"protocol":{"version":1,"min_version":1},"min_upgrade_from":"v0.5.2-alpha.4","manual_steps":%s},"applicable_steps":%s,"hold":%s,"reasons":%s}\n' \
			"$tag" "$image" "$digest" "$steps" "$steps" "$hold" "$reasons"
		[ "$hold" = false ] || { echo "aether-edge: hold: $reason" >&2; exit 3; }
		;;
	schema)
		[ ! -f "$w/schema-fails" ] || { echo "aether-edge: edge.db: file is not a database" >&2; exit 1; }
		read -r schema floor data <"$w/db"
		printf '{"schema":%s,"compat_floor":%s,"supported":%s}\n' "$schema" "$floor" "$(get supports "$d" 1)"
		;;
	backup) backup "$2" ;;
	restore)
		[ ! -f "$w/restore-fails" ] || { echo "aether-edge: restore: disk full" >&2; exit 1; }
		[ -z "$(cat "$w/running")" ] || { echo "aether-edge: another aether-edge process holds the lock; an edge is running on /var/lib/aether-edge" >&2; exit 1; }
		n=$(($(cat "$w/restores" 2>/dev/null || echo 0) + 1))
		echo "$n" >"$w/restores"
		cp "$w/db" "$w/data/backups/edge-before-restore-$(date -u +%Y%m%dT%H%M%S).$(printf '%06d' "$n")Z.db"
		cp "$w/data/backups/$(basename "$2")" "$w/db"
		if [ -f "$w/restore-fails-after" ]; then
			rm "$w/restore-fails-after"
			echo "aether-edge: sync /var/lib/aether-edge: input/output error" >&2
			exit 1
		fi
		if [ -f "$w/term-on-restore" ]; then
			rm "$w/term-on-restore"
			kill -TERM "$PPID"
		fi
		;;
	esac
	exit 0
	;;
exec)
	[ ! -f "$w/gone" ] || { echo "Error response from daemon: No such container: aether-edge" >&2; exit 1; }
	running="$(cat "$w/running")"
	[ -n "$running" ] || { echo "Error response from daemon: container is not running" >&2; exit 1; }
	case "$3 $4" in
	"aether-edge backup") backup "$5" ;;
	"aether-edge version") version_json "$(get reports "$running" "$(get version "$running")")" "$running" ;;
	"aether-edge healthcheck")
		claimed="$(get claimed "$running" 3)"
		slow="$(get slow "$running" 0)"
		if [ "$slow" -gt 0 ]; then
			echo $((slow - 1)) >"$w/slow/$running"
			claimed=0
		fi
		status=ready db=ok
		[ ! -f "$w/unready/$running" ] || status=unready db="database is locked"
		printf '{"status":"%s","version":"%s","protocol":{"version":1,"min_version":1},"checks":{"db":"%s","listener":"ok","draining":false},"servers":{"claimed":%s,"unclaimed":0}}\n' \
			"$status" "$(get version "$running")" "$db" "$claimed"
		[ "$status" = ready ] || { echo "aether-edge: healthcheck: edge is not ready (unready): db: $db" >&2; exit 1; }
		;;
	esac
	exit 0
	;;
compose)
	env_file="$3"
	case "$4" in
	config)
		printf '{"name":"edge","services":{"aether-edge":{"volumes":[{"type":"bind","source":"%s","target":"/var/lib/aether-edge","bind":{}}]}}}\n' "$w/data"
		;;
	stop)
		if [ -f "$w/write-on-stop" ]; then
			read -r schema floor _ <"$w/db"
			echo "$schema $floor last" >"$w/db"
		fi
		: >"$w/running"
		;;
	up)
		d="$(sed -n 's/^AETHER_EDGE_DIGEST=//p' "$env_file")"
		if [ -f "$w/term-on-up/$d" ]; then
			rm "$w/term-on-up/$d"
			kill -TERM "$PPID"
		fi
		[ ! -f "$w/up-fails/$d" ] || exit 1
		rm -f "$w/gone"
		: >"$w/running"
		read -r schema floor data <"$w/db"
		supports="$(get supports "$d" 1)"
		if [ "$supports" -gt "$schema" ]; then
			echo "$supports $(get floor "$d" "$supports") $data" >"$w/db"
		elif [ "$floor" -gt "$supports" ]; then
			exit 1
		fi
		echo "$d" >"$w/running"
		[ ! -f "$w/gone-after-up/$d" ] || touch "$w/gone"
		;;
	esac
	exit 0
	;;
esac
echo "docker stub: unexpected $*" >&2
exit 2
EOF

cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
printf 'curl %s\n' "$*" >>"$CMD_LOG"
unrouted="$(cat "$WORLD/unrouted" 2>/dev/null || echo 0)"
if [ "$unrouted" -gt 0 ]; then
	echo $((unrouted - 1)) >"$WORLD/unrouted"
	[ ! -f "$WORLD/hang" ] || echo $(($(cat "$WORLD/clock") + 10)) >"$WORLD/clock"
	echo "curl: (22) The requested URL returned error: 404" >&2
	exit 22
fi
running="$(cat "$WORLD/running")"
if [ -z "$running" ] || [ -f "$WORLD/gone" ]; then
	echo "curl: (22) The requested URL returned error: 502" >&2
	exit 22
fi
fp="$(cat "$WORLD/fingerprint/$running" 2>/dev/null || echo SHA256:good)"
printf '{"signin_origin":"https://auth.example.com","key":"MCowBQYDK2VwAyEA","fingerprint":"%s","version":1,"min_version":1}\n' "$fp"
EOF

# Answers with one attestation whose predicate promotes the digest on
# stable as its version, or as $WORLD/promotion/<digest> says ("<channel>
# <tag> [<digest>]", or "none" for no attestation). Like cosign, it refuses
# when the signer, $WORLD/signer if set, is not the identity asked for.
cat >"$bin/cosign" <<'EOF'
#!/bin/sh
set -eu
printf 'cosign %s\n' "$*" >>"$CMD_LOG"
signer="$(cat "$WORLD/signer" 2>/dev/null || echo https://github.com/3xDevOps/aether/.github/workflows/promote-edge.yml@refs/heads/main)"
while [ $# -gt 1 ]; do
	case "$1" in
	--certificate-identity) [ "$2" = "$signer" ] || { echo "error: expected SAN value \"$2\", got \"$signer\"" >&2; exit 1; } ;;
	esac
	shift
done
d="${1#*@}"
promotion="$(cat "$WORLD/promotion/$d" 2>/dev/null || echo "stable $(cat "$WORLD/version/$d")")"
[ "$promotion" != none ] || { echo "Error: no matching attestations:" >&2; exit 1; }
set -- $promotion
channel="$1" tag="$2" subject="${3:-$d}"
statement="$(printf '{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"ghcr.io/3xdevops/aether-edge","digest":{"sha256":"%s"}}],"predicateType":"https://github.com/3xDevOps/aether/edge-promotion/v1","predicate":{"channel":"%s","tag":"%s","digest":"%s"}}' "${d#sha256:}" "$channel" "$tag" "$subject")"
printf '{"payloadType":"application/vnd.in-toto+json","payload":"%s","signatures":[{"sig":"x"}]}\n' "$(printf '%s' "$statement" | base64 -w0)"
EOF

cat >"$bin/flock" <<'EOF'
#!/bin/sh
printf 'flock %s\n' "$*" >>"$CMD_LOG"
[ ! -f "$WORLD/locked" ]
EOF

cat >"$bin/sleep" <<'EOF'
#!/bin/sh
c="$(cat "$WORLD/clock" 2>/dev/null)" || exit 0
echo $((c + $1)) >"$WORLD/clock"
EOF

cat >"$bin/date" <<'EOF'
#!/bin/sh
if [ "$*" = +%s ] && [ -f "$WORLD/clock" ]; then
	cat "$WORLD/clock"
	exit 0
fi
PATH="${PATH#*:}" exec date "$@"
EOF

chmod 0755 "$bin/docker" "$bin/cosign" "$bin/curl" "$bin/flock" "$bin/sleep" "$bin/date"

state="$tmp/state"
compose_dir="$tmp/compose"
mount="-v $world/data:/var/lib/aether-edge"
export CMD_LOG="$log" WORLD="$world" PATH="$bin:$PATH"
export AETHER_EDGE_UPDATE_CONFIG="$tmp/none" AETHER_EDGE_UPDATE_STATE="$state" AETHER_EDGE_UPDATE_COMPOSE_DIR="$compose_dir"
export AETHER_EDGE_UPDATE_RELAY_URL=https://edge.example.com AETHER_EDGE_UPDATE_SERVER_WAIT=15 AETHER_EDGE_UPDATE_PUBLIC_WAIT=30

fail() {
	echo "edge-update-test: $*" >&2
	echo "--- command log ---" >&2
	cat "$log" >&2 2>/dev/null || true
	echo "--- output ---" >&2
	cat "$tmp/out" >&2 2>/dev/null || true
	exit 1
}

assert() {
	grep -Fq -- "$1" "$log" || fail "missing command: $1"
}

refute() {
	! grep -Fq -- "$1" "$log" || fail "unexpected command: $1"
}

# assert_before <first> <second>: both ran, the first one earlier.
assert_before() {
	a="$(grep -Fn -- "$1" "$log" | head -n 1 | cut -d: -f1)"
	b="$(grep -Fn -- "$2" "$log" | tail -n 1 | cut -d: -f1)"
	if [ -z "$a" ] || [ -z "$b" ] || [ "$a" -ge "$b" ]; then
		fail "expected '$1' before '$2'"
	fi
}

assert_out() {
	grep -Fq -- "$1" "$tmp/out" || fail "missing output: $1"
}

assert_digest() {
	grep -qx "AETHER_EDGE_DIGEST=$1" "$compose_dir/.env" || fail "expected $1 in .env, got $(cat "$compose_dir/.env")"
}

assert_state() {
	grep -qx "state=$1" "$state/state" || fail "expected state $1, got $(cat "$state/state" 2>/dev/null)"
}

assert_db() {
	[ "$(cat "$world/db")" = "$1" ] || fail "expected database '$1', got '$(cat "$world/db")'"
}

assert_denied() {
	grep -q "^$1 " "$state/failed" 2>/dev/null || fail "$1 not in failed"
}

refute_denied() {
	! grep -q "^$1 " "$state/failed" 2>/dev/null || fail "$1 in failed"
}

# Fresh world: A (v1.0.0) runs, the channel points to B (v1.1.0), C is
# v0.9.0, D is v1.2.0. Every image supports schema 1; the database is at 1
# and holds "old".
reset() {
	rm -rf "$world" "$state" "$compose_dir"
	for d in version label promotion reports supports floor legacy no-release-check up-fails gone-after-up term-on-up unready claimed slow fingerprint data/backups; do
		mkdir -p "$world/$d"
	done
	mkdir -p "$compose_dir"
	now="$(date +%s)"
	echo "$now" >"$world/clock"
	echo v1.0.0 >"$world/version/$A"
	echo v1.1.0 >"$world/version/$B"
	echo v0.9.0 >"$world/version/$C"
	echo v1.2.0 >"$world/version/$D"
	echo "$B" >"$world/channel"
	echo "$A" >"$world/running"
	echo "1 1 old" >"$world/db"
	printf 'COMPOSE_PROJECT_NAME=edge\nAETHER_EDGE_DIGEST=%s\n' "$A" >"$compose_dir/.env"
	: >"$log"
}

# run <expected exit> [args]: runs the updater, output in $tmp/out.
run() {
	want="$1"
	shift
	set +e
	sh "$script" "$@" >"$tmp/out" 2>&1
	got=$?
	set -e
	[ "$got" = "$want" ] || fail "edge-update $* exited $got, expected $want"
}

# Simulates the edge writing to its database.
write_db() {
	read -r schema floor _ <"$world/db"
	echo "$schema $floor $1" >"$world/db"
}

reset
run 0
assert "docker pull --quiet $image:stable"
assert "docker run --rm $image@$A release-check --tag v1.1.0 --digest $B --from v1.0.0 --json"
assert_before "docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/pre-update-v1.1.0-" "up -d --wait --wait-timeout 180 aether-edge"
assert_before "release-check" "aether-edge backup"
assert "cosign verify-attestation --new-bundle-format --type https://github.com/3xDevOps/aether/edge-promotion/v1 --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity https://github.com/3xDevOps/aether/.github/workflows/promote-edge.yml@refs/heads/main $image@$B"
assert_before "cosign verify-attestation" "release-check"
refute "restore"
refute " stop aether-edge"
assert_digest "$B"
grep -qx COMPOSE_PROJECT_NAME=edge "$compose_dir/.env" || fail "other .env lines were dropped"
assert_state idle
grep -qx "$B v1.1.0" "$state/current" || fail "current not recorded"
grep -q "^$A v1.0.0 /var/lib/aether-edge/backups/pre-update-v1.1.0-[0-9TZ]*\.db$" "$state/previous" || fail "previous not recorded"
[ "$(stat -c %a "$state")" = 700 ] || fail "state directory is not 0700"
grep -q '"store_compat_floor":1' "$state/from.json" || fail "from.json not recorded"
assert_out "now running v1.1.0"

# A manual rollback that needs no restore.
: >"$log"
run 0 --rollback
assert "aether-edge backup /var/lib/aether-edge/backups/pre-rollback-v1.1.0-"
assert "$mount $image@$A schema --json"
refute "restore"
refute "--volumes-from"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 rolled back by an operator" "$state/failed" || fail "rolled-back release not denied"
[ ! -e "$state/previous" ] || fail "previous kept after rollback"
: >"$log"
run 0
assert_out "skipping $B"
refute "aether-edge backup"

# A manual rollback over a migration refuses to restore the old backup
# until the operator passes --restore-backup.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
assert_db "2 2 old"
write_db new
touch -d "@$(($(cat "$world/clock") - 10 * 86400))" "$world"/data/backups/pre-update-v1.1.0-*.db
: >"$log"
run 1 --rollback
assert_out "10 days ago"
assert_out "Every account, device, sign-in and server change since then is lost"
assert_out "--rollback --restore-backup"
refute "restore /var"
refute " stop aether-edge"
refute "aether-edge backup"
assert_db "2 2 new"
assert_digest "$B"
assert_state idle
run 0 --rollback --restore-backup
assert_before " stop aether-edge" "restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_db "1 1 old"
assert_digest "$A"
run 2 --restore-backup

# A failed manual rollback puts back the database the restore replaced,
# with the writes since its pre-rollback backup, not that backup.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
write_db new
touch "$world/unready/$A" "$world/write-on-stop"
run 1 --rollback --restore-backup
assert "restore /var/lib/aether-edge/backups/edge-before-restore-"
refute "restore /var/lib/aether-edge/backups/pre-rollback-"
assert_db "2 2 last"
assert_digest "$B"
assert_state idle

# A restore that fails after replacing the database is undone too.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
write_db new
touch "$world/restore-fails-after"
run 1 --rollback --restore-backup
assert_out "aether-edge restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert "restore /var/lib/aether-edge/backups/edge-before-restore-"
assert_db "2 2 new"
assert_digest "$B"
assert_state idle

# So is one a signal interrupts: the state records the restore before it
# starts, and --recover puts the database back.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
write_db new
touch "$world/term-on-restore"
run 1 --rollback --restore-backup
assert_state rolling-back
grep -qx "restored=1" "$state/state" || fail "restore not recorded"
assert_db "1 1 old"
run 1 --recover
assert "restore /var/lib/aether-edge/backups/edge-before-restore-"
assert_db "2 2 new"
assert_digest "$B"
assert_state idle

# A manual rollback of an edge that is down, or whose container is gone.
for down in stopped gone; do
	reset
	run 0
	touch "$world/up-fails/$B"
	if [ "$down" = gone ]; then touch "$world/gone"; else : >"$world/running"; fi
	: >"$log"
	run 0 --rollback
	assert "$mount $image@$B backup /var/lib/aether-edge/backups/pre-rollback-v1.1.0-"
	assert_out "recorded by the last update"
	assert_digest "$A"
done

# No-op: the channel already points to the running digest.
reset
echo "$A" >"$world/channel"
run 0
assert_out "up to date"
refute "release-check"
refute "compose"

# --check verifies the running digest, so one pinned by hand is checked
# before its first start, and nothing runs from it before cosign passes.
reset
echo "$A" >"$world/channel"
run 0 --check
assert "cosign verify-attestation --new-bundle-format --type https://github.com/3xDevOps/aether/edge-promotion/v1 --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity https://github.com/3xDevOps/aether/.github/workflows/promote-edge.yml@refs/heads/main $image@$A"
assert_before "cosign verify-attestation" "$image@$A version --json"
assert "docker run --rm $image@$A release-check --tag v1.0.0 --digest $A --from v1.0.0 --json"
assert_out "verified the running v1.0.0 ($A)"
assert_out "up to date"
refute "compose"
reset
echo "$A" >"$world/channel"
echo none >"$world/promotion/$A"
run 1 --check
assert_out "the running v1.0.0 ($A) has no valid promotion attestation on stable"
refute "$image@$A version"
refute "release-check"
reset
echo "$A" >"$world/channel"
echo 1 >"$world/release-check"
run 1 --check
assert_out "release-check refused the running v1.0.0 ($A)"

reset
run 0 --check
assert "release-check --tag v1.1.0"
assert_out "would update v1.0.0 ($A) to v1.1.0 ($B)"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"

reset
run 0 --check --channel latest
assert "docker pull --quiet $image:latest"

# A tag that resolves to two digests is refused, not split by line.
reset
echo "$D" >"$world/extra-digest"
run 1
assert_out "has no single repository digest"
refute "release-check"

reset
printf 'AETHER_EDGE_DIGEST=sha256:abc\n' >"$compose_dir/.env"
run 1
assert_out "no AETHER_EDGE_DIGEST=sha256:<digest> line"
refute "docker"

# Verification failure: nothing changes.
reset
echo 1 >"$world/release-check"
run 1
assert_out "release-check refused v1.1.0"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"

# A digest stable was not promoted to.
for promotion in none "stable v1.0.5" "latest v1.1.0" "stable v1.1.0 $D"; do
	reset
	echo "$promotion" >"$world/promotion/$B"
	run 1
	assert_out "has no valid promotion attestation"
	refute "release-check"
	refute "compose"
	assert_digest "$A"
done

# A promotion signed from a branch or by another workflow.
for signer in promote-edge.yml@refs/heads/evil release.yml@refs/heads/main; do
	reset
	echo "https://github.com/3xDevOps/aether/.github/workflows/$signer" >"$world/signer"
	run 1
	assert_out "has no valid promotion attestation"
	refute "release-check"
	assert_digest "$A"
done

reset
echo none >"$world/promotion/$B"
run 0 --channel latest
refute "cosign"
assert_digest "$B"

reset
mkdir -p "$tmp/nocosign"
for f in "$bin"/*; do
	[ "$(basename "$f")" = cosign ] || ln -sf "$f" "$tmp/nocosign/"
done
set +e
PATH="$tmp/nocosign:/usr/bin:/bin" sh "$script" --check >"$tmp/out" 2>&1
got=$?
set -e
[ "$got" = 1 ] || fail "--check without cosign exited $got"
assert_out "cosign not found"
refute "docker"

reset
echo v1.1.1 >"$world/label/$B"
echo "stable v1.1.1" >"$world/promotion/$B"
run 1
assert_out "reports version v1.1.0 but is labelled v1.1.1"
refute "compose"

# A running image from before the updater's commands gets the manual first
# upgrade, and nothing runs from the candidate.
for missing in legacy no-release-check; do
	reset
	touch "$world/$missing/$A"
	run 1
	assert_out "predates aether-edge version --json, release-check, backup and schema"
	assert_out "cosign verify-attestation --new-bundle-format"
	assert_out "AETHER_EDGE_DIGEST=$B in $compose_dir/.env"
	assert_out "docker compose up -d --wait aether-edge"
	refute "$image@$B release-check"
	refute "$image@$B version"
	refute "aether-edge backup"
	refute "compose"
	assert_digest "$A"
done

# Held for a blocking manual step.
reset
echo 3 >"$world/release-check"
run 3
assert_state held
grep -q "^reason=.*manual step rotate-key.*manual-steps-done" "$state/state" || fail "held state has no reason"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"
: >"$log"
run 0 --manual-steps-done
assert_digest "$B"
assert_state idle

# --manual-steps-done passes only the release that was held.
reset
echo 3 >"$world/release-check"
run 3
echo "$D" >"$world/channel"
run 3 --manual-steps-done
assert_out "applies only to a held release"
assert_digest "$A"
reset
echo 3 >"$world/release-check"
run 3 --manual-steps-done
assert_digest "$A"

# A reason with backslashes, quotes and command substitutions survives the
# state file as it is, and the next run reads it.
reset
echo "3 min" >"$world/release-check"
cat >"$world/reasons" <<'EOF'
["a\\c b \\\\ 'single' \"double\" $(touch pwned) `touch pwned` end"]
EOF
run 3
run 3
assert_out "end"
expected="release-check holds v1.1.0: $(jq -r '.[0]' "$world/reasons")"
# shellcheck disable=SC1091,SC2154 # the state file sets reason
(cd "$tmp" && . "$state/state" && [ "$reason" = "$expected" ]) || fail "reason changed in the state file: $(cat "$state/state")"
if [ -e "$tmp/pwned" ] || [ -e "$compose_dir/pwned" ]; then
	fail "the state file ran a command"
fi

# --manual-steps-done never passes a min_upgrade_from hold.
reset
echo "3 min" >"$world/release-check"
run 3
assert_state held
grep -q "^reason=.*upgrades only from v1.0.5" "$state/state" || fail "held state has no reason"
! grep -q "^reason=.*manual-steps-done" "$state/state" || fail "held state offers --manual-steps-done"
run 1 --manual-steps-done
assert_out "passes only blocking manual steps, and release-check holds v1.1.0 for more: v1.1.0 upgrades only from v1.0.5"
refute "compose"
assert_digest "$A"

# Downgrade refused before any verification.
reset
echo "$C" >"$world/channel"
run 1
assert_out "refusing v0.9.0"
refute "release-check"
refute "compose"
reset
echo v1.1.0-alpha.2 >"$world/version/$B"
echo v1.1.0 >"$world/version/$A"
run 1
assert_out "not newer than the running v1.1.0"

reset
touch "$world/backup-fails"
run 1
assert_out "nothing changed"
refute "compose"
assert_digest "$A"
[ ! -e "$state/state" ] || fail "state written before the backup succeeded"

# Unhealthy new release: rollback without restore.
reset
touch "$world/unready/$B"
run 1
assert_out "rolled back to v1.0.0"
assert "--user 65532:65532 $mount $image@$A schema --json"
refute "restore"
refute " stop aether-edge"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 aether-edge healthcheck --ready fails" "$state/failed" || fail "failed release not denied"
[ "$(cat "$world/running")" = "$A" ] || fail "old release not running"

reset
echo v1.0.9 >"$world/reports/$B"
run 1
assert_out "the container reports version 'v1.0.9', expected v1.1.0"
assert_digest "$A"

reset
echo SHA256:other >"$world/fingerprint/$B"
run 1
assert_out "lost its edge_key"
assert_digest "$A"

# Migrated, then failed: the rollback restores the backup.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
touch "$world/unready/$B"
run 1
assert_before " stop aether-edge" "--user 65532:65532 $mount $image@$A restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_before "schema --json" "restore /var/lib"
assert_db "1 1 old"
assert_digest "$A"
assert_state idle

# The same when Compose removed the new container: the old image reaches
# the data directory through the bind mount.
reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
touch "$world/gone-after-up/$B"
run 1
assert_out "rolled back to v1.0.0"
assert "$mount $image@$A restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
refute "--volumes-from"
assert_db "1 1 old"
assert_digest "$A"
assert_state idle

# An additive migration within the compat floor: no restore.
reset
echo 2 >"$world/supports/$B"
echo 1 >"$world/floor/$B"
touch "$world/unready/$B"
run 1
refute "restore"
assert_db "2 1 old"
assert_digest "$A"

reset
touch "$world/unready/$B" "$world/schema-fails"
run 1
assert "restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_digest "$A"

# The rollback fails: broken, and later runs never loop.
reset
touch "$world/unready/$B" "$world/up-fails/$A"
run 1
assert_state broken
assert_out "State is broken"
assert_denied "$B"
: >"$log"
run 1
assert_out "state is broken since"
refute "docker"
rm "$state/state"
rm "$world/up-fails/$A"
echo "$A" >"$world/running"
run 0
assert_out "skipping $B"

reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
touch "$world/unready/$B" "$world/restore-fails"
run 1
assert_state broken
assert_out "aether-edge restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"

# A rollback is recorded before the release is denied: when the denylist
# cannot be written, the state already says rolling-back.
reset
touch "$world/unready/$B"
mkdir -p "$state/failed"
run 2
assert_state rolling-back
rmdir "$state/failed"
run 1
assert_out "rolled back to v1.0.0"
assert_denied "$B"
assert_digest "$A"

# An empty or garbled state file is an error, never idle.
for content in "" "garbage=1"; do
	reset
	mkdir -p "$state"
	printf '%s' "$content" >"$state/state"
	run 1
	assert_out "has no valid state= line"
	refute "docker"
done

# Interrupted run: a reboot during the switch is finished.
reset
cat >"$tmp/interrupted" <<EOF
state=switching
kind=update
from_digest=$A
from_version='v1.0.0'
to_digest=$B
to_version='v1.1.0'
fingerprint='SHA256:good'
servers=3
backup='/var/lib/aether-edge/backups/pre-update-v1.1.0.db'
restore=''
restored=0
reason=''
since=2026-01-01T00:00:00Z
EOF
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
run 0
assert_out "finishing the interrupted switch"
refute "docker pull"
assert_digest "$B"
assert_state idle

# The public check ends at its deadline, counting curl's own timeout.
reset
touch "$world/hang"
echo 100 >"$world/unrouted"
run 1
assert_out "GET https://edge.example.com/v1/edge failed; the running edge must answer before an update"
[ "$(grep -c '^curl' "$log")" = 3 ] || fail "expected 3 requests in 30 s, got $(grep -c '^curl' "$log")"

# At boot the proxy may route only after a while; the public check waits.
reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
echo 4 >"$world/unrouted"
run 0 --recover
assert_digest "$B"
assert_state idle

reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
touch "$world/unready/$B"
run 1
assert_out "rolled back to v1.0.0"
assert_digest "$A"
assert_state idle

# Servers missing after the rollback do not make it broken.
reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
touch "$world/unready/$B"
echo 1 >"$world/claimed/$A"
run 1
assert_out "rolled back to v1.0.0"
assert_state idle

# A reboot after the restore stopped the edge: --recover at boot.
reset
mkdir -p "$state"
sed 's/^state=switching/state=rolling-back/' "$tmp/interrupted" >"$state/state"
cp "$world/db" "$world/data/backups/pre-update-v1.1.0.db"
: >"$world/running"
run 1 --recover
assert_out "rolling back to v1.0.0"
refute "docker pull"
assert_digest "$A"
assert_state idle
assert_denied "$B"
: >"$log"
run 0 --recover
assert_out "no interrupted run to recover"
refute "docker"

# SIGTERM mid-switch, as from TimeoutStartSec: the switch is marked for a
# rollback that ExecStopPost=--recover performs, and the release is
# retried later.
reset
touch "$world/term-on-up/$B"
run 1
assert_out "stopped by a signal during the switch to v1.1.0; aether-edge-update --recover rolls it back"
assert_state rolling-back
grep -qx "retry=1" "$state/state" || fail "interrupted switch not marked for retry"
run 1 --recover
assert_out "the next run retries $B"
refute_denied "$B"
assert_digest "$A"
assert_state idle
run 0
assert_digest "$B"

reset
touch "$world/locked"
run 0
assert_out "another run holds"
refute "docker"
run 1 --rollback

reset
mkdir -p "$state"
echo "$B v1.1.0 checks failed" >"$state/failed"
run 0
assert_out "skipping $B, which failed before: v1.1.0 checks failed"
refute "release-check"
refute "compose"

# Servers have the configured time to come back.
reset
echo 2 >"$world/slow/$B"
run 0
assert_digest "$B"

# By default 10% of the claimed servers, rounded up, may stay away.
reset
echo 2 >"$world/claimed/$B"
run 0
assert_digest "$B"

# Too few servers: rolled back, not denied, retried on the next run.
reset
echo 1 >"$world/claimed/$B"
run 1
assert_out "1 claimed servers reconnected within 15s, expected at least 2"
assert_out "the next run retries $B"
refute_denied "$B"
assert_digest "$A"
assert_state idle
grep -q "^reason=.*next run retries" "$state/state" || fail "retry not recorded"
rm "$world/claimed/$B"
run 0
assert_digest "$B"

reset
echo 2 >"$world/claimed/$B"
export AETHER_EDGE_UPDATE_SERVER_TOLERANCE=0
run 1
assert_out "2 claimed servers reconnected within 15s, expected at least 3"
reset
echo 1 >"$world/claimed/$B"
export AETHER_EDGE_UPDATE_SERVER_TOLERANCE=50%
run 0
assert_digest "$B"

# Bad settings are refused before anything runs.
for bad in x 10%% %; do
	reset
	export AETHER_EDGE_UPDATE_SERVER_TOLERANCE="$bad"
	run 1
	assert_out "AETHER_EDGE_UPDATE_SERVER_TOLERANCE a count or a percentage such as 10%"
	[ ! -s "$log" ] || fail "commands ran despite a bad setting"
done
unset AETHER_EDGE_UPDATE_SERVER_TOLERANCE

echo "edge-update-test: ok"
