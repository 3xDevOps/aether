package mission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type recordingCanceller struct {
	runs []domain.RunID
	err  error
}

func (c *recordingCanceller) CancelMission(_ context.Context, run domain.RunID) error {
	c.runs = append(c.runs, run)
	return c.err
}

type recordingBus struct {
	mu     sync.Mutex
	calls  int
	err    error
	events []events.Event
}

func (b *recordingBus) Publish(_ context.Context, event events.Event) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return events.Event{}, b.err
	}
	b.events = append(b.events, event)
	return events.Event{}, nil
}

// waitForTimelineNote waits for the stamp a delivered notice leaves on the
// integrator run's timeline; it lands after the terminal write returns.
func (b *recordingBus) waitForTimelineNote(t *testing.T, run domain.RunID, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		for _, event := range b.events {
			note, ok := event.Payload.(events.TimelinePayload)
			if ok && event.RunID == run && note.Kind == events.TimelineNote && note.Message == message {
				b.mu.Unlock()
				return
			}
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timeline never recorded %q for run %s", message, run)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (b *recordingBus) Subscribe(context.Context, events.SubscribeOptions) (events.Subscription, error) {
	return nil, errors.New("unused")
}

func (b *recordingBus) Close() error { return nil }

type reconcileReportFixture struct {
	db        *store.DB
	svc       *Service
	mission   *domain.Mission
	task      *domain.Task
	attempt   *domain.Attempt
	canceller *recordingCanceller
	bus       *recordingBus
	notices   *recordingInjector
	packet    protocol.EvidencePacket
}

func setupReconcileReport(t *testing.T, outcome store.CoordOutcome) (reconcileReportFixture, *store.CoordReport) {
	t.Helper()
	return setupReconcileReportFor(t, outcome, domain.LaunchHeadless)
}

// setupReconcileReportFor builds one dispatched worker attempt under an
// integrator run of the given mode, with a terminal that records notices.
func setupReconcileReportFor(t *testing.T, outcome store.CoordOutcome, integratorMode domain.LaunchMode) (reconcileReportFixture, *store.CoordReport) {
	t.Helper()
	ctx := context.Background()
	db := openMissionRegressionDB(t)
	workspace := regressionWorkspace(t, db)
	member := regressionMember(t, db, "integrator")
	mission := regressionMission(t, db, workspace.ID, member.ID)
	if err := db.CreateRunWithID(ctx, &domain.Run{
		ID: mission.CurrentIntegratorRunID, WorkspaceID: workspace.ID, MemberID: member.ID, Task: "integrator",
		Harness: "claude", Mode: integratorMode, Status: domain.RunQueued,
	}); err != nil {
		t.Fatalf("create integrator run: %v", err)
	}
	task := &domain.Task{
		MissionID: mission.ID,
		Revision: &domain.TaskRevision{
			Title: "report task", Objective: "report task", Status: domain.TaskRevisionProposed,
			EvidenceRequirements: []domain.EvidenceRequirement{{Kind: "test"}},
		},
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	mission = regressionApprovePlan(t, db, mission)
	attempt, _, err := db.ReserveAttempt(ctx, &domain.AttemptReservation{
		MissionID: mission.ID, TaskID: task.ID, TaskRevision: task.CurrentRevision,
		DispatchKey: "report-dispatch", Harness: "claude", Mode: domain.LaunchHeadless,
		ActorRunID: mission.CurrentIntegratorRunID, AuthorizingHumanID: member.ID, RunOwnerID: member.ID, AccountOwnerID: member.ID,
		AuthorityGeneration: mission.IntegratorGeneration, IntegratorGeneration: mission.IntegratorGeneration,
	})
	if err != nil {
		t.Fatalf("reserve attempt: %v", err)
	}
	regressionRun(t, db, attempt.RunID, workspace.ID, member.ID, "worker")
	clock := time.Now().UTC()
	expires := clock.Add(time.Hour).Format(time.RFC3339Nano)
	packet := protocol.EvidencePacket{
		ID: "packet-1", WorkspaceID: string(workspace.ID), RunID: string(attempt.RunID),
		Origin:           protocol.EvidenceOrigin{Kind: protocol.EvidenceOriginRun, ID: string(attempt.RunID)},
		Availability:     protocol.EvidenceAvailable,
		RetainedRevision: "revision-1",
		ExpiresAt:        &expires,
		Sources:          []protocol.EvidenceSourceFact{{Name: "test", Available: true}},
	}
	canceller := &recordingCanceller{}
	bus := &recordingBus{}
	notices := &recordingInjector{writes: make(chan injectedLine, 16)}
	svc, err := New(Config{
		Store: db, Missions: db, Evidence: &mutableEvidenceReader{packet: packet},
		Cancel: canceller, Bus: bus, AuthorizationMu: &sync.Mutex{}, PTY: notices,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("new mission service: %v", err)
	}
	report := &store.CoordReport{
		WorkspaceID:  workspace.ID,
		RunID:        attempt.RunID,
		Outcome:      outcome,
		Summary:      "worker " + string(outcome),
		EvidenceRefs: []string{packet.ID},
	}
	return reconcileReportFixture{
		db: db, svc: svc, mission: mission, task: task, attempt: attempt,
		canceller: canceller, bus: bus, notices: notices, packet: packet,
	}, report
}

// expectNotice requires exactly one aether: line carrying text to reach the
// integrator terminal and the matching stamp to reach the timeline.
func (f reconcileReportFixture) expectNotice(t *testing.T, step, text string) {
	t.Helper()
	f.notices.expect(t, step, f.mission.CurrentIntegratorRunID, text, harness.SubmitSequence("claude"))
	f.bus.waitForTimelineNote(t, f.mission.CurrentIntegratorRunID, "mission notice: "+text)
	f.notices.expectNone(t, step+" again")
}

func TestReconcileReportSuccessCreatesSubmission(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeSuccess)
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport success: %v", err)
	}
	submissions, err := fix.db.ListSubmissions(ctx, fix.mission.ID, fix.task.ID)
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(submissions) != 1 || submissions[0] == nil || submissions[0].AttemptID != fix.attempt.ID {
		t.Fatalf("submissions after success = %#v, want one for attempt %s", submissions, fix.attempt.ID)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != domain.AttemptSubmitted {
		t.Fatalf("attempt state after success = %q, want submitted", attempt.State)
	}
	if len(fix.canceller.runs) != 0 {
		t.Fatalf("CancelMission called for success report: %v", fix.canceller.runs)
	}
}

func TestReconcileReportBlockedKeepsWorkerRunning(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeBlocked)
	before, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil {
		t.Fatalf("get attempt before blocked report: %v", err)
	}
	if err = fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport blocked: %v", err)
	}
	submissions, err := fix.db.ListSubmissions(ctx, fix.mission.ID, fix.task.ID)
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(submissions) != 0 {
		t.Fatalf("submissions after blocked = %#v, want none", submissions)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State == domain.AttemptSubmitted || attempt.State == domain.AttemptFailed || attempt.State == domain.AttemptCompleted {
		t.Fatalf("attempt state after blocked = %q, want unchanged from %q", attempt.State, before.State)
	}
	if attempt.State != before.State {
		t.Fatalf("attempt state after blocked = %q, want %q", attempt.State, before.State)
	}
	if len(fix.canceller.runs) != 0 {
		t.Fatalf("CancelMission called for blocked report: %v", fix.canceller.runs)
	}
}

func TestReconcileReportFailureMarksAttemptAndCancels(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeFailure)
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport failure: %v", err)
	}
	submissions, err := fix.db.ListSubmissions(ctx, fix.mission.ID, fix.task.ID)
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(submissions) != 0 {
		t.Fatalf("submissions after failure = %#v, want none", submissions)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != domain.AttemptFailed {
		t.Fatalf("attempt state after failure = %q, want failed", attempt.State)
	}
	if len(fix.canceller.runs) != 1 || fix.canceller.runs[0] != fix.attempt.RunID {
		t.Fatalf("CancelMission calls = %v, want [%s]", fix.canceller.runs, fix.attempt.RunID)
	}
}

