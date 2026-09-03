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

script="$script_dir/install.sh"
bin="$tmp/bin"           # stubs the installer itself calls
node="$tmp/node"         # npm and npx, dropped from PATH in one case
bindir="$tmp/bindir"     # AETHER_BIN_DIR: where the fake binaries land
fixtures="$tmp/fixtures" # what the fake curl serves
log="$tmp/cmd.log"
out="$tmp/out.txt"
mkdir -p "$bin" "$node" "$bindir" "$fixtures"

sha_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# --- the release assets the installer downloads --------------------------

# GUI_INTERRUPTS signals the installer the way a terminal delivers Ctrl-C
# during a long build, before the stub logs anything.
cat >"$fixtures/aether" <<'EOF'
#!/bin/sh
if [ "${GUI_INTERRUPTS:-0}" = 1 ] && [ "${1:-}" = gui ]; then
	kill -INT "$PPID"
	exit 130
fi
printf 'aether %s\n' "$*" >>"$CMD_LOG"
echo "installed /home/tester/.local/share/aether/desktop"
echo "open it from your application menu as Aether"
EOF

# Stands in for the real binary. A completed setup writes the systemd unit,
# which is what the installer checks afterwards. SETUP_FAILS is a setup that
# errors; SETUP_DECLINES is the operator answering no to the summary, which
# the real command reports by exiting 0 having written nothing.
cat >"$fixtures/aether-server" <<'EOF'
#!/bin/sh
printf 'aether-server %s\n' "$*" >>"$CMD_LOG"
[ "${SETUP_FAILS:-0}" = 0 ] || exit 1
if [ "${SETUP_DECLINES:-0}" = 1 ]; then
	echo "nothing written"
	exit 0
fi
: >"$AETHER_UNIT_PATH"
echo "systemctl daemon-reload && systemctl enable --now aether-server"
EOF

: >"$fixtures/checksums.txt"
for asset in aether-linux-amd64 aether-server-linux-amd64 aether-darwin-amd64; do
	src="$fixtures/aether"
	case "$asset" in aether-server-*) src="$fixtures/aether-server" ;; esac
	printf '%s  %s\n' "$(sha_of "$src")" "$asset" >>"$fixtures/checksums.txt"
done

# --- stubs ---------------------------------------------------------------

cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
url="" out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2" && shift 2 ;;
	-w) shift 2 ;;
	http*) url="$1" && shift ;;
	*) shift ;;
	esac
done
[ -n "$out" ] || exit 1
case "$url" in
*/checksums.txt) cp "$FIXTURES/checksums.txt" "$out" ;;
*/aether-server-*) cp "$FIXTURES/aether-server" "$out" ;;
*/aether-*) cp "$FIXTURES/aether" "$out" ;;
*) exit 22 ;;
esac
EOF

# A fixed platform keeps the asset names the same on every developer machine.
# STUB_OS fakes a Mac, which is the only place the role default differs.
cat >"$bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
-m) echo x86_64 ;;
*) echo "${STUB_OS:-Linux}" ;;
esac
EOF

# Never root, so the server path always goes through sudo.
cat >"$bin/id" <<'EOF'
#!/bin/sh
echo 1000
EOF

cat >"$bin/sudo" <<'EOF'
#!/bin/sh
printf 'sudo %s\n' "$*" >>"$CMD_LOG"
exec "$@"
EOF

for tool in npm npx; do
	cat >"$node/$tool" <<EOF
#!/bin/sh
printf '$tool %s\n' "\$*" >>"\$CMD_LOG"
EOF
done

