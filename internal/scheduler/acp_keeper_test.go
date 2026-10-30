package scheduler

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/acpkeeper"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// restart closes s, as a server update stops the process, and starts the
// next server process on the same state and runtime.
func (e *testEnv) restart(t *testing.T, s *Scheduler) *Scheduler {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	startScheduler(t, next)
	// Closing a scheduler that is still recovering races its recovery.
	select {
	case <-next.RecoveryReady():
	case <-time.After(30 * time.Second):
		t.Fatal("recovery did not finish")
	}
	waitFor(t, "the adopted session", func() bool { return next.acp.session(e.onlyRun(t, next)) != nil })
	return next
}

func (e *testEnv) onlyRun(t *testing.T, s *Scheduler) domain.RunID {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.runs {
		return id
	}
	return ""
}

func thoughts(items []acphost.Item) int {
	n := 0
	for _, it := range items {
		if it.Kind == acphost.KindThought {
			n++
		}
	}
	return n
}

func interrupted(items []acphost.Item) bool {
	return slices.ContainsFunc(items, func(it acphost.Item) bool {
		return it.Kind == acphost.KindTurnEnd && it.StopReason == "interrupted" ||
			it.Kind == acphost.KindNotice && it.Notice.Title == "Turn interrupted"
	})
}

func TestEnhancedRunKeepsItsTurnThroughRestarts(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptThink}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.waitAgentWorking(t, run.ID)

	s := e.sched
	for range 2 {
		before := thoughts(waitItems(t, s, run.ID, "streaming", func(items []acphost.Item) bool { return thoughts(items) > 0 }))
		s = e.restart(t, s)
		items := waitItems(t, s, run.ID, "streaming after the restart", func(items []acphost.Item) bool { return thoughts(items) > before+3 })
		if interrupted(items) {
			t.Fatalf("the restart interrupted the turn: %+v", items)
		}
		if st := s.acp.session(run.ID).State(); !st.TurnInFlight {
			t.Fatalf("the adopted session has no turn in flight: %+v", st)
		}
	}
	if execs := rt.all(); len(execs) != 1 || execs[0].exited() {
		t.Fatalf("the restarts replaced the adapter: %d execs", len(execs))
	}
	if prompts := rt.all()[0].wire.prompts(t); len(prompts) != 1 {
		t.Fatalf("the agent got %d prompts, want the one the member sent", len(prompts))
	}
	if err := s.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s, run.ID, "the cancelled turn", turnEnded("cancelled", 1))
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
}

func TestEnhancedRunKeepsAPendingPermissionThroughARestart(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptAskPermission}, false, nil); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})

	s := e.restart(t, e.sched)
	var adopted []domain.RunInputRequest
	waitFor(t, "the permission request after the restart", func() bool {
		adopted = s.PendingInputs(run.ID)
		return len(adopted) == 1
	})
	if adopted[0].ID != pending[0].ID {
		t.Fatalf("the request is %q after the restart, want %q", adopted[0].ID, pending[0].ID)
	}
	if err := s.ACPAnswer(run.ID, adopted[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	items := waitItems(t, s, run.ID, "the answered turn", assistantSaid("permission: allow"))
	if interrupted(items) {
		t.Fatalf("the restart interrupted the turn: %+v", items)
	}
	waitItems(t, s, run.ID, "the turn's end", turnEnded("end_turn", 1))
	if prompts := rt.all()[0].wire.prompts(t); len(rt.all()) != 1 || len(prompts) != 1 {
		t.Fatalf("the agent got %d prompts in %d execs, want one in one", len(prompts), len(rt.all()))
	}
}

func TestEnhancedRunRestoresWhenItsKeeperIsGone(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	rt.dropAdapters(t)
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	startScheduler(t, s)
	items := waitItems(t, s, run.ID, "the restart notice", func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == "Agent restarted"
		})
	})
	waitFor(t, "the restored session", func() bool { return s.acp.session(run.ID) != nil })
	if execs := rt.all(); len(execs) != 2 || !slices.ContainsFunc(execs[1].agent.Methods(), func(m string) bool {
		return m == acp.AgentMethodSessionLoad || m == acp.AgentMethodSessionResume
	}) {
		t.Fatalf("the restart did not restore the session in a new adapter: %d execs, %+v", len(execs), items)
	}
}

func TestEnhancedRunWithoutAKeeperRunsItsAdapterDirectly(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	rt.noKeeper = true
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if execs := rt.all(); len(execs) != 1 || execs[0].argv[0] != "acp-mock" {
		t.Fatalf("the adapter did not run directly: %d execs", len(execs))
	}
	if sc, err := e.sched.readSidecar(run.ID); err != nil || sc.AgentExec == nil || sc.AgentKeeper != nil {
		t.Fatalf("sidecar %+v, %v; want the adapter's exec and no keeper", sc.AgentExec, err)
	}

	s := e.restart(t, e.sched)
	if execs := rt.all(); len(execs) != 2 || !execs[0].exited() {
		t.Fatalf("the restart did not replace the adapter: %d execs", len(execs))
	}
	if _, err := s.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "again"}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, s, run.ID, "a turn in the restored session", turnEnded("end_turn", 2))
}

