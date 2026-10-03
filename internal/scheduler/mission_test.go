package scheduler

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

func launchRetentionWorker(t *testing.T, e *testEnv, mode domain.LaunchMode) (*domain.Run, *fakeContainer) {
	t.Helper()
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: "retention-worker", Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: "retained worker",
		Harness: "fake", Mode: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	container := e.rt.byName(string(run.ID))
	if container == nil {
		t.Fatal("worker container not created")
	}
	return run, container
}

func TestMissionCompletionRetainsExactContainerAcrossRestartAndExpiry(t *testing.T) {
	for _, mode := range []domain.LaunchMode{domain.LaunchTUI, domain.LaunchHeadless} {
		for _, outcome := range []domain.RunStatus{domain.RunCompleted, domain.RunFailed} {
			t.Run(string(mode)+"/"+string(outcome), func(t *testing.T) {
				e := newTestEnv(t, nil)
				run, container := launchRetentionWorker(t, e, mode)
				before := time.Now().UTC()
				if err := e.sched.CompleteMission(t.Context(), run.ID, outcome); err != nil {
					t.Fatal(err)
				}
				row := e.waitStoreStatus(t, run.ID, outcome)
				if row.Worktree != run.Worktree || container.currentState() != "paused" {
					t.Fatalf("completion changed checkout or kept executing: %+v, %s", row, container.currentState())
				}
				sc, err := e.sched.readSidecar(run.ID)
				if err != nil || sc.RetainedUntil == nil || sc.RetainedUntil.Before(before.Add(DefaultRunContainerTTL)) {
					t.Fatalf("default retention deadline: %+v, %v", sc, err)
				}
				if err = e.sched.CompleteMission(t.Context(), run.ID, outcome); err != nil {
					t.Fatal(err)
				}
				retry, err := e.sched.readSidecar(run.ID)
				if err != nil || retry.RetainedUntil == nil || !retry.RetainedUntil.Equal(*sc.RetainedUntil) {
					t.Fatalf("replay extended retention: %+v, %v", retry, err)
				}
				if err = e.sched.Close(); err != nil {
					t.Fatal(err)
				}
				s2 := e.newScheduler(t, e.rt, newFakePTY())
				if err = s2.recoverRuns(t.Context()); err != nil {
					t.Fatal(err)
				}
				obs, err := s2.ObserveMissionRun(t.Context(), run.ID)
				if err != nil || obs.State != MissionRunRetained || !obs.RetentionSettled {
					t.Fatalf("recovered completion not settled: %+v, %v", obs, err)
				}
				if e.rt.byName(string(run.ID)) != container || !s2.RetainsContainer(t.Context(), run.ID) {
					t.Fatal("recovery replaced container or lost coordination ownership")
				}
				if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("completed worker relaunched: %v", err)
				}
				// Even human relabeling must not revive an already-settled worker.
				if err := s2.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
					t.Fatal(err)
				}
				if _, err := s2.Relaunch(t.Context(), run.ID, e.member.ID); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("relabelled worker relaunched: %v", err)
				}
				s2.mu.Lock()
				past := time.Now().UTC().Add(-time.Second)
				s2.runs[run.ID].retainedUntil = &past
				s2.mu.Unlock()
				s2.sweepRetained(t.Context())
				if e.rt.byName(string(run.ID)) != nil || s2.RetainsContainer(t.Context(), run.ID) {
					t.Fatal("expiry kept runtime or coordination ownership")
				}
			})
		}
	}
}

