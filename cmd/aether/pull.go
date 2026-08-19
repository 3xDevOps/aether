package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/3xDevOps/Aether/internal/cli"
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
	if err := withControl(func(c *protocol.Client) error {
		return c.Call(protocol.MethodRunPull, protocol.RunIDParams{RunID: args[0]}, &coords)
	}); err != nil {
		return err
	}
	if coords.Branch == "" {
		return fmt.Errorf("run has no branch")
	}
	url := cli.GitURL(cfg.User, cfg.Addr, coords.WorkspaceID)
	refspec := "+refs/heads/" + coords.Branch + ":refs/remotes/aether/" + coords.Branch
	cmd := exec.Command("git", "-C", cfg.Repo, "fetch", "--no-tags", url, refspec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Printf("fetched %s into refs/remotes/aether/%s (not merged)\n", coords.Branch, coords.Branch)
	return nil
}
