package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "who",
		short: "show who is online and which runs they are watching",
		run:   runWho,
	})
}

func runWho(args []string) error {
	fs := flag.NewFlagSet("who", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	run := fs.String("run", "", "only members watching this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		// Asking who is here also announces that you are.
		if err := c.Call(protocol.MethodPresenceHeartbeat, protocol.PresenceHeartbeatParams{WorkspaceID: wsID}, nil); err != nil {
			return err
		}
		var roster protocol.PresenceRosterResult
		if err := c.Call(protocol.MethodPresenceRoster, protocol.PresenceRosterParams{
			WorkspaceID: wsID,
			RunID:       *run,
		}, &roster); err != nil {
			return err
		}
		var members protocol.MemberListResult
		if err := c.Call(protocol.MethodMemberList, struct{}{}, &members); err != nil {
			return err
		}
		byID := make(map[string]protocol.Member, len(members.Members))
		for _, m := range members.Members {
			byID[m.ID] = m
		}
		color := term.IsTerminal(int(os.Stdout.Fd()))
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "MEMBER\tSTATE\tWATCHING\tLAST SEEN")
		for _, p := range roster.Members {
			name := p.MemberID
			if m, ok := byID[p.MemberID]; ok {
				name = m.DisplayName
				if color {
					name = attribution.Sprint(m.Color, name)
				}
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				name, p.State, strings.Join(p.Watching, ","), p.LastSeen)
		}
		return tw.Flush()
	})
}
