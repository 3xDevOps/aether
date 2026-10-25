package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/Aether/internal/cli"
	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/shellquote"
)

func init() {
	register(command{
		name:  "swarm",
		short: "create, follow, and cancel swarms (an integrator run and its workers)",
		usage: "aether swarm create <objective>|- --agent <agent> [--worker <agent>[:<mode>[:<model>]]]... [--workspace <name>]\n" +
			"    [--mode standard|enhanced] [--model <model>] [--account <member-id>] [--fallback <member-id>,...|--no-fallback]\n" +
			"    [--idempotency-key <key>]\n" +
			"aether swarm list [--workspace <name>]\n" +
			"aether swarm show <swarm-id>\n" +
			"aether swarm answer <swarm-id> --question <question-id> \"<answer>\"\n" +
			"aether swarm answer <swarm-id> --question <question-id> -\n" +
			"aether swarm cancel <swarm-id>\n" +
			"aether swarm replace-integrator <swarm-id> --agent <agent> [--model <model>] [--account <member-id>]",
		example: "aether swarm create \"split the parser rewrite across workers\" --agent claude --worker codex\n" +
			"aether swarm list\n" +
			"aether swarm show swarm-1\n" +
			"aether swarm answer swarm-1 --question q-1 \"use the v2 schema\"\n" +
			"aether swarm cancel swarm-1",
		run: runSwarm,
	})
}

const swarmUsage = "usage: aether swarm create \"<objective>\"|- --agent <agent> [--mode standard|enhanced] [--model <model>] [--account <member-id>] [--fallback <member-id>,... | --no-fallback] [--worker <agent>[:standard|enhanced|background[:<model>]]]... [--workspace] [--idempotency-key <key>]\n" +
	"   or: aether swarm list [--workspace]\n" +
	"   or: aether swarm show <swarm-id>\n" +
	"   or: aether swarm answer <swarm-id> --question <question-id> \"<answer>\"|-\n" +
	"   or: aether swarm cancel <swarm-id>\n" +
	"   or: aether swarm replace-integrator <swarm-id> --agent <agent> [--model <model>] [--account <member-id>]"

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
	case "answer":
		return swarmAnswer(args[1:], os.Stdin)
	case "cancel":
		return swarmCancel(args[1:])
	case "replace-integrator":
		return swarmReplaceIntegrator(args[1:])
	}
	return fmt.Errorf("unknown swarm command %q\n%s", args[0], swarmUsage)
}

type swarmSpec struct {
	objective  string
	agent      string
	mode       string
	model      string
	account    string
	workspace  string
	workers    []protocol.MissionExecutionChoice
	fallbacks  []string
	noFallback bool
	key        string
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
		if err := fillSwarmModes(c, &spec); err != nil {
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
		return createSwarm(c, os.Stdout, missionCreateParams(wsID, accountID, spec, cmp.Or(spec.key, cli.NewControlSessionID())))
	})
}

