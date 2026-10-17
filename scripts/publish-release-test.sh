#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

dist="$tmp/dist"
published="$tmp/published"
bin="$tmp/bin"
log="$tmp/gh.log"
mkdir -p "$dist" "$published" "$bin"
printf '%s\n' binary >"$dist/aether-linux-amd64"
(cd "$dist" && sha256sum aether-linux-amd64 >checksums.txt)
sum=$(sha256sum "$dist/aether-linux-amd64" | cut -d ' ' -f 1)
printf '{"schema":1,"version":"v0.2.0","assets":{"aether-linux-amd64":{"sha256":"%s","size":7}}}\n' "$sum" \
	>"$dist/aether-release.json"
printf '%s\n' signature >"$dist/aether-release.json.sig"
# What an earlier run published: the same assets under a manifest signed
# at another time.
cp "$dist"/* "$published"/
printf '{"schema":1,"version":"v0.2.0","published":"earlier","assets":{"aether-linux-amd64":{"sha256":"%s","size":7}}}\n' "$sum" \
	>"$published/aether-release.json"

# GH_STATE is the state of the v0.2.0 release - none, draft, published,
# duplicate or error - GH_ASSETS its assets, GH_PUBLISHED the other published
# release tags and GH_DRAFT_BODY a replacement for the draft's binary.
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
case "$1 $2" in
"api --paginate") printf 'api %s\n' "$3" >>"$GH_LOG" ;;
"release download") printf 'release download %s\n' "$3" >>"$GH_LOG" ;;
*) printf '%s\n' "$*" >>"$GH_LOG" ;;
esac
case "$1 $2" in
"api --paginate")
	assets=$(printf '%s' "${GH_ASSETS:-}" | tr ' ' ',')
	case $GH_STATE in
	error) echo "HTTP 502: Bad Gateway (https://api.github.com/repos/3xDevOps/Aether/releases)" >&2 && exit 1 ;;
	draft) echo "v0.2.0 true false $assets" ;;
	published) echo "v0.2.0 false false $assets" ;;
	duplicate) printf '%s\n' "v0.2.0 true false" "v0.2.0 true false" ;;
	esac
	echo "v9.0.0 false true aether-linux-amd64"
	for other in ${GH_PUBLISHED:-}; do
		echo "$other false false"
	done
	;;
"release download")
	mkdir -p "$5"
	cp "$DIST"/* "$5"/
	if [ -n "${GH_DRAFT_BODY:-}" ]; then
		printf '%s\n' "$GH_DRAFT_BODY" >"$5/aether-linux-amd64"
	fi
	;;
"release create" | "release upload" | "release edit" | "release delete-asset") ;;
*) exit 2 ;;
esac
EOF
# Serves anonymous downloads from PUBLIC, except a tampered CURL_TAMPER.
cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=
while [ "$#" -gt 1 ]; do
	[ "$1" = --output ] && out="$2"
	shift
done
printf 'curl %s\n' "$1" >>"$GH_LOG"
if [ "${1##*/}" = "${CURL_TAMPER:-}" ]; then
	printf '%s\n' tampered >"$out"
else
	cp "$PUBLIC/${1##*/}" "$out"
fi
EOF
chmod 0755 "$bin/gh" "$bin/curl"
export GH_LOG="$log" DIST="$dist" PUBLIC="$dist" GITHUB_REPOSITORY=3xDevOps/Aether
export PATH="$bin:$PATH"

tag="v0.2.0"
url="https://github.com/3xDevOps/Aether/releases/download/$tag"
view="api repos/3xDevOps/Aether/releases?per_page=100"
upload_all="release upload $tag --clobber $dist/aether-linux-amd64 $dist/aether-release.json $dist/aether-release.json.sig $dist/checksums.txt"
expected="$tmp/expected.log"

run() {
	: >"$log"
	sh "$script_dir/publish-release.sh" "$tag" "$dist" >/dev/null 2>"$tmp/err"
}

publish_lines() {
	printf '%s\n' \
		"release download $tag" \
		"release edit $tag --draft=false --latest=$1" \
		"curl $url/aether-linux-amd64" \
		"curl $url/checksums.txt" \
		"curl $url/aether-release.json" \
		"curl $url/aether-release.json.sig"
}

