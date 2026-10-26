package ops

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/3xDevOps/Aether/internal/fallback"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// ContractVersion is the version of the ops' inputs and results; it rises
// when one changes in a way a client that relied on it would notice.
const ContractVersion = 1

// modeNames are the dashboard's names of the launch modes.
var modeNames = map[string]string{"tui": "Standard", "headless": "Background", "acp": "Enhanced"}

const (
	accessOwn    = "own"
	accessShared = "shared"
	accessOther  = "other"
)

// accountRef is an agent account: the member whose vendor login a run
// uses, and whether it is the caller's own, shared with the caller, or
// neither, such as a teammate's own run.
type accountRef struct {
	MemberID string `json:"member_id"`
	Name     string `json:"name,omitempty"`
	Access   string `json:"access"`
}

// accountBook is account.list: the accounts the caller may launch on, their
// own first.
type accountBook []protocol.Member

func accounts(ctx context.Context, read Call) (accountBook, error) {
	var res protocol.AccountListResult
	if err := read(ctx, protocol.MethodAccountList, nil, &res); err != nil {
		return nil, err
	}
	return res.Accounts, nil
}

func (b accountBook) self() string {
	if len(b) == 0 {
		return ""
	}
	return b[0].ID
}

func (b accountBook) ref(id string) *accountRef {
	if id == "" {
		return nil
	}
	i := slices.IndexFunc(b, func(m protocol.Member) bool { return m.ID == id })
	switch {
	case i == 0:
		return &accountRef{MemberID: id, Name: b[i].DisplayName, Access: accessOwn}
	case i > 0:
		return &accountRef{MemberID: id, Name: b[i].DisplayName, Access: accessShared}
	}
	return &accountRef{MemberID: id, Access: accessOther}
}

// usable refuses an account the caller may not launch on, before a swarm
// names it for a worker, which the server checks only when the worker
// starts.
func (b accountBook) usable(id string) error {
	if id == "" || slices.ContainsFunc(b, func(m protocol.Member) bool { return m.ID == id }) {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
		"account %s is not shared with you; aether_launch_options lists the accounts you may use", id)}
}

// fallbackPolicy is where a launch may move when its account's usage is
// exhausted: automatic, the accounts in order, or off.
type fallbackPolicy struct {
	Policy string   `json:"policy"`
	Order  []string `json:"order,omitempty"`
}

func policyOf(order []string, off bool) fallbackPolicy {
	switch {
	case off:
		return fallbackPolicy{Policy: "off"}
	case len(order) > 0:
		return fallbackPolicy{Policy: "ordered", Order: order}
	}
	return fallbackPolicy{Policy: "automatic"}
}

// choice is an agent a run or swarm role runs: its harness, mode and model
// on an account. No model is the agent's default.
type choice struct {
	Harness  string      `json:"harness"`
	Mode     string      `json:"mode"`
	ModeName string      `json:"mode_name,omitempty"`
	Model    string      `json:"model,omitempty"`
	Account  *accountRef `json:"account,omitempty"`
}

func choiceOf(harness, mode, model, account string, book accountBook) choice {
	return choice{Harness: harness, Mode: mode, ModeName: modeNames[mode], Model: model, Account: book.ref(account)}
}

type repoRef struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name,omitempty"`
	Primary     bool   `json:"primary,omitempty"`
}

// launchReceipt is what a launch started and on what, the result field
// launch of aether_launch_run, aether_launch_swarm, aether_launch_template,
// aether_get_run and aether_get_swarm. RequestedAccount is set when usage
// fallback moved a run off the account asked for.
type launchReceipt struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	*choice
	RequestedAccount *accountRef               `json:"requested_account,omitempty"`
	Integrator       *choice                   `json:"integrator,omitempty"`
	Workers          []choice                  `json:"workers,omitempty"`
	Fallback         fallbackPolicy            `json:"fallback"`
	Skipped          []protocol.SkippedAccount `json:"skipped_fallback_accounts,omitempty"`
	Repos            []repoRef                 `json:"repos,omitempty"`
	Via              string                    `json:"via,omitempty"`
	IdempotencyKey   string                    `json:"idempotency_key,omitempty"`
	URL              string                    `json:"url,omitempty"`
}

