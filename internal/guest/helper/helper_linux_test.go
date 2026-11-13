package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/guest"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// testProcs is the one table of the test binary: its reaper collects every
// child, so two tables would steal each other's exits. The reaper starts
// with the first helper a test serves, so TestKVMHelper, run alone, keeps
// os/exec's own waits for the VM monitor and the disk tools.
var (
	testProcs   = newProcesses(nil)
	startReaper sync.Once
)

func TestMain(m *testing.M) {
	// A process spawned with NoNewPrivileges starts as this binary.
	if len(os.Args) > 2 && os.Args[1] == "guest" {
		os.Exit(Main(os.Args[2:]))
	}
	// Orphans of the tests' commands come to this process, as they come to
	// PID 1 in a VM.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// hybridVsock answers Cloud Hypervisor's CONNECT handshake in front of the
// helper's server, as the VM monitor does on the host.
type hybridVsock struct{ net.Listener }

func (l hybridVsock) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	line := make([]byte, 0, 32)
	b := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, b); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	if string(line) != fmt.Sprintf("CONNECT %d", guest.APIPort) {
		return nil, fmt.Errorf("handshake %q", line)
	}
	if _, err := conn.Write([]byte("OK 1073741824\n")); err != nil {
		return nil, err
	}
	return conn, nil
}

