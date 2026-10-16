#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

dist="$tmp/dist"
bin="$tmp/bin"
log="$tmp/gh.log"
mkdir -p "$dist" "$bin"
printf '%s\n' binary >"$dist/aether-linux-amd64"
printf '%s\n' checksum >"$dist/checksums.txt"
sum=$(sha256sum "$dist/aether-linux-amd64" | cut -d ' ' -f 1)
printf '{"schema":1,"version":"v0.1.0-test","assets":{"aether-linux-amd64":{"sha256":"%s","size":7}}}\n' "$sum" \
	>"$dist/aether-release.json"
printf '%s\n' signature >"$dist/aether-release.json.sig"

cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$GH_LOG"
case "$1 $2" in
"release view")
	[ "${GH_EXISTING:-0}" = 1 ] || exit 1
	printf '%s\n' ${GH_ASSETS:-}
	;;
"release create" | "release upload") exit 0 ;;
*) exit 2 ;;
esac
EOF
# Serves the anonymous download from dist, or CURL_BODY when set.
cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=
while [ "$#" -gt 1 ]; do
	[ "$1" = --output ] && out="$2"
	shift
done
printf 'curl %s\n' "$1" >>"$GH_LOG"
if [ -n "${CURL_BODY:-}" ]; then
	printf '%s\n' "$CURL_BODY" >"$out"
else
	cp "$DIST/${1##*/}" "$out"
fi
EOF
chmod 0755 "$bin/gh" "$bin/curl"
export GH_LOG="$log" DIST="$dist" GITHUB_REPOSITORY=3xDevOps/Aether
export PATH="$bin:$PATH"

tag="v0.1.0-test"
url="https://github.com/3xDevOps/Aether/releases/download/$tag/aether-linux-amd64"
expected="$tmp/expected.log"

sh "$script_dir/publish-release.sh" "$tag" "$dist" >/dev/null
printf '%s\n' \
	"release view $tag --json assets --jq .assets[].name" \
	"release create $tag --title $tag --generate-notes $dist/aether-linux-amd64 $dist/checksums.txt" \
	"curl $url" \
	"release upload $tag $dist/aether-release.json $dist/aether-release.json.sig" \
	>"$expected"
diff -u "$expected" "$log"

: >"$log"
export GH_EXISTING=1 GH_ASSETS="aether-linux-amd64 checksums.txt"
sh "$script_dir/publish-release.sh" "$tag" "$dist" >/dev/null
printf '%s\n' \
	"release view $tag --json assets --jq .assets[].name" \
	"release upload $tag --clobber $dist/aether-linux-amd64 $dist/checksums.txt" \
	"curl $url" \
	"release upload $tag $dist/aether-release.json $dist/aether-release.json.sig" \
	>"$expected"
diff -u "$expected" "$log"

# A download that does not match the manifest never gets the manifest.
: >"$log"
if CURL_BODY=tampered sh "$script_dir/publish-release.sh" "$tag" "$dist" 2>"$tmp/err"; then
	echo "publish-release accepted a download that does not match the manifest" >&2
	exit 1
fi
grep -q "the manifest says 7 bytes with sha256 $sum" "$tmp/err"
if grep -q "aether-release.json" "$log"; then
	echo "publish-release uploaded the manifest after a failed verification" >&2
	exit 1
fi

# A release that already has a manifest is never re-uploaded.
: >"$log"
export GH_ASSETS="aether-linux-amd64 aether-release.json"
if sh "$script_dir/publish-release.sh" "$tag" "$dist" 2>"$tmp/err"; then
	echo "publish-release re-uploaded a signed release" >&2
	exit 1
fi
grep -q "already has a signed manifest" "$tmp/err"
printf '%s\n' "release view $tag --json assets --jq .assets[].name" >"$expected"
diff -u "$expected" "$log"

# No manifest, no release.
: >"$log"
rm "$dist/aether-release.json.sig"
if sh "$script_dir/publish-release.sh" "$tag" "$dist" 2>"$tmp/err"; then
	echo "publish-release published without a signature" >&2
	exit 1
fi
grep -q "aether-release.json.sig is missing" "$tmp/err"
test ! -s "$log"
