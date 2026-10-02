package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type deliveryTestStore struct {
	Store
	mu          sync.Mutex
	record      *store.IntegrationCandidate
	workspace   *domain.Workspace
	members     map[domain.MemberID]*domain.Member
	failUpdates int
}

func (s *deliveryTestStore) GetIntegrationCandidate(_ context.Context, id string) (*store.IntegrationCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.ID != id {
		return nil, store.ErrNotFound
	}
	copy := *s.record
	copy.Payload = append(json.RawMessage(nil), s.record.Payload...)
	return &copy, nil
}

func (s *deliveryTestStore) UpdateIntegrationCandidate(_ context.Context, record *store.IntegrationCandidate, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.Version != expected {
		return store.ErrConflict
	}
	if s.failUpdates > 0 {
		s.failUpdates--
		return errDeliveryTestLostSave
	}
	copy := *record
	copy.Payload = append(json.RawMessage(nil), record.Payload...)
	s.record = &copy
	return nil
}

func (s *deliveryTestStore) GetWorkspace(_ context.Context, id domain.WorkspaceID) (*domain.Workspace, error) {
	if s.workspace == nil || s.workspace.ID != id {
		return nil, store.ErrNotFound
	}
	copy := *s.workspace
	return &copy, nil
}

func (s *deliveryTestStore) GetMember(_ context.Context, id domain.MemberID) (*domain.Member, error) {
	member := s.members[id]
	if member == nil {
		return nil, store.ErrNotFound
	}
	copy := *member
	return &copy, nil
}

type deliveryTestGit struct {
	Git
	mu           sync.Mutex
	lookup       gitengine.CandidateGitReceipt
	lookupFound  bool
	lookupCalls  int
	deliverCalls int
	deliverErr   error
	lookupErr    error
}

func (g *deliveryTestGit) CheckCandidate(context.Context, domain.WorkspaceID, string, string) error {
	return nil
}
func (g *deliveryTestGit) LookupCandidateDelivery(context.Context, domain.WorkspaceID, string, string, string, string, string, string) (gitengine.CandidateGitReceipt, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lookupCalls++
	if g.lookupErr != nil {
		return gitengine.CandidateGitReceipt{}, false, g.lookupErr
	}
	return g.lookup, g.lookupFound, nil
}

func (g *deliveryTestGit) DeliverCandidate(context.Context, domain.WorkspaceID, string, string, string, string, string, string) (gitengine.CandidateGitReceipt, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deliverCalls++
	if g.deliverErr != nil {
		return gitengine.CandidateGitReceipt{}, g.deliverErr
	}
	return g.lookup, nil
}

var errDeliveryTestLostSave = errors.New("delivery test lost save")

