package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/protocol"
	"github.com/3xDevOps/Aether/internal/quota"
)

type usageReaderStub struct {
	mu        sync.Mutex
	providers []quota.Provider
	err       error
	calls     int
	started   chan struct{}
	release   chan struct{}
}

func (r *usageReaderStub) Read(_ context.Context, _ domain.MemberID, _ bool) ([]quota.Provider, error) {
	r.mu.Lock()
	r.calls++
	if r.started != nil && r.calls == 1 {
		close(r.started)
	}
	release := r.release
	providers := r.providers
	err := r.err
	r.mu.Unlock()
	if release != nil {
		<-release
	}
	return providers, err
}

func (r *usageReaderStub) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func usageParams(t *testing.T, params protocol.AccountUsageParams) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func usageProviderFixture() []quota.Provider {
	checked := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	updated := checked.Add(-time.Minute)
	reset := checked.Add(time.Hour)
	return []quota.Provider{{
		Provider: "claude", Status: "ok", Plan: "pro",
		UpdatedAt: &updated, CheckedAt: checked,
		Windows: []quota.Window{{ID: "five_hour", Label: "5-hour", UsedPercent: 12.5, ResetsAt: &reset}},
	}}
}

func installUsageStub(e *testEnv, reader QuotaReader) {
	e.srv.cfg.Services.Usage = reader
}

func dispatchUsage(t *testing.T, e *testEnv, member domain.MemberID, params json.RawMessage) (protocol.AccountUsageResult, *protocol.Error) {
	t.Helper()
	raw, perr := e.srv.dispatch(context.Background(), member, protocol.MethodAccountUsage, params)
	if perr != nil {
		return protocol.AccountUsageResult{}, perr
	}
	var result protocol.AccountUsageResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode account.usage result: %v", err)
	}
	return result, nil
}

func TestAccountUsageSelfAndSharedAccount(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	reader := &usageReaderStub{providers: usageProviderFixture()}
	installUsageStub(e, reader)

	self, perr := dispatchUsage(t, e, e.member.ID, nil)
	if perr != nil {
		t.Fatalf("self account.usage: %+v", perr)
	}
	if self.AccountMemberID != string(e.member.ID) || len(self.Providers) != 1 {
		t.Fatalf("self result = %+v", self)
	}
	if self.Providers[0].Windows == nil || self.Providers[0].Windows[0].ResetsAt == nil {
		t.Fatalf("self windows lost optional reset: %+v", self.Providers)
	}
	if self.Providers[0].CheckedAt != "2026-09-18T12:00:00Z" || *self.Providers[0].UpdatedAt != "2026-09-18T11:59:00Z" {
		t.Fatalf("self timestamps = %+v", self.Providers[0])
	}

	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(context.Background(), e.member.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	shared, perr := dispatchUsage(t, e, grantee.ID, usageParams(t, protocol.AccountUsageParams{AccountMemberID: string(e.member.ID)}))
	if perr != nil {
		t.Fatalf("shared account.usage: %+v", perr)
	}
	if shared.AccountMemberID != string(e.member.ID) || reader.Calls() != 2 {
		t.Fatalf("shared result/calls = %+v/%d", shared, reader.Calls())
	}
}

func TestAccountUsageDeniesUnsharedAdminAndPendingOwnerBeforeRead(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	reader := &usageReaderStub{providers: usageProviderFixture()}
	installUsageStub(e, reader)

	_, owner := addMember(t, e, "Owner", domain.RoleCollaborator, false)
	_, perr := dispatchUsage(t, e, e.member.ID, usageParams(t, protocol.AccountUsageParams{AccountMemberID: string(owner.ID)}))
	if perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("unshared admin account.usage = %+v, want denied", perr)
	}
	if reader.Calls() != 0 {
		t.Fatalf("unshared request reached provider %d times", reader.Calls())
	}

	_, pending := addMember(t, e, "Pending", domain.RoleCollaborator, true)
	_, perr = dispatchUsage(t, e, e.member.ID, usageParams(t, protocol.AccountUsageParams{AccountMemberID: string(pending.ID)}))
	if perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("pending owner account.usage = %+v, want denied", perr)
	}
	if reader.Calls() != 0 {
		t.Fatalf("pending request reached provider %d times", reader.Calls())
	}
}

func TestAccountUsageDeniesPendingCallerAndMalformedRequest(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	reader := &usageReaderStub{providers: usageProviderFixture()}
	installUsageStub(e, reader)

	_, pending := addMember(t, e, "Pending", domain.RoleCollaborator, true)
	_, perr := dispatchUsage(t, e, pending.ID, nil)
	if perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("pending caller account.usage = %+v, want denied", perr)
	}
	if reader.Calls() != 0 {
		t.Fatalf("pending caller reached provider %d times", reader.Calls())
	}

	_, perr = dispatchUsage(t, e, e.member.ID, json.RawMessage(`{"refresh":`))
	if perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("malformed account.usage = %+v, want invalid params", perr)
	}
	if reader.Calls() != 0 {
		t.Fatalf("malformed request reached provider %d times", reader.Calls())
	}
}

func TestAccountUsageRevocationAfterReadCannotReturnData(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	_, grantee := addMember(t, e, "Grace", domain.RoleCollaborator, false)
	if err := e.store.ShareAccount(context.Background(), e.member.ID, grantee.ID); err != nil {
		t.Fatal(err)
	}
	reader := &usageReaderStub{
		providers: usageProviderFixture(),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	installUsageStub(e, reader)

	result := make(chan *protocol.Error, 1)
	go func() {
		_, perr := dispatchUsage(t, e, grantee.ID, usageParams(t, protocol.AccountUsageParams{AccountMemberID: string(e.member.ID)}))
		result <- perr
	}()
	<-reader.started
	if _, perr := e.srv.accountRevoke(context.Background(), e.member.ID, accountParams(t, grantee.ID)); perr != nil {
		t.Fatalf("revoke account: %+v", perr)
	}
	close(reader.release)
	if perr := <-result; perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("revoked account.usage = %+v, want denied", perr)
	}
}

func TestAccountUsageUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	_, perr := dispatchUsage(t, e, e.member.ID, nil)
	if perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("missing usage service = %+v, want unavailable", perr)
	}
}

func TestAccountUsageDoesNotEchoProviderError(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	reader := &usageReaderStub{err: errors.New("provider body Authorization: Bearer super-secret")}
	installUsageStub(e, reader)
	_, perr := dispatchUsage(t, e, e.member.ID, nil)
	if perr == nil || perr.Code != protocol.CodeUnavailable {
		t.Fatalf("provider failure = %+v, want unavailable", perr)
	}
	if perr.Message != "account.usage: usage providers unavailable" {
		t.Fatalf("provider failure message = %q", perr.Message)
	}
}

func TestAccountUsageSerializesEmptyArrays(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	installUsageStub(e, &usageReaderStub{providers: []quota.Provider{{Provider: "claude", Status: "unsupported", CheckedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}}})
	raw, perr := e.srv.dispatch(context.Background(), e.member.ID, protocol.MethodAccountUsage, nil)
	if perr != nil {
		t.Fatalf("account.usage: %+v", perr)
	}
	if !strings.Contains(string(raw), `"providers":[`) || !strings.Contains(string(raw), `"windows":[]`) {
		t.Fatalf("account.usage arrays were not serialized: %s", raw)
	}
}
