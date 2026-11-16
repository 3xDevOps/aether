#!/bin/sh
set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

for case in v0.9.0:main v0.10.0:main v1.0.0:main v0.9.1-rc.1:main v0.8.7-beta.15:main \
	v0.4.0-alpha.6:main v0.9.1:hotfix v0.9.10:hotfix v0.8.8:hotfix v0.9.1+meta:hotfix; do
	tag=${case%:*}
	got=$(sh "$script_dir/release-tag-path.sh" "$tag") || { echo "FAIL: $tag was refused" >&2; exit 1; }
	[ "$got" = "${case##*:}" ] || { echo "FAIL: $tag took $got, want ${case##*:}" >&2; exit 1; }
done
for tag in v0.9.0-nightly.3 v1.0.0-nightly.1; do
	if sh "$script_dir/release-tag-path.sh" "$tag" >/dev/null 2>&1; then
		echo "FAIL: $tag was accepted" >&2
		exit 1
	fi
done
echo 'release tag path tests passed'
