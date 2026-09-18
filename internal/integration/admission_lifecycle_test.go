//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

func newAdmissionLifecycleService(t *testing.T, f *candidateLifecycleFixture, rt runtime.Runtime, admission AdmissionFunc) *Service {
	t.Helper()
	if f.service != nil {
		_ = f.service.Close()
	}
	svc, err := New(Config{
		Store: f.db, Git: f.git, Evidence: f.evidence, Runtime: rt,
		Root: filepath.Join(f.root, "candidate-artifacts"),
		Environment: func(_ context.Context, _ Actor, _ *domain.Workspace, checkout string) (runtime.Spec, error) {
			return runtime.Spec{
				Image: "aether-test-image", WorktreeHostPath: checkout,
				WorktreeMountPath: "/workspace", WorkingDir: "/workspace",
				Command: []string{"true"},
			}, nil
		},
		Admission: admission,
		Now: func() time.Time {
			f.nowMu.Lock()
			defer f.nowMu.Unlock()
			return f.now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	f.service = svc
	return svc
}

func TestPrepareAdmissionBindsBeforePersistenceAndReplayObservesBinding(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	var callbackErr error
	var newCalls, replayCalls int
	admission := func(_ context.Context, a Admission) (func(), error) {
		if a.Candidate == nil {
			callbackErr = errors.New("prepare admission received nil candidate")
			return nil, callbackErr
		}
		if a.NewCandidate {
			newCalls++
			if row, err := f.db.GetIntegrationCandidate(context.Background(), a.Candidate.CandidateID); err == nil || row != nil {
				callbackErr = errors.New("candidate was persisted before new admission")
				return nil, callbackErr
			}
			a.Candidate.MissionAcceptedSetVersion = 17
		} else {
			replayCalls++
			if a.Candidate.MissionAcceptedSetVersion != 17 {
				callbackErr = errors.New("replay admission did not observe persisted binding")
			}
		}
		return func() {}, nil
	}
	svc := newAdmissionLifecycleService(t, f, f.runtime, admission)
	source := f.source(t, "prepare-admission-binding", "source.txt", "source\n", "packet-prepare-admission", nil)
	base := candidateLifecycleGit(t, filepath.Join(f.repos, string(f.workspace.ID)+".git"), "rev-parse", "refs/heads/main")
	params := protocol.IntegrationPrepareParams{
		WorkspaceID: string(f.workspace.ID),
		Submissions: []protocol.SubmissionRef{{WorkspaceID: string(f.workspace.ID), RunID: string(source.run.ID), EvidenceRef: source.packet.ID, RetainedRevision: source.packet.RetainedRevision}},
		TargetRef:   "refs/heads/main", ExpectedTargetRevision: base,
		IdempotencyKey: "prepare-admission-binding",
	}
	candidate, err := svc.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatal(err)
	}
	if callbackErr != nil || newCalls != 1 || replayCalls != 0 {
		t.Fatalf("new admission calls = (%d, %d), error=%v", newCalls, replayCalls, callbackErr)
	}
	if candidate.MissionAcceptedSetVersion != 17 {
		t.Fatalf("prepared binding = %d, want 17", candidate.MissionAcceptedSetVersion)
	}
	row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted protocol.Candidate
	if err := json.Unmarshal(row.Payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.MissionAcceptedSetVersion != 17 {
		t.Fatalf("persisted binding = %d, want 17", persisted.MissionAcceptedSetVersion)
	}
	replayed, err := svc.Prepare(f.ctx, Actor{MemberID: f.owner.ID}, params)
	if err != nil {
		t.Fatal(err)
	}
	if callbackErr != nil || newCalls != 1 || replayCalls != 1 {
		t.Fatalf("replay admission calls = (%d, %d), error=%v", newCalls, replayCalls, callbackErr)
	}
	if replayed.MissionAcceptedSetVersion != 17 {
		t.Fatalf("replayed binding = %d, want 17", replayed.MissionAcceptedSetVersion)
	}
}

type admissionLifecycleRuntime struct {
	runtime.Runtime
	gate     chan struct{}
	waitDone chan struct{}
	doneOnce sync.Once
	mu       sync.Mutex
	started  bool
	blocked  bool
	stalled  bool
}

func (r *admissionLifecycleRuntime) Create(context.Context, runtime.Spec) (runtime.ID, error) {
	return "admission-lifecycle-container", nil
}

func (r *admissionLifecycleRuntime) Start(context.Context, runtime.ID) error {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	return nil
}

func (r *admissionLifecycleRuntime) Stop(context.Context, runtime.ID, time.Duration) error {
	return nil
}
func (r *admissionLifecycleRuntime) Destroy(context.Context, runtime.ID) error { return nil }

func (r *admissionLifecycleRuntime) Inspect(context.Context, runtime.ID) (runtime.ContainerInfo, error) {
	return runtime.ContainerInfo{Image: "aether-test-image"}, nil
}
func (r *admissionLifecycleRuntime) Attach(context.Context, runtime.ID) (runtime.Attachment, error) {
	if r.stalled {
		return newAdmissionLifecycleStalledAttachment(), nil
	}
	return &admissionLifecycleAttachment{
		stdout: strings.NewReader("successful output\n"),
		stderr: strings.NewReader(""),
	}, nil
}

func (r *admissionLifecycleRuntime) Wait(context.Context, runtime.ID) (runtime.ExitStatus, error) {
	acquired := false
	select {
	case <-r.gate:
		acquired = true
	case <-time.After(200 * time.Millisecond):
		r.mu.Lock()
		r.blocked = true
		r.mu.Unlock()
	}
	if acquired {
		r.gate <- struct{}{}
	}
	r.doneOnce.Do(func() { close(r.waitDone) })
	return runtime.ExitStatus{Code: 0}, nil
}

func (r *admissionLifecycleRuntime) FindByCreationKey(context.Context, string) (runtime.ID, error) {
	return "", runtime.ErrNotFound
}

type admissionLifecycleAttachment struct {
	runtime.Attachment
	stdout io.Reader
	stderr io.Reader
}

func (a *admissionLifecycleAttachment) Stdin() io.WriteCloser {
	return admissionLifecycleDiscardWriter{}
}
func (a *admissionLifecycleAttachment) Stdout() io.Reader { return a.stdout }
func (a *admissionLifecycleAttachment) Stderr() io.Reader { return a.stderr }
func (a *admissionLifecycleAttachment) Resize(context.Context, uint, uint) error {
	return nil
}
func (a *admissionLifecycleAttachment) Close() error { return nil }

type admissionLifecycleDiscardWriter struct{}

func (admissionLifecycleDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (admissionLifecycleDiscardWriter) Close() error                { return nil }

type admissionLifecycleStalledAttachment struct {
	admissionLifecycleAttachment
	closeCh   chan struct{}
	closeOnce sync.Once
}

func newAdmissionLifecycleStalledAttachment() *admissionLifecycleStalledAttachment {
	closeCh := make(chan struct{})
	return &admissionLifecycleStalledAttachment{
		admissionLifecycleAttachment: admissionLifecycleAttachment{
			stdout: &admissionLifecycleStalledReader{closeCh: closeCh},
			stderr: &admissionLifecycleStalledReader{closeCh: closeCh},
		},
		closeCh: closeCh,
	}
}

func (a *admissionLifecycleStalledAttachment) Close() error {
	a.closeOnce.Do(func() { close(a.closeCh) })
	return nil
}

type admissionLifecycleStalledReader struct {
	closeCh chan struct{}
	sent    bool
}

func (r *admissionLifecycleStalledReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "stalled output\n"), nil
	}
	<-r.closeCh
	return 0, io.EOF
}

func TestVerifyReleasesAdmissionBeforeRuntimeWait(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	candidate := f.prepare(t, "verify-admission-wait", f.source(t, "verify-admission-wait", "source.txt", "source\n", "packet-verify-admission", nil))
	rt := &admissionLifecycleRuntime{gate: make(chan struct{}, 1), waitDone: make(chan struct{})}
	rt.gate <- struct{}{}
	var admissions int
	admission := func(_ context.Context, _ Admission) (func(), error) {
		<-rt.gate
		admissions++
		return func() { rt.gate <- struct{}{} }, nil
	}
	svc := newAdmissionLifecycleService(t, f, rt, admission)
	started, err := svc.Verify(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationVerifyParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		CandidateRevision: candidate.CandidateRevision, Argv: []string{"true"},
		TimeoutSeconds: 30, IdempotencyKey: "verify-admission-wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Verifications) != 1 || started.Verifications[0].Status != protocol.VerificationRunning {
		t.Fatalf("Verify result = %+v, want queued running verification", started)
	}
	select {
	case <-rt.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("verification worker did not reach Wait")
	}
	rt.mu.Lock()
	blocked := rt.blocked
	rt.mu.Unlock()
	if blocked {
		t.Fatal("runtime Wait observed the authorization fence still held")
	}
	if admissions != 3 {
		t.Fatalf("admission phases = %d, want queue, create, and start", admissions)
	}
}

func TestVerifySuccessfulExitWithStalledOutputSettlesAfterCleanup(t *testing.T) {
	f := newCandidateLifecycleFixture(t, nil, false)
	candidate := f.prepare(t, "verify-stalled-output", f.source(t, "verify-stalled-output", "source.txt", "source\n", "packet-stalled-output", nil))
	rt := &admissionLifecycleRuntime{gate: make(chan struct{}, 1), waitDone: make(chan struct{}), stalled: true}
	rt.gate <- struct{}{}
	admission := func(_ context.Context, _ Admission) (func(), error) {
		<-rt.gate
		return func() { rt.gate <- struct{}{} }, nil
	}
	svc := newAdmissionLifecycleService(t, f, rt, admission)
	if _, err := svc.Verify(f.ctx, Actor{MemberID: f.owner.ID}, protocol.IntegrationVerifyParams{
		WorkspaceID: string(f.workspace.ID), CandidateID: candidate.CandidateID,
		CandidateRevision: candidate.CandidateRevision, Argv: []string{"true"},
		TimeoutSeconds: 30, IdempotencyKey: "verify-stalled-output",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("verification worker did not observe successful runtime exit")
	}
	deadline := time.After(20 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, err := f.db.GetIntegrationCandidate(f.ctx, candidate.CandidateID)
		if err != nil {
			t.Fatal(err)
		}
		var persisted protocol.Candidate
		if err := json.Unmarshal(row.Payload, &persisted); err != nil {
			t.Fatal(err)
		}
		if len(persisted.Verifications) == 1 && persisted.Verifications[0].Status != protocol.VerificationRunning {
			if persisted.Verifications[0].Status != protocol.VerificationError || persisted.Verifications[0].Output == "" {
				t.Fatalf("stalled-output verification = %+v, want cleanup error with captured output", persisted.Verifications[0])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("stalled-output verification remained running after cleanup deadline")
		case <-ticker.C:
		}
	}
}
