//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// agentScript is the deterministic e2e agent, committed to the seed repo
// and run by the "fake" harness (AETHER_FAKE_AGENT="sh /workspace/agent.sh")
// inside a real container. The leading sleep keeps its first output behind
// the supervisor's attach point; the e2eRuntime fallback scripts the same
// behavior in-process.
const agentScript = `sleep 1
echo agent-ready
read line
echo "got:$line"
printf 'hello-from-agent\n' > result.txt
`

// TestIntegrationEndToEnd proves the Wave 1 seams end to end through one
// wired server: bare repo seeded via git push over the SSH transport, a
// run launched over the control channel with the fake harness, live PTY
// output observed over a real SSH attach, an injection round-tripped
// through the agent, the run branch fetched back over SSH after exit, and
// the bus traffic matching the contract's lifecycle table.
func TestIntegrationEndToEnd(t *testing.T) {
	requireBinary(t, "git")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, image, verifyNoLeaks := pickRuntime(t)
	// A not-yet-existing data dir proves first-run startup: New must create
	// the directory itself before opening the store.
	dataDir := filepath.Join(t.TempDir(), "data")
	srv, err := New(ctx, Config{DataDir: dataDir, Addr: "127.0.0.1:0", Runtime: rt})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	keyPath, signer := writeClientKey(t)
	member := &domain.Member{
		DisplayName: "E2E Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleAdmin,
	}
	ws := &domain.Workspace{
		Name:        "e2e",
		Environment: domain.WorkspaceEnvironment{CustomImage: image},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err = srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err = srv.Store().CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	sub, err := srv.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	defer func() { _ = sub.Close() }()
	var seen []events.Event

	runDone := make(chan error, 1)
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { runDone <- srv.Run(runCtx) }()
	addr := waitSSHAddr(t, srv)

	// Seed the workspace bare repo with main via git push over SSH.
	seedDir := t.TempDir()
	repoURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, ws.ID)
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
	runGit(t, seedDir, gitEnv, "init", "-q", "-b", "main")
	runGit(t, seedDir, gitEnv, "config", "user.name", "E2E")
	runGit(t, seedDir, gitEnv, "config", "user.email", "e2e@localhost")
	runGit(t, seedDir, gitEnv, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(seedDir, "README.md"), "# e2e seed\n")
	writeFile(t, filepath.Join(seedDir, "agent.sh"), agentScript)
	runGit(t, seedDir, gitEnv, "add", "-A")
	runGit(t, seedDir, gitEnv, "commit", "-q", "-m", "seed")
	runGit(t, seedDir, gitEnv, "push", "-q", repoURL, "main")

	// Launch the fake-harness run over the control channel.
	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh")
	client := dialSSH(t, addr, signer)
	ctrl := openControl(t, client)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(ws.ID), Task: "integration e2e", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	run := launched.Run
	if run.Status != string(domain.RunRunning) {
		t.Fatalf("launched run status = %q, want running", run.Status)
	}
	if run.Branch == "" || !strings.HasPrefix(run.Branch, "aether/run-") {
		t.Fatalf("launched run branch = %q", run.Branch)
	}
	runID := domain.RunID(run.ID)

	statusOf := func(to domain.RunStatus) func(events.Event) bool {
		return func(e events.Event) bool {
			p, ok := e.Payload.(events.RunStatusPayload)
			return ok && e.RunID == runID && p.To == to
		}
	}
	waitEvent(t, sub, &seen, "run.status provisioning", statusOf(domain.RunProvisioning))
	waitEvent(t, sub, &seen, "run.status running", statusOf(domain.RunRunning))

	// Attach over a real SSH client connection and watch the agent live.
	att := openAttach(t, client, run.ID)
	att.waitOutput(t, "agent-ready")

	// Detach and reattach: the PTY session survives losing its only
	// attachment (the failure table's SSH-drop row) and a fresh attach
	// resumes the same terminal. The teardown is asynchronous, so wait for
	// the server to observe the detach (presence drops back to online)
	// before reattaching - otherwise the session may never actually hit a
	// zero-attachment interval.
	att.close()
	waitEvent(t, sub, &seen, "presence online after detach", func(e events.Event) bool {
		p, ok := e.Payload.(events.PresencePayload)
		return ok && e.RunID == runID && p.State == events.PresenceOnline
	})
	att = openAttach(t, client, run.ID)

	// Inject through the control channel; the agent echoes it back.
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: run.ID, Message: "ping-e2e",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	att.waitOutput(t, "got:ping-e2e")
	if !strings.Contains(att.output(), "E2E Tester injects") {
		t.Errorf("attach output missing inject banner: %q", att.output())
	}

	// Agent exits; the attach ends (server closes the channel after the
	// session ends) and the run parks at needs-attention with the
	// committed results published.
	att.waitEnd(t)
	ev := waitEvent(t, sub, &seen, "run.status needs-attention", statusOf(domain.RunNeedsAttention))
	if p := ev.Payload.(events.RunStatusPayload); p.Reason != "agent exited; results committed" {
		t.Fatalf("needs-attention reason = %q", p.Reason)
	}
	waitEvent(t, sub, &seen, "git.branch", func(e events.Event) bool {
		p, ok := e.Payload.(events.GitBranchPayload)
		return ok && e.RunID == runID && p.Branch == run.Branch && p.Commit != ""
	})

	// The run branch is fetchable over the SSH git transport.
	var pull protocol.RunPullResult
	if err := ctrl.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: run.ID}, &pull); err != nil {
		t.Fatalf("run.pull: %v", err)
	}
	if pull.Branch != run.Branch || pull.RepoPath != "/"+string(ws.ID)+".git" {
		t.Fatalf("run.pull = %+v", pull)
	}
	runGit(t, seedDir, gitEnv, "fetch", "-q", repoURL, pull.Branch)
	if got := runGit(t, seedDir, gitEnv, "show", "FETCH_HEAD:result.txt"); got != "hello-from-agent\n" {
		t.Fatalf("result.txt at branch tip = %q", got)
	}
	if got := strings.TrimSpace(runGit(t, seedDir, gitEnv, "log", "-1", "--format=%s", "FETCH_HEAD")); got != "aether: integration e2e" {
		t.Fatalf("branch tip subject = %q", got)
	}

	assertLifecycle(t, seen, runID, member.ID)

	// A workspace created while the server is running is usable without a
	// restart: the first transport touch lazily creates its bare repo.
	lateWS := &domain.Workspace{Name: "e2e-late", Environment: domain.WorkspaceEnvironment{CustomImage: image}}
	if err = srv.Store().CreateWorkspace(ctx, lateWS); err != nil {
		t.Fatalf("seed late workspace: %v", err)
	}
	lateURL := fmt.Sprintf("ssh://aether@%s/%s.git", addr, lateWS.ID)
	runGit(t, seedDir, gitEnv, "push", "-q", lateURL, "main")
	runGit(t, seedDir, gitEnv, "fetch", "-q", lateURL, "main")

	// Clean shutdown on cancellation.
	stopServer()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server did not shut down")
	}
	verifyNoLeaks(t)
}

