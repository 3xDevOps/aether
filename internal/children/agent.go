package children

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// MaxTaskBytes bounds a child's task text.
const MaxTaskBytes = 64 << 10

const startNext = "The child's report arrives in your inbox and wakes you: end your turn or report waiting --for agents. Do not poll it."

// Capabilities are the child methods run may call now.
func (s *Service) Capabilities(ctx context.Context, run domain.RunID) ([]string, error) {
	p, err := s.resolve(ctx, run)
	if err != nil {
		return nil, err
	}
	caps := []string{protocol.MethodChildOptions, protocol.MethodChildList, protocol.MethodChildCancel,
		protocol.MethodChildDetach, protocol.MethodChildFetch}
	if p.refusal == "" {
		caps = append(caps, protocol.MethodChildStart)
	}
	return caps, nil
}

func (s *Service) HandleAgent(ctx context.Context, run domain.RunID, method string, raw json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodChildOptions:
		p, perr := decode[protocol.ChildOptionsParams](raw)
		if perr != nil {
			return nil, perr
		}
		return s.options(ctx, run, p)
	case protocol.MethodChildStart:
		p, perr := decode[protocol.ChildStartParams](raw)
		if perr != nil {
			return nil, perr
		}
		return s.start(ctx, run, p)
	case protocol.MethodChildList:
		p, perr := decode[protocol.ChildListParams](raw)
		if perr != nil {
			return nil, perr
		}
		return s.list(ctx, run, p)
	case protocol.MethodChildCancel, protocol.MethodChildDetach, protocol.MethodChildFetch:
		p, perr := decode[protocol.ChildRunParams](raw)
		if perr != nil {
			return nil, perr
		}
		c, perr := s.ownChild(ctx, run, p.RunID, method)
		if perr != nil {
			return nil, perr
		}
		switch method {
		case protocol.MethodChildCancel:
			return s.cancelChild(ctx, c)
		case protocol.MethodChildDetach:
			return s.detach(ctx, c)
		}
		return s.fetch(ctx, c)
	}
	return nil, &protocol.Error{Code: protocol.CodeMethodNotFound, Message: "method not found: " + method}
}

func decode[T any](raw json.RawMessage) (T, *protocol.Error) {
	var p T
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, invalid("invalid params: %s", err.Error())
	}
	return p, nil
}

func (s *Service) options(ctx context.Context, run domain.RunID, req protocol.ChildOptionsParams) (protocol.ChildOptionsResult, error) {
	p, err := s.resolve(ctx, run)
	if err != nil {
		return protocol.ChildOptionsResult{}, rpcErr(protocol.MethodChildOptions, err)
	}
	counts, err := s.cfg.Store.ChildCounts(ctx, p.treeKey)
	if err != nil {
		return protocol.ChildOptionsResult{}, rpcErr(protocol.MethodChildOptions, err)
	}
	out := protocol.ChildOptionsResult{
		Allowed: p.refusal == "", Reason: p.refusal, Scope: p.scope, OwnerID: string(p.owner), Depth: p.depth,
		Limits: limitsWire(p.limits), Busy: counts.Busy, Queued: counts.Queued, Total: counts.Total,
		Choices: []protocol.ChildChoice{},
	}
	if p.refusal != "" {
		return out, nil
	}
	if out.Choices, err = s.choices(ctx, p); err != nil {
		return protocol.ChildOptionsResult{}, rpcErr(protocol.MethodChildOptions, err)
	}
	if req.Harness != "" {
		for i, c := range out.Choices {
			if c.Harness != req.Harness || c.Model != "" {
				continue
			}
			models, err := s.cfg.Runs.AgentModels(ctx, p.owner, domain.MemberID(c.AccountMemberID), c.Harness, false)
			if err != nil {
				return protocol.ChildOptionsResult{}, rpcErr(protocol.MethodChildOptions, err)
			}
			for _, m := range models.Models {
				out.Choices[i].Models = append(out.Choices[i].Models, m.ID)
			}
		}
	}
	return out, nil
}

