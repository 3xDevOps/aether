//go:build integration

package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// pressureAgent is the deterministic agent both pressure scenarios run. It
// announces itself, then loops on stdin instead of exiting after one line:
// the stall scenario needs an agent that goes quiet, answers a steer, and is
// still there afterwards to be killed - an agent that exits on the steer
// would finalize the run instead of coming back to running. "finish" is the
// disk scenario's way of ending a run cleanly.
const pressureAgent = `sleep 1
echo agent-ready
while read line; do
  echo "got:$line"
  if [ "$line" = "finish" ]; then
    printf 'hello-from-agent\n' > result.txt
    exit 0
  fi
done
`

// TestIntegrationChaosDiskPressure drives the failure table's "Disk
// pressure" row on the surfaces an operator actually sees: finished-run
// checkouts are reclaimed after their TTL while the branch survives, the
// dashboard's gauge accounts for all three directories that grow without
// bound, and new runs are refused once free space drops under the floor.
func TestIntegrationChaosDiskPressure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	env := newPressureEnv(ctx, t, Config{})

	// Load: several runs through the whole lifecycle, closed so their
	// checkouts become garbage the TTL can claim.
	const runs = 4
	type finished struct{ id, branch string }
	done := make([]finished, 0, runs)
	for i := range runs {
		var launched protocol.RunResult
		if err := env.ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			SessionID: string(env.sess.ID), Task: fmt.Sprintf("gc load %d", i), Harness: "fake",
		}, &launched); err != nil {
			t.Fatalf("run.launch %d: %v", i, err)
		}
		// The fake agent parks on stdin; steering it is what ends the run.
		att := openAttach(t, env.client, launched.Run.ID)
		waitOutput(t, att, "agent-ready")
		if err := env.ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
			RunID: launched.Run.ID, Message: "finish",
		}, nil); err != nil {
			t.Fatalf("run.inject %d: %v", i, err)
		}
		att.close()
		env.waitStatus(t, launched.Run.ID, domain.RunNeedsAttention)
		if err := env.ctrl.Call(protocol.MethodRunClose, protocol.RunCloseParams{
			RunID: launched.Run.ID, Outcome: string(domain.RunMerged),
		}, nil); err != nil {
			t.Fatalf("run.close %d: %v", i, err)
		}
		done = append(done, finished{launched.Run.ID, launched.Run.Branch})
	}

	// The gauge has to see the three directories that grow without bound,
	// not just the filesystem total: worktrees, transcripts, and the
	// persisted event log's database.
	before := env.disk(t)
	if before.TotalBytes == 0 {
		t.Fatal("disk gauge reports no filesystem total")
	}
	if before.WorktreeBytes == 0 {
		t.Error("disk gauge reports no worktree bytes after four runs; the gauge must cover the " +
			"checkouts the scheduler garbage-collects")
	}
	if before.TranscriptBytes == 0 {
		t.Error("disk gauge reports no transcript bytes after four runs; transcripts grow unbounded " +
			"and the gauge must cover them")
	}
	if before.DatabaseBytes == 0 {
		t.Error("disk gauge reports no database bytes; the persisted event log grows unbounded and " +
			"the gauge must cover it")
	}

	for _, f := range done {
		if _, err := os.Stat(filepath.Join(env.dataDir, "checkouts", f.id)); err != nil {
			t.Fatalf("checkout for closed run %s is already gone before any GC: %v", f.id, err)
		}
	}

	// A boot with the TTL turned down is the operator's GC: the checkouts
	// go, the branches stay, and the gauge drops to match.
	env.restart(t, Config{CheckoutTTL: time.Nanosecond})
	env.waitCheckoutsReclaimed(t, done[0].id)
	for _, f := range done {
		if _, err := os.Stat(filepath.Join(env.dataDir, "checkouts", f.id)); !os.IsNotExist(err) {
			t.Errorf("checkout for run %s survived the TTL sweep (stat err %v)", f.id, err)
		}
		if head := env.branchHeadMessage(t, f.branch); head == "" {
			t.Errorf("branch %s lost its head after the checkout was reclaimed; the branch is the "+
				"artifact and is never GC'd", f.branch)
		}
	}
	if after := env.disk(t); after.WorktreeBytes >= before.WorktreeBytes {
		t.Errorf("disk gauge worktree bytes = %d after the sweep, was %d before: the gauge does not "+
			"follow the reclaim", after.WorktreeBytes, before.WorktreeBytes)
	}

	// The floor: a server that cannot promise the configured headroom
	// refuses new work instead of filling the disk out from under the runs
	// already on it.
	env.restart(t, Config{MinFreeDiskBytes: math.MaxInt64})
	var refused protocol.RunResult
	err := env.ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(env.sess.ID), Task: "over the floor", Harness: "fake",
	}, &refused)
	if err == nil {
		t.Fatalf("run.launch (run %s) succeeded below the free-space floor; new runs must be refused",
			refused.Run.ID)
	}
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("run.launch below the floor = %v, want a %d (unavailable) error", err, protocol.CodeUnavailable)
	}
	if !strings.Contains(strings.ToLower(perr.Message), "disk") {
		t.Errorf("refusal message = %q, want it to name the disk so an operator knows what to fix",
			perr.Message)
	}
	// The refusal leaves no wreckage: a refused launch never becomes a row.
	var list protocol.RunListResult
	if err := env.ctrl.Call(protocol.MethodRunList, protocol.RunListParams{}, &list); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	for _, r := range list.Runs {
		if r.Task == "over the floor" {
			t.Errorf("refused launch left run %s behind in %s", r.ID, r.Status)
		}
	}
	// Relaunch is a new run too, and the floor holds for it.
	if err := env.ctrl.Call(protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: done[0].id}, nil); err == nil {
		t.Error("run.relaunch succeeded below the free-space floor; it provisions a new run too")
	}
}

