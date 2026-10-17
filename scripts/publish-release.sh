#!/bin/sh
# The release workflow's concurrency group runs one release at a time.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: publish-release.sh <tag> <dist-dir>" >&2
	exit 2
fi

tag="$1"
dist="$2"
repo="${GITHUB_REPOSITORY:?publish-release: GITHUB_REPOSITORY must name the repository}"
script_dir=$(dirname -- "$0")
manifest="$dist/aether-release.json"
signature="$manifest.sig"
public="https://github.com/$repo/releases/download/$tag"

fail() {
	echo "publish-release: $1" >&2
	exit 1
}

if [ ! -d "$dist" ]; then
	fail "distribution directory not found: $dist"
fi
for file in "$manifest" "$signature" "$dist/checksums.txt"; do
	if [ ! -s "$file" ]; then
		fail "$file is missing; write checksums.txt and sign the release with release-manifest first"
	fi
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
(cd "$dist" && ls) >"$tmp/expected"

# Requires every asset the manifest lists to be in dir with its size and
# SHA-256, and checksums.txt to match them.
verify() {
	jq -r '.assets | to_entries[] | "\(.key) \(.value.sha256) \(.value.size)"' "$1" >"$tmp/entries"
	while read -r name sum size; do
		[ -f "$2/$name" ] || fail "$3 has no $name, which the manifest lists"
		got=$(sha256sum "$2/$name" | cut -d ' ' -f 1)
		got_size=$(wc -c <"$2/$name" | tr -d ' ')
		if [ "$got" != "$sum" ] || [ "$got_size" != "$size" ]; then
			fail "$3/$name is $got_size bytes with sha256 $got; the manifest says $size bytes with sha256 $sum"
		fi
	done <"$tmp/entries"
	(cd "$2" && sha256sum --check --strict --quiet checksums.txt) ||
		fail "$3/checksums.txt does not match the assets beside it"
}

# Downloads assets the way clients do: anonymously. Retries cover a release
# that has just been published and is not yet served everywhere.
fetch() {
	for name in "$@"; do
		curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
			--retry 10 --retry-delay 6 --retry-all-errors --output "$tmp/public/$name" "$public/$name"
	done
}

verify_public() {
	rm -rf "$tmp/public"
	mkdir "$tmp/public"
	# Word splitting on purpose: asset names contain no spaces.
	# shellcheck disable=SC2046
	fetch $(jq -r '.assets | keys[]' "$1") checksums.txt
	verify "$1" "$tmp/public" "$public"
	echo "verified $public"
}

# One listing, not `gh release view`: gh looks drafts and published releases
# up separately and can report a failed lookup as a missing release.
gh api --paginate "repos/$repo/releases?per_page=100" \
	--jq '.[] | "\(.tag_name) \(.draft) \(.prerelease) \([.assets[].name] | join(","))"' >"$tmp/releases"
awk -v tag="$tag" '$1 == tag' "$tmp/releases" >"$tmp/release"
case $(wc -l <"$tmp/release" | tr -d ' ') in
0) state=none ;;
1) read -r _ state _ names <"$tmp/release" ;;
*) fail "$tag has more than one release; delete the extra drafts" ;;
esac
printf '%s\n' "${names:-}" | tr ',' '\n' | sed '/^$/d' >"$tmp/names"

# Latest, on GitHub and for the image latest tags, goes to the highest
# version, so an older tag that finishes after a newer one does not take it.
# Release notes start at the release below.
ours=$(sh "$script_dir/android-version-code.sh" "$tag")
latest=true
previous=
previous_code=0
while read -r other draft prerelease _; do
	[ "$draft $prerelease" = "false false" ] || continue
	code=$(sh "$script_dir/android-version-code.sh" "$other" 2>/dev/null </dev/null) || continue
	if [ "$code" -gt "$ours" ]; then
		latest=false
	elif [ "$code" -lt "$ours" ] && [ "$code" -gt "$previous_code" ]; then
		previous=$other
		previous_code=$code
	fi
done <"$tmp/releases"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	echo "latest=$latest" >>"$GITHUB_OUTPUT"
fi

if [ "$state" = false ]; then
	if grep -qx 'aether-release\.json' "$tmp/names"; then
		# Assets signed by a published manifest are never replaced.
		rm -rf "$tmp/public"
		mkdir "$tmp/public"
		fetch aether-release.json aether-release.json.sig
		jq -r '.assets | keys[]' "$manifest" >"$tmp/ours"
		jq -r '.assets | keys[]' "$tmp/public/aether-release.json" | diff -u "$tmp/ours" - >&2 ||
			fail "$tag is published with a manifest that lists other assets than $manifest; publish a new tag"
		cp "$tmp/public/aether-release.json" "$tmp/published.json"
		verify_public "$tmp/published.json"
		echo "$tag is already published and verified"
		exit 0
	fi
	echo "publish-release: $tag is published without aether-release.json, so it is incomplete; returning it to a draft until it is verified" >&2
	gh release edit "$tag" --draft=true
	state=true
fi

if [ "$state" = none ]; then
	gh release create "$tag" --draft --verify-tag --title "$tag" --generate-notes \
		${previous:+"--notes-start-tag=$previous"}
fi
gh release upload "$tag" --clobber "$dist"/*
while read -r name; do
	grep -qxF "$name" "$tmp/expected" || gh release delete-asset "$tag" "$name" --yes </dev/null
done <"$tmp/names"

# Drafts cannot be downloaded anonymously, so the draft is read back through
# the API before publishing.
gh release download "$tag" --dir "$tmp/draft"
(cd "$tmp/draft" && ls) | diff -u "$tmp/expected" - >&2 ||
	fail "draft $tag does not hold exactly the assets in $dist"
verify "$manifest" "$tmp/draft" "draft $tag"
for name in checksums.txt aether-release.json aether-release.json.sig; do
	cmp "$dist/$name" "$tmp/draft/$name" >&2 || fail "draft $tag/$name differs from $dist/$name"
done

gh release edit "$tag" --draft=false --latest="$latest"
verify_public "$manifest"
fetch aether-release.json aether-release.json.sig
for name in aether-release.json aether-release.json.sig; do
	cmp "$dist/$name" "$tmp/public/$name" >&2 || fail "$public/$name differs from $dist/$name"
done
echo "published $tag (latest: $latest)"
