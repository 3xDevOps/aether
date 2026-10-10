package scheduler

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/secretstore"
)

func TestProvisioningFailureReasonRedactsSetupDiagnostics(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	secret := "workspace-secret-9f4d"
	e.ws.Environment.Variables["AETHER_SECRET"] = secret
	e.ws.Environment.SetupPolicy.Script = "set -x; echo $AETHER_SECRET"
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	e.rt.startErr = errors.New("runtime: setup script exited 17: + echo " + secret + "\n/srv/aether/run-secret")

	_, launchErr := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
	if launchErr == nil {
		t.Fatal("Launch succeeded despite setup failure")
	}
	if strings.Contains(launchErr.Error(), secret) {
		t.Fatalf("Launch error leaked configured environment value: %q", launchErr)
	}

	prov := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	failed := waitStatusEvent(t, sub, prov.RunID, domain.RunFailed)
	reason := failed.Payload.(events.RunStatusPayload).Reason
	if !strings.Contains(reason, "setup script") {
		t.Fatalf("failure reason lost setup classification: %q", reason)
	}
	for _, leaked := range []string{secret, "set -x", "/srv/aether/run-secret"} {
		if strings.Contains(reason, leaked) {
			t.Fatalf("failure reason leaked %q: %q", leaked, reason)
		}
	}
	if len([]rune(reason)) > maxPublicRunStatusReason {
		t.Fatalf("failure reason length = %d, want <= %d", len([]rune(reason)), maxPublicRunStatusReason)
	}
}

func TestSetupFailureOutputReachesOnlyTheLauncher(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	sub := e.subscribe(t)
	const output = "npm error code E401\n+ curl -H ***\n"
	e.rt.startErr = &runtime.SetupError{ExitCode: 17, Output: output}

	_, launchErr := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "task", "fake", domain.LaunchTUI)
	var setup *SetupFailure
	if !errors.As(launchErr, &setup) || setup.Output != output {
		t.Fatalf("Launch error = %#v, want a SetupFailure carrying the script's output", launchErr)
	}
	const reason = "provisioning: start container: runtime: setup script exited 17"
	if launchErr.Error() != reason {
		t.Fatalf("Launch error message = %q, want %q", launchErr, reason)
	}
	prov := waitStatusEvent(t, sub, "", domain.RunProvisioning)
	failed := waitStatusEvent(t, sub, prov.RunID, domain.RunFailed)
	if got := failed.Payload.(events.RunStatusPayload).Reason; got != reason {
		t.Fatalf("published reason = %q, want %q", got, reason)
	}
	if row := e.waitStoreStatus(t, prov.RunID, domain.RunFailed); row.Reason != reason {
		t.Fatalf("stored reason = %q, want %q", row.Reason, reason)
	}
}

