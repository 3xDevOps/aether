package sshd

import (
	"context"
	"io"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
)

// GitTransport is the SSH server's view of the git engine (*gitengine.Engine).
type GitTransport interface {
	UploadPack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
	ReceivePack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
}

// PTYAttacher is the SSH server's view of the PTY host (*ptyhost.Host).
type PTYAttacher interface {
	Attach(ctx context.Context, key ptyhost.SessionKey, member domain.MemberID, cols, rows uint, readOnly bool, conn io.ReadWriter, resize <-chan [2]uint) error
}

// RunController is the SSH server's view of the scheduler (*scheduler.Scheduler).
type RunController interface {
	Launch(ctx context.Context, workspace domain.WorkspaceID, member domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error)
	Kill(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Pause(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Resume(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	// Paused reports whether the run's container is currently frozen;
	// unknown or finished runs report false.
	Paused(run domain.RunID) bool
	Inject(ctx context.Context, run domain.RunID, actor domain.MemberID, message string) error
	CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error
	Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error)
	EnsureRunShellTab(ctx context.Context, run domain.RunID, tab string, cols, rows uint) error
}
