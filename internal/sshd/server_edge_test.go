package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// An admin on an approved connection gets a claim code bound to their own
// member, and claiming with it binds the account to that admin. A
// collaborator, a device awaiting approval and an owned server get none.
func TestServerEdgeClaimCode(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	admin := controlClient(t, e)
	method := protocol.MethodServerEdgeClaimCode
	var pe *protocol.Error
	if err := admin.Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("claim code with the edge off = %v, want CodeUnavailable", err)
	}
	edge := &fakeEdgeOwner{state: edgeproto.StateClaimed}
	e.srv.SetEdgeOwner(edge)

	bobKey, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	if err := controlAs(t, e, bobKey).Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator claim code = %v, want CodeDenied", err)
	}
	if err := identities(t, e).BindIdentity(ctx, &domain.Identity{Member: e.member.ID, Provider: "github", Subject: "2002", Login: "admin-alt"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.DeviceStatus{domain.DevicePending, domain.DeviceRegistered} {
		dev := &domain.Device{Member: e.member.ID, Provider: "github", Subject: "2002",
			Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: string(status), Status: status}
		if err := identities(t, e).RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		if _, perr := e.srv.dispatch(withDevice(e.member.ID, dev), e.member.ID, method, json.RawMessage(`{}`)); perr == nil ||
			perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "needs an approved device") {
			t.Fatalf("claim code from a %s device = %v, want CodeDenied", status, perr)
		}
	}
	issued := func() domain.MemberID {
		edge.mu.Lock()
		defer edge.mu.Unlock()
		return edge.issuedFor
	}
	if got := issued(); got != "" {
		t.Fatalf("a refused call issued a code for %s", got)
	}

	var res protocol.ServerEdgeClaimCodeResult
	if err := admin.Call(method, struct{}{}, &res); err != nil || res.Code == "" || res.ExpiresAt == "" {
		t.Fatalf("admin claim code = %+v, %v", res, err)
	}
	if got := issued(); got != e.member.ID {
		t.Fatalf("code issued for %q, want the calling admin %s", got, e.member.ID)
	}
	codes := newFakeClaimCode()
	codes.admin = string(issued())
	laptop := newSigner(t)
	c, banner, err := e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, claimUser(t, res.Code, octo))
	if err != nil {
		t.Fatalf("claim with the issued code: %v (banner %q)", err, banner)
	}
	t.Cleanup(func() { _ = c.Close() })
	if m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); err != nil || m.ID != e.member.ID || m.Role != domain.RoleAdmin {
		t.Fatalf("claimed account is %+v, %v; want the admin %s", m, err, e.member.ID)
	}
	if deviceStatus(t, e, laptop) != domain.DeviceApproved {
		t.Fatal("the claiming device is not approved")
	}

	e.srv.SetEdgeOwner(&fakeEdgeOwner{owner: &octo, state: edgeproto.StateClaimed})
	if err := admin.Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
		!strings.Contains(pe.Message, "already owned at its edge by github octo") {
		t.Fatalf("claim code on an owned server = %v, want CodeInvalidState", err)
	}
}