// TestIntegrationChaosStallUX drives the failure table's "Agent crashes or
// hangs" row down its notification path: a silent agent parks the run at
// needs-attention with a reason that says why, the transition reaches the
// dashboard on the SPA's own event wire and the CLI's own run listing, and
// steering the run brings it back to running.
func TestIntegrationChaosStallUX(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := newPressureEnv(ctx, t, Config{
		StallThreshold: 2 * time.Second,
		PollInterval:   250 * time.Millisecond,
	})
	const stallTask = "stall me"
	env.parkOnStdin(t, stallTask)

	sub := env.subscribe(t)
	var launched protocol.RunResult
	if err := env.ctrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
		SessionID: string(env.sess.ID), Task: stallTask, Harness: "fake",
	}, &launched); err != nil {
		t.Fatalf("run.launch: %v", err)
	}
	runID := domain.RunID(launched.Run.ID)

	// The agent parks on stdin: no PTY output, no file changes, so the
	// stall threshold expires under it.
	var seen []events.Event
	stall := waitEvent(t, sub, &seen, "run.status needs-attention", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.To == domain.RunNeedsAttention
	})
	reason := stall.Payload.(events.RunStatusPayload).Reason
	if !strings.HasPrefix(reason, "stalled:") {
		t.Fatalf("stall transition reason = %q, want it to lead with \"stalled:\" so the badge and "+
			"the CLI can say why", reason)
	}

	// The CLI's own listing call sees it.
	var list protocol.RunListResult
	if err := env.ctrl.Call(protocol.MethodRunList, protocol.RunListParams{}, &list); err != nil {
		t.Fatalf("run.list: %v", err)
	}
	found := false
	for _, r := range list.Runs {
		if r.ID == string(runID) {
			found = true
			if r.Status != string(domain.RunNeedsAttention) {
				t.Errorf("run.list shows %s as %q, want needs-attention", r.ID, r.Status)
			}
		}
	}
	if !found {
		t.Fatalf("run %s missing from run.list", runID)
	}

	// Steering it clears the stall: the same loop that parked it puts it
	// back once the agent is talking again.
	if err := env.ctrl.Call(protocol.MethodRunInject, protocol.RunInjectParams{
		RunID: string(runID), Message: "wake",
	}, nil); err != nil {
		t.Fatalf("run.inject: %v", err)
	}
	// The transition has to be the return from needs-attention: the launch's
	// own provisioning -> running event is already in seen, and matching on
	// the destination alone would pass on that one without the stall ever
	// having cleared.
	waitEvent(t, sub, &seen, "run.status back to running", func(e events.Event) bool {
		p, ok := e.Payload.(events.RunStatusPayload)
		return ok && e.RunID == runID && p.From == domain.RunNeedsAttention && p.To == domain.RunRunning
	})

	if err := env.ctrl.Call(protocol.MethodRunKill, protocol.RunIDParams{RunID: string(runID)}, nil); err != nil {
		t.Fatalf("run.kill: %v", err)
	}
	env.waitStatus(t, string(runID), domain.RunAbandoned)
}

