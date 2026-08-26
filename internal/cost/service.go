package cost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

// Store is the persistence the service needs: the cost tables, plus the
// run and member rows it attributes usage to and re-checks admission
// against.
type Store interface {
	store.CostStore
	GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error)
	GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error)
}

// Config wires the service. Both fields are required.
type Config struct {
	Store Store
	Bus   events.Bus
}

// Service records per-run usage from the bus, rolls it up per run,
// member, and workspace, and enforces workspace budgets at run
// admission.
type Service struct {
	store Store
	bus   events.Bus

	wg   sync.WaitGroup
	stop context.CancelFunc

	mu      sync.Mutex
	sub     events.Subscription
	closed  bool
	lastSeq uint64
	// state is the last budget state published per workspace, so only
	// transitions reach the bus.
	state map[domain.WorkspaceID]events.BudgetState
}

// New builds the service; call Start to begin consuming events.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Bus == nil {
		return nil, errors.New("cost: Store and Bus are required")
	}
	return &Service{
		store: cfg.Store,
		bus:   cfg.Bus,
		state: map[domain.WorkspaceID]events.BudgetState{},
	}, nil
}

// Start subscribes to the cost and lifecycle streams. ctx bounds only the
// setup; the service runs until Close.
func (s *Service) Start(ctx context.Context) error {
	sub, err := s.subscribe(ctx, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()

	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.stop = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.consume(loopCtx)
	}()
	return nil
}

// Close stops consuming and waits for the consumer to return. Idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sub := s.sub
	s.mu.Unlock()
	if s.stop != nil {
		s.stop()
	}
	if sub != nil {
		_ = sub.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Service) subscribe(ctx context.Context, replay bool) (events.Subscription, error) {
	opts := events.SubscribeOptions{
		Filter: events.Filter{Types: []events.Type{events.TypeRunCost, events.TypeRunStatus}},
		Replay: replay,
	}
	if replay {
		s.mu.Lock()
		opts.AfterSeq = s.lastSeq
		s.mu.Unlock()
	}
	sub, err := s.bus.Subscribe(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("cost: subscribe: %w", err)
	}
	return sub, nil
}

// consume dispatches bus events until the service is closed. Losing a
// run.cost event would understate a workspace's spend for good, so a
// detected drop resubscribes with replay from the last handled cursor.
func (s *Service) consume(ctx context.Context) {
	for {
		sub := s.subscription()
		if sub == nil {
			return
		}
		gap := false
		for e := range sub.Events() {
			if sub.Dropped() > 0 {
				gap = true
				break
			}
			s.handle(ctx, e)
		}
		if s.isClosed() || ctx.Err() != nil {
			return
		}
		if err := sub.Err(); err != nil {
			slog.Warn("cost: subscription failed; stopping consumer", "error", err)
			return
		}
		if gap {
			_ = sub.Close()
		}
		next, err := s.subscribe(ctx, true)
		if err != nil {
			// Without an event log there is nothing to replay; a plain
			// resubscription still keeps future runs accounted for.
			if next, err = s.subscribe(ctx, false); err != nil {
				if !errors.Is(err, events.ErrBusClosed) {
					slog.Warn("cost: resubscribe failed; stopping consumer", "error", err)
				}
				return
			}
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = next.Close()
			return
		}
		s.sub = next
		s.mu.Unlock()
	}
}

func (s *Service) handle(ctx context.Context, e events.Event) {
	switch p := e.Payload.(type) {
	case events.RunCostPayload:
		// Unmetered reports are this service's own marker for runs nobody
		// measured; only real measurements are recorded from the bus.
		if p.Metered {
			s.record(ctx, e.RunID, p)
		}
	case events.RunStatusPayload:
		if p.To.Terminal() {
			s.markUnmetered(ctx, e.RunID)
		}
	}
	s.mu.Lock()
	if e.Seq > s.lastSeq {
		s.lastSeq = e.Seq
	}
	s.mu.Unlock()
}

