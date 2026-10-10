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
	"github.com/3xDevOps/Aether/internal/runtime"
)

// legalTransition encodes the run lifecycle table. Any state that holds a
// record may be closed as merged or abandoned on a human's say-so - live runs
// are stopped first, finished ones re-labeled. Final dispositions never
// transition.
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

// Cleanup diagnostics cross the public API boundary. Keep an allowlist rather
// than truncating raw errors: runtime and filesystem errors may contain host
// paths, command output or credentials. The detailed error stays in server logs.
const (
	cleanupEvidenceError = "Evidence preservation failed; cleanup will retry"
	cleanupRuntimeError  = "Execution cleanup failed; cleanup will retry"
	cleanupStateError    = "Cleanup state persistence failed; cleanup will retry"
	cleanupLookupError   = "Execution ownership lookup failed; cleanup will retry"
	cleanupUnknownError  = "Execution cleanup failed; see server logs"
)

func publicCleanupError(cause string) string {
	switch cause {
	case "", cleanupEvidenceError, cleanupRuntimeError, cleanupStateError, cleanupLookupError, cleanupUnknownError:
		return cause
	default:
		return cleanupUnknownError
	}
}

func (s *Scheduler) recordCleanupError(entry *supervised, cause string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCleanupErrorLocked(entry, cause)
}

// recordCleanupErrorLocked keeps the live cause even if its durable write
// fails, just like the ownership marker. Caller must hold s.mu.
func (s *Scheduler) recordCleanupErrorLocked(entry *supervised, cause string) {
	if entry == nil || s.runs[entry.runID] != entry {
		return
	}
	cause = publicCleanupError(cause)
	changed := entry.cleanupError != cause
	entry.cleanupError = cause
	if err := s.persistRetainedSidecar(entry.sidecar()); err != nil {
		slog.Warn("scheduler: persist cleanup diagnostic", "run", entry.runID, "error", err)
	}
	if changed {
		s.publishRetentionLocked(entry.runID)
	}
}

func (s *Scheduler) recordRunCleanupError(run domain.RunID, cause string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.runs[run]; entry != nil {
		s.recordCleanupErrorLocked(entry, cause)
		return
	}
	sc, err := s.readSidecar(run)
	if err != nil || sc.RunID != string(run) {
		// Never replace corrupt or unknown ownership with a diagnostic.
		slog.Warn("scheduler: read ownership for cleanup diagnostic", "run", run, "error", err)
		return
	}
	cause = publicCleanupError(cause)
	changed := sc.CleanupError != cause
	sc.CleanupError = cause
	if err := s.persistRetainedSidecar(sc); err != nil {
		slog.Warn("scheduler: persist cleanup diagnostic", "run", run, "error", err)
		return
	}
	if changed {
		s.publishRetentionLocked(run)
	}
}

func (s *Scheduler) publishRetention(run domain.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishRetentionLocked(run)
}

func retentionPayload(info RetentionInfo) events.RunRetentionPayload {
	payload := events.RunRetentionPayload{
		CleanupPending: info.CleanupPending,
		CleanupError:   info.CleanupError,
	}
	if info.RetainedUntil != nil {
		payload.ContainerRetainedUntil = info.RetainedUntil.UTC().Format(time.RFC3339Nano)
	}
	return payload
}

// rememberRetentionLocked establishes the pre-mutation baseline when adopting
// durable ownership after restart. Adoption itself is not a metadata change.
func (s *Scheduler) rememberRetentionLocked(row *domain.Run) {
	if _, known := s.retentionPublished[row.ID]; known || s.cfg.Bus == nil {
		return
	}
	info, _, err := s.retentionLocked(row)
	if err != nil {
		return
	}
	payload := retentionPayload(info)
	if payload != (events.RunRetentionPayload{}) {
		if s.retentionPublished == nil {
			s.retentionPublished = make(map[domain.RunID]events.RunRetentionPayload)
		}
		s.retentionPublished[row.ID] = payload
	}
}

// publishRetentionLocked publishes only changed runtime metadata, independently
// of business status. Persistence precedes publication; a failed sidecar write
// still leaves the live owner authoritative. Caller holds s.mu.
func (s *Scheduler) publishRetentionLocked(run domain.RunID) {
	if s.cfg.Bus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	row, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		return
	}
	info, _, err := s.retentionLocked(row)
	if err != nil {
		return
	}
	payload := retentionPayload(info)
	if s.retentionPublished[run] == payload {
		return
	}
	if _, publishErr := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: row.WorkspaceID,
		RunID:       run,
		Payload:     payload,
	}); publishErr != nil {
		slog.Warn("scheduler: publish retention failed", "run", run, "error", publishErr)
		return
	}
	if payload == (events.RunRetentionPayload{}) {
		delete(s.retentionPublished, run)
	} else {
		if s.retentionPublished == nil {
			s.retentionPublished = make(map[domain.RunID]events.RunRetentionPayload)
		}
		s.retentionPublished[run] = payload
	}
}

// finishCause is what moved a run to its status, which decides the
// outcome_unseen and finish_unopened flags the row and its run.status event
// carry. The transition's actor cannot stand in for it: a launch passes the
// launching member, so a provisioning failure carries an actor who ended
// nothing.
type finishCause int

