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

# The stub keeps the $GH_TAG release in $GH_STATE_DIR: state is none, draft,
# published, duplicate or error, prerelease its flag, and assets lists its
# assets. GH_PUBLISHED
# names other complete published releases, GH_LEGACY published releases
# without a manifest, GH_TAG_SHA the commit the tag resolves to,
# GH_DRAFT_BODY a replacement for the draft's binary and GH_RACE a release
# another run publishes just before this run's first Latest change. API answers are JSON
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
	prerelease=$(cat "$state_dir/prerelease" 2>/dev/null || echo false)
	case $path in
	*/releases\?per_page=100)
		if [ "$state" = error ]; then
			echo "HTTP 502: Bad Gateway (https://api.github.com/repos/3xDevOps/Aether/releases)" >&2
			exit 1
		fi
		json=$(
			case $state in
			draft) release "$GH_TAG" true "$prerelease" "$assets" ;;
			published) release "$GH_TAG" false "$prerelease" "$assets" ;;
			duplicate) release "$GH_TAG" true false "" && release "$GH_TAG" true false "" ;;
			esac
			release v9.0.0 false true "$complete"
			for other in ${GH_PUBLISHED:-} $(cat "$state_dir/raced" 2>/dev/null); do
				release "$other" false false "$complete"
			done
			for other in ${GH_LEGACY:-}; do
				release "$other" false false aether-linux-amd64
			done
			for other in ${GH_PRERELEASED:-}; do
				release "$other" false true "$complete"
			done
		)
		printf '%s\n' "$json" | jq -s . | jq -r "$filter"
		;;
	*/commits/"$GH_TAG") printf '{"sha":"%s"}\n' "${GH_TAG_SHA:-$COMMIT}" | jq -r "$filter" ;;
	*) exit 2 ;;
	esac
	exit 0
fi
case "$1 $2" in
"release download") printf 'release download %s\n' "$3" >>"$GH_LOG" ;;
*) printf '%s\n' "$*" >>"$GH_LOG" ;;
esac
if [ -n "${GH_RACE:-}" ] && [ "$1 $2 ${4:-}" = "release edit --latest=true" ]; then
	echo "$GH_RACE" >"$state_dir/raced"
fi
case "$1 $2 $3" in
"release create $GH_TAG")
	echo draft >"$state_dir/state"
	: >"$state_dir/assets"
	;;
"release upload $GH_TAG")
	shift 4
	for file in "$@"; do
		basename "$file"
	done >>"$state_dir/assets"
	sort -u "$state_dir/assets" -o "$state_dir/assets"
	;;
"release delete-asset $GH_TAG")
	grep -vxF "$4" "$state_dir/assets" >"$state_dir/kept" || true
	mv "$state_dir/kept" "$state_dir/assets"
	;;
"release edit $GH_TAG")
	case $4 in
	--draft=false) echo published >"$state_dir/state" ;;
	--draft=true) echo draft >"$state_dir/state" ;;
	esac
	case ${5:-} in --prerelease=*) echo "${5#--prerelease=}" >"$state_dir/prerelease" ;; esac
	;;
"release download $GH_TAG")
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
# Serves anonymous downloads of the $GH_TAG release from PUBLIC, except a
# tampered CURL_TAMPER. Another release's aether-release.json comes from
# $OTHERS/<tag>.json, else describes that tag from before channels; none is
# served for CURL_MISSING.
cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=
while [ "$#" -gt 1 ]; do
	[ "$1" = --output ] && out="$2"
	shift
done
printf 'curl %s\n' "$1" >>"$GH_LOG"
release=${1%/*}
release=${release##*/}
if [ "$release" != "$GH_TAG" ]; then
	[ "$release" != "${CURL_MISSING:-}" ] || exit 22
	if [ -f "$OTHERS/$release.json" ]; then
		cp "$OTHERS/$release.json" "$out"
	else
		printf '{"schema":1,"version":"%s"}\n' "$release" >"$out"
	fi
elif [ "${1##*/}" = "${CURL_TAMPER:-}" ]; then
	printf '%s\n' tampered >"$out"
else
	cp "$PUBLIC/${1##*/}" "$out"
