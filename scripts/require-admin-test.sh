#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/bin"
cat >"$tmp/bin/gh" <<'STUB'
#!/bin/sh
case $2 in
repos/3xDevOps/Aether/collaborators/owner/permission) echo admin ;;
repos/3xDevOps/Aether/collaborators/writer/permission) echo write ;;
*) echo "HTTP 502" >&2; exit 1 ;;
esac
STUB
chmod +x "$tmp/bin/gh"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}
check() { PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether sh "$script_dir/require-admin.sh" "$@" >"$tmp/out" 2>&1; }

check owner owner || fail "an admin dispatch and re-run was refused: $(cat "$tmp/out")"
if check owner writer; then fail "a re-run by a non-admin was accepted"; fi
grep -q '::error::writer is not an admin' "$tmp/out" || fail "no error named the non-admin: $(cat "$tmp/out")"
if check writer owner; then fail "a non-admin dispatch was accepted"; fi
if check owner flaky; then fail "a failed lookup was accepted"; fi
if check; then fail "no user was accepted"; fi
echo "require-admin tests passed"
