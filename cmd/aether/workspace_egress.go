package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const workspaceEnvEgressUsage = "usage: aether workspace env egress list [--workspace <name-or-id>]\n" +
	"   or: aether workspace env egress on|off [--workspace <name-or-id>]\n" +
	"   or: aether workspace env egress allow|deny [--workspace <name-or-id>] <host> [<host> ...]   (a host is example.com or *.example.com)"

func workspaceEnvEgress(args []string) error {
	if len(args) < 1 {
		return errors.New(workspaceEnvEgressUsage)
	}
	verb := args[0]
	fs, workspace := workspaceEnvFlags("workspace env egress " + verb)
	if err := fs.Parse(args[1:]); err != nil {
		return errors.New(workspaceEnvEgressUsage)
	}
	change := protocol.WorkspaceEnvironmentSetParams{}
	scope := "the host list applies at once to sessions started with egress control on"
	switch verb {
	case "list", "on", "off":
		if fs.NArg() != 0 {
			return errors.New(workspaceEnvEgressUsage)
		}
		if verb != "list" {
			on := verb == "on"
			change.Egress = &on
			scope = "egress control " + verb + " applies to sessions launched afterwards"
		}
	case "allow", "deny":
		if fs.NArg() == 0 || slices.ContainsFunc(fs.Args(), func(arg string) bool { return strings.HasPrefix(arg, "-") }) {
			return errors.New(workspaceEnvEgressUsage)
		}
		if verb == "allow" {
			change.EgressAllow = fs.Args()
		} else {
			change.EgressDeny = fs.Args()
		}
	default:
		return fmt.Errorf("unknown workspace env egress command %q", verb)
	}
	return withControl(func(c *protocol.Client) error {
		ws, err := resolveMirrorWorkspace(c, *workspace, false)
		if err != nil {
			return err
		}
		var res protocol.WorkspaceEnvironmentResult
		if verb == "list" {
			err = c.Call(protocol.MethodWorkspaceEnvironmentGet, protocol.WorkspaceEnvironmentGetParams{WorkspaceID: ws.ID}, &res)
		} else {
			change.WorkspaceID = ws.ID
			err = c.Call(protocol.MethodWorkspaceEnvironmentSet, change, &res)
		}
		if err != nil {
			return err
		}
		// A server from before egress control ignores the change and
		// answers without the host list.
		if res.EgressHosts == nil {
			return errors.New("this server has no egress control; update aether-server, then run the command again")
		}
		if err = printWorkspaceEgress(os.Stdout, ws, res); err != nil || verb == "list" {
			return err
		}
		if _, err = fmt.Fprintln(os.Stdout, scope); err != nil {
			return err
		}
		return printMirrorWarning(os.Stdout, res.Warning)
	})
}

func printWorkspaceEgress(w io.Writer, ws protocol.Workspace, res protocol.WorkspaceEnvironmentResult) error {
	state := "off"
	if res.Egress {
		state = "on"
	}
	if _, err := fmt.Fprintf(w, "workspace %s %s\negress control %s\n", ws.ID, ws.Name, state); err != nil {
		return err
	}
	if len(res.EgressHosts) == 0 {
		_, err := fmt.Fprintln(w, "no allowed hosts")
		return err
	}
	if _, err := fmt.Fprintln(w, "allowed hosts:"); err != nil {
		return err
	}
	for _, host := range res.EgressHosts {
		if _, err := fmt.Fprintln(w, "  "+host); err != nil {
			return err
		}
	}
	return nil
}
