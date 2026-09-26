package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func init() {
	register(command{
		name:  "swarm",
		short: "create, list, and show swarms (missions with an integrator run)",
		run:   runSwarm,
	})
}

const swarmUsage = "usage: aether swarm create \"<objective>\"|- --agent <harness> [--account <member-id>] [--worker <harness>[:tui|headless]]... [--max-concurrent N] [--max-attempts N] [--workspace]\n" +
	"   or: aether swarm list [--workspace]\n" +
	"   or: aether swarm show <mission-id>"

func runSwarm(args []string) error {
	if len(args) == 0 {
		return errors.New(swarmUsage)
	}
	switch args[0] {
	case "create":
		return swarmCreate(args[1:], os.Stdin)
	case "list":
		return swarmList(args[1:])
	case "show":
		return swarmShow(args[1:])
	}
	return fmt.Errorf("unknown swarm command %q\n%s", args[0], swarmUsage)
}

// swarmSpec is a validated create request before the account and workspace
// are resolved over the control channel.
type swarmSpec struct {
	objective     string
	agent         string
	account       string
	workspace     string
	workers       []protocol.MissionExecutionChoice
	maxConcurrent int
	maxAttempts   int
}

func swarmCreate(args []string, stdin io.Reader) error {
	spec, err := parseSwarmCreate(args, stdin)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, spec.workspace)
		if err != nil {
			return err
		}
		accountID := spec.account
		if accountID == "" {
			var info protocol.ServerInfoResult
			if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
				return err
			}
			accountID = info.Member.ID
		}
		return createSwarm(c, os.Stdout, missionCreateParams(wsID, accountID, spec, cli.NewControlSessionID()))
	})
}

// parseSwarmCreate rejects what the server would refuse before any RPC, so
// a typo never costs a round trip or a stored mission.
func parseSwarmCreate(args []string, stdin io.Reader) (swarmSpec, error) {
	fs := flag.NewFlagSet("swarm create", flag.ExitOnError)
	agent := fs.String("agent", "", "integrator harness name (runs in tui mode)")
	account := fs.String("account", "", "member ID whose shared agent account to use (default: yours)")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	maxConcurrent := fs.Int("max-concurrent", 2, "worker attempts running at once (1..8)")
	maxAttempts := fs.Int("max-attempts", 8, "worker attempts over the whole swarm (1..128)")
	var workers stringList
	fs.Var(&workers, "worker", "allow workers on this harness, harness[:tui|headless] (repeatable, default mode tui)")
	objective, err := parseLeadingArg(fs, args)
	if err != nil || *agent == "" {
		return swarmSpec{}, errors.New(swarmUsage)
	}
	if objective == "-" {
		raw, readErr := io.ReadAll(stdin)
		if readErr != nil {
			return swarmSpec{}, fmt.Errorf("read objective from stdin: %w", readErr)
		}
		objective = strings.TrimSpace(string(raw))
		if objective == "" {
			return swarmSpec{}, errors.New("objective on stdin is empty")
		}
	}
	spec := swarmSpec{
		objective: objective, agent: *agent, account: *account, workspace: *workspace,
		maxConcurrent: *maxConcurrent, maxAttempts: *maxAttempts,
	}
	for _, w := range workers {
		choice, parseErr := parseWorker(w)
		if parseErr != nil {
			return swarmSpec{}, parseErr
		}
		spec.workers = append(spec.workers, choice)
	}
	if err := validateSwarmLimits(spec.maxConcurrent, spec.maxAttempts); err != nil {
		return swarmSpec{}, err
	}
	return spec, nil
}

// parseWorker reads one --worker value, harness[:mode]. The account is the
// integrator's and is filled in by missionCreateParams.
func parseWorker(spec string) (protocol.MissionExecutionChoice, error) {
	harness, mode, ok := strings.Cut(spec, ":")
	if !ok {
		mode = "tui"
	}
	if harness == "" || (mode != "tui" && mode != "headless") {
		return protocol.MissionExecutionChoice{}, fmt.Errorf("invalid --worker %q (want harness or harness:tui|headless)", spec)
	}
	return protocol.MissionExecutionChoice{Harness: harness, Mode: mode}, nil
}

