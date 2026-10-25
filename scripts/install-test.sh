#!/bin/sh
# Exercises the follow-up half of scripts/install.sh - the role question and
# what each role runs afterwards - with stubbed curl, uname, id, sudo, npm,
# npx and stubbed "binaries" that only log how they were called. Same style
# as deploy-test.sh and publish-release-test.sh.
#
# Checksums are computed with the real sha256 tool rather than stubbed, so
# the download-verify path stays exactly the one users get.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

bin="$tmp/bin"           # stubs the installer itself calls
node="$tmp/node"         # npm and npx, dropped from PATH in one case
bindir="$tmp/bindir"     # AETHER_BIN_DIR: where the fake binaries land
sysbin="$tmp/usrlocal"   # stands in for /usr/local/bin
home="$tmp/home"         # HOME, so ~/.local/bin is inside the sandbox
fixtures="$tmp/fixtures" # what the fake curl serves
log="$tmp/cmd.log"
reqs="$tmp/requests.log" # every URL the fake curl was asked for
out="$tmp/out.txt"
err="$tmp/err.txt"
mkdir -p "$bin" "$node" "$bindir" "$sysbin" "$home" "$fixtures"

sha_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# --- the release assets the installer downloads --------------------------

# GUI_INTERRUPTS signals the installer the way a terminal delivers Ctrl-C
# during a long build, before the stub logs anything. GUI_FAILS is a build
# that errors.
cat >"$fixtures/aether" <<'EOF'
#!/bin/sh
if [ "${GUI_FAILS:-0}" = 1 ] && [ "${1:-}" = gui ]; then
	echo "aether: gui build: npm ci failed" >&2
	exit 1
fi
if [ "${GUI_INTERRUPTS:-0}" = 1 ] && [ "${1:-}" = gui ]; then
	kill -INT "$PPID"
	exit 130
fi
printf 'aether %s\n' "$*" >>"$CMD_LOG"
echo "installed /home/tester/.local/share/aether/desktop"
echo "open it from your application menu as Aether"
EOF

# Stands in for the real binary. A completed setup writes the systemd unit,
# which is what the installer checks afterwards, and ends on its next steps.
# It usually writes within the installer's marker's file time, which counts
# as written. SETUP_FAILS is a setup that errors;
# SETUP_DECLINES is the operator answering no to the summary, which the real
# command reports by exiting 0 having written nothing.
cat >"$fixtures/aether-server" <<'EOF'
#!/bin/sh
printf 'aether-server %s\n' "$*" >>"$CMD_LOG"
[ "${SETUP_FAILS:-0}" = 0 ] || exit 1
if [ "${SETUP_DECLINES:-0}" = 1 ]; then
	echo "nothing written"
	exit 0
fi
touch "$AETHER_UNIT_PATH"
echo "next:"
echo "  1. start the server on this machine:"
echo "       sudo systemctl daemon-reload && sudo systemctl enable --now aether-server"
EOF

# $fixtures/releases/<tag>/ holds what each tag publishes, $fixtures/latest
# names the tag github.com/.../releases/latest redirects to, and
# $fixtures/api.json (with api.status and api.headers) is the releases API
# answer. Every manifest is signed with a key made here; the installer under
# test is a copy of install.sh that trusts that key instead of the real one.

openssl genpkey -algorithm ed25519 -out "$tmp/test-key.pem" 2>/dev/null ||
	{ echo "install-test: needs OpenSSL 3 to sign fixture releases (macOS ships LibreSSL: brew install openssl@3 and put its bin first on PATH)" >&2; exit 1; }
test_pub="$(openssl pkey -in "$tmp/test-key.pem" -pubout -outform DER | tail -c 32 | openssl base64 -A)"
script="$tmp/install.sh"
sed "s|^RELEASE_KEYS=.*|RELEASE_KEYS=\"$test_pub\"|" "$script_dir/install.sh" >"$script"
grep -q "^RELEASE_KEYS=\"$test_pub\"\$" "$script" || { echo "install-test: could not swap the release key" >&2; exit 1; }

platforms="linux-amd64 linux-arm64 darwin-amd64 darwin-arm64"

# make_release TAG writes a complete release: every binary, each stamped with
# its tag so the installed bytes say which release they came from,
# checksums.txt, and a manifest in json.MarshalIndent form signed with the
# test key.
make_release() {
	rel="$fixtures/releases/$1"
	rm -rf "$rel"
	mkdir -p "$rel"
	for platform in $platforms; do
		{ cat "$fixtures/aether"; echo "# release $1 $platform"; } >"$rel/aether-$platform"
		case "$platform" in linux-*)
			{ cat "$fixtures/aether-server"; echo "# release $1 $platform"; } >"$rel/aether-server-$platform"
			;;
		esac
	done
	(cd "$rel" && for f in aether-*; do printf '%s  %s\n' "$(sha_of "$f")" "$f"; done) >"$rel/checksums.txt"
	{
		printf '{\n  "schema": 1,\n  "version": "%s",\n  "commit": "abc1234",\n' "$1"
		printf '  "published_at": "2026-10-17T00:00:00Z",\n  "notes_url": "https://example.invalid/notes",\n'
		printf '  "store_schema": 40,\n  "assets": {\n'
		first=yes
		for f in "$rel"/aether-*; do
			[ "$first" = yes ] || printf ',\n'
			first=no
			printf '    "%s": {\n      "sha256": "%s",\n      "size": %s\n    }' \
				"${f##*/}" "$(sha_of "$f")" "$(wc -c <"$f" | tr -d ' ')"
		done
		printf '\n  }\n}\n'
	} >"$rel/aether-release.json"
	sign_release "$1" "$tmp/test-key.pem"
}

sign_release() {
	openssl pkeyutl -sign -inkey "$2" -rawin -in "$fixtures/releases/$1/aether-release.json" |
		openssl base64 -A >"$fixtures/releases/$1/aether-release.json.sig"
	echo >>"$fixtures/releases/$1/aether-release.json.sig"
}

# api TAG:FLAGS ... writes the releases API answer, in that order, listing
# each tag's published files. FLAGS is any of d (draft) and p (prerelease).
# Release notes carry quotes, braces, escapes and decoy keys, and every asset
# has an uploader object, so a parser that matches lines instead of reading
# JSON picks the wrong fields.
api() {
	{
		printf '[\n'
		sep=""
		for spec in "$@"; do
			tag="${spec%%:*}" flags="${spec#*:}"
			draft=false pre=false
			case "$flags" in *d*) draft=true ;; esac
			case "$flags" in *p*) pre=true ;; esac
			printf '%s  {\n    "url": "https://api.github.com/x",\n    "author": {"login": "bot", "name": "aether-linux-amd64"},\n' "$sep"
			printf '    "tag_name": "%s",\n    "name": "Release %s",\n    "draft": %s,\n    "prerelease": %s,\n' "$tag" "$tag" "$draft" "$pre"
			printf '    "assets": ['
			asep=""
			if [ -d "$fixtures/releases/$tag" ]; then
				for f in "$fixtures/releases/$tag"/*; do
					[ -e "$f" ] || continue
					printf '%s\n      {"id": 1, "name": "%s", "uploader": {"login": "bot", "name": "x"}, "size": 3}' "$asep" "${f##*/}"
					asep=","
				done
			fi
			printf '\n    ],\n    "body": "fixes \\"tag_name\\": \\"v9.9.9\\", {\\"draft\\": false} [x] \\\\ \\\\\\"end"\n  }'
			sep=",
