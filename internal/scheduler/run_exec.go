package scheduler

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/3xDevOps/Aether/internal/control"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runtime"
)

// ErrRunPaused refuses a new process in a frozen container. Unlike the
// other ways a run has no live environment, it ends when the run resumes.
var ErrRunPaused = fmt.Errorf("%w: the run is paused", ErrNoLiveEnvironment)

// RunExec is one process a member's SSH connection starts in a run's
// container. It runs as the container's user, with its environment, in the
// run's checkout. None of its output is ever dropped, so the caller must
// read every stream.
type RunExec struct {
	Argv       []string
	Env        []string
	TTY        bool
	Cols, Rows uint
}

// CheckRunExec reports why p may not start a process in the run now. It is
// the admission StartRunExec applies: the development authorization shell
// tabs use, then a live, unpaused container still open to new surfaces.
func (s *Scheduler) CheckRunExec(ctx context.Context, id domain.RunID, p control.Principal, authorize func() error) error {
	_, err := s.admitRunExec(ctx, id, p, authorize)
	return err
}

func (s *Scheduler) admitRunExec(ctx context.Context, id domain.RunID, p control.Principal, authorize func() error) (LiveRun, error) {
	if err := s.developmentAuthorization(ctx, id, p, authorize); err != nil {
		return LiveRun{}, err
	}
	// Resolved as if paused were allowed, so a pause is named as one
	// instead of as a container that is stopping or gone.
	live, err := s.ResolveLiveRun(ctx, id, true)
	if err != nil {
		return LiveRun{}, err
	}
	if s.Paused(id) {
		return LiveRun{}, ErrRunPaused
	}
	if err = s.checkDevelopmentOpen(id, live.ContainerID); err != nil {
		return LiveRun{}, err
	}
	return live, nil
}

// StartRunExec starts spec as an owned execution, so stopping it reaps every
// process it started.
func (s *Scheduler) StartRunExec(ctx context.Context, id domain.RunID, p control.Principal, spec RunExec, authorize func() error) (runtime.ManagedExec, error) {
	live, err := s.admitRunExec(ctx, id, p, authorize)
	if err != nil {
		return nil, err
	}
	managed, ok := s.cfg.Runtime.(runtime.ManagedExecRuntime)
	if !ok {
		return nil, fmt.Errorf("%w: runtime does not support owned executions", runtime.ErrExecUnavailable)
	}
	exec := runtime.ExecSpec{
		Argv: spec.Argv, WorkingDir: live.Workdir, Env: spec.Env,
		Cols: spec.Cols, Rows: spec.Rows, CreationKey: "ssh-" + rand.Text(), Lossless: true,
	}
	if spec.TTY {
		return managed.StartExecTTY(ctx, live.ContainerID, exec)
	}
	return managed.StartExecPipe(ctx, live.ContainerID, exec)
}
