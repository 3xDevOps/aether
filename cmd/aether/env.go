package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "env",
		short: "workspace environment: show, rebuild, rollback",
		run:   runEnv,
	})
}

func runEnv(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: aether env <show|rebuild|rollback>")
	}
	switch args[0] {
	case "show":
		return envShow(args[1:])
	case "rebuild":
		return envRebuild(args[1:])
	case "rollback":
		return envRollback(args[1:])
	default:
		return fmt.Errorf("unknown env command %q; run \"aether env\" for usage", args[0])
	}
}

// envWorkspaceFlag parses one subcommand's flags, all of which are just
// --workspace, and rejects stray positionals.
func envWorkspaceFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace name or ID")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return "", fmt.Errorf("usage: aether %s [--workspace <workspace>]", name)
	}
	return *workspace, nil
}

func envShow(args []string) error {
	workspace, err := envWorkspaceFlag("env show", args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		selector, err := resolveWorkspaceSelector(c, workspace)
		if err != nil {
			return err
		}
		var res protocol.EnvStatusResult
		if err := c.Call(protocol.MethodEnvStatus, protocol.EnvStatusParams{Workspace: selector}, &res); err != nil {
			return err
		}
		return printEnvStatus(os.Stdout, res)
	})
}

// printEnvStatus renders the active version's manifest as a table, then
// every version newest first. Manifest and failure text is agent-authored,
// so both go through printable before reaching the terminal.
func printEnvStatus(w io.Writer, res protocol.EnvStatusResult) error {
	if len(res.Versions) == 0 {
		_, err := fmt.Fprintln(w, "no environment versions; save a definition through the dashboard, then run \"aether env rebuild\"")
		return err
	}
	for _, v := range res.Versions {
		if !v.Active {
			continue
		}
		who := string(v.Source)
		if v.Harness != "" {
			who += "/" + v.Harness
		}
		if _, err := fmt.Fprintf(w, "active: version %d (%s)\n", v.Version, who); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ITEM\tVERSION\tREASON")
		for _, item := range v.Manifest {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", printable(item.Name), printable(item.Version), printable(item.Reason))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if res.ActiveVersion == 0 {
		if _, err := fmt.Fprintln(w, "no active version; run \"aether env rebuild\" to build the newest saved definition"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "\nversions:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "VERSION\tSTATUS\tSOURCE\tCREATED\tDETAIL")
	for _, v := range res.Versions {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			v.Version, v.Status, v.Source, shortTime(v.CreatedAt), printable(firstLine(v.FailureDetail)))
	}
	return tw.Flush()
}

type envRebuildOptions struct {
	workspace string
	version   int
}

func parseEnvRebuild(args []string) (envRebuildOptions, error) {
	fs := flag.NewFlagSet("env rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace name or ID")
	version := fs.Int("version", 0, "definition version to build (default: the active one)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *version < 0 {
		return envRebuildOptions{}, fmt.Errorf("usage: aether env rebuild [--workspace <workspace>] [--version <n>]")
	}
	return envRebuildOptions{workspace: *workspace, version: *version}, nil
}

// envRebuild triggers a build and follows its event stream to the terminal
// status. The subscription opens before the build call so no event can be
// missed between the two.
func envRebuild(args []string) error {
	opts, err := parseEnvRebuild(args)
	if err != nil {
		return err
	}
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := conn.Control()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	selector, err := resolveWorkspaceSelector(c, opts.workspace)
	if err != nil {
		return err
	}
	stream, err := conn.Events(protocol.SubscribeRequest{
		WorkspaceID: selector.ID,
		Types:       []string{string(events.TypeEnvironmentBuild)},
	})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	var res protocol.EnvBuildResult
	if err := c.Call(protocol.MethodEnvBuild, protocol.EnvBuildParams{Workspace: selector, Version: opts.version}, &res); err != nil {
		return err
	}
	fmt.Printf("building environment version %d\n", res.Version)
	return followEnvBuild(stream, os.Stdout, res.Version)
}

// followEnvBuild reads environment.build events for one version until it
// reaches a terminal status: active returns nil, failed returns the detail.
// Engine output is agent-influenced text, so lines pass through printable.
func followEnvBuild(stream io.Reader, w io.Writer, version int) error {
	r := bufio.NewReaderSize(stream, 64<<10)
	for {
		line, err := protocol.ReadLine(r)
		if err != nil {
			return fmt.Errorf("event stream ended before the build finished; run \"aether env show\" for the result")
		}
		var ev protocol.Event
		if json.Unmarshal(line, &ev) != nil || ev.Type != string(events.TypeEnvironmentBuild) {
			continue
		}
		var payload events.EnvironmentBuildPayload
		if json.Unmarshal(ev.Payload, &payload) != nil || payload.Version != version {
			continue
		}
		switch {
		case payload.Line != "":
			if _, err := fmt.Fprintln(w, printable(payload.Line)); err != nil {
				return err
			}
		case payload.Status == domain.EnvironmentVerifying:
			if _, err := fmt.Fprintln(w, "verifying against the manifest"); err != nil {
				return err
			}
		case payload.Status == domain.EnvironmentActive:
			_, err := fmt.Fprintf(w, "version %d is active\n", version)
			return err
		case payload.Status == domain.EnvironmentFailed:
			return fmt.Errorf("environment build failed: %s; the workspace keeps its previous image - fix the definition, or run \"aether env rollback\" to re-activate the last good version", printable(payload.Detail))
		}
	}
}

func envRollback(args []string) error {
	workspace, err := envWorkspaceFlag("env rollback", args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		selector, err := resolveWorkspaceSelector(c, workspace)
		if err != nil {
			return err
		}
		var res protocol.EnvRollbackResult
		if err := c.Call(protocol.MethodEnvRollback, protocol.EnvRollbackParams{Workspace: selector}, &res); err != nil {
			return err
		}
		fmt.Printf("rolled back; version %d is active\n", res.Version)
		return nil
	})
}