// pressureEnv is an in-process server on a stable data directory that the
// disk and stall scenarios restart with different tuning. Unlike the reboot
// scenarios these need no process to kill, only knobs to turn.
type pressureEnv struct {
	ctx     context.Context
	dataDir string
	image   string
	rt      runtime.Runtime
	keyPath string
	signer  ssh.Signer
	ws      *domain.Workspace
	sess    *domain.Session

	srv    *Server
	addr   string
	base   string
	tok    string
	ctrl   *protocol.Client
	client *ssh.Client
	stop   func()
}

func newPressureEnv(ctx context.Context, t *testing.T, cfg Config) *pressureEnv {
	t.Helper()
	requireBinary(t, "git")
	rt, image, _ := pickRuntime(t)
	e := &pressureEnv{
		ctx:     ctx,
		dataDir: filepath.Join(shortTempDir(t), "data"),
		image:   image,
		rt:      rt,
	}
	e.keyPath, e.signer = writeClientKey(t)
	// The task rides in argv so the fallback runtime can key a scripted
	// agent off it; the committed script itself dispatches on stdin.
	t.Setenv("AETHER_FAKE_AGENT", "sh /workspace/agent.sh {task}")
	e.boot(t, cfg)

	member := &domain.Member{
		DisplayName: "Pressure Tester",
		PublicKey:   string(ssh.MarshalAuthorizedKey(e.signer.PublicKey())),
		Color:       "#4363d8",
		Role:        domain.RoleAdmin,
	}
	if err := e.srv.Store().CreateMember(ctx, member); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	e.ws = &domain.Workspace{Name: "pressure", Image: image}
	if err := e.srv.Store().CreateWorkspace(ctx, e.ws); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	e.sess = &domain.Session{WorkspaceID: e.ws.ID, Name: "pressure", BaseBranch: "main"}
	if err := e.srv.Store().CreateSession(ctx, e.sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	e.seedRepo(t)
	e.connect(t)
	return e
}

// boot starts a server on the shared data directory, filling in the fields
// every scenario needs. cfg carries only the tuning under test.
func (e *pressureEnv) boot(t *testing.T, cfg Config) {
	t.Helper()
	cfg.DataDir = e.dataDir
	cfg.Addr = "127.0.0.1:0"
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.Runtime = e.rt
	srv, err := New(e.ctx, cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	runCtx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	e.srv = srv
	e.addr = waitSSHAddr(t, srv)
	e.base = "http://" + waitDashboardAddr(t, srv)
	e.stop = sync.OnceFunc(func() {
		cancel()
		select {
		case rerr := <-done:
			if rerr != nil {
				t.Errorf("server.Run: %v", rerr)
			}
		case <-time.After(30 * time.Second):
			t.Error("server did not shut down")
		}
	})
	t.Cleanup(func() { e.stop() })
}

// parkOnStdin teaches the fallback runtime the parked agent pressureAgent
// gives the real one: its default in-process agent exits after echoing its
// first injected line, which finalizes the run instead of returning it to
// running - the transition the stall scenario exists to prove. A no-op on
// the Docker path, where the agent script does the parking itself.
func (e *pressureEnv) parkOnStdin(t *testing.T, task string) {
	t.Helper()
	fake, ok := e.rt.(*e2eRuntime)
	if !ok {
		return
	}
	fake.script(task, func(c *e2eContainer) {
		for {
			line, ok := c.readStdinLine()
			if !ok {
				return
			}
			c.output("got:" + line + "\r\n")
		}
	})
}

func (e *pressureEnv) restart(t *testing.T, cfg Config) {
	t.Helper()
	e.stop()
	e.boot(t, cfg)
	e.connect(t)
}

// connect redials the control channel and mints a fresh dashboard token;
// both are per-server, so every restart needs new ones.
func (e *pressureEnv) connect(t *testing.T) {
	t.Helper()
	e.client = dialSSH(t, e.addr, e.signer)
	e.ctrl = openControl(t, e.client)
	var minted protocol.DashTokenMintResult
	if err := e.ctrl.Call(protocol.MethodDashTokenMint, protocol.DashTokenMintParams{}, &minted); err != nil {
		t.Fatalf("dash.token.mint: %v", err)
	}
	e.tok = minted.Token
}

func (e *pressureEnv) subscribe(t *testing.T) events.Subscription {
	t.Helper()
	sub, err := e.srv.Bus().Subscribe(e.ctx, events.SubscribeOptions{Buffer: 4096})
	if err != nil {
		t.Fatalf("subscribe bus: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func (e *pressureEnv) gitEnv() []string {
	return append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+e.keyPath+
			" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes")
}

func (e *pressureEnv) repoURL() string {
	return fmt.Sprintf("ssh://aether@%s/%s.git", e.addr, e.ws.ID)
}

func (e *pressureEnv) seedRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	env := e.gitEnv()
	runGit(t, dir, env, "init", "-q", "-b", "main")
	runGit(t, dir, env, "config", "user.name", "E2E")
	runGit(t, dir, env, "config", "user.email", "e2e@localhost")
	runGit(t, dir, env, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "README.md"), "# pressure seed\n")
	writeFile(t, filepath.Join(dir, "agent.sh"), pressureAgent)
	runGit(t, dir, env, "add", "-A")
	runGit(t, dir, env, "commit", "-q", "-m", "seed")
	runGit(t, dir, env, "push", "-q", e.repoURL(), "main")
}

// branchHeadMessage fetches a published run branch over the same
// git-over-SSH transport a member pulls with and returns its tip message.
// A branch the GC took with the checkout would fail the fetch outright.
func (e *pressureEnv) branchHeadMessage(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	env := e.gitEnv()
	runGit(t, dir, env, "init", "-q", "-b", "scratch")
	runGit(t, dir, env, "fetch", "-q", e.repoURL(), branch)
	return strings.TrimSpace(runGit(t, dir, env, "log", "-1", "--format=%s", "FETCH_HEAD"))
}

// disk reads the dashboard's gauge over its own HTTP wire, which is the
// only place it is served.
func (e *pressureEnv) disk(t *testing.T) diskGauge {
	t.Helper()
	var got diskGauge
	dashGET(t, e.base+"/api/v1/disk", e.tok, &got)
	return got
}

// diskGauge mirrors the gateway's GET /api/v1/disk body: the filesystem
// headroom plus the three Aether directories that grow without bound.
type diskGauge struct {
	UsedBytes       uint64 `json:"used_bytes"`
	TotalBytes      uint64 `json:"total_bytes"`
	FreeBytes       uint64 `json:"free_bytes"`
	WorktreeBytes   uint64 `json:"worktree_bytes"`
	TranscriptBytes uint64 `json:"transcript_bytes"`
	DatabaseBytes   uint64 `json:"database_bytes"`
}

func (e *pressureEnv) waitStatus(t *testing.T, runID string, want domain.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		var got protocol.RunResult
		if err := e.ctrl.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &got); err != nil {
			t.Fatalf("run.get: %v", err)
		}
		if got.Run.Status == string(want) {
			return
		}
		last = got.Run.Status
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("run %s stayed at %q, want %q", runID, last, want)
}

// waitCheckoutsReclaimed waits for the boot sweep, which runs on the
// scheduler's own goroutine.
func (e *pressureEnv) waitCheckoutsReclaimed(t *testing.T, runID string) {
	t.Helper()
	path := filepath.Join(e.dataDir, "checkouts", runID)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("checkout %s was never reclaimed by the TTL sweep", path)
}