func validateSwarmLimits(concurrent, total int) error {
	if concurrent < 1 || concurrent > domain.MaxMissionConcurrentAttempts {
		return fmt.Errorf("--max-concurrent %d is out of range (want 1..%d)", concurrent, domain.MaxMissionConcurrentAttempts)
	}
	if total < 1 || total > domain.MaxMissionTotalAttempts {
		return fmt.Errorf("--max-attempts %d is out of range (want 1..%d)", total, domain.MaxMissionTotalAttempts)
	}
	if total < concurrent {
		return fmt.Errorf("--max-attempts %d is below --max-concurrent %d", total, concurrent)
	}
	return nil
}

// missionCreateParams builds the request the server accepts: the integrator
// tuple is always the first execution choice, and a --worker that repeats it
// or another worker is sent once, because the store refuses duplicates.
func missionCreateParams(workspaceID, accountID string, spec swarmSpec, key string) protocol.MissionCreateParams {
	integrator := protocol.MissionExecutionChoice{AccountMemberID: accountID, Harness: spec.agent, Mode: "tui"}
	choices := []protocol.MissionExecutionChoice{integrator}
	for _, w := range spec.workers {
		w.AccountMemberID = accountID
		duplicate := false
		for _, c := range choices {
			if c == w {
				duplicate = true
				break
			}
		}
		if !duplicate {
			choices = append(choices, w)
		}
	}
	return protocol.MissionCreateParams{
		WorkspaceID:           workspaceID,
		Objective:             spec.objective,
		Integrator:            protocol.MissionIntegrator(integrator),
		ExecutionChoices:      choices,
		MaxConcurrentAttempts: spec.maxConcurrent,
		MaxTotalAttempts:      spec.maxAttempts,
		IdempotencyKey:        key,
	}
}

func createSwarm(c *protocol.Client, w io.Writer, params protocol.MissionCreateParams) error {
	var res protocol.MissionCreateResult
	if err := c.Call(protocol.MethodMissionCreate, params, &res); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "swarm %s %s\nintegrator run %s\n", res.Mission.ID, res.Mission.Phase, res.Mission.CurrentIntegratorRunID); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}

func swarmList(args []string) error {
	fs := flag.NewFlagSet("swarm list", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether swarm list [--workspace]: unexpected argument %q", fs.Arg(0))
	}
	return withControl(func(c *protocol.Client) error {
		wsID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		missions, err := listSwarms(c, wsID)
		if err != nil {
			return err
		}
		return renderSwarms(os.Stdout, missions)
	})
}

// listSwarms follows the list cursor until the server has no older page, so
// a workspace with more missions than one page holds is listed whole.
func listSwarms(c *protocol.Client, wsID string) ([]protocol.Mission, error) {
	var out []protocol.Mission
	cursor := ""
	for {
		var res protocol.MissionListResult
		if err := c.Call(protocol.MethodMissionList, protocol.MissionListParams{WorkspaceID: wsID, Before: cursor}, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Missions...)
		if res.NextCursor == "" || len(res.Missions) == 0 {
			return out, nil
		}
		cursor = res.NextCursor
	}
}

// objectiveColumnWidth keeps the list readable in a terminal; the full
// objective is on aether swarm show.
const objectiveColumnWidth = 60

