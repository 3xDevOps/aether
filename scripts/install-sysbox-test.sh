#!/bin/sh
# Exercises scripts/install-sysbox.sh with stubbed id, dpkg, dpkg-query,
# uname, modinfo, ip, docker, apt-get, curl and sha256sum. Nothing is
# downloaded or installed: the stub apt-get fails on the .deb, and that
# failure is where a run that got past every check stops.
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
stub dpkg 'case "$1" in
--audit) [ -z "${FAKE_AUDIT:-}" ] || echo "sysbox-ce is half configured" ;;
--configure) echo "dpkg $*" >>"$CMD_LOG"; [ -z "${FAKE_CONFIGURE_FAIL:-}" ] ;;
*) echo "${FAKE_ARCH:-amd64}" ;;
esac'
stub dpkg-query 'printf "%s" "${FAKE_INSTALLED:-}"; [ -n "${FAKE_INSTALLED:-}" ]'
stub uname 'echo "${FAKE_KERNEL:-6.8.0-45-generic}"'
stub modinfo '[ -n "${FAKE_SHIFTFS:-}" ]'
stub ip 'case "$*" in
"-4 -o route show table all") echo "default via 10.0.0.1 dev eth0"; echo "10.0.0.0/24 dev eth0 scope link"; echo "local 10.0.0.5 dev eth0 table local"; echo "broadcast 10.0.0.255 dev eth0 table local"; [ -z "${FAKE_LOCAL:-}" ] || echo "local $FAKE_LOCAL dev lo table local"; [ -z "${FAKE_ROUTE:-}" ] || echo "$FAKE_ROUTE dev eth0" ;;
"-4 -o address") echo "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0"; [ -z "${FAKE_ADDR:-}" ] || echo "5: br0    inet $FAKE_ADDR scope global br0" ;;
esac'
stub docker 'case "$1" in
info) if [ -n "${FAKE_RUNTIMES:-}" ]; then echo "$FAKE_RUNTIMES"; elif [ -e "$STATE_FILE" ]; then echo "{\"runc\":{},\"sysbox-runc\":{}}"; else echo "{\"runc\":{}}"; fi ;;
ps) [ -z "${FAKE_RUNNING:-}" ] || echo abc123 ;;
pull) [ -z "${FAKE_PULL_FAIL:-}" ] || { echo "pull access denied"; exit 1; } ;;
run) exit "${FAKE_PROBE_RC:-0}" ;;
network) case "$2" in
	ls) [ -z "${FAKE_NET_FAIL:-}" ] || { echo "Cannot connect to the Docker daemon"; exit 1; }; echo abc ;;
	inspect) echo "172.17.0.0/16 ${FAKE_DOCKER_NETS:-} " ;;
	esac ;;
esac'
stub apt-get 'echo "apt-get $*" >>"$CMD_LOG"; case "$*" in
*.deb*) [ -n "${FAKE_APT_OK:-}" ] || { echo "E: Unmet dependencies"; exit 100; }; touch "$STATE_FILE" ;;
*purge*) [ -z "${FAKE_PURGE_FAIL:-}" ] || { echo "E: dpkg was interrupted"; exit 100; }; rm -f "$STATE_FILE" ;;
esac'
stub systemctl 'echo "systemctl $*" >>"$CMD_LOG"'
stub sha256sum 'case "${FAKE_ARCH:-amd64}" in arm64) h=04ca894ae0b53f0fa54eaacc173ce40363c9a95ea5450f773716a84ef650a69b ;; *) h=9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5 ;; esac; echo "${FAKE_SHA:-$h}  $1"'
stub curl 'out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done; echo not-a-deb >"$out"'
stub jq 'exec "$REAL_JQ" "$@"'
REAL_JQ=$(command -v jq) || { echo "install-sysbox-test: jq is required" >&2; exit 1; }
export REAL_JQ CMD_LOG="$log" STATE_FILE="$tmp/sysbox-registered"

daemon_json="$tmp/daemon.json"
dropin="$tmp/mgr.d/aether.conf"
export AETHER_SYSBOX_MGR_DROPIN="$dropin"
daemon_content='{}'

