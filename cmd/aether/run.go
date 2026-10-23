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
	model := fs.String("model", "", modelHelp)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	account := fs.String("account", "", "member ID whose shared agent account to use")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	cachedBase := fs.String("cached-base", "", "retry a launch from an exact accepted base commit")
	fallbackOrder := fs.String("fallback", "", fallbackHelp)
	noFallback := fs.Bool("no-fallback", false, noFallbackHelp)
	var repos stringList
	fs.Var(&repos, "repo", "also check out this workspace ID or name at /repos/<name> with its own run branch (repeatable)")
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
		if task != "" || *agent != "" || *model != "" || *account != "" || *cachedBase != "" || *fallbackOrder != "" || *noFallback || len(repos) > 0 {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt, --agent, --model, --account, --cached-base, --fallback, --no-fallback, and --repo")
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
		return fmt.Errorf("usage: aether run [\"task\"] --agent <name> [--mode standard|enhanced|background] [--model <model>] [--workspace] [--account <member-id>] [--fallback <member-id>,... | --no-fallback] [--cached-base <full-sha>] [--repo <workspace> ...]\n   (a task is required with --mode background)\n   or: aether run --template <name> [--param k=v] [--workspace]\n   or: aether run switch <run-id> --mode standard|enhanced")
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		wsID, err := pickWorkspace(list.Workspaces, *workspace)
		if err != nil {
			return err
		}
		var res protocol.RunResult
		params := launchParams(wsID, task, *agent, *mode, *model, *account, *cachedBase)
		params.FallbackAccountMemberIDs, params.NoFallback = fallbacks, *noFallback
		for _, repo := range repos {
			id, err := workspaceIDIn(list.Workspaces, repo)
			if err != nil {
				return fmt.Errorf("--repo %s: %w", repo, err)
			}
			params.ExtraWorkspaceIDs = append(params.ExtraWorkspaceIDs, id)
		}
		if err := c.Call(protocol.MethodRunLaunch, params, &res); err != nil {
			return cachedBaseLaunchError(err, params)
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}

func launchParams(workspace, task, agent, mode, model, account, cachedBase string) protocol.RunLaunchParams {
	return protocol.RunLaunchParams{
		WorkspaceID:     workspace,
		Task:            task,
		Harness:         agent,
		Mode:            mode,
		Model:           model,
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
	args := []string{"aether", "run", "--agent", shellquote.Quote(params.Harness), "--mode", modeName(params.Mode)}
	if params.Model != "" {
		args = append(args, "--model", shellquote.Quote(params.Model))
	}
	args = append(args,
		"--workspace", shellquote.Quote(params.WorkspaceID),
		"--account", shellquote.Quote(params.AccountMemberID),
	)
	if len(params.FallbackAccountMemberIDs) > 0 {
		args = append(args, "--fallback", shellquote.Quote(strings.Join(params.FallbackAccountMemberIDs, ",")))
	}
	if params.NoFallback {
		args = append(args, "--no-fallback")
	}
	for _, id := range params.ExtraWorkspaceIDs {
		args = append(args, "--repo", shellquote.Quote(id))
	}
	command := strings.Join(append(args,
		"--cached-base", shellquote.Quote(failure.AcceptedCommit),
		"--",
		shellquote.Quote(params.Task),
	), " ")
	return fmt.Errorf("%s\nretry from cached %s with:\n  %s",
		rpcErr.Message, failure.AcceptedCommit, command)
}
