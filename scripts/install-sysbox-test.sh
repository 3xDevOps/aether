#!/bin/sh
# Exercises scripts/install-sysbox.sh with stubbed id, dpkg, dpkg-query,
# uname, modinfo, ip, docker, apt-get, curl, sha256sum, sleep, journalctl and
# apparmor_parser. Nothing is
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
stub dpkg-query 'v="${FAKE_INSTALLED:-}"; [ -n "$v" ] || { [ ! -e "$STATE_FILE" ] || v=0.7.1.linux; }; printf "%s" "$v"; [ -n "$v" ]'
stub uname 'echo "${FAKE_KERNEL:-6.8.0-45-generic}"'
stub modinfo '[ -n "${FAKE_SHIFTFS:-}" ]'
stub ip 'case "$*" in
"-4 -o route show table all") echo "default via 10.0.0.1 dev eth0"; echo "10.0.0.0/24 dev eth0 scope link"; echo "local 10.0.0.5 dev eth0 table local"; echo "broadcast 10.0.0.255 dev eth0 table local"; [ -z "${FAKE_LOCAL:-}" ] || echo "local $FAKE_LOCAL dev lo table local"; [ -z "${FAKE_ROUTE:-}" ] || echo "$FAKE_ROUTE dev eth0" ;;
"-4 -o address") echo "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0"; [ -z "${FAKE_ADDR:-}" ] || echo "5: br0    inet $FAKE_ADDR scope global br0" ;;
esac'
stub docker 'case "$1" in
info) if [ -n "${FAKE_RUNTIMES:-}" ]; then echo "$FAKE_RUNTIMES"; elif [ -e "$STATE_FILE" ]; then echo "{\"runc\":{},\"sysbox-runc\":{}}"; else echo "{\"runc\":{}}"; fi ;;
pull) [ -z "${FAKE_PULL_FAIL:-}" ] || { echo "pull access denied"; exit 1; } ;;
run) if [ -n "${FAKE_PROBE_FLAKY:-}" ] && [ ! -e "$STATE_FILE.flaky" ]; then touch "$STATE_FILE.flaky"; echo "FuseServer InitWait error"; exit 125; fi
	[ -z "${FAKE_PROBE_OUT:-}" ] || echo "$FAKE_PROBE_OUT"; exit "${FAKE_PROBE_RC:-0}" ;;
network) case "$2" in
	ls) [ -z "${FAKE_NET_FAIL:-}" ] || { echo "Cannot connect to the Docker daemon"; exit 1; }; echo abc ;;
	inspect) echo "172.17.0.0/16 ${FAKE_DOCKER_NETS:-} " ;;
	esac ;;
esac'
stub apt-get 'echo "apt-get $*" >>"$CMD_LOG"; case "$*" in
*.deb*) [ -n "${FAKE_APT_OK:-}" ] || { echo "E: Unmet dependencies"; exit 100; }; touch "$STATE_FILE"
	[ -z "${FAKE_APT_RUNTIME:-}" ] || { jq ".runtimes[\"sysbox-runc\"] = {}" "$AETHER_SYSBOX_DAEMON_JSON" >"$STATE_FILE.json" && cat "$STATE_FILE.json" >"$AETHER_SYSBOX_DAEMON_JSON"; }
	[ -z "${FAKE_APT_EDIT:-}" ] || { jq ".unrelated = 1" "$AETHER_SYSBOX_DAEMON_JSON" >"$STATE_FILE.json" && cat "$STATE_FILE.json" >"$AETHER_SYSBOX_DAEMON_JSON"; } ;;
*purge*) [ -z "${FAKE_PURGE_FAIL:-}" ] || { echo "E: dpkg was interrupted"; exit 100; }; rm -f "$STATE_FILE"
	[ -z "${FAKE_APT_RUNTIME:-}" ] || { jq "del(.runtimes)" "$AETHER_SYSBOX_DAEMON_JSON" >"$STATE_FILE.json" && cat "$STATE_FILE.json" >"$AETHER_SYSBOX_DAEMON_JSON"; } ;;
esac'
stub sha256sum 'case "${FAKE_ARCH:-amd64}" in arm64) h=04ca894ae0b53f0fa54eaacc173ce40363c9a95ea5450f773716a84ef650a69b ;; *) h=9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5 ;; esac; echo "${FAKE_SHA:-$h}  $1"'
stub curl 'out=; while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done; echo not-a-deb >"$out"'
stub sleep 'echo "sleep $*" >>"$CMD_LOG"'
stub journalctl 'echo "sysbox-fs: fusermount3: mount failed: Permission denied"'
stub apparmor_parser 'echo "apparmor_parser $*" >>"$CMD_LOG"; [ -z "${FAKE_AA_FAIL:-}" ] || { echo "AppArmor parser error: syntax"; exit 1; }'
stub jq 'exec "$REAL_JQ" "$@"'
REAL_JQ=$(command -v jq) || { echo "install-sysbox-test: jq is required" >&2; exit 1; }
export REAL_JQ CMD_LOG="$log" STATE_FILE="$tmp/sysbox-registered"

