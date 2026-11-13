//go:build kvm

package vm

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/3xDevOps/Aether/internal/guest"
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
	id, err := e.create(t.Context(), spec.CreationKey, spec, options{})
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
		"awk '/NoNewPrivs/{print \"nnp=\" $2}' /proc/self/status": "nnp=1",
		"cat /proc/self/cgroup":                                   "session",
		"test -S /run/aether/coord3.sock && echo socket":          "socket",
		"getent hosts example.com >/dev/null && echo resolved":    "resolved",
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
	if _, err := e.create(t.Context(), spec.CreationKey, spec, options{}); err != nil {
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

func kvmSession(t *testing.T, e *engine, key string, mounts ...runtime.Mount) {
	t.Helper()
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20, Mounts: mounts,
	}
	if _, err := e.create(t.Context(), spec.CreationKey, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}

// TestKVMRebootNow is what TestKVMGuestRebootEndsTheSession runs inside the
// guest, from its own binary: the guest kernel has no SysRq.
func TestKVMRebootNow(t *testing.T) {
	if os.Getenv("AETHER_VM_TEST_REBOOT_NOW") == "" {
		t.Skip("runs inside a guest only")
	}
	t.Fatal(unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART))
}

// TestKVMRootSessionStartsFromTheFileService starts, in a Root session's
// VM with one CPU, a program from the file service and a process whose
// working directory is on it.
func TestKVMRootSessionStartsFromTheFileService(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-root"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.destroy(context.Background(), key) })
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: key, User: "1000:1000", Sudo: true,
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"}, Env: map[string]string{"AETHER_RUN_ID": key},
		CPULimit: 1, MemoryLimitBytes: 512 << 20,
		Mounts: []runtime.Mount{
			{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true},
			{HostPath: t.TempDir(), ContainerPath: "/mnt/host"},
		},
	}
	if _, err = e.create(t.Context(), key, spec, options{}); err != nil {
		t.Fatal(err)
	}
	if err = e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		argv []string
		dir  string
	}{
		{[]string{"/opt/aether-vmtest", "-test.run", "^$"}, ""},
		{[]string{"/bin/true"}, "/mnt/host"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		code, _, errOut, execErr := e.exec(ctx, key, tc.argv, tc.dir)
		cancel()
		if execErr != nil || code != 0 {
			t.Errorf("%v in %q: %d, %v: %s", tc.argv, tc.dir, code, execErr, errOut)
		}
	}
}

// hogged keeps what TestKVMHogNow allocates until the kernel kills it.
var hogged [][]byte