func runReceipt(r protocol.Run, l links, book accountBook) *launchReceipt {
	run := choiceOf(r.Harness, r.Mode, r.Model, r.AccountMemberID, book)
	out := &launchReceipt{Kind: "run", ID: r.ID, WorkspaceID: r.WorkspaceID, choice: &run,
		RequestedAccount: book.ref(r.RequestedAccountMemberID), Fallback: policyOf(r.FallbackAccountMemberIDs, r.NoFallback), Via: r.Via, URL: l.run(r.ID)}
	for _, repo := range r.Repos {
		out.Repos = append(out.Repos, repoRef{WorkspaceID: repo.WorkspaceID, Name: repo.Name, Primary: repo.Primary})
	}
	return out
}

func swarmReceipt(m protocol.Mission, l links, book accountBook) *launchReceipt {
	in := m.Integrator
	integrator := choiceOf(in.Harness, in.Mode, in.Model, in.AccountMemberID, book)
	out := &launchReceipt{Kind: "swarm", ID: m.ID, WorkspaceID: m.WorkspaceID, Integrator: &integrator,
		Fallback: policyOf(m.FallbackAccountMemberIDs, m.NoFallback), URL: l.swarm(m.ID)}
	for _, c := range m.ExecutionChoices {
		out.Workers = append(out.Workers, choiceOf(c.Harness, c.Mode, c.Model, c.AccountMemberID, book))
	}
	for _, id := range m.ExtraWorkspaceIDs {
		out.Repos = append(out.Repos, repoRef{WorkspaceID: id})
	}
	return out
}

type launchOptionsInput struct {
	WorkspaceID     string `json:"workspace_id,omitempty" jsonschema:"add its templates"`
	Models          string `json:"models,omitempty" jsonschema:"an agent name: add its models"`
	AccountMemberID string `json:"account_member_id,omitempty"`
}

type launchTemplateInput struct {
	WorkspaceID    string            `json:"workspace_id"`
	Name           string            `json:"name"`
	Params         map[string]string `json:"params,omitempty" jsonschema:"values for the template's placeholders"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

var launchOps = []Op{
	opOf("aether_launch_options", "Launch options", ScopeRead, true, nextWorkspaces,
		"Use this when choosing what to launch: workspaces, your accounts with usage, their agents and modes, swarm eligibility, fallback; "+
			"templates with workspace_id, models with models.", launchOptions).
		offer(TierRead).calling(protocol.MethodServerInfo, protocol.MethodWorkspaceList, protocol.MethodAccountList, protocol.MethodAgentList,
		protocol.MethodAccountUsage, protocol.MethodTemplateList, protocol.MethodAgentModels),
	opOf("aether_launch_template", "Launch template", ScopeWrite, false, "call aether_launch_options with workspace_id for its templates",
		"Use this when the user asks to run a saved template, on your own account."+
			idempotencyRule+"without one, an identical call still starting returns its run.", launchTemplate).hints(false, true).
		offer(TierConfirmed).calling(protocol.MethodServerInfo, protocol.MethodAccountList, protocol.MethodTemplateLaunch),
}

// agentRow is an agent on one account. Refused is why a launch on the
// account would be refused; Modes are the modes a run may start in, and
// Swarm the modes an integrator or a worker may.
type agentRow struct {
	Name        string   `json:"name"`
	Installed   bool     `json:"installed"`
	SignedIn    *bool    `json:"signed_in,omitempty"`
	DefaultMode string   `json:"default_mode"`
	Modes       []string `json:"modes"`
	Integrator  []string `json:"integrator_modes"`
	Worker      string   `json:"worker_mode,omitempty"`
	Refused     string   `json:"refused,omitempty"`
}

// rowOfAgent follows the dashboard's launch dialog: Enhanced needs it
// installed, a swarm agent needs a mode Aether can wake, and Background
// never runs an integrator.
func rowOfAgent(a protocol.AgentInfo) agentRow {
	row := agentRow{Name: a.Name, Installed: a.Installed, SignedIn: a.LoginFound, DefaultMode: a.DefaultMode, Modes: []string{"tui", "headless"}, Integrator: []string{}}
	switch {
	case !a.Installed:
		row.Refused = "not installed"
	case a.LoginMissing:
		row.Refused = "the account's owner is not signed in to it"
	case a.OwnAccountOnly:
		row.Refused = "a member's own agent runs only on their own account"
	case a.Unavailable != "":
		row.Refused = a.Unavailable
	}
	enhanced := a.Enhanced != "none" && a.EnhancedInstalled
	if enhanced {
		row.Modes = append([]string{"acp"}, row.Modes...)
		row.Integrator = append(row.Integrator, "acp")
	}
	if a.SwarmStandardRefusal == "" {
		row.Integrator = append(row.Integrator, "tui")
	}
	if row.Refused == "" {
		row.Worker = slices.Concat(row.Integrator, []string{"headless"})[0]
	}
	return row
}

type usageWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    *string `json:"resets_at,omitempty"`
}

type usageRow struct {
	Provider string        `json:"provider"`
	Status   string        `json:"status"`
	Windows  []usageWindow `json:"windows,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type accountRow struct {
	accountRef
	// Ready is whether any agent can launch on the account.
	Ready      bool       `json:"ready"`
	Usage      []usageRow `json:"usage"`
	UsageError string     `json:"usage_error,omitempty"`
	Agents     []agentRow `json:"agents"`
	AgentError string     `json:"agents_error,omitempty"`
}