fi
EOF
chmod 0755 "$bin/gh" "$bin/curl"
mkdir "$tmp/others"
export GH_LOG="$log" GH_STATE_DIR="$tmp/state" DIST="$dist" PUBLIC="$dist" OTHERS="$tmp/others" GH_TAG=v0.2.0 GITHUB_REPOSITORY=3xDevOps/Aether COMMIT="$built"
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
	rm -f "$tmp/state/raced" "$tmp/state/prerelease"
	printf '%s\n' "${GH_ASSETS:-}" | tr ' ' '\n' >"$tmp/state/assets"
	sh "$script_dir/publish-release.sh" "$GH_TAG" "$dist" >/dev/null 2>"$tmp/err"
}

# The anonymous manifest downloads that order the releases named.
fetched() {
	for other in "$@"; do
		echo "curl https://github.com/3xDevOps/Aether/releases/download/$other/aether-release.json"
	done
}

# Latest settles on $1 after reading the manifests of the releases after it,
# which no earlier listing named.
reconcile_lines() {
	best=$1
	shift
	echo "$view"
	fetched "$@"
	printf '%s\n' "release edit $best --latest=true" "$view"
}

publish_lines() {
	printf '%s\n' \
		"release download $tag" \
		"$resolve" \
		"release edit $tag --draft=false --prerelease=false --latest=false" \
		"curl $url/aether-linux-amd64" \
		"curl $url/checksums.txt" \
		"curl $url/aether-release.json" \
		"curl $url/aether-release.json.sig"
	reconcile_lines "$@"
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
	echo "$view"
	fetched v0.1.0 v0.2.0-alpha.3
	printf '%s\n' "$resolve" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.2.0-alpha.3" \
		"$upload_all"
	publish_lines "$tag" "$tag"
} >"$expected"
diff -u "$expected" "$log"

# A retry finds the draft a partial upload left and completes it.
export GH_STATE=draft GH_ASSETS="aether-linux-amd64"
run
{
	echo "$view"
	fetched v0.1.0 v0.2.0-alpha.3
	echo "$upload_all"
	publish_lines "$tag" "$tag"
} >"$expected"
diff -u "$expected" "$log"

# An asset the draft should not carry is removed before publishing.
export GH_ASSETS="aether-linux-amd64 aether-linux-amd64.old"
run
{
	echo "$view"
	fetched v0.1.0 v0.2.0-alpha.3
	printf '%s\n' "$upload_all" "release delete-asset $tag aether-linux-amd64.old --yes"
	publish_lines "$tag" "$tag"
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
	echo "$view"
	fetched v0.3.0 v0.1.0
	printf '%s\n' "$resolve" \
		"release create $tag --draft --verify-tag --title $tag --generate-notes --notes-start-tag=v0.1.0" \
		"$upload_all"
	publish_lines v0.3.0 "$tag"
} >"$expected"
diff -u "$expected" "$log"
export GH_LEGACY=

# A newer release published while this run marks Latest takes it back.
GH_STATE=draft GH_ASSETS="$complete" GH_PUBLISHED="v0.1.0" GH_RACE=v0.3.0 run
{
	echo "$view"
	fetched v0.1.0
	echo "$upload_all"
	publish_lines "$tag" "$tag"
	fetched v0.3.0
	printf '%s\n' "release edit v0.3.0 --latest=true" "$view"
} >"$expected"
diff -u "$expected" "$log"

