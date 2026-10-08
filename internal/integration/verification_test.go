package integration

import (
	"context"
	"errors"
	"io"
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

type verificationCleanupRuntime struct {
	runtime.Runtime
	findID     runtime.ID
	findErr    error
	destroyErr error
	events     []string
}

func (r *verificationCleanupRuntime) Stop(context.Context, runtime.ID, time.Duration) error {
	r.events = append(r.events, "stop")
	return nil
}

func (r *verificationCleanupRuntime) Destroy(context.Context, runtime.ID) error {
	r.events = append(r.events, "destroy")
	return r.destroyErr
}

func (r *verificationCleanupRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	r.events = append(r.events, "find")
	return r.findID, r.findErr
}

func TestCleanupVerificationRuntimeReleasesOnlyAfterAbsence(t *testing.T) {
	rt := &verificationCleanupRuntime{findErr: errors.New("runtime unavailable")}
	events := make([]string, 0, 4)
	svc := &Service{
		runtime: rt,
		releaseRuntime: func(context.Context, string) error {
			events = append(events, "release")
			return nil
		},
	}
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err == nil {
		t.Fatal("unknown runtime liveness returned nil")
	}
	if len(events) != 0 {
		t.Fatalf("release events after unknown liveness = %v, want none", events)
	}

	rt.findErr = runtime.ErrNotFound
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err != nil {
		t.Fatalf("cleanup after absent runtime: %v", err)
	}
	if len(events) != 1 || events[0] != "release" {
		t.Fatalf("release events after absent runtime = %v, want [release]", events)
	}

	events = events[:0]
	rt.events = nil
	rt.findErr = nil
	rt.findID = "container-id"
	if err := svc.cleanupVerificationRuntime(context.Background(), "creation-key", ""); err != nil {
		t.Fatalf("cleanup after found runtime: %v", err)
	}
	if got, want := append(rt.events, events...), []string{"find", "stop", "destroy", "release"}; len(got) != len(want) {
		t.Fatalf("cleanup event count = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("cleanup events = %v, want %v", got, want)
			}
		}
	}
}

type verificationProvisioningGit struct {
	*deliveryTestGit
	checkout string
}

func (g *verificationProvisioningGit) CandidateVerificationCheckout(context.Context, domain.WorkspaceID, string, string, string) (string, error) {
	return g.checkout, nil
}

func (g *verificationProvisioningGit) CheckCandidateVerification(context.Context, domain.WorkspaceID, string, string, string) error {
	return nil
}

func (g *verificationProvisioningGit) RemoveCandidateVerification(context.Context, domain.WorkspaceID, string, string) error {
	return nil
}

type verificationProvisioningAttachment struct{ runtime.Attachment }

func (*verificationProvisioningAttachment) Stdout() io.Reader { return strings.NewReader("output") }
func (*verificationProvisioningAttachment) Stderr() io.Reader { return strings.NewReader("") }
func (*verificationProvisioningAttachment) Close() error      { return nil }

type verificationProvisioningRuntime struct {
	verificationCleanupRuntime
	t               *testing.T
	reserved        *bool
	durableReleases *int
	fail            string
	store           *deliveryTestStore
	stages          []string
}

func (r *verificationProvisioningRuntime) Create(_ context.Context, spec runtime.Spec) (runtime.ID, error) {
	r.stages = append(r.stages, "create")
	if !*r.reserved || *r.durableReleases != 0 || spec.CreationKey != "provisioning-key" {
		r.t.Error("Create lacks allowance or durable creation key")
	}
	if r.fail == "create" || r.fail == "create-unknown" {
		return "", errors.New("create failed")
	}
	if r.fail == "save" {
		r.store.failUpdates = 1
	}
	return "provisioning-container", nil
}

func (r *verificationProvisioningRuntime) Inspect(context.Context, runtime.ID) (runtime.ContainerInfo, error) {
	r.stages = append(r.stages, "inspect")
	if r.fail == "inspect" {
		return runtime.ContainerInfo{}, errors.New("inspect failed")
	}
	if r.fail == "metadata" {
		r.store.failUpdates = 1
	}
	return runtime.ContainerInfo{Image: "busybox"}, nil
}

func (r *verificationProvisioningRuntime) Attach(context.Context, runtime.ID) (runtime.Attachment, error) {
	r.stages = append(r.stages, "attach")
	if r.fail == "attach" {
		return nil, errors.New("attach failed")
	}
	return &verificationProvisioningAttachment{}, nil
}

func (r *verificationProvisioningRuntime) Start(context.Context, runtime.ID) error {
	r.stages = append(r.stages, "start")
	if !*r.reserved {
		r.t.Error("allowance released before Start")
	}
	if r.fail == "start" {
		return errors.New("start failed")
	}
	return nil
}

