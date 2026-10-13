#!/bin/sh
# Installs the pinned Sysbox CE release on an Ubuntu or Debian host that
# already runs Docker, so Aether runs can start their own Docker daemon.
# Run as root:
#
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install-sysbox.sh | sudo sh
#
# The package registers the sysbox-runc runtime with Docker. When
# /etc/docker/daemon.json lacks "bip" or "default-address-pools" it adds them
# and restarts Docker, which stops every running container unless
# "live-restore" is on. This script refuses to run into that while containers
# are running.
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

for tool in dpkg apt-get docker; do
	command -v "$tool" >/dev/null 2>&1 || die "${tool} not found; this script needs an Ubuntu or Debian host with Docker installed"
done

case "$(dpkg --print-architecture)" in
amd64) want_sha="$SHA256_AMD64" ;;
arm64) want_sha="$SHA256_ARM64" ;;
*) die "unsupported architecture $(dpkg --print-architecture); Sysbox publishes amd64 and arm64 packages" ;;
esac
deb="sysbox-ce_${VERSION}.linux_$(dpkg --print-architecture).deb"

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

command -v jq >/dev/null 2>&1 || apt-get install -y jq

# The package adds "bip" and "default-address-pools" when either is missing,
# and a daemon.json edit of those needs a Docker restart.
restart_safe=no
if [ -s "$DAEMON_JSON" ] && jq -e '."live-restore" == true or ((.bip // "") != "" and ((."default-address-pools" // []) | length) > 0)' "$DAEMON_JSON" >/dev/null; then
	restart_safe=yes
fi
running="$(docker ps -q | wc -l | tr -d ' ')"
if [ "$restart_safe" = no ] && [ "$running" -gt 0 ]; then
	die "${running} containers are running and the Sysbox package may restart Docker, which stops them. Stop them first (docker stop \$(docker ps -q)), or add \"live-restore\": true to ${DAEMON_JSON} and run: systemctl reload docker. Then re-run this script."
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

say "downloading ${deb}"
curl -fsSL -o "$tmp/$deb" "${BASE_URL}/${deb}" || die "cannot download ${BASE_URL}/${deb}"
got="$(sha256_of "$tmp/$deb")"
[ "$got" = "$want_sha" ] || die "checksum mismatch for ${deb}: expected ${want_sha}, got ${got}"

say "installing ${deb}"
apt-get install -y "$tmp/$deb"

tries=0
until runtimes | grep -q '"sysbox-runc"'; do
	tries=$((tries + 1))
	[ "$tries" -lt 15 ] || die "Docker does not list sysbox-runc after the install; docker info: $(runtimes 2>&1). Try: systemctl restart docker"
	sleep 1
done

say "Docker lists sysbox-runc"
echo
echo "next:"
echo "  sudo systemctl restart aether-server   # the server picks its runtime once, at first use"
echo "New runs and terminals use Sysbox. Existing containers keep their runtime."