func renderSwarms(w io.Writer, missions []protocol.Mission) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tPHASE\tOBJECTIVE\tINTEGRATOR\tUPDATED"); err != nil {
		return err
	}
	for _, m := range missions {
		objective := []rune(cell(m.Objective))
		if len(objective) > objectiveColumnWidth {
			objective = append(objective[:objectiveColumnWidth-3], []rune("...")...)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			m.ID, m.Phase, string(objective), m.CurrentIntegratorRunID, m.UpdatedAt); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// cell renders free text as one table cell: its first line, with tabs
// turned into spaces so the value cannot shift the columns.
func cell(s string) string {
	return strings.ReplaceAll(firstLine(s), "\t", " ")
}

func swarmShow(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: aether swarm show <mission-id>")
	}
	return withControl(func(c *protocol.Client) error {
		var res protocol.MissionShowResult
		if err := c.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: args[0]}, &res); err != nil {
			return err
		}
		return renderSwarm(os.Stdout, res)
	})
}

func renderSwarm(w io.Writer, res protocol.MissionShowResult) error {
	m := res.Mission
	var b strings.Builder
	fmt.Fprintf(&b, "swarm %s %s\n", m.ID, m.Phase)
	fmt.Fprintf(&b, "objective: %s\n", m.Objective)
	fmt.Fprintf(&b, "accountable human: %s\n", m.AccountableHumanID)
	fmt.Fprintf(&b, "integrator: run %s generation %d (%s %s, account %s)\n",
		m.CurrentIntegratorRunID, m.IntegratorGeneration, m.Integrator.Harness, m.Integrator.Mode, m.Integrator.AccountMemberID)
	if m.IntegratorLaunchError != "" {
		fmt.Fprintf(&b, "launch error: %s (since %s)\n", m.IntegratorLaunchError, m.IntegratorLaunchErrorAt)
	}
	fmt.Fprintf(&b, "plan version: %d\n", m.PlanVersion)

	if len(res.Questions) > 0 {
		fmt.Fprintf(&b, "\nquestions (%d open):\n", m.OpenQuestions)
		for _, q := range res.Questions {
			fmt.Fprintf(&b, "  %s %s\n", q.ID, q.Body)
			if q.AnsweredAt == nil {
				b.WriteString("    unanswered\n")
			} else {
				fmt.Fprintf(&b, "    answer: %s\n", q.Answer)
			}
		}
	}
	if len(res.PlanReviews) > 0 {
		b.WriteString("\nplan reviews:\n")
		for _, r := range res.PlanReviews {
			decision := "undecided"
			if r.Decision != "" {
				decision = r.Decision
			}
			fmt.Fprintf(&b, "  v%d submitted from %s at %s: %s\n", r.PlanVersion, r.SubmittedPhase, r.SubmittedAt, decision)
			fmt.Fprintf(&b, "    summary: %s\n", r.Summary)
			if r.Feedback != "" {
				fmt.Fprintf(&b, "    feedback: %s\n", r.Feedback)
			}
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "\ntasks:\nID\tTITLE\tSTATUS\tBLOCKERS"); err != nil {
		return err
	}
	for _, t := range res.Tasks {
		title := ""
		switch {
		case t.Revision != nil && t.PendingRevision != nil:
			// A proposed revision awaiting review may carry a new title; a
			// reader of the swarm sees both, not only the one in force.
			title = fmt.Sprintf("%s (pending rev %d: %s)", cell(t.Revision.Title), t.PendingRevision.Revision, cell(t.PendingRevision.Title))
		case t.Revision != nil:
			title = cell(t.Revision.Title)
		case t.PendingRevision != nil:
			title = cell(t.PendingRevision.Title)
		}
		blockers := make([]string, 0, len(t.Blockers))
		for _, bl := range t.Blockers {
			ref := bl.TaskID
			if bl.OwnerRunID != "" {
				ref = bl.OwnerRunID
			}
			blockers = append(blockers, strings.TrimSpace(bl.Kind+" "+ref))
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.ID, title, t.Status, strings.Join(blockers, ", ")); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(tw, "\nattempts:\nID\tTASK\tSTATE\tRUN"); err != nil {
		return err
	}
	for _, a := range res.Attempts {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.ID, a.TaskID, a.State, a.RunID); err != nil {
			return err
		}
	}
	return tw.Flush()
}