"
		done
		printf '\n]\n'
	} >"$fixtures/api.json"
	echo 200 >"$fixtures/api.status"
	: >"$fixtures/api.headers"
}

set_latest() { echo "$1" >"$fixtures/latest"; }

mkdir -p "$fixtures/releases"
make_release v0.0.0-test
set_latest v0.0.0-test
api v0.0.0-test:

# --- stubs ---------------------------------------------------------------

# Logs every URL it is asked for, refuses anything without --proto =https,
# and answers like curl: --fail turns a 404 into exit 22 with no file, -w
# prints the status or the final URL. STUB_OFFLINE fakes an unreachable
# GitHub, STUB_OFFLINE_API one that only drops the API, STUB_CUT a download
# cut off mid-transfer (exit 18), STUB_SHORT one that ends early without an
# error, STUB_404_ONCE an asset the CDN serves on the second try, and
# STUB_GONE (tag/file) one that the API lists but never serves, and
# STUB_NO_SIG a mirror that serves no signature at all.
cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
url="" out="" fmt="" headers="" fail=no https=no
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2" && shift 2 ;;
	-w) fmt="$2" && shift 2 ;;
	-D) headers="$2" && shift 2 ;;
	--proto) [ "$2" != "=https" ] || https=yes; shift 2 ;;
	-H | --proto-redir | --connect-timeout | --retry) shift 2 ;;
	--fail) fail=yes && shift ;;
	http*) url="$1" && shift ;;
	*) shift ;;
	esac
done
printf '%s\n' "$url" >>"$REQ_LOG"
[ "$https" = yes ] || { echo "curl stub: called without --proto =https" >&2; exit 1; }
offline="${STUB_OFFLINE:-}"
case "$url" in https://api.github.com/*) offline="${offline}${STUB_OFFLINE_API:-}" ;; esac
if [ -n "$offline" ]; then
	host="${url#https://}"
	echo "curl: (6) Could not resolve host: ${host%%/*}" >&2
	exit 6
fi
case "$url" in
https://github.com/*/releases/latest)
	printf '%s' "https://github.com/3xDevOps/Aether/releases/tag/$(cat "$FIXTURES/latest")"
	exit 0
	;;
https://api.github.com/repos/*/releases*)
	[ -z "$headers" ] || cp "$FIXTURES/api.headers" "$headers"
	cp "$FIXTURES/api.json" "$out"
	cat "$FIXTURES/api.status" | tr -d '\n'
	exit 0
	;;
https://github.com/*/releases/download/*)
	rest="${url#*/releases/download/}"
	tag="${rest%%/*}" file="${rest#*/}"
	src="$FIXTURES/releases/$tag/$file"
	[ "${STUB_GONE:-}" != "$tag/$file" ] || src=""
	case "$file" in *.sig) [ -z "${STUB_NO_SIG:-}" ] || src="" ;; esac
	if [ "${STUB_404_ONCE:-}" = "$file" ] && [ ! -e "$FIXTURES/404-once" ]; then
		: >"$FIXTURES/404-once"
		src=""
	fi
	if [ -n "$src" ] && [ -f "$src" ]; then
		if [ "${STUB_CUT:-}" = "$file" ] || [ "${STUB_SHORT:-}" = "$file" ]; then
			head -c 10 "$src" >"$out"
			if [ "${STUB_CUT:-}" = "$file" ]; then
				echo "curl: (18) transfer closed with outstanding read data remaining" >&2
				exit 18
			fi
		else
			cp "$src" "$out"
		fi
		printf '200'
		exit 0
	fi
	printf '404'
	[ "$fail" = no ] || { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
	exit 0
	;;
esac
echo "curl stub: unexpected URL $url" >&2
exit 1
EOF

# STUB_MV_FAIL=NAME fails the rename onto NAME, the way a busy or read-only
# destination would.
cat >"$bin/mv" <<'EOF'
#!/bin/sh
for last; do :; done
if [ -n "${STUB_MV_FAIL:-}" ] && [ "${last##*/}" = "$STUB_MV_FAIL" ]; then
	echo "mv: cannot move to '$last': Text file busy" >&2
	exit 1
fi
exec /bin/mv "$@"
EOF

cat >"$bin/sleep" <<'EOF'
#!/bin/sh
printf 'sleep %s\n' "$*" >>"$CMD_LOG"
EOF

# A fixed platform keeps the asset names the same on every developer machine.
# STUB_OS fakes a Mac, which is the only place the role default differs, and
# STUB_ARCH another CPU.
cat >"$bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
-m) echo "${STUB_ARCH:-x86_64}" ;;
*) echo "${STUB_OS:-Linux}" ;;
esac
EOF

# Not root unless STUB_UID says so, so the server path goes through sudo.
cat >"$bin/id" <<'EOF'
#!/bin/sh
echo "${STUB_UID:-1000}"
EOF

# Logs, then runs the command as a real root would. STUB_ROOT_DIR names the
# read-only stand-in for /usr/local/bin, whose permissions root would ignore;
# chown is only logged, because no test runs as a user who could do it.
# STUB_SUDO_FAILS is a sudo that cannot ask for a password.
cat >"$bin/sudo" <<'EOF'
#!/bin/sh
printf 'sudo %s\n' "$*" >>"$CMD_LOG"
if [ "${STUB_SUDO_FAILS:-0}" = 1 ]; then
	echo "sudo: a terminal is required to read the password; either use the -S option to read from standard input or configure an askpass helper" >&2
	exit 1
fi
case "${1:-}" in chown) exit 0 ;; esac
if [ -n "${STUB_ROOT_DIR:-}" ]; then
	chmod u+w "$STUB_ROOT_DIR"
	status=0
	"$@" || status=$?
	chmod a-w "$STUB_ROOT_DIR"
	exit "$status"
fi
exec "$@"
EOF

for tool in npm npx; do
	cat >"$node/$tool" <<EOF
#!/bin/sh
printf '$tool %s\n' "\$*" >>"\$CMD_LOG"
EOF
done

chmod 0755 "$bin"/* "$node"/* "$fixtures/aether" "$fixtures/aether-server"

# A PATH with no npm and no npx anywhere on it, for the case that proves the
# client role builds the app on a machine without Node. Dropping the
# directories that carry npm would also drop sh, awk and mktemp, so those
# directories are mirrored into a shadow of symlinks with npm and npx left
# out.
shadow="$tmp/no-node"
mkdir -p "$shadow"
no_node_path=""
oldifs="$IFS"
IFS=:
for d in $PATH; do
	[ -d "$d" ] || continue
	if [ ! -x "$d/npm" ] && [ ! -x "$d/npx" ]; then
		no_node_path="${no_node_path:+$no_node_path:}$d"
		continue
	fi
	for f in "$d"/*; do
		base="${f##*/}"
		case "$base" in npm | npx | '*') continue ;; esac
		[ -e "$shadow/$base" ] || ln -s "$f" "$shadow/$base" 2>/dev/null || true
	done
	case ":${no_node_path}:" in
	*":${shadow}:"*) ;;
	*) no_node_path="${no_node_path:+$no_node_path:}$shadow" ;;
	esac