func TestReconcileReportFailureReturnsCancelError(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeFailure)
	fix.canceller.err = errors.New("scheduler unavailable")
	err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet)
	if err == nil || !errors.Is(err, fix.canceller.err) {
		t.Fatalf("ReconcileReport failure cancel error = %v, want wrapped scheduler error", err)
	}
	attempt, getErr := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if getErr != nil {
		t.Fatalf("get attempt: %v", getErr)
	}
	if attempt.State != domain.AttemptFailed {
		t.Fatalf("attempt state after cancel error = %q, want failed", attempt.State)
	}
	if len(fix.canceller.runs) != 1 || fix.canceller.runs[0] != fix.attempt.RunID {
		t.Fatalf("CancelMission calls = %v, want [%s]", fix.canceller.runs, fix.attempt.RunID)
	}
	if fix.bus.calls != 0 {
		t.Fatalf("publish calls = %d, want 0 after cancel error", fix.bus.calls)
	}
}

type settlingCanceller struct {
	db      *store.DB
	attempt *domain.Attempt
	runs    []domain.RunID
}

func (c *settlingCanceller) CancelMission(ctx context.Context, run domain.RunID) error {
	c.runs = append(c.runs, run)
	if c.db == nil || c.attempt == nil {
		return nil
	}
	current, err := c.db.GetAttempt(ctx, c.attempt.ID)
	if err != nil {
		return err
	}
	err = c.db.UpdateAttemptState(ctx, current.ID, current.RunID, current.AuthorityGeneration, current.IntegratorGeneration, domain.AttemptCancelled, "killed")
	if errors.Is(err, store.ErrMissionStale) {
		return nil
	}
	return err
}

