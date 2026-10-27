package ops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/3xDevOps/Aether/internal/protocol"
)

const (
	savedApply  = "apply"
	savedIgnore = "ignore"
)

// defaultSkip is a saved launch choice a launch did not use, by the input
// field it would have filled, and why.
type defaultSkip = protocol.AssistantDefaultSkip

// savedFill is what the member's saved launch choices filled into a
// launch: the input fields, and the choices it skipped.
type savedFill struct {
	Applied   []string
	Skipped   []defaultSkip
	Unchecked []defaultSkip
	// modelCheckedOn is the account get checked the saved model on, when the
	// launch used it and no note says it went unchecked.
	modelCheckedOn string
	// Account is the saved account, for a swarm worker that names none, or
	// AccountErr why it cannot be used.
	Account    string
	AccountErr error
}

func (f *savedFill) apply(field string) { f.Applied = append(f.Applied, field) }

func (f *savedFill) skip(field, reason string) {
	f.Skipped = append(f.Skipped, defaultSkip{Field: field, Reason: reason})
}

func (f savedFill) stamp(r *launchReceipt) {
	r.DefaultsApplied, r.DefaultsSkipped, r.DefaultsUnchecked = f.Applied, f.Skipped, f.Unchecked
}

// startedOn notes the saved model unchecked when usage fallback started the
// run on another account than the one get checked it on.
func (f *savedFill) startedOn(account string) {
	if f.modelCheckedOn != "" && account != "" && account != f.modelCheckedOn {
		f.Unchecked = append(f.Unchecked, defaultSkip{Field: "model",
			Reason: fmt.Sprintf("it was saved and checked for account %s, and usage fallback started the run on %s", f.modelCheckedOn, account)})
	}
}

// savedDefaults reads the caller's saved launch choices for a launch that
// applies them; one that ignores them gets none.
func savedDefaults(ctx context.Context, read Call, use string) (protocol.LaunchDefaultsResult, error) {
	var res protocol.LaunchDefaultsResult
	switch use {
	case "", savedApply:
	case savedIgnore:
		return res, nil
	default:
		return res, invalid("saved_defaults %q is not apply or ignore", use)
	}
	if err := read(ctx, protocol.MethodLaunchDefaultsGet, nil, &res); err != nil {
		return res, fmt.Errorf("read your saved launch defaults (pass saved_defaults ignore to launch without them): %w", err)
	}
	return res, nil
}

// savedIssue is why one of issues, launch.defaults.get's invalid or unknown
// list, names the saved field's value.
func savedIssue(issues []protocol.LaunchDefaultIssue, field, value string) (string, bool) {
	i := slices.IndexFunc(issues, func(is protocol.LaunchDefaultIssue) bool { return is.Field == field && is.Value == value })
	if i < 0 {
		return "", false
	}
	return issues[i].Reason, true
}

// agentLister lists, once per account, the agents a launch may use on it.
func agentLister(ctx context.Context, read Call, book accountBook) func(account string) ([]protocol.AgentInfo, error) {
	listed := map[string][]protocol.AgentInfo{}
	return func(account string) ([]protocol.AgentInfo, error) {
		if agents, ok := listed[account]; ok {
			return agents, nil
		}
		param := account
		if account == book.self() {
			param = ""
		}
		var res protocol.AgentListResult
		if err := read(ctx, protocol.MethodAgentList, protocol.AgentListParams{AccountMemberID: param}, &res); err != nil {
			return nil, err
		}
		listed[account] = res.Agents
		return res.Agents, nil
	}
}

// launchSlots are the fields of a launch's input the saved choices may
// fill.
type launchSlots struct {
	harness, mode, model *string
	account              *accountChoice
	// integrator is a swarm's: its modes are an integrator's.
	integrator bool
}

