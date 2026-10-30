// Package children lets a run start child runs on other agents and models,
// within the accounts, limits and budgets its owner already authorized.
package children

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Runs is the scheduler side of a child run.
type Runs interface {
	LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error)
	StopForParent(ctx context.Context, run, parent domain.RunID) error
	CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error
	Review(ctx context.Context, run domain.RunID, actor domain.MemberID, action string, reportedOnly bool) (*domain.Run, error)
	DefaultMode(ctx context.Context, member, account domain.MemberID, harnessName string) (domain.LaunchMode, error)
	FallbackLaunchable(ctx context.Context, member, requested, account domain.MemberID, harnessName string, mode domain.LaunchMode) (bool, error)
	AgentModels(ctx context.Context, member, account domain.MemberID, name string, refresh bool) (protocol.AgentModelsResult, error)
}

// Git publishes run branches and copies a child's into its parent.
type Git interface {
	PublishRunBranch(ctx context.Context, run domain.RunID) (string, error)
	FetchRunBranch(ctx context.Context, ws domain.WorkspaceID, branch string, into domain.RunID, ref string) (string, error)
}

// Agents lists the agents member can launch on account, as agent.list does.
type Agents interface {
	ListAgents(ctx context.Context, member, account domain.MemberID) ([]protocol.AgentInfo, error)
}

// Mailer delivers a server-written message from one run to another and
// wakes the recipient.
type Mailer interface {
	Notify(ctx context.Context, from, to domain.RunID, body, correlation, key string) error
}

// Admitter is the workspace budget gate.
type Admitter interface {
	Admit(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID) error
}

// Store is what the service reads and writes.
type Store interface {
	store.Store
	store.ChildStore
	GetMission(ctx context.Context, id domain.MissionID) (*domain.Mission, error)
}

type Config struct {
	Store Store
	Runs  Runs
	Git   Git
	// Agents and Cost resolve at call time: their owners are built after
	// this service, as is the Mailer SetMailer installs.
	Agents func() Agents
	Cost   func() Admitter
	Usage  fallback.QuotaReader
	// Bus carries the run status changes that wake the reconcile loop.
	Bus events.Bus
	// Authorize is sshd.AuthorizeLaunch; AuthorizationMu the mutex callers
	// hold across it and the launch's durable boundary.
	Authorize       func(ctx context.Context, st store.Store, actor domain.MemberID, account string) (domain.MemberID, error)
	AuthorizationMu *sync.Mutex
	// Interval is how often queued and ended children are reconciled.
	Interval time.Duration
	// Recovered closes once the scheduler recovered the runs a restart left,
	// so no child launch races that recovery.
	Recovered <-chan struct{}
}

type Service struct {
	cfg  Config
	kick chan struct{}

	mu   sync.Mutex
	mail Mailer
	// settingsMu serializes read-modify-write of member settings and
	// workspace limits.
	settingsMu sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Runs == nil || cfg.Git == nil || cfg.Authorize == nil || cfg.AuthorizationMu == nil {
		return nil, errors.New("children: store, runs, git and launch authorization are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 3 * time.Second
	}
	return &Service{cfg: cfg, kick: make(chan struct{}, 1)}, nil
}

// Start recovers reservations a restart interrupted, then reconciles until
// Close.
func (s *Service) Start(ctx context.Context) error {
	if err := s.recover(ctx); err != nil {
		return fmt.Errorf("children: recover reservations: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	var statuses <-chan events.Event
	if s.cfg.Bus != nil {
		sub, err := s.cfg.Bus.Subscribe(runCtx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeRunStatus}}})
		if err != nil {
			cancel()
			return fmt.Errorf("children: subscribe to run status: %w", err)
		}
		statuses = sub.Events()
	}
	go func() {
		defer close(done)
		if s.cfg.Recovered != nil {
			select {
			case <-s.cfg.Recovered:
			case <-runCtx.Done():
				return
			}
		}
		ticker := time.NewTicker(s.cfg.Interval)
		defer ticker.Stop()
		for {
			s.reconcile(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			case <-s.kick:
			case _, ok := <-statuses:
				if !ok {
					statuses = nil
				}
			}
		}
	}()
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

// SetMailer installs the coordination mailbox the parents' notices go to.
func (s *Service) SetMailer(m Mailer) {
	s.mu.Lock()
	s.mail = m
	s.mu.Unlock()
}

// Kick asks for a reconcile pass, as when a run's status changed.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// recover returns a launching reservation whose run never got a row to the
// queue, and marks one whose row exists started.
func (s *Service) recover(ctx context.Context) error {
	launching, err := s.cfg.Store.ChildrenInState(ctx, domain.ChildLaunching)
	if err != nil {
		return err
	}
	for _, c := range launching {
		to := domain.ChildQueued
		if _, getErr := s.cfg.Store.GetRun(ctx, c.RunID); getErr == nil {
			to = domain.ChildStarted
		}
		if err := s.cfg.Store.SetChildState(ctx, c.RunID, to, "", domain.ChildLaunching); err != nil {
			return err
		}
	}
	return nil
}

// repairStarted marks started a launching reservation whose run row
// exists, in case recording it at creation failed.
func (s *Service) repairStarted(ctx context.Context) error {
	launching, err := s.cfg.Store.ChildrenInState(ctx, domain.ChildLaunching)
	if err != nil {
		return err
	}
	for _, c := range launching {
		if _, getErr := s.cfg.Store.GetRun(ctx, c.RunID); getErr != nil {
			continue
		}
		if err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildStarted, "", domain.ChildLaunching); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (s *Service) reconcile(ctx context.Context) {
	if err := s.repairStarted(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("children: repair started reservations", "error", err)
	}
	if err := s.dispatch(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("children: dispatch failed", "error", err)
	}
	if err := s.settle(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("children: settle failed", "error", err)
	}
}

func (s *Service) notify(ctx context.Context, from, to domain.RunID, body, correlation, key string) error {
	s.mu.Lock()
	mail := s.mail
	s.mu.Unlock()
	if mail == nil {
		return errors.New("children: the coordination mailbox is not connected")
	}
	if err := mail.Notify(ctx, from, to, body, correlation, key); err != nil {
		slog.Warn("children: notify parent", "child", from, "parent", to, "error", err)
		return err
	}
	return nil
}

func denied(format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func rpcErr(method string, err error) *protocol.Error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	code := protocol.CodeInternal
	switch {
	case errors.Is(err, store.ErrConflict):
		code = protocol.CodeConflict
	case errors.Is(err, store.ErrNotFound):
		code = protocol.CodeNotFound
	}
	return &protocol.Error{Code: code, Message: method + ": " + err.Error()}
}

// publish tells subscribers a run's child-run fields changed.
func (s *Service) publish(ctx context.Context, run domain.RunID, actor domain.MemberID) {
	if s.cfg.Bus == nil {
		return
	}
	r, err := s.cfg.Store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("children: read run for its event", "run", run, "error", err)
		return
	}
	if _, err := s.cfg.Bus.Publish(ctx, events.Event{
		WorkspaceID: r.WorkspaceID, RunID: r.ID, ActorID: actor,
		Payload: events.RunChildrenPayload{Children: r.Children, Detached: r.Detached},
	}); err != nil {
		slog.Warn("children: publish run.children", "run", run, "error", err)
	}
}