# A fresh tag becomes a draft, is verified, then published as Latest.
export GH_STATE=none GH_PUBLISHED="v0.1.0 v0.2.0-alpha.3"
run
{
	printf '%s\n' "$view" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.2.0-alpha.3" \
		"$upload_all"
	publish_lines true
} >"$expected"
diff -u "$expected" "$log"

# A retry finds the draft a partial upload left and completes it.
export GH_STATE=draft GH_ASSETS="aether-linux-amd64"
run
{
	printf '%s\n' "$view" "$upload_all"
	publish_lines true
} >"$expected"
diff -u "$expected" "$log"

# An asset the draft should not carry is removed before publishing.
export GH_ASSETS="aether-linux-amd64 aether-linux-amd64.old"
run
{
	printf '%s\n' "$view" "$upload_all" \
		"release delete-asset $tag aether-linux-amd64.old --yes"
	publish_lines true
} >"$expected"
diff -u "$expected" "$log"

# A draft that does not match the manifest stays a draft.
if GH_DRAFT_BODY=tampered run; then
	echo "publish-release published a draft that does not match the manifest" >&2
	exit 1
fi
grep -q "draft $tag/aether-linux-amd64 is 9 bytes with sha256 .*; the manifest says 7 bytes with sha256 $sum" "$tmp/err"
if grep -q "release edit" "$log"; then
	echo "publish-release published after a failed draft verification" >&2
	exit 1
fi

# An older tag that finishes after a newer one does not take Latest.
export GH_STATE=none GH_PUBLISHED="v0.3.0 v0.1.0 nightly"
run
{
	printf '%s\n' "$view" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.1.0" \
		"$upload_all"
	publish_lines false
} >"$expected"
diff -u "$expected" "$log"

# A rerun after publication verifies the public assets against the
# published manifest and uploads nothing.
export GH_STATE=published GH_ASSETS="aether-linux-amd64 checksums.txt aether-release.json aether-release.json.sig"
PUBLIC="$published" run
printf '%s\n' "$view" \
	"curl $url/aether-release.json" \
	"curl $url/aether-release.json.sig" \
	"curl $url/aether-linux-amd64" \
	"curl $url/checksums.txt" \
	>"$expected"
diff -u "$expected" "$log"

# A published release whose assets no longer match its manifest fails.
if PUBLIC="$published" CURL_TAMPER=aether-linux-amd64 run; then
	echo "publish-release accepted a published release that does not match its manifest" >&2
	exit 1
fi
grep -q "$url/aether-linux-amd64 is 9 bytes" "$tmp/err"
if grep -q "release upload" "$log"; then
	echo "publish-release re-uploaded a signed release" >&2
	exit 1
fi

# A release published without a manifest is incomplete: it goes back to a
# draft, then is uploaded, verified and published from there.
export GH_ASSETS="aether-linux-amd64" GH_PUBLISHED="v0.1.0"
run
{
	printf '%s\n' "$view" "release edit $tag --draft=true" "$upload_all"
	publish_lines true
} >"$expected"
diff -u "$expected" "$log"
grep -q "returning it to a draft" "$tmp/err"

# A public download that does not match after publishing fails the job.
export GH_STATE=draft
if CURL_TAMPER=aether-linux-amd64 run; then
	echo "publish-release accepted a public download that does not match the manifest" >&2
	exit 1
fi
grep -q "$url/aether-linux-amd64 is 9 bytes with sha256 .*; the manifest says 7 bytes with sha256 $sum" "$tmp/err"

# A GitHub API failure is reported as GitHub gave it, not as a missing release.
export GH_STATE=error
if run; then
	echo "publish-release continued after a failed release lookup" >&2
	exit 1
fi
grep -q "HTTP 502: Bad Gateway" "$tmp/err"
printf '%s\n' "$view" >"$expected"
diff -u "$expected" "$log"

# Two releases for one tag are not guessed between.
export GH_STATE=duplicate
if run; then
	echo "publish-release chose between two releases for one tag" >&2
	exit 1
fi
grep -q "$tag has more than one release" "$tmp/err"

# No signature, no release.
rm "$dist/aether-release.json.sig"
if run; then
	echo "publish-release published without a signature" >&2
	exit 1
fi
grep -q "aether-release.json.sig is missing" "$tmp/err"
test ! -s "$log"
