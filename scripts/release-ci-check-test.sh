#!/bin/sh
# Only the GitHub API boundary is replaced; the real checker parses/selects runs.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/api"
export GH_FIXTURES="$tmp/api"
export TEST_SHA=0123456789abcdef0123456789abcdef01234567
export PATH="$tmp/bin:$PATH"

cat >"$tmp/bin/gh" <<'EOF'
#!/bin/sh
set -eu
[ "$1" = api ] || exit 90
shift
case "$*" in
"repos/3xDevOps/aether/actions/workflows/ci.yml") file=workflow ;;
"--paginate --slurp repos/3xDevOps/aether/actions/workflows/42/runs?event=push&branch=main&head_sha=$TEST_SHA&per_page=100") file=pages ;;
repos/3xDevOps/aether/actions/runs/*) file="run-${1##*/}" ;;
*) echo "unexpected API request: $*" >&2; exit 91 ;;
esac
[ "${FAIL_API:-}" != "$file" ] || exit 92
cat "$GH_FIXTURES/$file.json"
EOF
chmod 0755 "$tmp/bin/gh"

jq -n --arg sha "$TEST_SHA" '{
	id: 101, run_number: 10, run_attempt: 1, workflow_id: 42,
	path: ".github/workflows/ci.yml",
	repository: {full_name: "3xDevOps/aether"},
	head_repository: {full_name: "3xDevOps/aether"},
	event: "push", head_branch: "main", head_sha: $sha,
	status: "completed", conclusion: "success",
	html_url: "https://github.com/3xDevOps/aether/actions/runs/101"
}' >"$tmp/base.json"

reset_fixture() {
	unset FAIL_API
	printf '%s\n' '{"id":42,"path":".github/workflows/ci.yml","name":"CI"}' >"$GH_FIXTURES/workflow.json"
	cp "$tmp/base.json" "$GH_FIXTURES/run-101.json"
	jq '[{total_count: 1, workflow_runs: [.] }]' "$tmp/base.json" >"$GH_FIXTURES/pages.json"
}

check_accept() {
	label=$1
	expected_id=$2
	if ! sh "$script_dir/release-ci-check.sh" 3xDevOps/aether "$TEST_SHA" >"$tmp/out" 2>"$tmp/err"; then
		printf 'FAIL: %s was rejected\n' "$label" >&2
		cat "$tmp/err" >&2
		exit 1
	fi
	[ "$(cat "$tmp/out")" = "https://github.com/3xDevOps/aether/actions/runs/$expected_id" ] || {
		printf 'FAIL: %s selected the wrong run\n' "$label" >&2
		exit 1
	}
}

check_reject() {
	label=$1
	shift
	if sh "$script_dir/release-ci-check.sh" "$@" >"$tmp/out" 2>"$tmp/err"; then
		printf 'FAIL: %s authorized a release\n' "$label" >&2
		exit 1
	fi
	[ ! -s "$tmp/out" ] && [ -s "$tmp/err" ] || {
		printf 'FAIL: %s lacked a closed-gate diagnostic\n' "$label" >&2
		exit 1
	}
}
reject_fixture() { check_reject "$1" 3xDevOps/aether "$TEST_SHA"; }

reset_fixture
check_accept 'exact main push' 101
check_reject 'missing arguments'
check_reject 'short SHA' 3xDevOps/aether 0123456
check_reject 'nonhex SHA' 3xDevOps/aether z123456789abcdef0123456789abcdef01234567
check_reject 'repository query injection' '3xDevOps/aether?x=y' "$TEST_SHA"

# Repository identity is case-insensitive, as it is on GitHub.
jq '.repository.full_name = "3xdevops/Aether" | .head_repository.full_name = "3XDEVOPS/AETHER"' \
	"$tmp/base.json" >"$GH_FIXTURES/run-101.json"
check_accept 'repository casing' 101

for api in workflow pages run-101; do
	reset_fixture
	export FAIL_API=$api
	reject_fixture "$api API failure"
done

for expression in '.id = null' '.id = "42"' '.path = ".github/workflows/other.yml"' '.name = "Not CI"'; do
	reset_fixture
	jq "$expression" "$GH_FIXTURES/workflow.json" >"$tmp/change.json"
	cp "$tmp/change.json" "$GH_FIXTURES/workflow.json"
	reject_fixture "workflow identity: $expression"
done