chmod 0755 "$bin"/* "$node"/* "$fixtures/aether" "$fixtures/aether-server"

# A PATH with no npm and no npx anywhere on it, for the "Node is missing"
# case. Dropping the directories that carry npm would also drop sh, awk and
# mktemp, so those directories are mirrored into a shadow of symlinks with
# npm and npx left out.
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

export CMD_LOG="$log" FIXTURES="$fixtures"
# Test seams: a unit path outside /etc, so the post-setup check is real
# without root, and a fake terminal per case (set by answer(), below).
export AETHER_UNIT_PATH="$tmp/aether-server.service"
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
	exit 1
}

reset() {
	case_name="$1"
	: >"$log"
	: >"$out"
	rm -f "$bindir/aether" "$bindir/aether-server" "$AETHER_UNIT_PATH"
	rm -f "$answers"
}

# Writes the lines the fake terminal will hand back, one per prompt. No
# arguments means an empty file, which the installer sees as EOF.
answer() {
	: >"$answers"
	for line in "$@"; do printf '%s\n' "$line" >>"$answers"; done
}

answers="$tmp/answers"

ran() { grep -Fq "$1" "$log" || fail "expected to run: $1"; }
not_ran() { ! grep -Fq "$1" "$log" || fail "should not have run: $1"; }
printed() { grep -Fq "$1" "$out" || fail "missing from output: $1"; }
not_printed() { ! grep -Fq "$1" "$out" || fail "should not be in output: $1"; }

question="What is this machine?"

# Every run must end on the quickstart pointer.
quickstart="https://github.com/3xDevOps/Aether/blob/main/docs/quickstart.md"

# --- --role server: aether-server setup runs -----------------------------

reset "--role server"
sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
not_ran "aether gui build"
grep -q "^sudo .*aether-server setup$" "$log" || fail "setup did not go through sudo"
printed "systemctl daemon-reload && systemctl enable --now aether-server"
printed "This machine is your Aether server"
printed "$quickstart"

# --- a failing setup still lands on the docs, with the rerun command -----

reset "--role server, setup fails"
SETUP_FAILS=1 sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
not_ran "aether gui build"
printed "sudo aether-server setup"
printed "$quickstart"

# --- declining setup exits 0 but writes nothing: no activation line ------

reset "--role server, setup declined"
SETUP_DECLINES=1 sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether-server setup"
printed "sudo aether-server setup"
! grep -Fq "systemctl daemon-reload" "$out" || fail "offered to activate a service that was never written"
printed "$quickstart"

# --- declining on a box that already has a unit: activating it is right --

reset "--role server, declined but a unit is already installed"
: >"$AETHER_UNIT_PATH"
SETUP_DECLINES=1 sh "$script" --role server >"$out" 2>&1 || fail "installer exited non-zero"
printed "systemctl daemon-reload && systemctl enable --now aether-server"

# --- --role client: aether gui build runs --------------------------------

reset "--role client"
sh "$script" --role client >"$out" 2>&1 || fail "installer exited non-zero"
ran "aether gui build"
not_ran "aether-server setup"
printed "This machine is a client"
printed "aether link <server-host>:2222"
printed "$quickstart"

# --- --role none: binaries only ------------------------------------------

reset "--role none"
sh "$script" --role none >"$out" 2>&1 || fail "installer exited non-zero"
not_ran "aether gui build"
not_ran "aether-server setup"
printed "Nothing else was set up on this machine"
printed "$quickstart"

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
sh "$script" --server >"$out" 2>&1 || fail "installer exited non-zero"
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
sh "$script" --role SERVER >"$out" 2>&1 || fail "installer exited non-zero"
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

guard=""
command -v timeout >/dev/null 2>&1 && guard="timeout 120"

reset "prompt: a terminal that closes takes the default"
answer
# shellcheck disable=SC2086
$guard env AETHER_TTY="$answers" sh "$script" >"$out" 2>&1 || fail "installer blocked or exited non-zero"
ran "aether-server setup"

# --- Ctrl-C during the follow-up stops the installer ---------------------

reset "interrupted build"
status=0
GUI_INTERRUPTS=1 sh "$script" --role client >"$out" 2>&1 || status=$?
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

# --- client with no Node: say what is missing, still exit 0 --------------

reset "client without npm"
PATH="$no_node_path" sh "$script" --role client >"$out" 2>&1 || fail "installer exited non-zero"
not_ran "aether gui build"
printed "Node.js 22+"
printed "aether gui build"
printed "$quickstart"

# --- an unknown role is a usage error ------------------------------------

reset "--role bogus"
if sh "$script" --role bogus >"$out" 2>&1; then
	fail "an unknown role was accepted"
fi
printed "unknown role bogus"
[ ! -e "$bindir/aether" ] || fail "a bad role downloaded anything"

echo "install-test: ok"
