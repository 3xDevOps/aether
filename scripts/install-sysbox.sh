#!/bin/sh
# Installs the pinned Sysbox CE release on an Ubuntu or Debian host that
# already runs Docker, so Aether runs can start their own Docker daemon.
# Run as root:
#
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install-sysbox.sh | sudo sh
#
# The package registers the sysbox-runc runtime with Docker. Its installer
# refuses to run while any container exists, stopped ones included, if it must
# restart Docker, and it must when /etc/docker/daemon.json lacks "bip" or
# "default-address-pools". This script adds whichever is missing itself, with
# the first /16 that overlaps no route, interface or Docker network, so the
# installer only reloads Docker's runtimes. It never changes a key that is set.
# Docker applies the new keys at its next restart; running containers keep
# running. It then checks that bind mounts are ID-mapped, which Sysbox needs
# and which takes Linux 5.19 or newer, or the shiftfs module.
#
# On a host where Ubuntu enforces an AppArmor profile for fusermount3, it also
# adds a narrow override to /etc/apparmor.d/local/fusermount3 so Sysbox's
# filesystem can mount under /var/lib/sysboxfs. A failure after the first host
# change purges the package and undoes the AppArmor and daemon.json edits.
#
# Exit status: 0 installed, 3 the host cannot run Sysbox (not Debian or
# Ubuntu, unsupported architecture, no ID-mapped mounts, no Docker), 4 Sysbox
# is registered with Docker but failed the bind-mount probe and could not be
# removed, 1 any other failure.
set -eu

VERSION="0.7.1"
SHA256_AMD64="9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5"
SHA256_ARM64="04ca894ae0b53f0fa54eaacc173ce40363c9a95ea5450f773716a84ef650a69b"

# Test seams for install-sysbox-test.sh. They are not documented options.
BASE_URL="${AETHER_SYSBOX_BASE_URL:-https://github.com/nestybox/sysbox/releases/download/v${VERSION}}"
DAEMON_JSON="${AETHER_SYSBOX_DAEMON_JSON:-/etc/docker/daemon.json}"

AA_DIR="${AETHER_SYSBOX_APPARMOR_DIR:-/etc/apparmor.d}"
AA_PROFILES="${AETHER_SYSBOX_APPARMOR_PROFILES:-/sys/kernel/security/apparmor/profiles}"
aa_profile="$AA_DIR/fusermount3"
aa_local="$AA_DIR/local/fusermount3"
aa_begin="# BEGIN aether sysbox"
aa_end="# END aether sysbox"

tmp=
tmp_json=
fail_msg=
rolling=no
daemon_edited=no
daemon_existed=no
aa_added=no
aa_created=no
pkg_attempted=no

# Whether daemon.json differs from the script's own edit only by the
# sysbox-runc runtime entry that the package's postinst adds and postrm removes.
daemon_json_unchanged() {
	filter='del(.runtimes["sysbox-runc"]) | if (.runtimes // {}) == {} then del(.runtimes) else . end'
	now="$(jq -S -c "$filter" "$DAEMON_JSON" 2>/dev/null)" || return 1
	wrote="$(jq -S -c "$filter" "$tmp_json")" || return 1
	[ "$now" = "$wrote" ]
}

