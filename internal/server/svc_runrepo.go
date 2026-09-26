package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/runrepo"
	"github.com/3xDevOps/Aether/internal/scheduler"
	"github.com/3xDevOps/Aether/internal/sshd"
)

type runRepositoryService struct {
	native *runrepo.Service
	runs   *scheduler.Scheduler
}

var _ sshd.RunRepoService = (*runRepositoryService)(nil)

func (s *runRepositoryService) Native() *runrepo.Service { return s.native }

func (s *runRepositoryService) Execution(ctx context.Context, run domain.Run) (runrepo.Execution, error) {
	live, err := s.runs.ResolveLiveRun(ctx, run.ID, false)
	if err != nil {
		return runrepo.Execution{}, err
	}
	if live.Run.AccountMember() != run.AccountMember() {
		return runrepo.Execution{}, errors.New("run account changed; refresh before acting")
	}
	execution := runrepo.Execution{ContainerID: live.ContainerID, WorkDir: live.Workdir}
	execution.Authorize = func(ctx context.Context, _ bool) error {
		current, err := s.runs.ResolveLiveRun(ctx, run.ID, false)
		if err != nil {
			return err
		}
		if current.ContainerID != live.ContainerID || current.Workdir != live.Workdir || current.User != live.User || current.Run.AccountMember() != live.Run.AccountMember() {
			return errors.New("run execution or account changed; refresh before acting")
		}
		return nil
	}
	execution.CoAuthors = func(ctx context.Context, authorEmail string) ([]string, error) {
		current, err := s.runs.ResolveLiveRun(ctx, run.ID, false)
		if err != nil {
			return nil, err
		}
		return s.runs.RunCoAuthors(ctx, current.Run, authorEmail)
	}
	return execution, nil
}

func init() {
	registerService("run-repository", func(d Deps) (Service, error) {
		if d.SSH == nil || d.Runs == nil || d.Runtime == nil {
			return nil, fmt.Errorf("run repository: SSH, scheduler, and runtime are required")
		}
		// Exec inherits the existing run container's configured user, HOME,
		// credentials, signing configuration and environment. No GitHub command
		// is executed as the server process or in another member's container.
		d.SSH.Services.RunRepo = &runRepositoryService{native: runrepo.New(d.Runtime.Exec), runs: d.Runs}
		return nil, nil
	})
}
