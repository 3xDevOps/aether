#!/bin/sh
# ci-classify-edge.sh: paths that can affect the aether-edge image print
# true, unrelated ones false, and every repository package the edge
# imports is classified true.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(dirname -- "$script_dir")
script="$script_dir/ci-classify-edge.sh"

expect() {
	want=$1
	shift
	got=$(printf '%s\n' "$@" | sh "$script")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-edge-test: [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

expect true cmd/aether-edge/main.go
expect true cmd/aether-edge/admin_test.go
expect true internal/edge/service/web.go
expect true internal/edge/store/testdata/schema.sql
expect true internal/edge/edgetest/malicious_test.go
expect true internal/edge/agent/agent.go
expect true internal/domain/member.go
expect true internal/version/version.go
expect true go.mod
expect true go.sum
expect true Makefile
expect true .dockerignore
expect true images/edge/Dockerfile
expect true scripts/edge-image-smoke.sh
expect true scripts/ci-classify-edge.sh
expect true scripts/ci-classify-edge-test.sh
expect true .github/workflows/ci.yml
expect true .github/workflows/release.yml
expect true packaging/edge/aether-edge.env.example
expect true images/other/Dockerfile
expect true scripts/new-tool.sh
expect true tools/gen/main.go
expect true main.go
expect true LICENSE
expect true .gitattributes
expect true .md
expect true docs/edge.md internal/edge/relay/listener.go
expect true web/src/app/page.tsx go.sum

expect false docs/edge.md
expect false README.md
expect false docs/edge.md README.md
expect false web/src/app/page.tsx
expect false web/embed.go
expect false android/app/build.gradle.kts
expect false desktop/embed.go
expect false images/browser/Dockerfile
expect false images/standard/Dockerfile
expect false images/smoke/Dockerfile
expect false internal/server/server.go
expect false internal/sshd/edge_test.go
expect false internal/edgeish/x.go
expect false cmd/aether/main.go
expect false cmd/aether-edge-other/main.go
expect false scripts/install.sh
expect false scripts/ci-classify-changes.sh
expect false .github/workflows/windows-install.yml
expect false .github/actions/go-cache/action.yml
expect false packaging/windows/aether.manifest
expect false docs/testing.md internal/server/server.go web/embed.go

got=$(sh "$script" </dev/null)
if [ "$got" != true ]; then
	echo "ci-classify-edge-test: empty stdin gave $got, wanted true" >&2
	exit 1
fi

got=$(printf '\n' | sh "$script")
if [ "$got" != true ]; then
	echo "ci-classify-edge-test: blank line gave $got, wanted true" >&2
	exit 1
fi

# The image targets these two platforms; build constraints can give each
# its own imports.
for arch in amd64 arm64; do
	dirs=$(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go list -deps \
		-f '{{with .Module}}{{if .Main}}{{$.Dir}}{{end}}{{end}}' ./cmd/aether-edge)
	printf '%s\n' "$dirs" | while IFS= read -r dir; do
		rel=${dir#"$repo"/}
		got=$(printf '%s\n' "$rel/x.go" | sh "$script")
		if [ "$got" != true ]; then
			echo "ci-classify-edge-test: aether-edge imports $rel on linux/$arch, which ci-classify-edge.sh classifies $got; add $rel/* to its first case pattern" >&2
			exit 1
		fi
	done
done
