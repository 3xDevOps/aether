package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

// Launch defaults over real control channels: each member reads and
// replaces only their own, an account they may not use is refused when
// saved, one that stops being usable later is reported with why, still
// saved, and a save over a newer one is refused with the newer one.
func TestLaunchDefaultsBelongToTheCallerAndReportWhatTheyCannotUse(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	boSigner, bo := addMember(t, e, "Bo", domain.RoleCollaborator, false)
	_, cy := addMember(t, e, "Cy", domain.RoleCollaborator, false)
	ana, boClient := controlClient(t, e), controlAs(t, e, boSigner)
	if err := e.store.ShareAccount(ctx, bo.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}

	children := e.store.(store.ChildStore)
	childSettings := store.ChildSettings{Enabled: true, Fallback: false, Configured: true}
	if err := children.PutChildSettings(ctx, e.member.ID, childSettings); err != nil {
		t.Fatal(err)
	}

	var got protocol.LaunchDefaultsResult
	if err := ana.Call(protocol.MethodLaunchDefaultsGet, struct{}{}, &got); err != nil || got.Revision != 0 {
		t.Fatalf("launch.defaults.get before any save = %+v, %v", got, err)
	}
	want := protocol.LaunchDefaults{
		Agent: "claude",
		Agents: map[string]protocol.AgentLaunchDefault{
			"claude":   {Mode: string(domain.LaunchACP), Model: "claude"},
			"codex":    {Model: "gpt-unlisted"},
			"opencode": {Model: "any"},
		},
		AccountMemberID: string(bo.ID),
		Fallback:        &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: []string{string(bo.ID), string(e.member.ID)}},
	}
	set := map[string]any{"defaults": want, "revision": 0, "member_id": string(bo.ID)}
	if err := ana.Call(protocol.MethodLaunchDefaultsSet, set, &got); err != nil {
		t.Fatalf("launch.defaults.set: %v", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got.Defaults)
	if got.Revision == 0 || got.Source != protocol.LaunchSourceMember || got.UpdatedAt == "" || string(gotJSON) != string(wantJSON) {
		t.Fatalf("saved = %+v (%s), want %s", got, gotJSON, wantJSON)
	}
	if len(got.Invalid) != 1 || got.Invalid[0].Field != "agents.codex.model" || got.Invalid[0].Value != "gpt-unlisted" ||
		!strings.Contains(got.Invalid[0].Reason, `does not offer model "gpt-unlisted"`) {
		t.Fatalf("invalid = %+v, want only the model codex does not offer", got.Invalid)
	}
	if len(got.Unknown) != 1 || got.Unknown[0].Field != "agents.opencode.model" {
		t.Fatalf("unknown = %+v, want the opencode model, whose list was never read", got.Unknown)
	}
	saved := got
	if after, err := children.ChildSettings(ctx, e.member.ID); err != nil || after != childSettings {
		t.Fatalf("child settings after launch.defaults.set = %+v, %v; want them untouched, %+v", after, err, childSettings)
	}

	var bos protocol.LaunchDefaultsResult
	if err := boClient.Call(protocol.MethodLaunchDefaultsGet, struct{}{}, &bos); err != nil || bos.Revision != 0 {
		t.Fatalf("Bo's launch defaults = %+v, %v; want none: a member_id param does not reach another member", bos, err)
	}
	if err := boClient.Call(protocol.MethodLaunchDefaultsSet, protocol.LaunchDefaultsSetParams{
		Defaults: &protocol.LaunchDefaults{Agent: "codex"}}, &bos); err != nil || bos.Defaults.Agent != "codex" {
		t.Fatalf("Bo's launch.defaults.set = %+v, %v", bos, err)
	}

	stale := protocol.LaunchDefaultsSetParams{Defaults: &protocol.LaunchDefaults{Agent: "codex"}}
	var pe *protocol.Error
	if err := ana.Call(protocol.MethodLaunchDefaultsSet, stale, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeConflict {
		t.Fatalf("launch.defaults.set over saved defaults with revision 0 = %v, want a conflict", err)
	}
	var current protocol.LaunchDefaultsResult
	if err := json.Unmarshal(pe.Data, &current); err != nil || current.Revision != saved.Revision || current.Defaults.Agent != "claude" {
		t.Fatalf("conflict data = %s, %v; want the saved defaults", pe.Data, err)
	}

	for _, refused := range []struct {
		defaults protocol.LaunchDefaults
		code     int
		want     string
	}{
		{protocol.LaunchDefaults{AccountMemberID: string(cy.ID)}, protocol.CodeDenied, "account_member_id: "},
		{protocol.LaunchDefaults{Fallback: &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: []string{string(cy.ID)}}},
			protocol.CodeDenied, "fallback.order: "},
		{protocol.LaunchDefaults{Agents: map[string]protocol.AgentLaunchDefault{"claude": {Model: "-rf"}}}, protocol.CodeInvalidParams, "agents.claude.model"},
		{protocol.LaunchDefaults{Fallback: &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOff, Order: []string{string(bo.ID)}}},
			protocol.CodeInvalidParams, "fallback.order is set only"},
		{protocol.LaunchDefaults{Fallback: &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: []string{string(bo.ID), string(bo.ID)}}},
			protocol.CodeInvalidParams, "distinct"},
	} {
		d := refused.defaults
		params := protocol.LaunchDefaultsSetParams{Defaults: &d, Revision: saved.Revision}
		if err := ana.Call(protocol.MethodLaunchDefaultsSet, params, nil); !errors.As(err, &pe) || pe.Code != refused.code || !strings.Contains(pe.Message, refused.want) {
			t.Fatalf("launch.defaults.set %+v = %v, want code %d saying %q", d, err, refused.code, refused.want)
		}
	}

	if err := e.store.RevokeAccountShare(ctx, bo.ID, e.member.ID); err != nil {
		t.Fatal(err)
	}
	if err := ana.Call(protocol.MethodLaunchDefaultsGet, struct{}{}, &got); err != nil || got.Defaults.AccountMemberID != string(bo.ID) || got.Revision != saved.Revision {
		t.Fatalf("after the share was revoked = %+v, %v; want the account still saved", got, err)
	}
	reasons := map[string]string{}
	for _, issue := range got.Invalid {
		if issue.Value == string(bo.ID) {
			reasons[issue.Field] = issue.Reason
		}
	}
	for _, field := range []string{"account_member_id", "fallback.order"} {
		if !strings.Contains(reasons[field], "has not shared their account with you") {
			t.Fatalf("invalid = %+v, want %s naming Bo as not shared", got.Invalid, field)
		}
	}

	connector := e.srv.ConnectorLocal(ConnectorGrant{Member: e.member.ID}, "con-test", "Claude", []string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl})
	if _, perr := connector.Call(ctx, protocol.MethodLaunchDefaultsGet, nil); perr != nil {
		t.Fatalf("connector launch.defaults.get = %v", perr)
	}
	clearing, _ := json.Marshal(protocol.LaunchDefaultsSetParams{Clear: true, Revision: saved.Revision})
	if _, perr := connector.Call(ctx, protocol.MethodLaunchDefaultsSet, clearing); perr == nil || perr.Code != protocol.CodeDenied ||
		!strings.Contains(perr.Message, "cannot call this method") {
		t.Fatalf("connector launch.defaults.set = %v, want it refused to every connector", perr)
	}
	byAssistant, _ := json.Marshal(protocol.LaunchDefaultsSetParams{Defaults: &protocol.LaunchDefaults{Agent: "codex"}, Revision: saved.Revision})
	raw, perr := e.srv.AssistantLocal(e.member.ID).Call(ctx, protocol.MethodLaunchDefaultsSet, byAssistant)
	if perr != nil || json.Unmarshal(raw, &got) != nil || got.Source != protocol.LaunchSourceAssistant {
		t.Fatalf("assistant launch.defaults.set = %s, %v; want it recorded as the assistant's", raw, perr)
	}
	clearing, _ = json.Marshal(protocol.LaunchDefaultsSetParams{Clear: true, Revision: got.Revision})
	if err := ana.Call(protocol.MethodLaunchDefaultsSet, json.RawMessage(clearing), &got); err != nil || got.Revision != 0 {
		t.Fatalf("clear = %+v, %v", got, err)
	}
	if err := boClient.Call(protocol.MethodLaunchDefaultsGet, struct{}{}, &bos); err != nil || bos.Revision == 0 || bos.Defaults.Agent != "codex" {
		t.Fatalf("Bo's launch defaults after Ana cleared hers = %+v, %v", bos, err)
	}
}

