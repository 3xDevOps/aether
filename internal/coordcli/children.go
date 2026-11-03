package coordcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	maxChildTaskBytes    = 24 << 10
	maxChildRequestBytes = 60 << 10
)

const childUsage = `usage: aether-internal subsession <options|start|list|cancel|fetch> [options]

Dispatch fresh-context subsessions on other agents and models, within what this
session's owner allows, and manage them. A subsession cannot start subsessions.
  options [--harness <agent>]   whether you may start subsessions, the limits,
                                and each agent and account with its state
                                (ready, exhausted, not-installed, not-signed-in);
                                --harness also lists that agent's models
  start --harness <agent> --purpose <words> (--task <text> | --task-file <path|->)
        [--model <id>] [--mode standard|enhanced|background]
        [--account <member-id>] [--idempotency-key <key>]
                                start a subsession from your last published commit;
                                commit first, uncommitted changes are not included;
                                --account takes only your session owner's account
  list [--run-id <subsession>]  your subsessions with state, latest report, branch,
                                and account
  cancel --run-id <subsession>  stop a subsession, or drop it from the queue
  fetch --run-id <subsession>   copy its branch to refs/remotes/children/<subsession>

A subsession's report arrives in your inbox and wakes you; do not poll. So does a
notice that a subsession sat idle or waiting over an hour. Message a
subsession with send --to <subsession>; it asks you with ask --to <your session>.
`

func childCommand(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, usageError("subsession requires a subcommand")
	}
	switch args[0] {
	case "options":
		fs := newFlags("subsession options")
		harness := fs.String("harness", "", "also list this agent's models")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		var out protocol.ChildOptionsResult
		err := coordtransport.Call(ctx, socket, protocol.MethodChildOptions, protocol.ChildOptionsParams{Harness: *harness}, &out)
		return out, err
	case "start":
		return childStart(ctx, socket, args[1:], in, errOut)
	case "list":
		fs := newFlags("subsession list")
		runID := fs.String("run-id", "", "one subsession's session ID")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		var out protocol.ChildListResult
		err := coordtransport.Call(ctx, socket, protocol.MethodChildList, protocol.ChildListParams{RunID: *runID}, &out)
		return out, err
	case "cancel", "fetch":
		fs := newFlags("subsession " + args[0])
		runID := fs.String("run-id", "", "the subsession's session ID")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if *runID == "" && fs.NArg() == 1 {
			*runID = fs.Arg(0)
		}
		if *runID == "" {
			return nil, usageError("subsession " + args[0] + " requires --run-id <subsession>")
		}
		method := map[string]string{"cancel": protocol.MethodChildCancel, "fetch": protocol.MethodChildFetch}[args[0]]
		if args[0] == "fetch" {
			var out protocol.ChildFetchResult
			err := coordtransport.Call(ctx, socket, method, protocol.ChildRunParams{RunID: *runID}, &out)
			return out, err
		}
		var out protocol.Child
		err := coordtransport.Call(ctx, socket, method, protocol.ChildRunParams{RunID: *runID}, &out)
		return out, err
	}
	return nil, usageError("unknown subsession command: " + args[0])
}

func childStart(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.ChildReceipt, error) {
	fs := newFlags("subsession start")
	p := protocol.ChildStartParams{}
	fs.StringVar(&p.Harness, "harness", "", "agent")
	fs.StringVar(&p.Model, "model", "", "model id")
	fs.StringVar(&p.Mode, "mode", "", "standard, enhanced or background")
	fs.StringVar(&p.AccountMemberID, "account", "", "account member ID")
	fs.StringVar(&p.Purpose, "purpose", "", "3-8 words the dashboard shows")
	task := fs.String("task", "", "the subsession's task")
	taskFile := fs.String("task-file", "", "read the task from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this start")
	if err := parseFlags(fs, args); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if p.Harness == "" || p.Purpose == "" || fs.NArg() != 0 {
		return protocol.ChildReceipt{}, usageError("subsession start requires --harness, --purpose and --task or --task-file")
	}
	var err error
	if p.Task, err = readTask(*task, *taskFile, in); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if p.IdempotencyKey, err = mutationKey("child", *key, errOut); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if raw, marshalErr := json.Marshal(p); marshalErr == nil && len(raw) > maxChildRequestBytes {
		return protocol.ChildReceipt{}, usageError(fmt.Sprintf("the task escapes to a %d-byte request; the session socket takes %d; shorten the task", len(raw), maxChildRequestBytes))
	}
	var out protocol.ChildReceipt
	err = coordtransport.Call(ctx, socket, protocol.MethodChildStart, p, &out)
	return out, err
}

