//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// The restart acceptance harness: an Enhanced run must keep working through
// an ordinary server update or restart. It runs a real aether-server binary as
// a child process against real Docker, three Enhanced runs whose scripted agent
// is the acpmock agent, and in each of two of them the soakagent fixture, which
// starts a long shell command and calls the server through the staged CLI and
// the staged MCP bridge every 100 ms while sending peer mail. The third run
// holds a permission request open. The test then restarts the server five ways
// and prints, per restart, how long each channel was silent.
//
// It fails on the first thing a user would notice: an interrupted turn, a
// failed call, a lost or duplicated message, a command that did not finish in
// the same container.

const (
	soakVersionB = "v0.0.0-soak-b"
	soakImage    = "busybox:1.36"
	// soakFullEnv gates the full matrix, which takes about four minutes.
	soakFullEnv = "AETHER_RESTART_FULL"
)

// soakProfile is one scenario: which runs probe the server, for how long, and
// how the server is restarted.
type soakProfile struct {
	// probes are the agent names; "b" and the mail checks exist only when
	// mail is set, and without it "a" sends to the permission run.
	probes []string
	mail   bool
	// external runs the probe as a separate process in the container, not as
	// the agent's turn, so it measures the coordination path alone.
	external bool
	// burst fires keyed sends and one terminal input around every restart.
	burst bool
	// extra is how many idle Enhanced runs share the server.
	extra int
	// every is the probes' status interval.
	every    time.Duration
	duration time.Duration
	long     int
	steps    func(e *soakEnv) []soakStep
}

func (e *soakEnv) swapTo(bin string) func(t *testing.T) {
	return func(t *testing.T) { copyExecutable(t, bin, e.installed) }
}

type soakStep struct {
	name string
	at   time.Duration
	do   func(t *testing.T, e *soakEnv)
}

type soakOutage struct {
	name     string
	down, up time.Time
}

// soakEnv is one server child process the test restarts at will.
type soakEnv struct {
	ctx       context.Context
	dataDir   string
	addr      string
	signer    ssh.Signer
	keyPath   string
	installed string
	binA      string
	binB      string
	ws        *domain.Workspace

	outages []soakOutage
	// burstHook, when set, starts a burst of in-flight calls just before a
	// restart.
	burstHook func()

	mu  sync.Mutex
	cmd *exec.Cmd
	// exited closes when the current child has been reaped.
	exited chan struct{}
}

func buildSoakBinary(t *testing.T, pkg, out string, ldflags string) string {
	t.Helper()
	args := []string{"build", "-o", out}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, output)
	}
	return out
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	tmp := to + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, to); err != nil {
		t.Fatal(err)
	}
}

