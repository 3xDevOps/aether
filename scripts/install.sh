#!/bin/sh
# Aether installer: downloads released binaries from GitHub and puts them on
# PATH. Portable POSIX sh - no bash, no package manager, no dependencies
# beyond curl and a sha256 tool; OpenSSL 3, when present, checks the
# release signature.
#
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/3xDevOps/Aether/main/scripts/install.sh | sh -s -- --client
#
# Flags (or the matching environment variables):
#   --version <tag>   AETHER_VERSION      release tag; default: the newest
#                                         complete release
#   --bin-dir <dir>   AETHER_BIN_DIR      install directory; default: /usr/local/bin,
#                                         or ~/.local/bin for the client role
#   --client          AETHER_COMPONENTS=client   CLI only
#   --server          AETHER_COMPONENTS=server   server only
#   --role <role>     AETHER_ROLE         server | client | none
#
# The server binary is Linux-only by design. On macOS this installs the CLI.
#
# The script asks whether this machine is the server or a client. The answer
# picks what is installed and where: a client gets the CLI alone in
# ~/.local/bin, which the desktop app can replace on update without sudo. It
# then finishes that role's setup: `aether-server setup` on a server,
# `aether gui build` on a client. Pass --role none to skip both.

set -eu

REPO="${AETHER_REPO:-3xDevOps/Aether}"
BASE_URL="${AETHER_BASE_URL:-https://github.com/${REPO}/releases/download}"
VERSION="${AETHER_VERSION:-}"
BIN_DIR="${AETHER_BIN_DIR:-}"
COMPONENTS="${AETHER_COMPONENTS:-}"
ROLE="${AETHER_ROLE:-}"

# An explicit component choice - a flag, or AETHER_COMPONENTS - also answers
# the role question. The platform default must not: macOS gets the client
# binary automatically and is still asked, with client as the answer Enter
# takes.
COMPONENTS_CHOSEN=no
[ -z "$COMPONENTS" ] || COMPONENTS_CHOSEN=yes

# Test seams. install-test.sh points these at a fake terminal and a fake unit
# file so the question and the post-setup check are covered without a pty or
# a writable /etc. They are not documented options.
TTY_PATH="${AETHER_TTY:-/dev/tty}"
UNIT_PATH="${AETHER_UNIT_PATH:-/etc/systemd/system/aether-server.service}"
SYSTEM_BIN_DIR="${AETHER_SYSTEM_BIN_DIR:-/usr/local/bin}"

# Where the client CLI goes, and where the desktop launcher looks for it on
# its own (internal/localops/desktop.go).
USER_BIN_DIR="${HOME:-}/.local/bin"

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
		COMPONENTS_CHOSEN=yes
		shift
		;;
	--server)
		COMPONENTS="server"
		COMPONENTS_CHOSEN=yes
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

  --version   release tag to install, exactly that or nothing (default: the
              newest release with every asset this install needs published)
  --bin-dir   where to put the binaries (default: /usr/local/bin, or
              ~/.local/bin when the role is client)
  --client    install the aether CLI only
  --server    install the aether-server binary only (Linux)
  --role      what this machine is, skipping the question:
                server  both binaries into /usr/local/bin, then
                        `aether-server setup`, which needs a terminal
                client  the CLI alone into ~/.local/bin, then
                        `aether gui build`
                none    install into /usr/local/bin and stop

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

ROLE="$(printf '%s' "$ROLE" | tr '[:upper:]' '[:lower:]')"
case "$ROLE" in
"" | server | client | none) ;;
*) die "unknown role ${ROLE} (use server, client, or none)" ;;
esac

command -v curl >/dev/null 2>&1 || die "need curl"

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
*) die "unsupported OS $os (Windows clients: use scripts/install.ps1; see docs/install.md#windows-install-script)" ;;
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

# --- which role does this machine play? --------------------------------

# This script is normally piped into sh, so stdin is the script itself: a
# prompt that read stdin would swallow the rest of the installer. Every
# question and every interactive child therefore uses the terminal directly.
# The open is attempted rather than tested with `-r`: /dev/tty exists and
# looks readable inside a container or a cron job that has no controlling
# terminal, and only opening it says so. It is opened once, on fd 3, because
# reopening it per read would restart a non-terminal source from the top.
have_tty=no
if (exec 3<"$TTY_PATH") 2>/dev/null; then
	exec 3<"$TTY_PATH"
	have_tty=yes
