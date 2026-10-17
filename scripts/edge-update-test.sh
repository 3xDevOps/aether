#!/bin/sh
# Exercises scripts/edge-update.sh against stubbed docker, curl, flock and
# sleep, the same style as deploy-test.sh. The docker stub keeps a small
# world in files: which digest runs, what the channel tag points to, the
# database and its backups, and which releases fail. A real edge is not
# involved.
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
#   up-fails    docker compose up fails for it
#   unready     healthcheck --ready fails in it
#   claimed     claimed servers while it runs (default 3)
#   slow        readiness reports 0 claimed servers this many times first
#   fingerprint served on /v1/edge while it runs (default SHA256:good)
# and $WORLD/channel, $WORLD/running, $WORLD/db ("schema floor data"),
# $WORLD/backups/<name>, and the switches backup-fails, restore-fails and
# schema-fails.
cat >"$bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf 'docker %s\n' "$*" >>"$CMD_LOG"
w="$WORLD"
get() { cat "$w/$1/$2" 2>/dev/null || echo "${3:-}"; }
backup() {
	[ ! -f "$w/backup-fails" ] || { echo "aether-edge: backup: disk full" >&2; exit 1; }
	cp "$w/db" "$w/backups/$(basename "$1")"
}
image=ghcr.io/3xdevops/aether-edge
case "$1" in
pull) exit 0 ;;
image)
	d="${5#*@}"
	case "$4" in
	*RepoDigests*) echo "$image@$(cat "$w/channel")" ;;
	*image.version*) get label "$d" "$(get version "$d")" ;;
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
		[ ! -f "$w/schema-fails" ] || { echo "aether-edge: edge.db: file is not a database" >&2; exit 1; }
		read -r schema floor data <"$w/db"
		printf '{"schema":%s,"compat_floor":%s,"supported":%s}\n' "$schema" "$floor" "$(get supports "$d" 1)"
		;;
	backup) backup "$2" ;;
	restore)
		[ ! -f "$w/restore-fails" ] || { echo "aether-edge: restore: disk full" >&2; exit 1; }
		[ -z "$(cat "$w/running")" ] || { echo "aether-edge: restore: an edge holds edge.lock" >&2; exit 1; }
		cp "$w/backups/$(basename "$2")" "$w/db"
		;;
	esac
	exit 0
	;;
exec)
	running="$(cat "$w/running")"
	[ -n "$running" ] || { echo "Error response from daemon: container is not running" >&2; exit 1; }
	case "$3 $4" in
	"aether-edge backup") backup "$5" ;;
	"aether-edge version") printf '{"version":"%s","commit":"abc"}\n' "$(get reports "$running" "$(get version "$running")")" ;;
	"aether-edge healthcheck")
		[ ! -f "$w/unready/$running" ] || { echo "aether-edge: healthcheck: not ready" >&2; exit 1; }
		claimed="$(get claimed "$running" 3)"
		slow="$(get slow "$running" 0)"
		if [ "$slow" -gt 0 ]; then
			echo $((slow - 1)) >"$w/slow/$running"
			claimed=0
		fi
		printf '{"status":"ready","version":"%s","protocol":{"version":1,"min_version":1},"servers":{"claimed":%s,"unclaimed":0}}\n' \
			"$(get version "$running")" "$claimed"
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
		read -r schema floor data <"$w/db"
		supports="$(get supports "$d" 1)"
		if [ "$supports" -gt "$schema" ]; then
			echo "$supports $(get floor "$d" "$supports") $data" >"$w/db"
		elif [ "$floor" -gt "$supports" ]; then
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

assert_db() {
	[ "$(cat "$world/db")" = "$1" ] || fail "expected database '$1', got '$(cat "$world/db")'"
}

assert_denied() {
	grep -q "^$1 " "$state/failed" 2>/dev/null || fail "$1 not in failed"
}

