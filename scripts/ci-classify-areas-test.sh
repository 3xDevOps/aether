#!/bin/sh
# ci-classify-areas.sh: each area is false only for paths it cannot see.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$script_dir/ci-classify-areas.sh"

expect() {
	mode=$1
	want=$2
	shift 2
	got=$(printf '%s\n' "$@" | sh "$script" "$mode")
	if [ "$got" != "$want" ]; then
		echo "ci-classify-areas-test: $mode [$*] gave $got, wanted $want" >&2
		exit 1
	fi
}

expect web true web/src/app.tsx
expect web true web/package.json
expect web true internal/store/a.go web/src/app.tsx
expect web true cmd/aether/main.go
expect web true internal/localgw/gateway.go
expect web false internal/store/store.go
expect web false cmd/aether-server/main.go docs/testing.md
expect web false android/app/build.gradle.kts desktop/main.js
expect web false docs/testing.md README.md
expect web true Makefile
expect web true go.mod
expect web true .github/workflows/ci.yml
expect web true scripts/ci-classify-areas.sh
expect web false scripts/install.sh
expect web true unknown/dir/file

expect go false web/src/app.tsx
expect go false web/package.json web/bun.lock
expect go false web/e2e/foo.spec.ts docs/testing.md
expect go false docs/testing.md README.md
expect go true web/embed.go
expect go true web/src/app.tsx internal/store/store.go
expect go true internal/store/store.go
expect go true Makefile
expect go true go.mod
expect go true .github/workflows/ci.yml
expect go true .github/actions/go-cache/action.yml
expect go true scripts/install.sh
expect go false android/README.md
expect go true unknown/dir/file

expect caches false internal/store/store.go web/src/app.tsx docs/testing.md
expect caches false android/app/src/main/AndroidManifest.xml images
expect caches true internal/store/store.go go.sum
expect caches true .github/workflows/ci.yml
expect caches true images/browser/Dockerfile
expect caches true web/public/fonts/LICENSE-jetbrains-mono-nfm.txt
expect caches true web/package.json
expect caches true desktop/package-lock.json
expect caches true android/app/build.gradle.kts
expect caches true android/gradle/libs.versions.toml

expect vm true internal/runtime/vm/engine.go
expect vm true internal/guest/helper/main_linux.go
expect vm true internal/vmimage/store.go
expect vm true images/kernel/config-x86_64
expect vm true internal/scheduler/vm_sessions.go
expect vm true internal/server/vmruntime.go
expect vm true internal/sshd/sync_vm.go
expect vm true internal/runtime/managed_exec.go
expect vm true internal/acpkeeper/term_linux.go
expect vm true internal/egress/proxy.go
expect vm true internal/gitengine/checkout_vm.go
expect vm true internal/egress/proxy_vm_test.go
expect vm true docs/testing.md go.sum
expect vm true vm.mk
expect vm true .github/workflows/kvm.yml
expect vm false internal/runtime/docker.go internal/runtime/runtime.go internal/scheduler/environment.go
expect vm false cmd/aether-server/main.go
expect vm false internal/scheduler/envmerge.go web/src/app.tsx docs/testing.md
expect vm false images/standard/Dockerfile .github/workflows/ci-suite.yml Makefile

# Every package the KVM tests compile is in the vm list or here, with the
# reason the merge queue need not boot VMs for it, so a new dependency of
# the VM code is a decision rather than a silent skip.
not_vm='
internal/runtime: the Docker runtime; the vm list names the files the VM engine shares
internal/coordtransport: the VM engine reads two of its constants
internal/protocol: reached only through coordtransport
internal/store: reached only through coordtransport
internal/domain: data types; the gitengine and guest helper unit tests cover how the guest uses them
internal/edge/proto: reached only through coordtransport
internal/secretscan: reached only through coordtransport
internal/events: data types; the gitengine and guest helper unit tests cover how the guest uses them
internal/overlay: the server side of live sync; only the unit tests of the guest helper use it, without a VM
'
cd "$script_dir/.."
kvm_files=$(grep -rl --include='*_test.go' -E '^//go:build .*kvm' internal)
for file in $kvm_files; do
	expect vm true "$file"
done
kvm_pkgs=$(printf '%s\n' "$kvm_files" | sed 's|/[^/]*$||; s|^|./|' | sort -u)
# An assignment, so set -e stops the test when go list fails.
deps=$(GOOS=linux go list -deps -test -tags kvm -f '{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}' $kvm_pkgs)
for dir in $(printf '%s\n' "$deps" | sort -u); do
	pkg=${dir#"$PWD"/}
	case $not_vm in *"
$pkg: "*) continue ;;
	esac
	if [ "$(printf '%s/x.go\n' "$pkg" | sh "$script" vm)" != true ]; then
		echo "ci-classify-areas-test: the KVM tests compile $pkg; add it to vm in ci-classify-areas.sh, or to not_vm here with the reason it needs no VM run" >&2
		exit 1
	fi
done

for mode in web go caches vm; do
	got=$(sh "$script" "$mode" </dev/null)
	if [ "$got" != true ]; then
		echo "ci-classify-areas-test: $mode empty stdin gave $got, wanted true" >&2
		exit 1
	fi
	got=$(printf '\n' | sh "$script" "$mode")
	if [ "$got" != true ]; then
		echo "ci-classify-areas-test: $mode blank line gave $got, wanted true" >&2
		exit 1
	fi
done
