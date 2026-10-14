#!/bin/sh
# Exercises scripts/install-sysbox.sh with stubbed id, dpkg, dpkg-query,
# uname, modinfo, ip, docker, apt-get and curl. The real package is never downloaded, so the
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
stub uname 'echo "${FAKE_KERNEL:-6.8.0-45-generic}"'
stub modinfo '[ -n "${FAKE_SHIFTFS:-}" ]'
stub ip 'case "$*" in
"route get "*) case "$3" in "${FAKE_SUPERNET:-none}"*) echo "local $3 dev br0" ;; *) echo "$3 via 10.0.0.1 dev eth0" ;; esac ;;
"route") echo "default via 10.0.0.1 dev eth0" ;;
"-4 -o route") echo "default via 10.0.0.1 dev eth0"; echo "10.0.0.0/24 dev eth0 scope link"; [ -z "${FAKE_ROUTE:-}" ] || echo "$FAKE_ROUTE dev br0 scope link" ;;
"-4 -o address show dev docker0") [ -z "${FAKE_DOCKER0:-}" ] || echo "3: docker0    inet $FAKE_DOCKER0 brd 172.20.255.255 scope global docker0" ;;
"-4 -o address") echo "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0"; [ -z "${FAKE_ADDR:-}" ] || echo "5: br0    inet $FAKE_ADDR scope global br0"; [ -z "${FAKE_DOCKER0:-}" ] || echo "3: docker0    inet $FAKE_DOCKER0 scope global docker0" ;;
esac'
stub docker 'case "$1" in
info) echo "${FAKE_RUNTIMES:-{\"runc\":{}\}}" ;;
esac'
stub apt-get 'echo "apt-get $*" >>"$CMD_LOG"'
stub curl 'out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done; echo not-a-deb >"$out"'
stub jq 'exec "$REAL_JQ" "$@"'
REAL_JQ=$(command -v jq) || { echo "install-sysbox-test: jq is required" >&2; exit 1; }
export REAL_JQ CMD_LOG="$log"

daemon_json="$tmp/daemon.json"
daemon_content='{}'

run() {
	: >"$log"
	printf '%s\n' "$daemon_content" >"$daemon_json"
	if env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="$daemon_json" "$@" sh "$script" >"$tmp/out" 2>&1; then
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

run FAKE_KERNEL=5.15.0-122-generic
expect 1 "kernel 5.15.0-122-generic cannot ID-map bind mounts"

run FAKE_KERNEL=4.19.0 FAKE_SHIFTFS=1
expect 1 "checksum mismatch"

run FAKE_KERNEL=5.19.2
expect 1 "checksum mismatch"

daemon_content='{"log-level": "warn", "bip": "10.9.0.1/24"}'
run
expect 1 "checksum mismatch"
expect 1 "added \"default-address-pools\""
want='{"log-level":"warn","bip":"10.9.0.1/24","default-address-pools":[{"base":"172.25.0.0/16","size":24}]}'
[ "$(jq -c . "$daemon_json")" = "$want" ] || { echo "install-sysbox-test: daemon.json is $(jq -c . "$daemon_json"), want $want" >&2; exit 1; }
expect 1 "running containers keep running"

daemon_content='{"bip": "10.9.0.1/24", "default-address-pools": [{"base": "10.10.0.0/16", "size": 24}]}'
run
expect 1 "checksum mismatch"
[ "$(jq -c . "$daemon_json")" = "$(printf '%s' "$daemon_content" | jq -c .)" ] || { echo "install-sysbox-test: existing keys were rewritten" >&2; exit 1; }

daemon_content='{}'
run
expect 1 "added \"bip\": \"172.20.0.1/16\""
[ "$(jq -r '.bip, .["default-address-pools"][0].base' "$daemon_json" | tr '\n' ' ')" = "172.20.0.1/16 172.25.0.0/16 " ] || { echo "install-sysbox-test: empty daemon.json not filled in" >&2; exit 1; }

rm -f "$daemon_json"
mkdir -p "$tmp/etc"
run AETHER_SYSBOX_DAEMON_JSON="$tmp/etc/new.json"
expect 1 "checksum mismatch"
[ -s "$tmp/etc/new.json" ] || { echo "install-sysbox-test: missing daemon.json not created" >&2; exit 1; }

untouched() {
	[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed despite overlap" >&2; exit 1; }
	[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran despite overlap" >&2; exit 1; }
}

run FAKE_SUPERNET=172.20.
expect 1 "cannot add \"bip\": 172.20.0.1/16 overlaps a subnet on this host"
untouched

run FAKE_ROUTE=172.20.5.0/24
expect 1 "cannot add \"bip\": 172.20.0.1/16 overlaps"
untouched

run FAKE_ADDR=172.25.9.1/24
expect 1 "cannot add \"default-address-pools\": 172.25.0.0/16 overlaps"
untouched

run FAKE_ROUTE=172.25.0.0/20 FAKE_SUPERNET=172.99.
expect 1 "cannot add \"default-address-pools\": 172.25.0.0/16 overlaps"
untouched

rm -f "$daemon_json"
run AETHER_SYSBOX_DAEMON_JSON="$tmp/etc/none.json" FAKE_ADDR=172.25.9.1/24
expect 1 "172.25.0.0/16 overlaps"
[ ! -e "$tmp/etc/none.json" ] || { echo "install-sysbox-test: daemon.json created despite overlap" >&2; exit 1; }

run FAKE_DOCKER0=172.20.0.1/16 FAKE_SUPERNET=172.20.
expect 1 "checksum mismatch"

daemon_content='{}'
run FAKE_ARCH=arm64
expect 1 "checksum mismatch for sysbox-ce_0.7.1.linux_arm64.deb"
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran after a checksum mismatch" >&2; exit 1; }

echo "install-sysbox-test: ok"
