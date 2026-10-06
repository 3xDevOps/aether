package scheduler

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type acpRuntime struct {
	*fakeRuntime
	fixture acpmock.Fixture

	mu    sync.Mutex
	execs []*acpExec
}

func newACPRuntime(t *testing.T) *acpRuntime {
	fix, err := acpmock.Load("claude")
	if err != nil {
		t.Fatal(err)
	}
	return &acpRuntime{fakeRuntime: newFakeRuntime(), fixture: fix}
}

func (r *acpRuntime) StartExecTTY(context.Context, runtime.ID, runtime.ExecSpec) (runtime.ManagedExec, error) {
	return nil, errors.New("acpRuntime: no terminal execs")
}

func (r *acpRuntime) StartExecPipe(_ context.Context, id runtime.ID, spec runtime.ExecSpec) (runtime.ManagedExec, error) {
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	e := &acpExec{
		identity: runtime.ExecIdentity{ContainerID: id, ExecID: rand.Text(), CreationKey: spec.CreationKey, ClaimToken: "claim"},
		argv:     spec.Argv,
		agent:    acpmock.New(r.fixture),
		stdin:    hostOut,
		stdout:   hostIn,
		agentIn:  agentIn,
		done:     make(chan struct{}),
	}
	go func() {
		e.agent.Serve(agentIn, agentOut)
		_ = agentOut.Close()
		close(e.done)
	}()
	r.mu.Lock()
	r.execs = append(r.execs, e)
	r.mu.Unlock()
	return e, nil
}

func (r *acpRuntime) RecoverExec(_ context.Context, identity runtime.ExecIdentity) (runtime.ManagedExec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.execs {
		if e.identity == identity {
			return e, nil
		}
	}
	return nil, runtime.ErrExecUnavailable
}

func (r *acpRuntime) all() []*acpExec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.execs)
}

type acpExec struct {
	identity runtime.ExecIdentity
	argv     []string
	agent    *acpmock.Agent
	stdin    *io.PipeWriter
	stdout   *io.PipeReader
	agentIn  *io.PipeReader
	done     chan struct{}
}

func (e *acpExec) Identity() runtime.ExecIdentity { return e.identity }
func (e *acpExec) Attachment() runtime.Attachment { return e }
func (e *acpExec) Stdin() io.WriteCloser          { return e.stdin }
func (e *acpExec) Stdout() io.Reader              { return e.stdout }
func (e *acpExec) Stderr() io.Reader              { return strings.NewReader("") }
func (e *acpExec) Resize(context.Context, uint, uint) error {
	return errors.New("no terminal")
}
func (e *acpExec) Close() error  { return nil }
func (e *acpExec) Detach() error { return nil }

func (e *acpExec) Status(context.Context) (runtime.ExecState, error) {
	select {
	case <-e.done:
		code := 0
		return runtime.ExecState{Exited: true, ExitCode: &code}, nil
	default:
		return runtime.ExecState{Running: true, Attached: true}, nil
	}
}

func (e *acpExec) Wait(ctx context.Context) (runtime.ExitStatus, error) {
	select {
	case <-e.done:
		return runtime.ExitStatus{}, nil
	case <-ctx.Done():
		return runtime.ExitStatus{}, ctx.Err()
	}
}

func (e *acpExec) Stop(ctx context.Context, _ time.Duration) (runtime.ExitStatus, error) {
	_ = e.agentIn.Close()
	return e.Wait(ctx)
}

func (e *acpExec) exited() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func newACPEnv(t *testing.T) (*testEnv, *acpRuntime) {
	t.Helper()
	rt := newACPRuntime(t)
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = rt
		cfg.WorktreeMount = "/workspace"
		cfg.Harnesses["fake"] = HarnessSpec{
			TUIArgs: []string{"fake-agent", "{task}"}, ACPArgs: []string{"acp-mock"},
		}
	})
	e.rt = rt.fakeRuntime
	return e, rt
}

