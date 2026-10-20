package mission

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// TestMissionChangedNamesTheChangeAndWhatItTouched follows one task from
// proposal to an accepted submission and checks each mission.changed says
// which kind of change it was and which task and attempt it touched.
func TestMissionChangedNamesTheChangeAndWhatItTouched(t *testing.T) {
	ctx := context.Background()
	f := newMissionFixture(t)
	bus := &recordingBus{}
	f.svc.cfg.Bus = bus
	expect := func(step string, change domain.MissionChange, task domain.TaskID, attempt domain.AttemptID) {
		t.Helper()
		var last *events.MissionChangedPayload
		for _, e := range bus.events {
			if p, ok := e.Payload.(events.MissionChangedPayload); ok {
				last = &p
			}
		}
		if last == nil || last.MissionID != f.mission.ID || last.Change != change || last.TaskID != task || last.AttemptID != attempt {
			t.Fatalf("%s: last mission.changed = %+v, want change %q task %q attempt %q", step, last, change, task, attempt)
		}
	}

	task := domain.TaskID(f.propose(t, "propose"))
	expect("task.propose", domain.MissionTaskProposed, task, "")
	f.start(t, "start")
	expect("mission.start", domain.MissionPhaseChanged, "", "")
	f.mustCall(t, protocol.MethodTaskRevise, protocol.TaskReviseParams{
		TaskID: string(task), Revision: protocol.TaskRevision{Title: "clarified", Objective: "clarified"}, IdempotencyKey: "revise",
	})
	expect("task.revise", domain.MissionTaskProposed, task, "")
	f.mustCall(t, protocol.MethodTaskAccept, protocol.TaskAcceptParams{
		TaskID: string(task), Revision: 2, ExpectedIntegratorGeneration: f.mission.IntegratorGeneration, IdempotencyKey: "accept",
	})
	expect("task.accept", domain.MissionTaskAccepted, task, "")

	current, err := f.db.GetTask(ctx, task)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if startErr := f.startWorker(t, current, "dispatch"); startErr != nil {
		t.Fatalf("worker.start: %v", startErr)
	}
	attempts, err := f.db.ListAttempts(ctx, f.mission.ID, task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v (err %v), want one", attempts, err)
	}
	attempt := attempts[0]
	expect("worker.start", "", task, attempt.ID)

	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	f.evidence.packet = protocol.EvidencePacket{
		ID: "packet-1", WorkspaceID: string(f.workspace.ID), RunID: string(attempt.RunID),
		Origin:       protocol.EvidenceOrigin{Kind: protocol.EvidenceOriginRun, ID: string(attempt.RunID)},
		Availability: protocol.EvidenceAvailable, RetainedRevision: "revision-1", ExpiresAt: &expires,
	}
	submission, err := f.db.SubmitAttempt(ctx, attempt.ID, f.mission.IntegratorGeneration, f.mission.IntegratorGeneration,
		domain.SubmissionRef{WorkspaceID: f.workspace.ID, RunID: attempt.RunID, EvidenceRef: "packet-1", RetainedRevision: "revision-1"},
		[]domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "packet-1", Available: true}}, nil)
	if err != nil {
		t.Fatalf("submit attempt: %v", err)
	}
	m := f.reloadMission(t)
	f.mustCall(t, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
		SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: m.IntegratorGeneration,
		ExpectedAcceptedSetVersion: m.AcceptedSetVersion, IdempotencyKey: "accept-submission",
	})
	expect("task.submission.accept", domain.MissionTaskFinished, task, attempt.ID)
}
