#!/bin/sh
# Moves channel $1's pointer to release $2, whose signed manifest is in
# $3: reads the current pointer from the channels prerelease, has
# release-pointer refuse a lower (build, hotfix) and sign the new one, uploads
# it and reads it back the way updaters do. With --if-above it leaves a
# pointer that is not below the release as it is. Every writer must run in
# the channels concurrency group, so no two read and replace one pointer at
# once.
set -eu

if_above=
if [ "${1-}" = --if-above ]; then
	if_above=-if-above
	shift
fi
if [ "$#" -ne 3 ]; then
	echo "usage: publish-pointer.sh [--if-above] <channel> <tag> <dist-dir>" >&2
	exit 2
fi

channel="$1"
tag="$2"
dist="$3"
repo="${GITHUB_REPOSITORY:?publish-pointer: GITHUB_REPOSITORY must name the repository}"
tool="${RELEASE_POINTER:?publish-pointer: RELEASE_POINTER must name the built release-pointer command}"
name="$channel.json"
public="https://github.com/$repo/releases/download/channels"

fail() {
	echo "publish-pointer: $1" >&2
	exit 1
}

case $channel in nightly | beta | stable) ;; *) fail "$channel is not nightly, beta or stable" ;; esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/current" "$tmp/new" "$tmp/public"

release=$(gh api "repos/$repo/releases/tags/channels" 2>"$tmp/err") ||
	fail "cannot read the channels release: $(cat "$tmp/err"). A repository admin creates it once: gh release create channels --prerelease --title channels --notes 'Signed channel pointers.' --target main"
[ "$(printf '%s' "$release" | jq -r '"\(.draft) \(.prerelease)"')" = "false true" ] ||
	fail "the channels release must be a published prerelease, so Latest and the installers never pick it"
if printf '%s' "$release" | jq -e '.assets | any(.name == "aether-release.json")' >/dev/null; then
	fail "the channels release carries aether-release.json, so reconcile_latest could pick it; delete that asset"
fi

set --
assets=$(printf '%s' "$release" | jq -r '[.assets[].name | select(. == $n or . == $n + ".sig")] | sort | join(" ")' --arg n "$name")
case $assets in
"") echo "publish-pointer: channels has no $name yet; this is the first" >&2 ;;
"$name $name.sig")
	gh release download channels --pattern "$name" --pattern "$name.sig" --dir "$tmp/current"
	set -- -current "$tmp/current/$name"
	;;
*) fail "channels holds $assets for $name; it needs both $name and $name.sig" ;;
esac

"$tool" -channel "$channel" -tag "$tag" -manifest "$dist/aether-release.json" ${if_above:+"$if_above"} "$@" -out "$tmp/new"
if [ -n "$if_above" ] && [ ! -e "$tmp/new/$name" ]; then
	echo "$name is not below $tag and stays"
	exit 0
fi
gh release upload channels --clobber "$tmp/new/$name" "$tmp/new/$name.sig"

# A replaced asset can be served stale for a moment, so a mismatch is
# fetched again before it fails the run.
for file in "$name" "$name.sig"; do
	attempt=1
	while :; do
		curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
			--retry 10 --retry-delay 6 --retry-all-errors --output "$tmp/public/$file" "$public/$file" </dev/null
		cmp -s "$tmp/new/$file" "$tmp/public/$file" && break
		[ "$attempt" -lt 10 ] || fail "$public/$file differs from the pointer this run signed"
		attempt=$((attempt + 1))
		sleep 6
	done
done
echo "$name names $tag"
