package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestCoordReportSupersessionFollowsRunLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   domain.LaunchMode
		role   string
		status domain.RunStatus
		live   bool
	}{
		{"standard", domain.LaunchTUI, "", domain.RunRunning, true},
		{"enhanced", domain.LaunchACP, "", domain.RunNeedsAttention, true},
		{"standard integrator", domain.LaunchTUI, "integrator", domain.RunRunning, true},
		{"enhanced integrator", domain.LaunchACP, "integrator", domain.RunNeedsAttention, true},
		{"background", domain.LaunchHeadless, "", domain.RunRunning, false},
		{"background integrator", domain.LaunchHeadless, "integrator", domain.RunRunning, false},
		{"standard worker", domain.LaunchTUI, "worker", domain.RunRunning, false},
		{"enhanced worker", domain.LaunchACP, "worker", domain.RunRunning, false},
		{"closed standard", domain.LaunchTUI, "", domain.RunAbandoned, false},
		{"closed enhanced", domain.LaunchACP, "", domain.RunCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			workspace := mustCreateWorkspace(t, db)
			member := mustCreateMember(t, db)
			run := &domain.Run{
				WorkspaceID: workspace.ID, MemberID: member.ID, Task: "report tasks",
				Harness: "claude", Mode: tc.mode, Status: tc.status,
			}
			if tc.role != "" {
				mission := mustCreateMission(t, db, workspace.ID, member.ID)
				run.ID = mission.CurrentIntegratorRunID
				if tc.role == "worker" {
					task := mustCreateMissionTask(t, db, mission.ID, "assigned task")
					attempt, _, err := reserveMissionAttempt(t, db, mission, task, "worker")
					if err != nil {
						t.Fatalf("reserve worker: %v", err)
					}
					run.ID = attempt.RunID
				}
				if err := db.CreateRunWithID(ctx, run); err != nil {
					t.Fatalf("create assigned run: %v", err)
				}
			} else if err := db.CreateRun(ctx, run); err != nil {
				t.Fatalf("create run: %v", err)
			}
			report := func(outcome CoordOutcome, key string) *CoordReport {
				return &CoordReport{
					WorkspaceID: workspace.ID, RunID: run.ID, Outcome: outcome,
					Summary: key, IdempotencyKey: key, EvidenceRefs: []string{"evidence-" + key},
				}
			}
			first := report(CoordOutcomeSuccess, "first")
			if err := db.AppendCoordReport(ctx, first); err != nil {
				t.Fatalf("first report: %v", err)
			}
			replay := report(CoordOutcomeSuccess, "first")
			if err := db.AppendCoordReport(ctx, replay); err != nil || replay.ID != first.ID {
				t.Fatalf("same-key replay = %+v, %v; want %s", replay, err, first.ID)
			}
			changed := report(CoordOutcomeFailure, "first")
			if err := db.AppendCoordReport(ctx, changed); !errors.Is(err, ErrCoordReportIdempotencyConflict) {
				t.Fatalf("changed payload = %v, want idempotency conflict", err)
			}
			previous := first
			for i, outcome := range []CoordOutcome{CoordOutcomeFailure, CoordOutcomeBlocked, CoordOutcomeSuccess} {
				next := report(outcome, fmt.Sprintf("next-%d", i))
				err := db.AppendCoordReport(ctx, next)
				if !tc.live {
					if !errors.Is(err, ErrCoordReportConflict) {
						t.Fatalf("new %s report = %v, want terminal slot conflict", outcome, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("next %s report: %v", outcome, err)
				}
				prior, err := db.GetCoordReport(ctx, previous.ID)
				if err != nil || (previous.Outcome != CoordOutcomeBlocked && prior.SupersededAt == nil) {
					t.Fatalf("previous report = %+v, %v; want terminal outcome superseded", prior, err)
				}
				previous = next
			}
			stored, err := db.GetCoordReport(ctx, first.ID)
			if err != nil || (stored.SupersededAt != nil) != tc.live || stored.State != CoordReportFinalized || len(stored.EvidenceRefs) != 1 {
				t.Fatalf("first report history = %+v, %v; want finalized evidence and superseded=%v", stored, err, tc.live)
			}
			if _, err := db.GetCoordReportPublication(ctx, first.ID); err != nil {
				t.Fatalf("first report lost its outbox: %v", err)
			}
			if tc.live {
				if err := db.AppendCoordReport(ctx, report(CoordOutcomeSuccess, "first")); !errors.Is(err, ErrCoordReportSuperseded) {
					t.Fatalf("superseded replay = %v, want superseded conflict", err)
				}
				if err := db.AppendCoordReport(ctx, report(CoordOutcomeBlocked, "next-1")); err != nil {
					t.Fatalf("historical blocked replay: %v", err)
				}
				current, err := db.GetCoordReport(ctx, previous.ID)
				if err != nil || current.SupersededAt != nil {
					t.Fatalf("replays changed the latest outcome: %+v, %v", current, err)
				}
			}
		})
	}
}