fi

# Questions go to the terminal rather than stdout, so a run whose output is
# redirected to a file still shows what it is waiting for.
if [ "$have_tty" = yes ] && [ -c "$TTY_PATH" ] && (exec 4>"$TTY_PATH") 2>/dev/null; then
	exec 4>"$TTY_PATH"
else
	exec 4>&1
fi

ask() { echo "$*" >&4; }

# Runs a command with its stdin on the terminal - never on the script's own
# stdin, which is the script itself when this arrives through a pipe.
run_interactive() {
	interactive_status=0
	if [ "$have_tty" = yes ]; then
		"$@" <&3 || interactive_status=$?
	else
		"$@" </dev/null || interactive_status=$?
	fi
	# A POSIX shell cannot trap a signal that was ignored when it started.
	case "$interactive_status" in
	130 | 143) exit "$interactive_status" ;;
	esac
	return "$interactive_status"
}

# An explicit --client or --server, or AETHER_COMPONENTS, already answered
# the question. The platform default did not, so macOS is still asked.
if [ -z "$ROLE" ] && [ "$COMPONENTS_CHOSEN" = yes ]; then
	case "$COMPONENTS" in
	client) ROLE="client" ;;
	server) ROLE="server" ;;
	esac
fi

if [ -z "$ROLE" ] && [ "$have_tty" = no ]; then
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
	ask ""
	ask "What is this machine?"
	ask "  server   agents run here, each in its own container on this box"
	ask "  client   you work here and connect to a server"
	ask "  none     just the binaries; set the rest up yourself"
	while [ -z "$ROLE" ]; do
		printf 'role [%s]: ' "$role_default" >&4
		if IFS= read -r reply <&3; then :; else
			# Terminal closed mid-question; take the default rather than spin.
			reply=""
			ask ""
		fi
		case "$(printf '%s' "$reply" | tr '[:upper:]' '[:lower:]')" in
		"") ROLE="$role_default" ;;
		s | server) ROLE="server" ;;
		c | client) ROLE="client" ;;
		n | none) ROLE="none" ;;
		*) ask "  answer server, client, or none" ;;
		esac
	done
fi

# The platform default asked for both binaries before the role was known. A
# client answer makes aether-server irrelevant, and worse than useless next to
# the CLI: `aether update` treats it as proof this box is a server, so every
# update pulls a server binary this machine never runs and the dashboard asks
# for a `systemctl restart aether-server` that no unit backs. An explicit
# --client, --server or AETHER_COMPONENTS still decides for itself.
if [ "$ROLE" = client ] && [ "$COMPONENTS_CHOSEN" = no ]; then
	COMPONENTS="client"
fi

# --- destination -------------------------------------------------------

# A trailing slash would make the PATH check miss and the shadow check below
# compare the install directory against itself.
while [ "$BIN_DIR" != "/" ] && [ "${BIN_DIR%/}" != "$BIN_DIR" ]; do
	BIN_DIR="${BIN_DIR%/}"
done

sudo=""
if [ -n "$BIN_DIR" ]; then
	# An explicit --bin-dir or AETHER_BIN_DIR wins over the role.
	if [ ! -w "$BIN_DIR" ] && [ -d "$BIN_DIR" ] && command -v sudo >/dev/null 2>&1; then
		sudo="sudo"
	fi
elif [ "$ROLE" = client ]; then
	# The desktop app's Update button replaces this binary from the gateway,
	# which runs as you. A root-owned binary in /usr/local/bin turns that
	# button into "run `sudo aether update`" on every default Linux install.
	[ -n "${HOME:-}" ] || die "HOME is not set; pass --bin-dir <dir>"
	BIN_DIR="$USER_BIN_DIR"
else
	BIN_DIR="$SYSTEM_BIN_DIR"
	if [ "$(id -u)" -ne 0 ]; then
		if command -v sudo >/dev/null 2>&1; then
			sudo="sudo"
		else
			[ -n "${HOME:-}" ] ||
				die "server installation needs root or sudo, and HOME is not set; pass --bin-dir <dir>"
			BIN_DIR="$USER_BIN_DIR"
			say "server installation needs root or sudo; using ${BIN_DIR}"
		fi
	fi
fi
if ! $sudo mkdir -p "$BIN_DIR"; then
	if [ -n "$sudo" ] && [ "$ROLE" != server ]; then
		die "cannot create ${BIN_DIR} with sudo. To install only the aether CLI, without sudo, run:
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sh -s -- --client --role none --bin-dir \"\$HOME/.local/bin\""
	fi
	die "cannot create ${BIN_DIR}"
