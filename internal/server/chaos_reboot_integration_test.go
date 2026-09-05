//go:build integration

package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// chaosAgent is the deterministic agent for the reboot scenarios. It makes
// a file change first (so an interrupted run has real work to preserve),
// announces itself, then parks on stdin until the test injects a line -
// which is how a scenario keeps a run alive across the server's death and
// proves the reattached PTY still reaches the agent. The leading sleep
// keeps the banner behind the supervisor's attach point, as in the shipped
// e2e agent.
const chaosAgent = `sleep 1
printf 'partial work\n' > progress.txt
echo agent-ready
read line
echo "got:$line"
printf 'hello-from-agent\n' > result.txt
`

// TestIntegrationChaosRebootSurvivingContainer drives the failure table's
// "Server reboot" row in the case the design spec calls seamless: the
// server is SIGKILLed mid-run - no shutdown hook, no flush - while the run
// container keeps running. On boot the scheduler must reattach supervision
// to the surviving container, not restart or interrupt the run, and both
// durable stores (SQLite and git) must come back intact.
func TestIntegrationChaosRebootSurvivingContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	env := newChaosEnv(ctx, t)

	ctrl, client := env.connect(t)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(env.ws.ID), Task: "chaos reboot survivor", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := launched.Run.ID
	branch := launched.Run.Branch
	env.sweepContainer(t, runID)
	if launched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("launched run status = %q, want running", launched.Run.Status)
	}

	att := openAttach(t, client, runID)
	waitOutput(t, att, "agent-ready")
	att.close()

	// The kill lands with the agent parked on stdin, so the container is
	// alive and idle across the whole outage.
	env.hardKill(t)
	if alive := env.containerRunning(t, runID); !alive {
		t.Fatal("the run container did not survive the server's death; this scenario needs it alive")
	}
	env.start(t)

	ctrl, client = env.connect(t)
	// SQLite survived a SIGKILL mid-run: the row reads back with the same
	// identity it was launched with.
	var got protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &got); err != nil {
		t.Fatalf("run.get after reboot: %v", err)
	}
	if got.Run.Branch != branch || got.Run.Task != "chaos reboot survivor" {
		t.Fatalf("run after reboot = branch %q task %q, want branch %q task %q",
			got.Run.Branch, got.Run.Task, branch, "chaos reboot survivor")
	}
	if got.Run.Status != string(domain.RunRunning) {
		t.Fatalf("run status after reboot = %q, want running: supervision must reattach to a "+
			"surviving container, not interrupt it", got.Run.Status)
	}

	// Supervision is only really back if the run still steers and still
	// finalizes. Inject over the recovered PTY, watch the agent answer on a
	// fresh attach, then let it exit and see the new server finalize it.
	att = waitAttach(t, client, runID)
	if err := ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: runID, Message: "resume-probe",
	}, nil); err != nil {
		t.Fatalf("run.inject after reboot: %v", err)
	}
	waitOutput(t, att, "got:resume-probe")

	env.waitStatus(t, ctrl, runID, domain.RunNeedsAttention)

	// git survived too: the reattached supervisor committed and published
	// the agent's work on the run's own branch.
	head := env.fetchBranch(t, branch)
	if !strings.Contains(head.message, "aether:") {
		t.Errorf("run branch head message = %q, want the clean-exit \"aether:\" commit", head.message)
	}
	if head.files["result.txt"] != "hello-from-agent\n" {
		t.Errorf("result.txt on the published branch = %q, want the agent's post-reboot write",
			head.files["result.txt"])
	}
}

// TestIntegrationChaosRebootLostContainer drives the other half of the
// "Server reboot" row: the machine went down and took the containers with
// it. On boot the scheduler must commit the partial work as "wip:",
// publish the branch, and park the run at interrupted with its checkout
// preserved so the one-click relaunch has something to resume from.
func TestIntegrationChaosRebootLostContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	env := newChaosEnv(ctx, t)

	ctrl, client := env.connect(t)
	var launched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(env.ws.ID), Task: "chaos reboot casualty", Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := launched.Run.ID
	branch := launched.Run.Branch
	env.sweepContainer(t, runID)

	att := openAttach(t, client, runID)
	waitOutput(t, att, "agent-ready")
	att.close()

	env.hardKill(t)
	env.removeContainer(t, runID)
	env.start(t)

	ctrl, client = env.connect(t)
	env.waitStatus(t, ctrl, runID, domain.RunInterrupted)

	head := env.fetchBranch(t, branch)
	if !strings.HasPrefix(head.message, "wip:") {
		t.Errorf("run branch head message = %q, want a \"wip:\" commit preserving the partial work",
			head.message)
	}
	if head.files["progress.txt"] != "partial work\n" {
		t.Errorf("progress.txt on the published branch = %q, want the work the agent had already done",
			head.files["progress.txt"])
	}

	// One-click relaunch: the interrupted run is the source, and the new run
	// comes up running from the published branch.
	var relaunched protocol.RunResult
	if err := ctrl.Call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: runID}, &relaunched); err != nil {
		t.Fatalf("run.relaunch after an interrupted run: %v", err)
	}
	env.sweepContainer(t, relaunched.Run.ID)
	if relaunched.Run.Status != string(domain.RunRunning) {
		t.Fatalf("relaunched run status = %q, want running", relaunched.Run.Status)
	}
	att = waitAttach(t, client, relaunched.Run.ID)
	waitOutput(t, att, "agent-ready")
	if err := ctrl.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: relaunched.Run.ID}, nil); err != nil {
		t.Fatalf("run.kill the relaunched run: %v", err)
	}
}

