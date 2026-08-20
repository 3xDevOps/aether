package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type workspaceToolsOptions struct {
	operation string
	workspace string
	command   string
	snapshot  string
	confirm   bool
}

func parseWorkspaceTools(args []string) (workspaceToolsOptions, error) {
	if len(args) < 1 {
		return workspaceToolsOptions{}, fmt.Errorf("usage: aether workspace tools list|verify|rollback|reset <workspace> [args]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("workspace tools list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		workspace, err := parseLeadingArg(fs, args[1:])
		if err != nil || workspace == "" {
			return workspaceToolsOptions{}, fmt.Errorf("usage: aether workspace tools list <workspace>")
		}
		return workspaceToolsOptions{operation: "list", workspace: workspace}, nil
	case "verify":
		fs := flag.NewFlagSet("workspace tools verify", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		command := fs.String("command", "", "verification executable")
		workspace, err := parseLeadingArg(fs, args[1:])
		if err != nil || workspace == "" {
			return workspaceToolsOptions{}, fmt.Errorf("usage: aether workspace tools verify <workspace> [--command <executable>]")
		}
		return workspaceToolsOptions{operation: "verify", workspace: workspace, command: *command}, nil
	case "rollback":
		if len(args) != 3 || args[1] == "" || args[2] == "" || args[1][0] == '-' || args[2][0] == '-' {
			return workspaceToolsOptions{}, fmt.Errorf("usage: aether workspace tools rollback <workspace> <snapshot>")
		}
		return workspaceToolsOptions{operation: "rollback", workspace: args[1], snapshot: args[2]}, nil
	case "reset":
		fs := flag.NewFlagSet("workspace tools reset", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		confirm := fs.Bool("confirm", false, "confirm removal of tool snapshots")
		yes := fs.Bool("yes", false, "confirm removal of tool snapshots")
		workspace, err := parseLeadingArg(fs, args[1:])
		if err != nil || workspace == "" {
			return workspaceToolsOptions{}, fmt.Errorf("usage: aether workspace tools reset <workspace> --confirm")
		}
		if !*confirm && !*yes {
			return workspaceToolsOptions{}, fmt.Errorf("aether workspace tools reset: refusal requires --confirm")
		}
		return workspaceToolsOptions{operation: "reset", workspace: workspace, confirm: true}, nil
	default:
		return workspaceToolsOptions{}, fmt.Errorf("unknown workspace tools command %q", args[0])
	}
}

func runWorkspaceTools(args []string) error {
	opts, err := parseWorkspaceTools(args)
	if err != nil {
		return err
	}
	return withResolvedWorkspace(opts.workspace, func(selector protocol.WorkspaceSelector) error {
		switch opts.operation {
		case "list":
			var result protocol.ToolSnapshotListResult
			if err := callWorkspaceTools(protocol.MethodWorkspaceToolsList, protocol.ToolSnapshotListParams{Workspace: selector}, &result); err != nil {
				return err
			}
			printToolSnapshots(result)
			return nil
		case "verify":
			var result protocol.ToolSnapshotVerifyResult
			if err := callWorkspaceTools(protocol.MethodWorkspaceToolsVerify, protocol.ToolSnapshotVerifyParams{Workspace: selector, VerificationExecutable: opts.command}, &result); err != nil {
				return err
			}
			if !result.Verified {
				if result.Error == "" {
					return fmt.Errorf("workspace tools verify: verification failed")
				}
				return fmt.Errorf("workspace tools verify: %s", result.Error)
			}
			if result.Snapshot != nil {
				fmt.Printf("verified %s\n", result.Snapshot.ID)
			} else {
				fmt.Println("verified")
			}
			return nil
		case "rollback":
			var result protocol.ToolSnapshotRollbackResult
			if err := callWorkspaceTools(protocol.MethodWorkspaceToolsRollback, protocol.ToolSnapshotRollbackParams{Workspace: selector, SnapshotID: opts.snapshot}, &result); err != nil {
				return err
			}
			fmt.Printf("active %s\n", result.Snapshot.ID)
			return nil
		case "reset":
			var result protocol.ToolSnapshotResetResult
			if err := callWorkspaceTools(protocol.MethodWorkspaceToolsReset, protocol.ToolSnapshotResetParams{Workspace: selector, Confirm: opts.confirm}, &result); err != nil {
				return err
			}
			if !result.Reset {
				return fmt.Errorf("workspace tools reset: reset was not completed")
			}
			fmt.Println("tool snapshots reset")
			return nil
		default:
			return fmt.Errorf("unknown workspace tools command %q", opts.operation)
		}
	})
}

func callWorkspaceTools(method string, params any, result any) error {
	return withControl(func(c *protocol.Client) error { return c.Call(method, params, result) })
}

func printToolSnapshots(result protocol.ToolSnapshotListResult) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tACTIVE\tEXECUTABLE\tVERSION\tCREATED_AT")
	for _, snapshot := range result.Snapshots {
		active := "no"
		if snapshot.Active || (result.Active != nil && result.Active.ID == snapshot.ID) {
			active = "yes"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", snapshot.ID, active, snapshot.Manifest.Executable, snapshot.Manifest.Version, snapshot.CreatedAt)
	}
	_ = w.Flush()
}
