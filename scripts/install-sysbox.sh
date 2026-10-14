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
# "default-address-pools". This script adds those two keys itself, with the
# values Sysbox would choose, so the installer only reloads Docker's runtimes.
# Docker applies the new keys at its next restart; running containers keep
# running. It then checks that bind mounts are ID-mapped, which Sysbox needs
# and which takes Linux 5.19 or newer, or the shiftfs module.
set -eu

VERSION="0.7.1"
SHA256_AMD64="9d6d5484f980d0a17f86c492c1262015c2afb66280bdb97215b79fde6a0261c5"
SHA256_ARM64="04ca894ae0b53f0fa54eaacc173ce40363c9a95ea5450f773716a84ef650a69b"

# Test seams for install-sysbox-test.sh. They are not documented options.
BASE_URL="${AETHER_SYSBOX_BASE_URL:-https://github.com/nestybox/sysbox/releases/download/v${VERSION}}"
DAEMON_JSON="${AETHER_SYSBOX_DAEMON_JSON:-/etc/docker/daemon.json}"

die() {
	echo "install-sysbox: $*" >&2
	exit 1
}

say() {
	echo "install-sysbox: $*"
}

[ "$(id -u)" -eq 0 ] || die "run as root: sudo sh install-sysbox.sh"

for tool in dpkg apt-get docker uname ip; do
	command -v "$tool" >/dev/null 2>&1 || die "${tool} not found; this script needs an Ubuntu or Debian host with Docker installed"
done

case "$(dpkg --print-architecture)" in
amd64) want_sha="$SHA256_AMD64" ;;
arm64) want_sha="$SHA256_ARM64" ;;
*) die "unsupported architecture $(dpkg --print-architecture); Sysbox publishes amd64 and arm64 packages" ;;
esac
deb="sysbox-ce_${VERSION}.linux_$(dpkg --print-architecture).deb"

kernel="$(uname -r)"
kernel_major="${kernel%%.*}"
kernel_minor="${kernel#*.}"
kernel_minor="${kernel_minor%%[!0-9]*}"
if [ "$kernel_major" -lt 5 ] || { [ "$kernel_major" -eq 5 ] && [ "${kernel_minor:-0}" -lt 19 ]; }; then
	command -v modinfo >/dev/null 2>&1 && modinfo shiftfs >/dev/null 2>&1 ||
		die "kernel ${kernel} cannot ID-map bind mounts, which Sysbox needs: use Linux 5.19 or newer, or a kernel with the shiftfs module"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
else
	die "need sha256sum to verify the download"
fi

runtimes() { docker info --format '{{json .Runtimes}}'; }

runtimes_out="$(runtimes 2>&1)" || die "docker info failed: ${runtimes_out}"

installed="$(dpkg-query -W -f='${Version}' sysbox-ce 2>/dev/null || true)"
case "$runtimes_out" in
*'"sysbox-runc"'*)
	if [ "$installed" = "${VERSION}.linux" ]; then
		say "Sysbox ${VERSION} is installed and Docker lists sysbox-runc; nothing to do"
		exit 0
	fi
	die "Docker already lists sysbox-runc (sysbox-ce ${installed:-not installed from a package}). Upgrade by following https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install-package.md#upgrading-sysbox-or-sysbox-enterprise"
	;;
esac
[ -z "$installed" ] || die "sysbox-ce ${installed} is installed but Docker does not list sysbox-runc. Run: systemctl restart docker, or purge it with: apt-get purge -y sysbox-ce"

apt_updated=no
apt_install() {
	if [ "$apt_updated" = no ]; then
		apt-get update || die "apt-get update failed"
		apt_updated=yes
	fi
	apt-get install -y "$@"
}

command -v jq >/dev/null 2>&1 || apt_install jq

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
tmp_json="$tmp/daemon.json"
daemon_json_changed=
BIP="172.20.0.1/16"
POOL_BASE="172.25.0.0/16"

# Mirrors the package's own check: an address overlaps a local subnet unless
# the kernel routes it through the default gateway.
overlaps_local_subnet() {
	! ip route get "$1" | grep -q "via $(ip route | awk '/default/ {print $3}')"
}

mkdir -p "$(dirname "$DAEMON_JSON")"
[ -s "$DAEMON_JSON" ] || echo '{}' >"$DAEMON_JSON"

if ! jq -e '(.bip // "") != ""' "$DAEMON_JSON" >/dev/null; then
	if overlaps_local_subnet "${BIP%/*}" && ! ip -4 address show dev docker0 2>/dev/null | grep -q "$BIP"; then
		die "cannot add \"bip\": ${BIP} overlaps a subnet on this host. Set \"bip\" in ${DAEMON_JSON} to a free range, then re-run this script."
	fi
	jq --arg bip "$BIP" --indent 4 '. + {"bip": $bip}' "$DAEMON_JSON" >"$tmp_json" || die "cannot edit ${DAEMON_JSON}"
	cat "$tmp_json" >"$DAEMON_JSON"
	say "added \"bip\": \"${BIP}\" to ${DAEMON_JSON}"
	daemon_json_changed=yes
fi
if ! jq -e '((."default-address-pools" // []) | length) > 0' "$DAEMON_JSON" >/dev/null; then
	if overlaps_local_subnet "${POOL_BASE%/*}"; then
		die "cannot add \"default-address-pools\": ${POOL_BASE} overlaps a subnet on this host. Set \"default-address-pools\" in ${DAEMON_JSON} to a free range, then re-run this script."
	fi
	jq --arg base "$POOL_BASE" --indent 4 '. + {"default-address-pools": [{"base": $base, "size": 24}]}' "$DAEMON_JSON" >"$tmp_json" || die "cannot edit ${DAEMON_JSON}"
	cat "$tmp_json" >"$DAEMON_JSON"
	say "added \"default-address-pools\": [{\"base\": \"${POOL_BASE}\", \"size\": 24}] to ${DAEMON_JSON}"
	daemon_json_changed=yes
fi
[ -z "$daemon_json_changed" ] || say "Docker applies these at its next restart; this script does not restart it, and running containers keep running"

say "downloading ${deb}"
curl -fsSL -o "$tmp/$deb" "${BASE_URL}/${deb}" || die "cannot download ${BASE_URL}/${deb}"
got="$(sha256_of "$tmp/$deb")"
[ "$got" = "$want_sha" ] || die "checksum mismatch for ${deb}: expected ${want_sha}, got ${got}"

say "installing ${deb}"
apt_install "$tmp/$deb"

tries=0
until runtimes | grep -q '"sysbox-runc"'; do
	tries=$((tries + 1))
	[ "$tries" -lt 15 ] || die "Docker does not list sysbox-runc after the install; docker info: $(runtimes 2>&1). Try: systemctl restart docker"
	sleep 1
done

say "Docker lists sysbox-runc"

probe="$tmp/probe"
mkdir "$probe"
chown 1000:1000 "$probe"
probe_out="$(docker run --rm --runtime sysbox-runc -u 1000:1000 -v "$probe:/m" busybox:1.36 touch /m/probe 2>&1)" ||
	die "bind mounts are not ID-mapped on this host: a container user cannot write a host directory it owns: ${probe_out}"
say "bind-mount probe passed"
echo
echo "next:"
echo "  sudo systemctl restart aether-server   # the server picks its runtime once, at first use"
echo "New runs and terminals use Sysbox. Existing containers keep their runtime."
