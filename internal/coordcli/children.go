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

const childUsage = `usage: aether-internal child <options|start|list|cancel|detach|fetch> [options]

Start child runs on other agents and models, within what this run's owner or
swarm allows, and manage them.
  options [--harness <agent>]   whether you may start children, the limits,
                                and each agent and account with its state
                                (ready, exhausted, not-installed, not-signed-in);
                                --harness also lists that agent's models
  start --harness <agent> --purpose <words> (--task <text> | --task-file <path|->)
        [--model <id>] [--mode standard|enhanced|background]
        [--account <member-id>] [--idempotency-key <key>]
                                start a child from your last published commit;
                                commit first, uncommitted changes are not included
  list [--run-id <child>]       your children with state, latest report, branch,
                                account and whether their base is stale
  cancel --run-id <child>       stop a child, or drop it from the queue
  detach --run-id <child>       let a child run on without you
  fetch --run-id <child>        copy its branch to refs/remotes/children/<child>

A child's report arrives in your inbox and wakes you; do not poll. Message a
child with send --to <child>; it asks you with ask --to <your run>.
`

func childCommand(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, usageError("child requires a subcommand")
	}
	switch args[0] {
	case "options":
		fs := newFlags("child options")
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
		fs := newFlags("child list")
		runID := fs.String("run-id", "", "one child's run ID")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		var out protocol.ChildListResult
		err := coordtransport.Call(ctx, socket, protocol.MethodChildList, protocol.ChildListParams{RunID: *runID}, &out)
		return out, err
	case "cancel", "detach", "fetch":
		fs := newFlags("child " + args[0])
		runID := fs.String("run-id", "", "the child's run ID")
		if err := parseFlags(fs, args[1:]); err != nil {
			return nil, err
		}
		if *runID == "" && fs.NArg() == 1 {
			*runID = fs.Arg(0)
		}
		if *runID == "" {
			return nil, usageError("child " + args[0] + " requires --run-id <child>")
		}
		method := map[string]string{"cancel": protocol.MethodChildCancel, "detach": protocol.MethodChildDetach, "fetch": protocol.MethodChildFetch}[args[0]]
		if args[0] == "fetch" {
			var out protocol.ChildFetchResult
			err := coordtransport.Call(ctx, socket, method, protocol.ChildRunParams{RunID: *runID}, &out)
			return out, err
		}
		var out protocol.Child
		err := coordtransport.Call(ctx, socket, method, protocol.ChildRunParams{RunID: *runID}, &out)
		return out, err
	}
	return nil, usageError("unknown child command: " + args[0])
}

func childStart(ctx context.Context, socket string, args []string, in io.Reader, errOut io.Writer) (protocol.ChildReceipt, error) {
	fs := newFlags("child start")
	p := protocol.ChildStartParams{}
	fs.StringVar(&p.Harness, "harness", "", "agent")
	fs.StringVar(&p.Model, "model", "", "model id")
	fs.StringVar(&p.Mode, "mode", "", "standard, enhanced or background")
	fs.StringVar(&p.AccountMemberID, "account", "", "account member ID")
	fs.StringVar(&p.Purpose, "purpose", "", "3-8 words the dashboard shows")
	task := fs.String("task", "", "the child's task")
	taskFile := fs.String("task-file", "", "read the task from a file, or - for stdin")
	key := fs.String("idempotency-key", "", "stable key used to replay this start")
	if err := parseFlags(fs, args); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if p.Harness == "" || p.Purpose == "" || fs.NArg() != 0 {
		return protocol.ChildReceipt{}, usageError("child start requires --harness, --purpose and --task or --task-file")
	}
	var err error
	if p.Task, err = readTask(*task, *taskFile, in); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if p.IdempotencyKey, err = mutationKey("child", *key, errOut); err != nil {
		return protocol.ChildReceipt{}, err
	}
	if raw, marshalErr := json.Marshal(p); marshalErr == nil && len(raw) > maxChildRequestBytes {
		return protocol.ChildReceipt{}, usageError(fmt.Sprintf("the task escapes to a %d-byte request; the run socket takes %d; shorten the task", len(raw), maxChildRequestBytes))
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
			return "", usageError("child start requires --task or --task-file")
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
		return "Child runs: you may start focused runs on other agents and models, e.g. an adversarial review by another agent (aether-internal child --help). " +
			"Run `aether-internal child options` once, then `child start`; honor the number of children and agents the user asks for. " +
			"A child sees only its task text and your last commit. Its report reaches your inbox; account for unfinished children before reporting success.\n"
	case slices.Contains(status.Capabilities, protocol.MethodChildOptions):
		return "Child runs: not available to this run now; `aether-internal child options` gives the reason. If the user asks for one, tell them that reason.\n"
	}
	return ""
}