func newSoakEnv(ctx context.Context, t *testing.T) *soakEnv {
	t.Helper()
	requireBinary(t, "git")
	requireDockerDaemon(t)
	root := shortTempDir(t)
	e := &soakEnv{
		ctx:       ctx,
		dataDir:   filepath.Join(root, "data"),
		addr:      reserveAddr(t),
		installed: filepath.Join(root, "aether-server"),
		binA:      filepath.Join(root, "build-a"),
		binB:      filepath.Join(root, "build-b"),
	}
	buildSoakBinary(t, "./cmd/aether-server", e.binA, "")
	buildSoakBinary(t, "./cmd/aether-server", e.binB,
		"-X github.com/3xDevOps/Aether/internal/version.Version="+soakVersionB)
	copyExecutable(t, e.binA, e.installed)
	e.keyPath, e.signer = writeClientKey(t)
	if err := os.MkdirAll(e.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := storetest.Open(filepath.Join(e.dataDir, "aether.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	member := &domain.Member{
		DisplayName: "Soak Tester", PublicKey: string(ssh.MarshalAuthorizedKey(e.signer.PublicKey())),
		Color: "#e6194b", Role: domain.RoleAdmin,
	}
	if err = db.CreateMember(ctx, member); err != nil {
		t.Fatal(err)
	}
	e.ws = &domain.Workspace{Name: "soak", BaseBranch: domain.DefaultBaseBranch}
	if err = db.CreateWorkspace(ctx, e.ws); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	t.Cleanup(e.kill)
	return e
}

func (e *soakEnv) start(t *testing.T) {
	t.Helper()
	cmd := exec.Command(e.installed, "serve", "--data-dir", e.dataDir, "--addr", e.addr,
		"--standard-image", soakImage)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start aether-server: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	e.mu.Lock()
	e.cmd, e.exited = cmd, exited
	e.mu.Unlock()
	e.waitReady(t)
}

func (e *soakEnv) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := e.dial(); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("aether-server never accepted SSH on %s", e.addr)
}

// signal sends sig to the server and waits for it to exit.
func (e *soakEnv) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	e.mu.Lock()
	cmd, exited := e.cmd, e.exited
	e.mu.Unlock()
	if cmd == nil {
		t.Fatal("no server process")
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
	select {
	case <-exited:
	case <-time.After(90 * time.Second):
		t.Fatalf("server did not exit within 90s of %v", sig)
	}
}

func (e *soakEnv) kill() {
	e.mu.Lock()
	cmd, exited := e.cmd, e.exited
	e.mu.Unlock()
	if cmd != nil {
		_ = cmd.Process.Kill()
		<-exited
	}
}

func (e *soakEnv) dial() (*ssh.Client, error) {
	return ssh.Dial("tcp", e.addr, &ssh.ClientConfig{
		User: "aether", Auth: []ssh.AuthMethod{ssh.PublicKeys(e.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), ClientVersion: cli.ClientBanner(),
		Timeout: 3 * time.Second,
	})
}

// call makes one control call over a fresh connection.
func (e *soakEnv) call(method string, params, out any) error {
	client, err := e.dial()
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck // a one-shot connection
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close() //nolint:errcheck // a one-shot session
	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	if err = sess.RequestSubsystem(protocol.SubsystemControl); err != nil {
		return err
	}
	return protocol.NewClient(pipeRWC{stdout, stdin}).Call(method, params, out)
}

func (e *soakEnv) gitEnv() []string {
	return append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+e.keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
}

// seedRepo pushes the scripted agent and the soak fixture into the workspace.
func (e *soakEnv) seedRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	env := e.gitEnv()
	runGit(t, dir, env, "init", "-q", "-b", "main")
	runGit(t, dir, env, "config", "user.name", "Soak")
	runGit(t, dir, env, "config", "user.email", "soak@localhost")
	runGit(t, dir, env, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "README.md"), "# soak\n")
	buildSoakBinary(t, "./internal/acphost/acpmock/agent", filepath.Join(dir, "acp-mock"), "")
	buildSoakBinary(t, "./internal/server/testdata/soakagent", filepath.Join(dir, "soakagent"), "")
	runGit(t, dir, env, "add", "-A")
	runGit(t, dir, env, "commit", "-q", "-m", "seed")
	runGit(t, dir, env, "push", "-q", fmt.Sprintf("ssh://aether@%s/%s.git", e.addr, e.ws.ID), "main")
}

func (e *soakEnv) register(t *testing.T) {
	t.Helper()
	if err := e.call(protocol.MethodAgentRegister, protocol.AgentRegisterParams{Definition: protocol.AgentDefinition{
		Name: "soak", Executable: "sleep",
		TUIArgs: []string{"sleep", "3600"}, HeadlessArgs: []string{"sleep", "3600"},
		ACPArgs: []string{"sh", "-c", "exec /workspace/acp-mock"},
	}}, nil); err != nil {
		t.Fatalf("agent.register: %v", err)
	}
}

