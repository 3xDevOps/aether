package scheduler

import (
	"context"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// GitEngine is the scheduler's view of the git engine (*gitengine.Engine).
type GitEngine interface {
	CreateRunCheckout(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseBranch, task string) (checkoutPath, branch string, err error)
	WorkspaceBranchExists(ctx context.Context, ws domain.WorkspaceID, branch string) (bool, error)
	CommitAll(ctx context.Context, run domain.RunID, message string) (commit string, err error)
	PublishRunBranch(ctx context.Context, run domain.RunID) (commit string, err error)
	RemoveRunCheckout(ctx context.Context, run domain.RunID) error
	StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error
	StopDiffWatch(run domain.RunID)
	LastFileChange(run domain.RunID) (time.Time, bool)
}

// PTYHost is the scheduler's view of the PTY host (*ptyhost.Host).
type PTYHost interface {
	StartSession(ctx context.Context, key ptyhost.SessionKey, att runtime.Attachment) error
	StopSession(ctx context.Context, key ptyhost.SessionKey) error
	RemoveRunTranscripts(ctx context.Context, run domain.RunID) error
	StopSessionsWithPrefix(ctx context.Context, prefix string)
	ActiveSessions(prefix string) []ptyhost.SessionKey
	LastOutput(key ptyhost.SessionKey) (time.Time, bool)
	Inject(ctx context.Context, key ptyhost.SessionKey, actorName, actorColor, message string) error
}