fi

# --- download and verify -----------------------------------------------

# The ed25519 keys a release manifest may be signed with. Keep this list
# identical to releaseKeys in internal/selfupdate/keys.go; install-test.sh
# fails when they differ.
RELEASE_KEYS="J+AIq6GUVbRtEQ1ORqKJe3uh9JhvZkEQJQtYpxKbxD8="

# Every release from this tag on is signed. Two missing manifest files say
# nothing about a release's age: a release being published, or one whose
# manifest was deleted, looks the same. Only an older tag may install on
# checksums.txt alone.
SIGNED_SINCE="v0.7.0-alpha.12"

API_URL="https://api.github.com/repos/${REPO}/releases?per_page=100"

tmp="$(mktemp -d)"
staged=no
replacing=no replaced="" pending=""
cleanup() {
	rm -rf "$tmp"
	if [ "$staged" = yes ]; then
		for name in $names; do $sudo rm -f "${BIN_DIR}/.${name}.install.$$"; done
	fi
	if [ "$replacing" = yes ]; then
		echo "aether install: stopped while replacing binaries; now ${VERSION}: ${replaced:-none}; still the previous version: ${pending}" >&2
	fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

case "$COMPONENTS" in
client) names="aether" ;;
server) names="aether-server" ;;
both) names="aether aether-server" ;;
*) die "unknown component set ${COMPONENTS}" ;;
esac
assets=""
for name in $names; do assets="${assets:+$assets }${name}-${os}-${arch}"; done

curl_get() {
	curl -sS --fail -L --proto =https --proto-redir =https --connect-timeout 20 --retry 3 "$@"
}

# get URL FILE saves URL to FILE. It returns 1 only for a 404, an asset that
# is not published; any other failure is fatal, never a reason to fall back.
get() {
	http_status="$(curl_get -o "$2" -w '%{http_code}' "$1" 2>"$tmp/curl.err")" && return 0
	rc=$?
	[ "$rc" = 22 ] && [ "$http_status" = 404 ] && return 1
	cat "$tmp/curl.err" >&2
	if [ "$rc" = 22 ]; then
		die "cannot download $1: HTTP ${http_status}"
	fi
	die "cannot download $1 (curl exit ${rc})"
}

# A binary checksums.txt already lists can 404 for a few seconds while the
# release CDN catches up.
get_binary() {
	tries=0
	until get "$1" "$2"; do
		tries=$((tries + 1))
		[ "$tries" -le 3 ] || return 1
		sleep 2
	done
}

checksum_of() {
	awk -v a="$2" '$2 == a || $2 == "*" a { print $1 }' "$1"
}

# manifest_field FILE ASSET FIELD reads one field of one asset entry. The
# manifest is json.MarshalIndent output, one key per line; anything else
# reads as empty and fails the cross-check below.
manifest_field() {
	awk -v key="\"$2\":" -v field="\"$3\":" '
		index($0, key) == match($0, /[^ \t]/) && index($0, key) { inside = 1; next }
		inside && /}/ { exit }
		inside && index($0, field) {
			v = $0
			sub(/^[^:]*:[ \t]*/, "", v)
			gsub(/[", \t\r]/, "", v)
			print v
			exit
		}
	' "$1"
}

manifest_top() {
	sed -n "s/^  \"$2\": \"*\([^\",]*\)\"*,*\$/\1/p" "$1" | head -1
}

openssl_major() {
	command -v openssl >/dev/null 2>&1 || return 1
	openssl version 2>/dev/null | awk '$1 == "OpenSSL" { split($2, v, "."); print v[1] + 0; exit }'
}

# verify_signature DIR TAG checks DIR/aether-release.json.sig against
# RELEASE_KEYS. It returns 1, having said why, when this machine has no
# OpenSSL 3 to check it with; a signature that does not verify is fatal.
verify_signature() {
	major="$(openssl_major)" || major=""
	if [ -z "$major" ] || [ "$major" -lt 3 ]; then
		found="no openssl"
		command -v openssl >/dev/null 2>&1 && found="$(openssl version 2>&1 | head -1)"
		say "warning: aether-release.json.sig not verified: that needs OpenSSL 3 (found: ${found}); checking downloads against checksums.txt and aether-release.json only"
		return 1
	fi
	if ! openssl base64 -d -A -in "$1/aether-release.json.sig" -out "$1/sig.bin" 2>/dev/null ||
		[ "$(wc -c <"$1/sig.bin")" -ne 64 ]; then
		die "aether-release.json.sig for $2 is not a base64 ed25519 signature; not installing it"
	fi
	for key in $RELEASE_KEYS; do
		printf -- '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA%s\n-----END PUBLIC KEY-----\n' "$key" >"$1/key.pem"
		if err="$(openssl pkeyutl -verify -pubin -inkey "$1/key.pem" -rawin \
			-in "$1/aether-release.json" -sigfile "$1/sig.bin" 2>&1)"; then
			say "verified aether-release.json.sig for $2 against release key ${key}"
			return 0
		fi
		case "$err" in
		*"Signature Verification Failure"*) ;;
		*) die "cannot check aether-release.json.sig for $2 with $(openssl version): ${err}" ;;
		esac
	done
	die "aether-release.json for $2 is not signed by a trusted release key; not installing it"
}

