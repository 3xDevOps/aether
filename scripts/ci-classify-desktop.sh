#!/bin/sh
# Whether a pull request's changed paths can affect the packaged Windows
# desktop app.
#
# stdin: newline-separated repo paths. Prints true or false.
# true when any path is in the set below or the input is empty. Blank
# lines are ignored. The package build reads only desktop/ and the
# composite action that runs it.
set -eu

if [ "$#" -ne 0 ]; then
	echo "usage: ci-classify-desktop.sh" >&2
	exit 2
fi

seen=false
while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	seen=true
	case $path in
	desktop/* | .github/actions/desktop-windows-zip/* | \
		scripts/ci-classify-desktop* | .github/workflows/ci.yml)
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
