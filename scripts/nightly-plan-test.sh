#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
bin="$tmp/bin"
scripts="$tmp/scripts"
mkdir -p "$bin" "$scripts"
cp "$script_dir/nightly-plan.sh" "$script_dir/require-admin.sh" "$scripts/"

# release-ci-check.sh passes for the commits listed in $STATE/green.
cat >"$scripts/release-ci-check.sh" <<'EOF'
#!/bin/sh
grep -qx "$2" "$STATE/green" || { echo "release-ci-check: not green" >&2; exit 1; }
echo "https://github.com/3xDevOps/Aether/actions/runs/1"
EOF

# gh: $STATE/dispatchers are the triggering actors of workflow_dispatch
# runs after the newest nightly, $STATE/admins the admins.
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >>"$STATE/log"
case "$*" in
"api --paginate repos/3xDevOps/Aether/releases?per_page=100 "*) echo "v0.8.7 false 2026-11-01T00:00:00Z" ;;
"api repos/3xDevOps/Aether/releases/tags/channels") echo '{"assets":[{"name":"stable.json"},{"name":"stable.json.sig"},{"name":"other.txt"}]}' ;;
"release download "*)
	while [ "$#" -gt 1 ]; do
		[ "$1" = --dir ] && { mkdir -p "$2"; : >"$2/downloaded"; }
		shift
	done
	;;
"api --paginate repos/3xDevOps/Aether/actions/workflows/nightly.yml/runs?"*)
	for actor in $(cat "$STATE/dispatchers"); do echo "$actor"; done
	;;
"api repos/3xDevOps/Aether/actions/workflows/nightly.yml/runs?status=failure&per_page=1 "*)
	cat "$STATE/failed" 2>/dev/null || true
	;;
"api repos/3xDevOps/Aether/collaborators/"*)
	user=${2#repos/3xDevOps/Aether/collaborators/}
	user=${user%/permission}
	if grep -qx "$user" "$STATE/admins"; then echo admin; else echo write; fi
	;;
*) echo "gh stub: unexpected $*" >&2; exit 2 ;;
esac
EOF

# The tool prints $STATE/state for state and nothing for manifests.
cat >"$bin/nightly-plan" <<'EOF'
#!/bin/sh
echo "nightly-plan $1" >>"$STATE/log"
[ "$1" = state ] && cat "$STATE/state"
exit 0
EOF
chmod +x "$bin/gh" "$bin/nightly-plan" "$scripts/release-ci-check.sh"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

# main: c1 (the newest nightly) <- c2 <- c3 <- c4 (head).
repo="$tmp/repo"
git init -q "$repo"
for i in 1 2 3 4; do
	git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m "c$i"
done
c() { git -C "$repo" rev-parse "HEAD~$((4 - $1))"; }
git -C "$repo" -c user.name=t -c user.email=t@example.com tag -a v0.9.0-nightly.1 -m "$(printf 'v0.9.0-nightly.1\n\nbuild: 2')" "$(c 1)"
side=$(git -C "$repo" -c user.name=t -c user.email=t@example.com commit-tree -m side "$(git -C "$repo" rev-parse 'HEAD^{tree}')")

recent=$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)
old=$(date -u -d '-5 hours' +%Y-%m-%dT%H:%M:%SZ)

reset() {
	export STATE="$tmp/state"
	rm -rf "$STATE"
	mkdir -p "$STATE"
	: >"$STATE/log"
	: >"$STATE/dispatchers"
	echo alice >"$STATE/admins"
	printf '%s\n' "$(c 1)" >"$STATE/green"
	cat >"$STATE/state" <<EOF
newest-tag=v0.9.0-nightly.1
newest-commit=$(c 1)
newest-published-at=$old
resume-tag=
resume-commit=
resume-build=
next-tag=v0.9.0-nightly.2
next-build=3
first-pointer=false
EOF
}

set_state() {
	sed -i "s|^$1=.*|$1=$2|" "$STATE/state"
}

plan() {
	(cd "$repo" && PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether NIGHTLY_PLAN=nightly-plan \
		EVENT="$1" RUN_ID=99 sh "$scripts/nightly-plan.sh") >"$tmp/out" 2>"$tmp/err"
}

want() {
	grep -qxF "$1" "$tmp/out" || fail "$2: want $1 in: $(cat "$tmp/out") $(cat "$tmp/err")"
}

# The head is not green; the walk takes the newest green commit below it.
reset
printf '%s\n' "$(c 1)" "$(c 2)" "$(c 3)" >"$STATE/green"
plan schedule || fail "walk: $(cat "$tmp/err")"
want run=true walk
want "commit=$(c 3)" walk
want tag=v0.9.0-nightly.2 walk
want build=3 walk
want first-pointer=false walk
grep -q '^nightly-plan manifests' "$STATE/log" || fail "the manifests the state needs were not asked for"
grep -q 'release download channels .*--pattern stable.json ' "$STATE/log" || fail "stable.json was not downloaded"
if grep -q 'other.txt' "$STATE/log"; then fail "a non-pointer asset was downloaded"; fi

