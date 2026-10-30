package timeline

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

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
	swarm := events.Filter{Runs: []domain.RunID{"run-integrator"}, Mission: "mission-1"}
	if !swarm.Matches(events.Event{Payload: events.MissionChangedPayload{MissionID: "mission-1"}}) ||
		swarm.Matches(events.Event{Payload: events.MissionChangedPayload{MissionID: "mission-2"}}) {
		t.Error("a live subscription filtered by mission must see that mission's swarm events only")
	}
}

func TestBeforeReadsTheNewestHistoryFirst(t *testing.T) {
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	const ws domain.WorkspaceID = "ws-1"
	for seq := uint64(1); seq <= 7; seq++ {
		run := domain.RunID("run-1")
		if seq%2 == 0 {
			run = "run-other"
		}
		e := events.Event{ID: fmt.Sprintf("ev-%d", seq), Seq: seq, Time: time.Unix(int64(seq), 0).UTC(), WorkspaceID: ws, RunID: run, Payload: events.RunStatusPayload{To: domain.RunRunning}}
		e.Type = e.Payload.EventType()
		if aerr := log.Append(ctx, e); aerr != nil {
			t.Fatalf("append %d: %v", seq, aerr)
		}
	}
	reader := NewReader(log, fixedMission{})
	f := Filter{Workspace: ws, Run: "run-1"}
	var pages [][]uint64
	before := uint64(0)
	for {
		page, perr := reader.Before(ctx, f, before, 2)
		if perr != nil {
			t.Fatalf("Before(%d): %v", before, perr)
		}
		if page.NextSeq != 7 {
			t.Fatalf("NextSeq = %d, want the head 7", page.NextSeq)
		}
		var seqs []uint64
		for _, e := range page.Events {
			seqs = append(seqs, e.Seq)
		}
		pages = append(pages, seqs)
		if !page.More {
			break
		}
		before = page.OlderSeq
	}
	if want := [][]uint64{{5, 7}, {1, 3}}; !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages = %v, want %v", pages, want)
	}
}

func TestDetailEventsAreExcludedFromDefaultFeed(t *testing.T) {
	for _, typ := range []events.Type{events.TypeRunTitle, events.TypeMissionChanged, events.TypeRunPR, events.TypePresence} {
		if !detailTypes[typ] {
			t.Fatalf("%s should be a detail event in the default feed", typ)
		}
	}
}

func TestARereadStatusIsLeftOutUnlessAskedForByType(t *testing.T) {
	reread := events.Event{Type: events.TypeRunStatus, Payload: events.RunStatusPayload{To: domain.RunNeedsAttention, Reread: true}}
	changed := events.Event{Type: events.TypeRunStatus, Payload: events.RunStatusPayload{To: domain.RunNeedsAttention}}
	if (Filter{}).keeps(reread) || !(Filter{}).keeps(changed) {
		t.Fatal("the default feed should keep a status change and leave out a reread")
	}
	if !(Filter{Types: []events.Type{events.TypeRunStatus}}).keeps(reread) {
		t.Fatal("asking for run.status by type should return a reread")
	}
	if (Filter{Types: []events.Type{events.TypeRunStatus}, NoRereads: true}).keeps(reread) {
		t.Fatal("NoRereads should leave a reread out even when run.status is asked for")
	}
}

// A read without a workspace has no index to bound it, so a page covers at
// most scanWindows windows of seqs and resumes from where it stopped.
func TestMemberWidePagesStepThroughSparseHistory(t *testing.T) {
	ctx := context.Background()
	log, err := events.OpenSQLiteLog(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	far := uint64(3*scanWindow*scanWindows + 5)
	for _, seq := range []uint64{1, 5, far - 1, far} {
		e := events.Event{ID: fmt.Sprintf("ev-%d", seq), Seq: seq, Time: time.Unix(int64(seq), 0).UTC(), WorkspaceID: "ws-1", RunID: "run-1", Payload: events.RunStatusPayload{To: domain.RunRunning}}
		if seq == 1 || seq == far-1 {
			e.Payload = events.RunDiffPayload{}
		}
		e.Type = e.Payload.EventType()
		if aerr := log.Append(ctx, e); aerr != nil {
			t.Fatalf("append %d: %v", seq, aerr)
		}
	}
	reader := NewReader(log, fixedMission{})
	f := Filter{Types: []events.Type{events.TypeRunStatus}}

	var found []uint64
	pages := 0
	for cursor := uint64(0); ; pages++ {
		page, perr := reader.Page(ctx, f, cursor, 10)
		if perr != nil {
			t.Fatalf("Page(%d): %v", cursor, perr)
		}
		if page.NextSeq <= cursor && page.More {
			t.Fatalf("Page(%d) did not advance: %+v", cursor, page)
		}
		for _, e := range page.Events {
			found = append(found, e.Seq)
		}
		if !page.More {
			break
		}
		cursor = page.NextSeq
	}
	if !slices.Equal(found, []uint64{5, far}) || pages < 3 {
		t.Fatalf("paged %v in %d pages, want [5 %d] over at least 3 bounded pages", found, pages+1, far)
	}

	found = nil
	for before := uint64(0); ; {
		page, perr := reader.Before(ctx, f, before, 10)
		if perr != nil {
			t.Fatalf("Before(%d): %v", before, perr)
		}
		for i := len(page.Events) - 1; i >= 0; i-- {
			found = append(found, page.Events[i].Seq)
		}
		if !page.More {
			break
		}
		if page.OlderSeq >= before && before != 0 {
			t.Fatalf("Before(%d) did not move back: %+v", before, page)
		}
		before = page.OlderSeq
	}
	if !slices.Equal(found, []uint64{far, 5}) {
		t.Fatalf("read back %v, want [%d 5]", found, far)
	}
}
