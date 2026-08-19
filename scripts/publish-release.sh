#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: publish-release.sh <tag> <dist-dir>" >&2
	exit 2
fi

tag="$1"
dist="$2"

if [ ! -d "$dist" ]; then
	echo "publish-release: distribution directory not found: $dist" >&2
	exit 1
fi

set -- "$dist"/*
if [ ! -e "$1" ]; then
	echo "publish-release: no release assets in $dist" >&2
	exit 1
fi

if gh release view "$tag" >/dev/null 2>&1; then
	exec gh release upload "$tag" --clobber "$@"
fi

exec gh release create "$tag" --title "$tag" --generate-notes "$@"