# Nothing above the newest nightly is green: stop at it, never below it.
reset
plan schedule || fail "no new green: $(cat "$tmp/err")"
want run=false "no new green"
grep -q 'is already v0.9.0-nightly.1' "$tmp/out" || fail "reason: $(cat "$tmp/out")"

# A scheduled run waits 4 hours after the newest nightly.
reset
printf '%s\n' "$(c 4)" >"$STATE/green"
set_state newest-published-at "$recent"
plan schedule || fail "wait: $(cat "$tmp/err")"
want run=false wait
grep -q 'a scheduled nightly waits 4 hours' "$tmp/out" || fail "wait reason: $(cat "$tmp/out")"

# A non-admin's dispatch does not skip the wait; an admin's does.
echo mallory >"$STATE/dispatchers"
plan schedule || fail "non-admin dispatch: $(cat "$tmp/err")"
want run=false "non-admin dispatch"
printf 'mallory\nalice\n' >"$STATE/dispatchers"
plan schedule || fail "admin dispatch: $(cat "$tmp/err")"
want run=true "admin dispatch"
want "commit=$(c 4)" "admin dispatch"

# Build now skips the wait without asking.
: >"$STATE/dispatchers"
: >"$STATE/log"
plan workflow_dispatch || fail "build now: $(cat "$tmp/err")"
want run=true "build now"
if grep -q 'actions/workflows' "$STATE/log"; then fail "build now looked up dispatches"; fi

# The first nightly: no newest, first-pointer passed on.
reset
printf '%s\n' "$(c 4)" >"$STATE/green"
set_state newest-tag ''
set_state newest-commit ''
set_state newest-published-at ''
set_state first-pointer true
plan schedule || fail "first: $(cat "$tmp/err")"
want first-pointer=true first
want "commit=$(c 4)" first

# A tag a failed run left is resumed, with its numbers, once its commit is
# a green main commit; another commit is refused.
reset
printf '%s\n' "$(c 2)" >"$STATE/green"
set_state resume-tag v0.9.0-nightly.2
set_state resume-commit "$(c 2)"
set_state resume-build 3
plan schedule || fail "resume: $(cat "$tmp/err")"
want tag=v0.9.0-nightly.2 resume
want "commit=$(c 2)" resume
want build=3 resume
set_state resume-commit "$(c 3)"
if plan schedule; then fail "a resume of a commit without green CI was accepted"; fi
grep -q 'not a main commit with green push CI' "$tmp/err" || fail "resume refusal: $(cat "$tmp/err")"
echo "$side" >>"$STATE/green"
set_state resume-commit "$side"
if plan schedule; then fail "a resume of a commit off main was accepted"; fi

# A resumed tag whose run failed recently waits 4 hours on a schedule, by
# its tagger date; build now resumes it at once.
reset
printf '%s\n' "$(c 2)" >"$STATE/green"
set_state resume-tag v0.9.0-nightly.2
set_state resume-commit "$(c 2)"
set_state resume-build 3
GIT_COMMITTER_DATE=$recent git -C "$repo" -c user.name=t -c user.email=t@example.com tag -a v0.9.0-nightly.2 -m "$(printf 'v0.9.0-nightly.2\n\nbuild: 3')" "$(c 2)"
plan schedule || fail "recent resume: $(cat "$tmp/err")"
want run=false "recent resume"
grep -q 'retries it 4 hours after that' "$tmp/out" || fail "recent resume reason: $(cat "$tmp/out")"
grep -q "created=%3E$(date -u -d "$recent" +%Y-%m-%dT%H:%M:%SZ)&" "$STATE/log" || fail "dispatches since the tag were not looked up: $(cat "$STATE/log")"
plan workflow_dispatch || fail "build now resume: $(cat "$tmp/err")"
want tag=v0.9.0-nightly.2 "build now resume"
git -C "$repo" tag -d v0.9.0-nightly.2 >/dev/null
GIT_COMMITTER_DATE=$old git -C "$repo" -c user.name=t -c user.email=t@example.com tag -a v0.9.0-nightly.2 -m "$(printf 'v0.9.0-nightly.2\n\nbuild: 3')" "$(c 2)"
plan schedule || fail "old resume: $(cat "$tmp/err")"
want tag=v0.9.0-nightly.2 "old resume"
# An old tag whose last run failed recently still waits 4 hours from that run.
date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ >"$STATE/failed"
plan schedule || fail "recently failed resume: $(cat "$tmp/err")"
want run=false "recently failed resume"
grep -q 'was last tried at' "$tmp/out" || fail "recently failed reason: $(cat "$tmp/out")"
rm "$STATE/failed"
git -C "$repo" tag -d v0.9.0-nightly.2 >/dev/null

# No green commit in the walk.
reset
: >"$STATE/green"
set_state newest-commit ''
plan schedule || fail "none green: $(cat "$tmp/err")"
want run=false "none green"

if (cd "$repo" && PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether NIGHTLY_PLAN=nightly-plan EVENT=push RUN_ID=1 sh "$scripts/nightly-plan.sh") >/dev/null 2>&1; then
	fail "a push event was accepted"
fi

echo "nightly-plan tests passed"
