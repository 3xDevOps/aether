package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/agentstatus"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/harness"
)

// legalTransition encodes the pinned lifecycle table (Wave 1 contract
// §6.6). The close disposition comes first: any state that holds a record
// may be resolved as merged or abandoned on a human's say-so - live runs
// are stopped first, finished ones re-labeled. Everything else: final
// dispositions never transition.
func legalTransition(from, to domain.RunStatus) bool {
	if !to.Valid() {
		return false
	}
	switch to {
	case domain.RunMerged, domain.RunAbandoned:
		return true
	}
	if from.Final() || !from.Valid() {
		return false
	}
	switch to {
	case domain.RunProvisioning:
		return from == domain.RunQueued
	case domain.RunRunning:
		// Provisioned, or a stalled-but-alive run whose activity resumed.
		return from == domain.RunProvisioning || from == domain.RunNeedsAttention
	case domain.RunNeedsAttention:
		// A live run stalls while the agent still has a container.
		return from == domain.RunRunning || from == domain.RunNeedsAttention
	case domain.RunCompleted:
		return from == domain.RunRunning || from == domain.RunNeedsAttention
	case domain.RunFailed:
		return from == domain.RunProvisioning || from == domain.RunRunning ||
			from == domain.RunNeedsAttention
	case domain.RunInterrupted:
		return from == domain.RunQueued || from == domain.RunProvisioning ||
			from == domain.RunRunning || from == domain.RunNeedsAttention
	}
	return false
}

// maxPublicRunStatusReason bounds user-visible status details. Setup output
// is never part of the public reason, even when a non-Docker runtime includes
// it in an error string.
const maxPublicRunStatusReason = 256

func publicRunStatusReason(reason string) string {
	reason = strings.TrimSpace(reason)
	lower := strings.ToLower(reason)
	redactedSetup := false
	for _, marker := range []string{"setup script exited ", "release setup gate exited ", "probe setup gate"} {
		if i := strings.Index(lower, marker); i >= 0 {
			if marker == "probe setup gate" {
				reason = strings.TrimSpace(reason[:i+len(marker)]) + " failed"
			} else {
				end := i + len(marker)
				for end < len(reason) && reason[end] >= '0' && reason[end] <= '9' {
					end++
				}
				reason = strings.TrimSpace(reason[:end])
			}
			redactedSetup = true
			break
		}
	}
	if !redactedSetup {
		for _, marker := range []string{"setup script", "release setup gate"} {
			if i := strings.Index(lower, marker); i >= 0 {
				reason = strings.TrimSpace(reason[:i+len(marker)]) + " failed"
				break
			}
		}
	}
	if runes := []rune(reason); len(runes) > maxPublicRunStatusReason {
		// Wrapped errors put the root cause last ("... exec: \"claude\":
		// executable file not found"); elide the middle so both the failing
		// step and the cause survive the cap.
		head := maxPublicRunStatusReason * 2 / 5
		tail := maxPublicRunStatusReason - head - 5
		reason = string(runes[:head]) + " ... " + string(runes[len(runes)-tail:])
	}
	return reason
}

// transitionLocked persists a legal status change via UpdateRunStatus and
// publishes the run.status event. The caller must hold s.mu; from must be
// the run's current status.
func (s *Scheduler) transitionLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, from, to domain.RunStatus, reason string, actor domain.MemberID) error {
	return s.transitionOutcomeLocked(ctx, run, workspace, from, to, reason, actor, false)
}

// transitionOutcomeLocked is transitionLocked that, when reported, records
// the transition an agent's terminal report causes and marks the outcome
// unseen by the run's owner. The event's OutcomeUnseen equals reported:
// only completed and failed rows carry the flag, and neither has a legal
// same-status transition that would keep it.
func (s *Scheduler) transitionOutcomeLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, from, to domain.RunStatus, reason string, actor domain.MemberID, reported bool) error {
	if !legalTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	now := time.Now().UTC()
	var startedAt, finishedAt *time.Time
	if to == domain.RunRunning && from == domain.RunProvisioning {
		startedAt = &now
	}
	if to.Terminal() && !from.Terminal() {
		finishedAt = &now
	}
	public := publicRunStatusReason(reason)
	write := s.cfg.Store.UpdateRunStatus
	if reported {
		write = s.cfg.Store.FinishRunReported
	}
	if err := write(ctx, run, to, public, startedAt, finishedAt); err != nil {
		return err
	}
	if e := s.runs[run]; e != nil {
		e.status = to
		if startedAt != nil {
			e.startedAt = now
		}
		if to.Terminal() {
			e.agentReport = agentstatus.Report{}
			if len(e.pendingInputs) != 0 || e.inputPublishPending {
				// The terminal row is the durable invalidation. A stale sidecar
				// cannot restore this set, including after a retained relaunch.
				e.pendingInputs = nil
				e.inputPublishPending = false
				s.publish(ctx, events.Event{
					WorkspaceID: workspace,
					RunID:       run,
					ActorID:     actor,
					Payload:     events.RunInputPayload{PendingInputs: []domain.RunInputRequest{}},
				})
			}
		}
	}
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunStatusPayload{From: from, To: to, Reason: public, OutcomeUnseen: reported},
	})
	return nil
}

