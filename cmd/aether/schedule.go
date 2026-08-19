package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "schedule",
		short: "list, set, or delete the cron rules that fire task templates",
		run:   runSchedule,
	})
}

func runSchedule(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "set":
			return scheduleSet(args[1:])
		case "delete":
			return scheduleDelete(args[1:])
		case "list":
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("schedule list", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		sessID, err := resolveSession(c, *session)
		if err != nil {
			return err
		}
		var res protocol.ScheduleListResult
		if err := c.Call(protocol.MethodScheduleList, protocol.ScheduleListParams{SessionID: sessID}, &res); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TEMPLATE\tCRON\tOWNER\tLAST FIRE\tNEXT FIRE")
		for _, s := range res.Schedules {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				s.Template, s.Cron, s.MemberID, orDash(s.LastFireAt), orDash(s.NextFireAt))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		// The policy is worth stating where people read schedules.
		fmt.Println("\ncron rules are evaluated in UTC; runs missed while the server was down are skipped, never caught up")
		return nil
	})
}

func scheduleSet(args []string) error {
	fs := flag.NewFlagSet("schedule set", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	rest, err := parseTwoArgs(fs, args)
	if err != nil {
		return fmt.Errorf("usage: aether schedule set <template> \"<cron>\" [--session]\ncron is standard five-field syntax or an @descriptor, in UTC")
	}
	template, spec := rest[0], rest[1]
	return withControl(func(c *protocol.Client) error {
		sessID, err := resolveSession(c, *session)
		if err != nil {
			return err
		}
		var res protocol.ScheduleSaveResult
		if err := c.Call(protocol.MethodScheduleSave, protocol.ScheduleSaveParams{
			SessionID: sessID, Template: template, Cron: spec,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("template %s runs on %q; next fire %s\n",
			res.Schedule.Template, res.Schedule.Cron, orDash(res.Schedule.NextFireAt))
		return nil
	})
}

func scheduleDelete(args []string) error {
	fs := flag.NewFlagSet("schedule delete", flag.ExitOnError)
	session := fs.String("session", "", "session ID or name (default: the only session)")
	template, err := parseLeadingArg(fs, args)
	if err != nil || template == "" {
		return fmt.Errorf("usage: aether schedule delete <template> [--session]")
	}
	return withControl(func(c *protocol.Client) error {
		sessID, err := resolveSession(c, *session)
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodScheduleDelete, protocol.ScheduleDeleteParams{
			SessionID: sessID, Template: template,
		}, nil); err != nil {
			return err
		}
		fmt.Printf("template %s is no longer scheduled\n", template)
		return nil
	})
}

// parseTwoArgs pulls the two leading positional arguments off args before
// parsing the flags, the way parseLeadingArg does for one.
func parseTwoArgs(fs *flag.FlagSet, args []string) ([2]string, error) {
	var out [2]string
	if len(args) < 2 {
		return out, fmt.Errorf("expected two positional arguments")
	}
	out[0], out[1] = args[0], args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return out, err
	}
	if out[0] == "" || out[1] == "" || fs.NArg() != 0 {
		return out, fmt.Errorf("expected exactly two positional arguments")
	}
	return out, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
