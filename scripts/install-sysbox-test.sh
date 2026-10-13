#!/bin/sh
# Exercises scripts/install-sysbox.sh with stubbed id, dpkg, dpkg-query,
# docker, apt-get and curl. The real package is never downloaded, so the
# checksum failure stands in for the install path: it must stop before
# apt-get runs.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

script="$script_dir/install-sysbox.sh"
bin="$tmp/bin"
log="$tmp/cmd.log"
mkdir -p "$bin"

stub() {
	printf '#!/bin/sh\n%s\n' "$2" >"$bin/$1"
	chmod +x "$bin/$1"
}

stub id 'echo "${FAKE_UID:-0}"'
stub dpkg 'echo "${FAKE_ARCH:-amd64}"'
stub dpkg-query 'printf "%s" "${FAKE_INSTALLED:-}"; [ -n "${FAKE_INSTALLED:-}" ]'
stub docker 'case "$1" in
info) echo "${FAKE_RUNTIMES:-{\"runc\":{}\}}" ;;
ps) i=0; while [ "$i" -lt "${FAKE_RUNNING:-0}" ]; do echo "c$i"; i=$((i + 1)); done ;;
esac'
stub apt-get 'echo "apt-get $*" >>"$CMD_LOG"'
stub curl 'out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done; echo not-a-deb >"$out"'
stub jq 'exec "$REAL_JQ" "$@"'
REAL_JQ=$(command -v jq) || { echo "install-sysbox-test: jq is required" >&2; exit 1; }
export REAL_JQ CMD_LOG="$log"

printf '{}\n' >"$tmp/empty.json"
printf '{"live-restore": true}\n' >"$tmp/live.json"

run() {
	: >"$log"
	if env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="$tmp/empty.json" "$@" sh "$script" >"$tmp/out" 2>&1; then
		status=0
	else
		status=$?
	fi
}

expect() {
	want_status=$1 want_text=$2
	if [ "$status" -ne "$want_status" ] || ! grep -qF -- "$want_text" "$tmp/out"; then
		echo "install-sysbox-test: wanted status $want_status and [$want_text], got $status:" >&2
		cat "$tmp/out" >&2
		exit 1
	fi
}

run FAKE_UID=1000
expect 1 "run as root"

run FAKE_ARCH=riscv64
expect 1 "unsupported architecture riscv64"

run FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 0 "nothing to do"

run FAKE_INSTALLED=0.7.0.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 1 "Upgrade by following"

run FAKE_RUNNING=3
expect 1 "3 containers are running"
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran despite running containers" >&2; exit 1; }

run FAKE_RUNNING=3 AETHER_SYSBOX_DAEMON_JSON="$tmp/live.json"
expect 1 "checksum mismatch for sysbox-ce_0.7.1.linux_amd64.deb"

run FAKE_ARCH=arm64 FAKE_RUNNING=0
expect 1 "checksum mismatch for sysbox-ce_0.7.1.linux_arm64.deb"
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran after a checksum mismatch" >&2; exit 1; }

echo "install-sysbox-test: ok"
