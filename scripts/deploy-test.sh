#!/bin/sh
# Exercises scripts/deploy.sh remote mode with stubbed ssh/scp/go/git, the
# same style as publish-release-test.sh. Local mode touches the real system
# (sudo, systemctl) and is verified by actually deploying, not here.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

# A fake repo root keeps the dashboard check and unit copy hermetic; the
# script resolves everything relative to its own parent directory.
mkdir -p "$tmp/repo/scripts" "$tmp/repo/web/dist" "$tmp/repo/packaging/systemd"
cp "$script_dir/deploy.sh" "$tmp/repo/scripts/deploy.sh"
printf '%s\n' unit >"$tmp/repo/packaging/systemd/aether-server.service"

bin="$tmp/bin"
log="$tmp/cmd.log"
mkdir -p "$bin"

# go build -trimpath -ldflags <flags> -o <out> <pkg>
cat >"$bin/go" <<'EOF'
#!/bin/sh
set -eu
printf 'go GOOS=%s GOARCH=%s %s %s %s\n' "${GOOS:-}" "${GOARCH:-}" "$1" "$6" "$7" >>"$CMD_LOG"
touch "$6"
EOF

cat >"$bin/git" <<'EOF'
#!/bin/sh
case "$1" in
describe) echo v0.0.0-test ;;
rev-parse) echo abc1234 ;;
*) exit 2 ;;
esac
EOF

# Consumes the multiplexing options (-o ..., -tt, -O exit) before the host,
# then logs and answers the remote command. TTY: records whether -tt was set.
cat >"$bin/ssh" <<'EOF'
#!/bin/sh
set -eu
tty=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) shift 2 ;;
	-tt) tty="-tt" && shift ;;
	-O) exit 0 ;; # control-master teardown
	*) break ;;
	esac
done
host="$1"
shift
printf 'ssh%s %s %s\n' "${tty:+ $tty}" "$host" "$*" >>"$CMD_LOG"
case "$*" in
true) exit 0 ;;
"id -u") echo 1000 ;;
"uname -m") echo x86_64 ;;
"mktemp -d") echo /tmp/deploy-test ;;
*"systemctl cat aether-server.service"*) [ "${UNIT_EXISTS:-0}" = 1 ] ;;
*"aether-server' version") echo "aether-server v0.0.0-test (abc1234)" ;;
*) exit 0 ;;
esac
EOF

cat >"$bin/scp" <<'EOF'
#!/bin/sh
set -eu
while [ $# -gt 0 ]; do
	case "$1" in
	-q) shift ;;
	-o) shift 2 ;;
	*) break ;;
	esac
done
printf 'scp %s\n' "$*" >>"$CMD_LOG"
EOF

cat >"$bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF

chmod 0755 "$bin/go" "$bin/git" "$bin/ssh" "$bin/scp" "$bin/sleep"
export CMD_LOG="$log" PATH="$bin:$PATH"

fail() {
	echo "deploy-test: $*" >&2
	echo "--- command log ---" >&2
	cat "$log" >&2 2>/dev/null || true
	exit 1
}

assert() {
	grep -Fq "$1" "$log" || fail "missing command: $1"
}

refute() {
	! grep -Fq "$1" "$log" || fail "unexpected command: $1"
}

# --- an unbuilt dashboard must abort before anything runs ----------------

: >"$log"
if DEPLOY_HOST=user@testhost sh "$tmp/repo/scripts/deploy.sh" >/dev/null 2>&1; then
	fail "deploy succeeded without web/dist/index.html"
fi
[ ! -s "$log" ] || fail "commands ran despite the missing dashboard"
printf '%s\n' '<html/>' >"$tmp/repo/web/dist/index.html"

# --- first deploy: unit missing, so it is installed and enabled ----------

: >"$log"
DEPLOY_HOST=user@testhost sh "$tmp/repo/scripts/deploy.sh" >/dev/null

assert "ssh user@testhost uname -m"
assert "go GOOS=linux GOARCH=amd64 build dist/aether-server-deploy ./cmd/aether-server"
assert "scp dist/aether-server-deploy user@testhost:/tmp/deploy-test/aether-server"
assert "scp packaging/systemd/aether-server.service user@testhost:/tmp/deploy-test/aether-server.service"
# All privileged steps are one sudo invocation on a forced TTY.
assert "ssh -tt user@testhost sudo sh -c"
assert "install -m0755 -o root -g root '/tmp/deploy-test/aether-server' '/usr/local/bin/aether-server'"
assert "install -m0644 -o root -g root '/tmp/deploy-test/aether-server.service' '/etc/systemd/system/aether-server.service'"
assert "systemctl daemon-reload && systemctl enable aether-server"
assert "systemctl restart aether-server"
assert "ssh user@testhost rm -rf '/tmp/deploy-test'"
assert "ssh user@testhost systemctl is-active --quiet aether-server"
assert "ssh user@testhost '/usr/local/bin/aether-server' version"
count="$(grep -c '^ssh -tt' "$log")" || true
[ "$count" = 1 ] || fail "expected exactly one sudo invocation, got $count"

# --- later deploys: unit present, so no unit install or enable ------------

: >"$log"
UNIT_EXISTS=1 DEPLOY_HOST=user@testhost sh "$tmp/repo/scripts/deploy.sh" >/dev/null

refute "scp packaging/systemd/aether-server.service"
refute "systemctl enable"
assert "systemctl restart aether-server"

# --- a version mismatch on the target must fail the deploy ----------------

cat >"$bin/git" <<'EOF'
#!/bin/sh
case "$1" in
describe) echo v9.9.9-other ;;
rev-parse) echo abc1234 ;;
esac
EOF

: >"$log"
if DEPLOY_HOST=user@testhost sh "$tmp/repo/scripts/deploy.sh" >/dev/null 2>&1; then
	fail "deploy succeeded despite target reporting a different version"
fi

echo "deploy-test: ok"
