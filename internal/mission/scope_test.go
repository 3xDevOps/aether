package mission

import (
	"context"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestObservedOverlapDiagnosticsSkipFinishedAttempts: a live worker whose
// diff snapshot cannot be read is reported as unavailable, and the same
// attempt stops being reported once it finishes, since a finished run has
// no snapshot to observe.
func TestObservedOverlapDiagnosticsSkipFinishedAttempts(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	tasks := f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	if err := f.startWorker(t, tasks[0], "worker"); err != nil {
		t.Fatalf("worker.start: %v", err)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, tasks[0].ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v (err %v), want one", attempts, err)
	}
	attempt := attempts[0]
	observed := func() []protocol.MissionScopeDiagnostic {
		t.Helper()
		show, showErr := f.svc.Show(ctx, protocol.MissionShowParams{MissionID: string(f.mission.ID)})
		if showErr != nil {
			t.Fatalf("mission.show: %v", showErr)
		}
		var out []protocol.MissionScopeDiagnostic
		for _, d := range show.Diagnostics {
			if d.Kind == scopeDiagObserved {
				out = append(out, d)
			}
		}
		return out
	}

	got := observed()
	if len(got) != 1 || !got[0].Unavailable || got[0].RunID != string(attempt.RunID) || got[0].UnavailableWhy == "" {
		t.Fatalf("live attempt without a snapshot = %+v, want one unavailable diagnostic for run %s", got, attempt.RunID)
	}

	if err = f.db.UpdateAttemptState(ctx, attempt.ID, attempt.RunID, attempt.AuthorityGeneration,
		attempt.IntegratorGeneration, domain.AttemptCompleted, "done"); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if got = observed(); len(got) != 0 {
		t.Fatalf("finished attempt = %+v, want no observed-overlap diagnostic", got)
	}
}