rollback() {
	[ "$rolling" = no ] || return 0
	rolling=yes
	if [ "$pkg_attempted" = yes ] && dpkg-query -W sysbox-ce >/dev/null 2>&1; then
		say "removing Sysbox again: apt-get purge -y sysbox-ce"
		apt-get -o DPkg::Lock::Timeout=300 purge -y sysbox-ce || broken "${fail_msg}; apt-get purge -y sysbox-ce also failed"
		tries=0
		while runtimes | grep -q '"sysbox-runc"'; do
			tries=$((tries + 1))
			[ "$tries" -lt 15 ] || broken "${fail_msg}; Docker still lists sysbox-runc after the purge"
			sleep 1
		done
		say "Sysbox was removed again"
	fi
	if [ "$aa_added" = yes ]; then
		sed "/^${aa_begin}\$/,/^${aa_end}\$/d" "$aa_local" >"$tmp/aa.local"
		if [ "$aa_created" = yes ] && [ ! -s "$tmp/aa.local" ]; then
			rm -f "$aa_local"
		else
			cat "$tmp/aa.local" >"$aa_local"
		fi
		say "removed the AppArmor override from ${aa_local}"
		aa_out="$(apparmor_parser -r "$aa_profile" 2>&1)" || echo "install-sysbox: cannot reload ${aa_profile}: ${aa_out}" >&2
	fi
	if [ "$daemon_edited" = yes ]; then
		if [ -e "$DAEMON_JSON" ] && ! daemon_json_unchanged; then
			say "left ${DAEMON_JSON} as it is: it changed beyond the Sysbox runtime entry after this script edited it"
		elif [ "$daemon_existed" = yes ]; then
			cat "$tmp/daemon.orig" >"$DAEMON_JSON"
			say "restored ${DAEMON_JSON} to its original content"
		else
			rm -f "$DAEMON_JSON"
			say "removed ${DAEMON_JSON}, which did not exist before"
		fi
	fi
}

die() {
	echo "install-sysbox: $*" >&2
	fail_msg="$*"
	rollback
	exit 1
}

unsupported() {
	echo "install-sysbox: $*" >&2
	exit 3
}

say() {
	echo "install-sysbox: $*"
}

[ "$(id -u)" -eq 0 ] || die "run as root: sudo sh install-sysbox.sh"

for tool in dpkg apt-get docker uname ip curl; do
	command -v "$tool" >/dev/null 2>&1 || unsupported "${tool} not found; this script needs an Ubuntu or Debian host with Docker installed"
done

case "$(dpkg --print-architecture)" in
amd64) want_sha="$SHA256_AMD64" ;;
arm64) want_sha="$SHA256_ARM64" ;;
*) unsupported "unsupported architecture $(dpkg --print-architecture); Sysbox publishes amd64 and arm64 packages" ;;
esac
deb="sysbox-ce_${VERSION}.linux_$(dpkg --print-architecture).deb"

kernel="$(uname -r)"
kernel_major="${kernel%%.*}"
kernel_minor="${kernel#*.}"
kernel_minor="${kernel_minor%%[!0-9]*}"
if [ "$kernel_major" -lt 5 ] || { [ "$kernel_major" -eq 5 ] && [ "${kernel_minor:-0}" -lt 19 ]; }; then
	command -v modinfo >/dev/null 2>&1 && modinfo shiftfs >/dev/null 2>&1 ||
		unsupported "kernel ${kernel} cannot ID-map bind mounts, which Sysbox needs: use Linux 5.19 or newer, or a kernel with the shiftfs module"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
else
	die "need sha256sum to verify the download"
fi

runtimes() { docker info --format '{{json .Runtimes}}'; }

runtimes_out="$(runtimes 2>&1)" || die "docker info failed: ${runtimes_out}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

# Sets probe_reason and returns 1 when a container on sysbox-runc cannot write
# a host directory it owns, which means bind mounts are not ID-mapped. A failed
# image pull says nothing about Sysbox and dies instead.
probe_once() {
	probe="$tmp/probe"
	mkdir -p "$probe"
	chown 1000:1000 "$probe"
	pull_out="$(docker pull busybox:1.36 2>&1)" || die "cannot pull the probe image busybox:1.36: ${pull_out}"
	probe_rc=0
	probe_out="$(docker run --rm --runtime sysbox-runc -u 1000:1000 -v "$probe:/m" busybox:1.36 sh -c 'touch /m/probe || exit 42' 2>&1)" || probe_rc=$?
	case "$probe_rc" in
	0) return 0 ;;
	42) probe_reason="bind mounts are not ID-mapped on this host: a container user cannot write a host directory it owns: ${probe_out}" ;;
	*) probe_reason="cannot start the bind-mount probe container (exit ${probe_rc}): ${probe_out}" ;;
	esac
	case "$probe_out" in
	*"pre-register with sysbox-fs"*)
		if command -v journalctl >/dev/null 2>&1; then
			probe_reason="${probe_reason}
