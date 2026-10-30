package collab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

const agentDeliveryTimeout = 10 * time.Second

func (s *Service) deliverResult(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run, force bool, approver domain.MemberID, deliveryProof *control.Snapshot, claimAdmission func(func() error) error, steer bool) (Result, error) {
	var (
		claimed bool
		stored  *store.RoomMessage
	)
	claim := func() error {
		var err error
		claimed, err = s.cfg.Store.ClaimRoomMessage(ctx, msg.ID, s.now(), force, string(approver))
		if err != nil || claimed {
			return err
		}
		stored, err = s.cfg.Store.GetRoomMessage(ctx, msg.ID)
		return err
	}
	if claimAdmission != nil {
		if err := claimAdmission(claim); err != nil {
			return Result{}, err
		}
	} else if err := claim(); err != nil {
		return Result{}, err
	}
	if !claimed {
		if stored == nil {
			var err error
			stored, err = s.cfg.Store.GetRoomMessage(ctx, msg.ID)
			if err != nil {
				return Result{}, err
			}
		}
		return Result{Message: stored, Receipt: receiptForState(stored.State)}, nil
	}
	// Do not publish the uncertain claim before attempting PTY delivery. A
	// publication outage must never turn an otherwise unattempted steer into
	// a durable "uncertain" result.
	receipt, outcome, deliveryErr := s.deliver(ctx, msg, actor, run, s.deliveryAdmission(run.ID, deliveryProof), steer)
	if deliveryErr != nil {
		return Result{}, deliveryErr
	}
	// The terminal may already hold the message, so recording it must not
	// depend on the caller staying connected.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentDeliveryTimeout)
	defer cancel()
	return s.settleResult(settleCtx, msg, receipt, outcome)
}

func (s *Service) settleResult(ctx context.Context, msg *store.RoomMessage, receipt Receipt, outcome string) (Result, error) {
	if err := s.settleRoomMessage(ctx, msg, receipt, outcome == acphost.OutcomeQueued); err != nil {
		if errors.Is(err, store.ErrConflict) {
			stored, getErr := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
			if getErr != nil {
				return Result{}, getErr
			}
			return Result{Message: stored, Receipt: receiptForState(stored.State)}, nil
		}
		return Result{}, err
	}
	stored, err := s.cfg.Store.GetRoomMessage(ctx, msg.ID)
	if err != nil {
		return Result{}, err
	}
	return Result{Message: stored, Receipt: receipt, Outcome: outcome}, nil
}

// deliveryAdmission holds the same run admission lock used by controller
// takeover and fencing. An immediate delivery also proves the exact member,
// session, and generation that made it eligible.
func (s *Service) deliveryAdmission(run domain.RunID, deliveryProof *control.Snapshot) func(func() error) error {
	switch {
	case s.cfg.Control == nil:
		return func(fn func() error) error { return fn() }
	case deliveryProof != nil:
		return func(fn func() error) error {
			return s.cfg.Control.AdmitMember(string(run), deliveryProof.MemberID, deliveryProof.SessionID, deliveryProof.Generation, fn)
		}
	default:
		return func(fn func() error) error { return s.cfg.Control.Admit(string(run), fn) }
	}
}

// deliver rechecks mutable membership and workspace policy inside admit. The
// callback must not call back into Control.
func (s *Service) deliver(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run, admit func(func() error) error, steer bool) (Receipt, string, error) {
	var attempted, revoked bool
	var outcome string
	accept := func() error {
		freshRun, freshActor, ws, err := s.scope(ctx, msg.WorkspaceID, msg.RunID, msg.ActorID)
		if err != nil {
			if errors.Is(err, ErrNotMember) || errors.Is(err, store.ErrNotFound) {
				revoked = true
				return nil
			}
			return err
		}
		if freshRun.Protected {
			revoked = true
			return nil
		}
		if permissions.Check(permissions.Steer,
			permissions.Actor{ID: freshActor.ID, Role: freshActor.Role},
			permissions.Target{Workspace: ws.ID, Owner: freshRun.MemberID, Protected: freshRun.Protected, SteerOthers: ws.SteerOthers}) != nil {
			revoked = true
			return nil
		}
		// The initial values are retained only for low-level test injectors;
		// production canonical injection receives the freshly authorized actor.
		actor, run = freshActor, freshRun
		attempted = true
		outcome, err = s.inject(ctx, msg, actor, run, steer)
		return err
	}
	err := admit(accept)
	if revoked {
		return ReceiptNotSent, "", nil
	}
	if err != nil {
		if !attempted && errors.Is(err, control.ErrStale) {
			return ReceiptNotSent, "", nil
		}
		if !attempted {
			return "", "", err
		}
		return ClassifyReceipt(err), "", nil
	}
	return ReceiptSent, outcome, nil
}

func (s *Service) inject(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run, steer bool) (string, error) {
	prompt := domain.AgentPrompt{Text: msg.Body, Attachments: msg.Attachments, MessageID: msg.ID}
	if s.cfg.Inject == nil {
		return "", ErrNoInjector
	}
	return s.cfg.Inject(ctx, run.ID, actor.ID, prompt, steer, func(err error) {
		s.agentDelivered(context.WithoutCancel(ctx), msg.ID, err)
	})
}