// optionReads bounds accountRows' calls in flight: the assistant's socket
// takes 16 connections at once and refuses the rest.
const optionReads = 4

func accountRows(ctx context.Context, read Call, book accountBook) []accountRow {
	rows := make([]accountRow, len(book))
	var wg sync.WaitGroup
	slots := make(chan struct{}, optionReads)
	bounded := func(ctx context.Context, method string, params, out any) error {
		slots <- struct{}{}
		defer func() { <-slots }()
		return read(ctx, method, params, out)
	}
	for i := range book {
		rows[i].accountRef = *book.ref(book[i].ID)
		param := book[i].ID
		if i == 0 {
			param = ""
		}
		wg.Go(func() {
			var agents protocol.AgentListResult
			if err := bounded(ctx, protocol.MethodAgentList, protocol.AgentListParams{AccountMemberID: param}, &agents); err != nil {
				rows[i].AgentError = err.Error()
			}
			rows[i].Agents = []agentRow{}
			for _, a := range agents.Agents {
				row := rowOfAgent(a)
				rows[i].Ready = rows[i].Ready || row.Refused == ""
				rows[i].Agents = append(rows[i].Agents, row)
			}
		})
		wg.Go(func() {
			var usage protocol.AccountUsageResult
			if err := bounded(ctx, protocol.MethodAccountUsage, protocol.AccountUsageParams{AccountMemberID: param}, &usage); err != nil {
				rows[i].UsageError = err.Error()
			}
			rows[i].Usage = []usageRow{}
			for _, p := range usage.Providers {
				row := usageRow{Provider: p.Provider, Status: p.Status, Error: clip(p.Error, maxReport)}
				for _, w := range p.Windows {
					row.Windows = append(row.Windows, usageWindow{Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt})
				}
				rows[i].Usage = append(rows[i].Usage, row)
			}
		})
	}
	wg.Wait()
	return rows
}