sysbox-fs log (journalctl -u sysbox-fs --no-pager -n 20):
$(journalctl -u sysbox-fs --no-pager -n 20 2>&1)"
		fi
		;;
	esac
	return 1
}

# One retry covers the intermittent "FuseServer InitWait error" under
# concurrent container creation (sysbox #1044).
run_probe() {
	probe_once && return 0
	sleep 3
	probe_once
}

# Sysbox is registered but unusable, and this script is not removing it.
broken() {
	echo "install-sysbox: $1. Docker still lists sysbox-runc; remove it with: sudo apt-get purge -y sysbox-ce" >&2
	exit 4
}

# Already installed: the probe still decides, so a retry cannot report success
# for a Sysbox that failed it.
already_installed() {
	run_probe || broken "$probe_reason"
	say "Sysbox ${VERSION} is installed and Docker lists sysbox-runc; nothing to do"
	exit 0
}

installed="$(dpkg-query -W -f='${Version}' sysbox-ce 2>/dev/null || true)"
case "$runtimes_out" in
*'"sysbox-runc"'*)
	if [ "$installed" = "${VERSION}.linux" ]; then
		already_installed
	fi
	die "Docker already lists sysbox-runc (sysbox-ce ${installed:-not installed from a package}). Upgrade by following https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install-package.md#upgrading-sysbox-or-sysbox-enterprise"
	;;
esac
# A server restart or a lost session can interrupt dpkg mid-install, leaving
# sysbox-ce half configured; finish that before judging what is installed.
if [ -n "$(dpkg --audit 2>&1)" ]; then
	say "finishing an interrupted package install: dpkg --configure -a"
	dpkg --configure -a || die "dpkg --configure -a failed; its output is above"
	runtimes_out="$(runtimes 2>&1)" || die "docker info failed: ${runtimes_out}"
	installed="$(dpkg-query -W -f='${Version}' sysbox-ce 2>/dev/null || true)"
	case "$runtimes_out" in
	*'"sysbox-runc"'*)
		if [ "$installed" = "${VERSION}.linux" ]; then
			already_installed
		fi
		;;
	esac
fi
[ -z "$installed" ] || die "sysbox-ce ${installed} is installed but Docker does not list sysbox-runc. Run: systemctl restart docker, or purge it with: apt-get purge -y sysbox-ce"

apt_updated=no
apt_install() {
	if [ "$apt_updated" = no ]; then
		apt-get -o DPkg::Lock::Timeout=300 update || die "apt-get update failed; its output is above"
		apt_updated=yes
	fi
	apt-get -o DPkg::Lock::Timeout=300 install -y "$@" || die "apt-get install $* failed; its output is above"
}

command -v jq >/dev/null 2>&1 || apt_install jq

tmp_json="$tmp/daemon.json"

# Download and verify before touching daemon.json, so an offline host or a bad
# download leaves it as it was.
say "downloading ${deb}"
curl -fsSL -o "$tmp/$deb" "${BASE_URL}/${deb}" || die "cannot download ${BASE_URL}/${deb}"
got="$(sha256_of "$tmp/$deb")"
[ "$got" = "$want_sha" ] || die "checksum mismatch for ${deb}: expected ${want_sha}, got ${got}"
# Every IPv4 network on the host's routes, in every routing table, and on its
# interfaces, from the output of ip route and ip address. A route without a
# prefix length is a host route; local and broadcast entries only repeat an
# interface address.
local_networks() {
	echo "$routes" | awk '$1 ~ /^(local|broadcast|multicast|anycast)$/ {next} {net = $1} net ~ /^(unicast|blackhole|unreachable|prohibit|throw)$/ {net = $2} net ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(\/[0-9]+)?$/ {print net}'
	echo "$addrs" | awk '{print $4}'
}

