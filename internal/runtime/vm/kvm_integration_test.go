//go:build integration

package vm

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// kvmEngine runs the engine as the server does: real systemd units, the
// server binary as monitor and helper, and the image store. It needs root
// on a KVM host with cloud-hypervisor and virtiofsd, plus:
//
//	AETHER_VM_TEST_DIR     data directory, on the filesystem the store uses
//	AETHER_VM_TEST_KERNEL  guest kernel
//	AETHER_VM_TEST_SERVER  aether-server built from this tree
func kvmEngine(t *testing.T) *engine {
	t.Helper()
	dir, kernel, server := os.Getenv("AETHER_VM_TEST_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	if dir == "" || kernel == "" || server == "" {
		t.Skip("set AETHER_VM_TEST_DIR, AETHER_VM_TEST_KERNEL and AETHER_VM_TEST_SERVER to boot real VMs")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil || os.Geteuid() != 0 {
		t.Skip("needs root and /dev/kvm")
	}
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	return newEngine(Config{DataDir: dir, Images: images, Monitor: []string{server, MonitorCommand}, Helper: server}, systemdUnits{}, dialHelper)
}

func vmRun(t *testing.T, e *engine, key, script string) (string, int) {
	t.Helper()
	code, out, errOut, err := e.exec(t.Context(), key, []string{"/bin/sh", "-c", script}, "")
	if err != nil {
		t.Fatalf("exec %q: %v", script, err)
	}
	return out + errOut, code
}

func readUntil(t *testing.T, att runtime.Attachment, want string) {
	t.Helper()
	found := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(att.Stdout())
		for sc.Scan() {
			if strings.Contains(sc.Text(), want) {
				found <- true
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("main's output ended before %q", want)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no %q from main", want)
	}
}

func TestKVMSessionLifecycle(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-a"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	src := filepath.Join(e.root, "..", "src-"+key)
	if err := os.MkdirAll(filepath.Join(src, "checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	if err := os.WriteFile(filepath.Join(src, "checkout", "hello"), []byte("from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ro"), []byte("read only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	coord := filepath.Join(src, "coord")
	if err := os.MkdirAll(coord, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(coord, "coord3.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for c, acceptErr := l.Accept(); acceptErr == nil; c, acceptErr = l.Accept() {
			_, _ = fmt.Fprintln(c, "host answered")
			_ = c.Close()
		}
	}()
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, TTY: true,
		Command:     []string{"/bin/sh", "-c", "echo main-started; exec sleep infinity"},
		SetupScript: "echo setup-ran >> /var/tmp/setup-log",
		Env:         map[string]string{"AETHER_RUN_ID": key, "HOME": "/root"},
		CPULimit:    1.5, MemoryLimitBytes: 1 << 30, PidsLimit: 512,
		WorktreeHostPath: filepath.Join(src, "checkout"), WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
		Mounts: []runtime.Mount{
			{HostPath: filepath.Join(src, "ro"), ContainerPath: "/opt/ro", ReadOnly: true},
			{HostPath: coord, ContainerPath: "/run/aether", ReadOnly: true},
		},
	}
	start := time.Now()
	id, err := e.create(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("create (base disk, root and data disks) took %s", time.Since(start))
	start = time.Now()
	if err = e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	t.Logf("start (unit, boot, setup, main) took %s", time.Since(start))
	att, err := e.attach(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, att, "main-started")
	_ = att.Close()

	for script, want := range map[string]string{
		"cat /workspace/hello":              "from host",
		"pwd":                               "/workspace",
		"cat /var/tmp/setup-log":            "setup-ran",
		"echo x >> /opt/ro || echo refused": "refused",
		"nproc":                             "2",
		"cat /proc/self/cgroup":             "session",
		"test -S /run/aether/coord3.sock && echo socket":       "socket",
		"getent hosts example.com >/dev/null && echo resolved": "resolved",
	} {
		if out, _ := vmRun(t, e, key, script); !strings.Contains(out, want) {
			t.Errorf("%s: %q, want %q", script, out, want)
		}
	}
	if out, code := vmRun(t, e, key, "echo from-guest > /workspace/written"); code != 0 {
		t.Fatalf("write to the checkout: %d %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(src, "checkout", "written")); string(data) != "from-guest\n" {
		t.Errorf("host sees %q", data)
	}

	after := kvmEngine(t)
	if found, err := after.find(key); err != nil || found != id {
		t.Fatalf("a new engine finds %q, %v", found, err)
	}
	if info, err := after.inspect(t.Context(), key); err != nil || info.Exited {
		t.Fatalf("a new engine inspects %+v, %v", info, err)
	}
	if out, _ := vmRun(t, after, key, "cat /var/tmp/setup-log | wc -l"); strings.TrimSpace(out) != "1" {
		t.Errorf("setup ran %s times before the engine restart", out)
	}
	start = time.Now()
	if err := after.stop(t.Context(), key, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop took %s", time.Since(start))
	if info, _ := after.inspect(t.Context(), key); !info.Exited {
		t.Errorf("stopped VM inspects as %+v", info)
	}
	if err := after.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if out, _ := vmRun(t, after, key, "cat /var/tmp/setup-log | wc -l"); strings.TrimSpace(out) != "1" {
		t.Errorf("a cold start kept the old root disk: setup log has %s lines", out)
	}
}

func TestKVMSaveRestoreKeepsProcesses(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-b"
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 2, MemoryLimitBytes: 1 << 30,
	}
	if _, err := e.create(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	before, _ := vmRun(t, e, key, "setsid sleep 4000 </dev/null >/dev/null 2>&1 & sleep 0.5; pgrep -x sleep | sort | tr '\\n' ' '; cat /proc/sys/kernel/random/boot_id")
	start := time.Now()
	if err := e.save(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	t.Logf("save took %s", time.Since(start))
	if up, _ := e.running(t.Context(), key); up {
		t.Fatal("the VM still runs after save")
	}
	start = time.Now()
	if err := e.restore(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	t.Logf("restore took %s", time.Since(start))
	after, _ := vmRun(t, e, key, "pgrep -x sleep | sort | tr '\\n' ' '; cat /proc/sys/kernel/random/boot_id")
	if before != after {
		t.Errorf("processes or boot changed across save and restore:\n%s\nvs\n%s", before, after)
	}
	t.Logf("same processes and boot after restore: %s", strings.TrimSpace(after))
}