done
IFS="$oldifs"
no_node_path="$bin:$bindir:$no_node_path"

export CMD_LOG="$log" FIXTURES="$fixtures" REQ_LOG="$reqs"
# Test seams: a unit path outside /etc, so the post-setup check is real
# without root, and a fake terminal per case (set by answer(), below).
export AETHER_UNIT_PATH="$tmp/aether-server.service"
# The system directory and HOME both point inside the sandbox, so no case can
# write to the real /usr/local/bin or the real ~/.local/bin.
export AETHER_SYSTEM_BIN_DIR="$sysbin" HOME="$home"
export AETHER_VERSION="v0.0.0-test" AETHER_BIN_DIR="$bindir"
export PATH="$bin:$node:$bindir:$PATH"

# --- harness -------------------------------------------------------------

case_name=""

fail() {
	echo "install-test: ${case_name}: $*" >&2
	echo "--- command log ---" >&2
	cat "$log" >&2 2>/dev/null || true
	echo "--- installer output ---" >&2
	cat "$out" >&2 2>/dev/null || true
	echo "--- installer stderr ---" >&2
	cat "$err" >&2 2>/dev/null || true
	exit 1
}

reset() {
	case_name="$1"
	: >"$log"
	: >"$reqs"
	: >"$out"
	: >"$err"
	rm -f "$bindir/aether" "$bindir/aether-server" "$AETHER_UNIT_PATH"
	chmod 0755 "$sysbin"
	rm -rf "${sysbin:?}"/* "$home/.local"
	rm -f "$answers"
}

# Writes the lines the fake terminal will hand back, one per prompt. No
# arguments means an empty file, which the installer sees as EOF.
answer() {
	: >"$answers"
	for line in "$@"; do printf '%s\n' "$line" >>"$answers"; done
}

answers="$tmp/answers"
# An existing empty file opens as a terminal that answers nothing.
faketty="$tmp/empty-tty"
: >"$faketty"

guard=""
command -v timeout >/dev/null 2>&1 && guard="timeout 120"

ran() { grep -Fq "$1" "$log" || fail "expected to run: $1"; }
not_ran() { ! grep -Fq "$1" "$log" || fail "should not have run: $1"; }
printed() { grep -Fq "$1" "$out" || fail "missing from output: $1"; }
not_printed() { ! grep -Fq "$1" "$out" || fail "should not be in output: $1"; }
# Only the cases that redirect stderr separately can use this; everywhere else
# stderr is folded into $out.
no_stderr() { [ ! -s "$err" ] || fail "wrote to stderr"; }
# Output that is not a terminal carries no color.
plain() { ! grep -q "$(printf '\033')" "$out" || fail "escape codes in output that is not a terminal"; }
activations() { grep -c "systemctl daemon-reload" "$out" || true; }
ends_with() { [ "$(tail -n 1 "$out")" = "$1" ] || fail "last line is not: $1"; }

question="What is this machine?"

# Every run prints the quickstart pointer.
quickstart="https://github.com/3xDevOps/Aether/blob/main/docs/quickstart.md"

# --- --role server: aether-server setup runs -----------------------------

# Setup's next steps hold the one activation line, and only the guide
# follows them.
reset "--role server"
AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
not_ran "aether gui build"
grep -q "^sudo .*aether-server setup$" "$log" || fail "setup did not go through sudo"
[ "$(activations)" = 1 ] || fail "printed the activation line $(activations) times, want once"
[ "$(sed -n '/systemctl daemon-reload/,$p' "$out")" = "$(printf '%s\n\n%s' \
	"       sudo systemctl daemon-reload && sudo systemctl enable --now aether-server" \
	"Read the quickstart before going further: $quickstart")" ] ||
	fail "more than the guide follows setup's next steps"
plain

# --- a failing setup still lands on the docs, with the rerun command -----

reset "--role server, setup fails"
SETUP_FAILS=1 AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
not_ran "aether gui build"
printed "This machine is your Aether server"
printed "$quickstart"
ends_with "  aether link <this-host>:2222   # from your own machine; the first link becomes admin"
printed "  sudo aether-server setup       # config, systemd unit, and how to start it"
plain

# --- declining setup exits 0 but writes nothing: no activation line ------

reset "--role server, setup declined"
SETUP_DECLINES=1 AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
printed "sudo aether-server setup"
[ "$(activations)" = 0 ] || fail "offered to activate a service that was never written"
printed "$quickstart"

# --- declining on a box that already has a unit: activating it is right --
#
# The installer's own next steps print it, once, with sudo for a user who
# is not root and without it for root.

reset "--role server, declined but a unit is already installed"
touch -t 200001010000 "$AETHER_UNIT_PATH"
SETUP_DECLINES=1 AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
printed "  sudo systemctl daemon-reload && sudo systemctl enable --now aether-server"
[ "$(activations)" = 1 ] || fail "printed the activation line $(activations) times, want once"
ends_with "  aether link <this-host>:2222   # from your own machine; the first link becomes admin"

reset "--role server as root, declined but a unit is already installed"
touch -t 200001010000 "$AETHER_UNIT_PATH"
SUDO_USER="" STUB_UID=0 SETUP_DECLINES=1 AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
! grep -q "^sudo .*aether-server setup$" "$log" || fail "root ran setup through sudo"
printed "  systemctl daemon-reload && systemctl enable --now aether-server"
not_printed "sudo systemctl"

# Piped to `sudo sh`, the installer is root but the person is not.
reset "--role server through sudo, declined but a unit is already installed"
touch -t 200001010000 "$AETHER_UNIT_PATH"
SUDO_USER=ubuntu STUB_UID=0 SETUP_DECLINES=1 AETHER_TTY="$faketty" sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
printed "  sudo systemctl daemon-reload && sudo systemctl enable --now aether-server"

reset "--role server, no terminal"
status=0
# shellcheck disable=SC2086
$guard env AETHER_TTY="$tmp/no-such-tty" sh "$script" --role server >"$out" 2>&1 || status=$?
[ "$status" != 0 ] || fail "installer exited 0 without setting up the server"
not_ran "aether-server setup"
[ -x "$bindir/aether-server" ] || fail "the server binary was not installed"
printed "sudo aether-server install --edge-url https://edge.onaether.dev --edge-access <account|approved-devices>"
not_printed "This machine is your Aether server"

# --- --role client: aether gui build runs --------------------------------

reset "--role client"
sh "$script" --role client >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
not_ran "aether-server setup"
printed "This machine is a client"
printed "$quickstart"
ends_with "  aether link <server-host>:2222 # the first link on a fresh server becomes admin"
plain

reset "--role client, gui build fails"
GUI_FAILS=1 sh "$script" --role client >"$out" 2>&1 || fail "installer exited non-zero"
ends_with "aether install: the aether CLI is installed in $bindir; the desktop app is not: \`aether gui build\` exited 1; its error is above"
not_printed "The desktop app was not built"

# --- --role none: binaries only ------------------------------------------

reset "--role none"
sh "$script" --role none >"$out" 2>&1 || fail "installer exited non-zero"
not_ran "aether gui build"
not_ran "aether-server setup"
printed "Nothing else was set up on this machine"
printed "$quickstart"
ends_with "  aether link <server>:2222      # from a client; the first link becomes admin"
plain

reset "--role none, unit already installed"
touch "$AETHER_UNIT_PATH"
sh "$script" --role none >"$out" 2>&1 || fail "installer exited non-zero"
printed "sudo aether-server update --version v0.0.0-test"
not_printed "sudo aether-server setup"

# --- AETHER_ROLE=client is the same as --role client ---------------------

reset "AETHER_ROLE=client"
AETHER_ROLE=client sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
not_ran "aether-server setup"

# --- --client implies the client role ------------------------------------

reset "--client"
sh "$script" --client >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
[ ! -e "$bindir/aether-server" ] || fail "--client installed the server binary"

# --- --server implies the server role ------------------------------------

reset "--server"
AETHER_TTY="$faketty" sh "$script" --server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
not_ran "aether gui build"

# --- a role naming a binary that was never installed ---------------------

reset "--server --role client"
sh "$script" --server --role client >"$out" 2>&1 || fail "installer exited non-zero"
not_ran "aether gui build"
printed "the aether CLI was not installed here"
not_printed "No such file or directory"
printed "$quickstart"

# --- flags and answers are case-insensitive ------------------------------

reset "--role SERVER"
AETHER_TTY="$faketty" sh "$script" --role SERVER >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"

# --- the question itself, over a fake terminal ---------------------------
#
# Every case above bypasses the prompt. These drive it: AETHER_TTY is an
# undocumented seam that points the installer's terminal reads at a file of
# canned answers.

reset "prompt: server"
answer server
AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
printed "$question"
printed "role [server]:"
ran "aether-server setup"
not_ran "aether gui build"

reset "prompt: client"
answer client
AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
not_ran "aether-server setup"

reset "prompt: Enter takes server on linux"
answer ""
AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
printed "role [server]:"
ran "aether-server setup"

# The platform default must not answer the question: a Mac gets the client
# binary automatically and is still asked, with client as the default.
reset "prompt: Enter takes client on macOS"
answer ""
STUB_OS=Darwin AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
printed "$question"
printed "role [client]:"
ran "aether gui build"
not_ran "aether-server setup"
[ ! -e "$bindir/aether-server" ] || fail "macOS installed the server binary"

# An explicit component choice, unlike the platform default, does answer it.
reset "prompt: AETHER_COMPONENTS=client answers the question"
answer server
AETHER_COMPONENTS=client AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
not_printed "$question"
ran "aether gui build"
not_ran "aether-server setup"

reset "prompt: an unrecognised answer reprompts"
answer wat none
AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
printed "answer server, client, or none"
not_ran "aether gui build"
not_ran "aether-server setup"
printed "Nothing else was set up on this machine"

reset "prompt: answers are case-insensitive"
answer CLIENT
AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"

reset "prompt: a terminal that closes takes the default"
answer
# shellcheck disable=SC2086
$guard env AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer blocked or exited non-zero"
ran "aether-server setup"

# --- Ctrl-C during the follow-up stops the installer ---------------------

reset "interrupted build with inherited ignored SIGINT"
status=0
(
	trap '' INT
	GUI_INTERRUPTS=1 sh "$script" --role client
) >"$out" 2>&1 || status=$?
[ "$status" = 130 ] || fail "expected exit 130 after an interrupt, got $status"
not_printed "This machine is a client"
not_printed "$quickstart"

# --- no terminal at all: ask nothing, run nothing, do not block ----------

reset "unopenable terminal"
# shellcheck disable=SC2086
$guard env AETHER_TTY="$tmp/no-such-tty" sh "$script" >"$out" 2>&1 || fail "installer blocked or exited non-zero"
not_printed "$question"
not_ran "aether gui build"
not_ran "aether-server setup"
printed "Nothing else was set up on this machine"
printed "$quickstart"

# --- the same, with no controlling terminal rather than the seam ---------

reset "detached, no role"
detach=""
if ! (exec 3</dev/tty) 2>/dev/null; then
	detach="env"
elif command -v setsid >/dev/null 2>&1; then
	detach="setsid -w"
fi
if [ -n "$detach" ]; then
	# shellcheck disable=SC2086
	$guard $detach sh "$script" >"$out" 2>&1 || fail "installer blocked or exited non-zero"
	not_ran "aether gui build"
	not_ran "aether-server setup"
	printed "Nothing else was set up on this machine"
	printed "$quickstart"
else
	# The "unopenable terminal" case above covers the same branch, so this is
	# a bonus check rather than the only guard.
	echo "install-test: a terminal is attached and setsid is missing; skipping the detached run" >&2
fi

# --- client with no Node: the CLI supplies its own, so the build runs ----
#
# The stub aether does not really build; this asserts the installer stopped
# gating the build on npm, not that a build succeeded.

reset "client without npm or npx on PATH"
PATH="$no_node_path" sh "$script" --role client >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
not_ran "aether-server setup"
printed "This machine is a client"
printed "aether link <server-host>:2222"
not_printed "Node.js 22+"
printed "$quickstart"

# --- where each role installs --------------------------------------------
#
# These are the only cases that drop AETHER_BIN_DIR, so the resolved role
# picks the directory. Each runs in its own subshell so the unset stays there.

reset "client role installs the CLI alone into ~/.local/bin, without sudo"
(
	unset AETHER_BIN_DIR
	sh "$script" --role client
) >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$home/.local/bin/aether" ] || fail "the CLI did not land in ~/.local/bin"
not_ran "sudo "
printed "installed $home/.local/bin/aether"
ran "aether gui build"
# An aether-server next to the CLI makes `aether update` treat this machine as
# a server, so a client answer must not download or install one.
[ ! -e "$home/.local/bin/aether-server" ] || fail "a client answer installed the server binary"
not_printed "aether-server-linux-amd64"

reset "an explicit component set still wins over the client role"
(
	unset AETHER_BIN_DIR
	AETHER_COMPONENTS=both sh "$script" --role client
) >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$home/.local/bin/aether" ] || fail "the CLI did not land in ~/.local/bin"
[ -x "$home/.local/bin/aether-server" ] || fail "AETHER_COMPONENTS=both was overridden"

reset "server role installs into /usr/local/bin with sudo and root ownership"
chmod 0555 "$sysbin"
(
	unset AETHER_BIN_DIR
	STUB_ROOT_DIR="$sysbin" AETHER_TTY="$faketty" sh "$script" --role server
) >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$sysbin/aether-server" ] || fail "the server did not land in /usr/local/bin"
[ ! -e "$home/.local/bin/aether-server" ] || fail "the server went to ~/.local/bin"
ran "sudo cp"
ran "sudo mv -f $sysbin/.aether-server.install."
printed "installed $sysbin/aether-server"

reset "--bin-dir wins over the client role"
sh "$script" --role client --bin-dir "$bindir" >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$bindir/aether" ] || fail "--bin-dir was ignored"
[ ! -e "$home/.local/bin/aether" ] || fail "--bin-dir still used ~/.local/bin"

reset "--bin-dir wins over the server role"
AETHER_TTY="$faketty" sh "$script" --role server --bin-dir "$bindir" >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$bindir/aether-server" ] || fail "--bin-dir was ignored"
[ ! -e "$sysbin/aether-server" ] || fail "--bin-dir still used /usr/local/bin"

reset "role none keeps /usr/local/bin"
(
	unset AETHER_BIN_DIR
	sh "$script" --role none
) >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$sysbin/aether" ] || fail "role none did not use /usr/local/bin"
[ ! -e "$home/.local/bin/aether" ] || fail "role none used ~/.local/bin"
not_printed "would shadow"

reset "role none without a usable sudo points at the no-sudo client install"
status=0
(
	unset AETHER_BIN_DIR
	STUB_SUDO_FAILS=1 sh "$script" --role none
) >"$out" 2>&1 || status=$?
[ "$status" != 0 ] || fail "installer exited 0 though sudo failed"
printed "sh -s -- --client --role none --bin-dir \"\$HOME/.local/bin\""

# --- the PATH line, one per shell ----------------------------------------
#
# ~/.local/bin is not on the test PATH, so the client role always prints it.

path_case() {
	reset "PATH hint for ${1##*/}"
	(
		unset AETHER_BIN_DIR
		SHELL="$1" STUB_OS="${3:-Linux}" sh "$script" --role client
	) >"$out" 2>"$err" || fail "installer exited non-zero"
	no_stderr
	printed "$home/.local/bin is not on your PATH"
	printed "$2"
	printed "the desktop app looks in ~/.local/bin itself"
}

