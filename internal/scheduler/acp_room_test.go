package scheduler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/acphost/acpmock"
	"github.com/3xDevOps/Aether/internal/collab"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestEnhancedRunRoomMessageStaysQueuedUntilTheAgentTakesIt(t *testing.T) {
	t.Parallel()
	e, rt := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitAgentWorking(t, run.ID)
	post := roomPoster(t, e, run)
	var ids []string
	settles := func(id string, ok func(*store.RoomMessage) bool) {
		t.Helper()
		deadline := time.Now().Add(waitTimeout)
		for {
			m, err := e.db.GetRoomMessage(t.Context(), id)
			if err == nil && ok(m) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for room message %s\n%s", id, roomDiagnosis(t, e, rt, run.ID, ids))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	taken := post("say pong")
	refused := post(acpmock.PromptRefuse)
	waiting := post(acpmock.PromptWait)
	lost := post("never reaches the agent")
	ids = []string{taken, refused, waiting, lost}
	if err := e.sched.ACPCancel(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	settles(taken, func(m *store.RoomMessage) bool {
		return m.State == store.RoomMessageSent && m.AgentDelivery == store.AgentDelivered
	})
	settles(refused, func(m *store.RoomMessage) bool {
		return m.State == store.RoomMessageNotSent && m.Failure != nil && m.Failure.Code == "agent_refused"
	})
	e.waitAgentWorking(t, run.ID)
	if _, err := rt.all()[0].Stop(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{waiting, lost} {
		settles(id, func(m *store.RoomMessage) bool {
			return m.State == store.RoomMessageNotSent && m.Failure != nil && m.Failure.Code == "agent_disconnected"
		})
	}
}

func TestEnhancedRunShutdownRecordsAQueuedRoomMessageAsNotSent(t *testing.T) {
	t.Parallel()
	e, _ := newACPEnv(t)
	run := e.launchACP(t, acpmock.PromptWait)
	e.waitAgentWorking(t, run.ID)
	queued := roomPoster(t, e, run)("after this turn")
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := e.db.GetRoomMessage(t.Context(), queued)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != store.RoomMessageNotSent || m.Failure == nil || m.Failure.Code != "agent_disconnected" {
		t.Fatalf("queued message after shutdown = %q, %+v; want not_sent agent_disconnected", m.State, m.Failure)
	}
}

func roomPoster(t *testing.T, e *testEnv, run *domain.Run) func(string) string {
	t.Helper()
	ctl := control.New(control.Config{})
	room, err := collab.New(collab.Config{Store: e.db, Runs: e.db, Workspaces: e.db, Bus: e.bus, Control: ctl, Inject: e.sched.Inject})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := ctl.Acquire(string(run.ID), string(e.member.ID), "session", false)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) string {
		t.Helper()
		result, err := room.Post(t.Context(), collab.MessageInput{
			WorkspaceID: e.ws.ID, RunID: run.ID, ActorID: e.member.ID, Kind: store.RoomMessageSteerRequest,
			Body: body, IdempotencyKey: body, ControllerSessionID: lease.SessionID, ControllerGeneration: lease.Generation,
		})
		if err != nil {
			t.Fatalf("post %q: %v", body, err)
		}
		m := result.Message
		if result.Outcome != acphost.OutcomeQueued || m.State != store.RoomMessageSent || m.AgentDelivery != store.AgentQueued {
			t.Fatalf("post %q = outcome %q, state %q, agent %q; want queued", body, result.Outcome, m.State, m.AgentDelivery)
		}
		return m.ID
	}
	return post
}

func roomDiagnosis(t *testing.T, e *testEnv, rt *acpRuntime, run domain.RunID, ids []string) string {
	var b strings.Builder
	for _, id := range ids {
		m, err := e.db.GetRoomMessage(t.Context(), id)
		if err != nil {
			fmt.Fprintf(&b, "message %s: %v\n", id, err)
			continue
		}
		fmt.Fprintf(&b, "message %s %q: state %q agent %q failure %+v\n", id, m.Body, m.State, m.AgentDelivery, m.Failure)
	}
	if r, err := e.db.GetRun(t.Context(), run); err == nil {
		fmt.Fprintf(&b, "run: status %q reason %q\n", r.Status, r.Reason)
	}
	e.sched.mu.Lock()
	if entry := e.sched.runs[run]; entry != nil {
		fmt.Fprintf(&b, "agent report: %+v\n", entry.agentReport)
	}
	e.sched.mu.Unlock()
	if sess := e.sched.acp.session(run); sess != nil {
		fmt.Fprintf(&b, "session: %+v\n", sess.State())
	}
	for i, x := range rt.all() {
		fmt.Fprintf(&b, "exec %d %s: exited %v\n", i, x.identity.ExecID, x.exited())
	}
	page, err := e.sched.ACPHistory(run, 0, 0)
	if err != nil {
		fmt.Fprintf(&b, "history: %v\n", err)
	}
	items := page.Items
	for _, it := range items[max(0, len(items)-40):] {
		fmt.Fprintf(&b, "item %d turn %d %s", it.Seq, it.Turn, it.Kind)
		switch {
		case it.Message != nil:
			fmt.Fprintf(&b, " %s %q", it.Message.Role, it.Message.Text)
		case it.Notice != nil:
			fmt.Fprintf(&b, " %q %q", it.Notice.Title, it.Notice.Description)
		case it.StopReason != "":
			fmt.Fprintf(&b, " %s", it.StopReason)
		}
		b.WriteString("\n")
	}
	return b.String()
}
