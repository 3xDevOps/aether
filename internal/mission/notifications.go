package mission

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/store"
)

// noticeActor is the attribution an integrator notice carries: the server
// speaking, not a member, so it takes no palette color.
const noticeActor = "aether"

// noticeTimeout bounds one integrator notice. It runs detached from the
// human's request, so it needs a deadline of its own.
const noticeTimeout = 5 * time.Second

const answerNotice = "The accountable human answered a mission question. " +
	"Run /usr/local/bin/aether-internal mission plan show to read the answers."

// planDecisionNotice is the line that tells the integrator a human decided
// its plan. Decisions are validated before they reach here.
func planDecisionNotice(d domain.MissionPlanDecision) string {
	switch d {
	case domain.MissionPlanApprove:
		return "The plan was approved and the mission is active. Run /usr/local/bin/aether-internal skill for the next steps."
	case domain.MissionPlanRevise:
		return "The plan was sent back with feedback. Run /usr/local/bin/aether-internal skill to read the feedback and revise the plan."
	default:
		return "The plan was rejected and the mission is over. Run /usr/local/bin/aether-internal skill."
	}
}

// workerReportNotice is the line that tells the integrator a worker
// reported. It carries only server-issued IDs: a worker's summary is its own
// text and would land in the shell left on an exited integrator's terminal.
func workerReportNotice(outcome store.CoordOutcome, attempt *domain.Attempt) string {
	inspect := "worker inspect --attempt-id " + string(attempt.ID)
	switch outcome {
	case store.CoordOutcomeSuccess:
		return fmt.Sprintf("Worker run %s reported success on task %s, attempt %s. Run /usr/local/bin/aether-internal %s to review the submission, then task accept-submission or retry.", attempt.RunID, attempt.TaskID, attempt.ID, inspect)
	case store.CoordOutcomeFailure:
		return fmt.Sprintf("Worker run %s reported failure on task %s, attempt %s. Run /usr/local/bin/aether-internal %s.", attempt.RunID, attempt.TaskID, attempt.ID, inspect)
	default:
		return fmt.Sprintf("Worker run %s reports blocked on task %s, attempt %s. Run /usr/local/bin/aether-internal inbox and %s.", attempt.RunID, attempt.TaskID, attempt.ID, inspect)
	}
}

// attemptEndedNotice is the line that tells the integrator a worker's run
// ended before it reported.
func attemptEndedNotice(attempt *domain.Attempt) string {
	return fmt.Sprintf("Worker run %s on task %s ended without a report, attempt %s. Run /usr/local/bin/aether-internal worker inspect --attempt-id %s.", attempt.RunID, attempt.TaskID, attempt.ID, attempt.ID)
}

// publishMissionChanged emits a projection hint only after the caller's
// durable mutation has committed. Consumers must re-read the mission from the
// authoritative mission APIs; the payload carries versions for refresh
// ordering, not a freeform state snapshot.
func (s *Service) publishMissionChanged(ctx context.Context, missionID domain.MissionID) error {
	if s.cfg.Bus == nil {
		return nil
	}
	mission, err := s.cfg.Missions.GetMission(ctx, missionID)
	if err != nil {
		return fmt.Errorf("mission: load changed mission %q for event: %w", missionID, err)
	}
	if mission == nil {
		return errors.New("mission: changed mission is unavailable")
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: mission.WorkspaceID,
		Payload: events.MissionChangedPayload{
			MissionID:            mission.ID,
			IntegratorGeneration: mission.IntegratorGeneration,
			AcceptedSetVersion:   mission.AcceptedSetVersion,
		},
	}); err != nil {
		return fmt.Errorf("mission: publish changed event for %q: %w", mission.ID, err)
	}
	return nil
}

// noticeIntegrator writes text into the mission's current integrator
// terminal in the background, so an agent idle after asking, submitting, or
// dispatching learns that a human or a worker acted without that request
// waiting on the PTY. Delivery is best effort: the durable change already
// committed, and mission.plan.show and worker list still report it.
func (s *Service) noticeIntegrator(ctx context.Context, m *domain.Mission, text string) {
	if s.cfg.PTY == nil || m.CurrentIntegratorRunID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(s.operationContext(ctx), noticeTimeout)
	go func() {
		defer cancel()
		if err := s.deliverNotice(ctx, m, text); err != nil {
			slog.Warn("mission: integrator notice not delivered", "mission", m.ID, "run", m.CurrentIntegratorRunID, "error", err)
		}
	}()
}

// deliverNotice returns nil when the line reached the terminal and when
// there was deliberately nothing to deliver; the detached caller reports
// anything else.
func (s *Service) deliverNotice(ctx context.Context, m *domain.Mission, text string) error {
	run := m.CurrentIntegratorRunID
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return fmt.Errorf("mission: resolve integrator run %s: %w", run, err)
	}
	// Every run has a terminal, but a headless harness never reads it: the
	// line would sit unread while the timeline claimed delivery.
	if r.Mode != domain.LaunchTUI {
		return nil
	}
	current, err := s.cfg.Missions.GetMission(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("mission: re-read mission %s before notice: %w", m.ID, err)
	}
	// A replacement reads the mission through skill when it starts; typing
	// into it now would land in its harness's first prompt.
	if current.CurrentIntegratorRunID != run {
		return nil
	}
	err = s.cfg.PTY.Inject(ctx, ptyhost.RunSession(run), noticeActor, "", noticeActor+": "+text, harness.SubmitSequence(r.Harness))
	switch {
	case err == nil:
		s.stampNotice(ctx, m, text)
		return nil
	case errors.Is(err, ptyhost.ErrNoSession), errors.Is(err, ptyhost.ErrSessionEnded):
		// The run's terminal is gone; the integrator learns of the change
		// through mission.plan.show when it next runs.
		return nil
	default:
		return fmt.Errorf("mission: inject integrator notice into run %s: %w", run, err)
	}
}

// stampNotice records a delivered integrator notice on the workspace
// timeline, so the feed says the agent was told.
func (s *Service) stampNotice(ctx context.Context, m *domain.Mission, text string) {
	if s.cfg.Bus == nil {
		return
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: m.WorkspaceID,
		RunID:       m.CurrentIntegratorRunID,
		Payload:     events.TimelinePayload{Kind: events.TimelineNote, Message: "mission notice: " + text},
	}); err != nil {
		slog.Warn("mission: timeline stamp failed", "mission", m.ID, "run", m.CurrentIntegratorRunID, "error", err)
	}
}
