package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/3xDevOps/Aether/internal/attribution"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "runs",
		short: "list runs (--needs-you: runs that need you; --archived: only archived), or show one: aether runs <run-id>",
		usage: "aether runs [--needs-you|--archived] [--json]\n" +
			"aether runs <run-id> [--json]",
		example: "aether runs --json\n" +
			"aether runs --needs-you\n" +
			"aether runs run-d6ay38tj8j --json",
		flags: true,
		run:   runRuns,
	})
}

func runNotFound(runID string, err error) error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr.Code == protocol.CodeNotFound {
		return fmt.Errorf("run %s not found; aether runs lists runs", runID)
	}
	return err
}

func getRun(c *protocol.Client, runID string) (protocol.Run, error) {
	var res protocol.RunResult
	if err := c.Call(protocol.MethodRunGet, protocol.RunIDParams{RunID: runID}, &res); err != nil {
		return protocol.Run{}, runNotFound(runID, err)
	}
	return res.Run, nil
}

type runView struct {
	ID            string         `json:"id"`
	WorkspaceID   string         `json:"workspace_id"`
	Workspace     string         `json:"workspace"`
	Status        string         `json:"status"`
	State         string         `json:"state"`
	Reason        string         `json:"reason,omitempty"`
	PendingInputs []pendingInput `json:"pending_inputs"`
	Branch        string         `json:"branch"`
	Report        *runReport     `json:"report,omitempty"`
}

// pendingInput.Options are those aether answer accepts; a standard run's
// request is answered in its terminal and has none.
type pendingInput struct {
	Kind      string                     `json:"kind"`
	RequestID string                     `json:"request_id"`
	Options   []protocol.AttentionOption `json:"options,omitempty"`
}

type runReport struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
}

func viewOf(r protocol.Run, workspace string, items []protocol.AttentionItem, reports map[string]runReport) runView {
	v := runView{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Workspace: workspace, Status: displayStatus(r), State: r.Status,
		Reason: r.Reason, Branch: r.Branch, PendingInputs: []pendingInput{},
	}
	offered := map[string][]protocol.AttentionOption{}
	for _, it := range items {
		if it.RunID == r.ID && it.Request != nil {
			offered[it.Request.ID] = answerOptions(it)
		}
	}
	for _, in := range r.PendingInputs {
		v.PendingInputs = append(v.PendingInputs, pendingInput{Kind: in.Kind, RequestID: in.ID, Options: offered[in.ID]})
	}
	if rep, ok := reports[r.ID]; ok {
		v.Report = &rep
	}
	return v
}

// One page of the run's newest timeline entries: a report older than that
// page is not shown.
func latestReport(c *protocol.Client, workspaceID, runID string) (runReport, bool, error) {
	params := protocol.WorkspaceTimelineParams{WorkspaceID: workspaceID, RunID: runID, Types: []string{string(events.TypeTimeline)}, Newest: true, Limit: timelinePageSize}
	var res protocol.WorkspaceTimelineResult
	if err := c.Call(protocol.MethodWorkspaceTimeline, params, &res); err != nil {
		return runReport{}, false, err
	}
	for _, ev := range slices.Backward(res.Events) {
		p, err := events.DecodePayload(events.Type(ev.Type), ev.Payload)
		if entry, ok := p.(events.TimelinePayload); err == nil && ok && entry.Kind == events.TimelineReport {
			return runReport{Outcome: entry.Outcome, Summary: entry.Summary}, true, nil
		}
	}
	return runReport{}, false, nil
}