// record stores a metered result and re-evaluates the workspace's budget.
func (s *Service) record(ctx context.Context, run domain.RunID, p events.RunCostPayload) {
	r, err := s.store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("cost: cannot attribute usage", "run", run, "error", err)
		return
	}
	c := &store.RunCost{
		RunID:        r.ID,
		WorkspaceID:  r.WorkspaceID,
		MemberID:     r.MemberID,
		InputTokens:  p.InputTokens,
		OutputTokens: p.OutputTokens,
		CostUSD:      p.CostUSD,
		Metered:      true,
	}
	if err := s.store.PutRunCost(ctx, c); err != nil {
		slog.Warn("cost: record usage failed", "run", run, "error", err)
		return
	}
	s.announce(ctx, r.WorkspaceID)
}

// markUnmetered records a finished run nobody measured, and announces it
// with the documented unmetered signal so consumers can tell "we did not
// measure this" from "this cost nothing". A run that already has a record
// - metered or not - is left alone.
func (s *Service) markUnmetered(ctx context.Context, run domain.RunID) {
	if run == "" {
		return
	}
	_, err := s.store.GetRunCost(ctx, run)
	if err == nil {
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		slog.Warn("cost: read usage failed", "run", run, "error", err)
		return
	}
	r, err := s.store.GetRun(ctx, run)
	if err != nil {
		slog.Warn("cost: cannot attribute usage", "run", run, "error", err)
		return
	}
	if err := s.store.PutRunCost(ctx, &store.RunCost{
		RunID: r.ID, WorkspaceID: r.WorkspaceID, MemberID: r.MemberID,
	}); err != nil {
		slog.Warn("cost: record unmetered run failed", "run", run, "error", err)
		return
	}
	if _, err := s.bus.Publish(ctx, events.Event{
		WorkspaceID: r.WorkspaceID,
		RunID:       r.ID,
		Payload:     events.RunCostPayload{Metered: false},
	}); err != nil {
		slog.Warn("cost: publish unmetered signal failed", "run", run, "error", err)
	}
	s.announce(ctx, r.WorkspaceID)
}

// Report rolls a workspace's recorded usage up per member and per run.
func (s *Service) Report(ctx context.Context, workspace domain.WorkspaceID) (Report, error) {
	records, err := s.store.ListRunCosts(ctx, workspace)
	if err != nil {
		return Report{}, fmt.Errorf("cost: report %s: %w", workspace, err)
	}
	return Roll(workspace, records), nil
}

// Budget reports a workspace's budget and where its spend sits against it.
func (s *Service) Budget(ctx context.Context, workspace domain.WorkspaceID) (Status, error) {
	st, err := s.status(ctx, workspace)
	if err != nil {
		return Status{}, fmt.Errorf("cost: budget %s: %w", workspace, err)
	}
	return st, nil
}

// SetBudget applies an admin's change: a positive limit sets or replaces
// the budget, anything else clears it. The resulting state is published
// so the timeline and notifications see the edit.
func (s *Service) SetBudget(ctx context.Context, workspace domain.WorkspaceID, c Change, by domain.MemberID) (Status, error) {
	reason := fmt.Sprintf("budget set to $%.2f by %s", c.LimitUSD, by)
	if c.LimitUSD <= 0 {
		if err := s.store.DeleteWorkspaceBudget(ctx, workspace); err != nil {
			return Status{}, fmt.Errorf("cost: set budget %s: %w", workspace, err)
		}
		reason = fmt.Sprintf("budget cleared by %s", by)
	} else {
		b := &store.WorkspaceBudget{
			WorkspaceID: workspace,
			LimitUSD:    c.LimitUSD,
			WarnUSD:     c.WarnUSD,
			Override:    c.Override,
			UpdatedBy:   by,
		}
		if err := s.store.SetWorkspaceBudget(ctx, b); err != nil {
			return Status{}, fmt.Errorf("cost: set budget %s: %w", workspace, err)
		}
		if c.Override {
			reason += " (override on: the cap admits new runs)"
		}
	}
	st, err := s.status(ctx, workspace)
	if err != nil {
		return Status{}, fmt.Errorf("cost: set budget %s: %w", workspace, err)
	}
	s.publish(ctx, st, reason)
	return st, nil
}