func (s *Service) agentDelivered(ctx context.Context, id string, deliveryErr error) {
	ctx, cancel := context.WithTimeout(ctx, agentDeliveryTimeout)
	defer cancel()
	var failure *store.RoomMessageFailure
	switch {
	case errors.Is(deliveryErr, acphost.ErrClosed):
		failure = &store.RoomMessageFailure{Code: "agent_disconnected", Message: deliveryErr.Error()}
	case deliveryErr != nil:
		failure = &store.RoomMessageFailure{Code: "agent_refused", Message: deliveryErr.Error()}
	}
	changed, err := s.cfg.Store.SettleRoomMessageAgentDelivery(ctx, id, failure)
	if err == nil && changed {
		var stored *store.RoomMessage
		if stored, err = s.cfg.Store.GetRoomMessage(ctx, id); err == nil {
			err = s.publishMessage(ctx, stored)
		}
	}
	if err != nil {
		slog.Warn("collab: record the agent's delivery of a queued message", "message", id, "error", err)
	}
}

func (s *Service) dropAgentQueued(ctx context.Context) error {
	ids, err := s.cfg.Store.DropAgentQueuedRoomMessages(ctx, &store.RoomMessageFailure{Code: "agent_disconnected", Message: acphost.ErrClosed.Error()})
	if err != nil {
		return fmt.Errorf("collab: settle messages queued before the restart: %w", err)
	}
	for _, id := range ids {
		msg, err := s.cfg.Store.GetRoomMessage(ctx, id)
		if err != nil {
			return fmt.Errorf("collab: read dropped message %s: %w", id, err)
		}
		if err := s.publishMessage(ctx, msg); err != nil {
			return fmt.Errorf("collab: publish dropped message %s: %w", id, err)
		}
	}
	return nil
}

// settleAgentQueued settles the steers an earlier server process left queued
// behind an agent turn: delivered when the agent's keeper reported they
// reached the agent, sent again when the session was adopted and they did
// not, and not sent when the session was restored. It leaves those of a
// paused run queued and reports whether there are any. settled holds the
// steers earlier calls settled, which a redelivery can leave queued.
func (s *Service) settleAgentQueued(ctx context.Context, settled map[string]bool) (undecided bool, err error) {
	queued, err := s.cfg.Store.ListAgentQueuedRoomMessages(ctx, s.bootAt)
	if err != nil {
		return true, fmt.Errorf("collab: list messages queued before the restart: %w", err)
	}
	for _, msg := range queued {
		if settled[msg.ID] {
			continue
		}
		delivered, state := s.cfg.Reattached(msg.RunID)
		if state == Undecided {
			undecided = true
			continue
		}
		settled[msg.ID] = true
		switch {
		case state == Restored:
			s.agentDelivered(ctx, msg.ID, acphost.ErrClosed)
		case slices.Contains(delivered, msg.ID):
			s.agentDelivered(ctx, msg.ID, nil)
		default:
			s.redeliver(ctx, msg)
		}
	}
	return undecided, nil
}

// redeliver sends a steer that never reached the agent to its adopted
// session again, as the next prompt.
func (s *Service) redeliver(ctx context.Context, msg *store.RoomMessage) {
	run, err := s.cfg.Runs.GetRun(ctx, msg.RunID)
	var actor *domain.Member
	if err == nil {
		actor, err = s.cfg.Runs.GetMember(ctx, msg.ActorID)
	}
	var receipt Receipt
	var outcome string
	if err == nil {
		receipt, outcome, err = s.deliver(ctx, msg, actor, run, s.deliveryAdmission(run.ID, nil), false)
	}
	switch {
	case err != nil:
		s.agentDelivered(ctx, msg.ID, fmt.Errorf("collab: send again after the restart: %w", err))
	case receipt != ReceiptSent:
		s.agentDelivered(ctx, msg.ID, acphost.ErrClosed)
	case outcome != acphost.OutcomeQueued:
		s.agentDelivered(ctx, msg.ID, nil)
	}
}

// DeliverDue delivers every bounded page of overdue steer requests. It is
// safe to call on every process restart because queued rows remain durable.
func (s *Service) DeliverDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > store.MaxCollaborationPageSize {
		limit = store.MaxCollaborationPageSize
	}
	workspaces := s.cfg.Workspaces
	if workspaces == nil {
		if ws, ok := s.cfg.Runs.(Workspaces); ok {
			workspaces = ws
		}
	}
	if workspaces == nil {
		return 0, nil
	}
	list, err := workspaces.ListWorkspaces(ctx)
	if err != nil {
		return 0, fmt.Errorf("list workspaces for overdue delivery: %w", err)
	}
	total := 0
	var sweepErrs []error
	for _, ws := range list {
		before := ""
		for {
			page, err := s.cfg.Store.ListDueSteerRequests(ctx, ws.ID, s.now(), before, limit)
			if err != nil {
				return total, fmt.Errorf("list overdue room messages for workspace %q: %w", ws.ID, err)
			}
			for _, msg := range page.Items {
				run, err := s.cfg.Runs.GetRun(ctx, msg.RunID)
				if err != nil {
					sweepErrs = append(sweepErrs, fmt.Errorf("load overdue run %q for message %q: %w", msg.RunID, msg.ID, err))
					continue
				}
				if _, err := s.deliverResult(ctx, msg, nil, run, false, "", nil, nil, false); err != nil {
					sweepErrs = append(sweepErrs, fmt.Errorf("deliver overdue message %q: %w", msg.ID, err))
					continue
				}
				total++
			}
			if page.NextBefore == "" || len(page.Items) == 0 {
				break
			}
			before = page.NextBefore
		}
	}
	if len(sweepErrs) != 0 {
		return total, errors.Join(sweepErrs...)
	}
	return total, nil
}