path_case /usr/bin/fish "fish_add_path $home/.local/bin"
path_case /bin/zsh '>> ~/.zshrc'
path_case /bin/bash '>> ~/.bashrc'
path_case /bin/bash '>> ~/.bash_profile' Darwin
path_case /bin/ksh "add this to your shell profile"

# No SHELL at all: a container, a cron job, a CI runner. `${SHELL##*/}` under
# `set -u` is a fatal "parameter not set" there, which left a blank line where
# the fix should be. A shell that sets SHELL for itself - bash does - takes one
# of the named branches instead, so this is only a real test of the fallback
# under dash or busybox ash; what it asserts holds either way.
reset "no SHELL: still one runnable line, and nothing on stderr"
(
	unset AETHER_BIN_DIR
	unset SHELL
	sh "$script" --role client
) >"$out" 2>"$err" || fail "installer exited non-zero"
no_stderr
printed "$home/.local/bin is not on your PATH"
grep -q "^  .*${home}/.local/bin" "$out" || fail "the PATH hint names no directory"

# The same case under a shell that leaves SHELL alone, which is the one the
# `curl ... | sh` line lands on. dash and busybox ash are the two that ship as
# /bin/sh; either proves the generic fallback branch, and CI has dash.
strict=""
if command -v dash >/dev/null 2>&1; then
	strict="dash"
