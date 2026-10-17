#!/bin/sh
# Exercises scripts/edge-update.sh against stubbed docker, curl, flock and
# sleep, the same style as deploy-test.sh. The docker stub keeps a small
# world in files: which digest runs, what the channel tag points to, the
# database schema, and which releases fail. A real edge is not involved.
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

# World files, per digest under $WORLD/<name>/<digest>:
#   version     the release the image is (label and version --json)
#   supports    the highest database schema the image supports
#   floor       the compat floor its migrations leave in the database
#   up-fails    docker compose up fails for it
#   unready     healthcheck --ready fails in it
#   fingerprint served on /v1/edge while it runs (default SHA256:good)
# and $WORLD/channel, $WORLD/running, $WORLD/db (schema floor).
cat >"$bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf 'docker %s\n' "$*" >>"$CMD_LOG"
w="$WORLD"
get() { cat "$w/$1/$2" 2>/dev/null || echo "${3:-}"; }
image=ghcr.io/3xdevops/aether-edge
case "$1" in
pull) exit 0 ;;
image)
	ref="$5"
	case "$4" in
	*RepoDigests*) echo "$image@$(cat "$w/channel")" ;;
	*image.version*) get version "${ref#*@}" ;;
	esac
	exit 0
	;;
run)
	shift
	while [ $# -gt 0 ]; do
		case "$1" in
		*@sha256:*) break ;;
		esac
		shift
	done
	d="${1#*@}"
	shift
	case "$1" in
	version) printf '{"version":"%s","commit":"abc","store_schema":%s}\n' "$(get version "$d")" "$(get supports "$d" 1)" ;;
	release-check) exit "$(cat "$w/release-check" 2>/dev/null || echo 0)" ;;
	schema)
		read -r schema floor <"$w/db"
		printf '{"schema":%s,"compat_floor":%s,"supported":%s}\n' "$schema" "$floor" "$(get supports "$d" 1)"
		;;
	restore)
		[ ! -f "$w/restore-fails" ] || exit 1
		cp "$w/db.backup" "$w/db"
		;;
	esac
	exit 0
	;;
exec)
	running="$(cat "$w/running")"
	[ -n "$running" ] || { echo "container not running" >&2; exit 1; }
	case "$3 $4" in
	"aether-edge backup")
		[ ! -f "$w/backup-fails" ] || { echo "aether-edge: backup: disk full" >&2; exit 1; }
		cp "$w/db" "$w/db.backup"
		;;
	"aether-edge version") printf '{"version":"%s","commit":"abc"}\n' "$(get version "$running")" ;;
	"aether-edge healthcheck")
		[ ! -f "$w/unready/$running" ] || { echo "aether-edge: healthcheck: not ready" >&2; exit 1; }
		printf '{"status":"ready","version":"%s","protocol":{"version":1,"min_version":1},"servers":{"claimed":%s,"unclaimed":0}}\n' \
			"$(get version "$running")" "$(get claimed "$running" 3)"
		;;
	esac
	exit 0
	;;
compose)
	env_file="$3"
	case "$4" in
	stop) : >"$w/running" ;;
	up)
		d="$(sed -n 's/^AETHER_EDGE_DIGEST=//p' "$env_file")"
		[ ! -f "$w/up-fails/$d" ] || exit 1
		: >"$w/running"
		read -r schema floor <"$w/db"
		supports="$(get supports "$d" 1)"
		if [ "$supports" -gt "$schema" ]; then
			echo "$supports $(get floor "$d" "$supports")" >"$w/db"
		elif [ "$schema" -gt "$supports" ] && [ "$floor" -gt "$supports" ]; then
			exit 1
		fi
		echo "$d" >"$w/running"
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
running="$(cat "$WORLD/running")"
[ -n "$running" ] || exit 7
fp="$(cat "$WORLD/fingerprint/$running" 2>/dev/null || echo SHA256:good)"
printf '{"signin_origin":"https://auth.example.com","key":"k","fingerprint":"%s","version":1,"min_version":1}\n' "$fp"
EOF

cat >"$bin/flock" <<'EOF'
#!/bin/sh
printf 'flock %s\n' "$*" >>"$CMD_LOG"
[ ! -f "$WORLD/locked" ]
EOF

cat >"$bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF

chmod 0755 "$bin/docker" "$bin/curl" "$bin/flock" "$bin/sleep"

state="$tmp/state"
compose_dir="$tmp/compose"
export CMD_LOG="$log" WORLD="$world" PATH="$bin:$PATH"
export AETHER_EDGE_UPDATE_CONFIG="$tmp/none" AETHER_EDGE_UPDATE_STATE="$state" AETHER_EDGE_UPDATE_COMPOSE_DIR="$compose_dir"
export AETHER_EDGE_UPDATE_RELAY_URL=https://edge.example.com AETHER_EDGE_UPDATE_SERVER_WAIT=15

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

