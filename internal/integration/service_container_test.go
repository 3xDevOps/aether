//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

const serviceContainerImage = "busybox:1.36"

type serviceContainerSaveFailureStore struct {
	Store
	mu              sync.Mutex
	failReceiptSave bool
	err             error
}

func (s *serviceContainerSaveFailureStore) UpdateIntegrationCandidate(ctx context.Context, c *store.IntegrationCandidate, expectedVersion int64) error {
	var candidate protocol.Candidate
	if err := json.Unmarshal(c.Payload, &candidate); err == nil && candidate.DeliveryReceipt != nil {
		s.mu.Lock()
		fail := s.failReceiptSave
		if fail {
			s.failReceiptSave = false
		}
		err := s.err
		s.mu.Unlock()
		if fail {
			return err
		}
	}
	return s.Store.UpdateIntegrationCandidate(ctx, c, expectedVersion)
}

func serviceContainerRequireDocker(t *testing.T, f *candidateLifecycleFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := f.runtime.ImageExists(ctx, serviceContainerImage); err != nil {
		if strings.TrimSpace(os.Getenv("CI")) != "" {
			t.Fatalf("Docker daemon unavailable in CI: %v", err)
		}
		t.Skipf("Docker daemon unavailable: %v", err)
	}
}

func serviceContainerInstallService(t *testing.T, f *candidateLifecycleFixture, backing Store) {
	t.Helper()
	if f.service != nil {
		_ = f.service.Close()
	}
	svc, err := New(Config{
		Store: backing, Git: f.git, Evidence: f.evidence, Runtime: f.runtime,
		Root: filepath.Join(f.root, "candidate-artifacts"),
		Environment: func(_ context.Context, _ Actor, _ *domain.Workspace, checkout string) (runtime.Spec, error) {
			return runtime.Spec{
				Image:             serviceContainerImage,
				WorktreeHostPath:  checkout,
				WorktreeMountPath: "/workspace",
				WorkingDir:        "/workspace",
				Command:           []string{"true"},
			}, nil
		},
		Now: func() time.Time {
			f.nowMu.Lock()
			defer f.nowMu.Unlock()
			return f.now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service = svc
	t.Cleanup(func() { _ = svc.Close() })
}

func serviceContainerPreparedCandidate(t *testing.T, name string, twoInputs bool) (*candidateLifecycleFixture, protocol.Candidate) {
	t.Helper()
	f := newCandidateLifecycleFixture(t, nil, false)
	serviceContainerRequireDocker(t, f)
	serviceContainerInstallService(t, f, f.db)
	first := f.source(t, name+"-one", "one.txt", "one\n", name+"-packet-one", nil)
	sources := []candidateLifecycleSource{first}
	if twoInputs {
		second := f.source(t, name+"-two", "two.txt", "two\n", name+"-packet-two", nil)
		sources = append(sources, second)
	}
	candidate := f.prepare(t, name+"-candidate", sources...)
	if candidate.State != protocol.CandidateFrozen || candidate.CandidateRevision == "" {
		t.Fatalf("prepared candidate = %+v, want frozen candidate", candidate)
	}
	for _, source := range sources {
		f.removeSource(t, source)
	}
	shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != protocol.CandidateFrozen || shown.CandidateRevision != candidate.CandidateRevision {
		t.Fatalf("candidate after source purge = %+v", shown)
	}
	return f, shown
}

func serviceContainerWaitTerminal(t *testing.T, f *candidateLifecycleFixture, candidateID string, index int) (protocol.Candidate, protocol.Verification) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		candidate, err := f.service.Show(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
			WorkspaceID: string(f.workspace.ID), CandidateID: candidateID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(candidate.Verifications) > index {
			verification := candidate.Verifications[index]
			if verification.Status != protocol.VerificationRunning {
				return candidate, verification
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("verification did not settle: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func serviceContainerVerify(t *testing.T, f *candidateLifecycleFixture, candidate protocol.Candidate, argv []string, timeout int, idem string) (protocol.Candidate, protocol.Verification) {
	t.Helper()
	started, err := f.service.Verify(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationVerifyParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		CandidateRevision: candidate.CandidateRevision, Argv: argv,
		TimeoutSeconds: timeout, IdempotencyKey: idem,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Verifications) == 0 || started.Verifications[len(started.Verifications)-1].Status != protocol.VerificationRunning {
		t.Fatalf("Verify result = %+v, want persisted running verification", started)
	}
	terminalCandidate, terminal := serviceContainerWaitTerminal(t, f, candidate.CandidateID, len(started.Verifications)-1)
	if terminal.CandidateRevision != candidate.CandidateRevision {
		t.Fatalf("verification candidate revision = %q, want frozen revision %q", terminal.CandidateRevision, candidate.CandidateRevision)
	}
	return terminalCandidate, terminal
}

func serviceContainerAssertProvenance(t *testing.T, v protocol.Verification, timeout int) {
	t.Helper()
	if v.Image != serviceContainerImage || v.ObservedImage == "" || v.WorkingDir != "/workspace" {
		t.Fatalf("verification provenance = %+v, want image=%q observed image and /workspace", v, serviceContainerImage)
	}
	if v.TimeoutSeconds != timeout || v.EnvironmentSHA256 == "" || v.SetupScriptSHA256 == "" {
		t.Fatalf("verification limits/provenance = %+v, want timeout=%d and environment/setup digests", v, timeout)
	}
}

func serviceContainerTarget(t *testing.T, f *candidateLifecycleFixture) string {
	t.Helper()
	return candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
}

func TestServiceContainerVerifyDeliverRestartReceipt(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-e2e", true)
	before := serviceContainerTarget(t, f)
	originalRevision := candidate.CandidateRevision
	verified, v := serviceContainerVerify(t, f, candidate,
		[]string{"sh", "-c", "cat one.txt two.txt"}, 30, "service-e2e-verify")
	if v.Status != protocol.VerificationPassed || v.ExitCode == nil || *v.ExitCode != 0 {
		t.Fatalf("verification = %+v, want passed exit 0", v)
	}
	if strings.TrimSpace(v.Output) != "one\ntwo" {
		t.Fatalf("verification output = %q, want both retained files", v.Output)
	}
	serviceContainerAssertProvenance(t, v, 30)
	if verified.CandidateRevision != originalRevision || verified.State != protocol.CandidateFrozen {
		t.Fatalf("candidate changed during verification: before=%s after=%s", originalRevision, verified.CandidateRevision)
	}
	if got := f.candidateCommitFile(t, verified, "one.txt"); got != "one\n" {
		t.Fatalf("owned one.txt = %q", got)
	}
	if got := f.candidateCommitFile(t, verified, "two.txt"); got != "two\n" {
		t.Fatalf("owned two.txt = %q", got)
	}

	requested, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: verified.CandidateID,
		CandidateRevision: verified.CandidateRevision, VerificationIDs: []string{v.VerificationID},
		Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "service-e2e-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requested.DeliveryRequest == nil {
		t.Fatal("RequestDelivery returned no request")
	}
	requestID := requested.DeliveryRequest.RequestID
	requestVersion := requested.DeliveryRequest.RequestVersion
	decided, err := f.service.Decide(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: verified.CandidateID,
		RequestID: requestID, RequestVersion: requestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided.DeliveryRequest == nil || decided.DeliveryRequest.State != protocol.DeliveryApproved || decided.DeliveryRequest.RequestVersion != requestVersion {
		t.Fatalf("approved request = %+v, want immutable request version %d", decided.DeliveryRequest, requestVersion)
	}

	delivered, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: verified.CandidateID,
		RequestID: requestID, RequestVersion: requestVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered.DeliveryRequest == nil || delivered.DeliveryRequest.State != protocol.DeliveryDelivered || delivered.DeliveryRequest.RequestVersion != requestVersion {
		t.Fatalf("delivered request = %+v, want immutable request version %d", delivered.DeliveryRequest, requestVersion)
	}
	if delivered.DeliveryReceipt == nil || delivered.DeliveryReceipt.Result != protocol.DeliveryResultLanded || delivered.DeliveryReceipt.CandidateRevision != originalRevision {
		t.Fatalf("delivery receipt = %+v, want landed receipt for candidate revision", delivered.DeliveryReceipt)
	}
	if got := serviceContainerTarget(t, f); got != originalRevision {
		t.Fatalf("target after delivery = %s, want candidate %s", got, originalRevision)
	}
	if delivered.DeliveryReceipt.PreviousRevision != before {
		t.Fatalf("receipt previous revision = %s, want %s", delivered.DeliveryReceipt.PreviousRevision, before)
	}

	receipt := *delivered.DeliveryReceipt
	_ = f.service.Close()
	serviceContainerInstallService(t, f, f.db)
	replayed, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: verified.CandidateID,
		RequestID: requestID, RequestVersion: requestVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DeliveryReceipt == nil || *replayed.DeliveryReceipt != receipt {
		t.Fatalf("replayed receipt = %+v, want durable original %+v", replayed.DeliveryReceipt, receipt)
	}
	if got := serviceContainerTarget(t, f); got != originalRevision {
		t.Fatalf("target after receipt replay = %s, want no new action", got)
	}
}

func TestServiceContainerVerifiedArtifactLossRejectsDelivery(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-verified-input-loss", false)
	_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-verified-input-loss-verify")
	if v.Status != protocol.VerificationPassed || v.ExitCode == nil || *v.ExitCode != 0 {
		t.Fatalf("verification = %+v, want passed exit 0", v)
	}
	requested, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "service-verified-input-loss-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := f.service.Decide(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: requested.DeliveryRequest.RequestID, RequestVersion: requested.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeTarget := serviceContainerTarget(t, f)
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	candidateLifecycleGit(t, repo, "update-ref", "-d", "refs/aether/candidates/"+candidate.CandidateID+"/inputs/0")
	if _, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: decided.DeliveryRequest.RequestID, RequestVersion: decided.DeliveryRequest.RequestVersion,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Deliver after verified input loss = %v, want unavailable", err)
	}
	shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if shown.State != protocol.CandidateUnavailable || shown.Error == "" {
		t.Fatalf("Show after verified input loss = %+v, want unavailable with error", shown)
	}
	if got := serviceContainerTarget(t, f); got != beforeTarget {
		t.Fatalf("target moved after verified input loss: before=%s after=%s", beforeTarget, got)
	}
}

func TestServiceContainerApprovedDeliveryRejectsMovedTarget(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-moved-target", false)
	_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-moved-target-verify")
	if v.Status != protocol.VerificationPassed || v.ExitCode == nil || *v.ExitCode != 0 {
		t.Fatalf("verification = %+v, want passed exit 0", v)
	}
	requested, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "service-moved-target-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := f.service.Decide(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: requested.DeliveryRequest.RequestID, RequestVersion: requested.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.DeliveryRequest == nil || approved.DeliveryRequest.State != protocol.DeliveryApproved {
		t.Fatalf("approved request = %+v", approved.DeliveryRequest)
	}
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	before := serviceContainerTarget(t, f)
	candidateLifecycleGit(t, repo, "update-ref", "refs/heads/main", candidate.CandidateRevision)
	if moved := serviceContainerTarget(t, f); moved != candidate.CandidateRevision || moved == before {
		t.Fatalf("moved target = %s, want candidate %s and prior %s", moved, candidate.CandidateRevision, before)
	}
	if _, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: approved.DeliveryRequest.RequestID, RequestVersion: approved.DeliveryRequest.RequestVersion,
	}); err == nil {
		t.Fatal("delivery unexpectedly succeeded after target movement")
	}
	shown, err := f.service.Show(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if shown.DeliveryReceipt != nil {
		t.Fatalf("receipt after moved-target rejection = %+v, want none", shown.DeliveryReceipt)
	}
	if got := serviceContainerTarget(t, f); got != candidate.CandidateRevision {
		t.Fatalf("target after moved-target rejection = %s, want moved target %s", got, candidate.CandidateRevision)
	}
}

func TestServiceContainerMirroredProposalPreservesBase(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-mirrored-proposal", false)
	repo := filepath.Join(f.repos, string(f.workspace.ID)+".git")
	upstream := filepath.Join(f.root, "mirror-upstream.git")
	if err := os.MkdirAll(upstream, 0o700); err != nil {
		t.Fatal(err)
	}
	candidateLifecycleGit(t, upstream, "init", "--bare")
	candidateLifecycleGit(t, repo, "push", upstream, "refs/heads/main:refs/heads/main")

	oldGit := f.git
	if err := oldGit.Close(); err != nil {
		t.Fatal(err)
	}
	mirrorGit, err := gitengine.New(gitengine.Config{
		ReposDir: f.repos, CheckoutsDir: filepath.Join(f.root, "checkouts"),
		MirrorFetch: func(ctx context.Context, repo string, req gitengine.MirrorRequest, incoming string) error {
			cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", req.SourceURL, "+refs/heads/"+req.Branch+":"+incoming)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
			return cmd.Run()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.git = mirrorGit
	t.Cleanup(func() { _ = mirrorGit.Close() })
	ctx := f.ctx
	mirrorRequest := gitengine.MirrorRequest{
		SourceURL: upstream, Branch: "main", Generation: 1, Auth: domain.MirrorAuthPublic,
	}
	if _, err := mirrorGit.ConfigureWorkspaceMirror(ctx, f.workspace.ID, mirrorRequest); err != nil {
		t.Fatal(err)
	}
	refreshed, err := mirrorGit.RefreshWorkspaceMirror(ctx, f.workspace.ID, mirrorRequest)
	if err != nil || refreshed.Status != domain.MirrorStatusReady || !refreshed.Changed || refreshed.CandidateCommit == "" {
		t.Fatalf("mirror refresh = %+v, %v", refreshed, err)
	}
	adopted, err := mirrorGit.AdoptWorkspaceMirror(ctx, f.workspace.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.BaseCommit == "" || adopted.BaseCommit != adopted.AcceptedCommit || adopted.BaseCommit != candidate.ExpectedTargetRevision {
		t.Fatalf("adopted mirror = %+v, expected base %s", adopted, candidate.ExpectedTargetRevision)
	}
	serviceContainerInstallService(t, f, f.db)
	_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-mirrored-proposal-verify")
	if v.Status != protocol.VerificationPassed || v.ExitCode == nil || *v.ExitCode != 0 {
		t.Fatalf("verification = %+v, want passed exit 0", v)
	}
	requested, err := f.service.RequestDelivery(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "service-mirrored-update",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := f.service.Decide(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: requested.DeliveryRequest.RequestID, RequestVersion: requested.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := serviceContainerTarget(t, f)
	if _, err := f.service.Deliver(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: approved.DeliveryRequest.RequestID, RequestVersion: approved.DeliveryRequest.RequestVersion,
	}); err == nil {
		t.Fatal("mirrored update_ref delivery unexpectedly succeeded")
	}
	if got := serviceContainerTarget(t, f); got != before {
		t.Fatalf("mirrored base after rejected update_ref = %s, want %s", got, before)
	}

	proposal, err := f.service.RequestDelivery(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "service-mirrored-proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Deliver(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: proposal.DeliveryRequest.RequestID, RequestVersion: proposal.DeliveryRequest.RequestVersion,
	}); err == nil {
		t.Fatal("replacement proposal inherited the prior update_ref approval")
	}
	approvedProposal, err := f.service.Decide(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: proposal.DeliveryRequest.RequestID, RequestVersion: proposal.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := f.service.Deliver(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: approvedProposal.DeliveryRequest.RequestID, RequestVersion: approvedProposal.DeliveryRequest.RequestVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered.DeliveryReceipt == nil || delivered.DeliveryReceipt.Result != protocol.DeliveryResultProposed || delivered.DeliveryReceipt.ProposalRef == "" {
		t.Fatalf("proposal delivery = %+v, want proposed receipt", delivered.DeliveryReceipt)
	}
	if got := serviceContainerTarget(t, f); got != before {
		t.Fatalf("mirrored base after proposal = %s, want %s", got, before)
	}
	proposalRevision := candidateLifecycleGit(t, repo, "rev-parse", delivered.DeliveryReceipt.ProposalRef)
	if proposalRevision != candidate.CandidateRevision {
		t.Fatalf("proposal ref %s = %s, want candidate %s", delivered.DeliveryReceipt.ProposalRef, proposalRevision, candidate.CandidateRevision)
	}
}

func TestServiceContainerVerificationFailureStatesRejectDelivery(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		timeout    int
		status     protocol.VerificationStatus
		wantOutput []string
	}{
		{name: "exit", argv: []string{"sh", "-c", "printf fail-output; printf fail-error >&2; exit 7"}, timeout: 30, status: protocol.VerificationFailed, wantOutput: []string{"fail-output", "fail-error"}},
		// Leave enough time for Docker setup; the command itself runs past the deadline.
		{name: "timeout", argv: []string{"sh", "-c", "echo before-timeout; sleep 30"}, timeout: 10, status: protocol.VerificationTimedOut, wantOutput: []string{"before-timeout"}},
		{name: "tracked-source-mutation", argv: []string{"sh", "-c", "printf mutated > one.txt"}, timeout: 30, status: protocol.VerificationSourceChanged, wantOutput: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, candidate := serviceContainerPreparedCandidate(t, "service-failure-"+tc.name, false)
			beforeTarget := serviceContainerTarget(t, f)
			beforeRevision := candidate.CandidateRevision
			got, v := serviceContainerVerify(t, f, candidate, tc.argv, tc.timeout, "service-failure-"+tc.name)
			if v.Status != tc.status {
				t.Fatalf("verification status = %q, want %q (error=%q output=%q)", v.Status, tc.status, v.Error, v.Output)
			}
			serviceContainerAssertProvenance(t, v, tc.timeout)
			for _, want := range tc.wantOutput {
				if !strings.Contains(v.Output, want) {
					t.Fatalf("verification output = %q, want substring %q", v.Output, want)
				}
			}
			if tc.status != protocol.VerificationFailed && v.Error == "" {
				t.Fatalf("verification = %+v, want terminal error provenance", v)
			}
			if got.CandidateRevision != beforeRevision || got.State != protocol.CandidateFrozen {
				t.Fatalf("candidate changed after %s: %+v", tc.name, got)
			}
			if tc.name == "tracked-source-mutation" {
				if content := f.candidateCommitFile(t, got, "one.txt"); content != "one\n" {
					t.Fatalf("immutable candidate source after mutation = %q", content)
				}
			}
			if _, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
				WorkspaceID: string(f.workspace.ID), CandidateID: got.CandidateID,
				CandidateRevision: got.CandidateRevision, VerificationIDs: []string{v.VerificationID},
				Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "reject-" + tc.name,
			}); err == nil {
				t.Fatal("RequestDelivery accepted a non-passing verification")
			}
			if afterTarget := serviceContainerTarget(t, f); afterTarget != beforeTarget {
				t.Fatalf("target moved after rejected %s delivery: before=%s after=%s", tc.name, beforeTarget, afterTarget)
			}
		})
	}
}

func TestServiceContainerDeliveryRejectsStaleApprovalAndVerification(t *testing.T) {
	t.Run("replacement-request-invalidates-old-approval", func(t *testing.T) {
		f, candidate := serviceContainerPreparedCandidate(t, "service-replacement", false)
		_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-replacement-verify")
		first, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
			WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
			VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "replacement-first",
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
			WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
			VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionProposal, IdempotencyKey: "replacement-second",
		})
		if err != nil {
			t.Fatal(err)
		}
		if second.DeliveryRequest == nil || second.DeliveryRequest.RequestID == first.DeliveryRequest.RequestID || second.DeliveryRequest.RequestVersion <= first.DeliveryRequest.RequestVersion || second.DeliveryRequest.Action != protocol.DeliveryActionProposal {
			t.Fatalf("replacement request = %+v, first = %+v", second.DeliveryRequest, first.DeliveryRequest)
		}
		if _, err := f.service.Decide(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
			WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
			RequestID: first.DeliveryRequest.RequestID, RequestVersion: first.DeliveryRequest.RequestVersion, Approve: true,
		}); err == nil {
			t.Fatal("stale replaced request was approved")
		}
		if got := serviceContainerTarget(t, f); got != candidate.ExpectedTargetRevision {
			t.Fatalf("target changed while rejecting stale approval: %s", got)
		}
	})

	t.Run("expired-verification-rejected", func(t *testing.T) {
		f, candidate := serviceContainerPreparedCandidate(t, "service-expired", false)
		_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-expired-verify")
		f.nowMu.Lock()
		f.now = v.ExpiresAt.Add(time.Second)
		f.nowMu.Unlock()
		if _, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
			WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
			VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "expired-request",
		}); err == nil {
			t.Fatal("RequestDelivery accepted expired verification")
		}
	})
}

