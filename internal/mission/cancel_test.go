package mission

import (
	"context"
	"errors"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

func (f *planGateFixture) cancel(actor domain.MemberID, key string) (protocol.MissionCancelResult, error) {
	return f.svc.Cancel(context.Background(), actor, protocol.MissionCancelParams{
		MissionID: string(f.mission.ID), IdempotencyKey: key,
	})
}

// TestCancelEndsAMissionInEveryPhaseBeforeApproval: planning, clarified, and
// plan_review each move to rejected, and reconcile then stops the integrator.
func TestCancelEndsAMissionInEveryPhaseBeforeApproval(t *testing.T) {
	for phase, reach := range map[domain.MissionPhase]func(*testing.T, *planGateFixture){
		domain.MissionPhasePlanning:  func(*testing.T, *planGateFixture) {},
		domain.MissionPhaseClarified: func(t *testing.T, f *planGateFixture) { f.clarify(t, "1") },
		domain.MissionPhasePlanReview: func(t *testing.T, f *planGateFixture) {
			f.proposeAndSubmit(t)
		},
	} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			f := newPlanGateFixture(t)
			reach(t, f)
			if got := f.reloadMission(t).Phase; got != phase {
				t.Fatalf("phase before cancel = %s, want %s", got, phase)
			}
			out, err := f.cancel(f.member.ID, "cancel-1")
			if err != nil {
				t.Fatalf("cancel in %s: %v", phase, err)
			}
			if out.Mission.Phase != string(domain.MissionPhaseRejected) {
				t.Fatalf("cancel result phase = %s, want rejected", out.Mission.Phase)
			}
			if reconcileErr := f.svc.reconcileMission(ctx, f.reloadMission(t)); reconcileErr != nil {
				t.Fatalf("reconcile cancelled mission: %v", reconcileErr)
			}
			if len(f.canceller.runs) != 1 || f.canceller.runs[0] != f.mission.CurrentIntegratorRunID {
				t.Fatalf("cancelled runs = %v, want the integrator run %s", f.canceller.runs, f.mission.CurrentIntegratorRunID)
			}
			reviews, err := f.db.ListMissionPlanReviews(ctx, f.mission.ID)
			if err != nil {
				t.Fatalf("list plan reviews: %v", err)
			}
			for _, review := range reviews {
				if review.Decision != domain.MissionPlanReject || review.Feedback != "swarm cancelled" || review.DecidedByMemberID != f.member.ID || review.DecidedAt == nil {
					t.Fatalf("plan round after cancel = %+v, want rejected as swarm cancelled by %s", review, f.member.ID)
				}
			}
			if phase == domain.MissionPhasePlanReview && len(reviews) != 1 {
				t.Fatalf("plan rounds after cancel in plan_review = %d, want 1", len(reviews))
			}
		})
	}
}

func TestCancelRefusesAnApprovedOrEndedMission(t *testing.T) {
	f := newPlanGateFixture(t)
	f.activate(t, taskSpec{key: "propose-a", title: "task a", paths: []string{"internal/a"}})
	if _, err := f.cancel(f.member.ID, "cancel-active"); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("cancel in active = %v, want ErrMissionPhase", err)
	}
	tasks, err := f.db.ListTasks(context.Background(), f.mission.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("list tasks = %v (err %v), want one", tasks, err)
	}
	f.amend(t, tasks[0], protocol.TaskRevision{Title: "task a widened", Objective: "task a widened", Material: true,
		Scope: protocol.TaskScope{ExpectedPaths: []string{"internal/a", "internal/c"}}})
	if _, err := f.cancel(f.member.ID, "cancel-amendment"); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("cancel in amendment_review = %v, want ErrMissionPhase", err)
	}

	rejected := newPlanGateFixture(t)
	version := rejected.proposeAndSubmit(t)
	rejected.decide(t, version, domain.MissionPlanReject, "", "decide-reject-1")
	if _, err := rejected.cancel(rejected.member.ID, "cancel-rejected"); !errors.Is(err, store.ErrMissionPhase) {
		t.Fatalf("cancel in rejected = %v, want ErrMissionPhase", err)
	}
}

func TestCancelIsForTheAccountableHumanOrAnAdmin(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	other := regressionMember(t, f.db, "bystander")
	if _, err := f.cancel(other.ID, "cancel-foreign"); !errors.Is(err, permissions.ErrDenied) {
		t.Fatalf("foreign cancel = %v, want ErrDenied", err)
	}
	if got := f.reloadMission(t).Phase; got != domain.MissionPhasePlanning {
		t.Fatalf("phase after a refused cancel = %s, want planning", got)
	}
	other.Role = domain.RoleAdmin
	if err := f.db.UpdateMember(ctx, other); err != nil {
		t.Fatalf("promote bystander: %v", err)
	}
	if _, err := f.cancel(other.ID, "cancel-admin"); err != nil {
		t.Fatalf("admin cancel: %v", err)
	}
}

func TestCancelReplaysOnItsKeyAndRefusesItOnAnotherMission(t *testing.T) {
	f := newPlanGateFixture(t)
	first, err := f.cancel(f.member.ID, "cancel-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	replayed, err := f.cancel(f.member.ID, "cancel-1")
	if err != nil {
		t.Fatalf("replayed cancel: %v", err)
	}
	if replayed.Mission.ID != first.Mission.ID || replayed.Mission.Phase != string(domain.MissionPhaseRejected) {
		t.Fatalf("replay = %+v, want the same rejected mission %s", replayed.Mission, first.Mission.ID)
	}

	second := &domain.Mission{
		WorkspaceID: f.workspace.ID, Objective: "second mission", AccountableHumanID: f.member.ID,
		Integrator:            f.mission.Integrator,
		ExecutionChoices:      f.mission.ExecutionChoices,
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1, IdempotencyKey: "second-mission",
	}
	if err := f.db.CreateMission(context.Background(), second); err != nil {
		t.Fatalf("create second mission: %v", err)
	}
	if _, err := f.svc.Cancel(context.Background(), f.member.ID, protocol.MissionCancelParams{
		MissionID: string(second.ID), IdempotencyKey: "cancel-1",
	}); !errors.Is(err, store.ErrMissionIdempotencyConflict) {
		t.Fatalf("cancel of another mission under a used key = %v, want ErrMissionIdempotencyConflict", err)
	}
}
