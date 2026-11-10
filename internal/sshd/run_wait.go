package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
	"github.com/3xDevOps/Aether/internal/watch"
)

// RunWaitService reads and cancels the conditions runs wait on. Satisfied
// by *watch.Service, the same value as Services.Watches.
type RunWaitService interface {
	RunWaitsOf(ctx context.Context, runs []domain.RunID) (map[domain.RunID][]protocol.WaitCondition, error)
	CancelRunWait(ctx context.Context, run domain.RunID, wait string, actor domain.MemberID) (protocol.WaitCondition, error)
}

func init() {
	registerGuarded(protocol.MethodRunWaitCancel, permissions.Steer, runTarget, (*Server).runWaitCancel)
}

func (s *Server) runWaits() RunWaitService {
	w, _ := s.cfg.Services.Watches.(RunWaitService)
	return w
}

// fillWaitConditions sets each of runs' latest wait conditions on out, the
// snapshots of runs in the same order, in one read. A condition naming a
// session or a repository member does not reach is left out. Unless member
// owns the run or administers its workspace, the rest are redacted.
func (s *Server) fillWaitConditions(ctx context.Context, member domain.MemberID, runs []*domain.Run, out []protocol.Run) error {
	svc := s.runWaits()
	if svc == nil || len(runs) == 0 {
		return nil
	}
	ids := make([]domain.RunID, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	byRun, err := svc.RunWaitsOf(ctx, ids)
	if err != nil {
		return fmt.Errorf("read the runs' wait conditions: %w", err)
	}
	actor, actorErr := resolveActor(ctx, s.cfg.Store, member)
	prs := false
	for _, waits := range byRun {
		prs = prs || slices.ContainsFunc(waits, func(w protocol.WaitCondition) bool { return w.Repo != "" })
	}
	visible, err := s.waitVisibility(ctx, actor, prs)
	if err != nil {
		return err
	}
	for i, r := range runs {
		waits := slices.DeleteFunc(byRun[r.ID], func(w protocol.WaitCondition) bool { return !visible(w, r.MemberID) })
		if r.MemberID != member && (actorErr != nil || !workspaceAdmin(actor, r.WorkspaceID)) {
			for j := range waits {
				redactWait(&waits[j])
			}
		}
		out[i].WaitConditions = waits
	}
	return nil
}

// waitVisibility reports which conditions actor may read, given waiter,
// the member whose session registered the condition: a run_finished
// condition whose session they reach in every workspace, and a pull
// request condition whose repository is the origin or mirror source of a
// workspace actor reaches. A pull request condition whose repository no
// workspace has now, its origin changed or cleared since, only waiter
// reads. prs says whether it will be asked about pull request conditions.
func (s *Server) waitVisibility(ctx context.Context, actor permissions.Actor, prs bool) (func(w protocol.WaitCondition, waiter domain.MemberID) bool, error) {
	if actor.Role == domain.RoleSuperAdmin {
		return func(protocol.WaitCondition, domain.MemberID) bool { return true }, nil
	}
	repos := map[string][]domain.WorkspaceID{}
	if prs {
		workspaces, err := s.cfg.Store.ListWorkspaces(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the workspaces of the runs' wait conditions: %w", err)
		}
		mirrors, err := s.cfg.Store.ListWorkspaceMirrors(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the mirrors of the runs' wait conditions: %w", err)
		}
		add := func(url string, ws domain.WorkspaceID) {
			if repo := strings.ToLower(protocol.GitHubRemoteRepository(url)); repo != "" {
				repos[repo] = append(repos[repo], ws)
			}
		}
		for _, w := range workspaces {
			add(w.Origin, w.ID)
		}
		for _, m := range mirrors {
			add(m.SourceURL, m.WorkspaceID)
		}
	}
	runs := newRunScopes(s.cfg.Store)
	return func(w protocol.WaitCondition, waiter domain.MemberID) bool {
		if w.RunID != "" && !runs.reaches(ctx, actor, domain.RunID(w.RunID)) {
			return false
		}
		if w.Repo == "" {
			return true
		}
		if !prs {
			return false
		}
		if owners := repos[w.Repo]; len(owners) > 0 {
			return slices.ContainsFunc(owners, actor.Reaches)
		}
		return waiter != "" && waiter == actor.ID
	}, nil
}

// storedWait is wait id's condition as waitVisibility judges it, the run
// that waits on it and that run's member; ok is false when no run waits
// on id.
func (s *Server) storedWait(ctx context.Context, id string) (cond protocol.WaitCondition, owner domain.RunID, waiter domain.MemberID, ok bool, err error) {
	waits, has := s.cfg.Store.(store.RunWaitStore)
	if !has || id == "" {
		return cond, "", "", false, nil
	}
	w, err := waits.GetRunWait(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return cond, "", "", false, nil
	}
	if err != nil {
		return cond, "", "", false, err
	}
	return protocol.WaitCondition{ID: w.ID, Repo: w.Repo, RunID: string(w.TargetRun)}, w.OwnerRunID, w.MemberID, true, nil
}

// redactWait leaves out of w what only the run's owner and admins read:
// what its reads found with the owner's GitHub login and the free text of
// how it ended and how its wake failed. Its condition, state, outcome and
// a member's cancel stay.
func redactWait(w *protocol.WaitCondition) {
	w.Observed, w.Summary, w.Detail, w.LastError, w.LastErrorAt = protocol.WaitObserved{}, "", "", "", ""
	w.LastDeliveryError = ""
	if w.EndedCause != protocol.WaitCancelled {
		w.EndedCause = ""
	}
	if !strings.Contains(w.Spec, "@") {
		w.HeadSHA = ""
	}
}

// seesWaitDetails reports whether member reads run's wait conditions in
// full: its owner and its workspace's admins do.
func (s *Server) seesWaitDetails(ctx context.Context, member domain.MemberID, run *domain.Run) bool {
	if member == run.MemberID {
		return true
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	return err == nil && workspaceAdmin(actor, run.WorkspaceID)
}

func workspaceAdmin(actor permissions.Actor, ws domain.WorkspaceID) bool {
	return permissions.Check(permissions.WorkspaceAdmin, actor, permissions.Target{Workspace: ws}) == nil
}

func (s *Server) runWaitCancel(ctx context.Context, member domain.MemberID, params json.RawMessage) (any, *protocol.Error) {
	svc := s.runWaits()
	if svc == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "watch service not configured"}
	}
	p, perr := decodeParams[protocol.RunWaitCancelParams](params)
	if perr != nil {
		return nil, perr
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return nil, rpcError(err)
	}
	stored, owner, waiter, ok, err := s.storedWait(ctx, p.WaitID)
	if err != nil {
		return nil, rpcError(err)
	}
	if ok && owner == domain.RunID(p.RunID) {
		var visible func(protocol.WaitCondition, domain.MemberID) bool
		if visible, err = s.waitVisibility(ctx, actor, stored.Repo != ""); err != nil {
			return nil, rpcError(err)
		}
		if !visible(stored, waiter) {
			return nil, rpcError(fmt.Errorf("store: wait %s: %w", p.WaitID, store.ErrNotFound))
		}
	}
	w, err := svc.CancelRunWait(ctx, domain.RunID(p.RunID), p.WaitID, member)
	if errors.Is(err, watch.ErrInvalid) {
		return nil, invalidParams(err.Error())
	}
	if err != nil {
		return nil, rpcError(err)
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	if !s.seesWaitDetails(ctx, member, run) {
		redactWait(&w)
	}
	return protocol.RunWaitCancelResult{Wait: w}, nil
}