// assertLifecycle checks the collected bus traffic against the contract's
// publication and lifecycle tables.
func assertLifecycle(t *testing.T, seen []events.Event, run domain.RunID, actor domain.MemberID) {
	t.Helper()
	var statuses []domain.RunStatus
	var presences []events.PresenceState
	steer, banner := false, false
	for _, e := range seen {
		if e.RunID != run {
			continue
		}
		switch p := e.Payload.(type) {
		case events.RunStatusPayload:
			statuses = append(statuses, p.To)
		case events.PresencePayload:
			presences = append(presences, p.State)
			if e.ActorID != actor {
				t.Errorf("presence actor = %q, want %q", e.ActorID, actor)
			}
		case events.TimelinePayload:
			if p.Kind == events.TimelineSteer {
				steer = true
				if p.Message != "ping-e2e" || e.ActorID != actor {
					t.Errorf("steer entry = %+v actor %q", p, e.ActorID)
				}
			}
		case events.GitBranchPayload:
			banner = true
		}
	}
	want := []domain.RunStatus{domain.RunProvisioning, domain.RunRunning, domain.RunNeedsAttention}
	if len(statuses) != len(want) {
		t.Fatalf("run.status transitions = %v, want %v", statuses, want)
	}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("run.status transitions = %v, want %v", statuses, want)
		}
	}
	if !slicesContains(presences, events.PresenceWatching) || !slicesContains(presences, events.PresenceOnline) {
		t.Errorf("presence states = %v, want watching then online", presences)
	}
	if !steer {
		t.Error("no workspace.timeline steer entry observed")
	}
	if !banner {
		t.Error("no git.branch event observed")
	}
}

func slicesContains(states []events.PresenceState, want events.PresenceState) bool {
	for _, s := range states {
		if s == want {
			return true
		}
	}
	return false
}

// pickRuntime prefers the real Docker daemon, falling back to the
// in-process e2eRuntime when it is unreachable. On the real path it also
// returns a leak check asserting no container labeled for this test
// survives the run, and sweeps orphans a previously interrupted test
// process may have left behind.
func pickRuntime(t *testing.T) (runtime.Runtime, string, func(*testing.T)) {
	t.Helper()
	docker, err := runtime.NewDocker(
		runtime.WithLabels(map[string]string{"aether.test": t.Name()}),
		runtime.WithNetworkMode("none"),
	)
	if err == nil {
		probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if perr := docker.Destroy(probeCtx, "aether-e2e-daemon-probe"); perr == nil {
			t.Cleanup(func() { _ = docker.Close() })
			t.Log("using real Docker runtime")
			cli := newDockerCLI(t)
			label := "aether.test=" + t.Name()
			// A killed test process leaves nothing alive to Destroy its
			// container and the temp data dir defeats reboot recovery, so
			// self-heal any leak from a prior interrupted invocation.
			removeLabeledContainers(t, cli, label)
			t.Cleanup(func() { removeLabeledContainers(t, cli, label) })
			verify := func(t *testing.T) {
				t.Helper()
				if leaked := labeledContainers(t, cli, label); len(leaked) > 0 {
					t.Errorf("containers leaked after clean shutdown: %v", leaked)
				}
			}
			return docker, "busybox", verify
		}
		_ = docker.Close()
	}
	t.Log("Docker daemon unreachable; using in-process e2e runtime")
	return newE2ERuntime(), "e2e/fake", func(*testing.T) {}
}

