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

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
)

// legalTransition encodes the pinned lifecycle table (Wave 1 contract
// §6.6). Terminal states never transition.
func legalTransition(from, to domain.RunStatus) bool {
	if from.Terminal() || !to.Valid() {
		return false
	}
	switch to {
	case domain.RunProvisioning:
		return from == domain.RunQueued
	case domain.RunRunning:
		// Provisioned, or a stalled-but-alive run whose activity resumed.
		return from == domain.RunProvisioning || from == domain.RunNeedsAttention
	case domain.RunNeedsAttention:
		// Stall or agent exit; exit from an already-stalled run re-enters
		// with a new reason.
		return from == domain.RunRunning || from == domain.RunNeedsAttention
	case domain.RunFailed:
		return from == domain.RunProvisioning || from == domain.RunRunning ||
			from == domain.RunNeedsAttention
	case domain.RunMerged:
		return from == domain.RunNeedsAttention
	case domain.RunAbandoned, domain.RunInterrupted:
		return true // any non-terminal state
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
	if to.Terminal() {
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
	RunID         string `json:"run_id"`
	ContainerID   string `json:"container_id"`
	WorkspaceID   string `json:"workspace_id"`
	Paused        bool   `json:"paused"`
	KillRequested bool   `json:"kill_requested"`
	// RunUser is the resolved numeric "uid:gid" the run's container and
	// ownership pass use; empty means root. Recovered so the
	// credential-home ownership guard still sees live runs across a
	// server restart.
	RunUser string `json:"run_user,omitempty"`
	// ExitObserved is set after Runtime.Wait returns successfully, before
	// finalize. Recovery uses it to resume exit handling without re-attaching.
	ExitObserved bool `json:"exit_observed"`
	ExitCode     int  `json:"exit_code"`
	// The conflict-coordination assets this run's container holds, written
	// before the container is created. BridgeDigest and BridgePath name the
	// staged MCP bridge binary and are the reference that keeps it from
	// being collected; CoordDir is the provisioned coordination directory,
	// and its presence is what "this run has coordination" means. All empty
	// for a run launched with coordination off.
	BridgeDigest string `json:"bridge_digest,omitempty"`
	BridgePath   string `json:"bridge_path,omitempty"`
	CoordDir     string `json:"coord_dir,omitempty"`
}

// sidecar snapshots the entry's durable state. Caller must hold s.mu.
func (e *supervised) sidecar() sidecar {
	return sidecar{
		RunID:         string(e.runID),
		ContainerID:   string(e.containerID),
		WorkspaceID:   string(e.workspaceID),
		Paused:        e.paused,
		KillRequested: e.killRequested,
		RunUser:       e.runUser,
		ExitObserved:  e.exitObserved,
		ExitCode:      e.exitCode,
		BridgeDigest:  e.bridgeDigest,
		BridgePath:    e.bridgePath,
		CoordDir:      e.coordDir,
	}
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
	s.cleanupProfile(run)
}
