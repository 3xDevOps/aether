#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

nightly=1111111111111111111111111111111111111111
tagobj=9999999999999999999999999999999999999999
beta=2222222222222222222222222222222222222222
bin="$tmp/bin"
mkdir -p "$bin"

# The repository: v0.9.0-nightly.12 is an annotated tag of $nightly,
# v0.9.0-beta.1 a lightweight tag of $beta. $STATE/published lists the
# published releases, $STATE/channels the channels release's assets, or is
# missing for no channels release.
cat >"$bin/git" <<EOF
#!/bin/sh
[ "\$*" = "ls-remote --tags https://github.com/3xDevOps/Aether.git refs/tags/v*" ] || exit 2
printf '%s\trefs/tags/v0.9.0-nightly.12\n%s\trefs/tags/v0.9.0-nightly.12^{}\n%s\trefs/tags/v0.9.0-beta.1\n' $tagobj $nightly $beta
EOF
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >>"$STATE/log"
case "$1 $2" in
"api --paginate") cat "$STATE/published" ;;
"api repos/3xDevOps/Aether/releases/tags/channels")
	[ -f "$STATE/channels" ] || { echo "HTTP 404: Not Found" >&2; exit 1; }
	jq -R . "$STATE/channels" | jq -s '{assets: map({name: .})}'
	;;
"api repos/3xDevOps/Aether/releases/tags/"*) echo 2026-11-10T10:00:00Z ;;
"release download")
	dir=
	while [ "$#" -gt 0 ]; do
		case $1 in --dir) dir=$2 ;; --pattern) echo "$2" >>"$STATE/downloaded" ;; esac
		shift
	done
	for f in $(cat "$STATE/downloaded"); do echo data >"$dir/$f"; done
	: >"$STATE/downloaded"
	;;
*) exit 2 ;;
esac
EOF
# The fake promote-plan records what it was given and answers as the real
# one would for a resolvable promotion.
cat >"$bin/promote-plan" <<'EOF'
#!/bin/sh
set -eu
echo "promote-plan $*" >>"$STATE/log"
case $1 in
source) echo v0.9.0-nightly.12 ;;
resolve)
	while [ "$#" -gt 0 ]; do
		case $1 in
		-tags) sort "$2" >"$STATE/tags" ;;
		-pointers) (cd "$2" && ls) >"$STATE/pointers" ;;
		-manifest) ls "$2" "$2.sig" >/dev/null ;;
		esac
		shift
	done
	echo tag=v0.9.0-beta.2
	;;
esac
EOF
chmod +x "$bin/git" "$bin/gh" "$bin/promote-plan"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

reset() {
	export STATE="$tmp/state"
	rm -rf "$STATE"
	mkdir -p "$STATE"
	: >"$STATE/log"
	: >"$STATE/downloaded"
	printf 'v0.9.0-nightly.12\nv0.9.0-beta.1\n' >"$STATE/published"
	printf 'beta.json\nbeta.json.sig\nREADME\n' >"$STATE/channels"
}

run() {
	PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether PROMOTE_PLAN=promote-plan \
		sh "$script_dir/promote-plan.sh" >"$tmp/out" 2>&1
}

# A tag: the peeled commit of an annotated tag, the pointers that exist,
# and the outputs.
reset
FROM=v0.9.0-nightly.12 TO=beta run || fail "a nightly tag: $(cat "$tmp/out")"
[ "$(cat "$STATE/tags")" = "$(printf 'v0.9.0-beta.1 %s\nv0.9.0-nightly.12 %s' $beta $nightly)" ] ||
	fail "tags were not peeled to commits: $(cat "$STATE/tags")"
[ "$(cat "$STATE/pointers")" = "$(printf 'beta.json\nbeta.json.sig')" ] || fail "pointers: $(cat "$STATE/pointers")"
grep -q -- '-from v0.9.0-nightly.12 -to beta -tags' "$STATE/log" || fail "resolve got: $(cat "$STATE/log")"
! grep -q 'first-pointer\|-version' "$STATE/log" || fail "passed flags nobody asked for"
[ "$(cat "$tmp/out")" = "$(printf 'tag=v0.9.0-beta.2\nsource=v0.9.0-nightly.12\nsource-published-at=2026-11-10T10:00:00Z')" ] ||
	fail "outputs: $(cat "$tmp/out")"

# A commit SHA is resolved to its release first; version and first-pointer
# reach resolve.
reset
FROM=$nightly TO=beta VERSION=1.0.0 FIRST_POINTER=true run || fail "a commit: $(cat "$tmp/out")"
grep -q "promote-plan source -from $nightly -to beta -tags .* -published " "$STATE/log" || fail "source was not resolved: $(cat "$STATE/log")"
grep -q -- '-from v0.9.0-nightly.12 -to beta -version 1.0.0 -first-pointer -tags' "$STATE/log" || fail "resolve got: $(cat "$STATE/log")"

# An unpublished source is refused before anything is downloaded.
reset
echo v0.9.0-beta.1 >"$STATE/published"
if FROM=v0.9.0-nightly.12 TO=beta run; then fail "an unpublished source was accepted"; fi
grep -q 'not a published release' "$tmp/out" || fail "unpublished: $(cat "$tmp/out")"
! grep -q 'release download\|resolve' "$STATE/log" || fail "an unpublished source was read"

# Without a channels release there is nothing to check a pointer against.
reset
rm "$STATE/channels"
if FROM=v0.9.0-nightly.12 TO=beta run; then fail "a missing channels release was accepted"; fi
grep -q 'gh release create channels --prerelease' "$tmp/out" || fail "no admin command: $(cat "$tmp/out")"
! grep -q 'promote-plan resolve' "$STATE/log" || fail "resolved without pointers"

# An input that could add a line to GITHUB_OUTPUT is refused first.
reset
if FROM=$(printf 'v0.9.0-nightly.12\ntag=v9.9.9') TO=beta run; then fail "a two-line from was accepted"; fi
[ ! -s "$STATE/log" ] || fail "a two-line from reached the API"

# promote.yml's CI step fails when the check fails: the check is a command
# of its own, never an argument of echo.
step=$(awk '/name: Require green push CI for the exact commit/{f=1} f&&/run: \|/{r=1;next} f&&!r&&/run: /{sub(/^ *run: /,"");print;exit} r&&/^      - /{exit} r{print}' "$script_dir/../.github/workflows/promote.yml")
[ -n "$step" ] || fail "promote.yml's CI step was not found"
mkdir -p "$tmp/ci/scripts"
printf '#!/bin/sh\necho "no green push CI for $2" >&2\nexit 1\n' >"$tmp/ci/scripts/release-ci-check.sh"
if (cd "$tmp/ci" && GITHUB_REPOSITORY=o/r COMMIT=abc GITHUB_OUTPUT="$tmp/ci/out" bash -e -o pipefail -c "$step") 2>/dev/null; then
	fail "promote.yml goes on after a failed CI check"
fi
[ ! -s "$tmp/ci/out" ] || fail "a failed CI check wrote an output: $(cat "$tmp/ci/out")"
printf '#!/bin/sh\necho https://example.invalid/run/1\n' >"$tmp/ci/scripts/release-ci-check.sh"
(cd "$tmp/ci" && GITHUB_REPOSITORY=o/r COMMIT=abc GITHUB_OUTPUT="$tmp/ci/out" bash -e -o pipefail -c "$step") || fail "promote.yml's CI step failed on a green check"
grep -qx 'url=https://example.invalid/run/1' "$tmp/ci/out" || fail "CI url: $(cat "$tmp/ci/out")"

echo "promote-plan tests passed"
