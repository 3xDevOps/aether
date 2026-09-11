package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// testAdvertisedSHA is the ref tip the fake git transport advertises.
const testAdvertisedSHA = "1111222233334444555566667777888899990000"

// fakeGit speaks just enough upload-pack v0 for a client ls-remote: it
// advertises one branch and drains the client's flush.
type fakeGit struct {
	mu    sync.Mutex
	calls []string
}

func pktLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

func (g *fakeGit) record(op string, ws domain.WorkspaceID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, op+":"+string(ws))
}

func (g *fakeGit) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func (g *fakeGit) UploadPack(_ context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	g.record("upload-pack", ws)
	adv := pktLine(testAdvertisedSHA+" HEAD\x00multi_ack\n") +
		pktLine(testAdvertisedSHA+" refs/heads/main\n") +
		"0000"
	if _, err := io.WriteString(stdout, adv); err != nil {
		return 128, err
	}
	// Drain until the client's flush-pkt or EOF.
	buf := make([]byte, 256)
	var got []byte
	for !bytes.Contains(got, []byte("0000")) {
		n, err := stdin.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	return 0, nil
}

func (g *fakeGit) ReceivePack(_ context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	g.record("receive-pack", ws)
	if _, err := io.WriteString(stdout, pktLine(testAdvertisedSHA+" refs/heads/main\x00report-status\n")+"0000"); err != nil {
		return 128, err
	}
	_, _ = io.Copy(io.Discard, stdin)
	return 0, nil
}

// fakePTY records attach parameters, replays canned output, and echoes
// keystrokes prefixed with "echo:". transcripts backs the Replay seam:
// a run with an entry replays it, anything else reports os.ErrNotExist.
type fakePTY struct {
	mu          sync.Mutex
	err         error
	errDelay    time.Duration
	gate        ptyhost.WriteGate
	replay      []byte
	transcripts map[domain.RunID][]byte
	cols        uint
	rows        uint
	readOnly    bool
	follow      bool
	input       bytes.Buffer
	resizes     [][2]uint
	// session is the size the fake session is; when set it is what the
	// host reports to the attach, the way the real one reports the size
	// the PTY actually has rather than the size the header asked for.
	session [2]uint
	// tell carries a later geometry the test wants reported, which is
	// what someone else resizing the session does.
	tell chan [2]uint
}

func (p *fakePTY) Replay(run domain.RunID) (io.ReadCloser, error) {
	p.mu.Lock()
	data, ok := p.transcripts[run]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("ptyhost: open transcript: %w", os.ErrNotExist)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (p *fakePTY) setTranscript(run domain.RunID, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.transcripts == nil {
		p.transcripts = map[domain.RunID][]byte{}
	}
	p.transcripts[run] = data
}

func (p *fakePTY) Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error {
	member, cols, rows, readOnly := client.Member, client.Cols, client.Rows, client.ReadOnly
	p.mu.Lock()
	if p.err != nil {
		err, delay := p.err, p.errDelay
		p.mu.Unlock()
		// A delayed error models a failure after the ack: the replay
		// boundary goes out first, then the attach dies.
		if delay > 0 {
			if rw, ok := conn.(ptyhost.ReplayWriter); ok {
				_, _ = rw.WriteReplay(nil)
			}
			time.Sleep(delay)
		}
		return err
	}
	gate := p.gate
	p.mu.Unlock()
	// Mirror ptyhost.Host.Attach: the gate runs for write-mode attaches
	// only and its denial is wrapped in ErrWriteDenied.
	if !readOnly && gate != nil {
		if gerr := gate(ctx, member, key); gerr != nil {
			return fmt.Errorf("%w: %v", errWriteDenied, gerr)
		}
	}
	p.mu.Lock()
	p.cols, p.rows, p.readOnly, p.follow = cols, rows, readOnly, client.Follow
	replay := p.replay
	session, tell := p.session, p.tell
	p.mu.Unlock()

	geo, hasGeo := conn.(ptyhost.GeometryWriter)
	if hasGeo && session != [2]uint{} {
		geo.SetGeometry(session[0], session[1])
	}

	if rw, ok := conn.(ptyhost.ReplayWriter); ok {
		if _, err := rw.WriteReplay(replay); err != nil {
			return nil
		}
	} else if len(replay) > 0 {
		if _, err := conn.Write(replay); err != nil {
			return nil
		}
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.input.Write(buf[:n])
				p.mu.Unlock()
				if _, werr := conn.Write(append([]byte("echo:"), buf[:n]...)); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case sz := <-tell:
			if hasGeo {
				geo.SetGeometry(sz[0], sz[1])
			}
		case sz := <-resize:
			p.mu.Lock()
			p.resizes = append(p.resizes, sz)
			p.mu.Unlock()
		case <-readDone:
			return nil
		case <-ctx.Done():
			// Mirror ptyhost.Host.Attach: a canceled context ends the
			// attach with the context's error.
			return ctx.Err()
		}
	}
}

func (p *fakePTY) state() (cols, rows uint, readOnly bool, input string, resizes [][2]uint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows, p.readOnly, p.input.String(), append([][2]uint(nil), p.resizes...)
}

// following reports whether the last attach declared itself a follower.
func (p *fakePTY) following() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.follow
}

