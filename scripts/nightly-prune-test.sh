#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
bin="$tmp/bin"
mkdir -p "$bin"

old=$(date -u -d '-20 days' +%Y-%m-%dT%H:%M:%SZ)
young=$(date -u -d '-2 days' +%Y-%m-%dT%H:%M:%SZ)
c1=1111111111111111111111111111111111111111

# gh: every package has the versions in $STATE/versions as "<id> <tags>".
cat >"$bin/gh" <<EOF
#!/bin/sh
set -eu
echo "gh \$*" >>"\$STATE/log"
case "\$*" in
"api --paginate repos/3xDevOps/Aether/releases?per_page=100 "*)
	printf '%s\n' "v0.9.0-nightly.1 false $old" "v0.9.0-nightly.2 false $young" "v0.9.0-beta.1 false $old" "v0.9.0-nightly.3 true -"
	;;
"api repos/3xDevOps/Aether/releases/tags/channels") echo '{"assets":[{"name":"stable.json"},{"name":"stable.json.sig"},{"name":"nightly.json"},{"name":"nightly.json.sig"},{"name":"beta.json"}]}' ;;
"release download "*)
	while [ "\$#" -gt 1 ]; do
		[ "\$1" = --dir ] && { mkdir -p "\$2"; : >"\$2/downloaded"; }
		shift
	done
	;;
"api --paginate orgs/3xDevOps/packages/container/"*) cat "\$STATE/versions" ;;
"api -X DELETE orgs/3xDevOps/packages/container/"*) ;;
"release delete "*) ;;
*) echo "gh stub: unexpected \$*" >&2; exit 2 ;;
esac
EOF

cat >"$bin/nightly-plan" <<'EOF'
#!/bin/sh
echo "nightly-plan $*" >>"$STATE/log"
cat "$STATE/prune"
EOF
chmod +x "$bin/gh" "$bin/nightly-plan"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

reset() {
	export STATE="$tmp/state"
	rm -rf "$STATE"
	mkdir -p "$STATE"
	: >"$STATE/log"
	echo "v0.9.0-nightly.1 $c1" >"$STATE/prune"
	# The gh --jq output: id and comma-joined tags.
	cat >"$STATE/versions" <<EOF
10 v0.9.0-nightly.1,$c1,sha-11111111
11 v0.9.0-nightly.1-amd64
12 v0.9.0-nightly.1-arm64,v0.9.0-beta.1-arm64
13
14 v0.9.0-nightly.2
15 $c1
16 v0.9.0-nightly.1-amd64,sha-1111112
EOF
}

prune() {
	PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether NIGHTLY_PLAN=nightly-plan \
		sh "$script_dir/nightly-prune.sh" "$@" >"$tmp/out" 2>"$tmp/err"
}

# Only nightlies older than 14 days have their manifests read; only
# stable.json and nightly.json are downloaded.
reset
prune --dry-run || fail "dry run: $(cat "$tmp/err")"
grep -q 'release download v0.9.0-nightly.1 ' "$STATE/log" || fail "the old nightly's manifest was not read"
for tag in v0.9.0-nightly.2 v0.9.0-beta.1 v0.9.0-nightly.3; do
	if grep -q "release download $tag " "$STATE/log"; then fail "$tag's manifest was read"; fi
done
if grep -q 'pattern beta.json' "$STATE/log"; then fail "beta.json was downloaded"; fi
if grep -q -e '-X DELETE' -e 'release delete' "$STATE/log"; then fail "the dry run deleted something"; fi
grep -qx 'would delete release v0.9.0-nightly.1 (its git tag stays)' "$tmp/out" || fail "dry run output: $(cat "$tmp/out")"
grep -q 'would delete aether-edge version 10 ' "$tmp/out" || fail "dry run images: $(cat "$tmp/out")"

# The real run deletes the nightly's own versions in each package, keeps a
# version that also carries another release's tag, then the release, never
# its git tag.
reset
prune || fail "prune: $(cat "$tmp/err")"
for package in aether-browser aether-edge aether-standard; do
	for id in 10 11; do
		grep -qx "gh api -X DELETE orgs/3xDevOps/packages/container/$package/versions/$id" "$STATE/log" || fail "$package version $id was not deleted"
	done
	for id in 12 13 14 15 16; do
		if grep -q "container/$package/versions/$id\$" "$STATE/log"; then fail "$package version $id was deleted"; fi
	done
done
grep -q 'keeping aether-edge version 12 .*v0.9.0-beta.1-arm64' "$tmp/out" || fail "kept version not reported: $(cat "$tmp/out")"
grep -qx 'gh release delete v0.9.0-nightly.1 --repo 3xDevOps/Aether --yes' "$STATE/log" || fail "release not deleted as expected: $(cat "$STATE/log")"
[ "$(grep -n 'release delete' "$STATE/log" | cut -d: -f1)" -gt "$(grep -n -- '-X DELETE' "$STATE/log" | tail -1 | cut -d: -f1)" ] ||
	fail "the release was deleted before its images"

# A nightly that aether-edge:nightly still carries, because a newer
# nightly's edge move failed, keeps its release and its images.
reset
printf '%s\n' "v0.9.0-nightly.1 $c1" "v0.9.0-nightly.0 2222222222222222222222222222222222222222" >"$STATE/prune"
cat >>"$STATE/versions" <<'EOF2'
20 v0.9.0-nightly.0,nightly,sha-2222222
21 v0.9.0-nightly.0-amd64
EOF2
prune || fail "carried: $(cat "$tmp/err")"
grep -qx 'keeping release v0.9.0-nightly.0: its image is still nightly' "$tmp/out" || fail "carried output: $(cat "$tmp/out")"
if grep -q -e 'release delete v0.9.0-nightly.0' -e 'versions/2[01]$' "$STATE/log"; then fail "deleted what :nightly carries: $(cat "$STATE/log")"; fi
grep -qx 'gh release delete v0.9.0-nightly.1 --repo 3xDevOps/Aether --yes' "$STATE/log" || fail "the other nightly was not pruned"

# Nothing to prune: no package listing, nothing deleted.
reset
: >"$STATE/prune"
prune || fail "nothing: $(cat "$tmp/err")"
grep -qx 'nothing to prune' "$tmp/out" || fail "nothing output: $(cat "$tmp/out")"
if grep -q 'packages' "$STATE/log"; then fail "packages were listed with nothing to prune"; fi

# A refusal of the tool (stable.json missing) deletes nothing.
reset
printf '#!/bin/sh\necho "nightly-plan: stable.json is missing" >&2\nexit 1\n' >"$bin/nightly-plan"
if prune; then fail "a refusal of nightly-plan prune was ignored"; fi
if grep -q -e 'DELETE' -e 'release delete' "$STATE/log"; then fail "deleted after a refusal"; fi

echo "nightly-prune tests passed"
