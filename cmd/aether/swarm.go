package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const swarmCreateUsage = "usage: aether swarm create \"<objective>\" --integrator <harness> [--account <member-id>] [--worker <harness[:tui|headless]>] [--max-concurrent-attempts <n>] [--max-total-attempts <n>] [--workspace <name-or-id>]"

func init() {
	register(command{
		name:  "swarm",
		short: "create and inspect swarms",
		run:   runSwarm,
	})
}

func runSwarm(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: aether swarm <create|list|show>")
	}
	switch args[0] {
	case "create":
		return runSwarmCreate(args[1:])
	case "list":
		return runSwarmList(args[1:])
	case "show":
		return runSwarmShow(args[1:])
	default:
		return fmt.Errorf("unknown swarm command %q", args[0])
	}
}

type swarmWorkerOption struct {
	harness string
	mode    string
}

type swarmWorkerFlags []string

func (f *swarmWorkerFlags) String() string { return "" }

func (f *swarmWorkerFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type swarmCreateOptions struct {
	objective        string
	integrator       string
	account          string
	workspace        string
	workers          []swarmWorkerOption
	maxConcurrent    int
	maxTotalAttempts int
}

func parseSwarmCreate(args []string) (swarmCreateOptions, error) {
	fs := flag.NewFlagSet("swarm create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	integrator := fs.String("integrator", "", "TUI harness for the swarm integrator")
	account := fs.String("account", "", "member ID whose agent account the integrator and workers use")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	maxConcurrent := fs.Int("max-concurrent-attempts", 2, "maximum workers running at once (1 through 8)")
	maxTotal := fs.Int("max-total-attempts", 8, "maximum worker attempts for the swarm (at least concurrent, at most 128)")
	var workerValues swarmWorkerFlags
	fs.Var(&workerValues, "worker", "allowed worker harness[:tui|headless] (repeatable; default: integrator harness in headless mode)")

	objective := ""
	if len(args) > 0 && args[0] != "--" && !strings.HasPrefix(args[0], "-") {
		objective, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return swarmCreateOptions{}, err
	}
	if objective == "" && fs.NArg() == 1 {
		objective = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return swarmCreateOptions{}, errors.New(swarmCreateUsage)
	}
	objective = strings.TrimSpace(objective)
	*integrator = strings.TrimSpace(*integrator)
	*account = strings.TrimSpace(*account)
	if objective == "" || *integrator == "" {
		return swarmCreateOptions{}, errors.New(swarmCreateUsage)
	}
	if *maxConcurrent < 1 || *maxConcurrent > domain.MaxMissionConcurrentAttempts {
		return swarmCreateOptions{}, fmt.Errorf("--max-concurrent-attempts must be between 1 and %d", domain.MaxMissionConcurrentAttempts)
	}
	if *maxTotal < *maxConcurrent || *maxTotal > domain.MaxMissionTotalAttempts {
		return swarmCreateOptions{}, fmt.Errorf("--max-total-attempts must be at least --max-concurrent-attempts and at most %d", domain.MaxMissionTotalAttempts)
	}

	workers := make([]swarmWorkerOption, 0, len(workerValues))
	if len(workerValues) == 0 {
		workers = append(workers, swarmWorkerOption{harness: *integrator, mode: string(domain.LaunchHeadless)})
	}
	for _, value := range workerValues {
		harness, mode, hasMode := strings.Cut(value, ":")
		if !hasMode {
			mode = string(domain.LaunchHeadless)
		}
		harness = strings.TrimSpace(harness)
		mode = strings.TrimSpace(mode)
		if harness == "" {
			return swarmCreateOptions{}, fmt.Errorf("--worker must name a harness")
		}
		if mode != string(domain.LaunchTUI) && mode != string(domain.LaunchHeadless) {
			return swarmCreateOptions{}, fmt.Errorf("invalid worker mode %q (want tui or headless)", mode)
		}
		workers = append(workers, swarmWorkerOption{harness: harness, mode: mode})
	}
	return swarmCreateOptions{
		objective:        objective,
		integrator:       *integrator,
		account:          *account,
		workspace:        *workspace,
		workers:          workers,
		maxConcurrent:    *maxConcurrent,
		maxTotalAttempts: *maxTotal,
	}, nil
}

func runSwarmCreate(args []string) error {
	return executeSwarmCreate(args, withControl, os.Stdout)
}

func executeSwarmCreate(args []string, openControl func(func(*protocol.Client) error) error, out io.Writer) error {
	options, err := parseSwarmCreate(args)
	if err != nil {
		return err
	}
	return openControl(func(c *protocol.Client) error {
		return createSwarm(c, options, out)
	})
}

func createSwarm(c *protocol.Client, options swarmCreateOptions, out io.Writer) error {
	workspaceID, err := resolveWorkspace(c, options.workspace)
	if err != nil {
		return err
	}
	accountID := options.account
	if accountID == "" {
		var info protocol.ServerInfoResult
		if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil {
			return err
		}
		accountID = info.Member.ID
		if accountID == "" {
			return fmt.Errorf("server.info returned no member ID")
		}
	}

	choices := []protocol.MissionExecutionChoice{{
		AccountMemberID: accountID,
		Harness:         options.integrator,
		Mode:            string(domain.LaunchTUI),
	}}
	for _, worker := range options.workers {
		choices = append(choices, protocol.MissionExecutionChoice{
			AccountMemberID: accountID,
			Harness:         worker.harness,
			Mode:            worker.mode,
		})
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].AccountMemberID != choices[j].AccountMemberID {
			return choices[i].AccountMemberID < choices[j].AccountMemberID
		}
		if choices[i].Harness != choices[j].Harness {
			return choices[i].Harness < choices[j].Harness
		}
		return choices[i].Mode < choices[j].Mode
	})
	uniqueChoices := choices[:0]
	for _, choice := range choices {
		if len(uniqueChoices) > 0 && uniqueChoices[len(uniqueChoices)-1] == choice {
			continue
		}
		uniqueChoices = append(uniqueChoices, choice)
	}
	params := protocol.MissionCreateParams{
		WorkspaceID: workspaceID,
		Objective:   options.objective,
		Integrator: protocol.MissionIntegrator{
			AccountMemberID: accountID,
			Harness:         options.integrator,
			Mode:            string(domain.LaunchTUI),
		},
		ExecutionChoices:      uniqueChoices,
		MaxConcurrentAttempts: options.maxConcurrent,
		MaxTotalAttempts:      options.maxTotalAttempts,
		IdempotencyKey:        cli.NewControlSessionID(),
	}
	var result protocol.MissionCreateResult
	if err := c.Call(protocol.MethodMissionCreate, params, &result); err != nil {
		return err
	}
	return renderMissionCreated(out, result.Mission)
}

