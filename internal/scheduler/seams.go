package scheduler

import (
	"context"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/evidence"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/runtime"
)

type EvidenceService interface {
	Capture(context.Context, evidence.Request) (protocol.EvidencePacket, error)
	CaptureBeforeCleanup(context.Context, evidence.Request, func(context.Context) error) (protocol.EvidencePacket, error)
}

// EvidencePurger serializes explicit run deletion with evidence capture. The
// cleanup callback is invoked under the evidence service's per-run lock only
// after every packet artifact and manifest has been purged.
type EvidencePurger interface {
	PurgeRun(context.Context, domain.WorkspaceID, domain.RunID, func(context.Context) error) error
}

type GitEngine interface {
	CreateRunCheckoutAt(ctx context.Context, ws domain.WorkspaceID, run domain.RunID, baseCommit, baseBranch, task, origin string) (checkoutPath, branch string, err error)
	WorkspaceBranchExists(ctx context.Context, ws domain.WorkspaceID, branch string) (bool, error)
	WorkspaceBranchCommit(ctx context.Context, ws domain.WorkspaceID, branch string) (commit string, err error)
	CommitAll(ctx context.Context, run domain.RunID, message string, author domain.GitIdentity, signingKey []byte) (commit string, err error)
	PublishRunBranch(ctx context.Context, run domain.RunID) (commit string, err error)
	RemoveRunCheckout(ctx context.Context, run domain.RunID) error
	StartDiffWatch(ctx context.Context, workspace domain.WorkspaceID, run domain.RunID) error
	StopDiffWatch(run domain.RunID)
	LastFileChange(run domain.RunID) (time.Time, bool)
}

type PTYHost interface {
	StartSession(ctx context.Context, key ptyhost.SessionKey, att runtime.Attachment) error
	SessionGeneration(key ptyhost.SessionKey) uint64
	StopSession(ctx context.Context, key ptyhost.SessionKey) error
	RemoveRunTranscripts(ctx context.Context, run domain.RunID) error
	PruneTranscripts(ctx context.Context) error
	ItemLogPath(run domain.RunID) string
	StopSessionsWithPrefix(ctx context.Context, prefix string)
	ActiveSessions(prefix string) []ptyhost.SessionKey
	LastOutput(key ptyhost.SessionKey) (time.Time, bool)
	LastLine(ctx context.Context, run domain.RunID, wait time.Duration) (string, error)
	Inject(ctx context.Context, key ptyhost.SessionKey, actorName, actorColor, message, submit string) error
}
