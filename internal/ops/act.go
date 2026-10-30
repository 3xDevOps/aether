package ops

import (
	"context"
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

const (
	lifecycleRelaunch  = "relaunch"
	lifecycleProtect   = "protect"
	lifecycleUnprotect = "unprotect"
	lifecycleArchive   = "archive"
	lifecycleRestore   = "restore"

	lifecycleStandard = "standard"
	lifecycleEnhanced = "enhanced"
)

var actOps = []Op{
	opOf("aether_run_lifecycle", "Change run lifecycle", ScopeWrite, false, nextRuns,
		"Use this when the user wants a stopped run relaunched, or a run protected from cleanup, archived or restored.", runLifecycle).
		enum("action", lifecycleRelaunch, lifecycleProtect, lifecycleUnprotect, lifecycleArchive, lifecycleRestore).
		hints(false, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodAccountList, protocol.MethodRunGet, protocol.MethodRunRelaunch, protocol.MethodRunProtect,
			protocol.MethodRunArchive),
	opOf("aether_switch_run_mode", "Switch run mode", ScopeControl, true, nextRuns,
		"Use this when the user wants a live run's mode switched; a turn in progress is discarded.", switchRunMode).
		enum("mode", lifecycleStandard, lifecycleEnhanced).
		hints(true, true).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodRunModeSwitch),
	opOf("aether_handoff_run", "Hand off run", ScopeControl, true, "",
		"Use this when the user gives their run to a teammate; only they can hand it back.", handoffRun).
		hints(true, false).offer(TierConfirmed).
		calling(protocol.MethodServerInfo, protocol.MethodRunGet, protocol.MethodMemberList, protocol.MethodRunHandoff),
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
