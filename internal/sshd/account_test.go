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
