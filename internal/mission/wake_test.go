package mission

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/store"
)

func TestValidateWakeExcludesSubmittedCapacityHolder(t *testing.T) {
	ctx := context.Background()
	f, report := setupReconcileReport(t, store.CoordOutcomeSuccess)
	if err := f.svc.ValidateWake(ctx, f.attempt.RunID); err != nil {
		t.Fatalf("current worker authority: %v", err)
	}
	if err := f.svc.ReconcileReport(ctx, f.attempt.RunID, report, f.packet); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.db.GetAttempt(ctx, f.attempt.ID)
	if err != nil || attempt.State != domain.AttemptSubmitted || !attempt.State.HoldsConcurrency() {
		t.Fatalf("submission capacity fixture = %+v, %v", attempt, err)
	}
	if err := f.svc.ValidateWake(ctx, f.attempt.RunID); !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("submitted worker wake = %v, want stale", err)
	}
}

func TestValidateWakeRejectsWorkerAfterTaskRevisionChanges(t *testing.T) {
	ctx := context.Background()
	f, _ := setupReconcileReport(t, store.CoordOutcomeBlocked)
	revision, err := f.db.ProposeTaskRevision(ctx, f.task.ID, &domain.TaskRevision{Title: "replacement", Objective: "replacement"}, "wake-revise")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.AcceptTaskRevision(ctx, f.task.ID, revision.Revision, f.mission.IntegratorGeneration, f.mission.CurrentIntegratorRunID, "wake-accept"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ValidateWake(ctx, f.attempt.RunID); !errors.Is(err, store.ErrMissionStale) {
		t.Fatalf("superseded task worker wake = %v, want stale", err)
	}
}
