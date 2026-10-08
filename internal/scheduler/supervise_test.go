package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
)

func TestFailedExitReasonCarriesTheAgentsLastLine(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "migrate", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	c := e.rt.byName(string(run.ID))
	c.output("applying 0042_add_invoice_index\r\n")
	c.output(`migration 0042_add_invoice_index failed: relation "invoices" does not exist` + "\r\n\r\n")
	c.output("Error: " + strings.Repeat("x", 300) + "\r\n")
	c.exitNow(1)

	row := e.waitStoreStatus(t, run.ID, domain.RunFailed)
	want := "agent exited 1: Error: " + strings.Repeat("x", 190) + "..."
	if row.Reason != want {
		t.Fatalf("reason = %q, want %q", row.Reason, want)
	}
}

func TestFailedExitReasonWithoutOutput(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	run, err := e.sched.Launch(t.Context(), e.ws.ID, e.member.ID, e.member.ID, "silent", "fake", domain.LaunchHeadless)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	e.rt.byName(string(run.ID)).exitNow(3)

	if row := e.waitStoreStatus(t, run.ID, domain.RunFailed); row.Reason != "agent exited 3" {
		t.Fatalf("reason = %q, want agent exited 3", row.Reason)
	}
}

func TestRetainedCleanupFailureSurvivesRecoveryAndClearsAfterSettlement(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	capture.failures = 1
	e.sched.UseEvidence(capture)
	run, container := e.launchFake(t, "durable evidence failure")
	if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err == nil {
		t.Fatal("close accepted failed required evidence")
	}
	row, err := e.db.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.sched.Retention(row)
	if err != nil || !info.CleanupPending || info.CleanupError != cleanupEvidenceError || info.RetainedUntil == nil {
		t.Fatalf("initial cleanup failure = %+v, %v", info, err)
	}
	deadline := *info.RetainedUntil
	if err = e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	capture.failures = 1
	capture.mu.Unlock()
	recovered := e.newScheduler(t, e.rt, newFakePTY())
	recovered.UseEvidence(capture)
	if err = recovered.recoverRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	sc, err := recovered.readSidecar(run.ID)
	info, retentionErr := recovered.Retention(row)
	if err != nil || retentionErr != nil || !info.CleanupPending ||
		info.CleanupError != cleanupEvidenceError || sc.CleanupError != info.CleanupError ||
		e.rt.byName(string(run.ID)) != container {
		t.Fatalf("recovered cleanup failure = %+v, sidecar %+v, %v, %v", info, sc, err, retentionErr)
	}
	recovered.sweepRetained(t.Context())
	info, err = recovered.Retention(row)
	if err != nil || info.CleanupPending || info.CleanupError != "" ||
		info.RetainedUntil == nil || info.RetainedUntil.After(deadline) ||
		e.rt.byName(string(run.ID)) != container || container.currentState() != "paused" {
		t.Fatalf("successful settlement did not clear failure while retaining exact compute: %+v, %v", info, err)
	}
	sc, err = recovered.readSidecar(run.ID)
	if err != nil || sc.EvidencePending || sc.CleanupError != "" {
		t.Fatalf("successful settlement left stale durable diagnostic: %+v, %v", sc, err)
	}
}
