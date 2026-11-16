#!/bin/sh
# Verifies the build attestation of an edge image, $2 (image@digest), made
# for release $3, and with "qualify" finds the run that built that release
# and requires its edge-qualify job to have succeeded.
#
# release-build.yml signs a release Promote or nightly.yml built with
# source ref refs/heads/main, from a promote.yml or nightly.yml run, and a
# pushed tag with
# refs/tags/<tag>, from a release.yml run. release.yml itself signed the
# releases from before release-build.yml. Releases built before the
# repository took its current name are signed under its old name,
# 3xDevOps/aether. The run comes from the verified certificate, so no other
# run of either workflow can stand in for it.
set -eu

if [ "$#" -ne 3 ] || { [ "$1" != verify ] && [ "$1" != qualify ]; }; then
	echo "usage: edge-attestation.sh {verify|qualify} <image@digest> <tag>" >&2
	exit 2
fi
mode="$1"
ref="$2"
tag="$3"
repo="${GITHUB_REPOSITORY:?edge-attestation: GITHUB_REPOSITORY must name the repository}"

fail() {
	echo "::error::$1" >&2
	exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

# Each line: the repository name signed under, the signer workflow, the
# source ref, and the workflows, comma separated, one of which must have
# started the run.
for signed in "$repo" 3xDevOps/aether; do
	workflows="https://github.com/$signed/.github/workflows"
	cat <<EOF
$signed release-build.yml refs/heads/main $workflows/promote.yml@refs/heads/main,$workflows/nightly.yml@refs/heads/main
$signed release-build.yml refs/tags/$tag $workflows/release.yml@refs/tags/$tag
$signed release.yml refs/tags/$tag $workflows/release.yml@refs/tags/$tag
EOF
done >"$tmp/identities"

# The attestation API can answer a moment after the upload.
verified=
for attempt in 1 2 3; do
	while read -r signed signer source caller; do
		if gh attestation verify "oci://$ref" --repo "$signed" \
			--signer-workflow "$signed/.github/workflows/$signer" \
			--source-ref "$source" --deny-self-hosted-runners --format json >"$tmp/verified.json" 2>"$tmp/err"; then
			verified=$caller
			break 2
		fi
	done <"$tmp/identities"
	[ "$attempt" = 3 ] || sleep "${EDGE_ATTESTATION_RETRY_DELAY:-10}"
done
[ -n "$verified" ] || fail "$ref has no attestation from release-build.yml or release.yml in $repo or 3xDevOps/aether for $tag: $(cat "$tmp/err")"
[ "$mode" = qualify ] || exit 0

runs=$(jq -r --arg callers "$verified" '($callers | ascii_downcase | split(",")) as $allowed
	| [.[].verificationResult.signature.certificate
	| select(.buildConfigURI | ascii_downcase | IN($allowed[]))
	| .runInvocationURI | capture("/actions/runs/(?<id>[0-9]+)(/|$)").id] | unique | .[]' "$tmp/verified.json")
[ -n "$runs" ] || fail "the attestation of $ref was not made by a run of $verified, so no run qualified $tag"

for id in $runs; do
	run=$(gh api "repos/$repo/actions/runs/$id")
	url=$(printf '%s' "$run" | jq -r .html_url)
	qualify=$(gh api --paginate "repos/$repo/actions/runs/$id/jobs?filter=latest&per_page=100" |
		jq -s '[.[].jobs[] | select(.name == "edge-qualify" or (.name | startswith("edge-qualify ")) or (.name | endswith(" / edge-qualify")))]')
	if [ "$(printf '%s' "$qualify" | jq length)" -gt 0 ]; then
		if printf '%s' "$qualify" | jq -e 'all(.conclusion == "success")' >/dev/null; then
			echo "edge-qualify succeeded in $url"
			exit 0
		fi
		echo "edge-qualify did not succeed in $url" >&2
	# A release run from before edge-qualify existed has no such job; then
	# the whole run must have succeeded.
	elif [ "$(printf '%s' "$run" | jq -r '.status + " " + (.conclusion // "")')" = "completed success" ]; then
		echo "the release run $url succeeded (it has no edge-qualify job)"
		exit 0
	else
		echo "the release run $url has not succeeded, and it has no edge-qualify job" >&2
	fi
done
fail "no run that built $tag qualified its edge image"