// chaosEnv is a real aether-server child process on a fixed data directory.
// The scenarios here need a process they can SIGKILL, which an in-process
// server cannot give them: the point is that nothing on the shutdown path
// runs, and that SQLite and git come back from the dead on their own.
type chaosEnv struct {
	ctx     context.Context
	bin     string
	dataDir string
	addr    string
	keyPath string
	signer  ssh.Signer
	ws      *domain.Workspace

	mu  sync.Mutex
	cmd *exec.Cmd
}

func newChaosEnv(ctx context.Context, t *testing.T) *chaosEnv {
	t.Helper()
	requireBinary(t, "git")
	requireDockerDaemon(t)

	e := &chaosEnv{
		ctx:     ctx,
		bin:     buildServerBinary(t),
		dataDir: filepath.Join(shortTempDir(t), "data"),
		addr:    reserveAddr(t),
	}
	e.keyPath, e.signer = writeClientKey(t)
	e.seedStore(t)
	e.start(t)
	e.seedRepo(t)
	return e
}

// seedStore writes the member and workspace straight into the SQLite file
// before the server ever opens it. The child process has no
// bootstrap path a test can drive, and the store is the same contract
// either way.
func (e *chaosEnv) seedStore(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	db, err := store.Open(filepath.Join(e.dataDir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	member := &domain.Member{
		DisplayName: "Chaos Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(e.signer.PublicKey())),
		Color:       "#e6194b",
		Role:        domain.RoleAdmin,
	}
	if err := db.CreateMember(e.ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	e.ws = &domain.Workspace{
		Name:        "chaos",
		Environment: domain.WorkspaceEnvironment{},
		BaseBranch:  domain.DefaultBaseBranch,
	}
	if err := db.CreateWorkspace(e.ctx, e.ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
}

func (e *chaosEnv) gitEnv() []string {
	return append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+e.keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
}

func (e *chaosEnv) seedRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	env := e.gitEnv()
	runGit(t, dir, env, "init", "-q", "-b", "main")
	runGit(t, dir, env, "config", "user.name", "Chaos")
	runGit(t, dir, env, "config", "user.email", "chaos@localhost")
	runGit(t, dir, env, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "README.md"), "# chaos seed\n")
	writeFile(t, filepath.Join(dir, "agent.sh"), chaosAgent)
	runGit(t, dir, env, "add", "-A")
	runGit(t, dir, env, "commit", "-q", "-m", "seed")
	runGit(t, dir, env, "push", "-q", e.repoURL(), "main")
}

func (e *chaosEnv) repoURL() string {
	return fmt.Sprintf("ssh://aether@%s/%s.git", e.addr, e.ws.ID)
}

// start brings the server child up on the reserved address and waits for
// its SSH transport to answer a real handshake.
func (e *chaosEnv) start(t *testing.T) {
	t.Helper()
	cmd := exec.CommandContext(e.ctx, e.bin, "serve", "--data-dir", e.dataDir, "--addr", e.addr)
	cmd.Env = append(os.Environ(), "AETHER_FAKE_AGENT=sh /workspace/agent.sh")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start aether-server: %v", err)
	}
	e.mu.Lock()
	e.cmd = cmd
	e.mu.Unlock()
	t.Cleanup(func() { e.stop() })
	e.waitReady(t)
}

func (e *chaosEnv) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		client, err := ssh.Dial("tcp", e.addr, &ssh.ClientConfig{
			User:            "aether",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(e.signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         3 * time.Second,
		})
		if err == nil {
			_ = client.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("aether-server never accepted SSH on %s", e.addr)
}

// hardKill SIGKILLs the server: no signal handler, no graceful shutdown,
// no flush. Everything the next boot finds was already durable.
func (e *chaosEnv) hardKill(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	cmd := e.cmd
	e.cmd = nil
	e.mu.Unlock()
	if cmd == nil {
		t.Fatal("no server process to kill")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL the server: %v", err)
	}
	_ = cmd.Wait()
	// The listener has to be free before the next boot can claim it.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", e.addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s still accepting connections after the server was killed", e.addr)
}

func (e *chaosEnv) stop() {
	e.mu.Lock()
	cmd := e.cmd
	e.cmd = nil
	e.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func (e *chaosEnv) connect(t *testing.T) (*protocol.Client, *ssh.Client) {
	t.Helper()
	client := dialSSH(t, e.addr, e.signer)
	return openControl(t, client), client
}

// waitStatus polls run.get until the run reaches want. Recovery runs on
// the new server's own schedule, so the test waits for the outcome rather
// than assuming it already happened.
func (e *chaosEnv) waitStatus(t *testing.T, ctrl *protocol.Client, runID string, want domain.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		var got protocol.RunResult
		if err := ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &got); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		if got.Run.Status == string(want) {
			return
		}
		last = got.Run.Status
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("run %s stayed at %q, want %q", runID, last, want)
}

// branchHead is a published run branch's tip: its commit message and the
// files it carries, read back over the same git-over-SSH transport a member
// pulls with.
type branchHead struct {
	message string
	files   map[string]string
}

func (e *chaosEnv) fetchBranch(t *testing.T, branch string) branchHead {
	t.Helper()
	dir := t.TempDir()
	env := e.gitEnv()
	runGit(t, dir, env, "init", "-q", "-b", "scratch")
	runGit(t, dir, env, "fetch", "-q", e.repoURL(), branch)
	head := branchHead{
		message: strings.TrimSpace(runGit(t, dir, env, "log", "-1", "--format=%s", "FETCH_HEAD")),
		files:   map[string]string{},
	}
	for _, name := range strings.Fields(runGit(t, dir, env, "ls-tree", "-r", "--name-only", "FETCH_HEAD")) {
		head.files[name] = runGit(t, dir, env, "show", "FETCH_HEAD:"+name)
	}
	return head
}

// A run launched by the child server carries no test label - the child
// builds its own Docker runtime - so the scenarios address containers by
// the name the runtime derives from the run ID.
func containerName(runID string) string { return "aether-run-" + runID }

func (e *chaosEnv) containerRunning(t *testing.T, runID string) bool {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName(runID)).CombinedOutput()
	if err != nil {
		t.Logf("docker inspect %s: %v (%s)", containerName(runID), err, out)
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func (e *chaosEnv) removeContainer(t *testing.T, runID string) {
	t.Helper()
	if out, err := exec.Command("docker", "rm", "-f", containerName(runID)).CombinedOutput(); err != nil {
		t.Fatalf("docker rm -f %s: %v (%s)", containerName(runID), err, out)
	}
}

func (e *chaosEnv) sweepContainer(t *testing.T, runID string) {
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerName(runID)).Run() })
}

