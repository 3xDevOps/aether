#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

dist="$tmp/dist"
published="$tmp/published"
rebuilt="$tmp/rebuilt"
bin="$tmp/bin"
log="$tmp/gh.log"
built=1111111111111111111111111111111111111111
moved=2222222222222222222222222222222222222222
mkdir -p "$dist" "$published" "$rebuilt" "$bin" "$tmp/state"
printf '%s\n' binary >"$dist/aether-linux-amd64"
(cd "$dist" && sha256sum aether-linux-amd64 >checksums.txt)
sum=$(sha256sum "$dist/aether-linux-amd64" | cut -d ' ' -f 1)
manifest() {
	printf '{"schema":1,"version":"v0.2.0","commit":"%s","published":"%s","assets":{"aether-linux-amd64":{"sha256":"%s","size":7}}}\n' \
		"$1" "$2" "$sum" >"$3/aether-release.json"
}
manifest "$built" now "$dist"
printf '%s\n' signature >"$dist/aether-release.json.sig"
# What an earlier run of the same commit published: the same assets under a
# manifest signed at another time. rebuilt is the same tag from another commit.
cp "$dist"/* "$published"/
cp "$dist"/* "$rebuilt"/
manifest "$built" earlier "$published"
manifest "$moved" earlier "$rebuilt"

# The stub keeps the v0.2.0 release in $GH_STATE_DIR: state is none, draft,
# published, duplicate or error, and assets lists its assets. GH_PUBLISHED
# names other complete published releases, GH_LEGACY published releases
# without a manifest, GH_TAG_SHA the commit the tag resolves to and
# GH_DRAFT_BODY a replacement for the draft's binary. API answers are JSON
# filtered by the script's own --jq, as gh does.
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
state_dir=$GH_STATE_DIR
release() {
	jq -n --arg tag "$1" --argjson draft "$2" --argjson prerelease "$3" \
		'{tag_name: $tag, draft: $draft, prerelease: $prerelease, assets: ($ARGS.positional | map({name: .}))}' \
		--args $4
}
complete="aether-linux-amd64 checksums.txt aether-release.json aether-release.json.sig"
if [ "$1" = api ]; then
	shift
	path=
	filter=.
	while [ "$#" -gt 0 ]; do
		case $1 in
		--paginate) ;;
		--jq) filter=$2 && shift ;;
		*) path=$1 ;;
		esac
		shift
	done
	printf 'api %s\n' "$path" >>"$GH_LOG"
	state=$(cat "$state_dir/state")
	assets=$(cat "$state_dir/assets")
	case $path in
	*/releases\?per_page=100)
		if [ "$state" = error ]; then
			echo "HTTP 502: Bad Gateway (https://api.github.com/repos/3xDevOps/Aether/releases)" >&2
			exit 1
		fi
		json=$(
			case $state in
			draft) release v0.2.0 true false "$assets" ;;
			published) release v0.2.0 false false "$assets" ;;
			duplicate) release v0.2.0 true false "" && release v0.2.0 true false "" ;;
			esac
			release v9.0.0 false true "$complete"
			for other in ${GH_PUBLISHED:-}; do
				release "$other" false false "$complete"
			done
			for other in ${GH_LEGACY:-}; do
				release "$other" false false aether-linux-amd64
			done
		)
		printf '%s\n' "$json" | jq -s . | jq -r "$filter"
		;;
	*/commits/v0.2.0) printf '{"sha":"%s"}\n' "${GH_TAG_SHA:-$COMMIT}" | jq -r "$filter" ;;
	*) exit 2 ;;
	esac
	exit 0
fi
case "$1 $2" in
"release download") printf 'release download %s\n' "$3" >>"$GH_LOG" ;;
*) printf '%s\n' "$*" >>"$GH_LOG" ;;
esac
case "$1 $2 $3" in
"release create v0.2.0")
	echo draft >"$state_dir/state"
	: >"$state_dir/assets"
	;;
"release upload v0.2.0")
	shift 4
	for file in "$@"; do
		basename "$file"
	done >>"$state_dir/assets"
	sort -u "$state_dir/assets" -o "$state_dir/assets"
	;;
"release delete-asset v0.2.0")
	grep -vxF "$4" "$state_dir/assets" >"$state_dir/kept" || true
	mv "$state_dir/kept" "$state_dir/assets"
	;;
"release edit v0.2.0")
	case $4 in
	--draft=false) echo published >"$state_dir/state" ;;
	--draft=true) echo draft >"$state_dir/state" ;;
	esac
	;;