func TestMissionNaturalHeadlessExitRetainsStoppedContainer(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			e := newTestEnv(t, nil)
			run, container := launchRetentionWorker(t, e, domain.LaunchHeadless)
			container.exitNow(code)
			want := domain.RunCompleted
			if code != 0 {
				want = domain.RunFailed
			}
			e.waitStoreStatus(t, run.ID, want)
			waitFor(t, "settled retained exit", func() bool {
				obs, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
				return err == nil && obs.State == MissionRunRetained && obs.RetentionSettled
			})
			if e.rt.byName(string(run.ID)) != container || container.currentState() != "stopped" {
				t.Fatal("natural exit destroyed or restarted worker")
			}
			if err := e.sched.Close(); err != nil {
				t.Fatal(err)
			}
			s2 := e.newScheduler(t, e.rt, newFakePTY())
			if err := s2.recoverRuns(t.Context()); err != nil {
				t.Fatal(err)
			}
			if e.rt.byName(string(run.ID)) != container {
				t.Fatal("restart destroyed exited retained worker")
			}
			var cleanupErr error
			if code == 0 {
				cleanupErr = s2.Kill(t.Context(), run.ID, e.member.ID)
			} else {
				cleanupErr = s2.DeleteRun(t.Context(), run.ID, e.member.ID)
			}
			if cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			if e.rt.byName(string(run.ID)) != nil {
				t.Fatal("explicit Kill/Delete retained completed worker")
			}
		})
	}
}

func TestMissionRetentionFailureDoesNotSettleBeforeEvidence(t *testing.T) {
	e := newTestEnv(t, nil)
	capture := newSchedulerEvidenceCapture(e.ws.ID)
	e.sched.UseEvidence(capture)
	run, container := launchRetentionWorker(t, e, domain.LaunchHeadless)
	capture.failures = 1
	if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunFailed); err == nil {
		t.Fatal("completion accepted failed capture")
	}
	obs, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || obs.RetentionSettled || container.currentState() != "paused" {
		t.Fatalf("failed capture settled or left execution active: %+v, %v", obs, err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || !sc.EvidencePending {
		t.Fatalf("missing durable evidence retry: %+v, %v", sc, err)
	}
	e.sched.sweepRetained(t.Context())
	obs, err = e.sched.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || !obs.RetentionSettled || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("evidence retry failed to retain settled owner: %+v, %v", obs, err)
	}
}

type retentionPauseFailure struct{ runtime.Runtime }

func (retentionPauseFailure) Pause(context.Context, runtime.ID) error {
	return errors.New("runtime pause unavailable")
}

func TestMissionRetentionStopAndPersistenceFailuresHoldCapacity(t *testing.T) {
	for _, failure := range []string{"pause", "sidecar", "row"} {
		t.Run(failure, func(t *testing.T) {
			e := newTestEnv(t, nil)
			run, container := launchRetentionWorker(t, e, domain.LaunchHeadless)
			stateDir := e.sched.cfg.StateDir
			switch failure {
			case "pause":
				e.sched.cfg.Runtime = retentionPauseFailure{e.rt}
			case "sidecar":
				e.sched.cfg.StateDir = t.TempDir()
				if err := os.Mkdir(e.sched.sidecarPath(run.ID), 0o700); err != nil {
					t.Fatal(err)
				}
			case "row":
				e.sched.cfg.Store = &failingRunStatusStore{Store: e.db, fail: true}
			}
			if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted); err == nil {
				t.Fatal("failed retention accepted")
			}
			obs, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
			if err != nil || obs.RetentionSettled || e.rt.byName(string(run.ID)) != container {
				t.Fatalf("failed retention released ownership: %+v, %v", obs, err)
			}
			e.sched.cfg.Runtime, e.sched.cfg.Store, e.sched.cfg.StateDir = e.rt, e.db, stateDir
			if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted); err != nil {
				t.Fatalf("recovery after %s failure: %v", failure, err)
			}
		})
	}
}

func TestMissionNegativeTTLAndCancellationRemainDestructive(t *testing.T) {
	for _, operation := range []string{"complete", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			e := newTestEnv(t, func(cfg *Config) {
				if operation == "complete" {
					cfg.RunContainerTTL = -time.Second
				}
			})
			run, _ := launchRetentionWorker(t, e, domain.LaunchTUI)
			var err error
			if operation == "complete" {
				err = e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted)
			} else {
				err = e.sched.CancelMission(t.Context(), run.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "destroyed worker", func() bool { return e.rt.byName(string(run.ID)) == nil })
			if operation == "cancel" {
				waitFor(t, "settled cancellation", func() bool {
					obs, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
					return err == nil && obs.State == MissionRunStopped && obs.RetentionSettled
				})
				if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted); err != nil {
					t.Fatal(err)
				}
				e.waitStoreStatus(t, run.ID, domain.RunAbandoned)
			}
		})
	}
}

