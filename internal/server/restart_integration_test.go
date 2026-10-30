//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/acphost"
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

// history reads every item the run's log holds, oldest first.
func (e *soakEnv) history(runID string) ([]acphost.Item, error) {
	var all []acphost.Item
	before := int64(0)
	for {
		var page protocol.RunACPHistoryResult
		if err := e.call(protocol.MethodRunACPHistory, protocol.RunACPHistoryParams{
			RunID: runID, BeforeSeq: before, Limit: protocol.ACPHistoryMaxLimit,
		}, &page); err != nil {
			return nil, err
		}
		var items []acphost.Item
		for _, f := range page.Frames {
			var it acphost.Item
			if json.Unmarshal(f.Item, &it) == nil {
				items = append(items, it)
			}
		}
		all = append(items, all...)
		if len(page.Frames) < protocol.ACPHistoryMaxLimit || page.OldestSeq <= 1 {
			return all, nil
		}
		before = page.OldestSeq
	}
}

func dockerOut(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type soakContainer struct {
	id, started string
}

func containerIdentity(t *testing.T, runID string) soakContainer {
	t.Helper()
	out, err := dockerOut("inspect", "--format", "{{.Id}} {{.State.StartedAt}}", containerName(runID))
	if err != nil {
		t.Fatalf("inspect %s: %v (%s)", containerName(runID), err, out)
	}
	id, started, _ := strings.Cut(out, " ")
	return soakContainer{id: id, started: started}
}

func containerFile(runID, path string) (string, error) {
	return dockerOut("exec", containerName(runID), "cat", path)
}

// soakEvent mirrors the soakagent's record.
type soakEvent struct {
	T    int64  `json:"t"`
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	Dur  int64  `json:"dur_ms"`
	ID   string `json:"id"`
	Err  string `json:"err"`
}

func (e soakEvent) done() time.Time { return time.UnixMilli(e.T + e.Dur) }

func readEvents(runID, name string) []soakEvent {
	raw, err := containerFile(runID, "/workspace/soak/"+name+".jsonl")
	if err != nil {
		return nil
	}
	var out []soakEvent
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev soakEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// gapAround is the time between the last success that completed before the
// outage began and the first that completed after it began.
func gapAround(events []soakEvent, kind string, down time.Time) (time.Duration, bool) {
	var before, after time.Time
	for _, ev := range events {
		if ev.Kind != kind || !ev.OK {
			continue
		}
		d := ev.done()
		if d.Before(down) {
			if d.After(before) {
				before = d
			}
		} else if after.IsZero() || d.Before(after) {
			after = d
		}
	}
	if before.IsZero() || after.IsZero() {
		return 0, false
	}
	return after.Sub(before), true
}

// watcher samples a run's status and streamed ticks through the restarts.
type soakWatcher struct {
	mu sync.Mutex
	// arrivals is when the server's stream first showed each numbered line.
	arrivals map[int]time.Time
	lastTick int
	items    []string
	statuses []string
	ticks    []tickObs
}

type tickObs struct {
	at time.Time
	n  int
}

func (w *soakWatcher) run(ctx context.Context, e *soakEnv, runID string) {
	for ctx.Err() == nil {
		var got protocol.RunResult
		if err := e.call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &got); err == nil {
			w.mu.Lock()
			w.statuses = append(w.statuses, got.Run.Status+"/"+got.Run.Reason)
			w.mu.Unlock()
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// acpStream is an open aether-acp stream: the ack, then one ACPFrame a line.
type acpStream struct {
	ack    protocol.ACPStreamResponse
	lines  *bufio.Reader
	closer func()
}

func (s *acpStream) close() { s.closer() }

func (e *soakEnv) openACP(req protocol.ACPStreamRequest) (*acpStream, error) {
	client, err := e.dial()
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	closer := func() { _ = sess.Close(); _ = client.Close() }
	stdin, err := sess.StdinPipe()
	if err != nil {
		closer()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		closer()
		return nil, err
	}
	if err = sess.RequestSubsystem(protocol.SubsystemACP); err != nil {
		closer()
		return nil, err
	}
	header, _ := json.Marshal(req)
	if _, err = stdin.Write(append(header, '\n')); err != nil {
		closer()
		return nil, err
	}
	s := &acpStream{lines: bufio.NewReaderSize(stdout, 1<<20), closer: closer}
	line, err := s.lines.ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &s.ack)
	}
	if err == nil && !s.ack.OK {
		err = fmt.Errorf("aether-acp refused: %d %s", s.ack.Code, s.ack.Error)
	}
	if err != nil {
		closer()
		return nil, err
	}
	return s, nil
}

// ticks follows the run's stream through every restart, reconnecting from the
// last item it saw, and records when each streamed line first arrived.
func (w *soakWatcher) follow(ctx context.Context, e *soakEnv, runID string) {
	var after int64
	for ctx.Err() == nil {
		s, err := e.openACP(protocol.ACPStreamRequest{RunID: runID, AfterSeq: after})
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go func() { <-ctx.Done(); s.close() }()
		for {
			line, err := s.lines.ReadBytes('\n')
			if err != nil {
				break
			}
			var f protocol.ACPFrame
			var it acphost.Item
			if json.Unmarshal(line, &f) != nil || json.Unmarshal(f.Item, &it) != nil {
				continue
			}
			after = max(after, it.Seq)
			if it.Message == nil || it.Message.Role != "assistant" {
				continue
			}
			n := 0
			for _, l := range strings.Split(it.Message.Text, "\n") {
				if v, ok := strings.CutPrefix(l, "tick "); ok {
					if c, convErr := strconv.Atoi(v); convErr == nil && c > n {
						n = c
					}
				}
			}
			w.mu.Lock()
			if w.arrivals == nil {
				w.arrivals = map[int]time.Time{}
			}
			for k := w.lastTick + 1; k <= n; k++ {
				w.arrivals[k] = time.Now()
			}
			w.lastTick = max(w.lastTick, n)
			if n > 0 && (len(w.ticks) == 0 || w.ticks[len(w.ticks)-1].n < n) {
				w.ticks = append(w.ticks, tickObs{time.Now(), n})
			}
			w.mu.Unlock()
		}
		s.close()
	}
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

func countKind(events []soakEvent, kind string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func soakTurnEnded(e *soakEnv, runID string) bool {
	items, _ := e.history(runID)
	for _, it := range items {
		if it.Kind == acphost.KindTurnEnd {
			return true
		}
	}
	return false
}

func waitUntil(d time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool, dump ...func() string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, f := range dump {
		t.Log(f())
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func pendingRequest(e *soakEnv, runID string) string {
	items, err := e.history(runID)
	if err != nil {
		return ""
	}
	for _, it := range items {
		if it.Kind == acphost.KindRequest && it.Request != nil && it.Request.Status == acphost.RequestPending {
			return it.Request.ID
		}
	}
	return ""
}

func assertSoak(t *testing.T, e *soakEnv, prof soakProfile, ids map[string]string, identity map[string]soakContainer, watchers map[string]*soakWatcher) {
	t.Helper()
	logs := map[string][]soakEvent{}
	for _, name := range prof.probes {
		logs[name] = readEvents(ids[name], name)
	}

	var report bytes.Buffer
	fmt.Fprintf(&report, "\nrestart availability (ms between the last success before and the first success after the signal)\n")
	fmt.Fprintf(&report, "%-10s %8s %8s %8s %8s %8s %10s\n", "restart", "down_ms", "cli.a", "mcp.a", "cli.b", "mcp.b", "stream.a")
	for _, o := range e.outages {
		cell := func(name, kind string) string {
			if g, ok := gapAround(logs[name], kind, o.down); ok {
				return strconv.FormatInt(g.Milliseconds(), 10)
			}
			return "-"
		}
		stream := "-"
		w := watchers["a"]
		w.mu.Lock()
		var before, after time.Time
		for _, ob := range w.ticks {
			if ob.at.Before(o.down) {
				before = ob.at
			} else if after.IsZero() {
				after = ob.at
			}
		}
		w.mu.Unlock()
		if !before.IsZero() && !after.IsZero() {
			stream = strconv.FormatInt(after.Sub(before).Milliseconds(), 10)
		}
		fmt.Fprintf(&report, "%-10s %8d %8s %8s %8s %8s %10s\n", o.name, o.up.Sub(o.down).Milliseconds(),
			cell("a", "cli"), cell("a", "mcp"), cell("b", "cli"), cell("b", "mcp"), stream)
	}

	if w := watchers["a"]; w != nil && prof.external && prof.mail {
		emit := map[int]time.Time{}
		for _, ev := range readEvents(ids["a"], "ticker") {
			if n, err := strconv.Atoi(ev.ID); err == nil && ev.Kind == "tick" {
				emit[n] = time.UnixMilli(ev.T)
			}
		}
		w.mu.Lock()
		fmt.Fprintf(&report, "agent-side stream delay (server receipt minus the agent writing the line), max per window around each restart:\n")
		for _, o := range e.outages {
			worst, at := time.Duration(0), 0
			for n, em := range emit {
				arr, ok := w.arrivals[n]
				if !ok || em.Before(o.down.Add(-time.Second)) || em.After(o.up.Add(5*time.Second)) {
					continue
				}
				if d := arr.Sub(em); d > worst {
					worst, at = d, n
				}
			}
			fmt.Fprintf(&report, "  %-10s max %5d ms at tick %d\n", o.name, worst.Milliseconds(), at)
		}
		var steady time.Duration
		for n, em := range emit {
			if arr, ok := w.arrivals[n]; ok && arr.Sub(em) > steady {
				steady = arr.Sub(em)
			}
		}
		var never []int
		for n := range emit {
			if _, ok := w.arrivals[n]; !ok {
				never = append(never, n)
			}
		}
		sort.Ints(never)
		fmt.Fprintf(&report, "  worst of all %d lines %d ms; lines the live stream never showed: %v\n", len(emit), steady.Milliseconds(), never)
		w.mu.Unlock()
	}

	failures := map[string]int{}
	for name, evs := range logs {
		for _, ev := range evs {
			if !ev.OK {
				failures[name+"."+ev.Kind]++
			}
		}
	}
	keys := make([]string, 0, len(failures))
	for k := range failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&report, "agent-visible failed calls:")
	total := 0
	for _, k := range keys {
		fmt.Fprintf(&report, " %s=%d", k, failures[k])
		total += failures[k]
	}
	if total == 0 {
		fmt.Fprintf(&report, " none")
	}
	fmt.Fprintln(&report)
	if total > 0 {
		for name, evs := range logs {
			shown := 0
			for _, ev := range evs {
				if !ev.OK && shown < 3 {
					fmt.Fprintf(&report, "  first failures %s.%s at +%dms: %s\n", name, ev.Kind, ev.T-e.outages[0].down.UnixMilli(), firstLine(ev.Err))
					shown++
				}
			}
		}
		t.Errorf("%d agent-visible call failures", total)
	}

	for _, name := range prof.probes {
		if !prof.mail {
			break
		}
		other := map[string]string{"a": "b", "b": "a"}[name]
		recv := map[string]int{}
		for _, ev := range logs[other] {
			if ev.Kind == "recv" {
				recv[ev.ID]++
			}
		}
		lost, dup, sent, unsaid := 0, 0, 0, 0
		for _, ev := range logs[name] {
			if ev.Kind != "send" {
				continue
			}
			n := recv[ev.ID]
			switch {
			case ev.OK:
				sent++
				if n == 0 {
					lost++
				}
			case n > 0 && !strings.Contains(ev.Err, "may already have applied"):
				unsaid++
				t.Errorf("send %s failed with %q but was delivered: the error must say it may already have applied", ev.ID, firstLine(ev.Err))
			}
			if n > 1 {
				dup++
			}
		}
		fmt.Fprintf(&report, "mail %s->%s: acknowledged sends=%d lost=%d duplicated=%d misreported-failures=%d\n", name, other, sent, lost, dup, unsaid)
		if lost > 0 || dup > 0 {
			t.Errorf("mail %s->%s: %d lost, %d duplicated of %d sends", name, other, lost, dup, sent)
		}
	}

	if prof.burst {
		for _, name := range prof.probes {
			termLog, _ := containerFile(ids[name], "/workspace/soak/term.log")
			for _, ev := range logs[name] {
				if ev.Kind != "term-input" {
					continue
				}
				applied := strings.Count(termLog, ev.ID)
				fmt.Fprintf(&report, "terminal input %s: ok=%v applied=%d %s\n", ev.ID, ev.OK, applied, firstLine(ev.Err))
				if applied > 1 {
					t.Errorf("unkeyed terminal input %s was applied %d times", ev.ID, applied)
				}
				if !ev.OK && applied > 0 && !strings.Contains(ev.Err, "may already have applied") {
					t.Errorf("terminal input %s failed with %q but was applied", ev.ID, firstLine(ev.Err))
				}
			}
		}
	}
	for _, name := range prof.probes {
		id := ids[name]
		marker, err := containerFile(id, "/workspace/soak/marker."+name)
		want := make([]string, prof.long)
		for i := range want {
			want[i] = strconv.Itoa(i + 1)
		}
		got := strings.Fields(marker)
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("run %s: the long command's marker has %d lines, want %d contiguous (err %v)", name, len(got), prof.long, err)
		}
		now := containerIdentity(t, id)
		if now != identity[id] {
			t.Errorf("run %s: the container changed: %+v -> %+v", name, identity[id], now)
		}
		fmt.Fprintf(&report, "long command %s: %d/%d lines, container %.12s started %s\n", name, len(got), prof.long, now.id, now.started)
	}

	for _, name := range append(append([]string{}, prof.probes...), "c") {
		if name == "b" && prof.external && prof.mail {
			continue
		}
		id := ids[name]
		items, err := e.history(id)
		if err != nil {
			t.Errorf("run %s: read the log: %v", name, err)
			continue
		}
		var interrupted []string
		var text strings.Builder
		for _, it := range items {
			if it.Kind == acphost.KindNotice && it.Notice != nil {
				fmt.Fprintf(&report, "run %s notice: %s: %s\n", name, it.Notice.Title, firstLine(it.Notice.Description))
			}
			switch {
			case it.Kind == acphost.KindNotice && it.Notice != nil && strings.Contains(strings.ToLower(it.Notice.Title+it.Notice.Description), "interrupt"):
				interrupted = append(interrupted, it.Notice.Title+": "+firstLine(it.Notice.Description))
			case it.Kind == acphost.KindTurnEnd && it.StopReason != "" && it.StopReason != "end_turn":
				interrupted = append(interrupted, "turn_end:"+it.StopReason)
			case it.Kind == acphost.KindMessage && it.Message != nil && it.Message.Role == "assistant":
				text.WriteString(it.Message.Text)
				if len(it.Message.Text) > 20 {
					fmt.Fprintf(&report, "run %s coalesced message item seq=%d len=%d starts %q\n", name, it.Seq, len(it.Message.Text), it.Message.Text[:16])
				}
			}
		}
		fmt.Fprintf(&report, "run %s: interrupted-turn markers=%v\n", name, interrupted)
		if len(interrupted) > 0 {
			t.Errorf("run %s: the turn was interrupted: %v", name, interrupted)
		}
		switch name {
		case "c":
			if !strings.Contains(text.String(), "permission: allow") {
				t.Errorf("run C: the answered permission request never reached the agent; assistant said %q", text.String())
			}
		default:
			seen := map[int]int{}
			top := 0
			for _, line := range strings.Split(text.String(), "\n") {
				if v, ok := strings.CutPrefix(line, "tick "); ok {
					if n, convErr := strconv.Atoi(v); convErr == nil {
						seen[n]++
						top = max(top, n)
					}
				}
			}
			missing, dup := 0, 0
			var gaps []string
			for n := 1; n <= top; n++ {
				switch seen[n] {
				case 0:
					missing++
					gaps = append(gaps, fmt.Sprintf("-%d", n))
				case 1:
				default:
					dup++
					gaps = append(gaps, fmt.Sprintf("+%d", n))
				}
			}
			if len(gaps) > 0 {
				fmt.Fprintf(&report, "run %s: stream anomalies (- missing, + repeated): %s\n", name, strings.Join(gaps, " "))
			}
			fmt.Fprintf(&report, "run %s: streamed ticks=%d missing=%d duplicated=%d\n", name, top, missing, dup)
			if (!prof.external || prof.mail) && (missing > 0 || dup > 0 || top == 0) {
				t.Errorf("run %s: the stream lost %d and repeated %d of %d lines", name, missing, dup, top)
			}
			w := watchers[name]
			w.mu.Lock()
			bad := map[string]bool{}
			for _, s := range w.statuses {
				if strings.Contains(s, "interrupt") || strings.HasPrefix(s, "failed") {
					bad[s] = true
				}
			}
			w.mu.Unlock()
			if len(bad) > 0 {
				t.Errorf("run %s showed %v during the restarts", name, bad)
			}
		}
	}
	t.Log(report.String())
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 700 {
		line = line[:700]
	}
	return line
}