func attentionItems(c *protocol.Client, workspaceID string) ([]protocol.AttentionItem, error) {
	var res protocol.AttentionListResult
	if err := c.Call(protocol.MethodAttentionList, protocol.AttentionListParams{WorkspaceID: workspaceID}, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

func workspaceNames(c *protocol.Client) (map[string]string, error) {
	var res protocol.WorkspaceListResult
	if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &res); err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, w := range res.Workspaces {
		names[w.ID] = w.Name
	}
	return names, nil
}

// items must be the attention items of r's workspace.
func runViewOf(c *protocol.Client, r protocol.Run, items []protocol.AttentionItem) (runView, error) {
	names, err := workspaceNames(c)
	if err != nil {
		return runView{}, err
	}
	reports, err := reportsOf(c, []protocol.Run{r})
	if err != nil {
		return runView{}, err
	}
	return viewOf(r, names[r.WorkspaceID], items, reports), nil
}

func reportsOf(c *protocol.Client, runs []protocol.Run) (map[string]runReport, error) {
	reports := map[string]runReport{}
	for _, r := range runs {
		rep, ok, err := latestReport(c, r.WorkspaceID, r.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			reports[r.ID] = rep
		}
	}
	return reports, nil
}

func listRunViews(c *protocol.Client, runs []protocol.Run) ([]runView, error) {
	names, err := workspaceNames(c)
	if err != nil {
		return nil, err
	}
	items, err := attentionItems(c, "")
	if err != nil {
		return nil, err
	}
	reports, err := reportsOf(c, runs)
	if err != nil {
		return nil, err
	}
	views := make([]runView, 0, len(runs))
	for _, r := range runs {
		views = append(views, viewOf(r, names[r.WorkspaceID], items, reports))
	}
	return views, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func statusLine(v runView) string {
	line := fmt.Sprintf("run %s %s", v.ID, v.Status)
	if v.Reason != "" {
		line += ": " + printable(v.Reason)
	}
	return line
}

func printRunView(w io.Writer, v runView, items []protocol.AttentionItem) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "run\t%s\n", v.ID)
	_, _ = fmt.Fprintf(tw, "status\t%s\n", v.Status)
	if v.Reason != "" {
		_, _ = fmt.Fprintf(tw, "reason\t%s\n", printable(v.Reason))
	}
	_, _ = fmt.Fprintf(tw, "workspace\t%s (%s)\n", v.Workspace, v.WorkspaceID)
	_, _ = fmt.Fprintf(tw, "branch\t%s\n", v.Branch)
	_, _ = fmt.Fprintf(tw, "state\t%s\n", v.State)
	if v.Report != nil {
		_, _ = fmt.Fprintf(tw, "report\t%s: %s\n", v.Report.Outcome, printable(v.Report.Summary))
	}
	for _, in := range v.PendingInputs {
		_, _ = fmt.Fprintf(tw, "pending\t%s %s\n", in.Kind, in.RequestID)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return printNeeds(w, v.ID, items)
}

func runShow(runID string, asJSON bool) error {
	return withControl(func(c *protocol.Client) error {
		r, err := getRun(c, runID)
		if err != nil {
			return err
		}
		items, err := attentionItems(c, r.WorkspaceID)
		if err != nil {
			return err
		}
		v, err := runViewOf(c, r, items)
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(os.Stdout, v)
		}
		return printRunView(os.Stdout, v, items)
	})
}

// needsYou selects the runs that need a member, by the rule push
// notifications and the dashboard share (domain.Run.NeedsAttention).
func needsYou(r protocol.Run) bool {
	run := domain.Run{
		Status: domain.RunStatus(r.Status), Activity: domain.RunActivity(r.Activity), Reason: r.Reason,
		OutcomeUnseen: r.OutcomeUnseen, MissionRole: r.MissionRole, Review: domain.RunReview(r.Review),
	}
	return run.NeedsAttention(r.Paused)
}

// plural renders a count with its noun, so the notice reads as a sentence
// rather than a field.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s is", noun)
	}
	return fmt.Sprintf("%d %ss are", n, noun)
}

