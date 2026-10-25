package sshd

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// prDefaults fills a zero expected with the checkout's branch and head, and
// a zero target with the pull request the dashboard proposes
// (web/src/routes/diff/publish-pr.ts): the branch's upstream, else origin
// when the workspace sets one, into the run's base branch on the workspace
// source. A store failure is perr; repoErr is what the checkout cannot
// answer, for the result's error.
func (s *Server) prDefaults(ctx context.Context, call runRepoCall, expected *protocol.RunGitExpected, target *protocol.RunPRTarget) (perr *protocol.Error, repoErr error) {
	if *expected != (protocol.RunGitExpected{}) && *target != (protocol.RunPRTarget{}) {
		return nil, nil
	}
	state, err := call.native.Status(ctx, call.execution)
	if err != nil {
		return nil, err
	}
	if state.Branch == "" {
		return nil, fmt.Errorf("the checkout is on a detached HEAD at %s, so expected and target cannot default; pass both", state.Head)
	}
	if *expected == (protocol.RunGitExpected{}) {
		*expected = protocol.RunGitExpected{Branch: state.Branch, Head: state.Head}
	}
	if *target != (protocol.RunPRTarget{}) {
		return nil, nil
	}
	remotes, upstream, err := call.native.Remotes(ctx, call.execution, state.Branch)
	if err != nil {
		return nil, err
	}
	workspace, err := s.cfg.Store.GetWorkspace(ctx, call.repo.WorkspaceID)
	if err != nil {
		return rpcError(fmt.Errorf("default pr target: %w", err)), nil
	}
	source := ""
	mirror, err := s.cfg.Store.GetWorkspaceMirror(ctx, call.repo.WorkspaceID)
	switch {
	case err == nil:
		source = mirror.SourceURL
	case !errors.Is(err, store.ErrNotFound):
		return rpcError(fmt.Errorf("default pr target: %w", err)), nil
	}
	base := call.repo.BaseBranch
	if base == "" {
		base = workspace.BaseBranch
	}
	pr := protocol.DefaultPRTarget(protocol.DefaultPushTarget(remotes, upstream, state.Branch, workspace.Origin != ""), base, source)
	if pr.HeadRepository == "" || pr.HeadBranch == "" {
		return nil, fmt.Errorf("the checkout's branch %q has no GitHub push remote; pass target", state.Branch)
	}
	*target = pr
	return nil, nil
}
