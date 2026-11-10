#!/bin/sh
# Whether a pull request's changed paths can affect the web or go jobs, or
# whether a push to main changed what a CI cache key hashes.
#
# usage: ci-classify-areas.sh web|go|caches
# stdin: newline-separated repo paths. Prints true or false.
# web covers the dashboard checks: the dashboard source and everything that
# builds or serves it. go covers the Go unit and lint jobs: everything
# except the dashboard source (web/ other than web/embed.go) and docs-only
# changes. caches covers every file a cache key in .github/ hashes, and
# .github/ itself, which names the keys. false only when at least one path is present and every path is
# in the unrelated set for the mode. Empty input prints true. Blank lines
# are ignored. A path in neither set prints true.
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: ci-classify-areas.sh web|go|caches" >&2
	exit 2
fi
mode=$1

# The end-to-end suite builds and runs aether gui and aether-server, so the
# gateway packages and the CLI entrypoint stay on the web side.
web() {
	case $1 in
	web/* | cmd/aether/* | internal/localgw/* | internal/webgate/* | \
		go.mod | go.sum | Makefile | scripts/ci-classify-* | .github/actions/* | \
		.github/workflows/ci.yml)
		return 0
		;;
	cmd/* | internal/* | docs/* | images/* | packaging/* | android/* | desktop/* | scripts/*) return 1 ;;
	.github/workflows/* | .gitignore | .golangci.yml) return 1 ;;
	*/*) return 0 ;;
	*.md)
		[ -z "${1%.md}" ] && return 0
		return 1
		;;
	*) return 0 ;;
	esac
}

go() {
	case $1 in
	web/embed.go | go.mod | go.sum | Makefile | scripts/ci-classify-* | \
		.github/actions/* | .github/workflows/ci.yml)
		return 0
		;;
	web/* | docs/* | android/*) return 1 ;;
	*/*) return 0 ;;
	*.md)
		[ -z "${1%.md}" ] && return 0
		return 1
		;;
	*) return 0 ;;
	esac
}

caches() {
	case $1 in
	go.mod | go.sum | .golangci.yml | .github/* | images/* | web/public/fonts/* | \
		web/package.json | web/bun.lock | web/.bun-version | \
		desktop/package.json | desktop/package-lock.json | \
		android/gradle/* | android/gradle.properties | android/*.gradle.kts)
		return 0
		;;
	esac
	return 1
}

case $mode in
web | go | caches) ;;
*)
	echo "usage: ci-classify-areas.sh web|go|caches" >&2
	exit 2
	;;
esac

seen=false
while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	seen=true
	if "$mode" "$path"; then
		printf '%s\n' true
		exit 0
	fi
done

if [ "$seen" = true ]; then
	printf '%s\n' false
else
	printf '%s\n' true
fi
