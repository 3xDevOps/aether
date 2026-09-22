#!/bin/sh
# Whether a pull request's changed paths need the code jobs.
#
# stdin: newline-separated repo paths. Prints true or false.
# false only when at least one path is present and every path is under
# docs/ or is a root-level markdown file ([^/]+\.md). Empty input prints
# true. Blank lines are ignored. A markdown file anywhere else, including
# a dashboard end-to-end fixture, prints true.
set -eu

if [ "$#" -ne 0 ]; then
	echo "usage: ci-classify-changes.sh" >&2
	exit 2
fi

seen=false
while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	seen=true
	case $path in
	docs/*)
		;;
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