# prepare TAG downloads and verifies every asset this install needs from TAG
# into $tmp/TAG. It returns 1, with the absent file in $missing and a reason
# in $why, when TAG is not fully published. Integrity failures are fatal.
prepare() {
	tag="$1"
	dir="$tmp/$1"
	url="${BASE_URL}/$1"
	missing="" why=""
	mkdir -p "$dir" || die "cannot create ${dir}"
	if ! get "$url/checksums.txt" "$dir/checksums.txt"; then
		missing="checksums.txt" why="no checksums.txt"
		return 1
	fi
	signed=no
	get "$url/aether-release.json" "$dir/aether-release.json" && signed=manifest
	if get "$url/aether-release.json.sig" "$dir/aether-release.json.sig"; then
		[ "$signed" = manifest ] || { missing="aether-release.json" why="no aether-release.json"; return 1; }
		signed=yes
	elif [ "$signed" = manifest ]; then
		missing="aether-release.json.sig" why="no aether-release.json.sig"
		return 1
	elif signing_required "$tag"; then
		missing="aether-release.json" why="no aether-release.json"
		return 1
	fi
	if [ "$signed" = yes ]; then
		[ "$(manifest_top "$dir/aether-release.json" schema)" = 1 ] ||
			die "aether-release.json for ${tag} is not schema 1; this installer reads schema 1"
		[ "$(manifest_top "$dir/aether-release.json" version)" = "$tag" ] ||
			die "aether-release.json fetched for ${tag} describes $(manifest_top "$dir/aether-release.json" version); not installing it"
		if verify_signature "$dir" "$tag"; then
			verified="the signed aether-release.json and checksums.txt"
		else
			verified="aether-release.json and checksums.txt, without a signature check"
		fi
	else
		say "${tag} has no signed aether-release.json (it predates release signing); verifying SHA-256 against checksums.txt only"
		verified="checksums.txt only"
	fi
	for asset in $assets; do
		[ -z "$(checksum_of "$dir/checksums.txt" "$asset")" ] || continue
		if [ "$signed" = yes ] && [ -n "$(manifest_field "$dir/aether-release.json" "$asset" sha256)" ]; then
			die "aether-release.json for ${tag} lists ${asset} but checksums.txt does not; not installing it"
		fi
		missing="$asset" why="checksums.txt does not list $asset"
		return 1
	done
	for asset in $assets; do
		want="$(checksum_of "$dir/checksums.txt" "$asset")"
		[ "$(printf '%s\n' "$want" | wc -l)" -eq 1 ] ||
			die "checksums.txt for ${tag} lists ${asset} more than once"
		case "$want" in
		*[!0-9a-f]*) die "checksums.txt for ${tag} has a malformed entry for ${asset}" ;;
		esac
		[ "${#want}" -eq 64 ] || die "checksums.txt for ${tag} has a malformed entry for ${asset}"
		size=""
		if [ "$signed" = yes ]; then
			[ "$(manifest_field "$dir/aether-release.json" "$asset" sha256)" = "$want" ] ||
				die "aether-release.json and checksums.txt for ${tag} disagree about ${asset}; not installing it"
			size="$(manifest_field "$dir/aether-release.json" "$asset" size)"
		fi
		say "downloading ${asset} ${tag}"
		if ! get_binary "$url/$asset" "$dir/$asset"; then
			missing="$asset" why="no $asset"
			return 1
		fi
		if [ -n "$size" ]; then
			got="$(wc -c <"$dir/$asset" | tr -d ' ')"
			[ "$got" = "$size" ] ||
				die "size mismatch for ${asset} ${tag}: expected ${size} bytes, got ${got} (truncated download?)"
		fi
		got="$(sha256_of "$dir/$asset")"
		[ "$want" = "$got" ] || die "checksum mismatch for ${asset} ${tag}: expected ${want}, got ${got}"
	done
	say "verified $(echo "$assets" | sed 's/ /, /g') against ${verified}"
}