// publish sends an event on the bus; failures are logged, never fatal -
// the store, not the bus, is the source of truth.
func (s *Scheduler) publish(ctx context.Context, e events.Event) {
	if _, err := s.cfg.Bus.Publish(ctx, e); err != nil {
		slog.Warn("scheduler: publish event failed",
			"type", e.Payload.EventType(), "run", e.RunID, "error", err)
	}
}

func (s *Scheduler) publishTimeline(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID, actor domain.MemberID, kind events.TimelineKind, message string) {
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.TimelinePayload{Kind: kind, Message: message},
	})
}

// sidecar is the durable per-run supervision state at
// <StateDir>/<run-id>.json (Wave 1 contract §6.6). A file written by an
// older build still carries a session_id key; encoding/json ignores
// unknown fields, so it decodes here unchanged, and the run's workspace
// is read off the run row (entryFromSidecar) rather than this file.
type sidecar struct {
	RunID string `json:"run_id"`
	// TerminalMember identifies a member-terminal reference kept outside the
	// run sidecar directory. It is never populated for run supervision.
	TerminalMember  string            `json:"terminal_member,omitempty"`
	ContainerID     string            `json:"container_id"`
	WorkspaceID     string            `json:"workspace_id"`
	Mode            domain.LaunchMode `json:"mode,omitempty"`
	MissionAssigned bool              `json:"mission_assigned,omitempty"`
	Paused          bool              `json:"paused"`
	KillRequested   bool              `json:"kill_requested"`
	Retained        bool              `json:"retained,omitempty"`
	RetainedUntil   *time.Time        `json:"retained_until,omitempty"`
	DestroyPending  bool              `json:"destroy_pending,omitempty"`
	EvidencePending bool              `json:"evidence_pending,omitempty"`
	RunUser         string            `json:"run_user,omitempty"`
	Home            string            `json:"home,omitempty"`
	// LoginMember is the account owner whose login paths the container
	// mounts, empty when it mounts none.
	LoginMember         string                   `json:"login_member,omitempty"`
	Reporter            harness.Reporter         `json:"reporter,omitempty"`
	AgentState          agentstatus.State        `json:"agent_state,omitempty"`
	AgentReason         string                   `json:"agent_reason,omitempty"`
	PendingInputs       []domain.RunInputRequest `json:"pending_inputs,omitempty"`
	InputStartedAt      *time.Time               `json:"input_started_at,omitempty"`
	InputPublishPending bool                     `json:"input_publish_pending,omitempty"`
	ReportedOutcome     domain.RunStatus         `json:"reported_outcome,omitempty"`
	BlockedReason       string                   `json:"blocked_reason,omitempty"`
	BlockedShown        bool                     `json:"blocked_shown,omitempty"`
	BlockedReportID     string                   `json:"blocked_report_id,omitempty"`
	BlockedReportAt     *time.Time               `json:"blocked_report_at,omitempty"`
	RelaunchedAt        *time.Time               `json:"relaunched_at,omitempty"`
	ExitObserved        bool                     `json:"exit_observed"`
	ExitCode            int                      `json:"exit_code"`
	EvidenceIdentity    string                   `json:"evidence_identity,omitempty"`
	BridgeDigest        string                   `json:"bridge_digest,omitempty"`
	BridgePath          string                   `json:"bridge_path,omitempty"`
	CoordDir            string                   `json:"coord_dir,omitempty"`
	GitAuthorEmail      string                   `json:"git_author_email,omitempty"`
}