const (
	// causeUnattended is a change no member asked for: an exit, a failure, a
	// restart, or a swarm completing or stopping its workers.
	causeUnattended finishCause = iota
	// causeReported is an agent's success or failure report.
	causeReported
	// causeMember is a close or kill a member asked for.
	causeMember
)

// memberCause is the cause of a close or kill asked for by actor. CloseRun
// and Kill carry the member; CompleteMission and CancelMission, the swarm's
// and its integrator's own endings, carry none.
func memberCause(actor domain.MemberID) finishCause {
	if actor == "" {
		return causeUnattended
	}
	return causeMember
}

// transitionLocked persists a legal status change via UpdateRunStatus and
// publishes the run.status event. The caller must hold s.mu; from must be
// the run's current status.
func (s *Scheduler) transitionLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, from, to domain.RunStatus, reason string, actor domain.MemberID) error {
	return s.transitionOutcomeLocked(ctx, run, workspace, from, to, reason, actor, causeUnattended)
}

// transitionOutcomeLocked also marks a newly reported outcome unseen and
// unopened, including a nonterminal interactive park. Other transitions
// clear outcome_unseen and leave finish_unopened set only on a terminal
// status no member asked for.
func (s *Scheduler) transitionOutcomeLocked(ctx context.Context, run domain.RunID, workspace domain.WorkspaceID, from, to domain.RunStatus, reason string, actor domain.MemberID, cause finishCause) error {
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
	switch cause {
	case causeReported:
		write = s.cfg.Store.FinishRunReported
	case causeMember:
		write = s.cfg.Store.FinishRunByMember
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
		Payload: events.RunStatusPayload{
			From: from, To: to, Reason: public,
			OutcomeUnseen:  cause == causeReported,
			FinishUnopened: cause == causeReported || (cause == causeUnattended && to.Terminal()),
		},
	})
	if to.Terminal() || s.retentionPublished[run] != (events.RunRetentionPayload{}) {
		s.publishRetentionLocked(run)
	}
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
// <StateDir>/<run-id>.json. An older file's session_id key is ignored on
// decode; the run's workspace is read off the run row (entryFromSidecar).
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
	CleanupError    string            `json:"cleanup_error,omitempty"`
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
	IdleReason          string                   `json:"blocked_reason,omitempty"`
	IdleShown           bool                     `json:"blocked_shown,omitempty"`
	IdleReportID        string                   `json:"blocked_report_id,omitempty"`
	IdleReportAt        *time.Time               `json:"blocked_report_at,omitempty"`
	RelaunchedAt        *time.Time               `json:"relaunched_at,omitempty"`
	ExitObserved        bool                     `json:"exit_observed"`
	ExitCode            int                      `json:"exit_code"`
	EvidenceIdentity    string                   `json:"evidence_identity,omitempty"`
	BridgeDigest        string                   `json:"bridge_digest,omitempty"`
	BridgePath          string                   `json:"bridge_path,omitempty"`
	CoordDir            string                   `json:"coord_dir,omitempty"`
	GitAuthorEmail      string                   `json:"git_author_email,omitempty"`
	// AgentSessionID is the agent's own session, from the session host or
	// the agent's status reports. AgentExec is the managed exec of a driver
	// that hosts the agent outside the primary PTY, which every reattach
	// stops before starting a fresh one.
	AgentSessionID string                `json:"agent_session_id,omitempty"`
	AgentExec      *runtime.ExecIdentity `json:"agent_exec,omitempty"`
	Switch         *switchIntent         `json:"switch,omitempty"`
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
	var relaunchedAt, idleReportAt *time.Time
	if t := e.relaunchedAt; !t.IsZero() {
		relaunchedAt = &t
	}
	if t := e.idleReportAt; !t.IsZero() {
		idleReportAt = &t
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
		CleanupError:        publicCleanupError(e.cleanupError),
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
		IdleReason:          e.idleReason,
		IdleShown:           e.idleShown,
		IdleReportID:        e.idleReportID,
		IdleReportAt:        idleReportAt,
		RelaunchedAt:        relaunchedAt,
		ExitObserved:        e.exitObserved,
		ExitCode:            e.exitCode,
		EvidenceIdentity:    e.evidenceIdentity,
		BridgeDigest:        e.bridgeDigest,
		BridgePath:          e.bridgePath,
		CoordDir:            e.coordDir,
		GitAuthorEmail:      e.gitAuthorEmail,
		AgentSessionID:      e.agentSessionID,
		AgentExec:           e.agentExec,
		Switch:              e.switchIntent,
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
	sc.CleanupError = publicCleanupError(sc.CleanupError)
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
	sc.CleanupError = publicCleanupError(sc.CleanupError)
	return sc, nil
}

func (s *Scheduler) removeSidecar(run domain.RunID) {
	if err := os.Remove(s.sidecarPath(run)); err != nil && !os.IsNotExist(err) {
		slog.Warn("scheduler: remove sidecar failed", "run", run, "error", err)
	}
	s.releaseCoordination(run)
}