func (e *testEnv) launchACP(t *testing.T, task string) *domain.Run {
	t.Helper()
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, task, "fake", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return run
}

func waitItems(t *testing.T, s *Scheduler, run domain.RunID, what string, cond func([]acphost.Item) bool) []acphost.Item {
	t.Helper()
	var items []acphost.Item
	waitFor(t, what, func() bool {
		var err error
		items, err = s.ACPHistory(run, 0, 0)
		return err == nil && cond(items)
	})
	return items
}

func assistantSaid(text string) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		var said strings.Builder
		for _, it := range items {
			if it.Kind == acphost.KindMessage && it.Message.Role == "assistant" {
				said.WriteString(it.Message.Text)
			}
		}
		return strings.Contains(said.String(), text)
	}
}

func turnEnded(reason string, n int) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		count := 0
		for _, it := range items {
			if it.Kind == acphost.KindTurnEnd && it.StopReason == reason {
				count++
			}
		}
		return count >= n
	}
}

func (e *testEnv) waitAgentState(t *testing.T, run domain.RunID, state agentstatus.State) {
	t.Helper()
	waitFor(t, "agent "+string(state), func() bool {
		e.sched.mu.Lock()
		defer e.sched.mu.Unlock()
		entry := e.sched.runs[run]
		return entry != nil && entry.agentReport.State == state
	})
}

func TestEnhancedRunSendsTaskAndReportsTurns(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")

	c := e.rt.byName(string(run.ID))
	if c == nil {
		t.Fatal("no container")
	}
	cmd := strings.Join(c.spec.Command, " ")
	if !strings.Contains(cmd, "aether-run-supervisor") || strings.Contains(cmd, "fake-agent") || c.spec.Env["NO_BROWSER"] != "1" {
		t.Fatalf("container command %q env NO_BROWSER=%q, want the supervisor's login shell only", cmd, c.spec.Env["NO_BROWSER"])
	}
	execs := rt.all()
	if len(execs) != 1 || execs[0].argv[0] != "acp-mock" {
		t.Fatalf("execs %+v", execs)
	}

	items := waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	if !strings.Contains(items[slices.IndexFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindMessage && it.Message.Role == "user"
	})].Message.Text, "say pong") {
		t.Fatalf("the task was not the first prompt: %+v", items)
	}
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	fresh, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.HarnessSessionID != rt.fixture.SessionID() {
		t.Fatalf("harness_session_id %q, want %q", fresh.HarnessSessionID, rt.fixture.SessionID())
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sc.AgentSessionID != fresh.HarnessSessionID || sc.AgentExec == nil || sc.AgentExec.ExecID != execs[0].identity.ExecID {
		t.Fatalf("sidecar session %q exec %+v", sc.AgentSessionID, sc.AgentExec)
	}
	outcome, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "again", false)
	if err != nil || outcome != acphost.OutcomeSent {
		t.Fatalf("Inject = %q, %v", outcome, err)
	}
	waitItems(t, e.sched, run.ID, "the second turn", turnEnded("end_turn", 2))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
}

func TestEnhancedRunPermissionAnsweredOnce(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, acpmock.PromptAskPermission, false); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	if pending[0].Kind != "permission" {
		t.Fatalf("pending %+v", pending)
	}
	if err := e.sched.ACPAnswer(run.ID, pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ACPAnswer(run.ID, pending[0].ID, "reject", nil); !errors.Is(err, acphost.ErrAlreadyAnswered) {
		t.Fatalf("second answer: %v, want ErrAlreadyAnswered", err)
	}
	waitItems(t, e.sched, run.ID, "the agent's use of the answer", assistantSaid("permission: allow"))
	waitFor(t, "inputs cleared", func() bool { return len(e.sched.PendingInputs(run.ID)) == 0 })
}

func TestEnhancedRunFormAnswerCarriesValues(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, acpmock.PromptAskForm, false); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "form question", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	if err := e.sched.ACPAnswer(run.ID, pending[0].ID, "accept", map[string]any{"db": "postgres"}); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the agent's use of the form answer", assistantSaid(`form: accept {"db":"postgres"}`))
}

