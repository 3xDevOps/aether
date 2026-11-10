#!/bin/sh
# Print the URL of the merge queue CI run that succeeded on this exact
# commit, when a push to main may reuse that verdict; print nothing when it
# must run the full suite. The queue moves main to the commit its group
# tested, so that verdict covers the push.
#
# usage: ci-queue-verdict.sh <owner/repository> <commit before the push> <full-commit-sha>
# Only the newest merge_group run of ci.yml for the commit counts, and only
# its latest attempt, which must have completed with conclusion success and
# a passed `CI gate`: a job that a cancellation kept from starting reports
# skipped, so the gate alone does not prove the suite ran. A push that
# changes a file a cache key hashes (ci-classify-areas.sh caches) never
# reuses one: only a run on main can save the caches the queue restores. A failed lookup, or a compare at GitHub's
# 300-file limit, prints nothing and exits 0.
set -eu

[ "$#" -eq 3 ] || { echo "usage: ci-queue-verdict.sh <owner/repository> <before-sha> <full-commit-sha>" >&2; exit 2; }
repository=$1
before=$2
sha=$3
printf '%s\n' "$sha" | LC_ALL=C grep -Eq '^[0-9a-f]{40}$' || { echo "ci-queue-verdict: expected a full lowercase commit SHA" >&2; exit 2; }
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

none() {
	printf '::notice::%s; running the full suite\n' "$*" >&2
	exit 0
}

endpoint="repos/$repository/actions"
run=$(gh api "$endpoint/workflows/ci.yml/runs?event=merge_group&head_sha=$sha&per_page=100" --jq '
	[.workflow_runs[] | select(.path == ".github/workflows/ci.yml" and .event == "merge_group" and .head_sha == "'"$sha"'" and
		(.repository.full_name | ascii_downcase) == ("'"$repository"'" | ascii_downcase) and
		(.head_repository.full_name | ascii_downcase) == ("'"$repository"'" | ascii_downcase))] |
	max_by(.run_number) // empty | "\(.id) \(.status)/\(.conclusion) \(.html_url)"') || none "cannot list merge queue runs of $sha"
[ -n "$run" ] || none "no merge queue run tested $sha"
id=${run%% *}
run=${run#* }
state=${run%% *}
run=${run#* }
[ "$state" = completed/success ] || none "merge queue run $id is $state"
gate=$(gh api --paginate "$endpoint/runs/$id/jobs?per_page=100" --jq '.jobs[] | select(.name == "CI gate") | .conclusion') ||
	none "cannot read the jobs of merge queue run $id"
[ "$gate" = success ] || none "CI gate of merge queue run $id is '${gate:-missing}'"

# previous_filename counts a rename out of a cache key input.
files=$(gh api "repos/$repository/compare/$before...$sha" --jq '
	if (.files | length) >= 300 then error("at the file limit") else .files[] | .filename, (.previous_filename // empty) end') ||
	none "cannot list the files of $before...$sha completely"
caches=$(printf '%s\n' "$files" | sh "$script_dir/ci-classify-areas.sh" caches) || none "cannot classify the files of $before...$sha"
[ "$caches" = false ] || none "$before...$sha changes a cache key input"
printf '%s\n' "$run"
