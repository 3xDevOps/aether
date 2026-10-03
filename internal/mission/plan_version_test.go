package mission

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func acceptedVersionFromPlan(t *testing.T, out any) uint64 {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Plan struct {
			AcceptedSetVersion *uint64 `json:"accepted_set_version"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Plan.AcceptedSetVersion == nil {
		t.Fatalf("mission.plan.show omits accepted_set_version: %s", raw)
	}
	return *result.Plan.AcceptedSetVersion
}

func TestPlanShowDiscoversAcceptanceVersionAndWaitsForChange(t *testing.T) {
	f := newMissionFixture(t)
	task := f.activate(t, taskSpec{key: "worker", title: "worker"})[0]
	initial := f.mustCall(t, protocol.MethodMissionPlanShow, protocol.MissionPlanShowParams{})
	version := acceptedVersionFromPlan(t, initial)
	if version != 0 {
		t.Fatalf("initial accepted set = %d, want zero", version)
	}
	if err := f.startWorker(t, task, "version-worker"); err != nil {
		t.Fatal(err)
	}
	attempts, err := f.db.ListAttempts(t.Context(), f.mission.ID, task.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %v, %v", attempts, err)
	}
	attempt := attempts[0]
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	f.evidence.packet = protocol.EvidencePacket{
		ID: "version-evidence", WorkspaceID: string(f.workspace.ID), RunID: string(attempt.RunID),
		Origin:       protocol.EvidenceOrigin{Kind: protocol.EvidenceOriginRun, ID: string(attempt.RunID)},
		Availability: protocol.EvidenceAvailable, RetainedRevision: "revision-1", ExpiresAt: &expires,
	}
	submission, err := f.db.SubmitAttempt(t.Context(), attempt.ID, attempt.AuthorityGeneration, attempt.IntegratorGeneration,
		domain.SubmissionRef{WorkspaceID: f.workspace.ID, RunID: attempt.RunID, EvidenceRef: "version-evidence", RetainedRevision: "revision-1"},
		[]domain.SubmissionEvidence{{Kind: "retained_packet", Ref: "version-evidence", Available: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.reads:
	default:
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type response struct {
		out any
		err error
	}
	waited := make(chan response, 1)
	go func() {
		out, err := f.svc.HandleAgent(ctx, f.mission.CurrentIntegratorRunID, protocol.MethodMissionPlanShow, []byte(`{"wait_seconds":30}`))
		waited <- response{out, err}
	}()
	select {
	case <-f.reads:
	case <-time.After(5 * time.Second):
		t.Fatal("plan wait did not read its initial snapshot")
	}
	f.mustCall(t, protocol.MethodTaskAcceptSubmission, protocol.TaskAcceptSubmissionParams{
		SubmissionID: string(submission.ID), ExpectedIntegratorGeneration: f.mission.IntegratorGeneration,
		ExpectedAcceptedSetVersion: version, IdempotencyKey: "accept-observed-version",
	})
	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := acceptedVersionFromPlan(t, result.out); got != version+1 {
			t.Fatalf("accepted set after acceptance = %d, want %d", got, version+1)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mission.plan.show did not wake on accepted-set change")
	}
}
