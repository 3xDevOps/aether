package scheduler

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/store"
)

func inputUpdate(operation string, request domain.RunInputRequest) domain.RunInputUpdate {
	return domain.RunInputUpdate{Operation: operation, SessionID: request.SessionID, Kind: request.Kind, ID: request.ID}
}

func waitInputEvent(t *testing.T, sub events.Subscription, run domain.RunID, want []domain.RunInputRequest) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case event, ok := <-sub.Events():
			if !ok {
				t.Fatal("event subscription closed before input update")
			}
			if payload, ok := event.Payload.(events.RunInputPayload); ok && event.RunID == run {
				if payload.PendingInputs == nil || !slices.Equal(payload.PendingInputs, want) {
					t.Fatalf("input event = %+v, want explicit list %+v", payload.PendingInputs, want)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for input event")
		}
	}
}

func expectNoInputEvent(t *testing.T, sub events.Subscription, run domain.RunID) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case event, ok := <-sub.Events():
			if !ok {
				return
			}
			if event.RunID == run && event.Type == events.TypeRunInput {
				t.Fatalf("unchanged inputs published %+v", event.Payload)
			}
		case <-deadline:
			return
		}
	}
}

func assertPendingInputs(t *testing.T, sched *Scheduler, run domain.RunID, want []domain.RunInputRequest) {
	t.Helper()
	if got := sched.PendingInputs(run); got == nil || !slices.Equal(got, want) {
		t.Fatalf("pending inputs = %+v, want explicit list %+v", got, want)
	}
}

func TestInputRequestsPreserveExecutionAndCorrelateScopes(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	sub := e.subscribe(t)
	question := domain.RunInputRequest{SessionID: "a", Kind: "question", ID: "shared"}
	permission := domain.RunInputRequest{SessionID: "a", Kind: "permission", ID: "shared"}
	otherSession := domain.RunInputRequest{SessionID: "b", Kind: "question", ID: "shared"}
	want := []domain.RunInputRequest{permission, question, otherSession}
	report := agentstatus.Report{State: agentstatus.Working, InputUpdates: []domain.RunInputUpdate{
		inputUpdate("open", question), inputUpdate("open", permission), inputUpdate("open", otherSession),
	}}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, report); err != nil {
		t.Fatal(err)
	}
	waitInputEvent(t, sub, run.ID, want)
	assertPendingInputs(t, e.sched, run.ID, want)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)

	// Duplicate opens and reordered authoritative snapshots are the same set.
	if err := e.sched.ReportAgentState(t.Context(), run.ID, report); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{
		{Operation: "replace", Requests: []domain.RunInputRequest{otherSession, question, permission, question}},
	}}); err != nil {
		t.Fatal(err)
	}
	expectNoInputEvent(t, sub, run.ID)

	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("close", question)}}); err != nil {
		t.Fatal(err)
	}
	want = []domain.RunInputRequest{permission, otherSession}
	waitInputEvent(t, sub, run.ID, want)
	assertPendingInputs(t, e.sched, run.ID, want)
	e.waitStoreStatus(t, run.ID, domain.RunRunning)

	// Idle is still independent of unresolved input, and a close is not work.
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle}); err != nil {
		t.Fatal(err)
	}
	waitStatusEvent(t, sub, run.ID, domain.RunNeedsAttention)
	assertPendingInputs(t, e.sched, run.ID, want)
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{Operation: "clear", SessionID: "a"}}}); err != nil {
		t.Fatal(err)
	}
	waitInputEvent(t, sub, run.ID, []domain.RunInputRequest{otherSession})
	assertPendingInputs(t, e.sched, run.ID, []domain.RunInputRequest{otherSession})
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: []domain.RunInputRequest{}}}}); err != nil {
		t.Fatal(err)
	}
	waitInputEvent(t, sub, run.ID, nil)
	assertPendingInputs(t, e.sched, run.ID, nil)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
}

func TestInputReportPersistenceFailureDoesNotAnnounceState(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	request := domain.RunInputRequest{SessionID: "session", Kind: "form", ID: "form"}
	open := agentstatus.Report{State: agentstatus.Working, InputUpdates: []domain.RunInputUpdate{inputUpdate("open", request)}}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, open); err != nil {
		t.Fatal(err)
	}
	sub := e.subscribe(t)
	path := e.sched.sidecarPath(run.ID)
	backup := path + ".saved"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(path)
		_ = os.Rename(backup, path)
	}()
	// Identical reports do not depend on another disk write.
	if err := e.sched.ReportAgentState(t.Context(), run.ID, open); err != nil {
		t.Fatalf("duplicate input report wrote sidecar: %v", err)
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle,
		InputUpdates: []domain.RunInputUpdate{inputUpdate("close", request)},
	}); err == nil {
		t.Fatal("failed sidecar replacement claimed success")
	}
	assertPendingInputs(t, e.sched, run.ID, []domain.RunInputRequest{request})
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
	expectNoInputEvent(t, sub, run.ID)
}

