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
	Applied []string
	Skipped []defaultSkip
	// Account is the saved account the launch took, if it took one.
	Account string
}

func (f *savedFill) apply(field string) { f.Applied = append(f.Applied, field) }

func (f *savedFill) skip(field, reason string) {
	f.Skipped = append(f.Skipped, defaultSkip{Field: field, Reason: reason})
}

func (f savedFill) stamp(r *launchReceipt) {
	r.DefaultsApplied, r.DefaultsSkipped = f.Applied, f.Skipped
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

// savedIssue is why launch.defaults.get found the saved field unusable;
// value "" matches any of the field's.
func savedIssue(saved protocol.LaunchDefaultsResult, field, value string) (string, bool) {
	i := slices.IndexFunc(saved.Invalid, func(is protocol.LaunchDefaultIssue) bool {
		return is.Field == field && (value == "" || is.Value == value)
	})
	if i < 0 {
		return "", false
	}
	return saved.Invalid[i].Reason, true
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
// another; an unusable saved agent, mode, model or fallback member is
// skipped with its reason. The mode is checked against the agent on the
// account the launch resolved to, which may not be the one get checked.
func fillSaved(saved protocol.LaunchDefaultsResult, slots launchSlots, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) (savedFill, error) {
	var fill savedFill
	d := saved.Defaults
	if *slots.harness == "" && d.Agent != "" {
		if reason, bad := savedIssue(saved, "agent", ""); bad {
			fill.skip("harness", reason)
		} else {
			*slots.harness = d.Agent
			fill.apply("harness")
		}
	}
	if *slots.harness == "" {
		why := "you have no saved agent"
		if len(fill.Skipped) > 0 {
			why = fmt.Sprintf("your saved agent %s cannot launch: %s", d.Agent, fill.Skipped[0].Reason)
		}
		return fill, invalid("harness is required: %s; aether_launch_options lists the agents", why)
	}
	a := slots.account
	if a.AccountMemberID == "" && d.AccountMemberID != "" {
		if reason, bad := savedIssue(saved, "account_member_id", d.AccountMemberID); bad {
			return fill, invalid("saved account %s: %s; pass account_member_id or clear it", d.AccountMemberID, reason)
		}
		a.AccountMemberID, fill.Account = d.AccountMemberID, d.AccountMemberID
		fill.apply("account_member_id")
	}
	if f := d.Fallback; f != nil && len(a.FallbackAccountMemberIDs) == 0 && !a.NoFallback {
		switch f.Policy {
		case protocol.FallbackOff:
			a.NoFallback = true
		case protocol.FallbackOrdered:
			for _, id := range f.Order {
				if reason, bad := savedIssue(saved, "fallback.order", id); bad {
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
		if reason, bad := savedIssue(saved, "agents."+*slots.harness+".model", choice.Model); bad {
			fill.skip("model", reason)
		} else {
			*slots.model = choice.Model
			fill.apply("model")
		}
	}
	return fill, nil
}

// modeRefused is why harness cannot start in mode on account, as
// aether_launch_options lists its modes, or "".
func modeRefused(harness, mode string, integrator bool, account string, agentsOn func(string) ([]protocol.AgentInfo, error), book accountBook) string {
	agents, err := agentsOn(cmp.Or(account, book.self()))
	if err != nil {
		return "could not list the agents to check it: " + err.Error()
	}
	i := slices.IndexFunc(agents, func(info protocol.AgentInfo) bool { return info.Name == harness })
	if i < 0 {
		return fmt.Sprintf("no agent named %q is available on this account", harness)
	}
	row := rowOfAgent(agents[i])
	allowed := row.Modes
	if integrator {
		allowed = row.Integrator
		if mode == "tui" && agents[i].SwarmStandardRefusal != "" {
			return agents[i].SwarmStandardRefusal
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
		Message: perr.Message + "; your saved defaults changed since the first attempt; pass the fields explicitly"}
}
