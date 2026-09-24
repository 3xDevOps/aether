package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/mission"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
	"github.com/3xDevOps/Aether/internal/store"
)

// Mission orchestration owns durable objectives and the integrator/worker
// authority. It attaches to the existing SSH seam; ordinary runs remain
// ordinary runs when no MissionStore is available.
func init() {
	registerService("mission", func(d Deps) (Service, error) {
		ms, ok := d.Store.(store.MissionStore)
		if !ok {
			return nil, errors.New("mission: store does not implement MissionStore")
		}
		var launcher interface {
			mission.Launcher
			mission.Canceller
		}
		if d.Runs != nil {
			launcher = schedulerLauncher{runs: d.Runs}
		}
		cfg := mission.Config{
			Store: d.Store, Missions: ms, Runs: launcher, Cancel: launcher,
			AuthorizationMu: d.SSH.AuthorizationMu, Cost: d.SSH.Services.Costs,
			Evidence: d.Evidence, ScopeSnapshot: lazyMissionScope{ssh: d.SSH}.Snapshot,
			Bus: d.Bus, PTY: d.PTY,
			Integration: func() (sshd.IntegrationService, error) {
				if d.SSH == nil {
					return nil, errors.New("mission: SSH config is unavailable")
				}
				base := d.SSH.Services.Integration
				if base == nil {
					return nil, errors.New("mission: integration service is unavailable")
				}
				// A mission adapter is installed before the integration
				// service when registration order is reversed. Its owner
				// stores the real base once the integration builder runs.
				if _, isAdapter := base.(interface {
					SetIntegrationService(sshd.IntegrationService)
				}); isAdapter {
					return nil, errors.New("mission: integration service is not bound")
				}
				return base, nil
			},
			MissionControl: func() (sshd.MissionControl, error) {
				if d.SSH == nil || d.SSH.Services.MissionControl == nil {
					return nil, errors.New("mission: mission control is unavailable")
				}
				return d.SSH.Services.MissionControl, nil
			},
			RequireCoordination: func() error {
				if d.Runs == nil {
					return errors.New("mission: scheduler unavailable")
				}
				_, err := d.Runs.RequireCoordination()
				if err != nil && d.Config.CoordinationDisabled {
					return fmt.Errorf("swarms need conflict coordination; the server was started with --conflict-coordination=false: %w", err)
				}
				return err
			},
		}
		if d.Runs != nil {
			cfg.ObserveMissionRun = func(ctx context.Context, run domain.RunID) (mission.MissionRunObservation, error) {
				observed, err := d.Runs.ObserveMissionRun(ctx, run)
				if err != nil {
					return mission.MissionRunObservation{}, err
				}
				return mission.MissionRunObservation{
					State: mission.MissionRunState(observed.State), RetentionSettled: observed.RetentionSettled,
				}, nil
			}
		}
		baseIntegration := d.SSH.Services.Integration
		svc, err := mission.New(cfg)
		if err != nil {
			return nil, err
		}
		if baseIntegration != nil {
			if _, isAdapter := baseIntegration.(interface {
				SetIntegrationService(sshd.IntegrationService)
			}); !isAdapter {
				svc.SetIntegrationService(baseIntegration)
			}
		}
		d.SSH.Services.Missions = svc
		d.SSH.Services.Integration = svc.IntegrationAdapter()
		return svc, nil
	})
}

type schedulerLauncher struct{ runs *scheduler.Scheduler }

func (l schedulerLauncher) LaunchMission(ctx context.Context, req mission.MissionLaunchRequest) (*domain.Run, error) {
	if l.runs == nil {
		return nil, errors.New("mission: scheduler unavailable")
	}
	return l.runs.LaunchMission(ctx, scheduler.MissionLaunchSpec{
		WorkspaceID:  req.WorkspaceID,
		RunID:        req.RunID,
		Actor:        req.RunOwnerID,
		RunOwner:     req.RunOwnerID,
		AccountOwner: req.AccountOwner,
		Task:         req.Task,
		Harness:      req.Harness,
		Mode:         req.Mode,
	})
}

func (l schedulerLauncher) ValidateMissionLaunch(ctx context.Context, account domain.MemberID, harnessName string, mode domain.LaunchMode) error {
	if l.runs == nil {
		return errors.New("mission: scheduler unavailable")
	}
	return l.runs.ValidateMissionLaunch(ctx, account, harnessName, mode)
}

func (l schedulerLauncher) CancelMission(ctx context.Context, run domain.RunID) error {
	if l.runs == nil {
		return errors.New("mission: scheduler unavailable")
	}
	return l.runs.CancelMission(ctx, run)
}

type lazyMissionScope struct{ ssh *sshd.Config }

func (l lazyMissionScope) Snapshot(ctx context.Context, run domain.RunID) ([]string, bool, error) {
	if l.ssh == nil || l.ssh.Services.Overlaps == nil {
		return nil, false, errors.New("server: the conflict radar is not enabled")
	}
	snapshot, ok := l.ssh.Services.Overlaps.(interface {
		Snapshot(context.Context, domain.RunID) ([]string, bool, error)
	})
	if !ok {
		return nil, false, errors.New("server: the conflict radar has no snapshot seam")
	}
	return snapshot.Snapshot(ctx, run)
}

type lazyMission struct{ ssh *sshd.Config }

func (l lazyMission) service() (sshd.MissionService, error) {
	if l.ssh == nil || l.ssh.Services.Missions == nil {
		return nil, errors.New("mission: mission service is unavailable")
	}
	return l.ssh.Services.Missions, nil
}

func (l lazyMission) Assignment(ctx context.Context, run domain.RunID) (protocol.CoordMissionAssignment, error) {
	s, err := l.service()
	if err != nil {
		return protocol.CoordMissionAssignment{}, err
	}
	return s.(interface {
		Assignment(context.Context, domain.RunID) (protocol.CoordMissionAssignment, error)
	}).Assignment(ctx, run)
}
func (l lazyMission) Peers(ctx context.Context, run domain.RunID) ([]protocol.CoordPeer, error) {
	s, err := l.service()
	if err != nil {
		return nil, err
	}
	return s.(interface {
		Peers(context.Context, domain.RunID) ([]protocol.CoordPeer, error)
	}).Peers(ctx, run)
}
func (l lazyMission) HandleAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	s, err := l.service()
	if err != nil {
		return nil, err
	}
	return s.(interface {
		HandleAgent(context.Context, domain.RunID, string, json.RawMessage) (any, error)
	}).HandleAgent(ctx, run, method, raw)
}
func (l lazyMission) ValidateReport(ctx context.Context, run domain.RunID) error {
	s, err := l.service()
	if err != nil {
		return err
	}
	return s.(interface {
		ValidateReport(context.Context, domain.RunID) error
	}).ValidateReport(ctx, run)
}
func (l lazyMission) ReconcileReport(ctx context.Context, run domain.RunID, report *store.CoordReport, packet protocol.EvidencePacket) error {
	s, err := l.service()
	if err != nil {
		return err
	}
	return s.(interface {
		ReconcileReport(context.Context, domain.RunID, *store.CoordReport, protocol.EvidencePacket) error
	}).ReconcileReport(ctx, run, report, packet)
}

var _ sshd.MissionService = (*mission.Service)(nil)