# The IPv4 subnet of every Docker network, running or not. It runs in a
# subshell, so callers check its status.
docker_networks() {
	ids="$(docker network ls -q 2>&1)" || die "docker network ls failed: ${ids}"
	[ -n "$ids" ] || return 0
	# shellcheck disable=SC2086
	nets="$(docker network inspect $ids --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>&1)" || die "docker network inspect failed: ${nets}"
	echo "$nets" | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$' || true
}

ipv4_to_int() {
	old_ifs=$IFS
	IFS=.
	set -- $1
	IFS=$old_ifs
	echo $(($1 << 24 | $2 << 16 | $3 << 8 | $4))
}

# Two networks overlap when they agree on their first min(len1, len2) bits.
networks_overlap() {
	len_a=32
	len_b=32
	case "$1" in */*) len_a="${1#*/}" ;; esac
	case "$2" in */*) len_b="${2#*/}" ;; esac
	common=$((len_a < len_b ? len_a : len_b))
	[ "$common" -gt 0 ] || return 0
	shift_by=$((32 - common))
	[ $(($(ipv4_to_int "${1%/*}") >> shift_by)) -eq $(($(ipv4_to_int "${2%/*}") >> shift_by)) ]
}

overlaps_used() {
	while read -r network; do
		[ -n "$network" ] && networks_overlap "$network" "$1" && return 0
	done <<EOT
$used_networks
EOT
	return 1
}

# 172.17-31 is Docker's default pool until Docker restarts and applies the
# new keys, so a network created in between could take a 172.24-31 range and
# collide with the new bip. 10.200-254 is outside it and comes first; 192.168
# is the last resort.
candidates() {
	n=200
	while [ "$n" -le 254 ]; do
		echo "10.$n.0.0/16"
		n=$((n + 1))
	done
	for n in 24 25 26 27 28 29 30 31; do echo "172.$n.0.0/16"; done
	echo "192.168.0.0/16"
}

# Sets picked to the first candidate that overlaps nothing in use, then marks
# it in use so the next pick differs.
pick_network() {
	while read -r candidate; do
		overlaps_used "$candidate" && continue
		picked="$candidate"
		used_networks="${used_networks}
${candidate}"
		return 0
	done <<EOT
$(candidates)
EOT
	return 1
}

add_bip=false
add_pool=false
jq -e '(.bip // "") != ""' "$DAEMON_JSON" >/dev/null 2>&1 || add_bip=true
jq -e '((."default-address-pools" // []) | length) > 0' "$DAEMON_JSON" >/dev/null 2>&1 || add_pool=true

BIP=
POOL_BASE=
if [ "$add_bip" = true ] || [ "$add_pool" = true ]; then
	routes="$(ip -4 -o route show table all 2>&1)" || die "ip route failed: ${routes}"
	addrs="$(ip -4 -o address 2>&1)" || die "ip address failed: ${addrs}"
	docker_nets="$(docker_networks)" || exit 1
	local_nets="$(local_networks)"
	set_nets="$(jq -r '.bip // empty, ((."default-address-pools" // [])[].base)' "$DAEMON_JSON" 2>/dev/null || true)"
	used_networks="${local_nets}
${docker_nets}
${set_nets}"
	none_free="no free /16 among 10.200-254.0.0, 172.24-31.0.0 and 192.168.0.0 that avoids this host's routes, interfaces and Docker networks"
	if [ "$add_bip" = true ]; then
		pick_network || die "cannot add \"bip\" to ${DAEMON_JSON}: ${none_free}. Set it to a free range, then re-run this script."
		BIP="${picked%/*}"
		BIP="${BIP%.0}.1/16"
	fi
	if [ "$add_pool" = true ]; then
		pick_network || die "cannot add \"default-address-pools\" to ${DAEMON_JSON}: ${none_free}. Set it to a free range, then re-run this script."
		POOL_BASE="$picked"
	fi