elif command -v busybox >/dev/null 2>&1; then
	strict="busybox sh"
fi
if [ -n "$strict" ]; then
	reset "no SHELL under $strict: the generic export"
	(
		unset AETHER_BIN_DIR
		unset SHELL
		# shellcheck disable=SC2086
		$strict "$script" --role client
	) >"$out" 2>"$err" || fail "installer exited non-zero"
	no_stderr
	printed "add this to your shell profile"
	grep -q "^  .*${home}/.local/bin" "$out" || fail "the PATH hint names no directory"
else
	echo "install-test: no dash or busybox; skipping the strict-shell no-SHELL case" >&2
fi

reset "no PATH hint when the directory is already on PATH"
(
	unset AETHER_BIN_DIR
	PATH="$home/.local/bin:$PATH" sh "$script" --role client
) >"$out" 2>"$err" || fail "installer exited non-zero"
no_stderr
not_printed "is not on your PATH"
not_printed "the desktop app looks in"

reset "a trailing slash on --bin-dir does not fake a PATH miss"
sh "$script" --role client --bin-dir "$bindir/" >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$bindir/aether" ] || fail "--bin-dir with a trailing slash installed nothing"
not_printed "is not on your PATH"

# --- an older /usr/local/bin/aether would shadow the new one -------------

reset "stale /usr/local/bin/aether on a client"
: >"$sysbin/aether"
(
	unset AETHER_BIN_DIR
	sh "$script" --role client
) >"$out" 2>&1 || fail "installer exited non-zero"
printed "an older $sysbin/aether would shadow $home/.local/bin/aether"
printed "sudo rm -f $sysbin/aether"

reset "stale /usr/local/bin/aether warns for an explicit --bin-dir too"
: >"$sysbin/aether"
sh "$script" --role client --bin-dir "$bindir" >"$out" 2>&1 || fail "installer exited non-zero"
printed "an older $sysbin/aether would shadow $bindir/aether"

# Without normalising the path the check compares the install directory
# against itself and tells the user to delete what was just written.
reset "a trailing slash on --bin-dir does not warn about its own binary"
: >"$sysbin/aether"
sh "$script" --role client --bin-dir "$sysbin/" >"$out" 2>&1 || fail "installer exited non-zero"
[ -x "$sysbin/aether" ] || fail "--bin-dir with a trailing slash installed nothing"
not_printed "would shadow"

# These drop AETHER_VERSION, so the installer resolves releases/latest and,
# when that tag is incomplete, the releases API. Each starts from an empty
# fake host and an existing install it must either replace or leave alone.

requested() { grep -Fq "$1" "$reqs" || fail "expected a request for $1"; }
not_requested() { ! grep -Fq "$1" "$reqs" || fail "should not have requested $1"; }
installed_from() {
	grep -Fq "# release $1" "$bindir/${3:-aether}" || fail "${3:-aether} is not from $1 $2"
	grep -Fq "$2" "$bindir/${3:-aether}" || fail "${3:-aether} is not the $2 binary"
}
untouched() {
	[ "$(cat "$bindir/aether")" = "old cli" ] || fail "the existing aether was replaced"
	[ "$(cat "$bindir/aether-server")" = "old server" ] || fail "the existing aether-server was replaced"
	no_staged
}
no_staged() {
	for f in "$bindir"/.*.install.*; do
		[ ! -e "$f" ] || fail "a staged binary was left behind: $f"
	done
}

host() {
	reset "$1"
	rm -rf "$fixtures/releases" "$fixtures/404-once"
	mkdir -p "$fixtures/releases"
	printf 'old cli' >"$bindir/aether"
	printf 'old server' >"$bindir/aether-server"
}

latest_install() {
	(
		unset AETHER_VERSION
		sh "$script" --role none "$@"
	) >"$out" 2>&1
}

complete="the newest complete release"

host "latest exists with no assets: the previous complete release installs"
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.12 is not fully published yet (no checksums.txt); installing v0.7.0-alpha.11, $complete"
installed_from v0.7.0-alpha.11 linux-amd64 aether
installed_from v0.7.0-alpha.11 linux-amd64 aether-server
printed "verified aether-release.json.sig for v0.7.0-alpha.11 against release key $test_pub"
requested "https://api.github.com/repos/3xDevOps/Aether/releases?per_page=100"
grep "/releases/download/" "$reqs" | grep -v "/v0.7.0-alpha.12/checksums.txt$" | grep -vq "/v0.7.0-alpha.11/" &&
	fail "downloaded from a second release"