valid_tag() {
	case "$1" in
	"" | [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) return 1 ;;
	esac
}

resolve_latest() {
	latest="$(curl_get -o /dev/null -w '%{url_effective}' \
		"https://github.com/${REPO}/releases/latest" 2>"$tmp/curl.err")" || {
		cat "$tmp/curl.err" >&2
		die "cannot reach https://github.com/${REPO}/releases/latest"
	}
	latest="${latest##*/}"
	case "$latest" in
	"" | releases | latest) die "no published release found for ${REPO}; pass --version <tag>" ;;
	esac
	valid_tag "$latest" || die "the latest release redirect named an invalid tag: ${latest}"
}

SEMVER_AWK='
	function semver(t) {
		return t ~ /^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/
	}
	function cmpid(a, b) {
		if (a ~ /^[0-9]+$/ && b ~ /^[0-9]+$/) return (a + 0 < b + 0) ? -1 : (a + 0 > b + 0)
		if (a ~ /^[0-9]+$/) return -1
		if (b ~ /^[0-9]+$/) return 1
		return (a < b) ? -1 : (a > b)
	}
	function cmp(a, b,    pa, pb, na, nb, i, c, ma, mb) {
		sub(/^v/, "", a); sub(/^v/, "", b); sub(/\+.*/, "", a); sub(/\+.*/, "", b)
		pa = index(a, "-") ? substr(a, index(a, "-") + 1) : ""
		pb = index(b, "-") ? substr(b, index(b, "-") + 1) : ""
		if (index(a, "-")) a = substr(a, 1, index(a, "-") - 1)
		if (index(b, "-")) b = substr(b, 1, index(b, "-") - 1)
		split(a, ma, "."); split(b, mb, ".")
		for (i = 1; i <= 3; i++) if ((c = cmpid(ma[i], mb[i])) != 0) return c
		if (pa == "" || pb == "") return (pa == pb) ? 0 : (pa == "" ? 1 : -1)
		na = split(pa, ma, "."); nb = split(pb, mb, ".")
		for (i = 1; i <= na && i <= nb; i++) if ((c = cmpid(ma[i], mb[i])) != 0) return c
		return (na < nb) ? -1 : (na > nb)
	}
'

# signing_required TAG is true for a tag at or after SIGNED_SINCE, and for
# any tag that is not semver.
signing_required() {
	LC_ALL=C awk -v t="$1" -v s="$SIGNED_SINCE" "$SEMVER_AWK"'
		BEGIN { exit !(!semver(t) || cmp(t, s) >= 0) }'
}

semver_lt() {
	LC_ALL=C awk -v a="$1" -v b="$2" "$SEMVER_AWK"'
		BEGIN { exit !(semver(a) && semver(b) && cmp(a, b) < 0) }'
}