func hasNotice(title string) func([]acphost.Item) bool {
	return func(items []acphost.Item) bool {
		return slices.ContainsFunc(items, func(it acphost.Item) bool {
			return it.Kind == acphost.KindNotice && it.Notice.Title == title
		})
	}
}

func TestEnhancedRunRunsItsKeeperAsRoot(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	rt.mu.Lock()
	specs := append(slices.Clone(rt.specs), rt.bridges...)
	rt.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("%d execs, want the keeper and its bridge", len(specs))
	}
	for _, spec := range specs {
		if spec.User != "0" {
			t.Errorf("%v runs as %q, want root", spec.Argv, spec.User)
		}
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.AgentKeeper == nil || path.Dir(path.Dir(sc.AgentKeeper.Socket)) != acpkeeper.SocketRoot {
		t.Fatalf("keeper %+v, %v; want a socket in its own directory under %s", sc.AgentKeeper, err, acpkeeper.SocketRoot)
	}
}

func TestEnhancedRunRestartsItsAgentWhenItsKeeperIsTakenOver(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || sc.AgentKeeper == nil {
		t.Fatalf("sidecar keeper %+v, %v", sc.AgentKeeper, err)
	}
	rt.mu.Lock()
	k := rt.keepers[sc.AgentKeeper.Socket]
	rt.mu.Unlock()
	host, conn := net.Pipe()
	t.Cleanup(func() { _ = host.Close() })
	go k.ServeConn(conn)
	if _, err := acpkeeper.Attach(t.Context(), host, acpkeeper.Hello{Gen: sc.AgentKeeper.Gen + 1, Token: sc.AgentKeeper.Token}); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the takeover notice", hasNotice("Agent connection taken over"))
	waitFor(t, "a fresh agent with the restored session", func() bool {
		execs := rt.all()
		return len(execs) == 2 && execs[0].exited() && e.sched.acp.session(run.ID) != nil
	})
}

func TestEnhancedRunRestoresWhenTheServerStoppedBeforeItsSessionOpened(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A keeper whose adapter never answered initialize, as when the server
	// stopped right after it started the keeper.
	socket := path.Join(acpkeeper.SocketRoot, "opening", "keeper.sock")
	exec, err := rt.StartExecPipe(t.Context(), runtime.ID(sc.ContainerID), runtime.ExecSpec{
		Argv: acpkeeper.ServeArgv(coordtransport.CLIPath, socket, "", []string{"acp-mock"}), CreationKey: "opening",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(exec.Attachment().Stdin(), "opening-token\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(exec.Attachment().Stdout()).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("keeper said %q, %v", line, err)
	}
	identity := exec.Identity()
	sc.AgentExec, sc.AgentKeeper = &identity, &keeperRecord{Socket: socket, Token: "opening-token"}
	if err = e.sched.writeSidecar(sc); err != nil {
		t.Fatal(err)
	}

	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	startScheduler(t, s)
	items := waitItems(t, s, run.ID, "the restart notice", hasNotice("Agent restarted"))
	if hasNotice("Enhanced session failed")(items) {
		t.Fatalf("the run failed: %+v", items)
	}
	waitFor(t, "the restored session", func() bool { return s.acp.session(run.ID) != nil })
	if !exec.(*acpExec).exited() {
		t.Fatal("the keeper of the unopened session still runs")
	}
}

func TestKeeperAttachFailsWhenItsGenerationIsNotRecorded(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	gone := &supervised{runID: run.ID, agentKeeper: sc.AgentKeeper}
	if _, _, err := e.sched.acp.attachKeeper(t.Context(), gone, rt, runtime.ID(sc.ContainerID)); !errors.Is(err, errUnsupervised) {
		t.Fatalf("attach with an unrecorded generation: %v, want errUnsupervised", err)
	}
}

func TestEnhancedRunNeverReattachesToTheAgentThatLostItsSession(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	lost := e.sched.acp.session(run.ID)
	rt.all()[0].stopErr = errors.New("acpRuntime: stop is stuck")
	rt.stuckStdin.Store(true)
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, acpmock.PromptLoseSession); !errors.Is(err, domain.ErrPromptRefused) {
		t.Fatalf("wake of a lost session: %v, want domain.ErrPromptRefused", err)
	}
	waitFor(t, "the session restored in a fresh agent", func() bool {
		sess := e.sched.acp.session(run.ID)
		return len(rt.all()) == 2 && sess != nil && sess != lost
	})
	if !slices.Contains(rt.all()[1].agent.Methods(), acp.AgentMethodSessionResume) {
		t.Fatal("the fresh agent did not resume the lost session")
	}
}

func TestReattachedWaitsForAPausedRunsKeeper(t *testing.T) {
	t.Parallel()
	s := &Scheduler{runs: map[domain.RunID]*supervised{
		"paused":  {paused: true, agentKeeper: &keeperRecord{}},
		"adopted": {keeperAdopted: true, keeperDelivered: []string{"m1"}},
		"fresh":   {paused: true},
	}}
	for run, want := range map[domain.RunID]collab.Reattach{"paused": collab.Undecided, "adopted": collab.Adopted, "fresh": collab.Restored, "gone": collab.Restored} {
		if _, got := s.Reattached(run); got != want {
			t.Errorf("Reattached(%s) = %v, want %v", run, got, want)
		}
	}
	if delivered, _ := s.Reattached("adopted"); !slices.Equal(delivered, []string{"m1"}) {
		t.Errorf("delivered = %v, want [m1]", delivered)
	}
}

func TestEnhancedRunStreamsEveryLineOnceThroughRestarts(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptGoQuiet}, false, nil); err != nil {
		t.Fatal(err)
	}
	waitItems(t, e.sched, run.ID, "the quiet turn", func(items []acphost.Item) bool { return thoughts(items) > 0 })
	session := e.sched.acp.session(run.ID).SessionID()
	const lines = 400
	var want strings.Builder
	streamed := make(chan struct{})
	go func() {
		defer close(streamed)
		for n := range lines {
			line := fmt.Sprintf("tick %d\n", n)
			want.WriteString(line)
			chunk, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": acp.ClientMethodSessionUpdate, "params": map[string]any{
				"sessionId": session, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": line}},
			}})
			if _, err := rt.all()[0].agentOut.Write(append(chunk, '\n')); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	s := e.sched
	for range 2 {
		time.Sleep(60 * time.Millisecond)
		s = e.restart(t, s)
	}
	<-streamed
	said := func(items []acphost.Item) string {
		var b strings.Builder
		for _, it := range items {
			if it.Kind == acphost.KindMessage && it.Message.Role == "assistant" {
				b.WriteString(it.Message.Text)
			}
		}
		return b.String()
	}
	items := waitItems(t, s, run.ID, "every line", func(items []acphost.Item) bool { return len(said(items)) >= want.Len() })
	if got := said(items); got != want.String() {
		t.Fatalf("the agent's lines through two restarts:\n%s\nwant 0 to %d once each", got, lines-1)
	}
}

