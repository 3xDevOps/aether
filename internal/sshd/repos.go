package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// resolveRepo names the checkout and workspace a request's repo addresses.
// A run member cannot reach in every workspace reads as missing.
func (s *Server) resolveRepo(ctx context.Context, member domain.MemberID, run, workspace, repo string) (domain.RunID, domain.WorkspaceID, *protocol.Error) {
	if repo == "" {
		return domain.RunID(run), domain.WorkspaceID(workspace), nil
	}
	if run == "" {
		return "", "", invalidParams("repo requires run_id")
	}
	r, err := s.reachableRun(ctx, member, domain.RunID(run))
	if err != nil {
		return "", "", rpcError(err)
	}
	if workspace != "" && domain.WorkspaceID(workspace) != r.WorkspaceID {
		return "", "", invalidParams("workspace_id does not own run_id")
	}
	if domain.WorkspaceID(repo) == r.WorkspaceID {
		return r.ID, r.WorkspaceID, nil
	}
	extra, ok := r.ExtraRepo(domain.WorkspaceID(repo))
	if !ok {
		return "", "", invalidParams("repo " + repo + " is not one of the session's repos")
	}
	return extra.Key, extra.WorkspaceID, nil
}
