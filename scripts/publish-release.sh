#!/bin/sh
# The release workflow's concurrency group runs one release per tag at a
# time; releases of different tags run in parallel.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: publish-release.sh <tag> <dist-dir>" >&2
	exit 2
fi

tag="$1"
dist="$2"
repo="${GITHUB_REPOSITORY:?publish-release: GITHUB_REPOSITORY must name the repository}"
commit="${COMMIT:?publish-release: COMMIT must be the commit the release was built from}"
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

# Downloads an asset the way clients do: anonymously. Retries cover a
# release that has just been published and is not yet served everywhere.
download() {
	curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
		--retry 10 --retry-delay 6 --retry-all-errors --output "$2" "$1" </dev/null
}

fetch() {
	for name in "$@"; do
		download "$public/$name" "$tmp/public/$name"
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
list_releases() {
	gh api --paginate "repos/$repo/releases?per_page=100" \
		--jq '.[] | "\(.tag_name) \(.draft) \(.prerelease) \([.assets[].name] | join(","))"' >"$tmp/releases"
}

# The tag may have moved since release-policy resolved it.
require_tag_commit() {
	tagged=$(gh api "repos/$repo/commits/$tag" --jq .sha)
	[ "$tagged" = "$commit" ] ||
		fail "tag $tag now names $tagged; this run built $commit, so $tag is not published"
}

# The place of release $1, whose manifest is $2, in the release order:
# "<channel> <build code> <tag code>", channel "none" for a release from
# before channels. A release on a channel takes the versionCode of its
# (build, hotfix), which ranks it above every release from before channels;
# those rank among themselves by their tag's versionCode, which also breaks
# a tie between two releases with one build, so Latest does not depend on
# the listing's order. A nightly tag has no tag versionCode and ties at 0.
# Fails for a manifest of another tag or with channel fields an updater
# refuses.
release_order() {
	fields=$(jq -r --arg tag "$1" '
		select(type == "object" and .version == $tag)
		| [(if has("channel") then .channel else "" end), (if has("build") then .build else 0 end), (if has("hotfix") then .hotfix else 0 end)]
		| select((.[0] | IN("", "nightly", "beta", "stable")) and (.[1] | type) == "number" and (.[2] | type) == "number")
		| "\(if .[0] == "" then "none" else .[0] end) \(.[1]) \(.[2])"' "$2" 2>/dev/null) || return 1
	case $fields in *"
"*) return 1 ;; esac
	read -r channel build hotfix <<-EOF
		$fields
	EOF
	case "$channel:$1" in
	none:*)
		[ "$build $hotfix" = "0 0" ] || return 1
		code=$(sh "$script_dir/android-version-code.sh" "$1" 2>/dev/null </dev/null) || return 1
		echo "none 0 $code"
		;;
	stable:*-*) return 1 ;;
	stable:* | nightly:*-nightly.* | beta:*-beta.*)
		code=$(sh "$script_dir/android-version-code.sh" "$1" "$build" "$hotfix" 2>/dev/null </dev/null) || return 1
		label=$(sh "$script_dir/android-version-code.sh" "$1" 2>/dev/null </dev/null) || label=0
		echo "$channel $code $label"
		;;
	*) return 1 ;;
	esac
}

# Whether order $1 ranks above order $2, both "<channel> <build code> <tag code>".
above() {
	# Split on purpose: each order is three words.
	# shellcheck disable=SC2086
	set -- $1 $2
	[ "$2" -gt "$5" ] || { [ "$2" -eq "$5" ] && [ "$3" -gt "$6" ]; }
}

# Sets order to the order of published release $1 from its public
# manifest, downloaded once per run: a published manifest is never replaced.
# Runs in the caller's shell so a failed download fails the run instead of
# leaving the release out.
published_order() {
	order=
	case $1 in v[0-9]*) ;; *) return 1 ;; esac
	case $1 in *[!A-Za-z0-9.-]*) return 1 ;; esac
	mkdir -p "$tmp/manifests"
	if [ ! -s "$tmp/manifests/$1" ]; then
		download "https://github.com/$repo/releases/download/$1/aether-release.json" "$tmp/manifests/$1.part" ||
			fail "cannot read the aether-release.json of published release $1 to order it"
		mv "$tmp/manifests/$1.part" "$tmp/manifests/$1"
	fi
	order=$(release_order "$1" "$tmp/manifests/$1") || {
		echo "publish-release: published release $1 has an aether-release.json for another tag or with invalid channel fields; it is not ordered" >&2
		return 1
	}
}

