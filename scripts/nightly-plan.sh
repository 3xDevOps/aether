#!/bin/sh
# Decides what nightly.yml builds. Run in a full clone of main whose HEAD
# is the main commit the run started from. EVENT is schedule or
# workflow_dispatch; RUN_ID is this run's id. It reads the tags, the
# releases and the channels release's pointers, has nightly-plan
# ($NIGHTLY_PLAN, built from main) verify them, and walks main back from
# HEAD to the newest commit whose push CI passes release-ci-check.sh. Prints
# key=value lines for GITHUB_OUTPUT: run (true or false), reason, and when
# run is true tag, commit, build and first-pointer.
set -eu

repo="${GITHUB_REPOSITORY:?nightly-plan: GITHUB_REPOSITORY must name the repository}"
tool="${NIGHTLY_PLAN:?nightly-plan: NIGHTLY_PLAN must name the built nightly-plan command}"
event="${EVENT:?nightly-plan: EVENT is empty}"
run_id="${RUN_ID:?nightly-plan: RUN_ID is empty}"
script_dir=$(dirname -- "$0")
# How far back from main's head a green commit is looked for, and how long
# a scheduled run waits after the newest nightly.
walk="${NIGHTLY_WALK:-30}"
wait_seconds=14400

fail() {
	echo "nightly-plan: $1" >&2
	exit 1
}

skip() {
	echo "run=false"
	echo "reason=$1"
	exit 0
}

case $event in schedule | workflow_dispatch) ;; *) fail "event $event is not schedule or workflow_dispatch" ;; esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir "$tmp/pointers" "$tmp/manifests"

git for-each-ref --format='%(refname:strip=2) %(objecttype) %(objectname) %(*objectname)%0a%(contents)%00' 'refs/tags/v*' >"$tmp/refs"
gh api --paginate "repos/$repo/releases?per_page=100" \
	--jq '.[] | "\(.tag_name) \(.draft) \(.published_at // "-")"' >"$tmp/releases"

release=$(gh api "repos/$repo/releases/tags/channels") ||
	fail "cannot read the channels release, which holds the pointers"
for name in $(printf '%s' "$release" | jq -r '.assets[].name | select(test("^(nightly|beta|stable)\\.json(\\.sig)?$"))'); do
	gh release download channels --repo "$repo" --pattern "$name" --dir "$tmp/pointers"
done

"$tool" manifests -refs "$tmp/refs" -releases "$tmp/releases" -pointers "$tmp/pointers" >"$tmp/needed"
while read -r tag; do
	gh release download "$tag" --repo "$repo" --pattern aether-release.json --pattern aether-release.json.sig --dir "$tmp/manifests/$tag" </dev/null
done <"$tmp/needed"
"$tool" state -refs "$tmp/refs" -releases "$tmp/releases" -pointers "$tmp/pointers" -manifests "$tmp/manifests" >"$tmp/state"
value() {
	sed -n "s/^$1=//p" "$tmp/state"
}

# A scheduled run waits 4 hours after $1 (a time), unless an admin
# dispatched a run since. GitHub keeps one pending run per group and
# cancels the older one, so a build now waiting behind a running build can
# be replaced by this scheduled run.
waited() {
	[ "$event" = schedule ] && [ -n "$1" ] || return 0
	since=$(date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ)
	age=$(($(date +%s) - $(date -d "$since" +%s)))
	[ "$age" -lt "$wait_seconds" ] || return 0
	for actor in $(gh api --paginate "repos/$repo/actions/workflows/nightly.yml/runs?event=workflow_dispatch&created=%3E$since&per_page=100" \
		--jq ".workflow_runs[] | select(.id != $run_id) | .triggering_actor.login" | sort -u); do
		sh "$script_dir/require-admin.sh" "$actor" >/dev/null 2>&1 && return 0
	done
	return 1
}

# A commit main does not hold, or without green push CI, is never built.
green() {
	git merge-base --is-ancestor "$1" HEAD || return 1
	sh "$script_dir/release-ci-check.sh" "$repo" "$1" >/dev/null 2>"$tmp/ci-err"
}

resume=$(value resume-tag)
if [ -n "$resume" ]; then
	commit=$(value resume-commit)
	green "$commit" || fail "$resume was tagged without a published release, but its commit $commit is not a main commit with green push CI: $(cat "$tmp/ci-err" 2>/dev/null)"
	# A failure after tagging that persists is retried every 4 hours, not
	# every 30 minutes, counted from the tag or the newest failed run,
	# whichever is later; build now retries at once.
	tried=$(git for-each-ref --format='%(taggerdate:iso-strict)' "refs/tags/$resume")
	failed=$(gh api "repos/$repo/actions/workflows/nightly.yml/runs?status=failure&per_page=1" --jq '.workflow_runs[0].created_at // empty')
	if [ -n "$failed" ] && [ -n "$tried" ] && [ "$(date -d "$failed" +%s)" -gt "$(date -d "$tried" +%s)" ]; then tried=$failed; fi
	waited "$tried" || skip "$resume was last tried at $tried by a run that failed; a scheduled run retries it 4 hours after that"
	echo "run=true"
	echo "reason=resuming $resume, which a failed run tagged without publishing"
	echo "tag=$resume"
	echo "commit=$commit"
	echo "build=$(value resume-build)"
	echo "first-pointer=$(value first-pointer)"
	exit 0
fi

newest=$(value newest-commit)
commit=
for candidate in $(git rev-list --first-parent --max-count="$walk" HEAD); do
	[ "$candidate" != "$newest" ] || skip "$candidate, the newest green main commit, is already $(value newest-tag)"
	if green "$candidate"; then
		commit=$candidate
		break
	fi
done
[ -n "$commit" ] || skip "none of main's last $walk commits has green push CI"

published=$(value newest-published-at)
waited "$published" || skip "$(value newest-tag) was published at $published; a scheduled nightly waits 4 hours"

echo "run=true"
echo "reason=building $commit"
echo "tag=$(value next-tag)"
echo "commit=$commit"
echo "build=$(value next-build)"
echo "first-pointer=$(value first-pointer)"