// serverBinary is built once per test binary: every scenario that needs a
// real one runs the same server the operator installs, statically linked
// like the release so a copy of it also runs inside a minimal image.
var serverBinary struct {
	sync.Once
	path string
	err  error
}

func buildServerBinary(t *testing.T) string {
	t.Helper()
	serverBinary.Do(func() {
		dir, err := os.MkdirTemp("", "aether-chaos-bin")
		if err != nil {
			serverBinary.err = err
			return
		}
		path := filepath.Join(dir, "aether-server")
		cmd := exec.Command("go", "build", "-o", path, "./cmd/aether-server")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, berr := cmd.CombinedOutput()
		if berr != nil {
			serverBinary.err = fmt.Errorf("go build ./cmd/aether-server: %w (%s)", berr, out)
			return
		}
		serverBinary.path = path
	})
	if serverBinary.err != nil {
		t.Fatalf("build the server binary: %v", serverBinary.err)
	}
	return serverBinary.path
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate the repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// reserveAddr picks a loopback port the server child can bind. The child
// only reports the address it was asked for, so the test has to choose it -
// and it has to be the same one across a restart.
func reserveAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return addr
}

// shortTempDir is a temp directory outside the test's own name-derived
// path: a run's coordination socket lives under the data directory, and
// the 108-byte unix socket limit does not survive t.TempDir()'s naming.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aether-chaos")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// requireDockerDaemon skips when Docker is unreachable: a server child
// process always builds its own Docker runtime, so these scenarios have no
// in-process fallback the way the rest of the suite does.
func requireDockerDaemon(t *testing.T) {
	t.Helper()
	requireBinary(t, "docker")
	if err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		t.Skip("Docker daemon unreachable; the reboot chaos scenarios need a server child process " +
			"with a real container runtime")
	}
}

// waitOutput blocks until the attachment has seen want.
func waitOutput(t *testing.T, att *attachConn, want string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(att.output(), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("attachment never showed %q; saw %q", want, att.output())
}
