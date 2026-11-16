#!/bin/sh
# Runs release-hotfix.sh against real git repositories, with the
# release-hotfix command stubbed; the command is tested in Go.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
bin="$tmp/bin"
export STATE="$tmp/state"
mkdir -p "$bin" "$STATE"

# Prints what the signed manifests would: the base $BASE_TAG at $BASE, and
# the previous release at $PREVIOUS.
cat >"$bin/release-hotfix" <<'EOF'
#!/bin/sh
set -eu
echo "$*" >"$STATE/tool"
[ ! -e "$STATE/refuse" ] || { echo "release-hotfix: v0.9.4 has no signed aether-release.json" >&2; exit 1; }
printf 'build=412\nhotfix=%s\nbase-tag=%s\nbase-commit=%s\nprevious-commit=%s\n' \
	"${2##*.}" "$BASE_TAG" "$BASE" "$PREVIOUS"
EOF
chmod +x "$bin/release-hotfix"
export PATH="$bin:$PATH" RELEASE_HOTFIX="$bin/release-hotfix"
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

git init --quiet --bare "$tmp/origin.git"
git init --quiet -b main "$tmp/work"
cd "$tmp/work"
git remote add origin "$tmp/origin.git"
commit() {
	name=$(printf '%s' "$1" | tr -c 'a-z0-9' _)
	echo "$1" >"$name"
	git add "$name"
	git commit --quiet -m "$1"
	git rev-parse HEAD
}
commit start >/dev/null
base=$(commit "the line's stable base")
main1=$(commit 'main after v0.9.0')
git checkout --quiet -b release/0.9 "$base"
fix1=$(commit 'fix one')
fix2=$(commit 'fix two')
# release/1.0 was cut from a main commit after the one its manifests name.
git checkout --quiet -b release/1.0 "$main1"
late=$(commit 'fix on a late cut')
# release/1.1 was cut from the right commit, then merged main.
git checkout --quiet -b release/1.1 "$base"
commit 'fix before the merge' >/dev/null
git merge --quiet --no-edit main
merged=$(git rev-parse HEAD)
# The base of the 0.8 line is v0.8.7, its first stable with a build, not v0.8.0.
git checkout --quiet -b release/0.8 "$base"
fix87=$(commit 'fix on v0.8.7')
git checkout --quiet main
commit 'main later' >/dev/null
git push --quiet origin main release/0.8 release/0.9 release/1.0 release/1.1

# head moves release/0.9 on origin to $1.
head() { git push --quiet --force origin "$1:refs/heads/release/0.9"; }
run() {
	rm -f "$STATE/tool"
	sh "$script_dir/release-hotfix.sh" 3xDevOps/Aether "$@" >"$tmp/out" 2>"$tmp/err"
}
accept() {
	label=$1
	shift
	run "$@" || { echo "FAIL: $label was refused:" >&2; cat "$tmp/err" >&2; exit 1; }
}
refuse() {
	label=$1
	want=$2
	shift 2
	if run "$@"; then
		echo "FAIL: $label was accepted" >&2
		exit 1
	fi
	[ ! -s "$tmp/out" ] && grep -qF -- "$want" "$tmp/err" || {
		echo "FAIL: $label: want '$want' in:" >&2
		cat "$tmp/err" >&2
		exit 1
	}
}

export BASE_TAG=v0.9.0 BASE="$base" PREVIOUS="$base"
head "$fix1"
accept 'first hotfix' v0.9.1 "$fix1"
[ "$(cat "$tmp/out")" = "$(printf 'build=412\nhotfix=1\nbase-tag=v0.9.0\nbase-commit=%s\nprevious-commit=%s' "$base" "$base")" ] ||
	{ echo "FAIL: first hotfix printed:" >&2; cat "$tmp/out" >&2; exit 1; }
[ "$(cat "$STATE/tool")" = '-tag v0.9.1 -repo https://github.com/3xDevOps/Aether' ] ||
	{ echo "FAIL: first hotfix ran:" >&2; cat "$STATE/tool" >&2; exit 1; }
export BASE_TAG=v0.8.7
accept 'first hotfix on a line based on v0.8.7' v0.8.8 "$fix87"
export BASE_TAG=v0.9.0
refuse 'commit off the branch' 'is not the head of release/0.9' v0.9.1 "$main1"
refuse 'commit only on main after the tag' 'is not the head of release/0.9' v0.9.1 "$(git rev-parse main)"

head "$fix2"
refuse 'commit below the branch head' "$fix1 is not the head of release/0.9 ($fix2)" v0.9.1 "$fix1"
export PREVIOUS="$fix1"
accept 'next hotfix' v0.9.2 "$fix2"
export PREVIOUS=not-a-commit
refuse 'unparsable signed commit' "names commit 'not-a-commit'" v0.9.2 "$fix2"
touch "$STATE/refuse"
refuse 'release-hotfix refusal' 'v0.9.4 has no signed aether-release.json' v0.9.5 "$fix2"
rm "$STATE/refuse"

head "$fix1"
export PREVIOUS="$fix1"
refuse 'hotfix on the previous commit' "is v0.9.1's commit" v0.9.2 "$fix1"
export PREVIOUS="$fix2"
refuse 'hotfix that does not contain the previous one' 'does not contain v0.9.1' v0.9.2 "$fix1"

head "$base"
export PREVIOUS="$base"
refuse 'the base commit itself' "is v0.9.0's commit" v0.9.1 "$base"
export BASE_TAG=v1.0.0
refuse 'branch cut from a later main commit' "release/1.0 is not cut from the v1.0.0 commit $base: main and $late share $main1" v1.0.1 "$late"
export BASE_TAG=v1.1.0
refuse 'branch that merged main' 'release/1.1 is not cut from the v1.1.0 commit' v1.1.1 "$merged"
refuse 'line without a branch' 'cannot fetch main and release/0.7' v0.7.1 "$fix1"

for tag in v0.9.0 v0.9.1-beta.1 v0.9.01 0.9.1 v0.9.1.1 'v0.9.1;id' v0.9; do
	refuse "tag $tag" 'is not a hotfix tag' "$tag" "$fix1"
done
refuse 'short commit' 'full lowercase commit SHA' v0.9.1 "${fix1%????}"
refuse 'missing arguments' usage v0.9.1

echo 'release hotfix tests passed'
