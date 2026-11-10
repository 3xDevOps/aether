package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// resolveRepo names the checkout and workspace a request's repo addresses.
// A run member cannot reach reads as missing, and an extra repository in a
// workspace they cannot reach as one the run does not have.
func (s *Server) resolveRepo(ctx context.Context, member domain.MemberID, run, workspace, repo string) (domain.RunID, domain.WorkspaceID, *protocol.Error) {
	if repo == "" {
		return domain.RunID(run), domain.WorkspaceID(workspace), nil
	}
	if run == "" {
		return "", "", invalidParams("repo requires run_id")
	}
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return "", "", rpcError(err)
	}
	r, err := s.cfg.Store.GetRun(ctx, domain.RunID(run))
	if err != nil {
		return "", "", rpcError(err)
	}
	if !actor.Reaches(r.WorkspaceID) {
		return "", "", rpcError(permissions.ErrNoAccess)
	}
	if workspace != "" && domain.WorkspaceID(workspace) != r.WorkspaceID {
		return "", "", invalidParams("workspace_id does not own run_id")
	}
	if domain.WorkspaceID(repo) == r.WorkspaceID {
		return r.ID, r.WorkspaceID, nil
	}
	extra, ok := r.ExtraRepo(domain.WorkspaceID(repo))
	if !ok || !actor.Reaches(extra.WorkspaceID) {
		return "", "", invalidParams("repo " + repo + " is not one of the session's repos")
	}
	return extra.Key, extra.WorkspaceID, nil
}
