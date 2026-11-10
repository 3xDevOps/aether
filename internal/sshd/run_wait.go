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
	visible, err := s.waitVisibility(ctx, actor, byRun)
	if err != nil {
		return err
	}
	for i, r := range runs {
		waits := slices.DeleteFunc(byRun[r.ID], func(w protocol.WaitCondition) bool { return !visible(w) })
		if r.MemberID != member && (actorErr != nil || !workspaceAdmin(actor, r.WorkspaceID)) {
			for j := range waits {
				redactWait(&waits[j])
			}
		}
		out[i].WaitConditions = waits
	}
	return nil
}

// waitVisibility reports which of byRun's conditions actor may read: a
// run_finished condition whose session they reach in every workspace, and
// a pull request condition unless its repository is the origin or mirror
// source only of workspaces actor does not reach.
func (s *Server) waitVisibility(ctx context.Context, actor permissions.Actor, byRun map[domain.RunID][]protocol.WaitCondition) (func(protocol.WaitCondition) bool, error) {
	if actor.Role == domain.RoleSuperAdmin {
		return func(protocol.WaitCondition) bool { return true }, nil
	}
	prs := false
	for _, waits := range byRun {
		prs = prs || slices.ContainsFunc(waits, func(w protocol.WaitCondition) bool { return w.Repo != "" })
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
	return func(w protocol.WaitCondition) bool {
		if w.RunID != "" && !runs.reaches(ctx, actor, domain.RunID(w.RunID)) {
			return false
		}
		owners := repos[w.Repo]
		return len(owners) == 0 || slices.ContainsFunc(owners, actor.Reaches)
	}, nil
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
