#!/bin/sh
# Moves channel $1's pointer to release $2, whose signed manifest is in
# $3: reads the current pointer from the channels prerelease, has
# release-pointer refuse a lower (build, hotfix) and sign the new one into
# $4, uploads it and reads it back the way updaters do. A channel without a
# pointer needs --first; with --first, a pointer that already names this
# exact release (a re-run of the run that wrote it) stays. With --if-above
# it leaves a pointer that is not below the release as it is. The caller keeps $4 as the workflow artifact
# pointer-<channel>, the pair an admin restores if the upload fails.
# Every writer must run in the channels concurrency group, so no two read
# and replace one pointer at once.
set -eu

if_above=
first=
while :; do
	case ${1-} in
	--if-above) if_above=-if-above ;;
	--first) first=1 ;;
	*) break ;;
	esac
	shift
done
if [ "$#" -ne 4 ]; then
	echo "usage: publish-pointer.sh [--first] [--if-above] <channel> <tag> <dist-dir> <pair-dir>" >&2
	exit 2
fi

channel="$1"
tag="$2"
dist="$3"
pair="$4"
repo="${GITHUB_REPOSITORY:?publish-pointer: GITHUB_REPOSITORY must name the repository}"
tool="${RELEASE_POINTER:?publish-pointer: RELEASE_POINTER must name the built release-pointer command}"
name="$channel.json"
public="https://github.com/$repo/releases/download/channels"
# The command that restores run $1's pointer-<channel> artifact.
restore() {
	echo "gh run download $1 --repo $repo -n pointer-$channel -D pair && gh release upload channels --repo $repo --clobber pair/$name pair/$name.sig"
}
expired="The artifact lasts 90 days; once it has expired, a repository admin removes what is left of $name and re-signs it with first-pointer for the release it named or one above it, never below"

fail() {
	echo "publish-pointer: $1" >&2
	exit 1
}

case $channel in nightly | beta | stable) ;; *) fail "$channel is not nightly, beta or stable" ;; esac
if [ ! -d "$pair" ] || [ -n "$(ls -A "$pair")" ]; then
	fail "$pair must be an empty directory"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/current" "$tmp/public"

release=$(gh api "repos/$repo/releases/tags/channels" 2>"$tmp/err") ||
	fail "cannot read the channels release: $(cat "$tmp/err"). A repository admin creates it once: gh release create channels --prerelease --title channels --notes 'Signed channel pointers.' --target main"
[ "$(printf '%s' "$release" | jq -r '"\(.draft) \(.prerelease)"')" = "false true" ] ||
	fail "the channels release must be a published prerelease, so Latest and the installers never pick it"
if printf '%s' "$release" | jq -e '.assets | any(.name == "aether-release.json")' >/dev/null; then
	fail "the channels release carries aether-release.json, so reconcile_latest could pick it; delete that asset"
fi

assets=$(printf '%s' "$release" | jq -r '[.assets[].name | select(. == $n or . == $n + ".sig")] | sort | join(" ")' --arg n "$name")
case "$assets:$first" in
":1") set -- -first ;;
":")
	fail "channels has no $name. A missing pointer is not a first one: a failed upload or a deletion may have removed a higher one, and installs that saw it refuse anything lower. Restore the last good pair from the pointer-$channel artifact of the run that last moved it ($(restore '<run-id>')). $expired. Only when the channel has never had a pointer, run with the first-pointer input"
	;;
"$name $name.sig:"*)
	gh release download channels --pattern "$name" --pattern "$name.sig" --dir "$tmp/current"
	set -- -current "$tmp/current/$name"
	if [ -n "$first" ]; then
		set -- -first "$@"
		if_above=
	fi
	;;
*) fail "channels holds $assets for $name; it needs both $name and $name.sig. Restore the last good pair from the pointer-$channel artifact of the run that last moved it: $(restore '<run-id>'). $expired" ;;
esac

"$tool" -channel "$channel" -tag "$tag" -manifest "$dist/aether-release.json" ${if_above:+"$if_above"} "$@" -out "$pair"
if [ ! -e "$pair/$name" ]; then
	if [ -n "$if_above" ]; then echo "$name is not below $tag and stays"; else echo "$name already names $tag and stays"; fi
	exit 0
fi

# --clobber deletes each old asset before uploading its replacement, so a
# failure can leave half a pair; uploading this run's whole pair again
# repairs it.
attempt=1
until gh release upload channels --clobber "$pair/$name" "$pair/$name.sig"; do
	[ "$attempt" -lt 3 ] ||
		fail "uploading $name failed 3 times and channels may hold half a pair. This run's signed pair is its pointer-$channel artifact; restore it: $(restore "${GITHUB_RUN_ID:-<run-id>}")"
	attempt=$((attempt + 1))
	sleep 10
done

# A replaced asset can be served stale for a moment, so a mismatch is
# fetched again before it fails the run.
for file in "$name" "$name.sig"; do
	attempt=1
	while :; do
		curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
			--retry 10 --retry-delay 6 --retry-all-errors --output "$tmp/public/$file" "$public/$file" </dev/null
		cmp -s "$pair/$file" "$tmp/public/$file" && break
		[ "$attempt" -lt 10 ] || fail "$public/$file differs from the pointer this run signed"
		attempt=$((attempt + 1))
		sleep 6
	done
done
echo "$name names $tag"