// startHelper serves a helper on a Unix socket and returns its socket path
// and a client of it.
func startHelper(t *testing.T) (string, *guest.Client) {
	t.Helper()
	startReaper.Do(func() { go reapForever(testProcs) })
	socket := filepath.Join(t.TempDir(), "vsock.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(testProcs, nil, "boot-"+t.Name(), "")
	server := newServer(svc)
	go func() { _ = server.Serve(hybridVsock{listener}) }()
	t.Cleanup(server.Stop)
	return socket, dial(t, socket)
}

func dial(t *testing.T, socket string) *guest.Client {
	t.Helper()
	client, err := guest.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// self is the test's own user: tests do not run as root.
var self = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())

func spawn(t *testing.T, client *guest.Client, spec guest.ProcessSpec) *guest.Process {
	t.Helper()
	spec.ID = t.Name() + "/" + spec.ID
	spec.User = self
	spec.Env = append(spec.Env, "PATH="+os.Getenv("PATH"))
	p, err := client.Spawn(testContext(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// forgetBoot lets the next test boot the shared table again.
func forgetBoot(t *testing.T) {
	t.Cleanup(func() {
		testProcs.mu.Lock()
		defer testProcs.mu.Unlock()
		delete(testProcs.byID, guest.MainID)
		delete(testProcs.byID, guest.SetupID)
	})
}

func TestPipeProcess(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	if info, err := client.Ping(ctx); err != nil || info.Protocol != guest.Protocol || info.BootID == "" {
		t.Fatalf("ping: %+v, %v", info, err)
	}
	p := spawn(t, client, guest.ProcessSpec{ID: "pipe", Argv: []string{"sh", "-c", `echo "out $HOME"; echo err >&2; read x; echo "got $x"; exit 3`}, Dir: "/"})
	a := p.Attach(true)
	defer func() { _ = a.Close() }()
	if _, err := a.Stdin().Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	stdout, _ := io.ReadAll(a.Stdout())
	stderr, _ := io.ReadAll(a.Stderr())
	if !strings.HasPrefix(string(stdout), "out /") || !strings.HasSuffix(string(stdout), "got hello\n") || string(stderr) != "err\n" {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	if code, err := p.Wait(ctx); err != nil || code != 3 {
		t.Fatalf("wait: %d, %v", code, err)
	}
}

func TestTerminalProcess(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	p := spawn(t, client, guest.ProcessSpec{ID: "tty", Argv: []string{"sh", "-c", `stty size; read x; stty size; echo "term $TERM"`}, TTY: &guest.Size{Cols: 80, Rows: 24}})
	a := p.Attach(true)
	defer func() { _ = a.Close() }()
	waitFor(t, a.Stdout(), "24 80")
	if err := a.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Stdin().Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a.Stdout(), "30 100")
	waitFor(t, a.Stdout(), "term xterm")
	if err := p.CloseStdin(ctx); err == nil {
		t.Fatal("a terminal process must have no input to close")
	}
	if code, err := p.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait: %d, %v", code, err)
	}
}

// waitFor reads r until it has printed want.
func waitFor(t *testing.T, r io.Reader, want string) {
	t.Helper()
	var seen bytes.Buffer
	buf := make([]byte, 4096)
	for !strings.Contains(seen.String(), want) {
		n, err := r.Read(buf)
		seen.Write(buf[:n])
		if err != nil {
			t.Fatalf("waiting for %q: read %q, then %v", want, seen.String(), err)
		}
	}
}

// A server restart is a new client: it finds the process by ID and reads
// the output the helper kept, and the process never noticed.
func TestReattachAfterClientRestart(t *testing.T) {
	socket, first := startHelper(t)
	ctx := testContext(t)
	p := spawn(t, first, guest.ProcessSpec{ID: "kept", Argv: []string{"sh", "-c", `echo before; read x; echo "after $x"`}})
	a := p.Attach(true)
	waitFor(t, a.Stdout(), "before")
	_ = a.Close()
	_ = first.Close()

	second := dial(t, socket)
	again := second.Process(p.ID())
	b := again.Attach(true)
	defer func() { _ = b.Close() }()
	waitFor(t, b.Stdout(), "before")
	if _, err := b.Stdin().Write([]byte("restart\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b.Stdout(), "after restart")
	if code, err := again.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait: %d, %v", code, err)
	}
	if _, err := second.Process("never spawned").Status(ctx); !errors.Is(err, guest.ErrNotFound) {
		t.Fatalf("an unknown process: %v", err)
	}
}

// A lossless process waits for its reader instead of losing output, also
// past the helper's window.
func TestLosslessOutput(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	const size = 4 * outputLimit
	p := spawn(t, client, guest.ProcessSpec{ID: "lossless", Argv: []string{"head", "-c", fmt.Sprint(size), "/dev/zero"}, Lossless: true})
	time.Sleep(200 * time.Millisecond)
	if state, err := p.Status(ctx); err != nil || state.Exited {
		t.Fatalf("an unread lossless process must wait: %+v, %v", state, err)
	}
	a := p.Attach(true)
	defer func() { _ = a.Close() }()
	data, err := io.ReadAll(a.Stdout())
	if err != nil || len(data) != size {
		t.Fatalf("read %d of %d bytes: %v", len(data), size, err)
	}
	if code, err := p.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait: %d, %v", code, err)
	}
}

// A reader that comes after the stream's drain time still gets every byte a
// lossless process wrote: the rest waited in the pipe.
func TestLosslessOutputReadLate(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	const size = outputLimit + 48<<10
	p := spawn(t, client, guest.ProcessSpec{ID: "late", Argv: []string{"head", "-c", fmt.Sprint(size), "/dev/zero"}, Lossless: true})
	if code, err := p.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait: %d, %v", code, err)
	}
	time.Sleep(exitDrain + 500*time.Millisecond)
	a := p.Attach(true)
	defer func() { _ = a.Close() }()
	data, err := io.ReadAll(a.Stdout())
	if err != nil || len(data) != size {
		t.Fatalf("read %d of %d bytes: %v", len(data), size, err)
	}
}

func TestWorkingDirectory(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho ran in \"$(pwd)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := spawn(t, client, guest.ProcessSpec{ID: "relative", Argv: []string{"./run.sh"}, Dir: dir})
	a := p.Attach(true)
	out, _ := io.ReadAll(a.Stdout())
	_ = a.Close()
	if string(out) != "ran in "+dir+"\n" {
		t.Fatalf("a relative program runs from the working directory: %q", out)
	}
	var startErr *guest.StartError
	_, err := client.Spawn(ctx, guest.ProcessSpec{ID: t.Name() + "/nodir", User: self, Argv: []string{"true"}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: filepath.Join(dir, "missing")})
	if !errors.As(err, &startErr) || startErr.Code != 126 || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("a missing working directory: %v", err)
	}
}

// A given gid replaces the passwd entry's groups, supplementary ones too,
// as runc does: an explicit identity must not reach group-protected files.
func TestCredentialGroups(t *testing.T) {
	cred, _, err := credential("0:0")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || len(cred.Groups) != 0 {
		t.Fatalf("an explicit uid:gid got supplementary groups: %+v", cred)
	}
}

func TestNoNewPrivileges(t *testing.T) {
	_, client := startHelper(t)
	// Without the option a process keeps the helper's own setting, which a
	// test run inside a container may already have set.
	inherited, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, nnp := range []bool{false, true} {
		p := spawn(t, client, guest.ProcessSpec{ID: fmt.Sprint(nnp), Argv: []string{"sh", "-c", "grep NoNewPrivs /proc/self/status"}, NoNewPrivileges: nnp})
		a := p.Attach(true)
		out, _ := io.ReadAll(a.Stdout())
		_ = a.Close()
		want := map[bool]string{false: fmt.Sprint(inherited), true: "1"}[nnp]
		if fields := strings.Fields(string(out)); len(fields) != 2 || fields[1] != want {
			t.Errorf("NoNewPrivileges %v: a descendant reports %q", nnp, out)
		}
	}
	// A program that passes the lookup but cannot be executed fails the
	// spawn either way, also behind the helper binary.
	script := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(script, []byte("#!/no/such/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, nnp := range []bool{false, true} {
		var startErr *guest.StartError
		_, err := client.Spawn(testContext(t), guest.ProcessSpec{ID: t.Name() + "/bad-interpreter-" + fmt.Sprint(nnp), User: self, Argv: []string{script}, NoNewPrivileges: nnp})
		if !errors.As(err, &startErr) || startErr.Code != 127 {
			t.Errorf("NoNewPrivileges %v, a missing interpreter: %v", nnp, err)
		}
	}
}

func TestSpawnFailures(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	var startErr *guest.StartError
	if _, err := client.Spawn(ctx, guest.ProcessSpec{ID: t.Name() + "/missing", User: self, Argv: []string{"/no/such/program"}}); !errors.As(err, &startErr) || startErr.Code != 127 {
		t.Fatalf("missing program: %v", err)
	}
	if _, err := client.Spawn(ctx, guest.ProcessSpec{ID: t.Name() + "/dir", User: self, Argv: []string{"/"}}); !errors.As(err, &startErr) || startErr.Code != 126 {
		t.Fatalf("a directory: %v", err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "noexec"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Spawn(ctx, guest.ProcessSpec{ID: t.Name() + "/noexec", User: self, Argv: []string{"noexec"}, Env: []string{"PATH=" + bin}}); !errors.As(err, &startErr) || startErr.Code != 126 {
		t.Fatalf("a program on PATH that cannot run: %v", err)
	}
	p := spawn(t, client, guest.ProcessSpec{ID: "once", Argv: []string{"true"}})
	if _, err := client.Spawn(ctx, guest.ProcessSpec{ID: p.ID(), User: self, Argv: []string{"true"}}); err == nil {
		t.Fatal("an ID must not start a second process")
	}
	if _, err := client.Spawn(ctx, guest.ProcessSpec{ID: guest.MainID, Argv: []string{"true"}}); err == nil {
		t.Fatal("Spawn must not start the main process")
	}
}

func TestSignalAndInput(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	p := spawn(t, client, guest.ProcessSpec{ID: "sleep", Argv: []string{"sleep", "60"}})
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	// sleep never reads: the pipe fills, and the deadline ends the write
	// without closing the input.
	n, err := p.Write(short, make([]byte, 4<<20))
	if err == nil || n >= 4<<20 {
		t.Fatalf("write to a process that does not read: %d, %v", n, err)
	}
	if err := p.Signal(ctx, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(ctx); err != nil || code != 128+int(syscall.SIGTERM) {
		t.Fatalf("wait: %d, %v", code, err)
	}
	if err := p.Signal(ctx, syscall.SIGTERM); err == nil {
		t.Fatal("signalling an exited process must fail")
	}
}

// The helper reaps every orphan, as PID 1 must, so none stays a zombie.
func TestOrphansAreReaped(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	p := spawn(t, client, guest.ProcessSpec{ID: "parent", Argv: []string{"sh", "-c", `sleep 0.2 & echo $!`}})
	a := p.Attach(true)
	out, _ := io.ReadAll(a.Stdout())
	_ = a.Close()
	if _, err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	orphan := "/proc/" + strings.TrimSpace(string(out))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(orphan); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			stat, _ := os.ReadFile(orphan + "/stat")
			t.Fatalf("orphan %s was not reaped: %s", orphan, stat)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoot(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	marker := filepath.Join(t.TempDir(), "setup-ran")
	forgetBoot(t)
	main := guest.ProcessSpec{User: self, Argv: []string{"sh", "-c", `cat "$MARKER"; echo main; read x`}, Env: []string{"MARKER=" + marker, "PATH=" + os.Getenv("PATH")}}
	spec := guest.BootSpec{Main: main, SetupScript: `echo setup > "$MARKER"`}
	if err := client.Boot(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := client.Boot(ctx, guest.BootSpec{Main: main, SetupScript: "exit 1"}); err != nil {
		t.Fatalf("a second boot must return the first one's result: %v", err)
	}
	a := client.Process(guest.MainID).Attach(true)
	defer func() { _ = a.Close() }()
	waitFor(t, a.Stdout(), "setup\nmain\n")
	_ = a.Stdin().Close()
	if _, err := a.Stdin().Write([]byte("x")); err == nil {
		t.Fatal("a closed attachment input must refuse writes")
	}
	if state, err := client.Process(guest.MainID).Status(ctx); err != nil || state.Exited {
		t.Fatalf("closing an attachment's input must not end the main process: %+v, %v", state, err)
	}
	if _, err := client.Process(guest.MainID).Write(ctx, []byte("bye\n")); err != nil {
		t.Fatal(err)
	}
	if code, err := client.Process(guest.MainID).Wait(ctx); err != nil || code != 0 {
		t.Fatalf("main: %d, %v", code, err)
	}
}

// Setup gets no input, as under Docker: a prompt reads end of file.
func TestBootSetupHasNoInput(t *testing.T) {
	_, client := startHelper(t)
	forgetBoot(t)
	err := client.Boot(testContext(t), guest.BootSpec{
		Main:        guest.ProcessSpec{User: self, Argv: []string{"true"}, Env: []string{"PATH=" + os.Getenv("PATH")}},
		SetupScript: "read answer || true; echo done",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBootSetupFails(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	forgetBoot(t)
	marker := filepath.Join(t.TempDir(), "main-ran")
	err := client.Boot(ctx, guest.BootSpec{
		Main:        guest.ProcessSpec{User: self, Argv: []string{"touch", marker}, Env: []string{"PATH=" + os.Getenv("PATH")}},
		SetupScript: "echo installing; echo broken >&2; exit 7",
	})
	var setup *guest.SetupError
	if !errors.As(err, &setup) || setup.ExitCode != 7 || setup.Output != "installing\nbroken\n" || setup.Cut {
		t.Fatalf("boot: %v (%+v)", err, setup)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the main process ran after a failed setup: %v", err)
	}
}

func TestStats(t *testing.T) {
	_, client := startHelper(t)
	stats, err := client.Stats(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if stats.MemoryLimitBytes == 0 || stats.MemoryBytes == 0 || stats.MemoryBytes > stats.MemoryLimitBytes || stats.CPUUsageNanos == 0 {
		t.Fatalf("stats: %+v", stats)
	}
}

type fakeConn struct {
	net.Conn
	from net.Addr
}

func (c fakeConn) RemoteAddr() net.Addr { return c.from }
func (c fakeConn) Close() error         { return nil }

type fakeListener struct {
	net.Listener
	conns chan net.Conn
}

func (l fakeListener) Accept() (net.Conn, error) { return <-l.conns, nil }

func TestHostOnly(t *testing.T) {
	conns := make(chan net.Conn, 2)
	conns <- fakeConn{from: &vsock.Addr{ContextID: 3, Port: 5000}}
	conns <- fakeConn{from: &vsock.Addr{ContextID: vsock.Host, Port: 5001}}
	conn, err := hostOnly{fakeListener{conns: conns}}.Accept()
	if err != nil || conn.RemoteAddr().(*vsock.Addr).Port != 5001 {
		t.Fatalf("accepted %v, %v; want only the host's connection", conn.RemoteAddr(), err)
	}
}

func TestRecordsStayUntilTheirCallersAreDone(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	unread := spawn(t, client, guest.ProcessSpec{ID: "unread", Argv: []string{"sh", "-c", "echo late output >&2"}, Lossless: true})
	unwaited := spawn(t, client, guest.ProcessSpec{ID: "unwaited", Argv: []string{"sh", "-c", "exit 7"}, Lossless: true})
	// The unread one's exit is reported and its stdout read, its stderr
	// left unread; the
	// unwaited one's exit is watched in the table, since a Status that sees
	// an exit reports it.
	if code, err := unread.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait: %d, %v", code, err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		p, err := testProcs.get(unwaited.ID())
		if err != nil {
			t.Fatal(err)
		}
		if p.exited() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the unwaited process did not exit")
		}
	}
	u := unread.Attach(true)
	if out, err := io.ReadAll(u.Stdout()); err != nil || len(out) != 0 {
		t.Fatalf("unread stdout %q, %v", out, err)
	}
	_ = u.Close()
	a := unwaited.Attach(true)
	if out, err := io.ReadAll(a.Stdout()); err != nil || len(out) != 0 {
		t.Fatalf("unwaited output %q, %v", out, err)
	}
	_ = a.Close()
	for i := range maxExited + 8 {
		p := spawn(t, client, guest.ProcessSpec{ID: fmt.Sprintf("later-%d", i), Argv: []string{"true"}})
		if _, err := p.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a = unread.Attach(true)
	defer func() { _ = a.Close() }()
	if out, err := io.ReadAll(a.Stderr()); err != nil || string(out) != "late output\n" {
		t.Fatalf("stderr read after %d later exits = %q, %v", maxExited+8, out, err)
	}
	if code, err := unwaited.Wait(ctx); err != nil || code != 7 {
		t.Fatalf("exit read after %d later exits = %d, %v", maxExited+8, code, err)
	}
}

func TestReadAndAbandonedRecordsAreForgotten(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	run := func(n int, prefix string) {
		t.Helper()
		for i := range n {
			p := spawn(t, client, guest.ProcessSpec{ID: fmt.Sprintf("%s-%d", prefix, i), Argv: []string{"true"}})
			if _, err := p.Wait(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := spawn(t, client, guest.ProcessSpec{ID: "read", Argv: []string{"true"}})
	if _, err := read.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	abandoned := spawn(t, client, guest.ProcessSpec{ID: "abandoned", Argv: []string{"sh", "-c", "echo never read"}, Lossless: true})
	// A terminal's attachment reads it to its end, and nobody asks its exit.
	terminal := spawn(t, client, guest.ProcessSpec{ID: "terminal", Argv: []string{"sh", "-c", "head -c 100000 /dev/zero"}, TTY: &guest.Size{Cols: 80, Rows: 24}})
	ta := terminal.Attach(true)
	if out, err := io.ReadAll(ta.Stdout()); err != nil || len(out) < 100000 {
		t.Fatalf("terminal output %d bytes, %v", len(out), err)
	}
	_ = ta.Close()
	run(maxExited+1, "after")
	held, err := testProcs.get(terminal.ID())
	if err != nil {
		t.Fatal(err)
	}
	held.stdout.mu.Lock()
	kept := len(held.stdout.data)
	held.stdout.mu.Unlock()
	if kept != 0 {
		t.Errorf("an older record read to its end still holds %d bytes of output", kept)
	}
	if _, err := read.Status(ctx); !errors.Is(err, guest.ErrNotFound) {
		t.Fatalf("a record whose exit was read, older than the newest %d: %v; it must be forgotten", maxExited, err)
	}
	if _, err := abandoned.Status(ctx); err != nil {
		t.Fatalf("a record nobody read yet was forgotten early: %v", err)
	}
	run(maxKept, "later")
	if _, err := abandoned.Status(ctx); !errors.Is(err, guest.ErrNotFound) {
		t.Fatalf("a record nobody read, %d exits old: %v; it must be forgotten", maxKept+maxExited+1, err)
	}
	testProcs.mu.Lock()
	records := len(testProcs.exited)
	testProcs.mu.Unlock()
	if records > maxKept {
		t.Errorf("%d exited records kept, over %d", records, maxKept)
	}
}

func TestNewestRecordsStayWhileOlderOnesAreUnread(t *testing.T) {
	_, client := startHelper(t)
	ctx := testContext(t)
	for i := range maxExited + 1 {
		p := spawn(t, client, guest.ProcessSpec{ID: fmt.Sprintf("unread-%d", i), Argv: []string{"sh", "-c", "echo unread"}, Lossless: true})
		if _, err := p.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	recent := spawn(t, client, guest.ProcessSpec{ID: "recent", Argv: []string{"sh", "-c", "exit 5"}})
	if code, err := recent.Wait(ctx); err != nil || code != 5 {
		t.Fatalf("wait: %d, %v", code, err)
	}
	next := spawn(t, client, guest.ProcessSpec{ID: "next", Argv: []string{"true"}})
	if _, err := next.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	// A Wait retried after a lost reply still finds the recent exit.
	if code, err := recent.Wait(ctx); err != nil || code != 5 {
		t.Fatalf("a retried wait on one of the newest records = %d, %v", code, err)
	}
}