func (s *Service) start(ctx context.Context, run domain.RunID, req protocol.ChildStartParams) (protocol.ChildReceipt, error) {
	const method = protocol.MethodChildStart
	req.Purpose = trimPurpose(req.Purpose)
	switch {
	case req.Harness == "":
		return protocol.ChildReceipt{}, invalid("harness is required")
	case req.Purpose == "":
		return protocol.ChildReceipt{}, invalid("purpose is required: 3-8 words the dashboard shows")
	case strings.TrimSpace(req.Task) == "":
		return protocol.ChildReceipt{}, invalid("task is required: the child sees only this text")
	case len(req.Task) > MaxTaskBytes:
		return protocol.ChildReceipt{}, invalid("task exceeds %d bytes", MaxTaskBytes)
	case req.IdempotencyKey == "" || len(req.IdempotencyKey) > protocol.CoordMaxIdempotencyKeyBytes || strings.ContainsAny(req.IdempotencyKey, "\x00\r\n"):
		return protocol.ChildReceipt{}, invalid("idempotency_key is required and must be one line of at most %d bytes", protocol.CoordMaxIdempotencyKeyBytes)
	}
	if prior, err := s.priorStart(ctx, run, req); prior != nil || err != nil {
		return s.receipt(ctx, prior, err)
	}
	p, err := s.resolve(ctx, run)
	if err != nil {
		return protocol.ChildReceipt{}, rpcErr(method, err)
	}
	if p.refusal != "" {
		if p.off && !p.parent.ChildRefused {
			if markErr := s.cfg.Store.MarkChildRefused(ctx, run); markErr != nil {
				return protocol.ChildReceipt{}, rpcErr(method, markErr)
			}
			s.publish(ctx, run, "")
		}
		return protocol.ChildReceipt{}, denied("%s: %s", method, p.refusal)
	}
	if loop, err := s.loops(ctx, p, req); err != nil || loop != "" {
		if err != nil {
			return protocol.ChildReceipt{}, rpcErr(method, err)
		}
		return protocol.ChildReceipt{}, &protocol.Error{Code: protocol.CodeConflict, Message: method + ": " + loop}
	}
	spec, perr := s.choose(ctx, p, req)
	if perr != nil {
		return protocol.ChildReceipt{}, perr
	}
	if err := s.admit(ctx, p, spec.account); err != nil {
		return protocol.ChildReceipt{}, rpcErr(method, err)
	}
	base, err := s.cfg.Git.PublishRunBranch(ctx, p.parent.ID)
	if err != nil {
		return protocol.ChildReceipt{}, rpcErr(method, fmt.Errorf("publish the parent's branch: %w", err))
	}
	c := &domain.ChildReservation{
		ParentRunID: p.parent.ID, RootRunID: p.root, TreeKey: p.treeKey, WorkspaceID: p.parent.WorkspaceID,
		OwnerID: p.owner, AccountID: spec.account, Harness: spec.harness, Mode: spec.mode, Model: spec.model,
		Purpose: req.Purpose, Task: req.Task, Depth: p.depth, BaseCommit: base, BaseBranch: p.parent.Branch,
		Fallback: spec.fallback, IdempotencyKey: req.IdempotencyKey,
	}
	if p.mission != nil {
		c.MissionID = p.mission.ID
	}
	reserved, replay, err := s.cfg.Store.ReserveChild(ctx, c, p.limits)
	if err != nil {
		return protocol.ChildReceipt{}, rpcErr(method, err)
	}
	if replay && !sameStart(reserved, req) {
		return protocol.ChildReceipt{}, idempotencyConflict(req.IdempotencyKey)
	}
	s.Kick()
	return s.receipt(ctx, reserved, nil)
}

// priorStart returns the reservation a retried start already made, so a
// retry never re-runs policy that may since have changed.
func (s *Service) priorStart(ctx context.Context, run domain.RunID, req protocol.ChildStartParams) (*domain.ChildReservation, error) {
	kids, err := s.cfg.Store.ListChildren(ctx, run, false)
	if err != nil {
		return nil, err
	}
	for _, c := range kids {
		if c.IdempotencyKey == req.IdempotencyKey {
			if !sameStart(c, req) {
				return nil, idempotencyConflict(req.IdempotencyKey)
			}
			return c, nil
		}
	}
	return nil, nil
}

func sameStart(c *domain.ChildReservation, req protocol.ChildStartParams) bool {
	return c.Harness == req.Harness && c.Task == req.Task && c.Purpose == req.Purpose &&
		(req.Model == "" || c.Model == req.Model) &&
		(req.AccountMemberID == "" || string(c.AccountID) == req.AccountMemberID) &&
		(req.Mode == "" || c.Mode == modeNames[req.Mode])
}

func idempotencyConflict(key string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("child.start: idempotency key %q already started a different child; use a new key", key)}
}

func (s *Service) receipt(ctx context.Context, c *domain.ChildReservation, err error) (protocol.ChildReceipt, error) {
	if err != nil {
		return protocol.ChildReceipt{}, rpcErr(protocol.MethodChildStart, err)
	}
	child, err := s.wire(ctx, c)
	if err != nil {
		return protocol.ChildReceipt{}, rpcErr(protocol.MethodChildStart, err)
	}
	next := startNext
	if child.State == protocol.ChildQueued {
		next = "Queued until a slot frees in this tree (aether-internal child options shows the limits). " + next
	}
	return protocol.ChildReceipt{Child: child, Next: next}, nil
}

