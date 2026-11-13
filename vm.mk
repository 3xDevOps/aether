# The guest kernel session VMs boot and the tests that boot VMs. The
# Makefile includes this file; docs/testing.md, "VM tests".

.PHONY: kernel test-kvm

# The guest kernel session VMs boot (images/kernel). It builds in a Debian
# pinned by digest whose packages come from a dated snapshot, so a rebuild
# gives the same bytes. The kernel source tarball is cached outside the tree.
KERNEL_IMAGE    := debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
KERNEL_SNAPSHOT := 20261110T000000Z
KERNEL_CACHE    ?= $(HOME)/.cache/aether/kernel
KERNEL_PACKAGES := bc bison ca-certificates curl flex gcc libc6-dev libelf-dev make xz-utils

# The packages whose test files carry the `kvm` build tag, minus KVM_SKIP.
# Assigned lazily - only `test-kvm` reads it.
KVM_PKGS = $(filter-out $(KVM_SKIP),$(shell \
  grep -rl --include='*_test.go' -E '^//go:build .*kvm' . \
  | xargs -n1 dirname | sed -e 's|^\./||' -e 's|^|./|' | sort -u))

# What `test-kvm` boots: the guest kernel `make kernel` builds and the image
# the test VMs run.
KVM_KERNEL ?= $(CURDIR)/$(DIST)/aether-vmlinux-x86_64
KVM_IMAGE  ?= ghcr.io/3xdevops/aether-standard:latest

# apt runs as root in the container; the outputs are handed back to the
# invoking user.
kernel:
	@mkdir -p $(DIST) '$(KERNEL_CACHE)'
	docker run --rm \
		-v '$(CURDIR)/images/kernel':/src:ro -v '$(CURDIR)/$(DIST)':/out -v '$(KERNEL_CACHE)':/cache \
		-e KERNEL_CACHE=/cache $(KERNEL_IMAGE) sh -euc '\
		printf "Types: deb\nURIs: http://snapshot.debian.org/archive/debian/$(KERNEL_SNAPSHOT)\nSuites: trixie\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\nCheck-Valid-Until: no\n" \
			> /etc/apt/sources.list.d/debian.sources; \
		apt-get update -qq; \
		DEBIAN_FRONTEND=noninteractive apt-get install -qq --no-install-recommends $(KERNEL_PACKAGES) >/dev/null; \
		sh /src/build.sh /out/aether-vmlinux-x86_64; \
		chown $(shell id -u):$(shell id -g) /out/aether-vmlinux-x86_64 /cache/*'
	@echo 'built $(DIST)/aether-vmlinux-x86_64'

# The `kvm`-tagged tests boot real Cloud Hypervisor VMs, so they need root
# on a host with /dev/kvm and `make kernel` first. The test binaries and the
# server are static because they also run inside the VMs. Only the TestKVM
# tests run: the helper's unit tests reap every child of the test binary,
# the VM monitor's too. KVM_RUN replaces the -run pattern. The server and
# the disks go in a new private directory: root starts that server, so no
# other user may be able to replace it.
test-kvm:
	@[ "$$(id -u)" = 0 ] && [ -c /dev/kvm ] || { echo 'make test-kvm: run it as root on a host with /dev/kvm' >&2; exit 1; }
	@for tool in cloud-hypervisor qemu-img mkfs.ext4 /usr/libexec/virtiofsd; do \
		command -v $$tool >/dev/null || { echo "make test-kvm: $$tool not found" >&2; exit 1; }; done
	@[ -s '$(KVM_KERNEL)' ] || { echo 'make test-kvm: no kernel at $(KVM_KERNEL); run make kernel or set KVM_KERNEL' >&2; exit 1; }
	dir=$$(mktemp -d "$${TMPDIR:-/tmp}/aether-kvm.XXXXXX") && trap 'rm -rf "$$dir"' EXIT && \
		mkdir "$$dir/engine" && CGO_ENABLED=0 go build -o "$$dir/aether-server" ./cmd/aether-server && \
		CGO_ENABLED=0 TMPDIR="$$dir" \
		AETHER_TEST_KERNEL='$(KVM_KERNEL)' AETHER_TEST_IMAGE='$(KVM_IMAGE)' \
		AETHER_VM_TEST_KERNEL='$(KVM_KERNEL)' AETHER_VM_TEST_DIR="$$dir/engine" \
		AETHER_VM_TEST_SERVER="$$dir/aether-server" \
		go test -count=1 -p 1 -timeout=30m -tags kvm -run '$(or $(KVM_RUN),^TestKVM)' $(KVM_PKGS)
