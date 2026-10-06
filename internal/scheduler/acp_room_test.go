package scheduler

import (
	"testing"

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
	settles := func(id string, ok func(*store.RoomMessage) bool) {
		t.Helper()
		waitFor(t, "room message "+id, func() bool {
			m, err := e.db.GetRoomMessage(t.Context(), id)
			return err == nil && ok(m)
		})
	}

	taken := post("say pong")
	refused := post(acpmock.PromptRefuse)
	waiting := post(acpmock.PromptWait)
	lost := post("never reaches the agent")
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