# A rerun after publication verifies the public assets against the
# published manifest, uploads nothing and settles Latest again.
export GH_STATE=published GH_ASSETS="$complete" GH_PUBLISHED="v0.1.0"
PUBLIC="$published" run
{
	echo "$view"
	fetched "$tag" v0.1.0
	printf '%s\n' \
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
	echo "$view"
	fetched v0.1.0
	printf '%s\n' "release edit $tag --draft=true" "$upload_all"
	publish_lines "$tag" "$tag"
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

logged() {
	grep -qxF -- "$1" "$log" || {
		echo "publish-release did not run $1" >&2
		exit 1
	}
}

# Writes the aether-release.json another published release $1 serves.
other() {
	printf '%s\n' "$2" >"$OTHERS/$1.json"
}

# Signs $1 into dist's manifest with channel $2, build $3 and hotfix $4.
stamp() {
	jq -n --arg tag "$1" --arg channel "${2:-}" --argjson build "${3:-0}" --argjson hotfix "${4:-0}" \
		--arg commit "$built" --arg sum "$sum" \
		'{schema: 1, version: $tag, commit: $commit, assets: {"aether-linux-amd64": {sha256: $sum, size: 7}}}
		+ (if $channel == "" then {} else {channel: $channel, build: $build} end)
		+ (if $hotfix == 0 then {} else {hotfix: $hotfix} end)' >"$dist/aether-release.json"
}

# Latest is the stable release with the highest (build, hotfix): above a
# stable with a higher version but a lower build, above every release from
# before channels whatever its version, and never a prerelease, a nightly
# or beta published without the prerelease flag, or a release whose
# manifest describes another tag or carries a build without a channel.
other v0.9.0 '{"version":"v0.9.0","channel":"stable","build":3}'
other v0.9.1 '{"version":"v0.9.1","channel":"stable","build":3,"hotfix":1}'
other v0.10.0 '{"version":"v0.10.0","channel":"stable","build":2}'
other v0.11.0-nightly.1 '{"version":"v0.11.0-nightly.1","channel":"nightly","build":9}'
other v0.12.0-beta.1 '{"version":"v0.12.0-beta.1","channel":"beta","build":8}'
other v0.13.0 '{"version":"v0.13.0","build":50}'
other v0.14.0 '{"version":"v0.1.0"}'
other v0.15.0 '{"version":"v0.15.0","channel":"stable","build":"60"}'
others="v0.10.0 v0.9.0 v5.0.0 v0.9.1 v0.12.0-beta.1 v0.13.0 v0.14.0 v0.15.0"
export GH_STATE=published GH_ASSETS="$complete" GH_PUBLISHED="$others" GH_PRERELEASED="v0.11.0-nightly.1"
PUBLIC="$published" run
logged "release edit v0.9.1 --latest=true"
test "$(grep -c -- '--latest=true' "$log")" -eq 1
for bad in v0.13.0 v0.14.0 v0.15.0; do
	grep -q "published release $bad has an aether-release.json for another tag or with invalid channel fields" "$tmp/err"
done
not_logged "download/v0.11.0-nightly.1/"

# Two stable releases of one (build, hotfix) are one commit under two
# labels; the higher version is Latest whichever the listing names first.
other v0.9.2 '{"version":"v0.9.2","channel":"stable","build":3,"hotfix":1}'
for listing in "v0.9.1 v0.9.2" "v0.9.2 v0.9.1"; do
	GH_PUBLISHED="$listing" PUBLIC="$published" run
	logged "release edit v0.9.2 --latest=true"
	not_logged "release edit v0.9.1 --latest=true"
done

# Without a build anywhere, the highest version is Latest, as before.
GH_PUBLISHED="v0.1.0 v5.0.0 v0.3.0-beta.2" PUBLIC="$published" run
logged "release edit v5.0.0 --latest=true"

# A manifest that cannot be read fails the run rather than leaving its
# release out of the order.
CURL_MISSING=v0.9.1 PUBLIC="$published" refuses run
grep -q "cannot read the aether-release.json of published release v0.9.1 to order it" "$tmp/err"
not_logged "--latest=true"

# A nightly or beta is published as a prerelease and never becomes Latest;
# a stable on a channel is published as a release and does.
export GH_STATE=none GH_ASSETS='' GH_PUBLISHED="v0.1.0 v5.0.0" GH_PRERELEASED=''
for c in "v0.3.0-nightly.2 nightly 6 true" "v0.3.0-beta.1 beta 6 true" "v0.3.0 stable 6 false"; do
	# Split on purpose: each case is a few words.
	# shellcheck disable=SC2086
	set -- $c
	stamp "$1" "$2" "$3"
	GH_TAG=$1 run
	logged "release create $1 --draft --verify-tag --title $1 --generate-notes --notes-start-tag=v5.0.0"
	logged "release edit $1 --draft=false --prerelease=$4 --latest=false"
	if [ "$4" = true ]; then
		logged "release edit v5.0.0 --latest=true"
		not_logged "release edit $1 --latest=true"
	else
		logged "release edit $1 --latest=true"
	fi
done

# A nightly tag needs a build, and the manifest's channel must match its tag.
for c in "v0.3.0-nightly.2" "v0.3.0 beta 6" "v0.3.0-beta.1 stable 6" "v0.3.0 stable 6 100"; do
	# Split on purpose: each case is a few words.
	# shellcheck disable=SC2086
	set -- $c
	stamp "$@"
	GH_TAG=$1 refuses run
	grep -q "does not describe $1 with channel fields an updater accepts, or $1 has no versionCode" "$tmp/err"
	not_logged "release create"
done
stamp v0.2.0
export GH_TAG=v0.2.0

# No signature, no release.
rm "$dist/aether-release.json.sig"
refuses run
grep -q "aether-release.json.sig is missing" "$tmp/err"
test ! -s "$log"
