package server

import (
	"context"
	"io"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/gitengine"
)

// lazyGit wraps the git engine so the first touch of a workspace's repo -
// git transport or run checkout - creates the bare repo on demand
// (InitWorkspaceRepo is idempotent). Workspaces created while the server
// is running therefore work without a restart; both call sites (sshd and
// the scheduler) have already validated the workspace against the store
// before reaching these methods.
type lazyGit struct {
	*gitengine.Engine
}

func (g lazyGit) UploadPack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if _, err := g.InitWorkspaceRepo(ctx, ws); err != nil {
		return 0, err
	}
	return g.Engine.UploadPack(ctx, ws, stdin, stdout, stderr)
}

func (g lazyGit) ReceivePack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if _, err := g.InitWorkspaceRepo(ctx, ws); err != nil {
		return 0, err
	}
	return g.Engine.ReceivePack(ctx, ws, stdin, stdout, stderr)
}

func (g lazyGit) CreateRunCheckout(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, task string) (string, string, error) {
	if _, err := g.InitWorkspaceRepo(ctx, ws); err != nil {
		return "", "", err
	}
	return g.Engine.CreateRunCheckout(ctx, ws, run, baseBranch, task)
}
