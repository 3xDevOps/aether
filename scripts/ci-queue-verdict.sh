#!/bin/sh
# Print the URL of the merge queue CI run whose `CI gate` passed on this exact
# commit, or nothing when there is none. A push to main lands the commit its
# merge group already tested, so that verdict covers the push.
#
# usage: ci-queue-verdict.sh <owner/repository> <full-commit-sha>
# Only the newest merge_group run of ci.yml for the commit counts, and only
# its latest attempt. A failed lookup prints nothing and exits 0: the caller
# then runs the full suite.
set -eu

[ "$#" -eq 2 ] || { echo "usage: ci-queue-verdict.sh <owner/repository> <full-commit-sha>" >&2; exit 2; }
repository=$1
sha=$2
printf '%s\n' "$sha" | LC_ALL=C grep -Eq '^[0-9a-f]{40}$' || { echo "ci-queue-verdict: expected a full lowercase commit SHA" >&2; exit 2; }

none() {
	printf '::notice::%s; running the full suite\n' "$*" >&2
	exit 0
}

endpoint="repos/$repository/actions"
run=$(gh api "$endpoint/workflows/ci.yml/runs?event=merge_group&head_sha=$sha&per_page=100" --jq '
	[.workflow_runs[] | select(.path == ".github/workflows/ci.yml" and .event == "merge_group" and .head_sha == "'"$sha"'" and
		(.repository.full_name | ascii_downcase) == ("'"$repository"'" | ascii_downcase) and
		(.head_repository.full_name | ascii_downcase) == ("'"$repository"'" | ascii_downcase))] |
	max_by(.run_number) // empty | "\(.id) \(.html_url)"') || none "cannot list merge queue runs of $sha"
[ -n "$run" ] || none "no merge queue run tested $sha"
id=${run%% *}
gate=$(gh api --paginate "$endpoint/runs/$id/jobs?per_page=100" --jq '.jobs[] | select(.name == "CI gate") | .conclusion') ||
	none "cannot read the jobs of merge queue run $id"
[ "$gate" = success ] || none "CI gate of merge queue run $id is '${gate:-missing}'"
printf '%s\n' "${run#* }"