// loops refuses the same agent on the exact task of the parent or an
// ancestor.
func (s *Service) loops(ctx context.Context, p *policy, req protocol.ChildStartParams) (string, error) {
	task := strings.TrimSpace(req.Task)
	for r := p.parent; r != nil; {
		if r.Harness == req.Harness && strings.TrimSpace(r.Task) == task {
			return fmt.Sprintf("run %s already runs %s on this exact task; a child must narrow it", r.ID, req.Harness), nil
		}
		if r.ParentRunID == "" || r.MissionRole != "" {
			return "", nil
		}
		next, err := s.cfg.Store.GetRun(ctx, r.ParentRunID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		r = next
	}
	return "", nil
}

// admit checks the owner may launch on account and the budget admits them,
// as every launch does.
func (s *Service) admit(ctx context.Context, p *policy, account domain.MemberID) error {
	s.cfg.AuthorizationMu.Lock()
	_, err := s.cfg.Authorize(ctx, s.cfg.Store, p.owner, string(account))
	s.cfg.AuthorizationMu.Unlock()
	if err != nil {
		return err
	}
	if s.cfg.Cost != nil {
		if cost := s.cfg.Cost(); cost != nil {
			return cost.Admit(ctx, p.parent.WorkspaceID, p.owner)
		}
	}
	return nil
}

func (s *Service) list(ctx context.Context, run domain.RunID, req protocol.ChildListParams) (protocol.ChildListResult, error) {
	if req.RunID != "" {
		c, perr := s.ownChild(ctx, run, req.RunID, protocol.MethodChildList)
		if perr != nil {
			return protocol.ChildListResult{}, perr
		}
		child, err := s.wire(ctx, c)
		if err != nil {
			return protocol.ChildListResult{}, rpcErr(protocol.MethodChildList, err)
		}
		return protocol.ChildListResult{Children: []protocol.Child{child}}, nil
	}
	kids, err := s.cfg.Store.ListChildren(ctx, run, false)
	if err != nil {
		return protocol.ChildListResult{}, rpcErr(protocol.MethodChildList, err)
	}
	out := protocol.ChildListResult{Children: make([]protocol.Child, 0, len(kids))}
	for _, c := range kids {
		child, err := s.wire(ctx, c)
		if err != nil {
			return protocol.ChildListResult{}, rpcErr(protocol.MethodChildList, err)
		}
		out.Children = append(out.Children, child)
	}
	return out, nil
}

// ownChild is run's direct child id, or a not-found error naming it.
func (s *Service) ownChild(ctx context.Context, run domain.RunID, id, method string) (*domain.ChildReservation, *protocol.Error) {
	c, err := s.cfg.Store.GetChild(ctx, domain.RunID(id))
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.ParentRunID != run) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: run %s is not a child of run %s", method, id, run)}
	}
	if err != nil {
		return nil, rpcErr(method, err)
	}
	return c, nil
}

func (s *Service) cancelChild(ctx context.Context, c *domain.ChildReservation) (protocol.Child, error) {
	if err := s.stop(ctx, c, ""); err != nil {
		return protocol.Child{}, rpcErr(protocol.MethodChildCancel, err)
	}
	return s.wireOrErr(ctx, c.RunID, protocol.MethodChildCancel)
}

// stop cancels a queued child or kills a started one; actor is the human
// who asked, or empty for the parent or the server.
func (s *Service) stop(ctx context.Context, c *domain.ChildReservation, actor domain.MemberID) error {
	switch c.State {
	case domain.ChildQueued:
		err := s.cfg.Store.SetChildState(ctx, c.RunID, domain.ChildCancelled, "cancelled before it started", domain.ChildQueued)
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
		fresh, getErr := s.cfg.Store.GetChild(ctx, c.RunID)
		if getErr != nil {
			return getErr
		}
		return s.stop(ctx, fresh, actor)
	case domain.ChildLaunching, domain.ChildStarted:
		r, err := s.cfg.Store.GetRun(ctx, c.RunID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && r.Status.Terminal()) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.cfg.Runs.Kill(ctx, c.RunID, actor)
	}
	return nil
}

func (s *Service) detach(ctx context.Context, c *domain.ChildReservation) (protocol.Child, error) {
	if err := s.cfg.Store.DetachChild(ctx, c.RunID); err != nil {
		return protocol.Child{}, rpcErr(protocol.MethodChildDetach, err)
	}
	s.publish(ctx, c.RunID, "")
	return s.wireOrErr(ctx, c.RunID, protocol.MethodChildDetach)
}