func TestServiceContainerNewDifferentArgvFailureInvalidatesOldPass(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-newer-failure", false)
	_, first := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-newer-green")
	_, newer := serviceContainerVerify(t, f, candidate, []string{"sh", "-c", "echo newer-red >&2; exit 9"}, 30, "service-newer-red")
	if first.Status != protocol.VerificationPassed || newer.Status != protocol.VerificationFailed {
		t.Fatalf("verifications = first=%+v newer=%+v", first, newer)
	}
	before := serviceContainerTarget(t, f)
	if _, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{first.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "newer-failure-request",
	}); err == nil {
		t.Fatal("RequestDelivery selected an old green result despite newer different-argv failure")
	}
	if got := serviceContainerTarget(t, f); got != before {
		t.Fatalf("target moved after stale green rejection: before=%s after=%s", before, got)
	}
}

func TestServiceContainerCleanupInterruptedVerification(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-recovery", false)
	live := f.service
	started, err := live.Verify(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationVerifyParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		Argv: []string{"sh", "-c", "sleep 20"}, TimeoutSeconds: 30, IdempotencyKey: "service-recovery-verify",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Verifications) != 1 {
		t.Fatalf("started verification = %+v", started.Verifications)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	var running protocol.Verification
	for {
		shown, showErr := live.Show(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID})
		if showErr != nil {
			t.Fatal(showErr)
		}
		if len(shown.Verifications) == 1 && shown.Verifications[0].Status == protocol.VerificationRunning && shown.Verifications[0].ContainerID != "" {
			running = shown.Verifications[0]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("verification never persisted runtime identity: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}

	// A live service's active registry protects its running worker from a
	// normal sweep; it must not destroy the container it still owns.
	if n, err := live.Cleanup(ctx); err != nil || n != 0 {
		t.Fatalf("live cleanup = %d, %v; want no active-worker cleanup", n, err)
	}
	if _, err := f.runtime.FindByCreationKey(ctx, running.CreationKey); err != nil {
		t.Fatalf("live cleanup removed active verification container: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.FindByCreationKey(ctx, running.CreationKey); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("graceful service close container lookup = %v, want destroyed", err)
	}

	// Stage a real Docker container and its durable running record after the
	// worker is gone. This is the persisted crash boundary: no Service worker
	// owns the record, and no result/output is fabricated.
	serviceContainerInstallService(t, f, f.db)
	recoveryID := "service-recovery-crash"
	checkout, err := f.git.CandidateVerificationCheckout(ctx, f.workspace.ID, candidate.CandidateID, recoveryID, candidate.CandidateRevision)
	if err != nil {
		t.Fatal(err)
	}
	spec := runtime.Spec{
		Image: serviceContainerImage, WorktreeHostPath: checkout, WorktreeMountPath: "/workspace",
		WorkingDir: "/workspace", Command: []string{"sh", "-c", "sleep 20"},
		CreationKey: "service-recovery-crash-" + candidate.CandidateID,
	}
	containerID, err := f.runtime.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.runtime.Destroy(context.Background(), containerID) })
	info, err := f.runtime.Inspect(ctx, containerID)
	if err != nil {
		t.Fatal(err)
	}
	f.nowMu.Lock()
	created := f.now
	f.nowMu.Unlock()
	recovery := protocol.Verification{
		VerificationID: recoveryID, CandidateRevision: candidate.CandidateRevision,
		Argv: spec.Command, Image: spec.Image, ObservedImage: info.Image, User: info.User,
		WorkingDir: spec.WorkingDir, TimeoutSeconds: 30, EnvironmentSHA256: digest(spec.Env),
		SetupScriptSHA256: digest(spec.SetupScript), Status: protocol.VerificationRunning,
		CreatedAt: created, ExpiresAt: created.Add(protocol.IntegrationVerificationLifetime),
		CreationKey: spec.CreationKey, ContainerID: string(containerID),
	}
	if err := func() error {
		lock := f.service.lock(candidate.CandidateID)
		lock.Lock()
		defer lock.Unlock()
		record, fresh, loadErr := f.service.load(ctx, string(f.workspace.ID), candidate.CandidateID)
		if loadErr != nil {
			return loadErr
		}
		fresh.Verifications = append(fresh.Verifications, recovery)
		return f.service.save(ctx, record, fresh)
	}(); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.Start(ctx, containerID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.FindByCreationKey(ctx, spec.CreationKey); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("restarted cleanup container lookup = %v, want destroyed", err)
	}
	recovered, showErr := f.service.Show(ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationShowParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
	})
	if showErr != nil {
		t.Fatal(showErr)
	}
	if len(recovered.Verifications) != 2 {
		t.Fatalf("recovered verifications = %+v, want both durable records", recovered.Verifications)
	}
	terminal := recovered.Verifications[1]
	if terminal.VerificationID != recoveryID || terminal.Status != protocol.VerificationError || terminal.ExitCode != nil || terminal.Output != "" || terminal.Error == "" {
		t.Fatalf("recovered verification = %+v, want persisted interruption error without exit/output", terminal)
	}
}

func TestServiceContainerDeliveryReceiptReplayAfterLostSave(t *testing.T) {
	f, candidate := serviceContainerPreparedCandidate(t, "service-lost-save", false)
	_, v := serviceContainerVerify(t, f, candidate, []string{"true"}, 30, "service-lost-save-verify")
	requested, err := f.service.RequestDelivery(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationRequestDeliveryParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, CandidateRevision: candidate.CandidateRevision,
		VerificationIDs: []string{v.VerificationID}, Action: protocol.DeliveryActionUpdateRef, IdempotencyKey: "service-lost-save-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := f.service.Decide(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDecideParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		RequestID: requested.DeliveryRequest.RequestID, RequestVersion: requested.DeliveryRequest.RequestVersion, Approve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID := decided.DeliveryRequest.RequestID
	requestVersion := decided.DeliveryRequest.RequestVersion
	lost := errors.New("simulated lost post-Git save")
	backing := &serviceContainerSaveFailureStore{Store: f.db, failReceiptSave: true, err: lost}
	serviceContainerInstallService(t, f, backing)
	before := serviceContainerTarget(t, f)
	if _, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, RequestID: requestID, RequestVersion: requestVersion,
	}); !errors.Is(err, lost) {
		t.Fatalf("Deliver lost-save error = %v, want %v", err, lost)
	}
	if got := serviceContainerTarget(t, f); got != candidate.CandidateRevision {
		t.Fatalf("target after Git commit/lost save = %s, want %s", got, candidate.CandidateRevision)
	}
	row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	var delivering protocol.Candidate
	if err := json.Unmarshal(row.Payload, &delivering); err != nil {
		t.Fatal(err)
	}
	if delivering.DeliveryRequest == nil || delivering.DeliveryRequest.State != protocol.DeliveryDelivering || delivering.DeliveryRequest.RequestVersion != requestVersion {
		t.Fatalf("durable lost-save state = %+v, want delivering request version %d", delivering.DeliveryRequest, requestVersion)
	}

	// Restart against the real database and reconcile the private Git receipt.

	serviceContainerInstallService(t, f, f.db)
	replayed, err := f.service.Deliver(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationDeliverParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID, RequestID: requestID, RequestVersion: requestVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DeliveryReceipt == nil || replayed.DeliveryReceipt.RequestID != requestID || replayed.DeliveryRequest == nil || replayed.DeliveryRequest.State != protocol.DeliveryDelivered {
		t.Fatalf("replayed delivery = %+v, want durable receipt replay", replayed)
	}
	if got := serviceContainerTarget(t, f); got != candidate.CandidateRevision || got == before {
		t.Fatalf("target after receipt replay = %s, want exactly one landing from %s", got, before)
	}
}