// parseSwarmCreate rejects what the server would refuse before any RPC, so
// a typo never costs a round trip or a stored mission.
func parseSwarmCreate(args []string, stdin io.Reader) (swarmSpec, error) {
	fs := newFlagSet("swarm create")
	agent := fs.String("agent", "", "integrator agent name")
	mode := fs.String("mode", "", "integrator mode: standard or enhanced (default: "+swarmModeDefault+")")
	model := fs.String("model", "", "integrator "+modelHelp)
	account := fs.String("account", "", "member ID whose shared agent account to use (default: yours)")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	fallbackOrder := fs.String("fallback", "", fallbackHelp)
	noFallback := fs.Bool("no-fallback", false, noFallbackHelp)
	key := fs.String("idempotency-key", "", "the key a failed or interrupted create printed: the retry returns that swarm instead of starting another (default: a new key)")
	var workers stringList
	fs.Var(&workers, "worker", "allow workers on this agent, agent[:mode[:model]] with mode standard|enhanced|background; an omitted or empty mode is "+swarmModeDefault+" (repeatable)")
	objective, err := parseLeadingArg(fs, args)
	if err != nil {
		return swarmSpec{}, fmt.Errorf("%w\n%s", err, swarmUsage)
	}
	if *agent == "" {
		return swarmSpec{}, errors.New(swarmUsage)
	}
	objective, err = stdinText(objective, "objective", stdin)
	if err != nil {
		return swarmSpec{}, err
	}
	integratorMode, err := parseIntegratorMode(*mode)
	if err != nil {
		return swarmSpec{}, err
	}
	fallbacks, err := parseFallback(*fallbackOrder, *noFallback)
	if err != nil {
		return swarmSpec{}, err
	}
	if err := domain.ValidModel(*model); err != nil {
		return swarmSpec{}, fmt.Errorf("invalid --model: %w", err)
	}
	spec := swarmSpec{
		objective: objective, agent: *agent, mode: integratorMode, model: *model, account: *account, workspace: *workspace,
		fallbacks: fallbacks, noFallback: *noFallback, key: *key,
	}
	for _, w := range workers {
		choice, parseErr := parseWorker(w)
		if parseErr != nil {
			return swarmSpec{}, parseErr
		}
		spec.workers = append(spec.workers, choice)
	}
	return spec, nil
}

const swarmModeDefault = "enhanced where it is installed, else standard"

// parseIntegratorMode leaves an empty mode for fillSwarmModes.
func parseIntegratorMode(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	mode, err := parseLaunchMode(name)
	if err == nil && mode == string(domain.LaunchHeadless) {
		err = errors.New("the integrator runs until the swarm ends: --mode must be standard or enhanced")
	}
	return mode, err
}

// fillSwarmModes starts each agent spec gives no mode in Enhanced where it
// is installed on the swarm's account, since Aether can wake any Enhanced
// run when mail arrives, and in Standard otherwise.
func fillSwarmModes(c *protocol.Client, spec *swarmSpec) error {
	unset := spec.mode == ""
	for _, w := range spec.workers {
		unset = unset || w.Mode == ""
	}
	if !unset {
		return nil
	}
	mode, err := swarmModes(c, spec.account)
	if err != nil {
		return err
	}
	if spec.mode == "" {
		if spec.mode, err = mode(spec.agent); err != nil {
			return err
		}
	}
	for i, w := range spec.workers {
		if w.Mode == "" {
			if spec.workers[i].Mode, err = mode(w.Harness); err != nil {
				return err
			}
		}
	}
	return nil
}

func swarmModes(c *protocol.Client, account string) (func(agent string) (string, error), error) {
	var listed protocol.AgentListResult
	if err := c.Call(protocol.MethodAgentList, protocol.AgentListParams{AccountMemberID: account}, &listed); err != nil {
		return nil, fmt.Errorf("list agents to choose their modes (or pass every mode): %w", err)
	}
	return func(agent string) (string, error) { return protocol.SwarmMode(listed.Agents, agent) }, nil
}

func stdinText(value, what string, stdin io.Reader) (string, error) {
	if value != "-" {
		return value, nil
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", what, err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", fmt.Errorf("%s on stdin is empty", what)
	}
	return text, nil
}

// The account is the integrator's, filled in by missionCreateParams.
func parseWorker(spec string) (protocol.MissionExecutionChoice, error) {
	parts := strings.SplitN(spec, ":", 3)
	var mode, model string
	var err error
	if len(parts) > 1 && parts[1] != "" {
		mode, err = parseLaunchMode(parts[1])
	}
	if len(parts) > 2 {
		model = parts[2]
	}
	if parts[0] == "" || err != nil || domain.ValidModel(model) != nil {
		return protocol.MissionExecutionChoice{}, fmt.Errorf("invalid --worker %q (want agent, agent:mode or agent:mode:model, mode %s)", spec, launchModeHelp)
	}
	return protocol.MissionExecutionChoice{Harness: parts[0], Mode: mode, Model: model}, nil
}