latest_install || fail "rerunning the installer failed"
installed_from v0.7.0-alpha.11 linux-amd64 aether
no_staged

host "the releases API answer as compact JSON"
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
tr -d '\n' <"$fixtures/api.json" >"$fixtures/api.compact" && mv "$fixtures/api.compact" "$fixtures/api.json"
latest_install || fail "installer exited non-zero"
installed_from v0.7.0-alpha.11 linux-amd64

host "latest has its signature but no manifest yet: fall back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-release.json"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.12 is not fully published yet (no aether-release.json); installing v0.7.0-alpha.11, $complete"
installed_from v0.7.0-alpha.11 linux-amd64

host "latest has its manifest but no signature yet: fall back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-release.json.sig"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.12 is not fully published yet (no aether-release.json.sig); installing v0.7.0-alpha.11, $complete"
installed_from v0.7.0-alpha.11 linux-amd64

host "a complete latest release from before signing installs on checksums.txt"
make_release v0.7.0-alpha.11
rm "$fixtures/releases/v0.7.0-alpha.11/aether-release.json" "$fixtures/releases/v0.7.0-alpha.11/aether-release.json.sig"
set_latest v0.7.0-alpha.11
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.11 has no signed aether-release.json (it predates release signing); verifying SHA-256 against checksums.txt only"
printed "verified aether-linux-amd64, aether-server-linux-amd64 against checksums.txt only"
installed_from v0.7.0-alpha.11 linux-amd64
not_requested "https://api.github.com/"

# A tag at or after the first signed release with neither manifest file is
# mid-publish or has been stripped, not old.
host "a release that must be signed, with both manifest files gone: fall back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.13
rm "$fixtures/releases/v0.7.0-alpha.13/aether-release.json" "$fixtures/releases/v0.7.0-alpha.13/aether-release.json.sig"
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-release.json" "$fixtures/releases/v0.7.0-alpha.12/aether-release.json.sig"
set_latest v0.7.0-alpha.13
api v0.7.0-alpha.13: v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.13 is not fully published yet (no aether-release.json); installing v0.7.0-alpha.11, $complete"
not_requested "/v0.7.0-alpha.12/"
installed_from v0.7.0-alpha.11 linux-amd64

host "latest is missing this platform's binary: fall back after the CDN retries"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-linux-amd64"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.12 is not fully published yet (no aether-linux-amd64); installing v0.7.0-alpha.11, $complete"
[ "$(grep -c '^sleep' "$log")" = 3 ] || fail "expected three CDN retries"
installed_from v0.7.0-alpha.11 linux-amd64

host "latest's manifest lists a binary checksums.txt omits: fail, do not fall back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
grep -v ' aether-server-linux-amd64$' "$fixtures/releases/v0.7.0-alpha.12/checksums.txt" >"$tmp/sums"
mv "$tmp/sums" "$fixtures/releases/v0.7.0-alpha.12/checksums.txt"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if latest_install; then fail "installed despite the disagreement"; fi
printed "aether-release.json for v0.7.0-alpha.12 lists aether-server-linux-amd64 but checksums.txt does not; not installing it"
not_requested "https://api.github.com/"
untouched

host "a release whose checksums.txt and manifest both omit a binary is incomplete"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-server-linux-amd64"
(cd "$fixtures/releases/v0.7.0-alpha.12" && grep -v ' aether-server-linux-amd64$' checksums.txt >sums && mv sums checksums.txt)
awk '/"aether-server-linux-amd64": \{/ { skip = 4 } skip { skip--; next } { print }' \
	"$fixtures/releases/v0.7.0-alpha.12/aether-release.json" >"$tmp/manifest"
mv "$tmp/manifest" "$fixtures/releases/v0.7.0-alpha.12/aether-release.json"
sign_release v0.7.0-alpha.12 "$tmp/test-key.pem"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
printed "v0.7.0-alpha.12 is not fully published yet (checksums.txt does not list aether-server-linux-amd64); installing v0.7.0-alpha.11"
installed_from v0.7.0-alpha.11 linux-amd64 aether-server

# Readiness is per install: a client install does not need aether-server.
reset "a client install takes a latest release missing only the server"
printf 'old cli' >"$bindir/aether"
latest_install --client || fail "installer exited non-zero"
installed_from v0.7.0-alpha.12 linux-amd64
not_requested "https://api.github.com/"

host "prereleases and drafts are ignored, newest semver wins"
make_release v0.8.0-rc.1
make_release v0.7.1
make_release v0.7.0-alpha.9
make_release v0.7.0-alpha.10
make_release v0.7.0-alpha.10.5
rm "$fixtures/releases/v0.7.0-alpha.10.5/aether-release.json"
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.9.0"
set_latest v0.9.0
api v0.7.0-alpha.9: v0.8.0-rc.1:p v0.7.0-alpha.10.5: v0.9.0: v0.7.1:d v0.7.0-alpha.11: v0.7.0-alpha.10: not-a-version:
STUB_GONE=v0.7.0-alpha.11/aether-server-linux-amd64 latest_install || fail "installer exited non-zero"
printed "skipping v0.7.0-alpha.11: not fully published (no aether-server-linux-amd64)"
not_requested "/v0.7.0-alpha.10.5/"
printed "v0.9.0 is not fully published yet (no checksums.txt); installing v0.7.0-alpha.10, $complete"
installed_from v0.7.0-alpha.10 linux-amd64
not_requested "/v0.8.0-rc.1/"
not_requested "/v0.7.1/"
not_requested "/v0.7.0-alpha.9/"

host "a fallback is never newer than latest"
make_release v0.8.0
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.8.0: v0.7.0-alpha.12: v0.7.0-alpha.11:
latest_install || fail "installer exited non-zero"
installed_from v0.7.0-alpha.11 linux-amd64
not_requested "/v0.8.0/"

# A mirror or an asset editor that 404s every signature must not turn the
# fallback into an install of an unsigned release.
host "every signature missing: no fallback to an unsigned release"
make_release v0.7.0-alpha.11
rm "$fixtures/releases/v0.7.0-alpha.11/aether-release.json" "$fixtures/releases/v0.7.0-alpha.11/aether-release.json.sig"
make_release v0.7.0-alpha.12
make_release v0.7.0-alpha.13
set_latest v0.7.0-alpha.13
api v0.7.0-alpha.13: v0.7.0-alpha.12: v0.7.0-alpha.11:
if STUB_NO_SIG=1 latest_install; then fail "installed with every signature missing"; fi
printed "skipping v0.7.0-alpha.12: not fully published (no aether-release.json.sig)"
printed "no complete release for linux/amd64"
not_requested "/v0.7.0-alpha.11/"
untouched

host "no complete release at all"
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12:
if latest_install; then fail "installed with no complete release"; fi
printed "no complete release for linux/amd64 among the newest 100 releases of 3xDevOps/Aether; pass --version <tag>"
untouched