for json in '' 'not-json' 'null' '{}' '[]' '[{"total_count":0,"workflow_runs":[]}]' \
	'[{"total_count":1,"workflow_runs":[]}]' '[{"total_count":"0","workflow_runs":[]}]'; do
	reset_fixture
	printf '%s\n' "$json" >"$GH_FIXTURES/pages.json"
	reject_fixture "malformed or empty run response: $json"
done

# Identity checks apply to both the list and the selected run's current attempt.
for expression in \
	'.repository.full_name = "attacker/aether"' \
	'.head_repository.full_name = "attacker/aether"' \
	'.workflow_id = 43' '.path = ".github/workflows/other.yml"' \
	'.event = "pull_request"' '.event = "merge_group"' '.event = "workflow_dispatch"' \
	'.head_branch = "feature"' '.head_sha = "fedcba9876543210fedcba9876543210fedcba98"' \
	'.html_url = "https://github.com/attacker/aether/actions/runs/101"' \
	'del(.run_number)' '.run_attempt = 0' 'del(.repository)' '.id = "101"'; do
	reset_fixture
	jq "$expression" "$tmp/base.json" | jq '[{total_count:1, workflow_runs:[.]}]' >"$GH_FIXTURES/pages.json"
	reject_fixture "list identity: $expression"
	reset_fixture
	jq "$expression" "$tmp/base.json" >"$GH_FIXTURES/run-101.json"
	reject_fixture "attempt identity: $expression"
done

# The newest matching run is selected across pages regardless of list order.
jq '.id = 102 | .run_number = 11 | .html_url = "https://github.com/3xDevOps/aether/actions/runs/102"' \
	"$tmp/base.json" >"$tmp/new.json"
reset_fixture
cp "$tmp/new.json" "$GH_FIXTURES/run-102.json"
jq -s '[{total_count:2, workflow_runs:[.[0]]}, {total_count:2, workflow_runs:[.[1]]}]' \
	"$tmp/new.json" "$tmp/base.json" >"$GH_FIXTURES/pages.json"
check_accept 'newest run across unordered pages' 102

# In every non-success state, an older green run must not authorize publication.
for expression in \
	'.status = "queued" | .conclusion = null' \
	'.status = "in_progress" | .conclusion = null' \
	'.conclusion = "failure"' '.conclusion = "cancelled"' \
	'.conclusion = "timed_out"' '.conclusion = "action_required"' \
	'.conclusion = "neutral"' '.conclusion = "skipped"' '.conclusion = null'; do
	jq "$expression" "$tmp/new.json" >"$GH_FIXTURES/run-102.json"
	jq -s '[{total_count:2, workflow_runs:.}]' "$tmp/base.json" "$GH_FIXTURES/run-102.json" >"$GH_FIXTURES/pages.json"
	reject_fixture "newest run non-success: $expression"
done

reset_fixture
jq -s '[{total_count:2, workflow_runs:.}]' "$tmp/base.json" "$tmp/base.json" >"$GH_FIXTURES/pages.json"
reject_fixture 'duplicate runs from inconsistent pagination'
reset_fixture
jq '[{total_count:2, workflow_runs:[.]}, {total_count:1, workflow_runs:[]}]' "$tmp/base.json" >"$GH_FIXTURES/pages.json"
reject_fixture 'changing page totals'

# A rerun supersedes the successful attempt returned by a stale list response.
for expression in '.status = "in_progress" | .conclusion = null' '.conclusion = "failure"'; do
	reset_fixture
	jq ".run_attempt = 2 | $expression" "$tmp/base.json" >"$GH_FIXTURES/run-101.json"
	reject_fixture "rerun supersedes earlier success: $expression"
done
reset_fixture
jq '.run_attempt = 2' "$tmp/base.json" >"$GH_FIXTURES/run-101.json"
check_accept 'successful latest rerun' 101
jq '[{total_count:1, workflow_runs:[. | .run_attempt = 3]}]' "$tmp/base.json" >"$GH_FIXTURES/pages.json"
reject_fixture 'stale attempt detail'
reset_fixture
cp "$tmp/new.json" "$GH_FIXTURES/run-101.json"
reject_fixture 'detail substituted a different run'
reset_fixture
printf 'null\n' >"$GH_FIXTURES/run-101.json"
reject_fixture 'malformed latest attempt'

printf '%s\n' 'release CI authorization tests passed'
