package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
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
	account := fs.String("account", "", "member ID whose shared agent account to use")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	cachedBase := fs.String("cached-base", "", "retry a launch from an exact accepted base commit")
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
		if task != "" || *agent != "" || *account != "" || *cachedBase != "" {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt, --agent, --account, and --cached-base")
		}
		return launchTemplate(*workspace, *template, params)
	}
	if *mode != "tui" && *mode != "headless" {
		return fmt.Errorf("invalid mode %q (want tui or headless)", *mode)
	}
	// A taskless launch drops you into the agent's interactive TUI. Headless
	// has no interactive surface, so it still needs a prompt.
	if *agent == "" || fs.NArg() > 1 || (task == "" && *mode == "headless") {
		return fmt.Errorf("usage: aether run [\"task\"] --agent <name> [--mode tui|headless] [--workspace] [--account <member-id>] [--cached-base <full-sha>]\n   (a task is required with --mode headless)\n   or: aether run --template <name> [--param k=v] [--workspace]")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.RunResult
		if err := c.Call(protocol.MethodRunLaunch, launchParams(wsID, task, *agent, *mode, *account, *cachedBase), &res); err != nil {
			return cachedBaseLaunchError(err, task, *agent, *mode, wsID, *account)
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}

func launchParams(workspace, task, agent, mode, account, cachedBase string) protocol.RunLaunchParams {
	return protocol.RunLaunchParams{
		WorkspaceID:     workspace,
		Task:            task,
		Harness:         agent,
		Mode:            mode,
		AccountMemberID: account,
		CachedBase:      cachedBase,
	}
}

// cachedBaseLaunchError turns a structured base-capture refusal into an
// explicit, copyable retry without changing ordinary RPC error handling.
func cachedBaseLaunchError(err error, task, agent, mode, workspace, account string) error {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr == nil || len(rpcErr.Data) == 0 {
		return err
	}
	var failure protocol.MirrorFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil || failure.AcceptedCommit == "" {
		return err
	}
	command := strings.Join([]string{
		"aether", "run",
		"--agent", shellquote.Quote(agent),
		"--mode", shellquote.Quote(mode),
		"--workspace", shellquote.Quote(workspace),
		"--account", shellquote.Quote(account),
		"--cached-base", shellquote.Quote(failure.AcceptedCommit),
		"--",
		shellquote.Quote(task),
	}, " ")
	return fmt.Errorf("%s\nretry from cached %s with:\n  %s",
		rpcErr.Message, failure.AcceptedCommit, command)
}
