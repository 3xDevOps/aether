#!/bin/sh
# Prunes nightlies: deletes the GitHub release and the image tags of each
# published nightly older than 14 days, except the newest nightly and every
# nightly above stable.json's build, which a server that left nightly may
# still wait on. The git tag stays, so its build number stays taken.
# nightly-plan ($NIGHTLY_PLAN) decides from the verified pointers and
# manifests; nothing is pruned while stable.json or nightly.json cannot be
# read and verified. --dry-run prints what would be deleted and deletes
# nothing.
#
# An image version is deleted only when every tag it carries is one of the
# nightly's own: <tag>, <tag>-amd64, <tag>-arm64, its commit and
# sha-<abbreviated commit>. A version that also carries another tag, such as a beta
# rebuilt to the same digest, is kept; a nightly whose image a channel tag
# still carries keeps its release and every image.
set -eu

dry_run=false
if [ "${1:-}" = --dry-run ]; then
	dry_run=true
	shift
fi
if [ "$#" -ne 0 ]; then
	echo "usage: nightly-prune.sh [--dry-run]" >&2
	exit 2
fi
repo="${GITHUB_REPOSITORY:?nightly-prune: GITHUB_REPOSITORY must name the repository}"
tool="${NIGHTLY_PLAN:?nightly-prune: NIGHTLY_PLAN must name the built nightly-plan command}"
org=${repo%%/*}
packages="aether-browser aether-edge aether-standard"

fail() {
	echo "nightly-prune: $1" >&2
	exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/pointers" "$tmp/manifests"

gh api --paginate "repos/$repo/releases?per_page=100" \
	--jq '.[] | "\(.tag_name) \(.draft) \(.published_at // "-")"' >"$tmp/releases"
release=$(gh api "repos/$repo/releases/tags/channels") ||
	fail "cannot read the channels release; nothing is pruned"
for name in $(printf '%s' "$release" | jq -r '.assets[].name | select(test("^(nightly|stable)\\.json(\\.sig)?$"))'); do
	gh release download channels --repo "$repo" --pattern "$name" --dir "$tmp/pointers"
done

now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
cutoff=$(($(date -u +%s) - 1209600))
while read -r tag draft published; do
	case $tag in v*-nightly.*) ;; *) continue ;; esac
	[ "$draft" = false ] || continue
	[ "$(date -u -d "$published" +%s)" -le "$cutoff" ] || continue
	gh release download "$tag" --repo "$repo" --pattern aether-release.json --pattern aether-release.json.sig \
		--dir "$tmp/manifests/$tag" || echo "nightly-prune: cannot download the manifest of $tag; keeping it" >&2
done <"$tmp/releases"

"$tool" prune -releases "$tmp/releases" -pointers "$tmp/pointers" -manifests "$tmp/manifests" -now "$now" >"$tmp/prune"
if [ ! -s "$tmp/prune" ]; then
	echo "nothing to prune"
	exit 0
fi

for package in $packages; do
	gh api --paginate "orgs/$org/packages/container/$package/versions?per_page=100" \
		--jq '.[] | "\(.id) \(.metadata.container.tags | join(","))"' >"$tmp/versions-$package"
done

while read -r tag commit; do
	# A host on a channel tag runs that image, and promote-edge.yml reads
	# its release's manifest to order the next move, so a release whose
	# image a channel tag still carries is kept whole.
	channel=$(cat "$tmp"/versions-* | awk -v tag="$tag" '{
		n = split($2, t, ","); ours = 0; ch = ""
		for (i = 1; i <= n; i++) {
			if (t[i] == tag) ours = 1
			if (t[i] ~ /^(nightly|beta|stable|latest)$/) ch = t[i]
		}
		if (ours && ch != "") { print ch; exit }
	}')
	if [ -n "$channel" ]; then
		echo "keeping release $tag: its image is still $channel"
		continue
	fi
	for package in $packages; do
		while read -r id tags; do
			[ -n "$tags" ] || continue
			ours=false
			theirs=
			for t in $(printf '%s' "$tags" | tr ',' ' '); do
				case $t in
				"$tag" | "$tag-amd64" | "$tag-arm64") ours=true ;;
				"$commit") ;;
				# git rev-parse --short: 7 characters or more.
				sha-???????*) [ "${commit#"${t#sha-}"}" != "$commit" ] || theirs="$theirs $t" ;;
				*) theirs="$theirs $t" ;;
				esac
			done
			[ "$ours" = true ] || continue
			if [ -n "$theirs" ]; then
				echo "keeping $package version $id ($tags): it also carries$theirs"
			elif [ "$dry_run" = true ]; then
				echo "would delete $package version $id ($tags)"
			else
				gh api -X DELETE "orgs/$org/packages/container/$package/versions/$id" >/dev/null
				echo "deleted $package version $id ($tags)"
			fi
		done <"$tmp/versions-$package"
	done
	if [ "$dry_run" = true ]; then
		echo "would delete release $tag (its git tag stays)"
	else
		gh release delete "$tag" --repo "$repo" --yes
		echo "deleted release $tag (its git tag stays)"
	fi
done <"$tmp/prune"