func renderMissionCreated(out io.Writer, mission protocol.Mission) error {
	if _, err := fmt.Fprintf(out, "swarm %s %s\n", mission.ID, mission.Phase); err != nil {
		return err
	}
	if mission.CurrentIntegratorRunID != "" {
		if _, err := fmt.Fprintf(out, "integrator run %s\n", mission.CurrentIntegratorRunID); err != nil {
			return err
		}
	}
	if mission.IntegratorLaunchError != "" {
		if _, err := fmt.Fprintf(out, "integrator launch failed: %s\ninspect with: aether swarm show %s\n", mission.IntegratorLaunchError, mission.ID); err != nil {
			return err
		}
	}
	return nil
}

func runSwarmList(args []string) error {
	fs := flag.NewFlagSet("swarm list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: aether swarm list [--workspace <name-or-id>]")
	}
	return withControl(func(c *protocol.Client) error {
		workspaceID, err := resolveWorkspace(c, *workspace)
		if err != nil {
			return err
		}
		missions, err := listMissions(c, workspaceID)
		if err != nil {
			return err
		}
		return renderMissions(os.Stdout, missions)
	})
}

func listMissions(c *protocol.Client, workspaceID string) ([]protocol.Mission, error) {
	var missions []protocol.Mission
	var seen map[string]struct{}
	before := ""
	for {
		var result protocol.MissionListResult
		params := protocol.MissionListParams{WorkspaceID: workspaceID, Limit: 100, Before: before}
		if err := c.Call(protocol.MethodMissionList, params, &result); err != nil {
			return nil, err
		}
		missions = append(missions, result.Missions...)
		if result.NextCursor == "" {
			return missions, nil
		}
		if len(result.Missions) == 0 {
			return nil, fmt.Errorf("mission.list returned an empty page with a next cursor")
		}
		if seen == nil {
			seen = make(map[string]struct{})
		}
		if _, ok := seen[result.NextCursor]; ok {
			return nil, fmt.Errorf("mission.list repeated cursor %q", result.NextCursor)
		}
		seen[result.NextCursor] = struct{}{}
		before = result.NextCursor
	}
}