func TestInteractiveReportSupersedesPendingCapture(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	pending := &CoordReport{WorkspaceID: workspace.ID, RunID: run.ID, Outcome: CoordOutcomeSuccess, Summary: "first", IdempotencyKey: "first"}
	if _, err := db.ReserveCoordReport(ctx, pending); err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	invalid := &CoordReport{WorkspaceID: "missing-workspace", RunID: run.ID, Outcome: CoordOutcomeFailure, Summary: "invalid", IdempotencyKey: "invalid"}
	if _, err := db.ReserveCoordReport(ctx, invalid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid reservation = %v, want missing workspace", err)
	}
	if got, err := db.GetCoordReport(ctx, pending.ID); err != nil || got.SupersededAt != nil {
		t.Fatalf("failed reservation changed previous report: %+v, %v", got, err)
	}
	next := &CoordReport{WorkspaceID: workspace.ID, RunID: run.ID, Outcome: CoordOutcomeBlocked, Summary: "need a decision", IdempotencyKey: "next"}
	if err := db.AppendCoordReport(ctx, next); err != nil {
		t.Fatalf("new blocked report: %v", err)
	}
	pending.EvidenceRefs = []string{"late-evidence"}
	if finalized, err := db.FinalizeCoordReport(ctx, pending); finalized || !errors.Is(err, ErrCoordReportSuperseded) {
		t.Fatalf("late capture = %v, %v; want superseded conflict", finalized, err)
	}
	if got, err := db.GetCoordReport(ctx, pending.ID); err != nil || got.State != CoordReportPending || got.FinalizedAt != nil || got.SupersededAt == nil {
		t.Fatalf("late capture accepted: %+v, %v", got, err)
	}
	if _, err := db.GetCoordReportPublication(ctx, pending.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late capture publication = %v, want absent", err)
	}
}

func TestInteractiveReportReservationsSupersedeAtomically(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	const count = 8
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := db.ReserveCoordReport(ctx, &CoordReport{
				WorkspaceID: workspace.ID, RunID: run.ID, Outcome: CoordOutcomeSuccess,
				Summary: "done", IdempotencyKey: fmt.Sprintf("report-%d", i),
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reservation: %v", err)
		}
	}
	var total, active int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(superseded_at IS NULL) FROM coord_reports WHERE run_id = ?`, run.ID).Scan(&total, &active); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if total != count || active != 1 {
		t.Fatalf("reservations = %d total, %d active; want %d and 1", total, active, count)
	}
}

func TestInteractiveReportedOutcomeKeepsRunUnfinished(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
	for _, reason := range []string{"agent reported success", "agent reported failure"} {
		if err := db.FinishRunReported(ctx, run.ID, domain.RunNeedsAttention, reason, nil, nil); err != nil {
			t.Fatalf("record outcome: %v", err)
		}
		got, err := db.GetRun(ctx, run.ID)
		if err != nil || got.Status != domain.RunNeedsAttention || got.Reason != reason || !got.OutcomeUnseen || got.FinishedAt != nil {
			t.Fatalf("parked run = %+v, %v; want unseen unfinished outcome", got, err)
		}
		if err = db.UpdateRunStatus(ctx, run.ID, domain.RunRunning, "agent working", nil, nil); err != nil {
			t.Fatalf("resume: %v", err)
		}
		got, err = db.GetRun(ctx, run.ID)
		if err != nil || got.Status != domain.RunRunning || got.OutcomeUnseen || got.FinishedAt != nil {
			t.Fatalf("resumed run = %+v, %v; want running without unseen outcome", got, err)
		}
	}
}

func TestRunOutcomeUnseenFollowsIdleReason(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			status domain.RunStatus
			reason string
			unseen bool
		}{
			{"same outcome", domain.RunNeedsAttention, "agent reported success", true},
			{"blocked replaces outcome", domain.RunNeedsAttention, "blocked: need a decision", false},
			{"native failure replaces outcome", domain.RunNeedsAttention, "agent failed", false},
			{"terminal retention relabel", domain.RunCompleted, "agent reported success; retained container", true},
		} {
			t.Run(fmt.Sprintf("%s/snapshot=%v", tc.name, snapshot), func(t *testing.T) {
				ctx := context.Background()
				db := openTestDB(t)
				workspace := mustCreateWorkspace(t, db)
				member := mustCreateMember(t, db)
				run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunRunning)
				if err := db.FinishRunReported(ctx, run.ID, tc.status, "agent reported success", nil, nil); err != nil {
					t.Fatalf("record outcome: %v", err)
				}
				if snapshot {
					row, err := db.GetRun(ctx, run.ID)
					if err != nil {
						t.Fatalf("get run: %v", err)
					}
					row.Reason = tc.reason
					if err := db.UpdateRun(ctx, row); err != nil {
						t.Fatalf("update run: %v", err)
					}
				} else if err := db.UpdateRunStatus(ctx, run.ID, tc.status, tc.reason, nil, nil); err != nil {
					t.Fatalf("update status: %v", err)
				}
				got, err := db.GetRun(ctx, run.ID)
				if err != nil || got.Status != tc.status || got.Reason != tc.reason || got.OutcomeUnseen != tc.unseen {
					t.Fatalf("updated run = %+v, %v; want unseen=%v", got, err, tc.unseen)
				}
			})
		}
	}
}