// renderRuns writes the table, filtering archived runs independently of status.
func renderRuns(w io.Writer, runs []protocol.Run, memberName func(string) string, overlapOf map[string]string, onlyNeedsYou, archived bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSTATUS\tAGENT\tMEMBER\tACCOUNT\tOVERLAP\tTITLE\tTASK"); err != nil {
		return err
	}
	for _, r := range runs {
		if (r.ArchivedAt != nil) != archived {
			continue
		}
		if onlyNeedsYou && !needsYou(r) {
			continue
		}
		title := r.Title
		if title == "" {
			title = r.Task
		}
		title = strings.ReplaceAll(title, "\n", " ")
		task := strings.ReplaceAll(r.Task, "\n", " ")
		account := memberName(r.AccountMemberID)
		if r.RequestedAccountMemberID != "" {
			account += " (fallback)"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, displayStatus(r), r.Harness, memberName(r.MemberID), account, overlapOf[r.ID], title, task); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// displayStatus is the dashboard's label for a live run's state, else for
// its status. A reported success awaiting review is Ready for review, live or
// completed. A swarm worker's request waits for its integrator unless the
// worker reports itself blocked.
func displayStatus(r protocol.Run) string {
	review := domain.RunReview(r.Review)
	if review == domain.ReviewReady || review == domain.ReviewLater {
		if r.Activity == string(domain.ActivityFinished) || r.Status == string(domain.RunCompleted) {
			return "Ready for review"
		}
	}
	if r.MissionRole == "worker" && r.Activity == string(domain.ActivityNeedsYou) && !needsYou(r) {
		return "Waiting for the integrator"
	}
	if label, ok := activityLabels[domain.RunActivity(r.Activity)]; ok {
		return label
	}
	if label, ok := statusLabels[domain.RunStatus(r.Status)]; ok {
		return label
	}
	return r.Status
}

var activityLabels = map[domain.RunActivity]string{
	domain.ActivityWorking:       "Working",
	domain.ActivityWaitingAgents: "Waiting for agents",
	domain.ActivityWaitingCI:     "Waiting for CI",
	domain.ActivityWaitingTools:  "Waiting for process",
	domain.ActivityWaiting:       "Waiting",
	domain.ActivityIdle:          "Waiting",
	domain.ActivityNeedsYou:      "Needs you",
	domain.ActivityFinished:      "Finished",
	domain.ActivityFailed:        "Failed",
	domain.ActivityDisconnected:  "Disconnected",
}

var statusLabels = map[domain.RunStatus]string{
	domain.RunQueued:         "Queued",
	domain.RunProvisioning:   "Provisioning",
	domain.RunRunning:        "Running",
	domain.RunNeedsAttention: "Waiting",
	domain.RunCompleted:      "Completed",
	domain.RunMerged:         "Merged",
	domain.RunAbandoned:      "Closed",
	domain.RunFailed:         "Failed",
	domain.RunInterrupted:    "Interrupted",
}

func runRuns(args []string) error {
	fs := newFlagSet("runs")
	onlyNeedsYou := fs.Bool("needs-you", false, "list only runs that need you (state needs-you or disconnected, an unseen failure, or a reported success ready for review; never paused, and a swarm worker only when blocked)")
	archived := fs.Bool("archived", false, "list only archived runs")
	asJSON := fs.Bool("json", false, "print a JSON array, or with a run id one JSON object: id, workspace_id, workspace, status, state, reason, pending_inputs, branch, report")
	runID := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		runID, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if runID == "" && fs.NArg() == 1 {
		runID = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return fmt.Errorf("usage: aether runs [--needs-you|--archived] [--json]\n   or: aether runs <run-id> [--json]")
	}
	if *onlyNeedsYou && *archived {
		return fmt.Errorf("aether runs: --needs-you and --archived cannot be used together")
	}
	if runID != "" {
		if *onlyNeedsYou || *archived {
			return fmt.Errorf("aether runs <run-id> shows one run: drop --needs-you and --archived")
		}
		return runShow(runID, *asJSON)
	}
	return withControl(func(c *protocol.Client) error {
		var rl protocol.RunListResult
		if err := c.Call(protocol.MethodRunList, protocol.RunListParams{}, &rl); err != nil {
			return err
		}
		if *asJSON {
			listed := slices.DeleteFunc(rl.Runs, func(r protocol.Run) bool {
				return (r.ArchivedAt != nil) != *archived || *onlyNeedsYou && !needsYou(r)
			})
			views, err := listRunViews(c, listed)
			if err != nil {
				return err
			}
			return writeJSON(os.Stdout, views)
		}
		var ml protocol.MemberListResult
		if err := c.Call(protocol.MethodMemberList, struct{}{}, &ml); err != nil {
			return err
		}
		var ol protocol.RunOverlapsResult
		if err := c.Call(protocol.MethodRunOverlaps, struct{}{}, &ol); err != nil {
			return err
		}
		colorOf := map[string]string{}
		nameOf := map[string]string{}
		for _, m := range ml.Members {
			colorOf[m.ID] = m.Color
			nameOf[m.ID] = m.DisplayName
		}
		color := term.IsTerminal(int(os.Stdout.Fd()))
		memberName := func(id string) string {
			name := nameOf[id]
			if name == "" {
				name = id
			}
			if color {
				name = attribution.Sprint(colorOf[id], name)
			}
			return name
		}
		overlapOf := map[string]string{}
		for _, o := range ol.Overlaps {
			flags := make([]string, 0, len(o.With))
			for _, p := range o.With {
				flags = append(flags, strings.Join(p.Files, ",")+" with "+memberName(p.MemberID))
			}
			overlapOf[o.RunID] = strings.Join(flags, "; ")
		}
		waiting := 0
		for _, r := range rl.Runs {
			if needsYou(r) {
				waiting++
			}
		}
		if err := renderRuns(os.Stdout, rl.Runs, memberName, overlapOf, *onlyNeedsYou, *archived); err != nil {
			return err
		}
		// The notice goes to stderr so it never lands in a pipeline reading
		// the table, and it is skipped when the table already is the answer.
		if waiting > 0 && !*onlyNeedsYou && !*archived {
			_, _ = fmt.Fprintf(os.Stderr, "\n%s waiting for you: aether runs --needs-you\n",
				plural(waiting, "run"))
		}
		return nil
	})
}
