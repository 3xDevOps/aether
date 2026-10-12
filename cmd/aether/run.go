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
	if len(args) > 1 && args[0] == "switch" && !strings.HasPrefix(args[1], "-") {
		return runSwitch(args[1:])
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name")
	mode := fs.String("mode", "standard", launchModeHelp)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	account := fs.String("account", "", "member ID whose shared agent account to use")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	cachedBase := fs.String("cached-base", "", "retry a launch from an exact accepted base commit")
	fallbackOrder := fs.String("fallback", "", fallbackHelp)
	noFallback := fs.Bool("no-fallback", false, noFallbackHelp)
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
		if task != "" || *agent != "" || *account != "" || *cachedBase != "" || *fallbackOrder != "" || *noFallback {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt, --agent, --account, --cached-base, --fallback, and --no-fallback")
		}
		return launchTemplate(*workspace, *template, params)
	}
	wireMode, err := parseLaunchMode(*mode)
	if err != nil {
		return err
	}
	*mode = wireMode
	fallbacks, err := parseFallback(*fallbackOrder, *noFallback)
	if err != nil {
		return err
	}
	if *agent == "" || fs.NArg() > 1 || (task == "" && *mode == "headless") {
		return fmt.Errorf("usage: aether run [\"task\"] --agent <name> [--mode standard|enhanced|background] [--workspace] [--account <member-id>] [--fallback <member-id>,... | --no-fallback] [--cached-base <full-sha>]\n   (a task is required with --mode background)\n   or: aether run --template <name> [--param k=v] [--workspace]\n   or: aether run switch <run-id> --mode standard|enhanced")
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		var res protocol.RunResult
		params := launchParams(wsID, task, *agent, *mode, *account, *cachedBase)
		params.FallbackAccountMemberIDs, params.NoFallback = fallbacks, *noFallback
		if err := c.Call(protocol.MethodRunLaunch, params, &res); err != nil {
			return cachedBaseLaunchError(err, params)
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

const (
	fallbackHelp   = "member IDs to launch on, in order, when the account's usage is exhausted, comma separated (default: every account you may use, most usage left first)"
	noFallbackHelp = "launch only on the requested account, even when its usage is exhausted"
)

func parseFallback(order string, none bool) ([]string, error) {
	if order == "" {
		return nil, nil
	}
	if none {
		return nil, errors.New("--fallback and --no-fallback cannot be used together")
	}
	ids := strings.Split(order, ",")
	for i, id := range ids {
		ids[i] = strings.TrimSpace(id)
		if ids[i] == "" {
			return nil, fmt.Errorf("--fallback %q has an empty member ID", order)
		}
	}
	return ids, nil
}

func setupOutput(err error) string {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || len(rpcErr.Data) == 0 {
		return ""
	}
	var failure protocol.SetupFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil {
		return ""
	}
	return failure.SetupOutput
}

// cachedBaseLaunchError turns a structured base-capture refusal into an
// explicit, copyable retry without changing ordinary RPC error handling.
func cachedBaseLaunchError(err error, params protocol.RunLaunchParams) error {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr == nil || len(rpcErr.Data) == 0 {
		return err
	}
	var failure protocol.MirrorFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil || failure.AcceptedCommit == "" {
		return err
	}
	args := []string{
		"aether", "run",
		"--agent", shellquote.Quote(params.Harness),
		"--mode", modeName(params.Mode),
		"--workspace", shellquote.Quote(params.WorkspaceID),
		"--account", shellquote.Quote(params.AccountMemberID),
	}
	if len(params.FallbackAccountMemberIDs) > 0 {
		args = append(args, "--fallback", shellquote.Quote(strings.Join(params.FallbackAccountMemberIDs, ",")))
	}
	if params.NoFallback {
		args = append(args, "--no-fallback")
	}
	command := strings.Join(append(args,
		"--cached-base", shellquote.Quote(failure.AcceptedCommit),
		"--",
		shellquote.Quote(params.Task),
	), " ")
	return fmt.Errorf("%s\nretry from cached %s with:\n  %s",
		rpcErr.Message, failure.AcceptedCommit, command)
}