// A failed turn is not a turn end: an armed outcome report waits for one
// that succeeds.
func TestEnhancedRunFailedTurnDoesNotFinishReportedRun(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	if err := e.sched.FinishReported(t.Context(), run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, acpmock.PromptRefuse, false); err == nil {
		t.Fatal("Inject of a refused prompt succeeded")
	}
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !strings.HasPrefix(got.Reason, acpTurnFailedReason) || !strings.Contains(got.Reason, "Authentication required") {
		t.Fatalf("reason %q", got.Reason)
	}

	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "say pong", false); err != nil {
		t.Fatal(err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunCompleted)
}

func TestEnhancedRunCancel(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitAgentState(t, run.ID, agentstatus.Working)
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "cancelled turn", turnEnded("cancelled", 1))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
}

func TestEnhancedRunInputAfterAgentExitIsNotSent(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitAgentState(t, run.ID, agentstatus.Working)
	outcome, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "queued behind the turn", false)
	if err != nil || outcome != acphost.OutcomeQueued {
		t.Fatalf("Inject = %q, %v", outcome, err)
	}
	if _, err := rt.all()[0].Stop(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "undelivered notice", func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == "Message not delivered: agent connection closed" &&
				it.Notice.Description == "queued behind the turn"
		})
	})
	_, err = e.sched.Inject(t.Context(), run.ID, e.member.ID, "after exit", false)
	if got := collab.ClassifyReceipt(err); got != collab.ReceiptNotSent {
		t.Fatalf("receipt %q for %v", got, err)
	}
}

func TestEnhancedRunResumesAfterRestart(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptAskPermission)
	waitFor(t, "permission request", func() bool { return len(e.sched.PendingInputs(run.ID)) == 1 })
	e.waitAgentState(t, run.ID, agentstatus.Working)
	before := len(rt.all())
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	startScheduler(t, s2)
	waitFor(t, "resumed session", func() bool { return s2.acp.session(run.ID) != nil })

	execs := rt.all()
	if len(execs) != before+1 || !execs[before-1].exited() {
		t.Fatalf("the restart must stop the earlier ACP server and start one more: %d execs", len(execs))
	}
	if methods := execs[before].agent.Methods(); !slices.Contains(methods, acp.AgentMethodSessionResume) || slices.Contains(methods, acp.AgentMethodSessionNew) {
		t.Fatalf("restored with %v, want session/resume", methods)
	}
	waitFor(t, "the restored session's idle state", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		entry := s2.runs[run.ID]
		return entry != nil && entry.agentReport.State == agentstatus.Idle && len(entry.pendingInputs) == 0
	})
	if _, err := s2.Inject(t.Context(), run.ID, e.member.ID, "after restart", false); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s2, run.ID, "a turn after the restart", turnEnded("end_turn", 1))
}

func TestEnhancedRunCloseStopsAdapterAndReopenResumes(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	if err := e.sched.acp.session(run.ID).SetMode(t.Context(), "plan"); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
		t.Fatal(err)
	}
	first := rt.all()[0]
	waitFor(t, "adapter stopped", first.exited)
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "closed", false); err == nil {
		t.Fatal("a closed run took input")
	}
	if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	execs := rt.all()
	if len(execs) != 2 || !slices.Contains(execs[1].agent.Methods(), acp.AgentMethodSessionResume) {
		t.Fatalf("reopen must resume the session in a fresh ACP server: %d execs", len(execs))
	}
	waitFor(t, "resumed session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if mode := e.sched.acp.session(run.ID).State().Mode; mode != "plan" || !slices.Contains(execs[1].agent.Methods(), acp.AgentMethodSessionSetMode) {
		t.Fatalf("reopened in mode %q, want the recorded plan mode re-applied", mode)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "after reopen", false); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "a turn after reopen", turnEnded("end_turn", 2))
}