func TestReconcileReportFailureCancelsBeforePublishError(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeFailure)
	fix.bus.err = errors.New("mission bus unavailable")
	err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet)
	if err == nil || !errors.Is(err, fix.bus.err) {
		t.Fatalf("ReconcileReport failure publish error = %v, want wrapped bus error", err)
	}
	attempt, getErr := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if getErr != nil {
		t.Fatalf("get attempt: %v", getErr)
	}
	if attempt.State != domain.AttemptFailed {
		t.Fatalf("attempt state after publish error = %q, want failed", attempt.State)
	}
	if len(fix.canceller.runs) != 1 || fix.canceller.runs[0] != fix.attempt.RunID {
		t.Fatalf("CancelMission calls = %v, want [%s]", fix.canceller.runs, fix.attempt.RunID)
	}
	if fix.bus.calls != 1 {
		t.Fatalf("publish calls = %d, want 1", fix.bus.calls)
	}

	retryErr := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet)
	if retryErr == nil || !errors.Is(retryErr, fix.bus.err) {
		t.Fatalf("retry publish error = %v, want wrapped bus error", retryErr)
	}
	if len(fix.canceller.runs) != 2 {
		t.Fatalf("CancelMission calls after retry = %v, want 2", fix.canceller.runs)
	}
	if fix.bus.calls != 2 {
		t.Fatalf("publish calls after retry = %d, want 2", fix.bus.calls)
	}

	fix.bus.err = nil
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("retry after bus recovery: %v", err)
	}
	if len(fix.canceller.runs) != 3 {
		t.Fatalf("CancelMission calls after recovery = %v, want 3", fix.canceller.runs)
	}
	if fix.bus.calls != 3 {
		t.Fatalf("publish calls after recovery = %d, want 3", fix.bus.calls)
	}
}

func TestReconcileReportFailureKeepsReportedOutcomeAcrossCancel(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeFailure)
	settler := &settlingCanceller{db: fix.db, attempt: fix.attempt}
	fix.svc.cfg.Cancel = settler
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport failure: %v", err)
	}
	attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.State != domain.AttemptFailed {
		t.Fatalf("attempt state after raced cancel = %q, want failed", attempt.State)
	}
	if attempt.LastError != report.Summary {
		t.Fatalf("attempt detail = %q, want %q", attempt.LastError, report.Summary)
	}
	if len(settler.runs) != 1 || settler.runs[0] != fix.attempt.RunID {
		t.Fatalf("CancelMission calls = %v, want [%s]", settler.runs, fix.attempt.RunID)
	}
}

// TestReconcileReportNoticesTheIntegratorTerminal covers the wake-up an
// interactive integrator depends on after dispatching: each worker report
// types exactly one aether: line naming the attempt, a replay types none, and
// the line never carries the worker's own summary.
func TestReconcileReportNoticesTheIntegratorTerminal(t *testing.T) {
	for _, outcome := range []store.CoordOutcome{store.CoordOutcomeSuccess, store.CoordOutcomeFailure, store.CoordOutcomeBlocked} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			fix, report := setupReconcileReportFor(t, outcome, domain.LaunchTUI)
			report.Summary = "summary $(touch PWNED) `touch PWNED`; rm -rf /"
			if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
				t.Fatalf("ReconcileReport %s: %v", outcome, err)
			}
			text := workerReportNotice(outcome, fix.attempt)
			fix.expectNotice(t, string(outcome)+" report", text)
			if strings.Contains(text, report.Summary) || !strings.Contains(text, string(fix.attempt.ID)) ||
				!strings.Contains(text, string(fix.attempt.TaskID)) || !strings.Contains(text, string(fix.attempt.RunID)) {
				t.Fatalf("notice %q must name the attempt, task, and run and never the summary", text)
			}
			if outcome == store.CoordOutcomeBlocked {
				return
			}
			if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
				t.Fatalf("replayed ReconcileReport %s: %v", outcome, err)
			}
			fix.notices.expectNone(t, "replayed "+string(outcome)+" report")
		})
	}
}