// sidecar snapshots the entry's durable state. Caller must hold s.mu.
func (e *supervised) sidecar() sidecar {
	var inputStartedAt *time.Time
	pendingInputs := e.pendingInputs
	inputPublishPending := e.inputPublishPending
	if (len(pendingInputs) != 0 || inputPublishPending) && !e.status.Terminal() && !e.exitObserved && !e.retained && !e.destroyPending {
		started := e.startedAt
		inputStartedAt = &started
	} else {
		pendingInputs = nil
		inputPublishPending = false
	}
	var relaunchedAt, blockedReportAt *time.Time
	if t := e.relaunchedAt; !t.IsZero() {
		relaunchedAt = &t
	}
	if t := e.blockedReportAt; !t.IsZero() {
		blockedReportAt = &t
	}
	return sidecar{
		RunID:               string(e.runID),
		ContainerID:         string(e.containerID),
		WorkspaceID:         string(e.workspaceID),
		Mode:                e.launchMode,
		MissionAssigned:     e.missionAssigned,
		Paused:              e.paused,
		KillRequested:       e.killRequested,
		Retained:            e.retained,
		RetainedUntil:       e.retainedUntil,
		DestroyPending:      e.destroyPending,
		EvidencePending:     e.evidencePending,
		RunUser:             e.runUser,
		Home:                e.home,
		LoginMember:         string(e.loginMember),
		Reporter:            e.reporter,
		AgentState:          e.agentReport.State,
		AgentReason:         e.agentReport.Reason,
		PendingInputs:       pendingInputs,
		InputStartedAt:      inputStartedAt,
		InputPublishPending: inputPublishPending,
		ReportedOutcome:     e.reported,
		BlockedReason:       e.blockedReason,
		BlockedShown:        e.blockedShown,
		BlockedReportID:     e.blockedReportID,
		BlockedReportAt:     blockedReportAt,
		RelaunchedAt:        relaunchedAt,
		ExitObserved:        e.exitObserved,
		ExitCode:            e.exitCode,
		EvidenceIdentity:    e.evidenceIdentity,
		BridgeDigest:        e.bridgeDigest,
		BridgePath:          e.bridgePath,
		CoordDir:            e.coordDir,
		GitAuthorEmail:      e.gitAuthorEmail,
	}
}

// agentReport is the report the sidecar carries, or the zero report for a
// file written before runs recorded one - the behavior that build had.
func (sc sidecar) agentReport() agentstatus.Report {
	state, ok := agentstatus.ParseState(string(sc.AgentState))
	if !ok {
		return agentstatus.Report{}
	}
	return agentstatus.Report{State: state, Reason: sc.AgentReason}
}

func (s *Scheduler) sidecarPath(run domain.RunID) string {
	return filepath.Join(s.cfg.StateDir, string(run)+".json")
}
func (s *Scheduler) terminalSidecarDir() string {
	return filepath.Join(s.cfg.StateDir, "terminals")
}

func (s *Scheduler) terminalSidecarPath(member domain.MemberID) string {
	return filepath.Join(s.terminalSidecarDir(), string(member)+".json")
}

func (s *Scheduler) writeTerminalSidecar(sc sidecar) error {
	if sc.TerminalMember == "" || filepath.Base(sc.TerminalMember) != sc.TerminalMember {
		return errors.New("scheduler: terminal sidecar requires a plain member")
	}
	dir := s.terminalSidecarDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scheduler: create terminal sidecar dir: %w", err)
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("scheduler: encode terminal sidecar: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+sc.TerminalMember+"-*")
	if err != nil {
		return fmt.Errorf("scheduler: write terminal sidecar: %w", err)
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.terminalSidecarPath(domain.MemberID(sc.TerminalMember)))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("scheduler: write terminal sidecar: %w", werr)
	}
	if err := fsyncDir(dir); err != nil {
		return err
	}
	return fsyncDir(s.cfg.StateDir)
}

func (s *Scheduler) readTerminalSidecar(member domain.MemberID) (sidecar, error) {
	data, err := os.ReadFile(s.terminalSidecarPath(member))
	if err != nil {
		return sidecar{}, err
	}
	var sc sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return sidecar{}, fmt.Errorf("scheduler: decode terminal sidecar for %s: %w", member, err)
	}
	return sc, nil
}

func (s *Scheduler) removeTerminalSidecar(member domain.MemberID) {
	if err := os.Remove(s.terminalSidecarPath(member)); err != nil && !os.IsNotExist(err) {
		slog.Warn("scheduler: remove terminal sidecar failed", "member", member, "error", err)
		return
	}
	if err := fsyncDir(s.terminalSidecarDir()); err != nil && !os.IsNotExist(err) {
		slog.Warn("scheduler: fsync terminal sidecar dir failed", "member", member, "error", err)
	}
}

// writeSidecar writes atomically: temp file in the same directory, fsync,
// then rename.
func (s *Scheduler) writeSidecar(sc sidecar) error {
	data, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("scheduler: encode sidecar: %w", err)
	}
	tmp, err := os.CreateTemp(s.cfg.StateDir, "."+sc.RunID+"-*")
	if err != nil {
		return fmt.Errorf("scheduler: write sidecar: %w", err)
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.sidecarPath(domain.RunID(sc.RunID)))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("scheduler: write sidecar: %w", werr)
	}
	return nil
}

func (s *Scheduler) readSidecar(run domain.RunID) (sidecar, error) {
	data, err := os.ReadFile(s.sidecarPath(run))
	if err != nil {
		return sidecar{}, err
	}
	var sc sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return sidecar{}, fmt.Errorf("scheduler: decode sidecar for %s: %w", run, err)
	}
	return sc, nil
}

func (s *Scheduler) removeSidecar(run domain.RunID) {
	if err := os.Remove(s.sidecarPath(run)); err != nil && !os.IsNotExist(err) {
		slog.Warn("scheduler: remove sidecar failed", "run", run, "error", err)
	}
	s.releaseCoordination(run)
}