func (s *Service) fetch(ctx context.Context, c *domain.ChildReservation) (protocol.ChildFetchResult, error) {
	const method = protocol.MethodChildFetch
	if c.State != domain.ChildStarted {
		return protocol.ChildFetchResult{}, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf("%s: child %s has not started", method, c.RunID)}
	}
	if _, err := s.cfg.Git.PublishRunBranch(ctx, c.RunID); err != nil {
		return protocol.ChildFetchResult{}, rpcErr(method, fmt.Errorf("publish the child's branch: %w", err))
	}
	ref := "refs/remotes/children/" + string(c.RunID)
	commit, err := s.cfg.Git.FetchRunBranch(ctx, c.RunID, c.ParentRunID, ref)
	if err != nil {
		return protocol.ChildFetchResult{}, rpcErr(method, err)
	}
	return protocol.ChildFetchResult{Ref: ref, Commit: commit}, nil
}

func (s *Service) wireOrErr(ctx context.Context, id domain.RunID, method string) (protocol.Child, error) {
	c, err := s.cfg.Store.GetChild(ctx, id)
	if err != nil {
		return protocol.Child{}, rpcErr(method, err)
	}
	child, err := s.wire(ctx, c)
	if err != nil {
		return protocol.Child{}, rpcErr(method, err)
	}
	return child, nil
}

// wire describes c with its run's live state, its latest report and whether
// its parent moved past the commit it started from.
func (s *Service) wire(ctx context.Context, c *domain.ChildReservation) (protocol.Child, error) {
	out := protocol.Child{
		RunID: string(c.RunID), ParentRunID: string(c.ParentRunID), RootRunID: string(c.RootRunID), Depth: c.Depth,
		Purpose: c.Purpose, Harness: c.Harness, Mode: modeName(c.Mode), Model: c.Model, OwnerID: string(c.OwnerID),
		AccountMemberID: string(c.AccountID), Detached: c.Detached, BaseCommit: c.BaseCommit, Error: c.Error,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
	}
	switch c.State {
	case domain.ChildQueued:
		out.State = protocol.ChildQueued
	case domain.ChildLaunching:
		out.State = protocol.ChildStarting
	case domain.ChildCancelled:
		out.State = protocol.ChildCancelled
	case domain.ChildRefused:
		out.State = protocol.ChildRefused
	default:
		r, err := s.cfg.Store.GetRun(ctx, c.RunID)
		if errors.Is(err, store.ErrNotFound) {
			out.State = protocol.ChildEnded
			break
		}
		if err != nil {
			return protocol.Child{}, err
		}
		out.State, out.Branch, out.AccountMemberID = childState(r), r.Branch, string(r.AccountMember())
	}
	if parent, err := s.cfg.Store.GetRun(ctx, c.ParentRunID); err == nil && parent.LastCommit != "" && c.BaseCommit != "" {
		out.Stale = parent.LastCommit != c.BaseCommit
	}
	if report, err := s.cfg.Store.LatestCoordReport(ctx, c.RunID); err == nil && report != nil {
		out.Report = &protocol.ChildReport{Outcome: string(report.Outcome), Summary: report.Summary, At: report.CreatedAt.UTC().Format(time.RFC3339)}
		if out.State == protocol.ChildWorking || out.State == protocol.ChildWaiting {
			switch report.Outcome {
			case store.CoordOutcomeSuccess:
				out.State = protocol.ChildFinished
			case store.CoordOutcomeFailure:
				out.State = protocol.ChildFailed
			}
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return protocol.Child{}, err
	}
	costs, err := s.cfg.Store.ListRunCosts(ctx, c.RunID)
	if err != nil {
		return protocol.Child{}, err
	}
	var total float64
	metered := false
	for _, row := range costs {
		if row.Metered {
			metered, total = true, total+row.CostUSD
		}
	}
	if metered {
		out.CostUSD = &total
	}
	return out, nil
}

func childState(r *domain.Run) string {
	switch {
	case r.Status == domain.RunQueued || r.Status == domain.RunProvisioning:
		return protocol.ChildStarting
	case r.Status == domain.RunFailed:
		return protocol.ChildFailed
	case r.Status.Terminal():
		return protocol.ChildEnded
	}
	switch r.Activity {
	case domain.ActivityFinished, domain.ActivityAnswered:
		return protocol.ChildFinished
	case domain.ActivityFailed:
		return protocol.ChildFailed
	case domain.ActivityNeedsYou:
		return protocol.ChildNeedsYou
	case domain.ActivityWaiting, domain.ActivityWaitingAgents, domain.ActivityIdle:
		return protocol.ChildWaiting
	}
	return protocol.ChildWorking
}
