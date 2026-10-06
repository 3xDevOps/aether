package timeline

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

type fixedMission map[domain.MissionID][]domain.RunID

func (f fixedMission) ListMissionRunIDs(_ context.Context, id domain.MissionID) ([]domain.RunID, error) {
	return slices.Clone(f[id]), nil
}

func TestRunAndMissionFiltersMatchBothSidesOfAgentMail(t *testing.T) {
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	bus, err := events.NewInProc(ctx, log)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	const ws domain.WorkspaceID = "ws-1"
	publish := func(id string, run domain.RunID, payload events.Payload) {
		t.Helper()
		if _, perr := bus.Publish(ctx, events.Event{ID: id, WorkspaceID: ws, RunID: run, Payload: payload}); perr != nil {
			t.Fatalf("publish %s: %v", id, perr)
		}
	}
	publish("integrator-up", "run-integrator", events.RunStatusPayload{To: domain.RunRunning})
	publish("worker-up", "run-worker", events.RunStatusPayload{To: domain.RunRunning})
	publish("bystander-up", "run-bystander", events.RunStatusPayload{To: domain.RunRunning})
	publish("report", "run-worker", events.CoordMessagePayload{
		MessageID: "msg-1", WorkspaceID: ws, MissionID: "mission-1",
		FromRunID: "run-worker", ToRunID: "run-integrator", Kind: "report",
	})
	publish("overlap", "run-bystander", events.CoordMessagePayload{
		MessageID: "msg-2", WorkspaceID: ws, FromRunID: "run-bystander", ToRunID: "run-integrator", Kind: "message",
	})
	publish("ack", "run-integrator", events.CoordMessageAckedPayload{MessageID: "msg-1", ToRunID: "run-integrator", AckedAt: "2026-10-05T12:00:00Z"})

	reader := NewReader(log, fixedMission{"mission-1": {"run-integrator", "run-worker"}})
	ids := func(f Filter) []string {
		t.Helper()
		f.Workspace = ws
		page, perr := reader.Page(ctx, f, 0, 0)
		if perr != nil {
			t.Fatalf("Page(%+v): %v", f, perr)
		}
		var out []string
		for _, e := range page.Events {
			out = append(out, e.ID)
		}
		return out
	}
	for name, tc := range map[string]struct {
		filter Filter
		want   []string
	}{
		"recipient run":    {Filter{Run: "run-integrator"}, []string{"integrator-up", "report", "overlap"}},
		"sender run":       {Filter{Run: "run-bystander"}, []string{"bystander-up", "overlap"}},
		"mission":          {Filter{MissionID: "mission-1"}, []string{"integrator-up", "worker-up", "report", "overlap"}},
		"run in mission":   {Filter{MissionID: "mission-1", Run: "run-worker"}, []string{"worker-up", "report"}},
		"run outside":      {Filter{MissionID: "mission-1", Run: "run-bystander"}, nil},
		"unknown mission":  {Filter{MissionID: "mission-2"}, nil},
		"typed agent mail": {Filter{MissionID: "mission-1", Types: []events.Type{events.TypeCoordMessage}}, []string{"report", "overlap"}},
		"typed acks":       {Filter{Run: "run-integrator", Types: []events.Type{events.TypeCoordMessageAcked}}, []string{"ack"}},
	} {
		if got := ids(tc.filter); !slices.Equal(got, tc.want) {
			t.Errorf("%s: events = %v, want %v", name, got, tc.want)
		}
	}
	matcher := events.Filter{Runs: []domain.RunID{"run-integrator"}}
	if !matcher.Matches(events.Event{RunID: "run-worker", Payload: events.CoordMessagePayload{ToRunID: "run-integrator"}}) {
		t.Error("a live subscription filtered by run must see mail addressed to it")
	}
}

func TestRunTitleIsExcludedFromDefaultFeed(t *testing.T) {
	if !detailTypes[events.TypeRunTitle] {
		t.Fatal("run.title should be a detail event in the default feed")
	}
}