host "checksum mismatch on latest fails without falling back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
tr 'a' 'b' <"$fixtures/releases/v0.7.0-alpha.12/aether-linux-amd64" >"$tmp/corrupt"
mv "$tmp/corrupt" "$fixtures/releases/v0.7.0-alpha.12/aether-linux-amd64"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if latest_install; then fail "a corrupt binary installed"; fi
printed "checksum mismatch for aether-linux-amd64 v0.7.0-alpha.12"
not_requested "https://api.github.com/"
not_requested "/v0.7.0-alpha.11/"
untouched

host "a manifest signed by an untrusted key fails without falling back"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
openssl genpkey -algorithm ed25519 -out "$tmp/other-key.pem" 2>/dev/null
sign_release v0.7.0-alpha.12 "$tmp/other-key.pem"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if latest_install; then fail "an untrusted manifest installed"; fi
printed "aether-release.json for v0.7.0-alpha.12 is not signed by a trusted release key; not installing it"
not_requested "https://api.github.com/"
not_requested "/v0.7.0-alpha.12/aether-linux-amd64"
untouched

host "the shipped installer trusts only the real release key"
make_release v0.7.0-alpha.12
if AETHER_VERSION=v0.7.0-alpha.12 sh "$script_dir/install.sh" --role none >"$out" 2>&1; then
	fail "the real installer accepted a test-signed manifest"
fi
printed "is not signed by a trusted release key"
untouched

host "an older release's signed manifest replayed under a new tag"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
cp "$fixtures/releases/v0.7.0-alpha.11/aether-release.json" "$fixtures/releases/v0.7.0-alpha.11/aether-release.json.sig" \
	"$fixtures/releases/v0.7.0-alpha.12/"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if latest_install; then fail "a replayed manifest installed"; fi
printed "aether-release.json fetched for v0.7.0-alpha.12 describes v0.7.0-alpha.11; not installing it"
not_requested "https://api.github.com/"
untouched

host "checksums.txt disagreeing with the signed manifest"
make_release v0.7.0-alpha.12
sed "s/^[0-9a-f]* \( aether-linux-amd64\)$/$(printf '%064d' 0) \1/" "$fixtures/releases/v0.7.0-alpha.12/checksums.txt" >"$tmp/sums"
mv "$tmp/sums" "$fixtures/releases/v0.7.0-alpha.12/checksums.txt"
set_latest v0.7.0-alpha.12
if latest_install; then fail "disagreeing checksums installed"; fi
printed "aether-release.json and checksums.txt for v0.7.0-alpha.12 disagree about aether-linux-amd64"
untouched

host "an interrupted download leaves the existing install untouched"
make_release v0.7.0-alpha.11
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if STUB_CUT=aether-server-linux-amd64 latest_install; then fail "an interrupted download installed"; fi
printed "curl: (18) transfer closed"
printed "(curl exit 18)"
not_requested "https://api.github.com/"
untouched

host "a download that ends early without an error is a size mismatch"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
if STUB_SHORT=aether-linux-amd64 latest_install; then fail "a short download installed"; fi
printed "size mismatch for aether-linux-amd64 v0.7.0-alpha.12"
not_requested "https://api.github.com/"
untouched

host "a failed rename after the server was replaced says what changed"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
if STUB_MV_FAIL=aether latest_install; then fail "a failed rename succeeded"; fi
installed_from v0.7.0-alpha.12 linux-amd64 aether-server
[ "$(cat "$bindir/aether")" = "old cli" ] || fail "the CLI was replaced"
printed "stopped while replacing binaries; now v0.7.0-alpha.12: $bindir/aether-server; still the previous version: aether"
no_staged

host "a binary the CDN has not caught up with is retried"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
STUB_404_ONCE=aether-linux-amd64 latest_install || fail "installer exited non-zero"
ran "sleep 2"
installed_from v0.7.0-alpha.12 linux-amd64

host "without OpenSSL 3 the signature check is skipped out loud"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
mkdir -p "$tmp/libressl"
cat >"$tmp/libressl/openssl" <<'EOF'
#!/bin/sh
[ "$1" = version ] && echo "LibreSSL 3.3.6" && exit 0
exit 1
EOF
chmod 0755 "$tmp/libressl/openssl"
PATH="$tmp/libressl:$PATH" latest_install || fail "installer exited non-zero"
printed "warning: aether-release.json.sig not verified: that needs OpenSSL 3 (found: LibreSSL 3.3.6); checking downloads against checksums.txt and aether-release.json only"
printed "verified aether-linux-amd64, aether-server-linux-amd64 against aether-release.json and checksums.txt, without a signature check"
installed_from v0.7.0-alpha.12 linux-amd64

host "an OpenSSL 3 that cannot check ed25519 shows its own error"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
mkdir -p "$tmp/no-ed25519"
cat >"$tmp/no-ed25519/openssl" <<EOF
#!/bin/sh
case "\$1" in
version) echo "OpenSSL 3.0.13 30 Jan 2024" ;;
pkeyutl) echo "pkeyutl: Error initializing context: unsupported algorithm ED25519" >&2 && exit 1 ;;
*) exec "$(command -v openssl)" "\$@" ;;
esac
EOF
chmod 0755 "$tmp/no-ed25519/openssl"
if PATH="$tmp/no-ed25519:$PATH" latest_install; then fail "installed without checking the signature"; fi
printed "cannot check aether-release.json.sig for v0.7.0-alpha.12 with OpenSSL 3.0.13 30 Jan 2024: pkeyutl: Error initializing context: unsupported algorithm ED25519"
not_printed "not signed by a trusted release key"
untouched

reset "a bin dir with a space in it"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
mkdir -p "$tmp/with space"
(
	unset AETHER_VERSION
	sh "$script" --role none --bin-dir "$tmp/with space"
) >"$out" 2>&1 || fail "installer exited non-zero"
grep -Fq "# release v0.7.0-alpha.12" "$tmp/with space/aether" || fail "the CLI did not land in the spaced directory"
for f in "$tmp/with space"/.*.install.*; do [ ! -e "$f" ] || fail "left $f behind"; done

host "pinned complete release installs without asking for latest"
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
sh "$script" --role none --version v0.7.0-alpha.11 >"$out" 2>&1 || fail "installer exited non-zero"
installed_from v0.7.0-alpha.11 linux-amd64
not_requested "/releases/latest"
not_requested "https://api.github.com/"

host "pinned incomplete release fails without falling back"
make_release v0.7.0-alpha.11
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12: v0.7.0-alpha.11:
if sh "$script" --role none --version v0.7.0-alpha.12 >"$out" 2>&1; then fail "an incomplete pin installed"; fi
printed "v0.7.0-alpha.12 has no published checksums.txt; it may still be building or may have failed - not installing a different version"
not_requested "/v0.7.0-alpha.11/"
not_requested "https://api.github.com/"
untouched

host "pinned release with a manifest but no signature fails"
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-release.json.sig"
if sh "$script" --role none --version v0.7.0-alpha.12 >"$out" 2>&1; then fail "an unsigned manifest installed"; fi
printed "v0.7.0-alpha.12 has no published aether-release.json.sig; it may still be building"
untouched

host "pinned release missing this platform's binary fails"
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-server-linux-amd64"
if sh "$script" --role none --version v0.7.0-alpha.12 >"$out" 2>&1; then fail "a partial pin installed"; fi
printed "v0.7.0-alpha.12 has no published aether-server-linux-amd64; it may still be building or may have failed - not installing a different version"
untouched