daemon_json="$tmp/daemon.json"
daemon_content='{}'
aa_dir="$tmp/apparmor.d"
aa_profiles="$tmp/aa-profiles"
echo "docker-default (enforce)" >"$aa_profiles"

run() {
	: >"$log"
	rm -f "$STATE_FILE" "$STATE_FILE.flaky"
	rm -rf "$aa_dir"
	printf '%s\n' "$daemon_content" >"$daemon_json"
	if env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="$daemon_json" AETHER_SYSBOX_APPARMOR_DIR="$aa_dir" AETHER_SYSBOX_APPARMOR_PROFILES="$aa_profiles" "$@" sh "$script" >"$tmp/out" 2>&1; then
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
run FAKE_APT_OK=1
expect 0 "bind-mount probe passed"
expect 0 "added \"default-address-pools\""
want='{"log-level":"warn","bip":"10.9.0.1/24","default-address-pools":[{"base":"10.200.0.0/16","size":24}]}'
[ "$(jq -c . "$daemon_json")" = "$want" ] || { echo "install-sysbox-test: daemon.json is $(jq -c . "$daemon_json"), want $want" >&2; exit 1; }
expect 0 "running containers keep running"

daemon_content='{"bip": "10.9.0.1/24", "default-address-pools": [{"base": "10.10.0.0/16", "size": 24}]}'
run FAKE_APT_OK=1
expect 0 "bind-mount probe passed"
[ "$(jq -c . "$daemon_json")" = "$(printf '%s' "$daemon_content" | jq -c .)" ] || { echo "install-sysbox-test: existing keys were rewritten" >&2; exit 1; }

daemon_content='{}'
run FAKE_APT_OK=1
expect 0 "added \"bip\": \"10.200.0.1/16\""
expect 0 "added \"default-address-pools\": [{\"base\": \"10.201.0.0/16\""
[ "$(jq -r '.bip, .["default-address-pools"][0].base' "$daemon_json" | tr '\n' ' ')" = "10.200.0.1/16 10.201.0.0/16 " ] || { echo "install-sysbox-test: empty daemon.json not filled in" >&2; exit 1; }

rm -f "$daemon_json"
mkdir -p "$tmp/etc"
run FAKE_APT_OK=1 AETHER_SYSBOX_DAEMON_JSON="$tmp/etc/new.json"
expect 0 "bind-mount probe passed"
[ -s "$tmp/etc/new.json" ] || { echo "install-sysbox-test: missing daemon.json not created" >&2; exit 1; }

chosen() {
	got="$(jq -r '.bip, .["default-address-pools"][0].base' "$daemon_json" | tr '\n' ' ')"
	[ "$got" = "$1 " ] || { echo "install-sysbox-test: chose $got, want $1" >&2; exit 1; }
}

# A route, an interface address and a Docker network each take their /16 out.
run FAKE_APT_OK=1 FAKE_ROUTE=10.200.0.0/16
expect 0 "bind-mount probe passed"
chosen "10.201.0.1/16 10.202.0.0/16"

run FAKE_APT_OK=1 FAKE_ROUTE=10.201.5.0/24
chosen "10.200.0.1/16 10.202.0.0/16"

run FAKE_APT_OK=1 FAKE_ADDR=10.200.9.1/24
chosen "10.201.0.1/16 10.202.0.0/16"

run FAKE_APT_OK=1 FAKE_DOCKER_NETS="10.200.0.0/16 10.201.0.0/16"
chosen "10.202.0.1/16 10.203.0.0/16"

run FAKE_APT_OK=1 'FAKE_ROUTE=blackhole 10.200.0.0/16'
chosen "10.201.0.1/16 10.202.0.0/16"

# A /8 takes out every 10.200-254 candidate; 172.24-31 comes next.
run FAKE_APT_OK=1 'FAKE_ROUTE=10.0.0.0/8 via 10.0.0.1'
expect 0 "bind-mount probe passed"
chosen "172.24.0.1/16 172.25.0.0/16"

run FAKE_APT_OK=1 FAKE_DOCKER_NETS="10.0.0.0/8 172.24.0.0/16"
chosen "172.25.0.1/16 172.26.0.0/16"

# Only the missing key is chosen, and never the existing key's network.
daemon_content='{"bip": "10.200.0.1/16"}'
run FAKE_APT_OK=1
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
grep -q purge "$log" && { echo "install-sysbox-test: a passing probe purged Sysbox" >&2; exit 1; }

run FAKE_APT_OK=1 FAKE_PROBE_RC=42
expect 1 "bind mounts are not ID-mapped on this host"
expect 1 "Sysbox was removed again"
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
grep -q purge "$log" || { echo "install-sysbox-test: a pull failure left Sysbox installed" >&2; exit 1; }

run FAKE_INSTALLED=0.7.1.linux 'FAKE_RUNTIMES={"runc":{},"sysbox-runc":{}}' FAKE_PROBE_RC=42
expect 4 "bind mounts are not ID-mapped on this host"

# Routes in other tables count, local and broadcast entries do not.
run FAKE_APT_OK=1 'FAKE_ROUTE=10.200.0.0/16 dev wg0 table 100'
expect 0 "bind-mount probe passed"
chosen "10.201.0.1/16 10.202.0.0/16"
run FAKE_APT_OK=1 FAKE_LOCAL=10.200.0.1
chosen "10.200.0.1/16 10.201.0.0/16"

daemon_content='{}'
run FAKE_ARCH=arm64 FAKE_SHA=bad
expect 1 "checksum mismatch for sysbox-ce_0.7.1.linux_arm64.deb"
[ ! -s "$log" ] || { echo "install-sysbox-test: apt-get ran after a checksum mismatch" >&2; exit 1; }

# fusermount3 profile in enforce mode: the override is added once, the
# profile reloaded, and every other line of the local file kept.
echo "fusermount3 (enforce)" >>"$aa_profiles"
aa_local="$aa_dir/local/fusermount3"
setup_profile() {
	mkdir -p "$aa_dir/local"
	echo "profile fusermount3 { @{fuse_types} }" >"$aa_dir/fusermount3"
}
run_with_profile() {
	: >"$log"
	rm -f "$STATE_FILE" "$STATE_FILE.flaky"
	rm -rf "$aa_dir"
	setup_profile
	[ -z "${aa_prior:-}" ] || printf '%s' "$aa_prior" >"$aa_local"
	printf '%s\n' "$daemon_content" >"$daemon_json"
	if env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="${DJ:-$daemon_json}" AETHER_SYSBOX_APPARMOR_DIR="$aa_dir" AETHER_SYSBOX_APPARMOR_PROFILES="$aa_profiles" "$@" sh "$script" >"$tmp/out" 2>&1; then
		status=0
	else
		status=$?
	fi
}
aa_prior='# keep me
other rule,
'
run_with_profile FAKE_APT_OK=1
expect 0 "lets Sysbox's filesystem mount under /var/lib/sysboxfs; Ubuntu's fusermount3 profile blocks it otherwise"
grep -qF 'mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,' "$aa_local" || { echo "install-sysbox-test: rw rule missing" >&2; exit 1; }
grep -qF 'mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,' "$aa_local" || { echo "install-sysbox-test: ro rule missing" >&2; exit 1; }
grep -qF 'umount /var/lib/sysboxfs/**/,' "$aa_local" || { echo "install-sysbox-test: umount rule missing" >&2; exit 1; }
[ "$(head -2 "$aa_local")" = "# keep me
other rule," ] || { echo "install-sysbox-test: existing local lines changed" >&2; exit 1; }
grep -qF "apparmor_parser -r $aa_dir/fusermount3" "$log" || { echo "install-sysbox-test: profile not reloaded" >&2; exit 1; }
cp "$aa_local" "$tmp/aa.once"
# Re-run on the same host state: the markers are present, so nothing is added.
: >"$log"
env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="$daemon_json" AETHER_SYSBOX_APPARMOR_DIR="$aa_dir" AETHER_SYSBOX_APPARMOR_PROFILES="$aa_profiles" FAKE_APT_OK=1 sh "$script" >"$tmp/out" 2>&1 || true
cmp -s "$aa_local" "$tmp/aa.once" || { echo "install-sysbox-test: re-run changed the local file" >&2; exit 1; }
! grep -q apparmor_parser "$log" || { echo "install-sysbox-test: re-run reloaded the profile" >&2; exit 1; }

# An older profile without @{fuse_types} gets fuse.* rules.
aa_prior=
run_with_profile FAKE_APT_OK=1
echo "profile fusermount3 { }" >"$aa_dir/fusermount3"
rm -f "$aa_local"
rm -f "$STATE_FILE"
env PATH="$bin:$PATH" AETHER_SYSBOX_DAEMON_JSON="$daemon_json" AETHER_SYSBOX_APPARMOR_DIR="$aa_dir" AETHER_SYSBOX_APPARMOR_PROFILES="$aa_profiles" FAKE_APT_OK=1 sh "$script" >"$tmp/out" 2>&1 || true
grep -qF 'mount fstype=fuse.* options=(nosuid,nodev,rw)' "$aa_local" || { echo "install-sysbox-test: old profile got no fuse.* rule" >&2; exit 1; }

# No profile, or one that is not enforced: nothing is added.
run FAKE_APT_OK=1
expect 0 "bind-mount probe passed"
[ ! -e "$aa_dir" ] || { echo "install-sysbox-test: AppArmor dir created without a profile" >&2; exit 1; }
echo "docker-default (enforce)" >"$aa_profiles.other"
echo "fusermount3 (complain)" >>"$aa_profiles.other"
AA_SAVED="$aa_profiles"
aa_profiles="$aa_profiles.other"
run_with_profile FAKE_APT_OK=1
expect 0 "bind-mount probe passed"
[ ! -e "$aa_local" ] || { echo "install-sysbox-test: override added to a profile in complain mode" >&2; exit 1; }
aa_profiles="$AA_SAVED"

# apparmor_parser fails: the script dies before installing the package and
# takes the override out again.
aa_prior=
run_with_profile FAKE_APT_OK=1 FAKE_AA_FAIL=1
expect 1 "apparmor_parser -r $aa_dir/fusermount3 failed: AppArmor parser error: syntax"
expect 1 "removed the AppArmor override"
! grep -q 'apt-get.*install' "$log" || { echo "install-sysbox-test: package installed after apparmor_parser failed" >&2; exit 1; }
[ ! -e "$aa_local" ] || { echo "install-sysbox-test: override left after apparmor_parser failed" >&2; exit 1; }

# A failed probe undoes everything: package, override, daemon.json.
aa_prior='# keep me
'
daemon_content='{ "log-level":	"warn" }
'
printf '%s\n' "$daemon_content" >"$tmp/want.json"
run_with_profile FAKE_APT_OK=1 FAKE_APT_RUNTIME=1 FAKE_PROBE_RC=42
expect 1 "Sysbox was removed again"
expect 1 "removed the AppArmor override from $aa_local"
expect 1 "restored $daemon_json to its original content"
cmp -s "$daemon_json" "$tmp/want.json" || { echo "install-sysbox-test: daemon.json not restored byte for byte" >&2; exit 1; }
[ "$(cat "$aa_local")" = "# keep me" ] || { echo "install-sysbox-test: override block not removed cleanly" >&2; exit 1; }
[ ! -e "$STATE_FILE" ] || { echo "install-sysbox-test: package not purged" >&2; exit 1; }
[ "$(grep -c "apparmor_parser" "$log")" -eq 2 ] || { echo "install-sysbox-test: profile not reloaded after removal" >&2; exit 1; }
grep -q 'purge -y sysbox-ce' "$log" || { echo "install-sysbox-test: no purge" >&2; exit 1; }

# The file did not exist: it is removed. The local file did not exist either.
aa_prior=
DJ="$tmp/etc/gone.json"
rm -f "$DJ"
run_with_profile FAKE_APT_OK=1 FAKE_PROBE_RC=42
expect 1 "removed $DJ, which did not exist before"
[ ! -e "$DJ" ] || { echo "install-sysbox-test: daemon.json created by a failed install was kept" >&2; exit 1; }
[ ! -e "$aa_local" ] || { echo "install-sysbox-test: created local file kept" >&2; exit 1; }
unset DJ

# A daemon.json edited by someone else in between is left alone.
daemon_content='{}'
run_with_profile FAKE_APT_OK=1 FAKE_APT_EDIT=1 FAKE_PROBE_RC=42
expect 1 "left $daemon_json as it is"
[ "$(jq -r .unrelated "$daemon_json")" = 1 ] || { echo "install-sysbox-test: unrelated edit was clobbered" >&2; exit 1; }

# An install failure before the package registers also restores daemon.json.
run_with_profile
expect 1 "apt-get install"
[ "$(jq -c . "$daemon_json")" = '{}' ] || { echo "install-sysbox-test: daemon.json not restored after a failed install" >&2; exit 1; }

# The sysbox-fs pre-register error shows the service log.
run FAKE_APT_OK=1 FAKE_PROBE_RC=125 'FAKE_PROBE_OUT=OCI runtime create failed: failed to pre-register with sysbox-fs'
expect 1 "sysbox-fs: fusermount3: mount failed: Permission denied"

# The probe is retried once after 3 seconds: a flaky first attempt passes.
run FAKE_APT_OK=1 FAKE_PROBE_FLAKY=1
expect 0 "bind-mount probe passed"
grep -qx "sleep 3" "$log" || { echo "install-sysbox-test: the probe was not retried after 3 seconds" >&2; exit 1; }
run FAKE_APT_OK=1 FAKE_PROBE_RC=125
expect 1 "cannot start the bind-mount probe container"
[ "$(grep -cx "sleep 3" "$log")" -eq 1 ] || { echo "install-sysbox-test: the probe was retried more than once" >&2; exit 1; }

echo "install-sysbox-test: ok"
