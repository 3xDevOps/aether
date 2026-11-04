#!/bin/sh
# Cancel the merge queue runs of groups the queue has discarded: runs whose
# gh-readonly-queue branch is gone or now points at another commit.
#
# usage: ci-reap-queue-runs.sh <owner/repository> <this run's queue branch>
# Runs are listed before branches, so a group created in between is never
# mistaken for a discarded one. A failed lookup, or a branch list without
# this run's own branch, cancels nothing and exits 0: this is cleanup inside
# a required job.
set -eu

[ "$#" -eq 2 ] || { echo "usage: ci-reap-queue-runs.sh <owner/repository> <queue branch>" >&2; exit 2; }
repo=$1
own=$2

skip() {
	printf '::warning::%s; cancelling no runs\n' "$*"
	exit 0
}

runs=
for status in requested waiting pending queued in_progress; do
	out=$(gh api --paginate "repos/$repo/actions/runs?event=merge_group&status=$status&per_page=100" \
		--jq '.workflow_runs[] | "\(.id) \(.head_branch) \(.head_sha)"') ||
		skip "cannot list $status merge queue runs"
	runs="$runs$out
"
done
live=$(gh api --paginate "repos/$repo/git/matching-refs/heads/gh-readonly-queue/" \
	--jq '.[] | "\(.ref | ltrimstr("refs/heads/")) \(.object.sha)"') ||
	skip "cannot list merge queue branches"
printf '%s\n' "$live" | grep -q "^$own " || skip "the branch list lacks this run's own branch $own"

printf '%s' "$runs" | while read -r id branch sha; do
	[ -n "$id" ] || continue
	printf '%s\n' "$live" | grep -qxF "$branch $sha" && continue
	echo "Cancelling run $id of discarded group $branch at $sha"
	gh api -X POST "repos/$repo/actions/runs/$id/cancel" >/dev/null ||
		printf '::warning::could not cancel run %s; it may have finished\n' "$id"
done