func (e *soakEnv) launch(t *testing.T, task string) protocol.Run {
	t.Helper()
	var out protocol.RunResult
	if err := e.call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: task, Harness: "soak", Mode: string(domain.LaunchACP),
	}, &out); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	return out.Run
}

// TestIntegrationRestartSmoke is the CI variant: one probing run and a pending
// permission request through two SIGTERM restarts and one SIGKILL, with a
// keyed send in flight.
func TestIntegrationRestartSmoke(t *testing.T) {
	runRestartScenario(t, soakProfile{
		probes: []string{"a", "b"}, mail: true, external: true, burst: true, every: 4 * time.Second,
		duration: 60 * time.Second, long: 70,
		steps: func(e *soakEnv) []soakStep {
			return []soakStep{
				{"SIGTERM + restart", 8 * time.Second, e.restart("sigterm-1", e.term, nil)},
				{"SIGTERM + restart", 24 * time.Second, e.restart("sigterm-2", e.term, nil)},
				{"SIGKILL mid-request", 40 * time.Second, e.restart("sigkill", e.sigkill, nil)},
			}
		},
	})
}

// TestIntegrationRestartCoordination measures the coordination socket alone:
// the probe is a detached docker exec process, not the agent, so it keeps
// calling the server whatever happens to the agent session.
func TestIntegrationRestartCoordination(t *testing.T) {
	runRestartScenario(t, soakProfile{
		probes: []string{"a"}, external: true, duration: 60 * time.Second, long: 70,
		steps: func(e *soakEnv) []soakStep {
			return []soakStep{
				{"SIGTERM + restart", 8 * time.Second, e.restart("sigterm-1", e.term, nil)},
				{"SIGTERM + restart", 24 * time.Second, e.restart("sigterm-2", e.term, nil)},
				{"SIGKILL mid-request", 40 * time.Second, e.restart("sigkill", e.sigkill, nil)},
			}
		},
	})
}

// TestIntegrationRestartScaling shows how the stream delay at a restart grows
// with the number of live Enhanced runs.
func TestIntegrationRestartScaling(t *testing.T) {
	if os.Getenv(soakFullEnv) == "" {
		t.Skipf("the scaling check takes five minutes; set %s=1", soakFullEnv)
	}
	for _, extra := range []int{0, 2, 5} {
		t.Run(fmt.Sprintf("%d_live_runs", extra+2), func(t *testing.T) {
			runRestartScenario(t, soakProfile{
				probes: []string{"a", "b"}, mail: true, external: true, extra: extra, every: 4 * time.Second,
				duration: 45 * time.Second, long: 50,
				steps: func(e *soakEnv) []soakStep {
					return []soakStep{
						{"SIGTERM + restart", 6 * time.Second, e.restart("sigterm-1", e.term, nil)},
						{"SIGTERM + restart", 20 * time.Second, e.restart("sigterm-2", e.term, nil)},
					}
				},
			})
		})
	}
}

// TestIntegrationRestartAcceptance is the pre-release matrix: two mailing runs
// and a pending permission request through repeated restarts, an update to a
// second build, a rollback and a SIGKILL. It runs only with AETHER_RESTART_FULL=1.
func TestIntegrationRestartAcceptance(t *testing.T) {
	if os.Getenv(soakFullEnv) == "" {
		t.Skipf("the full restart matrix takes four minutes; set %s=1", soakFullEnv)
	}
	runRestartScenario(t, soakProfile{
		probes: []string{"a", "b"}, mail: true, external: true, burst: true, every: 4 * time.Second,
		duration: 105 * time.Second, long: 120,
		steps: func(e *soakEnv) []soakStep {
			return []soakStep{
				{"SIGTERM + restart", 8 * time.Second, e.restart("sigterm-1", e.term, nil)},
				{"SIGTERM + restart", 28 * time.Second, e.restart("sigterm-2", e.term, nil)},
				{"update to second build", 48 * time.Second, e.restart("update", e.term, e.swapTo(e.binB))},
				{"forced rollback to first build", 68 * time.Second, e.restart("rollback", e.term, e.swapTo(e.binA))},
				{"SIGKILL mid-request", 88 * time.Second, e.restart("sigkill", e.sigkill, nil)},
			}
		},
	})
}

