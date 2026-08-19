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

cat >"$bin/gh" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$GH_LOG"
case "$1 $2" in
"release view") [ "${GH_EXISTING:-0}" = 1 ] ;;
"release create" | "release upload") exit 0 ;;
*) exit 2 ;;
esac
EOF
chmod 0755 "$bin/gh"
export GH_LOG="$log"
export PATH="$bin:$PATH"

tag="v0.1.0-test"

sh "$script_dir/publish-release.sh" "$tag" "$dist"

expected="$tmp/expected-create.log"
printf '%s\n' \
	"release view $tag" \
	"release create $tag --title $tag --generate-notes $dist/aether-linux-amd64 $dist/checksums.txt" \
	>"$expected"
cmp -s "$expected" "$log"

: >"$log"
export GH_EXISTING=1
sh "$script_dir/publish-release.sh" "$tag" "$dist"

printf '%s\n' \
	"release view $tag" \
	"release upload $tag --clobber $dist/aether-linux-amd64 $dist/checksums.txt" \
	>"$expected"
cmp -s "$expected" "$log"
