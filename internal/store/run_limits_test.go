package store

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestRunLimitsRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	w, m := mustCreateWorkspace(t, db), mustCreateMember(t, db)

	if got, err := db.ServerRunLimits(ctx); err != nil || got != (domain.RunLimits{}) {
		t.Fatalf("ServerRunLimits on a fresh database = %+v, %v; want zero", got, err)
	}
	want := domain.RunLimits{CPUs: 1.5, MemoryBytes: 2 << 30, Pids: 512}
	cpus, memory, pids := want.CPUs, want.MemoryBytes, want.Pids
	if _, err := db.UpdateServerRunLimits(ctx, &cpus, &memory, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := db.UpdateServerRunLimits(ctx, nil, nil, &pids); err != nil || got != want {
		t.Fatalf("UpdateServerRunLimits of one field = %+v, %v; want %+v with the others kept", got, err, want)
	}
	if got, err := db.ServerRunLimits(ctx); err != nil || got != want {
		t.Fatalf("ServerRunLimits = %+v, %v; want %+v", got, err, want)
	}

	r := &domain.Run{WorkspaceID: w.ID, MemberID: m.ID, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued, Limits: want}
	if err := db.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRun(ctx, r.ID)
	if err != nil || got.Limits != want {
		t.Fatalf("GetRun limits = %+v, %v; want %+v", got.Limits, err, want)
	}
}