# Prints "tag asset asset ..." for every published, non-prerelease release
# with a semver tag in the GitHub API's JSON, newest version first. A
# tokenizer rather than a line match: it holds for compact and indented JSON
# and for release notes that contain anything at all.
parse_releases() {
	LC_ALL=C awk "$SEMVER_AWK"'
		BEGIN { RS = "\001" }
		{ doc = doc $0 }
		function scalar(v) {
			if (depth == 2) {
				if (key[2] == "tag_name") tag = v
				else if (key[2] == "draft") draft = v
				else if (key[2] == "prerelease") pre = v
			} else if (depth == 4 && key[2] == "assets" && key[4] == "name") {
				names = names " " v
			}
		}
		function open_(t) {
			depth++; type[depth] = t; key[depth] = ""
			if (depth == 2) { tag = ""; draft = ""; pre = ""; names = "" }
		}
		function close_() {
			if (depth == 2 && type[1] == "a" && draft == "false" && pre == "false" && semver(tag))
				rel[++n] = tag names
			depth--
		}
		END {
			# BWK awk, the macOS one, also splits on newlines below.
			gsub(/\n/, " ", doc)
			gsub(/\\\\/, "", doc)
			gsub(/\\"/, "", doc)
			parts = split(doc, piece, "\"")
			for (p = 1; p <= parts; p++) {
				if (p % 2 == 0) {
					rest = piece[p + 1]
					sub(/^[ \t\r\n]*/, "", rest)
					if (type[depth] == "o" && substr(rest, 1, 1) == ":") key[depth] = piece[p]
					else scalar(piece[p])
					continue
				}
				s = piece[p]
				while (s != "") {
					c = substr(s, 1, 1)
					if (c == "{") open_("o")
					else if (c == "[") open_("a")
					else if (c == "}" || c == "]") close_()
					else if (c ~ /[a-z0-9-]/) {
						match(s, /^[a-z0-9.eE+-]+/)
						scalar(substr(s, 1, RLENGTH))
						s = substr(s, RLENGTH + 1)
						continue
					}
					s = substr(s, 2)
				}
			}
			for (i = 2; i <= n; i++)
				for (j = i; j > 1; j--) {
					split(rel[j], x, " "); split(rel[j - 1], y, " ")
					if (cmp(x[1], y[1]) <= 0) break
					t = rel[j]; rel[j] = rel[j - 1]; rel[j - 1] = t
				}
			for (i = 1; i <= n; i++) print rel[i]
		}
	' "$1"
}

list_releases() {
	api_status="$(curl -sS -L --proto =https --proto-redir =https --connect-timeout 20 --retry 3 \
		-H 'Accept: application/vnd.github+json' -D "$tmp/api.headers" -o "$tmp/api.json" \
		-w '%{http_code}' "$API_URL" 2>"$tmp/curl.err")" || {
		cat "$tmp/curl.err" >&2
		die "cannot reach ${API_URL}"
	}
	[ "$api_status" = 200 ] && return 0
	if [ "$api_status" = 403 ] || [ "$api_status" = 429 ]; then
		remaining="$(tr -d '\r' <"$tmp/api.headers" | awk -F': *' 'tolower($1) == "x-ratelimit-remaining" { v = $2 } END { print v }')"
		if [ "$remaining" = 0 ]; then
			reset="$(tr -d '\r' <"$tmp/api.headers" | awk -F': *' 'tolower($1) == "x-ratelimit-reset" { v = $2 } END { print v }')"
			die "GitHub API rate limit exceeded (HTTP ${api_status} from ${API_URL}, x-ratelimit-remaining: 0, x-ratelimit-reset: ${reset}); pass --version <tag> to install a specific release"
		fi
	fi
	message="$(sed -n 's/.*"message": *"\([^"]*\)".*/\1/p' "$tmp/api.json" | head -1)"
	die "GET ${API_URL}: HTTP ${api_status}${message:+: $message}"
}

if [ -n "$VERSION" ]; then
	valid_tag "$VERSION" || die "invalid release tag ${VERSION}"
	say "installing ${VERSION} for ${os}/${arch}"
	if ! prepare "$VERSION"; then
		detail=""
		case "$why" in "checksums.txt does not list"*) detail=" (checksums.txt does not list it)" ;; esac
		die "${VERSION} has no published ${missing}${detail}; it may still be building or may have failed - not installing a different version"
	fi
else
	say "resolving the latest release"
	resolve_latest
	say "the latest release is ${latest}"
	if prepare "$latest"; then
		VERSION="$latest"
		say "installing ${VERSION} for ${os}/${arch}"
	else
		latest_why="$why"
		say "${latest} is not fully published yet (${latest_why}); looking for the newest complete release"
		list_releases
		listed() { case " $release_assets " in *" $1 "*) ;; *) return 1 ;; esac; }
		parse_releases "$tmp/api.json" >"$tmp/releases"
		while read -r tag release_assets; do
			[ "$tag" != "$latest" ] || continue
			valid_tag "$tag" || continue
			ready=yes
			for file in checksums.txt $assets; do listed "$file" || ready=no; done
			signed=no sig=no
			! listed aether-release.json || signed=yes
			! listed aether-release.json.sig || sig=yes
			# A fallback is always older than latest and always signed, so
			# neither a newer tag nor a 404 on every signature can pick it.
			if [ "$ready" = no ] || [ "$signed" = no ] || [ "$sig" = no ] ||
				! semver_lt "$tag" "$latest"; then
				continue
			fi
			if prepare "$tag"; then
				VERSION="$tag"
				break
			fi
			say "skipping ${tag}: not fully published (${why})"
		done <"$tmp/releases"
		[ -n "$VERSION" ] ||
			die "no complete release for ${os}/${arch} among the newest 100 releases of ${REPO}; pass --version <tag>"
		say "${latest} is not fully published yet (${latest_why}); installing ${VERSION}, the newest complete release, for ${os}/${arch}"
	fi
