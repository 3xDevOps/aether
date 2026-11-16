#!/bin/sh
# Resolves what promote.yml builds from its inputs, FROM (a nightly or
# beta tag, or a full commit SHA of one), TO (beta or stable), VERSION
# (optional X.Y.Z) and FIRST_POINTER (true for the migration's first
# stable). It reads the repository's tags, its published releases, the
# source's signed manifest and the channels release's pointers, and has
# promote-plan ($PROMOTE_PLAN, built from main) decide. Prints key=value
# lines for GITHUB_OUTPUT: promote-plan's, then source and
# source-published-at.
set -eu

repo="${GITHUB_REPOSITORY:?promote-plan: GITHUB_REPOSITORY must name the repository}"
tool="${PROMOTE_PLAN:?promote-plan: PROMOTE_PLAN must name the built promote-plan command}"
from="${FROM:?promote-plan: FROM is empty}"
to="${TO:?promote-plan: TO is empty}"

fail() {
	echo "promote-plan: $1" >&2
	exit 1
}

if [ "$(printf '%s' "$from" | wc -l)" -ne 0 ] || ! printf '%s\n' "$from" | grep -Eqx 'v[0-9A-Za-z.-]+|[0-9a-f]{40}'; then
	fail "from $from is not a release tag or a full commit SHA"
fi
printf '%s\n' "$to" | grep -Eqx 'beta|stable' || fail "to $to is not beta or stable"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/manifest" "$tmp/pointers"

# An annotated tag's peeled line names its commit; a lightweight tag has
# only the plain one.
git ls-remote --tags "https://github.com/$repo.git" 'refs/tags/v*' >"$tmp/refs"
awk '{ sub("^refs/tags/", "", $2) }
	$2 ~ /\^\{\}$/ { sub(/\^\{\}$/, "", $2); peeled[$2] = $1; next }
	{ plain[$2] = $1 }
	END { for (t in plain) print t, (t in peeled ? peeled[t] : plain[t]) }' "$tmp/refs" >"$tmp/tags"

gh api --paginate "repos/$repo/releases?per_page=100" --jq '.[] | select(.draft | not) | .tag_name' >"$tmp/published"

case $from in
v*) source=$from ;;
*) source=$("$tool" source -from "$from" -to "$to" -tags "$tmp/tags" -published "$tmp/published") ;;
esac
grep -qxF "$source" "$tmp/published" || fail "$source is not a published release; promote only a published one"
gh release download "$source" --repo "$repo" --pattern aether-release.json --pattern aether-release.json.sig --dir "$tmp/manifest"

release=$(gh api "repos/$repo/releases/tags/channels") ||
	fail "cannot read the channels release. A repository admin creates it once: gh release create channels --prerelease --title channels --notes 'Signed channel pointers.' --target main"
for name in $(printf '%s' "$release" | jq -r '.assets[].name | select(test("^(stable|beta)\\.json(\\.sig)?$"))'); do
	gh release download channels --repo "$repo" --pattern "$name" --dir "$tmp/pointers"
done

first=
[ "${FIRST_POINTER:-false}" = true ] && first=-first-pointer
"$tool" resolve -from "$source" -to "$to" ${VERSION:+-version "$VERSION"} $first \
	-tags "$tmp/tags" -published "$tmp/published" -manifest "$tmp/manifest/aether-release.json" -pointers "$tmp/pointers"
echo "source=$source"
echo "source-published-at=$(gh api "repos/$repo/releases/tags/$source" --jq .published_at)"
