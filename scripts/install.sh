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
#   --role <role>     AETHER_ROLE         server | client | none
#
# The server binary is Linux-only by design. On macOS this installs the CLI.
#
# After the binaries are in place the script asks whether this machine is the
# server or a client, then finishes that role's setup: `aether-server setup`
# on a server, `aether gui build` on a client. Pass --role none to skip.

set -eu

REPO="${AETHER_REPO:-3xDevOps/Aether}"
BASE_URL="${AETHER_BASE_URL:-https://github.com/${REPO}/releases/download}"
VERSION="${AETHER_VERSION:-}"
BIN_DIR="${AETHER_BIN_DIR:-}"
COMPONENTS="${AETHER_COMPONENTS:-}"
ROLE="${AETHER_ROLE:-}"

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
	--role)
		[ $# -ge 2 ] || die "--role needs a value"
		ROLE="$2"
		shift 2
		;;
	-h | --help)
		cat <<'EOF'
usage: install.sh [--version <tag>] [--bin-dir <dir>] [--client | --server]
                  [--role server|client|none]

  --version   release tag to install (default: the latest release)
  --bin-dir   where to put the binaries (default: /usr/local/bin)
  --client    install the aether CLI only
  --server    install the aether-server binary only (Linux)
  --role      what this machine is, skipping the question:
                server  run `aether-server setup` after installing
                client  run `aether gui build` after installing
                none    install the binaries and stop

Without --role the script asks, and answers the question itself when there is
no terminal to ask on (any non-interactive run behaves like --role none).

Environment equivalents: AETHER_VERSION, AETHER_BIN_DIR, AETHER_COMPONENTS
(client|server|both), AETHER_ROLE, AETHER_REPO, AETHER_BASE_URL.
EOF
		exit 0
		;;
	*) die "unknown option $1" ;;
	esac
done

case "$ROLE" in
"" | server | client | none) ;;
*) die "unknown role ${ROLE} (use server, client, or none)" ;;
esac

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

# --- which role does this machine play? --------------------------------

# This script is normally piped into sh, so stdin is the script itself: a
# prompt that read stdin would swallow the rest of the installer. Every
# question and every interactive child therefore reads the terminal directly,
# and only when there is one. The open is attempted rather than tested with
# `-r`: /dev/tty exists and looks readable inside a container or a cron job
# that has no controlling terminal, and only opening it says so.
if (exec 3</dev/tty) 2>/dev/null; then
	tty_in="/dev/tty"
else
	tty_in="/dev/null"
fi

# --client and --server already answered the question.
if [ -z "$ROLE" ]; then
	case "$COMPONENTS" in
	client) ROLE="client" ;;
	server) ROLE="server" ;;
	esac
fi

if [ -z "$ROLE" ] && [ "$tty_in" = "/dev/null" ]; then
	# No terminal to ask on: CI, a provisioning script, a Dockerfile. Install
	# and stop, exactly as this script has always done.
	ROLE="none"
fi

if [ -z "$ROLE" ]; then
	if [ "$COMPONENTS" != "client" ] && [ "$os" = "linux" ]; then
		role_default="server"
	else
		role_default="client"
	fi
	echo
	echo "What is this machine?"
	echo "  server   agents run here, each in its own container on this box"
	echo "  client   you work here and connect to a server"
	echo "  none     just the binaries; set the rest up yourself"
	while [ -z "$ROLE" ]; do
		printf 'role [%s]: ' "$role_default"
		if IFS= read -r reply </dev/tty; then :; else
			# Terminal closed mid-question; take the default rather than spin.
			reply=""
			echo
		fi
		case "$reply" in
		"") ROLE="$role_default" ;;
		s | server) ROLE="server" ;;
		c | client) ROLE="client" ;;
		n | none) ROLE="none" ;;
		*) echo "  answer server, client, or none" ;;
		esac
	done
fi

# --- finish that role's setup ------------------------------------------

setup_ran=no
gui_built=no
node_missing=no

# `aether-server setup` asks its own questions and prints the systemd
# activation line, so this script must not repeat any of that.
run_server_setup() {
	if [ ! -x "${BIN_DIR}/aether-server" ]; then
		say "aether-server was not installed here, so there is nothing to set up"
		return 1
	fi
	setup_sudo=""
	if [ "$(id -u)" -ne 0 ]; then
		if command -v sudo >/dev/null 2>&1; then
			setup_sudo="sudo"
		else
			say "server setup must run as root and sudo is not installed"
			return 1
		fi
	fi
	echo
	say "running aether-server setup"
	$setup_sudo "${BIN_DIR}/aether-server" setup <"$tty_in" || return 1
	# Declining setup's summary is a clean exit that writes nothing, so the
	# config file rather than the exit status says whether there is a service
	# to activate. Ask the binary where it puts it instead of guessing.
	conf="$("${BIN_DIR}/aether-server" config path 2>/dev/null)" || conf=""
	[ -n "$conf" ] && [ -f "$conf" ]
}

# The CLI is for agents to steer Aether; people should get the desktop app.
# electron-builder needs Node, which is the one thing the CLI cannot supply.
run_gui_build() {
	if ! command -v npm >/dev/null 2>&1 || ! command -v npx >/dev/null 2>&1; then
		node_missing=yes
		return 1
	fi
	echo
	say "building the desktop app"
	"${BIN_DIR}/aether" gui build <"$tty_in" || return 1
}

case "$ROLE" in
server) run_server_setup && setup_ran=yes || true ;;
client) run_gui_build && gui_built=yes || true ;;
esac

# --- what happened, what is next, and the guide ------------------------

echo
case "$ROLE" in
server)
	echo "This machine is your Aether server: agents run here."
	echo
	echo "next:"
	if [ "$setup_ran" = yes ]; then
		echo "  systemctl daemon-reload && systemctl enable --now aether-server"
		echo "  aether link <this-host>:2222    # from your own machine; the first link becomes admin"
	else
		echo "  sudo aether-server setup       # config, systemd unit, and how to start it"
		echo "  aether link <this-host>:2222   # from your own machine; the first link becomes admin"
	fi
	;;
client)
	echo "This machine is a client: you work here and connect to a server."
	if [ "$node_missing" = yes ]; then
		echo "The desktop app was not built - it needs Node.js 22+ (https://nodejs.org) with npm on PATH."
	elif [ "$gui_built" != yes ]; then
		echo "The desktop app was not built; the error above says why."
	fi
	echo
	echo "next:"
	echo "  aether link <server-host>:2222  # the first link on a fresh server becomes admin"
	if [ "$gui_built" = yes ]; then
		echo "  then open Aether from where your desktop lists applications"
	else
		echo "  aether gui build               # the desktop app, or run \`aether gui\` for a browser tab"
	fi
	;;
*)
	echo "The binaries are installed. Nothing else was set up on this machine."
	echo
	echo "next:"
	if [ "$COMPONENTS" != "client" ]; then
		echo "  sudo aether-server setup       # on the server box: config, systemd unit, activation line"
	fi
	echo "  aether link <server>:2222      # from a client; the first link becomes admin"
	;;
esac

echo
echo "Read the quickstart before going further. It is ten minutes to a finished agent"
echo "run, and skipping it is how people end up stuck."
echo
echo "  https://github.com/${REPO}/blob/main/docs/quickstart.md"
echo
echo "Install reference: https://github.com/${REPO}/blob/main/docs/install.md"