func newDockerCLI(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func labeledContainers(t *testing.T, cli *client.Client, label string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", label)),
	})
	if err != nil {
		t.Fatalf("list containers by label %q: %v", label, err)
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return ids
}

func removeLabeledContainers(t *testing.T, cli *client.Client, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range labeledContainers(t, cli, label) {
		t.Logf("removing leaked container %s (label %s)", id, label)
		if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
			t.Logf("remove leaked container %s: %v", id, err)
		}
	}
}

func waitSSHAddr(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a := srv.SSHAddr(); a != nil {
			return a.String()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("SSH listener never bound")
	return ""
}

// waitEvent returns the first event (already seen or newly received)
// matching pred, accumulating everything read into *seen.
func waitEvent(t *testing.T, sub events.Subscription, seen *[]events.Event, desc string, pred func(events.Event) bool) events.Event {
	t.Helper()
	for _, e := range *seen {
		if pred(e) {
			return e
		}
	}
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				t.Fatalf("bus subscription closed waiting for %s: %v", desc, sub.Err())
			}
			*seen = append(*seen, e)
			if pred(e) {
				return e
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s (saw %d events)", desc, len(*seen))
		}
	}
}

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("integration tests need a real %s binary: %v", name, err)
	}
}

func writeClientKey(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return path, signer
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v (stderr %q)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func dialSSH(t *testing.T, addr string, signer ssh.Signer) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "aether",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// pipeRWC glues a subsystem's stdout/stdin pipes into one ReadWriteCloser.
type pipeRWC struct {
	io.Reader
	io.WriteCloser
}

func openControl(t *testing.T, client *ssh.Client) *protocol.Client {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("control session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestSubsystem(protocol.SubsystemControl); err != nil {
		t.Fatalf("aether-control subsystem: %v", err)
	}
	return protocol.NewClient(pipeRWC{stdout, stdin})
}

// attachConn is a live aether-attach subsystem channel with its output
// pumped into a buffer.
type attachConn struct {
	sess  *ssh.Session
	stdin io.WriteCloser

	mu  sync.Mutex
	buf bytes.Buffer
	eof bool
}

func openAttach(t *testing.T, client *ssh.Client, runID string) *attachConn {
	t.Helper()
	a, err := tryAttach(t, client, runID)
	if err != nil {
		t.Fatalf("attach to run %s: %v", runID, err)
	}
	return a
}

// tryAttach opens the attach subsystem and reads its ack, returning the
// refusal rather than failing so a caller racing the server - a run whose
// PTY session is still being recovered - can retry.
func tryAttach(t *testing.T, client *ssh.Client, runID string) (*attachConn, error) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("attach session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err = sess.RequestPty("xterm-256color", 30, 120, ssh.TerminalModes{}); err != nil {
		t.Fatalf("pty-req: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = sess.RequestSubsystem(protocol.SubsystemAttach); err != nil {
		t.Fatalf("aether-attach subsystem: %v", err)
	}
	header, err := json.Marshal(protocol.AttachRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write(append(header, '\n')); err != nil {
		t.Fatalf("write attach header: %v", err)
	}
	r := bufio.NewReader(stdout)
	line, err := protocol.ReadLine(r)
	if err != nil {
		t.Fatalf("read attach ack: %v", err)
	}
	var ack protocol.AttachResponse
	if err := json.Unmarshal(line, &ack); err != nil || !ack.OK {
		return nil, fmt.Errorf("attach ack = %s (err %v)", line, err)
	}
	if ack.Cols != 120 || ack.Rows != 30 {
		t.Fatalf("attach geometry = %dx%d, want 120x30", ack.Cols, ack.Rows)
	}
	a := &attachConn{sess: sess, stdin: stdin}
	go a.pump(r)
	return a, nil
}

func (a *attachConn) pump(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		a.mu.Lock()
		if n > 0 {
			a.buf.Write(buf[:n])
		}
		if err != nil {
			a.eof = true
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
	}
}

// close drops the attachment (a detach); the run and its PTY session are
// unaffected.
func (a *attachConn) close() { _ = a.sess.Close() }

func (a *attachConn) output() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.String()
}

// waitEnd blocks until the server closes the attach stream (EOF on the
// channel's output).
func (a *attachConn) waitEnd(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		ended := a.eof
		a.mu.Unlock()
		if ended {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("attach stream never ended; output %q", a.output())
}

func (a *attachConn) waitOutput(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if strings.Contains(a.output(), substr) {
			return
		}
		a.mu.Lock()
		ended := a.eof
		a.mu.Unlock()
		if ended {
			t.Fatalf("attach stream ended before %q appeared; output %q", substr, a.output())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in attach output %q", substr, a.output())
}
