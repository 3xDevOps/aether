package server

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/sysstats"
)

type runOnlyStore struct {
	store.Store
	runs map[domain.RunID]domain.Run
}

func (s runOnlyStore) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	if run, ok := s.runs[id]; ok {
		return &run, nil
	}
	return nil, store.ErrNotFound
}

func TestBrowserSidecarIsItsOwnEntryOwnedByItsRun(t *testing.T) {
	t.Parallel()
	r := &systemReader{store: runOnlyStore{runs: map[domain.RunID]domain.Run{
		"run-1": {ID: "run-1", MemberID: "mem-1", WorkspaceID: "ws-1", Title: "fix it", Status: domain.RunRunning},
	}}}
	out, err := r.attribute(t.Context(), []sysstats.ContainerUsage{
		{ID: "side", BrowserRunContainer: "run-ctr", MemoryBytes: 40, MemoryLimitBytes: 500},
		{ID: "run-ctr", CreationKey: "run-1", MemoryBytes: 100, MemoryLimitBytes: 1000},
		{ID: "env", CreationKey: "terminal:mem-2"},
		{ID: "orphan", BrowserRunContainer: "gone"},
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]sysstats.ContainerUsage{}
	for _, c := range out {
		kinds[c.ID] = c
	}
	if run := kinds["run-ctr"]; run.Owner.Kind != "run" || run.Owner.Title != "fix it" || run.MemoryBytes != 100 {
		t.Errorf("run = %+v", run)
	}
	if side := kinds["side"]; side.Owner.Kind != "browser" || side.Owner.ID != "run-1" || side.Owner.MemberID != "mem-1" || side.MemoryLimitBytes != 500 {
		t.Errorf("sidecar = %+v, want its own limits under its run", side)
	}
	if env := kinds["env"]; env.Owner.Kind != "member" || env.Owner.MemberID != "mem-2" {
		t.Errorf("environment = %+v", env)
	}
	if orphan := kinds["orphan"]; orphan.Owner.Kind != "" {
		t.Errorf("a sidecar whose run is not sampled must be unowned, got %+v", orphan.Owner)
	}
}
