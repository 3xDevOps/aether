package scheduler

import (
	"context"
	"encoding/json"
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
	if err := s.cfg.Store.UpdateRunStatus(ctx, run, to, public, startedAt, finishedAt); err != nil {
		return err
	}
	if e := s.runs[run]; e != nil {
		e.status = to
		if startedAt != nil {
			e.startedAt = now
		}
	}
	s.publish(ctx, events.Event{
		WorkspaceID: workspace,
		RunID:       run,
		ActorID:     actor,
		Payload:     events.RunStatusPayload{From: from, To: to, Reason: public},
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
	RunID         string            `json:"run_id"`
	ContainerID   string            `json:"container_id"`
	WorkspaceID   string            `json:"workspace_id"`
	Mode          domain.LaunchMode `json:"mode,omitempty"`
	Paused        bool              `json:"paused"`
	KillRequested bool              `json:"kill_requested"`
	Retained      bool              `json:"retained,omitempty"`
	RetainedUntil *time.Time        `json:"retained_until,omitempty"`
	// DestroyPending is durable ownership for a container whose destruction
	// was attempted but not confirmed. Such an owner is retried by the
	// bounded cleanup sweep before the run is terminalized.
	DestroyPending bool `json:"destroy_pending,omitempty"`
	// RunUser is the resolved numeric "uid:gid" the run's container and
	// ownership pass use; empty means root. Recovered so the
	// credential-home ownership guard still sees live runs across a
	// server restart.
	RunUser string `json:"run_user,omitempty"`
	Home    string `json:"home,omitempty"`
	// Reporter is how much the status reporter this run's container was
	// actually given can say about its own state. Recorded rather than
	// recomputed on recovery: a headless run, a run with coordination off,
	// an argv override and a member's own harness definition all get no
	// reporter whatever the registry says about the harness name, and only
	// the launch saw that. Absent in a sidecar written before runs had
	// reporters, which reads as "none" - the behavior that build had.
	Reporter harness.Reporter `json:"reporter,omitempty"`
	// AgentState and AgentReason are the last thing the agent said about
	// itself, in the same text form the state travels in on the wire. The
	// run row carries the status the report produced but not who asked for
	// it, and only the report tells a run the agent parked for its member
	// from one that stalled - so without these a recovered run is released
	// by the first thing its agent repaints. Absent, or anything but a
	// state this build knows, reads as "no report yet".
	AgentState  agentstatus.State `json:"agent_state,omitempty"`
	AgentReason string            `json:"agent_reason,omitempty"`
	// ExitObserved is set after Runtime.Wait returns successfully, before
	// finalize. Recovery uses it to resume exit handling without re-attaching.
	ExitObserved bool `json:"exit_observed"`
	ExitCode     int  `json:"exit_code"`
	// The conflict-coordination assets this run's container holds, written
	// before the container is created. BridgeDigest and BridgePath name
	// the staged MCP bridge binary and are the reference that keeps it from
	// being collected; CoordDir is the provisioned coordination directory,
	// and its presence is what "this run has coordination" means. All empty
	// for a run launched with coordination off.
	BridgeDigest string `json:"bridge_digest,omitempty"`
	BridgePath   string `json:"bridge_path,omitempty"`
	CoordDir     string `json:"coord_dir,omitempty"`
	// GitAuthorEmail is the address baked into the container's
	// GIT_AUTHOR_EMAIL when it was created, kept so a restart still knows
	// who the agent's own commits are authored as.
	GitAuthorEmail string `json:"git_author_email,omitempty"`
}

// sidecar snapshots the entry's durable state. Caller must hold s.mu.
func (e *supervised) sidecar() sidecar {
	return sidecar{
		RunID:          string(e.runID),
		ContainerID:    string(e.containerID),
		WorkspaceID:    string(e.workspaceID),
		Mode:           e.launchMode,
		Paused:         e.paused,
		KillRequested:  e.killRequested,
		Retained:       e.retained,
		RetainedUntil:  e.retainedUntil,
		DestroyPending: e.destroyPending,
		RunUser:        e.runUser,
		Home:           e.home,
		Reporter:       e.reporter,
		AgentState:     e.agentReport.State,
		AgentReason:    e.agentReport.Reason,
		ExitObserved:   e.exitObserved,
		ExitCode:       e.exitCode,
		BridgeDigest:   e.bridgeDigest,
		BridgePath:     e.bridgePath,
		CoordDir:       e.coordDir,
		GitAuthorEmail: e.gitAuthorEmail,
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
