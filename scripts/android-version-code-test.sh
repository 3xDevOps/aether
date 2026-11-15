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

# A release on a channel takes its code from the build and hotfix the
# release workflow passes, whatever its tag, above every code a tag gives.
expect_build() {
	got=$(sh "$script" "$@")
	if [ "$got" != "$EXPECT" ]; then
		echo "android-version-code-test: $* gave $got, wanted $EXPECT" >&2
		exit 1
	fi
}
EXPECT=100000100 expect_build v0.8.7 1
EXPECT=100041200 expect_build v0.9.0-nightly.12 412
EXPECT=100041200 expect_build v0.9.0-beta.2 412 0
EXPECT=100041201 expect_build v0.9.1 412 1
EXPECT=100041299 expect_build v0.9.1 412 99
EXPECT=2100000000 expect_build v0.9.0 20000000
EXPECT=100000300 expect_build v0.9.0-nightly.250 3
if [ "$(code v9.99.99)" -ge "$(sh "$script" v0.9.0 1)" ]; then
	echo "android-version-code-test: build 1 is not above every tag below v10.0.0" >&2
	exit 1
fi

refuse_build() {
	if message=$(sh "$script" "$@" 2>&1); then
		echo "android-version-code-test: $* was accepted as $message" >&2
		exit 1
	fi
	case $message in
	"android-version-code: "*) ;;
	*)
		echo "android-version-code-test: $* failed without saying why: $message" >&2
		exit 1
		;;
	esac
}
# A nightly tag has no tag formula.
refuse v0.9.0-nightly.1
refuse_build v0.9.0-nightly.x 3
refuse_build v0.9.0-pre.1 3
refuse_build v0.4 3
refuse_build v0.9.0 0
refuse_build v0.9.0 ''
refuse_build v0.9.0 012
refuse_build v0.9.0 -1
refuse_build v0.9.0 3 100
refuse_build v0.9.0 3 -1
refuse_build v0.9.0 3 x
# Past Android's limit, and wider than the shell compares.
refuse_build v0.9.0 20000000 1
refuse_build v0.9.0 20000001
refuse_build v0.9.0 99999999999999999999
if sh "$script" v0.9.0 1 0 1 >/dev/null 2>&1; then
	echo "android-version-code-test: four arguments were accepted" >&2
	exit 1
fi

echo "android-version-code-test: ok"
