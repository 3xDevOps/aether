package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/harness"
	"github.com/3xDevOps/Aether/internal/integration"
	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
)

const (
	integrationCleanupInterval = time.Hour
	integrationCleanupTimeout  = 5 * time.Minute
)

// integrationService adapts the candidate service's durable cleanup lifecycle
// to the server's long-lived Service contract. Candidate operations themselves
// are served synchronously by SSH; only cleanup needs a server-owned worker.
type integrationService struct {
	cleanup func(context.Context) error
	close   func() error

	lifecycleMu sync.Mutex
	closed      bool
	startOnce   sync.Once
	startErr    error
	cancel      context.CancelFunc
	done        chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func (s *integrationService) Start(ctx context.Context) error {
	s.startOnce.Do(func() {
		s.lifecycleMu.Lock()
		if s.closed {
			s.lifecycleMu.Unlock()
			return
		}
		s.lifecycleMu.Unlock()

		if s.cleanup != nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, integrationCleanupTimeout)
			err := s.cleanup(cleanupCtx)
			cancel()
			if err != nil {
				s.startErr = fmt.Errorf("initial cleanup: %w", err)
				return
			}
		}

		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		if s.closed {
			return
		}
		workerCtx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.done = make(chan struct{})
		go s.sweep(workerCtx)
	})
	return s.startErr
}

func (s *integrationService) sweep(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(integrationCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.cleanup == nil {
				continue
			}
			sweepCtx, cancel := context.WithTimeout(ctx, integrationCleanupTimeout)
			if err := s.cleanup(sweepCtx); err != nil {
				slog.Warn("integration candidate cleanup failed", "error", err)
			}
			cancel()
		}
	}
}

func (s *integrationService) Close() error {
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closed = true
		cancel := s.cancel
		done := s.done
		s.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		if s.close != nil {
			s.closeErr = s.close()
		}
	})
	return s.closeErr
}

func init() {
	registerService("integration", func(d Deps) (Service, error) {
		if d.SSH == nil {
			return nil, fmt.Errorf("integration: SSH config is required")
		}
		if d.Runtime == nil {
			return nil, fmt.Errorf("integration: runtime is required")
		}

		admission := func(ctx context.Context, a integration.Admission) (func(), error) {
			releases := make([]func(), 0, 2)
			if d.SSH != nil && d.SSH.Services.Missions != nil {
				if policy, ok := d.SSH.Services.Missions.(interface {
					AdmitIntegration(context.Context, integration.Admission) (func(), error)
				}); ok {
					release, policyErr := policy.AdmitIntegration(ctx, a)
					if policyErr != nil {
						return nil, policyErr
					}
					if release != nil {
						releases = append(releases, release)
					}
				}
			}
			if d.Config.IntegrationAdmission != nil {
				release, extraErr := d.Config.IntegrationAdmission(ctx, a)
				if extraErr != nil {
					for i := len(releases) - 1; i >= 0; i-- {
						releases[i]()
					}
					return nil, extraErr
				}
				if release != nil {
					releases = append(releases, release)
				}
			}
			return func() {
				for i := len(releases) - 1; i >= 0; i-- {
					releases[i]()
				}
			}, nil
		}
		svc, err := integration.New(integration.Config{
			Store:       d.Store,
			Git:         d.Git,
			Evidence:    d.Evidence,
			Runtime:     d.Runtime,
			Root:        d.DataDir,
			Environment: integrationEnvironment(d),
			Admission:   admission,
			PrepareRuntime: func(ctx context.Context, spec *runtime.Spec) error {
				if d.Runs == nil {
					return fmt.Errorf("integration: scheduler is unavailable")
				}
				return d.Runs.PrepareVerificationRuntime(ctx, spec)
			},
			ReleaseRuntime: func(ctx context.Context, creationKey string) error {
				if d.Runs == nil {
					return fmt.Errorf("integration: scheduler is unavailable")
				}
				return d.Runs.ReleaseVerificationRuntime(ctx, creationKey)
			},
		})
		if err != nil {
			return nil, fmt.Errorf("integration: create service: %w", err)
		}
		d.Workspaces.candidates = svc
		// SSH owns the transport adapter, while the service remains the one
		// durable candidate engine shared by every transport. If mission
		// registered first, complete its lazy base binding instead.
		if setter, ok := d.SSH.Services.Integration.(interface {
			SetIntegrationService(sshd.IntegrationService)
		}); ok {
			setter.SetIntegrationService(svc)
		} else {
			d.SSH.Services.Integration = svc
		}

		return &integrationService{
			cleanup: integrationCleanup(svc),
			close:   svc.Close,
		}, nil
	})
}

func integrationCleanup(svc *integration.Service) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := svc.Cleanup(ctx)
		return err
	}
}

// integrationEnvironment is the only place where the candidate service gets
// an execution environment. It resolves the authenticated human directly,
// while agent actors use the account member recorded on their run. No request
// field can select a member or grant access to another account.
func integrationEnvironment(d Deps) func(context.Context, integration.Actor, *domain.Workspace, string) (runtime.Spec, error) {
	return func(ctx context.Context, actor integration.Actor, ws *domain.Workspace, checkout string) (runtime.Spec, error) {
		if d.Store == nil || d.Runs == nil {
			return runtime.Spec{}, fmt.Errorf("integration: server environment is not configured")
		}
		if ws == nil || ws.ID == "" {
			return runtime.Spec{}, fmt.Errorf("integration: workspace is required")
		}

		memberID := actor.MemberID
		if actor.RunID != "" {
			run, err := d.Store.GetRun(ctx, actor.RunID)
			if err != nil {
				return runtime.Spec{}, fmt.Errorf("integration: resolve run %q: %w", actor.RunID, err)
			}
			if run.WorkspaceID != ws.ID {
				return runtime.Spec{}, fmt.Errorf("integration: run %q does not belong to workspace %q", actor.RunID, ws.ID)
			}
			memberID = run.AccountMember()
		}
		if memberID == "" {
			return runtime.Spec{}, fmt.Errorf("integration: authenticated member is required")
		}
		member, err := d.Store.GetMember(ctx, memberID)
		if err != nil {
			return runtime.Spec{}, fmt.Errorf("integration: resolve member %q: %w", memberID, err)
		}

		// Supplying the checkout as a synthetic Run makes the scheduler apply
		// the same mount policy used by ordinary runs, including its owned-home
		// roots and the fixed /workspace worktree target.
		run := &domain.Run{WorkspaceID: ws.ID, Worktree: checkout}
		plan, err := d.Runs.BuildEnvironmentPlan(ctx, run, ws, member, harness.Profile{}, scheduler.EnvironmentPurposeRun)
		if err != nil {
			return runtime.Spec{}, fmt.Errorf("integration: build environment: %w", err)
		}
		return runtime.Spec{
			Image:             plan.Image,
			Env:               plan.Env,
			WorktreeHostPath:  checkout,
			WorktreeMountPath: "/workspace",
			WorkingDir:        "/workspace",
			SetupScript:       plan.SetupScript,
			Mounts:            plan.Mounts,
			User:              plan.User,
		}, nil
	}
}
