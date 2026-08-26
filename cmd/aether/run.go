package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "run",
		short: "launch an agent run",
		run:   runRun,
	})
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "harness name")
	mode := fs.String("mode", "tui", "tui or headless")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	params := kvFlag{}
	fs.Var(params, "param", "value for a template parameter, name=value (repeatable)")

	// A template launch carries no prompt, so the leading positional
	// argument is optional here.
	task := ""
	if len(args) > 0 && args[0] != "--" && !strings.HasPrefix(args[0], "-") {
		task, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if task == "" && fs.NArg() == 1 {
		task = fs.Arg(0)
	}
	if *template != "" {
		if task != "" || *agent != "" {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt and --agent")
		}
		return launchTemplate(*workspace, *template, params)
	}
	if *mode != "tui" && *mode != "headless" {
		return fmt.Errorf("invalid mode %q (want tui or headless)", *mode)
	}
	// A taskless launch drops you into the agent's interactive TUI. Headless
	// has no interactive surface, so it still needs a prompt.
	if *agent == "" || fs.NArg() > 1 || (task == "" && *mode == "headless") {
		return fmt.Errorf("usage: aether run [\"task\"] --agent <name> [--mode tui|headless] [--workspace]\n   (a task is required with --mode headless)\n   or: aether run --template <name> [--param k=v] [--workspace]")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: wsID,
			Task:        task,
			Harness:     *agent,
			Mode:        *mode,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}
