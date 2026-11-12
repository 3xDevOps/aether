package assistant

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/store/storetest"
)

// queueWorld is a member's assistant over a real store, its session a mock
// agent attached as a live session is.
type queueWorld struct {
	t      *testing.T
	dir    string
	db     *store.DB
	member domain.MemberID
	svc    *Service
	agent  *acpmock.Agent
	// crash ends the service as a server that stops at once does.
	crash func()
}

func newQueueWorld(t *testing.T) *queueWorld {
	t.Helper()
	w := &queueWorld{t: t, dir: t.TempDir()}
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
	w.start()
	return w
}

// start starts the service as a server start does, with a fresh agent.
func (w *queueWorld) start() {
	w.t.Helper()
	w.svc = New(Config{Dir: filepath.Join(w.dir, "assistant"), Store: w.db, Host: &fakeHost{}})
	fix, err := acpmock.Load("claude")
	if err != nil {
		w.t.Fatal(err)
	}
	w.agent = acpmock.New(fix)
	agentIn, hostOut := io.Pipe()
	hostIn, agentOut := io.Pipe()
	go w.agent.Serve(agentIn, agentOut)
	m := w.svc.member(w.member)
	var own atomic.Pointer[acphost.Session]
	sess, err := acphost.Start(w.t.Context(), hostIn, hostOut, acphost.Config{LogPath: w.svc.logPath(w.member), Cwd: w.t.TempDir(),
		Logger: slog.Default(), OnState: w.svc.onState(m, own.Load)})
	own.Store(sess)
	if err != nil {
		w.t.Fatal(err)
	}
	m.mu.Lock()
	m.sess = &session{acp: sess, ended: make(chan struct{})}
	m.mu.Unlock()
	svc := w.svc
	w.crash = func() { crash(svc, sess, agentIn, agentOut) }
	w.t.Cleanup(w.crash)
	if err = w.svc.Resume(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

// crash ends svc as a server that stops at once does: nothing it runs
// records an end.
func crash(svc *Service, sess *acphost.Session, pipes ...io.Closer) {
	svc.mu.Lock()
	svc.closing = true
	svc.mu.Unlock()
	svc.cancel()
	_ = sess.Close()
	for _, p := range pipes {
		_ = p.Close()
	}
	<-sess.Done()
	svc.wg.Wait()
}

func (w *queueWorld) prompt(text, key string) Entry {
	w.t.Helper()
	e, err := w.svc.Prompt(w.t.Context(), w.member, text, key, nil, Door(protocol.AssistantDoorPhone))
	if err != nil {
		w.t.Fatalf("prompt %q: %v", text, err)
	}
	return e
}

// until waits for entry id to reach state.
func (w *queueWorld) until(id, state string) Entry {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e, err := w.svc.Entry(w.t.Context(), w.member, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if e.State == state {
			return e
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("entry %s is %s (%s), want %s", id, e.State, e.Error, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (w *queueWorld) sent() []string {
	var out []string
	for _, raw := range w.agent.Prompts() {
		var blocks []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil && len(blocks) > 0 {
			out = append(out, blocks[0].Text)
		}
	}
	return out
}

func TestQueueRunsPromptsInOrderWithinItsCap(t *testing.T) {
	w := newQueueWorld(t)
	running := w.prompt(acpmock.PromptWait, "k0")
	if running.State != protocol.AssistantQueueRunning || running.Origin.Door != protocol.AssistantDoorPhone || !strings.HasPrefix(running.ID, "turn-") {
		t.Fatalf("first prompt = %+v, want it running from the phone", running)
	}
	one := w.prompt("run echo one", "k1")
	if one.State != protocol.AssistantQueueQueued {
		t.Fatalf("prompt during a turn = %+v, want it queued", one)
	}
	if again := w.prompt("run echo one", "k1"); again.ID != one.ID {
		t.Fatalf("re-sent key = %s, want the entry it made, %s", again.ID, one.ID)
	}
	gone := w.prompt("run echo gone", "k2")
	var last Entry
	for _, key := range []string{"k3", "k4", "k5"} {
		last = w.prompt("run echo "+key, key)
	}
	if _, err := w.svc.Prompt(t.Context(), w.member, "run echo six", "k6", nil, Door("")); !errors.Is(err, ErrQueueFull) ||
		err.Error() != "assistant: 5 prompts are already queued; wait or remove one" {
		t.Fatalf("sixth queued prompt = %v, want the queue full", err)
	}
	if err := w.svc.RemoveQueued(t.Context(), w.member, gone.ID); err != nil {
		t.Fatal(err)
	}
	if again := w.prompt("run echo gone", "k2"); again.ID != gone.ID || again.State != protocol.AssistantQueueRemoved {
		t.Fatalf("re-sent key of a removed prompt = %+v, want its removed entry", again)
	}
	if err := w.svc.RemoveQueued(t.Context(), w.member, gone.ID); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("second removal = %v, want ErrNoEntry", err)
	}
	if err := w.svc.RemoveQueued(t.Context(), w.member, running.ID); !errors.Is(err, ErrNoEntry) {
		t.Fatalf("removing the running prompt = %v, want ErrNoEntry", err)
	}
	st, err := w.svc.queueOf(t.Context(), w.member, 0)
	if err != nil || len(st) != 5 || st[0].ID != running.ID || st[1].ID != one.ID {
		t.Fatalf("queue = %+v, %v, want the running prompt, then four waiting", st, err)
	}

	if err := w.svc.Cancel(t.Context(), w.member); err != nil {
		t.Fatal(err)
	}
	if e := w.until(running.ID, protocol.AssistantQueueFailed); e.Error != "you stopped this turn" {
		t.Fatalf("stopped turn = %+v", e)
	}
	if again := w.prompt(acpmock.PromptWait, "k0"); again.ID != running.ID {
		t.Fatalf("re-sent key of the stopped turn = %+v, want its entry, not a new turn", again)
	}
	done := w.until(one.ID, protocol.AssistantQueueDone)
	if done.Reply != "one\n" || done.MessageID == "" {
		t.Fatalf("queued turn = %+v, want its reply and prompt message", done)
	}
	w.until(last.ID, protocol.AssistantQueueDone)
	if got := w.sent(); !slices.Equal(got, []string{acpmock.PromptWait, "run echo one", "run echo k3", "run echo k4", "run echo k5"}) {
		t.Fatalf("prompts the agent got = %q, want them in order without the removed one", got)
	}
}

func TestQueueMarksATurnARestartLostInterruptedAndRunsTheRest(t *testing.T) {
	w := newQueueWorld(t)
	lost := w.prompt(acpmock.PromptWait, "k0")
	after := w.prompt("run echo after", "k1")
	w.crash()
	w.start()
	if e := w.until(lost.ID, protocol.AssistantQueueInterrupted); e.Error != restartedWhileTurnRan {
		t.Fatalf("turn a restart lost = %+v", e)
	}
	if e := w.until(after.ID, protocol.AssistantQueueDone); e.Reply != "after\n" {
		t.Fatalf("waiting prompt after a restart = %+v, want it run", e)
	}
	if got := w.sent(); !slices.Equal(got, []string{"run echo after"}) {
		t.Fatalf("prompts the new agent got = %q, want only the waiting one", got)
	}
	if again := w.prompt(acpmock.PromptWait, "k0"); again.ID != lost.ID || again.State != protocol.AssistantQueueInterrupted {
		t.Fatalf("re-sent key of the lost turn = %+v, want its interrupted entry", again)
	}
}

func TestQueueRecordsTheCardsATurnRaised(t *testing.T) {
	w := newQueueWorld(t)
	running := w.prompt(acpmock.PromptWait, "k0")
	m := w.svc.member(w.member)
	done := make(chan error, 1)
	go func() {
		done <- w.svc.approver(m)(t.Context(), protocol.AssistantConfirmation{Method: protocol.MethodRunKill, Summary: "Stop session r1"})
	}()
	c := pendingCards(t, w.svc, m)[0]
	if c.Origin.Door != protocol.AssistantDoorPhone {
		t.Fatalf("card = %+v, want the phone turn's origin", c)
	}
	if err := w.svc.Confirm(t.Context(), w.member, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); !errors.Is(err, errDeclined) {
		t.Fatalf("declined card = %v", err)
	}
	if e, err := w.svc.Entry(t.Context(), w.member, running.ID); err != nil || !slices.Equal(e.Cards, []string{c.ID}) {
		t.Fatalf("entry = %+v, %v, want its card", e, err)
	}
}

func TestDoorNamesOnlyAetherClients(t *testing.T) {
	for door, want := range map[string]string{"phone": "phone", "desktop": "desktop", "": "dashboard", "connector": "dashboard", "connector:ChatGPT con-1": "dashboard"} {
		if got := Door(door); got.Door != want || got.parks() || got.Grant != "" {
			t.Errorf("Door(%q) = %+v, want %s", door, got, want)
		}
	}
}

func TestQueueEndsWithItsConversation(t *testing.T) {
	w := newQueueWorld(t)
	m := w.svc.member(w.member)
	if _, err := w.db.ResetAssistantSession(t.Context(), w.member); err != nil {
		t.Fatal(err)
	}
	// What a prompt that read the conversation before a reset leaves.
	stale, _, err := w.db.EnqueueAssistantPrompt(t.Context(), &store.AssistantQueueEntry{ID: "turn-stale", MemberID: w.member, Generation: 0,
		Key: "k0", Text: "run echo stale", Origin: `{"door":"dashboard"}`}, maxQueued)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.svc.advance(t.Context(), m, ""); err != nil {
		t.Fatal(err)
	}
	if e := w.until(stale.ID, protocol.AssistantQueueRemoved); e.Error != errQueueReset.Error() {
		t.Fatalf("prompt of an earlier conversation = %+v", e)
	}
	if got := w.sent(); len(got) != 0 {
		t.Fatalf("prompts the agent got = %q, want none", got)
	}
}

func TestQueueLeavesAPromptQueuedWhenTheServiceCloses(t *testing.T) {
	w := newQueueWorld(t)
	m := w.svc.member(w.member)
	held, _, err := w.db.EnqueueAssistantPrompt(t.Context(), &store.AssistantQueueEntry{ID: "turn-held", MemberID: w.member,
		Key: "k0", Text: "run echo later", Origin: `{"door":"dashboard"}`}, maxQueued)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.cancel()
	_ = w.svc.advance(t.Context(), m, "")
	if e := w.until(held.ID, protocol.AssistantQueueQueued); e.Error != "" {
		t.Fatalf("prompt the closing service did not send = %+v, want it still queued", e)
	}
}

func TestATurnAfterAConnectorTurnIsTheDashboards(t *testing.T) {
	w := newQueueWorld(t)
	m := w.svc.member(w.member)
	e, err := w.svc.Prompt(t.Context(), w.member, "run echo hi", "k0", nil, ConnectorOrigin("ChatGPT", "con-1", "grant-1", []string{"write"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := w.until(e.ID, protocol.AssistantQueueDone); got.Origin.Grant != "grant-1" || got.Reply != "hi\n" {
		t.Fatalf("connector turn = %+v", got)
	}
	// The entry is stored as done before endEntry clears the member's origin.
	deadline := time.Now().Add(10 * time.Second)
	for {
		origin, entry := m.turn()
		if origin.Door == protocol.AssistantDoorDashboard && origin.Grant == "" && entry == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("origin after the connector turn ended = %+v, %q, want the dashboard's", origin, entry)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewConversationDeletesTheQueue(t *testing.T) {
	w := newQueueWorld(t)
	running := w.prompt(acpmock.PromptWait, "k0")
	queued := w.prompt("run echo later", "k1")
	m := w.svc.member(w.member)
	m.op.Lock()
	err := w.svc.dropQueue(t.Context(), m)
	m.op.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{running.ID, queued.ID} {
		if _, err := w.svc.Entry(t.Context(), w.member, id); !errors.Is(err, ErrNoEntry) {
			t.Fatalf("entry %s after New conversation = %v, want it deleted with its text", id, err)
		}
	}
}

func TestARemovedPromptKeepsItsKey(t *testing.T) {
	w := newQueueWorld(t)
	w.prompt(acpmock.PromptWait, "k0")
	gone := w.prompt("run echo gone", "k1")
	if err := w.svc.RemoveQueued(t.Context(), w.member, gone.ID); err != nil {
		t.Fatal(err)
	}
	held, created, err := w.db.EnqueueAssistantPrompt(t.Context(), &store.AssistantQueueEntry{ID: "turn-again", MemberID: w.member, Key: "k1",
		Text: "run echo gone", Origin: `{"door":"dashboard"}`}, maxQueued)
	if err != nil || created || held.ID != gone.ID {
		t.Fatalf("the removed prompt's key after a restart = %+v, %v, %v, want its removed entry", held, created, err)
	}
}

func TestAFailedPromptAndItsRetryCanBothBeDismissed(t *testing.T) {
	w := newQueueWorld(t)
	add := func(id string) {
		t.Helper()
		if _, created, err := w.db.EnqueueAssistantPrompt(t.Context(), &store.AssistantQueueEntry{ID: id, MemberID: w.member, Key: "k1",
			Text: "deploy status?", Origin: `{"door":"dashboard"}`}, maxQueued); err != nil || !created {
			t.Fatalf("enqueue %s = %v, %v", id, created, err)
		}
		for _, to := range []string{protocol.AssistantQueueRunning, protocol.AssistantQueueFailed} {
			from := map[string]string{protocol.AssistantQueueRunning: protocol.AssistantQueueQueued, protocol.AssistantQueueFailed: protocol.AssistantQueueRunning}[to]
			if ok, err := w.db.MoveAssistantQueueEntry(t.Context(), w.member, id, []string{from}, store.AssistantQueueMove{State: to, Error: "no agent"}); err != nil || !ok {
				t.Fatalf("move %s to %s = %v, %v", id, to, ok, err)
			}
		}
	}
	add("turn-first")
	add("turn-retry")
	for _, id := range []string{"turn-first", "turn-retry"} {
		if err := w.svc.RemoveQueued(t.Context(), w.member, id); err != nil {
			t.Fatalf("dismiss %s = %v", id, err)
		}
	}
}

func TestAnEntryListsEachCardOnce(t *testing.T) {
	w := newQueueWorld(t)
	e := w.prompt(acpmock.PromptWait, "k0")
	for range 2 {
		if err := w.db.AddAssistantQueueCard(t.Context(), w.member, e.ID, "CARD1"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := w.svc.Entry(t.Context(), w.member, e.ID); err != nil || !slices.Equal(got.Cards, []string{"CARD1"}) {
		t.Fatalf("cards = %v, %v, want one", got.Cards, err)
	}
}
