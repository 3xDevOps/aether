#!/bin/sh
# ci-classify-platform.sh: paths that can affect the platform jobs print
# true, unrelated ones false, and every repository package the client
# imports is classified true.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(dirname -- "$script_dir")
script="$script_dir/ci-classify-platform.sh"

expect() {
	mode=$1
	want=$2
	shift 2
	got=$(printf '%s\n' "$@" | sh "$script" "$mode")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-platform-test: $mode [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

for mode in client installer release; do
	expect $mode true go.mod
	expect $mode true go.sum
	expect $mode true cmd/aether/main.go
	expect $mode true packaging/windows/aether.manifest
	expect $mode true scripts/ci-classify-platform.sh
	expect $mode true .github/actions/desktop-windows-zip/action.yml
	expect $mode true .github/workflows/ci.yml
	expect $mode true docs/install.md cmd/aether/main.go
	expect $mode true tools/gen/main.go
	expect $mode true LICENSE
	expect $mode true .md

	expect $mode false docs/testing.md
	expect $mode false README.md
	expect $mode false internal/server/server.go
	expect $mode false internal/scheduler/run.go
	expect $mode false cmd/aether-server/main.go
	expect $mode false images/browser/Dockerfile
	expect $mode false scripts/deploy.sh
	expect $mode false docs/testing.md internal/server/server.go

	got=$(sh "$script" $mode </dev/null)
	if [ "$got" != true ]; then
		echo "ci-classify-platform-test: $mode empty stdin gave $got, wanted true" >&2
		exit 1
	fi
done

expect client true web/src/app/page.tsx
expect client true android/app/build.gradle.kts
expect client true Makefile
expect client true scripts/android-version-code.sh
expect client true .github/workflows/windows-install.yml
expect installer true .github/workflows/windows-install.yml
expect release false .github/workflows/windows-install.yml
expect client true desktop/main.js
expect client true scripts/install.ps1
expect client true internal/selfupdate/manifest.go
expect client true internal/store/store.go
expect client true internal/harness/harness.go
expect client false internal/runtime/run.go
expect client false scripts/edge-update.sh

expect release true .github/workflows/release.yml
expect client false .github/workflows/release.yml
expect installer false .github/workflows/release.yml
expect release true Makefile
expect release true packaging/windows/versioninfo.json
expect release true cmd/aether/main.go
expect release false internal/cli/root.go
expect release false internal/store/store.go
expect release false web/src/app/page.tsx
expect release false android/app/build.gradle.kts
expect release false desktop/main.js
expect release false scripts/install.ps1

expect installer true desktop/main.js
expect installer true scripts/update-smoke.ps1
expect installer true internal/cli/root.go
expect installer false web/src/app/page.tsx
expect installer false android/app/build.gradle.kts
expect installer false Makefile
expect installer false scripts/android-version-code.sh
expect installer false internal/store/store.go
expect installer false internal/harness/harness.go

if sh "$script" 2>/dev/null </dev/null; then
	echo "ci-classify-platform-test: no mode should fail" >&2
	exit 1
fi

# The client builds for these targets; build constraints can give each its
# own imports. docs holds only the embedded markdown, which cannot break a
# build.
for target in linux/amd64 linux/arm64 android/arm64 darwin/arm64 windows/amd64 windows/arm64; do
	dirs=$(cd "$repo" && CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go list -deps \
		-f '{{with .Module}}{{if .Main}}{{$.Dir}}{{end}}{{end}}' ./cmd/aether)
	printf '%s\n' "$dirs" | while IFS= read -r dir; do
		[ -n "$dir" ] || continue
		rel=${dir#"$repo"/}
		[ "$rel" != docs ] || continue
		got=$(printf '%s\n' "$rel/x.go" | sh "$script" client)
		if [ "$got" != true ]; then
			echo "ci-classify-platform-test: aether imports $rel on $target, which ci-classify-platform.sh classifies $got; add $rel/* to client()" >&2
			exit 1
		fi
	done
done