fi

if [ "$add_bip" = true ] || [ "$add_pool" = true ]; then
	source_json="$DAEMON_JSON"
	if [ ! -s "$DAEMON_JSON" ]; then
		echo '{}' >"$tmp/empty.json"
		source_json="$tmp/empty.json"
	fi
	jq --indent 4 --arg bip "$BIP" --arg base "$POOL_BASE" --argjson add_bip "$add_bip" --argjson add_pool "$add_pool" \
		'(if $add_bip then .bip = $bip else . end) | (if $add_pool then ."default-address-pools" = [{"base": $base, "size": 24}] else . end)' \
		"$source_json" >"$tmp_json" || die "cannot edit ${DAEMON_JSON}"
	if [ -e "$DAEMON_JSON" ]; then
		cp "$DAEMON_JSON" "$tmp/daemon.orig"
		daemon_existed=yes
	fi
	daemon_edited=yes
	mkdir -p "$(dirname "$DAEMON_JSON")"
	cat "$tmp_json" >"$DAEMON_JSON"
	[ "$add_bip" = false ] || say "added \"bip\": \"${BIP}\" to ${DAEMON_JSON}"
	[ "$add_pool" = false ] || say "added \"default-address-pools\": [{\"base\": \"${POOL_BASE}\", \"size\": 24}] to ${DAEMON_JSON}"
	say "Docker applies these at its next restart; this script does not restart it, and running containers keep running"
fi

# Ubuntu 25.04 and later enforce an AppArmor profile that lets fusermount3
# mount only under home, /mnt, /run/user, /media, /tmp and /cvmfs, which blocks
# sysbox-fs (sysbox #947, #1005).
if [ -f "$aa_profile" ] && grep -q 'fusermount3 (enforce)' "$AA_PROFILES" 2>/dev/null &&
	! grep -qF "$aa_begin" "$aa_local" 2>/dev/null; then
	fuse_types='@{fuse_types}'
	grep -qF '@{fuse_types}' "$aa_profile" || fuse_types='fuse.*'
	mkdir -p "$(dirname "$aa_local")"
	[ -e "$aa_local" ] || aa_created=yes
	aa_added=yes
	if [ -s "$aa_local" ] && [ -n "$(tail -c1 "$aa_local")" ]; then
		echo >>"$aa_local"
	fi
	cat >>"$aa_local" <<EOT
${aa_begin}
mount fstype=${fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
mount fstype=${fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
umount /var/lib/sysboxfs/**/,
${aa_end}
EOT
	aa_out="$(apparmor_parser -r "$aa_profile" 2>&1)" || die "apparmor_parser -r ${aa_profile} failed: ${aa_out}"
	say "added mount rules to ${aa_local}: lets Sysbox's filesystem mount under /var/lib/sysboxfs; Ubuntu's fusermount3 profile blocks it otherwise"
fi

say "installing ${deb}"
pkg_attempted=yes
apt_install "$tmp/$deb"

tries=0
until runtimes | grep -q '"sysbox-runc"'; do
	tries=$((tries + 1))
	[ "$tries" -lt 15 ] || die "Docker does not list sysbox-runc after the install; docker info: $(runtimes 2>&1). Try: systemctl restart docker"
	sleep 1
done

say "Docker lists sysbox-runc"

run_probe || die "${probe_reason}"
say "bind-mount probe passed"
echo
echo "New runs and terminals use Sysbox within a minute. Existing containers keep their runtime."
echo "A server that predates automatic Sysbox detection needs: sudo systemctl restart aether-server"
