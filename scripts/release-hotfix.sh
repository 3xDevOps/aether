#!/bin/sh
# Decides whether the hotfix tag $2, vX.Y.Z with Z at least 1, may release
# commit $3 of repository $1, and prints the name=value lines release.yml
# needs: build, hotfix, base-tag, base-commit and previous-commit. Run it
# from a checkout of main whose origin is the repository, with
# RELEASE_HOTFIX naming the built scripts/release-hotfix command, which
# derives every value from the signed manifests of published releases.
#
# The commit must be the head of release/X.Y, contain vX.Y.(Z-1) and
# differ from it.
# The branch must be cut from the line's base, the stable release with
# hotfix 0 that the hotfix count of vX.Y.(Z-1) leads back to: its only
# merge base with main is that commit, so it carries nothing from main
# after the base.
set -eu

fail() {
	echo "release-hotfix: $1" >&2
	exit 1
}

[ "$#" -eq 3 ] || fail 'usage: release-hotfix.sh <owner/repository> <vX.Y.Z> <full-commit-sha>'
repository=$1
tag=$2
sha=$3
tool=${RELEASE_HOTFIX:?release-hotfix: RELEASE_HOTFIX must name the built release-hotfix command}
printf '%s\n' "$repository" | LC_ALL=C grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail 'invalid repository'
printf '%s\n' "$sha" | LC_ALL=C grep -Eq '^[0-9a-f]{40}$' || fail 'expected a full lowercase commit SHA'
printf '%s\n' "$tag" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.[1-9][0-9]{0,2}$' ||
	fail "$tag is not a hotfix tag vX.Y.Z with Z at least 1"
core=${tag#v}
branch=release/${core%.*}
previous_tag=v${core%.*}.$((${core##*.} - 1))

git fetch --quiet --no-tags origin "+refs/heads/main:refs/remotes/origin/main" \
	"+refs/heads/$branch:refs/remotes/origin/$branch" ||
	fail "cannot fetch main and $branch; a hotfix is tagged on $branch"
head=$(git rev-parse --verify --quiet "refs/remotes/origin/$branch^{commit}") || fail "$branch has no commit"
[ "$sha" = "$head" ] || fail "$sha is not the head of $branch ($head); tag the head, whose push CI is green"

out=$("$tool" -tag "$tag" -repo "https://github.com/$repository")
value() { printf '%s\n' "$out" | sed -n "s/^$1=//p"; }
base_tag=$(value base-tag)
base_commit=$(value base-commit)
previous_commit=$(value previous-commit)
for commit in "$base_commit" "$previous_commit"; do
	printf '%s\n' "$commit" | LC_ALL=C grep -Eq '^[0-9a-f]{40}$' || fail "a signed manifest names commit '$commit'"
done

[ "$sha" != "$previous_commit" ] || fail "$sha is $previous_tag's commit; a hotfix needs a change on top of it"
git merge-base --is-ancestor "$previous_commit" "$sha" 2>/dev/null ||
	fail "$sha does not contain $previous_tag ($previous_commit); hotfixes on $branch follow each other"
bases=$(git merge-base --all refs/remotes/origin/main "$sha" | paste -sd ' ' -)
[ "$bases" = "$base_commit" ] ||
	fail "$branch is not cut from the $base_tag commit $base_commit: main and $sha share ${bases:-no commit}"
printf '%s\n' "$out"