"release download v0.2.0")
	mkdir -p "$5"
	cp "$DIST"/* "$5"/
	if [ -n "${GH_DRAFT_BODY:-}" ]; then
		printf '%s\n' "$GH_DRAFT_BODY" >"$5/aether-linux-amd64"
	fi
	;;
"release edit "*) ;;
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
export GH_LOG="$log" GH_STATE_DIR="$tmp/state" DIST="$dist" PUBLIC="$dist" GITHUB_REPOSITORY=3xDevOps/Aether COMMIT="$built"
export PATH="$bin:$PATH"

tag="v0.2.0"
url="https://github.com/3xDevOps/Aether/releases/download/$tag"
view="api repos/3xDevOps/Aether/releases?per_page=100"
resolve="api repos/3xDevOps/Aether/commits/$tag"
upload_all="release upload $tag --clobber $dist/aether-linux-amd64 $dist/aether-release.json $dist/aether-release.json.sig $dist/checksums.txt"
complete="aether-linux-amd64 checksums.txt aether-release.json aether-release.json.sig"
expected="$tmp/expected.log"

# GH_STATE and GH_ASSETS set the release each run starts from.
run() {
	: >"$log"
	echo "$GH_STATE" >"$tmp/state/state"
	printf '%s\n' "${GH_ASSETS:-}" | tr ' ' '\n' >"$tmp/state/assets"
	sh "$script_dir/publish-release.sh" "$tag" "$dist" >/dev/null 2>"$tmp/err"
}

reconcile_lines() {
	printf '%s\n' "$view" "release edit $1 --latest=true"
}

publish_lines() {
	printf '%s\n' \
		"release download $tag" \
		"$resolve" \
		"release edit $tag --draft=false --latest=false" \
		"curl $url/aether-linux-amd64" \
		"curl $url/checksums.txt" \
		"curl $url/aether-release.json" \
		"curl $url/aether-release.json.sig"
	reconcile_lines "$1"
}

refuses() {
	if "$@"; then
		echo "publish-release succeeded where it must fail: $*" >&2
		exit 1
	fi
}

not_logged() {
	if grep -q -- "$1" "$log"; then
		echo "publish-release ran $1" >&2
		exit 1
	fi
}

# A fresh tag becomes a draft, is verified, published, and takes Latest.
export GH_STATE=none GH_PUBLISHED="v0.1.0 v0.2.0-alpha.3"
run
{
	printf '%s\n' "$view" "$resolve" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.2.0-alpha.3" \
		"$upload_all"
	publish_lines "$tag"
} >"$expected"
diff -u "$expected" "$log"

# A retry finds the draft a partial upload left and completes it.
export GH_STATE=draft GH_ASSETS="aether-linux-amd64"
run
{
	printf '%s\n' "$view" "$upload_all"
	publish_lines "$tag"
} >"$expected"
diff -u "$expected" "$log"

# An asset the draft should not carry is removed before publishing.
export GH_ASSETS="aether-linux-amd64 aether-linux-amd64.old"
run
{
	printf '%s\n' "$view" "$upload_all" \
		"release delete-asset $tag aether-linux-amd64.old --yes"
	publish_lines "$tag"
} >"$expected"
diff -u "$expected" "$log"

# A draft that does not match the manifest stays a draft.
GH_DRAFT_BODY=tampered refuses run
grep -q "draft $tag/aether-linux-amd64 is 9 bytes with sha256 .*; the manifest says 7 bytes with sha256 $sum" "$tmp/err"
not_logged "release edit"

# A tag moved after release-policy is not published: the draft stays a draft
# and no draft is created for a fresh tag.
GH_TAG_SHA=$moved refuses run
grep -q "tag $tag now names $moved; this run built $built, so $tag is not published" "$tmp/err"
not_logged "release edit"
grep -qx draft "$tmp/state/state"
GH_STATE=none GH_TAG_SHA=$moved refuses run
not_logged "release create"

# An older tag that finishes after a newer one leaves Latest on the newer
# one. A published release without a manifest is not complete, so it is not
# Latest even with a higher version, and neither is a prerelease.
export GH_STATE=none GH_PUBLISHED="v0.3.0 v0.1.0 nightly" GH_LEGACY="v0.4.0"
run
{
	printf '%s\n' "$view" "$resolve" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.1.0" \
		"$upload_all"
	publish_lines v0.3.0
} >"$expected"
diff -u "$expected" "$log"
export GH_LEGACY=

# A rerun after publication verifies the public assets against the
# published manifest, uploads nothing and settles Latest again.
export GH_STATE=published GH_ASSETS="$complete" GH_PUBLISHED="v0.1.0"
PUBLIC="$published" run
{
	printf '%s\n' "$view" \
		"curl $url/aether-release.json" \
		"curl $url/aether-release.json.sig" \
		"curl $url/aether-linux-amd64" \
		"curl $url/checksums.txt"
	reconcile_lines "$tag"
} >"$expected"
diff -u "$expected" "$log"

# A rerun whose tag was published from another commit fails loudly.
PUBLIC="$rebuilt" refuses run
grep -q "tag $tag was published from commit $moved; this run built $built" "$tmp/err"
not_logged "release edit"

# A published asset its manifest does not list fails the rerun.
GH_ASSETS="$complete aether-linux-amd64.old" PUBLIC="$published" refuses run
grep -q "published $tag holds other assets than its aether-release.json lists" "$tmp/err"

# A published release whose assets no longer match its manifest fails.
PUBLIC="$published" CURL_TAMPER=aether-linux-amd64 refuses run
grep -q "$url/aether-linux-amd64 is 9 bytes" "$tmp/err"
not_logged "release upload"

# A release published without a manifest is incomplete: it goes back to a
# draft, then is uploaded, verified and published from there.
export GH_ASSETS="aether-linux-amd64"
run
{
	printf '%s\n' "$view" "release edit $tag --draft=true" "$upload_all"
	publish_lines "$tag"
} >"$expected"
diff -u "$expected" "$log"
grep -q "returning it to a draft" "$tmp/err"

# A public download that does not match after publishing fails the job.
export GH_STATE=draft
CURL_TAMPER=aether-linux-amd64 refuses run
grep -q "$url/aether-linux-amd64 is 9 bytes with sha256 .*; the manifest says 7 bytes with sha256 $sum" "$tmp/err"

# A GitHub API failure is reported as GitHub gave it, not as a missing release.
export GH_STATE=error
refuses run
grep -q "HTTP 502: Bad Gateway" "$tmp/err"
printf '%s\n' "$view" >"$expected"
diff -u "$expected" "$log"

# Two releases for one tag are not guessed between.
export GH_STATE=duplicate
refuses run
grep -q "$tag has more than one release" "$tmp/err"

# No signature, no release.
rm "$dist/aether-release.json.sig"
refuses run
grep -q "aether-release.json.sig is missing" "$tmp/err"
test ! -s "$log"
