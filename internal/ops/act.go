package ops

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

type lifecycleInput struct {
	RunID  string `json:"run_id"`
	Action string `json:"action"`
}

type modeInput struct {
	RunID string `json:"run_id"`
	Mode  string `json:"mode"`
}

type handoffInput struct {
	RunID      string `json:"run_id"`
	ToMemberID string `json:"to_member_id" jsonschema:"member id or exact display name"`
}

type swarmManageInput struct {
	SwarmID string `json:"swarm_id"`
	Action  string `json:"action"`
	RunID   string `json:"run_id,omitempty" jsonschema:"for release_worker"`
}

type replaceIntegratorInput struct {
	SwarmID            string `json:"swarm_id"`
	ExpectedGeneration uint64 `json:"expected_generation" jsonschema:"integrator_generation from aether_get_swarm"`
	Harness            string `json:"harness,omitempty"`
	Mode               string `json:"mode,omitempty"`
	Model              string `json:"model,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
}

const (
	lifecycleRelaunch  = "relaunch"
	lifecycleProtect   = "protect"
	lifecycleUnprotect = "unprotect"
	lifecycleArchive   = "archive"
	lifecycleRestore   = "restore"

	lifecycleStandard = "standard"
	lifecycleEnhanced = "enhanced"

	swarmReleaseWorker = "release_worker"
	swarmArchive       = "archive"
	swarmUnarchive     = "unarchive"
)

var actOps = []Op{
	opOf("aether_run_lifecycle", "Change run lifecycle", ScopeWrite, false, nextRuns,
		"Use this when the user wants a stopped run relaunched, or a run protected from cleanup, archived or restored.", runLifecycle).
		enum("action", lifecycleRelaunch, lifecycleProtect, lifecycleUnprotect, lifecycleArchive, lifecycleRestore).
		hints(false, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodAccountList, protocol.MethodRunGet, protocol.MethodRunRelaunch, protocol.MethodRunProtect,
			protocol.MethodRunArchive),
	opOf("aether_switch_run_mode", "Switch run mode", ScopeControl, true, nextRuns,
		"Use this when the user wants a live run switched to standard or enhanced mode; a turn in progress is discarded.", switchRunMode).
		enum("mode", lifecycleStandard, lifecycleEnhanced).
		hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodRunModeSwitch),
	opOf("aether_handoff_run", "Hand off run", ScopeControl, true, "",
		"Use this when the user gives their run to a teammate, who then owns it; only they can hand it back.", handoffRun).
		hints(true, false).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodMemberList, protocol.MethodRunHandoff),
	opOf("aether_swarm_manage", "Manage swarm", ScopeControl, true, nextSwarms,
		"Use this when the user releases control of a swarm worker, or archives (closing its completed runs) or unarchives a swarm.", swarmManage).
		enum("action", swarmReleaseWorker, swarmArchive, swarmUnarchive).
		hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodMissionShow, protocol.MethodMissionWorkerRelease, protocol.MethodMissionArchive, protocol.MethodMissionUnarchive),
	opOf("aether_replace_integrator", "Replace integrator", ScopeControl, true, nextSwarms,
		"Use this when a swarm's integrator stopped or is stuck: a new one starts from the swarm's agents; fails if it changed since expected_generation."+
			idempotencyRule+"without one, a repeat is made once.", replaceIntegrator).
		enum("mode", "tui", "acp").hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodMissionShow, protocol.MethodAccountList, protocol.MethodAgentList, protocol.MethodMissionReplaceIntegrator),
}

func runLifecycle(ctx context.Context, c calls, in lifecycleInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	var method, did string
	var params any
	switch in.Action {
	case lifecycleRelaunch:
		// Read before relaunching: a failed read after it would fail a call
		// whose relaunch happened.
		book, err := accounts(ctx, c.read)
		if err != nil {
			return "", nil, err
		}
		var res protocol.RunResult
		if err := c.act(ctx, protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: in.RunID}, &res); err != nil {
			return "", nil, err
		}
		out := launched(res.Run, l, book)
		return fmt.Sprintf("Relaunched run %s (%s).", res.Run.ID, res.Run.Status), out, nil
	case lifecycleProtect, lifecycleUnprotect:
		// run.protect revokes the run's control even when the flag is unchanged.
		r, err := getRunRecord(ctx, c.read, in.RunID)
		if err != nil {
			return "", nil, err
		}
		if r.Protected == (in.Action == lifecycleProtect) {
			return acted(ctx, c.read, l, in.RunID, "Already "+in.Action+"ed:")
		}
		method, did, params = protocol.MethodRunProtect, cases(in.Action)+"ed", protocol.RunProtectParams{RunID: in.RunID, Protected: in.Action == lifecycleProtect}
	case lifecycleArchive, lifecycleRestore:
		method, did, params = protocol.MethodRunArchive, cases(in.Action)+"d", protocol.RunArchiveParams{RunID: in.RunID, Archived: in.Action == lifecycleArchive}
	default:
		return "", nil, invalid("action %q is not one of relaunch, protect, unprotect, archive, restore", in.Action)
	}
	if err := c.act(ctx, method, params, &struct{}{}); err != nil {
		return "", nil, err
	}
	return acted(ctx, c.read, l, in.RunID, did)
}

func switchRunMode(ctx context.Context, c calls, in modeInput) (string, any, error) {
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	mode := "tui"
	switch in.Mode {
	case lifecycleStandard:
	case lifecycleEnhanced:
		mode = "acp"
	default:
		return "", nil, invalid("mode %q is not one of standard, enhanced", in.Mode)
	}
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	if r.Mode == mode {
		return acted(ctx, c.read, l, in.RunID, "Already in "+in.Mode+" mode:")
	}
	if err := c.act(ctx, protocol.MethodRunModeSwitch, protocol.RunModeSwitchParams{RunID: in.RunID, Mode: mode}, &struct{}{}); err != nil {
		// A connector or the assistant holds no control lease.
		var perr *protocol.Error
		if errors.As(err, &perr) && strings.Contains(perr.Message, "holds the run's control") {
			return "", nil, &protocol.Error{Code: perr.Code, Data: perr.Data,
				Message: perr.Message + "; this tool holds no control, so ask that member to release it, or switch in the Aether dashboard"}
		}
		return "", nil, err
	}
	return acted(ctx, c.read, l, in.RunID, "Switched to "+in.Mode+" mode:")
}

func cases(action string) string {
	return strings.ToUpper(action[:1]) + action[1:]
}

func handoffRun(ctx context.Context, c calls, in handoffInput) (string, any, error) {
	r, err := getRunRecord(ctx, c.read, in.RunID)
	if err != nil {
		return "", nil, err
	}
	var members protocol.MemberListResult
	if err = c.read(ctx, protocol.MethodMemberList, nil, &members); err != nil {
		return "", nil, err
	}
	var matches []protocol.Member
	for _, m := range members.Members {
		if !m.Pending && (m.ID == in.ToMemberID || strings.EqualFold(m.DisplayName, in.ToMemberID)) {
			matches = append(matches, m)
		}
	}
	if i := slices.IndexFunc(matches, func(m protocol.Member) bool { return m.ID == in.ToMemberID }); i >= 0 {
		matches = matches[i : i+1]
	}
	if len(matches) != 1 {
		type teammate struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		var list []string
		for _, m := range members.Members[:min(len(members.Members), maxRows)] {
			if !m.Pending {
				list = append(list, jsonText(teammate{m.ID, m.Role}))
			}
		}
		what := "is not a teammate"
		if len(matches) > 1 {
			what = "names more than one teammate"
		}
		return "", nil, invalid("to_member_id %s; ask the user which teammate by member id; teammates: %s", what, strings.Join(list, ", "))
	}
	to := matches[0]
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	out := map[string]any{"run_id": r.ID, "owner_id": to.ID, "url": l.run(r.ID), "untrusted": map[string]any{"owner": clip(to.DisplayName, 80)}}
	if r.MemberID == to.ID {
		return fmt.Sprintf("Run %s is already owned by %s.", r.ID, to.ID), out, nil
	}
	if err := c.act(ctx, protocol.MethodRunHandoff, protocol.RunHandoffParams{RunID: r.ID, ToMemberID: to.ID}, &struct{}{}); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Handed run %s to %s.", r.ID, to.ID), out, nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func swarmManage(ctx context.Context, c calls, in swarmManageInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	switch in.Action {
	case swarmArchive, swarmUnarchive:
		method := protocol.MethodMissionArchive
		if in.Action == swarmUnarchive {
			method = protocol.MethodMissionUnarchive
		}
		var res protocol.MissionArchiveResult
		if err := c.act(ctx, method, protocol.MissionIDParams{MissionID: in.SwarmID}, &res); err != nil {
			return "", nil, err
		}
		m := res.Mission
		return fmt.Sprintf("Swarm %s is %sd.", m.ID, in.Action),
			map[string]any{"swarm_id": m.ID, "phase": m.Phase, "archived": m.ArchivedAt != nil, "url": l.swarm(m.ID)}, nil
	case swarmReleaseWorker:
	default:
		return "", nil, invalid("action %q is not one of release_worker, archive, unarchive", in.Action)
	}
	if in.RunID == "" {
		return "", nil, invalid("release_worker takes run_id, the worker's run from aether_get_swarm")
	}
	var shown protocol.MissionShowResult
	if err := c.read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: in.SwarmID}, &shown); err != nil {
		return "", nil, err
	}
	i := slices.IndexFunc(shown.Attempts, func(a protocol.Attempt) bool { return a.RunID == in.RunID })
	if i < 0 {
		return "", nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("swarm %s has no worker run %s; call aether_get_swarm for its workers", in.SwarmID, in.RunID)}
	}
	a := shown.Attempts[i]
	out := map[string]any{"swarm_id": in.SwarmID, "run_id": a.RunID, "url": l.run(a.RunID)}
	if !a.TakeoverActive {
		out["held"] = false
		return fmt.Sprintf("Nobody holds control of worker run %s; the integrator steers it.", a.RunID), out, nil
	}
	var res protocol.MissionWorkerReleaseResult
	if err := c.act(ctx, protocol.MethodMissionWorkerRelease, protocol.MissionWorkerReleaseParams{RunID: a.RunID, ExpectedTakeoverGeneration: a.TakeoverGeneration}, &res); err != nil {
		return "", nil, err
	}
	out["held"], out["released_member_id"] = res.TakeoverActive, a.TakeoverMemberID
	holder := a.TakeoverMemberID + "'s"
	if a.TakeoverMemberID == info.Member.ID {
		holder = "your"
	}
	return fmt.Sprintf("Released %s control of worker run %s; the integrator steers it again.", holder, a.RunID), out, nil
}

func replaceIntegrator(ctx context.Context, c calls, in replaceIntegratorInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	var shown protocol.MissionShowResult
	if err = c.read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: in.SwarmID}, &shown); err != nil {
		return "", nil, err
	}
	m := shown.Mission
	// The server answers a key it already used with that replacement before
	// it compares generations, so a retry of a replacement that went
	// through is not refused as stale.
	expected := in.ExpectedGeneration
	if expected == 0 {
		return "", nil, invalid("expected_generation is required: integrator_generation from aether_get_swarm, now %d", m.IntegratorGeneration)
	}
	harness := cmp.Or(in.Harness, m.Integrator.Harness)
	model := in.Model
	if in.Harness == "" && model == "" {
		model = m.Integrator.Model
	}
	var accounts protocol.AccountListResult
	if err = c.read(ctx, protocol.MethodAccountList, nil, &accounts); err != nil {
		return "", nil, err
	}
	usable := func(id string) bool {
		return slices.ContainsFunc(accounts.Accounts, func(a protocol.Member) bool { return a.ID == id })
	}
	var offered []protocol.MissionExecutionChoice
	for _, ch := range m.ExecutionChoices {
		if ch.Harness == harness && ch.Model == model {
			offered = append(offered, ch)
		}
	}
	if len(offered) > 0 && !slices.ContainsFunc(offered, func(ch protocol.MissionExecutionChoice) bool { return usable(ch.AccountMemberID) }) {
		return "", nil, &protocol.Error{Code: protocol.CodeDenied, Message: fmt.Sprintf(
			"swarm %s offers %s only on accounts that are not yours or shared with you; pick another agent, or ask an account's owner to share it",
			m.ID, strings.TrimSuffix(harness+" "+model, " "))}
	}
	offered = slices.DeleteFunc(offered, func(ch protocol.MissionExecutionChoice) bool { return !usable(ch.AccountMemberID) })
	if len(offered) == 0 {
		var agents []string
		for _, ch := range m.ExecutionChoices {
			agents = append(agents, strings.TrimSuffix(ch.Harness+" "+ch.Model, " "))
		}
		return "", nil, invalid("swarm %s offers no %s; its agents are %s", m.ID, strings.TrimSuffix(harness+" "+model, " "), strings.Join(slices.Compact(slices.Sorted(slices.Values(agents))), ", "))
	}
	account := offered[0].AccountMemberID
	for _, prefer := range []string{info.Member.ID, m.Integrator.AccountMemberID} {
		if slices.ContainsFunc(offered, func(ch protocol.MissionExecutionChoice) bool { return ch.AccountMemberID == prefer }) {
			account = prefer
		}
	}
	mode := in.Mode
	if mode == "" && harness != m.Integrator.Harness {
		// Another agent starts in the first integrator mode it can take, as
		// the launch dialog's integrator does.
		param := account
		if account == info.Member.ID {
			param = ""
		}
		var agents protocol.AgentListResult
		if err = c.read(ctx, protocol.MethodAgentList, protocol.AgentListParams{AccountMemberID: param}, &agents); err != nil {
			return "", nil, fmt.Errorf("list agents to choose the integrator's mode (or pass mode): %w", err)
		}
		if i := slices.IndexFunc(agents.Agents, func(a protocol.AgentInfo) bool { return a.Name == harness }); i >= 0 {
			if modes := rowOfAgent(agents.Agents[i]).Integrator; len(modes) > 0 {
				mode = modes[0]
			}
		}
		if mode == "" {
			if mode, err = protocol.SwarmMode(agents.Agents, harness); err != nil {
				return "", nil, err
			}
		}
	}
	mode = cmp.Or(mode, m.Integrator.Mode)
	if mode == "headless" {
		// Older swarms may have a headless integrator; a replacement must be interactive.
		mode = "acp"
	}
	choice := protocol.MissionIntegrator{AccountMemberID: account, Harness: harness, Mode: mode, Model: model}
	key := in.IdempotencyKey
	if key == "" {
		// The key is the request's, not the resolved choice's: the choice
		// follows the current integrator, which a later replacement changes.
		sum := sha256.Sum256([]byte(jsonText([]string{in.Harness, in.Mode, in.Model})))
		key = fmt.Sprintf("ops-replace-%s-%d-%s", m.ID, expected, hex.EncodeToString(sum[:8]))
	}
	var res protocol.MissionReplaceIntegratorResult
	if err := c.act(ctx, protocol.MethodMissionReplaceIntegrator, protocol.MissionReplaceIntegratorParams{MissionID: m.ID, ExpectedGeneration: expected,
		Integrator: choice, IdempotencyKey: key}, &res); err != nil {
		var perr *protocol.Error
		var data struct {
			Reason string `json:"reason"`
		}
		if errors.As(err, &perr) && perr.Code == protocol.CodeConflict && json.Unmarshal(perr.Data, &data) == nil && data.Reason == protocol.ErrorReasonStaleGeneration {
			return "", nil, &protocol.Error{Code: perr.Code, Data: perr.Data, Message: perr.Message + "; call aether_get_swarm: the integrator was replaced since expected_generation"}
		}
		if perr != nil && perr.Code == protocol.CodeConflict && in.IdempotencyKey == "" && m.IntegratorGeneration > expected+1 {
			return "", nil, &protocol.Error{Code: perr.Code, Data: perr.Data, Message: fmt.Sprintf(
				"%s; swarm %s's integrator was replaced again after this request (now generation %d), so its defaults changed; call aether_get_swarm",
				perr.Message, m.ID, m.IntegratorGeneration)}
		}
		return "", nil, retryable(err, key)
	}
	out := map[string]any{"swarm_id": m.ID, "integrator_run_id": res.RunID, "integrator_generation": res.Mission.IntegratorGeneration,
		"harness": choice.Harness, "mode": choice.Mode, "model": choice.Model, "account_member_id": choice.AccountMemberID,
		"idempotency_key": key, "url": l.swarm(m.ID), "next": "poll aether_get_swarm with swarm_id"}
	return fmt.Sprintf("Replaced swarm %s's integrator with %s (%s); its run is %s.", m.ID, choice.Harness, choice.Mode, cmp.Or(res.RunID, "not started yet")), out, nil
}