func TestInputReportStoreFailureRestoresDurableSet(t *testing.T) {
	t.Parallel()
	var failing *failingRunStatusStore
	e := newReportingEnv(t, func(cfg *Config) {
		failing = &failingRunStatusStore{Store: cfg.Store}
		cfg.Store = failing
	})
	run, _ := e.launchReporting(t)
	request := domain.RunInputRequest{SessionID: "session", Kind: "question", ID: "question"}
	failing.fail = true
	defer func() { failing.fail = false }()
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle,
		InputUpdates: []domain.RunInputUpdate{inputUpdate("open", request)},
	}); err == nil {
		t.Fatal("failed status transaction claimed success")
	}
	assertPendingInputs(t, e.sched, run.ID, nil)
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.PendingInputs) != 0 || sc.AgentState == agentstatus.Idle {
		t.Fatalf("refused report survived on disk: %+v", sc)
	}
	e.waitStoreStatus(t, run.ID, domain.RunRunning)
}

func TestInputReportReturnsDurableEventFailure(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	if err := e.bus.Close(); err != nil {
		t.Fatal(err)
	}
	request := domain.RunInputRequest{SessionID: "session", Kind: "extension_ui", ID: "dialog"}
	err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("open", request)}})
	if !errors.Is(err, events.ErrBusClosed) {
		t.Fatalf("event persistence failure = %v, want ErrBusClosed", err)
	}
	// The preceding sidecar commit is real even when event delivery failed.
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil || !slices.Equal(sc.PendingInputs, []domain.RunInputRequest{request}) {
		t.Fatalf("durable pending set = %+v, error %v", sc.PendingInputs, err)
	}
}

func TestInputRequestAggregateBoundIsAtomic(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	requests := make([]domain.RunInputRequest, domain.MaxRunInputRequests)
	for i := range requests {
		requests[i] = domain.RunInputRequest{SessionID: "session", Kind: "question", ID: fmt.Sprintf("question-%03d", i)}
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{{Operation: "replace", Requests: requests}}}); err != nil {
		t.Fatal(err)
	}
	extra := domain.RunInputRequest{SessionID: "other-session", Kind: "permission", ID: "extra"}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("open", extra)}}); err == nil {
		t.Fatal("unbounded aggregate accepted")
	}
	assertPendingInputs(t, e.sched, run.ID, requests)
}

func TestInputRecoveryKeepsOnlyCurrentPendingRequests(t *testing.T) {
	t.Parallel()
	e := newReportingEnv(t, nil)
	run, _ := e.launchReporting(t)
	closed := domain.RunInputRequest{SessionID: "a", Kind: "question", ID: "closed"}
	pending := domain.RunInputRequest{SessionID: "b", Kind: "permission", ID: "pending"}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle,
		InputUpdates: []domain.RunInputUpdate{inputUpdate("open", closed), inputUpdate("open", pending)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("close", closed)}}); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Close(); err != nil {
		t.Fatal(err)
	}
	pty2 := newFakePTY()
	s2 := e.newScheduler(t, e.rt, pty2)
	if err := s2.recoverRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPendingInputs(t, s2, run.ID, []domain.RunInputRequest{pending})
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
	if err := s2.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("close", pending)}}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	s3 := e.newScheduler(t, e.rt, newFakePTY())
	if err := s3.recoverRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPendingInputs(t, s3, run.ID, nil)
	e.waitStoreStatus(t, run.ID, domain.RunNeedsAttention)
}

func TestInputRequestsCannotSurviveEndedLifetime(t *testing.T) {
	t.Parallel()
	for _, end := range []string{"completed", "deleted", "relaunched"} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			e := newReportingEnv(t, func(cfg *Config) { cfg.RunContainerTTL = time.Hour })
			run, container := e.launchReporting(t)
			request := domain.RunInputRequest{SessionID: "session", Kind: "question", ID: "stale"}
			if err := e.sched.ReportAgentState(t.Context(), run.ID, agentstatus.Report{InputUpdates: []domain.RunInputUpdate{inputUpdate("open", request)}}); err != nil {
				t.Fatal(err)
			}
			old, err := e.sched.readSidecar(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			sub := e.subscribe(t)
			switch end {
			case "completed":
				container.exitNow(0)
				waitInputEvent(t, sub, run.ID, nil)
				e.waitStoreStatus(t, run.ID, domain.RunCompleted)
			case "deleted":
				if err := e.sched.DeleteRun(t.Context(), run.ID, e.member.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := e.db.GetRun(t.Context(), run.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("deleted row = %v", err)
				}
			case "relaunched":
				if err := e.sched.CloseRun(t.Context(), run.ID, e.member.ID, domain.RunMerged); err != nil {
					t.Fatal(err)
				}
				waitInputEvent(t, sub, run.ID, nil)
				assertPendingInputs(t, e.sched, run.ID, nil)
				if _, err := e.sched.Relaunch(t.Context(), run.ID, e.member.ID); err != nil {
					t.Fatal(err)
				}
				// Simulate a crash after row promotion but before the sidecar
				// cleared old input. The run's StartedAt generation must win.
				current, err := e.sched.readSidecar(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				current.PendingInputs, current.InputStartedAt = old.PendingInputs, old.InputStartedAt
				if err := e.sched.writeSidecar(current); err != nil {
					t.Fatal(err)
				}
			}
			assertPendingInputs(t, e.sched, run.ID, nil)
			if err := e.sched.Close(); err != nil {
				t.Fatal(err)
			}
			s2 := e.newScheduler(t, e.rt, newFakePTY())
			if err := s2.recoverRuns(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertPendingInputs(t, s2, run.ID, nil)
		})
	}
}
