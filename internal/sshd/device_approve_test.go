package sshd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/3xDevOps/Aether/internal/domain"
	edgeproto "github.com/3xDevOps/Aether/internal/edge/proto"
	"github.com/3xDevOps/Aether/internal/events"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// An admin approves a device by its id alone, after member.device.list
// or member.device.lookup by id showed whom it admits; a connection that
// signed in with a pending device cannot. Recording and approving the
// device are each published.
func TestApproveDeviceByID(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: "octo", Role: domain.RoleAdmin})
	approveAsAdmin(t, e, e.refusedForApproval(t, octo, newSigner(t), "phone"))
	laptop := newSigner(t)
	e.refusedForApproval(t, octo, laptop, "laptop")
	pending, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}

	sub, err := e.bus.Subscribe(ctx, events.SubscribeOptions{Filter: events.Filter{Types: []events.Type{events.TypeMemberDevice}}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close() //nolint:errcheck
	inviteAccount(t, e, domain.Invitation{Provider: "github", Login: mallory().Login, Role: domain.RoleCollaborator})
	invitee := newSigner(t)
	e.refusedForApproval(t, mallory(), invitee, "mallory-laptop")
	waiting, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(invitee.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(protocol.MemberDeviceApproveParams{DeviceID: string(waiting.ID)})
	if _, perr := e.srv.dispatch(withDevice(pending.Member, pending), pending.Member, protocol.MethodMemberDeviceApprove, raw); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("approve by id from a pending device = %v, want CodeDenied", perr)
	}

	admin := controlClient(t, e)
	var listed protocol.MemberDeviceListResult
	if err = admin.Call(protocol.MethodMemberDeviceList, nil, &listed); err != nil {
		t.Fatal(err)
	}
	admits := map[string]protocol.Device{}
	for _, d := range listed.Devices {
		admits[d.ID] = d
	}
	if d := admits[string(waiting.ID)]; d.DisplayName != "" || d.Role != string(domain.RoleCollaborator) {
		t.Fatalf("listed invitation device = %+v, want a new collaborator", d)
	}
	if d := admits[string(pending.ID)]; d.DisplayName != octo.Name || d.Role != string(domain.RoleAdmin) {
		t.Fatalf("listed member device = %+v, want octo's admin member", d)
	}

	var found protocol.MemberDeviceLookupResult
	if err = admin.Call(protocol.MethodMemberDeviceLookup, protocol.MemberDeviceLookupParams{DeviceID: string(waiting.ID)}, &found); err != nil ||
		found.Device.ID != string(waiting.ID) || found.MemberID != "" || found.Role != string(domain.RoleCollaborator) {
		t.Fatalf("lookup by id = %+v, %v; want the invitation device admitting a new collaborator", found, err)
	}
	if err = admin.Call(protocol.MethodMemberDeviceApprove, protocol.MemberDeviceApproveParams{DeviceID: string(waiting.ID)}, nil); err != nil {
		t.Fatalf("approve by id: %v", err)
	}
	m, err := identities(t, e).GetMemberByIdentity(ctx, mallory().Provider, mallory().Subject)
	if err != nil || m.Role != domain.RoleCollaborator {
		t.Fatalf("mallory after approval = %+v, %v; want a collaborator", m, err)
	}

	for _, want := range []events.MemberDevicePayload{
		{DeviceID: waiting.ID, Status: domain.DevicePending},
		{DeviceID: waiting.ID, Status: domain.DeviceApproved},
	} {
		select {
		case ev := <-sub.Events():
			if ev.Payload != want || ev.WorkspaceID != events.ServerScope {
				t.Fatalf("member.device = %+v in %s, want %+v in %s", ev.Payload, ev.WorkspaceID, want, events.ServerScope)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no member.device event for %+v", want)
		}
	}
}

func TestMemberApprovesOwnDeviceByID(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ctx := context.Background()
	approved := func(account edgeproto.Account, label string) *domain.Device {
		t.Helper()
		inviteAccount(t, e, domain.Invitation{Provider: "github", Login: account.Login, Role: domain.RoleCollaborator})
		key := newSigner(t)
		approveAsAdmin(t, e, e.refusedForApproval(t, account, key, label))
		dev, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(key.PublicKey()))
		if err != nil {
			t.Fatal(err)
		}
		return dev
	}
	phone, malloryPhone := approved(octo, "phone"), approved(mallory(), "mallory-phone")
	laptop := newSigner(t)
	e.refusedForApproval(t, octo, laptop, "laptop")
	pending, err := identities(t, e).GetDeviceByCredential(ctx, edgeproto.DeviceKeyLine(laptop.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.MemberDeviceApproveParams{DeviceID: string(pending.ID)})

	if _, perr := e.srv.dispatch(withDevice(malloryPhone.Member, malloryPhone), malloryPhone.Member, protocol.MethodMemberDeviceApprove, raw); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("collaborator approving another member's device by id = %v, want CodeDenied", perr)
	}
	if got := deviceStatus(t, e, laptop); got != domain.DevicePending {
		t.Fatalf("octo's laptop after mallory's attempt = %s, want pending", got)
	}
	if _, perr := e.srv.dispatch(withDevice(phone.Member, phone), phone.Member, protocol.MethodMemberDeviceApprove, raw); perr != nil {
		t.Fatalf("member approving their own device by id: %v", perr)
	}
	if got := deviceStatus(t, e, laptop); got != domain.DeviceApproved {
		t.Fatalf("octo's laptop after octo approved it = %s, want approved", got)
	}
}
