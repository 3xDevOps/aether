#!/bin/sh
# ci-reap-queue-runs.sh: keeps runs of live groups, cancels runs whose
# branch is gone or moved, and cancels nothing when a lookup fails. Only the
# gh boundary is replaced.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/bin"
export FIXTURES="$tmp" PATH="$tmp/bin:$PATH"

cat >"$tmp/bin/gh" <<'STUB'
#!/bin/sh
set -eu
[ "$1" = api ] || exit 90
shift
case "$*" in
-X\ POST\ repos/o/r/actions/runs/*/cancel)
	id=${3#repos/o/r/actions/runs/}
	echo "${id%/cancel}" >>"$FIXTURES/cancelled"
	[ "${FAIL:-}" != cancel ] || exit 1
	;;
*"actions/runs?event=merge_group&status=queued"*)
	[ "${FAIL:-}" != runs ] || exit 1
	cat "$FIXTURES/runs"
	;;
*"actions/runs?event=merge_group&status="*) ;;
*"git/matching-refs/heads/gh-readonly-queue/"*)
	[ "${FAIL:-}" != refs ] || exit 1
	cat "$FIXTURES/refs"
	;;
*) echo "unexpected request: $*" >&2; exit 91 ;;
esac
STUB
chmod 0755 "$tmp/bin/gh"

fail() {
	echo "ci-reap-queue-runs-test: $*" >&2
	exit 1
}

run() {
	: >"$tmp/cancelled"
	FAIL=${1:-} sh "$script_dir/ci-reap-queue-runs.sh" o/r gh-readonly-queue/main/pr-1-aaa >"$tmp/out" || fail "exit $? with FAIL=${1:-}"
}

printf '%s\n' \
	'1 gh-readonly-queue/main/pr-1-aaa sha1' \
	'2 gh-readonly-queue/main/pr-2-bbb sha2' \
	'3 gh-readonly-queue/main/pr-3-ccc sha3' >"$tmp/runs"
printf '%s\n' \
	'gh-readonly-queue/main/pr-1-aaa sha1' \
	'gh-readonly-queue/main/pr-3-ccc sha3-rebuilt' >"$tmp/refs"

run
[ "$(tr '\n' ' ' <"$tmp/cancelled")" = "2 3 " ] || fail "cancelled [$(tr '\n' ' ' <"$tmp/cancelled")], want [2 3 ]"

for what in runs refs; do
	run "$what"
	[ ! -s "$tmp/cancelled" ] || fail "a failed $what lookup cancelled [$(tr '\n' ' ' <"$tmp/cancelled")]"
	grep -q '::warning::' "$tmp/out" || fail "a failed $what lookup gave no warning"
done

run cancel
grep -q '::warning::could not cancel run 2' "$tmp/out" || fail "a failed cancel gave no warning"

for refs in '' 'gh-readonly-queue/main/pr-3-ccc sha3'; do
	printf '%s' "$refs" >"$tmp/refs"
	run
	[ ! -s "$tmp/cancelled" ] || fail "a branch list without this run's branch cancelled [$(tr '\n' ' ' <"$tmp/cancelled")]"
done
