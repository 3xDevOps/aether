package sshd

import (
	"context"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// resolveRepo maps the repo param of a per-run request to the git engine's
// checkout key and the workspace that checkout belongs to. An empty repo, or
// the run's own workspace, is the run's primary checkout and leaves run and
// workspace as given.
func (s *Server) resolveRepo(ctx context.Context, run, workspace, repo string) (domain.RunID, domain.WorkspaceID, *protocol.Error) {
	if repo == "" {
		return domain.RunID(run), domain.WorkspaceID(workspace), nil
	}
	if run == "" {
		return "", "", invalidParams("repo requires run_id")
	}
	r, err := s.cfg.Store.GetRun(ctx, domain.RunID(run))
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
		return "", "", invalidParams("repo " + repo + " is not one of the run's repos")
	}
	return extra.Key, extra.WorkspaceID, nil
}