// Admit reports whether member may start a new run in workspace. It
// re-checks the launch capability against a freshly read member row - the
// RPC boundary's check is not this service's to trust - and then the
// workspace's budget. A run already running is never affected: budgets
// gate admission only.
func (s *Service) Admit(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID) error {
	if err := s.checkLaunch(ctx, member); err != nil {
		return err
	}
	st, err := s.status(ctx, workspace)
	if err != nil {
		return fmt.Errorf("cost: admit run in %s: %w", workspace, err)
	}
	if st.Admits() {
		return nil
	}
	refusal := fmt.Sprintf("new run refused: $%.2f metered spend has reached the $%.2f cap",
		st.Spend.CostUSD, st.Budget.LimitUSD)
	s.publish(ctx, st, refusal)
	advisory := ""
	if st.Spend.Advisory() {
		advisory = fmt.Sprintf(" (%d of %d runs are unmetered, so the real spend is higher)",
			st.Spend.Unmetered, st.Spend.Runs)
	}
	return fmt.Errorf("%w: %w: workspace %s has spent $%.2f of its $%.2f cap%s; an admin can raise the cap or set an override with `aether budget set`",
		permissions.ErrDenied, ErrBudgetExceeded, workspace, st.Spend.CostUSD, st.Budget.LimitUSD, advisory)
}

// checkLaunch re-resolves the member and checks the launch capability, so
// a member demoted or removed since the request started cannot keep
// starting runs through this path.
func (s *Service) checkLaunch(ctx context.Context, member domain.MemberID) error {
	m, err := s.store.GetMember(ctx, member)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: member %s no longer exists", permissions.ErrDenied, member)
		}
		return fmt.Errorf("cost: resolve member %s: %w", member, err)
	}
	if m.Pending {
		return fmt.Errorf("%w: membership pending admin approval", permissions.ErrDenied)
	}
	if err := permissions.Check(permissions.Launch, permissions.Actor{ID: m.ID, Role: m.Role}, permissions.Target{}); err != nil {
		return err
	}
	return nil
}

// status reads a workspace's budget and current spend.
func (s *Service) status(ctx context.Context, workspace domain.WorkspaceID) (Status, error) {
	st := Status{Workspace: workspace, State: events.BudgetOK}
	b, err := s.store.GetWorkspaceBudget(ctx, workspace)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Status{}, err
	}
	st.Budget = b
	records, err := s.store.ListRunCosts(ctx, workspace)
	if err != nil {
		return Status{}, err
	}
	for _, c := range records {
		st.Spend.Add(c)
	}
	st.State = Evaluate(b, st.Spend)
	return st, nil
}

// announce publishes a workspace's budget state when it has moved since
// the last announcement, so ok -> warn -> exceeded is a signal and steady
// state is silence.
func (s *Service) announce(ctx context.Context, workspace domain.WorkspaceID) {
	st, err := s.status(ctx, workspace)
	if err != nil {
		slog.Warn("cost: budget evaluation failed", "workspace", workspace, "error", err)
		return
	}
	s.mu.Lock()
	changed := s.state[workspace] != st.State
	s.mu.Unlock()
	if !changed {
		return
	}
	s.publish(ctx, st, "")
}

func (s *Service) publish(ctx context.Context, st Status, reason string) {
	s.mu.Lock()
	s.state[st.Workspace] = st.State
	s.mu.Unlock()
	if _, err := s.bus.Publish(ctx, events.Event{
		WorkspaceID: st.Workspace,
		Payload:     st.payload(reason),
	}); err != nil {
		slog.Warn("cost: publish budget state failed", "workspace", st.Workspace, "error", err)
	}
}

func (s *Service) subscription() events.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.sub
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