run() {
	: >"$log"
	rm -f "$STATE_FILE" "$dropin"
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

mv "$bin/docker" "$bin/docker.off"
ln -s "$(command -v sh)" "$bin/sh"
run PATH="$bin"
rm "$bin/sh"
mv "$bin/docker.off" "$bin/docker"
expect 3 "docker not found"

run FAKE_ARCH=riscv64
expect 3 "unsupported architecture riscv64"

run FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 0 "nothing to do"

run FAKE_INSTALLED=0.7.0.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 1 "Upgrade by following"

run FAKE_KERNEL=5.15.0-122-generic
expect 3 "kernel 5.15.0-122-generic cannot ID-map bind mounts"

run FAKE_KERNEL=4.19.0 FAKE_SHIFTFS=1
expect 1 "apt-get install"

run FAKE_KERNEL=5.19.2
expect 1 "apt-get install"

daemon_content='{"log-level": "warn", "bip": "10.9.0.1/24"}'
run
expect 1 "apt-get install"
expect 1 "added \"default-address-pools\""
want='{"log-level":"warn","bip":"10.9.0.1/24","default-address-pools":[{"base":"10.200.0.0/16","size":24}]}'
[ "$(jq -c . "$daemon_json")" = "$want" ] || { echo "install-sysbox-test: daemon.json is $(jq -c . "$daemon_json"), want $want" >&2; exit 1; }
expect 1 "running containers keep running"

daemon_content='{"bip": "10.9.0.1/24", "default-address-pools": [{"base": "10.10.0.0/16", "size": 24}]}'
run
expect 1 "apt-get install"
[ "$(jq -c . "$daemon_json")" = "$(printf '%s' "$daemon_content" | jq -c .)" ] || { echo "install-sysbox-test: existing keys were rewritten" >&2; exit 1; }

daemon_content='{}'
run
expect 1 "added \"bip\": \"10.200.0.1/16\""
expect 1 "added \"default-address-pools\": [{\"base\": \"10.201.0.0/16\""
[ "$(jq -r '.bip, .["default-address-pools"][0].base' "$daemon_json" | tr '\n' ' ')" = "10.200.0.1/16 10.201.0.0/16 " ] || { echo "install-sysbox-test: empty daemon.json not filled in" >&2; exit 1; }

rm -f "$daemon_json"
mkdir -p "$tmp/etc"
run AETHER_SYSBOX_DAEMON_JSON="$tmp/etc/new.json"
expect 1 "apt-get install"
[ -s "$tmp/etc/new.json" ] || { echo "install-sysbox-test: missing daemon.json not created" >&2; exit 1; }

chosen() {
	got="$(jq -r '.bip, .["default-address-pools"][0].base' "$daemon_json" | tr '\n' ' ')"
	[ "$got" = "$1 " ] || { echo "install-sysbox-test: chose $got, want $1" >&2; exit 1; }
}

# A route, an interface address and a Docker network each take their /16 out.
run FAKE_ROUTE=10.200.0.0/16
expect 1 "apt-get install"
chosen "10.201.0.1/16 10.202.0.0/16"

run FAKE_ROUTE=10.201.5.0/24
chosen "10.200.0.1/16 10.202.0.0/16"

run FAKE_ADDR=10.200.9.1/24
chosen "10.201.0.1/16 10.202.0.0/16"

run FAKE_DOCKER_NETS="10.200.0.0/16 10.201.0.0/16"
chosen "10.202.0.1/16 10.203.0.0/16"

run 'FAKE_ROUTE=blackhole 10.200.0.0/16'
chosen "10.201.0.1/16 10.202.0.0/16"

# A /8 takes out every 10.200-254 candidate; 172.24-31 comes next.
run 'FAKE_ROUTE=10.0.0.0/8 via 10.0.0.1'
expect 1 "apt-get install"
chosen "172.24.0.1/16 172.25.0.0/16"

run FAKE_DOCKER_NETS="10.0.0.0/8 172.24.0.0/16"
chosen "172.25.0.1/16 172.26.0.0/16"

# Only the missing key is chosen, and never the existing key's network.
daemon_content='{"bip": "10.200.0.1/16"}'
run
chosen_pool="$(jq -r '.["default-address-pools"][0].base' "$daemon_json")"
[ "$chosen_pool" = 10.201.0.0/16 ] || { echo "install-sysbox-test: pool is $chosen_pool beside an existing bip" >&2; exit 1; }

daemon_content='{}'
run FAKE_DOCKER_NETS="172.16.0.0/12 10.0.0.0/8 192.168.0.0/16"
expect 1 "cannot add \"bip\""
expect 1 "no free /16"
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed with nothing free" >&2; exit 1; }
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran with nothing free" >&2; exit 1; }

# Only one /16 free: the bip gets it and the pool has none left.
run FAKE_DOCKER_NETS="172.16.0.0/12 10.0.0.0/8"
expect 1 "cannot add \"default-address-pools\""
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed with the pool unplaceable" >&2; exit 1; }

rm -f "$daemon_json"
run AETHER_SYSBOX_DAEMON_JSON="$tmp/etc/none.json" FAKE_DOCKER_NETS="172.16.0.0/12 10.0.0.0/8 192.168.0.0/16"
expect 1 "no free /16"
[ ! -e "$tmp/etc/none.json" ] || { echo "install-sysbox-test: daemon.json created with nothing free" >&2; exit 1; }

# curl is required, and a failed network listing is an error, not "all free".
mv "$bin/curl" "$bin/curl.off"
ln -s "$(command -v sh)" "$bin/sh"
run PATH="$bin"
rm "$bin/sh"
mv "$bin/curl.off" "$bin/curl"
expect 3 "curl not found"

daemon_content='{}'
run FAKE_NET_FAIL=1
expect 1 "docker network ls failed: Cannot connect to the Docker daemon"
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed after a network listing failure" >&2; exit 1; }

# The download is verified before daemon.json is touched.
run FAKE_SHA=bad
expect 1 "checksum mismatch"
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed before the checksum passed" >&2; exit 1; }
stub curl 'exit 22'
run
expect 1 "cannot download"
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json changed before the download succeeded" >&2; exit 1; }
stub curl 'out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done; echo not-a-deb >"$out"'

# An interrupted dpkg is finished before sysbox-ce is judged.
run FAKE_AUDIT=1 FAKE_INSTALLED=0.7.1.linux
expect 1 "sysbox-ce 0.7.1.linux is installed but Docker does not list sysbox-runc"
expect 1 "finishing an interrupted package install"
grep -q "dpkg --configure -a" "$log" || { echo "install-sysbox-test: dpkg --configure -a did not run" >&2; exit 1; }
run FAKE_AUDIT=1 FAKE_CONFIGURE_FAIL=1 FAKE_INSTALLED=0.7.1.linux
expect 1 "dpkg --configure -a failed"
run FAKE_INSTALLED=0.7.1.linux
expect 1 "is installed but Docker does not list sysbox-runc"
! grep -q "dpkg --configure" "$log" || { echo "install-sysbox-test: dpkg --configure ran on a clean dpkg" >&2; exit 1; }

# A probe that fails after the package registered the runtime removes it
# again; one that cannot be removed exits 4.
daemon_content='{}'
run FAKE_APT_OK=1
expect 0 "bind-mount probe passed"
grep -qF "ExecStart=/usr/bin/sysbox-mgr --disable-inner-image-preload" "$dropin" || { echo "install-sysbox-test: drop-in missing the preload flag" >&2; exit 1; }
grep -qx "ExecStart=" "$dropin" || { echo "install-sysbox-test: drop-in does not clear ExecStart" >&2; exit 1; }
grep -q "systemctl restart sysbox" "$log" || { echo "install-sysbox-test: sysbox was not restarted" >&2; exit 1; }
grep -q purge "$log" && { echo "install-sysbox-test: a passing probe purged Sysbox" >&2; exit 1; }

run FAKE_APT_OK=1 FAKE_PROBE_RC=42
expect 1 "bind mounts are not ID-mapped on this host"
expect 1 "Sysbox was removed again"
[ ! -e "$dropin" ] || { echo "install-sysbox-test: drop-in survived the purge" >&2; exit 1; }
grep -qF "apt-get -o DPkg::Lock::Timeout=300 purge -y sysbox-ce" "$log" || { echo "install-sysbox-test: the failed probe did not purge Sysbox" >&2; exit 1; }
[ ! -e "$STATE_FILE" ] || { echo "install-sysbox-test: Docker still lists sysbox-runc" >&2; exit 1; }

run FAKE_APT_OK=1 FAKE_PROBE_RC=125
expect 1 "cannot start the bind-mount probe container (exit 125)"
expect 1 "Sysbox was removed again"

run FAKE_APT_OK=1 FAKE_PROBE_RC=42 FAKE_PURGE_FAIL=1
expect 4 "apt-get purge -y sysbox-ce also failed"
expect 4 "Docker still lists sysbox-runc"

run FAKE_APT_OK=1 FAKE_PULL_FAIL=1
expect 1 "cannot pull the probe image"
grep -q purge "$log" && { echo "install-sysbox-test: a pull failure purged Sysbox" >&2; exit 1; }

run FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}' FAKE_PROBE_RC=42
expect 4 "bind mounts are not ID-mapped on this host"

# Re-running on a host that already has Sysbox adds the drop-in; a running
# container defers the restart.
run FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 0 "nothing to do"
[ -s "$dropin" ] || { echo "install-sysbox-test: re-run did not add the drop-in" >&2; exit 1; }
grep -q "systemctl restart sysbox" "$log" || { echo "install-sysbox-test: re-run did not restart sysbox" >&2; exit 1; }
run FAKE_RUNNING=1 FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}'
expect 0 "keeps its old flags"
! grep -q "systemctl restart" "$log" || { echo "install-sysbox-test: restarted sysbox under running containers" >&2; exit 1; }

# Routes in other tables count, local and broadcast entries do not.
run 'FAKE_ROUTE=10.200.0.0/16 dev wg0 table 100'
expect 1 "apt-get install"
chosen "10.201.0.1/16 10.202.0.0/16"
run FAKE_LOCAL=10.200.0.1
chosen "10.200.0.1/16 10.201.0.0/16"

daemon_content='{}'
run FAKE_ARCH=arm64 FAKE_SHA=bad
expect 1 "checksum mismatch for sysbox-ce_0.7.1.linux_arm64.deb"
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran after a checksum mismatch" >&2; exit 1; }

echo "install-sysbox-test: ok"