func TestMissionNegativeTTLFailedDestroyHoldsCapacityUntilCleanup(t *testing.T) {
	e := newTestEnv(t, func(cfg *Config) { cfg.RunContainerTTL = -time.Second })
	run, container := launchRetentionWorker(t, e, domain.LaunchHeadless)
	failure := &destroyFailureRuntime{Runtime: e.rt, destroyErr: errors.New("destroy unavailable")}
	e.sched.cfg.Runtime = failure
	if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted); err == nil {
		t.Fatal("completion accepted uncertain destruction")
	}
	obs, err := e.sched.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || obs.RetentionSettled || obs.State != MissionRunDestroyPending || e.rt.byName(string(run.ID)) != container {
		t.Fatalf("uncertain destruction released ownership: %+v, %v", obs, err)
	}
	e.sched.cfg.Runtime = e.rt
	e.sched.sweepRetained(t.Context())
	obs, err = e.sched.ObserveMissionRun(t.Context(), run.ID)
	if err != nil || obs.State != MissionRunStopped || !obs.RetentionSettled || e.rt.byName(string(run.ID)) != nil {
		t.Fatalf("cleanup did not settle: %+v, %v", obs, err)
	}
}

// TestIntegratorReportArmsTheReportedFinish: a mission integrator's terminal
// report finishes its run like an ordinary run's does.
func TestIntegratorReportArmsTheReportedFinish(t *testing.T) {
	e := newTestEnv(t, nil)
	m := &domain.Mission{
		WorkspaceID: e.ws.ID, Objective: "integrator finish", AccountableHumanID: e.member.ID,
		Integrator:            domain.MissionIntegrator{AccountMemberID: e.member.ID, Harness: "fake", Mode: domain.LaunchTUI},
		MaxConcurrentAttempts: 1, MaxTotalAttempts: 1, IdempotencyKey: "integrator-finish",
	}
	if err := e.db.CreateMission(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	run, err := e.sched.LaunchMission(t.Context(), MissionLaunchSpec{
		WorkspaceID: e.ws.ID, RunID: m.CurrentIntegratorRunID, Actor: e.member.ID,
		RunOwner: e.member.ID, AccountOwner: e.member.ID, Task: m.Objective,
		Harness: "fake", Mode: domain.LaunchTUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.sched.FinishReported(t.Context(), run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	armed := e.sched.runs[run.ID].reported
	e.sched.mu.Unlock()
	if armed != domain.RunCompleted {
		t.Fatalf("integrator armed for %q, want a completed reported finish", armed)
	}
}

// TestWorkerReportLeavesCompletionToTheMission: a worker's terminal report
// never arms the ordinary reported finish, so CompleteMission alone finishes
// it, with the mission reason, and leaves no unseen outcome for the owner.
func TestWorkerReportLeavesCompletionToTheMission(t *testing.T) {
	e := newTestEnv(t, nil)
	run, _ := launchRetentionWorker(t, e, domain.LaunchTUI)
	if err := e.sched.FinishReported(t.Context(), run.ID, "report-1", domain.RunCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.sched.mu.Lock()
	armed := e.sched.runs[run.ID].reported
	e.sched.mu.Unlock()
	if armed != "" {
		t.Fatalf("worker armed for a reported finish: %q", armed)
	}
	if err := e.sched.CompleteMission(t.Context(), run.ID, domain.RunCompleted); err != nil {
		t.Fatal(err)
	}
	row := e.waitStoreStatus(t, run.ID, domain.RunCompleted)
	if row.Reason != retainedCompletionReason || row.OutcomeUnseen {
		t.Fatalf("completed worker = %q, unseen %v; want %q and seen", row.Reason, row.OutcomeUnseen, retainedCompletionReason)
	}
}
