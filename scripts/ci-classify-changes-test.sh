#!/bin/sh
# ci-classify-changes.sh: docs-only lists print false, everything else true.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/ci-classify-changes.sh"

expect() {
	want=$1
	shift
	got=$(printf '%s\n' "$@" | sh "$script")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-changes-test: [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

expect false docs/testing.md
expect false docs/testing.md README.md
expect true web/e2e/testdata/claude-profile/skills/deploy/notes.md
expect true web/e2e/testdata/claude-profile/skills/deploy/notes.md docs/testing.md
expect true .github/workflows/ci.yml
expect true Makefile
expect true android/README.md

got=$(sh "$script" </dev/null)
if [ "$got" != true ]; then
	echo "ci-classify-changes-test: empty stdin gave $got, wanted true" >&2
	exit 1
fi

got=$(printf '\n' | sh "$script")
if [ "$got" != true ]; then
	echo "ci-classify-changes-test: blank line gave $got, wanted true" >&2
	exit 1
fi