func (p *fakePTY) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// fakeRuns records RunController calls and returns the configured error.
type fakeRuns struct {
	mu              sync.Mutex
	err             error
	addr            string
	addrErr         error
	addrStarted     chan<- struct{}
	addrRelease     <-chan struct{}
	addrCanceled    chan<- struct{}
	terminalAddr    string
	terminalAddrErr error
	calls           []string
	paused          map[domain.RunID]bool
}

func (f *fakeRuns) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.err
}

func (f *fakeRuns) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeRuns) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRuns) Launch(_ context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error) {
	if err := f.record(fmt.Sprintf("launch:%s:%s:%s:%s:%s", workspace, member, task, harness, mode)); err != nil {
		return nil, err
	}
	return &domain.Run{
		ID: "run_new", WorkspaceID: workspace, MemberID: member, AccountMemberID: account, Task: task,
		Harness: harness, Mode: mode, Status: domain.RunQueued,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeRuns) ContainerAddr(ctx context.Context, run domain.RunID) (string, error) {
	if err := f.record(fmt.Sprintf("container-addr:%s", run)); err != nil {
		return "", err
	}
	f.mu.Lock()
	started, release, canceled := f.addrStarted, f.addrRelease, f.addrCanceled
	f.mu.Unlock()
	if release != nil {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			select {
			case canceled <- struct{}{}:
			default:
			}
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addrErr != nil {
		return "", f.addrErr
	}
	return f.addr, nil
}
func (f *fakeRuns) TerminalContainerAddr(_ context.Context, member domain.MemberID) (string, error) {
	if err := f.record(fmt.Sprintf("terminal-container-addr:%s", member)); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.terminalAddrErr != nil {
		return "", f.terminalAddrErr
	}
	return f.terminalAddr, nil
}

func (f *fakeRuns) setTerminalAddr(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminalAddr = addr
}
func (f *fakeRuns) setAddr(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addr = addr
}
func (f *fakeRuns) blockContainerAddr() (<-chan struct{}, chan struct{}, <-chan struct{}) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	canceled := make(chan struct{}, 1)
	f.mu.Lock()
	f.addrStarted = started
	f.addrRelease = release
	f.addrCanceled = canceled
	f.mu.Unlock()
	return started, release, canceled
}

func (f *fakeRuns) Kill(_ context.Context, run domain.RunID, actor domain.MemberID) error {
	return f.record(fmt.Sprintf("kill:%s:%s", run, actor))
}

func (f *fakeRuns) DeleteRun(_ context.Context, run domain.RunID, actor domain.MemberID) error {
	return f.record(fmt.Sprintf("delete:%s:%s", run, actor))
}
func (f *fakeRuns) Pause(_ context.Context, run domain.RunID, actor domain.MemberID) error {
	return f.record(fmt.Sprintf("pause:%s:%s", run, actor))
}

func (f *fakeRuns) Resume(_ context.Context, run domain.RunID, actor domain.MemberID) error {
	return f.record(fmt.Sprintf("resume:%s:%s", run, actor))
}

func (f *fakeRuns) Paused(run domain.RunID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused[run]
}

func (f *fakeRuns) setPaused(run domain.RunID, paused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paused == nil {
		f.paused = map[domain.RunID]bool{}
	}
	f.paused[run] = paused
}

func (f *fakeRuns) Inject(_ context.Context, run domain.RunID, actor domain.MemberID, message string) error {
	return f.record(fmt.Sprintf("inject:%s:%s:%s", run, actor, message))
}

func (f *fakeRuns) RecordHandoff(_ context.Context, run domain.RunID, from domain.MemberID) {
	_ = f.record(fmt.Sprintf("handoff:%s:%s", run, from))
}

func (f *fakeRuns) RefreshMemberCoAuthors(_ context.Context, member domain.MemberID) {
	_ = f.record(fmt.Sprintf("refresh-coauthors:%s", member))
}

func (f *fakeRuns) CloseRun(_ context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error {
	return f.record(fmt.Sprintf("close:%s:%s:%s", run, actor, outcome))
}

func (f *fakeRuns) Relaunch(_ context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error) {
	if err := f.record(fmt.Sprintf("relaunch:%s:%s", run, actor)); err != nil {
		return nil, err
	}
	return &domain.Run{
		ID: "run_relaunched", WorkspaceID: "ws", MemberID: actor,
		Task: "t", Harness: "claude", Mode: domain.LaunchTUI,
		Status: domain.RunQueued, CreatedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeRuns) EnsureRunShellTab(_ context.Context, run domain.RunID, tab string, cols, rows uint) error {
	return f.record(fmt.Sprintf("run-shell:%s:%s:%d:%d", run, tab, cols, rows))
}
func (f *fakeRuns) EnsureTerminal(_ context.Context, member domain.MemberID) (*domain.Terminal, error) {
	if err := f.record(fmt.Sprintf("terminal:%s", member)); err != nil {
		return nil, err
	}
	return &domain.Terminal{Member: member, ContainerID: "terminal-container", Image: "standard", StartedAt: time.Now().UTC()}, nil
}

func (f *fakeRuns) EnsureTerminalTab(_ context.Context, member domain.MemberID, tab string, cols, rows uint) error {
	return f.record(fmt.Sprintf("terminal-tab:%s:%s:%d:%d", member, tab, cols, rows))
}

func (f *fakeRuns) StopTerminal(_ context.Context, member domain.MemberID) error {
	return f.record(fmt.Sprintf("terminal-stop:%s", member))
}

func (f *fakeRuns) TerminalStatus(_ context.Context, member domain.MemberID) (domain.TerminalStatus, error) {
	if err := f.record(fmt.Sprintf("terminal-status:%s", member)); err != nil {
		return domain.TerminalStatus{}, err
	}
	return domain.TerminalStatus{}, nil
}

func (f *fakeRuns) SaveEnvironment(_ context.Context, member domain.MemberID) (string, error) {
	if err := f.record(fmt.Sprintf("env-save:%s", member)); err != nil {
		return "", err
	}
	return "aether/member-" + string(member) + ":1", nil
}

func (f *fakeRuns) ResetEnvironment(_ context.Context, member domain.MemberID) error {
	return f.record(fmt.Sprintf("env-reset:%s", member))
}
func (f *fakeRuns) SaveTerminalImage(_ context.Context, actor domain.MemberID, run domain.RunID, extension string, data []byte) (string, error) {
	if err := f.record(fmt.Sprintf("terminal-image:%s:%s:%s:%d", actor, run, extension, len(data))); err != nil {
		return "", err
	}
	return "/root/.aether/terminal-images/image-test" + extension, nil
}

func (f *fakeRuns) ConnectGitHub(_ context.Context, member domain.MemberID) (domain.GitHubConnection, error) {
	if err := f.record(fmt.Sprintf("github-connect:%s", member)); err != nil {
		return domain.GitHubConnection{}, err
	}
	return domain.GitHubConnection{
		Login:       "octocat",
		SigningKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample aether " + string(member),
		Fingerprint: "SHA256:Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm8",
	}, nil
}

// The answer is deliberately over-populated - a real one never carries
// both SavedImage and AdminRemedy - so every field of the wire mapping has
// a distinct value to be pinned against.
func (f *fakeRuns) ProbeGitHubCLI(_ context.Context, member domain.MemberID) (domain.GitHubCLI, error) {
	if err := f.record(fmt.Sprintf("github-probe:%s", member)); err != nil {
		return domain.GitHubCLI{}, err
	}
	return domain.GitHubCLI{
		Status:      domain.GitHubCLIOutdated,
		Version:     "2.45.0",
		Minimum:     "2.81.0",
		Detail:      "gh version 2.45.0",
		Image:       "ghcr.io/3xdevops/aether-standard:latest",
		SavedImage:  "aether/member-" + string(member) + ":1",
		Path:        "/root/.local/bin/gh",
		Remedy:      "aether env reset",
		AdminRemedy: "docker pull ghcr.io/3xdevops/aether-standard:latest",
	}, nil
}

func (f *fakeRuns) HoldShell() func() { return func() {} }

type testEnv struct {
	srv         *Server
	addr        string
	store       store.Store
	bus         events.Bus
	git         *fakeGit
	pty         *fakePTY
	runs        *fakeRuns
	member      *domain.Member
	signer      ssh.Signer
	ws          *domain.Workspace
	run         *domain.Run
	serveCancel context.CancelFunc
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

func newTestEnv(t *testing.T, mod func(*Config)) *testEnv {
	t.Helper()
	return newTestEnvWithSigner(t, mod, newSigner(t))
}

// newFreshTestEnv builds an env against a fresh, unseeded store: no
// member, workspace, or run rows (bootstrap tests).
func newFreshTestEnv(t *testing.T, mod func(*Config)) *testEnv {
	t.Helper()
	return buildTestEnv(t, mod, newSigner(t), false)
}

func newTestEnvWithSigner(t *testing.T, mod func(*Config), signer ssh.Signer) *testEnv {
	t.Helper()
	return buildTestEnv(t, mod, signer, true)
}

func buildTestEnv(t *testing.T, mod func(*Config), signer ssh.Signer, seed bool) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bus, err := events.NewInProc(context.Background(), nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	e := &testEnv{
		store:  db,
		bus:    bus,
		git:    &fakeGit{},
		pty:    &fakePTY{},
		runs:   &fakeRuns{},
		signer: signer,
	}

	ctx := context.Background()
	if seed {
		e.member = &domain.Member{
			DisplayName: "Ada",
			PublicKey:   string(ssh.MarshalAuthorizedKey(e.signer.PublicKey())),
			Color:       "#e6194b",
			Role:        domain.RoleAdmin,
		}
		if cerr := db.CreateMember(ctx, e.member); cerr != nil {
			t.Fatalf("create member: %v", cerr)
		}
		e.ws = &domain.Workspace{Name: "proj", BaseBranch: "main", Environment: domain.WorkspaceEnvironment{}}
		if cerr := db.CreateWorkspace(ctx, e.ws); cerr != nil {
			t.Fatalf("create workspace: %v", cerr)
		}
		e.run = &domain.Run{
			WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "do things",
			Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunRunning,
			Branch: "aether/run-x-do-things",
		}
		if cerr := db.CreateRun(ctx, e.run); cerr != nil {
			t.Fatalf("create run: %v", cerr)
		}
	}

	cfg := Config{
		Addr:        "127.0.0.1:0",
		HostKeyPath: filepath.Join(dir, "ssh", "host_ed25519_key"),
		Store:       db,
		Bus:         bus,
		Git:         e.git,
		PTY:         e.pty,
		Runs:        e.runs,
	}
	if mod != nil {
		mod(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.srv = srv
	serveCtx, cancel := context.WithCancel(context.Background())
	e.serveCancel = cancel
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("server did not start listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.addr = srv.Addr().String()
	return e
}

func (e *testEnv) dialWith(signer ssh.Signer, banner *strings.Builder) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            "aether",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if banner != nil {
		cfg.BannerCallback = func(message string) error {
			banner.WriteString(message)
			return nil
		}
	}
	return ssh.Dial("tcp", e.addr, cfg)
}

func (e *testEnv) dial(t *testing.T) *ssh.Client {
	t.Helper()
	client, err := e.dialWith(e.signer, nil)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// subsystemPipe is the client side of one subsystem channel.
type subsystemPipe struct {
	io.Reader
	stdin io.WriteCloser
	sess  *ssh.Session
}

func (p *subsystemPipe) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *subsystemPipe) CloseWrite() error           { return p.stdin.Close() }
func (p *subsystemPipe) Close() error {
	_ = p.stdin.Close()
	return p.sess.Close()
}

func openSubsystem(t *testing.T, client *ssh.Client, name string, setup func(*ssh.Session) error) *subsystemPipe {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if setup != nil {
		if serr := setup(sess); serr != nil {
			t.Fatalf("session setup: %v", serr)
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := sess.RequestSubsystem(name); err != nil {
		t.Fatalf("subsystem %s: %v", name, err)
	}
	p := &subsystemPipe{Reader: stdout, stdin: stdin, sess: sess}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func TestWriteGateAllowsNonRunSessionKeys(t *testing.T) {
	gate := NewWriteGate(nil)
	if err := gate(t.Context(), "member", ptyhost.TerminalSession("member", "main")); err != nil {
		t.Fatalf("terminal session gate = %v, want nil", err)
	}
	if err := gate(t.Context(), "member", ptyhost.RunShellSession("run", "shell")); err != nil {
		t.Fatalf("run-shell session gate = %v, want nil", err)
	}
}
