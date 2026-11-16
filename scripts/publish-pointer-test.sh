#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

bin="$tmp/bin"
state="$tmp/state"
dist="$tmp/dist"
mkdir -p "$bin" "$dist"
echo manifest >"$dist/aether-release.json"

# The channels release lives in $state: meta is "<draft> <prerelease>" or
# missing for no release, assets/ its files, and public/ what anonymous
# downloads serve, which lags assets/ by $state/lag fetches.
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >>"$STATE/log"
case "$1 $2" in
"api repos/3xDevOps/Aether/releases/tags/channels")
	[ -f "$STATE/meta" ] || { echo "HTTP 404: Not Found" >&2; exit 1; }
	read -r draft prerelease <"$STATE/meta"
	(cd "$STATE/assets" && ls) | jq -R . | jq -s --argjson d "$draft" --argjson p "$prerelease" \
		'{draft: $d, prerelease: $p, assets: map({name: .})}'
	;;
"release download")
	shift 3
	dir=
	while [ "$#" -gt 0 ]; do
		case $1 in
		--pattern) cp "$STATE/assets/$2" "$tmp_dir/" 2>/dev/null || true; shift ;;
		--dir) dir=$2; shift ;;
		esac
		shift
	done
	cp "$tmp_dir"/* "$dir"/
	;;
"release upload")
	shift 4
	# As gh --clobber does: each old asset is deleted before its
	# replacement uploads. While $STATE/fail counts down, the .sig upload
	# fails after its old asset is gone.
	for f in "$@"; do
		rm -f "$STATE/assets/${f##*/}"
		fails=$(cat "$STATE/fail" 2>/dev/null || echo 0)
		case $f in *.sig) if [ "$fails" -gt 0 ]; then
			echo $((fails - 1)) >"$STATE/fail"
			echo "HTTP 502: upload failed" >&2
			exit 1
		fi ;; esac
		cp "$f" "$STATE/assets/"
	done
	;;
*) exit 2 ;;
esac
EOF
cat >"$bin/curl" <<'EOF'
#!/bin/sh
set -eu
out=
prev=
for arg in "$@"; do
	case $prev in --output) out=$arg ;; esac
	prev=$arg
	url=$arg
done
file=${url##*/}
lag=$(cat "$STATE/lag" 2>/dev/null || echo 0)
if [ "$lag" -gt 0 ]; then
	echo $((lag - 1)) >"$STATE/lag"
	cp "$STATE/stale/$file" "$out"
else
	cp "$STATE/assets/$file" "$out"
fi
EOF
# release-pointer itself is tested in Go; this one records how it was run,
# refuses when $STATE/refuse exists, and with -if-above, or -first beside a
# current pointer, writes nothing when $STATE/keep exists.
cat >"$bin/release-pointer" <<'EOF'
#!/bin/sh
set -eu
echo "release-pointer $*" >>"$STATE/log"
[ ! -f "$STATE/refuse" ] || { echo "release-pointer: refusing" >&2; exit 1; }
keeps= first= current=
while [ "$#" -gt 0 ]; do
	case $1 in
	-if-above) keeps=1 ;;
	-first) first=1 ;;
	-channel) channel=$2 && shift ;;
	-tag) tag=$2 && shift ;;
	-current) cp "$2" "$STATE/current-seen" && current=1 && shift ;;
	-out) out=$2 && shift ;;
	-manifest) shift ;;
	esac
	shift
