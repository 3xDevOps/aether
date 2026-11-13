//go:build kvm

package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// The test binary is the VM's helper as PID 1, and its own client inside
// the VM for what the image may lack.
func init() {
	switch {
	case os.Getpid() == 1:
		os.Exit(Main(nil))
	case len(os.Args) == 3 && os.Args[1] == "kvm-connect":
		os.Exit(kvmConnect(os.Args[2]))
	case len(os.Args) == 2 && os.Args[1] == "kvm-hog":
		var held [][]byte
		for {
			chunk := make([]byte, 64<<20)
			for i := range chunk {
				chunk[i] = 1
			}
			held = append(held, chunk)
		}
	}
}

// kvmConnect sends a line to a socket and prints the answer, retrying while
// the socket refuses.
func kvmConnect(path string) int {
	for attempt := 1; attempt <= 50; attempt++ {
		conn, err := net.Dial("unix", path)
		if err != nil {
			fmt.Printf("attempt %d: %v\n", attempt, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_, _ = conn.Write([]byte("hello from the VM\n"))
		answer, _ := bufio.NewReader(conn).ReadString('\n')
		fmt.Print(answer)
		return 0
	}
	return 1
}

// echoSocket serves a host socket that answers every line with name, as
// the coordination server stands in for.
func echoSocket(t *testing.T, path, name string) net.Listener {
	t.Helper()
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			_, _ = fmt.Fprintf(conn, "%s answers %s\n", name, strings.TrimSpace(line))
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// TestKVMHelper boots an image under Cloud Hypervisor with this test binary
// as the helper and drives it through the client. It needs root, /dev/kvm,
// cloud-hypervisor, virtiofsd, qemu-img and mkfs.ext4, the kernel in
// AETHER_TEST_KERNEL and the image in AETHER_TEST_IMAGE (docs/testing.md).
func TestKVMHelper(t *testing.T) {
	kernel, image := os.Getenv("AETHER_TEST_KERNEL"), os.Getenv("AETHER_TEST_IMAGE")
	if kernel == "" || image == "" {
		t.Skip("set AETHER_TEST_KERNEL and AETHER_TEST_IMAGE")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	virtiofsd := "/usr/libexec/virtiofsd"
	for _, tool := range []string{"cloud-hypervisor", virtiofsd, "qemu-img", "mkfs.ext4"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	store, err := vmimage.Open(filepath.Join(dir, "images"), kernel, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.BaseDisk(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.NewRootDisk(ctx, base, filepath.Join(dir, "root.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.NewDataDisk(ctx, filepath.Join(dir, "data.raw"), 4<<30)
	if err != nil {
		t.Fatal(err)
	}
	helperDisk, err := store.HelperDisk(ctx, self, filepath.Join(dir, "helper.raw"))
	if err != nil {
		t.Fatal(err)
	}

	share := filepath.Join(dir, "share")
	for _, d := range []string{"run-aether", "workspace", "bin"} {
		if err := os.MkdirAll(filepath.Join(share, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(self, filepath.Join(share, "bin", "aether-internal")); err != nil {
		t.Fatal(err)
	}
	coord := filepath.Join(share, "run-aether", "coord3.sock")
	coordListener := echoSocket(t, coord, "coord")
	signing := filepath.Join(dir, "signing.sock")
	echoSocket(t, signing, "signing")
	vsockPath := filepath.Join(dir, "vsock.sock")
	for port, target := range map[int]string{1025: coord, 1026: signing} {
		if err := os.Symlink(target, fmt.Sprintf("%s_%d", vsockPath, port)); err != nil {
			t.Fatal(err)
		}
	}

	cfg := guest.Config{
		Hostname: "kvm-helper",
		Network:  &guest.Network{Address: netip.MustParsePrefix("10.203.9.2/24"), Gateway: netip.MustParseAddr("10.203.9.1")},
		Mounts: []guest.Mount{
			{Volume: guest.VolumeShare, Source: "workspace", Target: "/workspace"},
			{Volume: guest.VolumeShare, Source: "run-aether", Target: "/run/aether", ReadOnly: true},
			{Volume: guest.VolumeShare, Source: "bin/aether-internal", Target: "/usr/local/bin/aether-internal", ReadOnly: true},
			{Volume: guest.VolumeData, Source: "tmp", Target: "/tmp"},
		},
		Sockets: []guest.Socket{{Port: 1025, Path: "/run/aether/coord3.sock"}, {Port: 1026, Path: "/run/aether-signing/agent.sock"}},
	}
	cmdline, err := cfg.KernelArgs("console=ttyS0 root=/dev/vda ro rootfstype=ext4 init=/init")
	if err != nil {
		t.Fatal(err)
	}
	fsSocket := filepath.Join(dir, "virtiofs.sock")
	fs := exec.CommandContext(ctx, virtiofsd, "--socket-path="+fsSocket, "--shared-dir="+share, "--cache=auto")
	if err := fs.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Process.Kill(); _ = fs.Wait() }()
	for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(fsSocket); err == nil {
			break
		}
	}
	serial := filepath.Join(dir, "serial.log")
	vm := exec.CommandContext(ctx, "cloud-hypervisor", "--kernel", kernel, "--cmdline", cmdline,
		"--disk", diskArg(helperDisk, "helper"), diskArg(root, "root"), diskArg(data, "data"),
		"--cpus", "boot=2", "--memory", "size=1G,shared=on", "--fs", "tag="+guest.ShareTag+",socket="+fsSocket,
		"--vsock", "cid=3,socket="+vsockPath, "--net", "ip=10.203.9.1,mask=255.255.255.0",
		"--serial", "file="+serial, "--console", "off")
	vm.Stdout, vm.Stderr = os.Stderr, os.Stderr
	if err := vm.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- vm.Wait() }()
	defer func() {
		_ = vm.Process.Kill()
		if t.Failed() {
			console, _ := os.ReadFile(serial)
			t.Logf("console:\n%s", console)
		}
	}()

	client, err := guest.Dial(vsockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	start := time.Now()
	info, err := client.Ping(ctx)
	if err != nil || info.BootError != "" {
		t.Fatalf("ping: %+v, %v", info, err)
	}
	t.Logf("helper answered %v after the VM started", time.Since(start).Round(time.Millisecond))
	env := []string{"PATH=" + defaultPath}
	if err := client.Boot(ctx, guest.BootSpec{
		Main:         guest.ProcessSpec{Argv: []string{"/bin/sh", "-c", "echo main; exec sleep infinity"}, Env: env},
		SetupScript:  "echo set up > /tmp/setup",
		NestedDocker: true,
		PidsLimit:    128,
	}); err != nil {
		t.Fatal(err)
	}

	run := func(id, user string, argv ...string) (string, int) {
		t.Helper()
		p, err := client.Spawn(ctx, guest.ProcessSpec{ID: id, Argv: argv, Env: env, User: user})
		if err != nil {
			t.Fatal(err)
		}
		a := p.Attach(true)
		defer func() { _ = a.Close() }()
		out, _ := io.ReadAll(io.MultiReader(a.Stdout(), a.Stderr()))
		code, err := p.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	mounts, _ := run("mounts", "", "/bin/sh", "-c", `cat /tmp/setup; hostname; cat /proc/1/comm; cut -d" " -f5,6 /proc/self/mountinfo; echo pids.max=$(cat /sys/fs/cgroup/session/pids.max)`)
	for _, want := range []string{"set up\n", "kvm-helper\n", "/workspace rw,nosuid,nodev", "/run/aether ro,nosuid,nodev", "/usr/local/bin/aether-internal ro,nosuid,nodev", "/tmp rw", "/run/aether/coord3.sock", "pids.max=128"} {
		if !strings.Contains(mounts, want) {
			t.Errorf("the VM lacks %q:\n%s", want, mounts)
		}
	}

	if out, code := run("docker", "", "docker", "info", "--format", "{{.ServerVersion}} {{.CgroupVersion}}"); code != 0 || !strings.HasSuffix(strings.TrimSpace(out), " 2") {
		t.Errorf("nested dockerd: exit %d: %s", code, out)
	}
	ahead := time.Now().Add(time.Hour)
	if err := client.SyncClock(ctx, ahead); err != nil {
		t.Fatal(err)
	}
	out, _ := run("clock", "", "date", "+%s")
	if got, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err != nil || got < ahead.Unix()-5 || got > ahead.Unix()+60 {
		t.Errorf("the clock was set to %d, the VM says %s", ahead.Unix(), out)
	}
	if err := client.SyncClock(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	shell, err := client.Spawn(ctx, guest.ProcessSpec{ID: "shell", Argv: []string{"/bin/sh", "-c", "tty; stty size; read x; stty size"}, Env: env, TTY: &guest.Size{Cols: 80, Rows: 24}})
	if err != nil {
		t.Fatal(err)
	}
	terminal := shell.Attach(true)
	waitFor(t, terminal.Stdout(), "24 80")
	if err := terminal.Resize(ctx, 132, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Stdin().Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, terminal.Stdout(), "40 132")
	_ = terminal.Close()

	if out, code := run("coord", "1000:1000", "/usr/local/bin/aether-internal", "kvm-connect", "/run/aether/coord3.sock"); code != 0 || !strings.Contains(out, "coord answers hello from the VM") {
		t.Errorf("coordination socket: exit %d: %s", code, out)
	}
	nnp, err := client.Spawn(ctx, guest.ProcessSpec{ID: "nnp", Env: env, User: "1000:1000", NoNewPrivileges: true, Argv: []string{"/bin/sh", "-c", "grep NoNewPrivs /proc/self/status"}})
	if err != nil {
		t.Fatal(err)
	}
	nnpOutput := nnp.Attach(true)
	if out, _ := io.ReadAll(nnpOutput.Stdout()); !strings.Contains(string(out), "NoNewPrivs:\t1") {
		t.Errorf("a NoNewPrivileges process in the VM: %q", out)
	}
	_ = nnpOutput.Close()
	if out, code := run("signing", "1000:1000", "/usr/local/bin/aether-internal", "kvm-connect", "/run/aether-signing/agent.sock"); code != 0 || !strings.Contains(out, "signing answers") {
		t.Errorf("signing socket: exit %d: %s", code, out)
	}
	// A stopped server leaves its socket file behind, and the next one
	// unlinks it and listens again. A call accepted just before the helper
	// noticed waits for the new server; later ones are refused and retried,
	// as under Docker. virtio-fs then drops the helper's bind, which it
	// binds again.
	coordListener.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = coordListener.Close()
	connect := []string{"/usr/local/bin/aether-internal", "kvm-connect", "/run/aether/coord3.sock"}
	held, err := client.Spawn(ctx, guest.ProcessSpec{ID: "coord-held", Env: env, User: "1000:1000", Argv: connect})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	refused, err := client.Spawn(ctx, guest.ProcessSpec{ID: "coord-refused", Env: env, User: "1000:1000", Argv: connect})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	echoSocket(t, coord, "restarted-coord")
	for _, p := range []*guest.Process{held, refused} {
		a := p.Attach(true)
		out, _ := io.ReadAll(a.Stdout())
		_ = a.Close()
		if !strings.Contains(string(out), "restarted-coord answers") {
			t.Errorf("%s, a call made while the server restarted: %s", p.ID(), out)
		}
		if p == refused && !strings.Contains(string(out), "connection refused") {
			t.Errorf("a connection while the server is down must be refused: %s", out)
		}
	}
	if out, code := run("coord-again", "1000:1000", "/usr/local/bin/aether-internal", "kvm-connect", "/run/aether/coord3.sock"); code != 0 || !strings.Contains(out, "restarted-coord answers") {
		t.Errorf("coordination socket after a server restart: exit %d: %s", code, out)
	}

	ticker, err := client.Spawn(ctx, guest.ProcessSpec{ID: "ticker", Env: env,
		Argv: []string{"/bin/sh", "-c", `echo started; read x; echo "got $x"`}})
	if err != nil {
		t.Fatal(err)
	}
	first := ticker.Attach(true)
	waitFor(t, first.Stdout(), "started")
	_ = first.Close()
	_ = client.Close()
	again, err := guest.Dial(vsockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	client = again
	reattached := client.Process("ticker").Attach(true)
	waitFor(t, reattached.Stdout(), "started")
	if _, err := reattached.Stdin().Write([]byte("again\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, reattached.Stdout(), "got again")
	_ = reattached.Close()

	if _, code := run("hog", "1000:1000", "/usr/local/bin/aether-internal", "kvm-hog"); code != 137 {
		t.Errorf("a process past the VM's memory exited %d, want 137", code)
	}
	stats, err := client.Stats(ctx)
	if err != nil || stats.OOMKills == 0 || stats.PidsLimit != 128 {
		t.Errorf("stats after an out-of-memory kill: %+v, %v", stats, err)
	}

	run("signal", "", "/bin/sh", "-c", "kill -USR1 1")
	if code, err := client.Process(guest.MainID).Wait(ctx); err != nil || code != 128+10 {
		t.Errorf("SIGUSR1 to PID 1 must end the main process: %d, %v", code, err)
	}
	if err := client.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("cloud-hypervisor: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("the VM did not power off")
	}
}

func diskArg(d vmimage.Disk, serial string) string {
	arg := fmt.Sprintf("path=%s,image_type=%s,serial=%s", d.Path, d.Format, serial)
	switch {
	case d.Format == vmimage.FormatQcow2:
		arg += ",backing_files=on"
	case serial == "helper":
		arg += ",readonly=on"
	}
	return arg
}
