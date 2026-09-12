package sshd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
)

func accountParams(t *testing.T, member domain.MemberID) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.AccountMemberParams{MemberID: string(member)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func launchParams(t *testing.T, workspace domain.WorkspaceID, account domain.MemberID) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(protocol.RunLaunchParams{
		WorkspaceID: string(workspace), Harness: "claude", AccountMemberID: string(account),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSharedAccountLaunchRequiresOwnerGrant(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	grantee := &domain.Member{
		DisplayName: "Grace", TailnetLogin: "grace@example.com",
		Color: "#3cb44b", Role: domain.RoleCollaborator,
	}
	if err := e.store.CreateMember(ctx, grantee); err != nil {
		t.Fatal(err)
	}

	if _, perr := e.srv.runLaunch(ctx, grantee.ID, launchParams(t, e.ws.ID, e.member.ID)); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("launch without grant = %+v, want denied", perr)
	}
	if _, perr := e.srv.accountShare(ctx, e.member.ID, accountParams(t, grantee.ID)); perr != nil {
		t.Fatalf("account.share: %+v", perr)
	}
	result, perr := e.srv.runLaunch(ctx, grantee.ID, launchParams(t, e.ws.ID, e.member.ID))
	if perr != nil {
		t.Fatalf("shared launch: %+v", perr)
	}
	run := result.(protocol.RunResult).Run
	if run.MemberID != string(grantee.ID) || run.AccountMemberID != string(e.member.ID) {
		t.Fatalf("run actor/account = %s/%s, want %s/%s", run.MemberID, run.AccountMemberID, grantee.ID, e.member.ID)
	}

	listed, perr := e.srv.accountList(ctx, grantee.ID, nil)
	if perr != nil {
		t.Fatalf("account.list: %+v", perr)
	}
	accounts := listed.(protocol.AccountListResult).Accounts
	if len(accounts) != 2 || accounts[0].ID != string(grantee.ID) || accounts[1].ID != string(e.member.ID) {
		t.Fatalf("accounts = %+v, want self then shared owner", accounts)
	}

	if _, perr := e.srv.accountRevoke(ctx, e.member.ID, accountParams(t, grantee.ID)); perr != nil {
		t.Fatalf("account.revoke: %+v", perr)
	}
	if _, perr := e.srv.runLaunch(ctx, grantee.ID, launchParams(t, e.ws.ID, e.member.ID)); perr == nil || !strings.Contains(perr.Message, "has not shared") {
		t.Fatalf("launch after revoke = %+v, want explicit denial", perr)
	}
}

// Relaunch reuses the retained container, so a handoff cannot silently
// authorize access to an unshared backing account.
func TestRelaunchRetainedRunRequiresCurrentAccountGrant(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	if err := e.store.UpdateRunStatus(ctx, e.run.ID, domain.RunMerged, "closed; retained container", nil, nil); err != nil {
		t.Fatalf("mark run retained: %v", err)
	}
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.TransferRun(ctx, e.run.ID, grantee.ID); err != nil {
		t.Fatalf("transfer run: %v", err)
	}
	params, err := json.Marshal(protocol.RunIDParams{RunID: string(e.run.ID)})
	if err != nil {
		t.Fatal(err)
	}

	if _, perr := e.srv.runRelaunch(ctx, grantee.ID, params); perr == nil ||
		perr.Code != protocol.CodeDenied ||
		!strings.Contains(perr.Message, "has not shared") {
		t.Fatalf("relaunch without account grant = %+v, want explicit denial", perr)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("scheduler calls after denied relaunch = %v, want none", calls)
	}

	if err := e.store.ShareAccount(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatalf("share account: %v", err)
	}
	result, perr := e.srv.runRelaunch(ctx, grantee.ID, params)
	if perr != nil {
		t.Fatalf("relaunch with account grant: %+v", perr)
	}
	reopened := result.(protocol.RunResult).Run
	if reopened.ID != string(e.run.ID) {
		t.Fatalf("relaunch returned %q, want addressed run %q", reopened.ID, e.run.ID)
	}
	if calls := e.runs.Calls(); len(calls) != 1 || calls[0] != "relaunch:"+string(e.run.ID)+":"+string(grantee.ID) {
		t.Fatalf("scheduler calls after granted relaunch = %v, want addressed run", calls)
	}

	if err := e.store.RevokeAccountShare(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatalf("revoke account share: %v", err)
	}
	if _, perr := e.srv.runRelaunch(ctx, grantee.ID, params); perr == nil ||
		perr.Code != protocol.CodeDenied {
		t.Fatalf("relaunch after account revoke = %+v, want denied", perr)
	}
}

func TestAccountShareCannotTargetSelfOrPendingMember(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	if _, perr := e.srv.accountShare(ctx, e.member.ID, accountParams(t, e.member.ID)); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("self share = %+v, want invalid params", perr)
	}
	pending := &domain.Member{
		DisplayName: "Pending", TailnetLogin: "pending@example.com", Pending: true,
		Color: "#4363d8", Role: domain.RoleCollaborator,
	}
	if err := e.store.CreateMember(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, perr := e.srv.accountShare(ctx, e.member.ID, accountParams(t, pending.ID)); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("pending share = %+v, want invalid params", perr)
	}
}
