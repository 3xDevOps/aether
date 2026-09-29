#!/bin/sh
# Whether a pull request's changed paths can affect the aether-edge image.
#
# stdin: newline-separated repo paths. Prints true or false.
# false only when at least one path is present and every path is in the
# unrelated set below. Empty input prints true. Blank lines are ignored.
# A path in neither set prints true.
set -eu

if [ "$#" -ne 0 ]; then
	echo "usage: ci-classify-edge.sh" >&2
	exit 2
fi

seen=false
while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	seen=true
	case $path in
	# Every repository package cmd/aether-edge imports on linux/amd64 and
	# linux/arm64, with its tests and testdata. ci-classify-edge-test.sh
	# fails when an imported package is missing here. internal/edge/ also
	# holds edgetest, the end-to-end suite, and the agent and client it
	# runs against the edge.
	cmd/aether-edge/* | internal/domain/* | internal/edge/* | internal/version/*)
		printf '%s\n' true
		exit 0
		;;
	go.mod | go.sum | Makefile | .dockerignore | images/edge/* | \
		scripts/ci-classify-edge* | scripts/edge-* | \
		.github/workflows/ci.yml | .github/workflows/edge-image.yml | \
		.github/workflows/release.yml)
		printf '%s\n' true
		exit 0
		;;
	# Packages the edge does not import. It can only start importing one
	# through a change to a package above.
	cmd/* | internal/*) ;;
	docs/* | web/* | android/* | desktop/* | packaging/windows/*) ;;
	images/browser/* | images/smoke/* | images/standard/*) ;;
	scripts/android-* | scripts/ci-classify-changes* | scripts/deploy* | \
		scripts/install* | scripts/make-icons.py | scripts/public-audit.sh | \
		scripts/publish-release*) ;;
	.github/actions/* | .github/workflows/windows-install.yml | .gitignore | .golangci.yml) ;;
	*/*)
		printf '%s\n' true
		exit 0
		;;
	*.md)
		# [^/]+\.md: one or more characters before the suffix. ".md" is not it.
		if [ -z "${path%.md}" ]; then
			printf '%s\n' true
			exit 0
		fi
		;;
	*)
		printf '%s\n' true
		exit 0
		;;
	esac
done

if [ "$seen" = true ]; then
	printf '%s\n' false
else
	printf '%s\n' true
fi
