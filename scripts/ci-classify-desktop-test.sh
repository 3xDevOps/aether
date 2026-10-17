#!/bin/sh
# ci-classify-desktop.sh: paths that can affect the packaged Windows
# desktop app print true, everything else false.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/ci-classify-desktop.sh"

expect() {
	want=$1
	shift
	got=$(printf '%s\n' "$@" | sh "$script")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-desktop-test: [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

expect true desktop/main.js
expect true desktop/package-lock.json
expect true .github/actions/desktop-windows-zip/action.yml
expect true scripts/ci-classify-desktop.sh
expect true scripts/ci-classify-desktop-test.sh
expect true .github/workflows/ci.yml
expect true docs/install.md desktop/package.json
expect false docs/install.md
expect false cmd/aether/gui_build.go
expect false web/src/main.tsx
expect false .github/actions/go-cache/action.yml
expect false .github/workflows/release.yml
expect false scripts/install-smoke.ps1
expect false desktopish/x.js

got=$(sh "$script" </dev/null)
if [ "$got" != true ]; then
	echo "ci-classify-desktop-test: empty stdin gave $got, wanted true" >&2
	exit 1
fi
got=$(printf '\n' | sh "$script")
if [ "$got" != true ]; then
	echo "ci-classify-desktop-test: blank line gave $got, wanted true" >&2
	exit 1
fi
