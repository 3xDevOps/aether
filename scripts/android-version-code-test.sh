#!/bin/sh
# The versionCode a tag maps to, and the ordering Android actually reads.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/android-version-code.sh"

code() {
	sh "$script" "$1"
}

expect() {
	got=$(code "$1")
	if [ "$got" != "$2" ]; then
		echo "android-version-code-test: $1 gave $got, wanted $2" >&2
		exit 1
	fi
}

expect v0.4.0-alpha.1 400101
expect v0.4.0-alpha.6 400106
expect v0.4.0-beta.2 400302
expect v0.4.0-rc.1 400501
expect v0.4.0 400999
expect v0.4.1-alpha.1 401101
expect v1.0.0 10000999
# The v is optional, and the ceilings stay under Android's 2100000000.
expect 1.2.3 10203999
expect v209.99.99 2099999999

# The whole point: every release above the one before it, in the order they
# would be published.
previous=0
for tag in \
	v0.4.0-alpha.1 v0.4.0-alpha.2 v0.4.0-beta.1 v0.4.0-rc.1 v0.4.0 \
	v0.4.1-alpha.1 v0.4.1 v0.5.0-alpha.1 v0.5.0 v1.0.0-rc.1 v1.0.0 v1.0.1; do
	current=$(code "$tag")
	if [ "$current" -le "$previous" ]; then
		echo "android-version-code-test: $tag gave $current, not above $previous" >&2
		exit 1
	fi
	previous=$current
done

refuse() {
	if message=$(sh "$script" "$1" 2>&1); then
		echo "android-version-code-test: $1 was accepted as $message" >&2
		exit 1
	fi
	case $message in
	"android-version-code: "*) ;;
	*)
		echo "android-version-code-test: $1 failed without saying why: $message" >&2
		exit 1
		;;
	esac
}

# What an untagged or dirty tree describes as, which only an unsigned build
# may have.
refuse v0.4.0-alpha.6-4-gd037f5e4
refuse v0.4.0-dirty
refuse dev
refuse ''
# Not a version.
refuse v0.4
refuse v0.4.0.1
refuse v0.4.0-pre.1
refuse v01.2.3
# Past a ceiling, where the number would collide with a later release.
refuse v210.0.0
refuse v0.100.0
refuse v0.4.99.0
refuse v0.4.0-alpha.200
# Wide enough to be outside what the shell compares as an integer, which has
# to read as a refusal rather than a shell error.
refuse v99999999999999999999.0.0
refuse v0.4.0-alpha.99999999999999999999

if sh "$script" v0.4.0 v0.4.1 >/dev/null 2>&1; then
	echo "android-version-code-test: two arguments were accepted" >&2
	exit 1
fi

echo "android-version-code-test: ok"