fi

# Every binary is staged beside its destination, on the same filesystem, so
# each replacement is a rename and a failure leaves the old install whole.
for name in $names; do
	stage="${BIN_DIR}/.${name}.install.$$"
	staged=yes
	$sudo cp "$tmp/$VERSION/${name}-${os}-${arch}" "$stage" || die "cannot write into ${BIN_DIR}"
	$sudo chmod 0755 "$stage" || die "cannot set the mode of ${stage}"
done
# The server goes first: a new CLI next to an old server is refused by it,
# while an old CLI next to a new server still updates itself.
order=""
for name in aether-server aether; do
	case " $names " in *" $name "*) order="${order:+$order }$name" ;; esac
done
pending="$order"
replacing=yes
for name in $order; do
	$sudo mv -f "${BIN_DIR}/.${name}.install.$$" "${BIN_DIR}/${name}" || die "cannot install ${BIN_DIR}/${name}"
	replaced="${replaced:+$replaced }${BIN_DIR}/${name}"
	pending="${pending#"$name"}"
	pending="${pending# }"
	say "installed ${BIN_DIR}/${name} ${VERSION}"
done
replacing=no staged=no

# --- finish that role's setup ------------------------------------------

setup_ran=no
setup_wrote=no
gui_built=no
gui_failure=""

# Once `aether-server setup` writes the unit it ends on the commands that
# start the service, so this script must print nothing after them that
# could bury them.
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
	if [ "$have_tty" = no ]; then
		die "installed ${replaced}, but did not set up the server: aether-server setup asks questions and there is no terminal. Set it up with flags instead:
  ${setup_sudo:+sudo }aether-server install --edge-url https://edge.onaether.dev --edge-access <account|approved-devices>"
	fi
	echo
	say "running aether-server setup"
	: >"$tmp/setup-started"
	run_interactive $setup_sudo "${BIN_DIR}/aether-server" setup || return 1
	# Declining setup's summary is a clean exit that writes nothing, so the
	# exit status alone cannot say whether setup printed its next steps. The
	# systemd unit can: setup rewrites it whenever it writes anything, so it
	# is not older than the marker, while a unit from an earlier install is.
	# Equal times count as written. The config file is the wrong signal,
	# because `aether-server config set` creates one without a unit.
	if [ -f "$UNIT_PATH" ] && ! [ "$UNIT_PATH" -ot "$tmp/setup-started" ]; then
		setup_wrote=yes
	fi
}

# The CLI is for agents to steer Aether; people should get the desktop app.
# electron-builder needs Node, and the CLI supplies it: a machine without
# Node 22+ gets a private copy downloaded into the user cache directory, so
# there is nothing to check for here.
run_gui_build() {
	if [ ! -x "${BIN_DIR}/aether" ]; then
		say "the aether CLI was not installed here, so there is no app to build"
		return 1
	fi
	echo
	say "building the desktop app - this downloads Electron and takes a few minutes"
	run_interactive "${BIN_DIR}/aether" gui build || {
		gui_failure="\`aether gui build\` exited ${interactive_status}; its error is above"
		return 1
	}
}

# --- PATH, and an older CLI that would shadow this one -----------------

# The one line that adds BIN_DIR to PATH for the login shell in $SHELL.
# Nothing here edits a profile; the user runs it. SHELL is unset in
# containers, cron jobs and most CI runners - exactly the machines that take
# this branch - so an unset or unrecognised shell gets the generic export
# rather than a "parameter not set" error under `set -u`.
path_line() {
	shell_name="${SHELL:-}"
	shell_name="${shell_name##*/}"
	case "$shell_name" in
	fish) echo "fish_add_path ${BIN_DIR}   # run this one in fish" ;;
	zsh) echo "echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.zshrc" ;;
	bash)
		if [ "$os" = darwin ]; then
			echo "echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.bash_profile"
		else
			echo "echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.bashrc"
		fi
		;;
	*) echo "export PATH=\"${BIN_DIR}:\$PATH\"   # add this to your shell profile" ;;
	esac
}