func launchOptions(ctx context.Context, c calls, in launchOptionsInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	book, err := accounts(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var ws protocol.WorkspaceListResult
	if err := c.read(ctx, protocol.MethodWorkspaceList, nil, &ws); err != nil {
		return "", nil, err
	}
	workspaces := make([]repoRef, 0, len(ws.Workspaces))
	for _, w := range ws.Workspaces {
		workspaces = append(workspaces, repoRef{WorkspaceID: w.ID, Name: w.Name})
	}
	rows := accountRows(ctx, c.read, book)
	out := map[string]any{
		"server":     map[string]any{"version": info.ServerVersion, "ops_contract": ContractVersion},
		"workspaces": workspaces, "accounts": rows, "modes": modeNames,
		"fallback": fmt.Sprintf("Without fallback_account_member_ids or no_fallback, a launch whose account has used %d%% of a usage window "+
			"moves to the usable account with the most usage left. Usage is unknown for agents with no usage row.", fallback.ExhaustedPercent),
	}
	summary := fmt.Sprintf("%d workspaces, %d accounts.", len(workspaces), len(rows))
	if in.WorkspaceID != "" {
		var res protocol.TemplateListResult
		if err := c.read(ctx, protocol.MethodTemplateList, protocol.TemplateListParams{WorkspaceID: in.WorkspaceID}, &res); err != nil {
			return "", nil, err
		}
		type template struct {
			Name    string   `json:"name"`
			Harness string   `json:"harness"`
			Mode    string   `json:"mode"`
			Model   string   `json:"model,omitempty"`
			Params  []string `json:"params,omitempty"`
		}
		templates, tasks := []template{}, map[string]string{}
		for _, t := range res.Templates[:min(len(res.Templates), maxRows)] {
			params := slices.Sorted(maps.Keys(t.Params))
			templates = append(templates, template{Name: t.Name, Harness: t.Harness, Mode: t.Mode, Model: t.Model, Params: params})
			tasks[t.Name] = clip(t.Task, maxReport)
		}
		out["templates"], out["untrusted"] = templates, map[string]any{"template_tasks": tasks}
		summary += fmt.Sprintf(" %d templates.", len(templates))
		if len(res.Templates) > len(templates) {
			out["templates_total"] = len(res.Templates)
		}
	}
	if in.Models != "" {
		if err := book.usable(in.AccountMemberID); err != nil {
			return "", nil, err
		}
		var res protocol.AgentModelsResult
		if err := c.read(ctx, protocol.MethodAgentModels, protocol.AgentModelsParams{Name: in.Models, AccountMemberID: in.AccountMemberID}, &res); err != nil {
			return "", nil, err
		}
		models := make([]string, 0, min(len(res.Models), maxRows))
		for _, m := range res.Models[:min(len(res.Models), maxRows)] {
			models = append(models, clip(m.ID, maxReport))
		}
		listed := map[string]any{"agent": in.Models, "source": res.Source, "default": res.DefaultLabel, "ids": models, "fetched_at": res.FetchedAt}
		if res.Error != "" {
			listed["error"] = clip(res.Error, maxReport)
		}
		if len(res.Models) > len(models) {
			listed["total"] = len(res.Models)
		}
		if len(models) == 0 {
			listed["note"] = "Its models are unknown; launch without model for the agent's default, or pass the id the agent takes."
		}
		out["models"] = listed
		summary += fmt.Sprintf(" %d models of %s (%s).", len(models), in.Models, res.Source)
	}
	return summary, out, nil
}

func launchTemplate(ctx context.Context, c calls, in launchTemplateInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	book, err := accounts(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	params := protocol.TemplateLaunchParams{WorkspaceID: in.WorkspaceID, Name: in.Name, Params: in.Params, IdempotencyKey: in.IdempotencyKey}
	dup, done := false, func(error) {}
	if params.IdempotencyKey == "" {
		params.IdempotencyKey, dup, _, done, err = keyless(info.Member.ID, protocol.MethodTemplateLaunch, params, func() (bool, error) { return false, nil })
		if err != nil {
			return "", nil, err
		}
	}
	res, finished, err := launch[protocol.TemplateLaunchResult](ctx, c, protocol.MethodTemplateLaunch, params, done)
	if err != nil && finished {
		return "", nil, retryable(err, params.IdempotencyKey)
	}
	if res.Run.ID == "" {
		return starting("aether_launch_template", params.IdempotencyKey, err)
	}
	receipt := runReceipt(res.Run, l, book)
	receipt.IdempotencyKey = params.IdempotencyKey
	out := map[string]any{"run_id": res.Run.ID, "state": res.Run.Status, "url": l.run(res.Run.ID), "launch": receipt, "next": "poll aether_get_run with run_id"}
	switch {
	case dup:
		out["duplicate"] = true
		return fmt.Sprintf("You are already launching this template; returning run %s.", res.Run.ID), out, nil
	case !finished:
		out["state"], out["write"] = "starting", writePending
		return fmt.Sprintf("Run %s is starting from template %s.", res.Run.ID, in.Name), out, nil
	case res.Replayed:
		out["replayed"] = true
		return fmt.Sprintf("This idempotency_key already launched run %s; nothing new was launched.", res.Run.ID), out, nil
	}
	return fmt.Sprintf("Launched run %s from template %s (%s).", res.Run.ID, in.Name, res.Run.Status), out, nil
}