# Latest goes to the highest stable release, or release from before
# channels, among complete published releases that are not prereleases.
# Every run recomputes it after its own publish and lists again after each
# change until the listing agrees, so a run that read a listing before a
# parallel release was published corrects its own write.
reconcile_latest() {
	marked=
	while :; do
		list_releases
		best=
		best_order="none 0 0"
		while read -r other draft prerelease others; do
			[ "$draft $prerelease" = "false false" ] || continue
			case ",$others," in *,aether-release.json,*) ;; *) continue ;; esac
			published_order "$other" || continue
			case $order in "none "* | "stable "*) ;; *) continue ;; esac
			if above "$order" "$best_order"; then
				best=$other
				best_order=$order
			fi
		done <"$tmp/releases"
		[ -n "$best" ] || fail "no published stable release, or release from before channels, carries aether-release.json to mark Latest"
		[ "$best" != "$marked" ] || break
		gh release edit "$best" --latest=true
		marked=$best
	done
	echo "Latest is $best"
}

list_releases
awk -v tag="$tag" '$1 == tag' "$tmp/releases" >"$tmp/release"
case $(wc -l <"$tmp/release" | tr -d ' ') in
0) state=none ;;
1) read -r _ state _ names <"$tmp/release" ;;
*) fail "$tag has more than one release; delete the extra drafts" ;;
esac
printf '%s\n' "${names:-}" | tr ',' '\n' | sed '/^$/d' >"$tmp/names"

ours=$(release_order "$tag" "$manifest") ||
	fail "$manifest does not describe $tag with channel fields an updater accepts, or $tag has no versionCode"
case $ours in
"nightly "* | "beta "*) publish_prerelease=true ;;
*) publish_prerelease=false ;;
esac

# Release notes start at the highest published release below the tag that
# is not a prerelease.
previous=
previous_order="none 0 0"
while read -r other draft prerelease others; do
	[ "$draft $prerelease" = "false false" ] || continue
	case ",$others," in
	*,aether-release.json,*) published_order "$other" || continue ;;
	*) order="none 0 $(sh "$script_dir/android-version-code.sh" "$other" 2>/dev/null </dev/null)" || continue ;;
	esac
	if above "$ours" "$order" && above "$order" "$previous_order"; then
		previous=$other
		previous_order=$order
	fi
done <"$tmp/releases"

if [ "$state" = false ]; then
	if grep -qx 'aether-release\.json' "$tmp/names"; then
		# Assets signed by a published manifest are never replaced.
		rm -rf "$tmp/public"
		mkdir "$tmp/public"
		fetch aether-release.json aether-release.json.sig
		cp "$tmp/public/aether-release.json" "$tmp/published.json"
		for field in commit version; do
			was=$(jq -r ".$field" "$tmp/published.json")
			built=$(jq -r ".$field" "$manifest")
			[ "$was" = "$built" ] || fail "tag $tag was published from $field $was; this run built $built"
		done
		{
			jq -r '.assets | keys[]' "$tmp/published.json"
			printf '%s\n' checksums.txt aether-release.json aether-release.json.sig
		} | sort >"$tmp/signed"
		sort "$tmp/names" | diff -u "$tmp/signed" - >&2 ||
			fail "published $tag holds other assets than its aether-release.json lists"
		verify_public "$tmp/published.json"
		echo "$tag is already published and verified"
		reconcile_latest
		exit 0
	fi
	echo "publish-release: $tag is published without aether-release.json, so it is incomplete; returning it to a draft until it is verified" >&2
	gh release edit "$tag" --draft=true
	state=true
fi

if [ "$state" = none ]; then
	require_tag_commit
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

require_tag_commit
gh release edit "$tag" --draft=false --prerelease="$publish_prerelease" --latest=false
verify_public "$manifest"
fetch aether-release.json aether-release.json.sig
for name in aether-release.json aether-release.json.sig; do
	cmp "$dist/$name" "$tmp/public/$name" >&2 || fail "$public/$name differs from $dist/$name"
done
echo "published $tag"
reconcile_latest
