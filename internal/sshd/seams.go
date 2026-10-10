package sshd

import (
	"context"
	"errors"
	"io"

	"github.com/3xDevOps/Aether/internal/acphost"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/ptyhost"
	"github.com/3xDevOps/Aether/internal/scheduler"
)

// GitTransport is the SSH server's view of the git engine (*gitengine.Engine).
type GitTransport interface {
	UploadPack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
	ReceivePack(ctx context.Context, ws domain.WorkspaceID, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
}

// PTYAttacher is the SSH server's view of the PTY host (*ptyhost.Host).
type PTYAttacher interface {
	Attach(ctx context.Context, key ptyhost.SessionKey, client ptyhost.AttachClient, conn io.ReadWriter, resize <-chan [2]uint) error
	// Replay opens a finite retained transcript window with atomic metadata;
	// os.ErrNotExist when the run never recorded one.
	Replay(run domain.RunID) (ptyhost.ReplayWindow, error)
	// RecentReplay returns a bounded suffix of a run's recorded transcript.
	RecentReplay(run domain.RunID, maxBytes int) (ptyhost.ReplayWindow, error)
	// Snapshot returns the compact current screen for a finished-run attach.
	Snapshot(run domain.RunID) (ptyhost.ScreenSnapshot, error)
}

// PTYHistoryReader is separate from PTYAttacher so attach-only adapters stay
// small. Implementations return at most limit lines, oldest first.
type PTYHistoryReader interface {
	History(ctx context.Context, run domain.RunID, before, query string, limit int) (ptyhost.HistoryPage, error)
}

// RunLauncherWithOptions is implemented by schedulers that can pin a launch to
// a caller-supplied base observation.
type RunLauncherWithOptions interface {
	LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error)
}

var errLaunchOptionsUnsupported = errors.New("sshd: cached base retry is not supported")

func launchWithOptions(ctx context.Context, runs RunController, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	if launcher, ok := runs.(RunLauncherWithOptions); ok {
		return launcher.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, opts)
	}
	if opts.CachedBase != "" {
		return nil, errLaunchOptionsUnsupported
	}
	return runs.Launch(ctx, workspace, member, account, task, harness, mode)
}

// RunController is the SSH server's view of the scheduler (*scheduler.Scheduler).
type RunController interface {
	Launch(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode) (*domain.Run, error)
	// CheckSharedLaunch reports whether member's launch of harness on
	// account would be refused over the harness or the account owner's
	// login, and why, resolving it as Launch does.
	CheckSharedLaunch(ctx context.Context, member, account domain.MemberID, harness string) (scheduler.SharedLaunch, string, error)
	ContainerAddr(ctx context.Context, run domain.RunID) (string, error)
	TerminalContainerAddr(ctx context.Context, member domain.MemberID) (string, error)
	Kill(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Release(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	DeleteRun(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Pause(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	Resume(ctx context.Context, run domain.RunID, actor domain.MemberID) error
	// Paused reports whether the run's container is currently frozen;
	// unknown or finished runs report false.
	Paused(run domain.RunID) bool
	// Retention reads current runtime ownership without probing or mutating it.
	Retention(run *domain.Run) (scheduler.RetentionInfo, error)
	// PendingInputs returns an independent snapshot of unresolved requests;
	// unknown or terminated run lifetimes return an empty list.
	PendingInputs(run domain.RunID) []domain.RunInputRequest
	Inject(ctx context.Context, run domain.RunID, actor domain.MemberID, prompt domain.AgentPrompt, steer bool, delivered func(error)) (string, error)
	ReadImage(ctx context.Context, runID domain.RunID, reference string) ([]byte, string, error)
	ACPSubscribe(run domain.RunID, afterSeq int64) (scheduler.ACPStream, error)
	// ACPAnswer resolves a pending request of an enhanced run's agent; the
	// first answer wins. values is the form answer for an accepted question.
	ACPAnswer(run domain.RunID, requestID, optionID string, values map[string]any) error
	ACPCancel(ctx context.Context, run domain.RunID) error
	ACPSetOption(ctx context.Context, run domain.RunID, optionID string, value any) error
	// ACPHistory reads up to limit items before beforeSeq, oldest first;
	// zero reads from the newest.
	ACPHistory(run domain.RunID, beforeSeq int64, limit int) (acphost.HistoryPage, error)
	ACPItem(run domain.RunID, seq int64) (acphost.Item, error)
	AgentSwitchable(ctx context.Context, member, account domain.MemberID, harness string) (bool, error)
	SwitchMode(ctx context.Context, run domain.RunID, actor domain.MemberID, mode domain.LaunchMode, admit func(begin func() error) error) error
	Switching(run domain.RunID) domain.LaunchMode
	CloseRun(ctx context.Context, run domain.RunID, actor domain.MemberID, outcome domain.RunStatus) error
	Relaunch(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error)
	SetArchived(ctx context.Context, run domain.RunID, actor domain.MemberID, archived bool) (*domain.Run, error)
	// Seen clears the run's finish_unopened flag, and its outcome_unseen
	// flag when actor owns the run. Clearing a clear flag returns the run
	// unchanged.
	Seen(ctx context.Context, run domain.RunID, actor domain.MemberID) (*domain.Run, error)
	// RecordHandoff credits the outgoing owner of a run as a steerer and
	// refreshes the co-author list its container reads. Called after the
	// run row already names the new owner.
	RecordHandoff(ctx context.Context, run domain.RunID, from domain.MemberID)
	// RefreshMemberCoAuthors rewrites the co-author list of every live run
	// that credits member, after their git identity changed.
	RefreshMemberCoAuthors(ctx context.Context, member domain.MemberID)
	EnsureRunShellTab(ctx context.Context, run domain.RunID, tab string, cols, rows uint) error
	EnsureRunShellTabReserved(ctx context.Context, run domain.RunID, tab string, cols, rows uint) (ptyhost.ShellTabReservation, error)
	EnsureTerminal(ctx context.Context, member domain.MemberID) (*domain.Terminal, error)
	EnsureTerminalTab(ctx context.Context, member domain.MemberID, tab string, cols, rows uint) error
	StopTerminal(ctx context.Context, member domain.MemberID) error
	// WithStoppedTerminal excludes terminal recreation through afterStop.
	// The callback may lock member caches but must not reenter terminal lifecycle.
	WithStoppedTerminal(ctx context.Context, member domain.MemberID, afterStop func() error) error
	TerminalStatus(ctx context.Context, member domain.MemberID) (domain.TerminalStatus, error)
	SaveEnvironment(ctx context.Context, member domain.MemberID) (string, error)
	ResetEnvironment(ctx context.Context, member domain.MemberID) error
	// InstallAgent returns the end of the install command's output and its
	// exit code.
	InstallAgent(ctx context.Context, member domain.MemberID, command string) (string, int, error)
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