func TestEnhancedRunDeleteRemovesItemLog(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", assistantSaid("pong"))
	path := e.pty.ItemLogPath(run.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("item log after delete: %v", err)
	}
}

func TestEnhancedRunAdapterFailureParksRun(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Harnesses["fake"] = HarnessSpec{ACPArgs: []string{"acp-mock"}}
	})
	run := e.launchACP(t, "task")
	got := e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if !strings.Contains(got.Reason, "enhanced session failed") || !strings.Contains(got.Reason, "managed exec") {
		t.Fatalf("reason %q", got.Reason)
	}
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, "hi", false); !errors.Is(err, ErrACPNotRunning) {
		t.Fatalf("Inject without a session: %v", err)
	}
	items := waitItems(t, e.sched, run.ID, "failure notice", func(items []acphost.Item) bool { return len(items) > 0 })
	if items[0].Kind != acphost.KindNotice || !strings.Contains(items[0].Notice.Description, "managed exec") {
		t.Fatalf("items %+v", items)
	}
}

// With no session live, a fresh viewer gets the newest ReplayWindow items
// and where they start; a viewer inside the window gets exactly its gap.
func TestACPSubscribeReplaysAtMostTheWindow(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	e.sched.acp.stopAdapter(t.Context(), run.ID)

	log, err := acphost.OpenLog(e.pty.ItemLogPath(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	for range acphost.ReplayWindow + 50 {
		if err := log.Append(&acphost.Item{Kind: acphost.KindNotice, Notice: &acphost.Notice{Title: "n"}}); err != nil {
			t.Fatal(err)
		}
	}
	last := log.LastSeq()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	fresh, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Replay) != acphost.ReplayWindow || fresh.OldestSeq != last-acphost.ReplayWindow+1 || fresh.Seq != last {
		t.Fatalf("fresh viewer: %d items, oldest %d, seq %d; want %d up to %d", len(fresh.Replay), fresh.OldestSeq, fresh.Seq, acphost.ReplayWindow, last)
	}
	gap, err := e.sched.ACPSubscribe(run.ID, last-10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gap.Replay) != 10 || gap.Replay[0].Seq != last-9 || gap.OldestSeq != 0 {
		t.Fatalf("viewer inside the window: %d items from %d, oldest %d", len(gap.Replay), gap.Replay[0].Seq, gap.OldestSeq)
	}
}

// A stream opened while no session is live is released by the next session
// start, or by the end of a closing session it found; the driver keeps no
// per-run entries once nobody holds or waits on them.
func TestACPSubscribeWithoutSessionWaitsForTheNext(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	sess := e.sched.acp.session(run.ID)
	e.sched.acp.stopAdapter(t.Context(), run.ID)

	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	driverEntries := func() (int, int) {
		e.sched.acp.mu.Lock()
		defer e.sched.acp.mu.Unlock()
		return len(e.sched.acp.ops), len(e.sched.acp.waiters)
	}

	waiting, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Items != nil || waiting.Started == nil || closed(waiting.Started) {
		t.Fatalf("stream without a session: items %v started %v", waiting.Items, waiting.Started)
	}
	// The stopped session stands in for a newly started one: setRun is where
	// a start publishes it.
	e.sched.acp.setRun(run.ID, &acpRun{session: sess, stopping: true})
	if !closed(waiting.Started) {
		t.Fatal("the session start did not release the waiting stream")
	}
	waiting.Cancel()

	closing, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if closing.Started == nil || !closed(closing.Started) {
		t.Fatal("a stream that found a closed session was not released")
	}
	closing.Cancel()

	e.sched.acp.stopAdapter(t.Context(), run.ID)
	idle, err := e.sched.ACPSubscribe(run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	idle.Cancel()
	if ops, waiters := driverEntries(); ops != 0 || waiters != 0 {
		t.Fatalf("driver kept %d op locks and %d waiters", ops, waiters)
	}
}