func TestEnhancedRunKeepsAPendingPermissionOfALaterTurnThroughRestarts(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, "say pong")
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	if err := e.sched.WakeEnhanced(t.Context(), run.ID, acpmock.PromptAskPermission); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	s := e.sched
	for range 2 {
		s = e.restart(t, s)
		var adopted []domain.RunInputRequest
		waitFor(t, "the permission request after the restart", func() bool {
			adopted = s.PendingInputs(run.ID)
			return len(adopted) == 1
		})
		if adopted[0].ID != pending[0].ID {
			t.Fatalf("the request is %q after the restart, want %q", adopted[0].ID, pending[0].ID)
		}
	}
	if err := s.ACPAnswer(run.ID, pending[0].ID, "allow", nil); err != nil {
		t.Fatal(err)
	}
	items := waitItems(t, s, run.ID, "the answered turn", assistantSaid("permission: allow"))
	if interrupted(items) {
		t.Fatalf("a restart interrupted the turn: %+v", items)
	}
	waitItems(t, s, run.ID, "the turn's end", turnEnded("end_turn", 2))
	if len(rt.all()) != 1 {
		t.Fatalf("%d agents, want the first one adopted", len(rt.all()))
	}
}

func TestEnhancedAnswerRightAfterARestartWaitsForRecovery(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, "")
	waitFor(t, "session", func() bool { return e.sched.acp.session(run.ID) != nil })
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: acpmock.PromptAskPermission}, false, nil); err != nil {
		t.Fatal(err)
	}
	var pending []domain.RunInputRequest
	waitFor(t, "permission request", func() bool {
		pending = e.sched.PendingInputs(run.ID)
		return len(pending) == 1
	})
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.PTY = newFakePTY()
	cfg.PTY.(*fakePTY).logDir = e.pty.logDir
	// Recovery's exit probe holds it open long enough to answer during it.
	cfg.ExitProbeTimeout = 500 * time.Millisecond
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	answered := make(chan error, 1)
	go func() {
		for !s.recovering.Load() {
			time.Sleep(time.Millisecond)
		}
		answered <- s.ACPAnswer(run.ID, pending[0].ID, "allow", nil)
	}()
	startScheduler(t, s)
	if err := <-answered; err != nil {
		t.Fatalf("answer during recovery: %v", err)
	}
	waitItems(t, s, run.ID, "the answered turn", assistantSaid("permission: allow"))
}