func (e *soakEnv) term(t *testing.T)    { e.signal(t, syscall.SIGTERM) }
func (e *soakEnv) sigkill(t *testing.T) { e.signal(t, syscall.SIGKILL) }

func (e *soakEnv) restart(name string, stop func(t *testing.T), prep func(t *testing.T)) func(*testing.T, *soakEnv) {
	return func(t *testing.T, _ *soakEnv) {
		t.Helper()
		if e.burstHook != nil {
			e.burstHook()
		}
		o := soakOutage{name: name, down: time.Now()}
		stop(t)
		if prep != nil {
			prep(t)
		}
		e.start(t)
		o.up = time.Now()
		e.outages = append(e.outages, o)
	}
}

func soakEvery(prof soakProfile) time.Duration {
	if prof.every == 0 {
		return 2 * time.Second
	}
	return prof.every
}

// soakTickTask is an agent turn that streams a numbered line every 100 ms
// for the profile's duration.
func soakTickTask(prof soakProfile) string {
	return fmt.Sprintf("%s/workspace/soakagent -name ticker -ticks %d",
		acpmock.PromptRunPrefix, int((prof.duration+40*time.Second)/(100*time.Millisecond)))
}

func soakProbeCommand(prof soakProfile, name, runCID, peer string) string {
	cmd := fmt.Sprintf("/workspace/soakagent -name %s -duration %s -long %d -every %s", name, prof.duration, prof.long, soakEvery(prof))
	if prof.burst {
		cmd += " -burst"
	}
	if peer == "" && !prof.mail {
		peer = runCID
	}
	if peer != "" {
		cmd += " -peer " + peer
	}
	return cmd
}

