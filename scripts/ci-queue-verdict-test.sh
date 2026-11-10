#!/bin/sh
# ci-queue-verdict.sh: prints the newest merge queue run of the commit only
# when its CI gate passed, and nothing on any other answer or a failed
# lookup. Only the gh boundary is replaced.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin"
sha=0123456789abcdef0123456789abcdef01234567
export FIXTURES="$tmp" PATH="$tmp/bin:$PATH" SHA="$sha"

# Applies --jq with the real jq, as gh does.
cat >"$tmp/bin/gh" <<'STUB'
#!/bin/sh
set -eu
[ "$1" = api ] || exit 90
shift
[ "$1" != --paginate ] || shift
url=$1
shift
[ "$1" = --jq ] || exit 91
case "$url" in
"repos/o/r/actions/workflows/ci.yml/runs?event=merge_group&head_sha=$SHA&per_page=100") file=runs ;;
repos/o/r/actions/runs/*/jobs?per_page=100) file=jobs-$(echo "$url" | cut -d/ -f6) ;;
*) echo "unexpected request: $url" >&2; exit 92 ;;
esac
[ "${FAIL:-}" != "$file" ] || exit 1
jq -r "$2" "$FIXTURES/$file.json"
STUB
chmod 0755 "$tmp/bin/gh"

fail() {
	echo "ci-queue-verdict-test: $*" >&2
	exit 1
}

run_json() {
	jq -n --arg sha "$sha" --argjson id "$1" --argjson number "$2" --arg path "${3:-.github/workflows/ci.yml}" --arg repo "${4:-o/r}" '{
		id: $id, run_number: $number, path: $path, event: "merge_group", head_sha: $sha,
		repository: {full_name: $repo}, head_repository: {full_name: $repo},
		html_url: "https://github.com/o/r/actions/runs/\($id)"
	}'
}

gate() {
	jq -n --arg c "$2" '{jobs: [{name: "lint", conclusion: "success"}, {name: "CI gate", conclusion: $c}]}' >"$tmp/jobs-$1.json"
}

expect() {
	want=$1
	got=$(FAIL=${2:-} sh "$script_dir/ci-queue-verdict.sh" o/r "$sha" 2>"$tmp/err") || fail "exit $? with FAIL=${2:-}"
	[ "$got" = "$want" ] || fail "printed [$got], want [$want] with FAIL=${2:-}: $(cat "$tmp/err")"
}

{ run_json 1 10; run_json 2 11; } | jq -s '{workflow_runs: .}' >"$tmp/runs.json"
gate 1 success
gate 2 success
expect https://github.com/o/r/actions/runs/2

gate 2 failure
expect ''
grep -q "CI gate of merge queue run 2 is 'failure'" "$tmp/err" || fail "a failed gate was not named"

gate 2 ''
expect ''

jq -n '{jobs: [{name: "lint", conclusion: "success"}]}' >"$tmp/jobs-2.json"
expect ''

gate 2 success
for what in runs jobs-2; do
	expect '' "$what"
done

{ run_json 1 10 .github/workflows/other.yml; run_json 3 12 .github/workflows/ci.yml fork/r; } | jq -s '{workflow_runs: .}' >"$tmp/runs.json"
gate 3 success
expect ''

printf '%s\n' '{"workflow_runs": []}' >"$tmp/runs.json"
expect ''
grep -q 'no merge queue run tested' "$tmp/err" || fail "a missing run was not named"

sh "$script_dir/ci-queue-verdict.sh" o/r ABC >/dev/null 2>&1 && fail "accepted a short SHA"
exit 0