check_path() {
	warned=no
	case ":${PATH}:" in
	*":${BIN_DIR}:"*) ;;
	*)
		echo
		warned=yes
		say "warning: ${BIN_DIR} is not on your PATH. Add it, then open a new shell:"
		echo "  $(path_line)"
		if [ "$BIN_DIR" = "$USER_BIN_DIR" ]; then
			say "the desktop app looks in ~/.local/bin itself, so it starts either way."
		fi
		;;
	esac

	# An older CLI in /usr/local/bin comes first on most PATHs, so it would run
	# instead of the one just installed.
	if [ "$BIN_DIR" != "$SYSTEM_BIN_DIR" ] && [ "$COMPONENTS" != server ] &&
		[ -e "${SYSTEM_BIN_DIR}/aether" ]; then
		[ "$warned" = yes ] || echo
		say "warning: an older ${SYSTEM_BIN_DIR}/aether would shadow ${BIN_DIR}/aether. Remove it:"
		echo "  sudo rm -f ${SYSTEM_BIN_DIR}/aether"
	fi
}

# The PATH warnings go before setup, which must end the run, and after the
# desktop build, whose output would bury them.
case "$ROLE" in
server)
	check_path
	run_server_setup && setup_ran=yes || true
	;;
client)
	run_gui_build && gui_built=yes || true
	check_path
	;;
*) check_path ;;
esac

# --- what happened, what is next, and the guide ------------------------

quickstart="https://github.com/${REPO}/blob/main/docs/quickstart.md"

if [ "$setup_wrote" = yes ]; then
	echo
	echo "Read the quickstart before going further: ${quickstart}"
	exit 0
fi

# NO_COLOR: https://no-color.org
hi="" lo=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	hi="$(printf '\033[1m\033[38;2;245;130;49m')"
	lo="$(printf '\033[0m')"
fi

next_step() {
	if [ $# -eq 1 ]; then
		printf '  %s%s%s\n' "$hi" "$1" "$lo"
	else
		printf '  %s%-30s%s # %s\n' "$hi" "$1" "$lo" "$2"
	fi
}

echo
case "$ROLE" in
server) echo "This machine is your Aether server: agents run here." ;;
client) echo "This machine is a client: you work here and connect to a server." ;;
*) echo "The binaries are installed. Nothing else was set up on this machine." ;;
esac
echo
echo "Read the quickstart before going further. It is ten minutes to a finished agent"
echo "run, and skipping it is how people end up stuck."
echo
echo "  ${quickstart}"
echo
echo "Install reference: https://github.com/${REPO}/blob/main/docs/install.md"
echo
echo "${hi}next:${lo}"
case "$ROLE" in
server)
	if [ "$setup_ran" = yes ] && [ -f "$UNIT_PATH" ]; then
		# Whoever piped this to `sudo sh` is not root either, as setup decides.
		as_root=""
		if [ -n "$setup_sudo" ] || [ -n "${SUDO_USER:-}" ]; then
			as_root="sudo "
		fi
		next_step "${as_root}systemctl daemon-reload && ${as_root}systemctl enable --now aether-server"
	elif [ -f "$UNIT_PATH" ]; then
		next_step "sudo aether-server update" "restart the running server onto this binary"
	else
		next_step "sudo aether-server setup" "config, systemd unit, and how to start it"
	fi
	next_step "aether link <this-host>:2222" "from your own machine; the first link becomes admin"
	;;
client)
	next_step "aether link <server-host>:2222" "the first link on a fresh server becomes admin"
	if [ "$gui_built" != yes ]; then
		next_step "aether gui build" "the desktop app, or run \`aether gui\` for a browser tab"
	fi
	if [ -n "$gui_failure" ]; then
		echo
		say "the aether CLI is installed in ${BIN_DIR}; the desktop app is not: ${gui_failure}"
	fi
	;;
*)
	if [ "$COMPONENTS" != "client" ] && [ -f "$UNIT_PATH" ]; then
		next_step "sudo aether-server update" "restart the running server onto this binary"
	elif [ "$COMPONENTS" != "client" ]; then
		next_step "sudo aether-server setup" "on the server box: config, systemd unit, activation line"
	fi
	next_step "aether link <server>:2222" "from a client; the first link becomes admin"
	;;
esac
