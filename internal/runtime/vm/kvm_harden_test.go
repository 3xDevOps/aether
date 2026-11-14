//go:build kvm

package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/runtime"
)

// TestKVMGuestKernelIsHardened checks the boot-time kernel settings that
// keep a Default-tier uid-1000 agent from escalating in the VM: unprivileged
// BPF is off and latched, io_uring is gone, kernel pointers and dmesg are
// hidden, and ptrace is scoped. The values come from
// images/kernel/config-x86_64 and the helper's harden().
func TestKVMGuestKernelIsHardened(t *testing.T) {
	e := kvmEngine(t)
	key := "run-harden-sysctls"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, User: "1000:1000",
		Command:  []string{"/bin/sh", "-c", "echo up; exec sleep infinity"},
		Env:      map[string]string{"AETHER_RUN_ID": key, "HOME": "/root"},
		CPULimit: 1, MemoryLimitBytes: 512 << 20, PidsLimit: 256,
	}
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]string{
		"kptr_restrict": "2", "dmesg_restrict": "1", "yama/ptrace_scope": "1",
		"io_uring_disabled": "2", "unprivileged_bpf_disabled": "1",
	} {
		if out, _ := vmRun(t, e, key, "cat /proc/sys/kernel/"+s); strings.TrimSpace(out) != want {
			t.Errorf("kernel.%s is %q, want %q", s, strings.TrimSpace(out), want)
		}
	}
}

// TestKVMShareKeepsDeviceNodesOffTheHost checks what root in its own VM
// still cannot do: land a device node on the host through a virtio-fs
// checkout share (virtiofsd has no CAP_MKNOD), and read a kernel pointer
// (kptr_restrict=2 zeroes /proc/kallsyms for root too). The share is also
// mounted nosuid,nodev inside the guest.
func TestKVMShareKeepsDeviceNodesOffTheHost(t *testing.T) {
	e := kvmEngine(t)
	key := "run-harden-share"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	src := filepath.Join(e.root, "..", "src-"+key)
	checkout := filepath.Join(src, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	// The session runs as root (User ""), the "root in its own VM" case the
	// finding is about, so vmRun's exec is root too: a device node it creates
	// must be refused by virtiofsd, not just by the unprivileged user.
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key,
		Command:  []string{"/bin/sh", "-c", "echo up; exec sleep infinity"},
		Env:      map[string]string{"AETHER_RUN_ID": key, "HOME": "/root"},
		CPULimit: 1, MemoryLimitBytes: 512 << 20, PidsLimit: 256,
		WorktreeHostPath: checkout, WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
	}
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if out, _ := vmRun(t, e, key, "id -u"); strings.TrimSpace(out) != "0" {
		t.Fatalf("the guest exec is not root: %q", out)
	}
	if out, _ := vmRun(t, e, key, "grep ' /workspace ' /proc/self/mountinfo"); !strings.Contains(out, "nosuid") || !strings.Contains(out, "nodev") {
		t.Errorf("the checkout share is not nosuid,nodev: %q", out)
	}
	// virtiofsd has no CAP_MKNOD, so even root in the VM cannot land a device
	// node on the host through the share.
	if out, code := vmRun(t, e, key, "mknod /workspace/charnode c 1 3 2>&1"); code == 0 {
		t.Errorf("mknod in the share succeeded, want a failure: %q", out)
	}
	if fi, statErr := os.Lstat(filepath.Join(checkout, "charnode")); statErr == nil && fi.Mode()&os.ModeDevice != 0 {
		t.Errorf("the guest landed a %s device node on the host", fi.Mode())
	}
	// kptr_restrict=2 hides kernel pointers from everyone; root here reads a
	// zero address, where without it root would see the real one.
	if out, _ := vmRun(t, e, key, "grep ' commit_creds$' /proc/kallsyms"); !strings.Contains(out, "0000000000000000") {
		t.Errorf("kallsyms exposes a kernel pointer to VM root: %q", out)
	}
}
