package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

const (
	changesPollInterval = 2 * time.Second
	changesDefaultLimit = 20
)

func init() {
	register(command{
		name:    "changes",
		short:   "show run and swarm changes since a cursor",
		usage:   "aether changes [--since <cursor>] [--run <run-id> | --swarm <swarm-id> | --workspace <name>] [--kinds <kinds>] [--limit <n>] [--follow]",
		example: "aether changes --swarm mis-gzy6wgdnky --kinds reports,swarm --follow",
		flags:   true,
		run:     runChanges,
	})
}

func runChanges(args []string) error {
	fs := newFlagSet("changes")
	since := fs.String("since", "", "cursor printed by an earlier call (default: the newest changes)")
	run := fs.String("run", "", "only changes for this run")
	swarm := fs.String("swarm", "", "only changes for this swarm's runs and the swarm itself")
	workspace := fs.String("workspace", "", "only changes in this workspace (ID or name)")
	kinds := fs.String("kinds", "", "comma-separated: status,reports,messages,pr,swarm,lifecycle")
	limit := fs.Int("limit", changesDefaultLimit, "changes per request, at most 100; the command reads every page")
	follow := fs.Bool("follow", false, "keep polling for new changes until interrupted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: aether changes [flags]; name the run with --run")
	}
	return withControl(func(c *protocol.Client) error {
		params := protocol.ChangesListParams{Cursor: *since, RunID: *run, MissionID: *swarm, Kinds: splitTypes(*kinds), Limit: *limit}
		if *workspace != "" {
			var wl protocol.WorkspaceListResult
			if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &wl); err != nil {
				return err
			}
			id, err := workspaceIDIn(wl.Workspaces, *workspace)
			unknown := !slices.ContainsFunc(wl.Workspaces, func(w protocol.Workspace) bool { return w.Name == *workspace })
			if err != nil && *since != "" && unknown {
				// A resumed feed may name a workspace deleted since; the
				// server still pages it, to deliver the deletion.
				id, err = *workspace, nil
			}
			if err != nil {
				return err
			}
			params.WorkspaceID = id
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		next := func() {
			if params.Cursor != "" {
				_, _ = fmt.Fprintln(os.Stderr, "next: "+changesResume(params))
			}
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SEQ\tTIME\tRUN\tKIND\tDETAIL")
		for {
			var res protocol.ChangesListResult
			if err := callInterruptibly(c, protocol.MethodChangesList, params, &res); err != nil {
				var refused *protocol.Error
				if !errors.As(err, &refused) {
					next()
				}
				if errors.Is(err, errInterrupted) {
					return &exitStatusError{code: 130, err: err}
				}
				return err
			}
			for _, ch := range res.Changes {
				_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", ch.Seq, shortTime(ch.Time), ch.RunID, ch.Kind, printable(changeDetail(ch)))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			params.Cursor = res.NextCursor
			if res.More {
				continue
			}
			if !*follow {
				next()
				return nil
			}
			select {
			case <-ctx.Done():
				next()
				return &exitStatusError{code: 130, err: errInterrupted}
			case <-time.After(changesPollInterval):
			}
		}
	})
}

// changesResume is the command that continues params' feed, naming the
// workspace by ID so it still resolves after the workspace is deleted.
func changesResume(p protocol.ChangesListParams) string {
	resume := []string{"aether", "changes", "--since=" + p.Cursor}
	for _, f := range [][2]string{{"run", p.RunID}, {"swarm", p.MissionID}, {"workspace", p.WorkspaceID}, {"kinds", strings.Join(p.Kinds, ",")}} {
		if f[1] != "" {
			resume = append(resume, "--"+f[0]+"="+shellquote.Quote(f[1]))
		}
	}
	if p.Limit != changesDefaultLimit {
		resume = append(resume, "--limit="+strconv.Itoa(p.Limit))
	}
	return strings.Join(resume, " ")
}

func changeDetail(ch protocol.Change) string {
	var detail string
	switch {
	case ch.Status != nil:
		detail = ch.Status.To
		if ch.Status.From != "" {
			detail = ch.Status.From + " -> " + detail
		}
		if ch.Status.State != "" {
			detail += " · " + ch.Status.State
			if ch.Status.Cause != "" {
				detail += " by " + ch.Status.Cause
			}
		}
	case ch.Input != nil:
		detail = strconv.Itoa(ch.Input.Pending) + " input requests open"
	case ch.Report != nil:
		detail = ch.Report.Outcome
		if ch.Report.WaitingFor != "" {
			detail += " for " + ch.Report.WaitingFor
		}
	case ch.Message != nil:
		detail = fmt.Sprintf("%s -> %s · %s", ch.Message.FromRunID, ch.Message.ToRunID, ch.Message.Kind)
	case ch.PR != nil:
		detail = fmt.Sprintf("#%d %s", ch.PR.Number, ch.PR.State)
	case ch.Swarm != nil && ch.Swarm.CandidateID != "":
		detail = fmt.Sprintf("%s candidate %s %s", ch.MissionID, ch.Swarm.CandidateID, ch.Swarm.State)
	case ch.Swarm != nil:
		detail = fmt.Sprintf("%s %s %s", ch.MissionID, ch.Swarm.Change, ch.Swarm.TaskID)
	case ch.Lifecycle != nil:
		detail = ch.Lifecycle.Action
		switch ch.Lifecycle.Action {
		case "workspace-deleted":
			detail += " " + ch.WorkspaceID
		case "swarm-deleted":
			detail += " " + ch.MissionID
		}
		if ch.Lifecycle.ToMemberID != "" {
			detail += " to " + ch.Lifecycle.ToMemberID
		}
	}
	if u := ch.Untrusted; u != nil {
		if u.Summary != "" {
			detail += ": " + firstLine(u.Summary)
		}
		if u.Reason != "" {
			detail += " (" + firstLine(u.Reason) + ")"
		}
	}
	return detail
}
