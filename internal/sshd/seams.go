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
	Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error
	// Replay streams a run's recorded transcript as raw terminal bytes;
	// os.ErrNotExist when the run never recorded one.
	Replay(run domain.RunID) (io.ReadCloser, error)
}

// RunController is the SSH server's view of the scheduler (*scheduler.Scheduler).
type RunController interface {
	Launch(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error)
	// ContainerAddr resolves the network address of a supervised run
	// container.
	ContainerAddr(ctx context.Context, run domain.RunID) (string, error)
	// TerminalContainerAddr resolves the network address of a member's
	// supervised environment terminal container.
	TerminalContainerAddr(ctx context.Context, member domain.MemberID) (string, error)
	Kill(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	DeleteRun(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Pause(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Resume(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	// Paused reports whether the run's container is currently frozen;
	// unknown or finished runs report false.
	Paused(run domain.RunID) bool
	Inject(ctx context.Context, run domain.RunID, actor domain.MemberID, message string) error
	// RecordHandoff credits the outgoing owner of a run as a steerer and
	// refreshes the co-author list its container reads. Called after the
	// run row already names the new owner.
	RecordHandoff(ctx context.Context, run domain.RunID, from domain.MemberID)
	// RefreshMemberCoAuthors rewrites the co-author list of every live run
	// that credits member, after their git identity changed.
	RefreshMemberCoAuthors(ctx context.Context, member domain.MemberID)
	CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error
	Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error)
	EnsureRunShellTab(ctx context.Context, run domain.RunID, tab string, cols, rows uint) error
	EnsureTerminal(ctx context.Context, member domain.MemberID) (*domain.Terminal, error)
	EnsureTerminalTab(ctx context.Context, member domain.MemberID, tab string, cols, rows uint) error
	StopTerminal(ctx context.Context, member domain.MemberID) error
	TerminalStatus(ctx context.Context, member domain.MemberID) (domain.TerminalStatus, error)
	SaveEnvironment(ctx context.Context, member domain.MemberID) (string, error)
	ResetEnvironment(ctx context.Context, member domain.MemberID) error
	// SaveTerminalImage writes validated image bytes to the target account's
	// persistent home and returns its absolute container-visible path.
	SaveTerminalImage(ctx context.Context, actor domain.MemberID, run domain.RunID, extension string, data []byte) (string, error)
	// ConnectGitHub finishes the GitHub login the member started with gh
	// auth login in their environment terminal.
	ConnectGitHub(ctx context.Context, member domain.MemberID) (domain.GitHubConnection, error)
	// ProbeGitHubCLI reports the gh in the member's environment terminal
	// before the member is asked to log in with it.
	ProbeGitHubCLI(ctx context.Context, member domain.MemberID) (domain.GitHubCLI, error)
	// HoldShell counts one live interactive terminal attach for the
	// self-update idle check; the returned func releases it.
	HoldShell() func()
}