func newDeliveryTestService(t *testing.T, requestState protocol.DeliveryState) (*Service, *deliveryTestStore, *deliveryTestGit) {
	t.Helper()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	exit := 0
	candidate := protocol.Candidate{
		CandidateID:            "candidate-1",
		WorkspaceID:            "workspace-1",
		CandidateRevision:      "candidate-revision-1",
		TargetRef:              "refs/heads/main",
		ExpectedTargetRevision: "target-revision-1",
		State:                  protocol.CandidateFrozen,
		ExpiresAt:              now.Add(time.Hour),
		Verifications: []protocol.Verification{{
			VerificationID:    "verification-1",
			CandidateRevision: "candidate-revision-1",
			Argv:              []string{"true"},
			Status:            protocol.VerificationPassed,
			ExitCode:          &exit,
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
		}},
	}
	candidate.DeliveryRequest = &protocol.DeliveryRequest{
		RequestID:              "request-1",
		RequestVersion:         1,
		CandidateRevision:      candidate.CandidateRevision,
		VerificationIDs:        []string{"verification-1"},
		TargetRef:              candidate.TargetRef,
		ExpectedTargetRevision: candidate.ExpectedTargetRevision,
		Action:                 protocol.DeliveryActionUpdateRef,
		State:                  requestState,
		RequestedBy:            "owner-1",
		DecidedBy:              "approver-1",
		CreatedAt:              now,
		ExpiresAt:              now.Add(time.Hour),
	}
	if requestState == protocol.DeliveryApproved {
		candidate.DeliveryRequest.DecidedAt = new(now)
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	st := &deliveryTestStore{
		record: &store.IntegrationCandidate{
			ID: "candidate-1", WorkspaceID: "workspace-1", ActorKey: "member:owner-1",
			IdempotencyKey: "prepare-1", Digest: "prepare-digest", State: string(candidate.State),
			Version: 1, Payload: payload, CreatedAt: now, ExpiresAt: candidate.ExpiresAt,
		},
		workspace: &domain.Workspace{ID: "workspace-1"},
		members: map[domain.MemberID]*domain.Member{
			"owner-1":    {ID: "owner-1", Role: domain.RoleCollaborator},
			"approver-1": {ID: "approver-1", Role: domain.RoleCollaborator},
		},
	}
	git := &deliveryTestGit{lookup: gitengine.CandidateGitReceipt{
		Result:           "landed",
		Revision:         candidate.CandidateRevision,
		PreviousRevision: candidate.ExpectedTargetRevision,
	}}
	service := &Service{
		store: st,
		git:   git,
		now:   func() time.Time { return now },
	}
	return service, st, git
}

func TestDeliverySelectionRejectsNewerFailureAcrossArgvAndEqualTimestamp(t *testing.T) {
	exit := 0
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	c := &protocol.Candidate{Verifications: []protocol.Verification{
		{VerificationID: "pass", CandidateRevision: "revision-1", Argv: []string{"true"}, Status: protocol.VerificationPassed, ExitCode: &exit, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{VerificationID: "fail", CandidateRevision: "revision-1", Argv: []string{"false"}, Status: protocol.VerificationFailed, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}}
	if err := validateVerificationSelection(c, "revision-1", []string{"pass"}, now); err == nil {
		t.Fatal("older passing verification was accepted after a newer different-argv failure")
	}
}

func TestDeliveryVersionReplacementHumanDecisionAndCallerRevocation(t *testing.T) {
	service, st, git := newDeliveryTestService(t, protocol.DeliveryPending)
	ctx := context.Background()
	first, err := service.RequestDelivery(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "request-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := service.RequestDelivery(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "request-one",
	}); !errors.Is(e, errDeliveryRequestConflict) {
		t.Fatalf("same idempotency key with different semantics = %v, want conflict", e)
	}
	if _, e := service.Decide(ctx, Actor{MemberID: "owner-1", RunID: "run-1"}, protocol.IntegrationDecideParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: first.DeliveryRequest.RequestID,
		RequestVersion: first.DeliveryRequest.RequestVersion, Approve: true,
	}); e == nil {
		t.Fatal("run actor was allowed to decide delivery")
	}
	approved, err := service.Decide(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationDecideParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: first.DeliveryRequest.RequestID,
		RequestVersion: first.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.DeliveryRequest.RequestVersion != first.DeliveryRequest.RequestVersion {
		t.Fatalf("approval changed request version: got %d want %d", approved.DeliveryRequest.RequestVersion, first.DeliveryRequest.RequestVersion)
	}
	second, err := service.RequestDelivery(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "request-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.DeliveryRequest.RequestVersion != first.DeliveryRequest.RequestVersion+1 {
		t.Fatalf("replacement version = %d, want %d", second.DeliveryRequest.RequestVersion, first.DeliveryRequest.RequestVersion+1)
	}
	if _, err := service.Decide(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationDecideParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: first.DeliveryRequest.RequestID,
		RequestVersion: first.DeliveryRequest.RequestVersion, Approve: true,
	}); err == nil {
		t.Fatal("old request approval was accepted after replacement")
	}
	st.members["owner-1"].Role = domain.RoleViewer
	if _, err := service.Deliver(ctx, Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: second.DeliveryRequest.RequestID,
		RequestVersion: second.DeliveryRequest.RequestVersion,
	}); err == nil {
		t.Fatal("revoked current caller was allowed to deliver")
	}
	if git.deliverCalls != 0 {
		t.Fatalf("revoked caller triggered Git action: %d", git.deliverCalls)
	}
}

func TestDeliveryStaleDeliveringBeforeGitDoesNotAct(t *testing.T) {
	service, _, git := newDeliveryTestService(t, protocol.DeliveryDelivering)
	candidate := serviceCandidate(t, service)
	candidate.DeliveryRequest.ExpiresAt = time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	storeCandidate(t, service, candidate)
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); err == nil {
		t.Fatal("stale delivering approval unexpectedly succeeded")
	}
	persisted := serviceCandidate(t, service)
	if persisted.DeliveryRequest.State != protocol.DeliveryApproved || persisted.DeliveryRequest.RequestVersion != 1 {
		t.Fatalf("stale delivering request = %+v, want approved version 1", persisted.DeliveryRequest)
	}
	if git.lookupCalls != 1 || git.deliverCalls != 0 {
		t.Fatalf("Git calls = lookup %d deliver %d, want read-only lookup only", git.lookupCalls, git.deliverCalls)
	}
}
func TestDeliveryUnknownReceiptThenCleanMissReleasesFence(t *testing.T) {
	service, _, git := newDeliveryTestService(t, protocol.DeliveryDelivering)
	lookupFailure := errors.New("receipt lookup unavailable")
	git.lookupErr = lookupFailure
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); !errors.Is(err, lookupFailure) {
		t.Fatalf("uncertain receipt lookup error = %v, want %v", err, lookupFailure)
	}
	if persisted := serviceCandidate(t, service); persisted.DeliveryRequest.State != protocol.DeliveryDelivering {
		t.Fatalf("request after uncertain lookup = %+v, want delivering fence", persisted.DeliveryRequest)
	}
	git.lookupErr = nil
	candidate := serviceCandidate(t, service)
	candidate.DeliveryRequest.ExpiresAt = time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	storeCandidate(t, service, candidate)
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); err == nil {
		t.Fatal("expired request after clean receipt miss unexpectedly delivered")
	}
	if persisted := serviceCandidate(t, service); persisted.DeliveryRequest.State != protocol.DeliveryApproved {
		t.Fatalf("request after clean miss = %+v, want approved for replacement", persisted.DeliveryRequest)
	}
	replacement, err := service.RequestDelivery(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "replacement-after-uncertain-receipt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.DeliveryRequest.State != protocol.DeliveryPending || replacement.DeliveryRequest.RequestVersion != 2 {
		t.Fatalf("replacement after clean miss = %+v", replacement.DeliveryRequest)
	}
	if git.deliverCalls != 0 {
		t.Fatalf("clean miss gate rejection triggered Git mutation: %d", git.deliverCalls)
	}
}

