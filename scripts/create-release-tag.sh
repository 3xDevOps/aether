#!/bin/sh
# Creates the annotated release tag $1 at commit $2 through the API, with
# a "build: $3" line in its message, and succeeds without a change when a
# failed attempt already created exactly that tag. Any other tag of that
# name is refused. The release signer runs it once every asset is built.
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: create-release-tag.sh <tag> <commit> <build>" >&2
	exit 2
fi
tag="$1"
commit="$2"
build="$3"
repo="${GITHUB_REPOSITORY:?create-release-tag: GITHUB_REPOSITORY must name the repository}"

fail() {
	echo "create-release-tag: $1" >&2
	exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

# Prints nothing when the tag does not exist; fails when it exists as
# anything but this annotated tag.
existing() {
	if ! ref=$(gh api "repos/$repo/git/ref/tags/$tag" 2>"$tmp/err"); then
		grep -q 'HTTP 404' "$tmp/err" || fail "cannot read tag $tag: $(cat "$tmp/err")"
		return 0
	fi
	[ "$(printf '%s' "$ref" | jq -r .object.type)" = tag ] ||
		fail "tag $tag exists and is not an annotated tag; an admin deletes it (gh api -X DELETE repos/$repo/git/refs/tags/$tag) before this release can be tagged"
	object=$(gh api "repos/$repo/git/tags/$(printf '%s' "$ref" | jq -r .object.sha)")
	target=$(printf '%s' "$object" | jq -r '"\(.object.type) \(.object.sha)"')
	[ "$target" = "commit $commit" ] ||
		fail "tag $tag names $target, not commit $commit; an admin deletes it (gh api -X DELETE repos/$repo/git/refs/tags/$tag) before this release can be tagged"
	printf '%s' "$object" | jq -r .message | grep -qxF "build: $build" ||
		fail "tag $tag names $commit without the line \"build: $build\"; an admin deletes it (gh api -X DELETE repos/$repo/git/refs/tags/$tag) before this release can be tagged"
	echo exists
}

state=$(existing)
if [ -n "$state" ]; then
	echo "tag $tag already names $commit at build $build"
	exit 0
fi
message=$(printf '%s\n\nbuild: %s\n' "$tag" "$build")
object=$(gh api "repos/$repo/git/tags" -f tag="$tag" -f message="$message" -f object="$commit" -f type=commit --jq .sha)
# Another writer may have created the ref meanwhile; then it must be this tag.
if ! gh api "repos/$repo/git/refs" -f ref="refs/tags/$tag" -f sha="$object" >/dev/null 2>"$tmp/err"; then
	cat "$tmp/err" >"$tmp/create-err"
	state=$(existing)
	[ -n "$state" ] || fail "cannot create tag $tag: $(cat "$tmp/create-err")"
fi
echo "tag $tag names $commit at build $build"
