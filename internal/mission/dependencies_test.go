package mission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// TestDependencyHoldsTheDependentUntilTheDependencyIsAccepted follows the
// integrator's path: declare depends_on while planning, watch the dependent
// stay Blocked with the blocking task named, and see it turn Ready once the
// dependency's submission is accepted.
func TestDependencyHoldsTheDependentUntilTheDependencyIsAccepted(t *testing.T) {
	ctx := context.Background()
	f := newPlanGateFixture(t)
	propose := func(key string, revision protocol.TaskRevision) (protocol.Task, error) {
		out, err := f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskPropose, protocol.TaskProposeParams{
			MissionID: string(f.mission.ID), Revision: revision, IdempotencyKey: key,
		})
		if err != nil {
			return protocol.Task{}, err
		}
		return out.(protocol.TaskMutationResult).Task, nil
	}
	first, err := propose("propose-first", protocol.TaskRevision{Title: "first", Objective: "first"})
	if err != nil {
		t.Fatalf("propose first: %v", err)
	}
	dependent := protocol.TaskRevision{Title: "second", Objective: "second", DependsOn: []string{first.ID}}
	second, err := propose("propose-second", dependent)
	if err != nil {
		t.Fatalf("propose second: %v", err)
	}
	if len(second.Dependencies) != 1 || second.Dependencies[0].DependsOnTaskID != first.ID {
		t.Fatalf("proposed dependencies = %+v, want one on %s", second.Dependencies, first.ID)
	}
	replayed, err := propose("propose-second", dependent)
	if err != nil || replayed.ID != second.ID {
		t.Fatalf("replayed propose = %+v (err %v), want task %s", replayed, err, second.ID)
	}

	if _, unknownErr := propose("propose-unknown", protocol.TaskRevision{Title: "third", Objective: "third", DependsOn: []string{"task-missing"}}); !errors.Is(unknownErr, store.ErrNotFound) {
		t.Fatalf("unknown dependency error = %v, want ErrNotFound", unknownErr)
	}
	invalidState := func(step string, err error, want ...string) {
		t.Helper()
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeInvalidState {
			t.Fatalf("%s error = %v, want invalid state", step, err)
		}
		for _, text := range want {
			if !strings.Contains(rpcErr.Message, text) {
				t.Fatalf("%s error = %q, want it to name %q", step, rpcErr.Message, text)
			}
		}
	}
	_, err = f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: second.ID, IdempotencyKey: "revise-self",
		Revision: protocol.TaskRevision{Title: "second", Objective: "second", DependsOn: []string{second.ID}},
	})
	invalidState("self dependency", err, "task "+second.ID+" waits for task "+second.ID)
	_, err = f.call(t, f.mission.CurrentIntegratorRunID, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: first.ID, IdempotencyKey: "revise-cycle",
		Revision: protocol.TaskRevision{Title: "first", Objective: "first", DependsOn: []string{second.ID}},
	})
	invalidState("cycle", err, "task "+first.ID+" waits for task "+second.ID+", which waits for task "+first.ID)
	if reloaded, reloadErr := f.db.GetTask(ctx, domain.TaskID(first.ID)); reloadErr != nil || reloaded.CurrentRevision != 1 {
		t.Fatalf("first after refused revise = %+v (err %v), want revision 1 untouched", reloaded, reloadErr)
	}

	f.clarify(t, "1")
	f.decide(t, f.submit(t, "1", domain.MissionPhasePlanReview), domain.MissionPlanApprove, "", "decide-approve-1")
	blocked, err := f.db.GetTask(ctx, domain.TaskID(second.ID))
	if err != nil {
		t.Fatalf("reload second: %v", err)
	}
	if blocked.Status != domain.TaskBlocked || len(blocked.Blockers) != 1 || blocked.Blockers[0].Kind != "dependency" || blocked.Blockers[0].TaskID != domain.TaskID(first.ID) {
		t.Fatalf("second after approval = %s %+v, want blocked by %s", blocked.Status, blocked.Blockers, first.ID)
	}
	err = f.startWorker(t, blocked, "dispatch-second")
	if !errors.Is(err, store.ErrMissionNotReady) || !strings.Contains(err.Error(), "task "+second.ID+" waits for task "+first.ID) {
		t.Fatalf("worker.start on a blocked task = %v, want ErrMissionNotReady naming %s", err, first.ID)
	}

	ready, err := f.db.GetTask(ctx, domain.TaskID(first.ID))
	if err != nil {
		t.Fatalf("reload first: %v", err)
	}
	if startErr := f.startWorker(t, ready, "dispatch-first"); startErr != nil {
		t.Fatalf("worker.start on the dependency: %v", startErr)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, ready.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v (err %v), want one", attempts, err)
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	f.evidence.packet = protocol.EvidencePacket{
		ID: "packet-1", WorkspaceID: string(f.workspace.ID), RunID: string(attempts[0].RunID),
		Availability: protocol.EvidenceAvailable, RetainedRevision: "revision-1", ExpiresAt: &expires,
	}
	submission, err := f.db.SubmitAttempt(ctx, attempts[0].ID, f.mission.IntegratorGeneration, f.mission.IntegratorGeneration,
		domain.SubmissionRef{WorkspaceID: f.workspace.ID, RunID: attempts[0].RunID, EvidenceRef: "packet-1", RetainedRevision: "revision-1"},
		[]domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet-1", Available: true}}, nil)
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}
	m := f.reloadMission(t)
	f.mustCall(t, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
		SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: m.IntegratorGeneration,
		ExpectedAcceptedSetVersion: m.AcceptedSetVersion, IdempotencyKey: "accept-first-submission",
	})
	unblocked, err := f.db.GetTask(ctx, domain.TaskID(second.ID))
	if err != nil {
		t.Fatalf("reload second after acceptance: %v", err)
	}
	if unblocked.Status != domain.TaskReady || len(unblocked.Blockers) != 0 {
		t.Fatalf("second after the dependency was accepted = %s %+v, want ready", unblocked.Status, unblocked.Blockers)
	}
	if startErr := f.startWorker(t, unblocked, "dispatch-second-again"); startErr != nil {
		t.Fatalf("worker.start once the dependency is accepted: %v", startErr)
	}
}