# Fresh world: A (v1.0.0) runs, the channel points to B (v1.1.0), C is
# v0.9.0, D is v1.2.0. Every image supports schema 1; the database is at 1
# and holds "old".
reset() {
	rm -rf "$world" "$state" "$compose_dir"
	for d in version label reports supports floor up-fails unready claimed slow fingerprint backups; do
		mkdir -p "$world/$d"
	done
	mkdir -p "$compose_dir"
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
assert "--volumes-from aether-edge $image@$A schema --json"
refute "restore"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 rolled back by an operator" "$state/failed" || fail "rolled-back release not denied"
[ ! -e "$state/previous" ] || fail "previous kept after rollback"
: >"$log"
run 0
assert_out "skipping $B"
refute "aether-edge backup"

# --- manual rollback over a migration restores the pre-update backup -------

reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
assert_db "2 2 old"
write_db new
run 0 --rollback
assert_before " stop aether-edge" "restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_db "1 1 old"
assert_digest "$A"

# --- a failed manual rollback puts back the newer data ---------------------

reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
run 0
write_db new
touch "$world/unready/$A"
run 1 --rollback
assert "restore /var/lib/aether-edge/backups/pre-rollback-v1.1.0-"
assert_db "2 2 new"
assert_digest "$B"
assert_state idle

# --- manual rollback of an edge that is down -------------------------------

reset
run 0
touch "$world/up-fails/$B"
: >"$world/running"
: >"$log"
run 0 --rollback
assert "--volumes-from aether-edge $image@$B backup /var/lib/aether-edge/backups/pre-rollback-v1.1.0-"
assert_out "recorded by the last update"
assert_digest "$A"

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

# --- a label that disagrees with the image is refused ----------------------

reset
echo v1.1.1 >"$world/label/$B"
run 1
assert_out "reports version v1.1.0 but is labelled v1.1.1"
refute "compose"

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

# --- --manual-steps-done passes only the release that was held -------------

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
assert "--user 65532:65532 --volumes-from aether-edge $image@$A schema --json"
refute "restore"
refute " stop aether-edge"
assert_digest "$A"
assert_state idle
grep -q "^$B v1.1.0 aether-edge healthcheck --ready fails" "$state/failed" || fail "failed release not denied"
[ "$(cat "$world/running")" = "$A" ] || fail "old release not running"

# --- the new edge reports another version: rollback ------------------------

reset
echo v1.0.9 >"$world/reports/$B"
run 1
assert_out "the container reports version 'v1.0.9', expected v1.1.0"
assert_digest "$A"

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
assert_before " stop aether-edge" "--user 65532:65532 --volumes-from aether-edge $image@$A restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_before "schema --json" "restore /var/lib"
assert_db "1 1 old"
assert_digest "$A"
assert_state idle

# --- additive migration within the compat floor: no restore ---------------

reset
echo 2 >"$world/supports/$B"
echo 1 >"$world/floor/$B"
touch "$world/unready/$B"
run 1
refute "restore"
assert_db "2 1 old"
assert_digest "$A"

# --- an unreadable database is restored -------------------------------------

reset
touch "$world/unready/$B" "$world/schema-fails"
run 1
assert "restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"
assert_digest "$A"

# --- rollback fails: broken, and later runs never loop ----------------------

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

# --- a failed restore is broken too ------------------------------------------

reset
echo 2 >"$world/supports/$B"
echo 2 >"$world/floor/$B"
touch "$world/unready/$B" "$world/restore-fails"
run 1
assert_state broken
assert_out "aether-edge restore /var/lib/aether-edge/backups/pre-update-v1.1.0-"

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

# --- interrupted run that cannot finish is rolled back ---------------------

reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
touch "$world/unready/$B"
run 1
assert_out "rolled back to v1.0.0"
assert_digest "$A"
assert_state idle

# --- servers missing after the rollback do not make it broken ---------------

reset
mkdir -p "$state"
cp "$tmp/interrupted" "$state/state"
touch "$world/unready/$B"
echo 1 >"$world/claimed/$A"
run 1
assert_out "rolled back to v1.0.0"
assert_state idle

# --- a reboot after the restore stopped the edge: --recover at boot --------

reset
mkdir -p "$state"
sed 's/^state=switching/state=rolling-back/' "$tmp/interrupted" >"$state/state"
cp "$world/db" "$world/backups/pre-update-v1.1.0.db"
: >"$world/running"
run 1 --recover
assert_out "rolling back to v1.0.0"
refute "docker pull"
assert_digest "$A"
assert_state idle
: >"$log"
run 0 --recover
assert_out "no interrupted run to recover"
refute "docker"

# --- another run holds the lock --------------------------------------------

reset
touch "$world/locked"
run 0
assert_out "another run holds"
refute "docker"
run 1 --rollback

# --- a release that failed before is skipped -------------------------------

reset
mkdir -p "$state"
echo "$B v1.1.0 checks failed" >"$state/failed"
run 0
assert_out "skipping $B, which failed before: v1.1.0 checks failed"
refute "release-check"
refute "compose"

# --- servers have the configured time to come back -------------------------

reset
echo 2 >"$world/slow/$B"
run 0
assert_digest "$B"
reset
echo 1 >"$world/claimed/$B"
run 1
assert_out "1 claimed servers reconnected within 15s, expected at least 3"
assert_digest "$A"
reset
echo 2 >"$world/claimed/$B"
export AETHER_EDGE_UPDATE_SERVER_TOLERANCE=1
run 0
assert_digest "$B"

# --- bad numbers are refused before anything runs --------------------------

reset
export AETHER_EDGE_UPDATE_SERVER_TOLERANCE=x
run 1
assert_out "tolerance must be a whole number, not 'x'"
[ ! -s "$log" ] || fail "commands ran despite a bad setting"
unset AETHER_EDGE_UPDATE_SERVER_TOLERANCE

echo "edge-update-test: ok"