func renderMissions(out io.Writer, missions []protocol.Mission) error {
	if len(missions) == 0 {
		_, err := fmt.Fprintln(out, "no swarms")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tPHASE\tINTEGRATOR\tOPEN QUESTIONS\tOBJECTIVE")
	for _, mission := range missions {
		integrator := strings.Join([]string{mission.Integrator.AccountMemberID, mission.Integrator.Harness, mission.Integrator.Mode}, "/")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", mission.ID, mission.Phase, integrator, mission.OpenQuestions, mission.Objective)
	}
	return tw.Flush()
}

func runSwarmShow(args []string) error {
	fs := flag.NewFlagSet("swarm show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if len(args) == 0 {
		return fmt.Errorf("usage: aether swarm show <swarm-id>")
	}
	missionID, err := parseLeadingArg(fs, args)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var result protocol.MissionShowResult
		if err := c.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &result); err != nil {
			return err
		}
		return renderMissionShow(os.Stdout, result)
	})
}

func renderMissionShow(out io.Writer, result protocol.MissionShowResult) error {
	m := result.Mission
	if _, err := fmt.Fprintf(out, "swarm %s\nphase %s\nworkspace %s\nobjective %s\n", m.ID, m.Phase, m.WorkspaceID, m.Objective); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "accountable human %s\nintegrator %s/%s/%s\n", m.AccountableHumanID, m.Integrator.AccountMemberID, m.Integrator.Harness, m.Integrator.Mode); err != nil {
		return err
	}
	if m.CurrentIntegratorRunID != "" {
		if _, err := fmt.Fprintf(out, "integrator run %s\n", m.CurrentIntegratorRunID); err != nil {
			return err
		}
	}
	if m.IntegratorLaunchError != "" {
		if _, err := fmt.Fprintf(out, "integrator launch error %s\n", m.IntegratorLaunchError); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "limits %d concurrent, %d attempts total\nplan version %d\nopen questions %d\n", m.MaxConcurrentAttempts, m.MaxTotalAttempts, m.PlanVersion, m.OpenQuestions); err != nil {
		return err
	}
	if err := writeMissionSection(out, "EXECUTION CHOICES", len(m.ExecutionChoices), func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ACCOUNT\tHARNESS\tMODE")
		for _, choice := range m.ExecutionChoices {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", choice.AccountMemberID, choice.Harness, choice.Mode)
		}
		return tw.Flush()
	}); err != nil {
		return err
	}
	if err := writeMissionSection(out, "TASKS", len(result.Tasks), func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tREVISION\tTITLE")
		for _, task := range result.Tasks {
			title := ""
			if task.Revision != nil {
				title = task.Revision.Title
			} else if task.PendingRevision != nil {
				title = task.PendingRevision.Title
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", task.ID, task.Status, task.CurrentRevision, title)
		}
		return tw.Flush()
	}); err != nil {
		return err
	}
	if err := writeMissionSection(out, "ATTEMPTS", len(result.Attempts), func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tTASK\tSTATE\tHARNESS\tMODE\tRUN\tERROR")
		for _, attempt := range result.Attempts {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", attempt.ID, attempt.TaskID, attempt.State, attempt.Harness, attempt.Mode, attempt.RunID, attempt.LastError)
		}
		return tw.Flush()
	}); err != nil {
		return err
	}
	if err := writeMissionSection(out, "SUBMISSIONS", len(result.Submissions), func(w io.Writer) error {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tTASK\tSTATE")
		for _, submission := range result.Submissions {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", submission.ID, submission.TaskID, submission.State)
		}
		return tw.Flush()
	}); err != nil {
		return err
	}
	if err := writeMissionSection(out, "QUESTIONS", len(result.Questions), func(w io.Writer) error {
		for _, question := range result.Questions {
			answer := question.Answer
			if answer == "" {
				answer = "unanswered"
			}
			if _, err := fmt.Fprintf(w, "%s %s\n  %s\n  answer: %s\n", question.ID, question.AskedAt, question.Body, answer); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := writeMissionSection(out, "PLAN REVIEWS", len(result.PlanReviews), func(w io.Writer) error {
		for _, review := range result.PlanReviews {
			if _, err := fmt.Fprintf(w, "version %d %s: %s\n", review.PlanVersion, review.Decision, review.Summary); err != nil {
				return err
			}
			if review.Feedback != "" {
				if _, err := fmt.Fprintf(w, "  feedback: %s\n", review.Feedback); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return writeMissionSection(out, "SCOPE DIAGNOSTICS", len(result.Diagnostics), func(w io.Writer) error {
		for _, diagnostic := range result.Diagnostics {
			if _, err := fmt.Fprintf(w, "%s task %s revision %d run %s: %s\n", diagnostic.Kind, diagnostic.TaskID, diagnostic.TaskRevision, diagnostic.RunID, diagnostic.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeMissionSection(out io.Writer, title string, count int, render func(io.Writer) error) error {
	if count == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "\n%s\n", title); err != nil {
		return err
	}
	return render(out)
}