func readTask(task, file string, in io.Reader) (string, error) {
	switch {
	case task != "" && file != "":
		return "", usageError("choose one of --task and --task-file")
	case file == "":
		if task == "" {
			return "", usageError("subsession start requires --task or --task-file")
		}
		return task, nil
	}
	r := in
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return "", fmt.Errorf("read task file: %w", err)
		}
		defer f.Close() //nolint:errcheck // read-only
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxChildTaskBytes+1))
	if err != nil {
		return "", fmt.Errorf("read task: %w", err)
	}
	if len(data) > maxChildTaskBytes {
		return "", usageError(fmt.Sprintf("task exceeds %d bytes", maxChildTaskBytes))
	}
	return string(data), nil
}

// childSkill is the child-run guidance for status's capabilities.
func childSkill(status *protocol.CoordStatusResult) string {
	switch {
	case slices.Contains(status.Capabilities, protocol.MethodChildStart):
		return "Subsessions: `aether-internal subsession start` runs part of your task as a separate Aether session with a fresh context and its own container and branch, on any installed agent and model, on your session owner's own account; its report reaches your inbox and wakes you. " +
			"Use one for what your own subagents cannot give: another agent or model, a separate branch or build, long work that would crowd out your context, or an independent review; honor the number of subsessions and agents the user asks for. " +
			"When to start one, the steps, the task format and the account rules: `aether-internal skill subsessions`. " +
			"Account for unfinished subsessions before reporting success. A subsession idle or waiting for over an hour is reported to your inbox: check it, then wake it with `send --to <subsession>` or stop it with `subsession cancel`.\n"
	case slices.Contains(status.Capabilities, protocol.MethodChildOptions):
		return "Subsessions: not available to this session now; `aether-internal subsession options` gives the reason. If the user asks for one, tell them that reason.\n"
	}
	return ""
}

const subsessionTopic = `Subsessions: separate Aether sessions you start, then collect. Each has a fresh
context and its own container and branch, and appears under this session in the
dashboard, where people can watch and steer it.
Start one when a part:
- needs another agent or model, such as an independent review;
- needs its own branch, container or build, or would conflict with your changes;
- is long enough that doing it here would crowd out your context;
- or when the user asks for subsessions, a number of them or particular agents:
  do exactly that.
Do the work yourself, or with your own subagents, when it is quick or one change,
when its result must land in your working tree right away or the parts share
files, or when what it needs lives in this conversation rather than in the
repository. Each subsession costs a full new context and its own setup time;
more sessions are not faster by default.
Steps:
1. Commit what it needs: it starts from your last commit and sees only its task.
2. aether-internal subsession options --harness <agent>: whether you may start
   one, the limits, and each agent's state and models.
3. aether-internal subsession start --harness <agent> [--model <id>]
     --purpose "<3-8 words>" --task-file <file>
   The task states the goal, the files it owns, the files it must not touch, how
   to verify, and what its final report must contain. Use repository-relative
   paths: it runs in another container.
4. Keep working on your part. Its report arrives in your inbox and wakes you;
   when only subsessions remain, report waiting --for agents. A success report
   is refused while one is unfinished.
5. aether-internal subsession fetch --run-id <id>, then merge or cherry-pick
   refs/remotes/children/<id> and verify before you report success.
Accounts: a subsession runs on your session owner's own sign-in for that agent.
--account accepts only that account; another member's account is refused and
shared accounts are not offered. To delegate on another member's account, that
member starts this session or is handed it.
A subsession cannot start subsessions.
`

// writeSubsessionSkill is the subsessions topic for status's capabilities.
func writeSubsessionSkill(out io.Writer, status *protocol.CoordStatusResult) (int, error) {
	text := "No mounted session socket: whether this session may start subsessions cannot be discovered.\n"
	switch {
	case status != nil && slices.Contains(status.Capabilities, protocol.MethodChildStart):
		text = subsessionTopic
	case status != nil:
		text = childSkill(status)
	}
	if text == "" {
		text = "Subsessions: this server offers none.\n"
	}
	if _, err := io.WriteString(out, text); err != nil {
		return ExitFailure, fmt.Errorf("write subsessions skill: %w", err)
	}
	return ExitOK, nil
}
