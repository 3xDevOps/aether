package collab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/store"
)

func (s *Service) deliverResult(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run, force bool, approver domain.MemberID, deliveryProof *control.Snapshot, claimAdmission func(func() error) error) (Result, error) {
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
	receipt, deliveryErr := s.deliver(ctx, msg, actor, run, deliveryProof)
	if deliveryErr != nil {
		return Result{}, deliveryErr
	}
	if err := s.settleRoomMessage(ctx, msg, receipt); err != nil {
		if errors.Is(err, store.ErrConflict) {
			var getErr error
			stored, getErr = s.cfg.Store.GetRoomMessage(ctx, msg.ID)
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
	return Result{Message: stored, Receipt: receipt}, nil
}

// deliver rechecks mutable membership and workspace policy while holding the
// same run admission lock used by controller takeover and fencing. An
// immediate delivery also proves the exact member, session, and generation
// that made it eligible. The callback must not call back into Control.
func (s *Service) deliver(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run, deliveryProof *control.Snapshot) (Receipt, error) {
	var attempted, revoked bool
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
		if err := permissions.Check(permissions.Steer,
			permissions.Actor{ID: freshActor.ID, Role: freshActor.Role},
			permissions.Target{Workspace: ws.ID, Owner: freshRun.MemberID, Protected: freshRun.Protected, SteerOthers: ws.SteerOthers}); err != nil {
			revoked = true
			return nil
		}
		// The initial values are retained only for low-level test injectors;
		// production canonical injection receives the freshly authorized actor.
		actor, run = freshActor, freshRun
		attempted = true
		return s.inject(ctx, msg, actor, run)
	}
	var err error
	if s.cfg.Control != nil {
		if deliveryProof != nil {
			err = s.cfg.Control.AdmitMember(string(run.ID), deliveryProof.MemberID, deliveryProof.SessionID, deliveryProof.Generation, accept)
		} else {
			err = s.cfg.Control.Admit(string(run.ID), accept)
		}
	} else {
		err = accept()
	}
	if revoked {
		return ReceiptNotSent, nil
	}
	if err != nil {
		if deliveryProof != nil && errors.Is(err, control.ErrStale) {
			return ReceiptNotSent, nil
		}
		if !attempted {
			return "", err
		}
		return ClassifyReceipt(err), nil
	}
	return ReceiptSent, nil
}

func (s *Service) inject(ctx context.Context, msg *store.RoomMessage, actor *domain.Member, run *domain.Run) error {
	message := serializeAgentMessage(msg.Body, msg.Attachments)
	if s.cfg.Inject == nil {
		return ErrNoInjector
	}
	return s.cfg.Inject(ctx, run.ID, actor.ID, message)
}

// serializeAgentMessage keeps the body and validated container-visible
// attachment references in one bounded, unambiguous agent-facing message.
func serializeAgentMessage(body string, attachments []string) string {
	if len(attachments) == 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body) + len(attachments)*DefaultAttachmentBytes + 64)
	b.WriteString(body)
	b.WriteString("\n\n--- AETHER ATTACHMENTS ---\n")
	for _, ref := range attachments {
		b.WriteString("- ")
		b.WriteString(ref)
		b.WriteByte('\n')
	}
	b.WriteString("--- END AETHER ATTACHMENTS ---")
	return b.String()
}

func (s *Service) agentMessageLimit() int {
	const prefix = "\n\n--- AETHER ATTACHMENTS ---\n"
	const suffix = "--- END AETHER ATTACHMENTS ---"
	return s.cfg.BodyLimit + len(prefix) + len(suffix) + s.cfg.AttachmentLimit*(s.cfg.AttachmentBytes+3)
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
			page, err := s.cfg.Store.ListRoomMessages(ctx, ws.ID, "", before, limit)
			if err != nil {
				return total, fmt.Errorf("list overdue room messages for workspace %q: %w", ws.ID, err)
			}
			for _, msg := range page.Items {
				if msg.Kind != store.RoomMessageSteerRequest || msg.State != store.RoomMessageQueued || msg.DeliverAfter != nil && msg.DeliverAfter.After(s.now()) {
					continue
				}
				run, err := s.cfg.Runs.GetRun(ctx, msg.RunID)
				if err != nil {
					sweepErrs = append(sweepErrs, fmt.Errorf("load overdue run %q for message %q: %w", msg.RunID, msg.ID, err))
					continue
				}
				if _, err := s.deliverResult(ctx, msg, nil, run, false, "", nil, nil); err != nil {
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
