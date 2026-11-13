//go:build kvm

package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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
