#!/bin/sh
# Whether a pull request's changed paths can affect the platform jobs.
#
# usage: ci-classify-platform.sh client|installer|release
# stdin: newline-separated repo paths. Prints true or false.
# client covers what the cross-compiled aether client, the Android app and
# the Linux desktop build are made from. installer covers what the Windows
# install and update lanes exercise. release covers what the release
# cross-compile adds to the native builds: the Makefile recipe, packaging
# and the Windows resources. false only when at least one path is
# present and every path is in the unrelated set for the mode. Empty input
# prints true. Blank lines are ignored. A path in neither set prints true.
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: ci-classify-platform.sh client|installer|release" >&2
	exit 2
fi
mode=$1

# Repository packages cmd/aether imports on linux, android, darwin and
# windows, with their tests and testdata. ci-classify-platform-test.sh fails
# when an imported package is missing here.
client() {
	case $1 in
	cmd/aether/* | internal/agentstatus/* | internal/attribution/* | \
		internal/cli/* | internal/coordhooks/* | internal/coordtransport/* | \
		internal/domain/* | internal/edge/client/* | internal/edge/proto/* | \
		internal/events/* | internal/harness/* | internal/jsonc/* | \
		internal/localgw/* | internal/localops/* | internal/macinstall/* | \
		internal/overlay/* | internal/profile/* | internal/protocol/* | \
		internal/reachability/* | internal/rootfs/* | internal/secretscan/* | \
		internal/selfupdate/* | internal/shellquote/* | internal/store/* | \
		internal/syncd/* | internal/version/* | internal/webgate/*)
		return 0
		;;
	go.mod | go.sum | Makefile | desktop/* | web/* | android/* | packaging/windows/* | \
		scripts/android-* | scripts/install* | scripts/*.ps1 | scripts/make-icons.py | \
		scripts/ci-classify-platform* | .github/actions/* | \
		.github/workflows/ci.yml | .github/workflows/windows-install.yml)
		return 0
		;;
	cmd/* | internal/* | docs/* | images/* | packaging/*) return 1 ;;
	scripts/*) return 1 ;;
	.github/workflows/* | .gitignore | .golangci.yml) return 1 ;;
	*/*) return 0 ;;
	*.md)
		[ -z "${1%.md}" ] && return 0
		return 1
		;;
	*) return 0 ;;
	esac
}

installer() {
	case $1 in
	cmd/aether/* | internal/cli/* | internal/selfupdate/* | internal/localops/* | \
		internal/macinstall/* | internal/version/* | desktop/* | packaging/windows/* | \
		go.mod | go.sum | scripts/install* | scripts/*.ps1 | scripts/ci-classify-platform* | \
		.github/actions/* | .github/workflows/ci.yml | .github/workflows/windows-install.yml)
		return 0
		;;
	cmd/* | internal/* | docs/* | images/* | packaging/* | web/* | android/*) return 1 ;;
	scripts/*) return 1 ;;
	.github/workflows/* | .gitignore | .golangci.yml | Makefile) return 1 ;;
	*/*) return 0 ;;
	*.md)
		[ -z "${1%.md}" ] && return 0
		return 1
		;;
	*) return 0 ;;
	esac
}

release() {
	case $1 in
	Makefile | go.mod | go.sum | packaging/* | desktop/build/* | cmd/aether/* | \
		scripts/make-icons.py | scripts/ci-classify-platform* | .github/actions/* | \
		.github/workflows/ci.yml | .github/workflows/release.yml)
		return 0
		;;
	cmd/* | internal/* | docs/* | images/* | web/* | android/* | desktop/* | scripts/*) return 1 ;;
	.github/workflows/* | .gitignore | .golangci.yml) return 1 ;;
	*/*) return 0 ;;
	*.md)
		[ -z "${1%.md}" ] && return 0
		return 1
		;;
	*) return 0 ;;
	esac
}

case $mode in
client | installer | release) ;;
*)
	echo "usage: ci-classify-platform.sh client|installer|release" >&2
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
