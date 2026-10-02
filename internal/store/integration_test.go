package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func integrationRecord(id string, workspace domain.WorkspaceID, state string, expires time.Time, payload string) *IntegrationCandidate {
	now := time.Unix(1_700_000_000, 0).UTC()
	return &IntegrationCandidate{
		ID:             id,
		WorkspaceID:    workspace,
		ActorKey:       "member:ada",
		IdempotencyKey: "prepare-" + id,
		Digest:         "digest-" + id,
		State:          state,
		Version:        1,
		Payload:        json.RawMessage(payload),
		CreatedAt:      now,
		ExpiresAt:      expires,
	}
}

func TestIntegrationCandidateCASIdempotencyAndSourceDeletion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := mustCreateWorkspace(t, db)
	member := mustCreateMember(t, db)
	run := mustCreateRun(t, db, workspace.ID, member.ID, domain.RunCompleted)

	now := time.Unix(1_700_000_000, 0).UTC()
	record := integrationRecord("candidate-cas", workspace.ID, "preparing", now.Add(time.Hour), `{"candidate_id":"candidate-cas","workspace_id":"ws","state":"preparing","target_ref":"refs/heads/main","expected_target_revision":"base","verifications":[],"delivery_request":{"request_id":"req-1"}}`)
	if err := db.CreateIntegrationCandidate(ctx, record); err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	got, err := db.GetIntegrationCandidate(ctx, record.ID)
	if err != nil {
		t.Fatalf("get candidate: %v", err)
	}
	if got.ActorKey != record.ActorKey || got.IdempotencyKey != record.IdempotencyKey || got.Digest != record.Digest || got.Version != 1 {
		t.Fatalf("immutable envelope changed on create: %#v", got)
	}
	byKey, err := db.GetIntegrationCandidateByKey(ctx, workspace.ID, record.ActorKey, record.IdempotencyKey)
	if err != nil || byKey.ID != record.ID {
		t.Fatalf("get by idempotency key = %#v, %v", byKey, err)
	}
	duplicate := *record
	duplicate.ID = "candidate-duplicate"
	if createErr := db.CreateIntegrationCandidate(ctx, &duplicate); !errors.Is(createErr, ErrConflict) {
		t.Fatalf("duplicate idempotency key = %v, want ErrConflict", createErr)
	}

	stale := *got
	stale.Version = 2
	stale.State = "frozen"
	stale.Payload = json.RawMessage(`{"candidate_id":"candidate-cas","state":"frozen","verifications":[]}`)
	if updateErr := db.UpdateIntegrationCandidate(ctx, &stale, 1); updateErr != nil {
		t.Fatalf("first CAS update: %v", updateErr)
	}
	changed := stale
	changed.Version = 3
	changed.State = "unavailable"
	changed.Payload = json.RawMessage(`{"candidate_id":"candidate-cas","state":"unavailable","verifications":[]}`)
	changed.ActorKey = "member:other"
	changed.IdempotencyKey = "other-key"
	changed.Digest = "other-digest"
	changed.WorkspaceID = "other-workspace"
	if updateErr := db.UpdateIntegrationCandidate(ctx, &changed, 2); updateErr != nil {
		t.Fatalf("metadata-preserving CAS update: %v", updateErr)
	}
	if updateErr := db.UpdateIntegrationCandidate(ctx, &stale, 1); !errors.Is(updateErr, ErrIntegrationConflict) {
		t.Fatalf("stale CAS update = %v, want ErrIntegrationConflict", updateErr)
	}
	stored, err := db.GetIntegrationCandidate(ctx, record.ID)
	if err != nil {
		t.Fatalf("get updated candidate: %v", err)
	}
	if stored.ActorKey != record.ActorKey || stored.IdempotencyKey != record.IdempotencyKey || stored.Digest != record.Digest || stored.WorkspaceID != workspace.ID {
		t.Fatalf("CAS rewrote immutable metadata: %#v", stored)
	}

	if err := db.DeleteRun(ctx, run.ID); err != nil {
		t.Fatalf("delete source run: %v", err)
	}
	if _, err := db.GetIntegrationCandidate(ctx, record.ID); err != nil {
		t.Fatalf("candidate removed with source run: %v", err)
	}
	if err := db.DeleteIntegrationCandidate(ctx, record.ID, 3); err != nil {
		t.Fatalf("delete candidate: %v", err)
	}
	if _, err := db.GetIntegrationCandidate(ctx, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted candidate = %v, want ErrNotFound", err)
	}
}

func TestIntegrationCandidateSummaryAndCleanupTraversal(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	workspace := domain.WorkspaceID("workspace-summary")
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, candidate := range []*IntegrationCandidate{
		integrationRecord("candidate-a", workspace, "preparing", now.Add(time.Hour), `{"candidate_id":"candidate-a","state":"preparing","verifications":[]}`),
		integrationRecord("candidate-b", workspace, "frozen", now.Add(time.Hour), `{"candidate_id":"candidate-b","state":"frozen","candidate_revision":"rev-b","target_ref":"refs/heads/main","expected_target_revision":"base-b","verifications":[{"status":"running"}]}`),
		integrationRecord("candidate-c", workspace, "frozen", now.Add(time.Hour), `{"candidate_id":"candidate-c","state":"frozen","candidate_revision":"rev-c","target_ref":"refs/heads/main","expected_target_revision":"base-c","verifications":[]}`),
		integrationRecord("candidate-d", workspace, "frozen", now.Add(-time.Hour), `{"candidate_id":"candidate-d","state":"frozen","verifications":[]}`),
		integrationRecord("candidate-e", workspace, "expired", now.Add(time.Hour), `{"candidate_id":"candidate-e","state":"expired","verifications":[]}`),
	} {
		if err := db.CreateIntegrationCandidate(ctx, candidate); err != nil {
			t.Fatalf("create %s: %v", candidate.ID, err)
		}
	}
	page, err := db.ListIntegrationCandidates(ctx, workspace, 10)
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	var foundFrozen bool
	for _, summary := range page {
		if summary.CandidateID == "candidate-b" {
			foundFrozen = summary.State == "frozen" &&
				summary.CandidateRevision == "rev-b" &&
				summary.TargetRef == "refs/heads/main" &&
				summary.ExpectedTargetRevision == "base-b"
		}
	}
	if !foundFrozen {
		t.Fatalf("bounded summary projection omitted candidate-b: %#v", page)
	}
	cleanup, err := db.ListIntegrationCleanupCandidates(ctx, now, 10)
	if err != nil {
		t.Fatalf("list cleanup candidates: %v", err)
	}
	if len(cleanup) != 4 {
		t.Fatalf("cleanup candidates = %d, want 4", len(cleanup))
	}
	first, err := db.ListIntegrationCleanupCandidatesAfter(ctx, now, "", 1)
	if err != nil || len(first) != 1 || first[0].ID != "candidate-a" {
		t.Fatalf("first cleanup cursor page = %#v, %v", first, err)
	}
	second, err := db.ListIntegrationCleanupCandidatesAfter(ctx, now, first[0].ID, 10)
	if err != nil || len(second) != 3 || second[0].ID != "candidate-b" || second[1].ID != "candidate-d" || second[2].ID != "candidate-e" {
		t.Fatalf("second cleanup cursor page = %#v, %v", second, err)
	}
}
