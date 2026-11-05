package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/assistant/tools"
	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// countingCaller is the member's caller: it counts each call by method and
// params, and names run-1's owner as owner says.
type countingCaller struct {
	actionServer
	mu    sync.Mutex
	runs  map[string]int
	owner string
}

func (c *countingCaller) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.Error) {
	c.mu.Lock()
	if c.runs == nil {
		c.runs = map[string]int{}
	}
	c.runs[method+" "+string(params)]++
	owner := c.owner
	c.mu.Unlock()
	if method == protocol.MethodRunGet && owner != "" {
		return json.RawMessage(`{"run":{"id":"run-1","member_id":"` + owner + `","workspace_id":"ws-1","title":"Fix login"}}`), nil
	}
	return c.actionServer.Call(ctx, method, params)
}

// kills counts the stops of run-1.
func (c *countingCaller) kills() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[protocol.MethodRunKill+` {"run_id":"run-1"}`]
}

// cardWorld is a member's assistant over a real store that survives the
// service's restarts.
type cardWorld struct {
	t      *testing.T
	dir    string
	db     *store.DB
	member domain.MemberID
	caller *countingCaller
	svc    *Service
	sock   string
	stop   func()
}

func newCardWorld(t *testing.T) *cardWorld {
	t.Helper()
	w := &cardWorld{t: t, dir: t.TempDir(), caller: &countingCaller{}}
	db, err := storetest.Open(filepath.Join(w.dir, "aether.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ada := &domain.Member{DisplayName: "Ada", TailnetLogin: "ada@example.com", Color: "#3cb44b", Role: domain.RoleCollaborator}
	if err = db.CreateMember(t.Context(), ada); err != nil {
		t.Fatal(err)
	}
	w.db, w.member = db, ada.ID
	w.caller.owner = string(ada.ID)
	w.start()
	t.Cleanup(func() { w.stop() })
	return w
}

// start starts the service as a server start does, and serves the member's
// tool socket.
func (w *cardWorld) start() {
	w.t.Helper()
	w.svc = New(Config{Dir: filepath.Join(w.dir, "assistant"), Store: w.db, Host: &fakeHost{},
		Caller: func(domain.MemberID) tools.Caller { return w.caller }})
	if err := w.svc.Resume(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "card-")
	if err != nil {
		w.t.Fatal(err)
	}
	w.sock = filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", w.sock)
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	srv := w.svc.toolServer(w.svc.member(w.member))
	go func() { done <- srv.Serve(ctx, ln) }()
	w.stop = func() {
		cancel()
		<-done
		_ = os.RemoveAll(dir)
		_ = w.svc.Close()
	}
}

func (w *cardWorld) restart() {
	w.t.Helper()
	w.stop()
	w.start()
}

func (w *cardWorld) m() *member { return w.svc.member(w.member) }

// leaveCard makes a stop of run-1 through the tool socket and hangs up once
// its card shows, as a turn that ends does. It returns the card.
func (w *cardWorld) leaveCard() protocol.AssistantConfirmation {
	w.t.Helper()
	before := len(pendingIDs(w))
	ctx, hangUp := context.WithCancel(w.t.Context())
	done := make(chan error, 1)
	go func() {
		done <- coordtransport.Call(ctx, w.sock, protocol.MethodRunKill, protocol.RunIDParams{RunID: "run-1"}, nil)
	}()
	var c protocol.AssistantConfirmation
	deadline := time.Now().Add(5 * time.Second)
	for {
		cards := pendingCards(w.t, w.svc, w.m())
		if len(cards) > before {
			c = cards[len(cards)-1]
			break
		}
		if time.Now().After(deadline) {
			w.t.Fatal("no new card")
		}
		time.Sleep(time.Millisecond)
	}
	hangUp()
	if err := wait(w.t, done); err == nil {
		w.t.Fatal("the call returned a result after its caller hung up")
	}
	// The socket sees the hang-up after the caller returns.
	r := w.m().confirms
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		r.mu.Lock()
		_, waiting := r.waiting[c.ID]
		r.mu.Unlock()
		if !waiting {
			return c
		}
		if time.Now().After(deadline) {
			w.t.Fatal("the call kept waiting after its caller hung up")
		}
	}
}

func pendingIDs(w *cardWorld) []string {
	rows, err := w.db.AssistantCards(w.t.Context(), w.member, protocol.AssistantCardPending)
	if err != nil {
		w.t.Fatal(err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// settled waits until card id leaves pending, approved and running.
func (w *cardWorld) settled(id string) store.AssistantCard {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, err := w.db.AssistantCard(w.t.Context(), w.member, id)
		if err != nil {
			w.t.Fatal(err)
		}
		switch row.State {
		case protocol.AssistantCardPending, protocol.AssistantCardApproved, protocol.AssistantCardRunning:
		default:
			return *row
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("card %s is still %s", id, row.State)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestCardIsApprovedAfterItsTurnAndAfterARestartAndRunsOnce is the check
// of docs/plans/2026-11-03-assistant-v2.md phase 2.
func TestCardIsApprovedAfterItsTurnAndAfterARestartAndRunsOnce(t *testing.T) {
	w := newCardWorld(t)

	c := w.leaveCard()
	if c.Origin.Door != protocol.AssistantDoorDashboard || c.ExpiresAt.Sub(c.CreatedAt) != confirmTTL {
		t.Fatalf("card = %+v, want a dashboard card open %s", c, confirmTTL)
	}
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); err != nil {
		t.Fatalf("approving a card whose turn ended = %v", err)
	}
	if row := w.settled(c.ID); row.State != protocol.AssistantCardFailed || !strings.Contains(row.Error, "finished at 12:04") {
		t.Fatalf("card = %+v, want the server's real error recorded as failed", row)
	}
	if n := w.caller.kills(); n != 1 {
		t.Fatalf("run.kill ran %d times, want once", n)
	}
	got := actions(t, w.svc, w.m())
	if len(got) != 1 || len(got[0]) != 2 || got[0][0].Outcome != protocol.AssistantActionApproved || got[0][1].Outcome != protocol.AssistantActionFailed ||
		got[0][1].ConfirmationID != c.ID || !strings.Contains(got[0][1].Error, "finished at 12:04") {
		t.Fatalf("records = %+v, want the approval, then the real error", got)
	}
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("second approval = %v, want ErrNoConfirmation", err)
	}

	c = w.leaveCard()
	w.restart()
	st, err := w.svc.openCards(t.Context(), w.member)
	if err != nil || len(st) != 1 || st[0].ID != c.ID || st[0].State != protocol.AssistantCardPending || st[0].Summary != c.Summary {
		t.Fatalf("cards after a restart = %+v, %v, want %s still pending", st, err, c.ID)
	}
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); err != nil {
		t.Fatalf("approving after a restart = %v", err)
	}
	w.settled(c.ID)
	w.restart()
	w.settled(c.ID)
	if n := w.caller.kills(); n != 2 {
		t.Fatalf("run.kill ran %d times over both cards and restarts, want twice", n)
	}
}

func TestCardClaimedWhenTheServerStoppedFailsAndNeverRuns(t *testing.T) {
	w := newCardWorld(t)
	c := w.leaveCard()
	// What a server that stopped while the call ran leaves behind.
	for _, state := range []string{protocol.AssistantCardApproved, protocol.AssistantCardRunning} {
		from := map[string]string{protocol.AssistantCardApproved: protocol.AssistantCardPending, protocol.AssistantCardRunning: protocol.AssistantCardApproved}[state]
		if ok, err := w.db.MoveAssistantCard(t.Context(), w.member, c.ID, []string{from}, store.AssistantCardMove{State: state, ActionID: "act-1"}); err != nil || !ok {
			t.Fatalf("move to %s = %v, %v", state, ok, err)
		}
	}
	w.restart()
	row := w.settled(c.ID)
	if row.State != protocol.AssistantCardFailed || row.Error != restartedWhileRunning {
		t.Fatalf("card after a restart = %+v, want failed: %s", row, restartedWhileRunning)
	}
	if n := w.caller.kills(); n != 0 {
		t.Fatalf("run.kill ran %d times, want never", n)
	}
	got := actions(t, w.svc, w.m())
	if len(got) != 1 || got[0][0].ID != "act-1" || got[0][0].Outcome != protocol.AssistantActionFailed || got[0][0].Error != restartedWhileRunning {
		t.Fatalf("records = %+v, want the claim's action failed", got)
	}
}

func TestCardWhoseTargetChangedFailsWithItsNewState(t *testing.T) {
	w := newCardWorld(t)
	c := w.leaveCard()
	w.caller.mu.Lock()
	w.caller.owner = "m2"
	w.caller.mu.Unlock()
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); err != nil {
		t.Fatal(err)
	}
	row := w.settled(c.ID)
	if row.State != protocol.AssistantCardFailed || !strings.Contains(row.Error, "\nIt now reads: Stop session run-1 (\"Fix login\"), owned by Dana") ||
		!strings.Contains(row.Error, "You approved: Stop session run-1 (\"Fix login\")\n") || !strings.Contains(row.Error, "nothing was run") {
		t.Fatalf("card = %+v, want failed with what it reads now", row)
	}
	if n := w.caller.kills(); n != 0 {
		t.Fatalf("run.kill ran %d times, want never", n)
	}
}

func TestTwoDoorsDecidingOneCardOneWins(t *testing.T) {
	w := newCardWorld(t)
	for range 5 {
		c := w.leaveCard()
		errs := make(chan error, 2)
		for _, approve := range []bool{true, false} {
			go func() { errs <- w.svc.Confirm(t.Context(), w.member, c.ID, approve) }()
		}
		won := 0
		for range 2 {
			switch err := <-errs; {
			case err == nil:
				won++
			case !errors.Is(err, ErrNoConfirmation):
				t.Fatalf("losing decision = %v, want ErrNoConfirmation", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d decisions won, want 1", won)
		}
		w.settled(c.ID)
	}
	if n := w.caller.kills(); n > 5 {
		t.Fatalf("run.kill ran %d times for 5 cards", n)
	}
}

func TestResetWithdrawsOpenCards(t *testing.T) {
	w := newCardWorld(t)
	c := w.leaveCard()
	m := w.m()
	m.op.Lock()
	err := w.svc.resetLocked(t.Context(), m)
	m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if row := w.settled(c.ID); row.State != protocol.AssistantCardWithdrawn {
		t.Fatalf("card after a reset = %+v, want withdrawn", row)
	}
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("approving a withdrawn card = %v, want ErrNoConfirmation", err)
	}
}

func TestCardOfAnEarlierConversationNeverRuns(t *testing.T) {
	w := newCardWorld(t)
	approved := w.leaveCard()
	// What an approval that lands while New conversation runs leaves: the
	// card approved, and the conversation reset after withdrawAll read it.
	if ok, err := w.db.MoveAssistantCard(t.Context(), w.member, approved.ID, []string{protocol.AssistantCardPending},
		store.AssistantCardMove{State: protocol.AssistantCardApproved}); err != nil || !ok {
		t.Fatalf("approve = %v, %v", ok, err)
	}
	pending := w.leaveCard()
	if _, err := w.db.ResetAssistantSession(t.Context(), w.member); err != nil {
		t.Fatal(err)
	}
	if cards, err := w.svc.openCards(t.Context(), w.member); err != nil || len(cards) != 0 {
		t.Fatalf("cards listed in the new conversation = %+v, %v, want none", cards, err)
	}
	if err := w.svc.Confirm(t.Context(), w.member, pending.ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("approving a card of the earlier conversation = %v, want ErrNoConfirmation", err)
	}
	w.svc.runParked(w.m(), approved.ID, nil)
	for _, id := range []string{pending.ID, approved.ID} {
		if row := w.settled(id); row.State != protocol.AssistantCardWithdrawn {
			t.Fatalf("card %s = %+v, want withdrawn", id, row)
		}
	}
	if n := w.caller.kills(); n != 0 {
		t.Fatalf("run.kill ran %d times, want never", n)
	}
}

func TestTheSameCallAskedAgainSharesItsCard(t *testing.T) {
	w := newCardWorld(t)
	first := w.leaveCard()
	done := make(chan error, 1)
	go func() {
		done <- coordtransport.Call(t.Context(), w.sock, protocol.MethodRunKill, protocol.RunIDParams{RunID: "run-1"}, nil)
	}()
	r := w.m().confirms
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		r.mu.Lock()
		_, waiting := r.waiting[first.ID]
		r.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second call did not wait for the first call's card")
		}
	}
	if ids := pendingIDs(w); len(ids) != 1 || ids[0] != first.ID {
		t.Fatalf("pending cards = %v, want only %s", ids, first.ID)
	}
	if err := w.svc.Confirm(t.Context(), w.member, first.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "finished at 12:04") {
		t.Fatalf("the waiting call = %v, want the server's real error", err)
	}
	if n := w.caller.kills(); n != 1 {
		t.Fatalf("run.kill ran %d times, want once", n)
	}
}

// TestResetAndSettingsDoNotWaitOnEachOther resets the conversation, which
// withdraws an open card, while auto-approval changes and notes it with no
// live session.
func TestResetAndSettingsDoNotWaitOnEachOther(t *testing.T) {
	w := newCardWorld(t)
	w.caller.owner = "m2" // another member's session asks even with auto-approval on
	m := w.m()
	for i := range 20 {
		w.leaveCard()
		on := i%2 == 0
		done := make(chan error, 2)
		go func() {
			m.op.Lock()
			defer m.op.Unlock()
			done <- w.svc.resetLocked(t.Context(), m)
		}()
		go func() {
			_, err := w.svc.SetSettings(t.Context(), w.member, protocol.AssistantSettingsParams{AutoApprove: &on})
			done <- err
		}()
		for range 2 {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("reset and a settings change wait on each other")
			}
		}
	}
}

