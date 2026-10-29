package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:  "run",
		short: "launch an agent run",
		usage: "aether run <task> [flags]\n" +
			"aether run - [flags]\n" +
			"aether run --template <name> [--param <name=value>]... [--workspace <name>]\n" +
			"aether run switch <run-id> --mode standard|enhanced",
		example: "aether run \"fix the flaky login test\" --agent claude\n" +
			"aether run - --agent codex --idempotency-key fix-login-1 < task.md\n" +
			"aether run --template nightly-audit\n" +
			"aether run switch run-d6ay38tj8j --mode enhanced",
		flags: true,
		run:   runRun,
	})
}

func runRun(args []string) error {
	if len(args) > 1 && args[0] == "switch" && !strings.HasPrefix(args[1], "-") {
		return runSwitch(args[1:])
	}
	fs := newFlagSet("run")
	agent := fs.String("agent", "", "agent name")
	mode := fs.String("mode", "", launchModeHelp+" (default: "+defaultModeHelp+")")
	model := fs.String("model", "", modelHelp)
	effort := fs.String("effort", "", effortHelp)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	account := fs.String("account", "", "member ID whose shared agent account to use")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	cachedBase := fs.String("cached-base", "", "retry a launch from an exact accepted base commit")
	fallbackOrder := fs.String("fallback", "", fallbackHelp)
	noFallback := fs.Bool("no-fallback", false, noFallbackHelp)
	children := fs.String("children", "", "child runs this run's agent may start: on or off for this run only (default: your setting, aether children)")
	idempotencyKey := fs.String("idempotency-key", "", "the key a failed or interrupted launch printed: the retry returns that launch's run instead of starting another (default: a new key)")
	var repos stringList
	fs.Var(&repos, "repo", "also check out this workspace ID or name at /repos/<name> with its own run branch (repeatable)")
	params := kvFlag{}
	fs.Var(params, "param", "value for a template parameter, name=value (repeatable)")

	// A template launch carries no prompt, so the leading positional
	// argument is optional here.
	task := ""
	if len(args) > 0 && args[0] != "--" && (args[0] == "-" || !strings.HasPrefix(args[0], "-")) {
		task, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if task == "" && fs.NArg() == 1 {
		task = fs.Arg(0)
	}
	if *template != "" {
		if task != "" || *agent != "" || *model != "" || *effort != "" || *account != "" || *cachedBase != "" || *fallbackOrder != "" || *noFallback || len(repos) > 0 || *idempotencyKey != "" || *children != "" {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt, --agent, --model, --effort, --account, --cached-base, --fallback, --no-fallback, --repo, --children, and --idempotency-key")
		}
		return launchTemplate(*workspace, *template, params)
	}
	if *mode != "" {
		wireMode, err := parseLaunchMode(*mode)
		if err != nil {
			return err
		}
		*mode = wireMode
	}
	fallbacks, err := parseFallback(*fallbackOrder, *noFallback)
	if err != nil {
		return err
	}
	if *children != "" && *children != "on" && *children != "off" {
		return fmt.Errorf("invalid --children %q: want on or off", *children)
	}
	if *agent == "" || fs.NArg() > 1 || (task == "" && *mode == "headless") {
		return fmt.Errorf("usage: aether run [\"task\"|-] --agent <name> [--mode standard|enhanced|background] [--model <model>] [--effort <level>] [--workspace] [--account <member-id>] [--fallback <member-id>,... | --no-fallback] [--cached-base <full-sha>] [--repo <workspace> ...] [--children on|off] [--idempotency-key <key>]\n   (a task is required with --mode background; - reads it from stdin)\n   or: aether run --template <name> [--param k=v] [--workspace]\n   or: aether run switch <run-id> --mode standard|enhanced")
	}
	fromStdin := task == "-"
	task, err = stdinText(task, "task", os.Stdin)
	if err != nil {
		return err
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
		if len(repos) > 0 {
			var info protocol.ServerInfoResult
			if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
				return err
			}
			if !slices.Contains(info.Features, protocol.FeatureMultiRepo) {
				return fmt.Errorf("--repo: server %s does not support runs with several repositories; update the server with aether server update", info.ServerVersion)
			}
		}
		var res protocol.RunLaunchResult
		params := launchParams(wsID, task, *agent, *mode, *model, *effort, *account, *cachedBase)
		params.FallbackAccountMemberIDs, params.NoFallback = fallbacks, *noFallback
		params.Children = *children
		params.IdempotencyKey = cmp.Or(*idempotencyKey, cli.NewControlSessionID())
		for _, repo := range repos {
			id, err := workspaceIDIn(list.Workspaces, repo)
			if err != nil {
				return fmt.Errorf("--repo %s: %w", repo, err)
			}
			params.ExtraWorkspaceIDs = append(params.ExtraWorkspaceIDs, id)
		}
		if err := callInterruptibly(c, protocol.MethodRunLaunch, params, &res); err != nil {
			return launchError(err, params, fromStdin)
		}
		if res.Replayed {
			_, _ = fmt.Fprintf(os.Stderr, "idempotency key %s already launched run %s; nothing new was launched\n", params.IdempotencyKey, res.Run.ID)
		}
		fmt.Printf("run %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}

var errInterrupted = errors.New("interrupted")

// The server finishes a call the user interrupts, so callInterruptibly
// does not wait for it.
func callInterruptibly(c *protocol.Client, method string, params, result any) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	done := make(chan error, 1)
	go func() { done <- c.Call(method, params, result) }()
	select {
	case err := <-done:
		return err
	case <-interrupt:
		return errInterrupted
	}
}

// Without the server's answer the launch may be going on, so only then
// does launchError name the retry.
func launchError(err error, params protocol.RunLaunchParams, fromStdin bool) error {
	var rpcErr *protocol.Error
	switch {
	case errors.Is(err, errInterrupted):
		return &exitStatusError{code: 130, err: fmt.Errorf("%w; the launch continues on the server. aether runs lists it, and aether kill <run-id> stops it once it is listed.\nto get its run id without launching twice, run:\n  %s", errInterrupted, launchCommand(params, fromStdin))}
	case errors.As(err, &rpcErr):
		return cachedBaseLaunchError(err, params, fromStdin)
	}
	return fmt.Errorf("%w\nthe launch may still be starting; aether runs lists it. To retry without launching twice, run:\n  %s", err, launchCommand(params, fromStdin))
}

// The server fingerprints every launch param but the key, so
// launchCommand must reproduce params exactly or a retry with the same key
// conflicts.
func launchCommand(params protocol.RunLaunchParams, fromStdin bool) string {
	args := []string{"aether", "run", "--agent", shellquote.Quote(params.Harness)}
	if params.Mode != "" {
		args = append(args, "--mode", modeName(params.Mode))
	}
	if params.Model != "" {
		args = append(args, "--model", shellquote.Quote(params.Model))
	}
	if params.Effort != "" {
		args = append(args, "--effort", shellquote.Quote(params.Effort))
	}
	args = append(args, "--workspace", shellquote.Quote(params.WorkspaceID))
	if params.AccountMemberID != "" {
		args = append(args, "--account", shellquote.Quote(params.AccountMemberID))
	}
	if len(params.FallbackAccountMemberIDs) > 0 {
		args = append(args, "--fallback", shellquote.Quote(strings.Join(params.FallbackAccountMemberIDs, ",")))
	}
	if params.NoFallback {
		args = append(args, "--no-fallback")
	}
	for _, id := range params.ExtraWorkspaceIDs {
		args = append(args, "--repo", shellquote.Quote(id))
	}
	if params.CachedBase != "" {
		args = append(args, "--cached-base", shellquote.Quote(params.CachedBase))
	}
	if params.Children != "" {
		args = append(args, "--children", params.Children)
	}
	if params.IdempotencyKey != "" {
		args = append(args, "--idempotency-key", shellquote.Quote(params.IdempotencyKey))
	}
	if fromStdin {
		return strings.Join(append(args, "--", "-"), " ") + " < <the same task file>"
	}
	return strings.Join(append(args, "--", shellquote.Quote(params.Task)), " ")
}

func launchParams(workspace, task, agent, mode, model, effort, account, cachedBase string) protocol.RunLaunchParams {
	return protocol.RunLaunchParams{
		WorkspaceID:     workspace,
		Task:            task,
		Harness:         agent,
		Mode:            mode,
		Model:           model,
		Effort:          effort,
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
// The retry differs from the refused launch, so it takes a new key.
func cachedBaseLaunchError(err error, params protocol.RunLaunchParams, fromStdin bool) error {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr == nil || len(rpcErr.Data) == 0 {
		return err
	}
	var failure protocol.MirrorFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil || failure.AcceptedCommit == "" {
		return err
	}
	params.CachedBase, params.IdempotencyKey = failure.AcceptedCommit, ""
	return fmt.Errorf("%s\nretry from cached %s with:\n  %s",
		rpcErr.Message, failure.AcceptedCommit, launchCommand(params, fromStdin))
}
