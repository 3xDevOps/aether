package overlap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// fakeLookup stands in for the store: the active runs plus the sessions
// they belong to.
type fakeLookup struct {
	runs     []*domain.Run
	sessions map[domain.SessionID]*domain.Session
}

func (f *fakeLookup) ListActiveRuns(context.Context) ([]*domain.Run, error) { return f.runs, nil }

func (f *fakeLookup) GetSession(_ context.Context, id domain.SessionID) (*domain.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no session %s", id)
	}
	return s, nil
}

// oneSession is the single-workspace case: every run in session s1.
var oneSession = &fakeLookup{sessions: map[domain.SessionID]*domain.Session{
	"s1": {ID: "s1", WorkspaceID: "w1"},
}}

// TestOverlapIsScopedToWorkspace pins the only scope in which two runs can
// really conflict: the same workspace, which is the same git repository.
// Files like README.md exist in every repo, so runs in different workspaces
// touching the same path are not in conflict - while two sessions on one
// workspace share a repo and are.
func TestOverlapIsScopedToWorkspace(t *testing.T) {
	ctx := context.Background()
	bus, err := events.NewInProc(ctx, nil)
	if err != nil {
		t.Fatalf("bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	lookup := &fakeLookup{
		runs: []*domain.Run{
			{ID: "r1", SessionID: "sA"},
			{ID: "r2", SessionID: "sB"},
			{ID: "r3", SessionID: "sC"},
		},
		sessions: map[domain.SessionID]*domain.Session{
			"sA": {ID: "sA", WorkspaceID: "w1"},
			"sB": {ID: "sB", WorkspaceID: "w2"},
			"sC": {ID: "sC", WorkspaceID: "w1"},
		},
	}
	idx := NewIndex(bus, lookup, nil)
	if err = idx.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	for _, r := range lookup.runs {
		if _, err = bus.Publish(ctx, events.Event{
			SessionID: r.SessionID, RunID: r.ID,
			Payload: events.RunDiffPayload{Files: []events.FileDiffStat{{Path: "README.md"}}},
		}); err != nil {
			t.Fatalf("publish diff: %v", err)
		}
	}

	// Events are consumed in order, so r1 seeing its same-workspace peer
	// means every diff above has already been applied.
	var got []Entry
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, err = idx.Overlaps(ctx); err != nil {
			t.Fatalf("overlaps: %v", err)
		}
		if len(got) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	peers := make(map[domain.RunID][]domain.RunID)
	for _, e := range got {
		for _, p := range e.With {
			peers[e.RunID] = append(peers[e.RunID], p.RunID)
		}
	}
	if want := []domain.RunID{"r3"}; len(peers["r1"]) != 1 || peers["r1"][0] != want[0] {
		t.Errorf("r1 peers = %v, want %v", peers["r1"], want)
	}
	if want := []domain.RunID{"r1"}; len(peers["r3"]) != 1 || peers["r3"][0] != want[0] {
		t.Errorf("r3 peers = %v, want %v", peers["r3"], want)
	}
	if len(peers["r2"]) != 0 {
		t.Errorf("r2 is in another workspace but overlaps %v", peers["r2"])
	}
}
