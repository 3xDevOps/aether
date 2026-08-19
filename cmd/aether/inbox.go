package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "inbox",
		short: "list, approve, or deny pending approval requests",
		run:   runInbox,
	})
}

func runInbox(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "approve", "deny":
			if len(args) < 2 {
				return fmt.Errorf("usage: aether inbox %s <request-id> [--session]", args[0])
			}
			return inboxDecide(args[0] == "approve", args[1], args[2:])
		}
	}
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	all := fs.Bool("all", false, "include already decided requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		list, err := inboxList(c, *session, *all)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "REQUEST\tRUN\tACTION\tDECISION\tBY\tDETAIL")
		for _, a := range list {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				a.ID, a.RunID, a.Action, a.Decision, a.DecidedBy, firstLine(a.Detail))
		}
		return tw.Flush()
	})
}

func inboxDecide(approve bool, requestID string, args []string) error {
	fs := flag.NewFlagSet("inbox decide", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		list, err := inboxList(c, *session, true)
		if err != nil {
			return err
		}
		run := ""
		for _, a := range list {
			if a.ID == requestID {
				run = a.RunID
			}
		}
		if run == "" {
			return fmt.Errorf("approval request %q not found", requestID)
		}
		var res protocol.ApprovalDecideResult
		if err := c.Call(protocol.MethodApprovalDecide, protocol.ApprovalDecideParams{
			RunID:     run,
			RequestID: requestID,
			Approve:   approve,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("%s %s (%s)\n", res.Approval.Decision, res.Approval.ID, res.Approval.Action)
		return nil
	})
}

func inboxList(c *protocol.Client, session string, all bool) ([]protocol.Approval, error) {
	sessID, err := resolveSession(c, session)
	if err != nil {
		return nil, err
	}
	var res protocol.ApprovalListResult
	if err := c.Call(protocol.MethodApprovalList, protocol.ApprovalListParams{
		SessionID: sessID,
		All:       all,
	}, &res); err != nil {
		return nil, err
	}
	return res.Approvals, nil
}

// firstLine keeps multi-line request bodies from breaking the table.
func firstLine(s string) string {
	line, _, cut := strings.Cut(s, "\n")
	if cut {
		line += " ..."
	}
	return line
}
