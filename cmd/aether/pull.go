package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/localops"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "pull",
		short: "fetch a run's branch into the linked repo (no merge)",
		run:   runPull,
	})
}

func runPull(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether pull <run>")
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	if cfg.Repo == "" {
		return fmt.Errorf("no linked repo; re-run aether link <addr> --repo <path>")
	}
	var coords protocol.RunPullResult
	if err = withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: args[0]}, &coords)
	}); err != nil {
		return err
	}
	result, err := localops.Pull(cfg.Repo, cfg.User, cfg.Addr, coords)
	if err != nil {
		return err
	}
	if result.Current {
		fmt.Printf("You are on %s; fast-forwarded.\n", result.Branch)
	} else {
		fmt.Printf("Branch %s is ready. Switch with: git switch %s\n", result.Branch, result.Branch)
	}
	return nil
}
