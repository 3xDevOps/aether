#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: publish-release.sh <tag> <dist-dir>" >&2
	exit 2
fi

tag="$1"
dist="$2"
repo="${GITHUB_REPOSITORY:?publish-release: GITHUB_REPOSITORY must name the repository}"
manifest="$dist/aether-release.json"
signature="$manifest.sig"

if [ ! -d "$dist" ]; then
	echo "publish-release: distribution directory not found: $dist" >&2
	exit 1
fi
for file in "$manifest" "$signature"; do
	if [ ! -s "$file" ]; then
		echo "publish-release: $file is missing; sign the release with release-manifest first" >&2
		exit 1
	fi
done

set --
for file in "$dist"/*; do
	case "${file##*/}" in
	aether-release.json | aether-release.json.sig) ;;
	*) set -- "$@" "$file" ;;
	esac
done
if [ "$#" -eq 0 ]; then
	echo "publish-release: no release assets in $dist" >&2
	exit 1
fi

# A published manifest has signed the assets beside it; replacing them
# under it would break every updater, so a signed release is never redone.
if names=$(gh release view "$tag" --json assets --jq '.assets[].name' 2>/dev/null); then
	if printf '%s\n' "$names" | grep -qx 'aether-release\.json\(\.sig\)\{0,1\}'; then
		echo "publish-release: $tag already has a signed manifest; publish a new tag instead of re-uploading" >&2
		exit 1
	fi
	gh release upload "$tag" --clobber "$@"
else
	gh release create "$tag" --title "$tag" --generate-notes "$@"
fi

# Clients download anonymously, so the release is verified the same way
# before the manifest that makes it installable goes up.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
jq -r '.assets | to_entries[] | "\(.key) \(.value.sha256) \(.value.size)"' "$manifest" >"$tmp/assets"
while read -r name sum size; do
	url="https://github.com/$repo/releases/download/$tag/$name"
	if [ ! -f "$dist/$name" ]; then
		echo "publish-release: the manifest lists $name, which is not in $dist" >&2
		exit 1
	fi
	curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
		--retry 5 --retry-delay 5 --retry-all-errors --output "$tmp/download" "$url"
	got=$(sha256sum "$tmp/download" | cut -d ' ' -f 1)
	got_size=$(wc -c <"$tmp/download" | tr -d ' ')
	if [ "$got" != "$sum" ] || [ "$got_size" != "$size" ]; then
		echo "publish-release: $url is $got_size bytes with sha256 $got; the manifest says $size bytes with sha256 $sum" >&2
		exit 1
	fi
	echo "verified $url"
done <"$tmp/assets"

gh release upload "$tag" "$manifest" "$signature"