done
[ -z "$first" ] || [ -z "$current" ] || keeps=1
[ -z "$keeps" ] || [ ! -f "$STATE/keep" ] || exit 0
echo "$channel $tag" >"$out/$channel.json"
echo "sig $tag" >"$out/$channel.json.sig"
EOF
printf '#!/bin/sh\n' >"$bin/sleep"
chmod +x "$bin"/*

reset() {
	rm -rf "$state"
	mkdir -p "$state/assets" "$state/stale" "$state/tmp"
	echo "false true" >"$state/meta"
}

run() {
	want=$1
	shift
	set +e
	rm -rf "$tmp/pair"
	mkdir "$tmp/pair"
	PATH="$bin:$PATH" STATE="$state" tmp_dir="$state/tmp" GITHUB_REPOSITORY=3xDevOps/Aether GITHUB_RUN_ID=4242 \
		RELEASE_POINTER="$bin/release-pointer" sh "$script_dir/publish-pointer.sh" "$@" "$tmp/pair" >"$tmp/out" 2>&1
	got=$?
	set -e
	if [ "$got" -ne "$want" ]; then
		echo "publish-pointer $* exited $got, want $want:" >&2
		cat "$tmp/out" >&2
		exit 1
	fi
}

assert_out() {
	grep -qF -- "$1" "$tmp/out" || { echo "output lacks: $1" >&2; cat "$tmp/out" >&2; exit 1; }
}

refute_log() {
	! grep -qF -- "$1" "$state/log" 2>/dev/null || { echo "log has: $1" >&2; cat "$state/log" >&2; exit 1; }
}

# A missing pointer is not a first one unless the caller says so.
reset
run 1 beta v0.9.0-beta.2 "$dist"
assert_out "channels has no beta.json. A missing pointer is not a first one"
assert_out "gh run download <run-id> --repo 3xDevOps/Aether -n pointer-beta -D pair"
assert_out "once it has expired, a repository admin removes what is left of beta.json and re-signs it with first-pointer"
refute_log "release-pointer"
refute_log "release upload"

reset
run 0 --first beta v0.9.0-beta.2 "$dist"
grep -qF -- "-first" "$state/log"
refute_log "-current"
grep -qx "beta v0.9.0-beta.2" "$state/assets/beta.json"
grep -qx "beta v0.9.0-beta.2" "$tmp/pair/beta.json"
grep -qF -- "-manifest $dist/aether-release.json" "$state/log"

reset
echo "beta v0.9.0-beta.1" >"$state/assets/beta.json"
echo "sig v0.9.0-beta.1" >"$state/assets/beta.json.sig"
touch "$state/refuse"
run 1 --first beta v0.9.0-beta.2 "$dist"
grep -qF -- "-first -current" "$state/log"
refute_log "release upload"

# A re-run of the run that wrote the first pointer, which release-pointer
# finds naming this release, keeps it and succeeds.
reset
echo "beta v0.9.0-beta.2" >"$state/assets/beta.json"
echo "sig v0.9.0-beta.2" >"$state/assets/beta.json.sig"
touch "$state/keep"
run 0 --first beta v0.9.0-beta.2 "$dist"
assert_out "beta.json already names v0.9.0-beta.2 and stays"
refute_log "release upload"
: >"$state/log"
run 0 --first --if-above beta v0.9.0-beta.2 "$dist"
assert_out "beta.json already names v0.9.0-beta.2 and stays"
refute_log "-if-above"

# An upload that fails after deleting the old .sig is repaired by uploading
# the whole pair again.
reset
echo "beta v0.9.0-beta.1" >"$state/assets/beta.json"
echo "sig v0.9.0-beta.1" >"$state/assets/beta.json.sig"
echo 1 >"$state/fail"
run 0 beta v0.9.0-beta.2 "$dist"
[ "$(grep -c "release upload" "$state/log")" -eq 2 ]
grep -qx "beta v0.9.0-beta.2" "$state/assets/beta.json"
grep -qx "sig v0.9.0-beta.2" "$state/assets/beta.json.sig"

# One that keeps failing names the artifact that restores the pair, and
# leaves this run's signed pair in the pair directory for it.
reset
echo "beta v0.9.0-beta.1" >"$state/assets/beta.json"
echo "sig v0.9.0-beta.1" >"$state/assets/beta.json.sig"
echo 99 >"$state/fail"
run 1 beta v0.9.0-beta.2 "$dist"
assert_out "uploading beta.json failed 3 times and channels may hold half a pair. This run's signed pair is its pointer-beta artifact; restore it: gh run download 4242 --repo 3xDevOps/Aether -n pointer-beta"
[ "$(grep -c "release upload" "$state/log")" -eq 3 ]
grep -qx "sig v0.9.0-beta.2" "$tmp/pair/beta.json.sig"

# A current pointer goes to release-pointer, which decides; a stale public
# copy is fetched again until it matches.
reset
echo "beta v0.9.0-beta.1" >"$state/assets/beta.json"
echo "sig v0.9.0-beta.1" >"$state/assets/beta.json.sig"
cp "$state/assets/beta.json" "$state/assets/beta.json.sig" "$state/stale/"
echo 2 >"$state/lag"
run 0 beta v0.9.0-beta.2 "$dist"
grep -qx "beta v0.9.0-beta.1" "$state/current-seen"
grep -qx "beta v0.9.0-beta.2" "$state/assets/beta.json"
assert_out "beta.json names v0.9.0-beta.2"

# --if-above moves a pointer the release is above, and keeps any other.
reset
run 0 --first --if-above beta v0.9.0 "$dist"
grep -qF -- "-if-above" "$state/log"
grep -qx "beta v0.9.0" "$state/assets/beta.json"
reset
echo "beta v0.9.1-beta.1" >"$state/assets/beta.json"
echo "sig v0.9.1-beta.1" >"$state/assets/beta.json.sig"
touch "$state/keep"
run 0 --if-above beta v0.9.0 "$dist"
assert_out "beta.json is not below v0.9.0 and stays"
refute_log "release upload"
reset
run 0 --first beta v0.9.0 "$dist"
refute_log "-if-above"

# A refusal uploads nothing.
reset
touch "$state/refuse"
run 1 --first beta v0.9.0-beta.2 "$dist"
assert_out "release-pointer: refusing"
refute_log "release upload"

# A public copy that never matches fails the run.
reset
echo "beta old" >"$state/stale/beta.json"
echo 99 >"$state/lag"
run 1 --first beta v0.9.0-beta.2 "$dist"
assert_out "differs from the pointer this run signed"

# A pointer without its signature.
reset
echo "beta v0.9.0-beta.1" >"$state/assets/beta.json"
run 1 beta v0.9.0-beta.2 "$dist"
assert_out "it needs both beta.json and beta.json.sig. Restore the last good pair from the pointer-beta artifact"
refute_log "release-pointer"

# No channels release, a channels release that is not a published
# prerelease, or one carrying a manifest.
reset
rm "$state/meta"
run 1 beta v0.9.0-beta.2 "$dist"
assert_out "A repository admin creates it once: gh release create channels --prerelease"
for meta in "false false" "true true"; do
	reset
	echo "$meta" >"$state/meta"
	run 1 beta v0.9.0-beta.2 "$dist"
	assert_out "must be a published prerelease"
	refute_log "release upload"
done
reset
echo m >"$state/assets/aether-release.json"
run 1 beta v0.9.0-beta.2 "$dist"
assert_out "carries aether-release.json"

reset
run 1 latest v0.9.0-beta.2 "$dist"
assert_out "latest is not nightly, beta or stable"
refute_log "gh "

echo "publish-pointer tests passed"