// A --worker that repeats the integrator or another worker is sent once,
// because the store refuses duplicates.
func missionCreateParams(workspaceID, accountID string, spec swarmSpec, key string) protocol.MissionCreateParams {
	integrator := protocol.MissionExecutionChoice{AccountMemberID: accountID, Harness: spec.agent, Mode: spec.mode, Model: spec.model}
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
		WorkspaceID:      workspaceID,
		Objective:        spec.objective,
		Integrator:       protocol.MissionIntegrator(integrator),
		ExecutionChoices: choices,
		IdempotencyKey:   key,

		FallbackAccountMemberIDs: spec.fallbacks,
		NoFallback:               spec.noFallback,
	}
}

func createSwarm(c *protocol.Client, w io.Writer, params protocol.MissionCreateParams) error {
	var res protocol.MissionCreateResult
	if err := callInterruptibly(c, protocol.MethodMissionCreate, params, &res); err != nil {
		var rpcErr *protocol.Error
		retry := "add --idempotency-key " + shellquote.Quote(params.IdempotencyKey) + " to the same aether swarm create command"
		switch {
		case errors.Is(err, errInterrupted):
			return &exitStatusError{code: 130, err: fmt.Errorf("%w; the swarm may have been created. aether swarm list shows it; to get it without starting another, %s", errInterrupted, retry)}
		case errors.As(err, &rpcErr):
			return err
		}
		return fmt.Errorf("%w\nthe swarm may have been created; aether swarm list shows it. To retry without starting another, %s", err, retry)
	}
	if _, err := fmt.Fprintf(w, "swarm %s %s\nintegrator run %s\n", res.Mission.ID, res.Mission.Phase, res.Mission.CurrentIntegratorRunID); err != nil {
		return fmt.Errorf("write swarm result: %w", err)
	}
	return nil
}

func swarmList(args []string) error {
	fs := newFlagSet("swarm list")
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

// The full objective is on aether swarm show.
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

func cell(s string) string {
	return strings.ReplaceAll(firstLine(s), "\t", " ")
}

func swarmShow(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: aether swarm show <swarm-id>")
	}
	return withControl(func(c *protocol.Client) error {
		res, err := showSwarm(c, args[0])
		if err != nil {
			return err
		}
		return renderSwarm(os.Stdout, res)
	})
}

func showSwarm(c *protocol.Client, missionID string) (protocol.MissionShowResult, error) {
	var res protocol.MissionShowResult
	err := c.Call(protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: missionID}, &res)
	return res, err
}

func renderSwarm(w io.Writer, res protocol.MissionShowResult) error {
	m := res.Mission
	var b strings.Builder
	fmt.Fprintf(&b, "swarm %s %s\n", m.ID, m.Phase)
	fmt.Fprintf(&b, "objective: %s\n", m.Objective)
	fmt.Fprintf(&b, "accountable human: %s\n", m.AccountableHumanID)
	integrator := m.Integrator.Harness + " " + modeName(m.Integrator.Mode)
	if m.Integrator.Model != "" {
		integrator += " " + m.Integrator.Model
	}
	fmt.Fprintf(&b, "integrator: run %s generation %d (%s, account %s)\n",
		m.CurrentIntegratorRunID, m.IntegratorGeneration, integrator, m.Integrator.AccountMemberID)
	if m.IntegratorLaunchError != "" {
		fmt.Fprintf(&b, "launch error: %s (since %s)\n", m.IntegratorLaunchError, m.IntegratorLaunchErrorAt)
	}

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
			// A proposed revision not yet accepted may carry a new title; a
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
	if _, err := fmt.Fprintln(tw, "\nattempts:\nID\tTASK\tSTATE\tRUN\tACCOUNT"); err != nil {
		return err
	}
	for _, a := range res.Attempts {
		account := a.AccountOwnerID
		if a.LaunchAccountID != "" && a.LaunchAccountID != a.AccountOwnerID {
			account = a.LaunchAccountID + " (fallback)"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.TaskID, a.State, a.RunID, account); err != nil {
			return err
		}
	}
	return tw.Flush()
}