# Fresh world: A (v1.0.0) runs, the channel points to B (v1.1.0), C is
# v0.9.0, every image supports schema 1 and the database is at 1.
reset() {
	rm -rf "$world" "$state" "$compose_dir"
	mkdir -p "$world/version" "$world/supports" "$world/floor" "$world/up-fails" \
		"$world/unready" "$world/claimed" "$world/fingerprint" "$compose_dir"
	echo v1.0.0 >"$world/version/$A"
	echo v1.1.0 >"$world/version/$B"
	echo v0.9.0 >"$world/version/$C"
	echo "$B" >"$world/channel"
	echo "$A" >"$world/running"
	echo "1 1" >"$world/db"
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

# --- success: verify, back up, switch, check -----------------------------

reset
run 0
assert "docker pull --quiet $image:stable"
assert "docker run --rm $image@$A release-check --tag v1.1.0 --digest $B --from v1.0.0 --json"
assert_before "docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/pre-update-v1.1.0-" "up -d --wait --wait-timeout 180 aether-edge"
assert_before "release-check" "aether-edge backup"
refute "restore"
refute " stop aether-edge"
assert_digest "$B"
grep -qx COMPOSE_PROJECT_NAME=edge "$compose_dir/.env" || fail "other .env lines were dropped"
assert_state idle
grep -qx "$B v1.1.0" "$state/current" || fail "current not recorded"
grep -q "^$A v1.0.0 /var/lib/aether-edge/backups/pre-update-v1.1.0-[0-9TZ]*\.db$" "$state/previous" || fail "previous not recorded"
[ "$(stat -c %a "$state")" = 700 ] || fail "state directory is not 0700"
assert_out "now running v1.1.0"

# --- manual rollback to the recorded previous release ----------------------

: >"$log"
run 0 --rollback
assert "aether-edge backup /var/lib/aether-edge/backups/pre-rollback-v1.1.0-"
assert_before " stop aether-edge" "schema --json"
refute "restore"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 rolled back by an operator" "$state/failed" || fail "rolled-back release not denied"
[ ! -e "$state/previous" ] || fail "previous kept after rollback"
: >"$log"
run 0
assert_out "skipping $B"
refute "aether-edge backup"

# --- no-op: the channel already points to the running digest ---------------

reset
echo "$A" >"$world/channel"
run 0
assert_out "up to date"
refute "release-check"
refute "compose"

# --- --check resolves and verifies, changes nothing ------------------------

reset
run 0 --check
assert "release-check --tag v1.1.0"
assert_out "would update v1.0.0 ($A) to v1.1.0 ($B)"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"

# --- --channel picks another tag -------------------------------------------

reset
run 0 --check --channel latest
assert "docker pull --quiet $image:latest"

# --- verification failure: nothing changes ---------------------------------

reset
echo 1 >"$world/release-check"
run 1
assert_out "release-check refused v1.1.0"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"

# --- held for a blocking manual step ---------------------------------------

reset
echo 3 >"$world/release-check"
run 3
assert_state held
grep -q "^reason=.*manual-steps-done" "$state/state" || fail "held state has no reason"
refute "aether-edge backup"
refute "compose"
assert_digest "$A"
: >"$log"
run 0 --manual-steps-done
assert_digest "$B"
assert_state idle

# --- downgrade refused before any verification -----------------------------

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

# --- backup failure aborts before any change -------------------------------

reset
touch "$world/backup-fails"
run 1
assert_out "nothing changed"
refute "compose"
assert_digest "$A"
[ ! -e "$state/state" ] || fail "state written before the backup succeeded"

# --- unhealthy new release: rollback without restore -----------------------

reset
touch "$world/unready/$B"
run 1
assert_out "rolled back to v1.0.0"
assert_before " stop aether-edge" "--user 65532:65532 --volumes-from aether-edge $image@$A schema --json"
refute "restore"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 aether-edge healthcheck --ready fails" "$state/failed" || fail "failed release not denied"
[ "$(cat "$world/running")" = "$A" ] || fail "old release not running"

# --- the new edge lost its key: rollback ------------------------------------

reset
echo SHA256:other >"$world/fingerprint/$B"
run 1
assert_out "lost its edge_key"
assert_digest "$A"

# --- migrated, then failed: rollback restores the backup -------------------

reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
touch "$world/unready/$B"
run 1
assert "--user 65532:65532 --volumes-from aether-edge $image@$A restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_before "schema --json" "restore /var/lib"
[ "$(cat "$world/db")" = "1 1" ] || fail "database not restored"
assert_digest "$A"
assert_state idle

# --- additive migration within the compat floor: no restore ---------------

reset
echo 2 >"$world/supports/$B"
echo 1 >"$world/floor/$B"
touch "$world/unready/$B"
run 1
refute "restore"
[ "$(cat "$world/db")" = "2 1" ] || fail "database changed"
assert_digest "$A"

# --- rollback fails: broken, and later runs never loop ----------------------

reset
touch "$world/unready/$B" "$world/up-fails/$A"
run 1
assert_state broken
assert_out "State is broken"
: >"$log"
run 1
assert_out "state is broken since"
refute "docker"

# --- interrupted run: a reboot during the switch is finished ---------------

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

# --- interrupted run that cannot finish is rolled back ---------------------

reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
touch "$world/unready/$B"
run 1
assert_out "rolled back to v1.0.0"
assert_digest "$A"
assert_state idle

# --- interrupted rollback is redone ----------------------------------------

reset
mkdir -p "$state"
sed 's/^state=switching/state=rolling-back/' "$tmp/interrupted" >"$state/state"
echo "$B" >"$world/running"
run 1
assert_out "rolling back to v1.0.0"
assert_digest "$A"
assert_state idle

# --- another run holds the lock --------------------------------------------

reset
touch "$world/locked"
run 0
assert_out "another run holds"
refute "docker"

# --- a release that failed before is skipped -------------------------------

reset
mkdir -p "$state"
echo "$B v1.1.0 checks failed" >"$state/failed"
run 0
assert_out "skipping $B, which failed before: v1.1.0 checks failed"
refute "release-check"
refute "compose"

# --- servers that do not come back trigger a rollback ----------------------

reset
echo 1 >"$world/claimed/$B"
run 1
assert_out "1 claimed servers reconnected within 15s, expected at least 3"
assert_digest "$A"
reset
echo 2 >"$world/claimed/$B"
export AETHER_EDGE_UPDATE_SERVER_TOLERANCE=1
run 0
unset AETHER_EDGE_UPDATE_SERVER_TOLERANCE
assert_digest "$B"

echo "edge-update-test: ok"
