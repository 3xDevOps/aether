#!/bin/sh
# Check that the release APK and app bundle were signed by the release key.
#
#     sh scripts/android-verify-signature.sh dist/aether-android.apk dist/aether-android.aab
#
# `apksigner verify` and `jarsigner -verify` prove a valid signature exists;
# neither says whose. An artifact signed by anything else - a stale build left
# in dist/, a debug key, a keystore that is not the one this build was handed -
# verifies, ships under the release name, and can never install over an
# existing install, which is worse than no APK at all.
#
# So the expected fingerprint is read from the keystore the build was given,
# and both artifacts are compared against it. Runs inside the pinned SDK
# container (Makefile), which is where keytool, jarsigner and apksigner are.
# ANDROID_KEYSTORE_FILE, ANDROID_KEYSTORE_PASSWORD, ANDROID_KEY_ALIAS and
# APKSIGNER come from the environment.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: android-verify-signature.sh <apk> <aab>" >&2
	exit 2
fi
apk=$1
aab=$2

fail() {
	echo "android-verify-signature: $1" >&2
	exit 1
}

# keytool prints "SHA256: AA:BB:.."; apksigner prints 64 lowercase hex digits.
# Both sides are normalised to the latter before they are compared.
normalise() {
	tr -d ':' | tr 'A-F' 'a-f'
}

expected=$(
	keytool -list -v -keystore "$ANDROID_KEYSTORE_FILE" \
		-storepass "$ANDROID_KEYSTORE_PASSWORD" -alias "$ANDROID_KEY_ALIAS" |
		sed -n 's/^[[:space:]]*SHA256:[[:space:]]*//p' | head -n 1 | normalise
)
[ -n "$expected" ] || fail "no SHA-256 certificate fingerprint in $ANDROID_KEYSTORE_FILE"
echo "release certificate SHA-256: $expected"

"$APKSIGNER" verify --print-certs -v -Werr "$apk" >/dev/null ||
	fail "$apk has no valid signature"
apk_actual=$(
	"$APKSIGNER" verify --print-certs "$apk" |
		sed -n 's/^Signer #1 certificate SHA-256 digest: //p' | head -n 1 | normalise
)
[ "$apk_actual" = "$expected" ] ||
	fail "$apk is signed by $apk_actual, not by the release key"

# jarsigner exits 0 for an unsigned file and only says so, which is what the
# grep turns into a failure. Its -strict would refuse every Android key,
# self-signed by nature. keytool reads the signer certificate out of the same
# bundle, which apksigner cannot do.
jarsigner -verify "$aab" | grep -q '^jar verified' ||
	fail "$aab has no valid signature"
aab_actual=$(
	keytool -printcert -jarfile "$aab" |
		sed -n 's/^[[:space:]]*SHA256:[[:space:]]*//p' | head -n 1 | normalise
)
[ "$aab_actual" = "$expected" ] ||
	fail "$aab is signed by $aab_actual, not by the release key"

echo "$apk and $aab carry the release signature"