func TestACardOfAnEarlierConversationCutOffByARestartLeavesNoRecord(t *testing.T) {
	w := newCardWorld(t)
	c := w.leaveCard()
	for _, state := range []string{protocol.AssistantCardApproved, protocol.AssistantCardRunning} {
		from := map[string]string{protocol.AssistantCardApproved: protocol.AssistantCardPending, protocol.AssistantCardRunning: protocol.AssistantCardApproved}[state]
		if ok, err := w.db.MoveAssistantCard(t.Context(), w.member, c.ID, []string{from}, store.AssistantCardMove{State: state}); err != nil || !ok {
			t.Fatalf("move to %s = %v, %v", state, ok, err)
		}
	}
	if _, err := w.db.ResetAssistantSession(t.Context(), w.member); err != nil {
		t.Fatal(err)
	}
	w.restart()
	if row := w.settled(c.ID); row.State != protocol.AssistantCardFailed || row.Error != restartedWhileRunning {
		t.Fatalf("card = %+v, want failed", row)
	}
	if got := actions(t, w.svc, w.m()); len(got) != 0 {
		t.Fatalf("records in the new conversation = %+v, want none", got)
	}
}

func TestAnApprovalKeepsItsTimeWhenItsCallStarts(t *testing.T) {
	w := newCardWorld(t)
	c := w.leaveCard()
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, true); err != nil {
		t.Fatal(err)
	}
	row := w.settled(c.ID)
	if row.DecidedAt.IsZero() || !row.DecidedAt.Before(row.UpdatedAt.Add(time.Millisecond)) {
		t.Fatalf("card = %+v, want the decision time kept", row)
	}
	card, err := w.svc.Card(t.Context(), w.member, c.ID)
	if err != nil || !card.DecidedAt.Equal(row.DecidedAt) {
		t.Fatalf("card on the wire decided at %v, %v, want %v", card.DecidedAt, err, row.DecidedAt)
	}
}

