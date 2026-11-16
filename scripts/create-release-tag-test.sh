#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

commit=1111111111111111111111111111111111111111
other=2222222222222222222222222222222222222222
bin="$tmp/bin"
mkdir -p "$bin"

# The repository's tag lives in $STATE: ref is "<type> <sha>" for
# refs/tags/v0.9.0-beta.1, tag.json the annotated tag object it names,
# readerr a non-404 failure to read it, and race a tag another writer
# creates just before this one.
cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
echo "gh $*" >>"$STATE/log"
case "$2" in
repos/3xDevOps/Aether/git/ref/tags/v0.9.0-beta.1)
	[ ! -f "$STATE/readerr" ] || { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
	[ -f "$STATE/ref" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
	read -r type sha <"$STATE/ref"
	jq -n --arg t "$type" --arg s "$sha" '{object: {type: $t, sha: $s}}'
	;;
repos/3xDevOps/Aether/git/tags/tagobject)
	cat "$STATE/tag.json"
	;;
repos/3xDevOps/Aether/git/tags)
	shift 2
	tag= message= object= type=
	while [ "$#" -gt 0 ]; do
		case $2 in tag=*) tag=${2#tag=} ;; message=*) message=${2#message=} ;; object=*) object=${2#object=} ;; type=*) type=${2#type=} ;; esac
		shift 2
		[ "${1-}" != --jq ] || break
	done
	jq -n --arg m "$message" --arg o "$object" --arg t "$type" '{message: $m, object: {type: $t, sha: $o}}' >"$STATE/new.json"
	echo tagobject
	;;
repos/3xDevOps/Aether/git/refs)
	if [ -f "$STATE/race" ]; then
		mv "$STATE/race" "$STATE/tag.json"
		echo "tag tagobject" >"$STATE/ref"
		echo "HTTP 422: Reference already exists" >&2
		exit 1
	fi
	mv "$STATE/new.json" "$STATE/tag.json"
	echo "tag tagobject" >"$STATE/ref"
	echo '{}'
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
}

tag_object() {
	jq -n --arg c "$1" --arg m "$2" '{message: $m, object: {type: "commit", sha: $c}}'
}

run() {
	PATH="$bin:$PATH" GITHUB_REPOSITORY=3xDevOps/Aether \
		sh "$script_dir/create-release-tag.sh" v0.9.0-beta.1 "$commit" 412 >"$tmp/out" 2>&1
}

created() { grep -q 'git/refs -f ref=refs/tags/v0.9.0-beta.1' "$STATE/log"; }

# A missing tag is created, annotated, at the commit with its build line.
reset
run || fail "creating a new tag: $(cat "$tmp/out")"
created || fail "no ref was created"
[ "$(cat "$STATE/ref")" = "tag tagobject" ] || fail "the ref does not name the annotated tag"
jq -e --arg c "$commit" '.object == {type: "commit", sha: $c}' "$STATE/tag.json" >/dev/null || fail "the tag does not name $commit"
jq -r .message "$STATE/tag.json" | grep -qx 'build: 412' || fail "the tag message has no build line"

# A re-run finds the tag its first attempt created and changes nothing.
: >"$STATE/log"
run || fail "re-running over its own tag: $(cat "$tmp/out")"
grep -q 'already names' "$tmp/out" || fail "a re-run did not report the existing tag"
! grep -q 'git/tags -f\|git/refs' "$STATE/log" || fail "a re-run wrote a tag"

# Any other tag of that name is refused, and nothing is written.
for case in lightweight other-commit other-build read-error; do
	reset
	case $case in
	lightweight) echo "commit $commit" >"$STATE/ref" ;;
	other-commit)
		echo "tag tagobject" >"$STATE/ref"
		tag_object "$other" "$(printf 'v0.9.0-beta.1\n\nbuild: 412')" >"$STATE/tag.json"
		;;
	other-build)
		echo "tag tagobject" >"$STATE/ref"
		tag_object "$commit" "$(printf 'v0.9.0-beta.1\n\nbuild: 4120')" >"$STATE/tag.json"
		;;
	read-error) touch "$STATE/readerr" ;;
	esac
	if run; then fail "$case: accepted"; fi
	! grep -q 'git/tags -f\|git/refs' "$STATE/log" || fail "$case: wrote a tag"
	grep -q 'create-release-tag:' "$tmp/out" || fail "$case: no error named the tag: $(cat "$tmp/out")"
done

# Losing a race to the same tag succeeds; losing it to another fails.
reset
tag_object "$commit" "$(printf 'v0.9.0-beta.1\n\nbuild: 412')" >"$STATE/race"
run || fail "a race with the same tag: $(cat "$tmp/out")"
reset
tag_object "$other" "$(printf 'v0.9.0-beta.1\n\nbuild: 412')" >"$STATE/race"
if run; then fail "a race with another commit's tag was accepted"; fi

echo "create-release-tag tests passed"