func (r *verificationProvisioningRuntime) Wait(context.Context, runtime.ID) (runtime.ExitStatus, error) {
	r.stages = append(r.stages, "wait")
	if *r.reserved {
		r.t.Error("verification command holds provisioning allowance")
	}
	if *r.durableReleases != 0 {
		r.t.Error("verification command lost durable ownership before cleanup")
	}
	if r.fail == "wait" {
		return runtime.ExitStatus{}, errors.New("wait failed")
	}
	// Nonzero command exit skips evidence validation, which is unrelated to
	// the startup allowance; it must still release durable resources.
	return runtime.ExitStatus{Code: 1}, nil
}

func (r *verificationProvisioningRuntime) FindByCreationKey(ctx context.Context, key string) (runtime.ID, error) {
	if *r.reserved {
		r.t.Error("runtime lookup still holds startup allowance")
	}
	if key != "provisioning-key" {
		r.t.Errorf("runtime lookup key changed: %q", key)
	}
	return r.verificationCleanupRuntime.FindByCreationKey(ctx, key)
}

func (r *verificationProvisioningRuntime) Stop(ctx context.Context, id runtime.ID, timeout time.Duration) error {
	if *r.reserved {
		r.t.Error("runtime cleanup still holds startup allowance")
	}
	return r.verificationCleanupRuntime.Stop(ctx, id, timeout)
}

func TestVerificationProvisioningAllowanceSpansCreateStartOnly(t *testing.T) {
	for _, tc := range []struct {
		failure   string
		stages    string
		errorText string
		pending   bool
	}{
		{failure: "", stages: "prepare,authorize,create,inspect,attach,authorize-start,start,wait"},
		{failure: "prepare", stages: "prepare", errorText: "capacity refused"},
		{failure: "key", stages: "prepare", errorText: "runtime preparation changed creation key"},
		{failure: "validate", stages: "prepare", errorText: "image is required"},
		{failure: "authorize", stages: "prepare,authorize", errorText: ErrUnauthorized.Error()},
		{failure: "create", stages: "prepare,authorize,create", errorText: "create failed"},
		{failure: "save", stages: "prepare,authorize,create", errorText: errDeliveryTestLostSave.Error()},
		{failure: "inspect", stages: "prepare,authorize,create,inspect", errorText: "inspect failed"},
		{failure: "metadata", stages: "prepare,authorize,create,inspect", errorText: errDeliveryTestLostSave.Error()},
		{failure: "attach", stages: "prepare,authorize,create,inspect,attach", errorText: "attach failed"},
		{failure: "authorize-start", stages: "prepare,authorize,create,inspect,attach,authorize-start", errorText: ErrUnauthorized.Error()},
		{failure: "start", stages: "prepare,authorize,create,inspect,attach,authorize-start,start", errorText: "start failed"},
		{failure: "wait", stages: "prepare,authorize,create,inspect,attach,authorize-start,start,wait", errorText: "wait failed"},
		{failure: "create-unknown", stages: "prepare,authorize,create", errorText: "lookup unavailable", pending: true},
		{failure: "destroy", stages: "prepare,authorize,create,inspect,attach,authorize-start,start,wait", errorText: "destroy unavailable", pending: true},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			svc, st, git := newDeliveryTestService(t, protocol.DeliveryPending)
			svc.root = t.TempDir()
			svc.git = &verificationProvisioningGit{deliveryTestGit: git, checkout: t.TempDir()}
			candidate := serviceCandidate(t, svc)
			candidate.Verifications[0].Status = protocol.VerificationRunning
			candidate.Verifications[0].CreationKey = "provisioning-key"
			storeCandidate(t, svc, candidate)
			reserved, releases, durableReleases := false, 0, 0
			rt := &verificationProvisioningRuntime{
				verificationCleanupRuntime: verificationCleanupRuntime{findErr: runtime.ErrNotFound},
				t:                          t, reserved: &reserved, durableReleases: &durableReleases, fail: tc.failure, store: st,
			}
			if tc.failure == "create-unknown" {
				rt.findErr = errors.New("lookup unavailable")
			}
			if tc.failure == "destroy" {
				rt.findID = "provisioning-container"
				rt.findErr = nil
				rt.destroyErr = errors.New("destroy unavailable")
			}
			svc.runtime = rt
			svc.environment = func(context.Context, Actor, *domain.Workspace, string) (runtime.Spec, error) {
				// runVerification supplies the host checkout; the environment
				// must supply its container mount just as the server does.
				return runtime.Spec{Image: "busybox", WorktreeMountPath: "/workspace", WorkingDir: "/workspace"}, nil
			}
			svc.prepareRuntime = func(_ context.Context, spec *runtime.Spec) (func(), error) {
				rt.stages = append(rt.stages, "prepare")
				reserved = true
				release := func() { reserved = false; releases++ }
				switch tc.failure {
				case "prepare":
					return release, errors.New("capacity refused")
				case "key":
					spec.CreationKey = "changed"
				case "validate":
					spec.Image = ""
				}
				return release, nil
			}
			svc.releaseRuntime = func(_ context.Context, key string) error {
				if reserved {
					t.Error("cleanup still holds startup allowance")
				}
				if key != "provisioning-key" {
					t.Errorf("cleanup key changed: %q", key)
				}
				durableReleases++
				return nil
			}
			authorizations := 0
			svc.admission = func(context.Context, Admission) (func(), error) {
				stage := "authorize"
				if authorizations > 0 {
					stage = "authorize-start"
				}
				authorizations++
				rt.stages = append(rt.stages, stage)
				if tc.failure == stage {
					return nil, ErrUnauthorized
				}
				return func() {}, nil
			}
			svc.runVerification(t.Context(), Actor{MemberID: "owner-1"}, "workspace-1", "candidate-1", "verification-1", []string{"true"}, time.Second)
			result := serviceCandidate(t, svc).Verifications[0]
			if got := strings.Join(rt.stages, ","); got != tc.stages {
				t.Fatalf("stages = %q, want %q; verification error: %s", got, tc.stages, result.Error)
			}
			wantDurableReleases := 1
			if tc.pending {
				wantDurableReleases = 0
			}
			if reserved || releases != 1 || durableReleases != wantDurableReleases {
				t.Fatalf("resources stranded: reserved=%v, allowance releases=%d, durable releases=%d; verification error: %s", reserved, releases, durableReleases, result.Error)
			}
			wantStatus := protocol.VerificationError
			if tc.failure == "" {
				wantStatus = protocol.VerificationFailed
				if result.ExitCode == nil || *result.ExitCode != 1 {
					t.Fatalf("command exit was not recorded: %+v", result)
				}
			}
			if tc.pending {
				wantStatus = protocol.VerificationRunning
			}
			if result.Status != wantStatus || !strings.Contains(result.Error, tc.errorText) || result.CreationKey != "provisioning-key" {
				t.Fatalf("verification = %+v, want status %s and error containing %q with original key", result, wantStatus, tc.errorText)
			}
			if tc.pending {
				// Retry from persisted state, with no worker allowance surviving
				// the restart. Only confirmed runtime cleanup releases the owner.
				rt.findErr = runtime.ErrNotFound
				rt.destroyErr = nil
				if tc.failure == "destroy" {
					rt.findErr = nil
				}
				reboot := &Service{store: svc.store, git: svc.git, runtime: rt, root: svc.root, now: svc.now, releaseRuntime: svc.releaseRuntime}
				persisted := serviceCandidate(t, reboot)
				if recoveryErr := reboot.recoverVerifications(t.Context(), &persisted); recoveryErr != nil {
					t.Fatalf("recover cleanup: %v", recoveryErr)
				}
				if durableReleases != 1 || releases != 1 || serviceCandidate(t, reboot).Verifications[0].Status != protocol.VerificationError {
					t.Fatal("restart did not release exactly the durable owner and finish verification")
				}
			}
		})
	}
}

