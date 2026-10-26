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

type handoffInput struct {
	RunID      string `json:"run_id"`
	ToMemberID string `json:"to_member_id" jsonschema:"the teammate's member id; an unknown one fails listing teammates"`
}

type swarmManageInput struct {
	SwarmID string `json:"swarm_id"`
	Action  string `json:"action"`
	RunID   string `json:"run_id,omitempty" jsonschema:"the worker, for release_worker"`
}

type replaceIntegratorInput struct {
	SwarmID            string `json:"swarm_id"`
	ExpectedGeneration uint64 `json:"expected_generation,omitempty" jsonschema:"integrator_generation from aether_get_swarm"`
	Harness            string `json:"harness,omitempty" jsonschema:"one of the swarm's agents; default the current integrator's"`
	Mode               string `json:"mode,omitempty"`
	Model              string `json:"model,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
}

const (
	lifecycleRelaunch  = "relaunch"
	lifecycleStandard  = "standard"
	lifecycleEnhanced  = "enhanced"
	lifecycleProtect   = "protect"
	lifecycleUnprotect = "unprotect"
	lifecycleArchive   = "archive"
	lifecycleRestore   = "restore"

	swarmReleaseWorker = "release_worker"
	swarmArchive       = "archive"
	swarmUnarchive     = "unarchive"
)

var actOps = []Op{
	opOf("aether_run_lifecycle", "Change run lifecycle", ScopeWrite, false, nextRuns,
		"Use this when the user wants a stopped run relaunched, a run switched to standard or enhanced mode, protected from cleanup, or archived or restored. Each action but relaunch sets a state, so a repeat changes nothing; relaunch refuses a live run.", runLifecycle).
		enum("action", lifecycleRelaunch, lifecycleStandard, lifecycleEnhanced, lifecycleProtect, lifecycleUnprotect, lifecycleArchive, lifecycleRestore).
		hints(false, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodRunRelaunch, protocol.MethodRunModeSwitch, protocol.MethodRunProtect, protocol.MethodRunArchive),
	opOf("aether_handoff_run", "Hand off run", ScopeControl, true, "",
		"Use this when the user gives one of their runs to a teammate, who then owns and controls it; only the teammate can hand it back.", handoffRun).
		hints(true, false).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodMemberList, protocol.MethodRunHandoff),
	opOf("aether_swarm_manage", "Manage swarm", ScopeWrite, true, nextSwarms,
		"Use this when the user releases their control of a swarm worker (release_worker, with its run_id) so the integrator steers it again, or archives or unarchives a swarm.", swarmManage).
		enum("action", swarmReleaseWorker, swarmArchive, swarmUnarchive).
		hints(false, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodMissionShow, protocol.MethodMissionWorkerRelease, protocol.MethodMissionArchive, protocol.MethodMissionUnarchive),
	opOf("aether_replace_integrator", "Replace integrator", ScopeControl, true, nextSwarms,
		"Use this when a swarm's integrator stopped or is stuck: it starts a new integrator from the swarm's agents and the old one loses its authority. Fails if the integrator changed since expected_generation."+
			idempotencyRule+"without one, the same replacement at the same generation is made once.", replaceIntegrator).
		enum("mode", "tui", "acp").hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodMissionShow, protocol.MethodMissionReplaceIntegrator),
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
		var res protocol.RunResult
		if err := c.act(ctx, protocol.MethodRunRelaunch, protocol.RunIDParams{RunID: in.RunID}, &res); err != nil {
			return "", nil, err
		}
		out := launched(res.Run, l)
		return fmt.Sprintf("Relaunched run %s (%s).", res.Run.ID, res.Run.Status), out, nil
	case lifecycleStandard, lifecycleEnhanced:
		mode := "tui"
		if in.Action == lifecycleEnhanced {
			mode = "acp"
		}
		method, did, params = protocol.MethodRunModeSwitch, "Switched to "+in.Action+" mode", protocol.RunModeSwitchParams{RunID: in.RunID, Mode: mode}
	case lifecycleProtect, lifecycleUnprotect:
		method, did, params = protocol.MethodRunProtect, cases(in.Action)+"ed", protocol.RunProtectParams{RunID: in.RunID, Protected: in.Action == lifecycleProtect}
	case lifecycleArchive, lifecycleRestore:
		method, did, params = protocol.MethodRunArchive, cases(in.Action)+"d", protocol.RunArchiveParams{RunID: in.RunID, Archived: in.Action == lifecycleArchive}
	default:
		return "", nil, invalid("action %q is not one of relaunch, standard, enhanced, protect, unprotect, archive, restore", in.Action)
	}
	if err := c.act(ctx, method, params, &struct{}{}); err != nil {
		return "", nil, lifecycleError(err)
	}
	return acted(ctx, c.read, l, in.RunID, did)
}

func cases(action string) string {
	return strings.ToUpper(action[:1]) + action[1:]
}

// lifecycleError adds the way forward to a mode switch refused because
// another session holds the run's control: a connector or the assistant
// holds no control lease.
func lifecycleError(err error) error {
	var perr *protocol.Error
	if !errors.As(err, &perr) || !strings.Contains(perr.Message, "holds the run's control") {
		return err
	}
	return &protocol.Error{Code: perr.Code, Data: perr.Data,
		Message: perr.Message + "; this tool holds no control, so ask that member to release it, or switch in the Aether dashboard"}
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
	i := slices.IndexFunc(members.Members, func(m protocol.Member) bool { return m.ID == in.ToMemberID && !m.Pending })
	if i < 0 {
		type teammate struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		}
		var list []string
		for _, m := range members.Members[:min(len(members.Members), maxRows)] {
			if !m.Pending {
				list = append(list, jsonText(teammate{m.ID, clip(m.DisplayName, 80), m.Role}))
			}
		}
		return "", nil, invalid("to_member_id %q is not a teammate; teammates: %s", in.ToMemberID, strings.Join(list, ", "))
	}
	to := members.Members[i]
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
	l, err := dashboardLinks(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
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
	out["held"] = res.TakeoverActive
	return fmt.Sprintf("Released control of worker run %s; the integrator steers it again.", a.RunID), out, nil
}

// replaceIntegrator chooses the replacement among the swarm's execution
// choices, as the dashboard's dialog does: the harness and model given, else
// the current integrator's, on the current integrator's account when it
// offers that agent, else on the member's own, else on the first account
// that does.
func replaceIntegrator(ctx context.Context, c calls, in replaceIntegratorInput) (string, any, error) {
	info, err := serverInfo(ctx, c.read)
	if err != nil {
		return "", nil, err
	}
	l := links(info.DashboardURL)
	var shown protocol.MissionShowResult
	if err := c.read(ctx, protocol.MethodMissionShow, protocol.MissionShowParams{MissionID: in.SwarmID}, &shown); err != nil {
		return "", nil, err
	}
	m := shown.Mission
	expected := cmp.Or(in.ExpectedGeneration, m.IntegratorGeneration)
	if expected != m.IntegratorGeneration {
		return "", nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf(
			"swarm %s's integrator is at generation %d, not %d: it was replaced since; call aether_get_swarm before replacing it again", m.ID, m.IntegratorGeneration, expected)}
	}
	harness := cmp.Or(in.Harness, m.Integrator.Harness)
	model := in.Model
	if in.Harness == "" && model == "" {
		model = m.Integrator.Model
	}
	var offered []protocol.MissionExecutionChoice
	for _, ch := range m.ExecutionChoices {
		if ch.Harness == harness && ch.Model == model {
			offered = append(offered, ch)
		}
	}
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
	choice := protocol.MissionIntegrator{AccountMemberID: account, Harness: harness, Mode: cmp.Or(in.Mode, m.Integrator.Mode), Model: model}
	key := in.IdempotencyKey
	if key == "" {
		sum := sha256.Sum256([]byte(jsonText(choice)))
		key = fmt.Sprintf("ops-replace-%s-%d-%s", m.ID, expected, hex.EncodeToString(sum[:8]))
	}
	var res protocol.MissionReplaceIntegratorResult
	if err := c.act(ctx, protocol.MethodMissionReplaceIntegrator, protocol.MissionReplaceIntegratorParams{MissionID: m.ID, ExpectedGeneration: expected,
		Integrator: choice, IdempotencyKey: key}, &res); err != nil {
		return "", nil, retryable(err, key)
	}
	out := map[string]any{"swarm_id": m.ID, "integrator_run_id": res.RunID, "integrator_generation": res.Mission.IntegratorGeneration,
		"harness": choice.Harness, "mode": choice.Mode, "model": choice.Model, "account_member_id": choice.AccountMemberID,
		"idempotency_key": key, "url": l.swarm(m.ID), "next": "poll aether_get_swarm with swarm_id"}
	return fmt.Sprintf("Replaced swarm %s's integrator with %s (%s); its run is %s.", m.ID, choice.Harness, choice.Mode, cmp.Or(res.RunID, "not started yet")), out, nil
}
