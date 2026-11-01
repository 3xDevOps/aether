#!/bin/sh
# ci-classify-areas.sh: each area is false only for paths it cannot see.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/ci-classify-areas.sh"

expect() {
	mode=$1
	want=$2
	shift 2
	got=$(printf '%s\n' "$@" | sh "$script" "$mode")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-areas-test: $mode [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

expect web true web/src/app.tsx
expect web true web/package.json
expect web true internal/store/a.go web/src/app.tsx
expect web true cmd/aether/main.go
expect web true internal/localgw/gateway.go
expect web false internal/store/store.go
expect web false cmd/aether-server/main.go docs/testing.md
expect web false android/app/build.gradle.kts desktop/main.js
expect web false docs/testing.md README.md
expect web true Makefile
expect web true go.mod
expect web true .github/workflows/ci.yml
expect web true scripts/ci-classify-areas.sh
expect web false scripts/install.sh
expect web true unknown/dir/file

expect go false web/src/app.tsx
expect go false web/package.json web/bun.lock
expect go false web/e2e/foo.spec.ts docs/testing.md
expect go false docs/testing.md README.md
expect go true web/embed.go
expect go true web/src/app.tsx internal/store/store.go
expect go true internal/store/store.go
expect go true Makefile
expect go true go.mod
expect go true .github/workflows/ci.yml
expect go true .github/actions/go-cache/action.yml
expect go true scripts/install.sh
expect go false android/README.md
expect go true unknown/dir/file

for mode in web go; do
	got=$(sh "$script" "$mode" </dev/null)
	if [ "$got" != true ]; then
		echo "ci-classify-areas-test: $mode empty stdin gave $got, wanted true" >&2
		exit 1
	fi
	got=$(printf '\n' | sh "$script" "$mode")
	if [ "$got" != true ]; then
		echo "ci-classify-areas-test: $mode blank line gave $got, wanted true" >&2
		exit 1
	fi
done