func TestVerificationFailedDestroyRecoveryKeepsDurableCleanupKey(t *testing.T) {
	svc, _, git := newDeliveryTestService(t, protocol.DeliveryPending)
	svc.root = t.TempDir()
	svc.git = &verificationProvisioningGit{deliveryTestGit: git, checkout: t.TempDir()}
	rt := &verificationCleanupRuntime{findID: "still-live", destroyErr: errors.New("destroy unavailable")}
	svc.runtime = rt
	released := false
	svc.releaseRuntime = func(_ context.Context, key string) error {
		if key != "restart-key" {
			t.Fatalf("lost cleanup key: %q", key)
		}
		released = true
		return nil
	}
	candidate := serviceCandidate(t, svc)
	candidate.Verifications[0].Status = protocol.VerificationRunning
	candidate.Verifications[0].CreationKey = "restart-key"
	storeCandidate(t, svc, candidate)
	if err := svc.recoverVerifications(t.Context(), &candidate); err == nil || released {
		t.Fatalf("failed destroy released ownership: %v, released=%v", err, released)
	}
	if got := serviceCandidate(t, svc).Verifications[0]; got.Status != protocol.VerificationRunning || got.CreationKey != "restart-key" {
		t.Fatalf("cleanup failure lost durable retry: %+v", got)
	}
	rt.destroyErr = nil
	reboot := &Service{store: svc.store, git: svc.git, runtime: rt, root: svc.root, now: svc.now, releaseRuntime: svc.releaseRuntime}
	candidate = serviceCandidate(t, reboot)
	if err := reboot.recoverVerifications(t.Context(), &candidate); err != nil || !released {
		t.Fatalf("restart did not release confirmed cleanup: %v, released=%v", err, released)
	}
}