// TestReconcileReportNoticeSkipsAHeadlessOrReplacedIntegrator: a headless
// integrator never reads its terminal, and a report reconciled after the
// integrator was replaced reaches the replacement, never the retired run.
func TestReconcileReportNoticeSkipsAHeadlessOrReplacedIntegrator(t *testing.T) {
	ctx := context.Background()
	fix, report := setupReconcileReport(t, store.CoordOutcomeSuccess)
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport for a headless integrator: %v", err)
	}
	fix.notices.expectNone(t, "success report to a headless integrator")

	fix, report = setupReconcileReportFor(t, store.CoordOutcomeSuccess, domain.LaunchTUI)
	choice := domain.MissionIntegrator{AccountMemberID: fix.mission.AccountableHumanID, Harness: "claude", Mode: domain.LaunchTUI}
	replaced, err := fix.db.ReplaceIntegrator(ctx, fix.mission.ID, fix.mission.IntegratorGeneration, choice, fix.mission.AccountableHumanID, fix.mission.AccountableHumanID, "replacement-1")
	if err != nil {
		t.Fatalf("replace integrator: %v", err)
	}
	if err := fix.db.CreateRunWithID(ctx, &domain.Run{
		ID: replaced.CurrentIntegratorRunID, WorkspaceID: fix.mission.WorkspaceID, MemberID: fix.mission.AccountableHumanID,
		Task: "integrator", Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued,
	}); err != nil {
		t.Fatalf("create replacement integrator run: %v", err)
	}
	if err := fix.svc.ReconcileReport(ctx, fix.attempt.RunID, report, fix.packet); err != nil {
		t.Fatalf("ReconcileReport after replacement: %v", err)
	}
	fix.notices.expect(t, "success report after the integrator was replaced", replaced.CurrentIntegratorRunID,
		workerReportNotice(store.CoordOutcomeSuccess, fix.attempt), harness.SubmitSequence("claude"))
	fix.notices.expectNone(t, "retired integrator after replacement")
}

// TestReconcileNoticesAWorkerThatEndedWithoutAReport: a worker whose run
// ended before it reported is marked failed and the integrator is told; one
// the integrator cancelled is not news to it.
func TestReconcileNoticesAWorkerThatEndedWithoutAReport(t *testing.T) {
	for reason, want := range map[string]domain.AttemptState{"exited 1": domain.AttemptFailed, "killed": domain.AttemptCancelled} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			fix, _ := setupReconcileReportFor(t, store.CoordOutcomeSuccess, domain.LaunchTUI)
			fix.svc.cfg.ObserveMissionRun = func(context.Context, domain.RunID) (MissionRunObservation, error) {
				return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
			}
			if err := fix.db.UpdateRunStatus(ctx, fix.attempt.RunID, domain.RunFailed, reason, nil, nil); err != nil {
				t.Fatalf("end worker run: %v", err)
			}
			if err := fix.svc.reconcileMission(ctx, fix.mission); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			attempt, err := fix.db.GetAttempt(ctx, fix.attempt.ID)
			if err != nil {
				t.Fatalf("get attempt: %v", err)
			}
			if attempt.State != want {
				t.Fatalf("attempt state after the run ended %q = %q, want %q", reason, attempt.State, want)
			}
			if want == domain.AttemptCancelled {
				fix.notices.expectNone(t, "cancelled worker")
				return
			}
			fix.expectNotice(t, "worker ended without a report", attemptEndedNotice(fix.attempt))
		})
	}
}

// TestWorkerNoticesAreShellInert: a notice can land in the shell left on an
// exited integrator's terminal, so its text holds nothing a shell acts on.
func TestWorkerNoticesAreShellInert(t *testing.T) {
	attempt := &domain.Attempt{ID: "attempt-1", TaskID: "task-1", RunID: "run-1"}
	for _, text := range []string{
		workerReportNotice(store.CoordOutcomeSuccess, attempt),
		workerReportNotice(store.CoordOutcomeFailure, attempt),
		workerReportNotice(store.CoordOutcomeBlocked, attempt),
		attemptEndedNotice(attempt),
	} {
		if strings.ContainsAny(text, "\"'`;()<>|&$\n") {
			t.Fatalf("notice %q carries a shell-active character", text)
		}
	}
}
