#!/bin/sh
# Aether installer: downloads released binaries from GitHub and puts them on
# PATH. Portable POSIX sh - no bash, no package manager, no dependencies
# beyond curl (or wget) and a sha256 tool.
#
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --client
#
# Flags (or the matching environment variables):
#   --version <tag>   AETHER_VERSION      release tag; default: latest
#   --bin-dir <dir>   AETHER_BIN_DIR      install directory; default: /usr/local/bin
#   --client          AETHER_COMPONENTS=client   CLI only
#   --server          AETHER_COMPONENTS=server   server only
#
# The server binary is Linux-only by design. On macOS this installs the CLI.

set -eu

REPO="${AETHER_REPO:-3xDevOps/Aether}"
BASE_URL="${AETHER_BASE_URL:-https://github.com/${REPO}/releases/download}"
VERSION="${AETHER_VERSION:-}"
BIN_DIR="${AETHER_BIN_DIR:-}"
COMPONENTS="${AETHER_COMPONENTS:-}"

die() {
	echo "aether install: $*" >&2
	exit 1
}

say() {
	echo "aether install: $*"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	--bin-dir)
		[ $# -ge 2 ] || die "--bin-dir needs a value"
		BIN_DIR="$2"
		shift 2
		;;
	--client)
		COMPONENTS="client"
		shift
		;;
	--server)
		COMPONENTS="server"
		shift
		;;
	-h | --help)
		cat <<'EOF'
usage: install.sh [--version <tag>] [--bin-dir <dir>] [--client | --server]

  --version   release tag to install (default: the latest release)
  --bin-dir   where to put the binaries (default: /usr/local/bin)
  --client    install the aether CLI only
  --server    install the aether-server binary only (Linux)

Environment equivalents: AETHER_VERSION, AETHER_BIN_DIR, AETHER_COMPONENTS
(client|server|both), AETHER_REPO, AETHER_BASE_URL.
EOF
		exit 0
		;;
	*) die "unknown option $1" ;;
	esac
done

# --- fetch helpers -----------------------------------------------------

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_effective_url() { curl -fsSL -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_effective_url() {
		# wget prints every redirect it follows; the last one is the target.
		wget -qS --spider -O /dev/null "$1" 2>&1 | awk '/^ *Location:/ { print $2 }' | tail -1
	}
else
	die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "need sha256sum or shasum to verify downloads"
fi

# --- platform ----------------------------------------------------------

os="$(uname -s)"
case "$os" in
Linux) os="linux" ;;
Darwin) os="darwin" ;;
*) die "unsupported OS $os (Windows clients: download aether-windows-<arch>.exe from the releases page)" ;;
esac

arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
*) die "unsupported architecture $arch" ;;
esac

if [ -z "$COMPONENTS" ]; then
	# The server is Linux-only (v1 cut-line); elsewhere install the client.
	if [ "$os" = "linux" ]; then COMPONENTS="both"; else COMPONENTS="client"; fi
fi
if [ "$COMPONENTS" != "client" ] && [ "$os" != "linux" ]; then
	die "aether-server runs on Linux only; re-run with --client"
fi

# --- version -----------------------------------------------------------

if [ -z "$VERSION" ]; then
	say "resolving latest release"
	latest="$(fetch_effective_url "https://github.com/${REPO}/releases/latest")" ||
		die "cannot reach github.com/${REPO}/releases/latest"
	VERSION="${latest##*/}"
	case "$VERSION" in
	"" | *releases*) die "no published release found; pass --version <tag>" ;;
	esac
fi
say "installing ${VERSION} for ${os}/${arch}"

# --- destination -------------------------------------------------------

sudo=""
if [ -z "$BIN_DIR" ]; then
	BIN_DIR="/usr/local/bin"
	if [ ! -w "$BIN_DIR" ]; then
		if command -v sudo >/dev/null 2>&1; then
			sudo="sudo"
		else
			BIN_DIR="$HOME/.local/bin"
			say "no write access to /usr/local/bin and no sudo; using ${BIN_DIR}"
		fi
	fi
elif [ ! -w "$BIN_DIR" ] && [ -d "$BIN_DIR" ] && command -v sudo >/dev/null 2>&1; then
	sudo="sudo"
fi
$sudo mkdir -p "$BIN_DIR" || die "cannot create ${BIN_DIR}"

# --- download and verify -----------------------------------------------

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch "${BASE_URL}/${VERSION}/checksums.txt" "$tmp/checksums.txt" ||
	die "cannot download checksums.txt for ${VERSION}"

install_one() {
	name="$1"
	asset="${name}-${os}-${arch}"
	say "downloading ${asset}"
	fetch "${BASE_URL}/${VERSION}/${asset}" "$tmp/$asset" ||
		die "cannot download ${asset}"
	want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1 }' "$tmp/checksums.txt" | head -1)"
	[ -n "$want" ] || die "${asset} is not listed in checksums.txt"
	got="$(sha256_of "$tmp/$asset")"
	[ "$want" = "$got" ] || die "checksum mismatch for ${asset}: expected ${want}, got ${got}"
	chmod 0755 "$tmp/$asset"
	$sudo mv "$tmp/$asset" "${BIN_DIR}/${name}" || die "cannot install into ${BIN_DIR}"
	# mv carries the invoking user's ownership into the destination, which
	# would leave a root-executed binary writable by that user.
	[ -z "$sudo" ] || $sudo chown 0:0 "${BIN_DIR}/${name}" ||
		die "cannot set root ownership on ${BIN_DIR}/${name}"
	say "installed ${BIN_DIR}/${name}"
}

case "$COMPONENTS" in
client) install_one aether ;;
server) install_one aether-server ;;
both)
	install_one aether
	install_one aether-server
	;;
*) die "unknown component set ${COMPONENTS}" ;;
esac

case ":${PATH}:" in
*":${BIN_DIR}:"*) ;;
*) say "warning: ${BIN_DIR} is not on your PATH" ;;
esac

echo
echo "next:"
if [ "$COMPONENTS" != "client" ]; then
	echo "  aether init                 # prepare /var/lib/aether and report the tailnet name"
	echo "  aether-server serve --data-dir /var/lib/aether --addr :2222 --dashboard-port 8080"
fi
echo "  aether link <server>:2222   # first link on a fresh server becomes admin"
echo
echo "10-minute quickstart: https://github.com/${REPO}/blob/main/docs/quickstart.md"
