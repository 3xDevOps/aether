#!/bin/sh
# Aether dev deploy: cross-compile aether-server here, install it on the
# target, and restart the service. This is the tight loop between "code
# changed" and "the server runs it" - it skips tests and CI on purpose,
# releases remain the quality gate for published binaries.
#
#   make deploy                              # this machine
#   DEPLOY_HOST=user@server make deploy      # remote over SSH
#
# The target needs systemd and Docker. If the aether-server unit is not
# installed yet, the first deploy installs packaging/systemd/
# aether-server.service and enables it.
#
# A remote deploy authenticates SSH once (every later ssh/scp multiplexes
# over the same connection) and runs all privileged steps in one sudo
# invocation with a terminal attached, so sudo prompts at most once.

set -eu

die() {
	echo "deploy: $*" >&2
	exit 1
}

say() {
	echo "deploy: $*"
}

# Everything below resolves paths relative to the repository root.
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

HOST="${DEPLOY_HOST:-}"
BIN=/usr/local/bin/aether-server
UNIT_SRC=packaging/systemd/aether-server.service
UNIT_DST=/etc/systemd/system/aether-server.service

# The server binary embeds web/dist; building with an unbuilt SPA would
# silently ship an empty dashboard. `make deploy` builds it first.
[ -f web/dist/index.html ] || die "dashboard not built; run 'make deploy'"

if [ -n "$HOST" ]; then
	# The first ssh opens a control master; everything after multiplexes
	# over its socket, so the SSH password or key touch happens once.
	# mktemp keeps the socket path short (unix sockets cap ~100 chars).
	ctldir="$(mktemp -d)"
	ssh_opts="-o ControlMaster=auto -o ControlPath=$ctldir/ctl -o ControlPersist=yes"
	cleanup() {
		ssh $ssh_opts -O exit "$HOST" 2>/dev/null || true
		rm -rf "$ctldir"
	}
	trap cleanup EXIT INT TERM
	target() { ssh $ssh_opts "$HOST" "$1"; }
	# A forced terminal lets remote sudo prompt for a password.
	target_tty() { ssh $ssh_opts -tt "$HOST" "$1"; }
	copy() { scp -q -o "ControlPath=$ctldir/ctl" "$1" "$HOST:$2"; }
	say "connecting to ${HOST}"
	target true
else
	target() { sh -c "$1"; }
	target_tty() { sh -c "$1"; }
	copy() { cp "$1" "$2"; }
fi

SUDO=""
[ "$(target 'id -u')" = 0 ] || SUDO="sudo"

machine="$(target 'uname -m')"
case "$machine" in
x86_64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) die "unsupported target architecture: $machine" ;;
esac

MODULE=github.com/3xDevOps/Aether
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

say "building aether-server ${VERSION} for linux/${goarch}"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath \
	-ldflags "-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT}" \
	-o dist/aether-server-deploy ./cmd/aether-server

tmp="$(target 'mktemp -d')"
copy dist/aether-server-deploy "$tmp/aether-server"

# All privileged steps run as one sudo invocation so a password prompt
# appears at most once. install(1) unlinks the destination first, so
# replacing the binary of a running server never hits ETXTBSY, and root
# ownership keeps a root-executed binary out of reach of the deploying
# user (same concern as install.sh).
privileged="install -m0755 -o root -g root '$tmp/aether-server' '$BIN'"
if ! target 'systemctl cat aether-server.service >/dev/null 2>&1'; then
	say "systemd unit missing; it will be installed and enabled"
	copy "$UNIT_SRC" "$tmp/aether-server.service"
	privileged="$privileged && install -m0644 -o root -g root '$tmp/aether-server.service' '$UNIT_DST' && systemctl daemon-reload && systemctl enable aether-server"
fi
privileged="$privileged && systemctl restart aether-server"

say "installing ${BIN} on ${HOST:-this machine} (sudo may ask for a password)"
target_tty "$SUDO sh -c \"$privileged\""
target "rm -rf '$tmp'"

# An immediately-crashing binary still reports active for an instant; give
# systemd a moment to notice before declaring success.
sleep 1
target 'systemctl is-active --quiet aether-server' ||
	die "aether-server is not running after restart; check journalctl -u aether-server"

deployed="$(target "'$BIN' version")"
case "$deployed" in
*"$VERSION"*) ;;
*) die "target reports '${deployed}', expected ${VERSION}" ;;
esac

say "deployed ${VERSION} (${COMMIT})"