func runRestartScenario(t *testing.T, prof soakProfile) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	e := newSoakEnv(ctx, t)
	e.start(t)
	e.seedRepo(t)
	e.register(t)

	runC := e.launch(t, "say pong")
	ids := map[string]string{"c": runC.ID}
	runs := []protocol.Run{runC}
	t.Cleanup(func() {
		for _, r := range runs {
			_, _ = dockerOut("rm", "-f", containerName(r.ID))
		}
	})
	for range prof.extra {
		r := e.launch(t, "say pong")
		runs = append(runs, r)
		waitFor(t, time.Minute, "an idle run's first turn", func() bool { return soakTurnEnded(e, r.ID) })
	}
	// An external mailing probe "b" lives in run C, which also holds the
	// permission request.
	sharedC := prof.external && prof.mail
	for _, name := range prof.probes {
		if sharedC && name == "b" {
			ids[name] = runC.ID
			continue
		}
		task := "say pong"
		switch {
		case !prof.external:
			task = acpmock.PromptRunPrefix + soakProbeCommand(prof, name, runC.ID, "")
		case prof.mail:
			task = soakTickTask(prof)
		}
		r := e.launch(t, task)
		ids[name] = r.ID
		runs = append(runs, r)
	}
	if prof.external {
		if !prof.mail {
			waitFor(t, time.Minute, "the agent's first turn", func() bool { return soakTurnEnded(e, ids["a"]) })
		}
		waitFor(t, time.Minute, "run C's first turn", func() bool { return soakTurnEnded(e, runC.ID) })
		peer := func(name string) string {
			if prof.mail {
				return map[string]string{"a": ids["b"], "b": ids["a"]}[name]
			}
			return runC.ID
		}
		for _, name := range prof.probes {
			args := append([]string{"exec", "-d", "-w", "/workspace", containerName(ids[name])},
				strings.Fields(soakProbeCommand(prof, name, "", peer(name)))...)
			if out, err := dockerOut(args...); err != nil {
				t.Fatalf("start the external probe: %v (%s)", err, out)
			}
		}
		if prof.burst {
			e.burstHook = func() {
				for _, name := range prof.probes {
					_, _ = dockerOut("exec", containerName(ids[name]), "touch", "/workspace/soak/burst")
				}
				time.Sleep(250 * time.Millisecond)
			}
		}
	}
	if prof.mail {
		waitFor(t, 3*time.Minute, "both agents exchanging mail", func() bool {
			return countKind(readEvents(ids["a"], "a"), "recv") > 0 && countKind(readEvents(ids["b"], "b"), "recv") > 0
		})
	} else {
		waitFor(t, 3*time.Minute, "the agent's first sends", func() bool {
			return countKind(readEvents(ids["a"], "a"), "send") > 0
		})
	}
	if !prof.external {
		waitFor(t, time.Minute, "run C's first turn", func() bool { return soakTurnEnded(e, runC.ID) })
	}
	if err := e.call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: runC.ID, Message: acpmock.PromptAskPermission, IdempotencyKey: "soak-ask-permission",
	}, nil); err != nil {
		t.Fatalf("run.inject on run C: %v", err)
	}
	waitFor(t, time.Minute, "the permission request on run C", func() bool {
		return pendingRequest(e, runC.ID) != ""
	}, func() string {
		items, err := e.history(runC.ID)
		out, _ := json.Marshal(items)
		return fmt.Sprintf("run C log (%v): %s", err, out)
	})
	identity := map[string]soakContainer{}
	for _, r := range runs {
		identity[r.ID] = containerIdentity(t, r.ID)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	watchers := map[string]*soakWatcher{}
	var watchWG sync.WaitGroup
	for _, name := range prof.probes {
		w := &soakWatcher{}
		watchers[name] = w
		watchWG.Add(1)
		go func() { defer watchWG.Done(); w.run(watchCtx, e, ids[name]) }()
		watchWG.Add(1)
		go func() { defer watchWG.Done(); w.follow(watchCtx, e, ids[name]) }()
	}

	steps := prof.steps(e)
	begin := time.Now()
	for _, s := range steps {
		time.Sleep(time.Until(begin.Add(s.at)))
		t.Logf("step: %s", s.name)
		s.do(t, e)
	}

	// The permission request opened before the first restart must still be
	// open, and answering it must finish the turn.
	if reqID := pendingRequest(e, runC.ID); reqID == "" {
		t.Errorf("run C: the permission request did not survive the restarts")
	} else if s, err := e.openACP(protocol.ACPStreamRequest{RunID: runC.ID, Write: true, ControlSessionID: "soak-control"}); err != nil {
		t.Errorf("run C: take control: %v", err)
	} else {
		defer s.close()
		if err = e.call(protocol.MethodRunInputAnswer, protocol.RunInputAnswerParams{
			RunID: runC.ID, RequestID: reqID, OptionID: "allow",
			ACPLease: protocol.ACPLease{ControlSessionID: s.ack.ControlSessionID, ControlGeneration: s.ack.ControlGeneration},
		}, nil); err != nil {
			t.Errorf("run C: answer the permission request: %v", err)
		}
	}

	finished := waitUntil(time.Minute, func() bool {
		for _, name := range prof.probes {
			if countKind(readEvents(ids[name], name), "end") == 0 {
				return false
			}
		}
		return true
	})
	if !finished {
		t.Errorf("an agent's turn never ended normally: its probe did not record its end")
	}
	waitUntil(time.Minute, func() bool {
		for _, name := range prof.probes {
			if _, err := containerFile(ids[name], "/workspace/soak/long.done."+name); err != nil {
				return false
			}
		}
		return true
	})
	time.Sleep(2 * time.Second)
	stopWatch()
	watchWG.Wait()

	assertSoak(t, e, prof, ids, identity, watchers)
}
