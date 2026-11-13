#!/bin/sh
# Builds the Aether guest kernel, an uncompressed vmlinux that Cloud
# Hypervisor boots through PVH, into <output file>. make kernel runs this in a
# pinned container; the same inputs give the same bytes.
set -eu

version=6.18.63
sha256=81764a545072d160748beff4d9de2e7d95aa9f16e8261110288f067a2cc61d71

[ $# -eq 1 ] || { echo "usage: $0 <output file>" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$(dirname "$1")"
out=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
tarball=${KERNEL_CACHE:-/tmp}/linux-$version.tar.xz
# A fixed source path keeps paths the build records identical.
work=/tmp/aether-kernel

if ! echo "$sha256  $tarball" | sha256sum -c --status 2>/dev/null; then
	mkdir -p "$(dirname "$tarball")"
	curl -fsSL -o "$tarball.part" "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-$version.tar.xz"
	echo "$sha256  $tarball.part" | sha256sum -c --quiet
	mv "$tarball.part" "$tarball"
fi
rm -rf "$work"
mkdir -p "$work"
tar -C "$work" -xJf "$tarball"
cd "$work/linux-$version"

export KBUILD_BUILD_TIMESTAMP="1970-01-01 00:00:00 UTC" KBUILD_BUILD_USER=aether KBUILD_BUILD_HOST=aether KBUILD_BUILD_VERSION=1
make -s KCONFIG_ALLCONFIG="$here/config-x86_64" allnoconfig
missing=$(grep -v '^#' "$here/config-x86_64" | grep . | grep -vxF -f .config || true)
if [ -n "$missing" ]; then
	echo "build.sh: these options did not reach .config; enable what they depend on:" >&2
	echo "$missing" >&2
	exit 1
fi
make -s -j"$(nproc)" KCFLAGS=-Wa,-mx86-used-note=no vmlinux
objcopy --strip-all --remove-section=.comment vmlinux "$out"
rm -rf "$work"
