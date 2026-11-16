#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
bin="$tmp/bin"
mkdir -p "$bin"

# $STATE/identity is the "<signer> <source ref> <caller workflow> <run id>"
# the image's attestation was made with, in the repository $STATE/repo
# names (default the current one); $STATE/jobs-<id> are that run's
# jobs as "<name>|<conclusion>" lines and $STATE/run-<id> its
# "<status> <conclusion>".
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >>"$STATE/log"
signed=$(cat "$STATE/repo" 2>/dev/null || echo 3xDevOps/cloudfield)
repo=https://github.com/$signed
case "$1 $2" in
"attestation verify")
	[ -f "$STATE/identity" ] || exit 1
	read -r signer source caller id <"$STATE/identity"
	shift 2
	want=
	while [ "$#" -gt 0 ]; do
		case $1 in --repo | --signer-workflow | --source-ref) want="$want $2" ;; esac
		shift
	done
	[ "$want" = " $signed $signed/.github/workflows/$signer $source" ] || { echo "no matching attestation" >&2; exit 1; }
	jq -n --arg c "$repo/.github/workflows/$caller" --arg r "$repo/actions/runs/$id/attempts/1" \
		'[{verificationResult: {signature: {certificate: {buildConfigURI: $c, runInvocationURI: $r}}}}]'
	;;
"api --paginate")
	id=$(printf '%s' "$3" | sed 's,.*/runs/\([0-9]*\)/jobs.*,\1,')
	jq -R 'split("|") | {name: .[0], conclusion: .[1]}' "$STATE/jobs-$id" | jq -s '{jobs: .}'
	;;
"api repos/3xDevOps/cloudfield/actions/runs/"*)
	id=${2##*/}
	read -r status conclusion <"$STATE/run-$id"
	jq -n --arg s "$status" --arg c "$conclusion" --arg id "$id" \
		'{status: $s, conclusion: $c, html_url: ("https://github.com/3xDevOps/cloudfield/actions/runs/" + $id)}'
	;;
*) exit 2 ;;
esac
EOF
chmod +x "$bin/gh"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

reset() {
	export STATE="$tmp/state"
	rm -rf "$STATE"
	mkdir -p "$STATE"
	: >"$STATE/log"
	echo "in_progress" >"$STATE/run-7"
	printf 'release-build / edge-qualify|success\nrelease-build / release|success\n' >"$STATE/jobs-7"
}

run() {
	PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/cloudfield EDGE_ATTESTATION_RETRY_DELAY=0 \
		sh "$script_dir/edge-attestation.sh" "$1" ghcr.io/3xdevops/aether-edge@sha256:abc v0.9.0-beta.3 >"$tmp/out" 2>&1
}

# A release Promote built: release-build.yml from main, in a promote.yml
# run whose called edge-qualify succeeded, even while that run goes on.
reset
echo "release-build.yml refs/heads/main promote.yml@refs/heads/main 7" >"$STATE/identity"
run qualify || fail "a Promote-built release: $(cat "$tmp/out")"
grep -qx 'edge-qualify succeeded in https://github.com/3xDevOps/cloudfield/actions/runs/7' "$tmp/out" || fail "evidence: $(cat "$tmp/out")"

# A nightly: release-build.yml from main, in a nightly.yml run.
reset
echo "release-build.yml refs/heads/main nightly.yml@refs/heads/main 7" >"$STATE/identity"
run qualify || fail "a nightly.yml-built release: $(cat "$tmp/out")"

# A pushed tag, from release-build.yml or, before the split, release.yml.
for signer in release-build.yml release.yml; do
	reset
	echo "$signer refs/tags/v0.9.0-beta.3 release.yml@refs/tags/v0.9.0-beta.3 7" >"$STATE/identity"
	run qualify || fail "a pushed tag signed by $signer: $(cat "$tmp/out")"
done

# A release built before the repository took its current name is signed
# under its old name, and its run is read under the current one.
for signer in release-build.yml release.yml; do
	reset
	echo 3xDevOps/aether >"$STATE/repo"
	echo "$signer refs/tags/v0.9.0-beta.3 release.yml@refs/tags/v0.9.0-beta.3 7" >"$STATE/identity"
	run qualify || fail "a pushed tag signed by $signer under the old name: $(cat "$tmp/out")"
	grep -qx 'edge-qualify succeeded in https://github.com/3xDevOps/cloudfield/actions/runs/7' "$tmp/out" || fail "evidence: $(cat "$tmp/out")"
done
reset
echo 3xDevOps/aether >"$STATE/repo"
echo "release-build.yml refs/heads/main promote.yml@refs/heads/main 7" >"$STATE/identity"
run qualify || fail "a Promote-built release under the old name: $(cat "$tmp/out")"

# Refused: an attestation signed under any other repository name.
for other in 3xDevOps/website evil/aether evil/cloudfield 3xDevOps/aether-fork; do
	reset
	echo "$other" >"$STATE/repo"
	echo "release.yml refs/tags/v0.9.0-beta.3 release.yml@refs/tags/v0.9.0-beta.3 7" >"$STATE/identity"
	if run verify; then fail "an attestation signed in $other was accepted"; fi
	grep -q 'has no attestation from release-build.yml or release.yml in 3xDevOps/cloudfield or 3xDevOps/aether for v0.9.0-beta.3: no matching attestation' "$tmp/out" || fail "$other: $(cat "$tmp/out")"
done

# A release from before edge-qualify needs its whole run to have succeeded.
reset
echo "release.yml refs/tags/v0.9.0-beta.3 release.yml@refs/tags/v0.9.0-beta.3 7" >"$STATE/identity"
echo 'release|success' >"$STATE/jobs-7"
if run qualify; then fail "an unfinished run without edge-qualify was accepted"; fi
echo "completed success" >"$STATE/run-7"
run qualify || fail "a finished run without edge-qualify: $(cat "$tmp/out")"

# Refused: a failed edge-qualify, an attestation from a run another
# workflow started, another tag's source ref, and no attestation.
reset
echo "release-build.yml refs/heads/main promote.yml@refs/heads/main 7" >"$STATE/identity"
echo 'release-build / edge-qualify|failure' >"$STATE/jobs-7"
if run qualify; then fail "a failed edge-qualify was accepted"; fi
reset
echo "release-build.yml refs/heads/main nightly-test.yml@refs/heads/main 7" >"$STATE/identity"
if run qualify; then fail "a run another main workflow started was accepted"; fi
grep -q 'not made by a run of' "$tmp/out" || fail "another caller: $(cat "$tmp/out")"
run verify || fail "verify needs only the signer and source ref: $(cat "$tmp/out")"
reset
echo "release-build.yml refs/tags/v0.9.0-beta.2 release.yml@refs/tags/v0.9.0-beta.2 7" >"$STATE/identity"
if run verify; then fail "another tag's attestation was accepted"; fi
reset
if run verify; then fail "an image without an attestation was accepted"; fi
[ "$(grep -c 'attestation verify' "$STATE/log")" = 18 ] || fail "did not retry every identity under both names 3 times"

echo "edge-attestation tests passed"