func TestWorkspaceSecretsJoinTheRunEnvironment(t *testing.T) {
	t.Parallel()
	secrets := secretstore.New(t.TempDir())
	e := newTestEnv(t, func(cfg *Config) { cfg.Secrets = secrets })
	e.ws.Environment.Variables["API_URL"] = "https://api.example.test"
	e.ws.Environment.Variables["SHADOWED"] = "plain"
	if err := e.db.UpdateWorkspace(t.Context(), e.ws); err != nil {
		t.Fatalf("UpdateWorkspace: %v", err)
	}
	if err := secrets.Put(e.ws.ID, map[string]string{"NPM_TOKEN": "token-value", "SHADOWED": "secret"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, c := e.launchFake(t, "task")
	for name, want := range map[string]string{"API_URL": "https://api.example.test", "NPM_TOKEN": "token-value", "SHADOWED": "secret"} {
		if got := c.spec.Env[name]; got != want {
			t.Errorf("container %s = %q, want %q", name, got, want)
		}
	}
	if want := []string{"NPM_TOKEN", "SHADOWED"}; !slices.Equal(c.spec.SecretEnv, want) {
		t.Errorf("SecretEnv = %v, want %v", c.spec.SecretEnv, want)
	}
}

func TestPublicProvisioningReasonPreservesImageFailureClassification(t *testing.T) {
	t.Parallel()
	got := publicRunStatusReason("provisioning: create container: no such image")
	if !strings.Contains(got, "create container") || !strings.Contains(got, "no such image") {
		t.Fatalf("reason = %q, want non-sensitive image classification", got)
	}
}

func TestCleanupDiagnosticIsBoundedDurableAndSafeAfterRecovery(t *testing.T) {
	s := &Scheduler{cfg: Config{StateDir: t.TempDir()}, runs: make(map[domain.RunID]*supervised)}
	run := &domain.Run{ID: "cleanup-diagnostic", Status: domain.RunCompleted}
	deadline := time.Now().UTC().Add(time.Hour)
	entry := &supervised{
		runID: run.ID, containerID: "owned", status: run.Status,
		retained: true, retainedUntil: &deadline, destroyPending: true,
	}
	s.runs[run.ID] = entry
	raw := strings.Repeat("/srv/private/token=credential\n", 1000)
	s.recordCleanupError(entry, raw)
	data, err := os.ReadFile(s.sidecarPath(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/srv/private") || strings.Contains(string(data), "credential") || len(data) > 1024 {
		t.Fatal("cleanup sidecar persisted unbounded/private runtime diagnostics")
	}
	sc, err := s.readSidecar(run.ID)
	if err != nil || sc.CleanupError != cleanupUnknownError {
		t.Fatalf("durable diagnostic = %+v, %v", sc, err)
	}
	recovered := s.entryFromSidecar(run, sc)
	s.runs[run.ID] = recovered
	info, err := s.Retention(run)
	if err != nil || !info.CleanupPending || info.CleanupError != cleanupUnknownError {
		t.Fatalf("recovered public diagnostic = %+v, %v", info, err)
	}
	s.recordCleanupError(recovered, cleanupRuntimeError)
	sc, err = s.readSidecar(run.ID)
	if err != nil || sc.CleanupError != cleanupRuntimeError {
		t.Fatalf("safe operation classification was not durable: %+v, %v", sc, err)
	}
}

func TestCleanupRefreshPreservesOutcomeAndFollowsDurableMarker(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := t.Context()
	run := &domain.Run{
		WorkspaceID: e.ws.ID, MemberID: e.member.ID, Harness: "fake",
		Mode: domain.LaunchTUI, Status: domain.RunRunning, Task: "cleanup event",
	}
	if err := e.db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	const reason = "agent reported success; retained container"
	if err := e.db.FinishRunReported(ctx, run.ID, domain.RunCompleted, reason, nil, &finished); err != nil {
		t.Fatal(err)
	}
	deadline := finished.Add(time.Hour)
	entry := &supervised{
		runID: run.ID, workspaceID: e.ws.ID, status: domain.RunCompleted,
		containerID: "owned", retained: true, retainedUntil: &deadline,
		evidencePending: true,
	}
	e.sched.mu.Lock()
	e.sched.runs[run.ID] = entry
	e.sched.mu.Unlock()
	defer func() {
		e.sched.mu.Lock()
		delete(e.sched.runs, run.ID)
		e.sched.mu.Unlock()
	}()
	sub := e.subscribe(t)
	for _, cause := range []string{cleanupEvidenceError, ""} {
		e.sched.recordCleanupError(entry, cause)
		var event events.Event
		select {
		case event = <-sub.Events():
		case <-time.After(time.Second):
			t.Fatal("missing retention event")
		}
		payload, ok := event.Payload.(events.RunRetentionPayload)
		if !ok || event.RunID != run.ID || payload.CleanupError != cause ||
			!payload.CleanupPending || payload.ContainerRetainedUntil != deadline.Format(time.RFC3339Nano) {
			t.Fatalf("cleanup event = %+v", event)
		}
		sc, err := e.sched.readSidecar(run.ID)
		if err != nil || sc.CleanupError != cause {
			t.Fatalf("cleanup event preceded its durable diagnostic: %+v, %v", sc, err)
		}
		row, err := e.db.GetRun(ctx, run.ID)
		if err != nil || row.Status != domain.RunCompleted || row.Reason != reason ||
			!row.OutcomeUnseen || row.FinishedAt == nil || !row.FinishedAt.Equal(finished) {
			t.Fatalf("cleanup refresh mutated durable outcome: %+v, %v", row, err)
		}
		e.sched.recordCleanupError(entry, cause)
		e.sched.publishRetention(run.ID)
		select {
		case duplicate := <-sub.Events():
			t.Fatalf("unchanged cleanup published an event: %+v", duplicate)
		default:
		}
	}
	for _, next := range []struct {
		deadline *time.Time
		want     events.RunRetentionPayload
	}{
		{&finished, events.RunRetentionPayload{ContainerRetainedUntil: finished.Format(time.RFC3339Nano), CleanupPending: true}},
		{nil, events.RunRetentionPayload{}},
	} {
		e.sched.mu.Lock()
		entry.retainedUntil = next.deadline
		if next.deadline == nil {
			entry.containerID = ""
			entry.evidencePending = false
			entry.retained = false
		}
		e.sched.publishRetentionLocked(run.ID)
		e.sched.mu.Unlock()
		select {
		case event := <-sub.Events():
			if payload, ok := event.Payload.(events.RunRetentionPayload); !ok || payload != next.want {
				t.Fatalf("retention change = %+v, want %+v", event, next.want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing changed retention metadata")
		}
		e.sched.publishRetention(run.ID)
		select {
		case duplicate := <-sub.Events():
			t.Fatalf("unchanged metadata published an event: %+v", duplicate)
		default:
		}
	}
}
