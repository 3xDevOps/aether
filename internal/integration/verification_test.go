package integration

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type verificationTestRuntime struct {
	runtime.Runtime
	createCalls atomic.Int32
}

func (r *verificationTestRuntime) Create(context.Context, runtime.Spec) (runtime.ID, error) {
	r.createCalls.Add(1)
	return runtime.ID("unexpected"), nil
}

func (r *verificationTestRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	return "", runtime.ErrNotFound
}

func TestVerifySaveFailureReturnsActualErrorWithoutLaunchingWorker(t *testing.T) {
	service, store, _ := newDeliveryTestService(t, protocol.DeliveryPending)
	store.failUpdates = 1
	fakeRuntime := &verificationTestRuntime{}
	service.runtime = fakeRuntime
	service.environment = func(context.Context, Actor, *domain.Workspace, string) (runtime.Spec, error) {
		return runtime.Spec{Image: "busybox:1.36", Command: []string{"true"}}, nil
	}
	_, err := service.Verify(context.Background(), Actor{MemberID: "owner-1"}, protocol.IntegrationVerifyParams{
		WorkspaceID:       "workspace-1",
		CandidateID:       "candidate-1",
		CandidateRevision: "candidate-revision-1",
		Argv:              []string{"true"},
		TimeoutSeconds:    1,
		IdempotencyKey:    "verify-save-failure",
	})
	if !errors.Is(err, errDeliveryTestLostSave) {
		t.Fatalf("Verify save failure = %v, want %v", err, errDeliveryTestLostSave)
	}
	if got := fakeRuntime.createCalls.Load(); got != 0 {
		t.Fatalf("runtime Create calls = %d, want no worker launch", got)
	}
}

func TestVerificationRecoveryReconcilesResultArtifactAfterFinalSaveFailure(t *testing.T) {
	service, store, _ := newDeliveryTestService(t, protocol.DeliveryPending)
	service.root = t.TempDir()
	service.ctx = context.Background()
	service.cancel = func() {}
	now := service.now()
	exit := 0
	rawOutput := []byte{'o', 'r', 'i', 'g', 'i', 'n', 'a', 'l', 0, '\n', 0xff, 'x', 0x01}
	expectedOutput := strings.ToValidUTF8(string(rawOutput), "\uFFFD")
	candidate := serviceCandidate(t, service)
	candidate.Verifications[0] = protocol.Verification{
		VerificationID:    "verification-1",
		CandidateRevision: candidate.CandidateRevision,
		Status:            protocol.VerificationRunning,
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		CreationKey:       "creation-key-1",
	}
	storeCandidate(t, service, candidate)
	store.failUpdates = 1
	service.finishVerification(context.Background(), "workspace-1", "candidate-1", "verification-1",
		protocol.VerificationPassed, &exit, rawOutput, false, nil)
	if err := service.Close(); !errors.Is(err, errDeliveryTestLostSave) {
		t.Fatalf("final verification save error = %v, want %v", err, errDeliveryTestLostSave)
	}

	restarted := &Service{
		store:   service.store,
		runtime: &verificationTestRuntime{},
		root:    service.root,
		now:     service.now,
	}
	restartedCandidate := serviceCandidate(t, restarted)
	if err := restarted.recoverVerifications(context.Background(), &restartedCandidate); err != nil {
		t.Fatalf("recoverVerifications: %v", err)
	}
	recovered := serviceCandidate(t, restarted)
	v := recovered.Verifications[0]
	if v.Status != protocol.VerificationPassed || v.ExitCode == nil || *v.ExitCode != 0 ||
		v.Output != expectedOutput || v.OutputTruncated || v.FinishedAt == nil ||
		!v.FinishedAt.Equal(now) || !v.CreatedAt.Equal(now) || !v.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("recovered verification = %+v, want original terminal result", v)
	}
	if err := validateVerificationSelection(&recovered, recovered.CandidateRevision, []string{"verification-1"}, now); err != nil {
		t.Fatalf("recovered passed verification rejected for delivery: %v", err)
	}
}

func TestBoundedOutputKeepsPrefixAndMarksTruncation(t *testing.T) {
	var output boundedOutput
	prefix := strings.Repeat("a", protocol.IntegrationMaxOutputBytes-7)
	if n, err := output.Write([]byte(prefix)); err != nil || n != len(prefix) {
		t.Fatalf("prefix write = (%d, %v)", n, err)
	}
	if n, err := output.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("overflow write = (%d, %v)", n, err)
	}
	if len(output.Bytes()) != protocol.IntegrationMaxOutputBytes {
		t.Fatalf("retained output length = %d", len(output.Bytes()))
	}
	if !output.Truncated() {
		t.Fatal("output truncation was not recorded")
	}
	if got := string(output.Bytes()[len(output.Bytes())-7:]); got != "0123456" {
		t.Fatalf("prefix boundary = %q", got)
	}
}

func TestVerificationFailureClassificationObservesTimeoutAndCancellation(t *testing.T) {
	deadlineCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyVerificationFailure(deadlineCtx, context.DeadlineExceeded); got != protocol.VerificationTimedOut {
		t.Fatalf("deadline status = %q", got)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyVerificationFailure(cancelCtx, context.Canceled); got != protocol.VerificationCancelled {
		t.Fatalf("cancel status = %q", got)
	}
	if got := classifyVerificationFailure(context.Background(), errors.New("command failed")); got != protocol.VerificationError {
		t.Fatalf("runtime status = %q", got)
	}
}