func TestARefusedPullRequestMergeIsRecordedFailed(t *testing.T) {
	if got := refusedIn(json.RawMessage(`{"merged":false,"error":"GitHub refused the merge: required checks failed"}`)); got != "GitHub refused the merge: required checks failed" {
		t.Fatalf("refusal = %q", got)
	}
	if got := refusedIn(json.RawMessage(`{"creation_uncertain":true}`)); got == "" {
		t.Fatal("an uncertain creation read as done")
	}
	for _, done := range []string{`{"merged":true,"error":"the first attempt timed out"}`, `{"reconciled":true,"error":"GitHub answered 502"}`, `{"created":true}`} {
		if got := refusedIn(json.RawMessage(done)); got != "" {
			t.Fatalf("%s read as refused: %q", done, got)
		}
	}
	if got := refusedIn(json.RawMessage(`{"run":{"id":"run-1"}}`)); got != "" {
		t.Fatalf("a launch's result read as refused: %q", got)
	}
}

func TestCardFullTextShowsEveryFieldWhole(t *testing.T) {
	task := "Fix the flaky login test\n" + strings.Repeat("and then some ", 20)
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: "ws-1", Harness: "claude", Task: task})
	c := Card{AssistantConfirmation: protocol.AssistantConfirmation{Summary: `Launch claude (default mode) in api: "Fix the flaky login test…"`, Params: params}}
	want := c.Summary + "\nharness: claude\ntask: " + strconv.Quote(task) + "\nworkspace_id: ws-1"
	if got := c.FullText(); got != want {
		t.Fatalf("full text = %q, want %q", got, want)
	}
}
