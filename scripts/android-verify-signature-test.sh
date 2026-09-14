#!/bin/sh
# What android-verify-signature.sh accepts and what it refuses.
#
# keytool, jarsigner and apksigner are stubbed on PATH, so this needs no
# keystore, no artifacts and no Android SDK. Each case sets what the three
# tools report and asserts the verdict.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/android-verify-signature.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
stubs="$work/stubs"
mkdir -p "$stubs"

# The two spellings the script has to reconcile: keytool prints the
# fingerprint uppercase with colons, apksigner prints bare lowercase hex.
KEY_COLONS=CA:57:ED:31:64:AF:EC:03:97:DF:8B:F8:13:76:3C:FB:D9:61:A8:3F:7E:92:F0:21:B3:6A:9D:22:8B:CC:2E:50
KEY_HEX=ca57ed3164afec0397df8bf813763cfbd961a83f7e92f021b36a9d228bcc2e50
OTHER_COLONS=6B:41:6B:A0:23:D4:7D:A8:DB:18:C5:C0:57:D9:4F:21:00:0B:10:27:D4:0D:7E:01:C3:D3:08:D3:92:5E:46:39
OTHER_HEX=6b416ba023d47da8db18c5c057d94f21000b1027d40d7e01c3d308d3925e4639

# keytool answers two different questions here: the keystore's own
# fingerprint, and the one inside the bundle. An empty value prints no
# SHA256 line at all, which is what the both-empty guard is about.
cat >"$stubs/keytool" <<'EOF'
#!/bin/sh
for arg in "$@"; do
	if [ "$arg" = -printcert ]; then
		[ -n "$STUB_AAB_SHA256" ] || exit 0
		printf 'Certificate fingerprints:\n\t SHA256: %s\n' "$STUB_AAB_SHA256"
		exit 0
	fi
done
[ -n "$STUB_KEYSTORE_SHA256" ] || exit 0
printf 'Certificate fingerprints:\n\t SHA256: %s\n' "$STUB_KEYSTORE_SHA256"
EOF

# jarsigner exits 0 either way and only says which it was.
cat >"$stubs/jarsigner" <<'EOF'
#!/bin/sh
if [ "$STUB_AAB_SIGNED" = yes ]; then
	echo 'jar verified.'
else
	echo 'jar is unsigned. (signatures missing or not parsable)'
fi
EOF

# -Werr is the pass or fail; the second call is the one that prints the
# signer, and it is never reached when the first has already failed.
cat >"$stubs/apksigner" <<'EOF'
#!/bin/sh
[ "$STUB_APK_SIGNED" = yes ] || exit 1
for arg in "$@"; do
	[ "$arg" = -Werr ] && exit 0
done
printf 'Signer #1 certificate SHA-256 digest: %s\n' "$STUB_APK_SHA256"
EOF

chmod +x "$stubs/keytool" "$stubs/jarsigner" "$stubs/apksigner"
PATH="$stubs:$PATH"
export PATH
export APKSIGNER="$stubs/apksigner"
export ANDROID_KEYSTORE_FILE="$work/release.jks"
export ANDROID_KEYSTORE_PASSWORD=throwaway
export ANDROID_KEY_ALIAS=release
: >"$ANDROID_KEYSTORE_FILE"

apk="$work/aether-android.apk"
aab="$work/aether-android.aab"
: >"$apk"
: >"$aab"

# Everything signed by the release key unless a case says otherwise.
reset() {
	STUB_KEYSTORE_SHA256=$KEY_COLONS
	STUB_APK_SIGNED=yes
	STUB_APK_SHA256=$KEY_HEX
	STUB_AAB_SIGNED=yes
	STUB_AAB_SHA256=$KEY_COLONS
	export STUB_KEYSTORE_SHA256 STUB_APK_SIGNED STUB_APK_SHA256 \
		STUB_AAB_SIGNED STUB_AAB_SHA256
}

accepts() {
	if ! output=$(sh "$script" "$apk" "$aab" 2>&1); then
		echo "android-verify-signature-test: $1 was refused: $output" >&2
		exit 1
	fi
	case $output in
	*"carry the release signature"*) ;;
	*)
		echo "android-verify-signature-test: $1 passed without saying so: $output" >&2
		exit 1
		;;
	esac
}

# Refused, and the reason names what was wrong rather than dying on a shell
# error or an unset variable.
refuses() {
	want=$2
	if output=$(sh "$script" "$apk" "$aab" 2>&1); then
		echo "android-verify-signature-test: $1 was accepted: $output" >&2
		exit 1
	fi
	case $output in
	*"android-verify-signature: "*"$want"*) ;;
	*)
		echo "android-verify-signature-test: $1 failed without saying why: $output" >&2
		exit 1
		;;
	esac
}

# The release key signed both, in the two spellings the tools really print.
reset
accepts "the release key"

# A rotated or mistyped keystore: both artifacts verify, neither is ours.
reset
STUB_APK_SHA256=$OTHER_HEX
refuses "an APK signed by another key" "not by the release key"

reset
STUB_AAB_SHA256=$OTHER_COLONS
refuses "a bundle signed by another key" "not by the release key"

# A stale unsigned build left in dist/.
reset
STUB_APK_SIGNED=no
refuses "an unsigned APK" "has no valid signature"

reset
STUB_AAB_SIGNED=no
refuses "an unsigned bundle" "has no valid signature"

# The guard that matters most: with no fingerprint on either side, an
# unguarded comparison would find "" equal to "" and pass everything.
reset
STUB_KEYSTORE_SHA256=
STUB_APK_SHA256=
STUB_AAB_SHA256=
refuses "a keystore with no fingerprint" "no SHA-256 certificate fingerprint"

# Only the keystore side empty, so the artifacts still name a signer.
reset
STUB_KEYSTORE_SHA256=
refuses "a keystore keytool cannot read" "no SHA-256 certificate fingerprint"

reset
if sh "$script" "$apk" >/dev/null 2>&1; then
	echo "android-verify-signature-test: one argument was accepted" >&2
	exit 1
fi
if sh "$script" "$apk" "$aab" extra >/dev/null 2>&1; then
	echo "android-verify-signature-test: three arguments were accepted" >&2
	exit 1
fi

echo "android-verify-signature-test: ok"