host "pinned release from before signing installs on checksums.txt alone"
make_release v0.7.0-alpha.11
rm "$fixtures/releases/v0.7.0-alpha.11/aether-release.json" "$fixtures/releases/v0.7.0-alpha.11/aether-release.json.sig"
sh "$script" --role none --version v0.7.0-alpha.11 >"$out" 2>&1 || fail "installer exited non-zero"
printed "v0.7.0-alpha.11 has no signed aether-release.json (it predates release signing); verifying SHA-256 against checksums.txt only"
installed_from v0.7.0-alpha.11 linux-amd64

host "pinned release that must be signed, with both manifest files gone, fails"
make_release v0.7.0-alpha.12
rm "$fixtures/releases/v0.7.0-alpha.12/aether-release.json" "$fixtures/releases/v0.7.0-alpha.12/aether-release.json.sig"
if sh "$script" --role none --version v0.7.0-alpha.12 >"$out" 2>&1; then fail "an unsigned new release installed"; fi
printed "v0.7.0-alpha.12 has no published aether-release.json; it may still be building or may have failed - not installing a different version"
untouched

host "pinned release with a checksum mismatch fails"
make_release v0.7.0-alpha.11
rm "$fixtures/releases/v0.7.0-alpha.11/aether-release.json" "$fixtures/releases/v0.7.0-alpha.11/aether-release.json.sig"
tr 'a' 'b' <"$fixtures/releases/v0.7.0-alpha.11/aether-linux-amd64" >"$tmp/corrupt"
mv "$tmp/corrupt" "$fixtures/releases/v0.7.0-alpha.11/aether-linux-amd64"
if sh "$script" --role none --version v0.7.0-alpha.11 >"$out" 2>&1; then fail "a corrupt pin installed"; fi
printed "checksum mismatch for aether-linux-amd64 v0.7.0-alpha.11"
untouched

host "checksums.txt listing a binary twice fails"
make_release v0.7.0-alpha.12
grep ' aether-linux-amd64$' "$fixtures/releases/v0.7.0-alpha.12/checksums.txt" >"$tmp/dup"
cat "$tmp/dup" >>"$fixtures/releases/v0.7.0-alpha.12/checksums.txt"
if sh "$script" --role none --version v0.7.0-alpha.12 >"$out" 2>&1; then fail "a duplicate entry installed"; fi
printed "checksums.txt for v0.7.0-alpha.12 lists aether-linux-amd64 more than once"
untouched

host "a pinned tag that is not a tag is refused before any download"
if sh "$script" --role none --version ../v1 >"$out" 2>&1; then fail "a path installed"; fi
printed "invalid release tag ../v1"
[ ! -s "$reqs" ] || fail "an invalid tag was requested"
untouched

host "API rate limit"
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12:
echo 403 >"$fixtures/api.status"
printf 'x-ratelimit-limit: 60\r\nx-ratelimit-remaining: 0\r\nx-ratelimit-reset: 1792209652\r\n' >"$fixtures/api.headers"
printf '{"message": "API rate limit exceeded for 192.0.2.1."}\n' >"$fixtures/api.json"
if latest_install; then fail "installed through a rate limit"; fi
printed "GitHub API rate limit exceeded (HTTP 403 from https://api.github.com/repos/3xDevOps/Aether/releases?per_page=100, x-ratelimit-remaining: 0, x-ratelimit-reset: 1792209652); pass --version <tag> to install a specific release"
untouched

host "any other API error shows its status and message"
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12:
echo 403 >"$fixtures/api.status"
printf 'x-ratelimit-remaining: 41\r\n' >"$fixtures/api.headers"
printf '{\n  "message": "Repository access blocked"\n}\n' >"$fixtures/api.json"
if latest_install; then fail "installed through an API error"; fi
printed "GET https://api.github.com/repos/3xDevOps/Aether/releases?per_page=100: HTTP 403: Repository access blocked"
not_printed "rate limit"

host "a compact API error body still shows its message"
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
api v0.7.0-alpha.12:
echo 403 >"$fixtures/api.status"
printf 'x-ratelimit-remaining: 41\r\n' >"$fixtures/api.headers"
printf '{"message":"You have exceeded a secondary rate limit","documentation_url":"https://docs.github.com/x"}' >"$fixtures/api.json"
if latest_install; then fail "installed through an API error"; fi
printed "HTTP 403: You have exceeded a secondary rate limit"

host "GitHub unreachable"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
if STUB_OFFLINE=1 latest_install; then fail "installed while offline"; fi
printed "curl: (6) Could not resolve host: github.com"
printed "cannot reach https://github.com/3xDevOps/Aether/releases/latest"
not_printed "no published release"
untouched

host "API unreachable"
mkdir -p "$fixtures/releases/v0.7.0-alpha.12"
set_latest v0.7.0-alpha.12
if STUB_OFFLINE_API=1 latest_install; then fail "installed with the API down"; fi
printed "curl: (6) Could not resolve host: api.github.com"
printed "cannot reach https://api.github.com/repos/3xDevOps/Aether/releases?per_page=100"
untouched

host "darwin/arm64 gets aether-darwin-arm64 and nothing else"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
STUB_OS=Darwin STUB_ARCH=arm64 latest_install || fail "installer exited non-zero"
installed_from v0.7.0-alpha.12 darwin-arm64
requested "/v0.7.0-alpha.12/aether-darwin-arm64"
not_requested "linux-"
[ "$(cat "$bindir/aether-server")" = "old server" ] || fail "macOS replaced aether-server"

host "linux/amd64 gets both linux-amd64 binaries"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
latest_install || fail "installer exited non-zero"
installed_from v0.7.0-alpha.12 linux-amd64 aether
installed_from v0.7.0-alpha.12 linux-amd64 aether-server
not_requested "arm64"
not_requested "darwin"

host "linux/arm64 gets both linux-arm64 binaries"
make_release v0.7.0-alpha.12
set_latest v0.7.0-alpha.12
STUB_ARCH=aarch64 latest_install || fail "installer exited non-zero"
installed_from v0.7.0-alpha.12 linux-arm64 aether
installed_from v0.7.0-alpha.12 linux-arm64 aether-server

case_name="release keys"
go_keys="$(grep -o '"[A-Za-z0-9+/]\{43\}="' "$script_dir/../internal/selfupdate/keys.go" | tr -d '"' | sort | tr '\n' ' ')"
sh_keys="$(sed -n 's/^RELEASE_KEYS="\(.*\)"$/\1/p' "$script_dir/install.sh" | tr ' ' '\n' | sort | tr '\n' ' ')"
[ -n "$go_keys" ] && [ "$go_keys" = "$sh_keys" ] ||
	fail "RELEASE_KEYS in install.sh ($sh_keys) differs from internal/selfupdate/keys.go ($go_keys)"

# --- an unknown role is a usage error ------------------------------------

reset "--role bogus"
if sh "$script" --role bogus >"$out" 2>&1; then
	fail "an unknown role was accepted"
fi
printed "unknown role bogus"
[ ! -e "$bindir/aether" ] || fail "a bad role downloaded anything"

echo "install-test: ok"