func TestDeliveryDefiniteGitFailureReleasesFenceForReplacement(t *testing.T) {
	service, _, git := newDeliveryTestService(t, protocol.DeliveryApproved)
	gitFailure := errors.New("target changed")
	git.deliverErr = gitFailure
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); !errors.Is(err, gitFailure) {
		t.Fatalf("definite Git failure = %v, want %v", err, gitFailure)
	}
	if git.deliverCalls != 1 || git.lookupCalls != 1 {
		t.Fatalf("Git calls = deliver %d lookup %d, want one mutation and one read-only reconciliation", git.deliverCalls, git.lookupCalls)
	}
	persisted := serviceCandidate(t, service)
	if persisted.DeliveryRequest.State != protocol.DeliveryApproved || persisted.DeliveryRequest.RequestVersion != 1 {
		t.Fatalf("request after definite failure = %+v, want approved version 1", persisted.DeliveryRequest)
	}
	replacement, err := service.RequestDelivery(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", CandidateRevision: "candidate-revision-1",
		VerificationIDs: []string{"verification-1"}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "replacement-after-git-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.DeliveryRequest.State != protocol.DeliveryPending ||
		replacement.DeliveryRequest.Action != protocol.DeliveryActionProposal ||
		replacement.DeliveryRequest.RequestVersion != 2 {
		t.Fatalf("replacement request = %+v", replacement.DeliveryRequest)
	}
	if _, err := service.Decide(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDecideParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1, Approve: true,
	}); err == nil {
		t.Fatal("old approval was accepted after Git failure replacement")
	}
}

func TestDeliveryCommittedReceiptRetriesReadOnlyAfterSaveFailure(t *testing.T) {
	service, st, git := newDeliveryTestService(t, protocol.DeliveryDelivering)
	st.failUpdates = 1
	git.lookupFound = true
	candidate := serviceCandidate(t, service)
	candidate.DeliveryRequest.ExpiresAt = time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	storeCandidate(t, service, candidate)
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); !errors.Is(err, errDeliveryTestLostSave) {
		t.Fatalf("first replay error = %v, want lost-save error", err)
	}
	st.members["owner-1"].Role = domain.RoleViewer
	if _, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	}); err == nil {
		t.Fatal("revoked caller was allowed to replay committed receipt")
	}
	st.members["owner-1"].Role = domain.RoleCollaborator
	st.members["approver-1"].Role = domain.RoleViewer
	replayed, err := service.Deliver(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationDeliverParams{
		WorkspaceID: "workspace-1", CandidateID: "candidate-1", RequestID: "request-1", RequestVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DeliveryReceipt == nil || replayed.DeliveryReceipt.Result != protocol.DeliveryResultLanded {
		t.Fatalf("replayed receipt = %+v", replayed.DeliveryReceipt)
	}
	if git.deliverCalls != 0 || git.lookupCalls != 2 {
		t.Fatalf("Git calls = lookup %d deliver %d, want two lookups and no mutation", git.lookupCalls, git.deliverCalls)
	}
}

func serviceCandidate(t *testing.T, service *Service) protocol.Candidate {
	t.Helper()
	_, candidate, err := service.load(context.Background(), "workspace-1", "candidate-1")
	if err != nil {
		t.Fatal(err)
	}
	return *candidate
}

func storeCandidate(t *testing.T, service *Service, candidate protocol.Candidate) {
	t.Helper()
	st := service.store.(*deliveryTestStore)
	payload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	st.record.Payload = payload
	st.record.State = string(candidate.State)
}
