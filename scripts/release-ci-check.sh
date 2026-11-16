#!/bin/sh
# Reuse full main CI only for the exact commit being released. No green fallback.
# The push run on main is that verdict: it runs the full suite, or it skips the
# suite only when scripts/ci-queue-verdict.sh found that the newest merge
# queue run of this same commit (and so the same tree and ci.yml) succeeded
# on its latest attempt. With --branch release/X.Y it reads the push runs
# on that branch instead, for a hotfix; a push there always runs the full
# suite.
set -eu

fail() {
	printf 'release-ci-check: %s\n' "$*" >&2
	exit 1
}

branch=main
if [ "${1-}" = --branch ]; then
	[ "$#" -ge 2 ] || fail 'usage: release-ci-check.sh [--branch release/X.Y] <owner/repository> <full-commit-sha>'
	printf '%s\n' "$2" | LC_ALL=C grep -Eq '^release/(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail "branch $2 is not release/X.Y"
	branch=$2
	shift 2
fi
[ "$#" -eq 2 ] || fail 'usage: release-ci-check.sh [--branch release/X.Y] <owner/repository> <full-commit-sha>'
repository=$1
sha=$2
printf '%s\n' "$repository" | LC_ALL=C grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail 'invalid repository'
printf '%s\n' "$sha" | LC_ALL=C grep -Eq '^[0-9a-f]{40}$' || fail 'expected a full lowercase commit SHA'

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
endpoint="repos/$repository/actions"
gh api "$endpoint/workflows/ci.yml" >"$tmp/workflow.json" || fail 'cannot look up CI workflow'
workflow=$(jq -er '
	select(.path == ".github/workflows/ci.yml" and .name == "CI") |
	.id | select(type == "number" and . > 0 and . == floor)
' "$tmp/workflow.json") || fail 'invalid CI workflow identity'

# Do not filter on success: a newer failed/queued run must supersede an older
# successful run. Fetch every page and reject incomplete or inconsistent data.
gh api --paginate --slurp \
	"$endpoint/workflows/$workflow/runs?event=push&branch=$branch&head_sha=$sha&per_page=100" \
	>"$tmp/pages.json" || fail "cannot look up exact-commit $branch CI runs"
identity='
	def positive_integer: type == "number" and . > 0 and . == floor;
	def trusted_run:
		(.id | positive_integer) and
		(.run_number | positive_integer) and
		(.run_attempt | positive_integer) and
		.workflow_id == $workflow and .path == ".github/workflows/ci.yml" and
		(.repository.full_name | ascii_downcase) == ($repository | ascii_downcase) and
		(.head_repository.full_name | ascii_downcase) == ($repository | ascii_downcase) and
		.event == "push" and .head_branch == $branch and .head_sha == $sha and
		(.html_url | ascii_downcase) ==
			("https://github.com/" + ($repository | ascii_downcase) + "/actions/runs/" + (.id | tostring)) and
		(.status | type == "string") and
		(.conclusion == null or (.conclusion | type == "string"));
'
jq -e --arg repository "$repository" --arg branch "$branch" --arg sha "$sha" --argjson workflow "$workflow" \
	"$identity"'
	select(type == "array" and length > 0) |
	select(all(.[]; (.total_count | type == "number" and . >= 0 and . == floor) and
		(.workflow_runs | type == "array"))) |
	.[0].total_count as $count |
	select(all(.[]; .total_count == $count)) |
	[.[].workflow_runs[]] |
	select(length == $count and length > 0) |
	select((map(.id) | unique | length) == length) |
	select(all(.[]; trusted_run)) |
	max_by([.run_number, .id])
' "$tmp/pages.json" >"$tmp/selected.json" || fail "no complete, trusted exact-commit $branch CI run set"
run=$(jq -er '.id' "$tmp/selected.json")

# The run endpoint reports its current attempt, not the historical attempt that
# happened to succeed. A rerun starting after the list was read also closes the gate.
gh api "$endpoint/runs/$run" >"$tmp/current.json" || fail 'cannot read latest CI attempt'
url=$(jq -er --arg repository "$repository" --arg branch "$branch" --arg sha "$sha" --argjson workflow "$workflow" \
	--slurpfile selected "$tmp/selected.json" "$identity"'
	select(trusted_run) |
	select(.id == $selected[0].id and .run_number == $selected[0].run_number and
		.run_attempt >= $selected[0].run_attempt) |
	select(.status == "completed" and .conclusion == "success") |
	.html_url
' "$tmp/current.json") || fail "latest exact-commit $branch CI run/attempt is not completed successfully"
printf '%s\n' "$url"
