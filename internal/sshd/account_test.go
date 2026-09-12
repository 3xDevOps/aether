package sshd

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/store"
)

type relaunchRunLookupGate struct {
	store.Store
	started chan struct{}
	release <-chan struct{}
	mu      sync.Mutex
	blocked bool
}

func (g *relaunchRunLookupGate) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	g.mu.Lock()
	first := !g.blocked
	if first {
		g.blocked = true
	}
	g.mu.Unlock()
	if first {
		close(g.started)
		<-g.release
	}
	return g.Store.GetRun(ctx, id)
}

type launchActorLookupGate struct {
	store.Store
	started chan struct{}
	release <-chan struct{}
	mu      sync.Mutex
	calls   int
}

func (g *launchActorLookupGate) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	g.mu.Lock()
	g.calls++
	block := g.calls == 2
	g.mu.Unlock()
	if block {
		close(g.started)
		<-g.release
	}
	return g.Store.GetMember(ctx, id)
}

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

func TestLaunchRejectsCompletedAccountRevokeBeforeFreshCheck(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	launch := launchParams(t, e.ws.ID, e.member.ID)
	revoke := accountParams(t, grantee.ID)
	releaseLookup := make(chan struct{})
	lookupStarted := make(chan struct{})
	e.srv.cfg.Store = &launchActorLookupGate{
		Store:   e.store,
		started: lookupStarted,
		release: releaseLookup,
	}

	launchDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.dispatch(ctx, grantee.ID, protocol.MethodRunLaunch, launch)
		launchDone <- perr
	}()
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("launch guard did not reach actor check")
	}

	revokeDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.dispatch(ctx, e.member.ID, protocol.MethodAccountRevoke, revoke)
		revokeDone <- perr
	}()
	if perr := <-revokeDone; perr != nil {
		t.Fatalf("account revoke: %+v", perr)
	}
	close(releaseLookup)

	if perr := <-launchDone; perr == nil ||
		perr.Code != protocol.CodeDenied ||
		!strings.Contains(perr.Message, "has not shared") {
		t.Fatalf("launch after completed account revoke = %+v, want account denial", perr)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("scheduler calls after denied launch = %v, want none", calls)
	}
}

func TestLaunchAdmissionSerializesAccountRevoke(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	started, release := e.runs.blockLaunch()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	launchParams := launchParams(t, e.ws.ID, e.member.ID)
	revokeParams := accountParams(t, grantee.ID)

	launchDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.runLaunch(ctx, grantee.ID, launchParams)
		launchDone <- perr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("launch did not reach scheduler admission")
	}

	revokeDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.accountRevoke(ctx, e.member.ID, revokeParams)
		revokeDone <- perr
	}()
	select {
	case perr := <-revokeDone:
		t.Fatalf("account revoke returned before launch admission: %+v", perr)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	if perr := <-launchDone; perr != nil {
		t.Fatalf("launch: %+v", perr)
	}
	if perr := <-revokeDone; perr != nil {
		t.Fatalf("account revoke after launch: %+v", perr)
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

// A completed account-share revoke must win before retained-run admission's
// fresh authorization check; the scheduler must never see the denied run.
func TestRelaunchRejectsCompletedAccountRevokeBeforeFreshCheck(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	if err := e.store.UpdateRunStatus(ctx, e.run.ID, domain.RunMerged, "closed; retained container", nil, nil); err != nil {
		t.Fatalf("mark run retained: %v", err)
	}
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatalf("share account: %v", err)
	}
	params, err := json.Marshal(protocol.RunIDParams{RunID: string(e.run.ID)})
	if err != nil {
		t.Fatal(err)
	}
	revokeParams := accountParams(t, grantee.ID)
	releaseLookup := make(chan struct{})
	lookupStarted := make(chan struct{})
	e.srv.cfg.Store = &relaunchRunLookupGate{
		Store:   e.store,
		started: lookupStarted,
		release: releaseLookup,
	}

	relaunchDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.dispatch(ctx, grantee.ID, protocol.MethodRunRelaunch, params)
		relaunchDone <- perr
	}()
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("relaunch guard did not resolve its target")
	}

	revoked := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.dispatch(ctx, e.member.ID, protocol.MethodAccountRevoke, revokeParams)
		revoked <- perr
	}()
	select {
	case perr := <-revoked:
		if perr != nil {
			t.Fatalf("account revoke: %+v", perr)
		}
	case <-time.After(time.Second):
		t.Fatal("account revoke did not complete before fresh relaunch check")
	}
	close(releaseLookup)

	if perr := <-relaunchDone; perr == nil ||
		perr.Code != protocol.CodeDenied ||
		!strings.Contains(perr.Message, "has not shared") {
		t.Fatalf("relaunch after completed account revoke = %+v, want account denial", perr)
	}
	if calls := e.runs.Calls(); len(calls) != 0 {
		t.Fatalf("scheduler calls after denied relaunch = %v, want none", calls)
	}
}

// Once retained-run admission enters Runs.Relaunch, a steering-policy revoke
// waits for that admission boundary instead of returning while it is in flight.
func TestRelaunchAdmissionSerializesWorkspaceSteerRevocation(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	if err := e.store.UpdateRunStatus(ctx, e.run.ID, domain.RunMerged, "closed; retained container", nil, nil); err != nil {
		t.Fatalf("mark run retained: %v", err)
	}
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(ctx, e.member.ID, grantee.ID); err != nil {
		t.Fatalf("share account: %v", err)
	}
	params, err := json.Marshal(protocol.RunIDParams{RunID: string(e.run.ID)})
	if err != nil {
		t.Fatal(err)
	}
	started, release := e.runs.blockRelaunch()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	relaunchDone := make(chan *protocol.Error, 1)
	go func() {
		_, perr := e.srv.runRelaunch(ctx, grantee.ID, params)
		relaunchDone <- perr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("relaunch did not reach scheduler admission")
	}

	settingsParams, err := json.Marshal(protocol.WorkspaceSettingsParams{
		WorkspaceID: string(e.ws.ID), SteerOthers: domain.SteerOthersAdminsOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{})
	settingsDone := make(chan *protocol.Error, 1)
	go func() {
		close(attempted)
		_, perr := e.srv.workspaceSettings(ctx, e.member.ID, settingsParams)
		settingsDone <- perr
	}()
	<-attempted
	select {
	case perr := <-settingsDone:
		t.Fatalf("workspace steer revoke returned before relaunch admission: %+v", perr)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	if perr := <-relaunchDone; perr != nil {
		t.Fatalf("relaunch: %+v", perr)
	}
	if perr := <-settingsDone; perr != nil {
		t.Fatalf("workspace settings: %+v", perr)
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
