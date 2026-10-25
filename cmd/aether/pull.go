package main

import (
	"errors"
	"fmt"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:    "pull",
		short:   "fetch a run's branches into the linked clones (no merge)",
		usage:   "aether pull <run-id>",
		example: "aether pull run-d6ay38tj8j",
		run:     runPull,
	})
}

func runPull(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: aether pull <run>")
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	var (
		repos  []protocol.RunRepo
		coords []protocol.RunPullResult
	)
	if err = withControl(func(c *protocol.Client) error {
		run, getErr := getRun(c, args[0])
		if getErr != nil {
			return getErr
		}
		repos = run.Repos
		if len(repos) == 0 {
			repos = []protocol.RunRepo{{Primary: true}}
		}
		for _, repo := range repos {
			params := protocol.RunPullParams{RunID: args[0]}
			if !repo.Primary {
				params.Repo = repo.WorkspaceID
			}
			var result protocol.RunPullResult
			if callErr := c.Call(protocol.MethodRunPull, params, &result); callErr != nil {
				return callErr
			}
			coords = append(coords, result)
		}
		return nil
	}); err != nil {
		return err
	}
	var errs []error
	for i, pull := range coords {
		prefix := ""
		if len(repos) > 1 {
			prefix = repos[i].Name + ": "
		}
		repo, repoErr := localops.RepoFor(cfg, pull.WorkspaceID)
		if repo == "" {
			missing := fmt.Errorf("no clone of workspace %s is linked; in your clone of it run: %s",
				pull.WorkspaceID, linkRepoCommand(cfg, pull.WorkspaceID, "."))
			if repoErr != nil {
				missing = fmt.Errorf("%w; %w", repoErr, missing)
			}
			if repos[i].Primary {
				errs = append(errs, fmt.Errorf("%s%w", prefix, missing))
			} else {
				fmt.Printf("%snot pulled: %v\n", prefix, missing)
			}
			continue
		}
		result, pullErr := localops.Pull(repo, cfg.User, cfg.GitHost(), pull)
		switch {
		case pullErr != nil:
			errs = append(errs, fmt.Errorf("%s%w", prefix, pullErr))
		case result.Current:
			fmt.Printf("%sYou are on %s in %s; fast-forwarded.\n", prefix, result.Branch, repo)
		default:
			fmt.Printf("%sBranch %s is ready. Switch with: git -C %s switch %s\n",
				prefix, result.Branch, shellquote.Quote(repo), shellquote.Quote(result.Branch))
		}
	}
	return errors.Join(errs...)
}