func TestKVMHogNow(t *testing.T) {
	if os.Getenv("AETHER_VM_TEST_HOG_NOW") == "" {
		t.Skip("runs inside a guest only")
	}
	for {
		b := make([]byte, 8<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		hogged = append(hogged, b)
	}
}

// TestKVMSessionThatFillsTheVMIsKilled fills the VM from a program the file
// service serves, whose pages the helper must still be able to fault back in.
func TestKVMSessionThatFillsTheVMIsKilled(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-oom"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	kvmSession(t, e, key, runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := time.Now()
	code, _, errOut, err := e.exec(ctx, key, []string{"/bin/sh", "-c", "AETHER_VM_TEST_HOG_NOW=1 exec /opt/aether-vmtest -test.run '^TestKVMHogNow$'"}, "")
	if err != nil || code != 128+int(unix.SIGKILL) {
		t.Fatalf("the program that filled the VM ended with %d, %v after %s: %s", code, err, time.Since(start), errOut)
	}
	t.Logf("the kernel killed the program %s after it started", time.Since(start))
	out, _ := vmRun(t, e, key, "grep '^oom ' /sys/fs/cgroup/session/memory.events; grep MemTotal /proc/meminfo; cat /sys/fs/cgroup/session/memory.max; grep VmHWM /proc/1/status")
	if !strings.Contains(out, "oom ") || strings.Contains(out, "oom 0") {
		t.Errorf("the session cgroup's own OOMs and memory: %q", out)
	}
	t.Logf("session cgroup and helper: %q", out)
	if _, code := vmRun(t, e, key, "/opt/aether-vmtest -test.run '^$'"); code != 0 {
		t.Errorf("the file service's program after the kill exited %d", code)
	}
}

func TestKVMHoldNow(t *testing.T) {
	var n int
	if _, err := fmt.Sscan(os.Getenv("AETHER_VM_TEST_HOLD_NOW"), &n); err != nil {
		t.Skip("runs inside a guest only")
	}
	b := make([]byte, n)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	hogged = append(hogged, b)
	fmt.Println("held")
	time.Sleep(time.Hour)
}

// TestKVMFileServiceWritesAtTheCap writes to the file service from many
// processes while the session is near its memory cap: what the helper holds
// for them is outside the cap.
func TestKVMFileServiceWritesAtTheCap(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-writes"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	written := t.TempDir()
	kvmSession(t, e, key, runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true}, runtime.Mount{HostPath: written, ContainerPath: "/mnt/h"})
	out, _ := vmRun(t, e, key, "echo $(( $(cat /sys/fs/cgroup/session/memory.max) - $(cat /sys/fs/cgroup/session/memory.current) - (96 << 20) ))")
	_, att, err := e.spawn(t.Context(), key, guest.ProcessSpec{
		ID: "hold", Argv: []string{"/opt/aether-vmtest", "-test.run", "^TestKVMHoldNow$"}, Env: []string{"AETHER_VM_TEST_HOLD_NOW=" + strings.TrimSpace(out)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = att.Close() }()
	readUntil(t, att, "held")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	code, stdout, errOut, err := e.exec(ctx, key, []string{"/bin/sh", "-c", "for i in $(seq 64); do dd if=/dev/zero of=/mnt/h/f$i bs=1M count=16 status=none & pids=\"$pids $!\"; done; for p in $pids; do wait $p || exit 1; done; grep VmHWM /proc/1/status"}, "")
	if err != nil || code != 0 {
		t.Fatalf("64 writers at the cap: %d, %v after %s: %s", code, err, time.Since(start), errOut)
	}
	t.Logf("64 writers at the cap took %s; helper %s", time.Since(start), strings.TrimSpace(stdout))
	for i := 1; i <= 64; i++ {
		if info, statErr := os.Stat(filepath.Join(written, fmt.Sprint("f", i))); statErr != nil || info.Size() != 16<<20 {
			t.Errorf("writer %d's file on the host: %v, %v", i, info, statErr)
		}
	}
}

func TestKVMGuestRebootEndsTheSession(t *testing.T) {
	e := kvmEngine(t)
	key := "run-vmtest-e"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	kvmSession(t, e, key, runtime.Mount{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true})
	waited := make(chan runtime.ExitStatus, 1)
	go func() {
		st, _ := e.wait(context.Background(), key)
		waited <- st
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if _, _, _, execErr := e.exec(ctx, key, []string{"/bin/sh", "-c", "AETHER_VM_TEST_REBOOT_NOW=1 /opt/aether-vmtest -test.run '^TestKVMRebootNow$'"}, ""); execErr != nil {
		t.Logf("the exec that reboots the guest ended with: %v", execErr)
	}
	select {
	case st := <-waited:
		if st.Code != lostExitCode {
			t.Errorf("wait after the guest rebooted = %+v", st)
		}
		t.Logf("the session ended %s after the guest rebooted", time.Since(start))
	case <-time.After(30 * time.Second):
		t.Fatal("a guest that rebooted on its own still counts as running")
	}
	if up, _ := e.running(t.Context(), key); up {
		t.Error("the rebooted VM still runs")
	}
}

func TestKVMSaveEnvironmentWhileTheTerminalRuns(t *testing.T) {
	e := kvmEngine(t)
	r := &Runtime{vms: e}
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: "terminal:i9a", TTY: true,
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"}, Env: map[string]string{"HOME": "/root"},
		CPULimit: 2, MemoryLimitBytes: 1 << 30,
	}
	name, opts, _ := vmFor(spec)
	// Offline keeps the test off the host's shared VM firewall chain.
	opts.Offline = true
	const saved, session = "aether/i9-saved:1", "run-i9-saved"
	t.Cleanup(func() {
		_ = e.destroy(context.Background(), name)
		_ = e.destroy(context.Background(), session)
		_ = e.cfg.Images.Untag(saved)
	})
	if _, err := e.create(t.Context(), name, spec, opts); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	// No sync: the freeze must flush what the guest has not written yet.
	if out, code := vmRun(t, e, name, "dd if=/dev/urandom of=/opt/payload bs=1M count=256 status=none && sha256sum /opt/payload > /opt/payload.sum"); code != 0 {
		t.Fatalf("write the payload: %d %s", code, out)
	}
	before, _ := vmRun(t, e, name, "setsid sh -c 'while :; do date >> /var/log/ticker; done' </dev/null >/dev/null 2>&1 & setsid sleep 4000 </dev/null >/dev/null 2>&1 & sleep 0.5; pgrep -x sleep | sort | tr '\\n' ' '")
	start := time.Now()
	if err := r.Commit(t.Context(), runtime.ID(idPrefix+name), saved, map[string]string{"aether.previous-image": "standard"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("save while running took %s", time.Since(start))
	after, _ := vmRun(t, e, name, "pgrep -x sleep | sort | tr '\\n' ' '")
	if before != after {
		t.Errorf("processes changed across the save: %q vs %q", before, after)
	}
	if out, _ := vmRun(t, e, name, "a=$(wc -l < /var/log/ticker); sleep 1; b=$(wc -l < /var/log/ticker); [ $b -gt $a ] && echo writable"); !strings.Contains(out, "writable") {
		t.Errorf("the root filesystem stayed frozen: %q", out)
	}
	if labels, err := r.ImageLabels(t.Context(), saved); err != nil || labels["aether.previous-image"] != "standard" {
		t.Errorf("labels = %v, %v", labels, err)
	}
	start = time.Now()
	if err := r.CheckImage(t.Context(), saved, "1000:1000"); err != nil {
		t.Fatalf("check the saved image: %v", err)
	}
	t.Logf("boot check took %s", time.Since(start))

	kvmSessionFrom(t, e, session, saved)
	if out, code := vmRun(t, e, session, "sha256sum -c /opt/payload.sum"); code != 0 {
		t.Errorf("the saved image lost the payload: %s", out)
	}
	ext4, _ := vmRun(t, e, session, "dmesg | grep 'EXT4-fs (vdb)'")
	if !strings.Contains(ext4, "mounted filesystem") || strings.Contains(ext4, "recovery") || strings.Contains(strings.ToLower(ext4), "error") {
		t.Errorf("the saved root filesystem was not mounted clean:\n%s", ext4)
	}
	t.Logf("the saved root filesystem mounted clean: %s", strings.TrimSpace(ext4))

	if err := e.stop(t.Context(), name, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	if out, code := vmRun(t, e, name, "sha256sum -c /opt/payload.sum"); code != 0 {
		t.Errorf("a terminal's cold start lost its root disk: %s", out)
	}
}

func TestKVMSavedImageThatDoesNotBootFailsTheCheck(t *testing.T) {
	e := kvmEngine(t)
	r := &Runtime{vms: e}
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: "terminal:i9b",
		Command: []string{"/bin/sh", "-c", "exec sleep infinity"}, CPULimit: 1, MemoryLimitBytes: 512 << 20,
	}
	name, opts, _ := vmFor(spec)
	opts.Offline = true
	const saved = "aether/i9-broken:1"
	t.Cleanup(func() {
		_ = e.destroy(context.Background(), name)
		_ = e.cfg.Images.Untag(saved)
	})
	if _, err := e.create(t.Context(), name, spec, opts); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	vmRun(t, e, name, "rm -f /bin/sh /usr/bin/sh /bin/dash /usr/bin/dash")
	if err := r.Commit(t.Context(), runtime.ID(idPrefix+name), saved, nil); err != nil {
		t.Fatal(err)
	}
	err := r.CheckImage(t.Context(), saved, "1000:1000")
	if !errors.Is(err, runtime.ErrUnstartableImage) {
		t.Fatalf("check of an image without a shell = %v", err)
	}
	t.Logf("the check refused it: %v", err)
	if keys, _ := e.keys(); slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, "check-") }) {
		t.Errorf("the check left its VM: %v", keys)
	}
}

func kvmSessionFrom(t *testing.T, e *engine, key, image string) {
	t.Helper()
	spec := runtime.Spec{
		Image: image, CreationKey: key, Command: []string{"/bin/sh", "-c", "exec sleep infinity"},
		Env: map[string]string{"AETHER_RUN_ID": key}, CPULimit: 1, MemoryLimitBytes: 512 << 20,
	}
	if _, err := e.create(t.Context(), key, spec, options{Offline: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}

// TestKVMDialNow is what TestKVMAssistantVM runs inside the guest: it reads
// a line from the unix socket AETHER_VM_TEST_DIAL names.
func TestKVMDialNow(t *testing.T) {
	path := os.Getenv("AETHER_VM_TEST_DIAL")
	if path == "" {
		t.Skip("runs inside a guest only")
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	fmt.Print("dialed: " + line)
}

// TestKVMAssistantVM boots a VM with the assistant's mounts: its home, the
// read-only instructions, install and empty policy directories, the server
// binary and the tool socket's directory, and runs its adapter the way the
// assistant does, as a managed exec.
func TestKVMAssistantVM(t *testing.T) {
	e := kvmEngine(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name, opts, _ := vmFor(runtime.Spec{CreationKey: "assistant:i9"})
	opts.Offline = true
	t.Cleanup(func() { _ = e.destroy(context.Background(), name) })
	src := filepath.Join(e.root, "..", "src-"+name)
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	dirs := map[string]string{}
	for _, d := range []string{"home", "instructions", "install", "policy", "tools"} {
		dirs[d] = filepath.Join(src, d)
		if err = os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(dirs["instructions"], "AGENTS.md"), []byte("be helpful\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(dirs["home"], 1000, 1000); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(dirs["tools"], "assistant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for c, acceptErr := l.Accept(); acceptErr == nil; c, acceptErr = l.Accept() {
			_, _ = fmt.Fprintln(c, "tool answered")
			_ = c.Close()
		}
	}()
	server := os.Getenv("AETHER_VM_TEST_SERVER")
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: "assistant:i9", User: "1000:1000",
		Command: []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done"},
		Env:     map[string]string{"HOME": "/home/aether"}, WorkingDir: "/var/lib/aether-assistant",
		CPULimit: 1, MemoryLimitBytes: 1 << 30,
		Mounts: []runtime.Mount{
			{HostPath: dirs["home"], ContainerPath: "/home/aether"},
			{HostPath: dirs["instructions"], ContainerPath: "/var/lib/aether-assistant", ReadOnly: true},
			{HostPath: dirs["install"], ContainerPath: "/opt/aether-assistant", ReadOnly: true},
			{HostPath: dirs["policy"], ContainerPath: "/etc/claude-code", ReadOnly: true},
			{HostPath: dirs["policy"], ContainerPath: "/etc/codex", ReadOnly: true},
			{HostPath: server, ContainerPath: "/usr/local/bin/aether-internal", ReadOnly: true},
			{HostPath: dirs["tools"], ContainerPath: "/run/aether-assistant", ReadOnly: true},
			{HostPath: self, ContainerPath: "/opt/aether-vmtest", ReadOnly: true},
		},
	}
	if _, err = e.create(t.Context(), name, spec, opts); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = e.start(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	t.Logf("the assistant VM started in %s", time.Since(start))
	for script, want := range map[string]string{
		"cat AGENTS.md": "be helpful",
		"echo signed-in > /home/aether/.credentials && echo wrote":                                                 "wrote",
		"touch /etc/claude-code/managed-settings.json 2>&1 || echo refused":                                        "refused",
		"AETHER_VM_TEST_DIAL=/run/aether-assistant/assistant.sock /opt/aether-vmtest -test.run '^TestKVMDialNow$'": "dialed: tool answered",
	} {
		if out, _ := vmRun(t, e, name, script); !strings.Contains(out, want) {
			t.Errorf("%s: %q, want %q", script, out, want)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dirs["home"], ".credentials")); string(data) != "signed-in\n" {
		t.Errorf("the host home has %q", data)
	}
	m, err := e.startManagedExec(t.Context(), name, runtime.ExecSpec{
		Argv: []string{"/bin/sh", "-c", "read line; echo adapter got $line; exit 3"}, CreationKey: "assistant-i9-exec", WorkingDir: "/var/lib/aether-assistant",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	att := m.Attachment()
	if _, err = att.Stdin().Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, att, "adapter got hello")
	if st, err := m.Wait(t.Context()); err != nil || st.Code != 3 {
		t.Errorf("the adapter's exit = %+v, %v", st, err)
	}
	_ = m.Detach()
}

// TestKVMUpdaterVM boots a VM with the agent updater's mounts: the member's
// home writable, a sign-in hidden behind a read-only file inside it, and
// runs an update there.
func TestKVMUpdaterVM(t *testing.T) {
	e := kvmEngine(t)
	name, opts, _ := vmFor(runtime.Spec{CreationKey: "harness-update-i9-claude"})
	opts.Offline = true
	t.Cleanup(func() { _ = e.destroy(context.Background(), name) })
	src := filepath.Join(e.root, "..", "src-"+name)
	t.Cleanup(func() { _ = os.RemoveAll(src) })
	home := filepath.Join(src, "home")
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(src, "no-login")
	if err := os.WriteFile(empty, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{home, filepath.Join(home, ".local"), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".credentials.json")} {
		if err := os.Chown(p, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	spec := runtime.Spec{
		Image: "docker.io/library/ubuntu:24.04", CreationKey: "harness-update-i9-claude", User: "1000:1000",
		Command: []string{"/bin/sh", "-c", "sleep 900"}, Env: map[string]string{"HOME": "/root"}, WorkingDir: "/root",
		CPULimit: 1, MemoryLimitBytes: 1 << 30,
		Mounts: []runtime.Mount{
			{HostPath: home, ContainerPath: "/root"},
			{HostPath: empty, ContainerPath: "/root/.credentials.json", ReadOnly: true},
		},
	}
	if _, err := e.create(t.Context(), name, spec, opts); err != nil {
		t.Fatal(err)
	}
	if err := e.start(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	update := `printf '#!/bin/sh\necho 2.0.0\n' > ~/.local/bin/claude && chmod +x ~/.local/bin/claude && ~/.local/bin/claude --version; cat ~/.credentials.json | wc -c; echo x > ~/.credentials.json 2>&1 || echo hidden`
	out, code := vmRun(t, e, name, update)
	if code != 0 || !strings.Contains(out, "2.0.0") || !strings.Contains(out, "0\n") || !strings.Contains(out, "hidden") {
		t.Errorf("update: %d %q", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".local", "bin", "claude")); !strings.Contains(string(data), "2.0.0") {
		t.Errorf("the host home has the CLI %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".credentials.json")); string(data) != "secret" {
		t.Errorf("the sign-in changed to %q", data)
	}
}

// TestKVMPermissionTiers checks what the tiers rest on in a VM: a command a
// person allowed runs as root through the helper, a Root session's user may
// gain privileges through set-user-ID programs and has the sudoers drop-in,
// and a Default session's user may not.
func TestKVMPermissionTiers(t *testing.T) {
	e := kvmEngine(t)
	server := os.Getenv("AETHER_VM_TEST_SERVER")
	for _, tc := range []struct {
		key        string
		sudo       bool
		euid, nnp  string
		sudoersHas string
	}{
		{"run-i9-default", false, "1000", "NoNewPrivs:\t1", ""},
		{"run-i9-root", true, "0", "NoNewPrivs:\t0", "#1000 ALL=(ALL:ALL) NOPASSWD: ALL"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Cleanup(func() { _ = e.destroy(context.Background(), tc.key) })
			spec := runtime.Spec{
				Image: "docker.io/library/ubuntu:24.04", CreationKey: tc.key, User: "1000:1000", Sudo: tc.sudo,
				Command: []string{"/bin/sh", "-c", "exec sleep infinity"}, Env: map[string]string{"AETHER_RUN_ID": tc.key, "HOME": "/root"},
				CPULimit: 1, MemoryLimitBytes: 512 << 20,
				Mounts: []runtime.Mount{{HostPath: server, ContainerPath: "/usr/local/bin/aether-internal", ReadOnly: true}},
			}
			if _, err := e.create(t.Context(), tc.key, spec, options{Offline: true}); err != nil {
				t.Fatal(err)
			}
			if err := e.start(t.Context(), tc.key); err != nil {
				t.Fatal(err)
			}
			var out, errOut strings.Builder
			code, err := e.execAttached(t.Context(), tc.key, runtime.AttachedExec{
				Argv: []string{"/bin/sh", "-c", "cp /usr/bin/id /usr/local/bin/suid-id && chmod 4755 /usr/local/bin/suid-id && id -u"},
				User: "0:0", Stdout: &out, Stderr: &errOut,
			})
			if err != nil || code != 0 || strings.TrimSpace(out.String()) != "0" {
				t.Fatalf("the allowed command as root: %d %v %q %q", code, err, out.String(), errOut.String())
			}
			if got, _ := vmRun(t, e, tc.key, "/usr/local/bin/suid-id -u"); strings.TrimSpace(got) != tc.euid {
				t.Errorf("a set-user-ID root program runs as %q, want %s", got, tc.euid)
			}
			if got, _ := vmRun(t, e, tc.key, "grep NoNewPrivs /proc/self/status"); !strings.Contains(got, tc.nnp) {
				t.Errorf("the user's process has %q, want %q", got, tc.nnp)
			}
			var sudoersOut strings.Builder
			if _, err = e.execAttached(t.Context(), tc.key, runtime.AttachedExec{
				Argv: []string{"/bin/sh", "-c", "cat /etc/sudoers.d/aether 2>/dev/null; true"}, User: "0:0", Stdout: &sudoersOut, Stderr: &errOut,
			}); err != nil {
				t.Fatal(err)
			}
			if sudoers := sudoersOut.String(); tc.sudoersHas == "" && sudoers != "" || !strings.Contains(sudoers, tc.sudoersHas) {
				t.Errorf("sudoers drop-in %q, want %q", sudoers, tc.sudoersHas)
			}
		})
	}
}

// TestKVMEngineNow is the stand-in dictation engine TestKVMDictationVM runs
// in the guest, as nobody: it answers every connection to its socket. With
// an argument after --, it exits with that status instead.
func TestKVMEngineNow(t *testing.T) {
	if os.Getuid() != 65534 {
		t.Skip("runs inside a guest only")
	}
	if args := flag.Args(); len(args) > 0 {
		code, _ := strconv.Atoi(args[0])
		os.Exit(code)
	}
	fmt.Println("engine loading")
	l, err := net.Listen("unix", "/run/dictation/engine.sock")
	if err != nil {
		t.Fatal(err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintln(c, "engine says hi")
		_ = c.Close()
	}
}

type outputBuffer struct {
	mu     sync.Mutex
	buf    strings.Builder
	closed bool
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *outputBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *outputBuffer) String() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.closed
}

// TestKVMDictationVM runs a stand-in engine the way the dictation service
// runs the real one, through the full runtime with Docker beside it.
func TestKVMDictationVM(t *testing.T) {
	e := kvmEngine(t)
	docker, err := runtime.NewDocker()
	if err != nil {
		t.Skipf("needs Docker: %v", err)
	}
	r := &Runtime{Docker: docker, vms: e}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(e.root, "..", "dictation-sock")
	if err = os.MkdirAll(sock, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	start := func(args ...string) (runtime.ID, *outputBuffer) {
		t.Helper()
		out := &outputBuffer{}
		id, startErr := r.StartDictation(t.Context(), runtime.DictationSpec{
			Owner: "i9test", Image: "docker.io/library/ubuntu:24.04", User: "65534:65534",
			Entrypoint: append([]string{"/aether-dictation", "-test.run", "^TestKVMEngineNow$", "--"}, args...),
			ReadOnly:   map[string]string{self: "/aether-dictation"},
			Writable:   map[string]string{sock: "/run/dictation"}, Socket: "engine.sock",
			CPUs: 1, MemoryBytes: 1 << 30, Output: out,
		})
		if startErr != nil {
			t.Fatal(startErr)
		}
		return id, out
	}
	t.Cleanup(func() { _ = r.RemoveDictation(context.Background(), "i9test") })
	began := time.Now()
	id, out := start()
	var line string
	for deadline := time.Now().Add(30 * time.Second); line == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c, dialErr := net.Dial("unix", filepath.Join(sock, "engine.sock")); dialErr == nil {
			line, _ = bufio.NewReader(c).ReadString('\n')
			_ = c.Close()
		}
	}
	if line != "engine says hi\n" {
		t.Fatalf("the host socket answered %q", line)
	}
	t.Logf("the engine answered on the host socket %s after start", time.Since(began))
	if st, stateErr := r.DictationState(t.Context(), id); stateErr != nil || !st.Running {
		t.Errorf("state of a running engine = %+v, %v", st, stateErr)
	}
	if got, _ := out.String(); !strings.Contains(got, "engine loading") {
		t.Errorf("output = %q", got)
	}
	if links, _ := vmRun(t, e, dictationVM("i9test"), "ls /sys/class/net"); strings.TrimSpace(links) != "lo" {
		t.Errorf("the engine's VM has network links %q, want only lo", links)
	}
	if err = r.RemoveDictation(t.Context(), "i9test"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(sock, "engine.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the host socket outlived the engine: %v", err)
	}

	id, out = start("3")
	var st runtime.DictationState
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if st, err = r.DictationState(t.Context(), id); err != nil || !st.Running {
			break
		}
	}
	if err != nil || st.Running || st.ExitCode != 3 || st.OOMKilled {
		t.Errorf("state of an engine that exited 3 = %+v, %v", st, err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, closed := out.String(); closed {
			break
		}
	}
	if _, closed := out.String(); !closed {
		t.Error("the output stayed open after the engine exited")
	}
}