// Saves are refused over their shape before anything is stored, and to
// members who may not launch.
func TestLaunchDefaultsSetRefusesOversizedShapesAndMembersWhoCannotLaunch(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ana := controlClient(t, e)
	tooMany := map[string]protocol.AgentLaunchDefault{}
	for i := range protocol.LaunchDefaultsMaxAgents + 1 {
		tooMany[fmt.Sprintf("agent%d", i)] = protocol.AgentLaunchDefault{}
	}
	var order []string
	for i := range protocol.LaunchDefaultsMaxFallback + 1 {
		order = append(order, fmt.Sprintf("m-%d", i))
	}
	for _, refused := range []struct {
		defaults protocol.LaunchDefaults
		want     string
	}{
		{protocol.LaunchDefaults{Agents: tooMany}, "at most 32 agents"},
		{protocol.LaunchDefaults{Fallback: &protocol.LaunchFallbackDefault{Policy: protocol.FallbackOrdered, Order: order}}, "1 to 8 member IDs"},
		{protocol.LaunchDefaults{Fallback: &protocol.LaunchFallbackDefault{}}, "fallback.policy must be"},
		{protocol.LaunchDefaults{Agent: strings.Repeat("a", protocol.LaunchDefaultsMaxAgentName+1)}, "longer than 64 bytes"},
		{protocol.LaunchDefaults{Agents: map[string]protocol.AgentLaunchDefault{strings.Repeat("b", protocol.LaunchDefaultsMaxAgentName+1): {}}}, "longer than 64 bytes"},
		{protocol.LaunchDefaults{Agent: "claude\x1b[2J"}, "control character"},
		{protocol.LaunchDefaults{Agents: map[string]protocol.AgentLaunchDefault{"co\x7fdex": {}}}, "control character"},
	} {
		d := refused.defaults
		var pe *protocol.Error
		if err := ana.Call(protocol.MethodLaunchDefaultsSet, protocol.LaunchDefaultsSetParams{Defaults: &d}, nil); !errors.As(err, &pe) ||
			pe.Code != protocol.CodeInvalidParams || !strings.Contains(pe.Message, refused.want) {
			t.Fatalf("launch.defaults.set %+v = %v, want invalid params saying %q", d, err, refused.want)
		}
	}
	if _, err := e.store.LaunchDefaults(context.Background(), e.member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("launch defaults after refused saves = %v, want none saved", err)
	}

	set := protocol.LaunchDefaultsSetParams{Defaults: &protocol.LaunchDefaults{Agent: "claude"}}
	viewerSigner, _ := addMember(t, e, "Vi", domain.RoleViewer, false)
	pendingSigner, _ := addMember(t, e, "Pat", domain.RoleCollaborator, true)
	wantDenied(t, controlAs(t, e, viewerSigner).Call(protocol.MethodLaunchDefaultsSet, set, nil), "viewer launch.defaults.set")
	pending := controlAs(t, e, pendingSigner)
	wantDenied(t, pending.Call(protocol.MethodLaunchDefaultsSet, set, nil), "pending member launch.defaults.set")
	wantDenied(t, pending.Call(protocol.MethodLaunchDefaultsGet, struct{}{}, nil), "pending member launch.defaults.get")
}