// fillSaved fills each field in slots the caller left empty from saved,
// explicit fields first, then saved, then the server's own defaults. An
// unusable saved account fails the launch rather than starting it on
// another; an unusable saved agent, mode or fallback member is skipped with
// its reason. The agent and mode are checked on the account the launch
// resolved to. get checked the model on the saved account, and only ever
// reports it unknown, so a saved model is used and reported unchecked when
// get could not check it or the launch uses another account; the agent
// refuses a model it does not offer when the run starts.
func fillSaved(saved protocol.LaunchDefaultsResult, slots launchSlots, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) (savedFill, error) {
	var fill savedFill
	d := saved.Defaults
	if d.AccountMemberID != "" {
		if reason, bad := savedIssue(saved.Invalid, "account_member_id", d.AccountMemberID); bad {
			fill.AccountErr = invalid("saved account %s: %s; pass account_member_id or clear it", d.AccountMemberID, reason)
		} else {
			fill.Account = d.AccountMemberID
		}
	}
	a := slots.account
	if a.AccountMemberID == "" && d.AccountMemberID != "" {
		if fill.AccountErr != nil {
			return fill, fill.AccountErr
		}
		a.AccountMemberID = fill.Account
		fill.apply("account_member_id")
	}
	if f := d.Fallback; f != nil && len(a.FallbackAccountMemberIDs) == 0 && !a.NoFallback {
		switch f.Policy {
		case protocol.FallbackOff:
			a.NoFallback = true
		case protocol.FallbackOrdered:
			for _, id := range f.Order {
				if reason, bad := savedIssue(saved.Invalid, "fallback.order", id); bad {
					fill.skip("fallback_account_member_ids", id+": "+reason)
					continue
				}
				a.FallbackAccountMemberIDs = append(a.FallbackAccountMemberIDs, id)
			}
			// An empty list is automatic fallback, which was not saved.
			a.NoFallback = len(a.FallbackAccountMemberIDs) == 0
		}
		fill.apply("fallback")
	}
	if *slots.harness == "" && d.Agent != "" {
		if reason := agentRefused(d.Agent, a.AccountMemberID, agentsOn, book); reason != "" {
			fill.skip("harness", reason)
		} else {
			*slots.harness = d.Agent
			fill.apply("harness")
		}
	}
	if *slots.harness == "" {
		why := "you have no saved agent"
		if i := slices.IndexFunc(fill.Skipped, func(s defaultSkip) bool { return s.Field == "harness" }); i >= 0 {
			why = fmt.Sprintf("your saved agent %s cannot launch: %s", d.Agent, fill.Skipped[i].Reason)
		}
		return fill, invalid("harness is required: %s; aether_launch_options lists the agents", why)
	}
	choice, ok := d.Agents[*slots.harness]
	if !ok {
		return fill, nil
	}
	if *slots.mode == "" && choice.Mode != "" {
		if reason := modeRefused(*slots.harness, choice.Mode, slots.integrator, a.AccountMemberID, agentsOn, book); reason != "" {
			fill.skip("mode", reason)
		} else {
			*slots.mode = choice.Mode
			fill.apply("mode")
		}
	}
	if *slots.model == "" && choice.Model != "" {
		*slots.model = choice.Model
		fill.apply("model")
		checked, launched := cmp.Or(d.AccountMemberID, book.self()), cmp.Or(a.AccountMemberID, book.self())
		if checked != launched {
			fill.Unchecked = append(fill.Unchecked, defaultSkip{Field: "model",
				Reason: fmt.Sprintf("it was saved and checked for account %s, and this launch runs on %s", checked, launched)})
		} else if reason, unknown := savedIssue(saved.Unknown, "agents."+*slots.harness+".model", choice.Model); unknown {
			fill.Unchecked = append(fill.Unchecked, defaultSkip{Field: "model", Reason: reason})
		} else {
			fill.modelCheckedOn = checked
		}
	}
	return fill, nil
}

// agentOn is harness as listed on account, or why it cannot be read.
func agentOn(harness, account string, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) (protocol.AgentInfo, string) {
	agents, err := agentsOn(cmp.Or(account, book.self()))
	if err != nil {
		return protocol.AgentInfo{}, "could not list the agents to check it: " + err.Error()
	}
	i := slices.IndexFunc(agents, func(info protocol.AgentInfo) bool { return info.Name == harness })
	if i < 0 {
		return protocol.AgentInfo{}, fmt.Sprintf("no agent named %q is available on this account", harness)
	}
	return agents[i], ""
}

// agentRefused is why harness cannot launch on account, as
// aether_launch_options says, or "".
func agentRefused(harness, account string, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) string {
	info, reason := agentOn(harness, account, agentsOn, book)
	if reason != "" {
		return reason
	}
	return rowOfAgent(info).Refused
}

// modeRefused is why harness cannot start in mode on account, as
// aether_launch_options lists its modes, or "".
func modeRefused(harness, mode string, integrator bool, account string, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) string {
	info, reason := agentOn(harness, account, agentsOn, book)
	if reason != "" {
		return reason
	}
	row := rowOfAgent(info)
	allowed := row.Modes
	if integrator {
		allowed = row.Integrator
		if mode == "tui" && info.SwarmStandardRefusal != "" {
			return info.SwarmStandardRefusal
		}
	}
	if slices.Contains(allowed, mode) {
		return ""
	}
	names := make([]string, len(allowed))
	for j, m := range allowed {
		names[j] = modeNames[m]
	}
	return fmt.Sprintf("%s cannot start in %s mode on this account; it offers %s", harness, cmp.Or(modeNames[mode], mode), cmp.Or(strings.Join(names, ", "), "none"))
}

// savedConflict adds to a keyed retry's refusal that the saved choices it
// was filled from may have changed since the key's first launch.
func savedConflict(err error, fill savedFill, keyed bool) error {
	var perr *protocol.Error
	if !keyed || len(fill.Applied) == 0 || !errors.As(err, &perr) || perr.Code != protocol.CodeConflict {
		return err
	}
	return &protocol.Error{Code: perr.Code, Data: perr.Data,
		Message: perr.Message + "; your saved defaults may have changed since the first attempt; pass the fields explicitly"}
}
