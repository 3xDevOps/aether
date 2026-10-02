package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/store"
)

// MissionLaunchSpec is the scheduler-side launch request for a durable
// mission attempt. RunID is reserved before provisioning and is therefore
// safe to retry after a lost launch response.
type MissionLaunchSpec struct {
	WorkspaceID  domain.WorkspaceID
	RunID        domain.RunID
	Actor        domain.MemberID
	RunOwner     domain.MemberID
	AccountOwner domain.MemberID
	Task         string
	Harness      string
	Mode         domain.LaunchMode
	CachedBase   string
}

// MissionLauncher is the optional scheduler extension consumed by mission
// orchestration. It keeps assigned-run identity out of ordinary run.launch.

// MissionRunState is an observation of the physical scheduler lifecycle.
// Store terminal status is intentionally not treated as cleanup evidence.
type MissionRunState string

const (
	MissionRunActive         MissionRunState = "active"
	MissionRunPending        MissionRunState = "pending"
	MissionRunDestroyPending MissionRunState = "destroy_pending"
	MissionRunRetained       MissionRunState = "retained"
	MissionRunUnknown        MissionRunState = "unknown"
	MissionRunStopped        MissionRunState = "stopped"
)

type MissionRunObservation struct {
	State            MissionRunState
	RetentionSettled bool
}

// ObserveMissionRun is the narrow lifecycle seam consumed by mission
// reconciliation. It reports only confirmed cleanup as stopped; a timeout or
// daemon error remains unknown and therefore continues to hold capacity.
func (s *Scheduler) ObserveMissionRun(ctx context.Context, run domain.RunID) (MissionRunObservation, error) {
	s.mu.Lock()
	if s.pending[run] != nil {
		s.mu.Unlock()
		return MissionRunObservation{State: MissionRunPending}, nil
	}
	if entry := s.runs[run]; entry != nil {
		switch {
		case entry.destroyPending:
			s.mu.Unlock()
			return MissionRunObservation{State: MissionRunDestroyPending}, nil
		case !entry.status.Terminal():
			s.mu.Unlock()
			return MissionRunObservation{State: MissionRunActive}, nil
		default:
			s.mu.Unlock()
			return MissionRunObservation{State: MissionRunRetained}, nil
		}
	}
	s.mu.Unlock()

	r, err := s.cfg.Store.GetRun(ctx, run)
	runExists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return MissionRunObservation{State: MissionRunUnknown}, err
	}
	sc, sidecarErr := s.readSidecar(run)
	sidecarPresent := sidecarErr == nil
	if sidecarPresent {
		if sc.EvidencePending || (sc.EvidenceIdentity != "" && sc.ContainerID == "") {
			return MissionRunObservation{State: MissionRunUnknown}, nil
		}
		if sc.DestroyPending {
			return MissionRunObservation{State: MissionRunDestroyPending}, nil
		}
		if sc.ContainerID != "" {
			probeCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			_, waitErr := s.cfg.Runtime.Wait(probeCtx, runtime.ID(sc.ContainerID))
			cancel()
			switch {
			case waitErr == nil:
				if runExists {
					return MissionRunObservation{State: MissionRunRetained}, nil
				}
				return MissionRunObservation{State: MissionRunUnknown}, nil
			case errors.Is(waitErr, context.DeadlineExceeded), errors.Is(waitErr, context.Canceled):
				if runExists && r != nil && r.Status.Terminal() {
					return MissionRunObservation{State: MissionRunRetained}, nil
				}
				return MissionRunObservation{State: MissionRunActive}, nil
			}
		}
	} else if !os.IsNotExist(sidecarErr) {
		return MissionRunObservation{State: MissionRunUnknown}, sidecarErr
	}
	if _, findErr := s.cfg.Runtime.FindByCreationKey(ctx, string(run)); findErr == nil {
		return MissionRunObservation{State: MissionRunUnknown}, nil
	} else if !errors.Is(findErr, runtime.ErrNotFound) {
		return MissionRunObservation{State: MissionRunUnknown}, findErr
	}
	if !runExists {
		if sidecarPresent {
			return MissionRunObservation{State: MissionRunUnknown}, nil
		}
		return MissionRunObservation{State: MissionRunUnknown}, store.ErrNotFound
	}
	if r.Status.Terminal() {
		return MissionRunObservation{State: MissionRunStopped, RetentionSettled: true}, nil
	}
	return MissionRunObservation{State: MissionRunUnknown}, nil
}

type MissionLauncher interface {
	LaunchMission(context.Context, MissionLaunchSpec) (*domain.Run, error)
}

// createAssignedRun uses the reserved-ID store seam when mission state assigned a
// run identity. Compatibility stores fail closed instead of silently minting
// a second run ID after a lost response.
func createAssignedRun(ctx context.Context, st store.Store, run *domain.Run, assigned domain.RunID) error {
	if assigned == "" {
		return st.CreateRun(ctx, run)
	}
	run.ID = assigned
	reserved, ok := st.(store.ReservedRunStore)
	if !ok {
		return errors.New("scheduler: reserved run IDs are unavailable")
	}
	if err := reserved.CreateRunWithID(ctx, run); err != nil {
		return fmt.Errorf("create reserved run %s: %w", assigned, err)
	}
	return nil
}

func sameReservedRun(existing, requested *domain.Run) bool {
	return existing != nil && requested != nil &&
		existing.ID == requested.ID &&
		existing.WorkspaceID == requested.WorkspaceID &&
		existing.MemberID == requested.MemberID &&
		existing.AccountMember() == requested.AccountMember() &&
		existing.Task == requested.Task &&
		existing.Harness == requested.Harness &&
		existing.Mode == requested.Mode
}

// CancelMission stops an assigned worker without attributing a human action.
// The durable mission decision remains the actor's authority record.
func (s *Scheduler) CancelMission(ctx context.Context, run domain.RunID) error {
	return s.Kill(ctx, run, "")
}

// LaunchMission provisions one preassigned mission run. Mission admission is
// performed by the caller while holding the shared authorization boundary;
// this method owns only the scheduler's durable launch handoff.
func (s *Scheduler) LaunchMission(ctx context.Context, spec MissionLaunchSpec) (*domain.Run, error) {
	if spec.WorkspaceID == "" || spec.RunID == "" || spec.Actor == "" || spec.RunOwner == "" || spec.AccountOwner == "" || spec.Harness == "" || !spec.Mode.Valid() {
		return nil, errors.New("scheduler: invalid mission launch")
	}
	return s.launchWithOptions(ctx, spec.WorkspaceID, spec.RunOwner, spec.AccountOwner, spec.Task, spec.Harness, spec.Mode, domain.LaunchOptions{CachedBase: spec.CachedBase, AssignedRunID: spec.RunID})
}

// ValidateMissionLaunch reports whether account's harness has a command for
// mode, resolving it exactly as a launch would, so a mission never records an
// integrator the scheduler cannot start.
func (s *Scheduler) ValidateMissionLaunch(ctx context.Context, account domain.MemberID, harnessName string, mode domain.LaunchMode) error {
	_, _, err := s.command(ctx, account, harnessName, mode, "task")
	return err
}

func (s *Scheduler) launchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	return s.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, opts)
}
